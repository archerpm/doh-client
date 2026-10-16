package resolver

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/archerpm/doh-client/internal/config"
	"github.com/archerpm/doh-client/internal/doh"
	"github.com/miekg/dns"
)

type fake struct {
	calls atomic.Int32
	fn    func(q *dns.Msg) (*dns.Msg, error)
}

func (f *fake) Exchange(ctx context.Context, q *dns.Msg) (*dns.Msg, error) {
	f.calls.Add(1)
	return f.fn(q)
}

func aResp(q *dns.Msg, ip string, ttl uint32) *dns.Msg {
	m := new(dns.Msg)
	m.SetReply(q)
	m.Answer = append(m.Answer, &dns.A{Hdr: dns.RR_Header{Name: q.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: ttl}, A: net.ParseIP(ip)})
	return m
}

func setup(t *testing.T, fakes map[string]*fake, mut func(c *config.Config)) *Router {
	t.Helper()
	c := config.Default()
	c.Providers = nil
	for id := range fakes {
		c.Providers = append(c.Providers, config.Provider{ID: id, URL: "https://" + id + ".test/dns-query", Enabled: true})
	}
	c.ActiveProvider = "a"
	if mut != nil {
		mut(c)
	}
	if err := c.Normalize(); err != nil {
		t.Fatal(err)
	}
	r := New()
	r.newExchanger = func(p config.Provider, _ *doh.Bootstrap, _ time.Duration) (Exchanger, error) {
		if f, ok := fakes[p.ID]; ok {
			return f, nil
		}
		return &fake{fn: func(q *dns.Msg) (*dns.Msg, error) { return nil, errors.New("builtin disabled in test") }}, nil
	}
	// builtins merged by Normalize: disable them
	for i := range c.Providers {
		if c.Providers[i].Builtin {
			c.Providers[i].Enabled = false
		}
	}
	if err := r.Apply(c); err != nil {
		t.Fatal(err)
	}
	return r
}

func query(name string, t uint16) *dns.Msg {
	q := new(dns.Msg)
	q.SetQuestion(dns.Fqdn(name), t)
	q.Id = 4242
	return q
}

func TestOnlyActiveProviderIsUsed(t *testing.T) {
	a := &fake{fn: func(q *dns.Msg) (*dns.Msg, error) { return nil, errors.New("boom") }}
	b := &fake{fn: func(q *dns.Msg) (*dns.Msg, error) { return aResp(q, "1.2.3.4", 300), nil }}
	// active = a (fails): no fallback to b
	r := setup(t, map[string]*fake{"a": a, "b": b}, nil)
	m := r.Resolve(context.Background(), query("example.com", dns.TypeA), "127.0.0.1")
	if m.Rcode != dns.RcodeServerFailure || b.calls.Load() != 0 {
		t.Fatalf("must not fall back: rcode=%d b=%d", m.Rcode, b.calls.Load())
	}
	// active = b: answers, then cache serves the repeat
	r = setup(t, map[string]*fake{"a": a, "b": b}, func(c *config.Config) { c.ActiveProvider = "b" })
	m = r.Resolve(context.Background(), query("example.com", dns.TypeA), "127.0.0.1")
	if m.Rcode != dns.RcodeSuccess || len(m.Answer) != 1 || m.Id != 4242 {
		t.Fatalf("bad answer %v", m)
	}
	callsA := a.calls.Load()
	m = r.Resolve(context.Background(), query("example.com", dns.TypeA), "127.0.0.1")
	if b.calls.Load() != 1 || a.calls.Load() != callsA || m.Id != 4242 || len(m.Answer) != 1 {
		t.Fatalf("cache miss or wrong provider: b=%d", b.calls.Load())
	}
	if r.Stats().CacheHits != 1 {
		t.Fatal("cache hit not counted")
	}
}
func TestServfailIsNotCachedAndNotRetriedElsewhere(t *testing.T) {
	a := &fake{fn: func(q *dns.Msg) (*dns.Msg, error) {
		m := new(dns.Msg)
		m.SetRcode(q, dns.RcodeServerFailure)
		return m, nil
	}}
	b := &fake{fn: func(q *dns.Msg) (*dns.Msg, error) { return aResp(q, "1.2.3.4", 60), nil }}
	r := setup(t, map[string]*fake{"a": a, "b": b}, nil)
	m := r.Resolve(context.Background(), query("x.com", dns.TypeA), "c")
	if m.Rcode != dns.RcodeServerFailure || a.calls.Load() != 1 || b.calls.Load() != 0 {
		t.Fatalf("rcode=%d a=%d b=%d", m.Rcode, a.calls.Load(), b.calls.Load())
	}
	r.Resolve(context.Background(), query("x.com", dns.TypeA), "c")
	if a.calls.Load() != 2 {
		t.Fatal("servfail was cached")
	}
}
func TestNXDomainIsFinal(t *testing.T) {
	a := &fake{fn: func(q *dns.Msg) (*dns.Msg, error) {
		m := new(dns.Msg)
		m.SetRcode(q, dns.RcodeNameError)
		return m, nil
	}}
	b := &fake{fn: func(q *dns.Msg) (*dns.Msg, error) { return aResp(q, "1.1.1.1", 60), nil }}
	r := setup(t, map[string]*fake{"a": a, "b": b}, nil)
	m := r.Resolve(context.Background(), query("nope.test", dns.TypeA), "c")
	if m.Rcode != dns.RcodeNameError || b.calls.Load() != 0 {
		t.Fatal("NXDOMAIN should not fall back")
	}
}

