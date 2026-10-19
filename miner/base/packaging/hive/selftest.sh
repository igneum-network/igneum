#!/usr/bin/env bash
# Self-test of the HiveOS package on a machine without Hive (the Mac): bash -n on every script, a package built from
# stub binaries, then the three hooks run the way Hive runs them (h-config.sh sourced with the Flight Sheet variables,
# h-run.sh with fake GPUs and a fake miner that prints the real miner's lines, h-stats.sh sourced and its JSON parsed).
# This proves the scripts' logic and the stats shape, not Hive itself: nothing here talks to a GPU or to Hive's agent.
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
T="$(mktemp -d)"
cleanup() {
  _pid="$(cat "$T/log/igneum.pid" 2>/dev/null)"; [[ -n "$_pid" ]] && kill -TERM "$_pid" 2>/dev/null; sleep 2
  pkill -KILL -f "$T/" 2>/dev/null || true
  [[ -n "${KEEP:-}" ]] && echo "kept $T" || rm -rf "$T"
}
trap cleanup EXIT
echo "== bash -n"
for f in "$HERE"/h-*.sh "$HERE"/make-hive-package.sh; do bash -n "$f" && echo "   $(basename "$f") ok"; done
echo "== package from stub binaries"
OUT="$T/build" VERSION=0.0.0-test "$HERE/make-hive-package.sh" --fake >/dev/null
tar -tzf "$T/build/igneum-hive-0.0.0-test.tar.gz" | sed 's/^/   /'
mkdir -p "$T/hive/miners/custom" "$T/log"
tar -C "$T/hive/miners/custom" -xzf "$T/build/igneum-hive-0.0.0-test.tar.gz"
M="$T/hive/miners/custom/igneum"
# the fake miner: records its argv, prints the real miner's start and STATUS lines, exits 42 once (the pack path)
cat > "$M/bin/igneum-miner" <<'FAKE'
#!/usr/bin/env bash
case "$1" in
  watch) exit 0 ;;
  export-pack) mkdir -p "$3"; echo "fake pack in $3"; exit 0 ;;
  mine)
    echo "$@" >> "$(dirname "$0")/../argv.log"
    fee=1; for ((i=1;i<=$#;i++)); do [[ "${!i}" == "--dev-fee" ]] && j=$((i+1)) && fee="${!j}"; done
    if [[ "$fee" == "0" ]]; then echo "t dev fee off (--dev-fee 0); the default is 1% (1 block in 100) to 0x1111111111111111111111111111111111111111"
    else echo "t dev fee ${fee}% (1 block in 100) to 0x1111111111111111111111111111111111111111; --dev-fee 0 turns it off"; fi
    n=0; while :; do n=$((n+1)); echo "t STATUS 'x' [worker]: ${n}0s jobs=$n accepted=$((n*2)) rejected=0 fee=$((n/50)) mismatched=0 extra=0 rate=0.10 blocks/s hash=120.00 MH/s wall (121.00 MH/s inside jobs) now=118.5 MH/s wall (119.0 MH/s inside jobs, 10 jobs, seed walk 0 calls) template_age=0.50s synced=true idle=1.0% (last 10s: 1.0%) queued=2 restarts=0 faults=0 identities=8 accepted_by_identity=1/1"; sleep 1; [[ $n == 2 && ! -f "$(dirname "$0")/../exited42" ]] && touch "$(dirname "$0")/../exited42" && exit 42; done ;;
esac
FAKE
chmod +x "$M/bin/igneum-miner"
cat > "$M/bin/igneumd" <<'FAKE'
#!/usr/bin/env bash
echo "fake igneumd $*"
for a in "$@"; do case "$a" in --override-params-file=*) echo "Finality rule v3 from the override file: active from checkpoint DAA score 135200" ;; esac; done
echo "Consensus params digest: 0139ab9dc2992d449ec787d8f021974933631eb55740ab4b6ce9d5c226e72888 (exchanged in the p2p handshake; a peer with another digest is refused)"
sleep 600
FAKE
chmod +x "$M/bin/igneumd"
mkdir -p "$T/bin"
printf '#!/bin/sh\n[ "$1" = NVIDIA ] && echo 2 || echo 0\n' > "$T/bin/gpu-detect"
cat > "$T/bin/gpu-stats" <<'GS'
#!/bin/sh
echo '{"temp":[61,58],"fan":[55,50],"busids":["01:00.0","02:00.0"]}'
GS
chmod +x "$T/bin/gpu-detect" "$T/bin/gpu-stats"
export PATH="$T/bin:$PATH"
echo "== h-config.sh (sourced, Flight Sheet variables)"
export CUSTOM_URL="local" CUSTOM_TEMPLATE="0xAbCd000000000000000000000000000000000001.rig7" CUSTOM_PASS="" CUSTOM_USER_CONFIG=$'DEV_FEE=0\nIDENTITIES=4' WORKER_NAME=rig7
export CUSTOM_CONFIG_FILENAME="$M/igneum.conf" CUSTOM_LOG_BASENAME="$T/log/igneum"
( . "$M/h-config.sh" ) || { echo "h-config.sh failed"; exit 1; }
grep -q '^WALLET=0xabcd000000000000000000000000000000000001$' "$M/igneum.conf" && grep -q '^DEV_FEE=0$' "$M/igneum.conf" && grep -q '^LABEL=rig7$' "$M/igneum.conf" && grep -q '^IDENTITIES=4$' "$M/igneum.conf" && echo "   conf ok: $(tr '\n' ' ' < "$M/igneum.conf" | cut -c1-160)"
( CUSTOM_TEMPLATE="notanaddress" . "$M/h-config.sh" >/dev/null 2>&1 ) && { echo "h-config.sh accepted a bad wallet"; exit 1; } || echo "   bad wallet refused ok"
echo "== h-config.sh IDENTITIES=auto"
# no GPU tool on the PATH (gpu-detect is not a VRAM source) and no amdgpu sysfs: every card falls back to 8
( CUSTOM_USER_CONFIG="IDENTITIES=auto" IGNEUM_DRM_ROOT="$T/no-drm" CUSTOM_CONFIG_FILENAME="$T/auto-none.conf" . "$M/h-config.sh" >/dev/null ) || { echo "h-config.sh failed on IDENTITIES=auto"; exit 1; }
grep -q '^IDENTITIES=8$' "$T/auto-none.conf" && ! grep -q '^IDENTITIES_GPU' "$T/auto-none.conf" && echo "   auto with no GPU tools: IDENTITIES=8, no per-card keys ok" || { echo "FAIL: auto without tools"; cat "$T/auto-none.conf"; exit 1; }
# the default is auto: no IDENTITIES line at all gives the same
( CUSTOM_USER_CONFIG="" IGNEUM_DRM_ROOT="$T/no-drm" CUSTOM_CONFIG_FILENAME="$T/auto-default.conf" . "$M/h-config.sh" >/dev/null ) && grep -q '^IDENTITIES=8$' "$T/auto-default.conf" && echo "   default (no IDENTITIES line) is auto ok" || { echo "FAIL: default not auto"; exit 1; }
# a stubbed nvidia-smi: 6 GB and 24 GB cards resolve to 2 and 8, in nvidia-smi order
printf '#!/bin/sh\nprintf "6144\\n24576\\n"\n' > "$T/bin/nvidia-smi"; chmod +x "$T/bin/nvidia-smi"
( CUSTOM_USER_CONFIG="IDENTITIES=auto" IGNEUM_DRM_ROOT="$T/no-drm" CUSTOM_CONFIG_FILENAME="$T/auto-nv.conf" . "$M/h-config.sh" >/dev/null ) || { echo "h-config.sh failed on stubbed nvidia-smi"; exit 1; }
grep -q '^IDENTITIES_GPU0=2$' "$T/auto-nv.conf" && grep -q '^IDENTITIES_GPU1=8$' "$T/auto-nv.conf" && grep -q '^IDENTITIES=8$' "$T/auto-nv.conf" && grep -q '^WORKER=auto$' "$T/auto-nv.conf" && echo "   auto with nvidia-smi 6144/24576: gpu0=2, gpu1=8 ok" || { echo "FAIL: auto by VRAM"; cat "$T/auto-nv.conf"; exit 1; }
# a number still overrides every card
( CUSTOM_USER_CONFIG="IDENTITIES=4" CUSTOM_CONFIG_FILENAME="$T/auto-num.conf" . "$M/h-config.sh" >/dev/null ) && grep -q '^IDENTITIES=4$' "$T/auto-num.conf" && ! grep -q '^IDENTITIES_GPU' "$T/auto-num.conf" && echo "   numeric override keeps IDENTITIES=4 ok" || { echo "FAIL: numeric override"; exit 1; }
rm -f "$T/bin/nvidia-smi"
echo "== h-config.sh OVERRIDE"
OV='{"difficulty_v2_activation_daa":33000,"proving_v0_activation_daa":84100,"fees_v1_activation_daa":210000,"finality_v3_activation_daa":135200}'
( CUSTOM_USER_CONFIG="DEV_FEE=0 VOTE=1"$'\n'"OVERRIDE='$OV'"$'\n'"IDENTITIES=4" CUSTOM_CONFIG_FILENAME="$T/ov.conf" . "$M/h-config.sh" >/dev/null ) || { echo "h-config.sh failed on OVERRIDE"; exit 1; }
( . "$T/ov.conf"; [[ "$OVERRIDE" == "$OV" && "$DEV_FEE" == 0 && "$IDENTITIES" == 4 ]] ) && echo "   OVERRIDE (single-quoted, own line) sourced back intact beside the other keys ok" || { echo "FAIL: OVERRIDE round trip"; cat "$T/ov.conf"; exit 1; }
( CUSTOM_USER_CONFIG="OVERRIDE=notjson" CUSTOM_CONFIG_FILENAME="$T/ov-bad.conf" . "$M/h-config.sh" >/dev/null 2>&1 ) && { echo "FAIL: OVERRIDE=notjson accepted"; exit 1; } || echo "   OVERRIDE that is not {...} refused ok"
( CUSTOM_USER_CONFIG="" CUSTOM_CONFIG_FILENAME="$T/ov-none.conf" . "$M/h-config.sh" | grep -q "override NONE" ) && grep -q "^OVERRIDE=''$" "$T/ov-none.conf" && echo "   no OVERRIDE: empty in the conf and named in the summary ok" || { echo "FAIL: empty OVERRIDE"; exit 1; }
# h-run.sh reads IDENTITIES_GPU<N> first: write a conf with gpu1=3 and check the second miner's argv
( CUSTOM_USER_CONFIG=$'DEV_FEE=0\nIDENTITIES=auto\n'"OVERRIDE=$OV" IGNEUM_DRM_ROOT="$T/no-drm" CUSTOM_CONFIG_FILENAME="$M/igneum.conf" . "$M/h-config.sh" >/dev/null ) && printf 'IDENTITIES_GPU1=3\n' >> "$M/igneum.conf"
echo "== h-run.sh (fake GPUs: 2 NVIDIA, fake node, fake miner)"
( cd "$M" && CUSTOM_CONFIG_FILENAME="$M/igneum.conf" CUSTOM_LOG_BASENAME="$T/log/igneum" ./h-run.sh > "$T/log/run.out" 2>&1 ) &
disown
sleep 8
ls "$T/log" | sed 's/^/   /'
_lines=$(grep -c "dev fee off (--dev-fee 0)" "$T/log/igneum.log" || true); echo "   dev fee lines in the main log: $_lines"; [[ "$_lines" -ge 2 ]] || { echo "FAIL: expected the two miners' dev fee lines in the main log"; cat "$T/log/run.out"; exit 1; }
grep -q -- "--dev-fee 0" "$M/argv.log" && echo "   DEV_FEE=0 reached the miner as --dev-fee 0 ok" || { echo "FAIL: --dev-fee 0 missing"; exit 1; }
grep -q -- "rig7-gpu0 .*--identities 8" "$M/argv.log" && echo "   gpu0 took the IDENTITIES=8 fallback ok" || { echo "FAIL: --identities 8 missing on gpu0"; cat "$M/argv.log"; exit 1; }
grep -q -- "rig7-gpu1 .*--identities 3" "$M/argv.log" && echo "   gpu1 took IDENTITIES_GPU1=3 over the fallback ok" || { echo "FAIL: --identities 3 missing on gpu1"; cat "$M/argv.log"; exit 1; }
[[ "$(cat "$M/data/override-params.json")" == "$OV" ]] && echo "   data/override-params.json written from OVERRIDE ok" || { echo "FAIL: override file"; exit 1; }
grep -q -- "--override-params-file=$M/data/override-params.json" "$T/log/igneum.node.log" && echo "   the node got --override-params-file ok" || { echo "FAIL: node flag"; cat "$T/log/igneum.node.log"; exit 1; }
grep -q "node: Consensus params digest: 0139ab9d" "$T/log/igneum.log" && grep -q "node: Finality rule v3 from the override file" "$T/log/igneum.log" && echo "   the node's digest and switch lines reached the main log ok" || { echo "FAIL: digest relay"; cat "$T/log/igneum.log"; exit 1; }
grep -q "igneum-worker-cuda" "$M/argv.log" && echo "   NVIDIA cards got the cuda worker ok" || { echo "FAIL: cuda worker missing"; exit 1; }
[[ -f "$M/exited42" && $(grep -c "^mine" "$M/argv.log") -ge 3 ]] && echo "   exit 42 restarted the miner and re-exported the pack ok" || { echo "FAIL: no restart after exit 42"; exit 1; }
echo "== h-stats.sh (sourced)"
( . "$M/h-stats.sh"; echo "   khs=$khs"; echo "   stats=$stats"; python3 -c "import json,sys; s=json.loads(sys.argv[1]); assert s['hs_units']=='khs' and len(s['hs'])==2 and s['ar'][0]>0 and s['algo']=='igneum' and len(s['temp'])==2, s; print('   stats JSON ok: hs', s['hs'], 'temp', s['temp'], 'ar', s['ar'], 'bus', s['bus_numbers'])" "$stats" )
echo "== h-run.sh without OVERRIDE (the warning)"
( CUSTOM_USER_CONFIG=$'DEV_FEE=0' IGNEUM_DRM_ROOT="$T/no-drm" CUSTOM_CONFIG_FILENAME="$T/noov.conf" . "$M/h-config.sh" >/dev/null )
mkdir -p "$T/log2"
( cd "$M" && CUSTOM_CONFIG_FILENAME="$T/noov.conf" CUSTOM_LOG_BASENAME="$T/log2/igneum" ./h-run.sh > "$T/log2/run.out" 2>&1 ) &
disown
sleep 4
grep -q "WARNING: no OVERRIDE in the Flight Sheet" "$T/log2/igneum.log" && ! grep -q -- "--override-params-file" "$T/log2/igneum.node.log" && echo "   no OVERRIDE: warning in the main log, no flag on the node ok" || { echo "FAIL: missing warning"; cat "$T/log2/igneum.log"; exit 1; }
_p2="$(cat "$T/log2/igneum.pid" 2>/dev/null)"; [[ -n "$_p2" ]] && kill -TERM "$_p2" 2>/dev/null; sleep 1
echo "== self-test passed (scripts and stats shape; Hive itself is untested)"
