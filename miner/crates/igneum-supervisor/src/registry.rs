//! The chain's discipline registry, read by the engine itself (Ember 0.2.15; the founder's question of 15 October
//! 2026, "so that is automated?", and the project's ruling): the vector's coordinates and their documents come from the
//! chain's public RPC, not from the pool's template alone, so a succession registered on the chain reaches the engine
//! at its activation with no app update and no hand, provided a shipped worker implements the document's version.
//!
//! The forms, as the node team and the protocol team named them on 15 October 2026:
//!   igneum_vectorWork("latest" | number | hash): the controller's state at a block: number, coord, names[] (the
//!     coordinates in index order; a succession appends a name and the predecessor's count stops), counts[], dRef[],
//!     targets[], lastHeights[], lags[], window, halfLife, rule, payShares[];
//!   igneum_getWork(coord): that coordinate's template with the document verbatim (params), paramsDigest, programAnchor
//!     and anchorHeight (the period-anchored form), coordName, coordDifficulty, and the node's own notice of the
//!     schedule's next entry (nextParams, nextParamsDigest, nextParamsHeight); answered by a node that seals or verifies
//!     the coordinate, so the engine reads a whole-vector reader (the public listener), never one sealer's RPC;
//!   igneum_vectorRegistry (cut B, chain-held): per coordinate index, name, algorithm, algorithmParams and its digest,
//!     activation, retiredFrom, successor, revealBlocks, plus noticeBlocks and maxLiveBlocks; read when it answers,
//!     the two above until then.
//! The cadence: one vectorWork read every REFRESH_S, a getWork per coordinate only when names[] or a digest changed
//! (the public listener allows 20 calls a second with a burst of 40 per host; this stays under two).
//! The anchor of the period-anchored form: the canonical hash at p x period_blocks - revealBlocks for
//! p = height / period_blocks (the genesis hash while that is below one), derived from headers alone; checked against
//! the node's own programAnchor on vectorwork8 at height 3787 (anchorHeight 3598).

use serde_json::{json, Value};
use std::collections::BTreeMap;
use std::time::Duration;

/// How often the controller's state is read (seconds).
pub const REFRESH_S: u64 = 15;
/// One read's bound.
pub const READ_TIMEOUT: Duration = Duration::from_secs(8);
/// The coordinates a registry may name (the protocol's bound; igneum_worker_proto::MAX_COORDINATES).
pub const MAX_COORDINATES: usize = 64;

/// The controller's state at a block (igneum_vectorWork).
#[derive(Clone, Debug, Default, PartialEq)]
pub struct VectorView {
    pub number: u64,
    pub names: Vec<String>,
    pub counts: Vec<u64>,
    pub last_heights: Vec<u64>,
    pub rule: String,
    pub window: u64,
}

/// One coordinate's document as the chain serves it (igneum_getWork(coord), or the registry entry after cut B).
#[derive(Clone, Debug, Default, PartialEq)]
pub struct CoordDoc {
    pub coord: u8,
    pub name: String,
    pub params: Value,
    pub digest: String,
    pub program_anchor: String,
    pub anchor_height: Option<u64>,
    pub height: u64,
    pub version: u64,
    /// the node's own notice of the next entry on this coordinate (the schedule form): the document, its digest,
    /// the height it takes effect; None when nothing is scheduled
    pub next: Option<(Value, String, u64)>,
    /// cut B's fields, None until the chain holds them
    pub activation: Option<u64>,
    pub retired_from: Option<u64>,
    pub successor: Option<u8>,
}

