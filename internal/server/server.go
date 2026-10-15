// Package server exposes the router as a local DNS server over UDP and TCP.
package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/miekg/dns"
)

// Resolver answers queries.
type Resolver interface {
	Resolve(ctx context.Context, q *dns.Msg, client string) *dns.Msg
}

// Server runs DNS listeners on a set of addresses.
type Server struct {
	res     Resolver
	mu      sync.Mutex
	servers []*dns.Server
	socks   []io.Closer // socks[i] is the socket behind servers[i]
	addrs   []string
}

// shutdown stops srv and closes its socket. dns.Server.Shutdown fails while the
// serving goroutine has not started yet, which would leave the port bound.
func shutdown(srv *dns.Server, sock io.Closer) {
	_ = srv.Shutdown()
	_ = sock.Close()
}

func New(r Resolver) *Server { return &Server{res: r} }

// allowed: loopback, private (RFC1918/ULA), link-local, CGNAT.
func allowed(a netip.Addr) bool {
	a = a.Unmap()
	if a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() {
		return true
	}
	return netip.MustParsePrefix("100.64.0.0/10").Contains(a)
}

func (s *Server) handler() dns.Handler {
	return dns.HandlerFunc(func(w dns.ResponseWriter, req *dns.Msg) {
		var ip netip.Addr
		proto := "udp"
		switch ra := w.RemoteAddr().(type) {
		case *net.UDPAddr:
			ip, _ = netip.AddrFromSlice(ra.IP)
		case *net.TCPAddr:
			ip, _ = netip.AddrFromSlice(ra.IP)
			proto = "tcp"
		}
		if !allowed(ip) {
			m := new(dns.Msg)
			m.SetRcode(req, dns.RcodeRefused)
			_ = w.WriteMsg(m)
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		resp := s.res.Resolve(ctx, req, ip.Unmap().String()).Copy()
		resp.Id = req.Id
		normalizeEDNS(req, resp)
		if proto == "udp" {
			size := dns.MinMsgSize
			if o := req.IsEdns0(); o != nil && int(o.UDPSize()) > size {
				size = int(o.UDPSize())
			}
			resp.Truncate(size)
		}
		_ = w.WriteMsg(resp)
	})
}

// localUDPSize is advertised to clients (DNS flag day 2020 recommendation).
const localUDPSize = 1232

// normalizeEDNS makes the OPT record reflect *this* hop (RFC 6891): no OPT if
// the client sent none; otherwise exactly one OPT with our size and the client's DO bit.
// Upstream EDNS options (padding, cookies, ECS) are not relayed.
func normalizeEDNS(req, resp *dns.Msg) {
	extra := resp.Extra[:0]
	var ede []dns.EDNS0
	for _, rr := range resp.Extra {
		if o, ok := rr.(*dns.OPT); ok {
			for _, opt := range o.Option {
				if e, ok := opt.(*dns.EDNS0_EDE); ok { // keep Extended DNS Errors (e.g. "Blocked")
					ede = append(ede, e)
				}
			}
			continue
		}
		extra = append(extra, rr)
	}
	resp.Extra = extra
	if ro := req.IsEdns0(); ro != nil {
		resp.SetEdns0(localUDPSize, ro.Do())
		if o := resp.IsEdns0(); o != nil {
			o.Option = append(o.Option, ede...)
		}
	}
}

// Start binds all addresses; on any failure, already-started listeners are stopped.
func (s *Server) Start(addrs []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.servers) > 0 {
		return errors.New("already running")
	}
	h := s.handler()
	var started []*dns.Server
	var socks []io.Closer
	var bound []string
	fail := func(err error) error {
		for i, srv := range started {
			shutdown(srv, socks[i])
		}
		return err
	}
	for _, a := range addrs {
		ap, err := netip.ParseAddrPort(a)
		if err != nil {
			return fail(err)
		}
		// IPv6 loopback may be unavailable (IPv6 disabled); skip it instead of failing.
		optional := ap.Addr().Is6() && ap.Addr().IsLoopback() && len(addrs) > 1
		udp, err := net.ListenPacket("udp", a)
		if err != nil {
			if optional {
				continue
			}
			return fail(bindErr(a, err))
		}
		tcp, err := net.Listen("tcp", a)
		if err != nil {
			udp.Close()
			if optional {
				continue
			}
			return fail(bindErr(a, err))
		}
		us := &dns.Server{PacketConn: udp, Handler: h, UDPSize: dns.DefaultMsgSize}
		ts := &dns.Server{Listener: tcp, Handler: h, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: func() time.Duration { return 10 * time.Second }}
		for i, srv := range []*dns.Server{us, ts} {
			sock := io.Closer(udp)
			if i == 1 {
				sock = tcp
			}
			ready := make(chan struct{})
			srv.NotifyStartedFunc = func() { close(ready) }
			go func(srv *dns.Server) { _ = srv.ActivateAndServe() }(srv)
			select {
			case <-ready:
			case <-time.After(2 * time.Second):
			}
			started = append(started, srv)
			socks = append(socks, sock)
		}
		bound = append(bound, a)
	}
	if len(started) == 0 {
		return errors.New("no listen addresses could be bound")
	}
	s.servers, s.socks, s.addrs = started, socks, bound
	return nil
}

func bindErr(a string, err error) error {
	return fmt.Errorf("cannot listen on %s (port busy or no admin rights?): %w", a, err)
}

// Stop shuts all listeners down.
func (s *Server) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, srv := range s.servers {
		shutdown(srv, s.socks[i])
	}
	s.servers, s.socks, s.addrs = nil, nil, nil
}

// Addrs returns the bound addresses.
func (s *Server) Addrs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.addrs...)
}

func (s *Server) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.servers) > 0
}
