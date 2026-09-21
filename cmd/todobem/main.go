// todobem — a local viewer for long AI-agent sessions.
package main

import (
	"embed"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/extractumio/todobem/internal/auth"
	"github.com/extractumio/todobem/internal/classify"
	"github.com/extractumio/todobem/internal/fleet"
	"github.com/extractumio/todobem/internal/server"
	"github.com/extractumio/todobem/internal/settings"
	"github.com/extractumio/todobem/internal/store"
)

//go:embed web
var webFS embed.FS

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "token":
			os.Exit(runToken(os.Args[2:]))
		case "cache":
			os.Exit(runCache(os.Args[2:]))
		case "unknown":
			os.Exit(runUnknown(os.Args[2:]))
		case "agent":
			os.Exit(runAgentCmd(os.Args[2:]))
		case "hub":
			os.Exit(runHub(os.Args[2:]))
		}
	}
	addr := flag.String("addr", "", "listen address (default 127.0.0.1:7788; agent mode: every interface on :7789)")
	agentMode := flag.Bool("agent", false, "agent mode: headless, TLS, answers a paired hub on /agent/v1/ (docs/AGENT-MODE.md); prints a pairing string")
	agentKey := flag.String("agent-key", fleet.DefaultAgentKeyPath(), "agent mode: the agent key file (the certificate sits next to it)")
	version := flag.Bool("version", false, "print the build and exit")
	codexHome := flag.String("codex", "", codexFlagHelp)
	claudeHome := flag.String("claude", "", claudeFlagHelp)
	settingsPath := flag.String("settings", settings.DefaultPath(), settingsFlagHelp)
	openBrowser := flag.Bool("open", true, "open the browser on start")
	rules := flag.String("rules", "", "path to a user rules overlay (JSON); also loads $TODOBEM_RULES and ~/.todobem/rules.json")
	cacheDir := flag.String("cache", "", "directory for the parsed-session cache (default ~/.todobem/cache or $TODOBEM_CACHE; \"off\" disables)")
	authPath := flag.String("auth", auth.DefaultKeyPath(), "auth key file that gates the UI (default ~/.todobem/auth.key or $TODOBEM_AUTH; \"off\" leaves the UI open)")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "usage: todobem [flags]          serve the UI\n       todobem -agent [flags]   run as an agent for a hub (headless; see docs/AGENT-MODE.md)\n       todobem token [flags]    mint a one-time login link (see: todobem token -h)\n       todobem agent pair       mint a pairing string for the hub (on an agent host)\n       todobem hub <cmd>        add | list | remove | doctor | rotate paired agents (on the hub)\n       todobem cache [flags]    show or prune the parsed-session cache (see: todobem cache -h)\n       todobem unknown [flags]  unmatched commands and telemetry gaps across sessions (see: todobem unknown -h)\n\nflags:\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	if *version {
		fmt.Println(fleet.VersionLine())
		return
	}
	if *addr == "" {
		*addr = "127.0.0.1:7788"
		if *agentMode {
			*addr = ":" + fleet.DefaultPort
		}
	}
	flag.Visit(func(f *flag.Flag) {
		if *agentMode && (f.Name == "auth" || f.Name == "open") {
			log.Fatal("agent mode: -auth and -open do not apply (there is no UI; the bearer is the gate)")
		}
	})
	if abs, err := filepath.Abs(*settingsPath); err == nil && *settingsPath != "" {
		*settingsPath = abs // the page names the file; a relative flag value would name it relative to a cwd nobody sees
	}
	homes, err := resolveHomes(*codexHome, *claudeHome, *settingsPath)
	if err != nil {
		log.Fatalf("session folders: %v", err)
	}

	// Second detection source: a user overlay of project-specific commands and review-skill names,
	// merged into the built-in table before any session is parsed. A malformed config fails loudly.
	if loaded, err := classify.LoadUserConfigDefaults(*rules); err != nil {
		log.Fatalf("loading user rules: %v", err)
	} else if len(loaded) > 0 {
		fmt.Printf("loaded user rules: %v\n", loaded)
	}

	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatal(err)
	}
	cache := resolveCacheDir(*cacheDir)
	if *agentMode {
		os.Exit(runAgent(*addr, *agentKey, homes, cache))
	}
	srv := server.NewWithCache(homes.Homes, sub, cache)
	srv.ConfigureSettings(*settingsPath, homes.Typed, homes.Pinned)
	go srv.Scan()
	// The hub side of agent mode: the paired agents' sessions join the list. The file may not
	// exist yet; `todobem hub add` creates it and a running hub notices.
	fl := fleet.New(fleet.DefaultAgentsPath(), fleet.DefaultSnapshotDir())
	srv.SetFleet(fl)
	if err := fl.Start(); err != nil {
		log.Fatalf("fleet: %v", err)
	}

	url := "http://" + *addr + "/"
	if cache != "" {
		n, size := store.New(cache).Size()
		fmt.Printf("session cache: %s (%d sessions, %.1f MB; `todobem cache -prune` drops entries of other session folders)\n", cache, n, float64(size)/1e6)
	}
	// The UI is gated by default: the key file is the root of trust, `todobem token` mints a
	// one-time login link from it. The token itself is never printed by the server (its stdout
	// may be a log file); with -open it goes straight into the browser.
	var key []byte
	if *authPath != "off" {
		var err error
		key, err = auth.LoadOrCreateKey(*authPath)
		if err != nil {
			log.Fatalf("auth key: %v", err)
		}
		srv.SetAuth(auth.NewFileVerifier(*authPath, time.Now()))
		fmt.Printf("auth: key %s · run `todobem token` for a login link\n", *authPath)
	} else {
		fmt.Println("auth: OFF (-auth=off) · anyone who can reach this port sees every session")
	}
	if names := fl.Names(); len(names) > 0 {
		fmt.Printf("fleet: %d agents (%s): %s · this hub dials them and nothing else\n", len(names), fleet.DefaultAgentsPath(), strings.Join(names, ", "))
	}
	fmt.Printf("todobem %s listening on %s\n%s\n", fleet.BuildVersion(), url, homes.Describe())
	if *openBrowser {
		open := url
		if key != nil {
			if tok, err := auth.MintToken(key, auth.DefaultTTL, time.Now()); err == nil {
				open = url + "#token=" + tok
			}
		}
		go func() {
			time.Sleep(300 * time.Millisecond)
			var cmd *exec.Cmd
			switch runtime.GOOS {
			case "darwin":
				cmd = exec.Command("open", open)
			case "linux":
				cmd = exec.Command("xdg-open", open)
			default:
				return
			}
			_ = cmd.Start()
		}()
	}
	log.Fatal(http.ListenAndServe(*addr, srv.Handler(*addr)))
}

