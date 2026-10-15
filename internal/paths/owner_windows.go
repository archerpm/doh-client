//go:build windows

package paths

import "golang.org/x/sys/windows"

// adminOwned makes BUILTIN\Administrators the owner of a file written by the
// elevated installer. With the "object creator" owner policy the file would
// otherwise belong to the user and be discarded as untrusted by SecureStateDir.
func adminOwned(path string) error {
	sid, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION, sid, nil, nil, nil)
}
