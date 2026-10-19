//! GET /api/scene: the miner's own feed for the network scene in the window (the founder's brief of 12 October
//! 2026: a person watches their own shares landing in blocks). One route, polled every block; the document stated
//! to the showcase team at 09:0x UK:
//!
//! * `head`: the chain's head from the public feed (feed/devnet.json: the height, its time, the hour's mean block
//!   time), read by the engine and cached ten seconds; never the ledger's head (the ledger's writer lags the chain
//!   by an hour, which froze the card at 1,138 on the founder's Mac at 10:3x UK) and never below the miner's own
//!   newest share height;
//! * `shares`: this session's accepted shares as the pool took them (the supervisor's ring, oldest first, up to
//!   256), each with the height it was mined at, the pool's step and weight, the job and whether it is the fee
//!   session's;
//! * `blocks`: the chain's share account for the address (the Live Work Ledger's miner view, the same source as
//!   the window's Earnings rows), one entry per block of the last 64 heights carrying any of this miner's shares,
//!   with the count and weight of the miner's shares in it, its state in the ledger's words and the wei paid;
//! * `totals`: the session's counts and the ledger's; `state`: "live" while the feed's head moved within two
//!   intervals, else "stale" with the words (the scene then reads catching up, never a stale height as live);
//! * `ledger`: the view's read time, the engine's fetch time and the error when the ledger does not answer.
//!
//! The page fetches no public host itself: the engine reads the feed and the ledger through curl like live.rs's
//! observer and the page reads the engine.
use crate::engine::Shared;
use serde_json::{json, Value};
use std::sync::{Arc, Mutex};
use std::time::{Duration, SystemTime, UNIX_EPOCH};

/// The miner view's URL root (the settlement team's ledger, build.igneum.network/ledger/miner-view/<address>.json).
pub const LEDGER_URL: &str = "https://build.igneum.network/ledger/miner-view/";
/// The chain's feed (the public test network's head and its cadence).
// 0.2.17: the root path stays (the pool team's pointer, 7322 from the public name's swap); the served feed path /feed/devnet12/ (unchanged under F-522) carries no
// devnet.json (read 19:1x UK, 15 October 2026) and the scene guards by chain id until the pointer moves
pub const FEED_URL: &str = "https://build.igneum.network/feed/devnet.json";
/// The chain's block interval target, 12 seconds (the founder's word of 16 October 2026): the poll interval the scene
/// reads from `head.block_interval_s` when the feed carries no mean, the next-block bar's target, the day's block count.
pub const BLOCK_INTERVAL_S: u64 = 12;
/// The heights of blocks the document carries.
pub const BLOCK_WINDOW: u64 = 64;
/// A day of blocks at the target interval (the Earnings page's 24 h window on the ledger's rows).
pub const BLOCKS_24H: u64 = 86_400 / BLOCK_INTERVAL_S;
/// The ledger is read again after this many seconds; the feed after FEED_TTL_S; the chain's balance after BALANCE_TTL_S.
pub const LEDGER_TTL_S: u64 = 20;
pub const FEED_TTL_S: u64 = 10;
pub const BALANCE_TTL_S: u64 = 20;
/// The public test network's JSON-RPC (the project's order of 11:1x UK, 12 October 2026, the founder's "not seeing any
/// coins": the IGN tile's headline is the chain's balance of the payout address, read here every block; the
/// ledger's rows are the detail under it). IGNEUM_APP_RPC_URL names another (a user's own node).
pub const RPC_URL: &str = "https://rpc.igneum.network";

// F-E68: every feed read goes through crate::feedclient (one keep-alive client, the failure memory, the back-off)
static LEDGER: crate::feedclient::Cache = crate::feedclient::cache();
static LEDGER_CHAIN: crate::feedclient::Cache = crate::feedclient::cache();
static LEDGER_INDEX: crate::feedclient::Cache = crate::feedclient::cache();
static FEED: crate::feedclient::Cache = crate::feedclient::cache();
static BALANCE: crate::feedclient::Cache = crate::feedclient::cache();
static SHARES: crate::feedclient::Cache = crate::feedclient::cache();

/// The live-shares feed (ecosystem, 18:00 UK, 12 October 2026): the pool's share journal for one payout address,
/// refreshed every 2 s on the store; 404 until the address has a share in the last ten minutes.
// 0.2.17: the root path stays (the pool's journal moves with the public name's swap; no per-chain path is served)
pub const SHARES_URL: &str = "https://build.igneum.network/feed/shares/";
/// F-E68: the strip's five seconds (was 2 s)
pub const SHARES_TTL_S: u64 = 5;

/// This machine's live shares for the chain card's sparks and tally (0.2.5, the founder's order of 18:5x UK, 12 October
/// 2026: the work between blocks), read by the engine and cached SHARES_TTL_S so the window never fetches another origin; the
/// feed's fields verbatim, or {"error": words} while the address has no share in the window.
pub fn shares(shared: &Arc<Shared>) -> Value {
    let address = shared.state.lock().unwrap().address.display.clone().to_ascii_lowercase();
    if !address.starts_with("0x") || address.len() != 42 {
        return json!({ "error": "no payout address yet" });
    }
    let url = format!("{SHARES_URL}{address}.json");
    match cached(&SHARES, &address, &url, "the live-shares feed", SHARES_TTL_S) {
        Ok((v, _)) => v,
        Err(e) => json!({ "error": e }),
    }
}

