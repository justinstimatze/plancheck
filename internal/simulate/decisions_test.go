package simulate

import (
	"fmt"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

func TestExtractOpenDecisions_TwoBlocks(t *testing.T) {
	resp := `DECISION: Should format be an enum or a bool?
CHOSE: enum with values standard and short
ALTERNATIVE: bool isShort
AFFECTS: internal/db/schema.go, cmd/list.go

DECISION: Does a short video link to a pitch?
CHOSE: no pitch relationship
ALTERNATIVE: optional pitch_id foreign key
AFFECTS: internal/db/schema.go`

	got := extractOpenDecisions(resp)
	if len(got) != 2 {
		t.Fatalf("want 2 decisions, got %d: %+v", len(got), got)
	}
	if got[0].Question != "Should format be an enum or a bool?" {
		t.Errorf("question = %q", got[0].Question)
	}
	if got[0].Chose != "enum with values standard and short" {
		t.Errorf("chose = %q", got[0].Chose)
	}
	if got[0].Alternative != "bool isShort" {
		t.Errorf("alternative = %q", got[0].Alternative)
	}
	if len(got[0].Affects) != 2 || got[0].Affects[1] != "cmd/list.go" {
		t.Errorf("affects = %v", got[0].Affects)
	}
	if len(got[1].Affects) != 1 {
		t.Errorf("second affects = %v", got[1].Affects)
	}
}

func TestExtractOpenDecisions_None(t *testing.T) {
	if got := extractOpenDecisions("NONE"); len(got) != 0 {
		t.Errorf("want no decisions, got %+v", got)
	}
}

func TestExtractOpenDecisions_ProseOnly(t *testing.T) {
	resp := "I made a few choices along the way but nothing that changed the file set."
	if got := extractOpenDecisions(resp); len(got) != 0 {
		t.Errorf("want no decisions from prose, got %+v", got)
	}
}

// A block with no AFFECTS is still a real fork — the alternative may be
// contained in files the spike already wrote.
func TestExtractOpenDecisions_MissingOptionalFields(t *testing.T) {
	resp := `DECISION: Where does the render pipeline live?
CHOSE: separate repo invoked over a shell boundary`

	got := extractOpenDecisions(resp)
	if len(got) != 1 {
		t.Fatalf("want 1 decision, got %d", len(got))
	}
	if got[0].Alternative != "" || len(got[0].Affects) != 0 {
		t.Errorf("optional fields should be empty, got %+v", got[0])
	}
}

// A question with no choice is not a decision the spike actually made.
func TestExtractOpenDecisions_QuestionWithoutChoice(t *testing.T) {
	resp := `DECISION: Should this be an enum?
ALTERNATIVE: bool`

	if got := extractOpenDecisions(resp); len(got) != 0 {
		t.Errorf("want no decisions without CHOSE, got %+v", got)
	}
}

func TestExtractOpenDecisions_AlternativeNoneDropped(t *testing.T) {
	resp := `DECISION: Which layer owns the conversion?
CHOSE: the handler
ALTERNATIVE: none`

	got := extractOpenDecisions(resp)
	if len(got) != 1 {
		t.Fatalf("want 1 decision, got %d", len(got))
	}
	if got[0].Alternative != "" {
		t.Errorf("literal none should be dropped, got %q", got[0].Alternative)
	}
}

func TestCleanAffectedPath(t *testing.T) {
	cases := map[string]string{
		"internal/db/schema.go":     "internal/db/schema.go",
		"  cmd/list.go  ":           "cmd/list.go",
		"./cmd/list.go":             "cmd/list.go",
		"`internal/api/run.go`":     "internal/api/run.go",
		"**cmd/create.go**":         "cmd/create.go",
		"schema.go":                 "schema.go",                 // bare names kept, unlike cleanGoPath
		"internal/db/store_test.go": "internal/db/store_test.go", // test files kept
		"the HTTP layer":            "",
		"":                          "",
	}
	for in, want := range cases {
		if got := cleanAffectedPath(in); got != want {
			t.Errorf("cleanAffectedPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestExtractOpenDecisions_DedupesRepeatedQuestion(t *testing.T) {
	resp := `DECISION: Should format be an enum or a bool?
CHOSE: enum
AFFECTS: a/b.go

DECISION: should format be an enum or a bool?
CHOSE: bool
AFFECTS: c/d.go`

	got := extractOpenDecisions(resp)
	if len(got) != 1 {
		t.Fatalf("want 1 deduped decision, got %d: %+v", len(got), got)
	}
	if got[0].Chose != "enum" {
		t.Errorf("first occurrence should win, got %q", got[0].Chose)
	}
}

func TestExtractOpenDecisions_StripsControlCharacters(t *testing.T) {
	resp := "DECISION: Enum or bool?\nCHOSE: enum\x1b[2Kfake findings\x07"

	got := extractOpenDecisions(resp)
	if len(got) != 1 {
		t.Fatalf("want 1 decision, got %d", len(got))
	}
	if strings.ContainsAny(got[0].Chose, "\x1b\x07") {
		t.Errorf("control characters survived: %q", got[0].Chose)
	}
}

func TestExtractOpenDecisions_ClampsLongFieldOnRuneBoundary(t *testing.T) {
	long := strings.Repeat("é", maxDecisionField+50)
	got := extractOpenDecisions("DECISION: Enum or bool?\nCHOSE: " + long)
	if len(got) != 1 {
		t.Fatalf("want 1 decision, got %d", len(got))
	}
	if !utf8.ValidString(got[0].Chose) {
		t.Error("clamped field is not valid UTF-8")
	}
	if n := utf8.RuneCountInString(got[0].Chose); n > maxDecisionField+1 {
		t.Errorf("clamped field has %d runes, want <= %d", n, maxDecisionField+1)
	}
	if !strings.HasSuffix(got[0].Chose, "…") {
		t.Error("truncation should be marked, not silent")
	}
}

func TestExtractOpenDecisions_CapsAffects(t *testing.T) {
	var files []string
	for i := 0; i < maxAffects*2; i++ {
		files = append(files, fmt.Sprintf("pkg/f%d.go", i))
	}
	resp := "DECISION: Enum or bool?\nCHOSE: enum\nAFFECTS: " + strings.Join(files, ", ")

	got := extractOpenDecisions(resp)
	if len(got) != 1 {
		t.Fatalf("want 1 decision, got %d", len(got))
	}
	if len(got[0].Affects) != maxAffects {
		t.Errorf("want affects capped at %d, got %d", maxAffects, len(got[0].Affects))
	}
}

func TestCleanAffectedPath_RejectsProse(t *testing.T) {
	for _, in := range []string{"the file cmd/list.go", "some prose", "cmd/list.go extra"} {
		if got := cleanAffectedPath(in); got != "" {
			t.Errorf("cleanAffectedPath(%q) = %q, want empty", in, got)
		}
	}
}

// Bidi overrides and zero-width characters let a decision render as something
// other than what the reader acts on. They survive a `r < 0x20` filter.
func TestExtractOpenDecisions_StripsUnicodeFormatCharacters(t *testing.T) {
	// U+202E right-to-left override, U+202C pop directional formatting,
	// U+200B zero-width space, U+FEFF byte order mark. Written as escapes
	// because a literal BOM is not legal in Go source.
	resp := "DECISION: Enum or bool?\nCHOSE: use \u202Eenum\u202C sa\u200Bfely\uFEFF"

	got := extractOpenDecisions(resp)
	if len(got) != 1 {
		t.Fatalf("want 1 decision, got %d", len(got))
	}
	for _, r := range got[0].Chose {
		if unicode.Is(unicode.Cf, r) {
			t.Errorf("format character U+%04X survived in %q", r, got[0].Chose)
		}
	}
	if got[0].Chose != "use enum safely" {
		t.Errorf("got %q, want %q", got[0].Chose, "use enum safely")
	}
}
