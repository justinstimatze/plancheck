package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/justinstimatze/plancheck/internal/history"
	"github.com/justinstimatze/plancheck/internal/types"
)

func setupGateTest(t *testing.T) (projectDir string, stateFile string) {
	t.Helper()
	tmp := t.TempDir()
	projectDir = filepath.Join(tmp, "project")
	os.MkdirAll(projectDir, 0o755)
	history.ProjectDirFn = func(string) string { return projectDir }
	t.Cleanup(func() { history.ProjectDirFn = nil })

	stateFile = filepath.Join(tmp, "gate-test.json")
	return projectDir, stateFile
}

func writeCheckResult(t *testing.T, dir string, result types.PlanCheckResult) {
	t.Helper()
	data, _ := json.Marshal(result)
	os.WriteFile(filepath.Join(dir, "last-check-result.json"), data, 0o600)
}

func writeCheckID(t *testing.T, dir, id string) {
	t.Helper()
	os.WriteFile(filepath.Join(dir, "last-check-id"), []byte(id), 0o600)
}

func simplePlanResult() types.PlanCheckResult {
	return types.PlanCheckResult{
		PlanStats: types.PlanStats{Steps: 6, FilesToModify: 3},
	}
}

func complexPlanResult() types.PlanCheckResult {
	return types.PlanCheckResult{
		PlanStats: types.PlanStats{Steps: 12, FilesToModify: 6, FilesToCreate: 2},
	}
}

func veryComplexPlanResult() types.PlanCheckResult {
	return types.PlanCheckResult{
		PlanStats: types.PlanStats{Steps: 22, FilesToModify: 10, FilesToCreate: 5},
	}
}

func TestGate_NoCheckResult_Blocks(t *testing.T) {
	_, stateFile := setupGateTest(t)

	d := evaluateGate("/fake/cwd", stateFile)
	if d.Allow {
		t.Fatal("expected block when no check result exists")
	}
}

func TestGate_NoCheckResult_AllowsAfter3Attempts(t *testing.T) {
	_, stateFile := setupGateTest(t)

	for i := 0; i < 2; i++ {
		d := evaluateGate("/fake/cwd", stateFile)
		if d.Allow {
			t.Fatalf("expected block on attempt %d", i+1)
		}
	}
	d := evaluateGate("/fake/cwd", stateFile)
	if !d.Allow {
		t.Fatal("expected allow after 3 attempts (graceful degradation)")
	}
}

func TestGate_MissingFiles_Blocks(t *testing.T) {
	projectDir, stateFile := setupGateTest(t)

	writeCheckID(t, projectDir, "check-1")
	writeCheckResult(t, projectDir, types.PlanCheckResult{
		MissingFiles: []types.MissingFileResult{
			{File: "src/nope.ts", List: "filesToModify", Suggestion: "fix path"},
		},
	})

	d := evaluateGate("/fake/cwd", stateFile)
	if d.Allow {
		t.Fatal("expected block on missing files")
	}
}

// ── Trivial plan (≤4 steps/files): 1 check needed ──

func TestGate_TrivialPlan_AllowsOnFirstCheck(t *testing.T) {
	projectDir, stateFile := setupGateTest(t)

	writeCheckID(t, projectDir, "check-1")
	writeCheckResult(t, projectDir, types.PlanCheckResult{
		PlanStats: types.PlanStats{Steps: 3, FilesToModify: 2},
	})

	d := evaluateGate("/fake/cwd", stateFile)
	if !d.Allow {
		t.Fatalf("expected allow for trivial plan after 1 check, got: %s", d.Message)
	}
}

// ── Simple plan (5-10 steps/files): 2 checks needed ──

func TestGate_SimplePlan_BlocksOnFirstCheck(t *testing.T) {
	projectDir, stateFile := setupGateTest(t)

	writeCheckID(t, projectDir, "check-1")
	writeCheckResult(t, projectDir, simplePlanResult())

	d := evaluateGate("/fake/cwd", stateFile)
	if d.Allow {
		t.Fatal("expected block after first check (need 2)")
	}
}

func TestGate_SimplePlan_AllowsOnSecondCheck(t *testing.T) {
	projectDir, stateFile := setupGateTest(t)

	writeCheckID(t, projectDir, "check-1")
	writeCheckResult(t, projectDir, simplePlanResult())
	evaluateGate("/fake/cwd", stateFile) // attempt 1 — blocked

	writeCheckID(t, projectDir, "check-2")
	d := evaluateGate("/fake/cwd", stateFile) // attempt 2
	if !d.Allow {
		t.Fatalf("expected allow after 2 checks on simple plan, got: %s", d.Message)
	}
}

