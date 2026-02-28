// novelty.go detects how novel a plan is and adjusts the forecast accordingly.
//
// A plan that modifies existing files is low-novelty (the reference graph
// knows what's connected). A plan that creates new packages is high-novelty
// (the graph has nothing to say). The forecast should be honest about this.
//
// The Flyvbjerg insight: nothing is truly novel at the right abstraction
// level. "Add rate limiting middleware" is novel in THIS codebase but
// standard across web frameworks. Cross-project analogies bridge the gap.
package forecast

import (
	"fmt"
	"path/filepath"
	"strings"
)

// NoveltyAssessment describes how much of a plan involves truly new work.
type NoveltyAssessment struct {
	Score       float64 `json:"score"` // 0.0 (pure modification) to 1.0 (pure creation)
	Label       string  `json:"label"` // "routine", "extension", "novel", "exploratory"
	NewFiles    int     `json:"newFiles"`
	ModFiles    int     `json:"modFiles"`
	NewPackages int     `json:"newPackages"` // distinct new directories
	Analogies   int     `json:"analogies"`   // cross-project matches found
	Uncertainty string  `json:"uncertainty"` // "narrow" (low novelty) or "wide" (high novelty)
	Guidance    string  `json:"guidance"`    // what to do about the uncertainty
}

// AssessNovelty evaluates how novel a plan is based on file operations
// and available cross-project analogies.
func AssessNovelty(filesToModify, filesToCreate []string, steps []string) NoveltyAssessment {
	a := NoveltyAssessment{
		ModFiles: len(filesToModify),
		NewFiles: len(filesToCreate),
	}

	// Count distinct new directories (new packages)
	newDirs := make(map[string]bool)
	for _, f := range filesToCreate {
		dir := filepath.Dir(f)
		if dir != "." && dir != "" {
			newDirs[dir] = true
		}
	}
	a.NewPackages = len(newDirs)

	// Novelty score: ratio of creation to total
	total := float64(a.ModFiles + a.NewFiles)
	if total == 0 {
		a.Score = 0.5 // no files = uncertain
	} else {
		a.Score = float64(a.NewFiles) / total
	}

	// Boost novelty for new packages (more uncertain than new files in existing packages)
	if a.NewPackages > 0 {
		a.Score = a.Score*0.7 + 0.3 // floor at 0.3 if creating new packages
	}

	// Label
	switch {
	case a.Score < 0.15:
		a.Label = "routine"
		a.Uncertainty = "narrow"
		a.Guidance = "Structural prediction is reliable. Reference graph covers this well."
	case a.Score < 0.4:
		a.Label = "extension"
		a.Uncertainty = "moderate"
		a.Guidance = "Mix of known and new. Structural prediction covers the modifications; cross-project analogies help with the additions."
	case a.Score < 0.7:
		a.Label = "novel"
		a.Uncertainty = "wide"
		a.Guidance = "Mostly new code. Cross-project analogies and backward scout are more valuable than the reference graph. Consider a spike on the riskiest new component."
	default:
		a.Label = "exploratory"
		a.Uncertainty = "very wide"
		a.Guidance = "Almost entirely new. Structural prediction unavailable. Subdivide into smaller increments and re-forecast after each. The backward scout (what must exist?) is the primary verification tool."
	}

	// Search for analogies based on step descriptions
	keywords := ExtractKeywords(steps)
	for _, kw := range keywords {
		result := FindAnalogies(kw)
		a.Analogies += result.Repos
	}

	// Classify uncertainty types (Alleman framework)
	// Epistemic = reducible by gathering information (spikes, reading code)
	// Aleatory = irreducible variance (task duration, integration surprises)
	if a.Score > 0.5 {
		a.Guidance += " Epistemic uncertainty is HIGH — consider a spike on the riskiest new component before estimating the full task."
	} else if a.Score > 0.2 {
		a.Guidance += " Mix of epistemic (new code) and aleatory (modification variance) uncertainty."
	}

	// Analogies reduce EPISTEMIC uncertainty (you learn from similar projects)
	if a.Analogies > 0 && a.Score > 0.3 {
		if a.Analogies >= 3 {
			a.Guidance += fmt.Sprintf(" Found patterns in %d repos — use these as structural templates.", a.Analogies)
			if a.Uncertainty == "very wide" {
				a.Uncertainty = "wide"
			} else if a.Uncertainty == "wide" {
				a.Uncertainty = "moderate"
			}
		} else {
			a.Guidance += fmt.Sprintf(" Found patterns in %d repo(s) — limited cross-project data.", a.Analogies)
		}
	}

	return a
}

// ExtractKeywords pulls likely searchable terms from plan steps.
func ExtractKeywords(steps []string) []string {
	// Common code concept words to search for
	concepts := []string{
		"middleware", "handler", "router", "server", "client",
		"auth", "cache", "queue", "worker", "scheduler",
		"config", "logger", "validator", "serializ", "parser",
		"metric", "monitor", "rate", "limit", "circuit",
		"migrat", "command", "plugin", "hook", "event",
	}

	found := make(map[string]bool)
	for _, step := range steps {
		lower := strings.ToLower(step)
		for _, concept := range concepts {
			if strings.Contains(lower, concept) {
				found[concept] = true
			}
		}
	}

	var result []string
	for kw := range found {
		result = append(result, kw)
	}
	return result
}
