# Builds dist\Igneum-Wallet-Setup-<version>.exe on a Windows PC (the second Windows machine through its queue, OLD-BASE.md step 10; the form
# of the product team's build-installer.ps1 cut to the wallet's needs). Needs Inno Setup 6 on the PC (found by the registry's
# uninstall keys or under Program Files (x86)); installs nothing itself.
#   build-wallet-installer.ps1 -Payload <folder> -Version <version>
# The payload folder (igneum-windows-wallet) holds igneum-wallet-engine.exe, igneum-signer.exe, igneum-wallet.exe, ui\,
# icon.ico, igneum-wallet.cmd, stop-igneum-wallet.ps1 and, when BUILD-WALLET-APP.bat ran, "Igneum Wallet.exe".
param(
    [string]$Payload = '',
    [string]$Version = '0.0.0'
)
$ErrorActionPreference = 'Stop'
$here = Split-Path -Parent $MyInvocation.MyCommand.Path
$dist = Join-Path $here 'dist'
New-Item -ItemType Directory -Force -Path $dist | Out-Null
function Say([string]$text) { Write-Host ('[build] ' + $text) }

if (-not $Payload) { $Payload = Join-Path $here 'igneum-windows-wallet' }
if (-not (Test-Path (Join-Path $Payload 'igneum-wallet-engine.exe'))) { throw "no igneum-wallet-engine.exe under $Payload" }
if (-not (Test-Path (Join-Path $Payload 'igneum-signer.exe'))) { throw "no igneum-signer.exe under $Payload" }
if (-not (Test-Path (Join-Path $Payload 'ui\index.html'))) { throw "no ui\index.html under $Payload" }
if (-not (Test-Path (Join-Path $Payload 'icon.ico'))) { throw "no icon.ico under $Payload" }
if (-not (Test-Path (Join-Path $Payload 'Igneum Wallet.exe'))) { Say 'no "Igneum Wallet.exe" in the payload: the installer ships without the window host (the launcher opens the page in the browser)' }
Copy-Item (Join-Path $here 'stop-igneum-wallet.ps1') (Join-Path $Payload 'stop-igneum-wallet.ps1') -Force

