# Build DoH Client for Windows (amd64 + arm64). Requires Go 1.25+.
# Resources (icon, manifest) are pre-generated in rsrc_windows_*.syso;
# regenerate with: wails3 generate syso -arch amd64 -icon build/icon.ico -manifest build/windows/app.manifest -info build/windows/info.json -out rsrc_windows_amd64.syso
$ErrorActionPreference = "Stop"
Set-Location (Split-Path $PSScriptRoot -Parent)
New-Item -ItemType Directory -Force dist | Out-Null
$env:CGO_ENABLED = "0"
foreach ($arch in "amd64", "arm64") {
    $env:GOOS = "windows"; $env:GOARCH = $arch
    go build -trimpath -tags production -ldflags "-H windowsgui -s -w" -o "dist/doh-client-windows-$arch.exe" .
    if ($LASTEXITCODE) { throw "build $arch failed" }
}
Remove-Item Env:GOOS, Env:GOARCH
Get-ChildItem dist
