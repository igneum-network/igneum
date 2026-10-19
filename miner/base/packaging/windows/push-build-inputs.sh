#!/usr/bin/env bash
# Publishes build-inputs.zip: the sources a `build` job (app/igneum-app/src/jobbuild.rs) compiles on a PC inside its
# WSL2 Ubuntu, so neither the GitHub runner nor the Mac's build lock is needed for the node and the app engine.
# The zip goes to the downloads folder (dl/<token>/) next to payload-inputs.zip, with its sha256 and a manifest of
# what is inside, exactly as push-inputs.sh does; the job carries the zip's sha256 under the signature, so only the
# signed hash is trusted, never the host. Nothing secret goes in: the jobs file is public.
#
#   packaging/windows/push-build-inputs.sh [--node <fork worktree, default vendor/igneum-node-v4>]
#       [--node-tests "igneum-miner"] [--app-tests "igneum-app"] [--no-app] [--no-node] [--no-deploy] [--out <zip>]
#       [--name <basename.zip>]   the zip's name in the downloads folder (default build-inputs.zip; build-job.mjs gives
#                                 every job its own name since 5 October 2026 night: with one shared name a job
#                                 published while another agent's pack landed pinned THAT agent's sources, three
#                                 times in one evening; zips older than two days are pruned here)
#   --no-node packs and builds the app engine only (a UI or engine change with no node change: miner-ui-2, 5 October
#   2026); the manifest's node block says "none" and the builds list has no node entry.
#
# What goes in (under igneum-build-inputs/):
#   manifest.json        created_at, node {branch, commit, dirty, source}, repo {branch, commit, dirty}, app_version,
#                        builds [{dir, packages, features, bins, targets, optional_on}], tests [{dir, packages}]
#   node/                the fork worktree: everything but target*/ and .git (the working tree, dirty or not; the
#                        manifest says so, and the job's RESULT lines carry it)
#   app/igneum-app/      the engine crate (src, ui, resources, build.rs, Cargo.lock), without target/
#   brand/icons/         igneum.ico and the icon sources (build.rs links the coin icon on the Windows target)
#   proto-cuda/          the worker sources (without nvrtc/redist, 90 MB of NVIDIA DLLs the job does not need)
#   ../igneum-pow/       beside the zip root (the node's crates depend on ../../../../igneum-pow, which resolves to
#                        <extract dir>/igneum-pow when the zip is unpacked); without it every node build fails at once
# Outputs the job returns: igneumd, igneum-miner (Linux and Windows), igneum-app (Windows; Linux when it builds).
#
# Reads: $IGNEUM_SECRETS_DIR/dl-token, $IGNEUM_SECRETS_DIR/dlsite-dir (IGNEUM_DLSITE overrides), $IGNEUM_SECRETS_DIR/vercel
# for the deploy. Then: packaging/ota/publish-jobs.sh add --kind build --target ae432dc7 --deploy, or
# node tools/build-job.mjs run, which does both and brings the binaries back.
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
NODE_SRC="$ROOT/vendor/igneum-node-v4"
NODE_TESTS="${IGNEUM_BUILD_NODE_TESTS:-igneum-miner}"
APP_TESTS="${IGNEUM_BUILD_APP_TESTS:-igneum-app}"
WITH_APP=1
DEPLOY=1
OUT=""
NAME=""
while [ $# -gt 0 ]; do
  case "$1" in
    --node) NODE_SRC="$2"; shift 2 ;;
    --node-tests) NODE_TESTS="$2"; shift 2 ;;
    --app-tests) APP_TESTS="$2"; shift 2 ;;
    --no-app) WITH_APP=0; shift ;;
    --no-node) WITH_NODE=0; shift ;;
    --no-deploy) DEPLOY=0; shift ;;
    --out) OUT="$2"; shift 2 ;;
    --name) NAME="$2"; shift 2 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done
