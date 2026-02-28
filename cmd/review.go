package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/justinstimatze/plancheck/internal/comod"
	"github.com/justinstimatze/plancheck/internal/refgraph"
	"github.com/justinstimatze/plancheck/internal/simulate"
	"github.com/justinstimatze/plancheck/internal/types"
)

type ReviewCmd struct {
	BaseRef string `arg:"" optional:"" help:"Git ref to compare against (default: HEAD for uncommitted, HEAD~1 for committed)."`
	Cwd     string `help:"Project root to analyze." default:"."`
}

func (c *ReviewCmd) Run() error {
	absCwd, _ := filepath.Abs(c.Cwd)

	// Require defn
	defnPath := filepath.Join(absCwd, ".defn")
	if _, err := os.Stat(defnPath); err != nil {
		fmt.Fprintf(os.Stderr, "plancheck: .defn/ not found in %s\n", absCwd)
		fmt.Fprintf(os.Stderr, "Run: defn init .\n")
		os.Exit(1)
	}

	// Determine which files have changed
	modifiedFiles, baseRef := getModifiedGoFiles(absCwd, c.BaseRef)
	if len(modifiedFiles) == 0 {
		fmt.Println("\x1b[32m✓ No Go files modified.\x1b[0m")
		return nil
	}

	fmt.Printf("Reviewing %d modified Go file(s) vs %s:\n", len(modifiedFiles), baseRef)
	for _, f := range modifiedFiles {
		fmt.Printf("  %s\n", f)
	}
	fmt.Println()

	// Build the file set for seen-checking
	seen := make(map[string]bool)
	for _, f := range modifiedFiles {
		seen[f] = true
		seen[filepath.Base(f)] = true
	}

	type suggestion struct {
		file       string
		reason     string
		source     string
		confidence float64
	}
	var suggestions []suggestion

	// 1. Graph callers/callees
	graph := refgraph.LoadGraph(absCwd)
	if graph != nil {
		for _, touchedFile := range modifiedFiles {
			base := filepath.Base(touchedFile)
			moduleID := refgraph.ResolveModuleIDFromGoMod(graph, absCwd, touchedFile)

			callerFiles := graph.CallerFiles(base, moduleID)
			for file, count := range callerFiles {
				if seen[file] || strings.HasSuffix(file, "_test.go") {
					continue
				}
				fullPath := resolveGraphFileForReview(file, graph, absCwd, moduleID)
				if fullPath == "" || seen[fullPath] {
					continue
				}
				suggestions = append(suggestions, suggestion{
					file:       fullPath,
					reason:     fmt.Sprintf("calls %d function(s) in %s", count, touchedFile),
					source:     "graph",
					confidence: minf(0.4+float64(count)*0.1, 0.8),
				})
			}
		}
	}

	// 2. Co-modification
	ep := types.ExecutionPlan{FilesToModify: modifiedFiles}
	comodGaps := comod.CheckComod(ep, absCwd)
	for _, gap := range comodGaps {
		file := gap.ComodFile
		if seen[file] || seen[filepath.Base(file)] || strings.HasSuffix(file, "_test.go") {
			continue
		}
		if gap.Frequency < 0.3 {
			continue
		}
		// Cap comod confidence at 0.80 — only compiler should reach MUST CHANGE tier
		conf := gap.Frequency
		if conf > 0.80 {
			conf = 0.80
		}
		suggestions = append(suggestions, suggestion{
			file:       file,
			reason:     fmt.Sprintf("co-changes with %s %.0f%% of the time", gap.PlanFile, gap.Frequency*100),
			source:     "comod",
			confidence: conf,
		})
	}

	// 3. Build-check (probe exported symbols)
	probeBlocks := make(map[string]string)
	for _, f := range modifiedFiles {
		absFile := filepath.Join(absCwd, f)
		original, err := os.ReadFile(absFile)
		if err != nil {
			continue
		}
		probed := simulate.ProbeExportedSymbols(string(original))
		if probed != string(original) {
			probeBlocks[f] = probed
		}
	}
	if len(probeBlocks) > 0 {
		buildResult, err := simulate.RunBuildCheck(probeBlocks, absCwd)
		if err == nil && buildResult != nil {
			for _, bo := range buildResult.Obligations {
				// Filter files already in the modified set (multiple path formats)
				boFile := bo.File
				if seen[boFile] || seen[filepath.Base(boFile)] {
					continue
				}
				// Also try cleaning relative paths (../../internal/... → internal/...)
				cleanFile := filepath.Clean(boFile)
				if seen[cleanFile] || seen[filepath.Base(cleanFile)] {
					continue
				}
				// Try resolving against cwd for absolute paths
				if filepath.IsAbs(boFile) {
					if rel, err := filepath.Rel(absCwd, boFile); err == nil {
						if seen[rel] || seen[filepath.Base(rel)] {
							continue
						}
					}
				}
				errSummary := bo.Errors[0]
				if len(bo.Errors) > 1 {
					errSummary = fmt.Sprintf("%s (+%d more)", bo.Errors[0], len(bo.Errors)-1)
				}
				suggestions = append(suggestions, suggestion{
					file:       bo.File,
					reason:     fmt.Sprintf("compiler: %s", errSummary),
					source:     "build-check",
					confidence: 0.95,
				})
			}
		}
	}

	// Confidence gate: require 2+ independent signal sources for non-compiler suggestions.
	// Compiler-verified (build-check) passes unconditionally — the compiler doesn't guess.
	// Single-source graph/comod suggestions are noise; intersections are signal.
	type fileSignals struct {
		sources    map[string]bool // distinct signal sources
		bestReason string
		bestConf   float64
	}
	fileMap := make(map[string]*fileSignals)
	for _, s := range suggestions {
		fs, ok := fileMap[s.file]
		if !ok {
			fs = &fileSignals{sources: make(map[string]bool)}
			fileMap[s.file] = fs
		}
		fs.sources[s.source] = true
		if s.confidence > fs.bestConf {
			fs.bestConf = s.confidence
			fs.bestReason = s.reason
		}
	}

	var ranked []suggestion
	for file, fs := range fileMap {
		// Build-check passes unconditionally (compiler-verified)
		if fs.sources["build-check"] {
			ranked = append(ranked, suggestion{
				file: file, reason: fs.bestReason,
				source: joinSources(fs.sources), confidence: fs.bestConf,
			})
			continue
		}
		// Non-compiler: require 2+ distinct signal sources
		if len(fs.sources) >= 2 {
			ranked = append(ranked, suggestion{
				file: file, reason: fs.bestReason,
				source: joinSources(fs.sources), confidence: fs.bestConf,
			})
		}
	}
	sort.Slice(ranked, func(i, j int) bool {
		return ranked[i].confidence > ranked[j].confidence
	})

	if len(ranked) == 0 {
		fmt.Println("\x1b[32m✓ No missing files detected. Your changes look complete.\x1b[0m")
		return nil
	}

	// Output
	must := 0
	for _, s := range ranked {
		if s.confidence >= 0.90 {
			must++
		}
	}

	if must > 0 {
		fmt.Printf("\x1b[31mMUST CHANGE (%d file(s) will not compile):\x1b[0m\n", must)
		for _, s := range ranked {
			if s.confidence >= 0.90 {
				fmt.Printf("  \x1b[31m✗\x1b[0m %s — %s\n", s.file, s.reason)
			}
		}
		fmt.Println()
	}

	likely := 0
	for _, s := range ranked {
		if s.confidence >= 0.50 && s.confidence < 0.90 {
			likely++
		}
	}
	if likely > 0 {
		fmt.Printf("\x1b[33mLIKELY NEEDED (%d file(s)):\x1b[0m\n", likely)
		for _, s := range ranked {
			if s.confidence >= 0.50 && s.confidence < 0.90 {
				fmt.Printf("  \x1b[33m~\x1b[0m %s — %s\n", s.file, s.reason)
			}
		}
		fmt.Println()
	}

	maybe := 0
	for _, s := range ranked {
		if s.confidence >= 0.30 && s.confidence < 0.50 {
			maybe++
		}
	}
	if maybe > 0 {
		fmt.Printf("Maybe (%d file(s)):\n", maybe)
		for _, s := range ranked {
			if s.confidence >= 0.30 && s.confidence < 0.50 {
				fmt.Printf("  ? %s — %s\n", s.file, s.reason)
			}
		}
	}

	return nil
}

