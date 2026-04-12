package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// SetupCmd configures Claude Code to use plancheck: MCP server, hooks, and skill file.
type SetupCmd struct {
	Binary string `help:"Path to the plancheck binary. Defaults to the current executable." default:""`
}

func (c *SetupCmd) Run() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("cannot determine home directory: %w", err)
	}

	binary := c.Binary
	if binary == "" {
		binary, err = os.Executable()
		if err != nil {
			return fmt.Errorf("cannot determine executable path: %w", err)
		}
		binary, _ = filepath.Abs(binary)
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
		return setupSkill(home)
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

	if _, exists := servers["plancheck"]; exists {
		return nil // already configured
	}

	servers["plancheck"] = map[string]interface{}{
		"type":    "stdio",
		"command": binary,
		"args":    []string{"mcp"},
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

// buildSuggestHook returns the PostToolUse hook script that calls `plancheck suggest`
// after Go file edits. Fire-and-forget: no `set -e`, always exits 0. Positive guards
// avoid the `[ -z "$a" ] || [ -z "$b" ] && exit 0` precedence trap that silently
// kills the script when both vars are non-empty.
func buildSuggestHook(binary string) string {
	return fmt.Sprintf(`#!/bin/bash
# PostToolUse: plancheck suggest after Go file edits. Shows MUST CHANGE only.
# Fire-and-forget: no set -e, always exits 0.
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
R=$(echo "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/call\",\"params\":{\"name\":\"suggest\",\"arguments\":{\"files_touched\":$(echo "$FJ" | python3 -c "import sys,json; print(json.dumps(sys.stdin.read().strip()))"),\"cwd\":\"$CWD\"}}}" | timeout 30 %s mcp 2>/dev/null | python3 -c "
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

func setupSkill(home string) error {
	skillDir := filepath.Join(home, ".claude", "skills", "check-plan")
	_ = os.MkdirAll(skillDir, 0o700)
	skillPath := filepath.Join(skillDir, "SKILL.md")

	if _, err := os.Stat(skillPath); err == nil {
		return nil // already exists
	}

	skill := `---
name: check-plan
description: Adaptive mesh refinement for plans — bidirectional verification that catches what single-pass planning misses
context: fork
---

Verify plans by tracing them from both ends. Trace forward from the current state. Trace backward from the goal. Compare where they meet. If the gap is too big, pick a midpoint and repeat. Disagreements between the forward and backward traces are the findings.

Works for any plan: software, infrastructure, data pipelines, project plans, anything.

---

## Before starting — Project knowledge

Read ` + "`~/.plancheck/projects/<hash>/knowledge.md`" + ` if it exists (call ` + "`get_last_check_id`" + ` with cwd to confirm the project is known). Use it to:
- Pre-load files that are always forgotten
- Focus on known risk areas
- Skip probes known to false-positive for this project

If the file doesn't exist, proceed normally — it will be created after the first reflection.

---

## Pass 0 — Deterministic probes (code projects only)

**When to run:** The plan touches source files AND the ` + "`check_plan`" + ` MCP tool is available. Skip for non-code plans.

1. Serialize the plan as ExecutionPlan JSON
2. Call ` + "`check_plan`" + ` with plan_json and cwd
3. Note the ` + "`historyId`" + ` — hold it for the reflection at the end
4. Fix any missingFiles findings before continuing
5. If ` + "`projectPatterns`" + ` contains recurring-miss files, add them to the plan or explain why they're not needed

**Use probe signals during verification:**
- Churn hotspots — merge conflict risk between steps
- Test pairing — test files that need updating
- Lock staleness — missing install step
- Import chains — API compatibility between changed files

**Context recovery:** If context was compacted and you lost the ` + "`historyId`" + `, call ` + "`get_last_check_id`" + ` with the project's cwd to recover it.

---

## Verification algorithm

### Step 1 — Trace forward

Start from the current project state. Walk through the first few steps of the plan.

For each step, state:
- **Before**: what exists, what's true before this step
- **Action**: what this step does
- **After**: what changed, what now exists

Stop when you're no longer confident about what state you're in. State explicitly: "Forward trace reaches [state] after step N."

### Step 2 — Trace backward from goal

Start from the completed goal. Work backward: "The goal is done. What were the last few steps that produced it?"

For each step (in reverse), state:
- **After**: what must be true after this step
- **Action**: what this step does
- **Before**: what must be true before this step for it to succeed

Stop when the required pre-state is no longer obvious from the goal alone. State explicitly: "Backward trace requires [state] before step M."

### Step 3 — Compare

Compare where the forward trace stopped (state after step N) with where the backward trace needs to start (state before step M).

Three outcomes:

1. **They agree.** The states match. The gap between them is empty or obvious. Done.

2. **They disagree but the gap is small.** You can bridge it in a few confident steps. Write them. Done.

3. **They disagree and the gap is large.** You can't reliably trace the steps between them. **Subdivide.**

### Step 4 — Subdivide

Pick the most important intermediate milestone between the two traces — where the plan's state is most constrained (a deployment, a migration, an API boundary, a test gate).

Trace forward from this midpoint and backward to it. Now you have two smaller gaps. Compare each one. If either is still too large, subdivide again.

### Step 5 — Done

The plan is verified when every adjacent trace agrees on the state between them. Each handoff is consistent: one trace's "after" matches the next trace's "before."

**Bounds:**
- Max recursion depth: 3 (at most 8 segments)
- Simple plans (5 steps or fewer, 3 files or fewer): forward + backward + single comparison is usually enough. Don't subdivide unless the comparison genuinely fails.
- Hard cap: 5 total trace passes

**What disagreements reveal:**
- State disagreements — missing steps, wrong ordering, implicit dependencies
- File list disagreements — intermediate artifacts the plan doesn't account for
- Assumption disagreements — different traces assumed different things about the environment

These disagreements ARE the findings.

---

## Output format

**Be terse.** The tracing is internal work. The user sees:

` + "```" + `
[Traces]: forward (steps 1-N), backward (steps M-end), [K midpoints if any]
[Handoffs]: X states checked between traces
[Conflicts]: Y disagreements found
- [steps N-M] [disagreement] -> [fix]
[Result]: Plan [verified | updated — re-checking]
` + "```" + `

After verification, present the final plan with all fixes applied. Note which changes came from trace disagreements vs deterministic probes.

---

## Guardrails

- If forward and backward traces agree perfectly on the first attempt for a plan with >5 steps, pause. Perfect agreement on a complex plan usually means both traces are making the same optimistic assumptions. State what assumptions they share and stress-test the most fragile one.
- The backward trace must reason from the goal, not from the forward trace. If you find yourself just extending the forward trace to the end, start over from "the goal is done."
- Subdivide at natural seams (API boundaries, deployment gates, data migrations), not at arbitrary step numbers.

---

## Post-execution reflection (automatic)

When execution completes — all tasks finished, user confirms working, or failure hit — call ` + "`record_reflection`" + `:

- ` + "`id`" + `: historyId from check_plan (if it ran), or omit
- ` + "`cwd`" + `: project root
- ` + "`passes`" + `: number of verification passes completed (count each forward+backward+compare as one pass; minimum 2)
- ` + "`probe_findings`" + `: findings from deterministic probes (check_plan) that changed the plan
- ` + "`persona_findings`" + `: findings from trace disagreements that changed the plan
- ` + "`missed`" + `: what went wrong during execution that no pass caught (empty string if nothing)
- ` + "`outcome`" + `: clean / rework / failed (your assessment)

Call it automatically. Do not ask the user to classify the outcome.

**After reflection, update the project's ` + "`knowledge.md`" + `** (stored in ` + "`~/.plancheck/projects/<hash>/`" + `). Keep it short (10-20 lines). Structure:

` + "```markdown" + `
# Project: <name>
## What works
- <patterns that produce useful findings for this project>
## What doesn't
- <probes/signals that false-positive here>
## Always check
- <files that are always forgotten — from recurring-miss patterns>
## Risk areas
- <places where forward and backward traces tend to disagree>
` + "```" + `

If the file exists, update it incrementally. If it doesn't, create it.

---

## Graceful degradation

| Condition | What degrades | What still works |
|-----------|--------------|-----------------|
| No ` + "`check_plan`" + ` MCP tool | No probes, no history | Verification passes run normally |
| No git history | Co-mod empty, churn/signals empty | File existence checks still run |
| Greenfield project | File existence finds nothing | Validation signals still run |
| ` + "`PLANCHECK_NOHISTORY=1`" + ` set | No history, no patterns | All probes run stateless |
| Remote/headless | History may fail to write | Probes still return results |
| Cowork (multiple agents) | Each agent runs its own fork | History is append-only JSONL |

Verification passes require nothing except the plan text and the goal. Deterministic probes require the MCP tool + a project directory. Each layer fails independently.

---

## Why this works

1. A single forward pass can't reliably simulate a 15-step plan. But it can reliably do 3-4 steps from a known state. Decompose until each piece is within range.
2. Forward and backward passes catch categorically different errors. Forward finds missing prerequisites and ordering issues. Backward finds missing arrival conditions and unstated assumptions.
3. Disagreements between independently-traced segments are real gaps, not hypothetical concerns.
4. Subdivide where the plan is uncertain, not where it's long.
5. Git history and file existence are ground truth the model can't generate on its own.
6. Every disagreement must have a fix. Identifying a gap without resolving it is noise.
`

	return os.WriteFile(skillPath, []byte(skill), 0o600)
}
