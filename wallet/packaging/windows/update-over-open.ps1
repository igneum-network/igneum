# update-over-open.ps1 -Setup <new Setup exe> -Out <dir> [-OldVersion 0.0.20-d8d544e2]: the landing check's rule of 16 October 2026
# (the project's one fix after a Windows host stayed on 0.0.12 for two days, every hourly try ending with the installer's
# code 5): an update applies while a previous wallet is OPEN, and a file this installer cannot free ends the install
# with words, never a bare code. Rows: install-old (the previous release's Setup into a prefix), start-old (its window
# host up on a test data directory, its engine answering), update-over-open (the Setup under test run silently as the
# fleet task and the engine's apply run it: exit 0, the new engine in the prefix, the wallet relaunched by the Run
# line), locked-file (everything stopped, the prefix's engine held open by this process with no sharing, the Setup run
# again: exit 7 and the stop step's words in its log), the uninstall. Runs inside the pause form on a machine whose
# installed wallet is running (the host is single-instance). The test root is its own prefix under -Out; the machine's
# installed wallet is never read or ended here; every process ended is one of the prefix's, recorded by pid with its
# command line first (the rule of record).
param([string]$Setup = '', [string]$Out = '', [string]$OldVersion = '0.0.20-d8d544e2')
$ErrorActionPreference = 'Continue'
if (-not $Setup -or -not (Test-Path $Setup)) { Write-Host "no Setup exe at '$Setup'"; exit 2 }
if (-not $Out) { $Out = Join-Path $env:TEMP 'igneum-wallet-update-over-open' }
New-Item -ItemType Directory -Force -Path $Out | Out-Null
$log = Join-Path $Out 'over-open.log'; '' | Set-Content $log
$rows = [ordered]@{}; $fail = $false
function Say([string]$t) { $line = ('[' + (Get-Date).ToUniversalTime().ToString('HH:mm:ssZ') + '] ' + $t); Write-Host $line; Add-Content $log $line }
function Row([string]$name, [bool]$ok, [string]$why) { $script:rows[$name] = $(if ($ok) { 'PASS' } else { 'FAIL' }); if (-not $ok) { $script:fail = $true }; Say ('ROW ' + $name + ' ' + $(if ($ok) { 'PASS' } else { 'FAIL' }) + ' ' + $why) }
$prefix = Join-Path $Out 'prefix'; $data = Join-Path $Out 'data'; New-Item -ItemType Directory -Force -Path $data | Out-Null
$oldName = "Igneum-Wallet-Setup-" + $OldVersion.Split('-')[0] + ".exe"
$old = Join-Path $Out $oldName
try { Invoke-WebRequest -Uri ("https://build.igneum.network/releases/wallet/" + $OldVersion + "/" + $oldName) -OutFile $old -UseBasicParsing } catch { Say ("the old Setup did not fetch: " + $_.Exception.Message); exit 2 }
$engine = Join-Path $prefix 'igneum-wallet-engine.exe'; $hostExe = Join-Path $prefix 'Igneum Wallet.exe'
# the processes of THIS test's own prefix (under $Out, never the machine's installed wallet), each read by its executable
# path and command line and recorded by pid before it is ended: the rule of record (F-194) ends a process only by a
# recorded pid after its command line is read, never by a name or a pattern
function Ours { @(Get-CimInstance Win32_Process -ErrorAction SilentlyContinue | Where-Object { $_.ExecutablePath -and $_.ExecutablePath.StartsWith($prefix, [StringComparison]::OrdinalIgnoreCase) }) }
$recorded = @()
function StopOurs {
    foreach ($p in (Ours)) { Say ("recorded pid " + $p.ProcessId + ": " + $p.CommandLine); $script:recorded += $p.ProcessId }
    foreach ($id in $script:recorded) { Stop-Process -Id $id -Force -ErrorAction SilentlyContinue; Say ("ended the recorded pid " + $id) }
    $script:recorded = @(); Start-Sleep -Seconds 2
}

