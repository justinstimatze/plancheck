// Package traces indexes Claude Code session traces for operational forecasting.
//
// Each Claude Code session produces a JSONL file with every tool call,
// file read, edit, and decision. This package extracts planning-relevant
// metrics from those traces:
//
//   - Which files were explored vs edited (exploration waste)
//   - Whether plan mode was used
//   - What check_plan found
//   - How many iterations before ExitPlanMode
//   - What the git diff was after the session
//
// This is the fourth signal: operational history. Like Rovo using Jira
// throughput to forecast delivery, plancheck uses session traces to
// forecast plan outcomes.
package traces

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// SessionIndex is the extracted metrics from one Claude Code session.
type SessionIndex struct {
	SessionID     string    `json:"sessionId"`
	Timestamp     time.Time `json:"timestamp"`
	// Activity metrics
	TotalMessages int      `json:"totalMessages"`
	ToolCalls     int      `json:"toolCalls"`
	FilesRead     []string `json:"filesRead"`
	FilesEdited   []string `json:"filesEdited"`
	FilesCreated  []string `json:"filesCreated"`
	// Planning metrics
	UsedPlanMode  bool     `json:"usedPlanMode"`
	CheckPlanCalls int     `json:"checkPlanCalls"`
	SimulateCalls int      `json:"simulateCalls"`
	// Outcome
	ExplorationRatio float64 `json:"explorationRatio"` // reads / (reads + edits)
	EditCount        int     `json:"editCount"`
	UniqueFilesRead  int     `json:"uniqueFilesRead"`
	UniqueFilesEdit  int     `json:"uniqueFilesEdited"`
}

// ProjectTrace is the accumulated index for a project.
type ProjectTrace struct {
	Project    string          `json:"project"`
	Sessions   []SessionIndex  `json:"sessions"`
	// Aggregate metrics (computed from sessions)
	AvgExplorationRatio float64 `json:"avgExplorationRatio"`
	AvgFilesPerSession  float64 `json:"avgFilesPerSession"`
	PlanModeUsageRate   float64 `json:"planModeUsageRate"`
	TotalSessions       int     `json:"totalSessions"`
}

// IndexSession extracts metrics from a single Claude Code session JSONL file.
func IndexSession(jsonlPath string) (SessionIndex, error) {
	idx := SessionIndex{
		SessionID: filepath.Base(strings.TrimSuffix(jsonlPath, ".jsonl")),
	}

	f, err := os.Open(jsonlPath)
	if err != nil {
		return idx, err
	}
	defer f.Close()

	readFiles := make(map[string]bool)
	editFiles := make(map[string]bool)
	createFiles := make(map[string]bool)

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024) // 1MB line buffer

	for scanner.Scan() {
		line := scanner.Bytes()
		idx.TotalMessages++

		// Parse just enough to extract tool calls
		var msg struct {
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
			Timestamp string `json:"timestamp"`
		}
		if json.Unmarshal(line, &msg) != nil {
			continue
		}

		if idx.Timestamp.IsZero() && msg.Timestamp != "" {
			idx.Timestamp, _ = time.Parse(time.RFC3339, msg.Timestamp)
		}

		// Parse content for tool calls
		var contents []struct {
			Type  string `json:"type"`
			Name  string `json:"name"`
			Input json.RawMessage `json:"input"`
		}
		if json.Unmarshal(msg.Message.Content, &contents) != nil {
			continue
		}

		for _, c := range contents {
			if c.Type != "tool_use" {
				continue
			}
			idx.ToolCalls++

			switch c.Name {
			case "Read":
				var input struct {
					FilePath string `json:"file_path"`
				}
				if json.Unmarshal(c.Input, &input) == nil && input.FilePath != "" {
					readFiles[input.FilePath] = true
				}

			case "Edit":
				var input struct {
					FilePath string `json:"file_path"`
				}
				if json.Unmarshal(c.Input, &input) == nil && input.FilePath != "" {
					editFiles[input.FilePath] = true
				}

			case "Write":
				var input struct {
					FilePath string `json:"file_path"`
				}
				if json.Unmarshal(c.Input, &input) == nil && input.FilePath != "" {
					createFiles[input.FilePath] = true
				}

			case "EnterPlanMode":
				idx.UsedPlanMode = true

			case "mcp__plancheck__check_plan":
				idx.CheckPlanCalls++

			case "mcp__plancheck__simulate_plan":
				idx.SimulateCalls++
			}
		}
	}

	for f := range readFiles {
		idx.FilesRead = append(idx.FilesRead, f)
	}
	for f := range editFiles {
		idx.FilesEdited = append(idx.FilesEdited, f)
	}
	for f := range createFiles {
		idx.FilesCreated = append(idx.FilesCreated, f)
	}

	idx.UniqueFilesRead = len(readFiles)
	idx.UniqueFilesEdit = len(editFiles) + len(createFiles)
	idx.EditCount = len(editFiles) + len(createFiles)

	total := float64(idx.UniqueFilesRead + idx.UniqueFilesEdit)
	if total > 0 {
		idx.ExplorationRatio = float64(idx.UniqueFilesRead) / total
	}

	return idx, nil
}

// IndexProject scans all session traces for a project and builds the index.
func IndexProject(cwd string) (ProjectTrace, error) {
	pt := ProjectTrace{Project: cwd}

	// Find the Claude Code project directory
	home, err := os.UserHomeDir()
	if err != nil {
		return pt, err
	}

	// Claude Code uses path-based directory names
	projectDirs, err := filepath.Glob(filepath.Join(home, ".claude", "projects", "*"))
	if err != nil {
		return pt, err
	}

	// Find the directory matching this project's cwd
	// Claude Code encodes paths as -home-user-Documents-project
	cwdEncoded := strings.TrimPrefix(strings.ReplaceAll(cwd, "/", "-"), "-")

	var projectDir string
	for _, dir := range projectDirs {
		base := filepath.Base(dir)
		// Fuzzy match: the encoded path should be a suffix of the dir name
		if strings.Contains(base, filepath.Base(cwd)) || base == cwdEncoded {
			projectDir = dir
			break
		}
	}

	if projectDir == "" {
		return pt, nil // no traces found
	}

	// Index all session JSONL files
	jsonlFiles, _ := filepath.Glob(filepath.Join(projectDir, "*.jsonl"))
	for _, jf := range jsonlFiles {
		idx, err := IndexSession(jf)
		if err != nil {
			continue
		}
		pt.Sessions = append(pt.Sessions, idx)
	}

	pt.TotalSessions = len(pt.Sessions)

	// Compute aggregates
	if pt.TotalSessions > 0 {
		totalExploration := 0.0
		totalFiles := 0.0
		planModeCount := 0
		for _, s := range pt.Sessions {
			totalExploration += s.ExplorationRatio
			totalFiles += float64(s.UniqueFilesEdit)
			if s.UsedPlanMode {
				planModeCount++
			}
		}
		pt.AvgExplorationRatio = totalExploration / float64(pt.TotalSessions)
		pt.AvgFilesPerSession = totalFiles / float64(pt.TotalSessions)
		pt.PlanModeUsageRate = float64(planModeCount) / float64(pt.TotalSessions)
	}

	return pt, nil
}
