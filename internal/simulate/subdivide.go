// subdivide.go implements recursive sub-plan simulation.
//
// When forward and backward scouts meet in the middle with a gap,
// the gap is subdivided and each sub-segment gets its own simulation.
// This is the reference-graph-grounded version of the skill's
// bidirectional subdivision.
//
// The key insight: each sub-segment has its own blast radius. A plan
// that modifies A→B→C→D can be subdivided at B-C, and the blast
// radius of A→B and C→D computed independently. If A→B has 5 callers
// and C→D has 50, the model should spend more verification effort
// on C→D.
package simulate

import (
	"fmt"
	"sort"

	"github.com/justinstimatze/defn/graph"
)

// SubPlan represents a segment of a larger plan with its own blast radius.
type SubPlan struct {
	StartStep   int         `json:"startStep"`
	EndStep     int         `json:"endStep"`
	Mutations   []Mutation  `json:"mutations"`
	BlastRadius int         `json:"blastRadius"`    // production callers
	TestCoverage int        `json:"testCoverage"`
	Risk        string      `json:"risk"`           // high, moderate, low
	Suggestion  string      `json:"suggestion"`     // what to do about this segment
}

// SubdivisionResult is the output of recursive plan subdivision.
type SubdivisionResult struct {
	Segments      []SubPlan `json:"segments"`
	TotalSteps    int       `json:"totalSteps"`
	HighRiskCount int       `json:"highRiskCount"`
	Suggestion    string    `json:"suggestion"`
}

// Subdivide splits a plan's mutations into segments and simulates each.
// Returns segments sorted by risk (highest first) so the model knows
// where to focus verification effort.
func Subdivide(g *graph.Graph, mutations []Mutation, totalSteps int) SubdivisionResult {
	result := SubdivisionResult{TotalSteps: totalSteps}

	if len(mutations) == 0 {
		result.Suggestion = "No mutations to simulate."
		return result
	}

	// If few mutations, simulate each independently
	if len(mutations) <= 3 {
		for i, m := range mutations {
			step := simulateStep(g, m)
			risk := classifyRisk(step.ProductionCallers, step.TestCoverage)
			segment := SubPlan{
				StartStep:    i + 1,
				EndStep:      i + 1,
				Mutations:    []Mutation{m},
				BlastRadius:  step.ProductionCallers,
				TestCoverage: step.TestCoverage,
				Risk:         risk,
				Suggestion:   riskSuggestion(risk, step),
			}
			result.Segments = append(result.Segments, segment)
			if risk == "high" {
				result.HighRiskCount++
			}
		}
	} else {
		// Split into halves recursively
		mid := len(mutations) / 2
		firstHalf := mutations[:mid]
		secondHalf := mutations[mid:]

		// Simulate each half
		firstResult := simulateGroup(g, firstHalf)
		secondResult := simulateGroup(g, secondHalf)

		firstRisk := classifyRisk(firstResult.prodCallers, firstResult.testCoverage)
		secondRisk := classifyRisk(secondResult.prodCallers, secondResult.testCoverage)

		result.Segments = append(result.Segments, SubPlan{
			StartStep:    1,
			EndStep:      mid,
			Mutations:    firstHalf,
			BlastRadius:  firstResult.prodCallers,
			TestCoverage: firstResult.testCoverage,
			Risk:         firstRisk,
			Suggestion:   fmt.Sprintf("Steps 1-%d: %s", mid, riskLabel(firstRisk)),
		})
		result.Segments = append(result.Segments, SubPlan{
			StartStep:    mid + 1,
			EndStep:      len(mutations),
			Mutations:    secondHalf,
			BlastRadius:  secondResult.prodCallers,
			TestCoverage: secondResult.testCoverage,
			Risk:         secondRisk,
			Suggestion:   fmt.Sprintf("Steps %d-%d: %s", mid+1, len(mutations), riskLabel(secondRisk)),
		})

		if firstRisk == "high" {
			result.HighRiskCount++
		}
		if secondRisk == "high" {
			result.HighRiskCount++
		}

		// If either half is high risk and has >2 mutations, subdivide further
		if firstRisk == "high" && len(firstHalf) > 2 {
			sub := Subdivide(g, firstHalf, mid)
			// Replace the first segment with its subdivisions
			result.Segments = append(sub.Segments, result.Segments[1:]...)
			result.HighRiskCount += sub.HighRiskCount - 1 // don't double-count
		}
		if secondRisk == "high" && len(secondHalf) > 2 {
			sub := Subdivide(g, secondHalf, len(secondHalf))
			// Adjust step numbers for the second half
			for i := range sub.Segments {
				sub.Segments[i].StartStep += mid
				sub.Segments[i].EndStep += mid
			}
			result.Segments = append(result.Segments[:len(result.Segments)-1], sub.Segments...)
			result.HighRiskCount += sub.HighRiskCount - 1
		}
	}

	// Sort by risk (high first)
	sort.Slice(result.Segments, func(i, j int) bool {
		return riskOrder(result.Segments[i].Risk) > riskOrder(result.Segments[j].Risk)
	})

	// Overall suggestion
	if result.HighRiskCount == 0 {
		result.Suggestion = "All segments are low-to-moderate risk. Standard verification sufficient."
	} else {
		result.Suggestion = fmt.Sprintf("%d high-risk segment(s) found. Focus verification on these — they have the largest blast radius.",
			result.HighRiskCount)
	}

	return result
}

type groupResult struct {
	prodCallers  int
	testCoverage int
}

func simulateGroup(g *graph.Graph, mutations []Mutation) groupResult {
	var res groupResult
	for _, m := range mutations {
		step := simulateStep(g, m)
		res.prodCallers += step.ProductionCallers
		res.testCoverage += step.TestCoverage
	}
	return res
}

func classifyRisk(prodCallers, testCoverage int) string {
	if prodCallers >= 10 || testCoverage >= 30 {
		return "high"
	}
	if prodCallers >= 3 || testCoverage >= 10 {
		return "moderate"
	}
	return "low"
}

func riskSuggestion(risk string, step StepResult) string {
	dn := displayName(step.Mutation)
	switch risk {
	case "high":
		return fmt.Sprintf("%s has high blast radius (%d callers, %d tests) — verify thoroughly, consider narrowing scope",
			dn, step.ProductionCallers, step.TestCoverage)
	case "moderate":
		return fmt.Sprintf("%s has moderate blast radius (%d callers, %d tests) — standard verification",
			dn, step.ProductionCallers, step.TestCoverage)
	default:
		return fmt.Sprintf("%s has low blast radius (%d callers, %d tests) — minimal verification needed",
			dn, step.ProductionCallers, step.TestCoverage)
	}
}

func riskLabel(risk string) string {
	switch risk {
	case "high":
		return "high risk — focus verification here"
	case "moderate":
		return "moderate risk — standard verification"
	default:
		return "low risk — minimal verification"
	}
}

func riskOrder(risk string) int {
	switch risk {
	case "high":
		return 3
	case "moderate":
		return 2
	default:
		return 1
	}
}
