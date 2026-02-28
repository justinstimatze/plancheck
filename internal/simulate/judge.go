// judge.go uses an LLM to synthesize raw check_plan signals into a
// contextual recommendation for improving the plan.
//
// Instead of deterministically assembling "ADD: file1, file2, file3",
// the judge sees ALL raw signals (comod, structural, analogies, blast
// radius, novelty, invariants, semantic validation) and produces a
// coherent, task-specific recommendation.
//
// The judge is optional — requires PLANCHECK_API_KEY. Without it,
// the deterministic summary (Minto pyramid) is used instead.
package simulate

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// JudgeInput is the raw signal bundle sent to the judge LLM.
type JudgeInput struct {
	Objective    string            `json:"objective"`
	PlanFiles    []string          `json:"planFiles"`
	NewFiles     []string          `json:"newFiles"`
	Signals      []JudgeSignal     `json:"signals"`
	Novelty      string            `json:"novelty"`
	Forecast     string            `json:"forecast"`
	DirTree      string            `json:"dirTree,omitempty"` // compact codebase directory structure
}

// JudgeSignal is a single signal for the judge to consider.
type JudgeSignal struct {
	Type    string `json:"type"`    // comod, structural, analogy, semantic, invariant, blast
	File    string `json:"file,omitempty"`
	Message string `json:"message"`
}

// JudgeFileRec is a file recommendation with a task-specific reason.
type JudgeFileRec struct {
	File   string `json:"file"`
	Reason string `json:"reason"`
}

// JudgeRecommendation is the judge's synthesized output.
type JudgeRecommendation struct {
	FilesToAdd     []JudgeFileRec `json:"filesToAdd"`
	FilesToCheck   []string       `json:"filesToCheck"`
	Risks          []string       `json:"risks"`
	Recommendation string         `json:"recommendation"`
}

// Judge processes raw check_plan signals through an LLM to produce
// a contextual recommendation. Returns nil if no API key available.
func Judge(input JudgeInput) (*JudgeRecommendation, error) {
	key := getAPIKey()
	if key == "" {
		return nil, nil // graceful fallback — use deterministic summary
	}

	prompt := buildJudgePrompt(input)
	// Use the best available model for judging — this is the highest-value
	// LLM call in the entire pipeline. Haiku for stubs, Opus for judgment.
	// Haiku outperforms Opus as judge (+10.4pp vs +4.7pp) because it
	// trusts the structural signals at face value instead of second-guessing.
	// Same pattern as naive union > learned weights.
	judgeModel := os.Getenv("PLANCHECK_JUDGE_MODEL")
	if judgeModel == "" {
		judgeModel = "claude-haiku-4-5-20251001"
	}
	response, _, err := callClaudeWithModel(key, prompt, judgeModel)
	if err != nil {
		return nil, err
	}

	return parseJudgeResponse(response), nil
}

func buildJudgePrompt(input JudgeInput) string {
	var b strings.Builder

	b.WriteString("You are a plan verification judge. Analyze these signals about a code change plan and produce a concise recommendation.\n\n")

	b.WriteString(fmt.Sprintf("PLAN OBJECTIVE: %s\n", input.Objective))
	b.WriteString(fmt.Sprintf("FILES TO MODIFY: %s\n", strings.Join(input.PlanFiles, ", ")))
	if len(input.NewFiles) > 0 {
		b.WriteString(fmt.Sprintf("FILES TO CREATE: %s\n", strings.Join(input.NewFiles, ", ")))
	}
	b.WriteString(fmt.Sprintf("NOVELTY: %s\n", input.Novelty))
	if input.Forecast != "" {
		b.WriteString(fmt.Sprintf("FORECAST: %s\n", input.Forecast))
	}

	if input.DirTree != "" {
		b.WriteString(fmt.Sprintf("\nCODEBASE STRUCTURE:\n%s\n", input.DirTree))
	}

	b.WriteString("\nSIGNALS:\n")
	for _, s := range input.Signals {
		b.WriteString(fmt.Sprintf("  [%s]", s.Type))
		if s.File != "" {
			b.WriteString(fmt.Sprintf(" %s:", s.File))
		}
		b.WriteString(fmt.Sprintf(" %s\n", s.Message))
	}

	b.WriteString(`
Identify SPECIFIC FILES the plan is missing, with a task-specific reason for each.

Rules:
- Use the codebase structure to find directories the task mentions but the plan doesn't cover
- If the task mentions multiple commands/domains, ALL domains must be in the plan
- For each file, explain WHY this task requires it (not generic — tied to the task)
- A comod gap of 38% is stronger evidence than a directory sibling
- A structurally-confirmed semantic suggestion is near-certain

Respond with ONLY a JSON object:
{
  "filesToAdd": [{"file": "path/to/file.go", "reason": "task-specific reason"}],
  "filesToCheck": ["files with weak evidence"],
  "risks": ["specific risks"],
  "recommendation": "one paragraph"
}`)


	return b.String()
}

func parseJudgeResponse(response string) *JudgeRecommendation {
	// Extract JSON from response
	jsonStr := response
	if idx := strings.Index(response, "{"); idx >= 0 {
		if end := strings.LastIndex(response, "}"); end > idx {
			jsonStr = response[idx : end+1]
		}
	}

	// Parse into raw map first to handle both old ([]string) and new ([]JudgeFileRec) formats
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(jsonStr), &raw); err == nil {
		rec := &JudgeRecommendation{}

		// Parse filesToAdd — handle both []string and []{file, reason}
		if fta, ok := raw["filesToAdd"]; ok {
			var fileRecs []JudgeFileRec
			if err := json.Unmarshal(fta, &fileRecs); err == nil {
				rec.FilesToAdd = fileRecs
			} else {
				// Fallback: try as []string
				var files []string
				if err := json.Unmarshal(fta, &files); err == nil {
					for _, f := range files {
						rec.FilesToAdd = append(rec.FilesToAdd, JudgeFileRec{File: f})
					}
				}
			}
		}

		if ftc, ok := raw["filesToCheck"]; ok {
			json.Unmarshal(ftc, &rec.FilesToCheck)
		}
		if r, ok := raw["risks"]; ok {
			json.Unmarshal(r, &rec.Risks)
		}
		if r, ok := raw["recommendation"]; ok {
			json.Unmarshal(r, &rec.Recommendation)
		}
		return rec
	}

	// Fallback: use the whole response as the recommendation
	return &JudgeRecommendation{
		Recommendation: response,
	}
}