/// The chain-held registry at a block (igneum_vectorRegistry, cut B of 15 October 2026): the documents, digests,
/// activations, retirements and successors in one answer, so one call replaces the controller read and a getWork per
/// coordinate. Returns the view and every coordinate's document; a coordinate whose entry lacks a document is left
/// out of the documents (the controller's name stands).
pub fn parse_vector_registry(r: &Value) -> Result<(VectorView, BTreeMap<u8, CoordDoc>), String> {
    let coords = r.get("coordinates").and_then(|c| c.as_array()).ok_or("vectorRegistry: no coordinates[]")?;
    if coords.is_empty() {
        return Err("vectorRegistry: coordinates[] is empty".into());
    }
    if coords.len() > MAX_COORDINATES {
        return Err(format!("vectorRegistry: {} coordinates, over the bound of {MAX_COORDINATES}", coords.len()));
    }
    let height = u64_of(r.get("height")).unwrap_or(0);
    let mut names = vec![String::new(); coords.len()];
    let mut docs = BTreeMap::new();
    for e in coords {
        let Some(i) = u64_of(e.get("index")) else { continue };
        if i as usize >= coords.len() {
            return Err(format!("vectorRegistry: index {i} beyond the {} entries", coords.len()));
        }
        let name = e.get("name").and_then(|n| n.as_str()).unwrap_or("").to_string();
        names[i as usize] = name.clone();
        let Some(params) = e.get("params").filter(|p| p.is_object()).cloned() else { continue };
        let digest = hex_of(e.get("paramsDigest"));
        if digest.len() != 64 {
            continue;
        }
        docs.insert(i as u8, CoordDoc {
            coord: i as u8,
            name,
            version: u64_of(params.get("version")).unwrap_or(0),
            params,
            digest,
            program_anchor: String::new(),
            anchor_height: None,
            height,
            next: None,
            activation: u64_of(e.get("activation")),
            retired_from: u64_of(e.get("retiredFrom")).filter(|h| *h > 0),
            successor: u64_of(e.get("successor")).filter(|_| u64_of(e.get("retiredFrom")).map_or(false, |h| h > 0)).map(|s| s as u8),
        });
    }
    Ok((VectorView { number: height, names, counts: Vec::new(), last_heights: Vec::new(), rule: String::new(), window: u64_of(r.get("window")).unwrap_or(0) }, docs))
}

pub fn vector_registry_request(id: u64) -> Value {
    json!({ "jsonrpc": "2.0", "id": id, "method": "igneum_vectorRegistry", "params": ["latest"] })
}

/// The succession words from a registry read: a coordinate retired with a successor, an activation ahead of the head.
pub fn succession_words(view: &VectorView, docs: &BTreeMap<u8, CoordDoc>) -> Vec<String> {
    let mut out = Vec::new();
    for (c, d) in docs {
        if let (Some(r), Some(s)) = (d.retired_from, d.successor) {
            let sname = docs.get(&s).map(|x| x.name.clone()).unwrap_or_default();
            out.push(format!("registry: coordinate {c} ({}) retired from block {r}; its successor is coordinate {s} ({sname})", d.name));
        }
        if let Some(a) = d.activation {
            if a > view.number {
                out.push(format!("registry: coordinate {c} ({}) activates at block {a}, {} blocks ahead; its document {} (version {}) is compiled now", d.name, a - view.number, &d.digest[..12], d.version));
            }
        }
    }
    out
}

/// What changed between two reads: the words the log says and the coordinates whose document must be re-read.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct Change {
    pub appended: Vec<(u8, String)>,
    pub stopped: Vec<(u8, String)>,
    pub reread: Vec<u8>,
}

fn u64_of(v: Option<&Value>) -> Option<u64> {
    let v = v?;
    if let Some(n) = v.as_u64() {
        return Some(n);
    }
    let s = v.as_str()?.trim();
    if let Some(h) = s.strip_prefix("0x") {
        return u64::from_str_radix(h, 16).ok();
    }
    s.parse().ok()
}

fn hex_of(v: Option<&Value>) -> String {
    v.and_then(|x| x.as_str()).map(|s| s.trim().trim_start_matches("0x").to_ascii_lowercase()).unwrap_or_default()
}

