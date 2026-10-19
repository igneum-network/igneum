//! The one path to the node's execution-layer JSON-RPC (ledger N7, 7 October 2026, the project's rule for an earlier release).
//!
//! A node before the exec RPC bounds fix (every an earlier release node) dies when a method that resolves a block number or indexes
//! the record vector is asked while its exec follower holds no record: `rpc.rs` indexes `records[0]` (or slices
//! `records[1..=0]`) on an empty vector, the panic hook exits the process, and the app restarts it. the first Windows machine crash-looped on
//! two callers in one night (the clock sample's eth_getBlockByNumber, then the prover's igneum_getAssignedShards after the
//! node read synced seconds before a slow follower loaded). The node-side fix ships with the an earlier release node; a an earlier release app on a
//! an earlier release node must be safe by itself, so every exec RPC call the app makes goes through [`call`]: a method in
//! [`SAFE_ON_EMPTY`] goes out at once; any other waits until igneum_getExecStatus reports an executed tip. The unit test
//! below enumerates the callers: no other file may build an exec JSON-RPC request, and every method name in the tree must
//! be classified here, so a new caller or method cannot bypass the gate.
use serde_json::{json, Value};
use std::time::Duration;

/// Methods that index nothing on an empty exec state (read from the an earlier release node's rpc.rs): safe at any time.
pub const SAFE_ON_EMPTY: &[&str] = &[
    "eth_chainId", "eth_blockNumber", "eth_syncing", "igneum_getExecStatus", "igneum_getProvingStatus", "igneum_getNodeInfo",
    // the engine's balance read (an earlier release): an account lookup at the latest state, nothing indexed by block number
    "eth_getBalance",
    // the scene's header poll on the public chain RPC (the newest header, no block number resolved by the node's record)
    "eth_getHeaderByNumber",
];

/// Methods the app sends that resolve a block number, index or slice the record vector, or simulate at a block: held until
/// the follower holds a record. Every method literal outside this module must be in one of the two lists.
pub const GATED: &[&str] = &[
    "eth_getBlockByNumber", "igneum_getAssignedShards", "igneum_getProofRecords", "igneum_getSegmentRecords", "igneum_getSegmentStatement",
    "igneum_getProofBytes", "igneum_getSegmentProofBytes", "igneum_exportSegments", "igneum_submitProofRecord", "igneum_submitSegmentRecord",
    "igneum_getFinalityWeights",
    // an earlier release's live page reads recent blocks by number through the same path (src/live.rs); held like the rest
    "igneum_getRecentBlocks",
];

/// One JSON-RPC POST to 127.0.0.1:<evm_port> through curl (the engine carries no HTTP client); the body goes through a file
/// so a large export request is not an argument. The reply's `result` (null allowed), or the error's message.
/// The node every read goes to (docs/product-a/OLD-BASE.md step 3): the engine runs no node, so the URL is the one the
/// user named as the work source (a node by URL), set by the engine at start and on a source change; while it is
/// unset the reads go to 127.0.0.1:<evm_port> as before (the port the callers still pass).
static NODE_URL: std::sync::Mutex<Option<String>> = std::sync::Mutex::new(None);

pub fn set_node_url(url: Option<String>) {
    *NODE_URL.lock().unwrap() = url.filter(|u| !u.trim().is_empty()).map(|u| u.trim().trim_end_matches('/').to_string());
}

pub fn node_url_or(evm_port: u16) -> String {
    NODE_URL.lock().unwrap().clone().unwrap_or_else(|| format!("http://127.0.0.1:{evm_port}"))
}

