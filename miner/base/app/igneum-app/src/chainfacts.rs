//! GET /api/ladder: the chain facts behind the miner's ladder (miner-ui-5, 7 October 2026). Every rung the dashboard
//! shows is a number from the node's finality RPC, never a count this app keeps: `getFinalityWeights` (the per-key
//! table: `keys[].blocks` in the weight window, `keys[].voter` against `params.dust`, `keys[].participation` over
//! `params.presenceWindow`, `totalWeight`, `activeWeight`, `voters`, `checkpointIndex`) and `getFinalityCheckpoints`
//! (`latestLockedIndex`, `nextIndex`, `finalityActive`). The network rate and the reward come from the public
//! `/api/stats` (`hashrate`, `block_reward.miner_ign`, `block_reward.ramp_factor`), the observer's reading of the same
//! node RPCs.
//!
//! Sources, in order: the local node's EVM port answering `igneum_getFinalityWeights` (OWED on the node: the finality
//! RPCs are wRPC only today and the engine carries no websocket client), else the observer's `/api/live`, whose
//! `finality.weights` is the node's `getFinalityWeights` relayed (field names in snake case, `keys[]` cut to 64, key ids
//! as the first 8 hex of the key hash). The reply names its source so the dashboard can say it on hover. Cached 10 s.
use serde_json::{json, Value};
use std::sync::Mutex;
use std::time::{Duration, Instant};

static CACHE: Mutex<Option<(Instant, Value)>> = Mutex::new(None);
const TTL: Duration = Duration::from_secs(10);
/// the observer relays at most this many keys (tools/observer/observer.mjs)
pub const OBSERVER_KEYS_CAP: usize = 64;

fn num(v: &Value) -> f64 {
    match v {
        Value::Number(n) => n.as_f64().unwrap_or(0.0),
        Value::String(s) => s.parse().unwrap_or(0.0),
        Value::Bool(b) => if *b { 1.0 } else { 0.0 },
        _ => 0.0,
    }
}
fn u(v: &Value) -> u64 { num(v).max(0.0) as u64 }
fn get<'a>(v: &'a Value, keys: &[&str]) -> &'a Value {
    for k in keys {
        if let Some(x) = v.get(k) { return x; }
    }
    &Value::Null
}

/// One key's row in either spelling (the node's camelCase or the observer's snake case).
fn key_row(k: &Value) -> Value {
    let id_full = get(k, &["keyHash", "key_hash", "id"]).as_str().unwrap_or("").trim_start_matches("0x").to_ascii_lowercase();
    let id: String = id_full.chars().take(8).collect();
    let revealed = match k.get("revealed") { Some(r) => r.as_bool().unwrap_or(false), None => k.get("pubkey").and_then(|p| p.as_str()).map(|p| !p.is_empty()).unwrap_or(false) };
    json!({
        "id": id,
        "blocks": u(get(k, &["blocks"])),
        "voter": get(k, &["voter"]).as_bool().unwrap_or(false),
        "participation": num(get(k, &["participation"])),
        "stripped_until_daa": u(get(k, &["strippedUntilDaa", "stripped_until_daa"])),
        "revealed": revealed,
    })
}

/// Is `id` one of this machine's key ids? A prefix of the other of at least 6 hex counts (the engine keeps 8, a
/// source may keep more or fewer).
pub fn is_mine(id: &str, ids: &[String]) -> bool {
    let id = id.trim_start_matches("0x").to_ascii_lowercase();
    ids.iter().any(|m| {
        let m = m.trim_start_matches("0x").to_ascii_lowercase();
        let n = id.len().min(m.len());
        n >= 6 && id[..n] == m[..n]
    })
}

