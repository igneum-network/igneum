#!/bin/bash
# wallet-mac-app-build.sh: the Igneum Wallet macOS app (the previous wallet's Swift host on the restart's engine,
# OLD-BASE.md steps 8 and 10) built on a Mac through its bench queue as one job step, from the tree the agent fetched
# under {work}/harness (harness_paths: wallet, tools, site/design/icons), with the command line tools alone.
#   wallet-mac-app-build.sh <out-dir> [src-dir]     src-dir default: $PWD/harness
# Steps: (1) cargo build --release --locked of the engine, the signer and the CLI (aarch64-apple-darwin, the
# reproducible flags); (2) swiftc of the host (wallet/app/mac/IgneumWallet.swift + Biometric.swift); (3) the bundle
# "Igneum Wallet.app": Contents/MacOS/{Igneum Wallet, igneum-wallet-engine, igneum-signer, igneum-wallet},
# Contents/Resources/{ui/, icon.icns, README.txt}, Info.plist from wallet/packaging/mac with the crate's version;
# every binary stripped and signed ad hoc (the founder's Developer ID signing is the release step); (4) the DMG by
# hdiutil (the app and a link to /Applications; no third-party dmgbuild); (5) the manifest and the store put of both
# under phase-zero/product-b-wallet when the machine's store key exists. Nothing here reads or writes a key.
set -u
OUT=${1:?out dir}; SRC=${2:-$PWD/harness}
mkdir -p "$OUT"; LOG=$OUT/build.log; : > "$LOG"
log() { echo "[$(date -u '+%H:%M:%SZ')] $*" | tee -a "$LOG"; }
[ -d "$SRC/wallet" ] || { log "no wallet under $SRC (harness_paths must list wallet, tools and site/design/icons)"; exit 2; }
[ -f "$SRC/wallet/Cargo.lock" ] || { log "no wallet/Cargo.lock: the locked build needs it"; exit 2; }
SWIFTC=$(xcrun -f swiftc 2>/dev/null) || { log "swiftc not found (the command line tools carry it)"; exit 2; }
CLT=$(pkgutil --pkg-info=com.apple.pkg.CLTools_Executables 2>/dev/null | awk '/^version:/{print $2}')
TOOLCHAIN="$("$SWIFTC" --version 2>/dev/null | head -1 | tr -d '\n'); command line tools ${CLT:-unknown}; macOS $(sw_vers -productVersion) $(sw_vers -buildVersion); $(sysctl -n hw.model)"
COMMIT=${IGNEUM_TIP:-$(cd "$SRC" && git rev-parse --short=8 HEAD 2>/dev/null || echo unknown)}
# the user's real home: MAC_HOME when the job names it (the product team's form), else the directory service BY USER NAME, never
# from $HOME: the bench agent gives a step HOME=<the job's work directory> by design, and a dscl read on that path
# answered "Invalid Path", so USER_HOME read "Invalid" and no job on the mini found cargo from 13:10Z on 12 October 2026
USER_HOME=${MAC_HOME:-$(eval echo "~$(id -un)")}; case "$USER_HOME" in /*) ;; *) USER_HOME=$HOME ;; esac   # the user's own home by name (tilde expansion reads the directory service), never the step's HOME and never a path literal (the mirror's scrub)
export CARGO_HOME=${CARGO_HOME:-$USER_HOME/.cargo} RUSTUP_HOME=${RUSTUP_HOME:-$USER_HOME/.rustup}
export PATH="$CARGO_HOME/bin:$PATH"
command -v cargo >/dev/null 2>&1 || { log "no cargo under $CARGO_HOME/bin"; exit 2; }
log "toolchain: $TOOLCHAIN; commit $COMMIT; cargo $(cargo --version 2>&1 | head -1)"
export SOURCE_DATE_EPOCH=${IGNEUM_SOURCE_DATE_EPOCH:-$(cd "$SRC" && git log -1 --format=%ct HEAD 2>/dev/null || echo 0)}
export RUSTFLAGS="--remap-path-prefix=$SRC=/src --remap-path-prefix=$CARGO_HOME=/cargo --remap-path-prefix=$(rustc --print sysroot)=/rust"
export CARGO_TARGET_DIR=$OUT/target
cd "$SRC/wallet" || exit 2
crate_ver=$(grep -m1 '^version' Cargo.toml | sed 's/.*"\(.*\)".*/\1/')
ver="$crate_ver-${COMMIT:0:8}"
log "building the engine, the signer and the CLI (aarch64-apple-darwin, locked)"
cargo build --release --locked -p igneum-wallet-engine -p igneum-signer -p igneum-wallet-cli >> "$LOG" 2>&1 || { log "cargo build failed"; tail -30 "$LOG"; exit 3; }
B=$CARGO_TARGET_DIR/release
for f in igneum-wallet-engine igneum-signer igneum-wallet; do [ -f "$B/$f" ] || { log "missing $B/$f"; exit 3; }; done
log "building the window (app/mac/IgneumWallet.swift + Biometric.swift)"
mkdir -p "$OUT/window"
# the command line tools' swiftc needs the SDK named (-sdk) and a deployment target its library carries; the old
# script built under Xcode without them (the first app job: "unable to load standard library for target")
SDK=$(xcrun --show-sdk-path 2>/dev/null) || { log "no SDK path"; exit 2; }
( cd app/mac && "$SWIFTC" -O -sdk "$SDK" -target arm64-apple-macos12.0 -o "$OUT/window/Igneum Wallet" IgneumWallet.swift Biometric.swift -framework Cocoa -framework WebKit -framework LocalAuthentication -framework Security -framework CryptoKit >> "$LOG" 2>&1 ) || { log "the window did not build"; tail -30 "$LOG"; exit 3; }
[ -x "$OUT/window/Igneum Wallet" ] || { log "the window binary is missing"; exit 3; }
STAGE=$OUT/dmg; APP="$STAGE/Igneum Wallet.app"; rm -rf "$STAGE"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources/ui"
STAMP=$(date -u -r "$SOURCE_DATE_EPOCH" +%Y%m%d%H%M)
sed -e "s/VERSION_STAMP/$STAMP/" packaging/mac/IgneumWallet-Info.plist > "$APP/Contents/Info.plist"
plutil -replace CFBundleShortVersionString -string "$crate_ver" "$APP/Contents/Info.plist"
plutil -replace CFBundleIconFile -string "icon" "$APP/Contents/Info.plist"
plutil -lint "$APP/Contents/Info.plist" >/dev/null || { log "the Info.plist does not lint"; exit 3; }
printf 'APPL????' > "$APP/Contents/PkgInfo"
cp "$OUT/window/Igneum Wallet" "$APP/Contents/MacOS/Igneum Wallet"
cp "$B/igneum-wallet-engine" "$B/igneum-signer" "$B/igneum-wallet" "$APP/Contents/MacOS/"
cp -R app/ui/. "$APP/Contents/Resources/ui/"; rm -f "$APP/Contents/Resources/ui/"*.test.mjs
if [ -f "$SRC/site/design/icons/wallet/icon.icns" ]; then cp "$SRC/site/design/icons/wallet/icon.icns" "$APP/Contents/Resources/icon.icns"; else log "no icon.icns in the fetched tree: the app has no icon"; fi
cp packaging/mac/README.txt "$STAGE/README.txt" 2>/dev/null || true
chmod 755 "$APP/Contents/MacOS/"*
# the signing identity (docs/release/ORG-SIGNING.md sections 3 and 6): infra/release/keys/PLATFORM-SIGNING names the
# Developer ID identity and the notary profile by name ("macos identity <name> <team id> <host> <from utc>" and
# "macos notary-profile <profile> ..."); the keys live in the mini's keychain alone. Without a line the bundle is
# signed ad hoc as before and the manifest says so. With one, every Mach-O is signed inside out with the hardened
# runtime, a secure timestamp and the entitlements, then the bundle, the dmg; notarytool submits and stapler staples;
# spctl's verdict goes to the manifest; a signing or notarisation failure ends the build by name (no unsigned dmg).
SIGNFILE="$SRC/infra/release/keys/PLATFORM-SIGNING"
IDENTITY=""; TEAMID=""; PROFILE=""
if [ -f "$SIGNFILE" ]; then
  IDENTITY=$(awk '$1=="macos" && $2=="identity" {for (i=3;i<NF-2;i++) printf "%s%s", $i, (i<NF-3?" ":""); print ""}' "$SIGNFILE" | tail -1)
  TEAMID=$(awk '$1=="macos" && $2=="identity" {print $(NF-2)}' "$SIGNFILE" | tail -1)
  PROFILE=$(awk '$1=="macos" && $2=="notary-profile" {print $3}' "$SIGNFILE" | tail -1)
