//! Two configuration files.
//! `settings.json` in the app data directory: what the user chose (address, identities, voting, setup done).
//! `igneum-app.json` next to the binary (macOS: Contents/Resources): what the packager wrote (the update manifest URL,
//! the log intake). Neither is in the UI source.

use serde::{Deserialize, Serialize};
use std::collections::HashMap;
use std::path::{Path, PathBuf};

/// What the user chose for one GPU (keyed by vendor, device index and name).
#[derive(Clone, Serialize, Deserialize, Default)]
pub struct CardPref {
    pub enabled: bool,
    pub identities: u32,
    /// NVIDIA power cap, percent of the card's default limit (50 to 100); 0 = the default 80
    #[serde(default)]
    pub power_pct: u32,
    /// the user moved the slider: this cap stays; the efficiency sweep (src/sweep.rs) records but does not change it
    #[serde(default)]
    pub pinned: bool,
    /// the last efficiency sweep: when (unix s), the cap it chose, and that step's numbers
    #[serde(default)]
    pub sweep_at: u64,
    #[serde(default)]
    pub sweep_pct: u32,
    #[serde(default)]
    pub sweep_eff: f64,
    #[serde(default)]
    pub sweep_watts: f64,
    #[serde(default)]
    pub sweep_mhs: f64,
    /// Ember Tune (src/ember.rs): the clock cap the last tune chose (0 = unlocked), the driver and program class it
    /// ran under (a change makes the card due again), and the plan that produced it (full | confirm | baseline)
    #[serde(default)]
    pub sweep_clock_mhz: u32,
    #[serde(default)]
    pub sweep_driver: String,
    #[serde(default)]
    pub sweep_class: String,
    #[serde(default)]
    pub sweep_source: String,
    /// the chosen clock sat on the ladder's floor (the lowest step measured, not the optimum)
    #[serde(default)]
    pub sweep_floor: bool,
    /// Miner UI 4 (6 October 2026): this card's goal (efficiency | balanced | rate); empty = the global tune_goal
    #[serde(default)]
    pub tune_goal: String,
    /// Ember Tune tiers (8 October 2026): this card's tier in force and when it was applied
    #[serde(default)]
    pub tier: String,
    #[serde(default)]
    pub tier_at: u64,
    /// tiers-table-26: the card's measured tiers, kept across restarts (the table seeds a card with none)
    #[serde(default)]
    pub tiers: Vec<serde_json::Value>,
    /// the program class the stored tiers were measured under ("" = none)
    #[serde(default)]
    pub tiers_class: String,
    /// the untuned point the last FULL plan measured first (its step 0), kept across confirm plans so the row can
    /// read "saves 84 W, 0.15% of rate"; 0 = never measured
    #[serde(default)]
    pub sweep_before_watts: f64,
    #[serde(default)]
    pub sweep_before_mhs: f64,
    /// Ember 2: the memory clock the last tune chose (0 = the driver's default)
    #[serde(default)]
    pub sweep_mem_mhz: u32,
}

/// One first-block card shown: the block's hash (empty until the node's line names it) and the unix time.
#[derive(Clone, Debug, Default, Serialize, Deserialize, PartialEq)]
pub struct FirstBlockMark {
    #[serde(default)]
    pub hash: String,
    #[serde(default)]
    pub at: f64,
}

