#!/bin/bash
# Builds packaging/mac/dist/Ember-<version>.dmg: "Ember.app" + README.txt + "Stop Ember.command" +
# a link to /Applications, on a branded image (volume "Ember", the rounded mark as the volume icon, background with the drag arrow,
# icons placed). 3 October 2026; 0.2.0 for devnet v4 on 4 October 2026; an earlier release the same day: the app (no Terminal).
#
# The bundle, an earlier release:
#   Contents/MacOS/Ember      the window (app/mac/Ember.swift, WKWebView + menu-bar item), compiled here
#   Contents/MacOS/igneum-app        the engine (app/igneum-app, cargo --release), compiled here unless ENGINE= names one
#   Contents/Resources/bin/igneum-worker-metal   the Metal worker (WORKER= names it: the store's package worker; never built here)
#   Contents/Resources/igneum-app.json   the update manifest URL (token from $IGNEUM_SECRETS_DIR/dl-token, not in the repo),
#                                    the log intake, the live page (packaged-config.sh)
#   Contents/Resources/igneum.icns   the master mark, a plain black square (brand/master/igneum-mark-square.svg; Tahoe masks it)
# Copies of the binaries are stripped and re-signed ad hoc (strip invalidates the linker signature and arm64 macOS
# refuses an unsigned binary); the originals are not touched.
#
#   packaging/mac/build-dmg.sh               build (the version is app/igneum-app/Cargo.toml's; VERSION= overrides it for a test build)
#   packaging/ota/publish-release-manifest.sh --version <v> --mac dist/Ember-<v>.dmg --notes "..."   then publishes it to the apps
#   WORKER=<path> ENGINE=<path>   the Metal worker (required) and the engine (built here unless named)
# Needs: brand/icons/igneum.icns, igneum-volume.icns and dmg-background.tiff (python3 brand/icons/make-icons.py), swiftc, cargo (rustup), and
# dmgbuild (pip3 install dmgbuild) for the icon layout. Without dmgbuild a plain hdiutil image is built and said so.
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
VERSION="${VERSION:-$(sed -n 's/^version = "\(.*\)"/\1/p' "$ROOT/app/igneum-app/Cargo.toml" | head -1)}"
# Ember runs no node and carries no old miner (docs/product-a/OLD-BASE.md): the bundle is the window, the engine and the Metal worker
WORKER="${WORKER:-}"
# the raster worker bound to coordinate 6 (the founder's order of 15 October 2026: all three lanes); optional
RASTER_WORKER="${RASTER_WORKER:-}"
ENGINE="${ENGINE:-}"
ICONS="$ROOT/brand/icons"
BUILD="$HERE/build"
DIST="$HERE/dist"
export PATH="$HOME/.cargo/bin:/opt/homebrew/bin:$PATH"
# PRODUCT=public|lab (packaged-config.sh): the app name, the dmg name, the volume, the bundle id
. "$HERE/packaged-config.sh"
DMG="$DIST/$PRODUCT_FILE-$VERSION.dmg"
STAGE="$BUILD/dmg"
APP="$STAGE/$PRODUCT_NAME.app"
STAMP="$(date -u +%Y%m%d%H%M)"

for f in igneum.icns igneum-volume.icns; do [ -f "$ICONS/$f" ] || { echo "no $ICONS/$f; run: python3 brand/icons/make-icons.py"; exit 1; }; done
command -v swiftc >/dev/null 2>&1 || { echo "swiftc is needed for the window (xcode-select --install)"; exit 1; }

rm -rf "$BUILD"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources/bin" "$DIST" "$BUILD/worker" "$BUILD/window"

# the Metal worker: the one the job names (WORKER=, the store's package worker igneum-worker-metal-<sha8>); never built here
[ -n "$WORKER" ] || { echo "WORKER= names the Metal worker (the store's)"; exit 1; }
[ -x "$WORKER" ] || { echo "missing: $WORKER"; exit 1; }
file "$WORKER" | grep -q 'arm64' || { echo "$WORKER is not an arm64 binary"; exit 1; }
echo "worker: $WORKER (minos $(otool -l "$WORKER" | awk '/minos/ { print $2; exit }'))"

