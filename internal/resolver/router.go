// Package resolver routes DNS queries to DoH upstreams with rules, fallback,
// caching, health tracking and a query log.
package resolver

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/archerpm/doh-client/internal/config"
	"github.com/archerpm/doh-client/internal/doh"
	"github.com/miekg/dns"
	"golang.org/x/sync/singleflight"
)

// Exchanger sends one DNS query upstream.
type Exchanger interface {
	Exchange(ctx context.Context, q *dns.Msg) (*dns.Msg, error)
}

type closer interface{ Close() }

type upstream struct {
	id   string
	name string
	ex   Exchanger
}

// state is an immutable snapshot built from a config.
type state struct {
	cfg       *config.Config
	upstreams map[string]*upstream
	rules     map[string][]string // domain -> provider chain (nil = default)
	chain     []string
	timeout   time.Duration
}

// Router is safe for concurrent use.
type Router struct {
	st     atomic.Pointer[state]
	cache  *Cache
	health *HealthTable
	log    *QueryLog
	stats  Stats

	// Test synchronization points; set before concurrent use.
	afterStateLoad func()
	beforeCachePut func()

	// newExchanger is replaceable in tests.
	newExchanger func(p config.Provider, boot *doh.Bootstrap, timeout time.Duration) (Exchanger, error)
	mu           sync.Mutex   // guards state/generation snapshots and cache publication
	exchangeMu   sync.RWMutex // Apply waits for all old exchanges before publishing

	flight singleflight.Group
	gen    atomic.Uint64 // bumped on Apply/Flush: older in-flight answers must not repopulate the cache
}

// Stats are cumulative counters.
type Stats struct {
	Queries   atomic.Uint64
	CacheHits atomic.Uint64
	Failures  atomic.Uint64
	RuleHits  atomic.Uint64
	Blocked   atomic.Uint64 // synthesized empty answers (AAAA/HTTPS for rule domains)

	StaleServed atomic.Uint64 // expired cached answers given because the provider failed
}

// StatsSnapshot is a JSON-friendly copy of Stats.
type StatsSnapshot struct {
	Queries   uint64 `json:"queries"`
	CacheHits uint64 `json:"cache_hits"`
	Failures  uint64 `json:"failures"`
	RuleHits  uint64 `json:"rule_hits"`
	Blocked   uint64 `json:"blocked"`
	CacheSize int    `json:"cache_size"`

	StaleServed uint64 `json:"stale_served"`
}

// New creates a router; call Apply before use.
func New() *Router {
	r := &Router{
		cache:  NewCache(4096),
		health: NewHealthTable(),
		log:    NewQueryLog(1000),
	}
	r.newExchanger = defaultExchanger
	return r
}

func defaultExchanger(p config.Provider, boot *doh.Bootstrap, timeout time.Duration) (Exchanger, error) {
	return doh.New(doh.Options{ID: p.ID, URL: p.URL, Bootstrap: p.Bootstrap, UseGET: p.UseGET, Resolver: boot, Timeout: timeout})
}

// SetExchangerFactory replaces how upstreams are built (tests and diagnostics).
// Call before Apply.
func (r *Router) SetExchangerFactory(f func(p config.Provider, boot *doh.Bootstrap, timeout time.Duration) (Exchanger, error)) {
	r.mu.Lock()
	r.newExchanger = f
	r.mu.Unlock()
}

// Apply installs a new configuration. Upstreams are rebuilt; the cache is flushed
// because routing may have changed.
func (r *Router) Apply(cfg *config.Config) error {
	r.exchangeMu.Lock()
	defer r.exchangeMu.Unlock()
	r.mu.Lock()
	defer r.mu.Unlock()
	cfg = cfg.Clone()
	if err := cfg.Normalize(); err != nil {
		return err
	}
	boot := doh.NewBootstrap(cfg.BootstrapDNS)
	ns := &state{cfg: cfg, upstreams: map[string]*upstream{}, rules: map[string][]string{}, timeout: cfg.Timeout()}
	for _, p := range cfg.Providers {
		if !p.Enabled {
			continue
		}
		ex, err := r.newExchanger(p, boot, cfg.Timeout())
		if err != nil {
			return fmt.Errorf("provider %s: %w", p.ID, err)
		}
		ns.upstreams[p.ID] = &upstream{id: p.ID, name: p.Name, ex: ex}
	}
	ns.chain = enabledOnly(cfg.DefaultChain, ns.upstreams)
	for _, rl := range cfg.Rules {
		if rl.Enabled {
			ns.rules[rl.Domain] = enabledOnly(rl.Providers, ns.upstreams)
		}
	}
	old := r.st.Swap(ns)
	r.gen.Add(1)
	r.cache.Reset(cfg.CacheSize)
	r.log.Resize(cfg.LogSize)
	ids := make([]string, 0, len(ns.upstreams))
	for id := range ns.upstreams {
		ids = append(ids, id)
	}
	r.health.Retain(ids)
	if old != nil {
		for _, u := range old.upstreams {
			if c, ok := u.ex.(closer); ok {
				c.Close()
			}
		}
	}
	return nil
}

