package daemon

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/archerpm/doh-client/internal/config"
	"github.com/archerpm/doh-client/internal/conflict"
)

func TestStatusWarnsAboutDNSTakingPrograms(t *testing.T) {
	dir := t.TempDir()
	d := New(config.NewStore(filepath.Join(dir, "config.json")), filepath.Join(dir, "backup.json"))
	cfg := config.Default()
	cfg.Mode = config.ModeProxy
	d.setCfg(cfg)
	d.conflicts = conflict.NewDetectorFunc(time.Hour, func() ([]string, error) {
		return []string{"explorer.exe", "sing-box.exe"}, nil
	})
	has := func() bool {
		for _, w := range d.Status().Warnings {
			if strings.Contains(w, "sing-box") {
				return true
			}
		}
		return false
	}
	if has() {
		t.Fatal("inactive client must not warn: its DNS is not in use")
	}
	d.mu.Lock()
	d.active = true
	d.mu.Unlock()
	if !has() {
		t.Fatal("active proxy mode with sing-box running must warn")
	}
	if st := d.Status(); len(st.Conflicts) != 1 || !strings.Contains(st.Conflicts[0], "sing-box") {
		t.Fatalf("Conflicts = %v", st.Conflicts)
	}
	cfg.Mode = config.ModeHosts
	d.setCfg(cfg)
	if has() {
		t.Fatal("hosts mode does not touch DNS, no warning expected")
	}
}
