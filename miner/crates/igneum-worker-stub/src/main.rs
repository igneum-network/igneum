//! igneum-worker-stub: speaks worker protocol v0 (docs/gpu-a/WORKER-PROTOCOL.md, with the extensions of
//! igneum-worker-proto) with the placeholder hash `stub-v0` over a simulated device, so the supervisor, the
//! tuner, the economics display and the first-use flow run before Phase Zero picks the algorithm. Nothing here
//! is a number about any card.
//!
//! Simulated devices come from `IGNEUM_STUB_DEVICES`, a comma list of `vendor:gb[:kind]`, default
//! `nvidia:8:discrete`. The device's rate rises with intensity to a knee and with duty; power rises faster;
//! above intensity 48 it produces a corrupted share now and then (so the tuner's instability rule is exercised).

use igneum_worker_proto as proto;
use proto::stubhash;
use proto::{AlgoEntry, Budget, Fault, FromWorker, Hello, HelloDevice, Knob, KnobAck, Params, Prepared, Ready, Refuse, Share, Stat, Telemetry, ToWorker, WorkerState, PROTO_VERSION};
use sha2::{Digest, Sha256};
use std::collections::BTreeMap;
use std::io::{BufRead, Write};
use std::sync::mpsc::{self, Receiver, Sender};
use std::sync::{Arc, Mutex};
use std::thread;
use std::time::{Duration, Instant};

const VERSION: &str = env!("CARGO_PKG_VERSION");
const GIB: u64 = 1 << 30;
const DATASET_BYTES: u64 = GIB;
const WORKING_BYTES: u64 = 64 << 20;

struct Out {
    stdout: Mutex<(std::io::Stdout, proto::Seq)>,
}

impl Out {
    fn emit(&self, m: &FromWorker, re: Option<u64>) {
        let mut g = self.stdout.lock().unwrap_or_else(|e| e.into_inner());
        let seq = g.1.next();
        if let Ok(line) = proto::encode(seq, re, m) {
            let _ = g.0.write_all(line.as_bytes());
            let _ = g.0.flush();
        }
    }
}

fn knobs() -> Vec<Knob> {
    vec![
        Knob::additive("intensity", 1.0, 64.0, 1.0, 16.0, ""),
        Knob::doubling("batch_nonces", 16384.0, 16777216.0, 1048576.0, "nonces"),
        Knob::additive("streams", 1.0, 4.0, 1.0, 2.0, ""),
        Knob::additive("duty", 0.1, 1.0, 0.05, 1.0, ""),
    ]
}

struct DeviceSpec {
    hello: HelloDevice,
    vendor: String,
}

fn parse_devices() -> Vec<DeviceSpec> {
    let spec = std::env::var("IGNEUM_STUB_DEVICES").unwrap_or_else(|_| "nvidia:8:discrete".into());
    let mut out = Vec::new();
    for (i, item) in spec.split(',').enumerate() {
        let parts: Vec<&str> = item.trim().split(':').collect();
        if parts.is_empty() || parts[0].is_empty() {
            continue;
        }
        let vendor = parts[0].to_string();
        let gb: u64 = parts.get(1).and_then(|s| s.parse().ok()).unwrap_or(8);
        let kind = parts.get(2).map(|s| s.to_string()).unwrap_or_else(|| if vendor == "apple" { "apple".into() } else { "discrete".into() });
        let name = format!("Simulated {vendor} device {gb} GB");
        let arch = match vendor.as_str() {
            "nvidia" => "sm_75",
            "amd" => "gfx1030",
            "apple" => "apple7",
            _ => "stub",
        };
        let mut h = Sha256::new();
        for part in ["stub", &vendor, &i.to_string(), &name, arch] {
            h.update(part.as_bytes());
            h.update([0u8]);
        }
        let id = format!("stub:{}", &proto::hex_encode(&h.finalize())[..16]);
        out.push(DeviceSpec {
            hello: HelloDevice { id: id.clone(), name, memory_bytes: gb * GIB, arch: arch.into(), arch_detail: None, driver: "stub 0".into(), runtime: "stub".into(), vendor: Some(vendor.clone()), kind: Some(kind), bus_id: Some(id), intrinsic_id: None, identity: None, slowdown_c: Some(90.0), knobs: knobs() },
            vendor,
        });
    }
    out
}

