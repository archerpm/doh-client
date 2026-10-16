#!/usr/bin/env bash
# Build DoH Client for Linux.
#   GTK4 / WebKitGTK 6.0 (Ubuntu 24.04+, Fedora 40+):  sudo apt install libgtk-4-dev libwebkitgtk-6.0-dev
#   GTK3 / WebKit2GTK 4.1 (Ubuntu 22.04, Debian 12):    sudo apt install libgtk-3-dev libwebkit2gtk-4.1-dev
# Usage: scripts/build-linux.sh [gtk3]
set -euo pipefail
cd "$(dirname "$0")/.."
mkdir -p dist
tags="-tags production"
suffix=""
if [[ "${1:-}" == "gtk3" ]]; then tags="-tags production,gtk3"; suffix="-gtk3"; fi
CGO_ENABLED=1 go build -trimpath $tags -ldflags "-s -w" -o "dist/doh-client-linux-$(go env GOARCH)$suffix" .
ls -la dist
