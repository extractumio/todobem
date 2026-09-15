package classify

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Lifecycle is the SDLC stage an operation or segment served: the second, orthogonal partition
// of a lane's time next to Phase. Phase says WHAT a tool call is (a test run, an edit, a push);
// Lifecycle says WHICH STAGE of the software lifecycle it belonged to (implementation, code
// review, release …). Both partition the same segments, so each sums to the lane's elapsed time.
//
// Every assignment is a literal, harness-level signal or the order of such signals inside one
// turn — never prose in a message, never a duration:
//   - lane level: the role a sub-agent was spawned with (RoleLifecycle, user overlay);
//   - turn level: the harness's collaboration mode (plan), Codex review mode; the whole turn
//     takes that stage. A skill the harness actually injected (SkillLifecycle) pins a run: from
//     the moment it was invoked to the turn's end (Codex injects at the turn start, so the run is
//     the whole turn; Claude Code's Skill tool is a mid-turn call);
//   - op level: the winning command rule's Lifecycle pin, an edited path (PathLifecycle, user
//     overlay); else the turn's composition (model.assignLifecycle: reads before the plan was
//     submitted are planning, every tool call between the turn's first and last change op
//     (ChangeKinds) is implementation); else the phase's default (PhaseLifecycle).
//
// Requirements and design have no built-in detector: nothing in a Codex rollout marks them.
// They exist so a user overlay can pin them from skill names, agent roles or document paths.
type Lifecycle string

const (
	LcPlan         Lifecycle = "plan"
	LcRequirements Lifecycle = "requirements"
	LcDesign       Lifecycle = "design"
	LcImplement    Lifecycle = "implement"
	LcReview       Lifecycle = "review"
	LcTest         Lifecycle = "test"
	LcRelease      Lifecycle = "release"
	LcOperate      Lifecycle = "operate"
)

// WorkLifecycles are the eight SDLC stages in lifecycle order — the only values a rule, skill,
// role or path may pin. Non-work time keeps its phase name as its lifecycle: llm (model output
// no tool call followed), wait_user, wait_worker, idle, compaction, no_telemetry, unknown.
var WorkLifecycles = []Lifecycle{LcPlan, LcRequirements, LcDesign, LcImplement, LcReview, LcTest, LcRelease, LcOperate}

// IsWorkLifecycle reports whether lc is one of the eight SDLC stages.
func IsWorkLifecycle(lc Lifecycle) bool {
	for _, w := range WorkLifecycles {
		if w == lc {
			return true
		}
	}
	return false
}

// PhaseLifecycle is the stage an activity serves when no literal signal pins another one:
// coding, building and environment work serve implementation; tests serve testing; pushes,
// PRs and deploys serve release. Everything else passes through under its own name.
func PhaseLifecycle(p Phase) Lifecycle {
	switch p {
	case Code, Build, Infra:
		return LcImplement
	case Test:
		return LcTest
	case Release:
		return LcRelease
	}
	return Lifecycle(p)
}

// ChangeKinds are the code-phase kinds that change files in the working tree — edits, patches,
// written files, in-place sed, formatters, file-system moves and copies: the literal evidence
// that a turn implemented something. Between a turn's first and last change op every other tool
// call is the implementation loop (model.assignLifecycle: the change window). Branch operations
// (checkout, merge, stash …) navigate the tree rather than change its content and stay out. The
// same set is the "Editing files" subgroup of the breakdown (Subgroup).
var ChangeKinds = map[string]bool{
	"edit": true, "sed -i": true, "write-file": true, "script-write": true, "format": true,
	"mkdir": true, "cp": true, "mv": true, "touch": true, "ln": true,
	"git rm": true, "git mv": true, "git apply": true, "git cherry-pick": true,
}

// BaseKind strips the "|role" suffix a grouped op carries (model.withRole: "go test|rerun").
func BaseKind(kind string) string {
	if i := strings.IndexByte(kind, '|'); i >= 0 {
		return kind[:i]
	}
	return kind
}

// IsChangeOp reports whether an op of this phase and kind changed a file (ChangeKinds).
func IsChangeOp(phase Phase, kind string) bool {
	return phase == Code && ChangeKinds[BaseKind(kind)]
}

// LifecyclePins are the command kinds whose stage is not the phase default, keyed by Rule.Kind
// (the kind must be unique to the commands it pins). Review verbs on a PR/MR are review work
// even though the phase stays release. Log reading, service control and system diagnostics are
// operations candidates: model.Derive demotes them to implementation before the lane's first
// release op (an order rule, like the change window — see docs/SCHEMA.md). An overlay rule with a
// "lifecycle" value adds its kind here.
var LifecyclePins = map[string]Lifecycle{
	"pr review": LcReview, "pr comment": LcReview, "mr approve": LcReview, "mr note": LcReview,
	"journalctl": LcOperate, "systemctl": LcOperate, "launchctl": LcOperate, "diagnostics": LcOperate,
	"docker logs": LcOperate, "kubectl logs": LcOperate, "kubectl describe": LcOperate,
}

