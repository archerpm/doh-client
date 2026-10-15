package daemon

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/archerpm/doh-client/internal/config"
	"github.com/archerpm/doh-client/internal/doh"
	"github.com/archerpm/doh-client/internal/hosts"
	"github.com/archerpm/doh-client/internal/resolver"
	"github.com/miekg/dns"
)

type exchangeFunc func(context.Context, *dns.Msg) (*dns.Msg, error)

func (f exchangeFunc) Exchange(ctx context.Context, q *dns.Msg) (*dns.Msg, error) { return f(ctx, q) }

func testAnswer(q *dns.Msg, ip string) *dns.Msg {
	m := new(dns.Msg)
	m.SetReply(q)
	m.Answer = []dns.RR{&dns.A{Hdr: dns.RR_Header{Name: q.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60}, A: net.ParseIP(ip)}}
	return m
}

func udpReference(t *testing.T, fail bool) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ready := make(chan struct{})
	srv := &dns.Server{PacketConn: pc, NotifyStartedFunc: func() { close(ready) }, Handler: dns.HandlerFunc(func(w dns.ResponseWriter, q *dns.Msg) {
		m := testAnswer(q, "198.51.100.1")
		if fail {
			m.SetRcode(q, dns.RcodeServerFailure)
			m.Answer = nil
		}
		if err := w.WriteMsg(m); err != nil {
			t.Error(err)
		}
	})}
	go func() {
		if err := srv.ActivateAndServe(); err != nil {
			t.Error(err)
		}
	}()
	<-ready
	t.Cleanup(func() {
		if err := srv.Shutdown(); err != nil {
			t.Error(err)
		}
	})
	return pc.LocalAddr().String()
}

func TestTestDomainUsesOnlyActiveAndUDPReference(t *testing.T) {
	for _, tc := range []struct {
		name, ip, classification string
		geo, refFails            bool
	}{
		{"proxy", "192.0.2.10", "proxied", true, false},
		{"different", "192.0.2.10", "differs", false, false},
		{"real", "198.51.100.1", "real", true, false},
		{"reference unavailable", "192.0.2.10", "unknown", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			d := New(config.NewStore(filepath.Join(dir, "config.json")), filepath.Join(dir, "backup.json"))
			cfg := config.Default()
			cfg.ActiveProvider = "a"
			cfg.Providers = append(cfg.Providers, config.Provider{ID: "a", URL: "https://a.test/dns-query", Enabled: true, Geo: tc.geo})
			cfg.BootstrapDNS = []string{udpReference(t, tc.refFails)}
			if err := cfg.Normalize(); err != nil {
				t.Fatal(err)
			}
			d.setCfg(cfg)
			d.router.SetExchangerFactory(func(p config.Provider, _ *doh.Bootstrap, _ time.Duration) (resolver.Exchanger, error) {
				if p.ID != "a" {
					t.Errorf("built inactive provider %s", p.ID)
				}
				return exchangeFunc(func(_ context.Context, q *dns.Msg) (*dns.Msg, error) { return testAnswer(q, tc.ip), nil }), nil
			})
			if err := d.router.Apply(cfg); err != nil {
				t.Fatal(err)
			}
			// Stale pools of another provider must not classify this answer.
			d.poolCache = map[string]*proxyPool{"old": {addrs: map[netip.Addr]bool{netip.MustParseAddr(tc.ip): true}}}
			if tc.refFails {
				d.poolCache["a|https://a.test/dns-query"] = &proxyPool{addrs: map[netip.Addr]bool{netip.MustParseAddr(tc.ip): true}, at: time.Now()}
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			got, err := d.TestDomain(ctx, "example.test")
			if err != nil || len(got) != 1 || got[0].Provider != "a" || got[0].Classification != tc.classification {
				t.Fatalf("%v %+v", err, got)
			}
			if tc.refFails && (got[0].Proxied || got[0].Differs) {
				t.Fatal("unavailable reference produced a verdict")
			}
		})
	}
}

