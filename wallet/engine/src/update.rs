//! Over-the-air updates (OLD-BASE.md step 3, fourth piece: the previous wallet's updater on the release host's
//! signed manifest, so the founder's one signature serves the publish and the update). The founder's rule, kept:
//! every app checks for its update and downloads it without being asked.
//!
//! The loop, on its own thread outside the engine lock (the network calls take seconds):
//!   check (25 s after start, then every ten minutes; 10 minutes after an error): GET latest.json and latest.json.sig from the
//!   release host, verify the SSH signature under the release key committed in the tree (ed25519, namespace
//!   igneum-release, SHA-512 inside the signature: the form `ssh-keygen -Y sign` writes), parse, compare versions
//!   -> the version's SHA256SUMS and its .sig the same way; this platform's package by its name in the sums
//!   -> download (ureq, with resume into <data>/updates/<name>.part, then the size and the sha256 against the sums)
//!   -> ready: the package sits in <data>/updates with "Open the download" on the card. The apply and the rollback
//!      (the previous wallet's helpers that swapped the bundle and restarted) come with the window hosts and the
//!      packaging (steps 8 and 10); this build's packages are archives a person unpacks.
//! Environment (tests): IGNEUM_WALLET_UPDATE_BASE overrides the release host's wallet directory,
//! IGNEUM_WALLET_UPDATE_CHECK_SECS the check interval, IGNEUM_WALLET_UPDATE_FIRST_SECS the first check's delay.

use std::fs;
use std::io::{Read, Write};
use std::path::{Path, PathBuf};
use std::time::{Duration, Instant};

use ed25519_dalek::{Signature, Verifier, VerifyingKey};
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256, Sha512};

/// The release keys as committed (`wallet/keys/allowed_signers`, the copy of `infra/release/keys/allowed_signers` the
/// served wallet tree carries, the rotation record's list: every key of the
/// record, each with its valid-after mark where it has one) and the keys superseded for signing
/// (`infra/release/keys/SUPERSEDED`: fingerprint and the UTC mark it is superseded from). A manifest or a sums file
/// must be signed by a listed key; a key superseded from a mark is refused over a manifest written after it; a key
/// with a valid-after mark is refused over a manifest written before it. A change of key is a change of these files
/// in the tree (R-3 of 12 October 2026: the second key's first release is the rotation's proof in the field).
const ALLOWED_SIGNERS: &str = include_str!("../../keys/allowed_signers");
const SUPERSEDED: &str = include_str!("../../keys/SUPERSEDED");
pub const NAMESPACE: &str = "igneum-release";
pub const DEFAULT_BASE: &str = "https://build.igneum.network/releases/wallet";
// ten minutes (0.0.17, 15 October 2026): the founder's rule puts every machine on a release within twenty minutes of its
// sign, and an hourly check left a Windows host on 0.0.15 eleven minutes after 0.0.16's pointer; a check is one small signed read
const CHECK_EVERY_S: u64 = 600;
const FIRST_CHECK_S: u64 = 25;
const RETRY_AFTER_ERROR_S: u64 = 600;
/// the idle moment an apply waits for: no POST from the page for this long and no site's ask pending
pub const IDLE_BEFORE_APPLY_S: u64 = 120;

#[derive(Clone, Debug, Deserialize)]
pub struct Latest {
    pub version: String,
    pub directory: String,
    pub sha256sums_sha256: String,
    #[serde(default)]
    pub notes: String,
    /// the manifest's mark the signer's validity is judged by (RELEASE.md 3.2)
    #[serde(default)]
    pub written_utc: String,
    /// the fingerprint of the key that signed it (filled by the check, not read from the manifest)
    #[serde(skip)]
    pub signer: String,
}

/// What `/api/state` carries under `update` (the page's update card reads it).
#[derive(Clone, Debug, Serialize, Default)]
pub struct UpdateState {
    /// current | available | downloading | ready | error
    pub status: String,
    /// the latest version the host names (empty until a check)
    pub version: String,
    pub directory: String,
    pub package: String,
    pub package_sha256: String,
    pub size: u64,
    pub progress: f64,
    pub error: String,
    pub notes: String,
    /// the downloaded file, once ready
    pub file: String,
    pub checked_unix: u64,
    pub auto: bool,
    /// this build's version
    pub current: String,
    /// the fingerprint of the key that signed the latest manifest (empty until a check)
    pub signer: String,
    /// the update log's last lines (the version changes this install logged)
    pub log: Vec<String>,
    /// "applying": the package is being applied and this engine is about to exit
    pub applying: bool,
}

/// The three numbers before the first '-' ("0.0.2-e810b8c9" is 0.0.2).
pub fn parse_version(s: &str) -> Option<(u64, u64, u64)> {
    let core = s.trim().split('-').next()?;
    let mut it = core.split('.');
    let a = it.next()?.parse().ok()?;
    let b = it.next()?.parse().ok()?;
    let c = it.next()?.parse().ok()?;
    Some((a, b, c))
}

pub fn newer(latest: &str, current: &str) -> bool {
    match (parse_version(latest), parse_version(current)) {
        (Some(a), Some(b)) => a > b,
        _ => false,
    }
}

