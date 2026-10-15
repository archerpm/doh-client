package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPresetsAreValidAndUnique(t *testing.T) {
	seen := map[string]string{}
	for _, p := range Presets() {
		if p.ID == "" || p.Name == "" || len(p.Domains) == 0 {
			t.Errorf("incomplete preset %+v", p)
		}
		for _, d := range p.Domains {
			norm, err := NormalizeDomain(d)
			if err != nil || norm != d {
				t.Errorf("%s: domain %q is not a normalized valid domain (%q, %v)", p.ID, d, norm, err)
			}
			if prev, dup := seen[d]; dup {
				t.Errorf("%q is listed in both %q and %q", d, prev, p.ID)
			}
			seen[d] = p.ID
		}
	}
	// the services the client is built for must stay covered
	for _, d := range []string{"chatgpt.com", "claude.ai", "claudeusercontent.com", "gemini.google.com", "grok.com", "spotify.com", "twitch.tv", "notion.so"} {
		if _, ok := seen[d]; !ok {
			t.Errorf("%s is missing from the presets", d)
		}
	}
}

// The reference list (one host per line, "suffix:" marks a suffix) must stay
// covered: rules match a domain and all its subdomains.
func TestPresetsCoverReferenceList(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "reference-domains.txt"))
	if err != nil {
		t.Fatal(err)
	}
	var have []string
	for _, p := range Presets() {
		have = append(have, p.Domains...)
	}
	covered := func(host string) bool {
		for _, d := range have {
			if host == d || strings.HasSuffix(host, "."+d) {
				return true
			}
		}
		return false
	}
	n := 0
	for _, line := range strings.Split(string(b), "\n") {
		host := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "suffix:")))
		if host == "" {
			continue
		}
		n++
		if !covered(host) {
			t.Errorf("%s is not covered by any preset rule", host)
		}
	}
	if n < 100 {
		t.Fatalf("reference list looks truncated: %d entries", n)
	}
}

func TestDefaultRules(t *testing.T) {
	rules := DefaultRules()
	want := map[string]bool{}
	optional := map[string]bool{}
	for _, p := range Presets() {
		for _, d := range p.Domains {
			if p.Optional {
				optional[d] = true
			} else {
				want[d] = true
			}
		}
	}
	if len(optional) == 0 {
		t.Fatal("expected the ai-support preset to be optional")
	}
	for _, r := range rules {
		if optional[r.Domain] && !want[r.Domain] {
			t.Errorf("optional preset domain %q is in the default rules", r.Domain)
		}
	}
	if len(rules) != len(want) {
		t.Fatalf("%d rules for %d preset domains", len(rules), len(want))
	}
	for _, r := range rules {
		if !r.Enabled || len(r.Providers) != 0 || !want[r.Domain] {
			t.Errorf("unexpected rule %+v", r)
		}
	}
}

func TestFirstRunSeedsRulesButExistingConfigIsKept(t *testing.T) {
	dir := t.TempDir()
	fresh := NewStore(filepath.Join(dir, "fresh.json"))
	c, err := fresh.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Rules) != len(DefaultRules()) || len(c.Rules) == 0 {
		t.Fatalf("first run has %d rules, want %d", len(c.Rules), len(DefaultRules()))
	}
	if c.ActiveProvider == "" || len(c.DefaultChain) != 1 {
		t.Fatalf("single-tunnel invariant broken: %+v %v", c.ActiveProvider, c.DefaultChain)
	}

	// a saved config without rules stays without rules
	empty := NewStore(filepath.Join(dir, "empty.json"))
	cfg := Default()
	if err := cfg.Normalize(); err != nil {
		t.Fatal(err)
	}
	if err := empty.Save(cfg); err != nil {
		t.Fatal(err)
	}
	got, err := empty.Load()
	if err != nil || len(got.Rules) != 0 {
		t.Fatalf("existing config got %d rules (%v)", len(got.Rules), err)
	}
}
