package classify

import (
	"strings"
	"testing"
)

func TestCommand(t *testing.T) {
	cases := []struct {
		cmd  string
		want Phase
		kind string
	}{
		{"pwd", Code, "shell"},
		{"git status --short", Code, "git status"},
		{"git add -A && git commit -m 'x' && git push -u origin main > log 2>&1", Release, "git push"},
		{"APP_REMOTE_HOST=buildhost skills/run-test-on-remote/run-remote-tests.sh workflow > artifacts/x.log 2>&1", Test, "test-script"},
		{"APP_TEST_FORCE_LOCAL=1 apps/demo/tests/run-test-group.sh backend-integration", Test, "test-script"},
		{"gh run watch 34575989044 --interval 10 --exit-status > artifacts/ci.log 2>&1", WaitWorker, "ci"},
		{"gh pr checks 259 --watch --interval 15", WaitWorker, "ci"},
		{"iq_wait_started=$SECONDS\nwhile ! ssh -o BatchMode=yes buildhost 'test ! -d lease'; do\n  sleep 20\ndone", WaitWorker, "poll-loop"},
		{"APP_APPLE_TEAM_ID=X ./release-app macos-ship --skip-tests > a.log 2>&1", Release, "deploy-script"},
		{"skills/deploy-prod/deploy.sh --image-archive x.tar", Release, "deploy-script"},
		{"xcrun devicectl device install app --device 123 x.app", Release, "device install"},
		{"xcrun devicectl device info apps --device 123", Code, "devicectl"},
		{"xcrun devicectl device process launch --device 123 --terminate-existing --console app.x 2>&1 | rg --line-buffered 'shot'", Test, "device run"},
		{"xcodebuild -project X.xcodeproj -scheme X -destination 'platform=macOS' test", Test, "xcodebuild test"},
		{"xcodebuild -project X.xcodeproj -scheme X build", Build, "xcodebuild"},
		{"swift build -c release", Build, "swift build"},
		{"python3 - <<'PY'\nfrom pathlib import Path\np=Path('a.md')\ns=p.read_text()\np.write_text(s)\nPY", Code, "script-write"},
		{"python3 - <<'PY'\nimport json\nprint(json.load(open('x')))\nPY", Code, "script-read"},
		{"python3 - <<'PY'\nimport sqlite3\nc=sqlite3.connect('x')\nc.execute('insert into t values (1)')\nc.commit()\nPY", Unknown, "unknown"},
		{"APP_TEST_FORCE_LOCAL=1 python3 - <<'PY' > a.log 2>&1\nimport subprocess\nraise SystemExit(subprocess.call(['apps/demo/tests/run-test-group.sh','backend']))\nPY", Test, "script→test-script"},
		{"python3 - <<'PY'\nimport subprocess\nsubprocess.run(['docker','info','--format','{{.ServerVersion}}'],capture_output=True)\nPY", Code, "script→docker"},
		{"python3 - <<'PY'\nimport subprocess\nfiles=subprocess.check_output(['ssh','buildhost','rg --files x'],text=True)\nsubprocess.run(['scp','-q','buildhost:a','b'],check=True)\nPY", Infra, "script→ssh"},
		{"python3 - <<'PY'\nimport os\nos.system(\"swift test\")\nPY", Test, "script→swift test"},
		{"scripts/iq-preflight.sh", Test, "check-script"},
		{"scripts/iq-preflight.sh > artifacts/x/merge-preflight.log 2>&1", Test, "check-script"},
		{"python3 - <<'PY'\nx = 1 + 1\nprint(x)\nPY", Unknown, "unknown"},
		{"sed -n '1,200p' foo.swift", Code, "read"},
		{"sed -i '' 's/a/b/' foo.swift", Code, "sed -i"},
		{"cat > /tmp/x.json <<'EOF'\n{}\nEOF", Code, "write-file"},
		{"docker compose -f x.yml up -d", Infra, "docker compose"},
		{"docker ps -a", Code, "docker"},
		{"ssh buildhost 'sed -n \"1,260p\" /path/file'", Infra, "ssh"},
		{"sleep 10", WaitWorker, "sleep"},
		{"rg -n 'foo' src | head -50", Code, "search"},
		{"cd apps && npm test", Test, "npm test"},
		{"timeout 300 pytest -q tests/", Test, "pytest"},
		{"gofmt -w backend/x.go", Code, "format"},
		{"git -C /Users/x show --stat HEAD", Code, "git show"},
		{"APP_TEST_FORCE_LOCAL=1 git -c core.sshCommand='ssh -o ServerAliveInterval=30' push -u origin fix-x > a.log 2>&1", Release, "git push"},
		{"git --no-pager -c color.ui=false log -3", Code, "git log"},
		{"APP_SKIP_RESOLVE=1 ./create-worktree.sh fix-x", Code, "vcs-script"},
		{"./run.sh --rebuild --incremental --build-only", Build, "build-flag"},
		{"env -u APP_QA_FIXTURE APP_REMOTE_HOST=buildhost skills/run-test-on-remote/run-remote-tests.sh qa", Test, "test-script"},
		{"make run", Infra, "service"}, {"make", Build, "make"}, {"make test", Test, "make test"},
		{"go run ./.calm-validation -serve", Infra, "service"},
		{"git merge-base abc def", Code, "git"}, {"git check-ignore -v x", Code, "git"},
		{"/usr/bin/log show --predicate 'x' --last 5m", Infra, "diagnostics"},
		{"bash -n scripts/deploy.sh", Test, "syntax-check"},
		{"APP_XCB_SCHEME=X APP_XCB_DESTINATION='platform=macOS' apps/demo/scripts/xcodebuild-app.sh build --configuration Debug", Build, "build-flag"},
		{"skills/deploy-prod/repair-performance-graph.sh --image-archive x.tar", Release, "deploy-script"},
		{"go run ./.calm-validation > out.txt", Test, "check-script"},
		{"python3 -u - <<'PY'\nimport subprocess\ncmd=['xcrun','devicectl','device','process','launch','--device',dev,'app.bundle']\nsubprocess.run(cmd, check=True)\nPY", Test, "script→device run"},
		{"./run.sh --test", Test, "test-flag"},
		{"bash -o pipefail -c 'apps/demo/tests/run-test-group.sh app 2>&1 | tail -5'", Test, "test-script"},
		{"bash -o pipefail -c 'git push origin main'", Release, "git push"},
		{"some-unknown-binary --flag", Unknown, "unknown"},
		{"rm -rf build/", Infra, "cleanup"},
		{"for f in a b; do echo $f; done", Code, "shell"},
		// GitLab (glab): reads are code (like gh api); MR lifecycle writes are release.
		{"glab api 'projects/2151/pipelines/496613/jobs?per_page=100'", Code, "glab api"},
		{"glab api projects/2151/merge_requests/54 --method PUT --input /tmp/x.json", Code, "glab api"},
		{"glab api projects/2151/merge_requests/54/discussions/abc --method PUT -F resolved=true", Code, "glab api"},
		{"glab mr list --output json", Code, "glab mr"},
		{"glab mr view 58", Code, "glab mr"},
		{"glab mr create --fill --yes", Release, "glab mr"},
		{"glab mr merge 58 --squash", Release, "glab mr"},
		{"glab mr note 58 -m 'looks good'", Release, "mr note"},
		{"glab mr approve 58", Release, "mr approve"},
		{"journalctl -u app -n 50", Infra, "journalctl"},
		{"xcrun swiftc -parse-as-library -typecheck -warnings-as-errors main.swift", Build, "swiftc"},
		{"swiftc -O -o out main.swift", Build, "swiftc"},
		// loop headers carry no command: the variable must not be read as a head word
		{"for log in artifacts/a.log artifacts/b.log; do tail -n 5 \"$log\"; done", Code, "read"},
		{"for f in *.go; do gofmt -l $f; done", Code, "format"},
		{"docker logs web --tail 100", Code, "docker logs"},
		{"docker compose logs -f api", Code, "docker logs"},
		{"kubectl logs pod/x", Code, "kubectl logs"},
		{"kubectl describe pod x", Code, "kubectl describe"},
		{"glab ci status", Code, "glab ci"},
		{"glab alias list", Code, "glab"},
		{"glab api projects/2151/jobs/2777637/trace | rg -n 'FAIL' | tail -130", Code, "glab api"},
		// GitLab write piped to jq: the write and the jq are both code, so code wins cleanly.
		{"glab api projects/2151/mr/54 --method PUT --input x.json > out.json && jq . out.json", Code, "glab api"},
		// Local dispatcher scripts: first positional subcommand (ci- prefix stripped).
		{"NODES=22 ./dev ci-build > /tmp/build.log 2>&1", Build, "build-flag"},
		{"NODES=22 ./dev ci-e2e > /tmp/e2e.log 2>&1", Test, "test-flag"},
		{"./dev preflight > /tmp/pf.log 2>&1", Test, "test-flag"},
		{"./dev benchmark run --node 22 --agent x.tgz", Test, "test-flag"},
		{"./dev ci-push origin main", Release, "release-flag"},
		{"./dev worktree create mysql-fixture-startup origin/main", Code, "vcs-subcommand"},
		{"./dev build", Build, "build-flag"},
		{"./dev typecheck", Test, "test-flag"}, // a type check verifies; it sits with npm run typecheck, not with build
		// tsc emits JavaScript (build); with --noEmit it only checks types (test outranks the word rule)
		{"tsc", Build, "tsc"}, {"npx tsc -p tsconfig.json", Build, "tsc"},
		{"tsc --noEmit", Test, "typecheck"}, {"npx tsc --noEmit -p tsconfig.json", Test, "typecheck"},
		{"cd web && tsc -p . --noEmit --pretty false 2>&1 | head -50", Test, "typecheck"},
		// First positional that is a read stays code, not work; unknown-first-word stays unknown.
		{"./dev status", Unknown, "unknown"}, // read-only first word: not forced to a work phase
		{"./dev integration pair sqlite/nocodb-sqli", Unknown, "unknown"},
		// A system binary (not a local script) is NOT reclassified by a verb-like argument.
		{"someprog test", Unknown, "unknown"},
		// Lint / toolchain / packaging across stacks.
		{"shellcheck -x dev docker/*.sh", Test, "lint"},
		{"golangci-lint run ./...", Test, "lint"},
		{"wasm-pack build --release", Build, "wasm-pack"},
		{"rustup toolchain list", Infra, "rustup"},
		{"npm --prefix plugin ci --ignore-scripts --no-audit --no-fund", Build, "npm install"},
		{"npm --prefix plugin run build", Build, "npm build"},
		{"npm pack --offline --pack-destination /tmp x.tgz", Build, "npm pack"},
		{"on-cli list --details", Infra, "remote vm"},
		{"onevm show 9313482 --json", Infra, "remote vm"},
	}
	for _, c := range cases {
		got := Command(c.cmd, "")
		if got.Phase != c.want || got.Kind != c.kind {
			t.Errorf("%q\n  got  %s/%s (rule %s)\n  want %s/%s", c.cmd, got.Phase, got.Kind, got.Rule, c.want, c.kind)
		}
	}
	if Command("cat x", "read").Phase != Code {
		t.Error("codex parsed_cmd read should be explore")
	}
	id1 := Identity("git push -u origin x > artifacts/a.log 2>&1")
	id2 := Identity("git push -u origin x > artifacts/a-retry.log 2>&1")
	id3 := Identity("git push -u origin x 2>&1 | tee a.log | tail -20")
	if id1 != id2 || id1 != "git push -u origin x" || id3 != id1 {
		t.Errorf("identity mismatch: %q vs %q vs %q", id1, id2, id3)
	}
	if Identity("a || b") != "a || b" || Identity("swift test | xcbeautify") != "swift test" {
		t.Errorf("identity pipes: %q %q", Identity("a || b"), Identity("swift test | xcbeautify"))
	}
	if !Command("APP_REMOTE_HOST=buildhost tests/run.sh", "").Remote {
		t.Error("remote flag")
	}
	if Command("cat > notes.md <<'EOF'\nrun it with ssh buildhost --remote later\nEOF", "").Remote {
		t.Error("remote flag from a heredoc body")
	}
	if !Command("ssh buildhost 'make test' <<'EOF'\nnothing\nEOF", "").Remote {
		t.Error("remote flag lost when a heredoc follows")
	}
}

