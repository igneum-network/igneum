//! Money stage 1, cut 4: the two statements (money-shield-v1, money-spend-v1) as the prover, the vectors
//! tool and the lab share them: the word encodings, H (Poseidon2 over Goldilocks, the VM's native hash,
//! every use a two-to-one merge), the statement hashes, the witness forms and the proving call. The node
//! (Go, consensus/igneum/pool) and the wallet (Rust, wallet/money) carry their own implementations of
//! the same definitions and all three read tools/money/vectors/money-stage1-vectors.json.
use std::sync::OnceLock;

use miden_assembly::Assembler;
use miden_core::{Felt, Word, program::Program};
use miden_crypto::hash::poseidon2::Poseidon2;
use miden_crypto::merkle::{MerklePath, store::MerkleStore};
use miden_vm::{
    DefaultHost, ExecutionOptions, FastProcessor, HashFunction, Prover, StackInputs,
    advice::{AdviceInputs, AdviceStack},
};
use serde::{Deserialize, Serialize};

pub mod vault;

/// The Goldilocks prime.
pub const P: u64 = 0xFFFF_FFFF_0000_0001;
/// The commitment tree's depth (the ADR's section 4.3 item 2).
pub const DEPTH: u8 = 32;
/// The statement kinds (the second element of the domain word).
pub const KIND_SHIELD: u64 = 1;
pub const KIND_SPEND: u64 = 2;
/// The statements' Miden assembly, the source of record.
pub const SPEND_MASM: &str = include_str!("../masm/spend.masm");
pub const SHIELD_MASM: &str = include_str!("../masm/shield.masm");

pub fn word(v: [u64; 4]) -> Word {
    Word::new(v.map(Felt::new_unchecked))
}

pub fn ints(w: Word) -> [u64; 4] {
    let e = w.as_elements();
    [e[0], e[1], e[2], e[3]].map(|f| f.as_canonical_u64())
}

/// A word's 32 bytes: each element canonical, little-endian, in order (miden Word::as_bytes).
pub fn word_to_bytes(w: Word) -> [u8; 32] {
    w.as_bytes()
}

/// The inverse; every element must be canonical (below the prime).
pub fn word_from_bytes(b: &[u8; 32]) -> Result<Word, String> {
    let mut v = [0u64; 4];
    for i in 0..4 {
        v[i] = u64::from_le_bytes(b[8 * i..8 * i + 8].try_into().unwrap());
        if v[i] >= P {
            return Err(format!("element {i} is not a field element"));
        }
    }
    Ok(word(v))
}

/// A 128-bit value as four 32-bit limbs, limb 0 the least significant.
pub fn value_word(v: u128) -> Word {
    word([
        (v & 0xffff_ffff) as u64,
        ((v >> 32) & 0xffff_ffff) as u64,
        ((v >> 64) & 0xffff_ffff) as u64,
        ((v >> 96) & 0xffff_ffff) as u64,
    ])
}

/// A 20-byte address as four 40-bit limbs, little-endian, in order.
pub fn address_word(a: &[u8; 20]) -> Word {
    let mut v = [0u64; 4];
    for i in 0..4 {
        let mut b = [0u8; 8];
        b[..5].copy_from_slice(&a[5 * i..5 * i + 5]);
        v[i] = u64::from_le_bytes(b);
    }
    word(v)
}

/// The domain word: the chain id and the statement kind.
pub fn domain_word(chain_id: u64, kind: u64) -> Word {
    word([chain_id, kind, 0, 0])
}

/// H(a, b).
pub fn merge(a: Word, b: Word) -> Word {
    Poseidon2::merge(&[a, b])
}

/// H(...H(H(w0, w1), w2)..., wn).
pub fn chain(words: &[Word]) -> Word {
    let mut acc = words[0];
    for w in &words[1..] {
        acc = merge(acc, *w);
    }
    acc
}

/// pk = H(sk_spend, nk).
pub fn pk(sk: Word, nk: Word) -> Word {
    merge(sk, nk)
}

