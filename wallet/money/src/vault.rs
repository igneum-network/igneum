//! The vault (Project Money stage two, cut one; docs/money/VAULT-SPEC-2026-10-18.md as re-cut by the review,
//! docs/money/STAGE2-CUT1-VAULT-2026-10-18.md): the objects, their hashes, the vault's keys, the vault notes'
//! sealing to the vault's own ML-KEM-768 key, the scan of a block stream for a vault, and the seven statements'
//! witnesses and records in the prover's form (tools/money/stage1-prover/prover/src/vault.rs, the one source of
//! the hash definitions; the vectors in tools/money/vectors/money-vault-vectors.json warrant this copy).
//!
//! The objects (every H the two-to-one Poseidon2 merge; a fold is H(...H(H(TAG, w0), w1)..., wn)):
//!
//! ```text
//! policy_hash = fold(TAG 11; pk_e, pk_c, pk_a, nk_field, [D_b, D_s, P, 0], value(L), [R_b, R_s, 0, 0], nonce)
//! cm_head     = fold(TAG 12; value(v), vid, policy_hash, [period_idx, rec_h, rec_t, 0], value(spent), rec_hash, r)
//! cm_pending  = fold(TAG 13; value(w), vid, policy_hash, pk_dest, [h_req, t_req, D_b, D_s], r)
//! cm_inbound  = fold(TAG 14; value(w), vid, policy_hash, r)
//! ptag        = fold(TAG 15; cm_pending, [position, 0, 0, 0], r)
//! pk_c = H(sk_c, TAG 16)   pk_a = H(sk_a, TAG 17)   nk_field = H(nk_v, TAG 18)   nk_c = H(sk_c, TAG 19)
//! ```
//!
//! The vault's keys from the everyday seed s and the vault id: nk_v = B("Igneum Money vault nullifier", s || vid)
//! reduced to a word; the vault's KEM pair from B_64("Igneum Money vault kem", s || vid). The cancellation key
//! and the recovery key are their own 32-byte secrets (`keygen_secret`), never derived from s. The vault file is
//! the vault's viewing key and state: vid, the policy's fields, nk_v, the KEM seed, the heads, the pending and
//! inbound notes seen; it holds no spending secret, so a copy given to the cancellation key's holder reads and
//! cannot spend. A vault note on the chain is one output of the one shape (1,325 bytes) sealed to the vault's
//! KEM key with a plaintext version of its own (3 the head, 4 the pending note, 5 the inbound note).

use chacha20poly1305::aead::{Aead, KeyInit, Payload};
use chacha20poly1305::{ChaCha20Poly1305, Key, Nonce};
use libcrux_ml_kem::mlkem768::{self, MlKem768Ciphertext, MlKem768KeyPair};
use rand_core::{OsRng, RngCore};
use serde::{Deserialize, Serialize};
use serde_json::{json, Value};
use zeroize::Zeroizing;

use crate::hash::{b_derive, b_derive_n, merge, reduce_word, value_word, word_from_bytes, word_to_bytes, CircuitHash, Word, H};
use crate::hex;
use crate::keys::{Address, Seed, Wallet};
use crate::note::Output;
use crate::scan::Block;
use crate::sizes;
use crate::{err, Error, Result};

pub const TAG_POLICY: u64 = 11;
pub const TAG_HEAD: u64 = 12;
pub const TAG_PENDING: u64 = 13;
pub const TAG_INBOUND: u64 = 14;
pub const TAG_PTAG: u64 = 15;
pub const TAG_CANCEL: u64 = 16;
pub const TAG_RECOVER: u64 = 17;
pub const TAG_NK_FIELD: u64 = 18;
pub const TAG_NK_CANCEL: u64 = 19;

pub const FLOOR_DELAY_BLOCKS: u64 = 300;
pub const FLOOR_DELAY_SECONDS: u64 = 3600;
pub const REVOKED_DEPTH: usize = 16;

const CTX_VAULT_NULLIFIER: &str = "Igneum Money vault nullifier";
const CTX_VAULT_KEM: &str = "Igneum Money vault kem";
const CTX_VAULT_NOTE_KEY: &str = "Igneum Money vault note key";
const VERSION_HEAD: u8 = 3;
const VERSION_PENDING: u8 = 4;
const VERSION_INBOUND: u8 = 5;

pub type Bytes32 = [u8; 32];

fn w(b: &Bytes32) -> Word {
    word_from_bytes(b).unwrap_or_else(|| word_from_bytes(&reduce_word(b)).unwrap())
}

fn tag(t: u64) -> Word {
    [t, 0, 0, 0]
}

fn fold(words: &[Word]) -> Word {
    let mut acc = words[0];
    for x in &words[1..] {
        acc = merge(acc, *x);
    }
    acc
}

pub fn random_word() -> Bytes32 {
    let mut b = [0u8; 32];
    OsRng.fill_bytes(&mut b);
    reduce_word(&b)
}

/// A fresh 32-byte secret for a cancellation key or a recovery key (a word).
pub fn keygen_secret() -> Zeroizing<Bytes32> {
    Zeroizing::new(random_word())
}

pub fn pk_cancel(sk_c: &Bytes32) -> Bytes32 {
    word_to_bytes(merge(w(sk_c), tag(TAG_CANCEL)))
}

pub fn pk_recover(sk_a: &Bytes32) -> Bytes32 {
    word_to_bytes(merge(w(sk_a), tag(TAG_RECOVER)))
}

pub fn nk_field(nk_v: &Bytes32) -> Bytes32 {
    word_to_bytes(merge(w(nk_v), tag(TAG_NK_FIELD)))
}

pub fn nk_cancel(sk_c: &Bytes32) -> Bytes32 {
    word_to_bytes(merge(w(sk_c), tag(TAG_NK_CANCEL)))
}

/// The vault's nullifier key from the everyday seed and the vault id.
pub fn vault_nk(seed: &Seed, vid: &Bytes32) -> Bytes32 {
    let mut m = Zeroizing::new([0u8; 64]);
    m[..32].copy_from_slice(seed.as_bytes());
    m[32..].copy_from_slice(vid);
    reduce_word(&b_derive(CTX_VAULT_NULLIFIER, m.as_ref()))
}

/// The vault's KEM seed from the everyday seed and the vault id.
pub fn vault_kem_seed(seed: &Seed, vid: &Bytes32) -> Zeroizing<[u8; 64]> {
    let mut m = Zeroizing::new([0u8; 64]);
    m[..32].copy_from_slice(seed.as_bytes());
    m[32..].copy_from_slice(vid);
    Zeroizing::new(b_derive_n::<64>(CTX_VAULT_KEM, m.as_ref()))
}

/// The policy's fields.
#[derive(Serialize, Deserialize, Clone, Debug, PartialEq, Eq)]
pub struct Policy {
    pub pk_e: String,
    pub pk_c: String,
    pub pk_a: String,
    pub nk_field: String,
    pub delay_blocks: u64,
    pub delay_seconds: u64,
    pub period: u64,
    pub limit: String,
    pub recovery_blocks: u64,
    pub recovery_seconds: u64,
    pub nonce: String,
}

impl Policy {
    fn words(&self) -> Result<[Word; 8]> {
        Ok([
            w(&hex::decode_array(&self.pk_e, "pk_e")?),
            w(&hex::decode_array(&self.pk_c, "pk_c")?),
            w(&hex::decode_array(&self.pk_a, "pk_a")?),
            w(&hex::decode_array(&self.nk_field, "nk_field")?),
            [self.delay_blocks, self.delay_seconds, self.period, 0],
            value_word(self.limit.parse().map_err(|_| Error("policy: the limit".into()))?),
            [self.recovery_blocks, self.recovery_seconds, 0, 0],
            w(&hex::decode_array(&self.nonce, "nonce")?),
        ])
    }

    pub fn hash(&self) -> Result<Bytes32> {
        let mut ws = vec![tag(TAG_POLICY)];
        ws.extend(self.words()?);
        Ok(word_to_bytes(fold(&ws)))
    }

    pub fn limit(&self) -> u128 {
        self.limit.parse().unwrap_or(0)
    }

    pub fn check_floors(&self) -> Result<()> {
        if self.delay_blocks < FLOOR_DELAY_BLOCKS || self.delay_seconds < FLOOR_DELAY_SECONDS {
            return err(format!("the delay is below the floor ({FLOOR_DELAY_BLOCKS} blocks, {FLOOR_DELAY_SECONDS} seconds)"));
        }
        if self.period < self.delay_blocks {
            return err("the period is shorter than the delay in blocks");
        }
        if self.recovery_blocks < 2 * self.delay_blocks || self.recovery_seconds < 2 * self.delay_seconds {
            return err("the recovery delay is below twice the withdrawal delay");
        }
        Ok(())
    }
}

