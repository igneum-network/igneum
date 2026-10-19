//! The driver table and the offer decision (branch driver-check, 7 October 2026): the pure half of src/drivers.rs,
//! with no dependency on the engine, so the signer binary (src/bin/ota-sign.rs) validates a manifest's `drivers`
//! object the way the app does. The install thread, the download and the Authenticode call live in drivers.rs.

use serde::Serialize;
use std::path::Path;

/// One vendor's row of the table.
#[derive(Clone, Debug, Default, PartialEq, Serialize)]
pub struct VendorEntry {
    pub vendor: String,
    /// the lowest version the worker runs on (NVIDIA: nvidia-smi's "570.00"; AMD and Intel: Windows' DriverVersion "32.0.101.9034")
    pub min_version: String,
    /// what the click installs
    pub version: String,
    pub url: String,
    pub size: u64,
    pub sha256: String,
    /// where the sha256 was read (the vendor's page), for the record on the row
    pub hash_source: String,
    /// a Referer the vendor's server requires (drivers.amd.com answers 403 without an amd.com one); empty = none
    pub referer: String,
    /// the installer's silent arguments
    pub args: Vec<String>,
    /// exit codes that mean "installed, restart required"
    pub reboot_codes: Vec<i64>,
    /// a word the Authenticode subject must carry ("NVIDIA", "Advanced Micro Devices", "Intel")
    pub signer: String,
    /// Linux and HiveOS: the package to name, no installer
    pub linux_package: String,
    pub hive_package: String,
}

#[derive(Clone, Debug, Default, PartialEq)]
pub struct Table {
    pub updated: String,
    pub dry_run: bool,
    pub vendors: Vec<VendorEntry>,
}

impl Table {
    /// The manifest's `drivers` object. Every entry is checked the way the platform entries are: https (or 127.0.0.1
    /// for a test), 64-hex sha256, a size, a version on both sides, at least one silent argument.
    pub fn parse(v: &serde_json::Value) -> Result<Table, String> {
        let s = |v: &serde_json::Value, k: &str| v.get(k).and_then(|x| x.as_str()).unwrap_or("").trim().to_string();
        let obj = v.get("vendors").and_then(|x| x.as_object()).ok_or("drivers: no vendors object")?;
        let mut vendors = Vec::new();
        for (name, e) in obj {
            if e.is_null() {
                continue;
            }
            let vendor = name.to_ascii_lowercase();
            if !["nvidia", "amd", "intel"].contains(&vendor.as_str()) {
                return Err(format!("drivers: vendor '{name}' is not nvidia, amd or intel"));
            }
            let entry = VendorEntry {
                vendor: vendor.clone(),
                min_version: s(e, "min_version"),
                version: s(e, "version"),
                url: s(e, "url"),
                size: e.get("size").and_then(|x| x.as_u64()).unwrap_or(0),
                sha256: s(e, "sha256").to_ascii_lowercase(),
                hash_source: s(e, "hash_source"),
                referer: s(e, "referer"),
                args: e.get("args").and_then(|a| a.as_array()).map(|a| a.iter().filter_map(|x| x.as_str()).map(|x| x.to_string()).collect()).unwrap_or_default(),
                reboot_codes: e.get("reboot_codes").and_then(|a| a.as_array()).map(|a| a.iter().filter_map(|x| x.as_i64()).collect()).unwrap_or_default(),
                signer: s(e, "signer"),
                linux_package: s(e, "linux_package"),
                hive_package: s(e, "hive_package"),
            };
            if parse_dotted(&entry.min_version).is_none() || parse_dotted(&entry.version).is_none() {
                return Err(format!("drivers.{name}: min_version and version must be dotted numbers"));
            }
            if !entry.url.starts_with("https://") && !entry.url.starts_with("http://127.0.0.1:") {
                return Err(format!("drivers.{name}: the url is not https"));
            }
            if entry.sha256.len() != 64 || !entry.sha256.chars().all(|c| c.is_ascii_hexdigit()) {
                return Err(format!("drivers.{name}: sha256 is not 64 hex characters"));
            }
            if entry.size == 0 {
                return Err(format!("drivers.{name}: size is missing"));
            }
            if entry.args.is_empty() || entry.args.iter().any(|a| a.contains(['"', '\'', '&', '|', ';', '`'])) {
                return Err(format!("drivers.{name}: args must be plain switches"));
            }
            if !entry.referer.is_empty() && !entry.referer.starts_with("https://") {
                return Err(format!("drivers.{name}: referer must be https"));
            }
            if entry.signer.is_empty() {
                return Err(format!("drivers.{name}: signer is missing (the Authenticode subject word)"));
            }
            vendors.push(entry);
        }
        if vendors.is_empty() {
            return Err("drivers: the vendors object is empty".into());
        }
        vendors.sort_by(|a, b| a.vendor.cmp(&b.vendor));
        Ok(Table { updated: s(v, "updated"), dry_run: v.get("dry_run").and_then(|x| x.as_bool()).unwrap_or(false), vendors })
    }

