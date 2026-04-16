// Package refgraph bridges plancheck with defn reference graphs.
//
// Core graph operations use defn/graph (in-memory, O(1) lookups).
// Raw SQL queries (QueryDefn) use defn/db for in-process access to
// coupling analysis and pattern search (bodies table).
package refgraph

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	defndb "github.com/justinstimatze/defn/db"
	"github.com/justinstimatze/plancheck/internal/types"
)

// Available returns true if a defn database exists for the given project.
func Available(cwd string) bool {
	defnDir := filepath.Join(cwd, ".defn")
	info, err := os.Stat(defnDir)
	return err == nil && info.IsDir()
}

// dbCache caches open defn database handles per cwd.
var (
	dbCache   = make(map[string]*defndb.DB)
	dbCacheMu sync.Mutex
)

// openDB returns a cached database handle for the given cwd.
// Returns nil if .defn/ doesn't exist.
func openDB(cwd string) *defndb.DB {
	dbCacheMu.Lock()
	defer dbCacheMu.Unlock()
	if d, ok := dbCache[cwd]; ok {
		return d
	}
	defnDir := filepath.Join(cwd, ".defn")
	if info, err := os.Stat(defnDir); err != nil || !info.IsDir() {
		return nil
	}
	d, err := defndb.Open(defnDir)
	if err != nil {
		return nil
	}
	dbCache[cwd] = d
	return d
}

// CloseDBCache closes all cached database handles. Call at process exit.
func CloseDBCache() {
	dbCacheMu.Lock()
	defer dbCacheMu.Unlock()
	for k, d := range dbCache {
		d.Close()
		delete(dbCache, k)
	}
}

// QueryDefn runs a SQL query against the .defn/ database using the
// embedded Dolt engine (no subprocess). Used for coupling analysis and
// pattern search (bodies table). For structural queries, use LoadGraph().
func QueryDefn(cwd, sql string) []map[string]interface{} {
	d := openDB(cwd)
	if d == nil {
		return nil
	}
	rows, _ := d.Query(sql)
	return rows
}

// QueryDefnDir runs a SQL query against a .defn/ directory directly.
// Used by simulate package which passes the defnDir path rather than cwd.
func QueryDefnDir(defnDir, sql string) []map[string]interface{} {
	// Strip trailing "/.defn" to get cwd for cache key
	cwd := filepath.Dir(defnDir)
	return QueryDefn(cwd, sql)
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
