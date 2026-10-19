//! ember (design section 4; `igneum-cli` is the same binary under its old name for one release): devices, mine, tune, bench, verify, config. Flags over a config file over
//! defaults; `--json` for machines; `--api` for a read-only local endpoint with /state and /metrics.

use clap::{Parser, Subcommand};
use igneum_supervisor::economics::Tariff;
use igneum_supervisor::modes::Mode;
use igneum_supervisor::settings::WorkSourceSetting;
use igneum_supervisor::state::{MiningState, Snapshot};
use igneum_supervisor::{Config, Supervisor};
use igneum_worker_proto as proto;
use std::io::{BufRead, BufReader, Write};
use std::net::TcpListener;
use std::path::PathBuf;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Arc;
use std::thread;
use std::time::Duration;

const VERSION: &str = env!("CARGO_PKG_VERSION");

#[derive(Parser)]
#[command(name = "ember", version = VERSION, about = "Ember: the Igneum command-line miner")]
struct Cli {
    /// Where the worker executables are (default: beside this binary).
    #[arg(long, global = true)]
    workers_dir: Option<PathBuf>,
    /// Where settings, the tune store and the journal live.
    #[arg(long, global = true)]
    data_dir: Option<PathBuf>,
    /// Machine-readable output.
    #[arg(long, global = true)]
    json: bool,
    #[command(subcommand)]
    cmd: Cmd,
}

#[derive(clap::Args, Clone)]
struct MineArgs {
    /// Pool host:port (IS/1 over TLS unless --no-tls).
    #[arg(long, conflicts_with = "node")]
    pool: Option<String>,
    /// Plain TCP to the pool (a pool on a private network); the default is TLS against the public roots.
    #[arg(long)]
    no_tls: bool,
    /// A node's RPC URL for solo mining (the public test network's is https://rpc.igneum.network); repeated or
    /// comma-separated for several (the first that answers serves the work and takes a block; every one takes the
    /// shares, until share gossip lands). Without --node or --pool the project's pool is the source.
    #[arg(long, value_delimiter = ',')]
    node: Vec<String>,
    /// A lab switch: the one node the template is read from, the shares going to every --node (the founder's
    /// gossip row of 11 October 2026: the template from a sealer, every share to the peer alone). Sets
    /// EMBER_TEMPLATE_NODE for the source.
    #[arg(long)]
    template_node: Option<String>,
    /// A lab switch (the devnet8 rows of 11 October 2026, never a release default): the share lane paces at the
    /// CHAIN's share target alone, no per-card measurement target, every result under it submitted, the node's
    /// cap bounding the flood. Sets EMBER_PACE_CHAIN_TARGET=1; the minute line and the record name it.
    #[arg(long)]
    pace_chain_target: bool,
    /// A backup pool host:port (same TLS setting as --pool): taken by itself after the first stays unreachable
    /// for a minute, the first tried again every ten minutes.
    #[arg(long, requires = "pool")]
    pool_backup: Option<String>,
    /// Ask the pool to waive its own fee (the pool decides; the protocol fund of 1 percent is a consensus rule
    /// and never optional).
    #[arg(long)]
    no_pool_fee: bool,
    /// The miner dev fee in percent (ED-05, the founder's word of 11 October 2026: on at 1 by default, shown,
    /// switchable off): one template in 100 / percent is requested with the dev payout address from the signed
    /// dev-fee-manifest.json beside the workers instead of yours; 0 turns it off. Without the signed manifest the
    /// fee is off and the start line says why.
    #[arg(long)]
    dev_fee: Option<u8>,
    /// A config file of `key = value` lines (pool, pool_backup, node, no_tls, address, worker_name, tariff,
    /// currency, price_per_ign, algo, no_pool_fee, api, workers_dir); flags win over it. The default is
    /// $IGNEUM_SECRETS_DIR/ember/ember.conf when it exists.
    #[arg(long)]
    config: Option<PathBuf>,
    /// The local stub source (nothing is mined; rehearsal only).
    #[arg(long)]
    stub: bool,
    /// The payout address (0x + 40 hex).
    #[arg(long)]
    address: Option<String>,
    #[arg(long)]
    worker_name: Option<String>,
    /// The candidate id (the algorithm version) to mine on a pool; a node's own algorithm always wins.
    #[arg(long)]
    algo: Option<String>,
    #[arg(long, value_parser = ["quiet", "efficient", "max"])]
    mode: Option<String>,
    #[arg(long)]
    power_cap: Option<f64>,
    #[arg(long)]
    temp_cap: Option<f64>,
    /// F-E18 (14 October 2026): set when a config file was read; the file then wins over the stored settings for the
    /// limit keys, so a line removed from the file clears the value settings.json still held (power_cap, temp_cap).
    #[arg(skip)]
    conf_read: bool,
    #[arg(long)]
    fan_cap: Option<f64>,
    /// Efficient's floor: a fraction (0 to 1) of the measured maximum, or an absolute rate in H/s.
    #[arg(long)]
    min_rate: Option<f64>,
    /// Device fingerprints to use (`ember devices` prints them; default: every enabled device). A fingerprint
    /// not among the cards seen refuses the start; an index is not a name.
    #[arg(long)]
    device: Vec<String>,
    /// Pin every card to one VectorWork coordinate the work source serves: the router neither probes nor switches.
    #[arg(long)]
    coord: Option<u8>,
    /// The chain's public RPC the engine reads the discipline registry from (0.2.15: igneum_vectorWork and
    /// igneum_getWork per coordinate, every 15 s), so a succession reaches it at the activation; none reads none.
    #[arg(long)]
    registry_rpc: Option<String>,
    /// Pin a knob on every selected device: name=value.
    #[arg(long)]
    pin: Vec<String>,
    /// Flat tariff per kWh.
    #[arg(long)]
    tariff: Option<f64>,
    #[arg(long)]
    currency: Option<String>,
    #[arg(long)]
    wall_watts: Option<f64>,
    #[arg(long)]
    price_per_ign: Option<f64>,
    #[arg(long)]
    other_per_day: Option<f64>,
    /// Serve /state and /metrics here (loopback unless you say otherwise).
    #[arg(long)]
    api: Option<String>,
    /// Seconds between state lines.
    #[arg(long, default_value_t = 10)]
    every: u64,
    /// Write the final state (the transitions, the switch figures, the totals, the device budgets) as JSON here at exit.
    #[arg(long)]
    report: Option<PathBuf>,
    /// Read the bond's secret (hex of 32 bytes, one line) from standard input at start, for a network whose
    /// document carries the bonded rounds; the wallet pipes it. The alternative is the EMBER_BOND_SK variable,
    /// scrubbed from the environment the moment it is read. Never a file, never printed.
    #[arg(long)]
    bond_secret_stdin: bool,
}

#[derive(Subcommand)]
enum Cmd {
    /// List the devices every installed worker reports.
    Devices,
    /// Mine until interrupted.
    Mine(MineArgs),
    /// Run the tuner on the selected devices (mines meanwhile), then exit.
    Tune {
        #[command(flatten)]
        mine: MineArgs,
        #[arg(long)]
        budget_seconds: Option<u64>,
        /// Seconds to settle after each change before measuring (default 15).
        #[arg(long)]
        settle_seconds: Option<u64>,
        /// Seconds to measure each candidate (default 45).
        #[arg(long)]
        window_seconds: Option<u64>,
    },
    /// Validated rate and device telemetry over N seconds (device telemetry, not a wall measurement).
    Bench {
        #[command(flatten)]
        mine: MineArgs,
        #[arg(long, default_value_t = 120)]
        seconds: u64,
    },
    /// Re-verify worker shares (JSON lines of {height, header, nonce, mix, hash, algo}) on the CPU.
    Verify { file: PathBuf },
    /// Show the miner's work-authority key (the public half; the key file never leaves its directory) and,
    /// with --node, the payout address's balance on the chain.
    Key {
        #[arg(long)]
        node: Option<String>,
    },
    /// Show the settings file, or its path.
    Config {
        #[arg(long)]
        path: bool,
    },
    /// Pause the running miner (the session stays dialled; the cards idle) through its API (F-E22).
    Pause {
        /// The running miner's API bind (default: the config file's `api`, else 127.0.0.1:4068).
        #[arg(long)]
        api: Option<String>,
        /// The config file to read the bind from (default: the service's one file).
        #[arg(long)]
        config: Option<PathBuf>,
    },
    /// Resume a paused miner through its API (F-E22).
    Resume {
        #[arg(long)]
        api: Option<String>,
        #[arg(long)]
        config: Option<PathBuf>,
    },
    /// The bounded menu's fit and transition evidence (the research test plan's "do GPUs retain access"): for
    /// every entry, the card's cold and warm memory fit and the overhead of switching to it.
    Fit {
        /// Which worker executable (default: igneum-worker-cpu when present, else the stub).
        #[arg(long)]
        worker: Option<String>,
        /// The candidate id (default: igneumhash).
        #[arg(long)]
        algo: Option<String>,
        /// A JSON file: an array of {"name": ..., "params": {...}} entries (default: the version-1 baseline at
        /// 1, 2 and 4 GiB datasets).
        #[arg(long)]
        entries: Option<PathBuf>,
        /// Seconds to mine each entry after its first result.
        #[arg(long, default_value_t = 15)]
        seconds: u64,
        /// Write the JSON record here as well.
        #[arg(long)]
        report: Option<PathBuf>,
    },
    /// The operational checks of the IgneumHash plan's chapter 15 against an installed worker, with no node:
    /// template switch and cancellation latency, the non-mining states, nonce coverage.
    Selftest {
        /// The candidate id to run (default: the first the worker carries).
        #[arg(long)]
        algo: Option<String>,
        /// Which worker executable (default: igneum-worker-cpu when present, else the stub).
        #[arg(long)]
        worker: Option<String>,
        /// The candidate's parameter document for configure.params (JSON; the node's exact shape).
        #[arg(long)]
        params: Option<String>,
        #[arg(long, default_value_t = 20)]
        seconds: u64,
        /// Template switches to time after the main run (the job-switch delay distribution: median and p99).
        #[arg(long, default_value_t = 20)]
        switches: u32,
    },
}

