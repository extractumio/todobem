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
	res := append([]*regexp.Regexp(nil), reviewSkillREs...)
	t.Cleanup(func() { Rules = rules; reviewSkillREs = res; compileRules() })
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
}

func TestUserConfigOverlay(t *testing.T) {
	restore(t)
	// A brand-new project command, an override of a built-in word rule, and a custom review skill.
	cfg := UserConfig{
		Rules: []Rule{
			{Match: "seg:^myci\\b", Phase: Test, Kind: "in-house ci"},
			{Match: "mydeploy", Phase: Release, Kind: "deploy"},
			{Match: "make", Phase: Test, Kind: "make (project override)"}, // override built-in make→build
		},
		ReviewSkills: []string{"audit-.*", "my-review"},
	}
	if err := ApplyUserConfig(cfg, "test"); err != nil {
		t.Fatalf("ApplyUserConfig: %v", err)
	}
	check := func(cmd string, want Phase, kind string) {
		t.Helper()
		got := Command(cmd, "")
		if got.Phase != want || got.Kind != kind {
			t.Errorf("%q -> %s/%s, want %s/%s", cmd, got.Phase, got.Kind, want, kind)
		}
	}
	check("myci run --all", Test, "in-house ci")
	check("mydeploy prod", Release, "deploy")
	check("make", Test, "make (project override)") // user rule wins over built-in make->build
	if !ReviewSkill("audit-security") || !ReviewSkill("my-review") {
		t.Error("user review skills not matched")
	}
	if !ReviewSkill("code-review-cc") {
		t.Error("built-in review skill lost after overlay")
	}
}

func TestUserConfigValidation(t *testing.T) {
	restore(t)
	bad := []UserConfig{
		{Rules: []Rule{{Match: "", Phase: Test}}},
		{Rules: []Rule{{Match: "x", Phase: "nonsense"}}},
		{Rules: []Rule{{Match: "seg:[", Phase: Test}}},
		{ReviewSkills: []string{"("}},
	}
	before := len(Rules)
	for i, cfg := range bad {
		if err := ApplyUserConfig(cfg, "test"); err == nil {
			t.Errorf("case %d: expected error, got nil", i)
		}
	}
	if len(Rules) != before {
		t.Errorf("invalid config mutated Rules: %d != %d", len(Rules), before)
	}
}

func TestLoadUserConfigFile(t *testing.T) {
	restore(t)
	dir := t.TempDir()
	p := filepath.Join(dir, "rules.json")
	os.WriteFile(p, []byte(`{"rules":[{"match":"projtool","phase":"build"}],"review_skills":["proj-review"]}`), 0644)
	if err := LoadUserConfig(p); err != nil {
		t.Fatalf("LoadUserConfig: %v", err)
	}
	if Command("projtool", "").Phase != Build {
		t.Error("file rule not applied")
	}
	if !ReviewSkill("proj-review") {
		t.Error("file review skill not applied")
	}
	// A missing file is not an error.
	if err := LoadUserConfig(filepath.Join(dir, "nope.json")); err != nil {
		t.Errorf("missing file should be no error, got %v", err)
	}
}
