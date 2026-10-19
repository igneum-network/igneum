#!/bin/bash
# wallet-mac-build.sh: the Igneum Wallet devnet package for macOS (arm64) built on a Mac through its bench queue as one
# job step (the form of pow/harness/ember-mac-build.sh), from the tree the agent fetched under {work}/harness
# (harness_paths: wallet, tools) with the user's rustup toolchain. Pure Rust: no reference library, no worker.
#   wallet-mac-build.sh <out-dir> [src-dir]     src-dir default: $PWD/harness (the job's fetched tree)
# Steps: (1) cargo build --release --locked of the engine, the signer and the CLI for aarch64-apple-darwin with the
# reproducible flags of wallet/tools/release-repro-build.sh (SOURCE_DATE_EPOCH from IGNEUM_SOURCE_DATE_EPOCH or the
# commit, paths remapped, lockfile locked); (2) the package igneum-wallet-macos-arm64-<version>/{bin,ui,launcher,
# README,VERSION}, tarred sorted with one mtime, gzip -n; (3) the manifest (the two-builder form) and the store put
# of both under phase-zero/product-b-wallet when the machine's store key exists. Nothing here reads or writes a key.
set -u
OUT=${1:?out dir}; SRC=${2:-$PWD/harness}
mkdir -p "$OUT"; LOG=$OUT/build.log; : > "$LOG"
log() { echo "[$(date -u '+%H:%M:%SZ')] $*" | tee -a "$LOG"; }
[ -d "$SRC/wallet" ] || { log "no wallet under $SRC (harness_paths must list wallet and tools)"; exit 2; }
[ -f "$SRC/wallet/Cargo.lock" ] || { log "no wallet/Cargo.lock: the locked build needs it"; exit 2; }
# one line: the first job's manifest carried the path and the version with a newline between them inside a JSON
# string (read leniently at assembly, 11 October 2026)
CLANGV=$("$(xcrun -f clang 2>/dev/null)" --version 2>/dev/null | head -1 | tr -d '\n')
CLT=$(pkgutil --pkg-info=com.apple.pkg.CLTools_Executables 2>/dev/null | awk '/^version:/{print $2}')
SDKV=$(xcrun --show-sdk-version 2>/dev/null || echo unknown)
TOOLCHAIN="$CLANGV; command line tools ${CLT:-unknown}; sdk $SDKV; macOS $(sw_vers -productVersion) $(sw_vers -buildVersion); $(sysctl -n hw.model)"
COMMIT=${IGNEUM_TIP:-$(cd "$SRC" && git rev-parse --short=8 HEAD 2>/dev/null || echo unknown)}
USER_HOME=$(dscl . -read "$HOME" NFSHomeDirectory 2>/dev/null | awk '{print $2}'); USER_HOME=${USER_HOME:-$HOME}
export CARGO_HOME=${CARGO_HOME:-$USER_HOME/.cargo} RUSTUP_HOME=${RUSTUP_HOME:-$USER_HOME/.rustup}
export PATH="$CARGO_HOME/bin:$PATH"
command -v cargo >/dev/null 2>&1 || { log "no cargo under $CARGO_HOME/bin"; exit 2; }
log "toolchain: $TOOLCHAIN; commit $COMMIT; cargo $(cargo --version 2>&1 | head -1)"
export SOURCE_DATE_EPOCH=${IGNEUM_SOURCE_DATE_EPOCH:-$(cd "$SRC" && git log -1 --format=%ct HEAD 2>/dev/null || echo 0)}
export RUSTFLAGS="--remap-path-prefix=$SRC=/src --remap-path-prefix=$CARGO_HOME=/cargo --remap-path-prefix=$(rustc --print sysroot)=/rust"
export CARGO_TARGET_DIR=$OUT/target
cd "$SRC/wallet" || exit 2
log "building the engine, the signer and the CLI (aarch64-apple-darwin, locked)"
cargo build --release --locked -p igneum-wallet-engine -p igneum-signer -p igneum-wallet-cli >> "$LOG" 2>&1 || { log "cargo build failed"; tail -30 "$LOG"; exit 3; }
B=$CARGO_TARGET_DIR/release
for f in igneum-wallet-engine igneum-signer igneum-wallet; do [ -f "$B/$f" ] || { log "missing $B/$f"; exit 3; }; done
crate_ver=$(grep -m1 '^version' "$SRC/wallet/Cargo.toml" | sed 's/.*"\(.*\)".*/\1/')
ver="$crate_ver-${COMMIT:0:8}"; name="igneum-wallet-macos-arm64-$ver"; pkg=$OUT/pkg; rm -rf "$pkg"; mkdir -p "$pkg/$name/bin" "$pkg/$name/ui"
cp "$B/igneum-wallet-engine" "$B/igneum-signer" "$B/igneum-wallet" "$pkg/$name/bin/"
cp -R app/ui/. "$pkg/$name/ui/"; rm -f "$pkg/$name/ui/"*.test.mjs
cp packaging/devnet/igneum-wallet.sh packaging/devnet/README.md "$pkg/$name/"
# the wallet's icon set (site/design/icons/wallet, the design lead's, main 199fed68); the harness export carries site
# when the job lists it, so the copy is skipped with a line when it is not there
if [ -d "$SRC/site/design/icons/wallet" ]; then
  mkdir -p "$pkg/$name/icons"; cp "$SRC"/site/design/icons/wallet/icon.icns "$SRC"/site/design/icons/wallet/icon-512.png "$SRC"/site/design/icons/wallet/icon-1024.png "$pkg/$name/icons/"
