//! The miner dev fee (ED-05; the project's order of 11 October 2026, "prepare, do not land": the founder decides tonight
//! whether Ember ships it ON by default). Software, not protocol: on a node session (the share lane) one template
//! in a hundred is requested with the company's dev payout address in place of the miner's, so the shares found on
//! it credit the dev address; the vote key and the chain's rules are untouched. `--dev-fee <percent>`, default 1;
//! `--dev-fee 0` off. The dev address is never in the code: it is read from the release's signed dev-fee manifest
//! beside the workers (`dev-fee-manifest.json` with `dev-fee-manifest.json.sig`, the founder's `ssh-keygen -Y sign`
//! under the release namespace, verified against the pinned release key), and without that file, its signature or
//! the key the fee is OFF and the start line says why (fail closed: no address, no fee).

use ed25519_dalek::{Signature, Verifier, VerifyingKey};
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};
use std::path::{Path, PathBuf};

/// The release signing key's raw 32 bytes (infra/release/keys/release-signing.pub, ssh-ed25519, the founder's,
/// fingerprint SHA256:umhm59z0pvL2OCPwi0zl1VcRCr4poHILz9zbzWEGCLk), the same constant the app's updater pins.
pub const RELEASE_PUBLIC_KEY_HEX: &str = "c1895edd5adc6c13c2eda472aee2324a854c0dca5023a80e592bd41588f144c5";
pub const RELEASE_SIG_NAMESPACE: &str = "igneum-release";
pub const MANIFEST_NAME: &str = "dev-fee-manifest.json";
pub const SIGNATURE_NAME: &str = "dev-fee-manifest.json.sig";

/// The signed manifest's fields (the rest is kept as it is).
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct DevFeeManifest {
    pub component: String,
    /// `0x` + 40 hex: the company's dev payout address.
    pub dev_payout_address: String,
    /// The percent the release ships as the default (1); the operator's `--dev-fee` overrides it, 0 off.
    #[serde(default = "one")]
    pub default_percent: u8,
    #[serde(default)]
    pub note: String,
}

fn one() -> u8 {
    1
}

/// The fee in force for a session.
#[derive(Debug, Clone, PartialEq)]
pub struct DevFee {
    /// 1 to 50: one template in `100 / percent` is the fee's.
    pub percent: u8,
    pub address: [u8; 20],
    pub address_hex: String,
    /// Where the address came from (the manifest's path), for the start line.
    pub source: String,
    /// The signed manifest's sha256 (its bytes as shipped): the pool's fee-waiver handshake names it (16.3).
    pub manifest_sha256: String,
}

/// The pool's answer to the dev-fee declaration in the subscribe (ED-08, the combined cap: every company charge
/// together at most 1 percent of the miner side, so the company's pool waives its fee for a session whose dev fee
/// is on): its fee for the session in basis points, whether it is waived, and its words.
#[derive(Debug, Clone, PartialEq, Default, Serialize, Deserialize)]
pub struct PoolFeeAnswer {
    pub bps: u32,
    pub waived: bool,
    #[serde(default)]
    pub reason: String,
}

impl PoolFeeAnswer {
    /// From the subscribe result's `pool_fee` object; None when the pool answered nothing.
    pub fn from_result(r: &serde_json::Value) -> Option<PoolFeeAnswer> {
        let p = r.get("pool_fee")?;
        let bps = p.get("bps").and_then(|b| b.as_u64().or_else(|| b.as_str().and_then(|s| s.parse().ok()))).unwrap_or(0) as u32;
        let waived = p.get("waived").and_then(|w| w.as_bool()).unwrap_or(false);
        let reason = p.get("reason").and_then(|w| w.as_str()).unwrap_or("").to_string();
        Some(PoolFeeAnswer { bps, waived, reason })
    }
}

