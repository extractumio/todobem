---
name: resolve-unknown
description: Find the commands todobem could not classify (phase "unknown") and the telemetry gaps across recent Codex and Claude Code sessions (the SOURCE column says which), work out what each command does, and add user rules to ~/.todobem/rules.json so the next report names them. Use when a session report shows "unknown" time, when cmd/dump prints new unknown heads, or when asked to improve classification for a project.
---

# resolve-unknown

todobem classifies every tool call by a **literal rule table** over the command text
(head word + sub-words, script names, heredoc signals). What matches no rule is `unknown` —
honest, but useless in a report. This skill turns unknowns into rules in the **user overlay**
(`~/.todobem/rules.json`), never into guesses. Product rule 3 applies throughout: a wrong
phase is worse than an honest unknown.

## Mechanics

```
todobem unknown [-last 20 | -since 7d | -session <id-prefix>] [-ops] [-json]
todobem unknown -explain '<command>'          # what the current rules (built-in + overlay) say
todobem unknown -rules /path/draft.json …     # dry-run a draft overlay before installing it
```

- `todobem unknown` scans the newest sessions (cache-backed, seconds) and prints the unknown
  **heads**: command name, op count, time, sessions touched, the longest op's command. `-ops`
  lists every unknown op with status/exit and the full command (400 chars); `-json` for tools.
- The **no telemetry** section lists turns that never closed (crash, killed CLI, resumed thread).
  No rule can fix those — report them as gaps, do not invent a phase for them.
- `-explain` classifies one command and exits 1 when it is still unknown. It prints the
  **word key** (the head's basename) a word rule must use.
- The overlay is merged at startup from `-rules`, `$TODOBEM_RULES` and `~/.todobem/rules.json`.
  **Restart todobem after editing it** (`make deploy` / `./scripts/deploy.sh`); cached sessions
  re-parse automatically because the rules hash is part of the cache key.

Rule forms (see `docs/SCHEMA.md` → User rules):

| match | matches | example |
|---|---|---|
| `word` / `word sub …` | the head's basename (+ following words) | `"xcodebuild-app.sh build-for-testing"` |
| `seg:<re>` | a top-level shell segment (after `&&`, `;`, `|`) | `"seg:capture-database\\.py"` |
| `head:<re>` | the head's basename | `"head:^run-.*-tests\\.sh$"` |
| `headpath:<re>` | the head with its path | `"headpath:^tools/ci/"` |
| `re:<re>` | the whole command | `"re:(?s)\\bwhile\\b.*\\bsleep\\b"` |

Phases: `code build test release infra wait_worker`. Optional `kind` (free label, shown in the
UI and used by `by_kind`) and `lifecycle` (`plan requirements design implement review test
release operate`) pin the SDLC stage. A word rule with an existing key overrides the built-in;
a regex rule can only raise the phase. A malformed file fails loudly at startup.

## Methodology

1. **Measure**: `todobem unknown -since 7d` (or `-last 30`). Work the table top-down by TIME —
   a head with 17 min beats one with 24 ops of 0 s. Ignore heads that are one-off shell noise
   (`const`, a bare `$var`): they will not recur.
2. **Establish what the command does from evidence, not from its name**: read the script
   (`cat`, `head`) in the project it ran in (`-ops` shows the session; the session's cwd is in
   the UI heading), read its `--help`, look at what it writes or spawns. A `.sh` that runs
   `xcodebuild … test` is `test`; one that runs `xcodebuild … build` is `build`; a Python tool
   that captures device state for a check is `test`; a migration generator is `code`; a
   `docker`/`ssh` wrapper is `infra`; a poll loop is `wait_worker`. If the evidence is
   ambiguous (the same script does different things by sub-command), write one rule per
   sub-command (`"script.sh build"`, `"script.sh test"`) or leave it unknown and say so.
3. **Write the narrowest rule that covers the evidence**: a word key for a named script,
   `seg:` when the script is called through an interpreter (`python3 path/tool.py` → the head
   is `python3`, so match the script), `headpath:` when only the directory identifies it.
   Never classify by duration, by who ran it, or by "it is probably".
4. **Dry-run**: put the rules in a draft file, then `todobem unknown -rules draft.json -explain
   '<the real command>'` for each sample and `todobem unknown -rules draft.json -since 7d` for
   the table. The head must disappear and nothing else may move.
5. **Install**: merge into `~/.todobem/rules.json` (keep it one JSON object; `python3 -m
   json.tool` to validate), restart todobem, re-check the report. Note each rule with a short
   `note` field ("wraps xcodebuild test", "device capture for smoke checks") — the reasoning
   is the evidence trail.
6. **Report**: which heads were resolved (time reclaimed), which were left unknown and why,
   the no-telemetry gaps as gaps. Unknown time that remains is stated, not hidden.

## Example

```
$ todobem unknown -last 8
sessions: 8 · unknown ops: 61 · 20m 23s of 17h 32m tool time (1.9%) · 10 heads
HEAD                                 SOURCE    OPS     TIME  SESS  SAMPLE (longest op)
python3                              codex,c…   15  17m 03s     5  python3 artifacts/ipad7-profile-reminder/capture-database.py before-complete
apps/demo/scripts/xcodebuild…   codex      24   3m 18s     1  APP_XCB_SCHEME=Demo-iOS … xcodebuild-app.sh build-for-testing -only-testing …
backend/cnc/scripts/new-migration…   codex       2       0s     1  backend/cnc/scripts/new-migration.sh app_device_identity
Artifact                             claude      3   1m 10s     2  {"file_path":"…/report.html","title":"…"}
```

A head like `Artifact` or `ReportFindings` is a Claude Code tool name, not a shell command: the
rule table cannot classify it (it holds no command text); leave it, it is an honest unknown.

Evidence: `capture-database.py` dumps the app's database from a device before/after a step so
a check can diff it (a test fixture step); `xcodebuild-app.sh` wraps `xcodebuild` and takes
the action as its first argument — `-explain` shows `build` and `test` are already caught by
the built-in `subcommand:` rule, only `build-for-testing` is not; `new-migration.sh`
scaffolds a migration file. Draft:

```json
{ "rules": [
  {"match": "seg:capture-database\\.py",           "phase": "test",  "kind": "device capture", "lifecycle": "test", "note": "dumps device DB for a diff check"},
  {"match": "xcodebuild-app.sh build-for-testing", "phase": "build", "kind": "xcodebuild",     "note": "wraps xcodebuild build-for-testing"},
  {"match": "new-migration.sh",                    "phase": "code",  "kind": "migration",      "note": "scaffolds a migration file"}
] }
```

```
$ todobem unknown -rules draft.json -explain 'python3 artifacts/x/capture-database.py before-complete'
head: python3 · phase: test · kind: device capture · rule: seg:capture-database\.py
$ todobem unknown -rules draft.json -last 8      # 20m 23s → 2m 51s unknown; what is left is other python3 tools, next round
```

Then merge into `~/.todobem/rules.json`, restart, confirm in the UI (the operation's inspector
shows the rule that matched).
