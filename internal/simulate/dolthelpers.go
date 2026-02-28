// dolthelpers.go provides Dolt query helpers for backward.go and replay.go
// which need SQL queries that don't map to graph library methods (subqueries,
// joins on bodies table, etc.).
//
// simulate.go and cascade.go use the in-memory graph instead.
package simulate

import "github.com/justinstimatze/plancheck/internal/doltutil"

// doltQuery runs a SQL query against a .defn/ database via the dolt CLI.
func doltQuery(defnDir string, sql string) []map[string]interface{} {
	rows, _ := doltutil.Query(defnDir, sql)
	return rows
}

func intVal(row map[string]interface{}, key string) int {
	v, ok := row[key]
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	default:
		return 0
	}
}

func strVal(row map[string]interface{}, key string) string {
	v, _ := row[key].(string)
	return v
}

