// Package support finds out which services each bypass provider really proxies.
// It asks every provider over DoH for the hosts of the service catalogue and
// compares the answers with ordinary DNS. A provider answers a geo-blocked host
// with an address of its own proxy; those addresses are learned from the answers
// themselves (see learnPool) and a service counts as supported when at least
// half of its hosts resolve to them.
//
// It is used by the generator tools/probesupport (the built-in "confirmed by
// tests" data) and by the service (the check run from the Providers tab).
package support

import (
	"context"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/archerpm/doh-client/internal/config"
	"github.com/archerpm/doh-client/internal/doh"
	"github.com/archerpm/doh-client/internal/resolver"
	"github.com/miekg/dns"
)

// PoolMinApex: an address must be returned for at least this many unrelated
// domains to be taken for a proxy address.
const PoolMinApex = 3

// cdnRanges are published ranges of big CDNs (Cloudflare, Fastly) and of Google:
// sites on them share addresses by nature, so such an address says nothing about
// a proxy even when ordinary DNS happened to show other ranges.
var cdnRanges = func() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{
		// Cloudflare (cloudflare.com/ips-v4)
		"173.245.48.0/20", "103.21.244.0/22", "103.22.200.0/22", "103.31.4.0/22", "141.101.64.0/18", "108.162.192.0/18",
		"190.93.240.0/20", "188.114.96.0/20", "197.234.240.0/22", "198.41.128.0/17", "162.158.0.0/15", "104.16.0.0/13",
		"104.24.0.0/14", "172.64.0.0/13", "131.0.72.0/22",
		// Fastly
		"151.101.0.0/16", "199.232.0.0/16",
		// Google
		"142.250.0.0/15", "172.217.0.0/16", "216.58.192.0/19", "74.125.0.0/16", "64.233.160.0/19", "173.194.0.0/16", "209.85.128.0/17",
	} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