func TestGate_SimplePlan_SameCheckID_Blocks(t *testing.T) {
	projectDir, stateFile := setupGateTest(t)

	writeCheckID(t, projectDir, "check-1")
	writeCheckResult(t, projectDir, simplePlanResult())
	evaluateGate("/fake/cwd", stateFile) // attempt 1

	// Same check ID — model didn't re-run check_plan
	d := evaluateGate("/fake/cwd", stateFile) // attempt 2
	if d.Allow {
		t.Fatal("expected block when check_id hasn't changed")
	}
}

// ── Complex plan (11-20 steps/files): 3 checks needed ──

func TestGate_ComplexPlan_NeedsThreeChecks(t *testing.T) {
	projectDir, stateFile := setupGateTest(t)

	writeCheckID(t, projectDir, "check-1")
	writeCheckResult(t, projectDir, complexPlanResult())
	evaluateGate("/fake/cwd", stateFile) // attempt 1

	writeCheckID(t, projectDir, "check-2")
	d := evaluateGate("/fake/cwd", stateFile) // attempt 2
	if d.Allow {
		t.Fatal("expected block — complex plan needs 3 checks, only 2 done")
	}

	writeCheckID(t, projectDir, "check-3")
	d = evaluateGate("/fake/cwd", stateFile) // attempt 3
	if !d.Allow {
		t.Fatalf("expected allow after 3 checks on complex plan, got: %s", d.Message)
	}
}

func TestGate_ComplexPlan_SubdivisionMessage(t *testing.T) {
	projectDir, stateFile := setupGateTest(t)

	writeCheckID(t, projectDir, "check-1")
	writeCheckResult(t, projectDir, complexPlanResult())
	evaluateGate("/fake/cwd", stateFile) // attempt 1

	writeCheckID(t, projectDir, "check-2")
	d := evaluateGate("/fake/cwd", stateFile) // attempt 2
	if !strings.Contains(d.Message, "subdivide") {
		t.Errorf("expected subdivision message on second round, got: %s", d.Message)
	}
}

// ── Very complex plan (>20 steps/files): 4 checks needed ──

func TestGate_VeryComplexPlan_NeedsFourChecks(t *testing.T) {
	projectDir, stateFile := setupGateTest(t)

	writeCheckID(t, projectDir, "check-1")
	writeCheckResult(t, projectDir, veryComplexPlanResult())
	evaluateGate("/fake/cwd", stateFile) // 1

	writeCheckID(t, projectDir, "check-2")
	d := evaluateGate("/fake/cwd", stateFile) // 2
	if d.Allow {
		t.Fatal("expected block at 2/4")
	}

	writeCheckID(t, projectDir, "check-3")
	d = evaluateGate("/fake/cwd", stateFile) // 3
	if d.Allow {
		t.Fatal("expected block at 3/4")
	}

	writeCheckID(t, projectDir, "check-4")
	d = evaluateGate("/fake/cwd", stateFile) // 4
	if !d.Allow {
		t.Fatalf("expected allow after 4 checks, got: %s", d.Message)
	}
}

// ── Complexity uses max(steps, files) ──

func TestGate_ComplexityUsesFiles(t *testing.T) {
	projectDir, stateFile := setupGateTest(t)

	// 5 steps but 12 files — complexity should be 12, requiring 3 checks
	writeCheckID(t, projectDir, "check-1")
	writeCheckResult(t, projectDir, types.PlanCheckResult{
		PlanStats: types.PlanStats{Steps: 5, FilesToModify: 10, FilesToCreate: 2},
	})
	evaluateGate("/fake/cwd", stateFile)

	writeCheckID(t, projectDir, "check-2")
	d := evaluateGate("/fake/cwd", stateFile)
	if d.Allow {
		t.Fatal("expected block — 12 files means 3 checks needed")
	}

	writeCheckID(t, projectDir, "check-3")
	d = evaluateGate("/fake/cwd", stateFile)
	if !d.Allow {
		t.Fatalf("expected allow after 3 checks, got: %s", d.Message)
	}
}

// ── Missing files fixed mid-flow ──