fn rpc_url() -> String {
    std::env::var("IGNEUM_APP_RPC_URL").ok().map(|s| s.trim().to_string()).filter(|s| !s.is_empty()).unwrap_or_else(|| RPC_URL.to_string())
}

/// One JSON-RPC POST to the chain's public RPC: the result value, or the words (the request built by the landing check, src/execrpc.rs).
fn rpc(method: &str, params: Value) -> Result<Value, String> {
    crate::execrpc::call_url(&rpc_url(), method, params, Duration::from_secs(6))
}

/// The window's feed proxies (GET api/feed/status, api/feed/devnet): the chain scene's network source reads the
/// project's status and chain feeds through the engine (the window never fetches another origin), each cached
/// FEED_PROXY_TTL_S so a page polling at five seconds costs the feed host one read in that time.
pub const FEED_PROXY_TTL_S: u64 = 1; // the component re-measures its clock offset from the status feed's generated time each tick (showcase 2bcbd3493): the proxy passes the upstream Date header beside the body
/// F-E68: the chain feed at the strip's five seconds (the status feed keeps the scene's second)
pub const DEVNET_PROXY_TTL_S: u64 = 5;
pub const STATUS_URL: &str = "https://build.igneum.network/feed/7322/status.json";
static FEED_STATUS: crate::feedclient::Cache = crate::feedclient::cache();
static FEED_DEVNET: crate::feedclient::Cache = crate::feedclient::cache();
pub fn feed_proxy(which: &str) -> Result<(Value, Option<String>), String> {
    let (slot, url, what, ttl) = match which {
        "status" => (&FEED_STATUS, STATUS_URL, "the status feed", FEED_PROXY_TTL_S),
        "devnet" => (&FEED_DEVNET, FEED_URL, "the chain feed", DEVNET_PROXY_TTL_S),
        _ => return Err(format!("no feed {which:?}")),
    };
    // the upstream Date header rides with the body (the showcase team's ask of 22:4x UK, 13 October 2026: the
    // component measures the feed host's clock from it); F-E68: one keep-alive read per cadence, a failure remembered
    let r = crate::feedclient::cached(slot, which, url, what, ttl)?;
    Ok((r.value, r.date))
}

/// The window's JSON-RPC proxy (POST api/rpc): the chain scene's clock reads the chain's latest header and the last
/// 64 headers for its band through the engine (the window never fetches another origin). One request or a batch
/// of at most RPC_BATCH_MAX, each an allow-listed read method, forwarded as sent to the public RPC; the answer
/// comes back as the RPC gave it (an array for a batch). Anything else is refused with the words.
pub const RPC_BATCH_MAX: usize = 20;
const RPC_ALLOWED: &[&str] = &["eth_getHeaderByNumber", "eth_getBlockByNumber", "eth_blockNumber", "eth_chainId"];
/// The engine's own header poll (the project's word of 01:2x UK, 14 October 2026: Ember's receipt of a seal on the same poll
/// the site uses, with no proxy hop in the second): a thread reads eth_getHeaderByNumber ["latest"] from the public
/// RPC at 250 and 750 ms past every second on the kept connection and keeps the newest answer with its Date header;
/// api/rpc answers a single "latest" call from that cache at once, so the page's own aligned poll reads the header
/// the engine received in the same half second.
static LATEST: Mutex<Option<(Value, Option<String>, std::time::Instant)>> = Mutex::new(None);
static POLL_STARTED: std::sync::atomic::AtomicBool = std::sync::atomic::AtomicBool::new(false);
fn start_header_poll() {
    if POLL_STARTED.swap(true, std::sync::atomic::Ordering::SeqCst) { return; }
    std::thread::Builder::new().name("header-poll".into()).spawn(|| {
        let body = crate::execrpc::request_body("eth_getHeaderByNumber", json!(["latest"]), json!("hdr"));
        loop {
            // the wall clock's quarter: 250 or 750 ms past the second, the same phase as the site's page
            let ms = SystemTime::now().duration_since(UNIX_EPOCH).map(|d| d.subsec_millis()).unwrap_or(0);
            let wait = if ms < 250 { 250 - ms } else if ms < 750 { 750 - ms } else { 1250 - ms };
            std::thread::sleep(Duration::from_millis(wait as u64));
            match rpc_upstream(&body) {
                Ok((v, date)) => { *LATEST.lock().unwrap() = Some((v, date, std::time::Instant::now())); }
                Err(_) => std::thread::sleep(Duration::from_millis(1000)),
            }
        }
    }).ok();
}
fn is_latest_call(body: &Value) -> bool {
    body.get("method").and_then(|m| m.as_str()) == Some("eth_getHeaderByNumber")
        && body.get("params").and_then(|p| p.as_array()).map_or(false, |p| p.len() == 1 && p[0].as_str() == Some("latest"))
}
pub fn rpc_proxy(body: Value) -> Result<(Value, Option<String>), String> {
    let check = |v: &Value| -> Result<(), String> {
        let m = v.get("method").and_then(|m| m.as_str()).unwrap_or("");
        if RPC_ALLOWED.contains(&m) { Ok(()) } else { Err(format!("api/rpc forwards only {}; not {m:?}", RPC_ALLOWED.join(", "))) }
    };
    match &body {
        Value::Array(items) => {
            if items.is_empty() || items.len() > RPC_BATCH_MAX {
                return Err(format!("api/rpc takes a batch of 1 to {RPC_BATCH_MAX} calls, not {}", items.len()));
            }
            for it in items { check(it)?; }
        }
        Value::Object(_) => check(&body)?,
        _ => return Err("api/rpc takes a JSON-RPC request or a batch".into()),
    }
    start_header_poll();
    if is_latest_call(&body) {
        // the newest header the engine's own aligned poll received, when it is fresher than a poll period
        if let Some((v, date, at)) = LATEST.lock().unwrap().clone() {
            if at.elapsed() < Duration::from_millis(1500) {
                let mut v = v;
                if let (Some(o), Some(id)) = (v.as_object_mut(), body.get("id").cloned()) { o.insert("id".into(), id); }
                return Ok((v, date));
            }
        }
    }
    rpc_upstream(&body)
}

