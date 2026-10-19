#!/bin/sh
# Igneum Wallet on a fleet Mac: install or update from the signed latest set, no hand on the machine (the founder's
# order of 12 October 2026: "install the new apps on windows and the macs, make sure over the air and auto updates are
# enabled"). Run by the fleet agent at start and hourly:
#   IGNEUM_WALLET_WATCH=0x<the machine's payout address> sh igneum-wallet-fleet-update.sh
# What it does: reads latest.json and its signature from the release host, verifies the signature against the two-key
# list below (ssh-keygen -Y verify, namespace igneum-release; a key superseded for signing is refused over a manifest
# written after its mark, a key with a valid-after mark over one written before it), reads the named directory's
# SHA256SUMS and its signature the same way, checks the sums' sha256 against the manifest, downloads the disk image the
# sums name, checks its sha256, and when the version is newer than the installed one (or nothing is installed) swaps the
# bundle in /Applications, logs the change and opens the app. A first install is put in watch mode on the payout
# address (watch.json in the app's data directory; no key is made on a fleet machine; nothing to unlock). A failed
# verification leaves the install untouched and logs the reason. Prints one verdict line.
# Exit: 0 applied or current; 2 a verification failed; 3 a download or the host failed.
set -u
BASE="${IGNEUM_WALLET_UPDATE_BASE:-https://build.igneum.network/releases}"
APP="${IGNEUM_WALLET_APP:-/Applications/Igneum Wallet.app}"
# the user's real home, not HOME as a job runner sets it (the bench agent gives each step HOME=<its work directory>,
# so the first fleet runs wrote the data under the job's directory; reliability's finding, 12 October 2026)
REAL_HOME="$(eval echo "~$(id -un)")"; [ -d "${REAL_HOME:-}" ] || REAL_HOME="$HOME"
# the engine's own data root (main.rs: $HOME/.igneum-wallet unless IGNEUM_WALLET_DATA names another), so the fleet's lines
# and the updater's share one updates.log (reliability's read of the mini, 12 October 2026 21:55 UK: two roots, two logs)
DATA="${IGNEUM_WALLET_DATA:-$REAL_HOME/.igneum-wallet}"
WATCH="${IGNEUM_WALLET_WATCH:-}"
W="$(mktemp -d /tmp/igneum-wallet-fleet.XXXXXX)"; trap 'rm -rf "$W"' EXIT
LOG="$DATA/updates/updates.log"; mkdir -p "$DATA/updates"
utc() { date -u +%Y-%m-%dT%H:%M:%SZ; }
log() { echo "$(utc) $*" >> "$LOG"; }
say() { echo "igneum-wallet-fleet-update: $*"; }
# the release keys of the rotation record (infra/release/keys/allowed_signers on main) and the superseded marks
# (infra/release/keys/SUPERSEDED): a change of key is a change of these lines
cat > "$W/allowed" <<'EOF'
igneum-release namespaces="igneum-release" ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIMGJXt1a3GwTwu2kcq7iMkqFTA3KUCOoDlkr1BWI8UTF
igneum-release namespaces="igneum-release",valid-after="20261012082000Z" ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIN9kDfjTxTcXkCpmIEViz3SQbuRb5B3nEc1QuwV6FkSx
igneum-release namespaces="igneum-release",valid-after="20261012102400Z" ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIIta/7adTpTjbRmK5+vIcWf0cSoSs13nazpjjNQFWqMh automatic
igneum-release namespaces="igneum-release",valid-after="20261012104300Z" ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIP0olnjXkjqAhlhW85BgKP6SVFzASLWZo0PtI0hEgB8t automatic
EOF
cat > "$W/superseded" <<'EOF'
EOF
fetch() { curl -fsS -m 120 -o "$2" "$1"; }
verify() { # $1 file, $2 sig, $3 written_utc; prints the signer's fingerprint
  ssh-keygen -Y verify -f "$W/allowed" -I igneum-release -n igneum-release -s "$2" -O "verify-time=$(echo "$3" | tr -cd '0-9')Z" < "$1" > /dev/null 2>&1 || return 1
  fp="$(ssh-keygen -Y find-principals -f "$W/allowed" -s "$2" 2>/dev/null | head -1)"
  key="$(ssh-keygen -Y verify -f "$W/allowed" -I igneum-release -n igneum-release -s "$2" < "$1" 2>&1 | grep -o 'SHA256:[A-Za-z0-9+/=]*' | head -1)"
  w="$(echo "$3" | tr -cd '0-9')"
  while read -r k from _; do
    case "$k" in ''|'#'*) continue;; esac
    if [ "$k" = "$key" ] && [ -n "$w" ] && [ "$w" \> "$(echo "$from" | tr -cd '0-9')" ]; then return 2; fi
  done < "$W/superseded"
  echo "${key:-$fp}"
}
fetch "$BASE/wallet/latest/latest.json" "$W/latest.json" || { say "the release host did not answer for latest.json"; exit 3; }
fetch "$BASE/wallet/latest/latest.json.sig" "$W/latest.json.sig" || { say "no signature on latest.json; refused"; log "check failed: latest.json unsigned; this install is untouched"; exit 2; }
written="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1])).get("written_utc",""))' "$W/latest.json")"
signer="$(verify "$W/latest.json" "$W/latest.json.sig" "$written")"; rc=$?
[ $rc = 0 ] || { say "latest.json's signature refused (code $rc)"; log "check failed: latest.json's signature refused (code $rc); this install is untouched"; exit 2; }
version="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["version"])' "$W/latest.json")"
dir="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["directory"].strip("/"))' "$W/latest.json")"
sums_sha="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["sha256sums_sha256"])' "$W/latest.json")"
case "$dir" in *..*|/*) say "latest.json names a directory outside the host"; exit 2;; esac
installed=""; [ -f "$APP/Contents/Info.plist" ] && installed="$(/usr/libexec/PlistBuddy -c 'Print :CFBundleShortVersionString' "$APP/Contents/Info.plist" 2>/dev/null)"
newer() { python3 -c 'import sys
def p(s):
    try: return tuple(int(x) for x in s.strip().split("-")[0].split("."))
    except Exception: return None
a,b=p(sys.argv[1]),p(sys.argv[2]); sys.exit(0 if (a and (b is None or a>b)) else 1)' "$1" "$2"; }
if [ -n "$installed" ] && ! newer "$version" "$installed"; then
  # the replace flag acts on a different numeric version only (the bundle's short version carries no sha: "0.0.4"
  # against the host's "0.0.4-d212767c" is the same release, left alone)
  if [ "${IGNEUM_WALLET_FLEET_REPLACE:-}" = 1 ] && [ "${installed%%-*}" != "${version%%-*}" ]; then say "replacing the development build $installed with the host's latest $version (the fleet's release path)"; else say "current: $installed installed, the host's latest is $version (signed by $signer)"; exit 0; fi
fi
fetch "$BASE/$dir/SHA256SUMS" "$W/SHA256SUMS" || { say "no SHA256SUMS at $BASE/$dir"; exit 3; }
fetch "$BASE/$dir/SHA256SUMS.sig" "$W/SHA256SUMS.sig" || { say "no signature on SHA256SUMS; refused"; log "check failed: $version's SHA256SUMS unsigned; this install is untouched"; exit 2; }
[ "$(shasum -a 256 "$W/SHA256SUMS" | cut -c1-64)" = "$sums_sha" ] || { say "SHA256SUMS is not the one latest.json names"; log "check failed: $version's SHA256SUMS is not the one latest.json names; this install is untouched"; exit 2; }
verify "$W/SHA256SUMS" "$W/SHA256SUMS.sig" "$written" > /dev/null || { say "SHA256SUMS's signature refused"; log "check failed: $version's SHA256SUMS signature refused; this install is untouched"; exit 2; }
line="$(grep -E '  Igneum-Wallet-.*\.dmg$' "$W/SHA256SUMS" | head -1)"
[ -n "$line" ] || { say "$version has no disk image in its sums"; exit 2; }
dmg_sha="${line%% *}"; dmg="${line##* }"
fetch "$BASE/$dir/$dmg" "$W/$dmg" || { say "the disk image did not download"; exit 3; }
[ "$(shasum -a 256 "$W/$dmg" | cut -c1-64)" = "$dmg_sha" ] || { say "the disk image's sha256 is not the sums'"; log "check failed: $version's disk image sha256 mismatch; this install is untouched"; exit 2; }
# the apply: the running app asked to quit by its recorded pid, the bundle swapped, the old one kept beside it
pid="$(pgrep -x 'Igneum Wallet' | head -1)"
if [ -n "$pid" ]; then kill "$pid" 2>/dev/null; n=0; while kill -0 "$pid" 2>/dev/null && [ $n -lt 30 ]; do sleep 1; n=$((n+1)); done; fi
mnt="$W/mnt"; mkdir -p "$mnt"
hdiutil attach -nobrowse -readonly -mountpoint "$mnt" "$W/$dmg" > /dev/null 2>&1 || { say "the disk image did not attach"; exit 3; }
new="$(ls -d "$mnt"/*.app 2>/dev/null | head -1)"
[ -n "$new" ] || { hdiutil detach "$mnt" > /dev/null 2>&1; say "no bundle in the disk image"; exit 3; }
stage="$(dirname "$APP")/.Igneum Wallet.update.app"; rm -rf "$stage"
ditto "$new" "$stage" || { hdiutil detach "$mnt" > /dev/null 2>&1; rm -rf "$stage"; say "the copy did not complete"; exit 3; }
hdiutil detach "$mnt" > /dev/null 2>&1
prev="$(dirname "$APP")/Igneum Wallet (previous).app"; rm -rf "$prev"
[ -d "$APP" ] && mv "$APP" "$prev"
mv "$stage" "$APP" || { say "the bundle did not move into place"; exit 3; }
xattr -dr com.apple.quarantine "$APP" 2>/dev/null
# a first install: watch mode on the payout address, nothing else in the data directory
if [ ! -f "$DATA/vault.json" ] && [ ! -f "$DATA/watch.json" ] && [ -n "$WATCH" ]; then
  printf '{\n  "watch_only": true,\n  "address": "%s",\n  "created_unix": %s\n}\n' "$WATCH" "$(date +%s)" > "$DATA/watch.json"
  log "installed $version in watch mode on $WATCH (fleet)"
fi
log "applied ${installed:-none} -> $version (fleet, signed by $signer)"
open -n "$APP"
say "applied ${installed:-none} -> $version (signed by $signer); the app is open"
exit 0
