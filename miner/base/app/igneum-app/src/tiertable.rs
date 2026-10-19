//! The team's tiers table (tiers-table-26, 8 October 2026, the project's order: the denominator lane's measured table,
//! app/igneum-app/tiers/class-v5-tiers.json, one entry per card class with the three tiers in the shape
//! src/ember.rs tier_from_json applies). A card whose own search has not run yet takes its class's rows from here, so
//! the three tabs show rate and watts from the first minute and a tap applies the table's point at once; the card's
//! own search (2 min into steady mining, weekly, on Tune, after a class flip) replaces the rows with its measured ones.
//! The rows carry `table: true` and the class's `label` ("measured" on a card the team measured, "estimated" for a
//! class read from the record) so the window says where they came from. A class with no lever (Apple, Intel) carries
//! the max row only. An AMD row carries the core offset and the power offset the lane measured; the apply path turns
//! the offset into the absolute clock the knob sends once the card's stock clock is known.
//! The match rule is the lane's (tiers/class-v5-tiers.mjs entryFor): the longest match string found in the card's
//! name wins, so "5070 Ti" beats "5070". The file is validated by tiers/class-v5-tiers.test.mjs in the gate.
use serde_json::{json, Value};
use std::sync::OnceLock;

const TABLE: &str = include_str!("../tiers/class-v5-tiers.json");

#[allow(dead_code)] // vendor and card name the entry in the log and the tests
pub struct Entry {
    pub card: String,
    pub matches: Vec<String>,
    pub vendor: String,
    pub label: String,
    pub tiers: Vec<Value>,
}

pub struct Table {
    pub class: String,
    pub entries: Vec<Entry>,
}

fn parse(text: &str) -> Result<Table, String> {
    let v: Value = serde_json::from_str(text).map_err(|e| format!("the tiers table is not JSON: {e}"))?;
    let class = v.get("class").and_then(|c| c.as_str()).unwrap_or("").to_string();
    if class.is_empty() {
        return Err("the tiers table names no class".into());
    }
    let mut entries = Vec::new();
    for c in v.get("cards").and_then(|c| c.as_array()).ok_or("the tiers table has no cards")? {
        let s = |k: &str| c.get(k).and_then(|x| x.as_str()).unwrap_or("").to_string();
        let matches: Vec<String> = c.get("match").and_then(|m| m.as_array()).map(|a| a.iter().filter_map(|x| x.as_str().map(|s| s.to_ascii_uppercase())).collect()).unwrap_or_default();
        let tiers: Vec<Value> = c.get("tiers").and_then(|t| t.as_array()).cloned().unwrap_or_default();
        if s("card").is_empty() || matches.is_empty() || tiers.is_empty() {
            continue;
        }
        entries.push(Entry { card: s("card"), matches, vendor: s("vendor"), label: s("label"), tiers });
    }
    Ok(Table { class, entries })
}

pub fn table() -> &'static Table {
    static T: OnceLock<Table> = OnceLock::new();
    T.get_or_init(|| parse(TABLE).unwrap_or_else(|e| { eprintln!("tiers table: {e}"); Table { class: String::new(), entries: Vec::new() } }))
}

/// The entry a card name matches: the longest match string found in the upper-cased name wins.
pub fn entry_for<'a>(t: &'a Table, card_name: &str) -> Option<&'a Entry> {
    let n = card_name.to_ascii_uppercase();
    let mut best: Option<(&Entry, usize)> = None;
    for e in &t.entries {
        for m in &e.matches {
            if n.contains(m.as_str()) && best.map(|(_, l)| m.len() > l).unwrap_or(true) {
                best = Some((e, m.len()));
            }
        }
    }
    best.map(|(e, _)| e)
}

/// What a card takes from the table: its rows in the card-state shape (table: true, the class's label on every row),
/// the table's class, the class's label; None when the table has no entry for the name.
#[allow(dead_code)]
pub struct Seed {
    pub rows: Vec<Value>,
    pub class: String,
    pub label: String,
    pub card: String,
}

