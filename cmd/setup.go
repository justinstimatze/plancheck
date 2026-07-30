package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/justinstimatze/plancheck/internal/skill"
)

// SetupCmd configures Claude Code to use plancheck: MCP server, hooks, and skill file.
type SetupCmd struct {
	Binary     string `help:"Path to the plancheck binary. Defaults to the current executable." default:""`
	ForceSkill bool   `help:"Overwrite the installed check-plan skill even if it has local edits." name:"force-skill"`
}

func (c *SetupCmd) Run() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("cannot determine home directory: %w", err)
	}

	binary := c.Binary
	if binary == "" {
		// Prefer ~/go/bin/plancheck — stable across `go install` upgrades.
		// Fall back to the current executable only if go/bin doesn't exist.
		gobin := filepath.Join(home, "go", "bin", "plancheck")
		if _, err := os.Stat(gobin); err == nil {
			binary = gobin
		} else {
			binary, err = os.Executable()
			if err != nil {
				return fmt.Errorf("cannot determine executable path: %w", err)
			}
			binary, _ = filepath.Abs(binary)
		}
	}

	var anyFailed bool
	step := func(name string, fn func() error) {
		if err := fn(); err != nil {
			fmt.Printf("  ✗ %s — %v\n", name, err)
			anyFailed = true
		} else {
			fmt.Printf("  ✓ %s\n", name)
		}
	}

	fmt.Println("plancheck setup")
	fmt.Println()

	// 0. Check defn is available
	step("defn binary", func() error {
		_, err := exec.LookPath("defn")
		if err != nil {
			// Check ~/go/bin/defn
			gobin := filepath.Join(home, "go", "bin", "defn")
			if _, err2 := os.Stat(gobin); err2 != nil {
				return fmt.Errorf("defn not found. Install: go install github.com/justinstimatze/defn@latest")
			}
		}
		return nil
	})

	// 1. MCP server in ~/.claude.json
	step("MCP server in ~/.claude.json", func() error {
		return setupMCP(home, binary)
	})

	// 2. Hooks in ~/.claude/settings.json (gate + suggest)
	step("Hooks in ~/.claude/settings.json", func() error {
		return setupHooks(home, binary)
	})

	// 3. Git pre-commit hook (symlink from hooks/pre-commit)
	step("Git pre-commit hook", func() error {
		return setupGitHook(binary)
	})

	// 4. Skill file
	step("Skill file", func() error {
		return setupSkill(home, c.ForceSkill)
	})

	fmt.Println()
	if anyFailed {
		fmt.Println("Some steps failed. Fix the issues above and re-run.")
		os.Exit(1)
	}
	fmt.Println("Setup complete. Run `plancheck doctor` to verify.")
	return nil
}

func setupGitHook(binary string) error {
	// Find the hooks/pre-commit source relative to the plancheck binary
	binDir := filepath.Dir(binary)
	hookSrc := filepath.Join(binDir, "hooks", "pre-commit")

	// Also try relative to cwd (for development builds)
	if _, err := os.Stat(hookSrc); err != nil {
		hookSrc = "hooks/pre-commit"
		if _, err := os.Stat(hookSrc); err != nil {
			return nil // no hook source found, skip silently
		}
	}

	hookSrc, _ = filepath.Abs(hookSrc)

	// Find .git/hooks directory
	gitHooksDir := ".git/hooks"
	if _, err := os.Stat(gitHooksDir); err != nil {
		return nil // not a git repo, skip
	}

	hookDst := filepath.Join(gitHooksDir, "pre-commit")

	// Don't overwrite existing hook that isn't ours
	if data, err := os.ReadFile(hookDst); err == nil {
		if !strings.Contains(string(data), "plancheck review") {
			return fmt.Errorf("%s exists and isn't a plancheck hook — not overwriting", hookDst)
		}
	}

	// Symlink
	_ = os.Remove(hookDst)
	if err := os.Symlink(hookSrc, hookDst); err != nil {
		// Fallback: copy if symlink fails
		data, err := os.ReadFile(hookSrc)
		if err != nil {
			return err
		}
		return os.WriteFile(hookDst, data, 0o755)
	}
	return nil
}

