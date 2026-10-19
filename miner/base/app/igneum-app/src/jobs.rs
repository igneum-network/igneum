//! Signed remote jobs: the Mac publishes `igneum-jobs.json` with a detached Ed25519 signature next to the update
//! manifest on the downloads host, and every Ember app polls it (src/jobrun.rs, every 10 minutes). A job
//! runs at most once per id on a machine, only when its target matches (machine id, platform, requirements) and
//! it has not expired. Same key, same canonical JSON (sorted keys, no whitespace) and the same `.sig` scheme as the
//! update manifest (src/manifest.rs). the founder's rule, 4 October 2026: one app on both PCs that the Mac can send
//! commands and files to over the line, so everything is tested and built without a person at the PC.
//!
//! This module is self-contained (serde_json and manifest.rs only), so the signer (src/bin/ota-sign.rs) includes it
//! with `#[path]` and validates what it signs with the code the app runs.
//!
//! File shape:
//! {
//!   "published_at": "2026-10-04T15:00:00Z",
//!   "jobs": [ {
//!     "id": "shard-20261004-150000", "kind": "shard-benchmark", "title": "Shard proof run on the 5090",
//!     "created_at": "2026-10-04T15:00:00Z", "expires_at": "2026-10-06T15:00:00Z",
//!     "target": { "machine_ids": ["1ccfe586"] | "all", "platform": "windows" | "mac" | "any", "requires": ["wsl-prover"] },
//!     "params": { ... per kind ... },
//!     "report": "log-upload"
//!   } ]
//! }
//!
//! Kinds and their params:
//!   run             script (the body), shell powershell|bash (default per platform), elevated, stop_miners_first,
//!                   timeout_minutes (default 60, at most 600), cards_off [keys] (the runner switches them off before
//!                   the script and restores them on any exit), cards_leave_off (with cards_off: restored as OFF, so
//!                   the card stays off and persisted after the job; 7 October 2026, the Arc on the first Windows machine)
//!   fetch           url (https), sha256, size, to (file name), dir jobs|prove|packs|updates (default jobs, which is
//!                   <app data>/app/jobs/<id>/), extract (tar -xf into the dir), fresh (empty extract_dir first), extract_dir
//!   collect         globs ["logs/app-*.log", ...] relative to the app data root (* and ? per path component), command
//!                   (its output goes into the report), label
//!   restart         what miners|node|app
//!   update-now      no params: the over-the-air check runs and a newer version installs at once
//!   shard-benchmark zip_url, sha256, size, fixtures [shard fixture, block fixtures...], cap_minutes (90), distro
//!                   (Ubuntu-24.04), wsl_user
//!   build           zip_url, sha256, size (the build-inputs zip from packaging/windows/push-build-inputs.sh), targets
//!                   ["linux", "windows"], budget_minutes (40 in total), stage_minutes {setup, fetch, linux, windows,
//!                   test, pack, upload}, min_free_gb (20), tests (true), relay_url (https, where the outputs go),
//!                   distro, wsl_user, nice (19). Mining is never stopped; the build is CPU work inside WSL (src/jobbuild.rs).
//! A job never writes outside the app data directory except through an explicit `run` script, which is the
//! operator's responsibility.
//!
//! Unknown kinds: the signer refuses them (`parse`), so a typo never ships; the app skips them (`parse_lenient`) so
//! a jobs file that carries a kind this version does not know still runs the kinds it does (an earlier release and earlier reject
//! the whole file, which is why a new kind goes to the PCs in an app update before its first job is published).

#![allow(dead_code)]

use crate::manifest;
use serde_json::{json, Value};
use std::collections::BTreeMap;
use std::path::{Path, PathBuf};

pub const JOBS_FILE: &str = "igneum-jobs.json";
/// The signed envelope (an earlier release): ONE file that carries the jobs file and its signature together, so an app never
/// pairs a file with a signature from another deployment. 5 October 2026, 13:19:41Z: the second Windows machine's an earlier release app fetched
/// `igneum-jobs.json` and then `igneum-jobs.json.sig` in two requests while a deploy was landing on the edge, got
/// a pair that did not belong together and logged "jobs file signature does not verify"; the identical bytes
/// re-signed verified four minutes later. The pair stays published for apps before an earlier release.
/// Shape: `{"file":"<the exact canonical igneum-jobs.json text>","format":"igneum-jobs-signed-1","sig":"<hex>"}`;
/// the signature is over the bytes of `file`, so the same key and the same signer sign both forms.
pub const JOBS_SIGNED_FILE: &str = "igneum-jobs.signed.json";
pub const JOBS_SIGNED_FORMAT: &str = "igneum-jobs-signed-1";
pub const KINDS: &[&str] = &["run", "fetch", "collect", "restart", "update-now", "shard-benchmark", "build", "cards"];
/// Requirements the engine knows how to probe (src/jobrun.rs). An unknown requirement is never satisfied.
pub const KNOWN_REQUIRES: &[&str] = &["wsl", "wsl-prover", "nvidia"];
/// Named folders a `fetch` may write into, all under the app data root.
pub const FETCH_DIRS: &[&str] = &["jobs", "prove", "packs", "updates"];
pub const DEFAULT_RUN_TIMEOUT_MIN: u64 = 60;
pub const MAX_RUN_TIMEOUT_MIN: u64 = 600;
pub const DEFAULT_SHARD_CAP_MIN: u64 = 90;
/// `build`: the whole job (fetch, setup, both targets, tests, pack, upload) must fit this unless budget_minutes says more.
pub const DEFAULT_BUILD_BUDGET_MIN: u64 = 40;
pub const DEFAULT_BUILD_MIN_FREE_GB: u64 = 20;
pub const BUILD_TARGETS: &[&str] = &["linux", "windows"];
pub const BUILD_STAGES: &[&str] = &["fetch", "setup", "extract", "linux", "windows", "test", "pack", "upload"];

#[derive(Clone, Debug, PartialEq, Default)]
pub struct Target {
    /// Machine ids (16 hex) or their first 8 hex; empty with `all` set means every machine.
    pub machine_ids: Vec<String>,
    pub all: bool,
    /// "windows" | "mac" | "linux" | "any"
    pub platform: String,
    pub requires: Vec<String>,
}

#[derive(Clone, Debug, PartialEq, Default)]
pub struct Job {
    pub id: String,
    pub kind: String,
    pub title: String,
    pub created_at: String,
    pub expires_at: String,
    pub expires_unix: u64,
    pub target: Target,
    pub params: Value,
    pub report: String,
}

#[derive(Clone, Debug, PartialEq, Default)]
pub struct JobsFile {
    pub published_at: String,
    pub jobs: Vec<Job>,
}

impl Job {
    /// Dashboard and report wording: the title, else the kind.
    pub fn label(&self) -> String {
        if self.title.trim().is_empty() { self.kind.clone() } else { self.title.trim().to_string() }
    }
    pub fn str_param(&self, k: &str) -> String {
        self.params.get(k).and_then(|v| v.as_str()).unwrap_or("").to_string()
    }
    pub fn bool_param(&self, k: &str) -> bool {
        self.params.get(k).and_then(|v| v.as_bool()).unwrap_or(false)
    }
    pub fn u64_param(&self, k: &str) -> Option<u64> {
        self.params.get(k).and_then(|v| v.as_u64())
    }
    pub fn list_param(&self, k: &str) -> Vec<String> {
        match self.params.get(k) {
            Some(Value::Array(a)) => a.iter().filter_map(|v| v.as_str()).map(|s| s.trim().to_string()).filter(|s| !s.is_empty()).collect(),
            Some(Value::String(s)) => s.split_whitespace().map(|s| s.to_string()).collect(),
            _ => vec![],
        }
    }
    /// `run`: the script's timeout; `shard-benchmark`: the cap; `build`: the total budget. Clamped to MAX_RUN_TIMEOUT_MIN.
    pub fn timeout_minutes(&self) -> u64 {
        let d = match self.kind.as_str() {
            "shard-benchmark" => DEFAULT_SHARD_CAP_MIN,
            "build" => DEFAULT_BUILD_BUDGET_MIN,
            _ => DEFAULT_RUN_TIMEOUT_MIN,
        };
        let v = self.u64_param("timeout_minutes").or_else(|| self.u64_param("cap_minutes")).or_else(|| self.u64_param("budget_minutes")).unwrap_or(d);
        v.clamp(1, MAX_RUN_TIMEOUT_MIN)
    }
    /// The run id under which the machine reports this job to the log intake.
    pub fn run_id(&self, machine_id: &str) -> String {
        format!("job-{}-{}", self.id, id8(machine_id))
    }
}

