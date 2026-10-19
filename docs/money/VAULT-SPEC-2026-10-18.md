# Vault specification: the protected balance inside the shielded pool (Project Money, 18 October 2026)

Status: DESIGN, on paper; PANEL-READ 18 October 2026 (docs/money/PANEL-READING-2-2026-10-18.md: PASS with re-cuts F-777 to F-781, applied). Stage 5 and stage 6 of ADR-0001 section 6. Nothing here is built; the ADR's verdict (section 4.6, "Money remains research") binds this page. Every construction below is stated so that an engineer can cut it without a question; every figure that is not a rule is marked ESTIMATE. The ADR's definitions are used unchanged: H is the in-circuit hash, B is BLAKE3, a note is (v, pk, r) with cm = H(v, pk, r), nf = H(nk, cm, position), the tree has depth 32, the anchor is its root, the turnstile is the pool's public running count. Amounts are in wei, 128 bits carried as four 32-bit limbs (the stage-one join's value rule, docs/money/STAGE1-JOIN-2026-10-18.md section 4). No em dash appears on this page. No price or earning figure appears or can be derived.

## 1. The decision

A vault is one note in the shielded pool, the head, whose commitment binds a policy hash and a running total of the period's withdrawals. Every operation on a vault consumes its head and emits a new head under a statement whose verifying key the node knows, so the chain serialises the vault's operations by the head's nullifier, and the rule that binds each operation is the statement itself, not the wallet. A withdrawal emits a pending note that matures after a delay counted in blocks and in seconds, both binding, the later deciding. The cancellation key consumes a pending note and returns its amount to the vault, and can do nothing else. The recovery authority replaces the policy as a whole through an announced, delayed, cancellable process, and its completion revokes every pending withdrawal requested under the old policy. The everyday key never appears in any statement that can change the policy hash, which is the proof, from public inputs, that it cannot shorten a delay, raise a limit or replace the recovery authority. Fees are never paid from the vault.

The one figure this page is asked for (section 9): after the everyday key is stolen, the thief takes at most W, the working balance of the everyday wallet, and nothing from the vault, provided the cancellation key acts inside the withdrawal delay D. Without a cancellation key, the thief takes at most W plus L times (floor((R minus D + 64) over P) + 2), where L is the period limit, P the period, R the recovery delay and 64 the declaration margin of section 4.2 (the count of period indices a window of that length can touch; section 9 gives the common case); and with neither a cancellation key nor a recovery authority, at most W plus L a period until the owner withdraws the rest under the closure delay. Fees, splitting, concurrent requests and reorganisations do not raise those bounds; section 9 shows each.

## 2. The three authorities

| authority | holds | can | cannot |
|---|---|---|---|
| everyday key (sk_e, with nk_e; pk_e = H(sk_e, nk_e)) | the everyday wallet's seed | spend the working balance (ordinary notes); create a vault; merge inbound notes into the head; request a withdrawal; complete a matured withdrawal to its destination; reclaim a revoked pending note into the vault | cancel a pending withdrawal; change any field of the policy; announce, cancel or complete a recovery; shorten any delay; pay a fee from the vault |
| cancellation key (sk_c; pk_c = H(sk_c, "cancel")) | a second device, a paper key, or a person the owner trusts to say no | cancel a pending withdrawal (its amount returns to the vault); cancel a pending recovery or policy change | spend anything; redirect a withdrawal; change the policy; start a recovery |
| recovery authority (A: one offline hash-signature key, or a threshold t of n such keys) | a cold device, or the chosen people, or nothing (the owner chose none) | announce a new policy (new keys, new delays, new limit, new authority) that applies after the recovery delay R unless cancelled | move funds; shorten the delay of a pending withdrawal; act without the delay; act without the vault's viewing key (section 4.4) |

Each authority's secret is a hash preimage or a hash-based one-time signature key (section 4.3); no curve anywhere, as ADR section 4.3 requires. The viewing key of the vault (section 4.4) is held by the everyday wallet, by the cancellation key's holder and by the recovery authority; it reads, it never spends.

## 3. Objects

### 3.1 The policy

policy = (pk_e, pk_c, nk_field, A_root, t, n, D_b, D_s, P, L, R_b, R_s, X_b, X_s, policy_nonce)

