package insights

import "testing"

func TestUnknownHeadSkipsEnvAssignments(t *testing.T) {
	for in, want := range map[string]string{
		"S=/tmp/x go run -overlay x ./cmd/probe": "go",
		"env FOO=1 mytool --flag":                "mytool",
		"mytool":                                 "mytool",
		"FOO=bar":                                "FOO=bar",
		"":                                       "",
	} {
		if got := unknownHead(in); got != want {
			t.Fatalf("unknownHead(%q) = %q, want %q", in, got, want)
		}
	}
}
