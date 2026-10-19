//! Hot-plug: what changed between two enumerations of the cards (src/detect.rs runs one at start and one every
//! minute; the Windows host also asks for one on WM_DEVICECHANGE). Pure, so the rules are unit-tested here; the
//! engine applies the result (start a worker, stop one, mark a row). Born 5 October 2026, when an RX 9070 XT went
//! into the first Windows machine through an eGPU box while the app ran and nothing noticed.
//!
//! Rules: a card is the same card when its key (vendor:code, "#2" for a twin) matches and the PCI addresses do not
//! disagree, or, failing that, when vendor and name match and that pair is unique on both sides (a twin whose
//! ordinal moved because the first one left). The device index is never part of the identity: it moves. A
//! card missing from a list is removed only when the tool that lists its vendor answered. Removed and faulty cards
//! stay in the engine's list (the other cards' indices are the miner slots), marked, and the dashboard hides a
//! removed row after five minutes.

use crate::config::CardPref;
use crate::state::CardState;

/// How long a removed card's row says "removed" before it hides.
pub const REMOVED_SHOWN_S: f64 = 300.0;
/// How often the engine enumerates again, seconds (macOS has no GPU hot-plug on Apple silicon: slower there).
pub const POLL_S: u64 = if cfg!(target_os = "macos") { 300 } else { 60 };
/// How often the app log carries the full card list, seconds (the console reads it from the log tail).
pub const CARDS_LINE_S: u64 = 600;

#[derive(Default, Debug)]
pub struct Diff {
    /// new cards (usable or with a problem), to be appended
    pub added: Vec<CardState>,
    /// cards that were marked removed earlier and are listed again: (slot, the fresh entry)
    pub revived: Vec<(usize, CardState)>,
    /// slots whose card is gone
    pub removed: Vec<usize>,
    /// slots whose card now reports a problem: (slot, "Code 43")
    pub errored: Vec<(usize, String)>,
    /// slots whose card had a problem and is now driven by a tool again: (slot, the fresh entry)
    pub recovered: Vec<(usize, CardState)>,
    /// slots whose card is the same but its device index (or memory, bus) changed: (slot, the fresh entry)
    pub moved: Vec<(usize, CardState)>,
    pub unchanged: Vec<usize>,
}

impl Diff {
    /// Nothing to do: every present card is where it was.
    pub fn is_quiet(&self) -> bool {
        self.added.is_empty() && self.revived.is_empty() && self.removed.is_empty() && self.errored.is_empty() && self.recovered.is_empty() && self.moved.is_empty()
    }
}

fn bus_compat(a: &CardState, b: &CardState) -> bool {
    a.bus.is_empty() || b.bus.is_empty() || a.bus == b.bus
}

fn same_identity(a: &CardState, b: &CardState) -> bool {
    a.vendor == b.vendor && (a.name.trim().eq_ignore_ascii_case(b.name.trim()) || (!a.code.is_empty() && a.code.eq_ignore_ascii_case(&b.code))) && bus_compat(a, b)
}