func TestRuleRoutingSinkholeAndAAAA(t *testing.T) {
	a := &fake{fn: func(q *dns.Msg) (*dns.Msg, error) { return aResp(q, "9.9.9.9", 60), nil }}
	g := &fake{fn: func(q *dns.Msg) (*dns.Msg, error) { return aResp(q, "193.233.112.67", 60), nil }}
	r := setup(t, map[string]*fake{"a": a, "g": g}, func(c *config.Config) {
		c.ActiveProvider = "g"
		// a rule naming another provider must not pull traffic to it
		c.Rules = []config.Rule{{Domain: "*.ChatGPT.com.", Providers: []string{"a"}, Enabled: true}}
	})
	m := r.Resolve(context.Background(), query("ab.chatgpt.com", dns.TypeA), "c")
	if got := AddrsOf(m); len(got) != 1 || got[0].String() != "193.233.112.67" {
		t.Fatalf("rule answer: %v", got)
	}
	if a.calls.Load() != 0 {
		t.Fatal("a non-active provider was used")
	}
	m = r.Resolve(context.Background(), query("chatgpt.com", dns.TypeAAAA), "c")
	if m.Rcode != dns.RcodeSuccess || len(m.Answer) != 0 {
		t.Fatalf("AAAA not suppressed: %v", m)
	}
	m = r.Resolve(context.Background(), query("chatgpt.com", dns.TypeHTTPS), "c")
	if len(m.Answer) != 0 || g.calls.Load() != 1 {
		t.Fatal("HTTPS RR not suppressed")
	}
	g0 := g.calls.Load()
	r.Resolve(context.Background(), query("notchatgpt.com", dns.TypeA), "c")
	if g.calls.Load() != g0+1 {
		t.Fatal("non-rule domain not resolved by the active provider")
	}
	// a sinkhole answer is returned as-is, there is nowhere to fall back to
	sink := &fake{fn: func(q *dns.Msg) (*dns.Msg, error) { return aResp(q, "0.0.0.0", 60), nil }}
	r2 := setup(t, map[string]*fake{"a": sink, "b": a}, nil)
	m = r2.Resolve(context.Background(), query("ads.example", dns.TypeA), "c")
	if got := AddrsOf(m); len(got) != 1 || !got[0].IsUnspecified() {
		t.Fatalf("sinkhole should pass: %v", got)
	}
	if a.calls.Load() != 0 {
		t.Fatal("fell back from sinkhole")
	}
}
func TestAllSinkholeReturnsSinkhole(t *testing.T) {
	b := &fake{fn: func(q *dns.Msg) (*dns.Msg, error) { return aResp(q, "0.0.0.0", 60), nil }}
	r := setup(t, map[string]*fake{"a": b, "b": b}, func(c *config.Config) {
		c.Rules = []config.Rule{{Domain: "x.com", Enabled: true}}
	})
	m := r.Resolve(context.Background(), query("x.com", dns.TypeA), "c")
	if m.Rcode != dns.RcodeSuccess || !isSinkhole(m) || m.Id != 4242 {
		t.Fatalf("got %v", m)
	}
}

