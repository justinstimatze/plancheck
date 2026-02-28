package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/justinstimatze/plancheck/internal/history"
	"github.com/justinstimatze/plancheck/internal/plan"
	"github.com/justinstimatze/plancheck/internal/simulate"
	"github.com/justinstimatze/plancheck/internal/types"
	"github.com/mark3labs/mcp-go/mcp"
)

func handleCheckPlan(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	planJSON, ok := args["plan_json"].(string)
	if !ok || planJSON == "" {
		return mcp.NewToolResultError("plan_json: required string argument"), nil
	}
	cwd, ok := args["cwd"].(string)
	if !ok || cwd == "" {
		return mcp.NewToolResultError("cwd: required string argument"), nil
	}

	absCwd, _ := filepath.Abs(cwd)
	info, err := os.Stat(absCwd)
	if err != nil || !info.IsDir() {
		return mcp.NewToolResultError(fmt.Sprintf("cwd: %q is not a valid directory", cwd)), nil
	}

	p, err := types.ParsePlan([]byte(planJSON))
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("Invalid plan: %v", err)), nil
	}

	result := plan.Check(plan.CheckOptions{Plan: p, Cwd: absCwd})

	history.SaveCheckResult(absCwd, result)
	compact := result.ToCompact()
	compact.ServerVersion = AppVersion

	// Lean response: Minto pyramid + every actionable signal.
	// Every token should help improve the plan.
	lean := map[string]interface{}{
		"historyId": compact.HistoryID,
		"summary":   compact.Summary,
	}

	// Ranked suggestions with reasons
	if len(compact.SuggestedAdditions.Ranked) > 0 {
		lean["ranked"] = compact.SuggestedAdditions.Ranked
	}

	// Semantic validation: confirm/deny the model's own suggestions
	var validated []map[string]string
	for _, sig := range result.Signals {
		if sig.Probe == "semantic-validation" {
			validated = append(validated, map[string]string{
				"file":    sig.File,
				"verdict": sig.Message,
			})
		}
	}
	if len(validated) > 0 {
		lean["semanticValidation"] = validated
	}

	// Invariant results
	var invariants []map[string]string
	for _, sig := range result.Signals {
		if sig.Probe == "invariant" || sig.Probe == "invariant-risk" {
			invariants = append(invariants, map[string]string{
				"status":  sig.Probe,
				"details": sig.Message,
			})
		}
	}
	if len(invariants) > 0 {
		lean["invariants"] = invariants
	}

	// Keyword-directory gaps: task mentions a domain not covered by plan.
	// These are critical errors — the plan is structurally incomplete.
	// Surfaced first in the response so the model sees them immediately.
	var domainGaps []string
	for _, sig := range result.Signals {
		if sig.Probe == "keyword-dir" {
			domainGaps = append(domainGaps, sig.Message)
		}
	}
	if len(domainGaps) > 0 {
		lean["domainGaps"] = domainGaps
	}

	// Analogy findings (only if novel work)
	if compact.Novelty != nil && compact.Novelty.Score > 0.3 {
		var analogies []string
		for _, sig := range result.Signals {
			if sig.Probe == "analogy" {
				analogies = append(analogies, sig.Message)
			}
		}
		if len(analogies) > 0 {
			lean["analogies"] = analogies
		}

		// Backward scout prerequisites
		for _, sig := range result.Signals {
			if sig.Probe == "backward-scout" {
				lean["prerequisites"] = sig.Message
				break
			}
		}

		// Novelty guidance (only for novel/exploratory)
		lean["noveltyGuidance"] = compact.Novelty.Guidance
	}

	// Blast radius (always useful)
	if compact.Simulation != nil && compact.Simulation.ProductionCallers > 0 {
		lean["blastRadius"] = map[string]interface{}{
			"callers": compact.Simulation.ProductionCallers,
			"tests":   compact.Simulation.TestCoverage,
		}
	}

	// Risk warning (only when significant)
	if compact.Forecast != nil && compact.Forecast.PFailed > 0.3 {
		lean["riskWarning"] = fmt.Sprintf("%.0f%% risk of rework (based on %d similar plans)",
			compact.Forecast.PFailed*100, compact.Forecast.BasedOn)
	}

	// LLM judge: synthesize raw signals into contextual recommendation
	if simulate.LLMAvailable() {
		var judgeSigs []simulate.JudgeSignal
		for _, sig := range result.Signals {
			judgeSigs = append(judgeSigs, simulate.JudgeSignal{
				Type:    sig.Probe,
				File:    sig.File,
				Message: sig.Message,
			})
		}
		for _, gap := range result.ComodGaps {
			if !gap.Acknowledged && !gap.Hub {
				judgeSigs = append(judgeSigs, simulate.JudgeSignal{
					Type:    "comod",
					File:    gap.ComodFile,
					Message: gap.Suggestion,
				})
			}
		}

		noveltyLabel := ""
		if compact.Novelty != nil {
			noveltyLabel = compact.Novelty.Label + " (" + compact.Novelty.Uncertainty + ")"
		}
		forecastStr := ""
		if compact.Forecast != nil {
			forecastStr = compact.Forecast.Summary
		}

		judgeRec, err := simulate.Judge(simulate.JudgeInput{
			Objective: p.Objective,
			PlanFiles: p.FilesToModify,
			NewFiles:  p.FilesToCreate,
			Signals:   judgeSigs,
			Novelty:   noveltyLabel,
			Forecast:  forecastStr,
			DirTree:   plan.CompactDirTree(absCwd, 30),
		})
		if err == nil && judgeRec != nil {
			lean["judge"] = judgeRec
		}
	}

	// Build checklist: ready-to-use text block for the consuming model.
	// Mirrors the benchmark prompt format that yielded +14.3pp lift.
	// Order: domain gaps (critical errors) → keyword-dir files → other ranked files.
	if checklist := buildChecklist(result, compact); checklist != "" {
		lean["checklist"] = checklist
	}

	// Write debug log for inspection
	debugPath := filepath.Join(absCwd, ".plancheck-debug.json")
	if debugData, err := json.MarshalIndent(compact, "", "  "); err == nil {
		_ = os.WriteFile(debugPath, debugData, 0o600)
	}

	out, _ := json.Marshal(lean)
	return mcp.NewToolResultText(string(out)), nil
}

