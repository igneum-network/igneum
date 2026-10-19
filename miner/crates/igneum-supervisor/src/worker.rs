//! The worker process manager (design section 1.2): launch from the app's own resources only, hash-checked,
//! clean environment, one child per device, protocol v0 over the pipes (seq on every line out), kills by
//! recorded pid after reading the command line.

use igneum_worker_proto::{self as proto, Envelope, FromWorker, Hello, Seq, ToWorker, PROTO_VERSION};
use sha2::{Digest, Sha256};
use std::collections::HashMap;
use std::io::{BufRead, BufReader, Read, Write};
use std::path::{Path, PathBuf};
use std::process::{Child, Command, Stdio};
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::mpsc::{self, Receiver, Sender, SyncSender, TrySendError};
use std::sync::Arc;
use std::thread;
use std::time::{Duration, Instant};

#[derive(Debug, thiserror::Error)]
pub enum WorkerError {
    #[error("the worker {0} is not in the app's resources")]
    NotFound(PathBuf),
    #[error("the worker {path} does not match the release manifest (expected {expected}, found {actual}); not launched")]
    HashMismatch { path: PathBuf, expected: String, actual: String },
    #[error("the worker {0} has no manifest hash; this build refuses unchecked workers")]
    Unchecked(PathBuf),
    #[error("could not start the worker: {0}")]
    Spawn(std::io::Error),
    #[error("handshake: {0}")]
    Handshake(String),
    #[error("protocol: {0}")]
    Proto(#[from] proto::ProtoError),
    #[error("io: {0}")]
    Io(#[from] std::io::Error),
    #[error("the worker has gone")]
    Gone,
    #[error("the recorded pid {pid} is running {found}, not {expected}; not killed")]
    PidMismatch { pid: u32, found: String, expected: String },
}

/// What arrives from a worker's pipes.
#[derive(Debug)]
pub enum Inbound {
    /// A message and the instant its line arrived (the switch delay is measured from arrival, not from the tick).
    Msg(Envelope<FromWorker>, Instant),
    /// A stderr line (free text for the log, never parsed for decisions).
    Log(String),
    /// A malformed stdout line: the supervisor restarts the worker.
    Fault(String),
    Exited,
}

/// How to launch one worker executable.
#[derive(Debug, Clone)]
pub struct Launcher {
    pub exe: PathBuf,
    pub expected_sha256: Option<String>,
    pub workdir: PathBuf,
    /// Environment the worker needs (the backend's runtime variables from its manifest), beside a minimal PATH.
    pub env: Vec<(String, String)>,
    pub args: Vec<String>,
}

pub struct WorkerProcess {
    pub pid: u32,
    pub exe: PathBuf,
    pub hello: Hello,
    pub started: Instant,
    child: Child,
    tx: Sender<String>,
    rx: Receiver<Inbound>,
    /// Results dropped at a full inbound queue since the last read.
    dropped: Arc<AtomicU64>,
    seq: Seq,
}

/// F-E36: the OpenCL worker's device walk (`igneum-worker-opencl telemetry`, the GPU team's form of 14 October 2026): one
/// JSON line {"telemetry":[{"index", "bus_id", "name", "vendor", ...}, ...]} in the ICD loader's enumeration order,
/// every platform included. Returns (index, vendor, name) per device; Err with the words when the walk does not run
/// or prints no such line within 25 s.
pub fn opencl_device_indices(exe: &Path) -> Result<Vec<(u32, String, String)>, String> {
    let mut cmd = Command::new(exe);
    cmd.arg("telemetry").env_clear().env("PATH", minimal_path());
    for k in PASSTHROUGH_ENV {
        if let Ok(v) = std::env::var(k) {
            cmd.env(k, v);
        }
    }
    rusticl_default(&mut cmd, exe);
    let mut child = cmd.stdin(Stdio::null()).stdout(Stdio::piped()).stderr(Stdio::piped()).spawn().map_err(|e| format!("{} telemetry did not start: {e}", exe.display()))?;
    let deadline = Instant::now() + Duration::from_secs(25);
    loop {
        match child.try_wait() {
            Ok(Some(_)) => break,
            Ok(None) if Instant::now() < deadline => thread::sleep(Duration::from_millis(100)),
            Ok(None) => {
                let _ = child.kill();
                return Err("the telemetry walk did not end in 25 s; killed".into());
            }
            Err(e) => return Err(format!("the telemetry walk: {e}")),
        }
    }
    let out = child.wait_with_output().map_err(|e| e.to_string())?;
    let text = String::from_utf8_lossy(&out.stdout);
    let line = text.lines().find(|l| l.trim_start().starts_with('{') && l.contains("\"telemetry\"")).ok_or_else(|| {
        let err = String::from_utf8_lossy(&out.stderr);
        format!("no telemetry line in the walk's output ({})", err.lines().last().unwrap_or("no words").chars().take(160).collect::<String>())
    })?;
    parse_telemetry_line(line)
}

/// The telemetry line's devices as (index, vendor, name).
pub fn parse_telemetry_line(line: &str) -> Result<Vec<(u32, String, String)>, String> {
    let v: serde_json::Value = serde_json::from_str(line.trim()).map_err(|e| format!("the telemetry line is not JSON: {e}"))?;
    let arr = v.get("telemetry").and_then(|t| t.as_array()).ok_or("the telemetry line carries no array")?;
    let mut out_list = Vec::new();
    for (n, d) in arr.iter().enumerate() {
        let idx = d.get("index").and_then(|x| x.as_u64()).unwrap_or(n as u64) as u32;
        let vendor = d.get("vendor").and_then(|x| x.as_str()).unwrap_or("").to_string();
        let name = d.get("name").and_then(|x| x.as_str()).unwrap_or("").to_string();
        out_list.push((idx, vendor, name));
    }
    Ok(out_list)
}

pub fn sha256_file(path: &Path) -> std::io::Result<String> {
    let mut f = std::fs::File::open(path)?;
    let mut h = Sha256::new();
    let mut buf = [0u8; 1 << 16];
    loop {
        let n = f.read(&mut buf)?;
        if n == 0 {
            break;
        }
        h.update(&buf[..n]);
    }
    Ok(proto::hex_encode(&h.finalize()))
}

/// `workers.sha256`: one `<hex>  <file name>` per line, written by the release build.
pub fn read_manifest(dir: &Path) -> HashMap<String, String> {
    let mut m = HashMap::new();
    if let Ok(s) = std::fs::read_to_string(dir.join("workers.sha256")) {
        for line in s.lines() {
            let mut it = line.split_whitespace();
            if let (Some(hash), Some(name)) = (it.next(), it.next()) {
                if proto::is_hex(hash, 64) {
                    m.insert(name.to_string(), hash.to_string());
                }
            }
        }
    }
    m
}

fn is_executable(p: &Path) -> bool {
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        return std::fs::metadata(p).map(|m| m.is_file() && m.permissions().mode() & 0o111 != 0).unwrap_or(false);
    }
    #[cfg(not(unix))]
    {
        p.is_file() && p.extension().map_or(false, |e| e == "exe")
    }
}

/// Every `igneum-worker-*` executable in the resources directory, with its manifest hash when the manifest
/// names it. Workers without a manifest entry launch only when `IGNEUM_DEV_UNCHECKED_WORKERS=1`.
/// The worker files discovery skipped because workers.sha256 does not name them (read and cleared by the caller).
static IGNORED: std::sync::Mutex<Vec<PathBuf>> = std::sync::Mutex::new(Vec::new());
pub fn take_ignored() -> Vec<PathBuf> {
    IGNORED.lock().map(|mut g| std::mem::take(&mut *g)).unwrap_or_default()
}

pub fn discover(resources_dir: &Path, workdir_root: &Path) -> Vec<Launcher> {
    // the worker runs with its own work directory as its cwd, so a relative resources path (a user's
    // `--workers-dir ./workers`, the mirror box's lab run, 11 October 2026: "could not start the worker: No such file or
    // directory" on a file that was there) is made absolute here, once, before any launch
    let abs = resources_dir.canonicalize().unwrap_or_else(|_| resources_dir.to_path_buf());
    let resources_dir = abs.as_path();
    let manifest = read_manifest(resources_dir);
    let mut out = Vec::new();
    let Ok(rd) = std::fs::read_dir(resources_dir) else { return out };
    let mut names: Vec<PathBuf> = rd.filter_map(|e| e.ok().map(|e| e.path())).collect();
    names.sort();
    for p in names {
        let Some(name) = p.file_name().and_then(|n| n.to_str()) else { continue };
        let stem = name.strip_suffix(".exe").unwrap_or(name);
        if !stem.starts_with("igneum-worker-") || stem.contains('.') || !is_executable(&p) {
            continue;
        }
        // the two-card Windows host under 0.2.16 (reliability's read of 15 October 2026): a leftover igneum-worker-opencl.exe from a 14 October
        // install sat beside the payload, the manifest did not name it, the engine launched it for its hello and then
        // refused it ("no manifest hash"), and the page raised "The worker ... did not start". A file the manifest
        // does not name is never launched: it is skipped here and named once in the log by the caller (`ignored`)
        if !manifest.is_empty() && !manifest.contains_key(name) {
            IGNORED.lock().map(|mut g| g.push(p.clone())).ok();
            continue;
        }
        // the Metal host declares one algorithm per launch, its --algo value (kawpow by default), and the supervisor
        // keeps only the algorithms a hello declares with passing vectors: launched bare it carried no igneumhash and
        // "no enabled worker carries the source's algorithm" stopped the Mac's first parity row (the mini,
        // 11 October 2026, pa-mini-parity-ember-macos-2); the candidate is named at launch until the host
        // declares every algorithm it carries (told to the Orchard lead)
        // the GL raster worker (the GPU team's protocol binary, 15 October 2026) is launched as `protocol --device 0`: the
        // --device is taken and ignored (the card is the EGL device the context opens); the CUDA and OpenCL workers
        // take no argument (the slot's device rides their environment)
        let args = if stem.starts_with("igneum-worker-metal") { vec!["--algo".to_string(), "igneumhash".to_string(), "protocol".to_string()] } else if stem.starts_with("igneum-worker-gl") { vec!["protocol".to_string(), "--device".to_string(), "0".to_string()] } else { Vec::new() };
        out.push(Launcher { exe: p.clone(), expected_sha256: manifest.get(name).cloned(), workdir: workdir_root.join(stem), env: Vec::new(), args });
    }
    out
}

/// The worker's inbound queue (messages read from its stdout, waiting for the supervisor's tick): bounded, because
/// a worker against a pool's easy share target reported results by the ten thousand a second and the tick drains
/// a fixed number per slot, so the queue grew without bound (the mini's Ember on the public pool, 12 October 2026:
/// 9.8 GB resident after 45 minutes, the card's 2.3 GB then unobtainable). A result that finds the queue full is
/// dropped and counted (`dropped_results`, folded into the unverified count); every other message waits its turn.
pub const INBOUND_QUEUE: usize = 4096;

/// A result offered to a bounded queue: sent, or dropped and counted when the queue is full (the sender's error
/// otherwise). The rule in one place for its test.
pub fn offer_result(tx: &SyncSender<Inbound>, m: Inbound, dropped: &AtomicU64) -> Result<(), ()> {
    match tx.try_send(m) {
        Ok(()) => Ok(()),
        Err(TrySendError::Full(_)) => {
            dropped.fetch_add(1, Ordering::Relaxed);
            Ok(())
        }
        Err(TrySendError::Disconnected(_)) => Err(()),
    }
}

/// How long a worker has to say hello after its launch: the CUDA worker's 227-vector reference self-test runs on
/// the CPU before it speaks, and a rented host's shared cores took 23 s for it with one other worker beside it
/// (an RTX 4090 pod on a 570 driver, 12 October 2026: thirteen restarts "exited before hello" under the thirty
/// seconds of the day, the card never at fault); two minutes leaves the slowest host its self-test.
pub const HELLO_WAIT: Duration = Duration::from_secs(120);
/// A worker that still speaks on stderr (a first-run compile, a dataset build) moves its hello deadline on each line,
/// up to this bound from its start (the E1 operability study, 12 October 2026: a 3060's first run compiled for
/// longer than the wait and Ember read "the worker said nothing for 120 s").
pub const HELLO_BOUND: Duration = Duration::from_secs(600);

/// Environment variables passed to a worker (everything else is cleared): the dynamic-loader paths, the GPU
/// runtimes' device selection and ICD variables, and the homes the drivers keep their compile caches in.
/// The Octominer rig's five AMD cards (16 October 2026): on Linux the OpenCL worker lists AMD cards through Mesa's rusticl, which
/// enumerates nothing unless RUSTICL_ENABLE names the driver; an environment that carries none gets `radeonsi` (the
/// AMD driver; a host that sets its own keeps it). Only the OpenCL worker, only on Linux.
pub fn rusticl_default(cmd: &mut Command, exe: &std::path::Path) {
    if !cfg!(target_os = "linux") {
        return;
    }
    let stem = exe.file_stem().and_then(|x| x.to_str()).unwrap_or("");
    if stem.starts_with("igneum-worker-opencl") && std::env::var_os("RUSTICL_ENABLE").is_none() {
        cmd.env("RUSTICL_ENABLE", "radeonsi");
    }
}

pub const PASSTHROUGH_ENV: &[&str] = &[
    "LD_LIBRARY_PATH",
    "DYLD_LIBRARY_PATH",
    "DYLD_FRAMEWORK_PATH",
    "CUDA_VISIBLE_DEVICES",
    "CUDA_DEVICE_ORDER",
    "CUDA_CACHE_PATH",
    "NVIDIA_VISIBLE_DEVICES",
    "HIP_VISIBLE_DEVICES",
    "ROCR_VISIBLE_DEVICES",
    "GPU_DEVICE_ORDINAL",
    "OCL_ICD_VENDORS",
    "OCL_ICD_FILENAMES",
    "OPENCL_VENDOR_PATH",
    // Mesa's rusticl lists no device unless RUSTICL_ENABLE names its driver (the Octominer rig's five AMD cards, 16 October 2026)
    "RUSTICL_ENABLE",
    "RUSTICL_DEVICE_TYPE",
    "HOME",
    "USERPROFILE",
    "LOCALAPPDATA",
    "APPDATA",
    "TMPDIR",
    "TEMP",
    "TMP",
    "XDG_CACHE_HOME",
    "SYSTEMROOT",
    "SystemRoot",
    "LANG",
    "LC_ALL",
];

fn minimal_path() -> &'static str {
    if cfg!(windows) {
        "C:\\Windows\\System32"
    } else {
        "/usr/bin:/bin"
    }
}

