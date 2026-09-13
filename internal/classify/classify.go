// Package classify maps shell commands to phases using a deterministic rule table.
// It never looks at durations. Anything that matches no rule is Unknown.
package classify

import (
	"regexp"
	"strconv"
	"strings"
)

// Phase is the coarse activity category of an operation.
type Phase string

const (
	LLM         Phase = "llm"
	Code        Phase = "code"
	Build       Phase = "build"
	Test        Phase = "test"
	Release     Phase = "release"
	Infra       Phase = "infra"
	WaitWorker  Phase = "wait_worker"
	WaitUser    Phase = "wait_user"
	Idle        Phase = "idle"
	Compaction  Phase = "compaction"
	NoTelemetry Phase = "no_telemetry"
	Unknown     Phase = "unknown"
)

// Priority decides (a) which segment wins when parallel commands in one lane overlap and
// (b) which phase a multi-segment command gets. Higher wins. Code covers everything about
// writing code: reading/searching sources, edits, local VCS, formatting.
var Priority = map[Phase]int{
	Release: 90, Test: 80, Build: 70, WaitWorker: 65, Infra: 60, Code: 50,
	Compaction: 35, Unknown: 30, LLM: 20, WaitUser: 10, Idle: 10, NoTelemetry: 0,
}

// Result of classifying one command.
type Result struct {
	Phase    Phase  `json:"phase"`
	Kind     string `json:"kind"`     // finer label: e.g. "git push", "pytest", "sleep-loop"
	Remote   bool   `json:"remote"`   // targets a remote host (ssh / *_REMOTE_HOST=…)
	Queued   bool   `json:"queued"`   // a polling loop (while … sleep) precedes the main work
	Identity string `json:"identity"` // normalized command for retry grouping ("" if not groupable)
	Title    string `json:"title"`    // short human label
	Rule     string `json:"rule"`     // which rule matched (for the inspector)
}

// Rule is one row of the classification table. Match forms:
//
//	"word", "word sub" … up to four words  — exact head + subcommand match
//	"head:<re>"     — regexp on the executable's base name
//	"headpath:<re>" — regexp on the executable as written (with directories)
//	"seg:<re>"      — regexp on one top-level segment (env prefixes stripped)
//	"re:<re>"       — regexp on the whole command (heredoc bodies masked)
type Rule struct {
	Match string `json:"match"`
	Phase Phase  `json:"phase"`
	Kind  string `json:"kind"`
	Note  string `json:"note,omitempty"`
}

