//! The engine's state as the dashboard and the window host read it (`GET /api/state`), plus the event and log rings.

use serde::Serialize;
use std::collections::VecDeque;

/// The page's listener for the window and the installers (see State::page).
#[derive(Debug, Clone, Serialize, PartialEq, Default)]
pub struct PageState {
    pub port: u16,
    pub wanted: u16,
    pub lan: bool,
}

/// The work source's state for the window: `kind` pool or node (the settings), `url` as set, `name` as the
/// supervisor names it, `connected`, `problem` (the supervisor's words when it is not reachable or not answering),
/// `height` the source's last job height.
#[derive(Debug, Clone, Serialize, PartialEq, Default)]
pub struct SourceState {
    pub kind: String,
    pub url: String,
    pub name: String,
    pub connected: bool,
    pub problem: String,
    pub height: u64,
    /// The chain the source's job names (a pool's chain_id on its notify); 0 until a job names one.
    pub chain_id: u64,
    /// The relation mined as the supervisor names it from the served document ("IgneumHash version N (bandwidth)").
    pub relation: String,
    /// F-E14: seconds the engine has waited for a job on a connected source (0 while work flows).
    #[serde(default)]
    pub waiting_for_work_s: u64,
}

#[derive(Clone, Serialize, Default)]
pub struct NodeState {
    pub state: String, // stopped | starting | syncing | synced | restarting | failed
    pub blocks: u64,
    pub headers: u64,
    pub daa: u64,
    pub blue: u64,
    pub peers: u64,
    pub tips: u64,
    pub difficulty: f64,
    pub synced: bool,
    pub starts: u32,
    pub restarts: u32,
    pub restart_in_s: u64,
    pub pid: u32,
    pub version: String,
    pub last_reading_age_s: f64,
    pub message: String,
    /// N4 (6 October 2026, the frozen tip): seconds since the sink block's header time, from the watch line's
    /// tip_age_s (the node lane, ca3-v4-0316); -1 until the node reports it. "synced" needs it at or under 120.
    pub tip_age_s: f64,
    /// whose node this is (publish 2's read-back, 6 October 2026): "app" (started by this app), "external" (another
    /// node on this machine holds the ports; used after a check), "none" (the ports are taken by a node on other rules
    /// or another network, so no node is read), "" before the first start
    pub source: String,
    /// for an external node: match | mismatch | unknown (an older node that cannot answer the check)
    pub rules_check: String,
    /// the one line that says what happened at the ports, kept for the Node details
    pub port_note: String,
    /// where consensus_digest came from: "rpc" (igneum_getNodeInfo), "log" (the node's own stdout line), ""
    pub digest_source: String,
    /// which node this app reads: "own" (it started one), "external" (another node on this machine holds the ports),
    /// "none" (the ports are taken by a node it refuses), "" before the first decision. Re-decided when the other
    /// node goes away (7 October 2026): after 60 s the app starts its own on the freed ports.
    pub mode: String,
    /// why the mode is what it is, one line
    pub mode_reason: String,
    /// why the node is not "synced", in plain words ("" when synced): "behind" | "no peers" | "syncing" | "frozen"
    pub sync_cause: String,
    /// the miner's stall exits (code 45, "STALLED") since the node last started; the second one restarts the node
    pub stall_exits: u32,
    /// a consensus switch the signed manifest announced (difficulty v2 activation DAA); 0 = none
    pub consensus_switch_daa: u64,
    pub override_restart_wait: String,
    /// the node's "Consensus params digest: <64 hex>" line (exchanged in the p2p handshake); empty until the node prints it
    pub consensus_digest: String,
    /// every activation height in the override file the node was started with (miner-ui-2): the Node page names the next one
    pub consensus_switches: Vec<ConsensusSwitch>,
}

/// One planned rule change the node applies by itself at a DAA score (from the override file's `*_activation_daa` keys).
#[derive(Clone, Serialize, Default, PartialEq, Debug)]
pub struct ConsensusSwitch {
    pub key: String,  // the override key, for example fees_v1_activation_daa
    pub name: String, // plain words: Fees v1
    pub daa: u64,
}

#[derive(Clone, Serialize, Default, Debug)]
pub struct LaneRow {
    pub coord: u8,
    pub word: String,
    pub accepted: u64,
    pub hps: f64,
    pub seconds: f64,
    pub current: bool,
}

