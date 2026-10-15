package api

import (
	"context"
	"encoding/json"
	"github.com/archerpm/doh-client/internal/doh"
	"github.com/archerpm/doh-client/internal/resolver"
	"github.com/miekg/dns"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/archerpm/doh-client/internal/config"
	"github.com/archerpm/doh-client/internal/daemon"
	"github.com/archerpm/doh-client/internal/probe"
)

type diagnoseTransport func(*http.Request) (*http.Response, error)

func (f diagnoseTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestDiagnoseRouteAndGuard(t *testing.T) {
	dir := t.TempDir()
	token := filepath.Join(dir, "token")
	d := daemon.New(config.NewStore(filepath.Join(dir, "config")), filepath.Join(dir, "backup"))
	s, err := NewServer(d, "127.0.0.1:9180", token, CurrentOwnerID())
	if err != nil {
		t.Fatal(err)
	}
	defer diagnoseTokenCleanup(t, token)
	for _, tc := range []struct {
		name, method, host, token, body string
		status                          int
	}{
		{"no auth", "POST", s.addr, "", `{"domain":"site.test"}`, 401},
		{"bad host", "POST", "evil.test", s.token, `{"domain":"site.test"}`, 403},
		{"get disallowed", "GET", s.addr, s.token, "", 405},
		{"bad json", "POST", s.addr, s.token, "{", 400},
		{"invalid domain", "POST", s.addr, s.token, `{"domain":"https://site.test"}`, 400},
		{"trailing json", "POST", s.addr, s.token, `{"domain":"site.test"} {}`, 400},
		{"trailing garbage", "POST", s.addr, s.token, `{"domain":"site.test"} garbage`, 400},
		{"unconfigured", "POST", s.addr, s.token, `{"domain":"site.test"}`, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, "http://"+tc.host+"/v1/diagnose", strings.NewReader(tc.body))
			req.Header.Set(tokenHeader, tc.token)
			w := httptest.NewRecorder()
			s.http.Handler.ServeHTTP(w, req)
			if w.Code != tc.status {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestClientDiagnose(t *testing.T) {
	token := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(token, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	want := daemon.DiagnoseResult{Domain: "site.test", Stages: []daemon.DiagnoseStage{{ID: "rule", Status: "ok", Data: map[string]string{"matched": "site.test"}}}, Verdict: probe.Decide(probe.Evidence{})}
	cl := NewClient("127.0.0.1:9180", token)
	cl.hc.Transport = diagnoseTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method != "POST" || r.URL.Path != "/v1/diagnose" || r.Header.Get(tokenHeader) != "secret" {
			t.Fatalf("%+v", r)
		}
		var req map[string]string
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req["domain"] != "site.test" {
			t.Fatalf("%v %v", req, err)
		}
		b, err := json.Marshal(want)
		if err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(b))), Header: make(http.Header)}, nil
	})
	got, err := cl.Diagnose("site.test")
	if err != nil || got.Domain != want.Domain || got.Verdict != want.Verdict || len(got.Stages) != 1 {
		t.Fatalf("%v %+v", err, got)
	}
	// Valid requests can return partial data with HTTP 200.
	cl.hc.Transport = diagnoseTransport(func(r *http.Request) (*http.Response, error) {
		w := httptest.NewRecorder()
		writeJSON(w, daemon.DiagnoseResult{Domain: "site.test", Stages: []daemon.DiagnoseStage{{ID: "dns", Status: "partial", Data: map[string]any{"providers": []any{}}}}, Verdict: probe.Decide(probe.Evidence{Matched: true})})
		return w.Result(), nil
	})
	got, err = cl.Diagnose("site.test")
	if err != nil || got.Stages[0].Status != "partial" || got.Verdict.Code != "inconclusive" {
		t.Fatalf("%v %+v", err, got)
	}
}

type diagnoseExchange struct{}

func (diagnoseExchange) Exchange(ctx context.Context, q *dns.Msg) (*dns.Msg, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	m := new(dns.Msg)
	m.SetReply(q)
	return m, nil
}

func TestDiagnoseHandlerPartialResponse(t *testing.T) {
	dir := t.TempDir()
	token := filepath.Join(dir, "token")
	d := daemon.New(config.NewStore(filepath.Join(dir, "config")), filepath.Join(dir, "backup"))
	d.Router().SetExchangerFactory(func(config.Provider, *doh.Bootstrap, time.Duration) (resolver.Exchanger, error) {
		return diagnoseExchange{}, nil
	})
	if err := d.Start(); err != nil {
		t.Fatal(err)
	}
	defer d.Stop()
	s, err := NewServer(d, "127.0.0.1:9180", token, CurrentOwnerID())
	if err != nil {
		t.Fatal(err)
	}
	defer diagnoseTokenCleanup(t, token)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest("POST", "http://"+s.addr+"/v1/diagnose", strings.NewReader(`{"domain":"site.test"}`)).WithContext(ctx)
	req.Header.Set(tokenHeader, s.token)
	w := httptest.NewRecorder()
	s.http.Handler.ServeHTTP(w, req)
	var got daemon.DiagnoseResult
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || len(got.Stages) != 5 || got.Domain != "site.test" {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}
