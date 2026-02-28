// Package history manages per-project plan check history, reflections,
// and calibration data stored in ~/.plancheck/projects/.
package history

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/justinstimatze/plancheck/internal/types"
)

const (
	historyFile    = "history.jsonl"
	knowledgeFile  = "knowledge.md"
	patternWindow  = 10
	minOccurrences = 2
	maxEntries     = 50
)

// ProjectDirFn resolves the storage directory for a given project cwd.
// Tests override this to avoid writing to ~/.plancheck/.
var ProjectDirFn = defaultProjectDir

func defaultProjectDir(cwd string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.TempDir()
	}
	sum := sha256.Sum256([]byte(cwd))
	hash := hex.EncodeToString(sum[:])[:16]
	dir := filepath.Join(home, ".plancheck", "projects", hash)
	_ = os.MkdirAll(dir, 0o700)
	_ = os.WriteFile(filepath.Join(dir, "project.txt"), []byte(cwd), 0o600)
	return dir
}

type HistoryEntry struct {
	ID              string   `json:"id"`
	Timestamp       string   `json:"timestamp"`
	Objective       string   `json:"objective"`
	ProjectType     string   `json:"projectType"`
	ComodMisses     []string `json:"comodMisses"`
	SuggestedModify []string `json:"suggestedModify"`
}

type OutcomeEntry struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	Outcome   string `json:"outcome"`
	Timestamp string `json:"timestamp"`
}

type ReflectionEntry struct {
	Type            string   `json:"type"`
	ID              string   `json:"id"`
	Passes          int      `json:"passes"`
	ProbeFindings   int      `json:"probe_findings"`
	PersonaFindings int      `json:"persona_findings"`
	Missed          string   `json:"missed"`
	Outcome         string   `json:"outcome"`
	SignalsUseful   []string `json:"signals_useful"`
	BlastRadius     int      `json:"blast_radius,omitempty"`  // production callers from simulation
	TestCoverage    int      `json:"test_coverage,omitempty"` // tests covering modified defs
	BrierScore      float64  `json:"brier_score,omitempty"`   // simulation accuracy (0 = perfect)
	InputTokens     int      `json:"input_tokens,omitempty"`  // API token usage
	OutputTokens    int      `json:"output_tokens,omitempty"`
	EstimatedCost   float64  `json:"estimated_cost,omitempty"` // USD
	Timestamp       string   `json:"timestamp"`
}

func historyPath(cwd string) string {
	return filepath.Join(ProjectDirFn(cwd), historyFile)
}

// historyEnabled returns false if PLANCHECK_NOHISTORY is set.
func historyEnabled() bool {
	return os.Getenv("PLANCHECK_NOHISTORY") == ""
}

// cullHistory trims the history file to the newest maxEntries lines.
func cullHistory(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) <= maxEntries {
		return
	}
	kept := lines[len(lines)-maxEntries:]
	_ = os.WriteFile(path, []byte(strings.Join(kept, "\n")+"\n"), 0o600)
}

