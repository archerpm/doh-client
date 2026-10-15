//go:build linux

package conflict

import (
	"os"
	"strconv"
	"strings"
)

func processNames() ([]string, error) {
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range ents {
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue
		}
		b, err := os.ReadFile("/proc/" + e.Name() + "/comm")
		if err != nil {
			continue // the process exited or is not readable
		}
		out = append(out, strings.TrimSpace(string(b)))
	}
	return out, nil
}
