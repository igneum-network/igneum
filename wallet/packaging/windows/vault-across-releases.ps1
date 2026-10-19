# vault-across-releases.ps1 -Setup <new Setup exe> -Out <dir> [-OldVersion 0.0.12-08a32c5c]: the landing check's rule of
# 15 October 2026 (F-W1) on Windows: the OLD release's Setup (fetched from the release host) installed silently into a
# prefix, its engine run directly on a fresh data directory (the installed form: no IGNEUM_WALLET_DATA, the engine's own
# root under %APPDATA%\network.igneum.wallet is NOT used; a test root is named so the machine's wallet is untouched),
# a vault created with a password, unlocked, locked, quit; the NEW Setup under test installed over the same prefix
# silently as the updater does; the new engine run on the same data directory, unlocked with the same password.
# Rows and the verdict in vault-across.log; exit 0 only on PASS. No secret printed.
param([string]$Setup = '', [string]$Out = '', [string]$OldVersion = '0.0.12-08a32c5c')
$ErrorActionPreference = 'Continue'
if (-not $Setup -or -not (Test-Path $Setup)) { Write-Host "no Setup exe at '$Setup'"; exit 2 }
if (-not $Out) { $Out = Join-Path $env:TEMP 'igneum-wallet-vault-across' }
New-Item -ItemType Directory -Force -Path $Out | Out-Null
$log = Join-Path $Out 'vault-across.log'; '' | Set-Content $log
function Say([string]$t) { $line = ('[' + (Get-Date).ToUniversalTime().ToString('HH:mm:ssZ') + '] ' + $t); Write-Host $line; Add-Content $log $line }
$prefix = Join-Path $Out 'prefix'; $data = Join-Path $Out 'data'; New-Item -ItemType Directory -Force -Path $data | Out-Null
$oldName = "Igneum-Wallet-Setup-" + $OldVersion.Split('-')[0] + ".exe"
$old = Join-Path $Out $oldName
try { Invoke-WebRequest -Uri ("https://build.igneum.network/releases/wallet/" + $OldVersion + "/" + $oldName) -OutFile $old -UseBasicParsing } catch { Say ("the old Setup did not fetch: " + $_.Exception.Message); exit 2 }
function Install([string]$exe) {
    $p = Start-Process -FilePath $exe -ArgumentList @('/VERYSILENT', '/SUPPRESSMSGBOXES', '/NORESTART', '/SP-', "/DIR=`"$prefix`"", '/MERGETASKS=!desktopicon', '/CLOSEAPPLICATIONS', '/FORCECLOSEAPPLICATIONS') -Wait -PassThru
    return $p.ExitCode
}
$engine = Join-Path $prefix 'igneum-wallet-engine.exe'; $signer = Join-Path $prefix 'igneum-signer.exe'; $ui = Join-Path $prefix 'ui'
$pw = 'vault-across-' + [DateTimeOffset]::UtcNow.ToUnixTimeSeconds() + '-' + (Get-Random) + '-test-value'
function StartEngine([int]$port) {
    $urlf = Join-Path $Out 'engine-url.json'; if (Test-Path $urlf) { Remove-Item $urlf -Force }
    $env:IGNEUM_WALLET_DATA = $data; $env:IGNEUM_WALLET_URL_FILE = $urlf; $env:IGNEUM_WALLET_UPDATE_FIRST_SECS = '600'
    $proc = Start-Process -FilePath $engine -ArgumentList @('--ui', $ui, '--signer', $signer, '--bridge-port', "$port") -WorkingDirectory $prefix -PassThru -WindowStyle Hidden
    $i = 0; while ($i -lt 60 -and -not (Test-Path $urlf)) { Start-Sleep -Milliseconds 500; $i++ }
    $script:eng = $proc
    if (Test-Path $urlf) { return ((Get-Content $urlf -Raw | ConvertFrom-Json).url.TrimEnd('/')) } else { return '' }
}
function Api([string]$u, [string]$path, [string]$body) { try { return (Invoke-RestMethod -Method Post -Uri ($u + '/' + $path) -ContentType 'application/json' -Body $body -TimeoutSec 180) } catch { $r = $_.ErrorDetails.Message; if (-not $r) { $r = $_.Exception.Message }; return @{ error = $r } } }
function Phase([string]$u) { try { $s = Invoke-RestMethod -Uri ($u + '/api/state') -TimeoutSec 30; return ($s.phase + ' ' + $s.address) } catch { return 'no answer' } }
function StopEngine([string]$u) { $null = Api $u 'api/quit' '{}'; $i = 0; while ($i -lt 30 -and -not $script:eng.HasExited) { Start-Sleep -Milliseconds 500; $i++ }; if (-not $script:eng.HasExited) { Stop-Process -Id $script:eng.Id -Force -ErrorAction SilentlyContinue }; Start-Sleep -Seconds 1 }

Say ("old install exit " + (Install $old) + "; engine " + (& $engine --version 2>$null))
$u = StartEngine 26931
$r = Api $u 'api/create' ('{"password":"' + $pw + '"}'); Say ("old create: " + $(if ($r.address) { $r.address } else { $r.error }))
Say ("old after create: " + (Phase $u))
$null = Api $u 'api/lock' '{}'; Say ("old after lock: " + (Phase $u))
$r = Api $u 'api/unlock' ('{"password":"' + $pw + '"}'); Say ("old unlock: " + $(if ($r.address) { 'ok' } else { $r.error }))
StopEngine $u
Say ("vault sha after the old engine: " + (Get-FileHash -Algorithm SHA256 (Join-Path $data 'vault.json')).Hash.Substring(0, 16))
Say ("new install over the prefix exit " + (Install $Setup) + "; engine " + (& $engine --version 2>$null))
$u = StartEngine 26932
Say ("new at open: " + (Phase $u))
$r = Api $u 'api/unlock' ('{"password":"' + $pw + '"}'); Say ("new unlock: " + $(if ($r.address) { 'ok' } else { $r.error }))
$p = Phase $u; Say ("new after unlock: " + $p)
StopEngine $u
Say ("vault sha after the new engine: " + (Get-FileHash -Algorithm SHA256 (Join-Path $data 'vault.json')).Hash.Substring(0, 16))
$un = Get-ChildItem -Path $prefix -Filter 'unins*.exe' -ErrorAction SilentlyContinue | Select-Object -First 1
if ($un) { Start-Process -FilePath $un.FullName -ArgumentList @('/VERYSILENT', '/SUPPRESSMSGBOXES', '/NORESTART') -Wait -PassThru | Out-Null }
Remove-Item -Recurse -Force $data -ErrorAction SilentlyContinue; Remove-Item -Force $old -ErrorAction SilentlyContinue
if ($p -like 'home*') { Say 'VAULT ACROSS RELEASES (windows): PASS'; exit 0 } else { Say 'VAULT ACROSS RELEASES (windows): FAIL'; exit 1 }