func TestServicesAndNodeScripts(t *testing.T) {
	cases := []struct {
		cmd  string
		want Phase
		kind string
	}{
		{"npm start", Infra, "service"},
		{"npm run dev", Infra, "service"},
		{"npm run lint", Test, "lint"},
		{"npm run test:unit", Test, "npm test"},
		{"npm run build:web", Build, "npm build"},
		{"node_modules/.bin/oxlint docs/design", Test, "lint"},
		{"./node_modules/.bin/oxfmt tests/x.mjs", Code, "format"},
		{"node --import=/tmp/probe.mjs node_modules/vinext/dist/cli.js dev --host 127.0.0.1 --port 5173", Infra, "service"},
		{"node --input-type=module <<'JS'\nimport { chromium } from '/opt/x/playwright/index.mjs';\nconst b = await chromium.launch();\nJS", Test, "script-check"},
		{"node --input-type=module <<'JS'\nconst t = await (await fetch('http://127.0.0.1:9231/json/list')).json();\nconsole.log(t);\nJS", Code, "script-read"},
		{"python3 - <<'PY'\nfrom pathlib import Path\nfor p in Path('x').glob('*.js'):\n s=p.read_text()\n assert len(s.splitlines())<=500,p\nPY", Test, "script-check"},
	}
	for _, c := range cases {
		got := Command(c.cmd, "")
		if got.Phase != c.want || got.Kind != c.kind {
			t.Errorf("%q\n  got  %s/%s (rule %s)\n  want %s/%s", c.cmd, got.Phase, got.Kind, got.Rule, c.want, c.kind)
		}
	}
}

