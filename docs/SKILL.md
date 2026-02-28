---
name: check-plan
description: Adaptive mesh refinement for plans — bidirectional verification that catches what single-pass planning misses
context: fork
---

Verify plans by tracing them from both ends. Trace forward from the current state. Trace backward from the goal. Compare where they meet. If the gap is too big, pick a midpoint and repeat. Disagreements between the forward and backward traces are the findings.

Works for any plan: software, infrastructure, data pipelines, project plans, anything.

---

## Before starting — Project knowledge

Read `~/.plancheck/projects/<hash>/knowledge.md` if it exists (call `get_last_check_id` with cwd to confirm the project is known). Use it to:
- Pre-load files that are always forgotten
- Focus on known risk areas
- Skip probes known to false-positive for this project

If the file doesn't exist, proceed normally — it will be created after the first reflection.

---

## Pass 0 — Deterministic probes (code projects only)

**When to run:** The plan touches source files AND the `check_plan` MCP tool is available. Skip for non-code plans.

1. Serialize the plan as ExecutionPlan JSON. Include `semanticSuggestions` — files you think might need changing but haven't committed to:
   ```json
   "semanticSuggestions": [
     {"file": "routes.go", "confidence": 0.7, "reason": "registers the new handler"},
     {"file": "types.go", "confidence": 0.5, "reason": "may need new types"}
   ]
   ```
   plancheck validates each suggestion against the reference graph and git history. You'll see which suggestions have structural support (must/likely) vs semantic-only (consider).
2. Call `check_plan` with plan_json and cwd
3. Note the `historyId` — hold it for the reflection at the end
4. Fix any missingFiles findings before continuing
5. If `projectPatterns` contains recurring-miss files, add them to the plan or explain why they're not needed
6. **Read the forecast** — if `pFailed > 0.4`, the plan has high risk:
   - Consider subdividing into smaller increments
   - Add invariants to anchor verification
   - Focus verification on the riskiest steps
7. **Read the novelty** — if `label` is "novel" or "exploratory":
   - The reference graph can't help much (new code has no connections)
   - Look at analogy signals — cross-project patterns for similar code
   - Look at backward-scout signals — prerequisites you might be missing
   - Consider a spike on the riskiest new component
8. **Read the ranked suggestions** — these are files you probably forgot,
   scored by combined structural + comod + analogy signals. Check the top 3.

**Use probe signals during verification:**
- Churn hotspots — merge conflict risk between steps
- Test pairing — test files that need updating
- Lock staleness — missing install step
- Import chains — API compatibility between changed files
- Simulation — blast radius, test coverage, confidence (when `.defn/` exists)

**If check_plan returns simulation data** (the `simulation` field):
- Note `productionCallers` — how many production definitions are in the blast radius
- Note `testCoverage` — how many tests cover the modified definitions
- Note `confidence` — high/moderate/low based on test density
- Note `highImpactDefs` — definitions with high blast radius to focus verification on

**Context recovery:** If context was compacted and you lost the `historyId`, call `get_last_check_id` with the project's cwd to recover it.

---

## Verification algorithm

### Step 1 — Trace forward

Start from the current project state. Walk through the first few steps of the plan.

For each step, state:
- **Before**: what exists, what's true before this step
- **Action**: what this step does
- **After**: what changed, what now exists

**For steps that create new definitions** (new functions, types, methods): generate a plausible Go function stub showing the signature and which existing functions it would call. If `simulate_plan` is available, call it with `type: "addition"` and the definition's name/receiver to see the structural impact. The stub doesn't need to compile — just show the structural relationships.

Stop when you're no longer confident about what state you're in. State explicitly: "Forward trace reaches [state] after step N."

### Step 2 — Trace backward from goal

Start from the completed goal. Work backward: "The goal is done. What were the last few steps that produced it?"

For each step (in reverse), state:
- **After**: what must be true after this step
- **Action**: what this step does
- **Before**: what must be true before this step for it to succeed

Stop when the required pre-state is no longer obvious from the goal alone. State explicitly: "Backward trace requires [state] before step M."

### Step 3 — Compare

Compare where the forward trace stopped (state after step N) with where the backward trace needs to start (state before step M).

Three outcomes:

1. **They agree.** The states match. The gap between them is empty or obvious. Done.

2. **They disagree but the gap is small.** You can bridge it in a few confident steps. Write them. Done.

3. **They disagree and the gap is large.** You can't reliably trace the steps between them. **Subdivide.**

### Step 4 — Subdivide

Pick the most important intermediate milestone between the two traces — where the plan's state is most constrained (a deployment, a migration, an API boundary, a test gate).

