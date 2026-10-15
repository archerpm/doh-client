//go:build !windows

package api

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestWriteToken(t *testing.T) {
	p := filepath.Join(t.TempDir(), "tok")
	if err := writeToken(p, "abc", ""); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(p); st.Mode().Perm() != 0o600 {
		t.Fatalf("no owner must be root-only (fail closed): %v", st.Mode())
	}
	if err := writeToken(p, "abc", strconv.Itoa(os.Getuid())); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(p); st.Mode().Perm() != 0o400 {
		t.Fatalf("owner: %v", st.Mode())
	}
	if err := writeToken(p, "abc", "S-1-5-21-bogus"); err == nil {
		t.Fatal("malformed owner must be rejected")
	}
	if b, _ := os.ReadFile(p); string(b) != "abc" {
		t.Fatalf("content %q", b)
	}
}
