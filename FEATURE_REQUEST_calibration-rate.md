# Feature request: calibration-rate readout (and direction-judgment climb)

Source: Anthropic, "When AI builds itself" (institute/recursive-self-improvement,
May 2026). Origin: brainstorm session 2026-06-04 reading that article against the
project portfolio. Verbatim quotes below pulled from the article HTML (not via
LLM paraphrase), saved at `/tmp/rsi-article.txt` during the session; line refs
are into that file.

## Verbatim from the article

> "An area of human comparative advantage, for now, is research taste and
> judgment, including choosing which problems matter, which results to trust,
> and when an approach is a dead end." — *What might the future of work at
> Anthropic look like?* (lines 336-338)

> "Large performance gaps persist when it comes to Claude exercising judgement
> in choosing goals in both engineering and research." (line 117)

> "Humans play a substantially diminished role in their development, likely
> moving most of our effort towards oversight, validation, and verification of
> an expanding 'virtual lab' run by AI systems." — *Possible futures* (lines
> 477-480)

> "they would build the systems needed to verify that AI outputs can be trusted."
> — *Possible futures* (line 451)

> "Because we deliberately picked moments (n=129) where we know the human's
> choice had room for improvement, this isn't a like-for-like comparison between
> model and human judgement. [...] On this measure, our best model in November
> 2025 (Opus 4.5) beat the human choice 51% of the time; in April 2026 (Mythos
> Preview), this grew to 64%." (lines 297-304)

## What the 51% / 64% numbers actually mean

**Not** "model matched human direction-choice 64% of the time" — that
paraphrase is wrong. The correct reading: on cherry-picked sessions where the
human's choice was known to have room for improvement (n=129), the model picked
a better next step 64% of the time (up from 51% in Nov 2025). The human side
is selection-biased to be suboptimal. The number is a "beat-the-human-on-hard-
moments" rate, not a match rate.

Comparability to plancheck: the *form* (a verdict-vs-truth hit rate plotted over
time) is comparable; the *magnitude* is not. plancheck would measure verdict→
outcome on the full distribution of checks, not on a deliberately hard subset.
Don't claim "comparable to Anthropic's 64%" — that conflates two different
denominators.

## Three senses of "verification" in the article

1. **Outputs verification** (line 451): "verify that AI outputs can be trusted."
   plancheck-shaped.
2. **Diminished human role in the RSI future** (lines 477-480): "oversight,
   validation, and verification of an expanding 'virtual lab' run by AI
   systems." Also plancheck-shaped.
3. **Treaty-level pause verification** (lines 538-545): "verify that others
   globally have actually stopped or slowed." Not plancheck-shaped.

The honest framing for plancheck: this is verification tooling in senses #1 and
#2 — it does not address #3. Specifically, plancheck verifies plan *structure*,
which is one piece of #1/#2, not the whole of either.

## Why

The article names research judgment as the human comparative advantage and
flags "oversight, validation, and verification" as the human role in an AI-built
research lab. plancheck already produces verdicts (persona passes, check_plan
scores) and already has the substrate for ground truth (`record_outcome` /
`record_reflection`). It just doesn't close the loop between them.

Right now plancheck emits a score with no published hit-rate. That makes it "a
tool that emits a number" rather than "a tool with a known accuracy." Closing
the loop tells the user whether the persona passes earn their tokens.

## Feature 1 (primary): calibration-rate readout

Compute and surface the agreement between plancheck's pre-execution verdict and
the eventual recorded outcome.

- Inputs partially exist: `internal/history` already does the verdict→outcome
  join (`LoadHistory` returns `Outcomes map[id]string` and `Reflections
  map[id]ReflectionEntry`; `RecordOutcome` validates "ID not found in history"
  at `history.go:194-196`). 9/9 reflections in the current dataset empirically
  match check IDs.
- Output: per-score-band agreement rate, n, and Wilson confidence interval (do
  NOT print a point estimate for n<5 in band — small-n calibration is noise).
- Surface: a CLI subcommand (`plancheck calibration`) and, when n≥5 in band,
  appended to check output ("historical hit rate at this score band: N% (n=M,
  CI [a,b])").
- Bucket by score band so the user can SEE whether the 80-threshold is
  empirically well-placed — but **don't auto-retune the gate**. Observe-then-
  retune turns into Goodhart drift.

