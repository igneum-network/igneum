//! The over-the-air update manifest: what the downloads host publishes as `igneum-app-latest.json` with a detached
//! Ed25519 signature next to it (`igneum-app-latest.json.sig`, 64 bytes as 128 hex characters). The signature is over
//! the exact bytes of the manifest file; the publisher (packaging/ota/publish-release-manifest.sh) writes the file in canonical
//! form (sorted keys, no whitespace) and the app verifies the bytes before it parses them.
//!
//! This module is self-contained (serde_json, ed25519-dalek, sha2 only), so the signer binary
//! (src/bin/ota-sign.rs) includes it with `#[path]` and signs with the very code that verifies.
//!
//! Manifest shape:
//! {
//!   "version": "an earlier release", "published_at": "2026-10-04T13:00:00Z", "channel": "devnet",
//!   "platforms": { "mac": {"url","sha256","size","kind":"dmg"|"zip"}, "windows": {"url","sha256","size","kind":"inno-setup"} },
//!   "min_supported_version": "an earlier release", "notes": "one line",
//!   "consensus": { "activation_height": null|number, "deadline_note": "", "override": {...} },
//!   "tuning": { "updated": "...", "cards": { "<card model>": { "variant": "u2", "race": true, "candidates": [..] } } },
//!   "ui": { "version": "an earlier release.1", "sha256": "<64 hex>", "size": 412345, "url": "https://dl.../ui/igneum-ui-an earlier release.1.tar.gz",
//!           "min_engine": "an earlier release", "signature": "<128 hex>" }
//! }
//! `ui` (7 October 2026, docs/plans/ui-ota.md) is the interface channel: a tar.gz of app/igneum-app/ui the engine serves
//! in place of its embedded copy once the hash and the entry's own Ed25519 signature (over `ui_sign_bytes`, the same
//! release key) check; the whole manifest's signature covers it too. Removing the object is the kill switch.
//! A platform that is missing is not updated (the Windows build lands later than the Mac one).
//! `tuning` (4 October 2026, docs/design/miner-tuning.md) is the fleet's per-card kernel tuning: the engine writes it
//! to <app data>/tuning.json and every GPU worker reads it at its next prepare (IGNEUM_TUNING_FILE).

#![allow(dead_code)]

use ed25519_dalek::{Signature, Verifier, VerifyingKey};
use sha2::{Digest, Sha256};

/// The release key (docs/product-a/OLD-BASE.md step 5, the project's ruling of 11 October 2026: no second key): the raw 32
/// bytes of infra/release/keys/release-signing.pub (ssh-ed25519, the founder's, fingerprint
/// SHA256:umhm59z0pvL2OCPwi0zl1VcRCr4poHILz9zbzWEGCLk). The manifest's signature is the SSH signature the founder
/// makes with `ssh-keygen -Y sign -n igneum-release` (the SSHSIG envelope, sha512 inside), verified by
/// `verify_sshsig`; the hex form of the old updater stays for the interface entries' own signatures and the lab root.
pub const OTA_PUBLIC_KEY_HEX: &str = "c1895edd5adc6c13c2eda472aee2324a854c0dca5023a80e592bd41588f144c5";
/// The release keys' rotation list, compiled in (the project's ruling of 11:5x UK, 12 October 2026, on the security review's RED:
/// every installed Ember had the one key above hard-coded, so a set signed under another release key reached fresh
/// installs only). `keys/allowed_signers` is the four-line list of the box branch release/rotation-4 at cded47fd
/// (sha256 768270a8afdacbcd..., verified by the security review on 12 October 2026; the re-cut of a25dfcd7 with the two
/// automatic keys' comment fields reading "automatic", the principals, options and blobs unchanged: key 1 the root above; key 2
/// igneum-release-2 valid from 2026-10-12T08:20:00Z; the first automatic signer on the store host, T0hJf1nD, valid from
/// 2026-10-12T10:24:00Z; the fresh automatic signer EdoH5aY4 valid from 2026-10-12T10:43:00Z), the bytes the
/// founder signs at his return and the host serves from then; `keys/SUPERSEDED` (sha256 8a68e2ebd23d40e4..., the dated
/// marks of 12 October 2026: key 1 superseded for signing from 2026-10-12T17:30:00Z, T0hJf1nD from 2026-10-13T00:00:00Z,
/// both re-signed under igneum-release-2; served at /keys/ since 22:05 UK) its supersession marks
/// (RELEASE.md 3.2 and 3.3): a key superseded for
/// signing stays in the list so everything it signed before its mark keeps verifying, and a manifest written after
/// the mark under that key is refused. Both copies are refreshed at each cut from /keys/.
pub const ALLOWED_SIGNERS: &str = include_str!("../keys/allowed_signers");
pub const SUPERSEDED: &str = include_str!("../keys/SUPERSEDED");

/// One release signer of the list: the raw key as hex, its OpenSSH fingerprint, the minute it is valid from and
/// the minute it is superseded from (unix seconds; None = unbounded).
#[derive(Clone, Debug, PartialEq)]
pub struct Signer {
    pub hex: String,
    pub fingerprint: String,
    pub valid_after: Option<u64>,
    pub superseded_from: Option<u64>,
}

/// The signers of the compiled list, in its order; a malformed line is skipped (the list is also checked by the
/// tests, which refuse a list with fewer than three well-formed keys).
pub fn release_signers() -> Vec<Signer> {
    parse_signers(ALLOWED_SIGNERS, SUPERSEDED)
}

pub fn parse_signers(allowed: &str, superseded: &str) -> Vec<Signer> {
    let marks: Vec<(String, u64)> = superseded
        .lines()
        .map(str::trim)
        .filter(|l| !l.is_empty() && !l.starts_with('#'))
        .filter_map(|l| {
            let mut f = l.split_whitespace();
            let fp = f.next()?.to_string();
            let from = unix_from_rfc3339(f.next()?)?;
            Some((fp, from))
        })
        .collect();
    allowed
        .lines()
        .map(str::trim)
        .filter(|l| !l.is_empty() && !l.starts_with('#'))
        .filter_map(|l| {
            let fields: Vec<&str> = l.split_whitespace().collect();
            // principal [options] key-type base64 [comment]
            let i = fields.iter().position(|f| *f == "ssh-ed25519")?;
            let b64 = fields.get(i + 1)?;
            let blob = base64_decode(b64)?;
            let mut r = SshReader { b: &blob, i: 0 };
            if r.string().ok()? != b"ssh-ed25519" {
                return None;
            }
            let raw = r.string().ok()?;
            if raw.len() != 32 {
                return None;
            }
            let valid_after = fields[1..i].iter().flat_map(|o| o.split(',')).find_map(|o| o.strip_prefix("valid-after=")).and_then(|v| unix_from_ssh_time(v.trim_matches('"')));
            let fingerprint = ssh_fingerprint(&blob);
            let superseded_from = marks.iter().find(|(fp, _)| *fp == fingerprint).map(|(_, t)| *t);
            Some(Signer { hex: hex_encode(raw), fingerprint, valid_after, superseded_from })
        })
        .collect()
}

/// OpenSSH's fingerprint of a key blob: "SHA256:" and the unpadded base64 of sha256 over the blob.
pub fn ssh_fingerprint(blob: &[u8]) -> String {
    format!("SHA256:{}", base64_encode_nopad(&Sha256::digest(blob)))
}

pub fn base64_encode_nopad(b: &[u8]) -> String {
    const T: &[u8; 64] = b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
    let mut out = String::new();
    for c in b.chunks(3) {
        let n = ((c[0] as u32) << 16) | ((*c.get(1).unwrap_or(&0) as u32) << 8) | (*c.get(2).unwrap_or(&0) as u32);
        out.push(T[(n >> 18) as usize & 63] as char);
        out.push(T[(n >> 12) as usize & 63] as char);
        if c.len() > 1 {
            out.push(T[(n >> 6) as usize & 63] as char);
        }
        if c.len() > 2 {
            out.push(T[n as usize & 63] as char);
        }
    }
    out
}

/// ssh-keygen's time form in allowed_signers: YYYYMMDD, YYYYMMDDHHMM[SS], with an optional Z; read as UTC.
pub fn unix_from_ssh_time(t: &str) -> Option<u64> {
    let d: String = t.chars().filter(|c| c.is_ascii_digit()).collect();
    if d.len() < 8 {
        return None;
    }
    let p = |a: usize, z: usize| d.get(a..z).unwrap_or("00");
    let s = format!("{}-{}-{}T{}:{}:{}Z", &d[0..4], &d[4..6], &d[6..8], p(8, 10), p(10, 12), p(12, 14));
    unix_from_rfc3339(&s)
}

/// The signer of an armoured SSH signature when it is one of the release list's; None otherwise.
pub fn release_signer_of(armoured: &str) -> Option<Signer> {
    let raw = sshsig_key_raw(armoured)?;
    let hex = hex_encode(&raw);
    release_signers().into_iter().find(|s| s.hex == hex)
}