#[derive(Clone, Serialize, Default, Debug)]
pub struct CardState {
    pub index: usize,
    pub key: String,    // stable id for the saved preference: vendor:code, "#2" and up for a second identical card (no index: it moves)
    pub code: String,   // the tool's own device name (AMD's OpenCL runtime says "gfx1201"); the key and the tooltip carry it
    pub platform: String, // the OpenCL platform and driver the worker opens it through (the tooltip)
    pub kind: String,   // apple | discrete | integrated | external | unknown
    pub vram_mb: u64,   // 0 when unknown
    pub reason: String, // why it is off by default, if it is
    pub path: String,   // prebuilt | build (how the worker comes to be)
    pub name: String,
    pub vendor: String, // apple | nvidia | amd | other
    pub worker: String, // Metal | CUDA | OpenCL
    pub detail: String, // memory, cores
    pub device: String, // the worker's --device value (Windows)
    pub enabled: bool,
    pub state: String, // off | waiting | starting | ready | mining | restarting (the old base: a timed restart) | stopped (the worker layer: a card the guard closed, reopened by its switch) | failed | unusable (the OS reports a problem) | removed (unplugged)
    pub hash_now: f64, // MH/s, the last interval
    pub hash_avg: f64, // MH/s since the start
    pub accepted: u64,
    pub rejected: u64,
    pub identities: u32,
    pub ids: Vec<String>, // vote key hash short ids
    pub ready: bool,
    pub prepare: bool,
    pub prepared: bool,
    pub template_age_s: f64,
    pub synced: bool,
    pub restarts: u32,
    pub restart_in_s: u64,
    pub pid: u32,
    pub last_status_age_s: f64,
    pub message: String,
    /// VectorWork: the router's word for the card, shown beside its state on the Cards page.
    #[serde(default)]
    pub coord_word: String,
    /// The lane by its name (the bandwidth lane, the programme lane, the raster lane): the word a miner reads on the Cards page.
    #[serde(default)]
    pub lane_word: String,
    /// seconds the card has mined its current stint under the worker layer (since the last lane switch); the tune's steady rule
    #[serde(default)]
    pub stint_s: f64,
    /// the last 10 s reported rate (MH/s) under the worker layer: the tune's sample, never the page's
    #[serde(skip)]
    pub hash_10s: f64,
    /// The card's rows per lane this session (the Cards page): shares accepted on it, the last rate on it, the seconds on it.
    #[serde(default)]
    pub lanes: Vec<LaneRow>,
    /// the device's own problem as the OS reports it ("Code 43" on Windows); set = listed but no worker can drive it
    pub problem: String,
    /// PCI bus id where the tool gives one (nvidia-smi pci.bus_id); a second way to recognise a card whose index moved
    pub bus: String,
    /// How this card was joined to its worker slot ("bus", "device id", "vendor order"); logged when it changes
    #[serde(default)]
    pub slot_join: String,
    /// The join's line for the log, taken by the engine once it is written (empty after)
    #[serde(default, skip_serializing)]
    pub slot_join_line: String,
    /// hot-plug (src/hotplug.rs): unix s when a re-detection added this card (0 = found at start), when it lost it
    /// (0 = present), and `gone` once a removed card has been shown as removed for five minutes (the row hides)
    pub added_at: f64,
    pub removed_at: f64,
    pub gone: bool,
    /// `WORKER FAULT` lines seen (the miner killed and restarted its worker) and the miner's own `faults=` count
    pub faults: u64,
    /// shares that failed the miner's CPU re-check this run (`mismatched=` on the STATUS line)
    pub mismatched: u64,
    // power and heat (NVIDIA through nvidia-smi; 0 = unknown)
    pub power_w: f64,          // draw now
    pub power_limit_w: f64,    // the limit in force
    pub power_default_w: f64,  // the card's default limit
    pub power_min_w: f64,
    pub power_max_w: f64,
    pub power_before_w: f64,   // the limit before the app touched it (restored on quit)
    pub power_pct: u32,        // the chosen cap, percent of the default (50 to 100)
    pub power_applied: bool,
    pub power_note: String,
    pub temp_gpu: f64,
    pub temp_mem: f64,
    pub telemetry_at: f64,
    // AMD through igneum-gpu-telemetry (ADLX on Windows, amdgpu sysfs on Linux), 5 October 2026; 0 = unknown
    pub fan_pct: f64,
    pub fan_rpm: f64,
    pub mclk_mhz: f64,
    pub util_pct: f64,
    // hash per watt (src/sweep.rs)
    pub eff_mhw: f64,          // live: hash_now over power_w, MH per watt; 0 = unknown
    pub sweep_supported: bool, // NVIDIA with readable limits; the note says why not otherwise
    pub sweep_state: String,   // idle | running | unsupported
    pub sweep_note: String,    // the phase in words while running, else why unsupported or the last outcome
    pub sweep_pct: u32,        // the cap the last sweep chose (0 = never swept)
    pub sweep_eff: f64,        // MH per watt at that cap
    pub sweep_watts: f64,
    pub sweep_mhs: f64,
    pub sweep_at: f64,         // unix s of the last sweep
    pub pinned: bool,          // the user set the cap by hand; the sweep records but does not change it
    // Ember Tune (src/ember.rs): the two-knob tune, 5 October 2026
    pub clock_max_mhz: u32,    // the vendor's maximum core clock (0 = unknown)
    pub clock_min_mhz: u32,    // the vendor's floor for a cap (0 = 60% of the maximum)
    pub gclk_mhz: f64,         // core clock now
    pub clock_cap_mhz: u32,    // the cap in force (0 = unlocked)
    pub mem_cap_mhz: u32,      // Ember 2: the memory clock set by the tune (0 = the driver's default)
    pub amd_ordinal: i64,      // the `amd N` ordinal of igneum-gpu-telemetry (-1 = unknown)
    // an earlier release (src/ember.rs amd_knob): the stock clock the driver's max-clock offsets apply to, and whether it speaks offsets
    pub amd_stock_mhz: u32,
    pub amd_gmax_offset: bool,
    pub driver: String,        // the driver version (nvidia-smi, or the worker's race line)
    /// driver-check (7 October 2026): the comparable version the OS reports (nvidia-smi's for NVIDIA, Windows'
    /// DriverVersion for AMD and Intel; empty = none found) and what the row says about it against the manifest's table
    pub driver_os: String,
    pub driver_offer: Option<crate::drivers::Offer>,
    pub program_class: String, // the program class of the race line (loads and wide loads per hash); "" = unknown
    pub tune_control: bool,    // both knobs reach the card (else measure only; sweep_note says why)
    pub tune_clock_mhz: u32,   // the clock cap the last tune chose (0 = unlocked)
    pub tune_source: String,   // full | confirm | baseline
    pub tune_line: String,     // "Tuned: 122.3 MH/s at 290 W (0.422 MH/W)" once tuned
    pub tune_goal: String,     // this card's goal override (efficiency | balanced | rate); "" = the global goal
    pub tune_before_watts: f64, // the untuned point of the last full plan (step 0); 0 = never measured
    pub tune_before_mhs: f64,
    pub tune_floor: bool,      // the chosen clock is the ladder's floor (the row's sub-line says so)
    // a tune in progress on this card, by this engine or by a measurement engine posting /api/tune-progress
    // (the founder, 6 October 2026: "don't we need to show in the app that tuning is in progress?")
    pub tune_step: u32,
    pub tune_steps: u32,
    pub tune_eta_s: i64,
    pub tune_plan: String,
    // Ember 2: the memory clock the last tune chose and the measured curve (every row of the last plan)
    pub tune_mem_mhz: u32,
    pub tune_curve: Vec<serde_json::Value>,
    // the core-clock knob (7 October 2026, src/ember.rs lock_result; the UI lane's field shape): the chosen lock
    // against the cap point's unlocked row, the step while the clock search runs, the moment and the stop reason
    pub lock_mhz: u32,         // the chosen core clock cap (0 = unlocked)
    pub lock_mhs: f64,
    pub lock_w: f64,
    pub lock_mhw: f64,
    pub unlocked_mhs: f64,     // the cap point's row the lock is read against
    pub unlocked_w: f64,
    pub lock_step: u32,        // the clock search's step while it runs (0 otherwise)
    pub lock_steps: u32,
    pub lock_at: f64,          // unix s the lock point was taken (0 = never)
    pub lock_note: String,     // "rate fell 5.1 percent at 1200 MHz", "fingerprint mismatch at 1400 MHz, clocks reset", "the floor at 700 MHz", "no lever"
    // Ember Tune tiers (8 October 2026, src/ember.rs tiers; the UI lane's field shape): the three rows of the card's own
    // search (efficiency, balanced, max), the tier in force, why only stock exists, and when the tier was applied
    pub tiers: Vec<serde_json::Value>,
    pub tier: String,          // "" | efficiency | balanced | max
    pub tier_note: String,     // "" or "no lever: ..."
    pub tier_at: f64,          // unix s (0 = never)
    // the class flip (8 October 2026, the project's order): the program class the stored tiers were measured under, when the
    // class-flip re-run was queued (0 = not started), and the rate loss at the same lock under the new class (0 = unknown)
    pub tiers_class: String,
    pub tiers_remeasure_at: f64,
    /// tiers-table-26: the rows came from the team's table (app/igneum-app/tiers), not this card's own search
    pub tiers_table: bool,
    pub knee_loss_pct: f64,
    // the kernel variant race (docs/design/miner-tuning.md): what the worker's last race chose
    pub variant: String,
    pub race_mhs: f64,
    pub race_gain_pct: f64,
    pub race_variants: u32,
}

