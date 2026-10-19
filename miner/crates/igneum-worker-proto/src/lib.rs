//! The worker protocol, version 0, as GPU engineer A published it (docs/gpu-a/WORKER-PROTOCOL.md): one worker
//! process per device, NDJSON on stdin and stdout, `t` and `seq` on every object, `re` on replies, integers
//! that may exceed 2^53 as decimal strings, 32-byte values as lower-case hex. The worker speaks first with
//! `hello`; the supervisor's first line is `configure`.
//!
//! Draft 0 as amended on 9 October 2026: per-device `knobs` (additive or `x2` ladders) in `hello.devices[]`,
//! `vendor`, `kind`, `bus_id`, `slowdown_c` on each device, a one-knob `set` answered by `knob`. The extensions
//! this crate still carries (optional on the wire; absent is handled): `hello.note`, `job.algo`, `stat.phase`
//! and `stat.epoch`, the telemetry fields
//! `fan_pct`, `util_pct`, `thermal_pressure`, `on_battery` and GPU engineer B's Metal set, and `need_bytes` and
//! `have_bytes` on `refuse`.
//!
//! Everything that depends on the chosen hash (Phase Zero) is opaque here: the candidate id, its `params`
//! document, and how the worker derives the epoch from `height`.

use serde::de::DeserializeOwned;
use serde::{Deserialize, Serialize};

pub mod stubhash;

pub const PROTO_VERSION: u32 = 0;
/// The longest line either side accepts, in bytes, including the newline.
pub const MAX_LINE_BYTES: usize = 1 << 20;
/// The longest free-text string in any field.
pub const MAX_STRING_BYTES: usize = 256;
pub const MAX_JOB_ID_BYTES: usize = 64;
pub const HEX_256: usize = 64;
pub const MAX_DEVICES: usize = 64;
pub const MAX_KNOBS: usize = 16;
/// F-E54 (15 October 2026): the VectorWork coordinates a source may serve (the engine keeps one context per coordinate;
/// a job naming a coordinate at or above this bound is read as a plain job, as every coordinate above 4 was before).
pub const MAX_COORDINATES: usize = 64;
/// The exit code a worker uses when the supervisor's first line is not `configure`.
pub const EXIT_BAD_FIRST_LINE: i32 = 64;

/// Knob values keyed by the knob's declared name (extension).
pub type Params = std::collections::BTreeMap<String, f64>;

#[derive(Debug, thiserror::Error)]
pub enum ProtoError {
    #[error("line is {0} bytes, the limit is {MAX_LINE_BYTES}")]
    LineTooLong(usize),
    #[error("not a protocol message: {0}")]
    Json(#[from] serde_json::Error),
    #[error("field {field}: {why}")]
    Field { field: &'static str, why: String },
}

fn field(field: &'static str, why: impl Into<String>) -> ProtoError {
    ProtoError::Field { field, why: why.into() }
}

/// `u64` on the wire as a decimal string (the protocol's rule for integers that may exceed 2^53).
pub mod dec {
    use serde::{Deserialize, Deserializer, Serializer};
    pub fn serialize<S: Serializer>(v: &u64, s: S) -> Result<S::Ok, S::Error> {
        s.serialize_str(&v.to_string())
    }
    pub fn deserialize<'de, D: Deserializer<'de>>(d: D) -> Result<u64, D::Error> {
        #[derive(Deserialize)]
        #[serde(untagged)]
        enum Raw {
            S(String),
            N(u64),
        }
        match Raw::deserialize(d)? {
            Raw::S(s) => s.trim().parse::<u64>().map_err(serde::de::Error::custom),
            Raw::N(n) => Ok(n),
        }
    }
}

/// A pressure word (`normal`, `warning`, `critical`) that a worker may write as the OS level's number instead: the
/// Metal host writes `memory_pressure` as 0, 1 or 2 (device.h: 0 normal, 1 warning, 2 critical; the mini's first
/// parity row, 11 October 2026, where the stat was refused as malformed); a number is read as its word, a string as
/// itself; written out always as the word.
pub mod pressure_word {
    use serde::{Deserialize, Deserializer, Serializer};
    pub fn serialize<S: Serializer>(v: &Option<String>, s: S) -> Result<S::Ok, S::Error> {
        match v {
            Some(v) => s.serialize_str(v),
            None => s.serialize_none(),
        }
    }
    pub fn deserialize<'de, D: Deserializer<'de>>(d: D) -> Result<Option<String>, D::Error> {
        #[derive(Deserialize)]
        #[serde(untagged)]
        enum Raw {
            S(String),
            N(i64),
            None,
        }
        Ok(match Option::<Raw>::deserialize(d)? {
            Some(Raw::S(s)) => Some(s),
            Some(Raw::N(n)) => Some(match n { 0 => "normal".to_string(), 1 => "warning".to_string(), 2 => "critical".to_string(), x => x.to_string() }),
            Some(Raw::None) | None => None,
        })
    }
}

/// `Option<u64>` as a decimal string.
pub mod dec_opt {
    use serde::{Deserialize, Deserializer, Serializer};
    pub fn serialize<S: Serializer>(v: &Option<u64>, s: S) -> Result<S::Ok, S::Error> {
        match v {
            Some(v) => s.serialize_str(&v.to_string()),
            None => s.serialize_none(),
        }
    }
    pub fn deserialize<'de, D: Deserializer<'de>>(d: D) -> Result<Option<u64>, D::Error> {
        #[derive(Deserialize)]
        #[serde(untagged)]
        enum Raw {
            S(String),
            N(u64),
            None,
        }
        match Option::<Raw>::deserialize(d)? {
            Some(Raw::S(s)) => s.trim().parse::<u64>().map(Some).map_err(serde::de::Error::custom),
            Some(Raw::N(n)) => Ok(Some(n)),
            Some(Raw::None) | None => Ok(None),
        }
    }
}

// ---------------------------------------------------------------------------------------------------------------
// shared payloads

/// A launch knob the tuner owns on one device (`hello.devices[].knobs`): `step` is additive, or `"x2"` for a
/// doubling ladder from `min`. Knobs change execution only, never the accepted hash.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct Knob {
    pub name: String,
    pub min: f64,
    pub max: f64,
    pub step: Step,
    pub default: f64,
    #[serde(default)]
    pub unit: String,
}

/// An additive step, or the doubling ladder `"x2"`.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(untagged)]
pub enum Step {
    Add(f64),
    Ladder(String),
}

impl Knob {
    pub fn additive(name: &str, min: f64, max: f64, step: f64, default: f64, unit: &str) -> Knob {
        Knob { name: name.into(), min, max, step: Step::Add(step), default, unit: unit.into() }
    }
    pub fn doubling(name: &str, min: f64, max: f64, default: f64, unit: &str) -> Knob {
        Knob { name: name.into(), min, max, step: Step::Ladder("x2".into()), default, unit: unit.into() }
    }
    fn is_x2(&self) -> bool {
        matches!(&self.step, Step::Ladder(s) if s == "x2")
    }
    /// Snap a value onto the knob's ladder and inside its range.
    pub fn clamp(&self, v: f64) -> f64 {
        if !v.is_finite() {
            return self.default;
        }
        if self.is_x2() {
            if !(self.min > 0.0) {
                return self.default;
            }
            let k = (v.max(self.min) / self.min).log2().round().max(0.0);
            let snapped = self.min * 2f64.powi(k as i32);
            return snapped.max(self.min).min(self.max);
        }
        match self.step {
            Step::Add(step) if step > 0.0 => {
                let steps = ((v - self.min) / step).round();
                (self.min + steps * step).max(self.min).min(self.max)
            }
            _ => self.default,
        }
    }
    /// One step up or down the ladder; `None` at the end.
    pub fn neighbour(&self, v: f64, up: bool) -> Option<f64> {
        let next = if self.is_x2() {
            if up { v * 2.0 } else { v / 2.0 }
        } else {
            match self.step {
                Step::Add(step) => if up { v + step } else { v - step },
                Step::Ladder(_) => return None,
            }
        };
        let next = (next * 1e9).round() / 1e9;
        if next < self.min - 1e-9 || next > self.max + 1e-9 {
            None
        } else {
            Some(next.max(self.min).min(self.max))
        }
    }
    pub fn validate(&self) -> Result<(), ProtoError> {
        check_str("knob.name", &self.name, 32)?;
        check_str("knob.unit", &self.unit, 16)?;
        let ok = match &self.step {
            Step::Add(step) => *step > 0.0,
            Step::Ladder(l) => l == "x2" && self.min > 0.0,
        };
        if !ok || self.min > self.max || !self.min.is_finite() || !self.max.is_finite() {
            return Err(field("knob", format!("{}: min {} max {} step {:?}", self.name, self.min, self.max, self.step)));
        }
        Ok(())
    }
}

/// A candidate the worker's binary carries and whether its embedded vectors passed at start.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct AlgoEntry {
    pub id: String,
    pub vectors_ok: bool,
}