// runAgent is `todobem -agent`: the same server, headless, answering a hub over pinned TLS on
// addr. It prints one pairing string at start (single use, five minutes) and logs from then on.
func runAgent(addr, keyPath string, homes sessionHomes, cache string) int {
	boot := time.Now()
	certPath, tlsKeyPath := fleet.CertPaths(keyPath)
	cert, pin, err := fleet.LoadOrCreateCert(certPath, tlsKeyPath)
	if err != nil {
		log.Printf("agent: %v", err)
		return 1
	}
	srv := server.NewWithCache(homes.Homes, nil, cache)
	go srv.Scan() // the port opens now; the first request waits for the scan like the hub's does
	handler, err := srv.AgentHandler(keyPath, boot)
	if err != nil {
		log.Printf("agent: %v", err)
		return 1
	}
	pairing, err := server.PairingString(keyPath, pin, portOf(addr), false, server.AgentBearerTTL, time.Now())
	if err != nil {
		log.Printf("agent: %v", err)
		return 1
	}
	if cache != "" {
		n, size := store.New(cache).Size()
		log.Printf("agent: session cache %s (%d sessions, %.1f MB)", cache, n, float64(size)/1e6)
	}
	log.Printf("agent: todobem %s · %s", fleet.BuildVersion(), homes.Describe())
	log.Printf("agent: listening on %s (TLS, pin sha256:%s…) · key %s · no UI, no outbound connection", addr, pin[:16], keyPath)
	log.Printf("agent: pair this host from the hub within %s (one use):\n  todobem hub add '%s'\n  later: todobem agent pair", auth.TokenWindow, pairing)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Printf("agent: %v", err)
		return 1
	}
	hs := &http.Server{
		Handler:           handler,
		TLSConfig:         fleet.ServerTLS(cert),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
	}
	if err := hs.ServeTLS(ln, "", ""); err != nil {
		log.Printf("agent: %v", err)
		return 1
	}
	return 0
}

