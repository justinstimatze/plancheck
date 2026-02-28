package plan

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/justinstimatze/plancheck/internal/refgraph"
	"github.com/justinstimatze/plancheck/internal/simulate"
	"github.com/justinstimatze/plancheck/internal/types"
)

// runSimulation runs a lightweight behavior-change simulation on all filesToModify
// and returns signals summarizing the blast radius. Each file's exported definitions
// are simulated as behavior-changes (the conservative assumption when we don't know
// what specifically will change).
func runSimulation(p types.ExecutionPlan, cwd string, g *refgraph.Graph) ([]types.Signal, *types.SimulationSummary, *simulate.Result, []string) {
	if len(p.FilesToModify) == 0 || g == nil {
		return nil, nil, nil, nil
	}

	mutations := buildMutationsFromPlan(p, cwd)
	if len(mutations) == 0 {
		return nil, nil, nil, nil
	}
	if len(mutations) > 5 {
		mutations = mutations[:5]
	}

	result, err := simulate.Run(g, mutations)
	if err != nil {
		return nil, nil, nil, nil
	}

	var sigs []types.Signal
	var summary *types.SimulationSummary
	var cascadeFileChain []string

	found := 0
	for _, s := range result.Steps {
		if s.DefinitionFound {
			found++
		}
	}

	if found > 0 {
		// Keep simulation signal concise — one line with key numbers.
		// Keep simulation signal concise — verbose signals confuse agents.
		var highImpact []string
		for _, s := range result.Steps {
			if s.DefinitionFound && (s.ProductionCallers >= 5 || s.TestCoverage >= 20) {
				highImpact = append(highImpact, displayMutation(s.Mutation))
			}
		}

		simMsg := fmt.Sprintf("Blast radius: %d production callers, %d tests (confidence: %s)",
			result.Total.ProductionCallers, result.Total.TestCoverage, result.Total.Confidence)
		if len(highImpact) > 0 && len(highImpact) <= 3 {
			simMsg += fmt.Sprintf(". High-impact: %s", strings.Join(highImpact, ", "))
		} else if len(highImpact) > 3 {
			simMsg += fmt.Sprintf(". %d high-impact definitions", len(highImpact))
		}

		sigs = append(sigs, types.Signal{
			Probe:   "simulation",
			Message: simMsg,
		})

		summary = &types.SimulationSummary{
			ProductionCallers: result.Total.ProductionCallers,
			TestCoverage:      result.Total.TestCoverage,
			TestDensity:       result.Total.TestDensity,
			Confidence:        result.Total.Confidence,
			HighImpactDefs:    highImpact,
		}

		// Apply base rates if available
		br := simulate.LoadBaseRates()
		if br != nil {
			// Use the median blast radius from all steps
			maxCallers := 0
			for _, s := range result.Steps {
				if s.ProductionCallers > maxCallers {
					maxCallers = s.ProductionCallers
				}
			}
			recall, hitRate, source := br.Lookup(true, maxCallers, result.Total.TestDensity)
			if source != "" {
				summary.BaseRateRecall = recall
				summary.BaseRateHitRate = hitRate
				summary.BaseRateSource = source
			}
		}

		// Run cascade for high-blast-radius mutations (≥5 callers).
		// Cascade traces the full ripple chain: mutation → broken callers →
		// their callers → ... until convergence. This gives depth the
		// one-shot simulation misses.
		hasHighBlast := false
		for _, s := range result.Steps {
			if s.ProductionCallers >= 5 {
				hasHighBlast = true
				break
			}
		}
		if hasHighBlast {
			// Only cascade signature-change mutations (behavior changes rarely cascade)
			var cascadeMuts []simulate.Mutation
			for _, m := range mutations {
				if m.Type == simulate.SignatureChange {
					cascadeMuts = append(cascadeMuts, m)
				}
			}
			if len(cascadeMuts) == 0 {
				// No signature changes — promote highest-blast behavior change
				cascadeMuts = mutations[:1]
			}
			cascade := simulate.Cascade(g, cascadeMuts, 4)
			summary.CascadeDepth = cascade.MaxDepth
			summary.CascadeFiles = cascade.TotalFiles
			summary.CascadeBreaks = cascade.TotalBreaks
			summary.CascadeConverged = cascade.Converged

			if cascade.TotalFiles > 0 {
				convergeTxt := "converged"
				if !cascade.Converged {
					convergeTxt = "NOT converged — ripples still spreading"
				}
				sigs = append(sigs, types.Signal{
					Probe:   "cascade",
					Message: fmt.Sprintf("Cascade: %d files affected across %d depths (%d breakages, %s). Changes ripple beyond direct callers.", cascade.TotalFiles, cascade.MaxDepth, cascade.TotalBreaks, convergeTxt),
				})
				cascadeFileChain = cascade.FileChain
			}
		}
	}

	return sigs, summary, &result, cascadeFileChain
}