/// The controller's state from a vectorWork answer (the result value).
pub fn parse_vector_work(r: &Value) -> Result<VectorView, String> {
    let names: Vec<String> = r.get("names").and_then(|n| n.as_array()).ok_or("vectorWork: no names[]")?.iter().filter_map(|n| n.as_str().map(|s| s.to_string())).collect();
    if names.is_empty() {
        return Err("vectorWork: names[] is empty".into());
    }
    if names.len() > MAX_COORDINATES {
        return Err(format!("vectorWork: {} coordinates, over the bound of {MAX_COORDINATES}", names.len()));
    }
    let list = |k: &str| -> Vec<u64> { r.get(k).and_then(|c| c.as_array()).map(|a| a.iter().map(|x| u64_of(Some(x)).unwrap_or(0)).collect()).unwrap_or_default() };
    Ok(VectorView {
        number: u64_of(r.get("number")).unwrap_or(0),
        names,
        counts: list("counts"),
        last_heights: list("lastHeights"),
        rule: r.get("rule").and_then(|x| x.as_str()).unwrap_or("").to_string(),
        window: u64_of(r.get("window")).unwrap_or(0),
    })
}

/// One coordinate's document from a getWork answer (the result value).
pub fn parse_get_work(coord: u8, r: &Value) -> Result<CoordDoc, String> {
    let params = r.get("params").cloned().ok_or_else(|| format!("getWork({coord}): no params"))?;
    if !params.is_object() {
        return Err(format!("getWork({coord}): params is not a document"));
    }
    let digest = hex_of(r.get("paramsDigest"));
    if digest.len() != 64 {
        return Err(format!("getWork({coord}): paramsDigest is not 32 bytes of hex"));
    }
    let next_digest = hex_of(r.get("nextParamsDigest"));
    let next = match (r.get("nextParams"), u64_of(r.get("nextParamsHeight"))) {
        (Some(p), Some(h)) if p.is_object() && h > 0 && !next_digest.is_empty() && next_digest.chars().any(|c| c != '0') => Some((p.clone(), next_digest, h)),
        _ => None,
    };
    Ok(CoordDoc {
        coord,
        name: r.get("coordName").and_then(|x| x.as_str()).unwrap_or("").to_string(),
        version: u64_of(params.get("version")).unwrap_or(0),
        params,
        digest,
        program_anchor: hex_of(r.get("programAnchor")),
        anchor_height: u64_of(r.get("anchorHeight")),
        height: u64_of(r.get("height")).unwrap_or(0),
        next,
        activation: u64_of(r.get("activation")),
        retired_from: u64_of(r.get("retiredFrom")),
        successor: u64_of(r.get("successor")).map(|s| s as u8),
    })
}

/// The period-anchored form's anchor height: p x period_blocks - reveal_blocks for p = height / period_blocks, the
/// genesis (0) while p is below one or the difference would go under zero.
pub fn anchor_height(height: u64, period_blocks: u64, reveal_blocks: u64) -> u64 {
    if period_blocks == 0 {
        return 0;
    }
    let p = height / period_blocks;
    if p == 0 {
        return 0;
    }
    (p * period_blocks).saturating_sub(reveal_blocks)
}

/// The JSON-RPC request bodies the reader sends (one place, so the read is auditable).
pub fn vector_work_request(id: u64) -> Value {
    json!({ "jsonrpc": "2.0", "id": id, "method": "igneum_vectorWork", "params": ["latest"] })
}
pub fn get_work_request(id: u64, coord: u8) -> Value {
    json!({ "jsonrpc": "2.0", "id": id, "method": "igneum_getWork", "params": [coord] })
}
pub fn header_request(id: u64, height: u64) -> Value {
    json!({ "jsonrpc": "2.0", "id": id, "method": "eth_getBlockByNumber", "params": [format!("0x{height:x}"), false] })
}

