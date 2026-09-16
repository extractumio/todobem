package classify

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// restore snapshots the mutable rule state so a test's overlay does not leak into others.
func restore(t *testing.T) {
	t.Helper()
	rules := append([]Rule(nil), Rules...)
	pins := map[string]Lifecycle{}
	for k, v := range LifecyclePins {
		pins[k] = v
	}
	snap := func(set map[Lifecycle][]*regexp.Regexp) map[Lifecycle][]*regexp.Regexp {
		out := map[Lifecycle][]*regexp.Regexp{}
		for k, v := range set {
			out[k] = append([]*regexp.Regexp(nil), v...)
		}
		return out
	}
	skills, roles, paths := snap(lifecycleSkills), snap(lifecycleRoles), snap(lifecyclePaths)
	t.Cleanup(func() {
		Rules, LifecyclePins = rules, pins
		lifecycleSkills, lifecycleRoles, lifecyclePaths = skills, roles, paths
		compileRules()
	})
}

func TestReviewSkill(t *testing.T) {
	yes := []string{"code-review-cc", "cc-code-review", "codereview", "/codereview", "$code-review", "code_review", "simplify", "simplify-code"}
	no := []string{"", "mdex", "security-review", "design-review", "pr-review-responder", "reviewer", "coder"}
	for _, n := range yes {
		if !ReviewSkill(n) {
			t.Errorf("ReviewSkill(%q) = false, want true", n)
		}
	}
	for _, n := range no {
		if ReviewSkill(n) {
			t.Errorf("ReviewSkill(%q) = true, want false", n)
		}
	}
	// Only code review has a built-in matcher: roles and paths pin nothing until an overlay says so.
	if lc, ok := SkillLifecycle("code-review-cc"); !ok || lc != LcReview {
		t.Errorf("SkillLifecycle(code-review-cc) = %q,%v", lc, ok)
	}
	if _, ok := RoleLifecycle("pragmatic"); ok {
		t.Error("RoleLifecycle matched without an overlay")
	}
	if _, ok := PathLifecycle([]string{"docs/DESIGN.md"}); ok {
		t.Error("PathLifecycle matched without an overlay")
	}
}

func TestLifecyclePins(t *testing.T) {
	cases := []struct {
		cmd  string
		want Lifecycle
	}{
		{"gh pr review 12 --approve", LcReview},
		{"gh pr comment 12 --body ok", LcReview},
		{"glab mr note 58 -m ok", LcReview},
		{"glab mr approve 58", LcReview},
		{"gh pr create --fill", ""}, // release stays the phase default
		{"journalctl -u app -n 50", LcOperate},
		{"docker logs web", LcOperate},
		{"docker compose logs -f api", LcOperate},
		{"kubectl logs pod/x", LcOperate},
		{"systemctl restart app", LcOperate},
		{"launchctl list", LcOperate},
		{"log show --last 5m", LcOperate},
		{"docker compose up -d", ""}, // dev loop: no pin, implementation by default
		{"make run", ""},
		{"go test ./...", ""},
	}
	for _, c := range cases {
		if got := Command(c.cmd, "").Lifecycle; got != c.want {
			t.Errorf("%q lifecycle = %q, want %q", c.cmd, got, c.want)
		}
	}
	if PhaseLifecycle(Code) != LcImplement || PhaseLifecycle(Build) != LcImplement || PhaseLifecycle(Infra) != LcImplement || PhaseLifecycle(Test) != LcTest || PhaseLifecycle(Release) != LcRelease || PhaseLifecycle(WaitUser) != "wait_user" || PhaseLifecycle(LLM) != "llm" {
		t.Error("PhaseLifecycle defaults changed")
	}
	if !IsWorkLifecycle(LcOperate) || IsWorkLifecycle("llm") || IsWorkLifecycle("") {
		t.Error("IsWorkLifecycle")
	}
}

func TestRulesFingerprintStable(t *testing.T) {
	a, b := RulesFingerprint(), RulesFingerprint()
	if a != b || len(a) != 16 {
		t.Errorf("fingerprint not stable: %q %q", a, b)
	}
	restore(t)
	if err := ApplyUserConfig(UserConfig{Lifecycle: LifecycleMatcherConfig{Roles: map[Lifecycle][]string{LcReview: {"^pragmatic$"}}}}, "test"); err != nil {
		t.Fatal(err)
	}
	if RulesFingerprint() == a {
		t.Error("overlay matcher did not change the fingerprint")
	}
}

