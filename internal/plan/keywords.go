package plan

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/justinstimatze/plancheck/internal/types"
)

// extractTestPatchDirs extracts production directories from test patch file paths.
// Test files at pkg/cmd/X/Y/Z_test.go → production dir pkg/cmd/X/Y/.
// Returns directories NOT already covered by plan files (max 5).
func extractTestPatchDirs(testPatch string, planFiles []string) []string {
	// Build set of directories already in the plan
	planDirs := make(map[string]bool)
	for _, pf := range planFiles {
		planDirs[filepath.Dir(pf)] = true
	}

	// Extract file paths from diff headers
	diffRe := regexp.MustCompile(`diff --git a/(.*?) b/`)
	matches := diffRe.FindAllStringSubmatch(testPatch, -1)

	dirCounts := make(map[string]int)
	for _, m := range matches {
		file := m[1]
		dir := filepath.Dir(file)
		if dir == "." || dir == "" {
			continue
		}
		// Skip acceptance/testdata directories — these aren't production code
		if strings.Contains(dir, "testdata") || strings.Contains(dir, "acceptance") {
			continue
		}
		// Only count test file directories (the production dir is the same)
		if !strings.HasSuffix(file, "_test.go") {
			continue
		}
		// Skip if already covered by plan
		if planDirs[dir] {
			continue
		}
		dirCounts[dir]++
	}

	// Sort by frequency, return top 5
	type dc struct {
		dir   string
		count int
	}
	var sorted []dc
	for d, c := range dirCounts {
		sorted = append(sorted, dc{d, c})
	}
	for i := 0; i < len(sorted); i++ {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[j].count > sorted[i].count {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}
	var result []string
	for i, s := range sorted {
		if i >= 5 {
			break
		}
		result = append(result, s.dir)
	}
	return result
}

// keywordDirGap represents an objective keyword that maps to a codebase directory
// not covered by any plan file.
type keywordDirGap struct {
	Keyword    string // the extracted keyword (e.g., "codespace")
	Dir        string // relative directory path in codebase
	CoveredDir string // peer directory that IS covered (e.g., "pkg/cmd/run")
}

// tokenize splits text into lowercase alpha tokens, stripping punctuation.
// Does NOT filter "noise" words — directory names like "list", "add", "run"
// are common English words, so filtering would cause false negatives.
// Non-matching tokens are harmlessly ignored during directory lookup.
func tokenize(text string) []string {
	words := strings.FieldsFunc(text, func(r rune) bool {
		return !unicode.IsLetter(r)
	})

	seen := make(map[string]bool)
	var tokens []string
	for _, w := range words {
		w = strings.ToLower(w)
		if len(w) < 2 || seen[w] {
			continue
		}
		seen[w] = true
		tokens = append(tokens, w)
		// Also add singularized form for matching "Issues" → "issue" dir
		s := singularize(w)
		if s != w && !seen[s] {
			seen[s] = true
			tokens = append(tokens, s)
		}
	}
	return tokens
}

// singularize does minimal English singularization for directory matching.
func singularize(s string) string {
	if strings.HasSuffix(s, "ies") && len(s) > 4 {
		return s[:len(s)-3] + "y"
	}
	if strings.HasSuffix(s, "es") && len(s) > 3 {
		stem := s[:len(s)-2]
		if strings.HasSuffix(stem, "ch") || strings.HasSuffix(stem, "sh") ||
			strings.HasSuffix(stem, "x") || strings.HasSuffix(stem, "z") {
			return stem
		}
		return s[:len(s)-1]
	}
	if strings.HasSuffix(s, "s") && !strings.HasSuffix(s, "ss") && len(s) > 2 {
		return s[:len(s)-1]
	}
	return s
}

// extractEntities parses objective and steps for explicitly named targets.
// These become mandatory coverage targets that bypass the parent-peer requirement.
func extractEntities(objective string, steps []string) []string {
	allText := objective
	for _, s := range steps {
		allText += " " + s
	}

	seen := make(map[string]bool)
	var entities []string
	add := func(s string) {
		s = strings.ToLower(strings.TrimSpace(s))
		if len(s) >= 2 && !seen[s] {
			seen[s] = true
			entities = append(entities, s)
		}
	}

	// Backticked words: `secret`, `variable`
	{
		// Odd-indexed parts are inside backticks
		parts := strings.Split(allText, "`")
		for i := 1; i < len(parts); i += 2 {
			for _, w := range strings.Fields(parts[i]) {
				add(w)
			}
		}
	}

	// "X and Y commands/subcommands" pattern
	lower := strings.ToLower(allText)
	for _, suffix := range []string{" commands", " subcommands", " endpoints", " packages"} {
		idx := strings.Index(lower, suffix)
		if idx < 0 {
			continue
		}
		// Look back for "X and Y" or "X, Y, and Z"
		prefix := lower[:idx]
		words := strings.Fields(prefix)
		if len(words) < 3 {
			continue
		}
		// Find "and" and collect surrounding words
		for i, w := range words {
			if w == "and" && i > 0 && i < len(words)-1 {
				add(words[i-1])
				add(words[i+1])
				// Also collect comma-separated words before "and"
				for j := i - 2; j >= 0; j-- {
					w := strings.Trim(words[j], ",")
					if w == "" || w == "the" || w == "to" || w == "for" {
						break
					}
					add(w)
				}
			}
		}
	}

	// "X command" / "X subcommand" (singular)
	for _, suffix := range []string{" command", " subcommand", " endpoint", " package"} {
		idx := 0
		for {
			pos := strings.Index(lower[idx:], suffix)
			if pos < 0 {
				break
			}
			pos += idx
			// Check it's not "commands" (plural, already handled above)
			end := pos + len(suffix)
			if end < len(lower) && lower[end] == 's' {
				idx = end
				continue
			}
			// Extract preceding word
			prefix := strings.TrimSpace(lower[:pos])
			words := strings.Fields(prefix)
			if len(words) > 0 {
				add(words[len(words)-1])
			}
			idx = end
		}
	}

	// Quoted strings: "secret", "variable"
	for _, quote := range []byte{'"', '\''} {
		parts := strings.Split(allText, string(quote))
		for i := 1; i < len(parts); i += 2 {
			w := strings.TrimSpace(parts[i])
			if len(w) >= 2 && len(w) <= 30 && !strings.Contains(w, " ") {
				add(w)
			}
		}
	}

	return entities
}

// matchKeywordsToDirs tokenizes objective+steps, scans codebase directories,
// and finds directories matching task tokens that aren't covered by plan files.
//
// To avoid noise from common words like "list" matching many directories,
// gaps are only reported when the token matches directories at the SAME depth
// as a covered directory. This ensures the signal fires for "task mentions
// domains A and B but plan only covers A" patterns.
//
// Entity-extracted targets (backticked, "X command", etc.) bypass the
// parent-peer filter — they're explicitly named in the task description.
//
// Returns gaps and ranked files from uncovered directories.
func matchKeywordsToDirs(objective string, steps []string, planFiles []string, cwd string) ([]keywordDirGap, []types.RankedFile) {
	allText := objective
	for _, s := range steps {
		allText += " " + s
	}
	tokens := tokenize(allText)
	if len(tokens) == 0 {
		return nil, nil
	}

	tokenSet := make(map[string]bool, len(tokens))
	for _, t := range tokens {
		tokenSet[t] = true
	}

	// Walk codebase directories (max 4 levels deep) looking for token matches.
	type dirMatch struct {
		token string
		dir   string
		depth int // directory depth
	}
	var allMatches []dirMatch
	tokenDirCount := make(map[string]int) // how many dirs each token matches
	walkDirs(cwd, "", 0, 4, func(relDir string) {
		base := strings.ToLower(filepath.Base(relDir))
		if tokenSet[base] {
			depth := strings.Count(relDir, "/")
			allMatches = append(allMatches, dirMatch{token: base, dir: relDir, depth: depth})
			tokenDirCount[base]++
		}
	})

	if len(allMatches) == 0 {
		return nil, nil
	}

	// Build plan coverage: which directories are ancestors of plan files
	planDirSet := make(map[string]bool)
	for _, f := range planFiles {
		d := filepath.Dir(f)
		for d != "." && d != "" {
			planDirSet[d] = true
			d = filepath.Dir(d)
		}
	}

	// Find covered directories and their parents — we only report gaps
	// that share a parent with a covered directory at the same depth.
	// This prevents "auth" at pkg/cmd/attestation/auth from being flagged
	// when pkg/cmd/auth is in the plan (different parents).
	type coveredInfo struct {
		parent string
		depth  int
	}
	// Map parent+depth → covered directory path (for relational messages)
	coveredPeerDir := make(map[coveredInfo]string)
	for _, m := range allMatches {
		if planDirSet[m.dir] {
			parent := filepath.Dir(m.dir)
			coveredPeerDir[coveredInfo{parent: parent, depth: m.depth}] = m.dir
		}
	}

	// Entity extraction: explicitly named targets bypass parent-peer filter
	entitySet := make(map[string]bool)
	for _, e := range extractEntities(objective, steps) {
		entitySet[e] = true
	}

	var gaps []keywordDirGap
	var ranked []types.RankedFile

	for _, m := range allMatches {
		if planDirSet[m.dir] {
			continue // already covered
		}

		isEntity := entitySet[m.token]
		parent := filepath.Dir(m.dir)
		peerDir, hasPeer := coveredPeerDir[coveredInfo{parent: parent, depth: m.depth}]

		if !isEntity {
			// Non-entity: require parent-peer match
			if !hasPeer {
				continue
			}
			// Suppress generic subcommand names that match many directories
			if tokenDirCount[m.token] > 3 {
				continue
			}
		} else {
			// Entity: still suppress if it matches too many dirs (>5 = likely noise)
			if tokenDirCount[m.token] > 5 {
				continue
			}
			// For gap message, find any covered peer if available
			if !hasPeer {
				// Find any covered dir as reference
				for _, cm := range allMatches {
					if planDirSet[cm.dir] {
						peerDir = cm.dir
						hasPeer = true
						break
					}
				}
				if !hasPeer {
					peerDir = "(none)"
				}
			}
		}

		gaps = append(gaps, keywordDirGap{Keyword: m.token, Dir: m.dir, CoveredDir: peerDir})

		// List .go files in uncovered directory + immediate subdirs, capped.
		// Prefer files with names matching other task tokens or mirroring
		// the subpath structure of covered plan files.
		goFiles := listGoFilesShallow(filepath.Join(cwd, m.dir))
		scored := scoreFileRelevance(goFiles, tokenSet, planFiles, peerDir)
		cap := 3
		if len(scored) < cap {
			cap = len(scored)
		}
		for _, sf := range scored[:cap] {
			rel, err := filepath.Rel(cwd, sf.path)
			if err != nil {
				continue
			}
			rel = filepath.ToSlash(rel)
			source := "keyword-dir"
			reason := "plan covers " + peerDir + "/ but NOT " + m.dir + "/ — task requires both"
			fileScore := sf.score
			if isEntity {
				source = "entity-dir"
				reason = "task explicitly names \"" + m.token + "\" — " + m.dir + "/ must be covered"
				fileScore += 0.2 // boost entity-matched files into top 5
				if fileScore > 1.0 {
					fileScore = 1.0
				}
			}
			ranked = append(ranked, types.RankedFile{
				File:   filepath.Base(sf.path),
				Path:   rel,
				Score:  fileScore,
				Source: source,
				Reason: reason,
			})
		}
	}

	return gaps, ranked
}

// walkDirs recursively visits directories up to maxDepth, calling fn for each.
func walkDirs(root, rel string, depth, maxDepth int, fn func(relDir string)) {
	if depth > maxDepth {
		return
	}
	absDir := root
	if rel != "" {
		absDir = filepath.Join(root, rel)
	}
	entries, err := os.ReadDir(absDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if name == "." || name == ".." || name == ".git" || name == ".defn" ||
			name == "vendor" || name == "node_modules" || name == "testdata" {
			continue
		}
		childRel := name
		if rel != "" {
			childRel = filepath.Join(rel, name)
		}
		fn(filepath.ToSlash(childRel))
		walkDirs(root, childRel, depth+1, maxDepth, fn)
	}
}

// scoredFile pairs a file path with a relevance score.
type scoredFile struct {
	path  string
	score float64
}

// scoreFileRelevance scores files by how well they match the task context.
// Considers: task token matches, common/shared names, and mirror-path
// patterns (same subpath as a plan file in the covered peer directory).
func scoreFileRelevance(files []string, tokens map[string]bool, planFiles []string, coveredDir string) []scoredFile {
	// Build mirror paths: for each plan file under coveredDir, extract
	// the subpath relative to coveredDir. E.g., plan has
	// "pkg/cmd/pr/create/create.go" and coveredDir is "pkg/cmd/pr" →
	// subpath "create/create.go". We look for "create/create.go" in
	// the gap directory.
	mirrorSubs := make(map[string]bool)
	for _, pf := range planFiles {
		if strings.HasPrefix(pf, coveredDir+"/") {
			sub := strings.TrimPrefix(pf, coveredDir+"/")
			mirrorSubs[sub] = true
		}
	}

	var scored []scoredFile
	for _, f := range files {
		base := strings.TrimSuffix(filepath.Base(f), ".go")
		base = strings.ToLower(base)
		s := 0.5 // base score

		// Highest: mirror-path match (same subpath as plan file in peer dir)
		// e.g., issue/create/create.go mirrors pr/create/create.go
		subDir := filepath.Base(filepath.Dir(f))
		fileName := filepath.Base(f)
		mirrorKey := subDir + "/" + fileName
		if mirrorSubs[mirrorKey] {
			s = 0.9
		} else if tokens[base] {
			s = 0.8
		} else if base == "common" || base == "shared" || base == "utils" || base == "helpers" {
			s = 0.7
		}
		scored = append(scored, scoredFile{path: f, score: s})
	}
	sort.Slice(scored, func(i, j int) bool {
		return scored[i].score > scored[j].score
	})
	return scored
}

// listGoFilesShallow returns non-test .go files in a directory and its
// immediate subdirectories. Subdirectory files include their subdir prefix
// so they can be distinguished (e.g., "create/create.go").
func listGoFilesShallow(dir string) []string {
	var files []string
	files = append(files, listGoFiles(dir)...)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return files
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		subFiles := listGoFiles(filepath.Join(dir, e.Name()))
		files = append(files, subFiles...)
	}
	return files
}

// listGoFiles returns non-test .go files in a directory (non-recursive).
func listGoFiles(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go") {
			files = append(files, filepath.Join(dir, name))
		}
	}
	return files
}