func TestUpdateConfigHostsRollback(t *testing.T) {
	for _, failure := range []string{"DNS", "hosts write", "partial hosts write", "router rollback", "store rollback", "hosts rollback"} {
		t.Run(failure, func(t *testing.T) {
			dir := t.TempDir()
			d := New(config.NewStore(filepath.Join(dir, "config.json")), filepath.Join(dir, "backup.json"))
			d.hostsPath = filepath.Join(dir, "hosts")
			cfg := config.Default()
			cfg.Mode, cfg.Enabled, cfg.ActiveProvider = config.ModeHosts, true, "a"
			cfg.Providers = append(cfg.Providers, config.Provider{ID: "a", URL: "https://a.test/dns-query"}, config.Provider{ID: "b", URL: "https://b.test/dns-query"})
			cfg.Rules = []config.Rule{{Domain: "example.test", Enabled: true}}
			if err := cfg.Normalize(); err != nil {
				t.Fatal(err)
			}
			if err := d.store.Save(cfg); err != nil {
				t.Fatal(err)
			}
			d.setCfg(cfg)
			d.active, d.activeMode = true, config.ModeHosts
			d.hostsSet = []string{"example.test"}
			original := hosts.Render([]byte("127.0.0.1 localhost\n"), map[string][]netip.Addr{"example.test": {netip.MustParseAddr("192.0.2.1")}})
			if err := os.WriteFile(d.hostsPath, original, 0600); err != nil {
				t.Fatal(err)
			}
			rollbackFailure := errors.New("router rollback failed")
			oldBuilds := 0
			d.router.SetExchangerFactory(func(p config.Provider, _ *doh.Bootstrap, _ time.Duration) (resolver.Exchanger, error) {
				if p.ID == "a" {
					oldBuilds++
					if failure == "hosts write" && oldBuilds > 1 {
						// Remove the injected write obstruction so snapshot restoration can succeed.
						if err := os.Remove(d.hostsPath); err != nil {
							t.Error(err)
						}
					}
					if failure == "router rollback" && oldBuilds > 1 {
						return nil, rollbackFailure
					}
				}
				return exchangeFunc(func(_ context.Context, q *dns.Msg) (*dns.Msg, error) {
					if p.ID == "b" {
						switch failure {
						case "partial hosts write":
							if err := os.WriteFile(d.hostsPath, []byte("partial"), 0600); err != nil {
								t.Error(err)
							}
						case "store rollback":
							if err := os.Remove(d.store.Path()); err != nil {
								t.Error(err)
							}
							if err := os.Mkdir(d.store.Path(), 0700); err != nil {
								t.Error(err)
							}
						case "hosts write", "hosts rollback":
							if err := os.Remove(d.hostsPath); err != nil {
								t.Error(err)
							}
							if err := os.Mkdir(d.hostsPath, 0700); err != nil {
								t.Error(err)
							}
						}
						if failure == "hosts write" {
							return testAnswer(q, "192.0.2.2"), nil
						}

						return nil, errors.New("new provider failed")
					}
					return testAnswer(q, "192.0.2.1"), nil
				}), nil
			})
			if err := d.router.Apply(cfg); err != nil {
				t.Fatal(err)
			}
			next := cfg.Clone()
			next.ActiveProvider = "b"
			err := d.UpdateConfig(next)
			if err == nil {
				t.Fatal("expected update failure")
			}
			if d.Config().ActiveProvider != "a" {
				t.Fatal("daemon config not rolled back")
			}
			if failure == "router rollback" {
				if !errors.Is(err, rollbackFailure) {
					t.Fatalf("lost rollback cause: %v", err)
				}
			} else if d.router.Config().ActiveProvider != "a" {
				t.Fatal("router not rolled back")
			}
			if failure != "store rollback" {
				stored, loadErr := d.store.Load()
				if loadErr != nil || stored.ActiveProvider != "a" {
					t.Fatalf("store not rolled back: %v %+v", loadErr, stored)
				}
			} else {
				var pe *os.PathError
				var le *os.LinkError
				if !errors.As(err, &pe) && !errors.As(err, &le) {
					t.Fatalf("lost store rollback error: %v", err)
				}
			}
			if failure != "hosts rollback" {
				data, readErr := os.ReadFile(d.hostsPath)
				if readErr != nil || !bytes.Equal(data, original) {
					t.Fatalf("hosts not restored: %v %q", readErr, data)
				}
			} else {
				var pe *os.PathError
				if !errors.As(err, &pe) {
					t.Fatalf("lost hosts rollback error: %v", err)
				}
			}
		})
	}
}

func TestDiagnoseQueriesOnlyActive(t *testing.T) {
	d, deps := diagnoseFixture(t)
	resolve := deps.resolve
	deps.resolve = func(ctx context.Context, p config.Provider, name string, typ uint16) (*dns.Msg, time.Duration, error) {
		if p.ID != d.cfg.ActiveProvider {
			t.Errorf("queried inactive provider %s", p.ID)
		}
		return resolve(ctx, p, name, typ)
	}
	got, err := d.diagnose(context.Background(), "site.test", deps)
	if err != nil {
		t.Fatal(err)
	}
	if answers := got.Stages[2].Data.(DiagnoseDNS).Providers; len(answers) != 2 {
		t.Fatalf("expected A/AAAA of one provider: %+v", answers)
	}
}
