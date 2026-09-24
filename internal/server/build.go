package server

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"sort"
)

// BuildHeader carries the fingerprint of the UI a server serves, on every response. The page
// remembers the first value it sees and reloads itself when a later answer carries another one:
// a deploy replaces the binary under an open tab, and the tab follows without a manual reload.
const BuildHeader = "X-Todobem-Build"

// VersionHeader names the product version the server runs (internal/buildinfo: a release's
// vX.Y.Z, dev-<rev> for a build from a clone), on every response too: the page shows it in the
// sidebar and the footer, so what the browser says is what the binary is.
const VersionHeader = "X-Todobem-Version"

// buildCookie lets the scripts identify the build of the HTML document itself. Without it, the
// first API answer after a deploy could make an old document accept a new server as its own.
const buildCookie = "todobem-page-build"

// webFingerprint hashes the embedded web tree (every path and its bytes, in path order) into a
// short id: the same files give the same id across restarts, one changed byte a different one.
// A server without a UI (`todobem unknown`, `cache`) has no build id.
func webFingerprint(web fs.FS) string {
	if web == nil {
		return ""
	}
	var paths []string
	fs.WalkDir(web, ".", func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			paths = append(paths, path)
		}
		return nil
	})
	sort.Strings(paths)
	h := sha256.New()
	for _, path := range paths {
		body, err := fs.ReadFile(web, path)
		if err != nil {
			continue
		}
		h.Write([]byte(path))
		h.Write([]byte{0})
		h.Write(body)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}
