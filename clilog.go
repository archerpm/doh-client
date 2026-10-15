package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/archerpm/doh-client/internal/paths"
)

// logCLIError appends to the service log: elevated runs have no visible console
// (Windows GUI subsystem / osascript / pkexec), so this is where errors surface.
func logCLIError(err error) {
	_ = os.MkdirAll(paths.StateDir(), 0o755)
	f, ferr := os.OpenFile(filepath.Join(paths.StateDir(), "cli-error.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if ferr != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %v\n", time.Now().Format(time.RFC3339), err)
}