func TestWrittenThenExecuted(t *testing.T) {
	inline := `python3 -c "from pathlib import Path; Path('run.sh').write_text('''git push''')"; bash run.sh`
	if r := Command(inline, ""); r.Phase != Release {
		t.Errorf("inline write_text+bash: %s/%s rule=%s", r.Phase, r.Kind, r.Rule)
	}
	py := "python3 - <<'PY'\nfrom pathlib import Path\np=Path('artifacts/x/run-ui-when-free.sh')\np.write_text('''#!/usr/bin/env bash\nwhile ssh buildhost 'test -e lease'; do sleep 10; done\nAPP_REMOTE_HOST=buildhost skills/run-test-on-remote/run-remote-tests.sh qa-scenario x macos > a.log 2>&1\n''')\nPY\nbash artifacts/x/run-ui-when-free.sh"
	if r := Command(py, ""); r.Phase != Test || r.Kind != "script→test-script" || !r.Queued {
		t.Errorf("write_text+bash: %s/%s queued=%v rule=%s", r.Phase, r.Kind, r.Queued, r.Rule)
	}
	sh := "cat > /tmp/deploy-now.sh <<'EOF'\n#!/bin/bash\ngit push origin main\nEOF\nchmod +x /tmp/deploy-now.sh && /tmp/deploy-now.sh"
	if r := Command(sh, ""); r.Phase != Release {
		t.Errorf("cat+exec: %s/%s rule=%s", r.Phase, r.Kind, r.Rule)
	}
	// written but not executed stays a file write
	if r := Command("cat > /tmp/x.sh <<'EOF'\ngit push\nEOF", ""); r.Phase != Code {
		t.Errorf("write only: %s/%s", r.Phase, r.Kind)
	}
}