| field | meaning | floor or rule |
|---|---|---|
| pk_e | the everyday key's commitment | any |
| nk_field | H(nk_v), the hash of the nullifier key that consumes the head under this policy (section 4.4); a new policy carries the new owner's |
| pk_c | the cancellation key's commitment, or the all-zero word for "no cancellation key" | the wallet says "not protected: no cancellation key" (ADR section 8.5) |
| A_root, t, n | the Merkle root (depth 4, over H) of n recovery keys and the threshold t; A_root all-zero and n = 0 for "no recovery authority" | 1 <= t <= n <= 16 |
| D_b, D_s | the withdrawal delay in blocks and in seconds | D_b >= 300 and D_s >= 3,600 (the fork choice's bound is twelve blocks, docs/money/PANEL-READING-2026-10-18.md and the review lead's rule of record; 300 is twenty-five times it) |
| P | the period in blocks | P >= D_b |
| L | the limit: the most that may be requested in one period index | any; 0 means nothing may be withdrawn except by closure |
| R_b, R_s | the recovery and policy-change delay in blocks and seconds | R_b >= 2 D_b and R_s >= 2 D_s |
| X_b, X_s | the closure delay: a withdrawal of the whole balance under the everyday key, allowed only when no recovery authority is set | X_b >= 7 D_b and X_s >= 7 D_s; when A_root is set, X_b = X_s = 0 and closure is refused |
| policy_nonce | 256 random bits, fresh at every policy | makes policy_hash unguessable |

policy_hash = H("policy", every field in this order). The hash is never a public input of any statement except S_rec_complete (section 4.2), where the OLD policy hash is revealed into the revoked set.

Defaults the wallet offers (the owner may change them above the floors): D_b 7,200 (one day at twelve seconds), D_s 86,400; P 7,200; R_b 21,600 (three days), R_s 259,200; X_b 50,400 (seven days), X_s 604,800.

### 3.2 The vault head

head = (v, vid, policy_hash, period_idx, spent, rec_hash, rec_h, rec_t, nk_field, r)

| field | meaning |
|---|---|
| v | the vault's balance, 128 bits as four 32-bit limbs |
| vid | the vault id, 256 bits, chosen at creation, fixed for the vault's life |
| policy_hash | the current policy |
| period_idx | the period index of the last withdrawal request |
| spent | the amount requested in period_idx (the running total) |
| rec_hash, rec_h, rec_t | the announced new policy hash and the block height and timestamp of the announcement; all zero when no recovery is pending |
| nk_field | H(nk_v): names the nullifier key that consumes this head; replaced only by S_rec_complete |
| r | 256 bits of randomness, fresh for every head |

cm_head = H("head", every field in this order). The head's nullifier is nf = H(nk_v, cm_head, position) in every statement that consumes a head, with nk_v the vault's own nullifier key (section 4.4), which every holder of the viewing key knows and which authorises nothing: the authorisation of each statement is its own witness (sk_e, sk_c or the guardians' signatures). One nullifying key per head means one nullifier per head, so no two statements can consume the same head on one chain.

A head is in the same tree as every other commitment and is indistinguishable from an ordinary note on the ledger. The domain word "head" in the hash keeps a head from being spent by the ordinary transfer statement S_transfer once that statement carries a domain word of its own: the stage-one join's money-spend-v1 checks cm = H(H(v, pk), r) with no domain word, so until the vault cut re-cuts the note form as money-spend-v2 with the word "note", the separation rests on a head's preimage having another shape, that is on H's second-preimage resistance alone, and the vault cut makes it a separation by construction.

### 3.3 The pending note

pending = (w, vid, policy_hash, pk_dest, h_req, t_req, D_b, D_s, r)

cm_pending = H("pending", every field). Its nullifier uses nk of pk_dest's holder for completion and nk_c (the cancellation key's) for cancellation: nf = H(nk_x, cm_pending, position) where nk_x is the key of the statement that consumes it; the two nullifiers differ, and the node refuses a second consumption because both statements also consume the pending note's position through a second public input, the position tag ptag = H("ptag", cm_pending, position, r), with r the pending note's randomness, which both consumers read from the note's ciphertext and nobody else holds (a tag without r would be computable by anyone from the public commitment and its position, and would link a completion or a cancellation to the request that made the note on the ledger, F-779), which the node keeps in the nullifier set beside the nullifiers. One pending note, two possible consumers, one position tag: whichever lands first wins and the other is refused as a duplicate.

