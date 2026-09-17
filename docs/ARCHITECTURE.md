# todobem — architecture

The one document that describes the current implementation: what the binary is made of, how
the components talk to each other, the normalized schema, and the algorithms that turn a
session log into operations, an exclusive time partition and SDLC stages. The binding rules
(product rules 1–8, engineering rules, definition of done) live in `CLAUDE.md`; this document
explains the mechanism those rules constrain. Dates mark the decisions that shaped a part.

Contents: 1 Overview · 2 Sources · 3 Normalized model · 4 Classification of operations ·
5 Derivation · 6 Stage detection · 7 Persistence, settings, access · 8 Server API · 9 UI ·
10 Insights · 11 Validation protocol · 12 Extending · 13 Decision record.

---

## 1. Overview

todobem is a local, single-binary viewer for AI coding-agent sessions: a flight recorder that
replays what the agents did and where the wall-clock time went. It reads the session logs two
harnesses write on disk — OpenAI Codex CLI rollouts and Claude Code session logs — lays every
thread out as a lane on one timeline (root agent plus sub-agents), classifies tool calls into
operations, finds retry cycles, long waits and background processes, and attributes every
millisecond of the main thread to exactly one operation *and* to exactly one SDLC stage.

Stack: Go 1.22, standard library only; a vanilla JS + SVG single-page app embedded in the
binary; no build step, no npm. Everything runs on loopback; nothing leaves the machine.

### 1.1 Pipeline

```
 ~/.codex/sessions/**/rollout-*.jsonl          ~/.claude/projects/<project>/<id>.jsonl
 ~/.codex/archived_sessions/**                 ~/.claude/projects/<project>/<id>/subagents/agent-*.jsonl
            │                                               │
   internal/codex (Index, laneParser)            internal/claude (Index, laneParser)
            └──────────────┬────────────────────────────────┘
                 internal/source: Multi (one index over every source)
                                  Session (the joiner: one LaneParser per file,
                                  incremental refresh, rewrite restart)
                                  │  model.Lane per file: turns, ops, markers, tokens
                                  ▼
                 internal/classify: command → phase / kind / subgroup / lifecycle pin
                 internal/model.Derive: pending ops, background, exclusive partition,
                                  lifecycle partition, retry groups, totals
                                  │  model.Session (JSON)
             ┌────────────────────┼─────────────────────────┐
   internal/store (cache)   internal/server (API)   internal/insights (facts → detectors → report)
                                  │
                       cmd/todobem/web (the SPA)
```

Each layer knows only the one below it. The adapters emit `model.*` types only; the server
knows the `source` seam and never a format; the store knows the model and the files it was
built from; Insights reads the derived model and never a log line.

### 1.2 Repository map

| path | responsibility |
|---|---|
| `cmd/todobem/main.go` | entry point, flags (`-addr`, `-codex`, `-claude`, `-settings`, `-open`, `-rules`, `-cache`, `-auth`), subcommands `token`, `cache`, `unknown`; embeds `web/` |
| `cmd/todobem/unknown.go` | `todobem unknown`: unmatched commands and telemetry gaps across sessions, the loop the `resolve-unknown` skill runs |
| `cmd/todobem/web/` | `index.html`, `app.js` (session list, timeline, breakdown, inspector), `inspector.js`, `filter.js` (period + project + source filter), `settings.js`, `insights.js`, `dropdown.js` (the list of every `select.select`, drawn by the page over the native control, which keeps its value and its `change` event), `markdown.js` (the rendered view of a recorded message: a small GFM-subset renderer that escapes everything and links only http/https/mailto, plus the Show raw / Show rendered switch every prose panel carries), `app.css` (the surface finish: `grain.svg` is the one texture tile it lays over the page; `grain.js` re-lays it on a dense screen as rasters scaled to the screen, so a speck stays one CSS pixel), `fonts/` (Fira Sans, self-hosted); `cmd/todobem/app_test.js` runs the SPA under `node --test` |
| `cmd/dump/` | developer tool: totals, per-lane partition checks, stage runs, groups, longest and unknown ops, `-ops` TSV, `-insights` facts and findings |
| `internal/source/` | the seam: `Meta`, `Source`, `Session` (joiner), `LaneParser`, `Multi`, `Summaries`, `TailReader`, text helpers |
| `internal/codex/` | Codex adapter: `index.go`, `reader.go` (line typing by prefix), `lane.go` (turns, ops, markers), `tokens.go` |
| `internal/claude/` | Claude Code adapter: `index.go`, `lane.go` (turn state machine), `tools.go` (tool name → operation) |
| `internal/classify/` | `classify.go` (the rule table `Rules`, priority, segments, heredocs), `shell.go` (tokenizer, `Identity`), `lifecycle.go` (stages, change kinds, pins, matchers, fingerprint), `subgroup.go`, `userconfig.go` (overlay) |
| `internal/model/` | `model.go` (schema), `derive.go` (partition, groups, totals), `lifecycle.go` (the second partition) |
| `internal/server/` | JSON API, auth gate, session pools, `/api/settings`, `/api/insights/*` |
| `internal/auth/` | key file, one-time tokens, HMAC sessions |
| `internal/settings/` | `~/.todobem/settings.json`: the session folders per source |
| `internal/store/` | the derived-session cache and the facts sidecar |
| `internal/insights/` | `facts.go`, `detect*.go`, `report.go`, `scan.go` |
| `docs/ARCHITECTURE.md` | this document |

---

## 2. Sources

A *source* is one harness's on-disk format. Both sources implement `source.Source`:

```go
type Source interface {
    Name() string                       // "codex" | "claude"
    SetHomes(homes []string)            // the folders to read (several per source)
    Scan()                              // (re)index cheaply: first lines / heads and tails only
    Roots() []Meta                      // root sessions
    Descendants(rootID string) []Meta   // sub-agent files of a root
    Open(rootID string) (*Session, error)
    HomeStatuses() []HomeStatus         // for the Settings page
    …
}
```

`Meta` is one session file as the list and the cache see it: source, id, parent id, path,
size, mtime, start, cwd, branch, CLI version, model, the harness's own title, the last answer
(read from the tail) and a pending question, all obtained **without parsing** the file.
`Multi` fans out over the registered sources: one list, ids global and unprefixed (both
harnesses use UUIDs; `Meta.Source` names the format of a row), sources consulted in
registration order. A rollout copied into two homes is one session, from the first home.

### 2.1 Codex CLI rollouts (`internal/codex`, CLI 0.134 → 0.154 verified)

One JSONL file per *thread*; a sub-agent is its own file linked to the parent through
`session_meta.payload.source.subagent.thread_spawn.parent_thread_id` (with `agent_path`,
`depth`, `agent_role`, `agent_nickname`). Every line is `{"timestamp", "ordinal", "type",
"payload"}`. The index reads line 1 (`session_meta`) of every file and `session_index.jsonl`
for names; sub-agent files are attached to their root by the parent chain.

| line `type` / item | used for |
|---|---|
| `session_meta` | lane identity, cwd, branch, CLI, base instructions |
| `event_msg/task_started`, `task_complete` (`last_agent_message`; an `error` when the turn ended on a usage limit or a provider failure), `turn_aborted` | turn boundaries, final answer, interrupts; the error's message is an `llm_error` marker (the turn closes as completed, with no answer) |
| `item_completed` · `UserMessage` | user-message markers (harness injections such as `<codex_internal_context>` and `<subagent_notification>` are `system_message`) |
| `CommandExecution` (`command`, `parsed_cmd[]`, `exit_code`, `status`, `started_at_ms`, `completed_at_ms`) | command operations with exact timing |
| `FileChange`, `apply_patch` | `code/edit` operations |
| `Reasoning`, `AgentMessage` | model-output operations (`llm/reasoning`, `llm/message`); `final_answer` markers |
| `SubAgentActivity` (started / interacted / completed / interrupted) | lane graph, spawn links |
| `CollabAgentToolCall` (`wait`, `spawn`, `send_message`), `wait_agent`, `sleep` | `wait_worker/agent`, `wait_worker/sleep` |
| `ContextCompaction` | `compaction` operations, with the context before and the re-read after |
| `McpToolCall`, `WebSearch`, `view_image` | `code/mcp`, `code/web_search`, `code/image` |
| `update_plan` | a `plan` marker; a call that *creates* a plan (no step completed) is an instantaneous `llm/plan` op pinned to the planning stage |
| `request_user_input` / `request_user_input_async` | `wait_user/question` (an open op while unanswered) / a `question` marker only (the agent keeps working; the harness's `{"accepted":true}` output is not the answer — a later user message is, so the list's pending question survives it and the end of the turn) |
| `response_item/function_call` + `function_call_output` (old format: `exec_command`, `write_stdin` polls) | tool-call envelopes; old-format command timing stitched from the call and its polls |
| `response_item/custom_tool_call` `exec` (a JS script fanning out `tools.exec_command`) | parallel commands inside one call (`Operation.Parallel`); provisional ops synthesized from the literal `cmd:` strings when the call returns before its commands finish, replaced by the real items |
| `message` with `skills.selected_skill_instructions` | a `skill` marker and `Turn.Skill`: the harness's record that a skill was invoked (never the words of a prompt) |
| `turn_context` | model, reasoning effort, collaboration mode (`plan`) per turn |
| `token_count` (cumulative `total_token_usage`, `last_token_usage`) | tokens per lane, turn and compaction |
| `token_usage_record` (CLI ≥ 0.153) | `root_turn_id` on sub-agent files: the harness's link from a sub-agent turn to the root turn |
| `compacted`, `world_state`, `inter_agent_communication_metadata` | skipped by prefix, never decoded (up to 5 MB per line) |