fi
ENTITLEMENTS="$SRC/wallet/app/mac/entitlements.plist"
SIGNING_JSON='"ad hoc (the founder'"'"'s Developer ID signing and notarisation are the release step)"'
if [ -n "$IDENTITY" ]; then
  log "signing with the identity named in PLATFORM-SIGNING (team $TEAMID), notary profile $PROFILE"
  [ -f "$ENTITLEMENTS" ] || { log "no entitlements file at $ENTITLEMENTS: the signing step cannot run"; exit 3; }
  for b in "$APP/Contents/MacOS/"*; do
    strip "$b" 2>/dev/null || strip -x "$b" 2>/dev/null || true
    codesign --force --options runtime --timestamp --entitlements "$ENTITLEMENTS" --sign "$IDENTITY" "$b" >> "$LOG" 2>&1 || { log "signing failed for $(basename "$b") (the identity '$IDENTITY' in this keychain?)"; exit 3; }
  done
  codesign --force --options runtime --timestamp --entitlements "$ENTITLEMENTS" --sign "$IDENTITY" "$APP" >> "$LOG" 2>&1 || { log "signing the bundle failed"; exit 3; }
  codesign --verify --deep --strict --verbose=2 "$APP" >> "$LOG" 2>&1 || { log "the signed bundle does not verify"; exit 3; }
