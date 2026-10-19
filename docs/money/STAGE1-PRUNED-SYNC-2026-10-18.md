# Money stage one, the pruned sync: ADR-0001 section 4.6 row 5, the lab of record

Date: 18 October 2026, 23:0x to 23:3x UK. Branch: lead/money-pruned-sync from lead/money-stage1-join 0f260033f. The design: docs/money/PRUNED-SYNC-DESIGN-2026-10-18.md. The lab: tools/money/lab/pool-stage-pruned.sh on lab box A (AMD EPYC 9454P, no GPU), chain 7403 (the research range; 7402 retired with the join), geth.pruned sha256 81807adc9145fd3d (built with -tags money_miden: the pinned Miden verifier linked in), the pinned prover and the Rust wallet of the join. Every figure measured there unless marked ESTIMATE; nothing ran on the Mac.

## 1. The verdict: PASS against threshold 5

ADR 4.6 row 5 asks that chain growth and the pruned-sync path be measured at 1,000 and 10,000 transfers a day. The join measured the growth and left the path unbuilt (FAIL, not built). This cut builds it and the lab shows it: **a fresh node joined from two peers that had dropped every proof of the chain's records, reached the head, agreed with both on the pool root and the turnstile to the wei, took the pruned history's retained records from a peer and refused every corrupted one, served two wallets' scans from that history, and validated 20 further real spends as they arrived.** The row's figures are in sections 4 and 5. The row now reads: growth MEASURED, the pruned-sync path BUILT and MEASURED: PASS.

What the pass does not say: the peer transport of the retained sets is this lab's RPC (igneum_poolRetained, igneum_poolBackfill), not yet a message of the peer protocol; the pruning node keeps the leaf set in state (the ADR's frontier-only node is the later form); the figures are a 465-block chain's, extrapolated per record to a day (section 5). The ADR's other open rows stand as the join left them: 128 bits (1), the N100 (2).

## 2. What was built (the cut)

- **The state pins the retained set** (consensus/igneum/pool/pool.go): on every append the keccak of the note's ciphertext goes to a slot beside the leaf (ct[i]); on every block that appended, the count after it goes to a slot by height (count-at[h], written by Finalize after RecordRoot). Two slots the pruned sync checks served records against (section 3 of the design page). The rule changes only where the gate is on: 7402 (retired, its data quarantined) and 7403 (this lab); every chain of record is at zero, byte-identical.
- **The retained record and its store** (pool/retained.go, pool/extract.go): a record's bytes up to its proof, the proof's keccak and length, the record's position; one RetainedBlock a block under its number and hash in the chain database; pool.Extract reads a block's admitted records against its state and its parent's; pool.Check verifies a served block against the state (positions contiguous, every commitment the leaf at its position, every ciphertext hashing to ct[], every nullifier in the set, the end count the chain's count-at); pool.Prune is the pruned form of a transaction's data (the retained bytes and the proof's keccak as a 32-byte tail).
- **The pruner** (eth/pool_prune.go, `--pool.prune`, `--pool.prune.keep` at least 256): at every executed import the block's retained set is stored; 256 blocks later the block's stored body is rewritten with each pool transaction's data pruned. Off by default.
- **The pruned sync** (eth/downloader/downloader.go SetPruned, eth/handler.go, cmd/utils/flags.go): a fresh node under `--pool.prune` joins by snap sync and fetches no body or receipt before the pivot; the headers before it are checked by their work and kept alone; the state at the pivot by the snap protocol; the blocks after it whole and executed. The dev-mode miner's start no longer ends a snap sync on a node still at genesis (handler.markSynced: the one behaviour change outside the flag, reached only by a node started in snap mode that begins mining from an empty data directory; the start forms of record run `--syncmode full` (docs/build/RUN-A-SEALER.md, the minimum node of docs/testnet/SLO.md), under which the handler's snap flag is never set and the miner's start takes the old path; F-795, the panel's third reading: the case named rather than denied).
- **The RPCs** (consensus/igneum/api_pool.go): igneum_poolBlock now reads the store first (so a node that joined by the pruned sync serves wallets from its backfilled history); igneum_poolRetained gives a block's retained set; igneum_poolBackfill takes a range from a peer's igneum_poolRetained, checks every block and stores it, stopping at the first refusal with the words.
- **The lab's lying peer** (cmd/money-retained-proxy): a JSON-RPC relay that corrupts one block's igneum_poolRetained reply in one named way; the driver (cmd/money-pool-lab) gained -reuse, -rpc3, -stream-rpc and the refusal and restore rows as switches.

