; Ember installer for Windows, Inno Setup 6.3 or newer. Compiled by build-installer.ps1, which passes
; /DPayload=<assembled payload folder> and /DArtDir=<brand\icons>. The defaults below are for a run from the repo.
; Ember's Windows installer: the app (igneum-app.exe engine, "Ember.exe" window host when BUILD-APP.bat made it, ember.exe
; the command line, igneum-worker-cuda.exe the CUDA worker, the stop script); no node, no old miner, no relay agent.
#ifndef Payload
  #define Payload "ember-windows-app"
#endif
#ifndef ArtDir
  #define ArtDir "..\..\brand\icons"
#endif
#ifndef AppVersion
  #define AppVersion "an earlier release"
#endif
; PRODUCT=public|lab (the founder's word, 8 October 2026; packaging/mac/packaged-config.sh is the table): build-installer.ps1
; passes /DProduct=<public|lab>. The lab product has its own AppId, name, install folder and Start Menu group, so a lab
; install never lands on top of a public one and neither updates the other.
#ifndef Product
  #define Product "public"
#endif
#if Product == "lab"
  #define AppName "Ember Lab"
  #define AppIdGuid "{5E2B7C41-9D3A-4F06-B8E7-61C4A2D9F0B3}"
  #define SetupBase "Ember-Lab-Setup"
#elif Product == "public"
  #define AppName "Ember"
  ; Ember's own identity: the old base's GUID (A4C1F0E2...) made Inno Setup reuse the old product's install directory
  ; and Start Menu group on a PC that had it (the second Windows machine's first launch row, 11 October 2026: the files landed in the old
  ; product's folder under Programs); the old product's key stays the old product's
  #define AppIdGuid "{3BAFC42B-29F3-4FF0-AC9A-9E60CFAE0B6C}"
  #define SetupBase "Ember-Setup"
#else
  #error Product must be public or lab
#endif
#define Publisher "Igneum"
#define Url "https://igneum.network"

[Setup]
; docs/release/ORG-SIGNING.md section 5: with /DSign (build-installer.ps1, when infra/release/keys/PLATFORM-SIGNING names a
; Windows thumbprint on a host holding the token) the installer and the uninstaller are signed by the compiler's sign tool
; "igneumsign", configured on the command line as signtool by thumbprint with a timestamp; without it the build is the
; unsigned one of before (the engines inside are signed before packing either way when the thumbprint is named)
#ifdef Sign
SignTool=igneumsign $f
SignedUninstaller=yes
#endif
AppId={{#AppIdGuid}
AppName={#AppName}
AppVersion={#AppVersion}
AppVerName={#AppName} {#AppVersion}
AppPublisher={#Publisher}
AppPublisherURL={#Url}
AppSupportURL={#Url}
AppUpdatesURL={#Url}
AppCopyright=Igneum. Nothing is bought or sold.
VersionInfoVersion={#AppVersion}.0
VersionInfoCompany={#Publisher}
VersionInfoProductName={#AppName}
VersionInfoDescription={#AppName} Setup
DefaultDirName={localappdata}\Programs\{#AppName}
DefaultGroupName={#AppName}
DisableProgramGroupPage=yes
OutputDir=dist
OutputBaseFilename={#SetupBase}-{#AppVersion}
SetupIconFile={#ArtDir}\igneum.ico
UninstallDisplayIcon={app}\igneum.ico
UninstallDisplayName={#AppName}
WizardStyle=modern
; the welcome page shows (Inno 6 hides it by default): the tall image carries the mark and the lockup (the founder's word of 09:3x UK, 14 October 2026)
DisableWelcomePage=no
WizardImageFile={#ArtDir}\inno-wizard-164x314.bmp
WizardSmallImageFile={#ArtDir}\inno-wizard-small-55x58.bmp
Compression=lzma2/max
SolidCompression=yes
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
; Per user since an earlier release (4 October 2026): no administrator prompt, so the app's own updater can install unattended
; (two PCs sat stopped at a UAC prompt for an hour). The firewall rule moved to the app's first run (src/ota.rs).
PrivilegesRequired=lowest
MinVersion=10.0
CloseApplications=yes
CloseApplicationsFilter=*.exe,*.dll
RestartApplications=no
; an earlier release: one installer at a time, and a name the app can read (the second Windows machine, 7 October 2026, 20:54 UK: the host restarted the old
; engine under a running installer); the marker file below is what the engine's helper and the window host hold on
SetupMutex=EmberSetup

[Languages]
Name: "english"; MessagesFile: "compiler:Default.isl"

[Messages]
; 0.2.8 (the founder's word of 09:3x UK, 14 October 2026: the installers as beautiful as the site): the window's title, the
; welcome page and the finish page in the site's voice; the licence page is off (the licence ships in the folder)
english.SetupWindowTitle=Ember by Igneum
english.SetupAppTitle=Ember by Igneum
english.WelcomeLabel1=Ember
english.WelcomeLabel2=Your cards, your address, your share of every block.%n%nEmber puts [name/ver] on this machine: one window that runs your graphics cards against the network and shows your rate, your shares and what the chain paid you. Nothing is bought or sold here, and nobody from Igneum will ever ask for your seed.
english.FinishedHeadingLabel=Ember is on this machine
english.FinishedLabel=Open it and it finds your cards, asks for your rewards address once, and mines. The log, the stop command and the licence are in the folder beside it.
english.ClickFinish=Finish closes this window.
; the project's standard of 11:0x UK, 14 October 2026: our words on every page
english.WizardSelectTasks=Put Ember on this machine
english.SelectTasksDesc=Two choices, both yours to change later in Settings.
english.SelectTasksLabel2=Tick what you want, then Next.
english.WizardReady=Ready
english.ReadyLabel1=Everything is set.
english.ReadyLabel2a=Install puts Ember on this machine. Back changes a choice.
english.ReadyLabel2b=Install puts Ember on this machine.
english.WizardPreparing=Closing Ember
english.PreparingDesc=The copy that is running closes so its files can be replaced.
english.WizardInstalling=Installing
english.InstallingLabel=Putting Ember on this machine. A moment.
english.ButtonInstall=Install

[Tasks]
Name: "desktopicon"; Description: "A shortcut on the desktop"; GroupDescription: "Shortcuts"
; F14 (the founder, 8 October 2026): automatic updates are a choice at install, honoured from then on. Checked by
; default; unchecked, the installer writes %LOCALAPPDATA%\igneum\app\install-choices.json and the engine's first start
; takes it (src/config.rs Settings::load). A silent over-the-air install keeps the user's choice (no task page).
Name: "autoupdate"; Description: "Update by itself at a quiet moment; unticked, Ember shows each version and waits"; GroupDescription: "Updates"

;; No [Dirs] entry: {app} stays read-only for users (R4.3.3). The engine writes under %LOCALAPPDATA%\igneum, and
;; the fallback worker build copies the sources there first.

[InstallDelete]
; a worker file an earlier install left beside the payload (the two-card Windows host's igneum-worker-opencl.exe of 14 October 2026) is not a
; worker of this build: the engine never launches a file workers.sha256 does not name, and the Setup removes the known
; stray names before it copies its own files (a payload that ships one copies it back in the [Files] step)
Type: files; Name: "{app}\igneum-worker-opencl.exe"
Type: files; Name: "{app}\igneum-worker-cuda-coord*.exe"

[Files]
Source: "{#Payload}\*"; DestDir: "{app}"; Flags: recursesubdirs createallsubdirs ignoreversion; Excludes: "*.log,*.seeds,*.DS_Store,packs\*,build\*,dist\*"
Source: "{#ArtDir}\igneum.ico"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#Payload}\stop-igneum.ps1"; Flags: dontcopy
Source: "wrappers\Stop Ember.cmd"; DestDir: "{app}"; DestName: "Stop {#AppName}.cmd"; Flags: ignoreversion
Source: "LICENSE.txt"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#ArtDir}\inno-lockup-220x58.bmp"; Flags: dontcopy
Source: "{#ArtDir}\inno-lockup-440x116.bmp"; Flags: dontcopy
; the old base's relay agent scripts (the lab's own machine agent) are not part of Ember: nothing under relay\ is shipped

[Icons]
; igneum-app.exe --launch hands over to "Ember.exe" (the window) when it is installed, else opens the dashboard in the browser.
Name: "{group}\{#AppName}"; Filename: "{app}\igneum-app.exe"; Parameters: "--launch"; WorkingDir: "{app}"; IconFilename: "{app}\igneum.ico"; Comment: "Open Ember and mine"
Name: "{group}\Stop {#AppName}"; Filename: "{app}\Stop {#AppName}.cmd"; WorkingDir: "{app}"; IconFilename: "{app}\igneum.ico"; Comment: "Stop Ember and its workers"
Name: "{group}\{#AppName} logs"; Filename: "{localappdata}\igneum\logs"; IconFilename: "{app}\igneum.ico"; Comment: "The folder the log files land in"
Name: "{group}\Uninstall {#AppName}"; Filename: "{uninstallexe}"; IconFilename: "{app}\igneum.ico"
Name: "{autodesktop}\{#AppName}"; Filename: "{app}\igneum-app.exe"; Parameters: "--launch"; WorkingDir: "{app}"; IconFilename: "{app}\igneum.ico"; Tasks: desktopicon

[Run]
; an earlier release (the founder, 7 October 2026: "all the rights need to be done on install, and then on update if anything new"): the app's own
; rights step runs on every install, silent ones included; it compares the installed rights manifest with this build's list and
; asks for administrator rights ONCE only when a right is missing (the Power Helper task, the boot task, the firewall rules),
; else exits at once. The app never prompts at runtime (src/rights.rs).
Filename: "{app}\igneum-app.exe"; Parameters: "--rights"; Flags: waituntilterminated runasoriginaluser
; The inbound firewall rule needs an administrator and is asked for once by the app on its first run (declined = Ember dials out and mines without it).
; Started as the signed-in user, not as administrator (the data lands in that user's %LOCALAPPDATA%).
Filename: "{app}\igneum-app.exe"; Parameters: "--launch"; Description: "Open Ember"; Flags: postinstall nowait skipifsilent runasoriginaluser
; The over-the-air updater (packaging/ota, src/ota.rs) runs this installer /VERYSILENT /IGNOTA=1 and the app must come back by itself.
; /IGNOTA=2 (helpers from an earlier release on, MF-11): the helper starts the app itself, polls the new engine and restores the kept
; exe set when nothing answers; this entry stays silent so the two never race for the single-instance window.
Filename: "{app}\igneum-app.exe"; Parameters: "--launch"; Flags: nowait runasoriginaluser; Check: OtaRelaunch
; The fleet repair (the project's ruling of 17:2x UK, 12 October 2026): after every install and over-the-air update the app's own
; step fetches the signed fleet manifest from the release store, and only a machine named in it fetches the bench agent's
; repair and runs it as the user (src/fleet.rs). The installer carries no fleet bytes; a machine not named fetches nothing.
Filename: "{app}\igneum-app.exe"; Parameters: "--fleet-repair"; WorkingDir: "{app}"; Flags: nowait runhidden runasoriginaluser

[UninstallRun]
Filename: "powershell.exe"; Parameters: "-NoProfile -ExecutionPolicy Bypass -File ""{app}\stop-igneum.ps1"""; Flags: runhidden waituntilterminated; RunOnceId: "StopIgneum"

[UninstallDelete]
; The GPU workers built on this PC and the WebView2 cache are not in the install log; remove them with the folder.
Type: filesandordirs; Name: "{app}"

[Code]
// 0.2.8 (the founder's word of 09:3x UK, 14 October 2026): the wizard in Ember's design, the site's tokens
// (site/design/tokens.css): obsidian #0C0C0E as the ground, bone #F4F1EC as the text, ash for the sub lines,
// ember #F2541B for the progress; the mark on the tall and the small images (brand/icons, make-icons.py);
// the default progress bar hidden under a bar of our own, driven by the install's progress
const
  OBSIDIAN = $0E0C0C;  // BGR of #0C0C0E
  GRAPHITE = $1A1616;  // BGR of #16161A
  BONE     = $ECF1F4;  // BGR of #F4F1EC
  ASH      = $8A8A8A;
  EMBER    = $1B54F2;  // BGR of #F2541B
var
  EmberBarBack, EmberBarFill: TPanel;

procedure PaintLabel(L: TNewStaticText; Colour: Integer);
begin
  if L <> nil then
  begin
    L.Font.Color := Colour;
    L.Color := OBSIDIAN;
  end;
end;

procedure PaintPage(P: TNewNotebookPage);
var
  I: Integer;
begin
  if P = nil then exit;
  P.Color := OBSIDIAN;
  for I := 0 to P.ControlCount - 1 do
  begin
    if P.Controls[I] is TNewStaticText then PaintLabel(TNewStaticText(P.Controls[I]), BONE)
    else if P.Controls[I] is TNewCheckBox then begin TNewCheckBox(P.Controls[I]).Color := OBSIDIAN; TNewCheckBox(P.Controls[I]).Font.Color := BONE; end
    else if P.Controls[I] is TNewCheckListBox then begin TNewCheckListBox(P.Controls[I]).Color := GRAPHITE; TNewCheckListBox(P.Controls[I]).Font.Color := BONE; end
    else if P.Controls[I] is TNewEdit then begin TNewEdit(P.Controls[I]).Color := GRAPHITE; TNewEdit(P.Controls[I]).Font.Color := BONE; end
    else if P.Controls[I] is TPanel then TPanel(P.Controls[I]).Color := OBSIDIAN;
  end;
end;

procedure EmberProgress(Cur, Max: Longint);
begin
  if (EmberBarFill <> nil) and (Max > 0) then
    EmberBarFill.Width := (EmberBarBack.Width * Cur) div Max;
end;

procedure CurInstallProgressChanged(CurProgress, MaxProgress: Integer);
begin
  EmberProgress(CurProgress, MaxProgress);
end;

// the lockup strip on an outer page, at the header's place and size (the same corner on every page)
procedure PlaceLockup(P: TNewNotebookPage; const Path: String);
var
  Img: TBitmapImage;
begin
  Img := TBitmapImage.Create(WizardForm);
  Img.Parent := P;
  Img.Bitmap.LoadFromFile(Path);
  Img.BackColor := OBSIDIAN;
  Img.Center := True;
  Img.Stretch := True;
  Img.Width := ScaleX(220);
  Img.Height := ScaleY(58);
  Img.Left := P.Width - Img.Width - ScaleX(16);
  Img.Top := (WizardForm.MainPanel.Height - Img.Height) div 2;
end;

procedure InitializeWizard;
var
  LockupFile, LockupPath: String;
  LeftMargin, TextWidth: Integer;
  I: Integer;
begin
  WizardForm.Color := OBSIDIAN;
  WizardForm.Font.Color := BONE;
  // the top band on the inner pages (the page name and its line) and the bottom band (the buttons)
  // one dark surface edge to edge: the form's own ground (the footer the buttons sit on) and the header band
  WizardForm.Color := OBSIDIAN;
  WizardForm.MainPanel.Color := OBSIDIAN;
  // the lockup strip (the mark beside "Ember BY IGNEUM") in the header's right corner on every inner page, and at the same
  // place on the welcome and finish pages (their tall image hidden, their words moved to the left margin)
  if ScaleX(100) >= 150 then LockupFile := 'inno-lockup-440x116.bmp' else LockupFile := 'inno-lockup-220x58.bmp';
  ExtractTemporaryFile(LockupFile);
  LockupPath := ExpandConstant('{tmp}\') + LockupFile;
  WizardForm.WizardSmallBitmapImage.Bitmap.LoadFromFile(LockupPath);
  // the strip drawn to the control's own size (the two-card Windows host's 2x capture of 11:3x UK, 14 October 2026: the bitmap drawn past the
  // control showed a white block with the lockup's top cut), the control's ground obsidian
  WizardForm.WizardSmallBitmapImage.BackColor := OBSIDIAN;
  WizardForm.WizardSmallBitmapImage.Center := True;
  WizardForm.WizardSmallBitmapImage.Stretch := True;
  WizardForm.WizardSmallBitmapImage.Width := ScaleX(220);
  WizardForm.WizardSmallBitmapImage.Height := ScaleY(58);
  WizardForm.WizardSmallBitmapImage.Left := WizardForm.MainPanel.Width - WizardForm.WizardSmallBitmapImage.Width - ScaleX(16);
  WizardForm.WizardSmallBitmapImage.Top := (WizardForm.MainPanel.Height - WizardForm.WizardSmallBitmapImage.Height) div 2;
  WizardForm.WizardBitmapImage.Visible := False;
  WizardForm.WizardBitmapImage2.Visible := False;
  PlaceLockup(WizardForm.WelcomePage, LockupPath);
  PlaceLockup(WizardForm.FinishedPage, LockupPath);
  LeftMargin := ScaleX(40);
  TextWidth := WizardForm.WelcomePage.Width - 2 * LeftMargin;
  WizardForm.WelcomeLabel1.Left := LeftMargin; WizardForm.WelcomeLabel1.Width := TextWidth; WizardForm.WelcomeLabel1.Top := ScaleY(96);
  WizardForm.WelcomeLabel2.Left := LeftMargin; WizardForm.WelcomeLabel2.Width := TextWidth; WizardForm.WelcomeLabel2.Top := ScaleY(140);
  WizardForm.FinishedHeadingLabel.Left := LeftMargin; WizardForm.FinishedHeadingLabel.Width := TextWidth; WizardForm.FinishedHeadingLabel.Top := ScaleY(96);
  WizardForm.FinishedLabel.Left := LeftMargin; WizardForm.FinishedLabel.Width := TextWidth; WizardForm.FinishedLabel.Top := ScaleY(140);
  WizardForm.RunList.Left := LeftMargin; WizardForm.RunList.Width := TextWidth;
  WizardForm.ReadyMemo.Color := GRAPHITE; WizardForm.ReadyMemo.Font.Color := BONE;
  WizardForm.PageNameLabel.Font.Color := BONE;
  WizardForm.PageDescriptionLabel.Font.Color := ASH;
  WizardForm.Bevel.Visible := False;
  WizardForm.Bevel1.Visible := False;
  // the notebooks carry no Color in Inno's script engine (the 14 October compile: unknown identifier); each page is painted below
  // the welcome page: the mark on the tall image, the words in bone
  WizardForm.WelcomePage.Color := OBSIDIAN;
  WizardForm.WelcomeLabel1.Font.Color := EMBER;
  WizardForm.WelcomeLabel1.Font.Size := 20;
  WizardForm.WelcomeLabel2.Font.Color := BONE;
  // the finish page: the mark, the heading and the one box
  WizardForm.FinishedPage.Color := OBSIDIAN;
  WizardForm.FinishedHeadingLabel.Font.Color := EMBER;
  WizardForm.FinishedLabel.Font.Color := BONE;
  WizardForm.RunList.Color := OBSIDIAN;
  WizardForm.RunList.Font.Color := BONE;
  for I := 0 to WizardForm.InnerNotebook.PageCount - 1 do
    PaintPage(WizardForm.InnerNotebook.Pages[I]);
  // the outer page that holds the inner notebook (the two-card Windows host's capture of 10:5x UK, 14 October 2026: a light band around the
  // tasks list) and the two outer pages take the same paint
  for I := 0 to WizardForm.OuterNotebook.PageCount - 1 do
    PaintPage(WizardForm.OuterNotebook.Pages[I]);
  WizardForm.InnerPage.Color := OBSIDIAN;
  // the installing page: the default bar hidden, an ember bar of our own in its place
  WizardForm.ProgressGauge.Visible := False;
  EmberBarBack := TPanel.Create(WizardForm);
  EmberBarBack.Parent := WizardForm.InstallingPage;
  EmberBarBack.Left := WizardForm.ProgressGauge.Left;
  EmberBarBack.Top := WizardForm.ProgressGauge.Top;
  EmberBarBack.Width := WizardForm.ProgressGauge.Width;
  EmberBarBack.Height := ScaleY(6);
  EmberBarBack.BevelOuter := bvNone;
  EmberBarBack.Color := GRAPHITE;
  EmberBarFill := TPanel.Create(WizardForm);
  EmberBarFill.Parent := EmberBarBack;
  EmberBarFill.Left := 0;
  EmberBarFill.Top := 0;
  EmberBarFill.Width := 0;
  EmberBarFill.Height := EmberBarBack.Height;
  EmberBarFill.BevelOuter := bvNone;
  EmberBarFill.Color := EMBER;
  WizardForm.StatusLabel.Font.Color := ASH;
  WizardForm.FilenameLabel.Font.Color := ASH;
end;

// F14: the install-time update choice, written for the engine's first start only when the task is unchecked (and
// removed when it is checked, so a reinstall that re-ticks it leaves no stale "off")
procedure WriteInstallChoices;
var
  Path: String;
begin
  Path := ExpandConstant('{localappdata}\igneum\app');
  ForceDirectories(Path);
  if WizardIsTaskSelected('autoupdate') then
    DeleteFile(Path + '\install-choices.json')
  else
    SaveStringToFile(Path + '\install-choices.json', '{"auto_update": false}', False);
end;

// /IGNOTA=1: the app's own updater (before an earlier release) started this install; relaunch the app when the files are in.
// /IGNOTA=2: the an earlier release helper started it and relaunches the app itself (src/ota.rs WIN_HELPER): no relaunch here.
function OtaRelaunch: Boolean;
begin
  Result := ExpandConstant('{param:IGNOTA|0}') = '1';
end;
// The an earlier release-and-earlier install in Program Files (administrator), when this per-user install lands on top of it.
function OldAdminInstallDir: String;
begin
  Result := ExpandConstant('{commonpf}\Ember');
  if not FileExists(Result + '\igneum-app.exe') then
    Result := '';
end;

// Stops a running copy before the files are replaced (an upgrade over a running miner): the old copy's own
// stop-igneum.ps1 when one is installed (Program Files or here), else ours from the payload. Then, when someone is at
// the keyboard, offers to remove the Program Files copy (its uninstaller needs one administrator prompt; the data in
// %LOCALAPPDATA%\igneum is the same for both, nothing to copy). A silent (over-the-air) install never asks.
// The install marker (an earlier release): <LOCALAPPDATA>\igneum\app\install-running.flag while this installer works; the engine's
// update helper and the window host never relaunch the app while it is there (src/ota.rs relaunch_allowed, host.cpp).
function MarkerPath: String;
begin
  Result := ExpandConstant('{localappdata}\igneum\app\install-running.flag');
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
// it, between PrepareToInstall and the copy (the second Windows machine, an earlier release take 2, 8 October 2026, 09:24Z: the host gone, no file copied,
// install-running.flag left behind, no ssDone and no DeinitializeSetup). So under a job (IGNEUM_JOB_ID set) this installer
// copies itself to the app's updates folder and runs that copy DETACHED through a one-shot scheduled task (a child of
// the Task Scheduler, outside the job's tree), with the same switches plus /IGDETACHED=1 and its own log, then exits
// without touching the app; the detached run installs and removes the task at its end. The job's proof is the --version
// wait and the detached log, not this process's exit code (1: "Setup failed to initialize").
const
  DetachTask = '{#AppName} install';

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
  Result := ExpandConstant('{localappdata}\igneum\app\updates\install-{#AppVersion}-detached.log');
end;

// The OS's words for the last error, for the log (the second Windows machine, take 3b, 8 October 2026: "could not copy" with no reason).
function LastErrorWords: String;
var
  E: Integer;
begin
  E := DLLGetLastError;
  Result := IntToStr(E) + ' ' + SysErrorMessage(E);
end;

// The installer's copy beside the app's data, or the source itself when no copy can be made (the job folder persists
// after the job; the detached task may run from it). Empty when neither can be used.
function DetachSource: String;
var
  Dir, Dest, Temp, Src: String;
begin
  Result := '';
  Src := ExpandConstant('{srcexe}');
  if not FileExists(Src) then
  begin
    Log('detach: the source ' + Src + ' does not exist (' + LastErrorWords + ')');
    exit;
  end;
  Dir := ExpandConstant('{localappdata}\igneum\app\updates');
  if not DirExists(Dir) then
  begin
    if not ForceDirectories(Dir) then
      Log('detach: could not create ' + Dir + ' (' + LastErrorWords + ')');
  end;
  Dest := Dir + '\Ember-Setup-{#AppVersion}.exe';
  Temp := Dir + '\Ember-Setup-{#AppVersion}.exe.part';
  if DirExists(Dir) then
  begin
    DeleteFile(Temp);
    if FileCopy(Src, Temp, False) then
    begin
      DeleteFile(Dest);
      if RenameFile(Temp, Dest) then
      begin
        Result := Dest;
        exit;
      end;
      Log('detach: copied to ' + Temp + ' but could not rename it to ' + Dest + ' (' + LastErrorWords + '); the task runs the .part copy');
      Result := Temp;
      exit;
    end;
    Log('detach: could not copy ' + Src + ' to ' + Temp + ' (' + LastErrorWords + ')');
  end;
  // no copy: the source itself, from where it is (a job's folder persists after the job)
  Log('detach: the task runs the source in place: ' + Src);
  Result := Src;
end;

// Starts the detached installer through a one-shot task. True when the task started.
function RelaunchDetached: Boolean;
var
  Dest, Args, Cmd: String;
  ResultCode: Integer;
begin
  Result := False;
  Dest := DetachSource;
  if Dest = '' then exit;
  Args := '/VERYSILENT /NORESTART /SUPPRESSMSGBOXES /IGNOTA=' + ExpandConstant('{param:IGNOTA|0}') + ' /IGDETACHED=1 /LOG="' + DetachLogPath + '"';
  Cmd := '/Create /F /SC ONCE /ST 23:59 /RL LIMITED /TN "' + DetachTask + '" /TR "\"' + Dest + '\" ' + Args + '"';
  if not Exec('schtasks.exe', Cmd, '', SW_HIDE, ewWaitUntilTerminated, ResultCode) or (ResultCode <> 0) then
  begin
    Log('detach: schtasks /Create returned ' + IntToStr(ResultCode) + ' (' + LastErrorWords + ') for ' + Cmd);
    exit;
  end;
  if not Exec('schtasks.exe', '/Run /TN "' + DetachTask + '"', '', SW_HIDE, ewWaitUntilTerminated, ResultCode) or (ResultCode <> 0) then
  begin
    Log('detach: schtasks /Run returned ' + IntToStr(ResultCode) + ' (' + LastErrorWords + ')');
    Exec('schtasks.exe', '/Delete /F /TN "' + DetachTask + '"', '', SW_HIDE, ewWaitUntilTerminated, ResultCode);
    exit;
  end;
  Log('detach: this installer runs under the app''s job ' + GetEnv('IGNEUM_JOB_ID') + '; the copy at ' + Dest + ' installs detached, log ' + DetachLogPath);
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
  if CurStep = ssPostInstall then
    WriteInstallChoices;
  if CurStep = ssDone then
  begin
    ClearMarker;
    RemoveDetachTask;
  end;
end;

// Two refusals before anything runs (an earlier release take 1, the second Windows machine, 22:07 UK): the installer's file name must carry ITS version
// (a an earlier release kit once shipped a an earlier release.0 host; a job that passes -Version reads this name), and a marker an earlier
// installer left behind (older than 15 min: it died) is cleared with a line, so nothing holds on it.

function InitializeSetup: Boolean;
var
  Name: String;
begin
  Result := True;
  if UnderAppJob then
  begin
    // the detached installer does the install; this process ends here, before any marker or stop step, so the job's tree
    // teardown finds nothing of the install to kill. When no detached start can be made the install STOPS with the
    // reason in the log (take 3b, the second Windows machine, 8 October 2026: the in-place fallback under the job ended the engine and died
    // with it 3 s later, the app down, nothing installed); it never installs in place under a job.
    if RelaunchDetached then
    begin
      Result := False;
      exit;
    end;
    Log('REFUSED: this installer runs under the app''s job ' + GetEnv('IGNEUM_JOB_ID') + ' and no detached start could be made (the lines above say why); nothing is installed and the app is left running');
    Result := False;
    exit;
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
  // Always THIS installer's stop step, never the installed one: the second Windows machine, an earlier release take 1 (7 October 2026, 22:07 UK) ran the
  // installed an earlier release script, which only asked the engine to quit; the window host lived on, Inno could not replace it,
  // and the host started the old engine again 45 s later. The payload's script ends the host first, by its path under
  // the install dir it is given, then the engine, then waits for every file to unlock (tools/ci/installer-stop-check.sh).
  ExtractTemporaryFile('stop-igneum.ps1');
  StopScript := ExpandConstant('{tmp}\stop-igneum.ps1');
  Log('stop step: ' + StopScript + ' -Install "' + ExpandConstant('{app}') + '"');
  if Exec('powershell.exe', '-NoProfile -ExecutionPolicy Bypass -File "' + StopScript + '" -Install "' + ExpandConstant('{app}') + '"', '', SW_HIDE, ewWaitUntilTerminated, ResultCode) then
    Log('stop step: exit ' + IntToStr(ResultCode) + ' (0 = every file of ours is free; 3 = a file stayed locked, CloseApplications is the fallback)')
  else
    Log('stop step: powershell did not start (' + SysErrorMessage(ResultCode) + '); CloseApplications is the fallback');
  if (OldDir <> '') and FileExists(OldDir + '\stop-igneum.ps1') then
    Exec('powershell.exe', '-NoProfile -ExecutionPolicy Bypass -File "' + StopScript + '" -Install "' + OldDir + '"', '', SW_HIDE, ewWaitUntilTerminated, ResultCode);
  if (OldDir <> '') and (not WizardSilent) and FileExists(OldDir + '\unins000.exe') then
  begin
    if MsgBox('{#AppName} now installs in your user folder, so updates need no administrator prompt.' + #13#10#13#10 +
              'An older copy is still in ' + OldDir + '. Remove it now? (one administrator prompt; your chain data, address and settings stay)',
              mbConfirmation, MB_YESNO) = IDYES then
      ShellExec('runas', OldDir + '\unins000.exe', '/VERYSILENT /SUPPRESSMSGBOXES /NORESTART', '', SW_HIDE, ewWaitUntilTerminated, ResultCode);
  end;
end;

procedure CurPageChanged(CurPageID: Integer);
begin
  // Enter advances on every page (the project's word of 11:3x UK, 14 October 2026: the restyled tasks page left the focus on its
  // first box and Enter did nothing): Next takes the focus as each page shows; Escape is Cancel as the wizard has it
  if WizardForm.NextButton.Enabled and WizardForm.NextButton.Visible then
    WizardForm.ActiveControl := WizardForm.NextButton;
  if CurPageID = wpFinished then
    WizardForm.FinishedLabel.Caption := WizardForm.FinishedLabel.Caption + #13#10#13#10 +
      'Nobody from Igneum will ever ask for your seed.';
end;
