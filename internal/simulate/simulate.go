// Package simulate runs plan simulations on defn reference graphs.
//
// Queries the in-memory graph (defn/graph.Graph) for blast radius, broken
// references, and affected tests. The result is a per-step ripple report
// — without running go build or go test.
//
// Mutation types:
//   - signature-change: function signature changes, all callers break
//   - behavior-change: internal logic changes, tests may break
//   - removal: definition deleted, all references break
//   - addition: new definition added, no breakage
package simulate

import (
	"fmt"
	"strings"

	"github.com/justinstimatze/defn/graph"
)

// MutationType describes how a definition is being changed.
type MutationType string

const (
	SignatureChange MutationType = "signature-change"
	BehaviorChange  MutationType = "behavior-change"
	Removal         MutationType = "removal"
	Addition        MutationType = "addition"
)

// Mutation describes a single change to a definition.
type Mutation struct {
	Type     MutationType `json:"type"`
	Name     string       `json:"name"`
	Receiver string       `json:"receiver,omitempty"`
}

// Caller is a definition that references the mutated definition.
type Caller struct {
	Name     string `json:"name"`
	Receiver string `json:"receiver,omitempty"`
	IsTest   bool   `json:"isTest"`
	RefKind  string `json:"refKind"` // call, type_ref, field_access
}

// StepResult is the ripple report for a single mutation.
type StepResult struct {
	Mutation          Mutation `json:"mutation"`
	DefinitionFound   bool     `json:"definitionFound"`
	ProductionCallers int      `json:"productionCallers"`
	TestCallers       int      `json:"testCallers"`
	TransitiveCallers  int      `json:"transitiveCallers"`
	TestCoverage      int      `json:"testCoverage"`
	TopCallers        []Caller `json:"topCallers,omitempty"`
	Impact            string   `json:"impact"` // human-readable summary
}

// Result is the complete simulation output.
type Result struct {
	Steps []StepResult `json:"steps"`
	Total struct {
		Mutations         int     `json:"mutations"`
		ProductionCallers int     `json:"productionCallers"`
		TestCoverage      int     `json:"testCoverage"`
		TestDensity       float64 `json:"testDensity"`       // % of definitions that are tests
		Confidence        string  `json:"confidence"`        // "high" (>35%), "moderate" (20-35%), "low" (<20%)
	} `json:"total"`
}

// Run executes a simulation against a defn graph.
func Run(g *graph.Graph, mutations []Mutation) (Result, error) {
	if g == nil {
		return Result{}, fmt.Errorf("no reference graph available")
	}

	var result Result
	totalProd := 0
	totalTests := 0

	for _, m := range mutations {
		step := simulateStep(g, m)
		result.Steps = append(result.Steps, step)
		totalProd += step.ProductionCallers
		totalTests += step.TestCoverage
	}

	result.Total.Mutations = len(mutations)
	result.Total.ProductionCallers = totalProd
	result.Total.TestCoverage = totalTests

	// Test density as confidence indicator
	density := graphTestDensity(g)
	result.Total.TestDensity = density
	switch {
	case density > 0.35:
		result.Total.Confidence = "high"
	case density > 0.20:
		result.Total.Confidence = "moderate"
	default:
		result.Total.Confidence = "low"
	}

	return result, nil
}

func graphTestDensity(g *graph.Graph) float64 {
	if g == nil {
		return 0
	}
	allDefs := g.AllDefs()
	if len(allDefs) == 0 {
		return 0
	}
	tests := 0
	for _, d := range allDefs {
		if d.Test {
			tests++
		}
	}
	return float64(tests) / float64(len(allDefs))
}

