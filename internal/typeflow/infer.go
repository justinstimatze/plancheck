package typeflow

import (
	"path/filepath"
	"strings"
)

// InferredMutation describes which function in a plan file is likely
// being modified, and why. Used to focus the cascade on specific
// functions instead of blasting every exported function.
type InferredMutation struct {
	File     string // plan file path
	FuncName string // the function likely being modified
	Reason   string // why we think this function is the target
	Score    float64 // confidence (0.0-1.0)
}

// InferMutations examines plan files and the task description to determine
// which specific functions are likely being modified.
//
// Strategy (in priority order):
//  1. Function names that appear in the task objective/steps (direct mention)
//  2. The main "Run" function (e.g., createRun, listRun) — most changes touch these
//  3. The "NewCmd" constructor — flag additions, option changes
//  4. Functions whose names share tokens with the task description
//
// Returns at most 3 mutations per file to keep the cascade focused.
func InferMutations(planFiles []string, objective string, steps []string, cwd string) []InferredMutation {
	taskText := strings.ToLower(objective)
	for _, s := range steps {
		taskText += " " + strings.ToLower(s)
	}
	taskTokens := tokenizeTask(taskText)

	var mutations []InferredMutation

	for _, pf := range planFiles {
		absPath := filepath.Join(cwd, pf)
		sigs, err := ParseExportedSigs(absPath)
		if err != nil || len(sigs) == 0 {
			continue
		}

		// Score each function by likelihood of being the mutation target
		type scored struct {
			sig   FuncSig
			score float64
			reason string
		}
		var candidates []scored

		for _, sig := range sigs {
			name := sig.Name
			nameLower := strings.ToLower(name)
			s := 0.0
			reason := ""

			// Direct mention in task text
			if strings.Contains(taskText, nameLower) {
				s = 0.9
				reason = "function name appears in task description"
			}

			// Main "Run" function pattern (createRun, listRun, deleteRun, etc.)
			if strings.HasSuffix(nameLower, "run") || nameLower == "run" {
				if s < 0.7 {
					s = 0.7
					reason = "main Run function — most changes touch this"
				}
			}

			// NewCmd constructor pattern — flag additions, wiring changes
			if strings.HasPrefix(name, "NewCmd") || strings.HasPrefix(name, "New") && strings.HasSuffix(name, "Cmd") {
				taskMentionsFlag := strings.Contains(taskText, "flag") ||
					strings.Contains(taskText, "option") ||
					strings.Contains(taskText, "--")
				if taskMentionsFlag {
					if s < 0.8 {
						s = 0.8
						reason = "NewCmd constructor — task mentions flags/options"
					}
				} else if s < 0.5 {
					s = 0.5
					reason = "NewCmd constructor — may need wiring changes"
				}
			}

			// Token overlap with task description
			nameTokens := splitFuncTokens(name)
			overlap := 0
			for _, tok := range nameTokens {
				if taskTokens[tok] {
					overlap++
				}
			}
			if overlap > 0 {
				tokenScore := 0.3 + float64(overlap)*0.15
				if tokenScore > s {
					s = tokenScore
					reason = "function name shares tokens with task description"
				}
			}

			if s > 0.0 {
				candidates = append(candidates, scored{sig, s, reason})
			}
		}

		// Sort by score, take top 3
		for i := 0; i < len(candidates); i++ {
			for j := i + 1; j < len(candidates); j++ {
				if candidates[j].score > candidates[i].score {
					candidates[i], candidates[j] = candidates[j], candidates[i]
				}
			}
		}
		cap := 3
		if len(candidates) < cap {
			cap = len(candidates)
		}
		for _, c := range candidates[:cap] {
			mutations = append(mutations, InferredMutation{
				File:     pf,
				FuncName: c.sig.Name,
				Reason:   c.reason,
				Score:    c.score,
			})
		}
	}

	return mutations
}

// InferredFuncNames returns just the function names from inferred mutations,
// for use as the cascade starting set.
func InferredFuncNames(mutations []InferredMutation) map[string]bool {
	names := make(map[string]bool)
	for _, m := range mutations {
		names[m.FuncName] = true
	}
	return names
}

func tokenizeTask(text string) map[string]bool {
	tokens := make(map[string]bool)
	for _, w := range strings.FieldsFunc(text, func(r rune) bool {
		return !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'))
	}) {
		if len(w) >= 3 {
			tokens[w] = true
		}
	}
	return tokens
}

// splitFuncTokens splits a CamelCase function name into lowercase tokens.
// "NewCmdCreate" → ["new", "cmd", "create"]
// "createRun" → ["create", "run"]
func splitFuncTokens(name string) []string {
	var tokens []string
	start := 0
	for i := 1; i < len(name); i++ {
		if name[i] >= 'A' && name[i] <= 'Z' {
			tok := strings.ToLower(name[start:i])
			if len(tok) >= 2 {
				tokens = append(tokens, tok)
			}
			start = i
		}
	}
	tok := strings.ToLower(name[start:])
	if len(tok) >= 2 {
		tokens = append(tokens, tok)
	}
	return tokens
}
