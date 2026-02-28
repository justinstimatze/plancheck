package simulate

import (
	"testing"
)

func TestDisplayName(t *testing.T) {
	tests := []struct {
		m    Mutation
		want string
	}{
		{Mutation{Name: "Foo"}, "Foo"},
		{Mutation{Name: "Render", Receiver: "*Context"}, "(*Context).Render"},
		{Mutation{Name: "Bar", Receiver: "MyType"}, "(MyType).Bar"},
	}
	for _, tt := range tests {
		got := displayName(tt.m)
		if got != tt.want {
			t.Errorf("displayName(%+v) = %q, want %q", tt.m, got, tt.want)
		}
	}
}

func TestImpactSummary(t *testing.T) {
	m := Mutation{Type: SignatureChange, Name: "Render", Receiver: "*Context"}
	step := StepResult{
		Mutation:          m,
		DefinitionFound:   true,
		ProductionCallers: 18,
		TestCoverage:      113,
	}
	got := impactSummary(m, step)
	if got == "" {
		t.Error("expected non-empty impact summary")
	}
	// Should mention "signature change" and the counts
	if !contains(got, "signature change") {
		t.Errorf("expected 'signature change' in %q", got)
	}
	if !contains(got, "18") {
		t.Errorf("expected '18' in %q", got)
	}

	// Not found case
	m2 := Mutation{Type: Removal, Name: "Missing"}
	step2 := StepResult{Mutation: m2, DefinitionFound: false}
	got2 := impactSummary(m2, step2)
	if !contains(got2, "not found") {
		t.Errorf("expected 'not found' in %q", got2)
	}
}

func TestImpactSummary_AllTypes(t *testing.T) {
	types := []struct {
		mutType MutationType
		keyword string
	}{
		{SignatureChange, "signature change"},
		{BehaviorChange, "behavior change"},
		{Removal, "removal"},
		{Addition, "addition"},
	}
	for _, tt := range types {
		m := Mutation{Type: tt.mutType, Name: "X"}
		step := StepResult{Mutation: m, DefinitionFound: true, ProductionCallers: 5, TestCoverage: 10}
		got := impactSummary(m, step)
		if !contains(got, tt.keyword) {
			t.Errorf("impactSummary for %s: expected %q in %q", tt.mutType, tt.keyword, got)
		}
	}
}

func TestGraphTestDensity_Nil(t *testing.T) {
	// Nil graph should return 0
	density := graphTestDensity(nil)
	if density != 0 {
		t.Errorf("expected 0 for nil graph, got %f", density)
	}
}

func TestRunNilGraph(t *testing.T) {
	// Run with nil graph should return error
	_, err := Run(nil, []Mutation{
		{Type: BehaviorChange, Name: "Foo"},
	})
	if err == nil {
		t.Fatal("expected error for nil graph")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
