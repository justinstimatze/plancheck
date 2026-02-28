package plan

import (
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"strings"

	"github.com/justinstimatze/plancheck/internal/comod"
	"github.com/justinstimatze/plancheck/internal/refgraph"
	"github.com/justinstimatze/plancheck/internal/typeflow"
	"github.com/justinstimatze/plancheck/internal/types"
)

// rankingInput bundles all inputs to rankCandidateFiles.
type rankingInput struct {
	Plan            types.ExecutionPlan
	ComodGaps       []types.ComodGap
	DirGaps         []comod.DirGap
	AnalogyRanked   []types.RankedFile
	KeywordRanked   []types.RankedFile
	CascadeFiles    []string
	PeerFiles       []peerFile
	ImportFiles     []importChainFile
	ConventionPeers []conventionPeer
	VerifiedFiles   []typeflow.DiscoveredFile
	SpikeFiles      map[string]bool // files the spike touched — used for precision filtering
	Graph           *refgraph.Graph // in-memory graph (nil = use subprocess)
	NoveltyScore    float64
	Cwd             string
	K               int
}

// rankCandidateFiles scores and ranks files by combined signals, weighted by novelty.
// As novelty increases, analogy and semantic weights increase while structural decreases.
func rankCandidateFiles(ri rankingInput) []types.RankedFile {
	p := ri.Plan
	comodGaps := ri.ComodGaps
	dirGaps := ri.DirGaps
	analogyRanked := ri.AnalogyRanked
	cascadeFiles := ri.CascadeFiles
	peerFiles := ri.PeerFiles
	noveltyScore := ri.NoveltyScore
	cwd := ri.Cwd
	k := ri.K
	scores := make(map[string]float64)
	sources := make(map[string][]string)
	reasons := make(map[string]string) // task-specific reason for each file
	paths := make(map[string]string)   // basename → full relative path (when known)

	planSet := make(map[string]bool)
	for _, f := range p.FilesToModify {
		planSet[filepath.Base(f)] = true
	}
	for _, f := range p.FilesToCreate {
		planSet[filepath.Base(f)] = true
	}

	// Build hub suppression set from comod gaps.
	hubSet := make(map[string]bool)
	for _, gap := range comodGaps {
		if gap.Hub {
			hubSet[filepath.Base(gap.ComodFile)] = true
		}
	}

	// Novelty-adjusted weights
	structWeight := 0.5 * (1.0 - noveltyScore*0.8) // 0.5 → 0.1
	comodWeight := 0.35 * (1.0 - noveltyScore*0.7) // 0.35 → 0.1
	analogyWeight := 0.05 + noveltyScore*0.35      // 0.05 → 0.4
	semanticWeight := 0.1 + noveltyScore*0.3       // 0.1 → 0.4

	// Source-verified signal: files with verified call sites to plan file functions.
	// Highest-precision signal — entered via dynamic source analysis, not metadata.
	for _, vf := range ri.VerifiedFiles {
		key := vf.Path
		if key == "" {
			key = vf.File
		}
		if planSet[vf.File] || planSet[key] {
			continue
		}
		scores[key] += vf.Score
		sources[key] = appendUnique(sources[key], "verified-"+vf.Direction)
		if vf.Path != "" {
			paths[key] = vf.Path
		}
		reasons[key] = vf.Reason
	}

	// Structural signal: callers' source files, weighted by coupling strength.
	g := ri.Graph
	if g != nil || refgraph.Available(cwd) {
		for _, f := range p.FilesToModify {
			base := filepath.Base(f)
			var modID int64
			if g != nil {
				modID = refgraph.ResolveModuleIDFromGoMod(g, cwd, f)
			} else {
				modID = refgraph.ResolveModuleID(cwd, f)
			}

			var targetNames []string
			if g != nil {
				targetNames = g.ExportedNames(base, modID)
				if len(targetNames) > 5 {
					targetNames = targetNames[:5]
				}
			} else {
				modFilter := ""
				if modID > 0 {
					modFilter = fmt.Sprintf(" AND d.module_id = %d", modID)
				}
				targetRows := refgraph.QueryDefn(cwd,
					"SELECT name FROM definitions d WHERE source_file = '"+base+
						"'"+modFilter+" AND test = FALSE AND exported = TRUE LIMIT 5")
				for _, tr := range targetRows {
					if n, ok := tr["name"].(string); ok && n != "" {
						targetNames = append(targetNames, n)
					}
				}
			}

			// Get caller files
			var callerFiles map[string]int
			if g != nil {
				callerFiles = g.CallerFiles(base, modID)
			} else {
				modFilter := ""
				if modID > 0 {
					modFilter = fmt.Sprintf(" AND d.module_id = %d", modID)
				}
				rows := refgraph.QueryDefn(cwd,
					"SELECT DISTINCT caller.source_file "+
						"FROM definitions d "+
						"JOIN `references` r ON r.to_def = d.id "+
						"JOIN definitions caller ON r.from_def = caller.id "+
						"WHERE d.source_file = '"+base+"'"+modFilter+" AND d.test = FALSE "+
						"AND caller.test = FALSE AND caller.source_file != '' "+
						"AND caller.source_file != '"+base+"'")
				callerFiles = make(map[string]int)
				for _, row := range rows {
					if sf, ok := row["source_file"].(string); ok && sf != "" {
						callerFiles[sf]++
					}
				}
			}

			for sf := range callerFiles {
				if sf == "" || sf == base {
					continue
				}

				couplingWeight := 0.5
				if len(targetNames) > 0 {
					kind := refgraph.ClassifyCallerCoupling(cwd, "", "", targetNames[0])
					couplingWeight = refgraph.CouplingWeight(kind)
				}

				hubPenalty := 1.0
				if hubSet[sf] {
					hubPenalty = 0.4
				}

				scores[sf] += structWeight * couplingWeight * hubPenalty
				sources[sf] = appendUnique(sources[sf], "structural")
				if reasons[sf] == "" {
					if couplingWeight >= 0.8 {
						reasons[sf] = fmt.Sprintf("directly calls definitions in %s — must change if signatures change", base)
					} else {
						reasons[sf] = fmt.Sprintf("references definitions in %s", base)
					}
				}
			}
		}
	}

	// Outgoing references: files that plan files CALL (callees).
	if g != nil || refgraph.Available(cwd) {
		for _, f := range p.FilesToModify {
			base := filepath.Base(f)
			var modID int64
			if g != nil {
				modID = refgraph.ResolveModuleIDFromGoMod(g, cwd, f)
			} else {
				modID = refgraph.ResolveModuleID(cwd, f)
			}

			var calleeFiles map[string]int
			if g != nil {
				calleeFiles = g.CalleeFiles(base, modID)
			} else {
				modFilter := ""
				if modID > 0 {
					modFilter = fmt.Sprintf(" AND d.module_id = %d", modID)
				}
				rows := refgraph.QueryDefn(cwd,
					"SELECT DISTINCT callee.source_file "+
						"FROM definitions d "+
						"JOIN `references` r ON r.from_def = d.id "+
						"JOIN definitions callee ON r.to_def = callee.id "+
						"WHERE d.source_file = '"+base+"'"+modFilter+" AND d.test = FALSE "+
						"AND callee.test = FALSE AND callee.source_file != '' "+
						"AND callee.source_file != '"+base+"'")
				calleeFiles = make(map[string]int)
				for _, row := range rows {
					if sf, ok := row["source_file"].(string); ok && sf != "" {
						calleeFiles[sf]++
					}
				}
			}

			for sf := range calleeFiles {
				if sf == "" || sf == base {
					continue
				}
				// Soft hub suppression for callees
				hubPenalty := 1.0
				if hubSet[sf] {
					hubPenalty = 0.4
				}
				scores[sf] += structWeight * 0.4 * hubPenalty
				sources[sf] = appendUnique(sources[sf], "callee")
				if reasons[sf] == "" {
					reasons[sf] = fmt.Sprintf("called by definitions in %s — may need changes if interfaces change", base)
				}
			}
		}
	}

	// Body pattern signal
	if refgraph.Available(cwd) {
		patternMatches := refgraph.PatternSearchForPlan(cwd, p.Objective, p.Steps, p.FilesToModify)
		for _, pm := range patternMatches {
			patternWeight := 0.3 + (1.0-noveltyScore)*0.2
			scores[pm.File] += patternWeight
			sources[pm.File] = appendUnique(sources[pm.File], "pattern")
			reasons[pm.File] = fmt.Sprintf("contains %d definitions with the same code pattern", pm.Occurrences)
		}
	}

	// Directory sibling signal — uses path as key when available.
	// Siblings whose names share tokens with plan files or objective
	// get a boost (e.g., query_builder.go boosted when plan has queries_pr.go).
	planTokens := make(map[string]bool)
	for _, f := range p.FilesToModify {
		for _, tok := range splitFileTokens(filepath.Base(f)) {
			planTokens[tok] = true
		}
	}
	for _, tok := range splitFileTokens(p.Objective) {
		planTokens[tok] = true
	}
	dirSiblings := findDirectorySiblings(p, cwd)
	for _, sib := range dirSiblings {
		key := sib.file
		if sib.relPath != "" {
			key = sib.relPath
		}
		if !planSet[sib.file] && !hubSet[sib.file] {
			weight := 0.35
			// Boost if sibling name shares tokens with plan files/objective
			sibTokens := splitFileTokens(sib.file)
			for _, tok := range sibTokens {
				if planTokens[tok] {
					weight = 0.55
					break
				}
			}
			scores[key] += weight
			sources[key] = appendUnique(sources[key], "dir-sibling")
			if sib.relPath != "" {
				paths[key] = sib.relPath
			}
			if reasons[key] == "" {
				reasons[key] = fmt.Sprintf("in same directory as %s", sib.planFile)
			}
		}
	}

	// Parent registration signal
	parentFiles := findParentRegistrations(p, cwd)
	for _, pf := range parentFiles {
		if !planSet[pf.file] && !hubSet[pf.file] {
			scores[pf.file] += 0.25
			sources[pf.file] = appendUnique(sources[pf.file], "parent")
			if reasons[pf.file] == "" {
				reasons[pf.file] = fmt.Sprintf("parent package of %s — may register or configure this subcommand", pf.planFile)
			}
		}
	}

	// Ancestor scope: files in peer directories under the feature root.
	// Uses path as key when available.
	for _, pf := range peerFiles {
		if !planSet[pf.file] && !hubSet[pf.file] {
			key := pf.file
			if pf.fullPath != "" {
				key = pf.fullPath
			}
			weight := 0.2
			if strings.Contains(strings.ToLower(filepath.Dir(pf.fullPath)), "shared") ||
				strings.Contains(strings.ToLower(filepath.Dir(pf.fullPath)), "common") {
				weight = 0.3
			}
			scores[key] += weight
			sources[key] = appendUnique(sources[key], "peer-dir")
			if pf.fullPath != "" {
				paths[key] = pf.fullPath
			}
			if reasons[key] == "" {
				reasons[key] = fmt.Sprintf("in peer package %s/ under same feature root", filepath.Base(filepath.Dir(pf.fullPath)))
			}
		}
	}

	// Import chain signal: files from Go import relationships.
	// Direct imports (dependencies) score higher than importers (dependents).
	for _, icf := range ri.ImportFiles {
		key := icf.fullPath
		if key == "" {
			key = icf.file
		}
		if planSet[icf.file] {
			continue
		}
		// Direct dependencies bypass hub suppression — explicit import relationship.
		// Importers still get soft suppression.
		if hubSet[icf.file] && icf.dir != "import" {
			continue
		}
		weight := structWeight * 0.6 // dependencies
		reason := fmt.Sprintf("imported by %s's package — dependency that may need changes", icf.planFile)
		if icf.dir == "importer" {
			// Coupling-density weighted: high-coupling importers (5+ refs) get full weight
			couplingFactor := 0.15 + 0.45*math.Min(float64(icf.refCount)/5.0, 1.0)
			weight = structWeight * couplingFactor
			if icf.refCount > 0 {
				reason = fmt.Sprintf("imports %s's package with %d cross-references — tightly coupled dependent", icf.planFile, icf.refCount)
			} else {
				reason = fmt.Sprintf("imports %s's package — dependent that may be affected", icf.planFile)
			}
		}
		scores[key] += weight
		sources[key] = appendUnique(sources[key], "import-chain")
		if key != icf.file {
			paths[key] = icf.fullPath
		}
		if reasons[key] == "" {
			reasons[key] = reason
		}
	}

	// Convention peer signal: files paired by directory convention patterns.
	// Different convention types get different weights and source tags.
	for _, cp := range ri.ConventionPeers {
		key := cp.fullPath
		if key == "" {
			key = cp.file
		}
		if planSet[cp.file] || planSet[key] {
			continue
		}
		// Hub suppression: skip for generic conventions, soft for structural ones
		if hubSet[cp.file] && cp.pattern != "shared-pkg" && cp.pattern != "cmdutil" {
			continue
		}

		var weight float64
		var source, reason string
		switch {
		case cp.pattern == "shared-pkg":
			weight = 0.6
			source = "shared-pkg"
			reason = fmt.Sprintf("shared package peer — %s likely imports from shared/", filepath.Base(cp.planFile))
		case strings.HasPrefix(cp.pattern, "sibling-cmd:"):
			weight = 0.55
			source = "sibling-cmd"
			sibling := strings.TrimPrefix(cp.pattern, "sibling-cmd:")
			reason = fmt.Sprintf("sibling command %s/ — task mentions this command", sibling)
		case cp.pattern == "cmdutil":
			weight = 0.45
			source = "cmdutil"
			reason = fmt.Sprintf("infrastructure file — task keywords suggest %s needs changes", cp.file)
		default:
			weight = 0.5
			source = "convention"
			reason = fmt.Sprintf("convention pattern %s — co-occurs in 5+ directories with %s", cp.pattern, filepath.Base(cp.planFile))
		}

		scores[key] += weight
		sources[key] = appendUnique(sources[key], source)
		if cp.fullPath != "" {
			paths[key] = cp.fullPath
		}
		if reasons[key] == "" {
			reasons[key] = reason
		}
	}

	// defn module sibling signal
	if g != nil || refgraph.Available(cwd) {
		for _, f := range p.FilesToModify {
			base := filepath.Base(f)
			var modID int64
			if g != nil {
				modID = refgraph.ResolveModuleIDFromGoMod(g, cwd, f)
			} else {
				modID = refgraph.ResolveModuleID(cwd, f)
			}

			var sibFiles []string
			if g != nil {
				sibFiles = g.SiblingFiles(base, modID)
				if len(sibFiles) > 10 {
					sibFiles = sibFiles[:10]
				}
			} else {
				var sibQuery string
				if modID > 0 {
					sibQuery = fmt.Sprintf(
						"SELECT DISTINCT source_file FROM definitions "+
							"WHERE source_file != '' AND source_file != '%s' "+
							"AND test = FALSE AND module_id = %d "+
							"LIMIT 10", base, modID)
				} else {
					sibQuery = "SELECT DISTINCT source_file FROM definitions " +
						"WHERE source_file != '' AND source_file != '" + base + "' " +
						"AND test = FALSE AND module_id IN " +
						"(SELECT module_id FROM definitions WHERE source_file = '" + base + "') " +
						"LIMIT 10"
				}
				sibRows := refgraph.QueryDefn(cwd, sibQuery)
				for _, row := range sibRows {
					if sf, ok := row["source_file"].(string); ok && sf != "" {
						sibFiles = append(sibFiles, sf)
					}
				}
			}

			for _, sf := range sibFiles {
				if !hubSet[sf] {
					scores[sf] += 0.15
					sources[sf] = appendUnique(sources[sf], "sibling")
					if reasons[sf] == "" {
						reasons[sf] = fmt.Sprintf("in same package as %s", base)
					}
				}
			}
		}
	}

	// Comod signal — soft hub suppression for high-frequency hub files
	for _, gap := range comodGaps {
		if gap.Acknowledged {
			continue
		}
		f := filepath.Base(gap.ComodFile)
		hubPenalty := 1.0
		if gap.Hub {
			if gap.Frequency >= 0.4 {
				hubPenalty = 0.5 // high-comod hub: penalize but keep
			} else {
				continue // low-comod hub: drop
			}
		}
		scores[f] += gap.Frequency * comodWeight * hubPenalty
		sources[f] = appendUnique(sources[f], "comod")
		reasons[f] = fmt.Sprintf("co-changes with %s %d%% of the time", gap.PlanFile, int(gap.Frequency*100+0.5))
	}

	// Directory comod — uses full path as key when available
	for _, dg := range dirGaps {
		base := filepath.Base(dg.ComodFile)
		key := dg.ComodFile // dir-comod provides full relative paths
		if key == base {
			key = base // fallback to basename if no path
		}
		if !hubSet[base] {
			scores[key] += dg.Frequency * comodWeight * 0.8
			sources[key] = appendUnique(sources[key], "dir-comod")
			if key != base {
				paths[key] = key
			}
			if reasons[key] == "" {
				reasons[key] = fmt.Sprintf("co-changes with files in %s/ %d%% of the time", filepath.Base(dg.PlanDir), int(dg.Frequency*100+0.5))
			}
		}
	}

	// Cascade signal
	for _, cf := range cascadeFiles {
		if !hubSet[cf] {
			scores[cf] += structWeight * 0.8
			sources[cf] = appendUnique(sources[cf], "cascade")
			if reasons[cf] == "" {
				reasons[cf] = "affected by cascading changes through the reference graph"
			}
		}
	}

	// Analogy signal
	for _, ar := range analogyRanked {
		scores[ar.File] += ar.Score * analogyWeight
		sources[ar.File] = appendUnique(sources[ar.File], "analogy")
		if reasons[ar.File] == "" {
			reasons[ar.File] = "similar pattern found in other Go projects"
		}
	}

	// Keyword-directory signal: files in directories that match task keywords.
	// Uses FULL PATH as key (not basename) to avoid merging with structural
	// signals for different files that share a basename (e.g., codespace/list.go
	// vs run/list/list.go).
	for _, kr := range ri.KeywordRanked {
		key := kr.File
		if kr.Path != "" {
			key = kr.Path
		}
		if !planSet[kr.File] && !hubSet[kr.File] {
			scores[key] += kr.Score
			srcTag := "keyword-dir"
			if kr.Source != "" {
				srcTag = kr.Source
			}
			sources[key] = appendUnique(sources[key], srcTag)
			if kr.Path != "" {
				paths[key] = kr.Path
			}
			if kr.Reason != "" {
				reasons[key] = kr.Reason
			}
		}
	}

	// Semantic signal
	for _, ss := range p.SemanticSuggestions {
		f := filepath.Base(ss.File)
		scores[f] += ss.Confidence * semanticWeight
		sources[f] = appendUnique(sources[f], "semantic")
		if ss.Reason != "" {
			reasons[f] = ss.Reason
		}
	}

	// Merge basename-keyed and path-keyed entries that refer to the same file.
	// Only merge when exactly ONE path-keyed entry has that basename (unambiguous).
	baseToPath := make(map[string][]string)
	for key := range scores {
		if strings.Contains(key, "/") {
			base := filepath.Base(key)
			baseToPath[base] = append(baseToPath[base], key)
		}
	}
	for base, pathKeys := range baseToPath {
		if len(pathKeys) != 1 {
			continue // ambiguous — multiple paths share this basename
		}
		if _, hasBase := scores[base]; !hasBase {
			continue // no basename entry to merge
		}
		pathKey := pathKeys[0]
		scores[pathKey] += scores[base]
		for _, s := range sources[base] {
			sources[pathKey] = appendUnique(sources[pathKey], s)
		}
		if reasons[pathKey] == "" {
			reasons[pathKey] = reasons[base]
		}
		delete(scores, base)
		delete(sources, base)
		delete(reasons, base)
	}

	// Build plan path set for filtering
	planPathSet := make(map[string]bool)
	for _, f := range p.FilesToModify {
		planPathSet[f] = true
	}
	for _, f := range p.FilesToCreate {
		planPathSet[f] = true
	}

	// Remove plan files and sort by score
	var ranked []types.RankedFile
	for file, score := range scores {
		if planSet[file] || planPathSet[file] {
			continue
		}
		src := strings.Join(sources[file], "+")
		displayFile := file
		filePath := paths[file]
		// If file key is a path (contains /), use basename for display
		if strings.Contains(file, "/") {
			filePath = file
			displayFile = filepath.Base(file)
		}
		ranked = append(ranked, types.RankedFile{
			File:   displayFile,
			Path:   filePath,
			Score:  math.Round(score*100) / 100,
			Source: src,
			Reason: reasons[file],
		})
	}
	sort.Slice(ranked, func(i, j int) bool {
		return ranked[i].Score > ranked[j].Score
	})

	// Confidence gate: require corroborating evidence from STRONG signals.
	// Weak signals (keyword-dir, dir-siblings) create false intersections.
	if len(ri.SpikeFiles) > 0 {
		type fileClass struct {
			idx          int
			inSpike      bool
			hasStructural bool
			isBuildCheck bool
		}
		var classes []fileClass
		intersectionCount := 0
		for i := range ranked {
			key := ranked[i].Path
			if key == "" {
				key = ranked[i].File
			}
			inSpike := ri.SpikeFiles[key] || ri.SpikeFiles[ranked[i].File]

			// Tiered structural evidence: only strong signals count for intersection
			hasStructural := hasStrongStructural(ranked[i].Source)
			isBuildCheck := strings.Contains(ranked[i].Reason, "build-check") ||
				strings.Contains(ranked[i].Source, "build-check")
			if (inSpike && hasStructural) || isBuildCheck {
				intersectionCount++
			}
			classes = append(classes, fileClass{i, inSpike, hasStructural, isBuildCheck})
		}

		// Second pass: gate based on confidence with per-tier K-caps
		var buildCheckFiles, intersectFiles, spikeOnlyFiles []types.RankedFile
		for _, c := range classes {
			if c.isBuildCheck {
				buildCheckFiles = append(buildCheckFiles, ranked[c.idx])
			} else if c.inSpike && c.hasStructural {
				intersectFiles = append(intersectFiles, ranked[c.idx])
			} else if c.inSpike && intersectionCount > 0 {
				ranked[c.idx].Score *= 0.60
				spikeOnlyFiles = append(spikeOnlyFiles, ranked[c.idx])
			}
		}

		// Per-tier K-caps: build-check unlimited, intersection K=3, spike-only K=2
		var gated []types.RankedFile
		gated = append(gated, buildCheckFiles...)
		if len(intersectFiles) > 3 {
			intersectFiles = intersectFiles[:3]
		}
		gated = append(gated, intersectFiles...)
		if len(spikeOnlyFiles) > 2 {
			spikeOnlyFiles = spikeOnlyFiles[:2]
		}
		gated = append(gated, spikeOnlyFiles...)
		ranked = gated
	}

	sort.Slice(ranked, func(i, j int) bool {
		return ranked[i].Score > ranked[j].Score
	})

	if len(ranked) > k {
		ranked = ranked[:k]
	}

	// No K-cap bypass for spike files — they live or die by score and rank
	// like everything else. Turn-based scoring already prioritizes core changes.

	return ranked
}