// runToken is `todobem token`: mint a one-time login link for the UI from the key file. Being
// able to read the key file (0600, same user) is what authorises minting; no server contact.
func runToken(args []string) int {
	fs := flag.NewFlagSet("todobem token", flag.ContinueOnError)
	addr := fs.String("addr", "127.0.0.1:7788", "address the UI is served on (for the link)")
	ttl := fs.String("ttl", "30d", "how long the browser stays logged in after using the token (e.g. 12h, 7d)")
	authPath := fs.String("auth", auth.DefaultKeyPath(), "auth key file (default ~/.todobem/auth.key or $TODOBEM_AUTH)")
	revoke := fs.Bool("revoke", false, "rotate the key first: every existing session and token stops working")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	d, err := auth.ParseTTL(*ttl)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	tok, err := auth.MintFromFile(*authPath, *revoke, d, time.Now())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if *revoke {
		fmt.Println("key rotated: every session and token is now invalid")
	}
	fmt.Printf("open:   http://%s/#token=%s\n", *addr, tok)
	fmt.Printf("token:  %s\n", tok)
	fmt.Printf("        one use, within %s · the browser then stays logged in for %s\n", auth.TokenWindow, *ttl)
	return 0
}

// runCache is `todobem cache`: report the parsed-session cache and, with -prune, delete the
// entries whose session the given codex home does not list (the server once ran against another
// home, or the rollout was deleted). The cache is derived data: a pruned entry is re-parsed on
// the next open, nothing else happens. Age is not a criterion: a cache hit never touches the
// file, so its mtime says when it was written, not when it was last useful.
func runCache(args []string) int {
	fs := flag.NewFlagSet("todobem cache", flag.ContinueOnError)
	codexHome := fs.String("codex", "", codexFlagHelp)
	claudeHome := fs.String("claude", "", claudeFlagHelp)
	settingsPath := fs.String("settings", settings.DefaultPath(), settingsFlagHelp)
	cacheDir := fs.String("cache", "", "cache directory (default ~/.todobem/cache or $TODOBEM_CACHE)")
	prune := fs.Bool("prune", false, "delete the entries of sessions the configured folders do not list, and leftover temp files")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	homes, err := resolveHomes(*codexHome, *claudeHome, *settingsPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "session folders:", err)
		return 2
	}
	dir := resolveCacheDir(*cacheDir)
	if dir == "" {
		fmt.Fprintln(os.Stderr, "cache: disabled (-cache off)")
		return 1
	}
	st := store.New(dir)
	n, size := st.Size()
	fmt.Printf("cache:  %s\n        %d sessions, %.1f MB\n", dir, n, float64(size)/1e6)
	if !*prune {
		return 0
	}
	srv := server.New(homes.Homes, nil)
	srv.Scan()
	ids := append(srv.Sources().IDs(), fleet.SnapshotIDs(fleet.DefaultSnapshotDir())...)
	removed, freed, err := st.Prune(ids)
	if err != nil {
		fmt.Fprintln(os.Stderr, "prune:", err)
	}
	left, leftSize := st.Size()
	fmt.Printf("pruned: %d entries, %.1f MB (%s list %d threads)\n        %d sessions, %.1f MB kept\n", removed, float64(freed)/1e6, strings.Join(homes.all(), ", "), len(ids), left, float64(leftSize)/1e6)
	if err != nil {
		return 1
	}
	return 0
}

