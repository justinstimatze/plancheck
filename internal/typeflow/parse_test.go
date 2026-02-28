package typeflow

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseExportedSigs(t *testing.T) {
	// Write a small Go file to a temp dir
	dir := t.TempDir()
	src := `package example

func ExportedFunc(ctx context.Context, name string) error {
	return nil
}

func unexported() {}

func (s *Server) Handle(w http.ResponseWriter, r *http.Request) {}

func TestSomething(t *testing.T) {}
`
	path := filepath.Join(dir, "example.go")
	os.WriteFile(path, []byte(src), 0o644)

	sigs, err := ParseExportedSigs(path)
	if err != nil {
		t.Fatalf("ParseExportedSigs: %v", err)
	}

	// Should find ExportedFunc and Handle, not unexported or TestSomething
	if len(sigs) != 2 {
		t.Fatalf("expected 2 sigs, got %d: %+v", len(sigs), sigs)
	}

	if sigs[0].Name != "ExportedFunc" {
		t.Errorf("expected ExportedFunc, got %s", sigs[0].Name)
	}
	if sigs[0].Receiver != "" {
		t.Errorf("expected no receiver, got %s", sigs[0].Receiver)
	}

	if sigs[1].Name != "Handle" {
		t.Errorf("expected Handle, got %s", sigs[1].Name)
	}
	if sigs[1].Receiver != "*Server" {
		t.Errorf("expected *Server receiver, got %s", sigs[1].Receiver)
	}
}

func TestFindCallSites(t *testing.T) {
	dir := t.TempDir()
	src := `package caller

func DoWork() {
	result := shared.SubmitPR(ctx, opts)
	NewCmdCreate(factory, nil)
	unrelated()
}

func Other() {
	x := pkg.NewCmdCreate(f, run)
}
`
	path := filepath.Join(dir, "caller.go")
	os.WriteFile(path, []byte(src), 0o644)

	targets := map[string]bool{"SubmitPR": true, "NewCmdCreate": true}
	sites, err := FindCallSites(path, targets)
	if err != nil {
		t.Fatalf("FindCallSites: %v", err)
	}

	if len(sites) != 3 {
		t.Fatalf("expected 3 call sites, got %d: %+v", len(sites), sites)
	}

	// Check first call site
	found := map[string]bool{}
	for _, s := range sites {
		found[s.Callee] = true
		if s.CallerFunc != "DoWork" && s.CallerFunc != "Other" {
			t.Errorf("unexpected caller func: %s", s.CallerFunc)
		}
	}
	if !found["shared.SubmitPR"] {
		t.Error("missing shared.SubmitPR call site")
	}
	if !found["NewCmdCreate"] && !found["pkg.NewCmdCreate"] {
		t.Error("missing NewCmdCreate call site")
	}
}

func TestFindCallSites_NoMatches(t *testing.T) {
	dir := t.TempDir()
	src := `package unrelated

func Foo() {
	bar()
	baz.Qux()
}
`
	path := filepath.Join(dir, "unrelated.go")
	os.WriteFile(path, []byte(src), 0o644)

	targets := map[string]bool{"SubmitPR": true}
	sites, err := FindCallSites(path, targets)
	if err != nil {
		t.Fatalf("FindCallSites: %v", err)
	}
	if len(sites) != 0 {
		t.Errorf("expected 0 sites, got %d", len(sites))
	}
}

func TestIsStructuralSource(t *testing.T) {
	tests := []struct {
		source string
		want   bool
	}{
		{"structural", true},
		{"callee+comod", true},
		{"import-chain", true},
		{"sibling", true},
		{"cascade", true},
		{"keyword-dir", false},
		{"entity-dir", false},
		{"comod", false},
		{"convention", false},
		{"shared-pkg", false},
		{"cmdutil", false},
		{"peer-dir", false},
		{"dir-sibling+callee", true},
	}
	for _, tt := range tests {
		got := isStructuralSource(tt.source)
		if got != tt.want {
			t.Errorf("isStructuralSource(%q) = %v, want %v", tt.source, got, tt.want)
		}
	}
}