### Three enabling pieces that sit upstream

Aggregation math is trivial; these are not:

1. **Persist score on HistoryEntry** (currently `comodMisses, id, objective,
   projectType, suggestedModify, timestamp` at `history.go:45-52` — no score).
   Add `Score`, `Verdict`, `MissingFiles`. Backfill = 0 on legacy rows.
2. **Stop culling rows that have a reflection** (currently `maxEntries=50`
   trims oldest at `history.go:89-100`). Either preserve reflected rows through
   cull or raise the cap.
3. **Reflection coverage is the gating constraint, not code.** Current coverage:
   9 / 497 ≈ 1.8% across all projects. Filtering benchmark datasets (which
   never reflect): ~5.5% on human projects. The instruction lives at `cmd/
   setup.go:527-539` and in user CLAUDE.md, but it asks Claude to remember to
   call `record_reflection` across a context boundary it doesn't reliably
   traverse. Code without supply is a number computed on n=4.

## Feature 1.5 (the actual gating work): supply-side instrumentation

Before Feature 1's readout has data to operate on, reflection coverage on human
projects needs to climb. The failure mode is structural:

- "Execution complete" has no clean trigger event for Claude — it's detected by
  the user's *next* prompt, by which time the `historyId` has dissolved.
- `record_reflection` requires `passes ≥ 2` + four counter fields
  (`history.go:228`, 245-249) — friction is too high for a deferred action.
- No external reminder.

**Highest-leverage fix:** PostToolUse:Bash hook → detect `git commit`
invocations (PostToolUse fires regardless of bash exit status; rerun-attempts
after a pre-commit rejection re-prompt, which is fine) → read project's
`last-check-id` → if no matching outcome in `history.jsonl`, emit a
system-reminder to Claude prompting outcome capture. Pair the deferred
action with a natural completion event; no in-context memory required. Note
that the hook **prompts** Claude — it does not auto-record. If real-world
compliance lands below ~30%, the fallback is auto-recording "committed" as
a default outcome the agent can upgrade to clean/rework/failed; this trades
information per row for guaranteed rows.

Companion: `plancheck reflect <outcome>` CLI command — single-keystroke surface
that reads `last-check-id` and calls `RecordOutcome`. Lighter than
`record_reflection` (4 fields, no pass-count gate). Calibration math should
prefer `record_outcome` as the substrate; `record_reflection` is richer per-row
but rarer.

**Also**: the calibration readout must filter benchmark project paths (skip
`~/.plancheck/datasets/*`) or annotate `automated bool` on HistoryEntry from
CLI entry points — otherwise 50:0 benchmark dilution swamps every human signal.

## Feature 2 (deferred): climb from plan-structure to direction-judgment

Today plancheck checks structural soundness (orphans, cascade risk, missing
callers). The article's gap is one level up: *is this the right problem, is
this a dead end* — not *is this plan well-formed*.

- Add a "direction" verdict alongside the existing "structure" verdict: does
  this plan pursue a goal worth pursuing, or is it a likely dead end?
- The reflection loop is the substrate — direction verdicts get their own
  calibration track.

**Defer until** Feature 1 has ≥30 outcomes recorded AND structural calibration
is interpretable. Additional reason to defer: the article's direction-match
trend is **51% → 64% in five months** on hard moments (Opus 4.5 Nov 2025 →
Mythos April 2026). If the model itself is climbing 13pp/5mo on its own
direction-choice, plancheck adding a separate direction layer is racing a
moving target — unlikely to pay rent on its tokens. The structural layer
(Feature 1) is a different question that doesn't compete with the model's
improving judgment.

## Risks / non-goals

- **Don't auto-tune the gate threshold from calibration.** Observe → tune →
  observation set shifts → threshold drifts. Goodhart. Publish; let the user
  retune deliberately.
- **Don't ship a single-number readout.** "76% hit rate" hides everything.
  Per-band + n + CI is the minimum honest surface.
- **Don't oversell against the article.** plancheck addresses senses #1 and #2
  of "verification" (output trust, lab oversight). It does not address sense #3
  (pause-detection). Numbers are not magnitude-comparable to the 64% (different
  denominators).

## Related

Sibling move in `../hindcast` — generalize its predicted-vs-actual machinery
from wall-clock to outcome/success calibration. Together the two become one
calibration family rather than two unrelated tools.
