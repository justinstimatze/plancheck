package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/justinstimatze/plancheck/internal/refgraph"
	"github.com/justinstimatze/plancheck/internal/simulate"
)

// SimulateCmd runs a plan simulation against a defn reference graph.
type SimulateCmd struct {
	Cwd       string   `arg:"" optional:"" help:"Project directory (default: cwd)"`
	Mutations []string `arg:"" optional:"" help:"Mutations as type:name or type:receiver.name"`
	Backward  bool     `help:"Run backward scout instead of forward simulation" short:"b"`
	Replay    string   `help:"Compare last simulation against actual changes since this git ref" short:"r"`
	Save      bool     `help:"Save simulation result for later replay comparison" short:"s"`
	Cascade   bool     `help:"Run cascade simulation — trace ripples until they die out" short:"c"`
	Depth     int      `help:"Max cascade depth" default:"5" short:"d"`
}

func (c *SimulateCmd) Run() error {
	cwd := c.Cwd
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return err
		}
	}

	// Replay mode: compare last simulation against actual changes
	if c.Replay != "" {
		simResult, err := simulate.LoadSimulationPrediction(cwd)
		if err != nil {
			return fmt.Errorf("no saved simulation found — run simulate with --save first: %w", err)
		}

		replay, err := simulate.Replay(cwd, *simResult, c.Replay)
		if err != nil {
			return fmt.Errorf("replay failed: %w", err)
		}

		fmt.Println("=== SIMULATION REPLAY ===")
		fmt.Println()
		fmt.Printf("  Predicted:  %d files, %d production callers, %d tests\n",
			len(replay.PredictedFiles), replay.PredictedCallers, replay.PredictedTests)
		fmt.Printf("  Actual:     %d files changed, %d definitions affected\n",
			len(replay.ActualFiles), replay.ActualDefinitions)
		fmt.Println()
		fmt.Printf("  Recall:     %.0f%% (%d/%d actual files predicted)\n",
			replay.Recall*100, len(replay.TruePositiveFiles), len(replay.ActualFiles))
		fmt.Printf("  Precision:  %.0f%% (%d/%d predictions were correct)\n",
			replay.Precision*100, len(replay.TruePositiveFiles), len(replay.PredictedFiles))
		fmt.Printf("  F1:         %.2f\n", replay.F1)
		fmt.Printf("  Brier:      %.2f (lower is better)\n", replay.BrierScore)

		if len(replay.TruePositiveFiles) > 0 {
			fmt.Println("\n  Correctly predicted:")
			for _, f := range replay.TruePositiveFiles {
				fmt.Printf("    ✓ %s\n", f)
			}
		}
		if len(replay.FalseNegativeFiles) > 0 {
			fmt.Println("\n  Missed (false negatives):")
			for _, f := range replay.FalseNegativeFiles {
				fmt.Printf("    ✗ %s\n", f)
			}
		}
		if len(replay.FalsePositiveFiles) > 0 && len(replay.FalsePositiveFiles) <= 10 {
			fmt.Println("\n  Overpredicted (false positives):")
			for _, f := range replay.FalsePositiveFiles {
				fmt.Printf("    ○ %s\n", f)
			}
		} else if len(replay.FalsePositiveFiles) > 10 {
			fmt.Printf("\n  Overpredicted: %d files (expected — graph predicts broadly)\n",
				len(replay.FalsePositiveFiles))
		}

		if os.Getenv("PLANCHECK_JSON") == "1" {
			fmt.Println()
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(replay)
		}
		return nil
	}

	// Cascade mode: trace ripples until they die out
	if c.Cascade {
		if len(c.Mutations) == 0 {
			return fmt.Errorf("usage: plancheck simulate -c [cwd] <mutation>...")
		}
		var mutations []simulate.Mutation
		for _, arg := range c.Mutations {
			m, err := parseMutation(arg)
			if err != nil {
				return err
			}
			mutations = append(mutations, m)
		}

		g := refgraph.LoadGraph(cwd)
		if g == nil {
			return fmt.Errorf("no defn reference graph found — run 'defn init .' first")
		}
		result := simulate.Cascade(g, mutations, c.Depth)

		fmt.Println("=== CASCADE SIMULATION ===")
		fmt.Println()
		for _, step := range result.Steps {
			if step.NewBreakages == 0 {
				continue
			}
			fmt.Printf("  Depth %d: %d new breakages (%d cumulative)\n",
				step.Depth, step.NewBreakages, step.Cumulative)
			if len(step.AffectedFiles) > 0 && len(step.AffectedFiles) <= 10 {
				for _, f := range step.AffectedFiles {
					fmt.Printf("    → %s\n", f)
				}
			} else if len(step.AffectedFiles) > 10 {
				for _, f := range step.AffectedFiles[:5] {
					fmt.Printf("    → %s\n", f)
				}
				fmt.Printf("    ... and %d more files\n", len(step.AffectedFiles)-5)
			}
		}

		fmt.Printf("\n  Total: %d files, %d breakages, depth %d",
			result.TotalFiles, result.TotalBreaks, result.MaxDepth)
		if result.Converged {
			fmt.Print(" (converged)")
		}
		fmt.Println()

		if len(result.FileChain) > 0 {
			fmt.Println("\n  File chain (all affected files):")
			for _, f := range result.FileChain {
				fmt.Printf("    %s\n", f)
			}
		}

		if os.Getenv("PLANCHECK_JSON") == "1" {
			fmt.Println()
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(result)
		}
		return nil
	}

	// Backward scout mode
	if c.Backward {
		if len(c.Mutations) == 0 {
			return fmt.Errorf("usage: plancheck simulate -b [cwd] <name>...\n" +
				"  name format: name or receiver.name\n" +
				"  example: plancheck simulate -b . *Context.CBOR")
		}
		for _, arg := range c.Mutations {
			name, receiver := parseNameReceiver(arg)
			result, err := simulate.BackwardScout(cwd, name, receiver)
			if err != nil {
				return err
			}
			fmt.Printf("Backward scout: %s\n", result.Goal)
			if result.PatternSource != "" {
				fmt.Printf("  Pattern source: %s (highest-blast-radius sibling)\n", result.PatternSource)
			}
			fmt.Println("\n  Prerequisites:")
			for _, p := range result.Prerequisites {
				fmt.Printf("    [%s] %s\n", p.Kind, p.Description)
			}
			if len(result.TestPattern) > 0 {
				fmt.Printf("\n  Test pattern to follow: %s\n", result.TestPattern[0])
			}
			fmt.Println()
		}
		return nil
	}

	if len(c.Mutations) == 0 {
		return fmt.Errorf("usage: plancheck simulate [cwd] <mutation>...\n" +
			"  mutation format: type:name or type:receiver.name\n" +
			"  types: signature-change, behavior-change, removal, addition\n" +
			"  examples:\n" +
			"    plancheck simulate . signature-change:Render\n" +
			"    plancheck simulate . removal:*Context.JSON\n" +
			"    plancheck simulate ~/.plancheck/datasets/repos/gin behavior-change:*Context.Render")
	}

	var mutations []simulate.Mutation
	for _, arg := range c.Mutations {
		m, err := parseMutation(arg)
		if err != nil {
			return err
		}
		mutations = append(mutations, m)
	}

	g := refgraph.LoadGraph(cwd)
	if g == nil {
		return fmt.Errorf("no defn reference graph found — run 'defn init .' first")
	}
	result, err := simulate.Run(g, mutations)
	if err != nil {
		return err
	}

	// Pretty print
	for _, step := range result.Steps {
		fmt.Println(step.Impact)
		if len(step.TopCallers) > 0 {
			fmt.Println("  Callers:")
			for _, c := range step.TopCallers {
				recv := ""
				if c.Receiver != "" {
					recv = fmt.Sprintf("(%s).", c.Receiver)
				}
				fmt.Printf("    %s%s [%s]\n", recv, c.Name, c.RefKind)
			}
		}
		if step.TransitiveCallers > 0 {
			fmt.Printf("  Transitive: %d more\n", step.TransitiveCallers)
		}
		fmt.Println()
	}

	// Save simulation for later replay
	if c.Save {
		if err := simulate.SaveSimulationPrediction(cwd, result); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not save simulation: %v\n", err)
		} else {
			fmt.Println("Simulation saved. After making changes, run:")
			fmt.Printf("  plancheck simulate %s --replay HEAD~1\n", cwd)
		}
	}

	// JSON output for programmatic use
	if os.Getenv("PLANCHECK_JSON") == "1" {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(result)
	}

	return nil
}

