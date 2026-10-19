//! The shared vectors re-derived with miden-crypto (tools/money/vectors/money-stage1-vectors.json): the
//! file is what the node's Go tests and the wallet's Rust tests read, so this test is the file's warrant.
use money_stage1_prover as money;
use miden_core::{Felt, Word};
use miden_crypto::hash::poseidon2::Poseidon2;
use serde_json::Value;

fn s<'a>(v: &'a Value, k: &str) -> &'a str {
    v.get(k).and_then(Value::as_str).unwrap_or_else(|| panic!("vector: no {k}"))
}
fn w(v: &Value, k: &str) -> Word {
    money::hex32(s(v, k), k).unwrap()
}
fn limbs(v: &Value) -> [u64; 4] {
    let a = v.as_array().unwrap();
    [a[0].as_u64().unwrap(), a[1].as_u64().unwrap(), a[2].as_u64().unwrap(), a[3].as_u64().unwrap()]
}

#[test]
fn the_shared_vectors_hold() {
    let text = std::fs::read_to_string(concat!(env!("CARGO_MANIFEST_DIR"), "/../../vectors/money-stage1-vectors.json")).unwrap();
    let v: Value = serde_json::from_str(&text).unwrap();
    let mut perm: [Felt; 12] = core::array::from_fn(|i| Felt::new_unchecked(v["permutation"]["input"][i].as_u64().unwrap()));
    Poseidon2::apply_permutation(&mut perm);
    for i in 0..12 {
        assert_eq!(perm[i].as_canonical_u64(), v["permutation"]["output"][i].as_u64().unwrap());
    }
    let m = money::merge(money::word(limbs(&v["merge"]["a"])), money::word(limbs(&v["merge"]["b"])));
    assert_eq!(money::ints(m), limbs(&v["merge"]["out"]));
    assert_eq!(hex::encode(money::word_to_bytes(m)), s(&v["merge"], "out_hex"));
    assert_eq!(money::merge(w(&v["node_hex"], "left"), w(&v["node_hex"], "right")), w(&v["node_hex"], "out"));
    for vw in v["value_words"].as_array().unwrap() {
        assert_eq!(money::ints(money::value_word(s(vw, "value").parse().unwrap())), limbs(&vw["limbs"]));
    }
    let addr = money::hex20(s(&v["address_word"], "address"), "address").unwrap();
    assert_eq!(money::ints(money::address_word(&addr)), limbs(&v["address_word"]["limbs"]));
    let pk = money::pk(w(&v["keys"], "sk_spend"), w(&v["keys"], "nk"));
    assert_eq!(pk, w(&v["keys"], "pk"));
    let n = &v["note"];
    let cm = money::commitment(s(n, "value").parse().unwrap(), w(n, "pk"), w(n, "r"));
    assert_eq!(cm, w(n, "cm"));
    assert_eq!(money::nullifier(w(&v["keys"], "nk"), cm, n["index"].as_u64().unwrap()), w(n, "nullifier"));
    let mut z = Word::default();
    for level in 1..=32 {
        z = money::merge(z, z);
        if let Some(want) = v["empty_roots"].get(level.to_string()) {
            assert_eq!(hex::encode(money::word_to_bytes(z)), want.as_str().unwrap(), "empty root at level {level}");
        }
    }
    let sh = &v["shield_statement"];
    assert_eq!(money::shield_statement_hash(sh["chain_id"].as_u64().unwrap(), w(sh, "cm"), s(sh, "value").parse().unwrap()), w(sh, "hash"));
    let sp = &v["spend_statement"];
    let nfs = [money::hex32(sp["nullifiers"][0].as_str().unwrap(), "nf").unwrap(), money::hex32(sp["nullifiers"][1].as_str().unwrap(), "nf").unwrap()];
    let cms = [money::hex32(sp["commitments"][0].as_str().unwrap(), "cm").unwrap(), money::hex32(sp["commitments"][1].as_str().unwrap(), "cm").unwrap()];
    let got = money::spend_statement_hash(
        sp["chain_id"].as_u64().unwrap(),
        w(sp, "anchor"),
        nfs,
        cms,
        s(sp, "public_value").parse().unwrap(),
        s(sp, "fee").parse().unwrap(),
        &money::hex20(s(sp, "public_output"), "public_output").unwrap(),
    );
    assert_eq!(got, w(sp, "hash"));
    for (name, kind) in [("money-shield-v1", money::KIND_SHIELD), ("money-spend-v1", money::KIND_SPEND)] {
        assert_eq!(money::ints(money::program_hash(kind)), limbs(&v["programs"][name]["hash"]), "program hash of {name}");
    }
}

