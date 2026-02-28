package types

import "testing"

func TestConfidenceLevel(t *testing.T) {
	tests := []struct {
		freq float64
		want string
	}{
		{0.0, "moderate"},
		{0.40, "moderate"},
		{0.50, "moderate"},
		{0.75, "moderate"},
		{0.76, "high"},
		{0.90, "high"},
		{1.0, "high"},
	}
	for _, tt := range tests {
		got := ConfidenceLevel(tt.freq)
		if got != tt.want {
			t.Errorf("ConfidenceLevel(%v) = %q, want %q", tt.freq, got, tt.want)
		}
	}
}

func TestParsePlan_Defaults(t *testing.T) {
	p, err := ParsePlan([]byte(`{"objective":"test","steps":["s1"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if p.FilesToRead == nil {
		t.Error("FilesToRead should be non-nil")
	}
	if p.FilesToModify == nil {
		t.Error("FilesToModify should be non-nil")
	}
	if p.FilesToCreate == nil {
		t.Error("FilesToCreate should be non-nil")
	}
}

func TestParsePlan_Required(t *testing.T) {
	_, err := ParsePlan([]byte(`{"steps":["s1"]}`))
	if err == nil {
		t.Error("expected error for missing objective")
	}
	_, err = ParsePlan([]byte(`{"objective":"test"}`))
	if err == nil {
		t.Error("expected error for missing steps")
	}
}

func TestToCompact_HighConfidenceCount(t *testing.T) {
	r := PlanCheckResult{
		ComodGaps: []ComodGap{
			{Confidence: "high", Acknowledged: false, Hub: false},
			{Confidence: "high", Acknowledged: true, Hub: false},  // acknowledged — not counted
			{Confidence: "moderate", Acknowledged: false, Hub: false},
			{Confidence: "high", Acknowledged: false, Hub: true},  // hub — not counted
		},
		Critique: []string{"c1", "c2"},
	}
	r.EnsureNonNil()
	c := r.ToCompact()
	if c.Findings.HighConfidenceGaps != 1 {
		t.Errorf("expected 1 high-confidence unacked non-hub gap, got %d", c.Findings.HighConfidenceGaps)
	}
	if len(c.TopFindings) != 2 {
		t.Errorf("expected 2 top findings, got %d", len(c.TopFindings))
	}
}
