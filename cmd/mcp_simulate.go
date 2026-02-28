package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/justinstimatze/plancheck/internal/forecast"
	"github.com/justinstimatze/plancheck/internal/refgraph"
	"github.com/justinstimatze/plancheck/internal/simulate"
	"github.com/justinstimatze/plancheck/internal/types"
	"github.com/mark3labs/mcp-go/mcp"
)

func handleForecastPlan(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	planJSON, ok := args["plan_json"].(string)
	if !ok || planJSON == "" {
		return mcp.NewToolResultError("plan_json required"), nil
	}
	cwdArg, ok := args["cwd"].(string)
	if !ok || cwdArg == "" {
		return mcp.NewToolResultError("cwd required"), nil
	}
	absCwd, _ := filepath.Abs(cwdArg)
	p, err := types.ParsePlan([]byte(planJSON))
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("Invalid plan: %v", err)), nil
	}
	fcHistory := forecast.BuildHistory(absCwd)
	if len(fcHistory) < 3 {
		return mcp.NewToolResultText(`{"error":"insufficient data"}`), nil
	}
	complexity := len(p.Steps)
	if len(p.FilesToModify)+len(p.FilesToCreate) > complexity {
		complexity = len(p.FilesToModify) + len(p.FilesToCreate)
	}
	maturity := forecast.Assess(absCwd)
	fc := forecast.Run(forecast.PlanProperties{
		Complexity: complexity, TestDensity: maturity.Score,
		BlastRadius: complexity, FileCount: len(p.FilesToModify),
	}, fcHistory, 10000)
	result := map[string]interface{}{
		"pClean": fc.PClean, "pRework": fc.PRework, "pFailed": fc.PFailed,
		"recallP50": fc.RecallP50, "recallP85": fc.RecallP85,
		"basedOn": fc.MatchingHistorical, "maturity": maturity.Label,
		"summary": fc.Summary,
	}
	out, _ := json.Marshal(result)
	return mcp.NewToolResultText(string(out)), nil
}

func handleCascadeRisk(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	mutJSON, ok := args["mutations_json"].(string)
	if !ok || mutJSON == "" {
		return mcp.NewToolResultError("mutations_json required"), nil
	}
	cwd, ok := args["cwd"].(string)
	if !ok || cwd == "" {
		return mcp.NewToolResultError("cwd required"), nil
	}
	absCwd, _ := filepath.Abs(cwd)

	maxDepth := 5
	if d, ok := args["max_depth"].(float64); ok && d > 0 {
		maxDepth = int(d)
	}

	var mutations []simulate.Mutation
	if err := json.Unmarshal([]byte(mutJSON), &mutations); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("invalid mutations_json: %v", err)), nil
	}

	g := refgraph.LoadGraph(absCwd)
	if g == nil {
		return mcp.NewToolResultError("no defn reference graph found — run 'defn init .' first"), nil
	}
	result := simulate.Cascade(g, mutations, maxDepth)
	out, _ := json.Marshal(result)
	return mcp.NewToolResultText(string(out)), nil
}

func handleSimulatePlan(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	mutJSON, ok := args["mutations_json"].(string)
	if !ok || mutJSON == "" {
		return mcp.NewToolResultError("mutations_json: required string argument"), nil
	}
	cwd, ok := args["cwd"].(string)
	if !ok || cwd == "" {
		return mcp.NewToolResultError("cwd: required string argument"), nil
	}
	absCwd, _ := filepath.Abs(cwd)

	var mutations []simulate.Mutation
	if err := json.Unmarshal([]byte(mutJSON), &mutations); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("invalid mutations_json: %v", err)), nil
	}
	if len(mutations) == 0 {
		return mcp.NewToolResultError("mutations_json: at least one mutation required"), nil
	}

	g := refgraph.LoadGraph(absCwd)
	if g == nil {
		return mcp.NewToolResultError("no defn reference graph found — run 'defn init .' first"), nil
	}
	result, err := simulate.Run(g, mutations)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	out, _ := json.Marshal(result)
	return mcp.NewToolResultText(string(out)), nil
}
