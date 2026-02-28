package comod

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/justinstimatze/plancheck/internal/types"
)

var withFile = types.ExecutionPlan{
	Objective:     "test",
	FilesToRead:   []string{},
	FilesToModify: []string{"src/app.ts"},
	FilesToCreate: []string{},
	Steps:         []string{"update app"},
}

var empty = types.ExecutionPlan{
	Objective:     "test",
	FilesToRead:   []string{},
	FilesToModify: []string{},
	FilesToCreate: []string{"src/new.ts"},
	Steps:         []string{"create new"},
}

func TestReturnsEmptyWhenNotGitRepo(t *testing.T) {
	tmp := t.TempDir()
	gaps := CheckComod(withFile, tmp)
	if len(gaps) != 0 {
		t.Errorf("expected 0 gaps, got %d", len(gaps))
	}
}

func TestReturnsEmptyWhenNoFilesToModify(t *testing.T) {
	gaps := CheckComod(empty, ".")
	if len(gaps) != 0 {
		t.Errorf("expected 0 gaps, got %d", len(gaps))
	}
}

func TestNeverThrowsOnAnyCwd(t *testing.T) {
	gaps := CheckComod(withFile, "/nonexistent/path/xyz")
	if gaps == nil {
		gaps = []Gap{}
	}
	// Verify function returned without panic; result is valid
	_ = len(gaps)
}

func TestAdjustedThreshold(t *testing.T) {
	tests := []struct {
		uniqueFiles int
		wantMin     float64
		wantMax     float64
	}{
		{0, 1.0, 1.0},      // no files — suppress all
		{4, 0.9, 1.01},     // 2/sqrt(4) = 1.0
		{10, 0.6, 0.65},    // 2/sqrt(10) ≈ 0.63
		{25, 0.39, 0.41},   // 2/sqrt(25) = 0.4 — converges to base
		{100, 0.39, 0.41},  // clamped at base
		{1000, 0.39, 0.41}, // clamped at base
	}
	for _, tt := range tests {
		got := adjustedThreshold(tt.uniqueFiles)
		if got < tt.wantMin || got > tt.wantMax {
			t.Errorf("adjustedThreshold(%d) = %.3f, want [%.2f, %.2f]",
				tt.uniqueFiles, got, tt.wantMin, tt.wantMax)
		}
	}
}

func TestEachGapHasRequiredFields(t *testing.T) {
	// Run against the real plancheck repo — find repo root
	wd, err := os.Getwd()
	if err != nil {
		t.Skip("cannot get working directory")
	}
	repoRoot := filepath.Join(wd, "..", "..", "..")
	if _, err := os.Stat(filepath.Join(repoRoot, ".git")); err != nil {
		t.Skip("not in plancheck repo")
	}
	gaps := CheckComod(withFile, repoRoot)
	for _, gap := range gaps {
		if gap.PlanFile == "" {
			t.Error("planFile is empty")
		}
		if gap.ComodFile == "" {
			t.Error("comodFile is empty")
		}
		if gap.Frequency < baseFrequencyThreshold || gap.Frequency > 1 {
			t.Errorf("frequency out of range: %f", gap.Frequency)
		}
		if gap.Suggestion == "" {
			t.Error("suggestion is empty")
		}
	}
}