# the engine (cargo, nice 19, 4 jobs: the devnet is mining on this Mac)
if [ -z "$ENGINE" ]; then
    echo "building the engine (app/igneum-app, cargo --release)"
    (cd "$ROOT/app/igneum-app" && nice -n 19 cargo build --release -j 4 --quiet)
    ENGINE="$ROOT/app/igneum-app/target/release/igneum-app"
fi
[ -x "$ENGINE" ] || { echo "missing: $ENGINE"; exit 1; }
"$ENGINE" --version

# the window
echo "building the window (app/mac/Ember.swift)"
(cd "$ROOT/app/mac" && nice -n 19 swiftc -O -target arm64-apple-macos11 -o "$BUILD/window/Ember" Ember.swift -framework Cocoa -framework WebKit 2>&1 | grep -v 'warning\|^ *\^\|^$' || true)
[ -x "$BUILD/window/Ember" ] || { echo "the window did not build"; exit 1; }

# the bundle
sed -e "s/VERSION_STAMP/$STAMP/" "$HERE/app/Info.plist" > "$APP/Contents/Info.plist"
# the short version is $VERSION whatever the template says (tools/ship-app.mjs keeps the template equal to Cargo.toml;
# the sed that replaced a literal an earlier release here stopped matching at an earlier release and the bundles said an earlier release after that)
plutil -replace CFBundleShortVersionString -string "$VERSION" "$APP/Contents/Info.plist"
plutil -replace CFBundleIdentifier -string "$PRODUCT_BUNDLE_ID" "$APP/Contents/Info.plist"
plutil -replace CFBundleName -string "$PRODUCT_NAME" "$APP/Contents/Info.plist"
plutil -replace CFBundleDisplayName -string "$PRODUCT_NAME" "$APP/Contents/Info.plist"
plutil -lint "$APP/Contents/Info.plist" >/dev/null
printf 'APPL????' > "$APP/Contents/PkgInfo"
cp "$BUILD/window/Ember" "$APP/Contents/MacOS/Ember"
cp "$ENGINE" "$APP/Contents/MacOS/igneum-app"
cp "$ICONS/igneum.icns" "$APP/Contents/Resources/igneum.icns"
cp "$WORKER" "$APP/Contents/Resources/bin/igneum-worker-metal"
if [ -n "$RASTER_WORKER" ]; then [ -x "$RASTER_WORKER" ] || { echo "missing raster worker: $RASTER_WORKER"; exit 1; }; cp "$RASTER_WORKER" "$APP/Contents/Resources/bin/igneum-worker-metal-v18"; echo "raster worker: $RASTER_WORKER ($(shasum -a 256 "$RASTER_WORKER" | cut -c1-16)) as igneum-worker-metal-v18"; fi
# the dev fee's signed manifest beside the worker (ED-05; infra/release/dev-fee-manifest.json and its .sig, copied as
# committed; without the .sig Ember reads the fee as off and says so)
for f in dev-fee-manifest.json dev-fee-manifest.json.sig; do [ -f "$ROOT/../../infra/release/$f" ] && cp "$ROOT/../../infra/release/$f" "$APP/Contents/Resources/bin/$f"; done
write_packaged_config "$APP/Contents/Resources/igneum-app.json"
chmod 755 "$APP/Contents/MacOS/"* "$APP/Contents/Resources/bin/"*

