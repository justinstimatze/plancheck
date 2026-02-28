# Influences & Citations

plancheck is a prediction market for code changes. Each file in a plan
has a probability of needing to change, computed by aggregating three
independent signals — structural (reference graph), statistical (git
co-modification), and semantic (the LLM itself) — weighted by their
historical accuracy for this project.

The tool's job: price each file's probability, present the portfolio
of bets to the model, and let forced iteration narrow the gap between
predicted and actual changes. Over time, calibration data adjusts the
signal weights per-project, making predictions more accurate.

This framing draws from forecasting, betting theory, cognitive science,
legal theory, and the current AI coding landscape. Each analogy below is
labeled **tight** (structural match) or **loose** (useful intuition,
imprecise mapping).

All dependencies are permissively licensed (MIT or Apache-2.0).

## Forecasting & Prediction

### Noise reduction via forced iteration (tight)

Philip Tetlock's BIN decomposition (*Superforecasting*, 2015) found that
**50% of accuracy improvement** in the Good Judgment Project came from
noise reduction — not better information, not less bias, but more
consistency. Teams were 23% more accurate than individuals.
Superforecasters predicted 300 days out as accurately as ordinary
forecasters predicted 100 days out.

plancheck's gate mechanism is a noise reducer. It forces multiple
verification rounds, preventing the model from locking in its first-draft
plan. The complexity-proportional scaling (2 rounds for simple plans,
up to 4 for complex ones) matches the finding that harder questions
benefit more from deliberation.

### Granular decomposition (tight)

Superforecasters decompose big questions into smaller answerable
sub-questions, estimate each, and combine. plancheck's bidirectional
verification does the same: subdivide the plan at midpoints, trace
forward and backward from each, verify handoffs. Each subdivision creates
a smaller, more verifiable prediction.

### Resulting vs decision quality (tight)

Annie Duke (*Thinking in Bets*, 2018) distinguishes outcome quality
(did it work?) from decision quality (was the process good given
available information?). plancheck's `record_reflection` separates these:
it tracks both the outcome (clean/rework/failed) and the process metrics
(number of passes, probe findings vs persona findings, what was missed).
A plan that worked despite being poorly verified was lucky, not good.

### Backcasting and premortems (tight)

Duke's backcasting (imagine success, trace backward) maps directly to
plancheck's backward trace. Gary Klein's premortem technique
("Prospective hindsight," HBR 2007) — imagining failure and identifying
causes — maps to the Skeptic persona. Klein found prospective hindsight
increases ability to identify failure reasons by 30%.

## Betting Theory

### Combined models: structural analysis + crowd estimate (tight at pattern level)

Bill Benter's horse racing system (1994) made ~$1B by combining a
fundamental model (~130 variables of horse/jockey/track data) with
public betting odds. The public odds already encode most information.
The fundamental model finds where the odds are slightly wrong.

plancheck works the same way, now with three signals:

1. **The LLM's plan** is the public odds — it already encodes most of
   the right answer, including semantic understanding of features,
   design patterns, and domain knowledge that no structural tool can
   replicate.
2. **The reference graph** is a fundamental model — it finds where the
   plan diverges from structural reality (calls, type references,
   interface dispatch).
3. **Git co-modification** is a second fundamental model — it captures
   historical co-change patterns the LLM can't generate.

The three signals are combined with **learned weights**
(`internal/predict/`). Weights start at default (structural 0.50,
comod 0.35, semantic 0.15) and adjust per-project via calibration —
projects where the graph is highly predictive weight it higher,
projects where the model's semantic suggestions are accurate weight
those higher.

Validated empirically: the two-signal combined model (graph + comod)
predicts 76% of failing tests across 643 tasks on 5 Go repos, beating
either signal alone (graph 59%, comod 46%). Statistical significance
confirmed (p<0.05, hold-out stable). The three-signal model
(+ semantic) is built but not yet validated at scale.

### Kelly criterion: proportional bet sizing (partially tight)

Kelly criterion says bet proportional to your edge. In plancheck, the
gate scales verification rounds to plan complexity (more rounds for
bigger plans), and blocks harder on high-confidence comod gaps (>75%
co-change frequency) than marginal ones (40-75%). The proportionality
maps well. The "bankroll" metaphor (developer time as bankroll) is loose.

