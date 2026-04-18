package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/justinstimatze/plancheck/internal/refgraph"
)

type DoctorCmd struct{}

func (c *DoctorCmd) Run() error {
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.TempDir()
	}

	ok := true
	check := func(name string, pass bool, detail string) {
		if pass {
			fmt.Printf("  ✓ %s\n", name)
		} else {
			fmt.Printf("  ✗ %s — %s\n", name, detail)
			ok = false
		}
	}
	// warn is like check but non-blocking — prints a ⚠ advisory without
	// flipping overall ok. Use for "needs user action" findings that
	// aren't broken configuration (e.g. stale data).
	warn := func(name string, pass bool, detail string) {
		if pass {
			fmt.Printf("  ✓ %s\n", name)
		} else {
			fmt.Printf("  \x1b[33m⚠\x1b[0m %s — %s\n", name, detail)
		}
	}

	fmt.Println("plancheck doctor")
	fmt.Println()

	// 1. Binary exists and is executable
	exe, _ := os.Executable()
	_, exeErr := os.Stat(exe)
	check("binary", exeErr == nil, fmt.Sprintf("%s not found", exe))

	// 2. MCP config in ~/.claude.json
	claudeJSON := filepath.Join(home, ".claude.json")
	claudeData, claudeErr := os.ReadFile(claudeJSON)
	hasMCP := false
	mcpPath := ""
	if claudeErr == nil {
		var cfg map[string]interface{}
		if json.Unmarshal(claudeData, &cfg) == nil {
			if servers, ok := cfg["mcpServers"].(map[string]interface{}); ok {
				if pc, ok := servers["plancheck"].(map[string]interface{}); ok {
					hasMCP = true
					if cmd, ok := pc["command"].(string); ok {
						mcpPath = cmd
					}
				}
			}
		}
	}
	check("MCP server in ~/.claude.json", hasMCP, "plancheck not found in mcpServers — run: claude mcp add --scope user plancheck -- /path/to/plancheck mcp")

	if hasMCP && mcpPath != "" {
		_, pathErr := os.Stat(mcpPath)
		check("MCP binary path valid", pathErr == nil, fmt.Sprintf("%s does not exist", mcpPath))
	}

	// 3. NOT in ~/.claude/settings.json (common misconfiguration)
	settingsJSON := filepath.Join(home, ".claude", "settings.json")
	settingsData, settingsErr := os.ReadFile(settingsJSON)
	staleMCP := false
	if settingsErr == nil {
		var cfg map[string]interface{}
		if json.Unmarshal(settingsData, &cfg) == nil {
			if servers, ok := cfg["mcpServers"].(map[string]interface{}); ok {
				if _, ok := servers["plancheck"]; ok {
					staleMCP = true
				}
			}
		}
	}
	check("no stale MCP in settings.json", !staleMCP, "plancheck in ~/.claude/settings.json does nothing — MCP servers belong in ~/.claude.json")

	// 4. Gate hook configured
	hasGateHook := false
	gateHookCmd := ""
	if settingsErr == nil {
		var cfg map[string]interface{}
		if json.Unmarshal(settingsData, &cfg) == nil {
			if hooks, ok := cfg["hooks"].(map[string]interface{}); ok {
				if pre, ok := hooks["PreToolUse"].([]interface{}); ok {
					for _, h := range pre {
						if hm, ok := h.(map[string]interface{}); ok {
							if hm["matcher"] == "ExitPlanMode" {
								hasGateHook = true
								if inner, ok := hm["hooks"].([]interface{}); ok && len(inner) > 0 {
									if ih, ok := inner[0].(map[string]interface{}); ok {
										if cmd, ok := ih["command"].(string); ok {
											gateHookCmd = cmd
										}
									}
								}
							}
						}
					}
				}
			}
		}
	}
	check("PreToolUse gate hook", hasGateHook, "no ExitPlanMode hook in ~/.claude/settings.json")

	// 4b. Gate hook uses new format (delegates to plancheck gate)
	if hasGateHook && gateHookCmd != "" {
		scriptData, err := os.ReadFile(gateHookCmd)
		stale := err == nil && !strings.Contains(string(scriptData), "gate")
		check("gate hook format", !stale, "gate hook uses old bash format — run: plancheck setup")
	}

	// 4c. Suggest hook configured
	hasSuggestHook := false
	if settingsErr == nil {
		var cfg map[string]interface{}
		if json.Unmarshal(settingsData, &cfg) == nil {
			if hooks, ok := cfg["hooks"].(map[string]interface{}); ok {
				if post, ok := hooks["PostToolUse"].([]interface{}); ok {
					for _, h := range post {
						if hm, ok := h.(map[string]interface{}); ok {
							if inner, ok := hm["hooks"].([]interface{}); ok {
								for _, ih := range inner {
									if ihm, ok := ih.(map[string]interface{}); ok {
										if cmd, ok := ihm["command"].(string); ok {
											if strings.Contains(cmd, "plancheck-suggest") {
												hasSuggestHook = true
											}
										}
									}
								}
							}
						}
					}
				}
			}
		}
	}
	check("PostToolUse suggest hook", hasSuggestHook, "no suggest hook — run: plancheck setup")

	// 4c. Detect pre-0.1.1 broken suggest hook (set -e + ||...&& precedence bug)
	suggestHookPath := filepath.Join(home, ".claude", "hooks", "plancheck-suggest.sh")
	if data, err := os.ReadFile(suggestHookPath); err == nil {
		content := string(data)
		hasBug := strings.Contains(content, "\nset -e\n") &&
			strings.Contains(content, "] || [") &&
			strings.Contains(content, "] && exit 0")
		check("suggest hook up-to-date", !hasBug, "suggest hook has the set -e bug — re-run: plancheck setup")
	}

	// 4c2. Surface RECENT hook failures from ~/.plancheck/hook-errors.log.
	// Only entries within the last 24h count — older errors are noise that
	// the user has either seen or fixed.
	hookLog := filepath.Join(home, ".plancheck", "hook-errors.log")
	if data, err := os.ReadFile(hookLog); err == nil {
		recent := recentHookErrors(string(data), time.Now(), 24*time.Hour)
		if len(recent) > 0 {
			latest := recent[len(recent)-1]
			msg := fmt.Sprintf("%d hook error(s) in last 24h, most recent: %s (full log: %s — rm to clear)", len(recent), latest, hookLog)
			check("no recent hook errors", false, msg)
		}
	}

	// 4d. defn binary available
	defnPath := ""
	if p, err := os.Executable(); err == nil {
		defnDir := filepath.Dir(p)
		candidate := filepath.Join(defnDir, "defn")
		if _, err := os.Stat(candidate); err == nil {
			defnPath = candidate
		}
	}
	if defnPath == "" {
		if p := filepath.Join(home, "go", "bin", "defn"); true {
			if _, err := os.Stat(p); err == nil {
				defnPath = p
			}
		}
	}
	check("defn binary", defnPath != "", "defn not found — install: go install github.com/justinstimatze/defn@latest")

	// 4e. defn graph in sync with working tree (only when doctor runs inside a defn-indexed project)
	// Non-blocking: stale data is actionable but not a broken config — scripts checking
	// exit status shouldn't fail on "needs re-ingest".
	cwd, _ := os.Getwd()
	if cwd != "" {
		if info, err := os.Stat(filepath.Join(cwd, ".defn")); err == nil && info.IsDir() {
			stale := refgraph.StaleFiles(cwd)
			msg := formatStaleCount(stale)
			if msg != "" {
				msg += ". Run: defn ingest"
			}
			warn("defn graph fresh", len(stale) == 0, msg)
		}
	}

	// 5. Skill file exists
	skillPath := filepath.Join(home, ".claude", "skills", "check-plan", "SKILL.md")
	_, skillErr := os.Stat(skillPath)
	check("skill file", skillErr == nil, fmt.Sprintf("%s not found", skillPath))

	// 6. Check for hash collisions in project dirs
	projectsDir := filepath.Join(home, ".plancheck", "projects")
	entries, _ := os.ReadDir(projectsDir)
	cwdsByHash := make(map[string][]string)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		projFile := filepath.Join(projectsDir, e.Name(), "project.txt")
		data, err := os.ReadFile(projFile)
		if err != nil {
			continue
		}
		cwd := strings.TrimSpace(string(data))
		cwdsByHash[e.Name()] = append(cwdsByHash[e.Name()], cwd)
	}
	collisions := 0
	for _, cwds := range cwdsByHash {
		if len(cwds) > 1 {
			collisions++
		}
	}
	check("no project hash collisions", collisions == 0, fmt.Sprintf("%d hash collisions detected in ~/.plancheck/projects/", collisions))

	// 7. Check for old base64 hash directories
	oldFormat := 0
	for _, e := range entries {
		name := e.Name()
		// Old base64 hashes start with uppercase letters (L2hv...), new SHA256 hex is all lowercase hex
		if len(name) == 16 && strings.ContainsAny(name[:1], "ABCDEFGHIJKLMNOPQRSTUVWXYZ+/") {
			oldFormat++
		}
	}
	check("no legacy hash directories", oldFormat == 0, fmt.Sprintf("%d old-format (base64) directories in ~/.plancheck/projects/ — delete them", oldFormat))

	fmt.Println()
	if ok {
		fmt.Println("All checks passed.")
	} else {
		fmt.Println("Some checks failed. Fix the issues above.")
		os.Exit(1)
	}
	return nil
}

// recentHookErrors parses hook-errors.log content and returns only entries
// whose ISO-8601 timestamp is within `window` of `now`. Lines without a
// parseable timestamp prefix are treated as old (excluded). Empty lines skipped.
//
// Log format: `[2026-04-11T12:00:00Z] message text`
func recentHookErrors(logContent string, now time.Time, window time.Duration) []string {
	cutoff := now.Add(-window)
	var recent []string
	for _, line := range strings.Split(strings.TrimRight(logContent, "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		// Extract `[timestamp]` prefix
		if !strings.HasPrefix(line, "[") {
			continue
		}
		end := strings.Index(line, "]")
		if end < 0 {
			continue
		}
		ts, err := time.Parse(time.RFC3339, line[1:end])
		if err != nil {
			continue
		}
		if ts.After(cutoff) || ts.Equal(cutoff) {
			recent = append(recent, line)
		}
	}
	return recent
}
