// Package typeflow provides source-level Go analysis for verifying
// structural relationships between files. It parses actual Go source
// to find concrete call sites, replacing metadata-level approximations
// with verified code-level connections.
//
// Uses go/parser (syntax only, no type resolution) for speed.
// Trades perfect type accuracy for ~1ms/file parse times.
package typeflow

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"unicode"
)

// FuncSig is an exported function signature extracted from Go source.
type FuncSig struct {
	Name     string // exported function name
	Receiver string // receiver type (empty for package-level functions)
	File     string // source file path
	Line     int    // line number of declaration
}

// ParseExportedSigs extracts exported function/method signatures from a Go file.
// Only returns exported (capitalized) non-test functions.
func ParseExportedSigs(filePath string) ([]FuncSig, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filePath, nil, 0)
	if err != nil {
		return nil, err
	}

	var sigs []FuncSig
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name == nil {
			continue
		}
		name := fn.Name.Name
		if !isExported(name) || strings.HasPrefix(name, "Test") {
			continue
		}

		sig := FuncSig{
			Name: name,
			File: filePath,
			Line: fset.Position(fn.Pos()).Line,
		}

		if fn.Recv != nil && len(fn.Recv.List) > 0 {
			sig.Receiver = exprString(fn.Recv.List[0].Type)
		}

		sigs = append(sigs, sig)
	}
	return sigs, nil
}

func isExported(name string) bool {
	if name == "" {
		return false
	}
	return unicode.IsUpper(rune(name[0]))
}

func exprString(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.StarExpr:
		return "*" + exprString(t.X)
	case *ast.Ident:
		return t.Name
	case *ast.SelectorExpr:
		return exprString(t.X) + "." + t.Sel.Name
	default:
		return ""
	}
}
