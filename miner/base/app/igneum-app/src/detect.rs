//! GPU detection with the real names. macOS: the Metal worker's hello (the protocol v0 device list: the name Metal
//! reports, its memory) plus the core count from system_profiler. Windows: nvidia-smi for NVIDIA cards, the OpenCL worker's --list for the rest
//! (AMD, Intel), the Windows adapter list (Win32_VideoController: status, problem code, memory) for the cards no
//! worker can drive and for the integrated-or-discrete call, and that list's names as a last resort when neither
//! tool runs. The engine runs this at start and again every minute (src/hotplug.rs compares the two lists).

use crate::state::CardState;
use std::io::Write;
use std::path::Path;
use std::process::{Command, Stdio};
use std::time::{Duration, Instant};

#[derive(Clone)]
pub struct Bins {
    pub node: std::path::PathBuf,
    pub miner: std::path::PathBuf,
    pub metal: Option<std::path::PathBuf>,
    pub cuda: Option<std::path::PathBuf>,
    pub opencl: Option<std::path::PathBuf>,
    /// igneum-gpu-telemetry: AMD power, heat, fans and clocks (proto-opencl/gpu-telemetry.c), 5 October 2026
    pub telemetry: Option<std::path::PathBuf>,
    pub dir: std::path::PathBuf,
}

/// One enumeration: the cards, the notes for the setup screen, and which tools answered. A tool that did not answer
/// (nvidia-smi timed out, the OpenCL worker crashed) says nothing about its cards: the engine keeps them rather
/// than calling them removed (src/hotplug.rs).
#[derive(Clone, Default)]
pub struct Detection {
    pub cards: Vec<CardState>,
    pub notes: Vec<String>,
    /// duplicate OpenCL platform entries left out (one line each, for the log)
    pub dropped: Vec<String>,
    pub nvidia_listed: bool,
    pub opencl_listed: bool,
    pub adapters_listed: bool,
    pub metal_listed: bool,
}

impl Detection {
    /// Whether this enumeration can say that `c` is gone: the tool that lists its vendor answered.
    pub fn listed(&self, c: &CardState) -> bool {
        if !c.problem.is_empty() {
            return self.adapters_listed;
        }
        match c.vendor.as_str() {
            "apple" => self.metal_listed,
            "nvidia" => self.nvidia_listed,
            _ => self.opencl_listed || (c.device.is_empty() && self.adapters_listed),
        }
    }
}

/// The hint on a card the OS reports as faulty (Windows Code 43, 12, 31 and friends).
pub const PROBLEM_HINT: &str = "reboot with the card attached; if it persists, reinstall the driver with the card attached";

/// Runs a command with a time limit; returns stdout (and stderr appended) or None.
pub fn run_timeout(cmd: &mut Command, stdin_text: Option<&str>, limit: Duration) -> Option<String> {
    cmd.stdout(Stdio::piped()).stderr(Stdio::piped());
    cmd.stdin(if stdin_text.is_some() { Stdio::piped() } else { Stdio::null() });
    crate::platform::quiet(cmd);
    let mut child = cmd.spawn().ok()?;
    if let (Some(text), Some(mut stdin)) = (stdin_text, child.stdin.take()) {
        let _ = stdin.write_all(text.as_bytes());
        drop(stdin);
    }
    let out = child.stdout.take()?;
    let err = child.stderr.take()?;
    let reader = std::thread::spawn(move || {
        let mut s = String::new();
        let _ = std::io::Read::read_to_string(&mut std::io::BufReader::new(out), &mut s);
        let mut e = String::new();
        let _ = std::io::Read::read_to_string(&mut std::io::BufReader::new(err), &mut e);
        (s, e)
    });
    let deadline = Instant::now() + limit;
    loop {
        match child.try_wait() {
            Ok(Some(_)) => break,
            Ok(None) if Instant::now() < deadline => std::thread::sleep(Duration::from_millis(50)),
            _ => {
                let _ = child.kill();
                let _ = child.wait();
                break;
            }
        }
    }
    let (s, e) = reader.join().ok()?;
    Some(if e.is_empty() { s } else { format!("{s}\n{e}") })
}

fn card(index: usize, name: &str, vendor: &str, worker: &str, detail: &str, device: &str) -> CardState {
    CardState {
        index,
        key: format!("{vendor}:{name}"),
        code: name.to_string(),
        name: name.to_string(),
        vendor: vendor.into(),
        worker: worker.into(),
        detail: detail.into(),
        device: device.into(),
        enabled: true,
        state: "off".into(),
        amd_ordinal: -1,
        amd_stock_mhz: 0,
        amd_gmax_offset: false,
        ..Default::default()
    }
}

/// Integrated GPUs by name: AMD APUs ("Radeon Graphics", "Vega 8"), Intel iGPUs (Iris, UHD, HD Graphics, Arc A3xx is
/// discrete), and the gfx codes AMD's OpenCL runtime reports instead of a marketing name (the worker's --list prints
/// CL_DEVICE_NAME: the first Windows machine's Ryzen iGPU is "gfx1036", 5 October 2026).
pub fn looks_integrated(name: &str) -> bool {
    let n = name.to_ascii_lowercase();
    if n.contains("arc") && n.contains("intel") {
        return arc_is_integrated(&n);
    }
    let integrated = ["radeon(tm) graphics", "radeon graphics", "vega 8", "vega 7", "vega 6", "vega 3", "vega 11", "iris", "uhd graphics", "hd graphics", "intel(r) graphics", "intel graphics", "apu", "780m", "760m", "680m", "610m", "890m", "880m"];
    if integrated.iter().any(|k| n.contains(k)) {
        return true;
    }
    // AMD APU graphics by gfx code (approximate list from AMD's ROCm and Mesa target tables): Raven/Picasso gfx902 and
    // gfx909, Renoir/Cezanne/Lucienne gfx90c, Van Gogh gfx1033, Rembrandt gfx1035, Raphael/Granite Ridge gfx1036,
    // Mendocino gfx1037, Phoenix gfx1103, Strix gfx1150 to gfx1152. Discrete codes (gfx1030 and so on) are not here.
    let apu = ["gfx902", "gfx909", "gfx90c", "gfx1033", "gfx1035", "gfx1036", "gfx1037", "gfx1103", "gfx1150", "gfx1151", "gfx1152"];
    let code = n.trim();
    apu.iter().any(|k| code == *k || code.starts_with(&format!("{k}:")) || code.starts_with(&format!("{k} ")))
}

/// An Intel name carrying "Arc" (lower-cased): the cards ("Arc(TM) A770", "Arc(TM) B580", "Arc(TM) Pro A60") are
/// discrete; the Arc-branded processor graphics are integrated: the bare "Intel(R) Arc(TM) Graphics" of Meteor Lake
/// and Arrow Lake, the "Arc(TM) 130V / 140V" of Lunar Lake and the "Arc(TM) 1xxT" of Panther Lake (approximate names
/// from Intel's product pages, 7 October 2026; the B580 is the first Intel card in hand, docs/plans/intel-arc.md).
fn arc_is_integrated(lower: &str) -> bool {
    let after = lower.split("arc").nth(1).unwrap_or("").trim_start_matches("(tm)").trim_start_matches("(r)").trim();
    let word = after.split(|c: char| !c.is_ascii_alphanumeric()).next().unwrap_or("");
    // "graphics" or nothing after "Arc": the processor graphics; "130v", "140t": digits first, the processor graphics
    if word.is_empty() || word == "graphics" || word.starts_with(|c: char| c.is_ascii_digit()) {
        return true;
    }
    // "pro", "a770", "b580": a card. Anything else unknown is read as a card (on by default; a wrong call costs an
    // iGPU 8 identities at 1 to 2 MH/s, a card called integrated would sit off with no reason the owner can see)
    false
}

/// One row of Windows' adapter list (Win32_VideoController), the part this app reads.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct Adapter {
    pub name: String,
    /// "OK", "Error", "Degraded", ... (the Status property)
    pub status: String,
    /// the PnP problem code (ConfigManagerErrorCode): 0 = fine, 43 = the driver stopped it, 12 = no resources, 31 = not loaded
    pub code: u32,
    /// AdapterRAM in MB; 0 = unknown (a faulty card reports 0, and the property caps at 4 GB on 32-bit values)
    pub ram_mb: u64,
    pub processor: String,
    pub pnp_id: String,
    /// "01:00.0" from DEVPKEY_Device_BusNumber and DEVPKEY_Device_Address; empty when PowerShell could not read them
    pub bus: String,
    /// the DriverVersion property ("32.0.101.9034"); empty when none is bound (driver-check, 7 October 2026)
    pub driver: String,
    /// the device sits behind a USB4 or Thunderbolt router in its parent chain (an eGPU enclosure; the second Windows machine's RTX 5060 Ti in a
    /// Razer Core X V2, 7 October 2026): the kind reads "external" and the Cards page says eGPU
    pub external: bool,
}

impl Adapter {
    /// The PCI device id from the PnP id ("PCI\\VEN_1002&DEV_7550&..." gives 0x7550); 0 when there is none.
    pub fn device_id(&self) -> u16 {
        let up = self.pnp_id.to_ascii_uppercase();
        up.find("DEV_").and_then(|i| u16::from_str_radix(up.get(i + 4..i + 8)?, 16).ok()).unwrap_or(0)
    }
    /// "Code 43" for a problem code, "status Error" for a bad status without one, None when the device is fine.
    pub fn problem(&self) -> Option<String> {
        if self.code != 0 {
            return Some(format!("Code {}", self.code));
        }
        let st = self.status.trim();
        if !st.is_empty() && !st.eq_ignore_ascii_case("ok") {
            return Some(format!("status {st}"));
        }
        None
    }
}

/// Integrated or discrete, from the name (APU and iGPU names, AMD gfx codes) and, when Windows' adapter row is
/// known, its processor string or a dedicated memory under 1 GB (a shared-memory iGPU; 0 = unknown, says nothing).
pub fn classify_kind(name: &str, adapter: Option<&Adapter>) -> &'static str {
    if looks_integrated(name) {
        return "integrated";
    }
    if let Some(a) = adapter {
        // the processor string names Intel iGPUs ("Intel(R) Iris(R) Xe Graphics Family"); AMD's reads "AMD Radeon
        // Graphics Processor (0x7550)" for discrete cards too, so only the Intel markers count here
        let proc_ = a.processor.to_ascii_lowercase();
        if looks_integrated(&a.name) || (["iris", "uhd graphics", "hd graphics"].iter().any(|k| proc_.contains(k)) && !proc_.contains("arc")) {
            return "integrated";
        }
        if a.ram_mb > 0 && a.ram_mb < 1024 {
            return "integrated";
        }
        if a.external {
            return "external";
        }
    }
    "discrete"
}