/// The reply from a weights table (`w`: the node's `getFinalityWeights` reply, or the observer's
/// `finality.weights` with `finality` beside it), the observer's `finality` block (params and the locked index), the
/// public stats, and this machine's key ids. Pure.
pub fn shape(w: &Value, finality: &Value, stats: &Value, ids: &[String], source: &str, read_at: f64) -> Value {
    let params = if w.get("params").is_some() { get(w, &["params"]) } else { get(finality, &["params"]) };
    let mut keys: Vec<Value> = get(w, &["keys"]).as_array().map(|a| a.iter().map(key_row).collect()).unwrap_or_default();
    keys.sort_by(|a, b| u(&b["blocks"]).cmp(&u(&a["blocks"])).then_with(|| a["id"].as_str().cmp(&b["id"].as_str())));
    let mut mine: Vec<Value> = Vec::new();
    for (i, k) in keys.iter().enumerate() {
        if is_mine(k["id"].as_str().unwrap_or(""), ids) {
            let mut m = k.clone();
            m["rank"] = json!(i + 1);
            mine.push(m);
        }
    }
    let best = mine.iter().min_by_key(|m| u(&m["rank"])).cloned();
    let reward = get(stats, &["block_reward"]);
    let latest_locked = if finality.get("latest_locked_index").is_some() { u(get(finality, &["latest_locked_index"])) } else { u(get(get(stats, &["finality"]), &["latest_locked_index"])) };
    let active = match finality.get("active").or_else(|| finality.get("finality_active")) { Some(a) => a.as_bool().unwrap_or(false), None => get(get(stats, &["finality"]), &["active"]).as_bool().unwrap_or(false) };
    json!({
        "ok": true,
        "source": source,
        "read_at": read_at,
        "rpc": {
            "weights": "getFinalityWeights",
            "checkpoints": "getFinalityCheckpoints",
            "stats": "/api/stats",
            "relay": if source == "node" { "" } else { "igneum.network/api/live (the observer's copy of the node's reply)" }
        },
        "params": {
            "dust": u(get(params, &["dust"])),
            "weight_window": u(get(params, &["weightWindow", "weight_window"])),
            "presence_window": u(get(params, &["presenceWindow", "presence_window"])),
            "checkpoint_interval": u(get(params, &["checkpointInterval", "checkpoint_interval"])),
            "min_daa": u(get(params, &["minDaa", "min_daa"])),
        },
        "checkpoint_index": u(get(w, &["checkpointIndex", "checkpoint_index"])),
        "daa_score": u(get(w, &["daaScore", "daa_score"])),
        "latest_locked_index": latest_locked,
        "next_index": u(get(finality, &["nextIndex", "next_index"])),
        "finality_active": active,
        "total_weight": u(get(w, &["totalWeight", "total_weight"])),
        "active_weight": num(get(w, &["activeWeight", "active_weight"])),
        "voters": u(get(w, &["voters"])),
        "keys_listed": keys.len(),
        "keys_cap": if source == "node" { 0 } else { OBSERVER_KEYS_CAP },
        "network": {
            "hashrate_hps": num(get(stats, &["hashrate"])),
            "miner_ign_per_block": num(get(reward, &["miner_ign"])),
            "ign_per_block": num(get(reward, &["ign"])),
            "ramp_factor": num(get(reward, &["ramp_factor"])),
            "daa": u(get(stats, &["daa"])),
            "blocks_per_day_measured": u(get(stats, &["blocks_per_day_measured"])),
            "bps": 1,
            "stale": get(stats, &["stale"]).as_bool().unwrap_or(stats.is_null()),
        },
        "mine": mine,
        "best": best,
    })
}

/// F-E68: one keep-alive read per URL at the reply's cadence, a failure remembered for its wait
static READS: Mutex<Vec<(String, Option<crate::feedclient::Slot>)>> = Mutex::new(Vec::new());
fn curl_json(url: &str) -> Option<Value> {
    crate::feedclient::cached_many(&READS, url, TTL.as_secs(), crate::feedclient::now_unix(), || crate::feedclient::get_json(url, "the observer")).ok().map(|r| r.value)
}

/// `https://igneum.network/api/live` -> `https://igneum.network/api/stats`
pub fn stats_api_from(live_api: &str) -> String {
    match live_api.strip_suffix("/api/live") {
        Some(base) => format!("{base}/api/stats"),
        None => String::new(),
    }
}

