package doh

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"
)

// h2only mimics providers (mafioznik, malw) that reject HTTP/1.1.
func newServer(t *testing.T, h2only bool, handler func(q *dns.Msg) *dns.Msg) *httptest.Server {
	t.Helper()
	s := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h2only && r.ProtoMajor != 2 {
			http.Error(w, "need h2", http.StatusHTTPVersionNotSupported)
			return
		}
		var body []byte
		switch r.Method {
		case http.MethodPost:
			if r.Header.Get("Content-Type") != mimeDNS {
				http.Error(w, "ct", 415)
				return
			}
			body, _ = io.ReadAll(r.Body)
		case http.MethodGet:
			b, err := decodeB64(r.URL.Query().Get("dns"))
			if err != nil {
				http.Error(w, "b64", 400)
				return
			}
			body = b
		}
		q := new(dns.Msg)
		if err := q.Unpack(body); err != nil || q.Id != 0 {
			http.Error(w, "bad msg or nonzero id", 400)
			return
		}
		out, _ := handler(q).Pack()
		w.Header().Set("Content-Type", mimeDNS)
		w.Write(out)
	}))
	s.EnableHTTP2 = true
	s.StartTLS()
	t.Cleanup(s.Close)
	return s
}

func up(t *testing.T, s *httptest.Server, get bool) *Upstream {
	t.Helper()
	u, _ := url.Parse(s.URL)
	host, port, _ := net.SplitHostPort(u.Host)
	pool := s.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
	x, err := New(Options{ID: "t", URL: "https://doh.test:" + port + "/dns-query", Bootstrap: []string{host},
		UseGET: get, Timeout: 2 * time.Second, TLSConfig: &tls.Config{RootCAs: pool, ServerName: "example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	return x
}

func answer(q *dns.Msg) *dns.Msg {
	m := new(dns.Msg)
	m.SetReply(q)
	m.Answer = []dns.RR{&dns.A{Hdr: dns.RR_Header{Name: q.Question[0].Name, Rrtype: dns.TypeA, Class: 1, Ttl: 60}, A: net.IPv4(1, 2, 3, 4)}}
	return m
}

func TestExchangePOSTandGETOverH2(t *testing.T) {
	s := newServer(t, true, answer)
	for _, get := range []bool{false, true} {
		u := up(t, s, get)
		q := new(dns.Msg)
		q.SetQuestion("example.com.", dns.TypeA)
		q.Id = 777
		r, err := u.Exchange(context.Background(), q)
		if err != nil {
			t.Fatalf("get=%v: %v", get, err)
		}
		if r.Id != 777 || len(r.Answer) != 1 {
			t.Fatalf("get=%v: bad reply %v", get, r)
		}
	}
}

func TestMismatchedQuestionRejected(t *testing.T) {
	s := newServer(t, false, func(q *dns.Msg) *dns.Msg {
		m := answer(q)
		m.Question[0].Name = "evil.com."
		return m
	})
	q := new(dns.Msg)
	q.SetQuestion("example.com.", dns.TypeA)
	if _, err := up(t, s, false).Exchange(context.Background(), q); err == nil || !strings.Contains(err.Error(), "match") {
		t.Fatalf("expected mismatch error, got %v", err)
	}
}

func TestDialFallsBackAcrossBootstrapIPs(t *testing.T) {
	s := newServer(t, false, answer)
	u := up(t, s, false)
	// first IP is a black hole (TEST-NET-1), second is the server
	good := u.static[0]
	u.static = []netipAddr{mustAddr("192.0.2.1"), mustAddr("192.0.2.2"), good}
	q := new(dns.Msg)
	q.SetQuestion("example.com.", dns.TypeA)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	if _, err := u.Exchange(ctx, q); err != nil {
		t.Fatal(err)
	}
	if el := time.Since(start); el > time.Second {
		t.Fatalf("dead bootstrap IP cost %v (should be ~stagger)", el)
	}
	if u.lastOK != good {
		t.Fatalf("lastOK=%v", u.lastOK)
	}
}

func TestHTTPErrorSurfaced(t *testing.T) {
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "nope", 400) }))
	defer s.Close()
	q := new(dns.Msg)
	q.SetQuestion("example.com.", dns.TypeA)
	if _, err := up(t, s, false).Exchange(context.Background(), q); err == nil || !strings.Contains(err.Error(), "http 400") {
		t.Fatalf("got %v", err)
	}
}

func TestRedirectRefusedAndAgeApplied(t *testing.T) {
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/dns-query" {
			http.Redirect(w, r, "http://evil.example/dns-query", http.StatusTemporaryRedirect)
			return
		}
	}))
	defer s.Close()
	q := new(dns.Msg)
	q.SetQuestion("example.com.", dns.TypeA)
	if _, err := up(t, s, false).Exchange(context.Background(), q); err == nil || !strings.Contains(err.Error(), "redirect") {
		t.Fatalf("redirect must be refused, got %v", err)
	}
	aged := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		qq := new(dns.Msg)
		qq.Unpack(body)
		out, _ := answer(qq).Pack() // TTL 60
		w.Header().Set("Content-Type", mimeDNS)
		w.Header().Set("Age", "45")
		w.Write(out)
	}))
	aged.StartTLS()
	defer aged.Close()
	r, err := up(t, aged, false).Exchange(context.Background(), q)
	if err != nil || r.Answer[0].Header().Ttl != 15 {
		t.Fatalf("age not applied: %v %v", err, r)
	}
}

func TestClassMismatchRejected(t *testing.T) {
	s := newServer(t, false, func(q *dns.Msg) *dns.Msg { m := answer(q); m.Question[0].Qclass = dns.ClassCHAOS; return m })
	q := new(dns.Msg)
	q.SetQuestion("example.com.", dns.TypeA)
	if _, err := up(t, s, false).Exchange(context.Background(), q); err == nil {
		t.Fatal("class mismatch accepted")
	}
}

func TestBadAddressDemoted(t *testing.T) {
	// first address accepts TCP but returns 503; second is healthy
	broken := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "x", 503) }))
	broken.StartTLS()
	defer broken.Close()
	u := up(t, broken, false)
	q := new(dns.Msg)
	q.SetQuestion("example.com.", dns.TypeA)
	u.Exchange(context.Background(), q)
	if u.lastOK.IsValid() || len(u.bad) != 1 {
		t.Fatalf("503 address not demoted: last=%v bad=%v", u.lastOK, u.bad)
	}
	ips, _ := u.addrs(context.Background(), u.host)
	u.static = append(u.static, mustAddr("192.0.2.9"))
	ips, _ = u.addrs(context.Background(), u.host)
	if ips[len(ips)-1] != u.static[0] {
		t.Fatalf("demoted address should be last: %v", ips)
	}
}