// Rules is the single source of truth. Order matters only among regex rules; word rules
// are looked up exactly (two-word first, then one-word).
var Rules = []Rule{
	// ---- waiting for workers / CI / leases
	{"gh run watch", WaitWorker, "ci", "waits for a GitHub Actions run"},
	{"gh pr checks", WaitWorker, "ci", "usually --watch; treated as CI wait"},
	{"gh workflow view", Code, "gh", ""},
	{"sleep", WaitWorker, "sleep", ""},
	{"re:(?s)\\bwhile\\b.*\\bsleep\\b", WaitWorker, "poll-loop", "shell polling loop"},
	{"re:(?s)\\buntil\\b.*\\bsleep\\b", WaitWorker, "poll-loop", ""},
	{"seg:^tail\\s+-[fF]\\b", WaitWorker, "tail-f", ""},

	// ---- release / deploy
	{"git push", Release, "git push", ""},
	{"gh pr create", Release, "pr create", ""},
	{"gh pr merge", Release, "pr merge", ""},
	{"gh pr ready", Release, "pr ready", ""},
	{"gh pr edit", Release, "pr edit", ""},
	{"gh pr comment", Release, "pr comment", ""},
	{"gh pr review", Release, "pr review", ""},
	{"gh pr close", Release, "pr close", ""},
	{"gh release", Release, "gh release", ""},
	{"gh run rerun", Release, "ci rerun", ""},
	{"gh run cancel", Release, "ci cancel", ""},
	{"glab mr", Release, "glab mr", ""},
	{"glab ci", Release, "glab ci", ""},
	{"docker push", Release, "docker push", ""},
	{"kubectl apply", Release, "kubectl apply", ""},
	{"kubectl rollout", Release, "kubectl rollout", ""},
	{"helm upgrade", Release, "helm", ""},
	{"helm install", Release, "helm", ""},
	{"npm publish", Release, "publish", ""},
	{"cargo publish", Release, "publish", ""},
	{"xcrun devicectl device install", Release, "device install", ""},
	{"xcrun altool", Release, "app store upload", ""},
	{"xcrun notarytool", Release, "notarize", ""},
	{"fastlane", Release, "fastlane", ""},
	{"flyctl", Release, "fly", ""}, {"fly", Release, "fly", ""},
	{"vercel", Release, "vercel", ""}, {"netlify", Release, "netlify", ""},
	{"head:(^|[-_])(deploy|release|ship|publish|rollout)([-_.]|$)", Release, "deploy-script", "executable whose name contains deploy/release/ship/publish/rollout as a word"},
	{"head:(^|[-_])(build|compile|bundle)([-_.]|$)", Build, "build-script", "executable whose name contains build/compile/bundle as a word"},
	{"headpath:(^|/)(deploy|release|ship)[\\w.-]*/[^/]+$", Release, "deploy-script", "script located in a deploy*/release*/ship* directory"},
	{"head:(^|[-_])(worktree|checkout|branch)([-_.]|$)", Code, "vcs-script", "executable whose name says it manages worktrees/branches"},

	// ---- test
	{"swift test", Test, "swift test", ""},
	{"xcodebuild test", Test, "xcodebuild test", ""},
	{"xcodebuild test-without-building", Test, "xcodebuild test", ""},
	{"seg:^xcodebuild\\b.*\\s(test|test-without-building)(\\s|$)", Test, "xcodebuild test", "xcodebuild … test action"},
	{"pytest", Test, "pytest", ""},
	{"python -m pytest", Test, "pytest", ""}, {"python3 -m pytest", Test, "pytest", ""}, {"python -m unittest", Test, "unittest", ""}, {"python3 -m unittest", Test, "unittest", ""},
	{"python3 -m http.server", Infra, "http server", ""}, {"python -m http.server", Infra, "http server", ""},
	{"python3", Unknown, "python", "python without a recognizable script"}, {"python", Unknown, "python", ""}, {"node", Unknown, "node", ""}, {"ruby", Unknown, "ruby", ""}, {"perl", Unknown, "perl", ""},
	{"npm test", Test, "npm test", ""},
	{"npm run test", Test, "npm test", ""}, {"seg:^npm run test\\S*", Test, "npm test", "npm run test:*"},
	{"npm run lint", Test, "lint", ""}, {"npm run typecheck", Test, "typecheck", ""}, {"npm run check", Test, "check", ""}, {"npm run verify", Test, "check", ""},
	{"pnpm lint", Test, "lint", ""}, {"pnpm typecheck", Test, "typecheck", ""}, {"yarn lint", Test, "lint", ""}, {"yarn typecheck", Test, "typecheck", ""},
	{"oxlint", Test, "lint", ""}, {"biome check", Test, "lint", ""}, {"biome lint", Test, "lint", ""}, {"stylelint", Test, "lint", ""}, {"mypy", Test, "typecheck", ""}, {"pyright", Test, "typecheck", ""},
	{"pnpm test", Test, "pnpm test", ""}, {"yarn test", Test, "yarn test", ""},
	{"npx playwright", Test, "playwright", ""}, {"playwright", Test, "playwright", ""},
	{"playwright-cli", Test, "browser automation", ""},
	{"agent-browser", Test, "browser automation", ""},
	{"npx jest", Test, "jest", ""}, {"jest", Test, "jest", ""},
	{"npx vitest", Test, "vitest", ""}, {"vitest", Test, "vitest", ""},
	{"cargo test", Test, "cargo test", ""},
	{"go test", Test, "go test", ""},
	{"mvn test", Test, "mvn test", ""}, {"gradle test", Test, "gradle test", ""},
	{"xcrun simctl", Test, "simulator", ""},
	{"xcrun devicectl device process launch", Test, "device run", "launches the app under test on a physical device (with --console it stays attached)"},
	{"xcrun xctrace", Test, "xctrace", ""},
	{"head:(^|[-_.])(test|tests|spec|e2e|smoke|qa)([-_.]|$)", Test, "test-script", "executable whose name contains test/spec/e2e/smoke/qa"},
	{"head:(^|[-_.])(preflight|precheck|check|checks|verify|verification|validate|validation|lint|audit|doctor|healthcheck|sanity)([-_.]|$)", Test, "check-script", "executable whose name says it verifies something (preflight/check/verify/validate/lint/audit)"},
	{"headpath:(^|/)(qa|tests?|e2e|spec)/", Test, "test-script", "executable located under qa/ or tests/"},
	{"seg:^node\\s+--test\\b", Test, "node test", ""},
	{"seg:^(bash|sh|zsh)\\s+-n\\b", Test, "syntax-check", "shell syntax check"},

	// ---- build
	{"swift build", Build, "swift build", ""}, {"swift package", Build, "swift package", ""}, {"xcodegen", Build, "xcodegen", ""}, {"swift run", Unknown, "swift run", "runs the built product"},
	{"xcodebuild", Build, "xcodebuild", "any xcodebuild not matched as test"},
	{"cargo build", Build, "cargo build", ""}, {"cargo check", Build, "cargo check", ""}, {"cargo clippy", Test, "lint", ""},
	{"go build", Build, "go build", ""}, {"go vet", Test, "lint", ""}, {"go generate", Build, "go generate", ""},
	{"npm run build", Build, "npm build", ""}, {"pnpm build", Build, "pnpm build", ""}, {"yarn build", Build, "yarn build", ""},
	{"npm ci", Build, "npm install", ""}, {"npm install", Build, "npm install", ""}, {"npm i", Build, "npm install", ""},
	{"pip install", Build, "pip install", ""}, {"pip3 install", Build, "pip install", ""}, {"uv sync", Build, "uv", ""},
	{"go mod", Build, "go mod", ""},
	{"make", Build, "make", "make with no target / build targets"}, {"make build", Build, "make", ""}, {"make all", Build, "make", ""}, {"make compile", Build, "make", ""},
	{"make test", Test, "make test", ""}, {"make check", Test, "make test", ""}, {"make lint", Test, "lint", ""},
	{"make run", Infra, "service", ""}, {"make serve", Infra, "service", ""}, {"make dev", Infra, "service", ""}, {"make start", Infra, "service", ""}, {"make up", Infra, "service", ""},
	{"make clean", Infra, "cleanup", ""}, {"make deploy", Release, "deploy", ""}, {"make release", Release, "deploy", ""}, {"make install", Build, "make", ""},
	{"cmake", Build, "cmake", ""}, {"ninja", Build, "ninja", ""},
	{"tsc", Build, "tsc", ""}, {"npx tsc", Build, "tsc", ""}, {"vite build", Build, "vite", ""}, {"esbuild", Build, "esbuild", ""},
	{"docker build", Build, "docker build", ""}, {"docker compose build", Build, "docker build", ""}, {"docker buildx", Build, "docker build", ""},
	{"gradle", Build, "gradle", ""}, {"mvn", Build, "mvn", ""},

	// ---- infrastructure: long-running local services
	{"npm start", Infra, "service", "dev server / app process"}, {"npm run dev", Infra, "service", ""}, {"npm run serve", Infra, "service", ""}, {"npm run start", Infra, "service", ""}, {"npm run preview", Infra, "service", ""},
	{"pnpm dev", Infra, "service", ""}, {"yarn dev", Infra, "service", ""}, {"pnpm start", Infra, "service", ""}, {"yarn start", Infra, "service", ""},
	{"vite", Infra, "service", "vite dev server"}, {"vite dev", Infra, "service", ""}, {"vite serve", Infra, "service", ""}, {"vite preview", Infra, "service", ""},
	{"vinext dev", Infra, "service", ""}, {"next dev", Infra, "service", ""}, {"next start", Infra, "service", ""}, {"nuxt dev", Infra, "service", ""}, {"astro dev", Infra, "service", ""}, {"webpack serve", Infra, "service", ""}, {"nodemon", Infra, "service", ""}, {"http-server", Infra, "service", ""}, {"serve", Infra, "service", ""},
	{"flask run", Infra, "service", ""}, {"uvicorn", Infra, "service", ""}, {"gunicorn", Infra, "service", ""}, {"rails server", Infra, "service", ""}, {"php -S", Infra, "service", ""},
	{"docker", Infra, "docker", "any other docker subcommand"},
	{"docker compose", Infra, "docker compose", ""},
	{"kubectl", Infra, "kubectl", ""},
	{"ssh", Infra, "ssh", ""},
	{"scp", Infra, "scp", ""}, {"rsync", Infra, "rsync", ""},
	{"kill", Infra, "kill", ""}, {"pkill", Infra, "kill", ""}, {"killall", Infra, "kill", ""},
	{"brew", Infra, "brew", ""}, {"apt", Infra, "apt", ""}, {"apt-get", Infra, "apt", ""},
	{"systemctl", Infra, "systemctl", ""}, {"launchctl", Infra, "launchctl", ""},
	{"chmod", Infra, "chmod", ""}, {"chown", Infra, "chown", ""},
	{"open", Infra, "open", "macOS open"}, {"osascript", Infra, "osascript", ""},
	{"xcrun devicectl", Infra, "devicectl", ""},
	{"xcrun", Infra, "xcrun", ""},
	{"caffeinate", Infra, "caffeinate", ""},
	{"log", Infra, "diagnostics", "macOS unified log"}, {"sample", Infra, "diagnostics", ""}, {"spindump", Infra, "diagnostics", ""}, {"dtrace", Infra, "diagnostics", ""},
	{"ffmpeg", Infra, "media", ""}, {"qlmanage", Infra, "media", ""}, {"pdftotext", Infra, "media", ""}, {"pdfinfo", Infra, "media", ""}, {"cairosvg", Infra, "media", ""}, {"ffprobe", Infra, "media", ""}, {"sips", Infra, "media", ""}, {"magick", Infra, "media", ""}, {"convert", Infra, "media", ""},
	{"head:(^|[-_])(install|setup|bootstrap|provision)([-_.]|$)", Infra, "setup-script", "executable whose name says it installs/sets up something"},
	{"seg:^rm\\s+-[rR]", Infra, "cleanup", "recursive delete"},
	{"head:^(cleanup|clean|teardown|reset)[\\w.-]*\\.(sh|py)$", Infra, "cleanup-script", ""},

	// ---- develop (edits, local VCS, formatting)
	{"git add", Code, "git add", ""}, {"git commit", Code, "git commit", ""},
	{"git switch", Code, "git switch", ""}, {"git checkout", Code, "git checkout", ""},
	{"git stash", Code, "git stash", ""}, {"git rebase", Code, "git rebase", ""},
	{"git merge", Code, "git merge", ""}, {"git restore", Code, "git restore", ""},
	{"git reset", Code, "git reset", ""}, {"git rm", Code, "git rm", ""}, {"git mv", Code, "git mv", ""},
	{"git worktree", Code, "git worktree", ""}, {"git fetch", Code, "git fetch", ""}, {"git pull", Code, "git pull", ""},
	{"git cherry-pick", Code, "git cherry-pick", ""}, {"git apply", Code, "git apply", ""}, {"git tag", Code, "git tag", ""},
	{"git clone", Code, "git clone", ""}, {"git init", Code, "git init", ""}, {"git submodule", Code, "git submodule", ""},
	{"gofmt", Code, "format", ""}, {"prettier", Code, "format", ""}, {"npx prettier", Code, "format", ""},
	{"oxfmt", Code, "format", ""}, {"black", Code, "format", ""}, {"ruff format", Code, "format", ""}, {"ruff", Test, "lint", "static verification"}, {"eslint", Test, "lint", ""},
	{"npx eslint", Test, "lint", ""}, {"swiftformat", Code, "format", ""}, {"swiftlint", Test, "lint", ""}, {"cargo fmt", Code, "format", ""},
	{"mkdir", Code, "mkdir", ""}, {"cp", Code, "cp", ""}, {"mv", Code, "mv", ""}, {"touch", Code, "touch", ""}, {"ln", Code, "ln", ""},
	{"seg:^sed\\s+-i\\b", Code, "sed -i", "in-place edit"},
	{"seg:^cat\\s*>{1,2}\\s*\\S", Code, "write-file", "cat > file <<EOF"},
	{"seg:^tee\\s+(-a\\s+)?\\S", Code, "write-file", ""},

	// ---- explore (read / search / inspect)
	{"cat", Code, "read", ""}, {"sed", Code, "read", "sed -n …p"}, {"head", Code, "read", ""}, {"tail", Code, "read", ""},
	{"less", Code, "read", ""}, {"more", Code, "read", ""}, {"nl", Code, "read", ""}, {"bat", Code, "read", ""},
	{"rg", Code, "search", ""}, {"grep", Code, "search", ""}, {"egrep", Code, "search", ""}, {"fgrep", Code, "search", ""}, {"ag", Code, "search", ""}, {"ack", Code, "search", ""},
	{"find", Code, "list", ""}, {"fd", Code, "list", ""}, {"ls", Code, "list", ""}, {"tree", Code, "list", ""}, {"stat", Code, "list", ""}, {"file", Code, "list", ""},
	{"wc", Code, "inspect", ""}, {"jq", Code, "inspect", ""}, {"yq", Code, "inspect", ""}, {"sort", Code, "inspect", ""}, {"uniq", Code, "inspect", ""}, {"cut", Code, "inspect", ""}, {"awk", Code, "inspect", ""}, {"tr", Code, "inspect", ""}, {"diff", Code, "inspect", ""}, {"cmp", Code, "inspect", ""}, {"strings", Code, "inspect", ""}, {"od", Code, "inspect", ""}, {"xxd", Code, "inspect", ""}, {"md5", Code, "inspect", ""}, {"shasum", Code, "inspect", ""}, {"sha256sum", Code, "inspect", ""},
	{"echo", Code, "shell", ""}, {"printf", Code, "shell", ""}, {"true", Code, "shell", ""}, {"test", Code, "shell", ""}, {"[", Code, "shell", ""}, {"pwd", Code, "shell", ""}, {"cd", Code, "shell", ""}, {"which", Code, "shell", ""}, {"type", Code, "shell", ""}, {"env", Code, "shell", ""}, {"date", Code, "shell", ""}, {"export", Code, "shell", ""}, {"set", Code, "shell", ""}, {"uname", Code, "shell", ""}, {"whoami", Code, "shell", ""}, {"hostname", Code, "shell", ""}, {"id", Code, "shell", ""}, {"basename", Code, "shell", ""}, {"dirname", Code, "shell", ""}, {"realpath", Code, "shell", ""}, {"read", Code, "shell", ""}, {"exit", Code, "shell", ""}, {"local", Code, "shell", ""}, {"declare", Code, "shell", ""}, {"shift", Code, "shell", ""}, {"return", Code, "shell", ""}, {"break", Code, "shell", ""}, {"continue", Code, "shell", ""}, {"trap", Code, "shell", ""}, {"wait", WaitWorker, "wait", "shell wait for background jobs"},
	{"ps", Code, "process", ""}, {"lsof", Code, "process", ""}, {"pgrep", Code, "process", ""}, {"top", Code, "process", ""}, {"df", Code, "system", ""}, {"du", Code, "system", ""}, {"uptime", Code, "system", ""}, {"sw_vers", Code, "system", ""}, {"system_profiler", Code, "system", ""}, {"defaults", Code, "system", ""}, {"plutil", Code, "inspect", ""}, {"mdls", Code, "inspect", ""},
	{"git status", Code, "git status", ""}, {"git diff", Code, "git diff", ""}, {"git log", Code, "git log", ""}, {"git show", Code, "git show", ""}, {"git blame", Code, "git blame", ""},
	{"git rev-parse", Code, "git", ""}, {"git branch", Code, "git branch", ""}, {"git remote", Code, "git", ""}, {"git ls-files", Code, "git", ""}, {"git grep", Code, "search", ""}, {"git describe", Code, "git", ""}, {"git config", Code, "git", ""}, {"git rev-list", Code, "git", ""}, {"git shortlog", Code, "git", ""}, {"git cat-file", Code, "git", ""}, {"git ls-remote", Code, "git", ""}, {"git name-rev", Code, "git", ""}, {"git stash list", Code, "git", ""}, {"git worktree list", Code, "git", ""}, {"git -C", Code, "git", "resolved by next word"}, {"git", Code, "git", "any other git subcommand (local)"}, {"git merge-base", Code, "git", ""}, {"git check-ignore", Code, "git", ""},
	{"gh pr view", Code, "gh", ""}, {"gh pr list", Code, "gh", ""}, {"gh pr diff", Code, "gh", ""}, {"gh pr status", Code, "gh", ""}, {"gh api", Code, "gh api", ""}, {"gh run view", Code, "gh", ""}, {"gh run list", Code, "gh", ""}, {"gh issue", Code, "gh", ""}, {"gh repo view", Code, "gh", ""}, {"gh auth", Code, "gh", ""}, {"gh", Code, "gh", "other gh"},
	{"sqlite3", Code, "sqlite", ""}, {"psql", Code, "sql", ""}, {"mysql", Code, "sql", ""},
	{"curl", Code, "http", ""}, {"wget", Code, "http", ""}, {"http", Code, "http", ""}, {"dig", Code, "dns", ""}, {"nslookup", Code, "dns", ""}, {"ping", Code, "net", ""}, {"nc", Code, "net", ""},
	{"node --version", Code, "version", ""}, {"python3 --version", Code, "version", ""}, {"go version", Code, "version", ""}, {"swift --version", Code, "version", ""},
	{"go env", Code, "go env", ""}, {"go list", Code, "go list", ""}, {"go doc", Code, "go doc", ""}, {"cargo metadata", Code, "cargo", ""}, {"npm ls", Code, "npm", ""}, {"npm view", Code, "npm", ""}, {"npm run", Unknown, "npm run", "arbitrary script"}, {"seg:^npm run build\\S*", Build, "npm build", "npm run build:*"},
	{"xcrun devicectl device info", Code, "devicectl", ""}, {"xcrun devicectl list", Code, "devicectl", ""}, {"xcrun simctl list", Code, "simulator", ""},
	{"xcode-select", Code, "xcode", ""}, {"xcodebuild -list", Code, "xcode", ""}, {"xcodebuild -showsdks", Code, "xcode", ""}, {"xcodebuild -version", Code, "xcode", ""},
	{"docker ps", Code, "docker", ""}, {"docker info", Code, "docker", ""}, {"docker version", Code, "docker", ""}, {"docker events", Code, "docker", ""}, {"docker images", Code, "docker", ""}, {"docker logs", Code, "docker", ""}, {"docker inspect", Code, "docker", ""}, {"docker compose ps", Code, "docker", ""}, {"docker compose logs", Code, "docker", ""}, {"docker network ls", Code, "docker", ""}, {"docker volume ls", Code, "docker", ""}, {"docker system df", Code, "docker", ""},
	{"kubectl get", Code, "kubectl", ""}, {"kubectl describe", Code, "kubectl", ""}, {"kubectl logs", Code, "kubectl", ""},
	{"security find-identity", Code, "codesign", ""}, {"codesign", Code, "codesign", ""}, {"spctl", Code, "codesign", ""},
	{"base64", Code, "inspect", ""},
}

