package plan

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/justinstimatze/plancheck/internal/types"
)

// A plan file at the repo root must not let peer-dir scanning escape into
// sibling projects under the same parent directory (e.g. a Python repo
// surfacing .go files from a neighboring Go repo).
func TestFindAncestorScope_StaysInsideProject(t *testing.T) {
	parent := t.TempDir()
	cwd := filepath.Join(parent, "project")
	sibling := filepath.Join(parent, "sibling")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sibling, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(sibling, "other.go"), []byte("package sibling\n"), 0o644)
	os.WriteFile(filepath.Join(cwd, "root.go"), []byte("package main\n"), 0o644)

	p := types.ExecutionPlan{FilesToModify: []string{"root.go"}}
	peers, _ := findAncestorScope(p, cwd)
	for _, pf := range peers {
		t.Errorf("expected no peer files outside the project, got: %+v", pf)
	}
}

func TestFindParentRegistrations_StaysInsideProject(t *testing.T) {
	parent := t.TempDir()
	cwd := filepath.Join(parent, "project")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	// A .go file directly in the parent of cwd would be wrongly surfaced if the
	// scan escaped above the project root.
	os.WriteFile(filepath.Join(parent, "outside.go"), []byte("package parent\n"), 0o644)
	os.WriteFile(filepath.Join(cwd, "root.go"), []byte("package main\n"), 0o644)

	p := types.ExecutionPlan{FilesToModify: []string{"root.go"}}
	regs := findParentRegistrations(p, cwd)
	for _, r := range regs {
		if r.file == "outside.go" {
			t.Errorf("expected no registration file from above the project root, got: %+v", r)
		}
	}
}

// The legitimate in-project case still works: a peer package under the same
// feature root is surfaced.
func TestFindAncestorScope_FindsInProjectPeers(t *testing.T) {
	cwd := t.TempDir()
	os.MkdirAll(filepath.Join(cwd, "pkg", "cmd", "view"), 0o755)
	os.MkdirAll(filepath.Join(cwd, "pkg", "cmd", "shared"), 0o755)
	os.WriteFile(filepath.Join(cwd, "pkg", "cmd", "view", "view.go"), []byte("package view\n"), 0o644)
	os.WriteFile(filepath.Join(cwd, "pkg", "cmd", "shared", "shared.go"), []byte("package shared\n"), 0o644)

	p := types.ExecutionPlan{FilesToModify: []string{"pkg/cmd/view/view.go"}}
	peers, _ := findAncestorScope(p, cwd)
	found := false
	for _, pf := range peers {
		if pf.file == "shared.go" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected shared.go peer within the project, got: %+v", peers)
	}
}
