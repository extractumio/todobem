# Metrics page — specification (v1)

Status: draft for the owner's review, 2026-09-17. Written against `main` at `2f72db5`
(`insights.FactsVersion` 12). Inputs: the owner's research write-up on SDLC metrics for agent
sessions, a `pragmatic` critique of the first draft (verdicts folded in below), the code in
`internal/insights`, `internal/model`, `internal/server` and `cmd/todobem/web`, and counts taken
from the facts sidecars on the author's machine (239 sessions, 22 projects; counts only, no
session content read). Nothing here is implemented yet.

---

## 1. Purpose and the loop

The page answers one question per project: **is the harness getting better or worse, on the
numbers the log can support, since I changed something?** It closes a loop the product already
has three quarters of:

1. **Session page** — one session as recorded: lanes, phases, stages, the messages verbatim,
   and its own insight cards (`renderSessionInsights`). *What happened.*
2. **Insights** — the period report: where time and tokens go, with evidence and a "what to do"
   bound to each rule. *What to change in the harness.*
3. **Metrics** (this page) — the period's numbers per project, each with its denominator, each
   linked to the insight card that explains it. *Where we stand.*
4. **Freeze as baseline** — a marker: these sessions, this meter, this date, a note on what is
   about to change and which insight cards it acts on. *The before.*
5. Apply the change (a stop hook, a pre-push hook, a review skill, a CLAUDE.md rule, a model or
   effort switch), keep working.
6. **Since the baseline** — the same numbers over the sessions started after the marker, side
   by side with the frozen set, both recomputed with today's meter. *Better, worse, or not
   enough sessions to say.*

The page never says why a number moved. It shows the two sides with their n, the source and CLI
mix of each, and the insight card whose evidence is the place to look.

## 2. What the page is not

Refused, with the reason, so the page is not asked for them later:

