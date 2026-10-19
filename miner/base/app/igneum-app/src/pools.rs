//! The Pools page's source (DESIGN 2.7, PA-06; the founder's order of 20:4x UK, 13 October 2026): GET /api/pools
//! gives every pool the window lists, each from the pool's own disclosure document (`/.well-known/igneum-pool.json`,
//! read by the engine through curl like the scene's feeds and cached a minute; the window never fetches another
//! origin), beside what the pool cannot self-report: the independent probe's reliability (the network team's status feed,
//! `surfaces.pool.stratum_availability_7d_pct` by region, for the pool of record) and the explorer's concentration
//! (not published yet: the field says so and the 33 percent warning band is armed for the day it is). The list is
//! the ecosystem lead's signed `pools.json` the day it serves (POOLS_URL); until then the built-in list of the
//! project's pools, every one labelled "operated by the project". Nothing preselected; solo is the window's own card.
use serde_json::{json, Value};
use std::sync::Mutex;
use std::time::{SystemTime, UNIX_EPOCH};

/// The signed list of every pool that files (PA-06), the day the ecosystem lead serves it.
pub const POOLS_URL: &str = "https://build.igneum.network/feed/pools.json";
/// the network team's status feed: the pool of record's probe figure.
pub const STATUS_URL: &str = "https://build.igneum.network/feed/7322/status.json";
/// The documents and the feed are read again after this many seconds.
pub const TTL_S: u64 = 60;

/// The built-in list until the signed one serves: (label, host, port, tls, the document's URL or "", operated by the project).
const BUILT_IN: &[(&str, &str, u16, bool, &str, bool)] = &[
    ("Igneum Labs pool", "pool.igneum.network", 3342, false, "https://pool.igneum.network/.well-known/igneum-pool.json", true),
    ("pool-b2 (vectorwork7)", "91.98.220.77", 3334, false, "", true),
];

static DOCS: Mutex<Vec<(String, Option<crate::feedclient::Slot>)>> = Mutex::new(Vec::new());
static LIST: crate::feedclient::Cache = crate::feedclient::cache();
static STATUS: crate::feedclient::Cache = crate::feedclient::cache();

fn now_unix() -> u64 {
    SystemTime::now().duration_since(UNIX_EPOCH).map(|d| d.as_secs()).unwrap_or(0)
}

/// F-E68: one keep-alive read through crate::feedclient
fn get_json(url: &str, what: &str) -> Result<Value, String> {
    crate::feedclient::get_json(url, what).map(|(v, _)| v)
}

/// F-E68: one keep-alive read per cadence, a failure remembered for its wait (the last good list serves through it)
fn cached_one(slot: &crate::feedclient::Cache, url: &str, what: &str) -> Result<Value, String> {
    crate::feedclient::cached(slot, url, url, what, TTL_S).map(|r| r.value)
}

fn cached_doc(url: &str) -> Result<Value, String> {
    crate::feedclient::cached_many(&DOCS, url, TTL_S, now_unix(), || crate::feedclient::get_json(url, "the pool's disclosure document")).map(|r| r.value)
}

/// The probe figure for a pool from the status feed: the regions' percentages and the state, when the feed names it.
fn probe_for(status: &Value, host: &str, port: u16) -> Value {
    let pool = status.get("surfaces").and_then(|s| s.get("pool"));
    let addr = pool.and_then(|p| p.get("address")).and_then(|a| a.as_str()).unwrap_or("");
    if addr != format!("{host}:{port}") {
        return json!({ "published": false, "note": "probe: not published (the feed probes the pool of record only)" });
    }
    let by_region = pool.and_then(|p| p.get("stratum_availability_7d_pct")).cloned().unwrap_or(Value::Null);
    let state = pool.and_then(|p| p.get("state")).and_then(|s| s.as_str()).unwrap_or("").to_string();
    let opened = status.get("network").and_then(|n| n.get("opened_utc")).and_then(|s| s.as_str()).unwrap_or("").to_string();
    json!({ "published": by_region.is_object(), "window": "the trailing 7 days, bounded by the chain's opening", "by_region": by_region, "state": state, "opened_utc": opened, "source": STATUS_URL })
}