/// One call to the public RPC on the kept connection; the upstream Date header beside the answer.
fn rpc_upstream(body: &Value) -> Result<(Value, Option<String>), String> {
    let url = rpc_url();
    // one kept connection to the RPC (the run of record of 22:24Z, 13 October 2026: a fresh TLS dial through curl each
    // second put Ember one second behind the site at eight of nine seals); the upstream Date header rides with the answer
    let parsed = igneum_supervisor::net::parse_url(&url).map_err(|e| format!("the chain's RPC URL: {e}"))?;
    let (status, headers, raw) = igneum_supervisor::net::http_keepalive("POST", &parsed, Some("application/json"), body.to_string().as_bytes(), Duration::from_secs(6)).map_err(|e| format!("the chain's RPC did not answer ({url}): {e}"))?;
    if status == 503 || status == 429 {
        return Err(format!("the chain's RPC answered {status}"));
    }
    let date = headers.iter().find(|(k, _)| k == "date").map(|(_, v)| v.clone());
    let v: Value = serde_json::from_slice(&raw).map_err(|_| format!("the chain's RPC answered no JSON ({} bytes)", raw.len()))?;
    Ok((v, date))
}

/// The payout address's balance on the chain (wei, decimal string) and the head it was read at, cached
/// BALANCE_TTL_S under the address.
fn chain_balance(address: &str) -> Result<(String, u64, u64), String> {
    let key = address.to_lowercase();
    // F-E68: the three RPC reads once per BALANCE_TTL_S, a failure remembered for its wait
    let r = crate::feedclient::cached_with(&BALANCE, &key, BALANCE_TTL_S, now_unix(), || {
        let head = rpc("eth_blockNumber", json!([])).ok().and_then(|v| v.as_str().map(|s| u64::from_str_radix(s.trim_start_matches("0x"), 16).unwrap_or(0))).unwrap_or(0);
        // the RPC names its chain beside the balance (the project's read of 13 October 2026: every balance carries its chain)
        let chain_id = rpc("eth_chainId", json!([])).ok().and_then(|v| v.as_str().map(|s| u64::from_str_radix(s.trim_start_matches("0x"), 16).unwrap_or(0))).unwrap_or(0);
        let hex = rpc("eth_getBalance", json!([address, "latest"]))?;
        let wei = hex_to_decimal(hex.as_str().unwrap_or("0x0")).ok_or("the balance is not hex")?;
        Ok((json!({ "wei": wei, "head": head, "chain_id": chain_id }), None))
    })?;
    Ok((r.value["wei"].as_str().unwrap_or("0").to_string(), r.value["head"].as_u64().unwrap_or(0), r.value["chain_id"].as_u64().unwrap_or(0)))
}

pub fn hex_to_decimal(h: &str) -> Option<String> {
    let h = h.trim().trim_start_matches("0x");
    if h.is_empty() {
        return Some("0".into());
    }
    u128::from_str_radix(h, 16).ok().map(|v| v.to_string())
}

fn now_unix() -> u64 {
    SystemTime::now().duration_since(UNIX_EPOCH).map(|d| d.as_secs()).unwrap_or(0)
}

/// One GET through curl, the body as JSON; Err with the words when the host does not answer or answers no JSON.
/// A cached GET (F-E68: through crate::feedclient, one keep-alive client, a failure remembered for its wait, the
/// last good value serving through it): the value and the time it was read.
fn cached(slot: &crate::feedclient::Cache, key: &str, url: &str, what: &str, ttl_s: u64) -> Result<(Value, u64), String> {
    let r = crate::feedclient::cached(slot, key, url, what, ttl_s)?;
    Ok((r.value, r.fetched_unix))
}

