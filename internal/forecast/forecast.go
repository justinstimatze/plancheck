// Package forecast implements Monte Carlo forecasting for plan outcomes.
//
// Instead of learning weights or optimizing thresholds, this package
// samples from historical outcomes of similar plans to produce a
// probability distribution of results.
//
// Methodology (same as ActionableAgile / Vacanti / Magennis):
// 1. Find historical calibration entries with similar properties
// 2. Sample randomly from their actual recall/precision distributions
// 3. Output probability distribution at P50/P85/P95
//
// No optimization, no overfitting. Just: "here's what happened on
// similar plans, here's what will probably happen on yours."
package forecast

import (
	"fmt"
	"math"
	"math/rand"
	"sort"
)

// PlanProperties describes the structural characteristics of a plan.
type PlanProperties struct {
	Complexity   int     // max(steps, filesToModify + filesToCreate)
	TestDensity  float64 // fraction of definitions that are tests (0.0-1.0)
	BlastRadius  int     // total production callers across modified definitions
	FileCount    int     // number of files in filesToModify
	CascadeDepth int     // depth of ripple chain (0 = no cascade, 4 = deep)
	CascadeFiles int     // unique files in ripple chain
	ChainDepth   int     // stub reference chain depth (0-2)
}

// HistoricalOutcome is one data point from past plan executions.
type HistoricalOutcome struct {
	Recall       float64 // what fraction of affected files were predicted
	Precision    float64 // what fraction of predictions were correct
	Outcome      string  // "clean", "rework", "failed"
	Complexity   int
	TestDensity  float64
	BlastRadius  int
	CascadeDepth int // 0 if not computed
	CascadeFiles int // 0 if not computed
}

// Forecast is the Monte Carlo output.
type Forecast struct {
	// Input properties
	Properties PlanProperties `json:"properties"`

	// Sample info
	MatchingHistorical int `json:"matchingHistorical"` // how many similar plans found
	Simulations        int `json:"simulations"`

	// Recall distribution
	RecallP50  float64 `json:"recallP50"`
	RecallP85  float64 `json:"recallP85"`
	RecallP95  float64 `json:"recallP95"`
	RecallMean float64 `json:"recallMean"`

	// Outcome probabilities
	PClean  float64 `json:"pClean"`  // P(clean execution)
	PRework float64 `json:"pRework"` // P(minor rework needed)
	PFailed float64 `json:"pFailed"` // P(significant rework)

	// Human-readable
	Summary string `json:"summary"`
}

// Run performs Monte Carlo forecasting for a plan.
func Run(props PlanProperties, history []HistoricalOutcome, nSim int) Forecast {
	if nSim <= 0 {
		nSim = 10000
	}

	f := Forecast{
		Properties:  props,
		Simulations: nSim,
	}

	// Find similar historical plans (fuzzy matching on properties)
	similar := findSimilar(props, history)
	f.MatchingHistorical = len(similar)

	if len(similar) < 3 {
		// Not enough data — use all history as fallback
		similar = history
		if len(similar) < 3 {
			f.Summary = "Insufficient historical data for forecasting (need 3+ similar plans)"
			return f
		}
	}

	// Cascade risk adjustment: deep non-converged cascades reduce expected recall.
	// A plan touching infrastructure (cascade depth 3+, 20+ files) is structurally
	// riskier than one with shallow or no cascade, even if historical complexity matches.
	cascadePenalty := 0.0
	if props.CascadeDepth >= 3 && props.CascadeFiles >= 20 {
		cascadePenalty = 0.15 // significant: 15pp recall penalty
	} else if props.CascadeDepth >= 2 && props.CascadeFiles >= 10 {
		cascadePenalty = 0.08 // moderate: 8pp recall penalty
	} else if props.CascadeDepth >= 1 && props.CascadeFiles >= 5 {
		cascadePenalty = 0.03 // mild: 3pp recall penalty
	}

	// Monte Carlo: sample from similar outcomes
	recalls := make([]float64, nSim)
	cleanCount, reworkCount, failedCount := 0, 0, 0

	for i := 0; i < nSim; i++ {
		// Sample a random historical outcome
		idx := rand.Intn(len(similar))
		sample := similar[idx]

		// Apply cascade penalty to recall estimate
		recall := sample.Recall - cascadePenalty
		if recall < 0 {
			recall = 0
		}
		recalls[i] = recall

		// Classify outcome based on adjusted recall
		switch {
		case recall >= 0.8:
			cleanCount++
		case recall >= 0.4:
			reworkCount++
		default:
			failedCount++
		}
	}

	// If we have explicit outcome labels, use those instead
	hasLabels := false
	for _, s := range similar {
		if s.Outcome != "" {
			hasLabels = true
			break
		}
	}
	if hasLabels {
		cleanCount, reworkCount, failedCount = 0, 0, 0
		for i := 0; i < nSim; i++ {
			sample := similar[rand.Intn(len(similar))]
			switch sample.Outcome {
			case "clean":
				cleanCount++
			case "rework":
				reworkCount++
			case "failed":
				failedCount++
			default:
				// Use recall-based classification
				switch {
				case sample.Recall >= 0.8:
					cleanCount++
				case sample.Recall >= 0.4:
					reworkCount++
				default:
					failedCount++
				}
			}
		}
	}

	// Compute percentiles
	sort.Float64s(recalls)
	f.RecallP50 = percentile(recalls, 0.50)
	f.RecallP85 = percentile(recalls, 0.85)
	f.RecallP95 = percentile(recalls, 0.95)
	f.RecallMean = mean(recalls)

	// Outcome probabilities
	n := float64(nSim)
	f.PClean = round2(float64(cleanCount) / n)
	f.PRework = round2(float64(reworkCount) / n)
	f.PFailed = round2(float64(failedCount) / n)

	// Summary
	f.Summary = buildSummary(f, props)

	return f
}

