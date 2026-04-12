package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestSuggestHook_ExitsCleanlyOnValidInput is the smoke test for the bug
// where `[ -z "$a" ] || [ -z "$b" ] && exit 0` under `set -e` silently killed
// the hook when both vars were non-empty (the normal case). See
// /tmp/plancheck-feedback-2026-04-11.md for the original report.
//
// The hook must exit 0 on every code path when given valid JSON input,
// regardless of whether .defn exists or plancheck is available.
func TestSuggestHook_ExitsCleanlyOnValidInput(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}

	// Write the hook to a temp file
	tmp := t.TempDir()
	hookPath := filepath.Join(tmp, "plancheck-suggest.sh")
	// Use a binary path that won't actually run — the hook should short-circuit
	// before reaching it because .defn doesn't exist in the fake cwd.
	script := buildSuggestHook("/nonexistent/plancheck")
	if err := os.WriteFile(hookPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		payload string
	}{
		{
			name:    "go file edit with no .defn",
			payload: `{"cwd":"` + tmp + `","tool_input":{"file_path":"` + tmp + `/foo.go"}}`,
		},
		{
			name:    "non-go file",
			payload: `{"cwd":"` + tmp + `","tool_input":{"file_path":"` + tmp + `/README.md"}}`,
		},
		{
			name:    "test file skipped",
			payload: `{"cwd":"` + tmp + `","tool_input":{"file_path":"` + tmp + `/foo_test.go"}}`,
		},
		{
			name:    "empty cwd",
			payload: `{"cwd":"","tool_input":{"file_path":"/tmp/foo.go"}}`,
		},
		{
			name:    "empty file_path",
			payload: `{"cwd":"/tmp","tool_input":{"file_path":""}}`,
		},
		{
			name:    "missing fields",
			payload: `{}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := exec.Command("bash", hookPath)
			cmd.Stdin = strings.NewReader(tt.payload)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("hook exited non-zero on valid input: %v\noutput: %s", err, string(out))
			}
		})
	}
}

// TestSuggestHook_LogsMCPFailure verifies that when the plancheck binary is
// missing (MCP call fails), the hook logs to ~/.plancheck/hook-errors.log
// instead of failing silently. This gives plancheck doctor a way to surface
// broken installs.
func TestSuggestHook_LogsMCPFailure(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}

	// Use a fake HOME so we don't pollute the real log
	fakeHome := t.TempDir()

	// Set up a fake project with .defn so the hook reaches the MCP call
	proj := filepath.Join(fakeHome, "project")
	if err := os.MkdirAll(filepath.Join(proj, ".defn"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Write the hook with a bogus plancheck binary path
	hookPath := filepath.Join(fakeHome, "plancheck-suggest.sh")
	script := buildSuggestHook("/nonexistent/plancheck-binary")
	if err := os.WriteFile(hookPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	// Touch two files so the hook actually makes the MCP call
	// (the count < 2 guard needs to be satisfied)
	runHook := func(filePath string) {
		cmd := exec.Command("bash", hookPath)
		cmd.Env = append(os.Environ(), "HOME="+fakeHome)
		cmd.Stdin = strings.NewReader(`{"cwd":"` + proj + `","tool_input":{"file_path":"` + filePath + `"}}`)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("hook exited non-zero: %v\noutput: %s", err, string(out))
		}
	}
	runHook(filepath.Join(proj, "a.go"))
	runHook(filepath.Join(proj, "b.go"))

	// Error log should now exist with at least one entry about the missing binary
	logPath := filepath.Join(fakeHome, ".plancheck", "hook-errors.log")
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("expected hook error log at %s, got: %v", logPath, err)
	}
	if !strings.Contains(string(data), "plancheck mcp failed") {
		t.Errorf("expected MCP failure in log, got: %s", string(data))
	}
}

// TestSuggestHook_NoSetE ensures the hook doesn't enable set -e, which would
// make it exit silently on any command returning non-zero. This is a guard
// against the original bug pattern regressing.
func TestSuggestHook_NoSetE(t *testing.T) {
	script := buildSuggestHook("/usr/bin/plancheck")
	for _, line := range strings.Split(script, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		// Match the bare `set -e` statement, not references in comments
		if trimmed == "set -e" || strings.HasPrefix(trimmed, "set -e ") || strings.HasPrefix(trimmed, "set -eu") || strings.HasPrefix(trimmed, "set -euo") {
			t.Errorf("suggest hook must not use `set -e` — it's fire-and-forget and must always exit 0. Found: %s", line)
		}
	}
	// The bug pattern: `[ ... ] || [ ... ] && exit 0`
	if strings.Contains(script, "] || [") && strings.Contains(script, "] && exit") {
		// This heuristic is imprecise but catches the exact anti-pattern.
		// Valid uses go inside `if ... then ... fi` blocks.
		for _, line := range strings.Split(script, "\n") {
			if strings.Contains(line, "] || [") && strings.Contains(line, "] && exit") {
				t.Errorf("suggest hook contains `||...&&` anti-pattern: %s", line)
			}
		}
	}
}