type reRule struct {
	re *regexp.Regexp
	r  Rule
}

var (
	wordRules     = map[string]Rule{}
	regexRules    []reRule // whole command
	segRules      []reRule // one segment
	headRules     []reRule // executable base name
	headPathRules []reRule // executable with path
)

func init() {
	for _, r := range Rules {
		switch {
		case strings.HasPrefix(r.Match, "re:"):
			regexRules = append(regexRules, reRule{regexp.MustCompile(r.Match[3:]), r})
		case strings.HasPrefix(r.Match, "seg:"):
			segRules = append(segRules, reRule{regexp.MustCompile(r.Match[4:]), r})
		case strings.HasPrefix(r.Match, "head:"):
			headRules = append(headRules, reRule{regexp.MustCompile(r.Match[5:]), r})
		case strings.HasPrefix(r.Match, "headpath:"):
			headPathRules = append(headPathRules, reRule{regexp.MustCompile(r.Match[9:]), r})
		default:
			wordRules[r.Match] = r
		}
	}
}

var (
	reEnvPrefix   = regexp.MustCompile(`^(?:[A-Za-z_][A-Za-z0-9_]*=(?:"[^"]*"|'[^']*'|\S*)\s+)+`)
	reWS          = regexp.MustCompile(`\s+`)
	reHeredoc     = regexp.MustCompile(`<<-?\s*['"]?([A-Za-z_][A-Za-z0-9_]*)['"]?`)
	reRemote      = regexp.MustCompile(`(^|\s)(ssh|scp|rsync)\s|\b\w*REMOTE_HOST=|--remote\b|run-remote`)
	reScriptWrite = regexp.MustCompile(`write_text\(|write_bytes\(|open\([^)]*['"][wa]b?['"]|fs\.writeFile|writeFileSync|json\.dump\(|shutil\.|os\.rename|os\.remove|Path\([^)]*\)\.unlink`)
	reSubst       = regexp.MustCompile(`\$\([^)]*\)|` + "`[^`]*`")
	reNodePkgCLI  = regexp.MustCompile(`node_modules/(?:@[\w.-]+/)?([\w.-]+)/(?:dist/|bin/|lib/)?[\w.-]*(?:cli|bin|index)[\w.-]*\.(?:m?js|cjs)$`)
	reScriptRead  = regexp.MustCompile(`read_text\(|read_bytes\(|json\.load\(|open\([^)]*\)|sqlite3\.connect\(|\.glob\(|os\.listdir|os\.walk|readFileSync|readdirSync|\breadFile\(|\.execute\(|\bfetch\(|new WebSocket\(|https?\.get\(|urlopen\(|requests\.get\(`)
	reScriptTest  = regexp.MustCompile(`(?m)^\s*assert\b|\bassert\.(?:ok|equal|strictEqual|deepEqual|deepStrictEqual|match|throws)\(|\bassert\(|\bexpect\(|from ['"][^'"]*(?:playwright|puppeteer)[^'"]*['"]|require\(['"](?:playwright|puppeteer)|import pytest|import unittest`)
	reScriptDML   = regexp.MustCompile(`(?i)\b(insert\s+into|update\s+\w+\s+set|delete\s+from|drop\s+table|alter\s+table|create\s+table|replace\s+into)\b|\.commit\(`)
	reSubprocList = regexp.MustCompile(`(?:subprocess\.(?:run|call|check_call|check_output|Popen)|spawnSync|spawn|execFileSync|execFile)\(\s*\[([^\]]*)\]`)
	reArgvAssign  = regexp.MustCompile(`(?m)^\s*\w+\s*=\s*\[([^\]]*)\]`)
	reSubprocStr  = regexp.MustCompile(`(?:subprocess\.(?:run|call|check_call|check_output|Popen|getoutput|getstatusoutput)|os\.system|os\.popen|execSync|exec)\(\s*(?:f?r?)(?:"((?:[^"\\]|\\.)*)"|'((?:[^'\\]|\\.)*)')`)
	reStrLit      = regexp.MustCompile(`"((?:[^"\\]|\\.)*)"|'((?:[^'\\]|\\.)*)'`)
)

