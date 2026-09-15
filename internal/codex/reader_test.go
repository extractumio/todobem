package codex

import "testing"

// the skipped line's head keeps what the parser needs: its type and timestamp
func TestSkippedLineHeadIsUsable(t *testing.T) {
	head := []byte(`{"timestamp":"2026-09-10T11:15:46.609Z","ordinal":5,"type":"compacted","payload":{"x":"yyyy`)
	if !skipLine(head) || lineType(head) != "compacted" || prefixTS(head) == 0 {
		t.Fatalf("head of skipped line unusable: %q", head)
	}
	if skipLine([]byte(`{"timestamp":"2026-09-10T11:15:46.609Z","type":"event_msg","payload":{}}`)) {
		t.Fatal("event_msg must not be skipped")
	}
}

func TestLineType(t *testing.T) {
	if lineType([]byte(`{"timestamp":"2026-09-10T11:15:46.609Z","ordinal":0,"type":"session_meta","payload":{}}`)) != "session_meta" {
		t.Fatal("type")
	}
	if payloadType([]byte(`{"timestamp":"x","type":"event_msg","payload":{"type":"task_started","turn_id":"1"}}`)) != "task_started" {
		t.Fatal("ptype")
	}
	if prefixTS([]byte(`{"timestamp":"2026-09-10T11:15:46.609Z","ordinal":0}`)) != 1789038946609 {
		t.Fatal("ts")
	}
}
