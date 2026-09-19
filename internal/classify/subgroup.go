package classify

import "strings"

// Subgroup is the finer row a phase splits into in the breakdown: a deterministic function of
// (phase, kind) that never changes Phase (priority, retry groups, query kinds and the timeline
// colours stay). Only the phases that lump different work have subgroups: code (Development),
// build (compiling vs installing dependencies), wait_worker and unknown; every other phase
// answers "". model.Derive sets it on every op (Operation.Subgroup); the UI sums the exclusive
// partition per subgroup and lists the map in the guide from /api/rules (Subgroups).
func Subgroup(phase Phase, kind string) string {
	k := strings.TrimPrefix(BaseKind(kind), "script→") // a heredoc classified by its inner command
	switch phase {
	case Build:
		if DepsKinds[k] {
			return "deps"
		}
		return "compile"
	case Code:
		switch {
		case k == "read" || k == "image":
			return "read"
		case k == "search" || k == "list" || k == "list_files":
			return "search"
		case ChangeKinds[k]:
			return "edit"
		case strings.HasPrefix(k, "git") || k == "vcs-script" || k == "vcs-subcommand":
			return "vcs"
		case k == "gh" || k == "gh api" || k == "glab" || k == "glab mr" || k == "glab ci" || k == "glab api":
			return "hosting"
		case k == "http" || k == "dns" || k == "net" || k == "web_search":
			return "network"
		case k == "mcp":
			return "mcp"
		}
		return "shell"
	case WaitWorker:
		switch k {
		case "agent":
			return "agents"
		case "hook":
			return "hooks"
		}
		return "polling"
	case Unknown:
		switch {
		case k == "python" || k == "node" || k == "ruby" || k == "perl" || k == "npm run" || k == "run" || k == "swift run" || k == "go run" || k == "cargo run" || k == "bazel run" || k == "dotnet run" || k == "deno run" || k == "exec-script-error":
			return "script"
		case strings.HasPrefix(k, "tool:"):
			return "tool"
		}
		return "command"
	}
	return ""
}

// DepsKinds are the build-phase kinds that install or resolve dependencies rather than compile
// the project: the `deps` sub-row, so "build time" can be read as compile time alone. A package
// manager's install, add, lock, sync, fetch, vendor and update commands; a tool installed from
// a registry (`cargo install`, `go install pkg@version`); a Makefile's deps / vendor / bootstrap /
// setup target. Everything else in the build phase is `compile`.
var DepsKinds = map[string]bool{
	"npm install": true, "pnpm install": true, "yarn install": true, "bun install": true,
	"pip install": true, "uv": true, "poetry install": true, "pipenv install": true,
	"go mod": true, "go get": true, "go install tool": true,
	"cargo deps": true, "cargo install": true, "bundle install": true,
	"pod install": true, "carthage": true, "mint install": true, "swift package resolve": true,
	"make deps": true,
}

// SubgroupRow is one line of the guide's subgroup table.
type SubgroupRow struct {
	Phase    Phase  `json:"phase"`
	Subgroup string `json:"subgroup"`
	Kinds    string `json:"kinds"`
}

// Subgroups lists every subgroup with the kinds it collects, in breakdown order, for /api/rules.
// The kinds column is documentation for the guide; Subgroup above is the rule.
func Subgroups() []SubgroupRow {
	return []SubgroupRow{
		{Code, "read", "read, image"},
		{Code, "search", "search, list, list_files"},
		{Code, "edit", "edit, sed -i, write-file, script-write, format, mkdir, cp, mv, touch, ln, git rm, git mv, git apply, git cherry-pick"},
		{Code, "vcs", "git status/diff/log/show/blame/commit/add/… (local), vcs-script, vcs-subcommand"},
		{Code, "hosting", "gh, gh api, glab, glab mr, glab ci, glab api (queries)"},
		{Code, "network", "http, dns, net, web_search"},
		{Code, "mcp", "mcp"},
		{Code, "shell", "inspect, shell, probe, process, system, version, script-read, sqlite, sql, go env/list/doc, npm, cargo, rm, docker/kubectl queries, devicectl, simulator, xcode, codesign, everything else in the code phase"},
		{Build, "compile", "cargo build/check, go build/generate/install, swift build, xcodebuild, cc, link, configure, rustc, tsc, vite, esbuild, webpack, next build, npm/pnpm/yarn/bun build, make and its build targets, cmake, ninja, meson, bazel, gradle, mvn, docker build, everything else in the build phase"},
		{Build, "deps", "npm/pnpm/yarn/bun install, pip install, uv, poetry, pipenv, go mod/get, go install pkg@version, cargo add/fetch/update/vendor/install, bundle, pod, carthage, mint, swift package resolve/update, make deps/vendor"},
		{WaitWorker, "agents", "agent"},
		{WaitWorker, "polling", "sleep, poll-loop, tail-f, process, ci, wait"},
		{WaitWorker, "hooks", "hook"},
		{Unknown, "script", "python, node, ruby, perl, npm run, run, swift run, go run, cargo run, bazel run, dotnet run, deno run, exec-script-error"},
		{Unknown, "tool", "tool:<name> (a Claude Code tool without a mapping)"},
		{Unknown, "command", "unknown (no rule matched)"},
	}
}