func simulateStep(g *graph.Graph, m Mutation) StepResult {
	step := StepResult{Mutation: m}

	// Resolve definition
	def := resolveDef(g, m.Name, m.Receiver)
	if def == nil && m.Type == Addition {
		step = simulateAddition(g, m)
		return step
	}
	if def == nil {
		step.Impact = fmt.Sprintf("definition %s not found in reference graph", displayName(m))
		return step
	}
	step.DefinitionFound = true

	// Query direct callers
	callerDefs := g.CallerDefs(def.ID)
	var prodCallers []Caller
	for _, c := range callerDefs {
		caller := Caller{
			Name:     c.Name,
			Receiver: c.Receiver,
			IsTest:   c.Test,
		}
		if c.Test {
			step.TestCallers++
		} else {
			step.ProductionCallers++
			prodCallers = append(prodCallers, caller)
		}
	}

	// Top production callers (limit 10)
	if len(prodCallers) > 10 {
		step.TopCallers = prodCallers[:10]
	} else {
		step.TopCallers = prodCallers
	}

	// Transitive callers (production only)
	transitive := g.TransitiveCallers(def.ID)
	transCount := 0
	for _, t := range transitive {
		if !t.Test {
			transCount++
		}
	}
	step.TransitiveCallers = transCount

	// Test coverage
	step.TestCoverage = len(g.Tests(def.ID))

	// Impact summary
	step.Impact = impactSummary(m, step)

	return step
}

// simulateAddition handles the Addition mutation type for definitions that
// don't exist yet. Uses LLM stub generation (if available) or sibling
// heuristic to infer what the new definition would reference, then reports
// the impact of those references.
func simulateAddition(g *graph.Graph, m Mutation) StepResult {
	step := StepResult{Mutation: m}

	// Find sibling definitions (same receiver) for context
	var siblings []string
	if m.Receiver != "" {
		for _, d := range g.AllDefs() {
			if d.Receiver == m.Receiver && !d.Test && d.Exported {
				siblings = append(siblings, d.Name)
				if len(siblings) >= 10 {
					break
				}
			}
		}
	}

	// Find module path from first definition's module
	modulePath := ""
	if defs := g.AllDefs(); len(defs) > 0 {
		modulePath = g.ModulePath(defs[0].ModuleID)
	}

	// Try LLM stub generation
	var refs []string
	var refConfidence map[string]float64
	if LLMAvailable() {
		stubOpts := StubOptions{
			Description: fmt.Sprintf("Add %s", displayName(m)),
			TypeName:    m.Receiver,
			Siblings:    siblings,
			ModulePath:  modulePath,
		}
		ensemble, err := GenerateStubEnsemble(stubOpts, 3)
		if err == nil && ensemble != nil && len(ensemble.References) > 0 {
			step.DefinitionFound = true
			refConfidence = make(map[string]float64)
			for _, ref := range ensemble.References {
				refs = append(refs, ref.Name)
				refConfidence[ref.Name] = ref.Agreement
			}
		} else {
			stub, err := GenerateStub(stubOpts)
			if err == nil && stub != nil {
				step.DefinitionFound = true
				refs = stub.References
			}
		}
	}

	// Fallback: use sibling reference patterns via graph
	if len(refs) == 0 && len(siblings) > 0 {
		step.DefinitionFound = true
		seen := make(map[string]bool)
		for _, sib := range siblings {
			sibDef := resolveDef(g, sib, m.Receiver)
			if sibDef == nil {
				continue
			}
			for _, callee := range g.CalleeDefs(sibDef.ID) {
				if !callee.Test && !seen[callee.Name] {
					seen[callee.Name] = true
					refs = append(refs, callee.Name)
					if len(refs) >= 10 {
						break
					}
				}
			}
			if len(refs) >= 10 {
				break
			}
		}
	}

	if !step.DefinitionFound {
		step.Impact = fmt.Sprintf("definition %s not found and no LLM key for stub generation", displayName(m))
		return step
	}

	// Chain through referenced definitions with depth tracking.
	seenDefs := make(map[int64]bool)
	affectedFiles := make(map[string]bool)

	var resolvedRefs []int64
	for _, ref := range refs {
		refDef := resolveDef(g, ref, "")
		if refDef == nil {
			continue
		}

		conf := 1.0
		if refConfidence != nil {
			conf = refConfidence[ref]
		}
		if conf > 0.3 {
			resolvedRefs = append(resolvedRefs, refDef.ID)
		}
		seenDefs[refDef.ID] = true

		for _, c := range g.CallerDefs(refDef.ID) {
			if c.Test {
				step.TestCallers++
			} else {
				step.ProductionCallers++
			}
		}

		if refDef.SourceFile != "" {
			affectedFiles[refDef.SourceFile] = true
		}
	}

	// Depth 1: follow outgoing references
	var depth1Refs []int64
	for _, refID := range resolvedRefs {
		for _, callee := range g.CalleeDefs(refID) {
			if seenDefs[callee.ID] {
				continue
			}
			seenDefs[callee.ID] = true
			depth1Refs = append(depth1Refs, callee.ID)
		}
	}

	// Depth 2: callers of depth-1 refs
	for _, d1ID := range depth1Refs {
		for _, c := range g.CallerDefs(d1ID) {
			if seenDefs[c.ID] {
				continue
			}
			if c.Test {
				step.TestCallers++
			} else {
				step.ProductionCallers++
				if c.SourceFile != "" {
					affectedFiles[c.SourceFile] = true
				}
			}
		}
	}

	step.TestCoverage = step.TestCallers
	chainDepth := 0
	if len(depth1Refs) > 0 {
		chainDepth = 2
	} else if len(resolvedRefs) > 0 {
		chainDepth = 1
	}
	step.Impact = fmt.Sprintf("%s addition: references %d definitions, chain depth %d, %d files in scope, %d production callers, %d tests",
		displayName(m), len(refs), chainDepth, len(affectedFiles), step.ProductionCallers, step.TestCoverage)

	return step
}