/// The two fees in one line, the first lines' and the Earnings row's: "Dev fee 1 percent; pool fee 0 (waived)",
/// "Dev fee off; pool fee 100 bps", or "pool fee: not declared" for a pool that answered nothing; a node session
/// (no pool) names the dev fee alone.
pub fn fee_words(dev_percent: u8, pool: Option<&PoolFeeAnswer>, on_a_pool: bool) -> String {
    let dev = if dev_percent > 0 { format!("Dev fee {dev_percent} percent") } else { "Dev fee off".to_string() };
    if !on_a_pool {
        return dev;
    }
    match pool {
        Some(p) if p.waived => format!("{dev}; pool fee 0 (waived{})", if p.reason.is_empty() { String::new() } else { format!(": {}", p.reason) }),
        Some(p) => format!("{dev}; pool fee {} bps{}", p.bps, if p.reason.is_empty() { String::new() } else { format!(" ({})", p.reason) }),
        None => format!("{dev}; pool fee: not declared"),
    }
}

/// The bytes the dev-fee declaration signs (ecosystem's wire form of 11 October 2026, plain ed25519 by the session
/// key, no pre-hash): "igneum-dev-fee\n<percent>\n<address>\n<manifest_sha256>\n<worker>\n".
pub fn declaration_bytes(percent: u8, address_hex: &str, manifest_sha256: &str, worker: &str) -> Vec<u8> {
    format!("igneum-dev-fee\n{percent}\n{address_hex}\n{manifest_sha256}\n{worker}\n").into_bytes()
}

/// A pool receipt's v1 digest (ecosystem's form): BLAKE3("IgneumPool/receipt/v1" || u64be(seq) || share_hash || key
/// || u64be(weight) || u64be(total) || prev || u32be(dev_fee_percent) || u32be(pool_fee_bps)).
pub fn receipt_v1_digest(seq: u64, share_hash: &[u8], key: &[u8], weight: u64, total: u64, prev: &[u8], dev_fee_percent: u32, pool_fee_bps: u32) -> [u8; 32] {
    receipt_digest(1, seq, share_hash, key, weight, total, prev, dev_fee_percent, pool_fee_bps, None)
}

/// A pool receipt's v2 digest (ecosystem's form of 12 October 2026, the company pool since its 00:33 restart): the
/// v1 bytes under the label "IgneumPool/receipt/v2" with u64be(extra_nonce) || u64be(height) || parent appended.
pub fn receipt_v2_digest(seq: u64, share_hash: &[u8], key: &[u8], weight: u64, total: u64, prev: &[u8], dev_fee_percent: u32, pool_fee_bps: u32, extra_nonce: u64, height: u64, parent: &[u8]) -> [u8; 32] {
    receipt_digest(2, seq, share_hash, key, weight, total, prev, dev_fee_percent, pool_fee_bps, Some((extra_nonce, height, parent)))
}

fn receipt_digest(version: u8, seq: u64, share_hash: &[u8], key: &[u8], weight: u64, total: u64, prev: &[u8], dev_fee_percent: u32, pool_fee_bps: u32, v2: Option<(u64, u64, &[u8])>) -> [u8; 32] {
    let mut h = blake3::Hasher::new();
    h.update(format!("IgneumPool/receipt/v{version}").as_bytes());
    h.update(&seq.to_be_bytes());
    h.update(share_hash);
    h.update(key);
    h.update(&weight.to_be_bytes());
    h.update(&total.to_be_bytes());
    h.update(prev);
    h.update(&dev_fee_percent.to_be_bytes());
    h.update(&pool_fee_bps.to_be_bytes());
    if let Some((extra_nonce, height, parent)) = v2 {
        h.update(&extra_nonce.to_be_bytes());
        h.update(&height.to_be_bytes());
        h.update(parent);
    }
    *h.finalize().as_bytes()
}

