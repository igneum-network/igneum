//! GET /api/live for the dashboard's chain scene (ui/live-dag.js, the site's module) and the Cards tab's network
//! numbers. Two sources, one contract:
//!
//! * the observer's /api/live (the same URL ota.rs polls for the identity count), read through curl, cached 2 s per
//!   window, passed through untouched (`shape` adds `state.you_blocks` and `state.source` = "observer"): the scene's full
//!   feed (lanes, parents, colours, the selected chain, checkpoints, proof state). This machine's blocks are NOT rewritten:
//!   the UI marks them through the scene's `mine` option (scene/feed-contract.md: `miner` is always the 8-hex key id).
//! * the local node's `igneum_getRecentBlocks(seconds)` (igneumd an earlier release, the node lane's eec34ac3): the last 600 s
//!   of chain and merged blocks with vote key hashes, blue scores and colours. The engine computes the observer's
//!   state fields from it (`shape_from_blocks`: miners_10m, blocks_10m, blocks_per_minute) and adds them to every
//!   observer reply as `state.node`, so the Cards tab reads network numbers that need no site; and when the observer
//!   is unreachable the node's rows stand in for the scene in the SAME JSON shape as the observer's reply
//!   (`node_only_reply`, every key of scene/feed-contract.json, null where the node cannot know; `state.source` = "node",
//!   `partial: true`: no parents, no proof state), instead of `{ok:false}`. A node before an earlier release answers -32601 and the
//!   node source rests 10 minutes. The test `the_node_only_reply_has_the_contract_shape` reads the contract file itself.
//!
//! Read-only; one observer call per 2 s whatever the window asks; one node call per 5 s at most.
use crate::engine::Shared;
use serde_json::{json, Value};
use std::collections::HashSet;
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};

/// F-E68: the observer's read cadence (the shaped reply keeps its own TTL cache below)
const OBSERVER_TTL_S: u64 = 5;
static OBSERVER: crate::feedclient::Cache = crate::feedclient::cache();
static CACHE: Mutex<Option<(Instant, u32, Value)>> = Mutex::new(None);
const TTL: Duration = Duration::from_secs(2);

struct NodeCache {
    at: Option<Instant>,
    blocks: Vec<RecentBlock>,
    /// the node said "method not found" (-32601): rest until then
    retry: Option<Instant>,
}
static NODE: Mutex<NodeCache> = Mutex::new(NodeCache { at: None, blocks: Vec::new(), retry: None });
const NODE_TTL: Duration = Duration::from_secs(5);

/// The observer's reply with this machine's blocks counted. `ids` are the 8-character vote key ids of this machine's
/// cards; `state.you_blocks` counts the blocks whose `miner` is one of them and `state.source` names where the data came
/// from. Every row passes through untouched (until an earlier release the field was rewritten to "you", which the feed contract refuses:
/// the UI's `mine` function marks the lane from the card ids instead), so the contract holds on both surfaces.
pub fn shape(mut reply: Value, ids: &HashSet<String>) -> Value {
    let mut yours = 0u64;
    if let Some(blocks) = reply.get("blocks").and_then(|b| b.as_array()) {
        yours = blocks.iter().filter(|b| b.get("miner").and_then(|m| m.as_str()).map(|m| ids.contains(m)).unwrap_or(false)).count() as u64;
    }
    if let Some(st) = reply.get_mut("state").and_then(|s| s.as_object_mut()) {
        st.insert("you_blocks".into(), json!(yours));
        st.insert("source".into(), json!("observer"));
    }
    reply
}

/// The observer URL for a window: `<live_api>?window=<s>`, the window clamped to the module's 30 to 300 s.
pub fn url_for(live_api: &str, window_s: u32) -> String {
    format!("{live_api}?window={}", window_s.clamp(30, 300))
}

/// One block from `igneum_getRecentBlocks`.
#[derive(Clone, Debug)]
pub struct RecentBlock {
    pub hash: String,
    pub blue_score: u64,
    pub daa_score: u64,
    pub timestamp_ms: u64,
    pub vote_key_hash: String,
    pub is_chain_block: bool,
    pub color: String,
    /// "header" or "chain_block" (eec34ac3: a merged block whose own header was not at hand carries its merging
    /// chain block's time and blue score, marked so blocks_per_minute stays honest); "" on an older row = header
    pub timestamp_source: String,
}

