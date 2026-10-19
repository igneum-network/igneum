# Money stage two, cut one: the vault's re-run (the cured withdraw at 128 bits)

Date: 19 October 2026, 02:4x to 05:0x UK. Branch: lead/money-vault-rerun, from main b87a323de (the night's landing 43a25f253 and the vault's reading page) with lead/money-vault afa794d95 merged. Lab chain 7410 on lab box A (three nodes, archive mode, the real verifier at the 128-bit setting; the form of chain 7404's recipe tools/money/lab/vault-stage2.sh as tools/money/lab/vault-rerun.sh). The first run's page: docs/money/STAGE2-CUT1-VAULT-2026-10-18.md (FAIL by one wei: the period-index hole F-798, cured there and not re-run). the review's reading: docs/money/PANEL-READING-3-2026-10-19.md section 5 (PASS WITH RE-CUTS, held off the landing until this re-run).

## 1. PASS

Every take is within its bound to the wei, and the alternation the hole allowed is refused three times:

| run | the take, to the wei | the bound | | the vault at the end | requests / cancelled / completed |
|---|---|---|---|---|---|
| run1-cancel-within-D | 2000000000000000000 wei (2 IGN): W 2 + from the vault 0 | 2000000000000000000 wei (2 IGN): W (the cancellation key acts inside D; the vault loses nothing) | within | 8.999999999999999999 IGN in the head, 1.000000000000000001 IGN returned as inbound notes (the two cancelled halves and the partition row's 1 wei, cancelled after the rejoin) | 3 / 3 / 0 |
| run2-recovery-at-once | 3000000000000000000 wei (3 IGN): W 2 + from the vault 1 | 6000000000000000000 wei (6 IGN): W + 4 L at the worst alignment, W + 3 L common; rule 6 makes W + 2 L the most the chain admits when the authority announces at once | within | 9 IGN in the head, 0 returned | 1 / 0 / 1 |
| run3-owner-silent | 6000000000000000000 wei (6 IGN): W 2 + from the vault 4 | 6000000000000000000 wei (6 IGN): W + L a period index touched: four indexes (0, 1, 2, 3) in three periods of silence | within (the first run took 6 IGN + 1 wei) | 6 IGN in the head, 0 returned | 4 / 0 / 4; the alternation refused 3 of 3 |
| run4-malicious-recovery | 2000000000000000000 wei (2 IGN): W 2 + from the vault 0 | 2000000000000000000 wei (2 IGN): W | within | 10 IGN in the head, 0 returned | 0 / 1 / 0 |
| run5-cancel-griefing | 2000000000000000000 wei (2 IGN): W 2 + from the vault 0 | 2000000000000000000 wei (2 IGN): W | within | 9 IGN in the head, 1 IGN returned | 1 / 1 / 0 |

The chain at the end (block 2,516; the driver's blocks 11 to 2,516, 7,312 s of wall time, 01:49:46Z to 03:51:38Z): the turnstile 60000000000000000000 wei equal to the pool address's balance (reconciled: no vault statement moved value out of the pool); the revoked set 2 hashes (ffe5db52c885cd83 at block 2,513, b22bd89f5ee94a29 at 2,516); the reading node R agrees with S on the root, the count (73) and the revoked root at the last block; the driver's own verdict allPass true. Nodes S, T and R down by their recorded pids (1272751, 1272800, 1272852) at 03:52:5xZ; the lab directory with its seeds, wallets, keys, witnesses and proofs moved to the lab directory (0700), nothing read or printed.

## 2. What was re-run, and on what

The cured withdraw (bbc068268 on lead/money-vault: `idx >= period_idx` asserted in the circuit, checked by the host and by the wallet; the withdraw program re-pinned 0x366b94f954e3623a...) had been cut after the first lab and never run on a chain. This re-run puts the whole cut on the project's tree as it stands after the night's landing: the pool at 128 conjectured bits (the cubic challenge field, 38 FRI queries), the pruned sync's retained set, the shielded coinbase. The merge (61274202b) carried four textual conflicts and one real join: the vault's seven statements enter the retained set (pool/retained.go SplitProof and Outputs, pool/extract.go: a vault record is admitted when its first nullifier is in the set at the block and not at the parent and its first output is the leaf at the next position; the proof is the tail, pruned like a spend's; a cancellation's or completion's position tag is served beside the nullifiers), igneum_poolBlock names the admitted statements from the retained set, and the vault's outputs are appended with their ciphertexts (the retained-set check hashes each; the first cut appended the commitment alone, which the project's tree no longer admits). The settle chain names its four record kinds. The wallet's five vault test blocks gained the coinbase cut's mints field (d48b00766: the test target had failed on lab box B while the release build passed).

The driver (79b1efc2b) gained the alternation rows: in run 3, after each request declared ahead through the 64-block margin, one of 1 wei declared back at the current height. Under the hole this is the drain (a fresh allowance each time the index goes back); under the cure the wallet refuses it before proving, with the words of the circuit's assertion. The partition row's in-flight request moved from run 3 to run 1 (run 3's head, its index moved ahead, refuses a request at the current height under the cure; run 1's period allowance is fresh at the partition's height and no recovery is pending), and the owner's cancellation key clears it after the rejoin, so run 1's take stays W.

The binaries of record: geth.vault sha256 1208c789c5121338 (this branch at 79b1efc2b built with `-tags money_miden`), the verifier library libmoney_verifier.a f61656805434cc06 (the five patches of the 128-bit cut: miden-field f3655966010a362c, miden-crypto 6ad4fb6fee076907, miden-air 8c8496d3b45dbd34, miden-prover 183411494c9c1e23, miden-verifier 27b0bf1c15ef8145; rustc 1.99.0; the same library bytes on lab box B), money-prove b58ffee9906a036c, igneum-money e25b30c186dae533. The node's verifier name is still `miden-vm-3601-blake3-min108bits`: the name is pool.MinSecurityBits's label, the one line the 128-bit page left for the next node cut; the library behind it is the cubic-field one by its hash, and every proof below was made and verified at 128 conjectured bits (section 4).

## 3. The pins at 128 bits

The seven program hashes (pool/miden/pinned.go, tools/money/vectors/money-vault-vectors.json) are the Miden assembler's MAST roots and do not depend on the proof's field or query count: the 128-bit patches touch the prover and the verifier, not the assembler. Re-pinned under the 128-bit build: `go test -tags money_miden ./consensus/igneum/pool/...` passes on lab box A and lab box B (the miden package's New checks the nine pinned hashes against the linked library at start), and the lab's nodes admitted every statement under them. The hashes:

| statement | pinned hash (first word) |
|---|---|
| money-vault-create-v1 | 0x33f55dfa8c2da823 |
| money-vault-withdraw-v1 | 0x366b94f954e3623a (the cure; 7404 verified 0xa311dd7aba17c5d1 without it) |
| money-vault-cancel-v1 | 0x785f979fe4bc3d7b |
| money-vault-complete-v1 | 0xb895507916e6196d |
| money-vault-announce-v1 | 0xa751b68883700110 |
| money-vault-policy-complete-v1 | 0x6f18b069347b7ca6 |
| money-vault-rec-cancel-v1 | 0x98478223f2d737e2 |

## 4. The figures at 128 bits

The lab's own proofs (the driver's prover at RAYON_NUM_THREADS=8 on lab box A's EPYC 9454P, every proof it made; the median is the upper middle of an even count, as the first page's) and the node's cost as S paid it inside Finalize (the proof, the tree appends, the state writes, under the sealer's load, from S's log over the whole run):

| statement | proofs made | proof bytes median (min, max) | prove, eight threads, median (min, max) | the node's cost inside Finalize (S) |
|---|---|---|---|---|
| money-vault-create-v1 | 5 | 99,404 (98,476, 101,068) | 47.4 ms (42.7, 52.6) | 11 admitted; median 4.088 ms, max 6.007 ms |
| money-vault-withdraw-v1 | 9 | 99,756 (98,988, 101,036) | 52.8 ms (45.1, 65.3) | 18 admitted; median 2.455 ms, max 4.662 ms |
| money-vault-cancel-v1 | 4 | 99,692 (98,156, 99,788) | 47.5 ms (43.1, 52.5) | 8 admitted; median 3.606 ms, max 3.990 ms |
| money-vault-complete-v1 | 7 (six admitted; the cancelled note's refused) | 98,924 (98,028, 100,364) | 52.2 ms (50.8, 55.4) | 10 admitted; median 4.055 ms, max 4.635 ms |
| money-vault-announce-v1 | 3 | 99,052 (96,100, 100,268) | 49.3 ms (42.6, 49.5) | 7 admitted; median 3.510 ms, max 3.989 ms |
| money-vault-policy-complete-v1 | 2 | 99,692 (98,572, 99,692) | 47.8 ms (45.2, 47.8) | 4 admitted; median 1.908 ms, max 2.469 ms |
| money-vault-rec-cancel-v1 | 1 | 100,140 | 49.1 ms | 2 admitted; median 4.162 ms |
| money-spend-v1 (stage one) | 5 | 98,828 (98,540, 100,268) | 48.8 ms (43.2, 57.2) | |
| money-shield-v1 (stage one) | 15 | 75,076 (73,212, 76,676) | 13.0 ms (11.3, 18.1) | |

The counts admitted on S exceed the proofs made because S's log counts each statement at the block's sealing and at its import (two lines a statement on the sealing node). At 108 bits the seven were 72 to 77 KB and 32 to 37 ms on eight cores (the first page); at 128 bits they are 96 to 101 KB and 43 to 65 ms, the stage-one spend's band at this setting (98,540 to 100,268 here; the 128-bit page's 99,980). Against the ADR's threshold 1 (140,000 bytes a proof at 128 bits): every vault proof passes, the largest 101,068. The prover test's figures for reference (prover/tests/vault.rs on lab box A, the process's default thread count, one proving each): create 99,852 B in 76.7 ms, withdraw 98,956 / 62.7, cancel 97,740 / 57.6, complete 99,340 / 53.6, announce 101,036 / 54.6, rec-cancel 100,684 / 51.8, policy-complete 98,892 / 49.7; execute 0.43 to 0.51 ms; each reported at 128 conjectured bits.

The record sizes are the first page's (the proof three quarters of each, now about 100 KB of about 103 KB a withdrawal).

## 5. The experiment, as it ran

The chain: 7410 (the research range), the gate from block 1 (params.IgneumChainPoolFrom[7410]), S sealing on one thread (the null algorithm, target 3 s), T a second sealer stopped at start, R reading, the bond seeds from 0600 files, every node with the real verifier, no unsafe flag. The policy of every vault: D_b = 300 blocks, D_s = 3,600 s, P = 300, L = 1 IGN, R_b = 600, R_s = 7,200 s; each owner W = 2 IGN as two notes and a vault of V = 10 IGN; the attacker holds every owner's everyday wallet and vault file. The common prelude in every run: W drained at once by an ordinary spend (blocks 22 to 27).

**run3-owner-silent** (the re-run's reason)

- 01:50:22Z L in period 0: request of 1 IGN admitted at block 44 (declared 45); matures at block 345 and time 1792378281
- 01:55:42Z L into period 1 through the margin: admitted at block 241 (declared 300); matures at 600 and 1792378598
- 01:55:42Z THE ALTERNATION back into period 0: 1 wei declared at the current height 243 after the head's index moved to 1: the wallet refuses (igneum-money: the period index 0 is below the head's 1 (the index only advances)); the circuit's assertion is the same expression (prover/tests/vault.rs: the head at period 3 refuses a request declared into period 2)
- 02:10:48Z L into period 2 through the margin: admitted at block 541 (declared 600); matures at 900 and 1792379503
- 02:10:48Z THE ALTERNATION back into period 1 (declared 543, the head's index 2): refused with the same words
- 02:25:50Z L into period 3 through the margin: admitted at block 844 (declared 900); matures at 1200 and 1792380406
- 02:25:50Z THE ALTERNATION back into period 2 (declared 846, the head's index 3): refused with the same words
- 02:51:27Z, 02:56:45Z, 03:11:53Z, 03:26:56Z the thief completed 1 IGN at blocks 1310, 1423, 1725 and 2025 (each after its seconds bound; the blocks bound had long passed)
- the take: W + 4 L = 6 IGN exactly; the first run's extra wei is gone with the hole

**run1-cancel-within-D**: the two halves of L requested at blocks 28 and 29 and cancelled at 33 and 34; the refusal rows of the first page re-run and refused the same way (a policy below the floor; a third request past L; a policy change under the everyday key; a forged announcement at block 30 refused by the verifier with the pool unchanged; a completion before maturity; a completion proven at the maturity height 329 refused by the node at block 32 as past the including block; two requests against one head refused while the boundary is more than 64 blocks ahead); the partition row's 1 wei requested at block 846 (declared 846) and cancelled at 1,026 after the rejoin; the thief's completion of a cancelled note after maturity refused by the chain at block 2,027 (the position tag in the set).

**run2-recovery-at-once**: L requested at block 35; the recovery announced at 37 (a request while it is pending refused); the thief completed L at block 1,309 (02:51:22Z, the seconds bound); the recovery completed at block 2,513 (03:51:17Z), the old hash revoked; the thief's request under the old key refused after it.

**run4-malicious-recovery**: the compromised authority's policy announced at block 38, cleared by the owner's cancellation key at 39; the attacker's request and completion before R refused; the owner's own request while it was pending refused (a compromised authority can freeze withdrawals for up to R: the first page's row).

**run5-cancel-griefing**: the owner's request at block 40 cancelled by the compromised key at 41 (the allowance spent: the owner's second request refused); the rotation announced at 43 and completed at block 2,516 (03:51:37Z), the old hash revoked; the thief's request under the old key refused after it. The LIMIT row stands (F-800: the compromised key could cancel the rotation; the wallet prepared it, the lab did not send it).

**The partition row**: T partitioned at block 844 and sealing alone on three threads; its blocks reached S through R's static entry for T as on 7404 (the driver's admin_removePeer leaks: the first page's lesson), so T stood level with S at the rejoin (1,018 and 1,018) and no block was replaced; the in-flight request stood at position 59, height 846, its maturity 1,146 and 1792380408 as declared. The reorganisation of record is the first page's (depth 10, forced by hand on 7404 during the same windows, the five pending notes' maturities unchanged); it was not forced again here: nothing of this cut's rules changed on that row, and the re-run's purpose was the bound.

## 6. The suites

On lab box B (the lab directory at d48b00766, the 128-bit library built there from the patches, LIB f61656805434cc06, geth b68be65544d37f38) with `-tags money_miden`: pool and pool/miden 26 pass; params 11 pass; issuance and issuance/lanes 23 pass; consensus/igneum 255 pass (33.1 s); go vet clean on pool, consensus/igneum, the driver and the sender; the wallet `cargo test --release -p igneum-wallet-money` 24 pass (the lib's 19, the CLI's 2, the shared vectors, the toolchain, the vectors); the prover `cargo test --release -p money-stage1-prover` 4 pass (the toolchain, the vault's seven proven and verified at 128 bits, the two vector tests). On lab box A the same pool, wallet and prover suites pass.

The gate's two REDs on this branch (03:55Z and 04:03Z) were 35-finalize-writes: first the vault's write sites unlisted (listed in 402fbaf5d, the landing carrying the line Fork-Gate: IgneumChainPoolFrom as the night's did), then the same check reading that line as absent: its `grep -q` under `set -o pipefail` quits at the first match, git gets a SIGPIPE and the pipeline fails, so "gated" and "touched" were decided by a race (the night's landing passed it by chance). Cured in this landing: the check's deciding greps consume their input. A process finding for the review lead's register.

The live chains' rules stay byte-identical: the gate at zero everywhere but 7402, 7403, 7404, 7405, 7409 and 7410 (params), confirmed by the suites and by the code paths (nothing of pool/vault.go runs where poolOn is false).

## 7. The next cut (the panel's three findings, not cut tonight: the clock)

Each in its own commit with a failing-then-passing test, runs 4 and 5 re-run on a lab chain under them, pushed as refs/heads for the review's read:

1. **F-799, the revocation at the announcement.** Rule 7's revocation never binds under the floors (a pending note requested before an announcement matures at D while the old hash is revoked only at R >= 2 D). The cut: the revoked set gains the old policy hash's key at the announcement (S_announce writes it; S_policy_complete no longer does; S_rec_cancel removes it, so a cancelled recovery restores the old policy's pending notes), the completion's non-membership proof then refusing every pending note of the old policy from the announcement on; the test: a pending note requested before an announcement, matured, refused at completion; the same note completed after a rec-cancel.
2. **F-800, the rotation only the authority can cancel.** A recovery that changes pk_c is cancellable only by the recovery authority (S_rec_cancel admits the recovery key when the announced policy's pk_c differs from the head's; the cancellation key alone otherwise); the test: the compromised cancellation key's rec-cancel of a pk_c-changing recovery refused by the circuit, the authority's admitted; run 5's LIMIT row then sends the prepared cancellation and sees it refused.
3. **F-801, the order inside a block.** Finalize applies the block's announce and rec-cancel records before its withdraw and complete records (a two-pass walk over the pool transactions in pool_settle.go, the order public and the same on every node); the test: a withdraw and an announce in one block in either transaction order leave the head with the recovery pending and the withdrawal refused.

Then the merge statement, the lifetime ceiling, the unification's measurement and the t-of-n authority, as the first page's section 10 names them.
