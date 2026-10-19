//! The `build` job (the model is src/jobs.rs, the runner src/jobrun.rs): a Windows PC builds the node and the app
//! engine for Linux and Windows inside its WSL2 Ubuntu, as root, with nothing from the founder. the founder's ask, 4 October 2026
//! evening ("efficiency"): every Windows build went through a GitHub runner at 15 to 25 minutes a round and every
//! Linux binary was cross-compiled on the Mac under the build lock; the two RTX 5090 PCs sit idle on the CPU side.
//!
//! What this module holds is the pure part, so it is unit-tested on the Mac: the job's parameters, the plan read
//! from the inputs manifest (what to build, what to test), the bash stage scripts that run inside the distro, the
//! per-stage time caps, and the parsers for what comes back (free space, the pack stage's outputs file, the relay's
//! JSON replies). The runner (jobrun.rs, `run_build`) does the process work: fetch, wsl.exe per stage, upload.
//!
//! Stages, in order (each a RESULT or STAGE line with a UTC time in the report, each under its own cap):
//!   fetch    the build-inputs zip by https, sha256 checked (src/jobrun.rs fetch_file), the manifest read out of it
//!   setup    apt packages (mingw, clang for bindgen, protoc, zstd), rustup target x86_64-pc-windows-gnu; idempotent
//!   extract  /opt/igneum-build/src replaced by the zip's sources (the target dir /opt/igneum-build/target persists)
//!   linux    cargo build --release per unit, native
//!   windows  cargo build --release --target x86_64-pc-windows-gnu per unit, mingw-w64 (posix threads), static libgcc
//!   test     cargo test --release for the packages the manifest names (Linux, native)
//!   pack     zstd per binary, sha256 of both forms, build-outputs.json, copied to the job folder on the Windows side
//!   upload   every .zst and the outputs file to the relay (fn=upload client token, PUT to Blob, fn=drop); 50 MB each
//! Mining is never stopped: the build is CPU work under `nice`; the app's job runner is serial, so a build never
//! overlaps a shard benchmark (src/jobrun.rs: one `Active` at a time, queued jobs wait).
//!
//! The zip's layout (packaging/windows/push-build-inputs.sh): igneum-build-inputs/{manifest.json, node/ (the fork
//! worktree without target dirs), app/igneum-app/, brand/icons/, proto-cuda/ (without nvrtc/redist)}. The manifest:
//! { "created_at", "node": {"branch","commit","dirty","source"}, "repo": {...}, "app_version",
//!   "builds": [{"dir":"node","packages":["kaspad","igneum-miner"],"features":["kaspad/igneum-pow"],"bins":["igneumd","igneum-miner"],"targets":["linux","windows"]},
//!              {"dir":"app/igneum-app","packages":["igneum-app"],"bins":["igneum-app"],"targets":["linux","windows"],"optional_on":["linux"]}],
//!   "tests":  [{"dir":"app/igneum-app","packages":["igneum-app"]}, {"dir":"node","packages":["igneum-miner"]}] }

#![allow(dead_code)]

use crate::jobs::{self, Job};
use serde_json::Value;
use std::collections::BTreeMap;
use std::time::Duration;

pub const RELAY_URL_DEFAULT: &str = "https://relay.igneum.network";
/// Everything of the build lives here inside the distro: target/ (persists), src/ (per job), out/<id>/ (outputs).
pub const WSL_BASE: &str = "/opt/igneum-build";
pub const ZIP_ROOT: &str = "igneum-build-inputs";
pub const OUTPUTS_FILE: &str = "build-outputs.json";
pub const DEFAULT_DISTRO: &str = "Ubuntu-24.04";
pub const DEFAULT_WSL_USER: &str = "root";
pub const DEFAULT_NICE: u64 = 19;
/// The relay's cap for one Blob upload (relay/lib/relay.mjs MAX_BLOB).
pub const MAX_UPLOAD_BYTES: u64 = 50 * 1024 * 1024;
/// Debian packages the build needs; installed only when `dpkg -s` says they are missing.
pub const APT_COMMON: &[&str] = &["build-essential", "pkg-config", "libssl-dev", "clang", "libclang-dev", "cmake", "unzip", "zstd", "protobuf-compiler", "ca-certificates", "curl", "git"];
pub const APT_WINDOWS: &[&str] = &["gcc-mingw-w64-x86-64", "g++-mingw-w64-x86-64", "binutils-mingw-w64-x86-64", "mingw-w64-x86-64-dev"];

#[derive(Clone, Debug, PartialEq)]
pub struct BuildParams {
    pub targets: Vec<String>,
    pub budget_min: u64,
    pub stage_min: BTreeMap<String, u64>,
    pub min_free_gb: u64,
    pub tests: bool,
    pub relay_url: String,
    pub distro: String,
    pub wsl_user: String,
    pub nice: u64,
    /// cargo -j; 0 = cargo's default (every core)
    pub cargo_jobs: u64,
}

impl BuildParams {
    pub fn from_job(job: &Job) -> BuildParams {
        let mut targets = job.list_param("targets");
        if targets.is_empty() {
            targets = jobs::BUILD_TARGETS.iter().map(|s| s.to_string()).collect();
        }
        targets.dedup();
        let mut stage_min = BTreeMap::new();
        if let Some(o) = job.params.get("stage_minutes").and_then(|v| v.as_object()) {
            for (k, v) in o {
                if let Some(n) = v.as_u64() {
                    stage_min.insert(k.clone(), n.max(1));
                }
            }
        }
        let relay = job.str_param("relay_url");
        let distro = job.str_param("distro");
        let user = job.str_param("wsl_user");
        BuildParams {
            targets,
            budget_min: job.timeout_minutes(),
            stage_min,
            min_free_gb: job.u64_param("min_free_gb").unwrap_or(jobs::DEFAULT_BUILD_MIN_FREE_GB),
            tests: job.params.get("tests").and_then(|v| v.as_bool()).unwrap_or(true),
            relay_url: if relay.is_empty() { RELAY_URL_DEFAULT.to_string() } else { relay.trim_end_matches('/').to_string() },
            distro: if distro.is_empty() { DEFAULT_DISTRO.to_string() } else { distro },
            wsl_user: if user.is_empty() { DEFAULT_WSL_USER.to_string() } else { user },
            nice: job.u64_param("nice").unwrap_or(DEFAULT_NICE).min(19),
            cargo_jobs: job.u64_param("cargo_jobs").unwrap_or(0),
        }
    }
    pub fn wants(&self, target: &str) -> bool {
        self.targets.iter().any(|t| t == target)
    }
}

