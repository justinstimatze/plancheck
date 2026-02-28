package signals

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/justinstimatze/plancheck/internal/types"
)

func makePlan(fields types.ExecutionPlan) types.ExecutionPlan {
	if fields.Objective == "" {
		fields.Objective = "test"
	}
	if fields.FilesToRead == nil {
		fields.FilesToRead = []string{}
	}
	if fields.FilesToModify == nil {
		fields.FilesToModify = []string{}
	}
	if fields.FilesToCreate == nil {
		fields.FilesToCreate = []string{}
	}
	if fields.Steps == nil {
		fields.Steps = []string{}
	}
	return fields
}

func filterByProbe(signals []types.Signal, probe string) []types.Signal {
	var filtered []types.Signal
	for _, s := range signals {
		if s.Probe == probe {
			filtered = append(filtered, s)
		}
	}
	return filtered
}

// ── Test pairing ──────────────────────────────────────────────

func TestTestPairing_ExistingTestNotInPlan(t *testing.T) {
	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "src")
	os.MkdirAll(srcDir, 0o755)
	os.WriteFile(filepath.Join(srcDir, "app.ts"), []byte(""), 0o644)
	os.WriteFile(filepath.Join(srcDir, "app.test.ts"), []byte(""), 0o644)
	signals := Check(makePlan(types.ExecutionPlan{
		FilesToModify: []string{"src/app.ts"},
	}), tmp)
	pairing := filterByProbe(signals, "test-pairing")
	if len(pairing) != 1 {
		t.Fatalf("expected 1 test-pairing signal, got %d", len(pairing))
	}
	if pairing[0].File != "src/app.test.ts" {
		t.Errorf("expected file 'src/app.test.ts', got %q", pairing[0].File)
	}
}

func TestTestPairing_TestInPlan(t *testing.T) {
	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "src")
	os.MkdirAll(srcDir, 0o755)
	os.WriteFile(filepath.Join(srcDir, "app.ts"), []byte(""), 0o644)
	os.WriteFile(filepath.Join(srcDir, "app.test.ts"), []byte(""), 0o644)
	signals := Check(makePlan(types.ExecutionPlan{
		FilesToModify: []string{"src/app.ts", "src/app.test.ts"},
	}), tmp)
	if len(filterByProbe(signals, "test-pairing")) != 0 {
		t.Error("expected no test-pairing signal")
	}
}

func TestTestPairing_NoTestOnDisk(t *testing.T) {
	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "src")
	os.MkdirAll(srcDir, 0o755)
	os.WriteFile(filepath.Join(srcDir, "app.ts"), []byte(""), 0o644)
	signals := Check(makePlan(types.ExecutionPlan{
		FilesToModify: []string{"src/app.ts"},
	}), tmp)
	if len(filterByProbe(signals, "test-pairing")) != 0 {
		t.Error("expected no test-pairing signal")
	}
}

func TestTestPairing_SpecVariant(t *testing.T) {
	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "src")
	os.MkdirAll(srcDir, 0o755)
	os.WriteFile(filepath.Join(srcDir, "app.ts"), []byte(""), 0o644)
	os.WriteFile(filepath.Join(srcDir, "app.spec.ts"), []byte(""), 0o644)
	signals := Check(makePlan(types.ExecutionPlan{
		FilesToModify: []string{"src/app.ts"},
	}), tmp)
	pairing := filterByProbe(signals, "test-pairing")
	if len(pairing) != 1 {
		t.Fatalf("expected 1 test-pairing signal, got %d", len(pairing))
	}
	if pairing[0].File != "src/app.spec.ts" {
		t.Errorf("expected file 'src/app.spec.ts', got %q", pairing[0].File)
	}
}

// ── Lock staleness ────────────────────────────────────────────

func TestLockStaleness_ModifiesPackageJsonNoInstall(t *testing.T) {
	signals := Check(makePlan(types.ExecutionPlan{
		FilesToModify: []string{"package.json"},
		Steps:         []string{"add new dependency to package.json"},
	}), "/tmp")
	lock := filterByProbe(signals, "lock-staleness")
	if len(lock) != 1 {
		t.Fatalf("expected 1 lock-staleness signal, got %d", len(lock))
	}
}

func TestLockStaleness_WithInstallStep(t *testing.T) {
	signals := Check(makePlan(types.ExecutionPlan{
		FilesToModify: []string{"package.json"},
		Steps:         []string{"add new dependency", "run bun install"},
	}), "/tmp")
	if len(filterByProbe(signals, "lock-staleness")) != 0 {
		t.Error("expected no lock-staleness signal")
	}
}

func TestLockStaleness_NoDependencyFiles(t *testing.T) {
	signals := Check(makePlan(types.ExecutionPlan{
		FilesToModify: []string{"src/app.ts"},
		Steps:         []string{"update app"},
	}), "/tmp")
	if len(filterByProbe(signals, "lock-staleness")) != 0 {
		t.Error("expected no lock-staleness signal")
	}
}

func TestLockStaleness_RequirementsTxtTriggersCheck(t *testing.T) {
	signals := Check(makePlan(types.ExecutionPlan{
		FilesToModify: []string{"requirements.txt"},
		Steps:         []string{"add flask dependency"},
	}), "/tmp")
	if len(filterByProbe(signals, "lock-staleness")) != 1 {
		t.Error("expected 1 lock-staleness signal")
	}
}

