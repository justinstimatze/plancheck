# plancheck

Plan verification + prediction engine for AI coding agents.

**Language: Go.** Pure Go project (`go.mod`: `github.com/justinstimatze/plancheck`). The `.ts` files under `testdata/fixtures/` are test fixtures simulating target projects — not part of plancheck's source.

## Requires [defn](https://github.com/justinstimatze/defn)

Run `defn init .` in the target project. `plancheck review` and `plancheck check` error if `.defn/` is missing.

## Architecture

- **Layer 1**: Implementation spike (LLM agent explores + implements) + exploration signals
- **Layer 2**: Deterministic probes (compiler via `go build -overlay`, reference graph, git comod)
- **Layer 3**: Confidence gate (require spike + structural signal intersection)

## Three product surfaces

- **`plancheck check <plan.json>`** — pre-flight plan verification (spike + structural, ~$0.05-0.15/task est.)
- **`plancheck review [base_ref]`** — post-hoc git change analysis (zero LLM cost, seconds)
- **`suggest` MCP tool** — live mid-implementation navigation (zero LLM cost, instant)

## Key packages

- `internal/types/` — all shared types (no import cycles)
- `internal/plan/` — probe orchestrator (`Check()`)
- `internal/plan/rank.go` — confidence gate, tiered structural evidence
- `internal/plan/keywords.go` — keyword-dir matching, test-patch dir extraction
- `internal/simulate/agent.go` — tool-using agent spike, code/impact/constructors tools
- `internal/simulate/spike.go` — spike orchestration, diff-aware scoring
- `internal/simulate/buildcheck.go` — `go build -overlay`, blast radius probing
- `internal/comod/` — git co-modification analysis
- `internal/refgraph/` — defn reference graph bridge + embedded Dolt queries
- `cmd/` — CLI commands + MCP handlers
- `cmd/mcp_suggest.go` — suggest() MCP tool

## Running

```bash
go test ./...
go build -o plancheck .
./plancheck check testdata/fixtures/complete-plan.json --cwd testdata/fixtures/sample-project
./plancheck review HEAD~3
```

## Conventions

- All shared types in `internal/types/` to avoid import cycles
- Every package has a `// Package foo ...` doc comment on its primary .go file
- `filepath.IsAbs()` check before `filepath.Join(cwd, f)`
- MCP server calls Go functions directly — no subprocess spawning
- File permissions: 0o600/0o700 (user-private)
- mcp-go: use `Enum()` not `WithEnum()` for enum constraints
- grep uses `-F` (fixed strings) when input is LLM-controlled