/// The head's fields as the wallet holds them.
#[derive(Serialize, Deserialize, Clone, Debug, PartialEq, Eq)]
pub struct Head {
    pub value: String,
    pub policy_hash: String,
    pub period_idx: u64,
    pub rec_h: u64,
    pub rec_t: u64,
    pub spent: String,
    pub rec_hash: String,
    pub r: String,
    /// The co-signature bit (F-800): 1 when the everyday key co-signed the pending announcement, so a
    /// rotation of the cancellation key is the recovery authority's alone to cancel.
    #[serde(default)]
    pub co: u64,
    pub cm: String,
    /// Where it sits in the tree once seen; its height; whether its nullifier was seen.
    pub position: Option<u64>,
    pub height: Option<u64>,
    pub spent_at: Option<u64>,
}

impl Head {
    pub fn value(&self) -> u128 {
        self.value.parse().unwrap_or(0)
    }
    pub fn spent(&self) -> u128 {
        self.spent.parse().unwrap_or(0)
    }
    pub fn recovery_pending(&self) -> bool {
        self.rec_hash != hex::encode(&[0u8; 32])
    }
}

/// cm_head.
pub fn head_commitment(value: u128, vid: &Bytes32, policy_hash: &Bytes32, period_idx: u64, rec_h: u64, rec_t: u64, spent: u128, rec_hash: &Bytes32, r: &Bytes32, co: u64) -> Bytes32 {
    word_to_bytes(fold(&[tag(TAG_HEAD), value_word(value), w(vid), w(policy_hash), [period_idx, rec_h, rec_t, co], value_word(spent), w(rec_hash), w(r)]))
}

/// A pending note as the wallet holds it.
#[derive(Serialize, Deserialize, Clone, Debug, PartialEq, Eq)]
pub struct Pending {
    pub value: String,
    pub vid: String,
    pub policy_hash: String,
    pub pk_dest: String,
    pub h_req: u64,
    pub t_req: u64,
    pub delay_blocks: u64,
    pub delay_seconds: u64,
    pub r: String,
    pub cm: String,
    pub position: Option<u64>,
    pub height: Option<u64>,
    /// The height at which its position tag was seen among the nullifiers (cancelled or completed).
    pub consumed_at: Option<u64>,
}

impl Pending {
    pub fn value(&self) -> u128 {
        self.value.parse().unwrap_or(0)
    }
    pub fn ptag(&self, position: u64) -> Result<Bytes32> {
        Ok(ptag(&hex::decode_array(&self.cm, "cm")?, position, &hex::decode_array(&self.r, "r")?))
    }
}

pub fn pending_commitment(value: u128, vid: &Bytes32, policy_hash: &Bytes32, pk_dest: &Bytes32, h_req: u64, t_req: u64, delay_blocks: u64, delay_seconds: u64, r: &Bytes32) -> Bytes32 {
    word_to_bytes(fold(&[tag(TAG_PENDING), value_word(value), w(vid), w(policy_hash), w(pk_dest), [h_req, t_req, delay_blocks, delay_seconds], w(r)]))
}

pub fn inbound_commitment(value: u128, vid: &Bytes32, policy_hash: &Bytes32, r: &Bytes32) -> Bytes32 {
    word_to_bytes(fold(&[tag(TAG_INBOUND), value_word(value), w(vid), w(policy_hash), w(r)]))
}

pub fn ptag(cm: &Bytes32, position: u64, r: &Bytes32) -> Bytes32 {
    word_to_bytes(fold(&[tag(TAG_PTAG), w(cm), [position, 0, 0, 0], w(r)]))
}

/// The revoked set's key of a policy hash: its first element.
pub fn revoked_key(policy_hash: &Bytes32) -> u64 {
    w(policy_hash)[0]
}

/// An inbound note as the wallet holds it.
#[derive(Serialize, Deserialize, Clone, Debug, PartialEq, Eq)]
pub struct Inbound {
    pub value: String,
    pub policy_hash: String,
    pub r: String,
    pub cm: String,
    pub position: Option<u64>,
    pub height: Option<u64>,
}

/// The vault file: the viewing key and the state. No spending secret.
#[derive(Serialize, Deserialize, Clone, Debug)]
pub struct VaultFile {
    pub kind: String,
    pub chain_id: u64,
    pub vid: String,
    pub policy: Policy,
    /// The policy announced and not yet completed or cancelled (its fields, for the completion).
    pub pending_policy: Option<Policy>,
    pub nk_v: String,
    pub kem_seed: String,
    pub heads: Vec<Head>,
    pub pending: Vec<Pending>,
    pub inbound: Vec<Inbound>,
    /// The nullifier of every head seen spent (so a head is never offered twice).
    #[serde(default)]
    pub next_position: u64,
}

impl VaultFile {
    pub fn vid(&self) -> Result<Bytes32> {
        hex::decode_array(&self.vid, "vid")
    }
    pub fn nk_v(&self) -> Result<Bytes32> {
        hex::decode_array(&self.nk_v, "nk_v")
    }
    pub fn kem(&self) -> Result<MlKem768KeyPair> {
        let seed: [u8; 64] = hex::decode_array(&self.kem_seed, "kem_seed")?;
        Ok(mlkem768::generate_key_pair(seed))
    }
    /// The live head: in the tree and not spent (the newest such).
    pub fn head(&self) -> Option<&Head> {
        self.heads.iter().rev().find(|h| h.position.is_some() && h.spent_at.is_none())
    }
    pub fn head_mut(&mut self) -> Option<&mut Head> {
        self.heads.iter_mut().rev().find(|h| h.position.is_some() && h.spent_at.is_none())
    }
    pub fn add_head(&mut self, h: Head) {
        if !self.heads.iter().any(|x| x.cm == h.cm) {
            self.heads.push(h);
        }
    }
    pub fn add_pending(&mut self, p: Pending) {
        if !self.pending.iter().any(|x| x.cm == p.cm) {
            self.pending.push(p);
        }
    }
    pub fn add_inbound(&mut self, i: Inbound) {
        if !self.inbound.iter().any(|x| x.cm == i.cm) {
            self.inbound.push(i);
        }
    }
    pub fn balance(&self) -> (u128, u128, u128) {
        let head = self.head().map(|h| h.value()).unwrap_or(0);
        let pending: u128 = self.pending.iter().filter(|p| p.position.is_some() && p.consumed_at.is_none()).map(|p| p.value()).sum();
        let inbound: u128 = self.inbound.iter().filter(|i| i.position.is_some()).map(|i| i.value.parse::<u128>().unwrap_or(0)).sum();
        (head, pending, inbound)
    }
    pub fn to_json(&self) -> String {
        serde_json::to_string_pretty(self).unwrap()
    }
    pub fn from_json(text: &str) -> Result<VaultFile> {
        serde_json::from_str(text).map_err(|e| Error(format!("vault file: {e}")))
    }
}

/// A vault note's plaintext (189 bytes, one of three versions).
fn plaintext_head(value: u128, vid: &Bytes32, ph: &Bytes32, period_idx: u64, spent: u128, rec_hash: &Bytes32, rec_h: u64, rec_t: u64, r: &Bytes32, co: u64) -> Zeroizing<[u8; sizes::NOTE_PLAINTEXT]> {
    let mut p = Zeroizing::new([0u8; sizes::NOTE_PLAINTEXT]);
    p[0] = VERSION_HEAD;
    p[1..17].copy_from_slice(&value.to_le_bytes());
    p[17..49].copy_from_slice(vid);
    p[49..81].copy_from_slice(ph);
    p[81..85].copy_from_slice(&(period_idx as u32).to_le_bytes());
    p[85..101].copy_from_slice(&spent.to_le_bytes());
    p[101..133].copy_from_slice(rec_hash);
    p[133..137].copy_from_slice(&(rec_h as u32).to_le_bytes());
    p[137..141].copy_from_slice(&(rec_t as u32).to_le_bytes());
    p[141..173].copy_from_slice(r);
    p[173] = co as u8;
    p
}

fn plaintext_pending(value: u128, vid: &Bytes32, ph: &Bytes32, pk_dest: &Bytes32, h_req: u64, t_req: u64, d_b: u64, d_s: u64, r: &Bytes32) -> Zeroizing<[u8; sizes::NOTE_PLAINTEXT]> {
    let mut p = Zeroizing::new([0u8; sizes::NOTE_PLAINTEXT]);
    p[0] = VERSION_PENDING;
    p[1..17].copy_from_slice(&value.to_le_bytes());
    p[17..49].copy_from_slice(vid);
    p[49..81].copy_from_slice(ph);
    p[81..113].copy_from_slice(pk_dest);
    p[113..117].copy_from_slice(&(h_req as u32).to_le_bytes());
    p[117..121].copy_from_slice(&(t_req as u32).to_le_bytes());
    p[121..125].copy_from_slice(&(d_b as u32).to_le_bytes());
    p[125..129].copy_from_slice(&(d_s as u32).to_le_bytes());
    p[129..161].copy_from_slice(r);
    p
}