func TestIdentityPreservesShellArguments(t *testing.T) {
	cases := []struct{ cmd, want string }{
		{`npm test -- --testNamePattern='left > right'`, `npm test -- --testNamePattern='left > right'`},
		{`npm test -- --testNamePattern='left  right'`, `npm test -- --testNamePattern='left  right'`},
		{`go test -run 'tee | grep' > "first log" 2>&1 | tee "second log"`, `go test -run 'tee | grep'`},
		{`go test | tee log && git status`, `go test | tee log && git status`},
		{`go test 2 > log`, `go test 2`},
		{`go test < input`, `go test < input`},
		{"go test\nprintf done", "go test\nprintf done"},
		{`go test > "$(choose_log)"`, `go test > "$(choose_log)"`},
		{"python3 - <<'PY'\nassert 'a  b' != 'a b'\nPY\n", "python3 - <<'PY'\nassert 'a  b' != 'a b'\nPY\n"},
	}
	for _, c := range cases {
		if got := Identity(c.cmd); got != c.want {
			t.Errorf("Identity(%q) = %q; want %q", c.cmd, got, c.want)
		}
	}
	if Identity(`npm test -- --testNamePattern='left > right'`) == Identity(`npm test -- --testNamePattern='left > wrong'`) {
		t.Fatal("distinct quoted patterns became one identity")
	}
}

