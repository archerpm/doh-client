//go:build !windows

package paths

import (
	"fmt"
	"log"
	"os"
	"syscall"
)

// SecureStateDir makes the state directory root-owned and not writable by
// others. A directory or file planted by another user (or a symlink) is not
// trusted: its known files are deleted. Must run as root.
func SecureStateDir() error {
	dir := StateDir()
	fi, err := os.Lstat(dir)
	switch {
	case os.IsNotExist(err):
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	case err != nil:
		return err
	case fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir():
		return fmt.Errorf("state directory %s is not a plain directory", dir)
	default:
		if st, ok := fi.Sys().(*syscall.Stat_t); ok && st.Uid != 0 {
			log.Printf("state directory %s owned by uid %d: not trusted, resetting", dir, st.Uid)
			removeKnown()
		}
	}
	if err := os.Chown(dir, 0, 0); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		return err
	}
	for _, f := range knownFiles() {
		fi, err := os.Lstat(f)
		if err != nil {
			continue
		}
		st, ok := fi.Sys().(*syscall.Stat_t)
		// the token is deliberately owned by the GUI user (read-only)
		if fi.Mode()&os.ModeSymlink != 0 || !fi.Mode().IsRegular() || (ok && st.Uid != 0 && f != TokenFile()) {
			_ = os.Remove(f)
		}
	}
	return nil
}
