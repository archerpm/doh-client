#!/usr/bin/env bash
# Build a universal DoH Client.app on macOS (needs Xcode command line tools + Go 1.25+).
set -euo pipefail
cd "$(dirname "$0")/.."
mkdir -p dist
export CGO_ENABLED=1 MACOSX_DEPLOYMENT_TARGET=11.0
GOARCH=arm64 go build -trimpath -tags production -ldflags "-s -w" -o dist/doh-client-arm64 .
GOARCH=amd64 go build -trimpath -tags production -ldflags "-s -w" -o dist/doh-client-amd64 .
APP="dist/DoH Client.app"
rm -rf "$APP"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"
lipo -create -output "$APP/Contents/MacOS/doh-client" dist/doh-client-arm64 dist/doh-client-amd64
cp build/darwin/Info.plist "$APP/Contents/Info.plist"
# icon
ICONSET=$(mktemp -d)/icon.iconset
mkdir -p "$ICONSET"
for s in 16 32 128 256 512; do
  sips -z $s $s build/appicon.png --out "$ICONSET/icon_${s}x${s}.png" >/dev/null
  sips -z $((s*2)) $((s*2)) build/appicon.png --out "$ICONSET/icon_${s}x${s}@2x.png" >/dev/null
done
iconutil -c icns "$ICONSET" -o "$APP/Contents/Resources/icon.icns"
codesign --force --deep -s - "$APP"   # ad-hoc signature (Gatekeeper: right-click → Open on first launch)
rm dist/doh-client-arm64 dist/doh-client-amd64
echo "built: $APP"
