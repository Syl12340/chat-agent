#Requires -Version 5.1
<#
.SYNOPSIS
  Open a local port that reaches the deployed chat-agent from this machine.

.DESCRIPTION
  The server listens on 127.0.0.1:9991 only, so a browser on this machine cannot
  reach it directly. This forwards a local port over SSH to that loopback address
  and leaves the tunnel running in the foreground: close the window or press
  Ctrl+C to tear it down.

  The port is chosen automatically when the requested one is busy, and the script
  waits until /healthz actually answers before printing the address, so an address
  it prints is an address that works.

.PARAMETER Port
  Local port to listen on. Default 9992. If it is taken, the next free port is used.

.PARAMETER Target
  SSH destination for the server. Default root@47.117.40.74.

.PARAMETER WithImageBridge
  Also forward the reverse image-bridge port (18190) so ComfyUI on this machine is
  reachable from the server. Requires bin/imagebridge.exe to be running locally.

.PARAMETER ShowToken
  Read and print the access token from the server, so it can be pasted into the
  login field. Omit this if the token should not appear in terminal scrollback.

.EXAMPLE
  .\scripts\serve-local.ps1

.EXAMPLE
  .\scripts\serve-local.ps1 -Port 8080 -ShowToken
#>
param(
  [int]$Port = 9992,
  [string]$Target = 'root@47.117.40.74',
  [switch]$WithImageBridge,
  [switch]$ShowToken
)

$ErrorActionPreference = 'Stop'

function Test-PortFree {
  param([int]$Candidate)
  $listener = $null
  try {
    $listener = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Loopback, $Candidate)
    $listener.Start()
    return $true
  } catch {
    return $false
  } finally {
    if ($listener) { $listener.Stop() }
  }
}

function Resolve-Port {
  param([int]$Preferred)
  for ($candidate = $Preferred; $candidate -lt $Preferred + 40; $candidate++) {
    if (Test-PortFree -Candidate $candidate) { return $candidate }
  }
  throw "no free local port in $Preferred..$($Preferred + 39)"
}

Write-Host '==> checking the server is up' -ForegroundColor Cyan
$remoteHealth = ssh -o BatchMode=yes -o ConnectTimeout=10 $Target 'curl -s -o /dev/null -w "%{http_code}" --max-time 5 http://127.0.0.1:9991/healthz'
if ($remoteHealth -ne '200') {
  throw "the server did not answer /healthz on 127.0.0.1:9991 (got '$remoteHealth'). Is chat-agent running?"
}

$port = Resolve-Port -Preferred $Port
if ($port -ne $Port) {
  Write-Host "    port $Port is busy; using $port instead" -ForegroundColor Yellow
}

if ($WithImageBridge) {
  $bridge = Get-Process -Name imagebridge -ErrorAction SilentlyContinue
  if (-not $bridge) {
    $exe = Join-Path $PSScriptRoot '..\bin\imagebridge.exe'
    if (-not (Test-Path $exe)) { throw "imagebridge.exe not found at $exe" }
    Start-Process -FilePath $exe -WorkingDirectory (Split-Path $exe -Parent) -WindowStyle Hidden
    Start-Sleep -Seconds 2
    Write-Host '    started imagebridge' -ForegroundColor Yellow
  }
}

if ($ShowToken) {
  $token = (ssh -o BatchMode=yes $Target 'grep -m1 "^CHAT_ACCESS_TOKEN=" /etc/chat-agent/chat-agent.env' 2>$null) -replace '^CHAT_ACCESS_TOKEN=',''
  if ($token) { Write-Host "    token: $token" -ForegroundColor Yellow }
  else { Write-Host '    token: could not read it from the server' -ForegroundColor Yellow }
}

Write-Host "==> forwarding 127.0.0.1:$port -> server 127.0.0.1:9991" -ForegroundColor Cyan
$arguments = @(
  '-N',
  '-o', 'BatchMode=yes',
  '-o', 'ServerAliveInterval=30',
  '-o', 'ServerAliveCountMax=3',
  '-o', 'ExitOnForwardFailure=yes',
  '-L', "127.0.0.1:$port`:127.0.0.1:9991"
)
if ($WithImageBridge) {
  $arguments += @('-R', '127.0.0.1:18190:127.0.0.1:18190')
}
$arguments += $Target

$tunnel = Start-Process ssh -ArgumentList $arguments -PassThru -WindowStyle Hidden

try {
  $ready = $false
  for ($attempt = 0; $attempt -lt 20; $attempt++) {
    Start-Sleep -Milliseconds 400
    if ($tunnel.HasExited) { throw "the ssh tunnel exited immediately (code $($tunnel.ExitCode))" }
    try {
      $probe = Invoke-WebRequest -Uri "http://127.0.0.1:$port/healthz" -UseBasicParsing -TimeoutSec 3
      if ($probe.StatusCode -eq 200) { $ready = $true; break }
    } catch { }
  }
  if (-not $ready) { throw 'the tunnel is open but /healthz never answered through it' }

  Write-Host ''
  Write-Host "  chat-agent  http://127.0.0.1:$port" -ForegroundColor Green
  if ($WithImageBridge) { Write-Host '  imagebridge reverse port 18190 forwarded' -ForegroundColor Green }
  Write-Host ''
  Write-Host '  Open that address, then paste the access token at the login prompt.' -ForegroundColor Gray
  Write-Host '  Press Ctrl+C or close this window to close the tunnel.' -ForegroundColor Gray
  Write-Host ''

  # Hold the window open for as long as the tunnel lives.
  while (-not $tunnel.HasExited) { Start-Sleep -Seconds 2 }
  Write-Host 'tunnel closed.' -ForegroundColor Yellow
} finally {
  if ($tunnel -and -not $tunnel.HasExited) {
    Stop-Process -Id $tunnel.Id -Force -ErrorAction SilentlyContinue
  }
}