pub fn parse_recent(v: &Value) -> Vec<RecentBlock> {
    let s = |b: &Value, k: &str| b.get(k).and_then(|x| x.as_str()).unwrap_or("").to_string();
    let n = |b: &Value, k: &str| b.get(k).and_then(|x| x.as_u64()).unwrap_or(0);
    v.as_array()
        .map(|a| {
            a.iter()
                .map(|b| RecentBlock {
                    hash: s(b, "hash"),
                    blue_score: n(b, "blue_score"),
                    daa_score: n(b, "daa_score"),
                    timestamp_ms: n(b, "timestamp_ms"),
                    vote_key_hash: s(b, "vote_key_hash"),
                    is_chain_block: b.get("is_chain_block").and_then(|x| x.as_bool()).unwrap_or(false),
                    color: s(b, "color"),
                    timestamp_source: s(b, "timestamp_source"),
                })
                .collect()
        })
        .unwrap_or_default()
}

/// The lane id the UI matches against a card's `ids`: the first 8 hex of the vote key hash (engine.rs gives a card
/// its ids the same way, `h.chars().take(8)`); "" when the block carries no key.
pub fn lane(vote_key_hash: &str) -> String {
    vote_key_hash.trim_start_matches("0x").chars().take(8).collect()
}

/// The site's `short`: the first 16 hex (site/api/live.mjs), so a hash reads the same from either source.
fn short(h: &str) -> String {
    h.trim_start_matches("0x").chars().take(16).collect()
}

/// The observer's `state` fields that come from the blocks: distinct vote keys in 10 minutes, blocks in 10
/// minutes, blocks per minute over the last 10 minutes (oldest first), and the `blocks` (last `window_s`, in the
/// contract's row shape as far as the node knows it: no parents, no number, no proof state) and `miners` arrays
/// (the contract's `{id, blocks, share, last_seen, engine}`). `now_ms` is the reference time.
pub fn shape_from_blocks(blocks: &[RecentBlock], now_ms: u64, window_s: u64) -> Value {
    let ten = now_ms.saturating_sub(600_000);
    let recent: Vec<&RecentBlock> = blocks.iter().filter(|b| b.timestamp_ms >= ten).collect();
    let mut miners: std::collections::BTreeMap<&str, u64> = Default::default();
    for b in &recent {
        if !b.vote_key_hash.is_empty() {
            *miners.entry(b.vote_key_hash.as_str()).or_insert(0) += 1;
        }
    }
    let mut per_min = vec![0u64; 10];
    for b in &recent {
        let idx = ((b.timestamp_ms - ten) / 60_000).min(9) as usize;
        per_min[idx] += 1;
    }
    let win = now_ms.saturating_sub(window_s * 1000);
    let rows: Vec<Value> = blocks
        .iter()
        .filter(|b| b.timestamp_ms >= win)
        .map(|b| {
            let id = lane(&b.vote_key_hash);
            json!({
                "hash": short(&b.hash), "number": Value::Null, "blue_score": b.blue_score, "daa": b.daa_score,
                "ts": b.timestamp_ms, "rx": b.timestamp_ms, "parents": [], "chain": b.is_chain_block,
                "miner": if id.is_empty() { Value::Null } else { json!(id) },
                "color": if b.color.is_empty() { "pending" } else { b.color.as_str() },
                "locked": false, "final": false, "shards": [], "proven": false
            })
        })
        .collect();
    json!({
        "miners_10m": miners.len(),
        "blocks_10m": recent.len(),
        "blocks_per_minute": per_min,
        "blocks": rows,
        "miners": miners.iter().map(|(k, n)| json!({ "id": lane(k), "blocks": n, "share": if recent.is_empty() { 0.0 } else { (*n as f64 * 1000.0 / recent.len() as f64).round() / 10.0 }, "last_seen": Value::Null, "engine": Value::Null })).collect::<Vec<_>>(),
    })
}

