# Normalized event schema

Every source adapter emits the same structures. All times are Unix milliseconds (UTC). Two
adapters exist: `internal/codex` (OpenAI Codex CLI rollouts) and `internal/claude` (Claude Code
session logs); both plug into `internal/source` (the index/joiner seam) and touch nothing below.

## Session
```
Session {
  id            string     // root thread / session id (a UUID in both sources; unprefixed)
  source        string     // "codex" | "claude"
  title         string     // the harness's own name (Codex thread_name, Claude Code ai-title), else first user message
  cwd, branch   string
  started, ended int64     // ms; ended = last event (or now while live)
  live          bool       // root file grew during the last refresh
  lanes         Lane[]     // lanes[0] is the root
  groups        Group[]    // retry / iteration groups (session-wide)
  totals        Totals     // root-lane exclusive wall-clock accounting
  parallel      { agent_ms, wall_ms }  // sub-agent time, reported separately
  version       string     // changes when any file grew
}
SessionSummary (/api/sessions) { id, source, title, cwd, branch, started, updated, bytes, agents,
  live, cli, model, last_answer, question?, totals? }
```
`last_answer` is the final message of the last completed root turn (Codex `task_complete.
last_agent_message`; Claude Code the last `text` block of a message that stopped with
`end_turn`), verbatim and clipped, read from the file's tail by the index (no parse) and
replaced by the parsed turn once the session is open. It is the only recorded description
of what a session ended on: the Codex TUI's "recap" is generated in a temporary thread and
never written to the rollout, so there is nothing to extract and nothing is generated (rule 2).
The UI shows its first paragraph under the title and in the session list, labelled "last answer".
`question` is the time (ms) of a question the agent asked the user that nothing has answered yet:
Claude Code an `AskUserQuestion` (a question) or `ExitPlanMode` (a plan awaiting approval) tool_use
with no `tool_result` for its id, no later model output and no later typed prompt; Codex a
`request_user_input` call with no `function_call_output` for
its `call_id` and no user message after it. Read from the tail by the index like `last_answer`,
refreshed whenever the file grows; absent when none is pending. The session list marks such a
session with a pulsing ? chip next to "active" and the time since the question. A final answer
that merely ends with a question mark is prose and carries no signal (rule 2); any other tool call
still waiting for its result is indistinguishable from a long-running one and is not one either.


## Lane (= one agent thread)
```
Lane {
  id        string   // thread id
  path      string   // "/root", "/root/design_review", "/root/a/b"
  parent    string   // parent lane id ("" for root)
  role, nickname, model string
  depth     int
  file      string   // rollout path
  started, ended int64
  turns     Turn[]      // {id, start, end, status: completed|aborted|open|orphaned, trigger: user|system,
                        //  skill, review, mode: "plan"|"", lc: turn-level lifecycle or "",
                        //  lc_rule: the signal that pinned it (the origin lane when inherited),
                        //  lc_runs: skill runs inside the turn [{from, lc, lc_rule}]}
  ops       Operation[] // may overlap (parallel commands)
  segments  Segment[]   // exclusive partition of [started, ended]
  markers   Marker[]    // point events
  active    [start,end][] // the lane's own turns (derive.activeIntervals): a sub-agent is active inside its turns
  tokens    {input, cached, cache_write, output, reasoning, total} // usage consumed by this thread: per-call usage
                        // summed on every change of the cumulative counter — survives a counter restart (resumed thread)
                        // and a forked child's inherited counter; never the last cumulative value, never a plain sum of records
  by_phase, by_lifecycle {…: ms} // the two exclusive partitions of [started, ended]; same sum
  lc        Lifecycle   // lane-level stage pinned by the agent's spawn role (overlay), or ""
}
```

