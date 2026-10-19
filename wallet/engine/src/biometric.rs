//! The biometric gate (OLD-BASE.md step 3, third piece: the previous wallet's `igneum-common::biometric`, carried
//! whole; the randomness and the hex from the restart's crates). The part that needs no Touch ID and no Windows
//! Hello: challenges the engine issues, the host confirms after the prompt, and the action consumes once. The window
//! host (macOS: the Swift host; Windows: the WebView2 host) holds the sealed secret; this module only decides whether
//! a confirmation is fresh.
//! confirms after the prompt, and the action consumes once. The window host (macOS: app/mac/Biometric.swift;
//! Windows: the WebView2 host) holds the secret; this module only decides whether a confirmation is fresh.
//!
//! The flow for a gated action:
//!   1. the window asks the engine for a challenge: `Gate::issue(purpose, bound, reason)` gives a nonce; the reason
//!      (at most 80 characters) is what the prompt shows, built by the engine so the window cannot word it;
//!   2. the window hands the nonce to the host; the host reads the reason from the engine (with the host token, which
//!      only the host knows: the engine printed it on its stdout), shows the prompt, and on success posts
//!      `Gate::confirm(nonce)`;
//!   3. the window calls the action with the nonce; the engine runs `Gate::take(nonce, purpose, bound)`: confirmed
//!      within CHALLENGE_TTL_S, the same purpose and the same binding (the quote for a send, the new address for an
//!      address change), never used before. One nonce, one action.
//!
//! Enrolment (the wallet): the window posts the password to the engine, which checks it and keeps it under a one-time
//! token for ENROL_TTL_S; the host takes it with the token after the prompt, seals it and writes the file. The
//! password reaches the host over 127.0.0.1 only, never through the page.

use rand_core::{OsRng, RngCore};
use serde::Serialize;
use std::time::{Duration, Instant};

/// A confirmation is fresh for this long after the prompt succeeded.
pub const CHALLENGE_TTL_S: u64 = 30;
/// An unconfirmed challenge waits for the prompt this long (the confirm screen can sit open).
pub const CHALLENGE_WAIT_S: u64 = 180;
/// The enrolment token's life.
pub const ENROL_TTL_S: u64 = 120;
/// The prompt's reason string: at most this many characters (the hard clip); the strings the engines build stay
/// under 60, in our voice, no trailing full stop ("Unlock your wallet", "Send 1.5 IGN to 0x7F45…C126").
pub const REASON_MAX: usize = 80;
/// The idle lock (the wallet): minutes without the window reporting activity before the key is zeroed.
pub const IDLE_LOCK_MIN: u64 = 5;

/// What `/api/state` carries under `biometric`.
#[derive(Clone, Serialize, Default)]
pub struct BiometricState {
    /// the host said the prompt can be shown (Touch ID set up, Windows Hello configured)
    pub available: bool,
    /// touchid | hello | "" (no host yet)
    pub kind: String,
    /// the sealed file exists: the gated actions need the prompt
    pub enrolled: bool,
    /// the host's word for why it is unavailable, in the window's wording
    pub message: String,
    /// the last prompt's outcome: ok | cancelled | failed | locked | invalidated | unavailable | ""
    pub last_result: String,
    pub last_op: String,
    pub last_at: f64,
    /// the wallet: lock after IDLE_LOCK_MIN minutes idle (setting, on by default once enrolled)
    pub idle_lock: bool,
    pub idle_lock_min: u64,
    /// set when the password changed under an enrolment: the sealed password is stale and was removed
    pub needs_enrol: bool,
}

pub struct Challenge {
    pub nonce: String,
    pub purpose: String,
    pub bound: String,
    pub reason: String,
    issued: Instant,
    confirmed: Option<Instant>,
    used: bool,
}

#[derive(Default)]
pub struct Gate {
    challenges: Vec<Challenge>,
    enrol: Option<(String, String, Instant)>,
}

#[derive(Debug, PartialEq, Eq)]
pub enum GateError {
    Unknown,
    Expired,
    NotConfirmed,
    Stale,
    Used,
    Mismatch,
}

