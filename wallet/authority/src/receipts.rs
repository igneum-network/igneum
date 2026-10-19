//! The pool's signed receipt per share (docs/ecosystem/DESIGN.md 2.17 "The signed receipt per share";
//! `pool/internal/stratum/server.go` `Receipt`, `signed_igneumhash.go` `Ed25519Receipts.Sign`): the message is
//! BLAKE3("IgneumPool/receipt/v0" || BE64(seq) || share hash || member key || BE64(weight) || BE64(total) || the
//! previous digest), the signature Ed25519 under the pool's receipt key (the subscribe result's `receipt_key`),
//! and the digest the next receipt chains on is that message. A member keeps every receipt as its proof of credit:
//! a changed weight, a dropped share or a total that does not match the member's own count is detectable from the
//! receipts alone. This module is that check, the wallet's view of a pool member's standing.

use serde::{Deserialize, Serialize};

use ed25519_dalek::{Signature, Verifier, VerifyingKey};
use igneum_wallet_core::{hex, Error, Result};

pub const DOMAIN: &[u8] = b"IgneumPool/receipt/v0";

/// One `mining.receipt` notification's params, as the pool sends them (hex without 0x, the pool's form).
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct Receipt {
    pub seq: u64,
    pub share_hash: String,
    pub key: String,
    pub weight: u64,
    pub total: u64,
    pub digest: String,
    #[serde(default)]
    pub prev: String,
    pub pool_key: String,
    pub sig: String,
    #[serde(default)]
    pub job_id: String,
    #[serde(default)]
    pub nonce: u64,
}

/// The verdict on one receipt.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct Verdict {
    pub seq: u64,
    pub ok: bool,
    /// empty when ok; else the first reason in the order the checks run
    pub reason: String,
    pub weight: u64,
    pub total: u64,
    pub share_hash: String,
}

/// The member's standing from its receipts.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct Report {
    pub pool_key: String,
    pub member_key: String,
    pub receipts: usize,
    pub verified: usize,
    pub bad: usize,
    /// the sum of the weights of the verified receipts, the member's own count
    pub weight_counted: u64,
    /// the last verified receipt's running total, the pool's count; equal to weight_counted when every receipt is
    /// present and unaltered
    pub pool_total: u64,
    /// the last verified receipt's digest, the point the next receipt must chain on
    pub last_digest: String,
    pub verdicts: Vec<Verdict>,
}

fn bytes<const N: usize>(field: &str, s: &str) -> Result<[u8; N]> {
    let b = hex::decode(s).map_err(|e| Error::Input(format!("{field}: {e}")))?;
    if b.len() != N {
        return Err(Error::Input(format!("{field} has {} bytes, not {N}", b.len())));
    }
    let mut out = [0u8; N];
    out.copy_from_slice(&b);
    Ok(out)
}

/// The receipt's message: what the pool signed and what the next receipt chains on.
pub fn message(seq: u64, share_hash: &[u8; 32], key: &[u8; 32], weight: u64, total: u64, prev: &[u8; 32]) -> [u8; 32] {
    let mut h = blake3::Hasher::new();
    h.update(DOMAIN);
    h.update(&seq.to_be_bytes());
    h.update(share_hash);
    h.update(key);
    h.update(&weight.to_be_bytes());
    h.update(&total.to_be_bytes());
    h.update(prev);
    *h.finalize().as_bytes()
}