/// F-E68: one keep-alive POST through the supervisor's client (one open connection per host), never a curl process
/// per call; an empty or unparsable body is no answer (a stopped node's socket still accepts the connection; the
/// probe must read that as silence, 7 October 2026).
fn keepalive_post(url: &str, body: &str, timeout: Duration, who: &str) -> Result<String, String> {
    let parsed = igneum_supervisor::net::parse_url(url).map_err(|e| format!("{who}: {e}"))?;
    let (_status, _headers, raw) = igneum_supervisor::net::http_keepalive("POST", &parsed, Some("application/json"), body.as_bytes(), timeout).map_err(|e| format!("{who} did not answer ({e})"))?;
    Ok(String::from_utf8_lossy(&raw).into_owned())
}

fn post(evm_port: u16, method: &str, params: Value, timeout: Duration) -> Result<Value, String> {
    let body = request_body(method, params, json!(1)).to_string();
    let url = node_url_or(evm_port);
    let out = keepalive_post(&url, &body, timeout, &format!("{method}: the node's RPC"))?;
    let v: Value = serde_json::from_str(&out).map_err(|e| format!("{method}: the node's RPC did not answer ({e})"))?;
    if let Some(err) = v.get("error") {
        return Err(format!("{method}: {}", err.get("message").and_then(|m| m.as_str()).unwrap_or("error")));
    }
    Ok(v.get("result").cloned().unwrap_or(Value::Null))
}


/// True once the node's exec follower holds a record (igneum_getExecStatus's executedTipHash is set). Any error or an
/// unreachable node reads false: the gated call waits rather than asks.
pub fn has_record(evm_port: u16) -> bool {
    match post(evm_port, "igneum_getExecStatus", json!([]), Duration::from_secs(5)) {
        Ok(r) => status_has_record(&r),
        Err(_) => false,
    }
}

/// The reading of an igneum_getExecStatus result: a record is held when executedTipHash is a non-empty string.
pub fn status_has_record(result: &Value) -> bool {
    result.get("executedTipHash").and_then(|h| h.as_str()).map(|h| !h.is_empty()).unwrap_or(false)
}

/// The engine's readiness probe (every 5 s): did the node's RPC answer at all, and does the follower hold a record.
/// The first is the node watchdog's sign of life; the second, with `synced`, is the workers' start gate.
pub fn probe(evm_port: u16) -> (bool, bool) {
    match post(evm_port, "igneum_getExecStatus", json!([]), Duration::from_secs(5)) {
        Ok(r) => (true, status_has_record(&r)),
        Err(e) => (!e.contains("did not answer"), false),
    }
}

/// The gate: a safe method goes out; a gated one waits for a record; an unclassified method is refused (add it to a list).
/// The one place a JSON-RPC request body is built (the gate test reads every other file for one).
pub fn request_body(method: &str, params: Value, id: Value) -> Value {
    json!({ "jsonrpc": "2.0", "id": id, "method": method, "params": params })
}

/// A call to the public chain RPC by URL (the scene's reads: rpc.igneum.network, or IGNEUM_APP_RPC_URL): the public
/// endpoint serves the chain whole, so the node-record gate does not apply; the method must still be classified.
pub fn call_url(url: &str, method: &str, params: Value, timeout: Duration) -> Result<Value, String> {
    if !SAFE_ON_EMPTY.contains(&method) && !GATED.contains(&method) {
        return Err(format!("{method}: not classified in execrpc (safe on an empty state, or gated); add it before calling"));
    }
    let body = request_body(method, params, json!(1)).to_string();
    let out = keepalive_post(url, &body, timeout, "the chain's RPC")?;
    let v: Value = serde_json::from_str(out.trim()).map_err(|_| format!("the chain's RPC answered no JSON ({} bytes)", out.len()))?;
    if let Some(e) = v.get("error") {
        return Err(format!("the chain's RPC refused {method}: {}", e.get("message").and_then(|m| m.as_str()).unwrap_or("error")));
    }
    v.get("result").cloned().ok_or_else(|| format!("the chain's RPC answered {method} without a result"))
}

