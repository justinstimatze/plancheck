package cmd

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/justinstimatze/plancheck/internal/history"
	"github.com/mark3labs/mcp-go/mcp"
)

func handleRecordOutcome(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	id, ok := args["id"].(string)
	if !ok || id == "" {
		return mcp.NewToolResultError("id: required string argument"), nil
	}
	outcome, ok := args["outcome"].(string)
	if !ok || outcome == "" {
		return mcp.NewToolResultError("outcome: required string argument (clean, rework, or failed)"), nil
	}
	cwd, ok := args["cwd"].(string)
	if !ok || cwd == "" {
		return mcp.NewToolResultError("cwd: required string argument"), nil
	}
	absCwd, _ := filepath.Abs(cwd)
	if err := history.RecordOutcome(absCwd, id, outcome); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(fmt.Sprintf("Recorded: %s → %s", id, outcome)), nil
}

func handleRecordReflection(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	cwd, ok := args["cwd"].(string)
	if !ok || cwd == "" {
		return mcp.NewToolResultError("cwd: required string argument"), nil
	}
	passesRaw, ok := args["passes"].(float64)
	if !ok {
		return mcp.NewToolResultError("passes: required number argument"), nil
	}
	probeRaw, ok := args["probe_findings"].(float64)
	if !ok {
		return mcp.NewToolResultError("probe_findings: required number argument"), nil
	}
	personaRaw, ok := args["persona_findings"].(float64)
	if !ok {
		return mcp.NewToolResultError("persona_findings: required number argument"), nil
	}
	missed, ok := args["missed"].(string)
	if !ok {
		return mcp.NewToolResultError("missed: required string argument"), nil
	}
	outcome, ok := args["outcome"].(string)
	if !ok || outcome == "" {
		return mcp.NewToolResultError("outcome: required string argument (clean, rework, or failed)"), nil
	}
	id, _ := args["id"].(string)

	absCwd, _ := filepath.Abs(cwd)
	resultID, err := history.RecordReflection(absCwd, history.ReflectionOpts{
		ID:              id,
		Passes:          int(passesRaw),
		ProbeFindings:   int(probeRaw),
		PersonaFindings: int(personaRaw),
		Missed:          missed,
		Outcome:         outcome,
	})
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(fmt.Sprintf("Reflection recorded: %s -> %s", resultID, outcome)), nil
}

func handleClearHistory(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	cwd, ok := args["cwd"].(string)
	if !ok || cwd == "" {
		return mcp.NewToolResultError("cwd: required string argument"), nil
	}
	absCwd, _ := filepath.Abs(cwd)
	if err := history.ClearHistory(absCwd); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText("History cleared for " + absCwd), nil
}

func handleGetLastCheckID(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	cwd, ok := args["cwd"].(string)
	if !ok || cwd == "" {
		return mcp.NewToolResultError("cwd: required string argument"), nil
	}
	id := history.LoadLastCheckID(cwd)
	return mcp.NewToolResultText(id), nil
}