### 3.4 The inbound note

inbound = (v, vid, policy_hash, r), cm_inbound = H("inbound", every field). Created by a deposit from anyone who knows the vault's address (section 4.4), by a cancellation, and by a reclaim. Merged into the head by the everyday key. An inbound note is never spendable to anywhere but the head of the vault whose vid and policy_hash it names.

### 3.5 The revoked set

A public, append-only set of policy hashes revealed by S_rec_complete, kept by every node as an indexed Merkle tree over H (the sorted-leaf form that proves non-membership with one path and one neighbour; Aztec's nullifier tree is the cited form). Its root, revoked_root, is a public input of S_complete, which proves that the pending note's policy_hash is not in the set. The node accepts only the revoked set's root as it stands at the parent of the including block: a completion proven against an older root is refused and re-proven, which is rare because recoveries are rare, and a revocation therefore binds from the block after S_rec_complete lands (a window of recent roots, as the pool's anchor has, would let a pending note of a revoked policy complete for the length of the window by a proof against a stale root, F-778). The pool's own anchor window is its own rule: 64 recorded roots in the stage-one cut.

## 4. The statements

Every statement is one STARK proof with the ADR's proof system, a verifying key the node pins by a statement id (one byte in the transaction type), and the public inputs below. Every statement also takes the ADR's transfer fields for its fee: up to two ordinary input notes, up to two ordinary output notes, the fee, tx_hash, and the anchor; the fee is paid from those ordinary inputs and the conservation check is inputs = outputs + fee over the ordinary notes alone. A vault's balance is never on the fee side of any statement. The node checks, for every statement, the duplicate rules of the ADR (nullifiers and position tags), the anchor's age, and the height and time rules named per statement. Public inputs are listed; everything else is witness.

### 4.1 Creation, deposit and merge (the everyday key)

**S_vault_create.** Public: anchor, nf of each ordinary input, cm_head_out, fee, tx_hash. Circuit: the ordinary inputs are in the tree and owned by sk_e (pk = H(sk_e, nk_e)); v_head = inputs minus outputs minus fee, in range; policy opens to policy_hash with pk_e equal to the inputs' pk (the creator's everyday key is the policy's everyday key); period_idx = 0, spent = 0, rec fields 0, nk_field = the policy's nk_field = H(nk_v) with nk_v proven from the creator's seed; cm_head_out = H("head", ...). Floors of section 3.1 checked in circuit.