func enabledOnly(ids []string, ups map[string]*upstream) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, ok := ups[id]; ok {
			out = append(out, id)
		}
	}
	return out
}

// MatchRule returns the matched rule domain for name (suffix match on label boundaries).
func (s *state) matchRule(name string) (string, []string, bool) {
	for n := name; n != ""; {
		if ch, ok := s.rules[n]; ok {
			return n, ch, true
		}
		i := strings.IndexByte(n, '.')
		if i < 0 {
			break
		}
		n = n[i+1:]
	}
	return "", nil, false
}

// Result describes how a query was answered (for the log and the API).
type Result struct {
	Msg      *dns.Msg
	Provider string
	Rule     string
	Cached   bool
	Synth    bool
	Err      error
	Tried    []string
}

// Resolve answers q. It never returns nil Msg.
func (r *Router) Resolve(ctx context.Context, q *dns.Msg, client string) *dns.Msg {
	start := time.Now()
	res := r.resolve(ctx, q)
	r.stats.Queries.Add(1)
	if res.Err != nil {
		r.stats.Failures.Add(1)
	}
	e := Entry{Time: start, Client: client, Duration: time.Since(start), Provider: res.Provider,
		Rule: res.Rule, Cached: res.Cached, Synth: res.Synth, Tried: res.Tried}
	if len(q.Question) > 0 {
		e.Name = strings.TrimSuffix(q.Question[0].Name, ".")
		e.Type = dns.TypeToString[q.Question[0].Qtype]
	}
	if res.Msg != nil {
		e.Rcode = dns.RcodeToString[res.Msg.Rcode]
		e.Answers = summarize(res.Msg)
	}
	if res.Err != nil {
		e.Error = res.Err.Error()
	}
	r.log.Add(e)
	return res.Msg
}

// ResolveWith queries one specific provider bypassing cache and rules (diagnostics).
func (r *Router) ResolveWith(ctx context.Context, providerID, name string, qtype uint16) (*dns.Msg, time.Duration, error) {
	r.exchangeMu.RLock()
	defer r.exchangeMu.RUnlock()
	st := r.st.Load()
	if st == nil {
		return nil, 0, errors.New("router not configured")
	}
	u, ok := st.upstreams[providerID]
	if !ok {
		return nil, 0, fmt.Errorf("provider %q is not enabled", providerID)
	}
	q := new(dns.Msg)
	q.SetQuestion(dns.Fqdn(name), qtype)
	q.RecursionDesired = true
	ctx, cancel := context.WithTimeout(ctx, st.timeout)
	defer cancel()
	t := time.Now()
	m, err := u.ex.Exchange(ctx, q)
	return m, time.Since(t), err
}

// ResolveProvider queries only an installed active upstream, bypassing cache and rules.
func (r *Router) ResolveProvider(ctx context.Context, p config.Provider, name string, qtype uint16) (*dns.Msg, time.Duration, error) {
	return r.ResolveWith(ctx, p.ID, name, qtype)
}

// snapshot captures state and its cache generation atomically with Apply/FlushCache.
func (r *Router) snapshot() (*state, uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	st := r.st.Load()
	if r.afterStateLoad != nil {
		r.afterStateLoad()
	}
	return st, r.gen.Load()
}

func (r *Router) cacheAnswer(st *state, gen uint64, key string, m *dns.Msg) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.gen.Load() == gen {
		if r.beforeCachePut != nil {
			r.beforeCachePut()
		}
		r.cache.Put(key, m, st.cfg.MinTTL, st.cfg.MaxTTL)
	}
}

