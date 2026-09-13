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
