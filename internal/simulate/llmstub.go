package simulate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

// StubOptions configures LLM stub generation.
type StubOptions struct {
	Description string   // "Add CBOR method to Context"
	TypeName    string   // "*Context" (receiver type, if method)
	Siblings    []string // existing methods on the same type
	ModulePath  string   // "github.com/gin-gonic/gin"
}

// StubResult is a parsed definition shape from an LLM-generated stub.
type StubResult struct {
	Name       string   `json:"name"`
	Receiver   string   `json:"receiver"`
	Signature  string   `json:"signature"`
	Body       string   `json:"body"`       // raw Go source
	References []string `json:"references"` // identifiers referenced in body
}

// getAPIKey returns the API key from environment, or empty string.
func getAPIKey() string {
	if key := os.Getenv("PLANCHECK_API_KEY"); key != "" {
		return key
	}
	return os.Getenv("ANTHROPIC_API_KEY")
}

// LLMAvailable returns true if an API key is configured and LLM stubs aren't disabled.
func LLMAvailable() bool {
	if os.Getenv("PLANCHECK_NO_LLM") == "1" {
		return false
	}
	return getAPIKey() != ""
}

// GenerateStub uses the Claude API to generate a definition shape.
// Returns nil, nil if no API key is available (graceful fallback).
func GenerateStub(opts StubOptions) (*StubResult, error) {
	key := getAPIKey()
	if key == "" {
		return nil, nil
	}

	prompt := buildStubPrompt(opts)
	source, _, err := callClaude(key, prompt)
	if err != nil {
		return nil, fmt.Errorf("LLM stub generation failed: %w", err)
	}

	return parseGoStub(source)
}

// EnsembleRef tracks a reference seen across multiple stub generations.
type EnsembleRef struct {
	Name       string  `json:"name"`
	Agreement  float64 `json:"agreement"`  // fraction of stubs that reference this (0.0-1.0)
	Count      int     `json:"count"`      // how many stubs reference this
	Confidence string  `json:"confidence"` // "high" (>0.6), "moderate" (>0.3), "low"
}

// EnsembleResult is the output of probabilistic stub generation.
type EnsembleResult struct {
	Stubs      int           `json:"stubs"`      // number of stubs generated
	References []EnsembleRef `json:"references"` // references with agreement scores
	Variance   float64       `json:"variance"`   // ref count variance across stubs (0 = all agree)
}

// GenerateStubEnsemble generates N stubs at temperature 0.7 and computes
// reference agreement. References that appear in >60% of stubs are high
// confidence. This gives a probability distribution over what the new
// code will reference, replacing the single-stub all-or-nothing approach.
//
// Returns nil, nil if no API key available or ensemble disabled.
func GenerateStubEnsemble(opts StubOptions, n int) (*EnsembleResult, error) {
	if os.Getenv("PLANCHECK_NO_ENSEMBLE") == "1" {
		return nil, nil
	}
	key := getAPIKey()
	if key == "" {
		return nil, nil
	}
	if n <= 0 {
		n = 3 // default: 3 stubs balances cost vs signal
	}
	if n > 5 {
		n = 5
	}

	prompt := buildStubPrompt(opts)
	temp := 0.7

	// Generate N stubs
	refCounts := make(map[string]int)
	generated := 0

	for i := 0; i < n; i++ {
		source, _, err := callClaudeWithTemp(key, prompt, "", &temp)
		if err != nil {
			continue
		}
		stub, err := parseGoStub(source)
		if err != nil || stub == nil {
			continue
		}
		generated++
		for _, ref := range stub.References {
			refCounts[ref]++
		}
	}

	if generated == 0 {
		return nil, fmt.Errorf("all %d stub generations failed", n)
	}

	// Build agreement-scored references
	var refs []EnsembleRef
	for name, count := range refCounts {
		agreement := float64(count) / float64(generated)
		conf := "low"
		if agreement > 0.6 {
			conf = "high"
		} else if agreement > 0.3 {
			conf = "moderate"
		}
		refs = append(refs, EnsembleRef{
			Name:       name,
			Agreement:  agreement,
			Count:      count,
			Confidence: conf,
		})
	}

	// Sort by agreement descending
	sort.Slice(refs, func(i, j int) bool {
		return refs[i].Agreement > refs[j].Agreement
	})

	// Compute variance: sum of Bernoulli variances across refs.
	// High variance = stubs disagree on what this definition references.
	variance := 0.0
	for _, ref := range refs {
		p := ref.Agreement
		variance += p * (1 - p)
	}

	return &EnsembleResult{
		Stubs:      generated,
		References: refs,
		Variance:   variance,
	}, nil
}

