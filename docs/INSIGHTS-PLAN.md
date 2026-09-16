# Insights extension — execution plan

*todobem · 16 Sep 2026 · a working plan; `docs/ARCHITECTURE.md` stays the one document and is
updated by the work packages below.*

Goal: make the Insights report answer "which earlier work stopped being current because of a
later recorded event" — a change with no test after it, a review with edits after it, a retry
with nothing recorded between — from the logs both harnesses already write, under the product
rules (literal signals, complete counts, no estimate, no causal title). Source: the brainstorm
brief (`docs/INSIGHTS-BRAINSTORM.md`) and the review proposal that answers it; the assessment of
that proposal, the corpus numbers and the design review are in Appendix A–C.

Reading order for an implementer: §1 (the rules the new code obeys), §2 (work packages, in
execution order, each with files, steps, tests and a done criterion), §3 (decisions still open),
§4 (later batches). Effort: batch 1 is 4 to 5 working days for one engineer including the
validation run and the browser pass.

## 1. Rules every new detector obeys

- Literal order of recorded events, session scope unless said otherwise. No duration threshold,
  no prose, no similarity.
- A **Check** card says "the sequence happened in N of M sessions" and opens the interval. It
  never adds to a group's time or tokens total and is ranked by sessions affected among Checks.
- **The denominator is the sessions the rule applies to.** A session where the rule's
  precondition is absent (no change op for D17, no review run for D24) is `NotApplicable`:
  neither in `Of` nor in "no data" — it did not happen, and it could not have. The card text
  names the denominator ("of M sessions with changes").
- Complete counts come from every op of the session, computed in `Extract`; evidence lists are
  capped (10 rows per card, as today). A detector never counts from a capped list. When the
  walker could not decide (an `unknown` or `no_telemetry` interval inside the decisive window)
  the session is `NoData` for that rule, never counted.
- Verification means a **successful op in phase `test`** (runners, linters, type checks,
  simulators — the phase already holds `go vet`, `mypy`, `pyright`, `eslint`, `ruff`,
  `golangci-lint`, `npm run typecheck`) or a **stop hook** whose command classifies as `test` and
  ended without a hook error. Build does not count.
- A title states the recorded order ("Final changes had no later successful test"), never a
  cause ("unverified code", "flaky", "waste"). Advice changes the harness (a hook, a skill, an
  instructions file, a rule), never judges the person or the model.
- Headline numbers come from `Result.Stats`, never parsed back out of a finding's note.

## 2. Batch 1 — work packages in execution order

Dependencies: WP1 and WP2 are independent of each other and precede WP4; WP3 precedes WP5 and
WP6; WP7 and WP8 close the batch. Each package is one commit (small, single-concern).

### WP1 · One definition of a type check — `internal/classify`

Why first: D17 counts a type check as verification only when it sits in phase `test`; today
`tsc` / `npx tsc` and the dispatcher verb `typecheck` are `build` while `npm run typecheck`,
`mypy`, `pyright` are `test`. A TypeScript project verifying with `tsc --noEmit` or
`./dev typecheck` would be a permanent D17 false positive.

| | |
|---|---|
| Files | `classify.go` (`Rules`, `dispatcherVerbs`), `classify_test.go`, `docs/ARCHITECTURE.md` §4.1 (one sentence) |
| Steps | 1. a `seg:` rule `^(npx )?tsc\b.*--noEmit` → `test/typecheck`, ahead of the `tsc` word rule (word rules match first, so the regex must be checked before or the word rule must exclude the flag — verify in `Command` and add the case that proves the order); 2. `dispatcherVerbs["typecheck"]` → `Test`, kind `test-flag`; 3. bare `tsc` stays `build` (it emits JavaScript) |
| Tests | `tsc --noEmit`, `npx tsc --noEmit -p tsconfig.json`, `./dev typecheck`, bare `tsc`, `tsc -p . --noEmit --watch` (still test); the subgroup test (every kind of a subgrouped phase maps) |
| Done | `go test ./internal/classify`; `cmd/dump` on the sessions of the local corpus that contain `tsc` or a `typecheck` verb (none in the probe corpus: then on a synthetic fixture); no new `unknown` head; a §11 note in the local validation report |
| Estimate | 0.5 day |