## Turn
```
Turn {
  id, start, end, status: completed|aborted|open|orphaned, trigger: user|system, model, effort,
  final, skill, review, mode: "plan"|"", lc, lc_rule
  lc_runs      [{from, lc, lc_rule}] // a skill invoked mid-turn (Claude Code's Skill tool call) pins its stage
                      // from `from` to the turn's end or the next run; a skill invoked before anything ran
                      // (Codex injects it at the turn start) is the turn's `lc` instead, so Codex turns have none
  tokens       {…}    // sum of the turn's counted model calls (the counting rule above); absent = no record
  responses    int    // number of counted calls in the turn
  first        {…}    // the first counted call: its uncached input (input − cached) is what the model re-read
                      // after a gap; on a sub-agent's first turn it is the cost of being spawned. Absent = no record
  context_peak int    // largest input of one call in the turn (the context size it reached)
  root_turn    string // sub-agent turns, CLI ≥ 0.153: the root turn this turn ran inside, from the harness's
                      // token_usage_record.root_turn_id; "" = not recorded. Never inferred
}
```
Every `token_count` in the corpus sits inside a turn, so per lane `Σ turns.tokens == lane.tokens`
(`cmd/dump` prints the check); a record outside a turn would count for the lane only.

## Operation
```
Operation {
  id       string    // source item id / call id
  lane     string
  turn     string
  phase    Phase     // see table
  kind     string    // sub-kind, see table
  lc       Lifecycle // SDLC stage served (see "Lifecycle"); lc_rule: the literal signal that decided it
  sub      string    // the breakdown sub-row of the phase (classify.Subgroup): code → read | search | edit | vcs |
                     // hosting | network | mcp | shell; wait_worker → agents | polling | hooks; unknown → script |
                     // tool | command; absent for every other phase. A function of (phase, kind), never of time
  start, end int64
  open     bool      // no end recorded yet
  status   string    // completed | failed | aborted | running | recorded (the harness's word, verbatim)
  exit     *int      // exit code when known
  query_miss bool    // status "failed" or a non-zero exit on a query kind (read, search, listing, probe, git
                     // diff/show/status/log, read-only docker/gh/glab/kubectl queries; classify.queryKinds) or on
                     // a CI status wait (`gh pr checks`, `gh run watch`: wait_worker/ci — the exit says the checks
                     // are pending or failing): the failure is an answer, not a failed step. Derived; status and
                     // exit stay literal (Claude Code records no exit code, only is_error); not counted in failed_ops
  title    string    // short human label (command head / file / tool)
  detail   string    // full command / file list (≤ 2 KB)
  identity string    // "<cwd>\n<normalized command>" for test/build/release ops and verdict-kind infra ops (classify.infraAttemptKinds); "" otherwise
  group    string    // group id when member of a retry group
  attempt  int       // attempt number within the group (test/build/release/infra ops)
  parallel int       // number of sibling commands started in the same tool call
  background bool    // outlived its turn (dev server, watcher): thin bar, excluded from totals
  context  int       // compaction ops: input of the last counted call before it (the context it started from)
  tokens   {…}       // compaction ops: the first counted call after it (what the model re-read); absent = none
  src      {file, off, len}   // exact source line for the inspector
}
```
Around every `ContextCompaction` the harness writes an all-zero `token_count` before (and often
after) the item; zero records are never counted, so `context` is the last real call and `tokens`
the first real call after (measured: contexts 190–246 k, re-reads ~26 k of which ~12–14 k uncached).

### Phases
| phase | meaning | rule source |
|---|---|---|
| `llm` | the model generating: `Reasoning`/`AgentMessage` items (verified, `by_kind.llm_verified`) plus uncovered in-turn time (convention, `by_kind.llm_gap`); includes the generation of every patch | turn boundaries + item timestamps |
| `code` | shown as **Development**: every tool call around the code short of build, test and release — reading/searching sources, listing, web/MCP lookups, file edits, local VCS, formatting, probes, scripted reads/writes; never model output (the key stays `code`: overlays, caches and the API reference it) | Codex `parsed_cmd`, `FileChange`, `apply_patch`, command table |
| `build` | compiling / bundling | command table |
| `test` | running tests, simulators, UI automation | command table |
| `release` | push, PR/MR, CI, deploy, device install, publish | command table |
| `infra` | containers, remote hosts, processes, cleanup, packages | command table |
| `wait_worker` | waiting for sub-agents, CI, remote leases, sleeps, process polls; also idle time before a harness-triggered turn (goal loop) | tool names, command table (`sleep` loops, `--watch`), `Turn.trigger` |
| `wait_user` | outside a turn on the root lane, before a user-triggered turn; **and a turn blocked on a question to the user** (Claude Code `AskUserQuestion`, Codex `request_user_input`): an open op holds the wait, so on a live session the tail up to *now* is `wait_user`, not `no_telemetry` | turn boundaries; the question tool call |
| `idle` | sub-agent outside a turn (waiting for the parent) | state machine |
| `compaction` | context compaction by the harness | `ContextCompaction` |
| `no_telemetry` | open turn with no events yet (`open_turn`), or turn never closed (`orphaned_turn`) — the agent is generating or the turn was interrupted, **not** blocked on the user (a question holds a `wait_user` op instead) | state machine |
| `unknown` | command matched no rule, opaque scripts, a Claude Code tool without a mapping (`tool:<name>`) | classifier fallback |