var (
	reWriteBody = regexp.MustCompile(`(?s)\.write_text\(\s*(?:r|f|rf|fr)?(?:'''(.*?)'''|"""(.*?)""")`)
	rePathLit   = regexp.MustCompile(`Path\(\s*['"]([^'"]+)['"]\s*\)|open\(\s*['"]([^'"]+)['"]\s*,\s*['"][wa]`)
	reCatHead   = regexp.MustCompile(`^cat\s*>{1,2}\s*(\S+)\s*<<-?\s*['"]?([A-Za-z_][A-Za-z0-9_]*)['"]?`)
)

// writtenScripts uses executable interpreter sources and actual cat heredoc writes.
// Data inside a file being written cannot itself supply evidence of another write.
func writtenScripts(sources []heredocSource) map[string]string {
	out := map[string]string{}
	for _, source := range sources {
		if source.writePath != "" {
			out[source.writePath] = source.body
		}
		if source.interpreter != "script" {
			continue
		}
		bodies := reWriteBody.FindAllStringSubmatch(source.body, -1)
		if len(bodies) == 0 {
			continue
		}
		var paths []string
		for _, m := range rePathLit.FindAllStringSubmatch(source.body, -1) {
			p := m[1]
			if p == "" {
				p = m[2]
			}
			paths = append(paths, p)
		}
		for _, m := range bodies {
			body := m[1]
			if body == "" {
				body = m[2]
			}
			if strings.TrimSpace(body) == "" {
				continue
			}
			for _, p := range paths {
				if _, dup := out[p]; !dup {
					out[p] = body
				}
			}
		}
	}
	return out
}

