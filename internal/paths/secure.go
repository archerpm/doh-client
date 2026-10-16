package paths

import (
	"os"
	"path/filepath"
)

// knownFiles are trusted by the privileged service; in an untrusted state
// directory they are deleted rather than read.
func knownFiles() []string {
	d := StateDir()
	return []string{
		filepath.Join(d, "config.json"), filepath.Join(d, "dns-backup.json"), filepath.Join(d, "api.token"),
		filepath.Join(d, "owner"), filepath.Join(d, "hosts.original"), filepath.Join(d, "cli-error.log"),
		filepath.Join(d, "service.log"), filepath.Join(d, "service.log.old"),
		filepath.Join(d, "config.json.tmp"), filepath.Join(d, "dns-backup.json.tmp"),
		filepath.Join(d, "service-check.json"), filepath.Join(d, "service-check.json.tmp"),
	}
}

func removeKnown() {
	for _, f := range knownFiles() {
		_ = os.Remove(f)
	}
}
