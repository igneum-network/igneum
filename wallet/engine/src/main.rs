// the Desk's answer is one json! tree past the macro's default depth
#![recursion_limit = "512"]
//! The engine (docs/product-b/DESIGN.md, 1.1): the local service behind the window. It keeps the public state,
//! talks to the node, composes transactions, measures the settlement inputs, and asks the signer for signatures.
//! It never holds a key. The window reaches it on 127.0.0.1 under a per-run token path, printed once on stdout as
//! `ENGINE {"url": ...}` for the host to read.
//!
//! Routes (all under `/t/<token>/`): `api/state`, `api/create`, `api/create/confirm`, `api/import`, `api/unlock`,
//! `api/lock`, `api/send/quote`, `api/send`, `api/envelope/export`, `api/envelope/import`, `api/tx/<hash>`,
//! `api/backed-up`, `api/reveal`, `api/settings`, `api/remove`, `api/activity`, `api/quit`; anything else under the
//! token is a static file of the window.

mod biometric;
mod bridge;
mod history_scan;
mod http;
mod node_source;
mod signer_client;
mod update;


use std::collections::{HashMap, VecDeque};
use std::fs;
use std::path::PathBuf;
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};

use rand_core::{OsRng, RngCore};
use serde::{Deserialize, Serialize};
use serde_json::{json, Value};

use igneum_wallet_core::{
    chain::Chain,
    display,
    envelope::{SignedEnvelope, UnsignedEnvelope},
    hex,
    settlement::{self, Observations, Policy, State as TxState},
    tx::{Eip1559Tx, Transaction},
    vault::now_unix,
    Address, Error, Result, U256,
};

use bridge::{AskQueue, Site};
use http::{Request, Response};
use igneum_wallet_core::bridge::{self as bridge_words, Ask};
use igneum_wallet_rpc::Rpc;
use signer_client::SignerClient;

const VERSION: &str = env!("CARGO_PKG_VERSION");

#[derive(Clone, Serialize, Deserialize)]
struct Settings {
    rpc: String,
    /// true once a person (or --rpc) named the node: it is then used as it is and no other source is probed
    /// (node_source.rs); false while `rpc` is the chain's public endpoint the build chose
    #[serde(default)]
    rpc_named: bool,
    idle_lock_min: u64,
    /// the one automatic Touch ID or Windows Hello sheet on the first arrival at the lock screen (the previous
    /// wallet's setting, default on; read by the page's lock screen)
    #[serde(default = "default_true")]
    ask_on_open: bool,
    /// updates downloaded by themselves (the founder's rule; off means the card offers the download)
    #[serde(default = "default_true")]
    auto_update: bool,
    /// dark unless the person chose otherwise (the founder's order of 15 October 2026: every run without a saved
    /// choice is dark whatever the system theme); theme_chosen marks a choice made on the Settings page, so a
    /// settings file written by an earlier version with the old default "system" reads as no choice
    theme: String,
    #[serde(default)]
    theme_chosen: bool,
    #[serde(default = "default_true")]
    bridge_on: bool,
    #[serde(default)]
    tokens: Vec<Value>,
}

fn default_true() -> bool {
    true
}

impl Default for Settings {
    fn default() -> Self {
        // a fresh wallet reads devnet7, the launch-equivalent chain, through its public read-only endpoint (the
        // founder's order of 11 October 2026, 19:0x by the project's clock; devnet5 was the first public build's); every tool passes --rpc for a dev node, and a write goes through the endpoint's rules (reads only:
        // a send is refused there with the endpoint's words until the protocol team opens a write path)
        Settings { rpc: igneum_wallet_core::chain::PUBLIC_7322_RPC.into(), rpc_named: false, idle_lock_min: biometric::IDLE_LOCK_MIN, ask_on_open: true, auto_update: true, theme: "dark".into(), theme_chosen: false, bridge_on: true, tokens: vec![] }
    }
}

#[derive(Clone, Serialize, Deserialize)]
struct WatchFile {
    watch_only: bool,
    address: Address,
    created_unix: u64,
}

#[derive(Clone, Serialize, Deserialize)]
struct Sent {
    hash: String,
    to: Address,
    value: U256,
    sent_unix: u64,
    sentences: Vec<String>,
    /// what the call moves beyond the transaction's value (a bond's release moves the bond; participant 3's
    /// "for 0 IGN" on the release row, 12 October 2026): the settlement band and its words read this when set
    #[serde(default)]
    moves: Option<U256>,
}

struct Quote {
    envelope: UnsignedEnvelope,
    created: Instant,
    moves: Option<U256>,
}

#[derive(Clone, Default)]
struct NodeInfo {
    ok: bool,
    error: String,
    block: u64,
    head_unix: u64,
    chain_id: u64,
    balance: Option<U256>,
    /// the genesis hash the node serves, read once per endpoint: the pin that names the chain (D-B14)
    genesis: String,
}

struct Engine {
    data_dir: PathBuf,
    ui_dir: Option<PathBuf>,
    /// the common passwords refused by name (ST-4): app/ui/common-passwords.txt, one a line, read at start;
    /// empty until the file ships (the founder's word on the outside file)
    common_passwords: Vec<String>,
    settings: Settings,
    chain: Chain,
    signer: SignerClient,
    watch: Option<WatchFile>,
    signer_status: Value,
    quotes: HashMap<String, Quote>,
    sent: Vec<Sent>,
    node: NodeInfo,
    /// where the chain comes from (node_source.rs): chosen at start, again when the source is lost
    source: node_source::Source,
    /// the biometric gate (biometric.rs): the challenges the host confirms and the actions consume once
    gate: biometric::Gate,
    /// what the host reported and the enrolment state (the sealed file is the host's; its presence is read here)
    bio: biometric::BiometricState,
    /// the host's token: printed once on stdout (the HOST line) for the window host alone; marks the calls only
    /// the host may make (X-Igneum-Host); empty means no host
    host_token: String,
    /// the updater's schedule and state (update.rs); the checks and downloads run on their own thread
    update: update::Updater,
    /// the mining identity's public half once made in this session (api/identity; derived, never stored)
    identity: Option<String>,
    /// consecutive failed polls held before the node reads lost
    poll_misses: u32,
    /// the shares feed's last answer and its time (two seconds' cache)
    shares_cache: Option<(Instant, Value)>,
    /// what happened to this address on the chain (history_scan.rs), advanced a few blocks per poll
    scan: Option<history_scan::Scan>,
    /// (number, hash) of recent heads, for reorganisation detection
    headers: VecDeque<(u64, String)>,
    /// (unix time, depth) of reorganisations this engine saw itself
    reorgs: Vec<(u64, u64)>,
    last_activity: Instant,
    policy: Policy,
    asks: Arc<AskQueue>,
    sites: Vec<Site>,
    bridge_port: u16,
}

impl Engine {
    /// A new password's rule (ST-4): the minimum with the count, then the common list by name. The meter is the
    /// page's (the same arithmetic in app.js); `api/password/check` serves the engine's reading of a candidate for
    /// the page's final word before create or import.
    fn check_password(&self, pw: &str) -> Result<()> {
        igneum_wallet_core::password::check_minimum(pw)?;
        igneum_wallet_core::password::check_not_trivial(pw)?;
        igneum_wallet_core::password::check_common(pw, self.common_passwords.iter().map(|s| s.as_str()))
    }

    fn password_reading(&self, pw: &str) -> Value {
        use igneum_wallet_core::password;
        let (bits, grade) = password::estimate(pw);
        let refusal = self.check_password(pw).err().map(|e| e.to_string());
        json!({"bits": bits, "grade": grade.word(), "minimum": password::MINIMUM, "count": pw.chars().count(),
               "ok": refusal.is_none(), "refusal": refusal, "sentence": password::SENTENCE,
               "common_list": self.common_passwords.len()})
    }

    fn vault_path(&self) -> PathBuf {
        self.data_dir.join("vault.json")
    }

    fn watch_path(&self) -> PathBuf {
        self.data_dir.join("watch.json")
    }

    fn history_path(&self) -> PathBuf {
        self.data_dir.join("history.json")
    }

    fn settings_path(&self) -> PathBuf {
        self.data_dir.join("settings.json")
    }

    fn sites_path(&self) -> PathBuf {
        self.data_dir.join("sites.json")
    }

    fn save_sites(&self) -> Result<()> {
        fs::write(self.sites_path(), serde_json::to_vec_pretty(&self.sites)?)?;
        Ok(())
    }

    /// A site is known when its record holds a token and the request carries that token (G8 and A-IPC): a record
    /// from before the token, or a request without it, connects again (one ask, which mints the token).
    fn site_known(&self, origin: &str, token: Option<&str>) -> bool {
        match token {
            Some(t) if !t.is_empty() => self.sites.iter().any(|s| s.origin == origin && s.token.as_deref() == Some(t)),
            _ => false,
        }
    }

    /// The approved connect: a fresh token for the site, the record made or replaced, the token returned once in
    /// the reply's header.
    fn connect_site(&mut self, origin: &str) -> String {
        let mut bytes = [0u8; 16];
        OsRng.fill_bytes(&mut bytes);
        let token = hex::encode(&bytes[..]);
        self.sites.retain(|s| s.origin != origin);
        self.sites.push(Site { origin: origin.to_string(), connected_unix: now_unix(), last_seen_unix: now_unix(), token: Some(token.clone()) });
        let _ = self.save_sites();
        token
    }

    fn touch_site(&mut self, origin: &str) {
        if let Some(s) = self.sites.iter_mut().find(|s| s.origin == origin) {
            s.last_seen_unix = now_unix();
        }
    }

    /// A finished transaction from the bridge or the window, into the history.
    fn record_sent(&mut self, hash_hex: &str, env: &UnsignedEnvelope) {
        self.sent.push(Sent {
            hash: hash_hex.to_string(),
            to: env.transaction.to().unwrap_or_default(),
            value: env.transaction.value(),
            sent_unix: now_unix(),
            sentences: env.sentences.clone(),
            moves: None,
        });
        let _ = self.save_history();
    }

    /// Completes a site's transaction with the node's nonce, fees and gas and wraps it with its sentences.
    fn compose(&self, from: Address, p: &bridge_words::SendParams) -> Result<UnsignedEnvelope> {
        let rpc = self.rpc();
        let nonce = rpc.nonce(&from)?;
        let fees = rpc.fees()?;
        let gas = match p.gas {
            Some(g) => g,
            None => {
                let call = json!({"from": from.to_checksum(), "to": p.to.map(|a| a.to_checksum()), "value": p.value.to_hex(), "data": hex::encode(&p.data)});
                let plain = p.data.is_empty() && p.to.map(|a| !rpc.has_code(&a.to_checksum()).unwrap_or(true)).unwrap_or(false);
                if plain { 21000 } else { rpc.estimate_gas(&call)? * 12 / 10 }
            }
        };
        let tx = Transaction::Eip1559(Eip1559Tx {
            chain_id: self.chain.chain_id,
            nonce,
            max_priority_fee_per_gas: fees.max_priority_fee_per_gas,
            max_fee_per_gas: fees.max_fee_per_gas,
            gas_limit: gas,
            to: p.to,
            value: p.value,
            data: p.data.clone(),
        });
        Ok(UnsignedEnvelope::new(&self.chain.name, from, tx))
    }

    fn save_settings(&self) -> Result<()> {
        fs::write(self.settings_path(), serde_json::to_vec_pretty(&self.settings)?)?;
        Ok(())
    }

    fn save_history(&self) -> Result<()> {
        fs::write(self.history_path(), serde_json::to_vec_pretty(&self.sent)?)?;
        Ok(())
    }

    fn refresh_signer_status(&mut self) -> Result<()> {
        self.signer_status = self.signer.request("status", json!({}))?;
        Ok(())
    }

    fn address(&self) -> Option<Address> {
        if let Some(w) = &self.watch {
            return Some(w.address);
        }
        self.signer_status.get("address").and_then(|a| a.as_str()).and_then(|a| Address::parse(a).ok())
    }

    fn unlocked(&self) -> bool {
        self.watch.is_some() || self.signer_status.get("unlocked").and_then(|u| u.as_bool()).unwrap_or(false)
    }

