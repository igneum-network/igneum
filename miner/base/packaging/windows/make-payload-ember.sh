#!/usr/bin/env bash
# The Windows payload of Ember on the old base (docs/product-a/OLD-BASE.md step 7): what the installer ships, staged
# as ember-windows-app/ and zipped, on a Linux box (the Linux builder) from named inputs, nothing built here and nothing of the
# founder's read. What goes in:
#   igneum-app.exe           the engine (the old base's, cross-built with MinGW: three system DLLs, no runtime DLLs)
#   ember-cli.exe            the command line (the restart's igneum-cli, the same cross-build; named apart from the window host Ember.exe)
#   igneum-worker-cuda.exe   the CUDA worker (the store's, the sha the candidate names; the NVRTC DLLs ride in its tail)
#   icon.png, icon.ico       Ember's icon (the design lead's set, brand/icons)
#   igneum-app.json          the packaged configuration (packaged-config.sh: the fixed manifest path; no token, no key)
#   stop-igneum.ps1          what the installer runs before an upgrade and on uninstall
#   app/windows/             host.cpp, host.rc, version.h, opening_size.h, BUILD-APP.bat (the window host is built on the PC)
#   VERSION                  the engine's version
#   make-payload-ember.sh --engine <igneum-app.exe> --cli <ember.exe> --worker <igneum-worker-cuda.exe> [--opencl-worker <igneum-worker-opencl.exe>] [--v21-worker <igneum-worker-cuda-v21.exe>] [--gl-v18-worker <igneum-worker-gl-v18.exe>] [--out <zip>] [--version <v>]
# Prints the staged list and the zip's sha256. The installer (build-installer.ps1 -Payload) takes the extracted folder.
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
BASE="$(cd "$HERE/../.." && pwd)"          # miner/base
ENGINE="" CLI="" WORKER="" OUT="" VERSION=""
while [ $# -gt 0 ]; do
  case "$1" in
    --engine) ENGINE="$2"; shift 2;;
    --cli) CLI="$2"; shift 2;;
    --worker) WORKER="$2"; shift 2;;
    --opencl-worker) OPENCL="$2"; shift 2;;
    --v21-worker) V21="$2"; shift 2;;
    --gl-v18-worker) GLV18="$2"; shift 2;;
    --out) OUT="$2"; shift 2;;
    --version) VERSION="$2"; shift 2;;
    *) echo "unknown argument: $1" >&2; exit 2;;
  esac