/// A stage's cap: its own minutes when the job names them, else what is left of the total; never over what is left.
pub fn stage_cap(p: &BuildParams, stage: &str, remaining: Duration) -> Duration {
    match p.stage_min.get(stage) {
        Some(m) => Duration::from_secs(m * 60).min(remaining),
        None => remaining,
    }
}

// ---- the plan from the inputs manifest ------------------------------------------------------------------------------

#[derive(Clone, Debug, PartialEq, Default)]
pub struct Unit {
    /// path inside the zip root ("node", "app/igneum-app")
    pub dir: String,
    pub packages: Vec<String>,
    pub features: Vec<String>,
    /// the binaries cargo leaves in <target dir>/release (and <target dir>/x86_64-pc-windows-gnu/release with .exe)
    pub bins: Vec<String>,
    pub targets: Vec<String>,
    /// targets where a failure is reported but does not fail the job (the engine on Linux)
    pub optional_on: Vec<String>,
}

#[derive(Clone, Debug, PartialEq, Default)]
pub struct TestUnit {
    pub dir: String,
    pub packages: Vec<String>,
}

#[derive(Clone, Debug, PartialEq, Default)]
pub struct Manifest {
    pub created_at: String,
    pub node_branch: String,
    pub node_commit: String,
    /// the 40-hex commit (manifest node.commit_full, packer of 6 October 2026); empty in older manifests
    pub node_commit_full: String,
    /// the commit's author time (manifest node.commit_time, unix seconds): SOURCE_DATE_EPOCH of every build stage, so mimalloc's
    /// __DATE__/__TIME__ and anything else that reads the clock give one answer per commit (6 October 2026, the an earlier release repro)
    pub node_commit_time: u64,
    pub node_dirty: bool,
    pub app_version: String,
    pub builds: Vec<Unit>,
    pub tests: Vec<TestUnit>,
}

fn strings(v: Option<&Value>) -> Vec<String> {
    v.and_then(|a| a.as_array()).map(|a| a.iter().filter_map(|x| x.as_str()).map(|s| s.trim().to_string()).filter(|s| !s.is_empty()).collect()).unwrap_or_default()
}

/// A plain relative directory inside the zip root: no "..", no leading slash, no drive, one or more components.
fn plain_dir(s: &str) -> bool {
    jobs::safe_rel_path(s).is_some()
}

fn plain_name(s: &str) -> bool {
    !s.is_empty() && s.len() <= 80 && s.chars().all(|c| c.is_ascii_alphanumeric() || c == '-' || c == '_' || c == '.' || c == '/') && !s.starts_with('.') && !s.contains("..")
}

/// Reads manifest.json out of the zip. Refuses anything that would not be a plain cargo invocation inside the
/// extracted tree (a dir with "..", a package name with spaces), because these strings go into a shell script.
pub fn parse_manifest(text: &str) -> Result<Manifest, String> {
    let v: Value = serde_json::from_str(text).map_err(|e| format!("manifest.json is not JSON: {e}"))?;
    let s = |k: &str| v.get(k).and_then(|x| x.as_str()).unwrap_or("").trim().to_string();
    let node = v.get("node").cloned().unwrap_or(Value::Null);
    let ns = |k: &str| node.get(k).and_then(|x| x.as_str()).unwrap_or("").trim().to_string();
    let mut m = Manifest { created_at: s("created_at"), node_branch: ns("branch"), node_commit: ns("commit"), node_commit_full: ns("commit_full"), node_commit_time: node.get("commit_time").and_then(|x| x.as_u64()).unwrap_or(0), node_dirty: node.get("dirty").and_then(|x| x.as_bool()).unwrap_or(false), app_version: s("app_version"), ..Default::default() };
    let builds = v.get("builds").and_then(|b| b.as_array()).ok_or("manifest.json has no \"builds\" list")?;
    for (i, b) in builds.iter().enumerate() {
        let dir = b.get("dir").and_then(|x| x.as_str()).unwrap_or("").trim().to_string();
        if !plain_dir(&dir) {
            return Err(format!("builds[{i}].dir '{dir}' is not a plain relative path"));
        }
        let u = Unit { dir, packages: strings(b.get("packages")), features: strings(b.get("features")), bins: strings(b.get("bins")), targets: { let t = strings(b.get("targets")); if t.is_empty() { jobs::BUILD_TARGETS.iter().map(|s| s.to_string()).collect() } else { t } }, optional_on: strings(b.get("optional_on")) };
        if u.packages.is_empty() {
            return Err(format!("builds[{i}] ({}) names no packages", u.dir));
        }
        if u.bins.is_empty() {
            return Err(format!("builds[{i}] ({}) names no bins", u.dir));
        }
        for x in u.packages.iter().chain(u.features.iter()).chain(u.bins.iter()) {
            if !plain_name(x) {
                return Err(format!("builds[{i}] ({}): '{x}' is not a plain name", u.dir));
            }
        }
        for t in &u.targets {
            if !jobs::BUILD_TARGETS.contains(&t.as_str()) {
                return Err(format!("builds[{i}] ({}): target '{t}' is unknown", u.dir));
            }
        }
        m.builds.push(u);
    }
    if m.builds.is_empty() {
        return Err("manifest.json names nothing to build".into());
    }
    for (i, t) in v.get("tests").and_then(|b| b.as_array()).map(|a| a.to_vec()).unwrap_or_default().iter().enumerate() {
        let dir = t.get("dir").and_then(|x| x.as_str()).unwrap_or("").trim().to_string();
        if !plain_dir(&dir) {
            return Err(format!("tests[{i}].dir '{dir}' is not a plain relative path"));
        }
        let packages = strings(t.get("packages"));
        if packages.iter().any(|p| !plain_name(p)) {
            return Err(format!("tests[{i}] ({dir}): a package name is not plain"));
        }
        if !packages.is_empty() {
            m.tests.push(TestUnit { dir, packages });
        }
    }
    Ok(m)
}