fn plaintext_inbound(value: u128, vid: &Bytes32, ph: &Bytes32, r: &Bytes32) -> Zeroizing<[u8; sizes::NOTE_PLAINTEXT]> {
    let mut p = Zeroizing::new([0u8; sizes::NOTE_PLAINTEXT]);
    p[0] = VERSION_INBOUND;
    p[1..17].copy_from_slice(&value.to_le_bytes());
    p[17..49].copy_from_slice(vid);
    p[49..81].copy_from_slice(ph);
    p[81..113].copy_from_slice(r);
    p
}

fn u128_at(p: &[u8], at: usize) -> u128 {
    let mut b = [0u8; 16];
    b.copy_from_slice(&p[at..at + 16]);
    u128::from_le_bytes(b)
}
fn u32_at(p: &[u8], at: usize) -> u64 {
    let mut b = [0u8; 4];
    b.copy_from_slice(&p[at..at + 4]);
    u32::from_le_bytes(b) as u64
}
fn b32_at(p: &[u8], at: usize) -> Bytes32 {
    let mut b = [0u8; 32];
    b.copy_from_slice(&p[at..at + 32]);
    b
}

/// Seals a vault note's plaintext to the vault's KEM key under its commitment: one output of the one shape.
pub fn seal_vault(ek: &[u8; sizes::KEM_PUBLIC_KEY], cm: &Bytes32, plaintext: &[u8; sizes::NOTE_PLAINTEXT]) -> Result<Output> {
    let mut rnd = Zeroizing::new([0u8; 32]);
    OsRng.fill_bytes(rnd.as_mut());
    let pk = mlkem768::MlKem768PublicKey::from(*ek);
    let (kem_ct, shared) = mlkem768::encapsulate(&pk, *rnd);
    let shared = Zeroizing::new(shared);
    let mut material = Zeroizing::new(Vec::with_capacity(32 + sizes::KEM_CIPHERTEXT));
    material.extend_from_slice(shared.as_ref());
    material.extend_from_slice(kem_ct.as_slice());
    let key = Zeroizing::new(b_derive(CTX_VAULT_NOTE_KEY, &material));
    let cipher = ChaCha20Poly1305::new(Key::from_slice(key.as_ref()));
    let sealed = cipher.encrypt(Nonce::from_slice(&[0u8; 12]), Payload { msg: plaintext, aad: cm }).map_err(|_| Error("seal: the AEAD refused".into()))?;
    let mut body = [0u8; sizes::NOTE_BODY];
    body.copy_from_slice(&sealed);
    let mut kem = [0u8; sizes::KEM_CIPHERTEXT];
    kem.copy_from_slice(kem_ct.as_slice());
    Ok(Output { cm: *cm, kem_ct: kem, body })
}

/// Opens an output with the vault's KEM key; the plaintext when it is a vault note.
pub fn open_vault(kem: &MlKem768KeyPair, output: &Output) -> Option<Zeroizing<Vec<u8>>> {
    let kem_ct = MlKem768Ciphertext::from(output.kem_ct);
    let shared = Zeroizing::new(mlkem768::decapsulate(kem.private_key(), &kem_ct));
    let mut material = Zeroizing::new(Vec::with_capacity(32 + sizes::KEM_CIPHERTEXT));
    material.extend_from_slice(shared.as_ref());
    material.extend_from_slice(&output.kem_ct);
    let key = Zeroizing::new(b_derive(CTX_VAULT_NOTE_KEY, &material));
    let cipher = ChaCha20Poly1305::new(Key::from_slice(key.as_ref()));
    let p = cipher.decrypt(Nonce::from_slice(&[0u8; 12]), Payload { msg: &output.body, aad: &output.cm }).ok()?;
    if p.len() != sizes::NOTE_PLAINTEXT {
        return None;
    }
    Some(Zeroizing::new(p))
}

/// Scans one block for the vault: heads, pending and inbound notes of this vid that open under its KEM key
/// (each recomputed and kept only when its commitment matches), the heads' nullifiers and the pending notes'
/// position tags among the block's nullifiers. Returns how many vault notes were found.
pub fn scan_block(v: &mut VaultFile, kem: &MlKem768KeyPair, block: &Block) -> Result<usize> {
    let vid = v.vid()?;
    let nk_v = v.nk_v()?;
    if let Some(p) = block.position {
        v.next_position = p;
    }
    let mut found = 0;
    for output in &block.outputs {
        let position = v.next_position;
        v.next_position += 1;
        let Some(p) = open_vault(kem, output) else { continue };
        match p[0] {
            VERSION_HEAD => {
                let (value, nvid, ph, idx, spent, rec_hash, rec_h, rec_t, r, co) = (u128_at(&p, 1), b32_at(&p, 17), b32_at(&p, 49), u32_at(&p, 81), u128_at(&p, 85), b32_at(&p, 101), u32_at(&p, 133), u32_at(&p, 137), b32_at(&p, 141), p[173] as u64);
                if nvid != vid || head_commitment(value, &vid, &ph, idx, rec_h, rec_t, spent, &rec_hash, &r, co) != output.cm {
                    continue;
                }
                let h = Head { value: value.to_string(), policy_hash: hex::encode(&ph), period_idx: idx, rec_h, rec_t, spent: spent.to_string(), rec_hash: hex::encode(&rec_hash), r: hex::encode(&r), co, cm: hex::encode(&output.cm), position: Some(position), height: Some(block.height), spent_at: None };
                match v.heads.iter_mut().find(|x| x.cm == h.cm) {
                    Some(x) => {
                        x.position = Some(position);
                        x.height = Some(block.height);
                    }
                    None => v.heads.push(h),
                }
                found += 1;
            }
            VERSION_PENDING => {
                let (value, nvid, ph, pkd, h_req, t_req, d_b, d_s, r) = (u128_at(&p, 1), b32_at(&p, 17), b32_at(&p, 49), b32_at(&p, 81), u32_at(&p, 113), u32_at(&p, 117), u32_at(&p, 121), u32_at(&p, 125), b32_at(&p, 129));
                if nvid != vid || pending_commitment(value, &vid, &ph, &pkd, h_req, t_req, d_b, d_s, &r) != output.cm {
                    continue;
                }
                let n = Pending { value: value.to_string(), vid: hex::encode(&vid), policy_hash: hex::encode(&ph), pk_dest: hex::encode(&pkd), h_req, t_req, delay_blocks: d_b, delay_seconds: d_s, r: hex::encode(&r), cm: hex::encode(&output.cm), position: Some(position), height: Some(block.height), consumed_at: None };
                match v.pending.iter_mut().find(|x| x.cm == n.cm) {
                    Some(x) => {
                        x.position = Some(position);
                        x.height = Some(block.height);
                    }
                    None => v.pending.push(n),
                }
                found += 1;
            }
            VERSION_INBOUND => {
                let (value, nvid, ph, r) = (u128_at(&p, 1), b32_at(&p, 17), b32_at(&p, 49), b32_at(&p, 81));
                if nvid != vid || inbound_commitment(value, &vid, &ph, &r) != output.cm {
                    continue;
                }
                let n = Inbound { value: value.to_string(), policy_hash: hex::encode(&ph), r: hex::encode(&r), cm: hex::encode(&output.cm), position: Some(position), height: Some(block.height) };
                match v.inbound.iter_mut().find(|x| x.cm == n.cm) {
                    Some(x) => {
                        x.position = Some(position);
                        x.height = Some(block.height);
                    }
                    None => v.inbound.push(n),
                }
                found += 1;
            }
            _ => {}
        }
    }
    for nf in &block.nullifiers {
        for h in v.heads.iter_mut() {
            if let Some(pos) = h.position {
                if h.spent_at.is_none() && H::nullifier(&nk_v, &hex::decode_array(&h.cm, "cm")?, pos) == *nf {
                    h.spent_at = Some(block.height);
                }
            }
        }
        for p in v.pending.iter_mut() {
            if let Some(pos) = p.position {
                if p.consumed_at.is_none() && p.ptag(pos)? == *nf {
                    p.consumed_at = Some(block.height);
                }
            }
        }
    }
    Ok(found)
}

/// The witness (secret, the prover's input) and the record (public, the node's input) of one statement, plus
/// what the wallet keeps.
pub struct Prepared {
    pub witness: Zeroizing<String>,
    pub record: Value,
}

fn record(statement: &str, kind: u8, chain_id: u64, anchor: &Bytes32, nullifiers: Vec<String>, outputs: Vec<(Bytes32, Output)>, extra: Value) -> Value {
    let mut r = json!({
        "statement": statement, "kind": kind, "chain_id": chain_id, "anchor": hex::encode(anchor), "nullifiers": nullifiers,
        "outputs": outputs.iter().map(|(cm, o)| json!({ "commitment": hex::encode(cm), "ciphertext": hex::encode(&o.to_bytes()[32..]) })).collect::<Vec<_>>(),
        "ptag": hex::encode(&[0u8; 32]), "revoked_root": hex::encode(&[0u8; 32]), "revealed": hex::encode(&[0u8; 32]), "height": 0, "time": 0,
    });
    if let Some(m) = extra.as_object() {
        for (k, val) in m {
            r[k] = val.clone();
        }
    }
    r
}

