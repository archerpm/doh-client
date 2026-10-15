package daemon

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/archerpm/doh-client/internal/config"
	"github.com/archerpm/doh-client/internal/doh"
	"github.com/archerpm/doh-client/internal/hosts"
	"github.com/archerpm/doh-client/internal/resolver"
	"github.com/archerpm/doh-client/internal/sysdns"
	"github.com/miekg/dns"
)

type diagnoseDial func(context.Context, string, string) (net.Conn, error)

func (f diagnoseDial) DialContext(c context.Context, n, a string) (net.Conn, error) {
	return f(c, n, a)
}

func diagnoseFixture(t *testing.T) (*Daemon, diagnoseDeps) {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Providers = []config.Provider{{ID: "a", URL: "https://a.test/dns-query", Enabled: true}, {ID: "b", URL: "https://b.test/dns-query", Enabled: true}}
	cfg.ActiveProvider = "a"
	cfg.DefaultChain = []string{"a", "b"}
	cfg.Rules = []config.Rule{{Domain: "site.test", Enabled: true}}
	d := New(config.NewStore(filepath.Join(dir, "config")), filepath.Join(dir, "backup"))
	if err := cfg.Normalize(); err != nil {
		t.Fatal(err)
	}
	d.cfg = cfg
	d.hostsPath = filepath.Join(dir, "hosts")
	d.router.SetExchangerFactory(func(config.Provider, *doh.Bootstrap, time.Duration) (resolver.Exchanger, error) { return nil, nil })
	if err := d.router.Apply(cfg); err != nil {
		t.Fatal(err)
	}
	deps := diagnoseDeps{
		resolve: func(_ context.Context, _ config.Provider, name string, typ uint16) (*dns.Msg, time.Duration, error) {
			m := new(dns.Msg)
			m.SetQuestion(dns.Fqdn(name), typ)
			m.Response = true
			if typ == dns.TypeA {
				m.Answer = []dns.RR{&dns.A{Hdr: dns.RR_Header{Rrtype: typ}, A: net.ParseIP("192.0.2.1")}}
			}
			return m, time.Millisecond, nil
		},
		inspect: func(context.Context, []string) sysdns.Inspection {
			return sysdns.Inspection{Complete: true, Expected: []string{}, Actual: []string{}, Interfaces: []sysdns.DNSInterface{}}
		},
		lookup: func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("192.0.2.1")}, nil
		},
		dialer: diagnoseDial(func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("fake TCP failure") }),
	}
	return d, deps
}

func TestDiagnoseStages(t *testing.T) {
	for _, tc := range []struct {
		name, domain, want string
		change             func(*testing.T, *Daemon, *diagnoseDeps)
	}{
		{"tcp failure", "SITE.TEST.", "tcp_failed", nil},
		{"no rule", "other.test", "no_rule", nil},
		{"os mismatch", "site.test", "os_dns_mismatch", func(t *testing.T, d *Daemon, p *diagnoseDeps) {
			p.inspect = func(context.Context, []string) sysdns.Inspection {
				return sysdns.Inspection{Complete: true, Mismatch: true}
			}
		}},
		{"unreachable", "site.test", "provider_unreachable", func(t *testing.T, d *Daemon, p *diagnoseDeps) {
			p.resolve = func(context.Context, config.Provider, string, uint16) (*dns.Msg, time.Duration, error) {
				return nil, 0, errors.New("fake DNS failure")
			}
		}},
		{"nil response", "site.test", "provider_unreachable", func(t *testing.T, d *Daemon, p *diagnoseDeps) {
			p.resolve = func(context.Context, config.Provider, string, uint16) (*dns.Msg, time.Duration, error) {
				return nil, 0, nil
			}
		}},
		{"sinkhole", "site.test", "provider_blocked", func(t *testing.T, d *Daemon, p *diagnoseDeps) {
			old := p.resolve
			p.resolve = func(c context.Context, p config.Provider, n string, typ uint16) (*dns.Msg, time.Duration, error) {
				m, lat, err := old(c, p, n, typ)
				if typ == dns.TypeA {
					m.Answer[0].(*dns.A).A = net.IPv4zero
				}
				return m, lat, err
			}
		}},
		{"empty active answer", "site.test", "no_answer", func(t *testing.T, d *Daemon, p *diagnoseDeps) {
			old := p.resolve
			p.resolve = func(c context.Context, p config.Provider, n string, typ uint16) (*dns.Msg, time.Duration, error) {
				m, lat, err := old(c, p, n, typ)
				if p.ID == "a" {
					m.Answer = nil
				}
				return m, lat, err
			}
		}},
		{"hosts exact", "site.test", "tcp_failed", func(t *testing.T, d *Daemon, p *diagnoseDeps) {
			d.cfg.Mode = config.ModeHosts
			data := hosts.Render(nil, map[string][]netip.Addr{"site.test": {netip.MustParseAddr("192.0.2.1")}})
			if err := os.WriteFile(d.hostsPath, data, 0600); err != nil {
				t.Fatal(err)
			}
		}},
		{"hosts stale", "site.test", "no_rule", func(t *testing.T, d *Daemon, p *diagnoseDeps) {
			// hosts still hold 192.0.2.9, the provider already returns 192.0.2.1
			d.cfg.Mode = config.ModeHosts
			data := hosts.Render(nil, map[string][]netip.Addr{"site.test": {netip.MustParseAddr("192.0.2.9")}})
			if err := os.WriteFile(d.hostsPath, data, 0600); err != nil {
				t.Fatal(err)
			}
			p.lookup = func(context.Context, string) ([]netip.Addr, error) {
				return []netip.Addr{netip.MustParseAddr("192.0.2.9")}, nil
			}
		}},
		{"hosts suffix insufficient", "sub.site.test", "no_rule", func(t *testing.T, d *Daemon, p *diagnoseDeps) {
			d.cfg.Mode = config.ModeHosts
			data := hosts.Render(nil, map[string][]netip.Addr{"site.test": {netip.MustParseAddr("192.0.2.1")}})
			if err := os.WriteFile(d.hostsPath, data, 0600); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, deps := diagnoseFixture(t)
			if tc.change != nil {
				tc.change(t, d, &deps)
			}
			before := d.cfg.Clone()
			stats := d.router.Stats()
			got, err := d.diagnose(context.Background(), tc.domain, deps)
			if err != nil || got.Verdict.Code != tc.want {
				t.Fatalf("%v %+v", err, got)
			}
			if len(got.Stages) != 5 {
				t.Fatalf("%+v", got.Stages)
			}
			for i, id := range []string{"rule", "chain", "dns", "os_dns", "connect"} {
				if got.Stages[i].ID != id {
					t.Fatal("stage order")
				}
			}
			if !reflect.DeepEqual(before, d.cfg) || stats != d.router.Stats() {
				t.Fatal("diagnose changed settings or stats")
			}
			if _, err := os.Stat(d.store.Path()); !os.IsNotExist(err) {
				t.Fatal("config written")
			}
		})
	}
}