/// The signed envelope sits next to the jobs file: same folder, fixed name (`<folder>/igneum-jobs.signed.json`).
pub fn signed_jobs_url(jobs_url: &str) -> String {
    let u = jobs_url.trim();
    if u.is_empty() {
        return String::new();
    }
    match u.rfind('/') {
        Some(i) => format!("{}/{}", &u[..i], JOBS_SIGNED_FILE),
        None => String::new(),
    }
}

/// The envelope text for a jobs file and its detached signature: what publish-jobs.sh writes next to the pair
/// (through `igneum-ota-sign envelope-jobs`). The signature is checked here, so a pair that does not belong
/// together is never wrapped. Keys in sorted order, no whitespace, as the jobs file itself.
pub fn signed_envelope(file: &[u8], sig_hex: &str, pub_hex: &str) -> Result<String, String> {
    manifest::verify_signature(file, sig_hex.trim(), pub_hex).map_err(|_| "jobs file signature does not verify; not wrapping it".to_string())?;
    let text = std::str::from_utf8(file).map_err(|_| "jobs file is not UTF-8")?;
    Ok(format!("{{\"file\":{},\"format\":\"{}\",\"sig\":\"{}\"}}", Value::String(text.to_string()), JOBS_SIGNED_FORMAT, sig_hex.trim()))
}

/// Opens the envelope: the jobs file bytes and the signature hex, both as strings in one JSON object. Nothing is
/// verified here; `verify_and_parse_signed*` do that over the exact inner bytes.
pub fn open_envelope(bytes: &[u8]) -> Result<(Vec<u8>, String), String> {
    let text = std::str::from_utf8(bytes).map_err(|_| "signed jobs file is not UTF-8")?;
    let v: Value = serde_json::from_str(text).map_err(|e| format!("signed jobs file is not JSON: {e}"))?;
    let format = v.get("format").and_then(|x| x.as_str()).unwrap_or("");
    if format != JOBS_SIGNED_FORMAT {
        return Err(format!("signed jobs file: format '{format}' is not {JOBS_SIGNED_FORMAT}"));
    }
    let file = v.get("file").and_then(|x| x.as_str()).ok_or("signed jobs file has no \"file\" string")?;
    let sig = v.get("sig").and_then(|x| x.as_str()).ok_or("signed jobs file has no \"sig\" string")?.trim();
    if sig.len() != 128 || !sig.chars().all(|c| c.is_ascii_hexdigit()) {
        return Err("signed jobs file: sig is not 128 hex characters".into());
    }
    Ok((file.as_bytes().to_vec(), sig.to_string()))
}

/// The signer's check of an envelope: the inner file and signature verify and parse (strict).
pub fn verify_and_parse_signed(bytes: &[u8], pub_hex: &str) -> Result<JobsFile, String> {
    let (file, sig) = open_envelope(bytes)?;
    verify_and_parse(&file, &sig, pub_hex)
}

/// The runner's check of an envelope: as `verify_and_parse_lenient` over the inner pair. Also returns the inner
/// file bytes, which the runner keeps on disk as jobs.json for the dashboard.
pub fn verify_and_parse_signed_lenient(bytes: &[u8], pub_hex: &str) -> Result<(JobsFile, Vec<String>, Vec<u8>), String> {
    let (file, sig) = open_envelope(bytes)?;
    let (f, skipped) = verify_and_parse_lenient(&file, &sig, pub_hex)?;
    Ok((f, skipped, file))
}

/// The jobs file sits next to the update manifest: same folder, fixed name.
pub fn jobs_url_from_manifest(manifest_url: &str) -> String {
    let u = manifest_url.trim();
    if u.is_empty() {
        return String::new();
    }
    match u.rfind('/') {
        Some(i) => format!("{}/{}", &u[..i], JOBS_FILE),
        None => String::new(),
    }
}

pub fn id8(machine_id: &str) -> String {
    machine_id.trim().to_ascii_lowercase().chars().take(8).collect()
}

fn valid_id(s: &str) -> bool {
    !s.is_empty() && s.len() <= 64 && s.chars().all(|c| c.is_ascii_alphanumeric() || c == '-' || c == '_' || c == '.') && !s.starts_with('.')
}

/// "2026-10-04T15:00:00Z" (or with a fractional second, or "+00:00") to unix seconds; a plain number is unix already.
pub fn parse_time(s: &str) -> Option<u64> {
    let s = s.trim();
    if s.is_empty() {
        return None;
    }
    if let Ok(n) = s.parse::<u64>() {
        return Some(n);
    }
    let (date, time) = s.split_once('T')?;
    let d: Vec<i64> = date.split('-').map(|p| p.parse().ok()).collect::<Option<Vec<_>>>()?;
    if d.len() != 3 || !(1..=12).contains(&d[1]) || !(1..=31).contains(&d[2]) {
        return None;
    }
    let time = time.trim_end_matches('Z');
    let time = time.split('+').next()?;
    let time = time.split('.').next()?;
    let t: Vec<i64> = time.split(':').map(|p| p.parse().ok()).collect::<Option<Vec<_>>>()?;
    if t.len() < 2 || t.len() > 3 || t[0] > 23 || t[1] > 59 {
        return None;
    }
    let sec = if t.len() == 3 { t[2] } else { 0 };
    if sec > 60 {
        return None;
    }
    let days = days_from_civil(d[0], d[1], d[2]);
    let unix = days * 86400 + t[0] * 3600 + t[1] * 60 + sec;
    if unix < 0 { None } else { Some(unix as u64) }
}

fn days_from_civil(y: i64, m: i64, d: i64) -> i64 {
    let y = if m <= 2 { y - 1 } else { y };
    let era = if y >= 0 { y } else { y - 399 } / 400;
    let yoe = y - era * 400;
    let mp = (m + 9) % 12;
    let doy = (153 * mp + 2) / 5 + d - 1;
    let doe = yoe * 365 + yoe / 4 - yoe / 100 + doy;
    era * 146_097 + doe - 719_468
}

/// Unix seconds to "YYYY-MM-DDTHH:MM:SSZ".
pub fn format_time(unix: u64) -> String {
    let z = (unix / 86400) as i64 + 719_468;
    let era = if z >= 0 { z } else { z - 146_096 } / 146_097;
    let doe = z - era * 146_097;
    let yoe = (doe - doe / 1460 + doe / 36_524 - doe / 146_096) / 365;
    let y = yoe + era * 400;
    let doy = doe - (365 * yoe + yoe / 4 - yoe / 100);
    let mp = (5 * doy + 2) / 153;
    let d = doy - (153 * mp + 2) / 5 + 1;
    let m = if mp < 10 { mp + 3 } else { mp - 9 };
    let y = if m <= 2 { y + 1 } else { y };
    let s = unix % 86400;
    format!("{y:04}-{m:02}-{d:02}T{:02}:{:02}:{:02}Z", s / 3600, s % 3600 / 60, s % 60)
}

fn parse_target(v: Option<&Value>) -> Result<Target, String> {
    let Some(v) = v else { return Err("target is missing".into()) };
    let mut t = Target { platform: "any".into(), ..Default::default() };
    match v.get("machine_ids") {
        None => return Err("target.machine_ids is missing (a list of machine ids, or \"all\")".into()),
        Some(Value::String(s)) if s == "all" => t.all = true,
        Some(Value::Array(a)) => {
            for m in a {
                let m = m.as_str().ok_or("target.machine_ids holds a non-string")?.trim().to_ascii_lowercase();
                if (m.len() != 8 && m.len() != 16) || !m.chars().all(|c| c.is_ascii_hexdigit()) {
                    return Err(format!("target.machine_ids: '{m}' is not 8 or 16 hex characters"));
                }
                t.machine_ids.push(m);
            }
            if t.machine_ids.is_empty() {
                return Err("target.machine_ids is empty (use \"all\" for every machine)".into());
            }
        }
        Some(_) => return Err("target.machine_ids must be a list or \"all\"".into()),
    }
    if let Some(p) = v.get("platform") {
        let p = p.as_str().ok_or("target.platform is not a string")?.trim().to_ascii_lowercase();
        if !p.is_empty() {
            if !["windows", "mac", "linux", "any"].contains(&p.as_str()) {
                return Err(format!("target.platform '{p}' is unknown"));
            }
            t.platform = p;
        }
    }
    if let Some(r) = v.get("requires") {
        let a = r.as_array().ok_or("target.requires is not a list")?;
        for x in a {
            let x = x.as_str().ok_or("target.requires holds a non-string")?.trim().to_string();
            if !x.is_empty() {
                t.requires.push(x);
            }
        }
    }
    Ok(t)
}