// buildMutationsFromPlan finds the highest-blast-radius exported definitions
// in the files the plan modifies. Uses source_file column for exact matching
// when available, falls back to module-path stem matching.
func buildMutationsFromPlan(p types.ExecutionPlan, cwd string) []simulate.Mutation {
	if !refgraph.Available(cwd) || len(p.FilesToModify) == 0 {
		return nil
	}

	// Try exact source_file matching first (requires re-ingested defn with source_file column)
	mutations := buildMutationsFromSourceFile(p, cwd)
	if len(mutations) > 0 {
		return mutations
	}

	// Fallback: module-path stem matching
	return buildMutationsFromModulePath(p, cwd)
}

// buildMutationsFromSourceFile uses defn's source_file column for exact matching.
func buildMutationsFromSourceFile(p types.ExecutionPlan, cwd string) []simulate.Mutation {
	// Build SQL IN clause from plan file basenames
	var fileNames []string
	for _, f := range p.FilesToModify {
		base := filepath.Base(f)
		if base == "" || !isGoFile(base) {
			continue
		}
		fileNames = append(fileNames, "'"+base+"'")
	}
	if len(fileNames) == 0 {
		return nil
	}

	inClause := "(" + strings.Join(fileNames, ", ") + ")"

	rows := refgraph.QueryDefn(cwd,
		"SELECT d.name, d.receiver, d.source_file, "+
			"(SELECT COUNT(*) FROM `references` r WHERE r.to_def = d.id) as callers "+
			"FROM definitions d "+
			"WHERE d.test = FALSE AND d.exported = TRUE AND d.source_file IN "+inClause+
			" ORDER BY callers DESC LIMIT 15")

	if len(rows) == 0 {
		return nil // source_file column might not be populated
	}

	seen := make(map[string]bool)
	var mutations []simulate.Mutation
	for _, row := range rows {
		name, _ := row["name"].(string)
		receiver, _ := row["receiver"].(string)
		callers := 0
		if c, ok := row["callers"].(float64); ok {
			callers = int(c)
		}
		if callers < 2 {
			continue
		}
		key := name + "|" + receiver
		if seen[key] {
			continue
		}
		seen[key] = true
		mutations = append(mutations, simulate.Mutation{
			Type:     simulate.BehaviorChange,
			Name:     name,
			Receiver: receiver,
		})
	}
	if len(mutations) > 5 {
		mutations = mutations[:5]
	}
	return mutations
}