pub fn vendor_of(name: &str) -> &'static str {
    let n = name.to_ascii_lowercase();
    if n.contains("nvidia") || n.contains("geforce") {
        "nvidia"
    } else if n.contains("amd") || n.contains("radeon") || n.starts_with("gfx") {
        "amd"
    } else if n.contains("apple") {
        "apple"
    } else if n.contains("intel") {
        // Arc cards and Intel processor graphics alike (the kind tells them apart); the first Intel card is the B580
        // on the first Windows machine, 7 October 2026 (docs/plans/intel-arc.md)
        "intel"
    } else {
        "other"
    }
}

/// Parses `Get-CimInstance Win32_VideoController | Select-Object ... | ConvertTo-Json` (one object or an array).
pub fn parse_adapters(json: &str) -> Vec<Adapter> {
    let Ok(v) = serde_json::from_str::<serde_json::Value>(json.trim()) else { return Vec::new() };
    let rows: Vec<serde_json::Value> = match v {
        serde_json::Value::Array(a) => a,
        o @ serde_json::Value::Object(_) => vec![o],
        _ => Vec::new(),
    };
    let s = |r: &serde_json::Value, k: &str| r.get(k).and_then(|x| x.as_str()).unwrap_or("").trim().to_string();
    let n = |r: &serde_json::Value, k: &str| r.get(k).and_then(|x| x.as_u64().or_else(|| x.as_str().and_then(|t| t.trim().parse::<u64>().ok()))).unwrap_or(0);
    rows.iter()
        .map(|r| {
            // DEVPKEY_Device_Address on PCI is (device << 16) | function
            let bus = match (r.get("BusNumber").and_then(|x| x.as_u64()), r.get("Address").and_then(|x| x.as_u64())) {
                (Some(b), Some(a)) => format!("{:02x}:{:02x}.{:x}", b & 0xff, (a >> 16) & 0xff, a & 0xffff),
                _ => String::new(),
            };
            let external = r.get("External").and_then(|x| x.as_bool()).unwrap_or(false);
            Adapter { name: s(r, "Name"), status: s(r, "Status"), code: n(r, "ConfigManagerErrorCode") as u32, ram_mb: n(r, "AdapterRAM") / (1024 * 1024), processor: s(r, "VideoProcessor"), pnp_id: s(r, "PNPDeviceID"), driver: s(r, "DriverVersion"), bus, external }
        })
        .filter(|a| !a.name.is_empty())
        .collect()
}

/// The PowerShell behind adapters(): one object per adapter with the PCI bus and address and whether a USB4 or Thunderbolt
/// router sits in the device's parent chain (External), which is how an eGPU enclosure shows (the second Windows machine's Razer Core X V2 reads
/// "USB4 Router (2.0), Razer - Core X V2", instance USB4\VID_8087&PID_5786...).
pub const ADAPTERS_SCRIPT: &str = "$v = Get-CimInstance Win32_VideoController | ForEach-Object { $id = $_.PNPDeviceID; $bus = $null; $addr = $null; $ext = $false; try { foreach ($x in (Get-PnpDeviceProperty -InstanceId $id -KeyName 'DEVPKEY_Device_BusNumber','DEVPKEY_Device_Address' -ErrorAction Stop)) { if ($x.KeyName -eq 'DEVPKEY_Device_BusNumber') { $bus = $x.Data } elseif ($x.KeyName -eq 'DEVPKEY_Device_Address') { $addr = $x.Data } } } catch {}; try { $cur = $id; for ($i = 0; $i -lt 6 -and $cur; $i++) { $par = (Get-PnpDeviceProperty -InstanceId $cur -KeyName 'DEVPKEY_Device_Parent' -ErrorAction Stop).Data; if (-not $par) { break }; if (\"$par\" -match '^USB4\\\\|THUNDERBOLT|TBT') { $ext = $true; break }; $cur = $par } } catch {}; [pscustomobject]@{ Name = $_.Name; Status = $_.Status; ConfigManagerErrorCode = $_.ConfigManagerErrorCode; AdapterRAM = $_.AdapterRAM; VideoProcessor = $_.VideoProcessor; DriverVersion = $_.DriverVersion; PNPDeviceID = $id; BusNumber = $bus; Address = $addr; External = $ext } }; ConvertTo-Json -InputObject @($v) -Compress";

/// Windows' adapter list through PowerShell (about a second); None when PowerShell did not answer.
#[cfg(windows)]
pub fn adapters() -> Option<Vec<Adapter>> {
    // one object per adapter, with the PCI bus number and address from the PnP properties (they name the card
    // the OpenCL worker's "pci" field names); @() keeps a single adapter an array
    // External: the parent chain (DEVPKEY_Device_Parent, up to 6 hops) holds a USB4 router or a Thunderbolt device
    let script = ADAPTERS_SCRIPT;
    let out = run_timeout(Command::new(crate::platform::tool("powershell")).args(["-NoProfile", "-Command", script]), None, Duration::from_secs(15))?;
    let start = out.find(|c| c == '[' || c == '{')?;
    Some(parse_adapters(&out[start..]))
}

#[cfg(not(windows))]
#[allow(dead_code)]
pub fn adapters() -> Option<Vec<Adapter>> {
    None
}

/// Windows' row for a detected card, by name (nvidia-smi and Windows agree on NVIDIA names; AMD's OpenCL runtime
/// reports gfx codes, which match nothing here and fall back to the name rules).
pub fn adapter_for<'a>(name: &str, adapters: &'a [Adapter]) -> Option<&'a Adapter> {
    let n = name.trim().to_ascii_lowercase();
    adapters.iter().find(|a| a.name.trim().to_ascii_lowercase() == n)
}

/// The row's words for an integrated GPU that is off by default (the switch turns it on; the choice is kept).
pub const INTEGRATED_REASON: &str = "integrated GPU, off by default (2 to 3 MH/s for 30 W)";

/// The defaults the launchers use: discrete cards on (8 identities on a big card, 2 on a small one), integrated off
/// (1 identity), Apple silicon on with 1.
pub fn apply_defaults(c: &mut CardState) {
    match c.kind.as_str() {
        "apple" => {
            c.enabled = true;
            c.identities = 1;
        }
        "integrated" => {
            c.enabled = false;
            c.identities = 1;
            c.reason = INTEGRATED_REASON.into();
        }
        _ => {
            c.enabled = true;
            c.identities = if c.vram_mb >= 8192 { 8 } else { 2 };
        }
    }
    if c.vendor == "nvidia" && c.power_pct == 0 {
        c.power_pct = 80; // the default cap: 80% of the card's default limit (the second Windows machine's 5090 hard-crashed at full power)
    }
}

/// Marks whether the efficiency sweep (src/sweep.rs) can run on a card, with the reason when it cannot. Called
/// once the power limits are known.
pub fn mark_sweep_support(c: &mut CardState) {
    match crate::sweep::unsupported_reason(&c.vendor, c.power_default_w, &c.device) {
        None => {
            c.sweep_supported = true;
            if c.sweep_state.is_empty() || c.sweep_state == "unsupported" {
                c.sweep_state = "idle".into();
                c.sweep_note = String::new();
            }
        }
        Some(why) => {
            c.sweep_supported = false;
            c.sweep_state = "unsupported".into();
            c.sweep_note = why.into();
        }
    }
}

/// `nvidia-smi --query-gpu=index,memory.used`: index -> MiB used now (review B F07: the device coordinator confirms a
/// card's memory is free after a holder's process exits; pausing dispatch is not releasing memory).
#[cfg(not(target_os = "macos"))]
pub fn nvidia_memory_used() -> std::collections::HashMap<String, u64> {
    run_timeout(Command::new(crate::platform::tool("nvidia-smi")).args(["--query-gpu=index,memory.used", "--format=csv,noheader,nounits"]), None, Duration::from_secs(10))
        .map(|t| parse_memory_used(&t))
        .unwrap_or_default()
}
#[cfg(target_os = "macos")]
pub fn nvidia_memory_used() -> std::collections::HashMap<String, u64> {
    std::collections::HashMap::new()
}
/// "0, 6129\n1, 2" -> {"0": 6129, "1": 2}
pub fn parse_memory_used(text: &str) -> std::collections::HashMap<String, u64> {
    let mut out = std::collections::HashMap::new();
    for line in text.lines() {
        let mut it = line.split(',').map(|x| x.trim());
        if let (Some(i), Some(m)) = (it.next(), it.next()) {
            if let Ok(mib) = m.parse::<u64>() {
                out.insert(i.to_string(), mib);
            }
        }
    }
    out
}

/// `nvidia-smi --query-gpu=index,power.draw`: index -> watts now (the one-shot form; the telemetry child streams it).
#[cfg(not(target_os = "macos"))]
#[allow(dead_code)]
pub fn nvidia_power_draw() -> std::collections::HashMap<String, f64> {
    run_timeout(Command::new(crate::platform::tool("nvidia-smi")).args(["--query-gpu=index,power.draw", "--format=csv,noheader,nounits"]), None, Duration::from_secs(10))
        .map(|t| crate::sweep::parse_power_draw(&t))
        .unwrap_or_default()
}

/// NVIDIA power limits per card index: (default, current, min, max) in watts.
#[cfg(target_os = "macos")]
pub fn nvidia_power_limits() -> std::collections::HashMap<String, (f64, f64, f64, f64)> {
    std::collections::HashMap::new()
}

