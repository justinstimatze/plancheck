package plan

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/justinstimatze/plancheck/internal/types"
)

// conventionPeer is a file suggested by directory convention patterns.
type conventionPeer struct {
	file     string // basename (e.g., "http.go")
	fullPath string // relative path from cwd
	pattern  string // convention name (e.g., "create.go+http.go")
	planFile string // which plan file triggered this
}

// findConventionPeers detects per-project file co-occurrence conventions.
// If basename X and Y co-occur in >threshold directories, and a plan file
// matches X but Y is absent from the plan, Y is suggested.
func findConventionPeers(planFiles []string, cwd string) []conventionPeer {
	const minOccurrences = 5

	// Collect leaf directories with .go files
	leafDirs := findLeafGoDirs(cwd)
	if len(leafDirs) < minOccurrences {
		return nil
	}

	// Build basename co-occurrence: for each leaf dir, record which basenames exist
	dirBasenames := make(map[string]map[string]bool) // dir → set of basenames
	for _, dir := range leafDirs {
		entries, err := os.ReadDir(filepath.Join(cwd, dir))
		if err != nil {
			continue
		}
		bases := make(map[string]bool)
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".go") && !strings.HasSuffix(e.Name(), "_test.go") {
				bases[e.Name()] = true
			}
		}
		if len(bases) >= 2 {
			dirBasenames[dir] = bases
		}
	}

	// Count pair co-occurrences
	type pair struct{ a, b string }
	pairCount := make(map[pair]int)
	for _, bases := range dirBasenames {
		names := make([]string, 0, len(bases))
		for n := range bases {
			names = append(names, n)
		}
		for i := 0; i < len(names); i++ {
			for j := i + 1; j < len(names); j++ {
				a, b := names[i], names[j]
				if a > b {
					a, b = b, a
				}
				pairCount[pair{a, b}]++
			}
		}
	}

	// Build convention set: pairs with enough occurrences
	conventions := make(map[pair]bool)
	for p, count := range pairCount {
		if count >= minOccurrences {
			conventions[p] = true
		}
	}
	if len(conventions) == 0 {
		return nil
	}

	// Build plan basename → full path map
	planBases := make(map[string]string) // basename → relative path
	planBaseSet := make(map[string]bool)
	for _, f := range planFiles {
		base := filepath.Base(f)
		planBases[base] = f
		planBaseSet[base] = true
	}

	// For each plan file, check conventions
	var peers []conventionPeer
	seen := make(map[string]bool) // dedup by fullPath
	for _, f := range planFiles {
		base := filepath.Base(f)
		dir := filepath.Dir(f)

		// Check which files exist in this directory
		entries, err := os.ReadDir(filepath.Join(cwd, dir))
		if err != nil {
			continue
		}
		dirFiles := make(map[string]bool)
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".go") && !strings.HasSuffix(e.Name(), "_test.go") {
				dirFiles[e.Name()] = true
			}
		}

		// Check each convention
		for conv := range conventions {
			var other string
			if conv.a == base {
				other = conv.b
			} else if conv.b == base {
				other = conv.a
			} else {
				continue
			}

			// Other side must exist in this dir but not be in the plan
			if !dirFiles[other] {
				continue
			}
			otherPath := filepath.ToSlash(filepath.Join(dir, other))
			if planBaseSet[other] || seen[otherPath] {
				continue
			}
			// Also skip if the full path is in the plan
			skip := false
			for _, pf := range planFiles {
				if pf == otherPath {
					skip = true
					break
				}
			}
			if skip {
				continue
			}

			seen[otherPath] = true
			peers = append(peers, conventionPeer{
				file:     other,
				fullPath: otherPath,
				pattern:  conv.a + "+" + conv.b,
				planFile: f,
			})
		}
	}

	return peers
}