impl Launcher {
    pub fn check(&self) -> Result<(), WorkerError> {
        if !self.exe.is_file() {
            return Err(WorkerError::NotFound(self.exe.clone()));
        }
        match &self.expected_sha256 {
            Some(expected) => {
                let actual = sha256_file(&self.exe)?;
                if &actual != expected {
                    return Err(WorkerError::HashMismatch { path: self.exe.clone(), expected: expected.clone(), actual });
                }
                Ok(())
            }
            None => {
                if std::env::var("IGNEUM_DEV_UNCHECKED_WORKERS").map(|v| v == "1").unwrap_or(false) {
                    Ok(())
                } else {
                    Err(WorkerError::Unchecked(self.exe.clone()))
                }
            }
        }
    }

    /// Launch and wait for `hello`. The supervisor's first line (`configure`) is the caller's.
    pub fn launch(&self) -> Result<WorkerProcess, WorkerError> {
        self.check()?;
        std::fs::create_dir_all(&self.workdir)?;
        let mut cmd = Command::new(&self.exe);
        cmd.args(&self.args).env_clear().env("PATH", minimal_path()).current_dir(&self.workdir).stdin(Stdio::piped()).stdout(Stdio::piped()).stderr(Stdio::piped());
        // on Windows a console program spawned by a windowed app gets a console window of its own on the desktop
        // (the first real capture of the app window, 12 October 2026: the CUDA worker's black console beside
        // Ember's window); CREATE_NO_WINDOW keeps the worker's pipes and shows nothing
        #[cfg(windows)]
        {
            use std::os::windows::process::CommandExt;
            cmd.creation_flags(0x0800_0000);
        }
        // the environment is cleared except what a GPU runtime needs to find its driver and caches (a CUDA image
        // with its libraries under LD_LIBRARY_PATH, the device-selection variables, the driver's cache home); no
        // secret-bearing variable passes
        for k in PASSTHROUGH_ENV {
            if let Ok(v) = std::env::var(k) {
                cmd.env(k, v);
            }
        }
        for (k, v) in &self.env {
            cmd.env(k, v);
        }
        rusticl_default(&mut cmd, &self.exe);
        // F-E17 (14 October 2026, the project's cause from E2's container: /etc/ld.so.conf.d listed a CUDA forward-compat
        // libcuda 575 ahead of the driver's 570, and forward compat is refused on GeForce, so cuInit failed): the
        // driver's own library directories go FIRST on the loader path for every worker on Linux, so libcuda.so.1
        // resolves to the driver's copy before any cuda-*/compat directory; a container that sets LD_LIBRARY_PATH keeps
        // it after them
        #[cfg(target_os = "linux")]
        {
            let driver_dirs = ["/usr/lib/x86_64-linux-gnu", "/usr/lib64", "/usr/lib/x86_64-linux-gnu/nvidia/current", "/usr/lib"];
            let present: Vec<&str> = driver_dirs.iter().copied().filter(|d| std::path::Path::new(d).join("libcuda.so.1").exists()).collect();
            if !present.is_empty() {
                let tail = std::env::var("LD_LIBRARY_PATH").ok().filter(|v| !v.is_empty());
                let mut path = present.join(":");
                if let Some(t) = tail { path.push(':'); path.push_str(&t); }
                cmd.env("LD_LIBRARY_PATH", path);
            }
        }
        let mut child = cmd.spawn().map_err(WorkerError::Spawn)?;
        let pid = child.id();
        let stdin = child.stdin.take().ok_or(WorkerError::Gone)?;
        let stdout = child.stdout.take().ok_or(WorkerError::Gone)?;
        let stderr = child.stderr.take().ok_or(WorkerError::Gone)?;

        let (in_tx, rx) = mpsc::sync_channel::<Inbound>(INBOUND_QUEUE);
        let (tx, out_rx) = mpsc::channel::<String>();
        let dropped = Arc::new(AtomicU64::new(0));

        // stdout: protocol lines, bounded in length and in number waiting
        let t1 = in_tx.clone();
        let d1 = dropped.clone();
        thread::Builder::new().name(format!("worker-{pid}-out")).spawn(move || {
            let mut r = BufReader::new(stdout);
            let mut buf = Vec::with_capacity(4096);
            loop {
                buf.clear();
                match (&mut r).take(proto::MAX_LINE_BYTES as u64 + 1).read_until(b'\n', &mut buf) {
                    Ok(0) => break,
                    Ok(_) => {
                        if buf.len() > proto::MAX_LINE_BYTES {
                            let _ = t1.send(Inbound::Fault(format!("a line of {} bytes", buf.len())));
                            break;
                        }
                        let s = String::from_utf8_lossy(&buf);
                        if s.trim().is_empty() {
                            continue;
                        }
                        match proto::decode::<FromWorker>(&s) {
                            Ok(m) => {
                                let result = matches!(m.body, FromWorker::Share(_));
                                let msg = Inbound::Msg(m, Instant::now());
                                let sent = if result { offer_result(&t1, msg, &d1) } else { t1.send(msg).map_err(|_| ()) };
                                if sent.is_err() {
                                    break;
                                }
                            }
                            Err(e) => {
                                let _ = t1.send(Inbound::Fault(e.to_string()));
                                break;
                            }
                        }
                    }
                    Err(_) => break,
                }
            }
            let _ = t1.send(Inbound::Exited);
        })?;

        // stderr: log lines
        let t2 = in_tx;
        thread::Builder::new().name(format!("worker-{pid}-err")).spawn(move || {
            let r = BufReader::new(stderr);
            for line in r.lines().map_while(Result::ok) {
                let line: String = line.chars().take(2000).collect();
                if t2.send(Inbound::Log(line)).is_err() {
                    break;
                }
            }
        })?;

        // stdin: the writer
        thread::Builder::new().name(format!("worker-{pid}-in")).spawn(move || {
            let mut w = stdin;
            for line in out_rx {
                if w.write_all(line.as_bytes()).is_err() || w.flush().is_err() {
                    break;
                }
            }
        })?;

        let mut p = WorkerProcess {
            pid,
            exe: self.exe.clone(),
            hello: Hello { proto: 0, worker: String::new(), version: String::new(), backend: String::new(), algos: Vec::new(), devices: Vec::new(), note: String::new(), extensions: Vec::new() },
            started: Instant::now(),
            child,
            tx,
            rx,
            dropped,
            seq: Seq::default(),
        };

        // the worker speaks first; what it says on stderr before its hello goes into the error when it never comes.
        // Thirty seconds: the reference worker's vector self-test runs over ten on a loaded host (10 October 2026,
        // the the Linux builder service beside another miner), and the GPU team's worker compiled before speaking; a worker that
        // takes longer is the fault
        let started = Instant::now();
        let mut deadline = started + HELLO_WAIT;
        let mut said: Vec<String> = Vec::new();
        loop {
            let left = deadline.saturating_duration_since(Instant::now());
            // a hello already in the channel is taken whatever the clock says: a busy engine is not a silent worker
            // (the E1 study's read, 12 October 2026)
            if left.is_zero() {
                if let Ok(Inbound::Msg(Envelope { body: FromWorker::Hello(h), .. }, _)) = p.rx.try_recv() {
                    proto::validate_from_worker(&FromWorker::Hello(h.clone()))?;
                    if h.proto == PROTO_VERSION {
                        p.hello = h;
                        break;
                    }
                }
                let _ = p.kill_by_pid("no hello within the wait");
                return Err(WorkerError::Handshake(if said.is_empty() { format!("the worker said nothing for {} s (a first run compiles its program for the card and builds the dataset; it says so while it does, and Ember waits up to {} s for a worker that speaks)", HELLO_WAIT.as_secs(), HELLO_BOUND.as_secs()) } else { format!("no hello within {} s; the worker's stderr: {}", HELLO_WAIT.as_secs(), said.join(" | ")) }));
            }
            match p.rx.recv_timeout(left) {
                Ok(Inbound::Msg(Envelope { body: FromWorker::Hello(h), .. }, _)) => {
                    proto::validate_from_worker(&FromWorker::Hello(h.clone()))?;
                    if h.proto != PROTO_VERSION {
                        let _ = p.send(&ToWorker::Shutdown);
                        let _ = p.shutdown(Duration::from_secs(2));
                        return Err(WorkerError::Handshake(format!("the worker speaks protocol {}, this app speaks {PROTO_VERSION}", h.proto)));
                    }
                    p.hello = h;
                    break;
                }
                Ok(Inbound::Log(l)) => {
                    if said.len() < 6 {
                        said.push(l.chars().take(200).collect());
                    }
                    // the worker is preparing (it says so, or compiles and builds aloud): the deadline moves on each
                    // line, never past HELLO_BOUND from the start
                    deadline = (Instant::now() + HELLO_WAIT).min(started + HELLO_BOUND);
                    continue;
                }
                Ok(Inbound::Msg(..)) => {
                    let _ = p.kill_by_pid("spoke before hello");
                    return Err(WorkerError::Handshake("the worker sent a message before hello".into()));
                }
                Ok(Inbound::Fault(f)) => {
                    let _ = p.kill_by_pid("malformed hello");
                    return Err(WorkerError::Handshake(f));
                }
                // the wait ran out: the loop's head names it ("no hello within"), never as an exit
                Err(mpsc::RecvTimeoutError::Timeout) => continue,
                Ok(Inbound::Exited) | Err(mpsc::RecvTimeoutError::Disconnected) => {
                    // the stderr lines read so far name the cause (a missing library, a driver that did not load)
                    thread::sleep(Duration::from_millis(100));
                    while let Ok(Inbound::Log(l)) = p.rx.try_recv() {
                        if said.len() < 6 {
                            said.push(l.chars().take(200).collect());
                        }
                    }
                    return Err(WorkerError::Handshake(if said.is_empty() { "the worker exited before hello".into() } else { format!("the worker exited before hello; its stderr: {}", said.join(" | ")) }));
                }
            }
        }
        Ok(p)
    }
}