/// The package name this build takes from a version's sums: the platform's word in the name.
pub fn platform_word() -> &'static str {
    match (std::env::consts::OS, std::env::consts::ARCH) {
        ("linux", "x86_64") => "linux-x86_64",
        ("windows", "x86_64") => "windows-x86_64",
        ("macos", "aarch64") => "macos-arm64",
        _ => "",
    }
}

// ---- base64 and the SSH wire form ---------------------------------------------------------------------------

fn b64_decode(s: &str) -> Option<Vec<u8>> {
    let mut out = Vec::with_capacity(s.len() * 3 / 4);
    let mut buf = 0u32;
    let mut bits = 0u32;
    for c in s.bytes() {
        let v = match c {
            b'A'..=b'Z' => c - b'A',
            b'a'..=b'z' => c - b'a' + 26,
            b'0'..=b'9' => c - b'0' + 52,
            b'+' => 62,
            b'/' => 63,
            b'=' | b'\n' | b'\r' | b' ' => continue,
            _ => return None,
        } as u32;
        buf = (buf << 6) | v;
        bits += 6;
        if bits >= 8 {
            bits -= 8;
            out.push(((buf >> bits) & 0xff) as u8);
        }
    }
    Some(out)
}

struct Reader<'a> {
    b: &'a [u8],
    at: usize,
}

impl<'a> Reader<'a> {
    fn u32(&mut self) -> Option<u32> {
        let s = self.b.get(self.at..self.at + 4)?;
        self.at += 4;
        Some(u32::from_be_bytes([s[0], s[1], s[2], s[3]]))
    }
    fn string(&mut self) -> Option<&'a [u8]> {
        let n = self.u32()? as usize;
        let s = self.b.get(self.at..self.at + n)?;
        self.at += n;
        Some(s)
    }
}

fn put_string(out: &mut Vec<u8>, s: &[u8]) {
    out.extend_from_slice(&(s.len() as u32).to_be_bytes());
    out.extend_from_slice(s);
}

fn b64_encode(bytes: &[u8]) -> String {
    const T: &[u8; 64] = b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
    let mut out = String::new();
    for chunk in bytes.chunks(3) {
        let n = (chunk[0] as u32) << 16 | (*chunk.get(1).unwrap_or(&0) as u32) << 8 | *chunk.get(2).unwrap_or(&0) as u32;
        out.push(T[(n >> 18) as usize & 63] as char);
        out.push(T[(n >> 12) as usize & 63] as char);
        if chunk.len() > 1 {
            out.push(T[(n >> 6) as usize & 63] as char);
        }
        if chunk.len() > 2 {
            out.push(T[n as usize & 63] as char);
        }
    }
    out
}

/// One key of the release list: its 32 bytes, its fingerprint as ssh-keygen prints it ("SHA256:" and the base64
/// of the blob's sha256, no padding), its valid-after mark (the list's option, "20261012T082000", empty when none)
/// and the mark it is superseded for signing from (keys/SUPERSEDED, empty when none).
#[derive(Clone, Debug)]
pub struct ReleaseKey {
    pub key: VerifyingKey,
    pub fingerprint: String,
    pub valid_after: String,
    pub superseded_from: String,
}

fn key_from_blob(blob_b64: &str) -> Result<(VerifyingKey, String), String> {
    let bytes = b64_decode(blob_b64).ok_or("the release key blob is not base64")?;
    let mut r = Reader { b: &bytes, at: 0 };
    let kind = r.string().ok_or("the release key blob has no type")?;
    if kind != b"ssh-ed25519" {
        return Err("the release key is not ssh-ed25519".into());
    }
    let key = r.string().ok_or("the release key blob has no key")?;
    let arr: [u8; 32] = key.try_into().map_err(|_| "the release key is not 32 bytes")?;
    let fp = format!("SHA256:{}", b64_encode(&Sha256::digest(&bytes)));
    Ok((VerifyingKey::from_bytes(&arr).map_err(|e| format!("the release key does not parse: {e}"))?, fp))
}

/// The release keys from the committed list: a line is "igneum-release [option,option] ssh-ed25519 <blob> ...";
/// comments and blank lines skipped; the superseded marks joined by fingerprint.
pub fn release_keys() -> Result<Vec<ReleaseKey>, String> {
    let mut out = Vec::new();
    for line in ALLOWED_SIGNERS.lines() {
        let line = line.trim();
        if line.is_empty() || line.starts_with('#') {
            continue;
        }
        let words: Vec<&str> = line.split_whitespace().collect();
        let at = match words.iter().position(|w| *w == "ssh-ed25519") {
            Some(i) => i,
            None => continue,
        };
        let blob = words.get(at + 1).ok_or("a release key line has no blob")?;
        let (key, fingerprint) = key_from_blob(blob)?;
        let mut valid_after = String::new();
        for opt in words[1..at].iter().flat_map(|w| w.split(',')) {
            if let Some(v) = opt.strip_prefix("valid-after=") {
                valid_after = v.trim_matches('"').to_string();
            }
        }
        let mut superseded_from = String::new();
        for s in SUPERSEDED.lines() {
            let s = s.trim();
            if s.is_empty() || s.starts_with('#') {
                continue;
            }
            let mut it = s.split_whitespace();
            if it.next() == Some(fingerprint.as_str()) {
                superseded_from = it.next().unwrap_or("").to_string();
            }
        }
        out.push(ReleaseKey { key, fingerprint, valid_after, superseded_from });
    }
    if out.is_empty() {
        return Err("the release key list is empty".into());
    }
    Ok(out)
}

