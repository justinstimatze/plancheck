# Roadmap

## Current state (2026-04-06)

**180 tasks across 4 repos. 53 improved (29%), 1 worsened (0.6%).** Single-run benchmarks — see README for methodology notes.

| Repo | Tasks | Recall lift | F1 lift | Improved | Worsened |
|------|-------|------------|---------|----------|----------|
| cli/cli | 50 | **+17.7pp** | **+5.2pp** | 21 (42%) | 0 |
| revive | 45 | +10.5pp | +6.1pp | 12 (27%) | 0 |
| nats-server | 37 | +7.5pp | ~0pp | 9 (24%) | 0 |
| helm | 48 | +5.8pp | -1.6pp | 11 (23%) | 1 |

Fast mode (suggest-only, no LLM): +7.9pp recall on cli/cli (50 tasks), $0 API cost. Not yet benchmarked cross-repo.

## Three product surfaces

1. **`plancheck check <plan.json>`** — pre-flight plan verification (spike + structural, ~$0.05-0.15/task est., ~2 min)
2. **`plancheck review [base_ref]`** — post-hoc git change analysis (zero LLM cost, seconds)
3. **`suggest` MCP tool** — live mid-implementation navigation (zero LLM cost, instant)

## Onboarding (one command)

```bash
plancheck setup    # configures MCP server, hooks, skill — once per user, works forever
defn init .        # once per Go project
```

## Next up

### 1. Dogfood on real development

**Status: ready**

Everything is configured. Use plancheck review before commits, see suggest() fire during sessions. Find friction, fix it.

### 2. suggest() end-to-end agent test

**Status: planned**

Real product test: give Claude Code the suggest() MCP tool and a task. Let it implement end-to-end. Score against gold. Does suggest() help a real agent?

### 3. Adaptive spike turns

**Status: planned**

Scale exploration turns with codebase complexity (median file size). cli/cli (151-line median): 4 turns. nats-server (3311-line median): 6 turns. Could close the gap between repos.

### 4. Cost instrumentation

**Status: planned**

Track per-task: spike tokens, API cost, wall time. Quantify the value prop.

### 5. Beyond Go

**Status: future**

suggest()'s compiler signal generalizes: TypeScript (`tsc --noEmit`), Rust (`cargo check`), Python (`mypy`). Graph signal requires per-language reference extraction.

## Completed

### Signals
- [x] Domain-aware spike seeding (keyword-dir gaps)
- [x] Test-patch directory hints (first positive F1)
- [x] Exploration signal extraction (read/code/find tool calls)
- [x] code() tool via go/ast, constructors() tool
- [x] read(start_line) + code() auto-suggestion on truncation
- [x] Frequency-weighted scoring, find 2-hit filter
- [x] Targeted build-check on explored definitions
- [x] Confidence gate for suggest/review (2+ signal source intersection)

### Product
- [x] suggest() MCP tool — live copilot, zero LLM cost
- [x] plancheck review — post-hoc git change analysis
- [x] PostToolUse suggest hook (MUST CHANGE only)
- [x] One-command setup (plancheck setup)
- [x] Doctor validates full stack (MCP, hooks, defn, skill)
- [x] defn required — errors if missing
- [x] suggest-only benchmark mode
- [x] Multi-user support

### Infrastructure
- [x] Cross-repo benchmark (4 repos, 180 tasks)
- [x] Recursive spike (implemented, disabled — too slow)
- [x] Dead code removal (-715 lines), defn v0.4.1
- [x] Overlay path resolution fix

## Validated patterns

- **Tools > prompts** (AGENTbench paper, arxiv 2602.11988)
- **Implementation > reasoning** — writing code is discovery
- **Compiler > LLM for obligations** — go build -overlay near-100% precision
- **Test patches point to gold files** — pushed F1 positive
- **Domain hints from keyword-dir gaps** — grounded navigation from task text
- **Zero-cost signals are 45% as good** — compiler + graph for free
- **Confidence gate = precision** — 2+ signal sources for non-compiler suggestions
- **Signal generalizes** — 4 repos, 180 tasks, 29% improved, 0.6% worsened
- **One-command onboarding** — plancheck setup, then defn init per project
