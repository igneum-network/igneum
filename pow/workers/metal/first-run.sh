#!/bin/bash
# first-run.sh: the Metal worker's cells on a Mac, as one pasted command.
#
#   bash first-run.sh [exactness|screen|memory|jobd|all] [candidate]
#   with the repository already cloned:  IGNEUM_SRC=~/igneum-new bash pow/workers/metal/first-run.sh screen
#
# Under the current user with no elevated rights: checks the command-line tools, fetches
# the repository (mirror over ssh with the user's key, or IGNEUM_SRC), builds GPU engineer
# A's reference, its vectors tool and the Metal worker with clang alone (no Homebrew, no
# cmake), then runs the mode: exactness (the HW-01 classes), screen (the benches at the
# launch epoch, the matched point and the horizon), memory (the memory-requirement
# record), jobd (a 120 s smoke through A's jobd.py with shares verified by the reference)
# or all. Everything lands under ~/igneum-bench/<date>/ with a summary; with a store key
# at ~/igneum-bench/store.key the files go to the Phase Zero store under
# <candidate>__<model>__<measure>. Every long step writes its pid under pids/.
# Builds happen on this Mac only (the founder's word of 9 Oct). GPU engineer B, 9 Oct 2026.
set -u
MODE=${1:-exactness}
CAND=${2:-kawpow-0.9.4}
CANDTAG=$(echo "$CAND" | sed 's/kawpow-0\.9\.4/kawpow-094/; s/fishhash-1/fishhash-1/; s/autolykos-2/autolykos-2/')
# IGM_PARAMS: the candidate's params document (IgneumHash: the variant). The cell name carries
# the variant tag (igneumhash-<variant>__<model>__<measure>) and the vectors come from
# pow/vectors/igneumhash/<variant>/.
PARAMS=${IGM_PARAMS:-}
PARGS=(); [ -n "$PARAMS" ] && PARGS=(--params "$PARAMS")
VARIANT=""
if [ "$CAND" = igneumhash ]; then
  VARIANT=$(python3 - "$PARAMS" <<'EOV'
import json, sys
d = json.loads(sys.argv[1] or "{}") or {}
v = int(d.get("version", 1)); dm = int(d.get("dataset_mib", 1024 if v else 256)); sk = int(d.get("scratch_kib", 0 if v else 16)); r = int(d.get("rounds", 64))
print("v1-d%d-s%d-r%d-k%d-p%d" % (dm, sk, r, int(d.get("cache_mib", 32)), int(d.get("parents", 256))) if v else "d%d-s%d-r%d-c%d" % (dm, sk, r, int(d.get("item_chain", 1))))
EOV
)
  CANDTAG="igneumhash-$VARIANT"
fi
ROOT=~/igneum-bench
RUN=$ROOT/$(date +%Y%m%dT%H%M%S)
mkdir -p "$RUN" "$ROOT/pids"
log() { echo "[$(date '+%H:%M:%S')] $*" | tee -a "$RUN/log.txt"; }
MODEL=$(sysctl -n hw.model | tr -d ',' | tr 'A-Z' 'a-z')
STORE=${IGNEUM_STORE:-}   # the artefact store as user@host, from outside the tree; empty: nothing is pushed
push() { # cell name file
  [ -n "$STORE" ] && [ -f "$ROOT/store.key" ] || return 0
  ssh -o BatchMode=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -i "$ROOT/store.key" "$STORE" igneum-artefact-put phase-zero "$1" "$2" < "$3" >/dev/null 2>&1 && log "stored $1/$2" || log "store failed for $1/$2"
}

if ! xcode-select -p >/dev/null 2>&1; then
  log "Xcode command-line tools are missing: run   xcode-select --install   then paste the command again"
  exit 2
fi
CXXBIN=$(xcrun -f clang++ 2>/dev/null) || { log "clang++ not found through xcrun"; exit 2; }
SDK=$(xcrun --show-sdk-path 2>/dev/null) || { log "no SDK path from xcrun"; exit 2; }
CXX="$CXXBIN -isysroot $SDK"   # the command-line tools' clang finds no C++ headers without the sysroot (seen on macOS 27)
log "clang++ at $CXXBIN, sdk $SDK, $(sw_vers -productVersion), $(sysctl -n hw.model), $(sysctl -n machdep.cpu.brand_string), $(( $(sysctl -n hw.memsize) / 1073741824 )) GB; mode $MODE candidate $CAND"