    pub fn entry(&self, vendor: &str) -> Option<&VendorEntry> {
        self.vendors.iter().find(|e| e.vendor == vendor)
    }
}

/// "32.0.101.9034" or "581.57" as numbers; None for anything else (an empty string, "3683.0 (PAL,LC)").
pub fn parse_dotted(s: &str) -> Option<Vec<u64>> {
    let t = s.trim();
    if t.is_empty() {
        return None;
    }
    let mut out = Vec::new();
    for p in t.split('.') {
        out.push(p.trim().parse::<u64>().ok()?);
    }
    Some(out)
}

/// `a` is at least `b`, compared part by part (a missing trailing part reads 0).
pub fn at_least(a: &[u64], b: &[u64]) -> bool {
    let n = a.len().max(b.len());
    for i in 0..n {
        let (x, y) = (a.get(i).copied().unwrap_or(0), b.get(i).copied().unwrap_or(0));
        if x != y {
            return x > y;
        }
    }
    true
}

/// What the card's row shows about its driver.
#[derive(Clone, Debug, Default, PartialEq, Serialize)]
pub struct Offer {
    pub vendor: String,
    /// "missing" | "old" | "fine" | "none" (no row for this vendor in the table)
    pub status: String,
    pub installed: String,
    pub wanted: String,
    pub min_version: String,
    pub size: u64,
    pub url: String,
    pub hash_source: String,
    /// the row's sentence
    pub text: String,
    /// true when the click can install here (Windows with an entry); false with a note on macOS, Linux and HiveOS
    pub installable: bool,
}

/// The platform word for the notes: "windows" | "macos" | "linux" | "hive".
pub fn platform_word() -> &'static str {
    if cfg!(windows) {
        "windows"
    } else if cfg!(target_os = "macos") {
        "macos"
    } else if Path::new("/hive").is_dir() || Path::new("/hive-config").is_dir() {
        "hive"
    } else {
        "linux"
    }
}

