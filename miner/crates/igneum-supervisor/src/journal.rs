//! The first-run journal for gate HW-06 (design section 2.6): what screens were seen and when, which failure
//! states were shown and recovered from, when the first work was accepted. No identifiers, no network; the
//! user exports it by hand.

use serde::{Deserialize, Serialize};
use std::path::PathBuf;
use std::time::{SystemTime, UNIX_EPOCH};

pub const KINDS: &[&str] = &[
    "app_opened",
    "screen_enter",
    "screen_leave",
    "device_detected",
    "device_refused",
    "address_entered",
    "address_error",
    "source_chosen",
    "tariff_entered",
    "tariff_skipped",
    "start_pressed",
    "stage",
    "failure_shown",
    "recovered",
    "first_work_accepted",
    "setup_done",
    "download_seconds",
    "note",
];

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct Entry {
    pub t_unix_ms: u64,
    pub kind: String,
    pub detail: String,
}

pub struct Journal {
    path: PathBuf,
    entries: Vec<Entry>,
}

impl Journal {
    pub fn open(path: PathBuf) -> Journal {
        let entries = std::fs::read_to_string(&path)
            .ok()
            .map(|s| s.lines().filter_map(|l| serde_json::from_str::<Entry>(l).ok()).collect())
            .unwrap_or_default();
        Journal { path, entries }
    }

    pub fn now_ms() -> u64 {
        SystemTime::now().duration_since(UNIX_EPOCH).map(|d| d.as_millis() as u64).unwrap_or(0)
    }

    /// Records one line. Unknown kinds and details over 200 bytes are refused so the interface cannot write
    /// arbitrary text here.
    pub fn record(&mut self, kind: &str, detail: &str) -> Result<(), String> {
        if !KINDS.contains(&kind) {
            return Err(format!("unknown journal kind {kind}"));
        }
        if detail.len() > 200 || detail.bytes().any(|b| b < 0x20) {
            return Err("journal detail: up to 200 printable bytes".into());
        }
        let e = Entry { t_unix_ms: Self::now_ms(), kind: kind.to_string(), detail: detail.to_string() };
        if let Ok(line) = serde_json::to_string(&e) {
            if let Some(dir) = self.path.parent() {
                let _ = std::fs::create_dir_all(dir);
            }
            use std::io::Write;
            if let Ok(mut f) = std::fs::OpenOptions::new().create(true).append(true).open(&self.path) {
                let _ = writeln!(f, "{line}");
            }
        }
        self.entries.push(e);
        Ok(())
    }

    pub fn entries(&self) -> &[Entry] {
        &self.entries
    }

    /// The text the user copies for the study: relative times from the first line, one entry per line.
    pub fn export(&self, app_version: &str) -> String {
        let mut out = format!("Ember first-run journal (app {app_version}). No identifiers; times are seconds from the first line.\n");
        let t0 = self.entries.first().map(|e| e.t_unix_ms).unwrap_or(0);
        for e in &self.entries {
            out.push_str(&format!("{:>9.3}  {:<20} {}\n", (e.t_unix_ms.saturating_sub(t0)) as f64 / 1000.0, e.kind, e.detail));
        }
        let completed = self.entries.iter().any(|e| e.kind == "first_work_accepted");
        let rescues = self.entries.iter().filter(|e| e.kind == "failure_shown").count();
        out.push_str(&format!("\nfirst work accepted: {}; failure states shown: {rescues}\n", if completed { "yes" } else { "no" }));
        out
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn records_known_kinds_only_and_exports() {
        let dir = std::env::temp_dir().join(format!("igneum-journal-test-{}", std::process::id()));
        let mut j = Journal::open(dir.join("journal.jsonl"));
        j.record("app_opened", "").unwrap();
        j.record("screen_enter", "welcome").unwrap();
        assert!(j.record("anything", "x").is_err());
        assert!(j.record("note", &"x".repeat(201)).is_err());
        j.record("first_work_accepted", "pool").unwrap();
        let text = j.export("0.1.0");
        assert!(text.contains("first work accepted: yes"));
        assert!(text.contains("screen_enter"));
        let again = Journal::open(dir.join("journal.jsonl"));
        assert_eq!(again.entries().len(), 3);
        let _ = std::fs::remove_dir_all(&dir);
    }
}
