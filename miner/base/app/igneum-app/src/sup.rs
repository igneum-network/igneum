//! The worker layer (docs/product-a/OLD-BASE.md, step 2): the restart's supervisor inside the old engine. The
//! engine keeps its process shape (the local server, the hosts' wrapper lines, the updater, the jobs, rights, heat,
//! its own watchdogs) and gives the cards to `igneum_supervisor::Supervisor`: protocol v0 workers, the in-process
//! verifier, the transition state machine, the keyed share path, pools and failover. This module builds it, ticks
//! it, and reads its snapshot into the window's card words (`CardState`), so the window and its 126 audit tests see
//! the same page they always did.
//!
//! The first piece (this file): the supervisor is built beside the old miner path and ticked; the card mapping is a
//! pure function over the slot's facts, with its tests. The old miner launch goes in the next piece, when the
//! engine's cards are read from here instead of from igneum-miner's lines.

use std::path::{Path, PathBuf};

use igneum_supervisor::state::{LaneView, SlotState, SlotView, Snapshot};
use igneum_supervisor::{Config, Supervisor};

use crate::state::CardState;

/// The supervisor's data directory: `<app dir>/ember` (its settings, tune file, journal and worker cache), apart
/// from the engine's own files in the app directory.
pub fn data_dir(app_dir: &Path) -> PathBuf {
    app_dir.join("ember")
}

/// Build the supervisor for this engine: the workers are found in the packaged binaries directory (where the
/// engine's own tools live) and beside the executable, as the restart's Ember finds them.
pub fn build(version: &str, bins_dir: &Path, app_dir: &Path) -> Supervisor {
    build_with_dev_fee(version, bins_dir, app_dir, true)
}

/// `build` with the app's dev fee switch (settings `dev_fee`, default on; ED-05, the founder's word of 11 October
/// 2026): on takes the signed manifest's default percent beside the workers, off is `--dev-fee 0`'s form; without
/// the signed manifest the fee is off and the Earnings row says why (igneum_supervisor::devfee).
pub fn build_with_dev_fee(version: &str, bins_dir: &Path, app_dir: &Path, dev_fee_on: bool) -> Supervisor {
    let cfg = Config::new("Ember", version, bins_dir, data_dir(app_dir)).with_dev_fee(if dev_fee_on { None } else { Some(0) });
    Supervisor::new(cfg)
}

/// The facts of one slot the window's card is made from (the slot view's fields the mapping reads, and nothing
/// else), so the mapping is a pure function with tests that need no running supervisor.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct SlotFacts {
    pub id: String,
    pub fingerprint: String,
    pub bus_id: String,
    pub index: u32,
    pub backend: String,
    pub name: String,
    pub vendor: String,
    pub kind: String,
    pub runtime: String,
    pub memory_total: u64,
    pub reason: String,
    pub enabled: bool,
    pub state: Option<SlotState>,
    pub message: String,
    /// VectorWork: the router's word for the card ("coordinate 2", "coordinate 1, probing"), empty when unrouted.
    pub coord_word: String,
    pub lane_word: String,
    pub lanes: Vec<LaneView>,
    pub reported_60s_hps: f64,
    pub reported_10s_hps: f64,
    pub stint_s: f64,
    pub validated_hps: f64,
    pub invalid_results: u64,
    pub restarts: u32,
}

impl From<&SlotView> for SlotFacts {
    fn from(v: &SlotView) -> SlotFacts {
        SlotFacts {
            id: v.id.clone(),
            fingerprint: v.fingerprint.clone(),
            bus_id: v.bus_id.clone(),
            index: v.index,
            backend: v.backend.clone(),
            name: v.name.clone(),
            vendor: v.vendor.clone(),
            kind: v.kind.clone(),
            runtime: v.runtime.clone(),
            memory_total: v.memory_total,
            reason: v.reason.clone(),
            enabled: v.enabled,
            state: Some(v.state.clone()),
            message: v.message.clone(),
            coord_word: v.coord_word.clone(),
            lane_word: v.lane_word.clone(),
            lanes: v.lanes.clone(),
            reported_60s_hps: v.reported_60s_hps,
            reported_10s_hps: v.reported_10s_hps,
            stint_s: v.stint_s,
            validated_hps: v.validated_hps,
            invalid_results: v.invalid_results,
            restarts: v.restarts,
        }
    }
}

