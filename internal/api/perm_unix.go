//go:build !windows

package api

import (
	"fmt"
	"os"
	"strconv"
)

// writeToken creates the token readable only by root and, if known, the owner uid.
// Without an owner it stays root-only (fail closed): an admin can still run
// `doh-client service install --owner <uid>`.
func writeToken(path, tok, owner string) error {
	_ = os.Remove(path)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(tok); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if owner == "" {
		return nil
	}
	uid, err := strconv.Atoi(owner)
	if err != nil || uid < 0 {
		return fmt.Errorf("invalid owner uid %q", owner)
	}
	if err := os.Chown(path, uid, -1); err != nil {
		return fmt.Errorf("chown token: %w", err)
	}
	return os.Chmod(path, 0o400)
}

// ValidOwner reports whether id is a usable owner on this platform.
func ValidOwner(id string) bool {
	n, err := strconv.Atoi(id)
	return err == nil && n >= 0
}

// CurrentOwnerID identifies the calling user for `service install --owner`.
func CurrentOwnerID() string { return strconv.Itoa(os.Getuid()) }
