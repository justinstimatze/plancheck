// buildcheck.go applies the spike's code to the codebase via go build -overlay
// and collects compilation errors. Files mentioned in errors that AREN'T part of
// the spike are obligations — they MUST change because the spike broke them.
//
// This uses the Go compiler as an oracle for type-system obligations:
// signature changes, missing struct fields, unimplemented interfaces.
// Near-100% precision because the compiler doesn't guess.
package simulate

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

func contextWithTimeout(seconds int) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), time.Duration(seconds)*time.Second)
}

// BuildObligation is a file that the compiler says must change.
type BuildObligation struct {
	File   string // relative file path
	Errors []string // compiler error messages
	Count  int    // number of errors in this file
}

// BuildCheckResult is the output of the build-and-check pass.
type BuildCheckResult struct {
	Obligations []BuildObligation // files that MUST change (not in spike)
	SpikeErrors int               // errors in spike-modified files (expected)
	TotalErrors int               // total compilation errors
}

// RunBuildCheck applies spike file blocks via -overlay and runs go build.
// Returns obligations: files with compile errors that aren't spike-modified.
func RunBuildCheck(fileBlocks map[string]string, cwd string) (*BuildCheckResult, error) {
	if len(fileBlocks) == 0 {
		return nil, nil
	}

	// Create temp dir for overlay files
	tmpDir, err := os.MkdirTemp("", "plancheck-buildcheck-*")
	if err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// Write spike code to temp files and build the overlay map
	overlay := struct {
		Replace map[string]string `json:"Replace"`
	}{
		Replace: make(map[string]string),
	}

	spikeFiles := make(map[string]bool)    // normalized relative paths we're overlaying
	tmpToRelPath := make(map[string]string) // temp file path → original relative path
	for relPath, code := range fileBlocks {
		absPath := filepath.Join(cwd, relPath)

		// Only overlay files that actually exist — don't create new files
		if _, err := os.Stat(absPath); err != nil {
			continue
		}

		// Surgical overlay: patch only type-level changes (struct fields,
		// function signatures) from spike into the original file. This keeps
		// the overlay parseable so the compiler finds real breakage in callers.
		original, err := os.ReadFile(absPath)
		if err != nil {
			continue
		}
		patched := patchTypeChanges(string(original), code)

		tmpFile := filepath.Join(tmpDir, strings.ReplaceAll(relPath, "/", "_"))
		if err := os.WriteFile(tmpFile, []byte(patched), 0o600); err != nil {
			continue
		}

		overlay.Replace[absPath] = tmpFile
		spikeFiles[relPath] = true
		tmpToRelPath[tmpFile] = relPath
	}

	if len(overlay.Replace) == 0 {
		return nil, nil
	}

	// Write overlay JSON
	overlayPath := filepath.Join(tmpDir, "overlay.json")
	overlayJSON, err := json.Marshal(overlay)
	if err != nil {
		return nil, fmt.Errorf("marshal overlay: %w", err)
	}
	if err := os.WriteFile(overlayPath, overlayJSON, 0o600); err != nil {
		return nil, fmt.Errorf("write overlay: %w", err)
	}

	// Check this is a Go project
	if _, err := os.Stat(filepath.Join(cwd, "go.mod")); err != nil {
		return nil, nil // not a Go project
	}

	// Run go build with overlay (30s timeout — we want errors, not a full build)
	ctx, cancel := contextWithTimeout(30)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "build", "-overlay="+overlayPath, "./...")
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0") // avoid CGO issues
	output, _ := cmd.CombinedOutput() // errors expected — that's the signal

	// Parse compilation errors
	// Format: path/to/file.go:line:col: error message
	errorRe := regexp.MustCompile(`(?m)^([^\s:]+\.go):(\d+):(\d+): (.+)$`)
	matches := errorRe.FindAllStringSubmatch(string(output), -1)

	// Group errors by file
	fileErrors := make(map[string][]string)
	for _, m := range matches {
		filePath := m[1]
		errorMsg := m[4]

		// Normalize: map temp overlay paths back to relative paths
		if rel, ok := tmpToRelPath[filePath]; ok {
			filePath = rel
		} else if filepath.IsAbs(filePath) {
			// Also check if this absolute path matches a temp file
			if rel, ok := tmpToRelPath[filePath]; ok {
				filePath = rel
			} else if rel, err := filepath.Rel(cwd, filePath); err == nil {
				filePath = rel
			}
		}

		fileErrors[filePath] = append(fileErrors[filePath], errorMsg)
	}

	// Separate spike errors from obligation errors
	var obligations []BuildObligation
	spikeErrorCount := 0
	totalErrors := 0

	for file, errs := range fileErrors {
		totalErrors += len(errs)
		// Check if this file is in the overlay (spike file) — errors there are expected
		if spikeFiles[file] || spikeFiles[filepath.Clean(file)] {
			spikeErrorCount += len(errs)
			continue
		}
		// Also check basename match when spikeFiles has different prefix
		base := filepath.Base(file)
		isSpike := false
		for sf := range spikeFiles {
			if filepath.Base(sf) == base {
				isSpike = true
				break
			}
		}
		if isSpike {
			spikeErrorCount += len(errs)
			continue
		}
		obligations = append(obligations, BuildObligation{
			File:   file,
			Errors: errs,
			Count:  len(errs),
		})
	}

	return &BuildCheckResult{
		Obligations: obligations,
		SpikeErrors: spikeErrorCount,
		TotalErrors: totalErrors,
	}, nil
}