// ---- the stage scripts (bash, run as `wsl -d <distro> -u root -- bash <script on /mnt/c>`) ---------------------------

/// What every stage script starts with: the same PATH the working shard job used (run-20261004-173115: cargo and
/// the toolchain live under /root; a login shell from a process without a console need not source ~/.cargo/env),
/// the persistent target dir, no interactive apt.
pub fn prelude(p: &BuildParams, job_id: &str) -> String {
    let mut s = String::new();
    s.push_str("#!/usr/bin/env bash\n");
    s.push_str("set -uo pipefail\n");
    s.push_str("export HOME=/root\n");
    s.push_str("export CARGO_HOME=$HOME/.cargo RUSTUP_HOME=$HOME/.rustup\n");
    s.push_str("CUDA_DIR=$(ls -d /usr/local/cuda-12.* 2>/dev/null | sort -V | tail -1)\n");
    s.push_str("export PATH=\"$HOME/.cargo/bin:${CUDA_DIR:+$CUDA_DIR/bin:}/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin\"\n");
    s.push_str("export DEBIAN_FRONTEND=noninteractive\n");
    s.push_str(&format!("B={WSL_BASE}\n"));
    s.push_str(&format!("SRC=$B/src/{ZIP_ROOT}\n"));
    s.push_str(&format!("OUT=$B/out/{job_id}\n"));
    s.push_str("export CARGO_TARGET_DIR=$B/target\n");
    s.push_str("export CARGO_NET_RETRY=5 CARGO_TERM_COLOR=never CARGO_INCREMENTAL=0\n");
    s.push_str(&format!("NICE=\"nice -n {}\"\n", p.nice));
    s.push_str(&format!("JOBS=\"{}\"\n", if p.cargo_jobs > 0 { format!("-j {}", p.cargo_jobs) } else { String::new() }));
    s.push_str("now() { date -u +%Y-%m-%dT%H:%M:%SZ; }\n");
    s.push_str("mkdir -p \"$B\" \"$OUT\"\n");
    s
}

/// The environment of the Windows target build: Ubuntu's mingw-w64 (posix threads, so libstdc++ has std::thread
/// for rocksdb), bindgen's clang pointed at the mingw headers (librocksdb-sys), static libgcc and winpthread
/// (`-static`; proto-cuda/windows-node/cross-build.sh does the same with Homebrew's toolchain). libstdc++ itself
/// stayed dynamic whatever these flags said (the gcc driver ignores `-static-libstdc++`), and the GCC 13 exe
/// calling into libstdc++-6.dll died at its first rocksdb call (5 October 2026, an earlier release plan section 10);
/// since the fork's database/build.rs (housekeeping) the C++ runtime is static and the exes import no mingw DLL.
/// The DLL copy below stays for a fork without that build script.
pub fn windows_env() -> String {
    let mut s = String::new();
    s.push_str("export CC_x86_64_pc_windows_gnu=x86_64-w64-mingw32-gcc-posix\n");
    s.push_str("export CXX_x86_64_pc_windows_gnu=x86_64-w64-mingw32-g++-posix\n");
    s.push_str("export AR_x86_64_pc_windows_gnu=x86_64-w64-mingw32-ar\n");
    s.push_str("export CARGO_TARGET_X86_64_PC_WINDOWS_GNU_LINKER=x86_64-w64-mingw32-gcc-posix\n");
    s.push_str("export CARGO_TARGET_X86_64_PC_WINDOWS_GNU_RUSTFLAGS=\"-C link-arg=-static -C link-arg=-static-libgcc -C link-arg=-Wl,--no-insert-timestamp\"\n");   // no PE timestamp: reproducible exes (6 Oct 2026)
    s.push_str("export IGNEUM_WINDRES=x86_64-w64-mingw32-windres\n");
    s.push_str("LLVM_LIB=$(ls -d /usr/lib/llvm-*/lib 2>/dev/null | sort -V | tail -1)\n");
    s.push_str("export LIBCLANG_PATH=\"${LLVM_LIB:-/usr/lib/llvm-18/lib}\"\n");
    s.push_str("export BINDGEN_EXTRA_CLANG_ARGS_x86_64_pc_windows_gnu=\"--target=x86_64-w64-mingw32 --sysroot=/usr/x86_64-w64-mingw32 -I/usr/x86_64-w64-mingw32/include\"\n");
    s
}

/// The stage script for `setup`: packages the build needs when dpkg says they are missing, rustup when cargo is
/// missing, the Windows target when wanted. Every step idempotent; every outcome a RESULT line.
pub fn setup_script(p: &BuildParams, job_id: &str) -> String {
    let mut s = prelude(p, job_id);
    s.push_str("echo \"STAGE setup start $(now)\"\n");
    s.push_str("need=\"\"\n");
    let mut pkgs: Vec<&str> = APT_COMMON.to_vec();
    if p.wants("windows") {
        pkgs.extend_from_slice(APT_WINDOWS);
    }
    s.push_str(&format!("for pkg in {}; do dpkg -s \"$pkg\" >/dev/null 2>&1 || need=\"$need $pkg\"; done\n", pkgs.join(" ")));
    s.push_str("if [ -n \"$need\" ]; then echo \"installing:$need\"; apt-get update -qq && apt-get install -y -qq --no-install-recommends $need || { echo \"RESULT setup apt failed for:$need\"; exit 2; }; else echo \"apt packages present\"; fi\n");
    s.push_str("if ! command -v cargo >/dev/null 2>&1; then echo \"installing rustup (minimal)\"; curl -fsSL https://sh.rustup.rs | sh -s -- -y --profile minimal --no-modify-path || { echo \"RESULT setup rustup failed\"; exit 2; }; fi\n");
    if p.wants("windows") {
        s.push_str("rustup target list --installed 2>/dev/null | grep -qx x86_64-pc-windows-gnu || rustup target add x86_64-pc-windows-gnu || { echo \"RESULT setup rustup target x86_64-pc-windows-gnu failed\"; exit 2; }\n");
        s.push_str("command -v x86_64-w64-mingw32-gcc-posix >/dev/null 2>&1 || { echo \"RESULT setup mingw gcc (posix) missing after install\"; exit 2; }\n");
        s.push_str("command -v x86_64-w64-mingw32-windres >/dev/null 2>&1 || { echo \"RESULT setup mingw windres missing after install\"; exit 2; }\n");
    }
    s.push_str("command -v protoc >/dev/null 2>&1 || { echo \"RESULT setup protoc missing after install\"; exit 2; }\n");
    s.push_str("command -v zstd >/dev/null 2>&1 || { echo \"RESULT setup zstd missing after install\"; exit 2; }\n");
    if p.wants("windows") {
        s.push_str("echo \"RESULT setup ok $(cargo --version) | $(rustc --version) | mingw $(x86_64-w64-mingw32-gcc-posix --version 2>/dev/null | head -1 || echo none) | $(now)\"\n");
    } else {
        s.push_str("echo \"RESULT setup ok $(cargo --version) | $(rustc --version) | $(now)\"\n");
    }
    s
}

