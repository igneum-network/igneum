//! Did this PC come up from a power loss or a hard reset? (MF-11, 7 October 2026: the second Windows machine dropped twice in one day with
//! Kernel-Power 41 and EventLog 6008 at the next boot, no bugcheck, no dump, and nothing said so until a person read
//! the event log.) Windows: the System log's event 41 (Kernel-Power, critical) or 6008 (EventLog, "the previous
//! shutdown was unexpected") inside the last 15 minutes, read once at the engine's start through wevtutil; the engine
//! logs one `FAULT pc-restart:` line to the intake. Other platforms: nothing (a Mac's power log is not this class).

use std::process::Command;
use std::time::Duration;

/// How far back the start-up check looks: an engine starts at login, inside a minute or two of the boot.
pub const WINDOW_MS: u64 = 15 * 60 * 1000;

/// One line naming the event, or None when the boot was clean, the query failed, or this is not Windows.
pub fn unexpected_restart() -> Option<String> {
    if !cfg!(windows) {
        return None;
    }
    let query = format!("*[System[(EventID=41 or EventID=6008) and TimeCreated[timediff(@SystemTime) <= {WINDOW_MS}]]]");
    let mut c = Command::new(crate::platform::tool("wevtutil"));
    c.args(["qe", "System", &format!("/q:{query}"), "/f:text", "/c:2", "/rd:true"]);
    let out = crate::detect::run_timeout(&mut c, None, Duration::from_secs(20))?;
    parse_events(&out)
}

/// The reading of wevtutil's text output: the newest 41 or 6008 as "event 41 (Kernel-Power) at <time>" or
/// "event 6008 (the previous shutdown was unexpected) at <time>"; None when neither is in the text.
pub fn parse_events(text: &str) -> Option<String> {
    let mut date = String::new();
    let mut id = String::new();
    for line in text.lines() {
        let t = line.trim();
        if let Some(d) = t.strip_prefix("Date:") {
            date = d.trim().to_string();
        } else if let Some(i) = t.strip_prefix("Event ID:") {
            id = i.trim().to_string();
            if id == "41" || id == "6008" {
                break;
            }
        }
    }
    match id.as_str() {
        "41" => Some(format!("event 41 (Kernel-Power: the system rebooted without shutting down cleanly) at {date}")),
        "6008" => Some(format!("event 6008 (the previous shutdown was unexpected) at {date}")),
        _ => None,
    }
}

#[cfg(test)]
mod tests {
    use super::parse_events;

    const PC2: &str = "Event[0]\n  Log Name: System\n  Source: Microsoft-Windows-Kernel-Power\n  Date: 2026-10-07T14:37:19.4360000Z\n  Event ID: 41\n  Task: N/A\n  Level: Critical\n  Opcode: Info\n  Keyword: N/A\n  User: S-1-5-18\n  Computer: X\n  Description: \n\nEvent[1]\n  Log Name: System\n  Source: EventLog\n  Date: 2026-10-07T14:37:29.0380000Z\n  Event ID: 6008\n  Task: None\n  Level: Error\n";

    /// the second Windows machine's boot of 13:37Z on 7 October 2026 (the collection job's wevtutil text): the known-failed case reads as one.
    #[test]
    fn pc2_boot_reads_as_a_power_loss() {
        assert_eq!(parse_events(PC2), Some("event 41 (Kernel-Power: the system rebooted without shutting down cleanly) at 2026-10-07T14:37:19.4360000Z".into()));
    }

    #[test]
    fn a_6008_alone_is_the_unexpected_shutdown() {
        let t = "Event[0]\n  Log Name: System\n  Source: EventLog\n  Date: 2026-10-07T08:22:34.0000000Z\n  Event ID: 6008\n  Level: Error\n";
        assert_eq!(parse_events(t), Some("event 6008 (the previous shutdown was unexpected) at 2026-10-07T08:22:34.0000000Z".into()));
    }

    /// A clean boot (the known-good case): nothing, including other ids inside the window and an empty answer.
    #[test]
    fn a_clean_boot_reads_as_nothing() {
        assert_eq!(parse_events(""), None);
        assert_eq!(parse_events("Event[0]\n  Log Name: System\n  Source: Microsoft-Windows-Kernel-General\n  Date: 2026-10-07T14:37:17.7400000Z\n  Event ID: 12\n  Level: Information\n"), None);
        assert_eq!(parse_events("wevtutil: access denied"), None);
    }
}
