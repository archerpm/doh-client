// Package probe checks transport and derives diagnostic verdicts without changing settings.
package probe

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/netip"
	"time"
)

type Dialer interface {
	DialContext(context.Context, string, string) (net.Conn, error)
}

type Connection struct {
	Provider string `json:"provider"`
	IP       string `json:"ip"`
	TCP      string `json:"tcp"`
	TLS      string `json:"tls"`
	MS       int64  `json:"ms"`
	Error    string `json:"error,omitempty"`
}

// Connect dials an IP directly and validates TLS with domain as SNI and hostname.
// TLS options are injectable for tests; certificate verification is always enabled.
func Connect(ctx context.Context, dialer Dialer, options *tls.Config, provider, domain string, ip netip.Addr) (r Connection) {
	ip = ip.Unmap()
	r = Connection{Provider: provider, IP: ip.String(), TCP: "pending", TLS: "skipped"}
	start := time.Now()
	defer func() { r.MS = time.Since(start).Milliseconds() }()
	if !ip.IsValid() || ip.IsUnspecified() {
		r.TCP = "skipped"
		r.Error = "некорректный адрес для подключения"
		return
	}
	if ctx.Err() != nil {
		r.Error = "проверка прервана: " + ctx.Err().Error()
		return
	}
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(ip.String(), "443"))
	if err != nil {
		r.TCP = "failed"
		if ctx.Err() != nil {
			r.TCP = "pending"
		}
		r.Error = fmt.Sprintf("ошибка TCP: %v", err)
		return
	}
	defer conn.Close()
	r.TCP = "ok"
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if options != nil {
		cfg = options.Clone()
	}
	cfg.ServerName, cfg.InsecureSkipVerify = domain, false
	tc := tls.Client(conn, cfg)
	if err := tc.HandshakeContext(ctx); err != nil {
		r.TLS = "failed"
		if ctx.Err() != nil {
			r.TLS = "pending"
		}
		r.Error = fmt.Sprintf("ошибка TLS: %v", err)
		return
	}
	r.TLS = "ok"
	return
}

// Addresses selects at most two non-sinkhole addresses of each family.
func Addresses(values []string) []netip.Addr {
	out := []netip.Addr{}
	seen := map[netip.Addr]bool{}
	n4, n6 := 0, 0
	for _, s := range values {
		a, err := netip.ParseAddr(s)
		if err != nil {
			continue
		}
		a = a.Unmap()
		if a.IsUnspecified() {
			continue
		}
		if seen[a] {
			continue
		}
		seen[a] = true
		if a.Is4() {
			if n4 == 2 {
				continue
			}
			n4++
		} else {
			if n6 == 2 {
				continue
			}
			n6++
		}
		out = append(out, a)
	}
	return out
}