// patchTypeChanges applies the spike's type-level changes to the original.
// Two strategies:
// 1. For structs: if the spike has different fields, add a _probe field to the
//    original struct. This is more robust than copying the spike's definition
//    because it guarantees the file still parses. The compiler then finds
//    every positional constructor that breaks.
// 2. For functions: replace the original signature with the spike's signature.
func patchTypeChanges(original, spike string) string {
	spikeStructs := extractStructDefs(spike)
	spikeFuncs := extractFuncSigs(spike)

	if len(spikeStructs) == 0 && len(spikeFuncs) == 0 {
		// No type-level changes found — fall back to full overlay
		return spike
	}

	patched := original

	// Proactive struct probe: if the spike modifies a struct, add a dummy field
	// to the ORIGINAL struct. This guarantees the overlay parses correctly and
	// the compiler finds every positional constructor (MyStruct{"x", 42}).
	origStructs := extractStructDefs(original)
	for name, spikeDef := range spikeStructs {
		if origDef, ok := origStructs[name]; ok && origDef != spikeDef {
			// Struct differs — add probe field before closing brace
			probed := strings.TrimSuffix(strings.TrimSpace(origDef), "}")
			probed += "\t_plancheck_probe__ int\n}"
			patched = strings.Replace(patched, origDef, probed, 1)
		}
	}

	// Replace function signatures with spike's versions
	origFuncs := extractFuncSigs(original)
	for name, spikeSig := range spikeFuncs {
		if origSig, ok := origFuncs[name]; ok && origSig != spikeSig {
			patched = strings.Replace(patched, origSig, spikeSig, 1)
		}
	}

	return patched
}

// ProbeExportedSymbols adds dummy fields to exported structs and mutates
// exported function signatures to trigger compiler errors in callers.
// ProbeExportedSymbols adds dummy fields to exported structs and mutates
// exported function signatures to trigger compiler errors in callers.
func ProbeExportedSymbols(code string) string {
	patched := code

	// Add a probe field to every exported struct
	structRe := regexp.MustCompile(`(?ms)^(type\s+([A-Z]\w*)\s+struct\s*\{)`)
	patched = structRe.ReplaceAllStringFunc(patched, func(match string) string {
		return match + "\n\t_plancheck_cascade__ int"
	})

	// Add an extra parameter to every exported function/method
	funcRe := regexp.MustCompile(`(?m)^(func\s+(?:\([^)]*\)\s+)?[A-Z]\w*\s*)\(([^)]*)\)`)
	patched = funcRe.ReplaceAllStringFunc(patched, func(match string) string {
		loc := funcRe.FindStringSubmatchIndex(match)
		if loc == nil {
			return match
		}
		prefix := match[loc[2]:loc[3]]
		params := match[loc[4]:loc[5]]
		if strings.TrimSpace(params) == "" {
			return prefix + "(_plancheck_probe__ int)"
		}
		return prefix + "(_plancheck_probe__ int, " + params + ")"
	})

	return patched
}

// probeSpecificDefinition adds a dummy field/param to ONLY the named definition.
// Unlike ProbeExportedSymbols (which probes ALL exports), this is targeted:
// probing "LookupAccount" only breaks callers of LookupAccount, not everything.
func probeSpecificDefinition(code string, defName string) string {
	// Strip receiver prefix for matching: "(*Server).LookupAccount" → "LookupAccount"
	baseName := defName
	if idx := strings.LastIndex(defName, "."); idx >= 0 {
		baseName = defName[idx+1:]
	}

	patched := code

	// Probe specific struct: add a dummy field
	structRe := regexp.MustCompile(`(?ms)^(type\s+` + regexp.QuoteMeta(baseName) + `\s+struct\s*\{)`)
	patched = structRe.ReplaceAllStringFunc(patched, func(match string) string {
		return match + "\n\t_plancheck_targeted__ int"
	})

	// Probe specific function/method: add a dummy parameter
	funcRe := regexp.MustCompile(`(?m)^(func\s+(?:\([^)]*\)\s+)?` + regexp.QuoteMeta(baseName) + `\s*)\(([^)]*)\)`)
	patched = funcRe.ReplaceAllStringFunc(patched, func(match string) string {
		loc := funcRe.FindStringSubmatchIndex(match)
		if loc == nil {
			return match
		}
		prefix := match[loc[2]:loc[3]]
		params := match[loc[4]:loc[5]]
		if strings.TrimSpace(params) == "" {
			return prefix + "(_plancheck_targeted__ int)"
		}
		return prefix + "(_plancheck_targeted__ int, " + params + ")"
	})

	return patched
}

