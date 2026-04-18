package cmd

import (
	"fmt"
	"io"

	"github.com/justinstimatze/plancheck/internal/refgraph"
)

// formatStaleCount renders the stale-files list as "N file(s) — sample (+X more)".
// Empty stale slice returns "" so callers can treat "" as the in-sync case.
func formatStaleCount(stale []string) string {
	if len(stale) == 0 {
		return ""
	}
	sample := stale[0]
	if len(stale) > 1 {
		sample = fmt.Sprintf("%s (+%d more)", sample, len(stale)-1)
	}
	return fmt.Sprintf("%d .go file(s) modified since last ingest — %s", len(stale), sample)
}

// warnIfStale prints a warning to w if the defn database is older than
// the working tree. Silent when the DB is in-sync or when no last_ingest
// marker exists (pre-v0.11.3 DBs). Non-fatal — plancheck still runs.
func warnIfStale(w io.Writer, cwd string) {
	msg := formatStaleCount(refgraph.StaleFiles(cwd))
	if msg == "" {
		return
	}
	fmt.Fprintf(w, "\x1b[33m⚠ defn out of date: %s\n"+
		"  results may miss recent changes. Run: defn ingest\x1b[0m\n\n", msg)
}
