package daemon

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/archerpm/doh-client/internal/config"
	"github.com/archerpm/doh-client/internal/support"
)

func waitServiceTest(t *testing.T, d *Daemon) ServiceTestState {
	t.Helper()
	for i := 0; i < 200; i++ {
		if st := d.ServiceTest(); !st.Running {
			return st
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("service check did not finish")
	return ServiceTestState{}
}

func TestServiceTest(t *testing.T) {
	orig := runSupport
	t.Cleanup(func() { runSupport = orig })
	release := make(chan struct{})
	var asked [][]string
	runSupport = func(ctx context.Context, o support.Options) ([]support.ProviderResult, error) {
		var ids []string
		var out []support.ProviderResult
		for _, p := range o.Providers {
			ids = append(ids, p.ID)
			out = append(out, support.ProviderResult{Provider: p.ID, Services: []string{"Claude", "ChatGPT"}, Unreachable: []string{"ChatGPT"}, Hosts: 9})
		}
		asked = append(asked, ids)
		o.Progress(5, 10)
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return out, nil
	}
	dir := t.TempDir()
	d := New(config.NewStore(filepath.Join(dir, "config.json")), filepath.Join(dir, "backup.json"))
	d.setCfg(config.Default())

	if err := d.StartServiceTest("cloudflare"); !errors.Is(err, ErrNotBypassProvider) {
		t.Fatalf("a non-bypass provider must be refused: %v", err)
	}
	if err := d.StartServiceTest("nope"); err == nil {
		t.Fatal("an unknown provider must be refused")
	}

	if err := d.StartServiceTest("geohide"); err != nil {
		t.Fatal(err)
	}
	if err := d.StartServiceTest("comss"); !errors.Is(err, ErrServiceTestRunning) {
		t.Fatalf("second run at once: %v", err)
	}
	if st := d.ServiceTest(); !st.Running || st.Provider != "geohide" {
		t.Fatalf("running state: %+v", st)
	}
	close(release)
	st := waitServiceTest(t, d)
	if st.Error != "" || len(st.Results) != 1 || st.Results[0].Provider != "geohide" || st.Results[0].Name == "" || st.Results[0].At.IsZero() {
		t.Fatalf("one provider result: %+v", st)
	}

	if r := st.Results[0]; len(r.Services) != 1 || r.Services[0] != "Claude" || len(r.Unreachable) != 1 || r.Unreachable[0] != "ChatGPT" {
		t.Fatalf("reachable and unreachable services must be split: %+v", r)
	}

	// all bypass providers: the geohide result is replaced, nothing else is lost
	if err := d.StartServiceTest(""); err != nil {
		t.Fatal(err)
	}
	st = waitServiceTest(t, d)
	seen := map[string]int{}
	for _, r := range st.Results {
		seen[r.Provider]++
	}
	if len(asked[1]) < 2 || seen["geohide"] != 1 || seen["cloudflare"] != 0 || len(st.Results) != len(asked[1]) {
		t.Fatalf("all providers: asked %v, results %+v", asked[1], st.Results)
	}

	// the bypass on: refused
	d.mu.Lock()
	d.active = true
	d.mu.Unlock()
	if err := d.StartServiceTest(""); !errors.Is(err, ErrBypassActive) {
		t.Fatalf("active bypass: %v", err)
	}
}

func TestServiceTestCancelKeepsOldResults(t *testing.T) {
	orig := runSupport
	t.Cleanup(func() { runSupport = orig })
	first := true
	runSupport = func(ctx context.Context, o support.Options) ([]support.ProviderResult, error) {
		if first {
			first = false
			return []support.ProviderResult{{Provider: "geohide", Services: []string{"Claude"}}}, nil
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	dir := t.TempDir()
	d := New(config.NewStore(filepath.Join(dir, "config.json")), filepath.Join(dir, "backup.json"))
	d.setCfg(config.Default())
	if err := d.StartServiceTest("geohide"); err != nil {
		t.Fatal(err)
	}
	waitServiceTest(t, d)
	if err := d.StartServiceTest("geohide"); err != nil {
		t.Fatal(err)
	}
	d.CancelServiceTest()
	st := waitServiceTest(t, d)
	if st.Error == "" || len(st.Results) != 1 {
		t.Fatalf("a cancelled run must report why and keep the old results: %+v", st)
	}
}
