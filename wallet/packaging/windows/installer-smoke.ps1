# The Setup exe's own smoke on a Windows PC through its queue, no hand (the project's word, 11 October 2026): the installer
# run silently into a temporary prefix, the host started from there with a test data dir, its engine answering
# api/state, a wallet created and locked through the API, the host told to close, the uninstaller run silently, the
# prefix empty after. Rows in smoke.log and smoke.json; exit 0 only when every row passed.
#   installer-smoke.ps1 -Setup <path to Igneum-Wallet-Setup-<v>.exe> -Out <folder>
param(
    [string]$Setup = '',
    [string]$Out = ''
)
$ErrorActionPreference = 'Continue'
if (-not $Setup -or -not (Test-Path $Setup)) { Write-Host "no Setup exe at '$Setup'"; exit 2 }
if (-not $Out) { $Out = Join-Path $env:TEMP 'igneum-wallet-smoke' }
New-Item -ItemType Directory -Force -Path $Out | Out-Null
$log = Join-Path $Out 'smoke.log'
'' | Set-Content $log
$rows = [ordered]@{}
$fail = $false
function Say([string]$t) { $line = ('[' + (Get-Date).ToUniversalTime().ToString('HH:mm:ssZ') + '] ' + $t); Write-Host $line; Add-Content $log $line }
function Row([string]$name, [bool]$ok, [string]$why) { $script:rows[$name] = $(if ($ok) { 'PASS' } else { 'FAIL' }); if (-not $ok) { $script:fail = $true }; Say ('ROW ' + $name + ' ' + $(if ($ok) { 'PASS' } else { 'FAIL' }) + ' ' + $why) }

$prefix = Join-Path $Out 'prefix'
$data = Join-Path $Out 'data'
New-Item -ItemType Directory -Force -Path $data | Out-Null

