//go:build windows

package svc

import "golang.org/x/sys/windows"

func deleteOnReboot(p string) error {
	ptr, err := windows.UTF16PtrFromString(p)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(ptr, nil, windows.MOVEFILE_DELAY_UNTIL_REBOOT)
}