/// The node-only reply, in the contract's shape: every key of scene/feed-contract.json present, null where the node cannot
/// know (the observer's lag, the mempool, the proving layer), `partial: true` and `state.source` = "node" as the documented
/// extensions, `state.node` carrying the node's own numbers as on an observer reply. `now_iso` is the clock as ISO 8601.
pub fn node_only_reply(blocks: &[RecentBlock], now_ms: u64, now_iso: &str, window_s: u64, node: Value, ids: &HashSet<String>) -> Value {
    let mut v = shape_from_blocks(blocks, now_ms, window_s);
    let o = v.as_object_mut().unwrap();
    let rows = o.remove("blocks").unwrap_or(Value::Null);
    let miners = o.remove("miners").unwrap_or(Value::Null);
    let state = json!({
        "stale": false, "age_s": 0, "network": Value::Null, "node_version": Value::Null, "height": Value::Null,
        "block_count": node.get("block_count").cloned().unwrap_or(Value::Null), "header_count": node.get("header_count").cloned().unwrap_or(Value::Null),
        "blue_score": node.get("blue_score").cloned().unwrap_or(Value::Null), "difficulty": node.get("difficulty").cloned().unwrap_or(Value::Null),
        "hashes_per_second_estimate": Value::Null, "peers": node.get("peers").cloned().unwrap_or(Value::Null), "mempool": Value::Null,
        "blocks_60s": Value::Null, "blocks_per_second_60s": Value::Null, "blocks_per_minute": o.get("blocks_per_minute").cloned().unwrap_or(json!([])),
        "miners_10m": o.get("miners_10m").cloned().unwrap_or(json!(0)), "observer_started_at": Value::Null, "observer_lag_s": Value::Null,
        "queue_depth": Value::Null, "updated_at": now_iso, "source": "node", "node": node
    });
    let finality = json!({ "supported": false, "active": false, "next_index": Value::Null, "latest_locked_index": Value::Null, "latest_locked_hash": Value::Null,
        "latest_locked_blue_score": Value::Null, "params": Value::Null, "weights": Value::Null, "checkpoints": [] });
    let out = json!({ "ok": true, "partial": true, "now": now_iso, "state": state, "blocks": rows, "miners": miners, "events": [], "finality": finality,
        "proving": { "supported": false, "reason": "the local node's rows: no proving layer in this view" } });
    let mut out = shape(out, ids);
    if let Some(st) = out.get_mut("state").and_then(|s| s.as_object_mut()) {
        st.insert("source".into(), json!("node"));   // shape() names the observer; these rows are the node's
    }
    out
}

/// The clock as the contract's `now`: ISO 8601 with milliseconds, UTC.
fn iso_now(unix_s: f64) -> String {
    let ms = (unix_s * 1000.0) as i64;
    let (secs, millis) = (ms.div_euclid(1000), ms.rem_euclid(1000));
    // civil date from days since 1970-01-01 (Howard Hinnant's algorithm), no chrono dependency
    let (days, rem) = (secs.div_euclid(86_400), secs.rem_euclid(86_400));
    let z = days + 719_468;
    let era = z.div_euclid(146_097);
    let doe = z - era * 146_097;
    let yoe = (doe - doe / 1460 + doe / 36_524 - doe / 146_096) / 365;
    let y = yoe + era * 400;
    let doy = doe - (365 * yoe + yoe / 4 - yoe / 100);
    let mp = (5 * doy + 2) / 153;
    let d = doy - (153 * mp + 2) / 5 + 1;
    let m = if mp < 10 { mp + 3 } else { mp - 9 };
    let y = if m <= 2 { y + 1 } else { y };
    format!("{y:04}-{m:02}-{d:02}T{:02}:{:02}:{:02}.{millis:03}Z", rem / 3600, (rem % 3600) / 60, rem % 60)
}