impl WorkerProcess {
    /// Send one message with the next sequence number; returns that number.
    pub fn send(&mut self, m: &ToWorker) -> Result<u64, WorkerError> {
        proto::validate_to_worker(m)?;
        let seq = self.seq.next();
        let line = proto::encode(seq, None, m)?;
        self.tx.send(line).map_err(|_| WorkerError::Gone)?;
        Ok(seq)
    }

    pub fn try_recv(&self) -> Option<Inbound> {
        self.rx.try_recv().ok()
    }

    /// Results dropped at the full queue since the last call (the count resets).
    pub fn take_dropped_results(&self) -> u64 {
        self.dropped.swap(0, Ordering::Relaxed)
    }

    pub fn recv_timeout(&self, d: Duration) -> Option<Inbound> {
        self.rx.recv_timeout(d).ok()
    }

    pub fn is_alive(&mut self) -> bool {
        matches!(self.child.try_wait(), Ok(None))
    }

    /// What the recorded pid is running, as the OS reports it (the first argument's file name).
    fn command_line_name(&self) -> Option<String> {
        #[cfg(target_os = "linux")]
        {
            let raw = std::fs::read(format!("/proc/{}/cmdline", self.pid)).ok()?;
            let first = raw.split(|b| *b == 0).next()?;
            let s = String::from_utf8_lossy(first).to_string();
            return Some(Path::new(&s).file_name()?.to_string_lossy().to_string());
        }
        #[cfg(target_os = "macos")]
        {
            let out = Command::new("/bin/ps").args(["-p", &self.pid.to_string(), "-o", "comm="]).output().ok()?;
            let s = String::from_utf8_lossy(&out.stdout).trim().to_string();
            if s.is_empty() {
                return None;
            }
            return Some(Path::new(&s).file_name()?.to_string_lossy().to_string());
        }
        #[allow(unreachable_code)]
        None
    }

