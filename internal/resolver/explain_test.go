package resolver

import (
	"reflect"
	"testing"
	"time"

	"github.com/archerpm/doh-client/internal/config"
	"github.com/archerpm/doh-client/internal/doh"
)

func TestExplain(t *testing.T) {
	r := New()
	r.SetExchangerFactory(func(config.Provider, *doh.Bootstrap, time.Duration) (Exchanger, error) { return nil, nil })
	cfg := config.Default()
	cfg.Providers = []config.Provider{{ID: "a", URL: "https://a.test/dns-query", Enabled: true}, {ID: "b", URL: "https://b.test/dns-query", Enabled: true}, {ID: "off", URL: "https://off.test/dns-query"}}
	cfg.ActiveProvider = "a"
	cfg.DefaultChain = []string{"a", "b"}
	cfg.Rules = []config.Rule{{Domain: "test", Providers: []string{"b"}, Enabled: true}, {Domain: "deep.test", Providers: []string{"off", "a", "b"}, Enabled: true}, {Domain: "fallback.test", Providers: []string{"off"}, Enabled: true}, {Domain: "disabled.test", Providers: []string{"b"}}, {Domain: "default.test", Enabled: true}}
	if err := r.Apply(cfg); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		r.health.Report("a", false, time.Millisecond, nil)
	}
	before := r.Stats()
	for _, tc := range []struct {
		name, matched                  string
		configured, enabled, effective []string
		fallback                       bool
	}{
		{"X.DEEP.TEST.", "deep.test", []string{"a"}, []string{"a"}, []string{"a"}, false},
		{"fallback.test", "fallback.test", []string{"a"}, []string{"a"}, []string{"a"}, false},
		{"disabled.test", "test", []string{"a"}, []string{"a"}, []string{"a"}, false},
		{"default.test", "default.test", []string{"a"}, []string{"a"}, []string{"a"}, false},
		{"contest", "", []string{"a"}, []string{"a"}, []string{"a"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := r.Explain(tc.name)
			if e.Matched != tc.matched || !reflect.DeepEqual(e.Configured, tc.configured) || !reflect.DeepEqual(e.Enabled, tc.enabled) || !reflect.DeepEqual(e.Effective, tc.effective) || e.DefaultFallback != tc.fallback {
				t.Fatalf("%+v", e)
			}
			if e.BlockAAAA != (tc.matched != "") {
				t.Fatal("suppression")
			}
			e.Configured[0] = "mutated"
			e.Effective[0] = "mutated"
		})
	}
	if r.Stats() != before {
		t.Fatal("stats changed")
	}
	if e := New().Explain("test"); e.Matched != "" || len(e.Effective) != 0 {
		t.Fatalf("%+v", e)
	}
}