**S_deposit.** Public: anchor, input nullifiers, cm_inbound_out, fee, tx_hash. Circuit: ordinary inputs owned by the prover; cm_inbound_out = H("inbound", v, vid, policy_hash, r) for a vid and policy_hash the prover was given (the vault's address, section 4.4). Anyone may deposit.

**S_merge.** Public: anchor, nf_head_in, nf of each inbound (up to four), cm_head_out, fee, tx_hash. Circuit: the head and the inbound notes are in the tree; each inbound's vid and policy_hash equal the head's; the prover knows sk_e for the head's policy (the policy opens to policy_hash and pk_e = H(sk_e, nk_e)); head_out equals head_in in every field except v (the sum) and r. A merge changes no delay, no total and no policy.

### 4.2 Withdrawal, completion, cancellation, reclaim

**S_withdraw.** Public: anchor, nf_head_in, cm_head_out, cm_pending_out, h_decl, t_decl, fee, tx_hash. Circuit:

1. head_in is in the tree; the policy opens to head_in.policy_hash; pk_e = H(sk_e, nk_e) (the authorisation); nf_head_in = H(nk_v, cm_head_in, position) with nk_v = H(s, "vault-nullifier", vid) proven from the same seed as sk_e.
2. rec_hash of head_in is zero (no withdrawal while a recovery is pending; section 7 says why).
3. idx = floor(h_decl / P). If idx = head_in.period_idx then spent_new = head_in.spent + w, else spent_new = w. spent_new <= L. (The running total: hidden inside the head, carried forward by every withdrawal, never revealed.)
4. w <= head_in.v; head_out = head_in with v minus w, period_idx = idx, spent = spent_new, fresh r.
5. pending_out = (w, vid, policy_hash, pk_dest, h_decl, t_decl, D_b, D_s, r') with D_b and D_s copied from the policy; pk_dest is any key the prover chooses (the everyday key's own, or a payee's).
6. Closure form: if the policy's A_root is zero and the prover sets the closure flag (a witness bit), w may equal head_in.v with spent unchecked, and the pending note's delays are X_b and X_s in place of D_b and D_s; head_out then has v = 0. If A_root is set, the flag is refused.

Node rules: h_incl <= h_decl <= h_incl + 64 and ts(h_incl) <= t_decl <= ts(h_incl) + 768, where h_incl is the including block and ts its header timestamp. A request declared below its including height is invalid; the declared height is therefore never before inclusion, so the maturity h_decl + D_b is never before h_incl + D_b. The wallet declares the head it expects plus a margin, and a request not included by h_decl + 0 expires from the mempool (the mempool's expiry for this type: a shielded transaction is dropped at h_decl, and a new one proven; stage 1's relay item in the ADR).

**S_complete.** Public: anchor, revoked_root, nf_pending (under nk_dest), ptag, cm_out_1, cm_out_2, h_now, t_now, fee, tx_hash. Circuit: the pending note is in the tree; pk_dest = H(sk_dest, nk_dest); h_now >= h_req + D_b AND t_now >= t_req + D_s; policy_hash of the pending note is not in the revoked set under revoked_root (one indexed-tree non-membership path); the outputs are ordinary notes summing to w (the fee from the ordinary fee inputs as always, so that a payee with no other notes pays the fee from w: allowed as the one exception, in which case the fee inputs may be empty and outputs + fee = w). Node rules: h_now <= h_incl and t_now <= ts(h_incl). Under-declaring only delays the prover; over-declaring is refused. Both bounds bind; the later one decides.

**S_cancel.** Public: anchor, nf_pending (under nk_c), ptag, cm_inbound_out, fee, tx_hash. Circuit: the pending note is in the tree; the policy of the vault opens to the pending note's policy_hash and pk_c = H(sk_c, "cancel") with pk_c not all-zero; the inbound note = (w, vid, policy_hash, r''). Nothing else: no destination, no amount choice, no policy field. The cancellation does not restore the period's allowance (the head is untouched; a cancelled request still counts in spent). That is deliberate: a thief who requests and is cancelled has used the period's allowance and taken nothing; the owner's remedy is the recovery authority, section 9.

**S_reclaim.** Public: anchor, nf_pending (under nk_e of the NEW policy), ptag, revoked_root, cm_inbound_out, fee, tx_hash. Circuit: the pending note's policy_hash IS in the revoked set (a membership path); the prover knows sk_e of the vault's current policy, proven by opening a head of the same vid in the tree whose policy_hash's pk_e matches (a read-only membership of the head, no nullifier); the inbound note carries the current policy_hash. A revoked pending note can never complete (S_complete proves non-membership); reclaim is its only exit.

### 4.3 Recovery and policy change

A policy change and a recovery are one mechanism: the replacement of the policy as a whole by the recovery authority, with the delay R. A change of delays or limits alone is a replacement that keeps pk_e and pk_c; a key replacement is one that changes them. A vault with no recovery authority has no policy change; its exits are the limited withdrawal and the closure form.

**S_rec_announce.** Public: anchor, nf_head_in, cm_head_out, h_decl, t_decl, fee, tx_hash. Circuit: head_in in the tree, nullified under nk_v as always (the prover may not hold sk_e; it holds the viewing key, section 4.4); the policy opens; A_root is not zero; t signatures verify: for t distinct leaf indices i_1 < ... < i_t under A_root, a hash-based one-time signature (WOTS+ over H, parameters w = 16, 67 chains for a 256-bit message, with each guardian's key an XMSS-style tree of depth 10 of such one-time keys, the leaf root under A_root; the circuit verifies the chain hashes and the two paths) over msg = H("recover", vid, new_policy_hash, h_decl); new_policy opens to new_policy_hash with floors checked and its policy_nonce fresh; head_out = head_in with rec_hash = new_policy_hash, rec_h = h_decl, rec_t = t_decl, fresh r. Node rules on h_decl and t_decl as S_withdraw's. The one-time keys used are consumed by the guardian's own index; a cancelled or completed recovery costs each signer one leaf of its 1,024.

**S_rec_cancel.** Public: anchor, nf_head_in, cm_head_out, fee, tx_hash. Circuit: the policy opens; pk_c = H(sk_c, "cancel"), not all-zero; rec_hash not zero; head_out = head_in with the rec fields zero, fresh r.

**S_rec_complete.** Public: anchor, nf_head_in, cm_head_out, old_policy_hash, h_now, t_now, fee, tx_hash. Circuit: the policy opens to head_in.policy_hash and equals old_policy_hash (revealed); rec_hash not zero; h_now >= rec_h + R_b AND t_now >= rec_t + R_s; the new policy opens to rec_hash; head_out = (v, vid, rec_hash, period_idx = floor(h_now / P_new), spent = 0, rec fields zero, nk_field = the new policy's nk_field, fresh r). Node rules: h_now <= h_incl, t_now <= ts(h_incl); the node appends old_policy_hash to the revoked set in the same block. Anyone with the viewing key may complete; the new everyday key is whichever the new policy names.

What is revealed by a recovery: one hash, old_policy_hash, which no earlier statement ever published, so it links to nothing on the ledger. The wallet's rule: "recovery in progress" is shown from the head's ciphertext (ADR section 8.3).

### 4.4 Keys, viewing and addresses of a vault

From the everyday wallet's seed s (ADR section 4.3): sk_e = H(s, "spend"), nk_e = H(s, "nullifier"), and the vault's own keys: nk_v = H(s, "vault-nullifier", vid) and the vault's ML-KEM-768 key pair from the seed H(s, "vault-kem", vid). After a recovery the new policy's everyday seed derives a new nk_v' = H(s', "vault-nullifier", vid), and S_rec_complete's head_out is nullifiable under nk_v' only: the circuit binds head_out's nullifier key by committing nk_v into the head (the head's nk_field, equal to H(nk_v), checked by every consuming statement against the prover's nk_v, and copied from the policy at creation and at recovery completion). Every head, pending and inbound note is encrypted (ADR section 4.3 item 6) to the vault's KEM key, and a pending note additionally to pk_dest's KEM key. The vault's viewing key vk = (vid, policy, nk_v, dk_vault) is given by the owner to the cancellation key's holder and to the recovery authority at creation and at every policy change; it lets them see the vault's balance, its pending withdrawals and a pending recovery, and spend nothing. The vault's deposit address is (vid, policy_hash, ek_vault); a depositor who holds it sees nothing but the fact of its own deposit.

The cancellation key sk_c is not derived from the everyday seed: it is a separate 32-byte secret with its own 24 words, generated on a second device or printed, so that the everyday wallet's compromise does not compromise it. A recovery key is likewise its own secret. The everyday wallet's seed restores the everyday balance, the vault's viewing key and the vault's head; it does not restore sk_c or any recovery key, and the wallet says so at creation.

## 5. The state machine

The head (one per vault, always exactly one live commitment in the tree):

| state | fields | moves to | by |
|---|---|---|---|
| Open | rec_hash = 0 | Open | S_merge, S_withdraw (the head is replaced; the pending note is born) |
| Open | | RecoveryPending | S_rec_announce |
| RecoveryPending | rec_hash != 0 | Open, same policy | S_rec_cancel |
| RecoveryPending | | Open, new policy, period reset | S_rec_complete (old policy into the revoked set) |
| RecoveryPending | | refused | S_withdraw, S_rec_announce (one at a time) |
| any | | | S_merge is allowed in both states |

A pending note:

| state | condition | moves to | by |
|---|---|---|---|
| Pending | h < h_req + D_b or t < t_req + D_s | Cancelled (an inbound note exists) | S_cancel under sk_c |
| Pending | both delays passed, policy not revoked | Completed (ordinary notes to pk_dest) | S_complete under sk_dest |
| Pending | policy revoked by S_rec_complete | Reclaimed (an inbound note) | S_reclaim under the new sk_e |
| Pending, matured, not revoked | | Cancelled | S_cancel still works: a matured note that nobody completed can still be cancelled |
| Completed, Cancelled, Reclaimed | terminal | | the position tag is in the nullifier set |

An inbound note: Unmerged, then Merged (consumed by S_merge). It has no other state.

## 6. What the ledger, the recipient and the wallet see

The ledger: commitments, nullifiers, position tags, proofs, ciphertexts, the statement id of each transaction, and for S_rec_complete one revealed hash. The statement id reveals that a withdrawal, a cancellation, a completion or a recovery took place somewhere in the pool; it does not reveal which vault, which amount or which key. (The count of each statement type per block is public; section 10 says what is not claimed from it.)

The recipient of a withdrawal (pk_dest): at request time, a ciphertext opens to the pending note, with h_req, t_req, D_b, D_s and vid; the recipient's wallet shows "pending: w, matures at block h_req + D_b and not before <local time of t_req + D_s>, cancellable until then by the vault's cancellation key" and shows the amount as pending, never as received. After maturity the recipient completes it with S_complete and the ordinary outputs show as the wallet's existing rows (submitted, included, work on top; nothing called final, docs/product-b/DESIGN.md). If a cancellation lands, the recipient's wallet sees the position tag in the nullifier set and shows "cancelled". A recipient therefore never counts a pending withdrawal as settled: settled means completed and included with work on top.

The owner's wallet (ADR section 8): available (ordinary notes), protected (head.v, with D and L named), leaving (each pending note with its deadline and who can cancel), being recovered (rec_hash with who announced, from the signatures' leaf indices, and when it completes).

## 7. The rules, each with the reason

1. One live head per vault; every operation but deposit consumes it. Reason: the running total must be one number, so splitting a vault into two heads would double the allowance; the chain's duplicate-nullifier rule makes the serialisation free.
2. The running total lives in the head, hidden; spent_new <= L is proven, never revealed. Reason: the ADR's section 2.2 rolling commitment; the sum of recent withdrawals is proven by carrying it, not by summing public data.
3. The period index is floor(h_decl / P) with h_decl at or after inclusion. Reason: a thief can at most use the next period's allowance up to 64 blocks early; it cannot use a period twice, because the index only advances.
4. Both delays bind, the later deciding. Reason: a burst of fast blocks cannot shorten the time, and a slow or stalled chain cannot shorten the block count; header timestamps are bounded by the node's drift rule (the parent's plus one at least, the local clock plus the allowed drift at most), and block heights by the fork choice's twelve-block bound.
5. Fees come from ordinary notes in the same transaction, never from the vault. Reason: fee draining then drains the working balance only, which the thief has anyway.
6. No withdrawal while a recovery is pending. Reason: a thief who sees a recovery coming cannot start a last request; the owner's cancellation of the recovery (if the recovery is the thief's) reopens withdrawals.
7. A completed recovery revokes every pending note of the old policy. Reason: the thief's requests, still waiting out D, die with the policy; the bound of section 9 falls to W when R <= D.
8. The cancellation does not restore the allowance. Reason: it keeps S_cancel free of the head; a thief's cancelled request costs the owner one period's allowance, never money.
9. The closure form exists only without a recovery authority, with a delay of at least 7 D. Reason: a vault must have an exit; a thief's closure is cancellable for seven times the ordinary delay.
10. The viewing key is shared with the cancellation key's holder and the recovery authority. Reason: a pending withdrawal must be visible to whoever can cancel it, and a recovery must be provable by whoever can announce it; the ledger shows them nothing.