/// A receipt as the pool sends it (`mining.receipt`), the fee fields read and the digest checked when the receipt
/// says version 1 or 2: Ok(true) verified, Ok(false) a receipt of another version (no check), Err the digest or the
/// fields do not stand.
pub fn check_receipt(r: &serde_json::Value) -> Result<(bool, u32, u32), String> {
    let g = |k: &str| r.get(k);
    let num = |k: &str| g(k).and_then(|v| v.as_u64().or_else(|| v.as_str().and_then(|s| s.parse().ok())));
    let dev = num("dev_fee_percent").unwrap_or(0) as u32;
    let bps = num("pool_fee_bps").unwrap_or(0) as u32;
    let version = num("version").unwrap_or(0);
    if version != 1 && version != 2 {
        return Ok((false, dev, bps));
    }
    let hexf = |k: &str| g(k).and_then(|v| v.as_str()).and_then(|s| hex_decode(s.trim_start_matches("0x"))).ok_or_else(|| format!("receipt: {k} is not hex"));
    let seq = num("seq").ok_or("receipt: no seq")?;
    let weight = num("weight").ok_or("receipt: no weight")?;
    let total = num("total").ok_or("receipt: no total")?;
    let share_hash = hexf("share_hash")?;
    let key = hexf("key")?;
    let prev = hexf("prev")?;
    let digest = hexf("digest")?;
    let want = if version == 2 {
        let extra_nonce = num("extra_nonce").ok_or("receipt: no extra_nonce")?;
        let height = num("height").ok_or("receipt: no height")?;
        let parent = hexf("parent")?;
        receipt_v2_digest(seq, &share_hash, &key, weight, total, &prev, dev, bps, extra_nonce, height, &parent)
    } else {
        receipt_v1_digest(seq, &share_hash, &key, weight, total, &prev, dev, bps)
    };
    if digest.as_slice() != want {
        return Err(format!("receipt: the v{version} digest does not stand"));
    }
    Ok((true, dev, bps))
}

/// Why the fee is off, in words for the start line and the app.
#[derive(Debug, Clone, PartialEq)]
pub enum Off {
    /// `--dev-fee 0` (or the app's switch off).
    ByOperator,
    /// No signed manifest names an address: the file, its signature or the key is missing or wrong.
    NoSignedAddress(String),
}

/// The templates between two fee templates: 100 for 1 percent, 50 for 2, 20 for 5, 10 for 10. 0 means off; a
/// percent over 50 is capped at 50 (every second template; nothing takes more).
/// The pool's share step on the minute line (ecosystem's mining.notify field, 12 October 2026): " step S (weight
/// 2^S on the chain)" above zero, nothing for the count form or a node session.
pub fn share_step_words(step: u8) -> String {
    if step == 0 { String::new() } else { format!(" step {step} (weight {} on the chain)", 1u64 << step.min(62)) }
}

/// One line of the dev fee's record file (economics' ask of 12 October 2026): `event` is "template" (a fee
/// template issued on a node session: the job id, its height) or "share" (a share accepted on a fee template: the
/// nonce and the outcome); the percent and the address are the configuration's at the time. JSON, one line.
pub fn fee_record_line(event: &str, job_id: &str, height: u64, nonce: Option<u64>, outcome: Option<&str>, percent: u8, address_hex: &str) -> String {
    let at = std::time::SystemTime::now().duration_since(std::time::UNIX_EPOCH).map(|d| d.as_secs()).unwrap_or(0);
    let mut v = serde_json::json!({ "at_unix": at, "event": event, "job_id": job_id, "fee_percent": percent, "address": address_hex });
    if height > 0 {
        v["height"] = serde_json::json!(height);
    }
    if let Some(n) = nonce {
        v["nonce"] = serde_json::json!(n.to_string());
    }
    if let Some(o) = outcome {
        v["outcome"] = serde_json::json!(o);
    }
    v.to_string()
}

pub fn every_nth(percent: u8) -> Option<u64> {
    if percent == 0 {
        return None;
    }
    Some((100 / u64::from(percent.min(50))).max(2))
}

