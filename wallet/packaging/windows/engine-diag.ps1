# engine-diag.ps1: why does the installed engine not serve? The silent install into a prefix as the smoke does, then the
# engine run DIRECTLY (no host) twice with its stdout and stderr captured to files: once in the smoke's form (the test
# data directory named in the environment), once in the installed form (no data directory named: the engine's own
# default root), 30 s each; then the uninstall. Read only but for the prefix and the two data directories under -Out.
# Written 14 October 2026 for wallet 0.0.9's first Windows smoke (the engine started and never wrote its run URL).
param(
    [string]$Setup = '',
    [string]$Out = ''
)
$ErrorActionPreference = 'Continue'
if (-not $Setup -or -not (Test-Path $Setup)) { Write-Host "no Setup exe at '$Setup'"; exit 2 }
if (-not $Out) { $Out = Join-Path $env:TEMP 'igneum-wallet-diag' }
New-Item -ItemType Directory -Force -Path $Out | Out-Null
$log = Join-Path $Out 'diag.log'
'' | Set-Content $log
function Say([string]$t) { $line = ('[' + (Get-Date).ToUniversalTime().ToString('HH:mm:ssZ') + '] ' + $t); Write-Host $line; Add-Content $log $line }

$prefix = Join-Path $Out 'prefix'
$p = Start-Process -FilePath $Setup -ArgumentList @('/VERYSILENT', '/SUPPRESSMSGBOXES', '/NORESTART', '/SP-', "/DIR=`"$prefix`"", '/MERGETASKS=!desktopicon', "/LOG=`"$Out\install.log`"") -Wait -PassThru
$engine = Join-Path $prefix 'igneum-wallet-engine.exe'
Say ("install exit " + $p.ExitCode + "; engine present: " + (Test-Path $engine))
if (-not (Test-Path $engine)) { exit 1 }
$v = & $engine --version 2>&1
Say ("engine --version: " + $v)
Say ("APPDATA=" + $env:APPDATA + " HOME=" + $env:HOME + " USERPROFILE=" + $env:USERPROFILE)

function RunEngine([string]$label, [hashtable]$envs) {
    foreach ($k in $envs.Keys) { Set-Item -Path ("Env:" + $k) -Value $envs[$k] }
    $urlf = Join-Path $Out ($label + '-url.json')
    if (Test-Path $urlf) { Remove-Item $urlf -Force }
    $env:IGNEUM_WALLET_URL_FILE = $urlf
    $env:IGNEUM_SIGNER_LIGHT_KDF = '1'
    $env:IGNEUM_WALLET_UPDATE_FIRST_SECS = '600'
    $so = Join-Path $Out ($label + '-engine.out'); $se = Join-Path $Out ($label + '-engine.err')
    $proc = Start-Process -FilePath $engine -ArgumentList @('--ui', (Join-Path $prefix 'ui'), '--signer', (Join-Path $prefix 'igneum-signer.exe')) -WorkingDirectory $prefix -PassThru -WindowStyle Hidden -RedirectStandardOutput $so -RedirectStandardError $se
    $i = 0
    while ($i -lt 60 -and -not (Test-Path $urlf) -and -not $proc.HasExited) { Start-Sleep -Milliseconds 500; $i++ }
    Say ($label + ": after " + ($i / 2) + " s: url file written: " + (Test-Path $urlf) + "; exited: " + $proc.HasExited + $(if ($proc.HasExited) { " code " + $proc.ExitCode } else { "" }))
    if (-not $proc.HasExited) { Stop-Process -Id $proc.Id -Force -ErrorAction SilentlyContinue }
    Get-Process -Name 'igneum-signer' -ErrorAction SilentlyContinue | ForEach-Object { Stop-Process -Id $_.Id -Force -ErrorAction SilentlyContinue }
    Start-Sleep -Seconds 1
    Say ($label + " stdout: " + ((Get-Content $so -ErrorAction SilentlyContinue | Select-Object -First 5) -join ' | '))
    Say ($label + " stderr: " + ((Get-Content $se -ErrorAction SilentlyContinue | Select-Object -First 12) -join ' | '))
    foreach ($k in $envs.Keys) { Remove-Item -Path ("Env:" + $k) -ErrorAction SilentlyContinue }
}

$data = Join-Path $Out 'data'
New-Item -ItemType Directory -Force -Path $data | Out-Null
RunEngine 'named-data' @{ IGNEUM_WALLET_DATA = $data }
RunEngine 'default-root' @{}
$root = Join-Path $env:APPDATA 'network.igneum.wallet'
Say ("default root " + $root + " exists: " + (Test-Path $root) + "; beside the program: " + (Test-Path (Join-Path $prefix '.igneum-wallet')))
$ul = Join-Path $root 'updates\updates.log'
if (Test-Path $ul) { Say ("updates.log tail: " + ((Get-Content $ul | Select-Object -Last 4) -join ' | ')) }

$un = Get-ChildItem -Path $prefix -Filter 'unins*.exe' -ErrorAction SilentlyContinue | Select-Object -First 1
if ($un) { Start-Process -FilePath $un.FullName -ArgumentList @('/VERYSILENT', '/SUPPRESSMSGBOXES', '/NORESTART') -Wait -PassThru | Out-Null; Start-Sleep -Seconds 5 }
Say ("uninstalled: engine gone: " + (-not (Test-Path $engine)))
exit 0
