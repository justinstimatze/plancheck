// genres.go maps Go projects to genres based on go.mod imports.
//
// When a project imports "net/http" + a router, it's a web server.
// When it imports "github.com/spf13/cobra", it's a CLI tool.
// The genre determines which reference repos are most relevant for
// cross-project analogies.
//
// For v1: hardcoded list of well-known Go repos per genre, all
// MIT/Apache-2.0/BSD licensed. Auto-selected by import analysis.
package forecast

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// Genre represents a category of Go project.
type Genre struct {
	Name  string   `json:"name"`
	Repos []string `json:"repos"` // repo names in ~/.plancheck/datasets/repos/
}

// Known genres and their representative repos.
// All repos are MIT, Apache-2.0, or BSD licensed.
var genres = []struct {
	name    string
	signals []string // import paths that indicate this genre
	repos   []string // representative repos to search
}{
	{
		name:    "web-framework",
		signals: []string{"net/http", "gorilla/mux", "gin-gonic", "labstack/echo", "go-chi"},
		repos:   []string{"gin", "echo", "chi"},
	},
	{
		name:    "cli-tool",
		signals: []string{"spf13/cobra", "urfave/cli", "alecthomas/kong"},
		repos:   []string{"cobra", "cli", "fzf"},
	},
	{
		name:    "database-orm",
		signals: []string{"go-gorm/gorm", "jmoiron/sqlx", "database/sql"},
		repos:   []string{"gorm", "mysql"},
	},
	{
		name:    "data-structure",
		signals: []string{"RoaringBitmap", "emirpasic/gods", "Workiva/go-datastructures"},
		repos:   []string{"roaring", "go-datastructures", "bigcache"},
	},
	{
		name:    "search-engine",
		signals: []string{"blevesearch/bleve", "meilisearch"},
		repos:   []string{"bleve"},
	},
	{
		name:    "grpc-service",
		signals: []string{"google.golang.org/grpc", "google.golang.org/protobuf"},
		repos:   []string{"grpc-go"},
	},
	{
		name:    "tui-app",
		signals: []string{"charmbracelet/bubbletea", "charmbracelet/lipgloss"},
		repos:   []string{"bubbletea"},
	},
}

// UniversalRepos are always included in analogy searches regardless of genre.
var UniversalRepos = []string{}

// userGenre matches the JSON structure for user-configured genres.
type userGenre struct {
	Name    string   `json:"name"`
	Signals []string `json:"signals"`
	Repos   []string `json:"repos"`
}

// loadUserGenres loads ~/.plancheck/genres.json if it exists.
// Format:
//
//	[
//	  {"name": "web-framework", "signals": ["net/http", "gin-gonic"], "repos": ["gin", "echo"]},
//	  {"name": "my-custom-genre", "signals": ["my/lib"], "repos": ["my-reference-repo"]}
//	]
func loadUserGenres() []userGenre {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(home, ".plancheck", "genres.json"))
	if err != nil {
		return nil
	}
	var result []userGenre
	json.Unmarshal(data, &result)
	return result
}

// DetectGenre reads a project's go.mod and returns matching genres.
// Checks user-configured genres first (~/.plancheck/genres.json),
// then falls back to built-in genre list.
func DetectGenre(cwd string) []Genre {
	gomod := filepath.Join(cwd, "go.mod")
	data, err := os.ReadFile(gomod)
	if err != nil {
		return nil
	}
	content := string(data)

	// Check for user-configured genres
	userGenres := loadUserGenres()
	if len(userGenres) > 0 {
		var matched []Genre
		for _, g := range userGenres {
			for _, signal := range g.Signals {
				if strings.Contains(content, signal) {
					matched = append(matched, Genre{
						Name:  g.Name,
						Repos: filterAvailable(g.Repos),
					})
					break
				}
			}
		}
		if len(matched) > 0 {
			return matched
		}
	}

	var matched []Genre
	for _, g := range genres {
		for _, signal := range g.signals {
			if strings.Contains(content, signal) {
				matched = append(matched, Genre{
					Name:  g.name,
					Repos: filterAvailable(g.repos),
				})
				break
			}
		}
	}

	return matched
}

// filterAvailable returns only repos that have been defn-indexed locally.
func filterAvailable(repos []string) []string {
	dir := reposDir()
	if dir == "" {
		return repos // return all, let caller handle missing
	}
	var available []string
	for _, repo := range repos {
		defnDir := filepath.Join(dir, repo, ".defn")
		if _, err := os.Stat(defnDir); err == nil {
			available = append(available, repo)
		}
	}
	return available
}
