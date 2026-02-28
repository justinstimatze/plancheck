package forecast

import (
	"math"
	"testing"
)

func TestRunBasic(t *testing.T) {
	// Simulate historical data similar to our backtesting results
	history := []HistoricalOutcome{
		// High test density, low complexity — good outcomes
		{Recall: 0.85, Complexity: 5, TestDensity: 0.56, BlastRadius: 3},
		{Recall: 0.90, Complexity: 4, TestDensity: 0.50, BlastRadius: 5},
		{Recall: 0.75, Complexity: 6, TestDensity: 0.48, BlastRadius: 8},
		{Recall: 0.80, Complexity: 3, TestDensity: 0.55, BlastRadius: 2},
		{Recall: 0.95, Complexity: 5, TestDensity: 0.52, BlastRadius: 4},
		// Low test density — worse outcomes
		{Recall: 0.40, Complexity: 5, TestDensity: 0.17, BlastRadius: 10},
		{Recall: 0.30, Complexity: 8, TestDensity: 0.16, BlastRadius: 15},
		{Recall: 0.35, Complexity: 6, TestDensity: 0.20, BlastRadius: 12},
	}

	// Forecast for a plan similar to the good ones
	good := Run(PlanProperties{
		Complexity:  5,
		TestDensity: 0.50,
		BlastRadius: 4,
		FileCount:   3,
	}, history, 1000)

	if good.PClean < 0.5 {
		t.Errorf("expected high P(clean) for good plan, got %.2f", good.PClean)
	}
	if good.RecallMean < 0.6 {
		t.Errorf("expected high mean recall for good plan, got %.2f", good.RecallMean)
	}

	// Forecast for a plan similar to the bad ones
	bad := Run(PlanProperties{
		Complexity:  7,
		TestDensity: 0.18,
		BlastRadius: 12,
		FileCount:   5,
	}, history, 1000)

	if bad.RecallMean > good.RecallMean {
		t.Errorf("bad plan recall (%.2f) should be lower than good (%.2f)",
			bad.RecallMean, good.RecallMean)
	}
}

func TestRunInsufficientData(t *testing.T) {
	f := Run(PlanProperties{Complexity: 5}, nil, 100)
	if f.Summary == "" {
		t.Error("expected summary even with no data")
	}
	if f.MatchingHistorical != 0 {
		t.Errorf("expected 0 matching, got %d", f.MatchingHistorical)
	}
}

func TestFindSimilar(t *testing.T) {
	history := []HistoricalOutcome{
		{Complexity: 5, TestDensity: 0.50, BlastRadius: 4},
		{Complexity: 50, TestDensity: 0.10, BlastRadius: 100},
		{Complexity: 6, TestDensity: 0.45, BlastRadius: 6},
	}

	similar := findSimilar(PlanProperties{
		Complexity:  5,
		TestDensity: 0.50,
		BlastRadius: 4,
	}, history)

	if len(similar) < 1 {
		t.Error("expected at least 1 similar plan")
	}
	// The outlier (complexity=50) should not match
	for _, s := range similar {
		if s.Complexity == 50 {
			t.Error("outlier should not match")
		}
	}
}

func TestPercentile(t *testing.T) {
	data := []float64{0.1, 0.2, 0.3, 0.4, 0.5, 0.6, 0.7, 0.8, 0.9, 1.0}
	p50 := percentile(data, 0.50)
	if math.Abs(p50-0.5) > 0.15 {
		t.Errorf("P50 of uniform 0.1-1.0 should be ~0.5, got %.2f", p50)
	}
	p95 := percentile(data, 0.95)
	if p95 < 0.8 {
		t.Errorf("P95 should be high, got %.2f", p95)
	}
}
