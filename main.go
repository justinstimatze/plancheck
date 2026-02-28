// plancheck is a pre-flight plan checker for AI coding agents.
// It catches integration gaps before code is written by running deterministic
// probes against execution plans and scoring them for structural issues.
package main

import (
	"github.com/alecthomas/kong"
	"github.com/justinstimatze/plancheck/cmd"
)

var version = "dev"

func main() {
	cmd.AppVersion = version
	var cli cmd.CLI
	ctx := kong.Parse(&cli,
		kong.Name("plancheck"),
		kong.Description("Pre-flight plan checker for AI coding agents"),
		kong.UsageOnError(),
		kong.Vars{"version": version},
	)
	err := ctx.Run()
	ctx.FatalIfErrorf(err)
}
