// calibration.go tracks prediction accuracy over time per project.
//
// Each check_plan + validate_execution cycle produces a calibration data point:
// what did we predict vs what actually happened. Over time, this builds a
// per-project model of which types of predictions are accurate.
package history

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
)

const calibrationFile = "calibration.json"

// CalibrationEntry records one prediction→outcome comparison.
type CalibrationEntry struct {
	CheckID        string   `json:"checkId"`
	Timestamp      string   `json:"timestamp"`
	PredictedFiles []string `json:"predictedFiles"`
	ActualFiles    []string `json:"actualFiles"`
	Recall         float64  `json:"recall"`
	Precision      float64  `json:"precision"`
	BrierScore     float64  `json:"brierScore"`
	BlastRadius    int      `json:"blastRadius"`       // production callers predicted
	TestCoverage   int      `json:"testCoverage"`      // tests predicted
	Confidence     string   `json:"confidence"`        // high/moderate/low
	Outcome        string   `json:"outcome,omitempty"` // clean/rework/failed (if known)
}

// CalibrationStore holds accumulated prediction accuracy data for a project.
type CalibrationStore struct {
	Entries []CalibrationEntry `json:"entries"`
	Summary CalibrationSummary `json:"summary"`
}

// CalibrationSummary is the aggregate accuracy for a project.
type CalibrationSummary struct {
	TotalPredictions int     `json:"totalPredictions"`
	AvgRecall        float64 `json:"avgRecall"`
	AvgPrecision     float64 `json:"avgPrecision"`
	AvgBrier         float64 `json:"avgBrier"`
	HitRate          float64 `json:"hitRate"` // fraction where recall > 0
	// Breakdown by confidence level
	HighConfRecall float64 `json:"highConfRecall,omitempty"`
	ModConfRecall  float64 `json:"modConfRecall,omitempty"`
	LowConfRecall  float64 `json:"lowConfRecall,omitempty"`
}

// LoadCalibration loads the calibration store for a project.
func LoadCalibration(cwd string) *CalibrationStore {
	path := filepath.Join(ProjectDirFn(cwd), calibrationFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return &CalibrationStore{}
	}
	var store CalibrationStore
	if json.Unmarshal(data, &store) != nil {
		return &CalibrationStore{}
	}
	return &store
}

// AppendCalibration adds a new calibration entry and updates the summary.
func AppendCalibration(cwd string, entry CalibrationEntry) error {
	store := LoadCalibration(cwd)
	store.Entries = append(store.Entries, entry)

	// Keep last 100 entries
	if len(store.Entries) > 100 {
		store.Entries = store.Entries[len(store.Entries)-100:]
	}

	store.Summary = computeSummary(store.Entries)

	path := filepath.Join(ProjectDirFn(cwd), calibrationFile)
	data, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// GetCalibrationSummary returns the current calibration summary for a project.
func GetCalibrationSummary(cwd string) CalibrationSummary {
	store := LoadCalibration(cwd)
	if len(store.Entries) == 0 {
		return CalibrationSummary{}
	}
	return store.Summary
}

func computeSummary(entries []CalibrationEntry) CalibrationSummary {
	if len(entries) == 0 {
		return CalibrationSummary{}
	}

	// Recency-weighted average (newer entries count more).
	// Old learnings from failed tasks can poison future predictions.
	// Weight recent entries 2x vs oldest entries.
	s := CalibrationSummary{TotalPredictions: len(entries)}
	hits := 0
	var highRecalls, modRecalls, lowRecalls []float64
	totalWeight := 0.0

	for i, e := range entries {
		// Linear weight: oldest entry = 1.0, newest = 2.0
		weight := 1.0 + float64(i)/float64(len(entries))
		totalWeight += weight

		s.AvgRecall += e.Recall * weight
		s.AvgPrecision += e.Precision * weight
		s.AvgBrier += e.BrierScore * weight
		if e.Recall > 0 {
			hits++
		}
		switch e.Confidence {
		case "high":
			highRecalls = append(highRecalls, e.Recall)
		case "moderate":
			modRecalls = append(modRecalls, e.Recall)
		case "low":
			lowRecalls = append(lowRecalls, e.Recall)
		}
	}

	s.AvgRecall /= totalWeight
	s.AvgPrecision /= totalWeight
	s.AvgBrier /= totalWeight
	s.HitRate = float64(hits) / float64(len(entries))

	// Round to 3 decimal places
	s.AvgRecall = math.Round(s.AvgRecall*1000) / 1000
	s.AvgPrecision = math.Round(s.AvgPrecision*1000) / 1000
	s.AvgBrier = math.Round(s.AvgBrier*1000) / 1000
	s.HitRate = math.Round(s.HitRate*1000) / 1000

	if len(highRecalls) > 0 {
		sum := 0.0
		for _, r := range highRecalls {
			sum += r
		}
		s.HighConfRecall = math.Round(sum/float64(len(highRecalls))*1000) / 1000
	}
	if len(modRecalls) > 0 {
		sum := 0.0
		for _, r := range modRecalls {
			sum += r
		}
		s.ModConfRecall = math.Round(sum/float64(len(modRecalls))*1000) / 1000
	}
	if len(lowRecalls) > 0 {
		sum := 0.0
		for _, r := range lowRecalls {
			sum += r
		}
		s.LowConfRecall = math.Round(sum/float64(len(lowRecalls))*1000) / 1000
	}

	return s
}
