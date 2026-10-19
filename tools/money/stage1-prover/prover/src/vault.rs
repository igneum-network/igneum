//! Money stage 2, cut 1: the vault's seven statements (docs/money/VAULT-SPEC-2026-10-18.md as re-cut by the
//! panel; docs/money/STAGE2-CUT1-VAULT-2026-10-18.md) on the stage-one machinery: the object hashes (the policy,
//! the head, the pending note, the inbound note, the position tag, the three authorities' commitments), the
//! witness form, the host-side check and the advice layout of each statement. The Miden assembly of every
//! statement is the prelude (masm/vault-prelude.masm) followed by its own body; the node's verifier library
//! joins the two texts the same way, so the program hashes agree.
use miden_core::Word;
use miden_crypto::merkle::{MerklePath, store::MerkleStore};
use miden_vm::advice::{AdviceInputs, AdviceStack};
use serde::{Deserialize, Serialize};

use crate::{chain, commitment, domain_word, hex32, ints, merge, nullifier, pk, stack_for, u128s, value_word, word, InputWitness, OutputWitness, Prepared, DEPTH};

/// The statement kinds (the second element of the domain word, the record's tag byte).
pub const KIND_CREATE: u64 = 3;
pub const KIND_WITHDRAW: u64 = 4;
pub const KIND_CANCEL: u64 = 5;
pub const KIND_COMPLETE: u64 = 6;
pub const KIND_ANNOUNCE: u64 = 7;
pub const KIND_POLICY_COMPLETE: u64 = 8;
pub const KIND_REC_CANCEL: u64 = 9;

/// The statement names, by kind.
pub fn statement_name(kind: u64) -> Option<&'static str> {
    Some(match kind {
        KIND_CREATE => "money-vault-create-v1",
        KIND_WITHDRAW => "money-vault-withdraw-v1",
        KIND_CANCEL => "money-vault-cancel-v1",
        KIND_COMPLETE => "money-vault-complete-v1",
        KIND_ANNOUNCE => "money-vault-announce-v1",
        KIND_POLICY_COMPLETE => "money-vault-policy-complete-v1",
        KIND_REC_CANCEL => "money-vault-rec-cancel-v1",
        _ => return None,
    })
}

/// The kind of a statement name.
pub fn kind_of(name: &str) -> Option<u64> {
    (KIND_CREATE..=KIND_REC_CANCEL).find(|k| statement_name(*k) == Some(name))
}

/// The hash domain tags (the first element of a tag word [tag, 0, 0, 0]).
pub const TAG_POLICY: u64 = 11;
pub const TAG_HEAD: u64 = 12;
pub const TAG_PENDING: u64 = 13;
pub const TAG_INBOUND: u64 = 14;
pub const TAG_PTAG: u64 = 15;
pub const TAG_CANCEL: u64 = 16;
pub const TAG_RECOVER: u64 = 17;
pub const TAG_NK_FIELD: u64 = 18;
pub const TAG_NK_CANCEL: u64 = 19;

/// The revoked set's tree depth (an indexed Merkle tree over H, leaves [key, next_key, 0, 0]).
pub const REVOKED_DEPTH: u8 = 16;

/// The policy floors (the spec's section 3.1), checked in the circuit at creation and at every change.
pub const FLOOR_DELAY_BLOCKS: u64 = 300;
pub const FLOOR_DELAY_SECONDS: u64 = 3600;

pub const PRELUDE_MASM: &str = include_str!("../masm/vault-prelude.masm");
pub const CREATE_MASM: &str = include_str!("../masm/vault-create.masm");
pub const WITHDRAW_MASM: &str = include_str!("../masm/vault-withdraw.masm");
pub const CANCEL_MASM: &str = include_str!("../masm/vault-cancel.masm");
pub const COMPLETE_MASM: &str = include_str!("../masm/vault-complete.masm");
pub const ANNOUNCE_MASM: &str = include_str!("../masm/vault-announce.masm");
pub const POLICY_COMPLETE_MASM: &str = include_str!("../masm/vault-policy-complete.masm");
pub const REC_CANCEL_MASM: &str = include_str!("../masm/vault-rec-cancel.masm");

/// The assembly of a vault statement: the prelude and the body, one text.
pub fn source(kind: u64) -> Option<String> {
    let body = match kind {
        KIND_CREATE => CREATE_MASM,
        KIND_WITHDRAW => WITHDRAW_MASM,
        KIND_CANCEL => CANCEL_MASM,
        KIND_COMPLETE => COMPLETE_MASM,
        KIND_ANNOUNCE => ANNOUNCE_MASM,
        KIND_POLICY_COMPLETE => POLICY_COMPLETE_MASM,
        KIND_REC_CANCEL => REC_CANCEL_MASM,
        _ => return None,
    };
    Some(join_source(PRELUDE_MASM, body))
}