## 3. The lab, step by step (22:07:45Z to 22:24:43Z)

1. S (sealing on the CPU, the null algorithm, target 3 s) and R (reading), both `--pool.prune --pool.prune.keep 256`, both verifying every proof with the Miden verifier ("Shielded pool proof verifier verifier=miden-vm-3601-blake3-min108bits gate=1" in both logs), the bond seed and key of each from 0600 files, chain 7403 from genesis.
2. The records (the join's recipe, the driver's run 1, 82 s): two wallets born at height 6; 30 shields (105 IGN); 60 private transfers (30 with two real inputs, 12 unshields to the public output), every proof made by the pinned prover (51.4 ms median on eight threads) and verified by S and by R; blocks 7 to 87; 150 notes; turnstile 103,109,374,999,999,942,111 wei = the pool's balance = A + B unspent, RECONCILED to the wei; R agrees. A record's proof 74,828 bytes median; a spend's transaction data 77,665 bytes with the proof.
3. The pruning: the chain ran to 349; the pruner on S and R rewrote every block from 1 to 93 (every record's block): "Shielded pool proofs pruned" 90 records, **4,483,951 proof bytes dropped on S** (R the same); block 7's four shields' transaction data 57,908 / 57,748 / 56,788 / 58,548 bytes before, **1,360 each after** (the 1,328 retained bytes and the proof's 32-byte keccak); block 87's spend 2,869 after (2,837 + 32). The bodies of blocks 94 to 349 stay whole (inside the keep depth).
4. **T, the fresh node** (`--pool.prune`, from genesis, S and R its only peers, started 22:18:30.87Z): "Enabled snap sync head=0"; at 22:18:40.885Z (the downloader's forced cycle, 10 s after start) "Pruned sync: no bodies before the pivot origin=0 pivot=288 head=352"; the state download **128.42 KiB (5 accounts, 568 storage slots, 131,087 bytes) in 23 ms**; "Committed new head block number=288" at 22:18:40.917Z; the 64 blocks after the pivot imported whole and executed by 22:18:43.902Z, then "Snap sync complete, auto disabling". **T reached the head 13.0 s after its start, 3.0 s after its first sync cycle began.** T holds no body before the pivot (eth_getBlockByNumber(1) null; block 7 none), and the three nodes at block 438 give the same pool: count 150, root 0x516693...b7f221, seq 68, turnstile 103,109,374,999,999,942,111 wei. T's chain data after the sync: **599,740 bytes against S's 7,340,566** (S holds the chain's whole history with the recent bodies and its pruned bodies' old pages).
5. **The fourth test, the corrupted retained sets refused** (igneum_poolBackfill on T through the lying proxy to S):
   - block 7's first record with one ciphertext byte flipped: `refused "block 7 record 0 output 0: the ciphertext does not hash to the state's at position 0"`, refusedAt 7, blocks 1 to 6 (empty) stored, nothing of 7;
   - block 7's first record with one commitment byte flipped: `refused "block 7 record 0 output 0: the commitment is not the leaf at position 0"`, nothing stored;
   - block 87's last record dropped: `refused "block 87 ends at 148 by its records, 150 by its word"`, 80 honest blocks before it stored, nothing of 87;
   - then the honest backfill from S: 202 blocks taken, 86 skipped as already held, every block 1 to 288 checked against T's head state (the state root named in each report), **T's igneum_poolBlock equal to S's for all 288 blocks (90 records, 150 outputs)**; the retained bytes taken 210,060 for 90 records, in 0.20 s over localhost RPC.
6. **The 20 further spends** (the driver's run 2, 101 s, the wallets reused, every scan served by T's igneum_poolBlock, every receipt waited for on S, R and T): 20 transfers (10 two-input, 4 unshields), blocks 441 to 465, 190 notes; turnstile 99,615,234,374,999,923,922 wei, RECONCILED; **S, R and T agree on root, count and turnstile at block 465**; T's log 45 "Shielded pool spend admitted" lines (its validation passes), **3.479 ms median a spend admission on T (p95 4.110, max 8.585)**, the join's figure on S and R (3.1 ms). T's wallets' balances after the run: A 13,897,460,937,499,985,944 wei, B 85,717,773,437,499,937,978, scanned through T.
7. Down by recorded pid (S 1061295, R 1061347, T 1101345), seeds and the wallets' files under the lab directory; two false starts before this run (a shell function shadowing `head` in the script; the run restarted on the previous run's chain data) quarantined under -run0- and -run2-, nothing of them in the figures.

