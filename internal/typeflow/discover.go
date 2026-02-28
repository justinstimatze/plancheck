// Package typeflow discovers cross-file dependencies via source-level call site
// verification. Combines defn's reference graph with go/parser to find files
// that have verified function calls to plan file definitions.
package typeflow

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// DiscoveredFile is a file found through source-verified dynamic analysis.
// Unlike structural signals (which say "this file is nearby"), discovered
// files have a concrete, verified code relationship with a plan file.
type DiscoveredFile struct {
	Path      string     // relative path from cwd
	File      string     // basename
	CallSites []CallSite // verified call sites connecting this to plan files
	Direction string     // "caller" (this file calls plan funcs) or "callee" (plan calls this file's funcs)
	Reason    string     // human-readable explanation with line numbers
	Score     float64    // base score (0.0-1.0)
}

// DiscoverVerifiedFiles is the pre-ranking discovery signal.
// For each plan file:
//  1. Parse exported function signatures
//  2. Find callers via in-memory reference graph
//  3. Verify each caller has a real call site by parsing its source
//  4. Also check reverse: parse plan files for calls to other packages
//
// Returns only files with verified source-level relationships.
// This is the highest-precision signal in the system.
//
// graph parameter is the in-memory reference graph (nil = skip).
// queryDefn is kept as fallback but not used when graph is available.
func DiscoverVerifiedFiles(planFiles []string, cwd string, queryDefn func(string, string) []map[string]interface{}, graph interface{}) []DiscoveredFile {
	// Phase 1: Parse plan files for exported functions
	planFuncsByFile := make(map[string][]FuncSig) // relPath → sigs
	allPlanFuncs := make(map[string]bool)
	for _, pf := range planFiles {
		absPath := filepath.Join(cwd, pf)
		sigs, err := ParseExportedSigs(absPath)
		if err != nil {
			continue
		}
		planFuncsByFile[pf] = sigs
		for _, sig := range sigs {
			allPlanFuncs[sig.Name] = true
		}
	}
	if len(allPlanFuncs) == 0 {
		return nil
	}

	// Phase 2: Find candidate callers/callees via graph or defn
	type graphAPI interface {
		CallerFiles(sourceFile string, moduleID int64) map[string]int
		CalleeFiles(sourceFile string, moduleID int64) map[string]int
	}

	candidateFiles := make(map[string]bool)

	if g, ok := graph.(graphAPI); ok {
		// Fast path: in-memory graph
		for _, pf := range planFiles {
			base := filepath.Base(pf)
			for sf := range g.CallerFiles(base, 0) {
				candidateFiles[sf] = true
			}
			for sf := range g.CalleeFiles(base, 0) {
				candidateFiles[sf] = true
			}
		}
	} else if queryDefn != nil {
		// Fallback: dolt subprocess
		for _, pf := range planFiles {
			base := filepath.Base(pf)
			rows := queryDefn(cwd,
				"SELECT DISTINCT caller.source_file "+
					"FROM definitions d "+
					"JOIN `references` r ON r.to_def = d.id "+
					"JOIN definitions caller ON r.from_def = caller.id "+
					"WHERE d.source_file = '"+base+"' AND d.test = FALSE "+
					"AND caller.test = FALSE AND caller.source_file != '' "+
					"AND caller.source_file != '"+base+"' "+
					"LIMIT 15")
			for _, row := range rows {
				if sf, ok := row["source_file"].(string); ok && sf != "" {
					candidateFiles[sf] = true
				}
			}
			rows = queryDefn(cwd,
				"SELECT DISTINCT callee.source_file "+
					"FROM definitions d "+
					"JOIN `references` r ON r.from_def = d.id "+
					"JOIN definitions callee ON r.to_def = callee.id "+
					"WHERE d.source_file = '"+base+"' AND d.test = FALSE "+
					"AND callee.test = FALSE AND callee.source_file != '' "+
					"AND callee.source_file != '"+base+"' "+
					"LIMIT 15")
			for _, row := range rows {
				if sf, ok := row["source_file"].(string); ok && sf != "" {
					candidateFiles[sf] = true
				}
			}
		}
	} else {
		return nil
	}

	// Phase 3: Resolve candidate basenames to full paths
	// defn stores basenames in source_file; we need full paths for parsing.
	resolvedPaths := resolveSourceFiles(candidateFiles, planFiles, cwd)

	// Phase 4: Verify each candidate by parsing actual source.
	// Cap resolved paths per basename to avoid parsing every client.go in the repo.
	planPathSet := make(map[string]bool)
	for _, pf := range planFiles {
		planPathSet[pf] = true
	}

	// Prefer paths near plan files (same parent/grandparent directory)
	planDirs := make(map[string]bool)
	for _, pf := range planFiles {
		d := filepath.Dir(pf)
		for i := 0; i < 3 && d != "." && d != ""; i++ {
			planDirs[d] = true
			d = filepath.Dir(d)
		}
	}

	var discovered []DiscoveredFile
	seen := make(map[string]bool)

	for basename, fullPaths := range resolvedPaths {
		_ = basename
		// Sort paths: prefer those near plan files
		sort.Slice(fullPaths, func(i, j int) bool {
			iNear := planDirs[filepath.Dir(fullPaths[i])]
			jNear := planDirs[filepath.Dir(fullPaths[j])]
			if iNear != jNear {
				return iNear
			}
			return fullPaths[i] < fullPaths[j]
		})
		// Cap at 5 paths per basename
		if len(fullPaths) > 5 {
			fullPaths = fullPaths[:5]
		}
		for _, fullPath := range fullPaths {
			if planPathSet[fullPath] || seen[fullPath] {
				continue
			}

			absPath := filepath.Join(cwd, fullPath)

			// Forward check: does this file call plan file functions?
			callerSites, _ := FindCallSites(absPath, allPlanFuncs)

			// Reverse check: do plan files call this file's functions?
			fileFuncs := make(map[string]bool)
			sigs, _ := ParseExportedSigs(absPath)
			for _, sig := range sigs {
				fileFuncs[sig.Name] = true
			}
			var calleeSites []CallSite
			if len(fileFuncs) > 0 {
				for _, pf := range planFiles {
					pfAbs := filepath.Join(cwd, pf)
					sites, err := FindCallSites(pfAbs, fileFuncs)
					if err == nil {
						for j := range sites {
							sites[j].File = pf
						}
						calleeSites = append(calleeSites, sites...)
					}
				}
			}

			allSites := append(callerSites, calleeSites...)
			if len(allSites) == 0 {
				continue // no verified relationship
			}

			seen[fullPath] = true
			direction := "caller"
			if len(callerSites) == 0 && len(calleeSites) > 0 {
				direction = "callee"
			}

			// Score by number of call sites (more = stronger relationship)
			score := 0.6
			if len(allSites) >= 3 {
				score = 0.8
			} else if len(allSites) >= 2 {
				score = 0.7
			}

			reason := buildReason(allSites, direction)

			discovered = append(discovered, DiscoveredFile{
				Path:      fullPath,
				File:      filepath.Base(fullPath),
				CallSites: allSites,
				Direction: direction,
				Reason:    reason,
				Score:     score,
			})
		}
	}

	return discovered
}

