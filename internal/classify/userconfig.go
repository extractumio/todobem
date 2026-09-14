package classify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

// UserConfig is the second detection source: a user-supplied overlay that augments the built-in
// table for a custom setup — project-specific commands, in-house CI wrappers, and the names of
// per-project skills, agent roles and document paths that pin an SDLC stage. It never edits the
// built-ins; it only appends.
//
// File format (JSON), all fields optional:
//
//	{
//	  "rules": [
//	    {"match": "seg:^myci\\b",   "phase": "test",    "kind": "in-house ci"},
//	    {"match": "deploy-thing",   "phase": "release", "kind": "deploy"},
//	    {"match": "./dev integration", "phase": "test", "kind": "integration"},
//	    {"match": "prodctl", "phase": "infra", "kind": "prodctl", "lifecycle": "operate"}
//	  ],
//	  "lifecycle": {
//	    "skills": {"review": ["audit-.*", "my-review"], "plan": ["^brainstorm$"]},
//	    "roles":  {"review": ["^pragmatic$"]},
//	    "paths":  {"design": ["(^|/)docs/DESIGN\\.md$"], "requirements": ["(^|/)SPEC\\.md$"]}
//	  }
//	}
//
// `rules` entries use the SAME match forms as the built-in table (a bare word or "word sub" for
// an exact head match, or a "seg:"/"re:"/"head:"/"headpath:" regex). They are appended after the
// built-ins, so a word rule with an existing key overrides it and a regex rule can only raise the
// matched phase (highest priority wins). An optional "lifecycle" pins the SDLC stage the command
// serves (one of the eight work stages; see lifecycle.go), otherwise the phase's default applies.
// `lifecycle.skills` / `roles` / `paths` are regexes keyed by the stage they pin, matched against
// a selected skill's name, a sub-agent's spawn role, and the path of an edited file respectively;
// they are OR-ed with the built-in matchers (only code review has one).
type UserConfig struct {
	Rules     []UserRule             `json:"rules"`
	Lifecycle LifecycleMatcherConfig `json:"lifecycle"`
}

// UserRule is an overlay rule row: a built-in Rule plus an optional lifecycle pin for its kind.
type UserRule struct {
	Rule
	Lifecycle Lifecycle `json:"lifecycle,omitempty"`
}

// LifecycleMatcherConfig holds the overlay's stage matchers, keyed by lifecycle value.
type LifecycleMatcherConfig struct {
	Skills map[Lifecycle][]string `json:"skills"`
	Roles  map[Lifecycle][]string `json:"roles"`
	Paths  map[Lifecycle][]string `json:"paths"`
}

var validPhases = map[Phase]bool{
	LLM: true, Code: true, Build: true, Test: true, Release: true, Infra: true,
	WaitWorker: true, WaitUser: true, Idle: true, Compaction: true, NoTelemetry: true, Unknown: true,
}

// LoadUserConfig reads a JSON overlay from path, validates every entry, and merges it into the
// live rule set and review-skill matchers. It returns an error (and changes nothing) if the file
// is malformed, a phase is unknown, a match/regex is empty or does not compile — a bad custom
// config must fail loudly, never silently misclassify. A path that does not exist is not an error.
func LoadUserConfig(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF}) // tolerate a UTF-8 BOM
	var cfg UserConfig
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return ApplyUserConfig(cfg, path)
}

// ApplyUserConfig validates cfg and merges it. Exposed separately so callers can build a config
// in memory (tests, other adapters) without a file.
func ApplyUserConfig(cfg UserConfig, source string) error {
	rules := make([]UserRule, 0, len(cfg.Rules))
	for i, r := range cfg.Rules {
		if r.Match == "" {
			return fmt.Errorf("%s: rule %d has an empty match", source, i)
		}
		if !validPhases[r.Phase] {
			return fmt.Errorf("%s: rule %d (%q) has unknown phase %q", source, i, r.Match, r.Phase)
		}
		if r.Lifecycle != "" && !IsWorkLifecycle(r.Lifecycle) {
			return fmt.Errorf("%s: rule %d (%q) has unknown lifecycle %q", source, i, r.Match, r.Lifecycle)
		}
		for _, pfx := range []string{"re:", "seg:", "head:", "headpath:"} {
			if len(r.Match) > len(pfx) && r.Match[:len(pfx)] == pfx {
				if _, err := regexp.Compile(r.Match[len(pfx):]); err != nil {
					return fmt.Errorf("%s: rule %d (%q) has an invalid regex: %w", source, i, r.Match, err)
				}
			}
		}
		if r.Kind == "" {
			r.Kind = string(r.Phase)
		}
		rules = append(rules, r)
	}
	compileSet := func(field string, raw map[Lifecycle][]string) (map[Lifecycle][]*regexp.Regexp, error) {
		out := map[Lifecycle][]*regexp.Regexp{}
		for lc, pats := range raw {
			if !IsWorkLifecycle(lc) {
				return nil, fmt.Errorf("%s: lifecycle.%s has unknown lifecycle %q", source, field, lc)
			}
			for i, pat := range pats {
				if pat == "" {
					return nil, fmt.Errorf("%s: lifecycle.%s.%s[%d] is empty", source, field, lc, i)
				}
				re, err := regexp.Compile("(?i)" + pat)
				if err != nil {
					return nil, fmt.Errorf("%s: lifecycle.%s.%s[%d] (%q) is an invalid regex: %w", source, field, lc, i, pat, err)
				}
				out[lc] = append(out[lc], re)
			}
		}
		return out, nil
	}
	skills, err := compileSet("skills", cfg.Lifecycle.Skills)
	if err != nil {
		return err
	}
	roles, err := compileSet("roles", cfg.Lifecycle.Roles)
	if err != nil {
		return err
	}
	paths, err := compileSet("paths", cfg.Lifecycle.Paths)
	if err != nil {
		return err
	}
	// Commit only after every entry validated.
	for _, r := range rules {
		Rules = append(Rules, r.Rule)
		if r.Lifecycle != "" {
			LifecyclePins[r.Kind] = r.Lifecycle
		}
	}
	for _, lc := range WorkLifecycles { // fixed order keeps /api/rules and the fingerprint stable
		lifecycleSkills[lc] = append(lifecycleSkills[lc], skills[lc]...)
		lifecycleRoles[lc] = append(lifecycleRoles[lc], roles[lc]...)
		lifecyclePaths[lc] = append(lifecyclePaths[lc], paths[lc]...)
	}
	compileRules()
	return nil
}

// LoadUserConfigDefaults loads, in order, the explicit path (if any), then the standard
// locations, merging each that exists: $TODOBEM_RULES, then ~/.todobem/rules.json. It returns the
// paths actually loaded and the first error encountered.
func LoadUserConfigDefaults(explicit string) ([]string, error) {
	var paths []string
	if explicit != "" {
		paths = append(paths, explicit)
	}
	if env := os.Getenv("TODOBEM_RULES"); env != "" {
		paths = append(paths, env)
	}
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(home, ".todobem", "rules.json"))
	}
	var loaded []string
	for _, p := range paths {
		if _, err := os.Stat(p); err != nil {
			continue
		}
		if err := LoadUserConfig(p); err != nil {
			return loaded, err
		}
		loaded = append(loaded, p)
	}
	return loaded, nil
}