Facts that shape the adapter: commands inside one `exec` call run in parallel, so intervals
inside one lane overlap; between `task_complete` and the next `task_started` nothing is
written (the user's time); inside a turn, uncovered time is the model generating (the next line
is always a model output). A sub-agent is long-lived: one `started`, then many `interacted →
completed` cycles. A forked child file echoes the parent's history (`fork_turns: all`): replayed
turns with no items are dropped (superseded within 5 s or completed within 1 s); a
`task_complete` for a turn the file never opened is ignored.

**Token accounting** (`tokens.go`): `total_token_usage` is cumulative per thread, written more
than once per call, restarts when a thread is resumed, and a forked child inherits its parent's
value. A lane's usage is therefore the sum of `last_token_usage` over the records where the total
*changed* — never the last cumulative value, never a plain sum. All-zero records (around
compactions) are never counted; a compaction op keeps the last real call before it (`context`)
and the first real call after it (`tokens`, the re-read). Every counted record sits inside a
turn, so per lane `Σ turns.tokens == lane.tokens` (checked by `cmd/dump`).

### 2.2 Claude Code session logs (`internal/claude`, CLI 2.1.226 → 2.1.270 verified)

Layout: `~/.claude/projects/<encoded cwd>/<session-id>.jsonl` is a root session; a sub-agent
spawned by the Agent tool is `<session-id>/subagents/agent-<id>.jsonl` next to
`agent-<id>.meta.json` (`agentType`, `name`, `description`, `model`, `spawnDepth`,
`toolUseId`); every agent file, a nested agent's too, sits directly under the root's
`subagents/` (the lane tree is flat); `tool-results/` is never read. Only that layout is walked.

There is no session header. Message lines (`user`, `assistant`, `system`, `attachment`) start
`{"parentUuid":…,"isSidechain":…` and carry `cwd`, `gitBranch`, `version`, `sessionId`,
`timestamp`; small metadata lines start `{"type":"…"` (`ai-title`, `last-prompt`,
`permission-mode`, …). The index types lines by prefix, does a bounded head scan (cwd, version,
title — re-scanned as the file grows until an `ai-title` appears) and a tail scan (last answer,
pending question); no line longer than 256 KB is decoded there.

| line | used for |
|---|---|
| `user` with text (`promptSource: typed\|queued`, `origin.kind: human`; or a harness prompt: `<task-notification>`, `auto-continuation`, `peer`) | turn start (Trigger `user` / `system`), `user_message` / `system_message` markers |
| `user` `<command-name>…` (a slash command) | a user message; a turn only when the model ran |
| `user` `[Request interrupted by user…]` | turn aborted |
| `assistant` (one line per streamed content block, all carrying the message `id`, `model`, `stop_reason`, `usage`) | tool-call ops (start), model-output ops, tokens once per message id, turn end at the last `end_turn` block (deferred until the next line proves the message over), `isApiErrorMessage` → `llm_error`; a line whose model is `<synthetic>` (a notice the CLI wrote itself) never names the turn's or the lane's model |
| `user` with `tool_result` (`is_error`, `Exit code N`, `returnCodeInterpretation`, `interrupted`, `backgroundTaskId`, `agentId`) | op end, status and exit; the sub-agent lane link; background polls |
| `system/stop_hook_summary` | one `wait_worker/hook` op per hook (`hookInfos`: command and duration; each ends at the summary line), titled by the command, its classification in `rule` (`hook · test/go test`) and the retry identity of a shell call, so a hook re-running the agent's command joins its group and a test hook is a recorded verification (§10); `hookErrors` marks every hook of the summary failed (the harness does not say which); the turn ends when the hooks are done. The hook's output is attached (`attachment`) before the summary and does not close the turn; a summary that arrives after a queued prompt opened the next turn still belongs to the turn that ended |
| `system/compact_boundary` (`preTokens`, `postTokens`, `durationMs`) | a compaction op |
| `system/local_command`, `model_refusal_*`, `attachment` lines | evidence only |
| `permissionMode: plan` on a prompt | `Turn.Mode = "plan"` |

Facts: tool timing is call line → result line; there is no exit code, only `is_error` and the
`Exit code N` text for Bash. `AskUserQuestion` keeps the turn open while the user answers (a
`wait_user/question` op). Background Bash returns at once with a task id; `TaskOutput` /
`Monitor` polls extend the Bash op. Queued prompts are stamped at delivery, so a prompt line is
always the start of its turn. A file ending on an `end_turn` line is a finished turn; the parser
closes it at end of file and re-opens it only if a later read brings another block of the same
message.

Tool name → operation (`tools.go`): `Bash` → the command classifier; `Edit` / `Write` /
`MultiEdit` / `NotebookEdit` → `code/edit` (+ an edited-path lifecycle pin from the overlay);
`Read` / `Grep` / `Glob` (`LS`) → `code/read`, `code/search`, `code/list_files`;
`WebSearch` / `WebFetch` → `code/web_search`; `mcp__*` → `code/mcp`; `Agent` / `Task` →
`wait_worker/agent` (+ `agent_started`, linked to the lane when the result names the agent);
`AskUserQuestion` → `wait_user/question`; `TaskOutput` / `BashOutput` / `Monitor` → a poll of a
background task, else `wait_worker/process`; `Skill` → a `skill` marker and `Turn.Skill`;
`ExitPlanMode` → an instant `llm/plan` op pinned to planning + a `plan` marker; `TodoWrite` /
`TaskCreate` → `plan` markers; bookkeeping tools (`ToolSearch`, `ListAgents`, `TaskList`,
`SendMessage`, …) → nothing; anything else → `unknown/tool:<name>` (honest, listed by
`todobem unknown`).

### 2.3 The joiner (`source.Session`)

One `Session` per opened root: the model carries the meta's identity, the first lane is the
root's, and nothing is parsed until `Refresh`. `Refresh` stats every lane file; if a file grew it
feeds the parser the new bytes only (a partial trailing line is left for next time); it asks the
index for newly discovered sub-agent files and attaches them; a smaller file or a
non-monotonic `ordinal` means the file was rewritten and the lane restarts from byte 0; when
anything changed it re-derives the model once (`model.Derive`) and bumps `Session.Version`.
Every op stores `(file, byte offset, length)` instead of the raw line, so the inspector can fetch
the exact source event later (`/api/event`) and no raw event is ever kept in memory. Measured on
a 151 MB Codex sample (286 MB with sub-agents): 40 % of bytes decoded, 1.5 s wall, 63 MB RSS.

---

## 3. Normalized model (`internal/model`, the JSON the API serves)

All times are Unix milliseconds. Both adapters emit the same structures.

```
Session  { id, source, title, cwd, branch, model, version, cli, started, ended, live, now,
           lanes: Lane[] (lanes[0] is the root), groups: Group[], totals: Totals,
           parallel: {agent_ms, wall_ms, agents}, bytes }
Lane     { id, path ("/root", "/root/design_review"), parent, role, nickname, model, depth, file,
           started, ended, live, turns: Turn[], ops: Operation[], segments: Segment[], markers: Marker[],
           active: [{s,e}] (the lane's own turns), tokens, by_phase, raw_by_phase, by_lifecycle,
           lc (a stage pinned on the whole lane by the agent's spawn role), in_turn_ms }
Turn     { id, start, end, status: completed|aborted|open|orphaned, trigger: user|system, model, effort,
           final, skill, review, mode: "plan"|"", lc, lc_rule,
           lc_runs: [{from, lc, lc_rule}]   // skill runs inside the turn (Claude Code mid-turn Skill calls)
           tokens, responses, first, context_peak, root_turn }
Operation{ id, lane, turn, phase, kind ("<tool kind>|<role>" once grouped), sub, lc, lc_rule,
           start, end, open, status: completed|failed|aborted|running|recorded, exit, query_miss,
           title, identity, group, attempt, parallel, remote, queued, background, rule,
           context, tokens (compaction ops), src: {file, off, len} }
Segment  { s, e, p: phase, lc: lifecycle, op }   // exclusive partition of [started, ended]
Marker   { t, kind, lane, turn, text, ref, src }
Group    { id, identity, phase, title, start, end, attempts, failed, members, lanes }
Totals   { elapsed_ms, in_turn_ms, raw_ops_ms, by_phase, raw_by_phase, by_lifecycle, by_kind,
           ops, turns, user_messages, system_messages, questions, compactions, failed_ops,
           query_misses, background_ms, background_ops, tokens, compaction_ms, reviews }
TokenUsage { input, cached, cache_write, output, reasoning, total }
SessionSummary (the list) { id, source, title, cwd, branch, started, updated, bytes, agents, live,
           cli, model, last_answer, question, totals? }
```