/// `extract`: the zip (copied from the job folder on /mnt/c) replaces $B/src; the target dir stays.
pub fn extract_script(p: &BuildParams, job_id: &str, zip_wsl: &str) -> String {
    let mut s = prelude(p, job_id);
    s.push_str("echo \"STAGE extract start $(now)\"\n");
    s.push_str(&format!("ZIP='{}'\n", zip_wsl.replace('\'', "'\\''")));
    s.push_str("[ -f \"$ZIP\" ] || { echo \"RESULT extract zip missing at $ZIP\"; exit 2; }\n");
    s.push_str("rm -rf \"$B/src\" && mkdir -p \"$B/src\" && cp \"$ZIP\" \"$B/inputs.zip\" || { echo \"RESULT extract cannot copy the zip into $B\"; exit 2; }\n");
    s.push_str("unzip -q -o \"$B/inputs.zip\" -d \"$B/src\" || { echo \"RESULT extract unzip failed\"; exit 2; }\n");
    // every unpacked file (the zip root and the sibling igneum-pow) is stamped now: the target dir persists between
    // jobs and cargo judges freshness by mtime, so sources that keep the Mac's older mtimes would be taken as unchanged
    // since the last build and new callers linked against stale crates (5 October 2026: the an earlier release job built kaspad
    // against an earlier release's consensus-core). The packer stamps too (push-build-inputs.sh); this guard holds if it regresses.
    s.push_str("find \"$B/src\" -type f -exec touch {} + || { echo \"RESULT extract cannot stamp the sources\"; exit 2; }\n");
    s.push_str("[ -f \"$SRC/manifest.json\" ] || { echo \"RESULT extract no manifest.json under $SRC\"; exit 2; }\n");
    // the commit hash (6 October 2026, found on the store host): the zip has no .git, so kaspa-build-info's build.rs embedded
    // nothing in every PC build. It needs .git to be a directory with HEAD a symbolic ref to a branch file holding the hash
    // (git rev-parse reads it; its fallback reads the file itself). A minimal .git with the manifest's full commit gives the
    // binary its commit string; tools/ci/commit-string-check.sh then passes on the fetched binaries.
    s.push_str("ncf=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1])).get(\"node\",{}).get(\"commit_full\",\"\"))' \"$SRC/manifest.json\" 2>/dev/null || true)\n");
    s.push_str("if [ -d \"$SRC/node\" ] && printf '%s' \"$ncf\" | grep -qE '^[0-9a-f]{40}$'; then mkdir -p \"$SRC/node/.git/refs/heads\" && printf 'ref: refs/heads/build\\n' > \"$SRC/node/.git/HEAD\" && printf '%s\\n' \"$ncf\" > \"$SRC/node/.git/refs/heads/build\" && echo \"commit $ncf written to node/.git for kaspa-build-info\"; else echo \"no full node commit in the manifest: the node binaries will carry no commit string\"; fi\n");
    // the stale-build class (5 October 2026): the target dir persists and cargo rebuilds by mtime, so every extracted
    // source is stamped now, else a file older than the last build links against the cached crate of the old version
    s.push_str("find \"$B/src\" -type f -exec touch {} + 2>/dev/null || true\n");
    s.push_str("echo \"RESULT extract ok $(find \"$B/src\" -type f | wc -l) files, $(du -sh \"$B/src\" | cut -f1) at $(now)\"\n");
    s
}

fn cargo_unit_args(u: &Unit) -> String {
    let mut a = String::new();
    for p in &u.packages {
        a.push_str(&format!(" -p {p}"));
    }
    if !u.features.is_empty() {
        a.push_str(&format!(" --features {}", u.features.join(",")));
    }
    a
}