/// Compares the engine's list with a fresh enumeration. `listed(card)` says whether this enumeration's tools could
/// have seen that card (Detection::listed); a card its tool did not answer for is kept, not removed.
pub fn diff(old: &[CardState], fresh: &[CardState], listed: &dyn Fn(&CardState) -> bool) -> Diff {
    let mut out = Diff::default();
    let mut used = vec![false; fresh.len()];
    let mut pair: Vec<Option<usize>> = vec![None; old.len()];
    // exact keys first
    for (i, o) in old.iter().enumerate() {
        if let Some(j) = fresh.iter().enumerate().position(|(j, f)| !used[j] && f.key == o.key && bus_compat(o, f)) {
            used[j] = true;
            pair[i] = Some(j);
        }
    }
    // then vendor + name, when that pair is unique among what is still unmatched on both sides
    for (i, o) in old.iter().enumerate() {
        if pair[i].is_some() {
            continue;
        }
        let cands: Vec<usize> = fresh.iter().enumerate().filter(|(j, f)| !used[*j] && same_identity(o, f)).map(|(j, _)| j).collect();
        let twins = old.iter().enumerate().filter(|(k, x)| pair[*k].is_none() && *k != i && same_identity(o, x)).count();
        if cands.len() == 1 && twins == 0 {
            used[cands[0]] = true;
            pair[i] = Some(cands[0]);
        }
    }
    for (i, o) in old.iter().enumerate() {
        match pair[i] {
            Some(j) => {
                let f = &fresh[j];
                if o.removed_at > 0.0 {
                    out.revived.push((i, f.clone()));
                } else if o.problem.is_empty() && !f.problem.is_empty() {
                    out.errored.push((i, f.problem.clone()));
                } else if !o.problem.is_empty() && f.problem.is_empty() {
                    out.recovered.push((i, f.clone()));
                } else if !o.problem.is_empty() {
                    // still faulty: the same problem or a new code, nothing to start or stop
                    if o.problem != f.problem {
                        out.errored.push((i, f.problem.clone()));
                    } else {
                        out.unchanged.push(i);
                    }
                } else if o.device != f.device || o.key != f.key {
                    out.moved.push((i, f.clone()));
                } else {
                    out.unchanged.push(i);
                }
            }
            None => {
                if o.removed_at > 0.0 {
                    // already removed: stays hidden or shown as removed
                    out.unchanged.push(i);
                } else if listed(o) {
                    out.removed.push(i);
                } else {
                    out.unchanged.push(i);
                }
            }
        }
    }
    for (j, f) in fresh.iter().enumerate() {
        if !used[j] {
            out.added.push(f.clone());
        }
    }
    out
}

/// The saved choice for a card: by its key (vendor:code), else a key saved by an app before an earlier release (vendor:index:code,
/// vendor:index:name) when exactly one matches; an index that moved never changes the answer.
pub fn pref_for<'a>(prefs: &'a std::collections::HashMap<String, CardPref>, c: &CardState) -> Option<&'a CardPref> {
    if let Some(p) = prefs.get(&c.key) {
        return Some(p);
    }
    if c.key.contains('#') {
        return None; // a twin's choice is its own
    }
    let head = format!("{}:", c.vendor);
    for tail in [format!(":{}", c.code), format!(":{}", c.name)] {
        if tail.len() <= 1 {
            continue;
        }
        let found: Vec<&CardPref> = prefs.iter().filter(|(k, _)| k.starts_with(&head) && k.ends_with(&tail) && !k.contains('#')).map(|(_, p)| p).collect();
        if found.len() == 1 {
            return Some(found[0]);
        }
    }
    None
}

