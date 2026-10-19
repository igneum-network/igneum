#!/usr/bin/env python3
# gpua-graph-sealer.py: the graph coordinate's sealer on a rented pod (the project's order 12:4x UK 13 Oct): polls the node's work method, runs the
# cuckaroo29 CUDA solver on the template's (chain, height, seed) over a fresh nonce range for a short slice, submits every ticket whose
# hash reads at or under the target. The RPC's method and field names are a config (the protocol team's line), defaults the devnet form.
import json, os, subprocess, sys, time, urllib.request, random
cfg = {"rpc": "", "chain": 7355, "bin": "./igneum_graph_cuda2", "device": 0, "slice_s": 4.0, "getwork": "igneum_getWork", "getwork_params": [],
       "submit": "igneum_submitWork", "f_height": "height", "f_seed": "seed", "f_target": "target", "f_header": "headerHash", "f_coord": "coordinate", "coord_value": "graph",
       "submit_form": "nonce,header,mix,extra", "log": "graph-sealer.log", "max_minutes": 0}
for a in sys.argv[1:]:
    k, _, v = a.partition("="); k = k.lstrip("-")
    if k in cfg: cfg[k] = type(cfg[k])(v) if not isinstance(cfg[k], list) else json.loads(v)
    elif k == "config": cfg.update(json.load(open(v)))
def rpc(method, params):
    req = urllib.request.Request(cfg["rpc"], data=json.dumps({"jsonrpc": "2.0", "id": 1, "method": method, "params": params}).encode(), headers={"Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=20) as r: return json.load(r)
def log(s):
    line = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()) + " " + s
    print(line, flush=True); open(cfg["log"], "a").write(line + "\n")
def get(d, k):
    for kk in (k, k.lower(), k[:1].upper() + k[1:]):
        if isinstance(d, dict) and kk in d: return d[kk]
    return None
def as_int(v):
    if isinstance(v, int): return v
    s = str(v); return int(s, 16) if s.startswith("0x") else int(s)
t_start = time.time(); nonce_base = random.getrandbits(40) << 20; sealed = 0; submitted = 0; last_h = None; solver = None; pending = {}; landed = 0; last_poll = 0.0; w = None
log("nonce base 0x%016x (the pod's seals carry nonces from this base, stepped by 2^20 per slice)" % nonce_base)
while True:
    if cfg["max_minutes"] and time.time() - t_start > 60 * cfg["max_minutes"]: log("max minutes reached; stopping"); break
    if time.time() - last_poll >= cfg.get("poll_s", 1.0) or w is None:   # the template re-read once per poll_s; the slices between reuse it
        try: w = rpc(cfg["getwork"], cfg["getwork_params"]).get("result"); last_poll = time.time()
        except Exception as e: log("getwork failed: %s" % e); time.sleep(3); continue
    if not w: time.sleep(2); continue
    if cfg["f_coord"] and str(get(w, cfg["f_coord"])) not in (str(cfg["coord_value"]), "None"): log("template is not the graph coordinate: %s" % get(w, cfg["f_coord"])); time.sleep(3); continue
    height = as_int(get(w, cfg["f_height"])); seed = str(get(w, cfg["f_seed"])).replace("0x", ""); tv = get(w, cfg["f_target"]); target = "" if tv is None else ("%x" % tv) if isinstance(tv, int) else (str(tv)[2:] if str(tv).startswith("0x") else ("%x" % int(str(tv))) if str(tv).isdigit() else str(tv)); header = get(w, cfg["f_header"])   # the node serves the target as a decimal integer (chain 7321, the node 0.11.x): hex only when 0x-prefixed (the decimal-as-hex misread passed tickets above the target, 2,255 refusals on c3b, 22:5x UK 13 Oct)
    if height != last_h: log("template height %d seed %s target %s" % (height, seed[:16], target[:16])); last_h = height
    nonce_base += 1 << 20
    if solver is None or solver.poll() is not None:
        solver = subprocess.Popen([cfg["bin"], "--chain", str(cfg["chain"]), "--seed", "serve", "--device", str(cfg["device"]), "--out", cfg.get("serve_out", "graph-serve.jsonl")], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True, bufsize=1)
        log("solver service started pid %d" % solver.pid)
    solver.stdin.write("%d %s %d %.2f\n" % (height, seed, nonce_base, cfg["slice_s"])); solver.stdin.flush()
    lines = []
    while True:
        l = solver.stdout.readline()
        if not l: log("solver ended"); break
        if l.startswith("DONE"): break
        lines.append(l)
    try: tgt = int(target, 16) if target else (1 << 256) - 1
    except ValueError: log("template target unreadable (%s): the slice is dropped" % target[:24]); continue
    _ths = [ (json.loads(l).get("ticket_hash") or json.loads(l).get("hash")) for l in lines if l.startswith("{") ]
    _ths = [h for h in _ths if h]
    if _ths: log("slice height %d: %d tickets, min hash %s, target %s" % (height, len(_ths), min(_ths)[:16], target[:16]))
    for line in lines:
        try: t = json.loads(line)
        except Exception: continue
        th = t.get("ticket_hash") or t.get("hash")
        if th is None or int(th, 16) > tgt: continue
        extra = t.get("ticket") or "".join("%08x" % (((e & 255) << 24) | (((e >> 8) & 255) << 16) | (((e >> 16) & 255) << 8) | ((e >> 24) & 255)) for e in t["edges"])   # the 168 bytes LE32 as hex
        # no pre-check: the node refuses a stale header by name and the round trip costs more than the template's life
        params = {"nonce": "0x%016x" % t["nonce"], "header": header, "mix": "0x" + th, "extra": "0x" + extra, "ticket": "0x" + extra}
        form = [params[k] for k in cfg["submit_form"].split(",")]
        try: r = rpc(cfg["submit"], form); submitted += 1
        except Exception as e: log("submit failed: %s" % e); continue
        ok = r.get("result"); log("SUBMIT height %d nonce %d ticket %s -> %s" % (height, t["nonce"], th[:16], json.dumps(r)[:200]))
        if ok is True or (isinstance(ok, dict) and ok.get("accepted")):
            sealed += 1; log("ACCEPTED ticket at height %d (accepted %d, submitted %d)" % (height, sealed, submitted)); pending[height] = t["nonce"]; break   # one ticket per template
    # the landed check: a block at an accepted height whose nonce is the pod's is a LANDED graph seal
    for h in sorted(list(pending.keys())):
        if h >= height: continue
        try: b = rpc("eth_getBlockByNumber", ["0x%x" % h, False]).get("result") or {}
        except Exception: continue
        bn = int(str(b.get("nonce", "0x0")), 16) if b else 0
        if bn == pending[h]: landed += 1; log("LANDED graph block at height %d nonce 0x%016x (landed %d of %d accepted)" % (h, bn, landed, sealed))
        else: log("lost the race at height %d (the block's nonce 0x%016x is not the pod's)" % (h, bn))
        del pending[h]