// getModifiedGoFiles returns Go source files modified since baseRef.
func getModifiedGoFiles(cwd, baseRef string) ([]string, string) {
	// If no ref specified, check for uncommitted changes first
	if baseRef == "" {
		// Try uncommitted changes (staged + unstaged)
		cmd := exec.Command("git", "diff", "--name-only", "HEAD")
		cmd.Dir = cwd
		out, err := cmd.Output()
		if err == nil {
			files := filterGoFiles(strings.Split(strings.TrimSpace(string(out)), "\n"))
			if len(files) > 0 {
				return files, "HEAD (uncommitted)"
			}
		}
		// Fall back to last commit
		baseRef = "HEAD~1"
	}

	cmd := exec.Command("git", "diff", "--name-only", baseRef+"..HEAD")
	cmd.Dir = cwd
	out, err := cmd.Output()
	if err != nil {
		return nil, baseRef
	}

	// Also include uncommitted changes
	cmd2 := exec.Command("git", "diff", "--name-only", "HEAD")
	cmd2.Dir = cwd
	out2, _ := cmd2.Output()

	allFiles := strings.TrimSpace(string(out)) + "\n" + strings.TrimSpace(string(out2))
	return filterGoFiles(strings.Split(allFiles, "\n")), baseRef
}

func filterGoFiles(files []string) []string {
	seen := make(map[string]bool)
	var result []string
	for _, f := range files {
		f = strings.TrimSpace(f)
		if f == "" || !strings.HasSuffix(f, ".go") || strings.HasSuffix(f, "_test.go") {
			continue
		}
		if !seen[f] {
			seen[f] = true
			result = append(result, f)
		}
	}
	sort.Strings(result)
	return result
}

func resolveGraphFileForReview(file string, graph *refgraph.Graph, cwd string, moduleID int64) string {
	if _, err := os.Stat(filepath.Join(cwd, file)); err == nil {
		return file
	}
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

func joinSources(sources map[string]bool) string {
	var parts []string
	for s := range sources {
		parts = append(parts, s)
	}
	sort.Strings(parts)
	return strings.Join(parts, "+")
}

func minf(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