/// What a fresh controller read changes against the last: appended coordinates (a succession's new name), stopped
/// ones (a count no longer growing while the head moved on), and the coordinates whose document must be re-read
/// (new, or the last read's digest unknown).
pub fn diff(prev: Option<&VectorView>, next: &VectorView, docs: &BTreeMap<u8, CoordDoc>) -> Change {
    let mut ch = Change::default();
    let old_n = prev.map_or(0, |p| p.names.len());
    for (i, n) in next.names.iter().enumerate() {
        if i >= old_n {
            ch.appended.push((i as u8, n.clone()));
        }
        if !docs.contains_key(&(i as u8)) {
            ch.reread.push(i as u8);
        }
    }
    if let Some(p) = prev {
        for (i, n) in next.names.iter().enumerate() {
            let (pc, nc) = (p.counts.get(i).copied().unwrap_or(0), next.counts.get(i).copied().unwrap_or(0));
            let last = next.last_heights.get(i).copied().unwrap_or(0);
            // a coordinate that has not sealed for a whole window while the head moved on reads stopped (its words
            // only; the pool's served set decides what is mined)
            if nc == pc && next.window > 0 && next.number > last && next.number - last > next.window && pc > 0 && i < old_n {
                ch.stopped.push((i as u8, n.clone()));
            }
        }
    }
    ch
}

