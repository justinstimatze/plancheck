package plan

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/justinstimatze/plancheck/internal/history"
	"github.com/justinstimatze/plancheck/internal/types"
)

func TestMain(m *testing.M) {
	// Override ProjectDirFn so plan tests don't leak into ~/.plancheck/
	history.ProjectDirFn = func(cwd string) string {
		dir := filepath.Join(cwd, ".plancheck")
		_ = os.MkdirAll(dir, 0o700)
		return dir
	}
	os.Exit(m.Run())
}

func fixtureDir() string {
	wd, _ := os.Getwd()
	// internal/plan -> project root (2 levels up)
	goDir := filepath.Join(wd, "..", "..")
	return filepath.Join(goDir, "testdata", "fixtures", "sample-project")
}

func makePlan(fields types.ExecutionPlan) types.ExecutionPlan {
	if fields.Objective == "" {
		fields.Objective = "test"
	}
	if fields.FilesToRead == nil {
		fields.FilesToRead = []string{}
	}
	if fields.FilesToModify == nil {
		fields.FilesToModify = []string{}
	}
	if fields.FilesToCreate == nil {
		fields.FilesToCreate = []string{}
	}
	if fields.Steps == nil {
		fields.Steps = []string{}
	}
	return fields
}

func run(p types.ExecutionPlan, cwd ...string) types.PlanCheckResult {
	c := fixtureDir()
	if len(cwd) > 0 {
		c = cwd[0]
	}
	return Check(CheckOptions{Plan: p, Cwd: c})
}

