# todobem — design notes

Goal: a local tool (`todobem` binary → open in browser) that reads an AI agent's
session logs and shows, in near-real-time, *where the time goes*: a linear timeline
with lanes per agent (root + sub-agents), stage blocks instead of individual tool
calls, retry groups, user inputs, and a helicopter view of the whole session.

First adapter: OpenAI Codex CLI rollouts (`~/.codex/sessions/**/rollout-*.jsonl`).
The normalized schema (SCHEMA.md) is source-agnostic so other adapters (Claude Code
JSONL, OpenTelemetry spans) can be added without touching the UI.

## 1. What the Codex rollout format contains (research, 1856 files, cli 0.134 → 0.154)

One JSONL file per *thread*. Sub-agents are separate threads/files, linked to the
parent via `session_meta.payload.source.subagent.thread_spawn.parent_thread_id`
(+ `agent_path` like `/root/design_review`, `depth`, `agent_role`, `agent_nickname`).

| line `type` | what it is | used for |
|---|---|---|
| `session_meta` | thread id, parent, cwd, git branch, cli version, base instructions (~20 KB) | lane identity, session list |
| `event_msg/task_started` | turn begins (`turn_id`) | turn boundaries |
| `event_msg/task_complete` / `turn_aborted` | turn ends (+`last_agent_message`, `reason: interrupted`) | turn boundaries, final answer |
| `event_msg/item_completed` `item.type=` | | |
|   `UserMessage` | **exact user-typed text** (+`[Image #n]`) with `turn_id` | user-input markers, report |
|   `CommandExecution` | `command`, `parsed_cmd[]` (read/search/list_files/unknown), `exit_code`, `status`, `started_at_ms`, `completed_at_ms`, `duration` | operations (precise timing) |
|   `FileChange` | `changes{path: {type, unified_diff}}` | develop ops |
|   `Reasoning` | `started_at_ms`/`completed_at_ms` (content encrypted) | think ops |
|   `AgentMessage` | `phase: commentary|final_answer`, text | message markers / final answer |
|   `SubAgentActivity` | `kind: started|interacted|completed|interrupted`, `agent_thread_id`, `agent_path` | lane graph, lane active intervals |
|   `CollabAgentToolCall` | `tool: wait|spawn|send_message…` with timing | wait_worker(agent) |
|   `ContextCompaction` | real start/end (≈3 min each!) | compaction ops |
|   `Extension` (`clock.sleep`) | `durationMs` | wait(sleep) |
|   `McpToolCall`, `WebSearch`, `ImageView` | | explore ops |
| `response_item/function_call` + `function_call_output` | `exec_command` (old format, cmd in args), `write_stdin` (polls a running process by `session_id`), `wait_agent`, `spawn_agent`, `send_message`, `followup_task`, `list_agents`, `sleep`, `update_plan`, `request_user_input_async`, `close_agent`, `interrupt_agent`, `view_image`, `_create_pull_request` … | tool-call envelopes; old-format command timing |
| `response_item/custom_tool_call` (+`_output`) | `exec` (JS script that fans out `tools.exec_command` in parallel), `apply_patch` | envelopes |
| `response_item/message` role user/assistant/developer | `content_item_kinds` distinguishes `user.text` from injected `agents_md.instructions`, `environment_context` | fallback for user text |
| `response_item/agent_message` | `author`/`recipient` (`/root/x` → `/root`) | result-returned markers |
| `compacted` | replacement history, **up to 5 MB per line** | skipped without decoding |
| `event_msg/token_count` | `total_token_usage` (cumulative), `last_token_usage` (the call just made), written more than once per call, restarts on resume, all-zero around compactions | tokens per lane, turn and compaction (`codex/tokens.go`) |
| `turn_context` | model, reasoning effort, collaboration mode per turn | turn model/effort, plan mode |
| `token_usage_record` (CLI ≥ 0.153, one per model call) | `turn_id`, `root_turn_id`, per-call / per-turn / per-thread usage | `root_turn_id` on sub-agent files only (the harness's link from a sub-agent turn to the root turn) |
| `world_state`, `inter_agent_communication_metadata` | | skipped |

Facts that shape the design:
* Command timing is exact in new files (`started_at_ms`/`completed_at_ms` match `duration`); in old files (< ~0.150) only the call/output timestamps exist and a long process is polled with `write_stdin{session_id}` → the op must be stitched from `exec_command` + its polls.
* Commands inside one `exec` call run **in parallel** (`Promise.allSettled`) → intervals inside one lane overlap; exclusive wall-clock needs a partition, not a sum.
* Between `task_complete` and the next `task_started` nothing is written: that is the user's time (thinking / away). Inside a turn, uncovered time is the model generating (the next line is always a model output), plus tiny harness overhead.
* A sub-agent is *long-lived*: one `started`, then many `interacted → completed` cycles over hours.
* The same test/push command is re-run many times; `git push` here takes ~330 s every time (pre-push hook), a `while ! ssh buildhost test -d .remote-test-lease` loop waited 70 min for a remote worker, and 16 compactions cost ≈48 min. These are the things the tool must make visible.

## 1b. What a Claude Code session log contains (research, 165 root + 11 agent files, CLI 2.1.226 → 2.1.270)

Layout: `~/.claude/projects/<encoded cwd>/<session-id>.jsonl` is one root session; a sub-agent
spawned by the Agent tool is `<session-id>/subagents/agent-<id>.jsonl` next to
`agent-<id>.meta.json` (`agentType`, `name`, `description`, `model`, `spawnDepth`, `toolUseId`);
`<session-id>/tool-results/` holds persisted tool outputs and is never read. Every agent file,
a nested agent's too, sits directly under the root's `subagents/`: the lane tree is flat.
Legacy interleaved `isSidechain` lines: none seen.

There is no session header. Every *message* line (`user`, `assistant`, `system`, `attachment`)
starts `{"parentUuid":…,"isSidechain":…` and carries `cwd`, `gitBranch`, `version`, `sessionId`,
`timestamp` and `uuid` — for `assistant`/`attachment` lines **after** the payload, for `user`
lines before a possibly multi-megabyte `toolUseResult`. Small metadata lines start
`{"type":"…"` (`ai-title`, `last-prompt`, `mode`, `permission-mode`, `atis-latch`, `agent-name`,
`queue-operation`, `cost-state`, `file-history-*`, `pr-link`) and are re-emitted every ~30 s.
So the index types metadata lines by prefix and decodes message lines in full (an attachment's
timestamp is read from its tail); the largest line seen is 2.8 MB.

| line | what it is | used for |
|---|---|---|
| `user` with a string or `text`/`image` content | the prompt: the user's (`promptSource: typed\|queued`, `origin.kind: human` on CLI ≥ 2.1.26x), or the harness's (`<task-notification>`, `<teammate-message>`, `origin.kind: task-notification\|auto-continuation\|peer`, `promptSource: system`) | turn start (Trigger user / system), user_message / system_message markers |
| `user` `<command-name>…</command-name>` | a slash command as typed; `<local-command-stdout>` or `system/local_command` follows when the harness answered it itself | a user_message; a turn only when the model ran |
| `user` `[Request interrupted by user…]` | the user stopped the turn | turn aborted (a harness prompt the model never answered is dropped) |
| `user` `isMeta`, `<local-command-caveat>`, `[Image: …]`, `isCompactSummary` | attachments and echoes | evidence only |
| `assistant` | one line per streamed content block, all lines of a message carrying its `id`, `model`, `stop_reason` and the same `usage`; blocks `thinking`, `text`, `tool_use {id, name, input}` | tool-call ops (start), message ops, tokens (once per message id), turn end at the last `end_turn` block (deferred until the next line proves the message over; a tool_use in it keeps the turn open), `isApiErrorMessage` → llm_error |
| `user` with `tool_result {tool_use_id, is_error, content}` (+ `toolUseResult`) | the tool's answer; Bash failure = `is_error` with content `Exit code N`; `returnCodeInterpretation: "No matches found"` for grep misses; `interrupted`, `timedOutAfterMs`, `backgroundTaskId`; Agent results carry `agentId`, `isAsync` | op end / exit / status; the sub-agent lane link; background polls |
| `system/stop_hook_summary` | the Stop hooks (`hookInfos[].durationMs`) after the model's last message | a wait_worker/hook op inside the turn; the turn ends when they are done |
| `system/turn_duration` (newer CLIs) | `durationMs` = turn wall clock (verified) | evidence only |
| `system/compact_boundary` | `compactMetadata {trigger, preTokens, postTokens, durationMs}` | compaction op (start clamped to the last evidence), context = preTokens |
| `system/local_command`, `away_summary`, `informational`, `agents_killed`, `model_refusal_*` | | evidence (refusals: llm_error) |
| `attachment` (`hook_success`, `total_tokens_reminder`, `skill_listing`, …) | harness context | evidence only, never decoded |
| `ai-title` | the harness's own title (one per file, first within the first exchange) | session title (else the first prompt) |

Facts that shape the adapter:
* Tool timing is call line → result line; there is no recorded exit code, only `is_error` and
  the `Exit code N` text for Bash. A failed query tool (Read of a missing path) is therefore a
  query miss by status, not by exit (model rule, both sources).
* A message's block lines are written together once the message is complete (each with the
  block's own timestamp), so a file ending on an `end_turn` line is a finished turn; the parser
  closes it at end of file and re-opens it only if a later read brings another block of the same
  message.
* `AskUserQuestion` keeps the turn open while the user answers: the call → result span is a
  wait_user/question op (Codex ends the turn instead). Background Bash returns at once with a
  task id; `TaskOutput`/`Monitor` polls of that id extend the Bash op, as Codex polls do.
* Queued prompts are stamped at delivery (tens of ms after the previous `end_turn`), so a prompt
  line is always the start of its turn. A prompt delivered as the session is closed, followed at
  once by the interrupt, never ran: it is not a turn.
* 4 compactions in 165 files; `Artifact` and `ReportFindings` are the only tool names that stay
  `unknown` (a classification decision, see `todobem unknown`).

## 2. Classification (deterministic, documented in SCHEMA.md)

Two layers:

**Per-lane state machine** (turn/model/tool/idle) labels every instant of a lane with
exactly one *base state*. It uses only event order, timestamps and turn ids:

```
outside turn                      → wait_user (root) | idle (sub-agent)
turn open, awaiting model output  → think          (next line is a model output)
turn open, call sent, no output   → tool envelope  (overridden by the concrete op inside)
turn open, no more lines yet      → no_telemetry(open_turn)   (live: "in progress, no events since …")
turn never closed, next turn/meta → no_telemetry(orphaned_turn)
```

**Operation classifier** turns each tool item into an `Operation{phase, kind}`:
* `FileChange`/`apply_patch` → develop(edit); `update_plan` → plan marker;
  `Reasoning` → think(reasoning); `ContextCompaction` → compaction;
  `wait_agent`/`CollabAgentToolCall wait` → wait_worker(agent); `sleep` → wait_worker(sleep);
  `McpToolCall`/`WebSearch`/`view_image` → explore.
* `CommandExecution`/`exec_command`: Codex `parsed_cmd` read/search/list_files → explore.
  Otherwise the shell text is split into top-level segments (`&&`, `;`, `|`, `||`, newline;
  heredoc bodies are opaque), each segment's head word (+ subcommand for git/gh/glab/docker/
  xcrun/npm/cargo/go/swift/python) is matched against a rule table; the op takes the
  **highest-priority** phase among segments: release > test > build > infra > develop > explore.
  `while … sleep` loops, `gh run watch`, `gh pr checks --watch` → wait_worker(ci|lease).
  Heredoc scripts (`python3 - <<PY`): develop if the body writes files (`write_text(`, `open(…,'w')`),
  else **unknown**. Anything unmatched → **unknown** (never guessed).
