package classify

import (
	"strings"
	"testing"
)

// The 2026-09-21 unknown-command sweep (docs/ARCHITECTURE.md §13): every fix has its case here.
// Paths, hosts and names are synthetic.

// TestEnvPrefixScanner: an assignment whose quoted or $(…) value holds a space never leaves a
// fragment behind as an unknown part; a bare assignment still runs nothing.
func TestEnvPrefixScanner(t *testing.T) {
	cases := []struct {
		cmd       string
		want      Phase
		unknownOK bool
	}{
		{`LOG="/tmp/x_$(date +%Y%m%d_%H%M%S)_y.log"; echo "LOG=$LOG"; ./deploy-x.sh --cleanup > "$LOG" 2>&1; tail -n 5 "$LOG"`, Release, false},
		{`q1="WITH o AS (SELECT a, b FROM t) SELECT * FROM o"; psql -c "$q1"`, Code, false},
		{`B="/Volumes/Backups/ZX Spectrum [M4]/2026/Data"; cp -p "$B/f.md" out/; sleep 6`, WaitWorker, false},
		{`CODE="$(cat extract.js)"; playwright-cli -s=x run-code "$CODE"`, Test, false},
		{`PLACES=$(cat places.json | tr -d '\n') && playwright-cli -s=maps run-code "async page => {}"`, Test, false},
		{`IMG=$(docker image create my-image:latest) && echo "$IMG"`, Code, false},
		{`URL=$(git remote get-url origin); echo "$URL"`, Code, false},
		{`T=$(./app token 2>/dev/null | awk '/^token:/{print $2}'); echo "$T"`, Code, false},
		{`KEY="ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAA agent@host"; echo "$KEY" | wc -c`, Code, false},
		{`SINCE='2026-01-01 00:00:00 +0100'; git log --since="$SINCE"`, Code, false},
		{`GEMINI_API_KEY="$(venv/bin/python -c "from dotenv import dotenv_values; print(dotenv_values('.env').get('K',''))")" RUN_E2E=1 venv/bin/python -m pytest tests/e2e -q`, Test, false},
	}
	for _, c := range cases {
		r := Command(c.cmd, "")
		if r.Phase != c.want {
			t.Errorf("%q: %s/%s (rule %s), want %s", c.cmd, r.Phase, r.Kind, r.Rule, c.want)
		}
		for _, pt := range r.Parts {
			if pt.Phase == Unknown && !c.unknownOK {
				t.Errorf("%q: unknown part %q", c.cmd, pt.Segment)
			}
		}
	}
	for _, w := range []string{`FOO=bar`, `FOO="a b"`, `FOO=$(x y)`, `FOO='a b'`, `X=1`} {
		if !isEnvAssignment(w) {
			t.Errorf("isEnvAssignment(%q) = false", w)
		}
	}
	for _, w := range []string{`FOO`, `=x`, `1X=2`, `FOO=a b`, `./x`} {
		if isEnvAssignment(w) {
			t.Errorf("isEnvAssignment(%q) = true", w)
		}
	}
	if got := stripEnvPrefix(`A=1 B="x y" ./run`); got != "./run" {
		t.Errorf("stripEnvPrefix = %q", got)
	}
	if got := stripEnvPrefix(`A="x y"`); got != `A="x y"` {
		t.Errorf("a bare assignment must stay: %q", got)
	}
}

