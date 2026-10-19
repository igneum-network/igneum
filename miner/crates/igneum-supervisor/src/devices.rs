//! The supervisor's view of a device (normalised from `hello.devices`), support labels and the memory check
//! (plan pages 12, 14, 15). The horizon numbers below are the plan's published cohorts; the overheads are
//! placeholders until Phase Zero measures them per cell, and every label that comes from them says so.

use igneum_worker_proto::HelloDevice;
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};

pub const GIB: u64 = 1 << 30;

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct Device {
    /// The worker's id for the device (what `configure.device` carries).
    pub id: String,
    pub index: u32,
    pub backend: String,
    pub name: String,
    /// `nvidia`, `amd`, `apple`, `intel`, `other`
    pub vendor: String,
    /// `discrete`, `integrated`, `apple`, `external`
    pub kind: String,
    pub memory_total: u64,
    /// From the worker's measured budget after `configure`; the total until then.
    pub memory_free: u64,
    pub arch: String,
    pub driver: String,
    pub runtime: String,
    /// The stable device address: the PCI bus id, or the registry id on Apple (the id when absent).
    pub bus_id: String,
    /// sha-256 hex over backend, vendor, the card's identity, name, arch (each followed by a zero byte). The
    /// identity is intrinsic to the card where the host can read one (NVIDIA: the GPU UUID, `GPU-...`, read by
    /// bus id from nvidia-smi at inventory), else the bus_id. The index, the worker's ordinal id and any device
    /// mask are never inputs. The founder's rule of 11 October 2026: the 5080's fingerprint changed with its slot
    /// (0000:11:00.0 to 0D:00.0, the enclosure move) and two 5090s at 0000:01:00.0 on two hosts collided.
    pub fingerprint: String,
    /// What the fingerprint's identity term was: `uuid` (NVIDIA's GPU UUID or OpenCL's CL_DEVICE_UUID_KHR),
    /// `amdgpu-unique-id` (the Linux sysfs file), `platform-serial` (Apple), or `bus` (the slot, named as such).
    #[serde(default)]
    pub identity: String,
    pub slowdown_c: Option<f64>,
}

/// The intrinsic identities of this host's NVIDIA cards, by PCI bus id normalised (`normalise_bus`): the GPU
/// UUIDs nvidia-smi reports. Empty when nvidia-smi is absent, slow (over 10 s) or answers nothing; the bus-id
/// fingerprint then stands and the device's `identity` says `bus`.
pub fn nvidia_uuids_by_bus() -> std::collections::HashMap<String, String> {
    let (tx, rx) = std::sync::mpsc::channel();
    std::thread::spawn(move || {
        let mut cmd = std::process::Command::new("nvidia-smi");
        cmd.args(["--query-gpu=pci.bus_id,uuid", "--format=csv,noheader"]);
        cmd.stdin(std::process::Stdio::null());
        #[cfg(windows)]
        {
            use std::os::windows::process::CommandExt;
            cmd.creation_flags(0x0800_0000); // CREATE_NO_WINDOW: no console flashes on the desktop
        }
        let out = cmd.output().ok().map(|o| String::from_utf8_lossy(&o.stdout).to_string()).unwrap_or_default();
        let _ = tx.send(out);
    });
    let text = rx.recv_timeout(std::time::Duration::from_secs(10)).unwrap_or_default();
    parse_uuid_rows(&text)
}

/// `00000000:0D:00.0, GPU-8b1f...` rows to (normalised bus, uuid).
pub fn parse_uuid_rows(text: &str) -> std::collections::HashMap<String, String> {
    let mut m = std::collections::HashMap::new();
    for line in text.lines() {
        let mut parts = line.split(',').map(|s| s.trim());
        if let (Some(bus), Some(uuid)) = (parts.next(), parts.next()) {
            if uuid.starts_with("GPU-") && !bus.is_empty() {
                m.insert(normalise_bus(bus), uuid.to_string());
            }
        }
    }
    m
}

