# Lifecycle (SDLC stage) partition — design review record, 2026-09-14

Independent review (Claude Code `pragmatic` agent) of the proposal to make code review "a phase
like Coding", add planning, and shape the breakdown after the SDLC, with the user's constraint
that maintenance cannot precede coding inside one session. The user's decision on the taxonomy
was fixed; the review stress-tested *how*.

## Evidence base (measured, 2,175 rollouts: 1,929 local + 246 remote, CLI 0.134–0.154)

| signal | harness-level? | count |
|---|---|---|
| `turn_context.collaboration_mode.mode` (enum `plan` \| `default`, verified in openai/codex `config_types.rs`) | yes | 12,536 turn contexts, **0** in plan mode |
| `update_plan` function calls | yes | 1,075 in 225 files |
| `skills.selected_skill_instructions` | yes | `code-review-cc` 358, `simplify-code` 1, non-SDLC skills 7 |
| sub-agent `agent_role` (`session_meta`) | yes | `pragmatic` 905 (user-defined reviewer), `explorer` 330, `default` 322, `worker` 95 |
| sub-agent `task_name` words | no (model prose) | review 594, verify 374, design 63 — rejected as a source |
| `gh pr review\|comment`, `glab mr note\|approve` (real exec calls) | literal command | 4 |
| `journalctl` / `systemctl` / `docker logs` / `launchctl` (real exec calls) | literal command | 5 / 21 / 90 / 121; `tail … .log` 5,899 (dev loop) |
| Codex `/review` mode, `<proposed_plan>` | — | 0 |

Nothing in the format marks requirements or design.

## Verdict: approve with conditions — all taken, one narrowed

1. **Two dimensions, not a replacement.** Replacing `phase` breaks `Priority`, retry groups keyed
   on test/build/release, role kinds, the validated rule table and every JS test keyed on
   `data-phase`. One `lc` field per segment/op, assigned after overlaps are resolved, keeps both.
   The UI shows flat lifecycle rows with an inline model/tools split (the honesty mechanism) —
   no nested rows in v1.
2. **Ordering rule as a same-lane demotion guard, not a detector.** "First release on any lane"
   would be cross-lane inference; `docker up` after a mid-session push (577 pushes in the corpus,
   the dev loop continues after most) must not become operations. Taken as: an operations
   candidate before *this lane's* first release op is implementation; shown in the inspector.
   *Narrowed, not dropped*: the review recommended overlay-only operations detection; the
   implementation keeps a minimal built-in list of unmistakably operational kinds (`journalctl`,
   `systemctl`, `launchctl`, diagnostics, `docker logs`, `kubectl logs|describe`) under the guard,
   never dev-server kinds (`make run`, `docker compose up`). Validated on real sessions: the only
   candidates seen (`log show` during a skill build) were demoted correctly.
3. **No built-in design / requirements detectors.** A bare `design` skill matcher would collide
   with `design-review` (the exact collision the review matcher avoids); `docs/DESIGN.md` edits
   in this very repo happen during implementation. Taxonomy slots exist; the guide says
   "no built-in detector"; the overlay pins them.
4. **Unattributed `llm` stays.** Attributing trailing model output to the previous op is a guess
   in the other direction. Named "Model output — no tool call followed". Measured share on
   four real sessions: 0.0–3.4 % of elapsed.
5. **`update_plan` creation = "no step completed"**, not "all pending" (Codex often starts step 1
   in progress). The anchor op is `phase=llm, kind=plan`, so the activity partition and the stage
   view are byte-identical; only the lifecycle gets an anchor.
6. **Drop `review_ms`, keep `reviews`.** Two review totals that can differ are worse than one
   schema bump (`RulesFingerprint` schema=2 invalidates caches). Op-level review pins narrowed to
   review verbs: `gh pr view|diff|checks` is mostly an agent checking its own PR.
7. **Rule 7 amended** in `CLAUDE.md`: the activity partition keeps LLM separate; the lifecycle
   partition is a second exclusive partition attributing model output by literal turn/op signal,
   always shown with its activity split, never compared per key across dimensions.

Also taken: segments are cut at turn boundaries before assignment (`emit` merges adjacent
same-phase segments across back-to-back turns); ops carry `lc` so the list filters without
re-deriving Go logic in JS; one `lifecycle` overlay block with uniform `skills` / `roles` /
`paths` maps, and a `lifecycle` pin on overlay rules instead of a fifth key; `DisallowUnknownFields`
makes the old `review_skills` key fail loudly.

Well-designed, kept: turn signal from the harness skill injection; lane signal from `agent_role`;
model-chosen `task_name` rejected; no cross-lane inference for the root's `wait_worker`;
pass-through of waits / idle / compaction / telemetry gaps / unknown; fingerprint covers every
new table.

## Validation run (before UI work, as the review asked)

`cmd/dump` on the review session and three local sessions (plan creation; a mid-session push
followed by dev-loop `log show`; PR/MR work): both partitions balance on every lane
(`lifecycle partition mismatch` never printed); review session → `review 1h43m29s` (10.2 % of
elapsed, 93 % of in-turn time); plan anchors → `plan 16s`; operations candidates before the
first release → `implement` with the guard's rule text.
