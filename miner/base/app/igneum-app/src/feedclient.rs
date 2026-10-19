//! F-E68 (the project's order of 13:1x UK, 15 October 2026; reliability's read of the mini: the 0.2.15 app spawned five to
//! six curl processes a second against the feeds, every window request a process while the feed answered nothing).
//! The engine reads each public feed once per its cadence through ONE keep-alive client
//! (igneum_supervisor::net::http_keepalive: one open connection per host), never a process per read; a read that
//! fails or answers no JSON is remembered for its cadence, so the window's next request is answered from the memory
//! (the last good value when there is one, the words when there is none) and not by another read; a slow or failing
//! feed backs its cadence off, doubling to BACKOFF_MAX_S, and a good quick read restores it. No read happens without
//! a window asking: a closed window reads nothing.
use serde_json::Value;
use std::sync::Mutex;
use std::time::{Duration, Instant, SystemTime, UNIX_EPOCH};

/// a read slower than this backs the cadence off
pub const SLOW_S: f64 = 2.0;
/// the longest wait between reads of a slow or failing feed
pub const BACKOFF_MAX_S: u64 = 30;
pub const TIMEOUT: Duration = Duration::from_secs(6);

#[derive(Debug)]
pub struct Slot {
    pub key: String,
    /// the last good value, kept through failures
    pub value: Option<Value>,
    pub date: Option<String>,
    /// when the last good value was read
    pub good_unix: u64,
    /// when the last read (good or not) was made: the wait counts from here
    pub read_unix: u64,
    pub err: Option<String>,
    pub wait_s: u64,
}
pub type Cache = Mutex<Option<Slot>>;
pub const fn cache() -> Cache {
    Mutex::new(None)
}

#[derive(Debug)]
pub struct Reading {
    pub value: Value,
    /// the upstream Date header of the read that gave the value
    pub date: Option<String>,
    /// when the value was read (an older time while the feed fails and the last good value serves)
    pub fetched_unix: u64,
}

pub fn now_unix() -> u64 {
    SystemTime::now().duration_since(UNIX_EPOCH).map(|d| d.as_secs()).unwrap_or(0)
}

/// GET `url` as JSON through the keep-alive client: the value and the upstream Date header. A status other
/// than 200 is an Err naming it.
pub fn get_json(url: &str, what: &str) -> Result<(Value, Option<String>), String> {
    let parsed = igneum_supervisor::net::parse_url(url).map_err(|e| format!("{what}: {e}"))?;
    let (status, headers, body) = igneum_supervisor::net::http_keepalive("GET", &parsed, None, &[], TIMEOUT).map_err(|e| format!("{what} did not answer: {e}"))?;
    if status != 200 {
        return Err(format!("{what} answered {status}"));
    }
    let text = String::from_utf8_lossy(&body);
    let v: Value = serde_json::from_str(text.trim()).map_err(|_| format!("{what} answered no JSON ({} bytes)", body.len()))?;
    let date = headers.iter().find(|(k, _)| k.eq_ignore_ascii_case("date")).map(|(_, v)| v.clone());
    Ok((v, date))
}

/// The next wait after a read: the cadence after a good quick read; doubled (from the cadence, to BACKOFF_MAX_S)
/// after a slow or failed one.
pub fn next_wait(prev_wait_s: u64, ttl_s: u64, ok: bool, slow: bool) -> u64 {
    if ok && !slow {
        ttl_s
    } else {
        (prev_wait_s.max(ttl_s).saturating_mul(2)).clamp(ttl_s.max(1), BACKOFF_MAX_S.max(ttl_s))
    }
}

/// The cached read of `url` under `key`: the slot's value while its wait has not passed (the last good one, or the
/// failure's words when there is none), else one read through `fetch` and the slot updated.
pub fn cached_with<F: FnOnce() -> Result<(Value, Option<String>), String>>(slot: &Cache, key: &str, ttl_s: u64, now: u64, fetch: F) -> Result<Reading, String> {
    let mut g = slot.lock().unwrap_or_else(|p| p.into_inner());
    read_slot(&mut g, key, ttl_s, now, fetch)
}

/// `cached_with` over a list of slots keyed by name (the pools' disclosure documents, one slot per URL).
pub fn cached_many<F: FnOnce() -> Result<(Value, Option<String>), String>>(slots: &Mutex<Vec<(String, Option<Slot>)>>, key: &str, ttl_s: u64, now: u64, fetch: F) -> Result<Reading, String> {
    let mut g = slots.lock().unwrap_or_else(|p| p.into_inner());
    let i = match g.iter().position(|(k, _)| k == key) {
        Some(i) => i,
        None => { g.push((key.to_string(), None)); g.len() - 1 }
    };
    read_slot(&mut g[i].1, key, ttl_s, now, fetch)
}

