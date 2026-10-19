//! The vectors the node, the prover and this wallet share (tools/money/vectors/money-stage1-vectors.json, cut by
//! the prover with miden-crypto): this crate's Poseidon2 and encodings must reproduce every one it implements.

use igneum_wallet_money::hash::{self, CircuitHash, H};
use igneum_wallet_money::hex;
use serde_json::Value;

fn s<'a>(v: &'a Value, k: &str) -> &'a str {
    v.get(k).and_then(Value::as_str).unwrap_or_else(|| panic!("vector: no {k}"))
}
fn h32(v: &Value, k: &str) -> [u8; 32] {
    hex::decode_array(s(v, k), k).unwrap()
}
fn limbs(v: &Value) -> [u64; 4] {
    let a = v.as_array().unwrap();
    [a[0].as_u64().unwrap(), a[1].as_u64().unwrap(), a[2].as_u64().unwrap(), a[3].as_u64().unwrap()]
}

#[test]
fn the_shared_vectors_hold() {
    let path = concat!(env!("CARGO_MANIFEST_DIR"), "/../../tools/money/vectors/money-stage1-vectors.json");
    let Ok(text) = std::fs::read_to_string(path) else {
        eprintln!("the shared vectors are not in this tree; skipped");
        return;
    };
    let v: Value = serde_json::from_str(&text).unwrap();
    let mut st: [u64; 12] = core::array::from_fn(|i| v["permutation"]["input"][i].as_u64().unwrap());
    hash::permute(&mut st);
    for i in 0..12 {
        assert_eq!(st[i], v["permutation"]["output"][i].as_u64().unwrap(), "permutation element {i}");
    }
    let m = hash::merge(limbs(&v["merge"]["a"]), limbs(&v["merge"]["b"]));
    assert_eq!(m, limbs(&v["merge"]["out"]));
    assert_eq!(hex::encode(&hash::word_to_bytes(m)), s(&v["merge"], "out_hex"));
    assert_eq!(hash::node(&h32(&v["node_hex"], "left"), &h32(&v["node_hex"], "right")), h32(&v["node_hex"], "out"));
    for vw in v["value_words"].as_array().unwrap() {
        assert_eq!(hash::value_word(s(vw, "value").parse().unwrap()), limbs(&vw["limbs"]));
    }
    let pk = H::pk(&h32(&v["keys"], "sk_spend"), &h32(&v["keys"], "nk"));
    assert_eq!(pk, h32(&v["keys"], "pk"));
    let n = &v["note"];
    let cm = H::commitment(s(n, "value").parse().unwrap(), &h32(n, "pk"), &h32(n, "r"));
    assert_eq!(cm, h32(n, "cm"));
    assert_eq!(H::nullifier(&h32(&v["keys"], "nk"), &cm, n["index"].as_u64().unwrap()), h32(n, "nullifier"));
    let mut z = [0u8; 32];
    for level in 1..=32 {
        z = hash::node(&z, &z);
        if let Some(want) = v["empty_roots"].get(level.to_string()) {
            assert_eq!(hex::encode(&z), want.as_str().unwrap(), "empty root at level {level}");
        }
    }
}
