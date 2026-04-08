package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/justinstimatze/plancheck/internal/history"
	"github.com/justinstimatze/plancheck/internal/types"
)

// GateCmd implements the ExitPlanMode PreToolUse hook.
// It enforces that check_plan was called and the plan was verified
// with enough iterations for its complexity before allowing exit.
//
// Flow:
//  1. Model writes plan, calls check_plan → probes run
//  2. Model tries ExitPlanMode → gate blocks, says "verify the plan"
//  3. Model traces forward/backward, fixes disagreements, re-runs check_plan
//  4. For complex plans (>10 steps/files): gate blocks again for subdivision
//  5. Model subdivides, re-runs check_plan → gate allows
type GateCmd struct{}

// hookInput is the JSON structure Claude Code passes to PreToolUse hooks on stdin.
type hookInput struct {
	SessionID string                 `json:"session_id"`
	Cwd       string                 `json:"cwd"`
	ToolName  string                 `json:"tool_name"`
	ToolInput map[string]interface{} `json:"tool_input"`
}

// gateState tracks iteration across ExitPlanMode attempts within a session.
type gateState struct {
	Attempts   int      `json:"attempts"`
	ChecksSeen []string `json:"checksSeen"`
	Complexity int      `json:"complexity"` // max(steps, files touched)
	PlanHash   string   `json:"planHash"`   // detects plan rewrites → reset state
}

// gateDecision is the result of evaluating the gate logic.
type gateDecision struct {
	Allow   bool
	Message string
}

func (c *GateCmd) Run() error {
	if isDisabled() {
		return nil
	}

	data, err := io.ReadAll(os.Stdin)
	if err != nil || len(data) == 0 {
		return nil // no input — allow gracefully
	}

	var input hookInput
	if json.Unmarshal(data, &input) != nil {
		return nil // unparseable — allow gracefully
	}

	sessionID := input.SessionID
	if sessionID == "" {
		sessionID = os.Getenv("CLAUDE_SESSION_ID")
	}
	if sessionID == "" {
		sessionID = "nosession"
	}

	cwd := input.Cwd
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	cwd, _ = filepath.Abs(cwd)

	// Store gate state in the project directory (~/.plancheck/projects/<hash>/)
	// rather than /tmp, which may not persist across hook invocations due to
	// sandbox isolation. See https://github.com/justinstimatze/plancheck/issues/1
	projDir := history.ProjectDirFn(cwd)
	_ = os.MkdirAll(projDir, 0o700)
	stateFile := filepath.Join(projDir, fmt.Sprintf("gate-%s.json", sessionID))

	decision := evaluateGate(cwd, stateFile)
	if decision.Allow {
		os.Remove(stateFile)
		return nil
	}
	fmt.Fprintln(os.Stderr, decision.Message)
	os.Exit(2)
	return nil // unreachable
}

// requiredChecks returns the number of distinct check_plan runs needed
// based on plan complexity. More complex plans need more verification rounds.
func requiredChecks(complexity int) int {
	if complexity <= 4 {
		return 1 // trivial: single check sufficient
	}
	if complexity <= 10 {
		return 2 // forward + backward + compare
	}
	if complexity <= 20 {
		return 3 // + 1 subdivision round
	}
	return 4 // + 2 subdivision rounds
}

