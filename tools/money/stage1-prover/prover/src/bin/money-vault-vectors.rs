//! money-vault-vectors: writes tools/money/vectors/money-vault-vectors.json, the vault's fixed vectors the
//! node's Go tests and the wallet's Rust tests read: the authorities' commitments, the policy hash, the head,
//! pending and inbound commitments, the position tag, the revoked leaf, the seven statement hashes over fixed
//! public words and the seven program hashes, every one computed here with miden-crypto 0.36.1.
use money_stage1_prover as money;
use money::vault;
use miden_core::Word;
use serde_json::json;

fn h(w: Word) -> String {
    hex::encode(money::word_to_bytes(w))
}
fn rep(b: u8) -> Word {
    money::word_from_bytes(&[b; 32]).unwrap()
}

fn main() {
    let out = std::env::args().nth(1).unwrap_or_else(|| "../vectors/money-vault-vectors.json".into());
    let sk_c = rep(0x21);
    let sk_a = rep(0x22);
    let nk_v = rep(0x23);
    let policy = vault::Policy {
        pk_e: rep(0x24),
        pk_c: vault::pk_cancel(sk_c),
        pk_a: vault::pk_recover(sk_a),
        nk_field: vault::nk_field(nk_v),
        delay_blocks: 300,
        delay_seconds: 3600,
        period: 300,
        limit: 1_000_000_000_000_000_000,
        recovery_blocks: 600,
        recovery_seconds: 7200,
        nonce: rep(0x25),
    };
    let ph = policy.hash();
    let head = vault::Head { value: 7_000_000_000_000_000_000, vid: rep(0x26), policy_hash: ph, period_idx: 3, rec_h: 0, rec_t: 0, spent: 400_000_000_000_000_000, rec_hash: Word::default(), r: rep(0x27), co: 0 };
    let pending = vault::Pending { value: 400_000_000_000_000_000, vid: rep(0x26), policy_hash: ph, pk_dest: rep(0x28), h_req: 1000, t_req: 2_000_000, delay_blocks: 300, delay_seconds: 3600, r: rep(0x29) };
    let pcm = pending.commitment();
    let inbound = vault::inbound_commitment(pending.value, pending.vid, ph, rep(0x2a));
    let mut statements = serde_json::Map::new();
    let pw = |n: usize| -> Vec<Word> { (0..n).map(|i| rep(0x30 + i as u8)).collect() };
    for kind in vault::KIND_CREATE..=vault::KIND_REC_CANCEL {
        let n = match kind {
            vault::KIND_CREATE => 5,
            vault::KIND_WITHDRAW => 5,
            vault::KIND_CANCEL => 4,
            vault::KIND_COMPLETE => 7,
            vault::KIND_ANNOUNCE => 5,
            vault::KIND_POLICY_COMPLETE => 5,
            _ => 4,
        };
        let mut words = vec![money::domain_word(7404, kind)];
        words.extend(pw(n));
        statements.insert(
            vault::statement_name(kind).unwrap().into(),
            json!({ "kind": kind, "chain_id": 7404, "words_after_domain": words[1..].iter().map(|w| h(*w)).collect::<Vec<_>>(), "hash": h(vault::statement_hash(&words)), "program_hash": money::ints(money::program_hash(kind)), "program_hash_hex": h(money::program_hash(kind)) }),
        );
    }
    let v = json!({
        "definitions": {
            "tag": "[tag, 0, 0, 0]; policy 11, head 12, pending 13, inbound 14, ptag 15, cancel 16, recover 17, nk_field 18, nk_cancel 19",
            "policy_hash": "fold H over [TAG 11, pk_e, pk_c, pk_a, nk_field, [D_b, D_s, P, 0], value_word(L), [R_b, R_s, 0, 0], nonce]",
            "head": "fold H over [TAG 12, value_word(v), vid, policy_hash, [period_idx, rec_h, rec_t, 0], value_word(spent), rec_hash, r]",
            "pending": "fold H over [TAG 13, value_word(w), vid, policy_hash, pk_dest, [h_req, t_req, D_b, D_s], r]",
            "inbound": "fold H over [TAG 14, value_word(w), vid, policy_hash, r]",
            "ptag": "fold H over [TAG 15, cm_pending, [position, 0, 0, 0], r]",
            "pk_c": "H(sk_c, TAG 16)", "pk_a": "H(sk_a, TAG 17)", "nk_field": "H(nk_v, TAG 18)", "nk_c": "H(sk_c, TAG 19)",
            "revoked_leaf": "[key, next_key, 0, 0], key the policy hash's first element; the empty leaf is the sentinel",
            "statement_hash": "fold H over [domain [chain_id, kind, 0, 0], the record's public words in its order]"
        },
        "keys": { "sk_c": h(sk_c), "pk_c": h(policy.pk_c), "sk_a": h(sk_a), "pk_a": h(policy.pk_a), "nk_v": h(nk_v), "nk_field": h(policy.nk_field), "nk_c": h(vault::nk_cancel(sk_c)) },
        "policy": { "pk_e": h(policy.pk_e), "delay_blocks": 300, "delay_seconds": 3600, "period": 300, "limit": policy.limit.to_string(), "recovery_blocks": 600, "recovery_seconds": 7200, "nonce": h(policy.nonce), "hash": h(ph) },
        "head": { "value": head.value.to_string(), "vid": h(head.vid), "period_idx": 3, "rec_h": 0, "rec_t": 0, "spent": head.spent.to_string(), "rec_hash": h(head.rec_hash), "r": h(head.r), "cm": h(head.commitment()) },
        "pending": { "value": pending.value.to_string(), "vid": h(pending.vid), "pk_dest": h(pending.pk_dest), "h_req": 1000, "t_req": 2000000, "delay_blocks": 300, "delay_seconds": 3600, "r": h(pending.r), "cm": h(pcm), "ptag_at_5": h(pending.ptag(5)) },
        "inbound": { "r": h(rep(0x2a)), "cm": h(inbound) },
        "revoked": { "leaf_7_9": h(vault::revoked_leaf(7, 9)), "key_of_policy": vault::revoked_key(ph), "depth": vault::REVOKED_DEPTH },
        "statements": statements,
    });
    std::fs::write(&out, serde_json::to_string_pretty(&v).unwrap()).expect("write");
    eprintln!("vault vectors written to {out}");
}