fn policy_json(p: &Policy) -> Value {
    serde_json::to_value(p).unwrap()
}

/// A dummy ordinary output of value zero (stage one's form).
fn dummy_output() -> (u128, Bytes32, Bytes32, Bytes32, Vec<u8>) {
    let pk = random_word();
    let r = random_word();
    let cm = H::commitment(0, &pk, &r);
    let mut ct = vec![0u8; sizes::OUTPUT_CIPHERTEXT];
    OsRng.fill_bytes(&mut ct);
    (0, pk, r, cm, ct)
}

/// The creation: one or two of the wallet's ordinary notes (with their paths) become a vault of `vault_value`
/// under the policy's fields, the rest returning as change. Returns the witness, the record and the vault file.
#[allow(clippy::too_many_arguments)]
pub fn prepare_create(wallet: &Wallet, chain_id: u64, anchor: &Bytes32, inputs: &[crate::witness::SpendInput], vault_value: u128, pk_c: &Bytes32, pk_a: &Bytes32, delays: (u64, u64, u64, u64, u64), limit: u128) -> Result<(Prepared, VaultFile)> {
    if inputs.is_empty() || inputs.len() > 2 {
        return err("a creation takes one or two input notes");
    }
    let vid = random_word();
    let nk_v = vault_nk(wallet.seed(), &vid);
    let kem_seed = vault_kem_seed(wallet.seed(), &vid);
    let kem = mlkem768::generate_key_pair(*kem_seed);
    let policy = Policy {
        pk_e: hex::encode(wallet.fvk().pk()),
        pk_c: hex::encode(pk_c),
        pk_a: hex::encode(pk_a),
        nk_field: hex::encode(&nk_field(&nk_v)),
        delay_blocks: delays.0,
        delay_seconds: delays.1,
        period: delays.2,
        limit: limit.to_string(),
        recovery_blocks: delays.3,
        recovery_seconds: delays.4,
        nonce: hex::encode(&random_word()),
    };
    policy.check_floors()?;
    let ph = policy.hash()?;
    let mut sum_in: u128 = 0;
    let mut wit_inputs = Vec::new();
    let mut nullifiers = Vec::new();
    for (i, inp) in inputs.iter().enumerate() {
        let n = &inp.note;
        if n.note.pk != *wallet.fvk().pk() || n.spent_at.is_some() || n.note.value == 0 || inp.path.len() != sizes::TREE_DEPTH {
            return err(format!("input {i} is not an unspent note of this wallet with a path"));
        }
        let nf = H::nullifier(wallet.fvk().nk(), &n.cm, n.position);
        sum_in = sum_in.checked_add(n.note.value).ok_or_else(|| Error("the inputs overflow".into()))?;
        nullifiers.push(hex::encode(&nf));
        wit_inputs.push(json!({ "value": n.note.value.to_string(), "sk_spend": hex::encode(wallet.sk_spend()), "nk": hex::encode(wallet.fvk().nk()), "r": hex::encode(&n.note.r), "index": n.position, "path": inp.path.iter().map(|s| hex::encode(s)).collect::<Vec<_>>() }));
    }
    while wit_inputs.len() < 2 {
        let sk = random_word();
        let nk = random_word();
        let r = random_word();
        let cm = H::commitment(0, &H::pk(&sk, &nk), &r);
        nullifiers.push(hex::encode(&H::nullifier(&nk, &cm, 0)));
        wit_inputs.push(json!({ "value": "0", "sk_spend": hex::encode(&sk), "nk": hex::encode(&nk), "r": hex::encode(&r), "index": 0, "path": [] }));
    }
    if vault_value == 0 || vault_value > sum_in {
        return err(format!("the vault's value {vault_value} is not within the inputs ({sum_in})"));
    }
    let change = sum_in - vault_value;
    let (cv, cpk, cr, change_output) = if change == 0 {
        let d = dummy_output();
        let mut o = Output { cm: d.3, kem_ct: [0u8; sizes::KEM_CIPHERTEXT], body: [0u8; sizes::NOTE_BODY] };
        o.kem_ct.copy_from_slice(&d.4[..sizes::KEM_CIPHERTEXT]);
        o.body.copy_from_slice(&d.4[sizes::KEM_CIPHERTEXT..]);
        (0u128, d.1, d.2, o)
    } else {
        let (n, o) = crate::note::seal(&wallet.address(), change, b"")?;
        (n.value, n.pk, n.r, o)
    };
    let head_r = random_word();
    let head_cm = head_commitment(vault_value, &vid, &ph, 0, 0, 0, 0, &[0u8; 32], &head_r, 0);
    let mut ek = [0u8; sizes::KEM_PUBLIC_KEY];
    ek.copy_from_slice(kem.pk());
    let head_out = seal_vault(&ek, &head_cm, &plaintext_head(vault_value, &vid, &ph, 0, 0, &[0u8; 32], 0, 0, &head_r, 0))?;
    let witness = json!({
        "statement": "money-vault-create-v1", "chain_id": chain_id, "anchor": hex::encode(anchor),
        "nullifiers": nullifiers, "commitments": [hex::encode(&head_cm), hex::encode(&change_output.cm)],
        "policy": policy_json(&policy), "nk_v": hex::encode(&nk_v),
        "inputs": wit_inputs, "outputs": [{ "value": cv.to_string(), "pk": hex::encode(&cpk), "r": hex::encode(&cr) }],
        "head_out_vid": hex::encode(&vid), "head_out_r": hex::encode(&head_r),
    });
    let rec = record("money-vault-create-v1", 3, chain_id, anchor, witness["nullifiers"].as_array().unwrap().iter().map(|s| s.as_str().unwrap().to_string()).collect(), vec![(head_cm, head_out), (change_output.cm, change_output.clone())], json!({ "vault_value": vault_value.to_string(), "change": change.to_string() }));
    let vf = VaultFile {
        kind: "money-vault-v1".into(),
        chain_id,
        vid: hex::encode(&vid),
        policy,
        pending_policy: None,
        nk_v: hex::encode(&nk_v),
        kem_seed: hex::encode(kem_seed.as_ref()),
        heads: vec![Head { value: vault_value.to_string(), policy_hash: hex::encode(&ph), period_idx: 0, rec_h: 0, rec_t: 0, spent: "0".into(), rec_hash: hex::encode(&[0u8; 32]), r: hex::encode(&head_r), co: 0, cm: hex::encode(&head_cm), position: None, height: None, spent_at: None }],
        pending: Vec::new(),
        inbound: Vec::new(),
        next_position: 0,
    };
    Ok((Prepared { witness: Zeroizing::new(serde_json::to_string_pretty(&witness).unwrap()), record: rec }, vf))
}

fn head_witness(h: &Head, path: &[Bytes32]) -> Result<Value> {
    let pos = h.position.ok_or_else(|| Error("the head has no position: scan first".into()))?;
    if path.len() != sizes::TREE_DEPTH {
        return err("the head's path is not 32 siblings");
    }
    Ok(json!({ "value": h.value, "vid": "", "period_idx": h.period_idx, "rec_h": h.rec_h, "rec_t": h.rec_t, "spent": h.spent, "rec_hash": h.rec_hash, "co": h.co, "r": h.r, "index": pos, "path": path.iter().map(|s| hex::encode(s)).collect::<Vec<_>>() }))
}

fn head_nullifier(v: &VaultFile, h: &Head) -> Result<Bytes32> {
    Ok(H::nullifier(&v.nk_v()?, &hex::decode_array(&h.cm, "cm")?, h.position.ok_or_else(|| Error("the head has no position".into()))?))
}

fn vault_ek(v: &VaultFile) -> Result<[u8; sizes::KEM_PUBLIC_KEY]> {
    let kem = v.kem()?;
    let mut ek = [0u8; sizes::KEM_PUBLIC_KEY];
    ek.copy_from_slice(kem.pk());
    Ok(ek)
}

