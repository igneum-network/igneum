#!/usr/bin/env python3
"""The registry feed: what the research chain's registry holds, read from the chain and nothing else.

Reads one node's RPC (igneum_vectorRegistry at "latest" and at every change height, eth_getTransactionByHash and
eth_getTransactionReceipt for every landing, eth_getBlockByNumber for the landing blocks and the head) and writes one
JSON file the registry page renders. Every figure in the file is the chain's answer; the "checked" fields say whether
the registry served at a change height equals the document's entry for it, and at the height before equals the entry
before (the document's change heights are the heights the chain applies them at).

  registry-feed.py --rpc http://HOST:PORT --out /path/registry.json [--every 30] [--once]
"""
import argparse, json, os, sys, time, urllib.request

SELECTOR = "0x49474e56"


def rpc(url, method, params):
    body = json.dumps({"jsonrpc": "2.0", "id": 1, "method": method, "params": params}).encode()
    req = urllib.request.Request(url, body, {"content-type": "application/json"})
    with urllib.request.urlopen(req, timeout=10) as r:
        d = json.load(r)
    if "error" in d:
        raise RuntimeError("%s: %s" % (method, d["error"]))
    return d["result"]


def decode_document(tx_input):
    if not tx_input.startswith(SELECTOR):
        return None, "input does not start with the registry selector"
    try:
        return json.loads(bytes.fromhex(tx_input[len(SELECTOR):]).decode("utf-8")), None
    except Exception as e:  # noqa: BLE001
        return None, "document does not parse: %s" % e


def coordinate_summary(c):
    """The chain reports each coordinate's floor (dMin) beside its reference (dRef); the entry carries it as reported, so
    the floor of record is readable from the public file (16 October 2026, economics' cell on the served file). If an
    answer ever lacks it, the entry serves null and says so: nothing is derived, since the ratio differs by lane (the
    raster lanes' floor is dRef / 5, the others' dRef / 4: the security review's read of 16 October 2026, F-367)."""
    out = {k: c.get(k) for k in ("index", "name", "algorithm", "activation", "retiredFrom", "successor", "active", "weight", "paramsDigest", "target", "dRef", "dMin")}
    if out.get("dMin") is None:
        out["dMin"] = None
        out["dMinRule"] = "absent from the chain's answer"
    # the public lane name beside the chain's registered identifier (16 October 2026, the review lead's ruling closing the names
    # cell: the identifier is served as data; a reader never needs it): by the name's root, null for a root not listed
    out["lane"] = lane_name(out.get("name"))
    return out


LANE_NAMES = (("anvil", "the programme lane"), ("raster", "the raster lane"), ("bandwidth", "the bandwidth lane"))


def lane_name(name):
    for root, lane in LANE_NAMES:
        if isinstance(name, str) and (name == root or name.startswith(root + "-")):
            return lane
    return None


def rows_by_height(initial, changes, key):
    """The document's list of changes as rows: the top-level value from the document's start, then each change."""
    rows = [{"height": 0, key: initial, "origin": "the document's top-level value"}]
    for ch in changes or []:
        rows.append({"height": ch["height"], key: ch[key], "origin": "a change entry"})
    return rows


def same(served, entry):
    """The chain serves one share per coordinate registered today; a document entry written when fewer existed is
    shorter, and the chain reads the missing tail as zero."""
    a, b = list(served), list(entry)
    n = max(len(a), len(b))
    return a + [0] * (n - len(a)) == b + [0] * (n - len(b))


def check_rows(url, rows, key, served_key, head):
    """For each row from the second on: the registry served at the row's height carries the row's value, and the
    registry served at the height before carries the previous row's value."""
    prev = None
    for row in rows:
        h = row["height"]
        if prev is None or h == 0 or h > head:
            row["checked"] = None
            prev = row
            continue
        try:
            at = rpc(url, "igneum_vectorRegistry", [hex(h)])[served_key]
            before = rpc(url, "igneum_vectorRegistry", [hex(h - 1)])[served_key]
            row["checked"] = same(at, row[key]) and same(before, prev[key])
            row["servedAt"] = at
            row["servedBefore"] = before
        except Exception as e:  # noqa: BLE001
            row["checked"] = None
            row["checkError"] = str(e)
        prev = row
    return rows