## Software Estimation & Uncertainty

### Reference class forecasting and uniqueness bias (tight)

Bent Flyvbjerg (*How Big Things Get Done*, 2023; Flyvbjerg & Budzier,
2011) found that planners suffer from "uniqueness bias" — believing
their task is unprecedented when it rarely is at the right abstraction
level. An "unprecedented microservices migration" is still a migration.
plancheck's genre detection and cross-project analogies implement
this: search for the right abstraction level across defn-indexed repos.

However, Flyvbjerg found 1 in 6 IT projects are genuine black swans
(200% cost overrun) that RCF cannot predict. Our novelty score's
"exploratory" tier (score > 0.7) is the honest admission of this.

### Cone of uncertainty (tight)

Steve McConnell (*Software Estimation*, 2006) showed estimates can be
off by 4x at concept stage and 1.25x at detailed design. Critically:
**the cone does not narrow itself** — it narrows only through deliberate
decisions that remove variability. plancheck's check_plan IS a cone-
narrowing decision: each probe (file existence, comod, refgraph) removes
a specific source of uncertainty. The maturity axis tracks where on the
cone a project sits.

### Epistemic vs aleatory uncertainty (tight)

Glen Alleman (2017-2024) provides the operational test: if you can
reduce uncertainty by reading a file, asking a question, or running an
experiment, it's epistemic (invest in information). If variance persists
regardless, it's aleatory (add margin). plancheck's probes reduce
epistemic uncertainty (file existence, comod patterns, blast radius).
The MC forecast models aleatory uncertainty (inherent task duration
variance). The tool should explicitly label which is which.

### Analogical estimation and COCOMO PREC (tight)

Shepperd & Schofield (1997) showed analogy-based estimation performs
as well as regression with only ~10 historical projects. Our 18
defn-indexed repos exceed this threshold. COCOMO II's PREC
(Precedentedness) scale factor is literally a 1-6 knob for "how novel
is this" — our novelty score (0.0-1.0) is the same idea with continuous
gradation and automatic detection via go.mod import analysis.

28-year retrospective (Shepperd, 2025): the method's greatest strength
is explanatory power — you can show which past projects were used and
why. Our cross-project analogies do this: "echo's middleware calls
Allow, Context, Config."

### Spikes as expected value of information (partially tight)

Decision theory's EVPI (Expected Value of Perfect Information) frames
spike decisions: if the spike cost < expected savings from better
estimation, do the spike. plancheck's simulation IS a cheap spike —
structural exploration without implementation cost. For high-novelty
plans where simulation can't help (new packages with no graph),
recommending an actual implementation spike is the honest answer.

### Bayesian updating during execution (built, not yet validated)

Kim & Reinschmidt (2010) applied Kalman Filter Forecasting to earned
value data. After 3-4 increments, predictions reach +/-20% accuracy.
plancheck's MC forecast with the trace indexer implements this: start
with wide priors from seed data, tighten as the project accumulates
check_plan → validate_execution cycles. Not yet validated because we
need longitudinal data from real usage.

### Planning fallacy (motivates the entire project)

Kahneman & Tversky's inside/outside view: planners build optimistic
scenarios from task specifics (inside view) and miss unexpected
obstacles. The outside view asks "how long did similar tasks actually
take?" plancheck forces the outside view by surfacing cross-project
base rates, historical calibration data, and structural evidence that
contradicts the model's optimistic plan.

## Cognitive Science

### Desirable difficulty (tight)

Robert Bjork's desirable difficulty research shows that introducing
certain difficulties during learning — spacing, interleaving, retrieval
practice — improves long-term retention and transfer. Forced iteration
in plan verification is a desirable difficulty: it slows down the
automatic "plan once, execute immediately" pattern and forces the model
to re-engage with the plan from different angles.

This is a tighter fit than predictive processing for explaining *why*
iteration helps in this domain.

### Generate, predict, compare, update (loose)

