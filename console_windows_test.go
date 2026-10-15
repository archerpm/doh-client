//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"

	"golang.org/x/sys/windows"
)

func TestConsoleHandleMissing(t *testing.T) {
	for _, tc := range []struct {
		name    string
		handle  windows.Handle
		err     error
		missing bool
	}{
		{"zero", 0, nil, true},
		{"invalid", windows.InvalidHandle, nil, true},
		{"unusable", 123, windows.ERROR_INVALID_HANDLE, true},
		{"usable", 123, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := consoleHandleMissing(tc.handle, tc.err); got != tc.missing {
				t.Fatalf("missing=%v, want %v", got, tc.missing)
			}
		})
	}
}

func TestConsoleFileMissing(t *testing.T) {
	file := consoleTestFile(t)
	closed := consoleTestFile(t)
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	zero := os.NewFile(0, "missing")
	defer zero.Close()
	for _, tc := range []struct {
		name    string
		file    *os.File
		missing bool
	}{
		{"nil", nil, true},
		{"zero", zero, true},
		{"closed", closed, true},
		{"file", file, false},
		{"pipe reader", reader, false},
		{"pipe writer", writer, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := consoleFileMissing(tc.file); got != tc.missing {
				t.Fatalf("missing=%v, want %v", got, tc.missing)
			}
		})
	}
}

func TestAttachConsoleRestoresOnlyMissingStreams(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	inherited := [3]*os.File{reader, consoleTestFile(t), writer}
	for mask := 0; mask < 8; mask++ {
		t.Run(fmt.Sprint(mask), func(t *testing.T) {
			original := inherited
			for i := range original {
				if mask&(1<<i) != 0 {
					original[i] = nil
				}
			}
			want := original
			var opened, wantOpened []string
			for i, device := range [3]string{"CONIN$", "CONOUT$", "CONOUT$"} {
				if original[i] == nil {
					want[i] = consoleTestFile(t)
					wantOpened = append(wantOpened, device)
				}
			}
			attached := false
			next := 0
			got := attachConsoleStreams(original, func() bool { attached = true; return true }, func(device string) (*os.File, error) {
				if !attached {
					t.Fatal("opened console before attaching")
				}
				opened = append(opened, device)
				for next < len(original) && original[next] != nil {
					next++
				}
				if next >= len(original) {
					t.Fatal("opened console for an inherited stream")
				}
				f := want[next]
				next++
				return f, nil
			})
			if !attached || got != want || !reflect.DeepEqual(opened, wantOpened) {
				t.Fatalf("attached=%v streams=%v want=%v devices=%v want=%v", attached, got, want, opened, wantOpened)
			}
		})
	}
}

func TestAttachConsoleSnapshotsStreamsBeforeAttach(t *testing.T) {
	file := consoleTestFile(t)
	original := [3]*os.File{file, file, file}
	got := attachConsoleStreams(original, func() bool {
		// Make the original handle unusable while attaching. A late check would
		// wrongly replace a stream that was inherited and usable beforehand.
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		return true
	}, func(string) (*os.File, error) {
		t.Fatal("stream usability was checked after attaching")
		return nil, errors.New("unexpected open")
	})
	if got != original {
		t.Fatal("replaced an inherited stream")
	}
}

func TestAttachConsoleFailuresPreserveStreams(t *testing.T) {
	original := [3]*os.File{nil, consoleTestFile(t), nil}
	got := attachConsoleStreams(original, func() bool { return false }, func(string) (*os.File, error) {
		t.Fatal("opened console after failed attachment")
		return nil, errors.New("unexpected open")
	})
	if got != original {
		t.Fatal("changed streams after failed attachment")
	}
	calls := 0
	got = attachConsoleStreams(original, func() bool { return true }, func(string) (*os.File, error) {
		calls++
		return nil, errors.New("console unavailable")
	})
	if got != original || calls != 2 {
		t.Fatalf("streams=%v want=%v opens=%d want=2", got, original, calls)
	}
}

func consoleTestFile(t *testing.T) *os.File {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "console-stream")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}
