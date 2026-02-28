package plan

import (
	"os"
	"strings"
	"testing"
)

func TestTokenize(t *testing.T) {
	tests := []struct {
		input string
		want  []string // subset that must appear
	}{
		{
			"fix: list branches in `gh run` and `gh codespace`",
			[]string{"run", "codespace", "gh"},
		},
		{
			"Add handling of empty titles for Issues and PRs",
			[]string{"title", "issue", "pr"},
		},
		{
			"gh gist delete: prompt for gist id",
			[]string{"gist", "delete", "gh"},
		},
	}

	for _, tt := range tests {
		got := tokenize(tt.input)
		gotSet := make(map[string]bool)
		for _, g := range got {
			gotSet[g] = true
		}
		for _, w := range tt.want {
			if !gotSet[w] {
				t.Errorf("tokenize(%q) missing %q, got %v", tt.input, w, got)
			}
		}
	}
}

func TestTokenizeKeepsDomainWords(t *testing.T) {
	got := tokenize("fix: Add support for listing autolink references")
	gotSet := make(map[string]bool)
	for _, g := range got {
		gotSet[g] = true
	}
	// Domain words must survive — they may be directory names
	for _, keep := range []string{"autolink", "reference", "listing", "add"} {
		if !gotSet[keep] {
			t.Errorf("tokenize should keep %q, got %v", keep, got)
		}
	}
}

func TestSingularize(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"issues", "issue"},
		{"prs", "pr"},
		{"branches", "branch"},
		{"queries", "query"},
		{"codespace", "codespace"},
		{"pr", "pr"},
	}
	for _, tt := range tests {
		got := singularize(tt.input)
		if got != tt.want {
			t.Errorf("singularize(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestMatchKeywordsToDirs(t *testing.T) {
	home, _ := os.UserHomeDir()
	cwd := home + "/.plancheck/datasets/repos/cli"
	if info, err := os.Stat(cwd); err != nil || !info.IsDir() {
		t.Skip("cli/cli repo not available")
	}

	// Task 7: plan covers run but not codespace
	planFiles := []string{"pkg/cmd/run/shared/shared.go"}
	gaps, ranked := matchKeywordsToDirs(
		"fix: list branches in square brackets in gh run and gh codespace",
		[]string{"Fix branch listing format"},
		planFiles, cwd,
	)

	foundCodespace := false
	for _, g := range gaps {
		if g.Keyword == "codespace" {
			foundCodespace = true
		}
	}
	if !foundCodespace {
		t.Errorf("expected codespace gap, got gaps: %+v", gaps)
	}

	foundCodespaceFile := false
	for _, r := range ranked {
		if strings.Contains(r.Path, "codespace") {
			foundCodespaceFile = true
		}
	}
	if !foundCodespaceFile {
		t.Errorf("expected ranked file from codespace dir, got: %+v", ranked)
	}
}
