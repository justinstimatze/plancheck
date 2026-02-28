package plan

import (
	"path/filepath"
	"regexp"
	"strings"
)

type fileLayer int

const (
	layerUnknown fileLayer = iota
	layerFrontend
	layerBackend
)

var backendExts = map[string]bool{
	".py": true, ".rb": true, ".go": true, ".java": true, ".rs": true,
}

var backendDirPattern = regexp.MustCompile(`(?i)/(server|api|backend|routes|models|db|migrations?|controllers?|handlers?)(/|$)`)
var backendNamePattern = regexp.MustCompile(`(?i)^(server|api)\.(js|ts|mjs)$`)

var frontendExts = map[string]bool{
	".css": true, ".scss": true, ".sass": true, ".less": true,
	".html": true, ".vue": true, ".svelte": true,
}
var frontendDirPattern = regexp.MustCompile(`(?i)/(client|frontend|public|static|ui|components?|views?|pages?)(/|$)`)

var jsExts = map[string]bool{
	".js": true, ".jsx": true, ".ts": true, ".tsx": true, ".mjs": true,
}
var frontendJsDirPattern = regexp.MustCompile(`(?i)/(client|frontend|public|static|ui)(/|$)`)
var frontendJsNamePattern = regexp.MustCompile(`(?i)^(app|main|index|router|store)\.(js|ts|jsx|tsx)$`)

func getFileLayer(file string) fileLayer {
	name := strings.ToLower(filepath.Base(file))
	dir := strings.ToLower(filepath.ToSlash(file))
	ext := strings.ToLower(filepath.Ext(file))

	if backendExts[ext] {
		return layerBackend
	}
	if backendDirPattern.MatchString("/" + dir) {
		return layerBackend
	}
	if backendNamePattern.MatchString(name) {
		return layerBackend
	}

	if frontendExts[ext] {
		return layerFrontend
	}
	if frontendDirPattern.MatchString("/" + dir) {
		return layerFrontend
	}
	if jsExts[ext] {
		if frontendJsDirPattern.MatchString("/" + dir) {
			return layerFrontend
		}
		if frontendJsNamePattern.MatchString(name) {
			return layerFrontend
		}
	}

	return layerUnknown
}

func isCrossStack(fileA, fileB string) bool {
	a := getFileLayer(fileA)
	b := getFileLayer(fileB)
	return a != layerUnknown && b != layerUnknown && a != b
}