// BlastRadiusResult is the output of the plan file blast radius probe.
type BlastRadiusResult struct {
	DependentFiles []string // files that break when plan files' APIs change
	ProbedFiles    int      // how many plan files were probed
	TotalErrors    int
}

// RunBlastRadius probes ALL exported symbols in plan files by adding dummy
// struct fields and mutating function signatures, then builds. Returns every
// file in the dependency cone. Spike-independent worst-case blast radius.
func RunBlastRadius(planFiles []string, cwd string) (*BlastRadiusResult, error) {
	if len(planFiles) == 0 {
		return nil, nil
	}
	if _, err := os.Stat(filepath.Join(cwd, "go.mod")); err != nil {
		return nil, nil
	}

	tmpDir, err := os.MkdirTemp("", "plancheck-blastradius-*")
	if err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	overlayMap := make(map[string]string)
	probedSet := make(map[string]bool)
	probedCount := 0

	for _, relPath := range planFiles {
		absPath := filepath.Join(cwd, relPath)
		original, err := os.ReadFile(absPath)
		if err != nil {
			continue
		}
		probed := ProbeExportedSymbols(string(original))
		if probed == string(original) {
			continue
		}
		tmpFile := filepath.Join(tmpDir, "br_"+strings.ReplaceAll(relPath, "/", "_"))
		if err := os.WriteFile(tmpFile, []byte(probed), 0o600); err != nil {
			continue
		}
		overlayMap[absPath] = tmpFile
		probedSet[relPath] = true
		probedCount++
	}

	if probedCount == 0 {
		return nil, nil
	}

	overlay := struct {
		Replace map[string]string `json:"Replace"`
	}{Replace: overlayMap}
	overlayPath := filepath.Join(tmpDir, "overlay.json")
	overlayJSON, err := json.Marshal(overlay)
	if err != nil {
		return nil, fmt.Errorf("marshal overlay: %w", err)
	}
	if err := os.WriteFile(overlayPath, overlayJSON, 0o600); err != nil {
		return nil, fmt.Errorf("write overlay: %w", err)
	}

	ctx, cancel := contextWithTimeout(30)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "build", "-overlay="+overlayPath, "./...")
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	output, _ := cmd.CombinedOutput()

	errorRe := regexp.MustCompile(`(?m)^([^\s:]+\.go):(\d+):(\d+): (.+)$`)
	matches := errorRe.FindAllStringSubmatch(string(output), -1)

	dependentSet := make(map[string]bool)
	totalErrors := 0
	for _, m := range matches {
		filePath := m[1]
		if filepath.IsAbs(filePath) {
			if rel, err := filepath.Rel(cwd, filePath); err == nil {
				filePath = rel
			}
		}
		totalErrors++
		if !probedSet[filePath] {
			dependentSet[filePath] = true
		}
	}

	var dependents []string
	for f := range dependentSet {
		dependents = append(dependents, f)
	}

	return &BlastRadiusResult{
		DependentFiles: dependents,
		ProbedFiles:    probedCount,
		TotalErrors:    totalErrors,
	}, nil
}

// extractStructDefs extracts "type Name struct { ... }" blocks.
// Returns map of struct name → full definition text.
func extractStructDefs(code string) map[string]string {
	result := make(map[string]string)
	re := regexp.MustCompile(`(?ms)^(type\s+(\w+)\s+struct\s*\{[^}]*\})`)
	for _, match := range re.FindAllStringSubmatch(code, -1) {
		result[match[2]] = match[1]
	}
	return result
}

// extractFuncSigs extracts function signature lines (up to the opening brace).
// Returns map of func name → signature text.
func extractFuncSigs(code string) map[string]string {
	result := make(map[string]string)
	re := regexp.MustCompile(`(?m)^(func\s+(?:\([^)]*\)\s*)?(\w+)\s*\([^)]*\)[^{]*)\{`)
	for _, match := range re.FindAllStringSubmatch(code, -1) {
		result[match[2]] = match[1]
	}
	return result
}