func TestUserConfigOverlay(t *testing.T) {
	restore(t)
	// A brand-new project command, an override of a built-in word rule, an operations command
	// pinned by lifecycle, and custom review skills / a review agent role / a design document path.
	cfg := UserConfig{
		Rules: []UserRule{
			{Rule: Rule{Match: "seg:^myci\\b", Phase: Test, Kind: "in-house ci"}},
			{Rule: Rule{Match: "mydeploy", Phase: Release, Kind: "deploy"}},
			{Rule: Rule{Match: "make", Phase: Test, Kind: "make (project override)"}}, // override built-in make→build
			{Rule: Rule{Match: "prodctl", Phase: Infra, Kind: "prodctl"}, Lifecycle: LcOperate},
		},
		Lifecycle: LifecycleMatcherConfig{
			Skills: map[Lifecycle][]string{LcReview: {"audit-.*", "my-review"}, LcPlan: {"^brainstorm$"}},
			Roles:  map[Lifecycle][]string{LcReview: {"^pragmatic$"}},
			Paths:  map[Lifecycle][]string{LcDesign: {`(^|/)docs/DESIGN\.md$`}, LcRequirements: {`(^|/)SPEC\.md$`}},
		},
	}
	if err := ApplyUserConfig(cfg, "test"); err != nil {
		t.Fatalf("ApplyUserConfig: %v", err)
	}
	check := func(cmd string, want Phase, kind string, lc Lifecycle) {
		t.Helper()
		got := Command(cmd, "")
		if got.Phase != want || got.Kind != kind || got.Lifecycle != lc {
			t.Errorf("%q -> %s/%s/%s, want %s/%s/%s", cmd, got.Phase, got.Kind, got.Lifecycle, want, kind, lc)
		}
	}
	check("myci run --all", Test, "in-house ci", "")
	check("mydeploy prod", Release, "deploy", "")
	check("make", Test, "make (project override)", "") // user rule wins over built-in make->build
	check("prodctl restart api", Infra, "prodctl", LcOperate)
	if !ReviewSkill("audit-security") || !ReviewSkill("my-review") {
		t.Error("user review skills not matched")
	}
	if !ReviewSkill("code-review-cc") {
		t.Error("built-in review skill lost after overlay")
	}
	if lc, ok := SkillLifecycle("brainstorm"); !ok || lc != LcPlan {
		t.Errorf("plan skill: %q %v", lc, ok)
	}
	if lc, ok := RoleLifecycle("pragmatic"); !ok || lc != LcReview {
		t.Errorf("review role: %q %v", lc, ok)
	}
	if _, ok := RoleLifecycle("explorer"); ok {
		t.Error("unlisted role matched")
	}
	if lc, ok := PathLifecycle([]string{"internal/x.go", "docs/DESIGN.md"}); !ok || lc != LcDesign {
		t.Errorf("design path: %q %v", lc, ok)
	}
	if lc, ok := PathLifecycle([]string{"SPEC.md"}); !ok || lc != LcRequirements {
		t.Errorf("requirements path: %q %v", lc, ok)
	}
	m := Matchers()
	if len(m.Skills[LcReview]) != 3 || len(m.Roles[LcReview]) != 1 || len(m.Paths[LcDesign]) != 1 {
		t.Errorf("Matchers() = %+v", m)
	}
}

func TestUserConfigValidation(t *testing.T) {
	restore(t)
	bad := []UserConfig{
		{Rules: []UserRule{{Rule: Rule{Match: "", Phase: Test}}}},
		{Rules: []UserRule{{Rule: Rule{Match: "x", Phase: "nonsense"}}}},
		{Rules: []UserRule{{Rule: Rule{Match: "seg:[", Phase: Test}}}},
		{Rules: []UserRule{{Rule: Rule{Match: "x", Phase: Infra}, Lifecycle: "maintenance"}}},
		{Lifecycle: LifecycleMatcherConfig{Skills: map[Lifecycle][]string{LcReview: {"("}}}},
		{Lifecycle: LifecycleMatcherConfig{Roles: map[Lifecycle][]string{"llm": {"x"}}}},
		{Lifecycle: LifecycleMatcherConfig{Paths: map[Lifecycle][]string{LcDesign: {""}}}},
	}
	before, pins := len(Rules), len(LifecyclePins)
	for i, cfg := range bad {
		if err := ApplyUserConfig(cfg, "test"); err == nil {
			t.Errorf("case %d: expected error, got nil", i)
		}
	}
	if len(Rules) != before || len(LifecyclePins) != pins {
		t.Errorf("invalid config mutated the tables: %d != %d rules, %d != %d pins", len(Rules), before, len(LifecyclePins), pins)
	}
	if _, ok := RoleLifecycle("x"); ok {
		t.Error("invalid config mutated the role matchers")
	}
}

func TestLoadUserConfigFile(t *testing.T) {
	restore(t)
	dir := t.TempDir()
	p := filepath.Join(dir, "rules.json")
	os.WriteFile(p, []byte(`{"rules":[{"match":"projtool","phase":"build"}],"lifecycle":{"skills":{"review":["proj-review"]}}}`), 0644)
	if err := LoadUserConfig(p); err != nil {
		t.Fatalf("LoadUserConfig: %v", err)
	}
	if Command("projtool", "").Phase != Build {
		t.Error("file rule not applied")
	}
	if !ReviewSkill("proj-review") {
		t.Error("file review skill not applied")
	}
	// The pre-lifecycle key fails loudly instead of being ignored.
	old := filepath.Join(dir, "old.json")
	os.WriteFile(old, []byte(`{"review_skills":["x"]}`), 0644)
	if err := LoadUserConfig(old); err == nil {
		t.Error("unknown top-level key should be an error")
	}
	// A missing file is not an error.
	if err := LoadUserConfig(filepath.Join(dir, "nope.json")); err != nil {
		t.Errorf("missing file should be no error, got %v", err)
	}
}

func TestHookRuleRoundTrip(t *testing.T) {
	r := HookRule(Test, "go test")
	if r != "hook · test/go test" {
		t.Fatalf("rule %q", r)
	}
	if p, ok := HookPhase(r); !ok || p != Test {
		t.Fatalf("phase %q ok=%v", p, ok)
	}
	if _, ok := HookPhase("phase test"); ok {
		t.Fatal("a plain rule is not a hook")
	}
}