    fn phase(&self) -> &'static str {
        if self.watch.is_some() {
            return "home";
        }
        let has = self.signer_status.get("has_vault").and_then(|h| h.as_bool()).unwrap_or(false);
        if !has {
            "welcome"
        } else if self.unlocked() {
            "home"
        } else {
            "unlock"
        }
    }

    fn rpc(&self) -> Rpc {
        Rpc::new(self.source.url())
    }

    /// Where a signed transaction is posted: the chain's public submit-only route when the engine reads from a
    /// public endpoint (which refuses writes), else the read endpoint itself (a named or local node takes writes).
    /// With no route on the chain the read endpoint is used and its refusal is shown in its own words.
    fn submit_rpc(&self) -> Rpc {
        match (&self.source, &self.chain.submit) {
            (node_source::Source::Public { .. }, Some(route)) => Rpc::new(route),
            _ => self.rpc(),
        }
    }

    /// The source chosen again from the settings and the pinned chain (at start, and when the current one is lost).
    fn choose_source(&mut self) {
        let pinned = if self.chain.genesis.is_some() { self.chain.clone() } else { Chain::igneum_7322() };
        let s = node_source::choose(&self.settings.rpc, self.settings.rpc_named, &pinned);
        if s != self.source {
            eprintln!("engine: the chain is read from the {} node {}", s.name(), s.url());
            self.source = s;
            self.node = NodeInfo::default();
        }
    }

    fn scan_path(&self) -> PathBuf {
        self.data_dir.join("history-scan.json")
    }

    // ---- Touch ID and Windows Hello (biometric.rs; OLD-BASE.md step 3, third piece): the prompt and the sealed
    // password live in the window host; the engine issues challenges, the host confirms them, the action takes
    // one once. The password the host unseals reaches the signer through api/unlock as a typed one would. ----

    fn biometric_path(&self) -> PathBuf {
        self.data_dir.join("biometric.json")
    }

    /// "Touch ID" or "Windows Hello", for messages.
    fn what(&self) -> String {
        match self.bio.kind.as_str() {
            "hello" => "Windows Hello".into(),
            "touchid" => "Touch ID".into(),
            _ if cfg!(windows) => "Windows Hello".into(),
            _ => "Touch ID".into(),
        }
    }

    /// The host's token on a request, in constant time; no host, no token.
    fn host_ok(&self, given: Option<&str>) -> bool {
        !self.host_token.is_empty() && given.map(|g| biometric::constant_eq(g, &self.host_token)).unwrap_or(false)
    }

    fn take_nonce(&mut self, nonce: &str, purpose: &str, bound: &str) -> Result<()> {
        let what = self.what();
        self.gate.take(nonce, purpose, bound, Instant::now()).map_err(|e| Error::Refused(e.text(&what)))
    }

    /// The host reports what it can do, once at start (with its token).
    fn biometric_status(&mut self, available: bool, kind: &str, message: &str) -> Value {
        self.bio.available = available;
        self.bio.kind = kind.to_string();
        self.bio.message = message.to_string();
        self.bio.enrolled = self.biometric_path().exists();
        eprintln!("engine: biometric host: {kind} {}{}", if available { "available" } else { "unavailable" }, if message.is_empty() { String::new() } else { format!(" ({message})") });
        json!({"ok": true})
    }

    /// The host reports a prompt's outcome.
    fn biometric_report(&mut self, op: &str, ok: bool, code: &str, message: &str) -> Value {
        self.bio.last_result = if ok { "ok".into() } else if code.is_empty() { "failed".into() } else { code.to_string() };
        self.bio.last_op = op.to_string();
        self.bio.last_at = now_unix() as f64;
        eprintln!("engine: {} {op}: {}{}", self.what(), if ok { "ok" } else { code }, if message.is_empty() { String::new() } else { format!(" ({message})") });
        json!({"ok": true})
    }

    /// The window asks for a challenge (reveal); send challenges come with the quote.
    fn challenge(&mut self, purpose: &str) -> Result<Value> {
        if !self.unlocked() {
            return Err(Error::Refused("unlock first".into()));
        }
        let reason = match purpose {
            "reveal" => "Show your recovery words",
            _ => return Err(Error::Input("purpose is reveal".into())),
        };
        let nonce = self.gate.issue(purpose, "", reason, Instant::now());
        Ok(json!({"ok": true, "nonce": nonce, "reason": reason, "purpose": purpose}))
    }

    /// The challenge bound to a quote: the prompt's line and the nonce the host confirms.
    fn challenge_for_send(&mut self, env: &UnsignedEnvelope) -> (String, String) {
        let tx = &env.transaction;
        let to = tx.to().map(|a| a.to_checksum()).unwrap_or_default();
        let reason = biometric::send_reason(&display::ign(&tx.value(), 4), &to);
        let bound = biometric::send_binding(&to, &tx.value().to_dec_string(), tx.nonce(), tx.chain_id());
        let nonce = self.gate.issue("send", &bound, &reason, Instant::now());
        (nonce, reason)
    }

    /// The host reads what the prompt must say.
    fn challenge_reason(&self, nonce: &str) -> Result<Value> {
        let (purpose, reason) = self.gate.reason_of(nonce).ok_or_else(|| Error::Refused("unknown or used challenge".into()))?;
        Ok(json!({"ok": true, "purpose": purpose, "reason": reason}))
    }

    /// The host confirms: the prompt succeeded for this nonce.
    fn confirm(&mut self, nonce: &str) -> Result<Value> {
        let what = self.what();
        self.gate.confirm(nonce, Instant::now()).map_err(|e| Error::Refused(e.text(&what)))?;
        self.last_activity = Instant::now();
        Ok(json!({"ok": true}))
    }

    /// Enrolment, step 1 (the window): the password is checked by the signer (a wrong one is refused there) and
    /// parked under a one-time token for the host.
    fn enrol_begin(&mut self, password: &str) -> Result<Value> {
        if !self.bio.available {
            return Err(Error::Refused(format!("{} is not available on this machine", self.what())));
        }
        self.signer.request("verify_password", json!({"password": password}))?;
        let token = self.gate.enrol_begin(password, Instant::now());
        Ok(json!({"ok": true, "token": token}))
    }

    /// Enrolment, step 2 (the host, after the prompt): the password, once.
    fn enrol_take(&mut self, token: &str) -> Result<Value> {
        let p = self.gate.enrol_take(token, Instant::now()).ok_or_else(|| Error::Refused("no enrolment is waiting (it may have timed out: start again)".into()))?;
        Ok(json!({"ok": true, "secret": p}))
    }

    /// Enrolment, step 3 (the host): the sealed file is written; the engine checks it is there.
    fn enrolled_set(&mut self, kind: &str) -> Result<Value> {
        if !self.biometric_path().exists() {
            return Err(Error::Refused("the sealed file was not written".into()));
        }
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            let _ = fs::set_permissions(self.biometric_path(), fs::Permissions::from_mode(0o600));
        }
        self.bio.enrolled = true;
        self.bio.needs_enrol = false;
        if !kind.is_empty() {
            self.bio.kind = kind.to_string();
        }
        self.last_activity = Instant::now();
        Ok(json!({"ok": true}))
    }

    /// Settings > turn off: the sealed file goes; the password is the only way in again.
    fn biometric_remove(&mut self) -> Value {
        let _ = fs::remove_file(self.biometric_path());
        self.gate.enrol_cancel();
        self.bio.enrolled = false;
        self.bio.needs_enrol = false;
        json!({"ok": true})
    }

    /// A password change under an enrolment: the sealed password is stale, the file goes, the page asks to turn
    /// the prompt on again.
    fn biometric_stale(&mut self) {
        if self.bio.enrolled {
            let _ = fs::remove_file(self.biometric_path());
            self.bio.enrolled = false;
            self.bio.needs_enrol = true;
        }
    }

    /// The history scan advanced a few blocks towards the head after a good poll; a chain or address change starts
    /// the scan over (the file is keyed by the genesis and the address).
    fn advance_scan(&mut self) {
        let (Some(a), true) = (self.address(), self.node.ok && !self.node.genesis.is_empty()) else { return };
        let genesis = self.node.genesis.clone();
        let fresh = match &self.scan {
            Some(s) => !s.genesis.eq_ignore_ascii_case(&genesis) || s.address != a.to_checksum().to_ascii_lowercase(),
            None => true,
        };
        if fresh {
            self.scan = Some(history_scan::Scan::load(&self.scan_path(), &genesis, &a));
        }
        let rpc = self.rpc();
        let head = self.node.block;
        let path = self.scan_path();
        // the public endpoint's filter limits each source (twenty calls a second, a burst of forty): a small budget
        // there, the full one on a named or local node
        let budget = if matches!(self.source, node_source::Source::Public { .. }) { 8 } else { history_scan::BLOCKS_PER_POLL };
        if let Some(s) = self.scan.as_mut() {
            match s.advance(&rpc, head, budget) {
                Ok(n) if n > 0 => {
                    let _ = s.save(&path);
                }
                Ok(_) => {}
                Err(e) => eprintln!("engine: the history scan stopped at {:?}: {e}", s.scanned_to),
            }
        }
    }

    /// One poll of the node: head, chain id, balance, and the reorganisation watch.
    fn poll(&mut self) {
        let rpc = self.rpc();
        let mut info = NodeInfo::default();
        match (rpc.chain_id(), rpc.block_number()) {
            (Ok(id), Ok(n)) => {
                info.ok = true;
                info.chain_id = id;
                info.block = n;
                // the genesis is read once per endpoint and chain id; the chain is then pinned by it
                info.genesis = if self.node.chain_id == id && !self.node.genesis.is_empty() {
                    self.node.genesis.clone()
                } else {
                    rpc.genesis_hash().unwrap_or_default()
                };
                let pinned = Chain::identify(id, if info.genesis.is_empty() { None } else { Some(&info.genesis) });
                if pinned != self.chain {
                    self.chain = pinned;
                    self.quotes.clear();
                }
                if let Ok(Some(b)) = rpc.block(n) {
                    let hash = b.get("hash").and_then(|h| h.as_str()).unwrap_or("").to_string();
                    info.head_unix = b
                        .get("timestamp")
                        .and_then(|t| t.as_str())
                        .and_then(|t| U256::from_hex(t).ok())
                        .and_then(|t| t.to_u64())
                        .unwrap_or(0);
                    self.watch_reorg(n, hash, &rpc);
                }
                if let Some(a) = self.address() {
                    info.balance = rpc.balance(&a).ok();
                }
            }
            (Err(e), _) | (_, Err(e)) => {
                info.error = e.to_string();
            }
        }
        let was_ok = self.node.ok;
        // one failed poll (the public endpoint's rate limit under the page's reads) keeps the last good reading
        // for three polls (about six seconds) before the node reads lost: the pill does not flap on a bucket
        // (the 0.0.5 captures read "lost" for one poll, 12 October 2026)
        let limit = if info.error.to_ascii_lowercase().contains("rate") || info.error.contains("429") || info.error.to_ascii_lowercase().contains("limit") { 12 } else { 3 };
        if !info.ok && was_ok && self.poll_misses < limit {
            self.poll_misses += 1;
            return;
        }
        self.poll_misses = if info.ok { 0 } else { self.poll_misses };
        self.node = info;
        if self.node.ok {
            self.advance_scan();
        } else if was_ok || self.node.error.is_empty() {
            // the source went away (or never answered): chosen again, so a local node that appeared, or the public
            // endpoint when a local one stopped, is taken on the next poll
            self.choose_source();
        }
    }

    /// Keeps the last 256 heads; when a known number comes back with a different hash, the depth from that
    /// number to the previous head is a reorganisation this engine saw itself.
    fn watch_reorg(&mut self, head: u64, hash: String, rpc: &Rpc) {
        if let Some((last_n, _)) = self.headers.back().cloned() {
            if head <= last_n {
                // the head went back or stayed: compare what we knew at `head`
                if let Some((_, known)) = self.headers.iter().find(|(n, _)| *n == head) {
                    if *known != hash {
                        let depth = last_n - head + 1;
                        self.reorgs.push((now_unix(), depth));
                        self.headers.retain(|(n, _)| *n < head);
                    }
                }
            } else {
                // moved forward: check the parent of the new head still matches our last head when adjacent
                if head == last_n + 1 {
                    if let Ok(Some(b)) = rpc.block(head) {
                        let parent = b.get("parentHash").and_then(|h| h.as_str()).unwrap_or("");
                        if let Some((_, last_hash)) = self.headers.back() {
                            if !parent.is_empty() && parent != last_hash {
                                // our last head was replaced: find how deep by walking back
                                let mut depth = 1;
                                let mut n = last_n;
                                while let Some((_, known)) = self.headers.iter().find(|(k, _)| *k == n) {
                                    match rpc.block(n) {
                                        Ok(Some(bb)) => {
                                            let h = bb.get("hash").and_then(|h| h.as_str()).unwrap_or("");
                                            if h == known {
                                                break;
                                            }
                                            depth += 1;
                                        }
                                        _ => break,
                                    }
                                    if n == 0 {
                                        break;
                                    }
                                    n -= 1;
                                }
                                self.reorgs.push((now_unix(), depth));
                                self.headers.retain(|(k, _)| *k < n + 1);
                            }
                        }
                    }
                }
            }
        }
        if self.headers.back().map(|(n, _)| *n < head).unwrap_or(true) {
            self.headers.push_back((head, hash));
        }
        while self.headers.len() > 256 {
            self.headers.pop_front();
        }
        let week = now_unix().saturating_sub(7 * 86400);
        self.reorgs.retain(|(t, _)| *t >= week);
    }

    fn deepest_reorg(&self, since_secs: u64) -> u64 {
        let since = now_unix().saturating_sub(since_secs);
        self.reorgs.iter().filter(|(t, _)| *t >= since).map(|(_, d)| *d).max().unwrap_or(0)
    }

    fn observations(&self, rpc: &Rpc, state: &TxState) -> Observations {
        let (work, _) = match state {
            TxState::Included { block } | TxState::Reverted { block } => {
                let head = self.node.block;
                let mut diffs = Vec::new();
                let upto = head.min(block + 256);
                for n in (block + 1)..=upto {
                    if let Ok(Some((d, _))) = rpc.block_work(n) {
                        diffs.push(d);
                    }
                }
                let mut window = Vec::new();
                for n in head.saturating_sub(32)..=head {
                    if let Ok(Some(w)) = rpc.block_work(n) {
                        window.push(w);
                    }
                }
                let rate = settlement::recent_rate(&window);
                (settlement::work_minutes(&diffs, rate), rate)
            }
            _ => (0.0, 0.0),
        };
        Observations {
            head: self.node.block,
            head_unix: self.node.head_unix,
            now_unix: now_unix(),
            work_minutes_on_top: work,
            deepest_reorg_24h: self.deepest_reorg(86400),
            deepest_reorg_7d: self.deepest_reorg(7 * 86400),
        }
    }

    fn tx_view(&self, hash_hex: &str) -> Result<Value> {
        let bytes = hex::decode(hash_hex)?;
        if bytes.len() != 32 {
            return Err(Error::Input("a hash has 32 bytes".into()));
        }
        let mut h = [0u8; 32];
        h.copy_from_slice(&bytes);
        let rpc = self.rpc();
        let sent = self.sent.iter().find(|s| s.hash.eq_ignore_ascii_case(hash_hex));
        let sent_unix = sent.map(|s| s.sent_unix).unwrap_or_else(now_unix);
        let receipt = rpc.receipt(&h)?;
        let tx = rpc.transaction(&h)?;
        let value = sent.map(|s| s.moves.filter(|m| !m.is_zero()).unwrap_or(s.value)).or_else(|| {
            tx.as_ref().and_then(|t| t.get("value")).and_then(|v| v.as_str()).and_then(|v| U256::from_hex(v).ok())
        });
        let state = match (&tx, &receipt) {
            (_, Some(r)) => {
                let block = r
                    .get("blockNumber")
                    .and_then(|b| b.as_str())
                    .and_then(|b| U256::from_hex(b).ok())
                    .and_then(|b| b.to_u64())
                    .unwrap_or(0);
                let ok = r.get("status").and_then(|s| s.as_str()).map(|s| s == "0x1").unwrap_or(true);
                if ok { TxState::Included { block } } else { TxState::Reverted { block } }
            }
            (Some(_), None) => TxState::Submitted { sent_unix },
            // the dropped word is held until the pool has not carried the transaction for twelve blocks' time
            // (wallet-security's finding 14, 12 October 2026: a row read "dropped" for one reading between sent and
            // included, and a person who sees it may send again); until then the row stays "sent"
            (None, None) if now_unix().saturating_sub(sent_unix) < 12 * self.chain.block_target_secs => TxState::Submitted { sent_unix },
            (None, None) => TxState::Dropped { sent_unix },
        };
        let obs = self.observations(&rpc, &state);
        let view = settlement::view(&state, &value.unwrap_or(U256::ZERO), &obs, &self.policy);
        Ok(json!({"hash": hash_hex, "state": state, "observations": obs, "view": view, "sentences": sent.map(|s| s.sentences.clone())}))
    }

    fn state_json(&self) -> Value {
        let addr = self.address();
        let bal = self.node.balance;
        // the rows sent from here (with their sentences) first by time, then what the scan found on the chain that
        // was not sent from here (received, self, sealed; a send from here found by the scan keeps the local row)
        let mut history: Vec<Value> = self
            .sent
            .iter()
            .map(|s| json!({"hash": s.hash, "to": s.to, "value_ign": display::ign(&s.value, 18), "value_wei": s.value.to_dec_string(), "sent_unix": s.sent_unix, "sentences": s.sentences, "kind": "sent", "from_scan": false}))
            .collect();
        if let Some(scan) = &self.scan {
            for row in scan.rows() {
                let h = row["hash"].as_str().unwrap_or("");
                if !self.sent.iter().any(|s| s.hash.eq_ignore_ascii_case(h)) {
                    history.push(row);
                }
            }
        }
        history.sort_by(|a, b| b["sent_unix"].as_u64().unwrap_or(0).cmp(&a["sent_unix"].as_u64().unwrap_or(0)));
        history.truncate(50);
        let scan_line = match &self.scan {
            Some(s) => match s.scanned_to {
                // the first scan runs downwards from the head: its words name the block it is at and the floor
                Some(_) if matches!((s.back_to, s.back_floor), (Some(b), Some(f)) if b > f + 1) => {
                    format!("reading the chain back from the head: block {} down to {} for this address", display_commas(s.back_to.unwrap_or(0).saturating_sub(1)), display_commas(s.back_floor.unwrap_or(0)))
                }
                Some(n) if n + 1 >= self.node.block => format!("the chain read to block {} for this address", display_commas(n)),
                Some(n) => format!("reading the chain: block {} of {} for this address", display_commas(n), display_commas(self.node.block)),
                None => "reading the chain for this address".to_string(),
            },
            None => String::new(),
        };
        json!({
            "version": VERSION,
            "phase": self.phase(),
            "address": addr.map(|a| a.to_checksum()),
            "address_short": addr.map(|a| a.short()),
            "watch_only": self.watch.is_some(),
            "kind": if self.watch.is_some() { "watch_only" } else { self.signer_status.get("kind").and_then(|k| k.as_str()).unwrap_or("none") },
            "backed_up": self.watch.is_some() || self.signer_status.get("backed_up").and_then(|b| b.as_bool()).unwrap_or(false),
            // the signer's own process shape (its fds, its environment's names, its socket count), read by itself:
            // the isolation row ISO-01 reads it here once the memory guard hides /proc/<pid> from outside
            "signer_process": self.signer_status.get("process").cloned().unwrap_or(Value::Null),
            "balance_known": bal.is_some(),
            "balance_ign": bal.map(|b| display::ign(&b, 6)).unwrap_or_default(),
            "balance_wei": bal.map(|b| b.to_dec_string()),
            "node": {
                "state": if self.node.ok { "ok" } else { "lost" },
                "error": self.node.error,
                "endpoint": self.source.url(),
                "named": self.settings.rpc,
                // "Your node" only for a node the person named that is not the public endpoint (the capture tool
                // names the public endpoint on its command line and read "Your node", 12 October 2026)
                "own": self.settings.rpc_named && self.settings.rpc.trim_end_matches('/') != igneum_wallet_core::chain::PUBLIC_7322_RPC.trim_end_matches('/'),
                "block": self.node.block,
                "head_unix": self.node.head_unix,
                "chain_id": self.node.chain_id,
                "source": self.source.name(),
                "trust": self.source.trust(),
                "scan": scan_line,
                "deepest_reorg_24h": self.deepest_reorg(86400),
                "deepest_reorg_7d": self.deepest_reorg(7 * 86400),
            },
            "network": {
                "name": self.chain.name, "chain_id": self.chain.chain_id, "symbol": self.chain.symbol, "proposed": self.chain.proposed,
                "genesis": self.node.genesis, "genesis_pinned": self.chain.genesis_pinned, "faucet": self.chain.faucet, "faucet_relay": self.chain.faucet_relay, "bond_faucet": self.chain.bond_faucet, "explorer": self.chain.explorer,
                "pinned": self.chain.genesis.is_some() && !self.chain.name.starts_with("chain "),
                "registry": self.chain.registry().map(|r| r.to_checksum()),
                "authority": self.chain.authority,
            },
            "settings": self.settings,
            "policy": self.policy,
            "biometric": self.bio,
            "biometric_what": self.what(),
            "biometric_enrol_pending": self.gate.enrol_pending(),
            "update": self.update.state,
            "identity": self.identity,
            "history": history,
            "signer_pid": self.signer.pid(),
            "bridge": {
                "on": self.settings.bridge_on,
                "port": self.bridge_port,
                "pending": self.asks.pending(), "refused": self.asks.refused().into_iter().map(|(h, n)| json!({"host": h, "count": n})).collect::<Vec<_>>(),
                "sites": self.sites,
            },
        })
    }

    fn quote(&mut self, to: &str, amount: &str) -> Result<Value> {
        let to = Address::parse(to)?;
        let value = display::parse_ign(amount)?;
        self.quote_call(to, value, vec![])
    }

    /// The one-tap "Get test IGN" (the founder's "public ready" order, 12 October 2026): a quote of `drip()` to
    /// the chain's faucet contract (the kit's Faucet, 1 IGN per address per day), reviewed and sent as any send is;
    /// nothing on a chain without a faucet.
    /// What the chain's ledger holds for this address (the explorer's account transaction list, newest first, the
    /// last hundred): the History page reads this beside the engine's own send records, so a restored wallet shows
    /// its past on any machine and a sent transaction appears the minute it is included (study participant 4,
    /// 12 October 2026: History read empty on both machines after a settled send; the page listed only the local
    /// records and the paced block scan, which reads eight blocks a poll on the public endpoint from two thousand
    /// below the head, minutes behind a fresh install). Each entry: hash, kind (sent | received | self), block,
    /// unix, from, to, value_ign, ok (the receipt's status).
    fn chain_history(&mut self) -> Result<Value> {
        let me = self.address().ok_or_else(|| Error::Refused("no wallet".into()))?;
        let explorer = self.chain.explorer.clone().ok_or_else(|| Error::Refused(format!("{} has no explorer the wallet knows", self.chain.name)))?;
        let me_hex = me.to_checksum().to_ascii_lowercase();
        let url = format!("{}/api?module=account&action=txlist&address={}&sort=desc&page=1&offset=100", explorer.trim_end_matches('/'), me.to_checksum());
        let resp = ureq::AgentBuilder::new().timeout(Duration::from_secs(30)).build().get(&url).call();
        let v: Value = match resp {
            Ok(r) => r.into_json().map_err(|e| Error::Rpc(format!("the explorer answered badly: {e}")))?,
            Err(ureq::Error::Status(code, _)) => return Err(Error::Rpc(format!("the explorer refused ({code})"))),
            Err(e) => return Err(Error::Rpc(format!("the explorer is not reachable: {e}"))),
        };
        let items = v.get("result").and_then(|r| r.as_array()).cloned().unwrap_or_default();
        let num = |x: Option<&Value>| -> u64 { x.and_then(|n| n.as_str().and_then(|s| s.parse().ok()).or_else(|| n.as_u64())).unwrap_or(0) };
        let mut out = Vec::new();
        for t in items {
            let from = t.get("from").and_then(|f| f.as_str()).unwrap_or("").to_ascii_lowercase();
            let to = t.get("to").and_then(|f| f.as_str()).unwrap_or("").to_ascii_lowercase();
            let kind = match (from == me_hex, to == me_hex) {
                (true, true) => "self",
                (true, false) => "sent",
                _ => "received",
            };
            let value = t.get("value").and_then(|x| x.as_str()).and_then(|s| U256::from_dec_str(s).ok()).unwrap_or(U256::ZERO);
            let ok = t.get("txreceipt_status").and_then(|s| s.as_str()).map(|s| s == "1").unwrap_or(true) && t.get("isError").and_then(|s| s.as_str()).map(|s| s == "0").unwrap_or(true);
            out.push(json!({
                "hash": t.get("hash").and_then(|h| h.as_str()).unwrap_or(""),
                "kind": kind,
                "block": num(t.get("blockNumber")),
                "unix": num(t.get("timeStamp")),
                "from": from,
                "to": to,
                "value_ign": display::ign(&value, 18), "value_wei": value.to_dec_string(),
                "ok": ok,
            }));
        }
        // the internal transfers to this address (a faucet drip, a bond's release: the IGN moves inside a call whose
        // own value is zero; the project's 0.0.6 order on participant 7: the row says what moved to you)
        let url_i = format!("{}/api?module=account&action=txlistinternal&address={}&sort=desc&page=1&offset=100", explorer.trim_end_matches('/'), me.to_checksum());
        if let Ok(r) = ureq::AgentBuilder::new().timeout(Duration::from_secs(30)).build().get(&url_i).call() {
            if let Ok(vi) = r.into_json::<Value>() {
                let seen: std::collections::HashSet<String> = out.iter().filter_map(|e| e.get("hash").and_then(|h| h.as_str()).map(|h| h.to_ascii_lowercase())).collect();
                for t in vi.get("result").and_then(|r| r.as_array()).cloned().unwrap_or_default() {
                    let from = t.get("from").and_then(|f| f.as_str()).unwrap_or("").to_ascii_lowercase();
                    let to = t.get("to").and_then(|f| f.as_str()).unwrap_or("").to_ascii_lowercase();
                    let hash = t.get("transactionHash").or_else(|| t.get("hash")).and_then(|h| h.as_str()).unwrap_or("").to_ascii_lowercase();
                    let value = t.get("value").and_then(|x| x.as_str()).and_then(|v| U256::from_dec_str(v).ok()).unwrap_or(U256::ZERO);
                    if value.is_zero() || hash.is_empty() || seen.contains(&hash) || (to != me_hex && from != me_hex) {
                        continue;
                    }
                    let faucet = self.chain.faucet.as_deref().map(|f| f.eq_ignore_ascii_case(&from)).unwrap_or(false);
                    let registry = self.chain.registry().map(|r| r.to_checksum().eq_ignore_ascii_case(&from)).unwrap_or(false);
                    let words = if to == me_hex { if faucet { "paid by the faucet contract" } else if registry { "released by the registry (the bond returned)" } else { "moved to you inside a call" } } else { "moved from you inside a call" };
                    out.push(json!({
                        "hash": hash, "kind": if to == me_hex { "received" } else { "sent" }, "internal": true, "words": words,
                        "block": num(t.get("blockNumber")), "unix": num(t.get("timeStamp")), "from": from, "to": to,
                        "value_ign": display::ign(&value, 18), "value_wei": value.to_dec_string(), "ok": t.get("isError").and_then(|s| s.as_str()).map(|s| s == "0").unwrap_or(true),
                    }));
                }
            }
        }
        out.sort_by(|a, b| b.get("block").and_then(|x| x.as_u64()).unwrap_or(0).cmp(&a.get("block").and_then(|x| x.as_u64()).unwrap_or(0)));
        Ok(json!({"address": me.to_checksum(), "entries": out, "source": explorer}))
    }

    fn quote_faucet(&mut self) -> Result<Value> {
        let faucet = self.chain.faucet.clone().ok_or_else(|| Error::Refused(format!("{} has no faucet the wallet knows", self.chain.name)))?;
        let to = Address::parse(&faucet)?;
        let me = self.address().ok_or_else(|| Error::Refused("no wallet".into()))?;
        // the chain's faucet relay, when it has one (devnet9: the faucet page's route, POST {address}): the relay
        // pays the gas and calls the contract, so a fresh wallet with nothing to pay a fee gets its first IGN
        // (study participant 4, 12 October 2026: the wallet's own drip call reverted; a wallet holding nothing
        // cannot pay the call's gas, and an address the page already dripped today is TooSoon). The answer carries
        // the amount, the hash, the balance and the next allowed time when refused; nothing is signed here.
        if let Some(relay) = self.chain.faucet_relay.clone() {
            let url = relay.trim_end_matches('/').to_string();
            let body = json!({"address": me.to_checksum()});
            let resp = ureq::AgentBuilder::new().timeout(Duration::from_secs(90)).build().post(&url).set("Content-Type", "application/json").send_json(body);
            let v: Value = match resp {
                Ok(r) => r.into_json().map_err(|e| Error::Rpc(format!("the faucet relay answered badly: {e}")))?,
                Err(ureq::Error::Status(code, r)) => {
                    let v: Value = r.into_json().unwrap_or(json!({}));
                    let why = v.get("error").and_then(|e| e.as_str()).unwrap_or("refused").to_string();
                    let next = v.get("next_at").and_then(|n| n.as_u64());
                    return Ok(json!({"relayed": true, "refused": true, "error": format!("the faucet refused ({code}): {why}"), "next_at": next, "amount_ign": "25", "contract": faucet}));
                }
                Err(e) => return Err(Error::Rpc(format!("the faucet relay is not reachable: {e}"))),
            };
            self.last_activity = Instant::now();
            return Ok(json!({"relayed": true, "hash": v.get("hash"), "amount_ign": "25", "balance_ign": v.get("balance_ign"), "next_at": now_unix() + 86_400, "contract": faucet, "note": v.get("note"), "label": "development-network tokens; no monetary value"}));
        }
        // the devnet9 faucet is the DevFaucet: drip(address to), 25 IGN a day per address (the study's first
        // participant met a revert here, 12 October 2026: the wallet called the kit Faucet's drip() on it)
        let sel = igneum_wallet_core::keccak::keccak256(b"drip(address)");
        let mut data = sel[..4].to_vec();
        data.extend_from_slice(&[0u8; 12]);
        data.extend_from_slice(&me.0);
        self.quote_call(to, U256::from_hex("0x0")?, data)
    }

    /// A quote for a registry action (DESIGN.md 1.13): the same quote the send screen reviews, with the registry's
    /// sentence first. `action` is bond, set-signer, set-exit, claim-exit; the hostile claim needs the bond key's
    /// secret, which the engine never holds, so it is the CLI's only.
    fn quote_authority(&mut self, action: &str, body: &Value) -> Result<Value> {
        use igneum_wallet_core::authority;
        let registry = self.chain.registry().ok_or_else(|| {
            Error::Refused(format!("{} carries no bond registry the wallet knows; a bond is only built for a pinned authority chain", self.chain.name))
        })?;
        let key = authority::parse_key(field(body, "key")?)?;
        let (value, data) = match action {
            "bond" => {
                let value = display::parse_ign(field(body, "amount")?)?;
                match body.get("exit").and_then(|e| e.as_str()).filter(|e| !e.is_empty()) {
                    None => (value, authority::bond_data(&key)),
                    Some(e) => (value, authority::set_exit_data(&key, &Address::parse(e)?)),
                }
            }
            "set-signer" => (U256::ZERO, authority::set_signer_data(&key, &authority::parse_key(field(body, "signer")?)?)),
            // the holder's release at or past the lock (version 4): the whole bond to the exit address
            "release" => (U256::ZERO, authority::release_data(&key)),
            // the lab's form: the exit changes with a top-up, by the account (0.001 IGN unless the page says more)
            "set-exit" => {
                let amount = body.get("amount").and_then(|a| a.as_str()).filter(|a| !a.is_empty()).unwrap_or("0.001");
                (display::parse_ign(amount)?, authority::set_exit_data(&key, &Address::parse(field(body, "exit")?)?))
            }
            // the claim (the holder's own release to the exit address, or a hostile claim) carries the proof pi the
            // hash lead's prover made from the bond's secret; the page gives the proof file's path and the share,
            // the engine reads the file (never the secret) and checks the proof's shape
            "claim-exit" => {
                let to = Address::parse(field(body, "exit")?)?;
                let share = authority::parse_key(field(body, "share")?)?;
                let pi = fs::read(field(body, "pi_file")?)?;
                (U256::ZERO, authority::claim_data(&key, &to, &share, &pi)?)
            }
            other => return Err(Error::Input(format!("no registry action named {other}"))),
        };
        let mut q = self.quote_call(registry, value, data)?;
        // the screens before the review (wallet-security's W-2 and W-3; RECOVERY.md section 7): a bond carries the
        // ten statements with their figures and the height they were read at; a release carries the exit screen
        match action {
            "bond" => {
                let exit = body.get("exit").and_then(|e| e.as_str()).filter(|e| !e.is_empty()).map(Address::parse).transpose()?;
                q["risk_screen"] = self.risk_screen(&value, exit.as_ref())?;
            }
            "release" => {
                q["exit_screen"] = self.exit_screen(&key)?;
                let moved = q["exit_screen"].get("locked_wei").and_then(|w| w.as_str()).and_then(|w| U256::from_dec_str(w).ok());
                if let (Some(id), Some(m)) = (q.get("quote_id").and_then(|i| i.as_str()).map(|i| i.to_string()), moved) {
                    if let Some(qq) = self.quotes.get_mut(&id) { qq.moves = Some(m); }
                }
            }
            _ => {}
        }
        Ok(q)
    }

    /// The bond-risk screen (RECOVERY.md section 7, W-2): the ten statements a person must understand before
    /// bonding, each with its figures from the chain and the height read at. K (statement 5) counts the blocks of
    /// the last 100 sealed by keys whose units lie between half and double this bond's.
    fn risk_screen(&self, amount: &U256, exit: Option<&Address>) -> Result<Value> {
        use igneum_wallet_authority::draw;
        use igneum_wallet_core::authority;
        let rpc = self.rpc();
        let s = rpc.igneum_sortition()?;
        // the authority's lock read before the headers below, and again after a pause when the public host's
        // bucket refuses it (the study's first participant read "not served by this node" beside a Desk showing
        // the lock, 12 October 2026)
        let mut auth = rpc.call("igneum_authority", json!([])).unwrap_or(json!({}));
        if auth.get("lockBlocks").is_none() {
            std::thread::sleep(Duration::from_millis(1500));
            auth = rpc.call("igneum_authority", json!([])).unwrap_or(json!({}));
        }
        let params = s.get("params").cloned().unwrap_or(json!({}));
        let expected = params.get("Expected").and_then(|v| v.as_u64()).unwrap_or(1);
        let epoch_blocks = params.get("EpochBlocks").and_then(|v| v.as_u64()).unwrap_or(5760);
        let head = s.get("head").and_then(|v| v.as_u64()).unwrap_or(0);
        let total = U256::from_hex(s.get("total").and_then(|v| v.as_str()).unwrap_or("0x0")).unwrap_or(U256::ZERO);
        let lock = auth.get("lockBlocks").and_then(|v| v.as_u64());
        let total_after = total.checked_add(amount).unwrap_or(total);
        let share = draw::share(amount, &total_after, expected, epoch_blocks);
        let unit_price = total_after.to_f64() / 65536.0 / 1e18;
        let account = self.address().map(|a| a.to_checksum()).unwrap_or_default();
        let exit_s = exit.map(|e| e.to_checksum()).unwrap_or_else(|| account.clone());
        let window: u64 = head.min(100);
        // the last window's sealers and their bonds: the keys of this bond's size
        let mut sealers: std::collections::BTreeMap<String, u64> = std::collections::BTreeMap::new();
        let mut h = head.saturating_sub(window) + 1;
        while h <= head {
            let to = (h + 19).min(head);
            let calls: Vec<(&str, Value)> = (h..=to).map(|n| ("eth_getBlockByNumber", json!([format!("0x{:x}", n), false]))).collect();
            if let Ok(hs) = rpc.call_batch(&calls) {
                for b in hs.iter().flatten() {
                    if let Some(bk) = b.get("bondKey").and_then(|x| x.as_str()) {
                        *sealers.entry(bk.to_ascii_lowercase()).or_insert(0) += 1;
                    }
                }
            }
            h = to + 1;
            if h <= head {
                std::thread::sleep(Duration::from_millis(1100));
            }
        }
        // the sealers' bonds read after a pause: the headers above used the public listener's burst
        std::thread::sleep(Duration::from_millis(1500));
        let mut k = 0u64;
        let lo = share.units / 2;
        let hi = share.units.saturating_mul(2);
        for (bk, n) in &sealers {
            if let Ok(key) = authority::parse_key(bk) {
                if let Ok(b) = rpc.igneum_bond_of(&key, "latest") {
                    let a = U256::from_hex(b.get("amount").and_then(|v| v.as_str()).unwrap_or("0x0")).unwrap_or(U256::ZERO);
                    let u = draw::share(&a, &total, expected, epoch_blocks).units;
                    if u >= lo && u <= hi {
                        k += n;
                    }
                }
            }
        }
        let p = share.round0_chance * window as f64;
        let x = display::ign(amount, 4);
        let h_words = match lock {
            Some(l) => format!("H, at least {} blocks after the block your bond lands in, so not before about height {} (the head is {})", Self::commas(l), Self::commas(head + 1 + l), Self::commas(head)),
            None => "the post height plus the chain's lock (not served by this node)".to_string(),
        };
        let st = |n: u32, text: String| json!({"n": n, "text": text, "head": head});
        Ok(json!({
            "head": head, "read_unix": igneum_wallet_core::vault::now_unix(), "chain": self.chain.name, "window_blocks": window,
            "units": share.units, "unit_price_ign": format!("{unit_price:.5}"), "predicted_round0": p, "sealed_by_keys_of_this_size": k,
            "statements": [
                st(1, format!("You lock {x} IGN. It leaves your balance now and returns only by a release after height {h_words}, to the exit address {exit_s}.")),
                st(2, format!("Your bond buys {} of 65,536 in the draw ({unit_price:.5} IGN a unit at this epoch's price; the price moves every epoch with the total bonded, so the count will move).{}", Self::units_words(share.units), {
                    let rem = amount.to_f64() / 1e18 - share.units as f64 * unit_price;
                    if rem > 0.0005 { format!(" The {rem:.4} IGN above the whole units buys no draw; it stays locked with the bond and returns at the release.") } else { String::new() }
                })),
                st(3, "Being drawn means the right to seal a block in a round, not a payment. To seal you need your own machine with a card that meets the seal's difficulty inside the round, and your signing key on it.".to_string()),
                st(4, "A sealed block pays the sealer's part of that block's subsidy to this account. A draw you do not seal pays nothing and costs nothing.".to_string()),
                st(5, format!("On this chain, over the last {window} blocks, your {} would have been eligible at round 0 about {p:.1} times; keys of your size sealed {k} blocks (the draw and the result are shown separately on the Desk).", Self::units_words(share.units))),
                st(6, "You lose the WHOLE bond if your signing key seals two different blocks at one height, or endorses two at one height. One block's reward of it goes to whoever reports it; the rest is burnt; the key is marked and can never bond again.".to_string()),
                st(7, "Anyone who holds your bond secret can take three quarters of the bond to any address at any time; a quarter is burnt. Keep the secret off every machine that does not need it.".to_string()),
                st(8, format!("The exit address is set now and can be changed only after height {h_words}.")),
                st(9, "If you set the exit address to a key you keep offline, a thief of this wallet's key can release the bond only to that address. A thief of the bond secret cannot release it at all: the only door is the claim, which burns a quarter and removes the key.".to_string()),
                st(10, "This is a development chain: its coins have no value and it can be reset. Nothing here is a yield or a return.".to_string()),
            ],
        }))
    }

    /// The exit screen before a release (W-3): the earliest exit height, the exit address in full, the amount that
    /// returns, the words "after the lock".
    fn exit_screen(&self, key: &[u8; 32]) -> Result<Value> {
        use igneum_wallet_core::{authority, keccak::keccak256};
        let rpc = self.rpc();
        let registry = self.chain.registry().ok_or_else(|| Error::Input("no registry".into()))?;
        let reg = registry.to_checksum();
        let auth = rpc.call("igneum_authority", json!([])).unwrap_or(json!({}));
        let head = rpc.block_number()?;
        let mut pre = b"IgneumHash/R0A/bond-height".to_vec();
        pre.extend_from_slice(key);
        let posted = rpc
            .call("eth_getStorageAt", json!([reg, hex::encode(&keccak256(&pre)[..]), "latest"]))
            .ok()
            .and_then(|v| v.as_str().and_then(|h| U256::from_hex(h).ok()))
            .and_then(|v| v.to_u64())
            .unwrap_or(0);
        let lock = auth.get("lockBlocks").and_then(|v| v.as_u64());
        let earliest = lock.map(|l| posted + l);
        let bond = rpc.igneum_bond_of(key, "latest")?;
        let amount = U256::from_hex(bond.get("amount").and_then(|v| v.as_str()).unwrap_or("0x0")).unwrap_or(U256::ZERO);
        let exit = rpc
            .eth_call(&json!({"to": reg, "data": hex::encode(&authority::bond_data(key))}))
            .ok()
            .and_then(|out| authority::decode_read(&out).ok())
            .and_then(|r| r.exit)
            .map(|e| e.to_checksum());
        let open = earliest.map(|e| head >= e).unwrap_or(false);
        let words = match (earliest, &exit) {
            (Some(e), Some(x)) => format!(
                "After the lock: the earliest exit height is {} (the bond posted at {}, the lock {} blocks; the head is {}, so the release is {}). The whole bond, {} IGN, returns to the exit address {} and to no other; the key leaves the table for good.",
                Self::commas(e), Self::commas(posted), Self::commas(lock.unwrap_or(0)), Self::commas(head), if open { "open now" } else { "refused by the chain until then" }, display::ign(&amount, 6), x
            ),
            _ => format!("This node does not serve the lock or the exit address for this key; the chain refuses a release before the lock. The bond reads {} IGN at head {}.", display::ign(&amount, 6), Self::commas(head)),
        };
        Ok(json!({"head": head, "posted_height": posted, "lock_blocks": lock, "earliest_exit_height": earliest, "release_open": open,
                  "amount_ign": display::ign(&amount, 6), "locked_wei": amount.to_dec_string(), "exit_address": exit, "words": words}))
    }

    /// The engine's own transaction: the node supplies nonce, fees and the gas estimate (a refused estimate is the
    /// chain's refusal, reported in the simulation and not hidden; the quote then cannot be sent).
    fn quote_call(&mut self, to: Address, value: U256, data: Vec<u8>) -> Result<Value> {
        let from = self.address().ok_or_else(|| Error::Refused("no wallet".into()))?;
        let rpc = self.rpc();
        let nonce = rpc.nonce(&from)?;
        let fees = rpc.fees()?;
        let call = json!({"from": from.to_checksum(), "to": to.to_checksum(), "value": value.to_hex(), "data": hex::encode(&data)});
        // a plain transfer to an account is 21,000; to a contract it runs `receive` and is estimated
        let (gas, refused) = if data.is_empty() && !rpc.has_code(&to.to_checksum()).unwrap_or(true) {
            (21000, None)
        } else {
            // the public host's bucket (twenty calls a second per source) can refuse the estimate right after the
            // page's reads: one pause and one retry before the refusal is the quote's (a stranger at the Pay demo
            // met it, 12 October 2026)
            match rpc.estimate_gas(&call) {
                Ok(g) => (g * 12 / 10, None),
                Err(e) if e.to_string().contains("rate limited") => {
                    std::thread::sleep(Duration::from_millis(1500));
                    match rpc.estimate_gas(&call) {
                        Ok(g) => (g * 12 / 10, None),
                        Err(e) => (0, Some(e.to_string())),
                    }
                }
                Err(e) => (0, Some(e.to_string())),
            }
        };
        let tx = Transaction::Eip1559(Eip1559Tx {
            chain_id: self.chain.chain_id,
            nonce,
            max_priority_fee_per_gas: fees.max_priority_fee_per_gas,
            max_fee_per_gas: fees.max_fee_per_gas,
            gas_limit: gas,
            to: Some(to),
            value,
            data,
        });
        let env = UnsignedEnvelope::new(&self.chain.name, from, tx);
        // the simulation: eth_call on the exact payload; a failure is shown, not hidden
        let simulation = if let Some(reason) = refused {
            json!({"ok": false, "text": format!("the chain refuses this: {reason}"), "lines": [], "traced": false})
        } else { match rpc.eth_call(&call).or_else(|e| if e.to_string().contains("rate limited") { std::thread::sleep(Duration::from_millis(1500)); rpc.eth_call(&call) } else { Err(e) }) {
            Ok(_) => {
                // the trace, when the node serves it: what is expected to move, in sentences
                match rpc.trace_call(&call) {
                    Ok(trace) => {
                        let summary = igneum_wallet_core::simulation::summarise(&trace, &from);
                        let ok = summary.reverted.is_none();
                        json!({"ok": ok, "text": if ok { "expected to succeed" } else { "this would fail" }, "lines": summary.sentences, "traced": true})
                    }
                    Err(e) => json!({"ok": true, "text": "expected to succeed", "lines": [if e.to_string().contains("debug namespace") { "the node serves no trace, so the call was checked without one".to_string() } else { format!("checked without a trace ({e})") }], "traced": false}),
                }
            }
            Err(e) => json!({"ok": false, "text": format!("this would fail: {e}"), "lines": [], "traced": false}),
        } };
        // a review while the wallet is locked reads sendable false and says so (wallet-security's finding 13,
        // 12 October 2026: the Desk's review read sendable after the idle lock, and only the send refused)
        let unlock_first = !self.unlocked() && self.watch.is_none();
        let sendable = (simulation.get("ok").and_then(|o| o.as_bool()).unwrap_or(false) || env.transaction.gas_limit() > 0) && !unlock_first;
        let fee_max = env.transaction.max_fee().unwrap_or(U256::ZERO);
        let total = value.checked_add(&fee_max).unwrap_or(U256::MAX);
        let mut id_bytes = [0u8; 16];
        OsRng.fill_bytes(&mut id_bytes);
        let id = hex::encode(&id_bytes);
        let enough = self.node.balance.map(|b| b >= total).unwrap_or(true);
        // the biometric challenge for this quote: the prompt's line and the nonce the host confirms; needed only
        // while the prompt is enrolled (the page shows the control then)
        let (confirm_nonce, confirm_reason) = self.challenge_for_send(&env);
        let confirm_needed = self.bio.enrolled;
        let out = json!({
            "quote_id": id,
            "confirm_nonce": confirm_nonce,
            "confirm_reason": confirm_reason,
            "confirm_needed": confirm_needed,
            "sentences": env.sentences,
            "value_ign": display::ign(&value, 6),
            "fee_max_ign": display::ign(&fee_max, 9),
            "total_max_ign": display::ign(&total, 9),
            // the exact bounds in wei, for applications and tests; the IGN strings above are for people
            "value_wei": value.to_dec_string(),
            "fee_max_wei": fee_max.to_dec_string(),
            "total_max_wei": total.to_dec_string(),
            "display_to": to.short(),
            "nonce": nonce,
            "watch_only": self.watch.is_some(),
            "simulation": simulation,
            "enough": enough,
            "sendable": sendable,
            "unlock_first": unlock_first,
            // N-3: a send on a chain whose genesis is not this build's pin is labelled, never silent
            "chain_note": if self.chain.genesis_pinned { Value::Null } else { json!(format!(
                "This send goes to {} (chain id {}), whose genesis is not this build's pin: a development node the wallet does not vouch for.",
                self.chain.name, self.chain.chain_id)) },
        });
        self.quotes.retain(|_, q| q.created.elapsed() < Duration::from_secs(180));
        if sendable {
            self.quotes.insert(id, Quote { envelope: env, created: Instant::now(), moves: None });
        }
        Ok(out)
    }

    /// A mining identity's share of the draw, from the chain's own inputs (DESIGN.md 1.13).
    fn draw(&self, key: Option<&str>, at: Option<u64>) -> Result<Value> {
        use igneum_wallet_authority::draw;
        use igneum_wallet_core::authority;
        let rpc = self.rpc();
        let registry = self.chain.registry().ok_or_else(|| Error::Input(format!("{} carries no bond registry", self.chain.name)))?;
        let s = rpc.igneum_sortition()?;
        let params = s.get("params").cloned().unwrap_or(json!({}));
        let p = draw::Params {
            expected: params.get("Expected").and_then(|v| v.as_u64()).unwrap_or(1),
            max_round: params.get("MaxRound").and_then(|v| v.as_u64()).unwrap_or(16),
            unbonded_round: params.get("UnbondedRound").and_then(|v| v.as_u64()).unwrap_or(17),
            delta_secs: params.get("Delta").and_then(|v| v.as_u64()).unwrap_or(20),
        };
        let head = s.get("head").and_then(|v| v.as_u64()).unwrap_or(0);
        let mut epoch_blocks = params.get("EpochBlocks").and_then(|v| v.as_u64()).unwrap_or(5760);
        if let Some(changes) = params.get("EpochChanges").and_then(|v| v.as_array()) {
            for c in changes {
                if c.get("Height").and_then(|h| h.as_u64()).unwrap_or(u64::MAX) <= head {
                    epoch_blocks = c.get("EpochBlocks").and_then(|e| e.as_u64()).unwrap_or(epoch_blocks);
                }
            }
        }
        let key_bytes = match key {
            Some(k) => authority::parse_key(k)?,
            None => authority::parse_key(s.get("bondKey").and_then(|v| v.as_str()).ok_or_else(|| Error::Rpc("the node has no bond key of its own; name one".into()))?)?,
        };
        let height = at.unwrap_or_else(|| s.get("next").and_then(|v| v.as_u64()).unwrap_or(head + 1));
        let snapshot = s.get("snapshotBlock").and_then(|v| v.as_u64()).unwrap_or(0);
        let snap_tag = U256::from_u64(snapshot).to_hex();
        let bond_now = rpc.igneum_bond_of(&key_bytes, "latest")?;
        let bond_snap = rpc.igneum_bond_of(&key_bytes, &snap_tag)?;
        let exit_addr = rpc
            .eth_call(&json!({"to": registry.to_checksum(), "data": hex::encode(&authority::bond_data(&key_bytes))}))
            .ok()
            .and_then(|out| authority::decode_read(&out).ok())
            .and_then(|r| r.exit)
            .map(|e| e.to_checksum());
        let total_snap = U256::from_hex(rpc.call("eth_getBalance", json!([registry.to_checksum(), snap_tag]))?.as_str().unwrap_or("0x0"))?;
        let amount_snap = U256::from_hex(bond_snap.get("amount").and_then(|v| v.as_str()).unwrap_or("0x0"))?;
        let seed = authority::parse_key(s.get("seed").and_then(|v| v.as_str()).unwrap_or("0x0")).unwrap_or([0u8; 32]);
        let share = draw::share(&amount_snap, &total_snap, p.expected, epoch_blocks);
        // the block the bond enters the draw at: the snapshot two epochs after the post's epoch
        let enters_at = {
            let mut pre = b"IgneumHash/R0A/bond-height".to_vec();
            pre.extend_from_slice(&key_bytes);
            let posted = rpc.call("eth_getStorageAt", json!([registry.to_checksum(), hex::encode(&igneum_wallet_core::keccak::keccak256(&pre)[..]), "latest"]))
                .ok().and_then(|v| v.as_str().and_then(|h| U256::from_hex(h).ok())).and_then(|v| v.to_u64()).unwrap_or(0);
            if posted > 0 && epoch_blocks > 0 { Some((posted / epoch_blocks + 2) * epoch_blocks) } else { None }
        };
        let round = draw::eligible_round(&p, &seed, &key_bytes, share.units, height);
        let words = if round > p.max_round {
            format!("not drawn by round {} at height {}: only the unbonded round {} is open to it", p.max_round, height, p.unbonded_round)
        } else {
            format!("eligible at round {} of height {} ({} s after the parent at the earliest)", round, height, round * p.delta_secs)
        };
        Ok(json!({
            "chain": self.chain.name, "registry": registry.to_checksum(), "key": hex::encode(&key_bytes),
            "bond_now": {"amount_ign": display::ign(&U256::from_hex(bond_now.get("amount").and_then(|v| v.as_str()).unwrap_or("0x0"))?, 6), "account": bond_now.get("account"), "signer": bond_now.get("signer"), "bonded": bond_now.get("bonded"), "exit": exit_addr},
            "snapshot": {"block": snapshot, "epoch": s.get("epoch"), "bond_ign": display::ign(&amount_snap, 6), "total_ign": display::ign(&total_snap, 6)},
            "enters_draw_block": enters_at,
            "draw": {"units": share.units, "share_percent": format!("{:.4}", share.share * 100.0), "round0_chance": share.round0_chance, "round0_heights_per_epoch": share.round0_heights_per_epoch, "epoch_blocks": epoch_blocks},
            "height": height, "eligible_round": round, "eligible_words": words,
            // where the reading was taken: the head the draw was read at and when that head was seen (the page's
            // "read from the chain at head N, M s ago")
            "head": head, "head_unix": self.node.head_unix, "read_unix": now_unix(),
            "rule": {"expected": p.expected, "max_round": p.max_round, "unbonded_round": p.unbonded_round, "delta_secs": p.delta_secs, "sortition_active": s.get("active")},
        }))
    }

    /// The holder's system (the founder's correction of 12 October 2026, 00:0x UK, through main): "bond X IGN; on
    /// this chain you are drawn about Y times a day and paid about Z per seal", every number read from the chain at
    /// this moment with its height, nothing projected beyond the chain's own recent window (the last 100 blocks),
    /// no yield or return word. X is the amount asked, else the balance less a fee reserve; Y is the round-0
    /// eligibility per day of a bond X against the live table (`draw::share` over total + X, the epoch's blocks,
    /// the cadence measured over the last 100 blocks); Z is the sealer's cut of the recent subsidy (the issuance
    /// per block over the last 100 blocks net of the fund's percent, times the rule's sealer fraction, a quarter:
    /// ECONOMICS-RESTART.md 2.2.2, "(1 minus rho) x the subsidy"; the node does not serve rho, so the fraction is
    /// the rule's and named as such).
    fn holder(&self, amount: Option<&str>) -> Result<Value> {
        use igneum_wallet_authority::draw;
        let rpc = self.rpc();
        let s = rpc.igneum_sortition()?;
        let params = s.get("params").cloned().unwrap_or(json!({}));
        let expected = params.get("Expected").and_then(|v| v.as_u64()).unwrap_or(1);
        let epoch_blocks = params.get("EpochBlocks").and_then(|v| v.as_u64()).unwrap_or(5760);
        let head = s.get("head").and_then(|v| v.as_u64()).unwrap_or(0);
        let total = U256::from_hex(s.get("total").and_then(|v| v.as_str()).unwrap_or("0x0")).unwrap_or(U256::ZERO);
        let window: u64 = 100;
        let back = head.saturating_sub(window);
        let (sub_per_block, cadence_secs) = if head > back {
            let sup_h = rpc.call("igneum_supply", json!([U256::from_u64(head).to_hex()]))?;
            let sup_b = rpc.call("igneum_supply", json!([U256::from_u64(back).to_hex()]))?;
            let q = |v: &Value, k: &str| U256::from_hex(v.get(k).and_then(|x| x.as_str()).unwrap_or("0x0")).unwrap_or(U256::ZERO);
            let issued = q(&sup_h, "issued").to_f64() - q(&sup_b, "issued").to_f64();
            let fund = q(&sup_h, "fundIssued").to_f64() - q(&sup_b, "fundIssued").to_f64();
            let bh = rpc.block(head)?.unwrap_or(json!({}));
            let bb = rpc.block(back)?.unwrap_or(json!({}));
            let t = |b: &Value| U256::from_hex(b.get("timestamp").and_then(|x| x.as_str()).unwrap_or("0x0")).ok().and_then(|x| x.to_u64()).unwrap_or(0) as f64;
            let secs = (t(&bh) - t(&bb)) / (head - back) as f64;
            ((issued - fund) / (head - back) as f64 / 1e18, if secs > 0.0 { secs } else { self.chain.block_target_secs as f64 })
        } else {
            (0.0, self.chain.block_target_secs as f64)
        };
        let sealer_fraction = 0.25;
        // the registry's version AS READ from the chain (wallet-security's W-1, the project's ruling of 12 October 2026):
        // a recorded post height for the node's own bond key (the slot keccak("IgneumHash/R0A/bond-height" || K),
        // written only by version 4) means the penalties are live, whatever a chains file or this pin says; with no
        // key to read, the pin's version stands
        let mut version = self.chain.authority.as_ref().map(|a| a.registry_version).unwrap_or(0);
        if let (Some(reg), Some(k)) = (self.chain.registry(), s.get("bondKey").and_then(|v| v.as_str())) {
            if let Ok(key) = igneum_wallet_core::authority::parse_key(k) {
                let mut pre = b"IgneumHash/R0A/bond-height".to_vec();
                pre.extend_from_slice(&key);
                let slot = igneum_wallet_core::keccak::keccak256(&pre);
                if let Ok(v) = rpc.call("eth_getStorageAt", json!([reg.to_checksum(), hex::encode(&slot[..]), "latest"])) {
                    if v.as_str().map(|h| h.trim_start_matches("0x").trim_start_matches('0').len() > 0).unwrap_or(false) {
                        version = 4;
                    }
                }
            }
        }
        let reserve = U256::from_u64(50_000_000_000_000_000); // 0.05 IGN kept for fees
        let balance = self.balance_wei();
        // the chain's unit price (igneum_sortition's unitPrice: B_floor over 65,536; 28.76 IGN on devnet9 at 1,537),
        // never the balance over 65,536 (the project's read of the 0.0.5 captures, 12 October 2026)
        let unit_price = s.get("unitPrice").and_then(|v| v.as_str()).and_then(|v| U256::from_hex(v).ok()).filter(|u| !u.is_zero())
            .unwrap_or_else(|| { let mut t = total.to_f64() / 65536.0; if t <= 0.0 { t = 1e18 } U256::from_u64(t as u64) });
        // the figures compute on the amount typed, else on the current bond the page names, else on one unit's worth;
        // never on the whole balance
        let x = match amount {
            Some(a) if !a.trim().is_empty() => display::parse_ign(a)?,
            _ => unit_price,
        };
        let total_after = total.checked_add(&x).unwrap_or(total);
        let share = draw::share(&x, &total_after, expected, epoch_blocks);
        let units = (x.to_f64() / unit_price.to_f64()).floor() as u64;
        let blocks_per_day = 86_400.0 / cadence_secs;
        let drawn_per_day = share.round0_chance * blocks_per_day;
        let z = sub_per_block * sealer_fraction;
        let x_ign = display::ign(&x, 4);
        let bal_short = balance.checked_sub(&reserve).unwrap_or(U256::ZERO);
        let words = if balance.is_zero() {
            format!("Nothing to bond yet: this wallet holds no IGN. On this chain a bond is drawn in proportion to its units of the table (head {}).", Self::commas(head))
        } else {
            format!(
                "Bond {} IGN: {} of the draw; on this chain you would be drawn first about {} and paid about {:.3} IGN per seal (the sealer's quarter of the block's subsidy, {:.3} IGN over the last {} blocks); read at head {}, {:.0} s a block.",
                Self::commas_ign(&x_ign), Self::units_words(units), Self::times_words(drawn_per_day), z, sub_per_block, head - back, Self::commas(head), cadence_secs
            )
        };
        let _ = bal_short;
        Ok(json!({
            "chain": self.chain.name, "head": head, "read_unix": now_unix(), "window_blocks": head - back, "cadence_secs": cadence_secs,
            "bond_ign": x_ign, "balance_ign": display::ign(&balance, 4), "table_total_ign": display::ign(&total, 2),
            "unit_price_ign": format!("{:.2}", unit_price.to_f64() / 1e18),
            "units": units, "units_words": Self::units_words(units), "share_percent": format!("{:.4}", share.share * 100.0), "round0_chance": share.round0_chance,
            "drawn_per_day": drawn_per_day, "subsidy_per_block_ign": sub_per_block, "sealer_fraction": sealer_fraction, "paid_per_seal_ign": z,
            "words": words,
            "risk": if version >= 4 {
                "Your whole bond is forfeited if your signing key seals or endorses two different blocks at one height (one block's reward of it to the reporter, the rest burnt, the key marked for good); anyone holding your bond secret can take three quarters of the bond to any address at once, a quarter burnt; nothing is released before the post height plus the chain's lock in blocks (a genesis field; the node serves its number in its next release), then the whole bond to the exit address; a missed draw costs nothing. Nothing here is a yield or a return; this is a development chain and can be reset."
            } else {
                "On this chain the registry is version 3: the penalties (equivocation forfeits the whole bond; a hostile claim burns a quarter) are built for the node and run on the version-4 chains, not here; the bond leaves the table two epochs after you set the exit, nothing sooner. Nothing here is a yield or a return; this is a development chain and can be reset."
            },
            "registry_version": version,
            "rule": {"expected": expected, "epoch_blocks": epoch_blocks, "registry_version_source": if version >= 4 { "read from the chain: a post height recorded for the node's bond key" } else { "the pin; no post height read" }, "sealer_fraction_source": "ECONOMICS-RESTART.md 2.2.2: (1 minus rho) x the subsidy, rho three quarters; not served by the node"},
        }))
    }

    fn balance_wei(&self) -> U256 {
        self.node.balance.unwrap_or(U256::ZERO)
    }

    /// The Live Work Ledger's wallet side (the outside review's seventh reading; the settlement team's miner-view v1,
    /// 12 October 2026): the IGN arriving per block, from the chain and the signed receipts, never a pool balance.
    /// Takes the miner-view JSON text; checks its schema and chain id against this engine's chain; for every block
    /// row reads this engine's own balance of the address at the height and the height before (the balance step)
    /// and marks the row reproduced when the step equals paid plus sealer wei, differs otherwise; keeps paid_wei
    /// (the share entries) and sealer_wei (the sealer's cut, on a sealed block) apart as the chain pays them
    /// apart; the identity line from totals. Nothing estimated, nothing from a pool database.
    /// The scene's feed (the founder's brief of 12 October 2026: the network page's animation adapted so a person
    /// watches the address's payments arriving in blocks): the address's payments since a block, from the chain's
    /// own read. A block where the address's balance rose without the address sending is a payment; its kind from
    /// the block: the block's miner is the address (seal), a WorkPay Paid log for the address in it (work, with
    /// its window), a transaction to the address (transfer, with its sender), else the chain's credit (reward).
    /// At most fifty blocks a call under the public host's twenty calls a second; the page polls once a block.
    fn payments(&self, since: Option<u64>) -> Result<Value> {
        const WORKPAY: &str = "0x15411e6ec2b174dbd838edacf62807edf60b9ddd";
        // Paid(uint64 indexed window, address indexed account, uint256 weight, uint256 amount): the topic is the
        // signature's keccak, the window topic 1, the account topic 2
        let me = self.address().ok_or_else(|| Error::Refused("no wallet".into()))?;
        let me_hex = me.to_checksum().to_ascii_lowercase();
        let rpc = self.rpc();
        let head = rpc.block_number()?;
        let from = match since {
            Some(n) if n < head => n + 1,
            Some(_) => return Ok(json!({"address": me.to_checksum(), "head": head, "payments": []})),
            // the first answer carries the last twenty blocks (the stage fills its whole width at the first draw:
            // fifteen blocks at 900 px plus two off the left edge, the showcase's one form of 12 October 2026)
            None => head.saturating_sub(20),
        };
        let to = head.min(from + 49);
        // the window's balances and headers are read in batches of twenty (the public edge counts a batch as one
        // request): the first document on the public listener took over ten seconds call by call under its rate
        // limit, and the home's chain card stood empty at the capture (the project's read of the 0.0.7 Windows capture set, 12 October
        // 2026, 20:3x UK); the whole opening read is now a handful of requests
        let heights: Vec<u64> = (from.saturating_sub(1)..=to).collect();
        let mut bal_map: std::collections::HashMap<u64, U256> = std::collections::HashMap::new();
        let mut blk_map: std::collections::HashMap<u64, Value> = std::collections::HashMap::new();
        for chunk in heights.chunks(20) {
            let calls: Vec<(&str, Value)> = chunk.iter().map(|h| ("eth_getBalance", json!([me.to_checksum(), U256::from_u64(*h).to_hex()]))).collect();
            for (h, r) in chunk.iter().zip(rpc.call_batch(&calls)?) {
                let r = r?;
                bal_map.insert(*h, U256::from_hex(r.as_str().unwrap_or("0x0")).unwrap_or(U256::ZERO));
            }
            let calls: Vec<(&str, Value)> = chunk.iter().map(|h| ("eth_getBlockByNumber", json!([U256::from_u64(*h).to_hex(), true]))).collect();
            for (h, r) in chunk.iter().zip(rpc.call_batch(&calls)?) {
                blk_map.insert(*h, r?);
            }
        }
        let bal = |h: u64| -> Result<U256> {
            if let Some(v) = bal_map.get(&h) { return Ok(*v); }
            let r = rpc.call("eth_getBalance", json!([me.to_checksum(), U256::from_u64(h).to_hex()]))?;
            Ok(U256::from_hex(r.as_str().unwrap_or("0x0")).unwrap_or(U256::ZERO))
        };
        let blk = |h: u64| -> Result<Value> {
            if let Some(v) = blk_map.get(&h) { if !v.is_null() { return Ok(v.clone()); } }
            Ok(rpc.block_with_transactions(h)?.unwrap_or(json!({})))
        };
        let topic_paid = {
            let sig = igneum_wallet_core::keccak::keccak256(b"Paid(uint64,address,uint256,uint256)");
            hex::encode(&sig[..])
        };
        let me_topic = format!("0x{:0>64}", me_hex.trim_start_matches("0x"));
        let logs = rpc
            .call("eth_getLogs", json!([{"fromBlock": format!("0x{:x}", from), "toBlock": format!("0x{:x}", to), "address": WORKPAY, "topics": [topic_paid, Value::Null, me_topic]}]))
            .ok()
            .and_then(|v| v.as_array().cloned())
            .unwrap_or_default();
        let mut out = Vec::new();
        let mut before = bal(from.saturating_sub(1))?;
        for h in from..=to {
            let after = bal(h)?;
            if after > before {
                let step = after.checked_sub(&before).unwrap_or(U256::ZERO);
                let b = blk(h)?;
                let unix = b.get("timestamp").and_then(|t| t.as_str()).and_then(|t| U256::from_hex(t).ok()).and_then(|t| t.to_u64()).unwrap_or(0);
                let hash = b.get("hash").cloned().unwrap_or(Value::Null);
                let miner = b.get("miner").and_then(|m| m.as_str()).unwrap_or("").to_ascii_lowercase();
                // the block's sealer is its bondKey (the identity key the header carries), never the miner field,
                // which is the block's coinbase and differs block to block on devnet9 (the project's read of the 0.0.6 home,
                // 12 October 2026: twelve blocks with twelve "sealers" folded into "others")
                let sealer = b.get("bondKey").and_then(|k| k.as_str()).filter(|k| !k.is_empty() && k.trim_start_matches("0x").chars().any(|c| c != '0')).map(|k| k.to_ascii_lowercase()).unwrap_or_else(|| miner.clone());
                let paid = logs.iter().find(|l| l.get("blockNumber").and_then(|n| n.as_str()).and_then(|n| U256::from_hex(n).ok()).and_then(|n| n.to_u64()) == Some(h));
                let tx_in = b.get("transactions").and_then(|t| t.as_array()).and_then(|txs| txs.iter().find(|t| t.get("to").and_then(|x| x.as_str()).map(|x| x.eq_ignore_ascii_case(&me_hex)).unwrap_or(false)));
                let (kind, from_addr, window) = if let Some(l) = paid {
                    let w = l.get("topics").and_then(|t| t.as_array()).and_then(|t| t.get(1)).and_then(|t| t.as_str()).and_then(|t| U256::from_hex(t).ok()).and_then(|t| t.to_u64());
                    ("work", Some(WORKPAY.to_string()), w)
                } else if miner == me_hex {
                    ("seal", None, None)
                } else if let Some(t) = tx_in {
                    ("transfer", t.get("from").and_then(|f| f.as_str()).map(|f| f.to_string()), None)
                } else {
                    ("reward", None, None)
                };
                out.push(json!({"block": h, "hash": hash, "sealer": sealer, "kind": kind, "amount_wei": step.to_dec_string(), "amount_ign": display::ign(&step, 18), "from": from_addr, "window": window, "unix": unix}));
            } else {
                // a block that paid the address nothing (the showcase team's contract, kind "none"): the scene draws
                // the block with no spark so the lane keeps moving between payments; the header alone is read
                let b = blk(h)?;
                let unix = b.get("timestamp").and_then(|t| t.as_str()).and_then(|t| U256::from_hex(t).ok()).and_then(|t| t.to_u64()).unwrap_or(0);
                out.push(json!({"block": h, "hash": b.get("hash").cloned().unwrap_or(Value::Null), "sealer": b.get("bondKey").and_then(|k| k.as_str()).filter(|k| k.trim_start_matches("0x").chars().any(|c| c != '0')).map(|k| k.to_ascii_lowercase()).unwrap_or_else(|| b.get("miner").and_then(|m| m.as_str()).unwrap_or("").to_ascii_lowercase()), "kind": "none", "amount_wei": "0", "amount_ign": "0", "from": Value::Null, "window": Value::Null, "unix": unix}));
            }
            before = after;
        }
        // the head's sealed time and the chain's live mean over its last twenty blocks (the scene's countdown anchor
        // and interval, the founder's "show the work between blocks", 12 October 2026)
        let ts = |h: u64| -> u64 { blk(h).ok().and_then(|b| b.get("timestamp").and_then(|t| t.as_str()).and_then(|t| U256::from_hex(t).ok()).and_then(|t| t.to_u64())).unwrap_or(0) };
        let head_unix = ts(head);
        let back = head.saturating_sub(20);
        let back_unix = if back < head { ts(back) } else { 0 };
        let interval = if head_unix > back_unix && back < head { (head_unix - back_unix) as f64 / (head - back) as f64 } else { self.chain.block_target_secs as f64 };
        Ok(json!({"address": me.to_checksum(), "head": head, "head_unix": head_unix, "block_interval_s": (interval * 10.0).round() / 10.0, "server_unix": now_unix(), "from": from, "to": to, "payments": out,
                  "source": "the chain's own read: the address's balance at each block, the block's sealer, WorkPay's Paid logs, the block's transactions"}))
    }

    /// The watched address's live shares from the public pool's feed (ecosystem's per-address document, refreshed every
    /// 2 s on the host; 404 when no share of this address landed in the last ten minutes), read through the engine so
    /// the page needs no reach beyond it; cached for two seconds.
    fn shares_feed(&mut self) -> Result<Value> {
        let me = self.address().ok_or_else(|| Error::Refused("no wallet".into()))?;
        let now = Instant::now();
        if let Some((at, v)) = &self.shares_cache {
            if now.duration_since(*at) < Duration::from_secs(2) {
                return Ok(v.clone());
            }
        }
        let url = format!("https://build.igneum.network/feed/shares/{}.json", me.to_checksum().to_ascii_lowercase());
        let agent = ureq::AgentBuilder::new().timeout(Duration::from_secs(10)).build();
        let mut v = match agent.get(&url).call() {
            Ok(r) => r.into_json::<Value>().map_err(|e| Error::Rpc(format!("the shares feed answered badly: {e}")))?,
            Err(ureq::Error::Status(404, _)) => json!({"none": true, "address": me.to_checksum()}),
            Err(ureq::Error::Status(code, _)) => json!({"none": true, "error": format!("the shares feed refused ({code})")}),
            Err(e) => json!({"none": true, "error": format!("the shares feed is not reachable: {e}")}),
        };
        // the rate beside the mean block time is the NETWORK's shares a second (the founder's words: "the network's
        // shares per second beside the mean block time"); the address's own rate is kept under its own name
        if let Ok(r) = agent.get("https://build.igneum.network/feed/shares.json").call() {
            if let Ok(net) = r.into_json::<Value>() {
                if let Some(rate) = net.get("shares_per_s_10s").cloned() {
                    if let Some(o) = v.as_object_mut() {
                        if let Some(own) = o.get("shares_per_s_10s").cloned() { o.insert("address_shares_per_s_10s".into(), own); }
                        o.insert("shares_per_s_10s".into(), rate);
                        o.insert("network_shares_per_s_10s".into(), net.get("shares_per_s_10s").cloned().unwrap_or(Value::Null));
                    }
                }
            }
        }
        self.shares_cache = Some((now, v.clone()));
        Ok(v)
    }

    fn ledger(&self, text: &str) -> Result<Value> {
        let v: Value = serde_json::from_str(text).map_err(|e| Error::Input(format!("the miner view is not JSON: {e}")))?;
        let schema = v.get("schema").and_then(|x| x.as_str()).unwrap_or("");
        if schema != "igneum/ledger/miner-view/v1" {
            return Err(Error::Input(format!("the schema is {schema:?}, not igneum/ledger/miner-view/v1")));
        }
        let chain_id = v.get("chain_id").and_then(|x| x.as_u64()).unwrap_or(0);
        if chain_id != self.chain.chain_id {
            return Err(Error::Input(format!("the view is of chain {chain_id}; this wallet reads {} (chain {})", self.chain.name, self.chain.chain_id)));
        }
        let address = v.get("address").and_then(|x| x.as_str()).unwrap_or("");
        let addr = Address::parse(address)?;
        let mine = self.address().map(|a| a == addr).unwrap_or(false);
        let rpc = self.rpc();
        let wei = |x: Option<&Value>| x.and_then(|y| y.as_str()).and_then(|y| y.parse::<u128>().ok()).unwrap_or(0);
        let mut rows = Vec::new();
        let mut reproduced = 0usize;
        let mut differs = 0usize;
        let mut arrived: u128 = 0;
        if let Some(blocks) = v.get("blocks").and_then(|b| b.as_array()) {
            for b in blocks.iter().take(64) {
                let height = b.get("height").and_then(|h| h.as_u64()).unwrap_or(0);
                let paid = wei(b.get("paid_wei"));
                let sealer = wei(b.get("sealer_wei"));
                let sealed = b.get("sealed").and_then(|x| x.as_bool()).unwrap_or(false);
                let bal_at = |h: u64| -> Result<u128> {
                    let r = rpc.call("eth_getBalance", json!([addr.to_checksum(), U256::from_u64(h).to_hex()]))?;
                    Ok(u128::from_str_radix(r.as_str().unwrap_or("0x0").trim_start_matches("0x"), 16).unwrap_or(0))
                };
                let (after, before) = (bal_at(height)?, if height > 0 { bal_at(height - 1)? } else { 0 });
                let step = after.saturating_sub(before);
                let ok = step == paid + sealer;
                if ok { reproduced += 1 } else { differs += 1 }
                arrived += paid + sealer;
                rows.push(json!({
                    "height": height, "hash": b.get("hash"), "paid_ign": display::ign(&U256::from_dec_str(&paid.to_string())?, 6),
                    "sealed": sealed, "sealer_ign": display::ign(&U256::from_dec_str(&sealer.to_string())?, 6),
                    "balance_after_ign": display::ign(&U256::from_dec_str(&after.to_string())?, 6),
                    "step_wei": step.to_string(), "reproduced": ok,
                    "words": format!("Block {}: {} IGN paid for shares{}; balance after {} IGN; {}", height, display::ign(&U256::from_dec_str(&paid.to_string())?, 4),
                        if sealed { format!(", sealed, the sealer's cut {} IGN", display::ign(&U256::from_dec_str(&sealer.to_string())?, 4)) } else { String::new() },
                        display::ign(&U256::from_dec_str(&after.to_string())?, 4),
                        if ok { "reproduced by this wallet's own read" } else { "DIFFERS from this wallet's own read" }),
                }));
            }
        }
        let t = v.get("totals").cloned().unwrap_or(json!({}));
        let n = |k: &str| t.get(k).and_then(|x| x.as_u64()).unwrap_or(0);
        let identity_ok = n("accepted") == n("paid") + n("in_carry") + n("pooled") + n("expired_window") + n("expired_reorg");
        Ok(json!({
            "address": addr.to_checksum(), "mine": mine, "chain": self.chain.name, "view_head": v.get("head"), "read_at": v.get("read_at"),
            "ledger": v.get("ledger"), "rows": rows, "reproduced": reproduced, "differs": differs,
            "arrived_ign": display::ign(&U256::from_dec_str(&arrived.to_string())?, 6),
            "totals": t, "identity": format!("accepted {} = paid {} + in carry {} + pooled {} + expired (window) {} + expired (reorg) {}: {}", n("accepted"), n("paid"), n("in_carry"), n("pooled"), n("expired_window"), n("expired_reorg"), if identity_ok { "closes" } else { "DOES NOT CLOSE" }),
            "identity_ok": identity_ok,
            "source": "the chain and the signed receipts, read again by this wallet; never a pool database, nothing estimated",
        }))
    }

    /// The Authority Desk's numbers from the chain (wallet-security's AUTHORITY-DESK-SPEC.md, sections 1.3, 1.4's
    /// observed column, 1.7 and 1.8; the outside review's seventh reading): for a bond key, the exact IGN locked, the post
    /// height and the lock (`lockBlocks` from igneum_authority, the genesis's field), the earliest exit at the
    /// chain's measured pace, the predicted round-0 draws over the window beside the observed seals from the
    /// headers (bondKey and round), the penalty table with this key's own slots (marked, the claim counter, the
    /// burnt supply), every number with the head it was read at. Nothing is a yield, a return or an annual figure;
    /// nothing comes from an indexer. The per-height eligibility recompute (eligibleRound0 from the epoch's table
    /// at each parent) is wallet-security's desk-read.py until the engine carries the table walk; the row says so.
    fn desk(&self, key: Option<&str>) -> Result<Value> {
        use igneum_wallet_authority::draw;
        use igneum_wallet_core::{authority, keccak::keccak256};
        let rpc = self.rpc();
        let registry = self.chain.registry().ok_or_else(|| Error::Input(format!("{} carries no bond registry", self.chain.name)))?;
        let reg = registry.to_checksum();
        let s = rpc.igneum_sortition()?;
        let auth = rpc.call("igneum_authority", json!([])).unwrap_or(json!({}));
        let params = s.get("params").cloned().unwrap_or(json!({}));
        let expected = params.get("Expected").and_then(|v| v.as_u64()).unwrap_or(1);
        let epoch_blocks = params.get("EpochBlocks").and_then(|v| v.as_u64()).unwrap_or(5760);
        let head = s.get("head").and_then(|v| v.as_u64()).unwrap_or(0);
        let key_bytes = match key {
            Some(k) if !k.trim().is_empty() => authority::parse_key(k)?,
            _ => authority::parse_key(s.get("bondKey").and_then(|v| v.as_str()).ok_or_else(|| Error::Rpc("the node has no bond key of its own; name one".into()))?)?,
        };
        let khex = hex::encode(&key_bytes[..]); // the wallet's hex::encode carries the 0x prefix
        let slot = |tag: &[u8]| -> String {
            let mut pre = tag.to_vec();
            pre.extend_from_slice(&key_bytes);
            hex::encode(&keccak256(&pre)[..])
        };
        let word = |v: &Result<Value>| -> U256 { v.as_ref().ok().and_then(|x| x.as_str()).and_then(|h| U256::from_hex(h).ok()).unwrap_or(U256::ZERO) };
        let reads = rpc.call_batch(&[
            ("eth_getStorageAt", json!([reg, slot(b"IgneumHash/R0A/bond-height"), "latest"])),
            ("eth_getStorageAt", json!([reg, slot(b"IgneumHash/R0A/marked"), "latest"])),
            ("eth_getStorageAt", json!([reg, slot(b"IgneumHash/R0A/claim-nonce"), "latest"])),
            ("eth_getStorageAt", json!([reg, "0x0000000000000000000000000000000000000000000000000000000000000005", "latest"])),
            ("eth_getStorageAt", json!([reg, "0x0000000000000000000000000000000000000000000000000000000000000003", "latest"])),
            ("igneum_bondOf", json!([khex.clone(), "latest"])),
            ("eth_getBlockByNumber", json!([U256::from_u64(head).to_hex(), false])),
            ("eth_getBlockByNumber", json!([U256::from_u64(head.saturating_sub(100)).to_hex(), false])),
        ])?;
        let posted = word(&reads[0]).to_u64().unwrap_or(0);
        let marked = !word(&reads[1]).is_zero();
        let claims = word(&reads[2]).to_u64().unwrap_or(0);
        let burnt = word(&reads[3]);
        let keys_total = word(&reads[4]).to_u64().unwrap_or(0);
        let bond = reads[5].as_ref().ok().cloned().unwrap_or(json!({}));
        let amount = U256::from_hex(bond.get("amount").and_then(|v| v.as_str()).unwrap_or("0x0")).unwrap_or(U256::ZERO);
        let ts = |v: &Result<Value>| -> f64 { v.as_ref().ok().and_then(|b| b.get("timestamp")).and_then(|t| t.as_str()).and_then(|t| U256::from_hex(t).ok()).and_then(|t| t.to_u64()).unwrap_or(0) as f64 };
        let span = head.min(100) as f64;
        // a header read that failed or carries no timestamp (the public endpoint's bucket) gives no pace and no
        // age: the chain's target stands in and the words say the read is partial (the project's 0.0.6 order on
        // participant 7: a pace of 17,918,064 s a block and a head "497723 h old" were such reads turned into figures)
        let (t6, t7) = (ts(&reads[6]), ts(&reads[7]));
        let header_read = t6 > 0.0 && t7 > 0.0 && t6 >= t7;
        let cadence = if header_read && span > 0.0 { (t6 - t7) / span } else { 0.0 };
        let cadence = if cadence > 0.0 && cadence < 3600.0 { cadence } else { self.chain.block_target_secs as f64 };
        let head_age = if t6 > 0.0 { (now_unix() as f64 - t6).max(0.0) } else { 0.0 };
        let stale = header_read && head_age > 6.0 * cadence;
        let version = auth.get("registryVersion").and_then(|v| v.as_u64()).unwrap_or(if posted > 0 { 4 } else { 3 });
        let lock_blocks = auth.get("lockBlocks").and_then(|v| v.as_u64());
        let earliest = lock_blocks.map(|l| posted + l);
        // the window: min(EpochBlocks, head - posted), at most a day's blocks at this pace, the headers in batches
        // of twenty (the edge counts a batch as one request), never more than ten batches a refresh
        let w = if posted > 0 { epoch_blocks.min(head.saturating_sub(posted)) } else { epoch_blocks.min(head) }.min(200);
        let mut sealed = 0u64;
        let mut sealed_round0 = 0u64;
        let mut rounds: std::collections::BTreeMap<u64, u64> = std::collections::BTreeMap::new();
        let mut heights_sealed: Vec<u64> = Vec::new();
        let mut read_errors: Vec<String> = Vec::new();
        let mut h = head.saturating_sub(w) + 1;
        while h <= head {
            let to = (h + 19).min(head);
            let calls: Vec<(&str, Value)> = (h..=to).map(|n| ("eth_getBlockByNumber", json!([format!("0x{:x}", n), false]))).collect();
            // the public listener's bucket counts every call of a batch (devnet9's A9: twenty a second, a burst of
            // forty; "rate limited" per slot beyond it), so a batch of twenty goes once a second and a limited
            // batch is read again after a pause
            let mut hs = rpc.call_batch(&calls);
            if let Ok(ref v) = hs {
                if v.iter().any(|b| b.as_ref().err().map(|e| e.to_string().contains("rate limited")).unwrap_or(false)) {
                    std::thread::sleep(Duration::from_millis(1500));
                    hs = rpc.call_batch(&calls);
                }
            }
            match hs {
                Err(e) => read_errors.push(format!("blocks {h} to {to}: {e}")),
                Ok(hs) => for (i, b) in hs.iter().enumerate() {
                    if let Err(e) = b {
                        read_errors.push(format!("block {}: {e}", h + i as u64));
                        continue;
                    }
                    if let Ok(b) = b {
                        let bk = b.get("bondKey").and_then(|x| x.as_str()).unwrap_or("");
                        let round = b.get("round").and_then(|x| x.as_str()).and_then(|x| U256::from_hex(x).ok()).and_then(|x| x.to_u64()).unwrap_or(0);
                        if bk.eq_ignore_ascii_case(&khex) {
                            sealed += 1;
                            if round == 0 { sealed_round0 += 1 }
                            *rounds.entry(round).or_insert(0) += 1;
                            heights_sealed.push(h + i as u64);
                        }
                    }
                },
            }
            h = to + 1;
            std::thread::sleep(Duration::from_millis(1100));
        }
        let total = U256::from_hex(s.get("total").and_then(|v| v.as_str()).unwrap_or("0x0")).unwrap_or(U256::ZERO);
        let share = draw::share(&amount, &total, expected, epoch_blocks);
        let predicted = share.round0_chance * w as f64;
        // the endorsement lot (the spec's 1.4): the chance a unit of this key is asked to endorse at a height
        let endorse_expected = params.get("EndorseExpected").and_then(|v| v.as_u64()).unwrap_or(32) as f64;
        let pe = 1.0 - (1.0 - endorse_expected / 65536.0).powi(share.units as i32);
        // the observed column's recompute (the spec's eligibleRound0): for the heights of the CURRENT epoch in the
        // window, this key's eligible round from the epoch's seed and its units in the epoch's table (the bond and
        // the registry's balance at the snapshot block), the same arithmetic as the draw card's; earlier epochs'
        // seeds are not served, so their heights stay uncounted and the words say how many
        let p = draw::Params {
            expected,
            max_round: params.get("MaxRound").and_then(|v| v.as_u64()).unwrap_or(16),
            unbonded_round: params.get("UnbondedRound").and_then(|v| v.as_u64()).unwrap_or(17),
            delta_secs: params.get("Delta").and_then(|v| v.as_u64()).unwrap_or(20),
        };
        let seed = authority::parse_key(s.get("seed").and_then(|v| v.as_str()).unwrap_or("0x0")).unwrap_or([0u8; 32]);
        let snapshot = s.get("snapshotBlock").and_then(|v| v.as_u64()).unwrap_or(0);
        let snap_tag = format!("0x{:x}", snapshot);
        let table_live = s.get("tableLive").and_then(|v| v.as_bool()).unwrap_or(false);
        // a live table (devnet9: tableLive, snapshotBlock 0) draws on the latest bonds; a snapshotted one on the
        // bonds at the snapshot block
        let units_snap = if table_live || snapshot == 0 {
            share.units
        } else {
            let b = rpc.igneum_bond_of(&key_bytes, &snap_tag).ok();
            let a = b.as_ref().and_then(|b| b.get("amount")).and_then(|v| v.as_str()).and_then(|h| U256::from_hex(h).ok()).unwrap_or(U256::ZERO);
            let tot = rpc.call("eth_getBalance", json!([reg, snap_tag])).ok().and_then(|v| v.as_str().and_then(|h| U256::from_hex(h).ok())).unwrap_or(U256::ZERO);
            draw::units(&a, &tot)
        };
        let epoch_first = (head / epoch_blocks) * epoch_blocks + 1;
        let first = (head.saturating_sub(w) + 1).max(epoch_first);
        let mut eligible_round0 = 0u64;
        let mut eligible_heights: Vec<u64> = Vec::new();
        let recomputed = if first <= head { head - first + 1 } else { 0 };
        if units_snap > 0 && recomputed > 0 {
            for hh in first..=head {
                if draw::eligible_round(&p, &seed, &key_bytes, units_snap, hh) == 0 {
                    eligible_round0 += 1;
                    eligible_heights.push(hh);
                }
            }
        }
        // the eligible heights this key did not seal (any round): drawn and not sealed
        let drawn_not_sealed = eligible_heights.iter().filter(|hh| !heights_sealed.contains(hh)).count() as u64;
        let interval = (w as f64 * share.round0_chance * (1.0 - share.round0_chance)).sqrt();
        let to_exit = earliest.map(|e| e.saturating_sub(head));
        // the exit address the registry holds for this key (participant 3, 12 October 2026: the Desk did not print
        // where the bond goes) and the clock time the lock ends at the measured pace (its "when to come back")
        let exit_addr = rpc
            .eth_call(&json!({"to": reg, "data": hex::encode(&authority::bond_data(&key_bytes))}))
            .ok()
            .and_then(|out| authority::decode_read(&out).ok())
            .and_then(|r| r.exit)
            .map(|e| e.to_checksum());
        let exit_s = exit_addr.clone().unwrap_or_else(|| "the bond's account".to_string());
        let exit_eta_unix = to_exit.map(|n| now_unix() + (n as f64 * cadence) as u64);
        let words_exit = match (earliest, to_exit) {
            // a key with no bond: released (its post height stands in the registry) or never bonded; no exit height,
            // no pace, no placeholder (the project's 0.0.6 order on participants 6 and 7)
            _ if amount.is_zero() && posted > 0 => format!("Released: the bond left this key at or after height {}; not bonded now, and the key never bonds again", posted + lock_blocks.unwrap_or(0)),
            _ if amount.is_zero() => "Never bonded: nothing is locked and nothing is released".to_string(),
            (Some(e), Some(0)) => format!("the lock has passed (earliest exit height {e}); a release moves the whole bond of {} IGN to the exit address {exit_s}", display::ign(&amount, 4)),
            (Some(e), Some(n)) => format!("nothing is released before height {e}: {n} blocks, about {} at the chain's pace over its last 100 blocks ({:.0} s a block); come back then and the Desk offers the release (this wallet locks after {} minutes idle; unlock when you come back); the bond of {} IGN goes to the exit address {exit_s}", Self::span_words(n as f64 * cadence), cadence, self.settings.idle_lock_min, display::ign(&amount, 4)),
            _ => "the chain's lock in blocks is not served by this node; the earliest exit is the post height plus the genesis's lockBlocks".into(),
        };
        Ok(json!({
            "chain": self.chain.name, "registry": reg, "registry_version": version, "head": head, "read_unix": now_unix(),
            "key": khex, "locked_ign": display::ign(&amount, 6), "locked_wei": amount.to_dec_string(), "account": bond.get("account"), "signer": bond.get("signer"), "signer_words": if bond.get("signer").and_then(|x| x.as_str()).map(|x| x.eq_ignore_ascii_case(&khex)).unwrap_or(false) { "the identity key signs its own blocks (no separate signing key set)" } else { "a separate signing key is set" }, "bonded": bond.get("bonded"),
            "units": share.units, "fraction_percent": format!("{:.4}", share.share * 100.0), "table_total_ign": display::ign(&total, 2), "keys_in_table": keys_total,
            "posted_height": posted, "lock_blocks": lock_blocks, "earliest_exit_height": earliest, "blocks_to_exit": to_exit, "exit_words": words_exit,
            "cadence_secs": cadence, "cadence_window_blocks": span as u64,
            // N-1 (AUTHORITY-DESK-SPEC.md 0.3): a head older than six times the measured cadence is stale from this
            // node; the page shows the staleness and no draw figure, the bond's amount stays with its height
            "head_age_secs": head_age, "stale": stale,
            "stale_words": if stale { json!(format!("the chain is stale from this node: read at head {}, {:.0} s ago (more than six times the pace of {:.0} s a block); no draw figure is shown", Self::commas(head), head_age, cadence)) } else { Value::Null },
            "window_blocks": w, "predicted_round0": predicted, "predicted_interval": interval,
            "endorse_chance": pe, "endorse_words": format!("asked to endorse at about {:.1} percent of heights (a unit of this key among the {} expected endorsers)", pe * 100.0, endorse_expected),
            "release_open": earliest.map(|e| head >= e).unwrap_or(false),
            "observed": {"sealed": sealed, "sealed_round0": sealed_round0, "rounds": rounds, "heights": heights_sealed, "read_errors": read_errors,
                          "eligible_round0": eligible_round0, "eligible_heights": eligible_heights, "drawn_not_sealed": drawn_not_sealed,
                          "recomputed_heights": recomputed, "recomputed_from": first, "units_at_snapshot": units_snap, "snapshot_block": snapshot, "table_live": table_live,
                          "note": format!("the eligibility recompute covers the current epoch's {} heights of the window (from {}) with the epoch's seed and this key's {} units at the snapshot block {}; earlier epochs' seeds are not served, so {} heights are counted by the headers alone", recomputed, first, units_snap, snapshot, w.saturating_sub(recomputed))},
            "predicted_words": format!("predicted at round 0 over {} blocks: {:.1} (give or take {:.1})", w, predicted, interval),
            "observed_words": format!("eligible at round 0 over the epoch's {} heights: {}; sealed over {} blocks: {} (of which at round 0: {}); drawn and not sealed: {}; a key seals at later rounds too", recomputed, eligible_round0, w, sealed, sealed_round0, drawn_not_sealed),
            "penalties": {
                "version": version,
                "table": [
                    {"action": "a seal equivocation (two sealed headers at one height under this key)", "loss": "the whole bond: one block's reward of it to the reporter, the rest burnt; the key marked, never bonds again", "this_key": if marked { "MARKED" } else { "not marked" }},
                    {"action": "an endorsement equivocation (one unit endorsing two hashes at one height)", "loss": "the same", "this_key": if marked { "MARKED" } else { "not marked" }},
                    {"action": "a hostile claim (the bond secret used to claim to any address but the exit)", "loss": "the whole bond leaves at once: a quarter burnt, three quarters to the claimant; the key removed", "this_key": format!("accepted claims on this key: {claims}")},
                    {"action": "a missed draw, an offline sealer, a late seal", "loss": "nothing (no inactivity penalty in the rule)", "this_key": ""},
                    {"action": "the lock", "loss": "nothing lost; nothing released before the post height plus the lock", "this_key": earliest.map(|e| format!("earliest exit height {e}")).unwrap_or_default()}
                ],
                "max_loss_words": format!("the most you can lose is all of it: {} IGN", display::ign(&amount, 6)),
                "burnt_to_date_ign": display::ign(&burnt, 6), "marked": marked, "claims": claims,
                "enforced": version >= 4,
            },
            "source": "igneum_sortition, igneum_authority, igneum_bondOf and the registry's slots at latest; the headers of the window by eth_getBlockByNumber in batches; nothing from an indexer",
        }))
    }

    /// A whole number with its thousands grouped (the page's withCommas, for sentences built here).
    fn commas(n: u64) -> String {
        let d = n.to_string();
        let mut out = String::new();
        for (i, c) in d.chars().enumerate() {
            if i > 0 && (d.len() - i) % 3 == 0 {
                out.push(',');
            }
            out.push(c);
        }
        out
    }

    /// "1 unit", "2 units", "16,378 units" (participant 3's "1 units", 12 October 2026).
    fn units_words(n: u64) -> String {
        if n == 1 { "1 unit".to_string() } else { format!("{} units", Self::commas(n)) }
    }

    fn span_words(secs: f64) -> String {
        if secs < 90.0 { format!("{:.0} s", secs) } else if secs < 5400.0 { format!("{:.0} minutes", secs / 60.0) } else if secs < 172_800.0 { format!("{:.1} hours", secs / 3600.0) } else { format!("{:.1} days", secs / 86400.0) }
    }

    /// The Desk's one tap for a stranger (the project's approval, 12 October 2026): a bond's worth of development-chain
    /// IGN for this wallet's address and the bond key named, through the chain's faucet relay (POST /bond); the
    /// relay pays the gas, the contract's bounds hold (once per key, once per address, twenty keys a day). The
    /// engine asks on the person's behalf so the page needs no reach beyond the engine; nothing is signed here.
    fn bond_faucet(&self, key_hash: &str) -> Result<Value> {
        use igneum_wallet_core::authority;
        let relay = self.chain.faucet_relay.clone().ok_or_else(|| Error::Refused(format!("{} has no faucet relay the wallet knows", self.chain.name)))?;
        let contract = self.chain.bond_faucet.clone().ok_or_else(|| Error::Refused(format!("{} has no bond faucet the wallet knows", self.chain.name)))?;
        let from = self.address().ok_or_else(|| Error::Refused("no wallet".into()))?;
        let key = authority::parse_key(key_hash)?;
        let url = format!("{}/bond", relay.trim_end_matches('/'));
        let body = json!({"address": from.to_checksum(), "key_hash": hex::encode(&key[..])});
        let resp = ureq::AgentBuilder::new().timeout(Duration::from_secs(90)).build().post(&url).set("Content-Type", "application/json").send_json(body);
        let v: Value = match resp {
            Ok(r) => r.into_json().map_err(|e| Error::Rpc(format!("the faucet relay answered badly: {e}")))?,
            Err(ureq::Error::Status(code, r)) => {
                let v: Value = r.into_json().unwrap_or(json!({}));
                let why = v.get("error").and_then(|e| e.as_str()).unwrap_or("refused").to_string();
                return Err(Error::Refused(format!("the bond faucet refused ({code}): {why}")));
            }
            Err(e) => return Err(Error::Rpc(format!("the faucet relay is not reachable: {e}"))),
        };
        Ok(json!({"hash": v.get("hash"), "amount_ign": "87", "contract": contract, "key": hex::encode(&key[..]), "note": v.get("note"), "label": "development-network tokens; no monetary value"}))
    }

    /// "997,170,614.3367" from the engine's plain decimal string.
    fn commas_ign(s: &str) -> String {
        let (whole, frac) = match s.find('.') { Some(i) => (&s[..i], &s[i..]), None => (s, "") };
        let w: u64 = whole.parse().unwrap_or(0);
        if whole.len() > 18 { return s.to_string(); }
        format!("{}{}", Self::commas(w), frac)
    }

    fn times_words(per_day: f64) -> String {
        // the whole phrase: "2 times a day", "once every 15 days", "never"
        if per_day >= 1.0 {
            format!("{} times a day", Self::commas(per_day.round() as u64))
        } else if per_day > 0.0 {
            let days = 1.0 / per_day;
            if days < 1.5 { "about once a day".into() } else { format!("once every {} days", Self::commas(days.round() as u64)) }
        } else {
            "never".into()
        }
    }

    fn send(&mut self, quote_id: &str, nonce: Option<&str>) -> Result<Value> {
        if self.watch.is_some() {
            return Err(Error::Refused("this wallet is watch-only: export the envelope and sign it where the key is".into()));
        }
        // the quote stays in the table until the gate has passed: a refused send (no nonce, not confirmed, another
        // quote's nonce) leaves it for the person to confirm again without a new review (the first gate smoke found
        // the quote gone after one refusal)
        let (expired, bound) = {
            let q = self.quotes.get(quote_id).ok_or_else(|| Error::Refused("the quote expired; review again".into()))?;
            let tx = &q.envelope.transaction;
            let to = tx.to().map(|a| a.to_checksum()).unwrap_or_default();
            (q.created.elapsed() > Duration::from_secs(180), biometric::send_binding(&to, &tx.value().to_dec_string(), tx.nonce(), tx.chain_id()))
        };
        if expired {
            self.quotes.remove(quote_id);
            return Err(Error::Refused("the quote expired; review again".into()));
        }
        // with the prompt enrolled, a send needs the host-confirmed nonce bound to this exact quote (to, value,
        // transaction nonce, chain id), fresh and unused; without it nothing is signed
        if self.bio.enrolled {
            let n = nonce.filter(|n| !n.is_empty()).ok_or_else(|| Error::Refused(format!("confirm with {} first", self.what())))?;
            self.take_nonce(n, "send", &bound)?;
        }
        let q = self.quotes.remove(quote_id).ok_or_else(|| Error::Refused("the quote expired; review again".into()))?;
        let signed = self.signer.request("sign_envelope", json!({"envelope": q.envelope}))?;
        let signed: SignedEnvelope = serde_json::from_value(signed)?;
        signed.check_against(&q.envelope)?;
        let hash = self.submit_rpc().send_raw(&signed.raw)?;
        let hash_hex = hex::encode(&hash);
        self.sent.push(Sent {
            hash: hash_hex.clone(),
            to: q.envelope.transaction.to().unwrap_or_default(),
            value: q.envelope.transaction.value(),
            sent_unix: now_unix(),
            sentences: q.envelope.sentences.clone(),
            moves: q.moves,
        });
        let _ = self.save_history();
        Ok(json!({"hash": hash_hex}))
    }

    fn envelope_export(&mut self, quote_id: &str) -> Result<Value> {
        let q = self.quotes.get(quote_id).ok_or_else(|| Error::Refused("the quote expired; review again".into()))?;
        Ok(serde_json::to_value(&q.envelope)?)
    }

    fn envelope_import(&mut self, signed: Value) -> Result<Value> {
        let signed: SignedEnvelope = serde_json::from_value(signed)?;
        // find the unsigned envelope it answers among the open quotes
        let (id, q) = self
            .quotes
            .iter()
            .find(|(_, q)| signed.check_against(&q.envelope).is_ok())
            .map(|(id, q)| (id.clone(), q.envelope.clone()))
            .ok_or_else(|| Error::Refused("this signed envelope answers no open quote of this wallet".into()))?;
        let hash = self.submit_rpc().send_raw(&signed.raw)?;
        self.quotes.remove(&id);
        let hash_hex = hex::encode(&hash);
        self.sent.push(Sent {
            hash: hash_hex.clone(),
            to: q.transaction.to().unwrap_or_default(),
            value: q.transaction.value(),
            sent_unix: now_unix(),
            sentences: q.sentences.clone(),
            moves: None,
        });
        let _ = self.save_history();
        Ok(json!({"hash": hash_hex}))
    }

    fn import(&mut self, mode: &str, data: &str, password: &str) -> Result<Value> {
        if self.phase() != "welcome" {
            return Err(Error::Refused("a wallet already exists on this machine".into()));
        }
        match mode {
            "watch" => {
                let address = Address::parse(data)?;
                let w = WatchFile { watch_only: true, address, created_unix: now_unix() };
                fs::write(self.watch_path(), serde_json::to_vec_pretty(&w)?)?;
                self.watch = Some(w);
                Ok(json!({"address": address}))
            }
            "seed" => {
                let r = self.signer.request("import_phrase", json!({"phrase": data, "password": password, "chain_id": self.chain.chain_id}))?;
                self.refresh_signer_status()?;
                Ok(r)
            }
            "key" => {
                let r = self.signer.request("import_key", json!({"key": data, "password": password, "chain_id": self.chain.chain_id}))?;
                self.refresh_signer_status()?;
                Ok(r)
            }
            other => Err(Error::Input(format!("unknown import mode {other}"))),
        }
    }
}