* Phase priority also resolves parallel overlaps within a lane when building the exclusive partition.

**Retry / iteration groups** use only explicit identity: the normalized command string
(redirections `> file 2>&1` and `| tee` stripped, env prefixes kept) of test/build/release ops
and of infra ops whose kind is a verdict (`docker`, `kubectl`, `ssh`, `brew`, … — a literal
allowlist, `classify.infraAttemptKinds`; `kill` exits 1 when nothing matched, so routine kinds
stay out; added 2026-09-14 after a failed `docker run …` and its identical retry proved invisible
as a retry). ≥2 occurrences in the session → a group; attempt N = N-th occurrence in start
order; an infra group additionally needs a failed attempt (all-successful repeats such as
`ssh host 'cat lease.json'` ×7 are polls, not retries).
Kinds: `first`, `retry_after_failure` (previous attempt exit≠0), `rerun` (previous passed/aborted).
Develop/explore ops between a failed attempt and the next attempt of the same group → kind `fix`;
infra ops there → `infra_recovery` (unless they are attempts of a group themselves); wait ops
there → `worker_queue`. No text similarity, no duration.

**Lifecycle: the second partition (SDLC stages)**. The user asked for code review "as a phase
like Coding" and a breakdown shaped after the SDLC (planning, requirements, design,
implementation, review, testing, release, maintenance). A single enum cannot say "this `go test`
ran inside a code review" without losing either the activity (retry groups, kinds, the validated
rule table) or the purpose — so every segment carries two labels, `phase` (what the call was) and
`lc` (which stage it served), each an exclusive partition of the same segments
(`sum(by_lifecycle) == sum(by_phase) == elapsed_ms`, tested). Signals are harness-level and
literal only (corpus of 2,175 rollouts checked, 2026-09-14): Codex plan collaboration mode
(`turn_context.collaboration_mode.mode`, enum `plan|default` — 0 plan turns seen so far),
`update_plan` calls creating a plan (1,075 calls), the harness's `skills.selected_skill_instructions`
injection (`code-review-cc` 358×), Codex review mode, a sub-agent's spawn `agent_role`
(`pragmatic` 905× — a user-defined reviewer, so overlay-mapped, not built-in), PR/MR review verbs,
and a short list of operations kinds (logs, service control, diagnostics). Model-chosen sub-agent
`task_name`s (review 594×, design 63×) are prose and rejected as a source. Nothing in a Codex
rollout marks requirements or design: those stages exist, the guide says "no built-in detector",
and the overlay's `lifecycle.skills/roles/paths` can pin them. A turn signal covers the whole
turn (tests, waits, model output) and, because a sub-agent is its parent turn's tool call, the
sub-agent turns that ran inside it (inherited downward, never upward); otherwise model output
takes the stage of the nearest tool call in the turn — the next one, else the previous one —
and only a turn without any tool call keeps an honest `llm` bucket (both amended 2026-09-15 after
a code-review session showed 33 review sub-agents as "implementation" and their findings as
unattributed model output; `docs/REVIEW-lifecycle.md`). The order-dependent rules are the
operations guard — an operations candidate before the lane's first release op is implementation —
the user's "maintenance cannot precede coding" constraint, made a same-lane demotion rather than a
detector after the design review, and, since the 2026-09-15 stage rework (`docs/REVIEW-lifecycle.md`),
the turn's composition read as a group: a skill run from its marker to the turn's end (Claude Code
invokes skills mid-turn), the plan run before the turn's first plan anchor, and the change window —
every code, build, test and infra call between the turn's first and last edit is implementation, so
a test between two edits is the loop and only the verification pass after the last edit is testing.
Waiting, compaction, telemetry gaps, unknown commands and the model output of a tool-less turn are
not stages: they keep their own key in the data (the partition still sums to elapsed) and the UI
lists them under "Outside stages". The breakdown splits Development, Waiting for workers and Unknown
into sub-rows (`classify.Subgroup`, a function of phase and kind carried on every op as `sub`).

