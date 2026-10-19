//! The vault's seven statements executed and proven end to end on one scenario: two ordinary notes become a
//! vault; a withdrawal is requested; the pending note is cancelled by the cancellation key and (on a second
//! proof) completed by the destination after maturity; a recovery is announced, cancelled by the cancellation
//! key and (on a second proof) completed after the delay. Each proof is verified with the pinned verifier
//! under the claim the node builds. The refusals the host check and the circuit share are exercised through
//! the host check (the same expressions, prover/src/vault.rs).
use money_stage1_prover as money;
use money::vault::{self, VaultWitness};
use miden_core::{Felt, Word};
use miden_core::program::{ExecutionClaim, ProgramInfo, StackInputs, StackOutputs};
use miden_core::proof::ExecutionProof;
use miden_vm::Verifier;

fn h(w: Word) -> String {
    hex::encode(money::word_to_bytes(w))
}
fn rep(b: u8) -> Word {
    money::word_from_bytes(&[b; 32]).unwrap()
}

/// A small append-only tree of the pool's depth, the root and the paths computed in full.
struct Tree {
    depth: u8,
    leaves: Vec<Word>,
    zeros: Vec<Word>,
}

impl Tree {
    fn new(depth: u8) -> Tree {
        let mut zeros = vec![Word::default()];
        for i in 0..depth as usize {
            zeros.push(money::merge(zeros[i], zeros[i]));
        }
        Tree { depth, leaves: Vec::new(), zeros }
    }
    fn push(&mut self, w: Word) -> u64 {
        self.leaves.push(w);
        (self.leaves.len() - 1) as u64
    }
    fn root(&self) -> Word {
        let mut level = self.leaves.clone();
        for d in 0..self.depth as usize {
            let mut next = Vec::new();
            for k in 0..(level.len() + 1) / 2 {
                let l = level[2 * k];
                let r = if 2 * k + 1 < level.len() { level[2 * k + 1] } else { self.zeros[d] };
                next.push(money::merge(l, r));
            }
            level = next;
        }
        if level.is_empty() { self.zeros[self.depth as usize] } else { level[0] }
    }
    fn path(&self, index: u64) -> Vec<String> {
        let mut level = self.leaves.clone();
        let mut out = Vec::new();
        let mut i = index;
        for d in 0..self.depth as usize {
            let sib = if (i ^ 1) < level.len() as u64 { level[(i ^ 1) as usize] } else { self.zeros[d] };
            out.push(h(sib));
            let mut next = Vec::new();
            for k in 0..(level.len() + 1) / 2 {
                let l = level[2 * k];
                let r = if 2 * k + 1 < level.len() { level[2 * k + 1] } else { self.zeros[d] };
                next.push(money::merge(l, r));
            }
            level = next;
            i >>= 1;
        }
        out
    }
}

fn verify(kind: u64, sh: Word, proof: &[u8]) -> u32 {
    let program = money::program(kind);
    let felts = money::ints(sh).map(Felt::new_unchecked);
    let mut outs = [Felt::new_unchecked(0); 16];
    outs[..4].copy_from_slice(&felts);
    let claim = ExecutionClaim::from_program_info(ProgramInfo::new(program.hash(), Default::default()), StackInputs::new(&felts).unwrap(), StackOutputs::new(&outs).unwrap());
    let proof = ExecutionProof::read_from_bytes(proof).unwrap();
    let outcome = Verifier::new().verify(&claim, &proof).expect("the proof verifies");
    assert!(outcome.is_complete());
    outcome.vm_security_parameters().conjectured_security_level()
}

fn refusal(w: &VaultWitness) -> String {
    match vault::prepare_vault(w) {
        Err(e) => e,
        Ok(_) => panic!("the host check accepted a witness it should refuse"),
    }
}

fn run(name: &str, w: &VaultWitness) -> (usize, f64) {
    let p = vault::prepare_vault(w).unwrap_or_else(|e| panic!("{name}: the host check refuses: {e}"));
    let outs = money::execute(&p).unwrap_or_else(|e| panic!("{name}: {e}"));
    assert_eq!(outs[..4], money::ints(p.statement_hash)[..]);
    let (bytes, _, exec_ms, prove_ms) = money::prove(&p).unwrap();
    let bits = verify(p.kind, p.statement_hash, &bytes);
    eprintln!("{name}: execute {exec_ms:.2} ms, prove {prove_ms:.1} ms, proof {} bytes, {bits} conjectured bits", bytes.len());
    (bytes.len(), prove_ms)
}