/// The config file: `key = value` lines, `#` comments; the keys of the mine flags. Flags win over it.
fn read_conf(path: &std::path::Path) -> Result<std::collections::HashMap<String, String>, String> {
    let text = std::fs::read_to_string(path).map_err(|e| format!("{}: {e}", path.display()))?;
    let mut m = std::collections::HashMap::new();
    for (n, line) in text.lines().enumerate() {
        let l = line.trim();
        if l.is_empty() || l.starts_with('#') {
            continue;
        }
        let (k, v) = l.split_once('=').ok_or_else(|| format!("{}:{}: not key = value", path.display(), n + 1))?;
        let v = v.trim().trim_matches('"').trim_matches('\'').to_string();
        m.insert(k.trim().replace('-', "_"), v);
    }
    Ok(m)
}

fn default_conf_path() -> Option<PathBuf> {
    let home = std::env::var("HOME").or_else(|_| std::env::var("USERPROFILE")).ok()?;
    let p = PathBuf::from(home).join(".config").join("ember").join("ember.conf");
    if p.is_file() { Some(p) } else { None }
}

/// Fill the mine arguments the flags left unset from the config file.
fn merge_conf(a: &mut MineArgs, m: &std::collections::HashMap<String, String>) -> Result<(), String> {
    a.conf_read = true;
    let f = |k: &str| m.get(k).cloned();
    let num = |k: &str| -> Result<Option<f64>, String> { m.get(k).map(|v| v.parse::<f64>().map_err(|_| format!("{k}: not a number"))).transpose() };
    let flag = |k: &str| m.get(k).map_or(false, |v| matches!(v.as_str(), "1" | "true" | "yes" | "on"));
    if a.pool.is_none() && a.node.is_empty() && !a.stub {
        a.pool = f("pool");
        if a.pool.is_none() {
            a.node = f("node").map(|v| igneum_supervisor::settings::rpc_url_list(&v)).unwrap_or_default();
        }
        if a.pool.is_none() && a.node.is_empty() && flag("stub") {
            a.stub = true;
        }
    }
    if a.pool_backup.is_none() {
        a.pool_backup = f("pool_backup");
    }
    if flag("no_tls") {
        a.no_tls = true;
    }
    if flag("no_pool_fee") {
        a.no_pool_fee = true;
    }
    if a.address.is_none() {
        a.address = f("address").or_else(|| f("wallet"));
    }
    if a.coord.is_none() {
        a.coord = f("coord").and_then(|v| v.trim().parse().ok());
    }
    if a.registry_rpc.is_none() {
        a.registry_rpc = f("registry_rpc");
    }
    if a.worker_name.is_none() {
        a.worker_name = f("worker_name").or_else(|| f("worker"));
    }
    if a.algo.is_none() {
        a.algo = f("algo");
    }
    if a.mode.is_none() {
        a.mode = f("mode");
    }
    if a.tariff.is_none() {
        a.tariff = num("tariff")?;
    }
    if a.currency.is_none() {
        a.currency = f("currency");
    }
    if a.wall_watts.is_none() {
        a.wall_watts = num("wall_watts")?;
    }
    if a.price_per_ign.is_none() {
        a.price_per_ign = num("price_per_ign")?;
    }
    if a.other_per_day.is_none() {
        a.other_per_day = num("other_per_day")?;
    }
    if a.power_cap.is_none() {
        a.power_cap = num("power_cap")?;
    }
    if a.temp_cap.is_none() {
        a.temp_cap = num("temp_cap")?;
    }
    if a.api.is_none() {
        a.api = f("api");
    }
    if a.device.is_empty() {
        if let Some(d) = f("device") {
            a.device = d.split(',').map(|x| x.trim().to_string()).filter(|x| !x.is_empty()).collect();
        }
    }
    Ok(())
}

pub fn main() {
    let mut cli = Cli::parse();
    // the config file fills what the flags left unset (the service's one file: pool, wallet, worker name, tariff)
    let conf_path = match &cli.cmd {
        Cmd::Mine(a) => a.config.clone().or_else(default_conf_path),
        _ => None,
    };
    let conf = match conf_path.as_ref().map(|p| read_conf(p)) {
        Some(Ok(m)) => Some(m),
        Some(Err(e)) => {
            eprintln!("{e}");
            std::process::exit(2);
        }
        None => None,
    };
    if let (Cmd::Mine(a), Some(m)) = (&mut cli.cmd, &conf) {
        if let Err(e) = merge_conf(a, m) {
            eprintln!("{}: {e}", conf_path.as_ref().map(|p| p.display().to_string()).unwrap_or_default());
            std::process::exit(2);
        }
    }
    if cli.workers_dir.is_none() {
        if let Some(w) = conf.as_ref().and_then(|m| m.get("workers_dir")) {
            cli.workers_dir = Some(PathBuf::from(w));
        }
    }
    if cli.data_dir.is_none() {
        if let Some(d) = conf.as_ref().and_then(|m| m.get("data_dir")) {
            cli.data_dir = Some(PathBuf::from(d));
        }
    }
    let exe_dir = std::env::current_exe().ok().and_then(|p| p.parent().map(|d| d.to_path_buf())).unwrap_or_else(|| PathBuf::from("."));
    let workers_dir = cli.workers_dir.clone().unwrap_or(exe_dir);
    let data_dir = cli.data_dir.clone().unwrap_or_else(default_data_dir);
    let mut cfg = Config::new("ember", VERSION, &workers_dir, data_dir.clone());
    // the dev fee (devfee.rs): the operator's --dev-fee (the config file's dev_fee line otherwise) against the
    // signed manifest beside the workers; None takes the manifest's default (1); 0 is off
    let dev_fee_percent: Option<u8> = match &cli.cmd {
        Cmd::Mine(a) => a.dev_fee.or_else(|| conf.as_ref().and_then(|m| m.get("dev_fee")).and_then(|v| v.trim().parse::<u8>().ok())),
        _ => None,
    };
    cfg = cfg.with_dev_fee(dev_fee_percent);
    // the work-authority key lives under the user's config directory, beside the config file (the founder's
    // word, 10 October 2026); the data dir only where there is no home
    cfg.key_path = Some(default_conf_path().and_then(|p| p.parent().map(|d| d.to_path_buf())).or_else(|| std::env::var("HOME").or_else(|_| std::env::var("USERPROFILE")).ok().map(|h| PathBuf::from(h).join(".config").join("ember"))).unwrap_or_else(|| data_dir.clone()).join("miner.key"));
    // the bond's secret for a keyed document (bond.rs): the environment first (scrubbed), standard input on the flag
    match igneum_supervisor::bond::BondSecret::from_env() {
        Ok(Some(b)) => cfg.bond_secret = Some(std::sync::Arc::new(b)),
        Ok(None) => {}
        Err(e) => {
            eprintln!("{e}");
            std::process::exit(2);
        }
    }
    if let Cmd::Mine(a) = &cli.cmd {
        if a.bond_secret_stdin {
            match igneum_supervisor::bond::BondSecret::from_reader(&mut std::io::stdin().lock()) {
                Ok(b) => cfg.bond_secret = Some(std::sync::Arc::new(b)),
                Err(e) => {
                    eprintln!("{e}");
                    std::process::exit(2);
                }
            }
        }
    }
    let json = cli.json;
    if let Cmd::Tune { budget_seconds, settle_seconds, window_seconds, .. } = &cli.cmd {
        if let Some(b) = budget_seconds {
            cfg.tune.budget = Duration::from_secs(*b);
        }
        if let Some(x) = settle_seconds {
            cfg.tune.settle = Duration::from_secs(*x);
        }
        if let Some(x) = window_seconds {
            cfg.tune.window = Duration::from_secs(*x);
        }
    }

    let code = match cli.cmd {
        Cmd::Devices => devices(cfg, json),
        Cmd::Mine(a) => mine(cfg, a, json, None, None),
        Cmd::Tune { mine: a, budget_seconds, .. } => mine(cfg, a, json, Some(budget_seconds.unwrap_or(12 * 60)), None),
        Cmd::Bench { mine: a, seconds } => mine(cfg, a, json, None, Some(seconds)),
        Cmd::Verify { file } => verify(file, json),
        Cmd::Pause { api, config } => control("pause", api, config),
        Cmd::Resume { api, config } => control("resume", api, config),
        Cmd::Selftest { algo, worker, params, seconds, switches } => selftest(cfg, algo, worker, params, seconds, switches, json),
        Cmd::Fit { worker, algo, entries, seconds, report } => fit(cfg, worker, algo, entries, seconds, report, json),
        Cmd::Key { node } => key_cmd(cfg, node),
        Cmd::Config { path } => {
            let p = cfg.settings_path();
            if path {
                println!("{}", p.display());
            } else {
                match std::fs::read_to_string(&p) {
                    Ok(s) => println!("{s}"),
                    Err(_) => println!("no settings file yet at {}", p.display()),
                }
            }
            0
        }
    };
    std::process::exit(code);
}