// executedPath returns the script path a segment executes (bash f, sh f, ./f, f), or "".
func executedPath(seg string) string {
	f := strings.Fields(strings.TrimSpace(reEnvPrefix.ReplaceAllString(strings.TrimSpace(seg), "")))
	if len(f) == 0 {
		return ""
	}
	h := f[0]
	if h == "bash" || h == "sh" || h == "zsh" || h == "source" || h == "." {
		for _, a := range f[1:] {
			if !strings.HasPrefix(a, "-") {
				return a
			}
		}
		return ""
	}
	if strings.Contains(h, "/") || strings.HasSuffix(h, ".sh") {
		return h
	}
	return ""
}

func samePath(a, b string) bool {
	a, b = strings.TrimPrefix(a, "./"), strings.TrimPrefix(b, "./")
	return a == b || strings.HasSuffix(a, "/"+b) || strings.HasSuffix(b, "/"+a)
}

// scriptCommands extracts literal shell commands from a script body: argv lists and string
// commands given to subprocess/os.system/execSync. Non-literal elements are ignored.
func scriptCommands(body string) []string {
	var out []string
	for _, m := range reSubprocList.FindAllStringSubmatch(body, -1) {
		var argv []string
		for _, lit := range reStrLit.FindAllStringSubmatch(m[1], -1) {
			v := lit[1]
			if v == "" {
				v = lit[2]
			}
			argv = append(argv, v)
		}
		if len(argv) > 0 {
			out = append(out, strings.Join(argv, " "))
		}
	}
	for _, m := range reSubprocStr.FindAllStringSubmatch(body, -1) {
		v := m[1]
		if v == "" {
			v = m[2]
		}
		if strings.TrimSpace(v) != "" {
			out = append(out, v)
		}
	}
	// argv lists bound to a variable and passed to subprocess later (cmd=[…]; subprocess.run(cmd))
	if strings.Contains(body, "subprocess.") || strings.Contains(body, "spawn") || strings.Contains(body, "execFile") {
		for _, m := range reArgvAssign.FindAllStringSubmatch(body, -1) {
			var argv []string
			for _, lit := range reStrLit.FindAllStringSubmatch(m[1], -1) {
				v := lit[1]
				if v == "" {
					v = lit[2]
				}
				argv = append(argv, v)
			}
			if len(argv) >= 2 && !strings.ContainsAny(argv[0], " \t") {
				out = append(out, strings.Join(argv, " "))
			}
		}
	}
	return out
}

// Command classifies a shell command string. codexKind is Codex's own parsed_cmd type
// ("read", "search", "list_files") when present; it short-circuits to Code.
func Command(cmd string, codexKind string) Result { return command(cmd, codexKind, 0) }