/// One device as `hello` lists it.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct HelloDevice {
    pub id: String,
    pub name: String,
    #[serde(with = "dec")]
    pub memory_bytes: u64,
    pub arch: String,
    /// The driver's whole device line where the worker shortened `arch` to the chip name (Mesa's rusticl names a card
    /// "AMD Radeon RX 580 Series (radeonsi, polaris10, ACO, DRM 3.61, 6.12.111+deb13-amd64)"; the Octominer rig, 16 October 2026).
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub arch_detail: Option<String>,
    pub driver: String,
    #[serde(default)]
    pub runtime: String,
    /// `nvidia`, `amd`, `intel`, `apple`.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub vendor: Option<String>,
    /// `discrete`, `integrated`, `apple`.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub kind: Option<String>,
    /// The stable device address for the fingerprint: the PCI bus id, or the registry id on Apple.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub bus_id: Option<String>,
    /// The card's own identity for the fingerprint where the worker reads one (OpenCL: `CL_DEVICE_UUID_KHR`, the Linux
    /// amdgpu `unique_id`; Metal: the platform serial), with its word in `identity`; absent means the bus-id form.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub intrinsic_id: Option<String>,
    /// `uuid`, `amdgpu-unique-id`, `platform-serial`; absent or `bus` means the slot.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub identity: Option<String>,
    /// The driver's slowdown temperature where it reports one, else absent.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub slowdown_c: Option<f64>,
    /// The launch knobs the tuner owns on this device; empty means nothing to tune.
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub knobs: Vec<Knob>,
}

/// The memory budget terms the worker measured for a device (DESIGN.md section 6 of gpu-a).
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Default)]
pub struct Budget {
    #[serde(with = "dec")]
    pub total_bytes: u64,
    #[serde(with = "dec")]
    pub free_bytes: u64,
    #[serde(with = "dec")]
    pub fit_cold_bytes: u64,
    #[serde(with = "dec")]
    pub fit_warm_bytes: u64,
    pub can_warm: bool,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Default)]
pub struct Telemetry {
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub power_w: Option<f64>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub temp_c: Option<f64>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub core_mhz: Option<f64>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub mem_mhz: Option<f64>,
    #[serde(default, with = "dec_opt", skip_serializing_if = "Option::is_none")]
    pub memory_used_bytes: Option<u64>,
    /// `thermal`, `power`, `reliability`, `other`
    #[serde(default)]
    pub throttle: Vec<String>,
    /// Extension.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub fan_pct: Option<f64>,
    /// Extension.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub util_pct: Option<f64>,
    /// Extension (Apple): `nominal`, `fair`, `serious`, `critical`.
    #[serde(default, with = "pressure_word", skip_serializing_if = "Option::is_none")]
    pub thermal_pressure: Option<String>,
    /// Extension.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub on_battery: Option<bool>,
    /// Metal (GPU engineer B): macOS low power mode; the battery pause keys on this as well as `on_battery`.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub low_power_mode: Option<bool>,
    /// Metal: `normal`, `warning`, `critical`.
    #[serde(default, with = "pressure_word", skip_serializing_if = "Option::is_none")]
    pub memory_pressure: Option<String>,
    /// Metal: the machine is asleep or about to sleep.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub asleep: Option<bool>,
    /// Metal: the duty cycle the worker is actually achieving.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub duty_effective: Option<f64>,
    /// Metal: bytes the GPU holds for this worker in unified memory.
    #[serde(default, with = "dec_opt", skip_serializing_if = "Option::is_none")]
    pub gpu_allocated_bytes: Option<u64>,
    /// Extension: the compiled code resident for the current parameter set (a program mode's kernel cache), when
    /// the worker knows it; absent means not reported.
    #[serde(default, with = "dec_opt", skip_serializing_if = "Option::is_none")]
    pub code_bytes: Option<u64>,
    /// Extension (the GPU team's sliced build): a next-epoch build is in flight on the mining queue.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub build_in_flight: Option<bool>,
    /// Extension: the build's progress, 0 to 1.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub build_progress: Option<f64>,
    /// Extension: the slices the swap had to finish, in ms (the only residual stall of a warm swap).
    #[serde(default, with = "dec_opt", skip_serializing_if = "Option::is_none")]
    pub last_swap_wait_ms: Option<u64>,
}

