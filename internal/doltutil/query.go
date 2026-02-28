// Package doltutil provides shared Dolt database query utilities.
package doltutil

import (
	"encoding/json"
	"os/exec"
	"strings"
	"time"
)

// Query runs a SQL query against a Dolt database directory.
// Retries up to 3 times on transient lock/manifest errors.
func Query(defnDir string, sql string) ([]map[string]interface{}, error) {
	var out []byte
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		var stderr strings.Builder
		cmd := exec.Command("dolt", "sql", "-q", sql, "-r", "json")
		cmd.Dir = defnDir
		cmd.Stderr = &stderr
		out, err = cmd.Output()
		if err == nil {
			break
		}
		errMsg := stderr.String() + err.Error()
		if !strings.Contains(errMsg, "read only") && !strings.Contains(errMsg, "lock") && !strings.Contains(errMsg, "manifest") {
			return nil, err
		}
		time.Sleep(time.Duration(attempt+1) * 100 * time.Millisecond)
	}
	if err != nil {
		return nil, err
	}

	var result struct {
		Rows []map[string]interface{} `json:"rows"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		return nil, err
	}
	return result.Rows, nil
}