#[derive(Clone, Serialize, Deserialize)]
pub struct Settings {
    #[serde(default)]
    pub setup_done: bool,
    /// Lower-case 0x address every identity pays to.
    #[serde(default)]
    pub address: String,
    /// "generated" (the wallet file next to this) or "pasted".
    #[serde(default)]
    pub address_source: String,
    /// The one-time "save your key" sheet was confirmed.
    #[serde(default)]
    pub key_saved: bool,
    /// Legacy single value (an earlier release first cut); per-card counts live in `cards`.
    #[serde(default = "one")]
    pub identities: u32,
    #[serde(default)]
    pub cards: HashMap<String, CardPref>,
    /// A friendly name for this machine (defaults to the hostname); display only.
    #[serde(default)]
    pub display_name: String,
    #[serde(default = "yes")]
    pub vote: bool,
    #[serde(default)]
    pub paused: bool,
    /// the project's ruling of 14 October 2026 (21:5x UK): who paused, "user" (the page's button) or "job" (the api from a
    /// script, reliability's); a job's pause outlives an update, a user's clears on the first start after one (the
    /// founder's rule of 8 October 2026: an update never leaves mining off).
    #[serde(default)]
    pub paused_by: String,
    /// Lifetime accepted blocks, carried across runs.
    #[serde(default)]
    pub accepted_total: u64,
    /// Over-the-air updates install by themselves at a safe moment (default on); off = download, then wait for Install now.
    #[serde(default = "yes")]
    pub auto_update: bool,
    /// Signed remote jobs from Igneum (src/jobs.rs) run on this machine (default on for the devnet build; the
    /// switch in Settings shows the signing key's fingerprint).
    #[serde(default = "yes")]
    pub remote_jobs: bool,
    /// Prove the shards assigned to this machine's keys (src/prover.rs). Default off until the GPU numbers exist.
    #[serde(default)]
    pub prove: bool,
    /// The efficiency sweep (src/sweep.rs): once after install, then weekly, each NVIDIA card's cap is stepped from
    /// 100% to 50% on the live program and held at the best MH per watt. Default off; implied by `power_control`
    /// (on when that is switched on, never effective while it is off). A pinned card is skipped.
    #[serde(default)]
    pub sweep: bool,
    /// Power control (the founder, 5 October 2026: "if we don't have to ask then don't ask"): the NVIDIA power cap and the
    /// efficiency sweep need administrator rights (one UAC prompt on Windows). Default OFF on every machine; the app
    /// never raises the prompt on its own. Switching it on asks once, at that moment; a refused, cancelled or
    /// unanswered prompt switches it back off with a notice, no retries.
    #[serde(default)]
    pub power_control: bool,
    /// The Fleet item in Ember's window (DESIGN.md 3.6 of docs/fleet, 0.2.8): the owner's console host and token
    /// path (the token directory with its trailing slash, e.g. https://host/_fleet/<token>/) and the window's own
    /// user token for the console's act path and read API, held here and sent by the engine as X-Fleet-Token; the
    /// page never holds the token. With the URL set the rail gains Fleet between Tune and Earnings.
    #[serde(default)]
    pub fleet_console_url: String,
    #[serde(default)]
    pub fleet_console_token: String,
    /// Ember 2 (6 October 2026): the tune's goal ("efficiency" = most MH per watt within 10% of the top rate,
    /// "balanced" = within 1%, "rate" = the fastest point), the electricity price in pence per kWh for the £/day
    /// reading on each card, and the hill-climb switch (memory up, core down from the fleet prior; off = the ladders).
    #[serde(default = "balanced")]
    pub tune_goal: String,
    /// Ember Tune tiers: the fleet tier (efficiency | balanced | max); balanced from install
    #[serde(default = "default_tier")]
    pub tune_tier: String,
    #[serde(default)]
    pub power_price_pence: f64,
    #[serde(default)]
    pub tune_climb: bool,
    /// Ember Heat (mission item 7, 7 October 2026; src/heat.rs): the region code the miner chose at first run or in
    /// Settings ("" = not chosen; the price above is in that region's minor unit per kWh, typed, never fetched), the
    /// heat-mode switch, the set point in degrees, the schedule (slots by minute of the day in the window's clock,
    /// `heat_tz_min` minutes east of UTC), the typed room reading and when it was typed (0 = none), and the learned
    /// idle offset of the card sensor above the room (0 = the default).
    #[serde(default)]
    pub region: String,
    /// the network step (an earlier release): the chain this machine runs, kept for older settings files; the chain is pinned (CHAIN_ID) and the field is read no more.
    #[serde(default)]
    pub network: String,
    /// the work source (docs/product-a/OLD-BASE.md step 3): "node" (solo through a node the user names by URL) or
    /// "pool" (a pool by host:port); "" = not chosen yet (the window asks). The chain is the one pinned below.
    #[serde(default)]
    pub source_kind: String,
    #[serde(default)]
    pub source_url: String,
    /// currency-21 (7 October 2026): the ISO 4217 code the miner chose in Settings ("" = follow the region, which
    /// follows the OS locale at first run); the price above is in hundredths of this unit per kWh. Survives restart here.
    #[serde(default)]
    pub currency: String,
    #[serde(default)]
    pub heat_on: bool,
    #[serde(default = "nineteen")]
    pub heat_set_c: f64,
    #[serde(default)]
    pub heat_schedule: Vec<crate::heat::Slot>,
    #[serde(default)]
    pub heat_tz_min: i32,
    #[serde(default)]
    pub heat_room_c: f64,
    #[serde(default)]
    pub heat_room_at: f64,
    #[serde(default)]
    pub heat_offset_c: f64,
    /// When this install first ran (unix s), for the "first hour after install" sweep.
    #[serde(default)]
    pub installed_at: u64,
    /// The miner software's dev fee (1 block template in 100 to the dev address; decision of 4 October 2026). Default
    /// on; off passes `--dev-fee 0` to igneum-miner. The protocol itself carries no fee.
    #[serde(default = "yes")]
    pub dev_fee: bool,
    /// Lifetime dev-fee blocks this machine found (the miner's `dev-fee block` lines), carried across runs.
    #[serde(default)]
    pub fee_total: u64,
    /// The first-block celebration, once per payout address, ever (the founder, 8 October 2026, v0.2.1): the lower-case
    /// address a window showed the card for, with the block's hash and time. Kept in settings.json so it survives runs,
    /// updates and a reinstall that keeps the wallet; a new address shows it once again; a dev-fee block never raises it.
    #[serde(default)]
    pub first_block_shown: HashMap<String, FirstBlockMark>,
    /// Devnet only: when no verifier is found next to the engine, start the node with `IGNEUM_PROOF_VERIFY=trust`
    /// so it includes proof records it never verified (src/verifier.rs). Default off; a found verifier always wins.
    #[serde(default)]
    pub proof_verify_trust: bool,
    /// Proving v1 step 1 (5 October 2026): the install-time default for `prove` has been applied once (src/provedefault.rs:
    /// on when the machine can prove, never switching an explicit on back off). Older installs apply it at their next start.
    #[serde(default)]
    pub prove_default_applied: bool,
    /// miner-ui-5 (7 October 2026): the public address profile on the site is behind this opt-in; off by default, and
    /// the switch's words say what becomes public (the key ids, the blocks, the weight rank, the card model).
    #[serde(default)]
    pub profile_public: bool,
    /// ui-ota (7 October 2026, src/uiota.rs): "Use the built-in interface": the embedded dashboard serves even when an
    /// over-the-air interface bundle is active. Default off.
    #[serde(default)]
    pub ui_builtin: bool,
    /// Prove instead of mining on a machine whose only NVIDIA card is under 12 GB (the project's routing, 7 October 2026: a
    /// 10 GB card holds the prover or the miner, never both). Off by default; the 12 GB refusal stays while it is off.
    #[serde(default)]
    pub prove_instead: bool,
}

fn one() -> u32 {
    1
}
fn balanced() -> String {
    "balanced".into()
}
fn nineteen() -> f64 {
    19.0
}
fn yes() -> bool {
    true
}

fn default_tier() -> String {
    "balanced".into()
}

impl Default for Settings {
    fn default() -> Settings {
        Settings { setup_done: false, fleet_console_url: String::new(), fleet_console_token: String::new(), address: String::new(), address_source: String::new(), key_saved: false, identities: 1, cards: HashMap::new(), display_name: String::new(), vote: true, paused: false, paused_by: String::new(), accepted_total: 0, auto_update: true, remote_jobs: true, prove: false, sweep: true, power_control: false, tune_goal: "balanced".into(), tune_tier: "balanced".into(), source_kind: String::new(), source_url: String::new(), power_price_pence: 0.0, tune_climb: false, region: String::new(), currency: String::new(), network: String::new(), heat_on: false, heat_set_c: 19.0, heat_schedule: Vec::new(), heat_tz_min: 0, heat_room_c: 0.0, heat_room_at: 0.0, heat_offset_c: 0.0, installed_at: 0, dev_fee: true, fee_total: 0, first_block_shown: HashMap::new(), proof_verify_trust: false, prove_default_applied: false, profile_public: false, ui_builtin: false, prove_instead: false }
    }
}