// callClaudeWithTokens calls Claude with configurable max tokens and temperature.
func callClaudeWithTokens(key, prompt, model string, temp *float64, maxTokens int) (string, apiUsage, error) {
	if model == "" {
		model = os.Getenv("PLANCHECK_LLM_MODEL")
		if model == "" {
			model = "claude-haiku-4-5-20251001"
		}
	}
	if maxTokens <= 0 {
		maxTokens = 500
	}

	reqBody := claudeRequest{
		Model:       model,
		MaxTokens:   maxTokens,
		Messages:    []claudeMessage{{Role: "user", Content: prompt}},
		Temperature: temp,
	}

	jsonBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", apiUsage{}, err
	}

	req, err := http.NewRequest("POST", "https://api.anthropic.com/v1/messages", bytes.NewReader(jsonBytes))
	if err != nil {
		return "", apiUsage{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", key)
	req.Header.Set("anthropic-version", "2023-06-01")

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", apiUsage{}, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", apiUsage{}, err
	}

	var result claudeResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return "", apiUsage{}, fmt.Errorf("parse response: %w", err)
	}
	if result.Error != nil {
		return "", apiUsage{}, fmt.Errorf("API error: %s", result.Error.Message)
	}
	if len(result.Content) == 0 {
		return "", apiUsage{}, fmt.Errorf("empty response")
	}

	text := result.Content[0].Text
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "```go") {
		text = strings.TrimPrefix(text, "```go")
		if idx := strings.LastIndex(text, "```"); idx >= 0 {
			text = text[:idx]
		}
	} else if strings.HasPrefix(text, "```") {
		text = strings.TrimPrefix(text, "```")
		if idx := strings.LastIndex(text, "```"); idx >= 0 {
			text = text[:idx]
		}
	}
	return strings.TrimSpace(text), result.Usage, nil
}

// callClaudeWithTemp calls Claude with optional temperature override.
func callClaudeWithTemp(key, prompt, model string, temp *float64) (string, apiUsage, error) {
	return callClaudeWithTokens(key, prompt, model, temp, 500)
}

func buildStubPrompt(opts StubOptions) string {
	var b strings.Builder
	b.WriteString("Generate a Go function stub. The body doesn't need to compile — just show which other functions and types this would reference.\n\n")
	if opts.ModulePath != "" {
		b.WriteString(fmt.Sprintf("Package: %s\n", opts.ModulePath))
	}
	if opts.TypeName != "" {
		b.WriteString(fmt.Sprintf("Type: %s\n", opts.TypeName))
		if len(opts.Siblings) > 0 {
			b.WriteString(fmt.Sprintf("Existing methods: %s\n", strings.Join(opts.Siblings, ", ")))
		}
	}
	b.WriteString(fmt.Sprintf("Task: %s\n\n", opts.Description))
	b.WriteString("Generate only the function. No explanation, no package declaration, no imports.")
	return b.String()
}

type claudeRequest struct {
	Model       string           `json:"model"`
	MaxTokens   int              `json:"max_tokens"`
	Messages    []claudeMessage  `json:"messages"`
	Temperature *float64         `json:"temperature,omitempty"`
}

type claudeMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type claudeResponse struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Usage apiUsage `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// apiUsage captures token usage from the Anthropic API response.
type apiUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

func callClaude(key, prompt string) (string, apiUsage, error) {
	model := os.Getenv("PLANCHECK_LLM_MODEL")
	if model == "" {
		model = "claude-sonnet-4-6"
	}
	return callClaudeWithModel(key, prompt, model)
}

func callClaudeWithModel(key, prompt, model string) (string, apiUsage, error) {

	reqBody := claudeRequest{
		Model:     model,
		MaxTokens: 500,
		Messages: []claudeMessage{
			{Role: "user", Content: prompt},
		},
	}

	jsonBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", apiUsage{}, err
	}

	req, err := http.NewRequest("POST", "https://api.anthropic.com/v1/messages", bytes.NewReader(jsonBytes))
	if err != nil {
		return "", apiUsage{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", key)
	req.Header.Set("anthropic-version", "2023-06-01")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", apiUsage{}, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", apiUsage{}, err
	}

	var result claudeResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return "", apiUsage{}, fmt.Errorf("parse response: %w", err)
	}

	if result.Error != nil {
		return "", apiUsage{}, fmt.Errorf("API error: %s", result.Error.Message)
	}

	if len(result.Content) == 0 {
		return "", apiUsage{}, fmt.Errorf("empty response")
	}

	// Extract the Go code from the response
	text := result.Content[0].Text
	// Strip markdown code fences if present
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "```go") {
		text = strings.TrimPrefix(text, "```go")
		if idx := strings.LastIndex(text, "```"); idx >= 0 {
			text = text[:idx]
		}
	} else if strings.HasPrefix(text, "```") {
		text = strings.TrimPrefix(text, "```")
		if idx := strings.LastIndex(text, "```"); idx >= 0 {
			text = text[:idx]
		}
	}

	return strings.TrimSpace(text), result.Usage, nil
}

