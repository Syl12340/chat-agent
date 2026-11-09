# 把本机的图像生成能力接到服务器
#
# 用法（本机 PowerShell）：
#   .\scripts\tunnel.ps1                 启动桥 + 隧道，前台守护
#   .\scripts\tunnel.ps1 -Build          先重新编译 imagebridge
#   .\scripts\tunnel.ps1 -Check          只做一次连通性检查
#
# 架构：
#   [服务器] chat-agent ──▶ 127.0.0.1:18190 ──(SSH 反向隧道)──▶ [本机] imagebridge ──▶ imgctl ──▶ ComfyUI
#
# 服务器不需要 Python、不需要模型权重，也不需要开任何入站端口。

[CmdletBinding()]
param(
  [string]$Host_ = 'root@47.117.40.74',
  [int]$Port = 18190,
  [string]$Token = $env:CHAT_IMAGE_TOKEN,
  [string]$Python = 'E:\IAST\image-generation\.venv\Scripts\python.exe',
  [string]$Script = 'E:\IAST\image-generation\agent-bridge\imgctl.py',
  [string]$Profiles = '',
  [string]$Default = 'z_anime_d8',
  [switch]$Build,
  [switch]$Check
)

$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
$bin = Join-Path $root 'bin\imagebridge.exe'

function Write-Step($text) { Write-Host "==> $text" -ForegroundColor Cyan }
function Write-Ok($text)   { Write-Host "    $text" -ForegroundColor Green }
function Write-Warn2($text){ Write-Host "    $text" -ForegroundColor Yellow }

if (-not (Test-Path $Python)) { throw "找不到 Python 解释器：$Python" }
if (-not (Test-Path $Script)) { throw "找不到 imgctl.py：$Script" }

if ($Build -or -not (Test-Path $bin)) {
  Write-Step '编译 imagebridge'
  Push-Location $root
  try {
    $env:TMP = Join-Path $root '.cache\gotmp'; $env:TEMP = $env:TMP
    New-Item -ItemType Directory -Force -Path $env:TMP | Out-Null
    go build -buildvcs=false -o $bin ./cmd/imagebridge
    if ($LASTEXITCODE -ne 0) { throw 'imagebridge 编译失败' }
    Write-Ok $bin
  } finally { Pop-Location }
}

# 桥必须是后台作业：它和隧道要同时活着，前台只能守一个。
function Start-Bridge {
  Write-Step "启动 imagebridge（127.0.0.1:$Port）"
  $args_ = @('-listen', "127.0.0.1:$Port", '-python', $Python, '-script', $Script, '-default', $Default)
  if ($Token)    { $args_ += @('-token', $Token) }
  if ($Profiles) { $args_ += @('-profiles', $Profiles) }
  $proc = Start-Process -FilePath $bin -ArgumentList $args_ -PassThru -WindowStyle Hidden `
            -RedirectStandardOutput (Join-Path $root '.cache\imagebridge.log') `
            -RedirectStandardError  (Join-Path $root '.cache\imagebridge.err')
  Start-Sleep -Seconds 2
  if ($proc.HasExited) { throw "imagebridge 启动失败，见 .cache\imagebridge.err" }
  Write-Ok "pid=$($proc.Id)"
  return $proc
}

function Test-BridgeLocal {
  try {
    $r = Invoke-RestMethod -Uri "http://127.0.0.1:$Port/health" -TimeoutSec 20 -Headers @{ Authorization = "Bearer $Token" }
  } catch {
    return @{ ok = $false; reason = $_.Exception.Message }
  }
  return $r
}

# SSH 反向隧道：把服务器的 127.0.0.1:PORT 转到本机同一个端口。
# ExitOnForwardFailure 保证端口被占用时立刻失败，而不是静默地不转发。
function Start-Tunnel {
  Write-Step "建立反向隧道 $Host_ 的 127.0.0.1:$Port → 本机 $Port"
  $sshArgs = @(
    '-N', '-T',
    '-o', 'BatchMode=yes',
    '-o', 'ExitOnForwardFailure=yes',
    '-o', 'ServerAliveInterval=30',
    '-o', 'ServerAliveCountMax=3',
    '-R', "127.0.0.1:${Port}:127.0.0.1:${Port}",
    $Host_
  )
  return Start-Process -FilePath 'ssh' -ArgumentList $sshArgs -PassThru -WindowStyle Hidden `
           -RedirectStandardError (Join-Path $root '.cache\tunnel.err')
}

if ($Check) {
  Write-Step '检查本机桥'
  $health = Test-BridgeLocal
  if ($health.ok) { Write-Ok "ready，可用配置：$($health.profiles -join ', ')" }
  else            { Write-Warn2 "未就绪：$($health.reason)" ; exit 1 }
  Write-Step '检查服务器侧'
  $remote = ssh -o BatchMode=yes $Host_ "curl -s --max-time 8 -H 'Authorization: Bearer $Token' http://127.0.0.1:$Port/health || echo TUNNEL_DOWN"
  if ($remote -match 'TUNNEL_DOWN') { Write-Warn2 '服务器到本机的隧道不通'; exit 1 }
  Write-Ok $remote
  exit 0
}

$bridge = Start-Bridge
$tunnel = Start-Tunnel
Start-Sleep -Seconds 3

Write-Step '验证端到端'
$health = Test-BridgeLocal
if ($health.ok) { Write-Ok "本机就绪：$($health.profiles -join ', ')" }
else            { Write-Warn2 "本机未就绪：$($health.reason)（隧道仍会保持，ComfyUI 启动后自动恢复）" }

$remote = ssh -o BatchMode=yes $Host_ "curl -s --max-time 8 -H 'Authorization: Bearer $Token' http://127.0.0.1:$Port/health || echo TUNNEL_DOWN"
if ($remote -match 'TUNNEL_DOWN') { Write-Warn2 '服务器还看不到桥，稍后可用 -Check 复查' }
else { Write-Ok '服务器已能看到图像生成服务' }

Write-Host ''
Write-Host '隧道运行中。Ctrl+C 结束（会一并结束桥）。' -ForegroundColor DarkGray
Write-Host "复查： .\scripts\tunnel.ps1 -Check" -ForegroundColor DarkGray
Write-Host ''

try {
  # 任一进程退出就重连，但避免疯狂重启。
  while ($true) {
    Start-Sleep -Seconds 5
    if ($bridge.HasExited) {
      Write-Warn2 'imagebridge 退出了，重启'
      Start-Sleep -Seconds 3
      $bridge = Start-Bridge
    }
    if ($tunnel.HasExited) {
      Write-Warn2 '隧道断开，重连'
      Start-Sleep -Seconds 5
      $tunnel = Start-Tunnel
    }
  }
} finally {
  Write-Step '清理'
  foreach ($p in @($tunnel, $bridge)) {
    if ($p -and -not $p.HasExited) { try { $p.Kill() } catch {} }
  }
  Write-Ok '已停止'
}
