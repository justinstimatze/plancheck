// source.go builds historical outcome data for MC forecasting from
// multiple sources, prioritized by relevance:
//
// 1. Project-specific session traces (most relevant, fewest entries)
// 2. Project calibration store (from check_plan → validate_execution cycles)
// 3. Cross-project base rates from SWE-smith-go backtesting (seed data)
//
// Greenfield projects start with seed data (source 3) and accumulate
// project-specific data (sources 1-2) over time. Mature projects
// primarily use their own history with seed data as fallback for
// underrepresented plan types.
package forecast

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/justinstimatze/plancheck/internal/traces"
)

// BuildHistory assembles historical outcomes for MC forecasting from
// all available sources. Project-specific entries are duplicated to
// give them higher sampling weight — the more project history you have,
// the more the forecast reflects YOUR project, not the average.
func BuildHistory(cwd string) []HistoricalOutcome {
	var history []HistoricalOutcome

	// Source 1: Project session traces (highest priority — 3x weight)
	traceOutcomes := outcomesFromTraces(cwd)
	for i := 0; i < 3; i++ {
		history = append(history, traceOutcomes...)
	}

	// Source 2: Project calibration store (high priority — 2x weight)
	calOutcomes := outcomesFromCalibration(cwd)
	for i := 0; i < 2; i++ {
		history = append(history, calOutcomes...)
	}

	// Source 3: Cross-project seed data (baseline — 1x weight)
	// Always included so greenfield projects have something to sample from.
	// As project history grows, seed data's relative weight shrinks naturally.
	seedOutcomes := loadSeedData()
	history = append(history, seedOutcomes...)

	return history
}

// outcomesFromTraces converts session trace metrics into historical outcomes.
func outcomesFromTraces(cwd string) []HistoricalOutcome {
	pt, err := traces.IndexProject(cwd)
	if err != nil || pt.TotalSessions == 0 {
		return nil
	}

	var outcomes []HistoricalOutcome
	for _, s := range pt.Sessions {
		if s.EditCount == 0 {
			continue // skip sessions with no edits
		}

		// Exploration ratio maps inversely to recall estimate:
		// high exploration = likely missed files initially = lower recall
		// low exploration = went straight to the right files = higher recall
		estimatedRecall := 1.0 - s.ExplorationRatio
		if estimatedRecall < 0.1 {
			estimatedRecall = 0.1
		}

		outcomes = append(outcomes, HistoricalOutcome{
			Recall:      estimatedRecall,
			Complexity:  s.EditCount, // files edited as complexity proxy
			TestDensity: 0,           // unknown from trace alone
			BlastRadius: s.UniqueFilesEdit,
		})
	}

	return outcomes
}

// outcomesFromCalibration converts calibration entries into historical outcomes.
func outcomesFromCalibration(cwd string) []HistoricalOutcome {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}

	// Find project directory by hash (same logic as history package)
	projectDirs, _ := filepath.Glob(filepath.Join(home, ".plancheck", "projects", "*"))
	for _, dir := range projectDirs {
		// Check if this dir's project.txt matches our cwd
		projFile := filepath.Join(dir, "project.txt")
		data, err := os.ReadFile(projFile)
		if err != nil {
			continue
		}
		if string(data) != cwd {
			continue
		}

		// Found matching project — load calibration
		calFile := filepath.Join(dir, "calibration.json")
		calData, err := os.ReadFile(calFile)
		if err != nil {
			return nil
		}

		var store struct {
			Entries []struct {
				Recall       float64 `json:"recall"`
				Precision    float64 `json:"precision"`
				BlastRadius  int     `json:"blastRadius"`
				TestCoverage int     `json:"testCoverage"`
				Confidence   string  `json:"confidence"`
				Outcome      string  `json:"outcome"`
			} `json:"entries"`
		}
		if json.Unmarshal(calData, &store) != nil {
			return nil
		}

		var outcomes []HistoricalOutcome
		for _, e := range store.Entries {
			outcomes = append(outcomes, HistoricalOutcome{
				Recall:      e.Recall,
				Precision:   e.Precision,
				Complexity:  e.BlastRadius,
				BlastRadius: e.BlastRadius,
				Outcome:     e.Outcome,
			})
		}
		return outcomes
	}

	return nil
}

// loadSeedData loads the cross-project baseline from SWE-smith-go backtesting.
// This provides the initial forecast for greenfield projects before they
// accumulate their own history.
func loadSeedData() []HistoricalOutcome {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}

	path := filepath.Join(home, ".plancheck", "forecast_history.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}

	var raw []struct {
		Recall      float64 `json:"recall"`
		Complexity  int     `json:"complexity"`
		TestDensity float64 `json:"test_density"`
		BlastRadius int     `json:"blast_radius"`
	}
	if json.Unmarshal(data, &raw) != nil {
		return nil
	}

	var outcomes []HistoricalOutcome
	for _, r := range raw {
		outcomes = append(outcomes, HistoricalOutcome{
			Recall:      r.Recall,
			Complexity:  r.Complexity,
			TestDensity: r.TestDensity,
			BlastRadius: r.BlastRadius,
		})
	}
	return outcomes
}