// invalidate drops the cached answer for key (a newer final answer replaced it).
func (r *Router) invalidate(gen uint64, key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.gen.Load() == gen {
		r.cache.Delete(key)
	}
}

// errConfigChanged: Apply swapped the state between the snapshot and the exchange.
var errConfigChanged = errors.New("configuration changed")

// resolve retries once on a fresh snapshot when a tunnel switch raced the query,
// so the client does not see a SERVFAIL for a perfectly answerable question.
func (r *Router) resolve(ctx context.Context, q *dns.Msg) Result {
	res := r.resolveOnce(ctx, q)
	if errors.Is(res.Err, errConfigChanged) && ctx.Err() == nil {
		res = r.resolveOnce(ctx, q)
	}
	return res
}

func (r *Router) resolveOnce(ctx context.Context, q *dns.Msg) Result {
	st, gen := r.snapshot()
	if st == nil {
		return Result{Msg: reply(q, dns.RcodeServerFailure), Err: errors.New("not configured")}
	}
	if q.Opcode != dns.OpcodeQuery {
		return Result{Msg: reply(q, dns.RcodeNotImplemented), Synth: true}
	}
	if len(q.Question) != 1 {
		return Result{Msg: reply(q, dns.RcodeFormatError), Synth: true}
	}
	qq := q.Question[0]
	name := strings.ToLower(strings.TrimSuffix(qq.Name, "."))

	chain := st.chain
	ruleDom, ruleChain, isRule := st.matchRule(name)
	// RFC 6891 §6.1.3: an unsupported EDNS version is answered with BADVERS before
	// anything else, a rule-based suppression included, and never from the cache.
	if o := q.IsEdns0(); o != nil && o.Version() != 0 {
		return Result{Msg: reply(q, dns.RcodeBadVers), Rule: ruleDom, Synth: true}
	}
	if isRule {
		r.stats.RuleHits.Add(1)
		if len(ruleChain) > 0 {
			chain = ruleChain
		}
		if qq.Qclass == dns.ClassINET &&
			((qq.Qtype == dns.TypeAAAA && st.cfg.BlockAAAAForRule) ||
				((qq.Qtype == dns.TypeHTTPS || qq.Qtype == dns.TypeSVCB) && st.cfg.BlockHTTPSRR)) {
			r.stats.Blocked.Add(1)
			return Result{Msg: reply(q, dns.RcodeSuccess), Rule: ruleDom, Synth: true}
		}
	}

	key := cacheKey(name, qq.Qtype, qq.Qclass, q)
	r.mu.Lock()
	var cached *dns.Msg
	var hit bool
	if r.gen.Load() == gen {
		cached, hit = r.cache.Get(key)
	}
	r.mu.Unlock()
	if m, ok := cached, hit; ok {
		r.stats.CacheHits.Add(1)
		m.Id = q.Id
		m.Question = []dns.Question{qq} // client's own case (0x20)
		return Result{Msg: m, Rule: ruleDom, Cached: true, Provider: "cache"}
	}
	if len(chain) == 0 {
		return Result{Msg: reply(q, dns.RcodeServerFailure), Rule: ruleDom, Err: errors.New("no enabled providers in chain")}
	}
	// Coalesce identical in-flight queries (browser + OS resolver often ask at once).
	// The shared exchange has its own deadline; each caller can still give up on
	// its own context without cancelling the others.
	up := upstreamQuery(q)
	ch := r.flight.DoChan(key+"|"+strconv.FormatUint(gen, 10), func() (any, error) {
		budget := st.timeout*time.Duration(len(chain)) + time.Second
		sctx, cancel := context.WithTimeout(context.Background(), budget)
		defer cancel()
		return r.exchangeChain(sctx, st, up, chain, isRule, ruleDom, key, gen), nil
	})
	var res Result
	select {
	case v := <-ch:
		res = v.Val.(Result)
		if v.Shared && res.Msg != nil {
			res.Msg = res.Msg.Copy()
		}
	case <-ctx.Done():
		return Result{Msg: reply(q, dns.RcodeServerFailure), Rule: ruleDom, Err: ctx.Err()}
	}
	// The provider could not be reached: an expired answer beats SERVFAIL (RFC 8767).
	if res.Err != nil && res.Msg != nil && res.Msg.Rcode == dns.RcodeServerFailure {
		r.mu.Lock()
		var stale *dns.Msg
		var ok bool
		if r.gen.Load() == gen {
			stale, ok = r.cache.GetStale(key)
		}
		r.mu.Unlock()
		if ok {
			r.stats.StaleServed.Add(1)
			stale.Id = q.Id
			stale.Question = []dns.Question{qq}
			return Result{Msg: stale, Rule: ruleDom, Cached: true, Provider: "cache-stale", Tried: res.Tried}
		}
	}
	if res.Msg != nil {
		res.Msg.Id = q.Id
		res.Msg.Question = []dns.Question{qq}
	}
	return res
}

