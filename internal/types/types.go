// Package types defines shared data structures for plancheck.
// All cross-package types live here to avoid import cycles.
package types

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ExecutionPlan is the input plan to check.
type ExecutionPlan struct {
	Objective         string            `json:"objective"`
	FilesToRead       []string          `json:"filesToRead"`
	FilesToModify     []string          `json:"filesToModify"`
	FilesToCreate     []string          `json:"filesToCreate"`
	Steps             []string          `json:"steps"`
	AcknowledgedComod      map[string]string `json:"acknowledgedComod,omitempty"`
	AcknowledgedDomainGaps []string          `json:"acknowledgedDomainGaps,omitempty"`
	// SemanticSuggestions are files the model thinks might need changing
	// but hasn't committed to. plancheck validates these against the
	// reference graph and comod — the third signal in the prediction market.
	SemanticSuggestions []SemanticSuggestion `json:"semanticSuggestions,omitempty"`

	// Invariants are end-state requirements the plan must satisfy.
	// These anchor the backward scout — concrete, verifiable claims
	// about what must be true after execution. Like TDD for plans.
	Invariants []Invariant `json:"invariants,omitempty"`

	// TestPatch is the unified diff of test changes (from gold patch).
	// Used for backward planning: test expectations → production file predictions.
	TestPatch string `json:"testPatch,omitempty"`
}

// Invariant is a verifiable end-state requirement for the plan.
type Invariant struct {
	Claim    string `json:"claim"`              // "all tests pass", "API backward compatible"
	Kind     string `json:"kind"`               // "tests", "api-compat", "no-new-deps", "custom"
	Verified bool   `json:"verified,omitempty"` // set by plancheck after checking
	Evidence string `json:"evidence,omitempty"` // how it was verified
}

// SemanticSuggestion is a file the model thinks might need changing.
type SemanticSuggestion struct {
	File       string  `json:"file"`
	Confidence float64 `json:"confidence"` // 0.0 to 1.0
	Reason     string  `json:"reason"`
}

// ParsePlan parses JSON bytes into an ExecutionPlan, applying defaults.
func ParsePlan(data []byte) (ExecutionPlan, error) {
	// First try normal parse
	var p ExecutionPlan
	if err := json.Unmarshal(data, &p); err != nil {
		// Steps might be [{id, description}] objects instead of strings.
		// Try parsing with flexible steps.
		var raw map[string]json.RawMessage
		if err2 := json.Unmarshal(data, &raw); err2 != nil {
			return p, fmt.Errorf("invalid JSON: %w", err)
		}
		// Parse steps as objects
		if stepsRaw, ok := raw["steps"]; ok {
			var stepObjs []struct {
				ID          string `json:"id"`
				Description string `json:"description"`
			}
			if err2 := json.Unmarshal(stepsRaw, &stepObjs); err2 == nil {
				var steps []string
				for _, s := range stepObjs {
					steps = append(steps, s.Description)
				}
				// Replace steps in raw and re-parse
				stepsJSON, _ := json.Marshal(steps)
				raw["steps"] = stepsJSON
				fixedData, _ := json.Marshal(raw)
				if err2 := json.Unmarshal(fixedData, &p); err2 != nil {
					return p, fmt.Errorf("invalid JSON: %w", err)
				}
			} else {
				return p, fmt.Errorf("invalid JSON: %w", err)
			}
		} else {
			return p, fmt.Errorf("invalid JSON: %w", err)
		}
	}
	if p.FilesToRead == nil {
		p.FilesToRead = []string{}
	}
	if p.FilesToModify == nil {
		p.FilesToModify = []string{}
	}
	if p.FilesToCreate == nil {
		p.FilesToCreate = []string{}
	}
	if p.Steps == nil {
		p.Steps = []string{}
	}
	if p.Objective == "" {
		return p, errors.New("objective is required")
	}
	if len(p.Steps) == 0 {
		return p, errors.New("steps is required")
	}
	const maxFiles = 10000
	if len(p.FilesToRead) > maxFiles || len(p.FilesToModify) > maxFiles ||
		len(p.FilesToCreate) > maxFiles || len(p.Steps) > maxFiles {
		return p, fmt.Errorf("plan exceeds size limit (%d entries)", maxFiles)
	}
	return p, nil
}

