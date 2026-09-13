# todobem

**A wall-clock profiler for AI coding-agent sessions.** *Check what your agents did while you were sleeping.*

A local, single-binary viewer for long AI-agent sessions. It tails OpenAI Codex CLI rollout
files (`~/.codex/sessions/**/rollout-*.jsonl`), lays every thread out as a lane on one timeline
(root agent + each sub-agent), groups tool calls into stages, finds retry cycles and long waits,
and shows where the wall-clock time went — live, with a 30 s – 5 min refresh.

```
go build -o todobem ./cmd/todobem
./todobem            # opens http://127.0.0.1:7788
./todobem -addr 127.0.0.1:9000 -codex ~/.codex -open=false
```

Nothing leaves the machine: the server only reads the rollout files and serves JSON to the page.

## What you see

* **Sessions** — every root thread on this machine, newest first (sub-agent threads are attached
  to their parent).
* **Session** — the first user message, a live/closed status, and seven totals: elapsed, tools
  (coding, build, test, release, infra), LLM, known waiting (user / workers), overhead (context
  compaction, background processes), no telemetry, sub-agent time (parallel, reported separately).
* **Timeline** — overview strip with a brush; one row per agent (▶ spawn, ticks per message
  from the parent, bars per completed turn, dashed connector to the parent); markers for user
  messages ▲, questions ?, final answers ✓, compactions ◆; retry-group brackets. Click a lane
  name to expand it into phase rows; at fine zoom every operation is drawn and can be
  inspected (rule that matched, exit code, the raw source event).
* **Time in this window** — exclusive breakdown of the selected range (toggle "inside turns
  only" to exclude waiting for the user), the inside-testing breakdown (first runs, retries
  after failure, reruns, fixes between attempts, worker queue, infra recovery, remote runners),
  and the sub-agent parallel time.
* **Operations in view** — sortable list (longest / latest / failed first) across all lanes.
* **Conversation** — user messages, questions to the user and final answers, verbatim.
* **Agents** — one row per lane with active time, turns, ops, tool work.

## How it decides

Documented in `docs/SCHEMA.md` (normalized event schema) and `docs/DESIGN.md` (research on the
rollout format, classification rules, incremental ingestion). Short version:

* Turn boundaries (`task_started` / `task_complete`) give waiting-for-user time. Inside a turn,
  time not covered by a tool operation is LLM time (the model generating). A turn that never closed becomes
  "no telemetry".
* Commands are classified by a rule table over the command text (head word + subcommand,
  script names, `while … sleep` loops). `/api/rules` and the in-app guide show the live table.
  Unmatched commands are Unknown. Durations are never used to classify.
* Retry groups join operations with an identical normalized command (redirections and
  cosmetic pipes stripped, cwd included). Nothing fuzzy.
* Parallel commands inside one lane are partitioned by phase priority so totals are exclusive
  wall-clock; the raw op sum is shown next to them.

## Development

```
go test ./...
node --test cmd/todobem/app_test.js    # frontend regressions; no npm dependencies
go run ./cmd/dump <root-thread-id>     # prints totals, lanes, groups, longest ops, unknown heads
```

The frontend is `cmd/todobem/web` (no build step; embedded into the binary).

Contributor rules and the repository map are in [`CLAUDE.md`](CLAUDE.md) (`AGENTS.md` is a symlink).

## License

todobem is dual-licensed — see [`LICENSING.md`](LICENSING.md):

* **AGPL-3.0** ([`LICENSE`](LICENSE)) — open source: personal, research and non-commercial use,
  and any project that meets the AGPL's copyleft obligations.
* **Commercial license** — commercial use without those obligations (proprietary products,
  hosted services that do not release source). Contact [info@extractum.io](mailto:info@extractum.io).

Contributions are accepted under the terms in `LICENSING.md`.