# IGNEUM_SRC set: build exactly what is there, fetch and check out nothing (a prepared
# tree, such as a local merge, is never touched). Unset: clone the mirror once.
if [ -n "${IGNEUM_SRC:-}" ]; then
  SRC=$IGNEUM_SRC
  [ -d "$SRC/pow/ref" ] || { log "IGNEUM_SRC=$SRC has no pow/ref"; exit 2; }
else
  SRC=$ROOT/src
  if [ ! -d "$SRC/pow/ref" ]; then
    log "fetching the repository into $SRC"
    git clone -q "${IGNEUM_MIRROR:?set IGNEUM_MIRROR to the git mirror (user@host:path), or IGNEUM_SRC to a clone}" "$SRC" 2>>"$RUN/log.txt" || {
      log "cannot clone the mirror with this user's ssh key; ask the reliability engineer for a read key for this Mac, or set IGNEUM_SRC to an existing clone"; exit 2; }
  fi
  ( cd "$SRC" && git fetch -q origin && { git checkout -q origin/gpu-b/design 2>>"$RUN/log.txt" || git checkout -q origin/main; } )
fi
( cd "$SRC" && git log --oneline -1 2>/dev/null; git status --short 2>/dev/null | head -5 ) | tee -a "$RUN/log.txt"

