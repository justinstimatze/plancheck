// cascade.go implements iterative ripple simulation.
//
// Given an initial set of mutations, simulate the cascading consequences:
// each mutation's broken callers need fixes, those fixes may break their
// callers, and so on until the ripples die out.
//
// This answers: "if I change X, what's the full chain of required changes?"
// Not just the blast radius, but the blast radius OF the blast radius.
package simulate

import (
	"sort"

	"github.com/justinstimatze/defn/graph"
)

// CascadeStep is one level of the ripple cascade.
type CascadeStep struct {
	Depth       int              `json:"depth"`
	Mutations   []Mutation       `json:"mutations"`
	AffectedFiles []string       `json:"affectedFiles"`
	NewBreakages int             `json:"newBreakages"` // callers broken at this depth
	Cumulative  int              `json:"cumulative"`   // total breakages so far
}

// CascadeResult is the full cascade simulation.
type CascadeResult struct {
	Steps       []CascadeStep `json:"steps"`
	MaxDepth    int           `json:"maxDepth"`
	TotalFiles  int           `json:"totalFiles"`  // unique files affected
	TotalBreaks int           `json:"totalBreaks"` // total broken callers
	Converged   bool          `json:"converged"`   // true if ripples died out before max depth
	FileChain   []string      `json:"fileChain"`   // files in order of impact
}

// Cascade simulates the ripple chain from initial mutations.
// At each depth:
//   1. Find callers of the mutated definitions
//   2. Those callers are now "broken" — they need signature/call-site updates
//   3. The fixes to those callers may break THEIR callers
//   4. Repeat until no new breakages or max depth reached
func Cascade(g *graph.Graph, initialMutations []Mutation, maxDepth int) CascadeResult {
	if g == nil {
		return CascadeResult{Converged: true}
	}
	if maxDepth <= 0 {
		maxDepth = 5
	}

	result := CascadeResult{}
	allAffectedFiles := make(map[string]bool)
	allAffectedDefs := make(map[string]bool) // "name|receiver" → seen
	cumulative := 0

	currentMutations := initialMutations

	for depth := 0; depth < maxDepth; depth++ {
		if len(currentMutations) == 0 {
			result.Converged = true
			break
		}

		step := CascadeStep{
			Depth:     depth,
			Mutations: currentMutations,
		}

		var nextMutations []Mutation
		filesAtDepth := make(map[string]bool)
		newBreaks := 0

		for _, m := range currentMutations {
			def := resolveDef(g, m.Name, m.Receiver)
			if def == nil {
				continue
			}

			for _, caller := range g.CallerDefs(def.ID) {
				if caller.Test {
					continue
				}

				key := caller.Name + "|" + caller.Receiver
				if allAffectedDefs[key] {
					continue
				}
				allAffectedDefs[key] = true
				newBreaks++

				if caller.SourceFile != "" {
					filesAtDepth[caller.SourceFile] = true
					allAffectedFiles[caller.SourceFile] = true
				}

				nextType := BehaviorChange
				if m.Type == SignatureChange || m.Type == Removal {
					nextType = SignatureChange
				}

				nextMutations = append(nextMutations, Mutation{
					Type:     nextType,
					Name:     caller.Name,
					Receiver: caller.Receiver,
				})
			}
		}

		cumulative += newBreaks
		step.NewBreakages = newBreaks
		step.Cumulative = cumulative

		for f := range filesAtDepth {
			step.AffectedFiles = append(step.AffectedFiles, f)
		}
		sort.Strings(step.AffectedFiles)

		result.Steps = append(result.Steps, step)

		// Dampen: only propagate signature changes
		var sigChangesOnly []Mutation
		for _, m := range nextMutations {
			if m.Type == SignatureChange {
				sigChangesOnly = append(sigChangesOnly, m)
			}
		}
		currentMutations = sigChangesOnly
	}

	if !result.Converged && len(currentMutations) == 0 {
		result.Converged = true
	}

	result.MaxDepth = len(result.Steps)
	result.TotalBreaks = cumulative

	for f := range allAffectedFiles {
		result.FileChain = append(result.FileChain, f)
	}
	sort.Strings(result.FileChain)
	result.TotalFiles = len(result.FileChain)

	return result
}
