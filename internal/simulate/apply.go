// apply.go applies spike-generated code to the reference graph via a
// throwaway dolt branch, then queries for consequences.
//
// This is the real simulation: mutate the graph to reflect the spike's
// implementation, then ask "what changed? what broke? what's new?"
// The dolt branch is discarded after querying — zero permanent cost.
package simulate

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"

	"github.com/justinstimatze/plancheck/internal/refgraph"
	"github.com/justinstimatze/plancheck/internal/typeflow"
)

// SimulationResult is the output of applying the spike to the graph.
type SimulationResult struct {
	NewRefs      []string // new function references the spike introduced
	BrokenCallers []string // files whose calls to modified functions may break
	NewFiles     []string // files the spike touched that aren't in the plan
}

// ApplySpike takes the spike's file blocks, parses them with go/parser,
// compares against the original graph, and returns the consequences.
//
// Instead of using a dolt branch (which requires write access to .defn),
// we do the comparison in-memory: parse the spike's code for function
// signatures and calls, compare against the graph's current state,
// and report the delta.
func ApplySpike(fileBlocks map[string]string, graph *refgraph.Graph, planFiles []string, cwd string) *SimulationResult {
	if graph == nil || len(fileBlocks) == 0 {
		return nil
	}

	planFileSet := make(map[string]bool)
	for _, pf := range planFiles {
		planFileSet[pf] = true
		planFileSet[filepath.Base(pf)] = true
	}

	result := &SimulationResult{}

	// Struct field analysis: find new fields the spike added to structs,
	// then find files that construct those structs.
	for file, code := range fileBlocks {
		absFile := filepath.Join(cwd, file)
		if _, err := os.Stat(absFile); err != nil {
			continue
		}
		diffs := DiffStructs(code, absFile)
		for _, diff := range diffs {
			constructors := FindStructConstructors(diff.TypeName, cwd)
			for _, cf := range constructors {
				if !planFileSet[cf] && !planFileSet[filepath.Base(cf)] {
					result.NewRefs = append(result.NewRefs, cf)
				}
			}
		}
	}
	allNewRefs := make(map[string]bool)
	touchedFiles := make(map[string]bool)

	for file, code := range fileBlocks {
		touchedFiles[file] = true

		// Parse the spike's code with go/parser
		fset := token.NewFileSet()
		src := "package spike\n\n" + code
		f, err := parser.ParseFile(fset, "spike.go", src, parser.AllErrors|parser.SkipObjectResolution)
		if err != nil {
			// Partial parse is fine — extract what we can
			f, _ = parser.ParseFile(fset, "spike.go", src, parser.SkipObjectResolution)
		}
		if f == nil {
			continue
		}

		// Extract all function calls from the spike's code
		spikeCalls := make(map[string]bool)
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			name := callExprName(call.Fun)
			if name != "" && len(name) >= 2 {
				spikeCalls[name] = true
			}
			return true
		})

		// Extract function calls from the original file
		base := filepath.Base(file)
		originalCalls := make(map[string]bool)
		if !planFileSet[file] {
			absPath := filepath.Join(cwd, file)
			origF, err := parser.ParseFile(token.NewFileSet(), absPath, nil, parser.SkipObjectResolution)
			if err == nil {
				ast.Inspect(origF, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					name := callExprName(call.Fun)
					if name != "" {
						originalCalls[name] = true
					}
					return true
				})
			}
		}

		// Find NEW calls: in spike but not in original
		for call := range spikeCalls {
			if !originalCalls[call] {
				// This is a new function call the spike introduced
				// Resolve it via the graph
				funcName := call
				if strings.Contains(funcName, ".") {
					parts := strings.Split(funcName, ".")
					funcName = parts[len(parts)-1]
				}
				if funcName == "" || funcName[0] < 'A' || funcName[0] > 'Z' {
					continue // unexported or empty
				}
				allNewRefs[funcName] = true
			}
		}

		// If this is a plan file and the spike changed it, find functions
		// whose signatures might have changed (new parameters, etc.)
		if planFileSet[file] || planFileSet[base] {
			// Extract function signatures from spike version
			for _, decl := range f.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Name == nil {
					continue
				}
				name := fn.Name.Name
				if name[0] < 'A' || name[0] > 'Z' {
					continue // unexported
				}
				// Check if this function exists in the graph with callers
				def := graph.GetDef(name, "")
				if def == nil {
					continue
				}
				callers := graph.CallerDefs(def.ID)
				for _, caller := range callers {
					if caller.Test || caller.SourceFile == "" || planFileSet[caller.SourceFile] {
						continue
					}
					result.BrokenCallers = append(result.BrokenCallers, caller.SourceFile)
				}
			}
		}
	}

	// Resolve new references to files
	for ref := range allNewRefs {
		def := graph.GetDef(ref, "")
		if def == nil || def.SourceFile == "" || planFileSet[def.SourceFile] || touchedFiles[def.SourceFile] {
			continue
		}
		// Resolve basename to full path
		resolved := typeflow.ResolveGoFiles(map[string]bool{def.SourceFile: true}, cwd)
		for _, paths := range resolved {
			for _, p := range paths {
				if !planFileSet[p] && !touchedFiles[p] {
					result.NewRefs = append(result.NewRefs, p)
				}
			}
		}
	}

	// Resolve broken callers to full paths
	brokenSet := make(map[string]bool)
	for _, sf := range result.BrokenCallers {
		resolved := typeflow.ResolveGoFiles(map[string]bool{sf: true}, cwd)
		for _, paths := range resolved {
			for _, p := range paths {
				if !planFileSet[p] && !touchedFiles[p] {
					brokenSet[p] = true
				}
			}
		}
	}
	result.BrokenCallers = nil
	for p := range brokenSet {
		result.BrokenCallers = append(result.BrokenCallers, p)
	}

	// Files the spike touched
	for file := range touchedFiles {
		if !planFileSet[file] {
			result.NewFiles = append(result.NewFiles, file)
		}
	}

	return result
}

func callExprName(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		prefix := callExprName(e.X)
		if prefix != "" {
			return prefix + "." + e.Sel.Name
		}
		return e.Sel.Name
	default:
		return ""
	}
}