impl CardState {
    /// Listed, driver fine, not unplugged: a worker can run on it (hot-plug keeps removed and faulty cards in the
    /// list so the other cards' indices stay put).
    pub fn present(&self) -> bool {
        self.removed_at == 0.0 && self.problem.is_empty()
    }
}

#[derive(Clone, Serialize, Default)]
pub struct MiningState {
    pub state: String, // idle | waiting | mining | paused | stopped
    pub paused: bool,
    pub cards: Vec<CardState>,
    pub hash_total: f64,
    pub accepted_total: u64,  // lifetime
    pub accepted_session: u64,
    pub rejected_session: u64,
    /// F-564: results the engine cancelled as a superseded job's this session (never submitted); on the status line when above zero.
    #[serde(default)]
    pub cancelled_session: u64,
    /// Shares the pool held before acceptance under its admission bounds (F-50, 12 October 2026) and the case
    /// word ("pool at carriage"); never rejected shares.
    #[serde(default)]
    pub held_session: u64,
    #[serde(default)]
    pub held_case: String,
    /// F-E50 (14 October 2026): the engine's own notices (level, text, detail; the newest last), so the page's strip
    /// can say why nothing mines (a pinned coordinate the pool does not serve, a refused document) instead of
    /// "Not mining" with no reason.
    #[serde(default)]
    pub notices: Vec<igneum_supervisor::state::Notice>,
    pub found: Vec<f64>, // unix times of the accepted blocks, last hour
    /// The last accepted shares (the supervisor's recent_shares, up to 256, oldest first): the scene's feed
    /// through /api/scene (the founder's brief of 12 October 2026).
    #[serde(default)]
    pub shares: Vec<igneum_supervisor::state::ShareEvent>,
    /// dev-fee blocks (the miner's `dev-fee block` lines): this run, and lifetime
    pub fee_session: u64,
    pub fee_total: u64,
    /// Miner UI 4 (6 October 2026): the sum of the mining cards' draw (a reading under 60 s old), and that draw as
    /// £ a day at settings.power_price_pence (0 when no price is set)
    pub watts_total: f64,
    pub pounds_per_day: f64,
    /// The share lane (LAUNCH-GUARDS.md 2.9), from the supervisor's totals: on while the node serves a share target
    /// and the workers search the share header; the node's accepted and refused counts, the refusals by its reason,
    /// and its operating count of shares a block.
    #[serde(default)]
    pub share_lane: bool,
    #[serde(default)]
    pub shares_accepted: u64,
    #[serde(default)]
    pub shares_refused: u64,
    #[serde(default)]
    pub share_reasons: std::collections::BTreeMap<String, u64>,
    #[serde(default)]
    pub shares_per_block: u64,
    /// The chain's credit on the share lane (the design seat's read, 11 October 2026): the payout address's balance
    /// rise since this session's first balance read, in wei as a decimal string ("" until it rises), the head the
    /// rise was last seen at, and pending = shares accepted while nothing has risen yet (the credit lands with a
    /// carrying block, blocks after the share).
    #[serde(default)]
    pub credit_wei: String,
    #[serde(default)]
    pub credit_block: u64,
    #[serde(default)]
    pub credit_pending: bool,
}