func setupMCP(home, binary string) error {
	claudeJSON := filepath.Join(home, ".claude.json")

	var cfg map[string]interface{}
	data, err := os.ReadFile(claudeJSON)
	if err != nil {
		cfg = make(map[string]interface{})
	} else {
		if err := json.Unmarshal(data, &cfg); err != nil {
			return fmt.Errorf("cannot parse %s: %w", claudeJSON, err)
		}
	}

	servers, _ := cfg["mcpServers"].(map[string]interface{})
	if servers == nil {
		servers = make(map[string]interface{})
	}

	// Update command path if stale, or create new entry
	if pc, ok := servers["plancheck"].(map[string]interface{}); ok {
		if cmd, _ := pc["command"].(string); cmd == binary {
			return nil // already configured with correct path
		}
		pc["command"] = binary
	} else {
		servers["plancheck"] = map[string]interface{}{
			"type":    "stdio",
			"command": binary,
			"args":    []string{"mcp"},
		}
	}
	cfg["mcpServers"] = servers

	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	_ = os.MkdirAll(filepath.Dir(claudeJSON), 0o700)
	return os.WriteFile(claudeJSON, append(out, '\n'), 0o600)
}

func setupHooks(home, binary string) error {
	settingsJSON := filepath.Join(home, ".claude", "settings.json")
	_ = os.MkdirAll(filepath.Dir(settingsJSON), 0o700)

	var cfg map[string]interface{}
	data, err := os.ReadFile(settingsJSON)
	if err != nil {
		cfg = make(map[string]interface{})
	} else {
		if err := json.Unmarshal(data, &cfg); err != nil {
			return fmt.Errorf("cannot parse %s: %w", settingsJSON, err)
		}
	}

	// Write gate hook script (delegates to plancheck gate subcommand)
	hooksDir := filepath.Join(home, ".claude", "hooks")
	_ = os.MkdirAll(hooksDir, 0o700)

	gateScript := fmt.Sprintf(`#!/bin/bash
# PreToolUse: fires before ExitPlanMode.
# Delegates to plancheck gate for iteration enforcement.
exec %s gate
`, binary)

	gatePath := filepath.Join(hooksDir, "plancheck-gate.sh")
	if err := os.WriteFile(gatePath, []byte(gateScript), 0o755); err != nil {
		return fmt.Errorf("cannot write gate hook: %w", err)
	}

	// Write suggest hook script (calls plancheck suggest after Go file edits)
	suggestScript := buildSuggestHook(binary)
	suggestPath := filepath.Join(hooksDir, "plancheck-suggest.sh")
	if err := os.WriteFile(suggestPath, []byte(suggestScript), 0o755); err != nil {
		return fmt.Errorf("cannot write suggest hook: %w", err)
	}

	// Write reflect-nudge hook script (PostToolUse:Bash — nudge after git commit
	// in a plancheck'd project when the latest check has no recorded outcome).
	nudgeScript := buildNudgeHook(binary)
	nudgePath := filepath.Join(hooksDir, "plancheck-reflect-nudge.sh")
	if err := os.WriteFile(nudgePath, []byte(nudgeScript), 0o755); err != nil {
		return fmt.Errorf("cannot write reflect-nudge hook: %w", err)
	}

	// Clean up old mark hook if it exists
	oldMarkPath := filepath.Join(hooksDir, "plancheck-mark.sh")
	_ = os.Remove(oldMarkPath)

	// Configure hooks in settings.json
	hooks, _ := cfg["hooks"].(map[string]interface{})
	if hooks == nil {
		hooks = make(map[string]interface{})
	}

	// PreToolUse — ExitPlanMode gate
	preHooks, _ := hooks["PreToolUse"].([]interface{})
	hasGate := false
	for _, h := range preHooks {
		if hm, ok := h.(map[string]interface{}); ok {
			if hm["matcher"] == "ExitPlanMode" {
				hasGate = true
				break
			}
		}
	}
	if !hasGate {
		preHooks = append(preHooks, map[string]interface{}{
			"matcher": "ExitPlanMode",
			"hooks": []interface{}{
				map[string]interface{}{
					"type":    "command",
					"command": gatePath,
				},
			},
		})
		hooks["PreToolUse"] = preHooks
	}

	// PostToolUse — suggest hook on Edit/Write
	postHooks, _ := hooks["PostToolUse"].([]interface{})
	// Remove old mark hook, keep other hooks
	var filteredPost []interface{}
	hasSuggest := false
	for _, h := range postHooks {
		if hm, ok := h.(map[string]interface{}); ok {
			if m, _ := hm["matcher"].(string); m == "mcp__plancheck__check_plan" {
				continue // remove old mark hook
			}
			// Check if suggest hook already configured
			if innerHooks, ok := hm["hooks"].([]interface{}); ok {
				for _, ih := range innerHooks {
					if ihm, ok := ih.(map[string]interface{}); ok {
						if cmd, _ := ihm["command"].(string); cmd == suggestPath {
							hasSuggest = true
						}
					}
				}
			}
		}
		filteredPost = append(filteredPost, h)
	}

	if !hasSuggest {
		// Find existing Edit|Write matcher or create one
		found := false
		for i, h := range filteredPost {
			if hm, ok := h.(map[string]interface{}); ok {
				if m, _ := hm["matcher"].(string); m == "Edit|Write" {
					// Add suggest hook to existing matcher
					innerHooks, _ := hm["hooks"].([]interface{})
					innerHooks = append(innerHooks, map[string]interface{}{
						"type":    "command",
						"command": suggestPath,
					})
					hm["hooks"] = innerHooks
					filteredPost[i] = hm
					found = true
					break
				}
			}
		}
		if !found {
			filteredPost = append(filteredPost, map[string]interface{}{
				"matcher": "Edit|Write",
				"hooks": []interface{}{
					map[string]interface{}{
						"type":    "command",
						"command": suggestPath,
					},
				},
			})
		}
	}

	// PostToolUse:Bash — reflect-nudge after git commit
	hasNudge := false
	for _, h := range filteredPost {
		if hm, ok := h.(map[string]interface{}); ok {
			if m, _ := hm["matcher"].(string); m == "Bash" {
				if innerHooks, ok := hm["hooks"].([]interface{}); ok {
					for _, ih := range innerHooks {
						if ihm, ok := ih.(map[string]interface{}); ok {
							if cmd, _ := ihm["command"].(string); cmd == nudgePath {
								hasNudge = true
							}
						}
					}
				}
			}
		}
	}
	if !hasNudge {
		found := false
		for i, h := range filteredPost {
			if hm, ok := h.(map[string]interface{}); ok {
				if m, _ := hm["matcher"].(string); m == "Bash" {
					innerHooks, _ := hm["hooks"].([]interface{})
					innerHooks = append(innerHooks, map[string]interface{}{
						"type":    "command",
						"command": nudgePath,
					})
					hm["hooks"] = innerHooks
					filteredPost[i] = hm
					found = true
					break
				}
			}
		}
		if !found {
			filteredPost = append(filteredPost, map[string]interface{}{
				"matcher": "Bash",
				"hooks": []interface{}{
					map[string]interface{}{
						"type":    "command",
						"command": nudgePath,
					},
				},
			})
		}
	}

	if len(filteredPost) > 0 {
		hooks["PostToolUse"] = filteredPost
	}

	cfg["hooks"] = hooks

	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(settingsJSON, append(out, '\n'), 0o600)
}