impl Settings {
    /// What a measurement engine (`--sweep`, started by a job beside the installed app) runs with, whatever the copied
    /// file says: no remote jobs (run 4, 6 October 2026: the second engine fetched the jobs file and ran 96 old jobs
    /// inside its scratch root), no updates, no proving, not paused, the tune on, Power control off (only an engine
    /// that is itself elevated controls NVIDIA, through the probe's `direct`), every card due and unpinned. The file
    /// on disk is never changed: the playbook copies the installed app's settings verbatim (a PowerShell JSON round
    /// trip rewrote big integers as doubles and the engine read the whole file as defaults: no payout address, every
    /// card off).
    pub fn for_measurement(mut self) -> Settings {
        self.remote_jobs = false;
        self.auto_update = false;
        self.prove = false;
        self.paused = false;
        self.sweep = true;
        self.power_control = false;
        self.setup_done = true;
        for p in self.cards.values_mut() {
            p.sweep_at = 0;
            p.pinned = false;
        }
        self
    }

    pub fn load(path: &Path) -> Settings {
        let fresh = !path.exists();
        let mut s: Settings = std::fs::read_to_string(path).ok().and_then(|t| serde_json::from_str(&t).ok()).unwrap_or_default();
        let mut dirty = false;
        // F14 (the founder, 8 October 2026): automatic updates are a choice at install. The Windows installer's task and
        // the Mac setup's first step write install-choices.json next to this file; a fresh install takes it once.
        if fresh {
            if let Some(dir) = path.parent() {
                if let Some(choices) = install_choices(&dir.join("install-choices.json")) {
                    if let Some(v) = choices.get("auto_update").and_then(|v| v.as_bool()) {
                        s.auto_update = v;
                        dirty = true;
                    }
                }
            }
        }
        if s.installed_at == 0 {
            // an install from before the sweep existed counts as installed now: it gets its first-hour sweep
            s.installed_at = crate::platform::unix_now();
            dirty = true;
        }
        if s.sweep && !s.power_control {
            // the sweep is implied by power control (5 October 2026): an install from before that setting carried
            // sweep = true by default; it no longer prompts on its own
            s.sweep = false;
            dirty = true;
        }
        if dirty {
            s.save(path);
        }
        s
    }
    pub fn save(&self, path: &Path) {
        if let Some(d) = path.parent() {
            let _ = std::fs::create_dir_all(d);
        }
        if let Ok(t) = serde_json::to_string_pretty(self) {
            let _ = std::fs::write(path, t);
        }
    }
}

/// The installer's choices (F14): a small JSON object the Windows installer writes when a task is unchecked and the
/// Mac setup writes from its first step; `{"auto_update": false}` today. None when absent or unreadable.
pub fn install_choices(path: &Path) -> Option<serde_json::Value> {
    serde_json::from_str(&std::fs::read_to_string(path).ok()?).ok()
}

/// Reads the machine id, or makes one on the first run (16 hex from the OS) and locks the file to the user.
fn machine_id(app_dir: &Path) -> String {
    let path = app_dir.join("machine-id");
    if let Some(id) = std::fs::read_to_string(&path).ok().map(|s| s.trim().to_ascii_lowercase()) {
        if id.len() == 16 && id.chars().all(|c| c.is_ascii_hexdigit()) {
            return id;
        }
    }
    let mut raw = [0u8; 8];
    getrandom::getrandom(&mut raw).expect("os randomness");
    let id = crate::keys::hex(&raw);
    let _ = std::fs::create_dir_all(app_dir);
    crate::platform::lock_permissions(app_dir, true);
    let _ = std::fs::write(&path, format!("{id}\n"));
    crate::platform::lock_permissions(&path, false);
    id
}

/// Written by the packager (build-dmg.sh, make-payload.sh). Missing fields disable the feature.
#[derive(Clone, Serialize, Deserialize, Default)]
pub struct Packaged {
    /// PRODUCT=public|lab (packaging/mac/packaged-config.sh, 8 October 2026): the edition the package was made for, its
    /// channel and the signing root its engine must carry; empty in a package from before the switch.
    #[serde(default)]
    pub edition: String,
    #[serde(default)]
    pub channel: String,
    #[serde(default)]
    pub ota_root_hex: String,
    #[serde(default)]
    pub update_manifest: String,
    /// The page's port (the project's order of 11:0x UK, 12 October 2026): 0 = the default 18080; a fleet machine's package
    /// may name another. `page_lan` binds the page to every address of the machine so a fleet page is reachable from
    /// another machine on the network (the token in the path still guards it); off = loopback only.
    #[serde(default)]
    pub page_port: u16,
    #[serde(default)]
    pub page_lan: bool,
    #[serde(default)]
    pub upload_url: String,
    #[serde(default)]
    pub upload_key: String,
    #[serde(default)]
    pub live_page: String,
    #[serde(default)]
    pub download_page: String,
    /// Consensus parameters the packager pins for the bundled node: the engine writes them to
    /// <app data>/override-params.json and starts igneumd with --override-params-file (the difficulty v2 activation
    /// height, 4 October 2026: `{"difficulty_v2_activation_daa": N}`). Absent or empty = no override file.
    #[serde(default)]
    pub node_override_params: Option<serde_json::Value>,
    /// The network the package's node joined (7 October 2026, an earlier chain): a suffixed devnet (`--devnet-suffix=N`), its own
    /// genesis and p2p port, its own node datadir devnet-N beside the shared devnet's devnet-v4 (kept for the way back).
    /// Absent = the shared devnet. IGNEUM_APP_DEVNET_SUFFIX in the environment wins over it.
    #[serde(default)]
    pub node_devnet_suffix: Option<u32>,
    /// The package's default peers for that network (host:port); absent = the shared devnet's list.
    #[serde(default)]
    pub node_peers: Option<Vec<String>>,
    /// Where the key and the manifest came from, for the log header: "packaged", "file <name>" or "none". Never
    /// serialised (the packaged file does not carry them; nothing sends this struct to the UI).
    #[serde(skip)]
    pub key_source: String,
    #[serde(skip)]
    pub manifest_source: String,
}

