package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/justinstimatze/plancheck/internal/history"
)

type StatsCmd struct {
	Out  string `help:"Write output to file instead of stdout." short:"o"`
	Last int    `help:"Only include the last N plans per project (0 = all)." short:"n" default:"0"`
}

type projectData struct {
	cwd     string
	summary history.HistorySummary
}

func (c *StatsCmd) Run() error {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "plancheck: cannot determine home directory: %v\n", err)
		os.Exit(2)
	}

	projectsDir := filepath.Join(home, ".plancheck", "projects")
	dirEntries, err := os.ReadDir(projectsDir)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Println("plancheck stats: no project data found")
			return nil
		}
		fmt.Fprintf(os.Stderr, "plancheck: cannot read projects dir: %v\n", err)
		os.Exit(2)
	}

	var projects []projectData

	for _, de := range dirEntries {
		if !de.IsDir() {
			continue
		}
		projFile := filepath.Join(projectsDir, de.Name(), "project.txt")
		data, err := os.ReadFile(projFile)
		if err != nil {
			continue
		}
		cwd := strings.TrimSpace(string(data))
		if cwd == "" {
			continue
		}

		summary, err := history.LoadHistory(cwd)
		if err != nil {
			continue
		}

		if len(summary.Entries) == 0 && len(summary.Outcomes) == 0 && len(summary.Reflections) == 0 {
			continue
		}

		projects = append(projects, projectData{cwd: cwd, summary: summary})
	}

	// Apply recency filter — keep last N plans per project
	if c.Last > 0 {
		for i := range projects {
			projects[i].summary = tailSummary(projects[i].summary, c.Last)
		}
		filtered := projects[:0]
		for _, p := range projects {
			s := p.summary
			if len(s.Entries) > 0 || len(s.Outcomes) > 0 || len(s.Reflections) > 0 {
				filtered = append(filtered, p)
			}
		}
		projects = filtered
	}

	if len(projects) == 0 {
		fmt.Println("plancheck stats: no project data found")
		return nil
	}

	// Redirect output to file if requested
	if c.Out != "" {
		f, err := os.Create(c.Out)
		if err != nil {
			fmt.Fprintf(os.Stderr, "plancheck: cannot create %s: %v\n", c.Out, err)
			os.Exit(2)
		}
		defer f.Close()
		os.Stdout = f
	}

	// --- Aggregate summary ---
	var (
		totalChecks int
		outcomes    = map[string]int{"clean": 0, "rework": 0, "failed": 0}
		allRefs     []history.ReflectionEntry
		missed      []string
	)

	for _, p := range projects {
		totalChecks += len(p.summary.Entries)
		for _, o := range p.summary.Outcomes {
			outcomes[o]++
		}
		for _, r := range p.summary.Reflections {
			allRefs = append(allRefs, r)
			if r.Missed != "" {
				missed = append(missed, r.Missed)
			}
		}
	}

	var iterChanged, probesCaught, personasCaught int
	for _, r := range allRefs {
		if r.ProbeFindings > 0 || r.PersonaFindings > 0 {
			iterChanged++
		}
		if r.ProbeFindings > 0 {
			probesCaught++
		}
		if r.PersonaFindings > 0 {
			personasCaught++
		}
	}

	totalReflections := len(allRefs)

	if c.Last > 0 {
		fmt.Printf("plancheck stats (%d projects, %d plans, last %d per project)\n\n", len(projects), totalChecks, c.Last)
	} else {
		fmt.Printf("plancheck stats (%d projects, %d plans)\n\n", len(projects), totalChecks)
	}
	fmt.Printf("  outcomes:       %d clean, %d rework, %d failed\n",
		outcomes["clean"], outcomes["rework"], outcomes["failed"])
	fmt.Printf("  reflections:    %d total\n", totalReflections)

	if totalReflections > 0 {
		fmt.Printf("    iteration changed plan:    %d/%d (%d%%)\n",
			iterChanged, totalReflections, pct(iterChanged, totalReflections))
		fmt.Printf("    probes caught something:   %d/%d (%d%%)\n",
			probesCaught, totalReflections, pct(probesCaught, totalReflections))
		fmt.Printf("    personas caught something: %d/%d (%d%%)\n",
			personasCaught, totalReflections, pct(personasCaught, totalReflections))
	}

	if len(missed) > 0 {
		fmt.Println("\n  missed:")
		for _, m := range missed {
			fmt.Printf("    - %q\n", m)
		}
	}

	totalOutcomes := outcomes["clean"] + outcomes["rework"] + outcomes["failed"]
	fmt.Printf("\n  verdict: %s\n", verdict(totalReflections, iterChanged, outcomes["rework"]+outcomes["failed"], totalOutcomes))

	// --- Per-project narratives ---
	fmt.Println("\n" + strings.Repeat("=", 60))
	fmt.Println("PER-PROJECT DETAIL")
	fmt.Println(strings.Repeat("=", 60))

	for _, p := range projects {
		printProjectNarrative(p)
	}

	return nil
}

