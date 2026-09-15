package settings

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadMissingFileIsNotAnError(t *testing.T) {
	s, exists, err := Load(filepath.Join(t.TempDir(), "settings.json"))
	if err != nil || exists || strings.Join(s.CodexHomes, ",") != DefaultCodexHome || strings.Join(s.ClaudeHomes, ",") != DefaultClaudeHome {
		t.Fatalf("missing file must give the defaults: s=%+v exists=%v err=%v", s, exists, err)
	}
	if s, exists, err := Load(""); err != nil || exists || len(s.CodexHomes) != 1 {
		t.Fatalf("empty path: s=%+v exists=%v err=%v", s, exists, err)
	}
}

// TestLoadAbsentKeyIsTheDefaultAndEmptyListIsOff: a file written before claude_homes existed
// keeps Claude Code on (its default home); an explicit [] turns a source off.
func TestLoadAbsentKeyIsTheDefaultAndEmptyListIsOff(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	s, _, err := Load(write("old.json", `{"codex_homes": ["/srv/codex"]}`))
	if err != nil || strings.Join(s.CodexHomes, ",") != "/srv/codex" || strings.Join(s.ClaudeHomes, ",") != DefaultClaudeHome {
		t.Fatalf("absent claude_homes must be the default: %+v %v", s, err)
	}
	s, _, err = Load(write("off.json", `{"codex_homes": [], "claude_homes": ["~/.claude"]}`))
	if err != nil || len(s.CodexHomes) != 0 || strings.Join(s.ClaudeHomes, ",") != "~/.claude" {
		t.Fatalf("empty codex_homes must be off: %+v %v", s, err)
	}
	// saving writes both keys, so a source turned off stays off on the next load
	path := filepath.Join(dir, "saved.json")
	if err := Save(path, Settings{ClaudeHomes: []string{"~/.claude"}}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), `"codex_homes": []`) {
		t.Fatalf("saved file must name both keys: %s", data)
	}
	if s, _, err := Load(path); err != nil || len(s.CodexHomes) != 0 {
		t.Fatalf("a saved empty list must stay empty: %+v %v", s, err)
	}
}

func TestSaveThenLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "settings.json") // the directory is created
	want := Settings{CodexHomes: []string{"~/.codex", "/Volumes/work/codex"}, ClaudeHomes: []string{"~/.claude"}}
	if err := Save(path, want); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0600 {
		t.Errorf("mode %o, want 0600", st.Mode().Perm())
	}
	got, exists, err := Load(path)
	if err != nil || !exists || strings.Join(got.CodexHomes, ",") != strings.Join(want.CodexHomes, ",") || strings.Join(got.ClaudeHomes, ",") != "~/.claude" {
		t.Fatalf("round trip: got=%+v exists=%v err=%v", got, exists, err)
	}
	// no temp file is left behind
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatalf("leftover files: %v", entries)
	}
}

func TestLoadRejectsMalformedAndUnknownFields(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{"broken.json": `{"codex_homes": [`, "typo.json": `{"codex_home": ["~/.codex"]}`} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if _, exists, err := Load(path); err == nil || !exists {
			t.Errorf("%s: want a loud error, got exists=%v err=%v", name, exists, err)
		}
	}
}

func TestNormalize(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	typed, homes, err := Normalize([]string{"  ~/.codex ", "", "/tmp/../var/codex/"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(typed, ",") != "~/.codex,/tmp/../var/codex/" {
		t.Errorf("typed entries kept as typed (trimmed, blanks dropped): %v", typed)
	}
	if len(homes) != 2 || homes[0] != filepath.Join(home, ".codex") || homes[1] != "/var/codex" {
		t.Errorf("expanded homes: %v", homes)
	}
	for _, bad := range [][]string{
		{"relative/codex"},
		{"~/.codex", "~/.codex"},
	} {
		if _, _, err := Normalize(bad); err == nil {
			t.Errorf("%q: want an error", bad)
		}
	}
	// one list may be empty (the source is off); typed is never nil so the file names the key
	if typed, homes, err := Normalize([]string{"   "}); err != nil || typed == nil || len(typed) != 0 || len(homes) != 0 {
		t.Errorf("blank list: typed=%v homes=%v err=%v", typed, homes, err)
	}
	// but not both
	if _, _, err := Resolve(Settings{}); err == nil {
		t.Error("no folder at all must be refused")
	}
	if s, h, err := Resolve(Settings{ClaudeHomes: []string{"~/.claude"}}); err != nil || len(h.Codex) != 0 || len(h.Claude) != 1 || len(s.CodexHomes) != 0 || s.CodexHomes == nil {
		t.Errorf("resolve with codex off: %+v %+v %v", s, h, err)
	}
	// the same folder through a symlink is a duplicate
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Normalize([]string{real, link}); err == nil || !strings.Contains(err.Error(), "same folder") {
		t.Errorf("symlinked duplicate: %v", err)
	}
	// a folder that does not exist yet is allowed (an unmounted drive is a valid setting)
	if _, homes, err := Normalize([]string{filepath.Join(real, "not-yet")}); err != nil || len(homes) != 1 {
		t.Errorf("missing folder must be accepted: %v %v", homes, err)
	}
}
