package predict

import (
	"math"
	"testing"
)

func TestCombineBasic(t *testing.T) {
	signals := []Signal{
		{Source: "structural", File: "handler.go", Score: 1.0, Reason: "calls AuthHandler"},
		{Source: "comod", File: "handler.go", Score: 0.8, Reason: "80% co-change"},
		{Source: "comod", File: "routes.go", Score: 0.4, Reason: "40% co-change"},
		{Source: "semantic", File: "middleware.go", Score: 0.7, Reason: "model suggests"},
	}
	weights := DefaultWeights()
	result := Combine(signals, weights, []string{"auth.go"})

	if len(result.Files) != 3 {
		t.Fatalf("expected 3 files, got %d", len(result.Files))
	}

	// handler.go should be highest (two strong signals)
	if result.Files[0].File != "handler.go" {
		t.Errorf("expected handler.go first, got %s", result.Files[0].File)
	}
	if result.Files[0].Confidence != "must" {
		t.Errorf("expected 'must' for structural=1.0, got %s", result.Files[0].Confidence)
	}

	// Plan files should be excluded
	for _, f := range result.Files {
		if f.File == "auth.go" {
			t.Error("plan file auth.go should be excluded")
		}
	}
}

func TestCombineExcludesPlanFiles(t *testing.T) {
	signals := []Signal{
		{Source: "structural", File: "auth.go", Score: 1.0, Reason: "self"},
		{Source: "structural", File: "handler.go", Score: 0.5, Reason: "caller"},
	}
	result := Combine(signals, DefaultWeights(), []string{"auth.go"})
	if len(result.Files) != 1 {
		t.Fatalf("expected 1 file (auth.go excluded), got %d", len(result.Files))
	}
}

func TestExtremize(t *testing.T) {
	// Factor > 1 should push away from 0.5
	p := 0.6
	e := extremize(p, 1.3)
	if e <= p {
		t.Errorf("extremize(0.6, 1.3) = %f, expected > 0.6", e)
	}

	// Factor > 1 on low probability should push toward 0
	p2 := 0.3
	e2 := extremize(p2, 1.3)
	if e2 >= p2 {
		t.Errorf("extremize(0.3, 1.3) = %f, expected < 0.3", e2)
	}

	// Edge cases
	if extremize(0, 1.5) != 0 {
		t.Error("extremize(0) should be 0")
	}
	if extremize(1, 1.5) != 1 {
		t.Error("extremize(1) should be 1")
	}
}

func TestCalibrateWeights(t *testing.T) {
	w := DefaultWeights()
	predictions := []FilePrediction{
		{File: "a.go", Structural: 1.0, Comod: 0.5, Semantic: 0.8},
		{File: "b.go", Structural: 0.0, Comod: 0.4, Semantic: 0.6},
	}
	actual := map[string]bool{"a.go": true} // a.go actually changed

	calibrated := CalibrateWeights(w, predictions, actual)

	// Structural was right (predicted a.go, correct) → should increase
	if calibrated.Structural <= 0.05 {
		t.Error("structural weight should increase when correct")
	}
	// All weights should sum to ~1
	sum := calibrated.Structural + calibrated.Comod + calibrated.Semantic
	if math.Abs(sum-1.0) > 0.01 {
		t.Errorf("weights should sum to 1, got %f", sum)
	}
	if calibrated.Source != "calibrated" {
		t.Errorf("expected 'calibrated' source, got %s", calibrated.Source)
	}
}

func TestClassifyCombined(t *testing.T) {
	tests := []struct {
		combined, structural float64
		want                 string
	}{
		{0.9, 1.0, "must"},
		{0.7, 0.0, "likely"},
		{0.3, 0.0, "consider"},
		{0.1, 0.0, "unlikely"},
		{0.3, 0.9, "must"}, // structural overrides
	}
	for _, tt := range tests {
		got := classifyCombined(tt.combined, tt.structural)
		if got != tt.want {
			t.Errorf("classifyCombined(%v, %v) = %q, want %q",
				tt.combined, tt.structural, got, tt.want)
		}
	}
}
