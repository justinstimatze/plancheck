// Package refgraph bridges plancheck with defn reference graphs.
//
// Core graph operations use defn/graph (in-memory, O(1) lookups).
// Raw SQL queries (QueryDefn) are kept for coupling analysis and
// pattern search which need the bodies table.
package refgraph

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/justinstimatze/plancheck/internal/doltutil"
	"github.com/justinstimatze/plancheck/internal/types"
)

// Available returns true if a defn database exists for the given project.
func Available(cwd string) bool {
	defnDir := filepath.Join(cwd, ".defn")
	info, err := os.Stat(defnDir)
	return err == nil && info.IsDir()
}

// QueryDefn runs a SQL query against the .defn/ database via the dolt CLI.
// Used for coupling analysis and pattern search (need bodies table).
// For structural queries, use LoadGraph() + typed methods instead.
func QueryDefn(cwd, sql string) []map[string]interface{} {
	rows, _ := doltQuery(cwd, sql)
	return rows
}

// doltQuery runs a SQL query against the .defn/ database via the dolt CLI.
func doltQuery(cwd, sql string) ([]map[string]interface{}, error) {
	defnDir := filepath.Join(cwd, ".defn")
	return doltutil.Query(defnDir, sql)
}

// CheckBlastRadius finds definitions in filesToModify whose callers
// are outside the plan's file set. Uses the in-memory graph.
func CheckBlastRadius(p types.ExecutionPlan, cwd string) []types.ComodGap {
	g := LoadGraph(cwd)
	if g == nil {
		return nil
	}

	planFiles := make(map[string]bool)
	for _, f := range p.FilesToModify {
		planFiles[f] = true
	}
	for _, f := range p.FilesToCreate {
		planFiles[f] = true
	}
	for _, f := range p.FilesToRead {
		planFiles[f] = true
	}

	var gaps []types.ComodGap

	for _, planFile := range p.FilesToModify {
		base := filepath.Base(planFile)
		defs := g.DefsInFile(base, 0) // 0 = all modules

		for _, def := range defs {
			if def.Test || !def.Exported {
				continue
			}
			callerDefs := g.CallerDefs(def.ID)
			totalCallers := len(callerDefs)
			if totalCallers == 0 {
				continue
			}

			// Count callers by source file
			fileCounts := make(map[string]int)
			for _, caller := range callerDefs {
				if !caller.Test && caller.SourceFile != "" {
					fileCounts[caller.SourceFile]++
				}
			}

			for file, count := range fileCounts {
				if planFiles[file] || file == base {
					continue
				}
				freq := float64(count) / float64(totalCallers)
				if freq < 0.1 {
					continue
				}
				gaps = append(gaps, types.ComodGap{
					PlanFile:   planFile,
					ComodFile:  file,
					Frequency:  freq,
					Confidence: confidenceFromCallers(count, totalCallers),
					Suggestion: fmt.Sprintf("%s has %d caller(s) of %s in %s — consider adding to filesToModify",
						file, count, def.Name, planFile),
				})
			}
		}
	}

	// Deduplicate by comod file (keep highest frequency)
	seen := make(map[string]int)
	for i, g := range gaps {
		if prev, ok := seen[g.ComodFile]; ok {
			if g.Frequency > gaps[prev].Frequency {
				seen[g.ComodFile] = i
			}
		} else {
			seen[g.ComodFile] = i
		}
	}
	var deduped []types.ComodGap
	for _, idx := range seen {
		deduped = append(deduped, gaps[idx])
	}

	return deduped
}

// ResolveModuleID maps a relative file path to module_id.
// Uses in-memory graph when available, returns int64 directly.
func ResolveModuleID(cwd, relPath string) int64 {
	g := LoadGraph(cwd)
	if g != nil {
		return ResolveModuleIDFromGoMod(g, cwd, relPath)
	}
	return 0
}

func confidenceFromCallers(callers, total int) string {
	if total == 0 {
		return "moderate"
	}
	ratio := float64(callers) / float64(total)
	if ratio > 0.5 || callers >= 5 {
		return "high"
	}
	return "moderate"
}