// writeFile creates a file in the tmp directory with the given content.
func writeFile(t *testing.T, dir, relPath, content string) {
	t.Helper()
	fp := filepath.Join(dir, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(fp), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fp, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// setupGitRepo creates a git repo in tmp with controlled co-modification history.
// Each entry in commits is a list of files that change together in one commit.
func setupGitRepo(t *testing.T, tmp string, commits [][]string) {
	t.Helper()
	cmds := [][]string{
		{"git", "-C", tmp, "init"},
		{"git", "-C", tmp, "config", "user.email", "test@test.com"},
		{"git", "-C", tmp, "config", "user.name", "Test"},
	}
	for _, cmd := range cmds {
		out, err := exec.Command(cmd[0], cmd[1:]...).CombinedOutput()
		if err != nil {
			t.Fatalf("%v failed: %s", cmd, out)
		}
	}

	// Create all files first (initial commit)
	allFiles := make(map[string]bool)
	for _, commit := range commits {
		for _, f := range commit {
			allFiles[f] = true
		}
	}
	for f := range allFiles {
		fp := filepath.Join(tmp, filepath.FromSlash(f))
		os.MkdirAll(filepath.Dir(fp), 0o755)
		os.WriteFile(fp, []byte("initial\n"), 0o644)
	}
	exec.Command("git", "-C", tmp, "add", "-A").Run()
	exec.Command("git", "-C", tmp, "commit", "-m", "initial").Run()

	// Now create co-modification commits by modifying files together
	for i, commit := range commits {
		for _, f := range commit {
			fp := filepath.Join(tmp, filepath.FromSlash(f))
			os.WriteFile(fp, []byte(fmt.Sprintf("change %d\n", i)), 0o644)
		}
		exec.Command("git", "-C", tmp, "add", "-A").Run()
		exec.Command("git", "-C", tmp, "commit", "-m", fmt.Sprintf("comod %d", i)).Run()
	}

	// Pad with modify-only commits so comod analysis runs (minCommitsForComod=10).
	// The pad file already exists from the initial commit setup, so all pad
	// commits are modifications (visible to --diff-filter=M in gitLogNameOnly).
	padFile := filepath.Join(tmp, ".pad")
	os.WriteFile(padFile, []byte("pad-init\n"), 0o644)
	exec.Command("git", "-C", tmp, "add", "-A").Run()
	exec.Command("git", "-C", tmp, "commit", "-m", "pad-init").Run()
	totalCommits := len(commits) + 2 // initial + pad-init + comod commits
	for i := totalCommits; i < 12; i++ {
		os.WriteFile(padFile, []byte(fmt.Sprintf("pad %d\n", i)), 0o644)
		exec.Command("git", "-C", tmp, "add", "-A").Run()
		exec.Command("git", "-C", tmp, "commit", "-m", fmt.Sprintf("pad %d", i)).Run()
	}
}

// ── Fixtures ──────────────────────────────────────────────────

func TestFixture_CompletePlan(t *testing.T) {
	goDir, _ := os.Getwd()
	goDir = filepath.Join(goDir, "..", "..")
	data, err := os.ReadFile(filepath.Join(goDir, "testdata", "fixtures", "complete-plan.json"))
	if err != nil {
		t.Fatalf("cannot read complete-plan.json: %v", err)
	}
	p, err := types.ParsePlan(data)
	if err != nil {
		t.Fatalf("invalid plan: %v", err)
	}
	result := run(p)
	if len(result.MissingFiles) != 0 {
		t.Errorf("expected 0 missing files, got %d", len(result.MissingFiles))
	}
}

// ── Greenfield ────────────────────────────────────────────────

func TestGreenfield_NoFindings(t *testing.T) {
	tmp := t.TempDir()
	result := Check(CheckOptions{
		Plan: makePlan(types.ExecutionPlan{FilesToCreate: []string{"src/a.ts", "src/b.ts"}, Steps: []string{"create them"}}),
		Cwd:  tmp,
	})
	if len(result.MissingFiles) != 0 {
		t.Errorf("expected 0 missing files, got %d", len(result.MissingFiles))
	}
	if result.ProjectType != "greenfield" {
		t.Errorf("expected 'greenfield', got %q", result.ProjectType)
	}
}

func hasSignal(r types.PlanCheckResult, probe string) bool {
	for _, s := range r.Signals {
		if s.Probe == probe {
			return true
		}
	}
	return false
}

// ── Path normalization ────────────────────────────────────────

func TestPathNorm_AbsolutePath(t *testing.T) {
	cwd := fixtureDir()
	absCwd, _ := filepath.Abs(cwd)
	result := run(makePlan(types.ExecutionPlan{
		FilesToModify: []string{filepath.Join(absCwd, "src/router.ts")},
		Steps:         []string{"update router"},
	}))
	if len(result.MissingFiles) != 0 {
		t.Errorf("expected 0 missing files after abs path normalization, got %d", len(result.MissingFiles))
	}
}

func TestPathNorm_DotSlashPrefix(t *testing.T) {
	result := run(makePlan(types.ExecutionPlan{
		FilesToModify: []string{"./src/router.ts"},
		Steps:         []string{"update router"},
	}))
	if len(result.MissingFiles) != 0 {
		t.Errorf("expected 0 missing files after ./ prefix normalization, got %d", len(result.MissingFiles))
	}
}

// ── Multi-probe integration ──────────────────────────────────

// ── Acknowledged comod ───────────────────────────────────────

func TestAcknowledgedComod_ReducesPenalty(t *testing.T) {
	// uniquePlanFiles is computed from structural gaps, not the plan itself.
	// Need multiple plan files with comod gaps so nUnique > 1.
	// config.ts co-changes with app.ts; other.ts co-changes with b.ts.
	// uniquePlanFiles = {app.ts, b.ts}, nUnique = 2.
	// config.ts: count=1, ratio=1/2=50% < 60%, count=1 < 3 → NOT hub.
	tmp := t.TempDir()
	setupGitRepo(t, tmp, [][]string{
		{"src/app.ts", "src/config.ts"},
		{"src/app.ts", "src/config.ts"},
		{"src/app.ts", "src/config.ts"},
		{"src/b.ts", "src/other.ts"},
		{"src/b.ts", "src/other.ts"},
		{"src/b.ts", "src/other.ts"},
	})

	// Without acknowledgment
	resultUnack := Check(CheckOptions{
		Plan: makePlan(types.ExecutionPlan{
			FilesToModify: []string{"src/app.ts", "src/b.ts"},
			Steps:         []string{"update app and b"},
		}),
		Cwd: tmp,
	})

	// With acknowledgment
	resultAck := Check(CheckOptions{
		Plan: makePlan(types.ExecutionPlan{
			FilesToModify:     []string{"src/app.ts", "src/b.ts"},
			Steps:             []string{"update app and b"},
			AcknowledgedComod: map[string]string{"src/config.ts": "config doesn't need changes here"},
		}),
		Cwd: tmp,
	})

	if len(resultUnack.ComodGaps) == 0 {
		t.Fatal("expected comod gaps without acknowledgment")
	}
	if len(resultAck.ComodGaps) == 0 {
		t.Fatal("expected comod gaps with acknowledgment (still reported)")
	}

	// Acknowledged gap should have Acknowledged=true
	for _, gap := range resultAck.ComodGaps {
		if gap.ComodFile == "src/config.ts" {
			if !gap.Acknowledged {
				t.Error("expected Acknowledged=true for config.ts")
			}
			if gap.AcknowledgedReason != "config doesn't need changes here" {
				t.Errorf("unexpected reason: %q", gap.AcknowledgedReason)
			}
		}
	}

	// Acknowledged gaps should still be reported but marked
	ackCount := 0
	for _, gap := range resultAck.ComodGaps {
		if gap.Acknowledged {
			ackCount++
		}
	}
	if ackCount == 0 {
		t.Error("expected at least one acknowledged gap")
	}
}

func TestAcknowledgedComod_BasenameMatch(t *testing.T) {
	tmp := t.TempDir()
	setupGitRepo(t, tmp, [][]string{
		{"src/app.ts", "src/config.ts"},
		{"src/app.ts", "src/config.ts"},
		{"src/app.ts", "src/config.ts"},
	})

	// Acknowledge by basename only
	result := Check(CheckOptions{
		Plan: makePlan(types.ExecutionPlan{
			FilesToModify:     []string{"src/app.ts"},
			Steps:             []string{"update app"},
			AcknowledgedComod: map[string]string{"config.ts": "not needed"},
		}),
		Cwd: tmp,
	})

	for _, gap := range result.ComodGaps {
		if gap.ComodFile == "src/config.ts" && !gap.Acknowledged {
			t.Error("expected basename-match acknowledgment for config.ts")
		}
	}
}

// ── Hub detection thresholds ─────────────────────────────────

func TestHub_ThreePlanFilesTriggersHub(t *testing.T) {
	tmp := t.TempDir()
	// shared.ts co-changes with a.ts, b.ts, and c.ts
	setupGitRepo(t, tmp, [][]string{
		{"src/a.ts", "src/shared.ts"},
		{"src/b.ts", "src/shared.ts"},
		{"src/c.ts", "src/shared.ts"},
	})

	result := Check(CheckOptions{
		Plan: makePlan(types.ExecutionPlan{
			FilesToModify: []string{"src/a.ts", "src/b.ts", "src/c.ts"},
			Steps:         []string{"update all three"},
		}),
		Cwd: tmp,
	})

	hubFound := false
	for _, gap := range result.ComodGaps {
		if gap.ComodFile == "src/shared.ts" {
			hubFound = true
			if !gap.Hub {
				t.Error("expected Hub=true for shared.ts (appears with 3 plan files)")
			}
		}
	}
	if !hubFound {
		t.Error("expected comod gap for shared.ts")
	}
}

func TestHub_OnePlanFileOfFourNoHub(t *testing.T) {
	tmp := t.TempDir()
	// Need all 4 plan files to have comod gaps so uniquePlanFiles counts all 4.
	// shared.ts co-changes only with a.ts: count=1, nUnique=4, ratio=25% < 60%, 1 < 3 → NOT hub.
	setupGitRepo(t, tmp, [][]string{
		{"src/a.ts", "src/shared.ts"},
		{"src/a.ts", "src/shared.ts"},
		{"src/a.ts", "src/shared.ts"},
		{"src/b.ts", "src/other1.ts"},
		{"src/b.ts", "src/other1.ts"},
		{"src/b.ts", "src/other1.ts"},
		{"src/c.ts", "src/other2.ts"},
		{"src/c.ts", "src/other2.ts"},
		{"src/c.ts", "src/other2.ts"},
		{"src/d.ts", "src/other3.ts"},
		{"src/d.ts", "src/other3.ts"},
		{"src/d.ts", "src/other3.ts"},
	})

	result := Check(CheckOptions{
		Plan: makePlan(types.ExecutionPlan{
			FilesToModify: []string{"src/a.ts", "src/b.ts", "src/c.ts", "src/d.ts"},
			Steps:         []string{"update all four"},
		}),
		Cwd: tmp,
	})

	for _, gap := range result.ComodGaps {
		if gap.ComodFile == "src/shared.ts" && gap.Hub {
			t.Error("expected Hub=false for shared.ts (only 1 of 4 plan files)")
		}
	}
}

func TestHub_FrequencyRatioTriggersHub(t *testing.T) {
	tmp := t.TempDir()
	// With 2 plan files, shared.ts appears with both = 100% >= 60%
	setupGitRepo(t, tmp, [][]string{
		{"src/a.ts", "src/shared.ts"},
		{"src/b.ts", "src/shared.ts"},
	})

	result := Check(CheckOptions{
		Plan: makePlan(types.ExecutionPlan{
			FilesToModify: []string{"src/a.ts", "src/b.ts"},
			Steps:         []string{"update both"},
		}),
		Cwd: tmp,
	})

	for _, gap := range result.ComodGaps {
		if gap.ComodFile == "src/shared.ts" {
			if !gap.Hub {
				t.Error("expected Hub=true for shared.ts (100% frequency ratio >= 60%)")
			}
			return
		}
	}
	t.Error("expected comod gap for shared.ts")
}

func TestHub_PenaltyIsDampened(t *testing.T) {
	tmp := t.TempDir()
	setupGitRepo(t, tmp, [][]string{
		{"src/a.ts", "src/shared.ts"},
		{"src/b.ts", "src/shared.ts"},
		{"src/c.ts", "src/shared.ts"},
	})

	result := Check(CheckOptions{
		Plan: makePlan(types.ExecutionPlan{
			FilesToModify: []string{"src/a.ts", "src/b.ts", "src/c.ts"},
			Steps:         []string{"update all three"},
		}),
		Cwd: tmp,
	})

	// Hub should be flagged but still reported as a comod gap
	hubFound := false
	for _, gap := range result.ComodGaps {
		if gap.ComodFile == "src/shared.ts" && gap.Hub {
			hubFound = true
		}
	}
	if !hubFound {
		t.Error("expected hub-flagged comod gap for shared.ts")
	}
}

// ── Empty plan ────────────────────────────────────────────────

func TestEmpty_NoFindings(t *testing.T) {
	tmp := t.TempDir()
	result := Check(CheckOptions{
		Plan: makePlan(types.ExecutionPlan{
			Steps: []string{"think about it"},
		}),
		Cwd: tmp,
	})
	if len(result.MissingFiles) != 0 {
		t.Errorf("expected 0 findings for empty plan, got %d missing files", len(result.MissingFiles))
	}
}

// ── Missing file detection ────────────────────────────────────

func TestMissingFile_ModifyNonexistent(t *testing.T) {
	tmp := t.TempDir()
	result := Check(CheckOptions{
		Plan: makePlan(types.ExecutionPlan{
			FilesToModify: []string{"src/phantom.ts"},
			Steps:         []string{"update phantom"},
		}),
		Cwd: tmp,
	})
	if len(result.MissingFiles) != 1 {
		t.Fatalf("expected 1 missing file, got %d", len(result.MissingFiles))
	}
	if result.MissingFiles[0].File != "src/phantom.ts" {
		t.Errorf("expected src/phantom.ts, got %s", result.MissingFiles[0].File)
	}
	if result.MissingFiles[0].List != "filesToModify" {
		t.Errorf("expected list filesToModify, got %s", result.MissingFiles[0].List)
	}
}

func TestMissingFile_ReadNonexistent(t *testing.T) {
	tmp := t.TempDir()
	result := Check(CheckOptions{
		Plan: makePlan(types.ExecutionPlan{
			FilesToRead: []string{"docs/nonexistent.md"},
			Steps:       []string{"read it"},
		}),
		Cwd: tmp,
	})
	if len(result.MissingFiles) != 1 {
		t.Fatalf("expected 1 missing file, got %d", len(result.MissingFiles))
	}
	if result.MissingFiles[0].List != "filesToRead" {
		t.Errorf("expected list filesToRead, got %s", result.MissingFiles[0].List)
	}
}

func TestMissingFile_ExistsOnDisk(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "src/real.ts", "export const x = 1")
	result := Check(CheckOptions{
		Plan: makePlan(types.ExecutionPlan{
			FilesToModify: []string{"src/real.ts"},
			Steps:         []string{"update it"},
		}),
		Cwd: tmp,
	})
	if len(result.MissingFiles) != 0 {
		t.Errorf("expected 0 missing files for existing file, got %d", len(result.MissingFiles))
	}
}

func TestMissingFile_InCreateSkipped(t *testing.T) {
	tmp := t.TempDir()
	// File is in both filesToCreate and filesToModify — should not be flagged
	result := Check(CheckOptions{
		Plan: makePlan(types.ExecutionPlan{
			FilesToCreate: []string{"src/new.ts"},
			FilesToModify: []string{"src/new.ts"},
			Steps:         []string{"create then modify"},
		}),
		Cwd: tmp,
	})
	if len(result.MissingFiles) != 0 {
		t.Errorf("expected 0 missing files when file is in filesToCreate, got %d", len(result.MissingFiles))
	}
}

func TestMissingFile_ReportedAsFinding(t *testing.T) {
	tmp := t.TempDir()
	result := Check(CheckOptions{
		Plan: makePlan(types.ExecutionPlan{
			FilesToModify: []string{"phantom.ts"},
			Steps:         []string{"update it"},
		}),
		Cwd: tmp,
	})
	if len(result.MissingFiles) != 1 {
		t.Errorf("expected 1 missing file finding, got %d", len(result.MissingFiles))
	}
}

// ── Result shape ──────────────────────────────────────────────

func TestResultShape(t *testing.T) {
	result := run(makePlan(types.ExecutionPlan{}))
	data, _ := json.Marshal(result)
	s := string(data)
	if strings.Contains(s, ":null") {
		t.Error("JSON output contains null values — all slices should be []")
	}
}

// ── Compact result ────────────────────────────────────────────

func TestToCompact(t *testing.T) {
	full := types.PlanCheckResult{
		HistoryID:   "abc123",
		ProjectType: "brownfield",
		MissingFiles: []types.MissingFileResult{
			{File: "phantom.ts", List: "filesToModify", Suggestion: "fix path"},
		},
		ComodGaps: []types.ComodGap{
			{PlanFile: "x.ts", ComodFile: "y.ts", Frequency: 0.8},
			{PlanFile: "x.ts", ComodFile: "z.ts", Frequency: 0.6},
			{PlanFile: "a.ts", ComodFile: "b.ts", Frequency: 0.5},
		},
		SuggestedAdditions: types.SuggestedAdditions{
			FilesToModify: []string{"router.ts"},
		},
		ProjectPatterns: []types.ProjectPattern{
			{Type: "recurring-miss", Description: "test"},
		},
		Signals: []types.Signal{
			{Probe: "churn", Message: "high"},
			{Probe: "import-chain", File: "app.ts", Message: "has importers"},
		},
		Critique: []string{"fix missing files"},
	}

	compact := full.ToCompact()

	if compact.HistoryID != "abc123" {
		t.Errorf("historyId: got %q", compact.HistoryID)
	}
	if compact.ProjectType != "brownfield" {
		t.Errorf("projectType: got %q", compact.ProjectType)
	}
	if compact.Findings.MissingFiles != 1 {
		t.Errorf("missingFiles count: got %d", compact.Findings.MissingFiles)
	}
	if compact.Findings.ComodGaps != 3 {
		t.Errorf("comodGaps count: got %d", compact.Findings.ComodGaps)
	}
	if compact.Findings.Signals != 2 {
		t.Errorf("signals count: got %d", compact.Findings.Signals)
	}
	if len(compact.SuggestedAdditions.FilesToModify) != 1 {
		t.Errorf("suggestedAdditions.filesToModify len: got %d", len(compact.SuggestedAdditions.FilesToModify))
	}
	if len(compact.ProjectPatterns) != 1 {
		t.Errorf("projectPatterns len: got %d", len(compact.ProjectPatterns))
	}
}

// ── File list validation ──────────────────────────────────────

func TestValidation_DirectoryInFileList(t *testing.T) {
	tmp := t.TempDir()
	os.MkdirAll(filepath.Join(tmp, "src"), 0o755)
	result := Check(CheckOptions{
		Plan: makePlan(types.ExecutionPlan{
			FilesToModify: []string{"src"},
			Steps:         []string{"update src"},
		}),
		Cwd: tmp,
	})
	if !hasSignal(result, "invalid-path") {
		t.Error("expected invalid-path signal for directory in filesToModify")
	}
}

func TestValidation_CreateExistingFile(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "src/app.ts", "export const x = 1")
	result := Check(CheckOptions{
		Plan: makePlan(types.ExecutionPlan{
			FilesToCreate: []string{"src/app.ts"},
			Steps:         []string{"create app"},
		}),
		Cwd: tmp,
	})
	if !hasSignal(result, "create-exists") {
		t.Error("expected create-exists signal for existing file in filesToCreate")
	}
}