## 8. The reorganisation rule

The chain's fork choice bounds a reorganisation at twelve blocks (the review lead's rule of record; docs/money/PANEL-READING-2026-10-18.md cites it). Every delay floor on this page is at least twenty-five times that bound. The rules:

1. A request, cancellation, completion, announcement or recovery completion that is reorganised out is not in the tree and has no effect; the wallet detects the missing commitment on its next scan and re-proves against a new anchor. A re-proven request carries a new h_decl at or after its new inclusion, so its maturity is later than the first attempt's, never earlier.
2. A pending note's maturity is fixed in its commitment at request time and is unchanged by any reorganisation: the completion proves h_now and t_now against the including block, so a reorganisation that replaces blocks after the request cannot bring maturity forward; at most it delays completion.
3. A cancellation and a completion of the same pending note that land on different forks resolve by the fork choice; after it, exactly one of the two position tags is in the canonical nullifier set and the other transaction is refused on re-import as a duplicate. The cancellation key's holder, seeing a completion on a short fork inside the twelve-block bound, resubmits its cancellation; the completion can only have been proven after maturity, so the cancellation was already late by the owner's own delay, and this case is the owner's late cancellation, not a shortened delay.
4. The revoked set follows the canonical chain: a reorganisation that removes an S_rec_complete removes its hash from the set (the indexed tree is rebuilt from the canonical blocks, as the nullifier set is), and a completion proven against the removed revoked_root is refused on the new chain because that root is not the parent's.
5. The node keeps the position tags and nullifiers per block and rolls them back with the block, as it does today for the public state.