enum Cmd {
    Configure(proto::Configure, u64),
    Job(proto::Job),
    Cancel(String),
    Set(String, f64, u64),
    Pause,
    Resume,
    Prepare(u64, u64),
    Shutdown,
}

fn knob(p: &Params, name: &str, default: f64) -> f64 {
    *p.get(name).unwrap_or(&default)
}

fn rate_for(p: &Params, vendor: &str) -> f64 {
    let i = knob(p, "intensity", 16.0);
    let duty = knob(p, "duty", 1.0);
    let streams = knob(p, "streams", 2.0);
    let base = match vendor {
        "apple" => 1.2e6,
        "amd" => 1.6e6,
        _ => 2.0e6,
    };
    base * (1.0 - (-i / 20.0).exp()) * duty * (1.0 + 0.08 * (streams - 1.0))
}

fn power_for(p: &Params, vendor: &str) -> f64 {
    let i = knob(p, "intensity", 16.0);
    let duty = knob(p, "duty", 1.0);
    let streams = knob(p, "streams", 2.0);
    let base = if vendor == "apple" { 8.0 } else { 30.0 };
    (base + 2.2 * i + 0.03 * i * i) * duty * (1.0 + 0.05 * (streams - 1.0))
}

/// Apply one knob, clamped to its ladder; `None` when the name is unknown.
fn apply_knob(params: &mut Params, name: &str, value: f64) -> Option<f64> {
    let kn = knobs().into_iter().find(|kn| kn.name == name)?;
    let v = kn.clamp(value);
    params.insert(name.to_string(), v);
    Some(v)
}

struct Sim {
    spec: DeviceSpec,
    params: Params,
    open: bool,
    paused: bool,
    epoch: Option<u64>,
    phase: String,
    preparing_until: Option<Instant>,
    temp: f64,
    stat_ms: u64,
    /// job, next nonce, end, hashes for the job
    job: Option<(proto::Job, u64, u64, u64)>,
    last_stat: Instant,
    stat_hashes: u64,
}

