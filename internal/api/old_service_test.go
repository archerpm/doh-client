package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A service built before a route existed answers it with the mux's plain-text
// 404; the client must say the service is outdated, not show "404 page not found".
// A JSON error with 404 (e.g. an unknown provider list) keeps its own text.
func TestClientOldService(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/known", func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, http.StatusNotFound, errors.New("у провайдера нет такого списка"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	tok := filepath.Join(t.TempDir(), "api.token")
	if err := os.WriteFile(tok, []byte("t"), 0o600); err != nil {
		t.Fatal(err)
	}
	cl := NewClient(strings.TrimPrefix(srv.URL, "http://"), tok)
	if _, err := cl.ServiceTest(); !errors.Is(err, ErrOldService) {
		t.Fatalf("unknown route: %v", err)
	}
	if err := cl.do("GET", "/v1/known", nil, nil); err == nil || errors.Is(err, ErrOldService) || !strings.Contains(err.Error(), "нет такого") {
		t.Fatalf("a JSON 404 must keep its own message: %v", err)
	}
}