#[cfg(not(target_os = "macos"))]
pub fn nvidia_power_limits() -> std::collections::HashMap<String, (f64, f64, f64, f64)> {
    let mut out = std::collections::HashMap::new();
    let Some(text) = run_timeout(Command::new(crate::platform::tool("nvidia-smi")).args(["--query-gpu=index,power.default_limit,power.limit,power.min_limit,power.max_limit", "--format=csv,noheader,nounits"]), None, Duration::from_secs(10)) else { return out };
    for line in text.lines() {
        let p: Vec<&str> = line.split(',').map(|s| s.trim()).collect();
        if p.len() >= 5 {
            let f = |s: &str| s.parse::<f64>().unwrap_or(0.0);
            out.insert(p[0].to_string(), (f(p[1]), f(p[2]), f(p[3]), f(p[4])));
        }
    }
    out
}

#[cfg(target_os = "macos")]
pub fn detect(bins: &Bins) -> Detection {
    let mut d = Detection::default();
    let Some(metal) = bins.metal.as_ref() else {
        d.notes.push("the Metal worker (igneum-worker-metal) is missing from the app".into());
        return d;
    };
    // the worker's hello (protocol v0): {"t":"hello","backend":"metal","devices":[{"name":"Apple M6","vendor":"apple",
    // "memory_bytes":"17179869184",...}],...}; the probe then sends the protocol's own closing word (shutdown) so the
    // worker ends by its exit path. It used to send a hello line, which the worker read as "first line must be
    // configure" and ended by the faulting path that aborted (a joinable thread's destructor: std::terminate,
    // a crash report on the user's Mac at every re-detect; 32 on the mini by 12 October 2026, the GPU team's reading)
    let out = run_timeout(Command::new(metal).args(["--algo", "igneumhash", "protocol"]), Some("{\"seq\":1,\"t\":\"shutdown\"}\n"), Duration::from_secs(20)).unwrap_or_default();
    let hello = out.lines().filter_map(|l| serde_json::from_str::<serde_json::Value>(l).ok()).find(|v| v.get("t").and_then(|t| t.as_str()) == Some("hello"));
    let Some(hello) = hello else {
        d.notes.push(format!("the Metal worker did not answer hello: {}", out.lines().last().unwrap_or("no output")));
        return d;
    };
    let device = hello.get("devices").and_then(|a| a.as_array()).and_then(|a| a.first()).cloned().unwrap_or_default();
    let name = device.get("name").and_then(|n| n.as_str()).filter(|n| !n.is_empty()).map(|n| n.to_string()).unwrap_or_else(|| "Apple GPU".into());
    // memory_bytes is a string in the hello (a number fits too)
    let hello_mem = device.get("memory_bytes").and_then(|m| m.as_str().and_then(|s| s.parse::<u64>().ok()).or_else(|| m.as_u64()));
    d.metal_listed = true;
    // cores and memory from system_profiler (a second or two); optional
    let mut detail = String::new();
    if let Some(sp) = run_timeout(Command::new(crate::platform::tool("system_profiler")).args(["SPDisplaysDataType", "-json"]), None, Duration::from_secs(8)) {
        if let Ok(v) = serde_json::from_str::<serde_json::Value>(&sp) {
            if let Some(g) = v.get("SPDisplaysDataType").and_then(|a| a.as_array()).and_then(|a| a.first()) {
                if let Some(c) = g.get("sppci_cores").and_then(|c| c.as_str()) {
                    detail = format!("{c} GPU cores");
                }
            }
        }
    }
    let mem = hello_mem.or_else(|| run_timeout(Command::new(crate::platform::tool("sysctl")).args(["-n", "hw.memsize"]), None, Duration::from_secs(3)).and_then(|m| m.trim().parse::<u64>().ok()));
    if let Some(b) = mem {
        let gb = b / (1024 * 1024 * 1024);
        detail = if detail.is_empty() { format!("{gb} GB unified memory") } else { format!("{detail}, {gb} GB unified memory") };
    }
    // the device is the Metal worker's one device index, "0", never empty: an empty device read every re-detect
    // as the card having moved (the mini, 12 October 2026: "device 0 is now" every five minutes)
    let mut c = card(0, &name, "apple", "Metal", &detail, "0");
    c.kind = "apple".into();
    c.path = "prebuilt".into();
    c.vram_mb = mem.map(|b| b / (1024 * 1024)).unwrap_or(0);
    apply_defaults(&mut c);
    mark_sweep_support(&mut c);
    d.cards.push(c);
    assign_keys(&mut d.cards);
    d
}

/// AMD gfx codes the OpenCL runtime reports as the device name, with the card names Windows uses, the words for
/// the row when no adapter matches, and the PCI device ids (approximate, from AMD's public ROCm and Linux driver
/// tables; add a line when a card is seen). gfx1036 is the Ryzen desktop iGPU (the first Windows machine: DEV_13C0, 5 October 2026).
const GFX: &[(&str, &str, &[&str], &[u16])] = &[
    ("gfx1201", "Radeon RX 9070 XT / 9070", &["Radeon RX 9070 XT", "Radeon RX 9070"], &[0x7550]),
    ("gfx1200", "Radeon RX 9060 XT", &["Radeon RX 9060 XT", "Radeon RX 9060"], &[0x7590]),
    ("gfx1100", "Radeon RX 7900 XTX / XT", &["Radeon RX 7900 XTX", "Radeon RX 7900 XT", "Radeon RX 7900 GRE"], &[0x744C]),
    ("gfx1101", "Radeon RX 7800 XT / 7700 XT", &["Radeon RX 7800 XT", "Radeon RX 7700 XT"], &[0x747E]),
    ("gfx1102", "Radeon RX 7600", &["Radeon RX 7600 XT", "Radeon RX 7600"], &[0x7480]),
    ("gfx1030", "Radeon RX 6800 / 6900", &["Radeon RX 6900 XT", "Radeon RX 6950 XT", "Radeon RX 6800 XT", "Radeon RX 6800"], &[0x73BF]),
    ("gfx1031", "Radeon RX 6700 XT", &["Radeon RX 6750 XT", "Radeon RX 6700 XT", "Radeon RX 6700"], &[0x73DF]),
    ("gfx1032", "Radeon RX 6600", &["Radeon RX 6650 XT", "Radeon RX 6600 XT", "Radeon RX 6600"], &[0x73FF]),
    ("gfx1036", "Ryzen integrated Radeon Graphics", &["Radeon(TM) Graphics", "Radeon Graphics"], &[0x164E, 0x13C0]),
    ("gfx1035", "Radeon 680M (integrated)", &["Radeon 680M", "Radeon 660M"], &[0x1681]),
    ("gfx1103", "Radeon 780M (integrated)", &["Radeon 780M", "Radeon 760M"], &[0x15BF, 0x15C8]),
    ("gfx1150", "Radeon 890M (integrated)", &["Radeon 890M", "Radeon 880M"], &[0x150E]),
    ("gfx90c", "Radeon Graphics (Renoir / Cezanne, integrated)", &["Radeon(TM) Graphics", "Radeon Graphics"], &[0x1636, 0x1638]),
];

fn gfx_entry(code: &str) -> Option<&'static (&'static str, &'static str, &'static [&'static str], &'static [u16])> {
    let c = code.trim().to_ascii_lowercase();
    let c = c.split(|ch: char| ch == ':' || ch == ' ').next().unwrap_or("");
    GFX.iter().find(|e| e.0 == c)
}

/// The name on the row for a device the tool knows by `code`: Windows' adapter name when one matches (by PCI bus,
/// else by the gfx code's device ids, else by the card names in the table), else the table's words, else the code.
/// `used` holds the adapters already given to another card, so two gfx1036 entries never share one.
pub fn resolve_name(code: &str, vendor: &str, bus: &str, adapters: &[Adapter], used: &mut Vec<usize>) -> (String, Option<usize>) {
    let free = |i: &usize| !used.contains(i);
    let fine = |a: &Adapter| a.problem().is_none();
    let vendor_ok = |a: &Adapter| vendor == "other" || vendor_of(&a.name) == vendor;
    if !bus.is_empty() {
        if let Some(i) = (0..adapters.len()).filter(free).find(|&i| adapters[i].bus == bus && vendor_ok(&adapters[i])) {
            used.push(i);
            return (adapters[i].name.clone(), Some(i));
        }
    }
    // the name is already a marketing name (nvidia-smi, Windows): the adapter with the same name
    let same: Vec<usize> = (0..adapters.len()).filter(free).filter(|&i| adapters[i].name.trim().eq_ignore_ascii_case(code.trim())).collect();
    if same.len() == 1 {
        used.push(same[0]);
        return (adapters[same[0]].name.clone(), Some(same[0]));
    }
    let Some(entry) = gfx_entry(code) else { return (code.to_string(), None) };
    let by_id: Vec<usize> = (0..adapters.len()).filter(free).filter(|&i| fine(&adapters[i]) && entry.3.contains(&adapters[i].device_id())).collect();
    if by_id.len() == 1 {
        used.push(by_id[0]);
        return (adapters[by_id[0]].name.clone(), Some(by_id[0]));
    }
    let by_name: Vec<usize> = (0..adapters.len()).filter(free).filter(|&i| fine(&adapters[i]) && vendor_ok(&adapters[i]) && { let n = adapters[i].name.to_ascii_lowercase(); entry.2.iter().any(|m| n.contains(&m.to_ascii_lowercase())) }).collect();
    if by_name.len() == 1 {
        used.push(by_name[0]);
        return (adapters[by_name[0]].name.clone(), Some(by_name[0]));
    }
    (entry.1.to_string(), None)
}

/// One device line pair of the OpenCL worker's --list.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct ClDevice {
    pub index: String,
    pub name: String,
    pub platform: String,
    pub platform_version: String,
    pub is_gpu: bool,
    pub vendor: String,
    pub driver: String,
    pub units: String,
    /// "01:00.0" when the worker printed `pci` (workers from 5 October 2026 on), else empty
    pub bus: String,
    pub mem_mb: u64,
}

impl ClDevice {
    pub fn vendor_word(&self) -> &'static str {
        if self.vendor.contains("NVIDIA") || self.name.contains("NVIDIA") {
            "nvidia"
        } else if self.vendor.contains("Advanced Micro") || self.vendor.contains("AMD") || self.name.contains("Radeon") || self.name.contains("AMD") || self.name.to_ascii_lowercase().starts_with("gfx") {
            "amd"
        } else if self.vendor.contains("Intel") || self.name.contains("Intel") {
            // "Intel(R) Corporation" on the "Intel(R) OpenCL Graphics" platform (the Arc driver's runtime)
            "intel"
        } else {
            "other"
        }
    }
    fn platform_key(&self) -> String {
        format!("{} ({}) driver {}", self.platform, self.platform_version, self.driver)
    }
}