func printProjectNarrative(p projectData) {
	fmt.Printf("\n--- %s ---\n", p.cwd)

	s := p.summary
	if len(s.Entries) == 0 {
		fmt.Println("  (no check entries)")
		return
	}

	fmt.Printf("  %d checks, %d outcomes, %d reflections\n\n", len(s.Entries), len(s.Outcomes), len(s.Reflections))

	for _, e := range s.Entries {
		ts := e.Timestamp
		if len(ts) > 16 {
			ts = ts[:16]
		}

		fmt.Printf("  [%s] check %s\n", ts, e.ID)
		fmt.Printf("    objective: %s\n", e.Objective)

		if len(e.ComodMisses) > 0 {
			fmt.Printf("    comod gaps: %s\n", strings.Join(e.ComodMisses, ", "))
		}
		if len(e.SuggestedModify) > 0 {
			fmt.Printf("    suggested adds: %s\n", strings.Join(e.SuggestedModify, ", "))
		}

		if outcome, ok := s.Outcomes[e.ID]; ok {
			fmt.Printf("    outcome: %s\n", outcome)
		}

		if ref, ok := s.Reflections[e.ID]; ok {
			fmt.Printf("    reflection: %d passes, %d probe findings, %d persona findings\n",
				ref.Passes, ref.ProbeFindings, ref.PersonaFindings)
			if ref.Missed != "" {
				fmt.Printf("    missed: %q\n", ref.Missed)
			}
			if len(ref.SignalsUseful) > 0 {
				fmt.Printf("    useful signals: %s\n", strings.Join(ref.SignalsUseful, ", "))
			}
		}

		fmt.Println()
	}
}

func tailSummary(s history.HistorySummary, n int) history.HistorySummary {
	entries := s.Entries
	if len(entries) > n {
		entries = entries[len(entries)-n:]
	}

	kept := make(map[string]bool)
	for _, e := range entries {
		kept[e.ID] = true
	}

	outcomes := make(map[string]string)
	for id, o := range s.Outcomes {
		if kept[id] {
			outcomes[id] = o
		}
	}

	reflections := make(map[string]history.ReflectionEntry)
	for id, r := range s.Reflections {
		if kept[id] {
			reflections[id] = r
		}
	}

	return history.HistorySummary{
		Entries:     entries,
		Outcomes:    outcomes,
		Reflections: reflections,
	}
}

func pct(n, total int) int {
	if total == 0 {
		return 0
	}
	return (n * 100) / total
}

func verdict(reflections, changed, bad, totalOutcomes int) string {
	if reflections == 0 {
		return "not enough data — keep using it"
	}
	changePct := pct(changed, reflections)
	if totalOutcomes > 0 && pct(bad, totalOutcomes) > 50 {
		return "plans pass checks but still fail — probes may need tuning"
	}
	if changePct > 50 {
		return "iteration is catching real gaps"
	}
	if changePct < 20 {
		return "iteration is mostly rubber-stamping"
	}
	return "iteration is helping sometimes — more data needed"
}
