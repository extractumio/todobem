package insights

import "testing"

func TestUnknownHeadSkipsEnvAssignments(t *testing.T) {
	for in, want := range map[string]string{
		"S=/tmp/x go run -overlay x ./cmd/probe": "go",
		"env FOO=1 mytool --flag":                "mytool",
		"mytool":                                 "mytool",
		"FOO=bar":                                "FOO=bar",
		"":                                       "",
		"export PATH=\"$HOME/bin:$PATH\"; rojo test": "rojo",
	} {
		if got := unknownHead(in); got != want {
			t.Fatalf("unknownHead(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{
		"/proj\nexport PATH=\"$HOME/bin:$PATH\"; rojo test":  "test rojo test",
		"/proj\nexport PATH=\"$HOME/bin:$PATH\" ; rojo test": "test rojo test",
		"/proj\ngo test ./...":                               "test go test",
	} {
		if got := Shape("test", in); got != want {
			t.Fatalf("Shape(%q) = %q, want %q", in, got, want)
		}
	}
}
