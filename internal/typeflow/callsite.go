package typeflow

import (
	"go/ast"
	"go/parser"
	"go/token"
)

// CallSite is a concrete location where one function calls another.
type CallSite struct {
	File       string // relative path of the caller file
	CallerFunc string // function containing the call
	Line       int    // line number of the call
	Callee     string // function being called (may include selector)
	Args       int    // number of arguments passed
}

// FindCallSites parses a Go file and finds all call sites of target functions.
// targetFuncs maps function names to true (e.g., "SubmitPR", "NewCmdCreate").
// Matches both direct calls (Foo()) and selector calls (pkg.Foo(), recv.Foo()).
func FindCallSites(filePath string, targetFuncs map[string]bool) ([]CallSite, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filePath, nil, 0)
	if err != nil {
		return nil, err
	}

	var sites []CallSite

	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}

		callerName := fn.Name.Name
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}

			callee := calleeString(call.Fun)
			// Check if any part of the callee matches a target
			// "shared.SubmitPR" → check both "SubmitPR" and "shared.SubmitPR"
			matched := false
			if targetFuncs[callee] {
				matched = true
			} else {
				// Try just the final selector
				parts := splitSelector(callee)
				if len(parts) > 0 && targetFuncs[parts[len(parts)-1]] {
					matched = true
				}
			}

			if matched {
				sites = append(sites, CallSite{
					File:       filePath,
					CallerFunc: callerName,
					Line:       fset.Position(call.Pos()).Line,
					Callee:     callee,
					Args:       len(call.Args),
				})
			}
			return true
		})
	}

	return sites, nil
}

// calleeString extracts the callee name from a call expression.
// f() → "f", pkg.F() → "pkg.F", obj.Method() → "obj.Method"
func calleeString(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		prefix := calleeString(e.X)
		if prefix != "" {
			return prefix + "." + e.Sel.Name
		}
		return e.Sel.Name
	case *ast.IndexExpr:
		// Generic call: F[T]()
		return calleeString(e.X)
	case *ast.IndexListExpr:
		// Multi-type-param generic: F[T1, T2]()
		return calleeString(e.X)
	default:
		return ""
	}
}

func splitSelector(s string) []string {
	var parts []string
	start := 0
	for i, c := range s {
		if c == '.' {
			if i > start {
				parts = append(parts, s[start:i])
			}
			start = i + 1
		}
	}
	if start < len(s) {
		parts = append(parts, s[start:])
	}
	return parts
}