/// The public release store (12 October 2026): every Ember release sits at `<RELEASE_BASE>/<version>/` (the artefacts,
/// MANIFEST.json, SHA256SUMS and their .sig files) and the updater's manifest at `<RELEASE_BASE>/latest/igneum-app-latest.json`
/// with `igneum-app-latest.json.sig` beside it, the release key's SSH signature (infra/release/verify.sh checks the
/// same pair). A public build whose package names no manifest URL reads this one.
pub const RELEASE_BASE: &str = "https://build.igneum.network/releases/ember";
pub const DEFAULT_UPDATE_MANIFEST: &str = "https://build.igneum.network/releases/ember/latest/igneum-app-latest.json";

/// The token-folder host of the older private downloads path (`<host>/dl/<token>/igneum-app-latest.json`, what
/// IGNEUM_DL_TOKEN_FILE and the packager's token still build); the public release store is RELEASE_BASE above.
pub const DL_HOST: &str = "https://dl.igneum.network";

/// The chain this build mines (docs/product-a/OLD-BASE.md step 3, the pin; devnet7 since 0.2.0): the chain id of
/// tests/igneumhash/protocol-a/out/devnet7-genesis.json; the genesis hash is written by the protocol team at the chain's
/// genesis and pinned here by its own commit (empty until then: a node whose chain id differs is refused, a node
/// whose genesis differs is refused once the hash is pinned).
// Ember 0.2.0 pins devnet7, the launch-equivalent chain with the share lane (LAUNCH-GUARDS.md 2.9: chain id 7317,
// network id 7371, sealing from genesis since 13:21:44 UK on 11 October 2026 on two independent builds of main
// 32cbb213); the genesis hash is the chain's, read from the record
// Ember 0.2.1 pins devnet9, the chain at the launch constants (the protocol team: chain id 7319, network id 7391, sealing
// from genesis since 01:47 UK on 12 October 2026 behind the project's pool); the genesis hash is block 0's as the
// chain's own sealer answers it (eth_getBlockByNumber 0x0 on the protocol team's pod a at 01:50 UK, chain id 0x1c97, and
// the same on A9), so a node of another chain id or another genesis is refused
// Ember 0.2.10 pins the public test network of its day, chain 7320 (F-E45, the project's word of 17:1x UK, 14 October 2026: the card names one
// chain, the one the work source mines; chain id 7320, network id 7392; the genesis hash is block 0's as the public RPC
// rpc.igneum.network answers it, eth_getBlockByNumber 0x0 at 17:17 UK, chain id 0x1c98)
// Ember 0.2.17 pins the public test network's next chain, 7322 (the founder's go of 15 October 2026 through main: the
// public name's swap in two steps; the network team's fields of record: chain id 7322, network id 7394, the genesis hash of the
// set of record 462c276d9 at https://build.igneum.network/feed/devnet12/index.json (the served feed path, unchanged under F-522), the node 0.10.19); the chains file
// dev/chain/igneum-chains.json carries the entry the MANIFEST and the pointer name
pub const CHAIN_ID: u64 = 7322;
pub const NETWORK_ID: u64 = 7394;
pub const CHAIN_NAME: &str = "Igneum public test network";
pub const GENESIS_HASH: &str = "0x45828a34b05153fd2a9d0b7c49e84ec2289c38dd2f8c755729321e4f9c2236ea";
/// The chain Ember pinned before this one and the port its default pool served it on: a stored work source naming
/// that pool exactly (the default a user never changed) moves to the new default with the pin; a pool the user
/// typed stays as typed.
pub const PREVIOUS_CHAIN_ID: u64 = 7320;
pub const PREVIOUS_DEFAULT_POOL_PORT: u16 = 3341;
/// The project's pool, the default work source (Ember 0.2.1, the founder's order of 11 October 2026: a user who
/// names nothing mines through it; a node of their own is the alternative). Plain TCP tonight (ecosystem's line of
/// 23:51 UK; a TLS front is a later row on the same port).
pub const DEFAULT_POOL_HOST: &str = "pool.igneum.network";
// 7322's stratum (the pool team, 15 October 2026): pool.igneum.network:3342 from step one, plain TCP, the pooled form
// under the pool key, lanes 0 bandwidth, 1 Anvil, 2 raster; :3341 moves to 7322 at step two and stays reachable
pub const DEFAULT_POOL_PORT: u16 = 3342;
pub const DEFAULT_POOL_TLS: bool = false;
/// The intake a key file points at when the packaged file names no intake (a developer run).
pub const DEFAULT_INTAKE_URL: &str = "https://igneum-six.vercel.app/api/log";

/// The manifest URL for a downloads token; empty for an empty token.
pub fn manifest_url_for_token(token: &str) -> String {
    let t = token.trim();
    if t.is_empty() {
        String::new()
    } else {
        format!("{DL_HOST}/dl/{t}/igneum-app-latest.json")
    }
}

/// The downloads token inside a manifest URL of the standard shape, or None.
pub fn token_of_manifest_url(url: &str) -> Option<&str> {
    let rest = url.strip_prefix(DL_HOST)?.strip_prefix("/dl/")?;
    let (token, file) = rest.split_once('/')?;
    (file == "igneum-app-latest.json" && !token.is_empty()).then_some(token)
}

/// A secret from a file: trimmed, None when the file is missing or blank.
pub fn read_secret_file(path: &Path) -> Option<String> {
    let t = std::fs::read_to_string(path).ok()?;
    let t = t.trim();
    (!t.is_empty()).then(|| t.to_string())
}

/// The first 8 hex of sha256 over a value: what logs and the console show instead of the value
/// (`tr -d '[:space:]' < file | shasum -a 256 | cut -c1-8` gives the same on the Mac).
pub fn fingerprint8(value: &str) -> String {
    use sha2::Digest;
    crate::manifest::hex_encode(&sha2::Sha256::digest(value.as_bytes()))[..8].to_string()
}

impl Packaged {
    pub fn load(candidates: &[PathBuf]) -> Packaged {
        for c in candidates {
            if let Some(mut p) = std::fs::read_to_string(c).ok().and_then(|t| serde_json::from_str::<Packaged>(&t).ok()) {
                p.key_source = if p.upload_key.is_empty() { "none".into() } else { "packaged".into() };
                p.manifest_source = if p.update_manifest.is_empty() { "none".into() } else { "packaged".into() };
                return p.with_default_manifest();
            }
        }
        Packaged { key_source: "none".into(), manifest_source: "none".into(), ..Packaged::default() }.with_default_manifest()
    }

