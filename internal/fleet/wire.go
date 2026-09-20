// Package fleet is the hub side of agent mode and the protocol both sides speak: a hub (an
// ordinary todobem with a UI) pulls the session lists, models and facts of the agents its owner
// paired (`todobem -agent`, headless) over `/agent/v1/`, TLS pinned at pairing, a bearer on every
// request. This file holds the wire types — the one normative description is
// docs/AGENT-MODE.md §6; the agent's handlers live in internal/server/agent.go.
package fleet

import (
	"github.com/extractumio/todobem/internal/insights"
	"github.com/extractumio/todobem/internal/model"
)

// Protocol is the version in the path: /agent/v1/. Within v1 payloads only grow and a field
// never changes meaning; a breaking change is v2.
const (
	Protocol    = 1
	BasePath    = "/agent/v1/"
	DefaultPort = "7789"
)

// Hello is who an agent is and what it speaks (GET hello, and inside a pair answer).
type Hello struct {
	Protocol         int        `json:"protocol"`
	Version          string     `json:"version"` // the build: vcs revision, "+dirty", or "dev"
	OS               string     `json:"os"`
	Arch             string     `json:"arch"`
	Hostname         string     `json:"hostname"`
	Started          int64      `json:"started"` // ms, the agent's clock
	Now              int64      `json:"now"`
	CacheVersion     int        `json:"cache_version"` // store.CacheVersion(): models comparable only when equal
	FactsVersion     int        `json:"facts_version"` // insights.FactsVersion: facts likewise
	RulesFingerprint string     `json:"rules_fingerprint"`
	Homes            []HomeInfo `json:"homes"`
	Roots            int        `json:"roots"`
	Digest           Digest     `json:"digest"`
}

// HomeInfo is one configured session folder of the agent as its index sees it.
type HomeInfo struct {
	Source   string `json:"source"`
	Path     string `json:"path"`
	Status   string `json:"status"`
	Sessions int    `json:"sessions"`
}

// Digest is the agent's background parse of closed sessions: how many of the roots have facts.
type Digest struct {
	Done  int `json:"done"`
	Total int `json:"total"`
}

// SessionsPage is the session list as a delta (GET sessions?since=<cursor>): the rows changed
// since the cursor, or every row when Full; Count and IDsHash let the hub prove its view whole.
type SessionsPage struct {
	Cursor  string                 `json:"cursor"`
	Full    bool                   `json:"full"`
	Rows    []model.SessionSummary `json:"rows"`
	Count   int                    `json:"count"`
	IDsHash string                 `json:"ids_hash"`
}

// FactsRequest asks for the facts of up to MaxFactsIDs sessions (POST facts).
type FactsRequest struct {
	IDs []string `json:"ids"`
}

// MaxFactsIDs bounds one facts request.
const MaxFactsIDs = 100

// FactsPage answers a FactsRequest: the facts the agent has (closed, digested sessions), the
// ids it does not have yet (live, or not digested — ask again later) and the ids it does not
// know at all (deleted — drop the row).
type FactsPage struct {
	FactsVersion int              `json:"facts_version"`
	Facts        []insights.Facts `json:"facts"`
	Pending      []string         `json:"pending"`
	Unknown      []string         `json:"unknown"`
}

// PairRequest redeems a one-time pairing token (POST pair).
type PairRequest struct {
	Token string `json:"token"`
}

// PairResponse is the bearer the token granted and the agent's hello.
type PairResponse struct {
	Bearer  string `json:"bearer"`
	Expires int64  `json:"expires"` // ms
	Hello   Hello  `json:"hello"`
}

// RotateResponse is the bearer under the staged key (POST rotate, two-phase: §6.2).
type RotateResponse struct {
	Bearer  string `json:"bearer"`
	Expires int64  `json:"expires"`
}

// ErrorBody is every error answer: {"error": "auth" | "bad_request" | …, "reason": …}.
type ErrorBody struct {
	Error  string `json:"error"`
	Reason string `json:"reason,omitempty"`
}