Claude Code tool calls map by name (`internal/claude/tools.go`): `Bash` through the command
table like a Codex command; `Edit`/`Write`/`NotebookEdit` → `code/edit`; `Read`, `Grep`,
`Glob` → `code/read`, `code/search`, `code/list_files` (query kinds); `WebSearch`/`WebFetch` →
`code/web_search`; `mcp__*` → `code/mcp`; `Agent` → `wait_worker/agent` (the parent waiting for
the sub-agent); `AskUserQuestion` → `wait_user/question` (the turn stays open while the user
answers); `TaskOutput`/`Monitor` → `wait_worker/process`, or the background Bash op they poll;
the Stop hooks → `wait_worker/hook`; `ExitPlanMode` → an instant `llm/plan` op pinned to the
planning stage; `Skill` → the turn's skill (a `skill` marker); `TodoWrite`/`TaskCreate` → `plan`
markers; bookkeeping tools (`ToolSearch`, `ListAgents`, `TaskList`, `SendMessage`, …) → nothing.

### Kinds used by the "inside testing" breakdown
Attempts of a retry group: `first`, `retry_after_failure` (previous attempt failed), `rerun`
(previous passed/aborted), `parallel` (started before the previous attempt ended).
Other ops on the same lane between a failed attempt and its retry, when exactly one group is
in that state: `fix` (code), `infra_recovery` (infra), `worker_queue` (wait_worker).
Kind `probe` (`test`, `[`, `which`, `type`, `command -v`): a shell question whose exit code is the
answer. Query kinds (`classify.queryKinds`) are the code-phase kinds that only read state; their
non-zero exits are `query_miss`, everything else — edits, patches, scripts, `shell`, every other
phase — is a verdict.
`remote` flag when the command targets a remote host (`ssh`, `*_REMOTE_HOST=`, `--remote`);
`queued` flag when a `while … sleep` loop precedes the work inside one command.
The kind is stored as `"<tool kind>|<role>"`.

### Script bodies (heredocs) — literal signals only
A `python3 - <<PY` / `node --input-type=module <<JS` body is classified by, in priority order:
literal commands passed to `subprocess.*` / `os.system` / `execSync` (classified with the same
table, one level deep, kind `script→<inner kind>`); a file written with a literal body
(`Path(f).write_text(...)`, `cat > f <<EOF`) and executed by a later segment (`bash f`, `./f`)
is classified by that body; assertion statements or a playwright/puppeteer import →
`test/script-check`; file writes → `code/script-write`; only reads/queries/probes (no DML,
no writes) → `code/script-read`; anything else → `unknown`.
Only interpreter source is inspected this way. Heredocs consumed as data (for example by
`cat`, or by Python running a named script) remain opaque unless a written script is later
executed in the same command.

### Replayed history in forked sub-agents
A sub-agent file forked from its parent (`fork_turns: all`) starts with the parent's
`task_started`/`task_complete` lines (parent turn ids, same timestamp as the file, no items).
Only child turns with no operations, final answer, or markers beyond the start and injected
context are eligible for removal: superseded within 5 s, or completed within 1 s. Injected
context moves to the real turn when superseded. Root turns and turns with user messages,
plans, or questions are retained. A `task_complete` for a turn the file never opened is ignored.

### Pending tool envelopes
An operation awaiting output advances to the current time while its turn is open. Its
recorded output replaces that display endpoint when it arrives, including buffered output
whose timestamp precedes the previous refresh. The exclusive partition stays within lane bounds.

### Provisional ops
When an `exec` call returns (`yield_time_ms`) before its commands finished, an op is synthesized
from the literal `cmd:` strings in the JS input with status `running`; the real
`CommandExecution` item (same command text) replaces it when it arrives.

