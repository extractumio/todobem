package fleet

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// Pairing is the pairing string taken apart: `todobem-agent://<hostname>:<port>/#<pin>.<token>`.
// The authority is the agent's own idea of its address (os.Hostname), overridable by the hub;
// the fragment carries the TLS pin (64 hex) and the one-time token (52 characters).
type Pairing struct {
	Addr  string // host:port as printed
	Pin   string
	Token string
}

const scheme = "todobem-agent"

// FormatPairing prints the pairing string.
func FormatPairing(hostname, port, pin, token string) string {
	return fmt.Sprintf("%s://%s/#%s.%s", scheme, net.JoinHostPort(hostname, port), pin, token)
}

// ParsePairing reads a pairing string; surrounding whitespace is ignored.
func ParsePairing(s string) (Pairing, error) {
	s = strings.TrimSpace(s)
	u, err := url.Parse(s)
	if err != nil || u.Scheme != scheme {
		return Pairing{}, errors.New("not a pairing string (expected todobem-agent://host:port/#pin.token)")
	}
	pin, token, ok := strings.Cut(u.Fragment, ".")
	if !ok || len(pin) != 64 || len(token) != 52 {
		return Pairing{}, errors.New("pairing string: the fragment must be <64-hex pin>.<52-char token>")
	}
	if u.Host == "" {
		return Pairing{}, errors.New("pairing string: no host")
	}
	host, port, err := net.SplitHostPort(u.Host)
	if err != nil {
		host, port = u.Host, DefaultPort
	}
	return Pairing{Addr: net.JoinHostPort(host, port), Pin: strings.ToLower(pin), Token: token}, nil
}