fn read_slot<F: FnOnce() -> Result<(Value, Option<String>), String>>(g: &mut Option<Slot>, key: &str, ttl_s: u64, now: u64, fetch: F) -> Result<Reading, String> {
    if let Some(c) = g.as_ref() {
        if c.key == key && now.saturating_sub(c.read_unix) < c.wait_s {
            return match (&c.value, &c.err) {
                (Some(v), _) => Ok(Reading { value: v.clone(), date: c.date.clone(), fetched_unix: c.good_unix }),
                (None, Some(e)) => Err(e.clone()),
                (None, None) => Err("no reading yet".into()),
            };
        }
    }
    let prev = g.take().filter(|c| c.key == key);
    let prev_wait = prev.as_ref().map(|c| c.wait_s).unwrap_or(ttl_s);
    let t0 = Instant::now();
    let r = fetch();
    let slow = t0.elapsed().as_secs_f64() > SLOW_S;
    match r {
        Ok((v, date)) => {
            *g = Some(Slot { key: key.into(), value: Some(v.clone()), date: date.clone(), good_unix: now, read_unix: now, err: None, wait_s: next_wait(prev_wait, ttl_s, true, slow) });
            Ok(Reading { value: v, date, fetched_unix: now })
        }
        Err(e) => {
            let (value, date, good_unix) = prev.map(|c| (c.value, c.date, c.good_unix)).unwrap_or((None, None, now));
            *g = Some(Slot { key: key.into(), value: value.clone(), date: date.clone(), good_unix, read_unix: now, err: Some(e.clone()), wait_s: next_wait(prev_wait, ttl_s, false, slow) });
            match value {
                Some(v) => Ok(Reading { value: v, date, fetched_unix: good_unix }),
                None => Err(e),
            }
        }
    }
}

/// `cached_with` reading `url` through the keep-alive client.
pub fn cached(slot: &Cache, key: &str, url: &str, what: &str, ttl_s: u64) -> Result<Reading, String> {
    cached_with(slot, key, ttl_s, now_unix(), || get_json(url, what))
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;
    use std::cell::Cell;

    #[test]
    fn the_wait_is_the_cadence_after_a_good_read_and_doubles_to_the_cap_on_failures() {
        assert_eq!(next_wait(1, 1, true, false), 1);
        assert_eq!(next_wait(16, 1, true, false), 1);
        assert_eq!(next_wait(1, 1, false, false), 2);
        assert_eq!(next_wait(2, 1, false, false), 4);
        assert_eq!(next_wait(16, 1, false, false), 30);
        assert_eq!(next_wait(30, 1, false, false), 30);
        assert_eq!(next_wait(5, 5, true, true), 10);
        assert_eq!(next_wait(1, 5, false, false), 10);
    }

    #[test]
    fn a_failed_read_is_remembered_for_its_wait_and_the_last_good_value_serves_through_it() {
        static SLOT: Cache = cache();
        let calls = Cell::new(0);
        let fetch_ok = || { calls.set(calls.get() + 1); Ok((json!({"n": 1}), Some("d1".into()))) };
        let fetch_bad = || { calls.set(calls.get() + 1); Err::<(Value, Option<String>), String>("the feed answered no JSON (0 bytes)".into()) };
        // a good read at t=100, served again at t=100 without a read
        let r = cached_with(&SLOT, "k", 1, 100, fetch_ok).unwrap();
        assert_eq!(r.value["n"], 1);
        assert_eq!(r.date.as_deref(), Some("d1"));
        let _ = cached_with(&SLOT, "k", 1, 100, fetch_ok).unwrap();
        assert_eq!(calls.get(), 1);
        // the feed dies at t=101: one read, the old value served, the wait now 2 s
        let r = cached_with(&SLOT, "k", 1, 101, fetch_bad).unwrap();
        assert_eq!(r.value["n"], 1);
        assert_eq!(r.fetched_unix, 100);
        assert_eq!(calls.get(), 2);
        // t=102: inside the 2 s wait, no read (the storm F-E68 named: a read per request)
        let _ = cached_with(&SLOT, "k", 1, 102, fetch_bad).unwrap();
        assert_eq!(calls.get(), 2);
        // t=103: one more read, the wait 4 s
        let _ = cached_with(&SLOT, "k", 1, 103, fetch_bad).unwrap();
        assert_eq!(calls.get(), 3);
        assert_eq!(SLOT.lock().unwrap().as_ref().unwrap().wait_s, 4);
        let _ = cached_with(&SLOT, "k", 1, 106, fetch_bad).unwrap();
        assert_eq!(calls.get(), 3);
        // t=107: the feed is back, the cadence restored
        let r = cached_with(&SLOT, "k", 1, 107, fetch_ok).unwrap();
        assert_eq!(r.fetched_unix, 107);
        assert_eq!(SLOT.lock().unwrap().as_ref().unwrap().wait_s, 1);
        // a new key reads at once
        let _ = cached_with(&SLOT, "k2", 1, 107, fetch_ok).unwrap();
        assert_eq!(calls.get(), 5);
    }

    #[test]
    fn a_failure_with_no_good_value_is_the_words_without_a_read_until_its_wait_passes() {
        static SLOT: Cache = cache();
        let calls = Cell::new(0);
        let fetch_bad = || { calls.set(calls.get() + 1); Err::<(Value, Option<String>), String>("the status feed answered 404".into()) };
        assert_eq!(cached_with(&SLOT, "a", 5, 10, fetch_bad).unwrap_err(), "the status feed answered 404");
        assert_eq!(cached_with(&SLOT, "a", 5, 12, fetch_bad).unwrap_err(), "the status feed answered 404");
        assert_eq!(calls.get(), 1);
        assert_eq!(SLOT.lock().unwrap().as_ref().unwrap().wait_s, 10);
        let _ = cached_with(&SLOT, "a", 5, 20, fetch_bad);
        assert_eq!(calls.get(), 2);
    }
}