// command is the depth-limited worker: script bodies and written-then-executed files are
// classified one level down, never deeper.
func command(cmd string, codexKind string, depth int) Result {
	res := Result{Phase: Unknown, Kind: "unknown", Title: shortTitle(cmd)}
	res.Remote = reRemote.MatchString(cmd)
	switch codexKind {
	case "read", "search", "list_files":
		res.Phase, res.Kind, res.Rule = Code, codexKind, "codex:parsed_cmd"
		return res
	}
	masked, bodies := maskHeredocs(cmd)
	best := -1
	bestSeg := ""
	consider := func(r Rule, label string, seg string) {
		if p := Priority[r.Phase]; p > best {
			best, res.Phase, res.Kind, res.Rule, bestSeg = p, r.Phase, r.Kind, label, seg
		}
	}
	loop := false
	for _, rr := range regexRules {
		if rr.re.MatchString(masked) {
			if rr.r.Kind == "poll-loop" {
				loop = true
			}
			consider(rr.r, "re:"+rr.r.Match[3:], "")
		}
	}
	for _, seg := range segments(masked) {
		seg = strings.TrimSpace(reEnvPrefix.ReplaceAllString(strings.TrimSpace(seg), ""))
		for _, rr := range segRules {
			if rr.re.MatchString(seg) {
				consider(rr.r, rr.r.Match, seg)
			}
		}
		if r, label, ok := matchHead(seg); ok {
			consider(r, label, seg)
		}
	}
	// Opaque interpreter scripts (heredoc bodies). Only explicit, literal signals are used:
	//  - literal commands passed to subprocess/os.system/execSync are classified with this
	//    same table (one level deep); the script takes the strongest of them;
	//  - bodies that write files → develop; bodies that only read/query → explore;
	//  - anything else stays unknown.
	if depth > 1 {
		bodies = nil
	}
	for _, source := range bodies {
		if source.interpreter == "" {
			continue
		}
		b := source.body
		if source.interpreter == "shell" {
			r := command(b, "", depth+1)
			if r.Phase != Unknown {
				consider(Rule{Phase: r.Phase, Kind: "script→" + r.Kind}, "shell heredoc", "")
				loop = loop || r.Queued
			}
			continue
		}
		inner := scriptCommands(b)
		for _, ic := range inner {
			r := command(ic, "", depth+1)
			if r.Phase != Unknown {
				consider(Rule{Phase: r.Phase, Kind: "script→" + r.Kind}, "heredoc runs: "+shortTitle(ic), ic)
				loop = loop || r.Queued
			}
		}
		if reScriptTest.MatchString(b) {
			consider(Rule{Phase: Test, Kind: "script-check"}, "heredoc asserts / drives a browser", "")
		}
		if reScriptWrite.MatchString(b) {
			consider(Rule{Phase: Code, Kind: "script-write"}, "heredoc writes files", "")
		} else if len(inner) == 0 && reScriptRead.MatchString(b) && !reScriptDML.MatchString(b) {
			consider(Rule{Phase: Code, Kind: "script-read"}, "heredoc only reads/queries/probes", "")
		}
	}
	// A file written in this command with a literal body (Path(f).write_text('''…''') or
	// cat > f <<EOF) and executed by a later segment (bash f / ./f) is classified by the
	// content that was written — the text is right there in the command.
	if depth <= 1 {
		writtenSources := bodies
		for _, seg := range segments(masked) {
			if source := inlinePythonSource(seg); source != "" {
				writtenSources = append(writtenSources, heredocSource{body: source, interpreter: "script"})
			}
		}
		if written := writtenScripts(writtenSources); len(written) > 0 {
			for _, seg := range segments(masked) {
				path := executedPath(seg)
				if path == "" {
					continue
				}
				for wp, content := range written {
					if samePath(wp, path) {
						r := command(content, "", depth+1)
						if r.Phase != Unknown {
							consider(Rule{Phase: r.Phase, Kind: "script→" + r.Kind}, "runs a script written in this command", seg)
							loop = loop || r.Queued || r.Phase == WaitWorker
						}
					}
				}
			}
		}
	}
	if best < 0 {
		res.Phase, res.Kind, res.Rule = Unknown, "unknown", ""
	}
	// A polling loop followed by real work: the work wins, but remember the queue.
	if loop && res.Phase != WaitWorker {
		res.Queued = true
	}
	// Title: the segment that decided the phase, so "x=1; while …; done; run-tests.sh" reads as the test.
	if bestSeg != "" && !strings.HasPrefix(cmd, bestSeg) {
		res.Title = shortTitle(bestSeg)
		if len(segments(masked)) > 1 {
			res.Title += "  (+" + strconv.Itoa(len(segments(masked))-1) + " more)"
		}
	}
	if res.Phase == Test || res.Phase == Build || res.Phase == Release {
		res.Identity = Identity(cmd)
	}
	return res
}

// cosmeticFilters are pipe stages that only shape output; they never change what ran.
var cosmeticFilters = map[string]bool{"tee": true, "tail": true, "head": true, "grep": true, "rg": true, "wc": true, "cat": true, "sed": true, "awk": true, "cut": true, "sort": true, "uniq": true, "xcbeautify": true, "xcpretty": true, "jq": true, "tr": true, "less": true, "column": true}

// Identity removes supported output redirections and trailing cosmetic pipe stages.
// Words retain their original quoting/whitespace. Complex shell input stays verbatim.
func Identity(cmd string) string {
	tokens, ok := shellTokens(cmd)
	if !ok {
		return cmd
	}
	for _, t := range tokens {
		if t.op && (strings.Contains(t.text, "<<") || strings.HasPrefix(t.text, "#")) {
			return cmd
		}
	}
	tokens = stripOutputRedirects(tokens)
	// Only a final simple pipe stage is cosmetic; never remove later &&/;/newline work.
	for {
		pipe := -1
		for i := len(tokens) - 1; i >= 0; i-- {
			if tokens[i].op {
				if tokens[i].text == "|" {
					pipe = i
				}
				break
			}
		}
		if pipe < 0 || pipe+1 >= len(tokens) || !cosmeticFilters[tokens[pipe+1].text] {
			break
		}
		tokens = tokens[:pipe]
	}
	return joinShellTokens(tokens)
}

// segments splits a (heredoc-masked) command into top-level simple commands.
func segments(cmd string) []string {
	var out []string
	var cur strings.Builder
	depth := 0
	inS, inD := false, false
	flush := func() {
		s := strings.TrimSpace(cur.String())
		cur.Reset()
		if s != "" {
			out = append(out, s)
		}
	}
	for i := 0; i < len(cmd); i++ {
		c := cmd[i]
		switch {
		case c == '\\' && i+1 < len(cmd):
			cur.WriteByte(c)
			i++
			cur.WriteByte(cmd[i])
			continue
		case inS:
			if c == '\'' {
				inS = false
			}
		case inD:
			if c == '"' {
				inD = false
			}
		case c == '\'':
			inS = true
		case c == '"':
			inD = true
		case c == '(' || c == '{':
			depth++
		case c == ')' || c == '}':
			if depth > 0 {
				depth--
			}
		case depth == 0 && (c == '\n' || c == ';'):
			flush()
			continue
		case depth == 0 && c == '&' && i+1 < len(cmd) && cmd[i+1] == '&':
			flush()
			i++
			continue
		case depth == 0 && c == '|':
			if i+1 < len(cmd) && cmd[i+1] == '|' {
				i++
			}
			flush()
			continue
		}
		cur.WriteByte(c)
	}
	flush()
	return out
}

