package plan

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

var dirTreeCache sync.Map // cwd → cached tree string

// CompactDirTree builds a condensed directory tree of the codebase.
// Shows directories that contain .go files, grouped by top-level package.
// Output is ~20-40 lines, designed to fit in an LLM judge prompt.
//
// Example output:
//
//	api/
//	cmd/gen-docs/
//	internal/ ghcmd/ prompter/ safepaths/ update/
//	pkg/cmd/ auth/login/ codespace/ extension/ gist/delete,edit,shared,view/
//	pkg/cmd/ issue/create,list/ pr/checkout,create,list,shared,view/
//	pkg/cmd/ repo/autolink/create,delete,list,shared,view/  run/download,shared/
//	pkg/extensions/
func CompactDirTree(cwd string, maxLines int) string {
	if maxLines <= 0 {
		maxLines = 30
	}

	// Cache by cwd — the directory tree doesn't change during a session
	cacheKey := cwd + fmt.Sprintf(":%d", maxLines)
	if cached, ok := dirTreeCache.Load(cacheKey); ok {
		return cached.(string)
	}

	// Walk directory tree, collect dirs that contain .go files
	goDirs := make(map[string]bool)
	filepath.Walk(cwd, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		// Skip hidden dirs and vendor
		name := info.Name()
		if info.IsDir() && (strings.HasPrefix(name, ".") || name == "vendor" || name == "testdata" || name == "node_modules") {
			return filepath.SkipDir
		}
		if !info.IsDir() && strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go") {
			rel, err := filepath.Rel(cwd, filepath.Dir(path))
			if err == nil && rel != "." {
				goDirs[filepath.ToSlash(rel)] = true
			}
		}
		return nil
	})

	if len(goDirs) == 0 {
		return ""
	}

	// Group by top-level directory
	groups := make(map[string][]string) // top-level → subdirs
	for dir := range goDirs {
		parts := strings.SplitN(dir, "/", 2)
		top := parts[0]
		if len(parts) > 1 {
			groups[top] = append(groups[top], parts[1])
		} else {
			groups[top] = append(groups[top], "")
		}
	}

	// Build compact output
	var lines []string
	tops := make([]string, 0, len(groups))
	for t := range groups {
		tops = append(tops, t)
	}
	sort.Strings(tops)

	for _, top := range tops {
		subs := groups[top]
		sort.Strings(subs)

		if len(subs) == 1 && subs[0] == "" {
			lines = append(lines, top+"/")
			continue
		}

		// Compress: group subdirs by their parent
		compressed := compressSubdirs(subs)
		// Split into lines of ~80 chars
		current := top + "/"
		for _, chunk := range compressed {
			if len(current)+len(chunk)+2 > 80 && current != top+"/" {
				lines = append(lines, current)
				current = top + "/"
			}
			if current == top+"/" {
				current += " " + chunk
			} else {
				current += "  " + chunk
			}
		}
		if current != top+"/" {
			lines = append(lines, current)
		}
	}

	if len(lines) > maxLines {
		lines = lines[:maxLines]
	}

	result := strings.Join(lines, "\n")
	dirTreeCache.Store(cacheKey, result)
	return result
}

// compressSubdirs groups subdirectories by parent.
// Input: ["cmd/pr/create", "cmd/pr/list", "cmd/pr/shared", "cmd/gist/delete"]
// Output: ["cmd/pr/create,list,shared", "cmd/gist/delete"]
func compressSubdirs(subs []string) []string {
	// Group by parent dir
	parents := make(map[string][]string)
	var parentOrder []string
	for _, s := range subs {
		if s == "" {
			continue
		}
		parts := strings.Split(s, "/")
		if len(parts) == 1 {
			// Direct child
			parent := ""
			if _, seen := parents[parent]; !seen {
				parentOrder = append(parentOrder, parent)
			}
			parents[parent] = append(parents[parent], parts[0])
		} else {
			// Has parent(s) — use all but last as parent key
			parent := strings.Join(parts[:len(parts)-1], "/")
			leaf := parts[len(parts)-1]
			if _, seen := parents[parent]; !seen {
				parentOrder = append(parentOrder, parent)
			}
			parents[parent] = append(parents[parent], leaf)
		}
	}

	var result []string
	for _, parent := range parentOrder {
		leaves := parents[parent]
		sort.Strings(leaves)
		if parent == "" {
			result = append(result, strings.Join(leaves, ", "))
		} else {
			result = append(result, parent+"/"+strings.Join(leaves, ","))
		}
	}
	return result
}