- **No throughput or productivity ratio** (hours per push, tokens per push, "changes
  delivered"). The log records no unit of delivered value: a `git push` is an attempt by
  whoever the agent was, one push is one or forty commits, a human push from a terminal is
  invisible, and sessions with no push still carry hours. The page says **spend and gates,
  not throughput**.
- **No composite score, index or grade.** One number invites the goal-achievement reading
  product rule 2 forbids (`CLAUDE.md`), and the SPACE framework's one durable finding is that
  activity does not reduce to a single productivity number.
- **No statistics that manufacture a signal from a handful of points**: no EWMA, no control
  charts, no standardized rates with baseline weights, no stage transition matrix (stage
  assignment is composition-based, so transitions are rule artifacts), no stage × phase
  matrix (descriptive, no action). Blocks of 10 sessions and two printed sides are the whole
  method.
- **No currency in v1.** Tokens are the honest unit. A user-entered price table per model in
  Settings (never fetched — product rule 8) is a follow-up if the owner wants a currency line.
- **No new telemetry and no `FactsVersion` bump in v1.** Every metric below reads fields
  `Facts` already carries; the page works on today's sidecars without a re-analyze.

## 3. Ground rules (product rules 1–8 applied to metrics)

1. **Literal only.** Every metric is a count, a sum or a ratio of fields in `insights.Facts`
   (§4). No duration threshold that explains anything, no similarity, no model.
2. **Printed denominators.** A ratio is shown as `num / den = x %`, never as `x %` alone. A
   zero denominator prints "n/a — nothing to measure", never 0 %.
3. **Three states, never merged**: *did not happen*, *could not be measured* (the detector's
   no-data rule), *does not apply* (the rule's precondition is absent). Each metric names its
   own no-data and not-applicable rules (§5) and the page prints all three counts.
4. **Σnum / Σden across sessions**, never the mean of per-session percentages. Medians are
   taken over the pooled observations of a side, never averaged across sessions.
5. **The time denominator is the root lane's in-turn time** (`Root.InTurnMs`). Elapsed time is
   printed once as context with its `wait_user` share (median 59 % of elapsed on the corpus;
   sessions span days) and is never a denominator.
6. **Sub-agent time never joins a root denominator** (product rule 6). Windows, waits and
   compactions are root-lane where the denominator is root time; tokens are split root /
   sub-agents wherever they are summed.
7. **A meter change is not a harness change.** A different `insights.FactsVersion`,
   `classify.RulesFingerprint()` (the user overlay is part of it) or `metrics.Version` between a
   baseline and now is printed as such; both sides are always recomputed with today's meter
   from their session ids (§7). A CLI release between the sides is printed through the CLI mix.
8. **Every ratio is keyed by source** (Codex / Claude Code) on both sides. The source mix is the
   first confounder a before/after hits (on the corpus the D17 rate differed fourfold between
   harnesses; the source mix of consecutive blocks of 10 flipped from 9:1 to 4:6).
9. **Direction is declared per metric**, and only three are allowed: `up` (a gate: more is what
   the hook is for), `down` (friction), `neutral` (spend, adoption, the user's own pace,
   measurements). Colour and the words "up / down" appear only on directional metrics; a neutral
   metric prints the delta in grey.
10. **Plain English**, every visible string in `METRIC_TEXT`, under the same sentence-length
    test as `INSIGHT_TEXT`.

## 4. What the data supports

### 4.1 Fields read (all in `internal/insights/facts.go` and `facts_delivery.go`, v12)

| area | fields |
|---|---|
| session | `ID, Source, CWD, CLI, Started, Ended, Live` |
| root totals | `Root.ElapsedMs, InTurnMs, ByPhase, ByLifecycle, Tokens, AllTokens, Turns, Aborted, UserMessages` |
| turns | `Turns[].Lane, Status, Trigger, Question, Responses, Tokens, Model, Effort` |
| delivery walk | `Delivery.Changes, Verified, VerifiedBy, VerifiedKinds, Tests, TestsFailed, HookOps, LastVerdictFailed, ReviewedAt, ChangesAfterReview, BlindAfterLastChangeMs, AmbiguousTestAfterLastChange, Pushes[].{Verified, BlindMs, AmbiguousTest}, EditTurns, EditTurnsUnverified, EditTurnsAmbiguous` |
| retries | `Groups[].Windows[].{Lane, Start, End}` |
| waits | `Waits[].{Kind, Start, End, SoloMs}` |
| compactions | `Compactions[].{Lane, InChangeWindow}` |
| gaps | `Gaps[].{Start, End, AfterQuestion, NextTurn, NextTrigger}` |
| agents | `Agents[].{Kind, Tokens, First}` |
| tool calls | `ToolCalls[].{Phase, Sub, Calls, Failed}` |

Not read, on purpose: `LongOps` (the 40 longest, lossy), `UnknownOps` (30), `Blind` (5),
`Cells` (tokens allocated pro rata by stage time — the weakest number in Facts; it stays on
the M1 / T3 cards), `Groups[].RoleMs` (summed over every lane).

### 4.2 Corpus grounding (author's machine, 2026-09-17, counts only)

- 239 analyzed sessions, 22 projects, Claude Code 140 / Codex 92. The largest project: 74
  sessions over six ISO weeks (1, 10, 18, 5, 29, 11 per week); the second: 38 in two weeks;
  most projects under 20 sessions in total. **Weekly points are hollow for every project but
  one; blocks of 10 sessions are the honest x-axis.**
- Root elapsed p50 128 min, p90 2,016 min; `wait_user` share of elapsed p50 59 %, p90 98 %;
  in-turn share p50 36 %.
- `unknown + no_telemetry` share of elapsed p50 0.0 %, p90 0.3 % — coverage is a footnote.
- Sessions with a change op 166, verified at end 77 of those; edit turns 1,277, unverified 831;
  pushes 210, unverified 101; test runs 7,654, failed 930; sessions that ran stop hooks 51,
  verified by one 0; retry groups 910, with a failure 485; aborted root turns 88 of ~1,900;
  questions 69; sessions with sub-agents 138; with compactions 70; review runs recorded in
  14–18 sessions.
- Turns with a usage record: Claude Code 1,412 of 1,471, Codex 3,039 of 3,340.
- **Stage coverage:** `plan` 0 turns in every sidecar, `requirements` / `design` 0, `operate`
  7, `review` 370 turns (concentrated in 18 sessions). Both adapters do set plan mode
  (`claude/lane.go`, `codex/lane.go`), and the 2026-09-16 notes counted 52 plan-stage
  sessions among 458 cached models — **none of them has a sidecar**. The stage bar must print
  the pending count per side, or a baseline frozen today shows planning "appearing" later as
  movement.
- The six candidate tiles per block of 10 sessions on the largest project swing widely (edit
  turns verified 9 %–62 %, pushes verified 36 %–67 %, retry-window share 0.2 %–38 %) while the
  source mix flips between blocks. Verified-at-end has 4–7 measurable sessions per block; edit
  turns 11–189; pushes 6–42. Hence edit turns verified is the headline gate and every tile
  carries its n. The second project never pushes from the agent: pushes verified is n/a there,
  not 0 %.

## 5. Metric catalogue v1

Notation: `num / den` over the sessions of one side (§6); *NA* = the session is not applicable
(outside den and no-data); *no data* = the session (or event) could not be measured. *Unit*
names what is counted. *Insight* is the card the row links to. Every ratio is also split by
source.

### 5.1 Gates — what left the machine, checked (`direction: up`)

| id | title (on the page) | num / den | unit | NA / no data | insight |
|---|---|---|---|---|---|
| `verified_end` | Sessions that ended verified | sessions with `Delivery.Verified` / sessions with `Changes > 0 && BlindAfterLastChangeMs == 0 && !AmbiguousTestAfterLastChange` | session | NA: `Changes == 0`; no data: `BlindAfterLastChangeMs > 0 || AmbiguousTestAfterLastChange` | D17 |
| `push_verified` | Pushes with a passing test since the last edit | `Pushes[].Verified` / pushes with `BlindMs == 0 && !AmbiguousTest` | push | NA: no push after a change; no data: `BlindMs > 0 || AmbiguousTest` | D25 |
| `push_verified_sessions` | Sessions whose every measurable push was verified | sessions with ≥ 1 measurable push, all verified / sessions with ≥ 1 measurable push | session | as above | D25 |
| `edit_turn_verified` | Edit turns verified inside the turn (any lane) | `EditTurns − EditTurnsUnverified − EditTurnsAmbiguous` / `EditTurns − EditTurnsAmbiguous` — turns with a change op **on any lane**, a sub-agent's edit turn included (the delivery walk's session scope, consistent with D17); the tile's definition sentence says so, since it sits next to root-only numbers | turn | NA: `EditTurns == 0`; no data: `EditTurnsAmbiguous` | D17 (stat) |
| `hook_verified` | Hook sessions verified first by the hook | sessions with `Verified && VerifiedBy == "hook"` / `verified_end`'s den ∩ `HookOps > 0` | session | NA: no hook ran, or `verified_end` NA / no data; **Codex sessions cannot carry the signal** (no hook op exists in `codex/lane.go`): not measurable, never in the all-sources ratio | D17 (stat) |
| `review_covered` | Reviews that covered the last change | sessions with `ReviewedAt > 0 && ChangesAfterReview == 0` / sessions with `ReviewedAt > 0` | session | NA: no review run | D24 |

Side stats printed under the rows, no direction: `verified_static_only` (verified by a lint,
type check or syntax check alone), `TestsFailed / Tests` as "failed test runs" (a red-green
loop is normal; the ratio is information, not a gate), sessions whose last test failed
(`LastVerdictFailed`).

Conventions printed on the rows: a rejected push and its retry are two push attempts
(`PushFacts` records no status); CI after a push is not in the log; an edit to any file counts,
docs included; "verified" means a test op completed without failing, or a test-running stop
hook without a hook error — nothing about what it covered; `VerifiedBy` names the **first**
verification after the last change, so a hook test that passed after the agent's own test
counts as "agent" (hence "verified first by the hook").

### 5.2 Adoption — did the process step happen (`direction: neutral`)

| id | title | num / den | unit | NA | insight |
|---|---|---|---|---|---|
| `review_run` | Sessions with a review run | sessions with `ReviewedAt > 0` / sessions with `Changes > 0` | session | NA: `Changes == 0` | D24 |
| `hook_run` | Sessions that ran stop hooks | sessions with `HookOps > 0` / Claude Code sessions | session | Codex sessions cannot carry the signal (no hook op); the row is Claude Code only | D17 (stat) |

Neutral because more review runs is a policy, not a gate; the rows exist so that "I added a
review skill / a stop hook" has a meter. The baseline note (§7) is where the owner says which
way they expect it to move.

### 5.3 Friction — time and turns lost inside turns (`direction: down`)

| id | title | num / den | unit | NA / no data | insight |
|---|---|---|---|---|---|
| `retry_window_share` | Time inside failure-to-retry windows | union of root-lane windows (`Groups[].Windows[]` with `Lane == 0`), clipped to the root's turns (`Turns[]` with `Lane == 0`), summed / `Root.InTurnMs` | ms | NA: `InTurnMs == 0` | D7 |
| `retry_sessions` | Sessions with a failure-to-retry window on the main thread | sessions with ≥ 1 root window / sessions | session | — | D7 |
| `stopped_turns` | Turns you stopped | `Root.Aborted` / `Root.Turns` | turn | NA: `Turns == 0` | D3 |
| `compactions_per_100` | Compactions per 100 model calls | 100 × root compactions (`Compactions[].Lane == 0`) / Σ `Turns[Lane == 0].Responses` | call | NA: no counted model call on the root (`Responses == 0` everywhere) | D11 |
| `compactions_in_window` | Compactions between two edits of one turn | `Compactions[].InChangeWindow` / root compactions | compaction | NA: no compaction | D11 (stat) |
| `serial_waits` | Sub-agent waits with at most one sub-agent working | Σ `Waits[].SoloMs` / Σ `(End − Start)` over `Waits[]` with `Kind ∈ {agent, wait}` | ms | NA: fewer than two sub-agents (`len(Agents) < 3`, D4's rule) | D4 |

The union + clip rule is not optional: on the largest project a plain sum of root windows in one
block of 10 sessions reached 107 % of in-turn time (windows of different groups overlap, and a
window that spans the user's overnight gap is mostly `wait_user`); the union clipped to turns
gives 6.8 % for the same block, and 38 % for the block that genuinely had a bad week.

### 5.4 Spend — what it cost (`direction: neutral`, printed by source and, on expand, by model)

| id | title | value | unit | note |
|---|---|---|---|---|
| `hours_in_turn` | Agent hours in turns | Σ `Root.InTurnMs` | ms | the main thread only; sub-agents run inside it |
| `tokens` | Tokens (uncached input + output) | Σ `billable(Root.AllTokens)`, split `Agents[0].Tokens` vs the rest | tokens | the same `billable()` as the Insights tokens axis; never summed across sources on one line |
| `tokens_per_hour` | Tokens per agent hour | `tokens / hours_in_turn` | tokens/h | the burn rate; moves with context hygiene (T1, T2, D11 actions) |
| `cache_share` | Input served from the cache | Σ `AllTokens.Cached` / Σ `AllTokens.Input` | tokens | `Cached` is part of `Input` (never add them) |
| `spawn_share` | Sub-agent tokens spent on starting | Σ `Agents[1:].First.Total` / Σ `Agents[1:].Tokens.Total` | tokens | NA: no sub-agent; no data: `First == nil` |

Per model on expand: the `Turns[].Model / Effort` of the side's root turns with `Tokens != nil`
(turn-level, so a model switched mid-session lands where it ran); a turn without a model is its
own row "not recorded".

### 5.5 The human side — information, never ranked (`direction: neutral`)

| id | title | value | insight |
|---|---|---|---|
| `questions_per_10` | Questions to you per 10 main-thread turns | 10 × root turns with `Turns[].Question` (`Lane == 0`) / Σ `Root.Turns` — not `Root.Questions`, which counts every lane's questions (`Totals.Questions` has no root guard) | D1 |
| `answer_wait_p50` | Median wait for your answer | p50 of `Gaps[].End − Start` with `AfterQuestion && NextTurn != ""`, pooled | D1 |
| `reply_p50` | Median time to your reply | p50 of gaps with `NextTurn != "" && NextTrigger != "system"`, `MinReplyMs ≤ gap < LongBreakMs` (the D2 conventions, printed) | D2 |
| `wait_user_share` | Waiting on you, share of elapsed | Σ `Root.ByPhase[wait_user]` / Σ `Root.ElapsedMs` | D2 · D2b |
| `user_messages` | Your messages | Σ `Root.UserMessages` (also on the n strip) | — |

A question can be the right move and the reply pace is the user's own (decision record
2026-09-16); these rows exist so a change in the agent's behaviour toward the user is visible,
not judged.

### 5.6 Failed steps — information (`direction: neutral`)

| id | title | value | insight |
|---|---|---|---|
| `failed_steps` | Failed steps per 100 main-thread tool calls, by subgroup | per `(Phase, Sub)` row of `ToolCalls[]`: 100 × Σ `Failed` / Σ `Calls` (query misses are not failures) | D15 · D16 |

Moves with the model and the CLI version more than with the harness; shown by subgroup (edit,
shell, vcs, hosting, network, mcp) so a jump in one kind is readable.

### 5.7 Coverage — the honesty footnote

| id | title | value |
|---|---|---|
| `coverage_unknown` | Time no rule matched, share of in-turn | Σ `Root.ByPhase[unknown]` / Σ `Root.InTurnMs` |
| `coverage_blind` | Time with no telemetry, share of elapsed | Σ `Root.ByPhase[no_telemetry]` / Σ `Root.ElapsedMs` — its own ratio: an orphaned turn's no-telemetry tail lies outside `InTurnMs` (`derive.go`), so one combined share could pass 100 % |
| `coverage_tokens` | Main-thread turns with a usage record | root turns with `Tokens != nil` / root turns |
| `coverage_gates` | Measurable share of the gates | for `verified_end` and `push_verified`: den / (den + no data) |

### 5.8 The stage bar (`direction: none`, never ranked)

One horizontal bar per side over Σ `Root.ByLifecycle` **excluding** `wait_user` and `idle`:
the eight work stages in lifecycle order (`plan, requirements, design, implement, review, test,
release, operate`), then `llm` ("model output that no tool call followed"), then the rest folded
into "other in turn" (`wait_worker, compaction, unknown, no_telemetry`). Its denominator is
printed (it is close to, not equal to, in-turn time: a question held open inside a turn is
`wait_user`). Stages with 0 ms are listed by name under the bar as "not recorded on either
side" so their absence is visible, and the pending count of the side is printed next to it
(§4.2, the plan-coverage gap).

### 5.9 The n strip (printed for every side, top of the page)

Closed sessions, root turns, your messages, agent hours in turns, elapsed with its `wait_user`
share, source mix (`codex n / claude n`), CLI mix (`Scope.CLIs`-style: version → sessions),
pending sessions (no facts yet), live sessions left out, and — on the "since" side —
straddlers (§7.3).

### 5.10 Headline tiles (six)

`edit_turn_verified`, `verified_end`, `push_verified`, `retry_window_share`, `hours_in_turn`,
`tokens`. A tile shows: title, the value with `num / den`, the other side's value, the delta
(pp for ratios; absolute and % for hours and tokens), the n of each side, the source split on
hover. Chosen for size of n (edit turns first) and for having a harness action (a Stop hook, a
PreToolUse hook on `git push`, D7's evidence) or being spend.

## 6. Aggregation and comparison semantics

- A **side** is a set of closed sessions (`Facts.Live == false`, or live included on request
  as Insights does), filtered by project (`CWD` string equality, as Insights today; §12 lists
  the alias map as a follow-up) and by sources.
- **Values:** for a ratio, `Value{Num, Den, NoData, NA, Sessions}` with `Sessions` the sessions
  that contributed to `Den`; `BySource map[source]Value`. For a duration or token sum,
  `Value{Sum, Sessions}`. For a median, the pooled observations are sorted and the lower median
  taken (`v[(n−1)/2]`, deterministic; printed as "median of n").
- **Delta** (`since − baseline`): ratios in percentage points, sums as absolute and relative.
  Sign and colour only for `up` / `down` metrics. A delta is not printed when either side's
  denominator is 0 (the row says which side had nothing to measure).
- **Small samples:** the whole side falls back like Insights when it has fewer than 3 closed
  sessions (`fewer_than_3_sessions`: the numbers are listed, no comparison). Per row, a ratio
  whose denominator is under **10 observations** is drawn muted and marked "small sample" — a
  display convention printed in the legend, explaining nothing.
- **Spread reference:** when the baseline side has at least two full blocks of 10 (§8), the
  row prints the metric's minimum and maximum across those blocks ("blocks of 10 in the
  baseline ranged 21 %–62 %"). A literal range, not a confidence interval; it is what stops a
  4-pp delta being read as an effect.
- **Direction words:** "up 12 pp" / "down 3 pp" on directional metrics; "changed by" on neutral
  ones. The page never prints "better" or "worse" as a verdict; the legend says which direction
  each group's hook is for.

## 7. Baselines

### 7.1 What a baseline is

A **marker**, not a value store: the sessions that were on the page when the owner pressed
Freeze, the meter they were measured with, the moment, and the owner's own words on what is
about to change. Values are stored too, but only to show the "as measured then" column when the
meter still matches; comparisons always recompute (§7.4).

### 7.2 Stored shape

`~/.todobem/baselines.json` — one JSON array, written atomically (temp file + rename, as
`settings.Save`), read at every request (small; a mtime check is enough), the only new write
under `~/.todobem/`.

```
Baseline {
  id           string   // 12 hex chars from crypto/rand
  cwd          string   // the project as filtered
  sources      []string // sources in scope ("" = all)
  created_at   int64    // ms; the "since" boundary
  name         string   // default: the date; editable later (v1.1)
  note         string   // what is about to change, verbatim (Markdown-rendered, escaped)
  insights     []string // rule ids the owner is acting on (D17, D25, D7 …)
  session_ids  []string // the frozen side
  period       Period   // the filter the ids came from (for the label)
  facts_version   int   // insights.FactsVersion at freeze
  rules_fp        string // classify.RulesFingerprint() at freeze
  metrics_version int   // metrics.Version at freeze
  cli_mix      map[string]int
  source_mix   map[string]int
  values       map[string]Value // §6 shape, as measured at freeze
}
```

Limits: 50 baselines per project, oldest dropped only on explicit delete (the page lists them;
the server refuses the 51st with a message).

### 7.3 The two sides

- **Baseline side:** `session_ids`, re-selected from the index (an id whose file is gone is
  counted as "missing" and printed).
- **Since side:** sessions of the same `cwd` and `sources` with `Facts.Started > created_at`
  (a hook or CLAUDE.md change takes effect at session start), closed, optionally narrowed by
  the page's period. **Straddlers** — sessions started before `created_at` and still running or
  ended after it — are excluded and their count printed.
- Pending sessions on either side (no facts yet) are listed with the same **Analyze** button
  as Insights; the scan reuses `insights.Scanner` with a custom period.

### 7.4 Meter matching

At every request the server compares the baseline's `facts_version`, `rules_fp` and
`metrics_version` with today's. Match → the "as measured then" column is shown next to the
recomputed baseline column (they should be equal; a difference is a bug the page says out
loud). Mismatch → the stored values are hidden behind a chip "meter changed since the freeze:
rules / facts / metrics" and both sides are recomputed with today's meter, which is the only
honest comparison. No "recompute the baseline" button exists because recomputation is the
default.

### 7.5 Where Freeze lives

- **Metrics page** (primary): a Freeze button in the bar when a project is selected and the
  side has ≥ 3 closed sessions; a dialog with the note field and the insight rule ids ticked
  from the current Insights report's check and exposure cards (titles shown, ids stored).
- **Insights page**: the same button in the report bar ("Freeze these sessions as a baseline");
  it opens the same dialog and lands on `#metrics?baseline=<id>`.
- Deleting a baseline asks once; nothing else is destructive.

## 8. Dynamics

- **Blocks of 10 closed sessions by `Facts.Started`**, within the project + sources (the period
  filter narrows the range). The x-axis is the block's date range (unequal widths are fine); the
  last partial block is drawn hollow and labelled with its n; a vertical marker per baseline
  at `created_at`. Every block carries its source mix on hover.
- One chart per metric **on expand** (a row click), not a wall of charts; the six tiles carry
  a sparkline of the same blocks. Ratios draw `num / den` per block; sums draw the sum; medians
  the pooled median per block.
- No weekly mode, no smoothing, no trend line. The reading is the two sides with their n; the
  chart is where the spread comes from.

## 9. Page design

Route `#metrics` (`#metrics?baseline=<id>` selects one); a nav item "Metrics" between Insights
and Settings, the mobile nav gets it too. Shares `filter.js` (period, project, sources); the
project select is required for the project view — "All projects" shows the projects table.

Layout, top to bottom:

1. **Page heading** — eyebrow "Development metrics on this machine", title "Metrics", subtitle
   "Spend and gates per project, before and after a change to your harness. Nothing here is a
   score."
2. **Bar** — the shared filter; a baseline select ("No baseline · This period" default, then the
   project's baselines newest first, with name and date); **Freeze as baseline**; Regenerate;
   the status row (sessions, generated, stale chip, pending + Analyze, live left out) as on
   Insights.
3. **Sides header** — two columns when a baseline is selected (Baseline: name, dates, its note
   rendered, the insight cards it acts on as chips linking to `#insights?card=<rule>`; Since:
   "sessions started after <date>"), one column otherwise ("This period"). Under each: the n
   strip (§5.9) and the meter chip when it changed (§7.4).
4. **Six tiles** (§5.10).
5. **Groups as rows** — Gates, Adoption, Friction, Spend, The human side, Failed steps,
   Coverage — each row: title, baseline value (`num / den`), since value, delta, n, no-data /
   NA counts, the source split (two small numbers), a "see the evidence" link to the insight
   card, and on expand: the blocks chart, the per-source table, the per-model table (Spend),
   the conventions the row relies on.
6. **Stage bar** (§5.8), one per side, legend shared with the timeline's stage colours.
7. **Legend and footer** — the direction rule per group, the small-sample convention, the
   union + clip rule for retry windows, "n/a" vs "no data" vs "did not apply".

**Projects table** (no project selected): one row per project with sessions in the period —
short path, closed sessions, agent hours, tokens, edit turns verified, sessions ended
verified, pushes verified, a "baselines: n" chip — sorted by sessions; a row click selects the
project. No deltas in this view.

**States:** no sessions in the period (the Insights empty state and projects hint); fewer than
3 closed sessions on a side (values listed, comparison withheld, the sentence says so);
pending sessions (Analyze); a baseline whose ids are partly missing (printed); meter changed
(chip); a project the baseline's sources no longer include (the sources are taken from the
baseline, the filter shows them).

**Text** — `METRIC_TEXT` in `metrics.js`: titles, one-sentence definitions ("Edit turns in
which a test passed after the turn's last edit, of all turns with an edit"), the direction
sentence per group, the conventions, the states. Same sentence-length test as
`insightsTextSamples`.

**Session page and Insights links:** the session page's insight block gets one line "Metrics
for this project →"; each Insights card whose rule backs a metric gets "Tracked on Metrics as
<title>" linking to `#metrics` with the row expanded; `#insights?card=<rule>` scrolls to and
opens the card (new, small: the route already accepts `#insights?`).

## 10. API (loopback, behind the auth gate like every `/api/*`)

| route | method | in | out |
|---|---|---|---|
| `/api/metrics/report` | GET / POST | `cwd, sources, period, from, to, include_live, baseline` (the Insights parameters plus a baseline id); `?probe=1` returns the sources hash + pending count as Insights does | `Report{generated_at, params, meter{facts_version, rules_fp, metrics_version}, baseline?{…, missing_ids, meter_match{facts, rules, metrics}}, sides{baseline?: Side, since|period: Side}, rows[]{id, group, title, direction, unit, values per side, delta, spread}, stage per side, blocks[], projects[]? (All projects), fallback}` |
| `/api/metrics/baselines` | GET | `cwd` | the project's baselines, newest first (values omitted) |
| `/api/metrics/baselines` | POST | `{cwd, sources, period, note, insights}` | the created baseline (the server resolves the session ids from the filter, computes the values, writes the file) |
| `/api/metrics/baselines/{id}` | DELETE | — | 204 |

Scanning pending sessions reuses `POST /api/insights/scan` with a custom period (the page
passes `from = created_at` for the since side). The server side of the report reuses
`insightsSvc.selectSessions` (facts from the same four places, never a parse on GET) and
`insights.Scanner`.

## 11. Code structure

```
internal/metrics/
  catalogue.go   Version const; Metric{ID, Group, Title, Unit, Direction, Insight}; Catalogue
  compute.go     Side(facts []insights.Facts, now) → SideValues (pure; Σnum/Σden, by source,
                 pooled medians, stage bar, n strip); union+clip helpers
  blocks.go      Blocks(facts, size=10) → []Block (by Started; last partial flagged)
  compare.go     Compare(baseline, since) → rows with deltas and spread
  baseline.go    Baseline type; Load/Save (atomic) on ~/.todobem/baselines.json; New (id, meter)
internal/server/metrics.go   the routes; reuses insightsSvc.selectSessions + scanner
cmd/todobem/web/metrics.js   the page + METRIC_TEXT (one statement per line)
cmd/todobem/web/app.css      the tiles, sides, blocks chart (SVG, no library)
cmd/todobem/web/index.html   nav items, script tag
cmd/todobem/web/app.js       route #metrics, go('metrics'); #insights?card=<rule>
cmd/todobem/web/insights.js  Freeze button, "Tracked on Metrics" line
docs/ARCHITECTURE.md         §8 rows, §10.4 Metrics, §12 "a new metric", §13 decision
CLAUDE.md                    one repository-map line for internal/metrics and metrics.js
```

`internal/metrics` imports `insights` (for `Facts` and `FactsVersion`) and `classify` (for
`RulesFingerprint`), never `server` or `source`. Nothing in `insights` changes except the two
UI hooks. No `FactsVersion` bump.

## 12. Tests, invariants, definition of done

Unit tests (`internal/metrics`), on synthetic `Facts` built in the test (fake paths, no
session content), one positive and one negative fixture per metric:

- `num ≤ den` for every ratio; `Den + NoData + NA == sessions` for session-unit ratios (and
  the analogue per event for push-unit ratios).
- The stage bar's shares sum to its printed denominator; the side's `hours_in_turn` equals
  Σ per-session `InTurnMs`.
- Union + clip: overlapping windows count once; a window across a `wait_user` gap contributes
  only its in-turn part; a sub-agent window never counts.
- Determinism: the same facts in a different order give the same `SideValues` (map iteration
  never leaks into a value).
- Blocks: 24 sessions → blocks of 10, 10, 4 with the last flagged partial; the block date
  range comes from `Started`.
- Baseline: round trip through the file; a corrupt file fails loudly (as `settings.Load`);
  meter mismatch flags each of the three parts separately; the 51st baseline is refused.
- Server: every route answers 401 without a session; `POST baselines` writes only under the
  configured `~/.todobem`.
- JS (`app_test.js`): the report query builder, `METRIC_TEXT` sentence lengths, the delta
  formatting (pp vs %), the small-sample mute rule.

Definition of done is `CLAUDE.md`'s: `gofmt`, `go vet`, `go test ./...`, `node --test`, the
real page in a browser (project view, projects table, freeze, since view, expand a row,
Analyze pending, the console clean), `./scripts/deploy.sh` to see it, `docs/ARCHITECTURE.md`
updated in the same change, the report with "Noticed, not fixed".

## 13. Effort and phasing

Estimate (one engineer, from the critique, agreed): compute + tests 1 d; baselines + API 1 d;
page with tiles, rows and one chart on expand 1.5–2 d; docs + browser verification 0.5 d →
**4–5 engineer-days for phases 1, 2 and 4**. Phase 3 (tile sparklines, the projects table,
the freeze dialog with card ticks, `#insights?card=` routing, the Insights and session-page
links, the mobile nav) is **another 1–1.5 d** on top; it is the first thing to drop if the
week is short. Phases, each shippable:

1. `internal/metrics` compute + blocks + tests; `cmd/dump -metrics <cwd>` prints a side (the
   validation step: the six tiles for the largest project before/after a real harness change
   the owner already made — if nothing moves or the after side is too small, the first metric
   set changes before the page is built).
2. Baselines + API + the page in "This period" and "since" modes, tiles and rows.
3. Blocks charts on expand, the projects table, the Insights and session-page links.
4. Docs, decision record, browser pass.

## 14. Deferred — each needs a decision, listed so it is not lost

| item | why deferred | decision needed |
|---|---|---|
| `PushFacts.Failed` (a rejected push counted apart) | a `FactsVersion` bump re-analyzes every session | bump together with the next Facts change |
| Distinct files changed per session | needs a bump; sed / formatter / mv change ops carry a command, not a path list; a normalizer with no action | whether a "size of change" number is wanted at all |
| Project alias map (worktrees, renames, symlinks split one repo into several `cwd`s and orphan a baseline) | same limitation as Insights today | a Settings section mapping paths → project; baselines keyed by the alias |
| Price table per model → currency | the owner's call; user-entered, never fetched | whether a currency line is wanted |
| Plan-stage coverage | 52 plan-stage sessions have models but no sidecars | run Analyze over the periods that hold them before any planning claim |
| Baseline name / note editing | cheap, v1.1 | — |
| PR created / merged counts | Release has no subgroup; `ToolCalls` cannot tell `pr create` from `pr merge`; not in Facts | a release subgroup row (a classifier change) |
| CI outcome after a push | not in the log (`gh pr checks` is a wait, no verdict recorded) | a source for CI results — another product |

## 15. Open questions for the owner

1. **Six tiles** — agree with `edit_turn_verified`, `verified_end`, `push_verified`,
   `retry_window_share`, `hours_in_turn`, `tokens`? (`stopped_turns` is the alternative to
   `retry_window_share` if "the user overrode the agent" matters more than retries.)
2. **The since side and the period filter** — narrow "since the baseline" by the page's period
   (default: no), or always everything after the freeze?
3. **Currency** — a price table in Settings (v1.1), or tokens only?
4. **Projects table** in v1, or project view only?
5. **The baseline dialog** — tick insight cards from the current report (proposed) or a free
   note only?

## Appendix A. Disposition of the research write-up

What the owner's write-up proposed, and what this spec does with it (the `pragmatic` verdicts
of 2026-09-17 folded in).

| proposal | disposition |
|---|---|
| Baseline as distributions and ratios, never one number; measurements / process indicators / completeness kept apart | **Kept** — §3, §5 (gates, adoption, friction, spend, information, coverage) |
| Σnum / Σden across sessions; never the mean of per-session %; never an average of p90s | **Kept** — §6 |
| Three times: elapsed, in-turn, agent-time; never add parallel lanes to root | **Kept** — §3.5, §3.6; agent-time is not shown (no denominator for it) |
| recorded / derived / allocated | **Kept as a rule, not a field**: allocated numbers (`Cells`) are not used at all |
| Meter change ≠ harness change; fixed baseline apart from the rolling view; "as measured then" vs "recomputed" | **Kept** — §7.4 (recompute is the default; "then" shown only on a meter match) |
| Coverage metrics (unknown, no-telemetry, token records, measurability) | **Kept as a footnote** — on the corpus unknown + no-telemetry is 0.0 % at the median |
| Verification indicators (D17, D25 event- and session-level, static-only share, last verdict failed, hook verification) | **Kept** — §5.1; "first-pass" and "delay to a passing check" deferred: they need verification episodes, a new fact |
| Failures and retries (recovery paths, union of retry windows, aborted turns) | **Kept** — §5.3; the union + clip rule adopted verbatim; "recovery time per command" deferred (needs the full op stream) |
| Tokens, cache share, context peak, compaction rate, sub-agent and first-call tokens | **Kept** in part — spend rows in §5.4; context-peak p90 stays on T2 (a per-turn measurement with no harness action beyond D11's) |
| Human interaction and delegation (waits by kind, questions, reply latency, serial sub-agents) | **Kept as information** — §5.5, `serial_waits` in §5.3 |
| Stage × phase matrix `T[k,p]` | **Cut** — descriptive, no action; the stage bar (§5.8) is the adoption meter |
| Stage visits and the transition matrix | **Cut** — stage assignment is composition-based, so transitions are rule artifacts |
| Per-operation distributions (p50 / p90 of command families), a `metrics.Extract` keeping full op streams | **Cut for v1** — no metric above needs them; Facts stays the extract |
| Metric registry with ~10 fields | **Reduced** to `Metric{ID, Group, Title, Unit, Direction, Insight}` + one `Version` |
| `project_id` distinct from the CWD string | **Deferred** — an alias map in Settings (§14); v1 keys by CWD as Insights does |
| Standardized rates with baseline weights; EWMA; control charts | **Cut** — statistics on 5–8 points manufacture a signal; blocks of 10 and printed spread instead |
| Session-cohort vs activity-time series; `as_of` snapshots | **Cut** — closed sessions by start date; live sessions excluded as Insights does |
| Tokens by stage (strict vs allocated modes) | **Cut** — the allocated number is the weakest in Facts; stays on M1 / T3 |
| Four invariants tested | **Kept** — §12 |
| "Harness health 83/100" (rejected by the write-up itself) | **Refused** — §2 |