/// The two statements execute on the host-checked witnesses and refuse the broken ones.
#[test]
fn the_statements_execute_and_refuse() {
    let mut addr = [0u8; 20];
    addr[19] = 1;
    let sk = money::word([1, 2, 3, 4]);
    let nk = money::word([5, 6, 7, 8]);
    let pk = money::pk(sk, nk);
    let r = money::word([9, 9, 9, 9]);
    let h = |w: Word| hex::encode(money::word_to_bytes(w));
    // A shield of 2^70 wei: the value crosses two limbs.
    let value: u128 = 1 << 70;
    let cm = money::commitment(value, pk, r);
    let shield = money::ShieldWitness { statement: "money-shield-v1".into(), chain_id: 7402, commitment: h(cm), value: value.to_string(), pk: h(pk), r: h(r) };
    let p = money::prepare_shield(&shield).unwrap();
    assert_eq!(money::execute(&p).unwrap(), money::claim_outputs(p.statement_hash).to_vec());
    let mut bad = shield.clone();
    bad.value = (value + 1).to_string();
    assert!(money::prepare_shield(&bad).is_err(), "the host refuses a value that is not the commitment's");
    // A spend of that note (as the only leaf) plus a dummy, into two outputs, a fee and a public value.
    let mut anchor = cm;
    let mut zero = Word::default();
    let mut path = Vec::new();
    for _ in 0..32 {
        path.push(h(zero));
        anchor = money::merge(anchor, zero);
        zero = money::merge(zero, zero);
    }
    let dummy_nk = money::word([11, 12, 13, 14]);
    let dummy_cm = money::commitment(0, money::pk(money::word([0, 0, 0, 1]), dummy_nk), money::word([2, 2, 2, 2]));
    let fee: u128 = 1000;
    let vpub: u128 = 1 << 69;
    let o1: u128 = (1u128 << 69) - 5000;
    let o2: u128 = value - fee - vpub - o1;
    let opk = money::word([21, 22, 23, 24]);
    let or1 = money::word([31, 31, 31, 31]);
    let or2 = money::word([32, 32, 32, 32]);
    let spend = money::SpendWitness {
        statement: "money-spend-v1".into(),
        chain_id: 7402,
        anchor: h(anchor),
        nullifiers: vec![h(money::nullifier(nk, cm, 0)), h(money::nullifier(dummy_nk, dummy_cm, 0))],
        commitments: vec![h(money::commitment(o1, opk, or1)), h(money::commitment(o2, opk, or2))],
        public_value: vpub.to_string(),
        fee: fee.to_string(),
        public_output: hex::encode(addr),
        inputs: vec![
            money::InputWitness { value: value.to_string(), sk_spend: h(sk), nk: h(nk), r: h(r), index: 0, path: path.clone() },
            money::InputWitness { value: "0".into(), sk_spend: h(money::word([0, 0, 0, 1])), nk: h(dummy_nk), r: h(money::word([2, 2, 2, 2])), index: 0, path: vec![] },
        ],
        outputs: vec![money::OutputWitness { value: o1.to_string(), pk: h(opk), r: h(or1) }, money::OutputWitness { value: o2.to_string(), pk: h(opk), r: h(or2) }],
    };
    let p = money::prepare_spend(&spend).unwrap();
    assert_eq!(money::execute(&p).unwrap(), money::claim_outputs(p.statement_hash).to_vec());
    // The host refuses an unbalanced spend, a wrong path and a foreign key; the circuit refuses a
    // witness whose advice was tampered after the host check (a wrong input value).
    let mut bad = spend.clone();
    bad.outputs[0].value = (o1 + 1).to_string();
    bad.commitments[0] = h(money::commitment(o1 + 1, opk, or1));
    assert!(money::prepare_spend(&bad).err().unwrap().contains("balance"));
    let mut bad = spend.clone();
    bad.inputs[0].path[3] = h(money::word([7, 7, 7, 7]));
    assert!(money::prepare_spend(&bad).err().unwrap().contains("anchor"));
    let mut bad = spend.clone();
    bad.inputs[0].sk_spend = h(money::word([4, 3, 2, 1]));
    assert!(money::prepare_spend(&bad).err().unwrap().contains("nullifier"));
}
