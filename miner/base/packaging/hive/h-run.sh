#!/usr/bin/env bash
# Ember for HiveOS: starts the bundled node when asked, exports the hourly program pack, then one igneum-miner
# per GPU with the matching worker (igneum-worker-cuda on NVIDIA, igneum-worker-opencl on AMD). Each miner mines
# against the node for its own payout address; the software dev fee (1 template in 100) is the miner's default and
# DEV_FEE=0 in the Flight Sheet's extra config turns it off. Per-GPU logs: $CUSTOM_LOG_BASENAME.gpu<N>.log; the main
# log is their merged tail. Run by Hive with the working directory at the miner folder.
cd "$(dirname "$0")" || exit 1
HERE="$(pwd)"
[[ -z "$CUSTOM_LOG_BASENAME" || -z "$CUSTOM_CONFIG_FILENAME" ]] && . "$HERE/h-manifest.conf"
[[ -z "$CUSTOM_LOG_BASENAME" ]] && echo "No CUSTOM_LOG_BASENAME is set" && exit 1
[[ -f "$CUSTOM_CONFIG_FILENAME" ]] || { echo "No config at $CUSTOM_CONFIG_FILENAME (apply the Flight Sheet first)"; exit 1; }
. "$CUSTOM_CONFIG_FILENAME"
BIN="${IGNEUM_BIN:-$HERE/bin}"
mkdir -p "$(dirname "$CUSTOM_LOG_BASENAME")" "$HERE/packs" "$HERE/data"
date +%s > "$CUSTOM_LOG_BASENAME.started"
MAIN="$CUSTOM_LOG_BASENAME.log"
: > "$MAIN"
say() { printf '%s %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*" | tee -a "$MAIN"; }
echo $$ > "$CUSTOM_LOG_BASENAME.pid"
pids=()
# the open-files limit (the coordinator's one-variable test, 9 October 2026: the same package walled on the first cut's
# records under HiveOS's default soft limit of 1,024 and passed under 524,288): raised here, best effort, read back from the
# node's own /proc limits once it runs; the line says both so the operator sees a rig whose hard limit is lower.
NOFILE_WANT=524288; NOFILE_BEFORE=$(ulimit -n); ulimit -n "$NOFILE_WANT" 2>/dev/null || ulimit -n "$(ulimit -Hn)" 2>/dev/null || true
say "open files: soft limit $NOFILE_BEFORE before, $(ulimit -n) now (hard $(ulimit -Hn)); the node needs $NOFILE_WANT"
# every process this script starts is ended by the pid it recorded (pids[] for the node and the per-GPU loops, the
# .gpuN.pid files for the miners the loops start); no kill by name or by parent anywhere (rule 1, 8 October 2026)
cleanup() {
  say "stopping"
  local mp; for f in "$CUSTOM_LOG_BASENAME".gpu*.pid; do [[ -f "$f" ]] && { mp=$(cat "$f" 2>/dev/null); [[ -n "$mp" ]] && kill -TERM "$mp" 2>/dev/null; }; done
  for p in "${pids[@]}"; do kill -TERM "$p" 2>/dev/null; done
  sleep 2
  for f in "$CUSTOM_LOG_BASENAME".gpu*.pid; do [[ -f "$f" ]] && { mp=$(cat "$f" 2>/dev/null); [[ -n "$mp" ]] && kill -KILL "$mp" 2>/dev/null; rm -f "$f"; }; done
  for p in "${pids[@]}"; do kill -KILL "$p" 2>/dev/null; done
  exit 0
}
trap cleanup INT TERM

# 1. the node (in pool mode, NODE_URL is "none" unless the Flight Sheet asked for a verifier node with NODE=)
POOL_URL="${POOL_URL:-}"
POOL_TLS="${POOL_TLS:-0}"
POOL_PIN="${POOL_PIN:-}"
if [[ "$NODE_URL" == "local" ]]; then
  NODE_URL="grpc://127.0.0.1:26610"
  peers=()
  if [[ -n "$PEERS" ]]; then IFS=',' read -r -a plist <<< "$PEERS"; for p in "${plist[@]}"; do peers+=("--addpeer=$p"); done
  else peers+=("--addpeer=213.173.98.75:21169" "--addpeer=213.173.110.229:38511" "--addpeer=213.173.108.150:12524" "--addpeer=188.40.146.49:26631" "--addpeer=188.40.146.49:26671"); fi   # the Igneum 2.0 devnet seeds (packaging/mac/packaged-config.sh NODE_PEERS)
  # the consensus override (OVERRIDE in the Flight Sheet's extra config, written to igneum.conf by h-config.sh): the
  # devnet's activation heights. A node without it runs on genesis parameters, prints another digest in the p2p
  # handshake and is refused by every devnet peer (release an earlier release, section 5, C41).
  override=()
  if [[ -n "${OVERRIDE:-}" ]]; then
    printf '%s\n' "$OVERRIDE" > "$HERE/data/override-params.json"
    override=("--override-params-file=$HERE/data/override-params.json")
    say "consensus override from the Flight Sheet: $OVERRIDE"
  else
    say "WARNING: no OVERRIDE in the Flight Sheet; the node runs on genesis parameters and every devnet peer will refuse it. Set OVERRIDE from the downloads page."
  fi
  say "starting the bundled node (the Igneum 2.0 devnet, suffix ${DEVNET_SUFFIX:-4}, data $HERE/data, peers ${peers[*]#--addpeer=})"
  "$BIN/igneumd" --devnet "--devnet-suffix=${DEVNET_SUFFIX:-4}" "--appdir=$HERE/data" --rpclisten=127.0.0.1:26610 --listen=0.0.0.0:26611 "${peers[@]}" \
    ${override[@]+"${override[@]}"} --nodnsseed --disable-upnp --nologfiles --yes >> "$CUSTOM_LOG_BASENAME.node.log" 2>&1 &
  pids+=($!); NODE_PID=$!; echo "$NODE_PID" > "$CUSTOM_LOG_BASENAME.node.pid"
  sleep 1; [[ -r "/proc/$NODE_PID/limits" ]] && say "node pid $NODE_PID open files: $(grep -i 'open files' "/proc/$NODE_PID/limits" | awk '{print "soft " $4 ", hard " $5}')"
fi
if [[ "$NODE_URL" != "none" ]]; then
  say "node $NODE_URL; waiting for it to answer"
  for _ in $(seq 1 60); do
    "$BIN/igneum-miner" watch 1 "$NODE_URL" >/dev/null 2>&1 && break
    sleep 2
  done
fi
if [[ -f "$CUSTOM_LOG_BASENAME.node.log" ]]; then
  # the switch lines and the digest the node printed at start, so the operator can compare the digest with the one
  # on the downloads page (another digest = refused by every peer)
  n=0
  while IFS= read -r l; do say "node: $l"; n=$((n + 1)); done < <(grep -E 'override params file|from the override file|Consensus params digest' "$CUSTOM_LOG_BASENAME.node.log" | head -12)
  [[ $n == 0 ]] && say "node: no digest line yet in $CUSTOM_LOG_BASENAME.node.log (an older node, or not started); check it by hand"
fi
[[ -n "$POOL_URL" ]] && say "pool mode: shares to $POOL_URL, payout $WALLET, worker $LABEL-gpuN, verifier node $NODE_URL (the vote key stays this rig's; no software dev fee)"

# 1b. the sync gate (main's ruling, 9 October 2026 05:3x UK): nothing mines until the node reports synced within ten blocks
# of the tip its peers hold. Before this gate the pack export and the miners started the moment the node answered, so a
# fresh rig mined its own genesis-side tip during IBD, islanded on its own unproven records 3,686 and 3,687 and never
# joined the chain (lp-4090-43, hive-1 and hive-2, 9 October 2026); the same node under the app's supervisor, which holds
# its miners until synced, joined. The wait is bounded (SYNC_WAIT_S in the Flight Sheet's extra config, default 7200 s);
# past it the script exits 1 with the reason and Hive's watchdog restarts it on the kept datadir (the sync resumes).
# UNSYNCED_MINING=1 skips the gate (a private rig with no peers), said in the log.
sync_reading() { "$BIN/igneum-miner" watch 1 "$NODE_URL" 2>/dev/null | grep -E 'blocks=' | tail -1; }
sync_gate() {
  [[ "$NODE_URL" == "none" ]] && { say "sync gate: no node (pool mode without a verifier); nothing to wait for"; return 0; }
  [[ "${UNSYNCED_MINING:-0}" == "1" ]] && { say "sync gate: skipped by UNSYNCED_MINING=1 in the Flight Sheet (a private rig); the miners start on an unsynced node"; return 0; }
  local bound="${SYNC_WAIT_S:-7200}" t0 line blocks headers peers synced lag
  t0=$(date +%s)
  say "sync gate: the miners wait until the node is synced within ten blocks of its peers' tip (bound ${bound} s)"
  while :; do
    line=$(sync_reading)
    blocks=$(sed -nE 's/.*(^|[^a-z_])blocks=([0-9]+).*/\2/p' <<< "$line"); headers=$(sed -nE 's/.*(^|[^a-z_])headers=([0-9]+).*/\2/p' <<< "$line")
    peers=$(sed -nE 's/.*(^|[^a-z_])peers=([0-9]+).*/\2/p' <<< "$line"); synced=$(sed -nE 's/.*(^|[^a-z_])synced=(true|false).*/\2/p' <<< "$line")
    blocks=${blocks:-0}; headers=${headers:-0}; peers=${peers:-0}; synced=${synced:-false}; lag=$((headers - blocks))
    if [[ "$synced" == "true" && "$peers" -gt 0 && "$blocks" -gt 1 && "$lag" -le 10 ]]; then
      say "sync gate: synced within ten blocks of the peers' tip (blocks $blocks, headers $headers, $peers peers) after $(( $(date +%s) - t0 )) s; the miners start"
      return 0
    fi
    if (( $(date +%s) - t0 >= bound )); then
      say "sync gate: not synced after ${bound} s (blocks $blocks, headers $headers, lag $lag, $peers peers, synced=$synced); the miners do not start; exiting for Hive's restart on the kept datadir"
      return 1
    fi
    say "sync gate: waiting (blocks $blocks, headers $headers, lag $lag, $peers peers, synced=$synced, $(( $(date +%s) - t0 )) s)"
    sleep 30
  done
}
sync_gate || { cleanup; exit 1; }

# 2. the GPUs: Hive's gpu-detect when present, else the driver tools
nv=0; amd=0
if command -v gpu-detect >/dev/null 2>&1; then nv=$(gpu-detect NVIDIA 2>/dev/null || echo 0); amd=$(gpu-detect AMD 2>/dev/null || echo 0)
else
  command -v nvidia-smi >/dev/null 2>&1 && nv=$(nvidia-smi -L 2>/dev/null | grep -c '^GPU')
  [[ -d /sys/bus/pci/drivers/amdgpu ]] && amd=$(ls /sys/bus/pci/drivers/amdgpu 2>/dev/null | grep -c '^0000:')
fi
nv=${nv:-0}; amd=${amd:-0}
say "GPUs: $nv NVIDIA, $amd AMD (worker setting: $WORKER)"
[[ "$nv" == "0" && "$amd" == "0" ]] && say "no GPU found; nothing to mine with" && sleep 30 && exit 1

# 3. the hourly program pack from the node (the workers read it with --pack; the miner writes the next one to packs/prepare)
export_pack() { [[ "$NODE_URL" == "none" ]] && return 0; rm -rf "$HERE/packs/devnet"; "$BIN/igneum-miner" export-pack "$NODE_URL" "$HERE/packs/devnet" >> "$MAIN" 2>&1; }
export_pack || say "pack export failed; the miners retry"

# 4. one miner per GPU, restarted on exit (exit 42 = the program changed and the worker cannot prepare: re-export the pack)
run_gpu() {
  local idx="$1" worker="$2" dev="$3" tag="$4"
  local log="$CUSTOM_LOG_BASENAME.gpu$idx.log"
  local args=(mine "$NODE_URL" 1 100000000 "$LABEL-gpu$idx" --worker "$BIN/$worker" --worker-args "--device $dev --pack packs/devnet"
    --prepare-packs packs/prepare --exit-on-seed-change --evm-address "$WALLET" --payout-label "$LABEL-gpu$idx" --status-secs 30)
  if [[ -n "$POOL_URL" ]]; then
    # pool mode: one vote key per operator (spec 9.6), the worker name labels the card, no software dev fee
    args+=(--pool "$POOL_URL" --worker-name "$LABEL-gpu$idx")
    # spec 9.3: TLS 1.3 to the pool (pools:// in the Flight Sheet), or a pinned self-signed certificate (POOL_PIN=)
    if [[ -n "$POOL_PIN" ]]; then args+=(--pool-pin "$POOL_PIN"); elif [[ "$POOL_TLS" == "1" ]]; then args+=(--pool-tls); fi
  else
    local identv="IDENTITIES_GPU$idx" ident="$IDENTITIES"; [[ -n "${!identv:-}" ]] && ident="${!identv}"   # per-card from h-config.sh, else the fallback
    [[ "$ident" -gt 1 ]] && args+=(--identities "$ident")
    [[ "$DEV_FEE" != "1" ]] && args+=(--dev-fee "$DEV_FEE")
  fi
  [[ "$VOTE" == "0" ]] && args+=(--no-vote)
  [[ -n "$EXTRA" ]] && args+=($EXTRA)
  while :; do
    echo "$(date -u +%Y-%m-%dT%H:%M:%SZ) gpu$idx ($tag $dev): igneum-miner ${args[*]}" >> "$log"
    "$BIN/igneum-miner" "${args[@]}" >> "$log" 2>&1 &
    local mp=$!; echo "$mp" > "$CUSTOM_LOG_BASENAME.gpu$idx.pid"; wait "$mp"; rc=$?; rm -f "$CUSTOM_LOG_BASENAME.gpu$idx.pid"
    if [[ $rc == 42 ]]; then export_pack; else sleep 5; fi
  done
}
idx=0
if [[ "$nv" -gt 0 && "$WORKER" != "opencl" ]]; then
  for ((d = 0; d < nv; d++)); do run_gpu "$idx" igneum-worker-cuda "$d" nvidia & pids+=($!); idx=$((idx + 1)); done
fi
if [[ "$amd" -gt 0 && "$WORKER" != "cuda" ]]; then
  for ((d = 0; d < amd; d++)); do run_gpu "$idx" igneum-worker-opencl "$d" amd & pids+=($!); idx=$((idx + 1)); done
fi
say "$idx miner(s) started; dev fee ${DEV_FEE}% (the miner prints its own dev fee line below)"

# 5. the main log: every GPU's lines, merged
sleep 1
tail -q -n +1 -F "$CUSTOM_LOG_BASENAME".gpu*.log 2>/dev/null | tee -a "$MAIN" &
pids+=($!)
wait
