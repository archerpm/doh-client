// Package doh implements an RFC 8484 DNS-over-HTTPS client.
package doh

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
)

const (
	mimeDNS     = "application/dns-message"
	maxRespSize = 65535
	userAgent   = "doh-client/1.0"
)

var errRedirect = errors.New("redirects are not allowed for DoH")

// HostResolver resolves the DoH host name without using the system resolver
// (the system resolver may point back at us).
type HostResolver interface {
	Resolve(ctx context.Context, host string) ([]netip.Addr, error)
}

// Upstream is a single DoH server.
type Upstream struct {
	ID     string
	URL    *url.URL
	host   string
	useGET bool
	static []netip.Addr
	hr     HostResolver
	client *http.Client

	mu     sync.Mutex
	lastOK netip.Addr               // IP that last worked, tried first
	bad    map[netip.Addr]time.Time // IPs where TLS/HTTP failed after TCP connected
}

const badHold = 2 * time.Minute

// Options configure an Upstream.
type Options struct {
	ID        string
	URL       string
	Bootstrap []string
	UseGET    bool
	Resolver  HostResolver
	Timeout   time.Duration
	// TLSConfig overrides TLS settings (tests only).
	TLSConfig *tls.Config
}

// New creates an Upstream.
func New(o Options) (*Upstream, error) {
	u, err := url.Parse(o.URL)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "https" {
		return nil, fmt.Errorf("doh %s: scheme must be https", o.ID)
	}
	up := &Upstream{ID: o.ID, URL: u, host: u.Hostname(), useGET: o.UseGET, hr: o.Resolver}
	for _, s := range o.Bootstrap {
		if a, err := netip.ParseAddr(s); err == nil {
			up.static = append(up.static, a.Unmap())
		}
	}
	if a, err := netip.ParseAddr(up.host); err == nil { // URL with literal IP
		up.static = []netip.Addr{a.Unmap()}
	}
	if o.Timeout <= 0 {
		o.Timeout = 3 * time.Second
	}
	tlsCfg := o.TLSConfig
	if tlsCfg == nil {
		tlsCfg = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	tr := &http.Transport{
		DialContext:           up.dial,
		TLSClientConfig:       tlsCfg,
		ForceAttemptHTTP2:     true, // several providers (mafioznik, malw) reject HTTP/1.1
		MaxIdleConns:          8,
		MaxIdleConnsPerHost:   4,
		IdleConnTimeout:       25 * time.Second, // dns-ai closes idle connections at 30s
		TLSHandshakeTimeout:   o.Timeout,
		ResponseHeaderTimeout: o.Timeout,
		Proxy:                 nil, // never route DNS through an HTTP proxy implicitly
		HTTP2: &http.HTTP2Config{
			SendPingTimeout: 15 * time.Second, // detect dead HTTP/2 connections
			PingTimeout:     5 * time.Second,
		},
	}
	up.client = &http.Client{Transport: tr, Timeout: o.Timeout,
		// DoH endpoints never legitimately redirect; following one could downgrade
		// to http:// or leak queries to another host.
		CheckRedirect: func(*http.Request, []*http.Request) error { return errRedirect }}
	return up, nil
}

// Close releases idle connections.
func (u *Upstream) Close() { u.client.CloseIdleConnections() }

func (u *Upstream) addrs(ctx context.Context, host string) ([]netip.Addr, error) {
	var list []netip.Addr
	if host == u.host && len(u.static) > 0 {
		list = append(list, u.static...)
	} else if u.hr != nil {
		r, err := u.hr.Resolve(ctx, host)
		if err != nil {
			return nil, err
		}
		list = r
	} else {
		return nil, fmt.Errorf("no bootstrap for %s", host)
	}
	u.mu.Lock()
	last := u.lastOK
	now := time.Now()
	var good, demoted []netip.Addr
	for _, a := range list {
		if t, ok := u.bad[a]; ok && now.Sub(t) < badHold {
			demoted = append(demoted, a)
		} else {
			good = append(good, a)
		}
	}
	u.mu.Unlock()
	list = append(good, demoted...)
	if last.IsValid() && len(good) > 0 {
		for i, a := range good {
			if a == last {
				list[0], list[i] = list[i], list[0]
				break
			}
		}
	}
	return list, nil
}

// markBad demotes the address of the current connection after a TLS/HTTP
// failure, so the next dial tries the other bootstrap addresses first.
func (u *Upstream) markBad() {
	u.mu.Lock()
	if u.lastOK.IsValid() {
		if u.bad == nil {
			u.bad = map[netip.Addr]time.Time{}
		}
		u.bad[u.lastOK] = time.Now()
		u.lastOK = netip.Addr{}
	}
	u.mu.Unlock()
	u.client.CloseIdleConnections()
}

// dial connects to the DoH host using bootstrap addresses, trying each in turn.
func (u *Upstream) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	ips, err := u.addrs(ctx, host)
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("no addresses for %s", host)
	}
	return u.race(ctx, ips, port)
}