RVN=$SRC/pow/third_party/ravencoin-ethash-22549129
REF=$SRC/pow/ref
FHROOT=$SRC/pow/third_party/fish-hash-823531c/src   # the pinned FishHash upstream root (the directory holding cpp/), placed by the fetch (measure-only, D-GA-04)
FH=$FHROOT/cpp
# A's fishhash_control.cpp includes "cpp/FishHash.cpp" through that root, which cannot collide with itself on a case-insensitive filesystem.
B=${IGNEUM_BUILD:-$RUN/build}; mkdir -p "$B"
if [ ! -x "$B/igneum-worker-metal" ] || [ ! -x "$B/ignpow-vectors" ]; then
log "building the reference"
# The same recipe as pow/ref/CMakeLists.txt (A's artefact recipe): the vendored C and C++
# files of rvn_ethash, then every pow/ref/src/*.cpp into ignpow; one target triple for
# the reference objects and the worker so the linker sees one deployment version.
ARCHF="-arch arm64 -mmacosx-version-min=14.0"
CC="$CXXBIN -isysroot $SDK -x c"
COMMIT=$(cd "$SRC" && git rev-parse --short=8 HEAD 2>/dev/null || echo unknown)
FHDEF=""; [ -f "$FH/FishHash.cpp" ] && [ -f "$REF/src/fishhash_control.cpp" ] && FHDEF="-DIGNPOW_HAVE_FISHHASH=1"   # ignpow.cpp lists the candidate only under this define
B3=$SRC/pow/third_party/blake3-1.8.2   # the hash team's vendored BLAKE3 (IgneumHash); portable only, as pow/ref/CMakeLists.txt builds it
B3INC=""; B3SRC=""; [ -f "$B3/blake3.c" ] && { B3INC="-I$B3"; B3SRC="$B3/blake3.c $B3/blake3_dispatch.c $B3/blake3_portable.c"; }
CK=$SRC/pow/third_party/cuckaroo29-5585282/src/src   # the hash team's Cuckaroo29 control (tromp's cuckoo, fetched by manifest); its blake2b beside BLAKE3 (the GPU team's fix, 10 Oct 2026)
CKINC=""; [ -f "$CK/cuckaroo/cuckaroo.hpp" ] && { CKINC="-I$CK"; B3SRC="$B3SRC $CK/crypto/blake2b-ref.c"; }
( cd "$B" && \
  $CC $ARCHF -std=c11 -O2 -fno-strict-aliasing -w -I"$RVN" -I"$RVN/crypto/ethash/lib/ethash" -c \
    "$RVN/crypto/ethash/lib/ethash/primes.c" "$RVN/crypto/ethash/lib/keccak/keccak.c" \
    "$RVN/crypto/ethash/lib/keccak/keccakf800.c" "$RVN/crypto/ethash/lib/keccak/keccakf1600.c" 2>>"$RUN/log.txt" && \
  $CXX $ARCHF -std=c++17 -O2 -fno-strict-aliasing -w -I"$RVN" -I"$RVN/crypto/ethash/lib/ethash" -c \
    "$RVN/crypto/ethash/lib/ethash/ethash.cpp" "$RVN/crypto/ethash/lib/ethash/progpow.cpp" 2>>"$RUN/log.txt" && \
  { [ -z "$B3SRC" ] || $CC $ARCHF -std=c11 -O2 -w -DBLAKE3_NO_SSE2 -DBLAKE3_NO_SSE41 -DBLAKE3_NO_AVX2 -DBLAKE3_NO_AVX512 -DBLAKE3_USE_NEON=0 $B3INC -c $B3SRC 2>>"$RUN/log.txt"; } && \
  $CXX $ARCHF -std=c++17 -O2 -w -DIGNPOW_BUILD_COMMIT="\"$COMMIT\"" $FHDEF $B3INC $CKINC -I"$FHROOT" -I"$RVN" -I"$RVN/crypto/ethash/lib/ethash" -I"$REF/include" -I"$REF/src" -c \
    $(ls "$REF"/src/*.cpp | grep -v "fishhash") 2>>"$RUN/log.txt" && \
  { [ -z "$FHDEF" ] || $CXX $ARCHF -std=c++17 -O2 -w $FHDEF -I"$FHROOT" -I"$RVN" -I"$RVN/crypto/ethash/lib/ethash" -I"$REF/include" -I"$REF/src" -c "$REF"/src/fishhash_control.cpp 2>>"$RUN/log.txt"; } && \
  ar rcs libignpow.a ./*.o ) || { log "reference build failed; see $RUN/log.txt"; exit 3; }
log "reference sources: $(ls "$REF"/src/*.cpp | xargs -n1 basename | tr '\n' ' '); fishhash upstream $([ -f "$FH/FishHash.cpp" ] && echo present || echo ABSENT: C2 not in this build)"
log "building the vectors tool"
$CXX $ARCHF -std=c++17 -O2 -w $B3INC -I"$RVN" -I"$RVN/crypto/ethash/lib/ethash" -I"$REF/include" "$REF/tools/ignpow-vectors.cpp" "$B/libignpow.a" -lpthread -o "$B/ignpow-vectors" 2>>"$RUN/log.txt" || { log "vectors tool build failed; see $RUN/log.txt"; exit 3; }

W=$SRC/pow/workers
log "building the Metal worker"
python3 "$W/common/tools_embed_kernels.py" "$B/kernels_embedded.cpp" "$W/metal/shaders/igm_progpow.metal" "$W/metal/shaders/igm_dag.metal" "$W"/common/kernels/prelude_msl.inc "$W"/common/kernels/igm_fishhash.inc "$W"/common/kernels/wrap_fishhash_msl.inc "$W"/common/kernels/igm_fishhash8.inc "$W"/common/kernels/wrap_fishhash8_msl.inc "$W"/common/kernels/igm_autolykos.inc "$W"/common/kernels/wrap_autolykos_msl.inc "$W"/common/kernels/igm_igneumhash.inc "$W"/common/kernels/igm_igneumhash1.inc "$W"/common/kernels/igm_igneumhash_ctx.inc "$W"/common/kernels/wrap_igneumhash_ctx_msl.inc "$W"/common/kernels/wrap_igneumhash_msl.inc "$W"/common/kernels/igm_rxe1.inc "$W"/common/kernels/wrap_rxe1_msl.inc
$CXX $ARCHF -std=c++20 -O2 -fobjc-arc -Wall -I"$W/common" -I"$W/metal/src" -I"$REF/include" \
  "$W/common/json.cpp" "$W/common/protocol.cpp" "$W/common/progpow_gen.cpp" "$W/common/r0d_gen.cpp" "$W/common/vectors.cpp" "$W/common/vectors_main.cpp" "$W/common/refbridge.cpp" "$W/common/ext_stub.cpp" "$W/common/bench_main.cpp" "$W/common/kernel_source.cpp" "$W/common/ih_params.cpp" "$W/common/rxe1_params.cpp" "$B/kernels_embedded.cpp" \
  "$W/metal/src/main.mm" "$W/metal/src/device.mm" "$W/metal/src/engine.mm" "$W/metal/src/engine_x.mm" \
  "$B/libignpow.a" -framework Metal -framework Foundation -framework AppKit -framework IOKit -framework CoreFoundation \
  -o "$B/igneum-worker-metal" 2>>"$RUN/log.txt" || { log "worker build failed; see $RUN/log.txt (paste it back)"; exit 3; }
mkdir -p "$B/shaders" && cp "$W/metal/shaders/"*.metal "$W/common/kernels/"*.inc "$B/shaders/"
fi
WK="$B/igneum-worker-metal"

log "device facts"
"$WK" --probe 2>>"$RUN/log.txt" | tee "$RUN/devices.jsonl"
{ sw_vers; sysctl -n hw.model machdep.cpu.brand_string hw.memsize hw.ncpu; system_profiler SPDisplaysDataType 2>/dev/null | head -30; pmset -g batt 2>/dev/null | head -3; pmset -g therm 2>/dev/null | head -5; vm_stat | head -12; } > "$RUN/host.txt" 2>&1
EXACT="${CANDTAG}__${MODEL}__exactness"; SCREEN="${CANDTAG}__${MODEL}__screen"; MEMC="${CANDTAG}__${MODEL}__memory"

run_pid() { # pid-name cmd... : runs in background with a pid file, waits, returns its exit
  local name=$1; shift
  "$@" & local p=$!; echo $p > "$ROOT/pids/$name.pid"; wait $p; local rc=$?; rm -f "$ROOT/pids/$name.pid"; return $rc
}

do_exactness() {
  V=$SRC/pow/vectors/$CAND; [ -n "$VARIANT" ] && V=$SRC/pow/vectors/igneumhash/$VARIANT
  MISM=0; FAILED=0; : > "$RUN/vectors.jsonl"
  for c in nonce-boundary unseen transitions malformed tails; do
    [ -f "$V/$c.json" ] || { log "vectors $c: no file for $CAND"; continue; }
    log "vectors $c: start"
    run_pid vectors "$WK" --algo "$CAND" ${PARGS[@]+"${PARGS[@]}"} --vectors "$V/$c.json" > "$RUN/vectors-$c.out" 2> "$RUN/vectors-$c.log"; VX=$?
    cat "$RUN/vectors-$c.out" >> "$RUN/vectors.jsonl"
    M=$(grep -o "class $c: [0-9]* vectors, [0-9]* mismatches.*" "$RUN/vectors-$c.log" | tail -1)
    log "vectors $c: exit $VX; $M"
    [ "$VX" = 0 ] || FAILED=1
    MISM=$((MISM + $(echo "$M" | grep -o "[0-9]* mismatches" | grep -o "[0-9]*" || echo 0)))
  done
  python3 - "$RUN" "$MODEL" "$MISM" "$FAILED" "$CANDTAG" <<'EOF'
import json, sys, glob, os, re
run, model, mism, failed, cand = sys.argv[1], sys.argv[2], int(sys.argv[3]), int(sys.argv[4]), sys.argv[5]
s = {"device": model, "candidate": cand, "backend": "metal", "gate": "HW-01", "mismatches": mism,
     "state": "PASS" if (mism == 0 and not failed) else "FAIL", "classes": {}, "host": open(run + "/host.txt").read()[:4000]}
try: s["devices"] = json.load(open(run + "/devices.jsonl"))
except Exception as e: s["devices_error"] = str(e)
for f in sorted(glob.glob(run + "/vectors-*.log")):
    c = os.path.basename(f)[8:-4]; txt = open(f).read()
    m = re.search(r"class %s: (\d+) vectors, (\d+) mismatches(?:, (\d+) unavailable)?" % re.escape(c), txt)
    d = re.search(r"height \d+: (.*)", txt)
    s["classes"][c] = {"vectors": int(m.group(1)) if m else None, "mismatches": int(m.group(2)) if m else None, "refused": int(m.group(3)) if (m and m.group(3)) else 0, "describe": d.group(1) if d else None}
json.dump(s, open(run + "/summary.json", "w"), indent=1)
print(json.dumps({k: v for k, v in s.items() if k != "host"}))
EOF
  log "exactness: $MISM mismatches (PASS needs 0 and every class exit 0)"
  for f in "$RUN"/host.txt "$RUN"/devices.jsonl "$RUN"/summary.json "$RUN"/vectors.jsonl "$RUN"/vectors-*.log "$RUN"/vectors-*.out "$RUN"/log.txt; do [ -f "$f" ] && push "$EXACT" "$(basename "$f")" "$f"; done
}

do_screen() {
  for spec in "1:90" "3360000:90" "4487776:90"; do
    H=${spec%%:*}; S=${spec##*:}
    log "bench $H: start ($S s)"
    run_pid bench "$WK" --algo "$CAND" ${PARGS[@]+"${PARGS[@]}"} bench "$H" "$S" > "$RUN/bench-$H.jsonl" 2> "$RUN/bench-$H.err"; BX=$?
    log "bench $H: exit $BX; $(grep -o '"bench": {[^}]*}' "$RUN/bench-$H.jsonl" | cut -c1-200)"
    push "$SCREEN" "bench-$H.jsonl" "$RUN/bench-$H.jsonl"; push "$SCREEN" "bench-$H.err" "$RUN/bench-$H.err"
  done
  push "$SCREEN" host.txt "$RUN/host.txt"; push "$SCREEN" devices.jsonl "$RUN/devices.jsonl"
}

do_memory() {
  # The memory-requirement record (tests/hw-gates/memory.py): baseline with the worker stopped,
  # the launch and horizon epochs' peaks (the process footprint plus the GPU allocation), the hot
  # transition attempted through the protocol's prepare, the cold transition timed, and the
  # page-outs over ten minutes of mining at the horizon from vm_stat. All through protocol v0.
  log "memory: start"
  python3 - "$WK" "$CAND" "$RUN" "$MODEL" "$CANDTAG" "$ROOT/pids/memory.pid" "$PARAMS" <<'EOF'
import json, subprocess, sys, time, re, os
wk, cand, run, model, cand_tag, pidfile, params = sys.argv[1:8]
def vm():
    out = subprocess.run(["vm_stat"], capture_output=True, text=True).stdout
    page = int(re.search(r"page size of (\d+)", out).group(1)); d = {}
    for line in out.splitlines()[1:]:
        m = re.match(r"\s*([^:]+):\s+(\d+)", line)
        if m: d[m.group(1).strip()] = int(m.group(2)) * page
    return d
def non_reclaimable_mb(d):
    return (d.get("Anonymous pages", 0) - d.get("Pages purgeable", 0) + d.get("Pages wired down", 0) + d.get("Pages occupied by compressor", 0)) // 1048576
def pageouts(d):
    out = subprocess.run(["vm_stat"], capture_output=True, text=True).stdout
    m = re.search(r"Pageouts:\s+(\d+)", out)
    return m.group(1) if m else "0"
rec = {"candidate": cand_tag, "params": params or None, "configuration": "apple-mini-founder", "operator": "GB", "backend": "metal", "display_attached": False,
       "device_memory_mb": int(subprocess.run(["sysctl", "-n", "hw.memsize"], capture_output=True, text=True).stdout) // 1048576,
       "start": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()), "run_ids": [os.path.basename(run)], "notes": []}
base = vm(); rec["baseline_mb"] = non_reclaimable_mb(base)
p = subprocess.Popen([wk, "--algo", cand] + (["--params", params] if params else []), stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=open(run + "/memory.err", "w"), text=True, bufsize=1)
open(pidfile, "w").write(str(p.pid))
seq = 0
def send(o):
    global seq; seq += 1; o["seq"] = seq; p.stdin.write(json.dumps(o) + "\n"); p.stdin.flush()
def wait_for(kinds, timeout=1800):
    t0 = time.time()
    while time.time() - t0 < timeout:
        line = p.stdout.readline()
        if not line: return None
        try: m = json.loads(line)
        except ValueError: continue
        if m.get("t") in kinds: return m
    return None
hello = wait_for(["hello"])
try: rws = int(json.load(open(run + "/devices.jsonl"))["devices"][0].get("recommended_working_set", 0)) // 1048576   # the probe carries it; the v0 hello does not
except Exception: rws = 0
def footprint_mb():
    d = vm(); rss = int(subprocess.run(["ps", "-o", "rss=", "-p", str(p.pid)], capture_output=True, text=True).stdout.strip() or 0) // 1024
    return {"non_reclaimable_mb": non_reclaimable_mb(d), "worker_rss_mb": rss, "free_mb": d.get("Pages free", 0) // 1048576}
def budget_of(m): return m.get("budget", {})
# launch epoch
t0 = time.time(); send({"t": "configure", "device": 0, "algo": cand, "height": 1, "stat_ms": 1000}); r = wait_for(["ready", "refuse"]); launch_s = time.time() - t0
fp = footprint_mb(); rec["launch_epoch"] = {"epoch": 0, "height": 1, "result": r.get("t") if r else "none", "prepare_seconds": round(launch_s, 1), "budget": budget_of(r) if r else None,
    "peak_mb": max(fp["worker_rss_mb"], (int(budget_of(r).get("fit_cold_bytes", 0)) // 1048576) if r and r.get("t") == "ready" else 0), "footprint": fp}
# horizon epoch: height 4487776 (epoch 598 for the KAWPOW schedule; the candidate's own schedule decides)
t0 = time.time(); send({"t": "configure", "device": 0, "algo": cand, "height": 4487776, "stat_ms": 1000}); r = wait_for(["ready", "refuse"]); hor_s = time.time() - t0
fp = footprint_mb(); rec["horizon_epoch"] = {"epoch": 598, "height": 4487776, "result": r.get("t") if r else "none", "prepare_seconds": round(hor_s, 1), "budget": budget_of(r) if r else None,
    "peak_mb": max(fp["worker_rss_mb"], (int(budget_of(r).get("fit_cold_bytes", 0)) // 1048576) if r and r.get("t") == "ready" else 0), "footprint": fp, "explain": r.get("explain") if r else None}
rec["cold_transition"] = {"peak_mb": rec["horizon_epoch"]["peak_mb"], "downtime_seconds": round(hor_s, 1), "note": "drain, release, rebuild: the horizon open from the launch epoch, timed"}
# hot transition: prepare the next epoch beside the resident one
if r and r.get("t") == "ready":
    t0 = time.time(); send({"t": "prepare", "height": 4487776 + 7500}); h = wait_for(["prepared", "refuse"]); hot_s = time.time() - t0
    fp2 = footprint_mb()
    rec["hot_transition"] = {"attempted": True, "fits": bool(h and h.get("t") == "prepared"), "peak_mb": max(fp2["worker_rss_mb"], int(budget_of(r).get("fit_warm_bytes", 0)) // 1048576), "seconds": round(hot_s, 1), "reply": h, "footprint": fp2}
    # ten minutes of mining at the horizon for the residency rule (page-outs attributable to the miner)
    po0 = pageouts(vm()); t0 = time.time()
    send({"t": "job", "job_id": "mem", "height": 4487776, "header": "11" * 32, "boundary": "00000001" + "00" * 28, "share_target": "00000001" + "00" * 28, "nonce_start": "0", "nonce_count": str(1 << 40), "clean": True})
    stats = 0
    while time.time() - t0 < 600:
        m = wait_for(["stat", "fault"], timeout=30)
        if m is None: break
        if m.get("t") == "stat": stats += 1
    send({"t": "cancel", "job_id": "mem"})
    po1 = pageouts(vm())
    rec["apple"] = {"unified_memory_mb": rec["device_memory_mb"], "recommended_working_set_mb": rws,
                    "headroom_mb_left": rec["device_memory_mb"] - footprint_mb()["non_reclaimable_mb"], "page_outs_10min": int(po1) - int(po0), "pageout_count_before": po0, "pageout_count_after": po1, "stat_lines": stats}
else:
    rec["hot_transition"] = {"attempted": False, "fits": False, "note": "horizon did not open"}
    rec["apple"] = {"unified_memory_mb": rec["device_memory_mb"], "recommended_working_set_mb": rws, "headroom_mb_left": rec["device_memory_mb"] - fp["non_reclaimable_mb"], "page_outs_10min": None}
send({"t": "shutdown"})
try: p.wait(timeout=60)
except Exception: p.kill()
rec["end"] = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
rec["notes"].append("peak_mb is the larger of the worker's resident size and the engine's fit figure; the budget settles up to 3 s after a release before refusing")
rec["notes"].append("recommended_working_set from MTLDevice.recommendedMaxWorkingSetSize; headroom_mb_left = unified memory less the system's non-reclaimable use at the end of the horizon run")
json.dump(rec, open(run + "/memory.json", "w"), indent=1)
print(json.dumps({k: v for k, v in rec.items() if k not in ("notes",)})[:1500])
EOF
  rm -f "$ROOT/pids/memory.pid"
  log "memory: done"
  push "$MEMC" memory.json "$RUN/memory.json"; push "$MEMC" memory.err "$RUN/memory.err"; push "$MEMC" host.txt "$RUN/host.txt"
}

do_jobd() {
  log "jobd smoke: start (120 s)"
  WARGS="$WK --algo $CAND protocol"; [ -n "$PARAMS" ] && WARGS="$WK --algo $CAND --params '$PARAMS' protocol"   # the reference side reads IGNPOW_PARAMS (the vectors tool's environment form)
  IGNPOW_PARAMS="$PARAMS" run_pid jobd python3 "$SRC/pow/harness/jobd.py" --worker "$WARGS" --algo "$CAND" --height 1 --seconds 120 --out "$RUN/jobd-smoke.json" --share-bits 20 --ref "$B/ignpow-vectors" --seed "mini-smoke" > "$RUN/jobd-smoke.log" 2>&1; JX=$?
  JS=$(python3 -c "import json;d=json.load(open('$RUN/jobd-smoke.json'));print('refused: '+d['refused'].get('explain','')[:200] if 'refused' in d else {k:d.get(k) for k in ('seconds','accepted','rejected','stale','submitted','mhs_reported','downtime_seconds_by_cause')})" 2>/dev/null)
  log "jobd smoke: exit $JX; $JS"
  for f in jobd-smoke.json jobd-smoke.log jobd-smoke.json.stream.jsonl jobd-smoke.json.worker.err; do [ -f "$RUN/$f" ] && push "$EXACT" "$f" "$RUN/$f"; done
}

do_tune() {
  # The tuning space of DESIGN.md 3.7 at the launch epoch: threadgroup (SIMD groups per
  # threadgroup) by batch duration, 20 s a point after the dataset build, duty 1; the best
  # point by rate with the thermal state recorded; a screen cell of its own.
  log "tune: start"
  : > "$RUN/tune.jsonl"
  for tg in 1 2 4 8 16 32; do for bm in 20 50 100 200; do
    run_pid tune "$WK" --algo "$CAND" ${PARGS[@]+"${PARGS[@]}"} --knob threadgroup=$tg --knob batch_ms=$bm bench 1 20 > "$RUN/tune-$tg-$bm.jsonl" 2> "$RUN/tune-$tg-$bm.err"
    L=$(grep -o '"bench": {.*' "$RUN/tune-$tg-$bm.jsonl" | tail -1)
    TP=$(grep -o '"thermal_pressure":"[a-z]*"' "$RUN/tune-$tg-$bm.jsonl" | tail -1)
    echo "{\"threadgroup\": $tg, \"batch_ms\": $bm, $TP, $L" | sed 's/, $//' >> "$RUN/tune.jsonl"
    log "tune tg=$tg batch_ms=$bm: $(echo "$L" | grep -o '"mhs": [0-9.]*') $TP"
  done; done
  python3 - "$RUN" "$CAND" <<'EOF2'
import json, sys, re
run = sys.argv[1]; best = None; rows = []
for line in open(run + "/tune.jsonl"):
    m = re.search(r'"threadgroup": (\d+), "batch_ms": (\d+).*?"thermal_pressure":"(\w+)".*?"mhs": ([0-9.]+)', line)
    if not m: continue
    r = {"threadgroup": int(m.group(1)), "batch_ms": int(m.group(2)), "thermal": m.group(3), "mhs": float(m.group(4))}; rows.append(r)
    if best is None or r["mhs"] > best["mhs"]: best = r
json.dump({"candidate": sys.argv[2], "points": rows, "best": best, "note": "rate at the launch epoch, 20 s a point, duty 1, telemetry grade; the tuned point for the longer runs"}, open(run + "/tune-summary.json", "w"), indent=1)
print("best:", best)
EOF2
  push "$SCREEN" tune.jsonl "$RUN/tune.jsonl"; push "$SCREEN" tune-summary.json "$RUN/tune-summary.json"
}

case "$MODE" in
  exactness) do_exactness ;;
  screen) do_screen ;;
  memory) do_memory ;;
  jobd) do_jobd ;;
  tune) do_tune ;;
  all) do_exactness; do_screen; do_memory; do_jobd ;;
  *) log "unknown mode $MODE"; exit 2 ;;
esac
push "$EXACT" "log-$MODE.txt" "$RUN/log.txt"
log "done: $RUN"