/// The node's recent blocks, cached 5 s; empty when the node is down, carries no such method (-32601, rests 10
/// minutes) or answered nothing.
fn node_blocks(shared: &Arc<Shared>) -> Vec<RecentBlock> {
    {
        let c = NODE.lock().unwrap();
        if c.at.map(|t| t.elapsed() < NODE_TTL).unwrap_or(false) {
            return c.blocks.clone();
        }
        if c.retry.map(|t| Instant::now() < t).unwrap_or(false) {
            return Vec::new();
        }
    }
    let node_up = matches!(shared.state.lock().unwrap().node.state.as_str(), "syncing" | "synced");
    if !node_up {
        return Vec::new();
    }
    match crate::execrpc::call(shared.runtime.evm_port(), "igneum_getRecentBlocks", json!([600]), Duration::from_secs(6)) {
        Ok(v) if v.is_array() => {
            let blocks = parse_recent(&v);
            let mut c = NODE.lock().unwrap();
            c.at = Some(Instant::now());
            c.blocks = blocks.clone();
            blocks
        }
        Ok(_) => Vec::new(),
        Err(e) => {
            // the node's default arm: {"code": -32601, "message": "method igneum_getRecentBlocks not found"}
            if e.contains("not found") || e.contains("-32601") {
                NODE.lock().unwrap().retry = Some(Instant::now() + Duration::from_secs(600));
            }
            Vec::new()
        }
    }
}

/// The node's `state` fields for the Cards tab (`state.node` on an observer reply, `state` on a node-only one).
fn node_state(shared: &Arc<Shared>, blocks: &[RecentBlock], now_ms: u64) -> Value {
    let mut v = shape_from_blocks(blocks, now_ms, 90);
    let st = shared.state.lock().unwrap();
    let o = v.as_object_mut().unwrap();
    o.remove("blocks");
    o.remove("miners");
    o.insert("block_count".into(), json!(st.node.blocks));
    o.insert("header_count".into(), json!(st.node.headers));
    o.insert("daa".into(), json!(st.node.daa));
    o.insert("blue_score".into(), json!(st.node.blue));
    o.insert("difficulty".into(), json!(st.node.difficulty));
    o.insert("peers".into(), json!(st.node.peers));
    o.insert("synced".into(), json!(st.node.synced));
    o.insert("hashes_per_second_estimate".into(), Value::Null);
    o.insert("source".into(), json!("node"));
    v
}

