# Integration Architecture

How plancheck, defn, and Claude Code fit together.

## The key realization

The LLM running the plan IS the LLM that generates one-shot stubs. In
Claude Code, there is no separate "stub generation API call" needed —
the model in the conversation window generates definition shapes as part
of its forward/backward tracing, and those shapes get fed to
`simulate_plan`. The `PLANCHECK_API_KEY` is only needed for offline
backtesting, not live use.

This means plancheck's three layers all flow through Claude Code's
existing context window:

```
Layer 1: Bidirectional verification (the skill)
  The model traces forward and backward through the plan.
  When it traces forward, it can generate definition shapes
  inline ("step 3 would create a function like this...").

Layer 2: Deterministic probes (check_plan MCP tool)
  Ground truth: file existence, git comod, reference graph.
  The model calls check_plan and gets findings + simulation signals.

Layer 3: Structural simulation (simulate_plan MCP tool)
  The model calls simulate_plan with typed mutations
  (or definition shapes it generated in Layer 1).
  Gets back blast radius, test coverage, confidence.
```

No API key needed. No separate LLM call. The model doing the planning
is the model doing the simulation. plancheck provides the structural
ground truth; the model provides the semantic understanding.

## How plancheck should relate to defn

### Current state

plancheck queries `.defn/` databases via `dolt sql` CLI calls in
`internal/refgraph/` and `internal/simulate/`. This works but:

- Requires `dolt` binary installed (150MB)
- Raw SQL queries are fragile and duplicated
- No access to defn's higher-level ops (fuzzy lookup, batch-impact, simulate)
- Can't use defn's MCP server from plancheck's MCP server (no MCP-to-MCP)

### Design principle: defn is an optional enhancement, not a dependency

plancheck MUST work without defn. The gate, comod analysis, file
existence checks, persona sweeps — all of this works on any git repo.
defn adds the reference graph layer, which improves predictions from
~43% recall (comod only) to ~68% recall (combined), but the tool is
useful without it.

### Three tiers of integration

**Tier 1: No defn (any project)**
- File existence checks
- Git co-modification analysis (confidence-weighted)
- Import chain detection
- Churn, test pairing, lock staleness signals
- Gate with complexity-proportional iteration
- Bidirectional verification via skill

This is plancheck today for non-Go projects or Go projects without defn.

**Tier 2: defn database present (.defn/ exists)**
- Everything in Tier 1, plus:
- Reference graph blast radius via dolt CLI queries
- Combined model (refgraph + comod)
- Simulation signals in check_plan output
- `plancheck simulate` command for manual exploration
- Test density confidence indicator
- Source-file-scoped simulation

Requires: `dolt` CLI installed, `defn init .` run once on the project.

**Tier 3: defn MCP server running (both MCP servers active)**
- Everything in Tier 2, plus:
- The model can call `code(op:"simulate", ...)` directly
- The model can call `code(op:"impact", ...)` for blast radius
- The model can call `code(op:"test-coverage", ...)` for test info
- The model can generate one-shot stubs inline and feed them to simulate
- Backward scout uses `code(op:"batch-impact")` for prerequisite analysis

This is the full experience. Both MCP servers are configured in
`.mcp.json` / `~/.claude.json`. The model uses whichever tools are
available.

### What plancheck should NOT do

- **Don't import defn as a Go library.** defn is 151MB with embedded
  Dolt. plancheck is 13MB. Keep them separate.
- **Don't require defn for any core functionality.** Every check_plan
  call must work without .defn/.
- **Don't duplicate defn's logic.** The dolt CLI queries in
  `internal/refgraph/` and `internal/simulate/` are thin wrappers,
  not reimplementations.
- **Don't MCP-to-MCP.** plancheck's MCP server shouldn't call defn's
  MCP server. The model calls both independently.

## How plancheck should integrate into Claude Code

### Current integration points

1. **MCP server** (`plancheck mcp`) — 8 tools including check_plan,
   simulate_plan, record_reflection
2. **PreToolUse hook** (`plancheck gate`) — blocks ExitPlanMode until
   sufficient verification
