package classify

import (
	"strconv"
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
		{"rm -fr build/", Infra, "cleanup"},
		{"rm -fR build/", Infra, "cleanup"},
		{"yarn --cwd web test", Test, "yarn test"},
		{"python3 check_tests", Test, "test-script"},
		{"printf x |& rg x", Code, "search"},
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
		{"tsc", Build, "tsc"}, {"npx tsc -p tsconfig.json", Build, "tsc"}, {"npx --no-install tsc -p tsconfig.json", Build, "tsc"}, {"bunx tsc -p .", Build, "tsc"},
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

// TestHeadWordsAndSegment: the cross-session key of a command is the head and subcommand of
// the segment that decided its phase, past keywords, wrappers, env prefixes and `bash -c`.
func TestHeadWordsAndSegment(t *testing.T) {
	for _, c := range []struct{ seg, head, sub string }{
		{"skills/run-remote-tests.sh candidate-local > out.log 2>&1", "skills/run-remote-tests.sh", "candidate-local"},
		{"while ! skills/run-remote-tests.sh app; do sleep 5; done", "skills/run-remote-tests.sh", "app"},
		{"bash -o pipefail -c 'skills/run-remote-tests.sh app 2>&1 | rg --line-buffered x'", "skills/run-remote-tests.sh", "app"},
		{"FOO=1 docker run --rm img", "docker", "run"},
		{"git -C /repo push --force-with-lease origin main", "git", "push"},
		{"make -j8 test", "make", "test"}, // make's options go, the target is the subcommand
		{"make -C src V=1 -f Makefile.dev all > build.log 2>&1", "make", "all"},
		{"ninja -C build -j8 test", "ninja", "test"},
		{"npx -y vite build", "vite", "build"}, {"pnpm dlx vite build", "vite", "build"}, {"bundle exec rspec spec/", "rspec", "spec/"},
		{"uv run --python 3.12 pytest -q", "pytest", ""}, {"npx tsc@5 --noEmit", "tsc", ""}, {"pnpm --filter web exec tsc -p .", "tsc", ""},
		{"python3 -m pytest tests/", "python3", "-m pytest"},
		{"sudo -n timeout 30 ./deploy-thing --now", "./deploy-thing", ""},
		{"for x in a b", "", ""},
		{"R=/tmp/ship.log", "", ""},
		{"", "", ""},
	} {
		if head, sub := HeadWords(c.seg); head != c.head || sub != c.sub {
			t.Errorf("HeadWords(%q) = %q %q, want %q %q", c.seg, head, sub, c.head, c.sub)
		}
	}
	for _, c := range []struct{ cmd, segment string }{
		{"set -euo pipefail; skills/run-remote-tests.sh candidate-local > out.log 2>&1", "skills/run-remote-tests.sh candidate-local > out.log 2>&1"},
		{"export GIT_SSH_COMMAND='ssh -o ServerAliveInterval=30'\ngit push origin main", "git push origin main"},
		{"go test ./...", "go test ./..."},
		{"ls", "ls"},
	} {
		if got := Command(c.cmd, "").Segment; got != c.segment {
			t.Errorf("Command(%q).Segment = %q, want %q", c.cmd, got, c.segment)
		}
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

// TestBuildRules covers the build phase across the ecosystems in scope (Rust, Go, Swift, Node,
// Python, C / C++ and make): what compiles, what installs dependencies (the deps sub-row), what a
// make / ninja target means, and what a package runner runs.
func TestBuildRules(t *testing.T) {
	cases := []struct {
		cmd  string
		want Phase
		kind string
		sub  string
	}{
		// make: options stripped, the highest-priority listed target decides, an artifact target
		// builds, an unlisted phony target is unknown by name
		{"make", Build, "make", "compile"}, {"make -j8", Build, "make", "compile"}, {"make -C src", Build, "make", "compile"}, {"make -ks", Build, "make", "compile"},
		{"make -C src test", Test, "make test", ""}, {"make --directory src test", Test, "make test", ""}, {"make V=1 CC=clang test", Test, "make test", ""}, {"make CC:=clang CFLAGS+=-O2 all", Build, "make", "compile"},
		{"make -j4 all", Build, "make", "compile"}, {"make --jobs=4 all", Build, "make", "compile"}, {"make -j 4 all", Build, "make", "compile"}, {"make -f Makefile.dev build", Build, "make", "compile"}, {"make -fMakefile.dev build", Build, "make", "compile"},
		{"make clean all", Build, "make", "compile"}, {"make build test", Test, "make test", ""}, {"make -- all", Build, "make", "compile"}, {"make -- -weird-target", Unknown, "make -weird-target", "command"},
		{"make clean", Infra, "cleanup", ""}, {"make distclean", Infra, "cleanup", ""}, {"make mrproper", Infra, "cleanup", ""},
		{"make -n build", Code, "make query", "shell"}, {"make --dry-run", Code, "make query", "shell"}, {"make -q all", Code, "make query", "shell"}, {"make -kn all", Code, "make query", "shell"}, {"make --version", Code, "make query", "shell"},
		{"make -p", Build, "make", "compile"}, // prints the database, then builds
		{"make help", Code, "make help", "shell"}, {"make logs", Code, "make logs", "shell"},
		{"make e2e", Test, "make test", ""}, {"make unit", Test, "make test", ""}, {"make bench", Test, "make test", ""}, {"make coverage", Test, "make test", ""}, {"make ci", Test, "make test", ""}, {"make vet", Test, "lint", ""}, {"make typecheck", Test, "typecheck", ""},
		{"make fmt", Code, "format", "edit"}, {"make format", Code, "format", "edit"},
		{"make deps", Build, "make deps", "deps"}, {"make vendor", Build, "make deps", "deps"}, {"make setup", Infra, "setup-script", ""}, {"make bootstrap", Infra, "setup-script", ""},
		{"make dist", Build, "make", "compile"}, {"make generate", Build, "make", "compile"}, {"make docs", Build, "make", "compile"},
		{"make build/app", Build, "make", "compile"}, {"make libfoo.a main.o", Build, "make", "compile"}, {"make -C src firmware.bin", Build, "make", "compile"},
		{"make firmware", Unknown, "make firmware", "command"}, {"make docker-up", Unknown, "make docker-up", "command"}, {"make clean firmware", Unknown, "make firmware", "command"},
		{"make up", Infra, "service", ""}, {"make down", Infra, "service", ""}, {"make watch", Infra, "service", ""}, {"make deploy", Release, "deploy", ""},
		{"make all > /tmp/build.log 2>&1", Build, "make", "compile"}, {"make > test.log 2>&1", Build, "make", "compile"},
		// ninja: the same option stripping and target rule, plus its sub-tools
		{"ninja", Build, "ninja", "compile"}, {"ninja -C build", Build, "ninja", "compile"}, {"ninja -C build -j8 all", Build, "ninja", "compile"}, {"ninja -C build test", Test, "ninja test", ""}, {"ninja check", Test, "ninja test", ""},
		{"ninja -C build install", Build, "ninja", "compile"}, {"ninja clean", Infra, "cleanup", ""}, {"ninja -t clean", Infra, "cleanup", ""}, {"ninja -t targets all", Code, "ninja query", "shell"}, {"ninja -n", Code, "ninja query", "shell"},
		{"ninja -C build src/foo.o", Build, "ninja", "compile"}, {"ninja mytarget", Unknown, "ninja mytarget", "command"},
		// C / C++
		{"gcc -O2 -o app main.c", Build, "cc", "compile"}, {"g++ -std=c++17 main.cpp", Build, "cc", "compile"}, {"clang++ -c x.cpp", Build, "cc", "compile"}, {"cc -c a.c", Build, "cc", "compile"},
		{"ld -o app a.o b.o", Build, "link", "compile"}, {"ar rcs libx.a a.o", Build, "link", "compile"}, {"./configure --prefix=/usr/local", Build, "configure", "compile"}, {"autoreconf -fi", Build, "configure", "compile"},
		{"cmake -B build -DCMAKE_BUILD_TYPE=Release", Build, "cmake", "compile"}, {"cmake --build build -j8", Build, "cmake", "compile"}, {"cmake --build build --target test", Test, "cmake test", ""}, {"cmake --build build -t check", Test, "cmake test", ""},
		{"cmake -E copy a b", Code, "cmake -E", "shell"}, {"cmake --version", Code, "version", "shell"}, {"gcc --version", Code, "version", "shell"}, {"docker --version", Code, "version", "shell"},
		{"ctest --test-dir build --output-on-failure", Test, "ctest", ""}, {"meson setup build", Build, "meson", "compile"}, {"meson compile -C build", Build, "meson", "compile"}, {"meson test -C build", Test, "meson test", ""},
		{"bazel build //...", Build, "bazel", "compile"}, {"bazel test //...", Test, "bazel test", ""}, {"bazel query //...", Code, "bazel", "shell"}, {"bazel run //:x", Unknown, "bazel run", "script"}, {"scons -j4", Build, "scons", "compile"},
		{"zig build", Build, "zig build", "compile"}, {"zig build test", Test, "zig test", ""}, {"zig test src/main.zig", Test, "zig test", ""}, {"rustc main.rs", Build, "rustc", "compile"},
		// Rust / Go / Swift
		{"cargo build --release", Build, "cargo build", "compile"}, {"cargo check", Build, "cargo check", "compile"}, {"cargo run", Unknown, "cargo run", "script"},
		{"cargo add serde", Build, "cargo deps", "deps"}, {"cargo fetch", Build, "cargo deps", "deps"}, {"cargo update", Build, "cargo deps", "deps"}, {"cargo vendor", Build, "cargo deps", "deps"}, {"cargo install cargo-nextest", Build, "cargo install", "deps"},
		{"go build ./...", Build, "go build", "compile"}, {"go install ./cmd/x", Build, "go install", "compile"}, {"go install golang.org/x/tools/cmd/goimports@latest", Build, "go install tool", "deps"},
		{"go mod tidy", Build, "go mod", "deps"}, {"go get github.com/x/y@v1.2.3", Build, "go get", "deps"}, {"go run ./cmd/x", Unknown, "go run", "script"},
		{"swift build -c release", Build, "swift build", "compile"}, {"swift package resolve", Build, "swift package resolve", "deps"}, {"swift package update", Build, "swift package resolve", "deps"}, {"xcodebuild -scheme App build", Build, "xcodebuild", "compile"},
		{"pod install --repo-update", Build, "pod install", "deps"}, {"carthage bootstrap --platform iOS", Build, "carthage", "deps"}, {"mint install realm/SwiftLint", Build, "mint install", "deps"},
		// Node: scripts, their variants, bundlers, runners
		{"npm run build", Build, "npm build", "compile"}, {"npm run build:prod", Build, "npm build", "compile"}, {"npm run build-storybook", Build, "npm build", "compile"}, {"npm run build_all", Build, "npm build", "compile"}, {"npm --prefix web run build:prod", Build, "npm build", "compile"},
		{"pnpm build", Build, "pnpm build", "compile"}, {"pnpm run build", Build, "pnpm build", "compile"}, {"pnpm build:web", Build, "pnpm build", "compile"}, {"pnpm --filter web run build", Build, "pnpm build", "compile"},
		{"yarn build", Build, "yarn build", "compile"}, {"yarn run build", Build, "yarn build", "compile"}, {"bun run build", Build, "bun build", "compile"}, {"bun build ./index.ts --outdir out", Build, "bun build", "compile"},
		{"npm run test:e2e", Test, "npm test", ""}, {"npm run tests", Test, "npm test", ""}, {"npm run test-watch", Test, "npm test", ""}, {"pnpm run test", Test, "pnpm test", ""}, {"pnpm test:unit", Test, "pnpm test", ""}, {"yarn run test", Test, "yarn test", ""},
		{"npm run testing", Unknown, "npm run", "script"}, // not a variant of test
		{"bun test", Test, "bun test", ""}, {"bun test.ts", Test, "test-script", ""}, {"bun run dev", Infra, "service", ""}, {"bun x vitest run", Test, "vitest", ""},
		{"deno test -A", Test, "deno test", ""}, {"deno lint", Test, "lint", ""}, {"deno check main.ts", Test, "typecheck", ""}, {"deno fmt", Code, "format", "edit"}, {"deno run main.ts", Unknown, "deno run", "script"}, {"deno task build", Build, "deno build", "compile"}, {"deno compile main.ts", Build, "deno compile", "compile"},
		{"next build", Build, "next build", "compile"}, {"npx next build", Build, "next build", "compile"}, {"npx vite build", Build, "vite", "compile"}, {"npx -y vite build", Build, "vite", "compile"}, {"npx -- vite build", Build, "vite", "compile"}, {"npx vite@5 build", Build, "vite", "compile"},
		{"pnpm dlx vite build", Build, "vite", "compile"}, {"pnpm exec vite build", Build, "vite", "compile"}, {"pnpm --filter web exec vite build", Build, "vite", "compile"}, {"yarn dlx vite build", Build, "vite", "compile"}, {"bunx vite build", Build, "vite", "compile"}, {"npm exec -- vite build", Build, "vite", "compile"},
		{"pnpm exec vitest run", Test, "vitest", ""}, {"npx playwright test", Test, "playwright", ""}, {"npx eslint .", Test, "lint", ""}, {"npx prettier --write .", Code, "format", "edit"}, {"npx @biomejs/biome check .", Test, "lint", ""},
		{"npx @scope/tool build", Unknown, "unknown", "command"}, // a scoped package is not a local dispatcher script
		{"npx", Unknown, "unknown", "command"}, {"npx -c 'vite build'", Unknown, "unknown", "command"},
		// a runner or a path never hides `tsc --noEmit`: the seg rule sees the unwrapped segment
		{"npx -y tsc --noEmit", Test, "typecheck", ""}, {"./node_modules/.bin/tsc --noEmit", Test, "typecheck", ""}, {"pnpm exec tsc --noEmit -p .", Test, "typecheck", ""}, {"npx tsc@5 --noEmit", Test, "typecheck", ""}, {"npx -p typescript tsc --noEmit", Test, "typecheck", ""},
		{"webpack --mode production", Build, "webpack", "compile"}, {"webpack serve", Infra, "service", ""}, {"rollup -c", Build, "rollup", "compile"}, {"parcel build src/index.html", Build, "parcel", "compile"},
		{"turbo build", Build, "turbo", "compile"}, {"turbo run build --filter=web", Build, "turbo", "compile"}, {"nx build app", Build, "nx", "compile"},
		{"npm install", Build, "npm install", "deps"}, {"npm ci", Build, "npm install", "deps"}, {"npm add left-pad", Build, "npm install", "deps"}, {"pnpm install --frozen-lockfile", Build, "pnpm install", "deps"}, {"pnpm i", Build, "pnpm install", "deps"}, {"pnpm add -D vitest", Build, "pnpm install", "deps"},
		{"yarn", Build, "yarn install", "deps"}, {"yarn install --immutable", Build, "yarn install", "deps"}, {"yarn add react", Build, "yarn install", "deps"}, {"bun install", Build, "bun install", "deps"}, {"bun add zod", Build, "bun install", "deps"},
		// Python / Ruby
		{"pip install -r requirements.txt", Build, "pip install", "deps"}, {"uv sync", Build, "uv", "deps"}, {"uv add httpx", Build, "uv", "deps"}, {"uv pip install -e .", Build, "uv", "deps"}, {"uv run pytest -q", Test, "pytest", ""}, {"uv run --with rich --python 3.12 pytest", Test, "pytest", ""},
		{"poetry install", Build, "poetry install", "deps"}, {"poetry run pytest", Test, "pytest", ""}, {"pipenv install --dev", Build, "pipenv install", "deps"},
		{"bundle install", Build, "bundle install", "deps"}, {"bundle update", Build, "bundle install", "deps"}, {"bundle exec rspec", Unknown, "unknown", "command"}, // no rspec row: honest, not build-script
		// JVM rows that were already there
		{"mvn package", Build, "mvn", "compile"}, {"mvn test", Test, "mvn test", ""}, {"gradle assemble", Build, "gradle", "compile"}, {"./gradlew build", Build, "build-flag", "compile"},
		{"rm -f /tmp/out.txt", Code, "rm", "shell"}, {"rm -rf node_modules", Infra, "cleanup", ""},
		// cargo's output directory is not a release script
		{"./target/release/app --bench", Unknown, "unknown", "command"}, {"./target/debug/app", Unknown, "unknown", "command"},
		// system packages are infrastructure, not project dependencies
		{"brew install jq", Infra, "brew", ""}, {"apt-get install -y jq", Infra, "apt", ""},
	}
	for _, c := range cases {
		got := Command(c.cmd, "")
		if got.Phase != c.want || got.Kind != c.kind {
			t.Errorf("%q\n  got  %s/%s (rule %s)\n  want %s/%s", c.cmd, got.Phase, got.Kind, got.Rule, c.want, c.kind)
			continue
		}
		if sub := Subgroup(got.Phase, got.Kind); sub != c.sub {
			t.Errorf("%q → subgroup %q, want %q", c.cmd, sub, c.sub)
		}
	}
}

// TestCompoundParts: the working parts of a compound command, the categories they span, and
// what is glue. The dominant phase (Result.Phase) is untouched by the parts.
func TestCompoundParts(t *testing.T) {
	cases := []struct {
		cmd   string
		parts string // "phase/kind[=ms]" per part, space-separated
		cats  int
	}{
		{"go build ./... && go test ./...", "build/go build test/go test", 2},
		{"cd web && npm run build 2>&1 | tail -3 && npm test 2>&1 | grep -E 'pass|fail' && npm run lint", "build/npm build test/npm test test/lint", 2},
		{"gofmt -w x.go && go vet ./... && go test ./...", "test/lint test/go test", 1},
		{"git status && git diff --stat", "", 0},
		{"make && make test", "build/make test/make test", 2},
		{"cargo build --release; cargo test", "build/cargo build test/cargo test", 2},
		{"sleep 5; go test ./...", "wait_worker/sleep=5000 test/go test", 2},
		{"sleep 2m && gh pr checks 12 --watch", "wait_worker/sleep=120000 wait_worker/ci", 1},
		{"docker compose up -d && sleep 10 && curl -s localhost:8080/health && npm test", "infra/docker compose wait_worker/sleep=10000 test/npm test", 3},
		{"python3 heavy_job.py && go test ./...", "unknown/python test/go test", 2},
		{"xcodebuild -scheme App build 2>&1 | xcbeautify", "build/xcodebuild", 1}, // an unknown pipe stage consumes, it does not work
		{"for p in a b; do go test ./$p; done", "test/go test", 1},
		{"if go build ./...; then echo ok; fi", "build/go build", 1},
		{"while ssh host 'test -d /lease'; do sleep 5; done\nAPP_REMOTE_HOST=host skills/run-remote-tests.sh workflow > a.log 2>&1", "wait_worker/poll-loop test/test-script", 2},
		{"./build.sh > b.log 2>&1 && apps/x/tests/run-test-group.sh ios > t.log 2>&1", "build/build-script test/test-script", 2},
		{"python3 - <<'PY'\nimport subprocess\nsubprocess.run(['go','build','./...'],check=True)\nsubprocess.run(['go','test','./...'],check=True)\nPY", "build/script→go build test/script→go test", 2},
		{"python3 - <<'PY'\nprint(1)\nPY\ngo test ./...", "test/go test", 1}, // an opaque script runs nothing the table knows
		// glue: an instant infra kind, a comment, a continuation fragment, a line of prose; a runnable unknown stays
		{"pkill -f app; open build/App.app && sleep 4 && \"$CLI\" health", "wait_worker/sleep=4000 unknown/unknown", 2},
		{"rm -f /tmp/out.txt && go test ./... > /tmp/out.txt", "test/go test", 1},
		{"# run the suite\ngo test ./...\n-v ./pkg | head -3", "test/go test", 1},
		{"say --voice Alex 'we start it, wait for the count-in, and play steadily'\nmeasures the little delay between our kit and the app. We start it\ngo build ./...", "unknown/unknown build/go build", 2}, // say runs; the prose line does not
		{"for id in a b; do go run ./cmd/dump \"$id\" 2>&1; done; ./scripts/check.sh", "unknown/go run test/check-script", 2},
		// a loop block is one job: its body's strongest verdict, or a wait when the body only sleeps (no literal seconds: the rounds are not in the log)
		{"for i in 1 2 3; do sleep 10; done; go test ./...", "wait_worker/poll-loop test/go test", 2},
		{"while true; do curl -s localhost:8080/health && break; sleep 2; done; npm test", "wait_worker/poll-loop test/npm test", 2},
		{"for f in a b; do gcc -c $f.c; done && ld -o app a.o b.o", "build/cc build/link", 1},
		// a pipeline is one job named by its strongest stage; infra is a category only when its exit is a verdict
		{"go test ./... 2>&1 | tee test.log | tail -5", "test/go test", 1},
		{"cat urls.txt | xargs -n1 curl -s | python3 parse.py", "", 0}, // the pipeline's strongest stage is a code lookup: glue
		{"chmod +x run.sh && ./run.sh --build-only", "build/build-flag", 1},
		{"ssh buildhost 'make -j8' && scp buildhost:out.bin . && ./flash.sh", "infra/ssh infra/scp unknown/unknown", 2},
		{"caffeinate -i ./long-job.sh; osascript -e 'display notification \"done\"'", "unknown/unknown", 1},
		// a subshell is its inner commands
		{"(cd web && npm run build) 2>&1 | tail -3 && (cd api && go test ./...)", "build/npm build test/go test", 2},
		{"(nohup node server.js > s.log 2>&1 &) && sleep 2 && curl -s localhost:3000", "unknown/node wait_worker/sleep=2000", 2},
		{"R=/tmp/x.log; go build ./... > $R 2>&1; tail -3 $R", "build/go build", 1},
	}
	for _, c := range cases {
		res := Command(c.cmd, "")
		var got []string
		for _, pt := range res.Parts {
			s := string(pt.Phase) + "/" + pt.Kind
			if pt.Ms > 0 {
				s += "=" + strconv.FormatInt(pt.Ms, 10)
			}
			got = append(got, s)
		}
		if strings.Join(got, " ") != c.parts || PartCategories(res.Parts) != c.cats {
			t.Errorf("%q\n  parts %q (%d categories)\n  want  %q (%d)", c.cmd, strings.Join(got, " "), PartCategories(res.Parts), c.parts, c.cats)
		}
	}
	if r := Command("sleep 0.5 && go test ./...", ""); r.Parts[0].Ms != 500 || sleepMs("3h") != 3*3600*1000 || sleepMs("x") != 0 {
		t.Errorf("sleep seconds %+v", r.Parts)
	}
}
