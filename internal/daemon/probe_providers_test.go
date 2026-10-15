package daemon

import (
	"context"
	"errors"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/archerpm/doh-client/internal/config"
	"github.com/archerpm/doh-client/internal/doh"
	"github.com/archerpm/doh-client/internal/probe"
	"github.com/archerpm/doh-client/internal/resolver"
	"github.com/miekg/dns"
)

var (
	origProbeUpstream = newProbeUpstream
	origProbeReach    = probeReach
)

func TestClassifyCanary(t *testing.T) {
	ref := map[netip.Addr]bool{netip.MustParseAddr("198.51.100.1"): true}
	ip := func(s ...string) []netip.Addr {
		var out []netip.Addr
		for _, v := range s {
			out = append(out, netip.MustParseAddr(v))
		}
		return out
	}
	for _, tc := range []struct {
		name  string
		ref   map[netip.Addr]bool
		addrs []netip.Addr
		rcode int
		err   error
		want  string
	}{
		{"proxy address", ref, ip("192.0.2.10"), dns.RcodeSuccess, nil, "bypass"},
		{"real address", ref, ip("198.51.100.1"), dns.RcodeSuccess, nil, "real"},
		{"one real among proxies", ref, ip("192.0.2.10", "198.51.100.1"), dns.RcodeSuccess, nil, "real"},
		{"sinkhole", ref, ip("0.0.0.0"), dns.RcodeSuccess, nil, "blocked"},
		{"no reference", nil, ip("192.0.2.10"), dns.RcodeSuccess, nil, "noref"},
		{"transport error", ref, nil, dns.RcodeSuccess, errors.New("timeout"), "error"},
		{"servfail", ref, nil, dns.RcodeServerFailure, nil, "error"},
		{"empty answer", ref, nil, dns.RcodeSuccess, nil, "error"},
	} {
		if got := classifyCanary("x.test", tc.ref, tc.addrs, tc.rcode, tc.err, 5).Kind; got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestSummarizeProvider(t *testing.T) {
	o := func(kinds ...string) []canaryOutcome {
		var out []canaryOutcome
		for i, k := range kinds {
			out = append(out, canaryOutcome{Domain: string(rune('a' + i)), Kind: k, Err: "boom", MS: int64(10 * (i + 1))})
		}
		return out
	}
	for _, tc := range []struct {
		name, want string
		outcomes   []canaryOutcome
	}{
		{"all proxied", "works", o("bypass", "bypass", "bypass")},
		{"proxy answers but unreachable", "unreachable", o("unreachable", "unreachable", "unreachable")},
		{"half reachable", "unreachable", o("bypass", "unreachable", "real")},
		{"one reachable only", "fails", o("bypass", "real", "real")},
		{"two of three", "works", o("bypass", "bypass", "real")},
		{"one of three", "fails", o("bypass", "real", "real")},
		{"all real", "fails", o("real", "real", "real")},
		{"blocked", "fails", o("blocked", "blocked", "bypass")},
		{"partial errors", "fails", o("bypass", "error", "error")},
		{"all errors", "error", o("error", "error", "error")},
		{"no reference", "unknown", o("noref", "noref", "noref")},
		{"proxied rest no reference", "fails", o("bypass", "noref", "noref")},
	} {
		got, detail, _ := summarizeProvider(tc.outcomes)
		if got != tc.want || detail == "" {
			t.Errorf("%s: %q (%q), want %q", tc.name, got, detail, tc.want)
		}
	}
	if _, _, ms := summarizeProvider(o("bypass", "bypass", "bypass")); ms != 20 {
		t.Errorf("median latency = %d, want 20", ms)
	}
}

func TestDeclaredBypass(t *testing.T) {
	for _, tc := range []struct {
		geo            bool
		status, expect string
	}{
		{true, "works", "works"},
		{false, "works", "fails"},
		{false, "unreachable", "fails"},
		{true, "unreachable", "unreachable"},
		{false, "fails", "fails"},
		{false, "error", "error"},
		{true, "unknown", "unknown"},
	} {
		got, detail := declaredBypass(tc.geo, tc.status, "d")
		if got != tc.expect || detail == "" {
			t.Errorf("geo=%v %s: %q (%q), want %q", tc.geo, tc.status, got, detail, tc.expect)
		}
	}
}

func TestCheckReach(t *testing.T) {
	t.Cleanup(func() { probeReach = origProbeReach })
	var dialed []string
	probeReach = func(_ context.Context, provider, domain string, ip netip.Addr) probe.Connection {
		dialed = append(dialed, ip.String())
		if ip.String() == "192.0.2.2" {
			return probe.Connection{TLS: "ok"}
		}
		return probe.Connection{TLS: "failed", Error: "ошибка TLS: timeout"}
	}
	a := func(s ...string) []netip.Addr {
		var out []netip.Addr
		for _, v := range s {
			out = append(out, netip.MustParseAddr(v))
		}
		return out
	}
	// the second address connects: the bypass stands
	if got := checkReach(context.Background(), time.Second, "p", canaryOutcome{Domain: "x.test", Kind: "bypass", Addrs: a("192.0.2.1", "192.0.2.2")}); got.Kind != "bypass" {
		t.Errorf("reachable second address: %+v", got)
	}
	// none connects: unreachable, with the reason
	got := checkReach(context.Background(), time.Second, "p", canaryOutcome{Domain: "x.test", Kind: "bypass", Addrs: a("192.0.2.1", "192.0.2.3")})
	if got.Kind != "unreachable" || !strings.Contains(got.Err, "192.0.2.3") {
		t.Errorf("unreachable proxy: %+v", got)
	}
	// anything but a bypass is never dialled
	dialed = nil
	for _, k := range []string{"real", "blocked", "error", "noref"} {
		if got := checkReach(context.Background(), time.Second, "p", canaryOutcome{Domain: "x.test", Kind: k, Addrs: a("192.0.2.1")}); got.Kind != k {
			t.Errorf("%s changed to %s", k, got.Kind)
		}
	}
	if len(dialed) != 0 {
		t.Errorf("dialled %v for non-bypass outcomes", dialed)
	}
}

func probeDaemon(t *testing.T, providers ...config.Provider) *Daemon {
	t.Helper()
	dir := t.TempDir()
	d := New(config.NewStore(filepath.Join(dir, "config.json")), filepath.Join(dir, "backup.json"))
	cfg := config.Default()
	cfg.Providers = providers
	cfg.ActiveProvider = providers[0].ID
	cfg.BootstrapDNS = []string{udpReference(t, false)}
	if err := cfg.Normalize(); err != nil {
		t.Fatal(err)
	}
	d.setCfg(cfg)
	return d
}

func TestTestProvidersRefusedWhileActive(t *testing.T) {
	d := probeDaemon(t, config.Provider{ID: "a", URL: "https://a.test/dns-query", Geo: true})
	d.active = true
	called := false
	t.Cleanup(func() { newProbeUpstream = origProbeUpstream })
	newProbeUpstream = func(config.Provider, *doh.Bootstrap, time.Duration) (resolver.Exchanger, error) {
		called = true
		return nil, errors.New("must not be built")
	}
	if _, err := d.TestProviders(context.Background()); !errors.Is(err, ErrBypassActive) {
		t.Fatalf("err = %v", err)
	}
	if called {
		t.Fatal("an upstream was built while the bypass is active")
	}
}

func TestTestProviders(t *testing.T) {
	d := probeDaemon(t,
		config.Provider{ID: "good", Name: "Good", URL: "https://good.test/dns-query", Geo: true},
		config.Provider{ID: "plain", Name: "Plain", URL: "https://plain.test/dns-query"},
		config.Provider{ID: "dead", Name: "Dead", URL: "https://dead.test/dns-query", Geo: true},
		config.Provider{ID: "cdn", Name: "CDN", URL: "https://cdn.test/dns-query"},                  // differs from the reference but is no bypass provider
		config.Provider{ID: "stuck", Name: "Stuck", URL: "https://stuck.test/dns-query", Geo: true}, // proxy addresses that do not connect
	)
	t.Cleanup(func() { newProbeUpstream = origProbeUpstream; probeReach = origProbeReach })
	probeReach = func(_ context.Context, provider, _ string, _ netip.Addr) probe.Connection {
		if provider == "stuck" {
			return probe.Connection{TCP: "failed", TLS: "skipped", Error: "ошибка TCP: i/o timeout"}
		}
		return probe.Connection{TCP: "ok", TLS: "ok"}
	}
	newProbeUpstream = func(p config.Provider, _ *doh.Bootstrap, _ time.Duration) (resolver.Exchanger, error) {
		return exchangeFunc(func(_ context.Context, q *dns.Msg) (*dns.Msg, error) {
			switch p.ID {
			case "good", "cdn", "stuck":
				return testAnswer(q, "192.0.2.10"), nil
			case "plain":
				return testAnswer(q, "198.51.100.1"), nil // same as the UDP reference
			}
			return nil, errors.New("connection refused")
		}), nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got, err := d.TestProviders(ctx)
	if err != nil || len(got) < 3 {
		t.Fatalf("%v %+v", err, got)
	}
	want := map[string]struct {
		status string
		works  bool
	}{"good": {"works", true}, "plain": {"fails", false}, "dead": {"error", false}, "cdn": {"fails", false}, "stuck": {"unreachable", false}}
	seen := 0
	for _, r := range got {
		w, ok := want[r.Provider]
		if !ok {
			continue // built-in providers Normalize adds back: answered with an error here
		}
		seen++
		if r.Status != w.status || r.Works != w.works || r.Detail == "" {
			t.Errorf("%s: %+v, want %+v", r.Provider, r, w)
		}
	}
	if seen != len(want) {
		t.Fatalf("results for %d of %d test providers: %+v", seen, len(want), got)
	}
	if got[0].Provider != "good" || got[0].Name != "Good" || !got[0].Geo {
		t.Errorf("result order or fields changed: %+v", got[0])
	}
}