/// A withdrawal request by the everyday key: the live head consumed, the head carried with the running total,
/// a pending note of `value` to `to` born with the declared height and time. The vault file gains the new head.
pub fn prepare_withdraw(wallet: &Wallet, v: &mut VaultFile, anchor: &Bytes32, head_path: &[Bytes32], to: &Address, value: u128, height: u64, time: u64) -> Result<(Prepared, Pending)> {
    let head = v.head().cloned().ok_or_else(|| Error("no live head: scan first".into()))?;
    if hex::encode(wallet.fvk().pk()) != v.policy.pk_e {
        return err("this wallet's key is not the policy's everyday key");
    }
    if head.recovery_pending() {
        return err("a recovery is pending: no withdrawal until it completes or is cancelled");
    }
    if value == 0 || value > head.value() {
        return err(format!("the withdrawal {value} is not within the vault's balance {}", head.value()));
    }
    let idx = height / v.policy.period.max(1);
    if idx < head.period_idx {
        return err(format!("the period index {idx} is below the head's {} (the index only advances)", head.period_idx));
    }
    let spent_new = if idx == head.period_idx { head.spent() + value } else { value };
    if spent_new > v.policy.limit() {
        return err(format!("the period's requests would be {spent_new}, past the limit {}", v.policy.limit()));
    }
    let vid = v.vid()?;
    let ph = v.policy.hash()?;
    let head_r = random_word();
    let head_cm = head_commitment(head.value() - value, &vid, &ph, idx, 0, 0, spent_new, &[0u8; 32], &head_r, 0);
    let pr = random_word();
    let pcm = pending_commitment(value, &vid, &ph, &to.pk, height, time, v.policy.delay_blocks, v.policy.delay_seconds, &pr);
    let ek = vault_ek(v)?;
    let head_out = seal_vault(&ek, &head_cm, &plaintext_head(head.value() - value, &vid, &ph, idx, spent_new, &[0u8; 32], 0, 0, &head_r, 0))?;
    let pending_out = seal_vault(&ek, &pcm, &plaintext_pending(value, &vid, &ph, &to.pk, height, time, v.policy.delay_blocks, v.policy.delay_seconds, &pr))?;
    let nf = head_nullifier(v, &head)?;
    let mut hw = head_witness(&head, head_path)?;
    hw["vid"] = json!(v.vid);
    let witness = json!({
        "statement": "money-vault-withdraw-v1", "chain_id": v.chain_id, "anchor": hex::encode(anchor),
        "nullifiers": [hex::encode(&nf)], "commitments": [hex::encode(&head_cm), hex::encode(&pcm)], "height": height, "time": time,
        "policy": policy_json(&v.policy), "nk_v": v.nk_v, "sk_e": hex::encode(wallet.sk_spend()), "nk_e": hex::encode(wallet.fvk().nk()),
        "head_in": hw, "head_out_r": hex::encode(&head_r),
        "pending_out_value": value.to_string(), "pending_out_pk_dest": hex::encode(&to.pk), "pending_out_r": hex::encode(&pr),
    });
    let rec = record("money-vault-withdraw-v1", 4, v.chain_id, anchor, vec![hex::encode(&nf)], vec![(head_cm, head_out), (pcm, pending_out)], json!({ "height": height, "time": time }));
    v.add_head(Head { value: (head.value() - value).to_string(), policy_hash: hex::encode(&ph), period_idx: idx, rec_h: 0, rec_t: 0, spent: spent_new.to_string(), rec_hash: hex::encode(&[0u8; 32]), r: hex::encode(&head_r), co: 0, cm: hex::encode(&head_cm), position: None, height: None, spent_at: None });
    let pending = Pending { value: value.to_string(), vid: v.vid.clone(), policy_hash: hex::encode(&ph), pk_dest: hex::encode(&to.pk), h_req: height, t_req: time, delay_blocks: v.policy.delay_blocks, delay_seconds: v.policy.delay_seconds, r: hex::encode(&pr), cm: hex::encode(&pcm), position: None, height: None, consumed_at: None };
    v.add_pending(pending.clone());
    Ok((Prepared { witness: Zeroizing::new(serde_json::to_string_pretty(&witness).unwrap()), record: rec }, pending))
}

fn pending_witness(p: &Pending, path: &[Bytes32]) -> Result<Value> {
    let pos = p.position.ok_or_else(|| Error("the pending note has no position: scan first".into()))?;
    if path.len() != sizes::TREE_DEPTH {
        return err("the pending note's path is not 32 siblings");
    }
    Ok(json!({ "value": p.value, "vid": p.vid, "policy_hash": p.policy_hash, "pk_dest": p.pk_dest, "h_req": p.h_req, "t_req": p.t_req, "delay_blocks": p.delay_blocks, "delay_seconds": p.delay_seconds, "r": p.r, "index": pos, "path": path.iter().map(|s| hex::encode(s)).collect::<Vec<_>>() }))
}

/// The cancellation: the holder of sk_c consumes a pending note of the vault into an inbound note.
pub fn prepare_cancel(v: &VaultFile, sk_c: &Bytes32, anchor: &Bytes32, pending: &Pending, path: &[Bytes32]) -> Result<Prepared> {
    if hex::encode(&pk_cancel(sk_c)) != v.policy.pk_c {
        return err("this key is not the policy's cancellation key");
    }
    if pending.policy_hash != hex::encode(&v.policy.hash()?) {
        return err("the pending note is not under this vault's policy");
    }
    let pos = pending.position.ok_or_else(|| Error("the pending note has no position".into()))?;
    let pcm: Bytes32 = hex::decode_array(&pending.cm, "cm")?;
    let nf = H::nullifier(&nk_cancel(sk_c), &pcm, pos);
    let tagw = pending.ptag(pos)?;
    let ir = random_word();
    let vid = v.vid()?;
    let ph = v.policy.hash()?;
    let icm = inbound_commitment(pending.value(), &vid, &ph, &ir);
    let out = seal_vault(&vault_ek(v)?, &icm, &plaintext_inbound(pending.value(), &vid, &ph, &ir))?;
    let witness = json!({
        "statement": "money-vault-cancel-v1", "chain_id": v.chain_id, "anchor": hex::encode(anchor),
        "nullifiers": [hex::encode(&nf)], "ptag": hex::encode(&tagw), "commitments": [hex::encode(&icm)],
        "policy": policy_json(&v.policy), "sk_c": hex::encode(sk_c), "pending_in": pending_witness(pending, path)?, "inbound_r": hex::encode(&ir),
    });
    let rec = record("money-vault-cancel-v1", 5, v.chain_id, anchor, vec![hex::encode(&nf)], vec![(icm, out)], json!({ "ptag": hex::encode(&tagw) }));
    Ok(Prepared { witness: Zeroizing::new(serde_json::to_string_pretty(&witness).unwrap()), record: rec })
}

/// The revoked set's non-membership witness as the node serves it (igneum_poolRevokedPath).
#[derive(Serialize, Deserialize, Clone, Debug)]
pub struct RevokedPath {
    pub root: String,
    #[serde(default)]
    pub member: bool,
    #[serde(rename = "lowIndex")]
    pub low_index: u64,
    #[serde(rename = "lowKey")]
    pub low_key: u64,
    #[serde(rename = "lowNext")]
    pub low_next: u64,
    pub siblings: Vec<String>,
}

/// The completion by the destination wallet: a matured pending note into two ordinary notes of this wallet.
pub fn prepare_complete(wallet: &Wallet, chain_id: u64, anchor: &Bytes32, pending: &Pending, path: &[Bytes32], revoked: &RevokedPath, height: u64, time: u64) -> Result<Prepared> {
    if pending.pk_dest != hex::encode(wallet.fvk().pk()) {
        return err("this wallet is not the pending note's destination");
    }
    if height < pending.h_req + pending.delay_blocks || time < pending.t_req + pending.delay_seconds {
        return err(format!("not matured: matures at block {} and time {}", pending.h_req + pending.delay_blocks, pending.t_req + pending.delay_seconds));
    }
    if revoked.member {
        return err("the pending note's policy is revoked: a completion cannot be proven");
    }
    let pos = pending.position.ok_or_else(|| Error("the pending note has no position".into()))?;
    let pcm: Bytes32 = hex::decode_array(&pending.cm, "cm")?;
    let nf = H::nullifier(wallet.fvk().nk(), &pcm, pos);
    let tagw = pending.ptag(pos)?;
    let (n0, o0) = crate::note::seal(&wallet.address(), pending.value(), b"vault")?;
    let d = dummy_output();
    let mut o1 = Output { cm: d.3, kem_ct: [0u8; sizes::KEM_CIPHERTEXT], body: [0u8; sizes::NOTE_BODY] };
    o1.kem_ct.copy_from_slice(&d.4[..sizes::KEM_CIPHERTEXT]);
    o1.body.copy_from_slice(&d.4[sizes::KEM_CIPHERTEXT..]);
    let witness = json!({
        "statement": "money-vault-complete-v1", "chain_id": chain_id, "anchor": hex::encode(anchor), "revoked_root": revoked.root.trim_start_matches("0x"),
        "nullifiers": [hex::encode(&nf)], "ptag": hex::encode(&tagw), "commitments": [hex::encode(&o0.cm), hex::encode(&o1.cm)], "height": height, "time": time,
        "sk_dest": hex::encode(wallet.sk_spend()), "nk_dest": hex::encode(wallet.fvk().nk()), "pending_in": pending_witness(pending, path)?,
        "outputs": [{ "value": n0.value.to_string(), "pk": hex::encode(&n0.pk), "r": hex::encode(&n0.r) }, { "value": "0", "pk": hex::encode(&d.1), "r": hex::encode(&d.2) }],
        "revoked": { "low_key": revoked.low_key, "low_next": revoked.low_next, "low_index": revoked.low_index, "path": revoked.siblings.iter().map(|s| s.trim_start_matches("0x").to_string()).collect::<Vec<_>>() },
    });
    let rec = record("money-vault-complete-v1", 6, chain_id, anchor, vec![hex::encode(&nf)], vec![(o0.cm, o0), (o1.cm, o1)], json!({ "ptag": hex::encode(&tagw), "revoked_root": revoked.root.trim_start_matches("0x"), "height": height, "time": time }));
    Ok(Prepared { witness: Zeroizing::new(serde_json::to_string_pretty(&witness).unwrap()), record: rec })
}

