# Builds dist\Ember-Setup-<version>.exe on a Windows PC (started by BUILD-INSTALLER.bat, or run directly).
#   1. Finds ISCC.exe (Inno Setup 6.3 or newer); installs Inno Setup 6 through winget when it is missing.
#   2. Finds the art (brand\icons in the repo layout, or an art\ folder next to this script).
#   3. Finds the payload: the extracted ember-windows-app folder (igneum-app.exe, ember-cli.exe, igneum-worker-cuda.exe, made
#      by make-payload-ember.sh) next to this script, in the parent folder, or given with -Payload; otherwise the zip at
#      -PayloadUrl is downloaded. "Ember.exe" (app\windows\BUILD-APP.bat) is shipped when it is in the payload.
#   4. Copies the payload into build\payload without logs, packs and per-PC worker builds.
#   5. Stamps the coin icon and the version block into igneum-app.exe, ember-cli.exe and igneum-worker-cuda.exe with rcedit when
#      the exes do not carry them yet (the Mac relink with packaging\windows\embed-resources.sh --relink makes this a no-op).
#   6. Compiles Ember.iss and prints where the installer is.
#
#   build-installer.ps1 [-Payload <folder>] [-PayloadUrl <zip url>] [-Version an earlier release] [-NoRcedit] [-NoWinget]
param(
    [string]$Payload = '',
    [string]$PayloadUrl = $(if ($env:IGNEUM_PAYLOAD_URL) { $env:IGNEUM_PAYLOAD_URL } else { 'https://dl.igneum.network/ember-windows-app.zip' }),
    [string]$Version = '0.1.0',
    # PRODUCT=public|lab (packaging/mac/packaged-config.sh, 8 October 2026): the installer's AppId, AppName, folder and file name
    [ValidateSet('public', 'lab')][string]$Product = $(if ($env:PRODUCT) { $env:PRODUCT } else { 'public' }),
    [switch]$NoRcedit,
    [switch]$NoWinget
)
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
$here = Split-Path -Parent $MyInvocation.MyCommand.Path
$build = Join-Path $here 'build'
$dist = Join-Path $here 'dist'
$rceditUrl = 'https://github.com/electron/rcedit/releases/download/v0.2.0/rcedit-x64.exe'
New-Item -ItemType Directory -Force -Path $build, $dist | Out-Null

function Say([string]$text) { Write-Host ('[build] ' + $text) }

function Find-Iscc {
    $candidates = @()
    foreach ($base in @(${env:ProgramFiles(x86)}, $env:ProgramFiles, (Join-Path $env:LOCALAPPDATA 'Programs'))) {
        if ($base) { $candidates += (Join-Path $base 'Inno Setup 6\ISCC.exe') }
    }
    foreach ($key in @('HKLM:\SOFTWARE\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall\Inno Setup 6_is1',
                       'HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\Inno Setup 6_is1',
                       'HKCU:\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\Inno Setup 6_is1')) {
        try {
            $loc = (Get-ItemProperty -Path $key -ErrorAction Stop).InstallLocation
            if ($loc) { $candidates += (Join-Path $loc 'ISCC.exe') }
        } catch { }
    }
    foreach ($c in $candidates) { if ($c -and (Test-Path $c)) { return $c } }
    return $null
}

# ---- 1. Inno Setup ----------------------------------------------------------------------------------------------------
$iscc = Find-Iscc
if (-not $iscc) {
    if ($NoWinget) { throw 'Inno Setup 6 is not installed (and -NoWinget was given). Install it from https://jrsoftware.org/isdl.php and run again.' }
    if (-not (Get-Command winget.exe -ErrorAction SilentlyContinue)) { throw 'Inno Setup 6 is not installed and winget is not available. Install Inno Setup 6 from https://jrsoftware.org/isdl.php and run again.' }
    Say 'Inno Setup 6 is not installed; installing it through winget (a UAC prompt may appear)'
    & winget.exe install --id JRSoftware.InnoSetup --exact --silent --accept-package-agreements --accept-source-agreements
    $iscc = Find-Iscc
    if (-not $iscc) { throw 'winget finished but ISCC.exe was not found. Install Inno Setup 6 from https://jrsoftware.org/isdl.php and run again.' }
}
# The version: ISCC.exe's own file version first; some installs (the GitHub runner image) ship an ISCC.exe without a
# version block (0.0.0.0), then the uninstall key's DisplayVersion or ISCmplr.dll next to it. Unknown = continue.
function Get-IsccVersion([string]$exe) {
    $candidates = @((Get-Item $exe).VersionInfo.FileVersion, (Get-Item $exe).VersionInfo.ProductVersion)
    foreach ($key in @('HKLM:\SOFTWARE\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall\Inno Setup 6_is1',
                       'HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\Inno Setup 6_is1',
                       'HKCU:\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\Inno Setup 6_is1')) {
        try { $candidates += (Get-ItemProperty -Path $key -ErrorAction Stop).DisplayVersion } catch { }
    }
    $dll = Join-Path (Split-Path -Parent $exe) 'ISCmplr.dll'
    if (Test-Path $dll) { $candidates += (Get-Item $dll).VersionInfo.FileVersion }
    foreach ($c in $candidates) {
        if (-not $c) { continue }
        $clean = ($c -replace '[^0-9.].*$', '')
        try { $v = [Version]$clean } catch { continue }
        if ($v.Major -gt 0) { return $v }
    }
    return $null
}
$v = Get-IsccVersion $iscc
if ($v) {
    Say "Inno Setup $v at $iscc"
    if ($v -lt [Version]'6.3.0') { throw "Inno Setup $v is too old; 6.3 or newer is needed (winget upgrade --id JRSoftware.InnoSetup)." }
} else {
    Say "Inno Setup at $iscc (version unknown: no version block, no uninstall key; continuing)"
}

