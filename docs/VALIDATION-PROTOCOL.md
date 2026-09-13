# Validation protocol for todobem summaries

You are checking a tool ("todobem") that analyses OpenAI Codex CLI session logs. For each
session id you are given, a file `<id>.json` in this directory holds todobem's OUTPUT
(its summary). You must compute the ground truth INDEPENDENTLY from the raw log files
and report every discrepancy. Do not trust the summary; verify it.

## Raw format (facts, verified)
Each session = one root JSONL file (`root_file` in the summary) plus one file per sub-agent
thread (`lane_files`). Each line: `{"timestamp": ISO8601, "type": T, "payload": {...}}`.
Lines can be several MB (`type: compacted`) — skip lines whose first 200 bytes don't contain the
types you need. Use Python with streaming line-by-line reads; never load a whole file.

Relevant lines:
- `type=session_meta` (line 1): `payload.id` (thread id), `payload.timestamp` (thread start),
  `payload.parent_thread_id` / `payload.source.subagent.thread_spawn.parent_thread_id` for sub-agents.
- `type=event_msg`, `payload.type=task_started` / `task_complete` / `turn_aborted` (with `turn_id`):
  turn boundaries. Time between a `task_complete` and the next `task_started` is time with no
  agent activity (the user is typing / away).
- `type=event_msg`, `payload.type=item_completed`, `payload.item.type` in:
  - `UserMessage` — user text in `item.content[].text`. Texts starting with `<codex_internal_context`,
    `<subagent_notification`, `<environment_context`, `# AGENTS.md` are harness injections, not the human.
  - `CommandExecution` — `item.command` (argv; the shell command is the last element), `item.exit_code`,
    `item.status`, and precise timing at the PAYLOAD level: `payload.started_at_ms`, `payload.completed_at_ms`.
  - `FileChange` — file edits (`item.changes`), instantaneous.
  - `Reasoning`, `AgentMessage` — model output, timed by `payload.started_at_ms/completed_at_ms`.
  - `ContextCompaction` — harness pause, timed the same way.
  - `SubAgentActivity` — `item.kind` started/interacted/completed/interrupted, `item.agent_thread_id`, `item.agent_path`.
- `type=event_msg`, `payload.type=token_count`: `payload.info.total_token_usage.total_tokens` is the
  CUMULATIVE token total of that thread (take the last one per file). May be `info: null` sometimes.
- Old files (< cli 0.147): commands are `type=response_item, payload.type=function_call, name=exec_command`
  (cmd in `payload.arguments` JSON) paired with `function_call_output` by `call_id`; no CommandExecution items.

## What to compute per session (independently)
1. start (session_meta timestamp), end (timestamp of the last line), elapsed.
2. turns: number of task_started; number completed / aborted; sum of in-turn time; sum of gaps
   between turns (= waiting for user) on the ROOT file.
3. user messages on the root: count (human only) and first text (first 100 chars).
4. sub-agent threads: how many files belong to this session (parent chain), and their agent_path.
5. tokens: sum over root + sub-agent files of the last cumulative `total_tokens`.
6. commands on all files: count; the 10 LONGEST by completed_at_ms - started_at_ms, with the command
   text (first 200 chars), exit code, duration, and YOUR OWN category for each:
   code (reading/searching/editing sources, local git), build, test, release (push/PR/deploy/install),
   infra (docker/ssh/processes/cleanup), wait (sleep/polling loops/CI watch), service (dev server that
   keeps running in background), unknown.
7. compactions: count and total duration over all files.
8. commands that ended AFTER the task_complete of the turn in which they started (background processes).
9. anything else significant you notice: interruptions, errors, very long gaps inside a turn with no
   events, repeated identical test/push commands (retries).

## Compare with todobem's summary
Fields in `<id>.json`: started/ended/elapsed, totals (in_turn_ms, user_messages, compactions,
background_ops, tokens), root_by_phase / all_lanes_by_phase (seconds per phase: llm, code, build,
test, release, infra, wait_worker, wait_user, idle, compaction, no_telemetry, unknown), root_turns,
user_messages, lanes, groups (retry groups = identical normalized command run ≥2 times),
longest_ops (with todobem's phase/kind/rule and the raw command), unknown_ops, background_ops.

Tolerances: times within 2% or 5 s; counts exact.

## Report format — write it to `docs/validation-reports/report-<batch>.md` (gitignored: it quotes real sessions)
For each session: a table `metric | todobem | independent | verdict (OK / MISMATCH / MISSING)`
covering items 1–8, then:
- `Classification disagreements`: for each of the 10 longest commands where your category differs
  from todobem's phase, one line: duration · your category vs todobem phase · why (quote the command).
- `Unknown ops`: for each entry of todobem's `unknown_ops`, your category if you are confident, else "unknown is fair".
- `Missing / notable`: things the summary should have shown but does not.
End the file with a section `## DISCREPANCIES` listing every MISMATCH/MISSING across your sessions
(session id, what, todobem value, true value) and `## SUGGESTED RULES` (concrete, deterministic
classification rules that would fix the disagreements, e.g. "head word X → phase Y").
Be terse and factual. No praise. If everything matches, say so per metric.
