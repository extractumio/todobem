# Insights — development spec (draft 3, 2026-09-14) — **implemented** (v1 catalogue)

Implementation record: P0 (per-turn tokens, compaction context, verdict-only failures), P0.5
(prototype over 20 sessions of the largest project: 9 recurring failing shapes, 140 compactions at
a 218 k median), P1 (`internal/insights`, sidecar facts, scanner, `/api/insights/*`), P2 (the page,
period report, groups, tones), P3 (the whole v1 catalogue: D1, D2, D2b, D3, D4, D7, D9, D11, D12,
D13, D14, D15, M1, T1, T2, T3, T6, T7) are in the tree. Deviations from the draft: T1 is a
tokens-only card (the break itself is D2 / D2b, so no double count); D9 keeps only command shapes
that ran two or more times in the period; D14 is shown from three occurrences; `Info` cards (D2b,
D14, D15, T7, D13) are shown but never ranked or totalled; the persisted report file was dropped
(a report from cached facts builds in well under a second — the page rebuilds on every period
change and probes the sources hash every minute for the staleness chip). Group colours follow
the timeline's phase palette. v1.1 items (D5, D6, D8, D10, T4, T5, estimates) stay open.

**Question the feature answers:** *Where is my harness inefficient?* — what costs wall-clock
between "task given" and "task delivered", what burns tokens, and what to change in the setup
(reply habits, delegation, skills, rules, models, effort) to deliver faster and cheaper.

Insights is a new page (`#insights`) that produces a **report for a period** (default: the
sessions of the last 30 days) over the parsed sessions of one project. It runs a catalogue of
deterministic pattern detectors, aggregates the findings across sessions, arranges them in
**seven groups** ordered by measured exposure, and shows each finding as a card: the measured
cost, its distribution, what to do, and the evidence — sessions, lanes and intervals, one click
away in the timeline. The page always says which period the report covers and when it was
generated; the period can be changed and the report regenerated.

Draft 1 was reviewed by the `pragmatic` agent (verdict: rework, targeted); draft 2 took the
review (record in §12). Draft 3 adds the user's requirements: groups, the period report with
its indicator and controls, a professionally designed UI, and **plain English** in every text a
reader sees. Every number below was measured on this machine on 2026-09-14, structure only, no
session content quoted: seven cached sessions, 60–200 root rollouts, corpus of 283 root threads
(304 files counting archived/tmp) / 1,625 sub-agent threads / 6.5 GB, CLI 0.134–0.154.

---

## 1. Product rules for Insights (extend CLAUDE.md rules 1–8; binding)

1. **An insight is a detector, not an opinion.** Every card is produced by a named rule
   (`D4`, `T1`, …) from the catalogue in §5 over literal structure in the model: event order,
   turn status, lane turns, retry identities, lifecycle stages, token records. No similarity, no
   models, no threshold on a duration to *explain* anything. A long operation is listed; a
   pattern is a structural fact (an aborted turn, a wait with one worker active, a question
   followed by a gap, a compaction).
2. **Exposure, distribution, evidence — no estimates in v1.** A card shows what the pattern
   measurably consumed in the period (time and/or tokens), how it is distributed (count, median,
   p90, buckets), and where. It never shows a counterfactual saving, a multiplier or a percent
   without its denominator printed next to it. Estimates (§5.5) are reintroduced one at a time,
   each with its own validation, after the exposure page has been used.
3. **Evidence is mandatory.** A card without at least one evidence link (session, lane,
   interval or op id) is not rendered. Evidence opens the session at that interval.
4. **Ranking is by absolute exposure in the period**, never by count of occurrences. The page
   has one axis switch, **Time** (default) or **Tokens**; groups and cards are ordered on the
   chosen axis. Time = root-lane time; Tokens = *uncached input + output tokens* (the axis is
   named on the page; cached input and reasoning are shown separately, never weighted). The
   denominator of every share is printed on the card. Ties by number of sessions affected.
5. **Cross-session cards need a project and at least three closed sessions in the period.**
   Aggregation is inside one `cwd`; live sessions are excluded (their open turn and trailing gap
   move on every refresh); below three sessions the page shows per-session cards and says why.
6. **"Did not happen" and "could not be measured" are different.** A card says "in N of M
   sessions" only over sessions where the signal exists for that CLI version
   (`token_usage_record` ≥ 0.153, sub-agent prompts encrypted ≥ 0.144, …); the rest are listed
   as "no data in K sessions (CLI < x)". Product rule 3 applies to counts.
7. **Advice is a template bound to the rule**, phrased as a change to the harness (habit,
   configuration, skill, rule, delegation pattern), never as a judgement of the user or the
   model, and never as a causal claim the log does not record.
8. **Nothing leaves the machine.** Insights adds no outbound path. The one fuzzy job that
   benefits from a model (clustering unknown commands into overlay rules) is handed to the user as
   a copyable prompt they run in their own agent (§8).