/// The offer for a card: its vendor, the version its driver reports (empty = none found) and the table.
pub fn offer_for(vendor: &str, installed: &str, table: Option<&Table>, platform: &str) -> Option<Offer> {
    if vendor == "apple" {
        return Some(Offer { vendor: "apple".into(), status: "none".into(), installed: installed.into(), text: "Apple silicon: no driver step, macOS carries it.".into(), ..Default::default() });
    }
    let e = table.and_then(|t| t.entry(vendor))?;
    let word = match vendor {
        "nvidia" => "NVIDIA",
        "amd" => "AMD",
        "intel" => "Intel Arc",
        _ => vendor,
    };
    let mb = (e.size + 512 * 1024) / (1024 * 1024);
    let size_text = if mb >= 1024 { format!("{:.1} GB", mb as f64 / 1024.0) } else { format!("{mb} MB") };
    let have = parse_dotted(installed);
    let need = parse_dotted(&e.min_version).unwrap_or_default();
    let status = match &have {
        None => "missing",
        Some(h) if at_least(h, &need) => "fine",
        Some(_) => "old",
    };
    let base = Offer { vendor: vendor.into(), status: status.into(), installed: installed.into(), wanted: e.version.clone(), min_version: e.min_version.clone(), size: e.size, url: e.url.clone(), hash_source: e.hash_source.clone(), text: String::new(), installable: false };
    let text = match (platform, status) {
        ("macos", _) => "macOS: no driver step.".to_string(),
        ("hive", "fine") | ("linux", "fine") => format!("{word} driver {installed}: fine."),
        ("hive", _) => format!("HiveOS: {} the driver with {} (the worker needs {} or newer).", if status == "missing" { "install" } else { "update" }, if e.hive_package.is_empty() { "hive's driver tool" } else { &e.hive_package }, e.min_version),
        ("linux", _) => format!("Linux: {} the package {} (the worker needs {} or newer).", if status == "missing" { "install" } else { "update" }, if e.linux_package.is_empty() { "the vendor's driver" } else { &e.linux_package }, e.min_version),
        (_, "fine") => format!("{word} driver {installed}: fine."),
        (_, "missing") => format!("Install the {word} driver {} ({size_text}). No driver was found, so this card cannot mine yet.", e.version),
        _ => format!("Install the {word} driver {} ({size_text}). Driver {installed} is older than the {} the worker needs.", e.version, e.min_version),
    };
    Some(Offer { text, installable: platform == "windows" && status != "fine", ..base })
}

/// The installer's exit code read: (restart required, the sentence).
pub fn exit_meaning(code: i64, e: &VendorEntry) -> (bool, String) {
    if code == 0 {
        return (false, format!("{} driver {} installed.", e.vendor.to_ascii_uppercase(), e.version));
    }
    if e.reboot_codes.contains(&code) {
        return (true, format!("{} driver {} installed. Restart Windows to finish.", e.vendor.to_ascii_uppercase(), e.version));
    }
    (false, format!("the {} installer exited with code {code}.", e.vendor.to_ascii_uppercase()))
}

/// The install's progress, published as `state.drivers`.
#[derive(Clone, Debug, Default, PartialEq, Serialize)]
pub struct DriverState {
    /// idle | downloading | verifying | installing | done | reboot | error
    pub status: String,
    pub vendor: String,
    pub version: String,
    pub progress: f64,
    pub message: String,
    pub error: String,
    pub reboot_required: bool,
    pub dry_run: bool,
    pub started_at: f64,
    pub finished_at: f64,
    /// the table's updated stamp, for the Settings page ("drivers table of 7 October")
    pub table_updated: String,
    pub platform: String,
}

/// The Authenticode verdict from `Get-AuthenticodeSignature`'s two lines ("Valid" and the subject): Ok(subject) when
/// the status is Valid and the subject carries the vendor's word.
pub fn authenticode_verdict(text: &str, signer: &str) -> Result<String, String> {
    let mut status = String::new();
    let mut subject = String::new();
    for l in text.lines() {
        if let Some(v) = l.strip_prefix("status=") {
            status = v.trim().to_string();
        } else if let Some(v) = l.strip_prefix("subject=") {
            subject = v.trim().to_string();
        }
    }
    if status != "Valid" {
        return Err(format!("the file's signature is {}: not installed", if status.is_empty() { "unreadable".to_string() } else { status }));
    }
    if !subject.to_ascii_lowercase().contains(&signer.to_ascii_lowercase()) {
        return Err(format!("the file is signed by \"{subject}\", not {signer}: not installed"));
    }
    Ok(subject)
}