# the signing identity (docs/release/ORG-SIGNING.md sections 3 and 6; the release team's order of 14:45 UK, 15 October
# 2026): infra/release/keys/PLATFORM-SIGNING names the Developer ID identity and the notary profile by name ("macos
# identity <name> <team id> <host> <from utc>", "macos notary-profile <profile> ..."); the keys live in the mini's
# keychain alone. With a line: every Mach-O inside out with the hardened runtime, a secure timestamp and the
# entitlements, then the bundle (verified deep and strict), the app notarised and stapled before the dmg is built, the
# dmg signed, notarised, stapled and assessed by Gatekeeper; a signing or notarisation failure ends the build by name
# (never an unsigned dmg under a named identity). Without a line: the ad hoc path below, as before.
SIGNFILE="$ROOT/../../infra/release/keys/PLATFORM-SIGNING"
IDENTITY=""; TEAMID=""; PROFILE=""; SIGNING_JSON=""
if [ -f "$SIGNFILE" ]; then
  IDENTITY=$(awk '$1=="macos" && $2=="identity" {for (i=3;i<NF-2;i++) printf "%s%s", $i, (i<NF-3?" ":""); print ""}' "$SIGNFILE" | tail -1)
  TEAMID=$(awk '$1=="macos" && $2=="identity" {print $(NF-2)}' "$SIGNFILE" | tail -1)
  PROFILE=$(awk '$1=="macos" && $2=="notary-profile" {print $3}' "$SIGNFILE" | tail -1)
fi
ENTITLEMENTS="$ROOT/entitlements.plist"
if [ -n "$IDENTITY" ]; then
  echo "signing with the identity named in PLATFORM-SIGNING (team $TEAMID), notary profile ${PROFILE:-none}"
  [ -f "$ENTITLEMENTS" ] || { echo "no entitlements file at $ENTITLEMENTS: the signing step cannot run"; exit 3; }
  [ -n "$PROFILE" ] || { echo "PLATFORM-SIGNING names no macos notary-profile: the signing step cannot run"; exit 3; }
  for b in "$APP/Contents/MacOS/"* "$APP/Contents/Resources/bin/igneum-worker-"*; do
    strip "$b" 2>/dev/null || strip -x "$b" 2>/dev/null || true
    codesign --force --options runtime --timestamp --entitlements "$ENTITLEMENTS" --sign "$IDENTITY" "$b" || { echo "signing failed for $(basename "$b") (the identity '$IDENTITY' in this host's keychain?)"; exit 3; }
  done
  codesign --force --options runtime --timestamp --entitlements "$ENTITLEMENTS" --sign "$IDENTITY" "$APP" || { echo "signing the bundle failed"; exit 3; }
  codesign --verify --deep --strict --verbose=2 "$APP" || { echo "the signed bundle does not verify"; exit 3; }
  TMPZIP="$DIST/$PRODUCT_FILE-$VERSION-notarize.zip"; rm -f "$TMPZIP"
  ditto -c -k --keepParent "$APP" "$TMPZIP"
  APP_SUB=$(xcrun notarytool submit "$TMPZIP" --keychain-profile "$PROFILE" --wait 2>&1 | tee /dev/stderr); rm -f "$TMPZIP"
  echo "$APP_SUB" | grep -q "status: Accepted" || { echo "the app's notarisation was not accepted"; exit 3; }
  APP_SUB_ID=$(echo "$APP_SUB" | grep -o "id: [0-9a-f-]*" | head -1 | cut -d' ' -f2)
  xcrun stapler staple "$APP" || { echo "stapling the app failed"; exit 3; }
  echo "the app is signed by '$IDENTITY', notarised ($APP_SUB_ID) and stapled"
else
# the app's own binaries: stripped, then signed ad hoc again (the engine is already stripped by cargo). The
# workers are NOT touched (the release team's rule from 0.2.2, 12 October 2026: one worker sha per platform per
# release; the bundle ships the tested worker's bytes, the ones the keyed pre-gate cells ran on, with the ad hoc
# signature the GPU team's build gave it; 0.2.1's bundle carried the same worker stripped and re-signed, 16a77f8d for
# 88128ef4, and the audit had to derive the one from the other)
for b in "$APP/Contents/MacOS/"*; do
    before=$(stat -f %z "$b")
    strip "$b" 2>/dev/null || strip -x "$b" 2>/dev/null || true
    codesign -s - -f "$b" 2>/dev/null
    codesign -v "$b"
    echo "$(basename "$b"): $before -> $(stat -f %z "$b") bytes, signed ad hoc"
