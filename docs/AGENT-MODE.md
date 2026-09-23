# Agent mode — design (v1)

Status: implemented 2026-09-19 (steps 1–4 of §11; `internal/fleet`, `server/agent.go`,
`server/remote.go`, `cmd/todobem/agent.go`), verified against one Linux agent; the map is
`docs/ARCHITECTURE.md` §7.4. Written first as a draft the same day and revised after a
`pragmatic` review (§12 records its verdicts). Written against `main` at `2f72db5` plus the uncommitted working
tree (`store.cacheVersion` 10, `insights.FactsVersion` 16). Inputs: the code in `cmd/todobem`,
`internal/server`, `internal/auth`, `internal/source`, `internal/store`, `internal/settings`,
`internal/insights`; sizes measured on the author's cache (482 models: 61 KB gzipped at the
median, 393 KB at p90, 3.2 MB max; 256 facts sidecars: 4 KB median; a real `/api/sessions`
list: 372 B per row gzipped). Where the implementation differs from the text below, the
difference is small and named in §12.

**Decided by the owner, 2026-09-19:** transport is TCP with TLS and a bearer (decision 3; the
ssh alternative is rejected — there will be no ssh connection to every machine); `upgrade` over
the wire is postponed to a feature of its own (appendix A keeps the sketch); product rule 8 is
amended as §9 says, with an explicit allowance for testing the core functionality.

---

## 1. What it is, in one paragraph

`todobem -agent` runs the same binary headless on a server: it indexes and parses that host's
Codex and Claude Code sessions exactly as the viewer does, keeps the derived models and facts in
its cache, and answers a **hub** — an ordinary `todobem` with a UI — over a small versioned API.
The hub pulls the session list of every paired agent, fetches a model when someone opens the
session, fetches facts for Insights, and shows the fleet in the one dashboard it has today, with
a **Host** dimension next to Project. The agent never connects anywhere; the hub calls only the
agents its owner paired. The two control actions — `doctor`, `rotate` — are fixed Go routines,
neither a shell, neither taking a wire argument that reaches a process.

## 2. Why not what exists

Today's answer to "sessions on other machines" is the Settings page: copy the trees over
(`rsync`) and list the copies as extra homes. That is right for two laptops and wrong for a
fleet: it moves the raw logs (the private conversations themselves, gigabytes), parses
everything on the hub, sees nothing live, and has no way to ask a host anything. Agent mode
moves derived models (60 KB, not 60 MB), parses where the CPU is, follows live sessions, and
gives the hub a channel to the host. The rsync path stays; nothing about it changes.

What the *daemon* buys over a stateless `ssh host todobem export`: the in-memory incremental
parsers (`source.Session`) that make following a live 300 MB session cost bytes, not a
re-parse per poll, and a background digest that makes facts and first opens cheap. Everything
else in this document is the cost of reaching that daemon.

## 3. Decisions (each with the alternative it beat)

1. **The hub pulls; the agent only answers.** The agent opens no connection, ever. The hub is
   configured with the list of agents, which is what "add a server to the hub" naturally means,
   and it keeps a snapshot of each agent so an unreachable host still shows its last-known rows.
   *Rejected: agents push to the hub.* It needs a hub address stable and reachable from every
   server (a laptop hub is neither), an inbound port on the hub for a hundred clients, and turns
   "add a server" into "configure every server with the hub's address".
2. **One binary, one code path.** Agent mode is `server.NewWithCache` (index, cache, classifier,
   incremental parsers) with a different handler on the socket: no `/api/*`, no static files,
   one `/agent/v1/*` tree. The model an agent serves is the model it would show locally.
3. **Transport: TLS on a TCP port, a self-signed certificate pinned at pairing, a bearer per
   hub (decided).** Self-contained: no process outside the binary, works wherever TCP reaches
   (VPN, LAN, a tunnel), pairing possible from the Settings page, and it gives the owner the
   things asked for by name — a token, its rotation, a protocol of its own. A CA is not needed:
   the agent mints an ECDSA P-256 self-signed certificate on first start (10-year lifetime); the
   pairing string carries its SHA-256 fingerprint; the hub's `VerifyPeerCertificate` compares
   the leaf's fingerprint **and nothing else — not the name, not `NotAfter`** — so a pin never
   expires under the hub (~50 lines, `crypto/tls`, `crypto/x509`). TLS is always on, whatever
   the bind: the protection lives in the binary, not in the operator's network, so an agent
   started on a public interface by mistake exposes a handshake and 401s, nothing more.
   *Rejected (owner, 2026-09-19): a unix socket reached through `ssh host todobem agent
   connect`.* Leaner — no certificate, token, bearer or rotation, and rule 8 literally intact
   for the agent — but it presumes an ssh connection from the hub to every machine, and there
   will not be one. It would also have made the hub depend on the OpenSSH client (a runtime
   dependency outside the stdlib), run one `ssh` process per agent, ruled out browser pairing,
   and left nothing to "regenerate". *Rejected: plain HTTP over tunnels* — the same presumption
   about the network, and the protection would live outside the binary.