# ---- 2. the art ------------------------------------------------------------------------------------------------------
$art = $null
foreach ($dir in @((Join-Path $here '..\..\brand\icons'), (Join-Path $here 'art'))) {
    if (Test-Path (Join-Path $dir 'igneum.ico')) { $art = (Resolve-Path $dir).Path; break }
}
if (-not $art) { throw "No art folder: expected brand\icons two levels up (the repo layout) or an art\ folder next to this script with igneum.ico, inno-wizard-164x314.bmp and inno-wizard-small-55x58.bmp (made by brand\icons\make-icons.py)." }
foreach ($f in @('igneum.ico', 'inno-wizard-164x314.bmp', 'inno-wizard-small-55x58.bmp')) {
    if (-not (Test-Path (Join-Path $art $f))) { throw "missing $f in $art" }
}
Say "art: $art"

# ---- 3. the payload --------------------------------------------------------------------------------------------------
function Test-PayloadDir([string]$dir) {
    if (-not $dir -or -not (Test-Path $dir)) { return $false }
    # Ember's payload (make-payload-ember.sh): the engine, the CLI, the CUDA worker and the stop script; the old
    # base's node and miner (igneumd.exe, igneum-miner.exe) are not shipped
    foreach ($f in @('igneum-app.exe', 'ember-cli.exe', 'igneum-worker-cuda.exe', 'stop-igneum.ps1')) {
        if (-not (Test-Path (Join-Path $dir $f))) { return $false }
    }
    return $true
}
$productName = $(if ($Product -eq 'lab') { 'Ember Lab' } else { 'Ember' })
$payloadName = $(if ($Product -eq 'lab') { 'ember-windows-app-lab' } else { 'ember-windows-app' })
Say "product: $Product ($productName; payload folder $payloadName)"
$source = $null
if ($Payload) {
    if (-not (Test-PayloadDir $Payload)) { throw "-Payload $Payload does not hold igneum-app.exe, ember-cli.exe, igneum-worker-cuda.exe and stop-igneum.ps1 (make-payload-ember.sh stages them)." }
    $source = (Resolve-Path $Payload).Path
} else {
    foreach ($dir in @((Join-Path $here $payloadName), (Join-Path $here "..\$payloadName"), (Join-Path $here 'payload'), (Join-Path $here "..\..\$payloadName"))) {
        if (Test-PayloadDir $dir) { $source = (Resolve-Path $dir).Path; break }
    }
}
if (-not $source) {
    $zip = Join-Path $build 'igneum-windows.zip'
    $unpacked = Join-Path $build 'payload-src'
    Say "no igneum-windows folder next to this script; downloading $PayloadUrl"
    Invoke-WebRequest -Uri $PayloadUrl -OutFile $zip -UseBasicParsing
    if (Test-Path $unpacked) { Remove-Item -Recurse -Force $unpacked }
    Expand-Archive -Path $zip -DestinationPath $unpacked -Force
    $hit = Get-ChildItem -Path $unpacked -Recurse -Filter 'igneum-app.exe' | Select-Object -First 1
    if (-not $hit -or -not (Test-PayloadDir $hit.DirectoryName)) { throw "the zip from $PayloadUrl holds no complete payload (igneum-app.exe, ember-cli.exe, igneum-worker-cuda.exe, stop-igneum.ps1)." }
    $source = $hit.DirectoryName
}
Say "payload source: $source"

