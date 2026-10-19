//! The state snapshot the interface renders and the CLI prints. Everything the user sees is in here, with its
//! label, so the interface never computes a number itself.

use crate::devices::SupportLabel;
use crate::economics::{Estimate, Line};
use crate::modes::{Limits, Mode};
use crate::schedule::PauseCause;
use crate::settings::Settings;
use crate::tuner::StopReason;
use igneum_worker_proto::{Params, Telemetry};
use serde::{Deserialize, Serialize};

#[derive(Debug, Clone, Copy, Serialize, Deserialize, PartialEq, Eq)]
#[serde(rename_all = "snake_case")]
pub enum SlotState {
    Off,
    Idle,
    Opening,
    /// Not mining: the worker builds the dataset or kernels, or moves to the next epoch (`phase` says which).
    Preparing,
    Mining,
    Paused,
    Tuning,
    Refused,
    Faulted,
}

/// The transition state machine of the specification's section 9, per device.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(tag = "state", rename_all = "snake_case")]
pub enum Transition {
    /// Mining the current epoch's dataset; nothing in flight.
    Current,
    /// The next epoch's dataset is being built in the background within the budget; the old one stays
    /// immutable and mining continues on it.
    Preparing { to: u64 },
    /// The next dataset is built and waiting for the chain to cross the boundary.
    Ready { to: u64 },
    /// The first job of the new epoch is issued; old work cancelled; the worker swaps datasets.
    Switching { from: u64, to: u64 },
    /// A fault during a transition: the worker restarts, rebuilds cold, resumes.
    Recovering { to: u64 },
}

/// Energy per accepted hash for one device, from its own telemetry, with the instrument grade named: the app and
/// the bench speak the same number (the GPU team's DESIGN section 5: accepted work per watt-hour, stated with its grade).
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct EnergyView {
    /// Joules per validated (accepted-equivalent, difficulty-weighted) hash.
    pub joules_per_hash: f64,
    /// The bench's unit: megahashes per watt-hour.
    pub mh_per_wh: f64,
    /// Mean power over the same window, in watts.
    pub power_w: f64,
    /// The instrument: "telemetry" (the device's own board-power reading), "wall" (a meter the user entered, machine
    /// wide), "estimated" (device readings plus the host allowance).
    pub grade: String,
}

