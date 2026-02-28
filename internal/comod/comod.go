// Package comod detects co-modification patterns from git history.
// Files that historically change together are likely to need concurrent updates.
package comod

import (
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/justinstimatze/plancheck/internal/types"
	"github.com/justinstimatze/plancheck/internal/walk"
)

const (
	baseFrequencyThreshold = 0.4
	gitLogLimit            = 200
	minCommitsForComod     = 10
)

// adjustedThreshold raises the co-change frequency threshold for small repos
// where the background co-change rate is high by chance.
// Formula: max(base, 2/sqrt(uniqueFiles)). Converges to base at ~25 files.
func adjustedThreshold(uniqueFiles int) float64 {
	if uniqueFiles <= 0 {
		return 1.0 // no files — suppress everything
	}
	t := 2.0 / math.Sqrt(float64(uniqueFiles))
	if t < baseFrequencyThreshold {
		return baseFrequencyThreshold
	}
	return t
}

// Gap is the base comod gap returned by CheckComod.
// The orchestrator in plan.Check enriches with CrossStack, Acknowledged, Hub.
type Gap struct {
	PlanFile   string
	ComodFile  string
	Frequency  float64
	Suggestion string
}

// CheckComod analyzes git history for co-modification patterns.
func CheckComod(p types.ExecutionPlan, cwd string) []Gap {
	if len(p.FilesToModify) == 0 {
		return nil
	}

	gitRoot, err := walk.GitRoot(cwd)
	if err != nil {
		return nil
	}

	cwdFromRoot, err := filepath.Rel(gitRoot, filepath.Join(cwd))
	if err != nil {
		cwdFromRoot = ""
	}
	if cwdFromRoot == "." {
		cwdFromRoot = ""
	}

	toGitPath := func(f string) string {
		if cwdFromRoot != "" {
			return filepath.ToSlash(filepath.Join(cwdFromRoot, f))
		}
		return filepath.ToSlash(f)
	}

	toCwdPath := func(f string) string {
		if cwdFromRoot != "" {
			rel, err := filepath.Rel(cwdFromRoot, f)
			if err != nil {
				return f
			}
			return filepath.ToSlash(rel)
		}
		return filepath.ToSlash(f)
	}

	logOutput, err := gitLogNameOnly(cwd)
	if err != nil {
		return nil
	}

	commits := parseCommits(logOutput)
	if len(commits) < minCommitsForComod {
		return nil // Not enough history for meaningful co-change analysis
	}

	// All files that appear in history — for fuzzy path resolution
	allGitFiles := make(map[string]bool)
	for _, commit := range commits {
		for f := range commit {
			allGitFiles[f] = true
		}
	}

	resolveGitPath := func(planFile string) string {
		exact := toGitPath(planFile)
		if allGitFiles[exact] {
			return exact
		}
		// Suffix match
		suffix := strings.TrimPrefix(planFile, "./")
		for f := range allGitFiles {
			if f == suffix || strings.HasSuffix(f, "/"+suffix) {
				return f
			}
		}
		// Basename match — only if unambiguous
		base := filepath.Base(planFile)
		var matches []string
		for f := range allGitFiles {
			if filepath.Base(f) == base {
				matches = append(matches, f)
			}
		}
		if len(matches) == 1 {
			return matches[0]
		}
		return exact
	}

	// Plan file set in repo-root-relative form
	planFileSet := make(map[string]bool)
	for _, f := range p.FilesToModify {
		planFileSet[resolveGitPath(f)] = true
	}
	for _, f := range p.FilesToCreate {
		planFileSet[resolveGitPath(f)] = true
	}

	threshold := adjustedThreshold(len(allGitFiles))

	var gaps []Gap
	for _, planFile := range p.FilesToModify {
		gitPlanFile := resolveGitPath(planFile)
		var relevant []map[string]bool
		for _, c := range commits {
			if c[gitPlanFile] {
				relevant = append(relevant, c)
			}
		}
		if len(relevant) == 0 {
			continue
		}

		coCounts := make(map[string]int)
		for _, commit := range relevant {
			for file := range commit {
				if file == gitPlanFile {
					continue
				}
				coCounts[file]++
			}
		}

		for gitComodFile, count := range coCounts {
			frequency := float64(count) / float64(len(relevant))
			if frequency < threshold {
				continue
			}
			if planFileSet[gitComodFile] {
				continue
			}
			// Must exist on disk
			diskPath := filepath.Join(gitRoot, filepath.FromSlash(gitComodFile))
			if _, err := os.Stat(diskPath); err != nil {
				continue
			}
			comodFile := toCwdPath(gitComodFile)
			gaps = append(gaps, Gap{
				PlanFile:  planFile,
				ComodFile: comodFile,
				Frequency: frequency,
				Suggestion: fmt.Sprintf("%s co-changes with %s %d%% of the time — add to filesToModify",
					comodFile, planFile, int(math.Round(frequency*100))),
			})
		}
	}

	return gaps
}

