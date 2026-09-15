---
name: resolve-unknown
description: Find the commands todobem could not classify (phase "unknown") and the telemetry gaps across recent Codex and Claude Code sessions (the SOURCE column says which), work out what each command does from evidence, and add user rules to ~/.todobem/rules.json so the next report names them. Use when a session report shows "unknown" time, when the Insights card "Commands no rule matched" lists heads, when cmd/dump prints new unknown heads, or when asked to improve classification for a project.
---

# resolve-unknown

todobem classifies every tool call by a **literal rule table** over the command text (head
word + sub-words, script names, heredoc signals; `docs/ARCHITECTURE.md` §4). What matches no
rule is `unknown` — honest, but useless in a report. This skill turns unknowns into rules in
the **user overlay** (`~/.todobem/rules.json`), never into guesses. Product rule 3 applies
throughout: a wrong phase is worse than an honest unknown.

Where unknowns show up: the session breakdown's **Unknown** row with its sub-rows
(*Unclassified scripts* — an opaque `python3 -` / `node` script, *Unmapped tools* — a Claude
Code tool without a mapping, *Unmatched commands* — no rule at all), the Insights card
**Commands no rule matched** (D13, with the same split), and `todobem unknown`.

## Mechanics

```
todobem unknown [-last 20 | -since 7d | -session <id-prefix>] [-ops] [-json]
todobem unknown -explain '<command>'          # what the current rules (built-in + overlay) say; exit 1 when unknown
todobem unknown -rules /path/draft.json …     # dry-run a draft overlay on top of the installed one
```

- `todobem unknown` scans the newest root sessions of every configured source (cache-backed,
  seconds; a session without a cache entry is parsed) and prints one row per unknown **head**:
  the command name, SOURCE (`codex`, `claude`), op count, time, sessions touched, the longest
  op's command. A Claude Code tool without a mapping is listed under its **tool name**
  (`Artifact`, `ReportFindings`). `-ops` lists every unknown op with status / exit and the full
  command (400 chars); `-json` gives the same for tools.
- `-explain` prints the head, the **word key** a word rule must use (the head's basename,
  optionally + sub-words), the phase, kind, rule, lifecycle and whether the command carries a
  retry identity; it exits 1 while the command is still unknown.
- The **no telemetry** section lists turns that never closed (a crash, a killed CLI, a resumed
  thread; a sub-agent whose file stopped mid-turn is orphaned at its last evidence once the root
  closed). No rule can fix those — report them as gaps, do not invent a phase for them.
- The overlay is merged at startup from `-rules`, `$TODOBEM_RULES` and `~/.todobem/rules.json`
  (`todobem unknown` prints which file it loaded). **Restart todobem after editing it**
  (`./scripts/deploy.sh`); every cached session re-derives automatically because the rules hash
  is part of the cache key, and the Insights facts follow.

Rule forms (`docs/ARCHITECTURE.md` §4.5):

| match | matches | example |
|---|---|---|
| `word` / `word sub …` | the head's basename (+ following words), matched **before** any regex, so it overrides a built-in with the same key | `"xcodebuild-app.sh build-for-testing"`, `"deploy.sh"` |
| `seg:<re>` | one top-level shell segment (after `&&`, `;`, `\|`), env prefixes stripped | `"seg:capture-database\\.py"` |
| `head:<re>` | the head's basename | `"head:^run-.*-tests\\.sh$"` |
| `headpath:<re>` | the head with its path | `"headpath:^tools/ci/"` |
| `re:<re>` | the whole command, heredoc bodies masked | `"re:(?s)\\bwhile\\b.*\\bsleep\\b"` |

Phases: `code build test release infra wait_worker`. Optional `kind` (a short label, shown in
the inspector and the breakdown; a code-phase kind lands in a sub-row by name — `read`,
`search`, `edit`, `format`, `git …`, `http`, `mcp`, anything else under *Shell, data &
inspection*) and `lifecycle` (`plan requirements design implement review test release
operate`) pin the SDLC stage. A regex rule can only **raise** the phase (the highest priority
among all matching rules wins: release > test > build > wait_worker > infra > code); to
**lower** one — a script named `deploy.sh` that only restarts a local server — use a word rule,
which is consulted first. A malformed file (bad phase or lifecycle, empty match, an
uncompilable regex, an unknown key) fails loudly at startup and changes nothing.

What a rule **cannot** do: classify a Claude Code tool by its name (the table holds command
text only; an unmapped tool stays `unknown/tool:<name>` until `internal/claude/tools.go` maps
it — that is a code change with a fixture), or fix a telemetry gap.

## Methodology

1. **Measure**: `todobem unknown -since 7d` (or `-last 30`). Work the table top-down by TIME —
   a head with 17 min beats one with 24 ops of 0 s. Skip one-off shell noise (`const`, `for`,
   `-c`, `#`, a bare `$var`: a variable assignment, a comment or a loop header as the first
   word); it will not recur and no rule should match it.
