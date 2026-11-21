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
  [switch]$Check,
  [switch]$Serve,
  [int]$ServePort = 9992
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
  # Starting a second bridge on a port that is already serving only produces a
  # bind error and a dead end; reuse the running one instead.
  $listening = Get-NetTCPConnection -LocalPort $Port -State Listen -ErrorAction SilentlyContinue
  if ($listening) { Write-Ok "imagebridge 已在运行（pid=$($listening[0].OwningProcess)）"; return $null }
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

# 网页访问用的本地转发：本机 9992 → 服务器的 9991。
# 同样用 Start-Process 起独立 ssh：作为作业的前台进程跑会被沙箱回收掉，
# 而独立进程能活过会话，这也是本脚本比 serve-local.py 更耐用的原因。
function Start-Serve {
  $listening = Get-NetTCPConnection -LocalPort $ServePort -State Listen -ErrorAction SilentlyContinue
  if ($listening) { Write-Ok "网页转发已在运行（pid=$($listening[0].OwningProcess)）"; return $null }
  Write-Step "建立网页转发 127.0.0.1:$ServePort → ${Host_} 的 9991"
  $args_ = @(
    '-N', '-T',
    '-o', 'BatchMode=yes',
    '-o', 'ExitOnForwardFailure=yes',
    '-o', 'ServerAliveInterval=30',
    '-o', 'ServerAliveCountMax=3',
    '-L', "127.0.0.1:${ServePort}:127.0.0.1:9991",
    $Host_
  )
  $errFile = Join-Path $root '.cache\serve-local.err'
  $proc = Start-Process -FilePath 'ssh' -ArgumentList $args_ -PassThru -WindowStyle Hidden -RedirectStandardError $errFile
  Start-Sleep -Seconds 4
  if ($proc.HasExited) {
    Write-Warn2 "网页转发失败：$((Get-Content $errFile -ErrorAction SilentlyContinue | Select-Object -Last 1))"
  } else {
    Write-Ok "pid=$($proc.Id)"
  }
  return $proc
}

# SSH 反向隧道：把服务器的 127.0.0.1:PORT 转到本机同一个端口。
# ExitOnForwardFailure 保证端口被占用时立刻失败，而不是静默地不转发。
function Start-Tunnel {
  # Probe the server side first: if its forward already reaches the bridge, reuse it.
  # Without this, a second run kills the forwarding sshd that the first run is
  # healthily using (the cleanup below cannot tell a stale socket from a live one)
  # and the two instances fight over the port, reconnecting in a loop.
  $probe = ssh -o BatchMode=yes $Host_ "curl -s -o /dev/null -w '%{http_code}' --max-time 5 http://127.0.0.1:$Port/health"
  if ($probe -match '^(200|503)$') {
    Write-Ok "服务器侧转发已就绪（$probe），复用既有隧道"
    return $null
  }
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
  $errFile = Join-Path $root '.cache\tunnel.err'
  $started = Start-Process -FilePath 'ssh' -ArgumentList $sshArgs -PassThru -WindowStyle Hidden `
             -RedirectStandardError $errFile
  Start-Sleep -Seconds 3

  # A tunnel that dies leaves its forwarding socket on the server: sshd keeps the
  # port bound, so the next attempt fails with "remote port forwarding failed" and
  # the port looks busy to every later attempt until that sshd is killed. Clear it
  # once and retry rather than making the operator find it by hand.
  if ($started.HasExited) {
    Write-Warn2 '首次转发失败，检查服务器侧是否有残留'
    # Keep the remote command free of pipes and nested quotes: this script already
    # carries Chinese text and PowerShell's parser is fragile around both. Parse the
    # listener here instead.
    $listeners = ssh -o BatchMode=yes $Host_ 'ss -lntp' 2>$null
    $stale = $null
    foreach ($line in $listeners) {
      if ($line -match "127\.0\.0\.1:$Port\s" -and $line -match 'pid=(\d+)') { $stale = $Matches[1] }
    }
    if ($stale) {
      Write-Warn2 "清理残留 sshd pid=$stale"
      ssh -o BatchMode=yes $Host_ "kill $stale" | Out-Null
      Start-Sleep -Seconds 2
      $started = Start-Process -FilePath 'ssh' -ArgumentList $sshArgs -PassThru -WindowStyle Hidden `
                 -RedirectStandardError $errFile
      Start-Sleep -Seconds 3
    }
  }

  if ($started.HasExited) {
    $why = Get-Content $errFile -ErrorAction SilentlyContinue | Select-Object -Last 1
    Write-Warn2 "隧道仍然失败：$why"
  } else {
    Write-Ok "隧道已建立 pid=$($started.Id)"
  }
  return $started
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
if ($Serve) { $serve = Start-Serve }
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
