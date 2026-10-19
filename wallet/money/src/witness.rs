//! The two statements' witnesses in the prover's form (tools/money/stage1-prover/prover/src/lib.rs:
//! money-shield-v1 and money-spend-v1), with the public record the node takes beside each. A witness carries the
//! spend authority and is written only to a file the person names; a record carries nothing secret.
//!
//! A spend takes one or two of the wallet's unspent notes and their Merkle paths under an anchor the node
//! served (igneum_poolPath), pads the second input with a dummy (value zero, fresh secrets, no path: the
//! statement's rule, Sapling's), and makes two outputs: the payment sealed to the recipient and the change
//! sealed to this wallet (a dummy output of value zero, sealed to a fresh key, when there is none). The values
//! balance to the wei: inputs = payment + change + fee + public value.

use rand_core::{OsRng, RngCore};
use serde_json::{json, Value};
use zeroize::Zeroizing;

use crate::hash::{reduce_word, CircuitHash, H};
use crate::hex;
use crate::keys::{Address, Wallet};
use crate::note;
use crate::scan::OwnedNote;
use crate::sizes;
use crate::{err, Result};

/// A spend's inputs: a note and its path (32 siblings, leaf to root) under the anchor.
pub struct SpendInput {
    pub note: OwnedNote,
    pub path: Vec<[u8; 32]>,
}

/// What a spend does.
pub struct SpendPlan {
    pub chain_id: u64,
    pub anchor: [u8; 32],
    pub inputs: Vec<SpendInput>,
    pub to: Address,
    pub value: u128,
    pub memo: Vec<u8>,
    pub fee: u128,
    pub public_value: u128,
    pub public_output: [u8; 20],
}

/// The witness (secret, the prover's input) and the record (public, the node's input) of one statement.
pub struct Prepared {
    pub witness: Zeroizing<String>,
    pub record: Value,
}

fn random_word() -> [u8; 32] {
    let mut b = [0u8; 32];
    OsRng.fill_bytes(&mut b);
    reduce_word(&b)
}

/// A dummy output: value zero to a key nobody holds, its ciphertext random bytes of the one output shape.
fn dummy_output() -> (u128, [u8; 32], [u8; 32], [u8; 32], Vec<u8>) {
    let pk = random_word();
    let r = random_word();
    let cm = H::commitment(0, &pk, &r);
    let mut ct = vec![0u8; sizes::OUTPUT_CIPHERTEXT];
    OsRng.fill_bytes(&mut ct);
    (0, pk, r, cm, ct)
}