    /// The public release store's manifest when the package names none (12 October 2026: the update channel points at
    /// releases/ember on the public host). A packaged URL, a token file (with_file_overrides) and
    /// IGNEUM_APP_UPDATE_MANIFEST (src/ota.rs) all win over it. The lab build keeps its own folder and gets no default.
    fn with_default_manifest(mut self) -> Packaged {
        if self.update_manifest.is_empty() && !crate::edition::LAB {
            self.update_manifest = DEFAULT_UPDATE_MANIFEST.into();
            self.manifest_source = "default".into();
        }
        self
    }

    /// Rotation phase 2 (5 October 2026, docs/plans/rotation-phase-2.md): the same two variables the packagers honour
    /// (packaging/mac/packaged-config.sh) work on a running engine, so a developer run or a build that was packaged
    /// with the old values can report to the rotated intake and check the rotated folder without a repackage:
    /// IGNEUM_UPLOAD_KEY_FILE names a file holding the key, IGNEUM_DL_TOKEN_FILE a file holding the downloads token.
    /// A variable that is unset, or names a missing or blank file, changes nothing.
    pub fn with_env_overrides(self) -> Packaged {
        let file = |k: &str| std::env::var(k).ok().filter(|v| !v.is_empty()).map(PathBuf::from);
        self.with_file_overrides(file("IGNEUM_UPLOAD_KEY_FILE").as_deref(), file("IGNEUM_DL_TOKEN_FILE").as_deref())
    }

    pub fn with_file_overrides(mut self, key_file: Option<&Path>, token_file: Option<&Path>) -> Packaged {
        let name = |p: &Path| p.file_name().map(|n| n.to_string_lossy().to_string()).unwrap_or_else(|| p.display().to_string());
        if let Some(key) = key_file.and_then(read_secret_file) {
            self.upload_key = key;
            if self.upload_url.is_empty() {
                self.upload_url = DEFAULT_INTAKE_URL.into();
            }
            self.key_source = format!("file {}", name(key_file.unwrap()));
        }
        if let Some(token) = token_file.and_then(read_secret_file) {
            self.update_manifest = manifest_url_for_token(&token);
            self.manifest_source = format!("file {}", name(token_file.unwrap()));
        }
        self
    }

    /// The words when the package was made for the other edition or another signing root (a public engine in a lab
    /// package would never verify a lab manifest, and the other way round); None when the fields match or are absent.
    pub fn edition_mismatch(&self) -> Option<String> {
        let mut faults = Vec::new();
        if !self.edition.is_empty() && self.edition != crate::edition::name() {
            faults.push(format!("the package is the {} edition and this engine is the {} build", self.edition, crate::edition::name()));
        }
        if !self.channel.is_empty() && !crate::edition::channel_accepted(&self.channel) {
            faults.push(format!("the package's channel {} is not this build's ({})", self.channel, crate::edition::channel()));
        }
        if !self.ota_root_hex.is_empty() && self.ota_root_hex != crate::edition::ota_key() {
            faults.push(format!("the package's signing root {} is not this build's root {}", fingerprint8(&self.ota_root_hex), fingerprint8(crate::edition::ota_key())));
        }
        if faults.is_empty() { None } else { Some(format!("package mismatch: {}; updates will not verify until the matching build is installed", faults.join("; "))) }
    }

    /// The log header line: the intake URL with the key's fingerprint and the manifest URL with the folder's
    /// fingerprint, each with its source; the values themselves never appear (the log is uploaded).
    pub fn describe(&self) -> String {
        let key = if self.upload_key.is_empty() { "no key".to_string() } else { format!("key {}", fingerprint8(&self.upload_key)) };
        let intake = if self.upload_url.is_empty() { "none".to_string() } else { self.upload_url.clone() };
        let (manifest, folder) = match token_of_manifest_url(&self.update_manifest) {
            Some(t) => (self.update_manifest.replace(t, "<token>"), format!("folder {}", fingerprint8(t))),
            None if self.update_manifest.is_empty() => ("none".to_string(), "no folder".to_string()),
            None if self.update_manifest == DEFAULT_UPDATE_MANIFEST => (self.update_manifest.clone(), "release store".to_string()),
            None => (self.update_manifest.clone(), "custom".to_string()),
        };
        format!("config: intake {intake} {key} ({}); manifest {manifest} {folder} ({})", self.key_source, self.manifest_source)
    }
}

/// Where the engine runs: network, ports, peers, directories. Defaults are the devnet v4 of today's launchers;
/// the environment overrides them (tests run a private simnet on 29400+).
#[derive(Clone, Serialize)]
pub struct Runtime {
    pub network: String, // devnet | simnet
    pub rpc_port: u16,
    pub p2p_port: u16,
    pub peers: Vec<String>,
    pub unsynced_mining: bool,
    /// A private devnet (--devnet-suffix=N): its own genesis, never peers with the shared devnet. Tests.
    pub devnet_suffix: Option<u32>,
    pub node_dir: PathBuf,
    pub app_dir: PathBuf,
    pub log_dir: PathBuf,
    pub status_secs: u32,
    /// `--sweep`: run the efficiency sweep on every supported card as soon as it mines, print the table on stdout,
    /// leave the chosen caps in force and quit (the PC measurement job; docs/plans/miner-eff.md).
    pub sweep_only: bool,
    /// The machine's hostname: a friendly label only, never part of a key or a payout address.
    pub host: String,
    /// Per-install random id (16 hex, app data dir/machine-id, locked to the user). Identity labels, vote keys and
    /// the upload fields come from this, so two PCs cloned with the same COMPUTERNAME never share a key
    /// (found 4 October 2026 on the founder's two Windows machines).
    pub machine_id: String,
}

impl Runtime {
    pub fn from_env() -> Runtime {
        Self::from_env_with(None, None)
    }

