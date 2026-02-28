// Package simulate — replay.go compares simulation predictions against
// actual execution outcomes to produce accuracy scores.
//
// This is the Brier score for code plans: did the simulation correctly
// predict which definitions would be affected?
package simulate

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ReplayResult compares simulation predictions against actual changes.
type ReplayResult struct {
	// What we predicted
	PredictedFiles      []string `json:"predictedFiles"`
	PredictedCallers    int      `json:"predictedCallers"`
	PredictedTests      int      `json:"predictedTests"`

	// What actually happened
	ActualFiles         []string `json:"actualFiles"`
	ActualDefinitions   int      `json:"actualDefinitions"`

	// Accuracy
	TruePositiveFiles   []string `json:"truePositiveFiles"`
	FalsePositiveFiles  []string `json:"falsePositiveFiles"`
	FalseNegativeFiles  []string `json:"falseNegativeFiles"`
	Precision           float64  `json:"precision"`
	Recall              float64  `json:"recall"`
	F1                  float64  `json:"f1"`
	BrierScore          float64  `json:"brierScore"` // lower is better, 0 = perfect

	Summary             string   `json:"summary"`
}

// Replay compares a simulation result against the actual git diff since baseRef.
func Replay(cwd string, simResult Result, baseRef string) (ReplayResult, error) {
	var replay ReplayResult

	// Get actual files changed since baseRef
	actualFiles, err := getChangedFiles(cwd, baseRef)
	if err != nil {
		return replay, fmt.Errorf("git diff: %w", err)
	}
	replay.ActualFiles = actualFiles

	// Get predicted files from simulation steps
	predictedFiles := getPredictedFiles(cwd, simResult)
	replay.PredictedFiles = predictedFiles
	replay.PredictedCallers = simResult.Total.ProductionCallers
	replay.PredictedTests = simResult.Total.TestCoverage

	// Count actual definitions changed (if defn available)
	replay.ActualDefinitions = countChangedDefinitions(cwd, actualFiles)

	// Compute file-level accuracy
	actualSet := make(map[string]bool)
	for _, f := range actualFiles {
		actualSet[filepath.Base(f)] = true
	}
	predictedSet := make(map[string]bool)
	for _, f := range predictedFiles {
		predictedSet[f] = true
	}

	for f := range predictedSet {
		if actualSet[f] {
			replay.TruePositiveFiles = append(replay.TruePositiveFiles, f)
		} else {
			replay.FalsePositiveFiles = append(replay.FalsePositiveFiles, f)
		}
	}
	for f := range actualSet {
		if !predictedSet[f] {
			replay.FalseNegativeFiles = append(replay.FalseNegativeFiles, f)
		}
	}

	// Precision, recall, F1
	tp := float64(len(replay.TruePositiveFiles))
	fp := float64(len(replay.FalsePositiveFiles))
	fn := float64(len(replay.FalseNegativeFiles))

	if tp+fp > 0 {
		replay.Precision = tp / (tp + fp)
	}
	if tp+fn > 0 {
		replay.Recall = tp / (tp + fn)
	}
	if replay.Precision+replay.Recall > 0 {
		replay.F1 = 2 * replay.Precision * replay.Recall / (replay.Precision + replay.Recall)
	}

	// Brier score: mean squared error of binary predictions
	// For each actual file: was it predicted? (1 if yes, 0 if no)
	// For each predicted file: did it actually change? (1 if yes, 0 if no)
	allFiles := make(map[string]bool)
	for f := range actualSet {
		allFiles[f] = true
	}
	for f := range predictedSet {
		allFiles[f] = true
	}
	if len(allFiles) > 0 {
		sumSqErr := 0.0
		for f := range allFiles {
			predicted := 0.0
			if predictedSet[f] {
				predicted = 1.0
			}
			actual := 0.0
			if actualSet[f] {
				actual = 1.0
			}
			sumSqErr += math.Pow(predicted-actual, 2)
		}
		replay.BrierScore = sumSqErr / float64(len(allFiles))
	}

	// Summary
	replay.Summary = fmt.Sprintf(
		"Predicted %d files, %d actually changed. "+
			"Recall: %.0f%% (%d/%d files predicted correctly). "+
			"Precision: %.0f%%. Brier: %.2f.",
		len(predictedFiles), len(actualFiles),
		replay.Recall*100, len(replay.TruePositiveFiles), len(actualFiles),
		replay.Precision*100, replay.BrierScore)

	return replay, nil
}