/// cm = H(H(v, pk), r).
pub fn commitment(value: u128, pk: Word, r: Word) -> Word {
    merge(merge(value_word(value), pk), r)
}

/// nf = H(H(nk, cm), [index, 0, 0, 0]).
pub fn nullifier(nk: Word, cm: Word, index: u64) -> Word {
    merge(merge(nk, cm), word([index, 0, 0, 0]))
}

/// The spend statement's hash.
pub fn spend_statement_hash(chain_id: u64, anchor: Word, nfs: [Word; 2], cms: [Word; 2], vpub: u128, fee: u128, addr: &[u8; 20]) -> Word {
    chain(&[
        domain_word(chain_id, KIND_SPEND),
        anchor,
        nfs[0],
        nfs[1],
        cms[0],
        cms[1],
        value_word(vpub),
        value_word(fee),
        address_word(addr),
    ])
}

/// The shield statement's hash.
pub fn shield_statement_hash(chain_id: u64, cm: Word, value: u128) -> Word {
    chain(&[domain_word(chain_id, KIND_SHIELD), cm, value_word(value)])
}

// The witness forms (JSON; 32-byte fields hex of the word bytes; values decimal strings).

#[derive(Serialize, Deserialize, Clone, Debug)]
pub struct InputWitness {
    pub value: String,
    pub sk_spend: String,
    pub nk: String,
    pub r: String,
    pub index: u64,
    /// The 32 siblings from the leaf up; empty for a dummy input (value zero).
    pub path: Vec<String>,
}

#[derive(Serialize, Deserialize, Clone, Debug)]
pub struct OutputWitness {
    pub value: String,
    pub pk: String,
    pub r: String,
}

#[derive(Serialize, Deserialize, Clone, Debug)]
pub struct SpendWitness {
    pub statement: String,
    pub chain_id: u64,
    pub anchor: String,
    pub nullifiers: Vec<String>,
    pub commitments: Vec<String>,
    pub public_value: String,
    pub fee: String,
    pub public_output: String,
    pub inputs: Vec<InputWitness>,
    pub outputs: Vec<OutputWitness>,
}

#[derive(Serialize, Deserialize, Clone, Debug)]
pub struct ShieldWitness {
    pub statement: String,
    pub chain_id: u64,
    pub commitment: String,
    pub value: String,
    pub pk: String,
    pub r: String,
}

pub fn hex32(s: &str, what: &str) -> Result<Word, String> {
    let b = hex::decode(s.trim_start_matches("0x")).map_err(|e| format!("{what}: {e}"))?;
    let a: [u8; 32] = b.try_into().map_err(|_| format!("{what}: not 32 bytes"))?;
    word_from_bytes(&a).map_err(|e| format!("{what}: {e}"))
}

pub fn hex20(s: &str, what: &str) -> Result<[u8; 20], String> {
    let b = hex::decode(s.trim_start_matches("0x")).map_err(|e| format!("{what}: {e}"))?;
    b.try_into().map_err(|_| format!("{what}: not 20 bytes"))
}

pub fn u128s(s: &str, what: &str) -> Result<u128, String> {
    s.parse::<u128>().map_err(|e| format!("{what}: {e}"))
}

/// A statement ready to execute and prove.
pub struct Prepared {
    pub kind: u64,
    pub statement_hash: Word,
    pub stack_inputs: StackInputs,
    pub advice: AdviceInputs,
}

fn push_word(adv: &mut Vec<u64>, w: Word) {
    adv.extend(ints(w));
}

