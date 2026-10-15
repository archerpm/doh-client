package doh

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/miekg/dns"
)

// A dead first bootstrap server must not eat the whole budget of the caller
// (default DoH timeout 3 s): the second server still has to answer.
func TestBootstrapDeadFirstServer(t *testing.T) {
	dead, err := net.ListenPacket("udp", "127.0.0.1:0") // accepts packets, never answers
	if err != nil {
		t.Fatal(err)
	}
	defer dead.Close()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &dns.Server{PacketConn: pc, Handler: dns.HandlerFunc(func(w dns.ResponseWriter, q *dns.Msg) {
		m := new(dns.Msg)
		m.SetReply(q)
		if q.Question[0].Qtype == dns.TypeA {
			m.Answer = append(m.Answer, &dns.A{Hdr: dns.RR_Header{Name: q.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60}, A: net.ParseIP("203.0.113.7")})
		}
		_ = w.WriteMsg(m)
	})}
	go srv.ActivateAndServe()
	defer srv.Shutdown()

	b := NewBootstrap([]string{dead.LocalAddr().String(), pc.LocalAddr().String()})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	addrs, err := b.Resolve(ctx, "doh.example")
	if err != nil || len(addrs) != 1 || addrs[0].String() != "203.0.113.7" {
		t.Fatalf("addrs=%v err=%v", addrs, err)
	}
}
