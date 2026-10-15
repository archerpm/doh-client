//go:build !windows && !linux

package conflict

import (
	"os/exec"
	"strings"
)

func processNames() ([]string, error) {
	b, err := exec.Command("ps", "-axo", "comm=").Output()
	if err != nil {
		return nil, err
	}
	return strings.Split(string(b), "\n"), nil
}
