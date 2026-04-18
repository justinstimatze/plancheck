package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/justinstimatze/plancheck/internal/history"
	"github.com/justinstimatze/plancheck/internal/plan"
	"github.com/justinstimatze/plancheck/internal/types"
)

type CheckCmd struct {
	Plan string `arg:"" help:"Path to plan JSON file."`
	Cwd  string `help:"Project root to analyze." default:"."`
	JSON bool   `help:"Output raw JSON." name:"json"`
}

func (c *CheckCmd) Run() error {
	data, err := os.ReadFile(c.Plan)
	if err != nil {
		fmt.Fprintf(os.Stderr, "plancheck: cannot read plan file: %s\n", c.Plan)
		os.Exit(2)
	}

	p, err := types.ParsePlan(data)
	if err != nil {
		fmt.Fprintf(os.Stderr, "plancheck: invalid plan schema: %v\n", err)
		os.Exit(2)
	}

	absCwd, _ := filepath.Abs(c.Cwd)
	if !c.JSON {
		warnIfStale(os.Stderr, absCwd)
	}
	result := plan.Check(plan.CheckOptions{Plan: p, Cwd: absCwd})
	history.SaveCheckResult(absCwd, result)

	if c.JSON {
		out, _ := json.MarshalIndent(result, "", "  ")
		fmt.Println(string(out))
		return nil
	}

	totalFindings := len(result.MissingFiles) + len(result.ComodGaps)

	if totalFindings == 0 {
		fmt.Println("\n\x1b[32m✓ plancheck: no findings\x1b[0m")
	} else {
		fmt.Printf("\n\x1b[33m⚠ plancheck: %d finding(s)\x1b[0m\n", totalFindings)
	}
	fmt.Println()

	if len(result.Critique) == 0 {
		fmt.Println("  No integration gaps detected.")
	} else {
		for _, line := range result.Critique {
			fmt.Printf("  • %s\n", line)
		}
	}

	if len(result.ComodGaps) > 0 {
		fmt.Println("\nCo-modification gaps:")
		for _, gap := range result.ComodGaps {
			fmt.Printf("  ~ %s (%d%% co-change with %s)\n",
				gap.ComodFile, int(gap.Frequency*100+0.5), gap.PlanFile)
		}
	}

	if len(result.Signals) > 0 {
		fmt.Println("\nSignals (informational):")
		for _, sig := range result.Signals {
			if sig.File != "" {
				fmt.Printf("  [%s] %s — %s\n", sig.Probe, sig.File, sig.Message)
			} else {
				fmt.Printf("  [%s] %s\n", sig.Probe, sig.Message)
			}
		}
	}

	if len(result.ProjectPatterns) > 0 {
		fmt.Println("\nProject patterns (from history):")
		for _, p := range result.ProjectPatterns {
			fmt.Printf("  ↺ %s\n", p.Description)
			fmt.Printf("    → %s\n", p.Suggestion)
		}
	}

	fmt.Printf("  id: %s\n\n", result.HistoryID)
	return nil
}