/// The ledger's miner view for `address` (cached LEDGER_TTL_S) and the engine's fetch time.
fn miner_view(address: &str, chain_id: u64) -> Result<(Value, u64), String> {
    let key = address.to_lowercase();
    // the per-chain path first (the settlement team, 13:4x UK 13 October 2026: /ledger/<chain_id>/miner-view/<address>.json,
    // served the day its chain's read runs); the unnamed path (today's, chain 7319's) when the chain's path does not
    // answer, and the page then says no ledger exists yet for the mined chain (the project's F-E4)
    if chain_id > 0 {
        let root = LEDGER_URL.trim_end_matches("miner-view/");
        let url = format!("{root}{chain_id}/miner-view/{key}.json");
        if let Ok(v) = cached(&LEDGER_CHAIN, &format!("{key}:{chain_id}"), &url, "the chain's ledger", LEDGER_TTL_S) {
            return Ok(v);
        }
        // the settlement team's rule (15 October 2026, the founder's capture): an address the chain has never carried
        // answers 404 on the per-chain path while the chain's own index answers; that is "no shares carried yet for this
        // address on this chain", never "no ledger", and the unnamed view is not consulted for it
        let index = format!("{root}{chain_id}/miner-view/index.json");
        if let Ok((idx, at)) = cached(&LEDGER_INDEX, &chain_id.to_string(), &index, "the chain's ledger index", LEDGER_TTL_S) {
            if idx.get("chain_id").and_then(|c| c.as_u64()) == Some(chain_id) {
                let head = idx.get("head").and_then(|h| h.as_u64()).unwrap_or(0);
                return Ok((json!({ "chain_id": chain_id, "head": head, "no_shares_yet": true, "network": idx.get("network").cloned().unwrap_or(Value::Null), "address": key }), at));
            }
        }
    }
    let url = format!("{LEDGER_URL}{key}.json");
    cached(&LEDGER, &key, &url, "the ledger", LEDGER_TTL_S)
}

/// The chain feed (cached FEED_TTL_S): (head height, head unix time, the mean block time in seconds), when the
/// feed answers and carries them.
fn chain_head() -> Result<(u64, u64, f64, u64, String), String> {
    let (v, _) = cached(&FEED, "feed", FEED_URL, "the chain feed", FEED_TTL_S)?;
    let height = v.get("head_height").and_then(|h| h.as_u64()).ok_or("the chain feed names no head_height")?;
    // the feed names its chain (chain_id and name); every figure of the scene carries it (the project's read of 13 October 2026)
    let chain_id = v.get("chain_id").and_then(|c| c.as_u64()).unwrap_or(0);
    let name = v.get("name").and_then(|n| n.as_str()).unwrap_or("").to_string();
    let at = v.get("head_utc").and_then(|t| t.as_str()).and_then(parse_utc).unwrap_or(0);
    let mean = v.get("block_time_s_mean").and_then(|m| m.as_f64()).filter(|m| *m > 0.0).unwrap_or(BLOCK_INTERVAL_S as f64);
    Ok((height, at, mean, chain_id, name))
}

/// "2026-10-12T09:42:59Z" to unix seconds; None for any other form.
pub fn parse_utc(s: &str) -> Option<u64> {
    let s = s.trim_end_matches('Z');
    let (date, time) = s.split_once('T')?;
    let mut d = date.split('-').map(|x| x.parse::<i64>().ok());
    let (y, m, day) = (d.next()??, d.next()??, d.next()??);
    let mut t = time.split(':').map(|x| x.split('.').next().unwrap_or("0").parse::<i64>().ok());
    let (hh, mm, ss) = (t.next()??, t.next()??, t.next()??);
    // days from civil (Howard Hinnant's algorithm)
    let (y2, m2) = if m <= 2 { (y - 1, m + 9) } else { (y, m - 3) };
    let era = if y2 >= 0 { y2 } else { y2 - 399 } / 400;
    let yoe = y2 - era * 400;
    let doy = (153 * m2 + 2) / 5 + day - 1;
    let doe = yoe * 365 + yoe / 4 - yoe / 100 + doy;
    let days = era * 146097 + doe - 719468;
    let secs = days * 86400 + hh * 3600 + mm * 60 + ss;
    if secs < 0 { None } else { Some(secs as u64) }
}

/// The blocks of the miner view within BLOCK_WINDOW of the VIEW's own head (the ledger's writer lags the chain;
/// the founder's "no coins" of 11:1x UK, 12 October 2026: windowed against the feed's head, every row fell out),
/// in the document's form. The view's rows (schema igneum/ledger/miner-view/v1, read on the served file) carry
/// height, hash, `carried` (this address's shares in the block), `weight`, `paid_wei`, `sealed`; the state word
/// is derived: paid when the block paid the address, sealed when this address sealed it, carried otherwise. The
/// older names (shares, my_shares, state) are read when a row carries them.
pub fn blocks_of(view: &Value, head: u64) -> Vec<Value> {
    let rows = view.get("blocks").and_then(|b| b.as_array()).cloned().unwrap_or_default();
    let view_head = view.get("head").and_then(|h| h.as_u64()).unwrap_or(0);
    let floor = view_head.max(head).saturating_sub(BLOCK_WINDOW).min(view_head.saturating_sub(BLOCK_WINDOW / 4));
    rows.iter()
        .filter_map(|r| {
            let h = r.get("height").and_then(|h| h.as_u64())?;
            if h < floor {
                return None;
            }
            let num = |k: &str| r.get(k).and_then(|v| v.as_u64().or_else(|| v.as_str().and_then(|s| s.parse().ok()))).unwrap_or(0);
            let wei = |k: &str| r.get(k).map(|v| v.as_str().map(|s| s.to_string()).unwrap_or_else(|| v.to_string())).unwrap_or_else(|| "0".into());
            let paid_wei = wei("paid_wei");
            let state = match r.get("state").and_then(|v| v.as_str()) {
                Some(s) if !s.is_empty() => s.to_string(),
                _ if paid_wei != "0" && !paid_wei.is_empty() => "paid".to_string(),
                _ if r.get("sealed").and_then(|v| v.as_bool()).unwrap_or(false) => "sealed".to_string(),
                _ => "carried".to_string(),
            };
            Some(json!({
                "height": h,
                "hash": r.get("hash").and_then(|v| v.as_str()).unwrap_or(""),
                "my_shares": num("carried").max(num("shares")).max(num("my_shares")),
                "my_weight": num("weight").max(num("my_weight")),
                "state": state,
                "paid_wei": paid_wei,
                // the sealer names the block's lane in the chain card: the row's bond_key (the identity key the header
                // carries), never the coinbase (the project's read of 15:1x UK, 12 October 2026: the miner field differs block to
                // block on devnet9 and is not the sealer)
                "sealer": r.get("bond_key").and_then(|v| v.as_str()).filter(|k| k.trim_start_matches("0x").chars().any(|c| c != '0')).unwrap_or("").to_ascii_lowercase(),
            }))
        })
        .collect()
}

