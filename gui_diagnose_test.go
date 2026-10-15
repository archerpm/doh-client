package main

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/archerpm/doh-client/internal/api"
)

func TestDiagnoseDomainForwardsServiceError(t *testing.T) {
	a := &App{cl: api.NewClient("127.0.0.1:9180", filepath.Join(t.TempDir(), "missing"))}
	if _, err := a.DiagnoseDomain("site.test"); !errors.Is(err, api.ErrNoService) {
		t.Fatalf("%v", err)
	}
}
