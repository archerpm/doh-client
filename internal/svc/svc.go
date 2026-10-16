// Package svc installs and runs the daemon as a system service
// (Windows SCM, launchd, systemd) via kardianos/service.
package svc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/archerpm/doh-client/internal/api"
	"github.com/archerpm/doh-client/internal/config"
	"github.com/archerpm/doh-client/internal/daemon"
	"github.com/archerpm/doh-client/internal/hosts"
	"github.com/archerpm/doh-client/internal/paths"
	"github.com/archerpm/doh-client/internal/sysdns"
	"github.com/kardianos/service"
)

type program struct {
	mu     sync.Mutex
	d      *daemon.Daemon
	cancel context.CancelFunc
	done   chan struct{}
}

func (p *program) Start(s service.Service) error {
	if err := paths.SecureStateDir(); err != nil {
		return err
	}
	setupLog()
	hosts.BackupPath = paths.HostsBackup()
	d := daemon.New(config.NewStore(paths.ConfigFile()), paths.BackupFile())
	if err := d.Start(); err != nil {
		return err
	}
	owner := paths.ReadOwner()
	if owner == "" {
		owner = firstEnv("PKEXEC_UID", "SUDO_UID") // `sudo doh-client run`
	}
	srv, err := api.NewServer(d, paths.APIAddr, paths.TokenFile(), owner)
	if err == nil {
		err = srv.Listen() // fail start (→ service manager retries) rather than run uncontrollable
	}
	if err != nil {
		d.Stop() // restores system DNS
		_ = os.Remove(paths.TokenFile())
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	p.mu.Lock()
	p.d, p.cancel, p.done = d, cancel, make(chan struct{})
	p.mu.Unlock()
	go func() {
		defer close(p.done)
		if err := srv.Serve(ctx); err != nil {
			log.Printf("api: %v", err)
		}
	}()
	log.Printf("service started, version %s", daemon.Version)
	return nil
}

func (p *program) Stop(s service.Service) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cancel != nil {
		p.cancel()
		<-p.done
	}
	var stopErr error
	if p.d != nil {
		stopErr = p.d.Stop() // restores system DNS
	}
	_ = os.Remove(paths.TokenFile())
	if stopErr != nil {
		log.Printf("service stopped, system DNS NOT restored: %v", stopErr)
		return stopErr
	}
	log.Printf("service stopped")
	return nil
}

