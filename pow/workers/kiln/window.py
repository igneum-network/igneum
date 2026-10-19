#!/usr/bin/env python3
"""The figure of record from a window log and the nvidia-smi 1 Hz log: the window's hashes a second (the first ten seconds dropped),
the mean watts over the window's samples, uJ a hash, pJ a useful fragment operation (programme words x fragments a hash), the clock
point (the median SM clock and the card's cap), the compile time a batch. Prints one JSON line."""
import json, sys, statistics, re
log, power = sys.argv[1], sys.argv[2]
row = None; lines = []
for l in open(log):
    l = l.strip()
    if l.startswith('{"kiln_vk"'): row = json.loads(l)["kiln_vk"]
    elif l.startswith('{"t"'): lines.append(json.loads(l))
if not row: print(json.dumps({"fault": "no window row in " + log})); sys.exit(1)
samples = []
for l in open(power):
    f = [x.strip() for x in l.split(",")]
    if len(f) < 7: continue
    try: samples.append((float(f[1].split()[0]), float(f[2].split()[0]), float(f[3].split()[0]), float(f[4].split()[0]), f[7] if len(f) > 7 else ""))
    except ValueError: continue
# the window is the log's seconds minus the warm seconds at the end of the sampling: the last window_s samples
n = int(round(row["window_s"])); win = samples[-n:] if n and len(samples) >= n else samples
watts = statistics.mean(s[0] for s in win) if win else 0.0
clock = statistics.median(s[1] for s in win) if win else 0.0
cap = win[0][2] if win else 0.0; plimit = win[0][3] if win else 0.0
hs = row["hashes_per_s"]; uj = watts / hs * 1e6 if hs else 0.0
frag_ops = row["fragments_per_hash"] * row["words"]
# the streamed bytes a hash: the coalesced form reads rows x 16 bytes a fragment, the tile form rows x 256 bytes a fragment
skib = round(row["fragments_per_hash"] * row.get("rows", 0) * (16 if row.get("coalesced", 0) else 256) / 1024, 1)
pj_op = uj * 1e6 / frag_ops if frag_ops else 0.0
out = {"device": row["device"], "doc": row["doc"], "tile_path": row["tile_path"], "k": row["k"], "window_s": row["window_s"], "window_hashes": row["window_hashes"],
       "hashes_per_s": round(hs), "watts_mean": round(watts, 1), "watt_samples": len(win), "uJ_per_hash": round(uj, 3), "fragments_per_hash": row["fragments_per_hash"], "ops_per_fragment": row["words"],
       "pJ_per_fragment_op": round(pj_op, 1), "clock_sm_median_mhz": clock, "clock_cap_mhz": cap, "power_limit_w": plimit, "compile_ms_per_batch": row["compile_ms_per_batch"],
       "nonces_per_batch": row["nonces_per_batch"], "gpu_busy_s": row.get("gpu_busy_s"), "card_ceiling_hashes_per_s_derived": row.get("card_ceiling_hashes_per_s_derived"), "host_bound": (row.get("gpu_busy_s") or 0) < 0.9 * row["window_s"], "compared": row["compared"], "mismatches": row["mismatches"], "first_fault": row["first_fault"], "tiles": row["tiles"], "T": row["T"], "side": row["side"],
       "tile_kib_per_hash_named": row["tiles"] * 16, "rows": row.get("rows", 0), "streamed_kib_per_hash": skib, "nJ_per_streamed_kib": round(uj * 1000 / skib, 2) if skib else None, "fragments_named": row.get("fragments", 0), "coalesced": row.get("coalesced", 0), "throttle_reasons": sorted(set(s[4] for s in win if s[4]))}
print(json.dumps(out))
