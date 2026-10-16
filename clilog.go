package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/archerpm/doh-client/internal/paths"
)

// cliLogMax: the log is dropped and started again past this size.
const cliLogMax = 64 << 10

// logCLIError appends to the service log: elevated runs have no visible console
// (Windows GUI subsystem / osascript / pkexec), so this is where errors surface.
//
// It often runs because the state directory was rejected as untrusted, with
// administrator rights. So it never follows a link, never truncates an existing
// file and writes nowhere that is not a plain directory and a plain file.
func logCLIError(err error) {
	dir := paths.StateDir()
	if fi, serr := os.Lstat(dir); os.IsNotExist(serr) {
		if os.MkdirAll(dir, 0o755) != nil {
			return
		}
	} else if serr != nil || !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
		return
	}
	file := filepath.Join(dir, "cli-error.log")
	if fi, serr := os.Lstat(file); serr == nil {
		if !fi.Mode().IsRegular() {
			return
		}
		if fi.Size() > cliLogMax {
			_ = os.Remove(file)
		}
	}
	f, ferr := os.OpenFile(file, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if ferr != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %v\n", time.Now().Format(time.RFC3339), err)
}
