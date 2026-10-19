//! money-vectors: writes tools/money/vectors/money-stage1-vectors.json, the fixed vectors the node's Go
//! tests, the wallet's Rust tests and this crate's test all read: the permutation, the merge, the three
//! derivations, the empty tree's roots, the value and address words, the two statement hashes and the
//! two program hashes, every one computed here with miden-crypto 0.36.1.
use money_stage1_prover as money;
use miden_core::{Felt, Word};
use miden_crypto::hash::poseidon2::Poseidon2;
use serde_json::json;

fn h(w: Word) -> String {
    hex::encode(money::word_to_bytes(w))
}
fn rep(b: u8) -> Word {
    money::word_from_bytes(&[b; 32]).unwrap()
}

fn main() {
    let out = std::env::args().nth(1).unwrap_or_else(|| "../vectors/money-stage1-vectors.json".into());
    let mut perm: [Felt; 12] = core::array::from_fn(|i| Felt::new_unchecked(i as u64));
    Poseidon2::apply_permutation(&mut perm);
    let merge = money::merge(money::word([1, 2, 3, 4]), money::word([5, 6, 7, 8]));
    let sk = rep(0x11);
    let nk = rep(0x22);
    let r = rep(0x33);
    let pk = money::pk(sk, nk);
    let value: u128 = 123_456_789;
    let cm = money::commitment(value, pk, r);
    let nf = money::nullifier(nk, cm, 7);
    let mut zeros = vec![Word::default()];
    for i in 0..32 {
        let z = money::merge(zeros[i], zeros[i]);
        zeros.push(z);
    }
    let mut addr = [0u8; 20];
    addr[16..].copy_from_slice(&0xae91_5001u32.to_be_bytes());
    let spend_sh = money::spend_statement_hash(7402, zeros[32], [nf, rep(0x44)], [cm, rep(0x55)], 0, 1000, &addr);
    let shield_sh = money::shield_statement_hash(7402, cm, value);
    let big: u128 = u128::MAX;
    let v = json!({
        "hash": "poseidon2-goldilocks (miden-crypto 0.36.1): width 12, rate 8, capacity 4, x^7, 4+22+4 rounds; H(a,b) = merge: state [a, b, 0^4], one permutation, the first four elements out",
        "word_bytes": "a word is four field elements; its 32 bytes are the canonical elements little-endian in order",
        "value_word": "a 128-bit value as four 32-bit limbs, limb 0 the least significant",
        "address_word": "a 20-byte address as four 40-bit limbs, little-endian, in order",
        "definitions": {
            "pk": "H(sk_spend, nk)",
            "commitment": "H(H(value_word(v), pk), r)",
            "nullifier": "H(H(nk, cm), [index, 0, 0, 0])",
            "node": "H(left, right)",
            "domain": "[chain_id, kind, 0, 0]; kind 1 a shield, 2 a spend",
            "shield_statement_hash": "H(H(domain, cm), value_word(v))",
            "spend_statement_hash": "fold H over [domain, anchor, nf1, nf2, cm1, cm2, value_word(vpub), value_word(fee), address_word(public_output)]"
        },
        "permutation": { "input": (0..12u64).collect::<Vec<_>>(), "output": perm.iter().map(|f| f.as_canonical_u64()).collect::<Vec<_>>() },
        "merge": { "a": [1, 2, 3, 4], "b": [5, 6, 7, 8], "out": money::ints(merge), "out_hex": h(merge) },
        "node_hex": { "left": hex::encode([0x01u8; 32]), "right": hex::encode([0x02u8; 32]), "out": h(money::merge(rep(0x01), rep(0x02))) },
        "value_words": [
            { "value": "0", "limbs": money::ints(money::value_word(0)) },
            { "value": "1", "limbs": money::ints(money::value_word(1)) },
            { "value": "4294967296", "limbs": money::ints(money::value_word(1 << 32)) },
            { "value": "18446744073709551616", "limbs": money::ints(money::value_word(1 << 64)) },
            { "value": big.to_string(), "limbs": money::ints(money::value_word(big)) }
        ],
        "address_word": { "address": hex::encode(addr), "limbs": money::ints(money::address_word(&addr)) },
        "keys": { "sk_spend": hex::encode([0x11u8; 32]), "nk": hex::encode([0x22u8; 32]), "pk": h(pk) },
        "note": { "value": value.to_string(), "pk": h(pk), "r": hex::encode([0x33u8; 32]), "cm": h(cm), "index": 7, "nullifier": h(nf) },
        "empty_roots": { "1": h(zeros[1]), "2": h(zeros[2]), "32": h(zeros[32]) },
        "shield_statement": { "chain_id": 7402, "cm": h(cm), "value": value.to_string(), "hash": h(shield_sh) },
        "spend_statement": {
            "chain_id": 7402, "anchor": h(zeros[32]), "nullifiers": [h(nf), hex::encode([0x44u8; 32])],
            "commitments": [h(cm), hex::encode([0x55u8; 32])], "public_value": "0", "fee": "1000",
            "public_output": hex::encode(addr), "hash": h(spend_sh)
        },
        "programs": {
            "money-shield-v1": { "hash": money::ints(money::program_hash(money::KIND_SHIELD)), "hash_hex": h(money::program_hash(money::KIND_SHIELD)) },
            "money-spend-v1": { "hash": money::ints(money::program_hash(money::KIND_SPEND)), "hash_hex": h(money::program_hash(money::KIND_SPEND)) }
        }
    });
    std::fs::write(&out, serde_json::to_string_pretty(&v).unwrap() + "\n").expect("write");
    eprintln!("vectors written to {out}");
}
