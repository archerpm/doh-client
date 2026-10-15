//go:build linux

package elevate

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

func run(exe string, args []string) error {
	if os.Geteuid() == 0 {
		return direct(exe, args)
	}
	pk, err := exec.LookPath("pkexec")
	if err != nil {
		return errors.New("pkexec не найден: выполните вручную `sudo " + exe + " " + strings.Join(args, " ") + "`")
	}
	out, err := exec.Command(pk, append([]string{exe}, args...)...).CombinedOutput()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && (ee.ExitCode() == 126 || ee.ExitCode() == 127) {
			return errors.New("отменено пользователем или нет агента polkit")
		}
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func direct(exe string, args []string) error {
	out, err := exec.Command(exe, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
