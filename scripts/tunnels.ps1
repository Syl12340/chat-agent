param(
  [string]$Target = 'root@47.117.40.74'
)

# Keep both directions open: the web port so a local browser can reach the live
# service, and the reverse image-bridge port so the server can reach this machine.
$ErrorActionPreference = 'Stop'

Write-Host '==> imagebridge' -ForegroundColor Cyan
$bridge = Get-Process -Name imagebridge -ErrorAction SilentlyContinue
if (-not $bridge) {
  Start-Process -FilePath (Join-Path $PSScriptRoot '..\bin\imagebridge.exe') `
    -WorkingDirectory (Join-Path $PSScriptRoot '..') -WindowStyle Hidden
  Start-Sleep -Seconds 3
}

Write-Host '==> web tunnel 9992 -> 9991' -ForegroundColor Cyan
Start-Process ssh -ArgumentList '-N','-o','BatchMode=yes','-o','ServerAliveInterval=30','-L','127.0.0.1:9992:127.0.0.1:9991',$Target -WindowStyle Hidden
Start-Sleep -Seconds 2

Write-Host '==> reverse tunnel 18190' -ForegroundColor Cyan
Start-Process ssh -ArgumentList '-N','-o','BatchMode=yes','-o','ServerAliveInterval=30','-o','ExitOnForwardFailure=yes','-R','127.0.0.1:18190:127.0.0.1:18190',$Target -WindowStyle Hidden
Start-Sleep -Seconds 3

$web = Test-NetConnection -ComputerName 127.0.0.1 -Port 9992 -InformationLevel Quiet -WarningAction SilentlyContinue
$rev = ssh -o BatchMode=yes $Target 'curl -s -o /dev/null -w "%{http_code}" --max-time 5 http://127.0.0.1:18190/health'
Write-Host "web tunnel: $web"
Write-Host "reverse tunnel: $rev (503 means reachable, bridge reports ComfyUI down)"
