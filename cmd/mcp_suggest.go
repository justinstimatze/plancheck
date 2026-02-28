package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/justinstimatze/plancheck/internal/comod"
	"github.com/justinstimatze/plancheck/internal/refgraph"
	"github.com/justinstimatze/plancheck/internal/simulate"
	"github.com/justinstimatze/plancheck/internal/types"
	"github.com/mark3labs/mcp-go/mcp"
)

// handleSuggest is the live navigation tool — called mid-implementation.
// No LLM calls. Pure structural + compiler signals. Instant response.
// The agent IS the spike; plancheck observes what it's touched and suggests
// what's missing.
func handleSuggest(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()

	cwd, ok := args["cwd"].(string)
	if !ok || cwd == "" {
		return mcp.NewToolResultError("cwd: required string argument"), nil
	}
	absCwd, _ := filepath.Abs(cwd)
	if info, err := os.Stat(absCwd); err != nil || !info.IsDir() {
		return mcp.NewToolResultError(fmt.Sprintf("cwd: %q is not a valid directory", cwd)), nil
	}

	// Require defn
	if _, err := os.Stat(filepath.Join(absCwd, ".defn")); err != nil {
		return mcp.NewToolResultError("plancheck requires defn. Run: defn init ."), nil
	}

	objective, _ := args["objective"].(string)

	// Parse filesTouched — files the agent has already modified
	var filesTouched []string
	if ftRaw, ok := args["files_touched"].(string); ok && ftRaw != "" {
		if err := json.Unmarshal([]byte(ftRaw), &filesTouched); err != nil {
			// Try comma-separated
			for _, f := range strings.Split(ftRaw, ",") {
				f = strings.TrimSpace(f)
				if f != "" {
					filesTouched = append(filesTouched, f)
				}
			}
		}
	}
	if len(filesTouched) == 0 {
		return mcp.NewToolResultError("files_touched: at least one file path required"), nil
	}

	// Build suggestions from multiple signal sources
	type suggestion struct {
		File       string  `json:"file"`
		Reason     string  `json:"reason"`
		Source     string  `json:"source"`
		Confidence float64 `json:"confidence"`
	}
	var suggestions []suggestion
	seen := make(map[string]bool)
	for _, f := range filesTouched {
		seen[f] = true
		seen[filepath.Base(f)] = true // also match bare filenames from graph
	}

	// 1. Graph callers/callees (requires defn)
	graph := refgraph.LoadGraph(absCwd)
	if graph != nil {
		for _, touchedFile := range filesTouched {
			base := filepath.Base(touchedFile)
			moduleID := refgraph.ResolveModuleIDFromGoMod(graph, absCwd, touchedFile)

			// Callers: files that call functions in the touched file
			callerFiles := graph.CallerFiles(base, moduleID)
			for file, count := range callerFiles {
				if seen[file] || strings.HasSuffix(file, "_test.go") {
					continue
				}
				fullPath := resolveGraphFile(file, graph, absCwd, moduleID)
				if fullPath == "" {
					continue
				}
				suggestions = append(suggestions, suggestion{
					File:       fullPath,
					Reason:     fmt.Sprintf("calls %d function(s) in %s", count, touchedFile),
					Source:     "graph-caller",
					Confidence: min(0.4+float64(count)*0.1, 0.8),
				})
			}

			// Callees: files called by the touched file (lower confidence)
			calleeFiles := graph.CalleeFiles(base, moduleID)
			for file, count := range calleeFiles {
				if seen[file] || strings.HasSuffix(file, "_test.go") {
					continue
				}
				fullPath := resolveGraphFile(file, graph, absCwd, moduleID)
				if fullPath == "" {
					continue
				}
				if count >= 3 { // only suggest heavily-used callees
					suggestions = append(suggestions, suggestion{
						File:       fullPath,
						Reason:     fmt.Sprintf("%s calls %d function(s) in this file", touchedFile, count),
						Source:     "graph-callee",
						Confidence: 0.3,
					})
				}
			}
		}
	}

	// 2. Co-modification patterns (requires git history)
	ep := types.ExecutionPlan{
		Objective:     objective,
		FilesToModify: filesTouched,
	}
	comodGaps := comod.CheckComod(ep, absCwd)
	for _, gap := range comodGaps {
		file := gap.ComodFile
		if seen[file] || strings.HasSuffix(file, "_test.go") {
			continue
		}
		confidence := gap.Frequency
		if confidence < 0.3 {
			continue
		}
		suggestions = append(suggestions, suggestion{
			File:       file,
			Reason:     fmt.Sprintf("co-changes with %s %.0f%% of the time", gap.PlanFile, gap.Frequency*100),
			Source:     "comod",
			Confidence: confidence,
		})
	}

	// 3. Build-check (if we can probe the touched files)
	// Read the current file contents and probe exported symbols
	probeBlocks := make(map[string]string)
	for _, touchedFile := range filesTouched {
		absFile := filepath.Join(absCwd, touchedFile)
		original, err := os.ReadFile(absFile)
		if err != nil {
			continue
		}
		probed := simulate.ProbeExportedSymbols(string(original))
		if probed != string(original) {
			probeBlocks[touchedFile] = probed
		}
	}
	if len(probeBlocks) > 0 {
		buildResult, err := simulate.RunBuildCheck(probeBlocks, absCwd)
		if err == nil && buildResult != nil {
			for _, bo := range buildResult.Obligations {
				if seen[bo.File] {
					continue
				}
				errSummary := bo.Errors[0]
				if len(bo.Errors) > 1 {
					errSummary = fmt.Sprintf("%s (+%d more)", bo.Errors[0], len(bo.Errors)-1)
				}
				suggestions = append(suggestions, suggestion{
					File:       bo.File,
					Reason:     fmt.Sprintf("compiler: %s", errSummary),
					Source:     "build-check",
					Confidence: 0.95,
				})
			}
		}
	}

	// Confidence gate: require 2+ independent signal sources for non-compiler suggestions.
	// Compiler-verified (build-check) passes unconditionally.
	// Single-source graph/comod suggestions are noise; intersections are signal.
	type fileSignals struct {
		sources    map[string]bool
		bestReason string
		bestConf   float64
	}
	fileMap := make(map[string]*fileSignals)
	for _, s := range suggestions {
		fs, ok := fileMap[s.File]
		if !ok {
			fs = &fileSignals{sources: make(map[string]bool)}
			fileMap[s.File] = fs
		}
		fs.sources[s.Source] = true
		if s.Confidence > fs.bestConf {
			fs.bestConf = s.Confidence
			fs.bestReason = s.Reason
		}
	}

	var ranked []suggestion
	for file, fs := range fileMap {
		if fs.sources["build-check"] {
			// Compiler-verified: always include
			ranked = append(ranked, suggestion{
				File: file, Reason: fs.bestReason,
				Source: joinSuggestSources(fs.sources), Confidence: fs.bestConf,
			})
			continue
		}
		if len(fs.sources) >= 2 {
			// 2+ signal intersection: include
			ranked = append(ranked, suggestion{
				File: file, Reason: fs.bestReason,
				Source: joinSuggestSources(fs.sources), Confidence: fs.bestConf,
			})
		}
	}
	sort.Slice(ranked, func(i, j int) bool {
		return ranked[i].Confidence > ranked[j].Confidence
	})

	// Cap at 10 suggestions
	if len(ranked) > 10 {
		ranked = ranked[:10]
	}

	// Build response
	if len(ranked) == 0 {
		return mcp.NewToolResultText("No additional files suggested. Your current file set looks complete."), nil
	}

	var b strings.Builder
	must := 0
	likely := 0
	for _, s := range ranked {
		if s.Confidence >= 0.90 {
			must++
		} else if s.Confidence >= 0.50 {
			likely++
		}
	}

	if must > 0 {
		b.WriteString(fmt.Sprintf("MUST CHANGE (%d files will not compile):\n", must))
		for _, s := range ranked {
			if s.Confidence >= 0.90 {
				b.WriteString(fmt.Sprintf("  %s — %s\n", s.File, s.Reason))
			}
		}
		b.WriteString("\n")
	}

	if likely > 0 {
		b.WriteString(fmt.Sprintf("LIKELY NEEDED (%d files):\n", likely))
		for _, s := range ranked {
			if s.Confidence >= 0.50 && s.Confidence < 0.90 {
				b.WriteString(fmt.Sprintf("  %s — %s [%s]\n", s.File, s.Reason, s.Source))
			}
		}
		b.WriteString("\n")
	}

	// Lower confidence — mention but don't push
	maybeCount := 0
	for _, s := range ranked {
		if s.Confidence < 0.50 {
			maybeCount++
		}
	}
	if maybeCount > 0 {
		b.WriteString(fmt.Sprintf("MAYBE (%d files, lower confidence):\n", maybeCount))
		for _, s := range ranked {
			if s.Confidence < 0.50 {
				b.WriteString(fmt.Sprintf("  %s — %s\n", s.File, s.Reason))
			}
		}
	}

	return mcp.NewToolResultText(b.String()), nil
}