// TestLoopPartsAndHead: a loop whose body is unknown is named by the body, not `for e in …`;
// Head skips assignments, comments, function definitions and clipped `…` records.
func TestLoopPartsAndHead(t *testing.T) {
	loop := "for e in alpha beta gamma; do ./tools/sim $e --write out-$e.json; done"
	r := Command(loop, "")
	if len(r.Parts) != 1 || !strings.HasPrefix(r.Parts[0].Segment, "./tools/sim") {
		t.Errorf("loop part = %+v, want the body segment", r.Parts)
	}
	if got := Head(loop); got != "./tools/sim" {
		t.Errorf("Head(loop) = %q", got)
	}
	cases := map[string]string{
		"S=/tmp/scratch; # first launch reseeds\n./app --seed":      "./app",
		"restore_flags() {\n  echo x\n}\nrestore_flags; ./tool run": "restore_flags",
		"need_go() { command -v go; }; need_go && go build ./...":   "need_go",
		"\\ …":                                "",
		"path in \\ …":                        "",
		"for f in a b; do cp $f out/; done":   "cp",
		"case \"$x\" in a) echo a;; esac":     "",
		`CLI=.build/debug/app; "$CLI" health`: ".build/debug/app",
		`AB="agent-browser --session v" && $AB open http://127.0.0.1:1/`:     "agent-browser",
		`S=/tmp/s; "$S/drag" 100 980`:                                        "/tmp/s/drag",
		`SWIFTC=$(xcrun -f swiftc); "$SWIFTC" -c x.swift`:                    "swiftc",
		`PY="$(scripts/python-test-env.sh --print-python)"; "$PY" -m pytest`: "$PY",
		"swift build 2>&1 && \\\n  echo built":                               "swift",
		"G=\"node scripts/ui-graph.mjs\" && \\\n$G role add x --scope live":  "node",
	}
	for cmd, want := range cases {
		if got := Head(cmd); got != want {
			t.Errorf("Head(%q) = %q, want %q", cmd, got, want)
		}
	}
	// a line continuation joins the lines: the second line is not a segment of its own
	if r := Command("swift build && \\\n  swift test 2>&1", ""); r.Phase != Test || PartCategories(r.Parts) != 2 {
		t.Errorf("continuation: %s/%s parts=%+v", r.Phase, r.Kind, r.Parts)
	}
	for _, pt := range Command("rsync -a --delete \\\n  src/ buildhost:/tmp/x/", "").Parts {
		if strings.HasPrefix(pt.Segment, "\\") {
			t.Errorf("a part named by a continuation: %q", pt.Segment)
		}
	}
	// a comment is not a command, and an apostrophe in it opens no quote over the next lines
	if r := Command("S=/tmp/s\n# first launch: the build's seed rows\n\"$S/run-app.sh\" warm 40 >/dev/null 2>&1\ngo test ./...", ""); r.Phase != Test || Head("S=/tmp/s\n# the build's seed\ngo test ./...") != "go" {
		t.Errorf("comment with an apostrophe: %s/%s (rule %s)", r.Phase, r.Kind, r.Rule)
	}
	// a nested substitution is one expansion, never a segment of its own
	nested := `ids=$( (ls -t a/*.jsonl | head -5 | sed 's/x//'; ls b/*.jsonl) | grep -v self) && go build ./... && for id in $ids; do ./dump $id; done`
	for _, pt := range Command(nested, "").Parts {
		if strings.HasPrefix(pt.Segment, "(ls") || strings.HasPrefix(pt.Segment, "|") {
			t.Errorf("a substitution's inside became a part: %q", pt.Segment)
		}
	}
	if got := stripSubstitutions(nested); !strings.HasPrefix(got, "ids= && go build") {
		t.Errorf("stripSubstitutions = %q", got)
	}
	// a process substitution is one word of an assignment; a subshell behind `time` / `do` is its
	// inner commands; a quoted head keeps its quote in a part's text; an SQL line is data
	if r := Command(`manifest=<(printf '%s' '{"cases":[{"id":"x","path":"a b.pdf"}]}') && pytest -q tests --manifest "$manifest"`, ""); r.Phase != Test || len(r.Parts) != 1 {
		t.Errorf("process substitution: %s/%s parts=%+v", r.Phase, r.Kind, r.Parts)
	}
	if r := Command("node --test tests/x.test.mjs && time (node scripts/evaluate-x.mjs > out.txt 2>&1; tail -1 out.txt)", ""); r.Phase != Test || Head("time (node scripts/evaluate-x.mjs > o; tail -1 o)") != "node" {
		t.Errorf("time (subshell): %s/%s", r.Phase, r.Kind)
	}
	if r := Command("for p in 22 80; do (nc -z -G 2 192.0.2.1 $p 2>/dev/null && echo open $p) ; done", ""); r.Phase != Code || r.Kind != "net" {
		t.Errorf("do (subshell): %s/%s", r.Phase, r.Kind)
	}
	if got := partText(`"/Applications/X.app/Contents/MacOS/X" --headless`); !strings.HasPrefix(got, `"/Applications`) {
		t.Errorf("partText dropped the quote: %q", got)
	}
	sql := "ssh buildhost 'psql -c '\\''\nSELECT jsonb_build_object(\n  x.ord::int-1) FROM t\n'\\'''"
	if r := Command(sql, ""); r.Phase != Infra {
		t.Errorf("ssh with a quoted statement: %s/%s", r.Phase, r.Kind)
	} else {
		for _, pt := range r.Parts {
			if pt.Phase == Unknown {
				t.Errorf("an SQL line became an unknown part: %q", pt.Segment)
			}
		}
	}
	// an array assignment is one word; a quoted variable whose value holds a space stays one word
	if r := Command(`COMMON=(-i "in/a b.wav" --score s.mid); ./render "${COMMON[@]}" --steps 64 2>&1`, ""); r.Phase != Unknown || Head(`COMMON=(-i "in/a b.wav" --score s.mid); ./render "${COMMON[@]}"`) != "./render" {
		t.Errorf("array assignment: %s/%s head=%q", r.Phase, r.Kind, Head(`COMMON=(-i "in/a b.wav"); ./render x`))
	}
	if got := Head(`C="/Applications/X Browser.app/Contents/MacOS/X Browser"; "$C" --headless=new x`); got != "/Applications/X Browser.app/Contents/MacOS/X Browser" {
		t.Errorf("quoted variable head = %q", got)
	}
	// an inline program's verdict is the segment's own: in a compound it is glue (a read), not
	// an unknown part, so the unknown share is named by the command that is unknown
	if r := Command(`python3 -c "import json; print(json.load(open('r.json'))['n'])" && ./tool analyze | tail -2`, ""); len(r.Parts) != 1 || !strings.HasPrefix(r.Parts[0].Segment, "./tool") {
		t.Errorf("inline read in a compound: parts=%+v", r.Parts)
	}
	// the resolved head is what the rules see
	if r := Command(`AB="agent-browser --session v --profile p" && $AB set viewport 1280 900 && $AB open http://127.0.0.1:1/`, ""); r.Phase != Test || r.Kind != "browser automation" {
		t.Errorf("resolved $AB: %s/%s", r.Phase, r.Kind)
	}
	if r := Command(`SWIFTC=$(xcrun -f swiftc); "$SWIFTC" -c -module-name X x.swift`, ""); r.Phase != Build {
		t.Errorf("resolved $SWIFTC: %s/%s", r.Phase, r.Kind)
	}
}