## 4. The third node's sync: time and bytes

| | |
|---|---|
| chain at T's start | 352 blocks, 90 records (30 shields, 60 spends) in blocks 7 to 87, every one pruned on both peers |
| headers taken | 352 (checked by their work), no body or receipt before the pivot |
| pivot | 288 (head - 64) |
| state taken | 131,087 bytes: 5 accounts, 568 storage slots (the pool's 150 leaves, 150 ciphertext hashes, 120 nullifiers, the frontier, 68 anchors and 68 count-at slots, the root, seq, count and turnstile), with their range proofs; 23 ms |
| bodies taken | 64 (the pivot and after), executed, every proof in them verified (none: the records sit before the pivot in this lab; the 20 later spends are the executed case, section 3.6) |
| time to the head | 13.0 s from the process start, of which 10 s the downloader's first forced cycle; 3.0 s of sync |
| chain data after the sync | 599,740 bytes (S: 7,340,566) |
| the retained history | 210,060 bytes for 288 blocks, 90 records, taken and checked in 0.20 s (the fourth test's three refused rounds before it: 0.10 s) |
| the same chain by a full sync | impossible from S and R (the bodies are pruned: a full-syncing peer would refuse them as bad bodies); from an archive it would take 6,159,464 bytes of proofs and 90 verifications (about 0.3 s of verifier time on one EPYC core) |

## 5. The figures per transfer, per day and per year

Per transfer, measured on this lab (a two-in two-out spend; a shield in brackets):

| what a pruning node keeps | bytes |
|---|---|
| the retained record in the store (the record's bytes before the proof 2,837 [1,328], the tx hash, the proof's keccak and length, the position) | 2,920 [1,410] |
| the pruned transaction in the body (the retained bytes and the 32-byte tail) | 2,869 [1,360] |
| the state (two leaves, two ciphertext hashes, two nullifiers: six slots; 231 bytes a slot as the snap sync carried them, trie overhead included) | about 1,390 [460] |
| **all in** | **about 7,180 [3,230]** |
| the proof dropped | 74,828 [55,700] |

The record is kept twice (the store for serving, the body for the public transaction history and the proof's hash); dropping the body copy, or the store's, is a 2,900-byte saving named for the next cut, which would put a transfer at about 4.3 KB. The join's retained figure (2,835) is the record once.

Per day and per year (the ADR's 1,000 and 10,000; the founder's 30,000), from the per-transfer figures (shields extra):

| transfers a day | with proofs a day / a year (the join) | pruned, this cut, a day / a year | a joining node takes (state + retained history) a year |
|---|---|---|---|
| 1,000 | 77.9 MB / 28.4 GB | 7.2 MB / 2.6 GB | 1.6 GB (0.5 GB state, 1.1 GB retained) |
| 10,000 | 779 MB / 284 GB | 72 MB / 26 GB | 16 GB |
| 30,000 | 2.34 GB / 853 GB | 215 MB / 79 GB | 47 GB (15 GB state, 32 GB retained); 4.3 KB a transfer would make it 24 GB a year of chain and 47 GB of sync |

ESTIMATE, from per-record sizes, not a day's run. The minimum node's 128 GB disk at 30,000 a day: 55 days with proofs kept (the join), about 19 months pruned at this cut's 7.2 KB, about 32 months at 4.3 KB.

## 6. The suites

The pool package: 9 tests of the join + 5 new (the split and the prune, the store's round trip, the chain's own sets accepted, twelve corruptions refused: a ciphertext byte, a commitment byte, a dropped record, a moved position, a moved start, a misreported end, a replaced nullifier, a spend's second ciphertext, a record added to an empty block, a record moved to the next block, an unreadable record, a kind its bytes do not carry; the ciphertext hash and count-at in state), all green. The node's `go test ./...` under the money_miden tag: `go test -v ./...` (igneumhash's package excluded: its TestReferenceVectors runs past every timeout, the join's item, untouched) 4,430 cases pass, 10 fail, 139 packages ok, 3 not: cmd/geth's three (TestGethFailureToLaunch, TestCustomBackend, TestCustomGenesis), tests/'s six and rlp/rlpgen's packaging, the same three packages the join recorded as identical on a clean main and none in a package this cut touches; the package run with igneumhash included: 30 ok and cmd/geth before igneumhash's timeout ended it. No downloader unit test for the pruned mode (the lab is its test; named below).

## 7. Caveats

- The transport of the retained sets is RPC (a syncing node is given a peer's HTTP endpoint); the peer protocol's message is the next cut's. The lab's backfill ran over localhost; a remote peer's time is the bytes over the link (210 KB here).
- The tx indexer of a node that joined by the pruned sync walks the headers-only blocks and logs "Failed to decode block body" for each (10,848 lines in T's log for 288 blocks: a log a block a pass, nothing wrong): the indexer should skip blocks it holds no body for; a one-line item.
- The pruner rewrites live bodies; a body already in the ancient store (90,000 blocks) is not rewritten, so a node that turns the flag on late keeps the proofs the freezer already holds.
- A pruned body no longer matches its header's transaction root: eth_getTransactionByHash of a pruned record's transaction returns nothing (the hash is gone with the proof), the block's public transactions and receipts stay; a full-syncing peer drops a pruning node as a bad-body peer (by design; the archive serves full syncs).
- The leaf set stays in state (igneum_poolPath builds witnesses from it); the frontier-only node and the wallet-side witness are the later form, 32 bytes a note of state less.
- The sync time's 10 s is the downloader's forced-cycle timer (minPeers 5 or ten seconds), not work; a chain of 352 blocks is small: headers are the one part of the pruned sync that grows with the chain's age (about 600 bytes a block: 6.3 GB a year at 3 s; the header chain's own pruning is outside Money).
- The chaindata sizes of S and R do not show the drop (the LSM keeps the old pages until compaction); T's size is the honest measure of a pruned node's disk.
- Two slots a note and one a block are added to the rule where the gate is on; the live chains' rules are byte-identical (every gate at zero; the flag off by default; a node without the flag behaves as the join's).

## 8. The next step

The peer protocol's retained-set message (GetPoolRetained / PoolRetained beside GetBlockBodies) so a joining node needs no RPC endpoint, with the backfill run by the downloader after the pivot's commit; then the body copy dropped (4.3 KB a transfer); then the tx indexer's skip. The vault's S_vault_create stays the stage's next statement; the N100 run stays open.