/// The window's state word for a slot (the seven words the audit tests know: off, waiting, starting, ready, mining,
/// stopped, failed; never a bare fault word).
pub fn state_word(s: Option<&SlotState>, enabled: bool) -> &'static str {
    if !enabled {
        return "off";
    }
    match s {
        None | Some(SlotState::Off) => "off",
        Some(SlotState::Idle) => "waiting",
        Some(SlotState::Opening) | Some(SlotState::Preparing) => "starting",
        Some(SlotState::Mining) | Some(SlotState::Tuning) => "mining",
        Some(SlotState::Paused) => "ready",
        Some(SlotState::Refused) => "failed",
        Some(SlotState::Faulted) => "stopped",
    }
}

/// The window's vendor word (apple | nvidia | amd | other) and worker word (Metal | CUDA | OpenCL | CPU) for a slot's
/// backend and vendor.
pub fn vendor_words(vendor: &str, backend: &str) -> (&'static str, &'static str) {
    let vendor = vendor.to_ascii_lowercase();
    let backend = backend.to_ascii_lowercase();
    let vendor_word = if vendor.contains("apple") || backend == "metal" {
        "apple"
    } else if vendor.contains("nvidia") || backend == "cuda" {
        "nvidia"
    } else if vendor.contains("amd") || vendor.contains("advanced micro") {
        "amd"
    } else {
        "other"
    };
    let worker = match backend.as_str() {
        "metal" => "Metal",
        "cuda" => "CUDA",
        "opencl" => "OpenCL",
        _ => "CPU",
    };
    (vendor_word, worker)
}

/// One slot as the window's card. The fields the old miner path filled from igneum-miner's lines come from the
/// slot: the state word, the last-minute reported rate as `hash_now`, the validated rate since the start as
/// `hash_avg`, the slot's counts, restarts and message; the proving fields (identities, ids, prepare) are gone with
/// the prover and stay at their defaults.
pub fn card_from(f: &SlotFacts, index: usize) -> CardState {
    let (vendor, worker) = vendor_words(&f.vendor, &f.backend);
    let failed = matches!(f.state, Some(SlotState::Faulted) | Some(SlotState::Refused));
    let mut c = CardState { index, ..CardState::default() };
    c.key = if f.fingerprint.is_empty() { f.id.clone() } else { f.fingerprint.clone() };
    c.code = f.id.clone();
    c.platform = f.runtime.clone();
    c.kind = f.kind.clone();
    c.vram_mb = f.memory_total / (1024 * 1024);
    c.reason = f.reason.clone();
    c.path = "prebuilt".into();
    c.name = f.name.clone();
    c.vendor = vendor.into();
    c.worker = worker.into();
    c.detail = if f.memory_total > 0 { format!("{} GB", f.memory_total / (1024 * 1024 * 1024)) } else { String::new() };
    c.device = f.index.to_string();
    c.enabled = f.enabled;
    c.state = state_word(f.state.as_ref(), f.enabled).into();
    c.hash_now = f.reported_60s_hps / 1e6;
    c.hash_10s = f.reported_10s_hps / 1e6;
    c.stint_s = f.stint_s;
    c.hash_avg = f.validated_hps / 1e6;
    c.rejected = f.invalid_results;
    c.restarts = f.restarts;
    c.message = f.message.clone();
    c.coord_word = f.coord_word.clone();
    c.lane_word = f.lane_word.clone();
    c.lanes = f.lanes.iter().map(|l| crate::state::LaneRow { coord: l.coord, word: l.word.clone(), accepted: l.accepted, hps: l.hps, seconds: l.seconds, current: l.current }).collect();
    c.problem = if failed { f.message.clone() } else { String::new() };
    c.bus = f.bus_id.clone();
    c
}

/// Every slot of the snapshot as the window's cards, in the supervisor's order.
pub fn cards_from(s: &Snapshot) -> Vec<CardState> {
    s.devices.iter().enumerate().map(|(i, v)| card_from(&SlotFacts::from(v), i)).collect()
}

/// The worker layer is on unless the operator keeps the old miner path for a comparison (IGNEUM_APP_OLD_MINER=1):
/// with it on, the engine plans no miner slots of its own, and the cards' live words come from the supervisor.
pub fn layer_on() -> bool {
    std::env::var("IGNEUM_APP_OLD_MINER").map(|v| v != "1").unwrap_or(true)
}