impl Telemetry {
    /// Which fields this worker reports, for the interface's "not reported by this worker" labels.
    pub fn reports(&self) -> Vec<&'static str> {
        let mut v = Vec::new();
        if self.power_w.is_some() {
            v.push("power_w");
        }
        if self.temp_c.is_some() {
            v.push("temp_c");
        }
        if self.fan_pct.is_some() {
            v.push("fan_pct");
        }
        if self.util_pct.is_some() {
            v.push("util_pct");
        }
        if self.thermal_pressure.is_some() {
            v.push("thermal_pressure");
        }
        if self.on_battery.is_some() {
            v.push("on_battery");
        }
        if self.low_power_mode.is_some() {
            v.push("low_power_mode");
        }
        if self.memory_pressure.is_some() {
            v.push("memory_pressure");
        }
        v
    }
}

#[derive(Debug, Clone, Copy, Serialize, Deserialize, PartialEq, Eq)]
#[serde(rename_all = "lowercase")]
pub enum WorkerState {
    Idle,
    Preparing,
    Mining,
    Paused,
    /// F-470's worker half (17 October 2026): the worker is rebuilding the period's programme on the card after an
    /// anchor set or a new job and will be silent while it does; the next ordinary stat says mining. The wire word
    /// is "building". A supervisor that reads it opens the build's allowance; one that predates it reads the stat
    /// as a preparing one, never a fault.
    Building,
}

#[derive(Debug, Clone, Copy, Serialize, Deserialize, PartialEq, Eq)]
#[serde(rename_all = "lowercase")]
pub enum Transition {
    Auto,
    Cold,
    Warm,
}

// ---------------------------------------------------------------------------------------------------------------
// worker to supervisor

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct Hello {
    pub proto: u32,
    pub worker: String,
    pub version: String,
    /// `cpu`, `cuda`, `opencl`, `metal` (and `stub` for the stub)
    pub backend: String,
    pub algos: Vec<AlgoEntry>,
    pub devices: Vec<HelloDevice>,
    /// Extension: free text the interface shows beside the worker's name ("stub: not consensus").
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub note: String,
    /// Extension: the optional parts of the protocol this worker takes, by name (`keyed-job`: the job's `sk` under
    /// a keyed document). A worker without the list takes none; the supervisor never sends what is not listed.
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub extensions: Vec<String>,
}

