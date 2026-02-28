// Package walk provides git-aware file traversal and source file utilities.
package walk

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// GitRoot returns the git repository root for cwd, or an error if not in a repo.
func GitRoot(cwd string) (string, error) {
	cmd := exec.Command("git", "-C", cwd, "rev-parse", "--show-toplevel")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

var sourceExts = regexp.MustCompile(`\.(ts|tsx|js|jsx|mjs|cjs|py|go|rb|rs|java)$`)

func shouldSkip(name string) bool {
	switch name {
	case "node_modules", ".git", "vendor":
		return true
	}
	return len(name) > 0 && name[0] == '.'
}

// Walk walks dir recursively, calling cb on every file whose name matches the
// filter. If filter is nil, all files match.
func Walk(dir string, filter func(name string) bool, cb func(filePath string)) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if shouldSkip(entry.Name()) {
			continue
		}
		full := filepath.Join(dir, entry.Name())
		if entry.IsDir() {
			Walk(full, filter, cb)
		} else if filter == nil || filter(entry.Name()) {
			cb(full)
		}
	}
}

// WalkSource walks dir recursively, calling cb on every source file.
func WalkSource(dir string, cb func(filePath string)) {
	Walk(dir, sourceExts.MatchString, cb)
}

// WalkAll walks dir recursively, calling cb on every file.
func WalkAll(dir string, cb func(filePath string)) {
	Walk(dir, nil, cb)
}

var testPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?:^|/)__tests?__/`),
	regexp.MustCompile(`(?:^|/)tests?/`),
	regexp.MustCompile(`\.(?:test|spec)\.[jt]sx?$`),
	regexp.MustCompile(`(?:^|/)test_\w+\.py$`),
	regexp.MustCompile(`\w+_test\.py$`),
}

// IsTestFile returns true if the file path looks like a test file.
func IsTestFile(file string) bool {
	for _, re := range testPatterns {
		if re.MatchString(file) {
			return true
		}
	}
	return false
}