/// The lane the card draws is never empty (the founder's fault of 11:0x UK, 12 October 2026, "no chain showing":
/// an address the ledger held no view for drew nothing). The chain's last LANE_BLOCKS heights up to the head are
/// always in the document; the ledger's rows (this miner's shares in them) stand in for the plain ones of the
/// same height. Sorted by height.
/// 0.2.5 (the showcase team's one form, c21e4095: the stage fills its whole width at the first draw, 15 blocks at 900 px
/// plus two off the left edge, more at a wider window), so the document carries the last 32 heights; each block's sealer
/// is one RPC read, cached.
pub const LANE_BLOCKS: u64 = 32;
pub fn lane_blocks(mut rows: Vec<Value>, head: u64) -> Vec<Value> {
    if head == 0 {
        return rows;
    }
    let floor = head.saturating_sub(LANE_BLOCKS - 1).max(1);
    for h in floor..=head {
        if !rows.iter().any(|r| r.get("height").and_then(|x| x.as_u64()) == Some(h)) {
            rows.push(json!({ "height": h, "hash": "", "my_shares": 0, "my_weight": 0, "state": "", "paid_wei": "0" }));
        }
    }
    rows.sort_by_key(|r| r.get("height").and_then(|x| x.as_u64()).unwrap_or(0));
    rows
}

/// Each block's sealer and time (the project's read of the 0.2.4 capture, 13:5x UK, 12 October 2026: the card drew every
/// block on one lane because the document named no sealer): the ledger's row carries the sealer's bond key; a plain
/// height takes the chain's block through the RPC (eth_getBlockByNumber: the header's bondKey, the identity key
/// beside round, sig and the endorsements, the miner field only when the header carries none, and the timestamp),
/// read once per height and kept in SEALERS (the last SEALER_CACHE heights). A block the RPC does not answer keeps
/// no sealer.
pub const SEALER_CACHE: usize = 256;
static SEALERS: Mutex<Vec<(u64, String, u64)>> = Mutex::new(Vec::new());

fn block_sealer(height: u64) -> Option<(String, u64)> {
    if let Some((_, who, unix)) = SEALERS.lock().unwrap().iter().find(|(h, _, _)| *h == height) {
        return Some((who.clone(), *unix));
    }
    let v = rpc("eth_getBlockByNumber", json!([format!("0x{height:x}"), false])).ok()?;
    let named = |k: &str| v.get(k).and_then(|m| m.as_str()).filter(|s| s.trim_start_matches("0x").chars().any(|c| c != '0'));
    let who = named("bondKey").or_else(|| named("miner"))?.to_ascii_lowercase();
    let unix = v.get("timestamp").and_then(|t| t.as_str()).and_then(|t| u64::from_str_radix(t.trim_start_matches("0x"), 16).ok()).unwrap_or(0);
    let mut c = SEALERS.lock().unwrap();
    c.push((height, who.clone(), unix));
    if c.len() > SEALER_CACHE {
        c.remove(0);
    }
    Some((who, unix))
}

/// The lane's rows with their sealers named: the ledger row's bond key when it carries one, else the chain's
/// block bondKey through the RPC; `unix` the block's time when known.
pub fn with_sealers(rows: Vec<Value>) -> Vec<Value> {
    rows.into_iter()
        .map(|mut r| {
            let h = r.get("height").and_then(|x| x.as_u64()).unwrap_or(0);
            let named = r.get("sealer").and_then(|s| s.as_str()).map(|s| !s.is_empty()).unwrap_or(false);
            if !named && h > 0 {
                if let Some((who, unix)) = block_sealer(h) {
                    r["sealer"] = Value::String(who);
                    if unix > 0 {
                        r["unix"] = json!(unix);
                    }
                }
            }
            r
        })
        .collect()
}