/// The miner software's dev fee as the miner reports it at start (`dev fee 1% (1 block in 100) to 0x...`).
#[derive(Clone, Serialize, Default)]
pub struct DevFeeState {
    pub on: bool,
    pub percent: u32,
    pub address: String, // 0x + 40 hex once a miner has printed it
    pub line: String,    // the miner's own words
    /// The fee's own counts this session (igneum_supervisor::devfee): templates requested with the dev address
    /// and the shares accepted on them; the Earnings row shows them beside the net.
    #[serde(default)]
    pub templates: u64,
    #[serde(default)]
    pub shares_accepted: u64,
    /// The fee-waiver handshake's words (DESIGN.md 16.3): "Dev fee 1 percent; pool fee 0 (waived)" on a pool
    /// session, the dev fee alone on a node; the pool's receipts as counted.
    #[serde(default)]
    pub fee_words: String,
    #[serde(default)]
    pub receipts_total: u64,
    #[serde(default)]
    pub receipts_v1_verified: u64,
    #[serde(default)]
    pub receipts_v1_failed: u64,
    /// The receipts file this session writes (the settlement team's form); empty before the first receipt.
    #[serde(default)]
    pub receipts_file: String,
}

#[derive(Clone, Serialize, Default)]
pub struct ProgramState {
    pub boundary_daa: u64,
    pub eta_s: i64,
    pub epoch_index: u64,
    pub prepared: bool,
    pub prepare_sent: bool,
    pub message: String,
}

/// review B F07: one NVIDIA device's proving mode (src/device.rs) with its one line for the window.
#[derive(Clone, Serialize, Default, Debug, PartialEq)]
pub struct ProveMode {
    pub key: String,
    pub name: String,
    pub mode: String,
    pub line: String,
}