/// A PCI bus id in one spelling: lower case, the domain cut to its last four hex digits (`0000:0d:00.0` from
/// `00000000:0D:00.0`, `0000:0D:00.0` or `0d:00.0`).
pub fn normalise_bus(bus: &str) -> String {
    let b = bus.trim().to_ascii_lowercase();
    let mut parts: Vec<&str> = b.split(':').collect();
    if parts.len() == 2 {
        parts.insert(0, "0000");
    }
    if parts.len() == 3 {
        let dom = parts[0];
        let dom = if dom.len() > 4 { &dom[dom.len() - 4..] } else { dom };
        format!("{:0>4}:{}:{}", dom, parts[1], parts[2])
    } else {
        b
    }
}

fn guess_vendor(name: &str, arch: &str, backend: &str) -> String {
    let n = name.to_ascii_lowercase();
    let a = arch.to_ascii_lowercase();
    if backend == "metal" || n.contains("apple") || a.starts_with("apple") {
        "apple".into()
    } else if a.starts_with("sm_") || n.contains("nvidia") || n.contains("geforce") || n.contains("rtx") || n.contains("gtx") || backend == "cuda" {
        "nvidia".into()
    } else if a.starts_with("gfx") || n.contains("amd") || n.contains("radeon") {
        "amd".into()
    } else if n.contains("intel") || n.contains("arc") || n.contains("iris") {
        "intel".into()
    } else {
        "other".into()
    }
}

fn guess_kind(name: &str, vendor: &str) -> String {
    let n = name.to_ascii_lowercase();
    if vendor == "apple" {
        "apple".into()
    } else if n.contains("integrated") || n.contains("igpu") || n.contains("iris") || n.contains("uhd graphics") || (vendor == "amd" && n.ends_with("graphics") && !n.contains("rx")) {
        "integrated".into()
    } else {
        "discrete".into()
    }
}

impl Device {
    /// Normalise one `hello` device. The extension fields are used when present and guessed when absent (and
    /// the guess is visible in the interface as the vendor and kind the label was based on).
    pub fn from_hello(backend: &str, index: u32, h: &HelloDevice) -> Device {
        Self::from_hello_with(backend, index, h, &std::collections::HashMap::new())
    }

    /// `from_hello` with the host's intrinsic identities (`nvidia_uuids_by_bus`): an NVIDIA device whose bus id
    /// is in the map is fingerprinted over its GPU UUID, the rest over the bus id as before.
    pub fn from_hello_with(backend: &str, index: u32, h: &HelloDevice, uuids: &std::collections::HashMap<String, String>) -> Device {
        let vendor = h.vendor.clone().unwrap_or_else(|| guess_vendor(&h.name, &h.arch, backend));
        let kind = h.kind.clone().unwrap_or_else(|| guess_kind(&h.name, &vendor));
        let address = h.bus_id.clone().unwrap_or_else(|| h.id.clone());
        let uuid = if vendor == "nvidia" { h.bus_id.as_deref().and_then(|b| uuids.get(&normalise_bus(b))) } else { None };
        // the project's ruling of 11 Oct 2026 21:07 UK: after NVIDIA's map, the worker's own intrinsic id with its word (OpenCL: the
        // device UUID, else the amdgpu unique_id; Metal: the platform serial); the bus-id form, named as such, when none is read
        let own = match (h.identity.as_deref(), h.intrinsic_id.as_deref()) {
            (Some(w), Some(t)) if w != "bus" && !t.is_empty() => Some((w, t)),
            _ => None,
        };
        let (identity, term) = match (uuid, own) {
            (Some(u), _) => ("uuid", u.as_str()),
            (None, Some((w, t))) => (w, t),
            (None, None) => ("bus", address.as_str()),
        };
        let mut hasher = Sha256::new();
        for part in [backend, &vendor, term, &h.name, &h.arch] {
            hasher.update(part.as_bytes());
            hasher.update([0u8]);
        }
        Device {
            id: h.id.clone(),
            index,
            backend: backend.to_string(),
            name: h.name.clone(),
            vendor,
            kind,
            memory_total: h.memory_bytes,
            memory_free: h.memory_bytes,
            arch: h.arch.clone(),
            driver: h.driver.clone(),
            runtime: h.runtime.clone(),
            bus_id: address,
            fingerprint: igneum_worker_proto::hex_encode(&hasher.finalize()),
            identity: identity.to_string(),
            slowdown_c: h.slowdown_c,
        }
    }
}

