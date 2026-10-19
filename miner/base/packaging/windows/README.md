# Ember for Windows (packaging/windows)

[![windows-ci](https://github.com/igneum-network/igneum/actions/workflows/windows.yml/badge.svg?branch=master)](https://github.com/igneum-network/igneum/actions/workflows/windows.yml)

Inno Setup installer around the app payload. Version an earlier release (4 October 2026): the app (engine + window host) instead of
the console launchers of 0.1.0.

## Built on GitHub, not on a PC (4 October 2026)

`.github/workflows/windows.yml` builds the whole Windows app on a hosted Windows runner on every push to master that
touches `app/`, `packaging/windows/`, `proto-cuda/windows-*`, `proto-cuda/nvrtc/`, `proto-opencl/`,
`proving/windows-wsl2/` or `relay/clients/` (and on `gh workflow run windows.yml`). Nobody runs `BUILD-APP.bat` or
`BUILD-INSTALLER.bat` on a PC any more; both files stay for a hand build and the workflow runs them as they are.

Two jobs:

1. `parse` (required): every `.ps1` under the Windows folders through the Windows PowerShell 5.1 parser
   (`tools/ci/windows/check-ps51.ps1`, run with `powershell.exe`, the PowerShell on the PCs; it refuses to pass unless
   the fixture `tools/ci/windows/fixtures/bad-drive-ref.ps1.txt`, a `"$x: y"` drive-qualified reference, FAILS, so a
   green run proves the check bites), PSScriptAnalyzer as warnings, and a parenthesis check of every `.bat` and `.cmd`
   (`check-bat.ps1`: caret escapes, quotes and `for /f ('...')` strings ignored, depth never below zero, zero at the
   end; its fixture `bad-bare-paren.bat.txt` must be flagged). Both scripts run on a PC too:
   `powershell -NoProfile -ExecutionPolicy Bypass -File tools\ci\windows\check-ps51.ps1`.
2. `build`: the engine with `cargo build --release` on the MSVC target (so the Mac cross-build is no longer an input),
   the window host with `app\windows\BUILD-APP.bat` as it is (MSVC from the runner's Visual Studio, WebView2 SDK from
   NuGet, `host.rc` with the coin icon; `version.h` carries the host version), the payload with `make-payload.sh` in
   Git Bash, the installer with `build-installer.ps1` (Inno Setup from the runner image or chocolatey, rcedit), then a
   smoke run (`igneum-app.exe --version`, `Ember.exe --version` and `--help`, the version block of the host),
   the launcher `proto-cuda/windows-app/start-igneum.ps1` with `DRY_RUN=1` (prints the node command and the GPU plan,
   starts nothing) through the `.ps1` and through `START-IGNEUM.bat`, and three artifacts kept 90 days:
   `igneum-windows-installer` (`Ember-Setup-<version>.exe`), `igneum-windows-payload`
   (`ember-windows-app.zip`), `igneum-windows-host` (`Ember.exe`).

### The inputs the runner cannot build

`igneumd.exe` and `igneum-miner.exe` come from the node fork in `vendor/` (not in git, 20 to 55 minutes to build) and
the prebuilt GPU workers need NVIDIA's NVRTC DLLs (90 MB, not in git). They travel as `payload-inputs.zip` on the
downloads host:

    packaging/windows/push-inputs.sh            # on the Mac, after each node or worker cross-build

collects `igneumd.exe`, `igneum-miner.exe`, the three mingw DLLs, `igneum-worker-cuda.exe`, `nvrtc*.dll`, the licence
texts, `igneum-worker-opencl.exe` and an `inputs.json` (sha256 and bytes of each, the commits, the date), zips them
into `dl/<token>/payload-inputs.zip` with `payload-inputs.sha256` and `payload-inputs.json` next to it in the
downloads folder (`$IGNEUM_SECRETS_DIR/dlsite-dir` names it; `IGNEUM_DLSITE` overrides), and deploys the folder with the
Vercel CLI (`--no-deploy` to skip). The workflow downloads the three with the `DL_TOKEN` repository secret and checks
the sha256. The secret was set once from the Mac and never printed:

    tr -d '[:space:]' < $IGNEUM_SECRETS_DIR/dl-token | gh secret set DL_TOKEN --repo igneum-network/igneum

### The outputs, back to the downloads host

The runner cannot deploy the downloads project (it is deployed by CLI from a folder outside the repo with the Igneum
Vercel login), so the Mac pulls the artifacts:

    packaging/windows/fetch-ci-artifacts.sh [--deploy] [run-id]

downloads the installer and the payload zip from the latest green run on master (`gh run download`, as the project's build account),
copies them into `dl/<token>/` next to the Mac-built packages, writes `igneum-windows-ci.json` (run URL, time), and
prints the deploy command, or deploys with `--deploy`.

### Over-the-air updates (4 October 2026)

The installed app updates itself: `packaging/ota/README.md`. `fetch-ci-artifacts.sh` adds the installer it copies to
the signed manifest (`igneum-app-latest.json`), `--deploy` ships both, and every app downloads the installer within
the hour and runs it `/VERYSILENT /IGNOTA=1` in its own minute of the hour (`Ember.iss` has
`CloseApplications=yes` and a `[Run]` relaunch for that flag). Since an earlier release the installer is per user
(`PrivilegesRequired=lowest`, `%LOCALAPPDATA%\Programs\Ember`): no administrator prompt, ever, for an
update (4 October 2026: two unattended PCs sat at a UAC prompt for an hour); the inbound firewall rule is asked for
once by the app on its first run. the second Windows machine steps: `packaging/ota/TEST.md`. The console
launcher packages (`proto-cuda/windows-app`, `igneum-windows-v4.zip`) are not auto-updated, by design: they are the
engineering path and are replaced by hand.

### What still needs a human

A code-signing certificate. Until Igneum has one, the installer and the exes are unsigned and SmartScreen shows
"Windows protected your PC" (More info, Run anyway). Everything else in this folder runs without a PC.

## On the Mac (hand build, kept for reference)

    packaging/windows/make-payload.sh [out.zip]

Assembles `packaging/windows/ember-windows-app/` and zips it (default `the Desktop folder/ember-windows-app.zip`, about 27 MB):
`igneum-app.exe` (the engine, `app/igneum-app` cross-compiled with the mingw toolchain exactly as `igneumd.exe` is:
`cargo build --release --target x86_64-pc-windows-gnu` with the environment of `proto-cuda/windows-node/cross-build.sh`;
it links against system DLLs only), `igneumd.exe` and `igneum-miner.exe` (the devnet-v4 cross-build), the three mingw
runtime DLLs, the prebuilt one-click workers when they exist (`proto-cuda/nvrtc/igneum-worker-cuda.exe` with NVIDIA's
`nvrtc64_*_0.dll` and `nvrtc-builtins64_*.dll` and the licence texts, `proto-opencl/igneum-worker-opencl.exe`), the
worker sources for the fallback build (`proto-cuda\`, `proto-opencl\`), `igneum-app.json` (update manifest URL with the
token from `$IGNEUM_SECRETS_DIR/dl-token`, log intake, live page), `stop-igneum.ps1`, and `app\windows\` (the window host
sources and `BUILD-APP.bat`, since WebView2 cannot be linked from the Mac).

## On the PC (hand build, kept for reference; CI does all of this)

1. Extract the zip next to `packaging\windows` (or anywhere: `build-installer.ps1 -Payload <folder>`; without a folder it
   downloads `dl.igneum.network/ember-windows-app.zip`).
2. Optional, for the app window: `ember-windows-app\app\windows\BUILD-APP.bat`. Needs Visual Studio with the MSVC v143
   x64 component; it fetches the WebView2 SDK from NuGet, compiles `host.cpp` with the static loader and copies
   `Ember.exe` into the payload. Without it the Start Menu entry runs `igneum-app.exe --launch`, which opens the
   dashboard in the default browser (same engine, same screens).
3. `packaging\windows\BUILD-INSTALLER.bat`: installs Inno Setup 6 through winget if missing, stamps the coin icon and the
   version block into `igneum-app.exe`, `igneumd.exe` and `igneum-miner.exe` with rcedit unless they carry one, compiles
   `Ember.iss`, writes `dist\Ember-Setup-an earlier release.exe`.

The installer: Program Files\Ember, Start Menu group (Ember, Stop Ember, Ember logs, Uninstall),
optional desktop icon and firewall rule for `igneumd.exe` on private networks, "Start Ember now" on the finish page
(as the signed-in user), `stop-igneum.ps1` before an upgrade and on uninstall (it POSTs `api/quit` to the running engine
through `%LOCALAPPDATA%\igneum\app\app.url`, waits, then ends what is left), licence page (MIT placeholder, pending),
finish page with the seed line. Data: `%LOCALAPPDATA%\igneum\devnet-v4` (the chain, the same folder the 0.2.0 launcher
used), `%LOCALAPPDATA%\igneum\app` (settings, wallet.json locked to the user with icacls, the hourly program packs),
`%LOCALAPPDATA%\igneum\logs`. Unsigned until Igneum has a code-signing certificate: SmartScreen warns (More info > Run anyway).

## What the engine does on Windows

GPU detection: `nvidia-smi --query-gpu=index,name,memory.total` for NVIDIA (Discrete, VRAM), the OpenCL worker's `--list`
for the rest (AMD, Intel; Integrated by name: "Radeon Graphics", "Iris", "UHD"), the WMI names as a last resort.
Discrete cards default on (8 identities at 8 GB and up, else 2), integrated off with the reason shown. Each enabled card
gets its own miner process: `igneum-miner mine grpc://127.0.0.1:26610 1 100000000 nvidia-<pc>[-n] --worker <igneum-worker-cuda.exe>
--status-secs 30 --exit-on-seed-change --prepare-packs packs\prepare --evm-address <addr> [--identities N] --payout-label <label>
--worker-args "--device N --pack packs\devnet"` (cwd `%LOCALAPPDATA%\igneum\app`, so the pack paths stay relative; the
OpenCL worker adds `--job-nonces 2097152`). Before each start the engine runs `igneum-miner export-pack` for the prebuilt
worker; without a prebuilt worker it runs today's build path (`proto-cuda\build.bat devnet sm_120` in the MSVC
environment, rebuilt at every hour boundary after exit 42), exactly as `igneum-common.ps1` falls back.

Devnet vote keys are test keys derived from the miner's label; mainnet vote keys will be random and stored like the
wallet (R4.3.12, devnet-only by design). Program Files stays read-only for users: the engine writes only under
`%LOCALAPPDATA%\igneum`, and every helper (nvidia-smi, powershell, cmd, curl, reg, icacls) is launched by its absolute path.

Untested at the time of writing (written on a Mac): the window host (`app/windows/host.cpp`, first run is BUILD-APP.bat),
the Inno build, nvidia-smi and OpenCL detection, the prebuilt workers under the engine. `embed-resources.sh` (windres +
relink on the Mac) still works for `igneumd.exe` and `igneum-miner.exe`; the engine's icon comes from rcedit on the PC.