func TestGate_MissingFilesFixed_ThenAllows(t *testing.T) {
	projectDir, stateFile := setupGateTest(t)

	writeCheckID(t, projectDir, "check-1")
	writeCheckResult(t, projectDir, types.PlanCheckResult{
		PlanStats: types.PlanStats{Steps: 4, FilesToModify: 2},
		MissingFiles: []types.MissingFileResult{
			{File: "src/nope.ts", List: "filesToModify"},
		},
	})

	d := evaluateGate("/fake/cwd", stateFile)
	if d.Allow {
		t.Fatal("expected block on missing files")
	}

	// Fix + re-check
	writeCheckID(t, projectDir, "check-2")
	writeCheckResult(t, projectDir, simplePlanResult())

	// This is attempt 2, check-2 is first clean check seen.
	// Simple plan needs 2 checks. Only 1 clean check seen → block.
	d = evaluateGate("/fake/cwd", stateFile)
	if d.Allow {
		t.Fatal("expected block — only 1 clean check seen")
	}

	// Re-check after verification
	writeCheckID(t, projectDir, "check-3")
	d = evaluateGate("/fake/cwd", stateFile)
	if !d.Allow {
		t.Fatalf("expected allow, got: %s", d.Message)
	}
}

func TestGate_ComodGaps_InBlockMessage(t *testing.T) {
	projectDir, stateFile := setupGateTest(t)

	writeCheckID(t, projectDir, "check-1")
	writeCheckResult(t, projectDir, types.PlanCheckResult{
		PlanStats: types.PlanStats{Steps: 6, FilesToModify: 3},
		ComodGaps: []types.ComodGap{
			{PlanFile: "cmd/gate.go", ComodFile: "cmd/gate_test.go", Frequency: 0.8, Confidence: "high", Suggestion: "add to filesToModify"},
		},
	})

	d := evaluateGate("/fake/cwd", stateFile)
	if d.Allow {
		t.Fatal("expected block after first check")
	}
	if !strings.Contains(d.Message, "cmd/gate_test.go") {
		t.Errorf("expected comod gap file in message, got: %s", d.Message)
	}
	if !strings.Contains(d.Message, "Co-mod") {
		t.Errorf("expected 'Co-mod' label in message, got: %s", d.Message)
	}
}

func TestGate_ComodGaps_DontPermanentlyBlock(t *testing.T) {
	projectDir, stateFile := setupGateTest(t)

	// High-confidence comod gaps should NOT permanently block.
	// With complexity 6 (needs 2 checks), gaps warn but don't prevent iteration.
	comodResult := types.PlanCheckResult{
		PlanStats: types.PlanStats{Steps: 6, FilesToModify: 4},
		ComodGaps: []types.ComodGap{
			{PlanFile: "cmd/gate.go", ComodFile: "README.md", Frequency: 0.8, Confidence: "high"},
			{PlanFile: "cmd/gate.go", ComodFile: ".gitignore", Frequency: 0.6, Confidence: "high"},
		},
	}

	writeCheckID(t, projectDir, "check-1")
	writeCheckResult(t, projectDir, comodResult)
	evaluateGate("/fake/cwd", stateFile) // attempt 1 — blocked (need 2 checks)

	writeCheckID(t, projectDir, "check-2")
	writeCheckResult(t, projectDir, comodResult) // same comod gaps
	d := evaluateGate("/fake/cwd", stateFile)    // attempt 2
	if !d.Allow {
		t.Fatalf("expected allow after 2 checks despite comod gaps, got: %s", d.Message)
	}
}

func TestGate_PlanRewrite_ResetsState(t *testing.T) {
	projectDir, stateFile := setupGateTest(t)

	// First plan: complex (needs 2 checks)
	writeCheckID(t, projectDir, "check-1")
	writeCheckResult(t, projectDir, types.PlanCheckResult{
		PlanHash:  "plan-a",
		PlanStats: types.PlanStats{Steps: 8, FilesToModify: 5},
	})
	evaluateGate("/fake/cwd", stateFile) // attempt 1 — blocked

	// Plan is rewritten (different hash) and re-checked
	writeCheckID(t, projectDir, "check-2")
	writeCheckResult(t, projectDir, types.PlanCheckResult{
		PlanHash:  "plan-b",
		PlanStats: types.PlanStats{Steps: 3, FilesToModify: 2}, // now trivial
	})
	d := evaluateGate("/fake/cwd", stateFile)
	if !d.Allow {
		t.Fatalf("expected allow after plan rewrite to trivial plan, got: %s", d.Message)
	}
}

func TestRequiredChecks(t *testing.T) {
	tests := []struct {
		complexity int
		want       int
	}{
		{0, 1}, {1, 1}, {3, 1}, {4, 1},
		{5, 2}, {10, 2},
		{11, 3}, {15, 3}, {20, 3},
		{21, 4}, {30, 4}, {100, 4},
	}
	for _, tt := range tests {
		got := requiredChecks(tt.complexity)
		if got != tt.want {
			t.Errorf("requiredChecks(%d) = %d, want %d", tt.complexity, got, tt.want)
		}
	}
}
