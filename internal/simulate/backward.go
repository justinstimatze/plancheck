package simulate

import (
	"fmt"
)

// BackwardResult is the output of a backward scout analysis.
type BackwardResult struct {
	Goal           string          `json:"goal"`
	PatternSource  string          `json:"patternSource"`  // existing definition used as template
	Prerequisites  []Prerequisite  `json:"prerequisites"`
	ExistingRefs   []string        `json:"existingRefs"`   // production defs the pattern references
	TestPattern    []string        `json:"testPattern"`    // test defs following the pattern
}

// Prerequisite is something that must exist for the goal state.
type Prerequisite struct {
	Name        string `json:"name"`
	Kind        string `json:"kind"`        // "new" or "modify"
	Description string `json:"description"`
	InferredFrom string `json:"inferredFrom"` // which existing def suggested this
}

// BackwardScout analyzes what must exist for a goal definition to work,
// by finding existing definitions with similar patterns and extracting
// their prerequisites.
func BackwardScout(cwd string, goalName string, goalReceiver string) (BackwardResult, error) {
	defnDir := cwd + "/.defn"
	result := BackwardResult{
		Goal: displayName(Mutation{Name: goalName, Receiver: goalReceiver}),
	}

	// Find sibling definitions (same receiver) as pattern sources
	var siblings []map[string]interface{}
	if goalReceiver != "" {
		siblings = doltQuery(defnDir, fmt.Sprintf(
			"SELECT name, receiver, "+
				"(SELECT COUNT(*) FROM `references` r WHERE r.to_def = d.id) as callers "+
				"FROM definitions d WHERE receiver = '%s' AND test = FALSE AND exported = TRUE "+
				"ORDER BY callers DESC LIMIT 5", goalReceiver))
	}

	if len(siblings) == 0 {
		result.Prerequisites = []Prerequisite{{
			Name: goalName, Kind: "new",
			Description: fmt.Sprintf("Create %s (no existing pattern found)", result.Goal),
		}}
		return result, nil
	}

	// Use the highest-caller sibling as the pattern source
	patternName := strVal(siblings[0], "name")
	result.PatternSource = patternName

	// What does the pattern source reference? These are prerequisites.
	refs := doltQuery(defnDir, fmt.Sprintf(
		"SELECT DISTINCT d2.name, d2.receiver, d2.kind, d2.source_file "+
			"FROM definitions d "+
			"JOIN `references` r ON r.from_def = d.id "+
			"JOIN definitions d2 ON r.to_def = d2.id "+
			"WHERE d.name = '%s' AND d.receiver = '%s' AND d2.test = FALSE "+
			"ORDER BY d2.name", patternName, goalReceiver))

	for _, ref := range refs {
		name := strVal(ref, "name")
		recv := strVal(ref, "receiver")
		display := name
		if recv != "" {
			display = fmt.Sprintf("(%s).%s", recv, name)
		}
		result.ExistingRefs = append(result.ExistingRefs, display)
	}

	// Find tests that test the pattern source — these define the test pattern
	tests := doltQuery(defnDir, fmt.Sprintf(
		"SELECT DISTINCT d.name FROM definitions d "+
			"JOIN `references` r ON r.from_def = d.id "+
			"JOIN definitions target ON r.to_def = target.id "+
			"WHERE target.name = '%s' AND target.receiver = '%s' AND d.test = TRUE "+
			"ORDER BY d.name LIMIT 10", patternName, goalReceiver))

	for _, t := range tests {
		result.TestPattern = append(result.TestPattern, strVal(t, "name"))
	}

	// Build prerequisites list
	result.Prerequisites = []Prerequisite{
		{
			Name:         result.Goal,
			Kind:         "new",
			Description:  fmt.Sprintf("Create %s following pattern of %s", result.Goal, patternName),
			InferredFrom: patternName,
		},
	}

	// The new definition should reference the same things as the pattern
	for _, ref := range refs {
		name := strVal(ref, "name")
		recv := strVal(ref, "receiver")
		display := name
		if recv != "" {
			display = fmt.Sprintf("(%s).%s", recv, name)
		}
		result.Prerequisites = append(result.Prerequisites, Prerequisite{
			Name:         display,
			Kind:         "modify",
			Description:  fmt.Sprintf("Verify %s handles the new %s (referenced by pattern %s)", display, goalName, patternName),
			InferredFrom: patternName,
		})
	}

	// Tests following the pattern
	if len(result.TestPattern) > 0 {
		testName := fmt.Sprintf("Test%s%s", goalReceiver, goalName)
		if goalReceiver != "" {
			// Clean receiver for test name: *Context → Context
			cleanRecv := goalReceiver
			if len(cleanRecv) > 0 && cleanRecv[0] == '*' {
				cleanRecv = cleanRecv[1:]
			}
			testName = fmt.Sprintf("Test%s%s", cleanRecv, goalName)
		}
		result.Prerequisites = append(result.Prerequisites, Prerequisite{
			Name:         testName,
			Kind:         "new",
			Description:  fmt.Sprintf("Create %s following pattern of %s", testName, result.TestPattern[0]),
			InferredFrom: result.TestPattern[0],
		})
	}

	return result, nil
}