    /// `packaged_suffix` and `packaged_peers` are the package's network (config.rs Packaged); the environment wins.
    pub fn from_env_with(packaged_suffix: Option<u32>, packaged_peers: Option<&[String]>) -> Runtime {
        Self::from_env_with_choice(packaged_suffix, packaged_peers, "")
    }
    /// The network step: `choice` is settings.network (the chain is pinned; the old choice is ignored); the environment wins inside.
    pub fn from_env_with_choice(packaged_suffix: Option<u32>, packaged_peers: Option<&[String]>, choice: &str) -> Runtime {
        let env = |k: &str| std::env::var(k).ok().filter(|v| !v.is_empty());
        let _ = choice; // the chain is pinned (CHAIN_ID); the old network choice is read no more
        let network = env("IGNEUM_APP_NETWORK").unwrap_or_else(|| "devnet".into());
        let rpc_port = env("IGNEUM_APP_RPC_PORT").and_then(|v| v.parse().ok()).unwrap_or(26610);
        let p2p_port = env("IGNEUM_APP_P2P_PORT").and_then(|v| v.parse().ok()).unwrap_or(26611);
        let peers = match std::env::var("IGNEUM_APP_PEERS") {
            Ok(v) => v.split(',').map(|s| s.trim().to_string()).filter(|s| !s.is_empty()).collect(),
            // the public seed node first, then the build box's hand nodes (the project's decision (b), 6 October 2026, 19:1x UTC:
            // the hands move off the founder's Mac to the store host), then the founder's Mac on the house LAN (devnet only)
            Err(_) if packaged_peers.map(|p| !p.is_empty()).unwrap_or(false) => packaged_peers.unwrap().to_vec(),
            Err(_) if network == "devnet" => vec!["188.245.5.161:26611".to_string(), "188.40.146.49:26611".to_string()],
            Err(_) => vec![],
        };
        let unsynced_mining = env("IGNEUM_APP_UNSYNCED").map(|v| v == "1").unwrap_or(false);
        let devnet_suffix = env("IGNEUM_APP_DEVNET_SUFFIX").and_then(|v| v.parse().ok()).or(packaged_suffix);
        let root = crate::platform::data_root();
        let node_dir = match env("IGNEUM_APP_NODE_DIR") {
            Some(d) => PathBuf::from(d),
            None if network == "devnet" && devnet_suffix.is_none() => root.join("devnet-v4"),
            None if network == "devnet" => root.join(format!("devnet-{}", devnet_suffix.unwrap_or(0))),
            None => root.join(format!("{network}-app")),
        };
        let app_dir = root.join("app");
        let log_dir = crate::platform::log_root();
        let status_secs = env("IGNEUM_APP_STATUS_SECS").and_then(|v| v.parse().ok()).unwrap_or(30);
        let machine_id = machine_id(&app_dir);
        let sweep_only = env("IGNEUM_APP_SWEEP").map(|v| v == "1").unwrap_or(false);
        Runtime { network, rpc_port, p2p_port, peers, unsynced_mining, devnet_suffix, node_dir, app_dir, log_dir, status_secs, sweep_only, host: crate::platform::host_label(), machine_id }
    }
    /// The first 8 hex of the machine id: what goes into labels.
    pub fn id8(&self) -> String {
        self.machine_id.chars().take(8).collect()
    }
    /// The node's Ethereum JSON-RPC port: the default 26790 sits 180 above the default RPC port, kept for any port.
    pub fn evm_port(&self) -> u16 {
        self.rpc_port.wrapping_add(180)
    }
    pub fn rpc_url(&self) -> String {
        format!("grpc://127.0.0.1:{}", self.rpc_port)
    }
}