func TestOnlyActiveProviderIsBuiltAndChecked(t *testing.T) {
	a := &fake{fn: func(q *dns.Msg) (*dns.Msg, error) { return aResp(q, "1.2.3.4", 60), nil }}
	b := &fake{fn: func(q *dns.Msg) (*dns.Msg, error) { return aResp(q, "1.2.3.5", 60), nil }}
	r := setup(t, map[string]*fake{"a": a, "b": b}, nil)
	st := r.st.Load()
	if len(st.upstreams) != 1 || st.upstreams["a"] == nil || len(st.chain) != 1 {
		t.Fatalf("expected exactly one upstream, got %d chain=%v", len(st.upstreams), st.chain)
	}
	r.CheckHealth(context.Background(), "example.com")
	if b.calls.Load() != 0 {
		t.Fatal("health check touched a non-active provider")
	}
}
func TestCacheTTLDecrementAndExpiry(t *testing.T) {
	c := NewCache(2)
	now := time.Unix(1000, 0)
	c.now = func() time.Time { return now }
	q := query("a.com", dns.TypeA)
	c.Put("k", aResp(q, "1.1.1.1", 100), 0, 3600)
	now = now.Add(40 * time.Second)
	m, ok := c.Get("k")
	if !ok || m.Answer[0].Header().Ttl != 60 {
		t.Fatalf("ttl=%v ok=%v", m, ok)
	}
	now = now.Add(61 * time.Second)
	if _, ok := c.Get("k"); ok {
		t.Fatal("not expired")
	}
	// LRU eviction
	c.Put("1", aResp(q, "1.1.1.1", 100), 0, 3600)
	c.Put("2", aResp(q, "1.1.1.1", 100), 0, 3600)
	c.Get("1")
	c.Put("3", aResp(q, "1.1.1.1", 100), 0, 3600)
	if _, ok := c.Get("2"); ok {
		t.Fatal("LRU did not evict 2")
	}
	// max TTL clamps stored records
	c.Put("4", aResp(q, "1.1.1.1", 99999), 0, 300)
	m, _ = c.Get("4")
	if m.Answer[0].Header().Ttl != 300 {
		t.Fatalf("not clamped: %d", m.Answer[0].Header().Ttl)
	}
}

func TestLogRing(t *testing.T) {
	l := NewQueryLog(3)
	for i := 0; i < 5; i++ {
		l.Add(Entry{Name: string(rune('a' + i))})
	}
	got := l.After(0, 0)
	if len(got) != 3 || got[0].Name != "c" || got[2].Seq != 5 {
		t.Fatalf("%+v", got)
	}
	if got := l.After(4, 0); len(got) != 1 || got[0].Name != "e" {
		t.Fatalf("%+v", got)
	}
	l.Resize(2)
	if got := l.After(0, 0); len(got) != 2 || got[1].Name != "e" {
		t.Fatalf("resize %+v", got)
	}
	l.Add(Entry{Name: "f"})
	if got := l.After(0, 0); len(got) != 2 || got[1].Seq != 6 {
		t.Fatalf("after resize %+v", got)
	}
}

