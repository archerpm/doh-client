//go:build linux

package main

import (
	"context"
	"log"
	"os"
	"os/exec"
	"time"

	"github.com/godbus/dbus/v5"
)

// prepareWebKitSandbox disables WebKitGTK's bubblewrap sandbox when bwrap cannot
// create a user namespace (Ubuntu 23.10+ AppArmor restriction when not running
// from the installed, profiled path; sysctl-disabled userns elsewhere). Without
// this WebKit aborts with "bwrap: setting up uid map: Permission denied".
// The webview only renders the app's embedded local assets, never remote content.
func prepareWebKitSandbox() {
	if os.Getenv("WEBKIT_DISABLE_SANDBOX_THIS_IS_DANGEROUS") != "" {
		return
	}
	bwrap, err := exec.LookPath("bwrap")
	if err != nil {
		return // WebKit falls back on its own when bwrap is absent
	}
	tr, err := exec.LookPath("true")
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := exec.CommandContext(ctx, bwrap, "--unshare-user", "--ro-bind", "/", "/", tr).Run(); err != nil {
		log.Printf("user namespaces unavailable (%v): disabling WebKit sandbox for local UI", err)
		_ = os.Setenv("WEBKIT_DISABLE_SANDBOX_THIS_IS_DANGEROUS", "1")
	}
}

// trayAvailable: the Linux tray is a StatusNotifierItem; it needs a session bus
// and a watcher (KDE, XFCE, Ubuntu's AppIndicator extension; plain GNOME has none).
func trayAvailable() (hasBus, hasWatcher bool) {
	if os.Getenv("DBUS_SESSION_BUS_ADDRESS") == "" {
		return false, false
	}
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return false, false
	}
	defer conn.Close()
	var has bool
	if err := conn.BusObject().Call("org.freedesktop.DBus.NameHasOwner", 0, "org.kde.StatusNotifierWatcher").Store(&has); err != nil {
		return true, false
	}
	return true, has
}
