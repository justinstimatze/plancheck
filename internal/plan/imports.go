package plan

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
)

// importChainFile represents a file discovered through Go import relationships.
type importChainFile struct {
	file     string // basename
	fullPath string // relative path from cwd
	dir      string // "import" (dependency) or "importer" (dependent)
	planFile string // which plan file triggered this
	refCount int    // function-level coupling density (importers only)
}

// findImportChainFiles analyzes Go import relationships for plan files.
// Returns files from imported packages (dependencies) and importing packages (dependents).
func findImportChainFiles(planFiles []string, cwd string) []importChainFile {
	// Read the module path from go.mod
	modPath := readModulePath(cwd)
	if modPath == "" {
		return nil
	}

	// For each plan file, find its package's imports and reverse imports.
	// Collect separately so both directions get fair representation.
	var imports []importChainFile
	var importers []importChainFile
	seen := make(map[string]bool)

	// Collect plan package dirs to avoid suggesting files already in the plan
	planPkgDirs := make(map[string]bool)
	for _, f := range planFiles {
		var dir string
		if filepath.IsAbs(f) {
			dir = filepath.Dir(f)
		} else {
			dir = filepath.Dir(f)
		}
		planPkgDirs[dir] = true
	}

	for _, f := range planFiles {
		if !strings.HasSuffix(f, ".go") {
			continue
		}

		var absDir string
		var relDir string
		if filepath.IsAbs(f) {
			absDir = filepath.Dir(f)
			rel, err := filepath.Rel(cwd, absDir)
			if err != nil {
				continue
			}
			relDir = filepath.ToSlash(rel)
		} else {
			relDir = filepath.ToSlash(filepath.Dir(f))
			absDir = filepath.Join(cwd, relDir)
		}

		// Parse imports from all .go files in this package directory
		internalImports := collectInternalImports(absDir, modPath)

		for _, imp := range internalImports {
			// Map import path to relative directory
			pkgRelDir := strings.TrimPrefix(imp, modPath+"/")
			if pkgRelDir == imp {
				continue // not a sub-package of this module
			}
			if planPkgDirs[pkgRelDir] {
				continue // already in plan
			}

			// Find .go files in the imported package
			pkgAbsDir := filepath.Join(cwd, pkgRelDir)
			goFiles := listNonTestGoFiles(pkgAbsDir)
			for _, gf := range goFiles {
				rel := filepath.ToSlash(filepath.Join(pkgRelDir, gf))
				if seen[rel] {
					continue
				}
				seen[rel] = true
				imports = append(imports, importChainFile{
					file:     gf,
					fullPath: rel,
					dir:      "import",
					planFile: filepath.Base(f),
				})
			}
		}

		// Reverse direction: find packages that import this plan file's package
		planPkgImportPath := modPath + "/" + relDir
		importerDirs := findImporters(cwd, modPath, planPkgImportPath, planPkgDirs)
		for _, imp := range importerDirs {
			pkgAbsDir := filepath.Join(cwd, imp)
			goFiles := listNonTestGoFiles(pkgAbsDir)
			for _, gf := range goFiles {
				rel := filepath.ToSlash(filepath.Join(imp, gf))
				if seen[rel] {
					continue
				}
				seen[rel] = true
				importers = append(importers, importChainFile{
					file:     gf,
					fullPath: rel,
					dir:      "importer",
					planFile: filepath.Base(f),
				})
			}
		}
	}

	// Cap each direction separately, then combine.
	// Importers collect up to 50 for coupling-based ranking in check.go;
	// scoreImporterCoupling trims to 10 after scoring.
	if len(imports) > 20 {
		imports = imports[:20]
	}
	if len(importers) > 50 {
		importers = importers[:50]
	}

	return append(imports, importers...)
}

// readModulePath reads the module path from go.mod in the given directory.
func readModulePath(cwd string) string {
	data, err := os.ReadFile(filepath.Join(cwd, "go.mod"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module "))
		}
	}
	return ""
}

// collectInternalImports parses all .go files in a directory and returns
// import paths that are internal to the project (not stdlib, not external).
func collectInternalImports(dir, modPath string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	seen := make(map[string]bool)
	var imports []string
	fset := token.NewFileSet()

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, e.Name()), nil, parser.ImportsOnly)
		if err != nil {
			continue
		}
		for _, imp := range f.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			if strings.HasPrefix(path, modPath+"/") && !seen[path] {
				seen[path] = true
				imports = append(imports, path)
			}
		}
	}
	return imports
}

// findImporters walks the project tree to find packages that import the given package.
// Returns relative directory paths of importing packages.
func findImporters(cwd, modPath, targetImport string, skipDirs map[string]bool) []string {
	var importers []string
	seen := make(map[string]bool)

	// Walk up to 5 levels deep
	walkDirs(cwd, "", 0, 5, func(relDir string) {
		if skipDirs[relDir] || seen[relDir] {
			return
		}
		absDir := filepath.Join(cwd, relDir)
		if hasImport(absDir, targetImport) {
			seen[relDir] = true
			importers = append(importers, relDir)
		}
	})

	// Collect up to 50 importers for coupling-based ranking
	if len(importers) > 50 {
		importers = importers[:50]
	}

	return importers
}

// hasImport checks whether any .go file in dir imports the target package.
func hasImport(dir, targetImport string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, e.Name()), nil, parser.ImportsOnly)
		if err != nil {
			continue
		}
		for _, imp := range f.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			if path == targetImport {
				return true
			}
		}
	}
	return false
}

// listNonTestGoFiles returns basenames of non-test .go files in a directory.
func listNonTestGoFiles(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go") {
			files = append(files, name)
		}
	}
	return files
}