fn field<'a>(v: &'a Value, k: &str) -> Result<&'a str> {
    v.get(k).and_then(|x| x.as_str()).ok_or_else(|| Error::Input(format!("missing {k}")))
}

/// Zeroizes every string in a JSON value (the request bodies that carry a password, a phrase or a key, and the
/// replies that carry the words or the key) once its use is over, so no copy outlives the turn in this process
/// (wallet-security's MEM-3 and the security review's A-MEM2, 12 October 2026).
fn scrub(v: &mut Value) {
    use zeroize::Zeroize;
    match v {
        Value::String(s) => s.zeroize(),
        Value::Array(a) => a.iter_mut().for_each(scrub),
        Value::Object(o) => o.values_mut().for_each(scrub),
        _ => {}
    }
}

fn route(engine: &Arc<Mutex<Engine>>, req: Request, rest: &str) -> Result<Value> {
    let mut body: Value = if req.body.is_empty() { json!({}) } else { serde_json::from_slice(&req.body)? };
    // the password's one copy: taken out of the parsed body into a zeroizing string before any handler runs and
    // put back as a borrowed field only for the turn (wallet-security's MEM-3, the parsed field's own buffer)
    let pw: Option<zeroize::Zeroizing<String>> = match body.get_mut("password") {
        Some(Value::String(s)) => Some(zeroize::Zeroizing::new(std::mem::take(s))),
        _ => None,
    };
    let out = route_body(engine, &req, rest, &body, pw.as_deref().map(|s| s.as_str()));
    scrub(&mut body);
    drop(pw);
    out
}