### WP2 · One op per stop hook — `internal/claude`

Why: Claude Code's `stop_hook_summary` carries, per hook, its command and `durationMs` plus a
`hookErrors` list; the adapter sums them into one op "stop hooks · N" and the per-hook durations
are lost. With one op per hook, a slow hook appears in D9 under its own shape, a failing hook in
D15, a hook re-running the agent's test command joins that command's retry group, and a hook
running the project's test command counts as verification for D17 (Appendix B.3).

| | |
|---|---|
| Files | `lane.go` (`onSystem`, case `stop_hook_summary`), `lane_test.go`, `docs/ARCHITECTURE.md` §2.2 (the `stop_hook_summary` row) |
| Steps | 1. for each `HookInfos[i]`: an op `wait_worker/hook`, interval `[ts − durationMs_i, ts]` clamped as today to the message end (hooks end together at the summary line; overlapping intervals are what the partition already handles); 2. `Title` = "stop hook · " + the command's head (via `classify.Command(cmd).Title`), `Detail` = the command, `Identity` = `cwd + "\n" + classify.Identity(cmd)` so an identical command groups with the agent's own run; 3. `Rule` = "hook · " + the classifier's phase and kind of the command (the inspector shows it; the phase of the op itself stays `wait_worker`: harness time inside the turn, the activity partition does not change); 4. `Status` = `failed` when `hookErrors` is not empty (the summary does not say which hook failed: every hook of that summary is `failed`, and the note says so) |
| Tests | one hook; three hooks with one error (all three failed, Detail intact); a hook whose command equals the turn's last `go test` (same group, role `rerun`); a summary with `total == 0` (no op, as today) |
| Done | `go test ./internal/claude`; `cmd/dump` on the 12 corpus sessions with stop hooks: hook count equals the sum of `HookInfos` lengths, partition balanced, no new `unknown` head; the timeline shows the hooks as separate ops in the turn's tail |
| Estimate | 0.5 day |

### WP3 · Report and detector plumbing — `internal/insights`

Why: the new cards need a card class the report can rank without totalling, a denominator that
excludes sessions the rule does not apply to, and headline stats that survive a changed note.
The package also carries three defects the "leave it better" rule assigns to this change.

| | |
|---|---|
| Files | `detect.go`, `report.go`, `report_test.go`, `facts.go` (comment only), `detect_delivery.go` → split |
| Steps | 1. `Detector.Info bool` → `Detector.Class string` (`exposure` default, `info`, `check`); `Card.Info` → `Card.Class`; 2. `Result.NotApplicable bool`: `buildCard` skips such sessions for `Of` and for `NoData`; 3. ranking in `Build`: the two-way Info sort becomes a three-way class order (exposure, check, info); a Check is excluded from `Group.TimeMs` / tokens like Info; `TopTime` / `TopTokens` unchanged (a Check has no positive exposure); new `Report.TopChecks []string` (up to three rule ids by sessions affected, then count); 4. `statsFor`: D7's `groups` / `attempts` and D11's `context_*` come from `Result.Stats` set by the detectors, the two `Sscanf` calls over notes are deleted; 5. the `FactsVersion` comment lists every version (it describes 3 while the constant is 4); 6. split `detect_delivery.go` (it holds D4, D7 and D11 under a name that fits none) into `detect_agents.go` (D4, T6), `detect_retries.go` (D7) and `detect_context.go` (D11, T2); pure moves |
| Tests | a Check card is ranked, not totalled, `TopChecks` filled; a `NotApplicable` session is in neither `Of` nor `NoData`; D7 and D11 headline stats stay when a note changes; the class order in a group; existing tests pass unchanged after the split |
| Done | `go test ./internal/insights`; `gofmt -l .` empty; `go vet` |
| Estimate | 0.5 day |