Karl Friston's Free Energy Principle and Andy Clark's predictive
processing (*Surfing Uncertainty*) describe a general pattern: generate
top-down predictions, compare with bottom-up signals, update based on
prediction error. plancheck's forward trace is the prediction, the
backward trace is the constraint signal, and the comparison is the
prediction error.

Caveat: this pattern is so general it describes every feedback system
from thermostats to gradient descent. The neuroscience mechanism
(precision-weighted prediction errors, hierarchical cortical processing)
doesn't map to plancheck's actual mechanism (complexity-proportional
gate rounds). We use the abstract pattern, not the neural mechanism.

### Premortem technique (tight)

Gary Klein's premortem ("prospective hindsight," HBR 2007) increases
failure identification by 30%. plancheck's backward trace from the goal
state is a structured premortem — it asks "what must be true for this
to have succeeded?" and checks whether the current plan provides it.

## Legal Theory — Optimal Plan Specificity

### Fuller's eight desiderata (tight mapping to plan quality)

Lon Fuller's *The Morality of Law* (1964) defines eight conditions for
a functioning legal system. Several map directly to plan quality:

1. **Non-contradiction**: Steps must not conflict. Bidirectional
   verification catches contradiction — if the forward trace says
   "file A handles auth" but the backward trace says "file B handles
   auth," that's a contradiction.
2. **Possibility**: Steps must be implementable. Missing file checks
   verify that files the plan references actually exist.
3. **Clarity**: Steps must be specific enough to execute. Under-specified
   steps can't be verified forward or backward.
4. **Constancy**: The plan shouldn't change capriciously between
   verification rounds (convergence, not oscillation).
5. **Congruence**: The executed code should match the plan.
   `validate_execution` compares git diff against the original plan.

### Rules vs standards (tight)

Louis Kaplow ("Rules Versus Standards: An Economic Analysis," 1992) shows
that rules are costly to create but cheap to apply; standards are cheap
to create but costly to apply. Plan steps have the same tradeoff:

- **Over-specified plans** (rules): "In auth.go line 42, add
  `if err != nil { return err }`." Brittle. Any deviation requires
  re-planning. Creates "loopholes" where the model follows the letter
  but not the spirit.
- **Under-specified plans** (standards): "Add authentication."
  Unfalsifiable. The model fills in gaps with its own biases.
- **Optimal**: Plans should be specific enough to verify (each step has
  testable postconditions) but flexible enough to accommodate runtime
  judgment. Guided by precedent — project patterns in `knowledge.md`.

## Code Change Planning — State of the Art (2026)

### The landscape gap

Every major AI coding tool now has a "plan mode" — Cursor (Oct 2025),
Copilot (Nov 2025), Claude Code, Kiro (AWS), Devin, Windsurf, Augment.
They all do the same thing: generate a markdown plan, let the user
review it, then execute. One shot. No verification. No iteration.

**No tool iterates on plans before execution.** No tool verifies plans
against code structure. No tool learns from whether plans succeeded.

### Related systems

