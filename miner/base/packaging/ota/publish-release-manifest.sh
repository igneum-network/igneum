#!/usr/bin/env bash
# The update manifest on the release store (docs/product-a/OLD-BASE.md step 5, the project's ruling of 11 October 2026: no
# second key). Two steps, so the founder's one command sits between them and nothing of his runs here:
#
#   publish-release-manifest.sh build --version 0.1.1 --notes "one line of what changed" \
#       [--mac ember-macos-arm64-0.1.1-<sha8>.tar.gz] [--win ember-<sha8>.exe] [--linux ember-linux-x86_64-0.1.1-<sha8>.tar.gz] \
#       [--min-supported 0.1.0] [--channel ember] [--dir <where the artefacts are>]
#     writes igneum-app-latest.json (canonical JSON: sorted keys, no whitespace; every platform entry carries the
#     served URL https://dl.igneum.network/ember/<version>/<name>, the sha256 and the size of the file in --dir) and
#     prints its sha256: the founder signs THAT file with the release key, through main:
#         ssh-keygen -Y sign -f <the release key> -n igneum-release igneum-app-latest.json
#     and the signature comes back as igneum-app-latest.json.sig.
#
#   publish-release-manifest.sh put --sig igneum-app-latest.json.sig [--dir <where the manifest is>]
#     verifies the signature against infra/release/keys/allowed_signers (the namespace igneum-release, the same
#     check the updater runs with the key pinned in the engine), then puts the manifest and its signature on
#     reliability's path: igneum-release-put ember latest igneum-app-latest.json and .sig, served at
#     https://build.igneum.network/releases/ember/latest/ (dl.igneum.network/ember/latest/ on the rewrite), the URL
#     every Ember checks (packaged-config.sh). The store's rotation rule for latest/ (reliability, main e7f991c5):
#     the manifest goes first and waits in staging until its .sig arrives; the pair is accepted only when the
#     signature verifies with the release key under the namespace igneum-release and the manifest's "version" is
#     not lower than the live one; a lower version or a bad signature is refused and the live pair stays; every
#     replacement is a line in latest/ROTATION.log; a .sig alone re-signs the live manifest; the version reads like
#     0.1.0 or 0.2.0-rc1.
#
# Nothing here reads a private key. The store's host and the put tool are reliability's; the host comes from the environment (IGNEUM_STORE_HOST), never from the tree.
set -u
HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/../../../.." && pwd)"   # the repository root (miner/base/packaging/ota -> ../../../..)
SIGNERS="$ROOT/infra/release/keys/allowed_signers"
DL="https://dl.igneum.network/ember"
STORE_HOST="${IGNEUM_STORE_HOST:?the store host (user@host), from outside the tree}"
MODE="${1:-}"; shift || true
VERSION="" NOTES="" MAC="" WIN="" LINUX="" MIN_SUPPORTED="" CHANNEL="ember" DIR="." SIG=""
while [ $# -gt 0 ]; do
  case "$1" in
    --version) VERSION="$2"; shift 2;;
    --notes) NOTES="$2"; shift 2;;
    --mac) MAC="$2"; shift 2;;
    --win) WIN="$2"; shift 2;;
    --linux) LINUX="$2"; shift 2;;
    --min-supported) MIN_SUPPORTED="$2"; shift 2;;
    --channel) CHANNEL="$2"; shift 2;;
    --dir) DIR="$2"; shift 2;;
    --sig) SIG="$2"; shift 2;;
    *) echo "unknown argument: $1" >&2; exit 2;;
  esac
done
cd "$DIR" || { echo "no such directory: $DIR" >&2; exit 2; }
case "$MODE" in
  build)
    [ -n "$VERSION" ] || { echo "--version is required" >&2; exit 2; }
    [ -n "$MAC$WIN$LINUX" ] || { echo "give at least one of --mac, --win, --linux" >&2; exit 2; }
    for f in $MAC $WIN $LINUX; do [ -f "$f" ] || { echo "missing: $DIR/$f" >&2; exit 1; }; done
    python3 - "$VERSION" "$NOTES" "$MIN_SUPPORTED" "$CHANNEL" "$DL" "$MAC" "$WIN" "$LINUX" <<'PY'
import hashlib, json, os, sys, datetime
version, notes, min_supported, channel, dl, mac, win, linux = sys.argv[1:9]
def entry(name, kind):
    b = open(name, "rb").read()
    return {"kind": kind, "url": "%s/%s/%s" % (dl, version, os.path.basename(name)), "sha256": hashlib.sha256(b).hexdigest(), "size": len(b)}
platforms = {}
if mac: platforms["mac"] = entry(mac, "tar.gz")
if win: platforms["windows"] = entry(win, "exe")
if linux: platforms["linux"] = entry(linux, "tar.gz")
m = {"version": version, "channel": channel, "published_at": datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
     "min_supported_version": min_supported or version, "notes": notes, "platforms": platforms}
data = json.dumps(m, sort_keys=True, separators=(",", ":")).encode()
open("igneum-app-latest.json", "wb").write(data)
print("igneum-app-latest.json written: %d bytes, sha256 %s" % (len(data), hashlib.sha256(data).hexdigest()))
print("the founder signs it (through main): ssh-keygen -Y sign -f <the release key> -n igneum-release igneum-app-latest.json")
PY
    ;;
  put)
    [ -n "$SIG" ] || { echo "--sig is required" >&2; exit 2; }
    [ -f igneum-app-latest.json ] || { echo "no igneum-app-latest.json in $DIR (run build first)" >&2; exit 1; }
    [ -f "$SIG" ] || { echo "missing: $SIG" >&2; exit 1; }
    [ -f "$SIGNERS" ] || { echo "no allowed_signers at $SIGNERS" >&2; exit 1; }
    ssh-keygen -Y verify -f "$SIGNERS" -I igneum-release -n igneum-release -s "$SIG" < igneum-app-latest.json || { echo "the signature does not verify against $SIGNERS: nothing put" >&2; exit 1; }
    [ "$SIG" = igneum-app-latest.json.sig ] || cp "$SIG" igneum-app-latest.json.sig
    for f in igneum-app-latest.json igneum-app-latest.json.sig; do
      printf '%s: ' "$f"; ssh -o BatchMode=yes "$STORE_HOST" igneum-release-put ember latest "$f" < "$f" 2>&1 | tail -1
    done
    echo "the apps check https://build.igneum.network/releases/ember/latest/igneum-app-latest.json (and dl.igneum.network/ember/latest/ on the rewrite)"
    ;;
  *) sed -n 2,22p "$0"; exit 2;;
esac
