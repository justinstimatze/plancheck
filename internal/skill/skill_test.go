package skill

import (
	"os"
	"path/filepath"
	"testing"
)

func writeInstalled(t *testing.T, home, content, stamp string) {
	t.Helper()
	if err := os.MkdirAll(Dir(home), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(home), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if stamp != "" {
		if err := os.WriteFile(filepath.Join(Dir(home), stampFile), []byte(stamp+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCheck_Missing(t *testing.T) {
	if got := Check(t.TempDir()); got != StatusMissing {
		t.Errorf("got %q, want %q", got, StatusMissing)
	}
}

func TestInstall_FreshThenCurrent(t *testing.T) {
	home := t.TempDir()
	before, err := Install(home, false)
	if err != nil {
		t.Fatal(err)
	}
	if before != StatusMissing {
		t.Errorf("prior status = %q, want %q", before, StatusMissing)
	}
	if got := Check(home); got != StatusCurrent {
		t.Errorf("after install: got %q, want %q", got, StatusCurrent)
	}

	// A second install is a no-op, not a rewrite.
	before, err = Install(home, false)
	if err != nil {
		t.Fatal(err)
	}
	if before != StatusCurrent {
		t.Errorf("second install prior status = %q, want %q", before, StatusCurrent)
	}
}

// An older version we installed ourselves is safe to replace: the stamp proves
// nobody edited it since.
func TestInstall_UpgradesUnmodifiedOldVersion(t *testing.T) {
	home := t.TempDir()
	old := "# an older shipped skill\n"
	writeInstalled(t, home, old, sum(old))

	if got := Check(home); got != StatusStale {
		t.Fatalf("got %q, want %q", got, StatusStale)
	}
	if _, err := Install(home, false); err != nil {
		t.Fatal(err)
	}
	if got := Check(home); got != StatusCurrent {
		t.Errorf("after upgrade: got %q, want %q", got, StatusCurrent)
	}
}

// Edited content must survive an ordinary setup run.
func TestInstall_PreservesLocalEditsWithoutForce(t *testing.T) {
	home := t.TempDir()
	shipped := "# an older shipped skill\n"
	edited := shipped + "\nmy own notes\n"
	writeInstalled(t, home, edited, sum(shipped))

	if got := Check(home); got != StatusModified {
		t.Fatalf("got %q, want %q", got, StatusModified)
	}
	if _, err := Install(home, false); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(Path(home))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != edited {
		t.Error("local edits were overwritten without force")
	}

	if _, err := Install(home, true); err != nil {
		t.Fatal(err)
	}
	if got := Check(home); got != StatusCurrent {
		t.Errorf("after forced install: got %q, want %q", got, StatusCurrent)
	}
}

// Every install from before fingerprint tracking lands here on first upgrade.
// It must not be reported as edited — usually it is an untouched old file —
// and it must still not be rewritten without force.
func TestCheck_UnstampedInstallIsUntrackedNotModified(t *testing.T) {
	home := t.TempDir()
	old := "# a skill from before stamping\n"
	writeInstalled(t, home, old, "")

	if got := Check(home); got != StatusUntracked {
		t.Errorf("got %q, want %q", got, StatusUntracked)
	}
	if _, err := Install(home, false); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(Path(home))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != old {
		t.Error("untracked install was rewritten without force")
	}

	if _, err := Install(home, true); err != nil {
		t.Fatal(err)
	}
	if got := Check(home); got != StatusCurrent {
		t.Errorf("after forced install: got %q, want %q", got, StatusCurrent)
	}
}

// A stamp that no longer matches the file is proof of an edit, which is a
// different claim from "no stamp" and gets a different message.
func TestCheck_StampMismatchIsModified(t *testing.T) {
	home := t.TempDir()
	shipped := "# what plancheck wrote\n"
	writeInstalled(t, home, shipped+"my notes\n", sum(shipped))

	if got := Check(home); got != StatusModified {
		t.Errorf("got %q, want %q", got, StatusModified)
	}
}

// The embedded markdown must actually be the skill, not an empty embed.
func TestMarkdownEmbedded(t *testing.T) {
	if len(Markdown) < 500 {
		t.Fatalf("embedded skill is %d bytes, expected the full document", len(Markdown))
	}
	if got := Markdown[:4]; got != "---\n" {
		t.Errorf("skill should open with frontmatter, got %q", got)
	}
}