/// A new policy for an announcement: the new everyday wallet's key and nullifier key for this vault, the
/// new cancellation and recovery commitments, the new delays and limit.
pub fn new_policy(new_wallet: &Wallet, v: &VaultFile, pk_c: &Bytes32, pk_a: &Bytes32, delays: (u64, u64, u64, u64, u64), limit: u128) -> Result<(Policy, Bytes32)> {
    let vid = v.vid()?;
    let nk_v2 = vault_nk(new_wallet.seed(), &vid);
    let p = Policy {
        pk_e: hex::encode(new_wallet.fvk().pk()),
        pk_c: hex::encode(pk_c),
        pk_a: hex::encode(pk_a),
        nk_field: hex::encode(&nk_field(&nk_v2)),
        delay_blocks: delays.0,
        delay_seconds: delays.1,
        period: delays.2,
        limit: limit.to_string(),
        recovery_blocks: delays.3,
        recovery_seconds: delays.4,
        nonce: hex::encode(&random_word()),
    };
    p.check_floors()?;
    Ok((p, nk_v2))
}

/// The announcement by the recovery authority: the live head consumed and re-emitted with the new policy's
/// hash and the declared height and time, the old policy hash revealed (the node revokes it at once, F-799:
/// every pending withdrawal under the old policy is refused at completion from the next block until the
/// recovery is cancelled). The vault file gains the new head and the pending policy.
pub fn prepare_announce(v: &mut VaultFile, sk_a: &Bytes32, anchor: &Bytes32, head_path: &[Bytes32], newp: &Policy, height: u64, time: u64, cosign: Option<&Wallet>) -> Result<Prepared> {
    if hex::encode(&pk_recover(sk_a)) != v.policy.pk_a {
        return err("this key is not the policy's recovery authority");
    }
    // F-800: the everyday key's co-signature makes a rotation of the cancellation key the recovery
    // authority's alone to cancel; without it the cancellation key cancels (a compromised authority's
    // rotation is the owner's to stop).
    if let Some(w) = cosign {
        if hex::encode(w.fvk().pk()) != v.policy.pk_e {
            return err("the co-signing wallet's key is not the policy's everyday key");
        }
    }
    let co: u64 = cosign.is_some() as u64;
    let head = v.head().cloned().ok_or_else(|| Error("no live head: scan first".into()))?;
    if head.recovery_pending() {
        return err("a recovery is already pending");
    }
    newp.check_floors()?;
    let vid = v.vid()?;
    let ph = v.policy.hash()?;
    let nph = newp.hash()?;
    let head_r = random_word();
    let head_cm = head_commitment(head.value(), &vid, &ph, head.period_idx, height, time, head.spent(), &nph, &head_r, co);
    let out = seal_vault(&vault_ek(v)?, &head_cm, &plaintext_head(head.value(), &vid, &ph, head.period_idx, head.spent(), &nph, height, time, &head_r, co))?;
    let nf = head_nullifier(v, &head)?;
    let mut hw = head_witness(&head, head_path)?;
    hw["vid"] = json!(v.vid);
    let witness = json!({
        "statement": "money-vault-announce-v1", "chain_id": v.chain_id, "anchor": hex::encode(anchor),
        "nullifiers": [hex::encode(&nf)], "commitments": [hex::encode(&head_cm)], "old_policy_hash": hex::encode(&ph), "height": height, "time": time,
        "policy": policy_json(&v.policy), "new_policy": policy_json(newp), "nk_v": v.nk_v, "sk_a": hex::encode(sk_a),
        "sk_e": cosign.map(|w| hex::encode(w.sk_spend())).unwrap_or_default(), "nk_e": cosign.map(|w| hex::encode(w.fvk().nk())).unwrap_or_default(),
        "head_in": hw, "head_out_r": hex::encode(&head_r),
    });
    let rec = record("money-vault-announce-v1", 7, v.chain_id, anchor, vec![hex::encode(&nf)], vec![(head_cm, out)], json!({ "revealed": hex::encode(&ph), "height": height, "time": time }));
    v.add_head(Head { value: head.value.clone(), policy_hash: hex::encode(&ph), period_idx: head.period_idx, rec_h: height, rec_t: time, spent: head.spent.clone(), rec_hash: hex::encode(&nph), r: hex::encode(&head_r), co, cm: hex::encode(&head_cm), position: None, height: None, spent_at: None });
    v.pending_policy = Some(newp.clone());
    Ok(Prepared { witness: Zeroizing::new(serde_json::to_string_pretty(&witness).unwrap()), record: rec })
}

/// The policy completion after the recovery delay: by anyone holding the viewing key and the pending policy's
/// fields. The vault file's policy becomes the new one (its nk_v given by the new everyday wallet).
pub fn prepare_policy_complete(v: &mut VaultFile, anchor: &Bytes32, head_path: &[Bytes32], new_nk_v: &Bytes32, height: u64, time: u64) -> Result<Prepared> {
    let head = v.head().cloned().ok_or_else(|| Error("no live head: scan first".into()))?;
    let newp = v.pending_policy.clone().ok_or_else(|| Error("no pending policy in the vault file".into()))?;
    let nph = newp.hash()?;
    if head.rec_hash != hex::encode(&nph) {
        return err("the head's pending recovery is not the vault file's pending policy");
    }
    if hex::encode(&nk_field(new_nk_v)) != newp.nk_field {
        return err("the new nullifier key does not open the new policy's nk_field");
    }
    if height < head.rec_h + v.policy.recovery_blocks || time < head.rec_t + v.policy.recovery_seconds {
        return err(format!("the recovery delay has not passed: completes at block {} and time {}", head.rec_h + v.policy.recovery_blocks, head.rec_t + v.policy.recovery_seconds));
    }
    let vid = v.vid()?;
    let old = v.policy.hash()?;
    let head_r = random_word();
    let idx = height / newp.period.max(1);
    let head_cm = head_commitment(head.value(), &vid, &nph, idx, 0, 0, 0, &[0u8; 32], &head_r, 0);
    let out = seal_vault(&vault_ek(v)?, &head_cm, &plaintext_head(head.value(), &vid, &nph, idx, 0, &[0u8; 32], 0, 0, &head_r, 0))?;
    let nf = head_nullifier(v, &head)?;
    let mut hw = head_witness(&head, head_path)?;
    hw["vid"] = json!(v.vid);
    let witness = json!({
        "statement": "money-vault-policy-complete-v1", "chain_id": v.chain_id, "anchor": hex::encode(anchor),
        "nullifiers": [hex::encode(&nf)], "commitments": [hex::encode(&head_cm)], "old_policy_hash": hex::encode(&old), "height": height, "time": time,
        "policy": policy_json(&v.policy), "new_policy": policy_json(&newp), "nk_v": v.nk_v,
        "head_in": hw, "head_out_r": hex::encode(&head_r),
    });
    let rec = record("money-vault-policy-complete-v1", 8, v.chain_id, anchor, vec![hex::encode(&nf)], vec![(head_cm, out)], json!({ "revealed": hex::encode(&old), "height": height, "time": time }));
    v.add_head(Head { value: head.value.clone(), policy_hash: hex::encode(&nph), period_idx: idx, rec_h: 0, rec_t: 0, spent: "0".into(), rec_hash: hex::encode(&[0u8; 32]), r: hex::encode(&head_r), co: 0, cm: hex::encode(&head_cm), position: None, height: None, spent_at: None });
    v.policy = newp;
    v.pending_policy = None;
    v.nk_v = hex::encode(new_nk_v);
    Ok(Prepared { witness: Zeroizing::new(serde_json::to_string_pretty(&witness).unwrap()), record: rec })
}

