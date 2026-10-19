# Stops a running Igneum Wallet on this PC. Run by the installer before an upgrade and on uninstall; also the Start Menu
# "Stop Igneum Wallet" entry. 0.0.23 (7 October 2026, the second Windows machine at 20:54 UK: an installer quit the engine, the window host's
# restart ladder started the old engine again 10 s later, the host stayed locked and no file was replaced): the window
# host goes FIRST, by its path under the install folder, so nothing can restart the engine; then the engine is asked to
# quit through its local API (the node after it), waited for, and whatever of ours is left is ended by path.
# -Install <dir>: the install folder whose processes to end. The installer passes its {app} (this script then runs from
# the installer's temp folder, so its own location is not the install dir); the Start Menu entry and uninstall run it
# from the install folder itself and give nothing.
param([string]$Install = '')
$ErrorActionPreference = 'Continue'
$install = if ($Install) { $Install.TrimEnd('\') } else { Split-Path -Parent $MyInvocation.MyCommand.Path }
Write-Host "stopping Igneum Wallet under $install"
function Ours { @(Get-CimInstance Win32_Process -ErrorAction SilentlyContinue | Where-Object { $_.ExecutablePath -and $_.ExecutablePath.StartsWith($install, [StringComparison]::OrdinalIgnoreCase) }) }
foreach ($h in (Ours | Where-Object { $_.Name -eq 'Igneum Wallet.exe' })) {
    Write-Host "ending the window host (pid $($h.ProcessId)) first, so it cannot start the engine again"
    Stop-Process -Id $h.ProcessId -Force -ErrorAction SilentlyContinue
}
$urlFile = Join-Path $env:LOCALAPPDATA 'igneum\wallet\wallet.url'
$asked = $false
if (Test-Path $urlFile) {
    $url = (Get-Content $urlFile -Raw).Trim()
    if ($url) {
        try {
            Invoke-WebRequest -Uri ($url + 'api/quit') -Method POST -Body '{}' -ContentType 'application/json' -UseBasicParsing -TimeoutSec 5 | Out-Null
            $asked = $true
            Write-Host 'asked the engine to stop (the node after it)'
        } catch { }
    }
}
if ($asked) {
    $deadline = (Get-Date).AddSeconds(50)
    while ((Get-Date) -lt $deadline) {
        $alive = @(Ours | Where-Object { $_.Name -eq 'igneum-wallet-engine.exe' -or $_.Name -eq 'igneum-signer.exe' })
        if ($alive.Count -eq 0) { break }
        Start-Sleep -Milliseconds 500
    }
}
foreach ($p in (Ours)) {
    Write-Host "ending $($p.Name) (pid $($p.ProcessId))"
    Stop-Process -Id $p.ProcessId -Force -ErrorAction SilentlyContinue
}
# the files must be free before Inno copies: each exe opened for write, up to 30 s; one clear line for a file that stays locked
# (the installer's /CLOSEAPPLICATIONS is the fallback for that one)
$deadline = (Get-Date).AddSeconds(30)
$locked = @()
do {
    $locked = @()
    foreach ($n in @('Igneum Wallet.exe', 'igneum-wallet-engine.exe', 'igneum-signer.exe')){
        $f = Join-Path $install $n
        if (-not (Test-Path $f)) { continue }
        try { $fs = [IO.File]::Open($f, [IO.FileMode]::Open, [IO.FileAccess]::ReadWrite, [IO.FileShare]::None); $fs.Close() } catch { $locked += $n }
    }
    if ($locked.Count -eq 0) { break }
    Start-Sleep -Milliseconds 500
} while ((Get-Date) -lt $deadline)
if ($locked.Count) { Write-Host ("STILL LOCKED after 30 s: " + ($locked -join ', ') + " (the installer's close-applications step is the fallback; a locked file is why an install leaves the old version in place)") } else { Write-Host 'every file of ours is free' }
Write-Host 'Igneum Wallet is stopped.'
if ($locked.Count) { exit 3 }