else
  for b in "$APP/Contents/MacOS/"*; do
    strip "$b" 2>/dev/null || strip -x "$b" 2>/dev/null || true
    codesign -s - -f "$b" >> "$LOG" 2>&1 || { log "ad hoc signing failed for $(basename "$b")"; exit 3; }
  done
  codesign -s - -f "$APP" >> "$LOG" 2>&1 || true
fi
v=$("$APP/Contents/MacOS/igneum-wallet-engine" --version 2>&1 | head -1); log "engine in the bundle: $v"
DMG="$OUT/Igneum-Wallet-$ver.dmg"
if [ -n "$IDENTITY" ]; then
  # the app inside the image is notarised and stapled first, so a copy of it keeps its ticket; then the image
  TMPZIP="$OUT/notarize-app.zip"; ditto -c -k --keepParent "$APP" "$TMPZIP"
  SUB=$(xcrun notarytool submit "$TMPZIP" --keychain-profile "$PROFILE" --wait 2>&1 | tee -a "$LOG"); rm -f "$TMPZIP"
  APP_SUB_ID=$(echo "$SUB" | awk '/^ *id:/ {print $2; exit}'); echo "$SUB" | grep -q "status: Accepted" || { log "notarisation of the app was not accepted (submission ${APP_SUB_ID:-none})"; exit 3; }
  xcrun stapler staple "$APP" >> "$LOG" 2>&1 || { log "stapling the app failed"; exit 3; }