done
for b in "$APP/Contents/Resources/bin/igneum-worker-"*; do
    codesign -v "$b" 2>/dev/null || codesign -s - "$b" 2>/dev/null
    echo "$(basename "$b"): $(stat -f %z "$b") bytes, the stored bytes ($(shasum -a 256 "$b" | cut -c1-16)), signature $(codesign -dv "$b" 2>&1 | grep -c Signature)"
done
fi
# the workers' manifest beside the worker (igneum_supervisor::worker::read_manifest, workers.sha256 in sha256sum's
# form), written AFTER the strip and the ad hoc signing, which change the worker's bytes: a worker whose line does
# not match its bytes is refused (HashMismatch), and a worker without a line launches only under
# IGNEUM_DEV_UNCHECKED_WORKERS=1, which every launch row set and no real install does; the shipped 0.2.0 dmg
# (11 October 2026) held no mining slot for the second reason and the first cut of the fix for the first
# 0.2.17: EVERY worker file beside the engine is named (the pair: igneum-worker-metal and igneum-worker-metal-v18); 0.2.16's
# dmg named the base worker alone, the engine refused the unmanifested raster worker ("no manifest hash") and the Mac's
# raster lane never ran (the mini's read of 18:35 UK, 15 October 2026). The packing check below refuses a bundle whose
# bin holds a worker file the manifest does not name.
( cd "$APP/Contents/Resources/bin" && shasum -a 256 igneum-worker-* > workers.sha256 )
echo "workers.sha256: $(tr '\n' ';' < "$APP/Contents/Resources/bin/workers.sha256")"
for w in "$APP/Contents/Resources/bin"/igneum-worker-*; do
  n=$(basename "$w"); grep -qE "  $n\$" "$APP/Contents/Resources/bin/workers.sha256" || { echo "the bundle's workers.sha256 does not name $n: no dmg"; exit 1; }
done
codesign -s - -f "$APP" 2>/dev/null || true
# each copy must still run
# the worker keeps talking after its hello and takes SIGPIPE when head closes the pipe: under pipefail that status
# ended the script without a word (the mini's third dmg run, 11 October), so the pipe's own status is dropped here
v="$( (echo '{"seq":1,"t":"hello"}' | "$APP/Contents/Resources/bin/igneum-worker-metal" --algo igneumhash protocol 2>&1 || true) | head -1)"; case "$v" in *'"t":"hello"'*|*'"t": "hello"'*) echo "worker: hello ok" ;; *) echo "the Metal worker copy did not answer hello: $v"; exit 1 ;; esac
v="$("$APP/Contents/MacOS/igneum-app" --version 2>&1 || true)"; case "$v" in "igneum-app $VERSION") echo "engine: $v" ;; igneum-app*) echo "the engine says '$v' but this DMG is $VERSION (the update manifest would not match; set VERSION= or rebuild the engine)"; exit 1 ;; *) echo "the engine copy does not run: $v"; exit 1 ;; esac

# the rest of the image
cp "$HERE/dmg/README.txt" "$STAGE/README.txt"
cp "$HERE/app/Stop Ember.command" "$STAGE/Stop $PRODUCT_NAME.command"
chmod 755 "$STAGE/Stop $PRODUCT_NAME.command"

rm -f "$DMG"
# a user install of dmgbuild lives under <home>/Library/Python/<v>; a queue job runs with another HOME, so the user
# base is named from MAC_HOME (the job form's variable for the user's home) when the module is not found as is
if ! python3 -c 'import dmgbuild' >/dev/null 2>&1 && [ -n "${MAC_HOME:-}" ]; then
    export PYTHONUSERBASE="$MAC_HOME/Library/Python/$(python3 -c 'import sys; print("%d.%d" % sys.version_info[:2])')"
fi
if python3 -c 'import dmgbuild' >/dev/null 2>&1; then
    # the module form: a user install under ~/Library/Python/<v>/bin is not on a queue agent's PATH
    echo "dmgbuild $(python3 -c 'import dmgbuild, importlib.metadata as m; print(m.version("dmgbuild"))' 2>/dev/null || echo '(version unread)')"
    python3 -m dmgbuild -s "$HERE/dmg/settings.py" -D "app=$APP" -D "stage=$STAGE" -D "icons=$ICONS" "$PRODUCT_NAME" "$DMG"