### WP4 · The delivery walker — `internal/insights/facts_delivery.go` (new)

Why: one deterministic pass over the session's ops, sorted by start across all lanes, yields
every number D17, D24, D18, D19 and D44 need; `facts.go` (705 lines) must not grow.

| | |
|---|---|
| Files | `facts_delivery.go`, `facts_delivery_test.go`, `facts.go` (the call and the new fields), `cmd/dump/main.go` (print `DeliveryFacts`) |
| Steps | 1. `Facts.Delivery DeliveryFacts` (below), computed from every non-background op of every lane in start order — session scope: a sub-agent's edit is the session's edit, a sub-agent's test verifies it; 2. `WindowFacts` gains `Recovery string` (`none` \| `fix` \| `infra` \| `worker` \| `mixed`: a change op / infra op / worker wait on the same lane inside the window, first seen wins, `mixed` when several), `RetryFailed bool` (the attempt that closed the window failed too), `HumanBoundary bool` (a user-triggered root turn started inside the window); 3. `CompactionFacts.InChangeWindow bool` (the compaction's start lies inside its turn's change window on the same lane); 4. `GapFacts.AfterFirstChange bool`; 5. `FactsVersion` → 5 (a bump re-extracts from the model cache, never re-parses) |
| Tests | the fixtures listed under WP5 exercise the walker through `Extract`; direct cases for `Recovery` (each label, `mixed`, a user turn inside), `InChangeWindow`, `AfterFirstChange`, `BlindAfterLastChangeMs` |
| Done | `go test ./internal/insights`; `cmd/dump` on four corpus sessions per source prints `DeliveryFacts` that match the independent count (WP8) |
| Estimate | 1 day |

```go
// DeliveryFacts is the session read as one delivery loop: the order of change ops, successful
// test ops, review runs and stop hooks across all lanes.
type DeliveryFacts struct {
    Changes        int    `json:"changes"`            // change ops (classify.IsChangeOp), all lanes
    FirstChangeAt  int64  `json:"first_change_at,omitempty"`
    LastChangeAt   int64  `json:"last_change_at,omitempty"`
    LastChangeOp   string `json:"last_change_op,omitempty"`
    LastChangeLane int    `json:"last_change_lane,omitempty"`
    // Verified: a successful test op started at or after LastChangeAt, or a stop hook whose
    // command classifies as test and ended without a hook error. VerifiedBy "agent" | "hook".
    Verified   bool   `json:"verified"`
    VerifiedAt int64  `json:"verified_at,omitempty"`
    VerifiedOp string `json:"verified_op,omitempty"`
    VerifiedBy string `json:"verified_by,omitempty"`
    Tests      int    `json:"tests"` // test ops in the session, any result
    // LastVerdict: the last test op by start, and whether it failed (D19 stat).
    LastVerdictFailed bool   `json:"last_verdict_failed,omitempty"`
    LastVerdictOp     string `json:"last_verdict_op,omitempty"`
    // Review: the end of the last turn carrying a review run (Turn.Review, a review skill run or a
    // review-pinned turn); ChangesAfterReview counts change ops started after it.
    ReviewedAt         int64 `json:"reviewed_at,omitempty"`
    ChangesAfterReview int   `json:"changes_after_review,omitempty"`
    // Blind: unknown + no_telemetry time on the root lane between LastChangeAt and the session
    // end; when > 0 the walker may have missed a test and D17 is NoData for the session.
    BlindAfterLastChangeMs int64 `json:"blind_after_last_change_ms,omitempty"`
    // Per-turn aggregates: turns with a change op, those with no successful test in the same
    // turn at or after their last change (D18 stat), unknown + no_telemetry time inside the
    // root lane's change windows (D44 stat).
    EditTurns           int   `json:"edit_turns"`
    EditTurnsUnverified int   `json:"edit_turns_unverified"`
    UnknownInWindowMs   int64 `json:"unknown_in_window_ms,omitempty"`
}
```