fn route_body(engine: &Arc<Mutex<Engine>>, req: &Request, rest: &str, body: &Value, pw: Option<&str>) -> Result<Value> {
    let mut e = engine.lock().map_err(|_| Error::Refused("engine lock".into()))?;
    // the idle lock counts the person's acts, not the page's reads (ST-3, 12 October 2026: the page polls
    // api/state every two seconds, so a read that counted kept an open window unlocked for ever): every POST is
    // the person's act (a create, a send, a decision, a setting) and marks activity; a GET never does;
    // api/activity is the page's word for the person's hand (mouse, keys, touch), sent at most every twenty seconds
    if req.method == "POST" {
        e.last_activity = Instant::now();
    }
    match (req.method.as_str(), rest) {
        ("GET", "api/state") => Ok(e.state_json()),
        ("POST", "api/create") => {
            if e.phase() != "welcome" {
                return Err(Error::Refused("a wallet already exists on this machine".into()));
            }
            let chain_id = e.chain.chain_id;
            let password = pw.filter(|p| !p.is_empty()).ok_or_else(|| Error::Input("missing password".into()))?;
            e.check_password(password)?;
            let r = e.signer.request("create", json!({"password": password, "chain_id": chain_id}))?;
            e.refresh_signer_status()?;
            Ok(r)
        }
        ("POST", "api/password/check") => Ok(e.password_reading(pw.unwrap_or(""))),
        ("POST", "api/create/confirm") => {
            let r = e.signer.request("confirm_words", json!({"checks": body.get("checks").cloned().unwrap_or(json!([]))}))?;
            e.refresh_signer_status()?;
            Ok(r)
        }
        ("POST", "api/import") => {
            let mode = field(&body, "mode")?.to_string();
            let data = field(&body, "data")?.to_string();
            let password = pw.unwrap_or("").to_string();
            if mode != "watch" {
                e.check_password(&password)?;
            }
            e.import(&mode, &data, &password)
        }
        ("POST", "api/unlock") => {
            let password = pw.filter(|p| !p.is_empty()).ok_or_else(|| Error::Input("missing password".into()))?;
            let mut r = e.signer.request("unlock", json!({"password": password}))?;
            e.refresh_signer_status()?;
            // F-W1 (15 October 2026): a sealed password the host holds that no longer opens the vault (the last host
            // unlock refused by the engine) is re-sealed from this password: it is parked under a one-time token and
            // the reply says so; the host or the page runs the enrolment's seal step once, with its prompt
            if e.bio.enrolled && e.bio.last_op == "unlock" && e.bio.last_result == "engine" {
                let token = e.gate.enrol_begin(password, Instant::now());
                e.bio.last_result = "reseal".into();
                if let Some(o) = r.as_object_mut() { o.insert("reseal".into(), json!(true)); o.insert("token".into(), json!(token)); }
            }
            Ok(r)
        }
        ("POST", "api/lock") => {
            let r = e.signer.request("lock", json!({}))?;
            e.quotes.clear();
            e.refresh_signer_status()?;
            Ok(r)
        }
        ("POST", "api/faucet") => e.quote_faucet(),
        ("POST", "api/history/chain") => e.chain_history(),
        // the mining identity made in the window (the signer derives it from the wallet's key; nothing new is stored)
        ("POST", "api/identity") => {
            if e.watch.is_some() {
                return Err(Error::Refused("a watch-only wallet has no key to make an identity from".into()));
            }
            let r = e.signer.request("identity", json!({}))?;
            let key = r.get("key").and_then(|k| k.as_str()).unwrap_or("").to_string();
            e.identity = Some(key.clone());
            e.last_activity = Instant::now();
            Ok(json!({"key": key}))
        }
        ("POST", "api/ledger") => e.ledger(field(&body, "view")?),
        ("POST", "api/payments") => e.payments(body.get("since").and_then(|n| n.as_u64())),
        // the Home's twenty-second read of the block payouts is a read, not the person's act: by GET with the head in
        // the query, so it never marks activity (a POST every twenty seconds kept the updater's idle apply from ever
        // running; one Windows host's engine downloaded 0.0.13 at 13:57Z on 15 October 2026 and never applied it)
        ("GET", "api/payments") => {
            let since = req.path.split('?').nth(1).and_then(|q| q.split('&').find_map(|kv| kv.strip_prefix("since="))).and_then(|v| v.parse::<u64>().ok());
            e.payments(since)
        }
        ("GET", "api/shares") => e.shares_feed(),
        ("POST", "api/bond-faucet") => e.bond_faucet(field(&body, "key_hash")?),
        // a contract call reviewed in the wallet's words (the Pay demo, the kit's examples): to, the IGN amount
        // and the call data; the same quote and send path as a plain send, the sentences from core::display
        ("POST", "api/call/quote") => {
            let to = Address::parse(field(&body, "to")?)?;
            let value = display::parse_ign(body.get("amount").and_then(|a| a.as_str()).unwrap_or("0"))?;
            let data = hex::decode(body.get("data").and_then(|d| d.as_str()).unwrap_or("0x"))?;
            e.quote_call(to, value, data)
        }
        ("POST", "api/authority/desk") => {
            let key = body.get("key").and_then(|k| k.as_str()).map(|k| k.to_string());
            e.desk(key.as_deref())
        }
        ("POST", "api/holder") => {
            let amount = body.get("amount").and_then(|a| a.as_str()).map(|a| a.to_string());
            e.holder(amount.as_deref())
        }
        ("POST", "api/send/quote") => {
            let to = field(&body, "to")?.to_string();
            let amount = field(&body, "amount")?.to_string();
            e.quote(&to, &amount)
        }
        ("POST", "api/send") => {
            let id = field(&body, "quote_id")?.to_string();
            let nonce = body.get("nonce").and_then(|n| n.as_str()).map(|n| n.to_string());
            e.send(&id, nonce.as_deref())
        }
        // ---- Touch ID and Windows Hello: the page's side, and the host's side behind its token ----
        ("POST", "api/biometric/challenge") => {
            let purpose = body.get("purpose").and_then(|p| p.as_str()).unwrap_or("").to_string();
            e.challenge(&purpose)
        }
        ("POST", "api/biometric/enrol/begin") => {
            let password = pw.filter(|p| !p.is_empty()).ok_or_else(|| Error::Input("missing password".into()))?.to_string();
            e.enrol_begin(&password)
        }
        ("POST", "api/biometric/enrol/cancel") => {
            e.gate.enrol_cancel();
            Ok(json!({"ok": true}))
        }
        ("POST", "api/biometric/remove") => Ok(e.biometric_remove()),
        // ---- updates: a check on request; the downloaded package opened with the platform's opener ----
        ("POST", "api/update/check") => {
            e.update.check_asked = true;
            Ok(json!({"ok": true, "update": e.update.state}))
        }
        ("POST", "api/update/download") => {
            // the page asked for the download with automatic updates off: one download, the setting unchanged
            if e.update.state.status == "available" {
                e.update.state.auto = true;
            }
            Ok(json!({"ok": true, "update": e.update.state}))
        }
        ("POST", "api/update/open") => {
            let file = e.update.state.file.clone();
            if file.is_empty() || !PathBuf::from(&file).exists() {
                return Err(Error::Refused("no downloaded update to open".into()));
            }
            let r = if cfg!(target_os = "macos") {
                std::process::Command::new("open").arg("-R").arg(&file).spawn()
            } else if cfg!(target_os = "windows") {
                std::process::Command::new("explorer").arg(format!("/select,{file}")).spawn()
            } else {
                std::process::Command::new("xdg-open").arg(PathBuf::from(&file).parent().map(|p| p.to_path_buf()).unwrap_or_default()).spawn()
            };
            r.map_err(|err| Error::Refused(format!("could not open the download's folder: {err}")))?;
            Ok(json!({"ok": true, "file": file}))
        }
        ("GET", "api/biometric/challenge") => {
            // the host reads what the prompt must say: ?nonce=<hex>, with its token
            if !e.host_ok(req.header("x-igneum-host")) {
                return Err(Error::Refused("the host's token is needed".into()));
            }
            let nonce = req.path.split("nonce=").nth(1).unwrap_or("").split('&').next().unwrap_or("").to_string();
            e.challenge_reason(&nonce)
        }
        ("POST", "api/biometric/status") | ("POST", "api/biometric/report") | ("POST", "api/biometric/confirm") | ("POST", "api/biometric/enrol/take") | ("POST", "api/biometric/enrolled") => {
            if !e.host_ok(req.header("x-igneum-host")) {
                return Err(Error::Refused("the host's token is needed".into()));
            }
            let s = |k: &str| body.get(k).and_then(|v| v.as_str()).unwrap_or("").to_string();
            match rest {
                "api/biometric/status" => Ok(e.biometric_status(body.get("available").and_then(|b| b.as_bool()).unwrap_or(false), &s("kind"), &s("message"))),
                "api/biometric/report" => Ok(e.biometric_report(&s("op"), body.get("ok").and_then(|b| b.as_bool()).unwrap_or(false), &s("code"), &s("message"))),
                "api/biometric/confirm" => e.confirm(&s("nonce")),
                "api/biometric/enrol/take" => e.enrol_take(&s("token")),
                _ => e.enrolled_set(&s("kind")),
            }
        }
        ("POST", "api/authority/quote") => {
            let action = field(&body, "action")?.to_string();
            e.quote_authority(&action, &body)
        }
        ("POST", "api/authority/draw") => {
            let key = body.get("key").and_then(|k| k.as_str()).filter(|k| !k.is_empty()).map(|k| k.to_string());
            let at = body.get("at").and_then(|a| a.as_u64());
            e.draw(key.as_deref(), at)
        }
        ("POST", "api/receipts/verify") => {
            use igneum_wallet_authority::receipts;
            let lines = field(&body, "lines")?;
            let rs = receipts::read_lines(lines)?;
            let report = receipts::verify(&rs, field(&body, "pool_key")?, field(&body, "member_key")?)?;
            // the per-line sha256 over the raw JSON line without its newline (the settlement team's receipt_sha,
            // 12 October 2026), in the lines' order, blank lines skipped as read_lines skips them
            let line_sha256: Vec<String> = lines
                .lines()
                .filter(|l| !l.trim().is_empty())
                .map(|l| {
                    use sha2::Digest;
                    hex::encode(sha2::Sha256::digest(l.trim_end_matches('\r').as_bytes()).as_slice())
                })
                .collect();
            let mut v = serde_json::to_value(report)?;
            v["line_sha256"] = json!(line_sha256);
            Ok(v)
        }
        ("POST", "api/envelope/export") => {
            let id = field(&body, "quote_id")?.to_string();
            e.envelope_export(&id)
        }
        ("POST", "api/envelope/import") => {
            let signed = body.get("signed").cloned().ok_or_else(|| Error::Input("missing signed".into()))?;
            let signed = if let Some(s) = signed.as_str() { serde_json::from_str(s)? } else { signed };
            e.envelope_import(signed)
        }
        ("POST", "api/backed-up") => {
            let r = e.signer.request("set_backed_up", json!({}))?;
            e.refresh_signer_status()?;
            Ok(r)
        }
        ("POST", "api/reveal") => {
            if e.watch.is_some() {
                return Err(Error::Refused("a watch-only wallet has no words and no key here".into()));
            }
            // the biometric path: a host-confirmed reveal challenge instead of the password (the words from the
            // key already unlocked in the signer); the password path stays for everyone
            if let Some(n) = body.get("nonce").and_then(|n| n.as_str()).filter(|n| !n.is_empty()) {
                e.take_nonce(n, "reveal", "")?;
                return e.signer.request("reveal_unlocked", json!({"host_confirmed": true}));
            }
            e.signer.request("reveal", json!({"password": pw.filter(|p| !p.is_empty()).ok_or_else(|| Error::Input("missing password".into()))?}))
        }
        ("GET", "api/settings") => Ok(serde_json::to_value(&e.settings)?),
        ("POST", "api/settings") => {
            if let Some(r) = body.get("rpc").and_then(|r| r.as_str()) {
                // a node named on the Settings page is used as it is; an empty field returns to the chain's
                // public endpoint and the source order (a local node first when it serves the chain)
                if r.trim().is_empty() {
                    e.settings.rpc = igneum_wallet_core::chain::PUBLIC_7322_RPC.to_string();
                    e.settings.rpc_named = false;
                } else {
                    e.settings.rpc = r.trim().to_string();
                    e.settings.rpc_named = true;
                }
                e.choose_source();
            }
            if let Some(on) = body.get("ask_on_open").and_then(|b| b.as_bool()) {
                e.settings.ask_on_open = on;
            }
            if let Some(on) = body.get("auto_update").and_then(|b| b.as_bool()) {
                e.settings.auto_update = on;
                e.update.state.auto = on;
            }
            if let Some(m) = body.get("idle_lock_min").and_then(|m| m.as_u64()) {
                e.settings.idle_lock_min = m;
            }
            if let Some(t) = body.get("theme").and_then(|t| t.as_str()) {
                e.settings.theme = t.to_string();
                e.settings.theme_chosen = true;
            }
            e.save_settings()?;
            Ok(serde_json::to_value(&e.settings)?)
        }
        ("POST", "api/remove") => {
            if e.watch.is_some() {
                let p = e.watch_path();
                if p.exists() {
                    fs::remove_file(p)?;
                }
                e.watch = None;
                return Ok(json!({"removed": true}));
            }
            let r = e.signer.request("remove", json!({"password": pw.filter(|p| !p.is_empty()).ok_or_else(|| Error::Input("missing password".into()))?}))?;
            // the sealed password of a removed wallet is stale: the biometric file goes with the vault, whether or not
            // the host has reported an enrolment this session (F-W1, 15 October 2026)
            e.biometric_stale();
            let _ = fs::remove_file(e.biometric_path());
            e.bio.enrolled = false;
            e.bio.needs_enrol = false;
            e.refresh_signer_status()?;
            Ok(r)
        }
        ("POST", "api/activity") => Ok(json!({"ok": true})),
        ("GET", "api/bridge/pending") => Ok(json!(e.asks.pending())),
        ("POST", "api/bridge/decide") => {
            let id = body.get("id").and_then(|i| i.as_u64()).ok_or_else(|| Error::Input("missing id".into()))?;
            let yes = body.get("yes").and_then(|y| y.as_bool()).unwrap_or(false);
            Ok(json!({"answered": e.asks.decide(id, yes)}))
        }
        ("POST", "api/bridge/on") => {
            e.settings.bridge_on = body.get("on").and_then(|o| o.as_bool()).unwrap_or(true);
            e.save_settings()?;
            Ok(json!({"on": e.settings.bridge_on}))
        }
        ("POST", "api/bridge/sites/remove") => {
            let origin = field(&body, "origin")?.to_string();
            e.sites.retain(|s| s.origin != origin);
            e.save_sites()?;
            Ok(json!({"sites": e.sites}))
        }
        ("POST", "api/quit") => {
            let _ = e.signer.request("lock", json!({}));
            std::thread::spawn(|| {
                std::thread::sleep(Duration::from_millis(200));
                std::process::exit(0);
            });
            Ok(json!({"ok": true}))
        }
        ("GET", p) if p.starts_with("api/tx/") => e.tx_view(&p["api/tx/".len()..]),
        _ => Err(Error::Input("not in this build".into())),
    }
}

