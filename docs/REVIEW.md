# todobem design review — verdict

**Recommendation: approve with conditions.** The lane/partition model and the Go
architecture are right. Drop `stages[]` + think-attribution, tighten retry identity,
fix four ingestion details. Line refs are against the code as of 2026-09-12.

## Evidence base (measured on this machine, not inferred)

Sample: `~/.codex/sessions/2026/09/10/rollout-2026-09-10T12-06-40-…jsonl` (151 MB) plus a
first-line tally of all 1856 files (6.4 GB).

- Timing is exact: 2719/2719 `CommandExecution` items have payload-level
  `started_at_ms`/`completed_at_ms` matching `duration` within 60 ms.
  `Reasoning`/`AgentMessage` carry the same fields → think is an explicit op.
- 714/1805 custom `exec` calls (40%) fan out ≥2 commands (3355 sub-commands).
  Root-lane raw op-sum = 974 min vs 894 min in-turn wall-clock → summing overcounts 45%.
- Uncovered in-turn time = 3.7 h of 14.9 h (25%): 3039 gaps, median 3.3 s, p95 12 s,
  max 150 s. Between-turn (user) time = 10.7 h of 25.6 h (42%).
- Phase coalescing simulated on the sample: think→next-op gives 2099 blocks;
  think-as-transparent gives 2098. Median block 13 s; 70% < 30 s; 46 > 5 min.
- `write_stdin` exists only in files ≤ 0.145.0 (~15% of files, ~1 GB, none recent).
  A 0.142.5 file: 1357 `exec_command` calls, 0 `CommandExecution` items, 3 polls.
  Every CLI version emits `item_completed`.
- 1580/1856 files are sub-agent threads (~5.7 per root).
- Byte share in the sample: `compacted` 52 MB, `custom_tool_call_output` 52 MB
  (1805 lines, 58 KB avg), `item_completed` 29 MB, everything else < 10 MB.
  Top-level skip set removes 37% of bytes, not the 55% stated in DESIGN.md §4.

## Q1 — Two-layer classification: keep the partition, drop the "state machine" layer

Not over-engineered in substance; over-described in form. The state machine exists only
to fill three uncovered cases. Model it as one sweep-line over explicit intervals:

- turns = `[task_started, task_complete|turn_aborted]`
- ops = every timed item (Reasoning/AgentMessage → think; tool items → classifier)
- sweep with priority tie-break; default fill = `outside_turn` (no open turn),
  `think(kind=gap)` (inside a turn, uncovered), `no_telemetry` (open-turn tail / orphaned)

Incremental = append ops + rerun the sweep (`internal/model/derive.go` is already
O(n log n)). Only resumable state: per-file byte offset + unmatched call_ids.
No simpler model gives exact exclusive accounting with 40% parallel fan-out.

Condition: show both raw op-sum and exclusive totals in the UI
("974 min of ops in 672 min wall-clock") or users will report the totals as broken.

## Q2 — Think → next-op attribution: cut it, and cut `stages[]` with it

It changes `by_phase` totals by inference ("the model was deciding what to do next" is a
cause claim) and buys nothing visually (2099 vs 2098 blocks).

Larger problem: phase coalescing does not produce stages — 2099 blocks over 26 h,
median 13 s. Remove `buildStages` (`internal/model/derive.go:203-257`) and
`Lane.Stages` (`internal/model/model.go:97`). Replace with:

1. Client-side pixel bucketing: bucket colour = highest-priority phase present (same rule
   as the partition, so it is consistent); opacity = tool coverage. Think/gaps transparent.
2. Explicit brackets — these are the real stages and need no inference: turn spans,
   retry-group spans ("swift test: 6 attempts, 3 failed, 42 min"), sub-agent delegation
   spans (spawn→complete drawn on the parent lane), compactions, `while … sleep` waits.

Uncovered in-turn time: phase `think`, `kind=gap`, documented as a convention
("no event; by log structure the harness is awaiting model output"). Explicit items get
`kind=reasoning|message`. by_kind then shows verified vs by-convention think.

## Q3 — Retry identity: keep exact-match-after-normalization; fix five failure modes

Exact string equality after mechanical normalization is an explicit identifier.
Fix in `internal/classify/classify.go:302-310` (`Identity`):

1. No `cwd`. `CommandExecution.cwd` exists; this project uses worktrees, so `swift test`
   in worktree A and B merge into one group with bogus attempt numbers. Prepend cwd
   (`internal/codex/lane.go:679` has the item).
2. `s[:400]` truncation (`classify.go:306`) collapses distinct heredoc scripts sharing a
   prefix. Hash the full normalized string instead.
