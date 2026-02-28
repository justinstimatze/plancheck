// agenttools.go implements the tool functions used by the agent spike.
//
// Each tool provides a specific capability:
//   - impact(name): callers, transitive callers, test coverage
//   - constructors(name): struct construction sites
//   - code(name): read specific definition by name
//   - read(path): read a file
//   - find(query): search codebase for a string
//   - list(path): directory listing
package simulate

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/justinstimatze/plancheck/internal/refgraph"
)

func toolImpact(name string, graph *refgraph.Graph) string {
	if graph == nil {
		return "no reference graph available"
	}
	def := graph.GetDef(name, "")
	if def == nil {
		return fmt.Sprintf("%s: not found in reference graph", name)
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("=== %s ===\n", def.FullName()))
	b.WriteString(fmt.Sprintf("File: %s\n", def.SourceFile))
	b.WriteString(fmt.Sprintf("Kind: %s\n", def.Kind))
	if def.Signature != "" {
		b.WriteString(fmt.Sprintf("Signature: %s\n", def.Signature))
	}

	callers := graph.CallerDefs(def.ID)
	b.WriteString(fmt.Sprintf("\nDirect callers (%d):\n", len(callers)))
	for i, c := range callers {
		if i >= 15 {
			b.WriteString(fmt.Sprintf("  ... and %d more\n", len(callers)-15))
			break
		}
		tag := ""
		if c.Test {
			tag = " [test]"
		}
		b.WriteString(fmt.Sprintf("  %s in %s%s\n", c.FullName(), c.SourceFile, tag))
	}

	tests := graph.Tests(def.ID)
	if len(tests) > 0 {
		b.WriteString(fmt.Sprintf("\nTest coverage (%d tests):\n", len(tests)))
		for i, t := range tests {
			if i >= 5 {
				b.WriteString(fmt.Sprintf("  ... and %d more\n", len(tests)-5))
				break
			}
			b.WriteString(fmt.Sprintf("  %s in %s\n", t.Name, t.SourceFile))
		}
	}

	callees := graph.CalleeDefs(def.ID)
	if len(callees) > 0 {
		b.WriteString(fmt.Sprintf("\nCalls (%d):\n", len(callees)))
		for i, c := range callees {
			if i >= 10 {
				b.WriteString(fmt.Sprintf("  ... and %d more\n", len(callees)-10))
				break
			}
			b.WriteString(fmt.Sprintf("  %s in %s\n", c.FullName(), c.SourceFile))
		}
	}

	return b.String()
}

// toolConstructors finds all places that construct a struct type.
// Combines graph callers (precise) with grep for struct literals (catches ungraphed sites).
func toolConstructors(name string, graph *refgraph.Graph, cwd string) string {
	if graph == nil {
		return "no reference graph available"
	}

	def := graph.GetDef(name, "")
	if def == nil {
		return fmt.Sprintf("%s: not found in reference graph", name)
	}
	if def.Kind != "type" {
		return fmt.Sprintf("%s is a %s, not a struct type. Use impact() for functions.", name, def.Kind)
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("=== Constructors of %s ===\n", name))
	b.WriteString(fmt.Sprintf("Defined in: %s\n\n", def.SourceFile))

	// 1. Graph constructors — functions that construct this type
	callers := graph.Constructors(def.ID)
	if len(callers) == 0 {
		// Fallback to all callers if Constructors returns nothing
		callers = graph.CallerDefs(def.ID)
	}
	var prodCallers, testCallers []*refgraph.Def
	for _, c := range callers {
		if c.Test {
			testCallers = append(testCallers, c)
		} else {
			prodCallers = append(prodCallers, c)
		}
	}

	b.WriteString(fmt.Sprintf("Production callers (%d):\n", len(prodCallers)))
	for i, c := range prodCallers {
		if i >= 15 {
			b.WriteString(fmt.Sprintf("  ... and %d more\n", len(prodCallers)-15))
			break
		}
		b.WriteString(fmt.Sprintf("  %s in %s\n", c.FullName(), c.SourceFile))
	}

	if len(testCallers) > 0 {
		b.WriteString(fmt.Sprintf("\nTest callers (%d):\n", len(testCallers)))
		for i, c := range testCallers {
			if i >= 5 {
				b.WriteString(fmt.Sprintf("  ... and %d more\n", len(testCallers)-5))
				break
			}
			b.WriteString(fmt.Sprintf("  %s in %s\n", c.FullName(), c.SourceFile))
		}
	}

	// 2. Grep for struct literal construction: TypeName{
	cmd := exec.Command("grep", "-rn", "-F", "--include=*.go", name+"{")
	cmd.Dir = cwd
	out, err := cmd.Output()
	if err == nil {
		grepLines := strings.Split(strings.TrimSpace(string(out)), "\n")
		// Filter to non-test files, deduplicate by file
		seen := make(map[string]bool)
		var literals []string
		for _, line := range grepLines {
			if strings.Contains(line, "_test.go") {
				continue
			}
			if idx := strings.Index(line, ":"); idx > 0 {
				file := line[:idx]
				if !seen[file] {
					seen[file] = true
					literals = append(literals, line)
				}
			}
		}
		if len(literals) > 0 {
			b.WriteString(fmt.Sprintf("\nStruct literal sites (%d files):\n", len(literals)))
			for i, l := range literals {
				if i >= 10 {
					b.WriteString(fmt.Sprintf("  ... and %d more\n", len(literals)-10))
					break
				}
				b.WriteString(fmt.Sprintf("  %s\n", l))
			}
		}
	}

	return b.String()
}