// hasStrongStructural checks if a file's source signals include strong
// structural evidence (actual code relationships, not proximity/keywords).
func hasStrongStructural(source string) bool {
	// Strong signals: verified call sites, direct callers, imports, cascade
	strong := []string{
		"verified-", "structural", "import-chain", "cascade",
		"callee", "comod", "shared-pkg", "sibling-cmd",
	}
	for _, s := range strong {
		if strings.Contains(source, s) {
			return true
		}
	}
	return false
	// Weak (not counted): keyword-dir, dir-sibling, parent, peer-dir,
	// convention, analogy, semantic, pattern, dir-comod
}

// splitFileTokens extracts lowercase tokens from a filename or text.
// "query_builder.go" → ["query", "builder"]
// "queries_pr.go" → ["queries", "query", "pr"]
func splitFileTokens(name string) []string {
	name = strings.TrimSuffix(name, ".go")
	name = strings.ToLower(name)
	// Split on underscores, hyphens, camelCase boundaries
	var tokens []string
	seen := make(map[string]bool)
	for _, part := range strings.FieldsFunc(name, func(r rune) bool {
		return r == '_' || r == '-' || r == '/' || r == '.' || r == ' '
	}) {
		if len(part) >= 2 && !seen[part] {
			seen[part] = true
			tokens = append(tokens, part)
			// Add singularized form
			s := singularize(part)
			if s != part && !seen[s] {
				seen[s] = true
				tokens = append(tokens, s)
			}
		}
	}
	return tokens
}

func appendUnique(ss []string, s string) []string {
	for _, existing := range ss {
		if existing == s {
			return ss
		}
	}
	return append(ss, s)
}
