//go:build windows

package api

import (
	"golang.org/x/sys/windows"
	"testing"
)

// The production token intentionally grants the owner only read access. Restore
// this test file's owner ACL before TempDir cleanup, without changing production.
func diagnoseTokenCleanup(t *testing.T, path string) {
	t.Helper()
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;" + CurrentOwnerID() + ")")
	if err != nil {
		t.Error(err)
		return
	}
	acl, _, err := sd.DACL()
	if err != nil {
		t.Error(err)
		return
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
		t.Error(err)
	}
}