/// Parses `igneum-worker-opencl --list`: `[idx] name  |  platform (version)` then `GPU, vendor V, driver D, OpenCL C
/// x.y, N compute units, M MHz[, pci bb:dd.f]` then `global N MiB, ...`. The bool says the worker printed its header.
/// F-E36 (14 October 2026, the GPU team's worker 660ea2d6): the OpenCL worker's `devices` verb prints the hello object as
/// one JSON line on stdout, its `devices` array carrying per device `index` (the loader's order, the same N as
/// `--device`), `bus_id` ("0000:63:00.0"), `name`, `vendor` ("intel"), `driver`, `memory_bytes`, `runtime`; the
/// `--list` text went to stderr in a form of its own, so the app's card list never saw the Arc. The JSON is read
/// first and `--list`'s text stays as the older workers' fallback.
pub fn parse_opencl_devices_json(text: &str) -> Vec<ClDevice> {
    let mut out = Vec::new();
    for line in text.lines() {
        let t = line.trim();
        if !t.starts_with('{') {
            continue;
        }
        let Ok(v) = serde_json::from_str::<serde_json::Value>(t) else { continue };
        let Some(devs) = v.get("devices").and_then(|d| d.as_array()) else { continue };
        let runtime = v.get("runtime").and_then(|r| r.as_str()).unwrap_or("").to_string();
        for (n, d) in devs.iter().enumerate() {
            let g = |k: &str| d.get(k).and_then(|x| x.as_str()).unwrap_or("").to_string();
            let index = d.get("index").and_then(|x| x.as_u64()).map(|x| x.to_string()).unwrap_or_else(|| n.to_string());
            let bus_full = g("bus_id");
            // "0000:63:00.0" (the worker's domain form) to the app's "63:00.0" (nvidia-smi's form), the domain dropped
            let bus = { let parts: Vec<&str> = bus_full.split(':').collect(); if parts.len() >= 3 { parts[parts.len() - 2..].join(":") } else { bus_full.clone() } };
            let mem_mb = d.get("memory_bytes").and_then(|x| x.as_u64()).unwrap_or(0) / (1024 * 1024);
            let dev_runtime = { let r = g("runtime"); if r.is_empty() { runtime.clone() } else { r } };
            out.push(ClDevice {
                index,
                name: g("name"),
                platform: dev_runtime,
                platform_version: String::new(),
                is_gpu: true,
                vendor: g("vendor"),
                driver: g("driver"),
                units: String::new(),
                bus,
                mem_mb,
            });
        }
        if !out.is_empty() {
            break;
        }
    }
    out
}

pub fn parse_opencl_list(text: &str) -> (Vec<ClDevice>, bool) {
    let lines: Vec<&str> = text.lines().collect();
    let listed = lines.iter().any(|l| l.starts_with("OpenCL devices"));
    let mut out = Vec::new();
    for (i, line) in lines.iter().enumerate() {
        let t = line.trim_start_matches(|c| c == ' ' || c == '*').trim();
        if !t.starts_with('[') {
            continue;
        }
        let Some(close) = t.find(']') else { continue };
        let rest = &t[close + 1..];
        let mut halves = rest.splitn(2, "  |");
        let name = halves.next().unwrap_or("").trim().to_string();
        let plat = halves.next().unwrap_or("").trim();
        // the first " (" opens the version: AMD's version string carries brackets of its own, "OpenCL 2.1 AMD-APP (3617.0)"
        let (platform, platform_version) = match plat.find(" (") {
            Some(p) if plat.ends_with(')') => (plat[..p].to_string(), plat[p + 2..plat.len() - 1].to_string()),
            _ => (plat.to_string(), String::new()),
        };
        let info = lines.get(i + 1).map(|l| l.trim()).unwrap_or("");
        let parts: Vec<&str> = info.split(", ").collect();
        let mem_mb = lines.get(i + 2).map(|l| l.trim()).and_then(|l| l.strip_prefix("global ")).and_then(|l| l.split_whitespace().next()).and_then(|n| n.parse::<u64>().ok()).unwrap_or(0);
        out.push(ClDevice {
            index: t[1..close].to_string(),
            name,
            platform,
            platform_version,
            is_gpu: info.starts_with("GPU"),
            vendor: parts.iter().find_map(|p| p.strip_prefix("vendor ")).unwrap_or("").trim().to_string(),
            driver: parts.iter().find_map(|p| p.strip_prefix("driver ")).unwrap_or("").trim().to_string(),
            units: parts.iter().find(|p| p.contains("compute units")).unwrap_or(&"").to_string(),
            bus: parts.iter().find_map(|p| p.strip_prefix("pci ")).unwrap_or("").trim().to_string(),
            mem_mb,
        });
    }
    (out, listed)
}

fn version_tuple(s: &str) -> Vec<u64> {
    s.split(|c: char| !c.is_ascii_digit()).filter(|p| !p.is_empty()).map(|p| p.parse::<u64>().unwrap_or(0)).collect()
}

/// One entry per physical card across OpenCL platforms. Two AMD ICDs after a driver upgrade each list every AMD
/// card (the first Windows machine, 5 October 2026: gfx1036 and gfx1201 twice, two workers on one 9070 XT). Per vendor, the fuller
/// platform wins (most GPUs, then the newest driver, then the first listed); a device on another platform is kept
/// only when the winner has no device at the same PCI address (or, without addresses, the same code and ordinal).
/// Returns the kept devices and one note per dropped duplicate.
pub fn dedupe_platforms(devs: Vec<ClDevice>) -> (Vec<ClDevice>, Vec<String>) {
    let gpus: Vec<ClDevice> = devs.into_iter().filter(|d| d.is_gpu).collect();
    let mut kept: Vec<ClDevice> = Vec::new();
    let mut dropped = Vec::new();
    let mut vendors: Vec<&'static str> = Vec::new();
    for d in &gpus {
        let v = d.vendor_word();
        if !vendors.contains(&v) {
            vendors.push(v);
        }
    }
    for v in vendors {
        let mine: Vec<&ClDevice> = gpus.iter().filter(|d| d.vendor_word() == v).collect();
        let mut plats: Vec<String> = Vec::new();
        for d in &mine {
            let k = d.platform_key();
            if !plats.contains(&k) {
                plats.push(k);
            }
        }
        let score = |k: &String| {
            let count = mine.iter().filter(|d| &d.platform_key() == k).count();
            let driver = mine.iter().find(|d| &d.platform_key() == k).map(|d| version_tuple(&d.driver)).unwrap_or_default();
            (count, driver)
        };
        let winner = plats.iter().max_by(|a, b| score(a).cmp(&score(b))).cloned().unwrap_or_default();
        let identity = |d: &ClDevice, ordinal: usize| if d.bus.is_empty() { format!("{}#{ordinal}", d.name.to_ascii_lowercase()) } else { d.bus.clone() };
        let mut have: Vec<String> = Vec::new();
        let mut seen_codes: std::collections::HashMap<String, usize> = std::collections::HashMap::new();
        let mut ordinal = |d: &ClDevice| {
            let n = seen_codes.entry(format!("{}|{}", d.platform_key(), d.name.to_ascii_lowercase())).or_insert(0);
            *n += 1;
            *n
        };
        for d in mine.iter().filter(|d| d.platform_key() == winner) {
            let o = ordinal(d);
            have.push(identity(d, o));
            kept.push((*d).clone());
        }
        for d in mine.iter().filter(|d| d.platform_key() != winner) {
            let o = ordinal(d);
            let id = identity(d, o);
            if have.contains(&id) {
                dropped.push(format!("[{}] {} on {} ({}) is the same card as the one on {}: no worker", d.index, d.name, d.platform, d.platform_version, winner));
            } else {
                have.push(id);
                kept.push((*d).clone());
            }
        }
    }
    kept.sort_by_key(|d| d.index.parse::<u64>().unwrap_or(u64::MAX));
    (kept, dropped)
}

/// Keys without an index (it moves when a card arrives): vendor:code, "#2" and up for identical cards in list order.
pub fn assign_keys(cards: &mut [CardState]) {
    let mut seen: std::collections::HashMap<String, usize> = std::collections::HashMap::new();
    for c in cards.iter_mut() {
        if c.code.is_empty() {
            c.code = c.name.clone();
        }
        let base = format!("{}:{}", c.vendor, c.code);
        let n = seen.entry(base.clone()).or_insert(0);
        *n += 1;
        c.key = if *n == 1 { base } else { format!("{base}#{n}") };
    }
}

/// What one Windows enumeration gathered; `assemble` turns it into the list (pure, so the the first Windows machine cases are tests).
#[derive(Default)]
pub struct Inputs {
    /// `nvidia-smi --query-gpu=index,name,memory.total,pci.bus_id --format=csv,noheader`; None = nvidia-smi did not run
    pub nvidia: Option<String>,
    pub nvidia_limits: std::collections::HashMap<String, (f64, f64, f64, f64)>,
    pub cuda_worker: bool,
    /// the OpenCL worker's --list; None = no worker installed or it did not answer
    pub opencl: Option<String>,
    pub opencl_installed: bool,
    /// Windows' adapter list; None = PowerShell did not answer
    pub adapters: Option<Vec<Adapter>>,
}

