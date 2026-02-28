# Contributing to plancheck

## Development setup

```bash
# Prerequisites
go install github.com/justinstimatze/defn@latest

# Build and test
go build -o plancheck .
go test ./...
go vet ./...

# Set up for Claude Code integration
defn init .
./plancheck setup
./plancheck doctor
```

## Project structure

```
cmd/                    CLI commands + MCP handlers
  mcp.go                MCP server registration
  mcp_suggest.go        suggest() tool (live copilot)
  mcp_check.go          check_plan handler
  review.go             plancheck review command
  setup.go              one-command onboarding
internal/
  plan/                 Probe orchestrator
    check.go            Main Check() pipeline
    rank.go             Confidence gate + file ranking
    keywords.go         Keyword-dir matching, test-patch extraction
  simulate/             LLM spike + compiler analysis
    agent.go            Tool-using agent (code/impact/constructors/read/find)
    spike.go            Spike orchestration + scoring
    buildcheck.go       go build -overlay probing
    obligations.go      Type-level obligation extraction
  refgraph/             defn reference graph bridge
  comod/                Git co-modification analysis
  doltutil/             Shared Dolt query utility
  types/                All shared types (no import cycles)
  history/              JSONL persistence
  signals/              Informational probes
scripts/
  system_benchmark.py   A/B benchmark with caching
```

## Key conventions

- **All shared types in `internal/types/`** — prevents import cycles
- **Every package has a `// Package foo ...` doc comment** on its primary .go file
- **`filepath.IsAbs()` before `filepath.Join(cwd, f)`** — mixed abs/rel paths cause bugs
- **MCP handlers call Go functions directly** — no subprocess spawning
- **File permissions: 0o600/0o700** — user-private
- **grep uses `-F` (fixed strings)** when input is LLM-controlled
- **defn is required** — plancheck errors if `.defn/` is missing

## Running benchmarks

```bash
# Requires PLANCHECK_API_KEY or ANTHROPIC_API_KEY in .env
export $(grep -v '^#' .env | xargs)

# Full mode (spike + structural)
python3 scripts/system_benchmark.py --repo cli --limit 50

# Cross-repo (rebench-V2 format)
python3 scripts/system_benchmark.py --repo nats-io/nats-server --limit 37

# Suggest-only mode (zero LLM cost)
python3 scripts/system_benchmark.py --repo cli --limit 50 --suggest-only

# A-condition results are cached in ~/.plancheck/datasets/bench-cache/
# Use --no-cache to force fresh A-condition calls
```

## Adding a new signal source

1. Compute the signal in `internal/plan/check.go` (before the spike if it feeds into domain hints, after if it uses spike results)
2. Add it to the ranked file list via `typeflow.DiscoveredFile` or directly to `verifiedFiles`
3. Give it a source tag (e.g., `"my-signal"`) for the confidence gate
4. The confidence gate in `internal/plan/rank.go` requires spike + structural intersection — single-source signals won't surface alone
5. Test with `python3 scripts/system_benchmark.py --repo cli --limit 10`

## Adding a new agent tool

1. Define the tool schema in `internal/simulate/agent.go` (in the tools list, gated by `graph != nil` if it needs defn)
2. Add the handler case in `executeTool()`
3. Implement the tool function (e.g., `toolConstructors()`)
4. Track exploration signals in the exploration loop if the tool reveals non-plan files
5. Add the tool name to the scoring map in the exploration signal section

## Pull requests

- Run `go test ./...` and `go vet ./...`
- Test manually with `plancheck review HEAD~1` on a real project
- If changing the spike or scoring, run a 10-task benchmark to check for regressions
