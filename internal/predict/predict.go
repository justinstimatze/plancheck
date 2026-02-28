// Package predict implements the multi-signal prediction model for code changes.
//
// Three signals are combined with learned weights to produce a probability
// for each file:
//
//	P(file needs changing) = w1*P_struct + w2*P_comod + w3*P_semantic
//
// Where:
//   - P_struct = 1.0 if reference graph connects file to plan files, 0.0 otherwise
//   - P_comod = co-change frequency from git history (0.0 to 1.0)
//   - P_semantic = model's stated confidence that file is related (0.0 to 1.0)
//
// Weights are initialized from cross-project base rates and updated per-project
// via calibration data. The combination follows Benter's combined model:
// structural analysis + crowd estimate, with weights learned from outcomes.
package predict

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
)

// Signal represents one source's prediction about a file.
type Signal struct {
	Source string  `json:"source"` // "structural", "comod", "semantic"
	File   string  `json:"file"`
	Score  float64 `json:"score"` // 0.0 to 1.0
	Reason string  `json:"reason"`
}

// FilePrediction is the combined prediction for a single file.
type FilePrediction struct {
	File       string  `json:"file"`
	Combined   float64 `json:"combined"`   // weighted aggregate probability
	Structural float64 `json:"structural"` // reference graph signal
	Comod      float64 `json:"comod"`      // git co-modification signal
	Semantic   float64 `json:"semantic"`   // LLM semantic signal
	Confidence string  `json:"confidence"` // must, likely, consider
	Reason     string  `json:"reason"`     // why this file was predicted
}

// PredictionResult is the complete output of the prediction model.
type PredictionResult struct {
	Files      []FilePrediction `json:"files"`
	Weights    Weights          `json:"weights"`
	PlanFiles  []string         `json:"planFiles"`  // files already in the plan
	TotalFiles int              `json:"totalFiles"` // files evaluated
}

// Weights holds the current signal weights.
type Weights struct {
	Structural float64 `json:"structural"`
	Comod      float64 `json:"comod"`
	Semantic   float64 `json:"semantic"`
	Source     string  `json:"source"` // "default", "base_rates", "calibrated"
}

// DefaultWeights returns the initial weights before any calibration.
// Based on our empirical validation:
//   - Structural has 100% precision but 34% recall
//   - Comod has moderate precision and adds 16.5% lift
//   - Semantic is unknown until calibrated
func DefaultWeights() Weights {
	return Weights{
		Structural: 0.50,
		Comod:      0.35,
		Semantic:   0.15,
		Source:     "default",
	}
}

// LoadWeights loads project-specific calibrated weights if available,
// otherwise returns defaults.
func LoadWeights(cwd string) Weights {
	home, err := os.UserHomeDir()
	if err != nil {
		return DefaultWeights()
	}

	// Try project-specific weights first
	projectWeightsPath := filepath.Join(home, ".plancheck", "projects", projectHash(cwd), "weights.json")
	if data, err := os.ReadFile(projectWeightsPath); err == nil {
		var w Weights
		if json.Unmarshal(data, &w) == nil && w.Structural > 0 {
			w.Source = "calibrated"
			return w
		}
	}

	// Try global base rate weights
	baseRatePath := filepath.Join(home, ".plancheck", "weights.json")
	if data, err := os.ReadFile(baseRatePath); err == nil {
		var w Weights
		if json.Unmarshal(data, &w) == nil && w.Structural > 0 {
			w.Source = "base_rates"
			return w
		}
	}

	return DefaultWeights()
}

// SaveWeights persists calibrated weights for a project.
func SaveWeights(cwd string, w Weights) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	dir := filepath.Join(home, ".plancheck", "projects", projectHash(cwd))
	_ = os.MkdirAll(dir, 0o700)
	data, err := json.MarshalIndent(w, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "weights.json"), data, 0o600)
}