/// The raw 32 bytes of the ssh-ed25519 key inside an armoured SSHSIG envelope; None when the armour is not one.
fn sshsig_key_raw(armoured: &str) -> Option<Vec<u8>> {
    let body: String = armoured.lines().filter(|l| !l.starts_with("-----")).map(|l| l.trim()).collect();
    let blob = base64_decode(&body)?;
    let mut p = SshReader { b: &blob, i: 0 };
    if p.take(6).ok()? != b"SSHSIG" || p.u32().ok()? != 1 {
        return None;
    }
    let pubkey = p.string().ok()?;
    let mut k = SshReader { b: pubkey, i: 0 };
    if k.string().ok()? != b"ssh-ed25519" {
        return None;
    }
    Some(k.string().ok()?.to_vec())
}

/// Whether a manifest written at `written_utc` (RFC 3339) signed by `signer` falls under that key's supersession
/// mark, or before the key's valid-after minute: Err with the words when it does. The time judged is the
/// manifest's written_utc alone (the security review, 12 October 2026: published_at is the signer's own claim, which a
/// superseded key's holder could set before the mark); a manifest without written_utc is refused.
pub fn superseded_check(signer: &Signer, written_utc: &str) -> Result<(), String> {
    let Some(w) = unix_from_rfc3339(written_utc) else { return Err("the manifest carries no written_utc (RFC 3339), so its signer's marks cannot be judged; refused".into()) };
    if let Some(m) = signer.superseded_from {
        if w > m {
            return Err(format!("the manifest ({written_utc}) is signed by a key superseded from unix {m} ({})", signer.fingerprint));
        }
    }
    if let Some(v) = signer.valid_after {
        if w < v {
            return Err(format!("the manifest ({written_utc}) is signed by a key not valid until unix {v} ({})", signer.fingerprint));
        }
    }
    Ok(())
}
/// The namespace every release signature carries (the same as SHA256SUMS's).
pub const RELEASE_SIG_NAMESPACE: &str = "igneum-release";

/// How many DAA blocks before a consensus activation height the app stops waiting for a safe moment.
pub const FORK_URGENT_BLOCKS: u64 = 1_800;
/// No apply within this many seconds of an hourly program boundary.
pub const BOUNDARY_GUARD_S: i64 = 180;
/// A ready update that found no safe moment for this long is applied anyway (an unsynced node mines nothing).
pub const SAFE_MOMENT_PATIENCE_S: u64 = 6 * 3600;

#[derive(Clone, Debug, PartialEq, Default)]
pub struct PlatformEntry {
    pub url: String,
    pub sha256: String,
    pub size: u64,
    pub kind: String, // dmg | zip | inno-setup
    /// V6-06 (8 October 2026): the engine exe's own sha256 inside this installer (the manifest's windows entry (publish-release-manifest.sh)),
    /// what the Power Helper checks before it refreshes its protected copy; empty when the publish did not name it.
    pub engine_sha256: String,
}

#[derive(Clone, Debug, PartialEq, Default)]
pub struct Manifest {
    /// The fingerprint of the key whose signature verified this manifest and whether it is the release root or a key of
    /// the compiled list; set by the fetch, not parsed (the over-the-air row reads it from the log).
    pub signer: String,
    pub version: String,
    pub published_at: String,
    pub channel: String,
    pub mac: Option<PlatformEntry>,
    pub windows: Option<PlatformEntry>,
    pub min_supported_version: String,
    pub notes: String,
    pub activation_height: Option<u64>,
    /// Horizon frontier lane (6 October 2026): the publisher's word that this version installs even while the
    /// network's finality is paused (nothing else does); false unless the signed manifest says so
    pub urgent: bool,
    pub deadline_note: String,
    /// consensus.override: the exact object the engine writes to <app data>/override.json for igneumd's
    /// --override-params-file (for example {"difficulty_v2_activation_daa": N}); signed with the rest of the manifest.
    pub override_params: Option<serde_json::Value>,
    /// tuning: the per-card kernel tuning object (tools/tuning.mjs writes it, the manifest's tuning object carries
    /// it), written as is to <app data>/tuning.json for the GPU workers; signed with the rest of the manifest.
    pub tuning: Option<serde_json::Value>,
    /// ui: the interface channel (src/uiota.rs); None = no over-the-air interface is published
    pub ui: Option<UiEntry>,
    pub default_network: String,
    /// drivers: the per-vendor driver table (src/drivers.rs; branch driver-check, 7 October 2026), validated here and
    /// written as is to <app data>/drivers.json; None = no table published, the rows say nothing about drivers
    pub drivers: Option<serde_json::Value>,
}

/// One published interface bundle (the manifest's `ui` object).
#[derive(Clone, Debug, PartialEq, Default)]
pub struct UiEntry {
    pub version: String,
    pub sha256: String,
    pub size: u64,
    pub url: String,
    /// the lowest engine version that may serve this bundle; a newer bundle for an older engine is ignored
    pub min_engine: String,
    /// Ed25519 over `ui_sign_bytes(version, sha256, min_engine)` by the release key, 128 hex
    pub signature: String,
}

/// The bytes the interface entry's own signature covers: a fixed tag, then the version, the hash and the engine
/// floor, one per line. The url and the size are not covered: the hash is what the engine trusts after the download.
pub fn ui_sign_bytes(version: &str, sha256: &str, min_engine: &str) -> Vec<u8> {
    format!("igneum-ui\n{version}\n{}\n{min_engine}\n", sha256.to_ascii_lowercase()).into_bytes()
}

/// The entry's own signature against the release key (the whole manifest's signature is checked before this).
pub fn verify_ui_entry(e: &UiEntry, pub_hex: &str) -> Result<(), String> {
    let key = public_key(pub_hex)?;
    let sig = hex_decode(&e.signature).and_then(|b| Signature::from_slice(&b).ok()).ok_or("interface signature is not 128 hex characters")?;
    key.verify(&ui_sign_bytes(&e.version, &e.sha256, &e.min_engine), &sig).map_err(|_| "interface signature does not verify".to_string())
}

impl Manifest {
    pub fn platform(&self, name: &str) -> Option<&PlatformEntry> {
        match name {
            "mac" => self.mac.as_ref(),
            "windows" => self.windows.as_ref(),
            _ => None,
        }
    }
    pub fn this_platform(&self) -> Option<&PlatformEntry> {
        self.platform(platform_name())
    }
}

pub fn platform_name() -> &'static str {
    if cfg!(target_os = "macos") {
        "mac"
    } else if cfg!(windows) {
        "windows"
    } else {
        "linux"
    }
}

pub fn hex_decode(s: &str) -> Option<Vec<u8>> {
    let s = s.trim();
    if s.len() % 2 != 0 {
        return None;
    }
    (0..s.len()).step_by(2).map(|i| u8::from_str_radix(&s[i..i + 2], 16).ok()).collect()
}

pub fn hex_encode(b: &[u8]) -> String {
    b.iter().map(|x| format!("{x:02x}")).collect()
}

pub fn public_key(hex: &str) -> Result<VerifyingKey, String> {
    let bytes = hex_decode(hex).ok_or("public key is not hex")?;
    let arr: [u8; 32] = bytes.try_into().map_err(|_| "public key is not 32 bytes")?;
    VerifyingKey::from_bytes(&arr).map_err(|e| format!("public key invalid: {e}"))
}

/// SHA-256 of the raw public key bytes, as hex: what the report and the docs quote.
pub fn fingerprint(pub_hex: &str) -> String {
    match hex_decode(pub_hex) {
        Some(b) => hex_encode(&Sha256::digest(&b)),
        None => String::new(),
    }
}

/// Checks the detached signature over the manifest bytes with the given public key (hex): the armoured SSH
/// signature of the release key (`-----BEGIN SSH SIGNATURE-----`, the founder's `ssh-keygen -Y sign`), or the
/// bare 128-hex Ed25519 signature of the older form (the interface entries, the lab root).
pub fn verify_signature(manifest_bytes: &[u8], sig_text: &str, pub_hex: &str) -> Result<(), String> {
    let sig_text = sig_text.trim();
    if sig_text.starts_with("-----BEGIN SSH SIGNATURE-----") {
        return verify_sshsig(manifest_bytes, sig_text, pub_hex, RELEASE_SIG_NAMESPACE);
    }
    let key = public_key(pub_hex)?;
    let sig = hex_decode(sig_text).ok_or("signature is not hex")?;
    let sig: [u8; 64] = sig.try_into().map_err(|_| "signature is not 64 bytes")?;
    let sig = Signature::from_bytes(&sig);
    key.verify(manifest_bytes, &sig).map_err(|_| "manifest signature does not verify".to_string())
}

