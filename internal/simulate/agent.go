// agent.go implements the agent spike — a tool-using implementation attempt.
//
// Instead of a single-turn completion with fixed context, the agent
// explores the codebase using tools: impact analysis, file reading,
// code search, directory listing. It discovers dependencies through
// interaction, like a real engineer.
//
// Tools are defined in agenttools.go; API types and HTTP client in agentapi.go.
package simulate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/justinstimatze/plancheck/internal/refgraph"
	"github.com/justinstimatze/plancheck/internal/types"
)

// AgentFile is a file discovered by the agent with metadata for scoring.
type AgentFile struct {
	Path  string  `json:"path"`
	Code  string  `json:"code"`  // implemented code (may be empty for mentions)
	Turn  int     `json:"turn"`  // which implementation turn discovered it (1-based)
	Lines int     `json:"lines"` // lines of code written
	Score float64 `json:"score"` // confidence score based on turn + code size
}

// ExploredDef is a definition the agent actively looked up via code().
type ExploredDef struct {
	Name string // definition full name (e.g., "(*Server).LookupAccount")
	File string // source file path
}

// AgentResult is the output of the agent spike.
type AgentResult struct {
	Files        []AgentFile       `json:"files"`        // files with scoring metadata
	FileBlocks   map[string]string `json:"fileBlocks"`   // file path → implemented code
	ToolCalls    int               `json:"toolCalls"`    // how many tool calls it made
	ExploredDefs []ExploredDef     `json:"-"`            // definitions looked up via code()
	Cost         types.CostSummary `json:"cost"`         // API token usage
}

// RunAgentSpikeLightweight runs a reduced-turn spike for recursive discovery.
// 2 exploration turns + 1 implementation turn (vs 4+3 for the full spike).
func RunAgentSpikeLightweight(cwd string, graph *refgraph.Graph, planFiles []string,
	objective string, steps []string) (*AgentResult, error) {
	return runAgentSpike(cwd, graph, planFiles, objective, steps, nil, 2, 1)
}

// RunAgentSpike runs the tool-using agent implementation.
// domainHints are specific directories the task mentions but the plan doesn't cover.
func RunAgentSpike(cwd string, graph *refgraph.Graph, planFiles []string,
	objective string, steps []string, domainHints []string) (*AgentResult, error) {
	return runAgentSpike(cwd, graph, planFiles, objective, steps, domainHints, 4, 3)
}