#[cfg(test)]
mod tests {
    #[test]
    fn a_fresh_install_takes_the_installers_auto_update_choice_once() {
        let dir = std::env::temp_dir().join(format!("igneum-choices-{}", std::process::id()));
        let _ = std::fs::create_dir_all(&dir);
        let settings = dir.join("settings.json");
        let _ = std::fs::remove_file(&settings);
        std::fs::write(dir.join("install-choices.json"), r#"{"auto_update": false}"#).unwrap();
        let s = Settings::load(&settings);
        assert!(!s.auto_update, "the installer's choice is honoured on a fresh install");
        assert!(settings.exists(), "the choice is written into settings.json");
        // the choice file no longer speaks once settings.json exists: the user's later change in Settings wins
        let mut t = Settings::load(&settings);
        t.auto_update = true;
        t.save(&settings);
        assert!(Settings::load(&settings).auto_update);
        let _ = std::fs::remove_dir_all(&dir);
    }

    #[test]
    fn currency_round_trip() {
        // currency-21: the override is a field of the settings file, so it survives a restart; an old file without it reads as ""
        let mut s = super::Settings::default();
        s.currency = "EUR".into();
        s.region = "de".into();
        let text = serde_json::to_string(&s).unwrap();
        let back: super::Settings = serde_json::from_str(&text).unwrap();
        assert_eq!(back.currency, "EUR");
        assert_eq!(back.region, "de");
        let old: super::Settings = serde_json::from_str(r#"{"region":"gb"}"#).unwrap();
        assert_eq!(old.currency, "");
    }

    use super::*;

    fn tmp(name: &str, content: &str) -> PathBuf {
        // tests run in parallel: every file name is unique to its call
        static N: std::sync::atomic::AtomicU32 = std::sync::atomic::AtomicU32::new(0);
        let n = N.fetch_add(1, std::sync::atomic::Ordering::Relaxed);
        let d = std::env::temp_dir().join(format!("igneum-config-test-{}-{n}-{}", std::process::id(), name));
        std::fs::write(&d, content).unwrap();
        d
    }

    fn packaged(key: &str, token: &str) -> Packaged {
        let json = format!(r#"{{"update_manifest":"{}","upload_url":"https://igneum-six.vercel.app/api/log","upload_key":"{}"}}"#, manifest_url_for_token(token), key);
        let p = tmp("packaged.json", &json);
        let out = Packaged::load(&[p.clone()]);
        let _ = std::fs::remove_file(p);
        out
    }

    /// Known failed first (PRODUCT=public|lab, 8 October 2026): the packager writes the edition, the channel and the signing
    /// root into igneum-app.json; an engine of the other build reports the mismatch in words (a public engine in a lab
    /// package would never verify a lab manifest, and the other way round), and an older package without the fields says nothing.
    #[test]
    fn a_package_of_the_other_edition_is_named() {
        let mine = Packaged { edition: crate::edition::name().into(), channel: crate::edition::channel().into(), ota_root_hex: crate::edition::ota_key().into(), ..Default::default() };
        assert_eq!(mine.edition_mismatch(), None);
        assert_eq!(Packaged::default().edition_mismatch(), None, "a package without the fields says nothing");
        let other = Packaged { edition: "beta".into(), channel: "ember-beta".into(), ota_root_hex: "ab".repeat(32), ..Default::default() };
        let w = other.edition_mismatch().expect("named");
        assert!(w.contains("beta") && w.contains(crate::edition::name()) && w.contains("root"), "{w}");
        let root_only = Packaged { edition: crate::edition::name().into(), ota_root_hex: "cd".repeat(32), ..Default::default() };
        assert!(root_only.edition_mismatch().unwrap().contains("signing root"));
    }

    #[test]
    fn a_measurement_engine_overrides_the_copied_settings_in_memory() {
        let mut s = Settings { remote_jobs: true, auto_update: true, prove: true, paused: true, sweep: false, power_control: true, address: "0xabc".into(), ..Default::default() };
        s.cards.insert("nvidia:0:x".into(), CardPref { enabled: true, identities: 8, sweep_at: 1_791_000_000, pinned: true, power_pct: 70, ..Default::default() });
        let m = s.for_measurement();
        assert!(!m.remote_jobs && !m.auto_update && !m.prove && !m.paused && m.sweep && !m.power_control && m.setup_done);
        assert_eq!(m.address, "0xabc", "the payout address is the installed app's");
        let c = &m.cards["nvidia:0:x"];
        assert!(c.enabled && c.identities == 8 && c.power_pct == 70, "the card's choices stay");
        assert!(c.sweep_at == 0 && !c.pinned, "every card is due and unpinned");
    }

    #[test]
    fn manifest_url_round_trips_through_the_token() {
        assert_eq!(manifest_url_for_token("abc123"), "https://dl.igneum.network/dl/abc123/igneum-app-latest.json");
        assert_eq!(manifest_url_for_token(" abc123\n"), "https://dl.igneum.network/dl/abc123/igneum-app-latest.json");
        assert_eq!(manifest_url_for_token(""), "");
        assert_eq!(token_of_manifest_url("https://dl.igneum.network/dl/abc123/igneum-app-latest.json"), Some("abc123"));
        assert_eq!(token_of_manifest_url("https://dl.igneum.network/dl/abc123/other.json"), None);
        assert_eq!(token_of_manifest_url("http://127.0.0.1:8080/dl/t/igneum-app-latest.json"), None);
        assert_eq!(token_of_manifest_url(""), None);
    }

    #[test]
    fn secret_files_are_trimmed_and_blank_means_none() {
        let f = tmp("key", "  thekey0123456789abcdef \n");
        assert_eq!(read_secret_file(&f).as_deref(), Some("thekey0123456789abcdef"));
        std::fs::write(&f, " \n").unwrap();
        assert_eq!(read_secret_file(&f), None);
        let _ = std::fs::remove_file(&f);
        assert_eq!(read_secret_file(Path::new("/nonexistent/igneum/key")), None);
    }

    #[test]
    fn fingerprint_matches_shasum() {
        // printf abc | shasum -a 256 | cut -c1-8
        assert_eq!(fingerprint8("abc"), "ba7816bf");
        assert_eq!(fingerprint8("").len(), 8);
    }

    #[test]
    fn load_records_the_sources() {
        let p = packaged("oldkey0123456789abcdef", "oldtok");
        assert_eq!(p.key_source, "packaged");
        assert_eq!(p.manifest_source, "packaged");
        let none = Packaged::load(&[PathBuf::from("/nonexistent/igneum-app.json")]);
        assert_eq!(none.key_source, "none");
        assert!(none.upload_key.is_empty());
        if crate::edition::LAB {
            assert_eq!(none.manifest_source, "none");
            assert!(none.update_manifest.is_empty(), "the lab build gets no default manifest");
        } else {
            assert_eq!(none.manifest_source, "default");
            assert_eq!(none.update_manifest, DEFAULT_UPDATE_MANIFEST);
        }
    }

    /// 12 October 2026: a public package that names no manifest reads the release store's; a packaged URL stays.
    #[test]
    fn a_public_package_without_a_manifest_reads_the_release_store() {
        assert_eq!(DEFAULT_UPDATE_MANIFEST, format!("{RELEASE_BASE}/latest/igneum-app-latest.json"));
        assert!(DEFAULT_UPDATE_MANIFEST.starts_with("https://build.igneum.network/releases/ember/"));
        let p = tmp("packaged-no-manifest.json", r#"{"update_manifest":"","upload_url":"https://igneum-six.vercel.app/api/log"}"#);
        let out = Packaged::load(&[p.clone()]);
        let _ = std::fs::remove_file(p);
        if crate::edition::LAB {
            assert!(out.update_manifest.is_empty());
        } else {
            assert_eq!(out.update_manifest, DEFAULT_UPDATE_MANIFEST);
            assert_eq!(out.manifest_source, "default");
            assert!(out.describe().contains("release store"), "{}", out.describe());
        }
        let kept = packaged("oldkey0123456789abcdef", "oldtok");
        assert_eq!(kept.update_manifest, manifest_url_for_token("oldtok"), "a packaged URL is kept as it is");
        assert_eq!(kept.manifest_source, "packaged");
    }

    #[test]
    fn file_overrides_replace_the_key_and_the_folder() {
        let key = tmp("upload-key.next", "newkey0123456789abcdef\n");
        let tok = tmp("dl-token.next", "newtok\n");
        let p = packaged("oldkey0123456789abcdef", "oldtok").with_file_overrides(Some(&key), Some(&tok));
        assert_eq!(p.upload_key, "newkey0123456789abcdef");
        assert_eq!(p.update_manifest, "https://dl.igneum.network/dl/newtok/igneum-app-latest.json");
        assert_eq!(p.upload_url, "https://igneum-six.vercel.app/api/log");
        assert!(p.key_source.starts_with("file ") && p.key_source.ends_with("upload-key.next"), "{}", p.key_source);
        assert!(p.manifest_source.ends_with("dl-token.next"), "{}", p.manifest_source);
        // only the key: the folder stays packaged
        let p = packaged("oldkey0123456789abcdef", "oldtok").with_file_overrides(Some(&key), None);
        assert_eq!(p.update_manifest, "https://dl.igneum.network/dl/oldtok/igneum-app-latest.json");
        assert_eq!(p.manifest_source, "packaged");
        // a missing or blank file changes nothing
        let blank = tmp("blank", "\n");
        let p = packaged("oldkey0123456789abcdef", "oldtok").with_file_overrides(Some(&blank), Some(Path::new("/nonexistent/dl-token")));
        assert_eq!(p.upload_key, "oldkey0123456789abcdef");
        assert_eq!(p.update_manifest, "https://dl.igneum.network/dl/oldtok/igneum-app-latest.json");
        assert_eq!(p.key_source, "packaged");
        // a developer run with no packaged file at all: the key file brings the default intake
        let p = Packaged::load(&[PathBuf::from("/nonexistent/igneum-app.json")]).with_file_overrides(Some(&key), Some(&tok));
        assert_eq!(p.upload_url, DEFAULT_INTAKE_URL);
        assert_eq!(p.update_manifest, "https://dl.igneum.network/dl/newtok/igneum-app-latest.json");
        for f in [key, tok, blank] {
            let _ = std::fs::remove_file(f);
        }
    }

    #[test]
    fn describe_never_carries_the_values() {
        let p = packaged("oldkey0123456789abcdef", "oldtok");
        let d = p.describe();
        assert!(!d.contains("oldkey"), "{d}");
        assert!(!d.contains("oldtok"), "{d}");
        assert!(d.contains("<token>/igneum-app-latest.json"), "{d}");
        assert!(d.contains(&format!("key {}", fingerprint8("oldkey0123456789abcdef"))), "{d}");
        assert!(d.contains(&format!("folder {}", fingerprint8("oldtok"))), "{d}");
        assert!(d.contains("(packaged)"), "{d}");
        let none = Packaged::load(&[PathBuf::from("/nonexistent/igneum-app.json")]).describe();
        assert!(none.contains("no key") && none.contains("(none)"), "{none}");
        assert!(if crate::edition::LAB { none.contains("no folder") } else { none.contains("release store") && none.contains("(default)") }, "{none}");
        let empty = Packaged::default().describe();
        assert!(empty.contains("no folder"), "{empty}");
        let custom = Packaged { update_manifest: "http://127.0.0.1:9/dl/t/igneum-app-latest.json".into(), ..Packaged::default() }.describe();
        assert!(custom.contains("custom"), "{custom}");
    }

    #[test]
    fn packaged_carries_the_node_override_params() {
        let p: Packaged = serde_json::from_str(r#"{"update_manifest":"","node_override_params":{"difficulty_v2_activation_daa":123456}}"#).unwrap();
        assert_eq!(p.node_override_params.as_ref().unwrap()["difficulty_v2_activation_daa"], 123456);
        let p: Packaged = serde_json::from_str(r#"{"update_manifest":""}"#).unwrap();
        assert!(p.node_override_params.is_none());
    }
}

#[cfg(test)]
mod fixture_tests {
    /// `IGNEUM_TEST_SETTINGS=<path> cargo test settings_fixture`: parses a real settings.json with this crate's
    /// struct and prints what it read (6 October 2026: the first Windows machine's copied file read as defaults; this names the field).
    #[test]
    fn settings_fixture_parses_when_given() {
        let Ok(p) = std::env::var("IGNEUM_TEST_SETTINGS") else { return };
        let t = std::fs::read_to_string(&p).unwrap();
        match serde_json::from_str::<super::Settings>(&t) {
            Ok(s) => println!("parsed: address {} cards {} remote_jobs {} setup_done {}", s.address, s.cards.len(), s.remote_jobs, s.setup_done),
            Err(e) => panic!("the crate refuses the file: {e}"),
        }
    }

}

// Ember 0.2.17 (the release team's rule RELEASE.md 4c, 15 October 2026): the chain this build pins is the chains file's
// entry alone, dev/chain/igneum-chains.json, the same entry the MANIFEST and the pointer carry; every host the build names
// is one the entry names. The test reads the file at the repository root and refuses a pin that drifted from it.
#[cfg(test)]
mod chains_file_tests {
    use super::*;
    fn first_word(v: &serde_json::Value) -> String {
        let s = match v { serde_json::Value::Array(a) => a.first().and_then(|x| x.as_str()).unwrap_or(""), serde_json::Value::String(s) => s.as_str(), _ => "" };
        s.split_whitespace().next().unwrap_or("").to_string()
    }
    #[test]
    fn the_pin_is_the_chains_file_entry() {
        let path = std::path::Path::new(env!("CARGO_MANIFEST_DIR")).join("../../../../dev/chain/igneum-chains.json");
        let text = std::fs::read_to_string(&path).unwrap_or_else(|e| panic!("the chains file {} is not readable: {e}", path.display()));
        let doc: serde_json::Value = serde_json::from_str(&text).expect("the chains file is JSON");
        let entry = doc["networks"].as_array().expect("networks").iter().find(|n| n["chain_id"].as_u64() == Some(CHAIN_ID))
            .unwrap_or_else(|| panic!("no entry for chain {CHAIN_ID} in the chains file"));
        assert_eq!(entry["name"].as_str(), Some(CHAIN_NAME), "the chain's name");
        assert_eq!(entry["network_id"].as_u64(), Some(NETWORK_ID), "the network id");
        assert_eq!(entry["genesis_hash"].as_str(), Some(GENESIS_HASH), "the genesis hash");
        assert_eq!(entry["live"].as_bool(), Some(true), "the entry is live");
        // the entry names the public pool on both ports since step two (16 October 2026: "pool.igneum.network:3341 and :3342, one
        // 7322 stratum on both ports"); the default is one of the ports the entry names
        let pool_line = entry["pool"].as_str().unwrap_or("");
        assert!(pool_line.contains(&format!("{DEFAULT_POOL_HOST}:{DEFAULT_POOL_PORT}")) || (pool_line.contains(DEFAULT_POOL_HOST) && pool_line.contains(&format!(":{DEFAULT_POOL_PORT}"))), "the default pool is one of the entry's stratum ports: {pool_line}");
        assert_eq!(first_word(&entry["rpc"]), crate::scene::RPC_URL, "the RPC host is the entry's own public host");
        assert_eq!(first_word(&entry["feed"]), crate::scene::STATUS_URL, "the status feed is the entry's always path");
        assert_eq!(crate::pools::STATUS_URL, crate::scene::STATUS_URL, "one status feed in the build");
        assert_ne!(PREVIOUS_CHAIN_ID, CHAIN_ID); assert_ne!(PREVIOUS_DEFAULT_POOL_PORT, DEFAULT_POOL_PORT);
    }
}
