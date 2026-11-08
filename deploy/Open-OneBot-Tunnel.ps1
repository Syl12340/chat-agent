[CmdletBinding()]
param(
    [string]$Server = "root@47.117.40.74",
    [ValidateRange(1, 65535)][int]$LocalIngressPort = 18080,
    [ValidateRange(1, 65535)][int]$RemoteChatPort = 9991,
    [ValidateRange(1, 65535)][int]$RemoteActionPort = 13000,
    [ValidateRange(1, 65535)][int]$LocalSnowLumaPort = 3000,
    [string]$IdentityFile = ""
)

$arguments = @(
    "-N",
    "-o", "ExitOnForwardFailure=yes",
    "-o", "ServerAliveInterval=30",
    "-o", "ServerAliveCountMax=3",
    "-L", "127.0.0.1:${LocalIngressPort}:127.0.0.1:${RemoteChatPort}",
    "-R", "127.0.0.1:${RemoteActionPort}:127.0.0.1:${LocalSnowLumaPort}"
)

if ($IdentityFile) {
    $arguments += @("-i", $IdentityFile)
}
$arguments += $Server

Write-Host "OneBot webhook: http://127.0.0.1:${LocalIngressPort}/hooks/onebot/v1/events"
Write-Host "Remote Action tunnel: http://127.0.0.1:${RemoteActionPort}/ -> local SnowLuma ${LocalSnowLumaPort}"
Write-Host "Keep this terminal open. No credential is passed to SSH."

& ssh @arguments
exit $LASTEXITCODE
