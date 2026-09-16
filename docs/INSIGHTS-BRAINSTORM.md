# Finding insights in AI coding-agent sessions — brainstorm brief

*todobem · 15 Sep 2026 · for architects, developers and QA who will propose new detectors.*

What the Insights analyzer receives, which heuristics exist today, and where to take it next.
Every number the analyzer prints is a measurement of a session log, never a guess about why.

| | |
|---|---|
| **Input** | one derived model per session → a compact `Facts` record |
| **Engine** | 18 deterministic detectors → findings → a period report |
| **Sources** | Codex CLI rollouts, Claude Code session logs |
| **Constraint** | literal signals only; no estimates; nothing leaves the machine |

Reference: `docs/ARCHITECTURE.md` §10 (facts, catalogue, report, scanner), §6 (stage
detection), §3.2 (subgroups). Catalogue ids are stable; a new detector takes the next free id
in its group.

---

## 1. The rules a heuristic must obey

- **A detector, not an opinion.** A card is produced by a named rule over literal structure:
  event order, turn status, retry identity, stage, token records. No similarity, no model, no
  duration threshold that *explains* anything. A long operation is listed; a pattern is a
  structural fact.
- **Exposure, distribution, evidence.** What the pattern consumed in the period (time and/or
  tokens, denominator printed), how it is spread (count, median, p90, buckets), and where
  (session, lane, interval, op — the evidence opens the timeline). No card without evidence.
  No counterfactual savings.
- **Ranking by absolute exposure** on one axis, time or tokens (uncached input + output). Info
  cards are measurements: shown, never ranked or totalled.
- **"Did not happen" ≠ "could not be measured".** A session that cannot carry a signal for its
  CLI version is listed as no data, never counted.
- **Advice is a template bound to the rule**, phrased as a change to the harness (habit,
  config, skill, rule, delegation), never a judgement of the user or the model, never a causal
  claim the log does not record.
- **Cross-session cards need one project and three closed sessions**; live sessions never
  count. Plain English everywhere; every visible string in one table with a sentence-length
  test.

---

## 2. Input: what the analyzer sees

The analyzer never reads a log line. Ingestion turns each session into a normalized model
(lanes = agent threads; turns; operations = tool calls, model output, waits; an exclusive time
partition by *activity* and a second one by *SDLC stage*; retry groups; tokens).
`Extract(model) → Facts` is a pure function; facts are cached next to the model and rebuilt
whenever ingestion, classification or the facts schema changes.