    /// Kill by the recorded pid, after reading its command line (the charter's rule). On Windows the child
    /// handle is bound to the pid at spawn and the OS gives no portable command line, so the handle is the check.
    pub fn kill_by_pid(&mut self, reason: &str) -> Result<(), WorkerError> {
        let expected = self.exe.file_name().map(|n| n.to_string_lossy().to_string()).unwrap_or_default();
        if !self.is_alive() {
            return Ok(());
        }
        if let Some(found) = self.command_line_name() {
            if found != expected {
                return Err(WorkerError::PidMismatch { pid: self.pid, found, expected });
            }
        }
        log::warn!("killing worker pid {} ({expected}): {reason}", self.pid);
        self.child.kill()?;
        let _ = self.child.wait();
        Ok(())
    }

    /// `shutdown`, a grace period for `bye` and exit, then the pid kill.
    pub fn shutdown(&mut self, grace: Duration) -> Result<(), WorkerError> {
        let _ = self.send(&ToWorker::Shutdown);
        let deadline = Instant::now() + grace;
        while Instant::now() < deadline {
            if !self.is_alive() {
                return Ok(());
            }
            thread::sleep(Duration::from_millis(50));
        }
        self.kill_by_pid("did not exit after shutdown")
    }
}

impl Drop for WorkerProcess {
    fn drop(&mut self) {
        if self.is_alive() {
            let _ = self.kill_by_pid("supervisor dropped the handle");
        }
    }
}