/// The one joining rule (the verifier library applies the same).
pub fn join_source(prelude: &str, body: &str) -> String {
    format!("{prelude}\n{body}")
}

pub fn tag(t: u64) -> Word {
    word([t, 0, 0, 0])
}

/// pk_c = H(sk_c, TAG 16).
pub fn pk_cancel(sk_c: Word) -> Word {
    merge(sk_c, tag(TAG_CANCEL))
}

/// pk_a = H(sk_a, TAG 17).
pub fn pk_recover(sk_a: Word) -> Word {
    merge(sk_a, tag(TAG_RECOVER))
}

/// nk_field = H(nk_v, TAG 18).
pub fn nk_field(nk_v: Word) -> Word {
    merge(nk_v, tag(TAG_NK_FIELD))
}

/// nk_c = H(sk_c, TAG 19): the cancellation key's nullifier key.
pub fn nk_cancel(sk_c: Word) -> Word {
    merge(sk_c, tag(TAG_NK_CANCEL))
}

/// The policy as the circuit reads it.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Policy {
    pub pk_e: Word,
    pub pk_c: Word,
    pub pk_a: Word,
    pub nk_field: Word,
    pub delay_blocks: u64,
    pub delay_seconds: u64,
    pub period: u64,
    pub limit: u128,
    pub recovery_blocks: u64,
    pub recovery_seconds: u64,
    pub nonce: Word,
}

impl Policy {
    /// The eight words, in the hash's order.
    pub fn words(&self) -> [Word; 8] {
        [
            self.pk_e,
            self.pk_c,
            self.pk_a,
            self.nk_field,
            word([self.delay_blocks, self.delay_seconds, self.period, 0]),
            value_word(self.limit),
            word([self.recovery_blocks, self.recovery_seconds, 0, 0]),
            self.nonce,
        ]
    }

    /// policy_hash = fold(TAG 11; the eight words).
    pub fn hash(&self) -> Word {
        let mut ws = vec![tag(TAG_POLICY)];
        ws.extend(self.words());
        chain(&ws)
    }

    /// The floors the circuit checks.
    pub fn check_floors(&self) -> Result<(), String> {
        if self.delay_blocks < FLOOR_DELAY_BLOCKS || self.delay_seconds < FLOOR_DELAY_SECONDS {
            return Err(format!("the delay is below the floor ({FLOOR_DELAY_BLOCKS} blocks, {FLOOR_DELAY_SECONDS} seconds)"));
        }
        if self.period < self.delay_blocks {
            return Err("the period is shorter than the delay".into());
        }
        if self.recovery_blocks < 2 * self.delay_blocks || self.recovery_seconds < 2 * self.delay_seconds {
            return Err("the recovery delay is below twice the withdrawal delay".into());
        }
        for v in [self.delay_blocks, self.delay_seconds, self.period, self.recovery_blocks, self.recovery_seconds] {
            if v >= 1 << 32 {
                return Err("a policy field is past 32 bits".into());
            }
        }
        Ok(())
    }
}

/// The head as the circuit reads it.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Head {
    pub value: u128,
    pub vid: Word,
    pub policy_hash: Word,
    pub period_idx: u64,
    pub rec_h: u64,
    pub rec_t: u64,
    pub spent: u128,
    pub rec_hash: Word,
    pub r: Word,
    /// The co-signature bit (F-800): 1 when the everyday key co-signed the pending announcement, so a
    /// rotation of the cancellation key is the recovery authority's alone to cancel; zero outside a recovery.
    pub co: u64,
}

impl Head {
    pub fn words(&self) -> [Word; 7] {
        [value_word(self.value), self.vid, self.policy_hash, word([self.period_idx, self.rec_h, self.rec_t, self.co]), value_word(self.spent), self.rec_hash, self.r]
    }

    /// cm_head = fold(TAG 12; the seven words).
    pub fn commitment(&self) -> Word {
        let mut ws = vec![tag(TAG_HEAD)];
        ws.extend(self.words());
        chain(&ws)
    }
}

/// The pending note as the circuit reads it.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Pending {
    pub value: u128,
    pub vid: Word,
    pub policy_hash: Word,
    pub pk_dest: Word,
    pub h_req: u64,
    pub t_req: u64,
    pub delay_blocks: u64,
    pub delay_seconds: u64,
    pub r: Word,
}

impl Pending {
    pub fn words(&self) -> [Word; 6] {
        [value_word(self.value), self.vid, self.policy_hash, self.pk_dest, word([self.h_req, self.t_req, self.delay_blocks, self.delay_seconds]), self.r]
    }

    /// cm_pending = fold(TAG 13; the six words).
    pub fn commitment(&self) -> Word {
        let mut ws = vec![tag(TAG_PENDING)];
        ws.extend(self.words());
        chain(&ws)
    }