// evaluateGate contains the testable gate logic.
func evaluateGate(cwd, stateFile string) gateDecision {
	var state gateState
	if data, err := os.ReadFile(stateFile); err == nil {
		_ = json.Unmarshal(data, &state)
	}
	state.Attempts++

	// Check if check_plan has been called for this project
	checkID := history.LoadLastCheckID(cwd)
	if checkID == "" {
		if state.Attempts >= 3 {
			// Graceful degradation: allow after 3 attempts with no probes.
			// Covers non-code plans and environments where check_plan isn't available.
			return gateDecision{Allow: true}
		}
		saveGateState(stateFile, state)
		return gateDecision{
			Allow:   false,
			Message: "BLOCKED: call check_plan before exiting plan mode. Serialize the plan as ExecutionPlan JSON and call the check_plan MCP tool.",
		}
	}

	// Load the actual check results
	result, err := history.LoadCheckResult(cwd)
	if err != nil {
		// Can't load results — allow gracefully
		return gateDecision{Allow: true}
	}

	// Detect plan rewrites: if the plan hash changed since the last check,
	// reset iteration state so stale findings don't block the new plan.
	if result.PlanHash != "" && state.PlanHash != "" && result.PlanHash != state.PlanHash {
		state.ChecksSeen = nil
		state.Complexity = 0
	}
	if result.PlanHash != "" {
		state.PlanHash = result.PlanHash
	}

	// Block on missing files — ground-truth findings the plan must fix
	if len(result.MissingFiles) > 0 {
		saveGateState(stateFile, state)
		msg := fmt.Sprintf("BLOCKED: %d missing file(s) in plan:", len(result.MissingFiles))
		for _, mf := range result.MissingFiles {
			msg += fmt.Sprintf("\n  - %s (%s): %s", mf.File, mf.List, mf.Suggestion)
		}
		msg += "\nFix the plan and re-run check_plan."
		return gateDecision{Allow: false, Message: msg}
	}

	// Count high-confidence comod gaps (warn but don't permanently block).
	// Comod gaps block on the FIRST attempt to force the model to review them,
	// but the iteration counter still advances so the gate eventually allows.
	// Missing files (above) are the only permanent hard blocker.
	var highConfUnacked []types.ComodGap
	for _, gap := range result.ComodGaps {
		if gap.Confidence == "high" && !gap.Acknowledged && !gap.Hub {
			highConfUnacked = append(highConfUnacked, gap)
		}
	}

	// Record this check if we haven't seen it before
	seen := false
	for _, id := range state.ChecksSeen {
		if id == checkID {
			seen = true
			break
		}
	}
	if !seen {
		state.ChecksSeen = append(state.ChecksSeen, checkID)
		// Update complexity from latest check result
		steps := result.PlanStats.Steps
		files := result.PlanStats.FilesToModify + result.PlanStats.FilesToCreate
		if files > steps {
			state.Complexity = files
		} else {
			state.Complexity = steps
		}
	}

	// Adjust required checks based on forecast risk and novelty
	required := requiredChecks(state.Complexity)

	// High-risk forecast → require extra round
	if result.Forecast != nil && result.Forecast.PFailed > 0.4 && required < 4 {
		required++
	}

	// High novelty → require extra round (more unknowns to verify)
	if result.Novelty != nil && result.Novelty.Score > 0.5 && required < 4 {
		required++
	}

	checksCompleted := len(state.ChecksSeen)

	if checksCompleted < required {
		saveGateState(stateFile, state)
		msg := phaseMessage(checksCompleted, required, state.Complexity)

		// Add comod gap context on first block only
		if len(highConfUnacked) > 0 && checksCompleted <= 1 {
			msg += fmt.Sprintf("\n\nCo-mod: %d high-confidence gap(s) — review these and either add to filesToModify or acknowledge in acknowledgedComod:", len(highConfUnacked))
			shown := highConfUnacked
			if len(shown) > 5 {
				shown = shown[:5]
			}
			for _, gap := range shown {
				msg += fmt.Sprintf("\n  - %s co-changes with %s %d%% of the time", gap.ComodFile, gap.PlanFile, int(gap.Frequency*100+0.5))
			}
			if len(highConfUnacked) > 5 {
				msg += fmt.Sprintf("\n  ... and %d more", len(highConfUnacked)-5)
			}
		}

		// Add forecast context to block message
		if result.Forecast != nil && result.Forecast.PFailed > 0.3 {
			msg += fmt.Sprintf("\n\nForecast: %.0f%% risk of significant rework (based on %d similar plans). %s",
				result.Forecast.PFailed*100, result.Forecast.BasedOn, result.Forecast.Summary)
		}

		// Add novelty context
		if result.Novelty != nil && result.Novelty.Score > 0.4 {
			msg += fmt.Sprintf("\n\nNovelty: %s (%s). %s",
				result.Novelty.Label, result.Novelty.Uncertainty, result.Novelty.Guidance)
		}

		if summary := findingsSummary(result); summary != "" {
			msg += "\n\nFindings from last check:\n" + summary
		}
		return gateDecision{
			Allow:   false,
			Message: msg,
		}
	}

	// Enough checks completed — allow
	return gateDecision{Allow: true}
}

// phaseMessage returns the appropriate block message for the current verification phase.
func phaseMessage(checksCompleted, required, complexity int) string {
	remaining := required - checksCompleted

	switch checksCompleted {
	case 1:
		if remaining > 1 {
			return fmt.Sprintf(
				"BLOCKED: verify the plan (%d steps/files — %d verification rounds remaining). "+
					"Trace it forward from current state and backward from the goal. "+
					"Compare where they meet — fix any disagreements. Then re-run check_plan.",
				complexity, remaining)
		}
		return "BLOCKED: verify the plan before exiting. " +
			"Trace it forward from current state and backward from the goal. " +
			"Compare where they meet — fix any disagreements. Then re-run check_plan and try ExitPlanMode again."
	case 2:
		return "BLOCKED: subdivide — the plan needs more verification. " +
			"Pick a midpoint in the largest gap between your forward and backward traces. " +
			"Trace forward and backward from that midpoint. Fix disagreements, then re-run check_plan."
	default:
		return "BLOCKED: verify the remaining segments. " +
			"Compare adjacent traces, fix any disagreements, then re-run check_plan."
	}
}

// findingsSummary returns a short text summary of actionable findings from a check result.
func findingsSummary(result types.PlanCheckResult) string {
	var lines []string
	for _, cg := range result.ComodGaps {
		if cg.Hub || cg.Acknowledged {
			continue
		}
		lines = append(lines, fmt.Sprintf("  - comod gap: %s is often modified with %s — %s", cg.ComodFile, cg.PlanFile, cg.Suggestion))
	}
	for _, s := range result.Signals {
		lines = append(lines, fmt.Sprintf("  - %s: %s", s.Probe, s.Message))
	}
	for _, c := range result.Critique {
		lines = append(lines, fmt.Sprintf("  - %s", c))
	}
	// Cap at 8 lines to keep messages scannable
	if len(lines) > 8 {
		lines = append(lines[:8], fmt.Sprintf("  ... and %d more (use get_check_details for full list)", len(lines)-8))
	}
	var out string
	for i, l := range lines {
		if i > 0 {
			out += "\n"
		}
		out += l
	}
	return out
}

func saveGateState(path string, state gateState) {
	data, _ := json.Marshal(state)
	_ = os.WriteFile(path, data, 0o600)
}