/// Drives one worker directly over protocol v0 and reports chapter 15's operational facts as measured.
fn selftest(cfg: Config, algo: Option<String>, worker: Option<String>, params: Option<String>, seconds: u64, switches: u32, json: bool) -> i32 {
    use igneum_supervisor::worker::Inbound;
    use igneum_worker_proto::{Cancel, Configure, Envelope, FromWorker, Job, ToWorker, Transition, WorkerState};
    use std::time::Instant;
    let pick = |name: &str| cfg.launchers.iter().find(|l| l.exe.file_name().map_or(false, |n| n.to_string_lossy().starts_with(name))).cloned();
    let launcher = match worker.as_deref() {
        Some(w) => pick(w),
        None => pick("igneum-worker-cpu").or_else(|| pick("igneum-worker-stub")),
    };
    let Some(launcher) = launcher else {
        eprintln!("no worker found in {}", cfg.launchers.first().map(|l| l.exe.parent().map(|p| p.display().to_string()).unwrap_or_default()).unwrap_or_else(|| "the workers directory".into()));
        return 2;
    };
    let mut p = match launcher.launch() {
        Ok(p) => p,
        Err(e) => {
            eprintln!("{e}");
            return 2;
        }
    };
    let algo = algo.or_else(|| p.hello.algos.iter().find(|a| a.vectors_ok).map(|a| a.id.clone())).unwrap_or_default();
    let device = p.hello.devices.first().map(|d| d.id.clone()).unwrap_or_default();
    let vectors = p.hello.algos.iter().map(|a| format!("{}:{}", a.id, if a.vectors_ok { "pass" } else { "FAIL" })).collect::<Vec<_>>().join(" ");
    let doc: serde_json::Value = match params.as_deref() {
        Some(t) => match serde_json::from_str(t) {
            Ok(v) => v,
            Err(e) => {
                eprintln!("--params is not JSON: {e}");
                return 2;
            }
        },
        // no document given: the candidate's version-1 baseline (the version is mandatory; null is a refusal)
        None if algo == "igneumhash" => serde_json::json!({"version": 1}),
        None => serde_json::Value::Null,
    };
    let mut report = serde_json::json!({ "worker": p.hello.worker, "version": p.hello.version, "backend": p.hello.backend, "algo": algo, "vectors": vectors, "params": doc });
    let _ = p.send(&ToWorker::Configure(Configure { device: device.clone(), algo: algo.clone(), params: doc.clone(), stat_ms: 100, transition: Transition::Auto, memory_margin_bytes: 0 }));
    let t0 = Instant::now();
    let mut ready = false;
    let deadline = Instant::now() + Duration::from_secs(60);
    while Instant::now() < deadline {
        match p.recv_timeout(Duration::from_millis(200)) {
            Some(Inbound::Msg(Envelope { body: FromWorker::Ready(_), .. }, _)) => {
                ready = true;
                break;
            }
            Some(Inbound::Msg(Envelope { body: FromWorker::Refuse(r), .. }, _)) => {
                eprintln!("refused: {} ({})", r.reason, r.term.unwrap_or_default());
                return 3;
            }
            Some(Inbound::Log(l)) => eprintln!("  worker: {l}"),
            Some(Inbound::Exited) => {
                eprintln!("the worker exited");
                return 3;
            }
            _ => {}
        }
    }
    if !ready {
        eprintln!("no ready within 60 s");
        return 3;
    }
    report["ready_ms"] = serde_json::json!(t0.elapsed().as_millis() as u64);
    // the keyed document (SPEC 0.9, bit 10): the secret in hand rides every job, and every share of job A is
    // checked by the keyed path (the no-GPU end to end of the hostile-key rule on the CPU worker)
    let keyed = igneum_supervisor::bond::keyed_rounds(&doc);
    let keyed_check: Option<Box<dyn igneum_supervisor::verify::Verifier>> = match (keyed, cfg.bond_secret.as_ref()) {
        (Some(_), Some(_)) => igneum_supervisor::verify::verifier_for_params(&algo, Some(&doc.to_string())).ok(),
        _ => None,
    };
    let mut keyed_checked = 0u64;
    let mut keyed_failed = 0u64;
    let job = |id: &str, height: u64, start: u64| ToWorker::Job(Job { job_id: id.into(), height, header: format!("{:064x}", height + 7), boundary: "00".repeat(32), share_target: "0001".to_string() + &"ff".repeat(30), nonce_start: start, nonce_count: 1 << 40, clean: true, algo: algo.clone(), sk: if keyed.is_some() { cfg.bond_secret.as_ref().map(|b| b.job_field()) } else { None } });
    // job A: watch the preparing phase (dataset) and the first shares
    let _ = p.send(&job("A", 1, 0));
    let mut phases: Vec<String> = Vec::new();
    let mut first_share_a: Option<u64> = None;
    let mut shares_a = 0u64;
    let mut prepared_ms: Option<u64> = None;
    let mut mining_seen = Instant::now();
    let ta = Instant::now();
    let end_a = Instant::now() + Duration::from_secs(seconds.max(5) + 120);
    let mut hashes = 0u64;
    loop {
        if Instant::now() > end_a {
            break;
        }
        match p.recv_timeout(Duration::from_millis(200)) {
            Some(Inbound::Msg(Envelope { body, .. }, _)) => match body {
                FromWorker::Stat(st) => {
                    hashes += st.hashes;
                    if st.state == WorkerState::Preparing && !st.phase.is_empty() && phases.last() != Some(&st.phase) {
                        phases.push(st.phase.clone());
                    }
                    if st.state == WorkerState::Mining {
                        mining_seen = Instant::now();
                        if first_share_a.is_some() && ta.elapsed() > Duration::from_secs(seconds.max(5)) {
                            break;
                        }
                    }
                }
                FromWorker::Prepared(pr) => prepared_ms = Some(pr.ms),
                FromWorker::Share(sh) => {
                    if sh.job_id == "A" {
                        shares_a += 1;
                        if first_share_a.is_none() {
                            first_share_a = Some(ta.elapsed().as_millis() as u64);
                        }
                        if let (Some(v), Some(b), Some(mix), Some(hash)) = (keyed_check.as_ref(), cfg.bond_secret.as_ref(), igneum_worker_proto::hex_decode(&sh.mix), igneum_worker_proto::hex_to_32(&sh.hash)) {
                            let header = igneum_worker_proto::hex_to_32(&format!("{:064x}", 1u64 + 7)).unwrap_or([0; 32]);
                            match v.verify_keyed(1, &header, sh.nonce, &mix, &hash, b.bytes()) {
                                Ok(Some(shares)) if shares.len() == keyed.unwrap_or(0) as usize * 32 => keyed_checked += 1,
                                _ => keyed_failed += 1,
                            }
                        }
                    }
                }
                FromWorker::Fault(f) => eprintln!("  fault {}: {}", f.code, f.message),
                _ => {}
            },
            Some(Inbound::Log(l)) => eprintln!("  worker: {l}"),
            Some(Inbound::Exited) => break,
            _ => {}
        }
    }
    let _ = mining_seen;
    report["phases_seen"] = serde_json::json!(phases);
    report["prepared_ms"] = serde_json::json!(prepared_ms);
    report["first_share_ms"] = serde_json::json!(first_share_a);
    report["shares_a"] = serde_json::json!(shares_a);
    report["hashes_a"] = serde_json::json!(hashes);
    if let Some(s_rounds) = keyed {
        report["keyed_rounds"] = serde_json::json!(s_rounds);
        report["keyed_secret_in_hand"] = serde_json::json!(cfg.bond_secret.is_some());
        report["keyed_shares_checked"] = serde_json::json!(keyed_checked);
        report["keyed_shares_failed"] = serde_json::json!(keyed_failed);
    }
    // template switch: job B replaces A; count A shares after the switch and the time to the first B stat
    let tb = Instant::now();
    let _ = p.send(&job("B", 1, 1 << 41));
    let mut a_after = 0u64;
    let mut last_a_ms: Option<u64> = None;
    let mut b_first_stat_ms: Option<u64> = None;
    let mut shares_b = 0u64;
    let end_b = Instant::now() + Duration::from_secs(seconds.max(5));
    while Instant::now() < end_b {
        match p.recv_timeout(Duration::from_millis(200)) {
            Some(Inbound::Msg(Envelope { body, .. }, _)) => match body {
                FromWorker::Share(sh) => {
                    if sh.job_id == "A" {
                        a_after += 1;
                        last_a_ms = Some(tb.elapsed().as_millis() as u64);
                    } else if sh.job_id == "B" {
                        shares_b += 1;
                    }
                }
                FromWorker::Stat(st) => {
                    if st.job_id == "B" && b_first_stat_ms.is_none() {
                        b_first_stat_ms = Some(tb.elapsed().as_millis() as u64);
                    }
                }
                _ => {}
            },
            Some(Inbound::Log(l)) => eprintln!("  worker: {l}"),
            _ => {}
        }
    }
    report["switch_a_shares_after"] = serde_json::json!(a_after);
    report["switch_last_a_share_ms"] = serde_json::json!(last_a_ms);
    report["switch_b_first_stat_ms"] = serde_json::json!(b_first_stat_ms);
    report["shares_b"] = serde_json::json!(shares_b);
    // explicit cancel: no share after the cancel
    let tc = Instant::now();
    let _ = p.send(&ToWorker::Cancel(Cancel { job_id: "B".into() }));
    let mut after_cancel = 0u64;
    let mut last_after_cancel_ms: Option<u64> = None;
    let mut idle_ms: Option<u64> = None;
    let end_c = Instant::now() + Duration::from_secs(5);
    while Instant::now() < end_c {
        match p.recv_timeout(Duration::from_millis(200)) {
            Some(Inbound::Msg(Envelope { body: FromWorker::Share(sh), .. }, _)) => {
                if sh.job_id == "B" {
                    after_cancel += 1;
                    last_after_cancel_ms = Some(tc.elapsed().as_millis() as u64);
                }
            }
            Some(Inbound::Msg(Envelope { body: FromWorker::Stat(st), .. }, _)) => {
                if st.state == WorkerState::Idle && idle_ms.is_none() {
                    idle_ms = Some(tc.elapsed().as_millis() as u64);
                }
            }
            _ => {}
        }
    }
    report["cancel_shares_after"] = serde_json::json!(after_cancel);
    report["cancel_last_share_ms"] = serde_json::json!(last_after_cancel_ms);
    report["cancel_idle_ms"] = serde_json::json!(idle_ms);
    // the job-switch delay distribution: N template switches, each timed to the first stat naming the new job
    let mut delays: Vec<u64> = Vec::new();
    let mut stale_after_switch = 0u64;
    for k in 0..switches {
        let old = format!("S{k}");
        let new = format!("S{}", k + 1);
        if k == 0 {
            let _ = p.send(&job(&old, 1, 1 << 42));
            thread::sleep(Duration::from_millis(400));
        }
        let ts = Instant::now();
        let _ = p.send(&job(&new, 1, (1 << 42) + ((k as u64 + 1) << 36)));
        let end = Instant::now() + Duration::from_secs(5);
        let mut seen = false;
        while Instant::now() < end {
            match p.recv_timeout(Duration::from_millis(50)) {
                Some(Inbound::Msg(Envelope { body: FromWorker::Stat(st), .. }, _)) => {
                    if st.job_id == new && st.state == WorkerState::Mining {
                        delays.push(ts.elapsed().as_millis() as u64);
                        seen = true;
                        break;
                    }
                }
                Some(Inbound::Msg(Envelope { body: FromWorker::Share(sh), .. }, _)) => {
                    if sh.job_id == old && ts.elapsed() > Duration::from_millis(300) {
                        stale_after_switch += 1;
                    }
                }
                _ => {}
            }
        }
        if !seen {
            delays.push(5000);
        }
        thread::sleep(Duration::from_millis(300));
    }
    let _ = p.send(&ToWorker::Cancel(Cancel { job_id: format!("S{switches}") }));
    delays.sort_unstable();
    let pct = |q: f64| -> Option<u64> { if delays.is_empty() { None } else { Some(delays[((q * delays.len() as f64).ceil() as usize).clamp(1, delays.len()) - 1]) } };
    report["switch_samples"] = serde_json::json!(delays.len());
    report["switch_median_ms"] = serde_json::json!(pct(0.5));
    report["switch_p99_ms"] = serde_json::json!(pct(0.99));
    report["switch_max_ms"] = serde_json::json!(delays.last().copied());
    report["switch_old_shares_after_window"] = serde_json::json!(stale_after_switch);
    let _ = p.shutdown(Duration::from_secs(5));
    // chapter 15: cancellation is honoured within one batch; shares already in the pipe at the moment of the
    // switch or the cancel arrive inside that window and are the supervisor's to drop (counted "cancelled")
    let window_ms = 300u64;
    let switch_ok = a_after == 0 || last_a_ms.map_or(false, |t| t <= window_ms);
    let cancel_ok = after_cancel == 0 || last_after_cancel_ms.map_or(false, |t| t <= window_ms);
    let ok = shares_a > 0 && switch_ok && cancel_ok && keyed_failed == 0 && (keyed_check.is_none() || keyed_checked > 0);
    report["window_ms"] = serde_json::json!(window_ms);
    report["verdict"] = serde_json::json!(if ok { "pass" } else { "FAIL" });
    if json {
        println!("{}", serde_json::to_string_pretty(&report).unwrap_or_default());
    } else {
        println!("selftest {} {} on {} with params {}: vectors {}", report["worker"], report["version"], algo, doc, vectors);
        println!("  ready in {} ms; phases seen {:?}; dataset prepared in {:?} ms; first share after {:?} ms; {} shares, {} hashes on A", report["ready_ms"], phases, prepared_ms, first_share_a, shares_a, hashes);
        println!("  template switch A to B: {} A shares after the switch (last at {:?} ms); B's first stat at {:?} ms; {} B shares", a_after, last_a_ms, b_first_stat_ms, shares_b);
        println!("  cancel B: {} shares after the cancel (last at {:?} ms); idle at {:?} ms", after_cancel, last_after_cancel_ms, idle_ms);
        println!("  job-switch delay over {} switches (stat every 100 ms, so the floor is one stat period): median {:?} ms, p99 {:?} ms, max {:?} ms; old-job shares after the 300 ms window {}", delays.len(), pct(0.5), pct(0.99), delays.last(), stale_after_switch);
        println!("  verdict: {} (shares after a switch or a cancel count as cancelled work if inside the {} ms batch window)", report["verdict"], window_ms);
    }
    if ok {
        0
    } else {
        1
    }
}