// findLeafGoDirs returns directories that contain .go files but have no
// subdirectories that also contain .go files.
func findLeafGoDirs(cwd string) []string {
	goFileDirs := make(map[string]bool)

	filepath.WalkDir(cwd, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		// Skip hidden dirs and common non-source dirs
		if d.IsDir() {
			name := d.Name()
			if strings.HasPrefix(name, ".") || name == "vendor" || name == "testdata" || name == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(d.Name(), ".go") && !strings.HasSuffix(d.Name(), "_test.go") {
			rel, err := filepath.Rel(cwd, filepath.Dir(path))
			if err == nil {
				goFileDirs[filepath.ToSlash(rel)] = true
			}
		}
		return nil
	})

	// Filter to leaf dirs: no child dir also in the set
	var leaves []string
	for dir := range goFileDirs {
		isLeaf := true
		for other := range goFileDirs {
			if other != dir && strings.HasPrefix(other, dir+"/") {
				isLeaf = false
				break
			}
		}
		if isLeaf {
			leaves = append(leaves, dir)
		}
	}
	return leaves
}

// suggestCreationPeers suggests files that should be created alongside filesToCreate.
// Uses the same convention detection as findConventionPeers but applied to new files:
// if the plan creates "pin.go" in a new directory, and the codebase convention says
// "command.go always pairs with http.go" (5+ dirs), suggest creating http.go too.
//
// Returns suggestions as signals (not convention peers) since these are new files.
func suggestCreationPeers(filesToCreate []string, cwd string) []types.Signal {
	if len(filesToCreate) == 0 {
		return nil
	}

	const minOccurrences = 5

	leafDirs := findLeafGoDirs(cwd)
	if len(leafDirs) < minOccurrences {
		return nil
	}

	// Build basename frequency: how often does each basename appear across leaf dirs
	basenameFreq := make(map[string]int)
	// Build pair co-occurrence (same as findConventionPeers)
	type pair struct{ a, b string }
	pairCount := make(map[pair]int)
	for _, dir := range leafDirs {
		entries, err := os.ReadDir(filepath.Join(cwd, dir))
		if err != nil {
			continue
		}
		var bases []string
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".go") && !strings.HasSuffix(e.Name(), "_test.go") {
				bases = append(bases, e.Name())
				basenameFreq[e.Name()]++
			}
		}
		for i := 0; i < len(bases); i++ {
			for j := i + 1; j < len(bases); j++ {
				a, b := bases[i], bases[j]
				if a > b {
					a, b = b, a
				}
				pairCount[pair{a, b}]++
			}
		}
	}

	conventions := make(map[pair]bool)
	for p, count := range pairCount {
		if count >= minOccurrences {
			conventions[p] = true
		}
	}
	if len(conventions) == 0 {
		return nil
	}

	// Find the most common companion basenames: files that appear in many
	// convention pairs. These are "expected" files in a command directory.
	companionScore := make(map[string]int)
	for p, count := range pairCount {
		if count >= minOccurrences {
			companionScore[p.a] += count
			companionScore[p.b] += count
		}
	}

	// For each file being created, suggest companions
	createSet := make(map[string]bool)
	for _, f := range filesToCreate {
		createSet[filepath.Base(f)] = true
		createSet[filepath.ToSlash(f)] = true
	}

	var signals []types.Signal
	seen := make(map[string]bool)

	for _, f := range filesToCreate {
		base := filepath.Base(f)
		dir := filepath.ToSlash(filepath.Dir(f))

		// Exact convention pairs (if basename matches)
		for conv := range conventions {
			var other string
			if conv.a == base {
				other = conv.b
			} else if conv.b == base {
				other = conv.a
			} else {
				continue
			}
			otherPath := dir + "/" + other
			if createSet[other] || createSet[otherPath] || seen[otherPath] {
				continue
			}
			if _, err := os.Stat(filepath.Join(cwd, otherPath)); err == nil {
				continue
			}
			seen[otherPath] = true
			signals = append(signals, types.Signal{
				Probe:   "creation-convention",
				File:    otherPath,
				Message: fmt.Sprintf("convention: %s and %s co-occur in %d+ directories \u2014 consider also creating %s", base, other, minOccurrences, otherPath),
			})
		}

		// Top companion files by frequency (even if basename doesn\u2019t match a pair).
		// Catches: creating archive.go \u2192 suggest http.go because it\u2019s the most
		// common companion in command directories.
		type scored struct {
			name  string
			score int
		}
		var companions []scored
		for name, score := range companionScore {
			if name == base || createSet[name] {
				continue
			}
			otherPath := dir + "/" + name
			if seen[otherPath] {
				continue
			}
			if _, err := os.Stat(filepath.Join(cwd, otherPath)); err == nil {
				continue
			}
			companions = append(companions, scored{name, score})
		}
		for i := 0; i < len(companions); i++ {
			for j := i + 1; j < len(companions); j++ {
				if companions[j].score > companions[i].score {
					companions[i], companions[j] = companions[j], companions[i]
				}
			}
		}
		cap := 2
		if len(companions) < cap {
			cap = len(companions)
		}
		for _, c := range companions[:cap] {
			otherPath := dir + "/" + c.name
			if seen[otherPath] {
				continue
			}
			seen[otherPath] = true
			signals = append(signals, types.Signal{
				Probe:   "creation-convention",
				File:    otherPath,
				Message: fmt.Sprintf("convention: %s appears in %d+ command directories \u2014 new commands typically need it", c.name, c.score/2),
			})
		}
	}

	return signals
}