/// Parses the jobs file (after the signature was checked). Every job is validated; one bad job rejects the file,
/// so a typo on the Mac is caught by the signer before anything is published.
pub fn parse(text: &str) -> Result<JobsFile, String> {
    parse_inner(text, false).map(|(f, _)| f)
}

/// The runner's parse: a job whose kind this version does not know is skipped (its id is returned) instead of
/// rejecting the file. Everything else is as strict as `parse`.
pub fn parse_lenient(text: &str) -> Result<(JobsFile, Vec<String>), String> {
    parse_inner(text, true)
}

fn parse_inner(text: &str, skip_unknown_kinds: bool) -> Result<(JobsFile, Vec<String>), String> {
    let v: Value = serde_json::from_str(text).map_err(|e| format!("jobs file is not JSON: {e}"))?;
    let s = |v: &Value, k: &str| v.get(k).and_then(|x| x.as_str()).unwrap_or("").trim().to_string();
    let list = v.get("jobs").and_then(|j| j.as_array()).ok_or("jobs file has no \"jobs\" list")?;
    let mut out = JobsFile { published_at: s(&v, "published_at"), jobs: Vec::new() };
    let mut skipped = Vec::new();
    let mut seen = std::collections::HashSet::new();
    for (i, j) in list.iter().enumerate() {
        let id = s(j, "id");
        if !valid_id(&id) {
            return Err(format!("job {i}: id '{id}' is not 1 to 64 of [A-Za-z0-9._-]"));
        }
        if !seen.insert(id.clone()) {
            return Err(format!("job id '{id}' appears twice"));
        }
        let kind = s(j, "kind");
        if !KINDS.contains(&kind.as_str()) {
            if skip_unknown_kinds {
                skipped.push(id);
                continue;
            }
            return Err(format!("job {id}: kind '{kind}' is unknown (known: {})", KINDS.join(", ")));
        }
        let expires_at = s(j, "expires_at");
        let expires_unix = parse_time(&expires_at).ok_or(format!("job {id}: expires_at '{expires_at}' is not a time"))?;
        let created_at = s(j, "created_at");
        if !created_at.is_empty() && parse_time(&created_at).is_none() {
            return Err(format!("job {id}: created_at '{created_at}' is not a time"));
        }
        let target = parse_target(j.get("target")).map_err(|e| format!("job {id}: {e}"))?;
        let params = match j.get("params") {
            None | Some(Value::Null) => json!({}),
            Some(p) if p.is_object() => p.clone(),
            Some(_) => return Err(format!("job {id}: params is not an object")),
        };
        let job = Job { id: id.clone(), kind, title: s(j, "title"), created_at, expires_at, expires_unix, target, params, report: { let r = s(j, "report"); if r.is_empty() { "log-upload".into() } else { r } } };
        validate_params(&job).map_err(|e| format!("job {id}: {e}"))?;
        out.jobs.push(job);
    }
    Ok((out, skipped))
}

fn https_ok(url: &str) -> bool {
    url.starts_with("https://") || url.starts_with("http://127.0.0.1:")
}

fn sha_ok(s: &str) -> bool {
    s.len() == 64 && s.chars().all(|c| c.is_ascii_hexdigit())
}

/// Per-kind checks of the params, so a job that cannot run is refused at signing time.
/// The founder's word at 20:21 UK, 8 October 2026 (fifteen Mac processes killed by a grep that evening; by construction,
/// not by rule): a run-script body that ends a process by NAME is refused before it runs; a pid is the only way. The
/// shapes: `Stop-Process -Name`, `taskkill /IM`, `Get-Process -Name … | Stop-Process`, `pkill`, `killall`, as commands
/// (the first word of a line or of a pipeline stage), never as a word inside a string. Returns the matched line.
pub fn kill_by_name_line(script: &str) -> Option<&str> {
    for raw in script.lines() {
        let line = raw.trim();
        if line.is_empty() || line.starts_with('#') {
            continue;
        }
        let lower = line.to_ascii_lowercase();
        let stage_starts = |word: &str| lower.split(['|', ';', '&']).any(|stage| { let st = stage.trim_start(); st == word || st.starts_with(&format!("{word} ")) || st.starts_with(&format!("{word}\t")) });
        let taskkill_im = lower.contains("taskkill") && lower.split_whitespace().any(|w| w == "/im");
        let stop_by_name = lower.contains("stop-process") && (lower.contains("-name") || lower.contains("get-process -name") || lower.contains("get-process "));
        if stage_starts("pkill") || stage_starts("killall") || taskkill_im || stop_by_name {
            return Some(raw.trim());
        }
    }
    None
}