/// earned-windows-26: the miner's reward per block in wei from the cached reply, without a fetch (the dashboard's poll
/// keeps the cache warm while a window is open; `None` when nothing is cached or the figure is missing). The ladder
/// prices a block at this figure when the block lands, so a window sums what the chain paid at the time.
pub fn cached_reward_wei() -> Option<u128> {
    let c = CACHE.lock().unwrap();
    let (_, v) = c.as_ref()?;
    let ign = num(get(v, &["network", "miner_ign_per_block"]));
    if ign > 0.0 { Some((ign * 1e18).round() as u128) } else { None }
}

/// The reply for the dashboard, cached 10 s: the node first, the observer second, `{ok:false}` with the reason when
/// neither answers.
pub fn fetch(evm_port: u16, live_api: &str, ids: &[String]) -> Value {
    if let Some((at, v)) = CACHE.lock().unwrap().as_ref() {
        if at.elapsed() < TTL {
            let mut c = v.clone();
            c["cached_age_s"] = json!(at.elapsed().as_secs_f64().round());
            return c;
        }
    }
    let now = crate::platform::unix_now_f();
    let stats = if live_api.is_empty() { None } else { curl_json(&stats_api_from(live_api)) };
    let stats = stats.unwrap_or(Value::Null);
    let reply = match crate::execrpc::call(evm_port, "igneum_getFinalityWeights", json!([]), Duration::from_secs(4)).ok().filter(|r| !r.is_null()) {
        Some(w) if w.get("keys").is_some() => shape(&w, &Value::Null, &stats, ids, "node", now),
        _ => match if live_api.is_empty() { None } else { curl_json(&crate::live::url_for(live_api, 60)) } {
            Some(live) => {
                let fin = get(&live, &["finality"]);
                let w = get(fin, &["weights"]);
                if w.get("keys").is_some() { shape(w, fin, &stats, ids, "observer", now) } else { json!({ "ok": false, "error": "the observer carries no finality weights yet", "source": "observer" }) }
            }
            None => json!({ "ok": false, "error": if live_api.is_empty() { "no observer address in this build and the chain source has no igneum_getFinalityWeights" } else { "neither the node nor the observer answered" }, "source": "" }),
        },
    };
    *CACHE.lock().unwrap() = Some((Instant::now(), reply.clone()));
    reply
}

#[cfg(test)]
mod tests {
    use super::*;

    fn observer_live() -> Value {
        json!({
            "ok": true,
            "state": { "hashes_per_second_estimate": 764944722 },
            "finality": {
                "supported": true, "active": true, "next_index": 8496, "latest_locked_index": 8495,
                "params": { "dust": 5, "minDaa": 7200, "aggregators": 8, "weightWindow": 7200, "presenceWindow": 20, "checkpointDepth": 20, "equivocationBan": 7200, "checkpointInterval": 30 },
                "weights": {
                    "checkpoint_index": 8495, "total_weight": 6000, "active_weight": 5900.5, "voters": 14,
                    "keys": [
                        { "id": "6e80f3ef", "voter": true, "blocks": 680, "revealed": true, "participation": 1, "stripped_until_daa": 0 },
                        { "id": "9e4ba6b0", "voter": true, "blocks": 559, "revealed": true, "participation": 1, "stripped_until_daa": 0 },
                        { "id": "8fafda27", "voter": true, "blocks": 61, "revealed": true, "participation": 0.95, "stripped_until_daa": 0 },
                        { "id": "00000001", "voter": false, "blocks": 3, "revealed": false, "participation": 0, "stripped_until_daa": 0 }
                    ]
                }
            }
        })
    }
    fn stats() -> Value {
        json!({ "ok": true, "stale": false, "daa": 266454, "hashrate": 764944722, "blocks_per_day_measured": 92280, "block_reward": { "miner_ign": "4.88044084", "ign": "6.10055105", "ramp_factor": 0.192519 }, "finality": { "active": true, "latest_locked_index": 8495 } })
    }