// resolveCacheDir picks the parsed-session cache directory: the -cache flag ("off" disables it),
// else $TODOBEM_CACHE, else ~/.todobem/cache.
func resolveCacheDir(flagVal string) string {
	if flagVal == "off" {
		return ""
	}
	if flagVal != "" {
		return flagVal
	}
	if env := os.Getenv("TODOBEM_CACHE"); env != "" {
		return env
	}
	if d := settings.Dir(); d != "" {
		return filepath.Join(d, "cache")
	}
	return ""
}

const (
	codexFlagHelp    = "one Codex home directory (contains sessions/), pinned for this run together with -claude: only the folders given on the command line are read; default: the folders in the settings file, else ~/.codex and ~/.claude"
	claudeFlagHelp   = "one Claude Code home directory (contains projects/), pinned for this run together with -codex; see -codex"
	settingsFlagHelp = "settings file with the session folders, written by the Settings page (default ~/.todobem/settings.json or $TODOBEM_SETTINGS)"
)

// sessionHomes is the resolved answer to "which folders hold the sessions": the absolute homes
// to scan per source, the same entries as the user wrote them (for the Settings page), whether
// a flag pinned them for this run, and where they came from (for the startup line).
type sessionHomes struct {
	Homes  settings.Homes
	Typed  settings.Settings
	Pinned bool
	source string
}

// resolveHomes picks the session folders: an explicit -codex and/or -claude pins this run to
// exactly the folders given (the other source is off; the Settings page shows them read-only);
// else the settings file when it exists (an absent key is that source's default, an empty list
// turns it off); else ~/.codex and ~/.claude. A settings file that exists but cannot be read
// fails loudly — silently reading the wrong folders would be worse. Called after the flag set
// is parsed: only a flag actually given on the command line counts as explicit ("" is not).
func resolveHomes(codexFlag, claudeFlag, settingsPath string) (sessionHomes, error) {
	if codexFlag != "" || claudeFlag != "" {
		cfg := settings.Settings{CodexHomes: []string{}, ClaudeHomes: []string{}}
		if codexFlag != "" {
			cfg.CodexHomes = []string{codexFlag}
		}
		if claudeFlag != "" {
			cfg.ClaudeHomes = []string{claudeFlag}
		}
		typed, homes, err := settings.Resolve(cfg)
		if err != nil {
			return sessionHomes{}, err
		}
		return sessionHomes{Homes: homes, Typed: typed, Pinned: true, source: "command-line flags; the Settings page is read-only"}, nil
	}
	cfg, exists, err := settings.Load(settingsPath)
	if err != nil {
		return sessionHomes{}, err
	}
	typed, homes, err := settings.Resolve(cfg)
	if err != nil {
		if exists {
			return sessionHomes{}, fmt.Errorf("%s: %w", settingsPath, err)
		}
		return sessionHomes{}, err
	}
	src := settingsPath
	if !exists {
		src = "default; " + settingsPath + " absent"
	}
	return sessionHomes{Homes: homes, Typed: typed, source: src}, nil
}

// all lists every folder in effect, Codex first.
func (h sessionHomes) all() []string {
	return append(append([]string(nil), h.Homes.Codex...), h.Homes.Claude...)
}

// Describe is the startup line: the folders in effect per source and why.
func (h sessionHomes) Describe() string {
	part := func(noun string, homes []string) string {
		if len(homes) == 0 {
			return noun + ": off"
		}
		if len(homes) > 1 {
			noun += "s"
		}
		return noun + ": " + strings.Join(homes, ", ")
	}
	return fmt.Sprintf("%s · %s (%s)", part("codex home", h.Homes.Codex), part("claude home", h.Homes.Claude), h.source)
}