## 9. The stolen everyday key, answered in the spec's own terms

Notation: W is the sum of the everyday wallet's ordinary notes at the moment of theft, including notes the thief later receives as change. L, P, D, R, X as in section 3.1. The thief holds sk_e, nk_e, the vault's viewing key (it is in the everyday wallet) and every ordinary note. The thief does not hold sk_c (a separate secret, section 4.4) or any recovery key.

| the thief's move | what the statement allows | what the thief takes |
|---|---|---|
| spend the working balance | S_transfer, no delay | W, in full, at once; this is the everyday wallet's loss and no vault rule touches it |
| request a withdrawal to its own key | S_withdraw: w <= L minus spent in the current period; matures after D_b blocks and D_s seconds | nothing until maturity; the request is visible to the owner's other wallet and to the cancellation key's holder within one scan (the head and pending ciphertexts) |
| request many small withdrawals (splitting) | each consumes the head and adds to spent; spent_new <= L is proven in every one | at most L per period index in total; the second request of the same head is refused as a duplicate nullifier, so they are serial, never concurrent |
| declare h_decl 64 blocks ahead into the next period | allowed once the index advances | the next period's L, never a period twice |
| pay the fees from the vault | no statement takes the head as a fee input | nothing: the fees come from W, and when W is exhausted no vault statement can be proven by the thief |
| shorten D, raise L, replace A, change P | no statement with sk_e in its witness changes policy_hash: S_withdraw and S_merge copy policy_hash from head_in to head_out as a circuit constraint; the only statements that write a new policy_hash are S_rec_announce (t guardian signatures required) and S_rec_complete (rec_hash required to be set, and R to have passed) | nothing; the node refuses the proof, because the verifying key for S_withdraw admits no policy change, and that is checked from the statement id and the public inputs alone |
| announce a recovery to a policy of its own | S_rec_announce requires t one-time signatures under A_root | nothing without the guardians' secrets |
| cancel the owner's recovery | S_rec_cancel requires sk_c | nothing |
| complete a pending note early | S_complete proves h_now >= h_req + D_b and t_now >= t_req + D_s, with h_now <= h_incl and t_now <= ts(h_incl) | nothing before both have passed |
| close the vault | S_withdraw's closure form, only when A_root is zero, with delay X >= 7 D | nothing before X; cancellable by sk_c throughout |
| reorganise | section 8 | no delay shortened; a request reorganised out restarts |