// resolveGraphFile maps a defn bare filename to a relative path.
func resolveGraphFile(file string, graph *refgraph.Graph, cwd string, moduleID int64) string {
	// If it exists directly, use it
	if _, err := os.Stat(filepath.Join(cwd, file)); err == nil {
		return file
	}
	// Resolve via module path
	if moduleID != 0 {
		modPath := graph.ModulePath(moduleID)
		if modPath != "" {
			gomodData, err := os.ReadFile(filepath.Join(cwd, "go.mod"))
			if err == nil {
				for _, line := range strings.Split(string(gomodData), "\n") {
					line = strings.TrimSpace(line)
					if strings.HasPrefix(line, "module ") {
						modRoot := strings.TrimSpace(strings.TrimPrefix(line, "module"))
						dir := strings.TrimPrefix(modPath, modRoot)
						dir = strings.TrimPrefix(dir, "/")
						if dir != "" {
							relPath := dir + "/" + file
							if _, err := os.Stat(filepath.Join(cwd, relPath)); err == nil {
								return relPath
							}
						}
						break
					}
				}
			}
		}
	}
	return ""
}

func joinSuggestSources(sources map[string]bool) string {
	var parts []string
	for s := range sources {
		parts = append(parts, s)
	}
	sort.Strings(parts)
	return strings.Join(parts, "+")
}

func min(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