/// Checks the spend witness on the host (what the circuit will check) and lays out the advice.
pub fn prepare_spend(w: &SpendWitness) -> Result<Prepared, String> {
    if w.statement != "money-spend-v1" {
        return Err(format!("statement {} is not money-spend-v1", w.statement));
    }
    if w.inputs.len() != 2 || w.outputs.len() != 2 || w.nullifiers.len() != 2 || w.commitments.len() != 2 {
        return Err("the statement takes two inputs and two outputs".into());
    }
    let anchor = hex32(&w.anchor, "anchor")?;
    let vpub = u128s(&w.public_value, "public_value")?;
    let fee = u128s(&w.fee, "fee")?;
    let addr = hex20(&w.public_output, "public_output")?;
    let mut nfs = [Word::default(); 2];
    let mut cms = [Word::default(); 2];
    for i in 0..2 {
        nfs[i] = hex32(&w.nullifiers[i], "nullifier")?;
        cms[i] = hex32(&w.commitments[i], "commitment")?;
    }
    let mut adv: Vec<u64> = Vec::new();
    for pw in [domain_word(w.chain_id, KIND_SPEND), anchor, nfs[0], nfs[1], cms[0], cms[1], value_word(vpub), value_word(fee), address_word(&addr)] {
        push_word(&mut adv, pw);
    }
    let mut store = MerkleStore::new();
    let mut sum_in: u128 = 0;
    for (i, inp) in w.inputs.iter().enumerate() {
        let v = u128s(&inp.value, "input value")?;
        let sk = hex32(&inp.sk_spend, "sk_spend")?;
        let nk = hex32(&inp.nk, "nk")?;
        let r = hex32(&inp.r, "r")?;
        let cm = commitment(v, pk(sk, nk), r);
        let nf = nullifier(nk, cm, inp.index);
        if nf != nfs[i] {
            return Err(format!("input {i}'s nullifier is not the statement's"));
        }
        if v != 0 {
            if inp.path.len() != DEPTH as usize {
                return Err(format!("input {i}: the path is not {DEPTH} siblings"));
            }
            if inp.index >= 1u64 << DEPTH {
                return Err(format!("input {i}: the index is past the tree"));
            }
            let mut sib = Vec::new();
            for s in &inp.path {
                sib.push(hex32(s, "sibling")?);
            }
            let root = store.add_merkle_path(inp.index, cm, MerklePath::new(sib)).map_err(|e| format!("input {i}: {e:?}"))?;
            if root != anchor {
                return Err(format!("input {i}'s path does not reach the anchor"));
            }
        }
        sum_in = sum_in.checked_add(v).ok_or("the inputs overflow")?;
        push_word(&mut adv, value_word(v));
        push_word(&mut adv, sk);
        push_word(&mut adv, nk);
        push_word(&mut adv, r);
        adv.push(inp.index);
    }
    let mut sum_out: u128 = vpub.checked_add(fee).ok_or("the public side overflows")?;
    for (j, out) in w.outputs.iter().enumerate() {
        let v = u128s(&out.value, "output value")?;
        let opk = hex32(&out.pk, "pk")?;
        let r = hex32(&out.r, "r")?;
        if commitment(v, opk, r) != cms[j] {
            return Err(format!("output {j}'s commitment is not the statement's"));
        }
        sum_out = sum_out.checked_add(v).ok_or("the outputs overflow")?;
        push_word(&mut adv, value_word(v));
        push_word(&mut adv, opk);
        push_word(&mut adv, r);
    }
    if sum_in != sum_out {
        return Err(format!("the values do not balance: in {sum_in}, out {sum_out}"));
    }
    let sh = spend_statement_hash(w.chain_id, anchor, nfs, cms, vpub, fee, &addr);
    let advice = AdviceInputs::default()
        .with_stack(AdviceStack::try_from_values(adv).map_err(|e| format!("{e:?}"))?)
        .with_merkle_store(store);
    Ok(Prepared { kind: KIND_SPEND, statement_hash: sh, stack_inputs: stack_for(sh), advice })
}

/// Checks the shield witness on the host and lays out the advice.
pub fn prepare_shield(w: &ShieldWitness) -> Result<Prepared, String> {
    if w.statement != "money-shield-v1" {
        return Err(format!("statement {} is not money-shield-v1", w.statement));
    }
    let v = u128s(&w.value, "value")?;
    let cm = hex32(&w.commitment, "commitment")?;
    let opk = hex32(&w.pk, "pk")?;
    let r = hex32(&w.r, "r")?;
    if commitment(v, opk, r) != cm {
        return Err("the commitment is not H(H(v, pk), r)".into());
    }
    let mut adv: Vec<u64> = Vec::new();
    for pw in [domain_word(w.chain_id, KIND_SHIELD), cm, value_word(v), value_word(v), opk, r] {
        push_word(&mut adv, pw);
    }
    let sh = shield_statement_hash(w.chain_id, cm, v);
    let advice = AdviceInputs::default().with_stack(AdviceStack::try_from_values(adv).map_err(|e| format!("{e:?}"))?);
    Ok(Prepared { kind: KIND_SHIELD, statement_hash: sh, stack_inputs: stack_for(sh), advice })
}

