// pattern.go searches definition bodies for code patterns.
//
// For refactoring tasks ("modernize for loops", "add error handling"),
// the right probe is "which files contain the pattern being changed?"
// not "which files are structurally connected."
//
// This uses defn's bodies table to search function bodies across the
// codebase for a given pattern.
package refgraph

import (
	"path/filepath"
	"sort"
	"strings"
)

// PatternMatch is a file containing a code pattern in its function bodies.
type PatternMatch struct {
	File        string `json:"file"`
	Occurrences int    `json:"occurrences"` // number of definitions with this pattern
}

// SearchBodies searches function bodies across the project for a LIKE pattern.
// Returns files that contain the pattern, sorted by occurrence count.
func SearchBodies(cwd string, pattern string) []PatternMatch {
	if !Available(cwd) {
		return nil
	}

	// Escape single quotes in pattern
	safe := strings.ReplaceAll(pattern, "'", "''")

	rows := QueryDefn(cwd,
		"SELECT d.source_file, COUNT(*) as n "+
			"FROM definitions d "+
			"JOIN bodies b ON b.def_id = d.id "+
			"WHERE b.body LIKE '%"+safe+"%' "+
			"AND d.test = FALSE AND d.source_file != '' "+
			"GROUP BY d.source_file "+
			"ORDER BY n DESC LIMIT 20")

	var matches []PatternMatch
	for _, row := range rows {
		sf, _ := row["source_file"].(string)
		n := 0
		if v, ok := row["n"].(float64); ok {
			n = int(v)
		}
		if sf != "" && n > 0 {
			matches = append(matches, PatternMatch{
				File:        sf,
				Occurrences: n,
			})
		}
	}
	return matches
}

// InferPatterns extracts searchable code patterns from task descriptions.
// Maps natural language descriptions to LIKE patterns for body search.
func InferPatterns(objective string, steps []string) []string {
	text := strings.ToLower(objective + " " + strings.Join(steps, " "))

	var patterns []string

	// For loop modernization
	if strings.Contains(text, "for loop") || strings.Contains(text, "range over") {
		patterns = append(patterns, "for %:= 0%<%++")
		patterns = append(patterns, "for %:= 0%<%+=")
	}

	// Error handling patterns
	if strings.Contains(text, "error handling") || strings.Contains(text, "error wrap") {
		patterns = append(patterns, "if err != nil")
	}

	// Package comments / documentation: can't search for missing comments
	// via body search, so no patterns to add for this case.

	// Rename patterns
	if strings.Contains(text, "rename") {
		// Extract the old name from the task if possible
		// "Rename IsValidDBNameChar to IsInvalidDBNameChar"
		for _, word := range strings.Fields(objective) {
			if len(word) > 8 && word[0] >= 'A' && word[0] <= 'Z' {
				patterns = append(patterns, word)
			}
		}
	}

	// Import migration
	if strings.Contains(text, "import") || strings.Contains(text, "migrate") {
		// Look for old import paths mentioned in the task
		for _, word := range strings.Fields(objective) {
			if strings.Contains(word, "/") && strings.Contains(word, ".") {
				patterns = append(patterns, word)
			}
		}
	}

	// Deprecated API patterns
	if strings.Contains(text, "deprecat") {
		patterns = append(patterns, "Deprecated")
	}

	// Generic: look for CamelCase identifiers in the objective that might be code
	for _, word := range strings.Fields(objective) {
		// CamelCase words longer than 6 chars are likely code identifiers
		if len(word) > 6 && word[0] >= 'A' && word[0] <= 'Z' &&
			strings.ContainsAny(word[1:], "ABCDEFGHIJKLMNOPQRSTUVWXYZ") &&
			!strings.ContainsAny(word, " .,;:!?()[]{}") {
			patterns = append(patterns, word)
		}
	}

	return dedup(patterns)
}

func dedup(ss []string) []string {
	seen := make(map[string]bool)
	var result []string
	for _, s := range ss {
		lower := strings.ToLower(s)
		if !seen[lower] {
			seen[lower] = true
			result = append(result, s)
		}
	}
	return result
}

// PatternSearchForPlan runs body pattern search based on the plan's objective.
// Returns files containing inferred patterns, with source_file basenames
// that can be compared against the plan's filesToModify.
func PatternSearchForPlan(cwd string, objective string, steps []string, planFiles []string) []PatternMatch {
	patterns := InferPatterns(objective, steps)
	if len(patterns) == 0 {
		return nil
	}

	// Build plan file set for exclusion
	planSet := make(map[string]bool)
	for _, f := range planFiles {
		planSet[filepath.Base(f)] = true
	}

	// Search for each pattern and merge results
	fileScores := make(map[string]int)
	for _, pattern := range patterns {
		matches := SearchBodies(cwd, pattern)
		for _, m := range matches {
			if !planSet[m.File] {
				fileScores[m.File] += m.Occurrences
			}
		}
	}

	var result []PatternMatch
	for file, score := range fileScores {
		result = append(result, PatternMatch{File: file, Occurrences: score})
	}

	// Sort by occurrences
	sort.Slice(result, func(i, j int) bool {
		return result[i].Occurrences > result[j].Occurrences
	})

	if len(result) > 10 {
		result = result[:10]
	}
	return result
}