**Two detection sources**: classification is built-in plus an optional user overlay
(`--rules`/`$TODOBEM_RULES`/`~/.todobem/rules.json`) for project-specific commands, lifecycle
pins on those commands, and the skill / role / path matchers, appended to the same tables and
served at `/api/rules` (SCHEMA.md).

**The coarse view**: the timeline's band above each lane is the lifecycle partition, drawn as
runs of consecutive same-stage segments where it is rendered; the fill is the raw phase
partition. The earlier "stage blocks" (think segments attributed to the phase of the next tool
op, coalesced) were retired on 2026-09-15: they were a third grouping of the same time.

## 2b. Insights — the period report (2026-09-14)

*Where is my harness inefficient?* is answered by `internal/insights`, a layer over the derived
model that never reads a rollout: `Extract(*model.Session) Facts` builds a compact per-session
record (turns with tokens, gaps with what preceded them, waits with sub-agent concurrency, retry
groups with their command shape, compactions with context and re-read, long and unknown ops,
lifecycle × model × effort cells); every rule of the catalogue (`detect_*.go`) is a pure function
`Facts → Result{Findings, Measurable, Reason, NoData, Stats}`; `report.go` selects the closed
sessions of a project whose last activity lies inside the period (live sessions never count),
aggregates findings per rule and per key (a command shape, a gap bucket, an agent type), computes
exposure, distribution and the printed denominator, arranges cards in eight groups ordered by
exposure on the chosen axis (time; or tokens not from cache + output — cached input and reasoning
are shown, never weighted) and keeps the ten best evidence rows per card. `Info` cards (long
breaks, counts, shares) are measurements: shown, never ranked or totalled. No estimate of savings
exists anywhere (the design review showed each one rested on something the log does not record);
a card without evidence is not rendered; "did not happen" and "no data" are separate counts.