# ---- install-old ----
$p = Start-Process -FilePath $old -ArgumentList @('/VERYSILENT', '/SUPPRESSMSGBOXES', '/NORESTART', '/SP-', "/DIR=`"$prefix`"", '/MERGETASKS=!desktopicon', "/LOG=`"$Out\install-old.log`"") -Wait -PassThru
$oldV = (& $engine --version 2>$null)
Row 'install-old' (($p.ExitCode -eq 0) -and (Test-Path $hostExe)) ("exit " + $p.ExitCode + "; " + $oldV)

# ---- start-old: the previous release's window host on the test data directory ----
$urlf = Join-Path $Out 'engine-url.json'; if (Test-Path $urlf) { Remove-Item $urlf -Force }
$env:IGNEUM_WALLET_DATA = $data; $env:IGNEUM_WALLET_URL_FILE = $urlf; $env:IGNEUM_WALLET_UPDATE_FIRST_SECS = '3600'
$hp = Start-Process -FilePath $hostExe -WorkingDirectory $prefix -PassThru
Say ("started the previous release's window under the test prefix: pid " + $hp.Id)
$i = 0; while ($i -lt 60 -and -not (Test-Path $urlf)) { Start-Sleep -Milliseconds 500; $i++ }
$eng = Ours | Where-Object { $_.Name -eq 'igneum-wallet-engine.exe' } | Select-Object -First 1
Row 'start-old' ((Test-Path $urlf) -and ($null -ne $eng)) ("host pid " + $hp.Id + ", engine pid " + $(if ($eng) { $eng.ProcessId } else { 'none' }) + ", the run URL written: " + (Test-Path $urlf))

# ---- update-over-open: the Setup under test as the fleet task and the engine's apply run it, with the wallet open ----
$u = Start-Process -FilePath $Setup -ArgumentList @('/VERYSILENT', '/SUPPRESSMSGBOXES', '/NORESTART', '/CLOSEAPPLICATIONS', '/FORCECLOSEAPPLICATIONS', '/IGNOTA=1', "/LOG=`"$Out\update.log`"") -PassThru
$u.WaitForExit()
Start-Sleep -Seconds 6
$newV = (& $engine --version 2>$null)
$relaunched = Ours | Where-Object { $_.Name -eq 'Igneum Wallet.exe' } | Select-Object -First 1
$stopLine = (Select-String -Path (Join-Path $Out 'update.log') -Pattern 'stop step: exit' -SimpleMatch | Select-Object -First 1).Line
Row 'update-over-open' (($u.ExitCode -eq 0) -and ($newV -ne $oldV) -and ($newV -like '*0.0.*')) ("exit " + $u.ExitCode + "; the prefix's engine " + $newV + " (was " + $oldV + "); the wallet relaunched by the Run line: " + ($null -ne $relaunched) + "; " + $stopLine)
StopOurs

# ---- locked-file: a file of ours held by a process this installer cannot end: the words, not code 5 ----
$fs = $null
try { $fs = [IO.File]::Open($engine, [IO.FileMode]::Open, [IO.FileAccess]::ReadWrite, [IO.FileShare]::None) } catch { Say ("could not hold the engine file: " + $_.Exception.Message) }
$l = Start-Process -FilePath $Setup -ArgumentList @('/VERYSILENT', '/SUPPRESSMSGBOXES', '/NORESTART', '/CLOSEAPPLICATIONS', '/FORCECLOSEAPPLICATIONS', '/IGNOTA=1', "/LOG=`"$Out\update-locked.log`"") -PassThru
$l.WaitForExit()
if ($fs) { $fs.Close() }
$words = (Select-String -Path (Join-Path $Out 'update-locked.log') -Pattern 'still open and holds its files' -SimpleMatch | Select-Object -First 1).Line
Row 'locked-file' (($l.ExitCode -eq 7) -and ($null -ne $words)) ("exit " + $l.ExitCode + " (7 = the stop step's words, never a file-in-use abort with code 5); the log: " + $(if ($words) { $words.Trim() } else { 'no words' }))
StopOurs

# ---- the uninstall and the test root ----
$un = Get-ChildItem -Path $prefix -Filter 'unins*.exe' -ErrorAction SilentlyContinue | Select-Object -First 1
if ($un) { Start-Process -FilePath $un.FullName -ArgumentList @('/VERYSILENT', '/SUPPRESSMSGBOXES', '/NORESTART') -Wait -PassThru | Out-Null }
Remove-Item -Recurse -Force $data -ErrorAction SilentlyContinue; Remove-Item -Force $old -ErrorAction SilentlyContinue
@{ rows = $rows; verdict = $(if ($fail) { 'FAIL' } else { 'PASS' }); setup = (Split-Path $Setup -Leaf); old = $OldVersion } | ConvertTo-Json | Set-Content (Join-Path $Out 'over-open.json')
if ($fail) { Say 'UPDATE OVER AN OPEN WALLET: FAIL'; exit 1 } else { Say 'UPDATE OVER AN OPEN WALLET: PASS'; exit 0 }
