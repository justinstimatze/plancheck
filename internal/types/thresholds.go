// thresholds.go centralizes all magic numbers used across plancheck.
//
// Every threshold has a default value derived from empirical validation
// against our backtesting data (1,451 SWE-smith-go tasks, 93 real commits,
// 235 Multi-SWE-bench tasks). Defaults can be overridden per-project
// via ~/.plancheck/projects/<hash>/thresholds.json.
//
// The calibration scripts (scripts/train_weights.py, scripts/base_rates.py)
// can generate optimal thresholds for a specific repo's characteristics.
package types

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Thresholds holds all configurable magic numbers.
type Thresholds struct {
	// Comod analysis
	ComodBaseFrequency float64 `json:"comodBaseFrequency"` // min co-change frequency (default: 0.4, from comod.go)
	ComodHighConfidence float64 `json:"comodHighConfidence"` // threshold for "high" confidence (default: 0.75)

	// Gate
	GateForecastRiskThreshold float64 `json:"gateForecastRisk"` // P(failed) above this → extra round (default: 0.4)
	GateNoveltyThreshold      float64 `json:"gateNovelty"`      // novelty above this → extra round (default: 0.5)
	GateMaxRounds             int     `json:"gateMaxRounds"`    // hard cap on verification rounds (default: 4)

	// Ranking
	RankStructuralBase float64 `json:"rankStructural"` // structural signal weight at novelty=0 (default: 0.5)
	RankComodBase      float64 `json:"rankComod"`      // comod signal weight at novelty=0 (default: 0.35)
	RankAnalogyBase    float64 `json:"rankAnalogy"`    // analogy signal weight at novelty=0 (default: 0.05)
	RankSemanticBase   float64 `json:"rankSemantic"`   // semantic signal weight at novelty=0 (default: 0.1)
	RankTopK           int     `json:"rankTopK"`       // number of ranked suggestions (default: 5)

	// Simulation
	SimMinCallers int     `json:"simMinCallers"` // min callers to include in simulation (default: 2)
	SimMaxMutations int   `json:"simMaxMutations"` // max mutations per simulation (default: 5)

	// Forecast
	ForecastMinHistory int `json:"forecastMinHistory"` // min data points for MC forecast (default: 3)
	ForecastSimulations int `json:"forecastSimulations"` // number of MC samples (default: 10000)

	// Novelty
	NoveltyRoutineMax    float64 `json:"noveltyRoutine"`    // max score for "routine" (default: 0.15)
	NoveltyExtensionMax  float64 `json:"noveltyExtension"`  // max score for "extension" (default: 0.4)
	NoveltyNovelMax      float64 `json:"noveltyNovel"`      // max score for "novel" (default: 0.7)

	// Analogies
	AnalogyMinCallers int `json:"analogyMinCallers"` // min callers for expansion (default: 3)
	AnalogyMinKeywordLen int `json:"analogyMinKeyword"` // min keyword length (default: 4)
}

// DefaultThresholds returns empirically-derived defaults.
func DefaultThresholds() Thresholds {
	return Thresholds{
		ComodBaseFrequency:  0.4,
		ComodHighConfidence: 0.75,

		GateForecastRiskThreshold: 0.4,
		GateNoveltyThreshold:     0.5,
		GateMaxRounds:            4,

		RankStructuralBase: 0.5,
		RankComodBase:      0.35,
		RankAnalogyBase:    0.05,
		RankSemanticBase:   0.1,
		RankTopK:           5,

		SimMinCallers:   2,
		SimMaxMutations: 5,

		ForecastMinHistory:  3,
		ForecastSimulations: 10000,

		NoveltyRoutineMax:   0.15,
		NoveltyExtensionMax: 0.4,
		NoveltyNovelMax:     0.7,

		AnalogyMinCallers:   3,
		AnalogyMinKeywordLen: 4,
	}
}

// LoadThresholds loads project-specific thresholds, falling back to defaults.
func LoadThresholds(cwd string) Thresholds {
	defaults := DefaultThresholds()

	home, err := os.UserHomeDir()
	if err != nil {
		return defaults
	}

	// Try project-specific thresholds
	// (Uses same project hash as history package)
	projectDirs, _ := filepath.Glob(filepath.Join(home, ".plancheck", "projects", "*"))
	for _, dir := range projectDirs {
		projFile := filepath.Join(dir, "project.txt")
		data, err := os.ReadFile(projFile)
		if err != nil || string(data) != cwd {
			continue
		}

		threshFile := filepath.Join(dir, "thresholds.json")
		threshData, err := os.ReadFile(threshFile)
		if err != nil {
			return defaults
		}

		// Overlay project thresholds on defaults (only override non-zero values)
		var override Thresholds
		if json.Unmarshal(threshData, &override) != nil {
			return defaults
		}
		return mergeThresholds(defaults, override)
	}

	return defaults
}

func mergeThresholds(base, override Thresholds) Thresholds {
	// Only override non-zero values
	if override.ComodBaseFrequency > 0 { base.ComodBaseFrequency = override.ComodBaseFrequency }
	if override.ComodHighConfidence > 0 { base.ComodHighConfidence = override.ComodHighConfidence }
	if override.GateForecastRiskThreshold > 0 { base.GateForecastRiskThreshold = override.GateForecastRiskThreshold }
	if override.GateNoveltyThreshold > 0 { base.GateNoveltyThreshold = override.GateNoveltyThreshold }
	if override.GateMaxRounds > 0 { base.GateMaxRounds = override.GateMaxRounds }
	if override.RankStructuralBase > 0 { base.RankStructuralBase = override.RankStructuralBase }
	if override.RankComodBase > 0 { base.RankComodBase = override.RankComodBase }
	if override.RankAnalogyBase > 0 { base.RankAnalogyBase = override.RankAnalogyBase }
	if override.RankSemanticBase > 0 { base.RankSemanticBase = override.RankSemanticBase }
	if override.RankTopK > 0 { base.RankTopK = override.RankTopK }
	if override.SimMinCallers > 0 { base.SimMinCallers = override.SimMinCallers }
	if override.SimMaxMutations > 0 { base.SimMaxMutations = override.SimMaxMutations }
	if override.ForecastMinHistory > 0 { base.ForecastMinHistory = override.ForecastMinHistory }
	if override.ForecastSimulations > 0 { base.ForecastSimulations = override.ForecastSimulations }
	if override.NoveltyRoutineMax > 0 { base.NoveltyRoutineMax = override.NoveltyRoutineMax }
	if override.NoveltyExtensionMax > 0 { base.NoveltyExtensionMax = override.NoveltyExtensionMax }
	if override.NoveltyNovelMax > 0 { base.NoveltyNovelMax = override.NoveltyNovelMax }
	if override.AnalogyMinCallers > 0 { base.AnalogyMinCallers = override.AnalogyMinCallers }
	if override.AnalogyMinKeywordLen > 0 { base.AnalogyMinKeywordLen = override.AnalogyMinKeywordLen }
	return base
}