function Find-Iscc {
    foreach ($key in @('HKLM:\SOFTWARE\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall\Inno Setup 6_is1',
                       'HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\Inno Setup 6_is1',
                       'HKCU:\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\Inno Setup 6_is1')) {
        try {
            $loc = (Get-ItemProperty -Path $key -ErrorAction Stop).InstallLocation
            if ($loc -and (Test-Path (Join-Path $loc 'ISCC.exe'))) { return (Join-Path $loc 'ISCC.exe') }
        } catch {}
    }
    foreach ($p in @("${env:ProgramFiles(x86)}\Inno Setup 6\ISCC.exe", "${env:ProgramFiles}\Inno Setup 6\ISCC.exe")) {
        if (Test-Path $p) { return $p }
    }
    throw 'Inno Setup 6 was not found on this PC (ISCC.exe): the installer cannot be built here'
}
$iscc = Find-Iscc
Say "ISCC: $iscc"
$iss = Join-Path $here 'Igneum-Wallet.iss'
$payloadAbs = (Resolve-Path $Payload).Path
Say "payload: $payloadAbs; version: $Version"
# the organisation's code signing (docs/release/ORG-SIGNING.md sections 5 and 6): the custody file names the Windows
# thumbprint ("windows thumbprint <hex> <subject...> <host> <from utc>"); with a line, every engine in the payload is
# signed by that thumbprint with a timestamp and verified before the Setup packs it, and the Setup and uninstaller
# are signed by the compiler's igneumsign tool; a host without signtool or the token fails here BY NAME and no Setup
# is made (the absent-artefact clause). Without a line the build is as before and signing.json says "none".
$custody = Join-Path $here '..\..\..\infra\release\keys\PLATFORM-SIGNING'
$thumb = ''; $subject = ''
if (Test-Path $custody) {
    $line = Get-Content $custody | Where-Object { $_ -match '^windows\s+thumbprint\s+([0-9A-Fa-f]{40})\s+(.*)$' } | Select-Object -Last 1
    if ($line) { $null = $line -match '^windows\s+thumbprint\s+([0-9A-Fa-f]{40})\s+(.*)$'; $thumb = $Matches[1]; $subject = $Matches[2] }
}
$tsa = 'http://timestamp.digicert.com'
$signing = @{ status = 'none'; note = 'no Windows identity named in infra/release/keys/PLATFORM-SIGNING: the Setup is unsigned' }
$signArgs = @()
if ($thumb) {
    $signtool = Get-Command signtool.exe -ErrorAction SilentlyContinue
    if (-not $signtool) { throw ("the custody file names the Windows thumbprint " + $thumb + " but signtool.exe is not on this host: the signing step cannot run, no Setup is made") }
    $cert = Get-ChildItem Cert:\CurrentUser\My, Cert:\LocalMachine\My -ErrorAction SilentlyContinue | Where-Object { $_.Thumbprint -ieq $thumb } | Select-Object -First 1
    if (-not $cert) { throw ("the custody file names the Windows thumbprint " + $thumb + " but no certificate with it is on this host (the token absent): the signing step cannot run, no Setup is made") }
    $verdicts = @{}
    foreach ($exe in @('igneum-wallet-engine.exe', 'igneum-signer.exe', 'igneum-wallet.exe')) {
        $f = Join-Path $payloadAbs $exe
        if (-not (Test-Path $f)) { throw ("the payload holds no " + $exe + " to sign") }
        & $signtool.Source sign /fd SHA256 /td SHA256 /tr $tsa /sha1 $thumb /d 'Igneum Wallet' $f
        if ($LASTEXITCODE -ne 0) { throw ("signtool could not sign " + $exe + " (exit " + $LASTEXITCODE + "): no Setup is made") }
        & $signtool.Source verify /pa /v $f | Out-Null
        $verdicts[$exe] = $(if ($LASTEXITCODE -eq 0) { 'verified' } else { 'not verified' })
        if ($LASTEXITCODE -ne 0) { throw ("signtool does not verify " + $exe + " after signing: no Setup is made") }
    }
    $signArgs = @('/DSign=1', ('/Signeumsign=' + $signtool.Source + ' sign /fd SHA256 /td SHA256 /tr ' + $tsa + ' /sha1 ' + $thumb + ' /d "Igneum Wallet" $p'))
    $signing = @{ status = 'signed'; subject = $subject; thumbprint = $thumb; timestamp_authority = $tsa; engines = $verdicts }
    Say ("signing by the thumbprint " + $thumb + " (" + $subject + ")")
}
& $iscc "/DPayload=$payloadAbs" "/DAppVersion=$Version" "/O$dist" @signArgs $iss
if ($LASTEXITCODE -ne 0) { throw "ISCC exited $LASTEXITCODE" }
$out = Join-Path $dist "Igneum-Wallet-Setup-$Version.exe"
if (-not (Test-Path $out)) { throw "ISCC made no $out" }
if ($thumb) {
    & $signtool.Source verify /pa /v $out | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'signtool does not verify the Setup after the build: the Setup is not kept' }
    $signing['setup'] = 'verified'
}
($signing | ConvertTo-Json -Depth 4) | Set-Content -Encoding ascii (Join-Path $dist 'signing.json')
$sha = (Get-FileHash -Algorithm SHA256 $out).Hash.ToLower()
Say ("built " + $out + " " + (Get-Item $out).Length + " bytes sha256 " + $sha)
"$sha  Igneum-Wallet-Setup-$Version.exe" | Set-Content -Encoding ascii (Join-Path $dist 'SHA256SUMS')