func parseNameReceiver(s string) (name, receiver string) {
	// Parse *Context.CBOR → name=CBOR, receiver=*Context
	if idx := strings.LastIndex(s, "."); idx > 0 {
		candidate := s[:idx]
		if strings.HasPrefix(candidate, "*") || strings.HasPrefix(candidate, "(") {
			return s[idx+1:], strings.Trim(candidate, "()")
		}
	}
	return s, ""
}

func parseMutation(s string) (simulate.Mutation, error) {
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 {
		return simulate.Mutation{}, fmt.Errorf("invalid mutation %q: expected type:name", s)
	}

	mutType := simulate.MutationType(parts[0])
	switch mutType {
	case simulate.SignatureChange, simulate.BehaviorChange, simulate.Removal, simulate.Addition:
		// valid
	default:
		return simulate.Mutation{}, fmt.Errorf("invalid mutation type %q", parts[0])
	}

	name := parts[1]
	receiver := ""

	// Parse receiver.name format: *Context.Render → receiver=*Context, name=Render
	if idx := strings.LastIndex(name, "."); idx > 0 {
		candidate := name[:idx]
		if strings.HasPrefix(candidate, "*") || strings.HasPrefix(candidate, "(") {
			receiver = strings.Trim(candidate, "()")
			name = name[idx+1:]
		}
	}

	return simulate.Mutation{
		Type:     mutType,
		Name:     name,
		Receiver: receiver,
	}, nil
}