/// The SSH signature format (PROTOCOL.sshsig): the armour holds base64 of `"SSHSIG" || u32 version 1 || string
/// publickey || string namespace || string reserved || string hash_algorithm || string signature`, where the
/// public key is `string "ssh-ed25519" || string key32` and the signature `string "ssh-ed25519" || string sig64`;
/// the bytes signed are `"SSHSIG" || string namespace || string reserved || string hash_algorithm || string
/// H(message)` with H the named hash (sha512 by default, sha256 allowed). The key inside the envelope must be the
/// pinned one; the namespace must be the release's.
pub fn verify_sshsig(message: &[u8], armoured: &str, pub_hex: &str, namespace: &str) -> Result<(), String> {
    let body: String = armoured.lines().filter(|l| !l.starts_with("-----")).map(|l| l.trim()).collect();
    let blob = base64_decode(&body).ok_or("SSH signature: the armour is not base64")?;
    let mut p = SshReader { b: &blob, i: 0 };
    if p.take(6)? != b"SSHSIG" {
        return Err("SSH signature: not an SSHSIG blob".into());
    }
    if p.u32()? != 1 {
        return Err("SSH signature: not version 1".into());
    }
    let pubkey = p.string()?;
    let ns = p.string()?;
    let _reserved = p.string()?;
    let hash_alg = p.string()?;
    let sig_blob = p.string()?;
    if ns != namespace.as_bytes() {
        return Err(format!("SSH signature: the namespace is {:?}, not {namespace:?}", String::from_utf8_lossy(ns)));
    }
    let mut k = SshReader { b: pubkey, i: 0 };
    if k.string()? != b"ssh-ed25519" {
        return Err("SSH signature: the key inside is not ssh-ed25519".into());
    }
    let key_raw = k.string()?;
    let pinned = hex_decode(pub_hex).ok_or("public key is not hex")?;
    // the pinned key, or (when the pinned key is the release root) any key of the rotation list
    let signer_hex: String = if key_raw == pinned.as_slice() {
        pub_hex.to_string()
    } else if pub_hex == OTA_PUBLIC_KEY_HEX {
        let hex = hex_encode(key_raw);
        match release_signers().into_iter().find(|s| s.hex == hex) {
            Some(s) => s.hex,
            None => return Err("SSH signature: the key inside is not a release key".into()),
        }
    } else {
        return Err("SSH signature: the key inside is not the release key".into());
    };
    let key = public_key(&signer_hex)?;
    let mut s = SshReader { b: sig_blob, i: 0 };
    if s.string()? != b"ssh-ed25519" {
        return Err("SSH signature: the signature inside is not ssh-ed25519".into());
    }
    let sig64 = s.string()?;
    let sig: [u8; 64] = sig64.try_into().map_err(|_| "SSH signature: the signature is not 64 bytes")?;
    let digest: Vec<u8> = match hash_alg {
        b"sha512" => sha2::Sha512::digest(message).to_vec(),
        b"sha256" => Sha256::digest(message).to_vec(),
        other => return Err(format!("SSH signature: the hash is {:?}, not sha512 or sha256", String::from_utf8_lossy(other))),
    };
    let mut signed = Vec::with_capacity(6 + 4 * 4 + ns.len() + hash_alg.len() + digest.len());
    signed.extend_from_slice(b"SSHSIG");
    ssh_string(&mut signed, ns);
    ssh_string(&mut signed, _reserved);
    ssh_string(&mut signed, hash_alg);
    ssh_string(&mut signed, &digest);
    key.verify(&signed, &Signature::from_bytes(&sig)).map_err(|_| "manifest signature does not verify".to_string())
}

struct SshReader<'a> {
    b: &'a [u8],
    i: usize,
}

impl<'a> SshReader<'a> {
    fn take(&mut self, n: usize) -> Result<&'a [u8], String> {
        if self.i + n > self.b.len() {
            return Err("SSH signature: truncated".into());
        }
        let s = &self.b[self.i..self.i + n];
        self.i += n;
        Ok(s)
    }
    fn u32(&mut self) -> Result<u32, String> {
        let b = self.take(4)?;
        Ok(u32::from_be_bytes([b[0], b[1], b[2], b[3]]))
    }
    fn string(&mut self) -> Result<&'a [u8], String> {
        let n = self.u32()? as usize;
        self.take(n)
    }
}

fn ssh_string(out: &mut Vec<u8>, s: &[u8]) {
    out.extend_from_slice(&(s.len() as u32).to_be_bytes());
    out.extend_from_slice(s);
}

/// Standard base64 (the armour's), no line breaks inside, padding optional.
pub fn base64_decode(s: &str) -> Option<Vec<u8>> {
    let mut out = Vec::with_capacity(s.len() * 3 / 4);
    let mut acc: u32 = 0;
    let mut bits = 0;
    for c in s.bytes() {
        let v = match c {
            b'A'..=b'Z' => c - b'A',
            b'a'..=b'z' => c - b'a' + 26,
            b'0'..=b'9' => c - b'0' + 52,
            b'+' => 62,
            b'/' => 63,
            b'=' => break,
            b'\n' | b'\r' | b' ' => continue,
            _ => return None,
        };
        acc = (acc << 6) | v as u32;
        bits += 6;
        if bits >= 8 {
            bits -= 8;
            out.push(((acc >> bits) & 0xFF) as u8);
        }
    }
    Some(out)
}

/// Parses the manifest JSON (after the signature was checked).
pub fn parse(text: &str) -> Result<Manifest, String> {
    let v: serde_json::Value = serde_json::from_str(text).map_err(|e| format!("manifest is not JSON: {e}"))?;
    let s = |v: &serde_json::Value, k: &str| v.get(k).and_then(|x| x.as_str()).unwrap_or("").to_string();
    let version = s(&v, "version");
    if parse_version(&version).is_none() {
        return Err(format!("manifest version '{version}' is not a version"));
    }
    let entry = |name: &str| -> Result<Option<PlatformEntry>, String> {
        let Some(p) = v.get("platforms").and_then(|p| p.get(name)) else { return Ok(None) };
        if p.is_null() {
            return Ok(None);
        }
        let e = PlatformEntry { url: s(p, "url"), sha256: s(p, "sha256").to_ascii_lowercase(), size: p.get("size").and_then(|x| x.as_u64()).unwrap_or(0), kind: s(p, "kind"), engine_sha256: s(p, "engine_sha256").to_ascii_lowercase() };
        if !e.url.starts_with("https://") && !e.url.starts_with("http://127.0.0.1:") {
            return Err(format!("{name}: the url is not https"));
        }
        if e.sha256.len() != 64 || !e.sha256.chars().all(|c| c.is_ascii_hexdigit()) {
            return Err(format!("{name}: sha256 is not 64 hex characters"));
        }
        if e.size == 0 {
            return Err(format!("{name}: size is missing"));
        }
        if !["dmg", "zip", "inno-setup"].contains(&e.kind.as_str()) {
            return Err(format!("{name}: kind '{}' is unknown", e.kind));
        }
        Ok(Some(e))
    };
    let consensus = v.get("consensus").cloned().unwrap_or(serde_json::Value::Null);
    let default_network = s(&v, "default_network");
    Ok(Manifest {
        signer: String::new(),
        default_network,
        version,
        published_at: s(&v, "published_at"),
        channel: s(&v, "channel"),
        mac: entry("mac")?,
        windows: entry("windows")?,
        min_supported_version: s(&v, "min_supported_version"),
        notes: s(&v, "notes"),
        activation_height: consensus.get("activation_height").and_then(|x| x.as_u64()),
        // the manifest's TOP-LEVEL "urgent": true (the publisher sets it; igneum-ota-sign passes the document through)
        urgent: v.get("urgent").and_then(|u| u.as_bool()).unwrap_or(false),
        deadline_note: s(&consensus, "deadline_note"),
        override_params: match consensus.get("override") {
            Some(o) if o.is_object() && !o.as_object().unwrap().is_empty() => Some(o.clone()),
            Some(o) if !o.is_null() => return Err("consensus.override must be an object".into()),
            _ => None,
        },
        tuning: match v.get("tuning") {
            Some(t) if t.is_object() && t.get("cards").map(|c| c.is_object()).unwrap_or(false) => Some(t.clone()),
            Some(t) if !t.is_null() => return Err("tuning must be an object with a cards object".into()),
            _ => None,
        },
        drivers: match v.get("drivers") {
            Some(d) if d.is_object() => {
                crate::drivertable::Table::parse(d)?;
                Some(d.clone())
            }
            Some(d) if !d.is_null() => return Err("drivers must be an object".into()),
            _ => None,
        },
        ui: match v.get("ui") {
            Some(u) if u.is_object() => {
                let e = UiEntry { version: s(u, "version"), sha256: s(u, "sha256").to_ascii_lowercase(), size: u.get("size").and_then(|x| x.as_u64()).unwrap_or(0), url: s(u, "url"), min_engine: s(u, "min_engine"), signature: s(u, "signature").to_ascii_lowercase() };
                if parse_version(&e.version).is_none() {
                    return Err(format!("ui: version '{}' is not a version", e.version));
                }
                if parse_version(&e.min_engine).is_none() {
                    return Err(format!("ui: min_engine '{}' is not a version", e.min_engine));
                }
                if !e.url.starts_with("https://") && !e.url.starts_with("http://127.0.0.1:") {
                    return Err("ui: the url is not https".into());
                }
                if e.sha256.len() != 64 || !e.sha256.chars().all(|c| c.is_ascii_hexdigit()) {
                    return Err("ui: sha256 is not 64 hex characters".into());
                }
                if e.size == 0 {
                    return Err("ui: size is missing".into());
                }
                if e.signature.len() != 128 || !e.signature.chars().all(|c| c.is_ascii_hexdigit()) {
                    return Err("ui: signature is not 128 hex characters".into());
                }
                Some(e)
            }
            Some(u) if !u.is_null() => return Err("ui must be an object".into()),
            _ => None,
        },
    })
}

