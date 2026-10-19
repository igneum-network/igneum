#!/usr/bin/env bash
# The reproducible build of the wallet's devnet release artefacts on a Linux box (the two-builder rule of
# docs/reliability/DESIGN.md section 4, the form of tools/release-repro-build.sh for Ember): the same commit, the same
# toolchain, the same checkout path on every box, the paths remapped out of the binaries, the commit's time as
# SOURCE_DATE_EPOCH, the lockfile locked, the archives with sorted names, one mtime, numeric owners and gzip without a
# name or a time. Two packages: Linux x86-64 (the engine, the signer, the CLI, the page, the launcher) and Windows
# x86-64 (the same three as .exe, cross-built with mingw, the page, a .cmd launcher). Prints the shas and writes the
# manifest; with --put appends the artefacts and the manifest to the Phase Zero store under product-b-wallet.
#   usage: release-repro-build.sh <commit> [--put]
set -u
commit=${1:?commit}; put=${2:-}
BOX=${IGNEUM_BOX_ROOT:-/srv}
B1=${IGNEUM_BUILDER_HOST:?the builder host (user@host), from outside the tree}
R=$BOX/builds/release/igneum-new-wallet
mkdir -p "$BOX/builds/release"
export GIT_SSH_COMMAND="ssh -o BatchMode=yes -o StrictHostKeyChecking=no"
if [ ! -d "$R/.git" ]; then
  if [ -d "$BOX/git/igneum-new.git" ]; then git clone -q "$BOX/git/igneum-new.git" "$R"; else git clone -q "$B1:$BOX/git/igneum-new.git" "$R"; fi || { echo "no clone"; exit 1; }
