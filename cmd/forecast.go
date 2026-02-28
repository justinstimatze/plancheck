package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/justinstimatze/plancheck/internal/forecast"
	"github.com/justinstimatze/plancheck/internal/types"
)

// ForecastCmd shows the MC outcome forecast for a plan.
type ForecastCmd struct {
	PlanFile string `arg:"" help:"Path to plan JSON file, or - for stdin"`
	Cwd      string `help:"Project directory (default: cwd)" short:"C"`
}

func (c *ForecastCmd) Run() error {
	cwd := c.Cwd
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return err
		}
	}

	// Read plan
	var data []byte
	var err error
	if c.PlanFile == "-" || c.PlanFile == "/dev/stdin" {
		data, err = os.ReadFile("/dev/stdin")
	} else {
		data, err = os.ReadFile(c.PlanFile)
	}
	if err != nil {
		return err
	}

	p, err := types.ParsePlan(data)
	if err != nil {
		return err
	}

	// Build history and run forecast
	history := forecast.BuildHistory(cwd)
	if len(history) < 3 {
		fmt.Println("Insufficient historical data for forecasting.")
		fmt.Println("Run more check_plan cycles or ensure seed data exists at ~/.plancheck/forecast_history.json")
		return nil
	}

	complexity := len(p.Steps)
	if len(p.FilesToModify)+len(p.FilesToCreate) > complexity {
		complexity = len(p.FilesToModify) + len(p.FilesToCreate)
	}

	maturity := forecast.Assess(cwd)

	fc := forecast.Run(forecast.PlanProperties{
		Complexity:  complexity,
		TestDensity: maturity.Score, // use maturity as test density proxy
		BlastRadius: complexity,     // rough proxy
		FileCount:   len(p.FilesToModify),
	}, history, 10000)

	// Pretty output
	fmt.Println("plancheck forecast")
	fmt.Println()
	fmt.Printf("  Plan: %s\n", p.Objective)
	fmt.Printf("  Complexity: %d (steps + files)\n", complexity)
	fmt.Printf("  Project maturity: %.2f (%s)\n", maturity.Score, maturity.Label)
	fmt.Println()

	fmt.Println("  Outcome probability:")
	fmt.Printf("    Clean execution:    %3.0f%%\n", fc.PClean*100)
	fmt.Printf("    Minor rework:       %3.0f%%\n", fc.PRework*100)
	fmt.Printf("    Significant rework: %3.0f%%\n", fc.PFailed*100)
	fmt.Println()

	fmt.Println("  Prediction accuracy (recall):")
	fmt.Printf("    P50: %.0f%%  (median — half the time you'll do at least this well)\n", fc.RecallP50*100)
	fmt.Printf("    P85: %.0f%%  (optimistic — 85%% of the time)\n", fc.RecallP85*100)
	fmt.Println()

	fmt.Printf("  Based on: %d similar historical plans\n", fc.MatchingHistorical)
	fmt.Println()

	fmt.Println("  Signal reliability for this project:")
	fmt.Printf("    Structural (refgraph): %s\n", maturity.Signals.Structural)
	fmt.Printf("    Statistical (comod):   %s\n", maturity.Signals.Comod)
	fmt.Printf("    Semantic (LLM):        %s\n", maturity.Signals.Semantic)
	fmt.Println()
	fmt.Printf("  Recommended verification: %s\n", maturity.Verification)

	if os.Getenv("PLANCHECK_JSON") == "1" {
		fmt.Println()
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]interface{}{
			"forecast": fc,
			"maturity": maturity,
		})
	}

	return nil
}
