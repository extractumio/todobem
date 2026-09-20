# todobem

**See what your coding agents did. Measure where the hours and the tokens went. Fix the
harness, not the agent.** *Check what your agents did while you were sleeping.*

todobem is a local, single-binary tool that turns the session logs your AI coding agents
already write into evidence about your **agent harness** — the prompts, reply habits,
delegation, skills, rules, models and effort settings that decide how fast and how cheaply a
task gets delivered. It reads OpenAI Codex CLI rollouts (`~/.codex/sessions/**/rollout-*.jsonl`)
and Claude Code session logs (`~/.claude/projects/**/*.jsonl`), replays every thread as a lane
on one timeline, accounts for every millisecond and every token,
and reports the patterns that cost you across sessions — with the evidence one click away.

## Why

An agent session runs for hours, spawns sub-agents, retries tests, waits for you, compacts its
context, and hands you a result and a bill. What you do not get is an explanation: which hour
was the model thinking, which was a test loop, which was the agent waiting for a reply you did
not see, which was three sub-agents running one after another instead of in parallel. The
harness is the part you control, and it is exactly the part nobody measures.

todobem makes that measurable, from logs you already have, without sending anything anywhere.

## Three layers

**1. Inspect — a flight recorder.** One timeline, one lane per agent thread (the main thread
and every sub-agent), phases coloured by what the tool call was, markers for your messages,
questions, final answers and compactions. Zoom to a single operation and open its source
event, the rule that classified it, its exit code. Read the conversation verbatim: what you
asked, what the agent asked back, what it answered. A running session is followed live
(30 s – 5 min refresh, or on demand).

**2. Profile — where the time and the tokens went.** Exclusive wall-clock partitions that add
up to the elapsed time: model generation, development, build, test, release, infrastructure,
waiting for workers, waiting for you, compaction, no telemetry. A second partition by lifecycle
stage — planning, implementation, review, testing, release, operations — over the same
minutes. Retry groups, failed attempts and the fixes between them, background processes,
sub-agent time in parallel, per-thread token usage that survives resumed sessions.

**3. Improve — Insights.** A report for a period (last 7 / 30 / 90 days, all time, custom) over
every session of a project, in seven groups a reader can act on: *You and the agent*,
*Sub-agents*, *Failures and retries*, *Long tool runs*, *Context size*, *Models and effort*,
*Not measured*. Each card is one deterministic detector over the parsed sessions and states
what happened (with its denominator), how it is spread, what to do, and where — sessions, lanes
and intervals that open in the timeline. Order by time or by tokens; the top findings sit on
one strip. The kind of thing it surfaces:

* the agent finished and waited for your answer — for how long, how often, and what a long
  break costs in cache the next turn has to rebuild;
* sub-agents that ran one after another while the main thread idled, and what each spawn costs;
* the commands that fail and get retried, the edits that failed, invalid tool calls;
* the longest tool runs by command shape, and processes left running after their turn;
* how large the context gets, how often it is compacted and what the pauses add up to;
* which model and effort level served which stage, and where the reasoning tokens go;
* what could not be measured — unmatched commands and missing telemetry, named, not hidden.

## The loop

Measure a period → change **one** thing in the harness — reply sooner, delegate in parallel,
add a rule for an in-house script, lower the effort for a stage that does not need it, stop a
retry loop with a fixture → measure the next period. Every finding links to the sessions it
came from, so the change is argued from evidence, and the next report shows whether it worked.

## Unknowns — and the skill that grows the classifier

Every command an agent runs is named by a rule table over its literal text: `go test`,
`git push`, `docker compose up`, a `while … sleep` loop. Your project has commands the table
has never seen — an in-house CI wrapper, a deploy script, a Python tool that captures device
state — and todobem does the honest thing with them: it calls them **unknown**, counts their
time, and shows it in every report and in the *Not measured* group of Insights. Unknown time
is never silently folded into "development" or guessed from how long it took.

