// Package api is the local control API between the privileged service and the GUI.
package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/archerpm/doh-client/internal/config"
	"github.com/archerpm/doh-client/internal/daemon"
	"github.com/archerpm/doh-client/internal/resolver"
)

const tokenHeader = "X-Doh-Token"

// Server serves the API for a daemon.
type Server struct {
	d     *daemon.Daemon
	addr  string
	token string
	http  *http.Server
	ln    net.Listener
}

// NewServer creates the token file and the HTTP server. owner (uid / SID, may be
// empty) is the only non-admin account allowed to read the token.
func NewServer(d *daemon.Daemon, addr, tokenFile, owner string) (*Server, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	tok := hex.EncodeToString(b)
	if err := os.MkdirAll(filepath.Dir(tokenFile), 0o755); err != nil {
		return nil, err
	}
	if err := writeToken(tokenFile, tok, owner); err != nil {
		return nil, fmt.Errorf("token: %w", err)
	}
	s := &Server{d: d, addr: addr, token: tok}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/status", s.status)
	mux.HandleFunc("GET /v1/config", s.getConfig)
	mux.HandleFunc("PUT /v1/config", s.putConfig)
	mux.HandleFunc("POST /v1/enable", s.enable)
	mux.HandleFunc("POST /v1/reapply-dns", s.reapplyDNS)
	mux.HandleFunc("POST /v1/test", s.test)
	mux.HandleFunc("POST /v1/test-providers", s.testProviders)
	mux.HandleFunc("POST /v1/provider-list", s.providerList)
	mux.HandleFunc("POST /v1/diagnose", s.diagnose)
	mux.HandleFunc("GET /v1/log", s.log)
	mux.HandleFunc("POST /v1/flush", s.flush)
	mux.HandleFunc("GET /v1/defaults", s.defaults)
	s.http = &http.Server{Handler: s.guard(mux), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 30 * time.Second}
	return s, nil
}

// guard enforces loopback Host (anti DNS-rebinding) and the token.
func (s *Server) guard(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != s.addr {
			http.Error(w, "bad host", http.StatusForbidden)
			return
		}
		if subtle.ConstantTimeCompare([]byte(r.Header.Get(tokenHeader)), []byte(s.token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		h.ServeHTTP(w, r)
	})
}

// Listen binds the API address; call it before reporting the service as started.
func (s *Server) Listen() error {
	l, err := net.Listen("tcp", s.addr)
	if err != nil {
		return fmt.Errorf("api listen %s (port busy?): %w", s.addr, err)
	}
	s.ln = l
	return nil
}

// Serve serves on the listener from Listen (binding now if needed) until ctx is cancelled.
func (s *Server) Serve(ctx context.Context) error {
	if s.ln == nil {
		if err := s.Listen(); err != nil {
			return err
		}
	}
	l := s.ln
	serveDone, shutdownDone := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(shutdownDone)
		select {
		case <-ctx.Done():
		case <-serveDone:
		}
		sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.http.Shutdown(sctx)
	}()
	err := s.http.Serve(l)
	close(serveDone)
	<-shutdownDone // Serve returns when listeners close; handlers may still be running.
	if !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

func (s *Server) status(w http.ResponseWriter, r *http.Request)    { writeJSON(w, s.d.Status()) }
func (s *Server) getConfig(w http.ResponseWriter, r *http.Request) { writeJSON(w, s.d.Config()) }
func (s *Server) defaults(w http.ResponseWriter, r *http.Request) {
	c := config.Default()
	if err := c.Normalize(); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, c)
}

func (s *Server) putConfig(w http.ResponseWriter, r *http.Request) {
	var c config.Config
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.d.UpdateConfig(&c); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, s.d.Config())
}

func (s *Server) enable(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.d.SetEnabled(req.Enabled); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, s.d.Status())
}

func (s *Server) reapplyDNS(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1))
	if err != nil || len(body) != 0 {
		writeErr(w, http.StatusBadRequest, errors.New("ожидается пустое тело запроса"))
		return
	}
	if err := s.d.ReapplyDNS(); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) test(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Domain string `json:"domain"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	res, err := s.d.TestDomain(r.Context(), req.Domain)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, res)
}

func (s *Server) providerList(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Provider string `json:"provider"`
		List     string `json:"list"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	res, err := s.d.ProviderList(r.Context(), req.Provider, req.List)
	if errors.Is(err, daemon.ErrNoProviderList) {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, res)
}

func (s *Server) testProviders(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1))
	if err != nil || len(body) != 0 {
		writeErr(w, http.StatusBadRequest, errors.New("ожидается пустое тело запроса"))
		return
	}
	res, err := s.d.TestProviders(r.Context())
	if errors.Is(err, daemon.ErrBypassActive) {
		writeErr(w, http.StatusConflict, err)
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, res)
}

