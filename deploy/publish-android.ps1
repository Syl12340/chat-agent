param(
    [string]$Server = 'root@47.117.40.74',
    [string]$Apk = (Join-Path $PSScriptRoot '../bin/chat-agent-android-debug.apk'),
    [string]$Release = ('android-' + (Get-Date -Format 'yyyyMMdd-HHmmss')),
    [string]$Sdk = $env:ANDROID_HOME
)
$ErrorActionPreference = 'Stop'
if ($Server -notmatch '^[A-Za-z0-9_.@-]+$' -or $Server.StartsWith('-')) { throw 'Invalid SSH target' }
if ($Release -notmatch '^[A-Za-z0-9_-]+$') { throw 'Invalid release ID' }
if (!$Sdk) { $Sdk = Join-Path $env:LOCALAPPDATA 'Android/Sdk' }
$buildTools = Get-ChildItem (Join-Path $Sdk 'build-tools') -Directory | Where-Object { $_.Name -match '^\d+\.\d+\.\d+$' } | Sort-Object { [version]$_.Name } -Descending | Select-Object -First 1
if (!$buildTools) { throw 'Android build tools not found' }
$resolvedApk = (Resolve-Path -LiteralPath $Apk).Path
$badging = & (Join-Path $buildTools.FullName 'aapt2.exe') dump badging $resolvedApk
if ($LASTEXITCODE -ne 0) { throw 'Cannot inspect APK' }
$packageLine = $badging | Where-Object { $_.StartsWith('package:') } | Select-Object -First 1
if ($packageLine -notmatch "name='([^']+)' versionCode='([0-9]+)' versionName='([^']+)'") { throw 'Cannot parse APK metadata' }
$packageName = $Matches[1]; $versionCode = [long]$Matches[2]; $versionName = $Matches[3]
$signature = & (Join-Path $buildTools.FullName 'apksigner.bat') verify --print-certs $resolvedApk
if ($LASTEXITCODE -ne 0) { throw 'APK signature validation failed' }
$stage = Join-Path $PSScriptRoot "../.cache/$Release"
New-Item -ItemType Directory -Force -Path $stage | Out-Null
Copy-Item -LiteralPath $resolvedApk -Destination (Join-Path $stage 'app.apk')
Copy-Item -LiteralPath (Join-Path $PSScriptRoot 'publish-android.sh') -Destination $stage
Copy-Item -LiteralPath (Join-Path $PSScriptRoot 'verify-android.py') -Destination $stage
$metadata = [ordered]@{ version_code=$versionCode; version_name=$versionName; package_name=$packageName; size=(Get-Item -LiteralPath $resolvedApk).Length; sha256=(Get-FileHash -LiteralPath $resolvedApk -Algorithm SHA256).Hash.ToLowerInvariant() }
[IO.File]::WriteAllText((Join-Path $stage 'manifest.json'), ($metadata | ConvertTo-Json), [Text.UTF8Encoding]::new($false))
# Linux scripts must retain LF even if the checkout uses CRLF.
foreach ($name in @('publish-android.sh', 'verify-android.py')) {
    $path = Join-Path $stage $name
    [IO.File]::WriteAllText($path, [IO.File]::ReadAllText($path).Replace("`r`n", "`n"), [Text.UTF8Encoding]::new($false))
}
$remote = "/tmp/pocket-$Release"
& ssh -o BatchMode=yes $Server "mkdir -p '$remote'"
if ($LASTEXITCODE -ne 0) { throw 'SSH staging failed' }
& scp -q (Join-Path $stage 'app.apk') (Join-Path $stage 'manifest.json') (Join-Path $stage 'publish-android.sh') (Join-Path $stage 'verify-android.py') "${Server}:${remote}/"
if ($LASTEXITCODE -ne 0) { throw 'APK upload failed' }
& ssh -o BatchMode=yes $Server "sh '$remote/publish-android.sh' '$Release' && python3 '$remote/verify-android.py'"
if ($LASTEXITCODE -ne 0) { throw 'Publication or verification failed; inspect the server before retrying' }
$signature | Where-Object { $_ -match 'certificate SHA-256 digest' } | Write-Output