// buildChecklist creates a ready-to-use text block from check_plan results.
// Format matches the benchmark prompt that yielded +14.3pp lift:
//   - Domain gaps as critical errors at top
//   - Keyword-dir files listed first (uncovered domains)
//   - Remaining ranked files after
//   - "INCLUDE X — reason" default-inclusion framing
//   - Checklist at END of output (Lost in the Middle mitigation)
func buildChecklist(result types.PlanCheckResult, compact types.CompactCheckResult) string {
	var parts []string

	// 1. Domain gaps: critical errors — the plan is structurally incomplete
	var gaps []string
	for _, sig := range result.Signals {
		if sig.Probe == "keyword-dir" {
			gaps = append(gaps, sig.Message)
		}
	}
	if len(gaps) > 0 {
		lines := []string{"INCOMPLETE PLAN — your plan is missing entire domains the task requires:"}
		for _, g := range gaps {
			lines = append(lines, "  - "+g)
		}
		lines = append(lines, "You MUST add files from each missing domain. This is not optional.")
		parts = append(parts, strings.Join(lines, "\n"))
	}

	// 2. Ranked file suggestions: keyword-dir first, then others
	ranked := compact.SuggestedAdditions.Ranked
	if len(ranked) == 0 {
		if len(parts) == 0 {
			return ""
		}
		return strings.Join(parts, "\n\n")
	}

	type entry struct {
		path   string
		reason string
	}
	var entries []entry
	seen := make(map[string]bool)

	// Keyword-dir files first (domain gaps are critical)
	for _, r := range ranked {
		if strings.Contains(r.Source, "keyword-dir") {
			path := r.Path
			if path == "" {
				path = r.File
			}
			if !seen[path] {
				seen[path] = true
				entries = append(entries, entry{path, r.Reason})
			}
		}
	}
	// Then remaining ranked files (up to 8 total)
	for _, r := range ranked {
		if len(entries) >= 8 {
			break
		}
		if !strings.Contains(r.Source, "keyword-dir") {
			path := r.Path
			if path == "" {
				path = r.File
			}
			if !seen[path] {
				seen[path] = true
				entries = append(entries, entry{path, r.Reason})
			}
		}
	}

	if len(entries) > 0 {
		lines := []string{"The following files should be INCLUDED in your plan unless you have a specific reason to exclude them:"}
		for _, e := range entries {
			if e.reason != "" {
				lines = append(lines, fmt.Sprintf("  INCLUDE %s — %s", e.path, e.reason))
			} else {
				lines = append(lines, fmt.Sprintf("  INCLUDE %s", e.path))
			}
		}
		parts = append(parts, strings.Join(lines, "\n"))
	}

	return strings.Join(parts, "\n\n")
}

func handleGetCheckDetails(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	cwd, ok := args["cwd"].(string)
	if !ok || cwd == "" {
		return mcp.NewToolResultError("cwd: required string argument"), nil
	}
	category, ok := args["category"].(string)
	if !ok || category == "" {
		return mcp.NewToolResultError("category: required string argument"), nil
	}
	limit := 10
	if raw, ok := args["limit"].(float64); ok {
		limit = int(raw)
	}

	absCwd, _ := filepath.Abs(cwd)
	cached, err := history.LoadCheckResult(absCwd)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	cached.EnsureNonNil()

	// Sort comodGaps by frequency descending (most coupled first)
	sort.Slice(cached.ComodGaps, func(i, j int) bool {
		return cached.ComodGaps[i].Frequency > cached.ComodGaps[j].Frequency
	})

	if category == "all" {
		result := map[string]interface{}{
			"missingFiles": cached.MissingFiles,
			"comodGaps":    cached.ComodGaps,
			"signals":      cached.Signals,
			"patterns":     cached.ProjectPatterns,
			"critique":     cached.Critique,
		}
		out, _ := json.Marshal(result)
		return mcp.NewToolResultText(string(out)), nil
	}

	var items interface{}
	var total int
	switch category {
	case "missingFiles":
		total = len(cached.MissingFiles)
		items = truncate(cached.MissingFiles, limit)
	case "comodGaps":
		total = len(cached.ComodGaps)
		items = truncate(cached.ComodGaps, limit)
	case "signals":
		total = len(cached.Signals)
		items = truncate(cached.Signals, limit)
	case "patterns":
		total = len(cached.ProjectPatterns)
		items = truncate(cached.ProjectPatterns, limit)
	case "critique":
		total = len(cached.Critique)
		items = truncate(cached.Critique, limit)
	default:
		return mcp.NewToolResultError(fmt.Sprintf("unknown category: %s", category)), nil
	}

	showing := total
	if limit > 0 && total > limit {
		showing = limit
	}

	result := map[string]interface{}{
		"items":   items,
		"total":   total,
		"showing": showing,
	}
	out, _ := json.Marshal(result)
	return mcp.NewToolResultText(string(out)), nil
}
