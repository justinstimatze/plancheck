package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/justinstimatze/plancheck/internal/history"
)

type ReflectCmd struct {
	Result string `arg:"" help:"Outcome: clean, rework, or failed."`
	Cwd    string `help:"Project root." default:"."`
	ID     string `help:"History ID. Defaults to the project's last-check-id."`
}

func (c *ReflectCmd) Run() error {
	switch c.Result {
	case "clean", "rework", "failed":
	default:
		fmt.Fprintln(os.Stderr, "plancheck: outcome must be clean, rework, or failed")
		os.Exit(2)
	}

	absCwd, _ := filepath.Abs(c.Cwd)

	id := c.ID
	if id == "" {
		id = history.LoadLastCheckID(absCwd)
		if id == "" {
			fmt.Fprintln(os.Stderr, "plancheck: no last-check-id for this project — pass --id, or run check_plan first")
			os.Exit(2)
		}
	}

	summary, err := history.LoadHistory(absCwd)
	if err == nil {
		if prior, ok := summary.Outcomes[id]; ok {
			fmt.Fprintf(os.Stderr, "plancheck: %s already has outcome %q — not overwriting\n", id, prior)
			os.Exit(2)
		}
	}

	if err := history.RecordOutcome(absCwd, id, c.Result); err != nil {
		return err
	}
	fmt.Printf("Recorded: %s → %s\n", id, c.Result)
	return nil
}
