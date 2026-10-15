package daemon

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/archerpm/doh-client/internal/config"
	"github.com/archerpm/doh-client/internal/doh"
	"github.com/archerpm/doh-client/internal/hosts"
	"github.com/archerpm/doh-client/internal/resolver"
	"github.com/miekg/dns"
)

type slow struct{ d time.Duration }

func (s slow) Exchange(ctx context.Context, q *dns.Msg) (*dns.Msg, error) {
	select {
	case <-time.After(s.d):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	m := new(dns.Msg)
	m.SetReply(q)
	if strings.HasPrefix(q.Question[0].Name, "fail") {
		m.Answer = []dns.RR{&dns.A{Hdr: dns.RR_Header{Name: q.Question[0].Name, Rrtype: dns.TypeA, Class: 1, Ttl: 60}, A: net.IPv4zero}}
		return m, nil
	}
	m.Answer = []dns.RR{&dns.A{Hdr: dns.RR_Header{Name: q.Question[0].Name, Rrtype: dns.TypeA, Class: 1, Ttl: 60}, A: net.IPv4(9, 9, 9, 9)}}
	return m, nil
}

// Hosts mode resolves 24 domains in parallel and Status stays responsive meanwhile.
func TestHostsModeParallelAndResponsive(t *testing.T) {
	dir := t.TempDir()
	store := config.NewStore(filepath.Join(dir, "c.json"))
	c := config.Default()
	c.Mode = config.ModeHosts
	for i := 0; i < 23; i++ {
		c.Rules = append(c.Rules, config.Rule{Domain: "d" + string(rune('a'+i)) + ".test", Enabled: true})
	}
	c.Rules = append(c.Rules, config.Rule{Domain: "fail.test", Enabled: true})
	c.Normalize()
	store.Save(c)
	d := New(store, filepath.Join(dir, "b.json"))
	d.hostsPath = filepath.Join(dir, "hosts")
	os.WriteFile(d.hostsPath, []byte("127.0.0.1 localhost\n"), 0o644)
	d.router.SetExchangerFactory(func(p config.Provider, _ *doh.Bootstrap, _ time.Duration) (resolver.Exchanger, error) {
		return slow{400 * time.Millisecond}, nil
	})
	if err := d.Start(); err != nil {
		t.Fatal(err)
	}
	defer d.Stop()

	start := time.Now()
	errc := make(chan error, 1)
	go func() { errc <- d.SetEnabled(true) }()
	time.Sleep(50 * time.Millisecond)
	st0 := time.Now()
	d.Status()
	if el := time.Since(st0); el > 100*time.Millisecond {
		t.Fatalf("Status blocked %v during hosts resolution", el)
	}
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	if el := time.Since(start); el > 2500*time.Millisecond { // 24 × 400 ms sequential would be ~9.6 s
		t.Fatalf("hosts resolution not parallel: %v", el)
	}
	b, _ := os.ReadFile(d.hostsPath)
	if strings.Count(string(b), "9.9.9.9") != 23 || strings.Contains(string(b), "fail.test") {
		t.Fatalf("hosts content:\n%s", b)
	}
	st := d.Status()
	if !st.Active || len(st.HostsSet) != 23 || !strings.Contains(strings.Join(st.Warnings, " "), "fail.test") {
		t.Fatalf("status %+v", st)
	}
	if err := d.SetEnabled(false); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(d.hostsPath); string(b) != "127.0.0.1 localhost\n" {
		t.Fatalf("hosts not restored: %q", b)
	}
}

func TestEnableFailureReported(t *testing.T) {
	dir := t.TempDir()
	busy, _ := net.ListenPacket("udp", "127.0.0.1:0")
	defer busy.Close()
	store := config.NewStore(filepath.Join(dir, "c.json"))
	c := config.Default()
	c.Listen = []string{busy.LocalAddr().String()}
	c.Normalize()
	store.Save(c)
	d := New(store, filepath.Join(dir, "b.json"))
	if err := d.Start(); err != nil {
		t.Fatal(err)
	}
	defer d.Stop()
	if err := d.SetEnabled(true); err == nil {
		t.Fatal("expected bind error")
	}
	st := d.Status()
	if st.Active || st.LastError == "" {
		t.Fatalf("%+v", st)
	}
	if c2, _ := store.Load(); c2.Enabled {
		t.Fatal("failed enable must not persist enabled=true")
	}
	_ = errors.New
}

// If the original DNS can't be restored, disabling must fail and keep the local
// server answering (the OS still points at it).
func TestDisableKeepsServerWhenRestoreFails(t *testing.T) {
	dir := t.TempDir()
	l, _ := net.ListenPacket("udp", "127.0.0.1:0")
	addr := l.LocalAddr().String()
	l.Close()
	store := config.NewStore(filepath.Join(dir, "c.json"))
	c := config.Default()
	c.Listen = []string{addr}
	c.Normalize()
	store.Save(c)
	backup := filepath.Join(dir, "b.json")
	d := New(store, backup)
	d.router.SetExchangerFactory(func(p config.Provider, _ *doh.Bootstrap, _ time.Duration) (resolver.Exchanger, error) {
		return slow{time.Millisecond}, nil
	})
	if err := d.Start(); err != nil {
		t.Fatal(err)
	}
	if err := d.SetEnabled(true); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(backup, []byte("{corrupt"), 0o600) // simulate an unrestorable backup
	if err := d.SetEnabled(false); err == nil {
		t.Fatal("disable must fail when DNS can't be restored")
	}
	q := new(dns.Msg)
	q.SetQuestion("x.test.", dns.TypeA)
	if _, err := dns.Exchange(q, addr); err != nil {
		t.Fatalf("server stopped although system DNS still points at it: %v", err)
	}
	if st := d.Status(); !st.Active || st.LastError == "" {
		t.Fatalf("status %+v", st)
	}
	if c2, _ := store.Load(); !c2.Enabled {
		t.Fatal("enabled must stay persisted")
	}
	os.Remove(backup)
	d.Stop()
}

func TestHostsEmptyRulesRemovesBlock(t *testing.T) {
	dir := t.TempDir()
	store := config.NewStore(filepath.Join(dir, "c.json"))
	c := config.Default()
	c.Mode = config.ModeHosts
	c.Rules = []config.Rule{{Domain: "a.test", Enabled: true}}
	c.Normalize()
	store.Save(c)
	d := New(store, filepath.Join(dir, "b.json"))
	d.hostsPath = filepath.Join(dir, "hosts")
	os.WriteFile(d.hostsPath, []byte("127.0.0.1 localhost\n"), 0o644)
	d.router.SetExchangerFactory(func(p config.Provider, _ *doh.Bootstrap, _ time.Duration) (resolver.Exchanger, error) {
		return slow{time.Millisecond}, nil
	})
	d.Start()
	defer d.Stop()
	if err := d.SetEnabled(true); err != nil {
		t.Fatal(err)
	}
	c2 := d.Config()
	c2.Rules[0].Enabled = false
	if err := d.UpdateConfig(c2); err != nil {
		t.Fatalf("disabling the last rule must succeed: %v", err)
	}
	if b, _ := os.ReadFile(d.hostsPath); string(b) != "127.0.0.1 localhost\n" {
		t.Fatalf("stale hosts entries left: %q", b)
	}
}

// A crash in the middle of a proxy→hosts switch leaves enabled=true with a DNS
// backup: the next start must give the OS its DNS back.
func TestStartHostsModeRestoresLeftoverDNSBackup(t *testing.T) {
	dir := t.TempDir()
	store := config.NewStore(filepath.Join(dir, "c.json"))
	c := config.Default()
	c.Enabled = true
	c.Mode = config.ModeHosts
	c.Normalize()
	store.Save(c)
	backup := filepath.Join(dir, "b.json")
	os.WriteFile(backup, []byte(`{"platform":"test","items":{}}`), 0o600)
	d := New(store, backup)
	d.hostsPath = filepath.Join(dir, "hosts")
	os.WriteFile(d.hostsPath, []byte("127.0.0.1 localhost\n"), 0o644)
	d.Start()
	defer d.Stop()
	if _, err := os.Stat(backup); err == nil {
		t.Fatal("hosts mode started with a leftover DNS backup: OS still points at a dead resolver")
	}
}

// The opposite switch: stale hosts entries must not survive in proxy mode.
func TestStartProxyModeClearsLeftoverHosts(t *testing.T) {
	dir := t.TempDir()
	store := config.NewStore(filepath.Join(dir, "c.json"))
	c := config.Default()
	c.Enabled = true
	c.Listen = []string{"127.0.0.1:0"}
	c.Normalize()
	store.Save(c)
	d := New(store, filepath.Join(dir, "b.json"))
	d.hostsPath = filepath.Join(dir, "hosts")
	os.WriteFile(d.hostsPath, []byte("127.0.0.1 localhost\n"), 0o644)
	if err := hosts.Write(d.hostsPath, map[string][]netip.Addr{"old.test": {netip.MustParseAddr("192.0.2.1")}}); err != nil {
		t.Fatal(err)
	}
	d.Start()
	defer d.Stop()
	if m := hosts.Managed(d.hostsPath); len(m) != 0 {
		t.Fatalf("stale hosts entries left in proxy mode: %v", m)
	}
}

func TestFailedStartRestoresCrashedDNS(t *testing.T) {
	dir := t.TempDir()
	busy, _ := net.ListenPacket("udp", "127.0.0.1:0")
	defer busy.Close()
	store := config.NewStore(filepath.Join(dir, "c.json"))
	c := config.Default()
	c.Enabled = true // previous run was enabled and crashed
	c.Listen = []string{busy.LocalAddr().String()}
	c.Normalize()
	store.Save(c)
	backup := filepath.Join(dir, "b.json")
	os.WriteFile(backup, []byte(`{"platform":"test","items":{}}`), 0o600)
	d := New(store, backup)
	d.Start()
	defer d.Stop()
	if _, err := os.Stat(backup); err == nil {
		t.Fatal("start failed but crashed DNS redirection was not restored")
	}
}