func TestDiagnoseDeadlinePartial(t *testing.T) {
	d, deps := diagnoseFixture(t)
	deps.resolve = func(ctx context.Context, p config.Provider, n string, typ uint16) (*dns.Msg, time.Duration, error) {
		<-ctx.Done()
		return nil, 0, ctx.Err()
	}
	deps.lookup = func(ctx context.Context, n string) ([]netip.Addr, error) { <-ctx.Done(); return nil, ctx.Err() }
	deps.inspect = func(ctx context.Context, w []string) sysdns.Inspection {
		<-ctx.Done()
		return sysdns.Inspection{Error: "прервано"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	start := time.Now()
	got, err := d.diagnose(ctx, "site.test", deps)
	if err != nil || len(got.Stages) != 5 || time.Since(start) > time.Second {
		t.Fatalf("%v %+v", err, got)
	}
	// At the deadline a worker may already have reported cancellation, but
	// cancellation must never be presented as provider unreachability.
	if got.Verdict.Code != "inconclusive" {
		t.Fatalf("%+v", got.Verdict)
	}
}

func TestDiagnoseConnectBound(t *testing.T) {
	d, deps := diagnoseFixture(t)
	deps.resolve = func(ctx context.Context, p config.Provider, n string, typ uint16) (*dns.Msg, time.Duration, error) {
		m := new(dns.Msg)
		m.SetQuestion(n, typ)
		for i := 1; i <= 4; i++ {
			if typ == dns.TypeA {
				m.Answer = append(m.Answer, &dns.A{A: net.IPv4(192, 0, 2, byte(i))})
			} else {
				m.Answer = append(m.Answer, &dns.AAAA{AAAA: net.ParseIP("2001:db8::" + string(rune('0'+i)))})
			}
		}
		return m, 0, nil
	}
	var active, maximum atomic.Int32
	deps.dialer = diagnoseDial(func(ctx context.Context, n, a string) (net.Conn, error) {
		count := active.Add(1)
		defer active.Add(-1)
		for old := maximum.Load(); count > old; old = maximum.Load() {
			if maximum.CompareAndSwap(old, count) {
				break
			}
		}
		select {
		case <-time.After(10 * time.Millisecond):
		case <-ctx.Done():
		}
		return nil, errors.New("fake")
	})
	got, err := d.diagnose(context.Background(), "site.test", deps)
	if err != nil {
		t.Fatal(err)
	}
	probes := got.Stages[4].Data.(DiagnoseConnect).Probes
	if len(probes) != 4 || maximum.Load() > 4 {
		t.Fatalf("probes=%d max=%d", len(probes), maximum.Load())
	}
}

func TestDiagnoseValidation(t *testing.T) {
	d, deps := diagnoseFixture(t)
	for _, domain := range []string{"", "https://site.test", "a..test"} {
		if _, err := d.diagnose(context.Background(), domain, deps); err == nil {
			t.Fatal("accepted invalid domain")
		}
	}
	d.cfg = nil
	if _, err := d.diagnose(context.Background(), "site.test", deps); err == nil {
		t.Fatal("accepted unconfigured daemon")
	}
}
