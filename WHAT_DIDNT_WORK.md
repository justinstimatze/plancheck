# What Didn't Work

Experiments that were tried, measured, and reverted or abandoned. Each entry includes what we tried, why we thought it would work, what actually happened, and why. This is the most valuable document in the repo — it prevents us from repeating mistakes.

_Numbers below are from specific experiment snapshots during development and may differ from the current combined results in README.md._

## Signal Generation

### Follow-up LLM call after spike (2026-04-03)
**Tried:** After the spike produces implementation code, one cheap LLM call asking "what did the engineer miss?" with the spike's file summaries as context. Tested with both Haiku and Sonnet.

**Expected:** The LLM could reason about dependencies the spike missed, catching fringe files.

**Result:** Haiku dropped avg task precision from 30% to 23%. Sonnet was 26%. Both worse than no follow-up.

**Why it failed:** LLM reasoning about "what files might be affected" without implementation is speculation. The spike's precision comes from actually writing code (implementation is discovery). Post-hoc reasoning is just guessing.

**Status:** Code removed.

### Test-driven backward planning (2026-03-28)
**Tried:** Parse test patches to predict production files backward — extract function calls, type references, import paths from test diffs.

**Expected:** Tests call production code, so test changes point to production files that must change.

**Result:** Way too noisy. Tests reference many production functions that DON'T need changes (called but not modified).

**Why it failed:** Tests exercise code they depend on, not just code that changed. Every imported function shows up as a signal.

### Cross-repo pattern learning (2026-03-29)
**Tried:** `LearnPatterns` across 21 repos — "when a function with N callers gets a signature change, X% of callers update."

**Result:** Implemented but never benchmarked. Superseded by build-check (compiler-verified) which is strictly more precise.

**Status:** Code removed.

## Spike Architecture

### Tools during implementation phase (2026-04-03)
**Tried:** Give the agent read/find/impact tools during Phase 2 (implementation), so it can look up files mid-code.

**Expected:** Agent discovers files it needs while writing, reads them, implements changes. More accurate implementation.

**Result:** Recall dropped from +14.6pp to +4.4pp. Only 8/50 improved (vs 19/50 baseline).

**Why it failed:** Tools give the agent an escape hatch from writing code to analyzing. It stops implementing and starts exploring. The "STOP ANALYZING. Write the code." constraint is load-bearing — anything that lets the agent not write code will cause it to not write code.

**Status:** Reverted.

### Strategic pre-loading of plan files + imports (2026-04-04)
**Tried:** Pre-read plan files and their local imports (up to 2000 lines), inject into the initial message before exploration.

**Expected:** Agent has full dependency context from the start, wastes fewer exploration turns on read() calls.

**Result:** Best F1 ever (-0.037) and best B-precision (0.683), but only 7/50 improved (vs 19/50). Recall dropped from +14.6pp to +5.2pp.

**Why it failed:** Agent already "knows" the codebase and writes less speculatively. It's more precise but discovers fewer files because it doesn't need to explore. The exploration phase IS the discovery mechanism — short-circuiting it hurts recall.

**Status:** Reverted.

### Spike unleashed: 64K tokens, no intersection scoring (2026-03-24)
**Tried:** Give the spike maximum context (64K tokens) and trust its output without structural filtering.

**Expected:** More context = better implementation = better file discovery.

**Result:** Massive over-inclusion. Precision collapsed.

**Why it failed:** More tokens = more speculation. The spike touches many files it shouldn't. Intersection scoring (spike + structural agreement) is the precision filter.

**Status:** Reverted to intersection scoring.

### Including ALL sibling subcommand directories (2026-03-25)
**Tried:** Auto-include every sibling directory in the spike's code neighborhood.

**Expected:** Catch the 60% of gold files that are sibling subcommands.

**Result:** Noise explosion. Most siblings are irrelevant.

**Why it failed:** Siblings are genuinely unpredictable — the task doesn't say which siblings need changes.

**Status:** Reverted.

## Scoring & Filtering

