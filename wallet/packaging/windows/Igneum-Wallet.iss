; Igneum Wallet installer for Windows (8 October 2026: the miner's detach-safe shape of release-0.0.25, install-close-23 and install-detach-25, with the wallet's names: the payload's own stop script with -Install, the install marker, the one-shot detached task under the app's job runner, the version-named file refusal)
; Igneum Wallet installer for Windows, Inno Setup 6.3 or newer. A copy of Igneum-Miner.iss with the names, the AppId and
; the payload changed: the wallet engine (igneum-wallet.exe), "Igneum Wallet.exe" (the window host when
; BUILD-WALLET-APP.bat made it), igneum-node.exe (started only when the miner's node is not running). Untested on a PC at
; the time of writing (4 October 2026): the wallet's Windows payload script is the follow-up.
#ifndef Payload
  #define Payload "igneum-windows-wallet"
#endif
#ifndef AppVersion
  #define AppVersion "0.0.1"
#endif
#define AppName "Igneum Wallet"
#define Publisher "Igneum"
#define Url "https://igneum.network"

[Setup]
; the organisation's code signing (docs/release/ORG-SIGNING.md section 5): the build script passes /DSign=1 only when
; infra/release/keys/PLATFORM-SIGNING names a Windows thumbprint and the token is on this host; then the Setup and its
; uninstaller are signed by the compiler's "igneumsign" tool (signtool by that thumbprint with a timestamp)
#ifdef Sign
SignTool=igneumsign $f
SignedUninstaller=yes
#endif
AppId={{B7D2E1F3-7C9E-4F8B-A042-3D6E9F8C0B12}
AppName={#AppName}
AppVersion={#AppVersion}
AppVerName={#AppName} {#AppVersion}
AppPublisher={#Publisher}
AppPublisherURL={#Url}
AppSupportURL={#Url}
AppUpdatesURL={#Url}
AppCopyright=Igneum. A dev chain.
VersionInfoVersion={#AppVersion}.0
VersionInfoCompany={#Publisher}
VersionInfoProductName={#AppName}
VersionInfoDescription={#AppName} Setup
DefaultDirName={localappdata}\Programs\{#AppName}
DefaultGroupName={#AppName}
DisableProgramGroupPage=yes
; no LicenseFile: the restart's payload carries none (the fourth run on the second Windows machine stopped on the licence line, 11 October
; 2026). The wizard images are the design lead's (site/design/icons/wallet, main ef7e6f59: the mark on obsidian),
; read from the tree beside the payload's icon when the build runs in the checkout; a build without them (the
; payload alone) takes Inno's own images
OutputDir=dist
OutputBaseFilename=Igneum-Wallet-Setup-{#AppVersion}
SetupIconFile={#Payload}\icon.ico
UninstallDisplayIcon={app}\icon.ico
UninstallDisplayName={#AppName}
WizardStyle=modern
#ifexist "..\..\..\site\design\icons\wallet\inno-wizard-164x314.bmp"
WizardImageFile=..\..\..\site\design\icons\wallet\inno-wizard-164x314.bmp
WizardSmallImageFile=..\..\..\site\design\icons\wallet\inno-wizard-small-55x58.bmp
#endif
Compression=lzma2/max
SolidCompression=yes
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
PrivilegesRequired=lowest
MinVersion=10.0
CloseApplications=yes
RestartApplications=no

[Languages]
Name: "english"; MessagesFile: "compiler:Default.isl"

[Messages]
english.WelcomeLabel2=This will install [name/ver] on your computer.%n%nYour key is made on this PC and kept encrypted with a password you choose. A dev chain. on this network.
english.FinishedHeadingLabel=Igneum Wallet is installed

[Tasks]
Name: "desktopicon"; Description: "{cm:CreateDesktopIcon}"; GroupDescription: "{cm:AdditionalIcons}"

; The payload on the restart's engine (OLD-BASE.md step 10, 11 October 2026): igneum-wallet-engine.exe, igneum-signer.exe,
; igneum-wallet.exe (the CLI), ui\ (the page), icon.ico (the wallet's icon set), "Igneum Wallet.exe" (the WebView2 host
; built on the PC by BUILD-WALLET-APP.bat), igneum-wallet.cmd (the launcher without a host), stop-igneum-wallet.ps1.
[Files]
Source: "{#Payload}\*"; DestDir: "{app}"; Flags: recursesubdirs createallsubdirs ignoreversion; Excludes: "*.log,*.DS_Store,build\*,dist\*"

[Icons]
Name: "{group}\Igneum Wallet"; Filename: "{app}\Igneum Wallet.exe"; WorkingDir: "{app}"; IconFilename: "{app}\icon.ico"; Comment: "Open your Igneum wallet"
Name: "{group}\Uninstall Igneum Wallet"; Filename: "{uninstallexe}"; IconFilename: "{app}\icon.ico"
Name: "{autodesktop}\Igneum Wallet"; Filename: "{app}\Igneum Wallet.exe"; WorkingDir: "{app}"; IconFilename: "{app}\icon.ico"; Tasks: desktopicon

[Run]
Filename: "{app}\Igneum Wallet.exe"; Description: "Open Igneum Wallet now"; Flags: postinstall nowait skipifsilent runasoriginaluser
; the over-the-air apply (the engine starts this installer /VERYSILENT /IGNOTA=1 and is closed by it): the app comes back when the
; files are in. Found 12 October 2026 22:01 UK on the first Windows machine under test: the 0.0.5 updater took 0.0.7 and the app stayed closed, because the
; line above skips a silent install and OtaRelaunch was never used.
Filename: "{app}\Igneum Wallet.exe"; Flags: nowait runasoriginaluser; Check: OtaRelaunch

[UninstallRun]
Filename: "powershell.exe"; Parameters: "-NoProfile -ExecutionPolicy Bypass -File ""{app}\stop-igneum-wallet.ps1"""; Flags: runhidden waituntilterminated; RunOnceId: "StopIgneumWallet"

[UninstallDelete]
; The WebView2 cache is not in the install log; remove it with the folder. The vault in %LOCALAPPDATA%\igneum\wallet stays.
Type: filesandordirs; Name: "{app}"

[Code]
// /IGNOTA=1: the app's own updater (before 0.0.22) started this install; relaunch the app when the files are in.
// /IGNOTA=2: the 0.0.22 helper started it and relaunches the app itself (src/ota.rs WIN_HELPER): no relaunch here.
function OtaRelaunch: Boolean;
begin
  Result := ExpandConstant('{param:IGNOTA|0}') = '1';
end;

// The 0.0.2-and-earlier install in Program Files (administrator), when this per-user install lands on top of it.
function OldAdminInstallDir: String;
begin
  Result := ExpandConstant('{commonpf}\Igneum Wallet');
  if not FileExists(Result + '\igneum-wallet-engine.exe') then
    Result := '';
end;

// Stops a running copy before the files are replaced (an upgrade over a running miner): the old copy's own
// stop-igneum-wallet.ps1 when one is installed (Program Files or here), else ours from the payload. Then, when someone is at
// the keyboard, offers to remove the Program Files copy (its uninstaller needs one administrator prompt; the data in
// %LOCALAPPDATA%\igneum is the same for both, nothing to copy). A silent (over-the-air) install never asks.
// The install marker (0.0.23): <LOCALAPPDATA>\igneum\app\install-running.flag while this installer works; the engine's
// update helper and the window host never relaunch the app while it is there (the wallet's updater and wallet-host.cpp read the same marker).
function MarkerPath: String;
begin
  Result := ExpandConstant('{localappdata}\igneum\wallet\install-running.flag');
end;

procedure SetMarker;
begin
  ForceDirectories(ExtractFileDir(MarkerPath));
  SaveStringToFile(MarkerPath, GetDateTimeString('yyyy/mm/dd hh:nn:ss', '-', ':') + ' ' + '{#AppVersion}', False);
end;

procedure ClearMarker;
begin
  DeleteFile(MarkerPath);
end;

// An installer started by the app's own job runner is a CHILD of the engine; the stop step then quits the engine, and
// the engine ends the job's whole process tree on its way out (src/jobrun.rs kill_tree: taskkill /T), the installer with
// it, between PrepareToInstall and the copy (the second Windows machine, 0.0.24 take 2, 8 October 2026, 09:24Z: the host gone, no file copied,
// install-running.flag left behind, no ssDone and no DeinitializeSetup). So under a job (IGNEUM_JOB_ID set) this installer
// copies itself to the app's updates folder and runs that copy DETACHED through a one-shot scheduled task (a child of
// the Task Scheduler, outside the job's tree), with the same switches plus /IGDETACHED=1 and its own log, then exits
// without touching the app; the detached run installs and removes the task at its end. The job's proof is the --version
// wait and the detached log, not this process's exit code (1: "Setup failed to initialize").
const
  DetachTask = 'Igneum Wallet install';

function DetachedRun: Boolean;
begin
  Result := ExpandConstant('{param:IGDETACHED|0}') = '1';
end;

function UnderAppJob: Boolean;
begin
  Result := (GetEnv('IGNEUM_JOB_ID') <> '') and (not DetachedRun);
end;

function DetachLogPath: String;
begin
  Result := ExpandConstant('{localappdata}\igneum\wallet\updates\install-{#AppVersion}-detached.log');
end;

// Copies this installer beside the app's data and starts the copy through a one-shot task. True when the task started.
function RelaunchDetached: Boolean;
var
  Dir, Copy, Args, Cmd: String;
  ResultCode: Integer;
begin
  Result := False;
  Dir := ExpandConstant('{localappdata}\igneum\wallet\updates');
  ForceDirectories(Dir);
  Copy := Dir + '\Igneum-Wallet-Setup-{#AppVersion}.exe';
  if not FileCopy(ExpandConstant('{srcexe}'), Copy, False) then
  begin
    Log('detach: could not copy ' + ExpandConstant('{srcexe}') + ' to ' + Copy);
    exit;
  end;
  Args := '/VERYSILENT /NORESTART /SUPPRESSMSGBOXES /IGNOTA=' + ExpandConstant('{param:IGNOTA|0}') + ' /IGDETACHED=1 /LOG="' + DetachLogPath + '"';
  Cmd := '/Create /F /SC ONCE /ST 23:59 /RL LIMITED /TN "' + DetachTask + '" /TR "\"' + Copy + '\" ' + Args + '"';
  if not Exec('schtasks.exe', Cmd, '', SW_HIDE, ewWaitUntilTerminated, ResultCode) or (ResultCode <> 0) then
  begin
    Log('detach: schtasks /Create returned ' + IntToStr(ResultCode));
    exit;
  end;
  if not Exec('schtasks.exe', '/Run /TN "' + DetachTask + '"', '', SW_HIDE, ewWaitUntilTerminated, ResultCode) or (ResultCode <> 0) then
  begin
    Log('detach: schtasks /Run returned ' + IntToStr(ResultCode));
    Exec('schtasks.exe', '/Delete /F /TN "' + DetachTask + '"', '', SW_HIDE, ewWaitUntilTerminated, ResultCode);
    exit;
  end;
  Log('detach: this installer runs under the app''s job ' + GetEnv('IGNEUM_JOB_ID') + '; the copy at ' + Copy + ' installs detached, log ' + DetachLogPath);
  Result := True;
end;

procedure RemoveDetachTask;
var
  ResultCode: Integer;
begin
  if DetachedRun then
    Exec('schtasks.exe', '/Delete /F /TN "' + DetachTask + '"', '', SW_HIDE, ewWaitUntilTerminated, ResultCode);
end;

procedure CurStepChanged(CurStep: TSetupStep);
begin
  if CurStep = ssDone then
  begin
    ClearMarker;
    RemoveDetachTask;
  end;
end;

// Two refusals before anything runs (0.0.23 take 1, the second Windows machine, 22:07 UK): the installer's file name must carry ITS version
// (a 0.0.21 kit once shipped a 0.0.22.0 host; a job that passes -Version reads this name), and a marker an earlier
// installer left behind (older than 15 min: it died) is cleared with a line, so nothing holds on it.

function InitializeSetup: Boolean;
var
  Name: String;
begin
  Result := True;
  if UnderAppJob then
  begin
    // the detached copy does the install; this process ends here, before any marker or stop step, so the job's tree
    // teardown finds nothing of the install to kill
    Result := not RelaunchDetached;
    if not Result then exit;
    Log('detach: the detached start failed; installing in place under the job (the stop step will end this process with the engine)');
  end;
  Name := ExtractFileName(ExpandConstant('{srcexe}'));
  if (Pos('Setup-', Name) > 0) and (Pos('Setup-{#AppVersion}', Name) = 0) then
  begin
    Log('REFUSED: this installer is {#AppVersion} but its file is named ' + Name + ' (the name must carry the version it installs)');
    if not WizardSilent then
      MsgBox('This installer is version {#AppVersion} but its file is named ' + Name + '. Download the installer again.', mbError, MB_OK);
    Result := False;
    exit;
  end;
  if FileExists(MarkerPath) then
  begin
    Log('a stale install marker was found at ' + MarkerPath + ' (an earlier installer did not finish); replaced by this run');
    ClearMarker;
  end;
end;

procedure DeinitializeSetup;
begin
  ClearMarker;
end;

function PrepareToInstall(var NeedsRestart: Boolean): String;
var
  StopScript, OldDir: String;
  ResultCode: Integer;
begin
  Result := '';
  SetMarker;
  OldDir := OldAdminInstallDir;
  // Always THIS installer's stop step, never the installed one: the second Windows machine, 0.0.23 take 1 (7 October 2026, 22:07 UK) ran the
  // installed 0.0.21 script, which only asked the engine to quit; the window host lived on, Inno could not replace it,
  // and the host started the old engine again 45 s later. The payload's script ends the host first, by its path under
  // the install dir it is given, then the engine, then waits for every file to unlock (tools/ci/installer-stop-check.sh).
  ExtractTemporaryFile('stop-igneum-wallet.ps1');
  StopScript := ExpandConstant('{tmp}\stop-igneum-wallet.ps1');
  Log('stop step: ' + StopScript + ' -Install "' + ExpandConstant('{app}') + '"');
  if Exec('powershell.exe', '-NoProfile -ExecutionPolicy Bypass -File "' + StopScript + '" -Install "' + ExpandConstant('{app}') + '"', '', SW_HIDE, ewWaitUntilTerminated, ResultCode) then
    Log('stop step: exit ' + IntToStr(ResultCode) + ' (0 = every file of ours is free; 3 = a file stayed locked)')
  else
    Log('stop step: powershell did not start (' + SysErrorMessage(ResultCode) + '); CloseApplications is the fallback');
  // a file of ours still locked after the stop step (a window from an earlier run at a level this installer cannot
  // close: the second Windows machine, 14 to 16 October 2026, every hourly try ending with code 5): the install
  // stops here with the words, Setup's exit 7, instead of a file-in-use abort with code 5 and no words
  if ResultCode = 3 then
  begin
    Result := 'A previous Igneum Wallet is still open and holds its files (a window from an earlier run that this installer cannot close). Close that window and run the update again; the wallet''s own updater applies it on its next check.';
    Log('stop step: ' + Result);
    exit;
  end;
  if (OldDir <> '') and FileExists(OldDir + '\stop-igneum-wallet.ps1') then
    Exec('powershell.exe', '-NoProfile -ExecutionPolicy Bypass -File "' + StopScript + '" -Install "' + OldDir + '"', '', SW_HIDE, ewWaitUntilTerminated, ResultCode);
  if (OldDir <> '') and (not WizardSilent) and FileExists(OldDir + '\unins000.exe') then
  begin
    if MsgBox('Igneum Wallet now installs in your user folder, so updates need no administrator prompt.' + #13#10#13#10 +
              'An older copy is still in ' + OldDir + '. Remove it now? (one administrator prompt; your chain data, address and settings stay)',
              mbConfirmation, MB_YESNO) = IDYES then
      ShellExec('runas', OldDir + '\unins000.exe', '/VERYSILENT /SUPPRESSMSGBOXES /NORESTART', '', SW_HIDE, ewWaitUntilTerminated, ResultCode);
  end;
end;

procedure CurPageChanged(CurPageID: Integer);
begin
  if CurPageID = wpFinished then
    WizardForm.FinishedLabel.Caption := WizardForm.FinishedLabel.Caption + #13#10#13#10 +
      'Nobody from Igneum will ever ask for your words or your key.';
end;
