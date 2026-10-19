//! The page bridge (docs/product-b/DESIGN.md, 1.7): a second loopback listener (port 26811 by default) that
//! websites reach with their Origin as their identity. Reads are answered from the node. Anything else becomes an
//! ask the window shows; the site's request waits for the person's answer (up to three minutes) and gets the
//! result or the refusal. Nothing is signed without a yes in the window; a bare hash is never signed.

use std::collections::HashMap;
use std::sync::{Arc, Condvar, Mutex};
use std::time::{Duration, Instant};

use serde::{Deserialize, Serialize};
use serde_json::{json, Value};

use igneum_wallet_core::{bridge::Ask, vault::now_unix, Address};

pub const DEFAULT_PORT: u16 = 26811;
pub const ASK_TIMEOUT: Duration = Duration::from_secs(180);

#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct Site {
    pub origin: String,
    pub connected_unix: u64,
    pub last_seen_unix: u64,
    /// the site's token, minted at the approved connect and required on every later call (G8 and A-IPC, 12
    /// October 2026): a local process cannot ride a site's approval; a record from before the token has none and
    /// connects again once
    #[serde(default)]
    pub token: Option<String>,
}

#[derive(Clone, Debug, Serialize)]
pub struct PendingAsk {
    pub id: u64,
    pub origin: String,
    pub host: String,
    pub ask: Ask,
    pub title: String,
    pub lines: Vec<String>,
    pub yes: &'static str,
    pub created_unix: u64,
    #[serde(skip)]
    pub created: Instant,
}

#[derive(Default)]
pub struct Asks {
    next_id: u64,
    pending: Vec<PendingAsk>,
    answers: HashMap<u64, bool>,
    /// asks refused by the cap, by host, for the Websites card (the security review's A-MS6; wallet-security's MS-6 numbers)
    refused: HashMap<String, u64>,
}

/// The cap on the ask queue (12 October 2026): one open ask per site, and a bound on the whole, so a flood of
/// requests reaches the window as one ask at a time and the rest are refused at once with a code the site reads.
pub const MAX_PENDING_TOTAL: usize = 64;
/// The bridge's refusal code for a capped ask (the EIP-1193 range is taken; -32005 is the provider's "limit exceeded").
pub const ASK_REFUSED_CODE: i64 = -32005;

/// Why an ask was refused before it reached the window.
pub struct AskRefused {
    pub reason: &'static str,
}

/// The ask queue shared by the bridge threads and the window's routes.
pub struct AskQueue {
    inner: Mutex<Asks>,
    cv: Condvar,
}

impl AskQueue {
    pub fn new() -> Arc<Self> {
        Arc::new(AskQueue { inner: Mutex::new(Asks::default()), cv: Condvar::new() })
    }

    /// Posts an ask and waits for the person's answer; Ok(None) when nobody answered in time; Err when the cap
    /// refuses it before the window sees it (one open ask per site; the whole queue bounded).
    pub fn ask(&self, origin: &str, ask: Ask) -> std::result::Result<Option<bool>, AskRefused> {
        let host = host_of(origin);
        let id;
        {
            let mut g = self.inner.lock().map_err(|_| AskRefused { reason: "the ask queue is unavailable" })?;
            if g.pending.iter().any(|p| p.origin == origin) {
                *g.refused.entry(host.clone()).or_insert(0) += 1;
                return Err(AskRefused { reason: "an ask is already open for this site; answer it in the wallet window first" });
            }
            if g.pending.len() >= MAX_PENDING_TOTAL {
                *g.refused.entry(host.clone()).or_insert(0) += 1;
                return Err(AskRefused { reason: "too many asks pending in the wallet; try again when the window has answered" });
            }
            g.next_id += 1;
            id = g.next_id;
            g.pending.push(PendingAsk {
                id,
                origin: origin.to_string(),
                host: host.clone(),
                title: ask.title(&host),
                lines: ask.lines(),
                yes: ask.yes_word(),
                ask,
                created_unix: now_unix(),
                created: Instant::now(),
            });
        }
        let deadline = Instant::now() + ASK_TIMEOUT;
        let mut g = self.inner.lock().map_err(|_| AskRefused { reason: "the ask queue is unavailable" })?;
        loop {
            if let Some(a) = g.answers.remove(&id) {
                return Ok(Some(a));
            }
            let now = Instant::now();
            if now >= deadline {
                g.pending.retain(|p| p.id != id);
                return Ok(None);
            }
            let (ng, _) = self.cv.wait_timeout(g, deadline - now).map_err(|_| AskRefused { reason: "the ask queue is unavailable" })?;
            g = ng;
        }
    }

    /// The asks refused by the cap, by host, since the engine started.
    pub fn refused(&self) -> Vec<(String, u64)> {
        self.inner.lock().map(|g| { let mut v: Vec<_> = g.refused.iter().map(|(h, n)| (h.clone(), *n)).collect(); v.sort(); v }).unwrap_or_default()
    }