#[derive(Debug, Clone, Copy, Serialize, Deserialize, PartialEq, Eq)]
#[serde(rename_all = "snake_case")]
pub enum SupportLabel {
    /// Inside the published horizon.
    Supported,
    /// The plan's explicit feasibility cohort (4 GB discrete, 8 GB Macs): may work, never promised.
    FeasibilityCohort,
    /// Below the cohort, or a backend or kind the release does not carry.
    NotSupported,
}

/// The published memory horizon (plan page 15) and the margins the supervisor hands the worker.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct Horizon {
    pub discrete_supported_min: u64,
    pub discrete_feasibility_min: u64,
    pub apple_supported_min: u64,
    pub apple_feasibility_min: u64,
    /// What the driver and the display hold on a discrete card beside the dataset: `memory_margin_bytes`.
    /// Placeholder.
    pub discrete_reserve: u64,
    /// The share of unified memory left to macOS, apps and the display on Apple Silicon. Placeholder.
    pub apple_system_reserve_fraction: f64,
    /// `true` until Phase Zero replaces the placeholders with measured figures.
    pub placeholder: bool,
}

impl Default for Horizon {
    fn default() -> Self {
        Horizon {
            discrete_supported_min: 6 * GIB,
            discrete_feasibility_min: 4 * GIB,
            apple_supported_min: 16 * GIB,
            apple_feasibility_min: 8 * GIB,
            discrete_reserve: 600 * (1 << 20),
            apple_system_reserve_fraction: 0.25,
            placeholder: true,
        }
    }
}

