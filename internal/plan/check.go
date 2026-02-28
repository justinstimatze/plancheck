// Package plan implements the core plan verification pipeline.
// Check() orchestrates all probes: file validation, co-modification analysis,
// reference graph queries, implementation spike, and confidence gating.
package plan

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/justinstimatze/plancheck/internal/comod"
	"github.com/justinstimatze/plancheck/internal/forecast"
	"github.com/justinstimatze/plancheck/internal/history"
	"github.com/justinstimatze/plancheck/internal/refgraph"
	"github.com/justinstimatze/plancheck/internal/signals"
	"github.com/justinstimatze/plancheck/internal/simulate"
	"github.com/justinstimatze/plancheck/internal/typeflow"
	"github.com/justinstimatze/plancheck/internal/types"
	"github.com/justinstimatze/plancheck/internal/walk"
)

// CheckOptions configures a plan check run with the plan and project root directory.
type CheckOptions struct {
	Plan types.ExecutionPlan
	Cwd  string
}

// Check runs all deterministic probes against a plan and returns findings.
func Check(opts CheckOptions) types.PlanCheckResult {
	cwd := opts.Cwd
	rawPlan := opts.Plan

	normalizePath := func(f string) string {
		// filepath.Join doesn't handle absolute f correctly — resolve manually
		var abs string
		if filepath.IsAbs(f) {
			abs = f
		} else {
			abs = filepath.Join(cwd, f)
		}
		rel, err := filepath.Rel(cwd, abs)
		if err != nil {
			return f
		}
		return filepath.ToSlash(rel)
	}

	p := rawPlan
	p.FilesToRead = filterEmpty(uniqueStrings(mapStrings(rawPlan.FilesToRead, normalizePath)))
	p.FilesToModify = filterEmpty(uniqueStrings(mapStrings(rawPlan.FilesToModify, normalizePath)))
	p.FilesToCreate = filterEmpty(uniqueStrings(mapStrings(rawPlan.FilesToCreate, normalizePath)))

	var suggestedModify []string
	var simSummary *types.SimulationSummary
	var lastSimResult *simulate.Result
	var cascadeFiles []string

	// Compute novelty FIRST — determines signal weighting
	novelty := forecast.AssessNovelty(p.FilesToModify, p.FilesToCreate, p.Steps)

	// ── File list validation ──────────────────────────────────────
	var validationSignals []types.Signal

	// Flag directories in file lists
	for _, list := range []struct {
		name  string
		files []string
	}{
		{"filesToRead", p.FilesToRead},
		{"filesToModify", p.FilesToModify},
		{"filesToCreate", p.FilesToCreate},
	} {
		for _, f := range list.files {
			absPath := filepath.Join(cwd, f)
			if info, err := os.Stat(absPath); err == nil && info.IsDir() {
				validationSignals = append(validationSignals, types.Signal{
					Probe:   "invalid-path",
					File:    f,
					Message: fmt.Sprintf("%s is a directory, not a file — remove or replace with specific file paths", f),
				})
			}
		}
	}

	// Flag existing files in filesToCreate
	for _, f := range p.FilesToCreate {
		absPath := filepath.Join(cwd, f)
		if info, err := os.Stat(absPath); err == nil && !info.IsDir() {
			validationSignals = append(validationSignals, types.Signal{
				Probe:   "create-exists",
				File:    f,
				Message: fmt.Sprintf("%s already exists on disk — did you mean filesToModify?", f),
			})
		}
	}

	// Flag cross-list overlap
	allLists := map[string][]string{
		"filesToRead":   p.FilesToRead,
		"filesToModify": p.FilesToModify,
		"filesToCreate": p.FilesToCreate,
	}
	type listPair struct{ a, b string }
	for _, pair := range []listPair{
		{"filesToModify", "filesToCreate"},
		{"filesToRead", "filesToCreate"},
	} {
		setA := make(map[string]bool, len(allLists[pair.a]))
		for _, f := range allLists[pair.a] {
			setA[f] = true
		}
		for _, f := range allLists[pair.b] {
			if setA[f] {
				validationSignals = append(validationSignals, types.Signal{
					Probe:   "list-overlap",
					File:    f,
					Message: fmt.Sprintf("%s appears in both %s and %s", f, pair.a, pair.b),
				})
			}
		}
	}

	projectPatterns := history.LoadPatterns(cwd)
	greenfield := isGreenfield(cwd, p.FilesToCreate)

	// ── Scored probes ────────────────────────────────────────────

	// File existence check — files listed in filesToModify/filesToRead must exist on disk
	creatingSet := make(map[string]bool, len(p.FilesToCreate))
	for _, f := range p.FilesToCreate {
		creatingSet[f] = true
	}
	var missingFiles []types.MissingFileResult
	for _, f := range p.FilesToModify {
		if creatingSet[f] {
			continue // will be created by this plan
		}
		absPath := filepath.Join(cwd, f)
		if _, err := os.Stat(absPath); os.IsNotExist(err) {
			missingFiles = append(missingFiles, types.MissingFileResult{
				File:       f,
				List:       "filesToModify",
				Suggestion: "Move to filesToCreate if this is a new file, or fix the path",
			})
		}
	}
	for _, f := range p.FilesToRead {
		if creatingSet[f] {
			continue
		}
		absPath := filepath.Join(cwd, f)
		if _, err := os.Stat(absPath); os.IsNotExist(err) {
			missingFiles = append(missingFiles, types.MissingFileResult{
				File:       f,
				List:       "filesToRead",
				Suggestion: "Verify the path is correct or remove from filesToRead",
			})
		}
	}

	// Co-modification analysis
	acknowledged := p.AcknowledgedComod
	if acknowledged == nil {
		acknowledged = map[string]string{}
	}
	var allComodGaps []comod.Gap
	if _, err := walk.GitRoot(cwd); err != nil {
		validationSignals = append(validationSignals, types.Signal{
			Probe:   "comod-skip",
			Message: "Co-modification analysis skipped: git not available",
		})
	} else {
		allComodGaps = comod.CheckComod(p, cwd)
	}

	var testCoverageGaps []string
	var structuralGaps []comod.Gap
	for _, g := range allComodGaps {
		if walk.IsTestFile(g.ComodFile) {
			testCoverageGaps = append(testCoverageGaps, g.ComodFile)
		} else {
			structuralGaps = append(structuralGaps, g)
		}
	}

	// Detect hubs
	comodFileCount := make(map[string]map[string]bool)
	for _, g := range structuralGaps {
		if comodFileCount[g.ComodFile] == nil {
			comodFileCount[g.ComodFile] = make(map[string]bool)
		}
		comodFileCount[g.ComodFile][g.PlanFile] = true
	}
	uniquePlanFiles := make(map[string]bool)
	for _, g := range structuralGaps {
		uniquePlanFiles[g.PlanFile] = true
	}

	const (
		hubMinPlanFiles   = 3
		hubFrequencyRatio = 0.6
	)

	hubFiles := make(map[string]bool)
	for comodFile, planFiles := range comodFileCount {
		count := len(planFiles)
		nUnique := len(uniquePlanFiles)
		if count >= hubMinPlanFiles || (nUnique > 0 && float64(count)/float64(nUnique) >= hubFrequencyRatio) {
			hubFiles[comodFile] = true
		}
	}

	var comodGaps []types.ComodGap
	for _, g := range structuralGaps {
		ackReason := acknowledged[g.ComodFile]
		if ackReason == "" {
			ackReason = acknowledged[filepath.Base(g.ComodFile)]
		}
		comodGaps = append(comodGaps, types.ComodGap{
			PlanFile:           g.PlanFile,
			ComodFile:          g.ComodFile,
			Frequency:          g.Frequency,
			Confidence:         types.ConfidenceLevel(g.Frequency),
			CrossStack:         isCrossStack(g.PlanFile, g.ComodFile),
			Acknowledged:       ackReason != "",
			AcknowledgedReason: ackReason,
			Hub:                hubFiles[g.ComodFile],
			Suggestion:         g.Suggestion,
		})
	}

	// Reference graph analysis (when defn is available)
	refGaps := refgraph.CheckBlastRadius(p, cwd)
	for _, rg := range refGaps {
		// Only add if not already covered by comod
		alreadyCovered := false
		for _, cg := range comodGaps {
			if cg.ComodFile == rg.ComodFile {
				alreadyCovered = true
				break
			}
		}
		if !alreadyCovered {
			rg.Suggestion = "[refgraph] " + rg.Suggestion
			comodGaps = append(comodGaps, rg)
		}
	}

	// Sort comod gaps: high confidence first, then by frequency descending
	sortComodGaps(comodGaps)

	// ── Signal-only probes (context for persona loop, no score impact) ──

	sigs := signals.Check(p, cwd)
	sigs = append(sigs, validationSignals...)

	// Hub explosion warning: when too many files are flagged as hubs,
	// the plan file is central infrastructure. Warn the LLM to scope
	// carefully instead of drowning it in 8+ equally-weighted suggestions.
	if len(hubFiles) >= 5 {
		hubList := make([]string, 0, len(hubFiles))
		for f := range hubFiles {
			hubList = append(hubList, filepath.Base(f))
		}
		sigs = append(sigs, types.Signal{
			Probe:   "hub-explosion",
			Message: fmt.Sprintf("Plan touches central infrastructure — %d files co-change with most plan files (%s). These are suppressed from suggestions. Focus on files specific to THIS task, not all possible co-changes.", len(hubFiles), strings.Join(hubList[:min(3, len(hubList))], ", ")),
		})
	}

	if refgraph.Available(cwd) {
		sigs = append(sigs, types.Signal{
			Probe:   "refgraph",
			Message: "defn reference graph available — blast radius analysis active",
		})

		// Run lightweight simulation on filesToModify
		var simSignals []types.Signal
		simGraph := refgraph.LoadGraph(cwd) // cached, same instance reused at line 479
		simSignals, simSummary, lastSimResult, cascadeFiles = runSimulation(p, cwd, simGraph)
		_ = lastSimResult // used for auto-save below
		sigs = append(sigs, simSignals...)
	}

	// Validate semantic suggestions against structural/comod signals
	if len(p.SemanticSuggestions) > 0 {
		for _, ss := range p.SemanticSuggestions {
			base := filepath.Base(ss.File)
			hasStructural := false
			hasComod := false

			// Check if structurally connected
			if refgraph.Available(cwd) {
				rows := refgraph.QueryDefn(cwd,
					"SELECT COUNT(*) as n FROM definitions d "+
						"JOIN `references` r ON r.from_def = d.id "+
						"JOIN definitions target ON r.to_def = target.id "+
						"WHERE d.source_file = '"+base+"' AND d.test = FALSE "+
						"AND target.source_file IN ("+planFileSQL(p)+") LIMIT 1")
				if len(rows) > 0 {
					n := 0
					if v, ok := rows[0]["n"].(float64); ok {
						n = int(v)
					}
					hasStructural = n > 0
				}
			}

			// Check if in comod gaps
			for _, cg := range comodGaps {
				if filepath.Base(cg.ComodFile) == base {
					hasComod = true
					break
				}
			}

			confidence := "consider"
			if hasStructural && hasComod {
				confidence = "must"
			} else if hasStructural || hasComod {
				confidence = "likely"
			}

			signals := ""
			if hasStructural {
				signals += "structural"
			}
			if hasComod {
				if signals != "" {
					signals += "+"
				}
				signals += "comod"
			}
			if signals == "" {
				signals = "semantic-only"
			}

			sigs = append(sigs, types.Signal{
				Probe: "semantic-validation",
				File:  ss.File,
				Message: fmt.Sprintf("[%s] %s: %s (model confidence: %.0f%%, validation: %s)",
					confidence, ss.File, ss.Reason, ss.Confidence*100, signals),
			})
		}
	}

	// Test coverage gaps → signal
	for _, f := range testCoverageGaps {
		sigs = append(sigs, types.Signal{
			Probe:   "test-coverage",
			File:    f,
			Message: fmt.Sprintf("%s co-modifies with plan files historically but no test files are in the plan", f),
		})
	}

	// Build critique (for drill-down via get_check_details)
	// High-confidence gaps use stronger language than moderate ones
	var critique []string
	for _, gap := range comodGaps {
		if gap.Acknowledged {
			critique = append(critique, fmt.Sprintf(`Co-mod (acknowledged): %s co-changes with %s (%d%%) — acknowledged: "%s".`,
				gap.ComodFile, gap.PlanFile, freqPct(gap.Frequency), gap.AcknowledgedReason))
		} else if gap.Hub {
			critique = append(critique, fmt.Sprintf("Co-mod (hub): %s co-changes with %s (%d%%) — hub file, co-changes with many plan files.",
				gap.ComodFile, gap.PlanFile, freqPct(gap.Frequency)))
		} else {
			label := confidenceLabel(gap.Confidence, gap.CrossStack)
			verb := confidenceVerb(gap.Confidence)
			critique = append(critique, fmt.Sprintf("Co-mod (%s): %s %s changes with %s (%d%%) but isn't in filesToModify.",
				label, gap.ComodFile, verb, gap.PlanFile, freqPct(gap.Frequency)))
			suggestedModify = append(suggestedModify, gap.ComodFile)
		}
	}

	projectType := "brownfield"
	if greenfield {
		projectType = "greenfield"
	}

	for _, mf := range missingFiles {
		critique = append(critique, fmt.Sprintf("Missing file: %s is in %s but does not exist on disk. %s.", mf.File, mf.List, mf.Suggestion))
	}

	// Verify invariants (must be before result construction to capture in sigs)
	if len(p.Invariants) > 0 && refgraph.Available(cwd) {
		verifiedInvariants := verifyInvariants(p, cwd)
		for _, inv := range verifiedInvariants {
			probe := "invariant"
			if !inv.Verified {
				probe = "invariant-risk"
			}
			sigs = append(sigs, types.Signal{
				Probe:   probe,
				Message: fmt.Sprintf("[%s] %s — %s", inv.Kind, inv.Claim, inv.Evidence),
			})
		}
	}

	result := types.PlanCheckResult{
		ProjectType: projectType,
		PlanStats: types.PlanStats{
			Steps:         len(p.Steps),
			FilesToModify: len(p.FilesToModify),
			FilesToCreate: len(p.FilesToCreate),
			FilesToRead:   len(p.FilesToRead),
		},
		MissingFiles:    missingFiles,
		ComodGaps:       comodGaps,
		Simulation:      simSummary,
		Signals:         sigs,
		ProjectPatterns: projectPatterns,
		Critique:        critique,
		SuggestedAdditions: types.SuggestedAdditions{
			FilesToModify: uniqueStrings(suggestedModify),
		},
	}

	result.EnsureNonNil()

	// Run addition pipeline for new packages (analogies + backward scout)
	var additionSigs []types.Signal
	var additionRanked []types.RankedFile
	if len(p.FilesToCreate) > 0 {
		additionSigs, additionRanked = additionPipeline(p.FilesToCreate, p.Steps, cwd)
		result.Signals = append(result.Signals, additionSigs...)

		// Convention-based creation suggestions: "you're creating pin.go,
		// convention says you probably also need http.go"
		creationSigs := suggestCreationPeers(p.FilesToCreate, cwd)
		result.Signals = append(result.Signals, creationSigs...)
	}

	// Keyword→directory matching: tokenize objective+steps,
	// flag directories the task mentions but the plan doesn't cover.
	allPlanFiles := append(append([]string{}, p.FilesToModify...), p.FilesToCreate...)
	// Build acknowledged domain gap set for filtering
	ackDomainGaps := make(map[string]bool)
	for _, d := range p.AcknowledgedDomainGaps {
		ackDomainGaps[d] = true
		ackDomainGaps[strings.TrimSuffix(d, "/")] = true
		ackDomainGaps[d+"/"] = true
	}

	kwGaps, kwRanked := matchKeywordsToDirs(p.Objective, p.Steps, allPlanFiles, cwd)
	for _, gap := range kwGaps {
		if ackDomainGaps[gap.Dir] {
			continue // acknowledged by plan author
		}
		result.Signals = append(result.Signals, types.Signal{
			Probe:   "keyword-dir",
			Message: fmt.Sprintf("plan covers %s/ but NOT %s/ — task requires both (add %s to acknowledgedDomainGaps to suppress)", gap.CoveredDir, gap.Dir, gap.Dir),
		})
	}

	// Compute ancestor scope (peer directories under feature root)
	peerFiles, scopeSignal := findAncestorScope(p, cwd)
	if scopeSignal != "" {
		result.Signals = append(result.Signals, types.Signal{
			Probe:   "scope",
			Message: scopeSignal,
		})
	}

	// Import chain analysis: Go import graph connections
	importFiles := findImportChainFiles(p.FilesToModify, cwd)

	// Score importers by function-level coupling density, keep top 10
	importFiles = scoreImporterCoupling(importFiles, cwd)

	// Directory-level comod: catches cross-layer coupling
	// (e.g., pkg/cmd/pr/* → api/queries_pr.go)
	dirGaps := comod.CheckDirComod(p, cwd)

	// Convention peer detection: directory co-occurrence patterns
	conventionPeers := findConventionPeers(allPlanFiles, cwd)

	// Shared-pkg peers: pkg/cmd/X/shared/ files for plan files in pkg/cmd/X/Y/
	conventionPeers = append(conventionPeers, findSharedPkgPeers(allPlanFiles, cwd)...)

	// Sibling command peers: pkg/cmd/X/Z/ when plan has pkg/cmd/X/Y/ and task names Z
	conventionPeers = append(conventionPeers, findSiblingCmdPeers(allPlanFiles, p.Objective, p.Steps, cwd)...)

	// Cmdutil peers: pkg/cmdutil/ files gated by keyword triggers
	conventionPeers = append(conventionPeers, findCmdutilPeers(allPlanFiles, p.Objective, p.Steps, cwd)...)

	// Dynamic source analysis: discover files with verified call sites
	// to plan file functions. Uses defn for caller discovery, go/parser
	// for source-level verification. Highest-precision signal.
	// Load in-memory graph for fast queries (cached per cwd)
	graph := refgraph.LoadGraph(cwd)

	var verifiedFiles []typeflow.DiscoveredFile
	if graph != nil {
		verifiedFiles = typeflow.DiscoverVerifiedFiles(
			p.FilesToModify, cwd, refgraph.QueryDefn, graph)

		// Build domain hints from keyword-dir gaps (computed above)
		var domainHints []string
		for _, gap := range kwGaps {
			domainHints = append(domainHints,
				fmt.Sprintf("Explore %s/ — task mentions this domain but plan only covers %s/", gap.Dir, gap.CoveredDir))
		}

		// Extract production directories from test patch file paths.
		// Test files at pkg/cmd/X/Y/Z_test.go → production dir pkg/cmd/X/Y/
		// These are high-signal: tests directly reference the packages that change.
		if p.TestPatch != "" {
			testDirs := extractTestPatchDirs(p.TestPatch, allPlanFiles)
			for _, td := range testDirs {
				domainHints = append(domainHints,
					fmt.Sprintf("Tests modify %s/ — check for production files here", td))
			}
		}

		// Implementation spike: ask LLM which files this task needs.
		spikeResult, spikeErr := simulate.RunSpike(cwd, graph, p.FilesToModify, p.Objective, p.Steps, 1, domainHints)
		if spikeErr != nil {
			result.Signals = append(result.Signals, types.Signal{
				Probe:   "spike",
				Message: fmt.Sprintf("Spike error: %v", spikeErr),
			})
		}
		if spikeResult != nil && len(spikeResult.Predictions) > 0 {
			for _, pred := range spikeResult.Predictions {
				verifiedFiles = append(verifiedFiles, typeflow.DiscoveredFile{
					Path:      pred.File,
					File:      filepath.Base(pred.File),
					Direction: "spike",
					Score:     pred.Score,
					Reason:    pred.Reason,
				})
			}
			result.Signals = append(result.Signals, types.Signal{
				Probe:   "spike",
				Message: fmt.Sprintf("Spike discovered %d additional files", len(spikeResult.Predictions)),
			})
		}

		// Plan file blast radius: probe ALL exported symbols in plan files.
		// Context only — NOT a ranking signal. Shows the dependency cone.
		blastResult, _ := simulate.RunBlastRadius(p.FilesToModify, cwd)
		if blastResult != nil && len(blastResult.DependentFiles) > 0 {
			result.Signals = append(result.Signals, types.Signal{
				Probe:   "blast-radius",
				Message: fmt.Sprintf("Plan files have %d dependent files (probed %d plan files)", len(blastResult.DependentFiles), blastResult.ProbedFiles),
			})
		}

		// Build implementation preview from spike's code + obligations + graph
		if spikeResult != nil {
			result.Preview = buildImplementationPreview(spikeResult, graph, cwd)
			if spikeResult.Cost.APICallCount > 0 {
				cost := spikeResult.Cost
				result.Cost = &cost
			}
		}

		// Merge blast radius into preview as risk context
		if blastResult != nil && len(blastResult.DependentFiles) > 0 {
			if result.Preview == nil {
				result.Preview = &types.ImplementationPreview{}
			}
			planSet := make(map[string]bool)
			for _, f := range p.FilesToModify {
				planSet[f] = true
			}
			spikeSet := make(map[string]bool)
			if spikeResult != nil {
				for f := range spikeResult.FileBlocks {
					spikeSet[f] = true
				}
			}
			var unseenDeps []string
			for _, dep := range blastResult.DependentFiles {
				if !planSet[dep] && !spikeSet[dep] {
					unseenDeps = append(unseenDeps, dep)
				}
			}
			if len(unseenDeps) > 0 {
				risk := fmt.Sprintf("Blast radius: %d additional files depend on plan files' exported APIs", len(unseenDeps))
				shown := 5
				if len(unseenDeps) < shown {
					shown = len(unseenDeps)
				}
				for i := 0; i < shown; i++ {
					risk += fmt.Sprintf("\n    - %s", unseenDeps[i])
				}
				result.Preview.Risks = append(result.Preview.Risks, risk)
			}
		}

		// Verified cascade: trace ripples through source-verified call sites.
		// Uses inferred mutations to focus on specific functions, not all exports.
		if graph != nil {
			inferred := typeflow.InferMutations(p.FilesToModify, p.Objective, p.Steps, cwd)
			startFuncs := typeflow.InferredFuncNames(inferred)
			cascadeResult := simulate.CascadeVerified(cwd, graph, p.FilesToModify, 2, startFuncs)
			// Merge cascade-discovered files (avoid duplicates)
			seen := make(map[string]bool)
			for _, vf := range verifiedFiles {
				seen[vf.Path] = true
			}
			for _, cvf := range cascadeResult.VerifiedFiles {
				if !seen[cvf.Path] {
					verifiedFiles = append(verifiedFiles, cvf)
					seen[cvf.Path] = true
				}
			}
			if cascadeResult.TotalFiles > 0 {
				result.Signals = append(result.Signals, types.Signal{
					Probe: "verified-cascade",
					Message: fmt.Sprintf("Source-verified cascade: %d files across %d depths (converged: %v)",
						cascadeResult.TotalFiles, cascadeResult.MaxDepth, cascadeResult.Converged),
				})
			}
		}
	}

	// Test-driven backward planning: disabled — test patches reference many
	// production functions that DON'T need changes (called but not modified).
	// Need smarter filtering: only include functions whose signatures/behavior
	// change, not every function the test calls.
	// Disabled: test-backward signals were too noisy (WHAT_DIDNT_WORK.md).
	// Test patches now used for directory hints instead (extractTestPatchDirs).

	// Cross-cutting detection: tasks that touch many files in a pattern
	// (e.g., "add headers to all tables") are better handled by the LLM alone.
	// Our structural signals suggest wrong files from the pattern, causing regressions.
	isCrossCutting := len(p.FilesToModify) > 5
	if !isCrossCutting {
		lcObj := strings.ToLower(p.Objective)
		for _, kw := range []string{"all ", "every ", "consistent", "each command", "each table"} {
			if strings.Contains(lcObj, kw) {
				isCrossCutting = true
				break
			}
		}
	}

	// Tight K: precision over quantity.
	k := 5
	if isCrossCutting {
		k = 0 // suppress suggestions for cross-cutting tasks
	}

	// Rank candidate files by combined score, weighted by novelty
	result.SuggestedAdditions.Ranked = rankCandidateFiles(rankingInput{
		Plan:            p,
		ComodGaps:       comodGaps,
		DirGaps:         dirGaps,
		AnalogyRanked:   additionRanked,
		KeywordRanked:   filterAckedKeywordFiles(kwRanked, ackDomainGaps),
		CascadeFiles:    cascadeFiles,
		PeerFiles:       peerFiles,
		ImportFiles:     importFiles,
		ConventionPeers: conventionPeers,
		VerifiedFiles:   verifiedFiles,
		SpikeFiles:      buildSpikeFileSet(verifiedFiles),
		Graph:           graph,
		NoveltyScore:    novelty.Score,
		Cwd:             cwd,
		K:               k,
	})

	// Source-level verification: check ranked suggestions for concrete call sites
	// to plan file functions. Boosts verified files, penalizes unverified structural.
	if len(result.SuggestedAdditions.Ranked) > 0 && len(p.FilesToModify) > 0 {
		verified := typeflow.VerifyRankedFiles(p.FilesToModify, result.SuggestedAdditions.Ranked, cwd)
		result.SuggestedAdditions.Ranked = typeflow.ApplyVerification(result.SuggestedAdditions.Ranked, verified)
		// Re-sort after verification adjustments
		sort.Slice(result.SuggestedAdditions.Ranked, func(i, j int) bool {
			return result.SuggestedAdditions.Ranked[i].Score > result.SuggestedAdditions.Ranked[j].Score
		})
	}

	result.Novelty = &types.NoveltySummary{
		Score:       novelty.Score,
		Label:       novelty.Label,
		Uncertainty: novelty.Uncertainty,
		Guidance:    novelty.Guidance,
	}

	// MC forecast: probability distribution of outcomes based on similar plans
	blastRadius := 0
	if result.Simulation != nil {
		blastRadius = result.Simulation.ProductionCallers
	}
	testDensity := 0.0
	if result.Simulation != nil {
		testDensity = result.Simulation.TestDensity
	}
	fcHistory := forecast.BuildHistory(cwd)
	if len(fcHistory) >= 3 {
		complexity := len(p.Steps)
		if len(p.FilesToModify)+len(p.FilesToCreate) > complexity {
			complexity = len(p.FilesToModify) + len(p.FilesToCreate)
		}
		cascadeDepth := 0
		cascadeFileCount := 0
		if result.Simulation != nil {
			cascadeDepth = result.Simulation.CascadeDepth
			cascadeFileCount = result.Simulation.CascadeFiles
		}
		fc := forecast.Run(forecast.PlanProperties{
			Complexity:   complexity,
			TestDensity:  testDensity,
			BlastRadius:  blastRadius,
			FileCount:    len(p.FilesToModify),
			CascadeDepth: cascadeDepth,
			CascadeFiles: cascadeFileCount,
		}, fcHistory, 10000)
		result.Forecast = &types.ForecastSummary{
			PClean:    fc.PClean,
			PRework:   fc.PRework,
			PFailed:   fc.PFailed,
			RecallP50: fc.RecallP50,
			RecallP85: fc.RecallP85,
			BasedOn:   fc.MatchingHistorical,
			Summary:   fc.Summary,
		}
	}
	result.HistoryID = history.AppendHistory(p, result, cwd)

	// Auto-save simulation for later replay via validate_execution
	if lastSimResult != nil {
		_ = simulate.SaveSimulationPrediction(cwd, *lastSimResult)
	}

	return result
}