func getChangedFiles(cwd, baseRef string) ([]string, error) {
	cmd := exec.Command("git", "-C", cwd, "diff", "--name-only", baseRef)
	out, err := cmd.Output()
	if err != nil {
		// Try HEAD~1 as fallback
		cmd = exec.Command("git", "-C", cwd, "diff", "--name-only", "HEAD~1")
		out, err = cmd.Output()
		if err != nil {
			return nil, err
		}
	}

	var files []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line != "" && strings.HasSuffix(line, ".go") {
			files = append(files, line)
		}
	}
	return files, nil
}

func getPredictedFiles(cwd string, simResult Result) []string {
	defnDir := filepath.Join(cwd, ".defn")
	fileSet := make(map[string]bool)

	for _, step := range simResult.Steps {
		if !step.DefinitionFound {
			continue
		}
		// Find source files of callers
		for _, caller := range step.TopCallers {
			name := caller.Name
			recv := caller.Receiver
			var where string
			if recv != "" {
				where = fmt.Sprintf("name = '%s' AND receiver = '%s'", name, recv)
			} else {
				where = fmt.Sprintf("name = '%s'", name)
			}
			rows := doltQuery(defnDir, fmt.Sprintf(
				"SELECT source_file FROM definitions WHERE %s AND test = FALSE AND source_file != '' LIMIT 1", where))
			for _, r := range rows {
				if f := strVal(r, "source_file"); f != "" {
					fileSet[f] = true
				}
			}
		}

		// Also get the source file of the mutated definition itself
		name := step.Mutation.Name
		recv := step.Mutation.Receiver
		var where string
		if recv != "" {
			where = fmt.Sprintf("name = '%s' AND receiver = '%s'", name, recv)
		} else {
			where = fmt.Sprintf("name = '%s'", name)
		}
		rows := doltQuery(defnDir, fmt.Sprintf(
			"SELECT source_file FROM definitions WHERE %s AND test = FALSE AND source_file != '' LIMIT 1", where))
		for _, r := range rows {
			if f := strVal(r, "source_file"); f != "" {
				fileSet[f] = true
			}
		}
	}

	var files []string
	for f := range fileSet {
		files = append(files, f)
	}
	return files
}

func countChangedDefinitions(cwd string, changedFiles []string) int {
	defnDir := filepath.Join(cwd, ".defn")
	if _, err := os.Stat(defnDir); err != nil {
		return 0
	}

	total := 0
	for _, f := range changedFiles {
		base := filepath.Base(f)
		rows := doltQuery(defnDir, fmt.Sprintf(
			"SELECT COUNT(*) as n FROM definitions WHERE source_file = '%s' AND test = FALSE", base))
		if len(rows) > 0 {
			total += intVal(rows[0], "n")
		}
	}
	return total
}

// SaveSimulationPrediction saves a simulation result for later replay comparison.
func SaveSimulationPrediction(cwd string, result Result) error {
	dir := filepath.Join(cwd, ".plancheck-sim")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "last-simulation.json"), data, 0o600)
}

// LoadSimulationPrediction loads the last saved simulation result.
func LoadSimulationPrediction(cwd string) (*Result, error) {
	path := filepath.Join(cwd, ".plancheck-sim", "last-simulation.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var result Result
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, err
	}
	return &result, nil
}