// ComodGap is a file historically co-modified with a plan file but missing from the plan.
type ComodGap struct {
	PlanFile           string  `json:"planFile"`
	ComodFile          string  `json:"comodFile"`
	Frequency          float64 `json:"frequency"`
	Confidence         string  `json:"confidence"` // "high" (>75%) or "moderate" (40-75%)
	CrossStack         bool    `json:"crossStack"`
	Acknowledged       bool    `json:"acknowledged"`
	AcknowledgedReason string  `json:"acknowledgedReason,omitempty"`
	Hub                bool    `json:"hub"`
	Suggestion         string  `json:"suggestion"`
}

// ConfidenceLevel returns the ordinal confidence label for a frequency value.
func ConfidenceLevel(frequency float64) string {
	if frequency > 0.75 {
		return "high"
	}
	return "moderate"
}

// ProjectPattern is a recurring pattern extracted from project history.
type ProjectPattern struct {
	Type        string `json:"type"`
	Description string `json:"description"`
	Suggestion  string `json:"suggestion"`
}

// MissingFileResult is a file listed in filesToModify or filesToRead that does not exist on disk.
type MissingFileResult struct {
	File       string `json:"file"`
	List       string `json:"list"` // "filesToModify" or "filesToRead"
	Suggestion string `json:"suggestion"`
}

// Signal is an informational probe result that provides context for the persona loop.
type Signal struct {
	Probe   string `json:"probe"`
	File    string `json:"file,omitempty"`
	Message string `json:"message"`
}

// PlanStats captures plan complexity metrics for the gate to determine required verification rounds.
type PlanStats struct {
	Steps         int `json:"steps"`
	FilesToModify int `json:"filesToModify"`
	FilesToCreate int `json:"filesToCreate"`
	FilesToRead   int `json:"filesToRead"`
}

// SuggestedAdditions contains files suggested to close detected gaps.
type SuggestedAdditions struct {
	FilesToModify []string       `json:"filesToModify,omitempty"`
	Ranked        []RankedFile   `json:"ranked,omitempty"` // top-K files by combined score
}

// RankedFile is a file ranked by combined structural + statistical score.
// Presented as "you probably forgot these" — precision ~57% on top-3.
type RankedFile struct {
	File     string  `json:"file"`
	Path     string  `json:"path,omitempty"`     // full relative path when known
	Score    float64 `json:"score"`              // 0.0 to 1.0
	Source   string  `json:"source"`             // what signals contributed
	Reason   string  `json:"reason"`             // task-specific WHY
	Verified bool    `json:"verified,omitempty"` // source-verified call site to plan file
	CallSite string  `json:"callSite,omitempty"` // e.g., "CreateRun() calls shared.SubmitPR at line 142"
}

// FilePrediction is the combined prediction for a single file from the
// three-signal model (structural + comod + semantic).
type FilePrediction struct {
	File       string  `json:"file"`
	Combined   float64 `json:"combined"`   // weighted aggregate probability
	Confidence string  `json:"confidence"` // must, likely, consider
	Reason     string  `json:"reason"`
}

// NoveltySummary captures how novel a plan is for the check_plan response.
type NoveltySummary struct {
	Score       float64 `json:"score"`       // 0.0-1.0
	Label       string  `json:"label"`       // routine/extension/novel/exploratory
	Uncertainty string  `json:"uncertainty"` // narrow/moderate/wide/very wide
	Guidance    string  `json:"guidance"`
}