pub fn validate_params(job: &Job) -> Result<(), String> {
    match job.kind.as_str() {
        "run" => {
            if job.str_param("script").trim().is_empty() {
                return Err("run: params.script is empty".into());
            }
            if let Some(line) = kill_by_name_line(&job.str_param("script")) {
                return Err(format!("run: the script ends a process by name ({line}); a pid is the only way (Stop-Process -Id, taskkill /PID, kill <pid>)"));
            }
            let sh = job.str_param("shell");
            if !sh.is_empty() && !["powershell", "bash"].contains(&sh.as_str()) {
                return Err(format!("run: shell '{sh}' is not powershell or bash"));
            }
        }
        "fetch" => {
            if !https_ok(&job.str_param("url")) {
                return Err("fetch: params.url is not https".into());
            }
            if !sha_ok(&job.str_param("sha256")) {
                return Err("fetch: params.sha256 is not 64 hex characters".into());
            }
            let dir = job.str_param("dir");
            if !dir.is_empty() && !FETCH_DIRS.contains(&dir.as_str()) {
                return Err(format!("fetch: dir '{dir}' is not one of {}", FETCH_DIRS.join(", ")));
            }
            for k in ["to", "extract_dir"] {
                let v = job.str_param(k);
                if !v.is_empty() && safe_rel_path(&v).is_none() {
                    return Err(format!("fetch: {k} '{v}' is not a plain relative path"));
                }
            }
        }
        "collect" => {
            let globs = job.list_param("globs");
            if globs.is_empty() && job.str_param("command").trim().is_empty() {
                return Err("collect: give params.globs or params.command".into());
            }
            for g in &globs {
                if safe_rel_path(g).is_none() {
                    return Err(format!("collect: glob '{g}' is not a plain relative path"));
                }
            }
        }
        "restart" => {
            let w = job.str_param("what");
            if !["miners", "node", "app"].contains(&w.as_str()) {
                return Err(format!("restart: what '{w}' is not miners, node or app"));
            }
        }
        "update-now" => {}
        "cards" => {
            // the signed `cards` kind (7 October 2026): per-card enabled and identities, applied by the app through
            // its own card path and persisted; it closes the exception of a one-off script POSTing /api/cards
            let list = job.params.get("cards").and_then(|v| v.as_array()).cloned().unwrap_or_default();
            if list.is_empty() {
                return Err("cards: params.cards is empty (a list of {key, enabled, identities})".into());
            }
            for c in &list {
                let key = c.get("key").and_then(|v| v.as_str()).unwrap_or("");
                if key.trim().is_empty() || !key.contains(':') {
                    return Err(format!("cards: key '{key}' is not a card key (vendor:device:name)"));
                }
                if c.get("enabled").map(|v| !v.is_boolean()).unwrap_or(false) {
                    return Err(format!("cards: {key}: enabled must be true or false"));
                }
                if let Some(n) = c.get("identities") {
                    if !n.as_u64().map(|n| (1..=64).contains(&n)).unwrap_or(false) {
                        return Err(format!("cards: {key}: identities must be 1 to 64"));
                    }
                }
                if let Some(n) = c.get("power_pct") {
                    if !n.as_u64().map(|n| (50..=100).contains(&n)).unwrap_or(false) {
                        return Err(format!("cards: {key}: power_pct must be 50 to 100"));
                    }
                }
            }
        }
        "shard-benchmark" => {
            let url = job.str_param("zip_url");
            if !url.is_empty() && !https_ok(&url) {
                return Err("shard-benchmark: zip_url is not https".into());
            }
            if !sha_ok(&job.str_param("sha256")) {
                return Err("shard-benchmark: params.sha256 of the prove zip is not 64 hex characters".into());
            }
            for f in job.list_param("fixtures") {
                if safe_rel_path(&f).is_none() || f.contains('/') || f.contains('\\') {
                    return Err(format!("shard-benchmark: fixture name '{f}' is not plain"));
                }
            }
        }
        "build" => {
            if !https_ok(&job.str_param("zip_url")) {
                return Err("build: params.zip_url is not https (the build-inputs zip)".into());
            }
            if !sha_ok(&job.str_param("sha256")) {
                return Err("build: params.sha256 of the build-inputs zip is not 64 hex characters".into());
            }
            for t in job.list_param("targets") {
                if !BUILD_TARGETS.contains(&t.as_str()) {
                    return Err(format!("build: target '{t}' is not one of {}", BUILD_TARGETS.join(", ")));
                }
            }
            if let Some(b) = job.params.get("budget_minutes") {
                match b.as_u64() {
                    Some(n) if (1..=MAX_RUN_TIMEOUT_MIN).contains(&n) => {}
                    _ => return Err(format!("build: budget_minutes must be 1 to {MAX_RUN_TIMEOUT_MIN}")),
                }
            }
            if let Some(m) = job.params.get("stage_minutes") {
                let o = m.as_object().ok_or("build: stage_minutes is not an object of stage: minutes")?;
                for (k, v) in o {
                    if !BUILD_STAGES.contains(&k.as_str()) {
                        return Err(format!("build: stage_minutes has unknown stage '{k}' (stages: {})", BUILD_STAGES.join(", ")));
                    }
                    if !matches!(v.as_u64(), Some(n) if n >= 1) {
                        return Err(format!("build: stage_minutes.{k} must be a whole number of minutes, at least 1"));
                    }
                }
            }
            if let Some(g) = job.params.get("min_free_gb") {
                if g.as_u64().is_none() {
                    return Err("build: min_free_gb must be a whole number of GB".into());
                }
            }
            let relay = job.str_param("relay_url");
            if !relay.is_empty() && !https_ok(&relay) {
                return Err("build: relay_url is not https".into());
            }
            if let Some(n) = job.params.get("nice") {
                if !matches!(n.as_u64(), Some(v) if v <= 19) {
                    return Err("build: nice must be 0 to 19".into());
                }
            }
        }
        _ => return Err(format!("kind '{}' is unknown", job.kind)),
    }
    Ok(())
}

/// Verifies the detached signature over the exact bytes, then parses (strict: the signer's check).
pub fn verify_and_parse(bytes: &[u8], sig_hex: &str, pub_hex: &str) -> Result<JobsFile, String> {
    manifest::verify_signature(bytes, sig_hex, pub_hex).map_err(|_| "jobs file signature does not verify".to_string())?;
    let text = std::str::from_utf8(bytes).map_err(|_| "jobs file is not UTF-8")?;
    parse(text)
}

/// The runner's variant: the same signature check, unknown kinds skipped (their ids come back).
pub fn verify_and_parse_lenient(bytes: &[u8], sig_hex: &str, pub_hex: &str) -> Result<(JobsFile, Vec<String>), String> {
    manifest::verify_signature(bytes, sig_hex, pub_hex).map_err(|_| "jobs file signature does not verify".to_string())?;
    let text = std::str::from_utf8(bytes).map_err(|_| "jobs file is not UTF-8")?;
    parse_lenient(text)
}

// ---- targeting ----------------------------------------------------------------------------------------------------

pub fn targets_machine(t: &Target, machine_id: &str) -> bool {
    if t.all {
        return true;
    }
    let full = machine_id.trim().to_ascii_lowercase();
    let short = id8(machine_id);
    t.machine_ids.iter().any(|m| *m == full || *m == short)
}

pub fn targets_platform(t: &Target, platform: &str) -> bool {
    t.platform.is_empty() || t.platform == "any" || t.platform == platform
}

pub fn expired(job: &Job, now: u64) -> bool {
    now > job.expires_unix
}

#[derive(Clone, Debug, PartialEq)]
pub enum Eligibility {
    Run,
    Expired,
    OtherMachine,
    OtherPlatform,
    /// Requirements this machine does not meet (today; checked again at the next poll until the job expires).
    Needs(Vec<String>),
}

/// Whether this machine runs the job now. `have` answers one requirement at a time (the engine probes WSL,
/// nvidia-smi and the prover toolchain); an unknown requirement is never met.
pub fn eligibility(job: &Job, machine_id: &str, platform: &str, now: u64, have: &dyn Fn(&str) -> bool) -> Eligibility {
    if expired(job, now) {
        return Eligibility::Expired;
    }
    if !targets_machine(&job.target, machine_id) {
        return Eligibility::OtherMachine;
    }
    if !targets_platform(&job.target, platform) {
        return Eligibility::OtherPlatform;
    }
    let missing: Vec<String> = job.target.requires.iter().filter(|r| !KNOWN_REQUIRES.contains(&r.as_str()) || !have(r)).cloned().collect();
    if missing.is_empty() { Eligibility::Run } else { Eligibility::Needs(missing) }
}

// ---- the once-only ledger (jobs-state.json in the app data dir) -----------------------------------------------------

#[derive(Clone, Debug, PartialEq, Default)]
pub struct Record {
    pub id: String,
    pub kind: String,
    pub title: String,
    /// running | done | failed | timeout | aborted
    pub status: String,
    pub started_at: u64,
    pub finished_at: u64,
    pub exit: i64,
    pub run_id: String,
    pub summary: String,
    pub uploaded: bool,
}

impl Record {
    pub fn to_json(&self) -> Value {
        json!({ "id": self.id, "kind": self.kind, "title": self.title, "status": self.status, "started_at": self.started_at, "finished_at": self.finished_at, "exit": self.exit, "run_id": self.run_id, "summary": self.summary, "uploaded": self.uploaded })
    }
    fn from_json(id: &str, v: &Value) -> Record {
        let s = |k: &str| v.get(k).and_then(|x| x.as_str()).unwrap_or("").to_string();
        Record {
            id: id.to_string(),
            kind: s("kind"),
            title: s("title"),
            status: s("status"),
            started_at: v.get("started_at").and_then(|x| x.as_u64()).unwrap_or(0),
            finished_at: v.get("finished_at").and_then(|x| x.as_u64()).unwrap_or(0),
            exit: v.get("exit").and_then(|x| x.as_i64()).unwrap_or(0),
            run_id: s("run_id"),
            summary: s("summary"),
            uploaded: v.get("uploaded").and_then(|x| x.as_bool()).unwrap_or(false),
        }
    }
}

/// Every job id this machine ever started, with its outcome. A job id in the ledger never runs again, whatever
/// its status: a crash mid-job counts as a run (the entry is marked aborted at the next start).
#[derive(Clone, Debug, Default)]
pub struct Ledger {
    pub records: BTreeMap<String, Record>,
}

