//! igneum-worker-cpu: worker protocol v0 over the shared reference (`pow/ref`, `ignpow.h`). It carries every
//! candidate the reference build carries, with the reference's own vectors checked at start, and mines on the
//! CPU: the dataset build is the `preparing` state (`phase` dataset or transition), cancellation is checked every
//! 256 nonces, the nonce range is covered exactly in batches shared between threads. It is the end-to-end path
//! for a dev chain and the oracle's worker; no CPU mining claim follows from it.
//!
//! The holding rule (F-215's class in the worker, 16 October 2026: 36.9 GB resident on a build box, three full datasets
//! at once): the worker holds at most two datasets, the one it mines and the next epoch's prepared one. A job on a
//! new epoch with no prepared set drops the old context before the new build (its last job is stopped first, nothing
//! reads it); a prepared set for a document other than the configured one is dropped at the next job (a build that
//! was in flight when the document changed); every change of the holding is said with its size.

use igneum_pow_sys as sys;
use igneum_worker_proto as proto;
use proto::{AlgoEntry, Budget, Fault, FromWorker, Hello, HelloDevice, Knob, KnobAck, Params, Prepared, Ready, Refuse, Share, Stat, Telemetry, ToWorker, WorkerState, PROTO_VERSION};
use sha2::{Digest, Sha256};
use std::ffi::{CStr, CString};
use std::io::{BufRead, Write};
use std::sync::atomic::{AtomicBool, AtomicU64, Ordering};
use std::sync::{Arc, Mutex};
use std::thread;
use std::time::{Duration, Instant};

const VERSION: &str = env!("CARGO_PKG_VERSION");

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

/// A reference context; read-only after creation, shared by the hashing threads.
struct Ctx(*mut sys::ignpow_ctx);
unsafe impl Send for Ctx {}
unsafe impl Sync for Ctx {}
impl Drop for Ctx {
    fn drop(&mut self) {
        unsafe { sys::ignpow_ctx_free(self.0) }
    }
}

fn cstr(p: *const std::os::raw::c_char) -> String {
    if p.is_null() {
        String::new()
    } else {
        unsafe { CStr::from_ptr(p) }.to_string_lossy().into_owned()
    }
}

fn algos() -> Vec<AlgoEntry> {
    let mut v = Vec::new();
    unsafe {
        for i in 0..sys::ignpow_algo_count() {
            let id = cstr(sys::ignpow_algo_id(i));
            if id.is_empty() {
                continue;
            }
            let c = CString::new(id.clone()).unwrap();
            let mut n = 0usize;
            let ok = sys::ignpow_vectors_check(c.as_ptr(), &mut n) == sys::IGNPOW_OK;
            eprintln!("cpu worker: {id}: {n} vectors {}", if ok { "pass" } else { "FAIL" });
            v.push(AlgoEntry { id, vectors_ok: ok });
        }
    }
    v
}

fn mem_total() -> u64 {
    if let Ok(s) = std::fs::read_to_string("/proc/meminfo") {
        for l in s.lines() {
            if let Some(rest) = l.strip_prefix("MemTotal:") {
                if let Some(kb) = rest.split_whitespace().next().and_then(|x| x.parse::<u64>().ok()) {
                    return kb * 1024;
                }
            }
        }
    }
    0
}

fn ncpu() -> usize {
    thread::available_parallelism().map(|n| n.get()).unwrap_or(1)
}

fn knobs() -> Vec<Knob> {
    let n = ncpu() as f64;
    vec![Knob::additive("threads", 1.0, n, 1.0, (n / 2.0).floor().max(1.0), "threads"), Knob::doubling("batch_nonces", 4096.0, 1048576.0, 65536.0, "nonces")]
}

struct Running {
    cancel: Arc<AtomicBool>,
    handle: thread::JoinHandle<()>,
    job_id: String,
}