/// The reply for the dashboard: the observer's, cached 2 s per window, with the node's state fields added as
/// `state.node` when the node answers; the node's rows alone (`partial: true`) when the observer is unreachable;
/// `{ok:false, error}` when neither answers (the UI then draws its own blocks strip).
pub fn fetch(shared: &Arc<Shared>, live_api: &str, window_s: u32, ids: &HashSet<String>) -> Value {
    let window_s = window_s.clamp(30, 300);
    let now_ms = (crate::platform::unix_now_f() * 1000.0) as u64;
    let blocks = node_blocks(shared);
    let node = if blocks.is_empty() { None } else { Some(node_state(shared, &blocks, now_ms)) };
    let cached = CACHE.lock().unwrap().as_ref().and_then(|(at, w, v)| if *w == window_s && at.elapsed() < TTL { Some(v.clone()) } else { None });
    let mut reply = match cached {
        Some(v) => v,
        None if live_api.is_empty() => json!({ "ok": false, "error": "no observer address in this build" }),
        None => {
            let url = url_for(live_api, window_s);
            // F-E68: one keep-alive read, a failure remembered for its wait (no read per window request while the observer is down)
            let out = crate::feedclient::cached(&OBSERVER, &window_s.to_string(), &url, "the observer", OBSERVER_TTL_S).ok().map(|r| r.value);
            let reply = match out {
                Some(v) if v.get("ok").and_then(|o| o.as_bool()).unwrap_or(false) && v.get("blocks").map(|b| b.is_array()).unwrap_or(false) => shape(v, ids),
                _ => json!({ "ok": false, "error": "the observer did not answer" }),
            };
            if reply.get("ok").and_then(|o| o.as_bool()).unwrap_or(false) {
                *CACHE.lock().unwrap() = Some((Instant::now(), window_s, reply.clone()));
            }
            reply
        }
    };
    let observer_ok = reply.get("ok").and_then(|o| o.as_bool()).unwrap_or(false);
    match (observer_ok, node) {
        (true, Some(n)) => {
            if let Some(st) = reply.get_mut("state").and_then(|s| s.as_object_mut()) {
                st.insert("node".into(), n);
            }
            reply
        }
        (true, None) => reply,
        (false, Some(n)) => {
            // the node's rows stand in, in the contract's shape: blocks without parents or proof state, and the reply says so
            node_only_reply(&blocks, now_ms, &iso_now(crate::platform::unix_now_f()), window_s as u64, n, ids)
        }
        (false, None) => reply,
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    // a fixture in the observer's shape (site/api/live.mjs): three miners, this machine is 8fafda27, one of its blocks on
    // the selected chain and proven, one excluded; a locked and a pending checkpoint
    fn fixture() -> Value {
        json!({
            "ok": true, "now": 1791301670,
            "state": { "stale": false, "age_s": 1, "height": 198490 },
            "blocks": [
                { "hash": "a1", "ts": 1791301600000i64, "blue_score": 194690, "daa": 198486, "parents": [], "chain": true, "color": "blue", "miner": "8fafda27", "locked": true, "final": true, "shards": [{ "state": "paid" }], "proven": true },
                { "hash": "a2", "ts": 1791301601000i64, "blue_score": 194691, "daa": 198487, "parents": ["a1"], "chain": true, "color": "blue", "miner": "1b2c3d4e", "locked": false, "final": false, "shards": [{ "state": "proving" }], "proven": false },
                { "hash": "a3", "ts": 1791301601500i64, "blue_score": null, "daa": 198487, "parents": ["a1"], "chain": false, "color": "red", "miner": "8fafda27", "locked": false, "final": false, "shards": [], "proven": false },
                { "hash": "a4", "ts": 1791301602000i64, "blue_score": 194692, "daa": 198488, "parents": ["a2", "a3"], "chain": true, "color": "pending", "miner": "deadbeef", "locked": false, "final": false, "shards": [], "proven": false }
            ],
            "finality": { "checkpoints": [
                { "index": 6490, "blue_score": 194690, "daa": 198486, "state": "locked", "fraction_total": 0.71 },
                { "index": 6491, "blue_score": 194720, "daa": 198516, "state": "pending", "fraction_total": 0.44 }
            ] }
        })
    }

    #[test]
    fn this_machines_blocks_are_counted_and_every_row_passes_through_untouched() {
        let ids: HashSet<String> = ["8fafda27".to_string(), "9a8b7c6d".to_string()].into_iter().collect();
        let out = shape(fixture(), &ids);
        let blocks = out["blocks"].as_array().unwrap();
        assert_eq!(blocks.len(), 4);
        assert_eq!(blocks[0]["miner"], "8fafda27", "the key id stays: the contract refuses a rewritten miner");
        assert_eq!(blocks[2]["miner"], "8fafda27");
        assert_eq!(blocks[1]["miner"], "1b2c3d4e");
        assert_eq!(blocks[3]["miner"], "deadbeef");
        // the rest of the contract passes through: parents, chain, colour, proof state, checkpoints
        assert_eq!(blocks[3]["parents"], json!(["a2", "a3"]));
        assert_eq!(blocks[0]["chain"], true);
        assert_eq!(blocks[2]["color"], "red");
        assert_eq!(blocks[0]["proven"], true);
        assert_eq!(blocks[1]["shards"][0]["state"], "proving");
        assert_eq!(out["finality"]["checkpoints"].as_array().unwrap().len(), 2);
        assert_eq!(out["finality"]["checkpoints"][0]["state"], "locked");
        assert_eq!(out["state"]["you_blocks"], 2);
        assert_eq!(out["state"]["source"], "observer");
        assert_eq!(out["state"]["height"], 198490);
        assert_eq!(out["ok"], true);
    }

    #[test]
    fn no_ids_means_no_lane_is_yours_and_a_bare_reply_survives() {
        let out = shape(fixture(), &HashSet::new());
        assert_eq!(out["state"]["you_blocks"], 0);
        let bare = shape(json!({ "ok": true, "blocks": [] }), &HashSet::new());
        assert_eq!(bare["blocks"].as_array().unwrap().len(), 0);
        assert!(bare.get("state").is_none(), "no state object is invented");
    }

    #[test]
    fn the_window_is_clamped_to_the_modules_range() {
        assert_eq!(url_for("https://igneum.network/api/live", 120), "https://igneum.network/api/live?window=120");
        assert_eq!(url_for("https://igneum.network/api/live", 5), "https://igneum.network/api/live?window=30");
        assert_eq!(url_for("https://igneum.network/api/live", 9000), "https://igneum.network/api/live?window=300");
    }

    fn b(ts: u64, key: &str, chain: bool) -> RecentBlock {
        RecentBlock { hash: format!("0x{:064x}", ts), blue_score: ts / 1000, daa_score: ts / 1000, timestamp_ms: ts, vote_key_hash: key.into(), is_chain_block: chain, color: if chain { "blue".into() } else { String::new() }, timestamp_source: String::new() }
    }

    #[test]
    fn the_state_fields_come_from_the_nodes_blocks_of_the_last_ten_minutes() {
        let now = 1_791_300_000_000u64;
        let blocks = vec![b(now - 5_000, "aa", true), b(now - 30_000, "bb", true), b(now - 95_000, "aa", false), b(now - 500_000, "cc", true), b(now - 700_000, "dd", true)];
        let v = shape_from_blocks(&blocks, now, 120);
        assert_eq!(v["miners_10m"], 3, "dd is older than 10 minutes");
        assert_eq!(v["blocks_10m"], 4);
        let pm = v["blocks_per_minute"].as_array().unwrap();
        assert_eq!(pm.len(), 10);
        assert_eq!(pm[9], 2, "the newest minute holds the 5 s and 30 s blocks");
        assert_eq!(pm[8], 1, "the 95 s block");
        assert_eq!(pm[1], 1, "the 500 s block");
        assert_eq!(v["blocks"].as_array().unwrap().len(), 3, "the 120 s window");
        assert_eq!(v["blocks"][2]["color"], "pending", "an unmerged block without a colour");
        assert_eq!(v["blocks"][0]["color"], "blue");
        assert_eq!(v["blocks"][0]["miner"], "aa", "the lane id is the first 8 hex of the vote key hash, as a card's ids");
        assert_eq!(v["blocks"][0]["chain"], true, "the scene's row shape");
        assert_eq!(v["blocks"][0]["parents"], json!([]), "the node method carries no parents");
        assert_eq!(v["miners"].as_array().unwrap().len(), 3);
        assert_eq!(v["miners"][0]["id"], "aa", "the miners rows carry id, as the site's");
        assert_eq!(v["miners"][0]["blocks"], 2, "the contract's miner row: blocks, not blocks_10m");
        // this machine's rows are counted, never rewritten
        let ids: HashSet<String> = ["aa".to_string()].into_iter().collect();
        let marked = shape(json!({ "ok": true, "state": {}, "blocks": v["blocks"] }), &ids);
        assert_eq!(marked["blocks"][0]["miner"], "aa");
        assert_eq!(marked["state"]["you_blocks"], 2);
        assert_eq!(lane("0x0123456789abcdef"), "01234567");
        assert_eq!(lane(""), "");
    }

    /// scene/feed-contract.json, the same file tools/scene/feed-contract.mjs reads: the node-only reply carries exactly the
    /// contract's keys (plus the documented extensions) at every level the scene reads.
    const CONTRACT: &str = include_str!("../../../scene/feed-contract.json");

    fn keys_of(v: &Value) -> Vec<String> {
        let mut k: Vec<String> = v.as_object().expect("an object").keys().cloned().collect();
        k.sort();
        k
    }
    fn listed(c: &Value, name: &str) -> Vec<String> {
        let mut k: Vec<String> = c[name].as_array().unwrap().iter().map(|x| x.as_str().unwrap().to_string()).collect();
        k.sort();
        k
    }

    #[test]
    fn the_node_only_reply_has_the_contract_shape() {
        let c: Value = serde_json::from_str(CONTRACT).expect("scene/feed-contract.json parses");
        let now = 1_791_300_000_000u64;
        let blocks = vec![b(now - 5_000, "aa11bb22", true), b(now - 30_000, "bb22cc33", true), b(now - 95_000, "aa11bb22", false), b(now - 40_000, "", true)];
        let node = json!({ "block_count": 10, "header_count": 12, "blue_score": 9, "difficulty": 1.5, "peers": 3, "synced": true, "source": "node", "miners_10m": 2, "blocks_10m": 3, "blocks_per_minute": [0,0,0,0,0,0,0,0,1,2] });
        let ids: HashSet<String> = ["aa11bb22".to_string()].into_iter().collect();
        let out = node_only_reply(&blocks, now, "2026-10-07T13:20:00.000Z", 120, node, &ids);
        // top level: the contract's keys plus the documented extensions, nothing else
        let mut top = listed(&c, "top"); top.extend(listed(&c, "top_extensions")); top.sort();
        assert_eq!(keys_of(&out), top);
        assert_eq!(out["partial"], true);
        assert_eq!(out["now"], "2026-10-07T13:20:00.000Z", "now is the ISO string, not a float of seconds");
        // state: every contract key present, extras only from the extension list
        let state = listed(&c, "state");
        let ext = listed(&c, "state_extensions");
        for k in &state { assert!(out["state"].get(k).is_some(), "state lacks {k}"); }
        for k in keys_of(&out["state"]) { assert!(state.contains(&k) || ext.contains(&k), "state carries an unknown key {k}"); }
        assert_eq!(out["state"]["source"], "node");
        assert_eq!(out["state"]["you_blocks"], 2);
        assert_eq!(out["state"]["node"]["peers"], 3);
        // rows: exactly the contract's keys
        for row in out["blocks"].as_array().unwrap() { assert_eq!(keys_of(row), listed(&c, "block")); }
        assert_eq!(out["blocks"][0]["miner"], "aa11bb22");
        assert_eq!(out["blocks"][0]["number"], Value::Null);
        assert_eq!(out["blocks"][3]["miner"], Value::Null, "a block without a key reads null, never an empty string");
        assert_eq!(out["miners"][0]["id"], "aa11bb22");
        assert_eq!(out["miners"][0]["share"], 50.0, "two of the four blocks in ten minutes");
        for m in out["miners"].as_array().unwrap() { assert_eq!(keys_of(m), listed(&c, "miner")); }
        assert_eq!(keys_of(&out["finality"]), listed(&c, "finality"));
        assert_eq!(keys_of(&out["proving"]), listed(&c, "proving_unsupported"));
        assert_eq!(out["events"], json!([]));
        // the miner id is 8 hex or null, never a word
        let re_ok = |s: &str| s.len() == 8 && s.chars().all(|ch| ch.is_ascii_hexdigit());
        for row in out["blocks"].as_array().unwrap() { if let Some(m) = row["miner"].as_str() { assert!(re_ok(m), "miner {m}"); } }
    }

    #[test]
    fn iso_now_formats_the_clock_as_the_contracts_now() {
        assert_eq!(iso_now(1_791_301_670.312), "2026-10-06T15:47:50.312Z");
        assert_eq!(iso_now(0.0), "1970-01-01T00:00:00.000Z");
        assert_eq!(iso_now(951_782_400.5), "2000-02-29T00:00:00.500Z");
    }

    #[test]
    fn recent_blocks_parse_from_the_node_reply_and_an_empty_reply_is_empty() {
        let v: Value = serde_json::from_str(r#"[{"hash":"0xab","blue_score":5,"daa_score":6,"timestamp_ms":7,"vote_key_hash":"cd","is_chain_block":true,"color":"blue"},{"hash":"0xcd","blue_score":5,"daa_score":6,"timestamp_ms":7,"vote_key_hash":"ef","is_chain_block":false,"color":"red","timestamp_source":"chain_block"}]"#).unwrap();
        let p = parse_recent(&v);
        assert_eq!(p.len(), 2);
        assert_eq!((p[0].blue_score, p[0].is_chain_block, p[0].color.as_str()), (5, true, "blue"));
        assert_eq!((p[1].color.as_str(), p[1].timestamp_source.as_str()), ("red", "chain_block"));
        // a null vote_key_hash (a chain block with no mergeset entry, which the executor never produces) reads as ""
        let n: Value = serde_json::from_str(r#"[{"hash":"0xab","blue_score":5,"daa_score":6,"timestamp_ms":7,"vote_key_hash":null,"is_chain_block":true,"color":"blue","timestamp_source":"header"}]"#).unwrap();
        assert_eq!(parse_recent(&n)[0].vote_key_hash, "");
        assert!(parse_recent(&json!(null)).is_empty());
        assert_eq!(short("0x1234567890abcdef00"), "1234567890abcdef", "the site's 16-hex short");
    }
}