### Blast radius as ranking signal (2026-04-02)
**Tried:** Feed compiler-verified blast radius files (from `RunBlastRadius`) into ranking at score 0.4 with "verified-blast-radius" tag.

**Expected:** Compiler-verified dependencies should be high-confidence.

**Result:** Precision dropped from 42% to 36%. F1 -0.038 → -0.052.

**Why it failed:** Blast radius returns EVERY file that depends on plan files' exported APIs — dozens of files. These create false intersections with spike files in the confidence gate. The blast radius is the universe of possibly-affected files, not a prediction.

**Fix:** Kept as context-only in preview risks (neutral impact).

**Status:** Kept as context-only.

### Iterative build-check (depth-2 cascading) (2026-04-02)
**Tried:** After round 1 build-check finds obligation files, probe those files too (add dummy struct fields/sig changes) and re-build to find depth-2 dependencies.

**Expected:** Depth-2 obligations catch transitive breakage.

**Result:** Precision dropped further (34%). Depth-2 obligations at 0.70 score create noise in the intersection tier.

**Why it failed:** Files 2 hops from the spike are much less likely to actually need modification for any given task. Direct breakage (depth-1) at 0.95 is the sweet spot.

**Status:** Never committed.

### Enriched spike reasons with graph call sites (2026-04-02)
**Tried:** Replace generic reasons ("agent spike: turn 1, 35 lines") with specific call-site data ("CreateRun() in create.go calls SubmitPR() — must update if signature changes").

**Expected:** More specific reasons help the LLM make better inclusion decisions.

**Result:** No improvement, possibly slight regression.

**Why it failed:** The B-condition LLM decides based on the overall preview context, not individual file reasons. More verbose reasons may even confuse it.

**Status:** Never committed.

### Condensed code diffs in preview (2026-03-31)
**Tried:** Show actual code diffs (struct field additions, new functions, signature changes) instead of summaries in the implementation preview.

**Expected:** More detail helps the LLM understand what changed.

**Result:** Avg task precision dropped from 42% to 28-32%.

**Why it failed:** More detail triggers over-inclusion. LLMs decide better from abstractions ("adds Draft field to CreateOptions, 12 usage sites") than from code diffs. Summaries beat diffs.

**Status:** Reverted. Dead code removed in v0.1.0.

### Type-level diff score boost (2026-03-31)
**Tried:** Boost scores for files with type-level changes (struct/signature) and penalize non-type changes.

**Expected:** Type-level changes are more likely to require caller updates.

**Result:** Gold files found dropped from 42 to 33. Made it worse.

**Why it failed:** Penalizing non-type changes removed valid body-change discoveries.

**Status:** Reverted.

### LIKELY max 3 (instead of max 2) (2026-03-31)
**Tried:** Allow 3 intersection files in the LIKELY section instead of 2.

**Expected:** One more file suggestion = more recall.

**Result:** F1 -0.078 vs -0.038 with max 2. The extra file added more noise than signal.

**Why it failed:** Fewer suggestions = higher F1. The third intersection file is usually marginal.

**Status:** Kept at max 2.

### Callee signal at 0.6 weight (2026-03-20)
**Tried:** Weight callees (files that plan files call) at 0.6, same as callers.

**Expected:** Dependencies should be as important as dependents.

**Result:** Too much noise. Callees rarely need changes when you modify a caller.

**Why it failed:** If A calls B and you modify A, B usually doesn't need to change. Reduced to 0.3.

**Status:** Reduced to 0.3 weight.

### Suggest+LLM revision mode (2026-04-06)
**Tried:** Run suggest() (free, instant structural signals), then give its output to the LLM as revision context instead of the full spike. "Structural analysis found: MUST CHANGE finder.go (compiler: broken call). Revise your plan."

**Expected:** LLM would adopt structural signals at fraction of spike cost (~$0.01 vs $0.10). Sweet spot between suggest-only ($0, +7.9pp) and full spike ($0.10, +17.7pp).