You do not have to leave it there. A **user overlay** (`~/.todobem/rules.json`) extends the
table with your commands, your review and planning skill names, your reviewer agent roles and
your design-document paths — merged with the built-ins at startup, shown in the in-app guide,
and part of the cache key, so every session re-parses under the new rules by itself.

Two tools make that a routine instead of a chore:

```
todobem unknown -since 7d                   # unmatched commands across recent sessions, by time
todobem unknown -explain 'scripts/gen-sdk.sh'  # what the rules say about one command (exit 1 = unknown)
todobem unknown -rules draft.json -since 7d # dry-run a draft overlay before installing it
```

and the **`resolve-unknown` skill** (`.claude/skills/resolve-unknown/`, also under
`.codex/skills/` for Codex): an agent workflow that takes the unknown table top-down by time,
establishes what each command does from evidence — the script itself, its `--help`, what it
spawns — never from its name, writes the narrowest rule that covers it, dry-runs it against the
real commands, installs it and reports what was reclaimed and what stays unknown and why. Point
your agent at it after the first few sessions of a new project; each round leaves the next
report more complete, and the rules stay yours, in one inspectable JSON file.

## Why the numbers can be trusted

* **Nothing is inferred from a duration.** A long operation is listed, never explained.
* **Classification is a rule table over the literal command text**, deterministic and
  inspectable (`/api/rules`, the in-app guide). What matches no rule is *unknown* — an honest
  number, not a guess — and one you can drive down with your own rules (above).
* **Every total is an exclusive partition** of the wall clock: the parts add up to the elapsed
  time, sub-agent time is reported in parallel, never added. Raw sums are shown beside them.
* **No "goal achieved" score.** User messages and final answers are shown verbatim instead.
* **Every insight is a named detector with evidence.** A card without a session, lane or
  interval to open is not rendered. Exposure, distribution and advice — no savings estimates.
* **Nothing leaves the machine.** Loopback bind, a token-locked UI, read-only access to the
  rollout tree, no telemetry, no outbound calls — except the fleet you set up yourself: an
  agent answers the hub it was paired with, a hub dials only its paired agents (see *Fleet*).

## Who it is for

* **The developer who runs agents overnight** and wants to know, next morning, what the ten
  hours were — and what to say differently tomorrow.
* **The lead standardising a harness for a team**: which skills, rules, effort levels and
  delegation patterns deliver, measured across a project's sessions — on one machine, or on
  a collected rollout tree pointed at with `-codex` / `-claude`.
* **The platform engineer** maintaining agent tooling: retry loops, failing wrappers,
  unmatched commands and telemetry gaps, with the rule table and overlay to fix them.

The model is source-agnostic (`internal/model`); two adapters ship — Codex CLI (0.134 → 0.154
verified) and Claude Code (2.1.226 → 2.1.270 verified) — and both kinds of session share one
list, one timeline and one Insights report, each row marked with its source (purple Codex,
brown Claude Code) and filtered by the source toggles. Stack: Go 1.22, standard library only;
vanilla JS + SVG embedded in the binary — no build step, no npm.

## Quick start

```
go build -o todobem ./cmd/todobem
./todobem            # opens http://127.0.0.1:7788, logged in
./todobem -addr 127.0.0.1:9000 -open=false
./todobem -codex /srv/rollouts/codex    # exactly this Codex folder, pinned for this run (Claude Code off)
./todobem -codex ~/.codex -claude ~/.claude   # both, pinned
```

## Deploy on any host (one-liner)

From a fresh clone, one command builds the binary and runs it in the background (needs Go 1.22+):

```
git clone <repo-url> todobem && cd todobem && ./scripts/deploy.sh
```

`./scripts/deploy.sh` (also `make deploy`) builds, restarts cleanly, and prints the URL;
`./scripts/deploy.sh stop` / `status` manage it, `run` runs in the foreground (for systemd).
It keeps the **loopback** bind — the viewer is never exposed on the network. To reach a remote
host, tunnel it and open the URL locally:

