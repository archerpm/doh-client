package server

import (
	"context"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"
)

type big struct{}

// Returns 60 A records to force UDP truncation.
func (big) Resolve(ctx context.Context, q *dns.Msg, client string) *dns.Msg {
	m := new(dns.Msg)
	m.SetReply(q)
	for i := 0; i < 60; i++ {
		m.Answer = append(m.Answer, &dns.A{Hdr: dns.RR_Header{Name: q.Question[0].Name, Rrtype: dns.TypeA, Class: 1, Ttl: 60}, A: net.IPv4(10, 0, 0, byte(i))})
	}
	return m
}

func freePort(t *testing.T) string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

func TestUDPTruncationAndTCP(t *testing.T) {
	addr := freePort(t)
	s := New(big{})
	if err := s.Start([]string{addr, "[::1]:" + strings.Split(addr, ":")[1]}); err != nil {
		t.Fatal(err)
	}
	defer s.Stop()
	q := new(dns.Msg)
	q.SetQuestion("x.test.", dns.TypeA)
	r, err := dns.Exchange(q, addr)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Truncated || len(r.Answer) == 60 {
		t.Fatalf("expected truncated UDP reply, tc=%v n=%d", r.Truncated, len(r.Answer))
	}
	c := &dns.Client{Net: "tcp"}
	r, _, err = c.Exchange(q, addr)
	if err != nil || len(r.Answer) != 60 || r.Id != q.Id {
		t.Fatalf("tcp: %v %v", err, r)
	}
	// EDNS 4096 fits everything over UDP
	q.SetEdns0(4096, false)
	r, err = dns.Exchange(q, addr)
	if err != nil || r.Truncated || len(r.Answer) != 60 {
		t.Fatalf("edns: %v tc=%v", err, r.Truncated)
	}
	if err := s.Start([]string{addr}); err == nil {
		t.Fatal("double start should fail")
	}
}

func TestBindConflictRollsBack(t *testing.T) {
	addr := freePort(t)
	busy, _ := net.ListenPacket("udp", "127.0.0.1:"+strings.Split(freePort(t), ":")[1])
	defer busy.Close()
	s := New(big{})
	if err := s.Start([]string{addr, busy.LocalAddr().String()}); err == nil {
		t.Fatal("expected bind error")
	}
	if s.Running() {
		t.Fatal("should not be running")
	}
	// first addr must be released
	l, err := net.ListenPacket("udp", addr)
	if err != nil {
		t.Fatalf("rollback leaked listener: %v", err)
	}
	l.Close()
}

func TestACL(t *testing.T) {
	for s, want := range map[string]bool{"127.0.0.1": true, "::1": true, "192.168.1.5": true, "100.64.1.1": true, "8.8.8.8": false, "2001:db8::1": false} {
		if got := allowed(netip.MustParseAddr(s)); got != want {
			t.Errorf("%s: %v", s, got)
		}
	}
}

type withOPT struct{}

// Upstream-like answer carrying an OPT with padding and a large size.
func (withOPT) Resolve(ctx context.Context, q *dns.Msg, client string) *dns.Msg {
	m := new(dns.Msg)
	m.SetReply(q)
	m.Answer = []dns.RR{&dns.A{Hdr: dns.RR_Header{Name: q.Question[0].Name, Rrtype: dns.TypeA, Class: 1, Ttl: 60}, A: net.IPv4(1, 1, 1, 1)}}
	m.SetEdns0(4096, true)
	o := m.IsEdns0()
	o.Option = append(o.Option, &dns.EDNS0_PADDING{Padding: make([]byte, 64)}, &dns.EDNS0_EDE{InfoCode: dns.ExtendedErrorCodeBlocked})
	return m
}

func TestEDNSNormalization(t *testing.T) {
	addr := freePort(t)
	s := New(withOPT{})
	if err := s.Start([]string{addr}); err != nil {
		t.Fatal(err)
	}
	defer s.Stop()
	q := new(dns.Msg)
	q.SetQuestion("x.test.", dns.TypeA)
	r, err := dns.Exchange(q, addr)
	if err != nil || r.IsEdns0() != nil {
		t.Fatalf("OPT returned to non-EDNS client: %v %v", err, r)
	}
	q.SetEdns0(1400, false)
	r, err = dns.Exchange(q, addr)
	if err != nil {
		t.Fatal(err)
	}
	o := r.IsEdns0()
	if o == nil || o.UDPSize() != localUDPSize || o.Do() {
		t.Fatalf("bad OPT: %v", o)
	}
	var pad, ede bool
	for _, op := range o.Option {
		switch op.(type) {
		case *dns.EDNS0_PADDING:
			pad = true
		case *dns.EDNS0_EDE:
			ede = true
		}
	}
	if pad || !ede {
		t.Fatalf("padding relayed=%v ede kept=%v", pad, ede)
	}
	n := 0
	for _, rr := range r.Extra {
		if _, ok := rr.(*dns.OPT); ok {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d OPT records", n)
	}
}

type badVers struct{}

func (badVers) Resolve(ctx context.Context, q *dns.Msg, client string) *dns.Msg {
	m := new(dns.Msg)
	m.SetRcode(q, dns.RcodeBadVers)
	return m
}

// BADVERS is an extended rcode: it only survives packing via the OPT record.
func TestBadVersOnTheWire(t *testing.T) {
	addr := freePort(t)
	s := New(badVers{})
	if err := s.Start([]string{addr}); err != nil {
		t.Fatal(err)
	}
	defer s.Stop()
	q := new(dns.Msg)
	q.SetQuestion("x.test.", dns.TypeA)
	q.SetEdns0(1232, false)
	q.IsEdns0().SetVersion(1)
	c := &dns.Client{Timeout: 2 * time.Second}
	r, _, err := c.Exchange(q, addr)
	if err != nil {
		t.Fatalf("no reply (packing failed?): %v", err)
	}
	if r.Rcode != dns.RcodeBadVers {
		t.Fatalf("rcode %d", r.Rcode)
	}
}