// findSharedPkgPeers finds files in shared/ subdirectories of plan file parents.
// Pattern: plan has pkg/cmd/X/Y/file.go → suggest pkg/cmd/X/shared/*.go
// This catches the common Go CLI pattern where command implementations
// import types/helpers from a sibling shared/ package.
func findSharedPkgPeers(planFiles []string, cwd string) []conventionPeer {
	planPathSet := make(map[string]bool)
	for _, f := range planFiles {
		planPathSet[filepath.ToSlash(f)] = true
	}

	seen := make(map[string]bool)
	var peers []conventionPeer

	for _, f := range planFiles {
		dir := filepath.ToSlash(filepath.Dir(f))
		// Walk up to find parent that might have a shared/ sibling
		// e.g., pkg/cmd/pr/create → check pkg/cmd/pr/shared/
		parts := strings.Split(dir, "/")
		if len(parts) < 2 {
			continue
		}
		parent := strings.Join(parts[:len(parts)-1], "/")
		sharedDir := parent + "/shared"

		absShared := filepath.Join(cwd, sharedDir)
		entries, err := os.ReadDir(absShared)
		if err != nil {
			continue
		}

		// Cap shared-pkg to 3 files per shared dir to avoid flooding
		count := 0
		for _, e := range entries {
			if count >= 3 {
				break
			}
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
				continue
			}
			fullPath := sharedDir + "/" + e.Name()
			if planPathSet[fullPath] || seen[fullPath] {
				continue
			}
			seen[fullPath] = true
			peers = append(peers, conventionPeer{
				file:     e.Name(),
				fullPath: fullPath,
				pattern:  "shared-pkg",
				planFile: f,
			})
			count++
		}
	}

	return peers
}

