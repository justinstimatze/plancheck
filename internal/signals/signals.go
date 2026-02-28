// Package signals generates deterministic validation signals from plan structure.
// Checks file existence, detects test file coverage, and flags common plan errors.
package signals

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/justinstimatze/plancheck/internal/types"
	"github.com/justinstimatze/plancheck/internal/walk"
)

const (
	highChurn       = 10
	churnWindowDays = 90
)

var testableExts = regexp.MustCompile(`\.[jt]sx?$|\.py$`)

// Check runs all informational signal probes and returns findings that don't affect score.
func Check(p types.ExecutionPlan, cwd string) []types.Signal {
	var signals []types.Signal
	signals = append(signals, checkChurn(p, cwd)...)
	signals = append(signals, checkTestPairing(p, cwd)...)
	signals = append(signals, checkLockStaleness(p)...)
	signals = append(signals, checkImportChains(p, cwd)...)
	return signals
}

func checkChurn(p types.ExecutionPlan, cwd string) []types.Signal {
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

	cmd := exec.Command("git", "-C", cwd, "log", fmt.Sprintf("--since=%d days ago", churnWindowDays),
		"--diff-filter=M", "--name-only", "--pretty=format:")
	cmd.Stderr = nil
	out, err := cmd.Output()
	if err != nil {
		return nil
	}

	counts := make(map[string]int)
	for _, line := range strings.Split(string(out), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			counts[trimmed]++
		}
	}

	var signals []types.Signal
	for _, file := range p.FilesToModify {
		count := counts[toGitPath(file)]
		if count >= highChurn {
			signals = append(signals, types.Signal{
				Probe:   "churn",
				File:    file,
				Message: fmt.Sprintf("%s: %d commits in 90 days — high-churn, review for conflicts", file, count),
			})
		}
	}
	return signals
}

func checkTestPairing(p types.ExecutionPlan, cwd string) []types.Signal {
	allPlanFiles := make(map[string]bool)
	for _, f := range p.FilesToModify {
		allPlanFiles[f] = true
	}
	for _, f := range p.FilesToCreate {
		allPlanFiles[f] = true
	}
	for _, f := range p.FilesToRead {
		allPlanFiles[f] = true
	}

	var signals []types.Signal
	files := make([]string, 0, len(p.FilesToModify)+len(p.FilesToCreate))
	files = append(files, p.FilesToModify...)
	files = append(files, p.FilesToCreate...)

	for _, file := range files {
		if walk.IsTestFile(file) {
			continue
		}
		ext := filepath.Ext(file)
		if !testableExts.MatchString(ext) {
			continue
		}
		base := file[:len(file)-len(ext)]

		var testVariants []string
		if ext == ".py" {
			testVariants = []string{
				base + "_test" + ext,
				filepath.Dir(base) + "/test_" + filepath.Base(base) + ext,
			}
		} else {
			testVariants = []string{
				base + ".test" + ext,
				base + ".spec" + ext,
			}
		}

		for _, testFile := range testVariants {
			if allPlanFiles[testFile] {
				continue
			}
			fullPath := filepath.Join(cwd, testFile)
			if _, err := os.Stat(fullPath); err == nil {
				signals = append(signals, types.Signal{
					Probe:   "test-pairing",
					File:    testFile,
					Message: fmt.Sprintf("%s exists but isn't in the plan — may need updating", testFile),
				})
				break
			}
		}
	}
	return signals
}

var depFiles = map[string]bool{
	"package.json":     true,
	"requirements.txt": true,
	"pyproject.toml":   true,
	"Cargo.toml":       true,
	"go.mod":           true,
	"Gemfile":          true,
}

var installPattern = regexp.MustCompile(`(?i)\b(?:npm|bun|yarn|pip|cargo|poetry|bundle)\s+(?:install|add|ci|update|upgrade|lock)\b|\bgo\s+mod\b|\binstall\s+(?:dep|package|requirement)`)

