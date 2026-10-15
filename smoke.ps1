Set-Location D:\AI\DoH\dist
$exe = '.\doh-client-windows-amd64.exe'
foreach ($a in @('help'), @('status','--json'), @('diagnose','claude.ai','--json'), @('reapply-dns'), @('config','export','a','b','c'), @('flush','--json')) {
  $out = & cmd /c "$exe $($a -join ' ') 2>&1"
  "--- $($a -join ' ') => exit $LASTEXITCODE"
  ($out | Select-Object -First 6) -join "`n"
}
