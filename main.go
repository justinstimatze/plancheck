// plancheck is a pre-flight plan checker for AI coding agents.
// It catches integration gaps before code is written by running deterministic
// probes against execution plans and scoring them for structural issues.
package main

import (
	"runtime/debug"

	"github.com/alecthomas/kong"
	"github.com/justinstimatze/plancheck/cmd"
)

// version is overridden at build time via
// -ldflags "-X main.version=$(git describe --tags --always --dirty)".
// The git tag is the single source of truth — there is no constant to edit.
var version = "dev"

// buildVersion resolves the version string, preferring the most specific
// source available: the ldflags-baked value, then the module version recorded
// by `go install ...@vX.Y.Z`, then the VCS revision stamped into a local
// `go build`, and finally "dev" for a build outside any git tree.
func buildVersion() string {
	if version != "dev" {
		return version
	}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return version
	}
	if v := bi.Main.Version; v != "" && v != "(devel)" {
		return v
	}
	var rev, dirty string
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			if len(s.Value) >= 12 {
				rev = s.Value[:12]
			} else {
				rev = s.Value
			}
		case "vcs.modified":
			if s.Value == "true" {
				dirty = "-dirty"
			}
		}
	}
	if rev != "" {
		return rev + dirty
	}
	return version
}

func main() {
	v := buildVersion()
	cmd.AppVersion = v
	var cli cmd.CLI
	ctx := kong.Parse(&cli,
		kong.Name("plancheck"),
		kong.Description("Pre-flight plan checker for AI coding agents"),
		kong.UsageOnError(),
		kong.Vars{"version": v},
	)
	err := ctx.Run()
	ctx.FatalIfErrorf(err)
}