/// Applies a saved choice to a freshly detected card (the first detection and every later one use this).
pub fn apply_pref(c: &mut CardState, p: &CardPref) {
    if !c.problem.is_empty() {
        return;
    }
    c.enabled = p.enabled && c.kind != "unknown";
    c.identities = p.identities.clamp(1, 64);
    if c.enabled || c.kind != "integrated" {
        c.reason = String::new();
    }
    if c.vendor == "nvidia" && p.power_pct > 0 {
        c.power_pct = p.power_pct.clamp(crate::sweep::MIN_PCT, 100);
    }
    c.pinned = p.pinned;
    c.sweep_pct = p.sweep_pct;
    c.sweep_eff = p.sweep_eff;
    c.sweep_watts = p.sweep_watts;
    c.sweep_mhs = p.sweep_mhs;
    c.sweep_at = p.sweep_at as f64;
    // Ember Tune (src/ember.rs): the clock cap the last tune chose, its plan, and the row's Tuned line
    c.tune_clock_mhz = p.sweep_clock_mhz;
    c.tune_mem_mhz = p.sweep_mem_mhz;
    c.mem_cap_mhz = if p.pinned || p.sweep_source == "baseline" { 0 } else { p.sweep_mem_mhz };
    c.clock_cap_mhz = if p.pinned || p.sweep_source == "baseline" { 0 } else { p.sweep_clock_mhz };
    c.tune_source = p.sweep_source.clone();
    c.tune_goal = p.tune_goal.clone();
    // Ember Tune tiers (8 October 2026): the tier in force and the class the set was measured under survive a restart
    c.tier = p.tier.clone();
    c.tier_at = p.tier_at as f64;
    c.tiers_class = p.tiers_class.clone();
    // tiers-table-26 (8 October 2026): the card's own measured rows when the pref kept them, else the team's table seeds
    // the three tabs for this card class until the search runs (the rows carry table: true, the class the table is for)
    if !p.tiers.is_empty() {
        c.tiers = p.tiers.clone();
        c.tiers_table = false;
    } else {
        seed_from_table(c);
    }
    c.tune_before_watts = p.sweep_before_watts;
    c.tune_before_mhs = p.sweep_before_mhs;
    c.tune_floor = p.sweep_floor;
    if p.sweep_mhs > 0.0 && p.sweep_watts > 0.0 {
        c.tune_line = crate::ember::tuned_line(p.sweep_mhs, p.sweep_watts, p.sweep_eff);
    }
}

/// The team's table seeds a card with no measured set of its own (tiers-table-26): the rows in the apply shape, the
/// table's class as the set's class (a card on another program class reads them stale and re-measures), the no-lever
/// note for a class the table carries as stock only. A name the table does not know gets nothing.
pub fn seed_from_table(c: &mut CardState) {
    // 0.2.3 (the founder's order of 12 October 2026): no table value anywhere; a card's rate and power are measured
    // on this relation or shown as "not measured yet" (the window's unmeasured set). The table stays for the AMD
    // clock offsets (engine.rs, the knob) and seeds no tiers.
    if true {
        return;
    }
    if !c.tiers.is_empty() || c.name.is_empty() {
        return;
    }
    let Some(seed) = crate::tiertable::seed(&c.name) else { return };
    c.tiers = seed.rows;
    c.tiers_table = true;
    c.tiers_class = seed.class;
    if c.tier.is_empty() {
        c.tier = String::new(); // the fleet tier applies until a tap or the search picks one for this card
    }
    if c.tiers.len() == 1 && c.vendor != "nvidia" && c.vendor != "amd" {
        c.tier_note = if c.vendor == "apple" { "no lever: Apple silicon sets its own clocks and power".into() } else { "no lever: no clock or power control for this card".into() };
        c.tier = "max".into();
    }
}

/// A card a re-detection added (or brought back): the saved choice if there is one, else the detect defaults it
/// came with; its slot and the time it appeared.
pub fn settle_new(c: &mut CardState, index: usize, pref: Option<&CardPref>, now: f64) {
    c.index = index;
    c.added_at = now;
    c.removed_at = 0.0;
    c.gone = false;
    if let Some(p) = pref {
        apply_pref(c, p);
    } else {
        seed_from_table(c);
    }
    if c.problem.is_empty() {
        c.state = if c.enabled { "waiting".into() } else { "off".into() };
    }
}

/// Marks a card unplugged: no worker, the row says removed, the saved choice is untouched.
pub fn mark_removed(c: &mut CardState, now: f64) {
    c.removed_at = now;
    c.gone = false;
    c.state = "removed".into();
    c.message = "removed".into();
    c.hash_now = 0.0;
    c.pid = 0;
    c.restart_in_s = 0;
    c.ready = false;
    c.prepared = false;
}

/// A removed row hides after REMOVED_SHOWN_S; returns true when something changed.
pub fn age(cards: &mut [CardState], now: f64) -> bool {
    let mut changed = false;
    for c in cards.iter_mut() {
        let gone = c.removed_at > 0.0 && now - c.removed_at >= REMOVED_SHOWN_S;
        if gone != c.gone {
            c.gone = gone;
            changed = true;
        }
    }
    changed
}