/// The node the supervisor's solo source reads until step 3 moves the choice into the engine's settings: the
/// operator's IGNEUM_APP_NODE_URL, else the engine's own node on its RPC port.
pub fn node_url(rpc_port: u16) -> String {
    std::env::var("IGNEUM_APP_NODE_URL").ok().filter(|u| !u.trim().is_empty()).unwrap_or_else(|| format!("http://127.0.0.1:{rpc_port}"))
}

/// The engine's work source as the supervisor takes it (docs/product-a/OLD-BASE.md step 3): "node" with a URL is
/// solo through that node; "pool" with host:port (an optional "tls" or "notls" suffix after a space; TLS by default)
/// is a pool the user typed; anything else is the node URL the engine would fall back to (IGNEUM_APP_NODE_URL or
/// its own RPC port) until the window's choice lands.
pub fn source_setting(kind: &str, url: &str, fallback_rpc_url: &str) -> Result<igneum_supervisor::settings::WorkSourceSetting, String> {
    use igneum_supervisor::settings::WorkSourceSetting;
    let url = url.trim();
    match kind {
        "node" if !url.is_empty() => Ok(WorkSourceSetting::Solo { rpc_url: url.to_string() }),
        "pool" if !url.is_empty() => {
            let (addr, tls) = match url.rsplit_once(' ') {
                Some((a, "notls")) => (a.trim(), false),
                Some((a, "tls")) => (a.trim(), true),
                _ => (url, true),
            };
            let (host, port) = addr.rsplit_once(':').ok_or_else(|| format!("the pool is host:port, not {addr:?}"))?;
            let port: u16 = port.parse().map_err(|_| format!("the pool's port is a number, not {port:?}"))?;
            if host.is_empty() { return Err("the pool's host is empty".into()); }
            // the chain pin moved (0.2.17, 7320 to 7322): a stored source that is exactly the old default pool (the
            // project's pool on the old chain's port) follows the pin to the new default; a pool the user typed stays
            if host == crate::config::DEFAULT_POOL_HOST && port == crate::config::PREVIOUS_DEFAULT_POOL_PORT && port != crate::config::DEFAULT_POOL_PORT {
                return Ok(WorkSourceSetting::PoolCustom { host: host.to_string(), port: crate::config::DEFAULT_POOL_PORT, tls: crate::config::DEFAULT_POOL_TLS });
            }
            Ok(WorkSourceSetting::PoolCustom { host: host.to_string(), port, tls })
        }
        "node" | "pool" => Err(format!("the {kind} source needs an address")),
        // no choice yet: the project's pool (Ember 0.2.1, the founder's order of 11 October 2026: a user who names
        // nothing mines through it; a node of their own is the alternative); the engine's own RPC port is no
        // source in this build (it runs no node)
        _ => {
            let _ = fallback_rpc_url;
            Ok(WorkSourceSetting::PoolCustom { host: crate::config::DEFAULT_POOL_HOST.to_string(), port: crate::config::DEFAULT_POOL_PORT, tls: crate::config::DEFAULT_POOL_TLS })
        }
    }
}

/// Give the supervisor the engine's payout address and its work source, the worker name from the engine's label
/// base. Returns the supervisor's refusal as text when the settings are not valid.
pub fn configure(sup: &Supervisor, address: &str, source: igneum_supervisor::settings::WorkSourceSetting, worker_name: &str) -> Result<(), String> {
    let address = address.trim().to_string();
    let worker_name = worker_name.to_string();
    sup.update_settings(move |s| {
        s.setup_done = true;
        s.payout_address = if address.is_empty() { None } else { Some(address.clone()) };
        s.work_source = source.clone();
        s.worker_name = worker_name.clone();
        Ok(())
    })
    .map_err(|e| e.to_string())
}

/// A card switched on or off in the window: the choice reaches the supervisor's settings by the card's key (the
/// slot's fingerprint, as the overlay keys the card); the supervisor applies its settings to the slots itself.
pub fn set_devices(sup: &Supervisor, changed: &[(String, bool)]) -> Result<(), String> {
    let changed: Vec<(String, bool)> = changed.to_vec();
    sup.update_settings(move |s| {
        for (key, enabled) in &changed {
            s.devices_on.retain(|f| f != key);
            s.devices_off.retain(|f| f != key);
            if *enabled { s.devices_on.push(key.clone()); } else { s.devices_off.push(key.clone()); }
        }
        Ok(())
    })
    .map_err(|e| e.to_string())
}