// goFileIndex caches the basename→paths mapping for a cwd.
var goFileIndexCache struct {
	cwd   string
	index map[string][]string // basename → relative paths
}

// buildGoFileIndex walks cwd once and builds a complete basename→paths index.
func buildGoFileIndex(cwd string) map[string][]string {
	if goFileIndexCache.cwd == cwd && goFileIndexCache.index != nil {
		return goFileIndexCache.index
	}

	index := make(map[string][]string)
	filepath.WalkDir(cwd, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if strings.HasPrefix(name, ".") || name == "vendor" || name == "testdata" || name == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") || strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(cwd, path)
		if err != nil {
			return nil
		}
		index[d.Name()] = append(index[d.Name()], filepath.ToSlash(rel))
		return nil
	})

	goFileIndexCache.cwd = cwd
	goFileIndexCache.index = index
	return index
}

// resolveSourceFiles maps defn basenames to full relative paths.
// Uses cached file index — first call builds it, subsequent calls are O(1) lookup.
func resolveSourceFiles(basenames map[string]bool, planFiles []string, cwd string) map[string][]string {
	if len(basenames) == 0 {
		return nil
	}

	index := buildGoFileIndex(cwd)
	result := make(map[string][]string)
	for basename := range basenames {
		if paths, ok := index[basename]; ok {
			result[basename] = paths
		}
	}
	return result
}

// ResolveGoFiles maps basenames to full relative paths using cached file index.
// Exported for use by verified cascade.
func ResolveGoFiles(basenames map[string]bool, cwd string) map[string][]string {
	return resolveSourceFiles(basenames, nil, cwd)
}

// BuildReasonExported builds a human-readable reason from call sites and cascade depth.
func BuildReasonExported(sites []CallSite, depth int) string {
	if len(sites) == 0 {
		return ""
	}
	s := sites[0]
	depthLabel := ""
	if depth > 0 {
		depthLabel = fmt.Sprintf(" (cascade depth %d)", depth)
	}
	if len(sites) > 1 {
		return fmt.Sprintf("%s() calls %s (line %d) + %d more%s — verified",
			s.CallerFunc, s.Callee, s.Line, len(sites)-1, depthLabel)
	}
	return fmt.Sprintf("%s() calls %s (line %d)%s — verified",
		s.CallerFunc, s.Callee, s.Line, depthLabel)
}

func buildReason(sites []CallSite, direction string) string {
	if len(sites) == 0 {
		return ""
	}
	s := sites[0]
	if direction == "callee" {
		return fmt.Sprintf("plan file calls %s (line %d) — verified dependency", s.Callee, s.Line)
	}
	if len(sites) > 1 {
		return fmt.Sprintf("%s() calls %s (line %d) + %d more call sites — verified caller",
			s.CallerFunc, s.Callee, s.Line, len(sites)-1)
	}
	return fmt.Sprintf("%s() calls %s (line %d) — verified caller", s.CallerFunc, s.Callee, s.Line)
}