fn default_data_dir() -> PathBuf {
    if let Ok(x) = std::env::var("IGNEUM_CLI_DATA_DIR") {
        return PathBuf::from(x);
    }
    let home = PathBuf::from(std::env::var("HOME").or_else(|_| std::env::var("USERPROFILE")).unwrap_or_else(|_| ".".into()));
    // the old name's directory is kept where it exists (the user's settings are preserved across the rename)
    let old = home.join(".igneum-cli");
    let new = home.join(".ember");
    if old.is_dir() && !new.exists() {
        return old;
    }
    new
}

fn devices(cfg: Config, json: bool) -> i32 {
    let sup = Supervisor::new(cfg);
    match sup.inventory() {
        Ok(v) => {
            if json {
                println!("{}", serde_json::to_string_pretty(&v).unwrap_or_default());
            } else {
                println!("{:<4} {:<8} {:<36} {:>8} {:>8} {:<12} {:<20} {}", "idx", "backend", "name", "total", "free", "arch", "label", "fingerprint");
                for d in v {
                    println!("{:<4} {:<8} {:<36} {:>6.1}GB {:>6.1}GB {:<12} {:<20} {}", d.index, d.backend, d.name.chars().take(36).collect::<String>(), d.memory_total as f64 / (1u64 << 30) as f64, d.memory_free as f64 / (1u64 << 30) as f64, d.arch.chars().take(12).collect::<String>(), format!("{:?}", d.label), d.fingerprint);
                    println!("     {}{}", d.reason, if d.worker_note.is_empty() { String::new() } else { format!(" [{}]", d.worker_note) });
                    if let Some(o) = &d.power_offer {
                        println!("     {o}");
                    }
                }
                let snap = sup.snapshot();
                for e in &snap.launch_errors {
                    println!("not started: {e}");
                }
            }
            0
        }
        Err(e) => {
            eprintln!("{e}");
            2
        }
    }
}

fn apply_args(sup: &Supervisor, a: &MineArgs) -> Result<(), String> {
    sup.update_settings(|s| {
        if let Some(addr) = &a.address {
            s.payout_address = Some(igneum_supervisor::settings::validate_address(addr)?);
        }
        if a.stub {
            s.work_source = WorkSourceSetting::Stub;
        } else if let Some(p) = &a.pool {
            let (host, port) = p.rsplit_once(':').ok_or("--pool is host:port")?;
            s.work_source = WorkSourceSetting::PoolCustom { host: host.to_string(), port: port.parse().map_err(|_| "the port is a number")?, tls: !a.no_tls };
        } else if !a.node.is_empty() {
            // several nodes: one setting, the addresses comma-separated in the order given; the lab switch names
            // the template node for the source through its variable
            if let Some(t) = a.template_node.as_deref().filter(|t| !t.trim().is_empty()) {
                std::env::set_var("EMBER_TEMPLATE_NODE", t.trim());
            }
            if a.pace_chain_target {
                std::env::set_var("EMBER_PACE_CHAIN_TARGET", "1");
            }
            s.work_source = WorkSourceSetting::Solo { rpc_url: a.node.join(",") };
        } else if matches!(s.work_source, WorkSourceSetting::Unset) {
            // nothing named and nothing saved: the project's pool (Ember 0.2.1, the founder's order of 11 October
            // 2026; plain TCP on 3341 tonight), the same default as the app's; --pool or --node overrides it
            s.work_source = WorkSourceSetting::PoolCustom { host: "pool.igneum.network".into(), port: 3341, tls: false };
        }
        if let Some(p) = &a.pool_backup {
            let (host, port) = p.rsplit_once(':').ok_or("--pool-backup is host:port")?;
            s.work_source_backup = WorkSourceSetting::PoolCustom { host: host.to_string(), port: port.parse().map_err(|_| "the port is a number")?, tls: !a.no_tls };
        }
        if a.no_pool_fee {
            s.no_pool_fee = true;
        }
        if let Some(w) = &a.worker_name {
            s.worker_name = w.clone();
        }
        if a.algo.is_some() {
            s.algo = a.algo.clone();
        }
        if let Some(m) = &a.mode {
            s.mode = match m.as_str() {
                "quiet" => Mode::Quiet,
                "max" => Mode::Max,
                _ => Mode::Efficient,
            };
        }
        // F-E18: with a config file read, the file's word is the whole word for the limit keys: a key it no longer
        // holds (and no flag) clears the value the stored settings kept from an earlier run
        if a.power_cap.is_some() || a.conf_read {
            s.limits.power_cap_w = a.power_cap;
        }
        if a.temp_cap.is_some() || a.conf_read {
            s.limits.temp_cap_c = a.temp_cap;
        }
        if let Some(c) = a.coord {
            s.coordinate_pin = Some(c);
        }
        if let Some(u) = a.registry_rpc.as_ref().filter(|u| !u.trim().is_empty()) {
            s.registry_rpc = Some(u.trim().to_string());
        }
        if a.fan_cap.is_some() {
            s.limits.fan_cap_pct = a.fan_cap;
        }
        if let Some(r) = a.min_rate {
            if r <= 1.0 {
                s.limits.min_fraction = r;
                s.limits.min_rate_hps = None;
            } else {
                s.limits.min_rate_hps = Some(r);
            }
        }
        if let Some(t) = a.tariff {
            s.tariff = Tariff::Flat { rate: t };
        }
        if let Some(c) = &a.currency {
            s.costs.currency = c.to_ascii_uppercase();
        }
        if a.wall_watts.is_some() {
            s.costs.wall_watts = a.wall_watts;
        }
        if a.price_per_ign.is_some() {
            s.costs.price_per_ign = a.price_per_ign;
        }
        if let Some(o) = a.other_per_day {
            s.costs.other_per_day = o;
        }
        s.setup_done = true;
        Ok(())
    })
    .map_err(|e| e.to_string())
}