func parseGoStub(source string) (*StubResult, error) {
	// Wrap in package declaration for parsing
	src := "package stub\n\n" + source

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "stub.go", src, parser.AllErrors)
	if err != nil {
		// Try to extract what we can even with parse errors
		f, _ = parser.ParseFile(fset, "stub.go", src, parser.AllErrors|parser.SkipObjectResolution)
		if f == nil {
			return nil, fmt.Errorf("cannot parse stub: %w", err)
		}
	}

	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}

		result := &StubResult{
			Name: fn.Name.Name,
			Body: source,
		}

		// Extract receiver
		if fn.Recv != nil && len(fn.Recv.List) > 0 {
			recvType := fn.Recv.List[0].Type
			result.Receiver = exprToString(recvType)
		}

		// Extract signature
		if fn.Type.Params != nil {
			result.Signature = fieldsToString(fn.Type.Params)
		}

		// Walk body for referenced identifiers (filter out builtins and locals)
		if fn.Body != nil {
			refs := make(map[string]bool)
			locals := localNames(fn)
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.SelectorExpr:
					// Selector expressions like c.Render, echo.Context
					if ident, ok := x.X.(*ast.Ident); ok {
						sel := x.Sel.Name
						if !locals[ident.Name] {
							refs[ident.Name+"."+sel] = true
						} else {
							// c.Render → just "Render" (method call on local var)
							refs[sel] = true
						}
					}
				case *ast.Ident:
					name := x.Name
					if !locals[name] && !isBuiltin(name) && len(name) > 1 {
						refs[name] = true
					}
				}
				return true
			})
			for ref := range refs {
				result.References = append(result.References, ref)
			}
		}

		return result, nil
	}

	return nil, fmt.Errorf("no function declaration found in stub")
}

// localNames returns parameter names, receiver names, and the function name.
func localNames(fn *ast.FuncDecl) map[string]bool {
	locals := map[string]bool{fn.Name.Name: true}
	if fn.Recv != nil {
		for _, f := range fn.Recv.List {
			for _, n := range f.Names {
				locals[n.Name] = true
			}
		}
	}
	if fn.Type.Params != nil {
		for _, f := range fn.Type.Params.List {
			for _, n := range f.Names {
				locals[n.Name] = true
			}
		}
	}
	if fn.Type.Results != nil {
		for _, f := range fn.Type.Results.List {
			for _, n := range f.Names {
				locals[n.Name] = true
			}
		}
	}
	return locals
}

func isBuiltin(name string) bool {
	builtins := map[string]bool{
		"nil": true, "true": true, "false": true, "err": true, "error": true,
		"string": true, "int": true, "bool": true, "byte": true, "any": true,
		"make": true, "len": true, "cap": true, "append": true, "copy": true,
		"close": true, "delete": true, "new": true, "panic": true, "recover": true,
		"print": true, "println": true, "fmt": true,
	}
	return builtins[name]
}

func exprToString(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.StarExpr:
		return "*" + exprToString(t.X)
	case *ast.Ident:
		return t.Name
	default:
		return ""
	}
}

func fieldsToString(fields *ast.FieldList) string {
	if fields == nil || len(fields.List) == 0 {
		return "()"
	}
	var parts []string
	for _, f := range fields.List {
		typeName := exprToString(f.Type)
		for _, name := range f.Names {
			parts = append(parts, name.Name+" "+typeName)
		}
		if len(f.Names) == 0 {
			parts = append(parts, typeName)
		}
	}
	return "(" + strings.Join(parts, ", ") + ")"
}
