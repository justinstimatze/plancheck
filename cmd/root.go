// Package cmd implements the plancheck CLI commands.
package cmd

import "github.com/alecthomas/kong"

// AppVersion is set by main from ldflags-injected version.
var AppVersion = "dev"

type CLI struct {
	Check      CheckCmd         `cmd:"" default:"withargs" help:"Check a plan JSON file for integration gaps."`
	Outcome    OutcomeCmd       `cmd:"" help:"Record the outcome of a previously checked plan."`
	Reflection ReflectionCmd    `cmd:"" help:"Record a post-execution reflection."`
	History    HistoryCmd       `cmd:"" help:"Show recent plan check history for a project."`
	MCP        MCPCmd           `cmd:"" help:"Start the MCP stdio server."`
	Setup      SetupCmd         `cmd:"" help:"Configure Claude Code: MCP server, hooks, and skill file."`
	Doctor     DoctorCmd        `cmd:"" help:"Check that plancheck is correctly configured."`
	Gate       GateCmd          `cmd:"" help:"ExitPlanMode hook: enforce check_plan + iteration before plan exit."`
	Stats      StatsCmd         `cmd:"" help:"Show aggregate stats across all projects."`
	Review     ReviewCmd        `cmd:"" help:"Review git changes and suggest missing files (zero LLM cost)."`
	Simulate   SimulateCmd      `cmd:"" help:"Simulate plan mutations against a defn reference graph."`
	Forecast   ForecastCmd      `cmd:"" help:"Show MC outcome forecast for a plan."`
	Disable    DisableCmd       `cmd:"" help:"Disable plancheck gate (persists across sessions)."`
	Enable     EnableCmd        `cmd:"" help:"Re-enable plancheck gate."`
	Version    kong.VersionFlag `name:"version" help:"Print version."`
}