### WP5 · Detectors — `internal/insights/detect_verify.go` (new) and existing files

| | |
|---|---|
| Files | `detect_verify.go`, `detect_verify_test.go`, `detect_retries.go`, `detect_context.go`, `detect_you.go`, `detect_unseen.go`, `detect.go` (catalogue, `GroupVerify`) |
| Steps | 1. group `GroupVerify = "verification"` ("Verification loop": *Were the last changes tested and reviewed before the answer?*), in `GroupOrder` before "Failures and retries"; a group of Checks has no time, so it sorts after every group with exposure and before "Not measured" with no special case; 2. **D17** (Check) and **D24** (Check) as in the table below; 3. **D7**: key stays the command shape; stats `groups`, `attempts`, `windows`, `windows_blind` (`Recovery == none && !HumanBoundary`), `windows_fix`, `windows_infra`, `windows_worker`, `windows_after_user`, `retries_failed_again`; the finding's note names the window's path; 4. **D11**: key = `main thread` / `sub-agents` × `inside a change window` / `between edits or outside`; stat `in_change_window`; 5. **D1**: key = gap bucket × `before any change` / `after changes began`; 6. **D13**: stat `unknown_in_change_window_ms`; 7. **D14**: title "Tool calls the harness could not parse", note and advice without "model error" |
| Tests | D17: `edit → final` (positive); `edit → passing test → final` (negative); `edit → test starts → edit → final` (positive); `child edit → parent test → final` (negative); `edit → failed test → final` (positive, note "last test after it failed"); `edit → unknown gap → final` (NoData); build-only (positive); `edit → final → stop hook running a test command without error` (negative, `VerifiedBy == hook`); the same hook with an error (positive); no change op (`NotApplicable`). D24: review run then edit (positive); edit then review run (negative); review by a lane role with no later change (negative); Codex review mode followed by edits (positive); no review run (`NotApplicable`). D7: one window per recovery label, a user turn inside, a fix followed by a second failure. D11: inside and outside a window. D1: before and after the first change |
| Done | `go test ./internal/insights`; the catalogue test lists 20 detectors; every card has a positive and a negative fixture |
| Estimate | 1 day |

| id | class | signal (literal) | finding | key | evidence interval | advice (gist) |
|---|---|---|---|---|---|---|
| **D17** · Final changes had no later successful test | check | `Changes > 0 && !Verified`; NoData when `BlindAfterLastChangeMs > 0`; `NotApplicable` without a change op | one per session | the source (`Codex` / `Claude Code`): the positive rate differed fourfold between harnesses in the corpus; the note says which of `no test at all` \| `last test before the last change` \| `last test after it failed` applies | `[LastChangeAt, session end]`, op `LastChangeOp` | a post-change verification step in the instructions file, or a stop hook that runs the project's test command after the last edit |
| **D24** · A review did not cover the last changes | check | `ReviewedAt > 0 && ChangesAfterReview > 0`; `NotApplicable` without a review run | one per session | `changes after the review: 1` \| `2-5` \| `more` (a display convention, printed) | `[ReviewedAt, LastChangeAt]` | run the review skill again after post-review edits, or make the review the last step of the turn |

D17's stats: `edit_turns`, `edit_turns_unverified` (printed as "N of M turns with edits had no
successful test after their last edit"), `last_verdict_failed`, `verified_by_hook` (sessions
verified by a stop hook, so the card can say so).

### WP6 · The page — `cmd/todobem/web/insights.js`, `app_test.js`

