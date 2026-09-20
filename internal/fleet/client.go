package fleet

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/extractumio/todobem/internal/model"
)

// Client speaks to one agent: pinned TLS, the bearer on every request, JSON in and out. It is
// safe for concurrent use; SetBearer swaps the credential live (a rotation).
type Client struct {
	addr string
	http *http.Client
	mu   sync.Mutex
	bear string
}

// ErrIncompatible is an agent that does not speak this protocol (404 on a v1 route).
var ErrIncompatible = errors.New("agent does not speak protocol v1")

// ErrAuth is a refused bearer or token.
var ErrAuth = errors.New("agent refused the credential")

var errNotModified = errors.New("not modified")

const (
	shortTimeout = 30 * time.Second
	longTimeout  = 5 * time.Minute
)

// NewClient dials addr, trusting exactly the certificate with that pin.
func NewClient(addr, pin, bearer string) *Client {
	return &Client{addr: addr, bear: bearer, http: &http.Client{Transport: PinnedTransport(pin)}}
}

// SetBearer replaces the credential (after a rotation).
func (c *Client) SetBearer(b string) {
	c.mu.Lock()
	c.bear = b
	c.mu.Unlock()
}

func (c *Client) bearer() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.bear
}

// Close drops the idle connections.
func (c *Client) Close() {
	if t, ok := c.http.Transport.(*http.Transport); ok {
		t.CloseIdleConnections()
	}
}

// do sends one request with the bearer and a timeout and turns the agent's error answers into
// errors: a refused credential is ErrAuth, an unknown hello/pair route ErrIncompatible, a 304
// errNotModified (with the response). etag, when given, is sent as If-None-Match.
func (c *Client) do(method, route string, q url.Values, body any, etag string, timeout time.Duration) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	u := "https://" + c.addr + BasePath + route
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequest(method, u, rd)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if b := c.bearer(); b != "" {
		req.Header.Set("Authorization", "Bearer "+b)
	}
	if etag != "" {
		req.Header.Set("If-None-Match", strconv.Quote(etag))
	}
	req.Header.Set("Accept-Encoding", "gzip")
	client := *c.http
	client.Timeout = timeout
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	switch resp.StatusCode {
	case http.StatusOK, http.StatusNoContent:
		return resp, nil
	case http.StatusNotModified:
		return resp, errNotModified
	}
	msg, _ := readBody(resp) // the agent compresses error answers like any other
	if len(msg) > 4096 {
		msg = msg[:4096]
	}
	var eb ErrorBody
	_ = json.Unmarshal(msg, &eb)
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		if eb.Reason != "" {
			return nil, fmt.Errorf("%w (%s)", ErrAuth, eb.Reason)
		}
		return nil, ErrAuth
	case http.StatusNotFound:
		if route == "hello" || route == "pair" {
			return nil, ErrIncompatible
		}
	}
	reason := eb.Reason
	if reason == "" {
		reason = strings.TrimSpace(string(msg))
	}
	if reason == "" {
		reason = resp.Status
	}
	return nil, fmt.Errorf("agent answered %d: %s", resp.StatusCode, reason)
}

// readBody reads and closes a response body, gunzipping when the agent compressed it (the
// transport does not when the request set Accept-Encoding itself).
func readBody(resp *http.Response) ([]byte, error) {
	defer resp.Body.Close()
	var r io.Reader = resp.Body
	if strings.Contains(resp.Header.Get("Content-Encoding"), "gzip") {
		gz, err := gzip.NewReader(resp.Body)
		if err != nil {
			return nil, err
		}
		defer gz.Close()
		r = gz
	}
	return io.ReadAll(io.LimitReader(r, 256<<20))
}

// call is one JSON round trip: GET with a query, or POST with a body; v (when given) takes the
// decoded answer.
func (c *Client) call(method, route string, q url.Values, body any, timeout time.Duration, v any) error {
	resp, err := c.do(method, route, q, body, "", timeout)
	if err != nil {
		return err
	}
	b, err := readBody(resp)
	if err != nil || v == nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// Pair redeems a one-time token for a bearer (no bearer is sent).
func (c *Client) Pair(token string) (PairResponse, error) {
	var out PairResponse
	err := c.call(http.MethodPost, "pair", nil, PairRequest{Token: token}, shortTimeout, &out)
	return out, err
}

// Hello asks who the agent is.
func (c *Client) Hello() (Hello, error) {
	var h Hello
	err := c.call(http.MethodGet, "hello", nil, nil, shortTimeout, &h)
	return h, err
}

// Sessions fetches the list delta since a cursor ("" = everything).
func (c *Client) Sessions(since string) (SessionsPage, error) {
	q := url.Values{}
	if since != "" {
		q.Set("since", since)
	}
	var p SessionsPage
	err := c.call(http.MethodGet, "sessions", q, nil, longTimeout, &p)
	return p, err
}

// Model fetches a session's cache file. etag is the version the caller holds ("" = none);
// notModified is true when the agent still serves that version (no body then).
func (c *Client) Model(uuid, etag string, refresh bool) (file []byte, notModified bool, err error) {
	q := url.Values{}
	if refresh {
		q.Set("refresh", "1")
	}
	resp, err := c.do(http.MethodGet, "sessions/"+url.PathEscape(uuid), q, nil, etag, longTimeout)
	if errors.Is(err, errNotModified) {
		resp.Body.Close()
		return nil, true, nil
	}
	if err != nil {
		return nil, false, err
	}
	// the file is gzip itself: served with Content-Encoding gzip and read raw
	defer resp.Body.Close()
	var buf bytes.Buffer
	if resp.ContentLength > 0 {
		buf.Grow(int(resp.ContentLength))
	}
	_, err = buf.ReadFrom(io.LimitReader(resp.Body, 256<<20))
	return buf.Bytes(), false, err
}

// Version is the Follow-mode poll, passed through as the agent answered it.
func (c *Client) Version(uuid string) (json.RawMessage, error) {
	resp, err := c.do(http.MethodGet, "sessions/"+url.PathEscape(uuid)+"/version", nil, nil, "", longTimeout)
	if err != nil {
		return nil, err
	}
	return readBody(resp)
}

// Facts asks for the facts of up to MaxFactsIDs sessions.
func (c *Client) Facts(ids []string) (FactsPage, error) {
	var p FactsPage
	err := c.call(http.MethodPost, "facts", nil, FactsRequest{IDs: ids}, longTimeout, &p)
	return p, err
}

// Event fetches one recorded source span of a session.
func (c *Client) Event(uuid string, src model.Src) ([]byte, error) {
	q := url.Values{"session": {uuid}, "file": {src.File}, "off": {strconv.FormatInt(src.Off, 10)}, "len": {strconv.Itoa(src.Len)}}
	resp, err := c.do(http.MethodGet, "event", q, nil, "", shortTimeout)
	if err != nil {
		return nil, err
	}
	return readBody(resp)
}

// Doctor is the health report as the agent wrote it.
func (c *Client) Doctor() (json.RawMessage, error) {
	resp, err := c.do(http.MethodGet, "doctor", nil, nil, "", shortTimeout)
	if err != nil {
		return nil, err
	}
	return readBody(resp)
}

// Rotate stages a new key on the agent and returns the bearer under it. The caller persists
// the bearer before using it: the first request that carries it retires the old key.
func (c *Client) Rotate() (RotateResponse, error) {
	var out RotateResponse
	err := c.call(http.MethodPost, "rotate", nil, map[string]any{}, shortTimeout, &out)
	return out, err
}