fi
ln -s /Applications "$STAGE/Applications"
hdiutil create -volname "Igneum Wallet" -srcfolder "$STAGE" -ov -format ULFO -fs HFS+ "$DMG" >> "$LOG" 2>&1 || { log "hdiutil failed"; tail -20 "$LOG"; exit 3; }
hdiutil verify "$DMG" >> "$LOG" 2>&1 || { log "the DMG does not verify"; exit 3; }
if [ -n "$IDENTITY" ]; then
  codesign --force --timestamp --sign "$IDENTITY" "$DMG" >> "$LOG" 2>&1 || { log "signing the dmg failed"; exit 3; }
  SUB=$(xcrun notarytool submit "$DMG" --keychain-profile "$PROFILE" --wait 2>&1 | tee -a "$LOG")
  DMG_SUB_ID=$(echo "$SUB" | awk '/^ *id:/ {print $2; exit}'); echo "$SUB" | grep -q "status: Accepted" || { log "notarisation of the dmg was not accepted (submission ${DMG_SUB_ID:-none})"; exit 3; }
  xcrun stapler staple "$DMG" >> "$LOG" 2>&1 || { log "stapling the dmg failed"; exit 3; }
  GATE=$(spctl --assess --type open --context context:primary-signature -v "$DMG" 2>&1 | tee -a "$LOG" | grep -o "accepted\|rejected" | head -1)
  [ "$GATE" = accepted ] || { log "Gatekeeper did not accept the dmg"; exit 3; }
  SIGNING_JSON="{\"identity\":\"$IDENTITY\",\"team_id\":\"$TEAMID\",\"hardened_runtime\":true,\"timestamp\":true,\"entitlements\":\"wallet/app/mac/entitlements.plist\",\"notarization\":{\"app_submission_id\":\"$APP_SUB_ID\",\"dmg_submission_id\":\"$DMG_SUB_ID\",\"status\":\"Accepted\",\"stapled\":true},\"gatekeeper\":\"$GATE\"}"
fi
l=$(shasum -a 256 "$DMG" | cut -c1-64)
genesis=$(grep -o 'GENESIS_7322: &str = "0x[0-9a-f]*"' core/src/chain.rs | grep -o '0x[0-9a-f]*')
man="$OUT/manifest.json"
cat > "$man" <<EOF
{"product":"igneum-wallet-app","version":"$ver","commit":"$COMMIT","builder":"builder-mac","built_utc":"$(date -u +%FT%TZ)","source_date_epoch":$SOURCE_DATE_EPOCH,
 "chain":{"name":"Igneum public test network","chain_id":7322,"network_id":7394,"genesis":"$genesis","rpc":"https://rpc.igneum.network","submit":"https://rpc.igneum.network (the public name takes raw sends)"},
 "toolchain":{"rustc":"$(rustc --version)","cargo":"$(cargo --version)","apple":"$TOOLCHAIN"},
 "signing":$SIGNING_JSON,
 "artefacts":{"$(basename "$DMG")":"$l"},
 "binaries":{"engine":"$(shasum -a 256 "$B/igneum-wallet-engine" | cut -c1-64)","signer":"$(shasum -a 256 "$B/igneum-signer" | cut -c1-64)","cli":"$(shasum -a 256 "$B/igneum-wallet" | cut -c1-64)","window":"$(shasum -a 256 "$OUT/window/Igneum Wallet" | cut -c1-64)"}}
EOF
printf '{"dmg":"%s","sha256":"%s","commit":"%s","engine_version":"%s"}\n' "$(basename "$DMG")" "$l" "$COMMIT" "$v" > "$OUT/build.json"
log "app $(basename "$DMG") $l ($(stat -f %z "$DMG") bytes)"
if command -v igneum-artefact-put >/dev/null 2>&1; then
  igneum-artefact-put phase-zero product-b-wallet "$DMG" 2>&1 | tail -1 | tee -a "$LOG"
  igneum-artefact-put phase-zero product-b-wallet "$man" 2>&1 | tail -1 | tee -a "$LOG"
else
  log "no store put on this machine: the DMG and manifest stay in $OUT (the job stores them)"
fi
rm -rf "$CARGO_TARGET_DIR" "$STAGE"
exit 0