### Background processes
An op whose end lies past the end of the turn it started in (1 s tolerance), or that started
outside any turn, is `background`: the log proves the agent moved on while the process kept
running (dev servers, watchers, a leftover `npm start`). Background ops are drawn as a thin
bar under the lane, summed in `totals.background_ms / background_ops`, and never overlay the
partition, the raw sums or retry groups.

## Segment
```
Segment { s, e, p: phase, lc: lifecycle, op }   // exclusive; op = the winning op id or ""
```
The timeline's stage band and the dump's stage runs are consecutive segments of one `lc`,
computed where they are drawn; no coalesced view is stored (the activity brackets that once
attributed model output to the next tool call by phase were retired on 2026-09-15 — the
lifecycle partition says which stage the time served, the fill says what ran).

## Marker
```
Marker { t, kind, lane, turn, text, ref }
kind: user_message | system_message (harness-injected, ref = tag) | question | final_answer |
      agent_started | agent_interacted | agent_completed | agent_interrupted |
      result_returned | message_sent | message_received | plan | turn_start | turn_end |
      compaction | interrupted | resumed | goal | skill (ref = skill name; a skill was actually invoked) |
      llm_error (an invalid tool call, an API error, a refusal)
```
`agent_started` / `agent_completed` carry the sub-agent's lane id in `ref` (Codex: the item's
`agent_thread_id`; Claude Code: `agent-<agentId>` from the Agent tool's result), which is how the
UI draws a lane's spawn and its dashed link to the parent. A `user_message` with an empty `turn`
is a slash command the harness answered itself (`/clear`, `/cost`): the user's words, no turn.
```
```

## Group
```
Group { id, identity, phase, title, start, end, attempts:int, failed:int, members: op ids[] }
```
Command identity preserves quoted arguments and their internal whitespace. It removes
supported output redirections and trailing cosmetic pipe stages; complex shell syntax is
kept verbatim when safe normalization is uncertain.

## Totals
```
Totals { elapsed_ms, in_turn_ms, raw_ops_ms, by_phase: {phase: ms}, raw_by_phase: {phase: ms},
         by_lifecycle: {lifecycle: ms}, by_kind: {kind: ms}, ops, turns, user_messages,
         system_messages, questions, compactions, failed_ops, query_misses, background_ms, background_ops,
         reviews, tokens }
```
`failed_ops` counts failed steps only (`Operation.Failure`: a recorded failure or non-zero exit that
is not a query miss); `query_misses` counts the rest, so the two together are the harness's
literal failure count. `tokens` is the sum of the lanes' `tokens`.
`sum(by_phase) == sum(by_lifecycle) == elapsed_ms` always (partition property, tested).
`raw_ops_ms` is the plain sum of op durations and is larger when commands ran in parallel inside
one lane.