/// Verifies, then parses.
pub fn verify_and_parse(manifest_bytes: &[u8], sig_hex: &str, pub_hex: &str) -> Result<Manifest, String> {
    verify_signature(manifest_bytes, sig_hex, pub_hex)?;
    let text = std::str::from_utf8(manifest_bytes).map_err(|_| "manifest is not UTF-8")?;
    parse(text)
}

/// A digest of a whole directory (the staged app bundle): sha256 over "relative path\nsha256 of the file\n" for every
/// regular file in byte-sorted path order (symlinks skipped). The macOS helper recomputes the same with find, sort
/// and shasum right before the swap (R4.3.5).
pub fn digest_dir(root: &std::path::Path) -> std::io::Result<String> {
    fn walk(dir: &std::path::Path, root: &std::path::Path, out: &mut Vec<String>) -> std::io::Result<()> {
        for e in std::fs::read_dir(dir)? {
            let e = e?;
            let ft = e.file_type()?;
            let p = e.path();
            if ft.is_symlink() {
                continue;
            }
            if ft.is_dir() {
                walk(&p, root, out)?;
            } else if ft.is_file() {
                out.push(p.strip_prefix(root).unwrap_or(&p).to_string_lossy().replace('\\', "/"));
            }
        }
        Ok(())
    }
    let mut files = Vec::new();
    walk(root, root, &mut files)?;
    files.sort_by(|a, b| a.as_bytes().cmp(b.as_bytes()));
    let mut h = Sha256::new();
    for f in files {
        let sum = sha256_file(&root.join(&f))?;
        h.update(f.as_bytes());
        h.update(b"\n");
        h.update(sum.as_bytes());
        h.update(b"\n");
    }
    Ok(hex_encode(&h.finalize()))
}

/// SHA-256 of a file, streamed, as hex.
pub fn sha256_file(path: &std::path::Path) -> std::io::Result<String> {
    use std::io::Read;
    let mut f = std::fs::File::open(path)?;
    let mut h = Sha256::new();
    let mut buf = vec![0u8; 1 << 20];
    loop {
        let n = f.read(&mut buf)?;
        if n == 0 {
            break;
        }
        h.update(&buf[..n]);
    }
    Ok(hex_encode(&h.finalize()))
}

// ---- versions -----------------------------------------------------------------------------------------------

/// major.minor.patch plus an optional pre-release tag ("0.4.0-rc1" sorts before "0.4.0"). A leading "v" is allowed.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Version {
    pub parts: [u64; 3],
    pub pre: Option<String>,
}

pub fn parse_version(s: &str) -> Option<Version> {
    let s = s.trim().trim_start_matches('v');
    if s.is_empty() {
        return None;
    }
    let (num, pre) = match s.split_once('-') {
        Some((n, p)) if !p.is_empty() => (n, Some(p.to_string())),
        Some(_) => return None,
        None => (s, None),
    };
    let mut parts = [0u64; 3];
    let mut n = 0;
    for p in num.split('.') {
        if n >= 3 || p.is_empty() {
            return None;
        }
        parts[n] = p.parse().ok()?;
        n += 1;
    }
    if n == 0 {
        return None;
    }
    Some(Version { parts, pre })
}

impl PartialOrd for Version {
    fn partial_cmp(&self, other: &Self) -> Option<std::cmp::Ordering> {
        Some(self.cmp(other))
    }
}

impl Ord for Version {
    fn cmp(&self, other: &Self) -> std::cmp::Ordering {
        match self.parts.cmp(&other.parts) {
            std::cmp::Ordering::Equal => match (&self.pre, &other.pre) {
                (None, None) => std::cmp::Ordering::Equal,
                (None, Some(_)) => std::cmp::Ordering::Greater,
                (Some(_), None) => std::cmp::Ordering::Less,
                (Some(a), Some(b)) => a.cmp(b),
            },
            o => o,
        }
    }
}

/// True when `latest` is a newer version than `current`. Unparseable input is never newer.
pub fn newer(latest: &str, current: &str) -> bool {
    match (parse_version(latest), parse_version(current)) {
        (Some(a), Some(b)) => a > b,
        _ => false,
    }
}

// ---- when to apply --------------------------------------------------------------------------------------------

/// What the engine knows when it asks whether now is a safe moment.
#[derive(Clone, Debug, Default)]
pub struct Moment {
    pub node_synced: bool,
    /// DAA blocks (about seconds) to the next hourly program boundary; None when the node has not said.
    pub boundary_eta_s: Option<i64>,
    /// A worker is starting, exporting a pack or being built: let it finish.
    pub miner_busy: bool,
    /// A remote job is running on this engine (src/jobrun.rs). It holds even an urgent install: a job is bounded by its
    /// cap, and an install under it kills its process tree (the first Windows machine, 6 October 2026, 17:52:54Z: an earlier release went in during a
    /// measurement job because install_asked from a two-hour-old update-now still counted as urgent).
    pub job_active: bool,
    /// How long the update has been ready and waiting.
    pub ready_for_s: u64,
    /// A consensus activation is close, or this version is below min_supported_version: now beats later.
    pub urgent: bool,
    /// This machine's turn has come (stagger_seconds after the update is ready): machines take turns, a third of a
    /// fleet a minute, never all at once (F-E52: the hourly slot before it).
    pub slot_ok: bool,
    /// How much of the network's identity count (/api/live, last 10 minutes) is gone right now, in percent.
    pub network_drop_pct: f64,
    /// Horizon frontier lane (6 October 2026): the network's finality is paused (the node is synced and no checkpoint
    /// has locked for FINALITY_PAUSE_S); a rollout never lands on a chain that cannot lock
    pub finality_paused: bool,
    /// the signed manifest's own urgent flag: the one thing that installs while finality is paused
    pub manifest_urgent: bool,
    /// review B F14 (the founder, 8 October 2026): automatic updates are off in Settings; nothing installs, urgent or
    /// not, until the user presses Install now (`install_asked`)
    pub auto_update_off: bool,
    pub install_asked: bool,
    /// an earlier release (the shipper, the founder's mini, 8 October 2026): no card is mining or starting; an idle or refused miner
    /// is the strongest reason to apply, never a reason to wait.
    pub miner_idle: bool,
    /// How long the node has read synced, in seconds (0 when it does not).
    pub synced_for_s: u64,
}

/// With automatic updates on, a staged update applies within this many seconds of the node reading synced (and of the
/// staging, whichever is later), whatever the miner is doing: the machine's slot minute, the boundary guard and a
/// starting worker hold it no longer than this (an earlier release; the mini held an earlier release for its slot minute with an idle miner).
pub const SYNCED_APPLY_BOUND_S: u64 = 120;

/// No checkpoint lock for this long on a synced node = finality paused (the devnet locks every few minutes).
pub const FINALITY_PAUSE_S: f64 = 15.0 * 60.0;

/// The percentage of identities the network may lose in 10 minutes before updates hold (we are the devnet).
pub const NETWORK_DROP_HOLD_PCT: f64 = 30.0;

/// F-E52 (the project's ruling of 14 October 2026, 23:1x UK, the founder's twenty-minute roll): the hourly slot (the machine's
/// minute of the hour, up to an hour's wait) is replaced by a per-machine stagger of up to STAGGER_MAX_S seconds
/// after the update is ready, so a fleet still takes turns (a third of it a minute) and every machine applies within
/// the roll's bound. The hourly turn for large fleets comes back as a manifest field with the pool team's word.
pub const STAGGER_MAX_S: u64 = 180;

/// This machine's turn: how many seconds after the update is ready it applies (the first 8 hex of the machine id
/// modulo STAGGER_MAX_S).
pub fn stagger_seconds(id8: &str) -> u64 {
    u64::from_str_radix(id8.trim(), 16).unwrap_or(0) % STAGGER_MAX_S
}

/// Ok when the update may be applied now; Err carries the reason to wait, in the words the dashboard shows.
pub fn safe_to_apply(m: &Moment) -> Result<(), String> {
    // the user's choice comes first (F14): with automatic updates off nothing installs until Install now, an urgent
    // manifest included; an unsupported version pauses mining instead (the engine) and the card says install now
    if m.auto_update_off && !m.install_asked {
        return Err("automatic updates are off: waiting for Install now".into());
    }
    // the finality rule next: a fork-close or unsupported urgency does not pass it, only the manifest's flag or the
    // operator's hand (Install now, an earlier release: the held update can be the fix that restores the locks; the rule is for
    // unattended machines)
    if m.finality_paused && !m.manifest_urgent && !m.install_asked {
        return Err("waiting for finality: the network has not locked a checkpoint for 15 min; nothing installs on a chain that cannot lock".into());
    }
    if m.job_active {
        return Err("a remote job is running; installing when it closes".into());
    }
    // the operator's Install now is urgent in its own right (the engine folds it into `urgent` too)
    if m.urgent || m.install_asked {
        return Ok(());
    }
    if m.network_drop_pct > NETWORK_DROP_HOLD_PCT {
        return Err(format!("the network lost {:.0}% of its identities in the last 10 minutes; holding the update", m.network_drop_pct));
    }
    // an earlier release: a synced node with an idle miner applies at once; a mining one within SYNCED_APPLY_BOUND_S of the sync
    // and the staging, slot or no slot, boundary or no boundary
    if m.node_synced && (m.miner_idle || (m.synced_for_s >= SYNCED_APPLY_BOUND_S && m.ready_for_s >= SYNCED_APPLY_BOUND_S)) {
        return Ok(());
    }
    if !m.slot_ok {
        return Err("waiting for this machine's turn (up to three minutes after the update is ready; machines take turns)".into());
    }
    if m.ready_for_s >= SAFE_MOMENT_PATIENCE_S {
        return Ok(());
    }
    if !m.node_synced {
        return Err("waiting for the work source to answer".into());
    }
    if let Some(eta) = m.boundary_eta_s {
        if (0..=BOUNDARY_GUARD_S).contains(&eta) {
            return Err(format!("hourly program boundary in {} s; installing after it", eta.max(1)));
        }
    }
    if m.miner_busy {
        return Err("a worker is starting; installing once it runs".into());
    }
    Ok(())
}

