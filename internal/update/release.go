package update

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// These names and URLs are frozen from the first release on: every installed version finds its
// successor with them.
const (
	SumsName     = "SHA256SUMS"
	MaxArchive   = 100 << 20 // bytes
	maxSums      = 64 << 10
	githubHost   = "github.com"
	fetchTimeout = 5 * time.Minute
)

// ArchiveName is the release archive of a version for an OS and architecture.
func ArchiveName(v, goos, goarch string) string {
	return fmt.Sprintf("todobem_%s_%s_%s.tar.gz", v, goos, goarch)
}

// ParseArchiveName reads the version, OS and architecture back from an archive's file name.
func ParseArchiveName(name string) (v Version, goos, goarch string, err error) {
	bad := fmt.Errorf("%q is not a todobem release archive (todobem_<version>_<os>_<arch>.tar.gz)", name)
	rest, ok := strings.CutPrefix(name, "todobem_")
	if !ok {
		return Version{}, "", "", bad
	}
	rest, ok = strings.CutSuffix(rest, ".tar.gz")
	if !ok {
		return Version{}, "", "", bad
	}
	parts := strings.Split(rest, "_")
	if len(parts) != 3 {
		return Version{}, "", "", bad
	}
	v, err = ParseVersion(parts[0])
	if err != nil {
		return Version{}, "", "", bad
	}
	return v, parts[1], parts[2], nil
}

// ParseSums reads a SHA256SUMS file: "<64 hex>  <name>" per line.
func ParseSums(b []byte) (map[string]string, error) {
	sums := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 || len(fields[0]) != 64 || strings.Trim(fields[0], "0123456789abcdef") != "" {
			return nil, fmt.Errorf("%s: malformed line %q", SumsName, line)
		}
		sums[strings.TrimPrefix(fields[1], "*")] = fields[0]
	}
	if len(sums) == 0 {
		return nil, fmt.Errorf("%s: empty", SumsName)
	}
	return sums, sc.Err()
}

// Remote is the releases of one GitHub repository ("owner/name"), reached over HTTPS only.
type Remote struct {
	Repo   string
	Client *http.Client
}

// NewRemote is a Remote with the updater's HTTP client: redirects only to GitHub's own hosts
// over HTTPS, a deadline per request.
func NewRemote(repo string) *Remote {
	return &Remote{Repo: repo, Client: &http.Client{
		Timeout: fetchTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			if req.URL.Scheme != "https" || !githubOwned(req.URL.Hostname()) {
				return fmt.Errorf("redirect to %s refused", req.URL.Host)
			}
			return nil
		},
	}}
}

// githubOwned is a host GitHub serves release files from.
func githubOwned(host string) bool {
	return host == githubHost || strings.HasSuffix(host, ".githubusercontent.com")
}

func (r *Remote) base() string { return "https://" + githubHost + "/" + r.Repo + "/releases" }

// Latest is the version of the repository's latest release: the tag named in the Location of
// the /releases/latest redirect (…/releases/tag/vX.Y.Z). Drafts and prereleases are never latest.
func (r *Remote) Latest() (Version, error) {
	c := *r.Client
	c.Timeout = 30 * time.Second
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := c.Get(r.base() + "/latest")
	if err != nil {
		return Version{}, fmt.Errorf("latest release of %s: %w", r.Repo, err)
	}
	resp.Body.Close()
	return latestFromLocation(r.Repo, resp.StatusCode, resp.Header.Get("Location"))
}

// latestFromLocation reads the tag out of the /releases/latest answer.
func latestFromLocation(repo string, status int, location string) (Version, error) {
	if status != http.StatusFound && status != http.StatusMovedPermanently && status != http.StatusSeeOther {
		return Version{}, fmt.Errorf("latest release of %s: HTTP %d (no published release?)", repo, status)
	}
	u, err := url.Parse(location)
	if err != nil {
		return Version{}, fmt.Errorf("latest release of %s: bad redirect %q", repo, location)
	}
	_, tag, ok := strings.Cut(u.Path, "/releases/tag/")
	if !ok || strings.Contains(tag, "/") {
		return Version{}, fmt.Errorf("latest release of %s: no release published yet", repo)
	}
	return ParseVersion(tag)
}

// Sums downloads and parses the SHA256SUMS of a release.
func (r *Remote) Sums(v Version) (map[string]string, error) {
	var buf bytes.Buffer
	if err := r.fetch(v, SumsName, &buf, maxSums); err != nil {
		return nil, err
	}
	return ParseSums(buf.Bytes())
}

// Download writes one file of a release to path, at most limit bytes.
func (r *Remote) Download(v Version, name, path string, limit int64) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	if err := r.fetch(v, name, f, limit); err != nil {
		f.Close()
		os.Remove(path)
		return err
	}
	return f.Close()
}

func (r *Remote) fetch(v Version, name string, w io.Writer, limit int64) error {
	u := r.base() + "/download/" + v.String() + "/" + name
	resp, err := r.Client.Get(u)
	if err != nil {
		return fmt.Errorf("download %s: %w", name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s of %s: HTTP %d", name, v, resp.StatusCode)
	}
	n, err := io.Copy(w, io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return fmt.Errorf("download %s: %w", name, err)
	}
	if n > limit {
		return fmt.Errorf("download %s: larger than %d bytes", name, limit)
	}
	return nil
}
