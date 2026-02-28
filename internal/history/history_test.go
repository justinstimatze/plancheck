package history

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/justinstimatze/plancheck/internal/types"
)

func TestMain(m *testing.M) {
	// Override ProjectDirFn so tests write to temp dirs, not ~/.plancheck/.
	ProjectDirFn = func(cwd string) string {
		dir := filepath.Join(cwd, ".plancheck")
		_ = os.MkdirAll(dir, 0o700)
		return dir
	}
	os.Exit(m.Run())
}

var basePlan = types.ExecutionPlan{
	Objective:     "test plan",
	FilesToRead:   []string{},
	FilesToModify: []string{"src/app.ts"},
	FilesToCreate: []string{},
	Steps:         []string{},
}

func baseResult(overrides ...func(*types.PlanCheckResult)) types.PlanCheckResult {
	r := types.PlanCheckResult{
		MissingFiles:       []types.MissingFileResult{},
		ComodGaps:          []types.ComodGap{},
		Signals:            []types.Signal{},
		ProjectPatterns:    []types.ProjectPattern{},
		Critique:           []string{},
		SuggestedAdditions: types.SuggestedAdditions{FilesToModify: []string{}},
	}
	for _, o := range overrides {
		o(&r)
	}
	return r
}

func TestAppendHistory_CreatesFile(t *testing.T) {
	tmp := t.TempDir()
	AppendHistory(basePlan, baseResult(), tmp)
	histPath := filepath.Join(tmp, ".plancheck", "history.jsonl")
	if _, err := os.Stat(histPath); err != nil {
		t.Errorf("history file not created: %v", err)
	}
}

func TestAppendHistory_AppendsValidJSON(t *testing.T) {
	tmp := t.TempDir()
	AppendHistory(basePlan, baseResult(), tmp)
	AppendHistory(basePlan, baseResult(), tmp)
	data, _ := os.ReadFile(filepath.Join(tmp, ".plancheck", "history.jsonl"))
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(lines))
	}
	var e1, e2 HistoryEntry
	json.Unmarshal([]byte(lines[0]), &e1)
	json.Unmarshal([]byte(lines[1]), &e2)
	if e1.Objective != "test plan" {
		t.Errorf("expected objective 'test plan', got %q", e1.Objective)
	}
	if e2.ID == "" {
		t.Error("expected non-empty ID")
	}
}

func TestAppendHistory_EntryContainsFields(t *testing.T) {
	tmp := t.TempDir()
	AppendHistory(basePlan, baseResult(), tmp)
	data, _ := os.ReadFile(filepath.Join(tmp, ".plancheck", "history.jsonl"))
	var entry HistoryEntry
	json.Unmarshal([]byte(strings.TrimSpace(string(data))), &entry)
	if entry.Objective != "test plan" {
		t.Errorf("expected objective 'test plan', got %q", entry.Objective)
	}
	if entry.ID == "" {
		t.Error("expected non-empty ID")
	}
}

func TestAppendHistory_NeverThrows(t *testing.T) {
	// Should not panic even on unwritable path
	id := AppendHistory(basePlan, baseResult(), "/nonexistent/xyz")
	if id == "" {
		t.Error("should return an id even on failure")
	}
}

func TestAppendHistory_WritesLastCheckID(t *testing.T) {
	tmp := t.TempDir()
	id := AppendHistory(basePlan, baseResult(), tmp)
	idPath := filepath.Join(tmp, ".plancheck", "last-check-id")
	data, err := os.ReadFile(idPath)
	if err != nil {
		t.Fatalf("last-check-id not written: %v", err)
	}
	if strings.TrimSpace(string(data)) != id {
		t.Errorf("expected %q, got %q", id, strings.TrimSpace(string(data)))
	}
}

func TestAppendHistory_LastCheckIDOverwritten(t *testing.T) {
	tmp := t.TempDir()
	id1 := AppendHistory(basePlan, baseResult(), tmp)
	id2 := AppendHistory(basePlan, baseResult(), tmp)
	if id1 == id2 {
		t.Error("ids should differ")
	}
	if LoadLastCheckID(tmp) != id2 {
		t.Errorf("expected last check id %q, got %q", id2, LoadLastCheckID(tmp))
	}
}