/// A consensus activation is within FORK_URGENT_BLOCKS of the node's DAA score (and the node has a score).
pub fn fork_is_close(activation_height: Option<u64>, daa: u64) -> bool {
    match activation_height {
        // Horizon polish Q4 (6 October 2026): an activation at or below the DAA has PASSED, nothing is pending; the
        // old rule read it as close, so every update since the an earlier release manifest said "0 blocks away, installing now",
        // stripped Later and skipped every safe-moment guard (the first Windows machine's 17:52:54Z install under a job came through it)
        Some(h) if daa > 0 && h > daa => daa.saturating_add(FORK_URGENT_BLOCKS) >= h,
        _ => false,
    }
}

/// `current` is older than the manifest's min_supported_version.
pub fn unsupported(m: &Manifest, current: &str) -> bool {
    !m.min_supported_version.is_empty() && newer(&m.min_supported_version, current)
}

#[cfg(test)]
mod tests {
    use super::*;
    use ed25519_dalek::{Signer, SigningKey};

    /// The helper's bash recipe (find | sort | shasum per file | shasum) must equal digest_dir.
    #[test]
    #[cfg(target_os = "macos")]
    fn digest_dir_matches_the_helper() {
        let root = std::env::temp_dir().join(format!("igneum-digest-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&root);
        std::fs::create_dir_all(root.join("Contents/MacOS")).unwrap();
        std::fs::write(root.join("Contents/MacOS/igneum-app"), b"engine").unwrap();
        std::fs::write(root.join("Contents/Info.plist"), b"<plist/>").unwrap();
        std::fs::write(root.join("Contents/zed.txt"), b"z").unwrap();
        let ours = digest_dir(&root).unwrap();
        let recipe = r#"(cd "$1" && /usr/bin/find . -type f -print | LC_ALL=C /usr/bin/sort | while IFS= read -r f; do printf '%s\n%s\n' "${f#./}" "$(/usr/bin/shasum -a 256 "$f" | /usr/bin/cut -d' ' -f1)"; done) | /usr/bin/shasum -a 256 | /usr/bin/cut -d' ' -f1"#;
        let out = std::process::Command::new("/bin/bash").args(["-c", recipe, "x", &root.display().to_string()]).output().unwrap();
        let theirs = String::from_utf8_lossy(&out.stdout).trim().to_string();
        let _ = std::fs::remove_dir_all(&root);
        assert_eq!(ours, theirs);
    }

    #[test]
    fn the_ui_entry_parses_and_every_bad_field_fails() {
        use ed25519_dalek::{Signer, SigningKey};
        let sk = SigningKey::from_bytes(&[3u8; 32]);
        let pk = hex_encode(sk.verifying_key().as_bytes());
        let sha = "ab".repeat(32);
        let sig = hex_encode(&sk.sign(&ui_sign_bytes("1.0.1", &sha, "0.1.19")).to_bytes());
        let base = serde_json::json!({ "version": "0.1.19", "platforms": {}, "ui": { "version": "1.0.1", "sha256": sha, "size": 400000, "url": "https://dl.igneum.network/dl/t/ui/igneum-ui-1.0.1.tar.gz", "min_engine": "0.1.19", "signature": sig } });
        let m = parse(&base.to_string()).unwrap();
        let u = m.ui.clone().unwrap();
        assert_eq!(u.version, "1.0.1");
        assert_eq!(u.min_engine, "0.1.19");
        assert!(verify_ui_entry(&u, &pk).is_ok());
        let mut t = u.clone();
        t.sha256 = "00".repeat(32);
        assert!(verify_ui_entry(&t, &pk).is_err(), "a changed hash breaks the entry's signature");
        let mut t = u.clone();
        t.min_engine = "0.1.0".into();
        assert!(verify_ui_entry(&t, &pk).is_err(), "a changed engine floor breaks it too");
        assert!(verify_ui_entry(&u, &hex_encode(SigningKey::from_bytes(&[4u8; 32]).verifying_key().as_bytes())).is_err());
        assert_eq!(parse(r#"{"version":"0.1.19","platforms":{}}"#).unwrap().ui, None, "no ui object: the kill switch");
        for (k, v) in [("version", serde_json::json!("x")), ("min_engine", serde_json::json!("")), ("url", serde_json::json!("http://evil/x.tar.gz")), ("sha256", serde_json::json!("abc")), ("size", serde_json::json!(0)), ("signature", serde_json::json!("zz"))] {
            let mut b = base.clone();
            b["ui"][k] = v;
            assert!(parse(&b.to_string()).is_err(), "ui.{k} bad must fail");
        }
        assert!(parse(r#"{"version":"0.1.19","platforms":{},"ui":"x"}"#).is_err());
        assert_eq!(std::str::from_utf8(&ui_sign_bytes("1.0.1", "AB", "0.1.19")).unwrap(), "igneum-ui\n1.0.1\nab\n0.1.19\n");
    }

    #[test]
    fn tuning_parses() {
        let m = parse(r#"{"version":"0.1.4","platforms":{},"tuning":{"updated":"2026-10-04T20:00:00Z","cards":{"NVIDIA_GeForce_RTX_5090":{"variant":"u2-ldg","race":true,"candidates":["u2-ldg","ldg","base"]}}}}"#).unwrap();
        assert_eq!(m.tuning.as_ref().unwrap()["cards"]["NVIDIA_GeForce_RTX_5090"]["variant"], "u2-ldg");
        assert!(parse(r#"{"version":"0.1.4","platforms":{},"tuning":null}"#).unwrap().tuning.is_none());
        assert!(parse(r#"{"version":"0.1.4","platforms":{}}"#).unwrap().tuning.is_none());
        assert!(parse(r#"{"version":"0.1.4","platforms":{},"tuning":"fast"}"#).is_err());
        assert!(parse(r#"{"version":"0.1.4","platforms":{},"tuning":{"cards":[]}}"#).is_err());
    }

    #[test]
    fn consensus_override_parses() {
        let m = parse(r#"{"version":"0.1.2","platforms":{},"consensus":{"activation_height":5000,"override":{"difficulty_v2_activation_daa":5000}}}"#).unwrap();
        assert_eq!(m.activation_height, Some(5000));
        assert_eq!(m.override_params.unwrap()["difficulty_v2_activation_daa"], 5000);
        assert!(parse(r#"{"version":"0.1.2","platforms":{},"consensus":{"override":"no"}}"#).is_err());
    }

    const SAMPLE: &str = r#"{"channel":"devnet","consensus":{"activation_height":120000,"deadline_note":"difficulty v2"},"min_supported_version":"0.1.0","notes":"difficulty v2 at height 120000","platforms":{"mac":{"kind":"dmg","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","size":20588331,"url":"https://dl.igneum.network/dl/t/Ember-0.1.1.dmg"},"windows":{"kind":"inno-setup","sha256":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","size":43978429,"url":"https://dl.igneum.network/dl/t/Ember-Setup-0.1.1.exe"}},"published_at":"2026-10-04T13:00:00Z","version":"0.1.1"}"#;

    fn key() -> (SigningKey, String) {
        let sk = SigningKey::from_bytes(&[7u8; 32]);
        let pk = hex_encode(sk.verifying_key().as_bytes());
        (sk, pk)
    }

    /// V6-06: the windows entry may name the engine exe's own sha256 (the manifest's windows entry (publish-release-manifest.sh)), what the
    /// Power Helper checks before it refreshes its protected copy; absent = empty, nothing refreshes.
    #[test]
    fn the_windows_entry_carries_the_engine_hash_when_published() {
        let sha = "cd".repeat(32);
        let m = parse(&format!(r#"{{"version":"0.2.2","platforms":{{"windows":{{"url":"https://x/s.exe","sha256":"{sha}","size":1,"kind":"inno-setup","engine_sha256":"ABCD"}}}}}}"#)).unwrap();
        assert_eq!(m.windows.unwrap().engine_sha256, "abcd");
        let n = parse(&format!(r#"{{"version":"0.2.2","platforms":{{"windows":{{"url":"https://x/s.exe","sha256":"{sha}","size":1,"kind":"inno-setup"}}}}}}"#)).unwrap();
        assert_eq!(n.windows.unwrap().engine_sha256, "");
    }

    #[test]
    fn parses_manifest() {
        let m = parse(SAMPLE).unwrap();
        assert_eq!(m.version, "0.1.1");
        assert_eq!(m.channel, "devnet");
        assert_eq!(m.activation_height, Some(120000));
        assert_eq!(m.deadline_note, "difficulty v2");
        assert_eq!(m.min_supported_version, "0.1.0");
        let mac = m.mac.as_ref().unwrap();
        assert_eq!(mac.kind, "dmg");
        assert_eq!(mac.size, 20588331);
        assert_eq!(m.windows.as_ref().unwrap().kind, "inno-setup");
        assert_eq!(m.platform("linux"), None);
    }

    #[test]
    fn missing_platform_is_none_and_bad_entries_fail() {
        let m = parse(r#"{"version":"0.1.1","platforms":{"mac":null},"consensus":{"activation_height":null}}"#).unwrap();
        assert!(m.mac.is_none() && m.windows.is_none());
        assert_eq!(m.activation_height, None);
        assert!(parse(r#"{"version":"0.1.1","platforms":{"mac":{"url":"http://x","sha256":"aa","size":1,"kind":"dmg"}}}"#).unwrap_err().contains("https"));
        assert!(parse(r#"{"version":"0.1.1","platforms":{"mac":{"url":"http://127.0.0.1:29790/x.dmg","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","size":1,"kind":"dmg"}}}"#).is_ok());
        assert!(parse(r#"{"version":"0.1.1","platforms":{"mac":{"url":"https://x","sha256":"aa","size":1,"kind":"dmg"}}}"#).unwrap_err().contains("sha256"));
        assert!(parse(r#"{"version":"0.1.1","platforms":{"mac":{"url":"https://x","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","size":1,"kind":"tar"}}}"#).unwrap_err().contains("kind"));
        assert!(parse(r#"{"version":"latest"}"#).is_err());
        assert!(parse("not json").is_err());
    }

    #[test]
    fn signature_verifies_and_tampering_fails() {
        let (sk, pk) = key();
        let sig = hex_encode(&sk.sign(SAMPLE.as_bytes()).to_bytes());
        assert!(verify_signature(SAMPLE.as_bytes(), &sig, &pk).is_ok());
        let m = verify_and_parse(SAMPLE.as_bytes(), &sig, &pk).unwrap();
        assert_eq!(m.version, "0.1.1");
        // a tampered sha256 inside the manifest: the bytes changed, the signature no longer verifies
        let tampered = SAMPLE.replace("aaaaaaaa", "aaaaaaab");
        assert!(verify_and_parse(tampered.as_bytes(), &sig, &pk).is_err());
        // a bad signature
        let mut bad = sig.clone();
        bad.replace_range(0..2, if &sig[0..2] == "00" { "01" } else { "00" });
        assert!(verify_signature(SAMPLE.as_bytes(), &bad, &pk).is_err());
        // another key
        let other = hex_encode(SigningKey::from_bytes(&[9u8; 32]).verifying_key().as_bytes());
        assert!(verify_signature(SAMPLE.as_bytes(), &sig, &other).is_err());
        // garbage
        assert!(verify_signature(SAMPLE.as_bytes(), "zz", &pk).is_err());
        assert!(verify_signature(SAMPLE.as_bytes(), &sig, "abcd").is_err());
    }

    #[test]
    fn sha256_of_file_is_checked_by_the_caller() {
        let dir = std::env::temp_dir().join(format!("igneum-manifest-test-{}", std::process::id()));
        std::fs::create_dir_all(&dir).unwrap();
        let f = dir.join("x.bin");
        std::fs::write(&f, b"abc").unwrap();
        assert_eq!(sha256_file(&f).unwrap(), "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad");
        std::fs::write(&f, b"abd").unwrap();
        assert_ne!(sha256_file(&f).unwrap(), "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad");
        let _ = std::fs::remove_dir_all(&dir);
    }

    /// The founder's real signature over Ember 0.1.0's SHA256SUMS (the published release, 11 October 2026): the
    /// SSHSIG envelope under the namespace igneum-release, sha512 inside, the release key's raw bytes pinned above.
    const SUMS: &str = "eefffb1185c15a34cfdcf539b2e33c0977dc2b016c05c7a964fc9ddabbbdfcec  ember-linux-x86_64-0.1.0-879295c7.tar.gz\n4c1115f6f1fd2bd39f468f9e43fe824857e555c77a4c99a8d1db51d3e10d53ab  ember-hiveos-0.1.0-879295c7.tar.gz\nab1e7296fc4f4561faddc92489e13bde7031803e455b083e9c2583fe40a37a8d  ember-879295c7.exe\ncc630a2e45d5f69b1c6b49aa5a7c23bd5c2226f229938c92589ec84ffb034961  ember-macos-arm64-0.1.0-879295c7.tar.gz\n";
    const SUMS_SIG: &str = "-----BEGIN SSH SIGNATURE-----\nU1NIU0lHAAAAAQAAADMAAAALc3NoLWVkMjU1MTkAAAAgwYle3VrcbBPC7aRyruIySoVMDc\npQI6gOWSvUFYjxRMUAAAAOaWduZXVtLXJlbGVhc2UAAAAAAAAABnNoYTUxMgAAAFMAAAAL\nc3NoLWVkMjU1MTkAAABAKZRpYhy4RH85xsxygTfSdWQy3g+jgHG2VWQ7nurF+lEJ+k9zYS\n0eI7e89DLnJOcxRbgMazcAQjD3HBwdRzXXAw==\n-----END SSH SIGNATURE-----\n";

    #[test]
    fn the_release_keys_ssh_signature_verifies_and_every_other_thing_is_refused() {
        assert_eq!(verify_signature(SUMS.as_bytes(), SUMS_SIG, OTA_PUBLIC_KEY_HEX), Ok(()), "the founder's signature over the published SHA256SUMS");
        assert_eq!(verify_sshsig(SUMS.as_bytes(), SUMS_SIG, OTA_PUBLIC_KEY_HEX, "igneum-release"), Ok(()));
        // the compiled rotation list (keys/allowed_signers, the served three-line list of 12 October 2026)
        let signers = release_signers();
        assert!(signers.len() >= 3, "the list carries the release keys: {signers:?}");
        let by = |fp: &str| signers.iter().find(|s| s.fingerprint == fp).cloned().unwrap_or_else(|| panic!("{fp} is not in the list"));
        let (k1, k2, k3) = (by("SHA256:umhm59z0pvL2OCPwi0zl1VcRCr4poHILz9zbzWEGCLk"), by("SHA256:5AolO/d43OyyJ+Ao3A755Vf9zsKL82TUI6M48hs/0uw"), by("SHA256:EdoH5aY4DWzQFohAWAju9m5OwfhRUj2J+eYVcUYJkwI"));
        assert_eq!(k1.hex, OTA_PUBLIC_KEY_HEX, "key 1 is the root");
        assert_eq!(k2.valid_after, unix_from_rfc3339("2026-10-12T08:20:00Z"));
        assert_eq!(k3.valid_after, unix_from_rfc3339("2026-10-12T10:43:00Z"), "the fresh automatic key of release/rotation-4");
        // the first automatic key (release/rotation-4 cded47fd: it signs 0.2.3 and 0.2.4; no mark on it yet)
        let k0 = by("SHA256:T0hJf1nD0cHC34ozAyMoC5O62Wris2ptLEvn+hjGoqM");
        assert_eq!(k0.valid_after, unix_from_rfc3339("2026-10-12T10:24:00Z"));
        // the mark written at the rotation's close (keys/SUPERSEDED): the first automatic key is superseded from
        // midnight after its sets, so its day's sets and pointers verify and anything it signs after is refused
        assert_eq!(k0.superseded_from, unix_from_rfc3339("2026-10-13T00:00:00Z"));
        assert_eq!(superseded_check(&k0, "2026-10-12T12:45:00Z"), Ok(()), "the day's set under the first automatic key verifies");
        assert_eq!(superseded_check(&k0, "2026-10-12T18:00:00Z"), Ok(()), "and its pointers that evening");
        assert!(superseded_check(&k0, "2026-10-13T00:00:01Z").unwrap_err().contains("superseded"), "a manifest it signs after midnight is refused");
        let signers = vec![k1.clone(), k2.clone(), k3.clone()];
        // the signer of the founder's signature is key 1; a key-1 signature is refused under another pinned root
        let s1 = release_signer_of(SUMS_SIG).expect("key 1 in the list");
        assert_eq!(s1.hex, OTA_PUBLIC_KEY_HEX);
        assert_eq!(release_signer_of("-----BEGIN SSH SIGNATURE-----\nU1NIU0lH\n-----END SSH SIGNATURE-----"), None);
        assert!(verify_sshsig(SUMS.as_bytes(), SUMS_SIG, &signers[1].hex, "igneum-release").unwrap_err().contains("not the release key"), "key 2 pinned alone does not admit key 1");
        // the supersession marks (keys/SUPERSEDED): the root key is superseded for signing from 17:30Z on 12 October
        // 2026 (the bridge sets signed by it that night keep verifying); the rule itself is read on a synthetic signer
        // marked from 14:00:00Z
        assert_eq!(s1.superseded_from, unix_from_rfc3339("2026-10-12T17:30:00Z"));
        assert_eq!(superseded_check(&s1, "2026-10-12T15:00:00Z"), Ok(()), "before the mark: a manifest under key 1 verifies");
        assert!(superseded_check(&s1, "2026-10-12T18:00:00Z").is_err(), "after the mark: refused");
        let marked = super::Signer { superseded_from: unix_from_rfc3339("2026-10-12T14:00:00Z"), ..s1.clone() };
        assert_eq!(superseded_check(&marked, "2026-10-12T13:30:00Z"), Ok(()), "signed before the mark: good");
        assert!(superseded_check(&marked, "2026-10-12T15:00:00Z").unwrap_err().contains("superseded"), "signed after the mark: refused");
        let two = parse_signers(ALLOWED_SIGNERS, "SHA256:umhm59z0pvL2OCPwi0zl1VcRCr4poHILz9zbzWEGCLk 2026-10-12T14:00:00Z x y\n");
        assert_eq!(two[0].superseded_from, unix_from_rfc3339("2026-10-12T14:00:00Z"), "a mark in the list is read onto its key");
        assert!(superseded_check(&signers[2], "2026-10-12T10:00:00Z").unwrap_err().contains("not valid until"), "before valid-after: refused");
        assert_eq!(superseded_check(&signers[2], "2026-10-12T12:00:00Z"), Ok(()));
        assert!(superseded_check(&s1, "").unwrap_err().contains("no written_utc"), "no written_utc: refused, never judged on published_at");
        assert!(superseded_check(&s1, "2026-10-12").unwrap_err().contains("no written_utc"), "a date without a time is not RFC 3339: refused");
        // the helpers
        assert_eq!(base64_encode_nopad(b"hello"), "aGVsbG8");
        assert_eq!(base64_encode_nopad(b""), "");
        assert_eq!(unix_from_ssh_time("20261012082000Z"), unix_from_rfc3339("2026-10-12T08:20:00Z"));
        assert_eq!(unix_from_ssh_time("20261012"), unix_from_rfc3339("2026-10-12T00:00:00Z"));
        assert_eq!(parse_signers("# none\n", "").len(), 0);
        assert_eq!(parse_signers("igneum-release namespaces=\"igneum-release\" ssh-ed25519 not-base64!\n", "").len(), 0, "a malformed line is skipped");
        // one byte of the message changed: refused
        let mut other = SUMS.to_string(); other.replace_range(0..1, "f");
        assert!(verify_signature(other.as_bytes(), SUMS_SIG, OTA_PUBLIC_KEY_HEX).is_err());
        // another namespace: refused before the primitive runs
        assert!(verify_sshsig(SUMS.as_bytes(), SUMS_SIG, OTA_PUBLIC_KEY_HEX, "igneum-update").unwrap_err().contains("namespace"));
        // another pinned key: the key inside the envelope is not it
        let other_key = "b3c9c5bd144e9d246dc0edf897387d4f3f7ca494cd47457123d1c2f892cabddd";
        assert!(verify_signature(SUMS.as_bytes(), SUMS_SIG, other_key).unwrap_err().contains("not the release key"));
        // the armour mangled: refused, never a panic
        assert!(verify_signature(SUMS.as_bytes(), "-----BEGIN SSH SIGNATURE-----\nU1NIU0lH\n-----END SSH SIGNATURE-----", OTA_PUBLIC_KEY_HEX).is_err());
        assert!(verify_signature(SUMS.as_bytes(), "-----BEGIN SSH SIGNATURE-----\n@@@\n-----END SSH SIGNATURE-----", OTA_PUBLIC_KEY_HEX).is_err());
        // the hex form still reads the old way (a wrong-length hex is refused as before)
        assert!(verify_signature(SUMS.as_bytes(), "abcd", OTA_PUBLIC_KEY_HEX).is_err());
        assert_eq!(base64_decode("U1NIU0lH").unwrap(), b"SSHSIG");
        assert_eq!(fingerprint(OTA_PUBLIC_KEY_HEX).len(), 64);
    }

    #[test]
    fn versions() {
        assert!(newer("0.1.1", "0.1.0"));
        assert!(newer("0.4.0", "0.1.9"));
        assert!(newer("1.0.0", "0.99.99"));
        assert!(newer("v0.1.1", "0.1.0"));
        assert!(!newer("0.1.0", "0.1.0"));
        assert!(!newer("0.0.9", "0.1.0"));
        assert!(!newer("0.1", "0.1.0"));
        assert!(newer("0.1.1", "0.1"));
        assert!(newer("0.1.1", "0.1.1-rc1"));
        assert!(!newer("0.1.1-rc1", "0.1.1"));
        assert!(newer("0.1.1-rc2", "0.1.1-rc1"));
        assert!(!newer("", "0.1.0"));
        assert!(!newer("latest", "0.1.0"));
        assert!(!newer("0.1.1", "garbage"));
        assert!(!newer("0.1.1-", "0.1.0"));
        assert!(!newer("0.1.1.2", "0.1.0"));
    }

    /// Known failed first (the shipper, the founder's mini, 8 October 2026 20:30 to 20:40 UK): an earlier release staged at 20:30:22,
    /// the node synced from 20:35, the miner idle the whole time, and nothing installed by 20:40 because the machine's
    /// own minute of the hour had not come. The rule: with automatic updates on, a staged update applies within
    /// SYNCED_APPLY_BOUND_S of the node reading synced, whatever the miner is doing; an idle or refused miner is the
    /// strongest reason to apply, never a reason to wait.
    #[test]
    fn a_staged_update_applies_within_two_minutes_of_sync_and_at_once_on_an_idle_miner() {
        let staged = Moment { node_synced: true, boundary_eta_s: Some(1800), miner_busy: false, job_active: false, ready_for_s: 300, urgent: false, slot_ok: false, network_drop_pct: 0.0, finality_paused: false, manifest_urgent: false, auto_update_off: false, install_asked: false, miner_idle: false, synced_for_s: 0 };
        assert_eq!(SYNCED_APPLY_BOUND_S, 120);
        // the mini's case: node synced, miner idle, outside the slot: applies at once
        assert!(safe_to_apply(&Moment { miner_idle: true, synced_for_s: 1, ..staged.clone() }).is_ok());
        // a mining miner: within the bound of the sync, outside the slot and inside the boundary guard, it still applies
        assert!(safe_to_apply(&Moment { synced_for_s: SYNCED_APPLY_BOUND_S, boundary_eta_s: Some(30), ..staged.clone() }).is_ok());
        assert!(safe_to_apply(&Moment { synced_for_s: SYNCED_APPLY_BOUND_S - 1, ..staged.clone() }).unwrap_err().contains("machine's turn"));
        // the bound counts from the later of the sync and the staging
        assert!(safe_to_apply(&Moment { synced_for_s: 3600, ready_for_s: SYNCED_APPLY_BOUND_S - 1, ..staged.clone() }).is_err());
        assert!(safe_to_apply(&Moment { synced_for_s: 3600, ready_for_s: SYNCED_APPLY_BOUND_S, ..staged.clone() }).is_ok());
        // an unsynced node still waits, idle miner or not; a job, paused finality and updates-off still hold
        assert!(safe_to_apply(&Moment { node_synced: false, miner_idle: true, synced_for_s: 0, ..staged.clone() }).is_err());
        assert!(safe_to_apply(&Moment { miner_idle: true, synced_for_s: 500, job_active: true, ..staged.clone() }).unwrap_err().contains("remote job"));
        assert!(safe_to_apply(&Moment { miner_idle: true, synced_for_s: 500, finality_paused: true, ..staged.clone() }).unwrap_err().contains("finality"));
        assert!(safe_to_apply(&Moment { miner_idle: true, synced_for_s: 500, auto_update_off: true, ..staged.clone() }).unwrap_err().contains("Install now"));
        // 20:45 on the mini: the finality guard held the update that was the fix for the locks, and Install now could not
        // pass it; the operator's hand outranks a rule for unattended machines (a fork-close urgency alone still does not)
        assert!(safe_to_apply(&Moment { finality_paused: true, install_asked: true, ..staged.clone() }).is_ok());
        assert!(safe_to_apply(&Moment { finality_paused: true, urgent: true, install_asked: false, ..staged.clone() }).unwrap_err().contains("finality"));
    }

    #[test]
    /// F-E52 (14 October 2026): from the pointer's move to the apply on every platform, the worst case is one check
    /// interval with its jitter, the download (allowed a minute), this machine's turn and nothing else: under nine minutes.
    #[test]
    fn the_pointer_is_taken_within_nine_minutes_on_every_platform() {
        let check_worst = crate::ota::CHECK_EVERY_S + crate::ota::CHECK_EVERY_S / 6 + 1;
        assert!(check_worst + 60 + STAGGER_MAX_S <= 9 * 60, "the check, the download and the turn must fit nine minutes");
        assert_eq!(stagger_seconds("00000000"), 0);
        assert_eq!(stagger_seconds("000000b3"), 179);
        assert_eq!(stagger_seconds("000000b4"), 0);
        assert!(stagger_seconds("1467b02b") < STAGGER_MAX_S);
    }

    fn safe_moments() {
        let base = Moment { node_synced: true, boundary_eta_s: Some(1800), miner_busy: false, job_active: false, ready_for_s: 60, urgent: false, slot_ok: true, network_drop_pct: 0.0, finality_paused: false, manifest_urgent: false, auto_update_off: false, install_asked: false, miner_idle: false, synced_for_s: 60 };
        assert!(safe_to_apply(&base).is_ok());
        // the finality rule (Horizon frontier lane, 6 October 2026), the known-failed case: paused, no install; active, install;
        // paused with the manifest's urgent flag, install; paused with only a fork-close urgency, no install; patience never passes it
        assert!(safe_to_apply(&Moment { finality_paused: true, ..base.clone() }).unwrap_err().starts_with("waiting for finality"));
        assert!(safe_to_apply(&Moment { finality_paused: false, ..base.clone() }).is_ok());
        assert!(safe_to_apply(&Moment { finality_paused: true, manifest_urgent: true, ..base.clone() }).is_ok());
        // F14 (known-failed first on an earlier release: urgent beat auto_update = false): off stays off until Install now
        assert_eq!(safe_to_apply(&Moment { auto_update_off: true, urgent: true, manifest_urgent: true, ..base.clone() }).unwrap_err(), "automatic updates are off: waiting for Install now");
        assert!(safe_to_apply(&Moment { auto_update_off: true, install_asked: true, ..base.clone() }).is_ok());
        assert!(safe_to_apply(&Moment { auto_update_off: true, urgent: true, install_asked: true, ..base.clone() }).is_ok());
        assert!(safe_to_apply(&Moment { finality_paused: true, urgent: true, ..base.clone() }).is_err());
        assert!(safe_to_apply(&Moment { finality_paused: true, ready_for_s: SAFE_MOMENT_PATIENCE_S, ..base.clone() }).is_err());
        assert_eq!(safe_to_apply(&Moment { node_synced: false, ..base.clone() }).unwrap_err(), "waiting for the work source to answer");
        assert!(safe_to_apply(&Moment { boundary_eta_s: Some(120), ..base.clone() }).unwrap_err().contains("boundary in 120 s"));
        assert!(safe_to_apply(&Moment { boundary_eta_s: Some(0), ..base.clone() }).is_err());
        assert!(safe_to_apply(&Moment { boundary_eta_s: Some(181), ..base.clone() }).is_ok());
        assert!(safe_to_apply(&Moment { boundary_eta_s: None, ..base.clone() }).is_ok());
        assert!(safe_to_apply(&Moment { miner_busy: true, ..base.clone() }).unwrap_err().contains("worker"));
        // urgent beats every wait
        assert!(safe_to_apply(&Moment { node_synced: false, boundary_eta_s: Some(5), miner_busy: true, urgent: true, ..base.clone() }).is_ok());
        // the first Windows machine, 6 October 2026, 17:52:54Z: a scheduled update arriving mid-job is deferred to the job's close, urgent or not
        assert!(safe_to_apply(&Moment { job_active: true, ..base.clone() }).unwrap_err().contains("remote job"));
        assert!(safe_to_apply(&Moment { job_active: true, urgent: true, ..base.clone() }).unwrap_err().contains("remote job"));
        assert!(safe_to_apply(&Moment { job_active: true, urgent: true, slot_ok: false, ..base.clone() }).is_err());
        assert!(safe_to_apply(&Moment { job_active: false, urgent: true, slot_ok: false, ..base.clone() }).is_ok());
        // patience: an unsynced node for 6 h applies anyway
        assert!(safe_to_apply(&Moment { node_synced: false, ready_for_s: SAFE_MOMENT_PATIENCE_S, ..base.clone() }).is_ok());
        assert!(safe_to_apply(&Moment { node_synced: false, ready_for_s: SAFE_MOMENT_PATIENCE_S - 1, ..base.clone() }).is_err());
        // the machine's slot: outside it nothing applies in the first SYNCED_APPLY_BOUND_S of a synced node (an earlier release; before
        // that nothing applied outside it at all, not even with patience); urgent ignores it
        assert!(safe_to_apply(&Moment { slot_ok: false, ..base.clone() }).unwrap_err().contains("machine's turn"));
        assert!(safe_to_apply(&Moment { slot_ok: false, ready_for_s: SAFE_MOMENT_PATIENCE_S, ..base.clone() }).is_err());
        assert!(safe_to_apply(&Moment { slot_ok: false, urgent: true, ..base.clone() }).is_ok());
        // the network guard: over 30% of identities gone in 10 minutes holds the update (we are the devnet)
        assert!(safe_to_apply(&Moment { network_drop_pct: 30.0, ..base.clone() }).is_ok());
        assert!(safe_to_apply(&Moment { network_drop_pct: 30.1, ..base.clone() }).unwrap_err().contains("lost 30%"));
        assert!(safe_to_apply(&Moment { network_drop_pct: 80.0, ready_for_s: SAFE_MOMENT_PATIENCE_S, ..base.clone() }).is_err());
        assert!(safe_to_apply(&Moment { network_drop_pct: 80.0, urgent: true, ..base.clone() }).is_ok());
    }

    #[test]
    fn turns() {
        // F-E52: the turn is seconds after ready, never a minute of the hour
        assert_eq!(stagger_seconds("1ccfe586"), 0x1ccfe586u64 % 180); // the second Windows machine
        assert_eq!(stagger_seconds("zz"), 0);
        assert!(stagger_seconds("ffffffff") < STAGGER_MAX_S);
    }

    #[test]
    fn fork_closeness_and_support() {
        assert!(!fork_is_close(None, 100_000));
        assert!(!fork_is_close(Some(120_000), 0));
        assert!(!fork_is_close(Some(120_000), 118_199));
        assert!(fork_is_close(Some(120_000), 118_200));
        assert!(fork_is_close(Some(120_000), 119_999), "one block before the activation");
        // a passed activation is not close (Horizon polish Q4): at the height and after it, nothing is pending
        assert!(!fork_is_close(Some(120_000), 120_000));
        assert!(!fork_is_close(Some(120_000), 130_000));
        assert!(!fork_is_close(Some(33_000), 201_776), "the 0.1.14 manifest's case: difficulty v2 at 33,000 against the first Windows machine's DAA");
        let m = parse(SAMPLE).unwrap();
        assert!(!unsupported(&m, "0.1.0"));
        assert!(unsupported(&m, "0.0.9"));
        assert!(!unsupported(&parse(r#"{"version":"0.1.1"}"#).unwrap(), "0.0.1"));
    }

    #[test]
    fn fingerprint_is_sha256_of_key_bytes() {
        let (_, pk) = key();
        assert_eq!(fingerprint(&pk).len(), 64);
        assert_eq!(fingerprint("zz"), "");
    }
}

/// Unix seconds of a manifest `published_at` ("2026-10-04T13:00:00Z", whole seconds, UTC); `None` for any other shape.
pub fn unix_from_rfc3339(t: &str) -> Option<u64> {
    let t = t.trim();
    let b = t.as_bytes();
    if b.len() < 20 || b[4] != b'-' || b[7] != b'-' || b[10] != b'T' || b[13] != b':' || b[16] != b':' || !t.ends_with('Z') {
        return None;
    }
    let n = |a: usize, z: usize| t[a..z].parse::<i64>().ok();
    let (y, m, d, hh, mm, ss) = (n(0, 4)?, n(5, 7)?, n(8, 10)?, n(11, 13)?, n(14, 16)?, n(17, 19)?);
    if !(1..=12).contains(&m) || !(1..=31).contains(&d) || hh > 23 || mm > 59 || ss > 60 {
        return None;
    }
    // days from civil (Howard Hinnant), valid for every date after 1970
    let (y2, m2) = if m <= 2 { (y - 1, m + 9) } else { (y, m - 3) };
    let era = y2.div_euclid(400);
    let yoe = y2 - era * 400;
    let doy = (153 * m2 + 2) / 5 + d - 1;
    let doe = yoe * 365 + yoe / 4 - yoe / 100 + doy;
    let days = era * 146097 + doe - 719468;
    if days < 0 {
        return None;
    }
    Some((days as u64) * 86400 + (hh as u64) * 3600 + (mm as u64) * 60 + ss as u64)
}

#[cfg(test)]
mod rfc3339_tests {
    use super::unix_from_rfc3339;
    #[test]
    fn a_manifest_publish_time_parses_to_unix_seconds() {
        assert_eq!(unix_from_rfc3339("1970-01-01T00:00:00Z"), Some(0));
        assert_eq!(unix_from_rfc3339("2026-10-05T23:57:49Z"), Some(1791244669));
        assert_eq!(unix_from_rfc3339("2026-10-04T13:00:00Z"), Some(1791118800));
        assert_eq!(unix_from_rfc3339(""), None);
        assert_eq!(unix_from_rfc3339("2026-10-05 23:57:49"), None);
        assert_eq!(unix_from_rfc3339("2026-13-05T23:57:49Z"), None);
    }
}