Marker kinds: `user_message`, `system_message` (harness-injected), `question`, `final_answer`,
`agent_started` / `agent_interacted` / `agent_completed` / `agent_interrupted` (`ref` = the
sub-agent's lane id), `result_returned`, `message_sent`, `message_received`, `plan`,
`turn_start`, `turn_end`, `compaction`, `interrupted`, `resumed`, `goal`, `skill` (`ref` = the
skill name), `llm_error` (an invalid tool call, an API error, a refusal).

### 3.1 Phases (the activity partition)

| phase | meaning | source of the label |
|---|---|---|
| `llm` | the model generating: `Reasoning` / `AgentMessage` items (verified) plus uncovered in-turn time (convention); includes the generation of every patch | turn boundaries + item timestamps |
| `code` | shown as **Development**: every tool call around the code short of build, test and release — reading, searching, listing, web / MCP lookups, edits, local VCS, formatting, probes, scripted reads and writes | `parsed_cmd`, `FileChange`, `apply_patch`, the rule table, the Claude tool mapping |
| `build` | compiling, bundling, dependency installation | rule table |
| `test` | test runners, linters, type checks, simulators, browser automation | rule table |
| `release` | push, PR / MR, CI reruns, deploy, device install, publish, scripts named deploy / release / ship | rule table |
| `infra` | services, containers, remote hosts, processes, cleanup, packages, media tools, diagnostics | rule table |
| `wait_worker` | waiting for sub-agents, CI, remote leases, sleeps, polls, stop hooks; idle time before a harness-triggered turn | tool names, rule table, `Turn.trigger` |
| `wait_user` | outside a turn on the root lane before a user-triggered turn; a turn blocked on a question to the user (an open op holds the wait) | turn boundaries, the question tool call |
| `idle` | a sub-agent outside a turn | state machine |
| `compaction` | context compaction by the harness | `ContextCompaction`, `compact_boundary` |
| `no_telemetry` | an open turn with no events yet, or a turn that never closed | state machine |
| `unknown` | a command no rule matched, an opaque script, an unmapped Claude Code tool | classifier fallback |

`Priority` (`classify.go`) decides which op wins when parallel commands overlap inside one lane
and which phase a multi-segment command gets: release 90 > test 80 > build 70 > wait_worker 65
> infra 60 > code 50 > compaction 35 > unknown 30 > llm 20 > wait_user / idle 10 > no_telemetry 0.

### 3.2 Subgroups (the breakdown's sub-rows)

`classify.Subgroup(phase, kind)` is a deterministic function of the phase and the base kind,
carried on every op as `sub`; only the phases that lump different work have one:

| phase | subgroup | kinds |
|---|---|---|
| `code` | `read` | read, image |
| | `search` | search, list, list_files |
| | `edit` | the change kinds: edit, sed -i, write-file, script-write, format, mkdir, cp, mv, touch, ln, git rm / mv / apply / cherry-pick |
| | `vcs` | git * (local), vcs-script, vcs-subcommand |
| | `hosting` | gh, gh api, glab, glab mr, glab ci, glab api (queries) |
| | `network` | http, dns, net, web_search |
| | `mcp` | mcp |
| | `shell` | inspect, shell, probe, process, system, version, script-read, sqlite, sql, go env / list / doc, npm, cargo, docker / kubectl queries, devicectl, simulator, xcode, codesign, everything else |
| `wait_worker` | `agents` / `polling` / `hooks` | agent / sleep, poll-loop, tail-f, process, ci, wait / hook |
| `unknown` | `script` / `tool` / `command` | python, node, ruby, perl, npm run, run, swift run, exec-script-error / `tool:<name>` / unknown |

A heredoc classified by its inner command (`script→<kind>`) maps by the inner kind. `Phase`
itself never changes: priority, retry groups, query kinds and the timeline colours stay.

### 3.3 Lifecycle stages (the second partition)

`classify.Lifecycle`: the eight work stages `plan`, `requirements`, `design`, `implement`,
`review`, `test` (shown as "Verification": test runners, linters and type checks alike),
`release`, `operate`, plus the pass-through keys `llm`, `wait_user`,
`wait_worker`, `idle`, `compaction`, `no_telemetry`, `unknown` — time that is **not a stage**
and keeps its own name so the partition still sums to elapsed. Every segment carries both
labels: `p` says what the call was, `lc` which stage it served. `sum(by_lifecycle) ==
sum(by_phase) == elapsed_ms` always (tested). The two partitions are never compared per key:
the activity partition keeps model output as `llm`, the lifecycle partition attributes it to
the stage it served (see §6).

---

## 4. Classification of operations (`internal/classify`)

Deterministic, over literal text; never a duration, a similarity or a model.

### 4.1 The rule table

`Rules` is the single source of truth (served at `/api/rules`). Match forms: `word` / `word
sub …` up to four words (exact head + subcommand), `head:<re>` (the executable's base name),
`headpath:<re>` (the executable as written), `seg:<re>` (one top-level shell segment, env
prefixes stripped), `re:<re>` (the whole command, heredoc bodies masked). Each row carries a
phase, a kind and an optional note; the overlay (§4.5) appends rows with an optional lifecycle
pin. Word rules are looked up exactly (longest key first: `xcrun devicectl device install` …
`git`), regex rules in order.

`Command(cmd, codexKind)`:

1. Codex `parsed_cmd` of type `read`, `search`, `list_files` short-circuits to `code`.
2. Heredoc bodies are masked (`maskHeredocs`): only stdin consumed as interpreter source is
   kept for inspection; data heredocs (`cat`, a named script) stay opaque.
3. The masked command is split into top-level segments at `&&`, `;`, `|`, `||`, newline
   (quotes and `(){}` depth respected). Every segment is normalized: env assignments, `sudo` /
   `nohup` / `exec` / `command` / `nice` / `timeout` / `env` / `xargs` / `caffeinate` / `time`
   wrappers and shell keywords stripped (`for x in …` / `case` headers carry no command);
   `node_modules/.bin/<tool>` → `<tool>`; `git -C dir -c k=v <sub>` → `git <sub>`; `npm --prefix
   … <sub>` → `npm <sub>`; `go run <pkg>` / `cargo run --bin <name>` judged by the package or
   binary name (a `-serve` flag → service); interpreters (`python3 script.py` judged by the
   script name, `bash -c '…'` by the inner command, `bash -n` a syntax check, `python3 -` opaque).
4. Every segment is matched (`matchHead`): word rules, then script-name rules (`head:` /
   `headpath:`; a read-only subcommand such as `status` / `list` keeps a deploy script a
   lookup), then a local dispatcher script's first positional verb (`./dev ci-build` → build;
   only unambiguous verbs, only for a clearly local script), then literal flags (`--build-only`,
   `--test`, `--deploy`, `--serve`). An unmatched executable stays unknown.
5. The op takes the **highest-priority** phase among its segments; at equal priority the first
   segment decides, except that a `shell` segment (`cd`, `echo`, `set`) yields to a substantive
   one (`cd x && rg foo` is the search). Priority also settles a word rule against a `seg:` rule
   on the same head: `tsc` is `build` (it emits JavaScript) and `tsc --noEmit` is
   `test/typecheck` — one definition of a type check across `mypy`, `pyright`, `npm run
   typecheck` and the dispatcher verb `typecheck`, which Insights reads as verification (§10.2).
6. Interpreter heredocs (`python3 - <<PY`, `node --input-type=module <<JS`) are classified one
   level deep by literal signals: commands passed to `subprocess.*` / `os.system` / `execSync`
   (argv lists, including one bound to a variable and passed later) are classified with the same
   table (kind `script→<inner>`); assertions or a playwright / puppeteer import → `test/
   script-check`; file writes → `code/script-write`; only reads / queries / probes and no DML →
   `code/script-read`; anything else stays `unknown`. A file written in the same command with a
   literal body (`Path(f).write_text('''…''')`, `cat > f <<EOF`) and executed by a later segment
   is classified by that body.
7. A `while … sleep` / `until … sleep` loop followed by real work keeps the work's phase with the
   `queued` flag; `ssh` / `scp` / `rsync` / `*_REMOTE_HOST=` / `--remote` set `remote`.
8. The title is the segment that decided the phase (`(+N more)` when there were others).
9. `Identity` (the retry key) is set for test, build and release ops and for infra kinds whose
   exit code is a verdict (`docker`, `kubectl`, `ssh`, `scp`, `rsync`, `brew`, `apt`, `rustup`,
   setup scripts, remote VMs, `devicectl`): the command with output redirections and trailing
   cosmetic pipe stages (`| tee`, `| tail`, `| grep`, `| xcbeautify`, …) removed, quoted
   arguments preserved; complex shell syntax stays verbatim. The adapter prefixes the cwd.

### 4.2 Query kinds and failures

A non-zero exit or `is_error` is a **failed step** only when the op is a verdict. Query kinds
(`classify.queryKinds`: read, search, list, probe, inspect, process, system, version, the
read-only git / docker / kubectl / gh / glab / go / npm / cargo / devicectl / simulator / xcode /
codesign queries, dns, net) and a CI status wait (`gh pr checks`, `gh run watch`: `wait_worker/
ci`, whose exit says the checks are still pending or failing) are **answers**: the op keeps its
literal status and exit, is marked `query_miss`, and is not counted in `failed_ops`.
`Operation.Failure()` is the single predicate every count, filter, retry role and Insights
signal reads. An exit of 130 (SIGINT) or 143 (SIGTERM) is a command that was stopped, not
judged — the record keeps its status and exit, `Failure()` says no. (CI status waits: decision
of 2026-09-15; kill signals: 2026-09-16.)

### 4.3 Lifecycle pins, matchers and change kinds (`lifecycle.go`)

- `PhaseLifecycle`: the stage default — `code`, `build`, `infra` → `implement`; `test` → `test`;
  `release` → `release`; everything else passes through under its own name.
- `LifecyclePins`: kinds whose stage is not the phase default — `pr review`, `pr comment`, `mr
  approve`, `mr note` → `review`; `journalctl`, `systemctl`, `launchctl`, `diagnostics`, `docker
  logs`, `kubectl logs`, `kubectl describe` → `operate` (demoted before the lane's first
  release, §6.3).
- `ChangeKinds`: the code-phase kinds that change files (§3.2 `edit`) — the literal evidence
  that a turn implemented something; `IsChangeOp` defines the change window (§6.2).
- Matchers: `ReviewSkillPattern` on a *selected skill name* (`code-review`, `codereview`,
  `simplify`, with `cc-` / path / `$` prefixes; bare `review` excluded so `security-review` does
  not match); overlay skill / role / path regexes keyed by stage. Requirements and design have no
  built-in detector: nothing in either log marks them; the overlay pins them.
- `RulesFingerprint`: a hash of the whole effective classifier (rules, change kinds, pins,
  matchers, a schema tag) — part of every cache key (§7.1).

### 4.4 Codex item and Claude tool mapping

Non-command items are mapped by type (§2.1) and by tool name (§2.2). The kinds they produce are
part of the same vocabulary the breakdown, the subgroups and Insights use.

### 4.5 The user overlay

`-rules <path>`, `$TODOBEM_RULES` and `~/.todobem/rules.json` are merged at startup:

```
{ "rules": [ {"match": "seg:^myci\\b", "phase": "test", "kind": "in-house ci"},
             {"match": "deploy.sh", "phase": "infra", "kind": "service"},
             {"match": "prodctl", "phase": "infra", "kind": "prodctl", "lifecycle": "operate"} ],
  "lifecycle": { "skills": {"review": ["audit-.*"], "plan": ["^brainstorm$"]},
                 "roles":  {"review": ["^pragmatic$"]},
                 "paths":  {"design": ["(^|/)docs/ARCHITECTURE\\.md$"]} } }
```

A word rule with the same key overrides a built-in (a `deploy.sh` word rule beats the
`head:` deploy-script regex because word rules are matched first); among regex rules the
highest-priority phase wins regardless of order, so a regex rule can only add or raise. A
malformed overlay (bad phase or lifecycle, empty match, uncompilable regex, unknown key) fails
loudly at startup and changes nothing. User rules and matchers are served at `/api/rules`
flagged as such, and `todobem unknown -explain <command>` prints the word key a rule must use.

---

## 5. Derivation (`model.Derive`)

`Derive(session, now)` runs after every change, parents before children, in this order:

1. **Review flags and query misses.** `Turn.Review` when the turn's skill or any `skill`
   marker in it matches the review matcher; `Operation.QueryMiss` (§4.2); `Operation.Subgroup`.
2. **Pending ops** (`extendPendingOperations`): an op awaiting its output advances to *now*
   while its turn is open; the recorded output replaces that endpoint when it arrives.
3. **Background** (`markBackground`): an op whose end lies past the end of the turn it started
   in (1 s tolerance) or that started outside any turn outlived the agent's attention (a dev
   server, a watcher). An op that names its turn and starts at or after that turn's end (a patch
   completed on the millisecond of `task_complete`, a stop hook whose summary arrived after a
   queued prompt opened the next turn) is that turn's close-out work and is judged by the same
   tolerance. Background is drawn as a thin bar, summed in `background_ms / background_ops`,
   and excluded from the partition, the raw sums and the retry groups.