// ForecastSummary is the MC forecast output included in check_plan results.
type ForecastSummary struct {
	PClean   float64 `json:"pClean"`   // probability of clean execution
	PRework  float64 `json:"pRework"`  // probability of minor rework
	PFailed  float64 `json:"pFailed"`  // probability of significant rework
	RecallP50 float64 `json:"recallP50"`
	RecallP85 float64 `json:"recallP85"`
	BasedOn   int     `json:"basedOn"` // number of historical data points
	Summary   string  `json:"summary"`
}

// SimulationSummary captures structural prediction data from plan simulation.
type SimulationSummary struct {
	ProductionCallers int      `json:"productionCallers,omitempty"`
	TestCoverage      int      `json:"testCoverage,omitempty"`
	TestDensity       float64  `json:"testDensity,omitempty"`
	Confidence        string   `json:"confidence,omitempty"`        // high, moderate, low
	HighImpactDefs    []string `json:"highImpactDefs,omitempty"`
	BaseRateRecall    float64  `json:"baseRateRecall,omitempty"`    // historical recall for similar changes
	BaseRateHitRate   float64  `json:"baseRateHitRate,omitempty"`   // historical hit rate for similar changes
	BaseRateSource    string   `json:"baseRateSource,omitempty"`    // what reference class was used
	CascadeDepth      int      `json:"cascadeDepth,omitempty"`      // depth of ripple chain
	CascadeFiles      int      `json:"cascadeFiles,omitempty"`      // unique files in ripple chain
	CascadeBreaks     int      `json:"cascadeBreaks,omitempty"`     // total breakages across chain
	CascadeConverged  bool     `json:"cascadeConverged,omitempty"`  // did ripples die out?
}

// PlanCheckResult is the complete output of a plan check.
// Contains findings from deterministic probes and signals for verification.
// ImplementationPreview is a pre-implementation code review of the future.
// The spike dreams the implementation; the compiler reality-checks it.
type ImplementationPreview struct {
	// FileChanges describes what the spike changed in each file
	FileChanges []FileChange `json:"fileChanges,omitempty"`
	// Obligations are compiler-verified files that MUST change
	Obligations []Obligation `json:"obligations,omitempty"`
	// Risks are things that could go wrong during implementation
	Risks []string `json:"risks,omitempty"`
}

// FileChange describes what the spike changed in a specific file.
type FileChange struct {
	File    string `json:"file"`
	Summary string `json:"summary"` // human-readable: "adds Draft bool field to CreateOptions"
	Kind    string `json:"kind"`    // "struct-field", "new-function", "signature-change", "body-change"
	Diff    string `json:"diff,omitempty"` // condensed diff showing the actual code changes
}

// Obligation is a file that MUST change based on compiler or type-system analysis.
type Obligation struct {
	File   string `json:"file"`
	Reason string `json:"reason"` // "positional constructor breaks", "caller passes wrong args"
}

type PlanCheckResult struct {
	HistoryID          string              `json:"historyId"`
	ProjectType        string              `json:"projectType"`
	PlanStats          PlanStats           `json:"planStats"`
	MissingFiles       []MissingFileResult `json:"missingFiles,omitempty"`
	ComodGaps          []ComodGap          `json:"comodGaps,omitempty"`
	Simulation         *SimulationSummary  `json:"simulation,omitempty"`
	Forecast           *ForecastSummary    `json:"forecast,omitempty"`
	Novelty            *NoveltySummary     `json:"novelty,omitempty"`
	Predictions        []FilePrediction    `json:"predictions,omitempty"`
	ProjectPatterns    []ProjectPattern    `json:"projectPatterns,omitempty"`
	Signals            []Signal            `json:"signals,omitempty"`
	Critique           []string            `json:"critique,omitempty"`
	SuggestedAdditions SuggestedAdditions  `json:"suggestedAdditions"`
	Preview            *ImplementationPreview `json:"preview,omitempty"`
	Cost               *CostSummary        `json:"cost,omitempty"`
}

