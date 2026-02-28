// coupling.go classifies HOW callers use a referenced definition.
//
// The reference graph tells us WHO calls a definition. Body-aware
// coupling analysis tells us HOW — which determines whether a change
// actually propagates. A caller that passes through (c.Render(code, r))
// MUST change if Render's signature changes. A caller that only
// type-checks (x.(Bar)) is unaffected.
//
// This addresses the hard-task recall problem: the graph says "23 callers"
// but only 7 do pass-through. Those 7 are the real blast radius.
package refgraph

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// CouplingKind classifies how tightly a caller is coupled to a definition.
type CouplingKind string

const (
	// PassThrough: caller directly invokes the definition with arguments.
	// Breaks on signature changes. e.g., c.Render(code, r)
	PassThrough CouplingKind = "pass-through"

	// FieldAccess: caller reads/writes a field on the definition's type.
	// Breaks on field removal/rename. e.g., x.Field
	FieldAccess CouplingKind = "field-access"

	// TypeRef: caller references the type but doesn't call methods.
	// Breaks on type removal/rename. e.g., var x Bar
	TypeRef CouplingKind = "type-ref"

	// Indirect: caller uses the definition through an interface or wrapper.
	// May not break on changes. e.g., x.(Bar), interface satisfaction
	Indirect CouplingKind = "indirect"

	// Unknown: can't classify from body text.
	Unknown CouplingKind = "unknown"
)

// CouplingWeight returns a 0.0-1.0 weight for how likely this coupling
// kind is to propagate a change.
func CouplingWeight(kind CouplingKind) float64 {
	switch kind {
	case PassThrough:
		return 1.0 // must change if signature changes
	case FieldAccess:
		return 0.8 // likely change if type changes
	case TypeRef:
		return 0.5 // might need updating
	case Indirect:
		return 0.2 // unlikely to break
	case Unknown:
		return 0.5 // default assumption
	default:
		return 0.5
	}
}

// CallerCoupling is a caller with its coupling classification.
type CallerCoupling struct {
	Name     string       `json:"name"`
	Receiver string       `json:"receiver,omitempty"`
	File     string       `json:"file"`
	Kind     CouplingKind `json:"kind"`
	Weight   float64      `json:"weight"`
}

// ClassifyCallerCoupling reads a caller's body and classifies how it
// uses the target definition. Uses the bodies table in defn.
func ClassifyCallerCoupling(cwd string, callerName, callerReceiver, targetName string) CouplingKind {
	if !Available(cwd) {
		return Unknown
	}

	// Query the caller's body
	var where string
	if callerReceiver != "" {
		where = fmt.Sprintf("d.name = '%s' AND d.receiver = '%s'",
			escapeSql(callerName), escapeSql(callerReceiver))
	} else {
		where = fmt.Sprintf("d.name = '%s' AND d.receiver = ''",
			escapeSql(callerName))
	}

	rows := QueryDefn(cwd,
		"SELECT b.body FROM bodies b "+
			"JOIN definitions d ON d.id = b.def_id "+
			"WHERE "+where+" AND d.test = FALSE LIMIT 1")

	if len(rows) == 0 {
		return Unknown
	}

	body, _ := rows[0]["body"].(string)
	if body == "" {
		return Unknown
	}

	return classifyFromBody(body, targetName)
}

// classifyFromBody analyzes a function body to determine coupling kind.
func classifyFromBody(body, targetName string) CouplingKind {
	// Direct method call: .TargetName( — pass-through
	callPattern := regexp.MustCompile(`\.` + regexp.QuoteMeta(targetName) + `\s*\(`)
	if callPattern.MatchString(body) {
		return PassThrough
	}

	// Function call (not method): TargetName( — pass-through
	funcPattern := regexp.MustCompile(`\b` + regexp.QuoteMeta(targetName) + `\s*\(`)
	if funcPattern.MatchString(body) {
		return PassThrough
	}

	// Type assertion: .(TargetName) — indirect
	assertPattern := regexp.MustCompile(`\.\(\s*\*?` + regexp.QuoteMeta(targetName) + `\s*\)`)
	if assertPattern.MatchString(body) {
		return Indirect
	}

	// Field access: .TargetName without ( — field access
	fieldPattern := regexp.MustCompile(`\.` + regexp.QuoteMeta(targetName) + `\b[^(]`)
	if fieldPattern.MatchString(body) {
		return FieldAccess
	}

	// Type reference in declaration: var/: TargetName, or as parameter type
	typePattern := regexp.MustCompile(`\b` + regexp.QuoteMeta(targetName) + `\b`)
	if typePattern.MatchString(body) {
		return TypeRef
	}

	return Unknown
}

// ClassifyBlastRadius takes a definition's callers and classifies each one's
// coupling strength. Returns callers sorted by coupling weight (strongest first).
func ClassifyBlastRadius(cwd string, targetName string, callerNames []struct{ Name, Receiver string }) []CallerCoupling {
	var result []CallerCoupling

	for _, caller := range callerNames {
		kind := ClassifyCallerCoupling(cwd, caller.Name, caller.Receiver, targetName)
		file := ""
		// Get source file
		var where string
		if caller.Receiver != "" {
			where = fmt.Sprintf("name = '%s' AND receiver = '%s'",
				escapeSql(caller.Name), escapeSql(caller.Receiver))
		} else {
			where = fmt.Sprintf("name = '%s' AND receiver = ''",
				escapeSql(caller.Name))
		}
		rows := QueryDefn(cwd,
			"SELECT source_file FROM definitions WHERE "+where+
				" AND test = FALSE AND source_file != '' LIMIT 1")
		if len(rows) > 0 {
			file, _ = rows[0]["source_file"].(string)
		}

		result = append(result, CallerCoupling{
			Name:     caller.Name,
			Receiver: caller.Receiver,
			File:     file,
			Kind:     kind,
			Weight:   CouplingWeight(kind),
		})
	}

	// Sort by weight descending
	sort.Slice(result, func(i, j int) bool {
		return result[i].Weight > result[j].Weight
	})

	return result
}

func escapeSql(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}