pub fn stack_for(sh: Word) -> StackInputs {
    StackInputs::new(&ints(sh).map(Felt::new_unchecked)).expect("stack inputs")
}

/// The claim's sixteen stack outputs: the statement hash carried through, zeros below.
pub fn claim_outputs(sh: Word) -> [u64; 16] {
    let mut o = [0u64; 16];
    o[..4].copy_from_slice(&ints(sh));
    o
}

static SPEND: OnceLock<Program> = OnceLock::new();
static SHIELD: OnceLock<Program> = OnceLock::new();
static VAULT: [OnceLock<Program>; 7] = [const { OnceLock::new() }; 7];

fn assemble(name: &str, src: &str) -> Program {
    Assembler::default().assemble_program(name, src).expect("assemble").unwrap_program()
}

/// The statement's program (assembled once): the two of stage one and the vault's seven (vault.rs).
pub fn program(kind: u64) -> &'static Program {
    match kind {
        KIND_SPEND => SPEND.get_or_init(|| assemble("spend", SPEND_MASM)),
        KIND_SHIELD => SHIELD.get_or_init(|| assemble("shield", SHIELD_MASM)),
        vault::KIND_CREATE..=vault::KIND_REC_CANCEL => {
            VAULT[(kind - vault::KIND_CREATE) as usize].get_or_init(|| assemble(vault::statement_name(kind).unwrap(), &vault::source(kind).unwrap()))
        }
        _ => panic!("no statement of kind {kind}"),
    }
}

pub fn program_hash(kind: u64) -> Word {
    program(kind).hash()
}

/// Executes and proves; returns the proof bytes and the stack outputs, with the execute and prove times
/// in milliseconds.
pub fn prove(p: &Prepared) -> Result<(Vec<u8>, Vec<u64>, f64, f64), String> {
    let program = program(p.kind);
    let mut host = DefaultHost::default();
    let t0 = std::time::Instant::now();
    let witness = FastProcessor::new_with_options(p.stack_inputs, p.advice.clone(), ExecutionOptions::default())
        .map_err(|e| format!("{e}"))?
        .execute_for_proving_sync(program, &mut host)
        .map_err(|e| format!("execution failed: {e}"))?;
    let t1 = std::time::Instant::now();
    let outputs: Vec<u64> = witness.claim().stack_outputs().iter().map(|f| f.as_canonical_u64()).collect();
    let proof = Prover::new().with_hash_fn(HashFunction::Blake3_256).prove_full(witness).map_err(|e| format!("proving failed: {e}"))?;
    let t2 = std::time::Instant::now();
    let want = claim_outputs(p.statement_hash);
    if outputs[..] != want[..] {
        return Err("the program's outputs are not the statement hash".into());
    }
    Ok((proof.to_bytes(), outputs, (t1 - t0).as_secs_f64() * 1000.0, (t2 - t1).as_secs_f64() * 1000.0))
}

/// Executes without proving (the host-side check of a witness).
pub fn execute(p: &Prepared) -> Result<Vec<u64>, String> {
    let program = program(p.kind);
    let mut host = DefaultHost::default();
    let witness = FastProcessor::new_with_options(p.stack_inputs, p.advice.clone(), ExecutionOptions::default())
        .map_err(|e| format!("{e}"))?
        .execute_for_proving_sync(program, &mut host)
        .map_err(|e| format!("execution failed: {e}"))?;
    Ok(witness.claim().stack_outputs().iter().map(|f| f.as_canonical_u64()).collect())
}
