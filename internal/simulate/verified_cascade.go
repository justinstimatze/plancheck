package simulate

import (
	"path/filepath"
	"sort"

	"github.com/justinstimatze/plancheck/internal/refgraph"
	"github.com/justinstimatze/plancheck/internal/typeflow"
)

// VerifiedCascadeResult is a cascade where each affected file has been
// verified via source-level call site analysis. Only files with concrete
// call sites to the mutated functions are included.
type VerifiedCascadeResult struct {
	Steps       []VerifiedCascadeStep `json:"steps"`
	MaxDepth    int                   `json:"maxDepth"`
	TotalFiles  int                   `json:"totalFiles"`
	Converged   bool                  `json:"converged"`
	VerifiedFiles []typeflow.DiscoveredFile `json:"verifiedFiles"` // all verified files across depths
}

// VerifiedCascadeStep is one depth level of the verified cascade.
type VerifiedCascadeStep struct {
	Depth         int      `json:"depth"`
	FuncNames     []string `json:"funcNames"`     // function names at this depth
	VerifiedFiles int      `json:"verifiedFiles"` // files verified at this depth
	TotalCallers  int      `json:"totalCallers"`  // callers from graph (before verification)
}

// CascadeVerified runs a source-verified cascade simulation.
// At each depth:
//  1. Find callers of current functions via in-memory graph
//  2. Resolve caller basenames to full paths
//  3. Verify each caller file has a real call site via go/parser
//  4. Only verified callers propagate to the next depth
//
// This is the precision-optimized cascade — it trades recall for near-100%
// precision. Every file in the output has a verified source-level connection.
// CascadeVerified runs a source-verified cascade simulation.
// startFuncs limits which functions to trace from (nil = all exported).
// When provided, only these functions are treated as mutation targets,
// making the cascade surgical rather than blasting every exported function.
func CascadeVerified(cwd string, graph *refgraph.Graph, planFiles []string, maxDepth int, startFuncs ...map[string]bool) VerifiedCascadeResult {
	if graph == nil {
		return VerifiedCascadeResult{}
	}
	if maxDepth <= 0 {
		maxDepth = 2
	}
	if maxDepth > 3 {
		maxDepth = 3
	}

	result := VerifiedCascadeResult{}
	allVerified := make(map[string]*typeflow.DiscoveredFile)

	// Start with inferred mutation targets if provided, else all exported functions
	currentFuncs := make(map[string]bool)
	if len(startFuncs) > 0 && startFuncs[0] != nil {
		for k, v := range startFuncs[0] {
			currentFuncs[k] = v
		}
	} else {
		for _, pf := range planFiles {
			absPath := filepath.Join(cwd, pf)
			sigs, err := typeflow.ParseExportedSigs(absPath)
			if err != nil {
				continue
			}
			for _, sig := range sigs {
				currentFuncs[sig.Name] = true
			}
		}
	}

	planFileSet := make(map[string]bool)
	for _, pf := range planFiles {
		planFileSet[pf] = true
		planFileSet[filepath.Base(pf)] = true
	}

	for depth := 0; depth < maxDepth; depth++ {
		if len(currentFuncs) == 0 {
			result.Converged = true
			break
		}

		step := VerifiedCascadeStep{
			Depth:     depth,
			FuncNames: sortedKeys(currentFuncs),
		}

		// Find caller files via graph for all current functions
		callerBasenames := make(map[string]bool)
		for funcName := range currentFuncs {
			def := graph.GetDef(funcName, "")
			if def == nil {
				continue
			}
			for _, caller := range graph.CallerDefs(def.ID) {
				if caller.Test || caller.SourceFile == "" || planFileSet[caller.SourceFile] {
					continue
				}
				callerBasenames[caller.SourceFile] = true
			}
		}
		step.TotalCallers = len(callerBasenames)

		// Resolve basenames to full paths
		resolved := typeflow.ResolveGoFiles(callerBasenames, cwd)

		// Verify each caller has a real call site to current functions
		nextFuncs := make(map[string]bool)

		for _, fullPaths := range resolved {
			// Cap per basename
			if len(fullPaths) > 3 {
				fullPaths = fullPaths[:3]
			}
			for _, fullPath := range fullPaths {
				if allVerified[fullPath] != nil || planFileSet[fullPath] {
					continue
				}

				absPath := filepath.Join(cwd, fullPath)
				sites, err := typeflow.FindCallSites(absPath, currentFuncs)
				if err != nil || len(sites) == 0 {
					continue
				}

				// Verified! This file has a real call site.
				step.VerifiedFiles++

				vf := &typeflow.DiscoveredFile{
					Path:      fullPath,
					File:      filepath.Base(fullPath),
					CallSites: sites,
					Direction: "cascade",
					Score:     cascadeScore(depth),
					Reason:    typeflow.BuildReasonExported(sites, depth),
				}
				allVerified[fullPath] = vf

				// Extract this file's exported functions for next depth
				sigs, err := typeflow.ParseExportedSigs(absPath)
				if err == nil {
					for _, sig := range sigs {
						nextFuncs[sig.Name] = true
					}
				}
			}
		}

		result.Steps = append(result.Steps, step)

		// Only propagate to next depth if we found verified callers
		currentFuncs = nextFuncs
	}

	if len(currentFuncs) == 0 {
		result.Converged = true
	}

	// Collect all verified files, cap at 10 (quality > quantity)
	for _, vf := range allVerified {
		result.VerifiedFiles = append(result.VerifiedFiles, *vf)
	}
	sort.Slice(result.VerifiedFiles, func(i, j int) bool {
		return result.VerifiedFiles[i].Score > result.VerifiedFiles[j].Score
	})
	if len(result.VerifiedFiles) > 10 {
		result.VerifiedFiles = result.VerifiedFiles[:10]
	}

	result.MaxDepth = len(result.Steps)
	result.TotalFiles = len(result.VerifiedFiles)

	return result
}

func cascadeScore(depth int) float64 {
	// Depth 0: direct callers (highest confidence)
	// Depth 1: callers of callers
	// Depth 2+: diminishing returns
	switch depth {
	case 0:
		return 0.7
	case 1:
		return 0.5
	default:
		return 0.35
	}
}

func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