else
  log "no site/design/icons/wallet in the fetched tree (harness_paths without site): the package carries no icons"
fi
chmod 755 "$pkg/$name/bin/"* "$pkg/$name/igneum-wallet.sh"
echo "$ver" > "$pkg/$name/VERSION"
# bsdtar on macOS has no --mtime (found on the first job, 11 October 2026: it printed its usage and the pipe went on
# with gzip's exit), so every file's time is set with touch to the commit's time first, the names are given sorted,
# the owners are numeric zero, and pipefail makes a failed tar fail the step
set -o pipefail
stamp=$(date -u -r "$SOURCE_DATE_EPOCH" +%Y%m%d%H%M.%S)
( cd "$pkg" && find "$name" -print | LC_ALL=C sort > files.txt && \
  while IFS= read -r f; do touch -h -t "$stamp" "$f"; done < files.txt && \
  tar --no-recursion --uid 0 --gid 0 --numeric-owner -cf - -T files.txt 2>>"$LOG" | gzip -n -9 > "$name.tar.gz" ) || { log "tar failed"; exit 3; }
[ "$(stat -f %z "$pkg/$name.tar.gz")" -gt 100000 ] || { log "the package is too small to hold the binaries"; exit 3; }
set +o pipefail
genesis=$(grep -o 'GENESIS_DEVNET7: &str = "0x[0-9a-f]*"' "$SRC/wallet/core/src/chain.rs" | grep -o '0x[0-9a-f]*')
l=$(shasum -a 256 "$pkg/$name.tar.gz" | cut -c1-64)
eb=$(shasum -a 256 "$B/igneum-wallet-engine" | cut -c1-64); sb=$(shasum -a 256 "$B/igneum-signer" | cut -c1-64); cb=$(shasum -a 256 "$B/igneum-wallet" | cut -c1-64)
man="$OUT/manifest.json"
cat > "$man" <<EOF
{"product":"igneum-wallet","version":"$ver","commit":"$COMMIT","box":"$(hostname -s)","built_utc":"$(date -u +%FT%TZ)","source_date_epoch":$SOURCE_DATE_EPOCH,
 "chain":{"name":"Igneum devnet7","chain_id":7317,"genesis":"$genesis","rpc":"https://build.igneum.network/devnet7/rpc","submit":"https://build.igneum.network/devnet7/submit","value":"none: a development chain that can be reset; the launch-equivalent chain, the fresh wallet's default; devnet5 a named pin"},
 "toolchain":{"rustc":"$(rustc --version)","cargo":"$(cargo --version)","apple":"$TOOLCHAIN"},
 "rustflags":"$RUSTFLAGS","lockfile":"locked",
 "artefacts":{"$name.tar.gz":"$l"},
 "binaries":{"macos":{"igneum-wallet-engine":"$eb","igneum-signer":"$sb","igneum-wallet":"$cb"}},
 "isolation_rows":{"ISO-01":"PASS","ISO-02":"PASS","ISO-03":"PASS","ISO-04-host-sandbox":"PENDING","record":"docs/product-b/RUNS.md 11 Oct 2026 10:42"}}
EOF
cp "$pkg/$name.tar.gz" "$OUT/"
printf '{"package":"%s","sha256":"%s","engine":"%s","signer":"%s","cli":"%s","commit":"%s"}\n' "$name.tar.gz" "$l" "$eb" "$sb" "$cb" "$COMMIT" > "$OUT/build.json"
log "package $name.tar.gz $l"
if command -v igneum-artefact-put >/dev/null 2>&1; then
  igneum-artefact-put phase-zero product-b-wallet "$OUT/$name.tar.gz" 2>&1 | tail -1 | tee -a "$LOG"
  igneum-artefact-put phase-zero product-b-wallet "$man" 2>&1 | tail -1 | tee -a "$LOG"
else
  log "no store put on this machine: the package and manifest stay in $OUT (the job stores them)"
fi
rm -rf "$CARGO_TARGET_DIR"
exit 0