# ---- 4. assemble build\payload ---------------------------------------------------------------------------------------
$payloadDir = Join-Path $build 'payload'
if (Test-Path $payloadDir) { Remove-Item -Recurse -Force $payloadDir }
New-Item -ItemType Directory -Force -Path $payloadDir | Out-Null
$skipNames = @('.DS_Store')
Get-ChildItem -Path $source -Recurse -Force | ForEach-Object {
    $rel = $_.FullName.Substring($source.Length).TrimStart('\')
    if ($rel -match '(^|\\)packs(\\|$)') { return }
    if ($rel -match '\.(log|seeds)$') { return }
    if ($rel -match '^proto-(cuda|opencl)\\[^\\]+\.exe$') { return }   # GPU workers built per PC by the fallback path
    if ($rel -match '^app\\windows\\(build|dist)(\\|$)') { return }   # BUILD-APP.bat's intermediate files
    if ($skipNames -contains $_.Name) { return }
    $dest = Join-Path $payloadDir $rel
    if ($_.PSIsContainer) { New-Item -ItemType Directory -Force -Path $dest | Out-Null }
    else {
        New-Item -ItemType Directory -Force -Path (Split-Path -Parent $dest) | Out-Null
        Copy-Item -Path $_.FullName -Destination $dest -Force
    }
}
$count = (Get-ChildItem -Path $payloadDir -Recurse -File | Measure-Object).Count
$bytes = (Get-ChildItem -Path $payloadDir -Recurse -File | Measure-Object -Property Length -Sum).Sum
Say ("payload: {0} files, {1:N1} MB" -f $count, ($bytes / 1MB))

# ---- 5. icon and version block in the exes ---------------------------------------------------------------------------
$rcedit = $null
foreach ($exeName in @('igneum-app.exe', 'ember-cli.exe', 'igneum-worker-cuda.exe')) {
    $exe = Join-Path $payloadDir $exeName
    if (-not (Test-Path $exe)) { continue }
    $info = (Get-Item $exe).VersionInfo
    if ($info.ProductName -eq $productName) { Say "$exeName already carries the $productName version block (relinked on the Mac)"; continue }
    if ($NoRcedit) { Say "$exeName has no version block; -NoRcedit given, so Explorer shows it without the coin icon"; continue }
    if (-not $rcedit) {
        if ($env:RCEDIT -and (Test-Path $env:RCEDIT)) { $rcedit = $env:RCEDIT }
        else {
            $rcedit = Join-Path $build 'rcedit-x64.exe'
            if (-not (Test-Path $rcedit)) { Say "downloading rcedit from $rceditUrl"; Invoke-WebRequest -Uri $rceditUrl -OutFile $rcedit -UseBasicParsing }
        }
    }
    $desc = switch ($exeName) { 'ember-cli.exe' { "$productName command line" } 'igneum-worker-cuda.exe' { "$productName CUDA worker" } 'igneum-app.exe' { "$productName engine" } default { $productName } }
    $rcArgs = @($exe, '--set-icon', (Join-Path $art 'igneum.ico'),
        '--set-version-string', 'CompanyName', 'Igneum',
        '--set-version-string', 'ProductName', $productName,
        '--set-version-string', 'FileDescription', $desc,
        '--set-version-string', 'OriginalFilename', $exeName,
        '--set-version-string', 'LegalCopyright', 'Igneum. Nothing is bought or sold.',
        '--set-file-version', $Version,
        '--set-product-version', $Version)
    & $rcedit @rcArgs
    if ($LASTEXITCODE -ne 0) { throw "rcedit failed on $exeName (exit $LASTEXITCODE)" }
    Say "$exeName stamped: coin icon, Ember $Version"
}

# ---- 6. compile ------------------------------------------------------------------------------------------------------
$iss = Join-Path $here 'Ember.iss'
# docs/release/ORG-SIGNING.md sections 5 and 6 (the release team's order of 14:45 UK, 15 October 2026): the custody file
# infra/release/keys/PLATFORM-SIGNING names the Windows code-signing identity by its SHA-1 thumbprint ("windows thumbprint
# <sha1 hex> <subject...> <custody host> <from utc>"); the key lives on the founder's token. With a line: every engine and
# worker in the payload is signed by the thumbprint with a timestamp before packing and verified, the compiler's sign tool
# signs the Setup and its uninstaller, and the Setup is verified; a host without the token fails the signing step by name
# and no Setup is written (the absent-artefact clause, never an unsigned Setup under a named thumbprint). Without a line:
# the unsigned build of before.
$signFile = Join-Path $here '..\..\..\..\infra\release\keys\PLATFORM-SIGNING'
$thumb = ''; $subject = ''
if (Test-Path $signFile) {
    $line = Get-Content $signFile | Where-Object { $_ -match '^windows\s+thumbprint\s+([0-9A-Fa-f]{40})\s+(.*)$' } | Select-Object -Last 1
    if ($line) { $null = $line -match '^windows\s+thumbprint\s+([0-9A-Fa-f]{40})\s+(.*)$'; $thumb = $Matches[1].ToUpper(); $subject = $Matches[2] }
}
$signingJson = 'null'
$tsa = 'http://timestamp.digicert.com'
if ($thumb) {
    Say "signing by the thumbprint named in PLATFORM-SIGNING ($thumb; $subject)"
    $signtool = Get-ChildItem -Path "${env:ProgramFiles(x86)}\Windows Kits\10\bin" -Recurse -Filter signtool.exe -ErrorAction SilentlyContinue | Where-Object { $_.FullName -match '\\x64\\' } | Sort-Object FullName -Descending | Select-Object -First 1
    if (-not $signtool) { throw "signtool.exe is not on this host (the Windows SDK): the signing step cannot run and no Setup is built" }
    $cert = Get-ChildItem -Path Cert:\CurrentUser\My, Cert:\LocalMachine\My -ErrorAction SilentlyContinue | Where-Object { $_.Thumbprint -eq $thumb } | Select-Object -First 1
    if (-not $cert) { throw "the signing certificate $thumb is not on this host (the token is not present): the signing step cannot run and no Setup is built" }
    $signed = @()
    Get-ChildItem -Path $payloadDir -Recurse -Include *.exe, *.dll | ForEach-Object {
        & $signtool.FullName sign /fd SHA256 /td SHA256 /tr $tsa /sha1 $thumb /d "Ember" $_.FullName | Out-Null
        if ($LASTEXITCODE -ne 0) { throw "signing $($_.Name) failed (signtool exit $LASTEXITCODE): no Setup is built" }
        & $signtool.FullName verify /pa /q $_.FullName | Out-Null
        if ($LASTEXITCODE -ne 0) { throw "the signature on $($_.Name) does not verify: no Setup is built" }
        $signed += $_.FullName.Substring($payloadDir.Length).TrimStart('\')
    }
    Say ("signed and verified {0} binaries in the payload" -f $signed.Count)
    $signCmd = '"' + $signtool.FullName + '" sign /fd SHA256 /td SHA256 /tr ' + $tsa + ' /sha1 ' + $thumb + ' /d "Ember" $p'
}
# not /Qp: a preprocessing error must name its line in the job's stored output (job 06 on the second Windows machine read "exit 2" and nothing else)
$isccArgs = @("/DPayload=$payloadDir", "/DArtDir=$art", "/DAppVersion=$Version", "/DProduct=$Product", "/O$dist", $iss)
if ($thumb) { $isccArgs = @("/DSign", "/Signeumsign=$signCmd") + $isccArgs }
Say "compiling $iss"
& $iscc @isccArgs
if ($LASTEXITCODE -ne 0) { throw "ISCC failed (exit $LASTEXITCODE)" }
$out = Join-Path $dist $(if ($Product -eq 'lab') { "Ember-Lab-Setup-$Version.exe" } else { "Ember-Setup-$Version.exe" })   # the lab product's SetupBase (Ember.iss); the public name here failed every lab build with "missing" (9 Oct 2026)
if (-not (Test-Path $out)) { throw "ISCC finished but $out is missing" }
$size = (Get-Item $out).Length
if ($thumb) {
    & $signtool.FullName verify /pa /v $out | Out-Null
    if ($LASTEXITCODE -ne 0) { Remove-Item -Force $out; throw "the signature on the Setup does not verify: the Setup is removed" }
    $signingJson = (@{ subject = $subject; thumbprint = $thumb; timestamp_authority = $tsa; setup_verify = 'ok'; signed_inside = $signed } | ConvertTo-Json -Compress)
    Set-Content -Path "$out.signing.json" -Value $signingJson -Encoding ASCII
    Say ("done: {0} ({1:N1} MB), signed by {2} and verified; the record beside it in {3}" -f $out, ($size / 1MB), $subject, (Split-Path -Leaf "$out.signing.json"))
} else {
    Say ("done: {0} ({1:N1} MB)" -f $out, ($size / 1MB))
    Say 'The installer is unsigned until Igneum has a code-signing certificate: SmartScreen shows "Windows protected your PC" (More info > Run anyway).'
}