func mapStrings(ss []string, fn func(string) string) []string {
	result := make([]string, len(ss))
	for i, s := range ss {
		result[i] = fn(s)
	}
	return result
}

func freqPct(f float64) int {
	return int(math.Round(f * 100))
}

func filterEmpty(ss []string) []string {
	var result []string
	for _, s := range ss {
		if s != "" && s != "." {
			result = append(result, s)
		}
	}
	return result
}

// filterAckedKeywordFiles removes keyword-dir ranked files whose directory
// has been acknowledged by the plan author.
func filterAckedKeywordFiles(files []types.RankedFile, acked map[string]bool) []types.RankedFile {
	if len(acked) == 0 {
		return files
	}
	var kept []types.RankedFile
	for _, f := range files {
		dir := filepath.Dir(f.Path)
		if dir == "" {
			dir = filepath.Dir(f.File)
		}
		if acked[dir] || acked[filepath.ToSlash(dir)] {
			continue
		}
		kept = append(kept, f)
	}
	return kept
}

func uniqueStrings(ss []string) []string {
	seen := make(map[string]bool)
	var result []string
	for _, s := range ss {
		if !seen[s] {
			seen[s] = true
			result = append(result, s)
		}
	}
	return result
}

// verifyInvariants checks each invariant against the reference graph.
func verifyInvariants(p types.ExecutionPlan, cwd string) []types.Invariant {
	var results []types.Invariant

	for _, inv := range p.Invariants {
		verified := inv
		switch inv.Kind {
		case "tests":
			// "all tests pass" — count tests covering modified definitions
			total := 0
			for _, f := range p.FilesToModify {
				base := filepath.Base(f)
				rows := refgraph.QueryDefn(cwd,
					"SELECT COUNT(DISTINCT tc.id) as n FROM definitions d "+
						"JOIN `references` r ON r.to_def = d.id "+
						"JOIN definitions tc ON r.from_def = tc.id "+
						"WHERE d.source_file = '"+base+"' AND d.test = FALSE "+
						"AND tc.test = TRUE")
				if len(rows) > 0 {
					if v, ok := rows[0]["n"].(float64); ok {
						total += int(v)
					}
				}
			}
			if total > 0 {
				verified.Verified = true
				verified.Evidence = fmt.Sprintf("%d tests cover modified definitions — run these to verify", total)
			} else {
				verified.Verified = false
				verified.Evidence = "no tests found covering modified definitions — invariant at risk"
			}

		case "api-compat":
			// "API backward compatible" — check for external callers
			externalCallers := 0
			for _, f := range p.FilesToModify {
				base := filepath.Base(f)
				rows := refgraph.QueryDefn(cwd,
					"SELECT COUNT(DISTINCT d.id) as n FROM definitions d "+
						"JOIN `references` r ON r.from_def = d.id "+
						"JOIN definitions target ON r.to_def = target.id "+
						"WHERE target.source_file = '"+base+"' AND target.exported = TRUE "+
						"AND d.source_file != '"+base+"' AND d.test = FALSE")
				if len(rows) > 0 {
					if v, ok := rows[0]["n"].(float64); ok {
						externalCallers += int(v)
					}
				}
			}
			if externalCallers > 0 {
				verified.Verified = false
				verified.Evidence = fmt.Sprintf("%d external callers of exported definitions — signature changes break API compat", externalCallers)
			} else {
				verified.Verified = true
				verified.Evidence = "no external callers of modified exports — API compat maintained"
			}

		case "no-new-deps":
			// "no new dependencies" — check filesToCreate
			if len(p.FilesToCreate) > 0 {
				verified.Verified = false
				verified.Evidence = fmt.Sprintf("plan creates %d new files — verify no new external dependencies", len(p.FilesToCreate))
			} else {
				verified.Verified = true
				verified.Evidence = "no new files created"
			}

		default:
			// Custom invariant — can't verify automatically
			verified.Evidence = "custom invariant — verify manually"
		}

		results = append(results, verified)
	}

	return results
}

