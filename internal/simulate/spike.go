// spike.go — the engineer's daydream.
//
// Give Opus the task, the code, and the full neighborhood. Let it
// implement the change across all files. Parse what it touched.
// Trust the output. The spike IS the signal.
package simulate

import (
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/justinstimatze/plancheck/internal/refgraph"
	"github.com/justinstimatze/plancheck/internal/typeflow"
	"github.com/justinstimatze/plancheck/internal/types"
)

// SpikeResult is the output of the implementation spike.
type SpikeResult struct {
	Predictions []SpikePrediction `json:"predictions"`
	Obligations []Obligation      `json:"obligations,omitempty"` // MUST-change files
	FileBlocks  map[string]string `json:"-"`
	Cost        types.CostSummary `json:"cost"` // API token usage
}

// SpikePrediction is a file the engineer touched during implementation.
type SpikePrediction struct {
	File     string  `json:"file"`
	Reason   string  `json:"reason"`
	Verified bool    `json:"verified,omitempty"`
	Score    float64 `json:"score"`
}

// RunSpike executes the implementation spike.
// Opus implements the change. Two passes if the first discovers new files.
// The spike's output is the primary signal — not filtered by structural signals.
// domainHints are specific directories the task mentions but the plan doesn't cover.
func RunSpike(cwd string, graph *refgraph.Graph, planFiles []string,
	objective string, steps []string, _ int, domainHints []string) (*SpikeResult, error) {

	if !LLMAvailable() || os.Getenv("PLANCHECK_NO_SPIKE") == "1" {
		return nil, nil
	}

	// Agent spike: tool-using exploration (default when graph is available)
	if graph != nil && os.Getenv("PLANCHECK_NO_AGENT") != "1" {
		agentResult, err := RunAgentSpike(cwd, graph, planFiles, objective, steps, domainHints)
		if err != nil {
			// Agent failed — fall through to single-turn spike
			agentResult = nil
		}
		if agentResult != nil && len(agentResult.Files) == 0 {
			// Agent produced 0 files — fall through to single-turn spike
			agentResult = nil
		}
		if agentResult != nil {
			var predictions []SpikePrediction
			planFileSet := make(map[string]bool)
			for _, pf := range planFiles {
				planFileSet[pf] = true
			}
			for _, af := range agentResult.Files {
				if planFileSet[af.Path] {
					continue
				}
				fullPath := filepath.Join(cwd, af.Path)
				if _, err := os.Stat(fullPath); err != nil {
					continue
				}
				score := af.Score
				// Diff-aware scoring: discount files where the spike barely changed anything
				if af.Code != "" {
					original, err := os.ReadFile(fullPath)
					if err == nil {
						changeMag := diffMagnitude(string(original), af.Code)
						if changeMag < 0.03 {
							score *= 0.50 // trivial change — likely noise
						} else if changeMag < 0.10 {
							score *= 0.75 // small change — moderate confidence
						}
					}
				}
				reason := fmt.Sprintf("agent spike: turn %d, %d lines", af.Turn, af.Lines)
				predictions = append(predictions, SpikePrediction{
					File:     af.Path,
					Reason:   reason,
					Verified: true,
					Score:    score,
				})
			}

			// Run obligation extraction on agent's code blocks
			var obligations []Obligation
			if len(agentResult.FileBlocks) > 0 {
				obligations = ExtractObligations(agentResult.FileBlocks, graph, planFiles, cwd)

				// BUILD-AND-CHECK: apply spike code via go build -overlay.
				// The compiler finds files that MUST change — near-100% precision.
				buildResult, buildErr := RunBuildCheck(agentResult.FileBlocks, cwd)
				if buildErr == nil && buildResult != nil {
					for _, bo := range buildResult.Obligations {
						if planFileSet[bo.File] {
							continue
						}
						// Compiler-verified obligation: highest confidence
						errSummary := bo.Errors[0]
						if len(bo.Errors) > 1 {
							errSummary = fmt.Sprintf("%s (+%d more)", bo.Errors[0], len(bo.Errors)-1)
						}
						predictions = append(predictions, SpikePrediction{
							File:     bo.File,
							Reason:   fmt.Sprintf("build-check: %s", errSummary),
							Verified: true,
							Score:    0.95, // compiler says it MUST change
						})
						obligations = append(obligations, Obligation{
							File:   bo.File,
							Kind:   "compile-error",
							Reason: errSummary,
						})
					}
				}
			}

			// TARGETED BUILD-CHECK on explored definitions: for definitions
			// the agent looked up via code(), probe ONLY that definition
			// (not all exports). This finds callers of the specific function
			// the agent was interested in, not the entire dependency cone.
			if len(agentResult.ExploredDefs) > 0 {
				targetedBlocks := make(map[string]string)
				for _, ed := range agentResult.ExploredDefs {
					if planFileSet[ed.File] || targetedBlocks[ed.File] != "" {
						continue
					}
					fullPath := filepath.Join(cwd, ed.File)
					original, err := os.ReadFile(fullPath)
					if err != nil {
						continue
					}
					probed := probeSpecificDefinition(string(original), ed.Name)
					if probed != string(original) {
						targetedBlocks[ed.File] = probed
					}
				}
				if len(targetedBlocks) > 0 {
					buildResult, buildErr := RunBuildCheck(targetedBlocks, cwd)
					if buildErr == nil && buildResult != nil {
						for _, bo := range buildResult.Obligations {
							if planFileSet[bo.File] {
								continue
							}
							errSummary := bo.Errors[0]
							if len(bo.Errors) > 1 {
								errSummary = fmt.Sprintf("%s (+%d more)", bo.Errors[0], len(bo.Errors)-1)
							}
							predictions = append(predictions, SpikePrediction{
								File:     bo.File,
								Reason:   fmt.Sprintf("targeted-build-check: %s", errSummary),
								Verified: true,
								Score:    0.90,
							})
							obligations = append(obligations, Obligation{
								File:   bo.File,
								Kind:   "targeted-compile-error",
								Reason: errSummary,
							})
						}
					}
				}
			}

			// RECURSIVE SPIKE: disabled by default — doubles wall time per task
			// (~5-8min vs ~2-3min) and most extra cost is wasted when the second
			// spike doesn't find new depth-2 files. Enable with PLANCHECK_RECURSIVE=1
			// for tasks where depth-2 discovery is worth the cost.
			if os.Getenv("PLANCHECK_RECURSIVE") == "1" {
				var expandedFiles []string
				for _, pred := range predictions {
					// Only recurse on implemented files (have code), not exploration-only
					if pred.Score >= 0.70 {
						if _, ok := agentResult.FileBlocks[pred.File]; ok {
							expandedFiles = append(expandedFiles, pred.File)
						}
					}
				}
				if len(expandedFiles) >= 1 && len(expandedFiles) <= 3 {
					// Second spike: expanded plan = original + discovered files
					expandedPlan := make([]string, len(planFiles))
					copy(expandedPlan, planFiles)
					expandedPlan = append(expandedPlan, expandedFiles...)

					secondResult, secondErr := RunAgentSpikeLightweight(
						cwd, graph, expandedPlan, objective, steps)
					if secondErr == nil && secondResult != nil {
						// Merge second spike's findings at lower confidence
						allSeen := make(map[string]bool)
						for _, p := range predictions {
							allSeen[p.File] = true
						}
						for _, pf := range planFiles {
							allSeen[pf] = true
						}
						for _, af := range secondResult.Files {
							if allSeen[af.Path] || planFileSet[af.Path] {
								continue
							}
							fullPath := filepath.Join(cwd, af.Path)
							if _, err := os.Stat(fullPath); err != nil {
								continue
							}
							// Depth-2 score: 70% of the original score
							score := af.Score * 0.70
							predictions = append(predictions, SpikePrediction{
								File:     af.Path,
								Reason:   fmt.Sprintf("recursive spike: %s", af.Path),
								Verified: true,
								Score:    score,
							})
						}
						// Merge file blocks
						for f, code := range secondResult.FileBlocks {
							if _, exists := agentResult.FileBlocks[f]; !exists {
								agentResult.FileBlocks[f] = code
							}
						}
					}
				}
			}

			return &SpikeResult{
				Predictions: predictions,
				Obligations: obligations,
				FileBlocks:  agentResult.FileBlocks,
				Cost:        agentResult.Cost,
			}, nil
		}
	}

	dirTree := buildDirTree(graph)
	mutations := typeflow.InferMutations(planFiles, objective, steps, cwd)
	var mutationHints []string
	for _, m := range mutations {
		mutationHints = append(mutationHints, fmt.Sprintf("- %s in %s (%s)", m.FuncName, m.File, m.Reason))
	}
	key := getAPIKey()

	// Build full neighborhood: plan files + siblings + shared + parent + mirrors + API layer
	neighborhood := buildNeighborhood(planFiles, objective, steps, graph, cwd)

	// === PASS 1 ===
	pass1Blocks := runSpikePass(neighborhood, objective, steps, dirTree, mutationHints, key, cwd)

	// === PASS 2: if pass 1 discovered files outside neighborhood ===
	fileBlocks := pass1Blocks
	neighborSet := make(map[string]bool)
	for _, nf := range neighborhood {
		neighborSet[nf] = true
	}

	var newDiscoveries []string
	for file := range pass1Blocks {
		if !neighborSet[file] {
			fullPath := filepath.Join(cwd, file)
			if _, err := os.Stat(fullPath); err == nil {
				newDiscoveries = append(newDiscoveries, file)
			}
		}
	}
	if graph != nil {
		for _, block := range pass1Blocks {
			for _, call := range extractCallNames(block) {
				def := graph.GetDef(call, "")
				if def != nil && def.SourceFile != "" {
					resolved := typeflow.ResolveGoFiles(map[string]bool{def.SourceFile: true}, cwd)
					for _, paths := range resolved {
						for _, p := range paths {
							if !neighborSet[p] {
								newDiscoveries = append(newDiscoveries, p)
							}
						}
					}
				}
			}
		}
	}

	if len(newDiscoveries) > 0 && os.Getenv("PLANCHECK_NO_PASS2") != "1" {
		expanded := append([]string{}, neighborhood...)
		seen := make(map[string]bool)
		for _, n := range expanded {
			seen[n] = true
		}
		for _, d := range newDiscoveries {
			if !seen[d] {
				seen[d] = true
				expanded = append(expanded, d)
			}
		}
		pass2Blocks := runSpikePass(expanded, objective, steps, dirTree, mutationHints, key, cwd)
		if len(pass2Blocks) > 0 {
			for file, code := range pass2Blocks {
				fileBlocks[file] = code
			}
		}
	}

	// Build predictions from what the spike touched
	planFileSet := make(map[string]bool)
	for _, pf := range planFiles {
		planFileSet[pf] = true
	}

	var predictions []SpikePrediction
	touchedSet := make(map[string]bool)

	for file, code := range fileBlocks {
		if planFileSet[file] {
			// Plan file — analyze for new dependencies
			if graph != nil {
				newCalls := extractNewCalls(code, filepath.Join(cwd, file))
				for _, call := range newCalls {
					def := graph.GetDef(call, "")
					if def != nil && def.SourceFile != "" && !planFileSet[def.SourceFile] {
						resolved := typeflow.ResolveGoFiles(map[string]bool{def.SourceFile: true}, cwd)
						for _, paths := range resolved {
							for _, p := range paths {
								if !planFileSet[p] && !touchedSet[p] {
									touchedSet[p] = true
									predictions = append(predictions, SpikePrediction{
										File:     p,
										Reason:   fmt.Sprintf("spike: implementation calls %s", call),
										Verified: true,
										Score:    0.70,
									})
								}
							}
						}
					}
				}
			}
			continue
		}

		fullPath := filepath.Join(cwd, file)
		if _, err := os.Stat(fullPath); err != nil {
			continue
		}
		touchedSet[file] = true

		predictions = append(predictions, SpikePrediction{
			File:     file,
			Reason:   "spike: engineer modified this file during implementation",
			Verified: true,
			Score:    0.80, // high confidence — the engineer touched it
		})
	}

	// Trace consequences: struct field changes, broken callers
	simResult := ApplySpike(fileBlocks, graph, planFiles, cwd)
	if simResult != nil {
		for _, file := range simResult.BrokenCallers {
			if !touchedSet[file] && !planFileSet[file] {
				touchedSet[file] = true
				predictions = append(predictions, SpikePrediction{
					File:     file,
					Reason:   "spike: callers of modified function may break",
					Verified: true,
					Score:    0.65,
				})
			}
		}
		for _, file := range simResult.NewRefs {
			if !touchedSet[file] && !planFileSet[file] {
				touchedSet[file] = true
				predictions = append(predictions, SpikePrediction{
					File:     file,
					Reason:   "spike: new dependency introduced by implementation",
					Verified: true,
					Score:    0.60,
				})
			}
		}
	}

	// Extract type-system obligations (MUST-change files)
	obligations := ExtractObligations(fileBlocks, graph, planFiles, cwd)

	// Obligation files also become predictions with high score
	for _, ob := range obligations {
		if !touchedSet[ob.File] && !planFileSet[ob.File] {
			touchedSet[ob.File] = true
			predictions = append(predictions, SpikePrediction{
				File:     ob.File,
				Reason:   "OBLIGATION: " + ob.Reason,
				Verified: true,
				Score:    0.95, // near-certain — type system says so
			})
		}
	}

	return &SpikeResult{
		Predictions: predictions,
		Obligations: obligations,
		FileBlocks:  fileBlocks,
	}, nil
}