fn run_device(spec: DeviceSpec, rx: Receiver<Cmd>, out: Arc<Out>) {
    let mut sim = Sim { spec, params: Params::new(), open: false, paused: false, epoch: None, phase: String::new(), preparing_until: None, temp: 35.0, stat_ms: 1000, job: None, last_stat: Instant::now(), stat_hashes: 0 };
    for k in knobs() {
        sim.params.insert(k.name, k.default);
    }
    let dev = sim.spec.hello.id.clone();
    let vendor = sim.spec.vendor.clone();
    loop {
        let busy = sim.job.is_some() && sim.open && !sim.paused;
        let cmd = if busy { rx.try_recv().ok() } else { rx.recv_timeout(Duration::from_millis(200)).ok() };
        if let Some(c) = cmd {
            match c {
                Cmd::Shutdown => {
                    sim.job = None;
                    sim.open = false;
                    return;
                }
                Cmd::Configure(c, re) => {
                    let need = DATASET_BYTES + WORKING_BYTES;
                    let total = sim.spec.hello.memory_bytes;
                    let free = total.saturating_sub(300 << 20).saturating_sub(c.memory_margin_bytes);
                    if c.algo != stubhash::STUB_ALGO {
                        out.emit(&FromWorker::Refuse(Refuse { device: dev.clone(), reason: format!("this stub carries {} only", stubhash::STUB_ALGO), term: Some("algo".into()), need_bytes: None, have_bytes: None }), Some(re));
                        continue;
                    }
                    if free < need {
                        out.emit(&FromWorker::Refuse(Refuse { device: dev.clone(), reason: "the dataset and its buffers do not fit beside the margin".into(), term: Some("fit_cold_bytes".into()), need_bytes: Some(need), have_bytes: Some(free) }), Some(re));
                        continue;
                    }
                    sim.stat_ms = c.stat_ms.clamp(100, 60_000);
                    eprintln!("stub device {dev}: building the simulated dataset");
                    thread::sleep(Duration::from_millis(1500));
                    sim.open = true;
                    sim.paused = false;
                    sim.last_stat = Instant::now();
                    out.emit(&FromWorker::Ready(Ready { device: dev.clone(), algo: c.algo, budget: Budget { total_bytes: total, free_bytes: free, fit_cold_bytes: free.saturating_sub(need), fit_warm_bytes: free.saturating_sub(2 * need), can_warm: free >= 2 * need } }), Some(re));
                }
                Cmd::Prepare(height, re) => {
                    out.emit(&FromWorker::Prepared(Prepared { epoch: stubhash::epoch_of(height), ms: 1200, bytes: DATASET_BYTES, mode: "warm".into(), digest: None }), Some(re));
                }
                Cmd::Job(j) => {
                    if !sim.open {
                        out.emit(&FromWorker::Fault(Fault { code: "not_configured".into(), message: "job before configure".into(), recoverable: true }), None);
                        continue;
                    }
                    if !j.algo.is_empty() && j.algo != stubhash::STUB_ALGO {
                        out.emit(&FromWorker::Fault(Fault { code: "algo".into(), message: format!("job for {}, configured for {}", j.algo, stubhash::STUB_ALGO), recoverable: true }), None);
                        continue;
                    }
                    let epoch = stubhash::epoch_of(j.height);
                    if sim.epoch != Some(epoch) {
                        // a new epoch: the simulated dataset is rebuilt, shown as a transition, not as mining
                        sim.phase = if sim.epoch.is_some() { "transition" } else { "dataset" }.into();
                        sim.preparing_until = Some(Instant::now() + Duration::from_millis(1200));
                        sim.epoch = Some(epoch);
                    }
                    if j.clean || sim.job.is_some() {
                        sim.job = None;
                    }
                    let end = j.nonce_start.saturating_add(j.nonce_count);
                    let start = j.nonce_start;
                    sim.job = Some((j, start, end, 0));
                }
                Cmd::Cancel(id) => {
                    if sim.job.as_ref().map_or(false, |(j, _, _, _)| j.job_id == id) {
                        sim.job = None;
                    }
                }
                Cmd::Pause => sim.paused = true,
                Cmd::Resume => sim.paused = false,
                Cmd::Set(name, value, re) => match apply_knob(&mut sim.params, &name, value) {
                    Some(v) => out.emit(&FromWorker::Knob(KnobAck { knob: name, value: v }), Some(re)),
                    None => out.emit(&FromWorker::Fault(Fault { code: "knob".into(), message: format!("unknown knob {name}"), recoverable: true }), Some(re)),
                },
            }
        }

        if !sim.open {
            continue;
        }

        // one 100 ms slice of simulated hashing
        let slice = Duration::from_millis(100);
        let t0 = Instant::now();
        let preparing = sim.preparing_until.map_or(false, |t| Instant::now() < t);
        if !preparing && sim.preparing_until.is_some() {
            sim.preparing_until = None;
            sim.phase.clear();
        }
        if !sim.paused && !preparing {
            if let Some((job, next, end, hashes)) = sim.job.as_mut() {
                let n = (0.1 * rate_for(&sim.params, &vendor)) as u64;
                let n = n.min(end.saturating_sub(*next)).min(400_000);
                let header = proto::hex_to_32(&job.header).unwrap_or([0; 32]);
                let share_target = proto::hex_to_32(&job.share_target).unwrap_or([0xff; 32]);
                let unstable = knob(&sim.params, "intensity", 16.0) > 48.0;
                let mut found = Vec::new();
                for k in 0..n {
                    let nonce = *next + k;
                    let (mix, mut hash) = stubhash::stub_hash_at(job.height, &header, nonce);
                    if stubhash::meets_target(&hash, &share_target) {
                        if unstable && nonce % 7 == 0 {
                            hash[0] ^= 0x80; // a corrupted result, as an unstable card produces
                        }
                        found.push(Share { job_id: job.job_id.clone(), nonce, mix: proto::hex_encode(&mix), hash: proto::hex_encode(&hash) });
                    }
                }
                *next += n;
                *hashes += n;
                sim.stat_hashes += n;
                let done = *next >= *end;
                for f in found {
                    out.emit(&FromWorker::Share(f), None);
                }
                if done {
                    sim.job = None;
                }
            }
        }
        if sim.last_stat.elapsed() >= Duration::from_millis(sim.stat_ms) {
            let preparing = sim.preparing_until.is_some();
            let mining = sim.job.is_some() && !sim.paused && !preparing;
            let power = if mining { power_for(&sim.params, &vendor) } else { 12.0 };
            let goal = 35.0 + power / 4.0;
            sim.temp += (goal - sim.temp) * 0.15;
            let fan = ((sim.temp - 40.0) * 3.0).clamp(0.0, 100.0);
            let mut throttle = Vec::new();
            if sim.temp > 83.0 {
                throttle.push("thermal".to_string());
            }
            let apple = vendor == "apple";
            out.emit(
                &FromWorker::Stat(Stat {
                    job_id: sim.job.as_ref().map(|(j, _, _, _)| j.job_id.clone()).unwrap_or_default(),
                    hashes: sim.stat_hashes,
                    elapsed_ms: sim.last_stat.elapsed().as_millis() as u64,
                    telemetry: Telemetry {
                        power_w: Some((power * 10.0).round() / 10.0),
                        temp_c: Some((sim.temp * 10.0).round() / 10.0),
                        core_mhz: Some(1500.0),
                        mem_mhz: Some(7000.0),
                        memory_used_bytes: Some(DATASET_BYTES + WORKING_BYTES),
                        throttle,
                        // as the real workers do: a field the platform does not give is omitted, never guessed
                        fan_pct: if apple { None } else { Some(fan.round()) },
                        util_pct: if apple { None } else { Some(if mining { 98.0 } else { 0.0 }) },
                        thermal_pressure: if apple { Some(if sim.temp > 80.0 { "serious" } else if sim.temp > 70.0 { "fair" } else { "nominal" }.into()) } else { None },
                        on_battery: if apple { Some(false) } else { None },
                        low_power_mode: if apple { Some(false) } else { None },
                        memory_pressure: if apple { Some("normal".into()) } else { None },
                        asleep: if apple { Some(false) } else { None },
                        duty_effective: if apple { Some(knob(&sim.params, "duty", 1.0)) } else { None },
                        gpu_allocated_bytes: if apple { Some(DATASET_BYTES + WORKING_BYTES) } else { None },
                        code_bytes: None,
                        build_in_flight: None,
                        build_progress: None,
                        last_swap_wait_ms: None,
                    },
                    state: if preparing { WorkerState::Preparing } else if sim.paused { WorkerState::Paused } else if mining { WorkerState::Mining } else { WorkerState::Idle },
                    phase: if preparing { sim.phase.clone() } else { String::new() },
                    building: None,
                    epoch: sim.epoch,
                }),
                None,
            );
            sim.stat_hashes = 0;
            sim.last_stat = Instant::now();
        }
        let spent = t0.elapsed();
        if spent < slice {
            thread::sleep(slice - spent);
        }
    }
}