/// One epoch transition as the supervisor measured it (section 9: the full cycle, not the kernel build alone).
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct TransitionRecord {
    pub device: u32,
    pub algo: String,
    pub params_digest: String,
    pub from_epoch: u64,
    pub to_epoch: u64,
    /// `warm` (prepared in the background) or `cold` (release, build, resume).
    pub path: String,
    /// Milliseconds from the first job of the new epoch to the first stat reporting it as mining.
    pub switch_ms: u64,
    /// Seconds the device spent not mining across the transition.
    pub gap_s: f64,
    /// Validated hashes the gap cost at the device's rate before it: the work charged to the variant.
    pub lost_validated_hashes: f64,
    /// The dataset digest the worker reported for the new epoch, or "not reported by this worker".
    pub digest: String,
    pub recovered: bool,
    pub at_unix: u64,
    /// The validated rate before the transition began.
    #[serde(default)]
    pub rate_before_hps: f64,
    /// The reported rate while the next dataset was being built beside mining (the warm path's overlap; zero
    /// means the build stalled mining).
    #[serde(default)]
    pub rate_while_building_hps: f64,
    /// The background build's length (prepare sent to prepared), ms.
    #[serde(default)]
    pub build_ms: u64,
    /// The slices the swap had to finish (the worker's last_swap_wait_ms), when it reports one.
    #[serde(default)]
    pub swap_wait_ms: Option<u64>,
    /// The document's bonded rounds S (SPEC 0.9, bit 10) the transition was crossed under; 0 for an unkeyed
    /// document. Launch gate L4 reads the transition and parity rows under the keyed form by this tag.
    #[serde(default)]
    pub keyed_rounds: u32,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct TuneView {
    pub running: bool,
    pub chosen: Params,
    pub candidates: usize,
    pub stopped: Option<StopReason>,
    pub when_unix: Option<u64>,
    pub fresh: bool,
    pub current_candidate: Option<Params>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct LaneView {
    pub coord: u8,
    pub word: String,
    pub accepted: u64,
    pub hps: f64,
    pub seconds: f64,
    pub current: bool,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct SlotView {
    pub fingerprint: String,
    /// The worker's id for the device.
    pub id: String,
    /// The stable device address (PCI bus id, or the registry id on Apple).
    pub bus_id: String,
    pub index: u32,
    pub backend: String,
    pub name: String,
    pub vendor: String,
    pub kind: String,
    pub memory_total: u64,
    pub memory_free: u64,
    pub driver: String,
    pub arch: String,
    pub runtime: String,
    pub label: SupportLabel,
    pub reason: String,
    pub enabled: bool,
    pub state: SlotState,
    pub message: String,
    /// VectorWork (router.rs): the coordinate this card mines, and the router's last word for it.
    #[serde(default)]
    pub coord: Option<u8>,
    #[serde(default)]
    pub coord_word: String,
    /// The lane the card mines by its name (the bandwidth lane, the programme lane, the raster lane), from the
    /// document's version (the project's read of 16 October 2026: no "coordinate 3", "version 21" or "Anvil" in a string a miner sees).
    #[serde(default)]
    pub lane_word: String,
    /// The card's rows per lane this session: shares accepted on it, the last rate read on it, the seconds on it.
    #[serde(default)]
    pub lanes: Vec<LaneView>,
    /// While preparing: `dataset`, `compile` or `transition`.
    pub phase: String,
    /// The epoch the worker holds, once it holds one.
    pub epoch: Option<u64>,
    /// The candidate id (the algorithm version) the device is configured for.
    pub algo: String,
    pub params: Params,
    pub pinned: Params,
    pub knobs: Vec<igneum_worker_proto::Knob>,
    pub telemetry: Option<Telemetry>,
    /// The telemetry fields this worker reports; a cap on a field not listed shows "not reported by this worker".
    pub reports: Vec<String>,
    /// Validated work (shares checked on the CPU), hashes per second, 10-minute mean.
    pub validated_hps: f64,
    /// The worker's own count: a display counter, never a decision.
    pub reported_hps: f64,
    /// The worker's own count over the last minute only, and the mean board power over it: the parity rows read
    /// three consecutive minutes from the state (11 October 2026).
    #[serde(default)]
    pub reported_60s_hps: f64,
    /// the reported rate over the last 10 s (the tune's sample: a step's own rate, not the minute before it)
    #[serde(default)]
    pub reported_10s_hps: f64,
    /// seconds the card has mined its current stint (since the last lane switch); 0 when not mining
    #[serde(default)]
    pub stint_s: f64,
    #[serde(default)]
    pub power_60s_w: Option<f64>,
    pub invalid_results: u64,
    pub restarts: u32,
    pub worker: String,
    pub worker_version: String,
    pub worker_note: String,
    pub tune: Option<TuneView>,
    pub transition: Transition,
    /// The epoch the device's dataset is for; the next one when prepared.
    pub dataset_epoch: Option<u64>,
    pub next_epoch: Option<u64>,
    /// The worker's budget allows a warm transition (two datasets resident).
    pub can_warm: bool,
    /// Job-switch delays measured on this device (first stat for the new job after the template changed).
    pub switch_median_ms: Option<u64>,
    pub switch_p99_ms: Option<u64>,
    pub switch_samples: usize,
    /// Energy per accepted hash from this device's telemetry, with its grade; None when the device reports no
    /// power or has no validated work yet.
    pub energy: Option<EnergyView>,
    /// The next epoch's build in progress on the device, 0 to 1 (the epoch-change indicator, not a stall).
    pub build_progress: Option<f64>,
    /// The cohort's measured power-cap lever for this card family, offered and never applied (the plan's point
    /// 10: automatic per-card tuning stops at what the app may set itself).
    pub power_offer: Option<String>,
    /// F-E18 (14 October 2026): the cap's words for the row: "cap N W applied" only when the driver read it back,
    /// else "cap N W requested, not applied: <the reason>"; empty with no cap set (Linux, NVIDIA).
    #[serde(default)]
    pub power_cap: String,
}

/// A pool's receipts as counted (DESIGN.md 16.3): every `mining.receipt`, the verified (v1 or v2) and the failed digests,
/// and the last receipt's two fee fields (what the member was charged, inside the signed message).
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Default)]
pub struct ReceiptCounts {
    pub total: u64,
    pub v1_verified: u64,
    pub v1_failed: u64,
    pub last_dev_fee_percent: u32,
    pub last_pool_fee_bps: u32,
    #[serde(default)]
    pub last_failure: String,
    /// The receipts file this session writes (`<data dir>/receipts/<pool host>-<session start unix>.jsonl`, one
    /// receipt line per accepted share as the pool sent it); empty before the first receipt or on a node session.
    #[serde(default)]
    pub file: String,
    /// The weight the pool stamped on the last receipt (the share's weight at its step; 50 at step 0 on devnet9).
    #[serde(default)]
    pub last_weight: u64,
}

/// One accepted share as the scene draws it (the founder's brief of 12 October 2026: a person watches their own
/// shares landing in blocks): when, at which height, at which step and weight, on which job, the fee session's or
/// the member's.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Default)]
pub struct ShareEvent {
    pub at_unix: u64,
    pub height: u64,
    pub step: u8,
    pub weight: u64,
    pub job: String,
    pub fee: bool,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Default)]
