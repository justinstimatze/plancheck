package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/justinstimatze/plancheck/internal/history"
)

// Tests are sequential — setupReflectFixture mutates the package-global
// history.ProjectDirFn. Do NOT call t.Parallel() in any subtest below.

func TestNudgeFor_GitCommitDetection(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    bool
	}{
		{"plain commit", `git commit -m "fix"`, true},
		{"commit with -am", `git commit -am "fix"`, true},
		{"commit with heredoc", "git commit -m \"$(cat <<'EOF'\nmsg\nEOF\n)\"", true},
		{"chained after cd", `cd foo && git commit -m "fix"`, true},
		{"commit --amend skipped", `git commit --amend -m "fix"`, false},
		{"commit --dry-run skipped", `git commit --dry-run`, false},
		{"git status not commit", `git status`, false},
		{"unrelated command", `ls -la`, false},
		{"git-commit-fish not real", `git-commit-fish`, false},
		{"git commit substring in arg", `echo "git commit -m hi"`, true}, // accepted — false positive but rare and benign
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Make a fake plancheck'd project so the gating beyond regex doesn't filter.
			dir := setupReflectFixture(t, "abc123", "")
			got := nudgeFor(tt.command, dir) != ""
			if got != tt.want {
				t.Errorf("nudgeFor(%q) emit=%v want %v", tt.command, got, tt.want)
			}
		})
	}
}

func TestNudgeFor_GatedOnConditions(t *testing.T) {
	cmd := `git commit -m "fix"`

	t.Run("emits when last-check-id set and unreflected", func(t *testing.T) {
		dir := setupReflectFixture(t, "abc123", "")
		if nudgeFor(cmd, dir) == "" {
			t.Error("expected nudge, got silence")
		}
	})

	t.Run("silent when no last-check-id", func(t *testing.T) {
		dir := setupReflectFixture(t, "", "")
		if msg := nudgeFor(cmd, dir); msg != "" {
			t.Errorf("expected silence, got: %s", msg)
		}
	})

	t.Run("silent when outcome already recorded", func(t *testing.T) {
		dir := setupReflectFixture(t, "abc123", "clean")
		if msg := nudgeFor(cmd, dir); msg != "" {
			t.Errorf("expected silence (outcome recorded), got: %s", msg)
		}
	})

	t.Run("silent when no .defn dir", func(t *testing.T) {
		dir := setupReflectFixtureNoDefn(t, "abc123")
		if msg := nudgeFor(cmd, dir); msg != "" {
			t.Errorf("expected silence (no .defn), got: %s", msg)
		}
	})

	t.Run("silent when cwd empty", func(t *testing.T) {
		if msg := nudgeFor(cmd, ""); msg != "" {
			t.Errorf("expected silence (empty cwd), got: %s", msg)
		}
	})

	t.Run("silent when reflection already recorded", func(t *testing.T) {
		dir := setupReflectFixtureWithReflection(t, "abc123", "clean")
		if msg := nudgeFor(cmd, dir); msg != "" {
			t.Errorf("expected silence (reflection recorded), got: %s", msg)
		}
	})
}