fn main() {
    let out = Arc::new(Out { stdout: Mutex::new((std::io::stdout(), proto::Seq::default())) });
    let specs = parse_devices();
    let hello_devices: Vec<HelloDevice> = specs.iter().map(|s| s.hello.clone()).collect();
    out.emit(
        &FromWorker::Hello(Hello {
            proto: PROTO_VERSION,
            worker: "igneum-worker-stub".into(),
            version: VERSION.into(),
            backend: "stub".into(),
            algos: vec![AlgoEntry { id: stubhash::STUB_ALGO.into(), vectors_ok: true }],
            devices: hello_devices,
            note: stubhash::STUB_NOTE.into(),
            extensions: Vec::new(),
        }),
        None,
    );

    let mut senders: BTreeMap<String, Sender<Cmd>> = BTreeMap::new();
    let mut handles = Vec::new();
    for spec in specs {
        let (tx, rx) = mpsc::channel();
        senders.insert(spec.hello.id.clone(), tx);
        let o = out.clone();
        handles.push(thread::spawn(move || run_device(spec, rx, o)));
    }

    let stdin = std::io::stdin();
    let mut lines = stdin.lock().lines();
    let mut first = true;
    let mut configured: Option<String> = None;
    while let Some(Ok(line)) = lines.next() {
        if line.len() > proto::MAX_LINE_BYTES {
            out.emit(&FromWorker::Fault(Fault { code: "protocol".into(), message: "line too long".into(), recoverable: true }), None);
            continue;
        }
        let env: proto::Envelope<ToWorker> = match proto::decode(&line) {
            Ok(m) => m,
            Err(e) => {
                if first {
                    std::process::exit(proto::EXIT_BAD_FIRST_LINE);
                }
                out.emit(&FromWorker::Fault(Fault { code: "protocol".into(), message: format!("{e}").chars().take(200).collect(), recoverable: true }), None);
                continue;
            }
        };
        if let Err(e) = proto::validate_to_worker(&env.body) {
            out.emit(&FromWorker::Fault(Fault { code: "protocol".into(), message: format!("{e}").chars().take(200).collect(), recoverable: true }), Some(env.seq));
            continue;
        }
        if first {
            first = false;
            if !matches!(env.body, ToWorker::Configure(_)) {
                eprintln!("stub: the first line was not configure; exiting 64");
                std::process::exit(proto::EXIT_BAD_FIRST_LINE);
            }
        }
        let seq = env.seq;
        match env.body {
            ToWorker::Configure(c) => {
                let Some(tx) = senders.get(&c.device) else {
                    out.emit(&FromWorker::Refuse(Refuse { device: c.device.clone(), reason: "no such device".into(), term: Some("device".into()), need_bytes: None, have_bytes: None }), Some(seq));
                    continue;
                };
                configured = Some(c.device.clone());
                let _ = tx.send(Cmd::Configure(c, seq));
            }
            ToWorker::Shutdown => break,
            ToWorker::Cancel(c) => {
                for tx in senders.values() {
                    let _ = tx.send(Cmd::Cancel(c.job_id.clone()));
                }
            }
            ToWorker::Pause => {
                for tx in senders.values() {
                    let _ = tx.send(Cmd::Pause);
                }
            }
            ToWorker::Resume => {
                for tx in senders.values() {
                    let _ = tx.send(Cmd::Resume);
                }
            }
            ToWorker::Prepare(p) => {
                if let Some(tx) = configured.as_ref().and_then(|d| senders.get(d)) {
                    let _ = tx.send(Cmd::Prepare(p.height, seq));
                }
            }
            ToWorker::Job(j) => {
                if let Some(tx) = configured.as_ref().and_then(|d| senders.get(d)) {
                    let _ = tx.send(Cmd::Job(j));
                } else {
                    out.emit(&FromWorker::Fault(Fault { code: "not_configured".into(), message: "job before configure".into(), recoverable: true }), Some(seq));
                }
            }
            ToWorker::SetText(s) => {
                // a text knob (the programme anchor): this worker draws no programme; acknowledged by name
                out.emit(&FromWorker::Knob(KnobAck { knob: s.knob, value: 0.0 }), Some(seq));
            }
            ToWorker::Set(s) => {
                if let Some(tx) = configured.as_ref().and_then(|d| senders.get(d)) {
                    let _ = tx.send(Cmd::Set(s.knob, s.value, seq));
                } else {
                    out.emit(&FromWorker::Fault(Fault { code: "not_configured".into(), message: "set before configure".into(), recoverable: true }), Some(seq));
                }
            }
        }
    }
    for tx in senders.values() {
        let _ = tx.send(Cmd::Shutdown);
    }
    for h in handles {
        let _ = h.join();
    }
    out.emit(&FromWorker::Bye, None);
}