struct State {
    algo: Option<String>,
    algo_c: Option<CString>,
    /// The candidate's parameter document from `configure.params`, passed to the reference verbatim; None is the
    /// reference's defaults (SPEC-R0A-v0.md revision 0.3 section 0.1: absent members take the defaults).
    params_c: Option<CString>,
    ctx: Option<Arc<Ctx>>,
    epoch: Option<u64>,
    /// The next epoch's context, built in the background by `prepare` (the warm path); swapped in on the first
    /// job of that epoch while the old context stays immutable until its last job is stopped.
    next: Arc<Mutex<Option<(u64, Arc<Ctx>, Option<String>)>>>,
    preparing_next: Option<thread::JoinHandle<()>>,
    params: Params,
    /// The configured document carries the bonded rounds (features bit 10 with keyed_rounds): every job needs `sk`.
    keyed: bool,
    stat_ms: u64,
    paused: Arc<AtomicBool>,
    hashes: Arc<AtomicU64>,
    preparing: Arc<Mutex<String>>,
    running: Option<Running>,
}

fn params_ptr(p: &Option<CString>) -> *const std::os::raw::c_char {
    p.as_ref().map_or(std::ptr::null(), |c| c.as_ptr())
}

fn epoch_of(algo_c: &CString, params: &Option<CString>, height: u64) -> Result<u64, String> {
    let mut st = 0;
    let e = unsafe { sys::ignpow_epoch_of(algo_c.as_ptr(), params_ptr(params), height, &mut st) };
    if st != sys::IGNPOW_OK {
        return Err(sys::status_name(st).into());
    }
    Ok(e)
}

fn dataset_bytes(algo_c: &CString, params: &Option<CString>, height: u64) -> u64 {
    let mut st = 0;
    unsafe { sys::ignpow_dataset_bytes(algo_c.as_ptr(), params_ptr(params), height, &mut st) }
}

/// What a full context holds: the per-epoch cache (a real cache under IgneumHash version 1) plus the dataset.
fn resident_bytes(algo_c: &CString, params: &Option<CString>, height: u64) -> u64 {
    let mut st = 0;
    let cache = unsafe { sys::ignpow_cache_bytes(algo_c.as_ptr(), params_ptr(params), height, &mut st) };
    cache.saturating_add(dataset_bytes(algo_c, params, height))
}

fn mine(ctx: Arc<Ctx>, job: proto::Job, threads: usize, batch: u64, cancel: Arc<AtomicBool>, paused: Arc<AtomicBool>, hashes: Arc<AtomicU64>, out: Arc<Out>) {
    let header = proto::hex_to_32(&job.header).unwrap_or([0; 32]);
    // the keyed document (SPEC 0.9, bit 10): the job's secret, folded into every attempt by the reference's prover
    // path; it lives in this thread's stack and the job value, nowhere else, and is zeroed with the job
    let sk: Option<Arc<[u8; 32]>> = job.sk.as_ref().and_then(|s| s.to_bytes()).map(Arc::new);
    let target = proto::hex_to_32(&job.share_target).unwrap_or([0xff; 32]);
    let end = job.nonce_start.saturating_add(job.nonce_count);
    let next = Arc::new(AtomicU64::new(job.nonce_start));
    let mut hs = Vec::new();
    for _ in 0..threads.max(1) {
        let (ctx, next, cancel, paused, hashes, out, job_id, sk) = (ctx.clone(), next.clone(), cancel.clone(), paused.clone(), hashes.clone(), out.clone(), job.job_id.clone(), sk.clone());
        hs.push(thread::spawn(move || {
            let mut mix = [0u8; 32];
            let mut hash = [0u8; 32];
            let mut shares = vec![0u8; 64 * 32];
            loop {
                if cancel.load(Ordering::Relaxed) {
                    return;
                }
                while paused.load(Ordering::Relaxed) {
                    if cancel.load(Ordering::Relaxed) {
                        return;
                    }
                    thread::sleep(Duration::from_millis(50));
                }
                // claim a batch; the last batch is the tail, never skipped
                let start = next.fetch_add(batch, Ordering::Relaxed);
                if start >= end {
                    return;
                }
                let stop = start.saturating_add(batch).min(end);
                let mut n = start;
                while n < stop {
                    let st = match sk.as_ref() {
                        Some(k) => unsafe { sys::ignh_hash_keyed(ctx.0, job.height, header.as_ptr(), n, k.as_ptr(), shares.as_mut_ptr(), mix.as_mut_ptr(), hash.as_mut_ptr()) },
                        None => unsafe { sys::ignpow_hash(ctx.0, job.height, header.as_ptr(), n, mix.as_mut_ptr(), hash.as_mut_ptr()) },
                    };
                    if st == sys::IGNPOW_OK && hash <= target {
                        out.emit(&FromWorker::Share(Share { job_id: job_id.clone(), nonce: n, mix: proto::hex_encode(&mix), hash: proto::hex_encode(&hash) }), None);
                    }
                    n += 1;
                    if n % 256 == 0 {
                        hashes.fetch_add(256, Ordering::Relaxed);
                        if cancel.load(Ordering::Relaxed) {
                            return;
                        }
                    }
                }
                hashes.fetch_add(stop.saturating_sub(start) % 256, Ordering::Relaxed);
            }
        }));
    }
    for h in hs {
        let _ = h.join();
    }
}