var shellKeywords = map[string]bool{"if": true, "then": true, "else": true, "elif": true, "fi": true, "for": true, "while": true, "until": true, "do": true, "done": true, "case": true, "esac": true, "in": true, "function": true, "select": true, "time": true, "!": true, "{": true, "}": true}

var readOnlySub = map[string]bool{"status": true, "list": true, "show": true, "info": true, "help": true, "--help": true, "-h": true, "--version": true, "version": true, "describe": true, "check": true, "validate": true, "plan": true, "diff": true}

var interpreters = map[string]bool{"python": true, "python3": true, "node": true, "ruby": true, "perl": true, "bash": true, "sh": true, "zsh": true, "deno": true, "bun": true, "swift": true}

// matchHead finds the rule for a simple command by its executable and optional subcommand.
func matchHead(seg string) (Rule, string, bool) {
	seg = strings.TrimSpace(reSubst.ReplaceAllString(seg, ""))
	for {
		fields := shellFields(seg)
		if len(fields) == 0 {
			return Rule{}, "", false
		}
		h := fields[0]
		if shellKeywords[h] {
			if len(fields) == 1 {
				return Rule{}, "", false
			}
			seg = strings.Join(fields[1:], " ")
			continue
		}
		switch h {
		case "sudo", "nohup", "exec", "command", "builtin", "nice", "timeout", "gtimeout", "env", "xargs", "caffeinate", "time":
			rest := fields[1:]
			for len(rest) > 0 && (strings.HasPrefix(rest[0], "-") || (h == "timeout" && isNumberish(rest[0])) || (h == "env" && strings.Contains(rest[0], "="))) {
				if h == "env" && (rest[0] == "-u" || rest[0] == "--unset") && len(rest) > 1 {
					rest = rest[2:]
					continue
				}
				rest = rest[1:]
			}
			if len(rest) > 0 {
				seg = strings.Join(rest, " ")
				continue
			}
			if h == "caffeinate" {
				return wordRules["caffeinate"], "caffeinate", true
			}
			return Rule{}, "", false
		}
		break
	}
	fields := shellFields(seg)
	head := fields[0]
	base := head
	if i := strings.LastIndex(head, "/"); i >= 0 {
		base = head[i+1:]
	}
	// node_modules/.bin/<tool> → <tool>; node … node_modules/<pkg>/…/cli.js <sub> → <pkg> <sub>
	if strings.Contains(head, "node_modules/.bin/") {
		head = base
	}
	if base == "node" || base == "npx" {
		for i, a := range fields[1:] {
			if strings.HasPrefix(a, "-") {
				continue
			}
			if m := reNodePkgCLI.FindStringSubmatch(a); m != nil {
				fields = append([]string{m[1]}, fields[i+2:]...)
				head, base = m[1], m[1]
			}
			break
		}
	}
	// git [-C dir] [-c k=v] [--git-dir=…] [--no-pager] <sub> → git <sub>
	if base == "git" {
		rest := fields[1:]
		for len(rest) > 0 {
			switch {
			case (rest[0] == "-C" || rest[0] == "-c") && len(rest) > 1:
				rest = rest[2:]
			case strings.HasPrefix(rest[0], "--git-dir") || strings.HasPrefix(rest[0], "--work-tree") || rest[0] == "--no-pager" || strings.HasPrefix(rest[0], "-c"):
				rest = rest[1:]
			default:
				goto gitDone
			}
		}
	gitDone:
		fields = append([]string{"git"}, rest...)
	}
	// go run <pkg> / cargo run --bin <name>: judge by the package or binary name
	if (base == "go" || base == "cargo") && len(fields) > 2 && fields[1] == "run" {
		for _, a := range fields[2:] { // a server flag describes the runtime behaviour best
			w := strings.TrimLeft(strings.ToLower(a), "-")
			if w == "serve" || w == "server" {
				return Rule{Match: "flag:serve", Phase: Infra, Kind: "service"}, "flag --serve", true
			}
		}
		for i := 2; i < len(fields); i++ {
			a := fields[i]
			if a == "--bin" && i+1 < len(fields) {
				a = fields[i+1]
			} else if strings.HasPrefix(a, "-") {
				continue
			}
			if r, label, ok := matchScript(strings.TrimSuffix(a, "/")); ok {
				return r, label, true
			}
			break
		}
	}
	// exact word rules first: four-word (xcrun devicectl device install) … down to one-word
	for n := 5; n >= 2; n-- {
		if len(fields) >= n {
			key := base + " " + strings.Join(fields[1:n], " ")
			if r, ok := wordRules[key]; ok {
				return r, key, true
			}
		}
	}
	// interpreters: "python3 - <<PY" is opaque; "python3 script.py" is judged by the script name;
	// "bash -lc '…'" is judged by the inner command.
	if interpreters[base] {
		if len(fields) > 1 && fields[1] == "-" {
			return Rule{}, "", false
		}
		args := fields[1:]
		if base == "bash" || base == "sh" || base == "zsh" {
			// bash -n script → syntax check only, nothing runs
			for _, a := range args {
				if a == "-n" {
					return Rule{Match: "bash -n", Phase: Test, Kind: "syntax-check"}, "bash -n", true
				}
			}
			// bash [-o pipefail] [-e] -c '<cmd>' / -lc '<cmd>' → judge the inner command
			for i, a := range args {
				if strings.HasPrefix(a, "-") && strings.Contains(a, "c") && !strings.HasPrefix(a, "--") && i+1 < len(args) {
					return matchHead(strings.Join(args[i+1:], " "))
				}
			}
		}
		for len(args) > 0 && strings.HasPrefix(args[0], "-") && args[0] != "-m" {
			args = args[1:]
		}
		if len(args) > 1 && args[0] == "-m" {
			key := base + " -m " + args[1]
			if r, ok := wordRules[key]; ok {
				return r, key, true
			}
			return Rule{}, "", false
		}
		if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
			if r, label, ok := matchScript(args[0]); ok {
				return r, label, true
			}
		}
		if r, ok := wordRules[base]; ok {
			return r, base, true
		}
		return Rule{}, "", false
	}
	if r, ok := wordRules[base]; ok {
		return r, base, true
	}
	if r, label, ok := matchScript(head); ok {
		// deploy/test scripts invoked with a read-only subcommand are lookups, not work
		if len(fields) > 1 && readOnlySub[fields[1]] {
			return Rule{Match: label, Phase: Code, Kind: r.Kind + " " + fields[1]}, label + " (read-only subcommand)", true
		}
		return r, label, true
	}
	// unknown executable: literal flags / subcommands say what it does (./run.sh --build-only)
	for _, a := range fields[1:] {
		w := strings.TrimLeft(strings.ToLower(a), "-")
		if i := strings.IndexByte(w, '='); i >= 0 {
			w = w[:i]
		}
		switch w {
		case "build", "rebuild", "build-only", "compile", "bundle":
			return Rule{Match: "flag:" + w, Phase: Build, Kind: "build-flag"}, "flag --" + w, true
		case "test", "tests", "test-only", "run-tests", "check", "verify", "lint":
			return Rule{Match: "flag:" + w, Phase: Test, Kind: "test-flag"}, "flag --" + w, true
		case "deploy", "release", "publish", "ship", "install-device":
			return Rule{Match: "flag:" + w, Phase: Release, Kind: "release-flag"}, "flag --" + w, true
		case "serve", "server", "dev-server", "watch":
			return Rule{Match: "flag:" + w, Phase: Infra, Kind: "service"}, "flag --" + w, true
		}
	}
	return Rule{}, "", false
}