/// The first key of the list (the founder's first release key; the tests' key).
#[cfg(test)]
pub fn release_key() -> Result<VerifyingKey, String> {
    Ok(release_keys()?.remove(0).key)
}

/// A UTC mark in the compact form the list uses: "2026-10-12T07:18:00Z" and "20261012T071800" compare alike.
fn compact_utc(s: &str) -> String {
    // digits only: "2026-10-12T07:18:00Z", "20261012T071800" and "20261012071800Z" compare alike
    s.chars().filter(|c| c.is_ascii_digit()).collect()
}

/// Verifies an SSH signature (`ssh-keygen -Y sign -n igneum-release`, the armored file) over `data` under one of
/// the release keys; `written_utc` is the manifest's mark the signer's validity is judged by (empty: no judgement).
/// Returns the signer's fingerprint. The signed blob is "SSHSIG" || string(namespace) || string(reserved) ||
/// string(hash name) || string(H(data)); H is the signature's hash algorithm (sha512 by default, sha256 allowed).
pub fn verify_sshsig_by(data: &[u8], armored: &str, written_utc: &str) -> Result<String, String> {
    let body: String = armored
        .lines()
        .filter(|l| !l.starts_with("-----"))
        .collect::<Vec<_>>()
        .join("");
    let bytes = b64_decode(&body).ok_or("the signature is not base64")?;
    let mut r = Reader { b: &bytes, at: 0 };
    let magic = bytes.get(0..6).ok_or("the signature is too short")?;
    if magic != b"SSHSIG" {
        return Err("not an SSH signature (no SSHSIG magic)".into());
    }
    r.at = 6;
    let version = r.u32().ok_or("the signature has no version")?;
    if version != 1 {
        return Err(format!("SSH signature version {version} is not 1"));
    }
    let pubkey = r.string().ok_or("the signature has no public key")?;
    let namespace = r.string().ok_or("the signature has no namespace")?;
    let reserved = r.string().ok_or("the signature has no reserved field")?;
    let hash_alg = r.string().ok_or("the signature has no hash algorithm")?;
    let sig_blob = r.string().ok_or("the signature has no signature")?;
    if namespace != NAMESPACE.as_bytes() {
        return Err(format!("the signature's namespace is {:?}, not {NAMESPACE}", String::from_utf8_lossy(namespace)));
    }
    // the key inside the signature must be the release key: a signature by any other key is refused even if valid
    let mut kr = Reader { b: pubkey, at: 0 };
    let kkind = kr.string().ok_or("the signature's key has no type")?;
    let kbytes = kr.string().ok_or("the signature's key has no bytes")?;
    let keys = release_keys()?;
    let rk = match keys.iter().find(|k| kkind == b"ssh-ed25519" && kbytes == k.key.as_bytes()) {
        Some(k) => k,
        None => return Err("the signature was not made by a release key on the signed list".into()),
    };
    let key = rk.key;
    if !written_utc.is_empty() {
        let w = compact_utc(written_utc);
        if !rk.valid_after.is_empty() && w < compact_utc(&rk.valid_after) {
            return Err(format!("the key {} is valid for signing from {} and this manifest is written {}", rk.fingerprint, rk.valid_after, written_utc));
        }
        if !rk.superseded_from.is_empty() && w > compact_utc(&rk.superseded_from) {
            return Err(format!("the key {} is superseded for signing from {} and this manifest is written {}", rk.fingerprint, rk.superseded_from, written_utc));
        }
    }
    let mut sr = Reader { b: sig_blob, at: 0 };
    let skind = sr.string().ok_or("the signature blob has no type")?;
    let sbytes = sr.string().ok_or("the signature blob has no bytes")?;
    if skind != b"ssh-ed25519" {
        return Err("the signature is not ed25519".into());
    }
    let sig = Signature::from_slice(sbytes).map_err(|e| format!("the signature does not parse: {e}"))?;
    let digest: Vec<u8> = match hash_alg {
        b"sha512" => Sha512::digest(data).to_vec(),
        b"sha256" => Sha256::digest(data).to_vec(),
        other => return Err(format!("the signature's hash {:?} is not sha512 or sha256", String::from_utf8_lossy(other))),
    };
    let mut signed = Vec::new();
    signed.extend_from_slice(b"SSHSIG");
    put_string(&mut signed, namespace);
    put_string(&mut signed, reserved);
    put_string(&mut signed, hash_alg);
    put_string(&mut signed, &digest);
    key.verify(&signed, &sig).map_err(|_| "the signature does not verify under the release key".to_string())?;
    Ok(rk.fingerprint.clone())
}

/// The one-key form (the tests and the callers that judge no mark).
pub fn verify_sshsig(data: &[u8], armored: &str) -> Result<(), String> {
    verify_sshsig_by(data, armored, "").map(|_| ())
}

