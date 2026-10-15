package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeDomain(t *testing.T) {
	ok := map[string]string{"*.ChatGPT.com.": "chatgpt.com", " claude.ai ": "claude.ai", "xn--d1acufc.xn--p1ai": "xn--d1acufc.xn--p1ai"}
	for in, want := range ok {
		if got, err := NormalizeDomain(in); err != nil || got != want {
			t.Errorf("%q -> %q %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "a..b", "домен.рф", "a b.com", "*."} {
		if _, err := NormalizeDomain(bad); err == nil {
			t.Errorf("%q should fail", bad)
		}
	}
}

func TestStoreRoundTripAndBuiltinMerge(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "config.json")
	s := NewStore(p)
	c, err := s.Load()
	if err != nil || len(c.Providers) != len(BuiltinProviders()) {
		t.Fatalf("default load: %v", err)
	}
	// user disables comss and adds custom provider + rule
	for i := range c.Providers {
		if c.Providers[i].ID == "comss" {
			c.Providers[i].Enabled = false
			c.Providers[i].URL = "https://stale.example/dns-query"
		}
	}
	c.Providers = append(c.Providers, Provider{ID: "mine", URL: "https://my.doh", Enabled: true})
	c.ActiveProvider = "mine"
	c.Rules = []Rule{{Domain: "Claude.AI", Providers: []string{"mine", "ghost"}, Enabled: true}}
	if err := c.Normalize(); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(c); err != nil {
		t.Fatal(err)
	}
	c2, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	cs, _ := c2.Provider("comss")
	if cs.Enabled || cs.URL != "https://dns.comss.one/dns-query" {
		t.Fatalf("builtin merge wrong: %+v", cs)
	}
	m, _ := c2.Provider("mine")
	if m.URL != "https://my.doh/dns-query" {
		t.Fatalf("path default: %s", m.URL)
	}
	if !m.Enabled || c2.ActiveProvider != "mine" || len(c2.DefaultChain) != 1 || c2.DefaultChain[0] != "mine" {
		t.Fatalf("active provider not kept: %q %v", c2.ActiveProvider, c2.DefaultChain)
	}
	if r := c2.Rules[0]; r.Domain != "claude.ai" || len(r.Providers) != 0 {
		t.Fatalf("rule: %+v", r)
	}
}

func TestValidation(t *testing.T) {
	c := Default()
	c.Providers = append(c.Providers, Provider{ID: "x", URL: "http://insecure/dns-query"})
	if c.Normalize() == nil {
		t.Fatal("http url accepted")
	}
	c = Default()
	c.Listen = []string{"localhost:53"}
	if c.Normalize() == nil {
		t.Fatal("hostname listen accepted")
	}
	c = Default()
	c.Providers = append(c.Providers, Provider{ID: "comss2", URL: "https://a.b", Bootstrap: []string{"nope"}})
	if c.Normalize() == nil {
		t.Fatal("bad bootstrap accepted")
	}
}

func TestCorruptFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.json")
	os.WriteFile(p, []byte("{"), 0o644)
	if _, err := NewStore(p).Load(); err == nil {
		t.Fatal("corrupt config accepted")
	}
}

func TestDefaultJSONHasNoNullSlices(t *testing.T) {
	c := Default()
	if err := c.Normalize(); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(c)
	if strings.Contains(string(b), "null") {
		t.Fatalf("null in default config JSON: %s", b)
	}
}

func TestSingleActiveProviderInvariant(t *testing.T) {
	enabled := func(c *Config) (n int) {
		for _, p := range c.Providers {
			if p.Enabled {
				n++
			}
		}
		return
	}
	// legacy config: several enabled, chain without active_provider -> first chain entry wins
	c := Default()
	c.ActiveProvider = ""
	c.DefaultChain = []string{"ghost", "geohide", "comss"}
	if err := c.Normalize(); err != nil {
		t.Fatal(err)
	}
	if c.ActiveProvider != "geohide" || enabled(c) != 1 || len(c.DefaultChain) != 1 {
		t.Fatalf("migration: %q enabled=%d chain=%v", c.ActiveProvider, enabled(c), c.DefaultChain)
	}
	// Legacy migration must skip a disabled, known chain entry.
	c = Default()
	c.DefaultChain = []string{"comss", "malw"}
	for i := range c.Providers {
		if c.Providers[i].ID == "comss" {
			c.Providers[i].Enabled = false
		}
	}
	if err := c.Normalize(); err != nil || c.ActiveProvider != "malw" || enabled(c) != 1 {
		t.Fatalf("disabled chain entry: %v active=%q enabled=%d", err, c.ActiveProvider, enabled(c))
	}
	// unknown active -> falls back to a known one, never zero providers
	c = Default()
	c.ActiveProvider, c.DefaultChain = "ghost", nil
	if err := c.Normalize(); err != nil || c.ActiveProvider == "" || enabled(c) != 1 {
		t.Fatalf("unknown active: %v %q %d", err, c.ActiveProvider, enabled(c))
	}
	// no chain, nothing known -> first enabled provider
	c = Default()
	c.ActiveProvider, c.DefaultChain = "", nil
	for i := range c.Providers {
		c.Providers[i].Enabled = c.Providers[i].ID == "cloudflare"
	}
	if err := c.Normalize(); err != nil || c.ActiveProvider != "cloudflare" {
		t.Fatalf("first enabled: %v %q", err, c.ActiveProvider)
	}
	// idempotent
	before := c.ActiveProvider
	if err := c.Normalize(); err != nil || c.ActiveProvider != before || enabled(c) != 1 {
		t.Fatal("not idempotent")
	}
}