| | |
|---|---|
| Steps | 1. the five `c.info` sites read `c.class`; a `check` chip next to the title, a rank number for Checks, the "Top findings" strip adds `top_checks`; 2. the group `verification` in `INSIGHT_GROUPS`, `INSIGHT_TONES` (the Testing yellow) and `INSIGHT_TEXT.groups`; 3. `INSIGHT_TEXT.cards` entries for D17 and D24 (title, signal, measured, happened with the denominator wording, todo), the D7 / D11 / D1 / D13 headline sentences reading the new stats, the D14 rename, the conditional wording of T1, M1, T3, T7 (advice applies "where a policy per stage exists"; a fresh thread "costs less only when the resumed context is mostly stale — the number shown is the uncached input"), the D4 sentence "the thread is blocked on the wait tool call in both harnesses, so the main thread never works meanwhile" |
| Tests | the sentence-length test covers every new string; a rendering test for a Check card (chip, rank, no exposure line) and for a card with `NotApplicable` sessions in the denominator |
| Done | `node --test cmd/todobem/app_test.js`; the browser pass in WP8 |
| Estimate | 0.5 day |

### WP7 · Documentation — `docs/ARCHITECTURE.md`, `CLAUDE.md`

| | |
|---|---|
| Steps | §2.2 (stop hooks: one op per hook); §4.1 (the type-check rule); §10.1 (delivery facts and the new fields); §10.2 (two rows, the changed keys and stats, a class column); §10.3 (the Check class and `NotApplicable` in ranking); §12 (the "new insight" bullet gains the class and the denominator); §13 (the decisions: verification Checks, session scope, build is not verification, stop hooks as verification, D20 behind `is_async`, D33 / D39 / D4b rejected on the corpus, the `pragmatic` review of this plan on 2026-09-16 as the design review the rules require). `CLAUDE.md`: the "new insight" sentence gains "its class and its denominator" — the design review above covers this edit |
| Done | the document describes the code as merged; no "was / now" language |
| Estimate | 0.5 day |

### WP8 · Validation — the §11 protocol and the browser pass

| | |
|---|---|
| Steps | 1. independent computation from the raw JSONL on four closed sessions per source chosen to include sub-agents, a review skill run, a session that ended on edits, a session with retry loops, a Codex session with compactions, a Claude Code session with stop hooks: last change op, successful tests after it, review turns, retry windows and what ran inside them, hook count and durations — compared with `cmd/dump` (`DeliveryFacts`) and with the findings; counts exact; the report stays local (gitignored); 2. the real page in a browser on this project's 30-day period and on one session: the new group, a Check card's rank and chip, the D7 and D11 headlines, an evidence row opening the timeline at `[A, B]`, Not measured without the `NotApplicable` sessions, hooks as separate ops in a turn's tail, a clean console |
| Done | every counted metric matches; discrepancies fixed in the package they belong to, with a fixture |
| Estimate | 1 day |

## 3. Decisions taken (the owner, 2026-09-16)

1. D17 / D24 live in a new group **"Verification loop"** (`GroupVerify`), not under "Failures
   and retries".
2. Check cards rank **by sessions affected**, then by count.
3. **WP1 as specified**: `tsc --noEmit` and the dispatcher verb `typecheck` become
   `test/typecheck`; bare `tsc` stays `build`.
4. **D17 is keyed by source** (`Codex` / `Claude Code`).
5. **`detect_delivery.go` is split** in WP3 into `detect_agents.go`, `detect_retries.go` and
   `detect_context.go`.