```
ssh -L 7788:127.0.0.1:7788 <host>     # then open http://127.0.0.1:7788/
```

Override with env: `ADDR=127.0.0.1:9000 RULES=~/rules.json ./scripts/deploy.sh`
(`CODEX=/path` / `CLAUDE=/path` pin the folders for the run, see Settings).

## Settings: which folders hold the sessions

By default todobem reads `~/.codex` — the Codex CLI's home, where `sessions/`,
`archived_sessions/` and `session_index.jsonl` live — and `~/.claude`, the Claude Code home,
where `projects/<project>/<session>.jsonl` and each session's `subagents/` live. **Settings** in
the sidebar has one section per source and lets you point each at several folders at once: your
own home, a tree copied from a laptop, a collected tree on a server. Every folder's sessions
appear in one list; a session present in two folders is shown once, from the first folder listed
(the session page names the file it was read from). The lists are saved to
`~/.todobem/settings.json`

```
{"codex_homes": ["~/.codex", "/Volumes/work/codex-from-laptop"], "claude_homes": ["~/.claude"]}
```

and applied live — no restart. Without the file the defaults are `~/.codex` and `~/.claude`; a
key that is absent means that source's default, an empty list turns the source off (at least
one folder overall is needed). Each Codex entry must be a Codex home, the folder that *contains*
`sessions/`; each Claude Code entry a Claude Code home, the folder that contains `projects/`; a
folder that does not exist yet (an unmounted drive) is accepted and its sessions appear when it
is back; only the session logs under those folders are read, and nothing is ever written there.
A folder on a slow or removable volume slows every rescan (the list is rescanned every 20 s).

`-codex <dir>` and `-claude <dir>` on the command line (or `CODEX=` / `CLAUDE=` for
`deploy.sh`) pin this run to exactly the folders given — a source not named is off — and make
the page read-only; `-settings <path>` (`$TODOBEM_SETTINGS`) picks another settings file. `todobem cache`, `todobem unknown` and `cmd/dump` read the same file, so they see the same
sessions as the UI. The server prints the folders in effect and where they came from at start,
and logs every change made from the page. With `-auth=off`, anyone who can reach the port can
change these folders too — one more reason the gate is on by default.

## Access: the UI is locked

Sessions contain whatever your agents read and wrote, so the viewer never stays open. Every
API route needs a browser session, and a session is opened with a **one-time token** minted in a
terminal on the machine that runs todobem:

```
todobem token                 # prints a link (one use, valid 5 min) and the bare token
todobem token -ttl 12h        # the browser stays logged in for 12 h (default 30 days)
todobem token -revoke         # rotate the key: every session and token stops working now
todobem unknown -since 7d     # unmatched commands and telemetry gaps across recent sessions
todobem unknown -explain CMD  # what the rules say about one command (exit 1 = still unknown)
todobem cache -prune          # drop cache entries of sessions the configured folders do not list
```