// runSpikePass executes a single implementation pass.
func runSpikePass(neighborhood []string, objective string, steps []string,
	dirTree string, mutationHints []string, key string, cwd string) map[string]string {

	var sourceBlocks []string
	var totalLines int
	for _, nf := range neighborhood {
		absPath := filepath.Join(cwd, nf)
		data, err := os.ReadFile(absPath)
		if err != nil {
			continue
		}
		src := string(data)
		lines := strings.Split(src, "\n")
		maxLines := 1200 - totalLines // bigger budget for Opus
		if maxLines <= 20 {
			break
		}
		if len(lines) > maxLines {
			lines = lines[:maxLines]
			src = strings.Join(lines, "\n") + "\n// ... (truncated)"
		}
		totalLines += len(lines)
		sourceBlocks = append(sourceBlocks, fmt.Sprintf("// FILE: %s\n%s", nf, src))
	}
	if len(sourceBlocks) == 0 {
		return nil
	}

	prompt := buildSpikePrompt(objective, steps, sourceBlocks, dirTree, mutationHints)

	// Let the model write as much as it needs
	model := os.Getenv("PLANCHECK_SPIKE_MODEL")
	if model == "" {
		model = "claude-sonnet-4-6"
	}
	response, _, err := callClaudeWithTokens(key, prompt, model, nil, 8192)
	if err != nil {
		return nil
	}

	return extractFileBlocks(response)
}