// findSiblingCmdPeers finds files in sibling command directories.
// Pattern: plan has pkg/cmd/X/Y/file.go → suggest pkg/cmd/X/Z/*.go
// where Z is a sibling subcommand directory (not shared/ — that's separate).
// Only fires when the task objective mentions the sibling command name.
func findSiblingCmdPeers(planFiles []string, objective string, steps []string, cwd string) []conventionPeer {
	// Build token set from objective+steps for gating
	allText := strings.ToLower(objective)
	for _, s := range steps {
		allText += " " + strings.ToLower(s)
	}
	tokens := make(map[string]bool)
	for _, w := range strings.Fields(allText) {
		w = strings.Trim(w, ".,;:\"'`()[]{}!?")
		if len(w) >= 2 {
			tokens[w] = true
		}
	}

	planPathSet := make(map[string]bool)
	for _, f := range planFiles {
		planPathSet[filepath.ToSlash(f)] = true
	}

	seen := make(map[string]bool)
	var peers []conventionPeer

	for _, f := range planFiles {
		dir := filepath.ToSlash(filepath.Dir(f))
		parts := strings.Split(dir, "/")
		if len(parts) < 2 {
			continue
		}
		parent := strings.Join(parts[:len(parts)-1], "/")

		// List sibling directories
		absParent := filepath.Join(cwd, parent)
		entries, err := os.ReadDir(absParent)
		if err != nil {
			continue
		}

		for _, e := range entries {
			if !e.IsDir() || e.Name() == "shared" {
				continue
			}
			sibDir := parent + "/" + e.Name()
			if sibDir == dir {
				continue // skip self
			}

			// Gate: sibling dir name must appear in task text
			if !tokens[strings.ToLower(e.Name())] {
				continue
			}

			// List .go files in sibling dir
			absSib := filepath.Join(cwd, sibDir)
			sibEntries, err := os.ReadDir(absSib)
			if err != nil {
				continue
			}

			for _, se := range sibEntries {
				if se.IsDir() || !strings.HasSuffix(se.Name(), ".go") || strings.HasSuffix(se.Name(), "_test.go") {
					continue
				}
				fullPath := sibDir + "/" + se.Name()
				if planPathSet[fullPath] || seen[fullPath] {
					continue
				}
				seen[fullPath] = true
				peers = append(peers, conventionPeer{
					file:     se.Name(),
					fullPath: fullPath,
					pattern:  "sibling-cmd:" + e.Name(),
					planFile: f,
				})
			}
		}
	}

	return peers
}

// findCmdutilPeers suggests pkg/cmdutil/ files when the task mentions
// keywords that indicate infrastructure changes (flags, errors, exit codes).
func findCmdutilPeers(planFiles []string, objective string, steps []string, cwd string) []conventionPeer {
	allText := strings.ToLower(objective)
	for _, s := range steps {
		allText += " " + strings.ToLower(s)
	}

	// Keyword gates: only fire when task mentions specific infrastructure concepts
	type cmdutilTrigger struct {
		keywords []string
		files    []string
	}
	triggers := []cmdutilTrigger{
		{keywords: []string{"flag", "--"}, files: []string{"flags.go"}},
		{keywords: []string{"error", "exit code", "exit status"}, files: []string{"errors.go"}},
		{keywords: []string{"factory", "cmdutil"}, files: []string{"factory.go"}},
		{keywords: []string{"json", "--json", "exporter"}, files: []string{"json_flags.go"}},
		{keywords: []string{"auth", "login", "token"}, files: []string{"auth_check.go"}},
		{keywords: []string{"repo", "remote", "host"}, files: []string{"repo_override.go"}},
	}

	// Check if any plan file is already in pkg/cmdutil/
	for _, f := range planFiles {
		if strings.Contains(f, "pkg/cmdutil/") {
			return nil // already covered
		}
	}

	// Find the cmdutil directory
	cmdutilDir := "pkg/cmdutil"
	absCmdutil := filepath.Join(cwd, cmdutilDir)
	if _, err := os.Stat(absCmdutil); err != nil {
		return nil
	}

	planPathSet := make(map[string]bool)
	for _, f := range planFiles {
		planPathSet[filepath.ToSlash(f)] = true
	}

	var peers []conventionPeer
	seen := make(map[string]bool)

	for _, t := range triggers {
		triggered := false
		for _, kw := range t.keywords {
			if strings.Contains(allText, kw) {
				triggered = true
				break
			}
		}
		if !triggered {
			continue
		}

		for _, file := range t.files {
			fullPath := cmdutilDir + "/" + file
			absPath := filepath.Join(cwd, fullPath)
			if _, err := os.Stat(absPath); err != nil {
				continue
			}
			if planPathSet[fullPath] || seen[fullPath] {
				continue
			}
			seen[fullPath] = true
			peers = append(peers, conventionPeer{
				file:     file,
				fullPath: fullPath,
				pattern:  "cmdutil",
				planFile: planFiles[0],
			})
		}
	}

	return peers
}
