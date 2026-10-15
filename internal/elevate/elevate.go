// Package elevate runs this binary with administrator rights to perform
// privileged one-shot actions (install / uninstall the service).
package elevate

import (
	"os"
	"path/filepath"
)

func self() (string, error) {
	p, err := os.Executable()
	if err != nil {
		return "", err
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		p = r
	}
	return p, nil
}

// Run executes `<self> args...` elevated and waits for it to finish.
func Run(args ...string) error {
	exe, err := self()
	if err != nil {
		return err
	}
	return run(exe, args)
}