func TestHeredocDataIsNotExecuted(t *testing.T) {
	body := "import subprocess\nsubprocess.run(['git', 'push'])\nassert True\n"
	for _, head := range []string{"cat > example.py", "cat", "tee example.py"} {
		cmd := head + " <<'PY'\n" + body + "PY"
		if got := Command(cmd, ""); got.Phase != Code {
			t.Errorf("file data in %q classified as %s/%s", head, got.Phase, got.Kind)
		}
	}
	for _, head := range []string{"python3 example.py", "python3 -c 'print(1)'", "python3 -m example"} {
		if got := Command(head+" <<'PY'\n"+body+"PY", ""); got.Phase != Unknown {
			t.Errorf("stdin data in %q classified as %s/%s", head, got.Phase, got.Kind)
		}
	}
	if got := Command("python3 - <<'PY'\n"+body+"PY", ""); got.Phase != Release {
		t.Errorf("executable Python source classified as %s/%s", got.Phase, got.Kind)
	}
}

func TestHeredocSourceFollowsStdinConsumer(t *testing.T) {
	cases := []struct {
		cmd  string
		want Phase
	}{
		{"bash -s <<'SH'\ngit push\nSH", Release},
		{"python3 - <<'PY' < data.txt\nsubprocess.run(['git', 'push'])\nPY", Unknown},
		{"python3 - 3<<'PY'\nsubprocess.run(['git', 'push'])\nPY", Unknown},
		{"python3 -Ic'print(1)' <<'PY'\nsubprocess.run(['git', 'push'])\nPY", Unknown},
		{"python3 - <<'FIRST' <<'SECOND'\nsubprocess.run(['git', 'push'])\nFIRST\nassert True\nSECOND", Test},
		{"cat <<'FIRST'; python3 - <<'SECOND'\nsubprocess.run(['git', 'push'])\nFIRST\nassert True\nSECOND", Test},
		{"cat > example.py <<'PY'\nPath('existing.sh').write_text('''git push''')\nPY\nbash existing.sh", Code},
		{"echo cat > example.sh <<'SH'\ngit push\nSH\nbash example.sh", Code},
		{"cat > first.sh <<'ONE'\ngit push\nONE\ncat > second.sh <<'TWO'\ngo test ./...\nTWO\nbash second.sh", Test},
	}
	for _, c := range cases {
		if got := Command(c.cmd, ""); got.Phase != c.want {
			t.Errorf("%q classified as %s/%s; want %s", c.cmd, got.Phase, got.Kind, c.want)
		}
	}
}

