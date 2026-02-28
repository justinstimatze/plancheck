package plan

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/justinstimatze/plancheck/internal/types"
)

type dirSibling struct {
	file     string // basename of sibling file
	relPath  string // relative path from cwd (e.g., "pkg/cmd/pr/view/http.go")
	planFile string // the plan file it's a sibling of
}

// findDirectorySiblings scans the filesystem for non-test .go files in the
// same directory as each plan file. This catches http.go, shared.go, etc.
// that live alongside the modified file in CLI-style package structures.
func findDirectorySiblings(p types.ExecutionPlan, cwd string) []dirSibling {
	var result []dirSibling
	seen := make(map[string]bool)

	for _, f := range p.FilesToModify {
		var dir string
		if filepath.IsAbs(f) {
			dir = filepath.Dir(f)
		} else {
			dir = filepath.Join(cwd, filepath.Dir(f))
		}
		base := filepath.Base(f)

		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".go") {
				continue
			}
			if strings.HasSuffix(name, "_test.go") {
				continue
			}
			if name == base || seen[name] {
				continue
			}
			seen[name] = true
			relPath := filepath.ToSlash(filepath.Join(filepath.Dir(f), name))
			result = append(result, dirSibling{file: name, relPath: relPath, planFile: base})
		}
	}
	return result
}

// findParentRegistrations looks one directory up from each plan file for
// .go files that might register or configure the subcommand. In CLI projects,
// pkg/cmd/pr/pr.go registers pkg/cmd/pr/view/view.go as a subcommand.
func findParentRegistrations(p types.ExecutionPlan, cwd string) []dirSibling {
	var result []dirSibling
	seen := make(map[string]bool)

	for _, f := range p.FilesToModify {
		var dir string
		if filepath.IsAbs(f) {
			dir = filepath.Dir(f)
		} else {
			dir = filepath.Join(cwd, filepath.Dir(f))
		}

		parentDir := filepath.Dir(dir)
		if parentDir == dir || parentDir == cwd {
			continue // already at root
		}

		entries, err := os.ReadDir(parentDir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".go") {
				continue
			}
			if strings.HasSuffix(name, "_test.go") {
				continue
			}
			if seen[name] {
				continue
			}
			seen[name] = true
			result = append(result, dirSibling{file: name, planFile: filepath.Base(f)})
		}
	}
	return result
}

type peerFile struct {
	file     string // basename
	fullPath string // full relative path from cwd
	planFile string // the plan file it's a peer of
}

// findAncestorScope scans peer directories under the feature root.
// When a plan file is at create/create.go and the parent has sibling
// directories (list/, shared/), those directories contain related files
// that often need co-modification.
//
// Returns peer files and a scope signal describing the feature structure.
func findAncestorScope(p types.ExecutionPlan, cwd string) ([]peerFile, string) {
	var result []peerFile
	seen := make(map[string]bool)
	var peerDirNames []string

	for _, f := range p.FilesToModify {
		var dir string
		if filepath.IsAbs(f) {
			dir = filepath.Dir(f)
		} else {
			dir = filepath.Join(cwd, filepath.Dir(f))
		}

		featureRoot := filepath.Dir(dir)
		if featureRoot == dir || featureRoot == cwd {
			continue
		}

		planDirName := filepath.Base(dir)

		entries, err := os.ReadDir(featureRoot)
		if err != nil {
			continue
		}

		// Collect peer dirs, prioritizing shared/common dirs first —
		// they contain shared code most likely to need co-modification.
		var sharedDirs, otherDirs []string
		for _, e := range entries {
			if !e.IsDir() || e.Name() == planDirName || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			name := strings.ToLower(e.Name())
			if name == "shared" || name == "common" || name == "utils" || name == "internal" {
				sharedDirs = append(sharedDirs, e.Name())
			} else {
				otherDirs = append(otherDirs, e.Name())
			}
		}
		subDirs := append(sharedDirs, otherDirs...)

		if len(subDirs) == 0 {
			continue
		}

		peerDirNames = subDirs

		for _, subDir := range subDirs {
			peerDir := filepath.Join(featureRoot, subDir)
			peerEntries, err := os.ReadDir(peerDir)
			if err != nil {
				continue
			}
			for _, pe := range peerEntries {
				name := pe.Name()
				if pe.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
					continue
				}
				key := subDir + "/" + name
				if seen[key] {
					continue
				}
				seen[key] = true
				relPath := filepath.Join(filepath.Dir(filepath.Dir(f)), subDir, name)
				result = append(result, peerFile{
					file:     name,
					fullPath: relPath,
					planFile: filepath.Base(f),
				})
			}
		}
	}

	scopeSignal := ""
	if len(peerDirNames) > 0 && len(peerDirNames) <= 8 {
		scopeSignal = fmt.Sprintf("Peer packages: %s. Check if this change affects other subcommands.",
			strings.Join(peerDirNames, ", "))
	}

	if len(result) > 15 {
		result = result[:15]
	}

	return result, scopeSignal
}

func isGreenfield(cwd string, filesToCreate []string) bool {
	creatingAbsolute := make(map[string]bool)
	for _, f := range filesToCreate {
		abs, _ := filepath.Abs(filepath.Join(cwd, f))
		creatingAbsolute[abs] = true
	}

	entries, err := os.ReadDir(cwd)
	if err != nil {
		return false
	}
	goFileCount := 0
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".go") {
			abs, _ := filepath.Abs(filepath.Join(cwd, e.Name()))
			if !creatingAbsolute[abs] {
				goFileCount++
			}
		}
	}
	return goFileCount == 0
}