/// Verifies a member's receipts in order under the pool's receipt key: the field shapes, the chain (each receipt's
/// `prev` is the member's last verified digest, zero at the first), the digest, the signature, the key, and the
/// running total against the member's own count. A bad receipt does not advance the chain, so one altered receipt
/// makes every later one unlinked too, which is the point: the member sees exactly where the pool's record and its
/// own part ways.
pub fn verify(receipts: &[Receipt], pool_key_hex: &str, member_key_hex: &str) -> Result<Report> {
    let pool_key: [u8; 32] = bytes("pool key", pool_key_hex)?;
    let member_key: [u8; 32] = bytes("member key", member_key_hex)?;
    let vk = VerifyingKey::from_bytes(&pool_key).map_err(|_| Error::Input("the pool key is not an Ed25519 point".into()))?;
    let mut last = [0u8; 32];
    let mut counted: u64 = 0;
    let mut pool_total: u64 = 0;
    let mut verdicts = Vec::with_capacity(receipts.len());
    let mut verified = 0usize;
    for r in receipts {
        let mut v = Verdict { seq: r.seq, ok: false, reason: String::new(), weight: r.weight, total: r.total, share_hash: r.share_hash.clone() };
        let check = (|| -> std::result::Result<(), String> {
            let sh: [u8; 32] = bytes("share_hash", &r.share_hash).map_err(|e| e.to_string())?;
            let key: [u8; 32] = bytes("key", &r.key).map_err(|e| e.to_string())?;
            let sig: [u8; 64] = bytes("sig", &r.sig).map_err(|e| e.to_string())?;
            let digest: [u8; 32] = bytes("digest", &r.digest).map_err(|e| e.to_string())?;
            let prev: [u8; 32] = if r.prev.is_empty() { [0u8; 32] } else { bytes("prev", &r.prev).map_err(|e| e.to_string())? };
            if key != member_key {
                return Err("the receipt names another member's key".into());
            }
            if !r.pool_key.is_empty() && bytes::<32>("pool_key", &r.pool_key).map_err(|e| e.to_string())? != pool_key {
                return Err("the receipt names another pool key".into());
            }
            if prev != last {
                return Err(format!("not linked: prev {} is not the last verified digest {}", r.prev.chars().take(16).collect::<String>(), hex::encode(&last[..8])));
            }
            let msg = message(r.seq, &sh, &key, r.weight, r.total, &prev);
            if msg != digest {
                return Err("the digest is not the message of the fields".into());
            }
            if vk.verify(&msg, &Signature::from_bytes(&sig)).is_err() {
                return Err("the signature does not verify under the pool key".into());
            }
            let expected_total = counted.checked_add(r.weight).ok_or("the total overflows")?;
            if r.total != expected_total {
                return Err(format!("the running total {} is not the member's own count {}", r.total, expected_total));
            }
            Ok(())
        })();
        match check {
            Ok(()) => {
                v.ok = true;
                verified += 1;
                counted = counted.saturating_add(r.weight);
                pool_total = r.total;
                last = bytes("digest", &r.digest)?;
            }
            Err(reason) => v.reason = reason,
        }
        verdicts.push(v);
    }
    Ok(Report {
        pool_key: pool_key_hex.trim_start_matches("0x").to_string(),
        member_key: member_key_hex.trim_start_matches("0x").to_string(),
        receipts: receipts.len(),
        verified,
        bad: receipts.len() - verified,
        weight_counted: counted,
        pool_total,
        last_digest: hex::encode(&last).trim_start_matches("0x").to_string(),
        verdicts,
    })
}

/// Reads receipts from JSON lines (one `mining.receipt` params object per line, or a whole notification whose
/// `params` is the object), skipping blank lines.
pub fn read_lines(text: &str) -> Result<Vec<Receipt>> {
    let mut out = Vec::new();
    for (i, line) in text.lines().enumerate() {
        let t = line.trim();
        if t.is_empty() {
            continue;
        }
        let v: serde_json::Value = serde_json::from_str(t).map_err(|e| Error::Input(format!("line {}: {e}", i + 1)))?;
        let obj = v.get("params").cloned().unwrap_or(v);
        let r: Receipt = serde_json::from_value(obj).map_err(|e| Error::Input(format!("line {}: {e}", i + 1)))?;
        out.push(r);
    }
    Ok(out)
}

#[cfg(test)]
mod tests {
    use super::*;
    use ed25519_dalek::{Signer, SigningKey};

    /// A pool, as the test's stand-in: signs receipts exactly as `Ed25519Receipts.Sign` does.
    struct Pool {
        sk: SigningKey,
        seq: u64,
        total: u64,
        last: [u8; 32],
    }

    impl Pool {
        fn receipt(&mut self, member: &[u8; 32], share: [u8; 32], weight: u64) -> Receipt {
            self.seq += 1;
            self.total += weight;
            let msg = message(self.seq, &share, member, weight, self.total, &self.last);
            let sig = self.sk.sign(&msg);
            let r = Receipt {
                seq: self.seq,
                share_hash: hex::encode(&share)[2..].to_string(),
                key: hex::encode(member)[2..].to_string(),
                weight,
                total: self.total,
                digest: hex::encode(&msg)[2..].to_string(),
                prev: hex::encode(&self.last)[2..].to_string(),
                pool_key: hex::encode(&self.sk.verifying_key().to_bytes())[2..].to_string(),
                sig: hex::encode(&sig.to_bytes())[2..].to_string(),
                job_id: "j1".into(),
                nonce: self.seq,
            };
            self.last = msg;
            r
        }
    }

