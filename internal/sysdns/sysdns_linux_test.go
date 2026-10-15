//go:build linux

package sysdns

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGlobalHasDNS(t *testing.T) {
	with := "Global\n         Protocols: +LLMNR\n  resolv.conf mode: stub\n       DNS Servers: 9.9.9.9\n\nLink 2 (eth0)\n    DNS Servers: 192.168.1.1\n"
	without := "Global\n         Protocols: +LLMNR\n\nLink 2 (eth0)\n    DNS Servers: 192.168.1.1\n"
	if !globalHasDNS(with) || globalHasDNS(without) {
		t.Fatal("globalHasDNS wrong")
	}
}

// File mode end-to-end on a temp resolv.conf (symlink and regular file).
func TestResolvConfApplyRestore(t *testing.T) {
	dir := t.TempDir()
	for _, symlink := range []bool{false, true} {
		target := filepath.Join(dir, "real.conf")
		os.WriteFile(target, []byte("nameserver 192.168.1.1\n"), 0o644)
		resolvConf = filepath.Join(dir, "resolv.conf")
		os.Remove(resolvConf)
		if symlink {
			os.Symlink(target, resolvConf)
		} else {
			os.WriteFile(resolvConf, []byte("nameserver 192.168.1.1\n"), 0o644)
		}
		var l linuxDNS
		b := &Backup{Items: map[string]*Item{}}
		saves := 0
		if err := l.ensureFile([]string{"127.0.0.1", "::1"}, b, func() error { saves++; return nil }); err != nil {
			t.Fatal(err)
		}
		got, _ := os.ReadFile(resolvConf)
		if !strings.Contains(string(got), "nameserver 127.0.0.1") || saves != 1 {
			t.Fatalf("apply: %q saves=%d", got, saves)
		}
		// idempotent
		if err := l.ensureFile([]string{"127.0.0.1"}, b, func() error { saves++; return nil }); err != nil || saves != 1 {
			t.Fatal("not idempotent")
		}
		if err := l.restore(b); err != nil {
			t.Fatal(err)
		}
		if symlink {
			if l, err := os.Readlink(resolvConf); err != nil || l != target {
				t.Fatalf("symlink not restored: %v %v", l, err)
			}
		} else if got, _ := os.ReadFile(resolvConf); string(got) != "nameserver 192.168.1.1\n" {
			t.Fatalf("content not restored: %q", got)
		}
	}
}

func TestManagerCrashSafety(t *testing.T) {
	dir := t.TempDir()
	resolvConf = filepath.Join(dir, "resolv.conf")
	os.WriteFile(resolvConf, []byte("nameserver 10.0.0.1\n"), 0o644)
	m := &Manager{path: filepath.Join(dir, "state", "backup.json"), p: fileOnly{}}
	if err := m.Apply([]string{"127.0.0.1"}); err != nil {
		t.Fatal(err)
	}
	if !m.HasBackup() || !m.Status().Applied {
		t.Fatal("backup missing")
	}
	// simulate crash: new manager instance restores from disk
	m2 := &Manager{path: m.path, p: fileOnly{}}
	if err := m2.Restore(); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(resolvConf); string(got) != "nameserver 10.0.0.1\n" || m2.HasBackup() {
		t.Fatalf("restore after crash failed: %q", got)
	}
}

type fileOnly struct{ linuxDNS }

func (fileOnly) ensure(s []string, b *Backup, save func() error) ([]string, error) {
	return nil, linuxDNS{}.ensureFile(s, b, save)
}
func (fileOnly) flush() {}

func TestResolvConfKeepsSearchAndReappliesOnServerChange(t *testing.T) {
	dir := t.TempDir()
	resolvConf = filepath.Join(dir, "resolv.conf")
	os.WriteFile(resolvConf, []byte("nameserver 10.0.0.1\nsearch corp.example\noptions ndots:2\n"), 0o644)
	var l linuxDNS
	b := &Backup{Items: map[string]*Item{}}
	save := func() error { return nil }
	if err := l.ensureFile([]string{"127.0.0.1"}, b, save); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(resolvConf)
	if !strings.Contains(string(got), "search corp.example") || !strings.Contains(string(got), "options ndots:2") {
		t.Fatalf("search/options lost:\n%s", got)
	}
	// listener changed: managed file must be rewritten, original backup kept
	if err := l.ensureFile([]string{"127.0.0.2"}, b, save); err != nil {
		t.Fatal(err)
	}
	got, _ = os.ReadFile(resolvConf)
	if !strings.Contains(string(got), "nameserver 127.0.0.2") || strings.Contains(string(got), "127.0.0.1") {
		t.Fatalf("stale nameserver:\n%s", got)
	}
	if !strings.Contains(string(b.ResolvConf.Content), "10.0.0.1") {
		t.Fatal("original backup overwritten by managed content")
	}
	// interrupted restore left no file: the saved original must survive
	os.Remove(resolvConf)
	if err := l.ensureFile([]string{"127.0.0.2"}, b, save); err != nil {
		t.Fatal(err)
	}
	if b.ResolvConf.Missing || !strings.Contains(string(b.ResolvConf.Content), "10.0.0.1") {
		t.Fatal("missing file replaced the real original in the backup")
	}
	if err := l.restore(b); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(resolvConf); string(got) != "nameserver 10.0.0.1\nsearch corp.example\noptions ndots:2\n" {
		t.Fatalf("restore: %q", got)
	}
}