// resolveSourceFile maps a defn SourceFile (bare filename like "accounts.go")
// to a relative path (like "server/accounts.go") using the module path from the graph.
func resolveSourceFile(def *refgraph.Def, graph *refgraph.Graph, cwd string) string {
	sourceFile := def.SourceFile
	// If the bare filename exists at cwd, use it directly
	if _, err := os.Stat(filepath.Join(cwd, sourceFile)); err == nil {
		return sourceFile
	}
	// Otherwise, resolve via module path: strip the go.mod module prefix
	// to get the directory, then join with the filename.
	if def.ModuleID != 0 {
		modPath := graph.ModulePath(def.ModuleID)
		if modPath != "" {
			// Read go.mod to get the module root
			gomodData, err := os.ReadFile(filepath.Join(cwd, "go.mod"))
			if err == nil {
				for _, line := range strings.Split(string(gomodData), "\n") {
					line = strings.TrimSpace(line)
					if strings.HasPrefix(line, "module ") {
						modRoot := strings.TrimSpace(strings.TrimPrefix(line, "module"))
						// modPath = "github.com/nats-io/nats-server/v2/server"
						// modRoot = "github.com/nats-io/nats-server/v2"
						// dir = "server"
						dir := strings.TrimPrefix(modPath, modRoot)
						dir = strings.TrimPrefix(dir, "/")
						if dir != "" {
							relPath := dir + "/" + sourceFile
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
	return "" // couldn't resolve
}

// toolCode reads a specific definition's source code by name.
// Uses the graph to find the file, then go/ast to extract exact source lines.
func toolCode(name string, graph *refgraph.Graph, cwd string) string {
	if graph == nil {
		return "no reference graph available — use read() instead"
	}

	// Handle Receiver.Method syntax
	def := graph.GetDef(name, "")
	if def == nil {
		return fmt.Sprintf("%s: not found in reference graph. Try find() to search.", name)
	}

	if def.SourceFile == "" {
		return fmt.Sprintf("%s: found but no source file recorded", name)
	}

	// Find the Go file on disk
	absPath := filepath.Join(cwd, def.SourceFile)
	data, err := os.ReadFile(absPath)
	if err != nil {
		return fmt.Sprintf("error reading %s: %v", def.SourceFile, err)
	}

	lines := strings.Split(string(data), "\n")

	var startLine, endLine int

	// Use defn's StartLine/EndLine when available (fast, no parsing)
	if def.StartLine > 0 && def.EndLine > 0 {
		startLine = def.StartLine
		endLine = def.EndLine
	} else {
		// Fallback: parse with go/ast to find exact source range
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, absPath, data, parser.ParseComments)
		if err != nil {
			return toolCodeFallback(name, def, data)
		}

		targetName := name
		if idx := strings.LastIndex(name, "."); idx >= 0 {
			targetName = name[idx+1:]
		}

		for _, decl := range file.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if d.Name.Name == targetName {
					if def.Receiver != "" && d.Recv != nil {
						startLine = fset.Position(d.Pos()).Line
						endLine = fset.Position(d.End()).Line
						break
					} else if def.Receiver == "" && d.Recv == nil {
						startLine = fset.Position(d.Pos()).Line
						endLine = fset.Position(d.End()).Line
						break
					} else if d.Recv != nil || def.Receiver != "" {
						continue
					}
					startLine = fset.Position(d.Pos()).Line
					endLine = fset.Position(d.End()).Line
				}
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					switch s := spec.(type) {
					case *ast.TypeSpec:
						if s.Name.Name == targetName {
							startLine = fset.Position(d.Pos()).Line
							endLine = fset.Position(d.End()).Line
						}
					case *ast.ValueSpec:
						for _, n := range s.Names {
							if n.Name == targetName {
								startLine = fset.Position(d.Pos()).Line
								endLine = fset.Position(d.End()).Line
							}
						}
					}
				}
			}
			if startLine > 0 {
				break
			}
		}

		if startLine == 0 {
			return toolCodeFallback(name, def, data)
		}
	}

	// Include doc comment lines above the declaration
	for i := startLine - 2; i >= 0 && i < len(lines); i-- {
		trimmed := strings.TrimSpace(lines[i])
		if strings.HasPrefix(trimmed, "//") {
			startLine = i + 1
		} else {
			break
		}
	}

	// Extract source lines
	if endLine > len(lines) {
		endLine = len(lines)
	}
	src := strings.Join(lines[startLine-1:endLine], "\n")

	var b strings.Builder
	b.WriteString(fmt.Sprintf("// %s — %s in %s (lines %d-%d of %d)\n",
		def.FullName(), def.Kind, def.SourceFile, startLine, endLine, len(lines)))
	if def.Signature != "" {
		b.WriteString(fmt.Sprintf("// Signature: %s\n", def.Signature))
	}
	b.WriteString(src)

	// Cap output at 200 lines to stay within context budget
	outLines := strings.Split(b.String(), "\n")
	if len(outLines) > 200 {
		return strings.Join(outLines[:200], "\n") + "\n// ... (definition truncated at 200 lines)"
	}

	return b.String()
}

// toolCodeFallback uses simple text search when AST parsing fails.
func toolCodeFallback(name string, def *refgraph.Def, data []byte) string {
	lines := strings.Split(string(data), "\n")
	targetName := name
	if idx := strings.LastIndex(name, "."); idx >= 0 {
		targetName = name[idx+1:]
	}

	// Find the line containing the definition
	var startIdx int
	found := false
	for i, line := range lines {
		if strings.Contains(line, "func ") && strings.Contains(line, targetName) {
			startIdx = i
			found = true
			break
		}
		if strings.Contains(line, "type "+targetName+" ") {
			startIdx = i
			found = true
			break
		}
	}
	if !found {
		return fmt.Sprintf("%s found in graph (%s in %s) but could not locate source. Use read(\"%s\") and find(\"%s\").",
			name, def.Kind, def.SourceFile, def.SourceFile, targetName)
	}

	// Return up to 100 lines from the start
	endIdx := startIdx + 100
	if endIdx > len(lines) {
		endIdx = len(lines)
	}
	return fmt.Sprintf("// %s in %s (line %d)\n%s",
		def.FullName(), def.SourceFile, startIdx+1,
		strings.Join(lines[startIdx:endIdx], "\n"))
}

// extractFilesFromFindResult extracts unique .go file paths from grep -rn output.
// Returns the top 3 most-matched non-test files (files with the most matches are
// most likely relevant to the agent's search).
func extractFilesFromFindResult(result string) []string {
	counts := make(map[string]int)
	for _, line := range strings.Split(result, "\n") {
		// grep -rn format: "file.go:123:matched line"
		if idx := strings.Index(line, ":"); idx > 0 {
			file := line[:idx]
			if strings.HasSuffix(file, ".go") && !strings.HasSuffix(file, "_test.go") {
				counts[file]++
			}
		}
	}
	// Sort by match count, return top 3
	type fc struct {
		file  string
		count int
	}
	var sorted []fc
	for f, c := range counts {
		sorted = append(sorted, fc{f, c})
	}
	for i := 0; i < len(sorted); i++ {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[j].count > sorted[i].count {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}
	var result2 []string
	for i, s := range sorted {
		if i >= 3 {
			break
		}
		result2 = append(result2, s.file)
	}
	return result2
}

// extractExportedDefNames finds exported function/type/method names in Go source.
// Returns names that aren't visible in the first 300 lines (i.e., the agent hasn't seen them).
func extractExportedDefNames(code string) []string {
	lines := strings.Split(code, "\n")
	seen := make(map[string]bool)
	var names []string

	exportedRe := regexp.MustCompile(`(?m)^(?:func\s+(?:\([^)]*\)\s+)?([A-Z]\w*)|type\s+([A-Z]\w*)\s)`)
	for i, line := range lines {
		if i < 300 {
			continue // skip lines the agent already saw
		}
		for _, match := range exportedRe.FindAllStringSubmatch(line, -1) {
			name := match[1]
			if name == "" {
				name = match[2]
			}
			if name != "" && !seen[name] {
				seen[name] = true
				names = append(names, name)
			}
		}
	}
	return names
}

func toolRead(path, cwd string, startLine int) string {
	absPath := filepath.Join(cwd, path)
	data, err := os.ReadFile(absPath)
	if err != nil {
		return fmt.Sprintf("error reading %s: %v", path, err)
	}
	allLines := strings.Split(string(data), "\n")
	totalLines := len(allLines)
	const readLimit = 300

	// Apply start_line offset (1-based)
	if startLine > 1 {
		offset := startLine - 1
		if offset >= totalLines {
			return fmt.Sprintf("%s has %d lines, start_line %d is past end of file", path, totalLines, startLine)
		}
		allLines = allLines[offset:]
	}

	if len(allLines) > readLimit {
		endLine := readLimit
		if startLine > 1 {
			endLine = startLine - 1 + readLimit
		} else {
			endLine = readLimit
		}
		allLines = allLines[:readLimit]
		truncMsg := fmt.Sprintf("\n// ... (truncated at line %d of %d total)", endLine, totalLines)

		// For large files, suggest code() with exported definition names
		if totalLines > 500 {
			defs := extractExportedDefNames(string(data))
			if len(defs) > 0 {
				if len(defs) > 5 {
					defs = defs[:5]
				}
				truncMsg += fmt.Sprintf("\n// Tip: use code(\"%s\") to jump to a specific definition", strings.Join(defs, "\"), code(\""))
			}
		}

		return strings.Join(allLines, "\n") + truncMsg
	}
	return strings.Join(allLines, "\n")
}

func toolFind(query, cwd string) string {
	// Show matching lines with file:line context so the agent can see what it found
	cmd := exec.Command("grep", "-rn", "-F", "--include=*.go", query)
	cmd.Dir = cwd
	out, err := cmd.Output()
	if err != nil {
		return "no matches found"
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) > 30 {
		return strings.Join(lines[:30], "\n") + fmt.Sprintf("\n... and %d more matches", len(lines)-30)
	}
	return strings.Join(lines, "\n")
}

func toolList(path, cwd string) string {
	absDir := filepath.Join(cwd, path)
	entries, err := os.ReadDir(absDir)
	if err != nil {
		return fmt.Sprintf("error listing %s: %v", path, err)
	}
	var lines []string
	for _, e := range entries {
		if e.IsDir() {
			lines = append(lines, e.Name()+"/")
		} else if strings.HasSuffix(e.Name(), ".go") && !strings.HasSuffix(e.Name(), "_test.go") {
			// Show line count so agent knows when to use code() vs read()
			info, err := e.Info()
			if err == nil && info.Size() > 0 {
				absFile := filepath.Join(absDir, e.Name())
				if data, err := os.ReadFile(absFile); err == nil {
					lineCount := len(strings.Split(string(data), "\n"))
					lines = append(lines, fmt.Sprintf("%s (%d lines)", e.Name(), lineCount))
					continue
				}
			}
			lines = append(lines, e.Name())
		}
	}
	return strings.Join(lines, "\n")
}