fn mine(cfg: Config, a: MineArgs, json: bool, tune_budget: Option<u64>, bench_seconds: Option<u64>) -> i32 {
    let sup = Supervisor::new(cfg);
    if let Err(e) = apply_args(&sup, &a) {
        eprintln!("{e}");
        return 2;
    }
    let inv = match sup.inventory() {
        Ok(v) => v,
        Err(e) => {
            eprintln!("{e}");
            return 2;
        }
    };
    if !a.device.is_empty() {
        // bound by fingerprint alone; an absent card refuses the start with the cards seen, never a fall-through
        // to another card (the founder's word of 11 October 2026: the enclosure moves)
        let chosen = match igneum_supervisor::devices_named(&a.device, &inv) {
            Ok(v) => v,
            Err(e) => {
                eprintln!("{e}");
                return 2;
            }
        };
        let pins: Vec<(String, f64)> = a.pin.iter().filter_map(|p| p.split_once('=').and_then(|(k, v)| v.parse().ok().map(|v| (k.to_string(), v)))).collect();
        let all: Vec<String> = inv.iter().map(|d| d.fingerprint.clone()).collect();
        if let Err(e) = sup.update_settings(|s| {
            s.devices_off = all.iter().filter(|f| !chosen.contains(f)).cloned().collect();
            s.devices_on = chosen.clone();
            for f in &chosen {
                let e = s.pins.entry(f.clone()).or_default();
                for (k, v) in &pins {
                    e.insert(k.clone(), *v);
                }
            }
            Ok(())
        }) {
            eprintln!("{e}");
            return 2;
        }
    }
    if let Err(e) = sup.start() {
        eprintln!("{e}");
        return 3;
    }
    // F-E20: the chain's figures for the Earnings estimate, the supervisor's own feed poll (no app here)
    sup.start_chain_figures_poll();
    // the dev fee on the first lines (ED-05): the rate, the form, the address and the switch, or why it is off
    println!("{}", sup.snapshot().totals.dev_fee_words);
    // the fee-waiver handshake's line (DESIGN.md 16.3): "Dev fee 1 percent; pool fee 0 (waived)" once the pool has
    // answered the declaration (a node session names the dev fee alone); printed once, when it first reads
    let mut fee_line_said = String::new();
    let stop = Arc::new(AtomicBool::new(false));
    {
        let s = stop.clone();
        let _ = ctrlc::set_handler(move || s.store(true, Ordering::SeqCst));
    }
    if let Some(bind) = &a.api {
        serve_api(sup.clone(), bind.clone());
    }
    let ticker = sup.spawn_ticker();
    let _watchdog = sup.spawn_watchdog();
    let mut last_print = std::time::Instant::now() - Duration::from_secs(3600);
    let started = std::time::Instant::now();
    let mut tune_started = false;
    let mut code = 0;
    let mut log_seq: u64 = 0;
    loop {
        if stop.load(Ordering::SeqCst) {
            // never silent: a termination signal (SIGINT, SIGTERM or SIGHUP, from a terminal, a service manager or
            // a dropped session) is the only way here
            eprintln!("stopping: a termination signal arrived (SIGINT, SIGTERM or SIGHUP; a dropped ssh session sends SIGHUP: run under the service unit, nohup or setsid to outlive it)");
            break;
        }
        if let Some(w) = sup.wedged() {
            // the core's lock is wedged: no snapshot can be taken; say so and exit for the service wrapper to restart
            eprintln!("mining stopped: {w}");
            code = 6;
            break;
        }
        let snap = sup.snapshot();
        if let Some(f) = &snap.tick_fault {
            eprintln!("mining stopped: {f}");
            print_state(&snap, json);
            code = 6;
            break;
        }
        if !snap.totals.fee_words.is_empty() && snap.totals.fee_words != fee_line_said && !snap.totals.fee_words.contains("waiting for the pool") {
            fee_line_said = snap.totals.fee_words.clone();
            println!("{fee_line_said}");
        }
        if matches!(snap.mining, MiningState::Idle) && started.elapsed() > Duration::from_secs(5) {
            eprintln!("mining stopped: {}", snap.notices.last().map(|n| n.text.clone()).unwrap_or_default());
            code = 4;
            break;
        }
        if let Some(budget) = tune_budget {
            if !tune_started && matches!(snap.mining, MiningState::Mining) {
                tune_started = true;
                if let Err(e) = sup.tune(None) {
                    eprintln!("{e}");
                    code = 3;
                    break;
                }
            }
            if tune_started && !snap.tune_running && started.elapsed() > Duration::from_secs(5) {
                print_tune(&snap, json);
                break;
            }
            if started.elapsed() > Duration::from_secs(budget + 120) {
                eprintln!("the tune did not finish inside its budget");
                code = 5;
                break;
            }
        }
        if let Some(secs) = bench_seconds {
            if started.elapsed() > Duration::from_secs(secs) {
                print_bench(&snap, json);
                break;
            }
        }
        if last_print.elapsed() >= Duration::from_secs(a.every.max(1)) {
            print_state(&snap, json);
            last_print = std::time::Instant::now();
        }
        // the supervisor's own lines (every move of the router, every refusal, every reload) go to stderr with the
        // status, so a service's journal or a soak's log carries them (the soak pods, 04:0x UK 13 October 2026: the
        // log held the status line only and the record could read no switch from it)
        let (seq, lines) = sup.log_since(log_seq);
        log_seq = seq;
        for l in lines {
            eprintln!("sup: {l}");
        }
        thread::sleep(Duration::from_millis(250));
    }
    if let Some(path) = &a.report {
        let snap = sup.snapshot();
        let mut v = serde_json::to_value(&snap).unwrap_or_default();
        if let Some(o) = v.as_object_mut() {
            o.remove("settings");
            o.insert("report_unix".into(), serde_json::json!(std::time::SystemTime::now().duration_since(std::time::UNIX_EPOCH).map(|d| d.as_secs()).unwrap_or(0)));
            o.insert("log".into(), serde_json::json!(sup.log_tail(500)));
        }
        match std::fs::write(path, serde_json::to_vec_pretty(&v).unwrap_or_default()) {
            Ok(()) => eprintln!("report written to {}", path.display()),
            Err(e) => eprintln!("report not written to {}: {e}", path.display()),
        }
    }
    sup.request_stop();
    let _ = ticker.join();
    code
}