impl GateError {
    /// The window's wording. `what` is "Touch ID" or "Windows Hello".
    pub fn text(&self, what: &str) -> String {
        match self {
            GateError::Unknown => format!("confirm with {what} first"),
            GateError::Expired => format!("the {what} request timed out; try again"),
            GateError::NotConfirmed => format!("{what} did not confirm this; try again"),
            GateError::Stale => format!("the {what} confirmation is older than {CHALLENGE_TTL_S} s; confirm again"),
            GateError::Used => format!("that {what} confirmation was already used; confirm again"),
            GateError::Mismatch => format!("the {what} confirmation was for something else; confirm again"),
        }
    }
}

fn random_hex(n: usize) -> String {
    let mut raw = vec![0u8; n];
    OsRng.fill_bytes(&mut raw);
    igneum_wallet_core::hex::encode(&raw)[2..].to_string()
}

/// Cuts a reason to REASON_MAX characters (not bytes), with a trailing ellipsis when it was longer.
pub fn clip_reason(s: &str) -> String {
    let count = s.chars().count();
    if count <= REASON_MAX {
        return s.to_string();
    }
    let mut out: String = s.chars().take(REASON_MAX - 1).collect();
    out.push('…');
    out
}

/// The prompt's line for a send: the amount (the caller gives it to 4 decimals) and the checksum address as its
/// first 6 and last 4 characters: "Send 1.5 IGN to 0x7F45…C126".
pub fn send_reason(amount_ign: &str, display_to: &str) -> String {
    let short = if display_to.len() > 10 { format!("{}…{}", &display_to[..6], &display_to[display_to.len() - 4..]) } else { display_to.to_string() };
    clip_reason(&format!("Send {amount_ign} IGN to {short}"))
}

impl Gate {
    pub fn new() -> Gate {
        Gate::default()
    }

    fn prune(&mut self, now: Instant) {
        // used nonces stay until their window ends, so a replay is answered "used", not "unknown"
        self.challenges.retain(|c| now.duration_since(c.issued) < Duration::from_secs(CHALLENGE_WAIT_S + CHALLENGE_TTL_S));
        if let Some((_, _, t)) = &self.enrol {
            if now.duration_since(*t) >= Duration::from_secs(ENROL_TTL_S) {
                self.enrol = None;
            }
        }
    }

    pub fn issue(&mut self, purpose: &str, bound: &str, reason: &str, now: Instant) -> String {
        self.prune(now);
        let nonce = random_hex(16);
        self.challenges.push(Challenge { nonce: nonce.clone(), purpose: purpose.into(), bound: bound.into(), reason: clip_reason(reason), issued: now, confirmed: None, used: false });
        nonce
    }

    /// For the host: what the prompt says for this nonce.
    pub fn reason_of(&self, nonce: &str) -> Option<(String, String)> {
        self.challenges.iter().find(|c| c.nonce == nonce && !c.used).map(|c| (c.purpose.clone(), c.reason.clone()))
    }

    /// The host says the prompt succeeded for this nonce.
    pub fn confirm(&mut self, nonce: &str, now: Instant) -> Result<(), GateError> {
        self.prune(now);
        let c = self.challenges.iter_mut().find(|c| c.nonce == nonce).ok_or(GateError::Unknown)?;
        if c.used {
            return Err(GateError::Used);
        }
        if now.duration_since(c.issued) >= Duration::from_secs(CHALLENGE_WAIT_S) {
            return Err(GateError::Expired);
        }
        c.confirmed = Some(now);
        Ok(())
    }

    /// The action runs: fresh, the same purpose and binding, once.
    pub fn take(&mut self, nonce: &str, purpose: &str, bound: &str, now: Instant) -> Result<(), GateError> {
        let c = self.challenges.iter_mut().find(|c| c.nonce == nonce).ok_or(GateError::Unknown)?;
        if c.used {
            return Err(GateError::Used);
        }
        let Some(t) = c.confirmed else {
            return Err(if now.duration_since(c.issued) >= Duration::from_secs(CHALLENGE_WAIT_S) { GateError::Expired } else { GateError::NotConfirmed });
        };
        if c.purpose != purpose || c.bound != bound {
            return Err(GateError::Mismatch);
        }
        if now.duration_since(t) >= Duration::from_secs(CHALLENGE_TTL_S) {
            c.used = true;
            return Err(GateError::Stale);
        }
        c.used = true;
        Ok(())
    }