func runAgentSpike(cwd string, graph *refgraph.Graph, planFiles []string,
	objective string, steps []string, domainHints []string,
	explorationTurnCount, implTurnCount int) (*AgentResult, error) {

	if !LLMAvailable() || os.Getenv("PLANCHECK_NO_SPIKE") == "1" {
		return nil, nil
	}

	key := getAPIKey()
	model := os.Getenv("PLANCHECK_SPIKE_MODEL")
	if model == "" {
		model = "claude-sonnet-4-6"
	}

	var cost types.CostSummary
	cost.Model = model

	// Build initial message
	var stepsText string
	for i, s := range steps {
		if len(s) > 300 {
			s = s[:300]
		}
		stepsText += fmt.Sprintf("%d. %s\n", i+1, s)
	}

	systemPrompt := "You are a senior Go engineer. You implement code changes by writing actual Go code. " +
		"Between writing code, you can use tools to read files you need. " +
		"Your text responses should contain the actual modified code, marked with // FILE: path/to/file.go headers. " +
		"Do NOT just describe what to change — write the code."

	userMessage := fmt.Sprintf("TASK: %s\n\nPLAN FILES:\n%s\n\n",
		objective, strings.Join(planFiles, "\n"))
	if stepsText != "" {
		userMessage += "STEPS:\n" + stepsText + "\n"
	}

	// Domain hints: specific directories the task mentions but the plan doesn't cover.
	// These are grounded in structural analysis, not generic advice.
	if len(domainHints) > 0 {
		userMessage += "DOMAIN GAPS (task mentions areas your plan doesn't cover):\n"
		for _, hint := range domainHints {
			userMessage += "  - " + hint + "\n"
		}
		userMessage += "\n"
	}

	userMessage += "Read the plan files first, then implement the change. Write the modified code for each file:\n\n" +
		"// FILE: path/to/file.go\npackage ...\n<actual Go code>\n\n" +
		"Use read() to look at files before modifying them. Use impact() to find callers of functions you change. " +
		"Use code() to read specific functions or types by name (better than read() for large files). " +
		"The code doesn't need to compile perfectly — just get the shape right so we can see which files change and how."

	// Tool definitions — code() and impact() require defn graph
	var tools []agentTool
	if graph != nil {
		tools = append(tools, agentTool{
			Name:        "code",
			Description: "Read the source code of a specific function, method, or type by name. Much better than read() for large files — goes directly to the definition instead of reading from line 1. Use this when you know the name of what you need.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"name": map[string]string{"type": "string", "description": "Definition name (e.g., 'Connz', 'initClient', 'Server.Start')"},
				},
				"required": []string{"name"},
			},
		}, agentTool{
			Name:        "impact",
			Description: "Show callers, transitive callers, and test coverage for a function or method. Use this to understand what depends on a function you're modifying.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"name": map[string]string{"type": "string", "description": "Function or method name (e.g., 'CreateRun', 'NewCmdCreate')"},
				},
				"required": []string{"name"},
			},
		}, agentTool{
			Name:        "constructors",
			Description: "Find all places that construct a struct type — struct literals, factory functions, and direct references. Use this when you add a field to a struct and need to find every place that builds it.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"name": map[string]string{"type": "string", "description": "Struct type name (e.g., 'Options', 'CreateOptions', 'Connz')"},
				},
				"required": []string{"name"},
			},
		})
	}
	readDesc := "Read a file from the codebase. Use start_line to read deeper into large files."
	if graph != nil {
		readDesc += " Prefer code() when you know the definition name."
	}
	tools = append(tools,
		agentTool{
			Name:        "read",
			Description: readDesc,
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path":       map[string]string{"type": "string", "description": "Relative file path (e.g., 'server/monitor.go')"},
					"start_line": map[string]string{"type": "number", "description": "Line number to start reading from (default: 1)"},
				},
				"required": []string{"path"},
			},
		},
		agentTool{
			Name:        "find",
			Description: "Search the codebase for a string pattern. Returns matching file paths and line numbers.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"query": map[string]string{"type": "string", "description": "Search pattern (e.g., 'CreateOptions{', 'func NewCmdCreate')"},
				},
				"required": []string{"query"},
			},
		},
		agentTool{
			Name:        "list",
			Description: "List Go files in a directory.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path": map[string]string{"type": "string", "description": "Relative directory path"},
				},
				"required": []string{"path"},
			},
		},
	)

	// Agent loop
	messages := []agentMessage{
		{Role: "user", Content: userMessage},
	}

	// Phase 1: Exploration — agent reads files and checks impact (with tools).
	// We do NOT parse file mentions from exploration text (causes precision collapse).
	// BUT we DO track which non-plan files the agent actively investigated via
	// tool calls (read, code). These are intentional navigation actions, not
	// casual mentions — strong signal that the agent thinks the file is relevant.
	var totalToolCalls int
	allImplementedFiles := make(map[string]bool)
	allFileBlocks := make(map[string]string) // file → code
	explorationTurns := explorationTurnCount

	planFileSet := make(map[string]bool)
	for _, pf := range planFiles {
		planFileSet[pf] = true
	}

	// Track files the agent actively explored (read/code on non-plan files)
	type exploredFile struct {
		path    string
		tool    string // "read", "code", "find"
		turn    int
		defName string // for code() calls: the definition name looked up
	}
	var exploredFiles []exploredFile

	debugSpike := os.Getenv("PLANCHECK_SPIKE_DEBUG") == "1"

	for turn := 0; turn < explorationTurns; turn++ {
		resp, err := callAgentAPI(key, model, systemPrompt, messages, tools)
		if err != nil {
			if debugSpike {
				fmt.Fprintf(os.Stderr, "[spike] exploration turn %d error: %v\n", turn, err)
			}
			break
		}
		cost.AddTokens(resp.Usage.InputTokens, resp.Usage.OutputTokens, model)

		hasToolUse := false
		var toolResults []agentMessage

		for _, block := range resp.Content {
			if block.Type == "tool_use" {
				hasToolUse = true
				totalToolCalls++
				if debugSpike {
					inputJSON, _ := json.Marshal(block.Input)
					fmt.Fprintf(os.Stderr, "[spike] explore turn %d: %s(%s)\n", turn, block.Name, string(inputJSON))
				}
				result := executeTool(block.Name, block.Input, graph, cwd)
				toolResults = append(toolResults, agentMessage{
					Role:         "user",
					Content:      result,
					ToolUseID:    block.ID,
					IsToolResult: true,
				})

				// Track non-plan file exploration for discovery signal
				switch block.Name {
				case "read":
					if path, ok := block.Input["path"].(string); ok && !planFileSet[path] {
						exploredFiles = append(exploredFiles, exploredFile{path: path, tool: "read", turn: turn})
					}
				case "code":
					if defName, ok := block.Input["name"].(string); ok {
						// Resolve definition to source file via graph
						if def := graph.GetDef(defName, ""); def != nil && def.SourceFile != "" {
							filePath := resolveSourceFile(def, graph, cwd)
							if filePath != "" && !planFileSet[filePath] {
								exploredFiles = append(exploredFiles, exploredFile{
									path: filePath, tool: "code", turn: turn,
									defName: def.FullName(),
								})
							}
						}
					}
				case "constructors":
					// constructors() returns callers of a type — track the caller files
					// as exploration signals (the agent wants to know who constructs this)
					if typeName, ok := block.Input["name"].(string); ok {
						if def := graph.GetDef(typeName, ""); def != nil {
							for _, caller := range graph.CallerDefs(def.ID) {
								if caller.Test || caller.SourceFile == "" {
									continue
								}
								callerPath := resolveSourceFile(caller, graph, cwd)
								if callerPath != "" && !planFileSet[callerPath] {
									exploredFiles = append(exploredFiles, exploredFile{
										path: callerPath, tool: "constructors", turn: turn,
									})
								}
							}
						}
					}
				case "find":
					// Extract top non-plan files from find results.
					// The agent searched for something — files with matches are relevant.
					findFiles := extractFilesFromFindResult(result)
					for _, ff := range findFiles {
						if !planFileSet[ff] {
							exploredFiles = append(exploredFiles, exploredFile{path: ff, tool: "find", turn: turn})
						}
					}
				}
			}
			if block.Type == "text" && debugSpike {
				text := block.Text
				if len(text) > 200 {
					text = text[:200] + "..."
				}
				fmt.Fprintf(os.Stderr, "[spike] explore turn %d text: %s\n", turn, text)
			}
		}

		messages = append(messages, agentMessage{
			Role:    "assistant",
			Content: resp.RawContent,
			IsRaw:   true,
		})
		messages = append(messages, toolResults...)

		if !hasToolUse {
			break
		}
	}

	fileTurns := make(map[string]int) // file → which turn discovered it
	fileLines := make(map[string]int) // file → lines of code

	// Phase 2: Multi-turn implementation. The agent writes actual code.
	// Each turn produces 2-3 files with // FILE: headers. We prompt "continue"
	// until it stops producing new files. The code doesn't need to compile —
	// it needs the right shape (function signatures, struct fields, imports)
	// to reveal which files change through data-flow discovery.
	messages = append(messages, agentMessage{
		Role: "user",
		Content: "STOP ANALYZING. Now write the code.\n\n" +
			"This is a SIMULATION. Even if the feature partially exists, write out the COMPLETE implementation " +
			"as if you were making the change from scratch. I need to see every file you would touch.\n\n" +
			"For EACH file that needs modification, write the FULL modified file:\n\n" +
			"// FILE: path/to/file.go\npackage pkgname\n\nimport (\n...\n)\n\n// ... full modified source\n\n" +
			"You MUST write actual Go code for every file. Do NOT explain, do NOT describe, do NOT say " +
			"\"this already exists\" — write the modified code. Start with the primary file, then write " +
			"every other file that needs changes to make the feature work end-to-end.\n\n" +
			"Follow the COMPLETE data flow: struct definition → constructors → flag wiring → HTTP layer → tests.",
	})

	// Turn-based confidence: core change (turn 1) > dependency chain (turn 2) > extended (turn 3)
	turnScores := [3]float64{0.85, 0.70, 0.55}
	const minCodeLines = 10 // require ≥10 lines of actual code

	implTurns := implTurnCount
	for turn := 0; turn < implTurns; turn++ {
		resp, err := callAgentAPI(key, model, systemPrompt, messages, nil) // no tools — forces code writing
		if err != nil {
			break
		}
		cost.AddTokens(resp.Usage.InputTokens, resp.Usage.OutputTokens, model)

		foundThisTurn := 0
		for _, block := range resp.Content {
			if block.Type == "text" {
				if debugSpike {
					text := block.Text
					if len(text) > 300 {
						text = text[:300] + "..."
					}
					fmt.Fprintf(os.Stderr, "[spike] impl turn %d text (%d chars): %s\n", turn, len(block.Text), text)
				}
				allBlocks := extractAgentFileBlocks(block.Text)
				if debugSpike {
					fmt.Fprintf(os.Stderr, "[spike] impl turn %d: extracted %d file blocks\n", turn, len(allBlocks))
					for f, code := range allBlocks {
						fmt.Fprintf(os.Stderr, "[spike]   %s: %d lines\n", f, len(strings.Split(code, "\n")))
					}
				}
				for f, code := range allBlocks {
					lines := len(strings.Split(strings.TrimSpace(code), "\n"))
					if code == "" || lines < minCodeLines {
						if debugSpike {
							fmt.Fprintf(os.Stderr, "[spike]   SKIP %s: %d lines (min %d)\n", f, lines, minCodeLines)
						}
						continue
					}
					if !allImplementedFiles[f] {
						foundThisTurn++
						allImplementedFiles[f] = true
						allFileBlocks[f] = code
						// Track turn for scoring — first discovery wins
						fileTurns[f] = turn
						fileLines[f] = lines
					}
				}
			}
		}

		messages = append(messages, agentMessage{
			Role:    "assistant",
			Content: resp.RawContent,
			IsRaw:   true,
		})

		// If the agent found new files, prompt for more
		if foundThisTurn > 0 && turn < implTurns-1 {
			messages = append(messages, agentMessage{
				Role: "user",
				Content: "Good. Continue implementing. What other files need changes? " +
					"Follow the dependency chains — callers that pass the new parameter, " +
					"constructors that build the modified struct, tests that exercise the changed path. " +
					"Write the code with // FILE: headers. Say DONE when finished.",
			})
		} else {
			break // no new files or agent said done
		}
	}

	// Build scored file list
	var agentFiles []AgentFile
	for f := range allImplementedFiles {
		turn := fileTurns[f]
		lines := fileLines[f]
		score := turnScores[turn]
		agentFiles = append(agentFiles, AgentFile{
			Path:  f,
			Code:  allFileBlocks[f],
			Turn:  turn + 1, // 1-based for display
			Lines: lines,
			Score: score,
		})
	}

	// Add explored-but-not-implemented files as lower-confidence signals.
	// Score based on HOW the agent interacted with the file:
	//   code() = 0.50 (most intentional — looked up specific definition)
	//   read() = 0.45 (actively read the file)
	//   find() = 0.30 (file appeared in search results — weakest)
	// Bonus: +0.10 if the file was hit by multiple distinct tool calls
	// (agent returned to it repeatedly = higher confidence).

	// First pass: count interactions per file and track best tool
	type fileInteraction struct {
		bestTool string
		hits     int
		bestTurn int
	}
	interactionMap := make(map[string]*fileInteraction)
	for _, ef := range exploredFiles {
		path := ef.path
		if allImplementedFiles[path] || planFileSet[path] {
			continue
		}
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		fi, ok := interactionMap[path]
		if !ok {
			fi = &fileInteraction{bestTool: ef.tool, bestTurn: ef.turn}
			interactionMap[path] = fi
		}
		fi.hits++
		// Upgrade tool rank: code/constructors > read > find
		toolRank := map[string]int{"code": 3, "constructors": 3, "read": 2, "find": 1}
		if toolRank[ef.tool] > toolRank[fi.bestTool] {
			fi.bestTool = ef.tool
		}
		if ef.turn < fi.bestTurn {
			fi.bestTurn = ef.turn
		}
	}

	// Second pass: score and add
	for path, fi := range interactionMap {
		// find()-only signals require 2+ hits to reduce noise in flat packages.
		// A file that appears in one find() is weak; appearing in multiple
		// searches means the agent kept encountering it.
		if fi.bestTool == "find" && fi.hits < 2 {
			continue
		}
		fullPath := filepath.Join(cwd, path)
		if _, err := os.Stat(fullPath); err != nil {
			continue
		}
		score := 0.30 // find() base
		switch fi.bestTool {
		case "code", "constructors":
			score = 0.50
		case "read":
			score = 0.45
		}
		// Multi-hit bonus: agent came back to this file
		if fi.hits >= 2 {
			score += 0.10
		}
		// Cap at 0.60 — exploration should never outscore implementation (0.85)
		if score > 0.60 {
			score = 0.60
		}
		agentFiles = append(agentFiles, AgentFile{
			Path:  path,
			Code:  "",
			Turn:  fi.bestTurn + 1,
			Lines: 0,
			Score: score,
		})
		if debugSpike {
			fmt.Fprintf(os.Stderr, "[spike] explored file: %s (tool=%s, hits=%d, score=%.2f)\n",
				path, fi.bestTool, fi.hits, score)
		}
	}

	// Collect definitions the agent looked up via code()
	var exploredDefs []ExploredDef
	for _, ef := range exploredFiles {
		if ef.tool == "code" && ef.defName != "" {
			exploredDefs = append(exploredDefs, ExploredDef{
				Name: ef.defName,
				File: ef.path,
			})
		}
	}

	return &AgentResult{
		Files:        agentFiles,
		FileBlocks:   allFileBlocks,
		ToolCalls:    totalToolCalls,
		ExploredDefs: exploredDefs,
		Cost:         cost,
	}, nil
}