// appendGlobalLog writes a JSON line to the cross-project aggregate log if PLANCHECK_GLOBAL_LOG is set.
func appendGlobalLog(line []byte, cwd string) {
	globalLog := os.Getenv("PLANCHECK_GLOBAL_LOG")
	if globalLog == "" {
		return
	}
	// Splice cwd into the JSON object
	withCwd := append(line[:len(line)-1], []byte(fmt.Sprintf(`,"cwd":%q}`, cwd))...)
	_ = os.MkdirAll(filepath.Dir(globalLog), 0o700)
	f, err := os.OpenFile(globalLog, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_, _ = f.Write(append(withCwd, '\n'))
	f.Close()
}

func randomID() string {
	const letters = "0123456789abcdefghijklmnopqrstuvwxyz"
	b := make([]byte, 6)
	for i := range b {
		b[i] = letters[rand.Intn(len(letters))]
	}
	return string(b)
}

// AppendHistory records a plan check result to the project's history file and returns the generated ID.
func AppendHistory(p types.ExecutionPlan, result types.PlanCheckResult, cwd string) string {
	id := randomID()
	if !historyEnabled() {
		return id
	}
	dir := ProjectDirFn(cwd)

	entry := HistoryEntry{
		ID:              id,
		Timestamp:       time.Now().UTC().Format(time.RFC3339),
		Objective:       p.Objective,
		ProjectType:     result.ProjectType,
		ComodMisses:     make([]string, len(result.ComodGaps)),
		SuggestedModify: result.SuggestedAdditions.FilesToModify,
	}
	for i, g := range result.ComodGaps {
		entry.ComodMisses[i] = g.ComodFile
	}
	if entry.ComodMisses == nil {
		entry.ComodMisses = []string{}
	}
	if entry.SuggestedModify == nil {
		entry.SuggestedModify = []string{}
	}

	line, err := json.Marshal(entry)
	if err != nil {
		return id
	}

	f, err := os.OpenFile(historyPath(cwd), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return id
	}
	defer f.Close()
	_, _ = f.Write(append(line, '\n'))

	cullHistory(historyPath(cwd))

	// Persist last check id
	_ = os.WriteFile(filepath.Join(dir, "last-check-id"), []byte(id), 0o600)

	appendGlobalLog(line, cwd)

	return id
}

// RecordOutcome appends an outcome entry (clean/rework/failed) to the project's history file.
// Returns an error if the ID is not found in the project's history.
func RecordOutcome(cwd, id, outcome string) error {
	if !historyEnabled() {
		return nil
	}

	summary, err := LoadHistory(cwd)
	if err != nil {
		return fmt.Errorf("failed to load history: %w", err)
	}
	found := false
	for _, e := range summary.Entries {
		if e.ID == id {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("history ID %q not found", id)
	}

	entry := OutcomeEntry{
		Type:      "outcome",
		ID:        id,
		Outcome:   outcome,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
	line, _ := json.Marshal(entry)
	f, err2 := os.OpenFile(historyPath(cwd), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err2 != nil {
		return fmt.Errorf("failed to write outcome: %w", err2)
	}
	defer f.Close()
	_, _ = f.Write(append(line, '\n'))

	appendGlobalLog(line, cwd)
	return nil
}

type ReflectionOpts struct {
	ID              string
	Passes          int
	ProbeFindings   int
	PersonaFindings int
	Missed          string
	Outcome         string
	SignalsUseful   []string
}

// RecordReflection appends a post-execution reflection with calibration data. Requires at least 2 passes.
func RecordReflection(cwd string, opts ReflectionOpts) (string, error) {
	if opts.Passes < 2 {
		return "", fmt.Errorf("minimum 2 persona passes required (got %d) — a single clean pass doesn't prove convergence", opts.Passes)
	}

	id := opts.ID
	if id == "" {
		id = randomID()
	}

	if !historyEnabled() {
		return id, nil
	}

	entry := ReflectionEntry{
		Type:            "reflection",
		ID:              id,
		Passes:          opts.Passes,
		ProbeFindings:   opts.ProbeFindings,
		PersonaFindings: opts.PersonaFindings,
		Missed:          opts.Missed,
		Outcome:         opts.Outcome,
		SignalsUseful:   opts.SignalsUseful,
		Timestamp:       time.Now().UTC().Format(time.RFC3339),
	}

	// Auto-populate cost from the last check result if available
	if checkResult, err := LoadCheckResult(cwd); err == nil && checkResult.Cost != nil {
		entry.InputTokens = checkResult.Cost.InputTokens
		entry.OutputTokens = checkResult.Cost.OutputTokens
		entry.EstimatedCost = checkResult.Cost.EstimatedUSD
	}
	if entry.SignalsUseful == nil {
		entry.SignalsUseful = []string{}
	}

	line, _ := json.Marshal(entry)

	dir := ProjectDirFn(cwd)
	f, err := os.OpenFile(filepath.Join(dir, historyFile), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return id, fmt.Errorf("failed to write reflection: %w", err)
	}
	defer f.Close()
	_, _ = f.Write(append(line, '\n'))

	appendGlobalLog(line, cwd)

	return id, nil
}

// HistorySummary is a parsed view of a project's history file.
type HistorySummary struct {
	Entries     []HistoryEntry
	Outcomes    map[string]string // id -> outcome
	Reflections map[string]ReflectionEntry
}

// LoadHistory reads and parses the project's history file into a structured summary.
func LoadHistory(cwd string) (HistorySummary, error) {
	summary := HistorySummary{
		Outcomes:    make(map[string]string),
		Reflections: make(map[string]ReflectionEntry),
	}

	data, err := os.ReadFile(historyPath(cwd))
	if err != nil {
		if os.IsNotExist(err) {
			return summary, nil
		}
		return summary, err
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}
		var raw map[string]interface{}
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			continue
		}

		switch t, _ := raw["type"].(string); t {
		case "outcome":
			var o OutcomeEntry
			if json.Unmarshal([]byte(line), &o) == nil {
				summary.Outcomes[o.ID] = o.Outcome
			}
		case "reflection":
			var r ReflectionEntry
			if json.Unmarshal([]byte(line), &r) == nil {
				summary.Reflections[r.ID] = r
			}
		default:
			var e HistoryEntry
			if json.Unmarshal([]byte(line), &e) == nil {
				summary.Entries = append(summary.Entries, e)
			}
		}
	}

	return summary, nil
}

// LoadLastCheckID returns the most recent check_plan history ID for the project, or "" if none.
func LoadLastCheckID(cwd string) string {
	data, err := os.ReadFile(filepath.Join(ProjectDirFn(cwd), "last-check-id"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// LoadProjectKnowledge returns the contents of the project's knowledge.md file, or "" if none.
func LoadProjectKnowledge(cwd string) string {
	data, err := os.ReadFile(filepath.Join(ProjectDirFn(cwd), knowledgeFile))
	if err != nil {
		return ""
	}
	return string(data)
}

// SaveProjectKnowledge writes content to the project's knowledge.md file.
func SaveProjectKnowledge(cwd, content string) {
	dir := ProjectDirFn(cwd)
	_ = os.WriteFile(filepath.Join(dir, knowledgeFile), []byte(content), 0o600)
}

const checkResultFile = "last-check-result.json"

// SaveCheckResult caches the full PlanCheckResult for drill-down via get_check_details.
func SaveCheckResult(cwd string, result types.PlanCheckResult) {
	dir := ProjectDirFn(cwd)
	data, err := json.Marshal(result)
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(dir, checkResultFile), data, 0o600)
}

// LoadCheckResult loads the cached PlanCheckResult, or returns an error if none exists.
func LoadCheckResult(cwd string) (types.PlanCheckResult, error) {
	var result types.PlanCheckResult
	data, err := os.ReadFile(filepath.Join(ProjectDirFn(cwd), checkResultFile))
	if err != nil {
		return result, fmt.Errorf("no cached check result — run check_plan first")
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return result, fmt.Errorf("corrupt cached result: %w", err)
	}
	return result, nil
}

// ClearHistory removes the project's history file, last-check-id, and cached check result.
// Preserves knowledge.md and project.txt.
func ClearHistory(cwd string) error {
	dir := ProjectDirFn(cwd)
	for _, name := range []string{historyFile, "last-check-id", checkResultFile} {
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("failed to remove %s: %w", name, err)
		}
	}
	return nil
}

// LoadPatterns extracts recurring patterns (missed files, score trends, layer effectiveness) from project history.
func LoadPatterns(cwd string) []types.ProjectPattern {
	if !historyEnabled() {
		return nil
	}
	data, err := os.ReadFile(historyPath(cwd))
	if err != nil {
		return nil
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) < minOccurrences {
		return nil
	}

	cutoff := time.Now().Add(-7 * 24 * time.Hour)

	var entries []HistoryEntry
	for _, line := range lines {
		if line == "" {
			continue
		}
		var raw map[string]interface{}
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			continue
		}
		// Skip outcome/reflection entries
		if t, ok := raw["type"]; ok && t != nil {
			continue
		}
		var entry HistoryEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		// Auto-decay: skip entries older than 7 days
		if ts, err := time.Parse(time.RFC3339, entry.Timestamp); err == nil && ts.Before(cutoff) {
			continue
		}
		entries = append(entries, entry)
	}

	var patterns []types.ProjectPattern
	// Take the last patternWindow entries
	window := entries
	if len(window) > patternWindow {
		window = window[len(window)-patternWindow:]
	}

	// Pattern 1: recurring misses
	missCounts := make(map[string]int)
	for _, entry := range window {
		files := make(map[string]bool)
		for _, f := range entry.ComodMisses {
			files[f] = true
		}
		for _, f := range entry.SuggestedModify {
			files[f] = true
		}
		for f := range files {
			if strings.HasPrefix(f, "<") {
				continue
			}
			missCounts[f]++
		}
	}

	type missEntry struct {
		file  string
		count int
	}
	var recurringMisses []missEntry
	for f, n := range missCounts {
		if n >= minOccurrences {
			recurringMisses = append(recurringMisses, missEntry{f, n})
		}
	}
	sort.Slice(recurringMisses, func(i, j int) bool {
		return recurringMisses[i].count > recurringMisses[j].count
	})

	if len(recurringMisses) > 0 {
		parts := make([]string, len(recurringMisses))
		for i, m := range recurringMisses {
			parts[i] = fmt.Sprintf("%s (%dx)", m.file, m.count)
		}
		patterns = append(patterns, types.ProjectPattern{
			Type:        "recurring-miss",
			Description: "Files repeatedly missing from plans: " + strings.Join(parts, ", "),
			Suggestion:  "ADD these to filesToModify unless you can explain why they're not needed this time",
		})
	}

	// Pattern 2: reflection summary — which layer is earning its cost
	var reflections []ReflectionEntry
	for _, line := range lines {
		if line == "" {
			continue
		}
		var raw map[string]interface{}
		if json.Unmarshal([]byte(line), &raw) != nil {
			continue
		}
		if t, _ := raw["type"].(string); t == "reflection" {
			var r ReflectionEntry
			if json.Unmarshal([]byte(line), &r) == nil {
				// Auto-decay: skip reflections older than 7 days
				if ts, err := time.Parse(time.RFC3339, r.Timestamp); err == nil && ts.Before(cutoff) {
					continue
				}
				reflections = append(reflections, r)
			}
		}
	}
	if len(reflections) >= 3 {
		recent := reflections
		if len(recent) > 5 {
			recent = recent[len(recent)-5:]
		}
		var totalProbe, totalPersona, reworks int
		for _, r := range recent {
			totalProbe += r.ProbeFindings
			totalPersona += r.PersonaFindings
			if r.Outcome == "rework" || r.Outcome == "failed" {
				reworks++
			}
		}
		if totalProbe+totalPersona > 0 {
			desc := fmt.Sprintf("Last %d reflections: probe findings %d, persona findings %d, reworks %d",
				len(recent), totalProbe, totalPersona, reworks)
			suggestion := "Both layers contributing"
			if totalPersona == 0 && totalProbe > 0 {
				suggestion = "Persona passes found nothing — consider skipping them for this project"
			} else if totalProbe == 0 && totalPersona > 0 {
				suggestion = "Probes found nothing — check if git history is available"
			}
			patterns = append(patterns, types.ProjectPattern{
				Type:        "layer-effectiveness",
				Description: desc,
				Suggestion:  suggestion,
			})
		}
	}

	return patterns
}
