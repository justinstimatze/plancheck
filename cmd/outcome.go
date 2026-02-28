package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/justinstimatze/plancheck/internal/history"
)

type OutcomeCmd struct {
	ID     string `arg:"" help:"History ID from a previous check."`
	Result string `arg:"" help:"Outcome: clean, rework, or failed."`
	Cwd    string `help:"Project root (must match original check)." default:"."`
}

func (c *OutcomeCmd) Run() error {
	switch c.Result {
	case "clean", "rework", "failed":
	default:
		fmt.Fprintln(os.Stderr, "plancheck: outcome must be clean, rework, or failed")
		os.Exit(2)
	}
	absCwd, _ := filepath.Abs(c.Cwd)
	if err := history.RecordOutcome(absCwd, c.ID, c.Result); err != nil {
		return err
	}
	fmt.Printf("Recorded: %s → %s\n", c.ID, c.Result)
	return nil
}
