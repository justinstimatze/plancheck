# plancheck

_Measure twice, cut once._

[![License: Apache-2.0](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](LICENSE)

plancheck predicts which files an AI coding agent will miss. It runs an implementation spike (an LLM agent that explores and prototypes the change), compiler verification (`go build -overlay`), and reference graph queries to find files the plan doesn't cover.

Go-only. Requires [defn](https://github.com/justinstimatze/defn) for reference graph queries.

**180 tasks across 4 Go repos. 53 tasks improved (29%), 1 worsened (0.6%).** <sup>[single-run benchmarks](#methodology)</sup>

## Three modes

### `plancheck check <plan.json>` — before coding

An LLM agent reads the plan files, explores the codebase with tools (definition lookup, impact analysis, grep), then writes a prototype implementation. The files it touches become predictions. Combined with compiler verification and structural signals, this produces a ranked list of files the plan is likely missing.

**+17.7pp recall on cli/cli (50 tasks, single run).** Cost estimated at ~$0.05-0.15/task (Sonnet pricing, not instrumented).

### `plancheck review [base_ref]` — after coding

Analyzes files you've already modified and suggests what you missed. Uses compiler probing (adds dummy struct fields, checks what breaks), reference graph callers, and git co-modification patterns. Zero LLM cost, runs in seconds.

```bash
plancheck review           # uncommitted changes
plancheck review HEAD~3    # last 3 commits
```

### `suggest` MCP tool — during coding

Fired automatically after Go file edits via a PostToolUse hook. "Given the files you've touched so far, what else needs to change?" Same signals as `review`, delivered as an MCP tool call. Zero LLM cost, instant.

## Requires [defn](https://github.com/justinstimatze/defn)

plancheck requires a [defn](https://github.com/justinstimatze/defn) database for reference graph queries. Run `defn init .` in your Go project first.

## Installation

```bash
go install github.com/justinstimatze/defn@latest
go install github.com/justinstimatze/plancheck@latest
```

## Setup (Claude Code)

```bash
cd your-go-project
defn init .            # create reference graph
plancheck setup        # configure MCP server, hooks, skill (once per user)
plancheck doctor       # verify everything
```

`plancheck setup` configures:
- **MCP server** — check_plan, suggest, and other tools available in all sessions
- **Gate hook** — enforces plan quality before exiting plan mode
- **Suggest hook** — shows compiler-verified suggestions after Go file edits
- **Check-plan skill** — persona-based plan verification

Setup writes hooks and MCP config pointing at `~/go/bin/plancheck`, the stable
`go install` path. This means `go install github.com/justinstimatze/plancheck@latest`
upgrades in-place — no need to re-run setup. Project-local `.mcp.json` files
should follow the same pattern: prefer `~/go/bin/defn` and `~/go/bin/plancheck`
over local dev build artifacts, otherwise stale binaries silently stick around
across version bumps.

## Signal sources

| Signal | Confidence | Source | Cost |
|--------|------------|--------|------|
| Compiler (`go build -overlay`) | Very high | Probe exported symbols, find broken callers | Free |
| Reference graph (defn) | High | Callers, callees, constructors of modified definitions | Free |
| Git co-modification | Moderate | Files that historically change together | Free |
| Implementation spike | Moderate | LLM writes prototype, discovers files through data flow | ~$0.05-0.15 est. |
| Exploration signals | Low-moderate | Files the spike agent actively investigated | Free |

_Confidence tiers are estimated from benchmark observation, not formally measured. Spike cost is estimated from Sonnet token pricing; cost instrumentation is planned but not yet built._

## Cross-repo results

| Repo | Tasks | Recall lift | F1 lift | Improved | Worsened |
|------|-------|------------|---------|----------|----------|
| cli/cli | 50 | **+17.7pp** | **+5.2pp** | 21 (42%) | 0 |
| revive | 45 | +10.5pp | +6.1pp | 12 (27%) | 0 |
| nats-server | 37 | +7.5pp | ~0pp | 9 (24%) | 0 |
| helm | 48 | +5.8pp | -1.6pp | 11 (23%) | 1 |

<a id="methodology"></a>

**Methodology notes:**
- Results are from single benchmark runs using `scripts/system_benchmark.py`, not averaged across multiple trials. Treat as point estimates.
- Repos were selected for defn compatibility (Go, reasonable size), not randomly sampled.
- "Improved/Worsened" counts per-task recall delta. A task is "improved" if plancheck's suggestions increased recall; "worsened" if they decreased it.
- helm's negative F1 means plancheck finds more files but also suggests more wrong files on that repo — likely due to its flat package structure producing noisier structural signals.
- Fast mode (suggest-only, no LLM): +7.9pp recall, 12/50 improved on cli/cli. Not yet benchmarked cross-repo.

**Glossary:**
- **Recall**: fraction of files that actually needed changing that plancheck suggested. Higher = fewer missed files.
- **Precision**: fraction of plancheck's suggestions that were correct. Higher = fewer false alarms.
- **F1**: harmonic mean of recall and precision. Balances finding files vs not suggesting wrong ones.
- **pp** (percentage points): absolute change. "+17.7pp recall" means recall went from e.g. 40% to 57.7%.
- **Recall lift / F1 lift**: how much plancheck improved that metric compared to the baseline (agent without plancheck).

## CLI commands

| Command | Description |
|---------|-------------|
| `plancheck check <plan.json>` | Full plan verification (spike + structural) |
| `plancheck review [base_ref]` | Review git changes for missing files |
| `plancheck setup` | Configure Claude Code integration |
| `plancheck doctor` | Verify configuration |
| `plancheck simulate` | Run mutations against reference graph |

## Configuration

| Variable | Default | Description |
|----------|---------|-------------|
| `PLANCHECK_NO_SPIKE` | *(unset)* | Set to `1` to skip the LLM spike (structural signals only) |
| `PLANCHECK_SPIKE_MODEL` | `claude-sonnet-4-6` | Model for the implementation spike |
| `PLANCHECK_API_KEY` | *(unset)* | Anthropic API key (for spike). Falls back to `ANTHROPIC_API_KEY` |
| `PLANCHECK_SPIKE_DEBUG` | *(unset)* | Set to `1` to print spike tool calls to stderr |

## License

[Apache-2.0](LICENSE)