/// `linux` or `windows`: every unit of the manifest that wants the target, one cargo build each, the binaries
/// copied to $OUT/<target>/. A unit that is optional on this target reports and goes on; any other failure ends
/// the stage with its exit code.
pub fn build_script(p: &BuildParams, job_id: &str, m: &Manifest, target: &str) -> String {
    let mut s = prelude(p, job_id);
    let windows = target == "windows";
    if windows {
        s.push_str(&windows_env());
    }
    s.push_str(&format!("echo \"STAGE {target} start $(now)\"\n"));
    if m.node_commit_time > 0 {
        // reproducible builds: the commit's author time and UTC for every compiler of this stage; the target dir is the one
        // persistent $CARGO_TARGET_DIR (never a per-run name: prost's generated code embeds OUT_DIR)
        s.push_str(&format!("export SOURCE_DATE_EPOCH={} TZ=UTC; echo \"SOURCE_DATE_EPOCH=$SOURCE_DATE_EPOCH TZ=UTC\"\n", m.node_commit_time));
    }
    s.push_str(&format!("mkdir -p \"$OUT/{target}\"\n"));
    s.push_str("rc_all=0\n");
    // the node's full commit from the manifest (each stage is its own script; the extract stage read it too)
    s.push_str("ncf=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1])).get(\"node\",{}).get(\"commit_full\",\"\"))' \"$SRC/manifest.json\" 2>/dev/null || true)\n");
    for u in m.builds.iter().filter(|u| u.targets.iter().any(|t| t == target)) {
        let optional = u.optional_on.iter().any(|t| t == target);
        let rel = if windows { "x86_64-pc-windows-gnu/release" } else { "release" };
        let tflag = if windows { " --target x86_64-pc-windows-gnu" } else { "" };
        s.push_str(&format!("echo \"STAGE {target} {dir} start $(now)\"\n", dir = u.dir));
        s.push_str(&format!("t0=$(date +%s); rc=1\n"));
        if u.dir == "node" {
            // kaspa-build-info emits no rerun-if-changed once it found nothing, so the persistent target dir keeps an empty
            // hash forever unless that one crate is cleaned when the commit differs from the last one built here (6 Oct 2026)
            s.push_str(&format!("if cd \"$SRC/{dir}\"; then [ \"$(cat \"$CARGO_TARGET_DIR/.node-commit-{target}\" 2>/dev/null)\" = \"$ncf\" ] || cargo clean -q --release -p kaspa-build-info{tflag} 2>/dev/null || true; fi\n", dir = u.dir));
        }
        s.push_str(&format!("if cd \"$SRC/{dir}\"; then $NICE cargo build --release $JOBS{args}{tflag} 2>&1; rc=$?; else echo \"no $SRC/{dir}\"; rc=2; fi\n", dir = u.dir, args = cargo_unit_args(u)));
        if u.dir == "node" {
            s.push_str(&format!("[ \"$rc\" = 0 ] && printf '%s\\n' \"$ncf\" > \"$CARGO_TARGET_DIR/.node-commit-{target}\"\n"));
        }
        s.push_str(&format!("echo \"RESULT {target} {dir} build exit $rc $(( $(date +%s) - t0 )) s at $(now)\"\n", dir = u.dir));
        s.push_str("if [ \"$rc\" = 0 ]; then\n");
        for b in &u.bins {
            let name = if windows { format!("{b}.exe") } else { b.clone() };
            s.push_str(&format!("  if [ -f \"$CARGO_TARGET_DIR/{rel}/{name}\" ]; then cp -f \"$CARGO_TARGET_DIR/{rel}/{name}\" \"$OUT/{target}/{name}\"; echo \"RESULT {target} {name} $(stat -c %s \"$OUT/{target}/{name}\") bytes sha256 $(sha256sum \"$OUT/{target}/{name}\" | cut -c1-64)\"; else echo \"RESULT {target} {name} missing after the build\"; rc=3; fi\n"));
        }
        s.push_str("fi\n");
        if windows && u.bins.iter().any(|b| b == "igneumd") {
            // the three mingw runtime DLLs from THIS toolchain go next to the exes (5 October 2026, an earlier release: the PC's
            // GCC 13 exes shipped with the Mac's GCC 16 libstdc++-6.dll, which lacks five codecvt symbols; the node
            // did not start on either PC). make-payload.sh takes DLLs next to the exes first.
            s.push_str("if [ \"$rc\" = 0 ]; then\n");
            s.push_str("  gccdir=$(dirname \"$(x86_64-w64-mingw32-gcc-posix -print-file-name=libstdc++-6.dll)\")\n");
            s.push_str("  for dll in \"$gccdir/libstdc++-6.dll\" \"$gccdir/libgcc_s_seh-1.dll\" /usr/x86_64-w64-mingw32/lib/libwinpthread-1.dll; do\n");
            s.push_str(&format!("    if [ -f \"$dll\" ]; then cp -f \"$dll\" \"$OUT/{target}/\"; echo \"RESULT {target} $(basename \"$dll\") $(stat -c %s \"$dll\") bytes sha256 $(sha256sum \"$dll\" | cut -c1-64) from $(dirname \"$dll\")\"; else echo \"RESULT {target} runtime dll missing: $dll\"; rc_all=1; fi\n"));
            s.push_str("  done\n");
            s.push_str("fi\n");
        }
        if optional {
            s.push_str(&format!("[ \"$rc\" = 0 ] || echo \"RESULT {target} {dir} optional on {target}: not fatal\"\n", dir = u.dir));
        } else {
            s.push_str("[ \"$rc\" = 0 ] || rc_all=$rc\n");
        }
    }
    s.push_str(&format!("echo \"STAGE {target} done $(now) exit $rc_all\"\n"));
    s.push_str("exit $rc_all\n");
    s
}

/// `test`: cargo test --release for the manifest's test units (native). Failures are reported per unit and the
/// stage's exit is the first failure; the runner packs and uploads the binaries regardless and marks the job failed.
pub fn test_script(p: &BuildParams, job_id: &str, m: &Manifest) -> String {
    let mut s = prelude(p, job_id);
    s.push_str("echo \"STAGE test start $(now)\"\n");
    s.push_str("rc_all=0\n");
    for t in &m.tests {
        let pk: String = t.packages.iter().map(|p| format!(" -p {p}")).collect();
        s.push_str(&format!("t0=$(date +%s); rc=1\n"));
        s.push_str(&format!("if cd \"$SRC/{dir}\"; then $NICE cargo test --release $JOBS{pk} 2>&1; rc=$?; else echo \"no $SRC/{dir}\"; rc=2; fi\n", dir = t.dir));
        s.push_str(&format!("echo \"RESULT test {dir} [{names}] exit $rc $(( $(date +%s) - t0 )) s at $(now)\"\n", dir = t.dir, names = t.packages.join(" ")));
        s.push_str("[ \"$rc\" = 0 ] || [ \"$rc_all\" != 0 ] || rc_all=$rc\n");
    }
    if m.tests.is_empty() {
        s.push_str("echo \"RESULT test nothing named in the manifest\"\n");
    }
    s.push_str("echo \"STAGE test done $(now) exit $rc_all\"\n");
    s.push_str("exit $rc_all\n");
    s
}

