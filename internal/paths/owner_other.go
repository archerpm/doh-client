//go:build !windows

package paths

import "os"

// adminOwned: files written by root are already root-owned; make sure of it.
func adminOwned(path string) error { return os.Chown(path, 0, 0) }