/// The spend statement's witness and record.
pub fn prepare_spend(wallet: &Wallet, plan: &SpendPlan) -> Result<Prepared> {
    if plan.inputs.is_empty() || plan.inputs.len() > 2 {
        return err("a spend takes one or two input notes");
    }
    let mut sum_in: u128 = 0;
    let mut inputs = Vec::new();
    let mut nullifiers = Vec::new();
    for (i, inp) in plan.inputs.iter().enumerate() {
        let n = &inp.note;
        if n.note.pk != *wallet.fvk().pk() {
            return err(format!("input {i} is not this wallet's note"));
        }
        if let Some(h) = n.spent_at {
            return err(format!("input {i} was spent at height {h}"));
        }
        if n.note.value == 0 {
            return err(format!("input {i} has no value"));
        }
        if inp.path.len() != sizes::TREE_DEPTH {
            return err(format!("input {i}: the path is not {} siblings", sizes::TREE_DEPTH));
        }
        if H::commitment(n.note.value, &n.note.pk, &n.note.r) != n.cm {
            return err(format!("input {i}: the note does not open to its commitment"));
        }
        let nf = H::nullifier(wallet.fvk().nk(), &n.cm, n.position);
        if nf != n.nullifier {
            return err(format!("input {i}: the nullifier is not this wallet's"));
        }
        sum_in = sum_in.checked_add(n.note.value).ok_or_else(|| crate::Error("the inputs overflow".into()))?;
        nullifiers.push(hex::encode(&nf));
        inputs.push(json!({
            "value": n.note.value.to_string(),
            "sk_spend": hex::encode(wallet.sk_spend()),
            "nk": hex::encode(wallet.fvk().nk()),
            "r": hex::encode(&n.note.r),
            "index": n.position,
            "path": inp.path.iter().map(|s| hex::encode(s)).collect::<Vec<_>>(),
        }));
    }
    while inputs.len() < 2 {
        // the dummy input: value zero, fresh secrets, no membership
        let sk = random_word();
        let nk = random_word();
        let r = random_word();
        let cm = H::commitment(0, &H::pk(&sk, &nk), &r);
        nullifiers.push(hex::encode(&H::nullifier(&nk, &cm, 0)));
        inputs.push(json!({ "value": "0", "sk_spend": hex::encode(&sk), "nk": hex::encode(&nk), "r": hex::encode(&r), "index": 0, "path": [] }));
    }
    let out = plan
        .value
        .checked_add(plan.fee)
        .and_then(|v| v.checked_add(plan.public_value))
        .ok_or_else(|| crate::Error("the outputs overflow".into()))?;
    if out > sum_in {
        return err(format!("the inputs ({sum_in} wei) do not cover the payment, the fee and the public value ({out} wei)"));
    }
    let change = sum_in - out;
    let mut outputs = Vec::new();
    let mut record_outputs = Vec::new();
    let mut commitments = Vec::new();
    // output 0: the payment; output 1: the change
    for (value, to) in [(plan.value, &plan.to), (change, &wallet.address())] {
        if value == 0 {
            let (v, pk, r, cm, ct) = dummy_output();
            outputs.push(json!({ "value": v.to_string(), "pk": hex::encode(&pk), "r": hex::encode(&r) }));
            commitments.push(hex::encode(&cm));
            record_outputs.push(json!({ "commitment": hex::encode(&cm), "ciphertext": hex::encode(&ct), "dummy": true }));
            continue;
        }
        let memo: &[u8] = if std::ptr::eq(to, &plan.to) { &plan.memo } else { b"" };
        let (n, o) = note::seal(to, value, memo)?;
        outputs.push(json!({ "value": n.value.to_string(), "pk": hex::encode(&n.pk), "r": hex::encode(&n.r) }));
        commitments.push(hex::encode(&o.cm));
        record_outputs.push(json!({ "commitment": hex::encode(&o.cm), "ciphertext": hex::encode(&o.to_bytes()[32..]), "dummy": false }));
    }
    let witness = json!({
        "statement": "money-spend-v1",
        "chain_id": plan.chain_id,
        "anchor": hex::encode(&plan.anchor),
        "nullifiers": nullifiers,
        "commitments": commitments,
        "public_value": plan.public_value.to_string(),
        "fee": plan.fee.to_string(),
        "public_output": hex::encode(&plan.public_output),
        "inputs": inputs,
        "outputs": outputs,
    });
    let record = json!({
        "statement": "money-spend-v1",
        "chain_id": plan.chain_id,
        "anchor": witness["anchor"],
        "nullifiers": witness["nullifiers"],
        "outputs": record_outputs,
        "public_value": witness["public_value"],
        "fee": witness["fee"],
        "public_output": witness["public_output"],
        "inputs_value": sum_in.to_string(),
        "change": change.to_string(),
    });
    Ok(Prepared { witness: Zeroizing::new(serde_json::to_string_pretty(&witness).unwrap()), record })
}

/// The shield statement's witness and record: one note of `value` sealed to `to`.
pub fn prepare_shield(to: &Address, value: u128, memo: &[u8], chain_id: u64) -> Result<Prepared> {
    if value == 0 {
        return err("a shield carries value");
    }
    let (n, o) = note::seal(to, value, memo)?;
    let witness = json!({
        "statement": "money-shield-v1",
        "chain_id": chain_id,
        "commitment": hex::encode(&o.cm),
        "value": value.to_string(),
        "pk": hex::encode(&n.pk),
        "r": hex::encode(&n.r),
    });
    let record = json!({
        "statement": "money-shield-v1",
        "chain_id": chain_id,
        "commitment": hex::encode(&o.cm),
        "ciphertext": hex::encode(&o.to_bytes()[32..]),
        "value": value.to_string(),
    });
    Ok(Prepared { witness: Zeroizing::new(serde_json::to_string_pretty(&witness).unwrap()), record })
}