/// The prover service (src/prover.rs): assigned, proving, submitted, paid.
#[derive(Clone, Serialize, Default)]
pub struct ProvingState {
    pub enabled: bool,
    /// every present NVIDIA card is under 12 GB: the prover refuses unless settings.prove_instead holds the miner off
    pub under_12gb: bool,
    /// review B F07: the per-device mode (src/device.rs): simultaneous, time-share, mining-only, prove-only, with one line each
    pub modes: Vec<ProveMode>,
    /// the host runs here (macOS, Linux) or inside WSL2 (Windows); false = Set up needed
    pub available: bool,
    pub setup_hint: String,
    pub backend: String, // cpu | cuda
    pub status: String,  // off | setup | waiting | idle | proving | submitted
    pub keys: u32,
    pub assigned: u32,
    pub proved: u32,
    pub submitted: u32,
    pub paid: u32,
    pub failed: u32,
    /// wei, serialised as a decimal string: serde_json's `to_value` refuses a u128 over u64::MAX (about 18.45 IGN,
    /// 15 paid shards at 1.23 IGN), and that refusal emptied the whole `/api/state` reply to "{}" (6 October 2026)
    #[serde(serialize_with = "u128_string")]
    pub paid_wei: u128,
    pub current: String,
    pub started_at: f64,
    pub last_prove_s: f64,
    pub last_paid: String,
    pub message: String,
    // the node's proof verifier (src/verifier.rs; spec 7.7 item 4): a node without one relays and never includes
    /// the node's own report, `igneum_getProvingStatus().verifier` (`Off`, `Trust`, `Command("...")`); empty until read
    pub verifier: String,
    /// off | trust | command | unknown, from the report
    pub verifier_mode: String,
    /// what the app passed its node: `command:<path>`, `trust`, or empty when it set nothing
    pub verifier_set: String,
    /// why the app set nothing (the paths it looked at), or the trust warning
    pub verifier_reason: String,
    /// the sentence on the tile for the state above
    pub verifier_note: String,
    /// the node's proof pool: records held, verified, rejected
    pub pool_entries: u64,
    pub pool_verified: u64,
    pub pool_failed: u64,
    /// the pinned guests' ids (`igneum-prove-host --mode id`): the shard program and the aggregator; empty until read
    pub program_id: String,
    pub aggregator_id: String,
    /// proving v1 step 1: the install-time default's one plain line (why proving is on or off on this machine)
    pub default_note: String,
    /// proving v1: segment records this machine aggregated and submitted, and the aggregator's last line
    pub aggregated: u32,
    pub segment_note: String,
    /// proving v1 segment path (6 October 2026): whole segments this machine proved and submitted, paid, and what
    /// they paid (wei as a decimal string, see `paid_wei`); the last segment's wall time
    pub segments_submitted: u32,
    pub segments_paid: u32,
    #[serde(serialize_with = "u128_string")]
    pub segment_paid_wei: u128,
    pub segment_last_s: f64,
    /// segment records the node refused by the chain rule and this machine offers again each pass
    pub segments_held: u32,
}

#[derive(Clone, Serialize, Default)]
pub struct FinalityState {
    pub last_lock: u64,
    pub last_lock_at: f64,
    pub age_s: f64,
    pub votes: u64,
    pub message: String,
    /// Horizon polish Q83/Q84 (6 October 2026): the network's finality is paused (a synced node, no checkpoint lock
    /// for manifest::FINALITY_PAUSE_S); since when (the last lock's time, else the engine's start); the cause when
    /// the node carries it (its `finality_reason=` / `held_by=` line), else the plain two-thirds line; and the one
    /// sentence every surface shows: "Finality paused since 18:39 UTC: under two thirds of the weight is signing"
    pub paused: bool,
    pub paused_since: f64,
    pub reason: String,
    pub held_by: String,
    pub line: String,
    /// the node's word (igneum_getProvingStatus finalityProvisional, an earlier release): locks are provisional right now
    pub provisional: bool,
    /// review B F04: the newest lock's kind from the node ("certified" | "recovery"; empty on a node without the field)
    /// and its why; a recovery lock is labelled "recovery" on every surface, never "final"
    pub lock_kind: String,
    pub lock_why: String,
    /// where the cause came from: "node" (the RPC's structured fields), "node-line" (its log line) or "" (the
    /// engine's own rule)
    pub cause_source: String,
    /// the node's own finalityActive (b2e21447), when it carries it: then the node decides `paused`, not the
    /// engine's 15-minute rule
    pub node_active: Option<bool>,
}

/// Clock skew against the network. skew_s = local time minus network time (negative = this machine is behind).
#[derive(Clone, Serialize, Default)]
pub struct ClockState {
    pub skew_s: f64,
    pub severity: String, // none | warn | block
    pub source: String,   // node | blocks | https
    pub message: String,
    pub hint: String,     // the manual instruction for this platform
    pub checked_at: f64,
    pub syncing: bool,
    pub sync_result: String,
}