done
for f in "$ENGINE" "$CLI" "$WORKER"; do [ -n "$f" ] && [ -f "$f" ] || { echo "--engine, --cli and --worker name files; missing: $f" >&2; exit 2; }; done
[ -n "$VERSION" ] || VERSION="$(sed -n 's/^version = "\(.*\)"/\1/p' "$BASE/app/igneum-app/Cargo.toml" | head -1)"
export PRODUCT="${PRODUCT:-public}"
# the packaged configuration's table and writer (the product name, the payload folder, the manifest path)
. "$BASE/packaging/mac/packaged-config.sh"
STAGE="$HERE/$PRODUCT_PAYLOAD_DIR"
[ -n "$OUT" ] || OUT="$HERE/$PRODUCT_PAYLOAD_DIR.zip"
rm -rf "$STAGE"; mkdir -p "$STAGE/app/windows"
cp "$ENGINE" "$STAGE/igneum-app.exe"
# the command line is ember-cli.exe inside the windowed install: Windows's file system is case-insensitive and the
# window host is Ember.exe (the engine's --launch, the stop script and the installer name it), so a CLI named
# ember.exe was overwritten by the host on the second Windows machine (the ladder's installer row, 11 October 2026: 287,744 bytes under
# both spellings); the released command-line artefact keeps its name ember-<sha>.exe
cp "$CLI" "$STAGE/ember-cli.exe"
cp "$WORKER" "$STAGE/igneum-worker-cuda.exe"
# 0.2.7 (F-110 closed, 14 October 2026): the OpenCL worker beside the CUDA one when given (AMD and Intel cards)
OPENCL="${OPENCL:-}"; WLIST="igneum-worker-cuda.exe"
if [ -n "$OPENCL" ]; then [ -f "$OPENCL" ] || { echo "--opencl-worker names a file; missing: $OPENCL" >&2; exit 2; }; cp "$OPENCL" "$STAGE/igneum-worker-opencl.exe"; WLIST="$WLIST igneum-worker-opencl.exe"; fi
# 0.2.19 (the project's order of 15 October 2026, the fg3 programme worker): the version 21 CUDA worker beside the base one when
# given, shipped as igneum-worker-cuda-v21.exe (the engine binds it to every version 21 coordinate) and named in workers.sha256
V21="${V21:-}"
if [ -n "$V21" ]; then [ -f "$V21" ] || { echo "--v21-worker names a file; missing: $V21" >&2; exit 2; }; cp "$V21" "$STAGE/igneum-worker-cuda-v21.exe"; WLIST="$WLIST igneum-worker-cuda-v21.exe"; fi
# 0.2.40 (the founder's order of 18 October 2026, F-478 on the Windows hosts): the raster lane's worker beside the CUDA
# ones when given, shipped as igneum-worker-gl-v18.exe (the engine binds it to every version 18 coordinate for a CUDA
# card through the GL base) and named in workers.sha256, so a Windows card draws on the raster lane
GLV18="${GLV18:-}"
if [ -n "$GLV18" ]; then [ -f "$GLV18" ] || { echo "--gl-v18-worker names a file; missing: $GLV18" >&2; exit 2; }; cp "$GLV18" "$STAGE/igneum-worker-gl-v18.exe"; WLIST="$WLIST igneum-worker-gl-v18.exe"; fi
cp "$BASE/brand/icons/icon-512.png" "$STAGE/icon.png"
cp "$BASE/brand/icons/icon.ico" "$STAGE/icon.ico"
cp "$BASE/packaging/windows/stop-igneum.ps1" "$STAGE/"
# the dev fee's signed manifest beside the workers (ED-05; infra/release/dev-fee-manifest.json and its .sig, copied as
# committed; without the .sig Ember reads the fee as off and says so)
for f in dev-fee-manifest.json dev-fee-manifest.json.sig; do [ -f "$BASE/../../infra/release/$f" ] && cp "$BASE/../../infra/release/$f" "$STAGE/$f"; done
for f in host.cpp host.rc version.h opening_size.h BUILD-APP.bat; do [ -f "$BASE/app/windows/$f" ] && cp "$BASE/app/windows/$f" "$STAGE/app/windows/"; done
echo "$VERSION" > "$STAGE/VERSION"
write_packaged_config "$STAGE/igneum-app.json" | sed 's/^/config: /'
( cd "$STAGE" && sha256sum igneum-app.exe ember-cli.exe $WLIST > payload.sha256 )
# the workers' manifest beside the worker (igneum_supervisor::worker::read_manifest reads workers.sha256; a worker
# without a line launches only under IGNEUM_DEV_UNCHECKED_WORKERS=1, which every launch row set and no real install
# does; the shipped 0.2.0 installer of 11 October 2026 held no mining slot for that reason)
( cd "$STAGE" && sha256sum $WLIST > workers.sha256 )
echo "--- staged $STAGE"; ( cd "$STAGE" && find . -type f | sort | sed 's|^\./||' )
rm -f "$OUT"
( cd "$HERE" && zip -q -X -r "$OUT" "$PRODUCT_PAYLOAD_DIR" )
echo "--- $OUT $(stat -c %s "$OUT" 2>/dev/null || stat -f %z "$OUT") bytes sha256 $(sha256sum "$OUT" 2>/dev/null | cut -c1-64 || shasum -a 256 "$OUT" | cut -c1-64)"