- **CodePlan** (Bairi et al., FSE 2024, arxiv 2309.12499) — The most
  rigorous formalization: repository-level coding as an adaptive plan
  graph where edits create new obligations. Key insight: plans must be
  adaptive because edits trigger new dependencies unknown at planning
  time. Evaluated on package migration (C#) and temporal edits (Python).

- **SWE-Search** (Antoniades et al., ICLR 2025, arxiv 2410.20285) —
  Monte Carlo Tree Search over search/edit/plan states. Value Agent
  provides feedback, Discriminator Agent enables debate. 23% relative
  improvement on SWE-bench. The most sophisticated planning algorithm
  applied to coding tasks, but operates during execution (action-level),
  not before execution (plan-level).

- **GitHub Copilot Workspace** (tech preview Apr 2024, sunset May 2025)
  — Four-phase pipeline: Task → Spec → Plan → Implementation. The most
  explicit plan-review architecture any major vendor shipped. Sunset in
  favor of autonomous agents (Copilot Coding Agent). The retirement is
  notable — GitHub moved toward autonomy over steerability.

- **GitHub Spec Kit** (open source, Sep 2025) — Spec-driven development
  as four gated phases: Specify → Plan → Tasks → Implement. Works across
  agents. The closest thing to a standardized plan-and-verify protocol,
  but standardizes the process, not the plan representation.

- **Kiro** (AWS, mid-2025) — Requirements → Design → Tasks, each
  generating a markdown artifact. The most structured commercial IDE flow.

- **Verdent AI** — Multi-agent platform with an explicit **verifier
  subagent** that checks outputs before merge. One of the few tools with
  automated verification in the loop.

### Plan verification research

- **Plan Verification for Embodied Agents** (arxiv 2509.02761, Sep 2025)
  — Iterative Judge-Planner framework. 62% converge at iter 1, 89% at
  iter 2, 96.5% at iter 3. Hard cap 5 rounds. Directly validates forced
  iteration.

- **Bridging LLM Planning and Formal Methods** (arxiv 2510.03469, 2025)
  — Converts plans into Kripke structures + LTL for model checking.
  GPT-5 achieves 96.3% F1 on PlanBench.

- **"LLMs Can Plan Only If We Tell Them"** (Sel et al., ICLR 2025,
  arxiv 2501.13545) — AoT+ achieves SOTA by structuring the planning
  prompt to tell the LLM how to plan. Planning performance depends on
  prompt structure, not just model capability.

## Bidirectional Reasoning

### Forward passes are greedy policies (tight)

- **"Why Reasoning Fails to Plan"** (arxiv 2601.22311, Jan 2026) —
  Forward-only reasoning is a greedy policy. Planning requires backward
  value propagation. LLaMA-8B with bidirectional Flare outperforms
  GPT-4o with step-by-step.

### Forward and backward catch categorically different errors (tight)

- **FOBAR** (Jiang et al., ACL 2024 Findings, arxiv 2308.07758) —
  Forward-backward reasoning outperforms forward-only on verification.
- **RevThink** (Chen et al., NAACL 2025, arxiv 2411.19865) — 13.53%
  improvement from structured forward-backward reasoning.
- **RT-ICA** (arxiv 2512.10273, Dec 2025) — Reverse thinking detects
  missing information that forward approaches systematically miss.
  27.62pp gain on GPT-3.5.
- **Bi-Chainer** (Liu et al., arxiv 2406.06586, 2024) — Forward chain
  from premises, backward chain from conclusion, switch direction at
  branch points.

### Self-correction requires external feedback (tight)

- **"Large Language Models Cannot Self-Correct Reasoning Yet"** (Huang
  et al., ICLR 2024, arxiv 2310.01798) — Without external feedback,
  LLMs struggle to self-correct. Justifies the deterministic probe layer
  (file existence, git comod) as ground truth the model can't generate.

## Iterative Refinement

- **Self-Refine** (Madaan et al., NeurIPS 2023, arxiv 2303.17651) —
  ~20% absolute improvement from iterative critique-revision. Stabilizes
  by iteration 3.
- **Intrinsic Self-Critique for Planning** (Bohnet et al., 2025,
  arxiv 2512.24103) — Iterative self-critique improves planning tasks
  (22% → 37.8%).
- **Constitutional AI** (Bai et al., Anthropic, 2022, arxiv 2212.08073)
  — Multiple critique-revision rounds with different principles.

## State Compression Between Passes

- **"Lost in the Middle"** (Liu et al., TACL 2024, arxiv 2307.03172) —
  LLMs attend to beginning and end of context, worst in middle. Informs
  prompt positioning (checklist at end, hard constraints first).
- **"Think Twice"** (arxiv 2503.19855, Mar 2025) — Compressed prior
  between rounds avoids cognitive inertia.
- **CoT as computation** (Li et al., ICLR 2024, arxiv 2402.12875) —
  CoT adds computational depth. More passes = higher complexity class.

## Multi-Agent Reasoning

- **"Should We Be Going MAD?"** (Smit et al., ICML 2024) — MAD
  frameworks don't consistently outperform single-agent. 3 agents optimal.
- **DMAD** (ICLR 2025) — Value comes from different reasoning methods,
  not different perspective labels.
- **"Two Tales of Persona in LLMs"** (EMNLP 2024) — How you prompt the
  persona matters more than which persona.

## Sibling Projects

- **defn** (MIT) — AI-native Go code database. Stores definitions in
  Dolt (SQL + git semantics). Reference graph enables exact blast radius
  queries: "impact this function" → 21 callers, 134 transitive, 111
  tests. plancheck now queries defn reference graphs directly via the
  combined model (reference graph + git comod), with the `simulate` op
  for typed mutations on throwaway Dolt branches. The `source_file`
  column enables exact file-to-definition mapping for plan scoping.

- **adit** (MIT) — Structural code analysis. Measures file size, nesting
  depth, grep noise, blast radius, co-location. Validated against
  SWE-bench agent trajectories (1,840 files, 49 repos). File size is the
  dominant predictor (median Spearman +0.474). plancheck's design was
  informed by this finding (e.g., the code() tool for navigating large
  files) but does not depend on adit at runtime. plancheck computes test
  definition density independently via defn's maturity assessment.

## Empirical Validation of Theoretical Claims

_The table below reflects validation against the combined model (Layer 2,
pre-spike architecture, March 2026). These numbers are from that earlier
pipeline, not the current spike-based architecture. The current spike
pipeline has been benchmarked on 180 tasks across 4 repos (see README)
but those benchmarks measure different metrics (recall/F1 lift, not the
claims below)._

Each influence made a prediction about plancheck's design. Here's how
they held up:

| Claim | Source | Status | Evidence |
|-------|--------|--------|----------|
| Combined signals beat individual | Benter | **Validated** | +37% over graph alone, +57% over comod |
| Noise reduction is highest leverage | Tetlock BIN | **Plausible** | Gate forces iteration; not A/B tested |
| Backward traces catch different errors | FOBAR, RevThink | **Validated structurally** | Backward scout finds prerequisites forward misses |
| Self-correction needs external signal | Huang et al. | **Validated** | Probes find what model can't (file existence, comod) |
| Proportional effort matters | Kelly | **Validated** | High-blast changes have better prediction (67% vs 46%) |
| Test density predicts reliability | Novel finding | **Validated** | >35% → 85% hit rate, <20% → 50% |
| Exploration overhead is structural | Novel finding | **Validated** | 29% across 12 models, not model-dependent |
| LLM stubs add precision | Novel hypothesis | **Inconclusive** | Stubs work, but heuristic has higher recall |
| Ranking > filtering for file prediction | Novel finding | **Validated** | Top-3: 57% precision, 88% hit rate vs 18% grep |
| Novelty predicts prediction quality | Flyvbjerg/COCOMO | **Validated** | 62% hit rate on mods, 18% on additions |
| Forecast is calibrated | Vacanti/Magennis | **Validated** | MAE=0.078 on 138-task holdout |
| Cross-repo generalization | Shepperd | **Validated** | Train/test delta=0.071 across repos |
| Naive union > learned weights | Novel finding | **Validated** | +52% over grep, simple OR beats optimization |

The strongest validations are Benter (combined model) and Kelly
(proportional effort). The weakest is Tetlock BIN (noise reduction) —
we haven't A/B tested iteration counts against plan quality.

## Go-Specific Data

- **SWE-smith-go** — 8,212 task instances across 87 Go repositories with
  executable environments (gin, caddy, cobra, fzf, echo, gorm, etc.).
  Gold patches enable backtesting of plan simulation accuracy.

- **Multi-SWE-bench_trajs** (ByteDance) — Agent trajectories from
  Agentless, SWE-agent, and OpenHands on Go repos (cli/cli, grpc-go,
  go-zero). Provides plan → execution → outcome data.

- **CommitPackFT** (bigcode) — 4M+ commit message → diff pairs across
  multiple languages including Go. The largest source of "intent →
  change" data.

## What We Built (previously couldn't find)

These gaps drove the project. Status as of March 2026:

- **Bidirectional verification for SE plans**: implemented as Layer 1
  (forward/backward/subdivide traces). Not yet validated in isolation
  — the empirical work validated the combined model (Layer 2), not the
  bidirectional tracing (Layer 1).

- **Structural code analysis + plan verification**: implemented and
  validated. The combined model (reference graph + git comod) achieves
  68% recall on test failure prediction across 1,451 tasks on 7 repos.
  The reference graph alone predicts 55% of PR file changes on 22 real
  GitHub PRs. This is (to our knowledge) the first empirical validation
  of combining reference graphs with plan verification.

- **Agent plan → execution → delta**: partially built. We analyzed 425
  agent trajectories across 12 models and found 29% exploration overhead
  that is structural (model-independent). We haven't yet built the
  labeled dataset of "plan vs reality delta" — the raw materials are
  ready (SWE-smith-go gold patches, Multi-SWE-bench trajectories,
  CommitPackFT Go commits, GitHub PRs with diffs).

- **"Plan as prediction" framing**: articulated in this document and
  validated through the combined model. The framing drove specific
  design decisions: confidence-weighted findings (Kelly), forced
  iteration (Tetlock BIN), backward scouts (Duke backcasting), and
  proportional effort (complexity-scaled gate rounds).

## New Research (discovered March 2026)

### Code graphs improve agent performance (validates our approach)

- **Code Graph Model (CGM)** (arxiv 2505.16901) —
  Integrates repository code graph structures into LLM attention.
  43% on SWE-bench Lite, first among open-weight models. Validates
  that code structure graphs improve real task performance. Uses
  graphs for generation, not verification — our contribution is
  the verification side.

### Bidirectional evaluation is a general technique (validates our architecture)

- **ToolTree** (ICLR 2026, arxiv 2603.12740) — MCTS with
  bidirectional pruning for tool planning. Pre-evaluation anticipates
  usefulness before execution, post-evaluation scores after. 10%
  improvement over SOTA. Closest to our forward/backward approach
  but at tool-selection level, not plan-segment level.

- **Bidirectional Process Reward Model (BiPRM)** (arxiv 2508.01682)
  — Parallel right-to-left + left-to-right evaluation with gating.
  Validates bidirectional evaluation as a general improvement over
  unidirectional. Applied to reasoning steps, not plans.

### Code model calibration is systematically wrong (validates our problem)

- **Multicalibration for Code Generation** (arxiv 2512.08810) —
  First paper on multicalibration specifically for code. Using
  structural features (like our test density, comod confidence)
  improves prediction calibration. **Directly validates our
  approach** of conditioning prediction quality on structural signals.

- **"Do Code Models Suffer from the Dunning-Kruger Effect?"**
  (Microsoft, arxiv 2510.05457) — Models are systematically
  overconfident, especially in low-competence regimes. Tested across
  37 programming languages. Confirms that uncalibrated agent
  confidence is a real problem our calibration store addresses.

- **"Mind the Generation Process"** (arxiv 2508.12040) —
  Proposes fine-grained confidence estimation including a Backward
  Confidence Integration technique that uses subsequent text to revise
  confidence for earlier steps. Connects to backward tracing.

### Hierarchical planning is advancing but not verifying (validates our gap)

- **Autonomous Deep Agent** (arxiv 2502.07056) — Hierarchical Task DAG with
  recursive two-stage planner-executor. Models complex tasks with
  recursive decomposition. Closest to our subdivision but decomposes
  then executes — doesn't verify each segment independently.

- **AB-MCTS** (arxiv 2503.04412, Sakana AI) — Follow-up to
  SWE-Search. Adaptively decides "go wider" (new candidates) or
  "go deeper" (refine existing). Bayesian posterior for explore/exploit.

- **Empirical-MCTS** (arxiv 2602.04248) — Cross-episode learning
  for MCTS (remembers across search episodes). The MCTS line is
  complementary to plancheck: MCTS explores execution paths,
  plancheck verifies plans before execution. Could compose.

## What We Still Can't Find

- No study of how plan verification affects downstream code quality
  (we measure prediction accuracy, not bug prevention)
- No A/B test of forced iteration (gate) on plan outcomes
- No cross-language validation (Go-specific via defn)
- The METR 2025 finding that AI tools INCREASED completion time by 19%
  for experienced devs raises the question: does plancheck add overhead
  that outweighs its value? Validation overhead is a real cost.
- Flyvbjerg's 1-in-6 black swan rate: can we detect black swan plans
  BEFORE execution? The novelty score is a proxy but unvalidated.

## Structured RAG Thesis

plancheck + defn = structured RAG for code change planning. The LLM
is the pattern recognition engine (trained on all of open source). defn
databases are the knowledge store (actual structural relationships,
queryable and verifiable). plancheck is the comparison layer.

This is how open source is supposed to work: we expect LLMs to emit
knowledge without grounding in the same examples that went into their
training data. But LLMs are better pattern recognition and translation
engines than knowledge stores. The defn databases bring the grounding
back — "here's how 3 real projects actually structured middleware,
with verifiable reference counts" instead of "here's what middleware
generally looks like."

Cross-project analogies are structured RAG with a queryable schema,
not vector search. The results are verifiable facts ("18 callers"
is a fact, not a similarity score).

---

## Actionability research — why output format determines adoption

**Analogy: tight. Static analysis tools are the closest prior art.**

Nguyen et al. (2022), "Why Do Software Developers Use Static Analysis
Tools?" — 41% false positive rate causes 28% developer dissatisfaction.
33% of teams spend >20% of remediation time reviewing non-actionable
alerts. Tools that only add comments without minimizing review effort
are ignored or turned off (Springer EMSE, 2019).

**Applied insight**: plancheck's output must provide context +
explanation + clear fix path, not just findings. Each ranked file
includes a task-specific reason ("co-changes with delete.go 49%") not
a generic label ("comod gap").

**Analogy: tight. Qodo's empirical results validate this.**

Qodo achieved 73.8% acceptance rate by combining context, explanation,
and fix path. Acceptance jumped 50% when they focused on meaningful
bugs through severity-driven triage. Without severity triage, critical
issues get buried under cosmetic noise and developers ignore AI
comments entirely.

**Applied insight**: plancheck ranks by signal strength (structural >
comod > sibling > analogy) and suppresses noise (hub dampening,
confidence gate on suggestions). The judge synthesizes signals into a recommendation
rather than listing everything.

**Analogy: moderate. Nudge theory / choice architecture (Thaler &
Sunstein, 2008).**

Choice architecture creates environments where the right choice is
easiest. Tools of choice architecture: defaults, expecting error,
understanding mappings, giving feedback, structuring complex choices.
Thoughtworks applied this to engineering platforms: "create environments
which make it easier for teams to choose what's best."

**Applied insight**: plancheck's checklist prompt ("INCLUDE X unless
you explain why not") is choice architecture. The default is inclusion.
Exclusion requires explicit justification. Combined with end-of-prompt
positioning and hard-constraints-first ordering, this cracked task 7
after 10+ flat runs, driving lift from +12.5pp to +14.3pp.

## Instruction Following — why models ignore suggestions

**Analogy: tight. The prompt IS the interface.**

"Curse of Instructions" (OpenReview, 2025) — As constraint count
increases, compliance with each drops. GPT-4o follows 10 simultaneous
instructions only 15% of the time. Models overwhelmingly commit
**omission errors** (34:1 ratio vs modification errors). Practical
implication: separate background context from actionable checklist to
reduce competing instructions.

"Lost in the Middle" (Liu et al., TACL 2024, arxiv 2307.03172) —
U-shaped attention: beginning and end of context get most attention,
middle is systematically neglected (>30% performance drop). File
suggestions buried in mid-prompt analysis are in the worst position.

"Order Matters" (arxiv 2502.17204, 2025) — Hard-to-easy ordering
improves compliance 5-7% single-round, 10-25% multi-round. Hard
constraints placed first cause more attention to the constraint
section overall. Applied: keyword-dir files (domain gaps) listed
first in the checklist.

Few-shot examples improve tool-calling compliance 16% → 52% (LangChain,
2024). Message format (fake conversation history) outperforms string
format for Claude models.

**Applied insight**: plancheck's benchmark prompt separates background
analysis from an actionable checklist at the END of the prompt, with
domain gaps listed first. This is the combination of Lost in the Middle
(position), Curse of Instructions (separation), Order Matters (hard
constraints first), and default-inclusion bias (Thaler/Sunstein).
