param([ValidateRange(1024, 65535)][int]$LocalPort = 19991)

# Keep this terminal open. Ctrl+C closes the tunnel without stopping the service.
ssh -N -T -o BatchMode=yes -o StrictHostKeyChecking=yes -o ExitOnForwardFailure=yes -o ServerAliveInterval=30 -o ServerAliveCountMax=3 -L "127.0.0.1:${LocalPort}:127.0.0.1:9991" root@47.117.40.74
exit $LASTEXITCODE