// DirGap is a directory-level co-modification gap.
// "When files in dirA change, fileB also changes X% of the time."
type DirGap struct {
	PlanDir   string  // directory containing the plan file
	ComodFile string  // file outside the directory that co-changes
	Frequency float64 // how often (0.0-1.0)
}

// CheckDirComod finds files that co-change with the DIRECTORY containing
// each plan file. This catches cross-layer coupling like:
//
//	pkg/cmd/pr/* → api/queries_pr.go
//
// where the API file co-changes with any file in the pr/ directory tree.
func CheckDirComod(p types.ExecutionPlan, cwd string) []DirGap {
	if len(p.FilesToModify) == 0 {
		return nil
	}

	gitRoot, err := walk.GitRoot(cwd)
	if err != nil {
		return nil
	}

	cwdFromRoot, err := filepath.Rel(gitRoot, filepath.Join(cwd))
	if err != nil {
		cwdFromRoot = ""
	}
	if cwdFromRoot == "." {
		cwdFromRoot = ""
	}

	toGitPath := func(f string) string {
		if cwdFromRoot != "" {
			return filepath.ToSlash(filepath.Join(cwdFromRoot, f))
		}
		return filepath.ToSlash(f)
	}

	logOutput, err := gitLogNameOnly(cwd)
	if err != nil {
		return nil
	}
	commits := parseCommits(logOutput)
	if len(commits) < minCommitsForComod {
		return nil
	}

	// Collect unique directories from plan files
	planDirs := make(map[string]bool)
	planFileSet := make(map[string]bool)
	for _, f := range p.FilesToModify {
		gitPath := toGitPath(f)
		planFileSet[gitPath] = true
		dir := filepath.ToSlash(filepath.Dir(gitPath))
		planDirs[dir] = true
		// Also add parent directory (feature root level)
		parent := filepath.ToSlash(filepath.Dir(dir))
		if parent != "." && parent != "" {
			planDirs[parent] = true
		}
	}
	for _, f := range p.FilesToCreate {
		planFileSet[toGitPath(f)] = true
	}

	// For each plan directory, find commits that touch ANY file in that dir
	// and count co-occurring files outside the dir
	threshold := adjustedThreshold(100) // use conservative threshold for dir-level
	var gaps []DirGap

	for planDir := range planDirs {
		var relevant []map[string]bool
		for _, c := range commits {
			// Does this commit touch any file in planDir?
			touchesDir := false
			for f := range c {
				fDir := filepath.ToSlash(filepath.Dir(f))
				if fDir == planDir || strings.HasPrefix(fDir, planDir+"/") {
					touchesDir = true
					break
				}
			}
			if touchesDir {
				relevant = append(relevant, c)
			}
		}
		if len(relevant) < 3 {
			continue
		}

		// Count files outside planDir that co-change
		coCounts := make(map[string]int)
		for _, commit := range relevant {
			for file := range commit {
				fDir := filepath.ToSlash(filepath.Dir(file))
				// Skip files inside the plan directory tree
				if fDir == planDir || strings.HasPrefix(fDir, planDir+"/") {
					continue
				}
				// Skip files already in the plan
				if planFileSet[file] {
					continue
				}
				coCounts[file]++
			}
		}

		for comodFile, count := range coCounts {
			frequency := float64(count) / float64(len(relevant))
			if frequency < threshold {
				continue
			}
			// Must exist on disk
			diskPath := filepath.Join(gitRoot, filepath.FromSlash(comodFile))
			if _, err := os.Stat(diskPath); err != nil {
				continue
			}
			// Convert to cwd-relative
			rel := comodFile
			if cwdFromRoot != "" {
				if r, err := filepath.Rel(cwdFromRoot, comodFile); err == nil {
					rel = filepath.ToSlash(r)
				}
			}
			gaps = append(gaps, DirGap{
				PlanDir:   planDir,
				ComodFile: rel,
				Frequency: frequency,
			})
		}
	}

	return gaps
}

func gitLogNameOnly(cwd string) (string, error) {
	cmd := exec.Command("git", "-C", cwd, "log", "--diff-filter=M",
		"--name-only", "--pretty=format:COMMIT", "-n", fmt.Sprintf("%d", gitLogLimit))
	cmd.Stderr = nil
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func parseCommits(logOutput string) []map[string]bool {
	var commits []map[string]bool
	var current map[string]bool

	for _, line := range strings.Split(logOutput, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "COMMIT" {
			current = make(map[string]bool)
			commits = append(commits, current)
		} else if trimmed != "" && current != nil {
			current[trimmed] = true
		}
	}

	return commits
}