## Lifecycle (SDLC stage) — the second partition
Every segment carries two labels. `phase` says **what** the tool call was (a test run, an edit,
a push); `lc` says **which stage of the software lifecycle** the time served. Both are exclusive
partitions of the same segments, so each sums to the lane's elapsed time — but they are **not
comparable per key**: the activity partition keeps model output in its own `llm` phase, the
lifecycle partition attributes it to the stage of the nearest tool call in the turn (or to the
turn's signal). A code-review hour includes its model time; the Development row never does. The UI therefore
shows every stage row with its model / tools split.

| lifecycle | meaning | assigned from (literal signals only) |
|---|---|---|
| `plan` | planning, scoping, writing the plan | turn: Codex `collaboration_mode.mode == "plan"` (`turn_context`), Claude Code `permissionMode: plan`; op: an `update_plan` call that *creates* a plan (no step `completed` yet) or `ExitPlanMode` is an instantaneous `llm`/`plan` op — the model output before it is planning, and so is every code, build, test or infra call before the turn's **first** such anchor that changed nothing (a change op there stays implementation); progress updates stay markers |
| `requirements` | eliciting / specifying requirements | **no built-in detector** — overlay `lifecycle.skills` / `roles` / `paths` |
| `design` | architecture and detailed design | **no built-in detector** — overlay |
| `implement` | writing code, building, environment work | default for `code`, `build`, `infra` phases; **the change window**: every `code`, `build`, `test` and `infra` call between the turn's first and last change op (`classify.ChangeKinds`: edits, patches, written files, `sed -i`, formatters, `mkdir`/`cp`/`mv`/`touch`/`ln`, `git rm`/`mv`/`apply`/`cherry-pick`) — a test between two edits is the implementation loop, a test after the last edit is the verification pass; operations candidates before this lane's first release op |
| `review` | code review / cleanup | turn: an invoked skill matching the review matcher (`code-review`, `codereview`, `simplify`, overlay) — a **run** from the skill marker to the turn's end (Codex injects the skill at the turn start, so the whole turn; Claude Code's `Skill` tool call is mid-turn, so the calls before it keep their own composition), Codex review mode (`EnteredReviewMode`, whole turn), a sub-agent turn that started inside such a turn or run (inherited, any depth); lane: a sub-agent whose spawn `agent_role` matches overlay `lifecycle.roles`; op: kinds `pr review`, `pr comment`, `mr note`, `mr approve` (an op pin beats the change window) |
| `test` | testing / QA | default for the `test` phase |
| `release` | push, PR/MR create/merge, deploy, publish | default for the `release` phase |
| `operate` | maintenance / operations on a running system | kinds `journalctl`, `systemctl`, `launchctl`, `diagnostics`, `docker logs`, `kubectl logs`, `kubectl describe` (and overlay rules with `"lifecycle": "operate"`) — **only after this lane's first release op** |
| `llm` | **not a stage**: model output of a turn that made no tool call at all (a text-only answer) | nothing in the turn says which stage it served; only a harness signal can. The UI lists it under "Outside stages" |
| `wait_user`, `wait_worker`, `idle`, `compaction`, `no_telemetry`, `unknown` | **not stages**: pass-through — they happened inside or between the stages and keep their own name, so the partition still sums to elapsed; the UI lists them under "Outside stages" | their phase name |

Precedence: lane role → the turn's own signal (plan mode — a skill invoked inside a plan-mode
turn reviews the plan, so the turn stays planning; review mode; else a skill run from its marker on) → the stage in force on the parent turn a sub-agent turn was **linked** to when it
started (inherited; `lc_rule` names the origin lane, through any depth) → op-level pin (command
kind, edited path, plan anchor) → the turn's composition (the plan run, the change window) →
phase default. Composition reads the whole turn before deciding any op of it: the stage of a
call is decided by the group it sits in, never by that call alone. The link is a recorded harness signal, never bare
time overlap: the harness's `root_turn_id` first, else the spawn / message marker for that lane
(`agent_started` / `agent_interacted`), else, for the child's
first turn only, an open `wait_worker` agent op on the parent whose window contains that turn's
start (a bare wait cannot say which of several concurrent agents it is, so a re-used agent's
later turn is not linked this way); no link → no inheritance. A turn-level stage covers the **whole** turn: tests,
waits, compaction and model output alike. Model output inside a turn with no turn-level stage
takes the stage of the nearest tool call in the same turn: the next one first (the call it
prepared), else the previous one (the answer that reported on it). Compactions and telemetry gaps inside the turn are transparent — skipped over,
they keep their own name for their own duration — while a wait for workers (a spawn, sleep or
poll the model chose), waiting for the user, idle time and the turn's end each close the bracket.
An unknown command is not transparent either: model output nearest to it is `unknown`. A turn
with no tool call at all keeps its model output as `llm`. Segments are cut at turn boundaries first so a signal never leaks into
the neighbouring turn. Inheritance runs downward only: a sub-agent is its parent turn's tool
call, so its work is that turn's work; nothing flows upward, and the root's wait for a review
sub-agent stays `wait_worker`.

The **order-dependent** rules, all over already-classified ops and never over a duration: the
operations guard (an operations candidate that starts before the lane's first `release` op is
`implement` — "nothing is after launch before anything shipped"), the skill run (from the marker
to the turn's end), the plan run (before the turn's first plan anchor) and the change window
(between the turn's first and last change op). The op's `lc_rule` names the one that decided it
in the inspector. Model-output ops (Codex `Reasoning` / `AgentMessage` items) carry the stage of
the segment that covers them, so the operations a stage row lists and the time it shows agree.

A `skill` marker records that a skill was *actually invoked* — parsed from the harness's
`skills.selected_skill_instructions` injection, never from the words in a user or model message.
A turn's `skill` names it; `review` is true when the name matches the review matcher (bare
`review` is excluded so `security-review` etc. do not match). `reviews` counts those invocations
across all lanes; the time is `by_lifecycle.review`. A turn that reuses a skill later without
re-invoking it carries no signal, so review time is a floor.

