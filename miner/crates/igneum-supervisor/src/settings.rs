//! User settings: one versioned file the user owns. The tuner's choices live in the tune store, never here,
//! so a tune can never overwrite a setting and a setting can never be lost to a tune.

use crate::economics::Tariff;
use crate::modes::{Limits, Mode};
use crate::schedule::{GamingPause, TimeOfUse};
use igneum_worker_proto::Params;
use serde::{Deserialize, Serialize};
use sha3::{Digest, Keccak256};
use std::collections::BTreeMap;
use std::path::Path;

pub const SETTINGS_VERSION: u32 = 1;

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(tag = "kind", rename_all = "snake_case")]
pub enum WorkSourceSetting {
    /// A pool from the signed list.
    Pool { pool_id: String },
    /// A pool the user typed.
    PoolCustom { host: String, port: u16, tls: bool },
    /// Solo through a node the user runs or trusts.
    Solo { rpc_url: String },
    /// The local stub source (development, the first-use flow's rehearsal). Labelled everywhere.
    Stub,
    /// Nothing chosen yet.
    Unset,
}

impl Default for WorkSourceSetting {
    fn default() -> Self {
        WorkSourceSetting::Unset
    }
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct Costs {
    pub wall_watts: Option<f64>,
    pub price_per_ign: Option<f64>,
    pub other_per_day: f64,
    pub currency: String,
    /// `None` means the platform default (PA-05: 40 W desktop, 10 W portable).
    pub host_allowance_w: Option<f64>,
}

impl Default for Costs {
    fn default() -> Self {
        Costs { wall_watts: None, price_per_ign: None, other_per_day: 0.0, currency: "GBP".into(), host_allowance_w: None }
    }
}

#[derive(Debug, Clone, Copy, Serialize, Deserialize, PartialEq, Eq, Default)]
#[serde(rename_all = "lowercase")]
pub enum UpdatePolicy {
    #[default]
    Ask,
    Auto,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct Settings {
    pub version: u32,
    pub setup_done: bool,
    pub payout_address: Option<String>,
    pub work_source: WorkSourceSetting,
    /// A second source the miner moves to by itself when the first stays unreachable for a minute, and back
    /// from after ten minutes when the first answers again (the plan's point 10: automatic pool transitions).
    /// Unset means no failover.
    #[serde(default)]
    pub work_source_backup: WorkSourceSetting,
    /// Ask the pool to waive its own fee (sent at sign-in; the pool decides). The protocol fund is a consensus
    /// rule and not a setting.
    #[serde(default)]
    pub no_pool_fee: bool,
    /// The candidate id to mine when the source cannot say (a pool before its first notify); none picks the
    /// first candidate the installed workers carry. A solo node's own algorithm always wins.
    #[serde(default)]
    pub algo: Option<String>,
    pub mode: Mode,
    pub limits: Limits,
    /// Machine-wide power budget, W; Quiet enforces per-device caps that sum to it.
    pub power_budget_w: Option<f64>,
    pub tariff: Tariff,
    pub costs: Costs,
    /// Devices the user switched off, by fingerprint.
    pub devices_off: Vec<String>,
    /// Devices the user switched on against the default (integrated, feasibility cohort), by fingerprint.
    pub devices_on: Vec<String>,
    /// Pinned knobs per device fingerprint.
    pub pins: BTreeMap<String, Params>,
    /// The one coordinate every card mines on a VectorWork pool (the founder's coordinate 4 line, 13 October 2026):
    /// the router neither probes nor switches while it is set; None routes by the ledger.
    #[serde(default)]
    pub coordinate_pin: Option<u8>,
    /// 0.2.15 (the founder's question of 15 October 2026): the chain's public RPC the engine reads the discipline
    /// registry from (igneum_vectorWork, igneum_getWork per coordinate; igneum_vectorRegistry when it answers), so a
    /// succession reaches the engine at its activation; None reads none. One read every 15 s. A list (comma, space or
    /// newline separated) names a whole-vector reader or one sealer per coordinate; each call tries them in order.
    #[serde(default)]
    pub registry_rpc: Option<String>,
    /// F-E66 (0.2.16): the last run's measured hash rate (hashes a second), declared at the first pool subscribe so the
    /// pool opens the session at that rate's step from the first job (the Windows build declared none before: the
    /// engine knew only a rate measured this run, zero at the first dial); written every minute while mining.
    #[serde(default)]
    pub last_rate_hps: u64,
    pub time_of_use: TimeOfUse,
    pub gaming_pause: GamingPause,
    pub update_policy: UpdatePolicy,
    pub resume_after_sleep: bool,
    pub pause_on_battery: bool,
    pub machine_name: String,
    pub worker_name: String,
    /// `system`, `light`, `dark`
    pub theme: String,
    pub settled_since_unix: Option<u64>,
}

impl Default for Settings {
    fn default() -> Self {
        Settings {
            version: SETTINGS_VERSION,
            setup_done: false,
            payout_address: None,
            work_source: WorkSourceSetting::Unset, work_source_backup: WorkSourceSetting::Unset, no_pool_fee: false,
            algo: None,
            mode: Mode::Efficient,
            limits: Limits::default(),
            power_budget_w: None,
            tariff: Tariff::default(),
            costs: Costs::default(),
            devices_off: Vec::new(),
            devices_on: Vec::new(),
            pins: BTreeMap::new(),
            coordinate_pin: None,
            registry_rpc: None,
            last_rate_hps: 0,
            time_of_use: TimeOfUse::default(),
            gaming_pause: GamingPause::default(),
            update_policy: UpdatePolicy::Ask,
            resume_after_sleep: false,
            pause_on_battery: true,
            machine_name: String::new(),
            worker_name: "igneum".into(),
            theme: "system".into(),
            settled_since_unix: None,
        }
    }
}

#[derive(Debug, thiserror::Error)]
pub enum SettingsError {
    #[error("{0}")]
    Invalid(String),
    #[error("the settings file is version {0}; this app writes version {SETTINGS_VERSION} and will not overwrite a newer file")]
    Newer(u32),
    #[error("io: {0}")]
    Io(#[from] std::io::Error),
    #[error("settings file: {0}")]
    Json(#[from] serde_json::Error),
}

impl Settings {
    /// The stored forms: the address checksummed, the currency upper case, names trimmed. Called before every
    /// validate on the way in, so what is saved is what is shown.
    pub fn normalise(&mut self) {
        if let Some(a) = &self.payout_address {
            if let Ok(c) = validate_address(a) {
                self.payout_address = Some(c);
            }
        }
        self.costs.currency = self.costs.currency.trim().to_ascii_uppercase();
        self.machine_name = self.machine_name.trim().to_string();
        self.worker_name = self.worker_name.trim().to_string();
    }

    pub fn validate(&self) -> Result<(), SettingsError> {
        if let Some(a) = &self.payout_address {
            validate_address(a).map_err(SettingsError::Invalid)?;
        }
        match (&self.work_source, &self.work_source_backup) {
            (_, WorkSourceSetting::Unset) => {}
            (WorkSourceSetting::Stub, _) | (_, WorkSourceSetting::Stub) => {
                return Err(SettingsError::Invalid("the stub source has no backup and is nobody's backup".into()));
            }
            (a, b) if a == b => {
                return Err(SettingsError::Invalid("the backup source must differ from the first".into()));
            }
            _ => {}
        }
        self.limits.validate().map_err(SettingsError::Invalid)?;
        self.tariff.validate().map_err(SettingsError::Invalid)?;
        if let Some(b) = self.power_budget_w {
            if !b.is_finite() || b < 5.0 || b > 20_000.0 {
                return Err(SettingsError::Invalid("the power budget must be between 5 and 20000 W".into()));
            }
        }
        for (name, v) in [("wall watts", self.costs.wall_watts), ("price per IGN", self.costs.price_per_ign), ("host allowance", self.costs.host_allowance_w)] {
            if let Some(v) = v {
                if !v.is_finite() || v < 0.0 || v > 1e9 {
                    return Err(SettingsError::Invalid(format!("{name} must be a number from 0 up")));
                }
            }
        }
        if !self.costs.other_per_day.is_finite() || self.costs.other_per_day < 0.0 {
            return Err(SettingsError::Invalid("other costs must be a number from 0 up".into()));
        }
        if self.costs.currency.len() != 3 || !self.costs.currency.bytes().all(|b| b.is_ascii_uppercase()) {
            return Err(SettingsError::Invalid("the currency is a three-letter code".into()));
        }
        match &self.work_source {
            WorkSourceSetting::PoolCustom { host, port, .. } => {
                validate_host(host).map_err(SettingsError::Invalid)?;
                if *port == 0 {
                    return Err(SettingsError::Invalid("the port must be 1 to 65535".into()));
                }
            }
            WorkSourceSetting::Solo { rpc_url } => validate_rpc_url(rpc_url).map_err(SettingsError::Invalid)?,
            WorkSourceSetting::Pool { pool_id } => {
                if pool_id.is_empty() || pool_id.len() > 64 || !pool_id.bytes().all(|b| b.is_ascii_alphanumeric() || b == b'-' || b == b'_') {
                    return Err(SettingsError::Invalid("the pool id is letters, digits, - and _".into()));
                }
            }
            _ => {}
        }
        // a pool's block session is switched by the worker name's ".block" suffix (the project's word of 15:4x UK, 13 October
        // 2026): the one dot allowed is that suffix
        let worker_stem = self.worker_name.strip_suffix(".block").unwrap_or(&self.worker_name);
        if self.machine_name.len() > 40 || self.worker_name.len() > 32 || !worker_stem.bytes().all(|b| b.is_ascii_alphanumeric() || b == b'-' || b == b'_') {
            return Err(SettingsError::Invalid("names: the machine name up to 40 characters, the worker name letters, digits, - and _ up to 32 (a .block suffix for a block session)".into()));
        }
        if let Some(a) = &self.algo {
            if a.len() > 32 || !a.bytes().all(|b| b.is_ascii_alphanumeric() || b == b'-' || b == b'_' || b == b'.') {
                return Err(SettingsError::Invalid("the algorithm id is letters, digits, dots, dashes and underscores, up to 32".into()));
            }
        }
        if !["system", "light", "dark"].contains(&self.theme.as_str()) {
            return Err(SettingsError::Invalid("theme is system, light or dark".into()));
        }
        self.time_of_use.validate().map_err(SettingsError::Invalid)?;
        for (fp, p) in &self.pins {
            if fp.len() != 64 || p.len() > 16 {
                return Err(SettingsError::Invalid("a pin names a device fingerprint and at most 16 knobs".into()));
            }
        }
        Ok(())
    }

    pub fn load(path: &Path) -> Result<Settings, SettingsError> {
        match std::fs::read_to_string(path) {
            Ok(s) => {
                let v: serde_json::Value = serde_json::from_str(&s)?;
                let ver = v.get("version").and_then(|x| x.as_u64()).unwrap_or(0) as u32;
                if ver > SETTINGS_VERSION {
                    return Err(SettingsError::Newer(ver));
                }
                let mut st: Settings = serde_json::from_value(v)?;
                st.version = SETTINGS_VERSION;
                st.normalise();
                st.validate()?;
                Ok(st)
            }
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => Ok(Settings::default()),
            Err(e) => Err(e.into()),
        }
    }

    pub fn save(&self, path: &Path) -> Result<(), SettingsError> {
        self.validate()?;
        if let Ok(s) = std::fs::read_to_string(path) {
            if let Ok(v) = serde_json::from_str::<serde_json::Value>(&s) {
                let ver = v.get("version").and_then(|x| x.as_u64()).unwrap_or(0) as u32;
                if ver > SETTINGS_VERSION {
                    return Err(SettingsError::Newer(ver));
                }
            }
        }
        write_atomic(path, &serde_json::to_vec_pretty(self)?)?;
        Ok(())
    }

    pub fn device_enabled(&self, fingerprint: &str, default_enabled: bool) -> bool {
        if self.devices_off.iter().any(|f| f == fingerprint) {
            return false;
        }
        if self.devices_on.iter().any(|f| f == fingerprint) {
            return true;
        }
        default_enabled
    }
}

/// Temp file beside the target, then rename, so a crash never leaves a half-written settings file.
pub fn write_atomic(path: &Path, bytes: &[u8]) -> std::io::Result<()> {
    if let Some(dir) = path.parent() {
        std::fs::create_dir_all(dir)?;
    }
    let tmp = path.with_extension("tmp");
    std::fs::write(&tmp, bytes)?;
    std::fs::rename(&tmp, path)
}

/// An Ethereum-style address: 0x and 40 hex characters; mixed case must be a valid EIP-55 checksum.
/// Returns the checksummed form.
pub fn validate_address(s: &str) -> Result<String, String> {
    let s = s.trim();
    let hex = s.strip_prefix("0x").or_else(|| s.strip_prefix("0X")).ok_or("an address starts with 0x")?;
    if hex.len() != 40 || !hex.bytes().all(|b| b.is_ascii_hexdigit()) {
        return Err("an address is 0x followed by 40 hex characters".into());
    }
    let lower = hex.to_ascii_lowercase();
    let checksummed = eip55(&lower);
    let has_upper = hex.bytes().any(|b| b.is_ascii_uppercase());
    let has_lower = hex.bytes().any(|b| b.is_ascii_lowercase());
    if has_upper && has_lower && checksummed[2..] != *hex {
        return Err("the capital letters in this address do not match its checksum; one character is wrong".into());
    }
    Ok(checksummed)
}

fn eip55(lower_hex: &str) -> String {
    let digest = Keccak256::digest(lower_hex.as_bytes());
    let mut out = String::with_capacity(42);
    out.push_str("0x");
    for (i, c) in lower_hex.chars().enumerate() {
        let nibble = (digest[i / 2] >> (if i % 2 == 0 { 4 } else { 0 })) & 0xf;
        if c.is_ascii_digit() {
            out.push(c);
        } else if nibble >= 8 {
            out.push(c.to_ascii_uppercase());
        } else {
            out.push(c);
        }
    }
    out
}

pub fn validate_host(h: &str) -> Result<(), String> {
    if h.is_empty() || h.len() > 253 {
        return Err("a host name is 1 to 253 characters".into());
    }
    if !h.bytes().all(|b| b.is_ascii_alphanumeric() || b == b'.' || b == b'-' || b == b':' || b == b'[' || b == b']') {
        return Err("a host name is letters, digits, dots and dashes".into());
    }
    Ok(())
}

/// One or more node addresses (the project's ruling of 11 October 2026 after ecosystem's reconciliation of the 4070 row:
/// until share gossip lands, a solo miner submits every share to every sealer it knows), separated by commas or
/// whitespace; every one validated.
pub fn validate_rpc_url(u: &str) -> Result<(), String> {
    let list = rpc_url_list(u);
    if list.is_empty() {
        return Err("the node address starts with http:// or https://".into());
    }
    for one in list {
        let rest = one.strip_prefix("http://").or_else(|| one.strip_prefix("https://")).ok_or("the node address starts with http:// or https://")?;
        let host = rest.split('/').next().unwrap_or("");
        let host = host.split('@').last().unwrap_or("");
        let host_only = host.rsplit_once(':').map(|(h, _)| h).unwrap_or(host);
        validate_host(host_only)?;
    }
    Ok(())
}

/// The node addresses in a `Solo { rpc_url }` setting, in the order given (the first that answers serves the
/// work; every one takes the shares).
pub fn rpc_url_list(u: &str) -> Vec<String> {
    u.split(|c: char| c == ',' || c.is_whitespace()).map(str::trim).filter(|s| !s.is_empty()).map(str::to_string).collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn eip55_known_vectors() {
        // vectors from the EIP-55 text
        assert_eq!(validate_address("0x5aaeb6053f3e94c9b9a09f33669435e7ef1beaed").unwrap(), "0x5aAeb6053F3E94C9b9A09f33669435E7Ef1BeAed");
        assert_eq!(validate_address("0xfB6916095ca1df60bB79Ce92cE3Ea74c37c5d359").unwrap(), "0xfB6916095ca1df60bB79Ce92cE3Ea74c37c5d359");
        assert!(validate_address("0xfB6916095ca1df60bB79Ce92cE3Ea74c37c5d358").is_err());
        assert!(validate_address("0x5AAEB6053F3E94C9B9A09F33669435E7EF1BEAED").is_ok());
        assert!(validate_address("5aaeb6053f3e94c9b9a09f33669435e7ef1beaed").is_err());
        assert!(validate_address("0x5aaeb6053f3e94c9b9a09f33669435e7ef1beae").is_err());
    }

    #[test]
    fn defaults_validate_and_round_trip() {
        let s = Settings::default();
        s.validate().unwrap();
        let dir = std::env::temp_dir().join(format!("igneum-settings-test-{}", std::process::id()));
        let p = dir.join("settings.json");
        s.save(&p).unwrap();
        let back = Settings::load(&p).unwrap();
        assert_eq!(back, s);
        std::fs::write(&p, "{\"version\": 99}").unwrap();
        assert!(matches!(Settings::load(&p), Err(SettingsError::Newer(99))));
        assert!(matches!(s.save(&p), Err(SettingsError::Newer(99))));
        let _ = std::fs::remove_dir_all(&dir);
    }

    #[test]
    fn invalid_inputs_are_named() {
        let mut s = Settings::default();
        s.costs.currency = "pounds".into();
        assert!(s.validate().is_err());
        s.costs.currency = "GBP".into();
        s.work_source = WorkSourceSetting::Solo { rpc_url: "ftp://x".into() };
        assert!(s.validate().is_err());
        s.work_source = WorkSourceSetting::Solo { rpc_url: "http://127.0.0.1:8545".into() };
        // several nodes in one setting (the project's ruling of 11 October 2026): every one validated, the order kept
        assert_eq!(rpc_url_list("http://10.0.0.2:8648, http://10.0.0.3:8648"), vec!["http://10.0.0.2:8648", "http://10.0.0.3:8648"]);
        assert_eq!(rpc_url_list("http://a:1\nhttp://b:2 ,,"), vec!["http://a:1", "http://b:2"]);
        assert!(rpc_url_list(" , ").is_empty());
        assert!(validate_rpc_url("http://10.0.0.2:8648,http://10.0.0.3:8648").is_ok());
        assert!(validate_rpc_url("http://10.0.0.2:8648,ftp://x").is_err(), "one bad address refuses the list");
        assert!(validate_rpc_url("").is_err());
        assert!(s.validate().is_ok());
        s.work_source = WorkSourceSetting::PoolCustom { host: "pool.example".into(), port: 0, tls: true };
        assert!(s.validate().is_err());
    }

    #[test]
    fn device_switches() {
        let mut s = Settings::default();
        assert!(s.device_enabled("a", true));
        assert!(!s.device_enabled("a", false));
        s.devices_on.push("a".into());
        assert!(s.device_enabled("a", false));
        s.devices_off.push("a".into());
        assert!(!s.device_enabled("a", true));
    }
}