pub struct Totals {
    pub validated_hps: f64,
    pub reported_hps: f64,
    pub device_watts: f64,
    /// Energy per accepted hash machine wide: wall grade when the user entered a meter reading, else estimated
    /// (device readings plus the host allowance); None before any validated work.
    pub energy: Option<EnergyView>,
    pub accepted: u64,
    pub rejected: u64,
    pub stale: u64,
    /// Shares that failed the miner's own verification against the reference.
    pub invalid: u64,
    /// Shares the source recomputed and refused as invalid (its implementation disagrees with the reference).
    pub source_invalid: u64,
    /// Results dropped because the verifier threads were behind (never counted, never submitted).
    pub unverified: u64,
    pub blocks_found: u64,
    pub worker_restarts: u64,
    /// Slots the tick reopened after sitting idle with no worker under a running layer (the mini, 12 October 2026).
    #[serde(default)]
    pub slot_reopens: u64,
    pub uptime_s: u64,
    /// Seconds with no working device while mining was requested (downtime, the directive's measure).
    pub downtime_s: u64,
    /// Seconds a device spent preparing (dataset, compile, transition): preparation downtime, chapter 10's measure.
    pub preparing_s: u64,
    /// The longest supervisor tick of the last minute (the responsiveness figure of the supervisor itself).
    pub tick_max_ms: u64,
    /// The reference's self-check time for the current document, ms (F-22).
    pub selfcheck_ms: u64,
    /// Median verification latency of recent results, ms (queued to checked).
    pub verify_median_ms: u64,
    /// Results checked after their template moved on (counted inside cancelled too).
    pub verify_late: u64,
    /// Results that failed the reference within TRANSITION_GRACE_S of the slot's period transition (the worker's last
    /// batch under the old programme tagged with the new job: the GPU team's row (2) of 15 October 2026, one per period);
    /// counted here, never toward a coordinate's refusal or a restart.
    #[serde(default)]
    pub straddled: u64,
    /// Moves between the first and the backup source.
    pub source_switches: u64,
    /// Reconnects forced by unanswered submissions.
    pub source_reconnects: u64,
    /// Results submitted to the source (blocks when solo, shares at a pool).
    pub submitted: u64,
    /// Seconds since the last submission and since the last answer; None before the first.
    pub last_submit_age_s: Option<u64>,
    pub last_outcome_age_s: Option<u64>,
    /// The expected seconds between blocks for this miner alone at the source's difficulty and the validated
    /// rate (difficulty over rate); None without both. A silence shorter than this is a block not due yet.
    pub expected_block_s: Option<f64>,
    /// Shares cancelled by a template switch: results for a superseded job, never submitted.
    pub cancelled: u64,
    /// The pool's refusal reasons, counted by their first word.
    #[serde(default)]
    pub reject_reasons: std::collections::BTreeMap<String, u64>,
    /// Results under the pool's target held back by the submission cap (the target too low for the rate).
    #[serde(default)]
    pub not_submitted: u64,
    /// Shares the pool refused BEFORE acceptance (pool/0.1.6 and later; F-50 of 12 October 2026: the reason reads
    /// "refused before acceptance: <case>: ..."): the pool at carriage, the address at cap, the node's pool at
    /// capacity, the list bound. Never a rejected share and never against the session; the case word is the
    /// reason's second field, the last one seen.
    #[serde(default)]
    pub held: u64,
    /// F-E66: shares the pool's own submit bucket paced (dropped and counted as the session's rate), never rejections.
    #[serde(default)]
    pub paced_by_pool: u64,
    #[serde(default)]
    pub held_case: String,
    /// Worker lines of a message kind this protocol version does not name (ignored, never a restart).
    #[serde(default)]
    pub unknown_messages: u64,
    /// The share lane (docs/igneumhash/LAUNCH-GUARDS.md 2.9): true while the node serves a share target and the
    /// workers search the share header; the node's operating count of shares a block beside it.
    #[serde(default)]
    pub share_lane: bool,
    #[serde(default)]
    pub shares_per_block: u64,
    /// The pacing in force on the share lane, in words (share_pacing_word): "measurement ..." or the lab
    /// "chain-target ..." of EMBER_PACE_CHAIN_TARGET; empty off the lane. The minute line and the record name it.
    #[serde(default)]
    pub share_pacing: String,
    /// The dev fee (devfee.rs, ED-05): the percent in force (0 off), the dev payout address it pays (empty when
    /// off), the words for the start line and the app, the templates requested with the dev address this session
    /// and the shares accepted on them (the fee's own count, beside the miner's).
    #[serde(default)]
    pub dev_fee_percent: u8,
    #[serde(default)]
    pub dev_fee_address: String,
    #[serde(default)]
    pub dev_fee_words: String,
    #[serde(default)]
    pub dev_fee_templates: u64,
    #[serde(default)]
    pub dev_fee_shares_accepted: u64,
    /// The dev fee's record file this session (`<data dir>/receipts/dev-fee-<session start unix>.jsonl`: one line
    /// per fee template issued on a node session and one per share accepted on it); empty until its first line.
    #[serde(default)]
    pub dev_fee_record_file: String,
    /// The fee-waiver handshake (DESIGN.md 16.3) on a pool session: the pool's answer to the dev-fee declaration
    /// (None until it answers or when the source is a node), the two fees in one line ("Dev fee 1 percent; pool
    /// fee 0 (waived)"), and the pool's receipts as counted with their fee fields.
    #[serde(default)]
    pub pool_fee: Option<crate::devfee::PoolFeeAnswer>,
    #[serde(default)]
    pub pool_fee_answered: bool,
    /// The pool's share step for this session (ecosystem's mining.notify field, 12 October 2026): an accepted
    /// share weighs 2^step on the chain; 0 for the count form or a node session.
    #[serde(default)]
    pub share_step: u8,
    #[serde(default)]
    pub fee_words: String,
    #[serde(default)]
    pub receipts: ReceiptCounts,
    /// Shares the node accepted into its pool, and shares it refused, by its reason (its words, counted).
    #[serde(default)]
    pub shares_accepted: u64,
    #[serde(default)]
    pub shares_refused: u64,
    #[serde(default)]
    pub share_refused_reasons: std::collections::BTreeMap<String, u64>,
    /// The share lane by node (the project's ruling of 11 October 2026: every share goes to every node the source names
    /// until share gossip lands): each node's accepted and refused counts and its reasons, in the setting's order.
    #[serde(default)]
    pub share_nodes: Vec<NodeAnswers>,
    /// Epoch transitions completed this run.
    pub transitions: u64,
    /// Validated hashes lost to transitions (the work charged to the variant).
    pub transition_lost_hashes: f64,
    /// Job-switch delay across devices: median and p99 (section 10's product targets).
    pub switch_median_ms: Option<u64>,
    pub switch_p99_ms: Option<u64>,
    pub switch_samples: usize,
}