pub fn sha256_hex(bytes: &[u8]) -> String {
    let d = Sha256::digest(bytes);
    d.iter().map(|b| format!("{b:02x}")).collect()
}

pub fn sha256_file(path: &Path) -> Result<String, String> {
    let mut f = fs::File::open(path).map_err(|e| e.to_string())?;
    let mut h = Sha256::new();
    let mut buf = vec![0u8; 1 << 16];
    loop {
        let n = f.read(&mut buf).map_err(|e| e.to_string())?;
        if n == 0 {
            break;
        }
        h.update(&buf[..n]);
    }
    Ok(h.finalize().iter().map(|b| format!("{b:02x}")).collect())
}

/// The package line of this platform in a SHA256SUMS text: (sha256, name).
pub fn package_in_sums(sums: &str, platform: &str) -> Option<(String, String)> {
    sums.lines().filter_map(|l| {
        let mut it = l.split_whitespace();
        let sha = it.next()?;
        let name = it.next()?;
        if sha.len() == 64 && name.contains(platform) && name.ends_with(".tar.gz") {
            Some((sha.to_string(), name.to_string()))
        } else {
            None
        }
    }).next()
}

// ---- the network side, outside the engine lock -------------------------------------------------------------

fn get_text(url: &str) -> Result<String, String> {
    let agent = ureq::AgentBuilder::new().timeout(Duration::from_secs(30)).build();
    let resp = agent.get(url).call().map_err(|e| format!("{url}: {e}"))?;
    resp.into_string().map_err(|e| format!("{url}: {e}"))
}

/// What a check found: the latest manifest (verified) and, when newer, this platform's package from the verified
/// sums.
#[derive(Clone, Debug)]
pub struct Found {
    pub latest: Latest,
    pub package: Option<(String, String, u64)>,
}

/// One check against the release host: the manifest and its signature, then the version's sums and theirs.
pub fn check(base: &str, current: &str, platform: &str) -> Result<Found, String> {
    let manifest = get_text(&format!("{base}/latest/latest.json"))?;
    let sig = get_text(&format!("{base}/latest/latest.json.sig"))?;
    let latest: Latest = serde_json::from_str(&manifest).map_err(|e| format!("latest.json: {e}"))?;
    let signer = verify_sshsig_by(manifest.as_bytes(), &sig, &latest.written_utc).map_err(|e| format!("latest.json: {e}"))?;
    // a pointer that calls its set a candidate is refused whatever its signatures (wallet-security's finding 15,
    // 12 October 2026: the 0.0.3 pointer carried a status literal naming the set unsigned while its signatures
    // were served; nothing is fetched by default but a release the pointer calls a release)
    if let Some(status) = serde_json::from_str::<serde_json::Value>(&manifest).ok().and_then(|v| v.get("status").and_then(|s| s.as_str()).map(|s| s.to_ascii_lowercase())) {
        if status.contains("candidate") || status.contains("unsigned") {
            return Err(format!("latest.json names {} a candidate ({status}); candidates are not fetched", latest.version));
        }
    }
    let latest = Latest { signer, ..latest };
    if !newer(&latest.version, current) {
        return Ok(Found { latest, package: None });
    }
    if latest.directory.contains("..") || latest.directory.starts_with('/') {
        return Err("latest.json names a directory outside the host".into());
    }
    let root = base.trim_end_matches("/wallet").trim_end_matches('/');
    let sums_url = format!("{root}/{}/SHA256SUMS", latest.directory.trim_matches('/'));
    let sums = get_text(&sums_url)?;
    if sha256_hex(sums.as_bytes()) != latest.sha256sums_sha256 {
        return Err("the version's SHA256SUMS is not the one latest.json names".into());
    }
    let sums_sig = get_text(&format!("{sums_url}.sig"))?;
    verify_sshsig_by(sums.as_bytes(), &sums_sig, &latest.written_utc).map_err(|e| format!("SHA256SUMS: {e}"))?;
    let (sha, name) = match install_package_in_sums(&sums, std::env::consts::OS).or_else(|| package_in_sums(&sums, platform)) {
        Some(p) => p,
        None => return Err(format!("version {} has no package for {}", latest.version, if platform.is_empty() { "this platform" } else { platform })),
    };
    Ok(Found { latest, package: Some((sha, name, 0)) })
}

/// The install package of this platform in a SHA256SUMS text, the one the apply step takes: the disk image on
/// macOS (Igneum-Wallet-<version>.dmg), the installer on Windows (Igneum-Wallet-Setup-<version>.exe); Linux has
/// no install package (its archive is a person's unpack).
pub fn install_package_in_sums(sums: &str, os: &str) -> Option<(String, String)> {
    let take = |name: &str| match os {
        "macos" => name.starts_with("Igneum-Wallet-") && name.ends_with(".dmg"),
        "windows" => name.starts_with("Igneum-Wallet-Setup-") && name.ends_with(".exe"),
        _ => false,
    };
    sums.lines().filter_map(|l| {
        let mut it = l.split_whitespace();
        let sha = it.next()?;
        let name = it.next()?.trim_start_matches('*');
        if sha.len() == 64 && take(name) { Some((sha.to_string(), name.to_string())) } else { None }
    }).next()
}

