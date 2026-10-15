//go:build darwin

package elevate

import (
	"fmt"
	"os/exec"
	"strings"
)

// shellQuote quotes for /bin/sh.
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// appleQuote quotes for an AppleScript string literal.
func appleQuote(s string) string {
	return `"` + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `"`, `\"`) + `"`
}

func run(exe string, args []string) error {
	parts := []string{shellQuote(exe)}
	for _, a := range args {
		parts = append(parts, shellQuote(a))
	}
	script := "do shell script " + appleQuote(strings.Join(parts, " ")) +
		` with prompt "DoH Client needs administrator rights to manage the DNS service." with administrator privileges`
	out, err := exec.Command("/usr/bin/osascript", "-e", script).CombinedOutput()
	if err != nil {
		s := strings.TrimSpace(string(out))
		if strings.Contains(s, "-128") {
			return fmt.Errorf("отменено пользователем")
		}
		return fmt.Errorf("%w: %s", err, s)
	}
	return nil
}