Trace forward from this midpoint and backward to it. Now you have two smaller gaps. Compare each one. If either is still too large, subdivide again.

### Step 5 — Done

The plan is verified when every adjacent trace agrees on the state between them. Each handoff is consistent: one trace's "after" matches the next trace's "before."

**Bounds:**
- Max recursion depth: 3 (at most 8 segments)
- Simple plans (5 steps or fewer, 3 files or fewer): forward + backward + single comparison is usually enough. Don't subdivide unless the comparison genuinely fails.
- Hard cap: 5 total trace passes

**What disagreements reveal:**
- State disagreements — missing steps, wrong ordering, implicit dependencies
- File list disagreements — intermediate artifacts the plan doesn't account for
- Assumption disagreements — different traces assumed different things about the environment

These disagreements ARE the findings.

---

## Output format

**Be terse.** The tracing is internal work. The user sees:

```
[Traces]: forward (steps 1-N), backward (steps M-end), [K midpoints if any]
[Handoffs]: X states checked between traces
[Conflicts]: Y disagreements found
- [steps N-M] [disagreement] -> [fix]
[Simulation]: X production callers, Y tests, confidence Z (if available)
[Result]: Plan [verified | updated — re-checking]
```

After verification, present the final plan with all fixes applied. Note which changes came from trace disagreements vs deterministic probes.

---

## Guardrails

- If forward and backward traces agree perfectly on the first attempt for a plan with >5 steps, pause. Perfect agreement on a complex plan usually means both traces are making the same optimistic assumptions. State what assumptions they share and stress-test the most fragile one.
- The backward trace must reason from the goal, not from the forward trace. If you find yourself just extending the forward trace to the end, start over from "the goal is done."
- Subdivide at natural seams (API boundaries, deployment gates, data migrations), not at arbitrary step numbers.

---

## Post-execution reflection (automatic)

When execution completes — all tasks finished, user confirms working, or failure hit:

**First**, call `validate_execution` with the original plan JSON and `base_ref` (the commit before execution started). This closes the prediction loop — plancheck compares its simulation predictions against the actual git diff and records calibration data for future forecasts.

**Then**, call `record_reflection`:

- `id`: historyId from check_plan (if it ran), or omit
- `cwd`: project root
- `passes`: number of verification passes completed (count each forward+backward+compare as one pass; minimum 2)
- `probe_findings`: findings from deterministic probes (check_plan) that changed the plan
- `persona_findings`: findings from trace disagreements that changed the plan
- `missed`: what went wrong during execution that no pass caught (empty string if nothing)
- `outcome`: clean / rework / failed (your assessment)

Call it automatically. Do not ask the user to classify the outcome.

**After reflection, update the project's `knowledge.md`** (stored in `~/.plancheck/projects/<hash>/`). Keep it short (10-20 lines). Structure:

```markdown
# Project: <name>
## What works
- <patterns that produce useful findings for this project>
## What doesn't
- <probes/signals that false-positive here>
## Always check
- <files that are always forgotten — from recurring-miss patterns>
## Risk areas
- <places where forward and backward traces tend to disagree>
```

If the file exists, update it incrementally. If it doesn't, create it.

---

## Graceful degradation

| Condition | What degrades | What still works |
|-----------|--------------|-----------------|
| No `check_plan` MCP tool | No probes, no history | Verification passes run normally |
| No `simulate_plan` MCP tool | No simulation data | check_plan still runs, comod still works |
| No `.defn/` directory | No reference graph, no simulation | Git comod still works |
| No git history | Co-mod empty, churn/signals empty | File existence checks still run |
| Greenfield project | File existence finds nothing | Validation signals still run |
| `PLANCHECK_NOHISTORY=1` set | No history, no patterns | All probes run stateless |
| Remote/headless | History may fail to write | Probes still return results |
| Cowork (multiple agents) | Each agent runs its own fork | History is append-only JSONL |

Verification passes require nothing except the plan text and the goal. Deterministic probes require the MCP tool + a project directory. Each layer fails independently.

---

## Why this works

1. A single forward pass can't reliably simulate a 15-step plan. But it can reliably do 3-4 steps from a known state. Decompose until each piece is within range.
2. Forward and backward passes catch categorically different errors. Forward finds missing prerequisites and ordering issues. Backward finds missing arrival conditions and unstated assumptions.
3. Disagreements between independently-traced segments are real gaps, not hypothetical concerns.
4. Subdivide where the plan is uncertain, not where it's long.
5. Git history and file existence are ground truth the model can't generate on its own.
6. Every disagreement must have a fix. Identifying a gap without resolving it is noise.