func TestLoadLastCheckID_NoHistory(t *testing.T) {
	tmp := t.TempDir()
	if LoadLastCheckID(tmp) != "" {
		t.Error("expected empty string")
	}
}

func TestLoadLastCheckID_AfterAppend(t *testing.T) {
	tmp := t.TempDir()
	id := AppendHistory(basePlan, baseResult(), tmp)
	if LoadLastCheckID(tmp) != id {
		t.Errorf("expected %q, got %q", id, LoadLastCheckID(tmp))
	}
}

func TestLoadPatterns_NoHistory(t *testing.T) {
	tmp := t.TempDir()
	patterns := LoadPatterns(tmp)
	if len(patterns) != 0 {
		t.Errorf("expected 0 patterns, got %d", len(patterns))
	}
}

func TestLoadPatterns_OneEntry(t *testing.T) {
	tmp := t.TempDir()
	AppendHistory(basePlan, baseResult(), tmp)
	patterns := LoadPatterns(tmp)
	if len(patterns) != 0 {
		t.Errorf("expected 0 patterns, got %d", len(patterns))
	}
}

func TestLoadPatterns_RecurringMiss(t *testing.T) {
	tmp := t.TempDir()
	r := baseResult(func(r *types.PlanCheckResult) {
		r.SuggestedAdditions.FilesToModify = []string{"src/missing.ts"}
	})
	AppendHistory(basePlan, r, tmp)
	AppendHistory(basePlan, r, tmp)
	patterns := LoadPatterns(tmp)
	var misses []types.ProjectPattern
	for _, p := range patterns {
		if p.Type == "recurring-miss" {
			misses = append(misses, p)
		}
	}
	if len(misses) != 1 {
		t.Fatalf("expected 1 recurring-miss pattern, got %d", len(misses))
	}
	if !strings.Contains(misses[0].Description, "src/missing.ts") {
		t.Errorf("expected description to contain 'src/missing.ts', got %q", misses[0].Description)
	}
	if !strings.Contains(misses[0].Description, "2x") {
		t.Errorf("expected description to contain '2x', got %q", misses[0].Description)
	}
}

func TestLoadPatterns_PlaceholderExcluded(t *testing.T) {
	tmp := t.TempDir()
	r := baseResult(func(r *types.PlanCheckResult) {
		r.SuggestedAdditions.FilesToModify = []string{"<file that imports foo>"}
	})
	AppendHistory(basePlan, r, tmp)
	AppendHistory(basePlan, r, tmp)
	patterns := LoadPatterns(tmp)
	for _, p := range patterns {
		if p.Type == "recurring-miss" {
			t.Error("placeholder should be excluded from recurring-miss")
		}
	}
}

func TestLoadPatterns_ComodMissesAlsoFeed(t *testing.T) {
	tmp := t.TempDir()
	r := baseResult(func(r *types.PlanCheckResult) {
		r.ComodGaps = []types.ComodGap{{
			PlanFile: "src/app.ts", ComodFile: "src/server.ts",
			Frequency: 0.9, Suggestion: "",
		}}
	})
	AppendHistory(basePlan, r, tmp)
	AppendHistory(basePlan, r, tmp)
	patterns := LoadPatterns(tmp)
	var misses []types.ProjectPattern
	for _, p := range patterns {
		if p.Type == "recurring-miss" {
			misses = append(misses, p)
		}
	}
	if len(misses) != 1 {
		t.Fatalf("expected 1 recurring-miss, got %d", len(misses))
	}
	if !strings.Contains(misses[0].Description, "src/server.ts") {
		t.Errorf("expected 'src/server.ts' in description, got %q", misses[0].Description)
	}
}

