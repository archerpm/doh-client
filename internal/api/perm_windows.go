//go:build windows

package api

import (
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// tokenSDDL: SYSTEM and Administrators full control, the owner SID read-only,
// protected (no inheritance from %ProgramData%, which grants Users read).
func tokenSDDL(owner string) string {
	s := "D:P(A;;FA;;;SY)(A;;FA;;;BA)"
	if owner != "" {
		s += "(A;;FR;;;" + owner + ")"
	}
	return s
}

// writeToken creates the file with its final security descriptor in the same
// CreateFile call, so it is never readable by others, not even briefly.
func writeToken(path, tok, owner string) error {
	if owner != "" && !ValidOwner(owner) {
		return fmt.Errorf("invalid owner SID %q", owner)
	}
	_ = os.Remove(path)
	sd, err := windows.SecurityDescriptorFromString(tokenSDDL(owner))
	if err != nil {
		return err
	}
	sa := &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	h, err := windows.CreateFile(p, windows.GENERIC_WRITE, 0, sa, windows.CREATE_NEW, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(h), path)
	if _, err := f.WriteString(tok); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// ValidOwner reports whether id is a well-formed SID.
func ValidOwner(id string) bool {
	_, err := windows.StringToSid(id)
	return err == nil
}

// CurrentOwnerID returns the SID of the user running this process.
func CurrentOwnerID() string {
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return ""
	}
	return u.User.Sid.String()
}