func TestHeredocTransparentRoutes(t *testing.T) {
	for _, head := range []string{
		"env python3 -",
		"env -i -u UNUSED EXAMPLE='two words' python3 -",
		"command python3 -",
		"command -p env python3 -",
		"cat <<'PY' | python3 -",
		"cat <<'PY' | command python3 -",
	} {
		cmd := head
		if !strings.Contains(cmd, "<<") {
			cmd += " <<'PY'"
		}
		cmd += "\nassert 1 == 1\nPY"
		if got := Command(cmd, ""); got.Phase != Test {
			t.Errorf("%q classified as %s/%s; want test", cmd, got.Phase, got.Kind)
		}
	}
	for _, head := range []string{
		"env python3 script.py <<'PY'",
		"command python3 script.py <<'PY'",
		"command -v python3 <<'PY'",
		"env -S 'python3 -' <<'PY'",
		"cat > output.py <<'PY' | python3 -",
		"cat extra.py <<'PY' | python3 -",
		"cat -n <<'PY' | python3 -",
		"cat <<'PY' | python3 script.py",
		"cat <<'PY' | python3 -c 'print(1)'",
		"cat <<'PY' | python3 - < other.py",
		"cat <<'PY' | python3 - <<'OTHER'\nassert 1 == 1\nPY\nprint(1)\nOTHER",
	} {
		cmd := head
		if !strings.Contains(cmd, "\n") {
			cmd += "\nassert 1 == 1\nPY"
		}
		if got := Command(cmd, ""); got.Phase == Test || got.Phase == Release {
			t.Errorf("data in %q classified as %s/%s", cmd, got.Phase, got.Kind)
		}
	}
}

func TestInfraIdentityAllowlist(t *testing.T) {
	// only infra kinds whose exit code is a verdict get a retry identity
	for _, c := range []struct {
		cmd  string
		want bool
	}{
		{"docker run --rm img cmd", true},
		{"docker compose -f x.yml up -d", true},
		{"ssh buildhost 'cat /tmp/lease.json'", true},
		{"brew install jq", true},
		{"pkill -f vite", false},
		{"kill 1234", false},
		{"chmod +x run.sh", false},
		{"open http://127.0.0.1:5173", false},
		{"rm -rf build", false},
		{"systemctl restart app", false},
		{"launchctl list", false},
		{"log show --last 5m", false},
	} {
		if got := Command(c.cmd, "").Identity != ""; got != c.want {
			t.Errorf("%q identity=%v, want %v (%s/%s)", c.cmd, got, c.want, Command(c.cmd, "").Phase, Command(c.cmd, "").Kind)
		}
	}
}

func TestCIStatusWaitIsAQuery(t *testing.T) {
	// gh pr checks / gh run watch exit non-zero while the checks are pending or failing: the
	// state of CI, not a failed step of this agent (docs/ARCHITECTURE.md §4.2)
	for _, kind := range []string{"ci", "ci|worker_queue"} {
		if !QueryKind(WaitWorker, kind) {
			t.Errorf("wait_worker/%s must be a query kind", kind)
		}
	}
	for _, kind := range []string{"sleep", "poll-loop", "agent", "hook", "process"} {
		if QueryKind(WaitWorker, kind) {
			t.Errorf("wait_worker/%s exits are not answers", kind)
		}
	}
	if QueryKind(Test, "ci") || QueryKind(Release, "ci rerun") {
		t.Error("only the wait_worker ci kind is a CI status query")
	}
}