// resolveDef finds a definition by name and optional receiver in the graph.
// Uses fuzzy matching: exact → name-only if unambiguous → receiver suffix → highest caller count.
func resolveDef(g *graph.Graph, name, receiver string) *graph.Def {
	allDefs := g.AllDefs()

	// Collect non-test matches by name
	var matches []*graph.Def
	for _, d := range allDefs {
		if d.Name == name && !d.Test {
			matches = append(matches, d)
		}
	}

	// Exact match (name + receiver)
	for _, d := range matches {
		if receiver != "" && d.Receiver == receiver {
			return d
		}
		if receiver == "" && d.Receiver == "" {
			return d
		}
	}

	// Name-only if unambiguous
	if len(matches) == 1 {
		return matches[0]
	}

	// Suffix match on receiver
	if receiver != "" && len(matches) > 0 {
		stripped := strings.TrimPrefix(receiver, "*")
		for _, d := range matches {
			if strings.HasSuffix(d.Receiver, stripped) {
				return d
			}
		}
	}

	// Fallback: highest caller count
	if len(matches) > 0 {
		best := matches[0]
		bestCount := len(g.CallerDefs(best.ID))
		for _, d := range matches[1:] {
			c := len(g.CallerDefs(d.ID))
			if c > bestCount {
				best = d
				bestCount = c
			}
		}
		return best
	}

	return nil
}

func impactSummary(m Mutation, step StepResult) string {
	dn := displayName(m)
	if !step.DefinitionFound {
		return fmt.Sprintf("%s not found in reference graph", dn)
	}

	switch m.Type {
	case SignatureChange:
		return fmt.Sprintf("%s signature change: %d production callers will break, %d tests cover this",
			dn, step.ProductionCallers, step.TestCoverage)
	case BehaviorChange:
		return fmt.Sprintf("%s behavior change: %d production callers unaffected syntactically, %d tests may fail",
			dn, step.ProductionCallers, step.TestCoverage)
	case Removal:
		return fmt.Sprintf("%s removal: %d production callers + %d test callers will fail to compile",
			dn, step.ProductionCallers, step.TestCallers)
	case Addition:
		return fmt.Sprintf("%s addition: no existing code breaks, %d tests in surrounding context",
			dn, step.TestCoverage)
	default:
		return fmt.Sprintf("%s: %d production callers, %d tests",
			dn, step.ProductionCallers, step.TestCoverage)
	}
}

func displayName(m Mutation) string {
	if m.Receiver != "" {
		return fmt.Sprintf("(%s).%s", m.Receiver, m.Name)
	}
	return m.Name
}

