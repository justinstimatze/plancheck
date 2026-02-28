# LLM-Generated Definition Stubs

## Overview

When plancheck simulates a plan, it needs definition shapes (name, signature,
receiver, references) for definitions that don't exist yet. Two sources:

1. **Heuristic inference** (default, no API key): analyze sibling definitions
   on the same type, copy their reference patterns. Works well when the new
   definition follows an existing pattern (CBOR method following JSON/XML).

2. **LLM inference** (opt-in, requires API key): ask the model to generate
   a plausible function body, extract structural metadata from the AST.
   Better for novel definitions where no sibling pattern exists.

## When LLM stubs add value

| Scenario | Heuristic | LLM | Winner |
|----------|-----------|-----|--------|
| New method on existing type (CBOR on *Context) | Good — siblings provide pattern | Better — knows CBOR semantics | LLM (slight) |
| New function in existing package | Weak — no siblings | Good — understands package context | LLM (clear) |
| New type + methods | Fails — no pattern | Good — generates coherent type | LLM (clear) |
| Refactor (rename, move) | N/A — definition exists | N/A | Neither |
| Bug fix (behavior change) | N/A — definition exists | N/A | Neither |

LLM stubs only matter for **additions** — new definitions that don't exist
yet. For modifications to existing code, the reference graph already has
the structural information.

## Pipeline design

### Environment variable

```bash
export PLANCHECK_API_KEY="sk-ant-..."  # Claude API key
# or
export ANTHROPIC_API_KEY="sk-ant-..."  # standard env var, used as fallback
```

No key = heuristic inference only. No degradation, no error. The tool works
fine without it — LLM stubs are a quality improvement, not a requirement.

### Flow

```
Plan step: "Add CBOR method to Context"
    │
    ▼
Has API key? ──no──▶ Heuristic: find siblings, copy reference pattern
    │                          │
    yes                        ▼
    │                   Simulate with inferred shape
    ▼
Generate stub prompt:
  "You are generating a Go function stub for plan simulation.
   Generate ONLY the function signature and a rough body that
   shows which other functions/types this would reference.
   The body does NOT need to compile — just show the structural
   relationships.

   Context: gin-gonic/gin
   Existing siblings on *Context: JSON, XML, YAML, HTML (all call Render)
   Task: Add CBOR method to Context

   Generate:"
    │
    ▼
LLM response:
  func (c *Context) CBOR(code int, obj any) {
      c.Render(code, render.CBOR{Data: obj})
  }
    │
    ▼
Parse AST → extract:
  name: CBOR
  receiver: *Context
  signature: (code int, obj any)
  references: [Render, render.CBOR]
    │
    ▼
Simulate with extracted shape
```

### Implementation

```go
// internal/simulate/llmstub.go

// GenerateStub uses the Claude API to generate a definition shape.
// Returns nil if no API key is available (graceful fallback).
func GenerateStub(ctx context.Context, opts StubOptions) (*StubResult, error) {
    key := os.Getenv("PLANCHECK_API_KEY")
    if key == "" {
        key = os.Getenv("ANTHROPIC_API_KEY")
    }
    if key == "" {
        return nil, nil  // no key = graceful fallback, not error
    }

    prompt := buildStubPrompt(opts)
    body := callClaude(key, prompt)
    return parseGoStub(body)
}

type StubOptions struct {
    Description string   // "Add CBOR method to Context"
    TypeName    string   // "*Context" (if method)
    Siblings    []string // ["JSON", "XML", "YAML"] (existing methods)
    ModulePath  string   // "github.com/gin-gonic/gin"
}

type StubResult struct {
    Name       string
    Receiver   string
    Signature  string
    Body       string   // raw Go source (may not compile)
    References []string // extracted from AST walk
}
```

### Prompt design

The prompt should be minimal — we need structural shape, not working code:

```
Generate a Go function stub. The body doesn't need to compile — just
show which other functions and types this would reference.

Package: {module_path}
{if receiver}
Type: {receiver}
Existing methods: {siblings}
{endif}
Task: {description}

Generate only the function. No explanation.
```

### API call

```go
func callClaude(key, prompt string) string {
    // Use Claude Haiku for speed + cost
    // This is structural inference, not creative writing
    body := map[string]any{
        "model":      "claude-haiku-4-5-20251001",
        "max_tokens": 500,
        "messages": []map[string]string{
            {"role": "user", "content": prompt},
        },
    }
    // ... standard HTTP POST to api.anthropic.com/v1/messages
}
```

### AST parsing

```go
func parseGoStub(source string) (*StubResult, error) {
    // Wrap in package declaration if needed
    src := "package stub\n" + source
    fset := token.NewFileSet()
    f, err := parser.ParseFile(fset, "stub.go", src, parser.AllErrors)
    if err != nil {
        // Partial parse is fine — extract what we can
    }

    // Walk AST for function declarations
    for _, decl := range f.Decls {
        if fn, ok := decl.(*ast.FuncDecl); ok {
            result := &StubResult{
                Name: fn.Name.Name,
            }
            if fn.Recv != nil {
                // Extract receiver type
            }
            // Walk body for identifiers → references
            ast.Inspect(fn.Body, func(n ast.Node) bool {
                if ident, ok := n.(*ast.Ident); ok {
                    result.References = append(result.References, ident.Name)
                }
                return true
            })
            return result, nil
        }
    }
    return nil, fmt.Errorf("no function declaration found")
}
```

### Cost estimate

- Claude Haiku: ~$0.25/MTok input, ~$1.25/MTok output
- Typical prompt: ~200 tokens input, ~100 tokens output
- Per stub: ~$0.00018
- Per plan check (5 stubs max): ~$0.001
- Monthly (50 plan checks/day): ~$1.50

Negligible cost. Haiku is the right model — this is structural inference.

### Testing strategy

**Phase 1: Manual validation (no API key needed)**
- We ARE an LLM. In this conversation, generate stubs for 10 SWE-smith-go
  tasks manually, feed them to simulate, compare structural predictions
  against gold patches.
- Question: do LLM-generated stubs improve recall over heuristic inference?

**Phase 2: Batch backtesting (needs API key)**
- For each SWE-smith-go task with an "addition" mutation:
  1. Generate stub via API
  2. Parse AST for references
  3. Simulate on Dolt branch
  4. Compare predicted blast radius against gold patch
  5. Score: LLM stub recall vs heuristic stub recall

**Phase 3: Live integration testing**
- Run plancheck with LLM stubs on real plans in Claude Code
- Measure: do simulation signals improve plan quality?
- A/B: same plan with and without LLM stubs

### What we're betting

The bet: LLM-generated definition shapes produce better structural
predictions than heuristic sibling copying, because:
- The LLM understands semantics (CBOR is a serialization format → calls Render)
- The LLM can generate novel reference patterns (new types, new packages)
- The LLM's body is wrong but the shape is right

The risk: the LLM hallucinates references that don't exist in the codebase,
producing false positive predictions. Mitigation: validate generated
references against the actual definition database before simulating.

### Configuration

```bash
# Enable LLM stubs (opt-in)
export PLANCHECK_API_KEY="sk-ant-..."

# Or use standard Anthropic env var
export ANTHROPIC_API_KEY="sk-ant-..."

# Disable LLM stubs even with key present
export PLANCHECK_NO_LLM=1

# Use a different model (default: claude-haiku-4-5-20251001)
export PLANCHECK_LLM_MODEL="claude-sonnet-4-5-20241022"
```

No config files, no setup wizard. Env var or nothing.
