// structdiff.go extracts struct field changes from the spike's implementation
// and finds files that construct or access those structs.
//
// When the spike adds `Draft bool` to `CreateOptions`, every file that
// constructs `CreateOptions{}` or accesses `.Draft` needs updating.
// This traces data flow through struct fields — the connection that
// function call analysis misses.
package simulate

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"

	"github.com/justinstimatze/plancheck/internal/typeflow"
)

// StructDiff represents a struct that was modified by the spike.
type StructDiff struct {
	TypeName  string   // "CreateOptions"
	NewFields []string // field names added by spike
	File      string   // which plan file contains this struct
}

// DiffStructs compares struct definitions between spike code and the original file.
// Returns structs where the spike added new fields.
func DiffStructs(spikeCode string, originalPath string) []StructDiff {
	spikeStructs := extractStructFields(spikeCode)
	if len(spikeStructs) == 0 {
		return nil
	}

	originalData, err := os.ReadFile(originalPath)
	if err != nil {
		return nil
	}
	originalStructs := extractStructFields(string(originalData))

	var diffs []StructDiff
	for typeName, spikeFields := range spikeStructs {
		origFields := originalStructs[typeName]
		origSet := make(map[string]bool)
		for _, f := range origFields {
			origSet[f] = true
		}

		var newFields []string
		for _, f := range spikeFields {
			if !origSet[f] {
				newFields = append(newFields, f)
			}
		}

		if len(newFields) > 0 {
			diffs = append(diffs, StructDiff{
				TypeName:  typeName,
				NewFields: newFields,
				File:      originalPath,
			})
		}
	}
	return diffs
}

// extractStructFields parses Go source and returns map of struct name → field names.
func extractStructFields(src string) map[string][]string {
	wrapped := src
	if !strings.Contains(src, "package ") {
		wrapped = "package spike\n\n" + src
	}

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "src.go", wrapped, parser.SkipObjectResolution)
	if err != nil {
		return nil
	}

	result := make(map[string][]string)
	ast.Inspect(f, func(n ast.Node) bool {
		gd, ok := n.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			return true
		}
		for _, spec := range gd.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok || st.Fields == nil {
				continue
			}
			name := ts.Name.Name
			if name == "" || name[0] < 'A' || name[0] > 'Z' {
				continue // unexported
			}
			var fields []string
			for _, field := range st.Fields.List {
				for _, ident := range field.Names {
					fields = append(fields, ident.Name)
				}
			}
			result[name] = fields
		}
		return true
	})
	return result
}

// FindStructConstructors searches Go files in the codebase for struct literal
// constructors of the given type name. Returns file paths that contain
// `TypeName{...}` composite literals.
func FindStructConstructors(typeName string, cwd string) []string {
	// Use the file index to search efficiently
	index := typeflow.ResolveGoFiles(nil, cwd) // just to warm the cache
	_ = index

	var files []string
	seen := make(map[string]bool)

	// Walk the codebase looking for composite literals of this type
	filepath.WalkDir(cwd, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if strings.HasPrefix(name, ".") || name == "vendor" || name == "testdata" || name == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") || strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}

		rel, err := filepath.Rel(cwd, path)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)

		if hasStructUsage(path, typeName) && !seen[rel] {
			seen[rel] = true
			files = append(files, rel)
		}
		return nil
	})

	return files
}

// hasStructUsage checks if a Go file contains composite literals or field
// access for the given struct type name.
func hasStructUsage(filePath, typeName string) bool {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filePath, nil, parser.SkipObjectResolution)
	if err != nil {
		return false
	}

	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		if found {
			return false
		}
		switch x := n.(type) {
		case *ast.CompositeLit:
			// Check for TypeName{...} struct literals
			if ident, ok := x.Type.(*ast.Ident); ok && ident.Name == typeName {
				found = true
			}
			if sel, ok := x.Type.(*ast.SelectorExpr); ok && sel.Sel.Name == typeName {
				found = true
			}
		case *ast.SelectorExpr:
			// Selector access (.FieldName) could indicate struct usage, but
			// name-based matching without go/types is too noisy.
			// Only composite literals above are matched for precision.
			_ = x
		}
		return true
	})

	return found
}