impl Horizon {
    /// The `memory_margin_bytes` for a device: what the worker must leave untouched.
    pub fn margin_for(&self, d: &Device) -> u64 {
        if d.vendor == "apple" {
            (d.memory_total as f64 * self.apple_system_reserve_fraction) as u64
        } else {
            self.discrete_reserve
        }
    }
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct Labelled {
    pub label: SupportLabel,
    pub reason: String,
    /// Off by default (integrated GPUs); the user may turn it on with the reason shown.
    pub default_enabled: bool,
}

pub fn support_label(dev: &Device, h: &Horizon) -> Labelled {
    let gib = |b: u64| format!("{:.0} GB", b as f64 / GIB as f64);
    let placeholder = if h.placeholder { " (horizon figures are the plan's; measured in Phase Zero)" } else { "" };
    if dev.kind == "integrated" && dev.vendor != "apple" {
        return Labelled {
            label: SupportLabel::NotSupported,
            reason: "an integrated graphics processor shares the machine's memory and is slow at this work; off by default".into(),
            default_enabled: false,
        };
    }
    match dev.vendor.as_str() {
        "apple" => {
            if dev.memory_total >= h.apple_supported_min {
                Labelled { label: SupportLabel::Supported, reason: format!("Apple Silicon with {} of unified memory{placeholder}", gib(dev.memory_total)), default_enabled: true }
            } else if dev.memory_total >= h.apple_feasibility_min {
                Labelled { label: SupportLabel::FeasibilityCohort, reason: format!("an {} Mac is in the feasibility cohort: it may work, it is not promised{placeholder}", gib(dev.memory_total)), default_enabled: true }
            } else {
                Labelled { label: SupportLabel::NotSupported, reason: format!("{} of unified memory is under the published cohort{placeholder}", gib(dev.memory_total)), default_enabled: false }
            }
        }
        // F-E36b (the two-card Windows host on 0.2.7, 14 October 2026): "intel" fell to the "no validated path" arm, so the Arc B580 the
        // OpenCL walk had opened was labelled NotSupported and never mined; a discrete Intel card is inside the
        // horizon as an AMD one is (the GPU team's row: the B580 version 15 exact on the OpenCL worker, 36.5 uJ a hash)
        "nvidia" | "amd" | "intel" => {
            if dev.memory_total >= h.discrete_supported_min {
                Labelled { label: SupportLabel::Supported, reason: format!("{} card inside the published horizon{placeholder}", gib(dev.memory_total)), default_enabled: true }
            } else if dev.memory_total >= h.discrete_feasibility_min {
                Labelled { label: SupportLabel::FeasibilityCohort, reason: format!("a {} card is in the feasibility cohort: it may work, it is not promised{placeholder}", gib(dev.memory_total)), default_enabled: true }
            } else {
                Labelled { label: SupportLabel::NotSupported, reason: format!("{} is under the published cohort{placeholder}", gib(dev.memory_total)), default_enabled: false }
            }
        }
        "cpu" => Labelled { label: SupportLabel::NotSupported, reason: "the processor running the shared reference: for tests, verification and dev chains, not a mining target".into(), default_enabled: false },
        other => Labelled { label: SupportLabel::NotSupported, reason: format!("no validated path for a {other} device in this release"), default_enabled: false },
    }
}

/// The sentence for a worker's memory refusal, with the numbers (plan page 15): the machine stays usable.
pub fn refusal_sentence(dev: &Device, need: Option<u64>, have: Option<u64>, term: Option<&str>, reason: &str) -> String {
    let gb = |b: u64| format!("{:.1} GB", b as f64 / GIB as f64);
    match (need, have) {
        // F-E40 (the founder's M5 Max, 14 October 2026): one closed plain sentence; on Apple silicon the figure is the worker's
        // budget (unified memory less the system's headroom and what other apps hold), named as such
        // the founder's M5 Max capture of 15 October 2026 (the project's cell): the budget read 0.0 GB and the sentence named no
        // cause; the worker's own reason (the GPU team's explain string, opening with the term that bound the budget: the device's
        // working set less what this process holds, the system's memory less the headroom, or the supervisor's budget)
        // follows the figures, so the page says which term it was
        (Some(n), Some(h)) if dev.vendor == "apple" => format!("needs {} of unified memory; the worker's budget for it is {} now ({}). Close some apps or mine later; nothing was changed.", gb(n), gb(h), if reason.trim().is_empty() { "memory less the system's headroom and what other apps hold".to_string() } else { reason.trim().trim_end_matches('.').to_string() }),
        (Some(n), Some(h)) => format!("needs {} free, this device has {} free with the driver and the display on. The machine stays as it is; nothing was changed.", gb(n), gb(h)),
        _ => match term {
            Some(t) => format!("{reason} ({t} does not fit). The machine stays as it is; nothing was changed."),
            None => format!("{reason}. The machine stays as it is; nothing was changed."),
        },
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn dev(vendor: &str, kind: &str, total: u64) -> Device {
        Device { id: "0".into(), index: 0, backend: "test".into(), name: "x".into(), vendor: vendor.into(), kind: kind.into(), memory_total: total, memory_free: total, arch: String::new(), driver: String::new(), runtime: String::new(), bus_id: "0".into(), fingerprint: "00".repeat(32), identity: "bus".into(), slowdown_c: None }
    }

    #[test]
    fn labels_follow_the_published_cohorts() {
        let h = Horizon::default();
        assert_eq!(support_label(&dev("nvidia", "discrete", 6 * GIB), &h).label, SupportLabel::Supported);
        assert_eq!(support_label(&dev("nvidia", "discrete", 4 * GIB), &h).label, SupportLabel::FeasibilityCohort);
        assert_eq!(support_label(&dev("amd", "discrete", 2 * GIB), &h).label, SupportLabel::NotSupported);
        assert_eq!(support_label(&dev("apple", "apple", 16 * GIB), &h).label, SupportLabel::Supported);
        assert_eq!(support_label(&dev("apple", "apple", 8 * GIB), &h).label, SupportLabel::FeasibilityCohort);
        let i = support_label(&dev("intel", "integrated", 8 * GIB), &h);
        assert_eq!(i.label, SupportLabel::NotSupported);
        assert!(!i.default_enabled);
        // F-E36b: a discrete Intel card (the Arc B580, 12 GB) is inside the horizon like an AMD one, on by default
        let arc = support_label(&dev("intel", "discrete", 12 * GIB), &h);
        assert_eq!(arc.label, SupportLabel::Supported);
        assert!(arc.default_enabled);
    }

    #[test]
    fn hello_devices_are_normalised_and_fingerprinted() {
        let h = HelloDevice { id: "0".into(), name: "NVIDIA GeForce RTX 3060".into(), memory_bytes: 12 * GIB, arch: "sm_86".into(), arch_detail: None, driver: "550.90".into(), runtime: "12.4".into(), vendor: None, kind: None, bus_id: Some("0000:01:00.0".into()), intrinsic_id: None, identity: None, slowdown_c: None, knobs: vec![] };
        let d = Device::from_hello("cuda", 0, &h);
        assert_eq!(d.vendor, "nvidia");
        assert_eq!(d.kind, "discrete");
        assert_eq!(d.bus_id, "0000:01:00.0");
        assert_eq!(d.fingerprint.len(), 64);
        let m = HelloDevice { id: "r1".into(), name: "Apple M1".into(), memory_bytes: 8 * GIB, arch: "apple7".into(), arch_detail: None, driver: "".into(), runtime: "metal".into(), vendor: None, kind: None, bus_id: None, intrinsic_id: None, identity: None, slowdown_c: None, knobs: vec![] };
        let d2 = Device::from_hello("metal", 0, &m);
        assert_eq!(d2.vendor, "apple");
        assert_eq!(d2.kind, "apple");
        assert_ne!(d.fingerprint, d2.fingerprint);
        assert_eq!(Horizon::default().margin_for(&d2), 2 * GIB);
    }

    #[test]
    fn a_fingerprint_is_the_cards_own_under_any_mask_slot_or_host() {
        // the founder's rule of 11 October 2026: the 5080's fingerprint changed with its slot (0000:11:00.0 to
        // 0D:00.0 after the enclosure move) and the first Windows machine's 5090 shared 223e0038 with a rented 5090 at the same bus
        let hd = |id: &str, bus: &str, name: &str| HelloDevice { id: id.into(), name: name.into(), memory_bytes: 16 * GIB, arch: "sm_120".into(), arch_detail: None, driver: "580".into(), runtime: "13.0".into(), vendor: Some("nvidia".into()), kind: Some("discrete".into()), bus_id: Some(bus.into()), intrinsic_id: None, identity: None, slowdown_c: None, knobs: vec![] };
        let uuids = parse_uuid_rows("00000000:01:00.0, GPU-aaaa1111-0000-0000-0000-000000005090\n00000000:0D:00.0, GPU-bbbb2222-0000-0000-0000-000000005080\n00000000:11:00.0, GPU-bbbb2222-0000-0000-0000-000000005080\n");
        // the same card under three masks: index 0, 1, 2 and the worker's ordinal id cuda:0, cuda:1, cuda:2
        let a = Device::from_hello_with("cuda", 0, &hd("cuda:0", "0000:0D:00.0", "NVIDIA GeForce RTX 5080"), &uuids);
        let b = Device::from_hello_with("cuda", 1, &hd("cuda:1", "0000:0D:00.0", "NVIDIA GeForce RTX 5080"), &uuids);
        let c = Device::from_hello_with("cuda", 2, &hd("cuda:2", "0000:0d:00.0", "NVIDIA GeForce RTX 5080"), &uuids);
        assert_eq!(a.fingerprint, b.fingerprint);
        assert_eq!(b.fingerprint, c.fingerprint);
        assert_eq!(a.identity, "uuid");
        // two cards swap indices and keep their fingerprints
        let p0 = Device::from_hello_with("cuda", 0, &hd("cuda:0", "0000:01:00.0", "NVIDIA GeForce RTX 5090"), &uuids);
        let p1 = Device::from_hello_with("cuda", 1, &hd("cuda:1", "0000:0D:00.0", "NVIDIA GeForce RTX 5080"), &uuids);
        let q0 = Device::from_hello_with("cuda", 0, &hd("cuda:0", "0000:0D:00.0", "NVIDIA GeForce RTX 5080"), &uuids);
        let q1 = Device::from_hello_with("cuda", 1, &hd("cuda:1", "0000:01:00.0", "NVIDIA GeForce RTX 5090"), &uuids);
        assert_eq!(p0.fingerprint, q1.fingerprint);
        assert_eq!(p1.fingerprint, q0.fingerprint);
        assert_ne!(p0.fingerprint, p1.fingerprint);
        // the card moved to another slot (or host) with the same UUID keeps its fingerprint
        let moved = Device::from_hello_with("cuda", 0, &hd("cuda:0", "0000:11:00.0", "NVIDIA GeForce RTX 5080"), &uuids);
        assert_eq!(moved.fingerprint, a.fingerprint);
        // the bus-id form (no nvidia-smi) changes with the slot and says so; two identical cards at one bus on two
        // hosts collide under it, which the UUID form never does
        let none = std::collections::HashMap::new();
        let s1 = Device::from_hello_with("cuda", 0, &hd("cuda:0", "0000:0D:00.0", "NVIDIA GeForce RTX 5080"), &none);
        let s2 = Device::from_hello_with("cuda", 0, &hd("cuda:0", "0000:11:00.0", "NVIDIA GeForce RTX 5080"), &none);
        assert_ne!(s1.fingerprint, s2.fingerprint);
        assert_eq!(s1.identity, "bus");
        assert_ne!(s1.fingerprint, a.fingerprint);
        // the UUID form from nvidia-smi's uuid alone, as a job form computes it: sha-256 over the five terms
        let mut h = Sha256::new();
        for part in ["cuda", "nvidia", "GPU-bbbb2222-0000-0000-0000-000000005080", "NVIDIA GeForce RTX 5080", "sm_120"] {
            h.update(part.as_bytes());
            h.update([0u8]);
        }
        assert_eq!(a.fingerprint, igneum_worker_proto::hex_encode(&h.finalize()));
        assert_eq!(normalise_bus("00000000:0D:00.0"), "0000:0d:00.0");
        assert_eq!(normalise_bus("0d:00.0"), "0000:0d:00.0");
        // the project's ruling of 11 Oct 2026 21:07 UK: the OpenCL and Metal workers offer the card's own identity in the hello with its
        // word; the supervisor hashes it over the same five terms; the slot is not an input while an intrinsic id is offered
        let ocl = |id: &str, bus: &str, name: &str, arch: &str, word: Option<&str>, term: Option<&str>| HelloDevice { id: id.into(), name: name.into(), memory_bytes: 16 * GIB, arch: arch.into(), arch_detail: None, driver: "25.10".into(), runtime: "AMD Accelerated Parallel Processing".into(), vendor: Some(if arch.starts_with("gfx") { "amd" } else { "intel" }.into()), kind: Some("discrete".into()), bus_id: Some(bus.into()), intrinsic_id: term.map(Into::into), identity: word.map(Into::into), slowdown_c: None, knobs: vec![] };
        let amd_a = Device::from_hello_with("opencl", 0, &ocl("0000:03:00.0", "0000:03:00.0", "AMD Radeon RX 9070 XT", "gfx1201", Some("uuid"), Some("3a1f00000000000000000000000000c1")), &none);
        let amd_b = Device::from_hello_with("opencl", 2, &ocl("0000:0d:00.0", "0000:0d:00.0", "AMD Radeon RX 9070 XT", "gfx1201", Some("uuid"), Some("3a1f00000000000000000000000000c1")), &none);
        assert_eq!(amd_a.fingerprint, amd_b.fingerprint);
        assert_eq!(amd_a.identity, "uuid");
        let mut h = Sha256::new();
        for part in ["opencl", "amd", "3a1f00000000000000000000000000c1", "AMD Radeon RX 9070 XT", "gfx1201"] {
            h.update(part.as_bytes());
            h.update([0u8]);
        }
        assert_eq!(amd_a.fingerprint, igneum_worker_proto::hex_encode(&h.finalize()));
        let rig = Device::from_hello_with("opencl", 0, &ocl("0000:0c:00.0", "0000:0c:00.0", "AMD Radeon RX 6600", "gfx1032", Some("amdgpu-unique-id"), Some("31f4a0c2b0d5e6f7")), &none);
        let rigb = Device::from_hello_with("opencl", 1, &ocl("0000:09:00.0", "0000:09:00.0", "AMD Radeon RX 6600", "gfx1032", Some("amdgpu-unique-id"), Some("31f4a0c2b0d5e6f7")), &none);
        assert_eq!(rig.identity, "amdgpu-unique-id");
        assert_eq!(rig.fingerprint, rigb.fingerprint);
        assert_ne!(rig.fingerprint, amd_a.fingerprint);
        let arc = Device::from_hello_with("opencl", 0, &ocl("Intel(R) OpenCL Graphics/0", "Intel(R) OpenCL Graphics/0", "Intel(R) Arc(TM) B580 Graphics", "Intel(R) Arc(TM) B580 Graphics", Some("uuid"), Some("8680e20b000000000000000000000000")), &none);
        assert_eq!(arc.identity, "uuid");
        // the worker that offers no intrinsic id, or names the bus form itself, is fingerprinted over the slot and says so
        let slot = Device::from_hello_with("opencl", 0, &ocl("0000:0c:00.0", "0000:0c:00.0", "AMD Radeon RX 6600", "gfx1032", None, None), &none);
        let slot2 = Device::from_hello_with("opencl", 0, &ocl("0000:09:00.0", "0000:09:00.0", "AMD Radeon RX 6600", "gfx1032", Some("bus"), Some("0000:09:00.0")), &none);
        assert_eq!(slot.identity, "bus");
        assert_eq!(slot2.identity, "bus");
        assert_ne!(slot.fingerprint, slot2.fingerprint);
        assert_ne!(slot.fingerprint, rig.fingerprint);
        // Apple: the platform serial with the chip name; the registry id (per boot) is not an input
        let mac = |reg: &str, serial: Option<&str>| HelloDevice { id: reg.into(), name: "Apple M6".into(), memory_bytes: 32 * GIB, arch: "apple10".into(), arch_detail: None, driver: "macOS 26.6.2".into(), runtime: "metal".into(), vendor: Some("apple".into()), kind: Some("apple".into()), bus_id: Some(reg.into()), intrinsic_id: serial.map(Into::into), identity: serial.map(|_| "platform-serial".into()), slowdown_c: None, knobs: vec![] };
        let m1 = Device::from_hello_with("metal", 0, &mac("4294968311", Some("C02XK0AAJG5H")), &none);
        let m2 = Device::from_hello_with("metal", 0, &mac("4294969002", Some("C02XK0AAJG5H")), &none);
        assert_eq!(m1.fingerprint, m2.fingerprint);
        assert_eq!(m1.identity, "platform-serial");
        let m3 = Device::from_hello_with("metal", 0, &mac("4294968311", None), &none);
        assert_eq!(m3.identity, "bus");
        assert_ne!(m3.fingerprint, m1.fingerprint);
        // an NVIDIA card keeps nvidia-smi's UUID ahead of anything the worker offers
        let nv = HelloDevice { id: "cuda:0".into(), name: "NVIDIA GeForce RTX 5080".into(), memory_bytes: 16 * GIB, arch: "sm_120".into(), arch_detail: None, driver: "580".into(), runtime: "13.0".into(), vendor: Some("nvidia".into()), kind: Some("discrete".into()), bus_id: Some("0000:0D:00.0".into()), intrinsic_id: Some("worker-said-this".into()), identity: Some("uuid".into()), slowdown_c: None, knobs: vec![] };
        assert_eq!(Device::from_hello_with("cuda", 0, &nv, &uuids).fingerprint, a.fingerprint);
    }

    #[test]
    fn refusal_sentences_carry_the_numbers() {
        let d = dev("nvidia", "discrete", 6 * GIB);
        let s = refusal_sentence(&d, Some(6 * GIB), Some(5 * GIB), Some("fit_cold_bytes"), "no fit");
        assert!(s.contains("needs 6.0 GB free"));
        assert!(s.contains("nothing was changed"));
        let s2 = refusal_sentence(&d, None, None, Some("fit_cold_bytes"), "no fit");
        assert!(s2.contains("fit_cold_bytes does not fit"));
        // the founder's M5 Max (15 October 2026): the worker's binding term rides the sentence; the old words without one
        let a = dev("apple", "integrated", 68 * GIB);
        let s3 = refusal_sentence(&a, Some(23 * GIB / 10), Some(0), None, "bound by the device's working set less what this process holds (working set 48.0 GB, ours 48.0 GB).");
        assert!(s3.contains("the worker's budget for it is 0.0 GB now (bound by the device's working set less what this process holds (working set 48.0 GB, ours 48.0 GB))"), "{s3}");
        let s4 = refusal_sentence(&a, Some(23 * GIB / 10), Some(0), None, "");
        assert!(s4.contains("(memory less the system's headroom and what other apps hold)"));
    }
}

/// A measured power-cap lever for a card family (the cohort's row), offered on the device page with its figure
/// and never applied by the app itself (PA-01; section 10's rule: no power or clock change without the user's
/// own setting in the driver's software). The table grows a row per cohort measurement that lands.
pub struct PowerOffer {
    /// A lowercase substring of the device name that names the family.
    pub family: &'static str,
    pub vendor: &'static str,
    /// The cap as a percentage of the stock limit.
    pub cap_percent: u32,
    /// What the cohort measured at that cap.
    pub finding: &'static str,
    /// Where the row is.
    pub source: &'static str,
}

pub const POWER_OFFERS: &[PowerOffer] = &[
    PowerOffer { family: "9070 xt", vendor: "amd", cap_percent: 70, finding: "21.7 percent less energy per accepted hash with the rate within 0.3 percent (IgneumHash version 1, 2 GiB, the founder's rows of 10 October 2026)", source: "docs/gpu-b/PHASE-ZERO-FINDINGS.md" },
];

/// The offer for a device, as a sentence the device page shows; None when the cohort has no row for the family.
pub fn power_offer(dev: &Device) -> Option<String> {
    power_offer_for(&dev.name, &dev.vendor)
}

/// The same by name and vendor.
pub fn power_offer_for(name: &str, vendor: &str) -> Option<String> {
    let name = name.to_lowercase();
    let vendor = vendor.to_lowercase();
    POWER_OFFERS.iter().find(|o| vendor.contains(o.vendor) && name.contains(o.family)).map(|o| {
        format!("A {} percent power cap on this card measured {}. Ember never sets a power limit; set it in your driver's software if you want it, and the earnings line follows your reading. Row: {}.", o.cap_percent, o.finding, o.source)
    })
}
