//go:build windows

package main

import (
	"os"

	"golang.org/x/sys/windows"
)

// attachConsole lets the GUI-subsystem executable print to the terminal it was
// started from (cmd, PowerShell); without it doh-client status shows nothing.
func attachConsole() {
	streams := attachConsoleStreams([3]*os.File{os.Stdin, os.Stdout, os.Stderr}, func() bool {
		k32 := windows.NewLazySystemDLL("kernel32.dll")
		r, _, _ := k32.NewProc("AttachConsole").Call(^uintptr(0)) // ATTACH_PARENT_PROCESS
		return r != 0
	}, func(name string) (*os.File, error) {
		// Each restored stream owns a separate handle. Inherited files and pipes
		// never reach this call, so no unused CONOUT$ handle is opened.
		return os.OpenFile(name, os.O_RDWR, 0)
	})
	os.Stdin, os.Stdout, os.Stderr = streams[0], streams[1], streams[2]
}

func attachConsoleStreams(streams [3]*os.File, attach func() bool, open func(string) (*os.File, error)) [3]*os.File {
	var missing [3]bool
	for i, f := range streams {
		missing[i] = consoleFileMissing(f)
	}
	// AttachConsole changes the WinAPI standard handles, but Go's existing
	// os.File objects retain their original handles. Check them before attaching.
	if !attach() {
		return streams
	}
	for i, name := range [3]string{"CONIN$", "CONOUT$", "CONOUT$"} {
		if missing[i] {
			if f, err := open(name); err == nil {
				streams[i] = f
			}
		}
	}
	return streams
}

func consoleFileMissing(f *os.File) bool {
	if f == nil {
		return true
	}
	h := windows.Handle(f.Fd())
	_, err := windows.GetFileType(h)
	return consoleHandleMissing(h, err)
}

func consoleHandleMissing(h windows.Handle, err error) bool {
	return h == 0 || h == windows.InvalidHandle || err != nil
}