// buildNeighborhood finds files the engineer would read before implementing.
// Plan files + siblings + shared + parent + mirrors + API layer imports.
func buildNeighborhood(planFiles []string, objective string, steps []string,
	graph *refgraph.Graph, cwd string) []string {

	seen := make(map[string]bool)
	var result []string

	add := func(path string) {
		path = filepath.ToSlash(path)
		if seen[path] {
			return
		}
		absPath := filepath.Join(cwd, path)
		if _, err := os.Stat(absPath); err != nil {
			return
		}
		seen[path] = true
		result = append(result, path)
	}

	// 1. Plan files
	for _, pf := range planFiles {
		add(pf)
	}

	// 2. Mirror directories from task text
	allText := strings.ToLower(objective)
	for _, s := range steps {
		allText += " " + strings.ToLower(s)
	}
	for _, pf := range planFiles {
		dir := filepath.ToSlash(filepath.Dir(pf))
		parts := strings.Split(dir, "/")
		for level := len(parts) - 1; level >= 1; level-- {
			parent := strings.Join(parts[:level], "/")
			siblings, err := os.ReadDir(filepath.Join(cwd, parent))
			if err != nil {
				continue
			}
			for _, sib := range siblings {
				if !sib.IsDir() {
					continue
				}
				sibName := strings.ToLower(sib.Name())
				if sibName == parts[level] || len(sibName) < 3 {
					continue
				}
				if !strings.Contains(allText, sibName) {
					continue
				}
				subPath := strings.Join(parts[level+1:], "/")
				mirrorDir := filepath.Join(parent, sib.Name(), subPath)
				addDirFiles(mirrorDir, cwd, add)
				sibRoot := filepath.Join(parent, sib.Name())
				addDirFiles(sibRoot, cwd, add)
				addDirFiles(filepath.Join(sibRoot, "shared"), cwd, add)
			}
		}
	}

	// 3. ALL sibling subcommand directories (not just text-mentioned)
	// If plan has pkg/cmd/repo/autolink/delete/, include ALL of:
	// pkg/cmd/repo/autolink/list/, /create/, /view/ etc.
	// This is the #1 gap: 60% of missed gold files are sibling subcommands.
	for _, pf := range planFiles {
		dir := filepath.ToSlash(filepath.Dir(pf))
		parts := strings.Split(dir, "/")
		if len(parts) >= 2 {
			parent := strings.Join(parts[:len(parts)-1], "/")
			siblings, err := os.ReadDir(filepath.Join(cwd, parent))
			if err == nil {
				for _, sib := range siblings {
					if sib.IsDir() && sib.Name() != filepath.Base(dir) {
						addDirFiles(filepath.Join(parent, sib.Name()), cwd, add)
					}
				}
			}
		}
	}

	// 4. Same-directory files + shared + parent
	for _, pf := range planFiles {
		dir := filepath.Dir(pf)
		addDirFiles(dir, cwd, add)

		parts := strings.Split(filepath.ToSlash(dir), "/")
		if len(parts) >= 2 {
			parent := strings.Join(parts[:len(parts)-1], "/")
			addDirFiles(filepath.Join(parent, "shared"), cwd, add)
			addDirFiles(parent, cwd, add)
		}
	}

	// 4. API layer: files imported by plan files
	for _, pf := range planFiles {
		absPath := filepath.Join(cwd, pf)
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, absPath, nil, parser.ImportsOnly)
		if err != nil {
			continue
		}
		modPath := readModPath(cwd)
		for _, imp := range f.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			if !strings.HasPrefix(path, modPath+"/") {
				continue
			}
			relDir := strings.TrimPrefix(path, modPath+"/")
			addDirFiles(relDir, cwd, add)
		}
	}

	return result
}

