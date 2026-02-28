package plan

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/justinstimatze/plancheck/internal/refgraph"
	"github.com/justinstimatze/plancheck/internal/simulate"
	"github.com/justinstimatze/plancheck/internal/typeflow"
	"github.com/justinstimatze/plancheck/internal/types"
)

// buildImplementationPreview creates a pre-implementation code review from the spike.
// It summarizes what the spike changed, what the compiler says must change, and risks.
func buildImplementationPreview(spike *simulate.SpikeResult, graph *refgraph.Graph, cwd string) *types.ImplementationPreview {
	preview := &types.ImplementationPreview{}

	// File changes: summarize what the spike did in each file, enriched with graph data
	for file, code := range spike.FileBlocks {
		if code == "" {
			continue
		}
		fullPath := filepath.Join(cwd, file)
		original, err := os.ReadFile(fullPath)
		if err != nil {
			continue
		}
		origStr := string(original)
		summary := summarizeFileChange(origStr, code, file)
		if summary.Summary != "" {
			if graph != nil {
				summary.Summary = enrichWithDependencies(summary, graph)
			}
			preview.FileChanges = append(preview.FileChanges, summary)
		}
	}

	// Obligations from build-check and type analysis
	for _, ob := range spike.Obligations {
		preview.Obligations = append(preview.Obligations, types.Obligation{
			File:   ob.File,
			Reason: ob.Reason,
		})
	}

	// Risks: derived from the spike's changes + graph
	for _, pred := range spike.Predictions {
		if strings.Contains(pred.Reason, "broken caller") {
			preview.Risks = append(preview.Risks,
				fmt.Sprintf("%s has callers that may break: %s", pred.File, pred.Reason))
		}
	}

	// Graph-based risks: functions with many callers
	if graph != nil {
		for file := range spike.FileBlocks {
			defs := graph.DefsInFile(filepath.Base(file), 0)
			for _, def := range defs {
				callers := graph.CallerDefs(def.ID)
				nonTestCallers := 0
				for _, c := range callers {
					if !c.Test {
						nonTestCallers++
					}
				}
				if nonTestCallers >= 5 {
					preview.Risks = append(preview.Risks,
						fmt.Sprintf("%s() in %s has %d callers — signature changes will cascade widely",
							def.Name, file, nonTestCallers))
				}
			}
		}
	}

	return preview
}

// enrichWithDependencies adds caller/constructor counts from the graph to a file change summary.
func enrichWithDependencies(fc types.FileChange, graph *refgraph.Graph) string {
	if fc.Kind == "struct-field" {
		// Find the struct name from the summary
		// "adds 1 field(s) to CreateOptions struct" → look up CreateOptions
		parts := strings.Split(fc.Summary, " to ")
		if len(parts) == 2 {
			structName := strings.TrimSuffix(parts[1], " struct")
			def := graph.GetDef(structName, "")
			if def != nil {
				callers := graph.CallerDefs(def.ID)
				if len(callers) > 0 {
					files := make(map[string]bool)
					for _, c := range callers {
						if !c.Test {
							files[c.SourceFile] = true
						}
					}
					return fmt.Sprintf("%s (%d constructor/usage sites across %d files)",
						fc.Summary, len(callers), len(files))
				}
			}
		}
	} else if fc.Kind == "signature-change" {
		// "changes signature of CreateRun" → look up callers
		parts := strings.Split(fc.Summary, " of ")
		if len(parts) == 2 {
			funcName := parts[1]
			def := graph.GetDef(funcName, "")
			if def != nil {
				callers := graph.CallerDefs(def.ID)
				nonTest := 0
				for _, c := range callers {
					if !c.Test {
						nonTest++
					}
				}
				if nonTest > 0 {
					return fmt.Sprintf("%s (%d callers must update)", fc.Summary, nonTest)
				}
			}
		}
	}
	return fc.Summary
}

// summarizeFileChange compares spike code vs original and produces a human-readable summary.
func summarizeFileChange(original, spike, file string) types.FileChange {
	// Detect struct field additions
	origStructs := extractStructFieldCounts(original)
	spikeStructs := extractStructFieldCounts(spike)
	for name, spikeCount := range spikeStructs {
		if origCount, ok := origStructs[name]; ok && spikeCount > origCount {
			return types.FileChange{
				File:    file,
				Summary: fmt.Sprintf("adds %d field(s) to %s struct", spikeCount-origCount, name),
				Kind:    "struct-field",
			}
		}
	}

	// Detect new function definitions
	origFuncs := extractFuncNames(original)
	spikeFuncs := extractFuncNames(spike)
	for fn := range spikeFuncs {
		if !origFuncs[fn] {
			return types.FileChange{
				File:    file,
				Summary: fmt.Sprintf("adds new function %s", fn),
				Kind:    "new-function",
			}
		}
	}

	// Detect function signature changes
	origSigs := extractFuncSignatures(original)
	spikeSigs := extractFuncSignatures(spike)
	for name, spikeSig := range spikeSigs {
		if origSig, ok := origSigs[name]; ok && origSig != spikeSig {
			return types.FileChange{
				File:    file,
				Summary: fmt.Sprintf("changes signature of %s", name),
				Kind:    "signature-change",
			}
		}
	}

	// Generic body change
	return types.FileChange{
		File:    file,
		Summary: "modifies implementation",
		Kind:    "body-change",
	}
}

var structFieldRe = regexp.MustCompile(`(?ms)type\s+(\w+)\s+struct\s*\{([^}]*)\}`)

func extractStructFieldCounts(code string) map[string]int {
	result := make(map[string]int)
	for _, match := range structFieldRe.FindAllStringSubmatch(code, -1) {
		count := 0
		for _, line := range strings.Split(match[2], "\n") {
			line = strings.TrimSpace(line)
			if line != "" && !strings.HasPrefix(line, "//") {
				count++
			}
		}
		result[match[1]] = count
	}
	return result
}

var funcDefRe = regexp.MustCompile(`(?m)^func\s+(?:\([^)]*\)\s*)?(\w+)\s*\(`)

func extractFuncNames(code string) map[string]bool {
	result := make(map[string]bool)
	for _, match := range funcDefRe.FindAllStringSubmatch(code, -1) {
		result[match[1]] = true
	}
	return result
}

var funcSigRe = regexp.MustCompile(`(?m)^(func\s+(?:\([^)]*\)\s*)?(\w+)\s*\([^)]*\)[^{]*)`)

func extractFuncSignatures(code string) map[string]string {
	result := make(map[string]string)
	for _, match := range funcSigRe.FindAllStringSubmatch(code, -1) {
		result[match[2]] = match[1]
	}
	return result
}

func buildSpikeFileSet(verifiedFiles []typeflow.DiscoveredFile) map[string]bool {
	result := make(map[string]bool)
	for _, vf := range verifiedFiles {
		if vf.Direction == "spike" {
			result[vf.Path] = true
			result[vf.File] = true
		}
	}
	return result
}
