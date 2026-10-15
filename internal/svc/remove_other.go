//go:build !windows

package svc

import "os"

func deleteOnReboot(p string) error { return os.Remove(p) }