/// The scene's state word: live while the feed's head moved within two intervals of its mean, else stale with
/// the words; a feed that does not answer reads stale with its error.
pub fn state_word(head_at: u64, now: u64, mean_s: f64, feed_error: Option<&str>) -> (String, String) {
    if let Some(e) = feed_error {
        return ("stale".into(), format!("the chain feed is not read: {e}"));
    }
    let age = now.saturating_sub(head_at) as f64;
    if head_at > 0 && age > 2.0 * mean_s {
        ("stale".into(), format!("no new block for {} s (the mean is {:.0} s)", age as u64, mean_s))
    } else {
        ("live".into(), String::new())
    }
}

/// The miner view as the ledger serves it, for the window's Earnings rows (GET /api/ledger): the view itself, or
/// `{ "error": <words> }` in the window's own form when the ledger does not answer or no address is set.
pub fn ledger(shared: &Arc<Shared>) -> Value {
    let (address, chain_id) = { let st = shared.state.lock().unwrap(); (st.address.display.clone(), st.source.chain_id) };
    if address.is_empty() {
        return json!({ "error": "no payout address yet" });
    }
    match miner_view(&address, chain_id) {
        Ok((v, _)) => v,
        Err(e) => json!({ "error": e }),
    }
}

/// The scene's document.
pub fn fetch(shared: &Arc<Shared>) -> Value {
    let (address, network, shares, accepted, rejected, source_height, source_chain_id) = {
        let st = shared.state.lock().unwrap();
        // a pool's height is its last JOB height, the block being sealed (one above the chain's head); the scene's head is
        // the chain's, so a pool source counts one below (the joint next-block sample of 21:06Z, 13 October 2026: the
        // stage's tip stood one above the chain with no seal time, and the caption lost its "N s ago")
        let src_head = if st.source.kind == "pool" { st.source.height.saturating_sub(1) } else { st.source.height };
        (st.address.display.clone(), st.network_name.clone(), st.mining.shares.clone(), st.mining.accepted_session, st.mining.rejected_session, src_head, st.source.chain_id)
    };
    let now = now_unix();
    let (feed_head, feed_at, mean, feed_error, feed_chain_id, feed_name) = match chain_head() {
        Ok((h, at, m, c, n)) => (h, at, m, None, c, n),
        Err(e) => (0, 0, BLOCK_INTERVAL_S as f64, Some(e), 0, String::new()),
    };
    let (view, fetched_unix, ledger_error) = if address.is_empty() {
        (None, 0, Some("no address yet".to_string()))
    } else {
        match miner_view(&address, source_chain_id) {
            Ok((v, t)) => (Some(v), t, None),
            // a view the ledger does not hold yet (no block carries this address's shares): no fault, the chain's
            // own lane with none of its blocks lit (the founder's "no chain showing", 11:0x UK, 12 October 2026)
            Err(e) if e.contains("answered no JSON (0 bytes)") => (None, 0, None),
            Err(e) => (None, 0, Some(e)),
        }
    };
    // the head: the feed's, never below the miner's own newest share (a share lands at a height the chain has)
    let own = shares.last().map_or(0, |s| s.height);
    let ledger_head = view.as_ref().and_then(|v| v.get("head")).and_then(|h| h.as_u64()).unwrap_or(0);
    // the ledger names its chain; a view of another chain than the feed's (the settlement team's ledger on devnet9
    // while the feed and the pool are on the old public test network, chain 7320, the project's read of 13 October 2026) never moves the scene's head and
    // is marked so the page names it
    let ledger_chain_id = view.as_ref().and_then(|v| v.get("chain_id")).and_then(|c| c.as_u64()).unwrap_or(0);
    let ledger_network = view.as_ref().and_then(|v| v.get("network")).and_then(|n| n.as_str()).unwrap_or("").to_string();
    // the chain of record for the ledger is the pool's (the project's F-E4, 13 October 2026: the Earnings ledger and the
    // address panel follow the pool's chain), the feed's when the pool names none
    let mined_chain_id = if source_chain_id > 0 { source_chain_id } else { feed_chain_id };
    let ledger_other_chain = ledger_chain_id > 0 && mined_chain_id > 0 && ledger_chain_id != mined_chain_id;
    let no_shares_yet = view.as_ref().map_or(false, |v| v.get("no_shares_yet").and_then(|b| b.as_bool()).unwrap_or(false));
    let ledger_head_for_scene = if ledger_other_chain { 0 } else { ledger_head };
    // a ledger of another chain lights no block of the lane and pays no figure here
    let view = if ledger_other_chain || no_shares_yet { None } else { view };
    let ledger_error = if ledger_other_chain { Some(format!("no ledger yet for chain {mined_chain_id}: the ledger's miner view names chain {ledger_chain_id}")) } else if no_shares_yet { Some(format!("no shares carried yet for this address on chain {mined_chain_id}; the ledger lists it from its first accepted share")) } else { ledger_error };
    // the pool's own job height too (the project's read of the 0.2.4 capture, 13:5x UK, 12 October 2026: the next-block
    // bar named a height the pool line had passed while the feed lagged two blocks)
    // F-E12 (the founder's photo, 13 October 2026): the scene is the mined chain's; when the feed names another chain
    // its head, clock and mean block time are not the scene's (the mini drew chain 7320's block 3,653 while mining
    // chain 7357 at 281)
    let feed_is_mined = feed_chain_id == 0 || mined_chain_id == 0 || feed_chain_id == mined_chain_id;
    let (feed_head, feed_at, mean, feed_error) = if feed_is_mined { (feed_head, feed_at, mean, feed_error) } else { (0, 0, BLOCK_INTERVAL_S as f64, Some(format!("the chain feed is chain {feed_chain_id}'s, not the mined chain {mined_chain_id}'s"))) };
    // a share's height is the block being MINED, one above the chain's head (the 22:05Z evidence run, 13 October 2026:
    // the first share on a new job raised the scene's head above the chain and a filler block with no seal time became
    // the stage's tip for seconds at a time, its caption losing "N s ago"); the head is the chain's, never a share's
    let _ = own;
    let head = feed_head.max(ledger_head_for_scene).max(source_height);
    let (word, detail) = state_word(feed_at, now, mean, feed_error.as_deref());
    let totals = view.as_ref().and_then(|v| v.get("totals")).cloned().unwrap_or(json!({}));
    let num = |k: &str| totals.get(k).and_then(|v| v.as_u64().or_else(|| v.as_str().and_then(|s| s.parse().ok()))).unwrap_or(0);
    // the paid total is the view's own top-level paid_wei (the chain's balance steps joined to this address's
    // shares); totals.paid is a share count
    let paid_wei = view.as_ref().and_then(|v| v.get("paid_wei")).map(|v| v.as_str().map(|s| s.to_string()).unwrap_or_else(|| v.to_string())).unwrap_or_else(|| "0".into());
    // the chain's balance of the payout address, the tile's headline; the words when it cannot be read
    let (balance_wei, balance_head, balance_chain_id, balance_error) = if address.is_empty() {
        (Value::Null, 0, 0, Some("no payout address yet".to_string()))
    } else {
        match chain_balance(&address) {
            Ok((w, h, c)) => (Value::String(w), h, c, None),
            Err(e) => (Value::Null, 0, 0, Some(e)),
        }
    };
    let behind = if ledger_other_chain { 0 } else { head.saturating_sub(ledger_head) };
    // the blocks of the view that paid this address (the Earnings page's "paid in N blocks")
    let paid_blocks: u64 = view
        .as_ref()
        .and_then(|v| v.get("blocks").and_then(|b| b.as_array()))
        .map(|rows| rows.iter().filter(|r| r.get("paid_wei").map(|v| v.as_str().map(|s| s != "0" && !s.is_empty()).unwrap_or_else(|| v.as_u64().unwrap_or(0) > 0)).unwrap_or(false)).count() as u64)
        .unwrap_or(0);
    // the ledger's paid wei over the last day's blocks (BLOCKS_24H at the target interval) for the Earnings page's
    // window; the view's rows carry heights, not times
    let paid_24h: u128 = view
        .as_ref()
        .and_then(|v| v.get("blocks").and_then(|b| b.as_array()))
        .map(|rows| {
            rows.iter()
                .filter(|r| r.get("height").and_then(|h| h.as_u64()).map(|h| h + BLOCKS_24H > ledger_head.max(head)).unwrap_or(false))
                .filter_map(|r| r.get("paid_wei").and_then(|v| v.as_str().map(|s| s.parse::<u128>().ok()).unwrap_or_else(|| v.as_u64().map(|n| n as u128))))
                .sum()
        })
        .unwrap_or(0);
    json!({
        "address": address,
        "network": network,
        // the scene of 65a3856b (15 October 2026) reads target_s and hour_mean_s from the head: the chain's target and
        // the feed's mean gap over its window (the site's network source fills both from status.json's chain.interval)
        "head": { "height": head, "at_unix": if feed_at > 0 { feed_at } else { now }, "block_interval_s": mean.round() as u64, "mean_s": mean, "hour_mean_s": mean, "target_s": BLOCK_INTERVAL_S, "pow": true },
        "shares": shares,
        "blocks": with_sealers(lane_blocks(view.as_ref().map(|v| blocks_of(v, head)).unwrap_or_default(), head)),
        "totals": { "accepted": accepted, "rejected": rejected, "paid_wei": paid_wei, "paid_24h_wei": paid_24h.to_string(), "paid_shares": num("paid"), "paid_blocks": paid_blocks, "in_carry": num("in_carry"), "pooled": num("pooled") },
        "chain": { "url": rpc_url(), "chain_id": balance_chain_id, "balance_wei": balance_wei, "balance_head": balance_head, "error": balance_error },
        "state": { "word": word, "detail": detail },
        "ledger": { "url": LEDGER_URL, "error": ledger_error, "head": ledger_head, "chain_id": ledger_chain_id, "network": ledger_network, "other_chain": ledger_other_chain, "mined_chain_id": mined_chain_id, "behind": behind, "read_at": view.as_ref().and_then(|v| v.get("read_at")).cloned().unwrap_or(Value::Null), "fetched_unix": fetched_unix },
        "feed": { "url": FEED_URL, "error": feed_error, "head": feed_head, "head_unix": feed_at, "chain_id": feed_chain_id, "name": feed_name },
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn the_lane_is_never_empty_and_the_ledgers_rows_stand_in() {
        // no view: the lane's LANE_BLOCKS heights up to the head, plain (32 since the scene's band of 0.2.10)
        let lane = lane_blocks(vec![], 1456);
        assert_eq!(lane.len(), LANE_BLOCKS as usize);
        assert_eq!(lane[0]["height"], 1456 - LANE_BLOCKS + 1);
        assert_eq!(lane[LANE_BLOCKS as usize - 1]["height"], 1456);
        assert_eq!(lane[LANE_BLOCKS as usize - 1]["my_shares"], 0);
        // the ledger's row at 1450 keeps its shares; the rest are filled; sorted
        let lane = lane_blocks(vec![json!({ "height": 1450, "my_shares": 3, "state": "paid" })], 1456);
        assert_eq!(lane.len(), LANE_BLOCKS as usize);
        let row = lane.iter().find(|r| r["height"] == 1450).unwrap();
        assert_eq!(row["my_shares"], 3);
        assert!(lane.windows(2).all(|w| w[0]["height"].as_u64() < w[1]["height"].as_u64()));
        // a head near the genesis fills from 1; no head, nothing invented
        assert_eq!(lane_blocks(vec![], 3).len(), 3);
        assert!(lane_blocks(vec![], 0).is_empty());
        // a row that names its sealer keeps it without a chain read (the RPC is never called for a named row)
        let named = with_sealers(vec![json!({ "height": 1450, "sealer": "0xabc", "my_shares": 3 })]);
        assert_eq!(named[0]["sealer"], "0xabc");
        // the ledger's bond key becomes the row's sealer; the coinbase never does; an all-zero key names nothing
        let view = json!({"head": 1000, "blocks": [{"height": 999, "coinbase": "0xFB4774E3615235724F0171229B394DE2F84D6F75", "bond_key": "0x755C1F2D5BCFAE636C290F0567D84A706AA241F6FFDD5CEA820AFF638732B48D", "carried": 1, "paid_wei": "5"}, {"height": 998, "coinbase": "0xfb47", "bond_key": "0x0000", "carried": 1}]});
        let b = blocks_of(&view, 1000);
        assert_eq!(b[0]["sealer"], "0x755c1f2d5bcfae636c290f0567d84a706aa241f6ffdd5cea820aff638732b48d");
        assert_eq!(b[1]["sealer"], "");
    }

    #[test]
    fn the_blocks_keep_the_window_and_the_ledgers_words() {
        // the served view's own names (carried, weight, paid_wei, sealed), the state derived
        let view = json!({"head": 1000, "blocks": [
            {"height": 999, "hash": "0xaa", "carried": 7, "weight": 350, "paid_wei": "0", "sealed": false},
            {"height": 998, "hash": "0xab", "carried": 2, "weight": 100, "paid_wei": "0", "sealed": true},
            {"height": 950, "hash": "0xbb", "carried": 1, "weight": 50, "paid_wei": "1200", "sealed": false},
            {"height": 10, "hash": "0xcc", "carried": 1, "weight": 50, "paid_wei": "0", "sealed": false}
        ]});
        let b = blocks_of(&view, 1000);
        assert_eq!(b.len(), 3, "the block older than the window is left out");
        assert_eq!(b[0]["my_shares"], 7);
        assert_eq!(b[0]["my_weight"], 350);
        assert_eq!(b[0]["state"], "carried");
        assert_eq!(b[1]["state"], "sealed");
        assert_eq!(b[2]["state"], "paid");
        assert_eq!(b[2]["paid_wei"], "1200");
        // the ledger lags the chain: the window's floor follows the feed's head, but never above a quarter window under
        // the view's own head, so the rows just under the ledger's head stay in while the oldest ones go
        let b = blocks_of(&view, 1080);
        assert_eq!(b.len(), 2, "the feed's head ahead of the ledger's keeps the rows under the view's head");
        assert_eq!(b[0]["height"], 999);
        assert_eq!(b[1]["height"], 998);
        // the older names still read
        let old = json!({"head": 1000, "blocks": [{"height": 999, "shares": 3, "state": "paid", "paid_wei": "5"}]});
        assert_eq!(blocks_of(&old, 1000)[0]["my_shares"], 3);
    }

    #[test]
    fn hex_quantities_read_as_decimal_strings() {
        assert_eq!(hex_to_decimal("0x0"), Some("0".into()));
        assert_eq!(hex_to_decimal("0x"), Some("0".into()));
        assert_eq!(hex_to_decimal("0x99bf18be07167d2a6c"), Some("2836121808075898038892".into()));
        assert_eq!(hex_to_decimal("zz"), None);
    }

    #[test]
    fn the_feed_time_parses_and_the_state_word_reads_stale_after_two_intervals() {
        assert_eq!(parse_utc("2026-10-12T09:42:59Z"), Some(1791798179));
        assert_eq!(parse_utc("1970-01-01T00:00:00Z"), Some(0));
        assert_eq!(parse_utc("not a time"), None);
        let at = 1791798179;
        assert_eq!(state_word(at, at + 30, 21.0, None).0, "live");
        assert_eq!(state_word(at, at + 43, 21.0, None).0, "stale");
        assert!(state_word(at, at + 43, 21.0, None).1.contains("43 s"));
        assert_eq!(state_word(0, at, 21.0, Some("the chain feed did not answer in 8 s")).0, "stale");
    }
}
