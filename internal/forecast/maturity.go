// maturity.go computes a project maturity score that affects forecasting.
//
// Not a binary greenfield/brownfield split — a continuous axis from
// "brand new, no history, no tests" to "mature, deep history, well-tested."
//
// The maturity score determines:
// 1. Which signals are reliable (comod needs history, graph needs definitions)
// 2. Which forecasting baseline to use (cross-project vs project-specific)
// 3. What kind of verification is most valuable (prerequisites vs blast radius)
package forecast

import (
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/justinstimatze/plancheck/internal/refgraph"
)

// Maturity represents where a project sits on the greenfield→mature axis.
type Maturity struct {
	Score        float64           `json:"score"` // 0.0 (brand new) to 1.0 (deeply mature)
	Label        string            `json:"label"` // "nascent", "growing", "established", "mature"
	Signals      SignalReliability `json:"signals"`
	Verification string            `json:"verification"` // what kind of verification is most valuable

	// Raw metrics that feed the score
	GitCommits  int     `json:"gitCommits"`
	GitAge      int     `json:"gitAgeDays"` // days since first commit
	TestDensity float64 `json:"testDensity"`
	FileCount   int     `json:"fileCount"`
	DefnAvail   bool    `json:"defnAvailable"`
}

// SignalReliability indicates how much to trust each signal for this project.
type SignalReliability struct {
	Structural string `json:"structural"` // "strong", "moderate", "weak", "unavailable"
	Comod      string `json:"comod"`      // same
	Semantic   string `json:"semantic"`   // always "available" (the LLM is always there)
}

// Assess computes the maturity of a project at the given path.
func Assess(cwd string) Maturity {
	m := Maturity{}

	// Git history depth
	m.GitCommits = countGitCommits(cwd)
	m.GitAge = gitAgeDays(cwd)

	// File count
	m.FileCount = countGoFiles(cwd)

	// Test density (from defn if available, else estimate from file names)
	m.DefnAvail = hasDefn(cwd)
	if m.DefnAvail {
		m.TestDensity = queryTestDensity(cwd)
	} else {
		m.TestDensity = estimateTestDensity(cwd)
	}

	// Compute composite score (0.0 to 1.0)
	// Each factor contributes proportionally:
	//   git history: 0-200 commits → 0.0-0.3
	//   git age: 0-365 days → 0.0-0.2
	//   test density: 0-60% → 0.0-0.3
	//   file count: 0-50 files → 0.0-0.1
	//   defn: 0 or 0.1
	score := 0.0
	score += math.Min(float64(m.GitCommits)/200.0, 1.0) * 0.3
	score += math.Min(float64(m.GitAge)/365.0, 1.0) * 0.2
	score += math.Min(m.TestDensity/0.60, 1.0) * 0.3
	score += math.Min(float64(m.FileCount)/50.0, 1.0) * 0.1
	if m.DefnAvail {
		score += 0.1
	}
	m.Score = math.Round(score*100) / 100

	// Label
	switch {
	case m.Score < 0.2:
		m.Label = "nascent"
	case m.Score < 0.4:
		m.Label = "growing"
	case m.Score < 0.7:
		m.Label = "established"
	default:
		m.Label = "mature"
	}

	// Signal reliability
	m.Signals = assessSignals(m)

	// Best verification approach
	m.Verification = bestVerification(m)

	return m
}

func assessSignals(m Maturity) SignalReliability {
	s := SignalReliability{Semantic: "available"}

	// Structural: needs defn + definitions
	if !m.DefnAvail {
		s.Structural = "unavailable"
	} else if m.FileCount < 5 {
		s.Structural = "weak"
	} else if m.TestDensity < 0.20 {
		s.Structural = "moderate"
	} else {
		s.Structural = "strong"
	}

	// Comod: needs git history
	if m.GitCommits < 10 {
		s.Comod = "unavailable"
	} else if m.GitCommits < 50 {
		s.Comod = "weak"
	} else if m.GitCommits < 200 {
		s.Comod = "moderate"
	} else {
		s.Comod = "strong"
	}

	return s
}

func bestVerification(m Maturity) string {
	switch {
	case m.Score < 0.2:
		// Nascent: no history, no graph. Focus on prerequisites.
		return "backward-scout (check prerequisites exist, verify plan completeness)"
	case m.Score < 0.4:
		// Growing: some history, maybe graph. Mix of forward and backward.
		return "bidirectional (forward blast radius + backward prerequisites)"
	case m.Score < 0.7:
		// Established: good history and graph. Forward + comod.
		return "combined-model (structural + comod union, ranked suggestions)"
	default:
		// Mature: deep history. Full model with MC forecasting.
		return "full-forecast (combined model + MC outcome prediction)"
	}
}

func countGitCommits(cwd string) int {
	cmd := exec.Command("git", "-C", cwd, "rev-list", "--count", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return 0
	}
	n := 0
	for _, c := range strings.TrimSpace(string(out)) {
		if c >= '0' && c <= '9' {
			n = n*10 + int(c-'0')
		}
	}
	return n
}

func gitAgeDays(cwd string) int {
	cmd := exec.Command("git", "-C", cwd, "log", "--reverse", "--format=%ct", "-1")
	out, err := cmd.Output()
	if err != nil {
		return 0
	}
	ts := 0
	for _, c := range strings.TrimSpace(string(out)) {
		if c >= '0' && c <= '9' {
			ts = ts*10 + int(c-'0')
		}
	}
	if ts == 0 {
		return 0
	}
	// Current time approximation (seconds since epoch)
	now := exec.Command("date", "+%s")
	nowOut, err := now.Output()
	if err != nil {
		return 0
	}
	nowTs := 0
	for _, c := range strings.TrimSpace(string(nowOut)) {
		if c >= '0' && c <= '9' {
			nowTs = nowTs*10 + int(c-'0')
		}
	}
	days := (nowTs - ts) / 86400
	if days < 0 {
		return 0
	}
	return days
}

func countGoFiles(cwd string) int {
	count := 0
	filepath.Walk(cwd, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if strings.Contains(path, "vendor/") || strings.Contains(path, ".git/") ||
			strings.Contains(path, "node_modules/") || strings.Contains(path, ".defn/") {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !info.IsDir() && strings.HasSuffix(info.Name(), ".go") {
			count++
		}
		return nil
	})
	return count
}

func hasDefn(cwd string) bool {
	info, err := os.Stat(filepath.Join(cwd, ".defn"))
	return err == nil && info.IsDir()
}

func queryTestDensity(cwd string) float64 {
	rows := refgraph.QueryDefn(cwd,
		"SELECT COUNT(CASE WHEN test=TRUE THEN 1 END) as t, COUNT(*) as n FROM definitions")
	if len(rows) == 0 {
		return estimateTestDensity(cwd)
	}
	t, n := intField(rows[0], "t"), intField(rows[0], "n")
	if n == 0 {
		return 0
	}
	return float64(t) / float64(n)
}

func estimateTestDensity(cwd string) float64 {
	total := 0
	tests := 0
	filepath.Walk(cwd, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if strings.Contains(path, "vendor/") || strings.Contains(path, ".git/") {
			return nil
		}
		if strings.HasSuffix(info.Name(), ".go") {
			total++
			if strings.HasSuffix(info.Name(), "_test.go") {
				tests++
			}
		}
		return nil
	})
	if total == 0 {
		return 0
	}
	return float64(tests) / float64(total)
}
