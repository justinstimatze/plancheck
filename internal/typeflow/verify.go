package typeflow

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/justinstimatze/plancheck/internal/types"
)

// VerifiedFile is the verification result for a single ranked suggestion.
type VerifiedFile struct {
	Path      string     // relative path
	CallSites []CallSite // concrete call sites to plan file functions
	Verified  bool       // has at least one verified call site
}

// VerifyRankedFiles checks each ranked suggestion against plan file signatures.
// Bidirectional: checks both directions:
//   - Forward: does the ranked file call plan file functions? (caller verification)
//   - Reverse: do plan files call the ranked file's functions? (callee verification)
//
// Only parses files already in the ranked list — O(planFiles + ranked), not O(repo).
func VerifyRankedFiles(planFiles []string, ranked []types.RankedFile, cwd string) []VerifiedFile {
	// Collect exported function names from all plan files
	planFuncs := make(map[string]bool)
	for _, pf := range planFiles {
		absPath := filepath.Join(cwd, pf)
		sigs, err := ParseExportedSigs(absPath)
		if err != nil {
			continue
		}
		for _, sig := range sigs {
			planFuncs[sig.Name] = true
		}
	}

	// Collect exported function names from all ranked files (for reverse check)
	rankedFuncs := make(map[string]map[string]bool) // relPath → set of func names
	for _, r := range ranked {
		relPath := r.Path
		if relPath == "" || !strings.Contains(relPath, "/") {
			continue
		}
		absPath := filepath.Join(cwd, relPath)
		sigs, err := ParseExportedSigs(absPath)
		if err != nil {
			continue
		}
		funcs := make(map[string]bool)
		for _, sig := range sigs {
			funcs[sig.Name] = true
		}
		rankedFuncs[relPath] = funcs
	}

	// Check each ranked file bidirectionally
	results := make([]VerifiedFile, len(ranked))
	for i, r := range ranked {
		relPath := r.Path
		if relPath == "" {
			relPath = r.File
		}
		results[i] = VerifiedFile{Path: relPath}

		if !strings.Contains(relPath, "/") {
			continue
		}

		// Forward: does ranked file call plan file functions?
		if len(planFuncs) > 0 {
			absPath := filepath.Join(cwd, relPath)
			sites, err := FindCallSites(absPath, planFuncs)
			if err == nil && len(sites) > 0 {
				results[i].CallSites = append(results[i].CallSites, sites...)
				results[i].Verified = true
			}
		}

		// Reverse: do plan files call this ranked file's functions?
		if rFuncs, ok := rankedFuncs[relPath]; ok && len(rFuncs) > 0 {
			for _, pf := range planFiles {
				absPath := filepath.Join(cwd, pf)
				sites, err := FindCallSites(absPath, rFuncs)
				if err == nil && len(sites) > 0 {
					// Mark as reverse call sites
					for j := range sites {
						sites[j].File = pf // the plan file is the caller
					}
					results[i].CallSites = append(results[i].CallSites, sites...)
					results[i].Verified = true
				}
			}
		}
	}

	return results
}

// ApplyVerification adjusts ranked file scores based on source-level verification.
// Verified files get boosted; unverified structural suggestions get penalized.
// Non-structural sources (keyword-dir, entity-dir, comod, convention) are NOT penalized.
func ApplyVerification(ranked []types.RankedFile, verified []VerifiedFile) []types.RankedFile {
	if len(verified) == 0 || len(verified) != len(ranked) {
		return ranked
	}

	for i := range ranked {
		v := verified[i]
		if v.Path == "" {
			continue // no verification attempted
		}

		if v.Verified {
			// Boost: add 0.25, cap total boost at +0.3
			boost := 0.25
			if ranked[i].Score+boost > ranked[i].Score*1.5 {
				boost = ranked[i].Score * 0.5
			}
			ranked[i].Score += boost
			ranked[i].Verified = true
			ranked[i].CallSite = formatCallSite(v.CallSites[0])
			// Prepend call-site info to reason
			ranked[i].Reason = fmt.Sprintf("calls %s (line %d) — %s",
				v.CallSites[0].Callee, v.CallSites[0].Line, ranked[i].Reason)
		} else if isStructuralSource(ranked[i].Source) {
			// Penalize unverified structural claims
			ranked[i].Score *= 0.7
		}
		// Non-structural sources: no penalty (can't verify via call sites)
	}

	return ranked
}

// isStructuralSource returns true if the source claims a code-level relationship
// that should be verifiable by parsing (callers, callees, imports, siblings).
func isStructuralSource(source string) bool {
	structural := []string{"structural", "callee", "import-chain", "sibling", "cascade"}
	for _, s := range structural {
		if strings.Contains(source, s) {
			return true
		}
	}
	return false
}

func formatCallSite(cs CallSite) string {
	return fmt.Sprintf("%s() calls %s at line %d", cs.CallerFunc, cs.Callee, cs.Line)
}