func isCDN(a netip.Addr) bool {
	for _, p := range cdnRanges {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// apexOf approximates the registrable domain: the last two labels.
func apexOf(host string) string {
	labels := strings.Split(host, ".")
	if len(labels) <= 2 {
		return host
	}
	return strings.Join(labels[len(labels)-2:], ".")
}

func prefix16(a netip.Addr) netip.Prefix { p, _ := a.Prefix(16); return p }
func prefix24(a netip.Addr) netip.Prefix { p, _ := a.Prefix(24); return p }

// pool is a provider's learned set of proxy addresses and their /24 networks.
type pool struct {
	addrs map[netip.Addr]bool
	nets  map[netip.Prefix]bool
}

func (p pool) has(a netip.Addr) bool { return p.addrs[a] || p.nets[prefix24(a)] }

// learnPool finds a provider's proxy addresses in its own answers: an IPv4
// address that shows up for at least minApex different domains while the
// ordinary DNS never returns anything from its /16. A CDN address shared by many
// sites is excluded by the second condition (ordinary DNS shows that network too).
func learnPool(answers map[string][]netip.Addr, ref16 map[netip.Prefix]bool, minApex int) pool {
	apexes := map[netip.Addr]map[string]bool{}
	for host, addrs := range answers {
		for _, a := range addrs {
			if !a.Is4() || a.IsUnspecified() || a.IsPrivate() || isCDN(a) {
				continue
			}
			if apexes[a] == nil {
				apexes[a] = map[string]bool{}
			}
			apexes[a][apexOf(host)] = true
		}
	}
	p := pool{addrs: map[netip.Addr]bool{}, nets: map[netip.Prefix]bool{}}
	for a, set := range apexes {
		if len(set) >= minApex && !ref16[prefix16(a)] {
			p.addrs[a] = true
			p.nets[prefix24(a)] = true
		}
	}
	return p
}

// proxied reports whether any answered address belongs to the pool.
func proxied(addrs []netip.Addr, p pool) bool {
	for _, a := range addrs {
		if a.Is4() && p.has(a) {
			return true
		}
	}
	return false
}

// supports: a service counts when at least half of its hosts are proxied.
func supports(s config.Service, isProxied func(host string) bool) (n int, ok bool) {
	for _, h := range s.Hosts {
		if isProxied(h) {
			n++
		}
	}
	return n, n > 0 && n*2 >= len(s.Hosts)
}

// RawAnswer is one provider's answer for one host (last run), for reports.
type RawAnswer struct {
	Rcode string   `json:"rcode"`
	Addrs []string `json:"addrs"`
}

// ProviderResult is what one provider was seen proxying.
type ProviderResult struct {
	Provider string
	Services []string // names of the supported services, catalogue order
	Pool     []string // learned proxy addresses
	Hosts    int      // hosts asked
	Errors   int      // hosts the provider did not answer in the last run
	Raw      map[string]RawAnswer

	votes map[string]int
	runs  int
}

// Proxied reports whether most runs saw the host resolved to the provider's proxy.
func (r ProviderResult) Proxied(host string) bool { return r.votes[host]*2 > r.runs }

// Options configure Run.
type Options struct {
	Services  []config.Service
	Extra     []string          // more hosts to ask: they help to learn the proxy addresses
	Providers []config.Provider // providers to ask
	Bootstrap []string          // bootstrap DNS for the DoH servers
	Repeat    int               // asks of everything; a host counts when most runs agree (default 3)
	Workers   int               // parallel DoH queries per provider (default 8)
	MinApex   int               // see PoolMinApex
	Progress  func(done, total int)

	// replaceable in tests
	Ask func(ctx context.Context, p config.Provider, host string) (*dns.Msg, error)
	Ref func(ctx context.Context, host string) []netip.Addr
}

// Hosts returns the sorted hosts Run asks: the services' hosts plus Extra.
func (o Options) Hosts() []string {
	set := map[string]bool{}
	for _, s := range o.Services {
		for _, h := range s.Hosts {
			set[h] = true
		}
	}
	for _, h := range o.Extra {
		set[h] = true
	}
	hosts := make([]string, 0, len(set))
	for h := range set {
		hosts = append(hosts, h)
	}
	sort.Strings(hosts)
	return hosts
}

// UDPReference asks ordinary DNS over UDP; it is the reference for "real" addresses.
func UDPReference(servers []string) func(context.Context, string) []netip.Addr {
	return func(ctx context.Context, host string) []netip.Addr {
		var out []netip.Addr
		for _, s := range servers {
			if ctx.Err() != nil {
				return out
			}
			q := new(dns.Msg)
			q.SetQuestion(dns.Fqdn(host), dns.TypeA)
			c := &dns.Client{Net: "udp", Timeout: 4 * time.Second}
			m, _, err := c.ExchangeContext(ctx, q, s)
			if err != nil || m == nil || m.Rcode != dns.RcodeSuccess {
				continue
			}
			out = append(out, resolver.AddrsOf(m)...)
		}
		return out
	}
}

// Run asks every provider for every host Repeat times and works out the
// supported services. The results come in the order of o.Providers.
func Run(ctx context.Context, o Options) ([]ProviderResult, error) {
	if o.Repeat < 1 {
		o.Repeat = 3
	}
	if o.Workers < 1 {
		o.Workers = 8
	}
	if o.MinApex < 1 {
		o.MinApex = PoolMinApex
	}
	hosts := o.Hosts()
	total := len(hosts) + o.Repeat*len(o.Providers)*len(hosts)
	var pmu sync.Mutex // Progress calls are serialized and never go backwards
	done := 0
	step := func() {
		pmu.Lock()
		defer pmu.Unlock()
		done++
		if o.Progress != nil && (done%10 == 0 || done == total) {
			o.Progress(done, total)
		}
	}

	// ordinary DNS as the reference
	refFn := o.Ref
	if refFn == nil {
		refFn = UDPReference([]string{"8.8.8.8:53", "1.1.1.1:53"})
	}
	ref16 := map[netip.Prefix]bool{}
	var rmu sync.Mutex
	runPool(hosts, o.Workers*2, func(h string) {
		defer step()
		addrs := refFn(ctx, h)
		rmu.Lock()
		defer rmu.Unlock()
		for _, a := range addrs {
			if a.Is4() {
				ref16[prefix16(a)] = true
			}
		}
	})
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	var boot *doh.Bootstrap
	if o.Ask == nil {
		boot = doh.NewBootstrap(o.Bootstrap)
	}
	type runAnswers map[string]map[string][]netip.Addr
	var runs []runAnswers
	raw := map[string]map[string]RawAnswer{}
	for run := 0; run < o.Repeat; run++ {
		answers := runAnswers{}
		raw = map[string]map[string]RawAnswer{}
		var wg sync.WaitGroup
		var mu sync.Mutex
		for _, p := range o.Providers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				ask := o.Ask
				if ask == nil {
					up, err := doh.New(doh.Options{ID: p.ID, URL: p.URL, Bootstrap: p.Bootstrap, UseGET: p.UseGET, Resolver: boot, Timeout: 6 * time.Second})
					if err != nil {
						for range hosts {
							step()
						}
						mu.Lock()
						answers[p.ID], raw[p.ID] = map[string][]netip.Addr{}, errorsFor(hosts)
						mu.Unlock()
						return
					}
					defer up.Close()
					ask = func(ctx context.Context, _ config.Provider, host string) (*dns.Msg, error) {
						q := new(dns.Msg)
						q.SetQuestion(dns.Fqdn(host), dns.TypeA)
						return up.Exchange(ctx, q)
					}
				}
				mine := map[string][]netip.Addr{}
				myRaw := map[string]RawAnswer{}
				var pm sync.Mutex
				runPool(hosts, o.Workers, func(h string) {
					defer step()
					var m *dns.Msg
					var err error
					for try := 0; try < 4 && ctx.Err() == nil; try++ {
						c, cancel := context.WithTimeout(ctx, 8*time.Second)
						m, err = ask(c, p, h)
						cancel()
						if err == nil && m != nil {
							break
						}
					}
					pm.Lock()
					defer pm.Unlock()
					if err != nil || m == nil {
						myRaw[h] = RawAnswer{Rcode: "ERROR"}
						return
					}
					addrs := resolver.AddrsOf(m)
					r := RawAnswer{Rcode: dns.RcodeToString[m.Rcode]}
					for _, a := range addrs {
						r.Addrs = append(r.Addrs, a.String())
					}
					myRaw[h] = r
					mine[h] = addrs
				})
				mu.Lock()
				answers[p.ID], raw[p.ID] = mine, myRaw
				mu.Unlock()
			}()
		}
		wg.Wait()
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		runs = append(runs, answers)
	}

	out := make([]ProviderResult, 0, len(o.Providers))
	for _, p := range o.Providers {
		// the per-host majority over all runs, each run with the pool learned from itself
		votes := map[string]int{}
		for _, ra := range runs {
			plr := learnPool(ra[p.ID], ref16, o.MinApex)
			for h, addrs := range ra[p.ID] {
				if proxied(addrs, plr) {
					votes[h]++
				}
			}
		}
		res := ProviderResult{Provider: p.ID, Hosts: len(hosts), Raw: raw[p.ID], votes: votes, runs: len(runs)}
		for a := range learnPool(runs[len(runs)-1][p.ID], ref16, o.MinApex).addrs {
			res.Pool = append(res.Pool, a.String())
		}
		sort.Strings(res.Pool)
		for _, r := range res.Raw {
			if r.Rcode == "ERROR" {
				res.Errors++
			}
		}
		for _, s := range o.Services {
			if _, ok := supports(s, res.Proxied); ok {
				res.Services = append(res.Services, s.Name)
			}
		}
		out = append(out, res)
	}
	return out, nil
}

func errorsFor(hosts []string) map[string]RawAnswer {
	m := make(map[string]RawAnswer, len(hosts))
	for _, h := range hosts {
		m[h] = RawAnswer{Rcode: "ERROR"}
	}
	return m
}

// runPool calls fn for every item with at most n calls in flight.
func runPool(items []string, n int, fn func(string)) {
	if n < 1 {
		n = 1
	}
	ch := make(chan string)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for it := range ch {
				fn(it)
			}
		}()
	}
	for _, it := range items {
		ch <- it
	}
	close(ch)
	wg.Wait()
}