pub fn assemble(inp: Inputs) -> Detection {
    let mut d = Detection::default();
    d.adapters_listed = inp.adapters.is_some();
    let adapters = inp.adapters.unwrap_or_default();
    let mut used: Vec<usize> = Vec::new();
    match inp.nvidia {
        Some(out) => {
            d.nvidia_listed = true;
            for line in out.lines() {
                let parts: Vec<&str> = line.split(',').map(|s| s.trim()).collect();
                if parts.len() >= 2 && parts[0].chars().all(|c| c.is_ascii_digit()) && !parts[0].is_empty() {
                    let mem_mb: u64 = parts.get(2).and_then(|m| m.split_whitespace().next()).and_then(|n| n.parse::<f64>().ok()).map(|v| v as u64).unwrap_or(0);
                    let detail = if mem_mb > 0 { format!("{} GB", (mem_mb + 512) / 1024) } else { String::new() };
                    // nvidia-smi prints 00000000:01:00.0; the worker and Windows say 01:00.0
                    let bus = parts.get(3).map(|b| b.trim().to_ascii_lowercase()).map(|b| b.rsplit_once(':').map(|(d, r)| format!("{}:{r}", d.rsplit(':').next().unwrap_or(d))).unwrap_or(b)).unwrap_or_default();
                    let (name, adapter) = resolve_name(parts[1], "nvidia", &bus, &adapters, &mut used);
                    let mut c = card(d.cards.len(), &name, "nvidia", "CUDA", &detail, parts[0]);
                    c.code = parts[1].to_string();
                    c.bus = bus;
                    c.kind = classify_kind(parts[1], adapter.map(|i| &adapters[i])).into();
                    c.vram_mb = mem_mb;
                    // driver-check: nvidia-smi's driver_version ("581.57"); the adapter row's DriverVersion is the fallback
                    c.driver_os = parts.get(4).map(|v| v.trim().to_string()).filter(|v| !v.is_empty() && !v.contains("N/A")).or_else(|| adapter.map(|i| adapters[i].driver.clone())).unwrap_or_default();
                    c.path = if inp.cuda_worker { "prebuilt".into() } else { "build".into() };
                    if !inp.cuda_worker {
                        c.message = "no prebuilt CUDA worker in the package; built from source on first run (needs the CUDA Toolkit and Visual Studio)".into();
                    }
                    apply_defaults(&mut c);
                    d.cards.push(c);
                }
            }
            if d.cards.is_empty() {
                d.notes.push("nvidia-smi ran but listed no card".into());
            }
            for c in d.cards.iter_mut() {
                if let Some((dflt, cur, lo, hi)) = inp.nvidia_limits.get(&c.device) {
                    c.power_default_w = *dflt;
                    c.power_limit_w = *cur;
                    c.power_before_w = *cur;
                    c.power_min_w = *lo;
                    c.power_max_w = *hi;
                }
                mark_sweep_support(c);
            }
        }
        None => d.notes.push("nvidia-smi is not on this PC (no NVIDIA driver): no NVIDIA card".into()),
    }
    if let Some(out) = inp.opencl {
        // F-E36: the JSON `devices` hello first (the worker from 14 October 2026), the `--list` text as the fallback
        let json_devs = parse_opencl_devices_json(&out);
        let (devs, listed) = if json_devs.is_empty() { parse_opencl_list(&out) } else { (json_devs, true) };
        d.opencl_listed = listed;
        let (kept, dropped) = dedupe_platforms(devs.into_iter().filter(|dv| dv.vendor_word() != "nvidia").collect());
        d.dropped = dropped;
        for dv in kept {
            let vendor = dv.vendor_word();
            let (name, adapter) = resolve_name(&dv.name, vendor, &dv.bus, &adapters, &mut used);
            let mut c = card(d.cards.len(), &name, vendor, "OpenCL", &dv.units, &dv.index);
            c.code = dv.name.clone();
            c.bus = dv.bus.clone();
            c.platform = format!("{} ({}), driver {}", dv.platform, dv.platform_version, dv.driver);
            c.kind = classify_kind(&dv.name, adapter.map(|i| &adapters[i])).into();
            c.vram_mb = if c.kind == "integrated" { 0 } else { dv.mem_mb };
            // driver-check: Windows' DriverVersion for the card (AMD "32.0.32015.2008", Intel "32.0.101.9034")
            c.driver_os = adapter.map(|i| adapters[i].driver.clone()).unwrap_or_default();
            c.path = "prebuilt".into();
            apply_defaults(&mut c);
            mark_sweep_support(&mut c);
            d.cards.push(c);
        }
    } else if !inp.opencl_installed && d.cards.is_empty() {
        d.notes.push("the OpenCL worker is not installed; AMD and Intel cards cannot be listed".into());
    }
    if d.cards.is_empty() {
        // last resort: the names Windows knows, so the screen can at least say what is in the PC
        for (i, a) in adapters.iter().enumerate().filter(|(_, a)| a.problem().is_none()) {
            let vendor = vendor_of(&a.name);
            let mut c = card(d.cards.len(), &a.name, vendor, if vendor == "nvidia" { "CUDA" } else { "OpenCL" }, "", "");
            c.bus = a.bus.clone();
            c.kind = if looks_integrated(&a.name) { "integrated".into() } else { "unknown".into() };
            c.driver_os = a.driver.clone();
            c.enabled = false;
            c.reason = "seen by Windows, but no worker can drive it (no NVIDIA driver and no OpenCL worker)".into();
            used.push(i);
            d.cards.push(c);
        }
    }
    // the cards Windows lists with a problem (Code 43 after an eGPU hot-plug on the first Windows machine, 5 October 2026): shown, never driven
    for (i, a) in adapters.iter().enumerate() {
        let Some(problem) = a.problem() else { continue };
        if used.contains(&i) || d.cards.iter().any(|c| c.name.trim().eq_ignore_ascii_case(a.name.trim())) {
            continue;
        }
        let vendor = vendor_of(&a.name);
        let mut c = card(d.cards.len(), &a.name, vendor, if vendor == "nvidia" { "CUDA" } else { "OpenCL" }, "", "");
        c.bus = a.bus.clone();
        c.kind = classify_kind(&a.name, Some(a)).into();
        c.driver_os = a.driver.clone();
        mark_unusable(&mut c, &problem);
        d.cards.push(c);
    }
    assign_keys(&mut d.cards);
    d
}

#[cfg(not(target_os = "macos"))]
pub fn detect(bins: &Bins) -> Detection {
    // Windows' own view of every adapter: status and problem code (a Code 43 card is in no tool's list), memory and
    // processor for the integrated call, the PCI address and the names for the rows
    let adapters = adapters();
    // NVIDIA: nvidia-smi ships with the driver
    let nvidia = run_timeout(Command::new(crate::platform::tool("nvidia-smi")).args(["--query-gpu=index,name,memory.total,pci.bus_id,driver_version", "--format=csv,noheader"]), None, Duration::from_secs(10));
    let nvidia_limits = if nvidia.is_some() { nvidia_power_limits() } else { Default::default() };
    // OpenCL: the worker's own device list (AMD, Intel; NVIDIA shows there too and is skipped)
    // F-E36: `devices` (the JSON hello on stdout) first; a worker without that verb answers nothing useful there and the
    // older `--list` text is read instead
    let opencl = bins.opencl.as_ref().and_then(|cl| {
        let j = run_timeout(Command::new(cl).arg("devices"), None, Duration::from_secs(15));
        match j {
            Some(t) if !parse_opencl_devices_json(&t).is_empty() => Some(t),
            _ => run_timeout(Command::new(cl).arg("--list"), None, Duration::from_secs(15)),
        }
    });
    assemble(Inputs { nvidia, nvidia_limits, cuda_worker: bins.cuda.is_some(), opencl, opencl_installed: bins.opencl.is_some(), adapters })
}

/// A listed card no worker can drive: off, no switch, the problem on the row and the hint under it.
pub fn mark_unusable(c: &mut CardState, problem: &str) {
    c.problem = problem.to_string();
    c.enabled = false;
    c.identities = 1;
    c.state = "unusable".into();
    c.message = format!("not usable ({problem})");
    c.reason = PROBLEM_HINT.into();
    c.sweep_supported = false;
    c.sweep_state = "unsupported".into();
    c.sweep_note = c.message.clone();
}

/// Finds the binaries next to the engine (Windows, a plain folder) or in Contents/Resources/bin (macOS bundle).
pub fn find_bins() -> Result<Bins, String> {
    let exe = std::env::current_exe().map_err(|e| e.to_string())?;
    let here = exe.parent().ok_or("no exe dir")?.to_path_buf();
    let mut candidates = vec![];
    if let Some(d) = std::env::var_os("IGNEUM_APP_BIN") {
        candidates.push(std::path::PathBuf::from(d));
    }
    candidates.push(here.clone());
    candidates.push(here.join("bin"));
    if let Some(c) = here.parent() {
        candidates.push(c.join("Resources").join("bin"));
    }
    let ext = if cfg!(windows) { ".exe" } else { "" };
    // Ember ships the engine, the CLI and the workers: a directory holding one of the engine's workers (or the
    // packaged configuration beside them) is the binaries' directory under the worker layer, and the old base's node
    // and miner paths are what they would be, present or not (the old miner path, IGNEUM_APP_OLD_MINER=1, still
    // needs both and refuses by name below; the mini's first launch row of the dmg, 11 October 2026, stopped here)
    let old_path = !crate::sup::layer_on();
    for dir in &candidates {
        let node = dir.join(format!("igneumd{ext}"));
        let miner = dir.join(format!("igneum-miner{ext}"));
        if dir_holds_the_binaries(dir, ext, old_path) {
            let opt = |n: &str| {
                let p = dir.join(format!("{n}{ext}"));
                if p.exists() { Some(p) } else { None }
            };
            // the prebuilt CUDA worker needs NVIDIA's nvrtc64_*_0.dll next to it (as igneum-common.ps1 checks)
            let nvrtc = std::fs::read_dir(dir).ok().map(|rd| rd.flatten().any(|e| { let n = e.file_name().to_string_lossy().to_ascii_lowercase(); n.starts_with("nvrtc64_") && n.ends_with("_0.dll") })).unwrap_or(false);
            let cuda = opt("igneum-worker-cuda").filter(|_| nvrtc || cfg!(not(windows)));
            return Ok(Bins { node, miner, metal: opt("igneum-worker-metal"), cuda, opencl: opt("igneum-worker-opencl"), telemetry: opt("igneum-gpu-telemetry"), dir: dir.clone() });
        }
    }
    Err(format!("{} were not found next to the app (looked in {})", if old_path { "igneumd and igneum-miner" } else { "the workers (igneum-worker-cuda, igneum-worker-metal or igneum-worker-opencl) or igneum-app.json" }, candidates.iter().map(|c| c.display().to_string()).collect::<Vec<_>>().join(", ")))
}