#[derive(Clone, Serialize, Default)]
pub struct AddressState {
    pub value: String,
    pub display: String,
    pub source: String,
    pub key_saved: bool,
    pub wallet_file: String,
    /// Miner UI 4: the payout address's balance in wei as a decimal string (eth_getBalance through the node's own
    /// RPC, read every 30 s while the node runs); null until the first read; balance_age_s = -1 until then
    pub balance_wei: Option<String>,
    pub balance_age_s: f64,
    pub balance_note: String, // the last read's error, in words; "" when the last read was good
    /// £ per IGN. null until a market exists. Its one source will be a SIGNED field of the OTA manifest (`price`:
    /// gbp_per_ign, as_of, source), checked like the manifest's tuning object; the app never computes or fetches a
    /// price on its own
    pub price_gbp_per_ign: Option<f64>,
}

#[derive(Clone, Serialize, Default)]
pub struct SettingsState {
    pub identities: u32,
    /// 0.2.8: the Fleet item's console URL and whether a token is held (the token itself never leaves the engine).
    #[serde(default)]
    pub fleet_console_url: String,
    #[serde(default)]
    pub fleet_token_set: bool,
    pub vote: bool,
    pub start_at_login: bool,
    pub auto_update: bool,
    /// signed remote jobs from Igneum run on this machine (src/jobs.rs)
    pub remote_jobs: bool,
    /// the prover service (src/prover.rs)
    pub prove: bool,
    /// the efficiency sweep (src/sweep.rs): once after install, then weekly; effective only with `power_control`
    pub sweep: bool,
    /// the NVIDIA power cap and the sweep may ask for administrator rights (config.rs: default off, one prompt when
    /// switched on)
    pub power_control: bool,
    /// the line beside the Power control switch: why it is off, or that the rights were given
    pub power_note: String,
    /// Ember Tune is paused fleet-wide by the signed manifest's kill switch (tuning.ember.enabled = false)
    pub tuning_off: bool,
    pub tuning_note: String,
    /// Ember 2: the goal (efficiency | balanced | rate), the electricity price in pence per kWh, the hill-climb switch
    pub tune_goal: String,
    pub power_price_pence: f64,
    pub tune_climb: bool,
    /// Ember Tune tiers: the fleet tier (efficiency | balanced | max), balanced from install
    pub tune_tier: String,
    /// Ember Heat (src/heat.rs, config.rs): the region code, the heat-mode switch, the set point, the schedule as
    /// the settings page types it, the window's clock offset, the typed room reading and the learned idle offset
    pub region: String,
    /// currency-21: the ISO 4217 override from the settings file ("" = follow the region)
    pub currency: String,
    /// the work source (step 3): "node" | "pool" | "" and its address
    pub source_kind: String,
    pub source_url: String,
    /// the network step: the chosen network ("" = the package's own)
    pub network: String,
    pub heat_on: bool,
    pub heat_set_c: f64,
    pub heat_schedule: String,
    pub heat_tz_min: i32,
    pub heat_room_c: f64,
    pub heat_room_at: f64,
    pub heat_offset_c: f64,
    /// the manifest's tune period in seconds (tuning.ember.period_s, default 7 days): a card is due again at
    /// sweep_at + tune_period_s, or at once after a driver major or program-class change
    pub tune_period_s: u64,
    /// the miner software's dev fee switch (settings; `--dev-fee 0` when off)
    pub dev_fee: bool,
    /// devnet only: the node trusts proof records without a verifier (`IGNEUM_PROOF_VERIFY=trust`)
    pub proof_verify_trust: bool,
    /// miner-ui-5: the public address profile opt-in (settings.profile_public)
    pub profile_public: bool,
    /// ui-ota: "Use the built-in interface" (settings.ui_builtin)
    pub ui_builtin: bool,
    /// prove instead of mining on a card under 12 GB (settings.prove_instead)
    pub prove_instead: bool,
}

/// The interface over the air (src/uiota.rs): what serves, from where, since when; Settings > Interface.
#[derive(Clone, Serialize, Default)]
pub struct UiState {
    /// the version compiled into this engine (ui/VERSION)
    pub embedded_version: String,
    /// the version the server serves now (the bundle's, or the embedded one)
    pub active_version: String,
    /// embedded | ota
    pub source: String,
    /// unix s the active bundle was swapped in (0 for the embedded interface)
    pub installed_at: f64,
    /// the page confirmed the active bundle with its health ping (always true for the embedded interface)
    pub confirmed: bool,
    /// the version the manifest publishes now ("" when the channel is withdrawn)
    pub published_version: String,
    pub busy: bool,
    pub error: String,
    /// versions refused here (never applied again)
    pub bad: Vec<String>,
    pub builtin: bool,
    /// why the published version is not applied, when it is not
    pub note: String,
}

