//go:build !windows

package paths

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestSecureStateDirDistrustsForeignOwner(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root")
	}
	base := t.TempDir()
	dir := filepath.Join(base, "state")
	t.Setenv("DOH_CLIENT_STATE_DIR", dir)
	os.MkdirAll(dir, 0o777)
	os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"evil":true}`), 0o666)
	os.WriteFile(filepath.Join(dir, "owner"), []byte("1000"), 0o666)
	os.Chown(dir, 1000, 1000)
	os.Chown(filepath.Join(dir, "config.json"), 1000, 1000)
	if err := SecureStateDir(); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(dir)
	if st := fi.Sys().(*syscall.Stat_t); st.Uid != 0 || fi.Mode().Perm() != 0o755 {
		t.Fatalf("dir not secured: uid=%d mode=%v", st.Uid, fi.Mode())
	}
	for _, f := range []string{"config.json", "owner"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err == nil {
			t.Fatalf("%s planted by another user was trusted", f)
		}
	}
	// a trusted dir keeps root-owned files; a planted symlink is removed
	os.WriteFile(filepath.Join(dir, "config.json"), []byte("{}"), 0o644)
	os.Symlink("/etc/shadow", filepath.Join(dir, "dns-backup.json"))
	if err := SecureStateDir(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "config.json")); err != nil {
		t.Fatal("trusted config removed")
	}
	if _, err := os.Lstat(filepath.Join(dir, "dns-backup.json")); err == nil {
		t.Fatal("symlink kept")
	}
	// the directory itself as a symlink is refused
	t.Setenv("DOH_CLIENT_STATE_DIR", filepath.Join(base, "link"))
	os.Symlink(dir, filepath.Join(base, "link"))
	if err := SecureStateDir(); err == nil {
		t.Fatal("symlinked state dir accepted")
	}
}