/// The cards whose worker stops before a vendor's driver installer starts: every enabled card of that vendor. the second Windows machine,
/// 7 October 2026, 16:26Z: the Intel installer took the kernel down (bugcheck 0x3B) with the app's worker mining on the
/// Arc B580 in a Razer Core X V2; at 19:14 UK the same card read kind=discrete on an earlier release (no USB4 or Thunderbolt
/// router in its parent chain on that board), so a hold keyed on the external kind alone would have missed the card
/// that crashed the box. The rule since an earlier release (the shipper's word, 19:1x UK): the vendor's cards all stop, the other
/// vendors' cards keep mining, a card already off has nothing to stop. Each item is (key, vendor, kind, enabled).
pub fn install_hold_keys<'a>(cards: impl IntoIterator<Item = (&'a str, &'a str, &'a str, bool)>, vendor: &str) -> Vec<String> {
    cards.into_iter().filter(|(_, v, _, enabled)| *v == vendor && *enabled).map(|(key, _, _, _)| key.to_string()).collect()
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn the_shipped_table_accepts_the_inbox_6733_driver_for_the_arc_b580_until_an_unattended_install_exists() {
        // the founder's rule, 7 October 2026 evening: no PC job needs a click or a UAC prompt. The one-click install asks for
        // one, so the table does not demand 9034 of an Arc on Windows' inbox 6733 (the worker mines at 11.0 MH/s on it
        // with the Intel rotate rewrite); 9034 stays the version on offer for a card with no driver at all.
        let v: serde_json::Value = serde_json::from_str(include_str!("../../../packaging/ota/drivers.json")).expect("the shipped table is JSON");
        let t = Table::parse(&v).expect("the shipped table parses");
        let intel = t.entry("intel").expect("an Intel row");
        assert_eq!(intel.min_version, "32.0.101.6733", "6733 is acceptable for the B580 until the Power Helper task installs drivers unattended");
        assert_eq!(intel.version, "32.0.101.9034");
        let o = offer_for("intel", "32.0.101.6733", Some(&t), "windows").unwrap();
        assert_eq!((o.status.as_str(), o.installable), ("fine", false), "{}", o.text);
        let o = offer_for("intel", "", Some(&t), "windows").unwrap();
        assert!(o.installable && o.text.starts_with("Install the Intel Arc driver 32.0.101.9034"), "{}", o.text);
    }

    #[test]
    fn a_driver_install_stops_every_card_of_its_vendor_first_and_leaves_the_other_vendors_mining() {
        // the second Windows machine, 7 October 2026: the Arc B580 in the Razer Core X V2 was mining when the Intel installer's display
        // reset took the machine down. The rule: the vendor's external cards stop, nothing else does.
        let cards = [
            ("nvidia:0:NVIDIA GeForce RTX 5090", "nvidia", "discrete", true),
            ("nvidia:1:NVIDIA GeForce RTX 5060 Ti", "nvidia", "external", true),
            ("intel:Intel(R) Arc(TM) B580 Graphics", "intel", "external", true),
            ("intel::Intel UHD 770", "other", "integrated", false),
        ];
        assert_eq!(install_hold_keys(cards, "intel"), vec!["intel:Intel(R) Arc(TM) B580 Graphics".to_string()], "the Arc in the enclosure stops; the NVIDIA cards and the iGPU keep mining");
        assert_eq!(install_hold_keys(cards, "nvidia"), vec!["nvidia:0:NVIDIA GeForce RTX 5090".to_string(), "nvidia:1:NVIDIA GeForce RTX 5060 Ti".to_string()], "every NVIDIA card stops for the NVIDIA install, inside the case or not");
        assert!(install_hold_keys(cards, "amd").is_empty(), "no AMD card: nothing stops");
        // a card already off has nothing to stop; a discrete card of the vendor IS held (the second Windows machine, 19:14 UK: the Arc in the
        // Razer Core X V2 read kind=discrete on an earlier release, so the kind alone would have missed the card that crashed the box)
        let off = [("intel:Intel(R) Arc(TM) B580 Graphics", "intel", "external", false), ("intel:Intel(R) Arc(TM) A770 Graphics", "intel", "discrete", true)];
        assert_eq!(install_hold_keys(off, "intel"), vec!["intel:Intel(R) Arc(TM) A770 Graphics".to_string()]);
        // the iGPU carries vendor "other" on the second Windows machine (an AMD Radeon(TM) Graphics as amd:gfx1036 on another read): the install's
        // vendor word decides, and a card off stays untouched
        let igpu = [("amd:gfx1036", "amd", "integrated", false), ("amd:gfx1201", "amd", "discrete", true)];
        assert_eq!(install_hold_keys(igpu, "amd"), vec!["amd:gfx1201".to_string()]);
    }


    const TABLE: &str = r#"{"updated":"2026-10-07","vendors":{
      "nvidia":{"min_version":"570.00","version":"617.42","url":"https://us.download.nvidia.com/Windows/617.42/617.42-desktop-win10-win11-64bit-international-dch-whql.exe","size":990853168,"sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","hash_source":"https://www.nvidia.com/en-us/drivers/details/","args":["-s","-noreboot"],"reboot_codes":[1],"signer":"NVIDIA","linux_package":"nvidia-driver-570","hive_package":"nvidia-driver-update 570"},
      "amd":{"referer":"https://www.amd.com/","min_version":"32.0.32000.0","version":"26.9.2","url":"https://drivers.amd.com/drivers/whql-amd-software-adrenalin-edition-26.9.2-win10-win11-sep2026.exe","size":912345678,"sha256":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","hash_source":"https://www.amd.com/en/support/download/drivers.html","args":["-install","-silent"],"reboot_codes":[3010],"signer":"Advanced Micro Devices","linux_package":"amdgpu-install --usecase=opencl"},
      "intel":{"min_version":"32.0.101.9034","version":"32.0.101.9034","url":"https://downloadmirror.intel.com/929959/gfx_win_101.9034.exe","size":932631144,"sha256":"72ba7eea08a0cc18650603976ff9433dfdf84d23d4cbbee662a4df9f2856ae98","hash_source":"https://www.intel.com/content/www/us/en/download/785597/","args":["-s"],"reboot_codes":[14,1014],"signer":"Intel","linux_package":"intel-opencl-icd"}}}"#;

    fn table() -> Table {
        Table::parse(&serde_json::from_str(TABLE).unwrap()).unwrap()
    }

    #[test]
    fn versions_compare_part_by_part() {
        assert!(at_least(&parse_dotted("32.0.101.9034").unwrap(), &parse_dotted("32.0.101.9034").unwrap()));
        assert!(at_least(&parse_dotted("32.0.101.9040").unwrap(), &parse_dotted("32.0.101.9034").unwrap()));
        assert!(!at_least(&parse_dotted("32.0.101.6733").unwrap(), &parse_dotted("32.0.101.9034").unwrap()));
        assert!(at_least(&parse_dotted("581.57").unwrap(), &parse_dotted("570.00").unwrap()));
        assert!(!at_least(&parse_dotted("566.36").unwrap(), &parse_dotted("570").unwrap()));
        assert!(at_least(&parse_dotted("570").unwrap(), &parse_dotted("570.0.0").unwrap()));
        assert_eq!(parse_dotted("3683.0 (PAL,LC)"), None);
        assert_eq!(parse_dotted(""), None);
    }

    #[test]
    fn the_table_parses_and_bad_rows_are_refused() {
        let t = table();
        assert_eq!(t.vendors.len(), 3);
        assert_eq!(t.entry("intel").unwrap().reboot_codes, vec![14, 1014]);
        assert_eq!(t.entry("nvidia").unwrap().args, vec!["-s", "-noreboot"]);
        assert!(!t.dry_run);
        let bad = |patch: &str, what: &str| {
            let txt = TABLE.replacen(patch, what, 1);
            let e = Table::parse(&serde_json::from_str(&txt).unwrap()).unwrap_err();
            e
        };
        assert!(bad("https://downloadmirror", "http://downloadmirror").contains("https"));
        assert!(bad("\"size\":932631144", "\"size\":0").contains("size"));
        assert!(bad("\"args\":[\"-s\"]", "\"args\":[\"-s; calc\"]").contains("args"));
        assert!(bad("\"signer\":\"Intel\"", "\"signer\":\"\"").contains("signer"));
        assert!(bad("\"args\":[\"-install\",\"-silent\"]", "\"referer\":\"http://x\",\"args\":[\"-install\",\"-silent\"]").contains("referer"));
        assert_eq!(t.entry("amd").unwrap().referer, "https://www.amd.com/");
        assert!(bad("\"min_version\":\"570.00\"", "\"min_version\":\"latest\"").contains("dotted"));
        assert!(Table::parse(&serde_json::json!({"vendors": {"apple": {}}})).unwrap_err().contains("vendor"));
        assert!(Table::parse(&serde_json::json!({"vendors": {}})).unwrap_err().contains("empty"));
        assert!(Table::parse(&serde_json::json!({"vendors": {"intel": null}})).unwrap_err().contains("empty"));
    }

    /// The offers per tier: a missing driver, an old one, a fine one, Apple, Linux and HiveOS.
    #[test]
    fn offers_name_the_version_the_size_and_the_reason() {
        let t = table();
        let o = offer_for("intel", "", Some(&t), "windows").unwrap();
        assert_eq!(o.status, "missing");
        assert!(o.installable);
        assert_eq!(o.text, "Install the Intel Arc driver 32.0.101.9034 (889 MB). No driver was found, so this card cannot mine yet.");
        let o = offer_for("intel", "32.0.101.6733", Some(&t), "windows").unwrap();
        assert_eq!(o.status, "old");
        assert_eq!(o.text, "Install the Intel Arc driver 32.0.101.9034 (889 MB). Driver 32.0.101.6733 is older than the 32.0.101.9034 the worker needs.");
        let o = offer_for("intel", "32.0.101.9034", Some(&t), "windows").unwrap();
        assert_eq!((o.status.as_str(), o.installable, o.text.as_str()), ("fine", false, "Intel Arc driver 32.0.101.9034: fine."));
        let o = offer_for("nvidia", "617.42", Some(&t), "windows").unwrap();
        assert_eq!(o.status, "fine");
        let o = offer_for("nvidia", "566.36", Some(&t), "windows").unwrap();
        assert_eq!(o.text, "Install the NVIDIA driver 617.42 (945 MB). Driver 566.36 is older than the 570.00 the worker needs.");
        let o = offer_for("amd", "32.0.21042.62", Some(&t), "windows").unwrap();
        assert_eq!(o.status, "old");
        assert!(o.text.starts_with("Install the AMD driver 26.9.2 (870 MB)."));
        // Apple: no step; Linux and HiveOS: the package, nothing installable
        let o = offer_for("apple", "", Some(&t), "macos").unwrap();
        assert_eq!((o.status.as_str(), o.installable), ("none", false));
        assert!(o.text.contains("no driver step"));
        let o = offer_for("nvidia", "", Some(&t), "linux").unwrap();
        assert_eq!(o.text, "Linux: install the package nvidia-driver-570 (the worker needs 570.00 or newer).");
        assert!(!o.installable);
        let o = offer_for("nvidia", "566.36", Some(&t), "hive").unwrap();
        assert_eq!(o.text, "HiveOS: update the driver with nvidia-driver-update 570 (the worker needs 570.00 or newer).");
        let o = offer_for("intel", "", Some(&t), "macos").unwrap();
        assert_eq!(o.text, "macOS: no driver step.");
        // no table, or a vendor the table lacks: no offer, the row says nothing
        assert!(offer_for("nvidia", "", None, "windows").is_none());
        assert!(offer_for("other", "", Some(&t), "windows").is_none());
    }

    #[test]
    fn exit_codes_and_signatures_read_as_the_vendor_means_them() {
        let t = table();
        let intel = t.entry("intel").unwrap();
        assert_eq!(exit_meaning(0, intel), (false, "INTEL driver 32.0.101.9034 installed.".into()));
        assert_eq!(exit_meaning(1014, intel).0, true);
        assert_eq!(exit_meaning(14, intel).1, "INTEL driver 32.0.101.9034 installed. Restart Windows to finish.");
        assert_eq!(exit_meaning(1007, intel), (false, "the INTEL installer exited with code 1007.".into()));
        assert!(authenticode_verdict("status=Valid\nsubject=CN=Intel Corporation, O=Intel Corporation, S=California, C=US\n", "Intel").is_ok());
        assert!(authenticode_verdict("status=NotSigned\nsubject=\n", "Intel").unwrap_err().contains("NotSigned"));
        assert!(authenticode_verdict("status=Valid\nsubject=CN=Someone Else\n", "Intel").unwrap_err().contains("Someone Else"));
        assert!(authenticode_verdict("", "Intel").unwrap_err().contains("unreadable"));
    }

}
