# Money stage two, cut one: the vault inside the shielded pool (the seven statements, the node's rules, the wallet's commands, and the stolen-key experiment run for real)

Status: PANEL-READ 19 October 2026 (docs/money/PANEL-READING-3-2026-10-19.md, section 5): PASS WITH RE-CUTS as a measured experiment, HELD OFF THE LANDING until the cured withdraw statement (finding 1, F-798) is re-run on a lab chain with one alternation row and its take read within the bound; the four findings numbered F-798 to F-801 (the period-index hole, cured here and not re-run; rule 7's revocation that never binds under the floors; the cancellation key that can cancel its own rotation; the head race), the first a fault of this cut, the other three of the spec. The cut's own verdict stands as written below: FAIL by one wei on the chain run, the hole cured after the run. The ADR's verdict stands: Money remains research. No privacy claim and no PQ claim is made here beyond what the ADR and the join page already say; the vault's claim is the one of docs/money/VAULT-SPEC-2026-10-18.md section 9, measured below.

Date: 18 to 19 October 2026, 00:1x to 02:3x UK (the lab 23:58 to 02:02 UK). Branch: lead/money-vault from main fab08c12e (the sha in the message to main). Lab chain 7404 on lab box A (AMD EPYC 9454P, 96 threads, no GPU), three nodes, the pinned prover, eleven real wallets. Every figure below was measured on lab box A unless marked ESTIMATE; nothing ran on the Mac. No em dash appears on this page. No price or earning figure appears or can be derived.

## 1. FAIL, by one wei, and what it found

**FAIL.** The measured take exceeded the spec's bound in one run by one wei, and the wei is a hole in the spec's own withdrawal rule, found by the experiment and cured in the circuit after the run: in run 3 (the owner silent across three periods) the thief took W + 4 L + 1 wei against the rule "L a period index"; the extra wei came from a request declared BACK into the current period after one declared ahead into the next through the 64-block margin. The spec's S_withdraw step 3 ("if idx = head_in.period_idx then spent_new = spent + w, else spent_new = w") lets the index go backwards, and a thief who alternates the two declarations inside the margin window opens a fresh allowance on every alternation: the bound is then V inside 64 blocks, not L a period. The cure is one assertion, idx >= period_idx (the spec's rule 3 as it is worded, "the index only advances", which the step's arithmetic did not enforce); it is in the circuit, the host check and the wallet of this branch (section 8, finding 1) and covered by the prover's test; it was NOT re-run on the chain in this session, so the chain figures below are the spec's rule as written.

Everything else held. Every other run's take was at or under its bound: with a cancellation key acting inside D the thief took W = 2 IGN and the vault lost nothing (run 1); with no cancellation key and the recovery authority announcing at once, W + 1 L = 3 IGN against the spec's W + 3 L common and W + 4 L worst (run 2); a compromised recovery authority moved nothing before R and was cancelled (run 4, take W); a compromised cancellation key moved nothing (run 5, the vault's loss zero). Every refusal the spec names for the everyday key was refused: by the wallet before proving where the wallet holds the rule, by the node where the rule is the node's, and in two rows by the chain after a real proof (the over-declared completion, the completion of a cancelled note). The takes to the wei are in section 6; the reviewer's four questions in section 7; the three further findings for the review (the revocation that never binds under the floors, the cancellation key that can cancel its own rotation, the head race) in section 8.

## 2. What this cut is

The vault of docs/money/VAULT-SPEC-2026-10-18.md built on the stage-one machinery (docs/money/STAGE1-JOIN-2026-10-18.md: Poseidon2 over Goldilocks as H, 128-bit values as four 32-bit limbs, the pool's tree of depth 32, the pinned Miden VM 0.36.1 prover and the cgo-linked verifier), as seven statements:

| tag | statement | what it does | who proves it |
|---|---|---|---|
| 3 | money-vault-create-v1 | two ordinary notes of the everyday key become a vault head (the policy hash, the balance, period 0, running total 0) and a change note | the everyday key |
| 4 | money-vault-withdraw-v1 | the head is consumed and re-emitted with the running total carried; a pending note of w to pk_dest is born with its maturity in blocks and in seconds | the everyday key |
| 5 | money-vault-cancel-v1 | the cancellation key consumes a pending note into an inbound note of the same vault; no destination, no amount choice | the cancellation key |
| 6 | money-vault-complete-v1 | the destination consumes a matured pending note into two ordinary notes, proving the note's policy is not in the revoked set at the parent block | the destination (pk_dest) |
| 7 | money-vault-announce-v1 | the recovery authority writes a new policy's hash onto the head with the declared height and time; it applies after R unless cancelled | the recovery authority |
| 8 | money-vault-policy-complete-v1 | after R the announced policy replaces the old one; the old policy hash is revealed into the revoked set; the period and the running total reset | anyone holding the viewing key and the new policy's fields |
| 9 | money-vault-rec-cancel-v1 | the cancellation key clears a pending recovery | the cancellation key |

The spec's ten statements against this cut's seven: S_deposit and S_merge are not built (a cancelled amount returns to the vault as an inbound note and waits for the merge statement; the spec's section 3.4 holds for it: it can go nowhere but the head); S_reclaim is not built (section 8, finding 1 says why it would never fire under the floors); the closure form of S_withdraw is not built; S_rec_announce's t-of-n WOTS+ authority is one hash-preimage authority in this cut (pk_a = H(sk_a, "recover")), the same form as the cancellation key, the threshold form the next cut's. The spec's "vault-policy-change" is the pair announce and policy-complete, with the delay between them.

The pieces, each its own file:

- tools/money/stage1-prover/prover/masm/vault-prelude.masm and vault-{create,withdraw,cancel,complete,announce,policy-complete,rec-cancel}.masm: the programs; each program is the prelude and its body, joined with one newline by the prover (prover/src/vault.rs) and by the verifier library (verifier-ffi/src/lib.rs) alike, so the hashes agree. prover/src/vault.rs: the object hashes, the witness form, the host-side check (the same expressions the circuits check) and the advice layout; prover/tests/vault.rs: the seven proven and verified on one scenario; prover/src/bin/money-vault-vectors.rs writes tools/money/vectors/money-vault-vectors.json, read by the node's and the wallet's tests.
- node/consensus/igneum/pool/vault.go: the record form (one layout for the seven), the statement words, the admissions, the revoked set; pool/miden/pinned.go: the seven program hashes; pool_settle.go: the vault branch of Finalize; api_pool.go: igneum_poolBlock extended (the statements named; the position tag among the nullifiers) and igneum_poolRevokedPath; params/config_igneum.go: IgneumChainPoolFrom[7404] = 1, the lab's entry under the SAME gate (no new gate; every chain of record at zero).
- wallet/money/src/vault.rs: the objects, the vault's keys, the vault notes' sealing, the scan, the seven preparations; wallet/money/src/main.rs: the ten commands vault-keygen, vault-create, vault-scan, vault-request, vault-cancel, vault-complete, vault-new-policy, vault-announce, vault-policy-complete, vault-rec-cancel.
- node/cmd/money-vault-lab/main.go and tools/money/lab/vault-stage2.sh: the experiment.

## 3. The objects and the keys, as built

Every hash is a fold of H: fold(TAG; w0, ..., wn) = H(...H(H(TAG, w0), w1)..., wn), TAG the word [tag, 0, 0, 0]. The domain tags: policy 11, head 12, pending 13, inbound 14, ptag 15, cancel 16, recover 17, nk_field 18, nk_cancel 19.

```text
policy_hash = fold(11; pk_e, pk_c, pk_a, nk_field, [D_b, D_s, P, 0], value(L), [R_b, R_s, 0, 0], nonce)
cm_head     = fold(12; value(v), vid, policy_hash, [period_idx, rec_h, rec_t, 0], value(spent), rec_hash, r)
cm_pending  = fold(13; value(w), vid, policy_hash, pk_dest, [h_req, t_req, D_b, D_s], r)
cm_inbound  = fold(14; value(w), vid, policy_hash, r)
ptag        = fold(15; cm_pending, [position, 0, 0, 0], r)           (F-779: r from the ciphertext, so nobody else computes it)
pk_c = H(sk_c, [16,0,0,0])   pk_a = H(sk_a, [17,0,0,0])   nk_field = H(nk_v, [18,0,0,0])   nk_c = H(sk_c, [19,0,0,0])
nf = H(H(nk, cm), [position, 0, 0, 0]) as stage one; an ordinary note's cm = H(H(v, pk), r) as stage one
```

Against the spec's section 3: the head's own nk_field is dropped (the policy's suffices: every statement that consumes a head opens the policy, and the head's commitment binds the policy hash); X_b, X_s, A_root, t and n are absent (no closure, one authority). The policy floors D_b >= 300, D_s >= 3,600, P >= D_b, R_b >= 2 D_b, R_s >= 2 D_s are checked in the circuit at creation and at every announcement. The fee: a vault statement carries no in-pool fee and no ordinary fee inputs in this cut; its carrier transaction pays the chain's gas from a public account, so no vault statement can touch the vault's balance for a fee (the spec's rule 5 by construction; the signerless type of F-761 is where the fee question lives).

The vault's keys from the everyday seed s and the vault id (the spec's section 4.4 in the wallet's form): nk_v = B("Igneum Money vault nullifier", s || vid) reduced to a word; the vault's ML-KEM-768 pair from B_64("Igneum Money vault kem", s || vid). The cancellation key and the recovery key are their own 32-byte secrets (vault-keygen), never derived from s. The vault file is the viewing key and the state (vid, the policy's fields, nk_v, the KEM seed, the heads, the pending and inbound notes seen) and holds no spending secret: a copy given to the cancellation key's holder scans and cancels and cannot withdraw. A head, a pending note or an inbound note on the chain is one output of the one shape (1,325 bytes) sealed to the vault's KEM key with a plaintext version of its own (3, 4, 5): indistinguishable from an ordinary note by its length. The pending note's second encryption to pk_dest's key (the spec's section 4.4) is not built: the request writes the destination a receipt file instead.

The revoked set: an indexed Merkle tree of depth 16 over H, leaves [key, next_key, 0, 0] with key a revoked policy hash's first element (64 bits; a collision between two policies' first elements makes an innocent completion unprovable at about 2^-64 a pair and never admits a revoked one), leaf 0 the sentinel [0, 0, 0, 0], which the empty leaf already is, so the empty revoked set's root is the empty tree's and needs no genesis write. A completion proves k_low < k and (k_next = 0 or k < k_next) for one leaf under the root; the node keeps the leaves and rebuilds the root on each insertion (recoveries are rare: the lab's form), and serves the witness by igneum_poolRevokedPath.

## 4. The statements' public inputs

Each statement hash is fold over [DOMAIN = [chain_id, kind, 0, 0], the words below in order]; the program re-derives it from the advice and asserts it equals the one stack input, so a proof binds its record, its kind and its chain. HT = [height, time, 0, 0]. The panel's check that the everyday key cannot shorten D, raise L or change an authority is the third column: the policy hash is a public input of no everyday-key statement, and in each of them the circuit copies head_in's policy hash into head_out's commitment (the pending note's too) from the policy it opened, writing no policy field.

| statement | public words after the domain | what the circuit binds about the policy |
|---|---|---|
| create (3) | anchor, nf_1, nf_2, cm_head, cm_change | the policy opens to policy_hash with pk_e = H(sk, nk) of input 0, nk_field = H(nk_v), the floors; the head carries policy_hash, period 0, spent 0, no recovery; v_head = v_1 + v_2 - change |
| withdraw (4) | anchor, nf_head, cm_head_out, cm_pending, HT (h_decl, t_decl) | the policy opens to head_in's policy_hash; pk_e = H(sk_e, nk_e); rec_hash = 0; idx = h_decl / P; spent_new = spent + w if idx = period_idx else w; spent_new <= L; 0 < w <= v; head_out = (v - w, vid, the SAME policy_hash, idx, spent_new, 0, r'); pending = (w, vid, the SAME policy_hash, pk_dest, [h_decl, t_decl, D_b, D_s], r'') with D_b, D_s the policy's |
| cancel (5) | anchor, nf_pending (under nk_c), ptag, cm_inbound | the policy opens to the pending note's policy_hash; pk_c = H(sk_c, 16), not all zero; the inbound note = (w, vid, the SAME policy_hash, r): no destination, no amount |
| complete (6) | anchor, revoked_root, nf_pending (under nk_dest), ptag, cm_1, cm_2, HT (h_now, t_now) | pk_dest = H(sk_dest, nk_dest); h_now >= h_req + D_b AND t_now >= t_req + D_s; cm_1 + cm_2 open to w; the pending note's policy_hash is not under revoked_root (the low leaf) |
| announce (7) | anchor, nf_head, cm_head_out, HT (h_decl, t_decl) | pk_a = H(sk_a, 17), not all zero; rec_hash = 0; the new policy opens with the floors; head_out = head_in with rec_hash = new_policy_hash, rec_h = h_decl, rec_t = t_decl (the policy hash itself unchanged) |
| policy-complete (8) | anchor, nf_head, cm_head_out, old_policy_hash, HT (h_now, t_now) | the old policy opens to the revealed hash and is head_in's; rec_hash = the new policy's hash; h_now >= rec_h + R_b AND t_now >= rec_t + R_s; head_out = (v, vid, new_policy_hash, h_now / P_new, 0, 0, r') |
| rec-cancel (9) | anchor, nf_head, cm_head_out | pk_c = H(sk_c, 16), not all zero; rec_hash != 0; head_out = head_in with the recovery fields zero |

The node's rules beside the proof (pool/vault.go): the record carries no value; the counts of the statement; every word canonical; the nullifiers unseen and distinct; the position tag unseen (cancel and complete: one consumer per pending note, whichever lands first); the anchor among the last 64 recorded roots; withdraw and announce: h_incl <= h_decl <= h_incl + 64 and ts(h_incl) <= t_decl <= ts(h_incl) + 768; complete and policy-complete: h_now <= h_incl and t_now <= ts(h_incl); complete: revoked_root equals the revoked root as the parent block left it (read once when the pool is opened for the block, so a revocation in the same block does not move it); then the writes: the nullifiers and the tag into the one set, the outputs appended, and for a policy completion the revealed hash's key into the revoked set. The turnstile is untouched by every vault statement; value leaves the pool only through the stage-one spend.

## 5. The figures

The circuits (prover/tests/vault.rs on lab box A, the pinned release at the join's setting, 108 conjectured bits, BLAKE3 commitments; the verifier's figure from money-verify on the lab's own proofs):

| statement | proof bytes | prove, eight cores (taskset 0-7 or 8-15, RAYON_NUM_THREADS=8) | prove, one core | execute (the host's run of the program) | verify, one core, 101 runs median (the lab's proofs) |
|---|---|---|---|---|---|
| create | 76,140 | 37.2 ms | 162.8 ms | 0.50 ms | 0.938 ms |
| withdraw | 75,564 | 34.9 ms | 182.4 ms | 0.46 ms | 0.948 ms |
| cancel | 75,468 | 31.8 ms | 172.1 ms | 0.43 ms | 0.924 ms |
| complete | 74,860 | 33.9 ms | 166.5 ms | 0.45 ms | 1.315 ms |
| announce | 74,124 | 35.3 ms | 181.8 ms | 0.46 ms | 0.951 ms |
| policy-complete | 73,932 | 36.5 ms | 163.6 ms | 0.45 ms | 0.914 ms |
| rec-cancel | 74,604 | 34.0 ms | 165.9 ms | 0.44 ms | 0.918 ms |

The proof sizes vary by a few hundred bytes from run to run (the FRI layers' query positions); every one is between 72 KB and 77 KB, the stage-one spend's band (75,084 median), at 108 conjectured bits, the release's ceiling (the join's section 5). The seven programs are of one size class because each is dominated by the same work: the Merkle membership at depth 32 (32 merges), the policy's eight merges, the head's or the pending note's seven, the nullifier's two; the create statement, with two stage-one inputs, is the largest, and the recovery cancellation the smallest. Peak memory of the test's seven provings at eight threads: 78 MB resident (the test process: the seven programs assembled, proven and verified in turn; the join's one-proving figure was 20.7 MB).

The lab's figures (the proof sizes of the lab's own proofs, kept in the quarantined work directory; the prove time the test's eight-thread figure, since the driver's result file was not written; the node's cost as S pays it inside Finalize: the proof, the tree appends, the state writes, under the sealer's load, from S's log over the whole run):

| statement | proofs made | proof bytes median | prove, eight threads, median | the node's cost inside Finalize (S) |
|---|---|---|---|---|
| money-vault-create-v1 | 5 | 74,956 (min 73,708, max 75,596) | 37.2 ms (max 37.2) | 10 admitted on S; median 3.453 ms, max 3.807 ms |
| money-vault-withdraw-v1 | 9 | 75,180 (min 74,060, max 76,364) | 34.9 ms (max 34.9) | 19 admitted on S; median 2.051 ms, max 5.087 ms |
| money-vault-cancel-v1 | 3 | 74,732 (min 74,700, max 75,500) | 31.8 ms (max 31.8) | 6 admitted on S; median 2.982 ms, max 3.409 ms |
| money-vault-complete-v1 | 8 (six admitted, the over-declared one and the cancelled note's refused) | 74,924 (min 71,940, max 76,044) | 33.9 ms (max 33.9) | 12 admitted on S; median 3.311 ms, max 3.682 ms |
| money-vault-announce-v1 | 3 | 73,668 (min 73,644, max 75,980) | 35.3 ms (max 35.3) | 6 admitted on S; median 2.936 ms, max 3.305 ms |
| money-vault-policy-complete-v1 | 2 | 74,572 (min 74,476, max 74,572) | 36.5 ms (max 36.5) | 2 admitted on S; median 3.647 ms, max 3.647 ms |
| money-vault-rec-cancel-v1 | 1 | 75,404 (min 75,404, max 75,404) | 34.0 ms (max 34.0) | 2 admitted on S; median 3.142 ms, max 3.142 ms |
| money-spend-v1 | 5 | 75,180 (min 73,740, max 75,276) | 0.0 ms (max 0.0) | (stage one's statement; the join's figure) |
| money-shield-v1 | 15 | 56,068 (min 53,340, max 57,124) | 0.0 ms (max 0.0) | (stage one's statement; the join's figure) |

The record sizes: a vault record is 1 + 32 + 1 + 32 n + 96 + 16 + 1 + m (34 + 1,293) bytes plus the proof: a withdrawal (n = 1, m = 2) 2,833 bytes retained and about 78 KB with its proof; a creation 2,865 and about 78 KB; a cancellation, an announcement, a policy completion or a recovery cancellation (m = 1) 1,506 and about 76 KB; the proof is three quarters of each as in stage one, and the pruning rule's retained set is the same.

## 6. The experiment

The chain (tools/money/lab/vault-stage2.sh; lab box A, the lab directory): chain 7404 (the research range; 7403 is the pruned-sync lab's), the gate from block 1, node S sealing on one thread (the null algorithm, target 3 s), node T a second sealer whose sealing is stopped at start and started only for the reorganisation row, node R reading; the bond seeds from 0600 files; every node with the real verifier (geth.vault sha256 53454b6b32154c24..., the library libmoney_verifier.a sha256 ddcb51b758c2cc94..., rustc 1.99.0); no unsafe flag. The policy of every vault: D_b = 300 blocks, D_s = 3,600 s (the floors: at the lab's 3 s blocks the seconds bind, one hour), P = 300, L = 1 IGN, R_b = 600, R_s = 7,200 s (two hours). Each owner holds W = 2 IGN as two ordinary notes and a vault of V = 10 IGN; the attacker holds every owner's everyday wallet file and vault file (the viewing key is in the everyday wallet) and its own wallet for the loot; the owner's cancellation key and recovery key are separate files the attacker does not hold, except where a run says it does. The driver: node/cmd/money-vault-lab.

The attacker's common prelude in every run: W drained at once by an ordinary spend of the two notes to its own wallet (the vault's rules touch nothing of this: the spec's first row).

| run | the take, to the wei | the bound | | the vault at the end | requests / cancelled / completed |
|---|---|---|---|---|---|
| run1-cancel-within-D | 2000000000000000000 wei (2 IGN): W 2 + from the vault 0 | 2000000000000000000 wei (2 IGN): W (the cancellation key acts inside D; the vault loses nothing) | within | 9 IGN in the head, 1 IGN returned as inbound notes | 2 / 2 / 0 |
| run2-recovery-at-once | 3000000000000000000 wei (3 IGN): W 2 + from the vault 1 | 6000000000000000000 wei (6 IGN): W + 4 L at the worst alignment, W + 3 L common (the spec's section 9 with the defaults' shape); rule 6 makes W + 2 L the most the chain admits when the authority announces at once, and the margin did not reach the next boundary here | within | 9 IGN in the head, 0 IGN returned as inbound notes | 1 / 0 / 1 |
| run3-owner-silent | 6000000000000000001 wei (6.000000000000000001 IGN): W 2 + from the vault 4.000000000000000001 | 6000000000000000000 wei (6 IGN): W + L a period index touched: four indexes (0, 1, 2, 3) in three periods of silence; the extra wei is the period-index hole of section 8, finding 1 | EXCEEDED | 5.999999999999999999 IGN in the head, 0 IGN returned as inbound notes | 5 / 0 / 5 |
| run4-malicious-recovery | 2000000000000000000 wei (2 IGN): W 2 + from the vault 0 | 2000000000000000000 wei (2 IGN): W (the recovery authority alone moves nothing before R; the cancellation key cleared it) | within | 10 IGN in the head, 0 IGN returned as inbound notes | 0 / 1 / 0 |
| run5-cancel-griefing | 2000000000000000000 wei (2 IGN): W 2 + from the vault 0 | 2000000000000000000 wei (2 IGN): W: a compromised cancellation key moves nothing; it costs the owner the period's allowance until the policy rotates it (the W of the shared prelude; the vault's loss is zero) | within | 9 IGN in the head, 1 IGN returned as inbound notes | 1 / 1 / 0 |

**run1-cancel-within-D**

- 22:58:15Z vault created at block 16 (V = 10000000000000000000, L = 1000000000000000000 a period of 300 blocks, D = 300 blocks and 3600 seconds, R = 600 blocks and 7200 seconds; cancellation key true)
- 22:58:20Z W drained: 2000000000000000000 moved to the thief's wallet at block 22 (the thief holds 2000000000000000000, the owner 0)
- 22:58:26Z split 1 of 2: request of 500000000000000000 admitted at block 28 (declared 29, 1792364365); matures at block 329 and time 1792367965
- 22:58:27Z split 2 of 2: request of 500000000000000000 admitted at block 29 (declared 30, 1792364366); matures at block 330 and time 1792367966
- 22:58:30Z cancelled the pending 500000000000000000 at block 32 (inside D: it would have matured at block 329)
- 22:58:31Z cancelled the pending 500000000000000000 at block 33 (inside D: it would have matured at block 330)

**run2-recovery-at-once**

- 22:58:16Z vault created at block 17 (V = 10000000000000000000, L = 1000000000000000000 a period of 300 blocks, D = 300 blocks and 3600 seconds, R = 600 blocks and 7200 seconds; cancellation key false)
- 22:58:21Z W drained: 2000000000000000000 moved to the thief's wallet at block 23 (the thief holds 2000000000000000000, the owner 0)
- 22:58:33Z L in the current period: request of 1000000000000000000 admitted at block 34 (declared 35, 1792364371); matures at block 335 and time 1792367971
- 22:58:33Z the next period boundary 300 is 265 blocks away: the margin (64) does not reach it; the thief holds one period's allowance before the announcement
- 22:58:34Z the recovery authority announced a new policy at block 36; it completes at block 637 and time 1792371573
- 23:59:44Z the thief completed 1000000000000000000 at block 1338, time 1792367978 (matured at block 335 and time 1792367971; the seconds bound decided)
- 00:59:57Z the recovery completed at block 2534: the old policy hash d996c0e707137147 revoked (the revoked set now 1), the head under the new policy

**run3-owner-silent**

- 22:58:17Z vault created at block 19 (V = 10000000000000000000, L = 1000000000000000000 a period of 300 blocks, D = 300 blocks and 3600 seconds, R = 600 blocks and 7200 seconds; cancellation key true)
- 22:58:23Z W drained: 2000000000000000000 moved to the thief's wallet at block 24 (the thief holds 2000000000000000000, the owner 0)
- 22:58:40Z L in period 0 of the silent run: request of 1000000000000000000 admitted at block 42 (declared 43, 1792364379); matures at block 343 and time 1792367979
- 23:04:13Z L into period 1 through the margin: request of 1000000000000000000 admitted at block 241 (declared 300, 1792364710); matures at block 600 and time 1792368310
- 23:19:16Z L into period 2 through the margin: request of 1000000000000000000 admitted at block 541 (declared 600, 1792365609); matures at block 900 and time 1792369209
- 23:34:01Z L into period 3 through the margin: request of 1000000000000000000 admitted at block 842 (declared 900, 1792366495); matures at block 1200 and time 1792370095
- 23:34:02Z 1 wei during the partition: request of 1 admitted at block 844 (declared 844, 1792366497); matures at block 1144 and time 1792370097
- 23:59:47Z the thief completed 1000000000000000000 at block 1340, time 1792367981 (matured at block 343 and time 1792367979; the seconds bound decided)
- 00:05:29Z the thief completed 1000000000000000000 at block 1446, time 1792368322 (matured at block 600 and time 1792368310; the seconds bound decided)
- 00:20:18Z the thief completed 1000000000000000000 at block 1748, time 1792369209 (matured at block 900 and time 1792369209; the seconds bound decided)
- 00:35:08Z the thief completed 1000000000000000000 at block 2044, time 1792370102 (matured at block 1200 and time 1792370095; the seconds bound decided)
- 00:35:11Z the thief completed 1 at block 2046, time 1792370107 (matured at block 1144 and time 1792370097; the seconds bound decided)

**run4-malicious-recovery**

- 22:58:18Z vault created at block 20 (V = 10000000000000000000, L = 1000000000000000000 a period of 300 blocks, D = 300 blocks and 3600 seconds, R = 600 blocks and 7200 seconds; cancellation key true)
- 22:58:24Z W drained: 2000000000000000000 moved to the thief's wallet at block 26 (the thief holds 2000000000000000000, the owner 0)
- 22:58:35Z the compromised recovery authority announced a policy to the attacker's keys (limit V, its own everyday key) at block 37; it would complete at block 638
- 22:58:36Z the owner's cancellation key cleared the malicious recovery at block 38 (inside R); the vault is untouched

**run5-cancel-griefing**

- 22:58:19Z vault created at block 21 (V = 10000000000000000000, L = 1000000000000000000 a period of 300 blocks, D = 300 blocks and 3600 seconds, R = 600 blocks and 7200 seconds; cancellation key true)
- 22:58:25Z W drained: 2000000000000000000 moved to the thief's wallet at block 27 (the thief holds 2000000000000000000, the owner 0)
- 22:58:37Z the owner requested L to itself at block 39
- 22:58:38Z the compromised cancellation key cancelled the owner's withdrawal at block 40: L returned to the vault as an inbound note; the period's allowance is spent
- 22:58:39Z the recovery authority announced a policy with a new cancellation key and a new everyday key at block 41; it completes at block 642 and time 1792371578
- 22:58:39Z LIMIT: the compromised cancellation key can also cancel the recovery that would rotate it (the spec's S_rec_cancel admits the policy's cancellation key); the wallet prepared it and the lab did not send it; the escape then needs the recovery authority to out-race the griefer, or a rule that a recovery rotating pk_c is cancellable by the recovery authority alone
- 01:01:4xZ (by hand after the driver stopped at the recovery completion: its vault file had forgotten the pending policy after the dry-run cancellation, the wallet fault cured in this cut) the recovery completed at block 2584: the old policy hash d1faae92f297e4cf revoked (the revoked set now 2), the head under the new policy at position 73, period 8, running total 0

The reorganisation during the pending windows (run 3's four pending notes, requested at blocks 42, 241, 541 and 842 and maturing from 343 to 1,200; the 1-wei request of the driver's partition row at 844, maturing at 1,144): the driver's own partition row leaked (its admin_removePeer calls left R's static entry for T in place, so T's blocks reached S through R and S only dropped its own sealed candidates: "Sealed block dropped: its parent is not the head"), so the reorganisation of record was forced by hand at 23:43 UTC during the same windows, from the box: T cut off from S and R with the enode strings as each side lists them (T at zero peers, S and R at one), T's sealing started on two threads at S's head 1,031, T eight blocks ahead by 23:44:06 (S 1,039, T 1,045), the peers restored, and both S and R logged `Chain reorg detected number=1029 drop=10 add=11`: ten blocks of S's branch (1,030 to 1,039, their hashes recorded before and after) replaced by eleven of T's, a reorganisation of depth 10, inside the fork choice's twelve-block bound. No pool record sat in the replaced blocks (the driver was waiting), so no request restarted; the five pending notes stood at their positions with their maturities unchanged, and the completions after the reorganisation (section 6's runs) proved against the canonical anchor as always. The rule that a request reorganised out restarts with a later maturity was not exercised by the lab (the window carried no record) and stands by the node's rule h_incl <= h_decl (a re-inclusion past the declared height is refused and re-declared later).

The refusals, each the spec's row (the wallet's refusal is the wallet's rule and the circuit's is the same expression; the node's is the node's):

- (run1-cancel-within-D) a policy with D_s 3,599 s: the wallet refuses before proving (igneum-money: the delay is below the floor (300 blocks, 3600 seconds)); the circuit's floor is the same expression
- (run1-cancel-within-D) a third request of 1 wei in the same period: the wallet refuses (igneum-money: the period's requests would be 1000000000000000001, past the limit 1000000000000000000); the circuit's running total is the same expression
- (run1-cancel-within-D) a policy change to the thief's keys (limit raised to V, no authorities) under the everyday key: the wallet refuses (igneum-money: this key is not the policy's recovery authority)
- (run1-cancel-within-D) a forged announcement record (a proof of 75,000 arbitrary bytes) sent to the chain: refused by the verifier at block 30, the pool unchanged
- (run1-cancel-within-D) a completion before maturity at block 30: the wallet refuses (igneum-money: not matured: matures at block 329 and time 1792367965); the circuit's maturity check is the same expression
- (run1-cancel-within-D) a completion proven at the maturity height 329 and time 1792367965 while the chain is at 30: refused by the node at block 31 (the proven height and time are past the including block)
- (run1-cancel-within-D) two requests of L against one head for the next period: the wallet refuses both while the boundary (300) is more than 64 blocks ahead of 31 (the declared height must be within the margin)
- (run1-cancel-within-D) a cancellation names no destination and no amount choice: the record's one output is the inbound note under the vault's policy (the statement's public inputs)
- (run1-cancel-within-D) the thief's completion of the cancelled note after maturity, proven and sent: refused by the chain at block 2047 (the position tag is in the set)
- (run2-recovery-at-once) a request while a recovery is pending: the wallet refuses (igneum-money: a recovery is pending: no withdrawal until it completes or is cancelled); the circuit requires the head's rec_hash to be zero
- (run2-recovery-at-once) the thief's request after the recovery completed: refused (igneum-money: this wallet's key is not the policy's everyday key): the policy's everyday key is the new one
- (run4-malicious-recovery) the attacker's request under its announced policy before R: the wallet refuses (igneum-money: this wallet's key is not the policy's everyday key); the head is still under the old policy
- (run4-malicious-recovery) the attacker's completion of its recovery before R: the wallet refuses (igneum-money: the recovery delay has not passed: completes at block 638 and time 1792371574); the circuit requires h_now >= rec_h + R_b and t_now >= rec_t + R_s
- (run4-malicious-recovery) the owner's own request while the malicious recovery is pending: refused (igneum-money: a recovery is pending: no withdrawal until it completes or is cancelled): a compromised authority can freeze withdrawals for up to R
- (run5-cancel-griefing) the owner's second request in the period after the griefing cancellation: refused (igneum-money: the period's requests would be 1000000000000000001, past the limit 1000000000000000000): the cancelled request still counts in the running total (rule 8)
- (run5-cancel-griefing) the thief's request under the old everyday key after the recovery completed: refused (igneum-money: this wallet's key is not the policy's everyday key)

The chain at the end: the verifier miden-vm-3601-blake3-min108bits; the turnstile 60000000000000000000 wei equal to the pool address's balance 60000000000000000000 (reconciled: no vault statement moved value out of the pool); the revoked set 2 hashes; the second node R agrees with S on the root, the count and the revoked root at the last block 2585: True; the driver's blocks 15 to 2585, 123 minutes of wall time.

## 7. The reviewer's four questions

**(1) When does protection end?** The delay buys time to notice; it does not cap the loss by itself. With no owner action the attacker holding the everyday key takes W at once and then at most L for every period index it can declare into: after N full periods of silence the take is W + (N + 1) L, and one L more when the 64-block declaration margin reaches the next boundary at the end, never more than one index at a time once the index can only advance (section 8, finding 1: the spec's step 3 as written let it go back, and the alternation inside the margin window would then drain V in 64 blocks; a second request against one head is refused as a duplicate nullifier in either case). Measured: run 3, the owner silent across three periods (45 minutes at the lab's block time), the thief requested L in period 0 and L into each of the next three through the margin, four indexes, and completed all four after maturity, plus the one wei that went back into period 2 (section 1): take 6000000000000000001 wei (6.000000000000000001 IGN) = W + 4 L + 1 wei. Nothing in this policy caps the total: the loss grows by L a period until the owner acts or the vault is empty (V / L periods, ten here). The policy can cap it only by L = 0 (nothing withdraws; the closure form, not built, would be the only exit), or by D and P long enough that the owner notices; a lifetime ceiling per policy (a field the circuit would check as the running total is checked, not reset by the period) is the next cut's one-field addition, and the wallet should say the rule in these words at creation.

**(2) Can recovery be the theft route?** A recovery can move the vault to new keys, so a compromised recovery authority is a path, bounded by two things: it does nothing before R (measured: run 4, the attacker holding the recovery key announced a policy to its own keys with the limit raised to V; its request under that policy and its completion before R were refused by the wallet, the one by the head still being under the old policy, the other by h_now < rec_h + R_b; the chain carries the same expressions in the circuit), and the owner's cancellation key cancels it at any time before R (measured: run 4, the recovery cleared at block 38, the vault untouched, take W). What else can challenge it: nothing in this cut; the recovery authority of the spec is t of n guardians and the one key here is the lab's form. What a compromised authority can do before R expires: announce, which freezes every withdrawal (rule 6: no request while a recovery is pending; measured: the owner's own request refused during the malicious recovery), so a malicious authority can grief the owner for up to R until the cancellation lands, and can repeat after each cancellation (each costs it one announce; each costs the owner one cancellation). A compromised cancellation key: it cancels every withdrawal the owner requests, each back into the vault as an inbound note (measured: run 5, the owner's L cancelled at block 40; no money moved; the period's allowance consumed, the owner's second request in the period refused), and the owner escapes by the recovery authority announcing a policy with a new cancellation key, which rotates it after R (measured: run 5, the policy with a new pk_c completed at block 2584; the old cancellation key is not the new policy's). THE LIMIT FOUND (finding 2 of section 8): the compromised cancellation key can also cancel that very recovery (the spec's S_rec_cancel admits the policy's cancellation key with no question of what the new policy changes), so the escape holds only if the recovery authority's announcement is not cancelled before R; a griefer with the cancellation key and the attention to cancel every announcement blocks its own rotation. The lab prepared that cancellation (the wallet accepted it as the policy's key) and did not send it. The cure is the panel's: a recovery that only rotates pk_c cancellable by the recovery authority alone, or a second signature of the everyday key on the announcement.

**(3) How private are vault operations?** The record's tag byte is public: a watcher tells a vault statement from an ordinary spend and counts each kind per block (the lab's igneum_poolBlock names them). It cannot link the operations of one vault to each other from the ledger: every head is a fresh commitment under fresh randomness, the head's nullifier is under nk_v and the pending note's two nullifiers under nk_c and nk_dest, the position tag carries r (F-779), the revealed old policy hash of a policy completion appeared in no earlier record, and no amount, key or vault id is a public input. What the kinds and the timing reveal: on a chain with one withdrawal and one cancellation in neighbouring blocks (the lab's run 1: the request at block 28, the cancellation at 32) the pair is visible as a pair, as the spec's section 10 says; on a busy chain the count per kind per block is the one signal. So the statements are distinguishable on the ledger: a limit. The unification named: one statement type with hidden policy flags, that is one program whose witness selects the branch (an ordinary transfer, a withdrawal, a cancellation, a completion, a recovery step) and whose record has one shape, so the ledger shows one kind; its cost is one proof of the largest branch's size for every transfer (the complete statement's rows plus the transfer's), ESTIMATE under 1.5x the stage-one spend proof, which the next cut measures.

**(4) Disclosure.** Wherever this page or the spec says disclosure, the words are: the owner chooses what the protocol exposes; a recipient can always disclose what they received, and anyone given a viewing credential can retain or share it. For the vault that reads: the cancellation key's holder and the recovery authority hold the vault's viewing key (the spec's section 4.4) and can keep or share it; the destination of a withdrawal holds the pending note's receipt and can disclose it; the owner's choice is only which receipt to produce.

## 8. Findings for the review (the spec's, found by the cut)

1. **F-798. The period index can go back (the one-wei hole; cured in this branch).** The spec's S_withdraw step 3 compares the declared index with the head's by equality only. The lab's thief, having declared into period 3 through the margin (block 842, h_decl 900), then declared at the current height 844 (period 2): idx 2 differs from the head's 3, so the running total restarted at the new amount and the request was admitted. Repeated inside the 64-block window the two declarations alternate and each opens L again: the real bound of the spec as written is the whole balance inside one margin window. The cure, in the circuit (`dup.0 mem_load.152 u32gte assert` after the division), the host check and the wallet: idx >= period_idx. The withdraw program's hash changes with it: the branch pins 0x366b94f954e3623a... (pinned.go, the vectors; the library libmoney_verifier.a sha256 7fb595ca820971e9...), the lab chain verified 0xa311dd7aba17c5d1... (the library ddcb51b758c2cc94...); the chain run of this page carries the hole. The prover's test proves the cured withdraw (75,148 bytes) and refuses a request declared back into period 2 against a head at period 3.
2. **F-799 (the spec's). The revocation never catches a pending note under the floors.** The spec's rule 7 ("a completed recovery revokes every pending note of the old policy; the bound falls to W when R <= D") cannot fire: rule 6 forbids a request after the announcement, so every pending note's maturity h_req + D_b is before h_announce + D_b, and the revocation binds at h_announce + R_b >= h_announce + 2 D_b. The revoked set, S_reclaim and the "W + 3 L common / W + 4 L worst" formula therefore describe a case the floors exclude; the chain's bound when the authority announces at once is W + 2 L (this period's L and the next's through the margin), measured W + 1 L in run 2 where the margin did not reach the boundary. The revoked set stays correct and tested (the node's and the circuit's tests) and would matter if the floors changed or if the announce revoked at once. The panel's choice: revoke at the announcement (the announce needs the recovery secret; revoking the thief's pending notes at once costs the owner nothing it cannot reclaim) and drop the R-vs-D clause.
3. **F-800 (the spec's). The cancellation key can cancel its own rotation** (section 7, question 2).
4. **F-801 (the spec's). The head can be raced.** Every head-consuming statement names the head's nullifier, so the owner's announcement proven against head_k is refused if the thief's request against head_k lands first, and the thief can replace the head every block with a request of one wei while its period allowance lasts; the owner's announcement then needs to land in the same block ahead of the thief's request, a miner-level race. Not run in the lab (the driver is one hand). A cure inside the node: Finalize applies a block's announce and cancel records before its withdraw and complete records (a kind-ordered pass, the appends still in that order), which igneum_poolBlock would have to mirror; or a minimum request as a policy field.

## 9. Caveats

- The circuits' refusals are exercised through the prover's host check in the tests (the same expressions: the limit, the maturity, the floors, the recovery pending, the authority's key, the revoked non-membership) and on the chain through the wallet's refusals plus the node's own rules (the over-declared completion, the forged proof, the duplicate head nullifier, the consumed position tag); a witness that passes the host check and fails the circuit was not constructed, so the circuit's refusal of a bad witness is inferred from the two being one expression, not measured.
- The second encryption of the pending note to the destination's key is not built (the receipt file stands in); MaxCiphertext (F-774) is the same question.
- The vault's KEM key is fixed at creation: after a recovery the thief, holding the old everyday seed, keeps the viewing key (the spec's 4.4 rotates it with the seed; the wallet's convention, not the circuit's).
- One recovery authority, a hash preimage, not the spec's t of n WOTS+ (ESTIMATE 2^20 rows for that statement; this cut's announce is the smallest of the seven).
- The timestamps are the lab chain's own (the node's drift rule); the seconds floor bound every maturity here because the lab's blocks are four times faster than the design's.
- No deposit, no merge, no reclaim, no closure; the inbound notes of the cancellations sit unmerged.
- The dev chain's two sealers raced small reorganisations until T's sealing was stopped outside the reorganisation row; one creation in a discarded lab start was refused as "no recorded root" and re-proven, the rule 8.1 path, and the driver re-proves on that refusal.
- The lab binary was built from the tree before gofmt's last pass over the Go files (formatting only) and before the period-index cure; the cure's withdraw program is not the one the lab chain verified.
- The driver stopped at run 5's recovery completion: its vault file had forgotten the pending policy because the wallet's rec-cancel preparation (run 5's dry-run LIMIT check, never sent) cleared it on preparation; the wallet now keeps it (wallet/money/src/vault.rs). Run 5 was finished by hand from the box with the same wallet, prover and a hand sender (cmd/money-vault-send), and the driver's result JSON was assembled from its log (the takes, the events, the refusals; every figure above is the log's or the chain's), not written by the driver.
- The driver's own reorganisation row leaked (section 6); the reorganisation of record was forced by hand.
- Two lab starts before the one of record were discarded (a non-archive node pruning the states the stream reads; run 3's period loop; the two-sealer race) and quarantined with it.

## 10. The exact next step

the review's read of this page and of the four findings; the period-index cure re-run on a lab chain (one hour: a single alternation row suffices); then, by the review's choice on finding 2, the revocation's timing re-cut in the spec; the merge statement (so a cancelled amount returns into the head); the lifetime ceiling field; the unification's measurement (question 3); the t-of-n authority. The live chains' rules stay byte-identical: the gate at zero everywhere but 7402 and 7404, confirmed from the code paths (nothing of pool/vault.go runs where poolOn is false) and by the suites (section 11).

## 11. The suites

On lab box A from the branch's tree (the clone the lab directory):

- the pool: `go test ./consensus/igneum/pool/...` ok, and with the real verifier linked, `go test -tags money_miden ./consensus/igneum/pool/...` ok (the miden package's New checks the nine pinned program hashes against the library);
- the chain's smoke: `go test -count=1 ./consensus/igneum/` ok (36.9 s); the full suite is the gate's;
- the wallet: `cargo test --release -p igneum-wallet-money` ok (the lib's 18 including the two vault tests, the CLI and vector tests; the vault vectors test reads tools/money/vectors/money-vault-vectors.json);
- the prover: `cargo test --release -p money-stage1-prover` ok (the stage-one vectors, the vault's seven proven and verified, prover/tests/vault.rs);
- `go vet` clean on the touched packages and the driver; the Go files gofmt'd.
