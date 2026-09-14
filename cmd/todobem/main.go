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

	"github.com/extractumio/todobem/internal/classify"
	"github.com/extractumio/todobem/internal/server"
)

//go:embed web
var webFS embed.FS

func main() {
	home, _ := os.UserHomeDir()
	addr := flag.String("addr", "127.0.0.1:7788", "listen address")
	codexHome := flag.String("codex", filepath.Join(home, ".codex"), "Codex home directory (contains sessions/)")
	openBrowser := flag.Bool("open", true, "open the browser on start")
	rules := flag.String("rules", "", "path to a user rules overlay (JSON); also loads $TODOBEM_RULES and ~/.todobem/rules.json")
	cacheDir := flag.String("cache", "", "directory for the parsed-session cache (default ~/.todobem/cache or $TODOBEM_CACHE; \"off\" disables)")
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
		fmt.Printf("session cache: %s\n", cache)
	}
	fmt.Printf("todobem listening on %s (codex home: %s)\n", url, *codexHome)
	if *openBrowser {
		go func() {
			time.Sleep(300 * time.Millisecond)
			var cmd *exec.Cmd
			switch runtime.GOOS {
			case "darwin":
				cmd = exec.Command("open", url)
			case "linux":
				cmd = exec.Command("xdg-open", url)
			default:
				return
			}
			_ = cmd.Start()
		}()
	}
	log.Fatal(http.ListenAndServe(*addr, srv.Handler(*addr)))
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
