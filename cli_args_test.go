package main

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestUnknownCommandDoesNotStartGUI(t *testing.T) {
	for _, a := range []string{"statuss", "helpme", "enabel"} {
		if code, handled := cli([]string{a}); !handled || code != 2 {
			t.Fatalf("%s: code=%d handled=%v", a, code, handled)
		}
	}
	for _, a := range []string{"help", "--help", "-h"} {
		if code, handled := cli([]string{a}); !handled || code != 0 {
			t.Fatalf("%s: code=%d handled=%v", a, code, handled)
		}
	}
	// GUI flags still fall through to the GUI
	for _, a := range []string{"--minimized", "-psn_0_12345"} {
		if _, handled := cli([]string{a}); handled {
			t.Fatalf("%s must start the GUI", a)
		}
	}
}

// These tests change process-wide streams and hooks, so they must not run in parallel.
func TestCLIConsoleAttachmentAndOutput(t *testing.T) {
	cases := []struct {
		args           []string
		code           int
		stdout, stderr string
	}{
		{args: []string{"service"}, code: 2, stderr: "usage: doh-client service install|uninstall|start|stop|restart|status|run\n"},
		{args: []string{"service", "unknown"}, code: 2, stderr: "unknown service command unknown\n"},
		{args: []string{"version"}, stdout: version + "\n"},
		{args: []string{"--version"}, stdout: version + "\n"},
		{args: []string{"-v"}, stdout: version + "\n"},
	}
	for _, command := range []string{"service", "run", "restore-dns", "version", "--version", "-v"} {
		cases = append(cases, struct {
			args           []string
			code           int
			stdout, stderr string
		}{args: []string{command, "--json"}, code: 2, stderr: "--json не поддерживается для команды " + command + "\n"})
	}
	for _, tc := range cases {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			calls := 0
			code, handled, stdout, stderr := cliOutput(t, tc.args, func() {
				calls++
				for _, f := range []*os.File{os.Stdout, os.Stderr} {
					st, err := f.Stat()
					if err != nil {
						t.Fatal(err)
					}
					if st.Size() != 0 {
						t.Error("console attached after output")
					}
				}
			})
			if calls != 1 {
				t.Errorf("attachConsole calls = %d, want 1", calls)
			}
			if code != tc.code || !handled || stdout != tc.stdout || stderr != tc.stderr {
				t.Fatalf("code=%d handled=%v stdout=%q stderr=%q; want code=%d stdout=%q stderr=%q", code, handled, stdout, stderr, tc.code, tc.stdout, tc.stderr)
			}
		})
	}
}

func TestGUIArgumentsDoNotAttachConsole(t *testing.T) {
	for _, args := range [][]string{nil, {"--minimized"}, {"-psn_0_12345"}, {"--minimized", "--json"}} {
		t.Run(fmt.Sprint(args), func(t *testing.T) {
			calls := 0
			code, handled, stdout, stderr := cliOutput(t, args, func() { calls++ })
			if calls != 0 || code != 0 || handled || stdout != "" || stderr != "" {
				t.Fatalf("attachConsole calls=%d code=%d handled=%v stdout=%q stderr=%q", calls, code, handled, stdout, stderr)
			}
		})
	}
}

func cliOutput(t *testing.T, args []string, attach func()) (int, bool, string, string) {
	t.Helper()
	out, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	errOut, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatal(err)
	}
	defer errOut.Close()
	oldOut, oldErr, oldAttach := os.Stdout, os.Stderr, attachConsoleCLI
	os.Stdout, os.Stderr, attachConsoleCLI = out, errOut, attach
	defer func() { os.Stdout, os.Stderr, attachConsoleCLI = oldOut, oldErr, oldAttach }()
	code, handled := cli(args)
	stdout, err := os.ReadFile(out.Name())
	if err != nil {
		t.Fatal(err)
	}
	stderr, err := os.ReadFile(errOut.Name())
	if err != nil {
		t.Fatal(err)
	}
	return code, handled, string(stdout), string(stderr)
}