4. **Retry groups** (`assignGroups`): ops with an identical `identity` (cwd + normalized
   command), across lanes, ≥ 2 occurrences → a group; attempt N = the N-th occurrence in start
   order. Roles: `first`; `parallel` (started before the previous attempt ended: a fan-out, not
   a retry); `retry_after_failure`; `rerun` (the previous attempt passed or was aborted). An
   infra group additionally needs a failed attempt (all-successful repeats are polls). Other ops
   on the same lane between a failed attempt and its retry, when exactly one group is in that
   state, get the roles `fix` (code), `infra_recovery` (infra), `worker_queue` (wait). The role
   rides on the kind as `"<kind>|<role>"`. Nothing fuzzy: no text similarity, no duration.
5. **Exclusive partition** (`buildSegments`, per lane): a base timeline from the turns —
   outside a turn `wait_user` on the root (`wait_worker` before a harness-triggered turn) and
   `idle` on a sub-agent; inside a turn `llm` (uncovered time is the model generating); an open
   turn with no events yet or a turn that never closed → `no_telemetry`. Ops overlay the base by
   `Priority` through an edge sweep, so parallel commands never double-count: at any instant the
   highest-priority open op owns the segment (`Segment.op`). Adjacent same-phase segments merge.
   `by_phase` sums the segments; `raw_by_phase` sums op durations (larger when commands ran in
   parallel). `sum(by_phase) == ended − started` on every lane (unit-tested on totals, checked
   per lane by `cmd/dump`).
6. **Lifecycle partition** (`assignLifecycle`, §6).
7. **Active intervals** (a sub-agent is active inside its own turns) and **totals**: the root
   lane's partitions, `raw_ops_ms`, `by_kind` (LLM verified vs gap, test sub-kinds), counts,
   failures vs query misses, compactions, background, `reviews`, tokens summed over all lanes;
   `parallel` = the sum and the union of sub-agent active intervals. Sub-agent time is never
   added to the root totals.

---

## 6. Stage detection: the mechanism

A **stage** answers *why*: which stage of the software lifecycle the time served. It is decided
for a **group** of operations — a turn, or a run inside a turn — after the whole turn has been
read, never for one call by itself. An **operation** answers *what*: one tool call, one model
output, one wait. Non-stage time (waiting for the user, waiting for workers, compaction, hooks,
telemetry gaps, unknown commands, the model output of a turn that made no tool call) exists only
at the operation level: in the stage view it keeps its own key and the UI lists it under
"Outside stages", never absorbed into a neighbouring stage and never shown as one.

Every assignment is a literal harness-level signal or the **order** of such signals inside one
turn; nothing is inferred from a duration, a similarity or prose (product rules 1, 3, 4).

### 6.1 Precedence per turn

1. **Lane role.** A sub-agent whose spawn role matches an overlay `lifecycle.roles` regex: every
   turn of the lane (`Lane.lc`, rule `agent role <role>`).
2. **Turn signal**, the whole turn (model output, waits and compaction included):
   - plan mode — Codex `collaboration_mode.mode == "plan"`, Claude Code `permissionMode: plan`;
   - Codex review mode (`EnteredReviewMode`);
   - **skill run** — a `skill` marker whose name matches a stage matcher opens a run from the
     marker's time to the turn's end or to the next stage-bearing skill marker. When nothing ran
     before the marker (Codex injects the skill at the turn start) the run is the whole turn and
     is promoted to `Turn.lc`; when calls precede it (Claude Code's `Skill` tool is a mid-turn
     call) the run is recorded in `Turn.lc_runs` and the calls before it keep their own
     composition. A plan-mode turn keeps planning whatever skill it invokes (the skill reviews the
     plan; decision of 2026-09-15). A lane role beats every run.
3. **Inherited.** A sub-agent turn with no signal of its own takes the stage in force on the
   parent turn it ran inside when it started — linked by a **recorded** signal only, never bare
   time overlap: the harness's `root_turn_id`; else the parent's `agent_started` /
   `agent_interacted` marker for this lane; else, for the lane's first turn only, an open
   worker-wait op on the parent whose window contains the turn's start. No link → no
   inheritance. Downward only: the parent's wait for a review sub-agent stays a wait. The rule
   names the origin lane through any depth (`inherited from /root (skill code-review-cc)`).
4. **Op pin.** A stage the adapter or the overlay pinned on the op: a command kind
   (`LifecyclePins`, an overlay rule's `lifecycle`), an edited path (`lifecycle.paths`), a plan
   anchor. The operations guard (§6.3) may demote an `operate` pin.
5. **Composition** (§6.2) for the turn's remaining tool calls.
6. **Phase default** (`PhaseLifecycle`).
7. **Segments** (§6.4): model output and pass-through time.

### 6.2 Composition: the turn read as a group

For a turn with no whole-turn signal, after all of its ops are known:

- **Plan run.** Every code, build, test or infra call before the turn's **first** plan anchor
  (an `update_plan` call that created a plan, an `ExitPlanMode` call) that is not a change op is
  `plan` — the reads made to write the plan are planning (`before the turn's plan was
  submitted`). A later anchor pins only its own model output.
- **Change window.** The turn's change ops (`ChangeKinds`: edits, patches, written files,
  in-place sed, formatters, file copies and moves) bound a window from the first to the last.
  Every code, build, test and infra call inside it is `implement` (`inside the turn's change
  window (first edit … last edit)`): a test between two edits is the implementation loop, not
  QA. A test after the last edit keeps the phase default `test` — the verification pass nothing
  was changed after. Release ops keep `release` wherever they sit (one definition of release for
  the stage and the activity; a script named `deploy*` is release by that literal name, and a
  user whose `deploy.sh` restarts a local server pins it to `infra/service` in the overlay).
