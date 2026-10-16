package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The error log is appended to, never truncated, and a path that is not a plain
// file (a link or a directory planted in the state directory) is left alone.
func TestLogCLIErrorAppendsAndRefusesOddFiles(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DOH_CLIENT_STATE_DIR", dir)
	logCLIError(errors.New("first"))
	logCLIError(errors.New("second"))
	b, err := os.ReadFile(filepath.Join(dir, "cli-error.log"))
	if err != nil || !strings.Contains(string(b), "first") || !strings.Contains(string(b), "second") {
		t.Fatalf("log must keep both lines: %q %v", b, err)
	}

	odd := t.TempDir()
	t.Setenv("DOH_CLIENT_STATE_DIR", odd)
	if err := os.Mkdir(filepath.Join(odd, "cli-error.log"), 0o755); err != nil { // not a regular file
		t.Fatal(err)
	}
	logCLIError(errors.New("must not be written"))
	if fi, err := os.Stat(filepath.Join(odd, "cli-error.log")); err != nil || !fi.IsDir() {
		t.Fatalf("a directory in place of the log must stay as it is: %v", err)
	}
}