/// Whether the template numbered `counter` (0 for the session's first) is the fee's: templates 99, 199, ... at
/// 1 percent (the economics form), so the miner's own address takes the first 99 of every hundred.
pub fn is_fee_template(counter: u64, percent: u8) -> bool {
    match every_nth(percent) {
        Some(n) => counter % n == n - 1,
        None => false,
    }
}

/// Read and verify the signed manifest beside the workers (the release directory). Ok(None) when no manifest is
/// there at all; Err when one is there but does not verify or parse (the fee is off either way; the words differ).
pub fn read_manifest(dir: &Path) -> Result<Option<(DevFeeManifest, PathBuf)>, String> {
    let m = dir.join(MANIFEST_NAME);
    if !m.exists() {
        return Ok(None);
    }
    let bytes = std::fs::read(&m).map_err(|e| format!("{}: {e}", m.display()))?;
    let sig = std::fs::read_to_string(dir.join(SIGNATURE_NAME)).map_err(|_| format!("{} is there but {} is not: the address is unsigned", MANIFEST_NAME, SIGNATURE_NAME))?;
    verify_sshsig(&bytes, &sig, RELEASE_PUBLIC_KEY_HEX, RELEASE_SIG_NAMESPACE).map_err(|e| format!("{MANIFEST_NAME}: {e}"))?;
    let parsed: DevFeeManifest = serde_json::from_slice(&bytes).map_err(|e| format!("{MANIFEST_NAME}: not the manifest's form: {e}"))?;
    if parsed.component != "ember" {
        return Err(format!("{MANIFEST_NAME}: for {:?}, not ember", parsed.component));
    }
    parse_address(&parsed.dev_payout_address).ok_or_else(|| format!("{MANIFEST_NAME}: dev_payout_address is not 0x and 40 hex"))?;
    Ok(Some((parsed, m)))
}

/// The fee a session runs with: the operator's percent (None: the manifest's default) against the signed manifest
/// in `dir`. Fail closed: no verified address, no fee.
pub fn resolve(dir: &Path, operator_percent: Option<u8>) -> Result<DevFee, Off> {
    if operator_percent == Some(0) {
        return Err(Off::ByOperator);
    }
    let (m, path) = match read_manifest(dir) {
        Ok(Some(x)) => x,
        Ok(None) => return Err(Off::NoSignedAddress(format!("no {MANIFEST_NAME} beside the workers in {}", dir.display()))),
        Err(e) => return Err(Off::NoSignedAddress(e)),
    };
    let percent = operator_percent.unwrap_or(m.default_percent);
    if percent == 0 {
        return Err(Off::ByOperator);
    }
    let address = parse_address(&m.dev_payout_address).ok_or_else(|| Off::NoSignedAddress("the manifest's address is not 20 bytes".into()))?;
    let manifest_sha256 = std::fs::read(&path).map(|b| hex_encode(&Sha256::digest(&b))).unwrap_or_default();
    Ok(DevFee { percent: percent.min(50), address, address_hex: m.dev_payout_address.clone(), source: path.display().to_string(), manifest_sha256 })
}

pub fn hex_encode(b: &[u8]) -> String {
    b.iter().map(|x| format!("{x:02x}")).collect()
}

/// The words for the start line and the app: on, with the rate and the form, or off with the reason.
pub fn words(r: &Result<DevFee, Off>) -> String {
    match r {
        Ok(f) => {
            let n = every_nth(f.percent).unwrap_or(100);
            format!("dev fee {} percent: one template in {n} is requested with the dev payout address {} instead of yours (from the signed {}); --dev-fee 0 turns it off", f.percent, f.address_hex, MANIFEST_NAME)
        }
        Err(Off::ByOperator) => "dev fee off (--dev-fee 0)".into(),
        Err(Off::NoSignedAddress(why)) => format!("dev fee off: no signed release manifest names the dev payout address ({why})"),
    }
}

pub fn parse_address(s: &str) -> Option<[u8; 20]> {
    let h = s.strip_prefix("0x").or_else(|| s.strip_prefix("0X"))?;
    if h.len() != 40 {
        return None;
    }
    let v = hex_decode(h)?;
    v.try_into().ok()
}