func addDirFiles(dir, cwd string, add func(string)) {
	entries, err := os.ReadDir(filepath.Join(cwd, dir))
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".go") &&
			!strings.HasSuffix(e.Name(), "_test.go") {
			add(filepath.Join(dir, e.Name()))
		}
	}
}

func readModPath(cwd string) string {
	data, err := os.ReadFile(filepath.Join(cwd, "go.mod"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module"))
		}
	}
	return ""
}

func buildDirTree(graph *refgraph.Graph) string {
	if graph == nil {
		return ""
	}
	dirs := make(map[string]bool)
	for _, d := range graph.AllDefs() {
		if d.SourceFile != "" && !d.Test {
			dirs[filepath.Dir(d.SourceFile)] = true
		}
	}
	var dirList []string
	for d := range dirs {
		if d != "." {
			dirList = append(dirList, d)
		}
	}
	sort.Strings(dirList)
	if len(dirList) > 40 {
		dirList = dirList[:40]
	}
	return strings.Join(dirList, "\n")
}

func buildSpikePrompt(objective string, steps []string, sourceBlocks []string,
	dirTree string, mutationHints []string) string {

	var b strings.Builder

	b.WriteString("You are a senior Go engineer. Implement this change completely.\n")
	b.WriteString("Write the actual code changes for EVERY file that needs modification.\n")
	b.WriteString("This is a real implementation — follow the data flow across files.\n\n")

	b.WriteString(fmt.Sprintf("TASK: %s\n\n", objective))

	if len(steps) > 0 {
		b.WriteString("PLAN:\n")
		for i, s := range steps {
			if len(s) > 300 {
				s = s[:300]
			}
			b.WriteString(fmt.Sprintf("%d. %s\n", i+1, s))
		}
		b.WriteString("\n")
	}

	if len(mutationHints) > 0 {
		b.WriteString("KEY FUNCTIONS:\n")
		for _, h := range mutationHints {
			b.WriteString(h + "\n")
		}
		b.WriteString("\n")
	}

	if dirTree != "" {
		b.WriteString("PROJECT STRUCTURE:\n")
		b.WriteString(dirTree)
		b.WriteString("\n\n")
	}

	b.WriteString("CURRENT CODE:\n\n")
	for _, block := range sourceBlocks {
		b.WriteString(block)
		b.WriteString("\n\n")
	}

	b.WriteString("Implement the change. For each file you modify:\n")
	b.WriteString("// FILE: path/to/file.go\n")
	b.WriteString("<actual modified code>\n\n")
	b.WriteString("RULES:\n")
	b.WriteString("- Modify ALL files needed, not just the first\n")
	b.WriteString("- If you add a struct field, update every constructor and reader\n")
	b.WriteString("- If you add a flag, wire it through to the HTTP/API layer\n")
	b.WriteString("- If the task mentions multiple commands, implement for ALL of them\n")
	b.WriteString("- No test files\n")

	return b.String()
}