/// The macOS apply script: waits for the engine to exit, attaches the disk image, swaps the bundle in place (the
/// old one kept beside it as "Igneum Wallet (previous).app" until the next apply), detaches, logs the change and
/// opens the new bundle. Arguments: the dmg, the installed bundle, the engine's pid, the log, the version pair.
const APPLY_SH: &str = r#"#!/bin/sh
# Igneum Wallet: apply a verified update on macOS (written by the engine, run detached).
dmg="$1"; app="$2"; pid="$3"; log="$4"; pair="$5"; host="$6"
n=0; while kill -0 "$pid" 2>/dev/null && [ $n -lt 120 ]; do sleep 1; n=$((n+1)); done
# the window host (the engine's parent, its pid recorded by the engine) does not watch the engine: it is asked to quit
if [ -n "$host" ] && [ "$host" != 0 ] && kill -0 "$host" 2>/dev/null; then kill "$host" 2>/dev/null; n=0; while kill -0 "$host" 2>/dev/null && [ $n -lt 30 ]; do sleep 1; n=$((n+1)); done; fi
mnt="$(mktemp -d /tmp/igneum-wallet-update.XXXXXX)"
if ! hdiutil attach -nobrowse -readonly -mountpoint "$mnt" "$dmg" >/dev/null 2>&1; then echo "$(date -u +%Y-%m-%dT%H:%M:%SZ) apply failed: the disk image did not attach ($pair)" >> "$log"; exit 1; fi
new="$(ls -d "$mnt"/*.app 2>/dev/null | head -1)"
if [ -z "$new" ]; then echo "$(date -u +%Y-%m-%dT%H:%M:%SZ) apply failed: no bundle in the disk image ($pair)" >> "$log"; hdiutil detach "$mnt" >/dev/null 2>&1; exit 1; fi
dir="$(dirname "$app")"; prev="$dir/Igneum Wallet (previous).app"; stage="$dir/.Igneum Wallet.update.app"
rm -rf "$stage"
if ! ditto "$new" "$stage"; then echo "$(date -u +%Y-%m-%dT%H:%M:%SZ) apply failed: the copy did not complete ($pair)" >> "$log"; rm -rf "$stage"; hdiutil detach "$mnt" >/dev/null 2>&1; exit 1; fi
hdiutil detach "$mnt" >/dev/null 2>&1
rm -rf "$prev"; mv "$app" "$prev" && mv "$stage" "$app"
echo "$(date -u +%Y-%m-%dT%H:%M:%SZ) applied $pair" >> "$log"
open -n "$app"
"#;

/// The engine's parent (the window host that started it; 0 when unknown or when the engine runs by itself).
fn host_pid() -> u32 {
    #[cfg(unix)]
    {
        let p = std::os::unix::process::parent_id();
        if p > 1 { p } else { 0 }
    }
    #[cfg(not(unix))]
    {
        0
    }
}

/// The update log beside the downloads: one line per change ("<utc> applied 0.0.3-19e227fd -> 0.0.4-xxxxxxxx",
/// "<utc> start 0.0.4-xxxxxxxx", "<utc> apply failed: ...").
pub fn log_path(data_dir: &Path) -> PathBuf {
    data_dir.join("updates").join("updates.log")
}

pub fn log_line(data_dir: &Path, line: &str) {
    let p = log_path(data_dir);
    let _ = fs::create_dir_all(p.parent().unwrap_or(data_dir));
    let utc = utc_now();
    if let Ok(mut f) = fs::OpenOptions::new().create(true).append(true).open(&p) {
        let _ = writeln!(f, "{utc} {line}");
    }
}

/// The last lines of the update log (the Settings card's "last change" reading).
pub fn log_tail(data_dir: &Path, n: usize) -> Vec<String> {
    let text = fs::read_to_string(log_path(data_dir)).unwrap_or_default();
    let lines: Vec<&str> = text.lines().filter(|l| !l.trim().is_empty()).collect();
    lines.iter().rev().take(n).rev().map(|s| s.to_string()).collect()
}

fn utc_now() -> String {
    let s = std::time::SystemTime::now().duration_since(std::time::UNIX_EPOCH).map(|d| d.as_secs()).unwrap_or(0) as i64;
    let days = s.div_euclid(86400);
    let rem = s.rem_euclid(86400);
    // civil from days (Howard Hinnant's algorithm)
    let z = days + 719468;
    let era = z.div_euclid(146097);
    let doe = z.rem_euclid(146097);
    let yoe = (doe - doe / 1460 + doe / 36524 - doe / 146096) / 365;
    let y = yoe + era * 400;
    let doy = doe - (365 * yoe + yoe / 4 - yoe / 100);
    let mp = (5 * doy + 2) / 153;
    let d = doy - (153 * mp + 2) / 5 + 1;
    let m = if mp < 10 { mp + 3 } else { mp - 9 };
    let y = if m <= 2 { y + 1 } else { y };
    format!("{y:04}-{m:02}-{d:02}T{:02}:{:02}:{:02}Z", rem / 3600, (rem % 3600) / 60, rem % 60)
}