/// The slot each detected card stands on, by the overlay's rule (the same bus id, else the next slot of the same
/// vendor word in order): the card's index in `cards` to the slot's index in `slots`.
/// Whether a card's bus (nvidia-smi's pci.bus_id as detect.rs keeps it, "01:00.0") and a slot's bus id (the worker's
/// hello form, "00000000:01:00.0" or "0000:01:00.0" or "01:00.0") name one PCI address: both are read in the
/// supervisor's one spelling. the project's read of the three-card test machine's 0.2.4 capture, 19:2x UK, 12 October 2026: the two spellings
/// differed, so the join fell back to vendor order and the 5090's watts sat on the 4090's row.
pub fn same_bus(card_bus: &str, slot_bus: &str) -> bool {
    if card_bus.is_empty() || slot_bus.is_empty() {
        return false;
    }
    let (a, b) = (igneum_supervisor::devices::normalise_bus(card_bus), igneum_supervisor::devices::normalise_bus(slot_bus));
    a.len() >= 7 && a == b
}

/// The slot a card stands on: the same PCI bus (both spellings read as one); else, for an NVIDIA card, the slot whose
/// worker device id is the card's nvidia-smi index (the worker's CUDA order is pinned to the PCI order, which is
/// nvidia-smi's); else the next slot of the same vendor word in order. Never by position while a bus or an id exists.
pub fn slot_for(c: &CardState, slots: &[SlotFacts], taken: &[bool]) -> (Option<usize>, &'static str) {
    if let Some(i) = slots.iter().position(|f| same_bus(&c.bus, &f.bus_id)) {
        if !taken[i] { return (Some(i), "bus"); }
    }
    if c.vendor == "nvidia" && !c.device.is_empty() {
        if let Some(i) = slots.iter().position(|f| vendor_words(&f.vendor, &f.backend).0 == "nvidia" && f.id == c.device) {
            if !taken[i] { return (Some(i), "device id"); }
        }
    }
    (slots.iter().enumerate().position(|(i, f)| !taken[i] && vendor_words(&f.vendor, &f.backend).0 == c.vendor), "vendor order")
}

pub fn card_slots(cards: &[CardState], slots: &[SlotFacts]) -> Vec<Option<usize>> {
    let mut taken = vec![false; slots.len()];
    cards.iter().map(|c| {
        let (pick, _) = slot_for(c, slots, &taken);
        if let Some(i) = pick {
            taken[i] = true;
        }
        pick
    }).collect()
}

/// A card switch as the supervisor takes it: every choice (by the window's card key, `vendor:name`) mapped to its
/// slot's fingerprint with the enabled flag; a choice for a card without a slot is dropped. The mini's install of
/// 11 October 2026: the window's switch reached the supervisor only when the ENGINE's own enabled flag changed, and
/// under the engine's card key, which the supervisor does not know, so a feasibility-cohort card (the 16 GB Apple
/// M6, off by default in the supervisor) could not be turned on through the API at all. Every choice goes through,
/// by fingerprint.
pub fn device_keys_for(choices: &[(String, bool)], cards: &[CardState], slots: &[SlotFacts]) -> Vec<(String, bool)> {
    let on = card_slots(cards, slots);
    let mut out = Vec::new();
    for (key, enabled) in choices {
        let Some(ci) = cards.iter().position(|c| &c.key == key) else { continue };
        let Some(si) = on.get(ci).copied().flatten() else { continue };
        let f = &slots[si];
        let fp = if f.fingerprint.is_empty() { f.id.clone() } else { f.fingerprint.clone() };
        if !fp.is_empty() && !out.iter().any(|(k, _)| *k == fp) {
            out.push((fp, *enabled));
        }
    }
    out
}

/// The live words of the supervisor's slots laid over the engine's detected cards (the names, keys and preferences
/// stay the engine's: detect.rs and hot-plug own the list). A detected card takes the slot with the same bus id,
/// else the slot of the same vendor word in order; a card without a slot keeps its words. Returns the fleet's
/// state word for the window ("mining" when any card mines, "paused", "waiting") and the fleet rate.
pub fn overlay_snapshot(cards: &mut [CardState], s: &Snapshot) -> (&'static str, f64) {
    let slots: Vec<SlotFacts> = s.devices.iter().map(SlotFacts::from).collect();
    let paused = matches!(s.mining, igneum_supervisor::state::MiningState::Paused);
    overlay(cards, &slots, paused, s.totals.validated_hps)
}