Open the link (or paste the token into the lock screen); the browser then stays logged in for
the token's lifespan without asking again. **Lock** in the header ends it early. `deploy.sh
start` prints a login link to your terminal (never into the log), and `todobem` started by hand
opens the browser already logged in (`-open`).

How it works, briefly: `~/.todobem/auth.key` (32 random bytes, mode 0600, created on first run)
is the root of trust — being able to read it is what authorises minting, so no server contact is
needed. Tokens and sessions are HMAC-signed with it; the session lives in an HttpOnly,
SameSite=Strict cookie scoped to `/api` (the UI renders agent text, so a script injection could
read localStorage but not this cookie). Rotating the key invalidates everything at once, even on a
running server. `-auth=off` (or `AUTH=off` for `deploy.sh`) leaves the UI open; the server says
so on stdout. Cookies are not port-scoped, so run a tunnelled and a local todobem on different
ports.

## What you see

* **Sessions** — every main thread of the configured folders, newest first, described by the
  last answer its agent gave (sub-agent threads are attached to their parent); a period
  (last 24 hours … all time, custom dates) and project filter — the same widget as the Insights
  report — plus search and sort; a pulsing ? chip marks a session whose agent asked you a
  question (or submitted a plan for approval) and is still waiting, with how long it has waited.
  The session page names the
  rollout file each thread was read from.
* **Session** — the first user message and the last answer, verbatim; a live/closed status; the
  whole-session totals: working time, tokens, LLM, tools (development, build, test, release, infra),
  known waiting (user / workers), context compaction, no telemetry, sub-agent time (parallel,
  reported separately), plus code review and planning time when the harness recorded them.
* **Timeline** — overview strip with a brush; one row per agent (▶ spawn, ticks per message
  from the parent, bars per completed turn, dashed connector to the parent); markers for user
  messages (a white person glyph), questions ?, final answers ✓, compactions ◆; retry-group brackets. Only lanes
  alive inside the visible window are drawn; each sub-agent row shows its path, nickname and
  model. The chevron expands a lane into phase rows (at fine zoom every operation is drawn and
  can be inspected: rule that matched, exit code, the raw source event); the name opens the
  agent card — model, timing, the prompt it was spawned with and its final answer, verbatim.
  Codex CLI 0.144+ stores inter-agent messages encrypted in the rollout, so for those sessions
  the card says so instead of showing a prompt.
* **Time in this window** — exclusive breakdown of the selected range (toggle "inside turns
  only" to exclude waiting for the user), the inside-testing breakdown (first runs, retries
  after failure, reruns, fixes between attempts, worker queue, infra recovery, remote runners),
  and the sub-agent parallel time.
* **Operations in view** — sortable list (longest / latest / failed first) across all lanes.
  **Failures only** lists failed steps; a read, search, listing or probe that answered with a
  non-zero exit is a *query miss* — recorded verbatim, shown in the inspector, not counted.
* **Conversation** — user messages, questions to the user and final answers, verbatim.
* **Agents** — one row per lane with active time, turns, ops, tool work.
* **Insights** — a report for a period (default: the last 30 days) over the sessions of one
  project: where the time and the tokens went, in eight groups (you and the agent, sub-agents,
  failures and retries, tool calls, long tool runs, context size, models and effort, not measured). Every card
  comes from one deterministic rule over the parsed sessions, shows what the pattern cost, how it
  is spread, what to do, and links to the evidence in the timeline. No estimates of savings, no
  cause guessed from a duration; sessions that cannot carry a signal are listed as "no data".
  Change the period (7 / 30 / 90 days, all time, custom dates, one session), pick the project,
  order by time or by tokens, and **Analyze** the sessions that are not parsed yet. Plain English
  throughout. Every report has an address you can copy: `#session/<id>/insights` is one
  session's report, `#insights?f=…` the filters in force (the address bar follows them; the
  session list has the same, `#sessions?f=…`, search and sort included).
  Design and rules: `docs/ARCHITECTURE.md` §10.

## How it decides

Documented in `docs/ARCHITECTURE.md` (the schema, the log formats, classification, derivation,
the stage detection mechanism). Short version:

* Turn boundaries (`task_started` / `task_complete`) give waiting-for-user time. Inside a turn,
  time not covered by a tool operation is LLM time (the model generating). A turn that never closed becomes
  "no telemetry".
* Commands are classified by a rule table over the command text (head word + subcommand,
  script names, `while … sleep` loops). `/api/rules` and the in-app guide show the live table.
  Unmatched commands are Unknown. Durations are never used to classify. Add project-specific
  commands, review/plan skill names, reviewer agent roles and design-document paths in a user
  rules file (`-rules <path>`, `$TODOBEM_RULES`, or `~/.todobem/rules.json`) — merged into the
  built-in tables, shown in the guide. `todobem unknown` lists what is still unmatched across
  recent sessions; the `resolve-unknown` skill (`.claude/skills/`, also under `.codex/skills/`)
  is the agent workflow that turns those into rules from evidence, never from guesses.
