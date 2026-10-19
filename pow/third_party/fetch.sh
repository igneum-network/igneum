#!/bin/bash
# fetch.sh <name>: fetch a manifest-pinned upstream into pow/third_party/<name>/src
# and verify every file's git blob sha1 against the FETCH manifest. Used for
# controls that are measured but not vendored (D-GA-04). Never run on the Mac.
set -eu
NAME=${1:?name}
DIR=$(cd "$(dirname "$0")" && pwd)/$NAME
MAN=$DIR/FETCH
REPO=$(grep -m1 '^# upstream:' "$MAN" | awk '{print $3}')
COMMIT=$(grep -m1 '^# commit:' "$MAN" | awk '{print $3}')
RAW=${REPO/github.com/raw.githubusercontent.com}
mkdir -p "$DIR/src"
fail=0
grep -v '^#' "$MAN" | grep -v '^\s*$' | while read -r path sha bytes; do
  out="$DIR/src/$path"
  mkdir -p "$(dirname "$out")"
  curl -sfL "$RAW/$COMMIT/$path" -o "$out"
  got=$( (printf 'blob %d\0' "$(stat -c %s "$out")"; cat "$out") | sha1sum | cut -c1-40)
  if [ "$got" != "$sha" ]; then echo "SHA MISMATCH $path: $got != $sha"; exit 1; fi
  echo "ok $path"
done
# Documented patches after verification (each a line in FETCH under "# patch:").
grep '^# patch:' "$MAN" | while read -r _ _ file what; do
  case "$what" in
    guard-neon-include)
      f="$DIR/src/$file"
      if grep -q '^#include "blake3_neon.c"' "$f"; then
        sed -i 's|^#include "blake3_neon.c"|#if BLAKE3_USE_NEON == 1\n#include "blake3_neon.c"\n#endif|' "$f"
        echo "patched $file: $what"
      fi;;
    *) echo "unknown patch $what for $file"; exit 1;;
  esac
done
echo "fetched $NAME at $COMMIT into $DIR/src"