pub fn seed(card_name: &str) -> Option<Seed> {
    let t = table();
    let e = entry_for(t, card_name)?;
    let rows: Vec<Value> = e.tiers.iter().map(|r| {
        let n = |k: &str| r.get(k).and_then(|v| v.as_f64()).unwrap_or(0.0);
        let mut row = json!({
            "id": r.get("id").cloned().unwrap_or(json!("")),
            "clock_mhz": n("clock_mhz") as u64, "power_pct": (n("power_pct") as u64).clamp(50, 100), "mem_mhz": n("mem_mhz") as u64,
            "limit_w": n("limit_w"), "w": n("w"), "mhs": n("mhs"), "mhw": if n("mhw") > 0.0 { n("mhw") } else if n("w") > 0.0 { n("mhs") / n("w") } else { 0.0 },
            "source": r.get("source").cloned().unwrap_or(json!("measured")), "label": r.get("label").cloned().unwrap_or(json!(e.label.clone())),
            "table": true, "note": r.get("note").cloned().unwrap_or(json!("")),
        });
        if let Some(o) = r.get("core_offset_mhz") { row["core_offset_mhz"] = o.clone(); }
        if let Some(o) = r.get("power_offset_pct") { row["power_offset_pct"] = o.clone(); }
        row
    }).collect();
    Some(Seed { rows, class: t.class.clone(), label: e.label.clone(), card: e.card.clone() })
}

/// A table row's core offset (AMD): the absolute clock the knob sends, from the card's stock clock; None when the row
/// carries no offset or the stock clock is not known yet.
pub fn clock_from_offset(row: &Value, stock_mhz: u32) -> Option<u32> {
    let off = row.get("core_offset_mhz").and_then(|v| v.as_f64())?;
    if stock_mhz == 0 || off >= 0.0 {
        return None;
    }
    Some((stock_mhz as f64 + off).max(1.0) as u32)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn the_shipped_table_parses_and_covers_the_cards_the_brief_names() {
        let t = table();
        assert_eq!(t.class, "v5");
        for want in ["5090", "5080", "5070 TI", "4090", "9070 XT", "H100", "M5 MAX", "B580"] {
            assert!(t.entries.iter().any(|e| e.card.to_ascii_uppercase().contains(want)), "{want}");
        }
    }
    #[test]
    fn the_longest_match_wins_and_an_unknown_card_gets_nothing() {
        let t = table();
        assert_eq!(entry_for(t, "NVIDIA GeForce RTX 5070 Ti").unwrap().card, "NVIDIA GeForce RTX 5070 Ti");
        assert_eq!(entry_for(t, "NVIDIA GeForce RTX 5070").unwrap().card, "NVIDIA GeForce RTX 5070");
        assert_eq!(entry_for(t, "AMD Radeon RX 9070 XT (gfx1201)").unwrap().card, "AMD Radeon RX 9070 XT");
        assert!(entry_for(t, "Some Other GPU").is_none());
        assert!(seed("Some Other GPU").is_none());
    }
    #[test]
    fn a_5090_takes_three_rows_in_the_apply_shape_with_the_table_flag_and_the_balanced_knee_at_1300() {
        let s = seed("NVIDIA GeForce RTX 5090").unwrap();
        assert_eq!((s.class.as_str(), s.label.as_str(), s.rows.len()), ("v5", "measured", 3));
        let ids: Vec<&str> = s.rows.iter().map(|r| r["id"].as_str().unwrap()).collect();
        assert_eq!(ids, ["efficiency", "balanced", "max"]);
        assert!(s.rows.iter().all(|r| r["table"] == json!(true) && r["label"] == json!("measured")));
        let (p, limit, mhs, w) = crate::ember::tier_from_json(&s.rows, "balanced").unwrap();
        assert_eq!((p.clock_mhz, p.power_pct, p.mem_mhz), (1300, 100, 0));
        assert_eq!(limit, 575.0);
        assert!((mhs - 134.76).abs() < 0.01 && (w - 318.8).abs() < 0.01);
        assert_eq!(s.rows[2]["source"], json!("stock"));
    }
    #[test]
    fn a_card_without_a_lever_takes_the_max_row_only_and_an_amd_row_converts_its_offset_once_the_stock_clock_is_known() {
        let m = seed("Apple M5 Max").unwrap();
        assert_eq!(m.rows.len(), 1);
        assert_eq!(m.rows[0]["id"], json!("max"));
        let a = seed("AMD Radeon RX 9070 XT").unwrap();
        let eff = &a.rows[0];
        assert_eq!(eff["core_offset_mhz"], json!(-500));
        assert_eq!(clock_from_offset(eff, 0), None, "no stock clock yet: the offset waits");
        assert_eq!(clock_from_offset(eff, 3292), Some(2792));
        assert_eq!(clock_from_offset(&a.rows[2], 3292), None, "the stock row has no offset");
        let (p, _, _, _) = crate::ember::tier_from_json(&a.rows, "efficiency").unwrap();
        assert_eq!(p.power_pct, 70);
    }
}
