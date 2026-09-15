// Package codex reads OpenAI Codex CLI rollout files (~/.codex/sessions/**/rollout-*.jsonl)
// and turns them into the normalized model (internal/model) through the shared joiner
// (internal/source): Index is the source, laneParser the parser of one rollout file.
package codex

import (
	"bytes"
	"encoding/json"
)

// Line types that carry nothing the timeline needs. They are dropped by looking at the
// line prefix only, so multi-megabyte `compacted` lines are never JSON-decoded.
var skipTypes = map[string]bool{
	"compacted":                          true,
	"world_state":                        true,
	"inter_agent_communication_metadata": true,
}

// rawLine is the envelope of every rollout line.
type rawLine struct {
	Timestamp string          `json:"timestamp"`
	Ordinal   int64           `json:"ordinal"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

// lineType extracts the top-level "type" value from the line prefix without decoding.
// Rollout lines always start with {"timestamp":"…","ordinal":N,"type":"…" (ordinal may be absent
// in older versions), so scanning the first ~120 bytes is enough.
func lineType(line []byte) string {
	head := line
	if len(head) > 160 {
		head = head[:160]
	}
	i := bytes.Index(head, []byte(`"type":"`))
	if i < 0 {
		return ""
	}
	rest := head[i+8:]
	j := bytes.IndexByte(rest, '"')
	if j < 0 {
		return ""
	}
	return string(rest[:j])
}

// eventMsgType extracts payload.type for event_msg / response_item lines (prefix scan).
func payloadType(line []byte) string {
	head := line
	if len(head) > 240 {
		head = head[:240]
	}
	i := bytes.Index(head, []byte(`"payload":{"type":"`))
	if i < 0 {
		return ""
	}
	rest := line[i+19:]
	j := bytes.IndexByte(rest, '"')
	if j < 0 {
		return ""
	}
	return string(rest[:j])
}

// prefixField finds a short string field ("call_id", "ordinal"…) in the first n bytes.
func prefixField(line []byte, key string, n int) string {
	head := line
	if len(head) > n {
		head = head[:n]
	}
	k := []byte(`"` + key + `":`)
	i := bytes.Index(head, k)
	if i < 0 {
		return ""
	}
	rest := head[i+len(k):]
	if len(rest) > 0 && rest[0] == '"' {
		rest = rest[1:]
		j := bytes.IndexByte(rest, '"')
		if j < 0 {
			return ""
		}
		return string(rest[:j])
	}
	j := 0
	for j < len(rest) && (rest[j] >= '0' && rest[j] <= '9' || rest[j] == '-') {
		j++
	}
	return string(rest[:j])
}

// skipLine is the TailReader predicate: a line whose type is in skipTypes is never accumulated.
func skipLine(head []byte) bool { return skipTypes[lineType(head)] }