func TestBadQueries(t *testing.T) {
	a := &fake{fn: func(q *dns.Msg) (*dns.Msg, error) { return aResp(q, "1.1.1.1", 60), nil }}
	r := setup(t, map[string]*fake{"a": a, "b": a}, nil)
	q := query("a.com", dns.TypeA)
	q.Question = append(q.Question, q.Question[0])
	if m := r.Resolve(context.Background(), q, "c"); m.Rcode != dns.RcodeFormatError {
		t.Fatal("multi-question should be FORMERR")
	}
	q = query("a.com", dns.TypeA)
	q.Opcode = dns.OpcodeUpdate
	if m := r.Resolve(context.Background(), q, "c"); m.Rcode != dns.RcodeNotImplemented {
		t.Fatal("UPDATE should be NOTIMP")
	}
}

func TestSingleflightAndCase(t *testing.T) {
	gate := make(chan struct{})
	a := &fake{fn: func(q *dns.Msg) (*dns.Msg, error) { <-gate; return aResp(q, "1.2.3.4", 60), nil }}
	r := setup(t, map[string]*fake{"a": a, "b": a}, nil)
	const n = 20
	done := make(chan *dns.Msg, n)
	for i := 0; i < n; i++ {
		go func(i int) {
			q := query("MiXeD.Example.COM", dns.TypeA)
			q.Id = uint16(i)
			done <- r.Resolve(context.Background(), q, "c")
		}(i)
	}
	time.Sleep(100 * time.Millisecond)
	close(gate)
	seen := map[uint16]bool{}
	for i := 0; i < n; i++ {
		m := <-done
		if m.Question[0].Name != "MiXeD.Example.COM." || len(m.Answer) != 1 {
			t.Fatalf("bad reply %v", m)
		}
		seen[m.Id] = true
	}
	if len(seen) != n {
		t.Fatalf("ids mixed up: %d unique", len(seen))
	}
	if c := a.calls.Load(); c != 1 {
		t.Fatalf("expected 1 upstream call, got %d", c)
	}
	// cached answer mirrors the new query's case
	m := r.Resolve(context.Background(), query("mixed.EXAMPLE.com", dns.TypeA), "c")
	if m.Question[0].Name != "mixed.EXAMPLE.com." {
		t.Fatalf("cache case: %s", m.Question[0].Name)
	}
}

func TestLogCursorFromPreviousInstance(t *testing.T) {
	l := NewQueryLog(10)
	l.Add(Entry{Name: "a"})
	l.Add(Entry{Name: "b"})
	if got := l.After(500, 0); len(got) != 2 {
		t.Fatalf("stale cursor must restart from 0, got %d", len(got))
	}
}

func TestStaleInFlightAnswerNotCached(t *testing.T) {
	gate := make(chan struct{})
	a := &fake{fn: func(q *dns.Msg) (*dns.Msg, error) { <-gate; return aResp(q, "6.6.6.6", 300), nil }}
	r := setup(t, map[string]*fake{"a": a, "b": a}, nil)
	done := make(chan struct{})
	go func() { r.Resolve(context.Background(), query("s.com", dns.TypeA), "c"); close(done) }()
	time.Sleep(50 * time.Millisecond)
	r.FlushCache() // routing/cache generation changes while the query is in flight
	close(gate)
	<-done
	if r.cache.Len() != 0 {
		t.Fatal("answer from an old generation was cached")
	}
}

func TestNegativeCaching(t *testing.T) {
	c := NewCache(10)
	q := query("x.com", dns.TypeA)
	mk := func(rcode int, withSOA bool, cname uint32) *dns.Msg {
		m := new(dns.Msg)
		m.SetRcode(q, rcode)
		if cname > 0 {
			m.Answer = []dns.RR{&dns.CNAME{Hdr: dns.RR_Header{Name: "x.com.", Rrtype: dns.TypeCNAME, Class: 1, Ttl: cname}, Target: "y.com."}}
		}
		if withSOA {
			m.Ns = []dns.RR{&dns.SOA{Hdr: dns.RR_Header{Name: "com.", Rrtype: dns.TypeSOA, Class: 1, Ttl: 900}, Minttl: 60}}
		}
		return m
	}
	// NXDOMAIN behind a CNAME(600): lifetime is the SOA minimum (60), not 600
	if ttl, ok := msgTTL(mk(dns.RcodeNameError, true, 600)); !ok || ttl != 60 {
		t.Fatalf("nxdomain+cname ttl=%d ok=%v", ttl, ok)
	}
	// NODATA behind a CNAME
	if ttl, ok := msgTTL(mk(dns.RcodeSuccess, true, 30)); !ok || ttl != 30 {
		t.Fatalf("nodata+cname ttl=%d", ttl)
	}
	// negative without SOA: not cached
	c.Put("k", mk(dns.RcodeNameError, false, 0), 30, 3600)
	if c.Len() != 0 {
		t.Fatal("negative answer without SOA cached")
	}
	// TTL 0 positive: not cached even with MinTTL
	c.Put("z", aResp(q, "1.1.1.1", 0), 30, 3600)
	if c.Len() != 0 {
		t.Fatal("TTL 0 answer cached")
	}
}