fn serve_static(ui_dir: &Option<PathBuf>, rest: &str) -> Response {
    let dir = match ui_dir {
        Some(d) => d,
        None => return Response::not_found(),
    };
    let name = if rest.is_empty() || rest == "/" { "index.html" } else { rest };
    if name.contains("..") || name.starts_with('/') {
        return Response::not_found();
    }
    let path = dir.join(name);
    match fs::read(&path) {
        Ok(bytes) => Response::file(http::content_type_for(name), bytes),
        Err(_) => Response::not_found(),
    }
}

/// One website request on the bridge. The engine lock is held only around state reads and the signing step,
/// never while the person is being asked.
fn bridge_request(engine: &Arc<Mutex<Engine>>, origin: &str, site_token: Option<&str>, browser: bool, minted: &mut Option<String>, req: &Value) -> Value {
    let id = req.get("id").cloned().unwrap_or(Value::Null);
    let method = req.get("method").and_then(|m| m.as_str()).unwrap_or("").to_string();
    let params = req.get("params").cloned().unwrap_or(json!([]));
    if let Some(why) = bridge_words::refused(&method) {
        return bridge::rpc_error(&id, 4200, why);
    }
    // the state this request needs, read under the lock and released
    let (on, known, from, chain_id, rpc_url, asks, unlocked) = match engine.lock() {
        Ok(mut e) => {
            e.touch_site(origin);
            (e.settings.bridge_on, e.site_known(origin, site_token), e.address(), e.chain.chain_id, e.settings.rpc.clone(), e.asks.clone(), e.unlocked())
        }
        Err(_) => return bridge::rpc_error(&id, -32603, "engine busy"),
    };
    if !on {
        return bridge::rpc_error(&id, 4100, "websites are not allowed to connect (Settings in Igneum Wallet)");
    }
    match method.as_str() {
        "eth_chainId" => return bridge::rpc_result(&id, json!(U256::from_u64(chain_id).to_hex())),
        "net_version" => return bridge::rpc_result(&id, json!(chain_id.to_string())),
        "eth_accounts" => return bridge::rpc_result(&id, if known { bridge::address_list(from) } else { json!([]) }),
        "wallet_switchEthereumChain" | "wallet_addEthereumChain" => {
            let want = params.get(0).and_then(|p| p.get("chainId")).and_then(|c| c.as_str()).and_then(|c| U256::from_hex(c).ok()).and_then(|c| c.to_u64());
            return if want == Some(chain_id) { bridge::rpc_result(&id, Value::Null) } else { bridge::rpc_error(&id, 4902, "this wallet is on one network; others are not added from a website") };
        }
        _ => {}
    }
    if bridge_words::is_read(&method) {
        let rpc = Rpc::new(&rpc_url);
        return match rpc.call(&method, params) {
            Ok(v) => bridge::rpc_result(&id, v),
            Err(e) => bridge::rpc_error(&id, -32000, &e.to_string()),
        };
    }
    let from = match from {
        Some(a) => a,
        None => return bridge::rpc_error(&id, 4100, "no wallet on this machine yet"),
    };
    match method.as_str() {
        "eth_requestAccounts" => {
            if known {
                return bridge::rpc_result(&id, bridge::address_list(Some(from)));
            }
            match asks.ask(origin, if browser { Ask::Connect } else { Ask::ConnectLocal }) {
                Err(r) => bridge::rpc_error(&id, bridge::ASK_REFUSED_CODE, r.reason),
                Ok(Some(true)) => {
                    if let Ok(mut e) = engine.lock() {
                        *minted = Some(e.connect_site(origin));
                    }
                    bridge::rpc_result(&id, bridge::address_list(Some(from)))
                }
                Ok(Some(false)) => bridge::rpc_error(&id, 4001, "the person declined"),
                Ok(None) => bridge::rpc_error(&id, 4001, "no answer in the wallet window"),
            }
        }
        _ if !known => bridge::rpc_error(&id, 4100, "connect first (eth_requestAccounts)"),
        "eth_sendTransaction" => {
            let p = match bridge_words::parse_send_params(&params, &from) {
                Ok(p) => p,
                Err(e) => return bridge::rpc_error(&id, -32602, &e.to_string()),
            };
            let env = match engine.lock().map_err(|_| Error::Refused("engine busy".into())).and_then(|e| e.compose(from, &p)) {
                Ok(env) => env,
                Err(e) => return bridge::rpc_error(&id, -32000, &e.to_string()),
            };
            match asks.ask(origin, Ask::SendTransaction { sentences: env.sentences.clone() }) {
                Err(r) => bridge::rpc_error(&id, bridge::ASK_REFUSED_CODE, r.reason),
                Ok(Some(true)) => {
                    let mut e = match engine.lock() {
                        Ok(e) => e,
                        Err(_) => return bridge::rpc_error(&id, -32603, "engine busy"),
                    };
                    if !e.unlocked() || e.watch.is_some() {
                        return bridge::rpc_error(&id, 4100, "the wallet is locked or watch-only; nothing was signed");
                    }
                    let signed = match e.signer.request("sign_envelope", json!({"envelope": env})).and_then(|v| serde_json::from_value::<SignedEnvelope>(v).map_err(Error::from)) {
                        Ok(s) => s,
                        Err(err) => return bridge::rpc_error(&id, -32000, &err.to_string()),
                    };
                    if let Err(err) = signed.check_against(&env) {
                        return bridge::rpc_error(&id, -32000, &err.to_string());
                    }
                    match e.submit_rpc().send_raw(&signed.raw) {
                        Ok(h) => {
                            let hh = hex::encode(&h);
                            e.record_sent(&hh, &env);
                            bridge::rpc_result(&id, json!(hh))
                        }
                        Err(err) => bridge::rpc_error(&id, -32000, &err.to_string()),
                    }
                }
                Ok(Some(false)) => bridge::rpc_error(&id, 4001, "the person declined"),
                Ok(None) => bridge::rpc_error(&id, 4001, "no answer in the wallet window"),
            }
        }
        "personal_sign" => {
            let (bytes, ask) = match bridge_words::parse_personal_sign(&params) {
                Ok(x) => x,
                Err(e) => return bridge::rpc_error(&id, 4200, &e.to_string()),
            };
            match asks.ask(origin, ask) {
                Err(r) => bridge::rpc_error(&id, bridge::ASK_REFUSED_CODE, r.reason),
                Ok(Some(true)) => {
                    let e = match engine.lock() {
                        Ok(e) => e,
                        Err(_) => return bridge::rpc_error(&id, -32603, "engine busy"),
                    };
                    if !unlocked && !e.unlocked() {
                        return bridge::rpc_error(&id, 4100, "the wallet is locked; nothing was signed");
                    }
                    match e.signer.request("sign_message", json!({"message": hex::encode(&bytes)})) {
                        Ok(v) => bridge::rpc_result(&id, v.get("signature").cloned().unwrap_or(Value::Null)),
                        Err(err) => bridge::rpc_error(&id, -32000, &err.to_string()),
                    }
                }
                Ok(Some(false)) => bridge::rpc_error(&id, 4001, "the person declined"),
                Ok(None) => bridge::rpc_error(&id, 4001, "no answer in the wallet window"),
            }
        }
        "eth_signTypedData_v4" => {
            let (_digest, ask) = match bridge_words::parse_typed_data(&params, &from, chain_id) {
                Ok(x) => x,
                Err(e) => return bridge::rpc_error(&id, 4200, &e.to_string()),
            };
            let lines = match &ask { Ask::SignTypedData { lines, .. } => lines.clone(), _ => vec![] };
            let typed: Value = match params.get(1) {
                Some(Value::String(s)) => serde_json::from_str(s).unwrap_or(Value::Null),
                Some(v) => v.clone(),
                None => Value::Null,
            };
            match asks.ask(origin, ask) {
                Err(r) => bridge::rpc_error(&id, bridge::ASK_REFUSED_CODE, r.reason),
                Ok(Some(true)) => {
                    let e = match engine.lock() {
                        Ok(e) => e,
                        Err(_) => return bridge::rpc_error(&id, -32603, "engine busy"),
                    };
                    match e.signer.request("sign_typed", json!({"typed": typed, "lines": lines})) {
                        Ok(v) => bridge::rpc_result(&id, v.get("signature").cloned().unwrap_or(Value::Null)),
                        Err(err) => bridge::rpc_error(&id, -32000, &err.to_string()),
                    }
                }
                Ok(Some(false)) => bridge::rpc_error(&id, 4001, "the person declined"),
                Ok(None) => bridge::rpc_error(&id, 4001, "no answer in the wallet window"),
            }
        }
        "wallet_watchAsset" => {
            let opts = params.get(0).and_then(|p| p.get("options")).or_else(|| params.get("options")).cloned().unwrap_or(Value::Null);
            let address = match opts.get("address").and_then(|a| a.as_str()).and_then(|a| Address::parse(a).ok()) {
                Some(a) => a,
                None => return bridge::rpc_error(&id, -32602, "wallet_watchAsset needs options.address"),
            };
            // the symbol a site names: at most twelve characters from the plain set (letters, digits, a few marks),
            // so a look-alike or a control character cannot dress the ask (wallet-security's G10)
            let symbol: String = opts.get("symbol").and_then(|s| s.as_str()).unwrap_or("?").chars().filter(|c| c.is_ascii_alphanumeric() || matches!(c, '.' | '-' | '_')).take(12).collect();
            let symbol = if symbol.is_empty() { "?".to_string() } else { symbol };
            match asks.ask(origin, Ask::WatchAsset { address, symbol: symbol.clone() }) {
                Err(r) => bridge::rpc_error(&id, bridge::ASK_REFUSED_CODE, r.reason),
                Ok(Some(true)) => {
                    if let Ok(mut e) = engine.lock() {
                        e.settings.tokens.push(json!({"address": address, "symbol": symbol, "origin": origin}));
                        let _ = e.save_settings();
                    }
                    bridge::rpc_result(&id, json!(true))
                }
                _ => bridge::rpc_result(&id, json!(false)),
            }
        }
        other => bridge::rpc_error(&id, 4200, &format!("{other} is not offered by this wallet")),
    }
}

