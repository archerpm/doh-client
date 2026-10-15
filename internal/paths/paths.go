// Package paths centralizes on-disk locations.
package paths

import (
	"os"
	"path/filepath"
	"runtime"
)

const (
	ServiceName = "doh-client"
	APIAddr     = "127.0.0.1:47153"
)

// StateDir holds config, DNS backup and the API token (system-wide, owned by the service).
func StateDir() string {
	if d := os.Getenv("DOH_CLIENT_STATE_DIR"); d != "" {
		return d
	}
	switch runtime.GOOS {
	case "windows":
		pd := os.Getenv("ProgramData")
		if pd == "" {
			pd = `C:\ProgramData`
		}
		return filepath.Join(pd, "DohClient")
	case "darwin":
		return "/Library/Application Support/DohClient"
	default:
		return "/var/lib/doh-client"
	}
}

// InstallPath is where the service binary is copied to.
func InstallPath() string {
	switch runtime.GOOS {
	case "windows":
		pf := os.Getenv("ProgramFiles")
		if pf == "" {
			pf = `C:\Program Files`
		}
		return filepath.Join(pf, "DohClient", "doh-client.exe")
	case "darwin":
		return "/Library/Application Support/DohClient/bin/doh-client"
	default:
		return "/usr/local/lib/doh-client/doh-client"
	}
}

func ConfigFile() string  { return filepath.Join(StateDir(), "config.json") }
func BackupFile() string  { return filepath.Join(StateDir(), "dns-backup.json") }
func TokenFile() string   { return filepath.Join(StateDir(), "api.token") }
func LogFile() string     { return filepath.Join(StateDir(), "service.log") }
func HostsBackup() string { return filepath.Join(StateDir(), "hosts.original") }