Cross-session keys are command *shapes* (phase + head word + subcommand): exact commands almost
never recur across sessions (temp paths, MR numbers — 0 of 662 in the largest project), shapes do
(7 of 40 failing shapes in 2+ sessions). Retry groups themselves stay exact (product rule 5).

Facts are cached as a sidecar of the model cache (`<id>.facts.json.gz`, same fingerprint plus
`FactsVersion`): a report over 47 cached sessions builds in well under a second; sessions with no
cache are "pending" and are parsed only on the page's Analyze, by a scanner with its own parse
path (a private `codex.Session`, refreshed once, model cached when closed, dropped after
Extract) that never touches the server's session pools. An LLM step in the product was
considered and rejected: it would trade "nothing leaves the machine" for one job (clustering
unknown commands) the user can run in their own agent from the card's list.

## 3. Lanes and aggregation

* Lane = one thread. Root lane + one lane per sub-agent (nested depth via `agent_path`).
  Each lane: turns, ops, markers, exclusive `segments[]`, stage blocks, active intervals.
* Sub-agent lane shows spawn (`started`), each `interacted → completed` cycle, `interrupted`,
  result messages (`agent_message` author→recipient), and its own ops/stages.
* A sub-agent's prompt is the first message the thread received. Before CLI 0.144 it is a
  plain user-role message in the child rollout (the `spawn_agent` arguments in the parent carry
  the same text). From 0.144 the child gets a `NEW_TASK` `agent_message` whose payload is an
  `encrypted_content` part, and the parent's `spawn_agent` / `send_message` arguments are
  encrypted too (`gAAAAA…`, verified 0.144.1 → 0.154.0 on local rollouts): no plaintext exists
  on disk. The UI shows the header and says the payload is encrypted; it never guesses.
