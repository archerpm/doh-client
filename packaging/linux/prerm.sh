#!/bin/sh
# Runs before the package is removed. On an upgrade (deb: upgrade, rpm: 1) the
# service is kept and refreshed by postinst. On removal the service is stopped
# and unregistered, which also puts the original system DNS back.
set -e

case "$1" in
    upgrade|deconfigure|failed-upgrade|1) exit 0 ;;
esac

SERVICE_BIN=/usr/local/lib/doh-client/doh-client
if [ -x "$SERVICE_BIN" ]; then
    if ! /usr/bin/doh-client service uninstall; then
        echo "doh-client: could not remove the service or restore DNS; run 'sudo doh-client restore-dns'" >&2
        exit 1
    fi
else
    # no service installed, but a leftover backup may still point DNS at us
    /usr/bin/doh-client restore-dns >/dev/null 2>&1 || true
fi
exit 0