case "$NODE_SRC" in /*) ;; *) NODE_SRC="$ROOT/$NODE_SRC" ;; esac
[ "${WITH_NODE:-1}" = 0 ] || [ -f "$NODE_SRC/Cargo.toml" ] || { echo "no node source at $NODE_SRC (a vendor/igneum-node-* worktree)" >&2; exit 1; }
[ "${WITH_NODE:-1}" = 1 ] || [ "$WITH_APP" = 1 ] || { echo "--no-node with --no-app packs nothing" >&2; exit 2; }
[ -f "$ROOT/app/igneum-app/Cargo.toml" ] || { echo "no app crate at $ROOT/app/igneum-app" >&2; exit 1; }
[ -f "$ROOT/brand/icons/igneum.ico" ] || { echo "no $ROOT/brand/icons/igneum.ico (python3 brand/icons/make-icons.py)" >&2; exit 1; }
command -v rsync >/dev/null || { echo "rsync is needed" >&2; exit 1; }
command -v zip >/dev/null || { echo "zip is needed" >&2; exit 1; }

TOKEN_FILE="$HOME/.config/igneum/dl-token"
DLSITE="${IGNEUM_DLSITE:-}"
[ -n "$DLSITE" ] || { [ -f "$HOME/.config/igneum/dlsite-dir" ] && DLSITE="$(tr -d '[:space:]' < "$HOME/.config/igneum/dlsite-dir")"; } || true
if [ -z "$OUT" ]; then
  [ -f "$TOKEN_FILE" ] || { echo "no $TOKEN_FILE (the downloads token); or give --out <zip>" >&2; exit 1; }
  TOKEN="$(tr -d '[:space:]' < "$TOKEN_FILE")"
  [ -n "$DLSITE" ] && [ -d "$DLSITE/dl/$TOKEN" ] || { echo "no downloads folder: set IGNEUM_DLSITE or $IGNEUM_SECRETS_DIR/dlsite-dir (it must hold dl/<token>/)" >&2; exit 1; }
  DEST="$DLSITE/dl/$TOKEN"
  OUT="$DEST/${NAME:-build-inputs.zip}"
  # per-job zips older than two days (a job expires in two days) go, with their sha256 and manifest
  find "$DEST" -maxdepth 1 -name 'build-inputs-*' \( -name '*.zip' -o -name '*.sha256' -o -name '*.json' \) -mtime +2 -delete 2>/dev/null || true
else
  TOKEN=""
  DEST="$(cd "$(dirname "$OUT")" && pwd)"
  OUT="$DEST/$(basename "$OUT")"
  DEPLOY=0
fi

NODE_BRANCH="$(git -C "$NODE_SRC" rev-parse --abbrev-ref HEAD 2>/dev/null || echo unknown)"
NODE_COMMIT="$(git -C "$NODE_SRC" rev-parse --short HEAD 2>/dev/null || echo unknown)"
NODE_COMMIT_FULL="$(git -C "$NODE_SRC" rev-parse HEAD 2>/dev/null || echo unknown)"
NODE_COMMIT_TIME="$(git -C "$NODE_SRC" log -1 --format=%at HEAD 2>/dev/null || echo 0)"   # SOURCE_DATE_EPOCH on the PC (reproducible builds, 6 Oct 2026)   # the PC job writes it into a .git for kaspa-build-info (6 Oct 2026)
NODE_DIRTY=false; [ -z "$(git -C "$NODE_SRC" status --porcelain 2>/dev/null)" ] || NODE_DIRTY=true
REPO_BRANCH="$(git -C "$ROOT" rev-parse --abbrev-ref HEAD 2>/dev/null || echo unknown)"
REPO_COMMIT="$(git -C "$ROOT" rev-parse --short HEAD 2>/dev/null || echo unknown)"
REPO_DIRTY=false; [ -z "$(git -C "$ROOT" status --porcelain -- app/igneum-app brand/icons proto-cuda 2>/dev/null)" ] || REPO_DIRTY=true
APP_VERSION="$(sed -n 's/^version = "\(.*\)"/\1/p' "$ROOT/app/igneum-app/Cargo.toml" | head -1)"

TMP="$(mktemp -d)"
STAGE="$TMP/igneum-build-inputs"
mkdir -p "$STAGE/node" "$STAGE/app" "$STAGE/brand"
if [ "${WITH_NODE:-1}" = 1 ]; then
  echo "packing the node fork $NODE_SRC ($NODE_BRANCH $NODE_COMMIT$([ "$NODE_DIRTY" = true ] && echo ', dirty'))"
  rsync -a --exclude 'target' --exclude 'target-*' --exclude 'target*' --exclude '.git' --exclude '.DS_Store' --exclude '*.vhdx' "$NODE_SRC/" "$STAGE/node/"
else
  echo "no node (--no-node): the app engine only"
  NODE_BRANCH=none; NODE_COMMIT=none; NODE_DIRTY=false
fi
if [ "$WITH_APP" = 1 ]; then
  echo "packing app/igneum-app ($APP_VERSION) and brand/icons"
  rsync -a --exclude 'target' --exclude '.DS_Store' "$ROOT/app/igneum-app/" "$STAGE/app/igneum-app/"
  rsync -a --exclude '.DS_Store' "$ROOT/brand/icons/" "$STAGE/brand/icons/"
  cp "$ROOT/brand/igneum-coin-1024.png" "$STAGE/brand/"   # app/igneum-app/src/server.rs embeds ../../../brand/igneum-coin-1024.png
fi
echo "packing igneum-pow (the node's path dependency ../../../../igneum-pow, a sibling of the zip root)"
[ -f "$ROOT/igneum-pow/Cargo.toml" ] || { echo "no $ROOT/igneum-pow/Cargo.toml" >&2; exit 1; }
rsync -a --exclude 'target' --exclude 'target-*' --exclude '.DS_Store' "$ROOT/igneum-pow/" "$TMP/igneum-pow/"
echo "packing proto-cuda (without nvrtc/redist)"
rsync -a --exclude 'nvrtc/redist' --exclude '.DS_Store' --exclude '*.exe' --exclude '*.dll' "$ROOT/proto-cuda/" "$STAGE/proto-cuda/"

python3 - "$STAGE/manifest.json" "$NODE_BRANCH" "$NODE_COMMIT" "$NODE_DIRTY" "${NODE_SRC#"$ROOT"/}" "$REPO_BRANCH" "$REPO_COMMIT" "$REPO_DIRTY" "$APP_VERSION" "$WITH_APP" "$NODE_TESTS" "$APP_TESTS" "${WITH_NODE:-1}" "$NODE_COMMIT_FULL" "$NODE_COMMIT_TIME" <<'PY'
import json, sys, datetime
out, nb, nc, nd, ns, rb, rc, rd, av, with_app, node_tests, app_tests, with_node, ncf, nct = sys.argv[1:16]
builds = [{"dir": "node", "packages": ["kaspad", "igneum-miner"], "features": ["kaspad/igneum-pow"], "bins": ["igneumd", "igneum-miner"], "targets": ["linux", "windows"]}] if with_node == "1" else []
tests = []
if node_tests.strip() and with_node == "1":
    tests.append({"dir": "node", "packages": node_tests.split()})
if with_app == "1":
    builds.append({"dir": "app/igneum-app", "packages": ["igneum-app"], "bins": ["igneum-app"], "targets": ["linux", "windows"], "optional_on": ["linux"]})
    if app_tests.strip():
        tests.append({"dir": "app/igneum-app", "packages": app_tests.split()})
m = {
    "created_at": datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
    "node": {"branch": nb, "commit": nc, "commit_full": ncf, "commit_time": int(nct) if nct.isdigit() else 0, "dirty": nd == "true", "source": ns},
    "repo": {"branch": rb, "commit": rc, "dirty": rd == "true"},
    "app_version": av if with_app == "1" else "",
    "builds": builds,
    "tests": tests,
}
json.dump(m, open(out, "w"), indent=2, sort_keys=True)
PY

# Every staged file gets the current time: the PC keeps its cargo target dir between jobs and cargo decides what to
# rebuild by source mtime, so a source older than the last build (rsync and zip keep the Mac's dates) is wrongly
# taken as unchanged. 5 October 2026: the an earlier release job compiled the new daemon against the cached an earlier release consensus crate.
find "$TMP" -type f -exec touch {} +
rm -f "$OUT"
(cd "$TMP" && zip -qr "$OUT" "igneum-build-inputs" "igneum-pow" -x '*.DS_Store')
SUM="$(shasum -a 256 "$OUT" | awk '{print $1}')"
SIZE="$(stat -f %z "$OUT")"
printf '%s\n' "$SUM" > "${OUT%.zip}.sha256"
cp "$STAGE/manifest.json" "${OUT%.zip}.json"
rm -rf "$TMP"
echo "$(basename "$OUT"): $SIZE bytes, sha256 $SUM"
cat "${OUT%.zip}.json"

if [ "$DEPLOY" = 1 ]; then
  echo "deploying $DLSITE"
  (cd "$DLSITE" && npx --yes vercel@latest --global-config "$HOME/.config/igneum/vercel" deploy --prod --yes 2>&1 | grep -v "$TOKEN" || true)
  # the edge serves the previous file for a few seconds after "Aliased" (5 October 2026: the an earlier release job failed here
  # on a sha256 that matched a moment later), so the check retries, as publish-jobs.sh does
  LIVE="$(mktemp)"
  ok=0
  for try in 1 2 3 4 5 6; do
    code="$(curl -s -o "$LIVE" -w '%{http_code}' "https://dl.igneum.network/dl/$TOKEN/$(basename "${OUT%.zip}").sha256")"
    echo "https://dl.igneum.network/dl/<token>/$(basename "${OUT%.zip}").sha256 -> HTTP $code (try $try)"
    if [ "$code" = 200 ] && [ "$(tr -d '[:space:]' < "$LIVE")" = "$SUM" ]; then ok=1; break; fi
    sleep 10
  done
  [ "$ok" = 1 ] || { echo "the live sha256 is not reachable or is not this zip's after 6 tries; check the deploy output" >&2; rm -f "$LIVE"; exit 1; }
  rm -f "$LIVE"
  echo "live: $(basename "$OUT") verified by sha256"
elif [ -n "$TOKEN" ]; then
  echo "not deployed (--no-deploy): cd $DLSITE && npx --yes vercel@latest --global-config $IGNEUM_SECRETS_DIR/vercel deploy --prod --yes"
else
  echo "written to $OUT (not the downloads folder; nothing deployed)"
fi
echo "Next: packaging/ota/publish-jobs.sh add --kind build --target ae432dc7 --deploy   (or node tools/build-job.mjs run)"
echo "Note: a build job pins this zip by sha256; publishing a new zip while a job is still queued makes that job fail its sha256 check."
