//go:build windows

package conflict

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

func processNames() ([]string, error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(snap)
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	var out []string
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		out = append(out, windows.UTF16ToString(e.ExeFile[:]))
	}
	return out, nil
}