func TestNudgeFromReader_HappyPath(t *testing.T) {
	dir := setupReflectFixture(t, "abc123", "")
	payload, err := json.Marshal(map[string]interface{}{
		"cwd": dir,
		"tool_input": map[string]interface{}{
			"command": `git commit -m "fix"`,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	msg := nudgeFromReader(strings.NewReader(string(payload)))
	if msg == "" {
		t.Error("expected nudge, got silence")
	}
	if !strings.Contains(msg, "abc123") {
		t.Errorf("nudge missing check ID, got: %s", msg)
	}
}

func TestNudgeFromReader_SilentOnMalformedInput(t *testing.T) {
	// Establish fixture so a successful parse would yield a nudge — that way
	// silence on malformed input is evidence of the parser short-circuiting,
	// not just the gating logic suppressing everything.
	dir := setupReflectFixture(t, "abc123", "")
	cwdJSON, _ := json.Marshal(dir)
	cwdStr := string(cwdJSON)

	tests := []struct {
		name    string
		payload string
	}{
		{"empty stdin", ""},
		{"not JSON", "this is not JSON at all"},
		{"truncated JSON", `{"cwd":`},
		{"wrong type for command", `{"cwd":` + cwdStr + `,"tool_input":{"command":42}}`},
		{"missing tool_input", `{"cwd":` + cwdStr + `}`},
		{"missing cwd", `{"tool_input":{"command":"git commit -m x"}}`},
		{"empty object", `{}`},
		{"null", `null`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := nudgeFromReader(strings.NewReader(tt.payload))
			if msg != "" {
				t.Errorf("expected silence on %s, got: %s", tt.name, msg)
			}
		})
	}
}

func TestNudgeFromReader_BoundsOversizedInput(t *testing.T) {
	// A payload larger than maxNudgeInputBytes must not consume unbounded
	// memory. The exact behavior (silent vs nudge) depends on whether the
	// truncated prefix happens to be valid JSON; what we care about is that
	// the call returns in bounded time with bounded memory.
	dir := setupReflectFixture(t, "abc123", "")
	prefix := `{"cwd":"` + dir + `","tool_input":{"command":"`
	// Pad past the limit. The result is invalid JSON (no closing quote) but
	// the function must terminate without panic or OOM.
	padding := strings.Repeat("a", maxNudgeInputBytes+1024)
	payload := prefix + padding
	_ = nudgeFromReader(strings.NewReader(payload))
	// No assertion beyond "doesn't panic / hang." The LimitReader guarantees
	// at most maxNudgeInputBytes are read.
}

func TestNudgeFor_MessageIncludesCheckID(t *testing.T) {
	dir := setupReflectFixture(t, "x9k2lp", "")
	msg := nudgeFor(`git commit -m "x"`, dir)
	if !strings.Contains(msg, "x9k2lp") {
		t.Errorf("nudge message missing check ID, got: %s", msg)
	}
	if !strings.Contains(msg, "plancheck reflect") {
		t.Errorf("nudge message missing CLI invocation hint, got: %s", msg)
	}
}

// setupReflectFixture creates a temp dir with .defn/ and a fake plancheck
// project directory containing the given last-check-id. If outcome != "",
// an OutcomeEntry is appended to history.jsonl. Overrides
// history.ProjectDirFn so tests don't touch the real ~/.plancheck.
func setupReflectFixture(t *testing.T, lastCheckID, outcome string) string {
	t.Helper()
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, ".defn"), 0o755); err != nil {
		t.Fatal(err)
	}
	projDir := filepath.Join(tmp, ".plancheck-project")
	if err := os.MkdirAll(projDir, 0o700); err != nil {
		t.Fatal(err)
	}
	origFn := history.ProjectDirFn
	history.ProjectDirFn = func(cwd string) string { return projDir }
	t.Cleanup(func() { history.ProjectDirFn = origFn })

	if lastCheckID != "" {
		if err := os.WriteFile(filepath.Join(projDir, "last-check-id"), []byte(lastCheckID), 0o600); err != nil {
			t.Fatal(err)
		}
		// Write a HistoryEntry so LoadHistory yields a non-empty store. Without
		// this, RecordOutcome would also reject the ID but the nudge code path
		// uses LoadHistory which is permissive.
		entry := map[string]interface{}{
			"id":              lastCheckID,
			"timestamp":       time.Now().UTC().Format(time.RFC3339),
			"objective":       "test",
			"projectType":     "go",
			"comodMisses":     []string{},
			"suggestedModify": []string{},
		}
		writeJSONL(t, filepath.Join(projDir, "history.jsonl"), entry)
		if outcome != "" {
			oEntry := map[string]interface{}{
				"type":      "outcome",
				"id":        lastCheckID,
				"outcome":   outcome,
				"timestamp": time.Now().UTC().Format(time.RFC3339),
			}
			writeJSONL(t, filepath.Join(projDir, "history.jsonl"), oEntry)
		}
	}
	return tmp
}

// setupReflectFixtureWithReflection is like setupReflectFixture but writes
// a ReflectionEntry instead of an OutcomeEntry — exercises the gating path
// at reflect_nudge.go via summary.Reflections rather than summary.Outcomes.
func setupReflectFixtureWithReflection(t *testing.T, lastCheckID, outcome string) string {
	t.Helper()
	dir := setupReflectFixture(t, lastCheckID, "")
	if lastCheckID == "" {
		return dir
	}
	projDir := history.ProjectDirFn(dir)
	rEntry := map[string]interface{}{
		"type":             "reflection",
		"id":               lastCheckID,
		"passes":           2,
		"probe_findings":   0,
		"persona_findings": 0,
		"missed":           "",
		"outcome":          outcome,
		"signals_useful":   []string{},
		"timestamp":        time.Now().UTC().Format(time.RFC3339),
	}
	writeJSONL(t, filepath.Join(projDir, "history.jsonl"), rEntry)
	return dir
}

// setupReflectFixtureNoDefn creates a fixture WITHOUT the .defn directory,
// to verify the gate that suppresses nudges in un-plancheck'd projects.
func setupReflectFixtureNoDefn(t *testing.T, lastCheckID string) string {
	t.Helper()
	tmp := t.TempDir()
	projDir := filepath.Join(tmp, ".plancheck-project")
	if err := os.MkdirAll(projDir, 0o700); err != nil {
		t.Fatal(err)
	}
	origFn := history.ProjectDirFn
	history.ProjectDirFn = func(cwd string) string { return projDir }
	t.Cleanup(func() { history.ProjectDirFn = origFn })
	if err := os.WriteFile(filepath.Join(projDir, "last-check-id"), []byte(lastCheckID), 0o600); err != nil {
		t.Fatal(err)
	}
	return tmp
}

func writeJSONL(t *testing.T, path string, entry map[string]interface{}) {
	t.Helper()
	line, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		t.Fatal(err)
	}
}