// TestSweepRules: the rows and the matching added by the sweep, each on a literal command.
func TestSweepRules(t *testing.T) {
	cases := []struct {
		cmd  string
		want Phase
		kind string
	}{
		// lookups: --version / --help with redirections, -h only without a word rule
		{"cargo --version 2>&1", Code, "version"}, {"php -V", Code, "version"}, {"mc version", Code, "version"},
		{"rokit --help", Code, "help"}, {"codex plugin add --help", Code, "help"}, {"gh pr create --help", Code, "help"}, {"./run.sh -h", Code, "help"},
		{"/usr/bin/time -l go test ./... 2>&1", Test, "go test"}, {"/usr/bin/env -i HOME=/tmp swift build", Build, "swift build"},
		{"df -h", Code, "system"}, {"yt-dlp -v https://example.invalid/x", Unknown, "unknown"},
		// interpreters
		{"python3.12 -m pytest -q qa/tests", Test, "pytest"}, {"/opt/x/python@3.11/bin/python3.11 -m unittest discover -s tests", Test, "unittest"},
		{"venv/bin/python -m py_compile a.py b.py", Test, "syntax-check"}, {"code/.venv/bin/python -m compileall -q code", Test, "syntax-check"},
		{"python3 -m pip install -r requirements.txt", Build, "pip install"}, {"python3 -m pip list", Code, "pip"}, {"pip freeze", Code, "pip"},
		{"python3 -m venv .venv-test", Build, "venv"}, {"uv venv /tmp/x-venv", Build, "uv"},
		{"../.venv/bin/python -m ruff check steps/", Test, "lint"}, {"python3 -m ruff format src/", Code, "format"}, {"python -m mypy src", Test, "typecheck"},
		{"../venv/bin/python -m uvicorn backend.main:app --port 8000", Infra, "service"}, {"build/dmgbuild-venv/bin/python3 -m dmgbuild -s settings.py -D x=y 'App' out.dmg", Build, "dmgbuild"},
		{"python3 -m twine upload dist/*", Release, "publish"}, {"python3 -m playwright install chromium", Build, "playwright install"}, {"npx playwright install --with-deps", Build, "playwright install"},
		{`PYTHON_BIN="$(scripts/python-test-env.sh --print-python)" && "$PYTHON_BIN" -m pytest -q qa/tests/test_x.py`, Test, "pytest"},
		{"luau tests/config.test.luau", Test, "test-script"}, {"PATH=$HOME/.rokit/bin:$PATH luau tests/config.test.luau 2>&1", Test, "test-script"}, {"rokit run luau tests/config.test.luau", Test, "test-script"},
		{"luau tools/menu-grid.luau", Unknown, "luau"}, {"luau -e 'print(1)'", Unknown, "luau"}, {"Rscript --vanilla -e 'library(x); print(args(f))'", Unknown, "Rscript"},
		{"perl -0pi -e 's/a\\nb/c/' x.go", Code, "perl -i"}, {"perl -pi -e 's/A/B/g' a.swift b.swift", Code, "perl -i"}, {"perl -i -ne 'print unless $. >= 5' big.json", Code, "perl -i"}, {"perl -i.bak -pe 's/x/y/' f", Code, "perl -i"},
		{"perl -Ilib t/x.t", Unknown, "perl"}, {"perl -Mstrict -e 'print 1'", Unknown, "perl"},
		{"node --check scripts/bench.mjs", Test, "syntax-check"},
		// inline programs judged like heredocs
		{`python3 -c "import json; print(json.load(open('r.json'))['wer'])"`, Code, "script-read"},
		{`python3 -c "import subprocess; subprocess.run(['go','test','./...'], check=True)"`, Test, "script→go test"},
		{`node -e "require('fs').writeFileSync('out.json', JSON.stringify({}))"`, Code, "script-write"},
		{`node -e "const t = require('fs').readFileSync('page.yml', 'utf8'); console.log(t.length)"`, Code, "script-read"},
		{`ruby -ryaml -e 'y = YAML.load_file(ARGV[0]); puts y.fetch("name")' .github/workflows/x.yml`, Code, "script-read"},
		{`ruby -e 'require "yaml"; YAML.parse_file(".github/workflows/x.yml"); puts "ok"'`, Code, "script-read"},
		{`ruby -e 'PI=Math::PI; def sim(x); x*2; end; puts sim(PI)'`, Unknown, "ruby"},
		{`venv/bin/python -c "import importlib.metadata as m; print(m.version('x'))"`, Code, "script-read"},
		{`bash -c "go build ./... && go test ./..."`, Test, "script→go test"},
		{`bash -o pipefail -c './bin/tool upload-status --job 1 --format json'`, Unknown, "unknown"},
		// make: verb-first targets resolve, a verb elsewhere stays for the overlay
		{"make test-one TEST=server/tests/unit/test_x.py", Test, "make test"}, {"make deploy-prod", Release, "deploy"}, {"make build-ios", Build, "make"}, {"make ci-e2e", Test, "make test"},
		{"make ios-test", Unknown, "make ios-test"}, {"make clean-all", Unknown, "make clean-all"}, {"make run-tests", Unknown, "make run-tests"}, {"make pg-up", Unknown, "make pg-up"},
		// npm scripts
		{"npm run e2e", Test, "npm e2e"}, {"npm run e2e:install", Test, "npm e2e"}, {"npm run bench 2>&1", Test, "bench"}, {"npm run bench:stop", Test, "bench"},
		{"npm run format -- --check docs/x.js", Code, "format"}, {"npm run format:check", Code, "format"}, {"npm root -g", Code, "npm"},
		{"npm audit", Test, "audit"}, {"npm audit fix", Build, "npm install"}, {"npm uninstall react-router-dom", Build, "npm install"}, {"npm create --yes @scope/sites@0.3.0 . -- --yes", Code, "npm init"},
		{"npm --userconfig=/dev/null --globalconfig=/tmp/empty.conf --registry=https://registry.npmjs.org install --save-dev 'vitest@^4' --package-lock-only", Build, "npm install"},
		{"npm --userconfig=/dev/null --registry=https://registry.npmjs.org update browserslist --package-lock-only", Build, "npm install"},
		{"uv --project server run mecli records --help", Code, "help"}, {"uv run --project server mecli records --local", Unknown, "unknown"},
		// tools
		{"hdiutil detach /Volumes/App 2>/dev/null", Infra, "hdiutil"}, {"hdiutil create -volname App -srcfolder dist out.dmg", Build, "dmg"},
		{"ditto build/App.app dist/App.app", Code, "cp"}, {"rmdir .lock", Code, "rm"}, {"trash ./.playwright-cli", Code, "rm"},
		{"umask 077", Code, "shell"}, {"unset PHS_SESSION", Code, "shell"}, {"shopt -s nullglob", Code, "shell"}, {": > $S/changed.tsv", Code, "shell"}, {"mktemp -d /tmp/x.XXXXXX", Code, "shell"}, {"readlink -f /opt/homebrew/bin/x", Code, "shell"},
		{"source venv/bin/activate", Code, "shell"}, {". scripts/env.sh", Code, "shell"}, {"source ./deploy-x.sh", Release, "deploy-script"}, {"source venv/bin/activate && pytest -q", Test, "pytest"},
		{`[[ -n "$existing" ]]`, Code, "probe"},
		{"eval 'go test ./...'", Test, "go test"}, {`eval "$(ssh-agent -s)" >/dev/null 2>&1`, Unknown, "eval"},
		{"xmllint --noout web/grain.svg", Test, "lint"}, {"xmllint --xpath '//a' x.xml", Code, "inspect"},
		{"dns-sd -B _remotepairing._tcp local.", Code, "dns"}, {"host example.invalid 2>&1", Code, "dns"}, {"whois 192.0.2.1", Code, "dns"}, {"arp -an", Code, "net"}, {"nettop -P -L 1 -p 1", Code, "net"},
		{`screencapture -x -R1,2,3,4 "$S/shot.png"`, Code, "image"}, {"identify -format '%w %h' x.png", Code, "inspect"}, {"md5sum scripts/x", Code, "inspect"}, {"comm -23 /tmp/a /tmp/b", Code, "inspect"}, {"join /tmp/a /tmp/b", Code, "inspect"},
		{"pdftoppm -f 1 -singlefile -png x.pdf out", Infra, "media"},
		{"security show-keychain-info ~/Library/Keychains/login.keychain-db", Code, "keychain"}, {"security list-keychains", Code, "keychain"}, {"security add-generic-password -a x -s y -w z", Infra, "keychain"}, {"security find-identity -v -p codesigning", Code, "codesign"},
		{"ssh-add -l 2>&1", Code, "ssh-agent"}, {"ssh-add --apple-use-keychain", Infra, "ssh keys"}, {"ssh-keygen -R 192.0.2.7", Infra, "ssh keys"}, {"ssh-copy-id -f -i ~/.ssh/x.pub buildhost", Infra, "ssh keys"},
		{"diskutil info /Volumes/X", Code, "system"}, {"diskutil unmount /Volumes/X", Infra, "system admin"}, {"dscl . -read /Users/x UserShell", Code, "system"}, {"scutil --get ComputerName", Code, "system"}, {"kmutil showloaded", Code, "system"}, {"dscacheutil -flushcache", Infra, "system admin"}, {"/usr/sbin/DevToolsSecurity -status", Code, "system"}, {"env TERM=xterm /usr/sbin/DevToolsSecurity -enable", Infra, "system admin"},
		{"man xcrun 2>/dev/null", Code, "read"},
		{"lipo -info build/App", Code, "inspect"}, {"lipo -create a b -output c", Build, "lipo"},
		{"/usr/libexec/PlistBuddy -c 'Print :CFBundleVersion' App/Info.plist", Code, "inspect"}, {`/usr/libexec/PlistBuddy -c "Set :UITargetAppPath /tmp/App.app" x.xctestrun`, Code, "edit"},
		{"tar --mac-metadata -cf x.tar dir", Code, "archive"}, {"gzip -t a.gz b.gz", Code, "archive"}, {"unzip -t x.zip", Code, "archive"},
		{"caddy validate --config Caddyfile", Test, "lint"}, {"caddy run --config Caddyfile", Infra, "service"}, {"caddy version", Code, "version"},
		{"actionlint .github/workflows/x.yml", Test, "lint"}, {"goimports -l internal/server", Code, "format"}, {"go clean -testcache", Infra, "cleanup"},
		{"codex plugin add x@curated", Code, "harness cli"}, {"codex plugin list", Code, "harness cli"}, {"codex exec 'summarize the diff'", WaitWorker, "agent"}, {"claude -p 'review this'", WaitWorker, "agent"}, {"claude mcp list", Code, "harness cli"},
		{"rokit install", Build, "rokit"}, {"~/.rokit/bin/rokit self-install", Build, "rokit"}, {"rokit list", Code, "rokit"}, {"rokit system-info", Code, "rokit"},
		{"rojo serve", Infra, "service"}, {"PATH=$HOME/.rokit/bin:$PATH rojo build -o build/game.rbxlx", Build, "rojo"}, {"rojo sourcemap --output sourcemap.json", Build, "rojo"}, {"rojo plugin install", Infra, "setup-script"},
		{"linear issue view IQU-1 --json --no-pager", Code, "linear query"}, {"linear issue update IQU-1 -s 'In Testing'", Code, "linear"}, {"linear issue create --no-interactive -t 'Fix x'", Code, "linear"},
		{"apps/x/build/Products/Debug/App.app/Contents/MacOS/App > /tmp/app.log 2>&1", Unknown, "app run"}, {"/tmp/live-release.abc/App.app/Contents/MacOS/App", Unknown, "app run"},
		// the fallback tier: only after every rule left the phase unknown
		{"./todobem -addr 127.0.0.1:7788 -open=false > server.log 2>&1 &", Infra, "service"}, {"$S/todobem-dev -addr 127.0.0.1:7799 -auth=off -cache=off > $S/dev.log 2>&1 &", Infra, "service"},
		{"go run ./cmd/api --listen :8080", Infra, "service"}, {"go run ./cmd/dump 0123 --port 5", Unknown, "go run"}, {"HTTP_ADDR=127.0.0.1:18080 STATE_DIR=/tmp/x go run ./cmd/app", Unknown, "go run"},
		{`"$DB" "SELECT name FROM sqlite_master WHERE type='table';"`, Code, "sql"}, {`"$after_db" "SELECT installation_uuid FROM sync_state LIMIT 1"`, Code, "sql"}, {`rg "SELECT id FROM users" src/`, Code, "search"},
		{"./scripts/serve-docs.sh", Infra, "service"}, {"./health-dashboard/start.sh", Infra, "service"}, {"node tools/dataset-review/serve.mjs data/", Infra, "service"}, {"./start-test-server.sh", Test, "test-script"},
		{"node scripts/evaluate-loop.mjs test-media/130bpm", Test, "check-script"}, {"python3 bench_cold.py --base http://127.0.0.1:1/ --reps 1", Test, "check-script"},
		{"./dev ci-build", Build, "build-flag"}, {"./dev status", Unknown, "unknown"},
	}
	for _, c := range cases {
		r := Command(c.cmd, "")
		if r.Phase != c.want || r.Kind != c.kind {
			t.Errorf("%q\n  got  %s/%s (rule %s)\n  want %s/%s", c.cmd, r.Phase, r.Kind, r.Rule, c.want, c.kind)
		}
	}
	// query kinds: a non-zero exit of a lookup is an answer, a failed keychain write is not
	for kind, want := range map[string]bool{"help": true, "keychain": true, "ssh-agent": true, "pip": true, "linear query": true, "linear": false, "archive": false, "artifact": false} {
		if QueryKind(Code, kind) != want {
			t.Errorf("QueryKind(code, %q) = %v", kind, !want)
		}
	}
	if QueryKind(Infra, "keychain") {
		t.Error("an infra keychain write is a verdict")
	}
	// a bash -c compound shares its clock among its parts like the same text on the line
	if r := Command(`bash -c "go build ./... && go test ./..."`, ""); PartCategories(r.Parts) != 2 {
		t.Errorf("bash -c compound parts = %+v", r.Parts)
	}
	// a written-then-executed inline script still wins by its body
	if r := Command(`python3 -c "from pathlib import Path; Path('t.sh').write_text('''swift test''')" && bash t.sh`, ""); r.Phase != Test {
		t.Errorf("written then executed: %s/%s (rule %s)", r.Phase, r.Kind, r.Rule)
	}
	// the unknown table names an app bundle and an eval by their kind
	for _, k := range []string{"app run", "eval", "exec-script", "luau", "Rscript"} {
		if Subgroup(Unknown, k) != "script" {
			t.Errorf("Subgroup(unknown, %q) = %q", k, Subgroup(Unknown, k))
		}
	}
	if !IsChangeOp(Code, "perl -i") || IsChangeOp(Code, "artifact") || Subgroup(Code, "perl -i") != "edit" || Subgroup(Code, "linear query") != "hosting" || Subgroup(Build, "venv") != "deps" || Subgroup(Build, "rokit") != "deps" {
		t.Error("kind sets")
	}
	if LifecyclePins["review findings"] != LcReview {
		t.Error("ReportFindings pins the review stage")
	}
}