func planFileSQL(p types.ExecutionPlan) string {
	var parts []string
	for _, f := range p.FilesToModify {
		parts = append(parts, "'"+filepath.Base(f)+"'")
	}
	for _, f := range p.FilesToCreate {
		parts = append(parts, "'"+filepath.Base(f)+"'")
	}
	if len(parts) == 0 {
		return "''"
	}
	return strings.Join(parts, ",")
}

// additionPipeline runs cross-project analogies and backward scout
// for new packages being created. Returns signals and ranked files
// that the main ranking function can incorporate.
func additionPipeline(filesToCreate, steps []string, cwd string) ([]types.Signal, []types.RankedFile) {
	var sigs []types.Signal
	var ranked []types.RankedFile

	// 1. Detect genre and get repos to search
	genres := forecast.DetectGenre(cwd)
	var genreRepos []string
	for _, g := range genres {
		genreRepos = append(genreRepos, g.Repos...)
	}

	// 2. Extract keywords from steps + file paths
	keywords := forecast.ExtractKeywords(steps)
	for _, f := range filesToCreate {
		// "internal/ratelimit/limiter.go" → "ratelimit", "limiter"
		parts := strings.Split(filepath.Dir(f), "/")
		for _, p := range parts {
			if p != "" && p != "." && p != "internal" && p != "cmd" && p != "pkg" {
				keywords = append(keywords, p)
			}
		}
		stem := filepath.Base(f)
		stem = strings.TrimSuffix(stem, ".go")
		if stem != "" && stem != "main" {
			keywords = append(keywords, stem)
		}
	}

	// Deduplicate keywords and prefer compound terms
	seen := make(map[string]bool)
	var uniqueKW []string

	// First: add compound path-derived terms (e.g., "ratelimit" from "internal/ratelimit/")
	// These are more specific than individual words
	for _, f := range filesToCreate {
		dir := filepath.Dir(f)
		compound := filepath.Base(dir)
		if compound != "" && compound != "." && compound != "internal" && compound != "cmd" && len(compound) > 4 {
			if !seen[compound] {
				seen[compound] = true
				uniqueKW = append(uniqueKW, compound)
			}
		}
	}

	// Then: add individual keywords, but skip very short/generic ones
	for _, kw := range keywords {
		if !seen[kw] && len(kw) > 3 {
			seen[kw] = true
			uniqueKW = append(uniqueKW, kw)
		}
	}

	// 3. Search analogies (genre-filtered if genres found, all repos otherwise)
	for _, kw := range uniqueKW {
		result := forecast.FindAnalogiesInRepos(kw, genreRepos)
		if result.Repos > 0 {
			patternPreview := result.Pattern
			if len(patternPreview) > 3 {
				patternPreview = patternPreview[:3]
			}
			sigs = append(sigs, types.Signal{
				Probe:   "analogy",
				Message: fmt.Sprintf("%s: found in %d repos. Common pattern: %v", kw, result.Repos, patternPreview),
			})
			for _, p := range result.Pattern {
				ranked = append(ranked, types.RankedFile{
					File:   p,
					Score:  0.4,
					Source: "analogy",
				})
			}
		}
	}

	// 4. Backward scout for each new package directory
	// Prerequisites flow into ranked suggestions so the model sees them
	newDirs := make(map[string]bool)
	for _, f := range filesToCreate {
		dir := filepath.Dir(f)
		if dir != "." && dir != "" {
			newDirs[dir] = true
		}
	}
	for dir := range newDirs {
		pkgName := filepath.Base(dir)
		scout, err := simulate.BackwardScout(cwd, pkgName, "")
		if err == nil && len(scout.Prerequisites) > 0 {
			sigs = append(sigs, types.Signal{
				Probe:   "backward-scout",
				Message: fmt.Sprintf("New package %s: %d prerequisites (pattern: %s)", pkgName, len(scout.Prerequisites), scout.PatternSource),
			})
			// Prerequisites become ranked suggestions — the model should consider these
			for _, prereq := range scout.Prerequisites {
				if prereq.Kind == "modify" {
					ranked = append(ranked, types.RankedFile{
						File:   prereq.Name,
						Score:  0.35,
						Source: "backward-scout",
					})
				}
			}
		}
	}

	return sigs, ranked
}

func isGoFile(name string) bool {
	return len(name) > 3 && name[len(name)-3:] == ".go"
}

func displayMutation(m simulate.Mutation) string {
	if m.Receiver != "" {
		return fmt.Sprintf("(%s).%s", m.Receiver, m.Name)
	}
	return m.Name
}

// sortComodGaps sorts gaps by confidence (high first), then by frequency descending.
func sortComodGaps(gaps []types.ComodGap) {
	sort.Slice(gaps, func(i, j int) bool {
		if gaps[i].Confidence != gaps[j].Confidence {
			return gaps[i].Confidence == "high"
		}
		return gaps[i].Frequency > gaps[j].Frequency
	})
}

// confidenceLabel returns the critique tag for a comod gap.
func confidenceLabel(confidence string, crossStack bool) string {
	layer := "same-layer"
	if crossStack {
		layer = "cross-stack"
	}
	if confidence == "high" {
		return "high-confidence " + layer
	}
	return layer
}

// confidenceVerb returns language proportional to confidence.
func confidenceVerb(confidence string) string {
	if confidence == "high" {
		return "almost always"
	}
	return "frequently"
}

