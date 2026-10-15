package paths

import (
	"os"
	"path/filepath"
	"strings"
)

// OwnerFile stores the identity (Unix uid or Windows SID) of the user allowed to
// control the service through the API. Written at install time.
func OwnerFile() string { return filepath.Join(StateDir(), "owner") }

// ReadOwner returns the stored owner or "".
func ReadOwner() string {
	b, err := os.ReadFile(OwnerFile())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// WriteOwner stores the owner (root/admin-only file).
func WriteOwner(id string) error {
	if err := os.MkdirAll(StateDir(), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(OwnerFile(), []byte(id+"\n"), 0o644); err != nil {
		return err
	}
	return adminOwned(OwnerFile())
}
