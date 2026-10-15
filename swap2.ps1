$ErrorActionPreference='Continue'
Set-Location D:\AI\DoH
Get-Process | Where-Object { $_.Name -like 'doh-client*' } | Stop-Process -Force
Start-Sleep -Seconds 2
tar -xzf doh-client-sync15.tgz
Copy-Item dist\doh-client-new-amd64.exe dist\doh-client-windows-amd64.exe -Force
Copy-Item dist\doh-client-new-amd64.exe dist\doh-client-1.1-windows-amd64.exe -Force
$h0=(Get-FileHash dist\doh-client-new-amd64.exe).Hash
$h1=(Get-FileHash dist\doh-client-windows-amd64.exe).Hash
$h2=(Get-FileHash dist\doh-client-1.1-windows-amd64.exe).Hash
"hashes: $h0 $h1 $h2"
if ($h0 -eq $h1 -and $h0 -eq $h2) {
  foreach ($f in 'doh-client-sync15.tgz','dist\doh-client-new-amd64.exe','swap.ps1','wait.ps1','codex-prompt7.txt','codex-task-fix7.txt','codex-run7.log','codex-run7b.log','codex-impl7.md') { Remove-Item $f -Force -ErrorAction SilentlyContinue }
  "OK"
} else { "MISMATCH" }