**Result:** +2.9pp recall, 6/50 improved, -5.6pp F1. WORSE than suggest-only (+7.9pp). The LLM revision step hurts.

**Why it failed:** The LLM is conservative — it ignores structural signals because it doesn't understand WHY a file needs to change. "These files are structurally related" isn't convincing. The spike works because it IMPLEMENTS the change and discovers through data flow. Telling the LLM about structural relationships ≠ showing it an implementation. There is no cheap middle ground between "implement" and "don't implement."

**Status:** Mode kept for reference but not the recommended path.

### Recursive spike (2026-04-05)
**Tried:** After the first spike discovers 1-3 files, run a second lightweight spike (2 explore + 1 implement) with those files added to the plan. Discovers depth-2 dependencies through implementation.

**Expected:** Second spike explores from expanded footprint, finds files the first spike missed.

**Result:** Too slow to benchmark — doubled wall time per task (~5-8 min vs ~2-3 min). 8/50 tasks completed before killing.

**Why it failed:** Most of the extra cost is wasted when the second spike doesn't find new files. The second spike's context is huge (all of the first spike's conversation) which makes each API call slower. Implemented but disabled by default (PLANCHECK_RECURSIVE=1 to enable).

**Status:** Implemented but disabled by default (PLANCHECK_RECURSIVE=1).

### Confidence gate on suggest-only mode (2026-04-06)
**Tried:** Apply the spike's confidence gate (require 2+ signal source intersection) to suggest-only mode. Single-source suggestions filtered as noise.

**Expected:** Improve F1 by filtering noisy single-source suggestions.

**Result:** F1 improved slightly (-6.8pp → -5.4pp) but recall halved (+7.9pp → +3.1pp, 12→6 improved). The gate killed real finds that only had one signal source.

**Why it failed:** The spike's confidence gate works because the spike IS a signal source — "spike + structural" is a 2-source intersection. In suggest-only mode, there's no spike, so most files only have one structural source. The gate filters too aggressively. Kept the gate in the product surfaces (review, suggest MCP) where precision matters, removed from benchmark suggest-only mode.

**Status:** Gate kept in product code, benchmark uses ungated suggest-only.

### "Focus exploration BEYOND plan files" prompt (2026-04-05)
**Tried:** Changed system prompt to include tool strategy ("START HERE with impact()") and user message to "focus exploration BEYOND plan files — callers, constructors, sibling commands."

**Expected:** Agent would use code()/impact() more and explore outward from plan files, discovering more non-plan gold files.

**Result:** nats-server collapsed: +2.5pp recall (was +7.5pp), 0% signal precision (0/6 suggestions were gold). Only 4/37 improved (was 9).

**Why it failed:** "Read the plan files first" is load-bearing. The agent needs to read plan files to understand context before exploring outward. Telling it to skip plan files broke its understanding of the task. "START HERE with impact()" pushed it toward analysis instead of the read→explore→implement flow.

**Status:** Reverted.

### Blanket build-check on explored files (2026-04-05)
**Tried:** For files the agent explored but didn't implement, probe ALL exported symbols with `probeExportedSymbols` via `go build -overlay` to find compiler-verified callers.

**Expected:** Turn 0.35-0.50 exploration signals into 0.95 compiler-verified obligations.

**Result:** nats-server: recall dropped from +7.5pp to +4.6pp, signal precision from 54% to 38%. cli/cli: slightly helped (+15.2pp recall, -0.3pp F1).

**Why it failed:** Same anti-pattern as blast radius ranking — probing ALL exports returns the full dependency cone (dozens of files in flat packages). Targeted probing of specific definitions (only what the agent looked up via code()) is the fix.

**Status:** Reverted, replaced with targeted build-check.

### Partial-file implementation prompt (2026-04-05)
**Tried:** Changed spike impl prompt from "write the FULL modified file" to "write only the modified functions/types" to handle large files (nats-server: 5K-13K lines).

**Expected:** Agent writes partial blocks for large files → more file discovery on big codebases.

**Result:** nats-server improved slightly (+3.9pp vs +3.1pp). cli/cli REGRESSED: F1 dropped from -5pp to -10pp (50 tasks). Signal precision dropped from 38% to 30%.

**Why it failed:** Partial blocks always look different from originals (comparing 20 lines against 150-line file). `summarizeFileChange` falls through to generic "modifies implementation" for every block. Preview becomes noisier, LLM over-includes. The "full file" prompt is calibrated for cli/cli's 150-line files.

**Fix:** Reverted prompt. Instead captured exploration tool calls as a separate signal source.

**Status:** Reverted.

## What DID Work (After Previous Failures)

### Exploration signal extraction (2026-04-05)
**Tried:** Track which non-plan files the agent actively investigated via read()/code()/find() during exploration. Score these lower than implementation blocks (0.35-0.50 vs 0.85) but let them intersect with structural signals in the confidence gate.

**Why it works:** On large-file codebases, the agent explores well (navigates to accounts.go:4362, jwt.go:100) but can't write full 6K-line implementations. The exploration tool calls ARE discovery — the agent deliberately chose to investigate those files. On small-file codebases (cli/cli), the agent also writes full implementations, so exploration signals are redundant and harmless.

**Result:** nats-server: +7.5pp recall (was +3.1pp), F1 +0.7pp, 9/37 improved. cli/cli: +11.1pp recall, F1 +0.0pp, 14/50 improved. Signal precision 49-54%.

### code() tool via go/ast (2026-04-05)
**Tried:** New agent tool that reads a specific definition's source code by name (using graph + go/ast). For a 6800-line file, `code("Connz")` returns just the 8-line struct definition, not 300 lines from the top.

**Why it works:** The agent can navigate directly to the relevant code in massive files. Combined with read(path, start_line) and total-line-count in truncation messages, the agent makes surgical exploration decisions.

**Status:** Now uses defn's StartLine/EndLine when available, falls back to go/ast.

## Meta-Lessons

1. **Implementation > reasoning**: The spike works because writing code is discovery. Every attempt to replace implementation with reasoning (follow-up calls, file listing, analysis) produces weaker signals.

2. **Constraints enable discovery**: "STOP ANALYZING. Write the code." and "No tools during implementation" are load-bearing. Removing constraints doesn't make the agent better — it makes it lazier.

3. **Less is more for suggestions**: Suggesting 1-2 confident files beats suggesting 5 mixed files. The LLM over-includes from generic file lists.

4. **Summaries beat details**: Abstractions ("adds Draft field, 12 usage sites") > code diffs > file lists. The LLM reasons at the right level of abstraction.

5. **Compiler > LLM for obligations**: `go build -overlay` at depth-1 is near-100% precision. Every LLM-based approach (follow-up, enriched reasons, test backward) is noisier.

6. **More context can hurt**: Pre-loading too much context makes the spike conservative. The exploration phase IS the discovery mechanism.

7. **Exploration IS discovery for large files**: When files are too large to implement, the agent's tool calls during exploration (read, code, find on non-plan files) are themselves a discovery signal. Capturing these at lower confidence (0.35-0.50) fills the gap without degrading small-file performance.

8. **One prompt doesn't fit all file sizes**: "Write the full file" works for 150-line files, produces nothing for 6000-line files. "Write partial" works for large files, introduces noise on small ones. The fix isn't an adaptive prompt — it's capturing signal from both exploration and implementation phases.

9. **There is no cheap substitute for implementation**: suggest+LLM (+2.9pp) is worse than suggest-only (+7.9pp) which is worse than the full spike (+17.7pp). The LLM filters out structural signals it doesn't understand. Direct file addition beats LLM-mediated adoption for structural signals. The spike beats both because implementation IS understanding.

10. **Confidence gates trade recall for precision**: Requiring 2+ signal sources improves precision but kills single-source true positives. The gate works for the spike (spike IS a signal source, so "spike + structural" is 2 sources). Without the spike, most files only have one source — the gate is too strict.