6. **The `security` stage** (§4.3) is its own change after batch 1, and the display name of
   `test` becomes **"Verification"** (UI text only: `LIFECYCLES.test.name` in `app.js`, the
   guide sentences that say "Testing / QA", and the Insights strings that name the stage; done
   in WP6's commit, key unchanged).

No decision is open; the batch starts with WP1 and WP2.

## 4. Later batches

### 4.1 Batch 2 · Paths on operations (a schema change; design review first; about 3 days)

- `Operation.Paths []string json:"paths,omitempty"`: the literal paths a call named, taken where
  the adapters already hold them structurally — Claude Code `Read` / `Edit` / `Write` /
  `MultiEdit` / `NotebookEdit` / `Glob` (the path argument at the `PathLifecycle` call in
  `tools.go`), Codex `apply_patch` (the file list `lane.go` collects for the same call). Not
  parsed out of `Detail` (a clipped display string in each adapter's format). Cascade per the
  schema rule: `model.go`, §3, `app.js` (the inspector lists the paths), `cmd/dump`,
  `cacheVersion` and `FactsVersion` bumps, fixtures in both `lane_test.go`.
- Shell reads with one plain operand (`cat`, `sed -n`, `head`, `tail`) are a second decision: a
  new inference surface (`classify.PathArgs`) with its own rows, tests and §11 run. Until then
  Codex read events are invisible to path cards and the cards say so per source.
- Facts: a per-session `Paths` table and `PathEvents []{Lane, Turn, Op, Path, Write, T}`,
  complete and compact.
- Cards, all count cards (reads take milliseconds; tool-result tokens are not recorded):
  **D28** same file read again in one turn with no write to it, no compaction and no user turn
  between (info, key = basename); **D32** files read again after a compaction (info);
  **D36** two threads edited the same file while their turns overlapped (check; the note says a
  shared working tree is not confirmed by the log).
- Smaller items with a consumer: **D43** the Claude adapter puts the tool name on the
  `llm_error` marker so D14 keys by tool and CLI version; **D42** chains of consecutive
  system-triggered root turns (info); **D9+** a complete verdict stream (every test / build /
  release op) replaces the 40 longest so D9 prints count, p50, p90 and failed share per shape,
  and D8 (rerun after a pass with no change between) becomes a stat on it.

### 4.2 Batch 3 · Telemetry the harnesses would have to record

Item 0 is the one this repository can do itself; the rest is a list to raise with the harness
teams, in the order the cards would consume it.

0. `is_async` on the Claude Code agent marker and wait op (the adapter reads it and drops it);
   then D20 / D35 ("the turn ended while a sub-agent it did not background was still working")
   becomes a Check with a true advice template.
1. Tool name on every rejected tool call (Codex).
2. An exit code and a duration on every Claude Code tool result.
3. A working-tree fingerprint at the start of every test / build op (turns "verdict changed with
   nothing recorded between" into a flakiness signal).
4. A result hash on polls and read-only queries.
5. The origin of a verification run beyond stop hooks (tool hooks, CI).
6. Skill and tool-schema version or content hash per session.
7. Per-call usage with timestamps on both harnesses.
8. A workspace id per lane (worktrees).

### 4.3 A `security` stage (its own change, about one day; design review first)

Literal signals exist and have no rule today: scanner heads `gosec`, `govulncheck`, `trivy`,
`grype`, `semgrep`, `bandit`, `gitleaks`, `trufflehog`, `snyk`, `osv-scanner`, `checkov`,
`tfsec`, `npm audit`, `pip-audit`, `cargo audit`; SBOM and policy tools `syft`, `opa`,
`conftest`; the built-in `security-review` skill, today deliberately excluded from the review
matcher. Corpus: none, so the value is for teams that run scanners; the rule is honest either way.

- `classify/lifecycle.go`: `LcSecurity` after `LcTest` in `WorkLifecycles` (the ring's order and
  the matcher precedence); `IsWorkLifecycle`, the overlay validation, `/api/rules` and `cmd/dump`
  follow automatically; a built-in skill matcher `(^|[-_/.$@ ])security[-_]?review([-_/.]|$)` →
  `security` next to `ReviewSkillPattern`, with a `userconfig_test.go` case that `security-review`
  is `security` and still not `review`.
- `classify.go`: rows for the scanner heads — phase `test`, kind `security-scan` — and a
  `LifecyclePins` entry `security-scan → security` (phase says what: a check; stage says why:
  security assurance — the split `pr review` already uses under `release`); `classify_test.go`.
- `app.js`: a `LIFECYCLES` entry (name "Security / governance", short "Security", a hue no fill
  uses), the abbreviation table, the guide row; the SDLC ring and the stage band read the table.
- `docs/ARCHITECTURE.md` §3.3, §4.3, §13; `RulesFingerprint` schema tag → 5.
- §11 on sessions containing any of the commands; none in the local corpus, so a synthetic
  fixture per source until a real session exists.
- Behaviour: turns and sub-agents inside a `security-review` run inherit the stage like review
  runs; a scanner inside a change window stays `security` (an op pin beats composition) — the
  one place this differs from a `go test` in the loop, and intended.

The other proposed stages are not added (Appendix A.2): discovery and learning have no literal
signal, integration is seconds of merges already visible in the git and hosting sub-rows.

---

## Appendix A · Assessment of the proposal

### A.1 The catalogue

Corpus: every closed session the local cache holds at the current cache version — 38 (27 Claude
Code, 11 Codex); counts only, never content. Small, one developer's work, and lopsided in ways
that decide cards (Appendix C); enough to reject a pattern whose absence is structural and to
defer one that is merely rare.

| Proposal item | Decision | Evidence |
|---|---|---|
| Generation walker (change → verified / reviewed / released) | build (WP4) | one deterministic walk feeds D17, D19, D24, D18, D44 |
| D17 final changes with no later successful test | build, Check (WP5) | 3 of 20 Claude sessions with changes, 6 of 11 Codex |
| D24 review not covering the last changes | build, Check (WP5) | 2 of 3 Codex sessions with a review run; no Claude session had one — cheap because it shares the walker, not because the corpus proves it common |
| D19 last verdict before the end failed | stat on D17 | 0 of 31 sessions with tests |
| D18 turn-level "edits without a test after" | stat on D17 (info) | 63 of 116 Claude edit-turns, 79 of 130 Codex: too common to rank |
| D22 / D23 blind retry / recovery that did not recover | stats on D7, not new cards, not keys | Codex: 138 windows, 16 blind, 122 with a recovery step, 59 of those failed again. Recovery is a property of a window, the key of a shape: shape × recovery would split one command over rows (27 → 41) |
| D21 verdict changed with nothing recorded between | defer | 1 flip in 38 sessions |
| D30 compaction inside a change window | key on D11 | 32 of 80 Codex compactions; 0 compactions in the Claude corpus |
| D20 / D35 final answer while a child was active / orphaned child | batch 3 behind `is_async` | bare overlap: 50 root turns; the rule as specified (a recorded link and a completed turn): 4 in Claude Code, 0 in Codex, all 4 async agents the user backgrounded on purpose; the model cannot tell them from abandoned ones |
| D1b question asked after changes began | key on D1 | 5 of 6 Claude questions, 6 of 9 Codex |
| D44 unknown / no-telemetry inside change windows | stat on D13 | 2 min of 8 h 43 (Claude), 6 min of 26 h (Codex) |
| D14 rename, T1 / M1 / T3 / T7 conditional wording | text only (WP6) | the log records the invalid call, not its cause |
| D4a / D4b root idle vs root working during a wait | reject | both harnesses block the thread on the wait tool call: 0 root tool calls inside any of the 161 root worker waits. (Not because the partition forbids it: `wait_worker` outranks `infra` and `code` in `classify.Priority`) |
| D33 worker wait continued with no active child | reject until seen | 0 ms in both corpora |
| D39 background op open at session close | reject | 0 cases; D12 lists every background op with its runtime |
| D10 / T5 poll-only turns, D27 search-miss streaks, D8 rerun after a pass | defer | 1 + 1 turns; 2 streaks; 3 of 180 reruns (13 of 18 in Claude but seconds each) |
| D28 / D32 / D36 path cards | batch 2 | 46 of 469 Claude reads were same-turn re-reads; 2 sessions had two lanes editing one path; needs `Paths` on the schema |
| D37 / D46 / D47 role and lifecycle policy | not planned | conformance to a declared process is a different product; without a policy the cards sit permanently under Not measured. If a team asks: one section in `rules.json`, hashed into `RulesFingerprint`, D37 first (needs only `AgentFacts.Kind`) |
| D43 llm_error by tool and version, D42 system-turn chains, D9+ shape profile | batch 2 | need an adapter field, a loop, a complete stream |
| Hooks as a verdict source (D41, `hook_verification`) | adapter change (WP2), no card | 27 stop hooks in 12 Claude sessions, all under a second, none failed |
| Extended SDLC | `security` only (§4.3) | Appendix A.2 |
| Trends, cross-harness split | not in this plan | a separate feature (brief §5.6) |
| Goal drift, task ambiguity, coverage, true flakiness, tool-result bloat … | reject | each rests on something the log does not record; the proposal's §10 agrees |
| A `Finding` redesign (`Capability`, `Limitations`, `AdviceTarget`) | not taken | `Result.Measurable` / `Reason` already express "could not be measured"; advice lives in `INSIGHT_TEXT`; a capability matrix returns with the first path card |

### A.2 The extended SDLC

The test for a stage: a literal harness-level signal can pin it (a mode, an injected skill
name, a spawn role, an edited path, a command kind). JSON keys are never renamed (cache, API,
overlay, page); display names may change.

| Proposed | Today | Decision |
|---|---|---|
| Discovery | none | no: the inferred version ("reads before the first edit") was rejected on 2026-09-15 — it measured turn boundaries, not comprehension; the literal version (a `brainstorm` skill, an `Explore` role) is expressible through the overlay onto `plan` |
| Planning, Implementation, Release, Operations | `plan`, `implement`, `release`, `operate` | exist |
| Verification | `test` | exists; linters and type checkers already live there, so rename the display name to "Verification", keep the key |
| Review / Integration | `review`; merges are `release` (`gh pr merge`, 15 in the corpus) or `implement` (`git merge`, `git rebase`, 5) | no new stage: merges are seconds-long slivers, and one key for a skill-run stage plus command pins mixes two kinds of signal |
| Security / Governance | none; scanners land in `unknown` | add (§4.3) |
| Learning | none | no: reading and searching are activities the breakdown lists; nothing literal marks learning |

## Appendix B · Facts checked in the code during planning

1. `Operation.Detail` is persisted in the model cache as a side table (`store.Load` restores
   it), so Extract sees it; it is still not a source for structure (A.1, paths).
2. `no_telemetry` is emitted only after an open or orphaned turn, and no closed session ends on
   one; the D17 blind rule reduces in practice to "an unknown command ran after the last edit".
   Corpus: 3 of 31 sessions had any blind time after the last change, none of the 9 D17
   positives — the rule is kept as one cheap field, not a design question.
3. Hooks on disk: only `stop_hook_summary` carries a command and a duration per hook; tool hooks
   (PreToolUse, PostToolUse, UserPromptSubmit) leave no timed record — their output reaches the
   log only as attached context; Codex records no hooks. The machine's `settings.json`
   configures no hooks.
4. Claude Code exit codes exist for `Bash` only (`is_error` for the other tools); every D17
   number inherits this and the card's "how" text says so.
5. Claude's failed ops (79 in the corpus) are mostly edit failures with no retry identity, hence
   0 retry windows there; the D7 stats will show Codex numbers first.

## Appendix C · The assumptions most likely to be wrong

1. **The corpus generalises.** The Claude Code sessions had no compactions, no retry windows and
   no review runs, and their sub-agents were launched async. Structural rejections (D33, D39,
   D4b) survive that; frequency-based deferrals (D8, D10, D21, D27) are revisited against a
   second corpus before they are final. D24 rests on three sessions in one source.
2. **"A successful op in phase `test`" is a stable definition of verification.** It rests on a
   hand-maintained rule table that disagreed with itself on type checking until WP1, and on
   Claude Code exit codes that exist for `Bash` only.
3. **A recorded parent link means a supervised child.** The async case shows it can equally mean
   a deliberately backgrounded agent. Until `is_async` reaches the model, no card may read a
   child that outlived its parent turn as a defect.

Reviewed on 2026-09-16 by the `pragmatic` agent (three probes reproduced the corpus numbers and
measured the three claims the first draft had not tested: session scope costs nothing on this
corpus, the blind rule drops no positive, D20's linked count is four). The plan is the reviewed
version.