/// Ember Heat (src/heat.rs): what the loop is doing, for the Cards strip, the Settings line and the Overview button.
#[derive(Clone, Serialize, Default)]
pub struct HeatState {
    pub on: bool,
    /// off | waiting | paused | heating | resting
    pub phase: String,
    /// the set point in force now (the schedule's slot, or the one set point)
    pub set_c: f64,
    /// the room estimate and where it came from: typed | card | none; the stated error; the reading's age
    pub room_c: f64,
    pub room_source: String,
    pub room_error_c: f64,
    pub room_age_s: f64,
    /// this period's duty (0 to 1) and the share of the last hour the cards heated
    pub duty: f64,
    pub duty_hour: f64,
    /// watts of heat now (the mining cards' draw; 0 while resting or with no draw reading), the cards' draw when
    /// they heat (the last reading), and the period's average (duty times that)
    pub heat_w: f64,
    pub full_w: f64,
    pub heat_avg_w: f64,
    /// seconds until the slice changes
    pub until_s: f64,
    /// the one line under the switch
    pub note: String,
    pub period_s: f64,
    /// the idle offset in use and whether a typed reading taught it
    pub offset_c: f64,
    pub offset_learned: bool,
}

/// One remote job this machine ran (the ledger entry), for the Settings history and the last-job strip.
#[derive(Clone, Serialize, Default)]
pub struct JobHistory {
    pub id: String,
    pub kind: String,
    pub title: String,
    pub status: String, // running | done | failed | timeout | aborted
    pub started_at: f64,
    pub finished_at: f64,
    pub exit: i64,
    pub run_id: String,
    pub uploaded: bool,
    pub summary: String,
}

/// The remote-job runner (src/jobrun.rs): the switch, the running job, the last outcome, the history.
#[derive(Clone, Serialize, Default)]
pub struct JobsState {
    pub allowed: bool,
    pub key_fingerprint: String,
    pub url_set: bool,
    pub checked_at: f64,
    pub error: String,
    pub queued: u32,
    /// set when the app runs elevated or under another account than the signed-in user (WSL belongs to the account)
    pub account: String,
    pub active: bool,
    pub id: String,
    pub kind: String,
    pub title: String,
    pub stage: String,
    pub message: String, // the last output line
    pub started_at: f64,
    pub run_id: String,
    pub results: Vec<String>, // RESULT and STAGE lines so far
    pub last: JobHistory,
    pub last_results: Vec<String>,
    pub history: Vec<JobHistory>,
}

#[derive(Clone, Serialize, Default)]
pub struct UpdateState {
    pub available: bool,
    pub version: String,
    pub url: String,
    pub notes: String,
    pub checked_at: f64,
    pub error: String,
    /// status "retrying": the seconds until the check is made again (a dark store or no network is not a failed
    /// update; the window says it is tried again).
    #[serde(default)]
    pub retry_s: u64,
    // the over-the-air updater (src/ota.rs)
    pub status: String, // unknown | checking | current | available | downloading | staging | ready | applying | manual | error | retrying
    pub downloaded: bool,
    pub ready: bool,
    pub applying: bool,
    pub progress: f64, // 0..1 of the download
    pub size: u64,
    pub auto: bool,            // settings: install by itself at a safe moment
    pub wait: String,          // why it has not applied yet, in the dashboard's words
    pub urgent: bool,          // a consensus activation is close, or this version is unsupported: no waiting
    pub urgent_text: String,
    pub activation_height: u64,
    pub unsupported: bool,
    /// F14: this version is unsupported and automatic updates are off: the engine pauses mining until Install now
    pub hold_mining: bool,
    pub min_supported: String,
    pub channel: String,
    pub published_at: String,
    pub file: String,          // the downloaded installer or disk image (the manual path opens it)
    pub updated_from: String,  // set on the first run after an update
    pub rolled_back: String,   // set when the helper restored the previous version
}

#[derive(Clone, Serialize)]
pub struct Event {
    pub t: f64,
    pub kind: String, // ok | info | block | build | error
    pub text: String,
}

#[derive(Clone, Serialize)]
pub struct LogLine {
    pub seq: u64,
    pub t: f64,
    pub src: String,
    pub err: bool,
    pub text: String,
}