/// The holding said: how many datasets the worker holds (the mined one and the prepared next) and their size, the
/// worker's own statement of F-215's bound (at most two).
fn say_holding(state: &State, algo_c: &CString, params_c: &Option<CString>, height: u64) {
    let next = state.next.lock().map(|g| g.is_some()).unwrap_or(false);
    let n = state.ctx.is_some() as u64 + next as u64;
    let each = resident_bytes(algo_c, params_c, height);
    eprintln!("cpu worker: holding {n} dataset{} ({:.1} GB; the bound is two: the mined epoch's and the prepared next)", if n == 1 { "" } else { "s" }, (n * each) as f64 / 1e9);
}

fn stop_running(state: &mut State) {
    if let Some(r) = state.running.take() {
        r.cancel.store(true, Ordering::Relaxed);
        let _ = r.handle.join();
        eprintln!("cpu worker: job {} stopped", r.job_id);
    }
}

fn main() {
    let out = Arc::new(Out { stdout: Mutex::new((std::io::stdout(), proto::Seq::default())) });
    let algos = algos();
    let build = unsafe { cstr(sys::ignpow_build_string()) };
    let name = format!("CPU, {} threads", ncpu());
    let mut h = Sha256::new();
    for part in ["cpu", &name, std::env::consts::ARCH] {
        h.update(part.as_bytes());
        h.update([0u8]);
    }
    let device = HelloDevice {
        id: "cpu:0".into(),
        name,
        memory_bytes: mem_total(),
        arch: std::env::consts::ARCH.into(), arch_detail: None,
        driver: String::new(),
        runtime: build.chars().take(60).collect(),
        vendor: Some("cpu".into()),
        kind: Some("cpu".into()),
        bus_id: Some(format!("cpu:{}", &proto::hex_encode(&h.finalize())[..12])),
        intrinsic_id: None,
        identity: None,
        slowdown_c: None,
        knobs: knobs(),
    };
    out.emit(&FromWorker::Hello(Hello { proto: PROTO_VERSION, worker: "igneum-worker-cpu".into(), version: VERSION.into(), backend: "cpu".into(), algos: algos.clone(), devices: vec![device], note: "the shared reference on the processor: for tests and dev chains, not a mining target".into(), extensions: vec![igneum_worker_proto::EXT_KEYED_JOB.into()] }), None);

    // a terminal's Ctrl-C reaches the whole process group: the supervisor handles it and ends this worker through
    // the protocol (shutdown, or the pipe closing); the worker itself ignores the signal
    #[cfg(unix)]
    unsafe {
        libc::signal(libc::SIGINT, libc::SIG_IGN);
    }
    let mut state = State { algo: None, algo_c: None, params_c: None, ctx: None, epoch: None, next: Arc::new(Mutex::new(None)), preparing_next: None, params: Params::new(), keyed: false, stat_ms: 1000, paused: Arc::new(AtomicBool::new(false)), hashes: Arc::new(AtomicU64::new(0)), preparing: Arc::new(Mutex::new(String::new())), running: None };
    for k in knobs() {
        state.params.insert(k.name, k.default);
    }

    // the stat thread: the heartbeat, with the state and phase
    let stat_out = out.clone();
    let stat_hashes = state.hashes.clone();
    let stat_prep = state.preparing.clone();
    let stat_paused = state.paused.clone();
    let stat_info: Arc<Mutex<(Option<u64>, String, bool, u64)>> = Arc::new(Mutex::new((None, String::new(), false, 1000)));
    let info = stat_info.clone();
    thread::spawn(move || loop {
        let ms = info.lock().map(|g| g.3).unwrap_or(1000);
        thread::sleep(Duration::from_millis(ms.clamp(100, 60_000)));
        // read the job after the sleep, so a stat never names a job a period old
        let (epoch, job_id, running, _) = info.lock().map(|g| g.clone()).unwrap_or((None, String::new(), false, 1000));
        let phase = stat_prep.lock().map(|p| p.clone()).unwrap_or_default();
        let hashes = stat_hashes.swap(0, Ordering::Relaxed);
        let st = if !phase.is_empty() {
            WorkerState::Preparing
        } else if stat_paused.load(Ordering::Relaxed) {
            WorkerState::Paused
        } else if running {
            WorkerState::Mining
        } else {
            WorkerState::Idle
        };
        stat_out.emit(&FromWorker::Stat(Stat { job_id: job_id.clone(), hashes, elapsed_ms: ms, telemetry: Telemetry::default(), state: st, phase, epoch, building: None }), None);
    });

    let stdin = std::io::stdin();
    let mut lines = stdin.lock().lines();
    let mut first = true;
    while let Some(Ok(line)) = lines.next() {
        let env: proto::Envelope<ToWorker> = match proto::decode(&line) {
            Ok(m) => m,
            Err(e) => {
                if first {
                    std::process::exit(proto::EXIT_BAD_FIRST_LINE);
                }
                out.emit(&FromWorker::Fault(Fault { code: "protocol".into(), message: e.to_string().chars().take(200).collect(), recoverable: true }), None);
                continue;
            }
        };
        if let Err(e) = proto::validate_to_worker(&env.body) {
            out.emit(&FromWorker::Fault(Fault { code: "protocol".into(), message: e.to_string().chars().take(200).collect(), recoverable: true }), Some(env.seq));
            continue;
        }
        if first {
            first = false;
            if !matches!(env.body, ToWorker::Configure(_)) {
                std::process::exit(proto::EXIT_BAD_FIRST_LINE);
            }
        }
        let seq = env.seq;
        match env.body {
            ToWorker::Configure(c) => {
                if c.device != "cpu:0" {
                    out.emit(&FromWorker::Refuse(Refuse { device: c.device.clone(), reason: "no such device".into(), term: Some("device".into()), need_bytes: None, have_bytes: None }), Some(seq));
                    continue;
                }
                match algos.iter().find(|a| a.id == c.algo) {
                    None => {
                        out.emit(&FromWorker::Refuse(Refuse { device: c.device.clone(), reason: format!("this reference build carries {}", algos.iter().map(|a| a.id.clone()).collect::<Vec<_>>().join(", ")), term: Some("algo".into()), need_bytes: None, have_bytes: None }), Some(seq));
                        continue;
                    }
                    Some(a) if !a.vectors_ok => {
                        out.emit(&FromWorker::Refuse(Refuse { device: c.device.clone(), reason: format!("the reference's vectors for {} failed at start", c.algo), term: Some("vectors".into()), need_bytes: None, have_bytes: None }), Some(seq));
                        continue;
                    }
                    Some(_) => {}
                }
                stop_running(&mut state);
                state.ctx = None;
                state.epoch = None;
                let new_params: Option<String> = if c.params.is_null() { None } else { Some(c.params.to_string()) };
                if let Ok(mut g) = state.next.lock() {
                    // a dataset prepared for exactly this document (an announced change) survives the configure:
                    // the switch is warm; anything else goes
                    let keep = g.as_ref().map_or(false, |(_, _, doc)| state.algo.as_deref() == Some(c.algo.as_str()) && *doc == new_params);
                    if !keep {
                        *g = None;
                    } else {
                        eprintln!("cpu worker: the dataset prepared for the new parameter document is kept across the configure");
                    }
                }
                state.algo_c = Some(CString::new(c.algo.clone()).unwrap());
                state.algo = Some(c.algo.clone());
                state.params_c = new_params.as_deref().and_then(|t| CString::new(t).ok());
                // the bonded rounds (SPEC 0.9, bit 10 with keyed_rounds): every job under this document needs sk
                state.keyed = new_params.as_deref().and_then(|t| serde_json::from_str::<serde_json::Value>(t).ok()).map_or(false, |d| d.get("features").and_then(|f| f.as_u64()).unwrap_or(0) & 1024 != 0 && d.get("keyed_rounds").and_then(|k| k.as_u64()).unwrap_or(0) >= 1);
                // the document must be one the reference accepts, before any dataset is built on it
                {
                    let mut st = 0;
                    let _ = unsafe { sys::ignpow_epoch_of(state.algo_c.as_ref().unwrap().as_ptr(), params_ptr(&state.params_c), 1, &mut st) };
                    if st != sys::IGNPOW_OK {
                        out.emit(&FromWorker::Refuse(Refuse { device: c.device.clone(), reason: format!("the reference rejects the parameter document: {}", sys::status_name(st)), term: Some("params".into()), need_bytes: None, have_bytes: None }), Some(seq));
                        state.algo = None;
                        state.algo_c = None;
                        state.params_c = None;
                        continue;
                    }
                }
                state.stat_ms = c.stat_ms;
                if let Ok(mut g) = stat_info.lock() {
                    g.3 = c.stat_ms;
                }
                let total = mem_total();
                let ds = resident_bytes(state.algo_c.as_ref().unwrap(), &state.params_c, 1);
                out.emit(&FromWorker::Ready(Ready { device: c.device.clone(), algo: c.algo.clone(), budget: Budget { total_bytes: total, free_bytes: total.saturating_sub(c.memory_margin_bytes), fit_cold_bytes: total.saturating_sub(c.memory_margin_bytes).saturating_sub(ds), fit_warm_bytes: total.saturating_sub(c.memory_margin_bytes).saturating_sub(2 * ds), can_warm: total > 2 * ds + c.memory_margin_bytes } }), Some(seq));
            }
            ToWorker::Job(j) => {
                let (Some(algo), Some(algo_c)) = (state.algo.clone(), state.algo_c.clone()) else {
                    out.emit(&FromWorker::Fault(Fault { code: "not_configured".into(), message: "job before configure".into(), recoverable: true }), Some(seq));
                    continue;
                };
                if !j.algo.is_empty() && j.algo != algo {
                    out.emit(&FromWorker::Fault(Fault { code: "algo".into(), message: format!("job for {}, configured for {algo}", j.algo), recoverable: true }), Some(seq));
                    continue;
                }
                let params_c = state.params_c.clone();
                if state.keyed && j.sk.is_none() {
                    out.emit(&FromWorker::Fault(Fault { code: "keyed".into(), message: "the document carries the bonded rounds and the job carries no secret: nothing can be hashed".into(), recoverable: true }), Some(seq));
                    continue;
                }
                let epoch = match epoch_of(&algo_c, &params_c, j.height) {
                    Ok(e) => e,
                    Err(e) => {
                        out.emit(&FromWorker::Fault(Fault { code: "height".into(), message: e, recoverable: true }), Some(seq));
                        continue;
                    }
                };
                // a new job replaces the old one (clean or not: the supervisor sends one job per device at a time)
                stop_running(&mut state);
                {
                    // a set prepared for another document (a build in flight when the document changed) is dropped here,
                    // the first job after it: never held beside the mined one
                    let cur_doc: Option<String> = state.params_c.as_ref().map(|c| c.to_string_lossy().into_owned());
                    if let Ok(mut g) = state.next.lock() {
                        if g.as_ref().map_or(false, |(_, _, doc)| *doc != cur_doc) {
                            let e = g.as_ref().map(|(e, _, _)| *e).unwrap_or(0);
                            *g = None;
                            eprintln!("cpu worker: the dataset prepared for epoch {e} under another parameter document is dropped");
                        }
                    }
                }
                if state.epoch != Some(epoch) {
                    // the warm path: the prepared context for this epoch, if `prepare` built it
                    let cur_doc: Option<String> = state.params_c.as_ref().map(|c| c.to_string_lossy().into_owned());
                    let prepared = state.next.lock().ok().and_then(|mut g| if g.as_ref().map_or(false, |(e, _, doc)| *e == epoch && *doc == cur_doc) { g.take() } else { None });
                    if let Some((e, c, _)) = prepared {
                        state.ctx = Some(c);
                        state.epoch = Some(e);
                        eprintln!("cpu worker: epoch {e}: the prepared dataset swapped in (warm)");
                        say_holding(&state, &algo_c, &params_c, j.height);
                    }
                }
                if state.epoch != Some(epoch) || state.ctx.is_none() {
                    let phase = if state.ctx.is_some() { "transition" } else { "dataset" };
                    if let Some(old) = state.epoch.filter(|_| state.ctx.is_some()) {
                        // the old epoch's context goes before the new build (its last job is stopped above): one
                        // dataset resident through the transition, never two
                        state.ctx = None;
                        state.epoch = None;
                        eprintln!("cpu worker: epoch {old}: its dataset released before the build for epoch {epoch}");
                        say_holding(&state, &algo_c, &params_c, j.height);
                    }
                    if let Ok(mut p) = state.preparing.lock() {
                        *p = phase.into();
                    }
                    let t0 = Instant::now();
                    let mut st = 0;
                    let p = unsafe { sys::ignpow_ctx_create(algo_c.as_ptr(), params_ptr(&params_c), j.height, 1, &mut st) };
                    if let Ok(mut g) = state.preparing.lock() {
                        g.clear();
                    }
                    if p.is_null() || st != sys::IGNPOW_OK {
                        out.emit(&FromWorker::Refuse(Refuse { device: "cpu:0".into(), reason: format!("the dataset for epoch {epoch} could not be built: {}", sys::status_name(st)), term: Some("fit_cold_bytes".into()), need_bytes: Some(dataset_bytes(&algo_c, &params_c, j.height)), have_bytes: Some(mem_total()) }), Some(seq));
                        continue;
                    }
                    state.ctx = Some(Arc::new(Ctx(p)));
                    state.epoch = Some(epoch);
                    out.emit(&FromWorker::Prepared(Prepared { epoch, ms: t0.elapsed().as_millis() as u64, bytes: resident_bytes(&algo_c, &params_c, j.height), mode: "cold".into(), digest: None }), None);
                    say_holding(&state, &algo_c, &params_c, j.height);
                }
                let ctx = state.ctx.clone().unwrap();
                let threads = *state.params.get("threads").unwrap_or(&1.0) as usize;
                let batch = *state.params.get("batch_nonces").unwrap_or(&65536.0) as u64;
                let cancel = Arc::new(AtomicBool::new(false));
                let (c2, paused, hashes, o2, job_id) = (cancel.clone(), state.paused.clone(), state.hashes.clone(), out.clone(), j.job_id.clone());
                let info = stat_info.clone();
                if let Ok(mut g) = info.lock() {
                    g.0 = Some(epoch);
                    g.1 = job_id.clone();
                    g.2 = true;
                }
                let info2 = stat_info.clone();
                let jid = job_id.clone();
                let handle = thread::spawn(move || {
                    mine(ctx, j, threads, batch, c2, paused, hashes, o2);
                    if let Ok(mut g) = info2.lock() {
                        if g.1 == jid {
                            g.2 = false;
                        }
                    }
                });
                // a stat the moment the new job is mining: the switch delay is measured from this, not from the
                // next tick of the stat clock
                out.emit(&FromWorker::Stat(Stat { job_id: job_id.clone(), hashes: 0, elapsed_ms: 0, telemetry: Telemetry::default(), state: WorkerState::Mining, phase: String::new(), epoch: Some(epoch), building: None }), None);
                state.running = Some(Running { cancel, handle, job_id });
            }
            ToWorker::Cancel(c) => {
                if state.running.as_ref().map_or(false, |r| r.job_id == c.job_id) {
                    stop_running(&mut state);
                    if let Ok(mut g) = stat_info.lock() {
                        g.2 = false;
                    }
                }
            }
            ToWorker::Pause => state.paused.store(true, Ordering::Relaxed),
            ToWorker::Resume => state.paused.store(false, Ordering::Relaxed),
            ToWorker::Prepare(p) => {
                if let Some(algo_c) = state.algo_c.clone() {
                    // the document the dataset is for: the announced one when the supervisor names it, else the
                    // configured one
                    let doc_text: Option<String> = match &p.params {
                        Some(v) if !v.is_null() => Some(v.to_string()),
                        _ => state.params_c.as_ref().map(|c| c.to_string_lossy().into_owned()),
                    };
                    let params_c: Option<CString> = doc_text.as_deref().and_then(|t| CString::new(t).ok());
                    let same_doc = doc_text == state.params_c.as_ref().map(|c| c.to_string_lossy().into_owned());
                    match epoch_of(&algo_c, &params_c, p.height) {
                        Ok(e) => {
                            let already = (same_doc && state.epoch == Some(e)) || state.next.lock().map(|g| g.as_ref().map_or(false, |(x, _, d)| *x == e && *d == doc_text)).unwrap_or(false);
                            if already || state.preparing_next.as_ref().map_or(false, |h| !h.is_finished()) {
                                // held already, or a build in flight: nothing to do
                            } else {
                                // the warm path: build the next epoch's context beside the current one, within the
                                // budget the supervisor granted (it asks only when can_warm)
                                let (o2, next, prep, height, doc_for) = (out.clone(), state.next.clone(), state.preparing.clone(), p.height, doc_text.clone());
                                let bytes = resident_bytes(&algo_c, &params_c, p.height);
                                state.preparing_next = Some(thread::spawn(move || {
                                    // the build beside mining, the CPU's form of the GPU team's sliced build: this thread and
                                    // the reference's build threads (which inherit it on Linux and macOS) run at the
                                    // lowest priority, so the mining threads keep the cores and the rate never drops
                                    // to zero while the next dataset is built; the build takes what mining leaves
                                    #[cfg(unix)]
                                    unsafe {
                                        libc::setpriority(libc::PRIO_PROCESS, 0, 19);
                                    }
                                    let t0 = Instant::now();
                                    let mut st = 0;
                                    let c = unsafe { sys::ignpow_ctx_create(algo_c.as_ptr(), params_ptr(&params_c), height, 1, &mut st) };
                                    if c.is_null() || st != sys::IGNPOW_OK {
                                        o2.emit(&FromWorker::Refuse(Refuse { device: "cpu:0".into(), reason: format!("the next epoch's dataset could not be built: {}", sys::status_name(st)), term: Some("fit_warm_bytes".into()), need_bytes: Some(bytes), have_bytes: Some(mem_total()) }), Some(seq));
                                        return;
                                    }
                                    if let Ok(mut g) = next.lock() {
                                        *g = Some((e, Arc::new(Ctx(c)), doc_for));
                                    }
                                    let _ = prep;
                                    o2.emit(&FromWorker::Prepared(Prepared { epoch: e, ms: t0.elapsed().as_millis() as u64, bytes, mode: "warm".into(), digest: None }), Some(seq));
                                }));
                            }
                        }
                        Err(e) => out.emit(&FromWorker::Refuse(Refuse { device: "cpu:0".into(), reason: e, term: Some("height".into()), need_bytes: None, have_bytes: None }), Some(seq)),
                    }
                }
            }
            ToWorker::SetText(s) => {
                // a text knob (the programme anchor): the CPU worker draws no programme; acknowledged by name
                out.emit(&FromWorker::Knob(KnobAck { knob: s.knob, value: 0.0 }), Some(seq));
            }
            ToWorker::Set(s) => match knobs().into_iter().find(|k| k.name == s.knob) {
                Some(k) => {
                    let v = k.clamp(s.value);
                    state.params.insert(s.knob.clone(), v);
                    out.emit(&FromWorker::Knob(KnobAck { knob: s.knob, value: v }), Some(seq));
                }
                None => out.emit(&FromWorker::Fault(Fault { code: "knob".into(), message: format!("unknown knob {}", s.knob), recoverable: true }), Some(seq)),
            },
            ToWorker::Shutdown => break,
        }
    }
    stop_running(&mut state);
    state.ctx = None;
    out.emit(&FromWorker::Bye, None);
}
