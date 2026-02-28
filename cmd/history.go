package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/justinstimatze/plancheck/internal/history"
)

type HistoryCmd struct {
	Cwd   string `help:"Project root." default:"."`
	Limit int    `help:"Number of entries to show." default:"10"`
	JSON  bool   `help:"Output raw JSON." name:"json"`
}

func (c *HistoryCmd) Run() error {
	absCwd, _ := filepath.Abs(c.Cwd)
	summary, err := history.LoadHistory(absCwd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "plancheck: cannot read history: %v\n", err)
		os.Exit(2)
	}

	if c.JSON {
		out, _ := json.MarshalIndent(summary, "", "  ")
		fmt.Println(string(out))
		return nil
	}

	entries := summary.Entries
	if len(entries) == 0 {
		fmt.Println("plancheck: no history for this project")
		return nil
	}

	// Newest first
	for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
		entries[i], entries[j] = entries[j], entries[i]
	}

	// Apply limit
	if c.Limit > 0 && len(entries) > c.Limit {
		entries = entries[:c.Limit]
	}

	fmt.Printf("plancheck history (last %d checks)\n\n", len(entries))

	for _, e := range entries {
		date := e.Timestamp
		if len(date) >= 10 {
			date = date[:10]
		}

		obj := e.Objective
		if len(obj) > 50 {
			obj = obj[:50] + "..."
		}

		outcome := ""
		if o, ok := summary.Outcomes[e.ID]; ok {
			outcome = " -> " + o
		}

		fmt.Printf("  %s  %s  %q%s\n", e.ID, date, obj, outcome)
	}
	fmt.Println()
	return nil
}