* **Session totals = root lane partition** (exclusive wall-clock). Sub-agent time is reported
  separately: `parallel.agent_seconds` (sum over sub-agent active time) and
  `parallel.wall_seconds` (union of sub-agent active intervals). Never added to root totals.
* UI: collapsed lane = 1 row of stage blocks; expanded lane = phase rows with ops. Only
  lanes whose lifetime (spawn → last turn end, or now while live) overlaps the visible window
  are drawn; the root lane always is.
  Zoomed out → time buckets (dominant stage colour, opacity = coverage); zoomed in → blocks.

## 4. Incremental ingestion, low CPU

Both sources share the joiner (`internal/source.Session`): one `LaneParser` per file, bytes
consumed by offset, a rewritten or truncated file restarts its lane, one derive per change.

* Go, single static binary, no deps. `todobem` serves `http://127.0.0.1:7788`.
* Session list: `session_meta` is line 1 of every file → read only the first line (≤ 30 KB),
  cache by (path,size,mtime). Names from `<home>/session_index.jsonl`. Root threads only;
  sub-agent files are attached to their parent.
* **Several Codex homes** (`internal/settings`, 2026-09-14): the index scans the `sessions/` and
  `archived_sessions/` of every home in `~/.todobem/settings.json` (`{"codex_homes": [...]}`;
  absent → `~/.codex`; `-codex` pins one home for the run and makes the Settings page
  read-only). Only the Codex home layout is walked — a home pointed at `/` or `~` by mistake is
  reported as "no sessions/ folder", never walked. The thread-id map is rebuilt after every scan,
  homes in configured order, paths sorted, first valid file wins, and `Roots`/`Descendants`/`IDs`
  read that map: a rollout copied into two homes is one session (from the first), not two rows
  with doubled sub-agent counts; names follow the same order. `SetHomes` drops the files of a
  removed home inside the index's critical section, the server then clears its in-memory pools
  (a parser pins its path, and would keep serving a removed home until eviction), cancels a
  running Insights scan, rescans synchronously and logs the change; the source reader accepts a
  span only from a file under a current home's rollout directories (logical path with no `..`
  and no symlink below the home). The disk cache is keyed by thread id with the file paths in
  the fingerprint, so a session read from another folder misses and re-parses.