    /// The window answers; the waiting request wakes.
    pub fn decide(&self, id: u64, yes: bool) -> bool {
        let mut g = match self.inner.lock() {
            Ok(g) => g,
            Err(_) => return false,
        };
        let before = g.pending.len();
        g.pending.retain(|p| p.id != id);
        if g.pending.len() == before {
            return false;
        }
        g.answers.insert(id, yes);
        self.cv.notify_all();
        true
    }

    pub fn pending(&self) -> Vec<PendingAsk> {
        self.inner.lock().map(|g| g.pending.clone()).unwrap_or_default()
    }

    /// Drops asks nobody will answer any more (the request side timed out).
    pub fn sweep(&self) {
        if let Ok(mut g) = self.inner.lock() {
            g.pending.retain(|p| p.created.elapsed() < ASK_TIMEOUT + Duration::from_secs(5));
        }
    }
}

pub fn host_of(origin: &str) -> String {
    let s = origin.trim();
    let s = s.strip_prefix("https://").or_else(|| s.strip_prefix("http://")).unwrap_or(s);
    s.split('/').next().unwrap_or(s).to_string()
}

/// The JSON-RPC error shapes sites expect (EIP-1193 codes).
pub fn rpc_error(id: &Value, code: i64, message: &str) -> Value {
    json!({"jsonrpc": "2.0", "id": id, "error": {"code": code, "message": message}})
}

pub fn rpc_result(id: &Value, result: Value) -> Value {
    json!({"jsonrpc": "2.0", "id": id, "result": result})
}

/// The provider script a site loads from the bridge (or ships itself): an EIP-1193 provider over fetch, announced
/// through EIP-6963 so wallets are discovered without fighting over window.ethereum.
pub fn provider_script(port: u16) -> String {
    PROVIDER_JS.replace("__PORT__", &port.to_string())
}

const PROVIDER_JS: &str = r##"// Igneum Wallet page provider (EIP-1193 over the local bridge; announced through EIP-6963).
(function () {
  'use strict';
  if (window.igneum && window.igneum.isIgneum) return;
  var BASE = 'http://127.0.0.1:__PORT__';
  var listeners = {};
  var accounts = [];
  var chainId = null;
  function emit(ev, data) { (listeners[ev] || []).forEach(function (f) { try { f(data); } catch (e) {} }); }
  var nextId = 1;
  var provider = {
    isIgneum: true,
    isConnected: function () { return chainId !== null; },
    request: function (args) {
      var id = nextId++;
      // the site's token: minted by the wallet at the approved connect (the X-Igneum-Site header of that reply),
      // kept for this tab's session, sent on every later call; without it a signing request is refused with no ask
      var headers = { 'Content-Type': 'application/json' };
      var token = null; try { token = sessionStorage.getItem('igneum-site-token'); } catch (e) {}
      if (token) headers['X-Igneum-Site'] = token;
      return fetch(BASE + '/rpc', { method: 'POST', headers: headers, body: JSON.stringify({ jsonrpc: '2.0', id: id, method: args.method, params: args.params || [] }) })
        .then(function (r) { var t = r.headers.get('x-igneum-site'); if (t) { try { sessionStorage.setItem('igneum-site-token', t); } catch (e) {} } return r.json(); }, function () { throw { code: 4900, message: 'Igneum Wallet is not running on this machine' }; })
        .then(function (j) {
          if (j.error) { var e = new Error(j.error.message); e.code = j.error.code; throw e; }
          if (args.method === 'eth_requestAccounts' || args.method === 'eth_accounts') {
            var before = accounts.join(','); accounts = j.result || [];
            if (accounts.join(',') !== before) emit('accountsChanged', accounts);
          }
          if (args.method === 'eth_chainId') { if (chainId !== j.result) { var first = chainId === null; chainId = j.result; if (first) emit('connect', { chainId: chainId }); else emit('chainChanged', chainId); } }
          return j.result;
        });
    },
    on: function (ev, f) { (listeners[ev] = listeners[ev] || []).push(f); return provider; },
    removeListener: function (ev, f) { listeners[ev] = (listeners[ev] || []).filter(function (g) { return g !== f; }); return provider; },
    enable: function () { return provider.request({ method: 'eth_requestAccounts' }); }
  };
  window.igneum = provider;
  var info = { uuid: 'd0b7f2a8-4a1e-4c3e-9f55-igneum-wallet-0', name: 'Igneum Wallet', icon: 'data:image/svg+xml;utf8,' + encodeURIComponent('<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><path fill="#F2541B" d="M12 2 5 13l7 9 7-9z"/></svg>'), rdns: 'network.igneum.wallet' };
  function announce() { try { window.dispatchEvent(new CustomEvent('eip6963:announceProvider', { detail: Object.freeze({ info: info, provider: provider }) })); } catch (e) {} }
  window.addEventListener('eip6963:requestProvider', announce);
  announce();
  if (!window.ethereum) { try { window.ethereum = provider; } catch (e) {} }
})();
"##;

pub fn address_list(addr: Option<Address>) -> Value {
    match addr {
        Some(a) => json!([a.to_checksum()]),
        None => json!([]),
    }
}