// findSimilar returns historical outcomes with similar properties.
// Uses fuzzy matching: complexity within 50%, test density within 20pp,
// blast radius within 2x, cascade depth within 1 level.
func findSimilar(props PlanProperties, history []HistoricalOutcome) []HistoricalOutcome {
	var similar []HistoricalOutcome

	for _, h := range history {
		// Complexity: within 50% or within 3
		complexityClose := abs(h.Complexity-props.Complexity) <= 3 ||
			(props.Complexity > 0 && float64(abs(h.Complexity-props.Complexity))/float64(props.Complexity) < 0.5)

		// Test density: within 20 percentage points
		densityClose := math.Abs(h.TestDensity-props.TestDensity) < 0.20

		// Blast radius: within 2x (or both small)
		blastClose := (props.BlastRadius <= 5 && h.BlastRadius <= 10) ||
			(props.BlastRadius > 0 && float64(h.BlastRadius)/float64(props.BlastRadius) > 0.3 &&
				float64(h.BlastRadius)/float64(props.BlastRadius) < 3.0)

		// Cascade similarity: prefer plans with similar cascade characteristics.
		// Deep cascades (depth 3+) are a different beast than shallow ones.
		cascadeClose := true
		if props.CascadeDepth > 0 && h.CascadeDepth > 0 {
			cascadeClose = abs(h.CascadeDepth-props.CascadeDepth) <= 1
		}

		if complexityClose && densityClose && cascadeClose {
			similar = append(similar, h)
		} else if complexityClose && blastClose && cascadeClose {
			similar = append(similar, h)
		}
	}

	return similar
}

func buildSummary(f Forecast, props PlanProperties) string {
	confidence := "low"
	if f.MatchingHistorical >= 20 {
		confidence = "high"
	} else if f.MatchingHistorical >= 5 {
		confidence = "moderate"
	}

	riskLevel := "low"
	if f.PFailed > 0.3 {
		riskLevel = "high"
	} else if f.PRework > 0.5 {
		riskLevel = "moderate"
	}

	return fmt.Sprintf(
		"Forecast (confidence: %s, based on %d similar plans): "+
			"%.0f%% chance of clean execution, %.0f%% minor rework, %.0f%% significant rework. "+
			"Expected recall: %.0f%% (P85: %.0f%%). Risk level: %s.",
		confidence, f.MatchingHistorical,
		f.PClean*100, f.PRework*100, f.PFailed*100,
		f.RecallMean*100, f.RecallP85*100, riskLevel)
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(p * float64(len(sorted)-1))
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return round2(sorted[idx])
}

func mean(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	sum := 0.0
	for _, v := range vals {
		sum += v
	}
	return round2(sum / float64(len(vals)))
}

func round2(f float64) float64 {
	return math.Round(f*100) / 100
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
