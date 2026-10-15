package api

import (
	"context"
	"errors"
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
)

func TestReapplyDNSRouteAndGuard(t *testing.T) {
	dir := t.TempDir()
	token := filepath.Join(dir, "token")
	d := daemon.New(config.NewStore(filepath.Join(dir, "config")), filepath.Join(dir, "backup"))
	if err := d.Start(); err != nil {
		t.Fatal(err)
	}
	defer d.Stop()
	s, err := NewServer(d, "127.0.0.1:9180", token, CurrentOwnerID())
	if err != nil {
		t.Fatal(err)
	}
	defer diagnoseTokenCleanup(t, token)
	for _, tc := range []struct {
		name, method, host, token, body string
		status                          int
	}{
		{"unauthorized", "POST", s.addr, "", "", 401},
		{"bad host", "POST", "evil.test", s.token, "", 403},
		{"wrong method", "GET", s.addr, s.token, "", 405},
		{"nonempty body", "POST", s.addr, s.token, "{}", 400},
		{"whitespace body", "POST", s.addr, s.token, " ", 400},
		{"inactive", "POST", s.addr, s.token, "", 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, "http://"+tc.host+"/v1/reapply-dns", strings.NewReader(tc.body))
			req.Header.Set(tokenHeader, tc.token)
			w := httptest.NewRecorder()
			s.http.Handler.ServeHTTP(w, req)
			if w.Code != tc.status {
				t.Fatalf("%d: %s", w.Code, w.Body.String())
			}
			if tc.name == "inactive" && !strings.Contains(w.Body.String(), "обход выключен") {
				t.Fatalf("not dispatched to daemon: %s", w.Body.String())
			}
		})
	}
}

func TestClientReapplyDNS(t *testing.T) {
	token := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(token, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	cl := NewClient("127.0.0.1:9180", token)
	for _, status := range []int{200, 500} {
		cl.hc.Transport = diagnoseTransport(func(r *http.Request) (*http.Response, error) {
			body, err := io.ReadAll(r.Body)
			if err != nil || len(body) != 0 || r.Method != "POST" || r.URL.Path != "/v1/reapply-dns" || r.Header.Get(tokenHeader) != "secret" {
				t.Fatalf("request=%+v body=%q err=%v", r, body, err)
			}
			return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(`{"error":"apply failed"}`)), Header: make(http.Header)}, nil
		})
		err := cl.ReapplyDNS()
		if status == 200 && err != nil || status == 500 && (err == nil || err.Error() != "apply failed") {
			t.Fatalf("status=%d err=%v", status, err)
		}
	}
}

func TestClientUnauthorizedAndTokenRotation(t *testing.T) {
	for _, scenario := range []string{"stale token", "rotated token", "rotated but unauthorized", "token removed"} {
		t.Run(scenario, func(t *testing.T) {
			token := filepath.Join(t.TempDir(), "token")
			if err := os.WriteFile(token, []byte("old"), 0600); err != nil {
				t.Fatal(err)
			}
			cl := NewClient("127.0.0.1:9180", token)
			calls := 0
			cl.hc.Transport = diagnoseTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				body, err := io.ReadAll(r.Body)
				if err != nil || string(body) != `{"enabled":true}` {
					t.Fatalf("retry lost request body: %q %v", body, err)
				}
				status, text := 401, "unauthorized"
				if calls == 1 {
					if r.Header.Get(tokenHeader) != "old" {
						t.Fatal("wrong initial token")
					}
					switch scenario {
					case "rotated token", "rotated but unauthorized":
						if err := os.WriteFile(token, []byte(" fresh\n"), 0600); err != nil {
							t.Fatal(err)
						}
					case "token removed":
						if err := os.Remove(token); err != nil {
							t.Fatal(err)
						}
					}
				} else {
					if r.Header.Get(tokenHeader) != "fresh" {
						t.Fatal("token not reloaded")
					}
					if scenario == "rotated token" {
						status, text = 200, `{"active":true}`
					}
				}
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(text)), Header: make(http.Header)}, nil
			})
			st, err := cl.SetEnabled(true)
			wantCalls := 1
			if strings.HasPrefix(scenario, "rotated") {
				wantCalls = 2
			}
			if calls != wantCalls {
				t.Fatalf("calls=%d want %d", calls, wantCalls)
			}
			if scenario == "rotated token" {
				if err != nil || !st.Active {
					t.Fatalf("rotation: %+v %v", st, err)
				}
			} else if !errors.Is(err, ErrNoAccess) {
				t.Fatalf("401 must mean no access: %v", err)
			}
		})
	}
}

func TestServeWaitsForShutdownHandlers(t *testing.T) {
	entered, release, shutdownStarted := make(chan struct{}), make(chan struct{}), make(chan struct{})
	s := &Server{addr: freeAddr(t), http: &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); <-release; w.WriteHeader(200) })}}
	s.http.RegisterOnShutdown(func() { close(shutdownStarted) })
	if err := s.Listen(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	served := make(chan error, 1)
	go func() { served <- s.Serve(ctx) }()
	requested := make(chan error, 1)
	go func() {
		resp, err := http.Get("http://" + s.addr)
		if err == nil {
			resp.Body.Close()
		}
		requested <- err
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not start")
	}
	cancel()
	<-shutdownStarted
	select {
	case err := <-served:
		close(release)
		<-requested
		t.Fatalf("Serve returned before handler completed: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if err := <-requested; err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-served:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown did not finish")
	}
}

func TestServeErrorDoesNotWaitForContext(t *testing.T) {
	s := &Server{addr: freeAddr(t), http: &http.Server{}}
	if err := s.Listen(); err != nil {
		t.Fatal(err)
	}
	if err := s.ln.Close(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.Serve(context.Background()) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected closed listener error")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Serve leaked shutdown goroutine")
	}
}
