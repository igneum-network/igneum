#!/usr/bin/env bash
# Copies the built workers into src-tauri/resources and writes workers.sha256, the manifest the supervisor checks
# every worker against before launching it (design section 1.2). Run after `cargo build --release` and before
# `npm run tauri build`.
set -euo pipefail
here="$(cd "$(dirname "$0")/.." && pwd)"
target="${1:-$here/../target/release}"
dest="$here/src-tauri/resources"
mkdir -p "$dest"
rm -f "$dest"/igneum-worker-* "$dest"/workers.sha256
found=0
for f in "$target"/igneum-worker-*; do
  [ -f "$f" ] || continue
  case "$f" in *.d|*.pdb) continue;; esac
  cp "$f" "$dest/"
  found=1
done
if [ "$found" = 0 ]; then
  echo "no igneum-worker-* in $target" >&2
  exit 1
fi
cd "$dest"
if command -v sha256sum >/dev/null; then
  sha256sum igneum-worker-* > workers.sha256
else
  shasum -a 256 igneum-worker-* > workers.sha256
fi
cat workers.sha256
