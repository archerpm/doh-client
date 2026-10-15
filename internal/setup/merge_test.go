package setup

import (
	"reflect"
	"testing"

	"github.com/archerpm/doh-client/internal/config"
)

func cfg() *config.Config {
	return &config.Config{
		Providers: []config.Provider{{ID: "a", Enabled: true}, {ID: "b", Enabled: false}, {ID: "c", Enabled: true}},
		Rules:     []config.Rule{{Domain: "claude.ai", Providers: []string{"c"}, Enabled: false}},
	}
}

func TestMergeKeepsUserRules(t *testing.T) {
	c := cfg()
	n := MergeRules(c, []Group{{Domains: []string{"Claude.ai", "chatgpt.com", "chatgpt.com."}, Providers: []string{"a", "b", "zzz", "c"}}})
	if n != 1 || len(c.Rules) != 2 {
		t.Fatalf("added %d, rules %+v", n, c.Rules)
	}
	if c.Rules[0].Enabled || !reflect.DeepEqual(c.Rules[0].Providers, []string{"c"}) {
		t.Fatalf("existing rule changed: %+v", c.Rules[0])
	}
	// rules never carry providers: one tunnel serves everything
	if got := c.Rules[1]; got.Domain != "chatgpt.com" || !got.Enabled || got.Providers == nil || len(got.Providers) != 0 {
		t.Fatalf("new rule: %+v", got)
	}
}

func TestMergeIdempotent(t *testing.T) {
	c := cfg()
	g := []Group{{Domains: []string{"x.example"}, Providers: []string{"b"}}}
	if MergeRules(c, g) != 1 || MergeRules(c, g) != 0 {
		t.Fatal("second run must add nothing")
	}
	if len(c.Rules[1].Providers) != 0 {
		t.Fatalf("rules carry no providers, got %v", c.Rules[1].Providers)
	}
	if MergeRules(c, []Group{{Domains: []string{"bad domain!", ""}}}) != 0 {
		t.Fatal("invalid domains must be skipped")
	}
}