impl Ledger {
    pub fn load(path: &Path) -> Ledger {
        let mut l = Ledger::default();
        let Ok(text) = std::fs::read_to_string(path) else { return l };
        let Ok(v) = serde_json::from_str::<Value>(&text) else { return l };
        if let Some(m) = v.get("jobs").and_then(|j| j.as_object()) {
            for (id, r) in m {
                l.records.insert(id.clone(), Record::from_json(id, r));
            }
        }
        l
    }
    pub fn to_json(&self) -> Value {
        let m: serde_json::Map<String, Value> = self.records.iter().map(|(k, r)| (k.clone(), r.to_json())).collect();
        json!({ "jobs": m })
    }
    pub fn save(&self, path: &Path) -> std::io::Result<()> {
        if let Some(d) = path.parent() {
            let _ = std::fs::create_dir_all(d);
        }
        let tmp = path.with_extension("json.tmp");
        std::fs::write(&tmp, serde_json::to_string_pretty(&self.to_json()).unwrap_or_default())?;
        std::fs::rename(&tmp, path)
    }
    pub fn seen(&self, id: &str) -> bool {
        self.records.contains_key(id)
    }
    /// Marks a job as started (status running). False when it was seen before: the caller must not run it.
    pub fn start(&mut self, job: &Job, run_id: &str, now: u64) -> bool {
        if self.seen(&job.id) {
            return false;
        }
        self.records.insert(job.id.clone(), Record { id: job.id.clone(), kind: job.kind.clone(), title: job.label(), status: "running".into(), started_at: now, run_id: run_id.to_string(), ..Default::default() });
        true
    }
    pub fn finish(&mut self, id: &str, status: &str, exit: i64, summary: &str, uploaded: bool, now: u64) {
        if let Some(r) = self.records.get_mut(id) {
            r.status = status.to_string();
            r.exit = exit;
            r.summary = summary.chars().take(400).collect();
            r.uploaded = uploaded;
            r.finished_at = now;
        }
    }
    pub fn set_uploaded(&mut self, id: &str, uploaded: bool) {
        if let Some(r) = self.records.get_mut(id) {
            r.uploaded = uploaded;
        }
    }
    /// On start: anything still "running" died with the previous engine. Returns the ids marked aborted.
    pub fn abandon_running(&mut self, now: u64) -> Vec<String> {
        let mut out = Vec::new();
        for r in self.records.values_mut() {
            if r.status == "running" {
                r.status = "aborted".into();
                r.finished_at = now;
                if r.summary.is_empty() {
                    r.summary = "the app stopped while the job ran".into();
                }
                out.push(r.id.clone());
            }
        }
        out
    }
    /// The newest `n` records, newest first.
    pub fn history(&self, n: usize) -> Vec<Record> {
        let mut v: Vec<Record> = self.records.values().cloned().collect();
        v.sort_by(|a, b| b.started_at.cmp(&a.started_at).then(b.id.cmp(&a.id)));
        v.truncate(n);
        v
    }
}

// ---- helpers the runner and the tests share -------------------------------------------------------------------------

/// The JSON summary that heads every report upload. One line, so the reader (tools/jobs.mjs) parses the first line.
pub fn summary_line(job: &Job, machine_id: &str, host: &str, status: &str, exit: i64, started: u64, finished: u64, summary: &str, results: &[String], extra: Value) -> String {
    let mut v = json!({
        "job": job.id, "kind": job.kind, "title": job.label(), "machine_id": machine_id, "machine": host, "run_id": job.run_id(machine_id),
        "status": status, "exit": exit, "started_at": format_time(started), "finished_at": if finished > 0 { format_time(finished) } else { String::new() },
        "duration_s": finished.saturating_sub(started), "summary": summary, "results": results,
    });
    if let (Some(a), Some(b)) = (v.as_object_mut(), extra.as_object()) {
        for (k, x) in b {
            a.insert(k.clone(), x.clone());
        }
    }
    format!("SUMMARY {}", v)
}

/// Lines a prover or benchmark prints for the bench log: RESULT and STAGE lines, plus BUILD FAILED.
pub fn result_lines(text: &str) -> Vec<String> {
    text.lines().map(|l| l.trim_end()).filter(|l| l.starts_with("RESULT") || l.starts_with("STAGE") || l.starts_with("BUILD FAILED")).map(|l| l.to_string()).collect()
}

/// C:\Work\Admin\AppData\Local\igneum\prove -> /mnt/c/Work/Admin/AppData/Local/igneum/prove (WSL's default mount).
pub fn to_wsl_path(win: &str) -> Option<String> {
    let w = win.trim().trim_start_matches("\\\\?\\");
    let mut chars = w.chars();
    let drive = chars.next()?;
    if !drive.is_ascii_alphabetic() || chars.next()? != ':' {
        return None;
    }
    let rest: String = chars.collect::<String>().replace('\\', "/");
    Some(format!("/mnt/{}{}", drive.to_ascii_lowercase(), if rest.starts_with('/') { rest } else { format!("/{rest}") }))
}

/// A relative path with no "..", no drive or root, and no empty components; "/" and "\" both separate.
pub fn safe_rel_path(s: &str) -> Option<PathBuf> {
    let s = s.trim();
    if s.is_empty() || s.starts_with('/') || s.starts_with('\\') || s.contains(':') || s.contains('\0') {
        return None;
    }
    let mut p = PathBuf::new();
    for c in s.split(|ch| ch == '/' || ch == '\\') {
        if c.is_empty() || c == "." || c == ".." {
            return None;
        }
        p.push(c);
    }
    Some(p)
}

/// `*` (any run) and `?` (one character) within one path component.
pub fn glob_match(pattern: &str, name: &str) -> bool {
    let p: Vec<char> = pattern.chars().collect();
    let n: Vec<char> = name.chars().collect();
    fn go(p: &[char], n: &[char]) -> bool {
        match (p.first(), n.first()) {
            (None, None) => true,
            (Some('*'), _) => go(&p[1..], n) || (!n.is_empty() && go(p, &n[1..])),
            (Some('?'), Some(_)) => go(&p[1..], &n[1..]),
            (Some(a), Some(b)) if a.eq_ignore_ascii_case(b) => go(&p[1..], &n[1..]),
            _ => false,
        }
    }
    go(&p, &n)
}

/// Files under `root` matching a relative glob (components with * and ?), as absolute paths, sorted.
pub fn glob_files(root: &Path, pattern: &str) -> Vec<PathBuf> {
    let Some(rel) = safe_rel_path(pattern) else { return vec![] };
    let comps: Vec<String> = rel.components().map(|c| c.as_os_str().to_string_lossy().into_owned()).collect();
    let mut cur = vec![root.to_path_buf()];
    for (i, c) in comps.iter().enumerate() {
        let last = i + 1 == comps.len();
        let mut next = Vec::new();
        for d in &cur {
            let Ok(rd) = std::fs::read_dir(d) else { continue };
            for e in rd.flatten() {
                let name = e.file_name().to_string_lossy().into_owned();
                if !glob_match(c, &name) {
                    continue;
                }
                let p = e.path();
                if last {
                    if p.is_file() {
                        next.push(p);
                    }
                } else if p.is_dir() {
                    next.push(p);
                }
            }
        }
        cur = next;
        if cur.is_empty() {
            break;
        }
    }
    cur.sort();
    cur
}

#[cfg(test)]
mod tests {
    use super::*;
    use ed25519_dalek::{Signer, SigningKey};

    const SAMPLE: &str = r#"{"jobs":[{"created_at":"2026-10-04T15:00:00Z","expires_at":"2026-10-06T15:00:00Z","id":"shard-20261004-150000","kind":"shard-benchmark","params":{"cap_minutes":90,"fixtures":["block-338-shard1","block-341-shards2","block-344-shards4"],"sha256":"5e7b56f5d71ae3eeb46dd1cb3b203c8be013ce27372ac5c93ea19e9e55950373","size":286438,"zip_url":"https://dl.igneum.network/dl/t/igneum-prove-wsl2.zip"},"report":"log-upload","target":{"machine_ids":["1ccfe586"],"platform":"windows","requires":["wsl-prover"]},"title":"Shard proof run on the 5090"},{"expires_at":"2026-10-05T00:00:00Z","id":"collect-1","kind":"collect","params":{"globs":["logs/app-*.log"]},"target":{"machine_ids":"all"}}],"published_at":"2026-10-04T15:00:00Z"}"#;

    fn key() -> (SigningKey, String) {
        let sk = SigningKey::from_bytes(&[3u8; 32]);
        let pk = manifest::hex_encode(sk.verifying_key().as_bytes());
        (sk, pk)
    }