* Open session: parse root + descendant files streaming line-by-line. The line type is read
  from the prefix (`"type":"…"`); skip-set lines (`compacted`, `world_state`,
  `inter_agent_communication_metadata`) are never JSON-decoded, and long skipped lines are
  discarded chunk by chunk without buffering; the small per-call lines (`token_count`,
  `turn_context`, and `token_usage_record` on sub-agent files) are decoded for tokens, model
  and effort. Tool-output lines (52 MB in the sample) are
  reduced to their `call_id` by prefix scan. Measured on the 151 MB sample (286 MB with
  sub-agents): 40% of bytes decoded, 1.5 s wall, 63 MB RSS.
* Each file keeps `offset` (end of last complete line) and the last `ordinal`. Refresh = `stat`;
  if size grew, read only the tail; a partial trailing line is left for next time; a smaller
  file or a non-monotonic ordinal means the file was rewritten → reparse from 0. Derived data
  (partition, groups, stages, totals) is recomputed in O(ops log ops) — ms for 10k ops — only
  when a file grew.
* New sub-agent files are discovered on refresh through the index (a full rescan of the
  sessions tree is ~10 ms when nothing changed; only new/changed files have line 1 re-read).
* Raw events are not kept in memory. Each op stores `(file, byte offset, length)`; the inspector
  fetches the exact source line on demand (`/api/event`).
* Client polls `/api/sessions/{id}/version` (≈ 200 B) every N seconds (default 30 s, "Follow"
  mode) and re-fetches the model only when the version changed. Server does nothing between polls.
* Response is gzip-compressed; model for the 150 MB sample ≈ 1–2 MB JSON.
* **Persistent cache (`internal/store`)**: the derived model is written to disk (gzipped JSON,
  one file per session, under `~/.todobem/cache`, `-cache`/`$TODOBEM_CACHE`, `off` to
  disable). A default open serves the cache when its fingerprint still matches, so an unchanged
  session is returned without parsing (measured on the 354 MB sample: ~3.7 s cold parse → ~0.16 s
  cache load across a restart). The fingerprint is the source files (path/size/mtime, from the
  index — no parse) **plus a hash of the effective classifier** (rule table + lifecycle pins and matchers +
  schema version): if a file grew or a rule changed, the entry is stale and the session is
  re-parsed automatically — the cache never serves a wrong number or an outdated classification.
  The Refresh button (`?refresh=1`) forces a full re-parse and rewrites the cache. Live sessions
  are never cached (their files change every poll) and cache hits are held in a separate, larger
  pool so browsing history never evicts a live parser-backed session. `Operation.Detail`
  (`json:"-"`, kept out of the payload) travels in a sidecar map inside the cache file so the
  inspector's per-op detail works from a cache hit without re-reading the source line.

## 4b. Access control (lite authentication, 2026-09-14)

The viewer shows whatever the agents read and wrote, so a loopback bind alone was not enough:
anyone at an unlocked laptop, any local user on a deploy host reached through `ssh -L`, saw
everything. Threats covered: a stranger at the browser; other local users on a shared host; a
foreign page in the same browser riding the session (CSRF); a script injection in our own UI
(it renders agent text through `esc()`, but defense in depth). Not covered: same-user malware —
it can read the key and the rollouts anyway.

Design (`internal/auth`, `internal/server/auth.go`, `todobem token`):
- **Key file is the root of trust.** `~/.todobem/auth.key`, 32 random bytes, 0600, refused if
  group/world-readable, created by whichever of server or CLI runs first (`O_EXCL`, re-read on
  `EEXIST`). Reading it is what authorises minting — the CLI never contacts the server.
- **One-time token** (52 chars base32): `nonce ‖ issued ‖ session-ttl ‖ HMAC[:16]`. Accepted
  within 5 minutes of minting, once (in-memory nonce set), and never if minted before the server
  booted — that comparison closes the replay window a restart would open without persisting
  nonces. Typed tokens are case-insensitive.