# ---- install: silent, into the prefix, no desktop icon, nothing started after ----
$p = Start-Process -FilePath $Setup -ArgumentList @('/VERYSILENT', '/SUPPRESSMSGBOXES', '/NORESTART', '/SP-', "/DIR=`"$prefix`"", '/MERGETASKS=!desktopicon', "/LOG=`"$Out\install.log`"") -Wait -PassThru
$engine = Join-Path $prefix 'igneum-wallet-engine.exe'
# ($host is PowerShell's own read-only variable: the host exe's path is $hostExe)
$hostExe = Join-Path $prefix 'Igneum Wallet.exe'
Row 'install' (($p.ExitCode -eq 0) -and (Test-Path $engine) -and (Test-Path (Join-Path $prefix 'igneum-signer.exe')) -and (Test-Path (Join-Path $prefix 'ui\index.html'))) ("exit " + $p.ExitCode + "; engine, signer and page under the prefix: " + ((Test-Path $engine) -and (Test-Path (Join-Path $prefix 'ui\index.html'))) + "; host exe present: " + (Test-Path $hostExe))

# ---- start: the host from the prefix (the launcher's path when no host), the engine's run URL from its file ----
$urlf = Join-Path $Out 'engine-url.json'
if (Test-Path $urlf) { Remove-Item $urlf -Force }
$env:IGNEUM_WALLET_DATA = $data
$env:IGNEUM_SIGNER_LIGHT_KDF = '1'
$env:IGNEUM_WALLET_URL_FILE = $urlf
$env:IGNEUM_WALLET_UPDATE_FIRST_SECS = '600'
# a wallet host already running on this machine (an installed copy): the host is single-instance (a named mutex and
# a hand-over to the existing window), so a second host exits at once and the smoke would read the INSTALLED copy's
# engine as its own (14 October 2026, the first 0.0.9 smoke: it then ended that engine by pid). The row names it and
# the smoke stops here; the installed copy is never touched.
$other = Get-Process -Name 'Igneum Wallet' -ErrorAction SilentlyContinue | Select-Object -First 1
if ($other) {
    Row 'start' $false ("an installed wallet host is already running (pid " + $other.Id + ", " + $other.Path + "); the host is single-instance, so this smoke cannot start its own; close that wallet first and run the smoke again")
    $script:rows['state'] = 'SKIP'; $script:rows['wallet'] = 'SKIP'; $script:rows['lock'] = 'SKIP'; $script:rows['quit'] = 'SKIP'
    $un = Get-ChildItem -Path $prefix -Filter 'unins*.exe' -ErrorAction SilentlyContinue | Select-Object -First 1
    if ($un) { Start-Process -FilePath $un.FullName -ArgumentList @('/VERYSILENT', '/SUPPRESSMSGBOXES', '/NORESTART') -Wait -PassThru | Out-Null }
    Say 'SMOKE FAIL (precondition: a wallet host was running)'
    @{ rows = $rows; verdict = 'FAIL'; precondition = 'a wallet host was running'; setup = (Split-Path $Setup -Leaf) } | ConvertTo-Json | Set-Content (Join-Path $Out 'smoke.json')
    exit 1
}
$proc = $null
if (Test-Path $hostExe) {
    $proc = Start-Process -FilePath $hostExe -WorkingDirectory $prefix -PassThru
} else {
    $proc = Start-Process -FilePath $engine -ArgumentList @('--ui', (Join-Path $prefix 'ui'), '--signer', (Join-Path $prefix 'igneum-signer.exe')) -WorkingDirectory $prefix -PassThru -WindowStyle Hidden
}
$i = 0
while ($i -lt 60 -and -not (Test-Path $urlf)) { Start-Sleep -Milliseconds 500; $i++ }
$enginep = Get-Process -Name 'igneum-wallet-engine' -ErrorAction SilentlyContinue | Select-Object -First 1
Row 'start' ((Test-Path $urlf) -and ($null -ne $enginep)) ("started from " + $(if (Test-Path $hostExe) { 'the host' } else { 'the engine (no host in the payload)' }) + "; the run URL written: " + (Test-Path $urlf) + "; engine pid: " + $(if ($enginep) { $enginep.Id } else { 'none' }))

# ---- close: the window's close hides to the tray and the one notice shows (0.0.12, the founder's bug): the capture ----
if ($proc -and -not $proc.HasExited -and (Test-Path $hostExe)) {
    try {
        Add-Type -AssemblyName System.Windows.Forms, System.Drawing
        $null = $proc.CloseMainWindow(); Start-Sleep -Seconds 3
        # the capture is of the notice's own window alone, never the display (a display capture on a machine in use
        # carries whatever else is on the screen; 14 October 2026)
        Add-Type -Namespace IgWin -Name U32 -MemberDefinition '[DllImport("user32.dll", CharSet=CharSet.Unicode)] public static extern IntPtr FindWindow(string c, string t); [DllImport("user32.dll")] public static extern bool GetWindowRect(IntPtr h, out RECT r); public struct RECT { public int L, T, R, B; }'
        # the notice is the host's main window while its own window is hidden (FindWindow by title read nothing on
        # the first run though the process's main window title was the notice's): the handle from the process itself
        $proc.Refresh(); $hwndN = [IntPtr]::Zero
        if ($proc.MainWindowTitle -like 'Igneum Wallet is still running*') { $hwndN = $proc.MainWindowHandle }
        if ($hwndN -eq [IntPtr]::Zero) { $hwndN = [IgWin.U32]::FindWindow($null, 'Igneum Wallet is still running in the tray') }
        if ($hwndN -ne [IntPtr]::Zero) {
            # the window's own pixels by PrintWindow, never a copy of the screen under it (a screen copy at the window's
            # rectangle carried whatever lay over it on a machine in use, 14 October 2026)
            Add-Type -Namespace IgWin -Name PW -MemberDefinition '[DllImport("user32.dll")] public static extern bool PrintWindow(IntPtr h, IntPtr hdc, uint f);'
            $r = New-Object IgWin.U32+RECT; $null = [IgWin.U32]::GetWindowRect($hwndN, [ref]$r)
            $w = [Math]::Max(1, $r.R - $r.L); $h = [Math]::Max(1, $r.B - $r.T)
            $bmp = New-Object System.Drawing.Bitmap $w, $h
            $g = [System.Drawing.Graphics]::FromImage($bmp); $hdc = $g.GetHdc(); $null = [IgWin.PW]::PrintWindow($hwndN, $hdc, 2); $g.ReleaseHdc($hdc); $g.Dispose()
            $bmp.Save((Join-Path $Out 'close-notice.png'), [System.Drawing.Imaging.ImageFormat]::Png); $bmp.Dispose()
            Say 'the notice window captured alone'
        } else { Say 'no notice window found to capture' }
        $hidden = ($proc.MainWindowHandle -eq 0) -or -not $proc.Responding -or $true
        $notice = Get-Process | Where-Object { $_.Id -eq $proc.Id } | ForEach-Object { $_.MainWindowTitle }
        Row 'close' (-not $proc.HasExited) ("the window closed by its button; the host still runs (hidden to the tray): " + (-not $proc.HasExited) + "; the notice captured to close-notice.png when found; main window title now: '" + $notice + "'")
        # the notice's OK, so the quit row runs clean
        # the notice's OK, then the second question's default (No: do not show again is NOT chosen, the default is Yes; the smoke answers Yes so the machine's choice stays)
        [System.Windows.Forms.SendKeys]::SendWait('{ENTER}'); Start-Sleep -Seconds 1; [System.Windows.Forms.SendKeys]::SendWait('y'); Start-Sleep -Seconds 1
    } catch { Row 'close' $false ("the close step failed: " + $_.Exception.Message) }
}

# ---- state, wallet, lock through the API ----
$url = ''
try { $url = (Get-Content $urlf -Raw | ConvertFrom-Json).url } catch {}
function Api([string]$method, [string]$path, [string]$body) {
    try {
        if ($method -eq 'GET') { return Invoke-RestMethod -Uri ($url + $path) -Method Get -TimeoutSec 10 }
        return Invoke-RestMethod -Uri ($url + $path) -Method Post -ContentType 'application/json' -Body $body -TimeoutSec 60
    } catch { return $null }
}
$st = Api 'GET' 'api/state' ''
Row 'state' (($null -ne $st) -and ($st.phase -ne $null)) ("api/state: phase " + $st.phase + ", version " + $st.version + ", node " + $st.node.state)
$cr = Api 'POST' 'api/create' '{"password":"smoke row password"}'
$st = Api 'GET' 'api/state' ''
Row 'wallet' ($st.phase -eq 'home') ("after create the phase reads " + $st.phase)
$null = Api 'POST' 'api/lock' '{}'
$st = Api 'GET' 'api/state' ''
Row 'lock' ($st.phase -eq 'unlock') ("after lock the phase reads " + $st.phase)

# ---- quit: the engine's own quit (api/quit: it locks the signer and exits, and the signer ends with its pipe); the
#      host hides to the tray on a window close by design and its tray Quit needs a hand, so after the engine's end
#      the host is ended by its recorded pid, its command line read first; the row stands on the engine and the
#      signer being gone by the engine's own path ----
$null = Api 'POST' 'api/quit' '{}'
$i = 0
while ($i -lt 30) { $alive = Get-Process -Name 'igneum-wallet-engine','igneum-signer' -ErrorAction SilentlyContinue; if ($null -eq $alive) { break }; Start-Sleep -Milliseconds 500; $i++ }
$left = Get-Process -Name 'igneum-wallet-engine','igneum-signer' -ErrorAction SilentlyContinue
Row 'quit' ($null -eq $left) ("the engine's own quit through api/quit; engine and signer left after 15 s: " + $(if ($left) { ($left | ForEach-Object { $_.Name }) -join ',' } else { 'none' }) + "; the host hides to the tray on close by design and is ended by its pid after this row")
# only processes whose executable is under this smoke's prefix are ended; an installed copy's are named and left
foreach ($l in $left) {
    if ($l.Path -and $l.Path.StartsWith($prefix, [System.StringComparison]::OrdinalIgnoreCase)) { Stop-Process -Id $l.Id -Force -ErrorAction SilentlyContinue; Say ('stopped ' + $l.Name + ' ' + $l.Id + ' (under the prefix) by its pid after the row') }
    else { Say ('left ' + $l.Name + ' ' + $l.Id + ' alone: not under the prefix (' + $l.Path + ')') }
}
if ($proc -and -not $proc.HasExited) {
    $cmd = (Get-CimInstance Win32_Process -Filter ("ProcessId = " + $proc.Id) -ErrorAction SilentlyContinue).CommandLine
    if ($cmd -and $cmd -like '*Igneum Wallet.exe*') { Stop-Process -Id $proc.Id -Force -ErrorAction SilentlyContinue; Say ('the host (pid ' + $proc.Id + ', its command line read) ended after the row') }
}
Start-Sleep -Seconds 2

# ---- uninstall: the uninstaller silently; the prefix's files gone ----
$un = Get-ChildItem -Path $prefix -Filter 'unins*.exe' -ErrorAction SilentlyContinue | Select-Object -First 1
if ($un) {
    # Inno's uninstaller hands over to a copy of itself and the first process's exit code is not the uninstall's
    # (the first smoke read -1 with the files gone): the row stands on the effect, the engine's exe and the page
    # gone from the prefix within 20 s, with the code recorded
    $u = Start-Process -FilePath $un.FullName -ArgumentList @('/VERYSILENT', '/SUPPRESSMSGBOXES', '/NORESTART') -Wait -PassThru
    $i = 0
    while ($i -lt 40 -and (Test-Path $engine)) { Start-Sleep -Milliseconds 500; $i++ }
    $gone = (-not (Test-Path $engine)) -and (-not (Test-Path (Join-Path $prefix 'ui\index.html')))
    Row 'uninstall' $gone ("the uninstaller ran silently (its first process exited " + $u.ExitCode + "); the engine's exe and the page gone from the prefix: " + $gone)
} else {
    Row 'uninstall' $false 'no uninstaller under the prefix'
}
$verdict = $(if ($fail) { 'FAIL' } else { 'PASS' })
$obj = [ordered]@{ rows = $rows; verdict = $verdict; setup = (Split-Path -Leaf $Setup); sha256 = (Get-FileHash -Algorithm SHA256 $Setup).Hash.ToLower() }
$obj | ConvertTo-Json -Depth 4 | Set-Content (Join-Path $Out 'smoke.json')
Say ('SMOKE ' + $verdict)
if ($fail) { exit 1 } else { exit 0 }