func TestValidation_ListOverlap(t *testing.T) {
	tmp := t.TempDir()
	result := Check(CheckOptions{
		Plan: makePlan(types.ExecutionPlan{
			FilesToModify: []string{"src/app.ts"},
			FilesToCreate: []string{"src/app.ts"},
			Steps:         []string{"create and modify"},
		}),
		Cwd: tmp,
	})
	if !hasSignal(result, "list-overlap") {
		t.Error("expected list-overlap signal for file in both filesToModify and filesToCreate")
	}
}

func TestValidation_DeduplicatesFileLists(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "src/app.ts", "export const x = 1")
	result := Check(CheckOptions{
		Plan: makePlan(types.ExecutionPlan{
			FilesToModify: []string{"src/app.ts", "src/app.ts"},
			Steps:         []string{"update app"},
		}),
		Cwd: tmp,
	})
	// Should not have duplicate missing files or other duplicate findings
	if len(result.MissingFiles) != 0 {
		t.Errorf("expected 0 missing files, got %d", len(result.MissingFiles))
	}
}

func TestValidation_ComodSkipWithoutGit(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "src/app.ts", "export const x = 1")
	result := Check(CheckOptions{
		Plan: makePlan(types.ExecutionPlan{
			FilesToModify: []string{"src/app.ts"},
			Steps:         []string{"update app"},
		}),
		Cwd: tmp,
	})
	if !hasSignal(result, "comod-skip") {
		t.Error("expected comod-skip signal in non-git directory")
	}
}

func TestValidation_NoComodSkipWithGit(t *testing.T) {
	tmp := t.TempDir()
	setupGitRepo(t, tmp, [][]string{
		{"src/app.ts", "src/config.ts"},
	})
	result := Check(CheckOptions{
		Plan: makePlan(types.ExecutionPlan{
			FilesToModify: []string{"src/app.ts"},
			Steps:         []string{"update app"},
		}),
		Cwd: tmp,
	})
	if hasSignal(result, "comod-skip") {
		t.Error("expected no comod-skip signal in git directory")
	}
}

func TestValidation_EmptyStringFiltered(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "src/app.ts", "export const x = 1")
	result := Check(CheckOptions{
		Plan: makePlan(types.ExecutionPlan{
			FilesToModify: []string{"", "src/app.ts"},
			Steps:         []string{"update app"},
		}),
		Cwd: tmp,
	})
	// Empty string should be filtered out, not produce invalid-path or other signals
	if hasSignal(result, "invalid-path") {
		t.Error("expected no invalid-path signal — empty strings should be filtered")
	}
	if len(result.MissingFiles) != 0 {
		t.Errorf("expected 0 missing files, got %d", len(result.MissingFiles))
	}
}