/// The words for the log, one line per change.
pub fn words(v: &VectorView, ch: &Change, docs: &BTreeMap<u8, CoordDoc>) -> Vec<String> {
    let mut out = Vec::new();
    for (c, n) in &ch.appended {
        let d = docs.get(c);
        out.push(format!(
            "registry: coordinate {c} ({n}) at block {}{}",
            v.number,
            d.map(|d| format!(": document {} (version {})", &d.digest[..d.digest.len().min(12)], d.version)).unwrap_or_default()
        ));
    }
    for (c, n) in &ch.stopped {
        out.push(format!("registry: coordinate {c} ({n}) has not sealed for a window at block {}", v.number));
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    // the node team's example of 15 October 2026 (vectorwork8, chain 7358, height 3787 on the bandwidth reader)
    fn vector_work_example() -> Value {
        json!({
            "number": 3774, "coord": 15,
            "names": ["tensor","bandwidth","fabric","graph","fabric-static","texture","raster","texture-17b","forge","anvil","forge-s256","anvil-s256","anvil-s128","forge-s128","anvilr-pair","forge-p64","anvilr-k2-lean-p64"],
            "counts": [752,907,857,836,40,108,1,114,0,0,41,6,1,43,33,2,33],
            "lastHeights": [3774,3773,3774,3701,3100,2900,1800,3760,0,0,3770,3600,3200,3772,3771,3500,3774],
            "rule": "geometric", "window": 64, "halfLife": 512
        })
    }
    fn get_work_example() -> Value {
        json!({
            "params": {"cache_mib":64,"chain_id":7358,"dataset_mib":4096,"epoch_blocks":4096,"item_bytes":256,"parents":256,"period_blocks":300,"program_anchor":"0000000000000000000000000000000000000000000000000000000000000000","program_ops":64,"rounds":256,"version":15},
            "paramsDigest": "0x9178827144ced4bded28a035684118c3f28c74ca8cba0fde9e48ef02f18c09fb",
            "nextParamsDigest": "0x0000000000000000000000000000000000000000000000000000000000000000",
            "coord": 1, "coordName": "bandwidth", "height": 3787,
            "programAnchor": "0xde960445d954f1dc7b2339bfadd60e8e1c1ec4b30aa65ab0bdb7bf90ee24feec",
            "anchorHeight": 3598
        })
    }

    #[test]
    fn the_controllers_state_reads_as_named() {
        let v = parse_vector_work(&vector_work_example()).unwrap();
        assert_eq!(v.number, 3774);
        assert_eq!(v.names.len(), 17);
        assert_eq!(v.names[16], "anvilr-k2-lean-p64");
        assert_eq!(v.counts[1], 907);
        assert_eq!(v.rule, "geometric");
        assert_eq!(v.window, 64);
        assert!(parse_vector_work(&json!({"number": 1})).is_err(), "no names: refused");
        let many: Vec<String> = (0..65).map(|i| format!("c{i}")).collect();
        assert!(parse_vector_work(&json!({"names": many})).unwrap_err().contains("over the bound"));
    }

    #[test]
    fn a_coordinates_document_reads_verbatim_with_its_digest_and_anchor() {
        let d = parse_get_work(1, &get_work_example()).unwrap();
        assert_eq!(d.name, "bandwidth");
        assert_eq!(d.version, 15);
        assert_eq!(d.params["period_blocks"], 300);
        assert_eq!(d.digest, "9178827144ced4bded28a035684118c3f28c74ca8cba0fde9e48ef02f18c09fb");
        assert_eq!(d.program_anchor, "de960445d954f1dc7b2339bfadd60e8e1c1ec4b30aa65ab0bdb7bf90ee24feec");
        assert_eq!(d.anchor_height, Some(3598));
        assert_eq!(d.height, 3787);
        assert_eq!(d.next, None, "a zero next digest is no notice");
        assert_eq!(d.activation, None, "cut B's fields are None until the chain holds them");
        // a scheduled next entry reads as the notice
        let mut r = get_work_example();
        r["nextParams"] = json!({"version": 16, "rounds": 128});
        r["nextParamsDigest"] = json!("0xabababababababababababababababababababababababababababababababab");
        r["nextParamsHeight"] = json!(4000);
        let d = parse_get_work(1, &r).unwrap();
        let (np, nd, nh) = d.next.unwrap();
        assert_eq!(np["version"], 16);
        assert_eq!(nd, "abababababababababababababababababababababababababababababababab");
        assert_eq!(nh, 4000);
        assert!(parse_get_work(1, &json!({"paramsDigest": "0x00"})).is_err(), "no document: refused");
    }

    #[test]
    fn the_period_anchor_follows_the_nodes_rule() {
        // the node team's check: height 3787, period_blocks 300, revealBlocks 2: floor(3787 / 300) x 300 - 2 = 3598
        assert_eq!(anchor_height(3787, 300, 2), 3598);
        // the protocol team's coordinates 15 and 16: period_blocks 64, revealBlocks 64: floor(3787 / 64) x 64 - 64 = 3712,
        // the previous period's first block
        assert_eq!(anchor_height(3787, 64, 64), 3712);
        // below one period: the genesis
        assert_eq!(anchor_height(40, 64, 64), 0);
        assert_eq!(anchor_height(64, 64, 64), 0);
        assert_eq!(anchor_height(100, 0, 2), 0, "no period: no anchor");
    }

    #[test]
    fn a_succession_reads_as_an_appended_name_and_a_stopped_count() {
        let prev = parse_vector_work(&vector_work_example()).unwrap();
        let mut n = vector_work_example();
        n["number"] = json!(3900);
        // the live coordinates sealed on through the window (bandwidth and graph here); the idle ones did not
        for c in [1usize, 3] {
            n["lastHeights"][c] = json!(3899);
            n["counts"][c] = json!(n["counts"][c].as_u64().unwrap() + 40);
        }
        n["names"].as_array_mut().unwrap().push(json!("bandwidth-2"));
        n["counts"].as_array_mut().unwrap().push(json!(0));
        n["lastHeights"].as_array_mut().unwrap().push(json!(0));
        let next = parse_vector_work(&n).unwrap();
        let mut docs = BTreeMap::new();
        for c in 0..17u8 {
            docs.insert(c, CoordDoc { coord: c, ..Default::default() });
        }
        let ch = diff(Some(&prev), &next, &docs);
        assert_eq!(ch.appended, vec![(17, "bandwidth-2".to_string())]);
        assert_eq!(ch.reread, vec![17], "the new coordinate's document is read; the known ones are not re-read");
        // coordinates whose count stood still for over a window while the head moved: texture (5, last 2900), raster (6),
        // fabric-static (4); bandwidth (1) and graph (3) sealed on and do not read stopped
        let stopped: Vec<u8> = ch.stopped.iter().map(|(c, _)| *c).collect();
        assert!(stopped.contains(&5) && stopped.contains(&6) && stopped.contains(&4));
        assert!(!stopped.contains(&3) && !stopped.contains(&1));
        let w = words(&next, &ch, &docs);
        assert!(w[0].starts_with("registry: coordinate 17 (bandwidth-2) at block 3900"));
        // the first read: every coordinate is appended and read, nothing is stopped
        let first = diff(None, &prev, &BTreeMap::new());
        assert_eq!(first.appended.len(), 17);
        assert_eq!(first.reread.len(), 17);
        assert!(first.stopped.is_empty());
    }

    // the protocol team's example of 15 October 2026 (cut B live at 09:4x UK)
    fn registry_example() -> Value {
        json!({
            "height": 4115, "chainHeld": true, "noticeBlocks": 0, "maxLiveBlocks": 0, "window": 64,
            "coordinates": [
                {"index": 0, "name": "tensor", "algorithm": "igneumhash", "params": {"version": 14, "period_blocks": 300}, "paramsDigest": "0x0ad0c9b42671000000000000000000000000000000000000000000000000ffff", "activation": 1, "retiredFrom": 0, "successor": 0, "revealBlocks": 2, "active": true, "weight": 0},
                {"index": 13, "name": "forge-s128", "algorithm": "igneumhash", "params": {"version": 21}, "paramsDigest": "0x1313131313131313131313131313131313131313131313131313131313131313", "activation": 3000, "retiredFrom": 3622, "successor": 15, "revealBlocks": 64, "active": false, "weight": 0},
                {"index": 15, "name": "forge-p64", "algorithm": "igneumhash", "params": {"version": 21, "period_blocks": 64, "program_anchor": "0000000000000000000000000000000000000000000000000000000000000000"}, "paramsDigest": "0xcc5d8a1b6d67000000000000000000000000000000000000000000000000ffff", "activation": 3622, "retiredFrom": 0, "successor": 0, "revealBlocks": 64, "active": true, "weight": 4},
                {"index": 17, "name": "forge-next", "algorithm": "igneumhash", "params": {"version": 24}, "paramsDigest": "0x1717171717171717171717171717171717171717171717171717171717171717", "activation": 4200, "retiredFrom": 0, "successor": 0, "revealBlocks": 64, "active": false, "weight": 0}
            ],
            "landings": [{"height": 4115, "hash": "0x499d0159", "txHash": "0x955296a6"}]
        })
    }

    #[test]
    fn the_chain_held_registry_reads_documents_successions_and_activations_ahead() {
        let mut r = registry_example();
        // the example names indexes 0, 13, 15 and 17 of an 18-wide vector: fill the rest with bare names
        let mut all: Vec<Value> = (0..18).map(|i| json!({"index": i, "name": format!("c{i}")})).collect();
        for e in r["coordinates"].as_array().unwrap() { let i = e["index"].as_u64().unwrap() as usize; all[i] = e.clone(); }
        r["coordinates"] = json!(all);
        let (view, docs) = parse_vector_registry(&r).unwrap();
        assert_eq!(view.number, 4115);
        assert_eq!(view.names.len(), 18);
        assert_eq!(view.names[15], "forge-p64");
        assert_eq!(docs.len(), 4, "the entries with documents");
        let d13 = &docs[&13];
        assert_eq!((d13.retired_from, d13.successor), (Some(3622), Some(15)));
        let d15 = &docs[&15];
        assert_eq!((d15.activation, d15.retired_from, d15.successor), (Some(3622), None, None));
        assert_eq!(d15.version, 21);
        let w = succession_words(&view, &docs);
        assert!(w.iter().any(|l| l == "registry: coordinate 13 (forge-s128) retired from block 3622; its successor is coordinate 15 (forge-p64)"), "{w:?}");
        assert!(w.iter().any(|l| l.starts_with("registry: coordinate 17 (forge-next) activates at block 4200, 85 blocks ahead; its document 171717171717 (version 24) is compiled now")), "{w:?}");
        assert!(parse_vector_registry(&json!({"height": 1})).is_err());
        assert_eq!(vector_registry_request(3)["method"], "igneum_vectorRegistry");
    }

    #[test]
    fn the_requests_are_the_named_methods() {
        assert_eq!(vector_work_request(7)["method"], "igneum_vectorWork");
        assert_eq!(vector_work_request(7)["params"][0], "latest");
        assert_eq!(get_work_request(8, 6)["params"][0], 6);
        assert_eq!(header_request(9, 3598)["params"][0], "0xe0e");
    }
}

// ---------- the read thread and the engine's words (0.2.15) ----------

/// What the read thread sends the engine: a fresh view with the documents read so far (the whole map, so the
/// engine replaces its copy), the compile verdict per re-read coordinate, or the read's failure words.
#[derive(Clone, Debug)]
pub enum RegistryEv {
    View { view: VectorView, docs: BTreeMap<u8, CoordDoc>, change: Change, compiled: Vec<(u8, Result<(), String>)> },
    Failed(String),
}

fn rpc(url: &crate::net::Url, body: &Value) -> Result<Value, String> {
    let (status, bytes) = crate::net::http("POST", url, Some("application/json"), body.to_string().as_bytes(), READ_TIMEOUT)?;
    if status != 200 {
        return Err(format!("the chain's RPC answered {status}"));
    }
    let v: Value = serde_json::from_slice(&bytes).map_err(|_| format!("the chain's RPC answered no JSON ({} bytes)", bytes.len()))?;
    if let Some(e) = v.get("error").filter(|e| !e.is_null()) {
        return Err(format!("the chain's RPC refused {}: {}", body.get("method").and_then(|m| m.as_str()).unwrap_or("?"), e.get("message").and_then(|m| m.as_str()).unwrap_or("error")));
    }
    v.get("result").cloned().ok_or_else(|| "the chain's RPC answered without a result".to_string())
}

/// One read: the controller's state, then the documents of the coordinates that changed (the first read: all), each
/// compiled through the shared reference as the engine would compile it from a template (the proof that the
/// registered document is one this build can mine, a worker implementing its version permitting).
/// One call tried across the readers in order (a sealer answers vectorWork but refuses another coordinate's getWork
/// with "this node seals coordinate N, not M"; a whole-vector reader answers every coordinate): the first answer wins,
/// the last refusal is the error.
fn rpc_any(urls: &[crate::net::Url], body: &Value) -> Result<Value, String> {
    let mut last = String::from("no reader named");
    for u in urls {
        match rpc(u, body) {
            Ok(v) => return Ok(v),
            Err(e) => last = format!("{}:{}: {e}", u.host, u.port),
        }
    }
    Err(last)
}

pub fn read_once(urls: &[crate::net::Url], prev: Option<&VectorView>, docs: &mut BTreeMap<u8, CoordDoc>, id: &mut u64) -> Result<(VectorView, Change, Vec<(u8, Result<(), String>)>), String> {
    // the chain-held registry first (one call, every document; 0.2.16): a node without the method answers an error
    // and the read falls to the controller's state plus a getWork per changed coordinate
    *id += 1;
    if let Ok(r) = rpc_any(urls, &vector_registry_request(*id)) {
        if let Ok((view, fresh)) = parse_vector_registry(&r) {
            let mut change = diff(prev, &view, docs);
            let mut compiled = Vec::new();
            for (c, d) in fresh {
                let known = docs.get(&c).map_or(false, |o| o.digest == d.digest && o.activation == d.activation && o.retired_from == d.retired_from);
                if !known {
                    let verdict = compile(&d);
                    docs.insert(c, d);
                    compiled.push((c, verdict));
                    if !change.reread.contains(&c) {
                        change.reread.push(c);
                    }
                }
            }
            return Ok((view, change, compiled));
        }
    }
    *id += 1;
    let view = parse_vector_work(&rpc_any(urls, &vector_work_request(*id))?)?;
    let mut change = diff(prev, &view, docs);
    // a coordinate whose name moved (a succession in place, cut B) is re-read too
    if let Some(p) = prev {
        for (i, n) in view.names.iter().enumerate() {
            if p.names.get(i).map_or(false, |o| o != n) && !change.reread.contains(&(i as u8)) {
                change.reread.push(i as u8);
            }
        }
    }
    let mut compiled = Vec::new();
    for c in change.reread.clone() {
        *id += 1;
        match rpc_any(urls, &get_work_request(*id, c)).and_then(|r| parse_get_work(c, &r)) {
            Ok(mut d) => {
                if d.name.is_empty() {
                    d.name = view.names.get(c as usize).cloned().unwrap_or_default();
                }
                let verdict = compile(&d);
                docs.insert(c, d);
                compiled.push((c, verdict));
            }
            Err(e) => compiled.push((c, Err(format!("not read: {e}")))),
        }
    }
    Ok((view, change, compiled))
}

/// The document compiled as a template's would be: the shared reference's verifier over the parameters (version,
/// dataset, rounds); Ok when this build can verify work on it.
pub fn compile(d: &CoordDoc) -> Result<(), String> {
    let text = d.params.to_string();
    crate::verify::verifier_for_params("igneumhash", Some(&text)).map(|_| ()).map_err(|e| e.to_string())
}

/// The read thread: a view every REFRESH_S, the documents on change, the words to the engine; a read that fails is
/// said once and retried at the next refresh.
pub fn spawn(rpc_url: String, tx: std::sync::mpsc::Sender<RegistryEv>) -> Result<(), String> {
    // a list of readers (comma, space or newline separated, the Solo setting's form): a whole-vector reader alone,
    // or one sealer per coordinate beside each other; each call tries them in order
    let urls: Vec<crate::net::Url> = crate::settings::rpc_url_list(&rpc_url).iter().map(|u| crate::net::parse_url(u)).collect::<Result<_, _>>()?;
    if urls.is_empty() {
        return Err("no reader named".into());
    }
    std::thread::Builder::new().name("registry-read".into()).spawn(move || {
        let mut prev: Option<VectorView> = None;
        let mut docs: BTreeMap<u8, CoordDoc> = BTreeMap::new();
        let mut id = 0u64;
        loop {
            let r = read_once(&urls, prev.as_ref(), &mut docs, &mut id);
            let ev = match r {
                Ok((view, change, compiled)) => {
                    prev = Some(view.clone());
                    RegistryEv::View { view, docs: docs.clone(), change, compiled }
                }
                Err(e) => RegistryEv::Failed(e),
            };
            if tx.send(ev).is_err() {
                return;
            }
            std::thread::sleep(Duration::from_secs(REFRESH_S));
        }
    }).map_err(|e| e.to_string())?;
    Ok(())
}

/// The engine's line when the pool's served set and the chain's registry differ: the coordinates the chain registers
/// that the pool does not serve, and the ones the pool serves that the chain does not name.
pub fn served_words(pool: &[u8], chain: &VectorView) -> Option<String> {
    let chain_set: Vec<u8> = (0..chain.names.len()).map(|i| i as u8).collect();
    let only_chain: Vec<String> = chain_set.iter().filter(|c| !pool.contains(c)).map(|c| format!("{c} ({})", chain.names[*c as usize])).collect();
    let only_pool: Vec<String> = pool.iter().filter(|c| !chain_set.contains(c)).map(|c| c.to_string()).collect();
    if only_chain.is_empty() && only_pool.is_empty() {
        return None;
    }
    let mut w = format!("registry: the chain registers {} coordinates at block {}", chain.names.len(), chain.number);
    if !only_chain.is_empty() {
        w.push_str(&format!("; the pool does not serve {}", only_chain.join(", ")));
    }
    if !only_pool.is_empty() {
        w.push_str(&format!("; the pool serves {} that the chain does not name", only_pool.join(", ")));
    }
    Some(w)
}

#[cfg(test)]
mod served_tests {
    use super::*;
    #[test]
    fn the_pool_and_the_chain_are_compared_by_coordinate() {
        let v = VectorView { number: 3900, names: vec!["tensor".into(), "bandwidth".into(), "fabric".into(), "graph".into()], ..Default::default() };
        assert_eq!(served_words(&[0, 1, 2, 3], &v), None);
        let w = served_words(&[0, 2, 7], &v).unwrap();
        assert!(w.contains("the pool does not serve 1 (bandwidth), 3 (graph)"), "{w}");
        assert!(w.contains("the pool serves 7 that the chain does not name"), "{w}");
        assert!(w.starts_with("registry: the chain registers 4 coordinates at block 3900"));
    }
}
