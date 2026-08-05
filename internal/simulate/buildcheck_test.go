package simulate

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// repoRoot returns the plancheck module root, which is a real Go project the
// probe can build against.
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("expected go.mod at %s: %v", root, err)
	}
	return root
}

// TestRunBuildCheck_TimeoutIsNotAPass is the regression test for the bug where
// a killed probe build was indistinguishable from a clean one. `go build`
// writes its diagnostics as `file.go:line:col: msg`; a build killed before the
// compiler runs writes none, so the error parser found zero errors and
// RunBuildCheck returned an empty-but-non-nil BuildCheckResult. Every caller
// reads that as "the compiler found no obligations".
func TestRunBuildCheck_TimeoutIsNotAPass(t *testing.T) {
	root := repoRoot(t)
	target := "internal/simulate/buildcheck.go"

	original, err := os.ReadFile(filepath.Join(root, target))
	if err != nil {
		t.Fatalf("read target: %v", err)
	}

	// Force the deadline to expire before go build can produce a verdict.
	prev := buildProbeTimeoutSecs
	buildProbeTimeoutSecs = 0
	t.Cleanup(func() { buildProbeTimeoutSecs = prev })

	result, err := RunBuildCheck(map[string]string{target: string(original)}, root)

	if !errors.Is(err, ErrProbeTimeout) {
		t.Fatalf("want ErrProbeTimeout, got err=%v result=%+v", err, result)
	}
	if result != nil {
		t.Errorf("want nil result on timeout, got %+v — a non-nil zero result reads as a clean build", result)
	}
}

// TestRunBlastRadius_TimeoutIsNotAnEmptyCone covers the same failure on the
// blast-radius probe, where an empty result reads as "no dependent files".
func TestRunBlastRadius_TimeoutIsNotAnEmptyCone(t *testing.T) {
	root := repoRoot(t)

	prev := buildProbeTimeoutSecs
	buildProbeTimeoutSecs = 0
	t.Cleanup(func() { buildProbeTimeoutSecs = prev })

	result, err := RunBlastRadius([]string{"internal/simulate/buildcheck.go"}, root)

	if !errors.Is(err, ErrProbeTimeout) {
		t.Fatalf("want ErrProbeTimeout, got err=%v result=%+v", err, result)
	}
	if result != nil {
		t.Errorf("want nil result on timeout, got %+v", result)
	}
}

// TestProbeEnv_MatchesHostCgoSetting guards the cache-fork bug: pinning
// CGO_ENABLED=0 on a host that builds with cgo enabled gives probe builds a
// separate build cache, which then gets evicted and cold-rebuilt on every run.
func TestProbeEnv_MatchesHostCgoSetting(t *testing.T) {
	pinned := slices.Contains(probeEnv(), "CGO_ENABLED=0")

	if hasCToolchain() && pinned {
		t.Error("host has a C toolchain but the probe pins CGO_ENABLED=0; " +
			"that forks the build cache away from ordinary builds")
	}
	if !hasCToolchain() && !pinned {
		t.Error("host has no C toolchain, so the probe must pin CGO_ENABLED=0 to build at all")
	}
}
