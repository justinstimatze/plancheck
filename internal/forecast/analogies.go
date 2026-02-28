// analogies.go finds similar code patterns across indexed repositories.
//
// When a plan adds something new, this searches defn-indexed repos for
// definitions with similar names/patterns and reports what structural
// relationships those definitions have. This is the cross-project
// learning signal — the LLM has it implicitly in training data, we
// make it explicit and grounded.
//
// Repo selection: auto-detected from go.mod imports (genre matching)
// or from the set of repos in ~/.plancheck/datasets/repos/.
package forecast

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
)

// Analogy is a similar definition found in another project.
type Analogy struct {
	Repo       string   `json:"repo"`
	Name       string   `json:"name"`
	Kind       string   `json:"kind"`
	Receiver   string   `json:"receiver,omitempty"`
	Callers    int      `json:"callers"`
	SourceFile string   `json:"sourceFile"`
	References []string `json:"references,omitempty"`
}

// AnalogyResult is the output of a cross-project search.
type AnalogyResult struct {
	Query     string    `json:"query"`
	Analogies []Analogy `json:"analogies"`
	Pattern   []string  `json:"pattern,omitempty"` // common references across analogies
	Repos     int       `json:"repos"`
}

// FindAnalogies searches across all defn-indexed repos.
func FindAnalogies(searchTerm string) AnalogyResult {
	return FindAnalogiesInRepos(searchTerm, nil)
}

// FindAnalogiesInRepos searches specific repos (genre-filtered).
// If repos is nil, searches all available repos.
func FindAnalogiesInRepos(searchTerm string, repos []string) AnalogyResult {
	result := AnalogyResult{Query: searchTerm}

	repoDir := reposDir()
	if repoDir == "" {
		return result
	}

	// Build repo list: either the filtered set or all available
	var repoDirs []string
	if len(repos) > 0 {
		for _, r := range repos {
			dir := filepath.Join(repoDir, r)
			if _, err := os.Stat(filepath.Join(dir, ".defn")); err == nil {
				repoDirs = append(repoDirs, r)
			}
		}
	} else {
		entries, err := os.ReadDir(repoDir)
		if err != nil {
			return result
		}
		for _, e := range entries {
			if e.IsDir() {
				repoDirs = append(repoDirs, e.Name())
			}
		}
	}

	refCounts := make(map[string]int)
	reposMatched := make(map[string]bool)

	// Capitalize first letter for CamelCase search
	capTerm := searchTerm
	if len(capTerm) > 0 && capTerm[0] >= 'a' && capTerm[0] <= 'z' {
		capTerm = string(capTerm[0]-32) + capTerm[1:]
	}

	for _, repo := range repoDirs {
		defnDir := filepath.Join(repoDir, repo, ".defn")
		if _, err := os.Stat(defnDir); err != nil {
			continue
		}

		// Search for matching definitions
		sql := "SELECT name, kind, receiver, source_file, " +
			"(SELECT COUNT(*) FROM `references` r WHERE r.to_def = definitions.id) as callers " +
			"FROM definitions WHERE (name LIKE '%" + searchTerm + "%' OR name LIKE '%" + capTerm + "%') " +
			"AND test = FALSE AND exported = TRUE ORDER BY callers DESC LIMIT 3"

		rows := queryDolt(defnDir, sql)
		for _, row := range rows {
			name := strField(row, "name")
			kind := strField(row, "kind")
			receiver := strField(row, "receiver")
			callers := intField(row, "callers")
			sourceFile := strField(row, "source_file")

			if callers == 0 {
				continue
			}

			reposMatched[repo] = true

			// Get what this definition references (its callees)
			var refs []string
			refSQL := "SELECT DISTINCT d2.name FROM definitions d " +
				"JOIN `references` r ON r.from_def = d.id " +
				"JOIN definitions d2 ON r.to_def = d2.id " +
				"WHERE d.name = '" + name + "' AND d.test = FALSE AND d2.test = FALSE " +
				"LIMIT 10"
			refRows := queryDolt(defnDir, refSQL)
			for _, rr := range refRows {
				if n := strField(rr, "name"); n != "" {
					refs = append(refs, n)
					refCounts[n]++
				}
			}

			// Query expansion: find siblings that share the same callees.
			// Only expand when the initial match has good structural signal
			// (3+ callers = well-connected, not a dead-end function).
			if callers < 3 {
				continue // skip expansion for low-quality matches
			}
			for _, ref := range refs {
				siblingSQL := "SELECT DISTINCT d.name, d.source_file FROM definitions d " +
					"JOIN `references` r ON r.from_def = d.id " +
					"JOIN definitions target ON r.to_def = target.id " +
					"WHERE target.name = '" + ref + "' AND d.test = FALSE AND d.exported = TRUE " +
					"AND d.name != '" + name + "' " +
					"ORDER BY (SELECT COUNT(*) FROM `references` r2 WHERE r2.to_def = d.id) DESC LIMIT 5"
				sibRows := queryDolt(defnDir, siblingSQL)
				for _, sr := range sibRows {
					sibName := strField(sr, "name")
					if sibName != "" && !reposMatched[repo+"/"+sibName] {
						reposMatched[repo+"/"+sibName] = true
						// Add sibling's callees to the pattern
						sibRefSQL := "SELECT DISTINCT d2.name FROM definitions d " +
							"JOIN `references` r ON r.from_def = d.id " +
							"JOIN definitions d2 ON r.to_def = d2.id " +
							"WHERE d.name = '" + sibName + "' AND d.test = FALSE AND d2.test = FALSE LIMIT 5"
						for _, srr := range queryDolt(defnDir, sibRefSQL) {
							if n := strField(srr, "name"); n != "" {
								refCounts[n]++
							}
						}
					}
				}
			}

			result.Analogies = append(result.Analogies, Analogy{
				Repo:       repo,
				Name:       name,
				Kind:       kind,
				Receiver:   receiver,
				Callers:    callers,
				SourceFile: sourceFile,
				References: refs,
			})
		}
	}

	result.Repos = len(reposMatched)

	// Common structural pattern: references appearing in 2+ repos
	for ref, count := range refCounts {
		if count >= 2 {
			result.Pattern = append(result.Pattern, ref)
		}
	}

	return result
}

func reposDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	dir := filepath.Join(home, ".plancheck", "datasets", "repos")
	if _, err := os.Stat(dir); err != nil {
		return ""
	}
	return dir
}

func queryDolt(defnDir, sql string) []map[string]interface{} {
	cmd := exec.Command("dolt", "sql", "-q", sql, "-r", "json")
	cmd.Dir = defnDir
	cmd.Stderr = nil
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var r struct {
		Rows []map[string]interface{} `json:"rows"`
	}
	json.Unmarshal(out, &r)
	return r.Rows
}

func strField(row map[string]interface{}, key string) string {
	v, _ := row[key].(string)
	return v
}

func intField(row map[string]interface{}, key string) int {
	v, _ := row[key].(float64)
	return int(v)
}