// Combine takes signals from all sources and produces weighted predictions.
func Combine(signals []Signal, weights Weights, planFiles []string) PredictionResult {
	// Group signals by file
	byFile := make(map[string]map[string]Signal) // file → source → signal
	for _, s := range signals {
		if byFile[s.File] == nil {
			byFile[s.File] = make(map[string]Signal)
		}
		byFile[s.File][s.Source] = s
	}

	// Build plan file set for exclusion
	planSet := make(map[string]bool)
	for _, f := range planFiles {
		planSet[f] = true
		planSet[filepath.Base(f)] = true
	}

	// Compute combined probability for each file
	var predictions []FilePrediction
	for file, sources := range byFile {
		if planSet[file] {
			continue // already in the plan
		}

		var structural, comod, semantic float64
		var reasons []string

		if s, ok := sources["structural"]; ok {
			structural = s.Score
			reasons = append(reasons, s.Reason)
		}
		if s, ok := sources["comod"]; ok {
			comod = s.Score
			reasons = append(reasons, s.Reason)
		}
		if s, ok := sources["semantic"]; ok {
			semantic = s.Score
			reasons = append(reasons, s.Reason)
		}

		// Weighted combination
		totalWeight := weights.Structural + weights.Comod + weights.Semantic
		combined := (weights.Structural*structural + weights.Comod*comod + weights.Semantic*semantic) / totalWeight

		// Extremizing: when multiple signals agree, push toward certainty
		// (Tetlock's extremizing algorithm)
		sigCount := 0
		if structural > 0.5 {
			sigCount++
		}
		if comod > 0.3 {
			sigCount++
		}
		if semantic > 0.3 {
			sigCount++
		}
		if sigCount >= 2 {
			// Multiple signals agree → extremize upward
			combined = extremize(combined, 1.3)
		}

		confidence := classifyCombined(combined, structural)

		reason := ""
		if len(reasons) > 0 {
			reason = reasons[0]
			if len(reasons) > 1 {
				reason += fmt.Sprintf(" (+%d more signals)", len(reasons)-1)
			}
		}

		predictions = append(predictions, FilePrediction{
			File:       file,
			Combined:   round3(combined),
			Structural: round3(structural),
			Comod:      round3(comod),
			Semantic:   round3(semantic),
			Confidence: confidence,
			Reason:     reason,
		})
	}

	// Sort by combined probability descending
	sort.Slice(predictions, func(i, j int) bool {
		return predictions[i].Combined > predictions[j].Combined
	})

	return PredictionResult{
		Files:      predictions,
		Weights:    weights,
		PlanFiles:  planFiles,
		TotalFiles: len(predictions),
	}
}

// CalibrateWeights adjusts weights based on prediction outcomes.
// actual = files that actually changed, predicted = what we predicted.
// Uses gradient-free optimization: increase weight of signals that
// were correct, decrease weight of signals that were wrong.
func CalibrateWeights(current Weights, predictions []FilePrediction, actualFiles map[string]bool) Weights {
	if len(predictions) == 0 {
		return current
	}

	// For each signal, compute accuracy on this round
	structCorrect, structTotal := 0.0, 0.0
	comodCorrect, comodTotal := 0.0, 0.0
	semanticCorrect, semanticTotal := 0.0, 0.0

	for _, p := range predictions {
		actual := 0.0
		if actualFiles[p.File] {
			actual = 1.0
		}

		if p.Structural > 0 {
			structTotal++
			if (p.Structural > 0.5) == (actual > 0.5) {
				structCorrect++
			}
		}
		if p.Comod > 0 {
			comodTotal++
			if (p.Comod > 0.3) == (actual > 0.5) {
				comodCorrect++
			}
		}
		if p.Semantic > 0 {
			semanticTotal++
			if (p.Semantic > 0.3) == (actual > 0.5) {
				semanticCorrect++
			}
		}
	}

	// Adjust weights proportional to accuracy (learning rate 0.1)
	lr := 0.1
	w := current

	if structTotal > 0 {
		accuracy := structCorrect / structTotal
		w.Structural += lr * (accuracy - 0.5) // increase if >50% accurate
	}
	if comodTotal > 0 {
		accuracy := comodCorrect / comodTotal
		w.Comod += lr * (accuracy - 0.5)
	}
	if semanticTotal > 0 {
		accuracy := semanticCorrect / semanticTotal
		w.Semantic += lr * (accuracy - 0.5)
	}

	// Clamp weights to [0.05, 0.90] — no signal should dominate completely
	w.Structural = clamp(w.Structural, 0.05, 0.90)
	w.Comod = clamp(w.Comod, 0.05, 0.90)
	w.Semantic = clamp(w.Semantic, 0.05, 0.90)

	// Normalize to sum to 1
	total := w.Structural + w.Comod + w.Semantic
	w.Structural /= total
	w.Comod /= total
	w.Semantic /= total

	w.Source = "calibrated"
	return w
}

// extremize pushes a probability toward 0 or 1.
// Factor > 1 pushes away from 0.5, factor < 1 pulls toward 0.5.
// From Tetlock's extremizing algorithm for forecast aggregation.
func extremize(p float64, factor float64) float64 {
	if p <= 0 || p >= 1 {
		return p
	}
	odds := p / (1 - p)
	extremized := math.Pow(odds, factor)
	return extremized / (1 + extremized)
}

func classifyCombined(combined, structural float64) string {
	if structural >= 0.8 {
		return "must" // structural connection is near-certain
	}
	if combined >= 0.5 {
		return "likely"
	}
	if combined >= 0.2 {
		return "consider"
	}
	return "unlikely"
}

func round3(f float64) float64 {
	return math.Round(f*1000) / 1000
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func projectHash(cwd string) string {
	// Simple hash — same as history package
	// In production, import from history to avoid duplication
	sum := uint64(0)
	for _, b := range []byte(cwd) {
		sum = sum*31 + uint64(b)
	}
	return fmt.Sprintf("%016x", sum)
}