func TestProbesAndQueryKinds(t *testing.T) {
	// a non-zero exit of a query kind is an answer (not there, no match, not installed), never a
	// failed step; edits, scripts, `shell` and every other phase keep their verdict
	for _, c := range []struct {
		cmd      string
		phase    Phase
		kind     string
		query    bool
		identity bool
	}{
		{"test -d build/worktrees/x", Code, "probe", true, false},
		{"[ -f go.mod ] && echo yes", Code, "probe", true, false},
		{"which node", Code, "probe", true, false},
		{"type -a python3", Code, "probe", true, false},
		{"command -v docker", Code, "probe", true, false},
		{"command -v docker >/dev/null 2>&1", Code, "probe", true, false},
		{"rg -n 'IQ_DISABLE_SYNC' apps", Code, "search", true, false},
		{"cat run.sh && sed -n '1,135p' scripts/x.sh", Code, "read", true, false},
		{"ls -ld .claude/skills", Code, "list", true, false},
		{"git diff --quiet -- qa", Code, "git diff", true, false},
		{"git rev-parse HEAD && git cat-file -t abc123", Code, "git", true, false},
		{"pgrep -f vite", Code, "process", true, false},
		{"docker image ls --format '{{.Repository}}'", Code, "docker", true, false},
		{"docker image inspect app:latest --format '{{json .Config}}'", Code, "docker", true, false},
		{"docker container ls -a", Code, "docker", true, false},
		{"docker compose ls", Code, "docker", true, false},
		{"docker run --rm img cmd", Infra, "docker", false, true},
		{"cd /repo && echo hi", Code, "shell", false, false},
		{"cd apps/backend && rg -n 'IQ_DISABLE_SYNC' src", Code, "search", true, false},
		{"cd /repo && ls -la build", Code, "list", true, false},
		{"cd /repo && git status --short", Code, "git status", true, false},
		{"cd /repo && sed -i 's/a/b/' x.go", Code, "sed -i", false, false},
		{"set -o pipefail; cat /tmp/x.log | head -5", Code, "read", true, false},
		{"cat > /tmp/x.py <<'PY'\nprint(1)\nPY", Code, "write-file", false, false},
		{"python3 - <<'PY'\nfrom pathlib import Path\nPath('a').write_text('x')\nPY", Code, "script-write", false, false},
		{"go test ./...", Test, "go test", false, true},
		{"git push -u origin main", Release, "git push", false, true},
	} {
		r := Command(c.cmd, "")
		if r.Phase != c.phase || r.Kind != c.kind {
			t.Errorf("%q → %s/%s, want %s/%s (rule %s)", c.cmd, r.Phase, r.Kind, c.phase, c.kind, r.Rule)
			continue
		}
		if got := QueryKind(r.Phase, r.Kind); got != c.query {
			t.Errorf("%q query=%v, want %v", c.cmd, got, c.query)
		}
		if got := r.Identity != ""; got != c.identity {
			t.Errorf("%q identity=%v, want %v", c.cmd, got, c.identity)
		}
	}
	if r := Command("cd apps/backend && rg -n 'IQ_DISABLE_SYNC' src", ""); r.Title != "rg -n 'IQ_DISABLE_SYNC' src  (+1 more)" {
		t.Errorf("title follows the deciding segment: %q", r.Title)
	}
	// Codex's own parsed kinds are queries; the role suffix of a grouped op does not matter
	for _, k := range []string{"read", "search", "list_files"} {
		if !QueryKind(Code, k) || !QueryKind(Code, k+"|first") {
			t.Errorf("codex kind %q must be a query", k)
		}
	}
	if QueryKind(Test, "read") || QueryKind(Infra, "docker") {
		t.Error("only the code phase has query kinds")
	}
}

func TestHead(t *testing.T) {
	for _, c := range []struct{ cmd, want string }{
		{"python3 artifacts/x/capture.py before", "python3"},
		{"APP_XCB_SCHEME=Demo-iOS APP_XCB_DESTINATION='platform=iOS Simulator,name=iPad' apps/demo/scripts/xcodebuild-app.sh build", "apps/demo/scripts/xcodebuild-app.sh"},
		{"native_path=\"$(skills/screencast/scripts/native/build.sh 2>/dev/null)\"; \"$native_path\" doctor", "$native_path"},
		{"sudo -n timeout 30 ./deploy-thing --now", "./deploy-thing"},
		{"env -u FOO bash -lc 'make'", "bash"},
		{"cd /repo && ./dev build > /tmp/b.log 2>&1", "cd"},
		{"python3 - <<'PY'\nprint(1)\nPY", "python3"},
		{"", ""},
	} {
		if got := Head(c.cmd); got != c.want {
			t.Errorf("Head(%q) = %q, want %q", c.cmd, got, c.want)
		}
	}
}