- A turn with no change op keeps the phase defaults.
- Unknown commands keep `unknown` (an honest unknown is never absorbed into a stage).

Measured on four real sessions when the window landed (2026-09-15): tests inside edit loops
stopped being "Testing / QA" (one session: 33 % → 0.5 % of in-turn time), verification tails
stayed test, release did not move.

### 6.3 The operations guard

The one cross-turn, order-dependent rule: an `operate` candidate (log reading, service control,
diagnostics) that starts before **this lane's** first `release` op is `implement` — nothing is
"after launch" before anything shipped. Same-lane only: `docker up` after a mid-session push on
another lane never becomes operations.

### 6.4 Segment level: model output and pass-through time

Segments are first cut at every turn boundary and every run start, so a signal never leaks into
the neighbouring turn. Then, inside a turn:

- a tool call with a work stage is an **anchor**; an unknown command is an anchor for `unknown`;
- **model output** takes the stage of the nearest anchor in the turn — the **next** one first
  (the call it prepared), else the **previous** one (the answer that reported on it). Compactions
  and telemetry gaps are transparent (a harness interruption, not a decision: skipped over, they
  keep their own name for their own duration); a wait for workers (a spawn, sleep or poll the
  model chose), an unknown command, waiting for the user, idle time and the turn's end each end
  the search. A turn with no tool call at all keeps its model output as `llm` ("Model output,
  no tool call": nothing says which stage it served, and only a harness signal could);
- a model-output **op** (Codex `Reasoning` / `AgentMessage`, a Claude Code text block) then takes
  the stage of the segment that covers it, so the operations a stage row lists and the time it
  shows agree (before 2026-09-15 the op kept `llm` while its segment carried the stage: a row
  with 875 operations and 0 s).

`by_lifecycle` sums the segments; the inspector shows every op's `lc` and `lc_rule` (the literal
signal that decided it); the timeline's stage band draws runs of consecutive same-stage segments
with the model / tools split in the tooltip (§9.2).

### 6.5 Worked example

A Claude Code turn: prompt → `Read` × 3 → `Edit` → `Bash go test` (fails) → `Edit` → `Bash go
test` → `Bash go test ./...` → `Skill simplify` → `Read` → `Edit` → answer.

- No lane role, no plan mode. The `Skill simplify` marker matches the review matcher and calls
  precede it: a run `review` from the marker to the turn's end (`Turn.lc_runs`).
- Before the run: change ops at the first and second `Edit` → window; the first `go test` sits
  inside it → `implement`; the third `go test ./...` runs after the last edit before the run →
  `test` (the verification pass); the three `Read`s before the first edit → `implement` (phase
  default; reads before the first change are not a stage of their own — decision D, 2026-09-15).
- Model output before each call takes that call's stage; the answer at the end is inside the
  review run → `review`. The stage band reads: Implement · Test · Review; the fill under it is
  mostly teal (model output) with thin test and edit slivers; the breakdown shows Implementation
  with its model / tools split, Verification, Code review, and — under Outside stages — nothing,
  because no wait, compaction or gap occurred.

### 6.6 What is deliberately not done

Absorbing waits, compaction or telemetry gaps into the surrounding stage (a 38-minute `sleep`
loop is not implementation; a gap with a stage would violate rule 3); a stage for the reads
before a turn's first edit (it measured where turn boundaries fell, 1–50 % across sessions, not
comprehension); a stage for tool-less turns; a byte count of tool results as "context" (Claude
Code writes a result twice per line, a Codex `exec` closes many commands with one output,
screenshots are base64 — `context_peak` and the first call's uncached input already measure
context growth); hard vs soft release signals (a script name is the same class of literal signal
that makes test scripts classifiable). Each is on record in §13.

---

## 7. Persistence, settings, access

### 7.1 The derived-session cache (`internal/store`)

One gzipped JSON per session under `~/.todobem/cache` (`-cache`, `$TODOBEM_CACHE`, `off`
disables). The key is a `Fingerprint`: the source files as `(path, size, mtime)` from the index
(no parse) plus `RulesFingerprint`, and a `cacheVersion` for the on-disk shape. A default open
serves the cache when the fingerprint still matches; a grown file, a new sub-agent file, a rule
change or a schema bump misses and the session is re-parsed and rewritten — the cache never
serves an outdated classification. `Operation.Detail` (`json:"-"`) travels in a sidecar map
inside the file so the inspector works from a cache hit. Live sessions are never cached. The
Refresh button (`?refresh=1`) forces a re-parse. `todobem cache` reports the cache; `-prune`
drops entries whose id the current homes no longer list. The Insights facts sidecar
(`<id>.facts.json.gz`, `FactsVersion` + the same fingerprint) sits next to it. Measured: a 354 MB
session, ~3.7 s cold parse → ~0.16 s cache load.

### 7.2 Settings (`internal/settings`, `/api/settings`, `settings.js`)

`~/.todobem/settings.json` = `{"codex_homes": [...], "claude_homes": [...]}`, written by the
Settings page (a draft per source, add / remove / save), applied live. A key absent → that
source's default (`~/.codex`, `~/.claude`); `[]` → the source is off; `-codex` / `-claude` pin
the run to exactly those folders and make the page read-only. `Normalize` per list (trim, `~`,
absolute only, no folder twice — through a symlink either); `Resolve` needs at least one folder
overall; a malformed file fails loudly. `resolveHomes` in `main.go` is the one precedence rule
(flags > file > defaults) the server, `cache` and `unknown` share. `Server.SetHomes` drops the
files of a removed home, clears the in-memory pools, cancels a running Insights scan and
rescans synchronously; the source reader serves an event span only from a file under a current
home (no `..`, no symlink out of it).

### 7.3 Access (`internal/auth`, `server/auth.go`, `todobem token`)

The viewer shows whatever the agents read and wrote, so a loopback bind alone is not enough.
The key file `~/.todobem/auth.key` (32 random bytes, 0600, refused when group/world-readable,
created by whichever of server or CLI runs first, re-read on change so `todobem token -revoke`
kills every session live) is the root of trust. `todobem token` mints a 52-character base32
one-time token (`nonce ‖ issued ‖ session-ttl ‖ HMAC[:16]`): accepted within 5 minutes, once,
and never if minted before the server booted. `POST /api/login` exchanges it for a stateless
session (`expiry ‖ nonce ‖ HMAC`) in an HttpOnly, SameSite=Strict cookie, Path=/api, Max-Age
only (30 days by default). Token and session MACs are domain-separated. Static files stay open
(no data in them); every other `/api/*` answers 401 without a session; login / logout are JSON
POSTs (a foreign page cannot send `application/json` without a preflight, and there is no
CORS); a browser-declared `Sec-Fetch-Site: cross-site` is 403; the host check closes DNS
rebinding. `-auth=off` is an explicit choice the server announces. `deploy.sh` prints the login
link to the terminal, never to the log.

---

## 8. Server API (`internal/server`, loopback, gzip, behind the gate)

| route | answer |
|---|---|
| `GET /api/sessions` | the list (`SessionSummary[]`, from the index and cached totals; no parse) |
| `GET /api/sessions/{id}` (`?refresh=1`) | the derived `Session`; served from the cache when its fingerprint matches, else parsed (and re-cached); `refresh` forces a full re-parse |
| `GET /api/sessions/{id}/version` | ≈ 200 B; the client polls it (Follow mode, default 30 s) and re-fetches the model only when it changed |
| `GET /api/sessions/{id}/op/{opId}` | the op's detail (the full command / file list) |
| `GET /api/event?…` | the exact source line of an op or marker, read by `(file, off, len)` from a file under a current home |
| `GET /api/rules` | the rule table (built-in and user rows flagged), priority, lifecycle stages, defaults, pins, matchers, subgroups |
| `GET /api/auth`, `POST /api/login`, `POST /api/logout` | the gate |
| `GET/POST /api/settings` | the homes per source as typed and what the index found; POST saves and applies |
| `/api/insights/report`, `scan`, `status`, `rules` | §10 |

