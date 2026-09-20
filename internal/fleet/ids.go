package fleet

import (
	"fmt"
	"regexp"
	"strings"
)

// A remote session's id on the hub is `<uuid>@<host>`: the agent's own id plus the agent's name.
// The suffix keeps every /api/* route, the hash route and the cache working unchanged, and two
// hosts holding a copy of the same session stay two rows. Agent names are DNS-label-like and
// never contain a dot (the cache names sidecars `<id>.<kind>.json.gz`).
var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// ValidName reports whether name can name an agent.
func ValidName(name string) bool { return nameRe.MatchString(name) }

// CheckName is ValidName with the reason.
func CheckName(name string) error {
	if !ValidName(name) {
		return fmt.Errorf("invalid agent name %q: lowercase letters, digits and dashes, 1-63 characters, no dot", name)
	}
	return nil
}

// Join builds the hub id of an agent's session.
func Join(uuid, host string) string { return uuid + "@" + host }

// Split takes a hub id apart. ok is false for a local id (no `@`).
func Split(id string) (uuid, host string, ok bool) {
	i := strings.LastIndexByte(id, '@')
	if i <= 0 || i == len(id)-1 {
		return id, "", false
	}
	return id[:i], id[i+1:], true
}

// IsRemote reports whether an id names a remote session.
func IsRemote(id string) bool { _, _, ok := Split(id); return ok }

// DefaultName is the agent name an agent proposes for itself: the first label of its hostname,
// lowercased, anything else replaced by a dash.
func DefaultName(hostname string) string {
	label, _, _ := strings.Cut(strings.ToLower(hostname), ".")
	var b strings.Builder
	for _, r := range label {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	name := strings.Trim(b.String(), "-")
	if name == "" {
		return "agent"
	}
	if len(name) > 63 {
		name = name[:63]
	}
	return name
}