func setupLog() {
	_ = os.MkdirAll(paths.StateDir(), 0o755)
	// Simple size cap: truncate when > 2 MB.
	if st, err := os.Stat(paths.LogFile()); err == nil && st.Size() > 2<<20 {
		_ = os.Rename(paths.LogFile(), paths.LogFile()+".old")
	}
	f, err := os.OpenFile(paths.LogFile(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err == nil {
		log.SetOutput(io.MultiWriter(f, os.Stderr))
	}
}

func newService(exe string) (service.Service, error) {
	cfg := &service.Config{
		Name:        paths.ServiceName,
		DisplayName: "DoH Client",
		Description: "Local DNS-over-HTTPS resolver for bypassing geo-blocking",
		Executable:  exe,
		Arguments:   []string{"service", "run"},
		Option: service.KeyValue{
			"OnFailure":              "restart", // windows
			"OnFailureDelayDuration": "5s",
			"Restart":                "always", // systemd
			"KeepAlive":              true,     // launchd
			"RunAtLoad":              true,
		},
	}
	if runtime.GOOS == "linux" {
		cfg.Dependencies = []string{"After=network.target", "Before=nss-lookup.target", "Wants=nss-lookup.target"}
	}
	return service.New(&program{}, cfg)
}

// Run is called by the service manager (`doh-client service run`).
func Run() error {
	s, err := newService(paths.InstallPath())
	if err != nil {
		return err
	}
	return s.Run()
}

// RunForeground runs the daemon in the terminal until Ctrl+C (`doh-client run`).
func RunForeground() error {
	p := &program{}
	if err := p.Start(nil); err != nil {
		return err
	}
	sig := make(chan os.Signal, 1)
	notifyInterrupt(sig)
	<-sig
	return p.Stop(nil)
}

// Install copies the running binary to the install path and (re)installs the service.
// owner (uid / SID) is the account whose GUI may control the service; when empty
// it is taken from PKEXEC_UID / SUDO_UID on Unix.
func Install(owner string) error {
	if owner == "" {
		owner = firstEnv("PKEXEC_UID", "SUDO_UID")
	}
	if owner != "" && !api.ValidOwner(owner) {
		return fmt.Errorf("invalid --owner %q", owner)
	}
	if err := paths.SecureStateDir(); err != nil {
		return fmt.Errorf("state directory: %w", err)
	}
	if owner != "" {
		if err := paths.WriteOwner(owner); err != nil {
			return fmt.Errorf("store owner: %w", err)
		}
	} else {
		log.Printf("install: owner unknown — only administrators can use the API (pass --owner)")
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	self, _ = filepath.EvalSymlinks(self)
	dst := paths.InstallPath()
	s, err := newService(dst)
	if err != nil {
		return err
	}
	if st, err := s.Status(); err == nil || !errors.Is(err, service.ErrNotInstalled) {
		if st == service.StatusRunning {
			if err := stopService(s); err != nil {
				return fmt.Errorf("старая служба не остановилась, обновление прервано: %w", err)
			}
		}
		// The old service reports a clean stop even when it could not put the
		// system DNS back; the backup on disk is then the only proof. Do not drop
		// the registration (and risk failing to start any service) in that state.
		if m := sysdns.New(paths.BackupFile()); m.HasBackup() {
			if err := m.Restore(); err != nil {
				_ = s.Start()
				return fmt.Errorf("не удалось вернуть исходный DNS, обновление прервано, старая служба запущена снова: %w", err)
			}
		}
		_ = s.Uninstall()
	}
	if !samePath(self, dst) {
		if err := copyFile(self, dst); err != nil {
			return fmt.Errorf("copy binary: %w", err)
		}
	}
	if err := s.Install(); err != nil {
		return fmt.Errorf("install service: %w", err)
	}
	installExtras()
	if err := s.Start(); err != nil {
		return fmt.Errorf("start service: %w", err)
	}
	return nil
}

// Uninstall stops (which restores DNS) and removes the service and its binary.
func Uninstall() error {
	if err := secureIfPresent(); err != nil {
		return err
	}
	s, err := newService(paths.InstallPath())
	if err != nil {
		return err
	}
	if st, _ := s.Status(); st == service.StatusRunning {
		if err := stopService(s); err != nil {
			return fmt.Errorf("служба не остановилась: %w", err)
		}
	}
	// Hosts mode: the service cleans its block on stop, but not if it had crashed.
	if len(hosts.Managed(hosts.Path())) > 0 {
		if err := hosts.Write(hosts.Path(), nil); err != nil {
			_ = s.Start()
			return fmt.Errorf("не удалось очистить hosts, служба не удалена: %w", err)
		}
	}
	// Safety net: the service restores DNS on stop; if it couldn't (or was dead),
	// retry here. If DNS still can't be restored, keep the service so the
	// system doesn't end up pointing at 127.0.0.1 with nothing listening.
	if rerr := sysdns.New(paths.BackupFile()).Restore(); rerr != nil {
		log.Printf("restore dns: %v", rerr)
		_ = s.Start()
		return fmt.Errorf("не удалось вернуть исходный DNS, служба не удалена и снова запущена: %w", rerr)
	}
	// Unregister first: if that fails the binary, token and owner must stay, or the
	// still-registered service would point at a missing executable.
	if err = s.Uninstall(); err != nil && !errors.Is(err, service.ErrNotInstalled) {
		return fmt.Errorf("не удалось удалить регистрацию службы: %w", err)
	}
	_ = os.Remove(paths.TokenFile())
	_ = os.Remove(paths.OwnerFile())
	removeExtras()
	removeBinary(paths.InstallPath())
	return nil
}

// Control runs start/stop/restart.
func Control(action string) error {
	s, err := newService(paths.InstallPath())
	if err != nil {
		return err
	}
	return service.Control(s, action)
}

// Status returns "running", "stopped", "not_installed" or "unknown".
func Status() string {
	s, err := newService(paths.InstallPath())
	if err != nil {
		return "unknown"
	}
	st, err := s.Status()
	switch {
	case errors.Is(err, service.ErrNotInstalled):
		return "not_installed"
	case err != nil:
		return "unknown"
	case st == service.StatusRunning:
		return "running"
	case st == service.StatusStopped:
		return "stopped"
	}
	return "unknown"
}

// RestoreDNS is an emergency command (`doh-client restore-dns`). A running
// service would re-apply its DNS within seconds, so it is stopped first.
func RestoreDNS() error {
	if err := secureIfPresent(); err != nil {
		return err
	}
	if s, err := newService(paths.InstallPath()); err == nil {
		if st, _ := s.Status(); st == service.StatusRunning {
			if err := stopService(s); err != nil {
				return fmt.Errorf("stop service first: %w", err)
			}
		}
	}
	// Both cleanups run even if the first fails: leftovers of either keep steering traffic.
	errs := []error{sysdns.New(paths.BackupFile()).Restore()}
	if len(hosts.Managed(hosts.Path())) > 0 {
		errs = append(errs, hosts.Write(hosts.Path(), nil))
	}
	return errors.Join(errs...)
}

// secureIfPresent vets the state directory before a DNS backup in it is applied
// with administrator rights: a directory that someone else prepared must not be
// trusted. Without a directory there is no backup, so nothing is created.
func secureIfPresent() error {
	if _, err := os.Lstat(paths.StateDir()); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("state directory: %w", err)
	}
	if err := paths.SecureStateDir(); err != nil {
		return fmt.Errorf("state directory: %w", err)
	}
	return nil
}

// removeBinary deletes the installed binary; the service process may hold it
// for a moment after stopping, so retry, then fall back to delete-on-reboot.
func removeBinary(p string) {
	for i := 0; i < 20; i++ {
		if err := os.Remove(p); err == nil || errors.Is(err, os.ErrNotExist) {
			_ = os.Remove(filepath.Dir(p)) // only succeeds if empty
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	if err := deleteOnReboot(p); err != nil {
		log.Printf("cannot remove %s: %v", p, err)
	}
}

func firstEnv(keys ...string) string {
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}

func waitStopped(s service.Service) {
	for i := 0; i < 50; i++ {
		if st, err := s.Status(); err != nil || st != service.StatusRunning {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func samePath(a, b string) bool {
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		return equalFold(filepath.Clean(a), filepath.Clean(b))
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		x, y := a[i], b[i]
		if 'A' <= x && x <= 'Z' {
			x += 32
		}
		if 'A' <= y && y <= 'Z' {
			y += 32
		}
		if x != y {
			return false
		}
	}
	return true
}

func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".new"
	// A planted tmp (symlink) must not be followed by the privileged installer.
	_ = os.Remove(tmp)
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	// On Windows the old binary may still be locked briefly after the service stops.
	var rerr error
	for i := 0; i < 20; i++ {
		if rerr = os.Rename(tmp, dst); rerr == nil {
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return rerr
}