/// A wallet-side check of a spend witness's shape (what the prover's host check will also do).
pub fn check_spend_witness(text: &str) -> Result<()> {
    let v: Value = serde_json::from_str(text).map_err(|e| crate::Error(format!("witness: {e}")))?;
    if v["statement"] != "money-spend-v1" || v["inputs"].as_array().map(|a| a.len()) != Some(2) || v["outputs"].as_array().map(|a| a.len()) != Some(2) {
        return err("witness: not a two-in two-out money-spend-v1 witness");
    }
    let mut sum_in: u128 = 0;
    for inp in v["inputs"].as_array().unwrap() {
        let value: u128 = inp["value"].as_str().unwrap_or("x").parse().map_err(|_| crate::Error("witness: a value".into()))?;
        let sk: [u8; 32] = hex::decode_array(inp["sk_spend"].as_str().unwrap_or(""), "sk_spend")?;
        let nk: [u8; 32] = hex::decode_array(inp["nk"].as_str().unwrap_or(""), "nk")?;
        let r: [u8; 32] = hex::decode_array(inp["r"].as_str().unwrap_or(""), "r")?;
        let cm = H::commitment(value, &H::pk(&sk, &nk), &r);
        let nf = H::nullifier(&nk, &cm, inp["index"].as_u64().unwrap_or(0));
        let i = sum_in.checked_add(value).ok_or_else(|| crate::Error("overflow".into()))?;
        sum_in = i;
        if !v["nullifiers"].as_array().unwrap().iter().any(|x| x.as_str() == Some(&hex::encode(&nf))) {
            return err("witness: an input's nullifier is not among the statement's");
        }
    }
    let mut sum_out: u128 = v["fee"].as_str().unwrap_or("x").parse().map_err(|_| crate::Error("witness: the fee".into()))?;
    let pv: u128 = v["public_value"].as_str().unwrap_or("x").parse().map_err(|_| crate::Error("witness: the public value".into()))?;
    sum_out += pv;
    for o in v["outputs"].as_array().unwrap() {
        let value: u128 = o["value"].as_str().unwrap_or("x").parse().map_err(|_| crate::Error("witness: a value".into()))?;
        sum_out = sum_out.checked_add(value).ok_or_else(|| crate::Error("overflow".into()))?;
    }
    if sum_in != sum_out {
        return err(format!("witness: the values do not balance ({sum_in} in, {sum_out} out)"));
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::note::Output;
    use crate::scan::{Block, Scanner};

    #[test]
    fn a_spend_balances_and_a_foreign_note_is_refused() {
        let w = Wallet::generate();
        let to = Wallet::generate();
        let (_, out) = note::seal(&w.address(), 1_000_000_000_000_000_000u128 * 5, b"").unwrap();
        let mut s = Scanner::new(w.fvk().clone(), 100);
        s.scan_block(&Block { height: 1, position: Some(100), outputs: vec![out], nullifiers: vec![], mints: vec![] });
        let n = s.notes()[0].clone();
        assert_eq!(n.position, 100);
        let plan = SpendPlan {
            chain_id: 7402,
            anchor: [7u8; 32],
            inputs: vec![SpendInput { note: n.clone(), path: vec![[0u8; 32]; 32] }],
            to: to.address(),
            value: 2_000_000_000_000_000_000,
            memo: b"rent".to_vec(),
            fee: 1000,
            public_value: 500,
            public_output: [1u8; 20],
        };
        let p = prepare_spend(&w, &plan).unwrap();
        check_spend_witness(&p.witness).unwrap();
        assert_eq!(p.record["change"], "2999999999999998500");
        // the recipient opens output 0, the sender opens output 1 (the change)
        let o0 = Output::from_bytes(&[hex::decode(p.record["outputs"][0]["commitment"].as_str().unwrap()).unwrap(), hex::decode(p.record["outputs"][0]["ciphertext"].as_str().unwrap()).unwrap()].concat()).unwrap();
        let got = note::open(to.fvk(), &o0).unwrap();
        assert_eq!(got.value, 2_000_000_000_000_000_000);
        assert_eq!(got.memo, b"rent");
        assert!(note::open(w.fvk(), &o0).is_none());
        let o1 = Output::from_bytes(&[hex::decode(p.record["outputs"][1]["commitment"].as_str().unwrap()).unwrap(), hex::decode(p.record["outputs"][1]["ciphertext"].as_str().unwrap()).unwrap()].concat()).unwrap();
        assert_eq!(note::open(w.fvk(), &o1).unwrap().value, 2_999_999_999_999_998_500);
        // too much
        let mut big = SpendPlan { ..plan };
        big.value = 6_000_000_000_000_000_000;
        assert!(prepare_spend(&w, &big).is_err());
        // another wallet cannot spend the note
        big.value = 1;
        assert!(prepare_spend(&to, &big).is_err());
        // a shield
        let sh = prepare_shield(&w.address(), 42, b"", 7402).unwrap();
        assert_eq!(sh.record["value"], "42");
        assert!(prepare_shield(&w.address(), 0, b"", 7402).is_err());
    }
}