    /// Enrolment: the engine keeps the checked secret under a one-time token.
    pub fn enrol_begin(&mut self, secret: &str, now: Instant) -> String {
        let token = random_hex(16);
        self.enrol = Some((token.clone(), secret.to_string(), now));
        token
    }

    /// The host takes the secret with the token, once.
    pub fn enrol_take(&mut self, token: &str, now: Instant) -> Option<String> {
        self.prune(now);
        match self.enrol.take() {
            Some((t, s, _)) if constant_eq(&t, token) => Some(s),
            Some(other) => {
                // a wrong token does not burn the pending enrolment
                self.enrol = Some(other);
                None
            }
            None => None,
        }
    }

    pub fn enrol_pending(&self) -> bool {
        self.enrol.is_some()
    }

    pub fn enrol_cancel(&mut self) {
        self.enrol = None;
    }

    #[cfg(test)]
    fn len(&self) -> usize {
        self.challenges.len()
    }
}

/// Equal strings, compared in constant time over the longer length.
pub fn constant_eq(a: &str, b: &str) -> bool {
    let (a, b) = (a.as_bytes(), b.as_bytes());
    let mut diff = (a.len() ^ b.len()) as u8;
    for i in 0..a.len().max(b.len()) {
        diff |= a.get(i).copied().unwrap_or(0) ^ b.get(i).copied().unwrap_or(0);
    }
    diff == 0
}

