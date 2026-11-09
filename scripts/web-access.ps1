# 从本机访问服务器的 chat-agent 网页。
#
# 服务器只监听 127.0.0.1:9991，没有对公网开放端口。这里做一次 SSH 本地转发，
# 把服务器的 9991 映射到本机同一个端口，然后浏览器打开 http://127.0.0.1:9991
#
# 用法：
#   .\scripts\web-access.ps1            建立转发并打印访问地址与令牌
#   .\scripts\web-access.ps1 -Port 9992 换一个本地端口（默认占用时有用）
#   .\scripts\web-access.ps1 -Token     只打印访问令牌
#   .\scripts\web-access.ps1 -Stop      断开已有的转发

[CmdletBinding()]
param(
  [string]$Host_ = 'root@47.117.40.74',
  [int]$Port = 9991,
  [string]$Identity = '',
  [switch]$Token,
  [switch]$Stop
)

$ErrorActionPreference = 'Stop'

function Get-AccessToken {
  $remote = "sed -n 's/^CHAT_ACCESS_TOKEN=//p' /etc/chat-agent/chat-agent.env"
  $args_ = @('-o', 'BatchMode=yes')
  if ($Identity) { $args_ = @('-i', $Identity) + $args_ }
  $token = (& ssh @args_ $Host_ $remote).Trim()
  if (-not $token) { throw '未能读取访问令牌，检查 SSH 与 /etc/chat-agent/chat-agent.env' }
  return $token
}

if ($Token) {
  Write-Output (Get-AccessToken)
  exit 0
}

if ($Stop) {
  $existing = Get-CimInstance Win32_Process -Filter "Name='ssh.exe'" -ErrorAction SilentlyContinue |
    Where-Object { $_.CommandLine -like "*-L*$Port`:127.0.0.1:9991*" }
  if (-not $existing) { Write-Host "没有 PID 使用本地端口 $Port 的转发" -ForegroundColor Yellow; exit 0 }
  foreach ($process in $existing) {
    try { Stop-Process -Id $process.ProcessId -Force; Write-Host "已断开 pid=$($process.ProcessId)" -ForegroundColor Green } catch {}
  }
  exit 0
}

# 端口被占用时直接说明，避免 SSH 静默失败
$busy = Get-NetTCPConnection -LocalPort $Port -State Listen -ErrorAction SilentlyContinue
if ($busy) {
  throw "本地端口 $Port 已被占用。换一个端口：.\scripts\web-access.ps1 -Port 9992"
}

Write-Host "==> 建立本地转发 127.0.0.1:$Port → $Host_:9991" -ForegroundColor Cyan
$sshArgs = @(
  '-N', '-T',
  '-o', 'BatchMode=yes',
  '-o', 'ExitOnForwardFailure=yes',
  '-o', 'ServerAliveInterval=30',
  '-o', 'ServerAliveCountMax=3',
  '-L', "127.0.0.1:${Port}:127.0.0.1:9991",
  $Host_
)
if ($Identity) { $sshArgs = @('-i', $Identity) + $sshArgs }

$log = Join-Path (Split-Path -Parent $PSScriptRoot) '.cache\web-access.err'
New-Item -ItemType Directory -Force -Path (Split-Path -Parent $log) | Out-Null
$tunnel = Start-Process -FilePath 'ssh' -ArgumentList $sshArgs -PassThru -WindowStyle Hidden `
            -RedirectStandardError $log
Start-Sleep -Seconds 2
if ($tunnel.HasExited) {
  $detail = Get-Content $log -ErrorAction SilentlyContinue | Select-Object -Last 3
  throw "转发失败：$($detail -join ' ')"
}

$token = Get-AccessToken
$url = "http://127.0.0.1:$Port"

Write-Host ""
Write-Host "  打开： $url" -ForegroundColor Green
Write-Host "  令牌： $token" -ForegroundColor Green
Write-Host ""
Write-Host "  登录方式：页面上的访问令牌输入框粘贴上面这串；" -ForegroundColor DarkGray
Write-Host "  想用设备密钥登录，点登录框里的管理入口。" -ForegroundColor DarkGray
Write-Host ""
Write-Host "  令牌等同密码，别贴到公开地方。断开： .\scripts\web-access.ps1 -Stop" -ForegroundColor DarkGray
Write-Host ""

# 前台守着，Ctrl+C 断开
try {
  while (-not $tunnel.HasExited) { Start-Sleep -Seconds 3 }
  Write-Host '转发已断开' -ForegroundColor Yellow
} finally {
  if (-not $tunnel.HasExited) { try { $tunnel.Kill() } catch {} }
}
