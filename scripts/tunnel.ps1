# 把本机的图像生成能力接到服务器
#
# 用法（本机 PowerShell）：
#   .\scripts\tunnel.ps1                  桥 + 反向隧道，前台守护
#   .\scripts\tunnel.ps1 -Serve           再带上网页面板 9992 的转发
#   .\scripts\tunnel.ps1 -Build           先重新编译 imagebridge
#   .\scripts\tunnel.ps1 -Check           只做一次连通性检查
#
# 架构：
#   [服务器] chat-agent ──► 127.0.0.1:18190 ──(SSH 反向隧道)──► [本机] imagebridge ──► imgctl ──► ComfyUI
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
  try { & go build -o $bin ./cmd/imagebridge } finally { Pop-Location }
  if ($LASTEXITCODE -ne 0) { throw 'imagebridge 编译失败' }
}

function Test-PortLocal([int]$localPort) {
  return [bool](Get-NetTCPConnection -LocalPort $localPort -State Listen -ErrorAction SilentlyContinue)
}

# 桥必须是后台作业：它和隧道要同时活着，前台只能守一个。
function Start-Bridge {
  # Starting a second bridge on a port that is already serving only produces a
  # bind error and a dead end; reuse the running one instead.
  if (Test-PortLocal $Port) {
    Write-Ok 'imagebridge 已在运行，复用'
    return $null
  }
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

# 服务器侧转发是否可用。判定必须区分「隧道通、生成未就绪」和「隧道不通」：
#   200  桥与 ComfyUI 都就绪
#   401  转发是通的，只是令牌不对
#   503  转发是通的，桥在，ComfyUI 没开 —— 原因看响应里的 reason
#   000 / 空  转发不通
# 探针要带鉴权：配置了 CHAT_IMAGE_TOKEN 的桥对匿名请求回 401，若把它当成「不通」
# 就会去重建一条本来健康的隧道。超时也给足 20 秒，因为 ComfyUI 关闭时桥自身的
# 健康探测会等它的超时才回话。
function Get-RemoteState {
  $auth = if ($Token) { "-H 'Authorization: Bearer $Token'" } else { '' }
  $cmd = "curl -s --max-time 20 $auth -w '__CODE__%{http_code}' http://127.0.0.1:$Port/health"
  $raw = ssh -o BatchMode=yes $Host_ $cmd 2>$null
  $text = (@($raw) -join "`n")
  $code = ''
  $body = $text
  if ($text -match '__CODE__(\d{3})') {
    $code = $Matches[1]
    $body = $text.Substring(0, $text.IndexOf('__CODE__'))
  }
  $reason = ''
  if ($body -match '"reason"\s*:\s*"([^"]*)"') { $reason = $Matches[1] }
  return [pscustomobject]@{
    code      = $code
    reason    = $reason
    # 任何 HTTP 状态码都证明服务器能走到桥。
    reachable = [bool]($code -match '^(200|401|503)$')
    ready     = ($code -eq '200')
  }
}

function Write-RemoteState($state) {
  switch ($state.code) {
    '200' { Write-Ok '隧道通，图像生成就绪' }
    '503' { Write-Ok "隧道通，生成未就绪：$($state.reason)" }
    '401' { Write-Warn2 '隧道通，但令牌被拒（检查 CHAT_IMAGE_TOKEN）' }
    default { Write-Warn2 '服务器到本机的隧道不通' }
  }
}

# 网页访问用的本地转发：本机 9992 → 服务器的 9991。
# 同样用 Start-Process 起独立 ssh：作为作业的前台进程跑会被沙箱回收掉，
# 而独立进程能活过会话，这也是本脚本比 serve-local.py 更耐用的原因。
function Start-Serve {
  if (Test-PortLocal $ServePort) {
    Write-Ok '网页转发已在运行，复用'
    return $null
  }
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
  # 先看服务器侧转发是否已经可用：可用就复用，绝不去动既有的转发。
  $state = Get-RemoteState
  if ($state.reachable) {
    Write-Ok "服务器侧转发已就绪（$($state.code)），复用既有隧道"
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

  if ($started.HasExited) {
    # 不按端口杀进程：监听 18190 的 sshd 很可能正是另一条健康隧道。误杀的代价
    # 是两条隧道互相踢、无限重连，比残留一条死转发严重得多。这里只报清楚。
    $why = Get-Content $errFile -ErrorAction SilentlyContinue | Select-Object -Last 1
    Write-Warn2 "隧道建立失败：$why"
    if ($why -match 'remote port forwarding failed') {
      Write-Warn2 "服务器上 $Port 被占用，多为上一个隧道的残留。手工确认后清理："
      Write-Warn2 "  ssh $Host_ `"ss -lntp | grep $Port`""
      Write-Warn2 "  ssh $Host_ `"kill <pid>`""
    }
  } else {
    Write-Ok "隧道已建立 pid=$($started.Id)"
  }
  return $started
}

if ($Check) {
  Write-Step '检查本机桥'
  $health = Test-BridgeLocal
  if ($health.ok) { Write-Ok "ready，可用配置：$($health.profiles -join ', ')" }
  else            { Write-Warn2 "未就绪：$($health.reason)" }
  Write-Step '检查服务器侧'
  Write-RemoteState (Get-RemoteState)
  if ($Serve) {
    Write-Step '检查网页转发'
    if (Test-PortLocal $ServePort) { Write-Ok "127.0.0.1:$ServePort 在听" }
    else                           { Write-Warn2 "127.0.0.1:$ServePort 没有在听" }
  }
  exit 0
}

$bridge = Start-Bridge
$tunnel = Start-Tunnel
if ($Serve) { $webProc = Start-Serve }
Start-Sleep -Seconds 3

Write-Step '验证端到端'
$health = Test-BridgeLocal
if ($health.ok) { Write-Ok "本机就绪：$($health.profiles -join ', ')" }
else            { Write-Warn2 "本机未就绪：$($health.reason)（隧道仍会保持，ComfyUI 启动后自动恢复）" }
Write-RemoteState (Get-RemoteState)

Write-Host ''
Write-Host '守护中。Ctrl+C 结束（会一并结束本条命令启动的进程）。' -ForegroundColor DarkGray
Write-Host "复查： .\scripts\tunnel.ps1 -Check" -ForegroundColor DarkGray
Write-Host ''

try {
  # 分段守护：复用来的组件没有进程句柄可等，所以每一轮都按「这一段现在到底通不通」
  # 判断，只重建确认断掉的那一段。只看自己持有的句柄会让脚本活着而链接已经死了。
  while ($true) {
    Start-Sleep -Seconds 15

    if ($Serve -and -not (Test-PortLocal $ServePort)) {
      Write-Warn2 '网页转发已断开，重建'
      $webProc = Start-Serve
    }

    if (-not (Test-PortLocal $Port)) {
      Write-Warn2 'imagebridge 已断开，重建'
      Start-Sleep -Seconds 2
      try { $bridge = Start-Bridge } catch { Write-Warn2 $_.Exception.Message }
    }

    $state = Get-RemoteState
    if (-not $state.reachable) {
      Write-Warn2 '反向转发已断开，重建'
      $tunnel = Start-Tunnel
    } elseif ($script:lastCode -ne $state.code) {
      # 状态变化时说一声：「隧道通、生成未就绪」和「就绪」是不同的结论。
      Write-RemoteState $state
      $script:lastCode = $state.code
    }
  }
} finally {
  Write-Step '清理'
  foreach ($p in @($tunnel, $webProc, $bridge)) {
    if ($p -and -not $p.HasExited) { try { $p.Kill() } catch {} }
  }
  Write-Ok '已停止'
}
