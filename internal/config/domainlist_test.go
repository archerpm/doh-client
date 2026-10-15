package config

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseDomainList(t *testing.T) {
	in := "# header comment\n" +
		"Claude.AI\n" +
		"  example.com   # trailing comment\r\n" +
		"\n" +
		"127.0.0.1 localhost\n" +
		"193.233.112.67 chatgpt.com openai.com\n" +
		"::1 ip6-localhost\n" +
		"1.2.3.4\n" +
		"not a domain!!\n" +
		"example.com\n" + // duplicate
		"bad..name\n" +
		"sub.Example.org.\n"
	want := []string{"chatgpt.com", "claude.ai", "example.com", "openai.com", "sub.example.org"}
	if got := ParseDomainList([]byte(in)); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestParseDomainListBounded(t *testing.T) {
	var b strings.Builder
	for i := 0; i < MaxListDomains+500; i++ {
		b.WriteString("h")
		b.WriteString(strings.Repeat("x", i%11))
		b.WriteString(string(rune('a' + i%26)))
		b.WriteString(string(rune('a' + i/26%26)))
		b.WriteString(string(rune('a' + i/676%26)))
		b.WriteString(".example.com\n")
	}
	if got := ParseDomainList([]byte(b.String())); len(got) > MaxListDomains {
		t.Fatalf("%d domains, limit %d", len(got), MaxListDomains)
	}
}

func TestCovers(t *testing.T) {
	rules := map[string]bool{"claude.ai": true, "x.y.example.com": true}
	for host, want := range map[string]bool{
		"claude.ai": true, "api.claude.ai": true, "a.b.claude.ai": true, "notclaude.ai": false,
		"ai": false, "x.y.example.com": true, "y.example.com": false, "z.x.y.example.com": true, "": false,
	} {
		if got := Covers(rules, host); got != want {
			t.Errorf("Covers(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestBuiltinListsAreSafeAndRefreshed(t *testing.T) {
	ids := map[string]bool{}
	var withLists int
	for _, p := range BuiltinProviders() {
		if ids[p.ID] {
			t.Fatalf("duplicate builtin %s", p.ID)
		}
		ids[p.ID] = true
		listIDs := map[string]bool{}
		for _, l := range p.Lists {
			withLists++
			if !strings.HasPrefix(l.URL, "https://") || l.ID == "" || l.Name == "" || listIDs[l.ID] {
				t.Errorf("%s: bad list %+v", p.ID, l)
			}
			listIDs[l.ID] = true
		}
		if p.Geo && len(p.Services) == 0 {
			t.Errorf("%s: a bypass provider needs a services description", p.ID)
		}
	}
	if withLists == 0 {
		t.Fatal("no built-in provider publishes a list")
	}

	// an old saved config gets the new fields; a custom provider cannot carry list URLs
	c := Default()
	c.Providers = []Provider{
		{ID: "geohide", Name: "GeoHide", URL: "https://dns.geohide.ru/dns-query", Builtin: true, Geo: true},
		{ID: "mine", Name: "Mine", URL: "https://mine.test/dns-query", Geo: true, Enabled: true,
			Lists: []ProviderList{{ID: "x", Name: "x", URL: "https://127.0.0.1/secret"}}},
	}
	if err := c.Normalize(); err != nil {
		t.Fatal(err)
	}
	g, _ := c.Provider("geohide")
	if len(g.Lists) == 0 || len(g.Services) == 0 {
		t.Errorf("builtin metadata not refreshed: %+v", g)
	}
	m, _ := c.Provider("mine")
	if len(m.Lists) != 0 {
		t.Errorf("custom provider kept its list URLs: %+v", m.Lists)
	}
}