fn print_state(s: &Snapshot, json: bool) {
    if json {
        println!("{}", serde_json::to_string(s).unwrap_or_default());
        return;
    }
    let t = &s.totals;
    let waiting = s.source.as_ref().map_or(false, |src| !src.connected || src.current_job.is_none());
    let devs: Vec<String> = s.devices.iter().filter(|d| d.enabled).map(|d| {
        let tel = d.telemetry.as_ref();
        let state = if waiting && matches!(d.state, igneum_supervisor::state::SlotState::Mining) { "WaitingForWork".to_string() } else { format!("{:?}", d.state) };
        let en = d.energy.as_ref().map(|e| format!(" {:.1} MH/Wh ({})", e.mh_per_wh, e.grade)).unwrap_or_default();
        format!("{}[{state} {:.2}MH/s {:.0}W {:.0}C{en}]", d.index, d.validated_hps / 1e6, tel.and_then(|t| t.power_w).unwrap_or(0.0), tel.and_then(|t| t.temp_c).unwrap_or(0.0))
    }).collect();
    let est = s.estimate_lines.iter().find(|l| l.label == "Estimated net").map(|l| l.value.clone()).unwrap_or_default();
    let receipts = s.estimate_lines.iter().find(|l| l.label == "Estimated receipts").map(|l| format!(" receipts {} ({})", l.value, l.note)).unwrap_or_default();
    let sw = match (t.switch_median_ms, t.switch_p99_ms) {
        (Some(m), Some(p)) => format!(" switch p50 {m} ms p99 {p} ms ({} samples)", t.switch_samples),
        _ => String::new(),
    };
    let tr = if t.transitions > 0 { format!(" transitions {} ({:.0} hashes charged)", t.transitions, t.transition_lost_hashes) } else { String::new() };
    let uv = if t.unverified > 0 { format!(" unverified {}", t.unverified) } else { String::new() };
    // security's rule (11 October 2026): a keyed job goes only to a worker on this machine; a device left off
    // under a keyed document for its transport is named on the line, so the operator sees why it is not mining
    let ko = s.devices.iter().filter(|d| !d.enabled && d.message.contains("keyed sealing runs only on this machine")).count();
    let ko = if ko > 0 { format!(" keyed-off {ko} (keyed sealing runs only on this machine's cards)") } else { String::new() };
    // the dev fee's own count (devfee.rs): the templates requested with the dev address and the shares accepted
    // on them, so the record shows the fee beside the miner's figures
    let ko = if t.dev_fee_percent > 0 { format!("{ko} DEV FEE {} percent: templates {} shares accepted {}", t.dev_fee_percent, t.dev_fee_templates, t.dev_fee_shares_accepted) } else { ko };
    // the pool's side of the handshake (16.3) and its receipts, on a pool session
    let ko = match &t.pool_fee {
        Some(p) if t.pool_fee_answered => format!("{ko} POOL FEE {} bps{}{}{}", p.bps, if p.waived { " (waived)" } else { "" }, if t.receipts.total > 0 { format!(" receipts {} (verified {}, failed {})", t.receipts.total, t.receipts.v1_verified, t.receipts.v1_failed) } else { String::new() }, igneum_supervisor::devfee::share_step_words(t.share_step)),
        _ => ko,
    };
    let mp = s.memory_pressure.as_deref().filter(|l| *l != "normal").map(|l| format!(" MEMORY PRESSURE {l}")).unwrap_or_default();
    // the stale word follows the watchdog's rule: a tick that should be beating (mining) and is not; a start holds
    // the tick through the workers' hello wait and is not a stall (the Linux parity row of 879295c7 read "TICK STALE
    // 19 s" on its Starting line)
    let stale = matches!(s.mining, MiningState::Mining) && s.tick_age_ms > 5000;
    let tk = format!(" tick max {} ms check {} ms{}{mp}", t.tick_max_ms, t.verify_median_ms, if stale { format!(" TICK STALE {} s", s.tick_age_ms / 1000) } else { String::new() });
    let cn = if t.cancelled > 0 || t.unverified > 0 { format!(" cancelled {}{}{}", t.cancelled, if t.verify_late > 0 { format!(" (late {})", t.verify_late) } else { String::new() }, if t.unverified > 0 { format!(" unverified {}", t.unverified) } else { String::new() }) } else { String::new() };   // F-271: the verify queue's drops on the line
    let src = if t.source_switches > 0 { format!(" source {} (switches {})", s.source.as_ref().map(|x| x.name.clone()).unwrap_or_default(), t.source_switches) } else { String::new() };
    let rr = if t.reject_reasons.is_empty() { String::new() } else { format!(" ({})", t.reject_reasons.iter().map(|(k, v)| format!("{k} {v}")).collect::<Vec<_>>().join(", ")) };
    let ns = if t.not_submitted > 0 { format!(" not-submitted {} (target too low)", t.not_submitted) } else { String::new() };
    // the project's reading of the 5090 Windows host's row (15 October 2026): "stale 23746" beside accepted 139 read as a storm; the counter is the
    // shares the pool answered stale (the job had moved on) plus submissions aged out unanswered, and says so
    let st = if t.stale > 0 { format!(" (the job moved on: answered stale by the pool or never answered)") } else { String::new() };
    let bl = {
        let last = t.last_submit_age_s.map(|a| format!(", last {a} s ago")).unwrap_or_default();
        let exp = t.expected_block_s.map(|e| format!(", one expected every {e:.0} s")).unwrap_or_default();
        let rc = if t.source_reconnects > 0 { format!(", reconnects {}", t.source_reconnects) } else { String::new() };
        format!(" submitted {}{last}{exp}{rc}", t.submitted)
    };
    let en = t.energy.as_ref().map(|e| format!(" energy {:.1} MH/Wh, {:.2} uJ per hash ({})", e.mh_per_wh, e.joules_per_hash * 1e6, e.grade)).unwrap_or_default();
    // the share lane (LAUNCH-GUARDS.md 2.9): the node's accepted and refused counts by its reason, and the expected
    // seconds between shares at the operating count (the block expectation over shares a block)
    let sl = if t.share_lane {
        let why = if t.share_refused_reasons.is_empty() { String::new() } else { format!(" ({})", t.share_refused_reasons.iter().map(|(k, v)| format!("{k} {v}")).collect::<Vec<_>>().join(", ")) };
        let exp = match (t.expected_block_s, t.shares_per_block) {
            (Some(e), n) if n > 0 => format!(", one expected every {:.0} s", e / n as f64),
            _ => String::new(),
        };
        // the pacing rule (the project's ruling of 11 October 2026): the job target is the measurement target, so the
        // worker reports about a result a second and keeps hashing; every verified result under the share target
        // is submitted, the cap stands, and what the verifier never reached is not work
        // by node (every share to every node the source names, until share gossip lands): each node's accepted
        // and refused counts with its reasons
        let by_node = if t.share_nodes.len() > 1 || t.share_nodes.iter().any(|n| n.refused > 0) {
            format!(" by node: {}", t.share_nodes.iter().map(|n| {
                let r = if n.reasons.is_empty() { String::new() } else { format!(" ({})", n.reasons.iter().map(|(k, v)| format!("{k} {v}")).collect::<Vec<_>>().join(", ")) };
                format!("{} accepted {} refused {}{r}", n.node, n.accepted, n.refused)
            }).collect::<Vec<_>>().join("; "))
        } else {
            String::new()
        };
        format!(" SHARE LANE accepted {} refused {}{why}{exp} ({} a block; pacing: {}){by_node}", t.shares_accepted, t.shares_refused, t.shares_per_block, if t.share_pacing.is_empty() { "measurement (the job target is the measurement target, every verified share submitted)" } else { t.share_pacing.as_str() })
    } else {
        String::new()
    };
    // the last minute per device (the parity rows: three consecutive lines at --every 60 are three 60 s windows)
    let lm: String = s.devices.iter().filter(|d| d.enabled && d.reported_60s_hps > 0.0).map(|d| format!(" last-minute {} {:.2} MH/s{}", d.index, d.reported_60s_hps / 1e6, d.power_60s_w.map(|p| format!(" at {p:.0} W")).unwrap_or_default())).collect();
    println!("{:?} validated {} (reported {:.3}) accepted {} rejected {}{rr}{ns} stale {}{st} invalid {} source-invalid {} restarts {} up {}s down {}s preparing {}s{sw}{tr}{uv}{ko}{cn}{tk}{bl}{sl}{en}{lm}{src} net {}{} {}{}", s.mining, igneum_supervisor::state::rate_words(t.validated_hps), t.reported_hps / 1e6, t.accepted, t.rejected, t.stale, t.invalid, t.source_invalid, t.worker_restarts, t.uptime_s, t.downtime_s, t.preparing_s, est, receipts, devs.join(" "), if s.stub { " [stub: nothing is mined]" } else { "" });
    for n in &s.notices {
        if n.level != "info" {
            println!("  ! {} {}", n.text, n.detail);
        }
    }
}

fn print_tune(s: &Snapshot, json: bool) {
    if json {
        println!("{}", serde_json::to_string_pretty(&s.devices.iter().map(|d| (&d.fingerprint, &d.tune)).collect::<Vec<_>>()).unwrap_or_default());
        return;
    }
    for d in &s.devices {
        if let Some(t) = &d.tune {
            println!("{}: chosen {} ({} candidates, {:?})", d.name, t.chosen.iter().map(|(k, v)| format!("{k}={v}")).collect::<Vec<_>>().join(" "), t.candidates, t.stopped);
        }
    }
}

fn print_bench(s: &Snapshot, json: bool) {
    if json {
        println!("{}", serde_json::to_string_pretty(&s.devices).unwrap_or_default());
        return;
    }
    println!("bench (device telemetry, not a wall measurement{})", if s.stub { "; stub: nothing is mined" } else { "" });
    for d in &s.devices {
        if d.enabled {
            let tel = d.telemetry.as_ref();
            let p = tel.and_then(|t| t.power_w);
            let en = d.energy.as_ref().map(|e| format!(", {:.1} MH per Wh, {:.2} uJ per accepted hash ({} grade)", e.mh_per_wh, e.joules_per_hash * 1e6, e.grade)).unwrap_or_else(|| ", energy per hash: no power reading from this device".into());
            println!("{}: validated {:.3} MH/s, reported {:.3} MH/s, {} W, {} per W{en}, invalid {}", d.name, d.validated_hps / 1e6, d.reported_hps / 1e6, p.map_or("no power reading".into(), |p| format!("{p:.0}")), p.filter(|p| *p > 0.0).map_or("n/a".into(), |p| format!("{:.0} H/s", d.validated_hps / p)), d.invalid_results);
        }
    }
}

fn verify(file: PathBuf, json: bool) -> i32 {
    let f = match std::fs::File::open(&file) {
        Ok(f) => f,
        Err(e) => {
            eprintln!("{}: {e}", file.display());
            return 2;
        }
    };
    let (mut valid, mut invalid, mut bad) = (0u64, 0u64, 0u64);
    for line in BufReader::new(f).lines().map_while(Result::ok) {
        if line.trim().is_empty() {
            continue;
        }
        let v: serde_json::Value = match serde_json::from_str(&line) {
            Ok(v) => v,
            Err(_) => {
                bad += 1;
                continue;
            }
        };
        let get = |k: &str| v.get(k).and_then(|x| x.as_str()).unwrap_or("");
        let algo = if get("algo").is_empty() { proto::stubhash::STUB_ALGO } else { get("algo") };
        let height = v.get("height").and_then(|x| x.as_u64().or_else(|| x.as_str().and_then(|s| s.parse().ok())));
        let nonce = v.get("nonce").and_then(|x| x.as_u64().or_else(|| x.as_str().and_then(|s| s.parse().ok())));
        let (Some(height), Some(header), Some(nonce), Some(mix), Some(hash)) = (height, proto::hex_to_32(get("header")), nonce, proto::hex_decode(get("mix")), proto::hex_to_32(get("hash"))) else {
            bad += 1;
            continue;
        };
        match igneum_supervisor::verify::verifier_for(algo) {
            Ok(ver) => match ver.verify(height, &header, nonce, &mix, &hash) {
                Ok(true) => valid += 1,
                Ok(false) => invalid += 1,
                Err(_) => bad += 1,
            },
            Err(_) => bad += 1,
        }
    }
    if json {
        println!("{{\"valid\":{valid},\"invalid\":{invalid},\"malformed\":{bad}}}");
    } else {
        println!("valid {valid}, invalid {invalid}, malformed {bad}");
    }
    if invalid > 0 || bad > 0 {
        1
    } else {
        0
    }
}

/// A read-only local endpoint: GET /state (JSON) and GET /metrics (Prometheus text). No settings, no address.
fn serve_api(sup: Supervisor, bind: String) {
    let listener = match TcpListener::bind(&bind) {
        Ok(l) => l,
        Err(e) => {
            eprintln!("--api {bind}: {e}");
            return;
        }
    };
    thread::spawn(move || {
        for stream in listener.incoming() {
            let Ok(mut s) = stream else { continue };
            let mut line = String::new();
            let mut r = BufReader::new(s.try_clone().expect("clone"));
            if r.read_line(&mut line).is_err() {
                continue;
            }
            let method = line.split_whitespace().next().unwrap_or("GET").to_string();
            let path = line.split_whitespace().nth(1).unwrap_or("/");
            // F-E22: pause and resume by POST alone (a browser's GET changes nothing); the answer is the state word
            let (status, ctype, body) = match (method.as_str(), path) {
                ("POST", "/pause") => { sup.pause(); let snap = sup.snapshot(); ("200 OK", "application/json", format!("{{\"ok\":true,\"mining\":{}}}\n", serde_json::to_string(&snap.mining).unwrap_or_default())) }
                ("POST", "/resume") => { sup.resume(); let snap = sup.snapshot(); ("200 OK", "application/json", format!("{{\"ok\":true,\"mining\":{}}}\n", serde_json::to_string(&snap.mining).unwrap_or_default())) }
                (_, "/pause") | (_, "/resume") => ("405 Method Not Allowed", "text/plain", "POST only\n".to_string()),
                (_, "/state") => { let snap = sup.snapshot(); ("200 OK", "application/json", state_public(&snap)) }
                (_, "/metrics") => { let snap = sup.snapshot(); ("200 OK", "text/plain; version=0.0.4", metrics(&snap)) }
                _ => ("404 Not Found", "text/plain", "state or metrics (GET), pause or resume (POST)\n".to_string()),
            };
            let _ = write!(s, "HTTP/1.1 {status}\r\nContent-Type: {ctype}\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{body}", body.len());
        }
    });
}