    #[test]
    fn parses_jobs() {
        let f = parse(SAMPLE).unwrap();
        assert_eq!(f.published_at, "2026-10-04T15:00:00Z");
        assert_eq!(f.jobs.len(), 2);
        let j = &f.jobs[0];
        assert_eq!(j.id, "shard-20261004-150000");
        assert_eq!(j.kind, "shard-benchmark");
        assert_eq!(j.label(), "Shard proof run on the 5090");
        assert_eq!(j.target.machine_ids, vec!["1ccfe586"]);
        assert_eq!(j.target.platform, "windows");
        assert_eq!(j.target.requires, vec!["wsl-prover"]);
        assert_eq!(j.expires_unix, parse_time("2026-10-06T15:00:00Z").unwrap());
        assert_eq!(j.list_param("fixtures").len(), 3);
        assert_eq!(j.timeout_minutes(), 90);
        assert_eq!(j.run_id("1ccfe586aabbccdd"), "job-shard-20261004-150000-1ccfe586");
        let c = &f.jobs[1];
        assert!(c.target.all);
        assert_eq!(c.target.platform, "any");
        assert_eq!(c.label(), "collect");
        assert_eq!(c.report, "log-upload");
        assert_eq!(c.timeout_minutes(), DEFAULT_RUN_TIMEOUT_MIN);
    }

