//go:build !windows

package sysdns

import "os/exec"

func hideWindow(*exec.Cmd) {}