// buildMutationsFromModulePath falls back to fuzzy module path matching.
func buildMutationsFromModulePath(p types.ExecutionPlan, cwd string) []simulate.Mutation {
	var likePatterns []string
	for _, f := range p.FilesToModify {
		base := filepath.Base(f)
		stem := base[:len(base)-len(filepath.Ext(base))]
		if stem == "" || stem == "main" || stem == "doc" {
			continue
		}
		likePatterns = append(likePatterns, stem)
	}
	if len(likePatterns) == 0 {
		return nil
	}

	var whereClauses []string
	for _, stem := range likePatterns {
		whereClauses = append(whereClauses, fmt.Sprintf("m.path LIKE '%%%s%%'", stem))
	}
	whereSQL := "(" + strings.Join(whereClauses, " OR ") + ")"

	rows := refgraph.QueryDefn(cwd,
		"SELECT d.name, d.receiver, "+
			"(SELECT COUNT(*) FROM `references` r WHERE r.to_def = d.id) as callers "+
			"FROM definitions d JOIN modules m ON d.module_id = m.id "+
			"WHERE d.test = FALSE AND d.exported = TRUE AND "+whereSQL+
			" ORDER BY callers DESC LIMIT 15")

	seen := make(map[string]bool)
	var mutations []simulate.Mutation
	for _, row := range rows {
		name, _ := row["name"].(string)
		receiver, _ := row["receiver"].(string)
		callers := 0
		if c, ok := row["callers"].(float64); ok {
			callers = int(c)
		}
		if callers < 2 {
			continue
		}
		key := name + "|" + receiver
		if seen[key] {
			continue
		}
		seen[key] = true
		mutations = append(mutations, simulate.Mutation{
			Type:     simulate.BehaviorChange,
			Name:     name,
			Receiver: receiver,
		})
	}
	if len(mutations) > 5 {
		mutations = mutations[:5]
	}
	return mutations
}

// scoreImporterCoupling scores importers by function-level coupling density
// using the defn reference graph. Importers with more cross-module references
// to plan file modules rank higher. Trims importers to top 10 after scoring.
// Non-importer entries (dir="import") pass through unchanged.
func scoreImporterCoupling(files []importChainFile, cwd string) []importChainFile {
	if !refgraph.Available(cwd) {
		// No refgraph: fall back to walk-order, cap at 10 importers
		var imports, importers []importChainFile
		for _, f := range files {
			if f.dir == "importer" {
				importers = append(importers, f)
			} else {
				imports = append(imports, f)
			}
		}
		if len(importers) > 10 {
			importers = importers[:10]
		}
		return append(imports, importers...)
	}

	var imports, importers []importChainFile
	for _, f := range files {
		if f.dir == "importer" {
			importers = append(importers, f)
		} else {
			imports = append(imports, f)
		}
	}

	// Score each importer by reference density: count distinct references
	// from the importer's source_file to any definition in plan files' source_files.
	planBases := make(map[string]bool)
	for _, f := range files {
		if f.dir != "importer" {
			continue
		}
		planBases[f.planFile] = true
	}
	// Also collect from imports to ensure we have all plan file basenames
	for _, f := range files {
		if f.dir == "import" {
			planBases[f.planFile] = true
		}
	}

	for i, imp := range importers {
		base := imp.file
		// Count references from this importer's definitions to plan file definitions
		var planFileList []string
		for pb := range planBases {
			planFileList = append(planFileList, "'"+pb+"'")
		}
		if len(planFileList) == 0 {
			continue
		}
		sql := fmt.Sprintf(
			"SELECT COUNT(DISTINCT r.id) as cnt "+
				"FROM definitions d "+
				"JOIN `references` r ON r.from_def = d.id "+
				"JOIN definitions target ON r.to_def = target.id "+
				"WHERE d.source_file = '%s' AND d.test = FALSE "+
				"AND target.source_file IN (%s)",
			base, strings.Join(planFileList, ","))
		rows := refgraph.QueryDefn(cwd, sql)
		if len(rows) > 0 {
			if cnt, ok := rows[0]["cnt"].(float64); ok {
				importers[i].refCount = int(cnt)
			}
		}
	}

	// Sort importers by refCount descending
	sort.Slice(importers, func(i, j int) bool {
		return importers[i].refCount > importers[j].refCount
	})

	// Keep top 10
	if len(importers) > 10 {
		importers = importers[:10]
	}

	return append(imports, importers...)
}