    #[test]
    fn bad_jobs_are_refused() {
        let base = |extra: &str| format!(r#"{{"jobs":[{{"id":"a","kind":"run","expires_at":"2026-10-06T15:00:00Z","target":{{"machine_ids":"all"}},"params":{{"script":"echo hi"}}{extra}}}]}}"#);
        assert!(parse(&base("")).is_ok());
        assert!(parse("nope").unwrap_err().contains("not JSON"));
        assert!(parse(r#"{"x":1}"#).unwrap_err().contains("jobs"));
        assert!(parse(&base("").replace("\"kind\":\"run\"", "\"kind\":\"dance\"")).unwrap_err().contains("unknown"));
        assert!(parse(&base("").replace("\"id\":\"a\"", "\"id\":\"../x\"")).unwrap_err().contains("id"));
        assert!(parse(&base("").replace("2026-10-06T15:00:00Z", "soon")).unwrap_err().contains("expires_at"));
        assert!(parse(&base("").replace("\"machine_ids\":\"all\"", "\"machine_ids\":[\"zz\"]")).unwrap_err().contains("hex"));
        assert!(parse(&base("").replace("\"machine_ids\":\"all\"", "\"machine_ids\":[]")).unwrap_err().contains("empty"));
        assert!(parse(&base("").replace("\"target\":{\"machine_ids\":\"all\"}", "\"target\":{\"machine_ids\":\"all\",\"platform\":\"amiga\"}")).unwrap_err().contains("platform"));
        assert!(parse(&base("").replace("\"script\":\"echo hi\"", "\"script\":\"\"")).unwrap_err().contains("script"));
        // a duplicate id
        let two = base("").replace("]}", ",{\"id\":\"a\",\"kind\":\"update-now\",\"expires_at\":\"2026-10-06T15:00:00Z\",\"target\":{\"machine_ids\":\"all\"}}]}");
        assert!(parse(&two).unwrap_err().contains("twice"));
        // per-kind params
        let j = |kind: &str, params: &str| parse(&format!(r#"{{"jobs":[{{"id":"a","kind":"{kind}","expires_at":"2026-10-06T15:00:00Z","target":{{"machine_ids":"all"}},"params":{params}}}]}}"#));
        assert!(j("fetch", r#"{"url":"http://x/a.zip","sha256":"aa"}"#).unwrap_err().contains("https"));
        assert!(j("fetch", r#"{"url":"https://x/a.zip","sha256":"aa"}"#).unwrap_err().contains("sha256"));
        assert!(j("fetch", &format!(r#"{{"url":"https://x/a.zip","sha256":"{}","dir":"etc"}}"#, "a".repeat(64))).unwrap_err().contains("dir"));
        assert!(j("fetch", &format!(r#"{{"url":"https://x/a.zip","sha256":"{}","to":"../a"}}"#, "a".repeat(64))).unwrap_err().contains("relative"));
        assert!(j("fetch", &format!(r#"{{"url":"https://x/a.zip","sha256":"{}","dir":"prove","extract":true}}"#, "a".repeat(64))).is_ok());
        assert!(j("collect", r#"{}"#).unwrap_err().contains("globs"));
        assert!(j("collect", r#"{"globs":["/etc/passwd"]}"#).unwrap_err().contains("relative"));
        assert!(j("collect", r#"{"command":"nvidia-smi"}"#).is_ok());
        assert!(j("restart", r#"{"what":"everything"}"#).unwrap_err().contains("restart"));
        assert!(j("restart", r#"{"what":"miners"}"#).is_ok());
        assert!(j("update-now", r#"{}"#).is_ok());
        assert!(j("cards", r#"{}"#).unwrap_err().contains("cards"));
        assert!(j("cards", r#"{"cards":[{"key":"nvidia:0:NVIDIA GeForce RTX 5090","enabled":true,"identities":8}]}"#).is_ok());
        assert!(j("cards", r#"{"cards":[{"key":"5090","enabled":true}]}"#).unwrap_err().contains("card key"));
        assert!(j("cards", r#"{"cards":[{"key":"nvidia:0:x","identities":65}]}"#).unwrap_err().contains("1 to 64"));
        assert!(j("shard-benchmark", r#"{}"#).unwrap_err().contains("sha256"));
        assert!(j("shard-benchmark", &format!(r#"{{"sha256":"{}","fixtures":["../x"]}}"#, "b".repeat(64))).unwrap_err().contains("fixture"));
        assert!(j("run", r#"{"script":"ls","shell":"zsh"}"#).unwrap_err().contains("shell"));
        // the founder's word at 20:21 UK, 8 October 2026 (by construction, not by rule): a run-script body that ends a
        // process by name is refused before it runs; a pid is the only way. Known-failed first: every shape ran.
        for body in ["Stop-Process -Name igneum-app -Force", "taskkill /IM igneum-app.exe /F", "Get-Process -Name igneumd | Stop-Process", "pkill -f igneumd", "killall igneum-worker", "echo ok\ntaskkill /f /im node.exe\necho done"] {
            let e = j("run", &format!(r#"{{"script":{}}}"#, serde_json::Value::String(body.into()))).unwrap_err();
            assert!(e.contains("ends a process by name") && e.contains("pid"), "{body}: {e}");
        }
        assert!(j("run", r#"{"script":"Stop-Process -Id 4242; taskkill /PID 4242 /F; kill -TERM $(cat run.pid)"}"#).is_ok(), "a pid is the way");
        assert_eq!(kill_by_name_line("echo one\nGet-Process -Name igneumd | Stop-Process\necho two"), Some("Get-Process -Name igneumd | Stop-Process"));
        assert_eq!(kill_by_name_line("echo pkill is a word in a string, not a command: 'the pkill rule'"), None, "the shape, not the word");
    }

    #[test]
    fn build_params_are_checked() {
        let j = |params: &str| parse(&format!(r#"{{"jobs":[{{"id":"b","kind":"build","expires_at":"2026-10-06T15:00:00Z","target":{{"machine_ids":["ae432dc7"],"platform":"windows","requires":["wsl"]}},"params":{params}}}]}}"#));
        let sha = "c".repeat(64);
        let ok = format!(r#"{{"zip_url":"https://dl.igneum.network/dl/t/build-inputs.zip","sha256":"{sha}","size":12345}}"#);
        let f = j(&ok).unwrap();
        let b = &f.jobs[0];
        assert_eq!(b.timeout_minutes(), DEFAULT_BUILD_BUDGET_MIN);
        assert!(b.list_param("targets").is_empty());
        assert!(j(r#"{}"#).unwrap_err().contains("zip_url"));
        assert!(j(r#"{"zip_url":"http://x/a.zip","sha256":"aa"}"#).unwrap_err().contains("https"));
        assert!(j(r#"{"zip_url":"https://x/a.zip","sha256":"aa"}"#).unwrap_err().contains("sha256"));
        assert!(j(&ok.replace("12345", r#"1,"targets":["linux","amiga"]"#)).unwrap_err().contains("amiga"));
        assert!(j(&ok.replace("12345", r#"1,"budget_minutes":0"#)).unwrap_err().contains("budget_minutes"));
        assert!(j(&ok.replace("12345", r#"1,"budget_minutes":601"#)).unwrap_err().contains("budget_minutes"));
        assert!(j(&ok.replace("12345", r#"1,"stage_minutes":{"lunch":5}"#)).unwrap_err().contains("lunch"));
        assert!(j(&ok.replace("12345", r#"1,"stage_minutes":{"linux":"ten"}"#)).unwrap_err().contains("stage_minutes.linux"));
        assert!(j(&ok.replace("12345", r#"1,"stage_minutes":[5]"#)).unwrap_err().contains("object"));
        assert!(j(&ok.replace("12345", r#"1,"min_free_gb":"lots""#)).unwrap_err().contains("min_free_gb"));
        assert!(j(&ok.replace("12345", r#"1,"relay_url":"ftp://relay""#)).unwrap_err().contains("relay_url"));
        assert!(j(&ok.replace("12345", r#"1,"nice":25"#)).unwrap_err().contains("nice"));
        let full = j(&ok.replace("12345", r#"1,"targets":["windows"],"budget_minutes":120,"stage_minutes":{"linux":30,"windows":40},"min_free_gb":25,"tests":false,"relay_url":"https://relay.igneum.network","nice":10"#)).unwrap();
        let b = &full.jobs[0];
        assert_eq!(b.timeout_minutes(), 120);
        assert_eq!(b.list_param("targets"), vec!["windows"]);
        assert_eq!(b.u64_param("min_free_gb"), Some(25));
        assert!(!b.params.get("tests").and_then(|v| v.as_bool()).unwrap_or(true));
    }

    #[test]
    fn unknown_kinds_are_refused_by_the_signer_and_skipped_by_the_runner() {
        let (sk, pk) = key();
        let text = SAMPLE.replace("],\"published_at\"", r#",{"id":"future-1","kind":"teleport","expires_at":"2026-10-06T15:00:00Z","target":{"machine_ids":"all"}}],"published_at""#);
        assert!(text.contains("teleport"), "the sample must carry the unknown kind");
        assert!(parse(&text).unwrap_err().contains("teleport"));
        let (f, skipped) = parse_lenient(&text).unwrap();
        assert_eq!(f.jobs.len(), 2);
        assert_eq!(skipped, vec!["future-1".to_string()]);
        // the signature still has to verify, and a bad job of a known kind still rejects the file
        let sig = manifest::hex_encode(&sk.sign(text.as_bytes()).to_bytes());
        let (f2, s2) = verify_and_parse_lenient(text.as_bytes(), &sig, &pk).unwrap();
        assert_eq!((f2.jobs.len(), s2.len()), (2, 1));
        assert!(verify_and_parse_lenient(text.replace("future-1", "future-2").as_bytes(), &sig, &pk).is_err());
        let bad = text.replace("\"kind\":\"collect\"", "\"kind\":\"restart\"");
        assert!(parse_lenient(&bad).unwrap_err().contains("restart"));
        // a duplicate id is a duplicate even when one of them is unknown
        let dup = text.replace("future-1", "collect-1");
        assert!(parse_lenient(&dup).unwrap_err().contains("twice"));
        // the strict parse is unchanged for a clean file
        assert_eq!(parse_lenient(SAMPLE).unwrap().1.len(), 0);
    }

    /// The envelope: one object, the file text and its signature together. A file with the signature of another
    /// file (the 13:19:41Z pair) is refused at wrapping time and at reading time; a tampered inner text is refused;
    /// a missing field, another format and a short sig are named.
    #[test]
    fn signed_envelope_binds_file_and_signature() {
        let (sk, pk) = key();
        let sig = manifest::hex_encode(&sk.sign(SAMPLE.as_bytes()).to_bytes());
        let env = signed_envelope(SAMPLE.as_bytes(), &sig, &pk).unwrap();
        assert!(env.starts_with("{\"file\":\"{") && env.ends_with(&format!("\",\"format\":\"{JOBS_SIGNED_FORMAT}\",\"sig\":\"{sig}\"}}")), "{env}");
        assert!(!env.contains('\n'), "one line, like the jobs file");
        // reading it back gives the exact inner bytes and the same parse as the pair
        let (file, s2) = open_envelope(env.as_bytes()).unwrap();
        assert_eq!((file.as_slice(), s2.as_str()), (SAMPLE.as_bytes(), sig.as_str()));
        let f = verify_and_parse_signed(env.as_bytes(), &pk).unwrap();
        assert_eq!(f.jobs.len(), 2);
        let (f2, skipped, inner) = verify_and_parse_signed_lenient(env.as_bytes(), &pk).unwrap();
        assert_eq!((f2.jobs.len(), skipped.len(), inner.as_slice()), (2, 0, SAMPLE.as_bytes()));
        // a stale pair cannot be wrapped: the signature of another publish over this file
        let other_file = SAMPLE.replace("collect-1", "collect-2");
        let other_sig = manifest::hex_encode(&sk.sign(other_file.as_bytes()).to_bytes());
        assert_eq!(signed_envelope(SAMPLE.as_bytes(), &other_sig, &pk).unwrap_err(), "jobs file signature does not verify; not wrapping it");
        // and a mixed envelope made by hand is refused on reading with the same words the app logs
        let mixed = format!("{{\"file\":{},\"format\":\"{JOBS_SIGNED_FORMAT}\",\"sig\":\"{other_sig}\"}}", Value::String(SAMPLE.to_string()));
        assert_eq!(verify_and_parse_signed(mixed.as_bytes(), &pk).unwrap_err(), "jobs file signature does not verify");
        // a byte changed inside the inner text after wrapping
        let tampered = env.replace("shard-20261004-150000", "shard-20261004-150001");
        assert_eq!(verify_and_parse_signed(tampered.as_bytes(), &pk).unwrap_err(), "jobs file signature does not verify");
        // another key
        let other_key = manifest::hex_encode(SigningKey::from_bytes(&[4u8; 32]).verifying_key().as_bytes());
        assert!(verify_and_parse_signed(env.as_bytes(), &other_key).is_err());
        // shape errors are named
        assert!(open_envelope(b"nope").unwrap_err().contains("not JSON"));
        assert!(open_envelope(env.replace(JOBS_SIGNED_FORMAT, "igneum-jobs-signed-9").as_bytes()).unwrap_err().contains("format"));
        assert!(open_envelope(env.replace("\"file\":", "\"body\":").as_bytes()).unwrap_err().contains("\"file\""));
        assert!(open_envelope(env.replace(&sig, "abcd").as_bytes()).unwrap_err().contains("128 hex"));
        // the plain pair still works for apps before an earlier release: the same signature verifies the inner file on its own
        assert!(verify_and_parse(SAMPLE.as_bytes(), &sig, &pk).is_ok());
        // the URL next to the jobs file
        assert_eq!(signed_jobs_url("https://dl.igneum.network/dl/tok/igneum-jobs.json"), "https://dl.igneum.network/dl/tok/igneum-jobs.signed.json");
        assert_eq!(signed_jobs_url(""), "");
    }

    #[test]
    fn signature_verifies_and_tampering_fails() {
        let (sk, pk) = key();
        let sig = manifest::hex_encode(&sk.sign(SAMPLE.as_bytes()).to_bytes());
        let f = verify_and_parse(SAMPLE.as_bytes(), &sig, &pk).unwrap();
        assert_eq!(f.jobs.len(), 2);
        // the id changed after signing: refused before parsing
        let tampered = SAMPLE.replace("collect-1", "collect-2");
        assert_eq!(verify_and_parse(tampered.as_bytes(), &sig, &pk).unwrap_err(), "jobs file signature does not verify");
        // the OTA key cannot be swapped for another
        let other = manifest::hex_encode(SigningKey::from_bytes(&[4u8; 32]).verifying_key().as_bytes());
        assert!(verify_and_parse(SAMPLE.as_bytes(), &sig, &other).is_err());
        assert!(verify_and_parse(SAMPLE.as_bytes(), "zz", &pk).is_err());
    }

    #[test]
    fn times() {
        assert_eq!(parse_time("1970-01-01T00:00:00Z"), Some(0));
        assert_eq!(parse_time("2026-10-04T11:17:47Z"), Some(1_791_112_667));
        assert_eq!(parse_time("2026-10-04T11:17:47.250Z"), Some(1_791_112_667));
        assert_eq!(parse_time("2026-10-04T11:17:47+00:00"), Some(1_791_112_667));
        assert_eq!(parse_time("2026-10-04T11:17Z"), Some(1_791_112_620));
        assert_eq!(parse_time("1791112667"), Some(1_791_112_667));
        assert_eq!(parse_time("2026-13-04T11:17:47Z"), None);
        assert_eq!(parse_time("yesterday"), None);
        assert_eq!(parse_time(""), None);
        assert_eq!(format_time(1_791_112_667), "2026-10-04T11:17:47Z");
        assert_eq!(format_time(0), "1970-01-01T00:00:00Z");
        for t in [1u64, 951_782_400, 1_709_164_800, 4_102_444_800] {
            assert_eq!(parse_time(&format_time(t)), Some(t));
        }
    }

    #[test]
    fn targeting() {
        let f = parse(SAMPLE).unwrap();
        let shard = &f.jobs[0];
        let all = &f.jobs[1];
        let now = parse_time("2026-10-04T16:00:00Z").unwrap();
        let have_all = |_: &str| true;
        let have_none = |_: &str| false;
        // the full 16-hex id or its first 8 hex both match
        assert_eq!(eligibility(shard, "1ccfe586aabbccdd", "windows", now, &have_all), Eligibility::Run);
        assert_eq!(eligibility(shard, "1CCFE586", "windows", now, &have_all), Eligibility::Run);
        assert_eq!(eligibility(shard, "ae432dc7aabbccdd", "windows", now, &have_all), Eligibility::OtherMachine);
        assert_eq!(eligibility(shard, "1ccfe586aabbccdd", "mac", now, &have_all), Eligibility::OtherPlatform);
        assert_eq!(eligibility(shard, "1ccfe586aabbccdd", "windows", now, &have_none), Eligibility::Needs(vec!["wsl-prover".into()]));
        // expiry: the second after expires_at
        assert_eq!(eligibility(shard, "1ccfe586aabbccdd", "windows", shard.expires_unix, &have_all), Eligibility::Run);
        assert_eq!(eligibility(shard, "1ccfe586aabbccdd", "windows", shard.expires_unix + 1, &have_all), Eligibility::Expired);
        // "all", any platform, no requirements
        assert_eq!(eligibility(all, "ae432dc7aabbccdd", "mac", now, &have_none), Eligibility::Run);
        // an unknown requirement is never met, even when the probe says yes
        let mut j = all.clone();
        j.target.requires = vec!["fast-link".into()];
        assert_eq!(eligibility(&j, "x", "mac", now, &have_all), Eligibility::Needs(vec!["fast-link".into()]));
        // a known one the probe meets
        j.target.requires = vec!["nvidia".into()];
        assert_eq!(eligibility(&j, "x", "mac", now, &have_all), Eligibility::Run);
    }

    #[test]
    fn once_only_ledger() {
        let f = parse(SAMPLE).unwrap();
        let dir = std::env::temp_dir().join(format!("igneum-jobs-test-{}", std::process::id()));
        let path = dir.join("jobs-state.json");
        let mut l = Ledger::load(&path);
        assert!(!l.seen("shard-20261004-150000"));
        assert!(l.start(&f.jobs[0], "job-shard-20261004-150000-1ccfe586", 100));
        // the same id never starts twice, whatever happened to the first run
        assert!(!l.start(&f.jobs[0], "x", 101));
        l.finish("shard-20261004-150000", "done", 0, "RESULT ok", true, 200);
        l.save(&path).unwrap();
        let l2 = Ledger::load(&path);
        assert!(l2.seen("shard-20261004-150000"));
        assert!(!l2.seen("collect-1"));
        let r = &l2.records["shard-20261004-150000"];
        assert_eq!((r.status.as_str(), r.exit, r.uploaded, r.started_at, r.finished_at), ("done", 0, true, 100, 200));
        assert_eq!(r.summary, "RESULT ok");
        assert_eq!(r.title, "Shard proof run on the 5090");
        // a run that died with the app is marked aborted on the next start and stays done-for-good
        let mut l3 = l2.clone();
        assert!(l3.start(&f.jobs[1], "job-collect-1-ae432dc7", 300));
        l3.save(&path).unwrap();
        let mut l4 = Ledger::load(&path);
        assert_eq!(l4.abandon_running(400), vec!["collect-1".to_string()]);
        assert_eq!(l4.records["collect-1"].status, "aborted");
        assert!(!l4.start(&f.jobs[1], "x", 500));
        let h = l4.history(10);
        assert_eq!(h.len(), 2);
        assert_eq!(h[0].id, "collect-1"); // newest first
        assert_eq!(l4.history(1).len(), 1);
        let _ = std::fs::remove_dir_all(&dir);
    }

    #[test]
    fn helpers() {
        assert_eq!(jobs_url_from_manifest("https://dl.igneum.network/dl/tok/igneum-app-latest.json"), "https://dl.igneum.network/dl/tok/igneum-jobs.json");
        assert_eq!(jobs_url_from_manifest(""), "");
        assert_eq!(id8("1ccfe586aabbccdd"), "1ccfe586");
        assert_eq!(to_wsl_path(r"C:\Work\Admin\AppData\Local\igneum\prove"), Some("/mnt/c/Work/Admin/AppData/Local/igneum/prove".into()));
        assert_eq!(to_wsl_path(r"\\?\D:\x"), Some("/mnt/d/x".into()));
        assert_eq!(to_wsl_path("/Volumes/x"), None);
        assert_eq!(safe_rel_path("logs/app-*.log").unwrap(), PathBuf::from("logs").join("app-*.log"));
        assert!(safe_rel_path("../x").is_none());
        assert!(safe_rel_path("a/../b").is_none());
        assert!(safe_rel_path("/etc").is_none());
        assert!(safe_rel_path("C:\\x").is_none());
        assert!(safe_rel_path("").is_none());
        assert!(glob_match("app-*.log", "app-20661-120000.log"));
        assert!(glob_match("*.json", "block-344-shards4-cuda-x.json"));
        assert!(!glob_match("*.json", "x.log"));
        assert!(glob_match("task-?.ps1", "task-7.ps1"));
        assert!(!glob_match("task-?.ps1", "task-77.ps1"));
        assert!(glob_match("*", ""));
        let lines = result_lines("building\nSTAGE execute 2026-10-04T15:00:00Z\nnoise\nRESULT core prove 1.4 s\nBUILD FAILED\n");
        assert_eq!(lines, vec!["STAGE execute 2026-10-04T15:00:00Z", "RESULT core prove 1.4 s", "BUILD FAILED"]);
        let f = parse(SAMPLE).unwrap();
        let s = summary_line(&f.jobs[0], "1ccfe586aabbccdd", "DESKTOP-X", "done", 0, 100, 160, "ok", &["RESULT a".into()], json!({ "uploaded_files": 3 }));
        assert!(s.starts_with("SUMMARY {"));
        let v: Value = serde_json::from_str(s.trim_start_matches("SUMMARY ")).unwrap();
        assert_eq!(v["run_id"], "job-shard-20261004-150000-1ccfe586");
        assert_eq!(v["duration_s"], 60);
        assert_eq!(v["uploaded_files"], 3);
        assert_eq!(v["results"][0], "RESULT a");
    }

    #[test]
    fn glob_files_walks_components() {
        let dir = std::env::temp_dir().join(format!("igneum-glob-test-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&dir);
        std::fs::create_dir_all(dir.join("logs")).unwrap();
        std::fs::create_dir_all(dir.join("prove").join("results")).unwrap();
        std::fs::write(dir.join("logs").join("app-1.log"), "a").unwrap();
        std::fs::write(dir.join("logs").join("node-1.log"), "n").unwrap();
        std::fs::write(dir.join("prove").join("results").join("block-344-cuda.json"), "{}").unwrap();
        let a = glob_files(&dir, "logs/app-*.log");
        assert_eq!(a.len(), 1);
        assert!(a[0].ends_with("app-1.log"));
        assert_eq!(glob_files(&dir, "logs/*.log").len(), 2);
        assert_eq!(glob_files(&dir, "prove/*/*.json").len(), 1);
        assert_eq!(glob_files(&dir, "prove/*.json").len(), 0);
        assert_eq!(glob_files(&dir, "../*").len(), 0);
        let _ = std::fs::remove_dir_all(&dir);
    }
}
