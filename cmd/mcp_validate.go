package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/justinstimatze/plancheck/internal/history"
	"github.com/justinstimatze/plancheck/internal/simulate"
	"github.com/justinstimatze/plancheck/internal/types"
	"github.com/mark3labs/mcp-go/mcp"
)

func handleValidateExecution(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	cwd, ok := args["cwd"].(string)
	if !ok || cwd == "" {
		return mcp.NewToolResultError("cwd: required string argument"), nil
	}
	planJSON, ok := args["plan_json"].(string)
	if !ok || planJSON == "" {
		return mcp.NewToolResultError("plan_json: required string argument"), nil
	}
	baseRef, _ := args["base_ref"].(string)
	if baseRef == "" {
		baseRef = "HEAD~1"
	}
	if !validGitRef.MatchString(baseRef) {
		return mcp.NewToolResultError("base_ref: invalid git ref format"), nil
	}

	absCwd, _ := filepath.Abs(cwd)
	p, err := types.ParsePlan([]byte(planJSON))
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("Invalid plan: %v", err)), nil
	}

	// Check if cwd is a git repository
	revParse := exec.CommandContext(ctx, "git", "-C", absCwd, "rev-parse", "--is-inside-work-tree")
	if out, err := revParse.CombinedOutput(); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("git failed: %s", strings.TrimSpace(string(out)))), nil
	}

	// Get actual changed files from git
	cmd := exec.CommandContext(ctx, "git", "diff", "--name-only", baseRef)
	cmd.Dir = absCwd
	out, err := cmd.Output()
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("git diff failed: %v (is %s a valid ref?)", err, baseRef)), nil
	}

	actualFiles := make(map[string]bool)
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line != "" {
			actualFiles[line] = true
		}
	}

	plannedFiles := make(map[string]bool)
	for _, f := range p.FilesToModify {
		plannedFiles[f] = true
	}
	for _, f := range p.FilesToCreate {
		plannedFiles[f] = true
	}

	var unplanned, untouched []string
	for f := range actualFiles {
		if !plannedFiles[f] {
			unplanned = append(unplanned, f)
		}
	}
	for f := range plannedFiles {
		if !actualFiles[f] {
			untouched = append(untouched, f)
		}
	}
	sort.Strings(unplanned)
	sort.Strings(untouched)

	result := map[string]interface{}{
		"unplanned_modifications": unplanned,
		"planned_but_untouched":   untouched,
		"actual_files_changed":    len(actualFiles),
		"planned_files":           len(plannedFiles),
	}
	if unplanned == nil {
		result["unplanned_modifications"] = []string{}
	}
	if untouched == nil {
		result["planned_but_untouched"] = []string{}
	}

	// Add simulation replay if a saved simulation exists
	simResult, err := simulate.LoadSimulationPrediction(absCwd)
	if err == nil && simResult != nil {
		replay, err := simulate.Replay(absCwd, *simResult, baseRef)
		if err == nil {
			result["simulation_replay"] = map[string]interface{}{
				"predicted_files": len(replay.PredictedFiles),
				"recall":          replay.Recall,
				"precision":       replay.Precision,
				"f1":              replay.F1,
				"brier_score":     replay.BrierScore,
				"true_positives":  replay.TruePositiveFiles,
				"false_negatives": replay.FalseNegativeFiles,
				"summary":         replay.Summary,
			}

			// Save calibration entry (closes the prediction loop)
			checkID := history.LoadLastCheckID(absCwd)
			_ = history.AppendCalibration(absCwd, history.CalibrationEntry{
				CheckID:        checkID,
				Timestamp:      time.Now().UTC().Format(time.RFC3339),
				PredictedFiles: replay.PredictedFiles,
				ActualFiles:    replay.ActualFiles,
				Recall:         replay.Recall,
				Precision:      replay.Precision,
				BrierScore:     replay.BrierScore,
				BlastRadius:    simResult.Total.ProductionCallers,
				TestCoverage:   simResult.Total.TestCoverage,
				Confidence:     simResult.Total.Confidence,
			})

			// Include calibration summary if we have enough data
			cal := history.GetCalibrationSummary(absCwd)
			if cal.TotalPredictions >= 3 {
				result["calibration"] = map[string]interface{}{
					"total_predictions": cal.TotalPredictions,
					"avg_recall":        cal.AvgRecall,
					"avg_brier":         cal.AvgBrier,
					"hit_rate":          cal.HitRate,
					"high_conf_recall":  cal.HighConfRecall,
				}
			}
		}
	}

	j, _ := json.MarshalIndent(result, "", "  ")
	return mcp.NewToolResultText(string(j)), nil
}