func checkLockStaleness(p types.ExecutionPlan) []types.Signal {
	touchesDeps := false
	allFiles := make([]string, 0, len(p.FilesToModify)+len(p.FilesToCreate))
	allFiles = append(allFiles, p.FilesToModify...)
	allFiles = append(allFiles, p.FilesToCreate...)
	for _, f := range allFiles {
		if depFiles[filepath.Base(f)] {
			touchesDeps = true
			break
		}
	}
	if !touchesDeps {
		return nil
	}

	for _, s := range p.Steps {
		if installPattern.MatchString(s) {
			return nil
		}
	}

	return []types.Signal{{
		Probe:   "lock-staleness",
		Message: "Plan modifies dependency files but no step runs install/lock — lockfile will drift",
	}}
}

const importChainCollapseThreshold = 5

func checkImportChains(p types.ExecutionPlan, cwd string) []types.Signal {
	if len(p.FilesToModify) == 0 {
		return nil
	}

	planFiles := make(map[string]bool)
	for _, f := range p.FilesToModify {
		planFiles[f] = true
	}
	for _, f := range p.FilesToCreate {
		planFiles[f] = true
	}
	for _, f := range p.FilesToRead {
		planFiles[f] = true
	}

	var signals []types.Signal
	for _, file := range p.FilesToModify {
		importers := findImporters(file, cwd, planFiles)
		if len(importers) == 0 {
			continue
		}
		if len(importers) > importChainCollapseThreshold {
			signals = append(signals, types.Signal{
				Probe:   "import-chain",
				File:    file,
				Message: fmt.Sprintf("%s has %d importers outside the plan — verify API compatibility", file, len(importers)),
			})
		} else {
			for _, imp := range importers {
				signals = append(signals, types.Signal{
					Probe:   "import-chain",
					File:    file,
					Message: fmt.Sprintf("%s imports %s but isn't in the plan", imp, file),
				})
			}
		}
	}
	return signals
}

func findImporters(targetFile, cwd string, planFiles map[string]bool) []string {
	basename := strings.TrimSuffix(filepath.Base(targetFile), filepath.Ext(targetFile))
	escaped := regexp.QuoteMeta(basename)

	pyPattern := regexp.MustCompile(`(?im)^(?:from|import)\s+\.{0,3}(?:[\w]+\.)*\b` + escaped + `\b(?:\s+import|\s*$)`)
	jsFromPattern := regexp.MustCompile("(?i)\\bfrom\\s+['\"`][^'\"`]*\\b" + escaped + "\\b[^'\"`]*['\"`]")
	jsCallPattern := regexp.MustCompile("(?i)\\b(?:require|import)\\s*\\(\\s*['\"`][^'\"`]*\\b" + escaped + "\\b[^'\"`]*['\"`]")
	goPattern := regexp.MustCompile(`(?m)^\s*"[^"]*\b` + escaped + `\b[^"]*"`)
	rustPattern := regexp.MustCompile(`(?m)^\s*(?:use|mod)\s+(?:\w+::)*` + escaped + `\b`)
	javaPattern := regexp.MustCompile(`(?m)^\s*import\s+[\w.]*\b` + escaped + `\b`)
	rubyPattern := regexp.MustCompile(`(?m)^\s*require(?:_relative)?\s+['"]` + escaped + `['"]`)

	var importers []string
	walk.WalkSource(cwd, func(filePath string) {
		rel, err := filepath.Rel(cwd, filePath)
		if err != nil {
			return
		}
		rel = filepath.ToSlash(rel)
		if planFiles[rel] || rel == targetFile {
			return
		}
		content, err := os.ReadFile(filePath)
		if err != nil {
			return
		}

		var matched bool
		ext := filepath.Ext(filePath)
		switch ext {
		case ".py":
			matched = pyPattern.Match(content)
		case ".js", ".ts", ".jsx", ".tsx", ".mjs", ".cjs":
			matched = jsFromPattern.Match(content) || jsCallPattern.Match(content)
		case ".go":
			matched = goPattern.Match(content)
		case ".rs":
			matched = rustPattern.Match(content)
		case ".java":
			matched = javaPattern.Match(content)
		case ".rb":
			matched = rubyPattern.Match(content)
		}

		if matched {
			importers = append(importers, rel)
		}
	})
	return importers
}
