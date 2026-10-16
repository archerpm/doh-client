#!/bin/sh
# Runs after install and upgrade (deb: configure, rpm: 1 = install, 2 = upgrade).
# A fresh install does not start anything: the GUI offers to install the service
# on first launch and asks for administrator rights itself. Only an already
# installed service is refreshed here, so it does not keep running the old binary.
set -e

SERVICE_BIN=/usr/local/lib/doh-client/doh-client
if [ -x "$SERVICE_BIN" ] && [ -d /run/systemd/system ]; then
    /usr/bin/doh-client service install || echo "doh-client: could not update the service; run 'sudo doh-client service install'" >&2
fi

command -v update-desktop-database >/dev/null 2>&1 && update-desktop-database -q /usr/share/applications || true
command -v gtk-update-icon-cache >/dev/null 2>&1 && gtk-update-icon-cache -q -t /usr/share/icons/hicolor || true
exit 0
