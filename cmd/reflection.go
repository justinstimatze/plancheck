package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/justinstimatze/plancheck/internal/history"
)

type ReflectionCmd struct {
	Cwd             string `help:"Project root." default:"."`
	ID              string `help:"History ID from check_plan (if it ran)."`
	Passes          int    `help:"Number of persona passes completed (minimum 2)." required:""`
	ProbeFindings   int    `help:"Findings from deterministic probes that changed the plan." default:"0"`
	PersonaFindings int    `help:"Findings from persona passes that changed the plan." default:"0"`
	Missed          string `help:"What went wrong that no pass caught." default:""`
	Outcome         string `help:"Outcome: clean, rework, or failed." required:""`
	SignalsUseful   string `help:"Comma-separated probe names that were useful." default:""`
}

func (c *ReflectionCmd) Run() error {
	switch c.Outcome {
	case "clean", "rework", "failed":
	default:
		fmt.Fprintln(os.Stderr, "plancheck: --outcome must be clean, rework, or failed")
		os.Exit(2)
	}

	var signalsUseful []string
	if c.SignalsUseful != "" {
		for _, s := range strings.Split(c.SignalsUseful, ",") {
			s = strings.TrimSpace(s)
			if s != "" {
				signalsUseful = append(signalsUseful, s)
			}
		}
	}

	absCwd, _ := filepath.Abs(c.Cwd)
	id, err := history.RecordReflection(absCwd, history.ReflectionOpts{
		ID:              c.ID,
		Passes:          c.Passes,
		ProbeFindings:   c.ProbeFindings,
		PersonaFindings: c.PersonaFindings,
		Missed:          c.Missed,
		Outcome:         c.Outcome,
		SignalsUseful:   signalsUseful,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "plancheck: %v\n", err)
		os.Exit(2)
	}
	fmt.Printf("Reflection recorded: %s -> %s (probe: %d, persona: %d, %d passes)\n", id, c.Outcome, c.ProbeFindings, c.PersonaFindings, c.Passes)
	return nil
}