/// `pack`: zstd per binary (linux: <name>.linux.zst, windows: <name>.exe.zst), sha256 of both forms,
/// build-outputs.json, the lot copied to the job folder on the Windows side for the upload.
pub fn pack_script(p: &BuildParams, job_id: &str, m: &Manifest, out_wsl: &str) -> String {
    let mut s = prelude(p, job_id);
    s.push_str("echo \"STAGE pack start $(now)\"\n");
    s.push_str(&format!("DEST='{}'\n", out_wsl.replace('\'', "'\\''")));
    s.push_str("PACK=$OUT/pack; rm -rf \"$PACK\"; mkdir -p \"$PACK\" \"$DEST\" || { echo \"RESULT pack cannot make $DEST\"; exit 2; }\n");
    s.push_str("n=0; first=1\n");
    s.push_str(&format!("printf '{{\"job\":\"%s\",\"packed_at\":\"%s\",\"node_branch\":\"%s\",\"node_commit\":\"%s\",\"app_version\":\"%s\",\"files\":[' \"{job_id}\" \"$(now)\" '{nb}' '{nc}' '{av}' > \"$PACK/{OUTPUTS_FILE}\"\n", nb = m.node_branch.replace('\'', ""), nc = m.node_commit.replace('\'', ""), av = m.app_version.replace('\'', "")));
    s.push_str("for target in linux windows; do\n");
    s.push_str("  [ -d \"$OUT/$target\" ] || continue\n");
    s.push_str("  for f in \"$OUT/$target\"/*; do\n");
    s.push_str("    [ -f \"$f\" ] || continue\n");
    s.push_str("    name=$(basename \"$f\"); case \"$target\" in linux) z=\"$name.linux.zst\";; *) z=\"$name.zst\";; esac\n");
    s.push_str("    zstd -q -f -T0 -12 \"$f\" -o \"$PACK/$z\" || { echo \"RESULT pack zstd failed on $name\"; exit 2; }\n");
    s.push_str("    sum=$(sha256sum \"$f\" | cut -c1-64); zsum=$(sha256sum \"$PACK/$z\" | cut -c1-64); bytes=$(stat -c %s \"$f\"); zbytes=$(stat -c %s \"$PACK/$z\")\n");
    s.push_str("    [ $first = 1 ] || printf ',' >> \"$PACK/build-outputs.json\"; first=0\n");
    s.push_str("    printf '{\"name\":\"%s\",\"target\":\"%s\",\"bytes\":%s,\"sha256\":\"%s\",\"zst\":\"%s\",\"zst_bytes\":%s,\"zst_sha256\":\"%s\"}' \"$name\" \"$target\" \"$bytes\" \"$sum\" \"$z\" \"$zbytes\" \"$zsum\" >> \"$PACK/build-outputs.json\"\n");
    s.push_str("    echo \"RESULT output $target $name $bytes bytes sha256 $sum zst $z $zbytes bytes sha256 $zsum\"\n");
    s.push_str("    n=$((n+1))\n");
    s.push_str("  done\n");
    s.push_str("done\n");
    s.push_str("printf ']}\\n' >> \"$PACK/build-outputs.json\"\n");
    s.push_str("[ $n -gt 0 ] || { echo \"RESULT pack nothing to pack\"; exit 3; }\n");
    s.push_str("rm -f \"$DEST\"/*.zst \"$DEST\"/build-outputs.json; cp -f \"$PACK\"/* \"$DEST\"/ || { echo \"RESULT pack cannot copy to $DEST\"; exit 2; }\n");
    s.push_str("rm -rf \"$OUT/linux\" \"$OUT/windows\" \"$B/inputs.zip\"\n");
    s.push_str("echo \"RESULT pack ok $n files, $(du -sh \"$PACK\" | cut -f1), target dir $(du -sh \"$CARGO_TARGET_DIR\" 2>/dev/null | cut -f1) at $(now)\"\n");
    s.push_str("echo \"STAGE pack done $(now) exit 0\"\n");
    s
}

/// The free-space probe inside the distro: GB available where the build lives (the ext4 vhdx; df reports its
/// virtual size, so the Windows drive is checked too by the runner).
pub fn free_gb_script() -> &'static str {
    "mkdir -p /opt/igneum-build; df -BG --output=avail /opt/igneum-build | tail -1 | tr -dc '0-9'"
}

/// What to run inside the distro after a cap ends wsl.exe: the Linux side outlives it.
pub fn kill_script() -> &'static str {
    "pkill -f 'cargo build' ; pkill -f 'cargo test' ; pkill -x rustc ; pkill -x cc1plus ; pkill -x zstd ; true"
}

// ---- parsers for what comes back ------------------------------------------------------------------------------------

/// The first whole number in a probe's output ("123" from "123\n", or from "  123G").
pub fn parse_free_gb(s: &str) -> Option<u64> {
    let digits: String = s.chars().skip_while(|c| !c.is_ascii_digit()).take_while(|c| c.is_ascii_digit()).collect();
    digits.parse().ok()
}

#[derive(Clone, Debug, PartialEq, Default)]
pub struct Output {
    pub name: String,
    pub target: String,
    pub bytes: u64,
    pub sha256: String,
    pub zst: String,
    pub zst_bytes: u64,
    pub zst_sha256: String,
}

/// build-outputs.json from the pack stage.
pub fn parse_outputs(text: &str) -> Result<Vec<Output>, String> {
    let v: Value = serde_json::from_str(text).map_err(|e| format!("{OUTPUTS_FILE} is not JSON: {e}"))?;
    let files = v.get("files").and_then(|f| f.as_array()).ok_or(format!("{OUTPUTS_FILE} has no files list"))?;
    let mut out = Vec::new();
    for f in files {
        let s = |k: &str| f.get(k).and_then(|x| x.as_str()).unwrap_or("").to_string();
        let n = |k: &str| f.get(k).and_then(|x| x.as_u64()).unwrap_or(0);
        let o = Output { name: s("name"), target: s("target"), bytes: n("bytes"), sha256: s("sha256"), zst: s("zst"), zst_bytes: n("zst_bytes"), zst_sha256: s("zst_sha256") };
        if o.name.is_empty() || o.zst.is_empty() || o.sha256.len() != 64 || o.zst_sha256.len() != 64 {
            return Err(format!("{OUTPUTS_FILE}: entry {:?} is incomplete", o.name));
        }
        out.push(o);
    }
    Ok(out)
}