fn bridge_listener(engine: Arc<Mutex<Engine>>, port: u16) {
    let handler: Arc<dyn Fn(Request) -> Response + Send + Sync> = Arc::new(move |req: Request| {
        let origin = req.header("origin").map(|o| o.to_string());
        let path = req.path.split('?').next().unwrap_or("").to_string();
        if path == "/provider.js" {
            let mut r = Response::file("text/javascript; charset=utf-8", bridge::provider_script(port).into_bytes());
            if let Some(o) = &origin {
                r = r.with_cors(o);
            }
            return r;
        }
        let origin = match origin {
            Some(o) if o.starts_with("http://") || o.starts_with("https://") => o,
            _ => return Response::json(403, &json!({"error": "a website's origin is needed"})),
        };
        if req.method == "OPTIONS" {
            return Response { status: 204, content_type: "text/plain", body: vec![], cors_origin: Some(origin), site_token: None };
        }
        if path != "/rpc" || req.method != "POST" {
            return Response::not_found().with_cors(&origin);
        }
        let body: Value = match serde_json::from_slice(&req.body) {
            Ok(v) => v,
            Err(_) => return Response::json(400, &bridge::rpc_error(&Value::Null, -32700, "bad JSON")).with_cors(&origin),
        };
        let site_token = req.header("x-igneum-site").map(|t| t.to_string());
        // a browser's fetch carries Sec-Fetch headers it will not let a page remove; a request without them is a
        // program on this machine wearing a site's name, and its connect ask says so
        let browser = req.header("sec-fetch-mode").is_some() || req.header("sec-fetch-site").is_some();
        let mut minted: Option<String> = None;
        let out = if let Some(batch) = body.as_array() {
            Value::Array(batch.iter().map(|r| bridge_request(&engine, &origin, site_token.as_deref(), browser, &mut minted, r)).collect())
        } else {
            bridge_request(&engine, &origin, site_token.as_deref(), browser, &mut minted, &body)
        };
        let mut r = Response::json(200, &out).with_cors(&origin);
        r.site_token = minted;
        r
    });
    if let Err(e) = http::serve(port, |_| {}, handler) {
        eprintln!("bridge: could not listen on 127.0.0.1:{port}: {e}");
    }
}