/// A pending recovery is cleared, revealing the policy hash the announcement revealed so the node restores
/// it (F-799: the policy's pending withdrawals complete again). The key (F-800): the cancellation key when
/// the announced policy keeps the cancellation key's commitment; the recovery authority alone when it changes
/// it, so a compromised cancellation key cannot cancel the recovery that would rotate it. The announced
/// policy's fields (the vault file's pending policy, given by the announcer) are in the witness. `force`
/// skips the key check (the lab's attacker form: the prover's host check then refuses with the circuit's
/// words).
pub fn prepare_rec_cancel(v: &mut VaultFile, sk: &Bytes32, anchor: &Bytes32, head_path: &[Bytes32], force: bool) -> Result<Prepared> {
    let head = v.head().cloned().ok_or_else(|| Error("no live head: scan first".into()))?;
    if !head.recovery_pending() {
        return err("no recovery is pending");
    }
    let newp = v.pending_policy.clone().ok_or_else(|| Error("no pending policy in the vault file: the announced policy's fields are needed to cancel it".into()))?;
    if head.rec_hash != hex::encode(&newp.hash()?) {
        return err("the head's pending recovery is not the vault file's pending policy");
    }
    let rotates = newp.pk_c != v.policy.pk_c && head.co == 1;
    if !force {
        if rotates && hex::encode(&pk_recover(sk)) != v.policy.pk_a {
            return err("the pending recovery changes the cancellation key and the everyday key co-signed it: only the recovery authority can cancel it (this key is not the policy's recovery authority)");
        }
        if !rotates && hex::encode(&pk_cancel(sk)) != v.policy.pk_c {
            return err("this key is not the policy's cancellation key");
        }
    }
    let vid = v.vid()?;
    let ph = v.policy.hash()?;
    let head_r = random_word();
    let head_cm = head_commitment(head.value(), &vid, &ph, head.period_idx, 0, 0, head.spent(), &[0u8; 32], &head_r, 0);
    let out = seal_vault(&vault_ek(v)?, &head_cm, &plaintext_head(head.value(), &vid, &ph, head.period_idx, head.spent(), &[0u8; 32], 0, 0, &head_r, 0))?;
    let nf = head_nullifier(v, &head)?;
    let mut hw = head_witness(&head, head_path)?;
    hw["vid"] = json!(v.vid);
    let witness = json!({
        "statement": "money-vault-rec-cancel-v1", "chain_id": v.chain_id, "anchor": hex::encode(anchor),
        "nullifiers": [hex::encode(&nf)], "commitments": [hex::encode(&head_cm)], "old_policy_hash": hex::encode(&ph),
        "policy": policy_json(&v.policy), "new_policy": policy_json(&newp), "nk_v": v.nk_v, "sk_c": hex::encode(sk), "sk_a": hex::encode(sk), "head_in": hw, "head_out_r": hex::encode(&head_r),
    });
    let rec = record("money-vault-rec-cancel-v1", 9, v.chain_id, anchor, vec![hex::encode(&nf)], vec![(head_cm, out)], json!({ "revealed": hex::encode(&ph) }));
    v.add_head(Head { value: head.value.clone(), policy_hash: hex::encode(&ph), period_idx: head.period_idx, rec_h: 0, rec_t: 0, spent: head.spent.clone(), rec_hash: hex::encode(&[0u8; 32]), r: hex::encode(&head_r), co: 0, cm: hex::encode(&head_cm), position: None, height: None, spent_at: None });
    // the pending policy's fields stay in the file: a prepared cancellation may never land (the lab's run 5
    // prepared one and did not send it; the file had forgotten the policy the completion then needed), and
    // the chain's head says whether a recovery is pending
    Ok(Prepared { witness: Zeroizing::new(serde_json::to_string_pretty(&witness).unwrap()), record: rec })
}

#[cfg(test)]
mod tests {
    use super::*;

    fn rep(b: u8) -> Bytes32 {
        reduce_word(&[b; 32])
    }

    #[test]
    fn the_vault_vectors_hold() {
        let text = match std::fs::read_to_string(concat!(env!("CARGO_MANIFEST_DIR"), "/../../tools/money/vectors/money-vault-vectors.json")) {
            Ok(t) => t,
            Err(_) => return,
        };
        let v: Value = serde_json::from_str(&text).unwrap();
        let s = |a: &Value, k: &str| a[k].as_str().unwrap().to_string();
        let keys = &v["keys"];
        let sk_c: Bytes32 = hex::decode_array(&s(keys, "sk_c"), "sk_c").unwrap();
        let sk_a: Bytes32 = hex::decode_array(&s(keys, "sk_a"), "sk_a").unwrap();
        let nk_v: Bytes32 = hex::decode_array(&s(keys, "nk_v"), "nk_v").unwrap();
        assert_eq!(hex::encode(&pk_cancel(&sk_c)), s(keys, "pk_c"));
        assert_eq!(hex::encode(&pk_recover(&sk_a)), s(keys, "pk_a"));
        assert_eq!(hex::encode(&nk_field(&nk_v)), s(keys, "nk_field"));
        assert_eq!(hex::encode(&nk_cancel(&sk_c)), s(keys, "nk_c"));
        let p = &v["policy"];
        let policy = Policy { pk_e: s(p, "pk_e"), pk_c: s(keys, "pk_c"), pk_a: s(keys, "pk_a"), nk_field: s(keys, "nk_field"), delay_blocks: 300, delay_seconds: 3600, period: 300, limit: s(p, "limit"), recovery_blocks: 600, recovery_seconds: 7200, nonce: s(p, "nonce") };
        let ph = policy.hash().unwrap();
        assert_eq!(hex::encode(&ph), s(p, "hash"));
        let h = &v["head"];
        let vid: Bytes32 = hex::decode_array(&s(h, "vid"), "vid").unwrap();
        let cm = head_commitment(s(h, "value").parse().unwrap(), &vid, &ph, 3, 0, 0, s(h, "spent").parse().unwrap(), &hex::decode_array(&s(h, "rec_hash"), "rec_hash").unwrap(), &hex::decode_array(&s(h, "r"), "r").unwrap(), 0);
        assert_eq!(hex::encode(&cm), s(h, "cm"));
        let n = &v["pending"];
        let pr: Bytes32 = hex::decode_array(&s(n, "r"), "r").unwrap();
        let pcm = pending_commitment(s(n, "value").parse().unwrap(), &vid, &ph, &hex::decode_array(&s(n, "pk_dest"), "pk_dest").unwrap(), 1000, 2_000_000, 300, 3600, &pr);
        assert_eq!(hex::encode(&pcm), s(n, "cm"));
        assert_eq!(hex::encode(&ptag(&pcm, 5, &pr)), s(n, "ptag_at_5"));
        let i = &v["inbound"];
        assert_eq!(hex::encode(&inbound_commitment(s(n, "value").parse().unwrap(), &vid, &ph, &hex::decode_array(&s(i, "r"), "r").unwrap())), s(i, "cm"));
        assert_eq!(revoked_key(&ph), v["revoked"]["key_of_policy"].as_u64().unwrap());
    }