func TestUpstreamQuerySanitizedAndBadVers(t *testing.T) {
	var seen *dns.Msg
	a := &fake{fn: func(q *dns.Msg) (*dns.Msg, error) { seen = q; return aResp(q, "1.1.1.1", 60), nil }}
	r := setup(t, map[string]*fake{"a": a, "b": a}, nil)
	q := query("e.com", dns.TypeA)
	q.SetEdns0(4096, true)
	o := q.IsEdns0()
	o.Option = append(o.Option, &dns.EDNS0_SUBNET{Code: dns.EDNS0SUBNET, Family: 1, SourceNetmask: 24, Address: net.ParseIP("203.0.113.0").To4()})
	r.Resolve(context.Background(), q, "c")
	so := seen.IsEdns0()
	if so == nil || !so.Do() || len(so.Option) != 0 || !seen.RecursionDesired {
		t.Fatalf("upstream query not sanitized: %v", seen)
	}
	q2 := query("e.com", dns.TypeA)
	q2.SetEdns0(1232, true) // same cache key as the answer cached above
	q2.IsEdns0().SetVersion(1)
	if m := r.Resolve(context.Background(), q2, "c"); m.Rcode != dns.RcodeBadVers {
		t.Fatalf("EDNS version 1 should get BADVERS, got %d", m.Rcode)
	}
}

func TestWaiterCancellation(t *testing.T) {
	gate := make(chan struct{})
	defer close(gate)
	a := &fake{fn: func(q *dns.Msg) (*dns.Msg, error) { <-gate; return aResp(q, "1.1.1.1", 60), nil }}
	r := setup(t, map[string]*fake{"a": a, "b": a}, nil)
	go r.Resolve(context.Background(), query("w.com", dns.TypeA), "c") // slow leader
	time.Sleep(30 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	m := r.Resolve(ctx, query("w.com", dns.TypeA), "c")
	if time.Since(start) > 500*time.Millisecond || m.Rcode != dns.RcodeServerFailure {
		t.Fatalf("waiter not released on its own deadline: %v rcode=%d", time.Since(start), m.Rcode)
	}
}

// An unsupported EDNS version gets BADVERS even for a name that a rule would
// otherwise answer with a synthetic empty reply (AAAA suppression).
func TestBadVersBeatsRuleSuppression(t *testing.T) {
	g := &fake{fn: func(q *dns.Msg) (*dns.Msg, error) { return aResp(q, "193.233.112.67", 60), nil }}
	r := setup(t, map[string]*fake{"g": g}, func(c *config.Config) {
		c.ActiveProvider = "g"
		c.Rules = []config.Rule{{Domain: "chatgpt.com", Enabled: true}}
	})
	q := query("chatgpt.com", dns.TypeAAAA)
	q.SetEdns0(1232, false)
	q.IsEdns0().SetVersion(1)
	if m := r.Resolve(context.Background(), q, "c"); m.Rcode != dns.RcodeBadVers {
		t.Fatalf("rule + EDNS v1 should get BADVERS, got rcode %d", m.Rcode)
	}
	if g.calls.Load() != 0 {
		t.Fatal("the provider was asked for a request that is refused")
	}
}