func executeTool(name string, input map[string]interface{}, graph *refgraph.Graph, cwd string) string {
	switch name {
	case "code":
		defName, _ := input["name"].(string)
		return toolCode(defName, graph, cwd)
	case "impact":
		funcName, _ := input["name"].(string)
		return toolImpact(funcName, graph)
	case "constructors":
		typeName, _ := input["name"].(string)
		return toolConstructors(typeName, graph, cwd)
	case "read":
		path, _ := input["path"].(string)
		startLine := 0
		if sl, ok := input["start_line"].(float64); ok {
			startLine = int(sl)
		} else if sl, ok := input["start_line"].(string); ok {
			startLine, _ = strconv.Atoi(sl)
		}
		return toolRead(path, cwd, startLine)
	case "find":
		query, _ := input["query"].(string)
		return toolFind(query, cwd)
	case "list":
		path, _ := input["path"].(string)
		return toolList(path, cwd)
	default:
		return "unknown tool: " + name
	}
}

// extractAgentFileBlocks extracts file paths AND their code from agent output.
// Handles multiple formats: // FILE: headers, ```go code blocks, markdown headers.
// Returns map of file path → code content.
func extractAgentFileBlocks(text string) map[string]string {
	blocks := make(map[string]string)

	// Pattern 1: // FILE: path/to/file.go followed by code until next // FILE: or end
	fileHeaderRe := regexp.MustCompile(`(?m)^//\s*FILE:\s*(\S+\.go)\s*$`)
	indices := fileHeaderRe.FindAllStringSubmatchIndex(text, -1)
	for i, match := range indices {
		file := cleanGoPath(text[match[2]:match[3]])
		if file == "" {
			continue
		}
		codeStart := match[1]
		codeEnd := len(text)
		if i+1 < len(indices) {
			codeEnd = indices[i+1][0]
		}
		blocks[file] = strings.TrimSpace(text[codeStart:codeEnd])
	}

	// Pattern 2: ```go with file path in first line or preceding markdown header
	// Match: ```go\n// path/to/file.go  OR  **path/to/file.go**\n```go
	codeBlockRe := regexp.MustCompile("(?ms)(?:(?:\\*\\*|###?\\s+)([\\w/.-]+\\.go)\\*{0,2}\\s*\n)?```go\\s*\n(?://\\s*([\\w/.-]+\\.go)\\s*\n)?(.*?)```")
	for _, match := range codeBlockRe.FindAllStringSubmatch(text, -1) {
		file := ""
		if match[2] != "" {
			file = cleanGoPath(match[2]) // // path inside code block
		} else if match[1] != "" {
			file = cleanGoPath(match[1]) // markdown header before block
		}
		if file != "" {
			blocks[file] = strings.TrimSpace(match[3])
		}
	}

	// Pattern 3: standalone file references (no code block) — for file-listing fallback
	// Only add if we didn't already get the file from a code block
	fallbackPatterns := []*regexp.Regexp{
		regexp.MustCompile(`(?m)\*\*([\w/.-]+\.go)\*\*`),
		regexp.MustCompile(`(?m)^###?\s+([\w/.-]+\.go)`),
		regexp.MustCompile(`(?m)^[\-\s*]*(\S+\.go)\s*$`),
	}
	for _, re := range fallbackPatterns {
		for _, match := range re.FindAllStringSubmatch(text, -1) {
			file := cleanGoPath(match[1])
			if file != "" && blocks[file] == "" {
				blocks[file] = "" // file reference without code
			}
		}
	}

	return blocks
}

// cleanGoPath normalizes a Go file path, returning "" for invalid paths.
func cleanGoPath(f string) string {
	f = strings.TrimPrefix(f, "./")
	f = strings.Trim(f, "`* ")
	if !strings.HasSuffix(f, ".go") || strings.HasSuffix(f, "_test.go") {
		return ""
	}
	if !strings.Contains(f, "/") {
		return "" // bare filename like "main.go" — too ambiguous
	}
	return f
}