    #[test]
    fn a_vault_is_created_scanned_requested_cancelled_and_completed() {
        let owner = Wallet::generate();
        let thief_dest = Wallet::generate();
        let sk_c = rep(0x61);
        let sk_a = rep(0x62);
        let (_, out) = crate::note::seal(&owner.address(), 7_000_000_000_000_000_000, b"").unwrap();
        let mut s = crate::scan::Scanner::new(owner.fvk().clone(), 0);
        s.scan_block(&Block { height: 1, position: Some(0), outputs: vec![out], nullifiers: vec![], mints: vec![] });
        let n = s.notes()[0].clone();
        let inputs = vec![crate::witness::SpendInput { note: n, path: vec![[0u8; 32]; 32] }];
        let (p, mut vf) = prepare_create(&owner, 7404, &[1u8; 32], &inputs, 5_000_000_000_000_000_000, &pk_cancel(&sk_c), &pk_recover(&sk_a), (300, 3600, 300, 600, 7200), 1_000_000_000_000_000_000).unwrap();
        assert_eq!(p.record["outputs"].as_array().unwrap().len(), 2);
        // the chain carries the head and the change; the vault scans its head
        let kem = vf.kem().unwrap();
        let head_out = Output::from_bytes(&[hex::decode(p.record["outputs"][0]["commitment"].as_str().unwrap()).unwrap(), hex::decode(p.record["outputs"][0]["ciphertext"].as_str().unwrap()).unwrap()].concat()).unwrap();
        let change_out = Output::from_bytes(&[hex::decode(p.record["outputs"][1]["commitment"].as_str().unwrap()).unwrap(), hex::decode(p.record["outputs"][1]["ciphertext"].as_str().unwrap()).unwrap()].concat()).unwrap();
        assert!(crate::note::open(owner.fvk(), &change_out).is_some());
        assert!(crate::note::open(owner.fvk(), &head_out).is_none());
        let found = scan_block(&mut vf, &kem, &Block { height: 2, position: Some(1), outputs: vec![head_out, change_out], nullifiers: vec![], mints: vec![] }).unwrap();
        assert_eq!(found, 1);
        assert_eq!(vf.head().unwrap().position, Some(1));
        assert_eq!(vf.balance().0, 5_000_000_000_000_000_000);
        // a copy of the vault file reads the same (the cancellation holder's copy)
        let mut holder = VaultFile::from_json(&vf.to_json()).unwrap();
        // a request to the thief's destination
        let (q, pending) = prepare_withdraw(&owner, &mut vf, &[2u8; 32], &vec![[0u8; 32]; 32], &thief_dest.address(), 400_000_000_000_000_000, 1000, 2_000_000).unwrap();
        let outs: Vec<Output> = q.record["outputs"].as_array().unwrap().iter().map(|o| Output::from_bytes(&[hex::decode(o["commitment"].as_str().unwrap()).unwrap(), hex::decode(o["ciphertext"].as_str().unwrap()).unwrap()].concat()).unwrap()).collect();
        let nf: Bytes32 = hex::decode_array(q.record["nullifiers"][0].as_str().unwrap(), "nf").unwrap();
        let b = Block { height: 3, position: Some(3), outputs: outs, nullifiers: vec![nf], mints: vec![] };
        scan_block(&mut vf, &kem, &b).unwrap();
        let hkem = holder.kem().unwrap();
        scan_block(&mut holder, &hkem, &b).unwrap();
        assert_eq!(vf.head().unwrap().position, Some(3));
        assert_eq!(vf.head().unwrap().spent, "400000000000000000");
        assert_eq!(holder.head().unwrap().cm, vf.head().unwrap().cm);
        assert_eq!(holder.pending.len(), 1);
        assert_eq!(holder.pending[0].position, Some(4));
        // past the limit in the same period
        assert!(prepare_withdraw(&owner, &mut vf, &[2u8; 32], &vec![[0u8; 32]; 32], &thief_dest.address(), 600_000_000_000_000_001, 1010, 2_000_010).is_err());
        // the holder cancels it; the wrong key cannot
        let hp = holder.pending[0].clone();
        assert!(prepare_cancel(&holder, &rep(0x63), &[3u8; 32], &hp, &vec![[0u8; 32]; 32]).is_err());
        let c = prepare_cancel(&holder, &sk_c, &[3u8; 32], &hp, &vec![[0u8; 32]; 32]).unwrap();
        assert_eq!(c.record["kind"], 5);
        let tagw: Bytes32 = hex::decode_array(c.record["ptag"].as_str().unwrap(), "ptag").unwrap();
        assert_eq!(hex::encode(&tagw), hex::encode(&pending.ptag(4).unwrap()));
        // the destination's completion before maturity is refused; after it, prepared
        let rp = RevokedPath { root: hex::encode(&[0u8; 32]), member: false, low_index: 0, low_key: 0, low_next: 0, siblings: vec![hex::encode(&[0u8; 32]); 16] };
        assert!(prepare_complete(&thief_dest, 7404, &[3u8; 32], &hp, &vec![[0u8; 32]; 32], &rp, 1299, 2_003_600).is_err());
        let k = prepare_complete(&thief_dest, 7404, &[3u8; 32], &hp, &vec![[0u8; 32]; 32], &rp, 1300, 2_003_600).unwrap();
        assert_eq!(k.record["kind"], 6);
        assert!(prepare_complete(&owner, 7404, &[3u8; 32], &hp, &vec![[0u8; 32]; 32], &rp, 1300, 2_003_600).is_err());
        // the cancellation lands: the pending note is consumed
        let ib = Output::from_bytes(&[hex::decode(c.record["outputs"][0]["commitment"].as_str().unwrap()).unwrap(), hex::decode(c.record["outputs"][0]["ciphertext"].as_str().unwrap()).unwrap()].concat()).unwrap();
        scan_block(&mut vf, &kem, &Block { height: 4, position: Some(5), outputs: vec![ib], nullifiers: vec![tagw], mints: vec![] }).unwrap();
        assert_eq!(vf.pending[0].consumed_at, Some(4));
        assert_eq!(vf.balance().2, 400_000_000_000_000_000);
        // recovery: announced by the authority, cancelled by the cancellation key
        let new_owner = Wallet::generate();
        let (newp, nk2) = new_policy(&new_owner, &vf, &pk_cancel(&rep(0x64)), &pk_recover(&rep(0x65)), (300, 3600, 300, 600, 7200), 1).unwrap();
        assert!(prepare_announce(&mut vf, &rep(0x66), &[4u8; 32], &vec![[0u8; 32]; 32], &newp, 2000, 2_010_000, None).is_err());
        // the owner co-signs the rotation (F-800); a stranger's wallet cannot
        assert!(prepare_announce(&mut vf.clone(), &sk_a, &[4u8; 32], &vec![[0u8; 32]; 32], &newp, 2000, 2_010_000, Some(&thief_dest)).is_err());
        let a = prepare_announce(&mut vf, &sk_a, &[4u8; 32], &vec![[0u8; 32]; 32], &newp, 2000, 2_010_000, Some(&owner)).unwrap();
        // F-799: the announcement reveals the old policy hash on its record (the node revokes it at once)
        assert_eq!(a.record["revealed"], json!(hex::encode(&vf.policy.hash().unwrap())));
        let ho = Output::from_bytes(&[hex::decode(a.record["outputs"][0]["commitment"].as_str().unwrap()).unwrap(), hex::decode(a.record["outputs"][0]["ciphertext"].as_str().unwrap()).unwrap()].concat()).unwrap();
        let nf2: Bytes32 = hex::decode_array(a.record["nullifiers"][0].as_str().unwrap(), "nf").unwrap();
        scan_block(&mut vf, &kem, &Block { height: 5, position: Some(6), outputs: vec![ho], nullifiers: vec![nf2], mints: vec![] }).unwrap();
        assert!(vf.head().unwrap().recovery_pending());
        assert_eq!(vf.head().unwrap().co, 1);
        assert!(prepare_withdraw(&owner, &mut vf, &[5u8; 32], &vec![[0u8; 32]; 32], &thief_dest.address(), 1, 2100, 2_011_000).is_err());
        assert!(prepare_policy_complete(&mut vf.clone(), &[5u8; 32], &vec![[0u8; 32]; 32], &nk2, 2599, 2_017_200).is_err());
        let pc = prepare_policy_complete(&mut vf.clone(), &[5u8; 32], &vec![[0u8; 32]; 32], &nk2, 2600, 2_017_200).unwrap();
        assert_eq!(pc.record["kind"], 8);
        // F-800: the announced policy changes the cancellation key and the owner co-signed, so the old
        // cancellation key cannot cancel the recovery; the recovery authority can
        assert!(prepare_rec_cancel(&mut vf.clone(), &sk_c, &[5u8; 32], &vec![[0u8; 32]; 32], false).is_err());
        assert!(prepare_rec_cancel(&mut vf.clone(), &rep(0x67), &[5u8; 32], &vec![[0u8; 32]; 32], false).is_err());
        let rc = prepare_rec_cancel(&mut vf, &sk_a, &[5u8; 32], &vec![[0u8; 32]; 32], false).unwrap();
        assert_eq!(rc.record["kind"], 9);
        assert_eq!(rc.record["revealed"], json!(hex::encode(&vf.policy.hash().unwrap())));
        // a rotation nobody co-signed (a compromised authority's) is cancellable by the cancellation key alone
        let ho2 = Output::from_bytes(&[hex::decode(rc.record["outputs"][0]["commitment"].as_str().unwrap()).unwrap(), hex::decode(rc.record["outputs"][0]["ciphertext"].as_str().unwrap()).unwrap()].concat()).unwrap();
        let nf3: Bytes32 = hex::decode_array(rc.record["nullifiers"][0].as_str().unwrap(), "nf").unwrap();
        scan_block(&mut vf, &kem, &Block { height: 6, position: Some(7), outputs: vec![ho2], nullifiers: vec![nf3], mints: vec![] }).unwrap();
        assert!(!vf.head().unwrap().recovery_pending());
        let (kept, _) = new_policy(&new_owner, &vf, &pk_cancel(&rep(0x64)), &pk_recover(&rep(0x65)), (300, 3600, 300, 600, 7200), 1).unwrap();
        let a2 = prepare_announce(&mut vf, &sk_a, &[6u8; 32], &vec![[0u8; 32]; 32], &kept, 2700, 2_020_000, None).unwrap();
        let ho3 = Output::from_bytes(&[hex::decode(a2.record["outputs"][0]["commitment"].as_str().unwrap()).unwrap(), hex::decode(a2.record["outputs"][0]["ciphertext"].as_str().unwrap()).unwrap()].concat()).unwrap();
        let nf4: Bytes32 = hex::decode_array(a2.record["nullifiers"][0].as_str().unwrap(), "nf").unwrap();
        scan_block(&mut vf, &kem, &Block { height: 7, position: Some(8), outputs: vec![ho3], nullifiers: vec![nf4], mints: vec![] }).unwrap();
        assert!(vf.head().unwrap().recovery_pending());
        assert_eq!(vf.head().unwrap().co, 0);
        assert!(prepare_rec_cancel(&mut vf.clone(), &sk_a, &[7u8; 32], &vec![[0u8; 32]; 32], false).is_err());
        let rc2 = prepare_rec_cancel(&mut vf, &sk_c, &[7u8; 32], &vec![[0u8; 32]; 32], false).unwrap();
        assert_eq!(rc2.record["kind"], 9);
    }
}
