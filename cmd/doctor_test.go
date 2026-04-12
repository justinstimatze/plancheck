package cmd

import (
	"strings"
	"testing"
	"time"
)

// TestRecentHookErrors verifies the time filter: only entries within the
// last `window` should be returned. Historical errors from before the window
// must be excluded so users don't see "doctor is failing" forever.
func TestRecentHookErrors(t *testing.T) {
	now := time.Date(2026, 4, 12, 12, 0, 0, 0, time.UTC)
	window := 24 * time.Hour

	tests := []struct {
		name     string
		log      string
		wantLen  int
		wantLast string // substring expected in last kept entry, "" to skip
	}{
		{
			name:    "empty log",
			log:     "",
			wantLen: 0,
		},
		{
			name:    "only whitespace",
			log:     "\n\n  \n",
			wantLen: 0,
		},
		{
			name: "all entries within window",
			log: "[2026-04-12T10:00:00Z] recent error 1\n" +
				"[2026-04-12T11:30:00Z] recent error 2\n",
			wantLen:  2,
			wantLast: "recent error 2",
		},
		{
			name: "all entries outside window",
			log: "[2026-04-10T10:00:00Z] old error 1\n" +
				"[2026-04-10T11:30:00Z] old error 2\n",
			wantLen: 0,
		},
		{
			name: "mix of old and new",
			log: "[2026-04-10T10:00:00Z] old error\n" +
				"[2026-04-12T11:00:00Z] fresh error\n" +
				"[2026-04-09T09:00:00Z] ancient error\n",
			wantLen:  1,
			wantLast: "fresh error",
		},
		{
			name: "exactly at boundary (24h ago) is included",
			log:  "[2026-04-11T12:00:00Z] boundary error\n",
			wantLen: 1,
		},
		{
			name: "one second before boundary is excluded",
			log:  "[2026-04-11T11:59:59Z] just-too-old error\n",
			wantLen: 0,
		},
		{
			name: "unparseable timestamps skipped",
			log: "not a valid line\n" +
				"[garbage] missing timestamp format\n" +
				"[2026-04-12T10:00:00Z] valid recent\n",
			wantLen:  1,
			wantLast: "valid recent",
		},
		{
			name: "line without brackets skipped",
			log: "plain text without brackets\n" +
				"[2026-04-12T10:00:00Z] valid\n",
			wantLen: 1,
		},
		{
			name: "bracket but no closing",
			log: "[2026-04-12T10:00:00Z unclosed\n" +
				"[2026-04-12T10:00:00Z] valid\n",
			wantLen: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := recentHookErrors(tt.log, now, window)
			if len(got) != tt.wantLen {
				t.Errorf("recentHookErrors returned %d entries, want %d: %v", len(got), tt.wantLen, got)
			}
			if tt.wantLast != "" && len(got) > 0 {
				last := got[len(got)-1]
				if !strings.Contains(last, tt.wantLast) {
					t.Errorf("last entry = %q, want substring %q", last, tt.wantLast)
				}
			}
		})
	}
}

// TestRecentHookErrors_PreservesOrder ensures entries come back in log order,
// not reversed or sorted. Doctor needs the newest-last order to show "most recent".
func TestRecentHookErrors_PreservesOrder(t *testing.T) {
	now := time.Date(2026, 4, 12, 12, 0, 0, 0, time.UTC)
	log := "[2026-04-12T09:00:00Z] first\n" +
		"[2026-04-12T10:00:00Z] second\n" +
		"[2026-04-12T11:00:00Z] third\n"
	got := recentHookErrors(log, now, 24*time.Hour)
	if len(got) != 3 {
		t.Fatalf("want 3 entries, got %d", len(got))
	}
	if !strings.Contains(got[0], "first") || !strings.Contains(got[1], "second") || !strings.Contains(got[2], "third") {
		t.Errorf("order not preserved: %v", got)
	}
}