// buildNudgeHook returns the PostToolUse:Bash hook script that nudges Claude
// to record an outcome after a git commit in a plancheck'd project. Bash-side
// short-circuit: if the JSON payload doesn't contain "commit" anywhere, skip
// the Go invocation. The hook runs on every Bash tool call, so most
// invocations (ls, cat, etc.) should not pay Go cold-start cost. The Go
// binary still does precise regex filtering for the hits — the bash check
// is only a cheap pre-filter.
func buildNudgeHook(binary string) string {
	return fmt.Sprintf(`#!/bin/bash
# PostToolUse: fires after Bash. Pre-filters in shell for perf, hands the
# JSON to plancheck for precise filtering and the silent/emit decision.
INPUT=$(cat)
case "$INPUT" in
  *commit*) printf '%%s' "$INPUT" | %s reflect-nudge ;;
esac
exit 0
`, binary)
}

// buildSuggestHook returns the PostToolUse hook script that calls `plancheck suggest`
// after Go file edits. Fire-and-forget: no `set -e`, always exits 0. Positive guards
// avoid the `[ -z "$a" ] || [ -z "$b" ] && exit 0` precedence trap that silently
// kills the script when both vars are non-empty.
func buildSuggestHook(binary string) string {
	return fmt.Sprintf(`#!/bin/bash
# PostToolUse: plancheck suggest after Go file edits. Shows MUST CHANGE only.
# Fire-and-forget: no set -e, always exits 0. Unexpected failures log to
# ~/.plancheck/hook-errors.log so plancheck doctor can surface them.

ERR_LOG="$HOME/.plancheck/hook-errors.log"
log_err() {
  mkdir -p "$HOME/.plancheck" 2>/dev/null
  echo "[$(date -u +%%Y-%%m-%%dT%%H:%%M:%%SZ)] $*" >> "$ERR_LOG" 2>/dev/null
  # Bounded log: keep last 100 lines once it exceeds 200
  if [ -f "$ERR_LOG" ] && [ "$(wc -l < "$ERR_LOG" 2>/dev/null || echo 0)" -gt 200 ]; then
    tail -100 "$ERR_LOG" > "$ERR_LOG.tmp" 2>/dev/null && mv "$ERR_LOG.tmp" "$ERR_LOG" 2>/dev/null
  fi
}

if ! command -v python3 >/dev/null 2>&1; then
  log_err "python3 not found — hook cannot parse Claude Code input payload"
  exit 0
fi

INPUT=$(cat)
CWD=$(echo "$INPUT" | python3 -c "import sys,json; print(json.load(sys.stdin).get('cwd',''))" 2>/dev/null || true)
FILE_PATH=$(echo "$INPUT" | python3 -c "import sys,json; print(json.load(sys.stdin).get('tool_input',{}).get('file_path',''))" 2>/dev/null || true)
if [ -z "$CWD" ] || [ -z "$FILE_PATH" ]; then exit 0; fi
case "$FILE_PATH" in *.go) ;; *) exit 0 ;; esac
case "$FILE_PATH" in *_test.go) exit 0 ;; esac
if [ ! -d "$CWD/.defn" ]; then exit 0; fi
SUGGEST_DIR="${XDG_RUNTIME_DIR:-$HOME/.plancheck/tmp}"
mkdir -p "$SUGGEST_DIR" 2>/dev/null
chmod 700 "$SUGGEST_DIR" 2>/dev/null
SF="$SUGGEST_DIR/suggest-$(echo "$CWD" | md5sum | cut -c1-8).txt"
REL="${FILE_PATH#$CWD/}"
touch "$SF"
if ! grep -qxF "$REL" "$SF" 2>/dev/null; then
  echo "$REL" >> "$SF"
fi
if [ "$(wc -l < "$SF" 2>/dev/null || echo 0)" -lt 2 ]; then exit 0; fi
FJ=$(python3 -c "import json; print(json.dumps([l.strip() for l in open('$SF') if l.strip()]))" 2>/dev/null)
if [ -z "$FJ" ]; then exit 0; fi

# Call plancheck mcp, capturing stderr separately so real failures get logged
MCP_ERR=$(mktemp 2>/dev/null || echo "/tmp/plancheck-hook-err.$$")
MCP_OUT=$(echo "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/call\",\"params\":{\"name\":\"suggest\",\"arguments\":{\"files_touched\":$(echo "$FJ" | python3 -c "import sys,json; print(json.dumps(sys.stdin.read().strip()))"),\"cwd\":\"$CWD\"}}}" | timeout 30 %s mcp 2>"$MCP_ERR")
MCP_STATUS=$?
if [ $MCP_STATUS -ne 0 ] && [ $MCP_STATUS -ne 124 ]; then
  # 124 is timeout, which is expected under heavy load; log anything else
  log_err "plancheck mcp failed (exit $MCP_STATUS): $(head -c 200 "$MCP_ERR" 2>/dev/null)"
fi
rm -f "$MCP_ERR"

R=$(echo "$MCP_OUT" | python3 -c "
import json, sys
for line in sys.stdin:
    line = line.strip()
    if not line: continue
    try:
        d = json.loads(line)
        if 'result' in d:
            for c in d['result'].get('content', []):
                if c.get('type') == 'text' and 'MUST CHANGE' in c['text']:
                    print(c['text'])
            break
    except: pass
" 2>/dev/null)
if [ -n "$R" ]; then echo "$R"; fi
exit 0
`, binary)
}

// setupSkill installs the check-plan skill, upgrading an older unmodified
// install in place. An edited skill is reported rather than clobbered — the
// caller has to pass --force-skill to lose their changes.
func setupSkill(home string, force bool) error {
	before, err := skill.Install(home, force)
	if err != nil {
		return err
	}
	if before == skill.StatusModified && !force {
		return fmt.Errorf("%s has local edits — left as-is. To replace it with this version: plancheck setup --force-skill", skill.Path(home))
	}
	return nil
}
