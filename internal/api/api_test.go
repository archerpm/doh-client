package api

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/archerpm/doh-client/internal/config"
	"github.com/archerpm/doh-client/internal/daemon"
	"github.com/archerpm/doh-client/internal/doh"
	"github.com/archerpm/doh-client/internal/resolver"
	"github.com/miekg/dns"
)

func freeAddr(t *testing.T) string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

// End-to-end: local DNS server, API and client, with deterministic upstreams.
func TestDaemonAPIEndToEnd(t *testing.T) {
	dir := t.TempDir()
	store := config.NewStore(filepath.Join(dir, "config.json"))
	c := config.Default()
	dnsAddr := freeAddr(t)
	c.Listen = []string{dnsAddr}
	c.BootstrapDNS = []string{apiReferenceDNS(t)}
	c.Normalize()
	store.Save(c)
	d := daemon.New(store, filepath.Join(dir, "backup.json"))
	d.Router().SetExchangerFactory(func(config.Provider, *doh.Bootstrap, time.Duration) (resolver.Exchanger, error) {
		return apiUpstream{}, nil
	})
	if err := d.Start(); err != nil {
		t.Fatal(err)
	}
	defer d.Stop()

	apiAddr := freeAddr(t)
	tok := filepath.Join(dir, "api.token")
	s, err := NewServer(d, apiAddr, tok, CurrentOwnerID())
	if err != nil {
		t.Fatal(err)
	}
	defer diagnoseTokenCleanup(t, tok)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Serve(ctx)
	time.Sleep(100 * time.Millisecond)

	cl := NewClient(apiAddr, tok)
	// auth / host checks
	resp, _ := http.Get("http://" + apiAddr + "/v1/status")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token: %d", resp.StatusCode)
	}
	req, _ := http.NewRequest("GET", "http://"+apiAddr+"/v1/status", nil)
	req.Host = "evil.example"
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("rebinding: %d", resp.StatusCode)
	}

	// add rule, enable
	cfg, err := cl.Config()
	if err != nil {
		t.Fatal(err)
	}
	cfg.ActiveProvider = "malw-cf"
	cfg.Rules = []config.Rule{{Domain: "claude.ai", Providers: []string{"comss"}, Enabled: true}}
	if _, err := cl.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	st, err := cl.SetEnabled(true)
	if err != nil || !st.Active {
		t.Fatalf("enable: %v %+v", err, st)
	}
	if len(st.Warnings) == 0 || !strings.Contains(st.Warnings[0], "порт не 53") {
		t.Fatalf("expected non-53 warning: %v", st.Warnings)
	}

	q := new(dns.Msg)
	q.SetQuestion("claude.ai.", dns.TypeA)
	r, err := dns.Exchange(q, dnsAddr)
	if err != nil || r.Rcode != dns.RcodeSuccess || len(r.Answer) == 0 {
		t.Fatalf("query via local server: %v %v", err, r)
	}
	q.SetQuestion("claude.ai.", dns.TypeAAAA)
	if r, _ := dns.Exchange(q, dnsAddr); len(r.Answer) != 0 {
		t.Fatal("AAAA not suppressed for rule domain")
	}
	q.SetQuestion("example.com.", dns.TypeA)
	if r, err := dns.Exchange(q, dnsAddr); err != nil || len(r.Answer) == 0 {
		t.Fatalf("default chain: %v", err)
	}

	logs, err := cl.Log(0)
	if err != nil || len(logs) < 3 {
		t.Fatalf("log: %v %d", err, len(logs))
	}
	var sawRule bool
	for _, e := range logs {
		if e.Name == "claude.ai" && e.Type == "A" && e.Rule == "claude.ai" && e.Provider == "malw-cf" && len(e.Tried) == 1 {
			sawRule = true
		}
	}
	if !sawRule {
		t.Fatalf("rule routing not visible in log: %+v", logs)
	}

	res, err := cl.Test("chatgpt.com")
	if err != nil || len(res) != 1 || res[0].Provider != "malw-cf" {
		t.Fatalf("test: %v", err)
	}
	var proxied int
	for _, p := range res {
		t.Logf("%-10s %v proxied=%v blocked=%v err=%s", p.Provider, p.Addrs, p.Proxied, p.Blocked, p.Error)
		if p.Proxied {
			proxied++
		}
	}
	if proxied == 0 && res[0].Classification != "unknown" {
		t.Fatal("active provider does not proxy chatgpt.com with an available reference")
	}

	// provider tests build their own connections: refused while the bypass is on
	if _, err := cl.TestProviders(); err == nil || !strings.Contains(err.Error(), "обход включён") {
		t.Fatalf("provider test must be refused while the bypass is on: %v", err)
	}

	// bad config rejected, old config kept
	bad := cfg
	bad.Listen = []string{"nonsense"}
	if _, err := cl.SaveConfig(bad); err == nil {
		t.Fatal("bad config accepted")
	}
	if st, _ := cl.Status(); !st.Active {
		t.Fatal("daemon broken by bad config")
	}
	// persisted enabled flag
	if c2, _ := store.Load(); !c2.Enabled {
		t.Fatal("enabled not persisted")
	}
	if st, err := cl.SetEnabled(false); err != nil || st.Active {
		t.Fatal("disable failed")
	}
	if _, err := dns.Exchange(q, dnsAddr); err == nil {
		t.Fatal("server still answering after disable")
	}
}

func TestClientNoService(t *testing.T) {
	cl := NewClient(freeAddr(t), filepath.Join(t.TempDir(), "missing"))
	if _, err := cl.Status(); err != ErrNoService {
		t.Fatalf("got %v", err)
	}
}

func TestDefaultsEnforcesOneTunnel(t *testing.T) {
	w := httptest.NewRecorder()
	(&Server{}).defaults(w, httptest.NewRequest(http.MethodGet, "/v1/defaults", nil))
	var cfg config.Config
	if err := json.Unmarshal(w.Body.Bytes(), &cfg); err != nil {
		t.Fatal(err)
	}
	var enabled []string
	for _, p := range cfg.Providers {
		if p.Enabled {
			enabled = append(enabled, p.ID)
		}
	}
	if w.Code != http.StatusOK || cfg.ActiveProvider == "" || len(enabled) != 1 || enabled[0] != cfg.ActiveProvider || len(cfg.DefaultChain) != 1 || cfg.DefaultChain[0] != cfg.ActiveProvider {
		t.Fatalf("defaults: status=%d config=%+v", w.Code, cfg)
	}
}

// Provider reachability is tested separately by the live-tag DoH tests.
type apiUpstream struct{}

func (apiUpstream) Exchange(_ context.Context, q *dns.Msg) (*dns.Msg, error) {
	m := new(dns.Msg)
	m.SetReply(q)
	m.Answer = []dns.RR{&dns.A{Hdr: dns.RR_Header{Name: q.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60}, A: net.ParseIP("192.0.2.1")}}
	return m, nil
}

func apiReferenceDNS(t *testing.T) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ready := make(chan struct{})
	srv := &dns.Server{PacketConn: pc, NotifyStartedFunc: func() { close(ready) }, Handler: dns.HandlerFunc(func(w dns.ResponseWriter, q *dns.Msg) {
		m, _ := (apiUpstream{}).Exchange(context.Background(), q)
		m.Answer[0].(*dns.A).A = net.ParseIP("198.51.100.1")
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
