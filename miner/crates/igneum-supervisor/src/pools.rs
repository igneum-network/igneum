//! Pool discovery (design section 2.7): fees, payout conditions, reliability, concentration, the operator;
//! nothing preselected, nothing hard-wired to a project pool, project pools labelled.

use ed25519_dalek::{Signature, Verifier, VerifyingKey};
use serde::{Deserialize, Serialize};

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct PoolListing {
    pub id: String,
    pub name: String,
    pub host: String,
    pub port: u16,
    pub tls: bool,
    pub fee_pct: f64,
    /// `pplns`, `pps`, `fpps`, `solo`, ...
    pub payout_scheme: String,
    pub min_payout_ign: f64,
    pub payout_frequency: String,
    /// `pool` or `miner`
    pub who_pays_tx_fee: String,
    /// The pool's own statement of uptime over 30 days, percent.
    pub uptime_stated_pct: Option<f64>,
    /// The independent probe's figure over 30 days, percent.
    pub uptime_probed_pct: Option<f64>,
    /// The pool's share of network work over 7 days, percent, from the explorer.
    pub concentration_pct: Option<f64>,
    pub operator: String,
    /// Operated by Igneum Labs or anyone connected to the project.
    pub project_operated: bool,
    pub region: String,
    pub url: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Default)]
pub struct PoolList {
    #[serde(default)]
    pub version: u32,
    #[serde(default)]
    pub published_unix: u64,
    #[serde(default)]
    pub pools: Vec<PoolListing>,
}

#[derive(Debug, Clone, Copy, Serialize, Deserialize, PartialEq, Eq)]
#[serde(rename_all = "snake_case")]
pub enum ConcentrationBand {
    Unknown,
    Low,
    Notable,
    /// Above a third of the network's work: a warning in the interface.
    Warning,
}

pub fn concentration_band(pct: Option<f64>) -> ConcentrationBand {
    match pct {
        None => ConcentrationBand::Unknown,
        Some(p) if p > 33.0 => ConcentrationBand::Warning,
        Some(p) if p > 15.0 => ConcentrationBand::Notable,
        Some(_) => ConcentrationBand::Low,
    }
}

/// Default order: concentration ascending (unknown last), then fee ascending, then name. Never by anything that
/// favours the project.
pub fn sort_for_display(pools: &mut [PoolListing]) {
    pools.sort_by(|a, b| {
        let ca = a.concentration_pct.unwrap_or(f64::INFINITY);
        let cb = b.concentration_pct.unwrap_or(f64::INFINITY);
        ca.partial_cmp(&cb).unwrap_or(std::cmp::Ordering::Equal).then(a.fee_pct.partial_cmp(&b.fee_pct).unwrap_or(std::cmp::Ordering::Equal)).then(a.name.cmp(&b.name))
    });
}

/// The fee a pool's statement implies: accepted work valued at the subsidy versus what it paid, as a percent.
pub fn implied_fee_pct(value_of_accepted_ign: f64, paid_ign: f64) -> Option<f64> {
    if value_of_accepted_ign <= 0.0 || paid_ign < 0.0 {
        return None;
    }
    Some(((value_of_accepted_ign - paid_ign) / value_of_accepted_ign * 100.0).max(0.0))
}

/// A difference worth a line: more than half a point between the stated fee and the implied one.
pub fn fee_discrepancy(stated_pct: f64, implied_pct: Option<f64>) -> Option<String> {
    let implied = implied_pct?;
    if (implied - stated_pct).abs() > 0.5 {
        Some(format!("the pool's paid total implies {implied:.1}%, it states {stated_pct:.1}%"))
    } else {
        None
    }
}

#[derive(Debug, thiserror::Error)]
pub enum PoolListError {
    #[error("the pool list is unsigned or its signature does not verify; only pools you add yourself are shown")]
    BadSignature,
    #[error("no publisher key is embedded in this build; only pools you add yourself are shown")]
    NoKey,
    #[error("the pool list is not readable: {0}")]
    Json(#[from] serde_json::Error),
}

/// The publisher's key (the ecosystem lead's), embedded at build time. Empty until published: the list then
/// fails closed and the interface says so.
pub const PUBLISHER_KEY: Option<[u8; 32]> = None;

/// `bytes` is `pools.json` exactly as served; `sig` is the detached 64-byte signature over those bytes.
pub fn load_signed(bytes: &[u8], sig: &[u8], key: Option<[u8; 32]>) -> Result<PoolList, PoolListError> {
    let key = key.ok_or(PoolListError::NoKey)?;
    let vk = VerifyingKey::from_bytes(&key).map_err(|_| PoolListError::BadSignature)?;
    let sig: [u8; 64] = sig.try_into().map_err(|_| PoolListError::BadSignature)?;
    vk.verify(bytes, &Signature::from_bytes(&sig)).map_err(|_| PoolListError::BadSignature)?;
    let list: PoolList = serde_json::from_slice(bytes)?;
    Ok(list)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn p(name: &str, fee: f64, conc: Option<f64>) -> PoolListing {
        PoolListing {
            id: name.into(),
            name: name.into(),
            host: "x".into(),
            port: 1,
            tls: true,
            fee_pct: fee,
            payout_scheme: "pplns".into(),
            min_payout_ign: 1.0,
            payout_frequency: "daily".into(),
            who_pays_tx_fee: "pool".into(),
            uptime_stated_pct: None,
            uptime_probed_pct: None,
            concentration_pct: conc,
            operator: "o".into(),
            project_operated: false,
            region: "eu".into(),
            url: "".into(),
        }
    }

    #[test]
    fn order_is_concentration_then_fee_unknown_last() {
        let mut v = vec![p("big", 0.5, Some(40.0)), p("unknown", 0.1, None), p("small-dear", 2.0, Some(5.0)), p("small-cheap", 1.0, Some(5.0))];
        sort_for_display(&mut v);
        let names: Vec<_> = v.iter().map(|x| x.name.as_str()).collect();
        assert_eq!(names, ["small-cheap", "small-dear", "big", "unknown"]);
        assert_eq!(concentration_band(Some(40.0)), ConcentrationBand::Warning);
        assert_eq!(concentration_band(None), ConcentrationBand::Unknown);
    }

    #[test]
    fn implied_fee_and_discrepancy() {
        assert!((implied_fee_pct(100.0, 98.6).unwrap() - 1.4).abs() < 1e-9);
        assert!(fee_discrepancy(1.0, Some(1.4)).is_none());
        assert!(fee_discrepancy(1.0, Some(2.0)).unwrap().contains("implies 2.0%"));
        assert_eq!(implied_fee_pct(0.0, 1.0), None);
    }

    #[test]
    fn a_list_without_a_key_fails_closed() {
        assert!(matches!(load_signed(b"{}", &[0u8; 64], None), Err(PoolListError::NoKey)));
        assert!(matches!(load_signed(b"{}", &[0u8; 64], Some([0u8; 32])), Err(PoolListError::BadSignature)));
    }
}