- **Session** is stateless: `expiry ‖ nonce ‖ HMAC`, in an HttpOnly, SameSite=Strict cookie,
  Path=/api, distinctive name, Max-Age only (no Secure — Safari drops it on plain http — no
  Expires, no Domain; the expiry is inside the signed value). Cookie over localStorage because
  an XSS bug could read localStorage and exfiltrate a bearer; it cannot read this cookie.
  Survives restarts; revocation = rotate the key (`todobem token -revoke`), which the running
  server notices on the next request because it re-reads the key file when its mtime/size
  changes. Token and session MACs are domain-separated by a type byte.
- **CSRF**: login/logout are JSON POSTs — a foreign page cannot send `application/json` without a
  preflight, and we answer no CORS; `mime.ParseMediaType` decides, so `text/plain` and form
  bodies are 415; non-POST is 405; a browser-declared `Sec-Fetch-Site: cross-site` is 403 on
  every `/api/*`; the host check already closes DNS rebinding. `POST /api/settings` (the Codex
  homes) sits behind the same gate and guard: it is the first UI write that outlives the process,
  and with `-auth=off` anyone on the port can re-point the server at another folder of
  `rollout-*.jsonl` the server's user can read — the startup line and the per-change log line
  are the audit trail, and the blast radius stays "rollouts served through this same gate".
- **Static files stay open** (no data in them); the lock screen is the SPA on a 401. The
  token never reaches the server's stdout (a log file may be world-readable on a shared host):
  `deploy.sh` mints it to the terminal and chmods the log 600; `-open` passes it straight to the
  browser in the URL fragment, which the page scrubs with `history.replaceState`.
- Residual, documented: cookies are not port-scoped, so a local and a tunnelled todobem on the
  same port share a cookie jar with different keys — use different ports.

Review record: approve with conditions (all taken): cookie not localStorage; stateless sessions;
pre-boot rejection instead of persisted nonces; `mime.ParseMediaType`; no failure delay
(goroutine-per-request rate-limits nothing); key re-read for live revocation; harness extensions
for the 401 path.

## 5. Review outcomes (pragmatic review, 2026-09-12)

Accepted: prefix-scan `call_id` instead of decoding tool outputs; discard long skipped lines
without buffering; `cwd` is part of the retry identity; trailing cosmetic pipes (`| tee`,
`| tail`, `| grep`, `| xcbeautify` …) are stripped and nothing is truncated; a same-identity
run that starts before the previous one ended is `parallel`, not a retry; `fix` /
`infra_recovery` / `worker_queue` roles are assigned only when exactly one group is
"failed and awaiting retry" on that lane; raw op-sum is reported next to exclusive time;
"inside turns only" toggle; `update_plan` is a marker, not a phase; ordinal-based rewrite
detection; partition-balance tests; CLAUDE.md with the non-inference rules.

Kept against the recommendation at first, then changed after real use: the stage strip that
drew LLM time in the next tool call's colour hid that ~90% of in-turn time is the model
generating. Lane fills now show the raw partition; stages were brackets above the fill until
2026-09-15, when the lifecycle partition took the band: a bracket attributed model time to the next
tool call by phase, the band says which stage the time served (model / tools split in its tooltip). Test sub-kinds
(first / retry-after-failure / rerun / fix / queue / infra) were an explicit requirement.
Old-format `write_stdin` polls are stitched into their process so a polled test is one test op.

### Findings from real sessions after the review
* A `npm start` dev server left running for 33 h painted a whole session "unknown" - hence the
  background rule (op outlives its turn => excluded from the partition, shown as a thin bar).
* Scripts that write a shell file and immediately run it hid 23-minute remote UI test runs
  behind "script-write" - hence written-then-executed classification by the literal content.
* Harness-injected user-role messages (`<codex_internal_context source="goal">`,
  `<subagent_notification>`) are `system_message` markers; idle time before a turn they
  trigger is harness waiting, not the user's.

## 6. What is *not* inferred
* No cause for a delay is derived from its duration. Long ops are simply listed.
* User intent achievement is never computed; user messages and final answers are shown verbatim.
* Unknown intervals are "No telemetry"; unmatched commands are "Unknown".
* Retry membership never uses text similarity.
