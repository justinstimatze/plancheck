package refgraph

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/justinstimatze/plancheck/internal/types"
)

func TestAvailable(t *testing.T) {
	// No .defn/ → not available
	tmp := t.TempDir()
	if Available(tmp) {
		t.Error("expected not available for empty dir")
	}

	// Create .defn/ → available
	if err := os.MkdirAll(filepath.Join(tmp, ".defn"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !Available(tmp) {
		t.Error("expected available with .defn/ dir")
	}
}

func TestCheckBlastRadius_NoDefn(t *testing.T) {
	// Should return nil when no .defn/ exists
	p := types.ExecutionPlan{
		Objective:     "test",
		Steps:         []string{"step 1"},
		FilesToModify: []string{"foo.go"},
	}
	gaps := CheckBlastRadius(p, t.TempDir())
	if gaps != nil {
		t.Errorf("expected nil gaps without .defn/, got %d", len(gaps))
	}
}

func TestQueryDefn_NoDefn(t *testing.T) {
	// Should return nil for directory without .defn/
	rows := QueryDefn(t.TempDir(), "SELECT 1")
	if rows != nil {
		t.Errorf("expected nil for nonexistent .defn/, got %v", rows)
	}
}

func TestConfidenceFromCallers(t *testing.T) {
	tests := []struct {
		callers, total int
		want           string
	}{
		{0, 0, "moderate"},
		{1, 10, "moderate"},
		{5, 10, "high"}, // ≥5 callers
		{3, 4, "high"},  // >50% ratio
		{2, 10, "moderate"},
	}
	for _, tt := range tests {
		got := confidenceFromCallers(tt.callers, tt.total)
		if got != tt.want {
			t.Errorf("confidenceFromCallers(%d, %d) = %q, want %q",
				tt.callers, tt.total, got, tt.want)
		}
	}
}