// upstreamQuery builds what is sent to providers: only the question and the
// flags that change the answer (RD, CD, DO). Client EDNS options such as ECS,
// cookies or padding are not forwarded — better privacy, and the cache key
// (name, type, class, DO, CD) then fully describes the request.
func upstreamQuery(q *dns.Msg) *dns.Msg {
	m := new(dns.Msg)
	m.Id = q.Id
	m.Opcode = dns.OpcodeQuery
	m.RecursionDesired = true
	m.CheckingDisabled = q.CheckingDisabled
	m.Question = []dns.Question{q.Question[0]}
	if o := q.IsEdns0(); o != nil {
		m.SetEdns0(1232, o.Do())
	}
	return m
}

func (r *Router) exchangeChain(ctx context.Context, st *state, q *dns.Msg, chain []string, isRule bool, ruleDom, key string, gen uint64) Result {
	r.exchangeMu.RLock()
	defer r.exchangeMu.RUnlock()
	if r.st.Load() != st {
		return Result{Msg: reply(q, dns.RcodeServerFailure), Err: errConfigChanged, Rule: ruleDom}
	}

	var (
		lastMsg  *dns.Msg
		lastProv string
		errs     []error
		tried    []string
	)
	for _, id := range r.health.Order(chain) {
		u := st.upstreams[id]
		tried = append(tried, id)
		actx, cancel := context.WithTimeout(ctx, st.timeout)
		t := time.Now()
		m, err := u.ex.Exchange(actx, q)
		cancel()
		lat := time.Since(t)
		if err == nil && m == nil {
			err = errors.New("empty DNS response")
		}
		if err != nil {
			r.health.Report(id, false, lat, err)
			errs = append(errs, fmt.Errorf("%s: %w", id, err))
			if ctx.Err() != nil {
				break
			}
			continue
		}
		switch m.Rcode {
		case dns.RcodeSuccess, dns.RcodeNameError:
			r.health.Report(id, true, lat, nil)
		default:
			err = fmt.Errorf("%s: rcode %s", id, dns.RcodeToString[m.Rcode])
			r.health.Report(id, false, lat, err)
			errs = append(errs, err)
			if lastMsg == nil {
				lastMsg, lastProv = m, id
			}
			continue
		}
		if isRule && isSinkhole(m) {
			// Provider refuses this domain (e.g. mafioznik returns 0.0.0.0): try the next one.
			r.invalidate(gen, key) // and an older answer must not come back as a stale one
			errs = append(errs, fmt.Errorf("%s: blocked answer", id))
			if lastMsg == nil || lastMsg.Rcode != dns.RcodeSuccess {
				lastMsg, lastProv = m, id
			}
			continue
		}
		r.cacheAnswer(st, gen, key, m)
		return Result{Msg: m, Provider: id, Rule: ruleDom, Tried: tried}
	}
	err := errors.Join(errs...)
	if lastMsg != nil {
		lastMsg.Id = q.Id
		return Result{Msg: lastMsg, Provider: lastProv, Rule: ruleDom, Err: err, Tried: tried}
	}
	return Result{Msg: reply(q, dns.RcodeServerFailure), Rule: ruleDom, Err: err, Tried: tried}
}

func reply(q *dns.Msg, rcode int) *dns.Msg {
	m := new(dns.Msg)
	m.SetRcode(q, rcode)
	m.RecursionAvailable = true
	return m
}