| Facts section | Fields | Literal signal behind it |
|---|---|---|
| Session | id, source, cwd, branch, model, CLI version, started / ended, live | the log header (Codex) or the first message line (Claude Code); the CLI version gates what is measurable |
| Root totals | `by_phase`, `by_lifecycle`, tokens (root and all lanes), turns, aborted, user messages, questions, failed ops, query misses | the main thread's exclusive partitions; a query miss is a read / search / probe / CI-status check that answered with a non-zero exit |
| Agents | path, role, model, depth, kind `root \| read_only \| worker`, turns, ops, tokens, first call, active intervals | sub-agent files; kind from op content (a lane that never edited, built, tested, released or touched infra is read-only) |
| Turns | lane, status (completed / aborted / open / orphaned), trigger (user / system), model, effort, stage, question, tokens, responses, first call, context peak, root turn, model-output time and its split by stage | turn boundaries; usage records; plan mode, skill markers, spawn links for the stage |
| Gaps | start / end, after a question, previous / next turn, next trigger, the next turn's first call | root `wait_user` segments (the user's time) |
| Waits | start / end, kind (agent, sleep, ci, poll-loop, process, tail-f, hook), turn, solo time, lanes active | root `wait_worker` segments joined with sub-agent activity |
| Retry groups | identity, *shape* (phase + head + subcommand: the cross-session key), attempts, failed, per-role time (first, retry_after_failure, rerun, parallel, fix, infra_recovery, worker_queue), failed-to-retry windows with tokens | identical normalized command (+cwd) run ≥ 2 times; nothing fuzzy |
| Compactions | lane, turn, start / end, context before, tokens re-read after | `ContextCompaction` / `compact_boundary` with the usage records around it |
| Ops | background ops; the 40 longest verdict-phase ops; failed code-phase ops (with subgroup); the 30 longest unknown ops — each with phase, subgroup, kind, shape, title, status, exit | classifier output; background = outlived its turn |
| Tool-call mix | per phase × subgroup on the main thread: calls, exclusive time, query misses, failures | subgroups: read, search, edit, vcs, hosting, network, mcp, shell; agents, polling, hooks; script, tool, command |
| Unknown | heads with count and time; unknown time by subgroup; invalid tool calls (llm_error markers) | the classifier's honest remainder |
| Cells | lane kind × model × effort × stage: time and tokens (spread pro rata over a turn's stages) | turn usage × stage segments |

**Not in the input, on purpose or by necessity:** the content of reasoning (encrypted in
Codex), sub-agent prompts (encrypted since CLI 0.144), user and model prose (shown verbatim in
the UI, never interpreted — rule 2), prices, tool-result sizes as "bytes into context" (Claude
Code writes a result twice per line, a Codex `exec` closes N commands with one output;
`context_peak` and the first call's uncached input are the honest measures of context growth),
anything from outside the session folders.

---

## 3. Heuristics implemented today (the catalogue)

| id | group | signal (literal) | exposure · key | advice template (gist) |
|---|---|---|---|---|
| D1 | You and the agent | a root turn ended with a question to the user; the gap until the answer | the gap · bucket | answer sooner, or let the agent decide the case in the prompt |
| D2 / D2b | You and the agent | the gap before each user-triggered turn; D2b: 4 h or more (a break; info) | the gap · bucket; median, p90 | queue prompts; close the loop with a sub-agent instead of a reply |
| D3 | You and the agent | a root turn the user stopped (aborted) | its time and tokens · main / sub | stop earlier with a narrower ask; ask for a plan first |
| T1 | You and the agent | the first model call after a break: input not served from cache | tokens · gap bucket | a fresh thread after a long break costs less than resuming a large one |
| D4 | Sub-agents | a root wait for sub-agents during which only one was active | solo wait time | spawn independent workers together, not one after another |
| T6 | Sub-agents | a sub-agent's first call (instructions + context) vs its work | tokens; agents that cost more to start than to work | shorter agent instructions; batch small tasks into one agent |
| D7 | Failures and retries | a retry group with a failed attempt; the windows from a failure to its retry | window time and tokens · command shape | the shape names the flaky or misconfigured command; fix it in the harness, not per session |
| D15 | Failures and retries | a failed step in the code phase (edit, patch, script, shell, git, hosting, network); query misses and CI status excluded (info) | count · subgroup; the command on each row | read the file right before editing; keep patches small |
| D14 | Failures and retries | the harness could not parse the model's tool call (from 3 occurrences; info) | count | a model error; check the tool description if it repeats with one tool |
| D16 | Tool calls | the main thread's calls by phase and subgroup, with misses and failures (info) | exclusive time, calls · subgroup | many empty searches or repeated reads mean the agent lacks a map of the code |
| D9 | Long tool runs | the longest test / build / release / infra runs, for shapes that ran ≥ 2 times in the period | their time · shape | faster or incremental variant; start it early and work meanwhile |
| D12 | Long tool runs | an op that outlived its turn (a server, a watcher) | how long it kept running | stop it, or run it under the harness process manager |
| D11 | Context size | a compaction event: its pause, the context before, the re-read after | pause time, tokens · main / sub | new threads at stage boundaries; keep instruction files short |
| T2 | Context size | each turn's context peak | the turn's tokens · context bucket | a new thread at each stage costs fewer tokens per response and compacts less often |
| M1 | Models and effort | the main thread's model-output segments × model × effort; split by the stage each served; output of tool-less turns stated apart | time · model / effort | set effort per stage, a model per agent role |
| T3 | Models and effort | each agent's tokens × model × effort × agent kind | tokens · kind / model / effort | a cheaper model or lower effort for read-only sub-agents |
| T7 | Models and effort | reasoning share of output tokens, per effort (info) | share | lower effort for simple stages |
| D13 | Not measured | commands no rule matched (by head and by subgroup: script / tool / command), time with no telemetry (info; always last) | time · head | add rules to the user overlay; the `resolve-unknown` loop |

Named display conventions, printed on the card and never explaining anything: the 4 h
long-break split and the gap buckets 5 / 15 / 60 / 240 min. Cross-session post-filters: D9
needs a shape seen twice in the period, D14 three occurrences. Groups are ordered by exposure;
"Not measured" is always last.

---

## 4. What the last changes made answerable

- **Stages are decided per turn, as a group.** A skill run pins its stage from the moment it
  was invoked; every code / build / test / infra call between a turn's first and last edit is
  implementation; the test after the last edit is the verification pass; reads before the first
  plan anchor are planning; waits, compaction and gaps are not stages. So "how much of the
  model's time served testing" now means the verification pass, not every `go test` in an edit
  loop.
- **Sub-rows of the operations** (reading, searching, editing, git, code hosting, network,
  MCP, shell; sub-agents, polling, hooks; unknown scripts / tools / commands) carried on every
  op — the vocabulary D15 and D16 already use.
- **A CI status check that exits non-zero is an answer**, not a failed step: failure counts no
  longer count "looked at CI before it was green".
- **Turn-level runs** (`lc_runs`) and the recorded parent link of every sub-agent turn:
  fan-out work is attributed to the stage of the turn that spawned it.

---

## 5. Directions for new heuristics

Each candidate names its literal signal, what it would sum, and what it needs before it can
ship (a corpus check, a facts field, a harness signal we do not have). Items marked
**needs data** depend on something the logs do not record today.

### 5.1 Delivery loop and verification (QA)

| candidate | signal | exposure | needs |
|---|---|---|---|
| **Edits shipped without a verification pass** | a turn with a change window and no test op after its last edit (the composition rule already computes both) | count of turns, their edits; per project | a per-turn facts field: last edit, tests after it |
| **Flaky commands** | a retry group whose attempts alternate pass / fail with no edit between them (roles are on the group; "no fix op in the window" is literal) | time in the group · shape | windows already carry ops between attempts; a corpus check of frequency |
| **Reruns after a pass** (D8 of the draft) | a rerun role right after a passing attempt, nothing edited between | the rerun's time · shape | corpus check (12 m in one sampled session) |
| **Edit during a running test, then rerun** (D6) | an edit op overlapping a test op of the same lane, followed by a rerun of that test | the wasted run · shape | overlap is literal; corpus check (1 case seen) |
| **Review then release in sequence** (D5) | a review run (skill) followed in the same session by a release op, and the time between | time from the review's end to the push · session | CI waits measured (now query-miss-aware) |
| **Time to green CI** | the first `gh pr checks` after a push to the last one that exited 0 | time · shape of the CI wait | the push ↔ checks link (same lane order is literal); corpus check |
| **Unverified tool failures** | a failed step (D15) with no later op on the same file / command in the turn | count | the file path on edit ops is in the title; a shape key for shell |

### 5.2 Context and tokens

| candidate | signal | exposure | needs |
|---|---|---|---|
| **Repeated reads of the same file** | ≥ N read ops of one path in one turn (the path is the op title; literal) | calls and time · path shape (basename) | a facts list of read paths per turn; a cap |
| **Searches that found nothing, in a row** | ≥ 3 consecutive search query misses in a turn | count · turn | op order per turn in facts |
| **Compaction inside a change window** | a compaction op between a turn's first and last edit (the model lost context mid-implementation) | the re-read tokens · session | the change window on TurnFacts |
| **Retry tokens** (T4) | tokens of the turns overlapping a failed-to-retry window, already on the group | tokens · shape | window ids on turns to avoid double counting |
| **Polling responses** (T5 / D10) | model calls whose only tool call was a poll (sleep, ci, process) — the `queued` flag and the polling subgroup | responses, tokens · kind | responses per op are not recorded; approximate by turns whose tool calls are all polls — **needs data** |
| **Cache lost across sub-agents** | a sub-agent's first call with uncached input ≥ its parent's context peak (it re-read what the parent had) | tokens · agent kind | parent link is recorded; corpus check |

### 5.3 Stages and the SDLC shape of a session

| candidate | signal | exposure | needs |
|---|---|---|---|
| **Stage cadence** | the sequence of stage runs per turn and per session (plan → implement → test → release); turns that skip a stage the project usually has | counts; time per stage per turn (info) | stage runs per turn in facts (from segments; cheap) |
| **Implementation turns without a review skill** | a turn with a change window and no review run in the session after it | edits not reviewed · session | review is a floor (a skill reused without re-invocation is invisible); say so on the card |
| **Planning share before the first edit** | time in the plan stage before the session's first change op | time · session (info) | plan anchors exist for both harnesses |
| **Requirements and design** | no built-in signal; overlay skills / roles / paths only | — | a convention the team adopts (a skill name, a document path) — a question for the group |
| **Operate after release** | log reads / service control after the lane's first release op (the guard already labels them) | time · session | corpus check: rare in the sampled projects |

### 5.4 Sub-agents and orchestration

| candidate | signal | exposure | needs |
|---|---|---|---|
| **Fan-out depth and width** | agents per root turn, max concurrent (from active intervals), nesting depth | counts, wall vs sum (info) | already in facts (agents, waits.lanes) |
| **Read-only agents that edited** | a lane whose kind is worker but whose role name says reviewer / explorer (overlay roles) | count · role | role → expected kind mapping in the overlay |
| **Orphaned sub-agents** | a sub-agent turn orphaned while the root went on (now detected; the harness stopped the agent) | count, their tokens · session | turn status is in facts |
| **Serial waits with an idle parent** | D4 refined: the root did nothing but wait while one agent worked, vs the root working meanwhile | solo wait minus root tool time | root ops during the wait window |

### 5.5 The user's side

| candidate | signal | exposure | needs |
|---|---|---|---|
| **Questions that could have been decided** | a question turn whose answer was a one-word reply followed by the same stage continuing | gap time · session | the reply's length is literal but its content is prose: report length only, never meaning |
| **Interrupts and what preceded them** | D3 refined: the op running when the user stopped the turn (a long test, a wait) | time · op shape | the aborted turn's last op is in facts order |
| **Prompt cadence** | user messages per hour of session, queued vs typed (Claude Code records the source) | counts (info) | promptSource on markers → facts |

### 5.6 Cross-session and presentation

- **Trends per project**: the same card over consecutive periods (week over week) — exposure
  only, no "improved by"; needs a stable key per card and a chart rule (one scale,
  denominators printed).
- **Command-shape latency profiles**: p50 / p90 of a shape across sessions (D9 generalized);
  flags nothing, lets a reader see the slow build.
- **Per-harness comparison**: the same cards split by source (Codex vs Claude Code) — the
  sources field is already in the report params.
- **Estimates**: still deferred; the previous review showed each rested on something the log
  does not record (worker independence, attainable reply speed, prices). They return one at a
  time with their own validation and a user-supplied price table.

### 5.7 Telemetry we would ask the harnesses for

- An exit code and a duration on every Claude Code tool result (today: `is_error` and
  "Exit code N" text for Bash only; timing is call line → result line).
- Per-call usage records with timestamps on both harnesses (Codex has them since CLI 0.153 on
  sub-agents; Claude Code repeats one usage per message).
- Plaintext or a hash of sub-agent prompts (encrypted in Codex since 0.144), so a spawn can be
  grouped by task shape without reading its text.
- A harness-recorded "verification" signal (the harness knows when it ran tests on behalf of a
  hook) to separate the agent's tests from the harness's.
- Tool-result sizes as the harness measured them (not the log line), for an honest "bytes into
  context".

---

## 6. Questions for the group

- **Q1 · Verification coverage.** "Edits shipped without a verification pass" is a literal
  rule over the change window. Is a turn the right unit, or the session (tests anywhere after
  the last edit)? What counts as verification: test runs only, or lint and type checks too?
- **Q2 · Flakiness.** Alternating pass / fail with no edit in between: is that a strong enough
  literal for "flaky", or do we need the same command to fail on a clean tree (unknowable from
  the log)?
- **Q3 · Requirements and design.** No harness marks them. Do we adopt conventions (a `design`
  skill, `docs/ARCHITECTURE.md` edits) and ship them as overlay presets, or leave the two stages
  empty by default?
- **Q4 · Repeated reads.** The path is literal, the intent is not (a re-read after an edit is
  legitimate). Report only re-reads with no edit of that file in between?
- **Q5 · Advice.** Which templates are acceptable to a team? The current ones change the
  harness (config, skills, delegation); none speaks about the person. Where is the line for
  QA-facing advice ("add a test for X" is a claim about the code we cannot make)?
- **Q6 · Trends.** Week-over-week cards without "improved by": is exposure per period enough,
  or do readers need a normalized rate (per session, per hour in turns)?
- **Q7 · Cross-harness.** Which cards are comparable between Codex and Claude Code given the
  telemetry gaps in 5.7, and which must stay per source?