/// The name a worker lists in `hello.extensions` when it takes the job's `sk` (the keyed rounds).
pub const EXT_KEYED_JOB: &str = "keyed-job";

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct Ready {
    pub device: String,
    pub algo: String,
    pub budget: Budget,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct Refuse {
    #[serde(default)]
    pub device: String,
    pub reason: String,
    /// The overflowing budget term, when memory is the reason.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub term: Option<String>,
    #[serde(default, with = "dec_opt", skip_serializing_if = "Option::is_none")]
    pub need_bytes: Option<u64>,
    #[serde(default, with = "dec_opt", skip_serializing_if = "Option::is_none")]
    pub have_bytes: Option<u64>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct Prepared {
    #[serde(with = "dec")]
    pub epoch: u64,
    pub ms: u64,
    #[serde(with = "dec")]
    pub bytes: u64,
    /// `cold` or `warm`
    pub mode: String,
    /// Extension: the dataset's commitment (hex) when the worker can compute it; absent means not reported.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub digest: Option<String>,
}

/// A result that met the job's `share_target`. Verified with the reference before anything else.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct Share {
    pub job_id: String,
    #[serde(with = "dec")]
    pub nonce: u64,
    /// hex 64
    pub mix: String,
    /// hex 64
    pub hash: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct Stat {
    #[serde(default)]
    pub job_id: String,
    /// Hashes since the previous `stat`.
    #[serde(with = "dec")]
    pub hashes: u64,
    pub elapsed_ms: u64,
    #[serde(default)]
    pub telemetry: Telemetry,
    pub state: WorkerState,
    /// Extension: what a `preparing` worker is doing: `dataset` (building the epoch's dataset), `compile`
    /// (building kernels), `transition` (the next epoch replacing the current one).
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub phase: String,
    /// F-470's worker half: what a `building` worker is building ("the period's programme on the set"); absent on
    /// every other state.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub building: Option<String>,
    /// Extension: the epoch the worker holds, once it holds one.
    #[serde(default, with = "dec_opt", skip_serializing_if = "Option::is_none")]
    pub epoch: Option<u64>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct Fault {
    pub code: String,
    pub message: String,
    /// true: the supervisor may resend the job; false: the worker exits after this line.
    pub recoverable: bool,
}

/// The answer to `set`: the value as applied, clamped to the ladder. `knob` defaults to empty: a worker that answers
/// without naming the knob (the Metal worker 9c4769ef on `release_dataset`, read by main on the mini's 0.2.5 set,
/// 00:4x UK 13 October 2026: "missing field `knob`" twice closed the slot) is parsed, and the supervisor takes the
/// name from the set it sent, never a malformed line.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct KnobAck {
    #[serde(default)]
    pub knob: String,
    #[serde(default)]
    pub value: f64,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(tag = "t", rename_all = "lowercase")]
pub enum FromWorker {
    Hello(Hello),
    Ready(Ready),
    Refuse(Refuse),
    Prepared(Prepared),
    Share(Share),
    Stat(Stat),
    Fault(Fault),
    Bye,
    Knob(KnobAck),
    /// A message kind this version of the protocol does not name (a worker's own line, such as the Metal host's
    /// `jobdone`): well formed, carrying nothing the supervisor acts on. Ignored and counted, never malformed and
    /// never a restart (the mini's parity row 4 stopped its worker on one).
    #[serde(other)]
    Unknown,
}

// ---------------------------------------------------------------------------------------------------------------
// supervisor to worker

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct Configure {
    pub device: String,
    pub algo: String,
    /// OPEN: the candidate's parameter document, passed through to the reference unchanged.
    #[serde(default)]
    pub params: serde_json::Value,
    pub stat_ms: u64,
    pub transition: Transition,
    #[serde(with = "dec")]
    pub memory_margin_bytes: u64,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct Job {
    pub job_id: String,
    #[serde(with = "dec")]
    pub height: u64,
    /// hex 64: the sealing hash as the node gives it
    pub header: String,
    /// hex 64: the block target
    pub boundary: String,
    /// hex 64: the share target (equal to `boundary` when solo and unmeasured)
    pub share_target: String,
    #[serde(with = "dec")]
    pub nonce_start: u64,
    #[serde(with = "dec")]
    pub nonce_count: u64,
    /// true: drop any earlier job
    #[serde(default)]
    pub clean: bool,
    /// The candidate id (the algorithm version) this job is for; the worker refuses a job for another than it was
    /// configured with. Chapter 15 of the IgneumHash plan: the job identity commits to the algorithm version.
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub algo: String,
    /// Extension (the hostile-key rule, SPEC-R0A-v0.md section 0.9, bit 10): the bonded identity's secret, hex of
    /// 32 bytes, present only under a document with keyed rounds and only on a job to a worker whose hello lists
    /// the `keyed-job` extension; the worker folds it into every attempt (the S keyed shares) and keeps it nowhere
    /// else. Absent otherwise. Never formatted, logged or written: `SecretHex` withholds itself from Debug.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub sk: Option<SecretHex>,
}

/// A hex string that never prints itself: Debug writes `[withheld]`, the bytes are zeroed when dropped. The
/// serialised form is the plain string (the worker's stdin line and nothing else carries it).
#[derive(Clone, Serialize, Deserialize, PartialEq)]
#[serde(transparent)]
pub struct SecretHex(String);

impl SecretHex {
    /// From 32 bytes.
    pub fn from_bytes(b: &[u8; 32]) -> SecretHex {
        SecretHex(hex_encode(b))
    }
    /// From hex of 32 bytes (any case, with or without 0x); None otherwise.
    pub fn from_hex(s: &str) -> Option<SecretHex> {
        hex_to_32(s).map(|b| SecretHex::from_bytes(&b))
    }
    pub fn as_hex(&self) -> &str {
        &self.0
    }
    pub fn to_bytes(&self) -> Option<[u8; 32]> {
        hex_to_32(&self.0)
    }
}

impl std::fmt::Debug for SecretHex {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str("[withheld]")
    }
}

impl Drop for SecretHex {
    fn drop(&mut self) {
        // the hex is ASCII: zeroing the bytes in place keeps the String valid
        unsafe { self.0.as_bytes_mut().fill(0) };
    }
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct Cancel {
    pub job_id: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct Prepare {
    #[serde(with = "dec")]
    pub height: u64,
    /// Extension: the parameter document the dataset at `height` is for, when the network announced a change
    /// (a node's nextParams); absent means the configured document. A worker without the extension builds on
    /// the configured document, and the supervisor's reconfigure on the change takes the cold path.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub params: Option<serde_json::Value>,
}

/// A live change of one knob from the device's `hello` list, honoured at the next launch.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct Set {
    pub knob: String,
    pub value: f64,
}

/// A `set` whose value is text (the period's programme anchor `r0d_anchor`, 64 hex characters: the GPU team's form of
/// 03:25 UK, 13 October 2026); on the wire the same `{"t":"set","knob":...,"value":"<text>"}`.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct SetText {
    pub knob: String,
    pub value: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(tag = "t", rename_all = "lowercase")]
pub enum ToWorker {
    Configure(Configure),
    #[serde(rename = "set")]
    SetText(SetText),
    Job(Job),
    Cancel(Cancel),
    Pause,
    Resume,
    Prepare(Prepare),
    Shutdown,
    Set(Set),
}

// ---------------------------------------------------------------------------------------------------------------
// framing: `seq` on every line out, `re` optional on lines in

/// A line with its `seq` (and `re` when it answers one).
#[derive(Debug, Clone, PartialEq)]
pub struct Envelope<T> {
    pub seq: u64,
    pub re: Option<u64>,
    pub body: T,
}

/// Encode a message with its sequence number, as one newline-terminated line.
pub fn encode<T: Serialize>(seq: u64, re: Option<u64>, m: &T) -> Result<String, ProtoError> {
    let mut v = serde_json::to_value(m)?;
    if let Some(o) = v.as_object_mut() {
        o.insert("seq".into(), serde_json::Value::from(seq));
        if let Some(r) = re {
            o.insert("re".into(), serde_json::Value::from(r));
        }
    }
    let mut s = serde_json::to_string(&v)?;
    s.push('\n');
    if s.len() > MAX_LINE_BYTES {
        return Err(ProtoError::LineTooLong(s.len()));
    }
    Ok(s)
}

/// Decode one line into its envelope; the trailing newline may be present or not.
pub fn decode<T: DeserializeOwned>(line: &str) -> Result<Envelope<T>, ProtoError> {
    if line.len() > MAX_LINE_BYTES {
        return Err(ProtoError::LineTooLong(line.len()));
    }
    let v: serde_json::Value = serde_json::from_str(line.trim_end_matches(['\n', '\r']))?;
    let seq = v.get("seq").and_then(|x| x.as_u64()).unwrap_or(0);
    let re = v.get("re").and_then(|x| x.as_u64());
    let body: T = serde_json::from_value(v)?;
    Ok(Envelope { seq, re, body })
}

/// A per-direction sequence counter starting at 1.
#[derive(Debug, Default)]
pub struct Seq(u64);

impl Seq {
    pub fn next(&mut self) -> u64 {
        self.0 += 1;
        self.0
    }
}

// ---------------------------------------------------------------------------------------------------------------
// bounds

pub fn is_hex(s: &str, len: usize) -> bool {
    s.len() == len && s.bytes().all(|b| matches!(b, b'0'..=b'9' | b'a'..=b'f'))
}

fn check_hex(name: &'static str, s: &str, len: usize) -> Result<(), ProtoError> {
    if is_hex(s, len) {
        Ok(())
    } else {
        Err(field(name, format!("expected {len} lower-case hex characters, got {} bytes", s.len())))
    }
}

fn check_str(name: &'static str, s: &str, max: usize) -> Result<(), ProtoError> {
    if s.len() > max {
        return Err(field(name, format!("{} bytes, the limit is {max}", s.len())));
    }
    if s.bytes().any(|b| b < 0x20 && b != b'\t') {
        return Err(field(name, "control characters"));
    }
    Ok(())
}

fn check_telemetry(t: &Telemetry) -> Result<(), ProtoError> {
    if t.throttle.len() > 8 {
        return Err(field("throttle", "at most 8 flags"));
    }
    for f in &t.throttle {
        check_str("throttle", f, 16)?;
    }
    if let Some(tp) = &t.thermal_pressure {
        check_str("thermal_pressure", tp, 16)?;
    }
    for (name, v) in [("power_w", t.power_w), ("temp_c", t.temp_c), ("fan_pct", t.fan_pct), ("util_pct", t.util_pct)] {
        if let Some(v) = v {
            if !v.is_finite() || !(-50.0..=10_000.0).contains(&v) {
                return Err(field("telemetry", format!("{name} = {v}")));
            }
        }
    }
    Ok(())
}

/// The supervisor's check on every line from a worker.
/// A worker's free-text fields brought within the protocol's bounds (the project's rule of 18:0x UK, 14 October 2026, after the
/// texture worker's 2,850-byte compile log stopped every NVIDIA card on the public pool): a message over the bound is
/// truncated to it, never refused; the engine logs what was cut. Returns the fields cut, by name and their length.
pub fn bound_from_worker(m: &mut FromWorker) -> Vec<(&'static str, usize)> {
    fn cut(s: &mut String, max: usize) -> Option<usize> {
        // a multi-line log (the texture worker's NVRTC compile log, 18:5x UK, 14 October 2026) reads as one line: every
        // control character becomes a space, so the field passes the control-character check instead of looping the card
        let had_control = s.bytes().any(|b| b < 0x20 && b != b'\t');
        if had_control {
            *s = s.chars().map(|c| if (c as u32) < 0x20 && c != '\t' { ' ' } else { c }).collect();
        }
        if s.len() <= max { return if had_control { Some(s.len()) } else { None }; }
        let n = s.len();
        let mut end = max;
        while end > 0 && !s.is_char_boundary(end) { end -= 1; }
        s.truncate(end);
        Some(n)
    }
    let mut out = Vec::new();
    match m {
        FromWorker::Fault(f) => { if let Some(n) = cut(&mut f.message, MAX_STRING_BYTES) { out.push(("message", n)); } if let Some(n) = cut(&mut f.code, 32) { out.push(("code", n)); } }
        FromWorker::Hello(h) => { if let Some(n) = cut(&mut h.note, MAX_STRING_BYTES) { out.push(("note", n)); } }
        FromWorker::Refuse(r) => { if let Some(n) = cut(&mut r.reason, MAX_STRING_BYTES) { out.push(("reason", n)); } }
        _ => {}
    }
    out
}

pub fn validate_from_worker(m: &FromWorker) -> Result<(), ProtoError> {
    match m {
        FromWorker::Hello(h) => {
            check_str("worker", &h.worker, 64)?;
            check_str("version", &h.version, 32)?;
            check_str("backend", &h.backend, 16)?;
            check_str("note", &h.note, MAX_STRING_BYTES)?;
            if h.algos.is_empty() || h.algos.len() > 16 {
                return Err(field("algos", "between 1 and 16"));
            }
            for a in &h.algos {
                check_str("algos.id", &a.id, 32)?;
            }
            if h.devices.len() > MAX_DEVICES {
                return Err(field("devices", format!("{} devices, the limit is {MAX_DEVICES}", h.devices.len())));
            }
            for d in &h.devices {
                check_str("device.id", &d.id, 128)?;
                check_str("device.name", &d.name, 128)?;
                // 128, never 32: rusticl's whole driver line (74 to 83 bytes) refused every AMD card on the Octominer rig (16 October 2026)
                check_str("device.arch", &d.arch, 128)?;
                if let Some(a) = &d.arch_detail {
                    check_str("device.arch_detail", a, 256)?;
                }
                check_str("device.driver", &d.driver, 64)?;
                check_str("device.runtime", &d.runtime, 64)?;
                if let Some(v) = &d.vendor {
                    check_str("device.vendor", v, 16)?;
                }
                if let Some(k) = &d.kind {
                    check_str("device.kind", k, 16)?;
                }
                if let Some(b) = &d.bus_id {
                    check_str("device.bus_id", b, 64)?;
                }
                if d.knobs.len() > MAX_KNOBS {
                    return Err(field("knobs", format!("{} knobs, the limit is {MAX_KNOBS}", d.knobs.len())));
                }
                for k in &d.knobs {
                    k.validate()?;
                }
            }
        }
        FromWorker::Ready(r) => {
            check_str("device", &r.device, 128)?;
            check_str("algo", &r.algo, 32)?;
        }
        FromWorker::Refuse(r) => {
            check_str("device", &r.device, 128)?;
            check_str("reason", &r.reason, MAX_STRING_BYTES)?;
            if let Some(t) = &r.term {
                check_str("term", t, 32)?;
            }
        }
        FromWorker::Prepared(p) => {
            check_str("mode", &p.mode, 8)?;
            if let Some(d) = &p.digest {
                check_hex("prepared.digest", d, HEX_256)?;
            }
        }
        FromWorker::Share(s) => {
            check_str("job_id", &s.job_id, MAX_JOB_ID_BYTES)?;
            check_hex("mix", &s.mix, HEX_256)?;
            check_hex("hash", &s.hash, HEX_256)?;
        }
        FromWorker::Stat(s) => {
            check_str("job_id", &s.job_id, MAX_JOB_ID_BYTES)?;
            check_str("phase", &s.phase, 16)?;
            check_telemetry(&s.telemetry)?;
        }
        FromWorker::Fault(f) => {
            check_str("code", &f.code, 32)?;
            check_str("message", &f.message, MAX_STRING_BYTES)?;
        }
        FromWorker::Bye | FromWorker::Unknown => {}
        FromWorker::Knob(a) => {
            if !a.knob.is_empty() {
                check_str("knob", &a.knob, 32)?;
            }
            if !a.value.is_finite() {
                return Err(field("knob.value", "not a finite number"));
            }
        }
    }
    Ok(())
}

/// The worker's check on every line from the supervisor.
pub fn validate_to_worker(m: &ToWorker) -> Result<(), ProtoError> {
    match m {
        ToWorker::Configure(c) => {
            check_str("device", &c.device, 128)?;
            check_str("algo", &c.algo, 32)?;
            if c.stat_ms < 100 || c.stat_ms > 60_000 {
                return Err(field("stat_ms", "between 100 and 60000"));
            }
        }
        ToWorker::Job(j) => {
            check_str("job_id", &j.job_id, MAX_JOB_ID_BYTES)?;
            check_hex("header", &j.header, HEX_256)?;
            check_hex("boundary", &j.boundary, HEX_256)?;
            check_hex("share_target", &j.share_target, HEX_256)?;
            if j.nonce_count == 0 {
                return Err(field("nonce_count", "zero"));
            }
            check_str("algo", &j.algo, 32)?;
            if let Some(sk) = &j.sk {
                check_hex("sk", sk.as_hex(), HEX_256)?;
            }
        }
        ToWorker::Cancel(c) => check_str("job_id", &c.job_id, MAX_JOB_ID_BYTES)?,
        ToWorker::Pause | ToWorker::Resume | ToWorker::Shutdown | ToWorker::Prepare(_) => {}
        ToWorker::Set(s) => {
            check_str("knob", &s.knob, 32)?;
            if !s.value.is_finite() {
                return Err(field("set.value", "not a finite number"));
            }
        }
        ToWorker::SetText(s) => {
            check_str("knob", &s.knob, 32)?;
            check_str("set.value", &s.value, 66)?;
        }
    }
    Ok(())
}

// ---------------------------------------------------------------------------------------------------------------
// hex helpers (no dependency)

pub fn hex_encode(bytes: &[u8]) -> String {
    const T: &[u8; 16] = b"0123456789abcdef";
    let mut s = String::with_capacity(bytes.len() * 2);
    for b in bytes {
        s.push(T[(b >> 4) as usize] as char);
        s.push(T[(b & 15) as usize] as char);
    }
    s
}

pub fn hex_decode(s: &str) -> Option<Vec<u8>> {
    if s.len() % 2 != 0 {
        return None;
    }
    let mut out = Vec::with_capacity(s.len() / 2);
    let b = s.as_bytes();
    for i in (0..b.len()).step_by(2) {
        let hi = nibble(b[i])?;
        let lo = nibble(b[i + 1])?;
        out.push((hi << 4) | lo);
    }
    Some(out)
}

fn nibble(c: u8) -> Option<u8> {
    match c {
        b'0'..=b'9' => Some(c - b'0'),
        b'a'..=b'f' => Some(c - b'a' + 10),
        b'A'..=b'F' => Some(c - b'A' + 10),
        _ => None,
    }
}

pub fn hex_to_32(s: &str) -> Option<[u8; 32]> {
    let v = hex_decode(s)?;
    if v.len() != 32 {
        return None;
    }
    let mut a = [0u8; 32];
    a.copy_from_slice(&v);
    Some(a)
}

// ---------------------------------------------------------------------------------------------------------------

#[cfg(test)]
mod bound_tests {
    use super::*;
    #[test]
    fn a_long_fault_message_is_cut_to_the_bound_and_validates() {
        let mut m = FromWorker::Fault(Fault { code: "compile".into(), message: "x".repeat(2850), recoverable: true });
        let cut = bound_from_worker(&mut m);
        assert_eq!(cut, vec![("message", 2850)]);
        if let FromWorker::Fault(f) = &m { assert_eq!(f.message.len(), MAX_STRING_BYTES); } else { panic!() }
        assert!(validate_from_worker(&m).is_ok());
        let mut short = FromWorker::Fault(Fault { code: "c".into(), message: "fine".into(), recoverable: true });
        assert!(bound_from_worker(&mut short).is_empty());
        let mut multi = FromWorker::Fault(Fault { code: "compile".into(), message: "line one\nline two\r\nwarning #179-D".into(), recoverable: true });
        assert_eq!(bound_from_worker(&mut multi).len(), 1);
        if let FromWorker::Fault(f) = &multi { assert_eq!(f.message, "line one line two  warning #179-D"); } else { panic!() }
        assert!(validate_from_worker(&multi).is_ok());
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_hello_device_with_rusticls_whole_driver_line_as_its_arch_validates() {
        // the Octominer rig, 16 October 2026: the OpenCL worker under Mesa's rusticl named every AMD card's arch by the driver's whole
        // line (74 to 83 bytes) and the engine refused each record on a 32-byte limit; no AMD card reached the listing
        let line = "AMD Radeon RX 580 Series (radeonsi, polaris10, ACO, DRM 3.61, 6.12.111+deb13-amd64)";
        assert_eq!(line.len(), 83);
        let d = HelloDevice { id: "0000:03:00.0".into(), name: line.into(), memory_bytes: 8 << 30, arch: line.into(), arch_detail: None, driver: "Mesa 25.2".into(), runtime: "rusticl".into(), vendor: Some("amd".into()), kind: None, bus_id: Some("0000:03:00.0".into()), intrinsic_id: None, identity: None, slowdown_c: None, knobs: vec![] };
        let h = Hello { proto: PROTO_VERSION, worker: "igneum-worker-opencl".into(), version: "0".into(), backend: "opencl".into(), algos: vec![AlgoEntry { id: "igneumhash".into(), vectors_ok: true }], devices: vec![d.clone()], note: String::new(), extensions: vec![] };
        assert!(validate_from_worker(&FromWorker::Hello(h.clone())).is_ok(), "the whole line passes under the 128 limit");
        // the chip name alone with the line in arch_detail (the worker's form from here) passes too, and round-trips
        let short = HelloDevice { arch: "polaris10".into(), arch_detail: Some(line.into()), ..d };
        let h2 = Hello { devices: vec![short.clone()], ..h };
        assert!(validate_from_worker(&FromWorker::Hello(h2.clone())).is_ok());
        let back: Hello = serde_json::from_str(&serde_json::to_string(&h2).unwrap()).unwrap();
        assert_eq!(back.devices[0].arch_detail.as_deref(), Some(line));
        // a 129-byte arch is still refused (the bound stands)
        let long = HelloDevice { arch: "x".repeat(129), ..short };
        assert!(validate_from_worker(&FromWorker::Hello(Hello { devices: vec![long], ..h2 })).is_err());
    }

    #[test]
    fn job_round_trip_with_seq_and_decimal_integers() {
        let j = ToWorker::Job(Job { job_id: "j1".into(), height: 42, header: "00".repeat(32), boundary: "ff".repeat(32), share_target: "ff".repeat(32), nonce_start: 1 << 40, nonce_count: 1 << 20, clean: true, algo: "stub-v0".into(), sk: None });
        let line = encode(7, None, &j).unwrap();
        assert!(line.ends_with('\n'));
        assert!(line.contains("\"t\":\"job\""));
        assert!(line.contains("\"seq\":7"));
        assert!(line.contains("\"nonce_start\":\"1099511627776\""));
        assert!(!line.contains("\"sk\""), "an unkeyed job carries no sk field: {line}");
        let back: Envelope<ToWorker> = decode(&line).unwrap();
        assert_eq!(back.seq, 7);
        assert_eq!(back.body, j);
    }

    /// The keyed job (SPEC 0.9, bit 10): the secret rides the line to the worker and nowhere else; Debug withholds
    /// it, the validator checks its form, and a worker without the extension is never sent one (the supervisor's
    /// rule, tested there).
    #[test]
    fn a_keyed_job_carries_the_secret_on_the_line_and_withholds_it_from_debug() {
        let sk = SecretHex::from_hex(&"ab".repeat(32)).unwrap();
        let j = ToWorker::Job(Job { job_id: "j2".into(), height: 42, header: "00".repeat(32), boundary: "ff".repeat(32), share_target: "ff".repeat(32), nonce_start: 0, nonce_count: 1, clean: true, algo: "igneumhash".into(), sk: Some(sk) });
        assert!(validate_to_worker(&j).is_ok());
        let line = encode(1, None, &j).unwrap();
        assert!(line.contains(&format!("\"sk\":\"{}\"", "ab".repeat(32))), "{line}");
        let dbg = format!("{j:?}");
        assert!(!dbg.contains(&"ab".repeat(32)), "Debug printed the secret: {dbg}");
        assert!(dbg.contains("[withheld]"), "{dbg}");
        let back: Envelope<ToWorker> = decode(&line).unwrap();
        assert_eq!(back.body, j);
        let bad = ToWorker::Job(Job { job_id: "j3".into(), height: 1, header: "00".repeat(32), boundary: "ff".repeat(32), share_target: "ff".repeat(32), nonce_start: 0, nonce_count: 1, clean: true, algo: String::new(), sk: Some(SecretHex("zz".repeat(32))) });
        assert!(validate_to_worker(&bad).is_err(), "a malformed secret is refused before the line is written");
        assert!(SecretHex::from_hex("abc").is_none());
    }

    #[test]
    fn unit_variants_and_replies() {
        let line = encode(1, None, &ToWorker::Pause).unwrap();
        assert_eq!(line.trim(), "{\"seq\":1,\"t\":\"pause\"}");
        let e: Envelope<FromWorker> = decode("{\"t\":\"bye\",\"seq\":9,\"re\":3}").unwrap();
        assert_eq!(e.body, FromWorker::Bye);
        assert_eq!(e.re, Some(3));
    }

    #[test]
    fn a_message_kind_this_version_does_not_name_is_unknown_not_malformed() {
        // The Metal host's `jobdone` line (the mini's parity row 4): well formed, a kind the protocol does not
        // name; it decodes as Unknown and passes the check, so the supervisor counts it and never restarts.
        let e: Envelope<FromWorker> = decode("{\"t\":\"jobdone\",\"seq\":12,\"job_id\":\"j1\",\"hashes\":4096}").unwrap();
        assert_eq!(e.body, FromWorker::Unknown);
        assert!(validate_from_worker(&e.body).is_ok());
        // A line with no kind at all is still malformed.
        assert!(decode::<FromWorker>("{\"seq\":1,\"job_id\":\"j1\"}").is_err());
    }

    #[test]
    fn decimal_integers_accept_numbers_too() {
        let e: Envelope<FromWorker> = decode("{\"t\":\"share\",\"seq\":2,\"job_id\":\"j\",\"nonce\":12345,\"mix\":\"ab\",\"hash\":\"cd\"}").unwrap();
        if let FromWorker::Share(s) = e.body {
            assert_eq!(s.nonce, 12345);
            assert!(validate_from_worker(&FromWorker::Share(s)).is_err());
        } else {
            panic!();
        }
    }

    #[test]
    fn hello_without_extensions_is_valid() {
        let e: Envelope<FromWorker> = decode("{\"t\":\"hello\",\"seq\":1,\"proto\":0,\"worker\":\"w\",\"version\":\"0\",\"backend\":\"cuda\",\"algos\":[{\"id\":\"kawpow-0.9.4\",\"vectors_ok\":true}],\"devices\":[{\"id\":\"0\",\"name\":\"x\",\"memory_bytes\":\"8589934592\",\"arch\":\"sm_75\",\"driver\":\"550\",\"runtime\":\"12.4\"}]}").unwrap();
        assert!(validate_from_worker(&e.body).is_ok());
        if let FromWorker::Hello(h) = e.body {
            assert!(h.devices[0].knobs.is_empty());
            assert_eq!(h.devices[0].memory_bytes, 8 << 30);
            assert_eq!(h.devices[0].vendor, None);
        }
    }

    #[test]
    fn the_anchor_set_is_a_set_with_a_text_value() {
        // the period's programme anchor (the GPU team and the GPU team, 03:2x UK 13 October 2026): the same `set` line, a string
        let a = "ab".repeat(32);
        let line = serde_json::to_string(&ToWorker::SetText(SetText { knob: "r0d_anchor".into(), value: a.clone() })).unwrap();
        assert_eq!(line, format!(r#"{{"t":"set","knob":"r0d_anchor","value":"{a}"}}"#));
        let back: ToWorker = serde_json::from_str(&line).unwrap();
        assert!(matches!(back, ToWorker::SetText(SetText { ref knob, .. }) if knob == "r0d_anchor"));
    }

    #[test]
    fn knob_ack_without_a_name_parses() {
        // the Metal worker's answer to `set release_dataset` (the project's read of the mini's 0.2.5 set, 13 October 2026)
        let a: FromWorker = serde_json::from_str(r#"{"t":"knob","value":1.0}"#).expect("an ack without the knob's name parses");
        match a {
            FromWorker::Knob(k) => { assert_eq!(k.knob, ""); assert_eq!(k.value, 1.0); }
            other => panic!("not a knob ack: {other:?}"),
        }
        // the exact form the Metal and OpenCL workers write today (the GPU team, 00:42 UK 13 October 2026): a `re` and the
        // whole knob table, no name and no value
        let m: FromWorker = serde_json::from_str(r#"{"t":"knob","re":7,"params":{"batch_ms":40,"batch_nonces":1048576}}"#).expect("the Metal worker's ack parses");
        assert!(matches!(m, FromWorker::Knob(KnobAck { ref knob, .. }) if knob.is_empty()));
        let b: FromWorker = serde_json::from_str(r#"{"t":"knob"}"#).expect("an ack with neither field parses");
        assert!(matches!(b, FromWorker::Knob(_)));
        let c: FromWorker = serde_json::from_str(r#"{"t":"knob","knob":"batch_ms","value":40}"#).unwrap();
        assert!(matches!(c, FromWorker::Knob(KnobAck { ref knob, .. }) if knob == "batch_ms"));
    }

    /// The Metal host's stat as it writes it (memory_pressure a number, the decimal fields strings): read, the
    /// number as its word (the mini's first parity row refused this line twice and stopped the worker).
    #[test]
    fn a_metal_stat_with_a_numeric_pressure_level_is_read() {
        let line = "{\"t\":\"stat\",\"seq\":9,\"job_id\":\"\",\"hashes\":\"0\",\"elapsed_ms\":1000,\"telemetry\":{\"memory_used_bytes\":\"2147483648\",\"throttle\":[],\"thermal_pressure\":\"nominal\",\"on_battery\":false,\"low_power_mode\":false,\"memory_pressure\":0,\"asleep\":false,\"duty_effective\":1.0,\"gpu_allocated_bytes\":\"2147483648\"},\"state\":\"idle\"}";
        let e: Envelope<FromWorker> = decode(line).expect("the Metal host's stat line decodes");
        assert!(validate_from_worker(&e.body).is_ok());
        if let FromWorker::Stat(st) = e.body {
            assert_eq!(st.telemetry.memory_pressure.as_deref(), Some("normal"));
            assert_eq!(st.telemetry.thermal_pressure.as_deref(), Some("nominal"));
            assert_eq!(st.telemetry.gpu_allocated_bytes, Some(2147483648));
        } else {
            panic!("not a stat");
        }
        let two: Envelope<FromWorker> = decode(&line.replace("\"memory_pressure\":0", "\"memory_pressure\":2")).unwrap();
        if let FromWorker::Stat(st) = two.body {
            assert_eq!(st.telemetry.memory_pressure.as_deref(), Some("critical"));
        }
    }

    #[test]
    fn bounds() {
        assert!(matches!(decode::<ToWorker>(&"x".repeat(MAX_LINE_BYTES + 1)), Err(ProtoError::LineTooLong(_))));
        let f = FromWorker::Fault(Fault { code: "x".into(), message: "a".repeat(300), recoverable: true });
        assert!(validate_from_worker(&f).is_err());
        let c = ToWorker::Configure(Configure { device: "0".into(), algo: "stub-v0".into(), params: serde_json::Value::Null, stat_ms: 10, transition: Transition::Auto, memory_margin_bytes: 0 });
        assert!(validate_to_worker(&c).is_err());
    }

    #[test]
    fn knob_ladders_additive_and_x2() {
        let k = Knob::additive("intensity", 1.0, 64.0, 1.0, 16.0, "");
        assert_eq!(k.clamp(100.0), 64.0);
        assert_eq!(k.clamp(0.0), 1.0);
        assert_eq!(k.clamp(f64::NAN), 16.0);
        assert_eq!(k.neighbour(64.0, true), None);
        assert_eq!(k.neighbour(1.0, false), None);
        assert_eq!(k.neighbour(16.0, true), Some(17.0));
        let d = Knob::additive("duty", 0.1, 1.0, 0.05, 1.0, "");
        assert_eq!(d.neighbour(1.0, false), Some(0.95));
        assert_eq!(d.neighbour(1.0, true), None);
        let b = Knob::doubling("batch_nonces", 16384.0, 16777216.0, 1048576.0, "nonces");
        assert_eq!(b.clamp(1000000.0), 1048576.0);
        assert_eq!(b.clamp(1.0), 16384.0);
        assert_eq!(b.clamp(1e9), 16777216.0);
        assert_eq!(b.neighbour(1048576.0, true), Some(2097152.0));
        assert_eq!(b.neighbour(16384.0, false), None);
        assert_eq!(b.neighbour(16777216.0, true), None);
        let wire: Knob = serde_json::from_str("{\"name\":\"batch_nonces\",\"min\":16384,\"max\":16777216,\"step\":\"x2\",\"default\":1048576,\"unit\":\"nonces\"}").unwrap();
        assert_eq!(wire, b);
        let wire2: Knob = serde_json::from_str("{\"name\":\"batch_ms\",\"min\":0,\"max\":2000,\"step\":10,\"default\":0}").unwrap();
        assert_eq!(wire2.step, Step::Add(10.0));
        assert!(wire2.validate().is_ok());
    }

    #[test]
    fn hex() {
        assert_eq!(hex_encode(&[0, 255, 16]), "00ff10");
        assert_eq!(hex_decode("00ff10"), Some(vec![0, 255, 16]));
        assert_eq!(hex_decode("0"), None);
        assert!(hex_to_32(&"00".repeat(32)).is_some());
        assert!(hex_to_32("00").is_none());
    }
}