pub fn call(evm_port: u16, method: &str, params: Value, timeout: Duration) -> Result<Value, String> {
    if SAFE_ON_EMPTY.contains(&method) {
        return post(evm_port, method, params, timeout);
    }
    if !GATED.contains(&method) {
        return Err(format!("{method}: not classified in execrpc (safe on an empty state, or gated); add it before calling"));
    }
    if !has_record(evm_port) {
        return Err(format!("{method}: the node's execution layer holds no record yet"));
    }
    post(evm_port, method, params, timeout)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn status_reading() {
        assert!(status_has_record(&json!({"executedTip":"0x38a5","executedTipHash":"0xa3ae37ec"})));
        assert!(!status_has_record(&json!({"executedTip":"0x0","executedTipHash":null})));
        assert!(!status_has_record(&json!({})));
        assert!(!status_has_record(&json!({"executedTipHash":""})));
    }

    #[test]
    fn lists_are_disjoint_and_sorted_enough() {
        for m in GATED {
            assert!(!SAFE_ON_EMPTY.contains(m), "{m} in both lists");
        }
    }

    /// Ledger N7: every exec JSON-RPC request the app builds goes through this module, and every exec method name in the
    /// tree is classified here. A new direct caller (a file that builds a "jsonrpc" POST) or an unclassified method fails.
    #[test]
    fn every_caller_goes_through_the_gate() {
        let src = std::path::Path::new(env!("CARGO_MANIFEST_DIR")).join("src");
        // every eth_* or igneum_* identifier on a line (a hand scanner: no regex crate in this binary)
        fn methods_in(line: &str) -> Vec<String> {
            // ASCII-only scan over char boundaries: a token of [A-Za-z0-9_] that starts with eth_ or igneum_
            let mut out = Vec::new();
            let mut token = String::new();
            let mut flush = |t: &mut String| {
                if t.starts_with("eth_") || t.starts_with("igneum_") { out.push(t.clone()); }
                t.clear();
            };
            for ch in line.chars() {
                if ch.is_ascii_alphanumeric() || ch == '_' { token.push(ch); } else { flush(&mut token); }
            }
            flush(&mut token);
            out
        }
        let mut offenders = Vec::new();
        let mut unclassified = Vec::new();
        for entry in std::fs::read_dir(&src).unwrap() {
            let path = entry.unwrap().path();
            if path.extension().and_then(|e| e.to_str()) != Some("rs") { continue; }
            let name = path.file_name().unwrap().to_string_lossy().to_string();
            let text = std::fs::read_to_string(&path).unwrap();
            // a JSON-RPC REQUEST names a method next to "jsonrpc" (replies and test fixtures carry "result" or "error");
            // only this module may build one
            if name != "execrpc.rs" {
                for (i, line) in text.lines().enumerate() {
                    let l = line.replace('\\', "");
                    if l.contains("\"jsonrpc\"") && l.contains("\"method\"") && !l.contains("\"result\"") && !l.contains("\"error\"") {
                        offenders.push(format!("{name}:{}", i + 1));
                    }
                }
            }
            for (i, line) in text.lines().enumerate() {
                if line.trim_start().starts_with("//") { continue; }
                for m in methods_in(line) {
                    let m = m.as_str();
                    // identifiers that are not RPC methods: crate and file names (igneum_app, igneum_miner, ...) carry no camel-case method part
                    let looks_like_method = m.contains("_get") || m.contains("_submit") || m.contains("_export") || m.contains("_estimate") || m.contains("_send") || m == "eth_chainId" || m == "eth_blockNumber" || m == "eth_syncing";
                    if looks_like_method && !SAFE_ON_EMPTY.contains(&m) && !GATED.contains(&m) {
                        unclassified.push(format!("{name}:{}: {m}", i + 1));
                    }
                }
            }
        }
        assert!(offenders.is_empty(), "exec JSON-RPC built outside execrpc.rs: {offenders:?}");
        assert!(unclassified.is_empty(), "exec methods not classified in execrpc.rs: {unclassified:?}");
    }
}
