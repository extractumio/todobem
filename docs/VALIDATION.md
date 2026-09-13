# Validation against 30 recent sessions (2026-09-12)

Method: todobem's output for the 30 most recent root sessions (0–798 MB, 1–46 lanes) was
exported; seven independent Sonnet agents recomputed ground truth from the raw rollout files
following `VALIDATION-PROTOCOL.md` (turns, user messages, sub-agent inventory, tokens, commands,
compactions, background processes, classification of the 10 longest commands) and reported every
discrepancy. In parallel a script (`xcheck.py`) recomputed user messages, turns, compactions and
tokens for all 30 sessions.

Classification of the longest commands: no disagreements in any session. Suggestions concerned
the small `unknown` tail only.

Discrepancies found and fixed:
1. **Phantom ops.** When `exec` returned after `yield_time_ms` with commands still running, a
   synthetic op was created from the JS input and the real `CommandExecution` item arrived later →
   duplicated commands (+13% op count in one session) and phantom retry groups. Synthetic ops are
   now provisional and are replaced when the real item reports the same command.
2. **User messages +1** when a message carried image attachments: the raw `message` line (text +
   `<image …>` placeholder) and the `UserMessage` item were both kept. The item now supersedes the
   fallback marker regardless of text.
3. **Replayed turns in forked sub-agent files.** A child file echoes the parent's history
   (`task_started`/`task_complete` with the parent's turn ids, same timestamp, no items). These
   were counted as extra/orphaned turns. Turns with no tool activity that are superseded within
   5 s, and zero-length empty completed turns, are dropped; markers move to the real turn.
   Orphan `task_complete` lines no longer synthesize turns.
4. **`background_ops` totals** counted the root lane only; now all lanes.
5. **Compaction time** in the exclusive partition is lower than the summed durations when a
   compaction overlaps a long test (the harness compacts while a remote test runs). The card
   now shows the summed durations and the exclusive share separately.
6. Classifier gaps: `git -c k=v …` options before the subcommand, `bash -o pipefail -c '…'`,
   `env -u VAR …`, `make <target>` (run/serve → service, test → test), `go run … -serve` and other
   `serve` flags → service, `./run.sh --build-only|--rebuild` flag rules, `git merge-base` /
   `check-ignore` (git fallback → code), `/usr/bin/log`, `sample` → infra diagnostics,
   `bash -n` → syntax check, `ffmpeg`/`sips`/`qlmanage`/`pdftotext` → infra media,
   `*install*`/`*setup*` scripts → infra, scripts inside `deploy*/` directories → release,
   `go run <pkg>` judged by the package name (or `-serve` → service), argv lists bound to a
   variable before `subprocess.run(cmd)` inside heredocs, JSON-quoted `"cmd":` keys in `exec` inputs.

Definitional notes surfaced by the check (documented, not changed):
* Session start is the thread creation time (`session_meta.payload.timestamp`); the time until the
  first message is "waiting for user" and is shown as *Created / First message* in the header.
* `totals.turns` and `failed_ops` span all lanes and all op kinds (not only commands).
* Retry groups cover test/build/release commands only; repeated `git status` is not a retry.
* Lease-wait loops followed by a test in one command are `test` with the `queued` flag.

After the fixes: user messages, turns, tokens and compaction counts/durations match the raw files
on all 30 sessions; `sum(by_phase) == elapsed` holds on every lane; 151 of 62,815 operations
(516 s in total) remain `unknown` — mostly ad-hoc python/node snippets with no literal signal.
