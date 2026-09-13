package codex

import (
	"encoding/json"
	"testing"
	"unicode/utf8"
)

func TestEmptySummariesAreJSONArray(t *testing.T) {
	b, err := json.Marshal(Summaries(NewIndex(t.TempDir()), nil))
	if err != nil || string(b) != "[]" {
		t.Fatalf("empty summaries=%s err=%v", b, err)
	}
}

func TestSummaryTitleAcceptsShortAndUnicodeIDs(t *testing.T) {
	for _, id := range []string{"r", "界界界界"} {
		t.Run(id, func(t *testing.T) {
			ix := NewIndex(t.TempDir())
			ix.files["synthetic"] = FileMeta{ThreadID: id, Path: "synthetic", Valid: true}
			got := Summaries(ix, nil)
			if len(got) != 1 || !utf8.ValidString(got[0].Title) {
				t.Fatalf("invalid summary title: %+v", got)
			}
		})
	}
}