/// The updater's thread: when a check is due it runs against the release host outside the engine lock (the
/// network calls take seconds), then the result is written under the lock; a wanted download runs the same way,
/// its progress written under the lock as it goes; the ready file is recorded in the state.
fn update_loop(engine: Arc<Mutex<Engine>>) {
    let base = update::Updater::base();
    let platform = update::platform_word();
    loop {
        std::thread::sleep(Duration::from_secs(5));
        let (due, want, data_dir, directory, package, sha, apply_now) = match engine.lock() {
            Ok(e) => {
                // the apply waits for an idle moment: a ready package, automatic updates on, no POST from the page
                // for two minutes, no site's ask pending, nothing already applying
                let idle = e.last_activity.elapsed() >= Duration::from_secs(update::IDLE_BEFORE_APPLY_S) && e.asks.pending().is_empty();
                let apply_now = e.update.state.status == "ready" && e.update.state.auto && !e.update.state.applying && !e.update.state.file.is_empty() && idle;
                (e.update.due(), e.update.wants_download(), e.data_dir.clone(), e.update.state.directory.clone(), e.update.state.package.clone(), e.update.state.package_sha256.clone(), apply_now)
            }
            Err(_) => continue,
        };
        if apply_now {
            let (file, pair) = match engine.lock() {
                Ok(e) => (PathBuf::from(&e.update.state.file), format!("{} -> {}", VERSION, e.update.state.version)),
                Err(_) => continue,
            };
            match update::apply(&file, &data_dir, &pair) {
                Ok(()) => {
                    if let Ok(mut e) = engine.lock() {
                        e.update.state.applying = true;
                        e.update.state.log = update::log_tail(&data_dir, 6);
                        let _ = e.signer.request("lock", json!({}));
                    }
                    eprintln!("engine: applying the update {pair}; exiting for the restart");
                    // the window host reads this line and ends itself when the engine is gone, so the installer finds
                    // no file of ours held (a Windows host's log of 15 October 2026: a window that stayed, the installer's code 5)
                    println!("UPDATE {}", json!({"applying": pair}));
                    use std::io::Write as _;
                    let _ = std::io::stdout().flush();
                    std::thread::sleep(Duration::from_millis(500));
                    std::process::exit(0);
                }
                Err(err) => {
                    eprintln!("engine: update apply: {err}");
                    update::log_line(&data_dir, &format!("apply failed: {} ({pair})", update::code_words(&err)));
                    if let Ok(mut e) = engine.lock() {
                        e.update.state.error = err;
                        e.update.state.status = "error".into();
                        e.update.state.log = update::log_tail(&data_dir, 6);
                    }
                }
            }
            continue;
        }
        if due {
            let result = update::check(&base, VERSION, platform);
            if let Ok(mut e) = engine.lock() {
                if let Err(err) = &result {
                    eprintln!("engine: update check: {err}");
                    update::log_line(&data_dir, &format!("check failed: {err}; this install is untouched"));
                }
                e.update.note_checked(&result, now_unix());
                e.update.state.log = update::log_tail(&data_dir, 6);
            }
            continue;
        }
        if want && !package.is_empty() {
            if let Ok(mut e) = engine.lock() {
                e.update.state.status = "downloading".into();
                e.update.state.progress = 0.0;
            }
            let dir = data_dir.join("updates");
            let eng = engine.clone();
            let mut last = Instant::now();
            let mut progress = |got: u64, total: u64| {
                if last.elapsed() >= Duration::from_millis(500) {
                    last = Instant::now();
                    if let Ok(mut e) = eng.lock() {
                        e.update.state.progress = if total > 0 { got as f64 / total as f64 } else { 0.0 };
                        e.update.state.size = total;
                    }
                }
            };
            let r = update::download(&base, &directory, &package, &sha, &dir, &mut progress);
            if let Ok(mut e) = engine.lock() {
                match r {
                    Ok(path) => {
                        e.update.state.status = "ready".into();
                        e.update.state.progress = 1.0;
                        e.update.state.size = fs::metadata(&path).map(|m| m.len()).unwrap_or(0);
                        e.update.state.file = path.to_string_lossy().to_string();
                        eprintln!("engine: update {} downloaded to {}", e.update.state.version, e.update.state.file);
                    }
                    Err(err) => {
                        eprintln!("engine: update download: {err}");
                        let failed: std::result::Result<update::Found, String> = Err(err);
                        e.update.note_checked(&failed, now_unix());
                    }
                }
            }
        }
    }
}