// CostSummary tracks API token usage and estimated cost for a plan check.
type CostSummary struct {
	InputTokens  int     `json:"inputTokens"`
	OutputTokens int     `json:"outputTokens"`
	TotalTokens  int     `json:"totalTokens"`
	APICallCount int     `json:"apiCallCount"`
	EstimatedUSD float64 `json:"estimatedUSD"`
	Model        string  `json:"model"`
}

// AddTokens accumulates token usage from a single API call and updates estimated cost.
func (c *CostSummary) AddTokens(input, output int, model string) {
	c.InputTokens += input
	c.OutputTokens += output
	c.TotalTokens = c.InputTokens + c.OutputTokens
	c.APICallCount++
	if c.Model == "" {
		c.Model = model
	}
	c.EstimatedUSD = estimateCost(c.InputTokens, c.OutputTokens, c.Model)
}

// Merge adds another CostSummary's tokens into this one.
// Preserves accurate cost by adding the other's already-computed estimate
// rather than re-pricing all tokens at the receiver's model rate.
func (c *CostSummary) Merge(other *CostSummary) {
	if other == nil {
		return
	}
	c.InputTokens += other.InputTokens
	c.OutputTokens += other.OutputTokens
	c.TotalTokens = c.InputTokens + c.OutputTokens
	c.APICallCount += other.APICallCount
	c.EstimatedUSD += other.EstimatedUSD
}

// modelPricing maps model names to per-token pricing (USD).
var modelPricing = map[string][2]float64{
	"claude-sonnet-4-6":         {3.0 / 1e6, 15.0 / 1e6},
	"claude-haiku-4-5-20251001": {0.80 / 1e6, 4.0 / 1e6},
	"claude-opus-4-6":           {15.0 / 1e6, 75.0 / 1e6},
}

func estimateCost(input, output int, model string) float64 {
	prices, ok := modelPricing[model]
	if !ok {
		// Default to Sonnet pricing
		prices = modelPricing["claude-sonnet-4-6"]
	}
	return float64(input)*prices[0] + float64(output)*prices[1]
}

// EnsureNonNil ensures all slice fields are non-nil for JSON marshaling.
func (r *PlanCheckResult) EnsureNonNil() {
	if r.MissingFiles == nil {
		r.MissingFiles = []MissingFileResult{}
	}
	if r.ComodGaps == nil {
		r.ComodGaps = []ComodGap{}
	}
	if r.ProjectPatterns == nil {
		r.ProjectPatterns = []ProjectPattern{}
	}
	if r.Signals == nil {
		r.Signals = []Signal{}
	}
	if r.Critique == nil {
		r.Critique = []string{}
	}
	if r.SuggestedAdditions.FilesToModify == nil {
		r.SuggestedAdditions.FilesToModify = []string{}
	}
}

// FindingCounts holds counts per finding category for the compact response.
type FindingCounts struct {
	MissingFiles       int `json:"missingFiles,omitempty"`
	ComodGaps          int `json:"comodGaps,omitempty"`
	RefGraphGaps       int `json:"refGraphGaps,omitempty"`
	HighConfidenceGaps int `json:"highConfidenceGaps,omitempty"`
	Signals            int `json:"signals,omitempty"`
}

// CompactCheckResult is a summary view of PlanCheckResult, returned by check_plan.
// Full details are available via get_check_details.
type CompactCheckResult struct {
	// Minto pyramid: answer first, then evidence
	Summary            string             `json:"summary"`            // one-paragraph recommendation
	ServerVersion      string             `json:"serverVersion,omitempty"`
	HistoryID          string             `json:"historyId"`
	ProjectType        string             `json:"projectType"`
	Findings           FindingCounts      `json:"findings"`
	Simulation         *SimulationSummary `json:"simulation,omitempty"`
	Forecast           *ForecastSummary   `json:"forecast,omitempty"`
	Novelty            *NoveltySummary    `json:"novelty,omitempty"`
	Predictions        []FilePrediction   `json:"predictions,omitempty"`
	TopFindings        []string           `json:"topFindings,omitempty"`
	SuggestedAdditions SuggestedAdditions `json:"suggestedAdditions,omitempty"`
	ProjectPatterns    []ProjectPattern   `json:"projectPatterns,omitempty"`
}