func TestLoadPatterns_SingleOccurrenceNoPattern(t *testing.T) {
	tmp := t.TempDir()
	AppendHistory(basePlan, baseResult(func(r *types.PlanCheckResult) {
		r.SuggestedAdditions.FilesToModify = []string{"src/rare.ts"}
	}), tmp)
	AppendHistory(basePlan, baseResult(func(r *types.PlanCheckResult) {
		r.SuggestedAdditions.FilesToModify = []string{"src/different.ts"}
	}), tmp)
	patterns := LoadPatterns(tmp)
	for _, p := range patterns {
		if p.Type == "recurring-miss" {
			t.Error("single occurrence should not become a pattern")
		}
	}
}

func TestRecordReflection_CreatesEntry(t *testing.T) {
	tmp := t.TempDir()
	id, err := RecordReflection(tmp, ReflectionOpts{
		Passes:          2,
		ProbeFindings:   2,
		PersonaFindings: 1,
		Missed:          "config file coupling",
		Outcome:         "rework",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id == "" {
		t.Fatal("expected non-empty id")
	}
	data, _ := os.ReadFile(filepath.Join(tmp, ".plancheck", "history.jsonl"))
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}
	var entry ReflectionEntry
	json.Unmarshal([]byte(lines[0]), &entry)
	if entry.Type != "reflection" {
		t.Errorf("expected type 'reflection', got %q", entry.Type)
	}
	if entry.Passes != 2 {
		t.Errorf("expected passes 2, got %d", entry.Passes)
	}
	if entry.ProbeFindings != 2 {
		t.Errorf("expected probe_findings 2, got %d", entry.ProbeFindings)
	}
	if entry.PersonaFindings != 1 {
		t.Errorf("expected persona_findings 1, got %d", entry.PersonaFindings)
	}
	if entry.Missed != "config file coupling" {
		t.Errorf("expected missed 'config file coupling', got %q", entry.Missed)
	}
	if entry.Outcome != "rework" {
		t.Errorf("expected outcome 'rework', got %q", entry.Outcome)
	}
}

func TestRecordReflection_UsesProvidedID(t *testing.T) {
	tmp := t.TempDir()
	id, err := RecordReflection(tmp, ReflectionOpts{
		ID:      "abc123",
		Passes:  3,
		Missed:  "",
		Outcome: "clean",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "abc123" {
		t.Errorf("expected 'abc123', got %q", id)
	}
	data, _ := os.ReadFile(filepath.Join(tmp, ".plancheck", "history.jsonl"))
	var entry ReflectionEntry
	json.Unmarshal([]byte(strings.TrimSpace(string(data))), &entry)
	if entry.ID != "abc123" {
		t.Errorf("expected id 'abc123', got %q", entry.ID)
	}
}

func TestRecordReflection_GeneratesID(t *testing.T) {
	tmp := t.TempDir()
	id, err := RecordReflection(tmp, ReflectionOpts{
		Passes:  2,
		Missed:  "",
		Outcome: "clean",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(id) != 6 {
		t.Errorf("expected 6-char id, got %q", id)
	}
}

func TestRecordReflection_RejectsFewerThan2Passes(t *testing.T) {
	tmp := t.TempDir()
	for _, passes := range []int{0, 1} {
		_, err := RecordReflection(tmp, ReflectionOpts{
			Passes:  passes,
			Missed:  "",
			Outcome: "clean",
		})
		if err == nil {
			t.Errorf("passes=%d: expected error, got nil", passes)
		}
	}
	// Confirm nothing was written
	histPath := filepath.Join(tmp, ".plancheck", "history.jsonl")
	data, err := os.ReadFile(histPath)
	if err == nil && strings.TrimSpace(string(data)) != "" {
		t.Error("rejected reflection should not write to history")
	}
}

func TestRecordReflection_Accepts2Passes(t *testing.T) {
	tmp := t.TempDir()
	id, err := RecordReflection(tmp, ReflectionOpts{
		Passes:  2,
		Missed:  "",
		Outcome: "clean",
	})
	if err != nil {
		t.Fatalf("passes=2 should succeed, got: %v", err)
	}
	if id == "" {
		t.Error("expected non-empty id")
	}
}

func TestRecordOutcome_BogusID(t *testing.T) {
	tmp := t.TempDir()
	// Create a real history entry first
	AppendHistory(basePlan, baseResult(), tmp)

	// Try to record outcome with a bogus ID
	err := RecordOutcome(tmp, "bogus999", "clean")
	if err == nil {
		t.Fatal("expected error for bogus ID, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' in error, got %q", err.Error())
	}
}

func TestRecordOutcome_ValidID(t *testing.T) {
	tmp := t.TempDir()
	id := AppendHistory(basePlan, baseResult(), tmp)

	err := RecordOutcome(tmp, id, "clean")
	if err != nil {
		t.Fatalf("expected no error for valid ID, got %v", err)
	}
}

func TestHistoryOptOut(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("PLANCHECK_NOHISTORY", "1")

	AppendHistory(basePlan, baseResult(), tmp)
	histPath := filepath.Join(tmp, ".plancheck", "history.jsonl")
	data, err := os.ReadFile(histPath)
	if err == nil && strings.TrimSpace(string(data)) != "" {
		t.Error("history should not be written when opted out")
	}

	// Reflection should still validate passes but skip writing
	_, err = RecordReflection(tmp, ReflectionOpts{
		Passes: 2, Missed: "", Outcome: "clean",
	})
	if err != nil {
		t.Fatalf("reflection should succeed when opted out: %v", err)
	}
	data, _ = os.ReadFile(histPath)
	if err == nil && strings.TrimSpace(string(data)) != "" {
		t.Error("reflection should not write when opted out")
	}

	// Patterns should return nil
	if p := LoadPatterns(tmp); len(p) != 0 {
		t.Error("patterns should be empty when opted out")
	}
}

func TestSaveLoadCheckResult(t *testing.T) {
	tmp := t.TempDir()
	original := baseResult(func(r *types.PlanCheckResult) {
		r.HistoryID = "test123"
		r.ComodGaps = []types.ComodGap{{PlanFile: "x.ts", ComodFile: "y.ts", Frequency: 0.8}}
	})

	SaveCheckResult(tmp, original)
	loaded, err := LoadCheckResult(tmp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if loaded.HistoryID != "test123" {
		t.Errorf("historyId: got %q", loaded.HistoryID)
	}
	if len(loaded.ComodGaps) != 1 {
		t.Errorf("comodGaps: got %d", len(loaded.ComodGaps))
	}
}

func TestSaveCheckResult_Overwrites(t *testing.T) {
	tmp := t.TempDir()
	SaveCheckResult(tmp, baseResult(func(r *types.PlanCheckResult) { r.HistoryID = "first" }))
	SaveCheckResult(tmp, baseResult(func(r *types.PlanCheckResult) { r.HistoryID = "second" }))
	loaded, err := LoadCheckResult(tmp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if loaded.HistoryID != "second" {
		t.Errorf("expected latest historyId 'second', got %q", loaded.HistoryID)
	}
}

func TestLoadCheckResult_NoCache(t *testing.T) {
	tmp := t.TempDir()
	_, err := LoadCheckResult(tmp)
	if err == nil {
		t.Fatal("expected error for missing cache")
	}
	if !strings.Contains(err.Error(), "no cached check result") {
		t.Errorf("expected 'no cached check result' in error, got %q", err.Error())
	}
}

func TestCullHistory(t *testing.T) {
	tmp := t.TempDir()
	// Write more than maxEntries
	var lastID string
	for i := 0; i < maxEntries+20; i++ {
		lastID = AppendHistory(basePlan, baseResult(), tmp)
	}
	data, _ := os.ReadFile(filepath.Join(tmp, ".plancheck", "history.jsonl"))
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != maxEntries {
		t.Errorf("expected %d lines after cull, got %d", maxEntries, len(lines))
	}
	// Verify newest entry survived
	var last HistoryEntry
	json.Unmarshal([]byte(lines[len(lines)-1]), &last)
	if last.ID != lastID {
		t.Errorf("expected newest entry ID %q, got %q", lastID, last.ID)
	}
}
