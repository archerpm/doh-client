package hosts

import (
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRenderRoundTrip(t *testing.T) {
	orig := "127.0.0.1 localhost\r\n::1 localhost\r\n\r\n"
	e := map[string][]netip.Addr{"claude.ai": {netip.MustParseAddr("1.2.3.4")}, "a.com": {netip.MustParseAddr("5.6.7.8")}}
	out := string(Render([]byte(orig), e))
	if !strings.Contains(out, "\r\n1.2.3.4 claude.ai\r\n") || strings.Index(out, "a.com") > strings.Index(out, "claude.ai") {
		t.Fatalf("bad render:\n%q", out)
	}
	// re-render is stable and replaces, not duplicates
	out2 := string(Render([]byte(out), e))
	if out2 != out {
		t.Fatalf("not idempotent:\n%q\n%q", out, out2)
	}
	clean := string(Render([]byte(out), nil))
	if clean != "127.0.0.1 localhost\r\n::1 localhost\r\n" {
		t.Fatalf("remove: %q", clean)
	}
}

func TestWriteAndManaged(t *testing.T) {
	p := filepath.Join(t.TempDir(), "hosts")
	os.WriteFile(p, []byte("10.0.0.1 router\n"), 0o640)
	if err := Write(p, map[string][]netip.Addr{"x.com": {netip.MustParseAddr("9.9.9.9")}}); err != nil {
		t.Fatal(err)
	}
	if m := Managed(p); len(m) != 1 || m[0] != "x.com" {
		t.Fatalf("%v", m)
	}
	st, _ := os.Stat(p)
	if runtime.GOOS != "windows" && st.Mode().Perm() != 0o640 { // no unix modes on Windows
		t.Fatalf("mode %v", st.Mode())
	}
	b, _ := os.ReadFile(p)
	if !strings.HasPrefix(string(b), "10.0.0.1 router\n") {
		t.Fatalf("user content lost: %q", b)
	}
}

func TestWriteBacksUpOriginalOnce(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "hosts")
	BackupPath = filepath.Join(dir, "state", "hosts.original")
	defer func() { BackupPath = "" }()
	os.WriteFile(p, []byte("10.0.0.1 nas\n"), 0o644)
	Write(p, map[string][]netip.Addr{"a.com": {netip.MustParseAddr("1.1.1.1")}})
	Write(p, map[string][]netip.Addr{"b.com": {netip.MustParseAddr("2.2.2.2")}})
	if b, _ := os.ReadFile(BackupPath); string(b) != "10.0.0.1 nas\n" {
		t.Fatalf("backup = %q", b)
	}
	if _, err := os.Stat(p + ".doh-client.tmp"); err == nil {
		t.Fatal("temp file left behind")
	}
}