/// One node's answers on the share lane: accepted, refused, and the refusals by the node's own reason.
#[derive(Debug, Clone, Default, Serialize, Deserialize, PartialEq)]
pub struct NodeAnswers {
    pub node: String,
    pub accepted: u64,
    pub refused: u64,
    #[serde(default)]
    pub reasons: std::collections::BTreeMap<String, u64>,
}

#[derive(Debug, Clone, Copy, Serialize, Deserialize, PartialEq, Eq)]
#[serde(rename_all = "snake_case")]
pub enum MiningState {
    Idle,
    Starting,
    Mining,
    Paused,
    Stopping,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct SourceView {
    pub name: String,
    pub algo: String,
    pub connected: bool,
    pub problem: Option<String>,
    pub solo: bool,
    pub difficulty: f64,
    pub current_job: Option<String>,
    pub height: u64,
    /// The chain the source's job names (a pool's chain_id); 0 until a job names one.
    #[serde(default)]
    pub chain_id: u64,
    /// The relation mined, from the served document: "IgneumHash version N (coordinate name)"; empty until a document is held.
    #[serde(default)]
    pub relation: String,
    /// F-E14: seconds since the last job on a connected source while mining (0 when work flows or nothing is connected).
    #[serde(default)]
    pub waiting_for_work_s: u64,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct Stage {
    pub name: String,
    /// `pending`, `active`, `done`, `failed`
    pub status: String,
    pub detail: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct Notice {
    pub level: String,
    pub text: String,
    pub detail: String,
}

/// F-E24: a newer Ember on the signed pointer, with the one command that takes it on this host.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct UpdateOffer {
    pub version: String,
    pub running: String,
    pub url: String,
    pub sha256: String,
    pub size: u64,
    pub directory: String,
    /// The one command for this host (the Linux archive: fetch, verify the sha, unpack, run install.sh again
    /// with the config kept); the service does not replace itself.
    pub command: String,
    pub policy: String,
    pub read_at_unix: u64,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct Snapshot {
    pub app: String,
    pub version: String,
    pub stub: bool,
    pub mining: MiningState,
    pub paused_for: Option<PauseCause>,
    /// The OS memory-pressure level the guard last read ("normal", "warning", "critical"); None where the
    /// platform has no reading.
    #[serde(default)]
    pub memory_pressure: Option<String>,
    pub mode: Mode,
    pub limits: Limits,
    pub devices: Vec<SlotView>,
    pub totals: Totals,
    pub source: Option<SourceView>,
    pub estimate: Option<Estimate>,
    pub estimate_lines: Vec<Line>,
    /// F-E24 (E2, 14 October 2026): on a host with no window the update policy "ask" asks nobody, so the offer
    /// stands here and in the log with the one command that takes it; None when the running version is the
    /// pointer's or the pointer was not read.
    #[serde(default)]
    pub update_offer: Option<UpdateOffer>,
    pub stages: Vec<Stage>,
    pub first_work_accepted: bool,
    pub notices: Vec<Notice>,
    pub settings: Settings,
    pub log_lines: usize,
    pub tune_running: bool,
    pub over_power_budget_w: Option<f64>,
    pub pools_status: String,
    /// The last transitions, newest last.
    pub transitions: Vec<TransitionRecord>,
    /// The last accepted shares, oldest first (up to RECENT_SHARES): the scene's feed (api/scene).
    #[serde(default)]
    pub recent_shares: Vec<ShareEvent>,
    /// The parameter document digest the run is on, as the miner tags it.
    pub params_digest: String,
    /// How long since the supervisor's tick last ran, ms (F-25: a dead tick is a named stall).
    pub tick_age_ms: u64,
    /// Why the tick failed or stalled, when it did.
    pub tick_fault: Option<String>,
    /// Workers that did not start at the last inventory, with the reason.
    pub launch_errors: Vec<String>,
}

/// 0.2.27 (the review lead's rider on F-439, 17 October 2026): a rate under a hundred thousand a second is the raster lane's
/// attempts, said as such; above it the figure is hashes, said in MH/s. The status line printed the Mac proof host's
/// 880 attempts a second as "0.00 MH/s".
pub fn rate_words(hps: f64) -> String {
    if hps > 0.0 && hps < 100_000.0 { format!("{hps:.0} attempts/s") } else { format!("{:.2} MH/s", hps / 1e6) }
}

#[cfg(test)]
mod rate_words_tests {
    use super::*;
    #[test]
    fn a_small_rate_is_attempts_a_second_and_a_large_one_megahashes() {
        assert_eq!(rate_words(880.0), "880 attempts/s", "the raster lane's figure");
        assert_eq!(rate_words(53_400_000.0), "53.40 MH/s", "a hashing card");
        assert_eq!(rate_words(0.0), "0.00 MH/s", "no rate at all");
        assert_eq!(rate_words(100_000.0), "0.10 MH/s", "the boundary is hashes");
    }
}
