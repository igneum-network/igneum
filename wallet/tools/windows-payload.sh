#!/usr/bin/env bash
# The wallet's Windows payload for the second Windows machine's installer job (OLD-BASE.md step 10; the product team's form): on a Linux box after
# wallet/tools/release-repro-build.sh <commit> has made the Windows cross-build, the three .exe, the page, the icon,
# the launcher and the stop script go under igneum-windows-wallet/ and into one zip with one mtime (the commit's
# time), named igneum-windows-wallet-<version>-<sha8>.zip; the zip's sha256 is printed. The put to the store is
# builder one's (igneum-artefact-put phase-zero product-b-wallet <zip>): this box carries the zip there by scp.
#   usage: windows-payload.sh <commit>
set -u
commit=${1:?commit}
BOX=${IGNEUM_BOX_ROOT:-/srv}
R=$BOX/builds/release/igneum-new-wallet
X=$BOX/builds/release/wallet-target/x86_64-pc-windows-gnu/release
cd "$R" || exit 1
sha=$(git rev-parse HEAD); case "$sha" in "$commit"*) ;; *) echo "the release tree is at $sha, not $commit: run release-repro-build.sh $commit first"; exit 1;; esac
for f in igneum-wallet-engine.exe igneum-signer.exe igneum-wallet.exe; do [ -f "$X/$f" ] || { echo "missing $X/$f (run release-repro-build.sh $commit)"; exit 1; }; done
export SOURCE_DATE_EPOCH=$(git log -1 --format=%ct HEAD)
crate_ver=$(grep -m1 '^version' wallet/Cargo.toml | sed 's/.*"\(.*\)".*/\1/')
ver="$crate_ver-$commit"
out=$BOX/builds/release/wallet-win; rm -rf "$out"; mkdir -p "$out/igneum-windows-wallet/ui"
cp "$X/igneum-wallet-engine.exe" "$X/igneum-signer.exe" "$X/igneum-wallet.exe" "$out/igneum-windows-wallet/"
cp -R wallet/app/ui/. "$out/igneum-windows-wallet/ui/"; rm -f "$out/igneum-windows-wallet/ui/"*.test.mjs
cp site/design/icons/wallet/icon.ico "$out/igneum-windows-wallet/icon.ico"
cp wallet/packaging/devnet/igneum-wallet.cmd wallet/packaging/windows/stop-igneum-wallet.ps1 "$out/igneum-windows-wallet/"
echo "$ver" > "$out/igneum-windows-wallet/VERSION"
zip="$out/igneum-windows-wallet-$ver.zip"
python3 - "$out/igneum-windows-wallet" "$zip" "$SOURCE_DATE_EPOCH" <<'PY'
import os, sys, time, zipfile
src, dst, epoch = sys.argv[1], sys.argv[2], int(sys.argv[3])
stamp = time.gmtime(epoch)[:6]
with zipfile.ZipFile(dst, "w", zipfile.ZIP_DEFLATED) as z:
    for root, dirs, files in os.walk(src):
        dirs.sort()
        for n in sorted(files):
            p = os.path.join(root, n)
            arc = os.path.relpath(p, os.path.dirname(src))
            zi = zipfile.ZipInfo(arc, date_time=stamp)
            zi.compress_type = zipfile.ZIP_DEFLATED
            zi.external_attr = (0o755 if n.endswith((".exe", ".cmd")) else 0o644) << 16
            with open(p, "rb") as f:
                z.writestr(zi, f.read())
print("zipped", dst)
PY
s=$(sha256sum "$zip" | cut -c1-64)
echo "payload $(basename "$zip") $s $(stat -c %s "$zip") bytes"
echo "$s  $(basename "$zip")" > "$out/payload.sha256"