## Command classification table (head word → phase)
Rules are evaluated per top-level shell segment; the operation takes the highest-priority
phase found. Priority: release > test > build > workers > infra > code > unknown. At equal
priority the first segment decides, except that a `shell` segment (`cd`, `echo`, `set`) yields
to a substantive one: `cd x && rg foo` is the search (kind, title and query-miss status follow it).
The table lives in `internal/classify/classify.go` and is the single source of truth; the
`/api/rules` endpoint exposes it (plus the lifecycle defaults, pins and matchers) so the UI
"How to read" page shows the live tables.

### Two detection sources (built-in + user overlay)
Detection has two layers. The **built-in** set covers common tools, commands and embedded skills
(`glab`/`gh`, `make`, build/test/release commands, `simplify`/`code-review`). A **user overlay**
adds project-specific commands and the skill names, agent roles and document paths that pin a
lifecycle stage for a custom setup, loaded at startup from `--rules <path>`, `$TODOBEM_RULES`,
and `~/.todobem/rules.json` (all merged). Format (all fields optional):
```
{ "rules": [ {"match": "seg:^myci\\b", "phase": "test", "kind": "in-house ci"},
             {"match": "deploy-thing", "phase": "release"},
             {"match": "prodctl", "phase": "infra", "kind": "prodctl", "lifecycle": "operate"} ],
  "lifecycle": {
    "skills": {"review": ["audit-.*", "my-review"], "plan": ["^brainstorm$"]},
    "roles":  {"review": ["^pragmatic$"]},
    "paths":  {"design": ["(^|/)docs/DESIGN\\.md$"], "requirements": ["(^|/)SPEC\\.md$"]} } }
```
`rules` entries use the same match forms as the built-in table (a bare word / `word sub`, or a
`seg:`/`re:`/`head:`/`headpath:` regex) and are appended after the built-ins, so a word rule
overrides a built-in with the same key and a regex rule can only raise the matched phase. An
optional `lifecycle` on a rule pins the stage its kind serves (one of the eight work stages).
`lifecycle.skills` / `roles` / `paths` are regexes keyed by stage, matched against a selected
skill's name, a sub-agent's spawn role and an edited file's path, OR-ed with the built-in
matchers (only code review has one). A malformed overlay (bad phase or lifecycle, empty match,
uncompilable regex, unknown key) fails loudly at startup and changes nothing. User rules and
matchers are served at `/api/rules` alongside the built-ins (flagged), keeping one inspectable
source of truth.


## Insights report (`/api/insights/report`)
```
Report { generated_at, params: {cwd, sources?: [codex|claude], period: {kind: 7d|30d|90d|all|custom|session, from, to, session}, include_live},
  scope: { sessions, live_excluded, pending: [{id, title, ended}], root_elapsed_ms, root_in_turn_ms, wait_user_ms, tokens, clis },
  top_time: [rule…], top_tokens: [rule…],
  groups: [ { id, cards: [Card], time_ms, tokens, order_time, order_tokens } ],   // not_measured last
  no_data: [ { rule, title, sessions, reason, items } ],
  fallback?: fewer_than_3_sessions | no_sessions, sources: [{id, fp, title, ended}], sources_hash }
Card { rule, group, title, info?, exposure: {time_ms, tokens?, count}, share?: {pct, of_ms, of},
  distribution: [{label, n, time_ms, tokens?, sessions}], sessions, of, no_data, reason?, no_data_items?,
  conventions?: [string], evidence: [{session, title, lane, lane_id, a, b, op?, time_ms, tokens?, note}], stats?: {name: int} }
```
A session belongs to the period when its last activity lies inside `[from, to]`; live sessions
never count unless `include_live=1`; `period=session` selects one id. Facts (`internal/insights`
`Facts`, cached as `<id>.facts.json.gz` next to the model cache) are derived from the model only.
`POST /api/insights/scan` parses the pending sessions in the background; `GET …/status` reports
progress; `GET …/rules` lists the catalogue. The visible texts live in `web/insights.js`
(`INSIGHT_TEXT`), the rules in `docs/INSIGHTS-SPEC.md` §5.
