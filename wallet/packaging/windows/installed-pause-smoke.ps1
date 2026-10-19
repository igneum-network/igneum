# installed-pause-smoke.ps1: on a machine whose INSTALLED wallet is running (the host is single-instance, so an installer
# smoke cannot start its own host beside it), one job: stop the installed wallet by its own recorded script (its pid read
# from its command line and logged), run installer-smoke.ps1 on the Setup under test, then relaunch the installed wallet
# so its window is back before the job ends. The installed copy's files and data are never touched. Written 14 October
# 2026 for wallet 0.0.9 on the second Windows machine under test, on the founder's word through main (the PC rule).
param(
    [string]$Setup = '',
    [string]$Out = '',
    [switch]$RelaunchOnly,
    [string]$Script = 'installer-smoke.ps1'
)
$ErrorActionPreference = 'Continue'
if (-not $Out) { $Out = Join-Path $env:TEMP 'igneum-wallet-pause-smoke' }
New-Item -ItemType Directory -Force -Path $Out | Out-Null
$log = Join-Path $Out 'pause.log'
'' | Set-Content $log
function Say([string]$t) { $line = ('[' + (Get-Date).ToUniversalTime().ToString('HH:mm:ssZ') + '] ' + $t); Write-Host $line; Add-Content $log $line }

$installed = Join-Path $env:LOCALAPPDATA 'Programs\Igneum Wallet'
$hostExe = Join-Path $installed 'Igneum Wallet.exe'
$stop = Join-Path $installed 'stop-igneum-wallet.ps1'
$running = Get-Process -Name 'Igneum Wallet' -ErrorAction SilentlyContinue | Select-Object -First 1
# -RelaunchOnly: the repair alone (the installed wallet's engine ended by the first 0.0.9 smoke, 14 October 2026): no stop,
# no smoke; a host still running without its engine is ended by its pid first so the relaunch brings a whole wallet
if ($RelaunchOnly) {
    if ($running) {
        $cmd = (Get-CimInstance Win32_Process -Filter ("ProcessId = " + $running.Id) -ErrorAction SilentlyContinue).CommandLine
        $eng0 = Get-Process -Name 'igneum-wallet-engine' -ErrorAction SilentlyContinue | Select-Object -First 1
        Say ("installed host running: pid " + $running.Id + ", engine: " + $(if ($eng0) { $eng0.Id } else { 'none' }) + ", command line: " + $cmd)
        if (-not $eng0 -and $cmd -like '*Igneum Wallet.exe*') { Stop-Process -Id $running.Id -Force -ErrorAction SilentlyContinue; Say 'the engineless host ended by its pid for the relaunch'; Start-Sleep -Seconds 2 }
        elseif ($eng0) { Say 'the installed wallet is whole; nothing to relaunch'; exit 0 }
    }
    $rc = 0
} elseif ($running) {
    $cmd = (Get-CimInstance Win32_Process -Filter ("ProcessId = " + $running.Id) -ErrorAction SilentlyContinue).CommandLine
    Say ("installed wallet host running: pid " + $running.Id + ", command line: " + $cmd)
    if (Test-Path $stop) {
        & powershell -NoProfile -ExecutionPolicy Bypass -File $stop -Install $installed 2>&1 | ForEach-Object { Say ('stop script: ' + $_) }
    } else {
        Say ("no stop script at " + $stop + "; the host ended by its pid (its command line read above)")
        Stop-Process -Id $running.Id -Force -ErrorAction SilentlyContinue
    }
    $i = 0
    while ($i -lt 40 -and (Get-Process -Name 'Igneum Wallet','igneum-wallet-engine','igneum-signer' -ErrorAction SilentlyContinue)) { Start-Sleep -Milliseconds 500; $i++ }
    $left = Get-Process -Name 'Igneum Wallet','igneum-wallet-engine','igneum-signer' -ErrorAction SilentlyContinue
    Say ("installed wallet stopped after " + ($i / 2) + " s; left: " + $(if ($left) { ($left | ForEach-Object { $_.Name + ' ' + $_.Id }) -join ', ' } else { 'none' }))
} else {
    Say 'no installed wallet host running; nothing to stop'
}

if (-not $RelaunchOnly) {
    # the script run inside the pause: the installer smoke, or the gate's update-over-open row (0.0.21)
    $smoke = Join-Path $PSScriptRoot $Script
    Say ("smoke: " + $smoke + " on " + $Setup)
    & powershell -NoProfile -ExecutionPolicy Bypass -File $smoke -Setup $Setup -Out $Out 2>&1 | ForEach-Object { Write-Host $_ }
    $rc = $LASTEXITCODE
    Say ("smoke exit " + $rc)
}

if (Test-Path $hostExe) {
    # the relaunch through the shell (explorer.exe) so the wallet runs at the desktop's own level whatever this job's:
    # a wallet relaunched by an elevated job was beyond the next, medium-level job's reach (a Windows host, 14 and 15 October 2026)
    Start-Process -FilePath 'explorer.exe' -ArgumentList ('"' + $hostExe + '"') | Out-Null
    Start-Sleep -Seconds 8
    $back = Get-Process -Name 'Igneum Wallet' -ErrorAction SilentlyContinue | Select-Object -First 1
    $eng = Get-Process -Name 'igneum-wallet-engine' -ErrorAction SilentlyContinue | Select-Object -First 1
    Say ("installed wallet relaunched: host pid " + $(if ($back) { $back.Id } else { 'none' }) + ", engine pid " + $(if ($eng) { $eng.Id } else { 'none' }) + " (" + (& $hostExe --version 2>$null) + ")")
} else {
    Say ("no installed wallet at " + $hostExe + "; nothing relaunched")
}
exit $rc