    #[test]
    fn the_observer_relay_shapes_into_the_node_fields_with_our_rank() {
        let live = observer_live();
        let fin = &live["finality"];
        let r = shape(&fin["weights"], fin, &stats(), &["8fafda27".into()], "observer", 1.0);
        assert_eq!(r["ok"], true);
        assert_eq!(r["source"], "observer");
        assert_eq!(r["params"]["dust"], 5);
        assert_eq!(r["params"]["weight_window"], 7200);
        assert_eq!(r["params"]["presence_window"], 20);
        assert_eq!(r["latest_locked_index"], 8495);
        assert_eq!(r["finality_active"], true);
        assert_eq!(r["voters"], 14);
        assert_eq!(r["keys_listed"], 4);
        assert_eq!(r["keys_cap"], 64);
        assert_eq!(r["network"]["hashrate_hps"], 764944722.0);
        assert_eq!(r["network"]["miner_ign_per_block"], 4.88044084);
        assert_eq!(r["mine"].as_array().unwrap().len(), 1);
        assert_eq!(r["mine"][0]["rank"], 3);
        assert_eq!(r["mine"][0]["blocks"], 61);
        assert_eq!(r["mine"][0]["voter"], true);
        assert_eq!(r["best"]["id"], "8fafda27");
        assert!(r["rpc"]["relay"].as_str().unwrap().contains("observer"));
    }

    #[test]
    fn the_node_reply_in_camel_case_shapes_the_same_and_names_no_relay() {
        let w = json!({
            "params": { "checkpointInterval": 30, "checkpointDepth": 20, "weightWindow": 2592000, "dust": 100, "presenceWindow": 240, "aggregators": 8, "equivocationBan": 2592000, "minDaa": 7200 },
            "checkpointIndex": 184220, "checkpointHash": "ab", "daaScore": 1284117, "totalWeight": 2000000, "activeWeight": 1900000.0, "voters": 1204,
            "keys": [
                { "keyHash": "8fafda27aa11bb22cc33dd44", "pubkey": "a1", "blocks": 2592, "voter": true, "participation": 1.0, "strippedUntilDaa": 0 },
                { "keyHash": "0000000100000000", "pubkey": "", "blocks": 44, "voter": false, "participation": 0.0, "strippedUntilDaa": 0 }
            ]
        });
        let r = shape(&w, &Value::Null, &stats(), &["8fafda27".into()], "node", 2.0);
        assert_eq!(r["source"], "node");
        assert_eq!(r["keys_cap"], 0);
        assert_eq!(r["params"]["dust"], 100);
        assert_eq!(r["params"]["weight_window"], 2592000);
        assert_eq!(r["checkpoint_index"], 184220);
        assert_eq!(r["mine"][0]["id"], "8fafda27");
        assert_eq!(r["mine"][0]["rank"], 1);
        assert_eq!(r["mine"][0]["revealed"], true);
        assert_eq!(r["rpc"]["relay"], "");
        // the locked index falls back to the public stats when the node reply carries no checkpoints block
        assert_eq!(r["latest_locked_index"], 8495);
    }

    #[test]
    fn a_machine_with_no_key_in_the_table_has_no_rank() {
        let live = observer_live();
        let fin = &live["finality"];
        let r = shape(&fin["weights"], fin, &Value::Null, &["deadbeef".into()], "observer", 1.0);
        assert_eq!(r["mine"].as_array().unwrap().len(), 0);
        assert!(r["best"].is_null());
        assert_eq!(r["network"]["stale"], true);
        assert_eq!(r["network"]["hashrate_hps"], 0.0);
    }

    #[test]
    fn id_matching_takes_a_prefix_of_six_or_more() {
        assert!(is_mine("8fafda27", &["8fafda27aa11".into()]));
        assert!(is_mine("0x8fafda27aa", &["8fafda27".into()]));
        assert!(!is_mine("8fafd", &["8fafda27".into()]));
        assert!(!is_mine("8fafda28", &["8fafda27".into()]));
        assert_eq!(stats_api_from("https://igneum.network/api/live"), "https://igneum.network/api/stats");
        assert_eq!(stats_api_from(""), "");
    }
}