2. **Interpreters first.** `python3`, `python`, `node`, `ruby`, `perl` are almost always the top
   heads, and the head says nothing: match the **script** with `seg:` (`seg:capture-database\.py`
   matches `python3 artifacts/x/capture-database.py …` wherever the file lives). `python3 -m
   unittest` / `pytest` are already `test`; `python3 - <<PY` heredocs are classified by their
   body and stay unknown only when the body carries no literal signal — those are honest.
3. **Establish what the script does from evidence, not from its name**: read it (`cat`, `head`)
   in the project it ran in (`-ops` shows the session; the session's cwd is in the UI heading),
   read its `--help`, look at what it writes or spawns. A `.sh` that runs `xcodebuild … test` is
   `test`; one that runs `xcodebuild … build` is `build`; a Python tool that captures device
   state for a check is `test`; a migration generator is `code`; a `docker` / `ssh` wrapper is
   `infra`; a poll loop is `wait_worker`; a script that builds and restarts a local server is
   `infra` with kind `service`, not `release`. If the same script does different things by
   sub-command, write one rule per sub-command (`"script.sh build"`, `"script.sh test"`) or leave
   it unknown and say so.
4. **Write the narrowest rule that covers the evidence**: a word key for a named script, `seg:`
   when the script is called through an interpreter, `headpath:` when only the directory
   identifies it. Never classify by duration, by who ran it, or by "it is probably".
5. **Dry-run**: put the rules in a draft file, then `todobem unknown -rules draft.json -explain
   '<the real command>'` for each sample (exit 0, the expected phase and rule) and `todobem
   unknown -rules draft.json -since 7d` for the table. The head must disappear and nothing else
   may move.
6. **Install**: merge into `~/.todobem/rules.json` — one JSON object, the `rules` list appended,
   `python3 -m json.tool ~/.todobem/rules.json` to validate — restart todobem, re-check the
   report. Give each rule a short `note` ("wraps xcodebuild test", "device capture for smoke
   checks"): the note is the evidence trail. Keep the file's permissions private (it names your
   projects' scripts).
7. **Report**: which heads were resolved (time reclaimed), which were left unknown and why, the
   no-telemetry gaps as gaps. Unknown time that remains is stated, not hidden.

## Example (synthetic names)

```
$ todobem unknown -last 8
sessions: 8 · unknown ops: 61 · 20m 23s of 17h 32m tool time (1.9%) · 10 heads

HEAD                                 SOURCE    OPS     TIME  SESS  SAMPLE (longest op)
python3                              codex,c…   15  17m 03s     5  python3 artifacts/tools/capture-database.py before-complete
apps/demo/scripts/xcodebuild…        codex      24   3m 18s     1  APP_XCB_SCHEME=Demo-iOS … xcodebuild-app.sh build-for-testing -only-testing …
backend/scripts/new-migration…   codex       2       0s     1  backend/scripts/new-migration.sh app_device_identity
Artifact                             claude      3   1m 10s     2  {"file_path":"…/report.html","title":"…"}

no telemetry: 2 intervals · 4m 10s — turns without a close event …
```

`Artifact` is a Claude Code tool name: no rule can cover it; it stays an honest unknown (or
becomes a mapping in `tools.go`). Evidence for the rest: `capture-database.py` dumps the app's
database from a device before / after a step so a check can diff it (a test fixture step);
`xcodebuild-app.sh` wraps `xcodebuild` and takes the action as its first argument — `-explain`
shows `build` and `test` are already caught by the built-in dispatcher-verb rule, only
`build-for-testing` is not; `new-migration.sh` scaffolds a migration file. Draft:

```json
{ "rules": [
  {"match": "seg:capture-database\\.py",           "phase": "test",  "kind": "device capture", "lifecycle": "test", "note": "dumps device DB for a diff check"},
  {"match": "xcodebuild-app.sh build-for-testing", "phase": "build", "kind": "xcodebuild",     "note": "wraps xcodebuild build-for-testing"},
  {"match": "new-migration.sh",                    "phase": "code",  "kind": "migration",      "note": "scaffolds a migration file"}
] }
```

```
$ todobem unknown -rules draft.json -explain 'python3 artifacts/tools/capture-database.py before-complete'
head:      python3
word key:  python3   (a word rule matches the head's basename, optionally + sub-words)
phase:     test
kind:      device capture
rule:      seg:capture-database\.py
lifecycle: test
$ todobem unknown -rules draft.json -last 8      # 20m 23s → 2m 51s unknown; what is left is other python3 tools, next round
```

Then merge into `~/.todobem/rules.json`, restart, and confirm in the UI: the operation's
inspector names the rule that matched, the Unknown sub-rows shrink, and the Insights card
"Commands no rule matched" loses the head after Analyze.