    fn pool() -> Pool {
        Pool { sk: SigningKey::from_bytes(&[42u8; 32]), seq: 0, total: 0, last: [0u8; 32] }
    }

    #[test]
    fn a_clean_chain_verifies_and_every_tamper_is_named() {
        let member = [7u8; 32];
        let mut p = pool();
        let pk = hex::encode(&p.sk.verifying_key().to_bytes())[2..].to_string();
        let mk = hex::encode(&member)[2..].to_string();
        let rs: Vec<Receipt> = (0..5).map(|i| p.receipt(&member, [i as u8; 32], 100)).collect();
        let rep = verify(&rs, &pk, &mk).unwrap();
        assert_eq!((rep.verified, rep.bad, rep.weight_counted, rep.pool_total), (5, 0, 500, 500));
        assert_eq!(rep.last_digest, rs[4].digest);

        // a weight changed after the fact: that receipt fails on the digest, and the rest are unlinked
        let mut t = rs.clone();
        t[2].weight = 1000;
        let rep = verify(&t, &pk, &mk).unwrap();
        assert_eq!((rep.verified, rep.bad), (2, 3));
        assert!(rep.verdicts[2].reason.contains("digest"), "{}", rep.verdicts[2].reason);
        assert!(rep.verdicts[3].reason.contains("not linked"), "{}", rep.verdicts[3].reason);

        // a dropped share: the next receipt's prev is not our last digest and the total jumps
        let mut d = rs.clone();
        d.remove(1);
        let rep = verify(&d, &pk, &mk).unwrap();
        assert_eq!(rep.verified, 1);
        assert!(rep.verdicts[1].reason.contains("not linked"));

        // a forged signature
        let mut f = rs.clone();
        f[0].sig = hex::encode(&[1u8; 64])[2..].to_string();
        let rep = verify(&f, &pk, &mk).unwrap();
        assert!(!rep.verdicts[0].ok && rep.verdicts[0].reason.contains("signature"));

        // another pool's key signing the same fields
        let other = hex::encode(&SigningKey::from_bytes(&[43u8; 32]).verifying_key().to_bytes())[2..].to_string();
        let rep = verify(&rs, &other, &mk).unwrap();
        assert_eq!(rep.verified, 0);
        assert!(rep.verdicts[0].reason.contains("another pool key"));

        // the receipts of another member are not ours
        let rep = verify(&rs, &pk, &hex::encode(&[8u8; 32])[2..]).unwrap();
        assert_eq!(rep.verified, 0);
        assert!(rep.verdicts[0].reason.contains("another member"));

        // the pool's total inflated (a credit we never earned): the total check names it
        let mut g = rs.clone();
        // re-sign receipt 0 with total 150 and weight 100 under the real pool key
        let share: [u8; 32] = bytes("s", &g[0].share_hash).unwrap();
        let msg = message(1, &share, &member, 100, 150, &[0u8; 32]);
        g[0].total = 150;
        g[0].digest = hex::encode(&msg)[2..].to_string();
        g[0].sig = hex::encode(&p.sk.sign(&msg).to_bytes())[2..].to_string();
        let rep = verify(&g, &pk, &mk).unwrap();
        assert!(rep.verdicts[0].reason.contains("running total"), "{}", rep.verdicts[0].reason);
    }

    #[test]
    fn reads_json_lines_in_both_shapes() {
        let member = [7u8; 32];
        let mut p = pool();
        let r = p.receipt(&member, [1u8; 32], 5);
        let plain = serde_json::to_string(&r).unwrap();
        let wrapped = format!("{{\"jsonrpc\":\"2.0\",\"method\":\"mining.receipt\",\"params\":{plain}}}");
        let rs = read_lines(&format!("{plain}\n\n{wrapped}\n")).unwrap();
        assert_eq!(rs.len(), 2);
        assert_eq!(rs[0], rs[1]);
        assert!(read_lines("{\"seq\": \"x\"}").is_err());
    }
}