/// Applies a downloaded, verified package: on macOS the apply script is written beside the download and started
/// detached (it swaps the bundle once this engine has exited and opens the new one); on Windows the installer is
/// started silently with the relaunch flag (its own close-and-relaunch, Igneum-Wallet.iss /IGNOTA=1). The caller
/// exits the engine after Ok. `pair` is "<current> -> <new>" for the log.
pub fn apply(file: &Path, data_dir: &Path, pair: &str) -> Result<(), String> {
    let os = std::env::consts::OS;
    let log = log_path(data_dir);
    if os == "macos" {
        let exe = std::env::current_exe().map_err(|e| e.to_string())?;
        let app = exe.ancestors().find(|p| p.extension().map(|x| x == "app").unwrap_or(false)).ok_or("this engine does not run from an app bundle; the download is in its folder")?.to_path_buf();
        let script = data_dir.join("updates").join("apply.sh");
        fs::write(&script, APPLY_SH).map_err(|e| e.to_string())?;
        std::process::Command::new("/bin/sh")
            .arg(&script).arg(file).arg(&app).arg(std::process::id().to_string()).arg(&log).arg(pair).arg(host_pid().to_string())
            .stdin(std::process::Stdio::null()).stdout(std::process::Stdio::null()).stderr(std::process::Stdio::null())
            .spawn().map_err(|e| format!("the apply script did not start: {e}"))?;
        log_line(data_dir, &format!("applying {pair} (macOS, the bundle {})", app.display()));
        return Ok(());
    }
    if os == "windows" {
        // the running wallet's own tree ends before the installer runs (the project's fix of 16 October 2026): the window host
        // by the pid it handed this engine in IGNEUM_WALLET_HOST_PID (after three seconds for it to end itself on the
        // UPDATE line), never a process by name; the signer ends with this engine's exit; the installer is spawned
        // first so it is not part of what ends
        std::process::Command::new(file)
            .args(["/VERYSILENT", "/SUPPRESSMSGBOXES", "/NORESTART", "/CLOSEAPPLICATIONS", "/FORCECLOSEAPPLICATIONS", "/IGNOTA=1"])
            .stdin(std::process::Stdio::null()).stdout(std::process::Stdio::null()).stderr(std::process::Stdio::null())
            .spawn().map_err(|e| format!("the installer did not start: {e}"))?;
        log_line(data_dir, &format!("applying {pair} (Windows, the installer {})", file.display()));
        if let Some(host) = std::env::var("IGNEUM_WALLET_HOST_PID").ok().and_then(|v| v.trim().parse::<u32>().ok()) {
            end_host_by_pid(host, data_dir);
        }
        return Ok(());
    }
    Err("this platform's package is an archive a person unpacks; the download is in its folder".into())
}

/// The window host ended by its pid before the installer replaces the files (Windows): three seconds for the host to
/// end itself on the UPDATE line, then the pid is read back by `tasklist` and ended by `taskkill /PID` only when the
/// image at that pid is still the wallet's window (a reused pid is never ended); the outcome logged.
fn end_host_by_pid(pid: u32, data_dir: &Path) {
    for _ in 0..6 {
        std::thread::sleep(std::time::Duration::from_millis(500));
        if !host_alive(pid) {
            log_line(data_dir, &format!("the window host (pid {pid}) ended by itself before the installer"));
            return;
        }
    }
    let out = std::process::Command::new("taskkill").args(["/PID", &pid.to_string(), "/F"]).output();
    match out {
        Ok(o) if o.status.success() => log_line(data_dir, &format!("the window host (pid {pid}) ended by this engine before the installer")),
        Ok(o) => log_line(data_dir, &format!("the window host (pid {pid}) could not be ended: {}", String::from_utf8_lossy(&o.stderr).trim())),
        Err(e) => log_line(data_dir, &format!("the window host (pid {pid}) could not be ended: {e}")),
    }
}

fn host_alive(pid: u32) -> bool {
    let out = std::process::Command::new("tasklist").args(["/FI", &format!("PID eq {pid}"), "/FO", "CSV", "/NH"]).output();
    match out {
        Ok(o) => String::from_utf8_lossy(&o.stdout).contains("Igneum Wallet.exe"),
        Err(_) => false,
    }
}

/// An installer's or apply's failure in words: the codes a person meets (Setup's 7, the stop step's locked file;
/// 5, a file in use at the copy) named, any other message kept as it is.
pub fn code_words(err: &str) -> String {
    if err.contains("code 7") {
        format!("a previous Igneum Wallet is still open and holds its files; close that window and the next check applies ({err})")
    } else if err.contains("code 5") {
        format!("a file of the previous wallet stayed in use at the copy, its window could not be closed ({err})")
    } else {
        err.to_string()
    }
}