Every response carries `X-Todobem-Build`, a 12-hex fingerprint of the embedded `web/` tree
(`build.go`). The page keeps the first value it sees and reloads itself when a later answer
carries another one: a deploy (`scripts/deploy.sh`) replaces the binary under an open tab, and
the tab follows within a poll, or as soon as it comes back into view (a tab returning asks the
gate's state; the list page has no poll of its own); a restart of the same binary never reloads it.

Pools: an LRU of 6 parser-backed sessions (`opened`, expensive, refreshed on demand) and a
separate pool of 32 cache-served read-only models, so browsing history never evicts a live
parser-backed session. The index is rescanned on list and report requests when older than 20 s (10 s
while a session page polls); a full rescan of the trees is ~10 ms when nothing changed, and
only new or changed files have their head re-read.

---

## 9. UI (`cmd/todobem/web`)

Pages (hash routes): the lock screen (on a 401), **Sessions** (the list with source marks,
last answer, a pulsing `?` for a pending question — under the default order the active sessions
come first, then those with a pending question, then the rest by update time; an explicit sort
is its key alone — the period / project / source filter of `filter.js`), **Session**, **Insights**, **Settings**, and the guide dialog ("How to read")
that lists the live rule tables from `/api/rules`.

### 9.1 The session page

Header (title, cwd, branch, model, CLI, created / first message, elapsed, tokens, the last
answer with a copy button, the SDLC ring over `by_lifecycle`), the overview (drag to
select a window; a strip of the lifecycle partition under it; errors below), the timeline, the
breakdown, the operations list, the conversation (user messages, questions, final answers),
the agents table, the per-session Insights cards. The top bar says live or closed and wears the
list's pulsing `?` while a question of the agent has no answer (from the session's list summary,
reloaded whenever the session changes).