4. **The existing `internal/auth` primitives, a second key file.** `~/.todobem/agent.key`
   is the agent's root of trust (0600, refused when group/world-readable). A **pairing token** is
   `auth.MintToken` from that key: 52 characters, one use, five minutes, refused if minted before
   the agent's boot second (its timestamp has whole-second precision). `POST /agent/v1/pair` redeems it for a **bearer**: `auth.MintSession` with a
   long TTL (365 days by default), stateless, HMAC-signed, verified on every request from
   `Authorization: Bearer …`. A separate key means rotating the UI's sessions never breaks the
   fleet and a UI login token can never be redeemed at an agent. Two footguns, named: the
   startup pairing token is minted *after* the verifier's `boot` is captured; and `todobem agent
   pair` run while the agent is down yields a token the agent will refuse once up (the boot
   check) — the CLI says so. The TTL is not what protects anything: the threat is the hub's
   `~/.todobem/agents.json`, which holds read credentials to every paired host's private
   conversations; the answer to a hub compromise is `todobem hub rotate --all` (one command,
   §7.2), and the file is 0600 and named in the security notes. Layouts: §6.2. *Rejected: a
   new token format with scopes* — one hub per agent holds one credential; a per-command scope
   protects nothing a leaked agents file has not already lost. First thing to add if agents
   ever serve several hubs.
5. **Composite ids on the hub: `<uuid>@<host>`.** The UI, the cache, the hash route and every
   `/api/*` call already carry an id; giving a remote session an id that names its host keeps
   `app.js`, `inspector.js` and `insights.js` unchanged except for display. Agent names are
   `^[a-z0-9][a-z0-9-]{0,62}$` (no dot: the cache names sidecars `<id>.<kind>.json.gz` and
   splits on the first dot). `store.safeName` already hashes an id with `@` to a SHA-1 name, and
   `Prune` keys by the same function, so **no store change**. Two hosts holding a copy of the
   same session are two rows, honestly. **The hub rewrites two ids on ingestion**: `model.ID`
   (the inspector builds `/op/{id}` from `m.id`, not from the route) and `Facts.ID` (the
   report's evidence links open `#session/<id>`), both to the composite id — one assignment
   each, without which every remote op detail and evidence link 404s. *Rejected:
   `/api/hosts/{h}/sessions/{id}`* — touches every fetch in three JS files for no gain.
6. **The transfer unit for a model is the cache file.** `{cache_version, fingerprint, model,
   details}` — what `store.Save` writes, gzipped; `store.Encode`/`Decode` are factored out of
   `Save`/`Load` so the wire and the disk share one encoder (and an agent with `-cache off`
   still answers, just slower). The hub stores the file, after the id rewrite, under the
   composite id; `/api/sessions/{id}/op/{op}` works from `details` without another round trip.
   No new format, no migration: a version mismatch is a miss, as it is locally.
7. **Deltas by a scan generation, reconciled by a hash.** The agent counts a generation on
   every index scan that added or changed any displayed row field or its parsed `totals` and
   remembers per id the generation it last changed in. `GET sessions?since=<boot>:<gen>`
   returns the rows changed since, plus the current `count` and `ids_hash` (SHA-1 of the sorted
   ids, 16 hex). **There are no tombstones**: a deleted rollout and an agent restart (the boot
   nonce changes) are both caught by the hash mismatch, and the hub asks for the full list. Idle
   poll ≈ 300 B each way. *Rejected: `since=<updated ms>`* — mtimes lie on copied trees and say
   nothing about deletions. *Rejected: an ETag on the whole list* — a live session changes
   `bytes` on every scan, so an active agent with 500 rows would resend ~190 KB per poll. The
   exact algorithm: §6.5.
8. **Facts by id, not by cursor.** After a delta the hub knows which rows changed; it asks
   `POST facts {ids}` in batches of 100. The agent answers with the facts it has (closed
   sessions its digest has parsed) and a `pending` list (live, or not digested yet) the hub
   retries on the next poll. One cursor mechanism, not two.
9. **Two control routines, no shell, no wire argument that reaches a process.** `GET doctor`
   (a read-only report) and `POST rotate` (two-phase key rotation, §7.2). Nothing the hub sends
   is ever executed on the host. **Upgrade over the wire is postponed to a feature of its own**
   (owner, 2026-09-19): the bearer that reads sessions must not, in v1, be the thing that can
   replace the program; appendix A keeps the sketch and the alternatives it had already ruled
   out, so that feature starts from a reviewed design rather than from zero.
10. **Project stays `cwd`; Host is a second filter.** The log records a path, not a repository;
    the same path on several hosts merges naturally (the project entry says "· 3 hosts"), a
    different path is a different project, and the Host select narrows either to one machine.
    Under deploy tooling identical paths are the norm. *Deferred (needs the owner):* a project
    alias across paths. When it comes, its literal basis exists for Codex — `session_meta.git`
    records `repository_url` and `commit_hash` (verified on a current rollout head; the index
    reads only `branch` today) — and not for Claude Code (`cwd` and `gitBranch` only).
11. **Adaptive polling, staggered, keep-alive, and a round on demand.** 60 s per agent while a
    browser is looking (any `/api/sessions` or report request in the last 10 minutes), 10 minutes
    otherwise; each agent's phase is offset by a hash of its name so a hundred agents never fire
    together; one `http.Transport` per agent reuses the TLS connection. The list page does not
    poll (only the session page does), so a list request arriving after an idle stretch kicks an
    immediate staggered round in the background and the list shows each remote row's age
    ("web-01 · 9 min ago"). `hello` is fetched at pairing, after an error, and when the boot
    nonce in the cursor changes — not every poll. Estimated cost for 100 agents: ~100 KB/min
    while a page is open, ~10 KB/min idle, ~2 KB per changed session, ~4 KB per closed
    session's facts; models move only when a session is opened. The schedule: §6.7.
12. **Protocol versioned in the path; data versioned by the numbers that already exist.**
    `/agent/v1/`; `hello` reports `protocol`, `version`, `cache_version`, `facts_version`,
    `rules_fingerprint`. The hub lists sessions of any v1 agent, shows models only from agents
    whose `cache_version` equals its own, facts only from equal `facts_version`, and says which
    is missing on the row ("agent build differs"). Within v1, payloads only grow and a field
    never changes meaning; a breaking change is v2. Rolling upgrades across a fleet degrade per
    host, never break the list. `version` comes from `runtime/debug.ReadBuildInfo()` — a plain
    `go build` of this repository already embeds `vcs.revision`, `vcs.modified`, `GOOS` and
    `GOARCH` (verified) — so `todobem -version` needs no `-ldflags` and no Makefile change;
    `dev` when built outside the clone. The compatibility rules: §6.8.

## 4. The agent

### 4.1 Running it

```
todobem -agent                       # TLS on :7789, every interface; UI off; prints a pairing string
todobem -agent -addr 127.0.0.1:7789  # loopback (tunnelled, or an agent under test) — TLS still on
todobem agent pair [-revoke] [-q]    # a fresh pairing string from the key file (-q: the string only)
todobem -version                     # todobem <revision> <goos>/<goarch> protocol 1
AGENT=1 ./scripts/deploy.sh          # build, start in the background, print a pairing string
```

Flags shared with the viewer keep their meaning: `-codex`, `-claude`, `-settings`, `-rules`,
`-cache`. `-auth` and `-open` are refused in agent mode (`-auth=off` would mean an open agent;
there is no browser). The default `-addr` is `:7789` in agent mode and the startup line says so:
`agent: listening on :7789 (TLS sha256:…) · pair: todobem hub add …`. The pairing string is
printed once at start, as asked: it is single-use and dead after five minutes, so its presence
in a 0600 log is worth nothing to a later reader; `todobem agent pair` mints another at any time
(against a running agent — see decision 4). The agent reads the homes of the user it runs as
(its settings file, else `~/.codex` and `~/.claude`); on a shared host that is one agent per
user, each on its own port. Logging is `log` to stderr: start, pairing, each control call with
its caller, authentication failures (throttled), a digest summary every ten minutes, errors.
Polls are not logged. The README gets a systemd unit to paste (`deploy.sh run` in the
foreground, `Restart=on-failure`): a hundred unattended agents want a supervisor, not a `nohup`.

Files under `~/.todobem/`: `agent.key` (0600), `agent.crt`, `agent-tls.key` (0600), and the
usual `cache/`, `settings.json`, `rules.json`. The UI's `auth.key` is never touched.

### 4.2 What it does between requests

- **Index scans** on request when the index is older than 20 s (as the viewer does): no hub, no
  scan, no CPU.
- **Digest** (from step 3, when facts ride the wire): one worker parses, newest first, every root
  session whose fingerprint has no cache entry and that it has not tried at this fingerprint,
  with a pause between sessions; a live session's model is not cached (the existing rule) and
  it is retried only when its files change. Ticks every 5 minutes (rescans first). No knob in
  v1; the doctor reports its progress.
- **Live sessions** on demand: a model request with `refresh=1`, or a `version` poll, refreshes
  an opened session incrementally (the LRU of 6 parsers), the same path as the viewer.

### 4.3 What it speaks

The routes, the token and bearer layouts, the cursor and the compatibility rules are in one
place, §6 — the normative protocol reference. In one line: `/agent/v1/` over pinned TLS, a
bearer on every request but `pair`, JSON in and out, a delta list by scan generation, models as
cache files, facts by id, two control routines.

## 5. The hub

### 5.1 Pairing and the agents file

```
todobem hub add [-name web-01] [-addr 10.0.0.5:7789] '<pairing string>'
todobem hub list
todobem hub remove [-revoke] web-01
todobem hub doctor web-01 | -all
todobem hub rotate web-01 | -all
```

Bulk onboarding is whatever runs commands on the hosts already — a provisioning tool, a
console — collecting each host's `todobem agent pair -q` line and feeding it to `hub add`; the
hub itself needs only TCP to the agents.

The pairing string is `todobem-agent://<hostname>:<port>/#<fingerprint>.<token>`; `-addr`
overrides the authority (the agent knows its hostname, not the address the hub can reach). `add`
dials with the pin, redeems the token, and appends to `~/.todobem/agents.json` (0600 — it holds
bearers): `[{name, addr, pin, bearer, expires}]`. Flags come before the name, as in every
other subcommand. Every CLI and UI read-modify-write holds the adjacent advisory lock, so
concurrent processes cannot overwrite one another's fleet change. A running hub re-reads
the file when its mtime changes; content and mtime come from one open descriptor so an atomic
replacement cannot suppress that reload. Every `hub` command takes effect without a restart; the
commands talk to the agents directly (they need the file, not the hub), a bounded pool of 8 in
parallel for `--all`. **The hub announces its fleet the way `-auth=off` is announced**, on the
startup line: `fleet: 3 agents (~/.todobem/agents.json)` — it is the one todobem that dials out.

The Settings page gains a **Servers** section: the table below (step 2, read-only — the fleet's
health view), then "Add server" with the pairing string and per-row *Doctor*, *Rotate*, *Remove*
(step 4).

| column | source |
|---|---|
| name, address | agents file |
| status | `ok · 12 s ago` · `unreachable since 14:02 (dial tcp: i/o timeout)` · `incompatible: protocol 2` · `build differs: models unavailable` · `token expires in 9 d` |
| version, protocol, rules | last `hello` |
| sessions, digested | last delta's `count`, `hello.digest` |

`remove` drops the agent, stops its poller before deleting its snapshot, and drops its cache
entries; `-revoke` first asks for a replacement bearer, records it durably, then proves it once
so the staged key is promoted. Only then is the record removed and the replacement discarded.
Thus a failed file update never leaves a recorded agent with an invalid bearer. The old bearer
is dead and the agent is paired with nobody until `agent pair`.

### 5.2 Storage — cache-shaped, nothing to migrate

- `~/.todobem/fleet/<name>.json.gz`: the agent's last `hello`, cursor, `count`, `ids_hash`,
  rows, durably held facts, `last_ok`, `last_error`. Written after every poll that changed
  something. It is what
  makes the list instant at start and lets the hub show an unreachable agent's last-known rows,
  greyed, with the badge.
- Models and facts in the existing cache dir under the composite id (hashed by `safeName`);
  `todobem cache -prune` keeps what the fleet snapshots list.
- Every file is derived; deleting `fleet/` costs one full list per agent.

### 5.3 Inside `internal/server`

`Server.SetFleet(*fleet.Fleet)`. `summaries()` appends `fleet.Summaries()` (rows carry `host`,
ids composite); the Insights report already selects its candidates from `summaries()`
(`insights.go` `selectSessions`), so remote sessions enter reports with no change to the
selection. `loadModel`, the `version` case of `handleSession`, `handleEvent`,
`insightsSvc.factsFor` and `Server.fingerprint` route an id that `fleet.Split` recognises to the
fleet — the last one matters: `fingerprint` of an unknown id is rules-only today, which would
make a remote session never look stale to the report's `sources_hash`; the fleet answers with
the row's `(bytes, updated)`. A remote session the agent has not digested is *pending* on the
hub too, shown as "digesting on web-01 (12/40)" — the hub's Analyze button never parses remote
files (`scanLoader.Parse` opens local sources only). `Params` gains `Hosts` the way it has
`Sources`. That is the whole footprint in the server: seven call sites and one new file,
`remote.go`.

## 6. Protocol reference (v1)

The one normative description of what passes between a hub and an agent. §3 says why each
choice was made; this section says exactly what is sent. Both sides are the same binary, so
"the agent" and "the hub" below are the two roles of `internal/fleet`.

### 6.1 Transport

- TCP, default port **7789**, TLS 1.3 (`MinVersion`), HTTP/1.1 with keep-alive; one
  `http.Transport` per agent on the hub, so a hundred agents are a hundred long-lived
  connections (`IdleConnTimeout` 90 s).
- The agent's certificate is self-signed, ECDSA P-256, 10-year validity, minted on first start
  into `~/.todobem/agent.crt` + `agent-tls.key` (0600). Its **pin** is the SHA-256 of the DER
  leaf, 64 lowercase hex. The hub sets `InsecureSkipVerify: true` and verifies in
  `VerifyPeerCertificate` that the leaf's pin equals the paired one — nothing else: not the
  subject, not the SANs, not `NotAfter`. The hub presents no client certificate.
- Base path `/agent/v1/`. Nothing else is served on the port: `/` and any path outside the base
  answer 404 with an empty body.

### 6.2 Identity and authentication

**Pairing string** (printed by `-agent` at start and by `todobem agent pair`):

```
todobem-agent://<hostname>:<port>/#<pin>.<token>
```

`hostname` is `os.Hostname()` — a hint, overridable with `hub add -addr`; the fragment splits at
its first `.` into the 64-hex pin and the 52-character token.

**Pairing token** — `auth.MintToken` under `~/.todobem/agent.key` (32 random bytes, 0600):
`nonce[8] ‖ issued[4] ‖ ttl[4] ‖ HMAC-SHA256(key, 'T' ‖ first 16)[:16]`, base32 without
padding, 52 characters. Accepted once, within 5 minutes of `issued`, and never if `issued`
precedes the agent's boot. `ttl` is the bearer's lifetime the token will grant (365 days by
default, `agent pair -ttl`).

**Bearer** — `auth.MintSession` under the same key: `expiry[8] ‖ nonce[16] ‖ HMAC-SHA256(key,
'S' ‖ first 24)`, base64url without padding, 75 characters. Sent as
`Authorization: Bearer <value>` on every request except `pair`. Verified statelessly on every
request against the current key file(s); nothing about a bearer is stored on the agent.

**Key rotation** (`rotate`, §7.2) is two-phase: the agent writes `agent.key.next`, answers with a
bearer under it, accepts both keys while `.next` exists, and promotes `.next` on the first
request that verifies under it — from which point every bearer under the old key is refused.

### 6.3 Conventions

- JSON, UTF-8. Request bodies are `application/json` (a POST with another media type is 415);
  answers are `application/json`, gzip when `Accept-Encoding: gzip`, `Cache-Control: no-store`.
- Every request body is bounded with `http.MaxBytesReader`: 4 KB (`pair`, `rotate`), 64 KB
  (`facts`); over the bound is 413. A model response is capped at 256 MiB compressed before
  allocation, and every cache object at 256 MiB after decompression.
- Times are Unix milliseconds on the **agent's** clock (as everywhere in the model); `hello.now`
  lets the hub measure skew and the doctor reports it.
- Unknown JSON fields are ignored by both sides (`encoding/json` default; the JS likewise) —
  this is what lets a v1 payload grow.
- Status codes: 200 · 304 (`sessions/{id}` unchanged) · 400
  (`{"error":"bad_request","reason":…}`) · 401 (`{"error":"auth"}`, plus `"reason"` on `pair`:
  `invalid` · `expired` · `used`) · 404 (unknown id, unknown route, an event span not recorded)
  · 405 (`Allow` set) · 413 · 415 · 500.
- The agent never logs a poll; it logs `pair`, `rotate`, `doctor`, authentication failures
  (throttled to one line per minute per address) and errors.

### 6.4 Routes

**`POST pair`** — exchange a pairing token for a bearer.

```
→ {"token": "<52 chars>"}
← 200 {"bearer": "<75 chars>", "expires": 1789806000000, "hello": {…as GET hello…}}
← 401 {"error": "auth", "reason": "invalid" | "expired" | "used"}
```

**`GET hello`** — who the agent is and what it speaks. Fetched at pairing, after any failed poll,
and when the `boot` half of the cursor changes — not on every poll.

```
← 200 {
  "protocol": 1,
  "version": "2f72db5",            // vcs.revision[:7], "+dirty" when modified, "dev" when unknown
  "os": "linux", "arch": "amd64",
  "hostname": "web-01",
  "started": 1758270000000, "now": 1758273600000,
  "cache_version": 10,             // store.cacheVersion — models are comparable only when equal
  "facts_version": 16,             // insights.FactsVersion — facts likewise
  "rules_fingerprint": "a1b2c3d4…", // classify.RulesFingerprint(): built-in rules + the host's overlay
  "homes": [{"source": "codex", "path": "/home/ci/.codex", "status": "ok", "sessions": 312},
            {"source": "claude", "path": "/home/ci/.claude", "status": "missing", "sessions": 0}],
  "roots": 312,
  "digest": {"done": 290, "total": 312}
}
```

**`GET sessions?since=<cursor>`** — the session list as a delta.

```
← 200 {
  "cursor": "3f9a1c2e8b7d6f40:44",
  "full": false,                    // true when `since` was absent or its boot is not this one
  "rows": [ …model.SessionSummary… ], // exactly the rows /api/sessions serves, without `host`
  "count": 312,
  "ids_hash": "9c1d5e7a3b2f4d60"
}
```

Rows carry everything the local list has (`totals` only for sessions the agent has opened —
the same semantics as locally). The hub sets `host` and rewrites `id` to `<uuid>@<host>` on
ingestion. Cursor and hash: §6.5.

**`GET sessions/{id}[?refresh=1]`** with `If-None-Match: "<version>"` — one derived model.

```
← 200  ETag: "<model.Version>"   Content-Encoding: gzip
   {"cache_version": 10,
    "fp": {"rules": "…", "files": [{"p": "/home/ci/.codex/sessions/…", "s": 1234567, "m": 1758270000000000000}]},
    "model": { …model.Session… },
    "details": {"<op id>": "<full command>", …}}
← 304  (the version still matches; for a live session the version changes at most once a minute)
← 404  (unknown id)
```

The body is byte-for-byte what `store.Save` writes (`store.Encode`); `refresh=1` forces a full
re-parse on the agent, exactly like the Refresh button locally. The hub rewrites `model.id` to
the composite id before storing or serving it.

**`GET sessions/{id}/version`** — the Follow-mode poll.

```
← 200 {"version": "…", "live": true, "ended": 1758273590000, "now": 1758273600000, "ops": 1234}
```

**`POST facts`** — facts of up to 100 sessions.

```
→ {"ids": ["<uuid>", …]}                                // ≤ 100; more is 400
← 200 {"facts_version": 16,
       "facts": [ …insights.Facts… ],                   // closed sessions the digest has parsed
       "pending": ["<uuid>", …],                        // live, or not digested yet — ask again later
       "unknown": ["<uuid>", …]}                        // not in the index (deleted) — drop the row
```

The hub rewrites `Facts.id` to the composite id. Facts of live sessions are never sent (they are
never persisted locally either); the hub shows such sessions as pending, "digesting on web-01".

**`GET event?session=<uuid>&file=<abs>&off=<int>&len=<int>`** — one recorded source line.

```
← 200  the line as JSON (a non-JSON span is served as a JSON string) — the /api/event contract
← 404  the span is not recorded in that session's model, or the file is not under a current home
```

**`GET doctor`** — the health report, read-only (§7.1). **`POST rotate`** `{}` → `{"bearer",
"expires"}` (§7.2).

### 6.5 The cursor and reconciliation

Agent state, in memory only: `boot` (8 random bytes hex at start), `gen` (starts at 1, +1 on
every index scan in which a row was added or any displayed field, pending-question signal or
parsed `totals` changed), `changed[id] = gen` for every row, and after each scan `count` and
`ids_hash = hex(sha1(sorted ids joined by "\n"))[:16]` (ids sorted bytewise).

```
since absent, or since.boot ≠ boot  →  full: true, every row
since.boot = boot                   →  full: false, rows with changed[id] > since.gen
cursor in the answer                =  boot ":" current gen
```

There are no tombstones. Hub algorithm, per poll:

1. `GET sessions?since=<stored cursor>` (absent on a fresh pairing or after `fleet/` was
   deleted).
2. `full` → replace the agent's rows; else upsert the rows by id.
3. Compute `count` and `ids_hash` over the hub's rows for that agent; if either differs from
   the answer's → `GET sessions` without `since` at once and replace. This is how a deleted
   rollout, a restarted agent and any lost update are healed, with one extra request.
4. Store the new cursor; persist the snapshot if anything changed; queue the changed ids for
   `facts` (the rows whose `updated` differs from the facts the hub holds). A fact is marked held
   in that snapshot only after the server wrote its sidecar; with cache off or a failed write it
   stays in memory for this run but is fetched again after restart.
5. On a transport error or a non-200: mark the agent unreachable with the time and the error,
   keep serving its last rows greyed, fetch `hello` on the next success.

### 6.6 Freshness of a remote model on the hub

Stored with every remote model: the row's `(bytes, updated)`, the agent's rules fingerprint and
its cache/facts versions, plus the model `version`. On open: if that fingerprint still matches
→ serve the stored model, no request. Otherwise `GET sessions/{id}` with `If-None-Match`
and take the 304 or the new file. Follow mode proxies `version` and re-fetches on change; the
Refresh button passes `refresh=1` through. `/api/event` on the hub takes a `session` parameter
(the composite id) to route the span to the owning agent; a local server ignores it.

### 6.7 Polling schedule (hub)

- **Active** — a browser asked the hub for `/api/sessions` or an Insights report within the last
  10 minutes: every **60 s** per agent. **Idle**: every **10 minutes**. A list request arriving
  while idle starts one staggered round at once, in the background; the list shows each remote
  row's age.
- Agents are phase-shifted by `hash(name) mod interval`, so a hundred agents never fire in the
  same second (≈ 1.7 requests/s fleet-wide when active).
- After three consecutive failures an agent is polled at the idle interval even when active.
- Models: only on open. Facts: right after the delta that changed them, batches of 100.
- `hello`: at pairing, after a failure, on a `boot` change.

### 6.8 Versioning and compatibility

1. The protocol version is the path segment. Within `v1`: fields are only added, a field's
   meaning never changes, a request never gains a required field. Anything else is `v2`, served
   next to `v1` for one release.
2. An agent that answers 404 for `GET /agent/v1/hello` is *incompatible: protocol*; the Servers
   table says so and the agent's rows stay as last seen.
3. Models are shown only when the agent's `cache_version` equals the hub's; facts only when
   `facts_version` does. The list works across any mix. The row says which is missing ("agent
   build differs: models unavailable"). Nothing is converted on either side — a version
   mismatch is a cache miss, as it is locally, and there is nothing to migrate.
4. `rules_fingerprint` is informational: the model is the agent's truth, classified with the
   agent's rules and overlay; the hub never re-derives, and the Servers table shows agents whose
   rules differ from the hub's.

### 6.9 Budget (measured on the author's cache, see the header)

| what | size | when |
|---|---|---|
| idle poll (`sessions` delta, nothing changed) | ~300 B each way | per agent per interval |
| one changed row | ~0.4–2 KB gz (`last_answer` is clipped at 1200 B) | on change |
| one closed session's facts | ~4 KB gz median | once per closed session |
| one model | 61 KB gz median, 393 KB p90, 3.2 MB max | on open, then 304s |
| `hello` | ~500 B | pairing, failure, boot change |
| 100 agents, active | ~100 KB/min | while a page is open |
| 100 agents, idle | ~10 KB/min | otherwise |

## 7. Control — two fixed routines

Each action is one function in the agent with no parameter from the wire. The bearer that reads
sessions is the bearer that controls (decision 4); neither routine can change what the host
runs.

### 7.1 `doctor`

Read-only, JSON: version, protocol, OS/arch, uptime, clock (`now`, for skew), homes with their
statuses and counts (`HomeStatuses`), roots indexed, digest progress, cache entries and bytes,
the last scan's duration, `agent.key` and TLS key file modes, certificate fingerprint, bearer
expiry of the caller, Go version, goroutines, RSS. The hub prints it or shows it in a dialog. It
runs nothing external.

### 7.2 `rotate` — two-phase, so a lost answer never locks the hub out

1. The agent writes fresh bytes to `agent.key.next` and answers with a bearer signed by the new
   key. The old key stays valid.
2. `KeySource` verifies against both files while `.next` exists. The first request that verifies
   under the new key promotes it: `.next` → `agent.key`, and the old key — with every bearer
   signed by it — is dead from that request on.
3. If the hub never received the answer, it keeps using the old bearer, which still works, and
   the next `rotate` reuses the same `.next` key, so a bearer from either answer can promote it.

About 40 lines over `auth.KeySource`. `todobem agent pair -revoke` (rotate on the host itself)
remains the emergency exit. Rotation is manual (`hub rotate --all`, cron if wanted); the Servers
table warns 30 days before a bearer expires.

## 8. UI changes, kept to display

- `SessionSummary.host`, `Session.host` (`""` = this machine). The list has a **Host** column
  (second): the agent's name for a remote row, empty for this machine; the mobile tile carries
  the same as a chip before the title. The session header reads `web-01 · /srv/app`; the session
  page names the agent's file path as it names local ones.
- `filter.js`: a **Host** select (All hosts · this machine · each agent) rendered when the list
  holds more than one host. The Project select names paths without an ellipsis; a path seen on
  exactly one agent is labelled with it (`web-01 · dev/app`), one seen on several says how many
  ("· 3 hosts"). Insights reuse it (`hosts=` parameter, like `sources=`). The host chip and the
  select live in `filter.js`, not in `app.js`.
- `settings.js`: the Servers section (§5.1).
- Remote rows of an unreachable agent are greyed with the agent's badge; a model that cannot be
  shown says why (build differs · unreachable) instead of a spinner.
- `app_test.js`: the id split for display, the host filter, the project host counts.

## 9. Product rule 8 (amended in CLAUDE.md, 2026-09-19, at the owner's direction)

Rule 8 read "… no telemetry, no outbound calls. Ever." It now names the exception and its two
roles, and the allowance for testing the owner asked for: *"no outbound calls — except for the
matter of the fleet, chosen by the owner, and of testing the core functionality of the product:
an **agent** answers — never calls — the hub its owner paired it with, over TLS pinned at
pairing, to a holder of the bearer; a **hub** dials only the agents listed in its
`~/.todobem/agents.json` (0600, their read credentials), and announces that list at start; and
a test or a verification run may connect a hub and an agent of its own, on loopback, for as
long as it runs. Neither role ever speaks to anything else; nothing else ever speaks out. The UI
stays on loopback in every mode."* The definition of done's "never a second instance on another
port" gained the matching exception: an agent under test on `:7789`, paired with the instance
on `:7788`, ending with the task. README's "Nothing leaves the machine" paragraph and
`ARCHITECTURE.md` §1 ("everything runs on loopback") follow with step 1, when the mode exists.

## 10. Not in v1 (named so they are not asked for later)

- Push mode / NAT traversal; a CA; certificate rotation (delete the files to re-mint and re-pair).
- Per-command scopes on the bearer; automatic rotation; more than one hub per agent.
- Project aliases across paths (the literal basis is in decision 10); deduplication of a session
  copied to two hosts.
- Server-side paging of the list. The list stays client-side as today at ~370 B per row
  gzipped; the practical ceiling is ~10 k rows (3.7 MB per list load), **which a fleet reaches
  early** — 100 hosts × 100 sessions is there already. The fix when it comes is a period filter
  on the hub's snapshots (they carry `updated` per row): no schema change, no protocol change.
- `todobem unknown` across the fleet (it stays local); Insights "by host" cards beyond the Host
  filter.
- **Upgrade over the wire** — its own feature, later (appendix A). Until then an agent is
  upgraded the way it was installed.
- Any agent → agent traffic; any traffic the owner did not configure.

## 11. Implementation plan (usable after step 2; docs land with each step, never after)

| step | delivers | files |
|---|---|---|
| 1 | `-agent` (TLS, key, pair, hello, sessions delta, cursor), `agent pair`, `-version`; `store.Encode/Decode`; `ARCHITECTURE.md` §7.4 + §8 rows + repo map, `CLAUDE.md` rule 8 and map, README agent section with the systemd unit | `internal/fleet/{wire,tls,cursor}.go`, `internal/server/agent.go`, `cmd/todobem/{main,agent}.go`, `internal/store/store.go`, `deploy.sh` (`AGENT=1`) |
| 2 | hub: agents file, pollers with the on-demand round, snapshots, composite ids **with the `model.ID`/`Facts.ID` rewrite and the remote fingerprint**, list + models + version + event through the UI, `hub add/list/remove`, the read-only Servers table, host chip and Host select; fleet announced at start | `internal/fleet/{agents,client,fleet,snapshot}.go`, `internal/server/remote.go`, `filter.js`, `settings.js`, `inspector.js` (the `session` parameter), `ARCHITECTURE.md` §8 |
| 3 | the digest, facts over the wire, Insights with Host, remote pending shown as digesting | `internal/server/{agent,insights}.go`, `insights.Params`, `insights.js` |
| 4 | `doctor`, two-phase `rotate`, the Servers buttons, `hub doctor/rotate`, `remove -revoke` | `internal/auth/auth.go` (`KeySource` with `.next`), `internal/server/agent.go`, `cmd/todobem/agent.go`, `settings.js` |

Tests: an in-process agent under `httptest.NewUnstartedServer` with the pinned TLS (pair,
bearer refusal, a token minted before boot, delta and reconciliation after a deleted row and a
restart, 304, facts pending, two-phase rotate with a lost answer); **one end-to-end case through
`Server`: the hub serving `/api/sessions/<uuid>@host`, its `/version` and one `/op/{id}` from a
fetched cache file** — that is where the `m.id` breakage lives and `fleet.Split` alone would not
catch it; the snapshot round trip; JS cases for the host filter. Fixtures synthetic, in
`t.TempDir()`, fake host names. Roughly 2,000 lines for steps 1–3 including tests, ~400 more
for step 4; stdlib only (`crypto/tls`, `crypto/x509`, `crypto/ecdsa`, `net/http`). Verifying
the real thing is the exception the definition of done now names: an agent on `127.0.0.1:7789`
pinned to a copy of a session folder, paired with the instance on `:7788`, both gone when the
task ends.

## 12. Review record (2026-09-19, `pragmatic` agent; approve with conditions)

Folded in: pin-only TLS ignoring `NotAfter` with a 10-year cert; the two `auth` footguns; the
hub-compromise note; two-phase rotate (the documented lockout was "the wrong bet with 100 hosts
and a cron"); no tombstones, hash-reconciled removals; `hello` timing; `safeName` change
dropped (hashing already works); the `model.ID` / `Facts.ID` rewrite and the remote
`fingerprint` (three breakages the first draft missed — inspector detail, evidence links and
report staleness would all have failed on remote sessions); `debug/buildinfo` instead of
executing the staged binary, and `runtime/debug.ReadBuildInfo` instead of `-ldflags`; body
limits; the on-demand round after idle and the per-host age; the early-arriving list ceiling;
the shared-host sentence; `-cache off`; rule 8 naming both roles and the hub announcing its
fleet; the digest moved to step 3; docs with each step; the end-to-end test; host UI out of
`app.js`; `FactsVersion` 16.

Two points were left to the owner and decided the same day: the reviewer's ssh transport is
rejected (no ssh connection to every machine will exist; the TCP/TLS design stands), and
`upgrade` is postponed to a feature of its own, as the reviewer recommended.

Verified by the reviewer: the size claims (within 25 %); that a plain `go build` embeds
`vcs.revision`, `GOOS`, `GOARCH`; that `safeName` hashes `@` safely. Verified by the author
after the review: Codex `session_meta.git` carries `repository_url` and `commit_hash`.

### Implementation notes (where the code differs from the text above)

- `hello` is fetched at pairing, at the first poll of a hub run whose snapshot has none, after
  a failed poll, and after any full page on a known cursor (the agent restarted); the answer
  also carries `digest.running`, and a facts page carries `unknown` (ids the index does not
  know — the row is dropped) and the digest's progress.
- The Servers table shows build, sessions, facts and bearer expiry; protocol and rules are in
  `/api/fleet` and `hub list`, not columns. The list rows carry the host chip without a per-row
  age; the last contact is the Servers table's status.
- `Poll now` (the page) and `PollNow` (tests, pairing) run the poll synchronously and ask for
  the pending facts again at once instead of waiting for the five-minute retry; the answer is
  the fresh status.
- The fleet never touches the cache: it fetches and hands over. The server owns every write —
  models under composite ids through `remoteModel`, facts through the sink `SetFleet` wires —
  and a Follow-mode poll that saw a newer version (`Fleet.Current`) makes the next open a
  conditional fetch instead of a cache hit, so a live remote session never shows a stale model.
  With `-cache off` on the hub every open of a remote session is a conditional fetch.

## Appendix A — upgrade over the wire (postponed; the sketch as reviewed)

Not in v1. Kept so the later feature starts from what was already worked out.

- **Shape:** `PUT /agent/v1/binary` (`X-Todobem-Sha256`, octet-stream, `MaxBytesReader` 64 MB),
  answered only when the agent was started with an explicit opt-in flag (`-allow-upgrade`);
  without it, 403. `todobem hub upgrade -binary ./todobem-linux-amd64 web-01|--all`, a bounded
  pool; the hub polls `hello` until `version` changes and reports per agent.
- **Verification without execution:** the agent writes `<exe>.new` (0755) while hashing, checks
  the hash, then reads the staged file with `debug/buildinfo.ReadFile` (stdlib) — the main
  module path must be this module's, `GOOS`/`GOARCH` must equal the agent's own, `vcs.revision`
  is logged. Nothing uploaded is ever run to be judged (running `-version` on it would let a
  hostile binary print the expected line).
- **Swap:** `<exe>` → `<exe>.old`, `<exe>.new` → `<exe>`, answer `{staged, version}`, log the
  caller, `syscall.Exec` with the same arguments (same PID; the socket is reopened). Unix only.
  `<exe>.old` stays for a manual rollback; an agent that cannot start afterwards needs its
  supervisor — the systemd unit of §4.1.
- **Said plainly:** with the flag, a bearer holder can replace the program that runs as the
  agent's user. That is the feature. The question the later design must answer first is
  whether that power should ride the same bearer that reads sessions, or a second credential.
- **Rejected already:** `git pull && go build` on the host (toolchain and clone everywhere, an
  outbound fetch, provenance "whatever main was"); a download URL from the hub (a backdoor with
  extra steps).
