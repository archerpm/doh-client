package config

import (
	"os"
	"path/filepath"
	"testing"
)

// A leftover config.json.tmp (e.g. planted in the state dir) must be replaced,
// not written through, and must not survive a successful Save.
func TestSaveReplacesStaleTmp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	victim := filepath.Join(dir, "victim")
	if err := os.WriteFile(victim, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	tmp := path + ".tmp"
	if err := os.Symlink(victim, tmp); err != nil {
		if err := os.WriteFile(tmp, []byte("stale"), 0o600); err != nil { // no symlink rights (Windows)
			t.Fatal(err)
		}
	}
	s := NewStore(path)
	if err := s.Save(Default()); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(victim); string(b) != "keep" {
		t.Fatalf("Save wrote through a planted tmp link: %q", b)
	}
	if _, err := os.Stat(tmp); err == nil {
		t.Fatal("tmp left behind")
	}
	if _, err := s.Load(); err != nil {
		t.Fatal(err)
	}
}
