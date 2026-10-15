package doh

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"sync"
	"time"

	"github.com/miekg/dns"
)

// Bootstrap resolves host names via plain DNS servers given as ip:port.
// It never consults the OS resolver, which may be pointed at this program.
type Bootstrap struct {
	Servers []string

	mu    sync.Mutex
	cache map[string]bootEntry
}

type bootEntry struct {
	addrs []netip.Addr
	exp   time.Time
}

func NewBootstrap(servers []string) *Bootstrap {
	return &Bootstrap{Servers: servers, cache: map[string]bootEntry{}}
}

// Resolve returns A and AAAA records (IPv4 first).
func (b *Bootstrap) Resolve(ctx context.Context, host string) ([]netip.Addr, error) {
	if a, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{a}, nil
	}
	b.mu.Lock()
	if e, ok := b.cache[host]; ok && time.Now().Before(e.exp) {
		b.mu.Unlock()
		return append([]netip.Addr(nil), e.addrs...), nil
	}
	b.mu.Unlock()

	var errs []error
	for _, srv := range b.Servers {
		// One budget per server, shared by A, AAAA and the UDP→TCP retry, so a slow
		// server cannot starve the ones after it.
		sctx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
		// A and AAAA in parallel: a dead server then costs one timeout, not two,
		// and the remaining servers still get a share of the caller's budget.
		var v4, v6 []netip.Addr
		var ttl4, ttl6 uint32
		var err4 error
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); v4, ttl4, err4 = b.query(sctx, srv, host, dns.TypeA) }()
		go func() { defer wg.Done(); v6, ttl6, _ = b.query(sctx, srv, host, dns.TypeAAAA) }()
		wg.Wait()
		cancel()
		addrs := append(v4, v6...)
		if len(addrs) == 0 {
			if err4 != nil {
				errs = append(errs, err4)
			} else {
				errs = append(errs, fmt.Errorf("%s: no addresses for %s", srv, host))
			}
			continue
		}
		ttl := ttl4
		if ttl == 0 || (ttl6 > 0 && ttl6 < ttl) {
			ttl = ttl6
		}
		if ttl < 60 {
			ttl = 60
		}
		if ttl > 3600 {
			ttl = 3600
		}
		b.mu.Lock()
		b.cache[host] = bootEntry{addrs: addrs, exp: time.Now().Add(time.Duration(ttl) * time.Second)}
		b.mu.Unlock()
		return addrs, nil
	}
	return nil, errors.Join(errs...)
}

func (b *Bootstrap) query(ctx context.Context, srv, host string, t uint16) ([]netip.Addr, uint32, error) {
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(host), t)
	m.RecursionDesired = true
	c := &dns.Client{Timeout: 1500 * time.Millisecond}
	r, _, err := c.ExchangeContext(ctx, m, srv)
	if err == nil && r.Truncated {
		c.Net = "tcp"
		r, _, err = c.ExchangeContext(ctx, m, srv)
	}
	if err != nil {
		return nil, 0, err
	}
	if r.Rcode != dns.RcodeSuccess {
		return nil, 0, fmt.Errorf("%s: rcode %s", srv, dns.RcodeToString[r.Rcode])
	}
	var out []netip.Addr
	var ttl uint32
	for _, rr := range r.Answer {
		switch v := rr.(type) {
		case *dns.A:
			if a, ok := netip.AddrFromSlice(v.A.To4()); ok {
				out = append(out, a)
				ttl = minTTL(ttl, v.Hdr.Ttl)
			}
		case *dns.AAAA:
			if a, ok := netip.AddrFromSlice(v.AAAA); ok {
				out = append(out, a)
				ttl = minTTL(ttl, v.Hdr.Ttl)
			}
		}
	}
	return out, ttl, nil
}

func minTTL(a, b uint32) uint32 {
	if a == 0 || b < a {
		return b
	}
	return a
}
