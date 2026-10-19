#!/usr/bin/env bash
# Builds the HiveOS custom-miner archive igneum-hive-<version>.tar.gz from the Linux binaries and the h-* scripts here.
#   packaging/hive/make-hive-package.sh                 binaries from infra/cross/out-v2 (igneumd, igneum-miner, version.txt)
#                                                       and infra/cross/out-workers (the two GPU workers, build-workers-linux.sh)
#   NODE_OUT=... WORKERS_OUT=... VERSION=... OUT=...     other inputs; VERSION defaults to the igneumd version in version.txt
#   --fake                                              stub binaries instead (the self-test; never ship it)
# Output: packaging/hive/build/igneum-hive-<version>.tar.gz with the directory igneum/ inside (what Hive expects:
# the archive name carries the version, the directory does not), plus its sha256 and the Flight Sheet lines.
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="$(cd "$HERE/../.." && pwd)"
NODE_OUT="${NODE_OUT:-$REPO/infra/cross/out-v2}"
WORKERS_OUT="${WORKERS_OUT:-$REPO/infra/cross/out-workers}"
OUT="${OUT:-$HERE/build}"
FAKE=0; [[ "${1:-}" == "--fake" ]] && FAKE=1
# PRODUCT=public|lab (packaging/mac/packaged-config.sh): the Hive custom-miner name is the slug (igneum or igneum-lab), so
# the archive, the directory inside it, the config and the log paths are the product's own
. "$REPO/packaging/mac/packaged-config.sh"
log() { printf '%s %s\n' "$(date -u +%H:%M:%S)" "$*"; }
die() { log "ERROR: $*" >&2; exit 1; }
stage="$OUT/$PRODUCT_SLUG"; rm -rf "$stage"; mkdir -p "$stage/bin"
if [[ $FAKE == 1 ]]; then
  VERSION="${VERSION:-0.0.0-fake}"
  for b in igneumd igneum-miner igneum-worker-cuda igneum-worker-opencl; do printf '#!/bin/sh\necho fake %s "$@"\n' "$b" > "$stage/bin/$b"; chmod +x "$stage/bin/$b"; done
  printf 'igneumd %s\nfake binaries for the self-test\n' "$VERSION" > "$stage/version.txt"
else
  [[ -x "$NODE_OUT/igneumd" && -x "$NODE_OUT/igneum-miner" ]] || die "no Linux igneumd and igneum-miner in $NODE_OUT (infra/cross/build-linux.sh)"
  for b in igneumd igneum-miner; do file "$NODE_OUT/$b" | grep -q 'ELF 64-bit.*x86-64' || die "$NODE_OUT/$b is not a Linux x86_64 binary"; done
  cp "$NODE_OUT/igneumd" "$NODE_OUT/igneum-miner" "$stage/bin/"
  cp "$NODE_OUT/version.txt" "$stage/version.txt"
  missing=()
  for w in igneum-worker-cuda igneum-worker-opencl; do
    if [[ -x "$WORKERS_OUT/$w" ]]; then cp "$WORKERS_OUT/$w" "$stage/bin/"; else missing+=("$w"); fi
  done
  [[ ${#missing[@]} -gt 0 ]] && log "WARNING: no Linux ${missing[*]} in $WORKERS_OUT (infra/cross/build-workers-linux.sh); the package mines nothing on that vendor"
  [[ -f "$WORKERS_OUT/version.txt" ]] && cat "$WORKERS_OUT/version.txt" >> "$stage/version.txt"
  VERSION="${VERSION:-$(head -1 "$NODE_OUT/version.txt" | awk '{print $2}')}"
fi
[[ -n "$VERSION" ]] || die "no version (VERSION=... or a version.txt with 'igneumd <version>')"
cp "$HERE/h-config.sh" "$HERE/h-run.sh" "$HERE/h-stats.sh" "$HERE/README.md" "$stage/"
sed -e "s/^CUSTOM_VERSION=.*/CUSTOM_VERSION=$VERSION/" -e "s/^CUSTOM_NAME=.*/CUSTOM_NAME=$PRODUCT_SLUG/" "$HERE/h-manifest.conf" > "$stage/h-manifest.conf"
chmod +x "$stage"/h-*.sh "$stage"/bin/*
for f in "$stage"/h-*.sh; do bash -n "$f"; done
tgz="$OUT/$PRODUCT_SLUG-hive-$VERSION.tar.gz"
COPYFILE_DISABLE=1 tar -C "$OUT" --no-xattrs --no-mac-metadata -czf "$tgz" "$PRODUCT_SLUG" 2>/dev/null || COPYFILE_DISABLE=1 tar -C "$OUT" -czf "$tgz" "$PRODUCT_SLUG"
rm -rf "$stage"
sum="$(shasum -a 256 "$tgz" 2>/dev/null | awk '{print $1}' || sha256sum "$tgz" | awk '{print $1}')"
log "wrote $tgz ($(du -h "$tgz" | cut -f1), sha256 $sum)"
cat <<TXT
Flight Sheet (HiveOS): Miner = Custom, then Setup Miner Config:
  Installation URL      https://<your host>/$PRODUCT_SLUG-hive-$VERSION.tar.gz   (publish the archive on the download host)
  Miner name            igneum
  Wallet and worker     0x<your 40-hex payout address>.%WORKER_NAME%
  Pool URL              grpc://<your node>:26610   or   local   (the bundled node on the rig)
  Pass                  (empty)
  Extra config          DEV_FEE=1 IDENTITIES=8 WORKER=auto VOTE=1       (one setting per line also works)
TXT