pub fn hex_decode(s: &str) -> Option<Vec<u8>> {
    let s = s.trim();
    if s.len() % 2 != 0 {
        return None;
    }
    (0..s.len()).step_by(2).map(|i| u8::from_str_radix(&s[i..i + 2], 16).ok()).collect()
}

fn public_key(pub_hex: &str) -> Result<VerifyingKey, String> {
    let raw = hex_decode(pub_hex).ok_or("public key is not hex")?;
    let raw: [u8; 32] = raw.try_into().map_err(|_| "public key is not 32 bytes")?;
    VerifyingKey::from_bytes(&raw).map_err(|_| "public key is not a valid Ed25519 point".to_string())
}

/// The SSH signature format (PROTOCOL.sshsig; the same reader as the app's updater): the armour holds base64 of
/// `"SSHSIG" || u32 1 || string publickey || string namespace || string reserved || string hash_algorithm ||
/// string signature`; the bytes signed are `"SSHSIG" || string namespace || string reserved || string
/// hash_algorithm || string H(message)`. The key inside must be the pinned one and the namespace the release's.
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
    let reserved = p.string()?;
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
    if key_raw != pinned.as_slice() {
        return Err("SSH signature: the key inside is not the release key".into());
    }
    let key = public_key(pub_hex)?;
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
    let signed = sshsig_signed_bytes(ns, reserved, hash_alg, &digest);
    key.verify(&signed, &Signature::from_bytes(&sig)).map_err(|_| "the signature does not verify".to_string())
}

fn sshsig_signed_bytes(ns: &[u8], reserved: &[u8], hash_alg: &[u8], digest: &[u8]) -> Vec<u8> {
    let mut signed = Vec::with_capacity(6 + 4 * 4 + ns.len() + hash_alg.len() + digest.len());
    signed.extend_from_slice(b"SSHSIG");
    ssh_string(&mut signed, ns);
    ssh_string(&mut signed, reserved);
    ssh_string(&mut signed, hash_alg);
    ssh_string(&mut signed, digest);
    signed
}