/// Whether a directory is the binaries' directory: under the worker layer, one of the engine's workers or the
/// packaged configuration is there; on the old miner path, the old base's node and miner both are.
pub fn dir_holds_the_binaries(dir: &Path, ext: &str, old_path: bool) -> bool {
    let has = |n: &str| dir.join(format!("{n}{ext}")).exists();
    if old_path {
        return has("igneumd") && has("igneum-miner");
    }
    (has("igneumd") && has("igneum-miner")) || has("igneum-worker-cuda") || has("igneum-worker-metal") || has("igneum-worker-opencl") || dir.join("igneum-app.json").exists()
}

pub fn node_version(node: &Path) -> String {
    run_timeout(Command::new(node).arg("--version"), None, Duration::from_secs(10))
        .and_then(|o| o.lines().next().map(|l| l.trim().to_string()))
        .unwrap_or_default()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn an_ember_bundle_holds_its_binaries_without_the_old_node_and_miner() {
        // the mini's first launch row of the dmg (11 October 2026): Ember.app carries the engine, the CLI and the
        // Metal worker, and find_bins refused it for want of igneumd and igneum-miner
        let d = std::env::temp_dir().join(format!("ember-bins-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&d);
        std::fs::create_dir_all(&d).unwrap();
        assert!(!dir_holds_the_binaries(&d, "", false), "an empty directory holds nothing");
        std::fs::write(d.join("igneum-worker-metal"), b"").unwrap();
        assert!(dir_holds_the_binaries(&d, "", false), "a worker is enough under the layer");
        assert!(!dir_holds_the_binaries(&d, "", true), "the old path still wants the node and the miner");
        std::fs::write(d.join("igneumd"), b"").unwrap();
        std::fs::write(d.join("igneum-miner"), b"").unwrap();
        assert!(dir_holds_the_binaries(&d, "", true));
        let w = std::env::temp_dir().join(format!("ember-bins-win-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&w);
        std::fs::create_dir_all(&w).unwrap();
        std::fs::write(w.join("igneum-app.json"), b"{}").unwrap();
        assert!(dir_holds_the_binaries(&w, ".exe", false), "the packaged configuration marks the directory too");
        let _ = std::fs::remove_dir_all(&d);
        let _ = std::fs::remove_dir_all(&w);
    }

    // the first Windows machine's adapter list on the evening of 5 October 2026, after the RX 9070 XT went in through the Sonnet eGPU box
    // while the app ran: "AMD Radeon RX 9070 XT | status Error | ram 0 GB", "AMD Radeon(TM) Graphics | status OK |
    // ram 2 GB" (the Ryzen iGPU, gfx1036 to OpenCL), "NVIDIA GeForce RTX 5090 | status OK".
    fn pc1() -> Vec<Adapter> {
        parse_adapters(r#"[{"Name":"AMD Radeon RX 9070 XT","Status":"Error","ConfigManagerErrorCode":43,"AdapterRAM":0,"VideoProcessor":"AMD Radeon Graphics Processor (0x7550)","PNPDeviceID":"PCI\\VEN_1002&DEV_7550&SUBSYS_0E4E1002&REV_C0\\6&1A2B3C4D&0&00000008"},
            {"Name":"AMD Radeon(TM) Graphics","Status":"OK","ConfigManagerErrorCode":0,"AdapterRAM":2147483648,"VideoProcessor":"AMD Radeon Graphics Processor (0x164E)","PNPDeviceID":"PCI\\VEN_1002&DEV_164E&SUBSYS_00000000&REV_C1\\4&2E5A1B3&0&0041"},
            {"Name":"NVIDIA GeForce RTX 5090","Status":"OK","ConfigManagerErrorCode":0,"AdapterRAM":4293918720,"VideoProcessor":"NVIDIA GeForce RTX 5090","PNPDeviceID":"PCI\\VEN_10DE&DEV_2B85&SUBSYS_10621043&REV_A1\\4&1F2E3D4C&0&0019"}]"#)
    }

    #[test]
    fn adapters_parse_with_status_code_and_memory() {
        let a = pc1();
        assert_eq!(a.len(), 3);
        assert_eq!(a[0].name, "AMD Radeon RX 9070 XT");
        assert_eq!(a[0].status, "Error");
        assert_eq!(a[0].code, 43);
        assert_eq!(a[0].ram_mb, 0);
        assert_eq!(a[0].problem().as_deref(), Some("Code 43"));
        assert_eq!(a[1].ram_mb, 2048);
        assert_eq!(a[1].problem(), None);
        assert_eq!(a[2].problem(), None);
        // a single adapter: ConvertTo-Json gives one object, not an array
        let one = parse_adapters(r#"{"Name":"NVIDIA GeForce RTX 5090","Status":"OK","ConfigManagerErrorCode":0,"AdapterRAM":4293918720,"VideoProcessor":"NVIDIA GeForce RTX 5090","PNPDeviceID":"PCI\\VEN_10DE"}"#);
        assert_eq!(one.len(), 1);
        assert!(parse_adapters("not json").is_empty());
    }

    #[test]
    fn problem_without_a_code_is_the_status_word() {
        let a = Adapter { name: "x".into(), status: "Degraded".into(), ..Default::default() };
        assert_eq!(a.problem().as_deref(), Some("status Degraded"));
        let fine = Adapter { name: "x".into(), status: "OK".into(), ..Default::default() };
        assert_eq!(fine.problem(), None);
        let code12 = Adapter { name: "x".into(), status: "Error".into(), code: 12, ..Default::default() };
        assert_eq!(code12.problem().as_deref(), Some("Code 12"));
    }

    #[test]
    fn kind_from_pc1_lines_and_the_mac() {
        let a = pc1();
        // the discrete cards, with and without their adapter row
        assert_eq!(classify_kind("AMD Radeon RX 9070 XT", adapter_for("AMD Radeon RX 9070 XT", &a)), "discrete");
        assert_eq!(classify_kind("NVIDIA GeForce RTX 5090", adapter_for("NVIDIA GeForce RTX 5090", &a)), "discrete");
        assert_eq!(classify_kind("NVIDIA GeForce RTX 5090", None), "discrete");
        // the second Windows machine, 7 October 2026: the RTX 5060 Ti behind the Razer Core X V2's USB4 router is an eGPU, not a discrete card
        let egpu = parse_adapters(r#"[{"Name":"NVIDIA GeForce RTX 5060 Ti","Status":"OK","ConfigManagerErrorCode":0,"AdapterRAM":4293918720,"VideoProcessor":"NVIDIA GeForce RTX 5060 Ti","PNPDeviceID":"PCI\\VEN_10DE&DEV_2D04&SUBSYS_8A111043&REV_A1\\31C898B6A12DB04800","BusNumber":11,"Address":0,"External":true}]"#);
        assert_eq!(egpu.len(), 1);
        assert!(egpu[0].external && egpu[0].bus == "0b:00.0");
        assert_eq!(classify_kind("NVIDIA GeForce RTX 5060 Ti", Some(&egpu[0])), "external");
        let inside = parse_adapters(r#"[{"Name":"NVIDIA GeForce RTX 5060 Ti","Status":"OK","ConfigManagerErrorCode":0,"AdapterRAM":4293918720,"VideoProcessor":"x","PNPDeviceID":"PCI\\VEN_10DE&DEV_2D04\\1","BusNumber":1,"Address":0}]"#);
        assert!(!inside[0].external, "no External field reads as inside the case");
        assert_eq!(classify_kind("NVIDIA GeForce RTX 5060 Ti", Some(&inside[0])), "discrete");
        assert!(ADAPTERS_SCRIPT.contains("DEVPKEY_Device_Parent") && ADAPTERS_SCRIPT.contains("USB4") && ADAPTERS_SCRIPT.contains("External = $ext"), "the script walks the parent chain");
        // the Ryzen iGPU: by its Windows name, and by the gfx code the OpenCL worker prints (no adapter row matches a code)
        assert_eq!(classify_kind("AMD Radeon(TM) Graphics", adapter_for("AMD Radeon(TM) Graphics", &a)), "integrated");
        assert_eq!(classify_kind("gfx1036", adapter_for("gfx1036", &a)), "integrated");
        assert_eq!(classify_kind("gfx1036", None), "integrated");
        assert_eq!(classify_kind("gfx1036:xnack-", None), "integrated");
        // a discrete gfx code stays discrete; an Arc card is discrete despite "Intel"
        assert_eq!(classify_kind("gfx1201", None), "discrete");
        assert_eq!(classify_kind("gfx1030", None), "discrete");
        assert_eq!(classify_kind("Intel(R) Arc(TM) A770 Graphics", None), "discrete");
        // an Intel iGPU by its processor string; an AMD discrete card's processor string ("AMD Radeon Graphics Processor") does not count
        let intel = Adapter { name: "Intel(R) Iris(R) Xe Graphics".into(), processor: "Intel(R) Iris(R) Xe Graphics Family".into(), ram_mb: 1024, ..Default::default() };
        assert_eq!(classify_kind("Intel(R) Iris(R) Xe Graphics", Some(&intel)), "integrated");
        assert_eq!(a[0].processor, "AMD Radeon Graphics Processor (0x7550)");
        // shared memory under 1 GB on the adapter row makes an unknown name integrated; 0 says nothing
        let small = Adapter { name: "Some iGPU".into(), ram_mb: 512, ..Default::default() };
        assert_eq!(classify_kind("Some iGPU", Some(&small)), "integrated");
        let unknown = Adapter { name: "Some card".into(), ram_mb: 0, ..Default::default() };
        assert_eq!(classify_kind("Some card", Some(&unknown)), "discrete");
        // the Mac: detect() labels Apple silicon "apple" itself; the name rules do not call it integrated
        assert!(!looks_integrated("Apple M5 Max"));
        assert_eq!(vendor_of("Apple M5 Max"), "apple");
        assert_eq!(vendor_of("Intel(R) Arc(TM) B580 Graphics"), "intel");
        assert_eq!(vendor_of("Intel(R) UHD Graphics 770"), "intel");
        assert_eq!(vendor_of("gfx1036"), "amd");
        assert_eq!(vendor_of("AMD Radeon RX 9070 XT"), "amd");
        assert_eq!(vendor_of("NVIDIA GeForce RTX 5090"), "nvidia");
    }

    // the first Windows machine after Adrenalin 26.9.2 and a reboot (5 October 2026, evening): all three adapters OK, the Windows names,
    // DEV ids and PCI addresses as the coordinator read them (the 5090's bus 01:00.0; the two AMD cards' addresses are
    // not in that reading, so this fixture leaves them empty, as an old worker's --list would)
    fn pc1_rebooted() -> Vec<Adapter> {
        parse_adapters(r#"[{"Name":"AMD Radeon(TM) Graphics","Status":"OK","ConfigManagerErrorCode":0,"AdapterRAM":2147483648,"VideoProcessor":"AMD Radeon Graphics Processor (0x13C0)","PNPDeviceID":"PCI\\VEN_1002&DEV_13C0&SUBSYS_00000000&REV_C1\\4&2E5A1B3&0&0041","BusNumber":null,"Address":null},
            {"Name":"NVIDIA GeForce RTX 5090","Status":"OK","ConfigManagerErrorCode":0,"AdapterRAM":4293918720,"VideoProcessor":"NVIDIA GeForce RTX 5090","PNPDeviceID":"PCI\\VEN_10DE&DEV_2B85&SUBSYS_10621043&REV_A1\\4&1F2E3D4C&0&0019","BusNumber":1,"Address":0},
            {"Name":"AMD Radeon RX 9070 XT","Status":"OK","ConfigManagerErrorCode":0,"AdapterRAM":4293918720,"VideoProcessor":"AMD Radeon Graphics Processor (0x7550)","PNPDeviceID":"PCI\\VEN_1002&DEV_7550&SUBSYS_0E4E1002&REV_C0\\6&1A2B3C4D&0&00000008","BusNumber":null,"Address":null}]"#)
    }
    // the an earlier release app's five rows came from this shape of --list: two AMD platforms, each listing both AMD GPUs (the old
    // 32.0.21042 ICD and the new 32.0.32015 one); the driver strings are the shape AMD's runtime prints, the numbers
    // are the Windows driver builds (approximate: the OpenCL CL_DRIVER_VERSION was not captured)
    const PC1_LIST: &str = "OpenCL devices (4):\n\
 [0] gfx1036  |  AMD Accelerated Parallel Processing (OpenCL 2.1 AMD-APP (3617.0))\n\
      GPU, vendor Advanced Micro Devices, Inc., driver 3617.0 (PAL,HSAIL), OpenCL C 2.0, 2 compute units, 2200 MHz\n\
      global 16384 MiB, max alloc 13926 MiB, local 64 KiB, max work-group 256, sub-group extension: cl_khr_subgroups (no shuffle extension), AMD wavefront width 32\n\
 [1] gfx1201  |  AMD Accelerated Parallel Processing (OpenCL 2.1 AMD-APP (3617.0))\n\
      GPU, vendor Advanced Micro Devices, Inc., driver 3617.0 (PAL,HSAIL), OpenCL C 2.0, 32 compute units, 2970 MHz\n\
      global 16368 MiB, max alloc 13912 MiB, local 64 KiB, max work-group 256, sub-group extension: cl_khr_subgroups (no shuffle extension), AMD wavefront width 32\n\
 [2] gfx1036  |  AMD Accelerated Parallel Processing (OpenCL 2.1 AMD-APP (3649.0))\n\
      GPU, vendor Advanced Micro Devices, Inc., driver 3649.0 (PAL,HSAIL), OpenCL C 2.0, 2 compute units, 2200 MHz\n\
      global 16384 MiB, max alloc 13926 MiB, local 64 KiB, max work-group 256, sub-group extension: cl_khr_subgroups (no shuffle extension), AMD wavefront width 32\n\
 [3] gfx1201  |  AMD Accelerated Parallel Processing (OpenCL 2.1 AMD-APP (3649.0))\n\
      GPU, vendor Advanced Micro Devices, Inc., driver 3649.0 (PAL,HSAIL), OpenCL C 2.0, 32 compute units, 2970 MHz\n\
      global 16368 MiB, max alloc 13912 MiB, local 64 KiB, max work-group 256, sub-group extension: cl_khr_subgroups (no shuffle extension), AMD wavefront width 32\n";
    const PC1_SMI: &str = "0, NVIDIA GeForce RTX 5090, 32607 MiB, 00000000:01:00.0\n";

    #[test]
    fn f_e36_the_opencl_devices_json_lists_the_arc_by_index_and_bus() {
        let line = r#"{"t":"hello","worker":"igneum-worker-opencl","version":"0.0.1","backend":"opencl","runtime":"OpenCL 3.0 NEO","devices":[{"index":2,"bus_id":"0000:63:00.0","id":"0000:63:00.0","name":"Intel(R) Arc(TM) B580 Graphics","vendor":"intel","driver":"32.0.101.9150","memory_bytes":12509904896,"arch":"Intel(R) Arc(TM) B580 Graphics","fingerprint":"a5b18c0d","identity":"uuid"}]}"#;
        let devs = parse_opencl_devices_json(line);
        assert_eq!(devs.len(), 1);
        assert_eq!(devs[0].index, "2");
        assert_eq!(devs[0].bus, "63:00.0");
        assert_eq!(devs[0].vendor_word(), "intel");
        assert_eq!(devs[0].mem_mb, 11930);
        assert!(parse_opencl_devices_json("OpenCL devices\n[0] x | y").is_empty(), "the text form is not JSON");
    }

    fn pc1_inputs(list: &str) -> Inputs {
        Inputs { nvidia: Some(PC1_SMI.into()), nvidia_limits: Default::default(), cuda_worker: true, opencl: Some(list.into()), opencl_installed: true, adapters: Some(pc1_rebooted()) }
    }

    #[test]
    fn opencl_list_parses_both_platforms_and_the_pci_field() {
        let (devs, listed) = parse_opencl_list(PC1_LIST);
        assert!(listed);
        assert_eq!(devs.len(), 4);
        assert_eq!(devs[1].index, "1");
        assert_eq!(devs[1].name, "gfx1201");
        assert_eq!(devs[1].platform, "AMD Accelerated Parallel Processing");
        assert_eq!(devs[1].platform_version, "OpenCL 2.1 AMD-APP (3617.0)");
        assert_eq!(devs[1].driver, "3617.0 (PAL,HSAIL)");
        assert_eq!(devs[1].units, "32 compute units");
        assert_eq!(devs[1].mem_mb, 16368);
        assert_eq!(devs[1].bus, "");
        assert!(devs[1].is_gpu);
        assert_eq!(devs[1].vendor_word(), "amd");
        let with_pci = PC1_LIST.replace("32 compute units, 2970 MHz\n", "32 compute units, 2970 MHz, pci 05:00.0\n");
        let (devs, _) = parse_opencl_list(&with_pci);
        assert_eq!(devs[1].bus, "05:00.0");
        assert_eq!(devs[3].bus, "05:00.0");
        assert!(!parse_opencl_list("").1);
    }

    #[test]
    fn two_amd_platforms_give_one_entry_per_card() {
        // without PCI addresses: by code and ordinal, the newer driver wins
        let (devs, _) = parse_opencl_list(PC1_LIST);
        let (kept, dropped) = dedupe_platforms(devs);
        assert_eq!(kept.iter().map(|d| d.index.as_str()).collect::<Vec<_>>(), vec!["2", "3"]);
        assert_eq!(dropped.len(), 2);
        assert!(dropped[0].starts_with("[0] gfx1036 on AMD Accelerated Parallel Processing (OpenCL 2.1 AMD-APP (3617.0)) is the same card as"), "{}", dropped[0]);
        // with PCI addresses: by address; a card the winner does not list (the old ICD still serving a third card) is kept
        let text = PC1_LIST
            .replace("2 compute units, 2200 MHz\n", "2 compute units, 2200 MHz, pci 0c:00.0\n")
            .replace("32 compute units, 2970 MHz\n", "32 compute units, 2970 MHz, pci 05:00.0\n")
            + " [4] gfx1100  |  AMD Accelerated Parallel Processing (OpenCL 2.1 AMD-APP (3617.0))\n      GPU, vendor Advanced Micro Devices, Inc., driver 3617.0 (PAL,HSAIL), OpenCL C 2.0, 96 compute units, 2500 MHz, pci 09:00.0\n      global 24560 MiB\n";
        let (devs, _) = parse_opencl_list(&text);
        let (kept, dropped) = dedupe_platforms(devs);
        // the old platform now lists three and wins on count: its three stay, the new platform's two are duplicates
        assert_eq!(kept.iter().map(|d| d.index.as_str()).collect::<Vec<_>>(), vec!["0", "1", "4"]);
        assert_eq!(dropped.len(), 2);
        // two real twins on one platform keep both entries (same code, different ordinal or address)
        let twins = "OpenCL devices (2):\n [0] gfx1201  |  P (v)\n      GPU, vendor Advanced Micro Devices, Inc., driver 1.0, OpenCL C 2.0, 32 compute units, 2970 MHz\n [1] gfx1201  |  P (v)\n      GPU, vendor Advanced Micro Devices, Inc., driver 1.0, OpenCL C 2.0, 32 compute units, 2970 MHz\n";
        let (kept, dropped) = dedupe_platforms(parse_opencl_list(twins).0);
        assert_eq!(kept.len(), 2);
        assert!(dropped.is_empty());
    }

    #[test]
    fn names_come_from_windows_by_bus_then_device_id_then_the_table() {
        let a = pc1_rebooted();
        let mut used = Vec::new();
        assert_eq!(resolve_name("NVIDIA GeForce RTX 5090", "nvidia", "01:00.0", &a, &mut used).0, "NVIDIA GeForce RTX 5090");
        assert_eq!(resolve_name("gfx1201", "amd", "", &a, &mut used).0, "AMD Radeon RX 9070 XT");
        assert_eq!(resolve_name("gfx1036", "amd", "", &a, &mut used).0, "AMD Radeon(TM) Graphics");
        assert_eq!(used.len(), 3);
        // every adapter is taken: a second gfx1036 gets the table's words, a code the table lacks stays a code
        assert_eq!(resolve_name("gfx1036", "amd", "", &a, &mut used).0, "Ryzen integrated Radeon Graphics");
        assert_eq!(resolve_name("gfx9999", "amd", "", &a, &mut used).0, "gfx9999");
        assert_eq!(resolve_name("gfx1100", "amd", "", &[], &mut Vec::new()).0, "Radeon RX 7900 XTX / XT");
        // by PCI address when both sides have one, before any table
        let mut b = pc1_rebooted();
        b[2].bus = "05:00.0".into();
        let mut used = Vec::new();
        assert_eq!(resolve_name("gfx1201", "amd", "05:00.0", &b, &mut used), ("AMD Radeon RX 9070 XT".to_string(), Some(2)));
        // the device id alone names a card whose adapter name the table does not know
        let mut c = pc1_rebooted();
        c[2].name = "AMD Radeon RX 9070 XT OC Edition".into();
        assert_eq!(resolve_name("gfx1201", "amd", "", &c, &mut Vec::new()).0, "AMD Radeon RX 9070 XT OC Edition");
        assert_eq!(a[2].device_id(), 0x7550);
        assert_eq!(a[0].device_id(), 0x13C0);
        assert_eq!(a[1].bus, "01:00.0");
    }

    #[test]
    fn pc1_five_rows_become_three_cards_named_properly() {
        let d = assemble(pc1_inputs(PC1_LIST));
        assert!(d.nvidia_listed && d.opencl_listed && d.adapters_listed);
        let rows: Vec<(String, String, String, String, bool, String)> = d.cards.iter().map(|c| (c.name.clone(), c.key.clone(), c.kind.clone(), c.device.clone(), c.enabled, c.code.clone())).collect();
        assert_eq!(rows, vec![
            ("NVIDIA GeForce RTX 5090".into(), "nvidia:NVIDIA GeForce RTX 5090".into(), "discrete".into(), "0".into(), true, "NVIDIA GeForce RTX 5090".into()),
            ("AMD Radeon(TM) Graphics".into(), "amd:gfx1036".into(), "integrated".into(), "2".into(), false, "gfx1036".into()),
            ("AMD Radeon RX 9070 XT".into(), "amd:gfx1201".into(), "discrete".into(), "3".into(), true, "gfx1201".into()),
        ]);
        assert_eq!(d.cards[0].bus, "01:00.0");
        assert_eq!(d.cards[1].reason, INTEGRATED_REASON);
        assert_eq!(d.cards[1].identities, 1);
        assert_eq!(d.cards[2].identities, 8, "16 GB: 8 identities");
        assert_eq!(d.cards[2].vram_mb, 16368);
        assert!(d.cards[2].platform.contains("3649.0"));
        assert_eq!(d.dropped.len(), 2);
        assert!(d.notes.is_empty(), "{:?}", d.notes);
        assert_eq!(crate::hotplug::cards_line(&d.cards), "cards: NVIDIA GeForce RTX 5090 [discrete, off] | AMD Radeon(TM) Graphics [integrated, off] | AMD Radeon RX 9070 XT [discrete, off]");
        // the same machine before the eGPU: one platform, the iGPU alone; the keys do not depend on the index
        let before = "OpenCL devices (1):\n [0] gfx1036  |  AMD Accelerated Parallel Processing (OpenCL 2.1 AMD-APP (3617.0))\n      GPU, vendor Advanced Micro Devices, Inc., driver 3617.0 (PAL,HSAIL), OpenCL C 2.0, 2 compute units, 2200 MHz\n      global 16384 MiB\n";
        let d0 = assemble(pc1_inputs(before));
        assert_eq!(d0.cards[1].key, "amd:gfx1036");
        assert_eq!(d0.cards[1].device, "0");
        // and the diff between the two lists: the iGPU moved (device 0 to 2), the 9070 XT is new, nothing is removed
        let diff = crate::hotplug::diff(&d0.cards, &d.cards, &|c| d.listed(c));
        assert_eq!(diff.unchanged, vec![0]);
        assert_eq!(diff.moved.len(), 1);
        assert_eq!(diff.moved[0].0, 1);
        assert_eq!(diff.added.len(), 1);
        assert_eq!(diff.added[0].name, "AMD Radeon RX 9070 XT");
        assert!(diff.removed.is_empty());
    }

    /// The first Intel card (the B580 on the first Windows machine, 7 October 2026, docs/plans/intel-arc.md): the Arc driver's OpenCL
    /// platform lists it as "Intel(R) Arc(TM) B580 Graphics" under vendor "Intel(R) Corporation" (the line shape is
    /// the worker's; the device name and vendor string are Intel's documented forms, approximate until the the first Windows machine job
    /// prints them). It becomes a discrete 12 GB card, vendor intel, worker OpenCL, on with 8 identities; the
    /// Arc-branded processor graphics stay integrated; the NVIDIA cards beside it are untouched.
    #[test]
    fn an_intel_arc_card_on_the_intel_platform_is_a_discrete_intel_card() {
        let list = String::from(PC1_LIST)
            + " [4] Intel(R) Arc(TM) B580 Graphics  |  Intel(R) OpenCL Graphics (OpenCL 3.0 )\n      GPU, vendor Intel(R) Corporation, driver 32.0.101.9034, OpenCL C 3.0, 160 compute units, 2850 MHz, pci 05:00.0\n      global 12208 MiB, max alloc 4095 MiB, local 64 KiB, max work-group 1024, sub-group extension: cl_intel_subgroups\n";
        let (devs, _) = parse_opencl_list(&list);
        let arc = devs.iter().find(|d| d.name.contains("B580")).expect("the Arc line parses");
        assert_eq!(arc.vendor_word(), "intel");
        assert_eq!(arc.mem_mb, 12208);
        assert_eq!(arc.bus, "05:00.0");
        let d = assemble(pc1_inputs(&list));
        let c = d.cards.iter().find(|c| c.vendor == "intel").expect("an intel card");
        assert_eq!((c.name.as_str(), c.kind.as_str(), c.worker.as_str(), c.device.as_str(), c.enabled, c.identities), ("Intel(R) Arc(TM) B580 Graphics", "discrete", "OpenCL", "4", true, 8));
        assert_eq!(c.key, "intel:Intel(R) Arc(TM) B580 Graphics");
        assert_eq!(c.vram_mb, 12208);
        assert!(!c.sweep_supported, "no cap through OpenCL");
        assert!(c.sweep_note.contains("Intel Arc"), "{}", c.sweep_note);
        assert!(d.listed(c), "the OpenCL list answered, so a vanished Arc can be called removed");
        // the Arc names: cards discrete, processor graphics integrated
        for card in ["Intel(R) Arc(TM) B580 Graphics", "Intel(R) Arc(TM) A770 Graphics", "Intel(R) Arc(TM) A380 Graphics", "Intel(R) Arc(TM) Pro A60 Graphics", "Intel(R) Arc(TM) B570 Graphics"] {
            assert_eq!(classify_kind(card, None), "discrete", "{card}");
        }
        for igpu in ["Intel(R) Arc(TM) Graphics", "Intel(R) Arc(TM) 140V GPU (16GB)", "Intel(R) Arc(TM) 130V GPU", "Intel(R) Arc(TM) 140T GPU"] {
            assert_eq!(classify_kind(igpu, None), "integrated", "{igpu}");
        }
        assert_eq!(classify_kind("Intel(R) UHD Graphics 770", None), "integrated");
    }

    #[test]
    fn keys_number_identical_cards() {
        let mut cards = vec![card(0, "NVIDIA GeForce RTX 5090", "nvidia", "CUDA", "", "0"), card(1, "NVIDIA GeForce RTX 5090", "nvidia", "CUDA", "", "1"), card(2, "gfx1201", "amd", "OpenCL", "", "2")];
        cards[2].name = "AMD Radeon RX 9070 XT".into();
        assign_keys(&mut cards);
        assert_eq!(cards.iter().map(|c| c.key.as_str()).collect::<Vec<_>>(), vec!["nvidia:NVIDIA GeForce RTX 5090", "nvidia:NVIDIA GeForce RTX 5090#2", "amd:gfx1201"]);
    }

    #[test]
    fn unusable_card_row() {
        let mut c = card(2, "AMD Radeon RX 9070 XT", "amd", "OpenCL", "", "");
        mark_unusable(&mut c, "Code 43");
        assert!(!c.enabled);
        assert_eq!(c.state, "unusable");
        assert_eq!(c.message, "not usable (Code 43)");
        assert_eq!(c.reason, PROBLEM_HINT);
        assert!(!c.present());
    }

    #[test]
    fn integrated_default_is_off_with_the_row_words() {
        let mut c = card(1, "gfx1036", "amd", "OpenCL", "2 compute units", "1");
        c.kind = classify_kind("gfx1036", None).into();
        apply_defaults(&mut c);
        assert!(!c.enabled);
        assert_eq!(c.identities, 1);
        assert_eq!(c.reason, INTEGRATED_REASON);
        let mut big = card(0, "NVIDIA GeForce RTX 5090", "nvidia", "CUDA", "32 GB", "0");
        big.kind = "discrete".into();
        big.vram_mb = 32768;
        apply_defaults(&mut big);
        assert!(big.enabled);
        assert_eq!(big.identities, 8);
    }
}

/// The machine's RAM in MB (the prover default's RAM gate, src/provedefault.rs): Windows through
/// `Win32_OperatingSystem.TotalVisibleMemorySize` (KB), Linux through `/proc/meminfo`, macOS through `sysctl hw.memsize`;
/// None when unreadable (no gate).
pub fn total_ram_mb() -> Option<u64> {
    if cfg!(windows) {
        let out = run_timeout(Command::new(crate::platform::tool("powershell")).args(["-NoProfile", "-Command", "(Get-CimInstance Win32_OperatingSystem).TotalVisibleMemorySize"]), None, Duration::from_secs(20))?;
        return out.replace('\0', "").trim().parse::<u64>().ok().map(|kb| kb / 1024);
    }
    if cfg!(target_os = "linux") {
        let text = std::fs::read_to_string("/proc/meminfo").ok()?;
        return text.lines().find(|l| l.starts_with("MemTotal:")).and_then(|l| l.split_whitespace().nth(1)).and_then(|kb| kb.parse::<u64>().ok()).map(|kb| kb / 1024);
    }
    let out = run_timeout(Command::new("sysctl").args(["-n", "hw.memsize"]), None, Duration::from_secs(5))?;
    out.trim().parse::<u64>().ok().map(|b| b / (1024 * 1024))
}