3. Only `| tee` is stripped (`classify.go:234`). `| tail -50`, `| xcbeautify`, `| grep FAIL`
   are equally cosmetic and split groups. Strip trailing pipe segments whose head is in
   {tee, tail, head, grep, rg, wc, cat, sed, awk, cut, sort, uniq, xcbeautify, xcpretty}.
   Stop there — no argument normalization, no similarity.
4. Same identity started in parallel within one fan-out is not a retry. If attempt N
   starts before N−1 ends → kind `parallel`.
5. `fix`/`infra_recovery`/`worker_queue` are ambiguous when two groups are both
   failed-and-awaiting. Assign only when exactly one group is open-after-failure;
   otherwise no kind. Or cut for v1 (see Q6).

Show the identity string in the inspector so "why aren't these grouped" is self-explaining.

## Q4 — Architecture: shape is right; four bites

1. `internal/codex/lane.go:458-464` fully decodes every `custom_tool_call_output` /
   `function_call_output` line, twice (envelope into RawMessage, then payload): 104 MB
   of decode for 1805 lines that contribute only `call_id` + timestamp. `call_id` sits in
   the first ~200 bytes; extract it by prefix scan like `payloadType` does. Also skip
   `event_msg/token_count`. Decoded bytes drop from ~63% to ~27%.
2. `internal/codex/reader.go:95-124` appends a long line into `buf` before the caller can
   skip it: every 5 MB `compacted` line is copied in full, and >64 MB returns
   "line too long", aborting the file. Peek the type in the first `ReadSlice` chunk; if
   skippable, discard to `\n` without appending; on overflow, discard and continue.
3. Sub-agent discovery by rescanning "today's directory" misses midnight rollover and
   sessions started on earlier days. Scan day dirs from the session's start date through
   today; the first-line index already has `parent_thread_id`.
4. Rewrite/truncation: if `size < offset`, or the `ordinal` at `offset` ≤ last seen,
   reparse from 0. `/api/event` should verify the fetched line's ordinal/id.
5. Follow-mode refetch must preserve zoom/brush/selection and only pin to "now" if the
   user was already at the right edge.
6. Memory is fine: ~6k ops × ≤2.5 KB ≈ 15 MB/session; 75 MB for five. No cache file.
   Startup reparse 5–15 s once; print progress.
7. 42% of the sample is between-turn user time. Totals and the helicopter bar need an
   "in-turn only" toggle.

## Q5 — Phases: 14 → 12

- `plan` → marker only (update_plan is a point event).
- `wait_user` + `idle` → `outside_turn` with kind `user` | `parent`.
- Keep `unknown` and `no_telemetry` distinct ("event but unclassifiable" vs "no event").
- Keep think, explore, develop, build, test, release, infra, wait_worker, compaction.
- Legend: outside_turn / no_telemetry / unknown as three greys → 9 real colours.

## Q6 — Cut / keep

CUT for v1:
- `write_stdin` stitching. Old-format op = `[function_call ts, function_call_output ts]`
  paired by call_id; `write_stdin` = its own `wait_worker(poll)` op. Wall-clock stays
  exact. Keep the call/output pairing itself (~30 lines, covers ~25% of files).
- `Lane.stages[]` and think→next-op attribution.
- `fix` / `infra_recovery` / `worker_queue` kinds — most interpretive part of the
  classifier. Ship groups + attempts; eyeball real groups first.
- Heredoc body analysis (`classify.go` "script-write" rule) → `unknown`. A false
  `develop` is worse than an honest unknown; it is the only rule that reads script bodies.
- Expandable lane → phase rows. v1 = one row per lane from `segments`, ops in the side
  list for the brushed range.

KEEP: exclusive partition with priority; explicit-identity groups; sub-agent lanes with
spawn/interacted/completed/interrupted/result markers; `parallel.agent_ms`/`wall_ms`
separate from root totals; `/api/event` by (file, off, len); version poll + full refetch;
gzip; `/api/rules`; session names from `session_index.jsonl`; `ordinal` for rewrite
detection.

## Repo gates

No CLAUDE.md / workflow / test docs exist in todobem — n/a. Add a 10-line CLAUDE.md with
DESIGN.md §5 ("what is not inferred") so later edits don't erode those rules. Tests: only
`classify_test.go` (61 lines). Add table tests for `sum(by_phase) == elapsed` per lane and
raw-vs-exclusive on a fixture cut from the sample before any UI work.

## Next validation step (~30 min)

Run `cmd/dump` on the 151 MB sample under `time` and print: bytes JSON-decoded / total,
`sum(by_phase) − elapsed` per lane (must be 0), raw op-sum vs exclusive, and the top 20
head words among `unknown` ops. If decoded > 35% or the partition doesn't balance, fix
ingestion before UI work.
