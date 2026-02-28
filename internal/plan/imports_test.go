package plan

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindImportChainFiles_CliRepo(t *testing.T) {
	cwd := filepath.Join(os.Getenv("HOME"), ".plancheck", "datasets", "repos", "cli")
	if _, err := os.Stat(filepath.Join(cwd, "go.mod")); err != nil {
		t.Skip("cli repo not available at ~/.plancheck/datasets/repos/cli")
	}

	planFiles := []string{"pkg/cmd/pr/create/create.go"}
	result := findImportChainFiles(planFiles, cwd)

	if len(result) == 0 {
		t.Fatal("expected import chain files, got none")
	}

	// Check that we find files from imported packages (dependencies)
	hasImportDir := false
	importPaths := make(map[string]bool)
	for _, f := range result {
		if f.dir == "import" {
			hasImportDir = true
			importPaths[filepath.Dir(f.fullPath)] = true
		}
	}
	if !hasImportDir {
		t.Error("expected at least one file with dir='import' (dependency)")
	}

	// The pr/create package should import api/ among its dependencies
	if !importPaths["api"] {
		t.Logf("import paths found: %v", importPaths)
		t.Error("expected to find api/ as a dependency")
	} else {
		t.Log("found expected dependency package: api")
	}

	// Check reverse direction: packages that import pkg/cmd/pr/create
	// pkg/cmd/pr/pr.go imports pkg/cmd/pr/create, so we should find it
	hasImporterDir := false
	for _, f := range result {
		if f.dir == "importer" {
			hasImporterDir = true
			t.Logf("found importer: %s (from %s)", f.fullPath, f.planFile)
		}
	}
	if !hasImporterDir {
		t.Error("expected at least one importer (pkg/cmd/pr/pr.go imports pkg/cmd/pr/create)")
	}

	// Verify all results have valid fullPath
	for _, f := range result {
		if f.fullPath == "" {
			t.Errorf("import chain file has empty fullPath: %+v", f)
		}
		if f.file == "" {
			t.Errorf("import chain file has empty file: %+v", f)
		}
		if f.dir != "import" && f.dir != "importer" {
			t.Errorf("import chain file has invalid dir %q: %+v", f.dir, f)
		}
	}

	t.Logf("total import chain files: %d", len(result))
}

func TestReadModulePath(t *testing.T) {
	cwd := filepath.Join(os.Getenv("HOME"), ".plancheck", "datasets", "repos", "cli")
	if _, err := os.Stat(filepath.Join(cwd, "go.mod")); err != nil {
		t.Skip("cli repo not available")
	}

	modPath := readModulePath(cwd)
	if modPath != "github.com/cli/cli/v2" {
		t.Errorf("expected github.com/cli/cli/v2, got %q", modPath)
	}
}

func TestFindImporters_CliRepo(t *testing.T) {
	cwd := filepath.Join(os.Getenv("HOME"), ".plancheck", "datasets", "repos", "cli")
	if _, err := os.Stat(filepath.Join(cwd, "go.mod")); err != nil {
		t.Skip("cli repo not available")
	}

	modPath := "github.com/cli/cli/v2"
	target := "github.com/cli/cli/v2/pkg/cmd/pr/create"
	skipDirs := map[string]bool{"pkg/cmd/pr/create": true}

	importers := findImporters(cwd, modPath, target, skipDirs)
	t.Logf("importers of pkg/cmd/pr/create: %v", importers)

	// pkg/cmd/pr/pr.go imports pkg/cmd/pr/create
	found := false
	for _, imp := range importers {
		if imp == "pkg/cmd/pr" {
			found = true
		}
	}
	if !found {
		t.Error("expected pkg/cmd/pr as an importer of pkg/cmd/pr/create")
	}
}

func TestFindImportChainFiles_NonGoFiles(t *testing.T) {
	// Test with non-.go files — should return empty
	result := findImportChainFiles([]string{"README.md", "Makefile"}, "/tmp")
	if len(result) != 0 {
		t.Errorf("expected no results for non-Go files, got %d", len(result))
	}
}

func TestFindImportChainFiles_NoGoMod(t *testing.T) {
	// Test with a directory that has no go.mod
	result := findImportChainFiles([]string{"main.go"}, "/tmp")
	if len(result) != 0 {
		t.Errorf("expected no results without go.mod, got %d", len(result))
	}
}
