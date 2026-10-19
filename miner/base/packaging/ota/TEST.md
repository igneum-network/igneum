# Testing the over-the-air update on Windows (the second Windows machine, machine id 1ccfe586)

The Windows apply path could not be run from the Mac. It was reviewed against `packaging/windows/Ember.iss`,
`stop-igneum.ps1` and `app/windows/host.cpp`; these steps run it for real. Allow 20 minutes.

## an earlier release (after the 4 October incident): what changed and what to check first

The installer is per-user (`%LOCALAPPDATA%\Programs\Ember`, no administrator prompt) and the updater runs
the installer BEFORE it stops anything. On a PC still on an earlier release (installed in Program Files) the an earlier release engine's old
helper stops the miners and raises the prompt once more for the an earlier release installer: click Yes when it appears, or run
`Ember-Setup-an earlier release.exe` by hand (it stops the old app, installs per user, offers to remove the Program Files
copy with one administrator prompt, starts the new app). After that no update ever asks again.

Check on the first an earlier release start: Settings shows an earlier release; the log has `update slot: minute 58 of every hour` (the second Windows machine) and
`firewall: asking once for administrator approval ...` (answer Yes once, or ignore: mining does not wait);
`%LOCALAPPDATA%\igneum\app\firewall-rule.json` exists afterwards. The Start Menu entry opens the new copy.

## What is untested on Windows

- `ota-apply.ps1` end to end: the wait for the engine, `Start-Process -Verb RunAs` of the installer, the UAC prompt,
  the exit code, the relaunch through the `[Run]` entry on `/IGNOTA=1`.
- `CloseApplications=yes` with the window host: the host hides on `WM_CLOSE` instead of quitting, so the Restart
  Manager cannot close it; `PrepareToInstall` (`stop-igneum.ps1`, `Stop-Process -Force` on "Ember") is what
  ends it. Watch for an installer dialog about files in use.
- The rollback: the previous installer is kept in `updates/` only from the second OTA on; a first update has none.
- The deferral path end to end: `Start-Process` without `-Verb RunAs` on an administrator installer, the exception
  on a declined or timed-out prompt, `deferred:true` in `update-result.json`, the engine's "OTA: administrator
  approval not given" line and the banner, the 6-hour retry. (Only reachable while an install is still in Program
  Files; a per-user install never prompts.)
- The per-user installer over a Program Files install: the stop script from the old folder, the "remove the older
  copy?" question, the HKCU Run entry rewritten, two Start Menu entries until the old copy goes.
- The first-run firewall prompt and `firewall-rule.json`.
- `powershell` 5.1 parsing of the helper (`tools/ci/windows/check-ps51.ps1` cannot see it: it is a string in
  `src/ota.rs`). The helper avoids `"$x: y"` and uses nothing newer than 5.1.

## Before

the second Windows machine runs Ember an earlier release, which has no updater at all. Step 0 is therefore a hand install of the first
OTA-capable build; from then on every update is automatic.

0. On the Mac: push master, wait for the green `windows-ci` run, then
   `packaging/windows/fetch-ci-artifacts.sh --deploy` (writes the Windows entry into the manifest and deploys).
   Note the version in the installer name (an earlier release or later). On the second Windows machine: download that installer from
   `https://dl.igneum.network/dl/<token>/Ember-Setup-<v>.exe`, run it over the running an earlier release (it stops the
   old app itself), let it start the app.

## The test

1. Settings (gear) shows "Ember <v>", "Check now", "Install now" (hidden until a download exists), the
   "Install updates by itself at a safe moment" switch ON, and a note. Click Check now: within 10 s the note says
   "This is the latest version (checked ...)" and the log drawer (Logs) has `update check: <v> is current`.
   If it says `no manifest at the update URL yet` the deploy did not land; if `manifest signature does not verify`
   the installer was built from a tree with a different `OTA_PUBLIC_KEY_HEX` than the key that signed.
2. On the Mac, publish a test version: bump `version` in `app/igneum-app/Cargo.toml`, `app/windows/version.h` and
   `app/igneum-app/resources/igneum-app.rc` (patch level only), push, wait for CI, `fetch-ci-artifacts.sh --deploy`
   with `OTA_NOTES="OTA test"`. (The Mac entry is carried over only when it has the same version; without a Mac
   build the manifest carries the Windows entry alone, which is fine.)
3. On the second Windows machine: Settings > Check now. Expected within a minute: the banner "Ember <v+1> is available.
   Downloading it." then "Downloading ... 43%" then "Ember <v+1> is ready. Installs at the next safe moment."
   with Install now and Later. Events: `is available: downloading (44 MB)`, `is ready; it installs at the next safe
   moment`. `%LOCALAPPDATA%\igneum\app\updates\` holds `Ember-Setup-<v+1>.exe` (and `manifest.json`).
4. Wait. The node is synced and a worker is mining, so the only wait is an hourly boundary within 3 minutes (the
   "next program" tile). Expected: the banner changes to "Installing Ember <v+1>: the miners stop, then the
   node, then the app opens again", the footer says stopping, then ONE UAC prompt "Ember-Setup-<v+1>.exe".
   Click Yes. The window closes (stop-igneum.ps1 ends it), about 20 s later the app opens again on its own.
   Check: Settings shows the new version and "Updated from <v>."; the event feed starts with
   `updated to Ember <v+1> from <v>`; the node and the miner are back (same chain data, same address).
   Files: `%LOCALAPPDATA%\igneum\app\ota-apply.log` (the helper), `ota-setup.log` (Inno), no `update-pending.json`
   after 90 s.
5. The declined prompt: repeat 2 and 3 with another patch bump, and click No on the UAC prompt. Expected: the app
   comes back by itself on the OLD version within 15 s with the banner "Update: Windows did not let the installer
   run: ..." and Install now; clicking it brings the prompt again, Yes installs.
6. Install now: with automatic off (the switch), the banner waits "waiting for Install now"; Install now installs
   at once, ignoring the boundary wait.
7. The red bar (consensus): publish with `--activation-height <node daa + 1000>` (the node tile shows the DAA
   score): the banner turns solid ember "Consensus upgrade at height ...: the node is N blocks away. Installing
   ... now." and the apply skips the safe-moment wait.

## If something goes wrong

- The app does not come back: Start Menu > Ember. `ota-apply.log` says which step failed. The old files are
  still in place when the installer did not run; when it ran and the new app fails, reinstall from the download page.
- A UAC prompt every hour: the installer failed or was declined and the retry loop runs at the next check; Settings
  shows the error. Switch automatic off to stop it, or install by hand.
- "update-pending.json" stays and the app keeps restarting: the new version dies early; on the third start the
  helper reinstalls the previous installer when one is in `updates/`, else reinstall by hand.