* Each session is described by the **last answer** its agent gave (first paragraph, verbatim) —
  in the session list. Codex's TUI recaps are generated on the fly and never recorded, so
  todobem shows what is on record instead of inventing a summary.
* Every millisecond also gets a **lifecycle stage** — Planning, Requirements, Design,
  Implementation, Code review, Testing, Release, Operations — a second partition of the same time
  (same total as the activity split, shown first in the breakdown, drawn as a thin strip under
  each lane). Stages come from harness signals only: Codex plan mode, a plan written with
  `update_plan`, a review skill the harness actually injected (e.g. `code-review-cc`, `simplify` —
  not the words in a prompt), Codex review mode, a sub-agent's spawn role (your overlay), PR/MR
  review verbs, and log/service commands after the lane's first release. Requirements and design
  have no built-in detector; model output with no tool call after it stays "Model output".
* Retry groups join operations with an identical normalized command (redirections and
  cosmetic pipes stripped, cwd included). Nothing fuzzy.
* Parallel commands inside one lane are partitioned by phase priority so totals are exclusive
  wall-clock; the raw op sum is shown next to them.
* Click a **Waiting for user** span to see what the agent last said before it stopped (the
  preceding turn's final answer) and the user message that resumed it.
* Parsed sessions are **cached to disk** (`internal/store`): opening an unchanged session serves
  the cache without re-parsing (a 354 MB session: ~3.7 s cold → ~0.16 s cached). The cache key
  includes the classifier, so a rule change or a grown file re-parses automatically — it never
  shows a stale number. **Refresh** forces a full re-parse. Disable with `-cache=off`. The server
  prints the cache size at start; `todobem cache` reports it and `todobem cache -prune` drops the
  entries of sessions the configured folders do not list (another home, deleted rollouts).

## Fleet: agents on your servers, one dashboard

Run todobem headless on the machines where the agents work and read them all from one place.
The **agent** is the same binary with `-agent`: it indexes and parses that host's sessions
exactly as the viewer does and answers a **hub** — an ordinary todobem — over TLS (a
self-signed certificate pinned when you pair) with a bearer on every request. It never
connects anywhere; the hub dials only the agents in `~/.todobem/agents.json`. Design and the
wire protocol: `docs/AGENT-MODE.md`.

On each server (needs nothing but the binary):

```
todobem -agent                        # TLS on :7789, every interface; no UI; prints a pairing string
AGENT=1 ./scripts/deploy.sh           # the same in the background, from a clone
todobem agent pair                    # another pairing string later (one use, 5 min, against a running agent)
```

On the hub:

```
todobem hub add 'todobem-agent://…'   # pair (-name web-01 -addr 10.0.0.5:7789 before the string override)
todobem hub list                      # every agent, reached or not, its build and session count
todobem hub doctor web-01             # the agent's health report (-all: every agent, in parallel)
todobem hub rotate web-01             # a new bearer; the old key retires on the new one's first use (-all)
todobem hub remove -revoke web-01     # forget it (-revoke: nobody holds a bearer any more)
```

A hundred unattended agents want a supervisor, not a `nohup`; a unit to paste
(`/etc/systemd/system/todobem-agent.service`, then `systemctl enable --now todobem-agent`):

```
[Unit]
Description=todobem agent
After=network-online.target

[Service]
User=ci
ExecStart=/usr/local/bin/todobem -agent
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
```

A running hub picks the file up by itself; the Settings page has the same in a **Paired
servers** section. Remote sessions join the list with a host chip and the id `<uuid>@<host>`,
a **Host** filter sits next to Project (the same path on several hosts is one project entry,
"· 3 hosts"), Insights take the host too, and a session opens through the hub — model, op
details, source lines — as if it were local. The hub polls each agent every minute while you
look and every ten minutes otherwise, asks only for what changed, fetches a model when you open
its session, and keeps what it fetched so an unreachable agent still shows its last-known
sessions. Models are comparable only between equal builds (the row says so otherwise); the list
always works.

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
