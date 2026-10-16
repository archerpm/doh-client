package daemon

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/archerpm/doh-client/internal/support"
)

// failingRestore is a system DNS whose restore always fails while a backup exists.
type failingRestore struct {
	fakeSystemDNS
	tries int
}

func (f *failingRestore) Restore() error  { f.tries++; return errors.New("netsh: failed") }
func (f *failingRestore) HasBackup() bool { return true }

// Stop tries the restore several times and reports the failure instead of a clean stop.
func TestStopReportsFailedRestore(t *testing.T) {
	old := stopRestoreDelay
	stopRestoreDelay = time.Millisecond
	t.Cleanup(func() { stopRestoreDelay = old })
	d := controlDaemon(t)
	fr := &failingRestore{}
	d.sys = fr
	if err := d.Stop(); err == nil {
		t.Fatal("Stop must report that the system DNS was not restored")
	}
	if fr.tries != stopRestoreTries {
		t.Fatalf("restore tried %d times, want %d", fr.tries, stopRestoreTries)
	}
}

// An explicit but unknown active provider is refused; the running choice stays.
func TestUpdateConfigRejectsUnknownActive(t *testing.T) {
	d := controlDaemon(t)
	cfg := d.Config()
	before := cfg.ActiveProvider
	cfg.ActiveProvider = "ghost"
	if err := d.UpdateConfig(cfg); err == nil {
		t.Fatal("an unknown active provider must be refused")
	}
	if got := d.Config().ActiveProvider; got != before {
		t.Fatalf("active provider changed to %q", got)
	}
}

// Switching the bypass on cancels a running service check: it asks other
// providers directly and must not run next to the one chosen tunnel.
func TestEnablingTheBypassCancelsServiceTest(t *testing.T) {
	orig := runSupport
	t.Cleanup(func() { runSupport = orig })
	runSupport = func(ctx context.Context, o support.Options) ([]support.ProviderResult, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	d := controlDaemon(t)
	cfg := d.Config()
	cfg.Listen = []string{"127.0.0.1:0"}
	d.setCfg(cfg)
	if err := d.StartServiceTest("geohide"); err != nil {
		t.Fatal(err)
	}
	if err := d.SetEnabled(true); err != nil {
		t.Fatal(err)
	}
	if st := waitServiceTest(t, d); st.Running {
		t.Fatalf("the service check is still running: %+v", st)
	}
}