func TestLockStaleness_PipKeywordClears(t *testing.T) {
	signals := Check(makePlan(types.ExecutionPlan{
		FilesToModify: []string{"requirements.txt"},
		Steps:         []string{"add flask", "pip install -r requirements.txt"},
	}), "/tmp")
	if len(filterByProbe(signals, "lock-staleness")) != 0 {
		t.Error("expected no lock-staleness signal")
	}
}

// ── Churn ─────────────────────────────────────────────────────

func TestChurn_NonGitDir(t *testing.T) {
	tmp := t.TempDir()
	signals := Check(makePlan(types.ExecutionPlan{
		FilesToModify: []string{"src/app.ts"},
	}), tmp)
	if len(filterByProbe(signals, "churn")) != 0 {
		t.Error("expected no churn signal")
	}
}

func TestChurn_NoFilesToModify(t *testing.T) {
	tmp := t.TempDir()
	signals := Check(makePlan(types.ExecutionPlan{}), tmp)
	if len(filterByProbe(signals, "churn")) != 0 {
		t.Error("expected no churn signal")
	}
}

// ── Import chain ──────────────────────────────────────────────

func TestImportChain_DetectsImporter(t *testing.T) {
	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "src")
	os.MkdirAll(srcDir, 0o755)
	os.WriteFile(filepath.Join(srcDir, "utils.ts"), []byte("export function foo() {}"), 0o644)
	os.WriteFile(filepath.Join(srcDir, "app.ts"), []byte(`import { foo } from "./utils"`), 0o644)

	signals := Check(makePlan(types.ExecutionPlan{
		FilesToModify: []string{"src/utils.ts"},
	}), tmp)
	chain := filterByProbe(signals, "import-chain")
	if len(chain) != 1 {
		t.Fatalf("expected 1 import-chain signal, got %d", len(chain))
	}
	if !strings.Contains(chain[0].Message, "app.ts") {
		t.Errorf("expected message to mention app.ts, got %q", chain[0].Message)
	}
}

func TestImportChain_ImporterInPlanExcluded(t *testing.T) {
	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "src")
	os.MkdirAll(srcDir, 0o755)
	os.WriteFile(filepath.Join(srcDir, "utils.ts"), []byte("export function foo() {}"), 0o644)
	os.WriteFile(filepath.Join(srcDir, "app.ts"), []byte(`import { foo } from "./utils"`), 0o644)

	signals := Check(makePlan(types.ExecutionPlan{
		FilesToModify: []string{"src/utils.ts", "src/app.ts"},
	}), tmp)
	chain := filterByProbe(signals, "import-chain")
	if len(chain) != 0 {
		t.Errorf("expected no import-chain signal when importer is in plan, got %d", len(chain))
	}
}

func TestImportChain_NoImporters(t *testing.T) {
	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "src")
	os.MkdirAll(srcDir, 0o755)
	os.WriteFile(filepath.Join(srcDir, "utils.ts"), []byte("export function foo() {}"), 0o644)
	os.WriteFile(filepath.Join(srcDir, "other.ts"), []byte("const x = 1"), 0o644)

	signals := Check(makePlan(types.ExecutionPlan{
		FilesToModify: []string{"src/utils.ts"},
	}), tmp)
	chain := filterByProbe(signals, "import-chain")
	if len(chain) != 0 {
		t.Errorf("expected no import-chain signal, got %d", len(chain))
	}
}

func TestImportChain_CollapseAboveThreshold(t *testing.T) {
	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "src")
	os.MkdirAll(srcDir, 0o755)
	os.WriteFile(filepath.Join(srcDir, "utils.ts"), []byte("export function foo() {}"), 0o644)
	for i := 0; i < 7; i++ {
		name := filepath.Join(srcDir, fmt.Sprintf("consumer%d.ts", i))
		os.WriteFile(name, []byte(`import { foo } from "./utils"`), 0o644)
	}

	signals := Check(makePlan(types.ExecutionPlan{
		FilesToModify: []string{"src/utils.ts"},
	}), tmp)
	chain := filterByProbe(signals, "import-chain")
	if len(chain) != 1 {
		t.Fatalf("expected 1 collapsed import-chain signal, got %d", len(chain))
	}
	if !strings.Contains(chain[0].Message, "7 importers") {
		t.Errorf("expected collapse message with count, got %q", chain[0].Message)
	}
}

func TestImportChain_GoImports(t *testing.T) {
	tmp := t.TempDir()
	pkgDir := filepath.Join(tmp, "pkg")
	os.MkdirAll(pkgDir, 0o755)
	os.WriteFile(filepath.Join(pkgDir, "utils.go"), []byte("package pkg\nfunc Foo() {}"), 0o644)
	os.WriteFile(filepath.Join(pkgDir, "consumer.go"), []byte("package pkg\n\nimport (\n\t\"example/utils\"\n)\n"), 0o644)

	signals := Check(makePlan(types.ExecutionPlan{
		FilesToModify: []string{"pkg/utils.go"},
	}), tmp)
	chain := filterByProbe(signals, "import-chain")
	if len(chain) != 1 {
		t.Fatalf("expected 1 import-chain signal for Go import, got %d", len(chain))
	}
}

func TestImportChain_NoFilesToModify(t *testing.T) {
	tmp := t.TempDir()
	signals := Check(makePlan(types.ExecutionPlan{}), tmp)
	chain := filterByProbe(signals, "import-chain")
	if len(chain) != 0 {
		t.Error("expected no import-chain signal with empty plan")
	}
}