/// A fingerprint of a send quote, so the confirmation binds to what the window showed.
pub fn send_binding(to: &str, value: &str, tx_nonce: u64, chain_id: u64) -> String {
    format!("send:{}:{}:{}:{}", to.to_ascii_lowercase(), value, tx_nonce, chain_id)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn t(base: Instant, s: u64) -> Instant {
        base + Duration::from_secs(s)
    }

    #[test]
    fn confirm_then_take_once() {
        let mut g = Gate::new();
        let now = Instant::now();
        let n = g.issue("send", "send:0xab:1:0:7", "Send 1 IGN to 0xab", now);
        assert_eq!(g.reason_of(&n), Some(("send".into(), "Send 1 IGN to 0xab".into())));
        // not confirmed yet
        assert_eq!(g.take(&n, "send", "send:0xab:1:0:7", t(now, 1)), Err(GateError::NotConfirmed));
        g.confirm(&n, t(now, 2)).unwrap();
        // wrong binding, wrong purpose
        assert_eq!(g.take(&n, "send", "send:0xcd:1:0:7", t(now, 3)), Err(GateError::Mismatch));
        assert_eq!(g.take(&n, "reveal", "send:0xab:1:0:7", t(now, 3)), Err(GateError::Mismatch));
        // the right one, once
        assert_eq!(g.take(&n, "send", "send:0xab:1:0:7", t(now, 3)), Ok(()));
        assert_eq!(g.take(&n, "send", "send:0xab:1:0:7", t(now, 4)), Err(GateError::Used));
        assert_eq!(g.confirm(&n, t(now, 4)), Err(GateError::Used));
        assert!(g.reason_of(&n).is_none());
    }

    #[test]
    fn replay_and_expiry() {
        let mut g = Gate::new();
        let now = Instant::now();
        assert_eq!(g.take("nope", "send", "", now), Err(GateError::Unknown));
        assert_eq!(g.confirm("nope", now), Err(GateError::Unknown));
        // a confirmation older than the TTL is stale and burns the nonce
        let n = g.issue("reveal", "", "Show the words", now);
        g.confirm(&n, t(now, 1)).unwrap();
        assert_eq!(g.take(&n, "reveal", "", t(now, 1 + CHALLENGE_TTL_S)), Err(GateError::Stale));
        assert_eq!(g.take(&n, "reveal", "", t(now, 2)), Err(GateError::Used));
        // a challenge nobody confirmed within the wait expires
        let n2 = g.issue("reveal", "", "Show the words", now);
        assert_eq!(g.confirm(&n2, t(now, CHALLENGE_WAIT_S)), Err(GateError::Expired));
        assert_eq!(g.take(&n2, "reveal", "", t(now, CHALLENGE_WAIT_S)), Err(GateError::Expired));
        // pruned away after wait + ttl
        let _ = g.issue("reveal", "", "x", t(now, CHALLENGE_WAIT_S + CHALLENGE_TTL_S + 1));
        assert_eq!(g.len(), 1);
        // and a used nonce still answers "used" inside its window
        let n4 = g.issue("send", "b", "r", t(now, 1000));
        g.confirm(&n4, t(now, 1001)).unwrap();
        assert_eq!(g.take(&n4, "send", "b", t(now, 1002)), Ok(()));
        assert_eq!(g.confirm(&n4, t(now, 1003)), Err(GateError::Used));
        // a fresh confirmation at the edge still works
        let n3 = g.issue("send", "b", "r", now);
        g.confirm(&n3, t(now, CHALLENGE_WAIT_S - 1)).unwrap();
        assert_eq!(g.take(&n3, "send", "b", t(now, CHALLENGE_WAIT_S - 1 + CHALLENGE_TTL_S - 1)), Ok(()));
    }

    #[test]
    fn nonces_are_distinct_and_hex() {
        let mut g = Gate::new();
        let now = Instant::now();
        let a = g.issue("send", "", "r", now);
        let b = g.issue("send", "", "r", now);
        assert_ne!(a, b);
        assert_eq!(a.len(), 32);
        assert!(a.chars().all(|c| c.is_ascii_hexdigit()));
    }

    #[test]
    fn enrol_token_one_time_and_expiry() {
        let mut g = Gate::new();
        let now = Instant::now();
        let tok = g.enrol_begin("correct horse", now);
        assert!(g.enrol_pending());
        // a wrong token leaves the pending enrolment in place
        assert_eq!(g.enrol_take("wrong", t(now, 1)), None);
        assert!(g.enrol_pending());
        assert_eq!(g.enrol_take(&tok, t(now, 1)).as_deref(), Some("correct horse"));
        assert_eq!(g.enrol_take(&tok, t(now, 1)), None);
        assert!(!g.enrol_pending());
        // expiry
        let tok2 = g.enrol_begin("p", now);
        assert_eq!(g.enrol_take(&tok2, t(now, ENROL_TTL_S)), None);
        // cancel
        let tok3 = g.enrol_begin("p", now);
        g.enrol_cancel();
        assert_eq!(g.enrol_take(&tok3, t(now, 1)), None);
    }

    #[test]
    fn reasons_are_clipped_to_eighty() {
        let long = "x".repeat(200);
        assert_eq!(clip_reason(&long).chars().count(), REASON_MAX);
        assert_eq!(clip_reason("short"), "short");
        let r = send_reason("1.5", "0x7E5F4552091A69125d5DfCb7b8C2659029395Bdf");
        assert_eq!(r, "Send 1.5 IGN to 0x7E5F…5Bdf");
        assert!(r.chars().count() < 60);
        // a long amount still fits under 60
        let r2 = send_reason("123456789.1234", "0x7E5F4552091A69125d5DfCb7b8C2659029395Bdf");
        assert_eq!(r2, "Send 123456789.1234 IGN to 0x7E5F…5Bdf");
        assert!(r2.chars().count() < 60);
        // absurd amount: still clipped at 80 characters
        let r3 = send_reason(&"9".repeat(90), "0x7E5F4552091A69125d5DfCb7b8C2659029395Bdf");
        assert_eq!(r3.chars().count(), REASON_MAX);
    }

    #[test]
    fn constant_eq_and_binding() {
        assert!(constant_eq("abc", "abc"));
        assert!(!constant_eq("abc", "abd"));
        assert!(!constant_eq("abc", "abcd"));
        assert!(!constant_eq("", "a"));
        assert_eq!(send_binding("0xAB", "5", 3, 7), "send:0xab:5:3:7");
    }

    #[test]
    fn state_defaults() {
        let s = BiometricState::default();
        assert!(!s.available && !s.enrolled && s.kind.is_empty() && s.last_result.is_empty());
        let v = serde_json::to_value(&s).unwrap();
        assert_eq!(v["idle_lock_min"], 0);
    }
}
