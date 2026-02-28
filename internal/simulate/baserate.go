package simulate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// BaseRates holds cross-project prediction accuracy data.
type BaseRates struct {
	TotalTasks     int                        `json:"total_tasks"`
	ByKind         map[string]BaseRateEntry   `json:"by_kind"`
	ByBlastRadius  map[string]BaseRateEntry   `json:"by_blast_radius"`
	ByTestDensity  map[string]BaseRateEntry   `json:"by_test_density"`
}

// BaseRateEntry is a single reference class.
type BaseRateEntry struct {
	Count   int     `json:"count"`
	Recall  float64 `json:"recall"`
	HitRate float64 `json:"hit_rate"`
}

// LoadBaseRates loads the cross-project base rates from ~/.plancheck/base_rates.json.
func LoadBaseRates() *BaseRates {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	path := filepath.Join(home, ".plancheck", "base_rates.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var br BaseRates
	if json.Unmarshal(data, &br) != nil {
		return nil
	}
	return &br
}

// Lookup finds the most specific base rate for a given condition.
func (br *BaseRates) Lookup(isMethod bool, callers int, testDensity float64) (recall, hitRate float64, source string) {
	if br == nil {
		return 0, 0, ""
	}

	// Start with kind-level base rate
	kind := "function"
	if isMethod {
		kind = "method"
	}
	if entry, ok := br.ByKind[kind]; ok {
		recall = entry.Recall
		hitRate = entry.HitRate
		source = kind
	}

	// Override with blast radius bucket (more specific)
	bucket := blastBucket(callers)
	if entry, ok := br.ByBlastRadius[bucket]; ok {
		recall = entry.Recall
		hitRate = entry.HitRate
		source = fmt.Sprintf("%s, %s callers", kind, bucket)
	}

	// Further refine with test density
	densityBucket := densityBucket(testDensity)
	if entry, ok := br.ByTestDensity[densityBucket]; ok && entry.Count >= 20 {
		// Weight the density signal in
		recall = (recall + entry.Recall) / 2
		hitRate = (hitRate + entry.HitRate) / 2
		source += fmt.Sprintf(", %s test density", densityBucket)
	}

	return recall, hitRate, source
}

func blastBucket(callers int) string {
	switch {
	case callers <= 2:
		return "0-2"
	case callers <= 5:
		return "3-5"
	case callers <= 10:
		return "6-10"
	case callers <= 20:
		return "11-20"
	default:
		return ">20"
	}
}

func densityBucket(density float64) string {
	switch {
	case density < 0.20:
		return "<20%"
	case density < 0.35:
		return "20-35%"
	default:
		return ">35%"
	}
}
