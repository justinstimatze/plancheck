package cmd

import (
	"os"
	"testing"

	"github.com/justinstimatze/plancheck/internal/skill"
)

// setupSkill reports on the status found *before* writing, which made an
// earlier version report a successful --force-skill run as a failure.
func TestSetupSkill_ForceReportsSuccess(t *testing.T) {
	home := t.TempDir()
	old := "# a skill from before fingerprint tracking\n"
	if err := os.MkdirAll(skill.Dir(home), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skill.Path(home), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := setupSkill(home, false); err == nil {
		t.Error("without force, an untracked skill should be reported, not silently kept")
	}
	if data, _ := os.ReadFile(skill.Path(home)); string(data) != old {
		t.Error("without force, the existing skill should be left alone")
	}

	if err := setupSkill(home, true); err != nil {
		t.Errorf("with force, a successful write should not report an error: %v", err)
	}
	if data, _ := os.ReadFile(skill.Path(home)); string(data) != skill.Markdown {
		t.Error("with force, the skill should be replaced")
	}
}

func TestSetupSkill_FreshInstallSucceeds(t *testing.T) {
	home := t.TempDir()
	if err := setupSkill(home, false); err != nil {
		t.Fatalf("fresh install should succeed: %v", err)
	}
	if got := skill.Check(home); got != skill.StatusCurrent {
		t.Errorf("after install: got %q, want %q", got, skill.StatusCurrent)
	}
	// A second run is a no-op, not a reported failure.
	if err := setupSkill(home, false); err != nil {
		t.Errorf("re-running setup should succeed: %v", err)
	}
}