3. **Skill file** (`~/.claude/skills/check-plan/SKILL.md`) — prompts
   for bidirectional verification
4. **CLAUDE.md** — project-level instructions

### What should change

#### The skill should orchestrate all three layers

Currently the skill does:
1. Forward trace
2. Backward trace
3. Compare
4. Call check_plan
5. Fix findings
6. Repeat

It should do:
1. Forward trace — **generate definition shapes for new definitions**
2. Backward trace — **query prerequisites via simulate_plan or defn**
3. Compare — identify prediction errors
4. Call check_plan — get comod + refgraph + simulation signals
5. **If simulate_plan available**: call it with typed mutations from
   the plan's filesToModify
6. Fix findings
7. Repeat

The model generates stubs in steps 1-2 as part of its natural reasoning.
No special prompt engineering needed — when a model traces forward
through "step 3: add CBOR method," it naturally thinks about what that
method would look like. We just need the skill to tell it to feed that
shape to simulate_plan.

#### check_plan should surface simulation data more prominently

Currently simulation appears as signals (informational). It should be
a first-class section of the CompactCheckResult:

```json
{
  "findings": { "missingFiles": 0, "comodGaps": 3, "refGraphGaps": 1 },
  "simulation": {
    "productionCallers": 18,
    "testCoverage": 113,
    "confidence": "high",
    "highImpactDefs": ["(*Context).Render", "(*Context).JSON"]
  },
  "topFindings": [...]
}
```

#### The gate should consider blast radius

Currently the gate blocks on:
1. Missing files
2. High-confidence comod gaps
3. Insufficient check_plan rounds

It should also consider:
4. **High blast radius without acknowledgment** — if simulation shows
   >20 production callers affected and the plan doesn't acknowledge
   the scope, block with "this plan affects 20+ production callers
   across 5 files — verify the blast radius is intentional."

This is the Kelly criterion applied to the gate: block proportional
to the stakes, not just the confidence.

#### One-shot stubs should be inline, not API calls

The current `internal/simulate/llmstub.go` calls the Claude API
directly. This is correct for offline backtesting but wrong for live
use. In Claude Code, the model should:

1. Generate the stub as part of its forward trace (natural reasoning)
2. Format it as a mutation for simulate_plan
3. Call simulate_plan with the stub

The skill prompt should guide this:
```
When tracing forward through a step that creates a new definition,
generate a plausible Go function stub showing the signature and
which existing functions it would call. Then call simulate_plan
with type "addition" and the stub's name/receiver to see the
structural impact.
```

No API key needed. No separate model call. The planning model IS the
stub generator.

#### defn should be recommended, not required

The setup flow should be:
```
plancheck setup          # configures MCP, hooks, skill
plancheck doctor         # checks everything

# Optional but recommended for Go projects:
defn init .              # creates .defn/, configures defn MCP
plancheck doctor         # now also checks defn integration
```

`plancheck doctor` should report defn status:
```
  ✓ binary
  ✓ MCP server
  ✓ gate hook
  ✓ skill file
  ○ defn not configured (optional — run 'defn init .' for reference graph analysis)
```
or:
```
  ✓ defn database (.defn/ found, 280 definitions)
  ✓ defn MCP server configured
  ✓ test density: 56% (high confidence predictions)
```

## What this means for the product

plancheck is not a "plan checker." It's a **prediction engine for code
changes** that integrates into AI coding workflows at three levels:

1. **Universal** (any project, any language): forced iteration +
   file/git probes. This is the gate + comod. Works today.

2. **Enhanced** (Go projects with defn): reference graph + combined
   model + simulation. Predicts 68% of failing tests, 55% of PR file
   changes. Works today.

3. **Full** (Claude Code with both MCP servers): the model generates
   definition shapes, feeds them to simulation, gets structural
   predictions back, and uses them to improve its plan. The model and
   the tool feed each other. The model provides semantic understanding;
   the tool provides structural ground truth.

The product is level 1 for adoption, level 2 for power users, level 3
for the full experience. Each level is independently useful.
