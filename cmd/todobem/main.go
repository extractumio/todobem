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

	"github.com/extractumio/todobem/internal/server"
)

//go:embed web
var webFS embed.FS

func main() {
	home, _ := os.UserHomeDir()
	addr := flag.String("addr", "127.0.0.1:7788", "listen address")
	codexHome := flag.String("codex", filepath.Join(home, ".codex"), "Codex home directory (contains sessions/)")
	openBrowser := flag.Bool("open", true, "open the browser on start")
	flag.Parse()

	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatal(err)
	}
	srv := server.New(*codexHome, sub)
	go srv.Scan()

	url := "http://" + *addr + "/"
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