Recorded messages — the first user message, the last answer, the conversation, the prompt and
final answer of an agent card, the two messages of a waiting interval, a message marker — are
shown rendered as Markdown by `markdown.js`, with the recorded text one toggle away (Show raw /
Show rendered, one switch for every panel and open dialog; the copy button always copies the
recorded text). The renderer is the project's own, not a library: a GFM subset (paragraphs,
ATX headings, fences, nested and numbered lists, task boxes, quotes, pipe tables, rules, code
spans, emphasis, strikethrough, links) with guarantees by construction — every character
passes through `esc()`, the tags come from a fixed set, a link is only http/https/mailto
(checked on the raw destination), any other destination (Claude Code's `[app.js:12](/abs/path)`
file references) is a reference with the path as its tooltip, an image is a link and never an
`<img>`, and a newline or leading space inside a paragraph or list item stays (pre-wrap): the
line structure of a chat message is part of the record. A harness message is never rendered.
`index.html` carries a Content-Security-Policy meta (`default-src 'self'`, images `'self'`,
`data:` and `blob:` — the last for the grain rasters `grain.js` makes in the page — inline styles
allowed, no inline scripts) as the backstop: a renderer bug cannot become a script or an outbound
request. `app_test.js` holds the structural invariant (only the
renderer's tags and attributes, only web hrefs) under hostile inputs.

### 9.2 The timeline

One row per lane (root first, sub-agents indented by depth with a ▶ at spawn and a dashed link
to the parent; only lanes alive in the window are drawn). Inside a row:

- the **fill** is the raw exclusive partition (§5.5): teal is model output, colours are tool
  phases, grey is waiting for the user, hatched is missing telemetry — *what ran*;
- the **stage band** above the fill is the lifecycle partition: runs of consecutive same-stage
  segments in the stage colours with a label, the model / tools split and the tool-call count in
  the tooltip; time outside the stages leaves the band empty — *why*. It replaced the activity
  brackets (a run of tool calls of one phase plus the model output before each call) on
  2026-09-15: a bracket attributed model time to the next call by phase and contradicted the
  stage of the same minutes;
- background ops are a thin bar under the row; turn ends carry ✓ / ✕ / ⊘; a pulsing dot is an
  open turn; dashed verticals are turn starts; markers (user messages, questions, final answers,
  compactions, skills, retry groups) sit in the markers row.

Zoomed out, columns show the dominant phase with opacity for coverage; zoomed in, an expanded
lane shows phase rows and individual operations. A click frames a block, a second click zooms in,
a turn glyph highlights the turn; the inspector opens an op or a marker with its source event.

### 9.3 The breakdown

Three lists over the selected window (main thread exclusive, or every lane; "inside turns
only" changes the denominator):

- **Lifecycle stage** — the SDLC stages only, each with its model / tools split (a review hour
  includes its model time; the Development row never does);
- **Outside stages** — waiting for user, waiting for workers, compaction, no telemetry, unknown,
  "Model output, no tool call": inside or between the stages, same denominator, so the two lists
  add up to the whole;
- **Activity** — the phases, with **sub-rows** for Development (reading, searching, editing,
  git, code hosting, network, MCP, shell), Waiting for workers (sub-agents, polling, hooks) and
  Unknown (scripts, tools, commands); then attempts and retries, background processes, sub-agent
  time. Every row filters the operations list; a stage row, a phase row, a sub-row and a role
  are mutually exclusive filters.

Raw op-sum is shown next to exclusive time; a "Failures only" toggle lists failed steps and
leaves query misses out.

---

## 10. Insights (`internal/insights`, `/api/insights/*`, `insights.js`)

A period report over the sessions of one project: where the time and the tokens went. Every
card is produced by a named deterministic detector over the derived model; it shows what the
pattern measurably consumed (time and / or tokens with the denominator printed), how it is
spread, what to do (a template bound to the rule, phrased as a change to the harness), and the
evidence (session, lane, interval, op — it opens the timeline there). No estimate of savings,
no threshold on a duration that explains anything, no LLM step (the one fuzzy job — clustering
unknown commands into overlay rules — is handed to the user as a copyable prompt). Plain English
everywhere a reader looks; every visible string lives in `INSIGHT_TEXT` and a test checks
sentence length.

### 10.1 Facts

`Extract(*model.Session) Facts` is a pure function producing a compact per-session record
(5–30 KB): root totals (`by_phase`, `by_lifecycle`, tokens, counts, failed ops, query misses);
agents (path, role, kind `root` / `read_only` / `worker` from literal op content, active
intervals); turns (status, trigger, model, effort, stage, tokens, responses, first call, context
peak, root turn, model-output time and its split by stage); root gaps with what preceded them;
worker waits with the number of sub-agents active; retry groups with their command *shape*
(phase + head word + subcommand of the top-level segment that decided the phase, as the
classifier saw it — past shell keywords, wrappers, env prefixes and `bash -c`, so `set -euo
pipefail; run-tests.sh app` is `test run-tests.sh app`; exact commands almost never recur
across sessions, shapes do); the root's no-telemetry intervals with the turn that never closed
(`Blind`: status open or orphaned, the five longest);
compactions (context, re-read); background ops; the longest verdict-phase ops; failed code-phase
ops with their subgroup; unknown heads and unknown time by subgroup; the main thread's tool calls
by phase and subgroup (calls, exclusive time, query misses, failures); lifecycle × model ×
effort cells; and the **delivery walk** (`facts_delivery.go`): one pass over every op of every
lane in start order — change ops (first, last), the first successful verification at or after
the last change (a completed test op that did not fail, or a stop hook whose command classified
as `test` and raised no hook error: `VerifiedBy` agent / hook) and the distinct kinds of every
such verification in the order they ran (`VerifiedKinds`: what verified the session by the name
the rule table gave it), the test runs and how many failed (a failed test is a verdict, not a
failed tool call), the stop hooks run whatever their command, the last verdict, the end of the
last review run (a review skill run, Codex review mode, a review-role lane) and the change ops
after it, the unknown + no-telemetry time on the root after the last change (the walk may have
missed a test there), every `git push` after a change with what ran between that change and
the push (`Pushes`: a verification or not, test runs in the window and whether the last one
failed, the root's blind time inside the window) and the change ops after the last push, and
per-turn counts (turns with edits, those with no successful verification after their last edit,
unknown time inside the root's change windows). Session scope: a sub-agent's edit is the
session's edit and a sub-agent's test verifies it. Retry windows carry the retry op and what ran
between the failure and the retry (`Recovery`: none — reads and queries only; fix — a change op
on any lane; infra; worker; other — any other step on the lane, a stash, a checkout, a narrowed
test, a build, an unknown command; mixed), whether the retry failed again and whether a user
turn started inside;
compactions whether they sat between two edits of one turn; gaps whether the turn before them
had already changed a file (its own edit, or a sub-agent's inside it); stop hooks are among the
long ops under their command's shape (`hook go test`). `FactsVersion` bumps whenever Extract's
output changes.

### 10.2 The catalogue

Three card classes. An **exposure** card is ranked by the time or tokens the pattern consumed
and adds to its group's total. A **check** says a recorded sequence happened in N of M sessions
(a change with no test after it): no honest time exposure, ranked by sessions affected among
checks, never in a total, its own top list (`top_checks`). An **info** card is a measurement
shown for the honesty of the picture, never ranked. A session the rule's precondition is absent
from (no change op for D17, no review run for D24) is `NotApplicable`: neither in the card's
denominator nor in "no data" — the page says "of M sessions with changes".

| id | group | class | signal (literal structure) | exposure |
|---|---|---|---|---|
| D1 | You and the agent | exposure | a root turn that asked a question, and the gap until the answer; the questions asked by a turn that had already changed a file are a stat | the gap |
| D2 · D2b | You and the agent | info · info | the gap before each user-triggered turn, from 1 s (a shorter gap is a queued message, not a wait; D2b: breaks of 4 h or more) — the user's own pace, never ranked as a loss | the gap, by bucket |
| D3 | You and the agent | exposure | a turn the user stopped (a sub-agent turn stopped with it is a row of its own) | the turn's time and tokens; the share and the group total take the main thread's turns only |
| T1 | You and the agent | exposure | the first model call after a break of 1 s or more: its uncached input | tokens, by gap bucket; stats: the starts after breaks of 15 min or more and their uncached input |
| D4 | Sub-agents | exposure | worker waits during which at most one sub-agent was inside a turn (both harnesses block the thread on the wait call, so the root never works meanwhile); not applicable with fewer than two sub-agents | the wait, keyed "one sub-agent at a time" / "several, one at a time" / "no sub-agent inside a turn" (the lane in the note) |
| T6 | Sub-agents | exposure | a sub-agent's first call (its instructions and context); not applicable without a sub-agent | tokens; how many spent more to start than to work |
| D17 | Verification loop | check | at session end: the session changed a file and no verification (a successful test op, or a test-running stop hook without error) started at or after its last change; not measurable with unknown / no-telemetry time after the last change; not applicable without a change op | sessions, keyed by source, each row over the measurable sessions of its source (`of_<source>`); stats: turns with edits and those unverified, test runs and failed ones, last verdict failed, the first verifying kind of each verified session (`verified_kind:<kind>`) and the sessions verified by a lint / type check / syntax check alone, sessions that ran stop hooks and those verified by one |
| D24 | Verification loop | check | a review run ended before the session's last change op; not applicable without a review run | sessions, keyed by the number of edits after the review (1, 2–5, more) |
| D25 | Verification loop | check | a `git push` with a change op before it and no verification started between the last change and the push (CI after the push is not in the log; an edit to any file counts); a push whose window holds unknown / no-telemetry root time is no data; not applicable without a push after a change | sessions, one finding per push, keyed "no test ran between the last edit and the push" / "tests ran between, none passed"; stats: pushes and unverified ones, pushes after a failed test, sessions with edits after their last push and those edits |
| D7 | Failures and retries | exposure | a retry group with a failed attempt; per failed-to-retry window, what ran between the failure and the retry (only reads, a fix on any lane, an infra step, a wait, another step, several kinds, a user turn) and whether the retry failed again | the retry, fix, recovery and queue time, by shape; a group on a sub-agent lane is parallel time; stats: windows by recovery path, blind test retries that passed |
| D15 | Failures and retries | info | a failed step in the code phase (edit, patch, script, shell, git, hosting, network); query misses and CI status waits excluded | count, by subgroup |

| D16 | Tool calls | info | the main thread's tool calls by phase and subgroup, with query misses and failures | exclusive time, calls (own group) |
| D9 | Long tool runs | exposure | the longest verdict-phase ops and stop hooks, for shapes that ran at least twice in the period | their time, by shape (a hook under its command, marked); the per-run average of the longest shape on the card; sub-agent runs are parallel time |
| D12 | Long tool runs | exposure | background ops | how long they kept running; sub-agent ops are parallel time |
| D11 | Context size | exposure | compaction events; how many sat between two edits of one turn is a stat | their pauses; context before, re-read after; the share over the main thread's time in turns |
| T2 | Context size | info | each turn's context peak | the turn's tokens, by context bucket (a measurement of every turn: never ranked) |
| M1 | Models and effort | info | the main thread's model-output segments × model × effort | that time; the split by the stage each segment served; output of tool-less turns stated apart as "not a stage" (a measurement: the model's work, never ranked as a loss) |
| T3 | Models and effort | info | each agent's tokens × model × effort × agent kind | tokens (a measurement of every token: never ranked) |
| T7 | Models and effort | info | reasoning tokens per effort; a Claude Code session cannot carry the signal (no reasoning usage is recorded) | share |
| D13 | Not measured | info | unknown commands (by head and by subgroup); the unknown time inside the root's change windows and the time with no telemetry are stats | time (always last) |
| D18 | Not measured | info | a root turn that never closed (status open: the log ends inside it; orphaned: the next prompt arrived first) and the no-telemetry time after its last event | that time, keyed by source, one evidence row per interval; never work, never a wait |

Named display conventions (printed on the card, never explaining anything): the long-break
split at 4 h, the 1 s floor under which a gap is not a reply, the gap buckets 5 / 15 / 60 /
240 min, and the context buckets 50 / 100 / 150 / 200 / 500 / 750 k (the scale continues past
200 k because 1M-context models put most turns there).

### 10.3 The report and the scanner

`Build(params, facts)` selects the closed sessions whose last activity lies inside the period
(`7d`, `30d` default, `90d`, `all`, `custom`, `session`; live sessions never count unless asked),
optionally narrowed to sources; runs every detector; aggregates findings per rule and per key
(a finding may stand for several calls, `Finding.N`); computes exposure (`time_ms` over every
lane, the raw op-sum, and `main_ms`, the main thread's part: the number every share, group
total and the time ranking use, because sub-agent turns, runs and compactions run in parallel
and never join a root denominator — product rule 6; the page words the rest as "in parallel"),
distribution (a row carries its own denominator `of` when the rule keys its measurable sessions
through `Result.Key`, as D17 does by source), shares with their denominator, no-data counts ("did
not happen", "could not be measured" and "does not apply" are different: a session that cannot
carry a signal for its CLI version is listed, never counted; a session the rule does not apply
to is in neither count and is reported apart as `not_applicable`, so a check can say how many
sessions changed no files or had no review run); headline numbers come from
the detectors' `Result.Stats` and `Finding.Value`, never parsed from a note; arranges cards in
nine groups ordered by exposure on the chosen axis (time, or uncached input + output tokens),
inside a group exposure cards first, then checks by sessions affected, measurements last, checks
and measurements never in a total; three top lists (time, tokens, checks); keeps the ten best
evidence rows per card. Cross-session cards need three closed sessions in the project; below
that the page shows what each session has and says why.

`Scanner` parses the sessions of a report that have no facts yet — only on an explicit
**Analyze** — on a dedicated path (`Loader`): a private session, one refresh, the model cache
written, the facts sidecar written, the model dropped; a bounded pool, cancellable, progress
polled by the page. It never touches the server's session pools. An overlay change re-analyzes
everything: the rules hash is part of every cache key, and every fact depends on classification.

---

## 11. Validation protocol (ground truth checks; run after classifier or ingestion changes)

The tool's numbers are checked against the raw files by an independent computation, never by
reading the tool's own output. Reports quote real sessions and stay local
(`docs/validation-reports/`, gitignored); only the method is public.

**Per session, computed independently from the raw JSONL with streaming reads** (never load a
whole file; skip lines whose first 200 bytes do not carry a needed type):

1. start (the thread's creation), end (the last line), elapsed;
2. turns: count, completed / aborted, in-turn time, the gaps between turns on the root
   (waiting for the user);
3. user messages on the root (human only, harness injections excluded) and the first text;
4. sub-agent files and their paths (the parent chain);
5. tokens per file (Codex: Σ `last_token_usage` over the records whose total changed; Claude
   Code: once per message id) and per session;
6. commands on all files: count, the 10 longest with exit code, duration and an independent
   category (code, build, test, release, infra, wait, service, unknown);
7. compactions: count and total duration;
8. commands that ended after the `task_complete` of their turn (background);
9. anything else notable: interruptions, errors, long gaps inside a turn, repeated identical
   test / push commands.

**Compare** with `cmd/dump <id> export.json` (started / ended / elapsed, totals, `by_phase`
per lane and overall, turns, user messages, lanes, groups, longest ops with phase / kind / rule,
unknown ops, background ops). Tolerances: times within 2 % or 5 s; counts exact. Both
partitions must balance on every lane (`partition mismatch` never printed).

**Report** per session: a table `metric | todobem | independent | verdict`, classification
disagreements on the longest commands (duration, category vs phase, why), a verdict on every
unknown op ("unknown is fair" or a category), missing / notable items; then `DISCREPANCIES`
across sessions and `SUGGESTED RULES` (concrete, deterministic rows for the table).

History: the 30-session run of 2026-09-12 found and fixed phantom ops from `exec` envelopes,
double-counted user messages with image attachments, replayed turns in forked sub-agent files,
root-only background totals, compaction time overlapping tests, and a list of classifier gaps
(`git -c`, `bash -c`, `env -u`, `make <target>`, `go run` package names, dispatcher flags,
media and diagnostics tools, written-then-executed scripts). After the fixes every counted
metric matched on all 30 sessions; 151 of 62,815 operations stayed `unknown`. The stage rework
of 2026-09-15 was validated on four sessions of both sources against a pre-implementation
simulation (the three Claude Code sessions matched to the second).

---

## 12. Extending

- **A new command rule**: a row in `Rules`, a case in `classify_test.go`; a new phase or kind
  also updates §3.1 / §3.2 here and the subgroup test (every kind of a subgrouped phase must map).
- **A stage pin**: `LifecyclePins` + a case in `userconfig_test.go`.
- **A new Claude Code tool**: a case in `internal/claude/tools.go`, a fixture in `lane_test.go`,
  a row in §2.2 (an unmapped tool is `unknown/tool:<name>`, which is honest).
- **A new insight**: a `Detector` in `internal/insights/detect_*.go` (a literal signal, no
  duration threshold that explains anything, no estimate) with its class (exposure, check, info)
  and its denominator (which sessions are `NotApplicable`), headline numbers in `Result.Stats`,
  a positive and a negative fixture, a text entry in `INSIGHT_TEXT` in plain English, a row in
  §10.2.
- **A new source**: a package under `internal/` emitting `model.*` only, implementing
  `source.Source` with a `LaneParser` per file, registered in `server.NewWithCache` and
  `cmd/dump`, with a name and mark in `filter.js` `SOURCES` and a section in `settings.js`.
- **A schema change**: `model.go`, §3 here, `app.js` and `cmd/dump` together; bump the store's
  `cacheVersion` or the classifier's schema tag so caches re-derive; bump `FactsVersion` when
  `Extract` changes.

---

## 13. Decision record (what shaped the design, with dates)

- **2026-09-12, first review.** Prefix-scan `call_id` instead of decoding tool outputs; discard
  long skipped lines without buffering; cwd in the retry identity; trailing cosmetic pipes
  stripped; a same-identity run that starts before the previous ended is `parallel`; fix /
  infra_recovery / worker_queue roles only when exactly one group awaits its retry; raw op-sum
  next to exclusive time; `update_plan` a marker, not a phase; ordinal-based rewrite detection;
  partition-balance tests. Kept against the recommendation, then changed after real use: the
  stage strip that drew model time in the next call's colour hid that ~90 % of in-turn time is
  the model generating — lane fills show the raw partition.
- **2026-09-17, Markdown in the message panels.** Rendered by default, the recorded text one
  toggle away. No library: `marked` ships without a sanitizer and `markdown-it` is ~100 KB of
  minified code for a narrow vocabulary; a ~470-line renderer of our own is reviewable and its
  output is provable (the invariant test) — measured on the local cache, 13.6 k messages /
  21 MB render in under half a second with no invariant violation. A single newline stays a
  newline (pre-wrap) rather than a `<br>` or a soft break: 26 % of user messages carry one and
  their line structure is part of the record. File references (85 % of link targets) are
  tooltips, not links; the CSP meta is the backstop.
- **From real sessions.** A dev server left running for 33 h painted a session unknown → the
  background rule. Scripts written then executed hid 23-minute remote test runs → classification
  by the literal body. Harness-injected user messages are `system_message`, and idle time before
  a turn they trigger is harness waiting, not the user's.
- **2026-09-14, the lifecycle partition.** Two dimensions, not a replacement (replacing `phase`
  would break priority, retry groups and the validated table); the operations guard as a
  same-lane demotion, not a detector; no built-in design / requirements detectors; skill
  detection from the harness's injection only, never from prose; model-chosen sub-agent task
  names rejected as a source; `reviews` counts invocations. Access control: cookie over
  localStorage, stateless sessions, pre-boot rejection instead of persisted nonces, key re-read
  for live revocation.
- **2026-09-14, Insights.** Deterministic detectors over the derived model; exposure,
  distribution, evidence — no estimates (each rested on something the log does not record);
  facts as a cached sidecar; a scanner on its own parse path; an LLM step considered and
  rejected for the product (handed to the user as a prompt).
- **2026-09-15, sub-agent inheritance and the model-output tail.** A sub-agent turn inherits the
  parent turn's stage by a recorded link only; model output takes the nearest call's stage,
  forward first, backward for the tail; the worker-wait fallback limited to the lane's first turn.
- **2026-09-15, stages by composition (reviewed: rework, taken).** The stage list holds SDLC
  stages only, non-stages under "Outside stages" with the data unchanged (Σ stages = elapsed
  kept: Σ = in-turn was false on the evidence); no absorption of waits and gaps (42 m of polling
  relabelled as implementation on one session); op pins above composition; the plan run ends at
  the first anchor; skill runs from the marker, source-uniform; model-output ops carry their
  segment's stage; one release definition; `OutBytes` dropped; sub-rows under the validated
  phases rather than a new vocabulary; "analysis before the first edit" rejected; the tool-less
  turn a remainder, not a stage; plan mode beats a skill run; CI status waits are query misses;
  the stage band replaces the activity brackets and the rail; `Lane.Stages` removed; Insights
  follow (M1 without a "Model" stage, D15 by subgroup, D13 by unknown subgroup, D16 new).
- **2026-09-16, the verification loop (reviewed by the `pragmatic` agent; the plan is a
  local note, not in the repository).** Cards that ask which earlier work a later recorded event made
  stale: a change with no verification after it, a review followed by edits — as a third card
  class, *check*, ranked by sessions affected and never totalled, with a denominator that leaves
  out the sessions the rule does not apply to. Session scope for verification (a sub-agent's test
  verifies the session's last change; on the first corpus of 38 sessions the choice cost
  nothing). A successful op in phase `test` is verification, so `tsc --noEmit` and the dispatcher
  verb `typecheck` joined `test/typecheck`; build is not. Stop hooks became one op each, classified
  by their command, so a hook that runs the tests is a recorded verification and a hook re-running
  the agent's command joins its retry group. Retry paths (nothing recorded between, a fix, an infra
  step, a wait, a user turn) are stats on D7, not keys: a key is a shape, a path is a window's.
  Rejected on the corpus: "the root worked while waiting" (both harnesses block on the wait call,
  0 root calls inside 161 waits), a wait with no active child (0 ms), a background op open at
  close (0 cases, D12 lists them anyway); deferred for lack of cases: verdict flips with nothing
  between, poll-only turns, search-miss streaks, reruns after a pass; "the turn ended while a
  sub-agent was still working" waits for `is_async` on the marker (every linked case found was
  an agent the user backgrounded on purpose). A policy overlay (required stages, role limits)
  is not planned: conformance to a declared process is another product. Headline numbers moved
  from `Sscanf` over notes to `Result.Stats`.
- **2026-09-16, the report read as a pragmatist (reviewed by the `pragmatic` agent).** On a
  73-session corpus the top findings were measurements of totals (model time, all tokens, every
  turn's context) and the real losses never reached the strip: M1, T2 and T3 became info cards.
  Exposure gained the main thread's part (`main_ms`): D3 had added 8 h of stopped sub-agent turns
  to a root denominator, D9 / D7 / D12 the same with sub-agent runs. A card's "not applicable"
  sessions are counted and printed (D4 needs two sub-agents, T6 one, D17 a change, D24 a review),
  a check row carries its own denominator (D17 by source). Command shapes come from the
  classifier's deciding segment (`test set`, `release -o ServerAliveInterval=30` and `test bash`
  were the first three shapes of the corpus). A gap under 1 s is a queued message, not a reply
  (a sixth of D2's replies; the median doubled). T1's "starts after 15 min" stat had never fired
  (it read a field the finding does not set). D1's "after changes" became turn-level (session-level
  it was 19 of 20 — true of any long session). A Claude Code assistant line with the model
  `<synthetic>` no longer names the turn's model (9 h of model time sat under it). Claude Code
  sessions are not measurable for T7 (no reasoning usage). The 43 h of no-telemetry time the D13
  card printed as one number are a card of their own (D18) with the interval and the turn that
  never closed, one session holding 38 h of it. Exit 130 / 143 is a stopped command, not a
  failed step. The owner's decisions on the same day: an orphaned turn's tail stays
  `no_telemetry` (the log does not say when the agent stopped, so it is neither work nor a
  wait); D2 "time to your reply" is an info card (the user's own pace, not a harness loss; D1
  stays ranked because its remedy is in the harness); D14 "tool calls the harness could not
  parse" is dropped (neither harness records the tool, so the card had no action; the
  `llm_error` marker stays on the timeline); no `todobem insights` CLI for now (`cmd/dump
  -insights` covers the developer's need; the report's sentences live in the page).
- **2026-09-16, a brainstorm of three expert views (QA automation, senior developer, SDLC)
  cut by the `pragmatic` agent** (local notes, not in the repository: 51 proposals, 5 kept for
  the day). D25
  "pushed with no passing test since the last edit" is the one new card: D17 answers the state
  at session end, so a test after the push made the session verified while the push carried an
  unverified change (on the corpus 167 of 407 pushes had no passing test since the last edit;
  keyed to the `git push` kind only — a PR comment is a release op too and must not fire).
  D17 names what verified the verified sessions (the kind the rule table gave the first
  passing check; a lint, type check or syntax check alone is counted apart, 2 sessions of 64),
  counts failed test runs apart from failed tool calls (1,329 of 6,865 test runs failed) and
  the sessions that ran stop hooks at all (81; none verified by one). D7's "nothing recorded
  between" was lane-scoped and read a sub-agent's fix, a stash, a checkout or a narrowed test
  as nothing: the recovery is session-scoped for fixes and names "other" steps; the blind test
  retries that passed are a stat (19 of 42 blind windows), never called flaky. T2's buckets
  continue past 200 k (1,024 of 1,733 root turns sat in one top bucket). D9 says that a Claude
  Code command's time includes an answered permission prompt. Dropped or deferred, with the
  reasons in the critique: a narrowed-test check (0 sessions where a subset run was the last
  verdict), "no review before the push" (fires on 9 of 10 push sessions: a policy, another
  product), stage-shape and commit-batch dashboards (no action), cards that would read "0 of
  M" and never render (max_tokens, userModified, fast mode), everything needing paths on
  operations (batch 2) or per-op tokens.
