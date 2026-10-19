# Igneum Wallet on a fleet PC: install or update from the signed latest set, no hand on the machine (the founder's order
# of 12 October 2026). Run by the fleet agent at start and hourly (PowerShell 5.1 or newer; ssh-keygen from Windows'
# OpenSSH client):
#   powershell -NoProfile -ExecutionPolicy Bypass -File igneum-wallet-fleet-update.ps1 -Watch 0x<the machine's payout address>
# The same steps as the macOS script: latest.json and its signature verified against the two-key list (namespace
# igneum-release; the superseded and valid-after marks judged by the manifest's written_utc), the named directory's
# SHA256SUMS and its signature, the sums' sha256 against the manifest, the installer the sums name downloaded and its
# sha256 checked, then, when the version is newer than the installed one (or nothing is installed), the installer run
# silently with the relaunch flag (Igneum-Wallet.iss: /IGNOTA=1 relaunches the app when the files are in). A first
# install is put in watch mode on the payout address (watch.json in the app's data directory; no key is made on a fleet
# machine). A failed verification leaves the install untouched and logs the reason. One verdict line.
# Exit: 0 applied or current; 2 a verification failed; 3 a download or the host failed.
param([string]$Watch = $env:IGNEUM_WALLET_WATCH, [string]$Base = $(if ($env:IGNEUM_WALLET_UPDATE_BASE) { $env:IGNEUM_WALLET_UPDATE_BASE } else { 'https://build.igneum.network/releases' }))
$ErrorActionPreference = 'Stop'
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
$Data = if ($env:IGNEUM_WALLET_DATA) { $env:IGNEUM_WALLET_DATA } else { Join-Path $env:APPDATA 'network.igneum.wallet' }
$W = Join-Path $env:TEMP ("igneum-wallet-fleet-" + [guid]::NewGuid().ToString('N').Substring(0, 8)); New-Item -ItemType Directory -Force -Path $W | Out-Null
New-Item -ItemType Directory -Force -Path (Join-Path $Data 'updates') | Out-Null
$Log = Join-Path $Data 'updates\updates.log'
function Utc { (Get-Date).ToUniversalTime().ToString('yyyy-MM-ddTHH:mm:ssZ') }
function Log($s) { Add-Content -Path $Log -Value ("{0} {1}" -f (Utc), $s) }
function Say($s) { Write-Output ("igneum-wallet-fleet-update: " + $s) }
function Done($code) { Remove-Item -Recurse -Force $W -ErrorAction SilentlyContinue; exit $code }
# the release keys of the rotation record (infra/release/keys/allowed_signers on main) and the superseded marks
# (infra/release/keys/SUPERSEDED): a change of key is a change of these lines
$allowed = @(
  'igneum-release namespaces="igneum-release" ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIMGJXt1a3GwTwu2kcq7iMkqFTA3KUCOoDlkr1BWI8UTF',
  'igneum-release namespaces="igneum-release",valid-after="20261012082000Z" ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIN9kDfjTxTcXkCpmIEViz3SQbuRb5B3nEc1QuwV6FkSx',
  'igneum-release namespaces="igneum-release",valid-after="20261012102400Z" ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIIta/7adTpTjbRmK5+vIcWf0cSoSs13nazpjjNQFWqMh automatic',
  'igneum-release namespaces="igneum-release",valid-after="20261012104300Z" ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIP0olnjXkjqAhlhW85BgKP6SVFzASLWZo0PtI0hEgB8t automatic'
)
$superseded = @(

)   # "<fingerprint> <from UTC> ..." per line when a key is superseded
[IO.File]::WriteAllText((Join-Path $W 'allowed'), ($allowed -join "`n") + "`n")
$sshkeygen = Join-Path $env:SystemRoot 'System32\OpenSSH\ssh-keygen.exe'
if (-not (Test-Path $sshkeygen)) { $sshkeygen = 'ssh-keygen' }
function Fetch($url, $to) { try { Invoke-WebRequest -UseBasicParsing -TimeoutSec 120 -Uri $url -OutFile $to; return $true } catch { return $false } }
function Compact($s) { $s -replace '[^0-9]', '' }
# the valid-after mark of each listed key by its fingerprint (ssh-keygen -l over the key alone), judged here rather than
# by -O verify-time, which older Windows OpenSSH builds do not take (the PCs' runs of 12 October 2026 refused a pointer
# the Macs verified Good)
$keyMarks = @{}
foreach ($line in $allowed) {
  $m = [regex]::Match($line, 'valid-after="?([0-9TZ]+)"?'); $mark = if ($m.Success) { $m.Groups[1].Value } else { '' }
  $parts = $line -split ' '; $i = [array]::IndexOf($parts, 'ssh-ed25519'); if ($i -lt 0) { continue }
  $kf = Join-Path $W ('k' + [guid]::NewGuid().ToString('N').Substring(0, 6) + '.pub')
  [IO.File]::WriteAllText($kf, ($parts[$i] + ' ' + $parts[$i + 1] + "`n"))
  $fp = (& $sshkeygen -l -f $kf 2>&1 | Out-String)
  if ($fp -match 'SHA256:[A-Za-z0-9+/=]+') { $keyMarks[$Matches[0]] = $mark }
}
$script:verifyWhy = ''
function Verify($file, $sig, $written) {
  # ssh-keygen reads the data on stdin: cmd's redirection hands it the file's raw bytes (a PowerShell pipe re-encodes
  # the text); the signer's fingerprint is read from the verbose line; valid-after and the superseded marks are judged
  # here against the manifest's written_utc (digits only)
  $t = Compact $written
  $allowedFile = Join-Path $W 'allowed'; $outFile = Join-Path $W 'verify.out'; $errFile = Join-Path $W 'verify.err'
  # no cmd line: Start-Process hands ssh-keygen the file on stdin as raw bytes and the exit code comes from the process
  # (reliability's hand read on the first PC, 12 October 2026: a cmd /c line with quoted paths never reached ssh-keygen)
  $p = Start-Process -FilePath $sshkeygen -ArgumentList @('-Y', 'verify', '-f', "`"$allowedFile`"", '-I', 'igneum-release', '-n', 'igneum-release', '-s', "`"$sig`"") -RedirectStandardInput $file -RedirectStandardOutput $outFile -RedirectStandardError $errFile -NoNewWindow -PassThru -Wait
  $null = $p.Handle
  $outAll = @(); if (Test-Path $outFile) { $outAll += Get-Content $outFile }; if (Test-Path $errFile) { $outAll += Get-Content $errFile }
  [IO.File]::WriteAllText($outFile, ($outAll -join "`n"))
  $out = if (Test-Path $outFile) { Get-Content $outFile } else { @() }
  $script:verifyWhy = (($out | Select-Object -First 3) -join ' | ')
  if ($p.ExitCode -ne 0) { return $null }
  $key = ''; foreach ($l in $out) { if ($l -match 'SHA256:[A-Za-z0-9+/=]+') { $key = $Matches[0]; break } }
  if ($key -and $t -and $keyMarks.ContainsKey($key) -and $keyMarks[$key]) { if ($t -lt (Compact $keyMarks[$key])) { $script:verifyWhy = "the key $key is valid for signing from $($keyMarks[$key]) and this manifest is written $written"; return $null } }
  foreach ($line in $superseded) { $p2 = $line -split ' '; if ($p2[0] -eq $key -and $t -and ($t -gt (Compact $p2[1]))) { $script:verifyWhy = "the key $key is superseded for signing from $($p2[1])"; return $null } }
  if ($key) { return $key } else { return 'igneum-release' }
}
function Sha256($path) { (Get-FileHash -Algorithm SHA256 $path).Hash.ToLower() }
function Newer($a, $b) {
  $pa = ($a -split '-')[0] -split '\.'; if (-not $b) { return $true }; $pb = ($b -split '-')[0] -split '\.'
  for ($i = 0; $i -lt 3; $i++) { $x = [int]$pa[$i]; $y = [int]$pb[$i]; if ($x -gt $y) { return $true }; if ($x -lt $y) { return $false } }
  return $false
}
if (-not (Fetch "$Base/wallet/latest/latest.json" (Join-Path $W 'latest.json'))) { Say 'the release host did not answer for latest.json'; Done 3 }
if (-not (Fetch "$Base/wallet/latest/latest.json.sig" (Join-Path $W 'latest.json.sig'))) { Say 'no signature on latest.json; refused'; Log 'check failed: latest.json unsigned; this install is untouched'; Done 2 }
$latest = Get-Content -Raw (Join-Path $W 'latest.json') | ConvertFrom-Json
$written = [string]$latest.written_utc
$signer = Verify (Join-Path $W 'latest.json') (Join-Path $W 'latest.json.sig') $written
if (-not $signer) { Say "latest.json's signature refused: $script:verifyWhy"; Log "check failed: latest.json's signature refused ($script:verifyWhy); this install is untouched"; Done 2 }
$version = [string]$latest.version; $dir = ([string]$latest.directory).Trim('/'); $sumsSha = [string]$latest.sha256sums_sha256
if ($dir -match '\.\.' -or $dir.StartsWith('/')) { Say 'latest.json names a directory outside the host'; Done 2 }
# the installed version is read from the installed engine itself (its --version line), the prefix from the uninstall
# key in either hive or the installer's default; the registry read alone gave "none" on one Windows host every hour from 12 to 15
# October 2026 and the installer was re-run on a current wallet each time (its code 5 while the wallet was open)
$installed = ''; $prefix = ''
$keyTail = 'Microsoft\Windows\CurrentVersion\Uninstall\{B7D2E1F3-7C9E-4F8B-A042-3D6E9F8C0B12}_is1'
foreach ($k in @("HKCU:\Software\$keyTail", "HKLM:\SOFTWARE\$keyTail", "HKLM:\SOFTWARE\WOW6432Node\$keyTail")) {
  if (Test-Path -LiteralPath $k) { $r = Get-ItemProperty -LiteralPath $k; $installed = [string]$r.DisplayVersion; $prefix = [string]$r.InstallLocation; break }
}
if (-not $prefix) { $prefix = Join-Path $env:LOCALAPPDATA 'Programs\Igneum Wallet' }
$engineExe = Join-Path $prefix 'igneum-wallet-engine.exe'
if (Test-Path -LiteralPath $engineExe) { try { $v = (& $engineExe --version 2>$null | Select-Object -First 1); if ($v -match '(\d+\.\d+\.\d+)') { $installed = $Matches[1] } } catch {} }
if (-not $installed) { Log "installed version read: none (no uninstall key in HKCU or HKLM, no engine at $engineExe)" }
if ($installed -and -not (Newer $version $installed)) { if ($env:IGNEUM_WALLET_FLEET_REPLACE -eq '1' -and (($installed -split '-')[0]) -ne (($version -split '-')[0])) { Say "replacing the development build $installed with the host's latest $version (the fleet's release path)" } else { Say "current: $installed installed, the host's latest is $version (signed by $signer)"; Done 0 } }
if (-not (Fetch "$Base/$dir/SHA256SUMS" (Join-Path $W 'SHA256SUMS'))) { Say "no SHA256SUMS at $Base/$dir"; Done 3 }
if (-not (Fetch "$Base/$dir/SHA256SUMS.sig" (Join-Path $W 'SHA256SUMS.sig'))) { Say 'no signature on SHA256SUMS; refused'; Log "check failed: $version's SHA256SUMS unsigned; this install is untouched"; Done 2 }
if ((Sha256 (Join-Path $W 'SHA256SUMS')) -ne $sumsSha) { Say 'SHA256SUMS is not the one latest.json names'; Log "check failed: $version's SHA256SUMS is not the one latest.json names; this install is untouched"; Done 2 }
if (-not (Verify (Join-Path $W 'SHA256SUMS') (Join-Path $W 'SHA256SUMS.sig') $written)) { Say "SHA256SUMS's signature refused: $script:verifyWhy"; Log "check failed: $version's SHA256SUMS signature refused ($script:verifyWhy); this install is untouched"; Done 2 }
$line = (Get-Content (Join-Path $W 'SHA256SUMS') | Where-Object { $_ -match '  Igneum-Wallet-Setup-.*\.exe$' } | Select-Object -First 1)
if (-not $line) { Say "$version has no installer in its sums"; Done 2 }
$exeSha = ($line -split '\s+')[0]; $exe = ($line -split '\s+')[-1]
$exePath = Join-Path $W $exe
if (-not (Fetch "$Base/$dir/$exe" $exePath)) { Say 'the installer did not download'; Done 3 }
if ((Sha256 $exePath) -ne $exeSha) { Say "the installer's sha256 is not the sums'"; Log "check failed: $version's installer sha256 mismatch; this install is untouched"; Done 2 }
# a first install: watch mode on the payout address, nothing else in the data directory
if ($Watch -and -not (Test-Path (Join-Path $Data 'vault.json')) -and -not (Test-Path (Join-Path $Data 'watch.json'))) {
  $now = [int][double]::Parse((Get-Date -UFormat %s))
  [IO.File]::WriteAllText((Join-Path $Data 'watch.json'), "{`n  `"watch_only`": true,`n  `"address`": `"$Watch`",`n  `"created_unix`": $now`n}`n")
  Log "installed $version in watch mode on $Watch (fleet)"
}
$pair = "$(if ($installed) { $installed } else { 'none' }) -> $version"
Log "applying $pair (fleet, the installer $exe, signed by $signer)"
# the installer alone is waited for (WaitForExit on its process): Start-Process -Wait waits for every descendant too,
# and the installer's relaunch line makes the new wallet one, so the "applied" line came only when that wallet ended
# (a Windows host, 14 October 2026: seven hours after the install)
$p = Start-Process -FilePath $exePath -ArgumentList '/VERYSILENT', '/SUPPRESSMSGBOXES', '/NORESTART', '/CLOSEAPPLICATIONS', '/FORCECLOSEAPPLICATIONS', '/IGNOTA=1' -PassThru
$null = $p.Handle
$p.WaitForExit()
# the installer's codes in words (0.0.21): 7 = the stop step found a file of ours still held by a window this installer cannot close; 5 = a file stayed in use at the copy
$why = switch ($p.ExitCode) { 7 { 'a previous Igneum Wallet is still open and holds its files; close that window and the next try applies' } 5 { 'a file of the previous wallet stayed in use at the copy (its window could not be closed)' } default { "the installer ended with code $($p.ExitCode)" } }
if ($p.ExitCode -ne 0) { Say $why; Log "apply failed: $why (code $($p.ExitCode); $pair)"; Done 3 }
Log "applied $pair (fleet, signed by $signer)"
Say "applied $pair (signed by $signer); the app relaunches"
Done 0