#[derive(Clone, Serialize, Default)]
pub struct State {
    /// The founder's preview mark (8 October 2026): "preview 1" on an internal build, empty on a public cut. Read from
    /// IGNEUM_PREVIEW at build time, never from the manifest, so the public aliases never carry it; the version itself
    /// stays the crate's (the update check and the DMG's engine check compare that).
    #[serde(default)]
    pub preview: String,
    /// the worker layer owns the cards (src/sup.rs, docs/product-a/OLD-BASE.md step 2): the window hides the old
    /// chain's proving section and reads the cards' live words from the supervisor's slots
    #[serde(default)]
    pub worker_layer: bool,
    /// "public" | "lab" (src/edition.rs, review B F14), the product name the build shows, the manifest channel it runs on
    pub edition: String,
    pub product: String,
    pub channel: String,
    pub version: String,
    pub phase: String, // welcome | cards | address | dashboard
    pub setup_done: bool,
    pub network: String,
    /// the network step: the chain this engine runs by name (the pinned chain's name) and
    /// the one chosen for the next start when it differs ("" = none)
    pub network_name: String,
    pub network_pending: String,
    pub network_error: String,
    pub chain: String,
    pub host: String,         // the hostname, a friendly label only
    pub display_name: String, // the user's name for this machine (settings), defaults to the hostname
    pub machine_id: String,   // per-install id; labels and vote keys come from its first 8 hex
    pub node_dir: String,
    pub log_dir: String,
    pub detecting: bool,
    pub detect_message: String,
    pub node: NodeState,
    /// The work source as the supervisor sees it (0.2.3, the founder's order of 12 October 2026: the header pill
    /// and the card lines read the SOURCE, never the old embedded node): the pool or the node a user named.
    #[serde(default)]
    pub source: SourceState,
    /// The page's listener (the project's order of 11:0x UK, 12 October 2026, after the founder's open tab on the mini went
    /// dark across an install): `port` the one bound, `wanted` the one asked for (18080 unless the package or
    /// IGNEUM_APP_PORT names another), `lan` when the page is bound to every address of the machine, not loopback only.
    #[serde(default)]
    pub page: PageState,
    pub mining: MiningState,
    pub program: ProgramState,
    pub finality: FinalityState,
    pub proving: ProvingState,
    pub clock: ClockState,
    pub address: AddressState,
    pub settings: SettingsState,
    pub heat: HeatState,
    pub dev_fee: DevFeeState,
    pub update: UpdateState,
    /// driver-check: the one install in flight (or the last), and the table's stamp
    pub drivers: crate::drivers::DriverState,
    pub ui: UiState,
    pub jobs: JobsState,
    pub events: Vec<Event>,
    pub uptime_s: u64,
    /// first-block-21: when this engine run began (unix s, the wall clock; uptime_s is monotonic and stops across a sleep)
    pub started_at: f64,
    pub now: f64,
    pub quitting: bool,
    /// an earlier release: the quit's stage in words (src/quitguard.rs Stage), what the window shows while it waits
    pub quit_stage: String,
    pub live_page: String,
    /// miner-ui-5: this machine's own ladder record (src/ladder.rs), summarised at `now`
    pub ladder: crate::ladder::LadderState,
}

/// Ring buffers behind the state: events (newest first in the JSON) and the log drawer.
pub struct Rings {
    pub events: VecDeque<Event>,
    pub log: VecDeque<LogLine>,
    pub seq: u64,
}

impl Rings {
    pub fn new() -> Rings {
        Rings { events: VecDeque::new(), log: VecDeque::new(), seq: 0 }
    }
    pub fn event(&mut self, kind: &str, text: &str) {
        self.events.push_front(Event { t: crate::platform::unix_now_f(), kind: kind.into(), text: text.into() });
        while self.events.len() > 40 {
            self.events.pop_back();
        }
    }
    pub fn log(&mut self, src: &str, err: bool, text: &str) {
        self.seq += 1;
        self.log.push_back(LogLine { seq: self.seq, t: crate::platform::unix_now_f(), src: src.into(), err, text: text.into() });
        while self.log.len() > 3000 {
            self.log.pop_front();
        }
    }
    pub fn since(&self, after: u64, limit: usize) -> Vec<LogLine> {
        let mut out: Vec<LogLine> = self.log.iter().filter(|l| l.seq > after).cloned().collect();
        if out.len() > limit {
            out = out.split_off(out.len() - limit);
        }
        out
    }
}

/// A u128 as a decimal JSON string (the dashboard reads it with `Number()`).
pub fn u128_string<S: serde::Serializer>(v: &u128, s: S) -> Result<S::Ok, S::Error> {
    s.serialize_str(&v.to_string())
}

#[cfg(test)]
mod paid_wei_tests {
    use super::*;

    #[test]
    fn a_paid_total_over_u64_max_still_serialises_the_whole_state() {
        let mut st = State::default();
        st.proving.paid_wei = u64::MAX as u128 + 1;
        let v = serde_json::to_value(&st).expect("the state serialises");
        assert_eq!(v["proving"]["paid_wei"], serde_json::Value::String("18446744073709551616".into()));
        assert!(v["mining"].is_object());
    }
}
