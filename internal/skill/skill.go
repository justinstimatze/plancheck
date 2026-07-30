// Package skill holds the check-plan skill markdown as the single source of
// truth and manages its installation into ~/.claude/skills/check-plan/.
//
// The markdown lived in two places before — a docs copy and a string literal
// in setup.go — and they drifted: the setup.go copy was missing four of the
// numbered Pass 0 steps, so `plancheck setup` installed a skill that never
// mentioned most of what check_plan returns. Embedding one file removes that
// failure mode at the source.
//
// Installation tracks a fingerprint of what it wrote, so a later upgrade can
// tell an untouched install (safe to replace) from one the user has edited
// (never replaced without an explicit force).
package skill

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
)

// Markdown is the check-plan skill installed by `plancheck setup`.
//
//go:embed SKILL.md
var Markdown string

// stampFile records the fingerprint of the markdown last written by plancheck.
// Its presence and value are what distinguish an untouched install from an
// edited one; without it we cannot tell the two apart and must not overwrite.
const stampFile = ".installed-sha256"

// Status describes the installed skill relative to the embedded one.
type Status string

const (
	// StatusMissing means no skill file is installed.
	StatusMissing Status = "missing"
	// StatusCurrent means the installed skill matches the embedded one.
	StatusCurrent Status = "current"
	// StatusStale means the installed skill is an older plancheck version,
	// unmodified since install, and safe to replace.
	StatusStale Status = "stale"
	// StatusUntracked means the installed skill carries no fingerprint at all.
	// Every install from before fingerprint tracking looks like this, so it is
	// the common case on first upgrade and usually just an old unedited file —
	// but nothing here can prove that, so it is never replaced without force.
	StatusUntracked Status = "untracked"
	// StatusModified means plancheck wrote this skill and the content has
	// changed since: someone edited it. Never replaced without force.
	StatusModified Status = "modified"
)

// Dir is the directory the skill is installed into.
func Dir(home string) string {
	return filepath.Join(home, ".claude", "skills", "check-plan")
}

// Path is the installed skill file.
func Path(home string) string { return filepath.Join(Dir(home), "SKILL.md") }

// Sum returns the fingerprint of the embedded markdown.
func Sum() string { return sum(Markdown) }

func sum(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// Check reports how the installed skill compares to the embedded one.
func Check(home string) Status {
	data, err := os.ReadFile(Path(home))
	if err != nil {
		return StatusMissing
	}
	installed := sum(string(data))
	if installed == Sum() {
		return StatusCurrent
	}
	stamp, err := os.ReadFile(filepath.Join(Dir(home), stampFile))
	if err != nil {
		// No fingerprint to compare against. Almost always an install from
		// before tracking existed, but "old and untouched" and "edited" are
		// indistinguishable without a stamp, so say unknown rather than
		// accusing the user of edits they may not have made.
		return StatusUntracked
	}
	if strings.TrimSpace(string(stamp)) == installed {
		return StatusStale
	}
	return StatusModified
}

// Install writes the embedded skill when it is missing or safely replaceable.
// A modified skill is left alone unless force is set. The returned Status is
// the state found before writing, so callers can report what they did.
func Install(home string, force bool) (Status, error) {
	status := Check(home)
	if status == StatusCurrent {
		return status, nil
	}
	if (status == StatusModified || status == StatusUntracked) && !force {
		return status, nil
	}
	if err := os.MkdirAll(Dir(home), 0o700); err != nil {
		return status, err
	}
	if err := os.WriteFile(Path(home), []byte(Markdown), 0o600); err != nil {
		return status, err
	}
	// Stamp last: a stamp without the matching file would misreport a later
	// hand-edit as stale and silently overwrite it.
	err := os.WriteFile(filepath.Join(Dir(home), stampFile), []byte(Sum()+"\n"), 0o600)
	return status, err
}