The bound, by what the owner holds:

| the owner holds | the maximum the thief takes before the owner's action lands | the owner's action |
|---|---|---|
| a cancellation key (acts inside D) | W | S_cancel on each pending note; then S_rec_announce to a new policy, which revokes the rest; the vault loses nothing |
| no cancellation key, a recovery authority | W + L times (floor((R minus D + 64) over P) + 2) at the worst alignment of the theft against the period boundaries; with the defaults (D one day, P one day, R three days) W + 3 L in the common case and W + 4 L at the worst alignment (the theft in a period's last blocks, the requests at the end of one period and the start of the next, and the 64-block declaration margin reaching one more) | S_rec_announce at once; every request whose maturity h_decl + D_b is at or before h_announce + R_b completes before the revocation binds; every later one is revoked by S_rec_complete |
| a cancellation key and no recovery authority | W | S_cancel on each pending note, every period, until the owner moves the balance out through its own withdrawals or the closure form; the thief can keep requesting; the owner's remedy is a new vault under new keys, funded by the owner's own matured withdrawals |
| neither | W + L per period, and the whole balance after X by closure | none inside the chain; the vault was a delay and nothing more, and the wallet said so at creation |

The one figure, in the spec's terms: with a cancellation key, the thief takes W and the vault loses zero; the vault's loss is bounded by L times (floor((R minus D + 64) over P) + 2) when only the recovery authority acts (3 L in the common case with the defaults), and by L a period when nothing acts. Fees, splitting, concurrency and reorganisations change none of these, by rules 1, 3, 5 and section 8.

## 10. What is not claimed

- Nothing here is measured: the circuit sizes of ten statements, the WOTS+ verification cost in rows (ESTIMATE: about 1,100 H evaluations per signature, so under 2^16 rows per signature at the ADR's hash; t up to 16 makes S_rec_announce the largest statement, ESTIMATE 2^20 rows, which may need the recovery to be proven on a PC, never a phone), the proving times, the proof sizes.
- The count of each statement id per block is public. On a small pool a lone S_cancel in a block, following a lone S_withdraw, is a visible pair; no unlinkability of vault operations by timing is claimed. The measurement is in docs/money/TEST-PLAN-2026-10-18.md group K.
- The recovery authority and the cancellation key's holder see the vault's balance and its pending states through the viewing key. A guardian is a party who sees your savings; the wallet says so when the owner names one.
- A recovery reveals one hash; the number of recoveries per block is public. The position tag of a completion or a cancellation is not computable from the ledger (section 3.3), so the ledger pairs neither with the request that made the pending note; the count of each statement id per block remains the one timing signal.
- The everyday wallet's seed does not restore sk_c or a recovery key; a holder who loses the cancellation key's words has no cancellation key, and the remedy is a policy change under the recovery authority.
- The per-diversifier address derivation (F-756) is unsettled; a vault's deposit address carries ek_vault in the clear, and two deposit addresses of one vault are the same address.
- No claim is made about the public side, about network metadata, or about the PQ property of the whole until the ADR's review of the complete path; this page adds WOTS+ over H to that path and names it as hash-only.
- Tests do not establish an unproven construction's security; the test plan finds bugs.

## 11. The tests that prove it

Each is a row in docs/money/TEST-PLAN-2026-10-18.md; the ids here are that page's.

- V-01 to V-06 (group D): the everyday key's refusals: policy change, early completion, splitting over L, fee from the vault, closure with an authority set, withdrawal during a pending recovery.
- V-07 to V-09 (group E): cancellation returns the amount; cancellation cannot redirect (no public input or witness names a destination; the circuit's inbound note is fixed to the vault); cancellation of a matured note.
- V-10 to V-13 (group F): recovery announce, cancel, complete; revoked pending notes refused at S_complete and accepted at S_reclaim.
- V-14 to V-17 (group H): reorganisations around each of request, cancellation, completion, recovery completion; the delay never shortened; the revoked set rebuilt.
- V-18 (group D, also B-05): the head's serialisation: two requests proven against one head, one lands, the other refused; the ledger's nullifier set has exactly one entry for the head.
- V-19 (group D): the bound of section 9 reproduced on the research chain with the defaults: W taken, the vault untouched with a cancellation key; W + 2 L without one.
- V-20 (group D): the viewing key sees pending states; the ledger, scanned without it, shows only statement ids.

## 12. Sources

- docs/money/ADR-0001-PRIVATE-PQ-SAFE-MONEY-2026-10-18.md (the pool's construction, section 4.3; the vault's rules, section 2.2; the theft-containment row, section 3; the campaign, section 7; the wallet's rules, section 8)
- docs/money/PANEL-READING-2026-10-18.md (F-756, the address linkability; F-757, the policy change's authority; the twelve-block fork-choice bound as the review lead's rule of record)
- docs/product-b/DESIGN.md (the wallet's settlement words: submitted, included, work on top; nothing called final)
- docs/reliability/DEVNET.md (the minimum node and the G1 budget)
- Indexed Merkle trees for non-membership: https://docs.aztec.network/aztec/concepts/advanced/storage/indexed_merkle_tree
- WOTS+ and XMSS (RFC 8391): https://www.rfc-editor.org/rfc/rfc8391
- SLH-DSA / FIPS 205 (the WOTS+ parameter form, for reference): https://csrc.nist.gov/pubs/fips/205/final
- Zcash ZIP 209 (the turnstile's form): https://zips.z.cash/zip-0209
- Safe recovery with guardians, Candide (the delay-and-cancel form in public): https://docs.candide.dev/wallet/plugins/recovery-with-guardians