#[cfg(test)]
mod tests {
    #[test]
    fn the_opencl_worker_on_linux_gets_rusticl_enabled_when_the_environment_carries_none() {
        let mut cmd = Command::new("true");
        rusticl_default(&mut cmd, std::path::Path::new("/opt/ember/workers/igneum-worker-opencl"));
        let set: Vec<String> = cmd.get_envs().filter_map(|(k, v)| v.map(|v| format!("{}={}", k.to_string_lossy(), v.to_string_lossy()))).collect();
        assert_eq!(set.iter().any(|e| e == "RUSTICL_ENABLE=radeonsi"), cfg!(target_os = "linux") && std::env::var_os("RUSTICL_ENABLE").is_none());
        let mut other = Command::new("true");
        rusticl_default(&mut other, std::path::Path::new("/opt/ember/workers/igneum-worker-cuda"));
        assert_eq!(other.get_envs().count(), 0, "only the OpenCL worker");
        assert!(PASSTHROUGH_ENV.contains(&"RUSTICL_ENABLE"));
    }
    use super::*;

    #[test]
    fn manifest_parses_hex_lines_only_and_discover_skips_non_executables() {
        let dir = std::env::temp_dir().join(format!("igneum-manifest-test-{}", std::process::id()));
        std::fs::create_dir_all(&dir).unwrap();
        std::fs::write(dir.join("workers.sha256"), format!("{}  igneum-worker-stub\nnot-a-hash  igneum-worker-x\n", "ab".repeat(32))).unwrap();
        let m = read_manifest(&dir);
        assert_eq!(m.len(), 1);
        assert_eq!(m["igneum-worker-stub"], "ab".repeat(32));
        std::fs::write(dir.join("igneum-worker-stub"), b"x").unwrap();
        std::fs::write(dir.join("igneum-worker-stub.d"), b"x").unwrap();
        std::fs::write(dir.join("other"), b"x").unwrap();
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            for n in ["igneum-worker-stub", "igneum-worker-stub.d"] {
                std::fs::set_permissions(dir.join(n), std::fs::Permissions::from_mode(0o755)).unwrap();
            }
            let l = discover(&dir, &dir.join("work"));
            assert_eq!(l.len(), 1, "{l:?}");
            assert_eq!(l[0].expected_sha256.as_deref(), Some(&*"ab".repeat(32)));
            assert!(matches!(l[0].check(), Err(WorkerError::HashMismatch { .. })));
        }
        let _ = std::fs::remove_dir_all(&dir);
    }

    /// A relative resources directory is made absolute at discovery (the worker's cwd is its own work directory).
    #[test]
    fn a_relative_workers_dir_is_made_absolute_at_discovery() {
        let dir = std::env::temp_dir().join(format!("igneum-rel-workers-{}", std::process::id()));
        std::fs::create_dir_all(dir.join("workers")).unwrap();
        std::fs::write(dir.join("workers").join("igneum-worker-stub"), b"#!/bin/sh\n").unwrap();
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            std::fs::set_permissions(dir.join("workers").join("igneum-worker-stub"), std::fs::Permissions::from_mode(0o755)).unwrap();
            let here = std::env::current_dir().unwrap();
            std::env::set_current_dir(&dir).unwrap();
            let l = discover(Path::new("./workers"), &dir.join("work"));
            std::env::set_current_dir(here).unwrap();
            assert_eq!(l.len(), 1, "{l:?}");
            assert!(l[0].exe.is_absolute(), "{:?}", l[0].exe);
        }
        let _ = std::fs::remove_dir_all(&dir);
    }

    /// The Metal host declares one algorithm per launch (its --algo value): Ember names the candidate at launch for a
    /// worker named metal and nothing for the others (the mini's first parity row, 11 October 2026).
    #[test]
    fn a_metal_worker_is_launched_under_the_candidates_name() {
        let dir = std::env::temp_dir().join(format!("igneum-metal-launch-{}", std::process::id()));
        std::fs::create_dir_all(&dir).unwrap();
        std::fs::write(dir.join("igneum-worker-metal"), b"#!/bin/sh\n").unwrap();
        std::fs::write(dir.join("igneum-worker-cuda"), b"#!/bin/sh\n").unwrap();
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            for n in ["igneum-worker-metal", "igneum-worker-cuda"] {
                std::fs::set_permissions(dir.join(n), std::fs::Permissions::from_mode(0o755)).unwrap();
            }
            let l = discover(&dir, &dir.join("work"));
            assert_eq!(l.len(), 2, "{l:?}");
            let metal = l.iter().find(|x| x.exe.ends_with("igneum-worker-metal")).unwrap();
            assert_eq!(metal.args, vec!["--algo".to_string(), "igneumhash".to_string(), "protocol".to_string()]);
            let cuda = l.iter().find(|x| x.exe.ends_with("igneum-worker-cuda")).unwrap();
            assert!(cuda.args.is_empty());
        }
        let _ = std::fs::remove_dir_all(&dir);
    }

    #[test]
    fn a_full_inbound_queue_drops_results_and_counts_them() {
        // the mini on the public pool (12 October 2026): results by the ten thousand a second, 9.8 GB resident
        let (tx, rx) = mpsc::sync_channel::<Inbound>(2);
        let dropped = AtomicU64::new(0);
        let share = || Inbound::Msg(Envelope { seq: 1, re: None, body: FromWorker::Share(proto::Share { job_id: "j".into(), nonce: 1, mix: "00".repeat(32), hash: "00".repeat(32) }) }, Instant::now());
        assert_eq!(offer_result(&tx, share(), &dropped), Ok(()));
        assert_eq!(offer_result(&tx, share(), &dropped), Ok(()));
        assert_eq!(offer_result(&tx, share(), &dropped), Ok(()), "the third finds the queue full: dropped, not an error");
        assert_eq!(dropped.load(Ordering::Relaxed), 1);
        assert_eq!(INBOUND_QUEUE, 4096);
        drop(rx);
        assert_eq!(offer_result(&tx, share(), &dropped), Err(()), "a closed queue is the sender's end");
        assert_eq!(dropped.load(Ordering::Relaxed), 1);
    }

    #[test]
    fn f_e36_the_opencl_walk_names_every_device_and_the_engine_skips_nvidia() {
        let line = r#"{"telemetry":[{"index":0,"bus_id":"0000:01:00.0","name":"NVIDIA GeForce RTX 5090","vendor":"NVIDIA Corporation","power_w":450.0},{"index":1,"bus_id":"0000:63:00.0","name":"Intel(R) Arc(TM) B580 Graphics","vendor":"Intel(R) Corporation"},{"index":2,"bus_id":"","name":"pthread-x86_64","vendor":"GenuineIntel"}]}"#;
        let list = parse_telemetry_line(line).unwrap();
        assert_eq!(list.len(), 3);
        assert_eq!(list[1], (1, "Intel(R) Corporation".to_string(), "Intel(R) Arc(TM) B580 Graphics".to_string()));
        let picked: Vec<u32> = list.iter().filter(|(_, v, _)| !v.to_ascii_lowercase().contains("nvidia")).map(|(i, _, _)| *i).collect();
        assert_eq!(picked, vec![1, 2], "the NVIDIA card mines through CUDA; the others open by index");
        assert!(parse_telemetry_line("{\"nope\":1}").is_err());
    }

    #[test]
    fn sha256_of_a_known_file() {
        let dir = std::env::temp_dir().join(format!("igneum-sha-test-{}", std::process::id()));
        std::fs::create_dir_all(&dir).unwrap();
        let p = dir.join("abc");
        std::fs::write(&p, b"abc").unwrap();
        assert_eq!(sha256_file(&p).unwrap(), "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad");
        let _ = std::fs::remove_dir_all(&dir);
    }
}