/// F-E22: `ember pause` and `ember resume` reach the running miner through its API bind (the config file's `api`
/// key, else 127.0.0.1:4068) with one POST and print the miner's state word; a miner that does not answer is said.
fn control(what: &str, api: Option<String>, config: Option<PathBuf>) -> i32 {
    let conf_path = config.or_else(default_conf_path);
    let from_conf = conf_path.as_ref().and_then(|p| read_conf(p).ok()).and_then(|m| m.get("api").map(|s| s.trim().to_string())).filter(|s| !s.is_empty());
    let bind = api.or(from_conf).unwrap_or_else(|| "127.0.0.1:4068".to_string());
    let addr = bind.trim_start_matches("http://").trim_end_matches('/').to_string();
    let mut stream = match std::net::TcpStream::connect_timeout(&match addr.parse::<std::net::SocketAddr>() { Ok(a) => a, Err(_) => { eprintln!("the API bind {addr} is not host:port"); return 2; } }, Duration::from_secs(5)) {
        Ok(s) => s,
        Err(e) => { eprintln!("no miner answers at {addr} ({e}); is Ember running with --api or the config's api key?"); return 3; }
    };
    let _ = stream.set_read_timeout(Some(Duration::from_secs(10)));
    let _ = write!(stream, "POST /{what} HTTP/1.1\r\nHost: {addr}\r\nContent-Length: 0\r\nConnection: close\r\n\r\n");
    let mut text = String::new();
    let _ = std::io::Read::read_to_string(&mut stream, &mut text);
    let status = text.lines().next().unwrap_or("").to_string();
    let body = text.split("\r\n\r\n").nth(1).unwrap_or("").trim().to_string();
    if status.contains("200") {
        let state = serde_json::from_str::<serde_json::Value>(&body).ok().and_then(|v| v.get("mining").map(|m| m.to_string().trim_matches('"').to_lowercase())).unwrap_or_else(|| body.clone());
        println!("{what}d: the miner is {state} (at {addr}; the session stays dialled, the log carries the minute)");
        0
    } else {
        eprintln!("the miner at {addr} answered: {status} {body}");
        4
    }
}

fn state_public(s: &Snapshot) -> String {
    let mut v = serde_json::to_value(s).unwrap_or_default();
    if let Some(o) = v.as_object_mut() {
        o.remove("settings");
    }
    serde_json::to_string(&v).unwrap_or_default()
}

fn metrics(s: &Snapshot) -> String {
    let t = &s.totals;
    let mut out = String::new();
    out.push_str(&format!("# TYPE igneum_validated_hps gauge\nigneum_validated_hps {}\n", t.validated_hps));
    out.push_str(&format!("# TYPE igneum_reported_hps gauge\nigneum_reported_hps {}\n", t.reported_hps));
    out.push_str(&format!("# TYPE igneum_accepted_shares_total counter\nigneum_accepted_shares_total {}\n", t.accepted));
    out.push_str(&format!("# TYPE igneum_rejected_shares_total counter\nigneum_rejected_shares_total {}\n", t.rejected));
    out.push_str(&format!("# TYPE igneum_stale_shares_total counter\nigneum_stale_shares_total {}\n", t.stale));
    out.push_str(&format!("# TYPE igneum_invalid_results_total counter\nigneum_invalid_results_total {}\n", t.invalid));
    out.push_str(&format!("# TYPE igneum_source_invalid_total counter\nigneum_source_invalid_total {}\n", t.source_invalid));
    out.push_str(&format!("# TYPE igneum_preparing_seconds counter\nigneum_preparing_seconds {}\n", t.preparing_s));
    out.push_str(&format!("# TYPE igneum_worker_restarts_total counter\nigneum_worker_restarts_total {}\n", t.worker_restarts));
    out.push_str(&format!("# TYPE igneum_uptime_seconds counter\nigneum_uptime_seconds {}\n", t.uptime_s));
    out.push_str(&format!("# TYPE igneum_downtime_seconds counter\nigneum_downtime_seconds {}\n", t.downtime_s));
    out.push_str(&format!("# TYPE igneum_tune_state gauge\nigneum_tune_state {}\n", if s.tune_running { 1 } else { 0 }));
    out.push_str("# TYPE igneum_device_power_watts gauge\n# TYPE igneum_device_temp_celsius gauge\n# TYPE igneum_device_validated_hps gauge\n");
    for d in &s.devices {
        let l = format!("{{device=\"{}\",backend=\"{}\"}}", d.index, d.backend);
        if let Some(t) = &d.telemetry {
            if let Some(p) = t.power_w {
                out.push_str(&format!("igneum_device_power_watts{l} {p}\n"));
            }
            if let Some(c) = t.temp_c {
                out.push_str(&format!("igneum_device_temp_celsius{l} {c}\n"));
            }
        }
        out.push_str(&format!("igneum_device_validated_hps{l} {}\n", d.validated_hps));
    }
    out
}