else
    echo "note: dmgbuild is not installed (pip3 install dmgbuild); building a plain image without icon positions or background"
    ln -s /Applications "$STAGE/Applications"
    cp "$ICONS/igneum-volume.icns" "$STAGE/.VolumeIcon.icns"
    hdiutil create -volname "$PRODUCT_NAME" -srcfolder "$STAGE" -ov -format ULFO -fs HFS+ "$DMG" >/dev/null
fi
# F-E42 (the project's order of 11:2x UK, 14 October 2026): Finder itself writes the window's layout when it can be scripted from
# here (a queue job in the user's session on the mini): the image converted to read-write, mounted, laid out by
# dmg/finder-layout.applescript, detached, converted back to the compressed read-only image. When Finder cannot be
# reached within twenty seconds (no session, no automation grant) the dmgbuild image stands and the log says so.
if osascript -e 'with timeout of 20 seconds' -e 'tell application "Finder" to get name of startup disk' -e 'end timeout' >/dev/null 2>&1; then
    RW="${DMG%.dmg}-rw.dmg"; rm -f "$RW"
    hdiutil convert "$DMG" -format UDRW -o "$RW" -quiet
    MP=$(hdiutil attach "$RW" -noautoopen 2>/dev/null | grep -oE "/Volumes/.*$" | tail -1)
    if [ -n "$MP" ]; then
        BG=$(cd "$MP" && ls -a | grep -E '^\.background' | head -1)
        if osascript "$HERE/dmg/finder-layout.applescript" "$MP" "$BG" >/dev/null 2>&1; then echo "window layout written by Finder (background $BG)"; else echo "note: Finder did not write the layout; the dmgbuild image stands"; fi
        sync; hdiutil detach "$MP" -quiet || { sleep 3; hdiutil detach "$MP" -force -quiet; }
        rm -f "$DMG"; hdiutil convert "$RW" -format ULFO -o "$DMG" -quiet && rm -f "$RW"
    fi
else
    echo "note: Finder is not scriptable from this session; the dmgbuild image stands (F-E42)"
fi
hdiutil verify "$DMG" >/dev/null
if [ -n "$IDENTITY" ]; then
  codesign --force --timestamp --sign "$IDENTITY" "$DMG" || { echo "signing the dmg failed"; exit 3; }
  DMG_SUB=$(xcrun notarytool submit "$DMG" --keychain-profile "$PROFILE" --wait 2>&1 | tee /dev/stderr)
  echo "$DMG_SUB" | grep -q "status: Accepted" || { echo "the dmg's notarisation was not accepted"; exit 3; }
  DMG_SUB_ID=$(echo "$DMG_SUB" | grep -o "id: [0-9a-f-]*" | head -1 | cut -d' ' -f2)
  xcrun stapler staple "$DMG" || { echo "stapling the dmg failed"; exit 3; }
  GATE=$(spctl --assess --type open --context context:primary-signature -v "$DMG" 2>&1 | grep -o "accepted\|rejected" | head -1)
  [ "$GATE" = accepted ] || { echo "Gatekeeper did not accept the dmg ($GATE)"; exit 3; }
  SIGNING_JSON="{\"identity\":\"$IDENTITY\",\"team_id\":\"$TEAMID\",\"hardened_runtime\":true,\"timestamp\":true,\"entitlements\":\"miner/base/packaging/mac/entitlements.plist\",\"notarization\":{\"app_submission_id\":\"$APP_SUB_ID\",\"dmg_submission_id\":\"$DMG_SUB_ID\",\"status\":\"Accepted\",\"stapled\":true},\"gatekeeper\":\"$GATE\"}"
  printf '%s\n' "$SIGNING_JSON" > "$DMG.signing.json"
  echo "the dmg is signed, notarised ($DMG_SUB_ID), stapled and Gatekeeper says $GATE; the record beside it in $(basename "$DMG").signing.json"
fi
echo "built $DMG ($(du -h "$DMG" | cut -f1), $(stat -f %z "$DMG") bytes, version $VERSION build $STAMP)"