// isSinkhole reports whether all address answers are 0.0.0.0 / ::.
func isSinkhole(m *dns.Msg) bool {
	n := 0
	for _, rr := range m.Answer {
		switch v := rr.(type) {
		case *dns.A:
			if !v.A.IsUnspecified() {
				return false
			}
			n++
		case *dns.AAAA:
			if !v.AAAA.IsUnspecified() {
				return false
			}
			n++
		}
	}
	return n > 0
}

func summarize(m *dns.Msg) []string {
	var out []string
	for _, rr := range m.Answer {
		switch v := rr.(type) {
		case *dns.A:
			out = append(out, v.A.String())
		case *dns.AAAA:
			out = append(out, v.AAAA.String())
		case *dns.CNAME:
			out = append(out, "CNAME "+strings.TrimSuffix(v.Target, "."))
		default:
			out = append(out, dns.TypeToString[rr.Header().Rrtype])
		}
		if len(out) >= 8 {
			break
		}
	}
	return out
}

// AddrsOf extracts A/AAAA addresses from a message.
func AddrsOf(m *dns.Msg) []netip.Addr {
	var out []netip.Addr
	for _, rr := range m.Answer {
		switch v := rr.(type) {
		case *dns.A:
			if a, ok := netip.AddrFromSlice(v.A.To4()); ok {
				out = append(out, a)
			}
		case *dns.AAAA:
			if a, ok := netip.AddrFromSlice(v.AAAA); ok {
				out = append(out, a)
			}
		}
	}
	return out
}

// Stats returns counters.
func (r *Router) Stats() StatsSnapshot {
	return StatsSnapshot{
		Queries: r.stats.Queries.Load(), CacheHits: r.stats.CacheHits.Load(), Failures: r.stats.Failures.Load(),
		RuleHits: r.stats.RuleHits.Load(), Blocked: r.stats.Blocked.Load(), CacheSize: r.cache.Len(),
		StaleServed: r.stats.StaleServed.Load(),
	}
}

// Health returns provider health, with the transport counters of each upstream
// (since the tunnel was selected) when the upstream keeps them.
func (r *Router) Health() []HealthInfo {
	hs := r.health.Snapshot()
	if st := r.st.Load(); st != nil {
		for i := range hs {
			if u := st.upstreams[hs[i].ID]; u != nil {
				if s, ok := u.ex.(interface{ Stats() doh.Stats }); ok {
					hs[i].Transport = s.Stats()
				}
			}
		}
	}
	return hs
}

// Log returns log entries after seq.
func (r *Router) Log(after uint64, limit int) []Entry { return r.log.After(after, limit) }

// FlushCache drops all cached answers.
func (r *Router) FlushCache() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.gen.Add(1)
	r.cache.Reset(0)
}

// CheckHealth probes every enabled upstream once (used by the periodic checker).
func (r *Router) CheckHealth(ctx context.Context, probe string) {
	r.exchangeMu.RLock()
	defer r.exchangeMu.RUnlock()
	st := r.st.Load()
	if st == nil {
		return
	}
	var wg sync.WaitGroup
	for id, u := range st.upstreams {
		wg.Add(1)
		go func(id string, u *upstream) {
			defer wg.Done()
			q := new(dns.Msg)
			q.SetQuestion(dns.Fqdn(probe), dns.TypeA)
			q.RecursionDesired = true
			cctx, cancel := context.WithTimeout(ctx, st.timeout)
			defer cancel()
			t := time.Now()
			m, err := u.ex.Exchange(cctx, q)
			if err == nil && m == nil {
				err = errors.New("empty DNS response")
			}
			if err == nil && m.Rcode != dns.RcodeSuccess && m.Rcode != dns.RcodeNameError {
				err = fmt.Errorf("rcode %s", dns.RcodeToString[m.Rcode])
			}
			r.health.Probe(id, err == nil, time.Since(t), err)
		}(id, u)
	}
	wg.Wait()
}

// RunHealth runs CheckHealth periodically until ctx is done.
func (r *Router) RunHealth(ctx context.Context, interval func() time.Duration) {
	t := time.NewTimer(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.CheckHealth(ctx, "example.com")
			t.Reset(interval())
		}
	}
}

// Config returns the active config snapshot (do not modify).
func (r *Router) Config() *config.Config {
	if st := r.st.Load(); st != nil {
		return st.cfg
	}
	return nil
}
