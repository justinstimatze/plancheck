# Feedback: gate is a degenerate loop on greenfield repos + cross-project peer noise

**Date:** 2026-05-26
**Reporter:** Claude (Opus 4.7), during a real planning session
**Context:** Bootstrapping a brand-new Python project (`caul`) — a git repo with **zero commits** and almost entirely new files. Used `check_plan` (MCP) + the `plancheck gate` ExitPlanMode hook as configured globally.

## TL;DR

On a greenfield repo (no git history, novel files), the gate **never reached a release condition through substantive convergence** — it released only after I ground out a fixed number of verification rounds on an unchanged plan. Along the way every structural suggestion was **from an unrelated project** (Go files `gozim/*.go`, `beads/*.go`) bleeding into a Python repo's check. Net effect: ~8 `check_plan` calls and 6 blocked `ExitPlanMode` attempts for a plan that had no real findings after round 2.

## What happened, concretely

The plan: 13 files (9 create, 4 modify), Python, in `/home/gas6amus/Documents/caul` (no commits yet).

1. **Round 1** — one genuine, useful finding: a file I listed under `filesToModify` (`.env.example`) didn't exist on disk → should be a create. *This is exactly the kind of catch the tool is for. Good.*
2. **Round 2** — fixed that; new genuine finding: `caul/loop.py` imports `caul/state.py` (which I was modifying) but wasn't in the plan. *Also a good, real catch (import-chain probe).*
3. **Rounds 3–8** — I addressed both, added a real design refinement (cribbing an LLM-call pattern from a sibling project). From here on, **every** check returned the same two things:
   - `"noveltyGuidance": "Almost entirely new. Structural prediction unavailable."`
   - A `ranked` / `checklist` list of **Go files from a completely different repo** — `gozim/iter.go`, `gozim/zim.go`, `beads/beads_cgo.go`, `gozim/search_analyzers.go`, etc. (all `score: 0.2`, `source: "peer-dir"`). This is a Python project; those files are not peers in any meaningful sense.
4. The **gate** (`plancheck gate` hook) blocked `ExitPlanMode` six times with a rotating set of messages:
   - `"BLOCKED: verify the plan (13 steps/files — 3 verification rounds remaining)"`
   - `"BLOCKED: subdivide — the plan needs more verification."`
   - `"BLOCKED: verify the remaining segments."`
   The "rounds remaining" counter appeared to **reset whenever I edited the plan** (even improving edits), so productive refinement *prolonged* the gate rather than satisfying it. It finally released after I ran `check_plan` several times on a **stable, unchanged** plan — i.e., the release condition was "N rounds elapsed," not "the plan converged."

## The two problems

### 1. No terminal condition on greenfield (the important one)

My global config says: *"Repeat until score >= 80 or the user explicitly says to proceed anyway. The hook gate will block ExitPlanMode if the last check_plan score is below the threshold."* But on a no-history repo, `check_plan` returns **no score at all** ("Structural prediction unavailable"). So:

- There is no score to compare to 80 → the score-based release path is unreachable.
- The fallback became a pure round-count, which **resets on plan edits** → the more I improved the plan, the longer the gate held.
- The only way out was to stop improving the plan and burn rounds on an identical payload. That inverts the intended incentive (it rewards *not* refining).

**Suggestions:**
- When `structural prediction unavailable` (no history), **don't fall back to a reset-on-edit round counter.** Either (a) compute a release from the *substantive* findings that ARE available (file-existence + import-chain probes both work fine without history — they're deterministic on disk/AST), or (b) require a fixed *small* number of rounds that does **not** reset on edits, or (c) emit an explicit "novel repo — N rounds required, edits won't reset" message so the agent doesn't fight it.
- Make the gate's release condition **legible in the block message**: "released when last 2 checks have zero non-advisory findings" or "3 more rounds regardless." Right now the messages ("subdivide", "verify remaining segments") read as substantive asks when they're actually just "elapse more rounds."
- Distinguish **advisory** suggestions (peer-dir adds, `score 0.2`) from **blocking** findings (missing file, broken import). Only blocking findings should gate. Eight rounds of `score: 0.2` advisories should never hold a gate.

### 2. Cross-project "peer-dir" contamination

Every `check_plan` for this Python repo recommended Go files from `gozim/` and `beads/` — a different project entirely. The `source: "peer-dir"` reason ("in peer package X under same feature root") suggests the peer-discovery is resolving against a **global index / wrong feature root**, not the `cwd` I passed (`/home/gas6amus/Documents/caul`).

**Suggestions:**
- Scope peer discovery to the `cwd` project root (and its language). A `.py`-only plan in repo A should never surface `.go` files from repo B.
- If there's a shared/global index, key it by repo identity (git remote, root path, or module path) and filter peers to the same repo.
- Language mismatch alone (plan is all Python; suggestion is Go) is a cheap, high-precision filter to drop these.

## What worked well (keep it)

- The **file-existence probe** (caught `.env.example` miscategorized as modify) and the **import-chain probe** (caught `loop.py → state.py`) were both genuinely useful and fired correctly *without* git history. These are the parts pulling weight on a greenfield repo — lean on them for the release condition.
- The forward/backward "trace from current state and from the goal, fix where they disagree" framing is a good planning discipline; it did make me nail down the live-vs-mock data-shape contract. The problem is purely that the gate couldn't *tell* I'd converged.

## Repro sketch

```
cd /a/brand-new/git/repo   # git init, zero commits
# plan that creates several new files in a new language
# call check_plan repeatedly with the SAME converged plan_json
# observe: no score, peer-dir suggestions from unrelated repos,
#          gate round-counter resets on any plan edit
```