var fileHeaderRe = regexp.MustCompile(`(?m)^//\s*FILE:\s*(\S+\.go)\s*$`)

func extractFileBlocks(response string) map[string]string {
	blocks := make(map[string]string)
	indices := fileHeaderRe.FindAllStringSubmatchIndex(response, -1)

	for i, match := range indices {
		file := response[match[2]:match[3]]
		file = strings.TrimPrefix(file, "./")
		if strings.HasSuffix(file, "_test.go") || !strings.Contains(file, "/") {
			continue
		}
		codeStart := match[1]
		codeEnd := len(response)
		if i+1 < len(indices) {
			codeEnd = indices[i+1][0]
		}
		blocks[file] = response[codeStart:codeEnd]
	}
	return blocks
}

func extractNewCalls(spikeCode, originalPath string) []string {
	spikeCalls := extractCallNames(spikeCode)
	originalData, err := os.ReadFile(originalPath)
	if err != nil {
		return spikeCalls
	}
	originalCalls := make(map[string]bool)
	for _, c := range extractCallNames(string(originalData)) {
		originalCalls[c] = true
	}
	var newCalls []string
	for _, c := range spikeCalls {
		if !originalCalls[c] {
			newCalls = append(newCalls, c)
		}
	}
	return newCalls
}

// diffMagnitude computes the fraction of lines that differ between original and spike.
// Returns 0.0 (identical) to 1.0 (completely different).
func diffMagnitude(original, spike string) float64 {
	origLines := strings.Split(strings.TrimSpace(original), "\n")
	spikeLines := strings.Split(strings.TrimSpace(spike), "\n")

	// Build set of trimmed lines from original
	origSet := make(map[string]int)
	for _, l := range origLines {
		origSet[strings.TrimSpace(l)]++
	}

	// Count lines in spike that aren't in original
	changed := 0
	for _, l := range spikeLines {
		key := strings.TrimSpace(l)
		if origSet[key] > 0 {
			origSet[key]--
		} else {
			changed++
		}
	}

	total := len(origLines)
	if len(spikeLines) > total {
		total = len(spikeLines)
	}
	if total == 0 {
		return 0
	}
	return float64(changed) / float64(total)
}

var callRe = regexp.MustCompile(`([a-zA-Z_]\w*(?:\.[A-Z]\w*)?)(?:\[.*?\])?\s*\(`)

func extractCallNames(code string) []string {
	seen := make(map[string]bool)
	var calls []string
	for _, match := range callRe.FindAllStringSubmatch(code, -1) {
		name := match[1]
		if isGoBuiltin(name) || len(name) < 3 {
			continue
		}
		if strings.Contains(name, ".") {
			parts := strings.Split(name, ".")
			name = parts[len(parts)-1]
		}
		if !seen[name] && name[0] >= 'A' && name[0] <= 'Z' {
			seen[name] = true
			calls = append(calls, name)
		}
	}
	return calls
}

func isGoBuiltin(name string) bool {
	builtins := map[string]bool{
		"make": true, "len": true, "cap": true, "append": true, "copy": true,
		"close": true, "delete": true, "new": true, "panic": true, "recover": true,
		"print": true, "println": true, "error": true, "string": true, "int": true,
		"bool": true, "byte": true, "nil": true, "true": true, "false": true,
		"fmt": true, "os": true, "if": true, "for": true, "func": true,
		"return": true, "range": true, "var": true, "type": true, "struct": true,
	}
	return builtins[name]
}
