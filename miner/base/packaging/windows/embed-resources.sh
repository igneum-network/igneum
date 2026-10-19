#!/usr/bin/env bash
# Gives igneumd.exe and igneum-miner.exe the coin icon and the version block Explorer shows under Properties > Details
# (CompanyName Igneum, ProductName Ember, an earlier release). the founder's rule (4 October 2026): every shipped exe carries the
# coin icon and a version block, like the Mac app and DMG. Two steps, both on the Mac:
#   1. windres compiles resources/igneumd.rc and resources/igneum-miner.rc into COFF objects (cheap, always done).
#   2. --relink runs the same cargo build as proto-cuda/windows-node/cross-build.sh (same packages, features and
#      environment, so every cached dependency is reused) with a linker shim first in PATH: a script named
#      x86_64-w64-mingw32-gcc (the linker rustc picks for x86_64-pc-windows-gnu) that runs the real one and appends
#      the matching object whenever the output being linked is igneumd-<hash>.exe or igneum-miner-<hash>.exe. The two
#      exes are removed from deps/ first so cargo relinks them even when nothing changed. Nothing in vendor/ changes.
#      Why not `cargo rustc -- -C link-arg=<obj>`: it takes one package, and a one-package selection unifies features
#      differently from the two-package cross-build (300 crates differ, checked 4 Oct 2026), so it would rebuild half
#      the tree and ship a differently-featured miner. Why PATH and not CARGO_TARGET_..._LINKER: cargo puts the
#      configured linker in its fingerprints, so that rebuilds every crate (checked on app/igneum-app the same day).
# The exes land in <target dir>/x86_64-pc-windows-gnu/release/, where proto-cuda/windows-app/make-package.sh and
# packaging/windows/make-payload.sh pick them up. Without --relink only the objects are compiled and the command is
# printed (build-installer.ps1 can stamp the resources with rcedit on the PC instead).
#
# Usage: packaging/windows/embed-resources.sh [--relink] [worktree] [target dir]
#   worktree     the node checkout to build, default vendor/igneum-node-v4 (or NODE_WORKTREE)
#   target dir   CARGO_TARGET_DIR, default vendor/igneum-node/target-integration (or CARGO_TARGET_DIR); relative paths
#                are taken from the repository root
#   JOBS=n       cargo jobs, default 4
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
ICONS="$ROOT/brand/icons"
OUT="$HERE/build/res"
VERIFY="$HERE/resources/verify-exe.py"
MINGW="/opt/homebrew/opt/mingw-w64/toolchain-x86_64/x86_64-w64-mingw32"
RELINK=0
if [ "${1:-}" = "--relink" ]; then RELINK=1; shift; fi
NODE="${1:-${NODE_WORKTREE:-$ROOT/vendor/igneum-node-v4}}"
TD="${2:-${CARGO_TARGET_DIR:-$ROOT/vendor/igneum-node/target-integration}}"
case "$NODE" in /*) ;; *) NODE="$ROOT/$NODE" ;; esac
case "$TD" in /*) ;; *) TD="$ROOT/$TD" ;; esac
JOBS="${JOBS:-4}"
command -v x86_64-w64-mingw32-windres >/dev/null || { echo "x86_64-w64-mingw32-windres not found (brew install mingw-w64)" >&2; exit 1; }
[ -f "$ICONS/igneum.ico" ] || { echo "no $ICONS/igneum.ico, making the icons"; python3 "$ICONS/make-icons.py"; }
[ -d "$NODE" ] || { echo "no worktree at $NODE" >&2; exit 1; }
mkdir -p "$OUT"
for name in igneumd igneum-miner; do
    x86_64-w64-mingw32-windres -I "$ICONS" -i "$HERE/resources/$name.rc" -O coff -o "$OUT/$name.res.o"
    echo "compiled $OUT/$name.res.o ($(stat -f %z "$OUT/$name.res.o") bytes)"
done

# The linker shim: the real x86_64-w64-mingw32-gcc plus the resource object for the exe being linked
REAL_GCC="$(command -v x86_64-w64-mingw32-gcc)" || { echo "x86_64-w64-mingw32-gcc not found" >&2; exit 1; }
SHIM="$HERE/build/linker-shim"
rm -rf "$SHIM" && mkdir -p "$SHIM"
cat > "$SHIM/x86_64-w64-mingw32-gcc" <<SHIM_SH
#!/bin/bash
# Written by packaging/windows/embed-resources.sh: runs the real linker and appends the resource object for the exe
# being linked (igneumd-<hash>.exe or igneum-miner-<hash>.exe, found after -o, also inside a @response file).
REAL="$REAL_GCC"
RES="$OUT"
out=""
prev=""
for a in "\$@"; do
  case "\$a" in @*) if [ -f "\${a#@}" ]; then
      p2=""; for w in \$(cat "\${a#@}"); do if [ "\$p2" = "-o" ]; then out="\$w"; fi; p2="\$w"; done
  fi ;; esac
  if [ "\$prev" = "-o" ]; then out="\$a"; fi
  prev="\$a"
done
case "\$(basename "\$out")" in
  igneumd-*.exe|igneumd.exe) exec "\$REAL" "\$@" "\$RES/igneumd.res.o" ;;
  igneum-miner-*.exe|igneum_miner-*.exe|igneum-miner.exe) exec "\$REAL" "\$@" "\$RES/igneum-miner.res.o" ;;
esac
exec "\$REAL" "\$@"
SHIM_SH
chmod +x "$SHIM/x86_64-w64-mingw32-gcc"

# The same environment as proto-cuda/windows-node/cross-build.sh, so the cached dependencies are reused.
export PATH="$HOME/.cargo/bin:/opt/homebrew/bin:$PATH"
export CARGO_TARGET_DIR="$TD"
export CC_x86_64_pc_windows_gnu=x86_64-w64-mingw32-gcc
export CXX_x86_64_pc_windows_gnu=x86_64-w64-mingw32-g++
export AR_x86_64_pc_windows_gnu=x86_64-w64-mingw32-ar
export LIBCLANG_PATH=/opt/homebrew/opt/llvm/lib
export BINDGEN_EXTRA_CLANG_ARGS_x86_64_pc_windows_gnu="--target=x86_64-w64-mingw32 --sysroot=$MINGW -I$MINGW/include"
export CARGO_TARGET_X86_64_PC_WINDOWS_GNU_RUSTFLAGS="-C link-arg=-static -C link-arg=-static-libgcc"
REL="$TD/x86_64-pc-windows-gnu/release"
BUILD="nice -n 19 cargo build --release -j $JOBS -p kaspad -p igneum-miner --features igneum-pow --target x86_64-pc-windows-gnu"

if [ "$RELINK" = 1 ]; then
    cd "$NODE"
    echo "worktree $NODE at $(git rev-parse --short HEAD) ($(git status --porcelain | grep -v '^??' | wc -l | tr -d ' ') modified files), target dir $TD"
    rm -f "$REL"/deps/igneumd-*.exe "$REL"/deps/igneum-miner-*.exe "$REL"/deps/igneum_miner-*.exe
    PATH="$SHIM:$PATH" $BUILD
    for exe in igneumd igneum-miner; do
        f="$REL/$exe.exe"
        x86_64-w64-mingw32-objdump -h "$f" | grep -q '\.rsrc' || { echo "$f has no .rsrc section" >&2; exit 1; }
        echo "$f: $(stat -f %z "$f") bytes; DLLs: $(x86_64-w64-mingw32-objdump -p "$f" | grep 'DLL Name' | awk '{print $3}' | sort -u | tr '\n' ' ')"
    done
    python3 "$VERIFY" --version 0.1.0 "$REL/igneumd.exe" "$REL/igneum-miner.exe"
else
    cat <<TXT

Not relinked (no --relink). To relink from $NODE into $TD, run with the same environment as
proto-cuda/windows-node/cross-build.sh, the shim first in PATH, and the old exes removed from $REL/deps/:
  PATH=$SHIM:\$PATH $BUILD
Or simply: $0 --relink${1:+ $1}${2:+ $2}
Then proto-cuda/windows-app/make-package.sh and packaging/windows/make-payload.sh ship the branded exes. Check with:
  python3 $VERIFY --version 0.1.0 $REL/igneumd.exe $REL/igneum-miner.exe
TXT
fi