/// A string field of a JSON reply (the relay's `token`, `put_url`, `api_version`, the Blob PUT's `url`).
pub fn json_str(text: &str, key: &str) -> Option<String> {
    let v: Value = serde_json::from_str(text.trim()).ok()?;
    v.get(key).and_then(|x| match x { Value::String(s) => Some(s.clone()), Value::Number(n) => Some(n.to_string()), _ => None })
}

/// The relay's item id from a `drop` reply.
pub fn json_u64(text: &str, key: &str) -> Option<u64> {
    let v: Value = serde_json::from_str(text.trim()).ok()?;
    v.get(key).and_then(|x| x.as_u64())
}

/// The title of a relay item that carries one output, which tools/build-job.mjs searches for.
pub fn upload_title(job_id: &str, file: &str) -> String {
    format!("build-job {job_id} {file}")
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    const MANIFEST: &str = r#"{"created_at":"2026-10-04T20:00:00Z","node":{"branch":"devnet-v4","commit":"3bfe346f","commit_time":1791300000,"dirty":false,"source":"vendor/igneum-node-v4"},"repo":{"commit":"0f44edd","branch":"build-job","dirty":true},"app_version":"0.1.4",
      "builds":[{"dir":"node","packages":["kaspad","igneum-miner"],"features":["kaspad/igneum-pow"],"bins":["igneumd","igneum-miner"],"targets":["linux","windows"]},
                {"dir":"app/igneum-app","packages":["igneum-app"],"bins":["igneum-app"],"optional_on":["linux"]}],
      "tests":[{"dir":"app/igneum-app","packages":["igneum-app"]},{"dir":"node","packages":["igneum-miner"]},{"dir":"node","packages":[]}]}"#;

    fn job(params: Value) -> Job {
        let text = json!({"jobs":[{"id":"job-200000","kind":"build","expires_at":"2026-10-06T15:00:00Z","target":{"machine_ids":["ae432dc7"],"platform":"windows","requires":["wsl"]},"params":params}]}).to_string();
        jobs::parse(&text).unwrap().jobs.remove(0)
    }

    #[test]
    fn params_defaults_and_overrides() {
        let base = json!({"zip_url":"https://dl.igneum.network/dl/t/build-inputs.zip","sha256":"a".repeat(64),"size":1});
        let p = BuildParams::from_job(&job(base.clone()));
        assert_eq!(p.targets, vec!["linux", "windows"]);
        assert_eq!(p.budget_min, 40);
        assert_eq!(p.min_free_gb, 20);
        assert!(p.tests);
        assert_eq!(p.relay_url, RELAY_URL_DEFAULT);
        assert_eq!((p.distro.as_str(), p.wsl_user.as_str(), p.nice, p.cargo_jobs), ("Ubuntu-24.04", "root", 19, 0));
        assert!(p.stage_min.is_empty());
        let mut o = base.as_object().unwrap().clone();
        o.insert("targets".into(), json!(["windows", "windows"]));
        o.insert("budget_minutes".into(), json!(120));
        o.insert("stage_minutes".into(), json!({"linux": 30, "upload": 5}));
        o.insert("min_free_gb".into(), json!(30));
        o.insert("tests".into(), json!(false));
        o.insert("relay_url".into(), json!("https://relay.example/"));
        o.insert("nice".into(), json!(5));
        o.insert("cargo_jobs".into(), json!(8));
        let p = BuildParams::from_job(&job(Value::Object(o)));
        assert_eq!(p.targets, vec!["windows"]);
        assert!(p.wants("windows") && !p.wants("linux"));
        assert_eq!(p.budget_min, 120);
        assert_eq!(p.stage_min.get("linux"), Some(&30));
        assert_eq!(p.min_free_gb, 30);
        assert!(!p.tests);
        assert_eq!(p.relay_url, "https://relay.example");
        assert_eq!((p.nice, p.cargo_jobs), (5, 8));
        // the cap: a stage's own minutes, never over what is left of the budget
        assert_eq!(stage_cap(&p, "linux", Duration::from_secs(3600)), Duration::from_secs(1800));
        assert_eq!(stage_cap(&p, "linux", Duration::from_secs(600)), Duration::from_secs(600));
        assert_eq!(stage_cap(&p, "windows", Duration::from_secs(600)), Duration::from_secs(600));
    }

    #[test]
    fn manifest_parses_and_refuses_shell_unsafe_names() {
        let m = parse_manifest(MANIFEST).unwrap();
        assert_eq!((m.node_branch.as_str(), m.node_commit.as_str(), m.app_version.as_str(), m.node_dirty), ("devnet-v4", "3bfe346f", "0.1.4", false));
        assert_eq!(m.builds.len(), 2);
        assert_eq!(m.builds[0].features, vec!["kaspad/igneum-pow"]);
        assert_eq!(m.builds[1].targets, vec!["linux", "windows"]); // default when absent
        assert_eq!(m.builds[1].optional_on, vec!["linux"]);
        assert_eq!(m.tests.len(), 2); // the empty one is dropped
        assert!(parse_manifest("nope").unwrap_err().contains("JSON"));
        assert!(parse_manifest(r#"{"builds":[]}"#).unwrap_err().contains("nothing"));
        assert!(parse_manifest(&MANIFEST.replace("\"dir\":\"node\",\"packages\":[\"kaspad\"", "\"dir\":\"../node\",\"packages\":[\"kaspad\"")).unwrap_err().contains("plain"));
        assert!(parse_manifest(&MANIFEST.replace("\"kaspad\"", "\"kaspad; rm -rf /\"")).unwrap_err().contains("plain name"));
        assert!(parse_manifest(&MANIFEST.replace("\"bins\":[\"igneum-app\"]", "\"bins\":[]")).unwrap_err().contains("bins"));
        assert!(parse_manifest(&MANIFEST.replace("\"linux\",\"windows\"", "\"linux\",\"amiga\"")).unwrap_err().contains("amiga"));
    }

    #[test]
    fn stage_scripts_carry_the_plan() {
        let p = BuildParams::from_job(&job(json!({"zip_url":"https://x/a.zip","sha256":"b".repeat(64),"cargo_jobs":6,"nice":10})));
        let m = parse_manifest(MANIFEST).unwrap();
        let id = "job-200000";
        let pre = prelude(&p, id);
        assert!(pre.contains("B=/opt/igneum-build\n") && pre.contains("export CARGO_TARGET_DIR=$B/target"));
        assert!(pre.contains("OUT=$B/out/job-200000"));
        assert!(pre.contains("NICE=\"nice -n 10\"") && pre.contains("JOBS=\"-j 6\""));
        assert!(!pre.contains('\r'));
        let setup = setup_script(&p, id);
        assert!(setup.contains("gcc-mingw-w64-x86-64") && setup.contains("protobuf-compiler") && setup.contains("rustup target add x86_64-pc-windows-gnu"));
        let only_linux = BuildParams { targets: vec!["linux".into()], ..p.clone() };
        assert!(!setup_script(&only_linux, id).contains("mingw"));
        let lin = build_script(&p, id, &m, "linux");
        assert!(lin.contains("cargo build --release $JOBS -p kaspad -p igneum-miner --features kaspad/igneum-pow 2>&1"));
        assert!(lin.contains("cd \"$SRC/app/igneum-app\""));
        // reproducible builds (6 October 2026): the commit's author time and UTC exported before any cargo build of the stage
        assert!(lin.contains("export SOURCE_DATE_EPOCH=1791300000 TZ=UTC") && lin.find("SOURCE_DATE_EPOCH=1791300000").unwrap() < lin.find("cargo build").unwrap(), "{lin}");
        assert!(lin.contains("optional on linux: not fatal"));
        assert!(!lin.contains("--target x86_64-pc-windows-gnu"));
        // the windows stage ships the runtime DLLs of its own toolchain next to the exes; the linux stage does not
        let win = build_script(&p, id, &m, "windows");
        assert!(win.contains("-print-file-name=libstdc++-6.dll") && win.contains("libgcc_s_seh-1.dll") && win.contains("libwinpthread-1.dll"), "{win}");
        assert!(!lin.contains("libstdc++-6.dll"));
        assert!(lin.contains("RESULT linux igneumd $(stat"));
        let win = build_script(&p, id, &m, "windows");
        assert!(win.contains("--target x86_64-pc-windows-gnu") && win.contains("x86_64-w64-mingw32-gcc-posix") && win.contains("link-arg=-static"));
        assert!(win.contains("x86_64-pc-windows-gnu/release/igneumd.exe") && win.contains("igneum-app.exe"));
        assert!(!win.contains("optional on windows"));
        let t = test_script(&p, id, &m);
        assert!(t.contains("cargo test --release $JOBS -p igneum-app 2>&1") && t.contains("-p igneum-miner"));
        let pk = pack_script(&p, id, &m, "/mnt/c/Work/Admin/AppData/Local/igneum/app/jobs/job-200000/out");
        assert!(pk.contains("zstd -q -f -T0 -12") && pk.contains("DEST='/mnt/c/Work/Admin/AppData/Local/igneum/app/jobs/job-200000/out'"));
        assert!(pk.contains("\"node_commit\":\"%s\"") && pk.contains("'3bfe346f'"));
        let ex = extract_script(&p, id, "/mnt/c/it's/build-inputs.zip");
        assert!(ex.contains("ZIP='/mnt/c/it'\\''s/build-inputs.zip'"));
        assert!(ex.contains("unzip -q -o"));
        // the unpacked sources are stamped after the unzip and before the manifest check (cargo's mtime freshness)
        let unzip_at = ex.find("unzip -q -o").unwrap();
        let touch_at = ex.find("find \"$B/src\" -type f -exec touch {} +").expect("the extract stamps the sources");
        let manifest_at = ex.find("manifest.json").unwrap();
        assert!(unzip_at < touch_at && touch_at < manifest_at, "{ex}");
    }

    #[test]
    fn parsers() {
        assert_eq!(parse_free_gb("123\n"), Some(123));
        assert_eq!(parse_free_gb("  57G\r\n"), Some(57));
        assert_eq!(parse_free_gb("Avail\n0\n"), Some(0));
        assert_eq!(parse_free_gb("no disk"), None);
        let text = r#"{"job":"b","packed_at":"2026-10-04T20:30:00Z","files":[{"name":"igneumd","target":"linux","bytes":46883304,"sha256":"a1b2c3d4a1b2c3d4a1b2c3d4a1b2c3d4a1b2c3d4a1b2c3d4a1b2c3d4a1b2c3d4","zst":"igneumd.linux.zst","zst_bytes":15000000,"zst_sha256":"ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"}]}"#;
        let o = parse_outputs(text).unwrap();
        assert_eq!(o.len(), 1);
        assert_eq!((o[0].name.as_str(), o[0].target.as_str(), o[0].bytes, o[0].zst.as_str(), o[0].zst_bytes), ("igneumd", "linux", 46883304, "igneumd.linux.zst", 15000000));
        assert!(parse_outputs(&text.replace("igneumd.linux.zst", "")).unwrap_err().contains("incomplete"));
        assert!(parse_outputs("{}").unwrap_err().contains("files"));
        assert_eq!(json_str(r#"{"ok":true,"token":"t.x","put_url":"https://vercel.com/api/blob/?pathname=a","api_version":"11"}"#, "put_url"), Some("https://vercel.com/api/blob/?pathname=a".into()));
        assert_eq!(json_str(r#"{"api_version":11}"#, "api_version"), Some("11".into()));
        assert_eq!(json_str("garbage", "url"), None);
        assert_eq!(json_u64(r#"{"ok":true,"id":4711,"kind":"file"}"#, "id"), Some(4711));
        assert_eq!(upload_title("the store host", "igneumd.exe.zst"), "build-job the store host igneumd.exe.zst");
    }
}