/// The event line for a card that appeared: "New card: <name>, mining" and its kind (ok | info | warn).
pub fn added_words(c: &CardState) -> (&'static str, String) {
    if !c.problem.is_empty() {
        ("warn", format!("New card: {}, not usable ({}); {}", c.name, c.problem, crate::detect::PROBLEM_HINT))
    } else if c.enabled {
        ("ok", format!("New card: {}, mining", c.name))
    } else if c.kind == "integrated" {
        ("info", format!("New card: {}, off ({})", c.name, crate::detect::INTEGRATED_REASON))
    } else {
        ("info", format!("New card: {}, off (switched off in settings)", c.name))
    }
}

/// One word for a card's state on the log line and the console: mining, waiting, off, removed, not usable (Code 43).
pub fn state_word(c: &CardState) -> String {
    if c.removed_at > 0.0 {
        "removed".into()
    } else if !c.problem.is_empty() {
        format!("not usable ({})", c.problem)
    } else if !c.enabled {
        "off".into()
    } else {
        c.state.clone()
    }
}

/// The app-log line the console reads (relay/lib/parse.mjs): `cards: <name> [<kind>, <state>] | ...`, hidden rows
/// left out, `cards: none` when nothing is listed.
pub fn cards_line(cards: &[CardState]) -> String {
    let parts: Vec<String> = cards.iter().filter(|c| !c.gone).map(|c| format!("{} [{}, {}]", c.name.replace('|', "/").replace('[', "(").replace(']', ")"), c.kind, state_word(c))).collect();
    if parts.is_empty() { "cards: none".into() } else { format!("cards: {}", parts.join(" | ")) }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::detect::{classify_kind, mark_unusable, INTEGRATED_REASON};

    fn card(name: &str, vendor: &str, device: &str) -> CardState {
        let mut c = CardState { index: 0, key: format!("{vendor}:{name}"), code: name.into(), name: name.into(), vendor: vendor.into(), worker: if vendor == "nvidia" { "CUDA".into() } else { "OpenCL".into() }, device: device.into(), enabled: true, state: "off".into(), ..Default::default() };
        c.kind = classify_kind(name, None).into();
        crate::detect::apply_defaults(&mut c);
        c
    }
    // the first Windows machine at start on 5 October 2026: the 5090 on nvidia-smi index 0, the Ryzen iGPU as OpenCL device 0 (gfx1036)
    fn pc1_start() -> Vec<CardState> {
        let mut a = card("NVIDIA GeForce RTX 5090", "nvidia", "0");
        a.bus = "00000000:01:00.0".into();
        a.state = "mining".into();
        let mut b = card("gfx1036", "amd", "0");
        b.index = 1;
        vec![a, b]
    }
    fn all_listed(_: &CardState) -> bool {
        true
    }
    fn fresh_pc1_with_egpu() -> Vec<CardState> {
        let mut v = pc1_start();
        v[0].state = "off".into();
        let mut e = card("gfx1201", "amd", "1");
        e.index = 2;
        v.push(e);
        v
    }

    #[test]
    fn unchanged_list_is_quiet() {
        let old = pc1_start();
        let mut fresh = pc1_start();
        fresh[0].state = "off".into(); // runtime state in the fresh list means nothing
        let d = diff(&old, &fresh, &all_listed);
        assert!(d.is_quiet(), "{d:?}");
        assert_eq!(d.unchanged, vec![0, 1]);
    }

    #[test]
    fn a_new_usable_card_is_added_and_nothing_else_moves() {
        let old = pc1_start();
        let d = diff(&old, &fresh_pc1_with_egpu(), &all_listed);
        assert_eq!(d.added.len(), 1);
        assert_eq!(d.added[0].name, "gfx1201");
        assert_eq!(d.added[0].kind, "discrete");
        assert!(d.added[0].enabled);
        assert_eq!(d.unchanged, vec![0, 1]);
        assert!(d.removed.is_empty() && d.moved.is_empty() && d.errored.is_empty());
    }

    #[test]
    fn a_new_card_with_a_problem_is_added_as_unusable() {
        let old = pc1_start();
        let mut fresh = pc1_start();
        let mut bad = card("AMD Radeon RX 9070 XT", "amd", "");
        mark_unusable(&mut bad, "Code 43");
        fresh.push(bad);
        let d = diff(&old, &fresh, &all_listed);
        assert_eq!(d.added.len(), 1);
        assert_eq!(d.added[0].problem, "Code 43");
        assert!(!d.added[0].enabled);
        let (kind, text) = added_words(&d.added[0]);
        assert_eq!(kind, "warn");
        assert!(text.starts_with("New card: AMD Radeon RX 9070 XT, not usable (Code 43); reboot with the card attached"), "{text}");
        assert_eq!(state_word(&d.added[0]), "not usable (Code 43)");
    }

    #[test]
    fn an_unplugged_card_is_removed_only_when_its_tool_answered() {
        let old = fresh_pc1_with_egpu();
        let fresh = pc1_start();
        let d = diff(&old, &fresh, &all_listed);
        assert_eq!(d.removed, vec![2]);
        assert_eq!(d.unchanged, vec![0, 1]);
        // the OpenCL worker did not answer this round: nothing is called removed
        let opencl_dead = |c: &CardState| c.vendor == "nvidia";
        let d2 = diff(&old, &pc1_start().into_iter().filter(|c| c.vendor == "nvidia").collect::<Vec<_>>(), &opencl_dead);
        assert!(d2.removed.is_empty(), "{d2:?}");
        assert!(d2.is_quiet());
    }

    #[test]
    fn a_card_whose_index_moved_keeps_its_slot() {
        // the eGPU landed before the iGPU in the OpenCL list: the iGPU is device 1 now, the eGPU device 0
        let old = pc1_start();
        let mut fresh = pc1_start();
        fresh[1].device = "1".into();
        let mut e = card("gfx1201", "amd", "0");
        e.index = 2;
        fresh.push(e);
        let d = diff(&old, &fresh, &all_listed);
        assert_eq!(d.moved.len(), 1);
        assert_eq!(d.moved[0].0, 1);
        assert_eq!(d.moved[0].1.device, "1");
        assert_eq!(d.added.len(), 1);
        assert!(d.removed.is_empty());
    }

    #[test]
    fn two_identical_cards_are_told_apart_by_key_and_never_swapped() {
        let mut a = card("NVIDIA GeForce RTX 5090", "nvidia", "0");
        let mut b = card("NVIDIA GeForce RTX 5090", "nvidia", "1");
        b.index = 1;
        b.key = "nvidia:NVIDIA GeForce RTX 5090#2".into();
        a.bus = "01:00.0".into();
        b.bus = "02:00.0".into();
        let old = vec![a.clone(), b.clone()];
        // the second twin leaves: the first keeps its slot by key; the missing one is removed, not "moved"
        let d = diff(&old, &[a.clone()], &all_listed);
        assert_eq!(d.unchanged, vec![0]);
        assert_eq!(d.removed, vec![1]);
        // the FIRST twin leaves: the survivor is now index 0 with the unsuffixed key, but its bus says which card it
        // is, so slot 1 is "moved" (new key and device) and slot 0 is removed; no worker is swapped between cards
        let mut survivor = b.clone();
        survivor.device = "0".into();
        survivor.key = "nvidia:NVIDIA GeForce RTX 5090".into();
        let d2 = diff(&old, &[survivor], &all_listed);
        assert_eq!(d2.removed, vec![0]);
        assert_eq!(d2.moved.len(), 1);
        assert_eq!(d2.moved[0].0, 1);
        assert_eq!(d2.moved[0].1.key, "nvidia:NVIDIA GeForce RTX 5090");
        // the same two cards again, nothing changed: quiet
        let d3 = diff(&old, &old, &all_listed);
        assert!(d3.is_quiet(), "{d3:?}");
    }

    #[test]
    fn a_driven_card_that_turns_faulty_is_errored_and_recovers_later() {
        let old = fresh_pc1_with_egpu();
        // the eGPU is still listed by Windows, now with Code 43, and no longer by OpenCL
        let mut fresh = pc1_start();
        let mut bad = card("gfx1201", "amd", "");
        mark_unusable(&mut bad, "Code 43");
        fresh.push(bad);
        let d = diff(&old, &fresh, &all_listed);
        assert_eq!(d.errored, vec![(2, "Code 43".to_string())]);
        assert!(d.added.is_empty() && d.removed.is_empty());
        // after a reboot with the card attached it is driven again: recovered, same slot
        let mut faulty = old.clone();
        mark_unusable(&mut faulty[2], "Code 43");
        faulty[2].device = String::new();
        let d2 = diff(&faulty, &fresh_pc1_with_egpu(), &all_listed);
        assert_eq!(d2.recovered.len(), 1);
        assert_eq!(d2.recovered[0].0, 2);
        assert!(d2.recovered[0].1.problem.is_empty());
        // the same problem again next minute: quiet
        let d3 = diff(&faulty, &fresh, &all_listed);
        assert!(d3.is_quiet(), "{d3:?}");
    }

    #[test]
    fn a_removed_card_that_comes_back_is_revived_in_its_slot() {
        let mut old = fresh_pc1_with_egpu();
        mark_removed(&mut old[2], 1000.0);
        assert_eq!(state_word(&old[2]), "removed");
        // still absent: quiet (and hidden after five minutes)
        let d = diff(&old, &pc1_start(), &all_listed);
        assert!(d.is_quiet(), "{d:?}");
        assert!(age(&mut old, 1000.0 + REMOVED_SHOWN_S));
        assert!(old[2].gone);
        assert!(!age(&mut old, 1000.0 + REMOVED_SHOWN_S + 1.0));
        // back: revived in slot 2
        let d2 = diff(&old, &fresh_pc1_with_egpu(), &all_listed);
        assert_eq!(d2.revived.len(), 1);
        assert_eq!(d2.revived[0].0, 2);
        assert!(d2.added.is_empty());
        let mut back = d2.revived[0].1.clone();
        settle_new(&mut back, 2, None, 2000.0);
        assert_eq!(back.index, 2);
        assert_eq!(back.removed_at, 0.0);
        assert!(!back.gone);
        assert_eq!(back.added_at, 2000.0);
        assert_eq!(back.state, "waiting");
    }

    #[test]
    fn the_users_choice_is_kept_on_a_new_card_and_an_integrated_one_is_off_by_default() {
        let mut prefs = std::collections::HashMap::new();
        // the user switched the iGPU on earlier and set 2 identities; the setting was saved under OpenCL index 0
        prefs.insert("amd:0:gfx1036".to_string(), CardPref { enabled: true, identities: 2, ..Default::default() });
        // the iGPU comes back as device 1 (the eGPU took index 0): the an earlier release key still answers, by vendor and code
        let mut igpu = card("gfx1036", "amd", "1");
        assert_eq!(igpu.kind, "integrated");
        assert!(!igpu.enabled);
        assert_eq!(igpu.reason, INTEGRATED_REASON);
        let p = pref_for(&prefs, &igpu).cloned();
        assert!(p.is_some());
        settle_new(&mut igpu, 1, p.as_ref(), 5.0);
        assert!(igpu.enabled);
        assert_eq!(igpu.identities, 2);
        assert_eq!(igpu.reason, "");
        assert_eq!(igpu.state, "waiting");
        // the user switched it off (saved under the index-free key): the row keeps the integrated words
        prefs.insert("amd:gfx1036".to_string(), CardPref { enabled: false, identities: 1, ..Default::default() });
        let mut igpu2 = card("gfx1036", "amd", "1");
        let p2 = pref_for(&prefs, &igpu2).cloned();
        settle_new(&mut igpu2, 1, p2.as_ref(), 6.0);
        assert!(!igpu2.enabled);
        assert_eq!(igpu2.reason, INTEGRATED_REASON);
        assert_eq!(igpu2.state, "off");
        let (kind, text) = added_words(&igpu2);
        assert_eq!(kind, "info");
        assert_eq!(text, format!("New card: gfx1036, off ({INTEGRATED_REASON})"));
        // no saved choice: the detect default (integrated off, discrete on)
        let mut egpu = card("gfx1201", "amd", "0");
        settle_new(&mut egpu, 2, None, 7.0);
        assert!(egpu.enabled);
        assert_eq!(added_words(&egpu), ("ok", "New card: gfx1201, mining".to_string()));
        // a pref never switches on a card with a problem
        let mut bad = card("AMD Radeon RX 9070 XT", "amd", "");
        mark_unusable(&mut bad, "Code 43");
        settle_new(&mut bad, 3, Some(&CardPref { enabled: true, identities: 8, ..Default::default() }), 8.0);
        assert!(!bad.enabled);
        assert_eq!(bad.state, "unusable");
        // old keys only, two of them for the same code (the five-row the first Windows machine list had amd:0:gfx1036 and amd:2:gfx1036):
        // ambiguous, so the default applies; the index-free key, once saved, always wins
        prefs.remove("amd:gfx1036");
        prefs.insert("amd:2:gfx1036".to_string(), CardPref { enabled: true, identities: 3, ..Default::default() });
        let other = card("gfx1036", "amd", "7");
        assert!(pref_for(&prefs, &other).is_none());
        prefs.insert("amd:gfx1036".to_string(), CardPref { enabled: true, identities: 4, ..Default::default() });
        assert_eq!(pref_for(&prefs, &other).map(|p| p.identities), Some(4));
        // a twin never borrows the first card's choice
        let mut twin = card("gfx1201", "amd", "3");
        twin.key = "amd:gfx1201#2".into();
        prefs.insert("amd:gfx1201".to_string(), CardPref { enabled: false, identities: 1, ..Default::default() });
        assert!(pref_for(&prefs, &twin).is_none());
        // the name on the row is the Windows name while the key keeps the code: the an earlier release key by code still answers
        let mut named = card("gfx1201", "amd", "1");
        named.name = "AMD Radeon RX 9070 XT".into();
        prefs.clear();
        prefs.insert("amd:1:gfx1201".to_string(), CardPref { enabled: false, identities: 2, ..Default::default() });
        assert_eq!(pref_for(&prefs, &named).map(|p| p.identities), Some(2));
    }

    #[test]
    fn the_console_line_lists_every_shown_card_with_kind_and_state() {
        let mut cards = fresh_pc1_with_egpu();
        cards[0].state = "mining".into();
        cards[2].state = "starting".into();
        let mut bad = card("AMD Radeon RX 9070 XT", "amd", "");
        mark_unusable(&mut bad, "Code 43");
        cards.push(bad);
        assert_eq!(cards_line(&cards), "cards: NVIDIA GeForce RTX 5090 [discrete, mining] | gfx1036 [integrated, off] | gfx1201 [discrete, starting] | AMD Radeon RX 9070 XT [discrete, not usable (Code 43)]");
        mark_removed(&mut cards[2], 10.0);
        assert!(cards_line(&cards).contains("gfx1201 [discrete, removed]"));
        age(&mut cards, 10.0 + REMOVED_SHOWN_S);
        assert!(!cards_line(&cards).contains("gfx1201"));
        assert_eq!(cards_line(&[]), "cards: none");
    }
}
