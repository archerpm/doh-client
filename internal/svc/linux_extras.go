//go:build linux

package svc

import (
	_ "embed"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/archerpm/doh-client/internal/paths"
)

//go:embed assets/doh-client.png
var iconPNG []byte

const (
	apparmorProfile = "/etc/apparmor.d/doh-client"
	desktopFile     = "/usr/share/applications/doh-client.desktop"
	iconFile        = "/usr/share/icons/hicolor/256x256/apps/doh-client.png"
	binLink         = "/usr/local/bin/doh-client"
)

// Ubuntu 23.10+ (incl. 24.04 / 26.04) restricts unprivileged user namespaces via
// AppArmor; WebKitGTK's bubblewrap sandbox needs one. This profile grants it to
// the installed binary only (same approach Ubuntu uses for browsers).
func apparmorText(exe string) string {
	return fmt.Sprintf(`# managed by doh-client
abi <abi/4.0>,
include <tunables/global>

profile doh-client %s flags=(unconfined) {
  userns,

  include if exists <local/doh-client>
}
`, exe)
}

func desktopText(exe string) string {
	return fmt.Sprintf(`[Desktop Entry]
Type=Application
Name=DoH Client
Comment=DNS-over-HTTPS client for bypassing geo-blocking
Exec=%s
Icon=doh-client
Terminal=false
Categories=Network;Utility;
StartupWMClass=doh-client
`, exe)
}

// installExtras is best-effort: failures are logged, not fatal.
func installExtras() {
	exe := paths.InstallPath()
	if _, err := os.Stat("/sys/kernel/security/apparmor"); err == nil {
		if _, err := exec.LookPath("apparmor_parser"); err == nil {
			if err := os.WriteFile(apparmorProfile, []byte(apparmorText(exe)), 0o644); err != nil {
				log.Printf("apparmor profile: %v", err)
			} else if out, err := exec.Command("apparmor_parser", "-r", apparmorProfile).CombinedOutput(); err != nil {
				// abi/4.0 unsupported (AppArmor 3, e.g. Ubuntu 22.04): not needed there.
				log.Printf("apparmor_parser: %v: %s", err, out)
				_ = os.Remove(apparmorProfile)
			}
		}
	}
	if err := os.MkdirAll(filepath.Dir(iconFile), 0o755); err == nil {
		_ = os.WriteFile(iconFile, iconPNG, 0o644)
	}
	if err := os.WriteFile(desktopFile, []byte(desktopText(exe)), 0o644); err != nil {
		log.Printf("desktop entry: %v", err)
	}
	_ = os.Remove(binLink)
	if err := os.Symlink(exe, binLink); err != nil {
		log.Printf("symlink %s: %v", binLink, err)
	}
	_ = exec.Command("update-desktop-database", "-q", filepath.Dir(desktopFile)).Run()
	_ = exec.Command("gtk-update-icon-cache", "-q", "-t", "/usr/share/icons/hicolor").Run()
}

func removeExtras() {
	if b, err := os.ReadFile(apparmorProfile); err == nil && len(b) > 0 {
		_ = exec.Command("apparmor_parser", "-R", apparmorProfile).Run()
		_ = os.Remove(apparmorProfile)
	}
	if t, err := os.Readlink(binLink); err == nil && t == paths.InstallPath() {
		_ = os.Remove(binLink)
	}
	_ = os.Remove(desktopFile)
	_ = os.Remove(iconFile)
	_ = exec.Command("update-desktop-database", "-q", filepath.Dir(desktopFile)).Run()
}
