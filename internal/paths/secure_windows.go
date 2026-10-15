//go:build windows

package paths

import (
	"fmt"
	"log"
	"os"

	"golang.org/x/sys/windows"
)

// stateDirSDDL: owner Administrators; SYSTEM and Administrators full control,
// Users read & execute; protected from %ProgramData% inheritance (which lets
// any user create files there).
const stateDirSDDL = "O:BAD:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;0x1200a9;;;BU)"

func trustedOwner(path string) (bool, error) {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return false, err
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return false, err
	}
	return owner.IsWellKnown(windows.WinBuiltinAdministratorsSid) || owner.IsWellKnown(windows.WinLocalSystemSid), nil
}

func isReparse(path string) bool {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return false
	}
	a, err := windows.GetFileAttributes(p)
	return err == nil && a&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0
}

// SecureStateDir applies an admin-only DACL to the state directory. A directory
// created beforehand by another user is not trusted: its known files are
// deleted. Must run elevated / as SYSTEM.
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
	case !fi.IsDir() || isReparse(dir):
		return fmt.Errorf("state directory %s is not a plain directory", dir)
	default:
		if ok, err := trustedOwner(dir); err != nil || !ok {
			log.Printf("state directory %s has an untrusted owner: resetting", dir)
			removeKnown()
		}
	}
	sd, err := windows.SecurityDescriptorFromString(stateDirSDDL)
	if err != nil {
		return err
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	if err := windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		owner, nil, dacl, nil); err != nil {
		return fmt.Errorf("secure %s: %w", dir, err)
	}
	for _, f := range knownFiles() {
		if _, err := os.Lstat(f); err != nil {
			continue
		}
		if isReparse(f) {
			_ = os.Remove(f)
			continue
		}
		if f == TokenFile() {
			continue // has its own explicit DACL
		}
		if ok, err := trustedOwner(f); err != nil || !ok {
			_ = os.Remove(f)
		}
	}
	return nil
}