#[test]
fn the_seven_vault_statements_prove_and_verify() {
    let chain_id = 7404;
    let sk_e = rep(0x41);
    let nk_e = rep(0x42);
    let pk_e = money::pk(sk_e, nk_e);
    let sk_c = rep(0x43);
    let sk_a = rep(0x44);
    let nk_v = rep(0x45);
    let policy = vault::Policy { pk_e, pk_c: vault::pk_cancel(sk_c), pk_a: vault::pk_recover(sk_a), nk_field: vault::nk_field(nk_v), delay_blocks: 300, delay_seconds: 3600, period: 300, limit: 1_000_000_000_000_000_000, recovery_blocks: 600, recovery_seconds: 7200, nonce: rep(0x46) };
    let pw = |p: &vault::Policy| vault::PolicyWitness { pk_e: h(p.pk_e), pk_c: h(p.pk_c), pk_a: h(p.pk_a), nk_field: h(p.nk_field), delay_blocks: p.delay_blocks, delay_seconds: p.delay_seconds, period: p.period, limit: p.limit.to_string(), recovery_blocks: p.recovery_blocks, recovery_seconds: p.recovery_seconds, nonce: h(p.nonce) };
    let mut tree = Tree::new(money::DEPTH);
    // two ordinary notes of the everyday key
    let (v0, r0) = (5_000_000_000_000_000_000u128, rep(0x47));
    let (v1, r1) = (3_000_000_000_000_000_000u128, rep(0x48));
    let cm0 = money::commitment(v0, pk_e, r0);
    let cm1 = money::commitment(v1, pk_e, r1);
    tree.push(cm0);
    tree.push(cm1);
    let anchor = tree.root();
    // create: the vault takes 7 IGN, 1 IGN of change
    let change = (1_000_000_000_000_000_000u128, rep(0x49), rep(0x4a));
    let change_cm = money::commitment(change.0, change.1, change.2);
    let head = vault::Head { value: 7_000_000_000_000_000_000, vid: rep(0x4b), policy_hash: policy.hash(), period_idx: 0, rec_h: 0, rec_t: 0, spent: 0, rec_hash: Word::default(), r: rep(0x4c), co: 0 };
    let create = VaultWitness {
        statement: "money-vault-create-v1".into(),
        chain_id,
        anchor: h(anchor),
        nullifiers: vec![h(money::nullifier(nk_e, cm0, 0)), h(money::nullifier(nk_e, cm1, 1))],
        commitments: vec![h(head.commitment()), h(change_cm)],
        policy: Some(pw(&policy)),
        nk_v: h(nk_v),
        inputs: vec![
            money::InputWitness { value: v0.to_string(), sk_spend: h(sk_e), nk: h(nk_e), r: h(r0), index: 0, path: tree.path(0) },
            money::InputWitness { value: v1.to_string(), sk_spend: h(sk_e), nk: h(nk_e), r: h(r1), index: 1, path: tree.path(1) },
        ],
        outputs: vec![money::OutputWitness { value: change.0.to_string(), pk: h(change.1), r: h(change.2) }],
        head_out_vid: h(head.vid),
        head_out_r: h(head.r),
        ..Default::default()
    };
    let mut sizes = vec![run("create", &create)];
    // a policy below the floors is refused
    let mut bad = create.clone();
    bad.policy.as_mut().unwrap().delay_seconds = 3599;
    assert!(refusal(&bad).contains("floor"));
    // withdraw at height 1000 (period 3): 0.4 IGN
    let head_pos = tree.push(head.commitment());
    tree.push(change_cm);
    let anchor = tree.root();
    let hw = |tree: &Tree, hd: &vault::Head, pos: u64| vault::HeadWitness { value: hd.value.to_string(), vid: h(hd.vid), period_idx: hd.period_idx, rec_h: hd.rec_h, rec_t: hd.rec_t, spent: hd.spent.to_string(), rec_hash: h(hd.rec_hash), r: h(hd.r), co: hd.co, index: pos, path: tree.path(pos) };
    let (sk_d, nk_d) = (rep(0x51), rep(0x52));
    let pk_d = money::pk(sk_d, nk_d);
    let wv = 400_000_000_000_000_000u128;
    let head2 = vault::Head { value: head.value - wv, period_idx: 3, spent: wv, r: rep(0x4d), ..head.clone() };
    let pending = vault::Pending { value: wv, vid: head.vid, policy_hash: policy.hash(), pk_dest: pk_d, h_req: 1000, t_req: 2_000_000, delay_blocks: 300, delay_seconds: 3600, r: rep(0x4e) };
    let withdraw = VaultWitness {
        statement: "money-vault-withdraw-v1".into(),
        chain_id,
        anchor: h(anchor),
        nullifiers: vec![h(money::nullifier(nk_v, head.commitment(), head_pos))],
        commitments: vec![h(head2.commitment()), h(pending.commitment())],
        height: 1000,
        time: 2_000_000,
        policy: Some(pw(&policy)),
        nk_v: h(nk_v),
        sk_e: h(sk_e),
        nk_e: h(nk_e),
        head_in: Some(hw(&tree, &head, head_pos)),
        head_out_r: h(head2.r),
        pending_out_value: wv.to_string(),
        pending_out_pk_dest: h(pk_d),
        pending_out_r: h(pending.r),
        ..Default::default()
    };
    sizes.push(run("withdraw", &withdraw));
    // the limit binds: a second request in the same period past L is refused by the host check
    let mut over = withdraw.clone();
    over.pending_out_value = (policy.limit + 1).to_string();
    assert!(refusal(&over).contains("limit"));
    // the everyday key cannot change the policy: a witness naming another policy does not open the head
    let mut other = withdraw.clone();
    other.policy.as_mut().unwrap().delay_blocks = 300;
    other.policy.as_mut().unwrap().limit = (policy.limit * 2).to_string();
    assert!(vault::prepare_vault(&other).is_err());
    // cancel and complete the pending note
    let head2_pos = tree.push(head2.commitment());
    let pending_pos = tree.push(pending.commitment());
    let anchor = tree.root();
    let pwit = |tree: &Tree, p: &vault::Pending, pos: u64| vault::PendingWitness { value: p.value.to_string(), vid: h(p.vid), policy_hash: h(p.policy_hash), pk_dest: h(p.pk_dest), h_req: p.h_req, t_req: p.t_req, delay_blocks: p.delay_blocks, delay_seconds: p.delay_seconds, r: h(p.r), index: pos, path: tree.path(pos) };
    let inbound_r = rep(0x4f);
    let cancel = VaultWitness {
        statement: "money-vault-cancel-v1".into(),
        chain_id,
        anchor: h(anchor),
        nullifiers: vec![h(money::nullifier(vault::nk_cancel(sk_c), pending.commitment(), pending_pos))],
        ptag: h(pending.ptag(pending_pos)),
        commitments: vec![h(vault::inbound_commitment(wv, head.vid, policy.hash(), inbound_r))],
        policy: Some(pw(&policy)),
        sk_c: h(sk_c),
        pending_in: Some(pwit(&tree, &pending, pending_pos)),
        inbound_r: h(inbound_r),
        ..Default::default()
    };
    sizes.push(run("cancel", &cancel));
    let mut wrong_key = cancel.clone();
    wrong_key.sk_c = h(rep(0x53));
    assert!(vault::prepare_vault(&wrong_key).is_err());
    let revoked = Tree::new(vault::REVOKED_DEPTH);
    let outs = [(wv - 7, rep(0x54), rep(0x55)), (7u128, rep(0x56), rep(0x57))];
    let complete = VaultWitness {
        statement: "money-vault-complete-v1".into(),
        chain_id,
        anchor: h(anchor),
        revoked_root: h(revoked.root()),
        nullifiers: vec![h(money::nullifier(nk_d, pending.commitment(), pending_pos))],
        ptag: h(pending.ptag(pending_pos)),
        commitments: outs.iter().map(|o| h(money::commitment(o.0, o.1, o.2))).collect(),
        height: 1300,
        time: 2_003_600,
        sk_dest: h(sk_d),
        nk_dest: h(nk_d),
        pending_in: Some(pwit(&tree, &pending, pending_pos)),
        outputs: outs.iter().map(|o| money::OutputWitness { value: o.0.to_string(), pk: h(o.1), r: h(o.2) }).collect(),
        revoked: Some(vault::RevokedWitness { low_key: 0, low_next: 0, low_index: 0, path: revoked.path(0) }),
        ..Default::default()
    };
    sizes.push(run("complete", &complete));
    let mut early = complete.clone();
    early.time = 2_003_599;
    assert!(refusal(&early).contains("matured"));
    // the revoked set holding this policy: non-membership cannot be shown
    let mut rt = Tree::new(vault::REVOKED_DEPTH);
    rt.push(vault::revoked_leaf(0, vault::revoked_key(policy.hash())));
    rt.push(vault::revoked_leaf(vault::revoked_key(policy.hash()), 0));
    let mut revoked_case = complete.clone();
    revoked_case.revoked_root = h(rt.root());
    revoked_case.revoked = Some(vault::RevokedWitness { low_key: 0, low_next: vault::revoked_key(policy.hash()), low_index: 0, path: rt.path(0) });
    assert!(refusal(&revoked_case).contains("non-membership"));
    // the period index cannot go back: the head at period 3 refuses a request declared into period 2
    let mut back = withdraw.clone();
    back.anchor = h(anchor);
    back.nullifiers = vec![h(money::nullifier(nk_v, head2.commitment(), head2_pos))];
    back.head_in = Some(hw(&tree, &head2, head2_pos));
    back.height = 700;
    back.time = 2_001_000;
    assert!(refusal(&back).contains("only advances"));
    // announce a new policy under the recovery authority at height 2000, co-signed by the everyday key
    // (F-800: the owner's rotation of a compromised cancellation key)
    let new_policy = vault::Policy { pk_e: money::pk(rep(0x61), rep(0x62)), pk_c: vault::pk_cancel(rep(0x63)), pk_a: vault::pk_recover(rep(0x64)), nk_field: vault::nk_field(rep(0x65)), delay_blocks: 600, delay_seconds: 7200, period: 600, limit: 500_000_000_000_000_000, recovery_blocks: 1200, recovery_seconds: 14400, nonce: rep(0x66) };
    let head3 = vault::Head { rec_hash: new_policy.hash(), rec_h: 2000, rec_t: 2_010_000, r: rep(0x67), co: 1, ..head2.clone() };
    let announce = VaultWitness {
        statement: "money-vault-announce-v1".into(),
        chain_id,
        anchor: h(anchor),
        nullifiers: vec![h(money::nullifier(nk_v, head2.commitment(), head2_pos))],
        commitments: vec![h(head3.commitment())],
        old_policy_hash: h(policy.hash()),
        height: 2000,
        time: 2_010_000,
        policy: Some(pw(&policy)),
        new_policy: Some(pw(&new_policy)),
        nk_v: h(nk_v),
        sk_a: h(sk_a),
        sk_e: h(sk_e),
        nk_e: h(nk_e),
        head_in: Some(hw(&tree, &head2, head2_pos)),
        head_out_r: h(head3.r),
        ..Default::default()
    };
    sizes.push(run("announce (co-signed)", &announce));
    let mut no_auth = announce.clone();
    no_auth.sk_a = h(rep(0x68));
    assert!(vault::prepare_vault(&no_auth).is_err());
    let mut bad_cosign = announce.clone();
    bad_cosign.sk_e = h(rep(0x68));
    assert!(refusal(&bad_cosign).contains("co-signature"));
    // F-799: the announcement reveals the old policy hash (the node revokes it in the announcing block); a
    // revealed word that is not the head's policy is refused
    let mut wrong_old = announce.clone();
    wrong_old.old_policy_hash = h(rep(0x70));
    assert!(refusal(&wrong_old).contains("revealed old policy hash"));
    // a withdrawal from the announced head is refused (no withdrawal while a recovery is pending)
    let head3_pos = tree.push(head3.commitment());
    let anchor = tree.root();
    let mut during = withdraw.clone();
    during.anchor = h(anchor);
    during.nullifiers = vec![h(money::nullifier(nk_v, head3.commitment(), head3_pos))];
    during.head_in = Some(hw(&tree, &head3, head3_pos));
    assert!(refusal(&during).contains("recovery is pending"));
    // the recovery is cleared. F-800: the announced policy changes pk_c and the owner co-signed, so the
    // cancellation key that it would rotate cannot cancel it; the recovery authority alone can
    let head4 = vault::Head { rec_hash: Word::default(), rec_h: 0, rec_t: 0, co: 0, r: rep(0x69), ..head3.clone() };
    let mut rec_cancel = VaultWitness {
        statement: "money-vault-rec-cancel-v1".into(),
        chain_id,
        anchor: h(anchor),
        nullifiers: vec![h(money::nullifier(nk_v, head3.commitment(), head3_pos))],
        commitments: vec![h(head4.commitment())],
        old_policy_hash: h(policy.hash()),
        policy: Some(pw(&policy)),
        new_policy: Some(pw(&new_policy)),
        nk_v: h(nk_v),
        sk_c: h(sk_c),
        head_in: Some(hw(&tree, &head3, head3_pos)),
        head_out_r: h(head4.r),
        ..Default::default()
    };
    assert!(refusal(&rec_cancel).contains("only the recovery authority"));
    rec_cancel.sk_a = h(rep(0x6b));
    assert!(refusal(&rec_cancel).contains("only the recovery authority"));
    rec_cancel.sk_a = h(sk_a);
    sizes.push(run("rec-cancel (the authority, pk_c rotated, co-signed)", &rec_cancel));
    // and a rotation nobody co-signed (a compromised authority's: the lab's run 4) is cancellable by the
    // cancellation key alone: announced on head4 (the chain's head after the cancellation) without sk_e,
    // cancelled by sk_c; the authority's key refused
    let kept_policy = new_policy.clone();
    let head4_pos = tree.push(head4.commitment());
    let anchor = tree.root();
    let head6 = vault::Head { rec_hash: kept_policy.hash(), rec_h: 2100, rec_t: 2_011_200, r: rep(0x6c), co: 0, ..head4.clone() };
    let announce_kept = VaultWitness {
        statement: "money-vault-announce-v1".into(),
        chain_id,
        anchor: h(anchor),
        nullifiers: vec![h(money::nullifier(nk_v, head4.commitment(), head4_pos))],
        commitments: vec![h(head6.commitment())],
        old_policy_hash: h(policy.hash()),
        height: 2100,
        time: 2_011_200,
        policy: Some(pw(&policy)),
        new_policy: Some(pw(&kept_policy)),
        nk_v: h(nk_v),
        sk_a: h(sk_a),
        head_in: Some(hw(&tree, &head4, head4_pos)),
        head_out_r: h(head6.r),
        ..Default::default()
    };
    sizes.push(run("announce (not co-signed)", &announce_kept));
    let head6_pos = tree.push(head6.commitment());
    let anchor = tree.root();
    let head7 = vault::Head { rec_hash: Word::default(), rec_h: 0, rec_t: 0, co: 0, r: rep(0x6d), ..head6.clone() };
    let mut rec_cancel_kept = VaultWitness {
        statement: "money-vault-rec-cancel-v1".into(),
        chain_id,
        anchor: h(anchor),
        nullifiers: vec![h(money::nullifier(nk_v, head6.commitment(), head6_pos))],
        commitments: vec![h(head7.commitment())],
        old_policy_hash: h(policy.hash()),
        policy: Some(pw(&policy)),
        new_policy: Some(pw(&kept_policy)),
        nk_v: h(nk_v),
        sk_c: h(rep(0x6e)),
        sk_a: h(sk_a),
        head_in: Some(hw(&tree, &head6, head6_pos)),
        head_out_r: h(head7.r),
        ..Default::default()
    };
    assert!(refusal(&rec_cancel_kept).contains("cancellation key"));
    rec_cancel_kept.sk_c = h(sk_c);
    sizes.push(run("rec-cancel (the cancellation key, a rotation not co-signed)", &rec_cancel_kept));
    let mut other_recovery = rec_cancel_kept.clone();
    other_recovery.new_policy = Some(pw(&vault::Policy { pk_c: policy.pk_c, ..new_policy.clone() }));
    assert!(refusal(&other_recovery).contains("not the new policy"));
    // a forged co-signature bit on the head witness does not open the head (the bit is in the commitment)
    let mut forged_bit = rec_cancel_kept.clone();
    forged_bit.head_in.as_mut().unwrap().co = 1;
    assert!(vault::prepare_vault(&forged_bit).is_err());
    // F-799: the cancellation reveals the hash the announcement revealed (the node restores it)
    let mut wrong_old = rec_cancel.clone();
    wrong_old.old_policy_hash = h(rep(0x70));
    assert!(refusal(&wrong_old).contains("revealed old policy hash"));
    // or the recovery completes after its delay: the old policy hash revealed, the head under the new policy
    let head5 = vault::Head { policy_hash: new_policy.hash(), period_idx: 2600 / 600, spent: 0, rec_hash: Word::default(), rec_h: 0, rec_t: 0, co: 0, r: rep(0x6a), ..head3.clone() };
    let policy_complete = VaultWitness {
        statement: "money-vault-policy-complete-v1".into(),
        chain_id,
        anchor: h(anchor),
        nullifiers: vec![h(money::nullifier(nk_v, head3.commitment(), head3_pos))],
        commitments: vec![h(head5.commitment())],
        old_policy_hash: h(policy.hash()),
        height: 2600,
        time: 2_017_200,
        policy: Some(pw(&policy)),
        new_policy: Some(pw(&new_policy)),
        nk_v: h(nk_v),
        head_in: Some(hw(&tree, &head3, head3_pos)),
        head_out_r: h(head5.r),
        ..Default::default()
    };
    sizes.push(run("policy-complete", &policy_complete));
    let mut too_soon = policy_complete.clone();
    too_soon.height = 2599;
    assert!(refusal(&too_soon).contains("delay has not passed"));
    for (n, (bytes, _)) in sizes.iter().enumerate() {
        assert!(*bytes > 10_000 && *bytes < 256 * 1024, "statement {n}: proof {bytes} bytes");
    }
}
