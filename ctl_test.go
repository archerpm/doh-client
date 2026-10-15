package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/archerpm/doh-client/internal/api"
)

func TestCtlRejectsInvalidArgumentsBeforeSideEffects(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.WriteHeader(500) }))
	defer srv.Close()
	dir := t.TempDir()
	token, file := filepath.Join(dir, "token"), filepath.Join(dir, "config.json")
	if err := os.WriteFile(token, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	cl := api.NewClient(strings.TrimPrefix(srv.URL, "http://"), token)
	for _, args := range [][]string{
		{"config", "export", file, "extra"},
		{"config", "export", file, "extra", "more"},
		{"flush", "--json"},
		{"config", "export", "--json"},
		{"config", "export", file, "--json"},
		{"config", "import", file, "--json"},
		{"config", "import", "-", "--json"},
		{"reapply-dns", "--json"},
		{"reapply-dns", "extra"},
		{"reapply-dns", "--jsno"},
	} {
		if code, handled := ctlWithClient(cl, args); code != 2 || !handled {
			t.Fatalf("%v: %d %v", args, code, handled)
		}
	}
	if requests.Load() != 0 {
		t.Fatalf("invalid arguments made %d requests", requests.Load())
	}
	if data, err := os.ReadFile(file); err != nil || string(data) != "keep" {
		t.Fatalf("export overwrote file: %q %v", data, err)
	}
}

func TestCLIRejectsUnsupportedJSON(t *testing.T) {
	for _, args := range [][]string{
		{"service", "install", "--json"}, {"service", "uninstall", "--json"}, {"service", "start", "--json"},
		{"service", "status", "--json"}, {"service", "run", "--json"},
		{"run", "--json"}, {"restore-dns", "--json"}, {"version", "--json"},
	} {
		if code, handled := cli(args); code != 2 || !handled {
			t.Fatalf("%v: %d %v", args, code, handled)
		}
	}
}

func TestCtlReapplyDNSExitCodes(t *testing.T) {
	for _, tc := range []struct {
		name         string
		status, code int
	}{
		{"success", 200, 0}, {"apply failure", 500, 1}, {"no access", 401, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				body, err := io.ReadAll(r.Body)
				if err != nil || len(body) != 0 || r.Method != "POST" || r.URL.Path != "/v1/reapply-dns" {
					t.Errorf("invalid request: %+v body=%q err=%v", r, body, err)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(`{"error":"apply failed"}`))
			}))
			defer srv.Close()
			token := filepath.Join(t.TempDir(), "token")
			if err := os.WriteFile(token, []byte("secret"), 0600); err != nil {
				t.Fatal(err)
			}
			cl := api.NewClient(strings.TrimPrefix(srv.URL, "http://"), token)
			if code, handled := ctlWithClient(cl, []string{"reapply-dns"}); code != tc.code || !handled {
				t.Fatalf("%d %v", code, handled)
			}
			if requests.Load() != 1 {
				t.Fatalf("requests=%d", requests.Load())
			}
		})
	}
	cl := api.NewClient("127.0.0.1:9180", filepath.Join(t.TempDir(), "missing"))
	if code, handled := ctlWithClient(cl, []string{"reapply-dns"}); code != 3 || !handled {
		t.Fatalf("no service: %d %v", code, handled)
	}
}

func TestAppReapplyDNSForwardsError(t *testing.T) {
	a := &App{cl: api.NewClient("127.0.0.1:9180", filepath.Join(t.TempDir(), "missing"))}
	if err := a.ReapplyDNS(); err != api.ErrNoService {
		t.Fatalf("%v", err)
	}
}