// dialStagger is the head start each address gets before the next one is tried in parallel.
var dialStagger = 250 * time.Millisecond

// race dials addresses "happy eyeballs" style: a new attempt starts every
// dialStagger (or immediately when one fails); the first success wins and the
// rest are cancelled. A dead bootstrap IP therefore costs ~250 ms, not a full
// connect timeout (on Windows a black-holed SYN otherwise hangs until the deadline).
func (u *Upstream) race(ctx context.Context, ips []netip.Addr, port string) (net.Conn, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type result struct {
		c   net.Conn
		ip  netip.Addr
		err error
	}
	results := make(chan result, len(ips))
	d := net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
	start := func(ip netip.Addr) {
		go func() {
			c, err := d.DialContext(ctx, "tcp", net.JoinHostPort(ip.String(), port))
			results <- result{c, ip, err}
		}()
	}
	next, pending := 0, 0
	launch := func() {
		if next < len(ips) {
			start(ips[next])
			next++
			pending++
		}
	}
	launch()
	timer := time.NewTimer(dialStagger)
	defer timer.Stop()
	var errs []error
	for pending > 0 {
		select {
		case r := <-results:
			pending--
			if r.err == nil {
				u.mu.Lock()
				u.lastOK = r.ip
				u.mu.Unlock()
				cancel()
				// close losers that may still connect
				go func(n int) {
					for i := 0; i < n; i++ {
						if l := <-results; l.c != nil {
							l.c.Close()
						}
					}
				}(pending)
				return r.c, nil
			}
			errs = append(errs, r.err)
			launch() // fail fast: try the next one now
		case <-timer.C:
			launch()
			timer.Reset(dialStagger)
		case <-ctx.Done():
			// remaining goroutines finish on ctx cancel; drain in background
			go func(n int) {
				for i := 0; i < n; i++ {
					if l := <-results; l.c != nil {
						l.c.Close()
					}
				}
			}(pending)
			return nil, ctx.Err()
		}
	}
	return nil, errors.Join(errs...)
}

// Exchange sends a query and returns the response with the original ID restored.
func (u *Upstream) Exchange(ctx context.Context, q *dns.Msg) (*dns.Msg, error) {
	m := q.Copy()
	m.Id = 0 // RFC 8484 §4.1: ID SHOULD be 0 for cache friendliness
	wire, err := m.Pack()
	if err != nil {
		return nil, err
	}
	var req *http.Request
	if u.useGET {
		uu := *u.URL
		qs := uu.Query()
		qs.Set("dns", base64.RawURLEncoding.EncodeToString(wire))
		uu.RawQuery = qs.Encode()
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, uu.String(), nil)
	} else {
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, u.URL.String(), bytes.NewReader(wire))
		if req != nil {
			req.Header.Set("Content-Type", mimeDNS)
		}
	}
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", mimeDNS)
	req.Header.Set("User-Agent", userAgent)
	resp, err := u.client.Do(req)
	if err != nil {
		// Only an explicit cancel is the caller giving up. A deadline (the router's
		// per-upstream timeout) expiring on a stalled TLS handshake is this address's fault.
		if !errors.Is(ctx.Err(), context.Canceled) {
			u.markBad()
		}
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxRespSize+1))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode >= 500 {
			u.markBad()
		}
		return nil, fmt.Errorf("http %d (%s): %s", resp.StatusCode, resp.Proto, snippet(body))
	}
	if len(body) > maxRespSize {
		return nil, errors.New("response too large")
	}
	if ct := resp.Header.Get("Content-Type"); ct != "" && !strings.HasPrefix(ct, mimeDNS) {
		return nil, fmt.Errorf("unexpected content-type %q", ct)
	}
	r := new(dns.Msg)
	if err := r.Unpack(body); err != nil {
		return nil, fmt.Errorf("bad dns message: %w", err)
	}
	if !r.Response || r.Opcode != q.Opcode || len(r.Question) != 1 || len(q.Question) != 1 ||
		!strings.EqualFold(r.Question[0].Name, q.Question[0].Name) ||
		r.Question[0].Qtype != q.Question[0].Qtype || r.Question[0].Qclass != q.Question[0].Qclass {
		return nil, errors.New("response does not match question")
	}
	// RFC 8484 §5.1: an HTTP cache may have held this response; age the TTLs.
	if age, err := strconv.Atoi(resp.Header.Get("Age")); err == nil && age > 0 {
		subtractTTL(r, uint32(age))
	}
	r.Id = q.Id
	r.Question[0].Name = q.Question[0].Name // keep the client's 0x20 case
	return r, nil
}

func subtractTTL(m *dns.Msg, age uint32) {
	for _, sec := range [][]dns.RR{m.Answer, m.Ns, m.Extra} {
		for _, rr := range sec {
			h := rr.Header()
			if h.Rrtype == dns.TypeOPT {
				continue
			}
			if h.Ttl > age {
				h.Ttl -= age
			} else {
				h.Ttl = 0
			}
		}
	}
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 80 {
		s = s[:80]
	}
	return s
}