/// Downloads `name` from the version's directory into `dir`, resuming a `.part`, and checks the sha256; calls
/// `progress(bytes_so_far, total)` as it goes. Returns the file.
pub fn download(base: &str, directory: &str, name: &str, sha: &str, dir: &Path, progress: &mut dyn FnMut(u64, u64)) -> Result<PathBuf, String> {
    fs::create_dir_all(dir).map_err(|e| e.to_string())?;
    let dest = dir.join(name);
    if dest.exists() && sha256_file(&dest)? == sha {
        return Ok(dest);
    }
    let part = dir.join(format!("{name}.part"));
    let have = fs::metadata(&part).map(|m| m.len()).unwrap_or(0);
    let root = base.trim_end_matches("/wallet").trim_end_matches('/');
    let url = format!("{root}/{}/{name}", directory.trim_matches('/'));
    let agent = ureq::AgentBuilder::new().timeout(Duration::from_secs(600)).build();
    let mut req = agent.get(&url);
    if have > 0 {
        req = req.set("Range", &format!("bytes={have}-"));
    }
    let resp = req.call().map_err(|e| format!("{url}: {e}"))?;
    let status = resp.status();
    let (mut file, mut got) = if status == 206 && have > 0 {
        (fs::OpenOptions::new().append(true).open(&part).map_err(|e| e.to_string())?, have)
    } else {
        (fs::File::create(&part).map_err(|e| e.to_string())?, 0)
    };
    let total = resp
        .header("Content-Length")
        .and_then(|l| l.parse::<u64>().ok())
        .map(|l| l + if status == 206 { have } else { 0 })
        .unwrap_or(0);
    let mut reader = resp.into_reader();
    let mut buf = vec![0u8; 1 << 16];
    loop {
        let n = reader.read(&mut buf).map_err(|e| e.to_string())?;
        if n == 0 {
            break;
        }
        file.write_all(&buf[..n]).map_err(|e| e.to_string())?;
        got += n as u64;
        progress(got, total);
    }
    drop(file);
    let seen = sha256_file(&part)?;
    if seen != sha {
        let _ = fs::remove_file(&part);
        return Err(format!("the download's sha256 {} is not the sums' {}", &seen[..16], &sha[..16]));
    }
    fs::rename(&part, &dest).map_err(|e| e.to_string())?;
    Ok(dest)
}

// ---- the schedule ---------------------------------------------------------------------------------------------

pub struct Updater {
    pub state: UpdateState,
    started: Instant,
    last_check: Option<Instant>,
    last_error: Option<Instant>,
    pub check_asked: bool,
}

impl Updater {
    pub fn new(current: &str, auto: bool) -> Updater {
        Updater {
            state: UpdateState { status: "current".into(), current: current.to_string(), auto, ..Default::default() },
            started: Instant::now(),
            last_check: None,
            last_error: None,
            check_asked: false,
        }
    }

    pub fn base() -> String {
        std::env::var("IGNEUM_WALLET_UPDATE_BASE").ok().filter(|b| !b.is_empty()).unwrap_or_else(|| DEFAULT_BASE.into())
    }

    fn secs(var: &str, default: u64) -> u64 {
        std::env::var(var).ok().and_then(|v| v.parse().ok()).unwrap_or(default)
    }

    /// Whether a check is due now (the first after FIRST_CHECK_S, then every CHECK_EVERY_S; RETRY_AFTER_ERROR_S
    /// after an error; at once when the page asked).
    pub fn due(&self) -> bool {
        if self.check_asked {
            return true;
        }
        match (self.last_check, self.last_error) {
            (None, _) => self.started.elapsed() >= Duration::from_secs(Self::secs("IGNEUM_WALLET_UPDATE_FIRST_SECS", FIRST_CHECK_S)),
            (Some(_), Some(e)) if e > self.last_check.unwrap_or(e) => e.elapsed() >= Duration::from_secs(RETRY_AFTER_ERROR_S),
            (Some(c), _) => c.elapsed() >= Duration::from_secs(Self::secs("IGNEUM_WALLET_UPDATE_CHECK_SECS", CHECK_EVERY_S)),
        }
    }

    pub fn note_checked(&mut self, result: &Result<Found, String>, now_unix: u64) {
        self.check_asked = false;
        self.last_check = Some(Instant::now());
        self.state.checked_unix = now_unix;
        match result {
            Ok(f) => {
                self.last_error = None;
                self.state.error.clear();
                self.state.version = f.latest.version.clone();
                self.state.directory = f.latest.directory.clone();
                self.state.notes = f.latest.notes.clone();
                self.state.signer = f.latest.signer.clone();
                match &f.package {
                    Some((sha, name, _)) => {
                        if self.state.package != *name {
                            self.state.package = name.clone();
                            self.state.package_sha256 = sha.clone();
                            self.state.file.clear();
                            self.state.progress = 0.0;
                        }
                        if self.state.status != "ready" {
                            self.state.status = "available".into();
                        }
                    }
                    None => {
                        self.state.status = "current".into();
                        self.state.package.clear();
                        self.state.package_sha256.clear();
                        self.state.file.clear();
                    }
                }
            }
            Err(e) => {
                self.last_error = Some(Instant::now());
                self.state.status = "error".into();
                self.state.error = e.clone();
            }
        }
    }