def build(url):
    t0 = time.time()
    chain_id = int(rpc(url, "eth_chainId", []), 16)
    head_block = rpc(url, "eth_getBlockByNumber", ["latest", False])
    head = int(head_block["number"], 16)
    reg = rpc(url, "igneum_vectorRegistry", ["latest"])
    coords = [coordinate_summary(c) for c in reg["coordinates"]]
    names = {c["index"]: c["name"] for c in coords}

    landings = []
    prev_doc = None
    for l in reg.get("landings", []):
        tx = rpc(url, "eth_getTransactionByHash", [l["txHash"]])
        rc = rpc(url, "eth_getTransactionReceipt", [l["txHash"]])
        blk = rpc(url, "eth_getBlockByNumber", [hex(l["height"]), False])
        doc, err = decode_document(tx["input"])
        entry = {
            "height": l["height"],
            "blockHash": l["hash"],
            "blockHashMatches": blk["hash"].lower() == l["hash"].lower(),
            "timestamp": int(blk["timestamp"], 16),
            "txHash": l["txHash"],
            "txBlock": int(tx["blockNumber"], 16),
            "txIndex": int(tx["transactionIndex"], 16),
            "from": tx["from"],
            "to": tx["to"],
            "nonce": int(tx["nonce"], 16),
            "gasUsed": int(rc["gasUsed"], 16),
            "status": int(rc["status"], 16),
            "documentBytes": len(tx["input"]) // 2 - 1 - 4,
            "documentError": err,
        }
        if doc is not None:
            entry["document"] = {
                "coordinates": len(doc.get("coordinates", [])),
                "payShares": doc.get("payShares"),
                "balancedLanes": doc.get("balancedLanes"),
                "balancedLanesFrom": doc.get("balancedLanesFrom"),
                "payChanges": doc.get("payChanges", []),
                "balancedLanesChanges": doc.get("balancedLanesChanges", []),
                "registryKey": doc.get("registryKey"),
                "window": doc.get("window"),
                "rule": doc.get("rule"),
            }
            # what this landing added against the landing before it (the chain appends; the lists only grow)
            added = {"payChanges": [], "balancedLanesChanges": [], "coordinates": []}
            if prev_doc is not None:
                pp = {json.dumps(x, sort_keys=True) for x in prev_doc.get("payChanges", [])}
                pl = {json.dumps(x, sort_keys=True) for x in prev_doc.get("balancedLanesChanges", [])}
                added["payChanges"] = [x for x in doc.get("payChanges", []) if json.dumps(x, sort_keys=True) not in pp]
                added["balancedLanesChanges"] = [x for x in doc.get("balancedLanesChanges", []) if json.dumps(x, sort_keys=True) not in pl]
                pc = {c["name"]: c for c in prev_doc.get("coordinates", [])}
                for c in doc.get("coordinates", []):
                    before = pc.get(c["name"])
                    if before is None:
                        added["coordinates"].append({"name": c["name"], "change": "new"})
                    else:
                        diff = sorted(k for k in set(c) | set(before) if c.get(k) != before.get(k))
                        if diff:
                            added["coordinates"].append({"name": c["name"], "change": "fields " + ", ".join(diff)})
                for name in pc:
                    if name not in {c["name"] for c in doc.get("coordinates", [])}:
                        added["coordinates"].append({"name": name, "change": "absent"})
                entry["added"] = added
            else:
                entry["added"] = None  # the first landing: the whole document
            prev_doc = doc
        landings.append(entry)

    latest_doc = prev_doc or {}
    pay_rows = rows_by_height(latest_doc.get("payShares"), latest_doc.get("payChanges"), "payShares") if latest_doc else []
    lane_rows = rows_by_height(latest_doc.get("balancedLanes"), latest_doc.get("balancedLanesChanges"), "lanes") if latest_doc else []
    if lane_rows and latest_doc.get("balancedLanesFrom") is not None:
        lane_rows[0]["height"] = latest_doc["balancedLanesFrom"]
        lane_rows[0]["origin"] = "the document's top-level value, from balancedLanesFrom"
    check_rows(url, pay_rows, "payShares", "payShares", head)
    check_rows(url, lane_rows, "lanes", "balancedLanes", head)

    return {
        "generated": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        "readSeconds": round(time.time() - t0, 2),
        "chainId": chain_id,
        "head": head,
        "headHash": head_block["hash"],
        "headTimestamp": int(head_block["timestamp"], 16),
        "registry": {k: reg.get(k) for k in ("height", "registryKey", "registryAddress", "selector", "chainHeld", "noticeBlocks", "maxLiveBlocks", "window", "payShares", "balancedLanes")},
        "coordinates": coords,
        "names": names,
        "landings": landings,
        "payByHeight": pay_rows,
        "lanesByHeight": lane_rows,
        "source": {"method": "igneum_vectorRegistry, eth_getTransactionByHash, eth_getTransactionReceipt, eth_getBlockByNumber", "rule": "every figure is the chain's answer to the call named; nothing is edited by hand"},
    }


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--rpc", required=True)
    ap.add_argument("--out", required=True)
    ap.add_argument("--every", type=float, default=30)
    ap.add_argument("--once", action="store_true")
    a = ap.parse_args()
    while True:
        try:
            data = build(a.rpc)
            tmp = a.out + ".tmp"
            with open(tmp, "w") as f:
                json.dump(data, f, separators=(",", ":"))
            os.replace(tmp, a.out)
            print("%s head %d landings %d read %.1fs" % (data["generated"], data["head"], len(data["landings"]), data["readSeconds"]), flush=True)
        except Exception as e:  # noqa: BLE001
            print("tick failed: %s" % e, file=sys.stderr, flush=True)
        if a.once:
            return
        time.sleep(a.every)


if __name__ == "__main__":
    main()