// ReviewSkillPattern matches the NAME of a skill whose run is a code review or code-cleanup
// pass (code-review-cc, cc-code-review, /codereview, $code-review, simplify, simplify-code).
// It is applied ONLY to the skill name in the harness's skills.selected_skill_instructions
// injection — the record of an ACTUAL invocation — never to prose in a user or model message,
// so the word "codereview" in a chat message can never trigger it. Bare "review" is excluded so
// unrelated skills (security-review, design-review, pr-review-responder) do not match. Served at
// /api/rules so this matcher is inspectable alongside the command table.
var ReviewSkillPattern = regexp.MustCompile(`(?i)(^|[-_/.$@ ])(cc-)?(code[-_]?review|codereview|simplify)([-_/.]|$)`)

// Live matcher sets, keyed by the stage they pin. Built-ins are set here; a user overlay
// (LoadUserConfig) appends. Set once at startup, read-only while serving.
var (
	lifecycleSkills = map[Lifecycle][]*regexp.Regexp{LcReview: {ReviewSkillPattern}} // selected skill name
	lifecycleRoles  = map[Lifecycle][]*regexp.Regexp{}                               // sub-agent role (agent_type)
	lifecyclePaths  = map[Lifecycle][]*regexp.Regexp{}                               // edited file path
)

func matchLifecycle(set map[Lifecycle][]*regexp.Regexp, s string) (Lifecycle, bool) {
	if s == "" {
		return "", false
	}
	for _, lc := range WorkLifecycles { // fixed order: the first stage whose matcher hits wins
		for _, re := range set[lc] {
			if re.MatchString(s) {
				return lc, true
			}
		}
	}
	return "", false
}

// SkillLifecycle is the stage a skill invocation pins on its turn (built-in: code review).
func SkillLifecycle(name string) (Lifecycle, bool) { return matchLifecycle(lifecycleSkills, name) }

// ReviewSkill reports whether a selected skill name denotes a code-review / cleanup run.
func ReviewSkill(name string) bool {
	lc, ok := SkillLifecycle(name)
	return ok && lc == LcReview
}

// RoleLifecycle is the stage a sub-agent's spawn role pins on its whole lane (overlay only).
func RoleLifecycle(role string) (Lifecycle, bool) { return matchLifecycle(lifecycleRoles, role) }

// PathLifecycle is the stage an edit pins from the paths it touched (overlay only): the first
// path, in the order given, that matches any pattern decides.
func PathLifecycle(paths []string) (Lifecycle, bool) {
	for _, p := range paths {
		if lc, ok := matchLifecycle(lifecyclePaths, p); ok {
			return lc, true
		}
	}
	return "", false
}

// LifecycleMatchers is the serialisable view of the live matcher sets for /api/rules.
type LifecycleMatchers struct {
	Skills map[Lifecycle][]string `json:"skills"`
	Roles  map[Lifecycle][]string `json:"roles"`
	Paths  map[Lifecycle][]string `json:"paths"`
}

func matcherSources(set map[Lifecycle][]*regexp.Regexp) map[Lifecycle][]string {
	out := map[Lifecycle][]string{}
	for lc, res := range set {
		for _, re := range res {
			out[lc] = append(out[lc], re.String())
		}
	}
	return out
}

// Matchers returns the source form of every active skill / role / path matcher (built-in first,
// then user-added) so the guide can show them the way it shows the Rules table.
func Matchers() LifecycleMatchers {
	return LifecycleMatchers{Skills: matcherSources(lifecycleSkills), Roles: matcherSources(lifecycleRoles), Paths: matcherSources(lifecyclePaths)}
}

// ReviewSkillMatchers returns the active review-skill patterns (kept for the API's flat list).
func ReviewSkillMatchers() []string { return matcherSources(lifecycleSkills)[LcReview] }

// RulesFingerprint hashes the effective classifier — the full rule table (built-in + user
// overlay, lifecycle pins included) and every lifecycle matcher — so a session cache keyed by it
// is invalidated whenever classification could change. The leading schema tag also invalidates it
// across model changes (2: the lifecycle partition; 3: sub-agent turns inherit the parent turn's
// stage and model output takes the nearest tool call's stage; 4: skill runs, the change window,
// model-output ops carry their segment's stage, subgroups).
func RulesFingerprint() string {
	h := sha1.New()
	fmt.Fprint(h, "schema=4;")
	change := make([]string, 0, len(ChangeKinds))
	for k := range ChangeKinds {
		change = append(change, k)
	}
	sort.Strings(change)
	for _, k := range change {
		fmt.Fprintf(h, "change:%s\n", k)
	}
	for _, r := range Rules {
		fmt.Fprintf(h, "%s|%s|%s\n", r.Match, r.Phase, r.Kind)
	}
	pins := make([]string, 0, len(LifecyclePins))
	for kind, lc := range LifecyclePins {
		pins = append(pins, kind+"="+string(lc))
	}
	sort.Strings(pins)
	for _, p := range pins {
		fmt.Fprintf(h, "pin:%s\n", p)
	}
	sets := []struct {
		name string
		set  map[Lifecycle][]*regexp.Regexp
	}{{"skill", lifecycleSkills}, {"role", lifecycleRoles}, {"path", lifecyclePaths}}
	for _, m := range sets { // fixed order: the hash must be stable across runs
		name, src := m.name, matcherSources(m.set)
		keys := make([]string, 0, len(src))
		for lc := range src {
			keys = append(keys, string(lc))
		}
		sort.Strings(keys)
		for _, lc := range keys {
			for _, pat := range src[Lifecycle(lc)] {
				fmt.Fprintf(h, "%s:%s:%s\n", name, lc, pat)
			}
		}
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}