    /// A download is wanted: an available package, automatic updates on (or the page asked), nothing downloaded yet.
    pub fn wants_download(&self) -> bool {
        self.state.status == "available" && !self.state.package.is_empty() && self.state.auto
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// wallet 0.0.2's SHA256SUMS as published and the founder's signature over it (11 October 2026): the real pair.
    const SUMS: &str = concat!("769dd98e646fc987b85992f114887ca51fd6ab92876e19f2487c50d27e9eb99e  igneum-wallet-linux-x86_64-0.0.2-e810b8c9.tar.gz\n19c25253501719837c2352b4cba5f76bf75ddc621f999e45bcf0ef9f2650f649  igneum-wallet-macos-arm64-0.0.2-e810b8c9.tar.gz\ne7cb2699f6aae4b1922c3e82678a3696ee4e4031c9d57277f8e3a28198359b6d  igneum-wallet-windows-x86_64-0.0.2-e810b8c9.tar.gz\nf8b7879aa157762be998832b515a8e6035a715910b1b26c9da1f4d7f69c88378  igneum-wallet-0.0.2-e810b8c9.manifest-igneum-build-", "7.json\nfccaac0a8ef040ea3edd8169d4163c71ffb2e2f4b8807f5b679f42792ee00ae5  igneum-wallet-0.0.2-e810b8c9.manifest-igneum-build-", "9.json\n5e585d8bba5fb12e31c365eb777b35d9d4a71588ad30624cb62257a0df976250  igneum-wallet-0.0.2-e810b8c9.manifest-mini.json\n515ce17558a02db8e708d95a221f496a4ec7ad0fd977d6570b6a599d6ec665da  MANIFEST.json\n");
    const SIG: &str = "-----BEGIN SSH SIGNATURE-----\nU1NIU0lHAAAAAQAAADMAAAALc3NoLWVkMjU1MTkAAAAgwYle3VrcbBPC7aRyruIySoVMDc\npQI6gOWSvUFYjxRMUAAAAOaWduZXVtLXJlbGVhc2UAAAAAAAAABnNoYTUxMgAAAFMAAAAL\nc3NoLWVkMjU1MTkAAABALJJ14M3y1qRmDLWzX4aGuV4IKUBBWh1i7h0zvNd4R7A+gbolUt\nHIKgQ50XPFquCFV0VRdGqsumxX3j3lKPL2Dg==\n-----END SSH SIGNATURE-----\n";

    #[test]
    fn the_release_key_and_the_founders_signature() {
        assert!(release_key().is_ok());
        assert_eq!(sha256_hex(SUMS.as_bytes()), "c3b267a8d2bd53d51be9ebf409ac6ef6e3fd360251eea0475a0375937f862aee");
        verify_sshsig(SUMS.as_bytes(), SIG).expect("the founder's signature over the published sums");
        // one changed byte in the data, or in the signature, and it does not verify
        let mut bad = SUMS.to_string();
        bad.replace_range(0..1, "8");
        assert!(verify_sshsig(bad.as_bytes(), SIG).is_err());
        let bad_sig = SIG.replace("LJJ14M3y", "LJJ14M3z");
        assert!(verify_sshsig(SUMS.as_bytes(), &bad_sig).is_err());
        // another namespace is refused before the key is looked at
        let other = SIG.replace("aWduZXVtLXJlbGVhc2U", "aWduZXVtLXJlbGVhc2V");
        assert!(verify_sshsig(SUMS.as_bytes(), &other).is_err());
    }

    #[test]
    fn versions_and_the_package_line() {
        assert!(newer("0.0.2-e810b8c9", "0.0.1-8428cffc"));
        assert!(!newer("0.0.2-e810b8c9", "0.0.2-ffffffff"));
        assert!(!newer("0.0.1", "0.0.2"));
        assert!(newer("0.1.0", "0.0.9"));
        assert!(!newer("junk", "0.0.1"));
        let (sha, name) = package_in_sums(SUMS, "linux-x86_64").unwrap();
        assert_eq!(name, "igneum-wallet-linux-x86_64-0.0.2-e810b8c9.tar.gz");
        assert!(sha.starts_with("769dd98e"));
        assert_eq!(package_in_sums(SUMS, "macos-arm64").unwrap().1, "igneum-wallet-macos-arm64-0.0.2-e810b8c9.tar.gz");
        assert!(package_in_sums(SUMS, "freebsd").is_none());
        assert!(!platform_word().is_empty() || cfg!(not(any(target_os = "linux", target_os = "windows", target_os = "macos"))));
    }

    #[test]
    fn the_schedule() {
        let mut u = Updater::new("0.0.1-8428cffc", true);
        std::env::set_var("IGNEUM_WALLET_UPDATE_FIRST_SECS", "0");
        assert!(u.due());
        let found = Found { latest: Latest { version: "0.0.2-e810b8c9".into(), directory: "wallet/0.0.2-e810b8c9".into(), sha256sums_sha256: "c3".into(), notes: String::new(), written_utc: String::new(), signer: String::new() }, package: Some(("769d".into(), "igneum-wallet-linux-x86_64-0.0.2-e810b8c9.tar.gz".into(), 0)) };
        u.note_checked(&Ok(found), 1);
        assert_eq!(u.state.status, "available");
        assert!(u.wants_download());
        assert!(!u.due());
        u.note_checked(&Err("no host".into()), 2);
        assert_eq!(u.state.status, "error");
        assert!(!u.due());
        u.check_asked = true;
        assert!(u.due());
        std::env::remove_var("IGNEUM_WALLET_UPDATE_FIRST_SECS");
    }
}