fn ssh_string(out: &mut Vec<u8>, s: &[u8]) {
    out.extend_from_slice(&(s.len() as u32).to_be_bytes());
    out.extend_from_slice(s);
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

fn base64_decode(s: &str) -> Option<Vec<u8>> {
    const T: &[u8; 64] = b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
    let mut out = Vec::with_capacity(s.len() * 3 / 4);
    let mut acc: u32 = 0;
    let mut bits = 0;
    for c in s.bytes() {
        if c == b'=' {
            break;
        }
        let v = T.iter().position(|&t| t == c)? as u32;
        acc = (acc << 6) | v;
        bits += 6;
        if bits >= 8 {
            bits -= 8;
            out.push((acc >> bits) as u8);
            acc &= (1 << bits) - 1;
        }
    }
    Some(out)
}

#[cfg(test)]
pub(crate) mod tests {
    use super::*;
    use ed25519_dalek::{Signer, SigningKey};

    fn base64_encode(b: &[u8]) -> String {
        const T: &[u8; 64] = b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
        let mut s = String::new();
        for chunk in b.chunks(3) {
            let n = chunk.len();
            let v = (u32::from(chunk[0]) << 16) | (u32::from(*chunk.get(1).unwrap_or(&0)) << 8) | u32::from(*chunk.get(2).unwrap_or(&0));
            s.push(T[(v >> 18) as usize & 63] as char);
            s.push(T[(v >> 12) as usize & 63] as char);
            s.push(if n > 1 { T[(v >> 6) as usize & 63] as char } else { '=' });
            s.push(if n > 2 { T[v as usize & 63] as char } else { '=' });
        }
        s
    }

    /// An armoured SSHSIG over `message` by `key` under `namespace`, as ssh-keygen -Y sign writes it (sha512).
    pub(crate) fn sign_sshsig(key: &SigningKey, message: &[u8], namespace: &str) -> String {
        let digest = sha2::Sha512::digest(message).to_vec();
        let signed = sshsig_signed_bytes(namespace.as_bytes(), b"", b"sha512", &digest);
        let sig = key.sign(&signed).to_bytes();
        let mut pubkey = Vec::new();
        ssh_string(&mut pubkey, b"ssh-ed25519");
        ssh_string(&mut pubkey, key.verifying_key().as_bytes());
        let mut sig_blob = Vec::new();
        ssh_string(&mut sig_blob, b"ssh-ed25519");
        ssh_string(&mut sig_blob, &sig);
        let mut blob = Vec::new();
        blob.extend_from_slice(b"SSHSIG");
        blob.extend_from_slice(&1u32.to_be_bytes());
        ssh_string(&mut blob, &pubkey);
        ssh_string(&mut blob, namespace.as_bytes());
        ssh_string(&mut blob, b"");
        ssh_string(&mut blob, b"sha512");
        ssh_string(&mut blob, &sig_blob);
        format!("-----BEGIN SSH SIGNATURE-----\n{}\n-----END SSH SIGNATURE-----\n", base64_encode(&blob))
    }

    fn hex(b: &[u8]) -> String {
        b.iter().map(|x| format!("{x:02x}")).collect()
    }

    #[test]
    fn zero_sends_nothing_and_one_percent_is_exactly_one_template_in_a_hundred() {
        // the test the project asked for: a 0 setting sends nothing; 1 sends exactly one template in a hundred (templates
        // 99, 199, ...: the miner's address takes the first 99 of every hundred)
        assert_eq!((0..10_000u64).filter(|&c| is_fee_template(c, 0)).count(), 0);
        let fee: Vec<u64> = (0..1_000u64).filter(|&c| is_fee_template(c, 1)).collect();
        assert_eq!(fee.len(), 10);
        assert_eq!(fee, vec![99, 199, 299, 399, 499, 599, 699, 799, 899, 999]);
        for start in [0u64, 1, 37, 99, 100, 101] {
            assert_eq!((start..start + 100).filter(|&c| is_fee_template(c, 1)).count(), 1, "one in any hundred from {start}");
        }
        assert_eq!(every_nth(0), None);
        assert_eq!(every_nth(1), Some(100));
        assert_eq!(every_nth(2), Some(50));
        assert_eq!(every_nth(50), Some(2));
        assert_eq!(every_nth(90), Some(2));
    }

    #[test]
    fn the_address_comes_from_the_signed_manifest_or_the_fee_is_off() {
        let dir = std::env::temp_dir().join(format!("ember-devfee-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&dir);
        std::fs::create_dir_all(&dir).unwrap();
        // no manifest: off, fail closed, even with the default percent
        assert!(matches!(resolve(&dir, None), Err(Off::NoSignedAddress(_))));
        assert!(matches!(resolve(&dir, Some(1)), Err(Off::NoSignedAddress(_))));
        // the operator's 0 wins over everything
        assert_eq!(resolve(&dir, Some(0)), Err(Off::ByOperator));
        // a manifest without its signature: off, and the words say unsigned
        let body = br#"{"component":"ember","dev_payout_address":"0x7F45d7d7272e57639BeBb739A60B05bB2CD4C126","default_percent":1,"note":"test"}"#;
        std::fs::write(dir.join(MANIFEST_NAME), body).unwrap();
        match resolve(&dir, None) {
            Err(Off::NoSignedAddress(w)) => assert!(w.contains("unsigned"), "{w}"),
            other => panic!("{other:?}"),
        }
        // signed by another key: off
        let other = SigningKey::from_bytes(&[7u8; 32]);
        std::fs::write(dir.join(SIGNATURE_NAME), sign_sshsig(&other, body, RELEASE_SIG_NAMESPACE)).unwrap();
        assert!(matches!(resolve(&dir, None), Err(Off::NoSignedAddress(_))));
        // signed by the pinned key (the verifier takes the key as a parameter; the constant is the founder's):
        // the full path checked through verify_sshsig with a test key, then the resolve with that key's manifest
        let key = SigningKey::from_bytes(&[3u8; 32]);
        let armoured = sign_sshsig(&key, body, RELEASE_SIG_NAMESPACE);
        let pub_hex = hex(key.verifying_key().as_bytes());
        assert_eq!(verify_sshsig(body, &armoured, &pub_hex, RELEASE_SIG_NAMESPACE), Ok(()));
        assert!(verify_sshsig(body, &armoured, &pub_hex, "other-namespace").is_err());
        assert!(verify_sshsig(b"tampered", &armoured, &pub_hex, RELEASE_SIG_NAMESPACE).is_err());
        assert!(verify_sshsig(body, &armoured, RELEASE_PUBLIC_KEY_HEX, RELEASE_SIG_NAMESPACE).is_err());
        // the parsed manifest's address and percent
        let m: DevFeeManifest = serde_json::from_slice(body).unwrap();
        assert_eq!(parse_address(&m.dev_payout_address).unwrap()[0], 0x7f);
        assert_eq!(m.default_percent, 1);
        assert!(words(&Err(Off::ByOperator)).contains("--dev-fee 0"));
        assert!(words(&Err(Off::NoSignedAddress("x".into()))).contains("no signed release manifest"));
        let on = DevFee { percent: 1, address: [0u8; 20], address_hex: m.dev_payout_address.clone(), source: "m".into(), manifest_sha256: String::new() };
        assert!(words(&Ok(on)).contains("one template in 100"));
        let _ = std::fs::remove_dir_all(&dir);
    }

    #[test]
    fn the_share_step_words() {
        assert_eq!(share_step_words(0), "");
        assert_eq!(share_step_words(2), " step 2 (weight 4 on the chain)");
        assert_eq!(share_step_words(5), " step 5 (weight 32 on the chain)");
    }

    #[test]
    fn the_fee_record_lines_carry_the_template_and_the_share() {
        // economics' ask (12 October 2026): the one in a hundred read from Ember's own count
        let t: serde_json::Value = serde_json::from_str(&fee_record_line("template", "j99", 1200, None, None, 1, "0x11")).unwrap();
        assert_eq!(t["event"], "template"); assert_eq!(t["job_id"], "j99"); assert_eq!(t["height"], 1200); assert_eq!(t["fee_percent"], 1); assert_eq!(t["address"], "0x11");
        assert!(t.get("nonce").is_none() && t.get("outcome").is_none()); assert!(t["at_unix"].as_u64().unwrap() > 1_700_000_000);
        let s: serde_json::Value = serde_json::from_str(&fee_record_line("share", "j99", 0, Some(7), Some("accepted"), 1, "0x11")).unwrap();
        assert_eq!(s["event"], "share"); assert_eq!(s["nonce"], "7"); assert_eq!(s["outcome"], "accepted"); assert!(s.get("height").is_none());
    }

    #[test]
    fn the_pool_handshake_words_bytes_and_receipt_digest() {
        // ED-08 (the project's order of 11 October 2026): the two fees on one line
        let waived = PoolFeeAnswer { bps: 0, waived: true, reason: "dev fee 1 percent declared".into() };
        let declared = PoolFeeAnswer { bps: 100, waived: false, reason: String::new() };
        assert_eq!(fee_words(1, Some(&waived), true), "Dev fee 1 percent; pool fee 0 (waived: dev fee 1 percent declared)");
        assert_eq!(fee_words(0, Some(&declared), true), "Dev fee off; pool fee 100 bps");
        assert_eq!(fee_words(1, None, true), "Dev fee 1 percent; pool fee: not declared");
        assert_eq!(fee_words(1, None, false), "Dev fee 1 percent");
        let void = PoolFeeAnswer { bps: 100, waived: false, reason: "dev fee not verified: key mismatch".into() };
        assert_eq!(fee_words(1, Some(&void), true), "Dev fee 1 percent; pool fee 100 bps (dev fee not verified: key mismatch)");
        // the declaration's bytes, exactly ecosystem's form
        assert_eq!(declaration_bytes(1, "0xF4b0", "abcd", "rig-1"), b"igneum-dev-fee\n1\n0xF4b0\nabcd\nrig-1\n".to_vec());
        // the pool's answer parsed from the subscribe result, absent when not answered
        let r = serde_json::json!({"session_id": "ab", "pool_fee": {"bps": 0, "waived": true, "reason": "declared"}});
        assert_eq!(PoolFeeAnswer::from_result(&r), Some(PoolFeeAnswer { bps: 0, waived: true, reason: "declared".into() }));
        assert_eq!(PoolFeeAnswer::from_result(&serde_json::json!({"session_id": "ab"})), None);
        // a v1 receipt verifies against the digest the pool computed by the same formula; a tampered field fails;
        // a v0 receipt passes without a check
        let share = [0x11u8; 32];
        let key = [0x22u8; 32];
        let prev = [0u8; 32];
        let d = receipt_v1_digest(7, &share, &key, 3, 21, &prev, 1, 0);
        let rc = serde_json::json!({"version": 1, "seq": 7, "share_hash": hex_encode(&share), "key": hex_encode(&key), "weight": 3, "total": 21, "prev": hex_encode(&prev), "digest": hex_encode(&d), "dev_fee_percent": 1, "pool_fee_bps": 0});
        assert_eq!(check_receipt(&rc), Ok((true, 1, 0)));
        let mut bad = rc.clone();
        bad["pool_fee_bps"] = serde_json::json!(100);
        assert!(check_receipt(&bad).is_err());
        let v0 = serde_json::json!({"seq": 7, "weight": 3, "total": 21, "dev_fee_percent": 1, "pool_fee_bps": 0});
        assert_eq!(check_receipt(&v0), Ok((false, 1, 0)));
        // a v2 receipt exactly as the company pool sent it on 12 October 2026 (ecosystem's sample, 00:55 UK)
        let v2: serde_json::Value = serde_json::from_str(r#"{"seq":2,"share_hash":"fb6ff7fc204bea5d0a19c0c612c31015179268e68844522e3a02380e3fe719f0","key":"a8b367bdf1276098efe32d4b03d4123cba44094abf69cfda9c886485fa262eb5","weight":50,"total":100,"digest":"0dc3b8f29a236de7ce9f833427c94c88d598ff9ee061d1b015b9c4651e20e759","prev":"a15de48af9cc83ffec1c4688d2d3a29fd4e58a9baf1b731218400a2c7a481ee1","pool_key":"0c22e2f79df3ab08bcc0f9e071a7f339a5e4c6440d88ed256f12fe4b4c0c9c4b","sig":"d1688e22d171dd995abf3f79a3e90202c57441d59d03bc6f98c6a33cbf27048c036a15042bfa3c82949c942b66ced741bdffc89a0fc4a88ffbec32f66b3bd502","job_id":"3182a0ceb971755d","nonce":1032589693637623860,"version":2,"dev_fee_percent":1,"pool_fee_bps":0,"extra_nonce":240418523,"height":140,"parent":"6f8977fb6aadd0e0aee81dad25125e77382bd08044b309cecbad27b46e62b964"}"#).unwrap();
        assert_eq!(check_receipt(&v2), Ok((true, 1, 0)));
        let mut bad2 = v2.clone();
        bad2["height"] = serde_json::json!(141);
        assert!(check_receipt(&bad2).is_err());
        let v3 = serde_json::json!({"version": 3, "seq": 1, "dev_fee_percent": 1, "pool_fee_bps": 0});
        assert_eq!(check_receipt(&v3), Ok((false, 1, 0)));
    }
}