/// The bounded menu's evidence, one worker, no node: for each entry (a parameter document), the budget the worker
/// states at ready (cold and warm fit against the card's memory), the dataset build, the first result, the rate,
/// the peak memory and code the worker reports, and the overhead of the switch from the previous entry (results
/// for the old entry after the switch, the delay to the new entry's first result).
fn fit(cfg: Config, worker: Option<String>, algo: Option<String>, entries: Option<PathBuf>, seconds: u64, report_path: Option<PathBuf>, json: bool) -> i32 {
    use igneum_supervisor::work::params_digest;
    use igneum_supervisor::worker::Inbound;
    use igneum_worker_proto::{Configure, Envelope, FromWorker, Job, ToWorker, Transition, WorkerState};
    use std::time::Instant;
    let pick = |name: &str| cfg.launchers.iter().find(|l| l.exe.file_name().map_or(false, |n| n.to_string_lossy().starts_with(name))).cloned();
    let launcher = match worker.as_deref() {
        Some(w) => pick(w),
        None => pick("igneum-worker-cpu").or_else(|| pick("igneum-worker-stub")),
    };
    let Some(launcher) = launcher else {
        eprintln!("no worker found");
        return 2;
    };
    let menu: Vec<(String, serde_json::Value)> = match entries {
        Some(path) => {
            let text = match std::fs::read_to_string(&path) {
                Ok(t) => t,
                Err(e) => {
                    eprintln!("{}: {e}", path.display());
                    return 2;
                }
            };
            let v: serde_json::Value = match serde_json::from_str(&text) {
                Ok(v) => v,
                Err(e) => {
                    eprintln!("{}: not JSON: {e}", path.display());
                    return 2;
                }
            };
            let arr = v.get("entries").and_then(|e| e.as_array()).cloned().or_else(|| v.as_array().cloned()).unwrap_or_default();
            arr.iter().enumerate().map(|(i, e)| (e.get("name").and_then(|n| n.as_str()).map(String::from).unwrap_or_else(|| format!("entry {}", i + 1)), e.get("params").cloned().unwrap_or(serde_json::Value::Null))).collect()
        }
        None => [1024u64, 2048, 4096].iter().map(|m| (format!("v1 {} GiB", m / 1024), serde_json::json!({"version": 1, "dataset_mib": m}))).collect(),
    };
    if menu.is_empty() {
        eprintln!("the menu is empty");
        return 2;
    }
    let mut p = match launcher.launch() {
        Ok(p) => p,
        Err(e) => {
            eprintln!("{e}");
            return 2;
        }
    };
    let algo = algo.unwrap_or_else(|| "igneumhash".into());
    let device = p.hello.devices.first().map(|d| d.id.clone()).unwrap_or_default();
    let device_name = p.hello.devices.first().map(|d| d.name.clone()).unwrap_or_default();
    let device_memory = p.hello.devices.first().map(|d| d.memory_bytes).unwrap_or(0);
    let mut rows: Vec<serde_json::Value> = Vec::new();
    let mut prev_job: Option<String> = None;
    for (k, (name, doc)) in menu.iter().enumerate() {
        let digest = params_digest(doc);
        let mut row = serde_json::json!({ "entry": name, "params": doc, "params_digest": digest, "algo": algo });
        let t_switch = Instant::now();
        let _ = p.send(&ToWorker::Configure(Configure { device: device.clone(), algo: algo.clone(), params: doc.clone(), stat_ms: 100, transition: Transition::Auto, memory_margin_bytes: 0 }));
        // ready or refuse, counting the old entry's results that arrive after the switch
        let mut budget: Option<igneum_worker_proto::Budget> = None;
        let mut refused: Option<String> = None;
        let mut old_after = 0u64;
        let deadline = Instant::now() + Duration::from_secs(60);
        while Instant::now() < deadline {
            match p.recv_timeout(Duration::from_millis(200)) {
                Some(Inbound::Msg(Envelope { body: FromWorker::Ready(r), .. }, _)) => {
                    budget = Some(r.budget);
                    break;
                }
                Some(Inbound::Msg(Envelope { body: FromWorker::Refuse(r), .. }, _)) => {
                    refused = Some(format!("{}{}", r.reason, match (r.need_bytes, r.have_bytes) { (Some(n), Some(h)) => format!(" (need {:.2} GB, have {:.2} GB)", n as f64 / 1e9, h as f64 / 1e9), _ => String::new() }));
                    break;
                }
                Some(Inbound::Msg(Envelope { body: FromWorker::Share(sh), .. }, _)) => {
                    if prev_job.as_deref() == Some(sh.job_id.as_str()) {
                        old_after += 1;
                    }
                }
                Some(Inbound::Exited) => {
                    refused = Some("the worker exited".into());
                    break;
                }
                _ => {}
            }
        }
        row["old_results_after_switch"] = serde_json::json!(old_after);
        row["ready_ms"] = serde_json::json!(t_switch.elapsed().as_millis() as u64);
        if let Some(b) = &budget {
            row["budget"] = serde_json::json!({ "total_bytes": b.total_bytes, "free_bytes": b.free_bytes, "fit_cold_bytes": b.fit_cold_bytes, "fit_warm_bytes": b.fit_warm_bytes, "can_warm": b.can_warm });
        }
        if let Some(r) = refused {
            row["refused"] = serde_json::json!(r);
            rows.push(row);
            prev_job = None;
            continue;
        }
        let Some(_) = budget else {
            row["refused"] = serde_json::json!("no ready within 60 s");
            rows.push(row);
            prev_job = None;
            continue;
        };
        // the job: the build, the first result, the rate, the peak memory and code reported
        let jid = format!("F{k}");
        let _ = p.send(&ToWorker::Job(Job { job_id: jid.clone(), height: 1, header: format!("{:064x}", 8 + k as u64), boundary: "00".repeat(32), share_target: "0001".to_string() + &"ff".repeat(30), nonce_start: (k as u64) << 40, nonce_count: 1 << 40, clean: true, algo: algo.clone(), sk: None }));
        let t_job = Instant::now();
        let (mut prepared, mut first, mut shares, mut hashes, mut peak_mem, mut peak_gpu, mut code, mut invalid_phase) = (None::<(u64, u64, String)>, None::<u64>, 0u64, 0u64, 0u64, 0u64, None::<u64>, String::new());
        let mut mining_since: Option<Instant> = None;
        let mut end: Option<Instant> = None;
        let hard_end = Instant::now() + Duration::from_secs(seconds + 300);
        loop {
            if Instant::now() > hard_end || end.map_or(false, |e| Instant::now() > e) {
                break;
            }
            match p.recv_timeout(Duration::from_millis(200)) {
                Some(Inbound::Msg(Envelope { body: FromWorker::Prepared(pr), .. }, _)) => prepared = Some((pr.ms, pr.bytes, pr.mode.clone())),
                Some(Inbound::Msg(Envelope { body: FromWorker::Share(sh), .. }, _)) => {
                    if sh.job_id == jid {
                        shares += 1;
                        if first.is_none() {
                            first = Some(t_job.elapsed().as_millis() as u64);
                            end = Some(Instant::now() + Duration::from_secs(seconds.max(1)));
                        }
                    } else if prev_job.as_deref() == Some(sh.job_id.as_str()) {
                        old_after += 1;
                    }
                }
                Some(Inbound::Msg(Envelope { body: FromWorker::Stat(st), .. }, _)) => {
                    if st.job_id == jid && st.state == WorkerState::Mining {
                        if mining_since.is_none() {
                            mining_since = Some(Instant::now());
                        }
                        if mining_since.is_some() {
                            hashes += st.hashes;
                        }
                    }
                    if !st.phase.is_empty() {
                        invalid_phase = st.phase.clone();
                    }
                    peak_mem = peak_mem.max(st.telemetry.memory_used_bytes.unwrap_or(0));
                    peak_gpu = peak_gpu.max(st.telemetry.gpu_allocated_bytes.unwrap_or(0));
                    if st.telemetry.code_bytes.is_some() {
                        code = st.telemetry.code_bytes;
                    }
                }
                Some(Inbound::Msg(Envelope { body: FromWorker::Refuse(r), .. }, _)) => {
                    row["refused"] = serde_json::json!(r.reason);
                    break;
                }
                Some(Inbound::Msg(Envelope { body: FromWorker::Fault(f), .. }, _)) => {
                    row["fault"] = serde_json::json!(f.message);
                    break;
                }
                Some(Inbound::Exited) => {
                    row["fault"] = serde_json::json!("the worker exited");
                    break;
                }
                _ => {}
            }
        }
        let _ = invalid_phase;
        let mined_s = mining_since.map(|t| t.elapsed().as_secs_f64()).unwrap_or(0.0);
        row["old_results_after_switch"] = serde_json::json!(old_after);
        row["prepared"] = match &prepared { Some((ms, bytes, mode)) => serde_json::json!({ "ms": ms, "bytes": bytes, "mode": mode }), None => serde_json::Value::Null };
        row["first_result_ms"] = serde_json::json!(first);
        row["results"] = serde_json::json!(shares);
        row["rate_hps"] = serde_json::json!(if mined_s > 0.0 { hashes as f64 / mined_s } else { 0.0 });
        row["peak_memory_used_bytes"] = serde_json::json!(if peak_mem > 0 { Some(peak_mem) } else { None });
        row["peak_gpu_allocated_bytes"] = serde_json::json!(if peak_gpu > 0 { Some(peak_gpu) } else { None });
        row["code_bytes"] = serde_json::json!(code);
        rows.push(row);
        prev_job = Some(jid);
    }
    let _ = p.shutdown(Duration::from_secs(5));
    let record = serde_json::json!({ "worker": p.hello.worker, "version": p.hello.version, "backend": p.hello.backend, "device": device_name, "device_memory_bytes": device_memory, "entries": rows });
    if let Some(path) = report_path {
        match std::fs::write(&path, serde_json::to_vec_pretty(&record).unwrap_or_default()) {
            Ok(()) => eprintln!("record written to {}", path.display()),
            Err(e) => eprintln!("record not written to {}: {e}", path.display()),
        }
    }
    if json {
        println!("{}", serde_json::to_string_pretty(&record).unwrap_or_default());
        return 0;
    }
    let gb = |v: &serde_json::Value| v.as_u64().map(|b| format!("{:.2}", b as f64 / 1e9)).unwrap_or_else(|| "-".into());
    println!("fit: {} {} ({}) on {} ({:.2} GB)", record["worker"].as_str().unwrap_or(""), record["version"].as_str().unwrap_or(""), record["backend"].as_str().unwrap_or(""), device_name, device_memory as f64 / 1e9);
    println!("| entry | digest | fit cold GB | fit warm GB | warm | dataset build | first result | rate | peak memory GB | code | old results after switch |");
    println!("|---|---|---|---|---|---|---|---|---|---|---|");
    for r in &record["entries"].as_array().cloned().unwrap_or_default() {
        if let Some(x) = r.get("refused").and_then(|x| x.as_str()) {
            println!("| {} | {} | refused: {} | | | | | | | | {} |", r["entry"].as_str().unwrap_or(""), &r["params_digest"].as_str().unwrap_or("")[..12], x, r["old_results_after_switch"]);
            continue;
        }
        let b = &r["budget"];
        let pr = &r["prepared"];
        println!("| {} | {} | {} | {} | {} | {} | {} ms | {:.3} MH/s | {} | {} | {} |", r["entry"].as_str().unwrap_or(""), &r["params_digest"].as_str().unwrap_or("")[..12], gb(&b["fit_cold_bytes"]), gb(&b["fit_warm_bytes"]), if b["can_warm"].as_bool().unwrap_or(false) { "yes" } else { "no" }, if pr.is_null() { "-".to_string() } else { format!("{} ms, {} GB, {}", pr["ms"], gb(&pr["bytes"]), pr["mode"].as_str().unwrap_or("")) }, r["first_result_ms"], r["rate_hps"].as_f64().unwrap_or(0.0) / 1e6, if r["peak_memory_used_bytes"].is_null() { "not reported".to_string() } else { gb(&r["peak_memory_used_bytes"]) }, if r["code_bytes"].is_null() { "not reported".to_string() } else { format!("{} bytes", r["code_bytes"]) }, r["old_results_after_switch"]);
    }
    0
}

/// `ember key`: the work-authority key's public half, its file, the bound payout address and its balance.
fn key_cmd(cfg: Config, node: Option<String>) -> i32 {
    let Some(kp) = cfg.key_path.clone() else {
        eprintln!("no key path");
        return 2;
    };
    let k = match igneum_supervisor::minerkey::MinerKey::load_or_create(&kp) {
        Ok(k) => k,
        Err(e) => {
            eprintln!("{e}");
            return 2;
        }
    };
    let bond_line = match cfg.bond_secret.as_ref() {
        Some(b) => format!("bond key (the keyed rounds, public): {} (the secret is in memory for this run only, from {} or --bond-secret-stdin)", b.public_hex(), igneum_supervisor::bond::BOND_SK_ENV),
        None => "bond key: none in hand (a network whose document carries the bonded rounds needs it from the wallet at start)".to_string(),
    };
    let sup = Supervisor::new(cfg);
    let addr = sup.settings().payout_address.clone().unwrap_or_default();
    println!("work-authority key (Ed25519, public): {}", k.public_hex());
    println!("key file: {} (never printed, never sent)", kp.display());
    println!("{bond_line}");

    println!("payout address bound at sign-in: {}", if addr.is_empty() { "none set" } else { addr.as_str() });
    if let (Some(n), false) = (node, addr.is_empty()) {
        match igneum_supervisor::net::parse_url(&n).and_then(|u| igneum_supervisor::net::json_rpc(&u, 1, "eth_getBalance", &serde_json::json!([addr.to_lowercase(), "latest"]), Duration::from_secs(10))) {
            Ok(raw) => {
                let h = raw.trim().trim_matches('"').trim_start_matches("0x").to_string();
                let wei = u128::from_str_radix(&h, 16).unwrap_or(0);
                println!("balance on the chain at {n}: {:.6} IGN ({wei} wei)", wei as f64 / 1e18);
            }
            Err(e) => println!("balance: the node did not answer: {e}"),
        }
    }
    0
}