    /// ptag = fold(TAG 15; cm, [position, 0, 0, 0], r).
    pub fn ptag(&self, position: u64) -> Word {
        ptag(self.commitment(), position, self.r)
    }
}

pub fn ptag(cm: Word, position: u64, r: Word) -> Word {
    chain(&[tag(TAG_PTAG), cm, word([position, 0, 0, 0]), r])
}

/// cm_inbound = fold(TAG 14; w, vid, policy_hash, r).
pub fn inbound_commitment(value: u128, vid: Word, policy_hash: Word, r: Word) -> Word {
    chain(&[tag(TAG_INBOUND), value_word(value), vid, policy_hash, r])
}

/// The revoked set's leaf: [key, next_key, 0, 0] (the empty leaf is the sentinel: key 0, no upper neighbour).
pub fn revoked_leaf(key: u64, next: u64) -> Word {
    word([key, next, 0, 0])
}

/// The revoked set's key of a policy hash: its first element.
pub fn revoked_key(policy_hash: Word) -> u64 {
    ints(policy_hash)[0]
}

// The witness form (JSON; 32-byte fields hex of the word bytes; values decimal strings; heights and times
// numbers). One struct for the seven statements; each reads the fields it names.

#[derive(Serialize, Deserialize, Clone, Debug, Default)]
pub struct PolicyWitness {
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

impl PolicyWitness {
    pub fn parse(&self) -> Result<Policy, String> {
        Ok(Policy {
            pk_e: hex32(&self.pk_e, "policy pk_e")?,
            pk_c: hex32(&self.pk_c, "policy pk_c")?,
            pk_a: hex32(&self.pk_a, "policy pk_a")?,
            nk_field: hex32(&self.nk_field, "policy nk_field")?,
            delay_blocks: self.delay_blocks,
            delay_seconds: self.delay_seconds,
            period: self.period,
            limit: u128s(&self.limit, "policy limit")?,
            recovery_blocks: self.recovery_blocks,
            recovery_seconds: self.recovery_seconds,
            nonce: hex32(&self.nonce, "policy nonce")?,
        })
    }
}

#[derive(Serialize, Deserialize, Clone, Debug, Default)]
pub struct HeadWitness {
    pub value: String,
    pub vid: String,
    pub period_idx: u64,
    pub rec_h: u64,
    pub rec_t: u64,
    pub spent: String,
    pub rec_hash: String,
    pub r: String,
    /// The co-signature bit of the pending announcement (F-800).
    #[serde(default)]
    pub co: u64,
    pub index: u64,
    /// The 32 siblings from the leaf up, under the anchor.
    pub path: Vec<String>,
}

#[derive(Serialize, Deserialize, Clone, Debug, Default)]
pub struct PendingWitness {
    pub value: String,
    pub vid: String,
    pub policy_hash: String,
    pub pk_dest: String,
    pub h_req: u64,
    pub t_req: u64,
    pub delay_blocks: u64,
    pub delay_seconds: u64,
    pub r: String,
    pub index: u64,
    pub path: Vec<String>,
}

#[derive(Serialize, Deserialize, Clone, Debug, Default)]
pub struct RevokedWitness {
    pub low_key: u64,
    pub low_next: u64,
    pub low_index: u64,
    /// The 16 siblings from the leaf up, under the revoked root.
    pub path: Vec<String>,
}

#[derive(Serialize, Deserialize, Clone, Debug, Default)]
pub struct VaultWitness {
    pub statement: String,
    pub chain_id: u64,
    pub anchor: String,
    /// The public nullifiers in the statement's order (create: the two inputs'; the others: the head's or the
    /// pending note's).
    #[serde(default)]
    pub nullifiers: Vec<String>,
    /// The public output commitments in the statement's order: create [head, change]; withdraw [head_out,
    /// pending]; cancel [inbound]; complete [out 1, out 2]; announce, policy-complete, rec-cancel [head_out].
    #[serde(default)]
    pub commitments: Vec<String>,
    #[serde(default)]
    pub ptag: String,
    #[serde(default)]
    pub revoked_root: String,
    #[serde(default)]
    pub old_policy_hash: String,
    /// h_decl (withdraw, announce) or h_now (complete, policy-complete), and the time beside it.
    #[serde(default)]
    pub height: u64,
    #[serde(default)]
    pub time: u64,
    #[serde(default)]
    pub policy: Option<PolicyWitness>,
    #[serde(default)]
    pub new_policy: Option<PolicyWitness>,
    #[serde(default)]
    pub nk_v: String,
    #[serde(default)]
    pub sk_e: String,
    #[serde(default)]
    pub nk_e: String,
    #[serde(default)]
    pub sk_c: String,
    #[serde(default)]
    pub sk_a: String,
    #[serde(default)]
    pub sk_dest: String,
    #[serde(default)]
    pub nk_dest: String,
    #[serde(default)]
    pub head_in: Option<HeadWitness>,
    #[serde(default)]
    pub head_out_vid: String,
    #[serde(default)]
    pub head_out_r: String,
    #[serde(default)]
    pub pending_in: Option<PendingWitness>,
    #[serde(default)]
    pub pending_out_value: String,
    #[serde(default)]
    pub pending_out_pk_dest: String,
    #[serde(default)]
    pub pending_out_r: String,
    #[serde(default)]
    pub inbound_r: String,
    /// create: the two ordinary inputs (stage one's form).
    #[serde(default)]
    pub inputs: Vec<InputWitness>,
    /// create: the change output; complete: the two ordinary outputs.
    #[serde(default)]
    pub outputs: Vec<OutputWitness>,
    #[serde(default)]
    pub revoked: Option<RevokedWitness>,
}

struct Layout {
    adv: Vec<u64>,
    store: MerkleStore,
}

impl Layout {
    fn word(&mut self, w: Word) {
        self.adv.extend(ints(w));
    }
    fn words(&mut self, ws: &[Word]) {
        for w in ws {
            self.word(*w);
        }
    }
    fn path(&mut self, index: u64, leaf: Word, path: &[String], depth: u8, root: Word, what: &str) -> Result<(), String> {
        if path.len() != depth as usize {
            return Err(format!("{what}: the path is not {depth} siblings"));
        }
        if index >= 1u64 << depth {
            return Err(format!("{what}: the index is past the tree"));
        }
        let mut sib = Vec::new();
        for s in path {
            sib.push(hex32(s, "sibling")?);
        }
        let got = self.store.add_merkle_path(index, leaf, MerklePath::new(sib)).map_err(|e| format!("{what}: {e:?}"))?;
        if got != root {
            return Err(format!("{what}: the path does not reach the root"));
        }
        Ok(())
    }
}

fn hexw(s: &str, what: &str) -> Result<Word, String> {
    hex32(s, what)
}

fn list(v: &[String], n: usize, what: &str) -> Result<Vec<Word>, String> {
    if v.len() != n {
        return Err(format!("{what}: {} given, {n} needed", v.len()));
    }
    v.iter().map(|s| hexw(s, what)).collect()
}

fn head_of(h: &HeadWitness, policy_hash: Word) -> Result<Head, String> {
    Ok(Head {
        value: u128s(&h.value, "head value")?,
        vid: hexw(&h.vid, "head vid")?,
        policy_hash,
        period_idx: h.period_idx,
        rec_h: h.rec_h,
        rec_t: h.rec_t,
        spent: u128s(&h.spent, "head spent")?,
        rec_hash: hexw(&h.rec_hash, "head rec_hash")?,
        r: hexw(&h.r, "head r")?,
        co: h.co,
    })
}

fn pending_of(p: &PendingWitness, policy_hash: Word) -> Result<Pending, String> {
    Ok(Pending {
        value: u128s(&p.value, "pending value")?,
        vid: hexw(&p.vid, "pending vid")?,
        policy_hash,
        pk_dest: hexw(&p.pk_dest, "pending pk_dest")?,
        h_req: p.h_req,
        t_req: p.t_req,
        delay_blocks: p.delay_blocks,
        delay_seconds: p.delay_seconds,
        r: hexw(&p.r, "pending r")?,
    })
}

/// The head's advice words in the circuit's read order (V, VID, IDX, SPENT, RECH, R, then the position).
fn push_head(l: &mut Layout, h: &Head, index: u64) {
    l.word(value_word(h.value));
    l.word(h.vid);
    l.word(word([h.period_idx, h.rec_h, h.rec_t, h.co]));
    l.word(value_word(h.spent));
    l.word(h.rec_hash);
    l.word(h.r);
    l.adv.push(index);
}

/// The pending note's advice words (W, VID, PKD, HT, R, then the position).
fn push_pending(l: &mut Layout, p: &Pending, index: u64) {
    l.word(value_word(p.value));
    l.word(p.vid);
    l.word(p.pk_dest);
    l.word(word([p.h_req, p.t_req, p.delay_blocks, p.delay_seconds]));
    l.word(p.r);
    l.adv.push(index);
}

/// Checks the witness on the host (what the circuit will check), lays out the advice and returns the
/// statement ready to execute and prove.
pub fn prepare_vault(w: &VaultWitness) -> Result<Prepared, String> {
    let kind = kind_of(&w.statement).ok_or_else(|| format!("statement {} is not a vault statement", w.statement))?;
    let anchor = hexw(&w.anchor, "anchor")?;
    let mut l = Layout { adv: Vec::new(), store: MerkleStore::new() };
    let policy = w.policy.as_ref().map(|p| p.parse()).transpose()?;
    let new_policy = w.new_policy.as_ref().map(|p| p.parse()).transpose()?;
    let nk_v = if w.nk_v.is_empty() { None } else { Some(hexw(&w.nk_v, "nk_v")?) };
    if let (Some(p), Some(nk)) = (&policy, nk_v) {
        if kind != KIND_COMPLETE && p.nk_field != nk_field(nk) {
            return Err("nk_v does not open the policy's nk_field".into());
        }
    }
    let ht = word([w.height, w.time, 0, 0]);
    if w.height >= 1 << 32 || w.time >= 1 << 32 {
        return Err("the height or the time is past 32 bits".into());
    }
    let public: Vec<Word>;
    match kind {
        KIND_CREATE => {
            let policy = policy.ok_or("create: no policy")?;
            policy.check_floors()?;
            let nk = nk_v.ok_or("create: no nk_v")?;
            let nfs = list(&w.nullifiers, 2, "nullifiers")?;
            let cms = list(&w.commitments, 2, "commitments")?;
            if w.inputs.len() != 2 || w.outputs.len() != 1 {
                return Err("create takes two inputs and one change output".into());
            }
            public = vec![domain_word(w.chain_id, kind), anchor, nfs[0], nfs[1], cms[0], cms[1]];
            l.words(&public);
            let mut sum_in: u128 = 0;
            let mut pk0 = None;
            for (i, inp) in w.inputs.iter().enumerate() {
                let v = u128s(&inp.value, "input value")?;
                let sk = hexw(&inp.sk_spend, "sk_spend")?;
                let nk = hexw(&inp.nk, "nk")?;
                let r = hexw(&inp.r, "r")?;
                let cm = commitment(v, pk(sk, nk), r);
                if nullifier(nk, cm, inp.index) != nfs[i] {
                    return Err(format!("input {i}'s nullifier is not the statement's"));
                }
                if v != 0 {
                    l.path(inp.index, cm, &inp.path, DEPTH, anchor, &format!("input {i}"))?;
                }
                if i == 0 {
                    pk0 = Some(pk(sk, nk));
                }
                sum_in = sum_in.checked_add(v).ok_or("the inputs overflow")?;
                l.word(value_word(v));
                l.word(sk);
                l.word(nk);
                l.word(r);
                l.adv.push(inp.index);
            }
            if pk0 != Some(policy.pk_e) {
                return Err("the policy's pk_e is not input 0's key".into());
            }
            l.words(&policy.words());
            l.word(nk);
            let out = &w.outputs[0];
            let cv = u128s(&out.value, "change value")?;
            let opk = hexw(&out.pk, "change pk")?;
            let or = hexw(&out.r, "change r")?;
            if commitment(cv, opk, or) != cms[1] {
                return Err("the change commitment is not the statement's".into());
            }
            l.word(value_word(cv));
            l.word(opk);
            l.word(or);
            let v_head = sum_in.checked_sub(cv).ok_or("the change exceeds the inputs")?;
            let head = Head {
                value: v_head,
                vid: hexw(&w.head_out_vid, "head vid")?,
                policy_hash: policy.hash(),
                period_idx: 0,
                rec_h: 0,
                rec_t: 0,
                spent: 0,
                rec_hash: Word::default(),
                r: hexw(&w.head_out_r, "head r")?,
                co: 0,
            };
            if head.commitment() != cms[0] {
                return Err("the head commitment is not the statement's".into());
            }
            l.word(head.vid);
            l.word(head.r);
        }
        KIND_WITHDRAW => {
            let policy = policy.ok_or("withdraw: no policy")?;
            let nk = nk_v.ok_or("withdraw: no nk_v")?;
            let nfs = list(&w.nullifiers, 1, "nullifiers")?;
            let cms = list(&w.commitments, 2, "commitments")?;
            let hw = w.head_in.as_ref().ok_or("withdraw: no head_in")?;
            let head = head_of(hw, policy.hash())?;
            public = vec![domain_word(w.chain_id, kind), anchor, nfs[0], cms[0], cms[1], ht];
            l.words(&public);
            l.words(&policy.words());
            let sk_e = hexw(&w.sk_e, "sk_e")?;
            let nk_e = hexw(&w.nk_e, "nk_e")?;
            if pk(sk_e, nk_e) != policy.pk_e {
                return Err("sk_e and nk_e do not open the policy's pk_e".into());
            }
            l.word(sk_e);
            l.word(nk_e);
            l.word(nk);
            let cm = head.commitment();
            l.path(hw.index, cm, &hw.path, DEPTH, anchor, "head")?;
            if nullifier(nk, cm, hw.index) != nfs[0] {
                return Err("the head's nullifier is not the statement's".into());
            }
            if head.rec_hash != Word::default() {
                return Err("a recovery is pending: no withdrawal".into());
            }
            push_head(&mut l, &head, hw.index);
            let wv = u128s(&w.pending_out_value, "pending value")?;
            let pkd = hexw(&w.pending_out_pk_dest, "pending pk_dest")?;
            let pr = hexw(&w.pending_out_r, "pending r")?;
            l.word(value_word(wv));
            l.word(pkd);
            l.word(pr);
            if policy.period == 0 {
                return Err("the period is zero".into());
            }
            let idx = w.height / policy.period;
            if idx < head.period_idx {
                return Err(format!("the period index {idx} is below the head's {} (the index only advances)", head.period_idx));
            }
            let spent_new = if idx == head.period_idx { head.spent.checked_add(wv).ok_or("the running total overflows")? } else { wv };
            if spent_new > policy.limit {
                return Err(format!("the period's requests would be {spent_new}, past the limit {}", policy.limit));
            }
            if wv == 0 {
                return Err("a withdrawal of nothing".into());
            }
            if wv > head.value {
                return Err("the withdrawal exceeds the vault's balance".into());
            }
            let head_out = Head { value: head.value - wv, period_idx: idx, spent: spent_new, rec_h: 0, rec_t: 0, rec_hash: Word::default(), co: 0, r: hexw(&w.head_out_r, "head_out r")?, ..head.clone() };
            if head_out.commitment() != cms[0] {
                return Err("the head_out commitment is not the statement's".into());
            }
            let pending = Pending { value: wv, vid: head.vid, policy_hash: head.policy_hash, pk_dest: pkd, h_req: w.height, t_req: w.time, delay_blocks: policy.delay_blocks, delay_seconds: policy.delay_seconds, r: pr };
            if pending.commitment() != cms[1] {
                return Err("the pending commitment is not the statement's".into());
            }
            l.word(head_out.r);
        }
        KIND_CANCEL => {
            let policy = policy.ok_or("cancel: no policy")?;
            let nfs = list(&w.nullifiers, 1, "nullifiers")?;
            let cms = list(&w.commitments, 1, "commitments")?;
            let ptagw = hexw(&w.ptag, "ptag")?;
            let pw = w.pending_in.as_ref().ok_or("cancel: no pending_in")?;
            let pending = pending_of(pw, policy.hash())?;
            public = vec![domain_word(w.chain_id, kind), anchor, nfs[0], ptagw, cms[0]];
            l.words(&public);
            l.words(&policy.words());
            let sk_c = hexw(&w.sk_c, "sk_c")?;
            if pk_cancel(sk_c) != policy.pk_c || policy.pk_c == Word::default() {
                return Err("sk_c does not open the policy's cancellation key".into());
            }
            l.word(sk_c);
            let cm = pending.commitment();
            l.path(pw.index, cm, &pw.path, DEPTH, anchor, "pending")?;
            if nullifier(nk_cancel(sk_c), cm, pw.index) != nfs[0] {
                return Err("the pending note's nullifier under nk_c is not the statement's".into());
            }
            if pending.ptag(pw.index) != ptagw {
                return Err("the position tag is not the statement's".into());
            }
            push_pending(&mut l, &pending, pw.index);
            let ir = hexw(&w.inbound_r, "inbound r")?;
            if inbound_commitment(pending.value, pending.vid, pending.policy_hash, ir) != cms[0] {
                return Err("the inbound commitment is not the statement's".into());
            }
            l.word(ir);
        }
        KIND_COMPLETE => {
            let nfs = list(&w.nullifiers, 1, "nullifiers")?;
            let cms = list(&w.commitments, 2, "commitments")?;
            let ptagw = hexw(&w.ptag, "ptag")?;
            let rev_root = hexw(&w.revoked_root, "revoked_root")?;
            let pw = w.pending_in.as_ref().ok_or("complete: no pending_in")?;
            let ph = hexw(&pw.policy_hash, "pending policy_hash")?;
            let pending = pending_of(pw, ph)?;
            if w.outputs.len() != 2 {
                return Err("complete takes two outputs".into());
            }
            public = vec![domain_word(w.chain_id, kind), anchor, rev_root, nfs[0], ptagw, cms[0], cms[1], ht];
            l.words(&public);
            let sk_d = hexw(&w.sk_dest, "sk_dest")?;
            let nk_d = hexw(&w.nk_dest, "nk_dest")?;
            if pk(sk_d, nk_d) != pending.pk_dest {
                return Err("sk_dest and nk_dest do not open the pending note's destination".into());
            }
            l.word(sk_d);
            l.word(nk_d);
            l.word(ph);
            let cm = pending.commitment();
            l.path(pw.index, cm, &pw.path, DEPTH, anchor, "pending")?;
            if nullifier(nk_d, cm, pw.index) != nfs[0] {
                return Err("the pending note's nullifier under nk_dest is not the statement's".into());
            }
            if pending.ptag(pw.index) != ptagw {
                return Err("the position tag is not the statement's".into());
            }
            if w.height < pending.h_req + pending.delay_blocks || w.time < pending.t_req + pending.delay_seconds {
                return Err(format!(
                    "not matured: height {} of {} and time {} of {}",
                    w.height,
                    pending.h_req + pending.delay_blocks,
                    w.time,
                    pending.t_req + pending.delay_seconds
                ));
            }
            push_pending(&mut l, &pending, pw.index);
            let mut sum_out: u128 = 0;
            for (j, out) in w.outputs.iter().enumerate() {
                let v = u128s(&out.value, "output value")?;
                let opk = hexw(&out.pk, "pk")?;
                let r = hexw(&out.r, "r")?;
                if commitment(v, opk, r) != cms[j] {
                    return Err(format!("output {j}'s commitment is not the statement's"));
                }
                sum_out = sum_out.checked_add(v).ok_or("the outputs overflow")?;
                l.word(value_word(v));
                l.word(opk);
                l.word(r);
            }
            if sum_out != pending.value {
                return Err(format!("the outputs ({sum_out}) do not equal the pending amount ({})", pending.value));
            }
            let rv = w.revoked.as_ref().ok_or("complete: no revoked witness")?;
            let k = revoked_key(ph);
            if !(rv.low_key < k && (rv.low_next == 0 || k < rv.low_next)) {
                return Err("the revoked witness does not show non-membership".into());
            }
            let leaf = revoked_leaf(rv.low_key, rv.low_next);
            l.path(rv.low_index, leaf, &rv.path, REVOKED_DEPTH, rev_root, "revoked")?;
            l.word(leaf);
            l.adv.push(rv.low_index);
        }
        KIND_ANNOUNCE => {
            let policy = policy.ok_or("announce: no policy")?;
            let newp = new_policy.ok_or("announce: no new policy")?;
            newp.check_floors()?;
            let nk = nk_v.ok_or("announce: no nk_v")?;
            let nfs = list(&w.nullifiers, 1, "nullifiers")?;
            let cms = list(&w.commitments, 1, "commitments")?;
            let old = hexw(&w.old_policy_hash, "old_policy_hash")?;
            if policy.hash() != old {
                return Err("the policy does not hash to the revealed old policy hash".into());
            }
            let hw = w.head_in.as_ref().ok_or("announce: no head_in")?;
            let head = head_of(hw, old)?;
            // F-799: the announcement reveals the old policy hash; the node revokes it in the announcing block.
            public = vec![domain_word(w.chain_id, kind), anchor, nfs[0], cms[0], old, ht];
            l.words(&public);
            l.words(&policy.words());
            let sk_a = hexw(&w.sk_a, "sk_a")?;
            if pk_recover(sk_a) != policy.pk_a || policy.pk_a == Word::default() {
                return Err("sk_a does not open the policy's recovery authority".into());
            }
            l.word(sk_a);
            l.word(nk);
            let cm = head.commitment();
            l.path(hw.index, cm, &hw.path, DEPTH, anchor, "head")?;
            if nullifier(nk, cm, hw.index) != nfs[0] {
                return Err("the head's nullifier is not the statement's".into());
            }
            if head.rec_hash != Word::default() {
                return Err("a recovery is already pending".into());
            }
            push_head(&mut l, &head, hw.index);
            // F-800: the everyday key's co-signature (sk_e and nk_e given): the bit the cancellation reads.
            let co = !w.sk_e.is_empty() || !w.nk_e.is_empty();
            l.adv.push(co as u64);
            if co {
                let sk_e = hexw(&w.sk_e, "sk_e")?;
                let nk_e = hexw(&w.nk_e, "nk_e")?;
                if pk(sk_e, nk_e) != policy.pk_e {
                    return Err("the co-signature's sk_e and nk_e do not open the policy's pk_e".into());
                }
                l.word(sk_e);
                l.word(nk_e);
            }
            l.words(&newp.words());
            let head_out = Head { rec_hash: newp.hash(), rec_h: w.height, rec_t: w.time, co: co as u64, r: hexw(&w.head_out_r, "head_out r")?, ..head.clone() };
            if head_out.commitment() != cms[0] {
                return Err("the head_out commitment is not the statement's".into());
            }
            l.word(head_out.r);
        }
        KIND_POLICY_COMPLETE => {
            let policy = policy.ok_or("policy-complete: no policy")?;
            let newp = new_policy.ok_or("policy-complete: no new policy")?;
            let nk = nk_v.ok_or("policy-complete: no nk_v")?;
            let nfs = list(&w.nullifiers, 1, "nullifiers")?;
            let cms = list(&w.commitments, 1, "commitments")?;
            let old = hexw(&w.old_policy_hash, "old_policy_hash")?;
            if policy.hash() != old {
                return Err("the policy does not hash to the revealed old policy hash".into());
            }
            let hw = w.head_in.as_ref().ok_or("policy-complete: no head_in")?;
            let head = head_of(hw, old)?;
            public = vec![domain_word(w.chain_id, kind), anchor, nfs[0], cms[0], old, ht];
            l.words(&public);
            l.words(&policy.words());
            l.word(nk);
            let cm = head.commitment();
            l.path(hw.index, cm, &hw.path, DEPTH, anchor, "head")?;
            if nullifier(nk, cm, hw.index) != nfs[0] {
                return Err("the head's nullifier is not the statement's".into());
            }
            if head.rec_hash != newp.hash() || head.rec_hash == Word::default() {
                return Err("the pending recovery is not the new policy".into());
            }
            if w.height < head.rec_h + policy.recovery_blocks || w.time < head.rec_t + policy.recovery_seconds {
                return Err("the recovery delay has not passed".into());
            }
            if newp.period == 0 {
                return Err("the new period is zero".into());
            }
            push_head(&mut l, &head, hw.index);
            l.words(&newp.words());
            let head_out = Head { policy_hash: newp.hash(), period_idx: w.height / newp.period, rec_h: 0, rec_t: 0, spent: 0, rec_hash: Word::default(), co: 0, r: hexw(&w.head_out_r, "head_out r")?, ..head.clone() };
            if head_out.commitment() != cms[0] {
                return Err("the head_out commitment is not the statement's".into());
            }
            l.word(head_out.r);
        }
        KIND_REC_CANCEL => {
            let policy = policy.ok_or("rec-cancel: no policy")?;
            let newp = new_policy.ok_or("rec-cancel: no new policy (the announced one)")?;
            let nk = nk_v.ok_or("rec-cancel: no nk_v")?;
            let nfs = list(&w.nullifiers, 1, "nullifiers")?;
            let cms = list(&w.commitments, 1, "commitments")?;
            let old = hexw(&w.old_policy_hash, "old_policy_hash")?;
            if policy.hash() != old {
                return Err("the policy does not hash to the revealed old policy hash".into());
            }
            let hw = w.head_in.as_ref().ok_or("rec-cancel: no head_in")?;
            let head = head_of(hw, old)?;
            if head.rec_hash == Word::default() {
                return Err("no recovery is pending".into());
            }
            if newp.hash() != head.rec_hash {
                return Err("the pending recovery is not the new policy".into());
            }
            if head.co > 1 {
                return Err("the head's co-signature bit is not a bit".into());
            }
            // F-800: a recovery that changes the cancellation key AND was co-signed by the everyday key is
            // cancellable by the recovery authority alone; every other recovery by the cancellation key
            // alone (the circuit's branch on the announced policy's pk_c against the policy's and the head's
            // co-signature bit).
            let sk = if newp.pk_c != policy.pk_c && head.co == 1 {
                let sk_a = hexw(&w.sk_a, "sk_a").map_err(|e| format!("the recovery changes the cancellation key: only the recovery authority can cancel it ({e})"))?;
                if pk_recover(sk_a) != policy.pk_a || policy.pk_a == Word::default() {
                    return Err("the recovery changes the cancellation key: only the recovery authority can cancel it (sk_a does not open the policy's recovery authority)".into());
                }
                sk_a
            } else {
                let sk_c = hexw(&w.sk_c, "sk_c")?;
                if pk_cancel(sk_c) != policy.pk_c || policy.pk_c == Word::default() {
                    return Err("sk_c does not open the policy's cancellation key".into());
                }
                sk_c
            };
            // F-799: the cancellation reveals the hash the announcement revealed; the node restores it.
            public = vec![domain_word(w.chain_id, kind), anchor, nfs[0], cms[0], old];
            l.words(&public);
            l.words(&policy.words());
            l.word(sk);
            l.word(nk);
            let cm = head.commitment();
            l.path(hw.index, cm, &hw.path, DEPTH, anchor, "head")?;
            if nullifier(nk, cm, hw.index) != nfs[0] {
                return Err("the head's nullifier is not the statement's".into());
            }
            push_head(&mut l, &head, hw.index);
            l.words(&newp.words());
            let head_out = Head { rec_h: 0, rec_t: 0, rec_hash: Word::default(), co: 0, r: hexw(&w.head_out_r, "head_out r")?, ..head.clone() };
            if head_out.commitment() != cms[0] {
                return Err("the head_out commitment is not the statement's".into());
            }
            l.word(head_out.r);
        }
        _ => unreachable!(),
    }
    let sh = chain(&public);
    let advice = AdviceInputs::default()
        .with_stack(AdviceStack::try_from_values(l.adv).map_err(|e| format!("{e:?}"))?)
        .with_merkle_store(l.store);
    Ok(Prepared { kind, statement_hash: sh, stack_inputs: stack_for(sh), advice })
}

/// The statement hash of a vault record from its public words (the node's fold; the record's own order).
pub fn statement_hash(public: &[Word]) -> Word {
    chain(public)
}
