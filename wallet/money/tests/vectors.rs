//! The derivation against fixed vectors (tests/vectors.json, cut by `igneum-money vectors --seed-hex` on the
//! lab box and committed; re-cut in cut four for Poseidon2 and the 128-bit value). The seeds are byte patterns,
//! not wallets.

use igneum_wallet_money::backup;
use igneum_wallet_money::hash::{b_hash, CircuitHash, H};
use igneum_wallet_money::hex;
use igneum_wallet_money::keys::{Seed, Wallet};
use igneum_wallet_money::note;
use serde_json::Value;

fn s<'a>(v: &'a Value, k: &str) -> &'a str {
    v.get(k).and_then(Value::as_str).unwrap_or_else(|| panic!("vector: no {k}"))
}

#[test]
fn the_fixed_vectors_hold() {
    let text = std::fs::read_to_string(concat!(env!("CARGO_MANIFEST_DIR"), "/tests/vectors.json")).unwrap();
    let list: Vec<Value> = serde_json::from_str(&text).unwrap();
    assert!(list.len() >= 2);
    for v in &list {
        assert_eq!(s(v, "hash"), H::NAME, "the vectors are for another hash instance; re-cut them");
        let seed: [u8; 32] = hex::decode_array(s(v, "seed"), "seed").unwrap();
        let w = Wallet::from_seed(Seed::from_bytes(seed));
        assert_eq!(hex::encode(w.sk_spend()), s(v, "sk_spend"));
        assert_eq!(hex::encode(w.fvk().nk()), s(v, "nk"));
        assert_eq!(hex::encode(w.fvk().ivk()), s(v, "ivk"));
        assert_eq!(hex::encode(w.fvk().pk()), s(v, "pk"));
        let ek = w.fvk().kem_public_key().as_slice();
        assert_eq!(hex::encode(&b_hash(ek)), s(v, "ek_blake3"));
        assert_eq!(hex::encode(&ek[..32]), s(v, "ek_first32"));
        assert_eq!(hex::encode(&w.address().short_hash()), s(v, "address_blake3"));
        assert_eq!(&w.address().encode()[..80], s(v, "address_text_prefix"));
        assert_eq!(backup::words(w.seed()).as_str(), s(v, "words"));
        assert_eq!(backup::restore(s(v, "words")).unwrap().address(), w.address());
        let n = v.get("note").unwrap();
        let r: [u8; 32] = hex::decode_array(s(n, "r"), "r").unwrap();
        let er: [u8; 32] = hex::decode_array(s(n, "encaps_randomness"), "er").unwrap();
        let (nt, out) = note::seal_with(&w.address(), n["value"].as_u64().unwrap() as u128, s(n, "memo").as_bytes(), r, er).unwrap();
        assert_eq!(hex::encode(&out.cm), s(v, "cm"));
        assert_eq!(hex::encode(&b_hash(&out.to_bytes())), s(v, "output_blake3"));
        assert_eq!(note::open(w.fvk(), &out).unwrap(), nt);
        assert_eq!(hex::encode(&w.fvk().nullifier(&out.cm, 7)), s(v, "nullifier_at_7"));
    }
}
