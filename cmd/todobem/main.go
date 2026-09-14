// todobem — a local viewer for long AI-agent sessions.
package main

import (
	"embed"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/extractumio/todobem/internal/auth"
	"github.com/extractumio/todobem/internal/classify"
	"github.com/extractumio/todobem/internal/codex"
	"github.com/extractumio/todobem/internal/server"
	"github.com/extractumio/todobem/internal/store"
)

//go:embed web
var webFS embed.FS

func main() {
	if len(os.Args) > 1 && os.Args[1] == "token" {
		os.Exit(runToken(os.Args[2:]))
	}
	if len(os.Args) > 1 && os.Args[1] == "cache" {
		os.Exit(runCache(os.Args[2:]))
	}
	if len(os.Args) > 1 && os.Args[1] == "unknown" {
		os.Exit(runUnknown(os.Args[2:]))
	}
	home, _ := os.UserHomeDir()
	addr := flag.String("addr", "127.0.0.1:7788", "listen address")
	codexHome := flag.String("codex", filepath.Join(home, ".codex"), "Codex home directory (contains sessions/)")
	openBrowser := flag.Bool("open", true, "open the browser on start")
	rules := flag.String("rules", "", "path to a user rules overlay (JSON); also loads $TODOBEM_RULES and ~/.todobem/rules.json")
	cacheDir := flag.String("cache", "", "directory for the parsed-session cache (default ~/.todobem/cache or $TODOBEM_CACHE; \"off\" disables)")
	authPath := flag.String("auth", auth.DefaultKeyPath(), "auth key file that gates the UI (default ~/.todobem/auth.key or $TODOBEM_AUTH; \"off\" leaves the UI open)")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "usage: todobem [flags]          serve the UI\n       todobem token [flags]    mint a one-time login link (see: todobem token -h)\n       todobem cache [flags]    show or prune the parsed-session cache (see: todobem cache -h)\n       todobem unknown [flags]  unmatched commands and telemetry gaps across sessions (see: todobem unknown -h)\n\nflags:\n")
		flag.PrintDefaults()
	}
	flag.Parse()

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
	cache := resolveCacheDir(*cacheDir, home)
	srv := server.NewWithCache(*codexHome, sub, cache)
	go srv.Scan()

	url := "http://" + *addr + "/"
	if cache != "" {
		n, size := store.New(cache).Size()
		fmt.Printf("session cache: %s (%d sessions, %.1f MB; `todobem cache -prune` drops entries of other codex homes)\n", cache, n, float64(size)/1e6)
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
	fmt.Printf("todobem listening on %s (codex home: %s)\n", url, *codexHome)
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
	var key []byte
	if *revoke {
		key, err = auth.RotateKey(*authPath)
		if err == nil {
			fmt.Println("key rotated: every session and token is now invalid")
		}
	} else {
		key, err = auth.LoadOrCreateKey(*authPath)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "auth key:", err)
		return 1
	}
	tok, err := auth.MintToken(key, d, time.Now())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
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
	home, _ := os.UserHomeDir()
	codexHome := fs.String("codex", filepath.Join(home, ".codex"), "Codex home directory whose sessions are kept")
	cacheDir := fs.String("cache", "", "cache directory (default ~/.todobem/cache or $TODOBEM_CACHE)")
	prune := fs.Bool("prune", false, "delete the entries of sessions the codex home does not list, and leftover temp files")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	dir := resolveCacheDir(*cacheDir, home)
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
	ix := codex.NewIndex(*codexHome)
	ix.Scan()
	ids := ix.IDs()
	removed, freed, err := st.Prune(ids)
	if err != nil {
		fmt.Fprintln(os.Stderr, "prune:", err)
	}
	left, leftSize := st.Size()
	fmt.Printf("pruned: %d entries, %.1f MB (codex home %s lists %d threads)\n        %d sessions, %.1f MB kept\n", removed, float64(freed)/1e6, *codexHome, len(ids), left, float64(leftSize)/1e6)
	if err != nil {
		return 1
	}
	return 0
}

// resolveCacheDir picks the parsed-session cache directory: the -cache flag ("off" disables it),
// else $TODOBEM_CACHE, else ~/.todobem/cache.
func resolveCacheDir(flagVal, home string) string {
	if flagVal == "off" {
		return ""
	}
	if flagVal != "" {
		return flagVal
	}
	if env := os.Getenv("TODOBEM_CACHE"); env != "" {
		return env
	}
	if home != "" {
		return filepath.Join(home, ".todobem", "cache")
	}
	return ""
}
