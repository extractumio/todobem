package classify

import "testing"

// Every kind the rule table or an adapter can emit for a phase that has subgroups maps to one
// of the listed subgroups, so a new kind cannot fall into the breakdown unnamed.
func TestSubgroupCoversEveryKind(t *testing.T) {
	known := map[Phase]map[string]bool{}
	for _, row := range Subgroups() {
		if known[row.Phase] == nil {
			known[row.Phase] = map[string]bool{}
		}
		known[row.Phase][row.Subgroup] = true
	}
	check := func(phase Phase, kind string) {
		sub := Subgroup(phase, kind)
		if sub == "" || !known[phase][sub] {
			t.Errorf("%s / %q → subgroup %q (not listed)", phase, kind, sub)
		}
	}
	for _, r := range Rules {
		if known[r.Phase] != nil {
			check(r.Phase, r.Kind)
		}
	}
	// adapter kinds (internal/codex/lane.go, internal/claude/tools.go) and grouped / scripted forms
	for _, k := range []string{"read", "search", "list_files", "edit", "web_search", "mcp", "image", "script-read", "script→docker", "edit|fix", "deploy-script status"} {
		check(Code, k)
	}
	for _, k := range []string{"agent", "process", "sleep", "hook", "ci", "poll-loop", "tail-f", "wait"} {
		check(WaitWorker, k)
	}
	for _, k := range []string{"unknown", "python", "node", "run", "tool:Artifact", "exec-script-error"} {
		check(Unknown, k)
	}
	cases := map[[2]string]string{
		{"code", "read"}: "read", {"code", "image"}: "read", {"code", "search"}: "search", {"code", "list"}: "search",
		{"code", "edit"}: "edit", {"code", "sed -i"}: "edit", {"code", "format"}: "edit", {"code", "git rm"}: "edit",
		{"code", "git status"}: "vcs", {"code", "git"}: "vcs", {"code", "gh api"}: "hosting", {"code", "glab mr"}: "hosting",
		{"code", "http"}: "network", {"code", "web_search"}: "network", {"code", "mcp"}: "mcp", {"code", "docker"}: "shell",
		{"code", "probe"}: "shell", {"code", "script→docker"}: "shell", {"code", "script-write"}: "edit",
		{"wait_worker", "agent"}: "agents", {"wait_worker", "hook"}: "hooks", {"wait_worker", "ci"}: "polling", {"wait_worker", "sleep"}: "polling",
		{"unknown", "python"}: "script", {"unknown", "tool:Artifact"}: "tool", {"unknown", "unknown"}: "command",
		{"test", "go test"}: "", {"release", "git push"}: "", {"llm", "reasoning"}: "", {"compaction", "compaction"}: "",
	}
	for k, want := range cases {
		if got := Subgroup(Phase(k[0]), k[1]); got != want {
			t.Errorf("Subgroup(%s, %q) = %q, want %q", k[0], k[1], got, want)
		}
	}
	if !IsChangeOp(Code, "edit|fix") || IsChangeOp(Code, "read") || IsChangeOp(Test, "edit") || BaseKind("go test|rerun") != "go test" {
		t.Error("IsChangeOp / BaseKind")
	}
}
