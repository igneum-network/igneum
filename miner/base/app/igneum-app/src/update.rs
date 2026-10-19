//! Clock sources for the skew check and log uploads to the intake. (The update check moved to src/ota.rs and
//! src/manifest.rs with the over-the-air updater.)
//! Both go through curl (macOS ships it; Windows 10 1803 and later ship curl.exe), so the engine carries no TLS stack.

use std::process::Command;
use std::time::Duration;

/// The network's idea of now from an HTTPS Date header (1 s resolution), as unix seconds. The second clock source,
/// independent of the node.
pub fn https_time(url: &str) -> Option<f64> {
    let out = crate::detect::run_timeout(Command::new(crate::platform::tool("curl")).args(["-sI", "--max-time", "10", url]), None, Duration::from_secs(12))?;
    let line = out.lines().find(|l| l.to_ascii_lowercase().starts_with("date:"))?;
    parse_http_date(line[5..].trim())
}

/// "Sun, 04 Oct 2026 11:17:47 GMT" -> unix seconds.
pub fn parse_http_date(s: &str) -> Option<f64> {
    let parts: Vec<&str> = s.split_whitespace().collect();
    if parts.len() < 5 {
        return None;
    }
    let day: i64 = parts[1].parse().ok()?;
    let month = ["jan", "feb", "mar", "apr", "may", "jun", "jul", "aug", "sep", "oct", "nov", "dec"].iter().position(|m| parts[2].to_ascii_lowercase().starts_with(m))? as i64 + 1;
    let year: i64 = parts[3].parse().ok()?;
    let hms: Vec<i64> = parts[4].split(':').filter_map(|p| p.parse().ok()).collect();
    if hms.len() != 3 {
        return None;
    }
    let days = days_from_civil(year, month, day);
    Some((days * 86400 + hms[0] * 3600 + hms[1] * 60 + hms[2]) as f64)
}

fn days_from_civil(y: i64, m: i64, d: i64) -> i64 {
    let y = if m <= 2 { y - 1 } else { y };
    let era = if y >= 0 { y } else { y - 399 } / 400;
    let yoe = y - era * 400;
    let mp = (m + 9) % 12;
    let doy = (153 * mp + 2) / 5 + d - 1;
    let doe = yoe * 365 + yoe / 4 - yoe / 100 + doy;
    era * 146_097 + doe - 719_468
}

/// True once the node's exec follower holds a record (igneum_getExecStatus's executedTipHash is set). Any error or an
/// unreachable node reads false: the block sample waits rather than asks.
pub fn exec_has_record(evm_port: u16) -> bool {
    crate::execrpc::has_record(evm_port)
}

/// The reading of an igneum_getExecStatus reply: a record is held when executedTipHash is a non-null string.
pub fn exec_status_has_record(reply: &str) -> bool {
    serde_json::from_str::<serde_json::Value>(reply).ok().map(|v| crate::execrpc::status_has_record(v.get("result").unwrap_or(&serde_json::Value::Null))).unwrap_or(false)
}

/// The latest block's timestamp (unix seconds) from the node's Ethereum JSON-RPC (the execution layer mirrors the
/// consensus block times). The first clock source: local time against what the peers produced.
pub fn latest_block_time(evm_port: u16) -> Option<f64> {
    // an earlier release/an earlier release (ledger N7): through the one gate, which asks nothing of a follower without a record
    let block = crate::execrpc::call(evm_port, "eth_getBlockByNumber", serde_json::json!(["latest", false]), Duration::from_secs(5)).ok()?;
    let ts = block.get("timestamp")?.as_str()?;
    u64::from_str_radix(ts.trim_start_matches("0x"), 16).ok().map(|t| t as f64)
}

#[cfg(test)]
mod tests {
    /// Ledger N7: the clock sample must not ask eth_getBlockByNumber of a follower with no record
    #[test]
    fn exec_status_gate() {
        assert!(super::exec_status_has_record(r#"{"jsonrpc":"2.0","id":1,"result":{"executedTip":"0x38a5","executedTipHash":"0xa3ae37ec31b9227c3855a1b25ba50ee9f6815bf00b0befcc8836b430baf04343"}}"#));
        assert!(!super::exec_status_has_record(r#"{"jsonrpc":"2.0","id":1,"result":{"executedTip":"0x0","executedTipHash":null}}"#));
        assert!(!super::exec_status_has_record(r#"{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"method not found"}}"#));
        assert!(!super::exec_status_has_record(""));
        assert!(!super::exec_status_has_record("not json"));
    }
    #[test]
    fn http_date() {
        assert_eq!(super::parse_http_date("Sun, 04 Oct 2026 11:17:47 GMT"), Some(1_791_112_667.0));
        assert_eq!(super::parse_http_date("Thu, 01 Jan 1970 00:00:00 GMT"), Some(0.0));
    }
}

/// Posts the last 256 KB of a log file as {label, machine, run_id, lines}, as site/api/log.mjs expects.
pub fn upload_log(url: &str, key: &str, label: &str, machine: &str, run_id: &str, path: &std::path::Path, header: &str) -> bool {
    if url.is_empty() || key.is_empty() {
        return false;
    }
    let Ok(mut data) = std::fs::read(path) else { return false };
    if data.is_empty() {
        return false;
    }
    if data.len() > 262_144 {
        data = data.split_off(data.len() - 262_144);
    }
    // second guard (R4.3.8): no line that carries a dashboard token leaves the machine
    let tail: String = String::from_utf8_lossy(&data).lines().filter(|l| !crate::platform::carries_token(l)).map(|l| crate::platform::redact(l)).collect::<Vec<_>>().join("\n");
    // the first line of every upload names the app, the machine and the node (the console parses it)
    let text = format!("{header}\n{tail}");
    upload_text(url, key, label, machine, run_id, &text)
}

/// One upload of ready text (a fault report, a job's lines): the same intake, the same token guard.
pub fn upload_text(url: &str, key: &str, label: &str, machine: &str, run_id: &str, text: &str) -> bool {
    if url.is_empty() || key.is_empty() || text.is_empty() {
        return false;
    }
    let text: String = text.lines().filter(|l| !crate::platform::carries_token(l)).collect::<Vec<_>>().join("\n");
    let body = serde_json::json!({ "label": label, "machine": machine, "run_id": run_id, "lines": text });
    let tmp = std::env::temp_dir().join(format!("igneum-upload-{}-{}.json", std::process::id(), label));
    if std::fs::write(&tmp, body.to_string()).is_err() {
        return false;
    }
    let out = crate::detect::run_timeout(
        Command::new(crate::platform::tool("curl")).args([
            "-sS", "--max-time", "60", "-X", "POST", url,
            "-H", "Content-Type: application/json",
            "-H", &format!("x-igneum-key: {key}"),
            "--data-binary", &format!("@{}", tmp.display()),
            "-o", if cfg!(windows) { "NUL" } else { "/dev/null" },
            "-w", "%{http_code}",
        ]),
        None,
        Duration::from_secs(65),
    );
    let _ = std::fs::remove_file(&tmp);
    out.map(|o| o.trim().starts_with("200")).unwrap_or(false)
}