fi
cd "$R" || exit 1
git fetch -q origin "+refs/heads/*:refs/remotes/origin/*" 2>/dev/null
# the box's mirror lags the push: the wallet team's branch straight from builder one
git fetch -q "$B1:$BOX/git/igneum-new.git" "+refs/heads/*:refs/remotes/b1/*" 2>&1 | tail -1   # every branch: a cut may sit on a box branch of its own (the 0.0.6 re-cut of 12 October 2026)
# a box without a key to builder one and a mirror that carries main but lags the branch (11 October 2026): the commit
# arrives as a git bundle (scp from the push host) named in BUNDLE
[ -n "${BUNDLE:-}" ] && [ -f "$BUNDLE" ] && git fetch -q "$BUNDLE" "+refs/heads/*:refs/remotes/bundle/*" 2>&1 | tail -1
git checkout -q --detach "$commit" || { echo "no commit $commit"; exit 1; }
# the commit given is a prefix of the full hash (git's own short form grew to nine characters on 12 October 2026 and
# the eight-character comparison refused a right tree)
sha=$(git rev-parse HEAD); case "$sha" in "$commit"*) ;; *) echo "tree $sha is not $commit"; exit 1;; esac
[ -f wallet/Cargo.lock ] || { echo "wallet/Cargo.lock is missing at $sha: the locked build needs it"; exit 1; }
export SOURCE_DATE_EPOCH=$(git log -1 --format=%ct HEAD)
export RUSTFLAGS="--remap-path-prefix=$R=/src --remap-path-prefix=$HOME/.cargo=/cargo --remap-path-prefix=$(rustc --print sysroot)=/rust"
# the manifest's copy of the flags names the three prefixes by role, never by the host's paths (identical on every builder)
MAN_RUSTFLAGS="--remap-path-prefix=<the proof host's checkout>=/src --remap-path-prefix=<the proof host's cargo home>=/cargo --remap-path-prefix=<the proof host's toolchain>=/rust"
export CARGO_TARGET_DIR=$BOX/builds/release/wallet-target
export CC_x86_64_pc_windows_gnu=x86_64-w64-mingw32-gcc AR_x86_64_pc_windows_gnu=x86_64-w64-mingw32-ar CARGO_TARGET_X86_64_PC_WINDOWS_GNU_LINKER=x86_64-w64-mingw32-gcc
# the PE header's TimeDateStamp written as zero: the two builders disagreed on every Windows binary on 12 October 2026
# (one builder's stamp the SOURCE_DATE_EPOCH, the other's the wall clock; the same binutils on both), so the linker is told
# to insert none and the archives agree whatever the linker makes of the epoch
export CARGO_TARGET_X86_64_PC_WINDOWS_GNU_RUSTFLAGS="$RUSTFLAGS -C link-arg=-Wl,--no-insert-timestamp"
cd wallet
rm -rf "$CARGO_TARGET_DIR"
cargo build --release --locked -p igneum-wallet-engine -p igneum-signer -p igneum-wallet-cli 2>&1 | grep -E "^error" -A6 | head -12
rustup target add x86_64-pc-windows-gnu >/dev/null 2>&1
cargo build --release --locked -p igneum-wallet-engine -p igneum-signer -p igneum-wallet-cli --target x86_64-pc-windows-gnu 2>&1 | grep -E "^error" -A6 | head -12
L=$CARGO_TARGET_DIR/release; X=$CARGO_TARGET_DIR/x86_64-pc-windows-gnu/release
for f in "$L/igneum-wallet-engine" "$L/igneum-signer" "$L/igneum-wallet" "$X/igneum-wallet-engine.exe" "$X/igneum-signer.exe" "$X/igneum-wallet.exe"; do [ -f "$f" ] || { echo "missing $f"; exit 1; }; done
# the version is the crate's (wallet/Cargo.toml, the workspace's one line): the binaries say it with --version and
# the updater compares it with the host's latest manifest, so a cut and its packages carry the same number
crate_ver=$(grep -m1 '^version' "$R/wallet/Cargo.toml" | sed 's/.*"\(.*\)".*/\1/')
ver="$crate_ver-$commit"; out=$BOX/builds/release/wallet-pkg; rm -rf "$out"; mkdir -p "$out"
# the Linux package
name="igneum-wallet-linux-x86_64-$ver"; mkdir -p "$out/$name/bin" "$out/$name/ui"
cp "$L/igneum-wallet-engine" "$L/igneum-signer" "$L/igneum-wallet" "$out/$name/bin/"
cp -R app/ui/. "$out/$name/ui/"; rm -f "$out/$name/ui/"*.test.mjs
cp packaging/devnet/igneum-wallet.sh packaging/devnet/README.md "$out/$name/"
# the wallet's icon set (the design lead's site/design/icons/wallet, main 199fed68: the same mark as Ember's): the
# .desktop entry and the hosts read it from the package's icons/
mkdir -p "$out/$name/icons"; cp "$R"/site/design/icons/wallet/icon-512.png "$R"/site/design/icons/wallet/icon-1024.png "$R"/site/design/icons/wallet/icon.icns "$R"/site/design/icons/wallet/icon.ico "$out/$name/icons/"
chmod 755 "$out/$name/bin/"* "$out/$name/igneum-wallet.sh"
echo "$ver" > "$out/$name/VERSION"
# the Windows package
wname="igneum-wallet-windows-x86_64-$ver"; mkdir -p "$out/$wname/bin" "$out/$wname/ui"
cp "$X/igneum-wallet-engine.exe" "$X/igneum-signer.exe" "$X/igneum-wallet.exe" "$out/$wname/bin/"
cp -R app/ui/. "$out/$wname/ui/"; rm -f "$out/$wname/ui/"*.test.mjs
cp packaging/devnet/igneum-wallet.cmd packaging/devnet/README.md "$out/$wname/"
mkdir -p "$out/$wname/icons"; cp "$R"/site/design/icons/wallet/icon-512.png "$R"/site/design/icons/wallet/icon-1024.png "$R"/site/design/icons/wallet/icon.ico "$out/$wname/icons/"
echo "$ver" > "$out/$wname/VERSION"
TARFLAGS="--sort=name --mtime=@$SOURCE_DATE_EPOCH --owner=0 --group=0 --numeric-owner --mode=u+rw,go+r-w,a+X --pax-option=exthdr.name=%d/PaxHeaders/%f,delete=atime,delete=ctime"
( cd "$out" && tar $TARFLAGS -cf - "$name" | gzip -n -9 > "$name.tar.gz" && tar $TARFLAGS -cf - "$wname" | gzip -n -9 > "$wname.tar.gz" )
# the builder by its ROLE (the project's word, 16 October 2026): builder-a or builder-b from the environment, never the box's name in a served file
box=${IGNEUM_BUILDER_ROLE:?the builder role (builder-a or builder-b), from outside the tree}
cd "$out"
l=$(sha256sum "$name.tar.gz" | cut -c1-64); w=$(sha256sum "$wname.tar.gz" | cut -c1-64)
eb=$(sha256sum "$L/igneum-wallet-engine" | cut -c1-64); sb=$(sha256sum "$L/igneum-signer" | cut -c1-64); cb=$(sha256sum "$L/igneum-wallet" | cut -c1-64)
ex=$(sha256sum "$X/igneum-wallet-engine.exe" | cut -c1-64); sx=$(sha256sum "$X/igneum-signer.exe" | cut -c1-64); cx=$(sha256sum "$X/igneum-wallet.exe" | cut -c1-64)
# the default chain of this cut: the public test network, chain 7322 (from 0.0.16; chain 7320 the old public test network), its genesis from the pin
genesis=$(grep -o 'GENESIS_7322: &str = "0x[0-9a-f]*"' "$R/wallet/core/src/chain.rs" | grep -o '0x[0-9a-f]*')
man="igneum-wallet-$ver.manifest-$box.json"
cat > "$man" <<EOF
{"product":"igneum-wallet","version":"$ver","commit":"$(git -C "$R" rev-parse HEAD)","builder":"$box","built_utc":"$(date -u +%FT%TZ)","source_date_epoch":$SOURCE_DATE_EPOCH,
 "chain":{"name":"Igneum public test network","chain_id":7322,"network_id":7394,"genesis":"$genesis","rpc":"https://rpc.igneum.network","submit":"https://rpc.igneum.network (the public name takes raw sends)","explorer":"https://explorer.igneum.network","faucet":"0x93482fa8a07bba7d3cc33058dd4f51a8a30a66b4","bond_faucet":"0xf7649aaa2999402d69444470c5b56d136b1365cb","faucet_source":"dev/chain/7322-faucets.json (the DevFaucet at block 207, the BondFaucet at 208; the chains file's 7322 entry)","value":"none: the public test network with a test-coin faucet; the fresh wallet's default; chain 7320 the old public test network; chains 7319, 7317 and 7315 retired development chains, named pins"},
 "toolchain":{"rustc":"$(rustc --version)","cargo":"$(cargo --version)","mingw":"$(x86_64-w64-mingw32-gcc --version | head -1)"},
 "rustflags":"$MAN_RUSTFLAGS","checkout":"the proof host's checkout, remapped to /src (the id rule of 17 October 2026: a served manifest names no host path)","lockfile":"locked",
 "artefacts":{"$name.tar.gz":"$l","$wname.tar.gz":"$w"},
 "binaries":{"linux":{"igneum-wallet-engine":"$eb","igneum-signer":"$sb","igneum-wallet":"$cb"},"windows":{"igneum-wallet-engine.exe":"$ex","igneum-signer.exe":"$sx","igneum-wallet.exe":"$cx"}},
 "isolation_rows":{"ISO-01":"PASS","ISO-02":"PASS","ISO-03":"PASS","ISO-04-host-sandbox":"PENDING","record":"the wallet team's runs page, 11 Oct 2026 10:42; the proof host's isolation work directory"}}
EOF
echo "box $box commit $sha epoch $SOURCE_DATE_EPOCH genesis $genesis"
echo "linux    $l  $name.tar.gz"; echo "windows  $w  $wname.tar.gz"
echo "engine   $eb  signer $sb  cli $cb"; echo "engine.exe $ex  signer.exe $sx  cli.exe $cx"
echo "manifest $out/$man"
if [ "$put" = "--put" ] && command -v igneum-artefact-put >/dev/null 2>&1; then
  for f in "$name.tar.gz" "$wname.tar.gz" "$man"; do igneum-artefact-put phase-zero product-b-wallet "$out/$f" 2>&1 | tail -1; done
fi