func (s *Server) log(w http.ResponseWriter, r *http.Request) {
	after, _ := strconv.ParseUint(r.URL.Query().Get("after"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 2000 {
		limit = 500
	}
	writeJSON(w, s.d.Router().Log(after, limit))
}

func (s *Server) flush(w http.ResponseWriter, r *http.Request) {
	s.d.FlushCache()
	writeJSON(w, map[string]bool{"ok": true})
}

// Client talks to the service.
type Client struct {
	Addr      string
	TokenFile string
	hc        *http.Client
}

func NewClient(addr, tokenFile string) *Client {
	return &Client{Addr: addr, TokenFile: tokenFile, hc: &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{Proxy: nil}}}
}

// ErrNoService means the service is not running / not installed.
var ErrNoService = errors.New("служба не запущена")

// ErrNoAccess means the service runs but this account may not control it.
var ErrNoAccess = errors.New("нет доступа к службе: она установлена из другой учётной записи — переустановите службу из этой")

func (c *Client) do(method, path string, in, out any) error {
	tok, err := os.ReadFile(c.TokenFile)
	if errors.Is(err, os.ErrPermission) {
		return ErrNoAccess
	}
	if err != nil {
		return ErrNoService
	}
	var body []byte
	if in != nil {
		body, err = json.Marshal(in)
		if err != nil {
			return err
		}
	}
	for attempt := 0; attempt < 2; attempt++ {
		req, err := http.NewRequest(method, "http://"+c.Addr+path, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set(tokenHeader, strings.TrimSpace(string(tok)))
		req.Header.Set("Content-Type", "application/json")
		resp, err := c.hc.Do(req)
		if err != nil {
			var oe *net.OpError
			if errors.As(err, &oe) {
				return ErrNoService
			}
			return err
		}
		if resp.StatusCode == http.StatusUnauthorized {
			resp.Body.Close()
			if attempt == 0 {
				fresh, err := os.ReadFile(c.TokenFile)
				if err == nil && strings.TrimSpace(string(fresh)) != strings.TrimSpace(string(tok)) {
					tok = fresh
					continue // guard rejected the first request before any side effects.
				}
			}
			return ErrNoAccess
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			var e struct {
				Error string `json:"error"`
			}
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			if json.Unmarshal(b, &e) == nil && e.Error != "" {
				return errors.New(e.Error)
			}
			return fmt.Errorf("api %s: %d %s", path, resp.StatusCode, strings.TrimSpace(string(b)))
		}
		if out != nil {
			return json.NewDecoder(resp.Body).Decode(out)
		}
		return nil
	}
	return ErrNoAccess
}

func (c *Client) Status() (st daemon.Status, err error) {
	err = c.do("GET", "/v1/status", nil, &st)
	return
}
func (c *Client) Config() (cfg config.Config, err error) {
	err = c.do("GET", "/v1/config", nil, &cfg)
	return
}
func (c *Client) Defaults() (cfg config.Config, err error) {
	err = c.do("GET", "/v1/defaults", nil, &cfg)
	return
}
func (c *Client) SaveConfig(cfg config.Config) (out config.Config, err error) {
	err = c.do("PUT", "/v1/config", cfg, &out)
	return
}
func (c *Client) SetEnabled(on bool) (st daemon.Status, err error) {
	err = c.do("POST", "/v1/enable", map[string]bool{"enabled": on}, &st)
	return
}
func (c *Client) Test(domain string) (r []daemon.ProbeResult, err error) {
	err = c.do("POST", "/v1/test", map[string]string{"domain": domain}, &r)
	return
}

// ProviderList fetches one domain list a built-in provider publishes.
func (c *Client) ProviderList(provider, list string) (r daemon.ProviderListResult, err error) {
	err = c.do("POST", "/v1/provider-list", map[string]string{"provider": provider, "list": list}, &r)
	return
}

// TestProviders checks the bypass of every provider; the service refuses while the bypass is on.
func (c *Client) TestProviders() (r []daemon.ProviderTest, err error) {
	err = c.do("POST", "/v1/test-providers", nil, &r)
	return
}

func (c *Client) Log(after uint64) (e []resolver.Entry, err error) {
	err = c.do("GET", "/v1/log?after="+strconv.FormatUint(after, 10), nil, &e)
	return
}
func (c *Client) Flush() error { return c.do("POST", "/v1/flush", nil, nil) }

// ReapplyDNS reapplies system DNS without restarting or disabling the proxy.
func (c *Client) ReapplyDNS() error { return c.do("POST", "/v1/reapply-dns", nil, nil) }
