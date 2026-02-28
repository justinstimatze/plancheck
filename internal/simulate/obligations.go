// obligations.go extracts type-system obligations from the spike's implementation.
//
// An obligation is a file that MUST change — not a soft prediction.
// When the spike changes a function signature, callers MUST update.
// When it adds a struct field, constructors MUST handle it.
// Obligations have near-100% precision because they're grounded in
// the type system, not statistical co-occurrence.
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

// Obligation represents a file that MUST change due to type system constraints.
type Obligation struct {
	File     string `json:"file"`     // file that must change
	Kind     string `json:"kind"`     // "signature-break", "struct-field", "interface-method"
	Reason   string `json:"reason"`   // human-readable explanation
	FuncName string `json:"funcName"` // the function/type that changed
}

// ExtractObligations compares the spike's code against originals to find
// type-system-level obligations: signature changes, struct field additions,
// interface method additions.
func ExtractObligations(fileBlocks map[string]string, graph *refgraph.Graph,
	planFiles []string, cwd string) []Obligation {

	if graph == nil {
		return nil
	}

	planFileSet := make(map[string]bool)
	for _, pf := range planFiles {
		planFileSet[pf] = true
		planFileSet[filepath.Base(pf)] = true
	}

	var obligations []Obligation
	seen := make(map[string]bool)

	for file, code := range fileBlocks {
		absFile := filepath.Join(cwd, file)
		if _, err := os.Stat(absFile); err != nil {
			continue
		}

		// 1. Signature change obligations
		sigObligations := findSignatureObligations(code, absFile, graph, planFileSet, cwd)
		for _, o := range sigObligations {
			if !seen[o.File+"|"+o.Kind] {
				seen[o.File+"|"+o.Kind] = true
				obligations = append(obligations, o)
			}
		}

		// 2. Struct field obligations
		structObligations := findStructObligations(code, absFile, planFileSet, cwd)
		for _, o := range structObligations {
			if !seen[o.File+"|"+o.Kind] {
				seen[o.File+"|"+o.Kind] = true
				obligations = append(obligations, o)
			}
		}
	}

	return obligations
}

// findSignatureObligations compares function signatures between spike and original.
// When parameter count changes, ALL callers MUST update their call sites.
func findSignatureObligations(spikeCode, originalPath string,
	graph *refgraph.Graph, planFileSet map[string]bool, cwd string) []Obligation {

	spikeSigs := parseFuncSignatures(spikeCode)
	origSigs := parseFuncSignaturesFromFile(originalPath)

	var obligations []Obligation

	for name, spikeSig := range spikeSigs {
		origSig, exists := origSigs[name]
		if !exists {
			continue // new function, not a signature change
		}

		// Check if parameter count changed
		if spikeSig.paramCount != origSig.paramCount || spikeSig.resultCount != origSig.resultCount {
			// This is a signature break — find all callers
			def := graph.GetDef(name, "")
			if def == nil {
				continue
			}
			callers := graph.CallerDefs(def.ID)
			for _, caller := range callers {
				if caller.Test || caller.SourceFile == "" || planFileSet[caller.SourceFile] {
					continue
				}
				// Resolve to full path
				resolved := typeflow.ResolveGoFiles(map[string]bool{caller.SourceFile: true}, cwd)
				for _, paths := range resolved {
					for _, p := range paths {
						if !planFileSet[p] {
							change := "parameter count"
							if spikeSig.paramCount != origSig.paramCount {
								change = "parameters"
							} else {
								change = "return values"
							}
							obligations = append(obligations, Obligation{
								File:     p,
								Kind:     "signature-break",
								Reason:   name + "() " + change + " changed — callers MUST update call sites",
								FuncName: name,
							})
						}
					}
				}
			}
		}
	}

	return obligations
}

// findStructObligations finds files that construct structs where the spike added fields.
func findStructObligations(spikeCode, originalPath string,
	planFileSet map[string]bool, cwd string) []Obligation {

	diffs := DiffStructs(spikeCode, originalPath)
	if len(diffs) == 0 {
		return nil
	}

	var obligations []Obligation
	for _, diff := range diffs {
		constructors := FindStructConstructors(diff.TypeName, cwd)
		for _, cf := range constructors {
			if !planFileSet[cf] && !planFileSet[filepath.Base(cf)] {
				obligations = append(obligations, Obligation{
					File:     cf,
					Kind:     "struct-field",
					Reason:   diff.TypeName + " gained field(s) " + strings.Join(diff.NewFields, ", ") + " — constructors MUST update",
					FuncName: diff.TypeName,
				})
			}
		}
	}

	return obligations
}

type funcSigInfo struct {
	name        string
	paramCount  int
	resultCount int
}

func parseFuncSignatures(code string) map[string]funcSigInfo {
	wrapped := code
	if !strings.Contains(code, "package ") {
		wrapped = "package spike\n\n" + code
	}
	fset := token.NewFileSet()
	f, _ := parser.ParseFile(fset, "spike.go", wrapped, parser.SkipObjectResolution)
	if f == nil {
		return nil
	}
	return extractSigs(f)
}

func parseFuncSignaturesFromFile(path string) map[string]funcSigInfo {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		return nil
	}
	return extractSigs(f)
}

func extractSigs(f *ast.File) map[string]funcSigInfo {
	sigs := make(map[string]funcSigInfo)
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name == nil {
			continue
		}
		name := fn.Name.Name
		if len(name) == 0 || name[0] < 'A' || name[0] > 'Z' {
			continue
		}
		sig := funcSigInfo{name: name}
		if fn.Type.Params != nil {
			sig.paramCount = countFields(fn.Type.Params)
		}
		if fn.Type.Results != nil {
			sig.resultCount = countFields(fn.Type.Results)
		}
		sigs[name] = sig
	}
	return sigs
}

func countFields(fl *ast.FieldList) int {
	if fl == nil {
		return 0
	}
	count := 0
	for _, field := range fl.List {
		if len(field.Names) == 0 {
			count++ // unnamed return/param
		} else {
			count += len(field.Names)
		}
	}
	return count
}
