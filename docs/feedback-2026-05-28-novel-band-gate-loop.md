# plancheck feedback — gate loop in the 0.5 ≤ novelty < 0.7 band

**Date:** 2026-05-28
**Project under check:** adding a brand-new package (`cmd/gozimmcp`) to an existing repo
**Severity:** medium — not a wrong gate, but a loop that wastes ~6 round-trips
and reads as "stuck" to the agent.

## Symptom

A well-formed, fully-verified plan could not pass `gate` for ~6 ExitPlanMode
attempts. The block message was identical every time:

```
BLOCKED: verify the plan (9 steps/files — 2 verification rounds remaining).
```

"2 remaining" never decremented, despite running `check_plan` repeatedly (each
returning `"Plan looks structurally complete"`, 0 missing files), running the
`check-plan` skill (full forward/backward trace, fixed 2 real findings), and
running `simulate_plan` (zero blast radius confirmed). From the agent's side it
looked like a hard wall with no visible lever.

## Root cause

Two correct-in-isolation rules combined badly in the **0.5 ≤ novelty < 0.7** band:

1. **Novelty bump:** the non-greenfield path did `required++` when
   `Novelty.Score > 0.5`. With complexity 9 → `requiredChecks` = 2, bumped to
   **3**.
2. **Plan-hash reset:** the non-greenfield path reset `ChecksSeen` whenever
   `PlanHash` (objective + file set) changed since the last check.

The greenfield guard (`Novelty.Score >= 0.7`) already fixed BOTH for high-novelty
plans: it capped `required` at 2 *and* skipped the reset. But a plan that scores
**novelty 0.65** — novel new-code work, yet just under the 0.7 cutoff — landed on
the non-greenfield path and got the *worst of both*:

- `required` raised to 3 (novelty bump), **and**
- `ChecksSeen` reset whenever the file set changed — which is exactly what
  happens when the agent corrects a file path during convergence (this run fixed
  a wrong package path). Every such edit reset the count → stuck at 1/3 forever.

This is the same "rewards NOT refining" failure the 2026-05-26 greenfield doc
warned about — it just wasn't caught because the 0.7 threshold is a hard cliff,
and the bump (more rounds when novel) directly contradicts the greenfield insight
(more rounds add no signal when novel). The logic flipped sign at 0.7.

## Fix (shipped 2026-05-28)

Unify the threshold. The novel regime (no reset, no bump, cap at 2) now kicks in
at `Novelty.Score >= 0.5` — the same point the bump used to fire and the point
where epistemic uncertainty becomes "wide". The standalone novelty bump is
removed entirely: novelty never *increases* the round count, it only caps it.
`>= 0.7` is retained only to select stronger block-message wording
("structural prediction unavailable") vs the softer 0.5–0.7 wording
("the reference graph only partially covers this").

Adjacent items addressed in the same change:

- **Block message now names the lever.** The non-novel `phaseMessage` says
  "Changing the objective or file set resets this count; wording/step edits
  don't. If the plan is final, re-run check_plan as-is to confirm." The novel
  message already says "editing the plan will NOT reset this count."
- **`acknowledgedComod` made discoverable.** The co-mod block message now spells
  out that it's an `acknowledgedComod` map (file → reason) in the plan JSON —
  it was always a valid `ExecutionPlan` field, just under-documented.

## Notes on the reporter's other observations

- **`PlanHash` is already structural** — `ExecutionPlan.Hash()` covers only
  objective + sorted file set, not step prose or `filesToRead`. So wording/step
  edits never reset the count; only objective or file-set changes do. The
  reset-on-structural-change is intended for the non-novel band (a changed file
  set warrants a fresh count) and is now suppressed for novel work.
- **Hub co-mod findings already excluded** from both the gating set and the
  "Findings from last check" summary in current source. The hub lines seen in
  the dogfood were from a stale installed binary; `go install` clears them.

## What worked well

- The `check-plan` skill's bidirectional trace caught two genuine plan bugs
  (wrong package path, an understated on-demand-index-build landmine).
- `simulate_plan` cleanly confirmed zero blast radius for the additive change.
- Forecast (`70% clean, low risk, based on 175 similar plans`) was a useful,
  calibrated signal.