fn idle_lock_loop(engine: Arc<Mutex<Engine>>) {
    loop {
        std::thread::sleep(Duration::from_secs(5));
        if let Ok(mut e) = engine.lock() {
            e.asks.sweep();
            e.poll();
            let minutes = e.settings.idle_lock_min;
            if minutes > 0 && e.unlocked() && e.watch.is_none() && e.last_activity.elapsed() > Duration::from_secs(minutes * 60) {
                let _ = e.signer.request("lock", json!({}));
                e.quotes.clear();
                let _ = e.refresh_signer_status();
            }
        }
    }
}

/// Thousands with a comma (the design system's number rule) for the engine's own lines.
fn display_commas(n: u64) -> String {
    let s = n.to_string();
    let mut out = String::new();
    for (i, c) in s.chars().enumerate() {
        if i > 0 && (s.len() - i) % 3 == 0 {
            out.push(',');
        }
        out.push(c);
    }
    out
}

/// The engine's data root when neither --data-dir nor IGNEUM_WALLET_DATA names one: $HOME/.igneum-wallet on Unix
/// (the Mac app's root since 0.0.1); on Windows %APPDATA%\network.igneum.wallet (the fleet script's root), because a
/// window host there has no HOME and through 0.0.8 the engine fell to the program's own folder, which an uninstall
/// removes with the vault inside it (found 12 October 2026 reading the first over-the-air rows).
fn default_data_dir() -> PathBuf {
    #[cfg(windows)]
    {
        if let Ok(a) = std::env::var("APPDATA") {
            if !a.is_empty() {
                return PathBuf::from(a).join("network.igneum.wallet");
            }
        }
    }
    let home = std::env::var("HOME").unwrap_or_else(|_| ".".into());
    PathBuf::from(home).join(".igneum-wallet")
}

/// The one-time move on Windows (0.0.9): a `.igneum-wallet` folder beside the program (the root through 0.0.8) is
/// moved whole into the new root when the new root holds no vault and no watch file yet; a copy that cannot be
/// moved is left in place and the old root is used, so no vault is ever lost to the move. Logged in updates.log.
fn migrate_windows_data_root(data_dir: &std::path::Path) {
    #[cfg(windows)]
    {
        if arg("--data-dir").is_some() || std::env::var("IGNEUM_WALLET_DATA").is_ok() {
            return;
        }
        let old = std::env::current_exe().ok().and_then(|e| e.parent().map(|p| p.join(".igneum-wallet")));
        let Some(old) = old else { return };
        if !old.is_dir() || old == data_dir {
            return;
        }
        let new_has = data_dir.join("vault.json").exists() || data_dir.join("watch.json").exists();
        if new_has {
            return;
        }
        let _ = fs::remove_dir(data_dir); // an empty new root makes way for the rename
        match fs::rename(&old, data_dir) {
            Ok(()) => update::log_line(data_dir, &format!("moved the data root from {} to {} (0.0.9, one time)", old.display(), data_dir.display())),
            Err(e) => {
                fs::create_dir_all(data_dir).ok();
                update::log_line(data_dir, &format!("the data root at {} could not be moved ({e}); it stays beside the program", old.display()));
            }
        }
    }
    #[cfg(not(windows))]
    {
        let _ = data_dir;
    }
}

fn arg(name: &str) -> Option<String> {
    let args: Vec<String> = std::env::args().collect();
    args.iter().position(|a| a == name).and_then(|i| args.get(i + 1).cloned())
}

/// The endpoint a settings file stored without a choice, moved from a retired default to the chain's current public
/// name; a named node (the person's or --rpc's) and every other stored endpoint are returned as they are.
fn migrate_default_endpoint(rpc: &str, named: bool) -> String {
    const RETIRED_DEFAULTS: &[&str] = &["https://rpc12.igneum.network"];
    if !named && RETIRED_DEFAULTS.contains(&rpc.trim_end_matches('/')) {
        igneum_wallet_core::chain::PUBLIC_7322_RPC.to_string()
    } else {
        rpc.to_string()
    }
}

fn main() {
    // --version: the crate's version and nothing else (the launcher and the updater's "current" read it; the
    // previous wallet's flag)
    if std::env::args().any(|a| a == "--version") {
        println!("igneum-wallet-engine {VERSION}");
        return;
    }
    let data_dir = arg("--data-dir")
        .map(PathBuf::from)
        .or_else(|| std::env::var("IGNEUM_WALLET_DATA").ok().map(PathBuf::from))
        .unwrap_or_else(default_data_dir);
    migrate_windows_data_root(&data_dir);
    fs::create_dir_all(&data_dir).expect("data dir");
    let ui_dir = arg("--ui").map(PathBuf::from);
    let signer_path = arg("--signer").unwrap_or_else(|| {
        std::env::current_exe()
            .ok()
            .and_then(|p| p.parent().map(|d| d.join("igneum-signer")))
            .map(|p| p.to_string_lossy().to_string())
            .unwrap_or_else(|| "igneum-signer".into())
    });
    let port: u16 = arg("--port").and_then(|p| p.parse().ok()).unwrap_or(0);
    let bridge_port: u16 = arg("--bridge-port").and_then(|p| p.parse().ok()).unwrap_or(bridge::DEFAULT_PORT);
    let light_kdf = std::env::var("IGNEUM_SIGNER_LIGHT_KDF").is_ok();
    // --exit-with-parent: the window host holds this process's stdin; when the host dies for any reason the pipe
    // closes, and the engine locks the signer and exits rather than living on with a key in the signer's memory
    // (found on builder seven: a killed host left its engine running). Opt-in, because a shell-started engine has no
    // such parent and a closed stdin would end it at once.
    if std::env::args().any(|a| a == "--exit-with-parent") {
        std::thread::spawn(|| {
            let mut line = String::new();
            loop {
                match std::io::stdin().read_line(&mut line) {
                    Ok(0) | Err(_) => break,
                    Ok(_) => line.clear(),
                }
            }
            eprintln!("engine: the host is gone; exiting");
            std::process::exit(0);
        });
    }

    let mut settings: Settings = fs::read_to_string(data_dir.join("settings.json"))
        .ok()
        .and_then(|s| serde_json::from_str(&s).ok())
        .unwrap_or_default();
    // the old default "system" with no choice recorded reads as no choice: dark
    if settings.theme == "system" && !settings.theme_chosen {
        settings.theme = "dark".into();
    }
    // the previous default endpoint stored without a choice (the wallets of 0.0.16 to 0.0.21 wrote 7322's own host as the
    // default) moves to the chain's public name (the review lead's ruling of 16 October 2026); a node a person named stays
    settings.rpc = migrate_default_endpoint(&settings.rpc, settings.rpc_named);
    if let Some(r) = arg("--rpc") {
        // a node named on the command line is used as it is (the tools and the smokes name a dev node)
        settings.rpc = r;
        settings.rpc_named = true;
    }
    let watch: Option<WatchFile> = fs::read_to_string(data_dir.join("watch.json")).ok().and_then(|s| serde_json::from_str(&s).ok());
    let sent: Vec<Sent> = fs::read_to_string(data_dir.join("history.json")).ok().and_then(|s| serde_json::from_str(&s).ok()).unwrap_or_default();
    let sites: Vec<Site> = fs::read_to_string(data_dir.join("sites.json")).ok().and_then(|s| serde_json::from_str(&s).ok()).unwrap_or_default();

    let signer = SignerClient::spawn(&signer_path, light_kdf).expect("the signer process");
    let mut engine = Engine {
        data_dir: data_dir.clone(),
        common_passwords: ui_dir.as_ref().and_then(|d| fs::read_to_string(d.join("common-passwords.txt")).ok())
            .map(|t| t.lines().map(|l| l.trim().to_lowercase()).filter(|l| !l.is_empty() && !l.starts_with('#')).collect()).unwrap_or_default(),
        ui_dir,
        settings,
        chain: Chain::igneum_devnet(),
        signer,
        watch,
        signer_status: json!({}),
        quotes: HashMap::new(),
        sent,
        node: NodeInfo::default(),
        source: node_source::Source::Named { url: String::new() },
        gate: biometric::Gate::new(),
        bio: biometric::BiometricState::default(),
        host_token: String::new(),
        update: update::Updater::new(VERSION, true),
        identity: None,
        poll_misses: 0,
        shares_cache: None,
        scan: None,
        headers: VecDeque::new(),
        reorgs: Vec::new(),
        last_activity: Instant::now(),
        policy: Policy::default_wallet(),
        asks: AskQueue::new(),
        sites,
        bridge_port,
    };
    let vault_path = engine.vault_path();
    engine.signer.request("open", json!({"path": vault_path})).expect("open the vault path");
    engine.refresh_signer_status().expect("signer status");
    engine.choose_source();
    engine.poll();
    let _ = engine.save_settings();
    // the biometric enrolment is the sealed file's presence; the host's token is made here and printed once on the
    // HOST line below for the window host alone (a shell-started engine has no host: the token is still made and
    // never read, so no call can pass the host check)
    engine.update.state.auto = engine.settings.auto_update;
    engine.bio.enrolled = engine.biometric_path().exists();
    engine.bio.idle_lock = engine.settings.idle_lock_min > 0;
    engine.bio.idle_lock_min = engine.settings.idle_lock_min;
    let mut host_bytes = [0u8; 16];
    OsRng.fill_bytes(&mut host_bytes);
    engine.host_token = hex::encode(&host_bytes)[2..].to_string();
    let host_line = json!({"token": engine.host_token, "biometric_file": engine.biometric_path().to_string_lossy()});

    let mut token_bytes = [0u8; 16];
    OsRng.fill_bytes(&mut token_bytes);
    let token = hex::encode(&token_bytes)[2..].to_string();
    let engine = Arc::new(Mutex::new(engine));
    {
        // the update log: every start names the running version (the version change an apply made is read here)
        let mut e = engine.lock().unwrap_or_else(|p| p.into_inner());
        let dd = e.data_dir.clone();
        update::log_line(&dd, &format!("start {VERSION}"));
        e.update.state.log = update::log_tail(&dd, 6);
    }
    {
        let e = engine.clone();
        std::thread::spawn(move || idle_lock_loop(e));
    }
    {
        let e = engine.clone();
        std::thread::spawn(move || update_loop(e));
    }
    if bridge_port != 0 {
        let e = engine.clone();
        std::thread::spawn(move || bridge_listener(e, bridge_port));
    }
    let handler_engine = engine.clone();
    let prefix = format!("/t/{token}/");
    let handler: Arc<dyn Fn(Request) -> Response + Send + Sync> = Arc::new(move |req: Request| {
        let path = req.path.split('?').next().unwrap_or("").to_string();
        let rest = match path.strip_prefix(&prefix) {
            Some(r) => r.to_string(),
            None => return Response::json(403, &json!({"error": "no token"})),
        };
        if rest.starts_with("api/") {
            match route(&handler_engine, req, &rest) {
                Ok(mut v) => {
                    // the reply is serialized then scrubbed: a reveal's words or key live on in no Value
                    let r = Response::json(200, &v);
                    scrub(&mut v);
                    r
                }
                Err(e) => Response::json(400, &json!({"error": e.to_string()})),
            }
        } else {
            let ui = handler_engine.lock().ok().and_then(|e| e.ui_dir.clone());
            serve_static(&ui, &rest)
        }
    });
    let token_for_print = token.clone();
    http::serve(
        port,
        move |p| {
            println!("ENGINE {}", json!({"url": format!("http://127.0.0.1:{p}/t/{token_for_print}/"), "pid": std::process::id()}));
            // IGNEUM_WALLET_URL_FILE (the app smoke on a bench Mac, 11 October 2026): the same line written to a
            // file the person running the smoke owns (0600), since a host started by `open` shows its engine's
            // stdout to nobody; never set in a packaged run
            if let Ok(f) = std::env::var("IGNEUM_WALLET_URL_FILE") {
                if !f.is_empty() {
                    let line = json!({"url": format!("http://127.0.0.1:{p}/t/{token_for_print}/"), "pid": std::process::id()}).to_string();
                    if fs::write(&f, line).is_ok() {
                        #[cfg(unix)]
                        {
                            use std::os::unix::fs::PermissionsExt;
                            let _ = fs::set_permissions(&f, fs::Permissions::from_mode(0o600));
                        }
                    }
                }
            }
            // the HOST line: the host's token and the sealed file's path, read by the window host alone (the
            // previous wallet's form); the page never sees it
            println!("HOST {host_line}");
            // --open: the page in the machine's default browser (the packaged devnet build has no window host of
            // its own: the launcher starts the engine with this flag). The URL carries the session token, so only
            // this browser profile reaches the page; the opener is the platform's own and nothing else is run.
            if std::env::args().any(|a| a == "--open") {
                let url = format!("http://127.0.0.1:{p}/t/{token_for_print}/");
                let r = if cfg!(target_os = "macos") {
                    std::process::Command::new("open").arg(&url).spawn()
                } else if cfg!(target_os = "windows") {
                    std::process::Command::new("cmd").args(["/C", "start", "", &url]).spawn()
                } else {
                    std::process::Command::new("xdg-open").arg(&url).spawn()
                };
                if let Err(e) = r {
                    eprintln!("engine: could not open a browser ({e}); open the ENGINE url above by hand");
                }
            }
            use std::io::Write;
            let _ = std::io::stdout().flush();
        },
        handler,
    )
    .expect("serve");
}

#[cfg(test)]
mod endpoint_migration_tests {
    use super::migrate_default_endpoint;

    #[test]
    fn the_stored_old_default_moves_to_the_public_name_unless_named() {
        let public = igneum_wallet_core::chain::PUBLIC_7322_RPC;
        assert_eq!(migrate_default_endpoint("https://rpc12.igneum.network", false), public);
        assert_eq!(migrate_default_endpoint("https://rpc12.igneum.network/", false), public);
        assert_eq!(migrate_default_endpoint("https://rpc12.igneum.network", true), "https://rpc12.igneum.network");
        assert_eq!(migrate_default_endpoint("http://192.0.2.10:8648", false), "http://192.0.2.10:8648");
        assert_eq!(migrate_default_endpoint(public, false), public);
    }
}
