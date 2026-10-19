#!/usr/bin/env bash
# Assembles the Windows payload for Ember an earlier release on the Mac and zips it: packaging/windows/ember-windows-app/
# and the Desktop folder/ember-windows-app.zip (or $1). On the PC: extract the zip next to packaging\windows (or let
# build-installer.ps1 fetch it), run app\windows\BUILD-APP.bat (the WebView2 window host, optional), then
# packaging\windows\BUILD-INSTALLER.bat.
#
# What goes in:
#   igneum-app.exe            the engine, cross-compiled here (app/igneum-app, x86_64-pc-windows-gnu)
#   igneum-prove-verify.exe   the node's proof verifier wrapper (runs igneum-prove-host inside WSL2), same build
#   igneumd.exe, igneum-miner.exe   the devnet-v4 cross-build (vendor/igneum-node/target-integration/x86_64-pc-windows-gnu/release)
#   lib*.dll                  the three mingw runtime DLLs, as igneum-windows-v4.zip ships them
#   igneum-worker-cuda.exe, igneum-worker-opencl.exe, nvrtc*.dll   the prebuilt GPU workers from the proto-cuda/proto-opencl
#                             agent when they exist (searched in a few places; IGNEUM_WORKERS_DIR overrides); without them
#                             the engine builds the CUDA worker from proto-cuda\ on the PC (needs the CUDA Toolkit and MSVC)
#   proto-cuda\, proto-opencl\   the worker sources and build.bat for that fallback
#   igneum-app.json           the update manifest URL and the log upload key, from the files named by IGNEUM_DL_TOKEN_FILE and
#                             IGNEUM_UPLOAD_KEY_FILE (defaults: the .next files under $IGNEUM_SECRETS_DIR/igneum when staged, else the
#                             plain ones; packaging/mac/packaged-config.sh, sourced here; never in the repo)
#   stop-igneum.ps1           what the installer runs before an upgrade and on uninstall
#   app\windows\              host.cpp, host.rc, version.h, BUILD-APP.bat (the window host is built on the PC or by CI)
#   Ember.exe          the window host, when app/windows/dist/ holds one (BUILD-APP.bat ran before this script)
# On GitHub (.github/workflows/windows.yml) the same script runs in Git Bash with IGNEUM_WIN_RELEASE and
# IGNEUM_WORKERS_DIR pointing at the unpacked payload-inputs.zip (push-inputs.sh) and IGNEUM_APP_EXE at the engine
# built on the runner; the mingw DLLs are taken from the release folder there, zip falls back to 7z.
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
VERSION="$(sed -n 's/^version = "\(.*\)"/\1/p' "$ROOT/app/igneum-app/Cargo.toml" | head -1)"
# the packaged configuration (the manifest URL from the token file, the upload key from the key file, NODE_OVERRIDE_PARAMS)
# and the PRODUCT=public|lab table (the payload folder ember-windows-app or ember-windows-app-lab, the product name)
. "$ROOT/packaging/mac/packaged-config.sh"
OUT="${1:-$HOME/Desktop/$PRODUCT_PAYLOAD_DIR.zip}"
case "$OUT" in /*) ;; *) OUT="$PWD/$OUT" ;; esac
mkdir -p "$(dirname "$OUT")"
REL="${IGNEUM_WIN_RELEASE:-$ROOT/vendor/igneum-node/target-integration/x86_64-pc-windows-gnu/release}"
ENGINE="${IGNEUM_APP_EXE:-$ROOT/app/igneum-app/target/x86_64-pc-windows-gnu/release/igneum-app.exe}"
MINGW=/opt/homebrew/opt/mingw-w64/toolchain-x86_64/x86_64-w64-mingw32
STAGE="$HERE/$PRODUCT_PAYLOAD_DIR"

[ -f "$ENGINE" ] || { echo "no $ENGINE: build it first (cd app/igneum-app && cargo build --release --target x86_64-pc-windows-gnu, see proto-cuda/windows-node/cross-build.sh for the environment)" >&2; exit 1; }
[ -f "$REL/igneumd.exe" ] || { echo "no $REL/igneumd.exe; cross-compile the node first" >&2; exit 1; }
[ -f "$REL/igneum-miner.exe" ] || { echo "no $REL/igneum-miner.exe; cross-compile the miner first" >&2; exit 1; }

rm -rf "$STAGE"
mkdir -p "$STAGE/proto-cuda/packs" "$STAGE/proto-opencl" "$STAGE/app/windows"
cp "$ENGINE" "$STAGE/igneum-app.exe"
# the node's proof verifier on a PC (release an earlier release, app/igneum-app/src/bin/prove-verify.rs): the engine sets
# IGNEUM_PROOF_VERIFIER to this wrapper, which runs the WSL2 host; built beside the engine by the same cargo build
if [ -f "$(dirname "$ENGINE")/igneum-prove-verify.exe" ]; then cp "$(dirname "$ENGINE")/igneum-prove-verify.exe" "$STAGE/"; echo "verifier wrapper: igneum-prove-verify.exe"
else echo "warning: no igneum-prove-verify.exe next to $ENGINE; the node on this build relays proof records and never includes them"; fi
cp "$REL/igneumd.exe" "$STAGE/igneumd.exe"
cp "$REL/igneum-miner.exe" "$STAGE/igneum-miner.exe"
for dll in lib/libstdc++-6.dll lib/libgcc_s_seh-1.dll bin/libwinpthread-1.dll; do
  name="$(basename "$dll")"
  if [ -f "$REL/$name" ]; then cp "$REL/$name" "$STAGE/"   # push-inputs.sh ships them next to the exes (CI)
  elif [ -f "$MINGW/$dll" ]; then cp "$MINGW/$dll" "$STAGE/"; x86_64-w64-mingw32-strip "$STAGE/$name" 2>/dev/null || true
  else echo "note: $name not found in the mingw toolchain (the node exe is linked -static, so it may not need it)"; fi
done
"$(dirname "$0")/check-runtime-dlls.sh" "$STAGE"   # every imported libstdc++ symbol exported by the shipped DLL (5 October 2026)

# the prebuilt one-click workers, from where proto-cuda/windows-app/make-package.sh takes them: the NVRTC CUDA worker
# with NVIDIA's redistributable DLLs and licence texts, and the OpenCL worker (IGNEUM_WORKERS_DIR overrides with a flat folder)
NVRTC_DIR="$ROOT/proto-cuda/nvrtc"
found_workers=0
if [ -n "${IGNEUM_WORKERS_DIR:-}" ] && [ -d "$IGNEUM_WORKERS_DIR" ]; then
  # igneum-gpu-telemetry.exe rides in the inputs too (push-inputs.sh); the worker glob does not match its name (6 October 2026, an earlier release)
  for f in "$IGNEUM_WORKERS_DIR"/igneum-worker-*.exe "$IGNEUM_WORKERS_DIR"/igneum-gpu-telemetry.exe "$IGNEUM_WORKERS_DIR"/nvrtc*.dll "$IGNEUM_WORKERS_DIR"/LICENSE-*.txt "$IGNEUM_WORKERS_DIR"/THIRD-PARTY.md; do
    [ -f "$f" ] && { cp "$f" "$STAGE/"; found_workers=1; }
  done
else
  if [ -f "$NVRTC_DIR/igneum-worker-cuda.exe" ]; then
    cp "$NVRTC_DIR/igneum-worker-cuda.exe" "$STAGE/"; found_workers=1
    for f in "$NVRTC_DIR"/redist/bin/nvrtc*.dll "$NVRTC_DIR"/redist/LICENSE-*.txt "$NVRTC_DIR/THIRD-PARTY.md"; do [ -f "$f" ] && cp "$f" "$STAGE/"; done
    ls "$STAGE"/nvrtc64_*_0.dll >/dev/null 2>&1 || echo "warning: igneum-worker-cuda.exe without nvrtc64_*_0.dll (run $NVRTC_DIR/fetch-redist.sh); the engine will not use it"
  fi
  if [ -f "$ROOT/proto-opencl/igneum-worker-opencl.exe" ]; then cp "$ROOT/proto-opencl/igneum-worker-opencl.exe" "$STAGE/"; found_workers=1; fi
  if [ -f "$ROOT/proto-opencl/igneum-gpu-telemetry.exe" ]; then cp "$ROOT/proto-opencl/igneum-gpu-telemetry.exe" "$STAGE/"; fi   # AMD power, heat, fans, clocks (5 October 2026)
fi
if [ "$found_workers" = 1 ]; then echo "workers: $(cd "$STAGE" && ls igneum-worker-*.exe nvrtc*.dll 2>/dev/null | tr '\n' ' ')"
else echo "note: no prebuilt igneum-worker-cuda.exe / igneum-worker-opencl.exe found; the engine builds the CUDA worker from proto-cuda\\ on the PC (CUDA Toolkit and MSVC needed)"; fi

# the worker sources for the fallback build
cp "$ROOT/proto-cuda/host.cu" "$ROOT/proto-cuda/build.bat" "$ROOT/proto-cuda/README.md" "$STAGE/proto-cuda/"
cp "$ROOT/proto-opencl/host.c" "$ROOT/proto-opencl/build.bat" "$ROOT/proto-opencl/README.md" "$STAGE/proto-opencl/"
# the window host sources, built on the PC or by CI; the built host when BUILD-APP.bat already ran here
cp "$ROOT/app/windows/host.cpp" "$ROOT/app/windows/host.rc" "$ROOT/app/windows/version.h" "$ROOT/app/windows/BUILD-APP.bat" "$STAGE/app/windows/"
mkdir -p "$STAGE/app/windows/art" && cp "$ROOT/brand/icons/igneum.ico" "$STAGE/app/windows/art/"
# the WebView2 evergreen bootstrapper (the rights step installs the runtime when absent or below the host loader's minimum):
# rides when it sits in app/windows/dist and its sha256 is the one pinned in packaging/windows/webview2.sha256
if [ -f "$ROOT/app/windows/dist/MicrosoftEdgeWebview2Setup.exe" ]; then
  WANT="$(grep -v '^#' "$ROOT/packaging/windows/webview2.sha256" 2>/dev/null | tr -d '[:space:]')"
  HAVE="$( (shasum -a 256 "$ROOT/app/windows/dist/MicrosoftEdgeWebview2Setup.exe" 2>/dev/null || sha256sum "$ROOT/app/windows/dist/MicrosoftEdgeWebview2Setup.exe") | cut -d' ' -f1)"
  [ -n "$WANT" ] && [ "$WANT" = "$HAVE" ] || { echo "make-payload: MicrosoftEdgeWebview2Setup.exe is not the pinned bootstrapper (packaging/windows/webview2.sha256)" >&2; exit 1; }
  cp "$ROOT/app/windows/dist/MicrosoftEdgeWebview2Setup.exe" "$STAGE/"; echo "webview2 bootstrapper: pinned, rides"
fi
if [ -f "$ROOT/app/windows/dist/Ember.exe" ]; then
  # the host gate (an earlier release): the version resource equals this payload's version, no mingw-w64 signature, and the sha pinned
  # in packaging/windows/host.sha256 when that file exists (the second Windows machine, 7 October 2026: a an earlier release.0 mingw host in a an earlier release installer
  # crashed in ntdll and left the window on "starting the engine")
  HOST_VERSION="${APP_VERSION:-$(sed -n 's/^version = "\(.*\)"/\1/p' "$ROOT/app/igneum-app/Cargo.toml" | head -1)}"
  PIN_ARG=""; [ -s "$ROOT/packaging/windows/host.sha256" ] && PIN_ARG="$ROOT/packaging/windows/host.sha256"
  python3 "$ROOT/packaging/windows/host-gate.py" "$ROOT/app/windows/dist/Ember.exe" "$HOST_VERSION" $PIN_ARG || { echo "make-payload: the window host is refused (host-gate.py above); no host rides this payload" >&2; exit 1; }
  cp "$ROOT/app/windows/dist/Ember.exe" "$STAGE/"; echo "window host: Ember.exe from app/windows/dist (host-gate ok)"
fi
cp "$HERE/stop-igneum.ps1" "$STAGE/stop-igneum.ps1"

# proving v0 (spec 7.7, app/igneum-app/src/prover.rs): the SP1 host runs inside WSL2 on a PC, so the payload ships the
# Linux x86_64 host and exporter (proving/igneum-prove cross-built with cargo-zigbuild, glibc 2.36, sp1 cuda feature;
# IGNEUM_PROVE_LINUX overrides the folder) under wsl2\bin\, the WSL2 setup scripts and the fixtures. The engine runs
# them as /mnt/<drive>/.../wsl2/bin/igneum-prove-host from Ubuntu-24.04; Set up runs wsl2\setup-wsl.sh.
PROVE_LINUX="${IGNEUM_PROVE_LINUX:-$ROOT/proving/igneum-prove/target-linux/x86_64-unknown-linux-gnu/release}"
mkdir -p "$STAGE/wsl2/bin" "$STAGE/wsl2/fixtures"
# 6 October 2026 (an earlier release): the pair is REQUIRED. First from the inputs next to the exes (push-inputs.sh ships them flat as
# igneum-prove-host.linux-x86_64 and igneum-prove-export.linux-x86_64, which is what CI has), else from PROVE_LINUX (a Mac
# build); a payload without them shipped every PC a stale prover from an earlier release to an earlier release (no peer-proof verification, no exports).
for b in igneum-prove-host igneum-prove-export; do
  if [ -f "$REL/$b.linux-x86_64" ]; then cp "$REL/$b.linux-x86_64" "$STAGE/wsl2/bin/$b"; src="the inputs ($REL)"
  elif [ -f "$PROVE_LINUX/$b" ]; then cp "$PROVE_LINUX/$b" "$STAGE/wsl2/bin/$b"; src="$PROVE_LINUX"
  else echo "no Linux $b (looked at $REL/$b.linux-x86_64 and $PROVE_LINUX/$b); the payload never ships without the WSL2 prover pair again" >&2; exit 1; fi
  chmod +x "$STAGE/wsl2/bin/$b"
  echo "prover (WSL2): $b $(stat -f %z "$STAGE/wsl2/bin/$b" 2>/dev/null || stat -c %s "$STAGE/wsl2/bin/$b") bytes from $src"
done
# the shas the app and a job can check the installed pair against (a stale host is loud at the node's start from an earlier release)
(cd "$STAGE/wsl2/bin" && { shasum -a 256 igneum-prove-host igneum-prove-export 2>/dev/null || sha256sum igneum-prove-host igneum-prove-export; } > SHA256SUMS)
cp "$ROOT"/proving/windows-wsl2/*.sh "$ROOT"/proving/windows-wsl2/*.ps1 "$ROOT"/proving/windows-wsl2/*.bat "$ROOT/proving/windows-wsl2/README.txt" "$STAGE/wsl2/"
cp "$ROOT"/proving/fixtures/*.json "$STAGE/wsl2/fixtures/"

# the packaged configuration: the token and the key stay out of the repo (packaged-config.sh prints the file names and
# the fingerprints, never the values)
write_packaged_config "$STAGE/igneum-app.json"
cat > "$STAGE/README.txt" <<TXT
$PRODUCT_NAME $VERSION for Windows (payload). Devnet v4. Test network; nothing is bought or sold.
Nobody from Igneum will ever ask for your seed.
This folder is what the installer packs. To build the installer on a PC:
  1. app\\windows\\BUILD-APP.bat        builds "Ember.exe" (the window; needs Visual Studio; optional)
  2. packaging\\windows\\BUILD-INSTALLER.bat   builds $PRODUCT_FILE-Setup-$VERSION.exe (Inno Setup via winget; PRODUCT=$PRODUCT)
To run without the installer: double-click igneum-app.exe (the dashboard opens in your browser).
TXT
for f in "$STAGE"/*.bat "$STAGE/README.txt" "$STAGE/stop-igneum.ps1" "$STAGE/app/windows/BUILD-APP.bat" "$STAGE/proto-cuda/build.bat" "$STAGE/proto-opencl/build.bat"; do
  [ -f "$f" ] && perl -pi -e 's/\r?\n/\r\n/' "$f"
done
rm -f "$OUT"
if command -v zip >/dev/null 2>&1; then (cd "$HERE" && zip -qr "$OUT" "$PRODUCT_PAYLOAD_DIR" -x '*.DS_Store')
elif command -v 7z >/dev/null 2>&1; then (cd "$HERE" && 7z a -tzip -bso0 -bsp0 "$OUT" "$PRODUCT_PAYLOAD_DIR" '-xr!.DS_Store')
else echo "neither zip nor 7z is on PATH" >&2; exit 1; fi
echo "staged $STAGE"
ls -la "$OUT"
if command -v unzip >/dev/null 2>&1; then unzip -l "$OUT" | tail -n +4 | awk '{print $1, $4}' | sed '/^ *$/d' | head -40
else (cd "$STAGE" && find . -type f | sort | head -40); fi