// ToCompact converts a full PlanCheckResult to a CompactCheckResult.
func (r *PlanCheckResult) ToCompact() CompactCheckResult {
	highConf := 0
	refGraph := 0
	for _, g := range r.ComodGaps {
		if g.Confidence == "high" && !g.Acknowledged && !g.Hub {
			highConf++
		}
		if strings.HasPrefix(g.Suggestion, "[refgraph]") {
			refGraph++
		}
	}
	c := CompactCheckResult{
		HistoryID:   r.HistoryID,
		ProjectType: r.ProjectType,
		Findings: FindingCounts{
			MissingFiles:       len(r.MissingFiles),
			ComodGaps:          len(r.ComodGaps) - refGraph,
			RefGraphGaps:       refGraph,
			HighConfidenceGaps: highConf,
			Signals:            len(r.Signals),
		},
		Simulation:         r.Simulation,
		Forecast:           r.Forecast,
		Novelty:            r.Novelty,
		Predictions:        r.Predictions,
		SuggestedAdditions: r.SuggestedAdditions,
		ProjectPatterns:    r.ProjectPatterns,
	}
	// Include top critique entries so the model gets actionable info in one call
	limit := 5
	if len(r.Critique) < limit {
		limit = len(r.Critique)
	}
	if limit > 0 {
		c.TopFindings = make([]string, limit)
		copy(c.TopFindings, r.Critique[:limit])
	}

	// Build Minto pyramid summary: answer first, then evidence
	c.Summary = buildSummary(r)
	return c
}

// buildSummary creates the top-of-pyramid recommendation.
// Answer → Why → Context, in that order.
func buildSummary(r *PlanCheckResult) string {
	var parts []string

	// ANSWER: what should the model do?
	ranked := r.SuggestedAdditions.Ranked
	if len(r.MissingFiles) > 0 {
		parts = append(parts, fmt.Sprintf("FIX: %d file(s) in your plan don't exist on disk.", len(r.MissingFiles)))
	}

	if len(ranked) > 0 {
		fileList := ""
		for i, rf := range ranked {
			if i >= 3 {
				break
			}
			if i > 0 {
				fileList += ", "
			}
			fileList += rf.File
		}
		parts = append(parts, fmt.Sprintf("ADD: Consider %s.", fileList))
	}

	if len(parts) == 0 {
		parts = append(parts, "Plan looks structurally complete.")
	}

	// WHY: reasons for the top suggestions
	for i, rf := range ranked {
		if i >= 3 {
			break
		}
		if rf.Reason != "" {
			parts = append(parts, fmt.Sprintf("  %s — %s", rf.File, rf.Reason))
		}
	}

	// CONTEXT: novelty + forecast (one line each)
	if r.Novelty != nil {
		switch r.Novelty.Label {
		case "routine":
			parts = append(parts, "Routine change — structural signals are reliable.")
		case "extension":
			parts = append(parts, "Extension — mix of known code and new additions.")
		case "novel", "exploratory":
			parts = append(parts, fmt.Sprintf("Novel work (%s uncertainty) — also check sibling files in the same package.", r.Novelty.Uncertainty))
		}
	}

	if r.Forecast != nil && r.Forecast.PFailed > 0.3 {
		parts = append(parts, fmt.Sprintf("Forecast: %.0f%% risk of rework (based on %d similar plans).",
			r.Forecast.PFailed*100, r.Forecast.BasedOn))
	}

	return strings.Join(parts, " ")
}