/// The window's list.
pub fn fetch() -> Value {
    let status = cached_one(&STATUS, STATUS_URL, "the status feed").ok();
    // the signed list (ecosystem, main aff4bfb9: feed/pools.json, pools[] with name, stratum host:port, chain_id,
    // operated_by, disclosure_url, the disclosure document inline or null, concentration {share_of_work_7d, status});
    // the built-in list when the feed cannot be reached
    let signed = cached_one(&LIST, POOLS_URL, "the signed pool list").ok().filter(|v| v.get("pools").map_or(false, |p| p.is_array()));
    let (list, list_source): (Vec<(String, String, u16, bool, String, bool, Option<Value>, Option<Value>, u64)>, &str) = match signed {
        Some(v) => (
            v["pools"].as_array().unwrap().iter().filter_map(|p| {
                let stratum = p.get("stratum")?.as_str()?.to_string();
                let (host, port) = stratum.rsplit_once(':')?;
                let port: u16 = port.parse().ok()?;
                let operator = p.get("operated_by").and_then(|o| o.as_str()).unwrap_or("");
                let project = operator.contains("Igneum Labs") || p.get("disclosure").and_then(|d| d.get("labs_operated")).and_then(|b| b.as_bool()).unwrap_or(false);
                Some((p.get("name").and_then(|n| n.as_str()).unwrap_or(&stratum).to_string(), host.to_string(), port, false, p.get("disclosure_url").and_then(|d| d.as_str()).unwrap_or("").to_string(), project, p.get("disclosure").filter(|d| d.is_object()).cloned(), p.get("concentration").cloned(), p.get("chain_id").and_then(|c| c.as_u64()).unwrap_or(0)))
            }).collect(),
            "the signed pools.json (the project's feed, refreshed every minute)",
        ),
        None => (BUILT_IN.iter().map(|(n, h, p, t, d, pr)| (n.to_string(), h.to_string(), *p, *t, d.to_string(), *pr, None, None, 0)).collect(), "the built-in list (the signed pools.json could not be read)"),
    };
    let mut pools = Vec::new();
    for (label, host, port, tls, doc_url, project, inline_doc, conc, chain_id) in list {
        let doc = match inline_doc {
            Some(d) => Ok(d),
            None if doc_url.is_empty() => Err("no disclosure document served to the public".to_string()),
            None => cached_doc(&doc_url),
        };
        let (fee_bps, scheme, window_n, maturity, min_payout_wei, payout_schedule, payout_gas, idle_s, rate_limit, uptime_stmt, operator, contact, regions, doc_error) = match &doc {
            Ok(d) => (
                d.get("fee_bps").and_then(|v| v.as_u64()), d.get("scheme").and_then(|v| v.as_str()).map(|s| s.to_string()), d.get("window_n").and_then(|v| v.as_u64()),
                d.get("maturity_blocks").and_then(|v| v.as_u64()), d.get("min_payout_wei").map(|v| v.as_str().map(|s| s.to_string()).unwrap_or_else(|| v.to_string())),
                d.get("payout_schedule").and_then(|v| v.as_str()).map(|s| s.to_string()), d.get("payout_gas").and_then(|v| v.as_str()).map(|s| s.to_string()),
                d.get("session_idle_seconds").and_then(|v| v.as_u64()), d.get("share_rate_limit").and_then(|v| v.as_u64()),
                d.get("uptime_30d_pct").and_then(|v| v.as_f64()), d.get("operator").and_then(|v| v.as_str()).map(|s| s.to_string()), d.get("contact").and_then(|v| v.as_str()).map(|s| s.to_string()),
                d.get("regions").cloned().unwrap_or(Value::Null), None,
            ),
            Err(e) => (None, None, None, None, None, None, None, None, None, None, None, None, Value::Null, Some(e.clone())),
        };
        let probe = status.as_ref().map(|s| probe_for(s, &host, port)).unwrap_or_else(|| json!({ "published": false, "note": "probe: the status feed did not answer" }));
        pools.push(json!({
            "label": label, "host": host, "port": port, "tls": tls, "address": format!("{host}:{port}"),
            "project": project, "operator": operator, "contact": contact, "regions": regions,
            "fee_bps": fee_bps, "scheme": scheme, "window_n": window_n, "maturity_blocks": maturity, "min_payout_wei": min_payout_wei,
            "payout_schedule": payout_schedule, "payout_gas": payout_gas, "session_idle_seconds": idle_s, "share_rate_limit": rate_limit,
            "reliability": { "statement_30d_pct": uptime_stmt, "probe": probe },
            "chain_id": chain_id,
            "concentration": {
                "published": conc.as_ref().and_then(|c| c.get("share_of_work_7d")).map_or(false, |v| v.is_number()),
                "share_7d_pct": conc.as_ref().and_then(|c| c.get("share_of_work_7d")).and_then(|v| v.as_f64()).map(|v| if v <= 1.0 { v * 100.0 } else { v }),
                "warn_above_pct": 33,
                "note": conc.as_ref().and_then(|c| c.get("status")).and_then(|s| s.as_str()).unwrap_or("not published").to_string(),
            },
            "document_url": doc_url, "document_error": doc_error,
        }));
    }
    // the order: concentration ascending (none published: all equal), then fee ascending; nothing preselected
    pools.sort_by(|a, b| {
        let ca = a["concentration"]["share_7d_pct"].as_f64().unwrap_or(0.0);
        let cb = b["concentration"]["share_7d_pct"].as_f64().unwrap_or(0.0);
        ca.partial_cmp(&cb).unwrap_or(std::cmp::Ordering::Equal).then(a["fee_bps"].as_u64().unwrap_or(u64::MAX).cmp(&b["fee_bps"].as_u64().unwrap_or(u64::MAX)))
    });
    json!({ "pools": pools, "list_source": list_source, "list_url": POOLS_URL, "status_url": STATUS_URL, "read_unix": now_unix() })
}