/// The overlay on the slots' facts (the pure half, with the tests).
pub fn overlay(cards: &mut [CardState], slots: &[SlotFacts], paused: bool, fleet_validated_hps: f64) -> (&'static str, f64) {
    let mut taken = vec![false; slots.len()];
    for c in cards.iter_mut() {
        let (pick, how) = slot_for(c, slots, &taken);
        let Some(i) = pick else { continue };
        if c.slot_join != how {
            // one line per card when the join changes (at start, and on a re-detection): the proof that the rate, the
            // state and the switch of this row belong to the card nvidia-smi names on it; the engine logs it
            c.slot_join = how.to_string();
            c.slot_join_line = format!("card {} (bus {}, device {}) joined to worker slot {} (bus {}) by {how}", c.name, c.bus, c.device, slots[i].id, slots[i].bus_id);
        }
        taken[i] = true;
        let f = &slots[i];
        let fresh = card_from(f, c.index);
        c.state = if c.enabled { fresh.state.clone() } else { "off".into() };
        c.hash_now = fresh.hash_now;
        c.hash_10s = fresh.hash_10s;
        c.stint_s = fresh.stint_s;
        c.hash_avg = fresh.hash_avg;
        c.rejected = fresh.rejected;
        c.restarts = fresh.restarts;
        c.message = fresh.message.clone();
        c.coord_word = fresh.coord_word.clone();
        c.lane_word = fresh.lane_word.clone();
        c.lanes = fresh.lanes.clone();
        c.problem = fresh.problem.clone();
        c.device = fresh.device.clone();
        if c.vram_mb == 0 {
            c.vram_mb = fresh.vram_mb;
        }
    }
    let any_mining = cards.iter().any(|c| c.enabled && c.state == "mining");
    let word = if paused { "paused" } else if any_mining { "mining" } else { "waiting" };
    // F-E12: the fleet rate the tray and the tile show is the validated one once it exists; in the first minute of
    // mining, before any result is validated, the reported rate stands in (the founder's photo of 13 October 2026:
    // the menu bar at 0.0 MH/s beside a scene counting shares)
    let reported: f64 = cards.iter().filter(|c| c.enabled && c.state == "mining").map(|c| c.hash_now).sum();
    let fleet = if fleet_validated_hps > 0.0 { fleet_validated_hps / 1e6 } else { reported };
    (word, fleet)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn facts(state: SlotState, enabled: bool) -> SlotFacts {
        SlotFacts {
            id: "cuda:0".into(),
            name: "NVIDIA GeForce RTX 5060 Ti".into(),
            vendor: "NVIDIA".into(),
            backend: "cuda".into(),
            memory_total: 16 * 1024 * 1024 * 1024,
            enabled,
            state: Some(state),
            reported_60s_hps: 46_240_000.0,
            reported_10s_hps: 46_240_000.0,
            stint_s: 0.0,
            validated_hps: 45_900_000.0,
            restarts: 1,
            message: "the worker said bye".into(),
            coord_word: String::new(),
            lane_word: String::new(),
            lanes: Vec::new(),
            ..SlotFacts::default()
        }
    }

    #[test]
    fn a_card_takes_the_slot_on_its_bus_in_either_spelling_then_by_device_id_never_by_position() {
        // the three-card test machine (the project's read of the 0.2.4 capture, 12 October 2026): nvidia-smi lists the 5090 at index 0 on 01:00.0 and the
        // 4090 at index 1 on 02:00.0; the worker's hello names the buses in the long spelling and the devices in the
        // other order, so a join by position put the 5090's slot on the 4090's row
        let mut c5090 = CardState { index: 0, name: "NVIDIA GeForce RTX 5090".into(), vendor: "nvidia".into(), device: "0".into(), bus: "01:00.0".into(), ..Default::default() };
        let mut c4090 = CardState { index: 1, name: "NVIDIA GeForce RTX 4090".into(), vendor: "nvidia".into(), device: "1".into(), bus: "02:00.0".into(), ..Default::default() };
        let slot = |id: &str, bus: &str| SlotFacts { id: id.into(), bus_id: bus.into(), backend: "cuda".into(), vendor: "nvidia".into(), ..Default::default() };
        let slots = vec![slot("1", "00000000:02:00.0"), slot("0", "00000000:01:00.0")];
        assert!(same_bus("01:00.0", "00000000:01:00.0") && same_bus("02:00.0", "0000:02:00.0") && !same_bus("01:00.0", "02:00.0") && !same_bus("", "0"));
        assert_eq!(card_slots(&[c5090.clone(), c4090.clone()], &slots), vec![Some(1), Some(0)], "by bus, whatever the slot order");
        // a worker whose hello names no bus: the device id stands in for it (the CUDA order pinned to the PCI order)
        let slots_no_bus = vec![slot("1", "1"), slot("0", "0")];
        assert_eq!(card_slots(&[c5090.clone(), c4090.clone()], &slots_no_bus), vec![Some(1), Some(0)], "by device id");
        // neither: the vendor's slots in order, and the join says so
        let slots_plain = vec![slot("x", ""), slot("y", "")];
        assert_eq!(card_slots(&[c5090.clone(), c4090.clone()], &slots_plain), vec![Some(0), Some(1)]);
        let mut cards = vec![c5090.clone(), c4090.clone()];
        overlay(&mut cards, &slots, false, 0.0);
        assert_eq!(cards[0].slot_join, "bus"); assert!(cards[0].slot_join_line.contains("joined to worker slot 0"));
        c5090.slot_join.clear(); c4090.slot_join.clear();
        let _ = (c5090, c4090);
    }

    #[test]
    fn a_card_switch_reaches_the_supervisor_by_its_slots_fingerprint() {
        // the mini, 11 October 2026: the window's card is keyed "apple:Apple M6" and the supervisor's slot by its
        // fingerprint; the switch maps by the overlay's rule (bus, else vendor in order), every choice, on or off
        let mut apple = facts(SlotState::Idle, false);
        apple.vendor = "apple".into();
        apple.backend = "metal".into();
        apple.id = "4294968627".into();
        apple.fingerprint = "9d1588caf5be7962d78e7893abd24699ad45d2f1226be2a56d5cea891c208a6b".into();
        let mut card = CardState::default();
        card.key = "apple:Apple M6".into();
        card.vendor = "apple".into();
        card.name = "Apple M6".into();
        let cards = vec![card];
        let slots = vec![apple.clone()];
        assert_eq!(device_keys_for(&[("apple:Apple M6".into(), true)], &cards, &slots), vec![(apple.fingerprint.clone(), true)]);
        assert_eq!(device_keys_for(&[("apple:Apple M6".into(), false)], &cards, &slots), vec![(apple.fingerprint.clone(), false)]);
        // a choice for a card the supervisor has no slot for is dropped, never sent under the engine's key
        assert!(device_keys_for(&[("nvidia:RTX 4070".into(), true)], &cards, &slots).is_empty());
        // two NVIDIA cards take their slots by bus id, whatever the order
        let mut a = facts(SlotState::Idle, true);
        a.bus_id = "0000:11:00.0".into();
        a.fingerprint = "fp-4070".into();
        let mut b = facts(SlotState::Idle, true);
        b.bus_id = "0000:01:00.0".into();
        b.fingerprint = "fp-5090".into();
        let mut c1 = CardState::default();
        c1.key = "nvidia:NVIDIA GeForce RTX 5090".into();
        c1.vendor = "nvidia".into();
        c1.bus = "0000:01:00.0".into();
        let mut c2 = CardState::default();
        c2.key = "nvidia:NVIDIA GeForce RTX 4070".into();
        c2.vendor = "nvidia".into();
        c2.bus = "0000:11:00.0".into();
        let keys = device_keys_for(&[(c2.key.clone(), true), (c1.key.clone(), false)], &[c1.clone(), c2.clone()], &[a, b]);
        assert_eq!(keys, vec![("fp-4070".to_string(), true), ("fp-5090".to_string(), false)]);
    }

    #[test]
    fn the_state_words_are_the_windows_seven() {
        assert_eq!(state_word(Some(&SlotState::Mining), true), "mining");
        assert_eq!(state_word(Some(&SlotState::Tuning), true), "mining");
        assert_eq!(state_word(Some(&SlotState::Opening), true), "starting");
        assert_eq!(state_word(Some(&SlotState::Preparing), true), "starting");
        assert_eq!(state_word(Some(&SlotState::Idle), true), "waiting");
        assert_eq!(state_word(Some(&SlotState::Paused), true), "ready");
        assert_eq!(state_word(Some(&SlotState::Faulted), true), "stopped");
        assert_eq!(state_word(Some(&SlotState::Refused), true), "failed");
        assert_eq!(state_word(Some(&SlotState::Mining), false), "off", "a disabled card is off whatever its slot says");
        assert_eq!(state_word(None, true), "off");
        for s in [SlotState::Off, SlotState::Idle, SlotState::Opening, SlotState::Preparing, SlotState::Mining, SlotState::Paused, SlotState::Tuning, SlotState::Refused, SlotState::Faulted] {
            assert!(["off", "waiting", "starting", "ready", "mining", "stopped", "failed"].contains(&state_word(Some(&s), true)));
        }
    }

    #[test]
    fn a_slot_becomes_the_windows_card() {
        let c = card_from(&facts(SlotState::Mining, true), 0);
        assert_eq!((c.vendor.as_str(), c.worker.as_str(), c.state.as_str()), ("nvidia", "CUDA", "mining"));
        assert_eq!(c.vram_mb, 16 * 1024);
        assert!((c.hash_now - 46.24).abs() < 1e-9 && (c.hash_avg - 45.9).abs() < 1e-9, "MH/s on the card, H/s in the slot");
        assert_eq!(c.restarts, 1);
        assert_eq!(c.problem, "", "a mining card carries no problem word");
        assert_eq!(c.key, "cuda:0", "the id stands in for the key until the worker's fingerprint is known");
        let f = card_from(&facts(SlotState::Faulted, true), 2);
        assert_eq!((f.index, f.state.as_str(), f.problem.as_str()), (2, "stopped", "the worker said bye"));
    }

    #[test]
    fn the_vendor_words_follow_the_backend() {
        assert_eq!(vendor_words("Apple", "metal"), ("apple", "Metal"));
        assert_eq!(vendor_words("Advanced Micro Devices, Inc.", "opencl"), ("amd", "OpenCL"));
        assert_eq!(vendor_words("GenuineIntel", "cpu"), ("other", "CPU"));
        assert_eq!(vendor_words("", "cuda"), ("nvidia", "CUDA"));
    }

    #[test]
    fn the_overlay_keeps_the_engines_names_and_takes_the_slots_words() {
        let slots = vec![SlotFacts { bus_id: "0000:01:00.0".into(), reported_60s_hps: 46_000_000.0, validated_hps: 45_000_000.0, ..facts(SlotState::Mining, true) }];
        let mut cards = vec![
            CardState { index: 0, name: "NVIDIA GeForce RTX 5060 Ti".into(), vendor: "nvidia".into(), bus: "0000:01:00.0".into(), enabled: true, state: "waiting".into(), ..CardState::default() },
            CardState { index: 1, name: "AMD Radeon RX 9070 XT".into(), vendor: "amd".into(), enabled: true, state: "waiting".into(), ..CardState::default() },
        ];
        let (word, rate) = overlay(&mut cards, &slots, false, 45_000_000.0);
        assert_eq!(cards[0].name, "NVIDIA GeForce RTX 5060 Ti", "the engine's name stays");
        assert_eq!((cards[0].state.as_str(), cards[0].hash_now), ("mining", 46.0));
        assert_eq!(cards[1].state, "waiting", "a card without a slot keeps its words");
        assert_eq!((word, rate), ("mining", 45.0));
        cards[0].enabled = false;
        let (word, _) = overlay(&mut cards, &slots, false, 45_000_000.0);
        assert_eq!((cards[0].state.as_str(), word), ("off", "waiting"), "a disabled card is off and the fleet word follows");
        assert_eq!(overlay(&mut cards, &slots, true, 0.0).0, "paused");
    }

    #[test]
    fn the_layer_and_node_defaults() {
        assert_eq!(node_url(26611), "http://127.0.0.1:26611");
    }

    #[test]
    fn the_data_directory_is_apart_from_the_engines_files() {
        assert_eq!(data_dir(Path::new("/x/app")), PathBuf::from("/x/app/ember"));
    }
}
