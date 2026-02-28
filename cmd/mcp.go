package cmd

import (
	"fmt"
	"os"
	"regexp"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// validGitRef matches safe git ref patterns (branches, tags, HEAD~N, SHA prefixes).
var validGitRef = regexp.MustCompile(`^[a-zA-Z0-9/_.\-~^]+$`)

type MCPCmd struct{}

func (c *MCPCmd) Run() error {
	s := server.NewMCPServer("plancheck", AppVersion)

	s.AddTool(mcp.NewTool("check_plan",
		mcp.WithDescription("Run deterministic plan checks against a project. Returns compact JSON with finding counts and historyId. Use get_check_details to drill into specific categories.\n\nPlan schema: objective, filesToRead, filesToModify, filesToCreate, steps.\n\nProbe triggers: missingFiles (file existence on disk), comodGaps (git history required)."),
		mcp.WithString("plan_json", mcp.Required(), mcp.Description("ExecutionPlan as JSON string with fields: objective, filesToRead, filesToModify, filesToCreate, steps")),
		mcp.WithString("cwd", mcp.Required(), mcp.Description("Absolute path to project root directory")),
	), handleCheckPlan)

	s.AddTool(mcp.NewTool("record_outcome",
		mcp.WithDescription("Record the outcome of a previously checked plan. Returns confirmation text."),
		mcp.WithString("id", mcp.Required(), mcp.Description("History ID from check_plan result")),
		mcp.WithString("outcome", mcp.Required(), mcp.Description("Execution outcome"), mcp.Enum("clean", "rework", "failed")),
		mcp.WithString("cwd", mcp.Required(), mcp.Description("Absolute path to project root directory")),
	), handleRecordOutcome)

	s.AddTool(mcp.NewTool("record_reflection",
		mcp.WithDescription("Record a post-execution reflection with findings calibration data. Returns confirmation with reflection ID. Requires at least 2 persona passes — will reject if passes < 2."),
		mcp.WithString("cwd", mcp.Required(), mcp.Description("Absolute path to project root directory")),
		mcp.WithNumber("passes", mcp.Required(), mcp.Description("Number of persona passes completed (minimum 2 — a single clean pass doesn't prove convergence)")),
		mcp.WithNumber("probe_findings", mcp.Required(), mcp.Description("Number of findings from deterministic probes (check_plan) that changed the plan")),
		mcp.WithNumber("persona_findings", mcp.Required(), mcp.Description("Number of findings from persona passes (Implementer/Day-After/Skeptic) that changed the plan")),
		mcp.WithString("missed", mcp.Required(), mcp.Description("What went wrong that no pass caught (empty string if nothing)")),
		mcp.WithString("outcome", mcp.Required(), mcp.Description("Execution outcome"), mcp.Enum("clean", "rework", "failed")),
		mcp.WithString("id", mcp.Description("History ID from check_plan (omit if check_plan was not called)")),
	), handleRecordReflection)

	s.AddTool(mcp.NewTool("validate_execution",
		mcp.WithDescription("Compare a plan's filesToModify/filesToCreate against actual git changes since a base commit. Returns discrepancies: unplanned files modified, planned files not touched."),
		mcp.WithString("cwd", mcp.Required(), mcp.Description("Absolute path to project root directory")),
		mcp.WithString("plan_json", mcp.Required(), mcp.Description("ExecutionPlan as JSON string")),
		mcp.WithString("base_ref", mcp.Description("Git ref to diff against (default: HEAD~1)")),
	), handleValidateExecution)

	s.AddTool(mcp.NewTool("get_last_check_id",
		mcp.WithDescription("Get the most recent check_plan history ID for a project. Returns the ID string (empty if no history)."),
		mcp.WithString("cwd", mcp.Required(), mcp.Description("Absolute path to project root directory")),
	), handleGetLastCheckID)

	s.AddTool(mcp.NewTool("get_check_details",
		mcp.WithDescription("Drill into a specific finding category from the last check_plan result. Returns findings sorted by relevance, capped at limit (default 10). Response includes total count so you know if more exist."),
		mcp.WithString("cwd", mcp.Required(), mcp.Description("Absolute path to project root directory")),
		mcp.WithString("category", mcp.Required(), mcp.Description("Finding category to retrieve"),
			mcp.Enum("missingFiles", "comodGaps", "signals", "patterns", "critique", "all")),
		mcp.WithNumber("limit", mcp.Description("Max items to return per category (default 10, 0 = unlimited)")),
	), handleGetCheckDetails)

	s.AddTool(mcp.NewTool("clear_history",
		mcp.WithDescription("Clear the project's check history, last-check-id, and cached check result. Preserves knowledge.md. Use this to reset after test runs or when history is polluted."),
		mcp.WithString("cwd", mcp.Required(), mcp.Description("Absolute path to project root directory")),
	), handleClearHistory)

	s.AddTool(mcp.NewTool("forecast_plan",
		mcp.WithDescription("Monte Carlo forecast of plan outcomes based on historical data."),
		mcp.WithString("plan_json", mcp.Required(), mcp.Description("ExecutionPlan as JSON string")),
		mcp.WithString("cwd", mcp.Required(), mcp.Description("Absolute path to project root")),
	), handleForecastPlan)

	s.AddTool(mcp.NewTool("cascade_risk",
		mcp.WithDescription("Simulate cascading consequences of code changes. Shows the full chain of required changes: if you change X, what breaks, and what breaks because of that, until ripples converge. Use for risk assessment on high-blast-radius changes."),
		mcp.WithString("mutations_json", mcp.Required(), mcp.Description(`JSON array of mutations. Each: {"type":"signature-change|behavior-change|removal","name":"FuncName","receiver":"*TypeName"}`)),
		mcp.WithString("cwd", mcp.Required(), mcp.Description("Absolute path to project root directory (must have .defn/)")),
		mcp.WithNumber("max_depth", mcp.Description("Maximum cascade depth (default 5)")),
	), handleCascadeRisk)

	s.AddTool(mcp.NewTool("suggest",
		mcp.WithDescription("Live navigation: given files you've already modified, suggests what else needs to change. Uses compiler analysis (go build), reference graph (defn), and git co-modification patterns. No LLM calls — instant, deterministic. Call this mid-implementation when you want to check if you're missing files."),
		mcp.WithString("files_touched", mcp.Required(), mcp.Description("JSON array of relative file paths you've modified, e.g. [\"pkg/cmd/pr/create/create.go\"]")),
		mcp.WithString("cwd", mcp.Required(), mcp.Description("Absolute path to project root directory")),
		mcp.WithString("objective", mcp.Description("One-line description of what you're implementing (improves co-modification matching)")),
	), handleSuggest)

	s.AddTool(mcp.NewTool("simulate_plan",
		mcp.WithDescription("Simulate plan mutations against a defn reference graph. Returns per-mutation ripple report: production callers, test coverage, transitive blast radius. Requires .defn/ in project (run `defn init .` first)."),
		mcp.WithString("mutations_json", mcp.Required(), mcp.Description(`JSON array of mutations. Each mutation: {"type":"signature-change|behavior-change|removal|addition","name":"FuncName","receiver":"*TypeName"}. Receiver is optional.`)),
		mcp.WithString("cwd", mcp.Required(), mcp.Description("Absolute path to project root directory (must have .defn/)")),
	), handleSimulatePlan)

	fmt.Fprintf(os.Stderr, "plancheck %s: MCP server starting\n", AppVersion)
	if err := server.ServeStdio(s); err != nil {
		fmt.Fprintf(os.Stderr, "plancheck MCP error: %v\n", err)
		return err
	}
	return nil
}

// truncate returns the first limit items from a slice. 0 means unlimited.
func truncate[T any](items []T, limit int) []T {
	if limit <= 0 || len(items) <= limit {
		return items
	}
	return items[:limit]
}
