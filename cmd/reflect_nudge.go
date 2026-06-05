package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"

	"github.com/justinstimatze/plancheck/internal/history"
)

type ReflectNudgeCmd struct{}

type nudgeInput struct {
	Cwd       string `json:"cwd"`
	ToolInput struct {
		Command string `json:"command"`
	} `json:"tool_input"`
}

// maxNudgeInputBytes caps stdin to keep a malformed or pathological hook
// payload from exhausting memory. The real Claude Code payload is a few KB;
// 1 MiB is generous and still bounded.
const maxNudgeInputBytes = 1 << 20

var (
	gitCommitRe     = regexp.MustCompile(`\bgit\s+commit\b`)
	commitSkipFlags = regexp.MustCompile(`--amend\b|--dry-run\b`)
)

func (c *ReflectNudgeCmd) Run() error {
	if msg := nudgeFromReader(os.Stdin); msg != "" {
		fmt.Println(msg)
	}
	return nil
}

// nudgeFromReader parses a Claude Code PostToolUse:Bash payload from r and
// returns the nudge string if conditions warrant one. Split from Run() so
// tests can exercise the parse path without manipulating os.Stdin.
func nudgeFromReader(r io.Reader) string {
	data, err := io.ReadAll(io.LimitReader(r, maxNudgeInputBytes))
	if err != nil {
		return ""
	}
	var in nudgeInput
	if err := json.Unmarshal(data, &in); err != nil {
		return ""
	}
	return nudgeFor(in.ToolInput.Command, in.Cwd)
}

// nudgeFor returns the system-reminder text if the conditions warrant one,
// or "" to stay silent. Split out for unit testing — no I/O beyond the
// history-store read.
func nudgeFor(command, cwd string) string {
	if !gitCommitRe.MatchString(command) {
		return ""
	}
	if commitSkipFlags.MatchString(command) {
		return ""
	}
	if cwd == "" {
		return ""
	}
	absCwd, err := filepath.Abs(cwd)
	if err != nil {
		return ""
	}
	if _, err := os.Stat(filepath.Join(absCwd, ".defn")); err != nil {
		return ""
	}
	id := history.LoadLastCheckID(absCwd)
	if id == "" {
		return ""
	}
	summary, err := history.LoadHistory(absCwd)
	if err != nil {
		return ""
	}
	if _, recorded := summary.Outcomes[id]; recorded {
		return ""
	}
	if _, reflected := summary.Reflections[id]; reflected {
		return ""
	}
	return fmt.Sprintf(
		"plancheck: you committed in a project with an unreflected check_plan call (id=%s). "+
			"Record the outcome so calibration data accumulates — call `record_outcome` (id=%s, outcome=clean|rework|failed) "+
			"or run `plancheck reflect <clean|rework|failed>` from %s. "+
			"If the commit doesn't relate to the most recent check_plan, skip.",
		id, id, absCwd)
}
