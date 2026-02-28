package typeflow

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveGoFiles(t *testing.T) {
	// Create a temp project structure
	dir := t.TempDir()
	mkfile := func(rel, content string) {
		abs := filepath.Join(dir, rel)
		os.MkdirAll(filepath.Dir(abs), 0o755)
		os.WriteFile(abs, []byte(content), 0o644)
	}

	mkfile("pkg/cmd/pr/create/create.go", "package create\nfunc NewCmdCreate() {}\n")
	mkfile("pkg/cmd/pr/list/list.go", "package list\nfunc NewCmdList() {}\n")
	mkfile("pkg/cmd/issue/create/create.go", "package create\nfunc NewCmdCreate() {}\n")
	mkfile("api/client.go", "package api\nfunc NewClient() {}\n")

	basenames := map[string]bool{"create.go": true, "client.go": true}
	resolved := ResolveGoFiles(basenames, dir)

	// create.go should resolve to multiple paths
	if paths, ok := resolved["create.go"]; !ok {
		t.Fatal("create.go not found")
	} else if len(paths) < 2 {
		t.Errorf("expected 2+ paths for create.go, got %d: %v", len(paths), paths)
	}

	// client.go should resolve to one path
	if paths, ok := resolved["client.go"]; !ok {
		t.Fatal("client.go not found")
	} else if len(paths) != 1 {
		t.Errorf("expected 1 path for client.go, got %d", len(paths))
	} else if paths[0] != "api/client.go" {
		t.Errorf("expected api/client.go, got %s", paths[0])
	}
}

func TestResolveGoFiles_SkipsTestFiles(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "pkg"), 0o755)
	os.WriteFile(filepath.Join(dir, "pkg/foo.go"), []byte("package pkg"), 0o644)
	os.WriteFile(filepath.Join(dir, "pkg/foo_test.go"), []byte("package pkg"), 0o644)

	resolved := ResolveGoFiles(map[string]bool{"foo.go": true, "foo_test.go": true}, dir)

	if _, ok := resolved["foo_test.go"]; ok {
		t.Error("should not resolve test files")
	}
	if paths, ok := resolved["foo.go"]; !ok || len(paths) != 1 {
		t.Error("should resolve foo.go")
	}
}

func TestResolveGoFiles_CacheWorks(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "a"), 0o755)
	os.WriteFile(filepath.Join(dir, "a/x.go"), []byte("package a"), 0o644)

	// First call builds cache
	r1 := ResolveGoFiles(map[string]bool{"x.go": true}, dir)
	// Second call should hit cache
	r2 := ResolveGoFiles(map[string]bool{"x.go": true}, dir)

	if len(r1["x.go"]) != len(r2["x.go"]) {
		t.Error("cache returned different results")
	}

	// Clear cache for other tests
	goFileIndexCache.cwd = ""
	goFileIndexCache.index = nil
}

func TestBuildReasonExported(t *testing.T) {
	sites := []CallSite{
		{File: "a.go", CallerFunc: "Foo", Line: 42, Callee: "Bar", Args: 2},
	}

	reason := BuildReasonExported(sites, 0)
	if reason == "" {
		t.Error("expected non-empty reason")
	}
	if reason != "Foo() calls Bar (line 42) — verified" {
		t.Errorf("unexpected reason: %s", reason)
	}

	// With depth
	reason = BuildReasonExported(sites, 1)
	if reason != "Foo() calls Bar (line 42) (cascade depth 1) — verified" {
		t.Errorf("unexpected reason with depth: %s", reason)
	}

	// Multiple sites
	sites = append(sites, CallSite{File: "b.go", CallerFunc: "Baz", Line: 10, Callee: "Qux"})
	reason = BuildReasonExported(sites, 0)
	if reason != "Foo() calls Bar (line 42) + 1 more — verified" {
		t.Errorf("unexpected multi-site reason: %s", reason)
	}
}

func TestDiscoverVerifiedFiles_NoGraph(t *testing.T) {
	// With no graph and no queryDefn, should return nil
	files := DiscoverVerifiedFiles([]string{"a.go"}, "/nonexistent", nil, nil)
	if files != nil {
		t.Errorf("expected nil, got %d files", len(files))
	}
}

func TestDiscoverVerifiedFiles_WithSource(t *testing.T) {
	dir := t.TempDir()
	mkfile := func(rel, content string) {
		abs := filepath.Join(dir, rel)
		os.MkdirAll(filepath.Dir(abs), 0o755)
		os.WriteFile(abs, []byte(content), 0o644)
	}

	// Plan file exports Foo
	mkfile("pkg/a/main.go", `package a

func Foo(x int) error {
	return nil
}

func Bar() string {
	return ""
}
`)

	// Caller file calls Foo
	mkfile("pkg/b/caller.go", `package b

import "pkg/a"

func DoWork() {
	a.Foo(42)
}
`)

	// Unrelated file
	mkfile("pkg/c/other.go", `package c

func Unrelated() {}
`)

	// Mock queryDefn that returns "caller.go" as a caller of definitions in "main.go"
	queryDefn := func(cwd, sql string) []map[string]interface{} {
		if contains(sql, "caller") || contains(sql, "callee") {
			return []map[string]interface{}{
				{"source_file": "caller.go"},
			}
		}
		return nil
	}

	files := DiscoverVerifiedFiles(
		[]string{"pkg/a/main.go"},
		dir,
		queryDefn,
		nil, // no graph
	)

	// Should discover caller.go because it calls Foo
	found := false
	for _, f := range files {
		if f.File == "caller.go" && len(f.CallSites) > 0 {
			found = true
		}
	}
	if !found {
		t.Errorf("expected to discover caller.go with call sites, got: %+v", files)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
