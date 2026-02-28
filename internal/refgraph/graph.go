// Bridge between plancheck and defn/graph library.
// Re-exports types and wraps Load with plancheck-specific conventions
// (cwd-based paths, go.mod module resolution).
package refgraph

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/justinstimatze/defn/graph"
)

// Graph is defn's in-memory reference graph. Re-exported so callers
// don't need to import defn/graph directly.
type Graph = graph.Graph

// Def is a single definition. Re-exported.
type Def = graph.Def

// LoadGraph loads the reference graph for a project.
// Wraps defn/graph.Load with plancheck's cwd convention.
// Cached per path — second call returns instantly.
func LoadGraph(cwd string) *Graph {
	defnPath := filepath.Join(cwd, ".defn")
	g, err := graph.Load(defnPath)
	if err != nil {
		return nil
	}
	return g
}

// ClearCache clears all cached graphs. For testing.
func ClearCache() {
	graph.ClearCache()
}

// ResolveModuleIDFromGoMod maps a relative file path to a defn module ID
// by reading go.mod to construct the full module path, then looking it up
// in the graph. Returns 0 if not found.
func ResolveModuleIDFromGoMod(g *Graph, cwd, relPath string) int64 {
	if g == nil {
		return 0
	}

	// Try graph's built-in resolver first (suffix match on module paths)
	if id, ok := g.ResolveModuleID(cwd, relPath); ok {
		return id
	}

	// Fall back to go.mod parsing for exact match
	gomodPath := filepath.Join(cwd, "go.mod")
	data, err := os.ReadFile(gomodPath)
	if err != nil {
		return 0
	}
	var modRoot string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module ") {
			modRoot = strings.TrimSpace(strings.TrimPrefix(line, "module"))
			break
		}
	}
	if modRoot == "" {
		return 0
	}

	dir := filepath.ToSlash(filepath.Dir(relPath))
	modulePath := modRoot
	if dir != "." && dir != "" {
		modulePath = modRoot + "/" + dir
	}

	return g.ModuleID(modulePath)
}