9. **A report has a period, a generation time and a staleness state.** The page never shows
   numbers without saying which sessions they cover ("Report for 15 Aug – 14 Sep 2026 · 12 closed
   sessions · generated 14 Sep 11:42"). The default period is the last 30 days. The period is
   changed on the page; the report is regenerated on demand; a report that no longer matches
   the sessions on disk says so.
10. **Plain English for non-native readers, everywhere a reader looks** (card titles,
    headlines, advice, group names, controls, empty states, the guide). Rules: short sentences
    (one idea, about 15 words or fewer); common words (*wait, reply, start, stop, cost, run*),
    no idioms, no metaphors, no jargon without a one-line explanation the first time it appears
    on the page; numbers always with units (`6h41m`, `50.8 M tokens`, `4 of 7 sessions`);
    active voice; "you" for the user, "the agent" / "the main agent" / "a sub-agent" for the
    actors; no abbreviations except `k`/`M` for tokens and `h`/`m` for time. Every visible string
    lives in one table (`INSIGHT_TEXT` in `insights.js`) so it can be reviewed against this rule
    in one place. Technical identifiers (`AGENTS.md`, `followup_task`, `rules.json`) may appear
    in code font, each with a plain-English phrase beside it.

---

## 2. Evidence base — what the corpus shows (measured)

Sessions in the derived cache (labels used throughout):

| label | id | elapsed | in-turn (root) | lanes | tokens (all lanes) | cached |
|---|---|---|---|---|---|---|
| S1 | 01a07ae9 | 52h45m | 8h13m | 12 | 220 M | 97.6 % |
| S2 | 01a07aea | 0h14m | 0h14m | 6 | 15 M | 95.9 % |
| S3 | 01a082a4 | 16h51m | 1h51m | 4 | 59 M | 97.7 % |
| S4 | 01a08af4 | 28h53m | 24h14m | 39 | 448 M | 97.9 % |
| S5 | 01a09218 | 37h06m | 19h19m | 103 | 824 M | 96.7 % |
| S6 | 01a09594 | 1h20m | 1h20m | 14 | 24 M | 97.8 % |

Patterns and their measured size (root lane unless stated; "all lanes" numbers are parallel
time and are never divided by a root total):

| pattern | S1 | S3 | S4 | S5 | S6 |
|---|---|---|---|---|---|
| waiting for user, gaps < 4 h (count · sum · median) | 27 · 5h14m · 4m | 1 · 6m | 13 · 4h38m · 3m | 7 · 1h31m · 6m | — |
| waiting for user, gaps ≥ 4 h ("away") | 2 · 39h18m | 1 · 14h53m | 0 | 2 · 16h15m | — |
| gap right after a question to the user | 1 · 2h27m | 0 | 2 · 4m | 0 | 0 |
| aborted (interrupted) root turns · time inside them | 3 · 45m | 0 | **3 · 9h42m** | 0 | 0 |
| root `wait_worker` · of which ≤ 1 sub-agent inside a turn | 1h03m · 1h02m | 24m · 24m | **6h48m · 6h41m** | 2h33m · 2h14m | 29m · 29m |
| sub-agent parallelism (agent time ÷ union wall, all lanes) | 1.61× | 1.86× | 1.59× | 2.25× | 2.05× |
| compactions · time, all lanes (parallel) | 18 · 50m | 3 · 6m | 20 · 1h00m | **64 · 3h05m** | 0 |
| retry-after-failure + fix + recovery + rerun time | 4m | 4m | 1h03m | 17m | 0 |
| retry groups · largest (attempts / failed) | 25 · infra 12/1 | 1 | 24 · test 4/0 | 85 · infra 6/4 | 0 |
| `unknown` time · share of elapsed | 10m · 0.3 % | 1m | **1h58m · 6.9 %** | 1h16m · 3.5 % | 0 |
| background processes · time | 1 · 0 | 0 | 3 · 58m | 1 · 33m | 0 |
| read-only sub-agent lanes (no edit/test/build/release) · tokens | 5/11 · 2.7 M | 0/3 | 20/38 · 13.7 M | 39/102 · 50.8 M | 12/13 · 2.0 M |
| longest single root turn | 1h14m | 1h43m | **13h28m** | 6h02m | 1h20m |
| model / effort of turns | astra/xhigh ×128 | ×26 | ×114 | ×343 | ×27 |

Corpus-wide facts that shape the design:

* **Prompt-cache share of the first response after a user gap** (60 root files, root lanes):
  gap < 5 min → 3.8 % of its input uncached (avg 4 k tokens); 5–15 min → 3.3 % (4 k);
  15–60 min → 21.7 % (28 k); 1–4 h → 46.4 % (54 k); ≥ 4 h → 97.3 % (133 k); n = 93 / 44 / 49 /
  18 / 20. An association in the data; the mechanism (a time-limited prompt cache) is the
  provider's documented behaviour, not an inference from this corpus.
* **Compaction happens at a median context of 212–219 k tokens** (two probes: 159 and 62
  compactions; range 85–246 k) against a reported `model_context_window` of 258 k; each takes
  140–230 s; the first response after it re-reads ~26 k tokens, of which ~12–14 k uncached.
* **Token classes** (60 root files): input 3,102 M, cached 3,043 M (98.1 %), output 9.0 M,
  reasoning 3.8 M (42 % of output).
* **`token_count` repeats.** The event is emitted more than once per model response and
  repeats `last_token_usage` verbatim (426 of 3,128 lines on S5's root; 258 of 1,378 on another):
  summing `last_token_usage` over-counts by 1.10× on average (worst 1.17×). The **delta of
  `total_token_usage`**, with a decrease treated as a fresh baseline (a resumed thread restarts
  the counter: 5 of 200 files), reproduces the harness's cumulative exactly on every file tested.
  `last_token_usage` is present on essentially every line (2 exceptions in ~8,600) and is only
  right for per-response fields.
* **`token_usage_record` is per response**, not per turn (1,120 records = 1,120 response ids
  on one file); `turn_token_usage` is monotone within a turn, so a turn's total is its *last*
  record. Present only from CLI 0.153. It carries `root_turn_id` — a harness-level link from a
  sub-agent's response to the root turn that was running, the only literal attribution of
  sub-agent work to a root turn on disk.
* **Around each `ContextCompaction`** the harness writes an all-zero `token_count` before (and
  often after) the item; the last non-zero record before it is the context, the first non-zero
  record after it is the re-read. First-of-turn records need the same guard: 8 of 672 turn
  starts carry zero input, 8 repeat the previous turn's last record.
* **Failure counts are inflated today.** `lane.go` marks every non-zero exit `failed` and
  `derive.go` counts it in `failed_ops`. Across the cached sessions the failed `code` ops are
  `read` 335, `search` 280, `list_files` 45, `git diff/show/status` 82 — a search exiting 1 is
  "no match", a read exiting 2 is a wrong path: probe misses, not harness failures (208 of 380
  failed ops in one project's 10 sessions). Every failure-ranked card would inherit this.
* **Exact-command recurrence across sessions is zero** in the largest project (10 sessions:
  662 distinct commands, 5 recur, 0 fail in two sessions — temp paths and MR numbers differ per
  session); by classifier key (head word + subcommand) 28 shapes recur and 7 of 40 failing shapes
  fail in ≥ 2 sessions. Retry grouping stays exact (rule 5); cross-session keys must be shapes.
* **Projects are small.** 283 root threads over 49 `cwd`s; 31 projects have one session;
  median 1; the largest has 163 sessions (1.81 GB). Cross-session insight exists in a handful
  of projects; the 30-day default period narrows it further, so the < 3 sessions fallback
  (rule 5) will be the common case for small projects.
* **Aborted turns are rare:** 18 of 650 turn starts in 150 files — but one session holds 9h42m
  inside them.
* **Model mix exists but is rare:** 3 of 80 recent files switch model or effort mid-session.
* **Sub-agent prompts and results are encrypted from CLI 0.144.** Whether two workers were
  independent is not on disk. User messages, final answers, questions, commands, exit codes,
  edited paths and the exact source line of every event are.
* **No approval events** in this corpus (approval policy `never`): "waiting for approval" is
  observable only as a `question` marker followed by a `wait_user` gap, or as an aborted turn.
* **`Lane.Active` is built from the lane's own turns** (`derive.go activeIntervals`), not from
  the parent's `SubAgentActivity` events as `SCHEMA.md` says; nothing in `internal/codex` sets
  it. The turn-based meaning is the better signal ("the child was inside a turn"); the doc is
  fixed in P0.

---

## 3. Approach: deterministic detectors over the derived model

**Hand the sessions to an agent and let it conclude** — rejected as the core. A session here is
27–70 MB of rollout (S5: 824 M tokens of history); an agent would see a sample and produce
non-reproducible prose without evidence links or numbers that survive rules 1–4, and would
contradict product rule 8 for every user.

**Engineered detectors over `model.Session`, aggregated per project and period** — chosen. The
model already carries what §5 needs — turns with status and trigger, lifecycle stages, retry
groups with attempt roles, sub-agent turns, wait kinds, compaction ops, markers — except per-turn
tokens, which §4 adds. Detectors are pure functions over a compact per-session `Facts` record,
unit-testable on synthetic fixtures; their output carries op/lane/interval ids the timeline
already knows how to focus.

**An LLM step in the product** — considered and rejected (§8): the one job where fuzziness
helps is handed to the user's own agent as a copyable prompt.

---

## 4. Data additions (ingestion + schema)

Token handling is extracted from `lane.go` into a new `internal/codex/tokens.go` (the file must
not grow); `model.go`, `docs/SCHEMA.md`, `cmd/dump`, `app.js` change together; store
`cacheVersion` 4, `RulesFingerprint` schema tag 3. Streaming only; no raw events retained; every
usage record with zero input is skipped for "last value" reads and harmless for sums.

| field | source (literal) | used by |
|---|---|---|
| `Lane.Tokens` — **now the sum of per-response usage** (was: last cumulative) | delta of `token_count.total_token_usage` between consecutive records; a decrease starts a fresh baseline (resume); a forked child's first record is the parent's inherited counter, so the first record of a file is a baseline, never a delta from zero | fixes the reset defect (§10); every token card |
| `Turn.Tokens *TokenUsage` | the same deltas attributed to the open turn; cross-checked against the last `token_usage_record.turn_token_usage` of the turn where present (CLI ≥ 0.153) | T1–T3, T6, D3/D4/D7 token exposures |
| `Turn.Responses int` — model responses | distinct `token_usage_record.response_id` when present, else `token_count` records whose `last_token_usage` differs from the previous one | T5, M1 |
| `Turn.First *TokenUsage` — the first response of the turn | first `token_count` after `task_started` whose `last_token_usage` is non-zero and not a repeat of the previous turn's last; else nil = "no data" | T1 (cache share after a gap), T6 (spawn cost) |
| `Turn.ContextPeak int64` | max `last_token_usage.input_tokens` in the turn | T2 |
| `Turn.RootTurn string` (sub-agent lanes) | `token_usage_record.root_turn_id` (CLI ≥ 0.153); "" = no data | D4/T6 attribution of sub-agent work to the root turn |
| `Operation.Context int64` on compaction ops | `input_tokens` of the last non-zero `token_count` before the item | D11, T2 |
| `Operation.Tokens *TokenUsage` on compaction ops — the re-read | first non-zero `token_count` after the item | D11 |
| `TokenUsage.CacheWrite int64` | `cache_write_input_tokens` | shown with tokens |
| `Totals.Failed` — **verdict failures only** (in progress in the working tree as `Operation.QueryMiss`, set by `Derive` from `classify.QueryKind`, and `Operation.Failure()`) | a query kind's non-zero exit (`read`, `search`, `list_files`, `git diff --quiet`, `test`/`which` probes: the literal list is `classify.queryKinds`) keeps `status: failed` and `exit` on the op but is a *query miss*, not a failure; `Failure()` is the single predicate every count, filter, retry role and Insights signal uses; a `classify_test.go` / `derive_test.go` case each | D7, D15, the metrics tile |
| `SCHEMA.md` `Lane.Active` | documented as "intervals of the lane's own turns" | D4 |

P0 validation (definition of done): on three real sessions, per lane, the reconstructed
`Lane.Tokens` equals the harness's last `total_token_usage` when no reset occurred, and the sum
of the per-reset baselines otherwise; `Σ Turn.Tokens == Lane.Tokens`; where `token_usage_record`
exists, each turn's last `turn_token_usage` equals `Turn.Tokens` within 1 %. `docs/VALIDATION-
PROTOCOL.md` item 5 changes from "last cumulative" to this reconstruction.

---

## 5. Groups and the detector catalogue

### 5.1 Groups

Cards are arranged in seven groups. A group is a question a reader can act on; its header shows
the group's total exposure on the chosen axis with its denominator, the number of cards, and
how many sessions had data. Groups are ordered by their exposure on the chosen axis; **Not
measured** is always last. A group with no cards in the period is shown collapsed with "Nothing
found in this period" so the reader sees that it was checked.

| group (plain English name) | the question it answers | cards |
|---|---|---|
| **You and the agent** | How fast do you reply, and what does waiting cost? | D1, D2, D2b, D3, T1 |
| **Sub-agents** | Do sub-agents run in parallel, and what does starting them cost? | D4, T6 (v1.1: D5) |
| **Failures and retries** | What breaks, and how often? | D7, D15, D14 (v1.1: D6, D8) |
| **Long tool runs** | Which commands take the longest, and what keeps running? | D9, D12 (v1.1: D10) |
| **Context size** | How big does the context get, and what does compaction cost? | D11, T2 |
| **Models and effort** | Which model and effort does each stage use? | M1, T3, T7 |
| **Not measured** | What could we not see? | D13, the "no data" counts of every other card |

A **Top findings** strip above the groups lists the three highest-exposure cards across all
groups, one line each, linking down to the card.

### 5.2 Card anatomy and text rules

Every detector declares: **signal** (the literal structure), **exposure** (what is summed),
**distribution** (what the card shows besides the sum), **advice** template, **evidence** (what
the card links to), **measurable** (which sessions can carry the signal), and the **measured**
value on this corpus. A finding carries both a time and a token exposure where both exist, so
one card shows both numbers and ranks on the chosen axis. Named conventions (thresholds that
only split a display, never explain) are listed in §5.6 and printed on the card.

Card text (rule 10) has four fixed parts, each one or two short sentences:
**What happened** (the measured fact with its denominator) · **How it is spread** (the
distribution) · **What to do** (the advice) · **Where** (the evidence table). The texts below
are the templates; numbers are examples from this corpus.

### 5.3 Delivery detectors

**D1 · The agent waited for your answer.** Signal: a root `wait_user` segment whose preceding
turn contains a `question` marker. Exposure: the gap.
*What happened:* "The agent asked you a question 3 times. You replied after 2h31m in total."
*What to do:* "Reply sooner. Or write the default answers into `AGENTS.md` (the instructions
file) so the agent does not need to ask. Or use plan mode at the start."
Measured: S1 2h27m after one question.

**D2 · Time to your reply.** Signal: root `wait_user` segments shorter than the *long break*
convention (4 h). Exposure: their sum. Distribution: count, median, p90, buckets (< 5 m,
5–15 m, 15–60 m, 1–4 h) — the same buckets as T1 so the two cards read together.
*What happened:* "You replied 27 times. The agent waited 5h14m in total (of 52h45m)."
*How it is spread:* "Half of your replies came within 4 minutes. One in ten took more than
41 minutes."
*What to do:* "Check the session more often, or answer several questions at once. Give the
agent follow-up work (goal loop, `followup_task`) so it keeps working while you are away."
Measured: S1 27 gaps 5h14m (median 4 m); S4 13 gaps 4h38m.

**D2b · Long breaks (4 hours or more).** Signal: gaps ≥ 4 h. Listed under "unused wall-clock",
never ranked as inefficiency.
*What happened:* "2 breaks, 39h18m in total. The agent had no work during this time. This is
not a mistake."
*What to do:* "Give the agent follow-up work before you leave. Or end the session and start a
new one later: after a break of 4 hours or more, the model re-reads almost all of its context
without cache (97 % in this data, see T1)."
Measured: S1 39h18m, S5 16h15m.

**D3 · Turns you stopped.** Signal: turns with status `aborted`. Exposure: in-turn time of those
turns (root and sub-agents separately) and their `Turn.Tokens`.
*What happened:* "You stopped 3 turns. The agent had worked 9h42m inside them (of 24h14m in
total)."
*What to do:* "Before a long task, ask for a plan first (plan mode). Ask the agent to report at
checkpoints. Tell it when to stop." No sentence about what the stop discarded: the transcript
stays in the thread and the log does not record what was salvaged.
Measured: **S4 3 turns · 9h42m**; S1 3 · 45 m; corpus-wide rare (18 of 650 turn starts).

**D4 · Sub-agents ran one after another.** Signal: root `wait_worker` segments (kind `agent`)
during which at most one sub-agent lane is inside a turn (`Lane.Active`). Exposure: that time.
Distribution: per waited-for sub-agent; where `Turn.RootTurn` exists (CLI ≥ 0.153), the
sub-agent tokens spent inside each such wait.
*What happened:* "The main agent waited 6h41m while only one sub-agent was working (of 24h14m
in turns; in 4 of 7 sessions; 2 sessions have no data)."
*What to do:* "If the sub-agents did not depend on each other, start them together and wait
once. If they did, let the main agent do its own next step while it waits. The log does not
show whether they depended on each other."
Measured: **S4 6h41m**, S5 2h14m, S6 29 m of 1h20m.

**D7 · Commands that fail and get retried.** Signal: retry groups with ≥ 1 failed attempt
(exact identity, unchanged). Exposure (root): time in `retry_after_failure` + `fix` +
`infra_recovery` + `worker_queue`, plus `Turn.Tokens` of the turns overlapping the
failed-to-retry windows (pro rata by time). **Cross-session key: the command shape** — the
classifier rule key (head word + subcommand, or the overlay rule) — labelled "command shape" on
the card, never the exact command (0 exact recurrences in the largest project; 7 failing shapes
recur). Three advice variants by phase:
- infra: "This setup step failed first in 4 of 7 sessions. The retries and fixes took 1h03m.
  Fix the setup script or the image. Or write the working command into `AGENTS.md`."
- test / build: "This test or build fails on the first run in 5 of 7 sessions. Add the check
  that the fix always does, before the run."
- release: "The push or the CI run failed first in 3 of 7 sessions. Check what the fix changed
  each time and do it before the push."
Measured: S4 1h03m; shapes with 12/10 attempts (S1) and 6/6/6 (S5) on one `docker run` shape.

**D9 · Long tool runs.** Signal: test/build/release/infra ops aggregated by command shape (and
by exact identity within a session). Exposure: total op time per shape; count, median. Listed,
not explained (rule 1).
*What happened:* "This build script ran 14 times, 4h12m in total (usually 18 m)."
*What to do:* "Try a faster or incremental version. Or start it early and let the agent do other
work while it runs."
Measured: single CI build/test scripts of 31–51 m per run in S4/S5.

**D11 · Context compaction pauses.** Signal: `compaction` ops. Exposure: **two numbers, never
mixed** — root-lane compaction time over root elapsed (partition share), and all-lane compaction
time reported as parallel time; plus the re-read tokens (`Operation.Tokens`). Distribution:
contexts at compaction (`Operation.Context`), durations.
*What happened:* "The harness compacted the context 64 times, at about 212 k tokens each time.
This took 3h05m across all agents (41 m on the main agent, 1.8 % of its time)."
*What to do:* "Split long tasks into new threads or sub-agents at stage boundaries. Keep
instruction files short. Do not paste large outputs into the chat."
Measured: S5 64× 3h05m all lanes; S1 18× 50 m.

**D12 · Processes left running.** Signal: `Operation.Background`. Exposure: duration, count.
*What happened:* "A server or watcher kept running after its turn ended, 3 times (58 m)."
*What to do:* "Stop it, or run it under the harness process manager."
Measured: S4 3 · 58 m; S5 1 · 33 m.

**D14 · Invalid tool calls.** Signal: `llm_error` markers. Exposure: count, tokens of the
response that follows. Shown when ≥ 3 in the period.
*What happened:* "The model sent 4 tool calls with invalid arguments. The retries cost 0.3 M
tokens."

**D15 · Edits that failed.** Signal: `failed` ops of kinds `edit`/`apply_patch`/`script-*`/
`write-file`/`shell` (verdict failures in the `code` phase, invisible to D7 because they carry no
identity). Exposure: count; the time to the next successful edit of the same path where recorded.
*What happened:* "17 patches failed to apply in 5 sessions. The next successful edit of the same
file came 4 m later (median)."
*What to do:* "Ask the agent to read the file right before it edits. Keep patches small."
Measured after P0 (the current count is polluted by probe misses).

**M1 · Model time by model, effort and stage.** The largest in-turn phase had no card in
draft 1: `llm` is 27 % of in-turn on a real session while every delivery detector looks at the
waits around it. Signal: `llm` segments × `Turn.Model` × `Turn.Effort` × lifecycle stage.
Exposure: time and `Turn.Tokens` per cell (ms-weighted over the turn's segments). Measurement
card; no substitution value is computed.
*What happened:* "All 343 turns used gpt-6-astra at effort xhigh. Model time by stage:
implementation 4h10m, review 1h55m, test 0h40m."
*What to do:* "You can set the effort per stage and a model per agent role in the harness
config."

### 5.4 Token detectors

**T1 · Cache after a break.** Signal: for each root turn started after a `wait_user` gap,
`Turn.First` (uncached = input − cached) bucketed by gap length (< 5 m, 5–15 m, 15–60 m, 1–4 h,
≥ 4 h); turns with `Turn.First == nil` counted as "no data". Exposure: uncached input of turn
starts after gaps ≥ 15 m. Distribution: the buckets with n and uncached share; the period's own
< 15 m share shown as the reference row.
*What happened:* "When you replied within 15 minutes, 96 % of the context came from cache.
After 1–4 hours: 54 %. After 4 hours or more: 3 %. 87 turn starts after breaks of 15 minutes or
more cost 5.0 M uncached input tokens."
*What to do:* "Reply within the cache window. After a long break, start a new thread with a
short summary instead of continuing a 200 k context."
Measured (60 files): 15–60 m 1.4 M uncached / 49 starts; 1–4 h 0.98 M / 18; ≥ 4 h 2.66 M / 20.

**T2 · Context size.** Signal: `Turn.ContextPeak`, compaction contexts, input per response
(`Turn.Tokens.Input / Turn.Responses`). Exposure: input tokens (cached and uncached separately)
of turns whose peak exceeded the period median. Distribution: histogram of context per response.
*What happened:* "2 turns (13h28m, 6h02m) ran with a context of 200 k tokens or more for hours:
1,240 model responses at about 205 k input tokens each."
*What to do:* "A new thread at each stage costs fewer tokens per response and compacts less
often."

**T3 · Tokens by model, effort, stage and agent type.** Signal: `Turn.Model`/`Effort` ×
`Turn.Tokens` × lifecycle stage × lane kind (main agent / read-only sub-agent / worker sub-agent,
by literal op content). Exposure: tokens per cell, uncached input + output as the ranked axis.
The cell "read-only sub-agents on the default model at the default effort" is shown first; the
corpus's own mixed-model turns appear as comparison rows (tokens per response by model/effort — a
measurement). No substitution value is computed (the quality side is not measurable, rule 2).
*What happened:* "39 sub-agents only read code (no edits, no tests). They used 50.8 M tokens
(6 % of the session) on gpt-6-astra / xhigh."
*What to do:* "Give read-only sub-agents a cheaper model or a lower effort (Codex: a model per
agent in the agent config)."
Measured: S5 39 read-only lanes · 50.8 M of 824 M; S4 20 · 13.7 M; S6 12 of 13 lanes.

**T6 · Cost to start a sub-agent.** Signal: every sub-agent lane's first turn `Turn.First` (the
instructions and context the child re-reads) versus the lane's total. Exposure: spawn tokens;
ranked continuously by spawn tokens over total, no op-count cliff.
*What happened:* "13 sub-agents used more tokens to start (1.4 M: instructions and context)
than to work (0.7 M)."
*What to do:* "Combine small lookups into one sub-agent, or do them in the main agent."
Measured: S6 13 lanes · 2.1 M of 24 M.

**T7 · Reasoning tokens.** `reasoning_output / output` by effort and lane kind. Measurement
(42 % corpus-wide), shown under T3.
*What happened:* "Reasoning is 42 % of all output tokens. All of it at effort xhigh."

Every delivery detector that owns turns (D3, D4 via `Turn.RootTurn`, D7 via windows, D11) also
reports those turns' tokens, so a card shows both numbers and ranks on either axis.

**D13 · Not measured.** Signal: `unknown` and `no_telemetry` time; unknown heads with counts and
time; orphaned turns. Exposure: time. Own group, always last, never mixed with delivery ranking.
*What happened:* "1h58m (6.9 % of the session time) is not classified: `mytool` ×41,
`run-thing` ×17 …"
*What to do:* "Add rules for these commands in `~/.todobem/rules.json` (the rules file).
[Copy as prompt] After a rule change, all cached sessions are analyzed again (last full scan:
64 s)."
Measured: S4 1h58m (6.9 %), S5 1h16m. The group also lists, per card, "no data in K sessions
(CLI < 0.153)" so a reader sees what the corpus cannot say before reading what it says.

v1.1 candidates (need corpus validation first): **D5** review then release in sequence (needs
CI waits measured); **D6** edit during a running test, then rerun (1 case · 9 m in S4); **D8**
reruns after a pass (12 m S4); **D10** polling (`sleep`/lease/CI kinds, `queued` flag;
responses that only polled); **T4** retry tokens (needs D7 window ids on turns); **T5** polling
responses.

### 5.5 Estimates — deferred, with the reasons

Draft 1 carried counterfactual estimates; the review showed each rests on something the log does
not record: D2's "if every reply came as fast as your median" is not an upper bound on anything
observable and moves with the long-break convention; D4's `Σ − max` assumes worker independence,
which is unverifiable (prompts encrypted); T3's substitution needs prices and a quality cost;
T1's attainability conflates gap length with resumed threads and larger prompts. They return, if
at all, one per change with its own validation and a price table supplied by the user (never
built-in: a stale built-in price is a wrong number).

### 5.6 Named conventions (display splits, printed on the card, overlay-configurable)

`insights.long_break_h` = 4 (D2/D2b split), `insights.cache_buckets_m` = 5/15/60/240 (D2/T1
buckets). Nothing else in the catalogue uses a duration threshold.

---

## 6. Architecture

```
codex.Index ─► scanner (own parse path) ─► Facts per session (tiny, cached) ─► detectors ─► Findings
                                                                                   │
              /api/insights/report ◄─ period + project aggregation, groups, ranking ◄──┘
                                   │                    (report cached with its parameters)
                             #insights page · session-page card
```

**`internal/insights/`** (new package; depends on `model`, `classify`, `store`; emits its own
types only):

* `facts.go` — `Facts`: session id, cwd, branch, model, CLI (drives *measurable*), started/ended,
  live, root totals; per-turn rows (start, end, status, trigger, model, effort, lc split, tokens,
  responses, first, context peak, question?, root turn); root gaps with their predecessor turn;
  sub-agent lane rows (path, role, kind read-only/worker, active intervals, ops, tokens, first
  turn); groups (identity, shape key, phase, attempts, failed, windows); compaction events (t,
  lane, duration, context, re-read); background ops; unknown heads with time; llm_error count;
  long-op shapes (top 20 by time); lifecycle × model × effort cells. 5–30 KB per session.
  `Extract(*model.Session) Facts` is a pure function.
* `detect_delivery.go`, `detect_tokens.go`, `detect_blindspots.go` — one function per rule,
  `func D4(f *Facts) []Finding`; `Finding = {Rule, Session, Lane, A, B, Op, TimeMs, Tokens, Note}`.
* `report.go` — `Build(period, project, facts[]) Report`: selects the sessions of the period
  (§6 "Period"), groups findings by rule (and by shape for D7/D9), computes exposure sums,
  distributions, affected/no-data session counts, assigns cards to groups, orders groups and
  cards on both axes, picks the top findings, stamps `generated_at` and the fingerprints of the
  facts it used.
* `scan.go` — `Scanner` over `Index.Roots()` filtered by project and period: facts file hit
  (fingerprint equal) → load; else model cache hit → `Extract`; else — **only on an explicit
  Analyze** — parse on a **dedicated path** that never touches the server's `opened`, `lastUse`
  or `cached` maps (a full parse through `loadModel` would evict the user's open sessions from
  the 6-slot LRU and fill the 32-slot model pool with ~25 MB models): `codex.Open` + `Refresh`
  into a local session, write the model cache and the facts file, drop the model. Bounded pool
  (2), cancellable, yields between sessions; progress `{done, total, parsing, errors}`. Live
  sessions are extracted on request and never persisted. Note: the server mutex is *not* held
  across a parse today (`codex.Open` under the lock is struct construction; `Refresh` runs
  unlocked), so the scanner does not freeze requests — the churn of the two pools is the reason
  for the separate path.
* `store`: `<id>.facts.json.gz` = `{version, fp, facts}` with the same `Fingerprint` as the
  model cache plus a facts schema version. Measured: loading a cached model costs 4–130 ms
  (4 → 103 lanes), a facts file ~1 ms; re-extracting 283 sessions from model caches would be
  12–40 s of CPU per page load, facts make it sub-second and keep 283 models out of memory.
  Growth: 8 cached sessions = 6.8 MB, so a full scan writes ~100–250 MB under `~/.todobem/cache`;
  `store` gets a prune (`todobem cache -prune`: drop entries whose id the index no longer knows —
  one orphan exists already — and, optionally, entries older than N days).
* `store`: `reports/<hash>.json` — the last report per (project, period kind, custom range,
  session id): the full `Report` JSON plus the list of `(session id, facts fingerprint)` it was
  built from. On page open the server returns it immediately and marks it **stale** when any of
  those fingerprints changed, a session in the period is missing, or a new closed session ended
  inside the period. Building a report from cached facts is sub-second, so a stale report is
  simply rebuilt on **Regenerate** (and on every period change); the persisted copy exists so
  that the page opens with numbers and a generation time, never with a spinner.

**Period.** A session belongs to the period when its last activity (`ended` for closed
sessions) lies inside `[from, to]`; `to` defaults to now; the period is evaluated in the
viewer's time zone, like every timestamp in the app. Kinds: `7d`, `30d` (default), `90d`,
`all`, `custom` (`from`, `to` as dates, inclusive), `session` (one root id; then the report is
that session's cards, cross-session cards absent by construction). Live sessions never count,
whatever the period (rule 5).

**Overlay changes re-analyze everything.** `RulesFingerprint` is part of every cache key, and
every fact depends on classification, so acting on D13's advice invalidates all model caches,
facts files and reports. The D13 card says so with the measured cost of the last full scan; the
rescan is incremental (facts first from model caches, parses only where needed), backgrounded
and cancellable.

**Server** (`internal/server/insights.go`, behind the auth gate):

* `GET /api/insights/report?cwd=&period=30d|7d|90d|all|custom|session&from=&to=&session=`
  → the cached `Report` for those parameters (`stale: true` when it no longer matches the disk),
  or a freshly built one when none exists and every session in the period has facts.
* `POST /api/insights/report` (same parameters) → rebuild from facts now; returns the new
  report; `pending` lists the sessions in the period that have no facts yet (not parsed).
* `POST /api/insights/scan` `{cwd, period…}` → parse the pending sessions in the background;
  `GET /api/insights/status` → progress; `DELETE /api/insights/scan` → cancel.
* `GET /api/insights/rules` → the catalogue (id, group, title, signal, exposure axis,
  conventions, advice template) for the guide, the way `/api/rules` exposes the classifier.

**Report JSON:**

```
Report {
  generated_at, period: { kind, from, to, label: "15 Aug – 14 Sep 2026", session? },
  project: { cwd, label },
  scope: { sessions: 12, closed: 12, live_excluded: 1, pending: 3, no_data: {rule: n},
           root_elapsed_ms, root_in_turn_ms, wait_user_ms, tokens: {input, cached, output, reasoning} },
  stale: { is: bool, changed: n, new: n, missing: n },
  top: [ {rule, group, headline} ],                                   // 3 entries
  groups: [ { id, name, question, order_time, order_tokens,
              exposure: { time_ms, tokens, of: "root elapsed 5d 3h" },
              cards: [ Card ], sessions_with_data: n } ],
  fallback?: "fewer_than_3_sessions" | "no_sessions"
}
Card { rule, group, title, headline,
  exposure: { time_ms?, tokens?: {input, cached, output, reasoning}, axis },
  share?: { pct, of: "root elapsed 24h14m" },              // denominator always spelled out
  distribution?: [ {label, n, time_ms?, tokens?} ],       // buckets, per-lane rows, shapes
  sessions: int, of: int, no_data: int, reason?: "CLI < 0.153: no root_turn_id",
  conventions?: [ "long break = 4 h" ],
  evidence: [ { session, title, lane, path, a, b, op?, time_ms, tokens?, note } ],   // top 10
  advice: string }
```

---

## 7. UI / UX

Design intent: a **report page** a professional would read in the morning and could hand to a
colleague — calm, dense where the numbers are, generous where the reading is. It reuses the
app's design tokens (`app.css`: surfaces, radius, chips, eyebrow, mono numerals, phase colours)
so it looks native next to the timeline; nothing is invented that the rest of the app does not
already have. All text follows rule 10.

**Navigation.** Sidebar item **Insights** (icon `target`) under Sessions; hash `#insights`;
breadcrumb `Codex / Insights`; mobile nav gets the same button. The session page gets a
**Session insights** card between the metrics and the timeline: the session's top 3 cards and a
link "See the report for this project".

**Page structure (top to bottom):**

1. **Page heading.** Eyebrow "Codex insights on this machine", H1 "Insights", subtitle in one
   sentence: "Where your sessions lose time and tokens, with the evidence."
2. **Report bar** (sticky under the top bar; wraps to two rows on narrow screens):
   - *Period control:* a `select` — **Last 7 days · Last 30 days (default) · Last 90 days ·
     All time · Custom dates… · One session…** — followed by the controls it needs: two
     `input type="date"` fields (from / to, inclusive) with **Apply** for custom dates; a
     searchable session picker (title, id, date, project) for one session.
   - *Project control:* `select` of `cwd`s with session counts in the chosen period, default =
     the project of the last opened session (rule 5's fallback shows when it has < 3).
   - *Report indicator chip:* "Report for **15 Aug – 14 Sep 2026** · 12 closed sessions ·
     generated 14 Sep 11:42". When stale: an amber chip "3 sessions changed since this report —
     **Regenerate**". When sessions are unparsed: "3 sessions not analyzed yet — **Analyze**"
     with a progress bar and **Cancel** while it runs.
   - *Axis switch:* a two-button segmented control **Time · Tokens** (changes group and card
     order and which number leads each headline; both numbers stay visible).
   - *Regenerate* button (secondary style; primary while stale). Changing the period or the
     project regenerates automatically.
3. **Summary strip:** four tiles in the existing `metrics-grid` style — Sessions in the period
   (closed / live excluded / not analyzed), Agent time in turns, Waiting for you, Tokens
   (uncached input + output, with cached and reasoning as the note). Then **Top findings**: three
   lines, each "rank · group · headline · exposure", linking to the card.
4. **Groups** (accordion; all open by default on desktop, the first two open on phones):
   header row = group name, the question in muted text, the exposure with its denominator on the
   chosen axis, "N cards · data in M of K sessions", a chevron. Order by exposure; **Not
   measured** last. Empty group: collapsed, "Nothing found in this period."
5. **Cards** inside a group, single column, max width ~880 px for reading comfort:
   - top row: rank badge, title (sentence case), a small chip with the rule id (`D4`) that
     links to the guide entry;
   - **headline**: one sentence, the leading number in bold with its denominator in the same
     sentence ("**6h41m** of 24h14m in turns, in 4 of 7 sessions");
   - **How it is spread**: a compact distribution — horizontal bar list for buckets (label ·
     bar · n · value), a small table for per-shape/per-lane rows; bars use the group's colour at
     two opacities, never phase colours (those mean phases elsewhere in the app);
   - **What to do**: the advice paragraph;
   - **Where**: the evidence table, three rows visible, "Show all N"; columns Session · Agent ·
     When · Duration · Tokens; the row is a button that opens `#session/<id>?focus=<a>-<b>` and
     the timeline calls `focusInterval` (new hash parameter in `route()`); the session title is
     clipped with a title tooltip;
   - footer line in muted text: conventions ("long break = 4 h"), "no data in 2 sessions
     (CLI < 0.153)", "How this is computed" link.
6. **Page footer** as elsewhere ("Local files only. Nothing leaves this machine.").

**States.** Loading: skeleton report bar and three skeleton cards (no layout shift when data
arrives). Empty period: "No closed sessions between 15 Aug and 14 Sep 2026 in this project.
Change the period or the project." Fewer than 3 sessions: a note above the groups "Only 2 closed
sessions in this period. Cross-session findings need 3 or more. Showing the findings of each
session." with per-session sections; projects with 3+ sessions listed as links. Analysis running:
the progress bar in the report bar; cards render from the sessions that already have facts and
the strip says "based on 9 of 12 sessions". Error: inline message in the report bar plus the
usual toast; the last good report stays on screen.

**Interaction and accessibility.** Every control is keyboard-reachable in reading order; group
headers are buttons with `aria-expanded`; evidence rows are buttons; tooltips only repeat what
is visible; focus ring as elsewhere; live region announces "Report regenerated for …". Both
themes (the viewer's `data-theme` and system preference); phone width (~400 px): report bar
wraps, tiles stack two per row, cards are full width, evidence tables scroll horizontally inside
their own container, nothing else scrolls sideways. **Print** (`@media print`): all groups
expanded, controls hidden, the report indicator printed as the header, evidence tables complete —
the page is a report people will print or save as PDF.

**Copy.** Every visible string is in `INSIGHT_TEXT` in `insights.js`, reviewed against rule 10
with a checklist in the PR: sentence length, word list, units, no idiom, one term explained once.
Group names, card titles and the six control labels are fixed in this spec (§5.1, §5.3–5.4).

**Files.** `cmd/todobem/web/insights.js` (new; `app.js` must not grow), one statement per
line; `index.html` (nav item, page container), `app.css` (report bar, group header, card parts,
print rules); `app_test.js` cases: period parsing and labels (7d/30d/90d/all/custom/session,
inclusive dates, viewer time zone), group ordering on both axes, "Not measured" last, the
staleness chip, the < 3 sessions fallback, live exclusion, denominator rendering, the focus hash,
"no evidence, no card", plain-English checks that can be automated (max sentence length, no
banned words, every number followed by a unit).

---

## 8. An LLM in the loop — considered, rejected for the product, handed to the user

Three jobs were candidates for a cheap model through an installed agent CLI (`codex exec`,
`claude -p`): labelling a recurring failure's root cause from stderr tails, clustering unknown
command heads into overlay rules, and phrasing advice. The review's assessment, accepted: the
label is a closed enum that changes no user action; the phrasing rewrites text; only clustering
has value and it is a one-time task. Against that, the product would lose its most distinctive
claim — "nothing leaves the machine" becomes "derived session text leaves the machine" — and the
stderr tails are exactly where injected text lives (bounded blast radius, but not zero). The
redaction, budget, logging, temp-file, schema and result-cache machinery would exist to serve one
job.

What ships instead: the D13 card's **Copy as prompt** button produces a self-contained prompt —
the unknown heads with counts and three sample commands each (secret shapes redacted, home paths
normalized) plus the overlay format — that the user pastes into their own agent session at their
own discretion; the answer is a `rules.json` snippet they review and save. The product itself
makes no call. Rule 8 stays as written. Revisit only if users ask for in-app clustering after
using the prompt.

---

## 9. Implementation plan

| phase | scope | files | gate |
|---|---|---|---|
| **P0 · tokens and failures in the model** | §4: delta-reconstructed `Lane.Tokens`/`Turn.Tokens`, `Responses`, `First`, `ContextPeak`, `RootTurn`, compaction context/re-read, `CacheWrite`; verdict-only `Totals.Failed`; `Lane.Active` doc; `cacheVersion` 4, fingerprint schema 3; SCHEMA.md; dump prints per-turn tokens and the reconstruction check | `internal/codex/tokens.go` (new, extracted), `lane.go`, `model.go`, `derive.go`, `lane_test.go` fixtures (`token_count` repeats, a reset, zero records, a `token_usage_record`), `derive_test.go` (`Σ turn == lane`; probe misses excluded from `Failed`), `classify_test.go`, `SCHEMA.md`, `cmd/dump` | full gate; §4 validation on 3 sessions; metrics tile unchanged in meaning |
| **P0.5 · prototype before any API** | `cmd/dump -insights <id>` prints per-rule exposure for one session; run over 20 sessions of the largest project | `internal/insights/facts.go`, `detect_*.go` for D7 (shape keys), D11, D4, T1 only; `cmd/dump` | three checks: D7 by shape yields a non-empty recurring-failure list; failure counts no longer move with search no-match exits; reconstructed token deltas match `total_token_usage` per lane. **Stop and rethink if the first fails.** |
| **P1 · report + API** | `report.go` (period selection, groups, both orderings, top findings, staleness), `scan.go` (dedicated parse path, pool, cancel), facts store + report store + prune, routes, `/api/insights/rules` | `internal/insights/*`, `internal/store/facts.go`, `internal/store/reports.go`, `internal/server/insights.go`, tests per detector (positive + negative synthetic fixture), period-selection tests (boundaries, time zone, live excluded, one session), golden report JSON | full gate; memory check: a full scan of the largest project keeps RSS flat (models dropped after extraction) |
| **P2 · page** | report bar (period select, custom dates, session picker, project select, indicator chip, stale/analyze/progress/cancel, axis switch, regenerate), summary strip, top findings, groups, cards, evidence focus, states, print, session card, guide section, `INSIGHT_TEXT` | `web/insights.js` (new), `index.html`, `app.css`, `route()`, `app_test.js` | full gate + real browser pass: both themes, phone width, every state in §7, keyboard-only walk-through, print preview; the plain-English checklist on every string |
| **P3 · the rest of v1** | D1, D2/D2b, D3, D9, D12, D13 (+ Copy as prompt), D14, D15, M1, T2, T3, T6, T7 — each: detector + fixtures + card text; wording pass | as above | per detector: the full gate and a false-positive review on this corpus |
| **P4 · validation** | independent recomputation of D2, D4, D11, T1 exposures on 3 real sessions (local report, gitignored); `VALIDATION-PROTOCOL.md` addendum "Insights"; a read-through of every card by a non-native reader with the checklist | docs | discrepancies zero or explained; wording issues fixed |
| **v1.1** | D5, D6, D8, D10, T4, T5; estimates one at a time (§5.5) | — | separate review each |

Definition of done for the feature: CLAUDE.md gates; `docs/DESIGN.md` §2b "Insights" (this
spec condensed: groups, detectors, report and period semantics, no-estimate policy, scan design,
the rejected LLM step, the plain-English rule) and `docs/SCHEMA.md` (tokens, facts, report JSON);
README "What you see" gets an Insights bullet; a "Noticed, not fixed" list.

---

## 10. Noticed while writing this spec (defects in the current product; fixed in P0)

* **`Lane.Tokens` under-reports after a counter reset** (a resumed thread restarts
  `total_token_usage`): 5 of 200 files; S1's root lane shows 474 k for 130 M. Fix: delta
  reconstruction with reset baselines. Regression: a lane fixture with a reset.
* **`Totals.Failed` and every "failed" filter count query misses** (`search` exit 1 = no match,
  `read`/`list_files` exit 2 = wrong path; 208 of 380 failed ops in one project). Fix: verdict
  failures only through one predicate, `Operation.Failure()` with `QueryMiss` (§4; in progress
  in the working tree). Regression: a classify/derive case each.
* **`SCHEMA.md` documents `Lane.Active` wrongly** (says parent `SubAgentActivity` events; the
  code builds it from the lane's own turns and nothing sets it otherwise). Fix: the doc.
* `model_context_window` (258 k) is smaller than the largest `input_tokens` observed (440 k);
  the field's meaning is unclear — not shown as a cap, not used.
* `store` has no delete path; 7 of 9 cache files on this machine point at rollouts outside the
  current `-codex` home (the cache is keyed by session id only, not by home; correctness holds
  because the fingerprint carries file paths, growth does not). Fix: prune (P1).

---

## 11. Decisions needed (in order)

* **D-1 · No LLM step in the product; D13 ships a copyable prompt instead** (§8). Recommended,
  and it keeps CLAUDE.md rule 8 untouched. The user's brief allowed an in-product cheap-model
  call; this spec argues it does not earn its cost — the user's call.
* **D-2 · No estimates in v1** (§5.5). Cards show exposure, distribution, evidence and the
  denominator; "you would save X" comes back one estimate at a time with its own validation. The
  user's examples ("save 40 %", "5× faster") are the target *format* — this draft says the
  numbers the corpus can defend look like "6h41m of 24h14m in 4 of 7 sessions".
* **D-3 · Scan policy.** Auto-analyze only sessions with a model cache; parse the rest on an
  explicit Analyze click, in the background, cancellable (recommended).
* **D-4 · `Lane.Tokens` semantics change** (delta reconstruction instead of last cumulative) and
  **verdict-only `failed_ops`** — two displayed numbers change; both are defect fixes; schema
  bump. Recommended.
* **D-5 · Default project** = the project of the last opened session, per-session fallback under
  three closed sessions, live excluded by default. Recommended.
* **D-6 · Conventions** `long_break_h` = 4 and the cache buckets 5/15/60/240 min, printed on the
  cards, overlay-configurable. Confirm.
* **D-7 · Cache growth.** A full scan writes ~100–250 MB of derived data under
  `~/.todobem/cache`; add `-prune` and an age limit (recommended) or cap the facts-only path.
* **D-8 · Period semantics.** Default **last 30 days**; a session is in the period when its
  last activity lies inside it (a session started 40 days ago that ended 5 days ago is in);
  custom dates inclusive, evaluated in the viewer's time zone; live sessions never count.
  Alternative: membership by start time. Recommended: last activity — "sessions not older than a
  month" reads as "recently active".

---

## 12. Review record (pragmatic agent, 2026-09-14) — verdict "rework, targeted"

Taken in draft 2: (1) tokens by delta of `total_token_usage`, reset = fresh baseline — verified
here (naive sum 1.17× on S5's root, delta = harness exactly); (2) probe misses excluded from
failure counts, a failed-edit detector added (D15); (3) cross-session key = command shape, retry
grouping stays exact; (4) estimates cut from v1, ranking axis named instead of "billable";
(5) D3 reframed as "turns you stopped", causal sentence dropped; (6) scanner gets its own parse
path that never touches the server pools — **corrected**: the server mutex is not held across a
parse (`codex.Open` is struct construction, `Refresh` runs unlocked), the pool churn is the real
problem; (7) rank by absolute exposure, denominators printed, D11 split into a root partition
share and an all-lane parallel number; (8) "N sessions in scope" first, < 3 sessions fallback,
live excluded, "no data" separated from "did not happen"; (9) the analyst layer dropped, rule 8
untouched, Copy-as-prompt on D13; (10) `token_usage_record` is per response — last record of a
turn is the turn total; `root_turn_id` adopted as the literal sub-agent → root-turn link;
(11) facts cache kept with corrected costs (4–130 ms per model load, ~1 ms per facts file),
growth stated, prune added; (12) the D13 card states that a rule change re-analyzes the scope,
rescan incremental and cancellable; (13) `Lane.Active` doc fixed, `Turn.First` guards (zero /
repeat → no data), compaction order resolved (last non-zero before, first non-zero after; median
218 k on 62 checked), T6 ranked continuously. Added on the review's "what the corpus supports"
list: M1 (model time by model/effort/stage) and D15 (failed edits). Plan reordered: P0 → P0.5
prototype in `cmd/dump` over 20 sessions of the largest project → P1 with four detectors → P2
page → the rest.

Added in draft 3 (user requirements, not reviewed by the agent): seven groups with plain-English
names and questions (§5.1); the period report — default last 30 days, 7 / 90 days, all time,
custom dates, one session; the report indicator, staleness and regeneration (§6, §7); the
persisted report per parameters; the UI/UX specification (§7); rule 10 (plain English) and the
`INSIGHT_TEXT` review mechanism; card texts rewritten to the four-part plain-English form;
decision D-8. The `pragmatic` review of these additions is part of P1's gate.

Kept as designed: the deterministic core over `Facts`; evidence mandatory; no built-in price
table; the `Lane.Tokens` defect worth a schema bump; the scan-cost estimate (~100 MB/s, ~65 s
for 6.5 GB); the honest "Noticed" section.