// matchScript applies head/headpath rules to an executable path.
func matchScript(path string) (Rule, string, bool) {
	base := path
	if i := strings.LastIndex(path, "/"); i >= 0 {
		base = path[i+1:]
	}
	for _, rr := range headRules {
		if rr.re.MatchString(base) {
			return rr.r, rr.r.Match, true
		}
	}
	for _, rr := range headPathRules {
		if rr.re.MatchString(path) {
			return rr.r, rr.r.Match, true
		}
	}
	return Rule{}, "", false
}

// shellFields splits a simple command into words, keeping quoted spans together
// (quotes removed) so `-c key='a b'` is one argument.
func shellFields(s string) []string {
	var out []string
	var cur strings.Builder
	inS, inD, has := false, false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\\' && i+1 < len(s) && !inS:
			i++
			cur.WriteByte(s[i])
			has = true
		case inS:
			if c == '\'' {
				inS = false
			} else {
				cur.WriteByte(c)
			}
		case inD:
			if c == '"' {
				inD = false
			} else {
				cur.WriteByte(c)
			}
		case c == '\'':
			inS, has = true, true
		case c == '"':
			inD, has = true, true
		case c == ' ' || c == '\t' || c == '\n':
			if has {
				out = append(out, cur.String())
				cur.Reset()
				has = false
			}
		default:
			cur.WriteByte(c)
			has = true
		}
	}
	if has {
		out = append(out, cur.String())
	}
	return out
}

func isNumberish(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c == '.' || c == 's' || c == 'm' || c == 'h') {
			return false
		}
	}
	return true
}

type heredocSource struct {
	body, interpreter, writePath string
}

// maskHeredocs keeps all heredoc data opaque. Only stdin consumed as interpreter
// source is returned for classification; a script filename/-c/-m receives data.
func maskHeredocs(cmd string) (string, []heredocSource) {
	var bodies []heredocSource
	lines := strings.Split(cmd, "\n")
	var out []string
	for i := 0; i < len(lines); i++ {
		out = append(out, lines[i])
		type declaration struct {
			tag, interpreter, writePath string
			tabs                        bool
		}
		var declarations []declaration
		tokens, ok := shellTokens(lines[i])
		if !ok {
			// Unsupported shell expressions remain opaque rather than inferred executable.
			for _, m := range reHeredoc.FindAllStringSubmatch(lines[i], -1) {
				declarations = append(declarations, declaration{tag: m[1], tabs: strings.HasPrefix(m[0], "<<-")})
			}
		} else {
			for k, token := range tokens {
				op := strings.TrimLeft(token.text, "0123456789")
				if !token.op || (op != "<<" && op != "<<-") || k+1 >= len(tokens) || tokens[k+1].op {
					continue
				}
				words := shellFields(tokens[k+1].text)
				if len(words) != 1 {
					continue
				}
				lo, hi := k, k+2
				for lo > 0 && !shellControl(tokens[lo-1]) {
					lo--
				}
				for hi < len(tokens) && !shellControl(tokens[hi]) {
					hi++
				}
				stdin := token.text == op || token.text == "0"+op
				for _, later := range tokens[k+2 : hi] {
					redir := strings.TrimLeft(later.text, "0123456789")
					if later.op && strings.HasPrefix(redir, "<") && (later.text == redir || later.text == "0"+redir) {
						stdin = false // a later input redirection replaces this heredoc
					}
				}
				interpreter, writePath := "", ""
				if stdin {
					interpreter = heredocInterpreter(tokens[lo:hi])
					if interpreter == "" {
						interpreter = pipedHeredocInterpreter(tokens, lo, hi)
					}
					header := reEnvPrefix.ReplaceAllString(joinShellTokens(tokens[lo:hi]), "")
					if m := reCatHead.FindStringSubmatch(header); m != nil {
						writePath = m[1]
					}
				}
				declarations = append(declarations, declaration{words[0], interpreter, writePath, op == "<<-"})
			}
		}
		for _, decl := range declarations {
			var body []string
			j := i + 1
			for ; j < len(lines); j++ {
				line := lines[j]
				if decl.tabs {
					line = strings.TrimLeft(line, "\t")
				}
				if line == decl.tag {
					break
				}
				body = append(body, line)
			}
			bodies = append(bodies, heredocSource{strings.Join(body, "\n"), decl.interpreter, decl.writePath})
			out = append(out, "<<heredoc>>")
			i = j
		}
	}
	return strings.Join(out, "\n"), bodies
}

// shortTitle makes a compact one-line label from a command. Heredoc scripts are labelled
// by their interpreter and first meaningful body line.
func shortTitle(cmd string) string {
	s := strings.TrimSpace(cmd)
	if m := reHeredoc.FindStringIndex(s); m != nil {
		head := strings.TrimSpace(reEnvPrefix.ReplaceAllString(s[:m[0]], ""))
		lines := strings.Split(s, "\n")
		body := ""
		n := 0
		for _, ln := range lines[1:] {
			t := strings.TrimSpace(ln)
			if t == "" || strings.HasPrefix(t, "#") {
				continue
			}
			n++
			if body == "" && !strings.HasPrefix(t, "import ") && !strings.HasPrefix(t, "from ") && !strings.HasPrefix(t, "const ") && !strings.HasPrefix(t, "require(") {
				body = t
			}
		}
		if n > 1 {
			n-- // the terminator
		}
		s = head + " script (" + strconv.Itoa(n) + " lines): " + body
	} else if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i] + " …"
	}
	s = reWS.ReplaceAllString(s, " ")
	if len(s) > 110 {
		s = s[:107] + "…"
	}
	return s
}
