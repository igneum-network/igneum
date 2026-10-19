# Project Money stage one, cut two: the shielded pool's state and transaction forms in the node, the lab row (18 October 2026)

SUPERSEDED by docs/money/STAGE1-JOIN-2026-10-18.md (the review's reading of 18 October 2026, F-773): the hash is Poseidon2 over Goldilocks, not BLAKE3; a spend is exactly two inputs and two outputs, not one to four and zero to four; a shield carries a proof of money-shield-v1 (this page's shield, whose commitment the node could not check against the value, is the hole the join closed), so a chain with the gate on and no verifier admits nothing, not shields only; the verifier of record is the pinned Miden verifier, the double a lab tool; and chain 7401 is retired with its data, 7402 the lab chain. The figures below are the record of the pool's rules under the lab double, nothing more.

PASS. Lab chain 7401 on lab box A (two nodes, geth.pool from this branch, sha256 f4b29347725f2121): the pool's gate on
from block 1, the spend verifier the lab's test double, 200 scripted shields and spends then the refusal rows, and the
chain's public figures reconciled against the driver's own wallet to the wei: turnstile 319,044,810,242,341,029,531
wei = the pool address's balance = the wallet's unspent notes = 417 IGN shielded in minus 97.955189757658871469 IGN
unshielded out minus 99,000 wei of fees; the public outputs' balance equal to the unshielded figure; 314 notes on the
chain and in the wallet; the root 0xef8c8275...2db8bbfe on both nodes and the wallet's own tree; the block hashes
equal on the sealer and the reader at 1, 7, 32, 62, 66 and 80 and the heads one at 103. Every refusal row refused
with the pool unchanged and the value returned (a replayed nullifier, a stale anchor, a wrong proof, an unreadable
record carrying value, a spend carrying value); the same note spent against the current root admitted after the stale
refusal. The unit tests green (eight new; the existing suites' count below).

## What this cut is (the branch lead/money-stage1-pool, from main b0c7d2ecc)

The ADR's section 4.3 items 2, 3, 4 and 7 in the node's rules, on a chain-id gate, with the proof verifier behind an
interface so cut one's prover (lead/money-stage1-prover) plugs in without touching the pool's code.

- **The state** (node/consensus/igneum/pool, pool.go): kept in the chain state under the pool address (derived from the
  chain id alone, `pool.Address`, no code, no key: the fund's and the sponsored escrow's form), so every node computes
  one state root. An append-only Merkle tree of note commitments of depth 32 (the frontier, one filled subtree a level;
  the leaves by index; the count; the root), the root sequence with each recorded root's sequence number (the anchor
  check: a spend's anchor is one of the last K = 64 recorded roots, `RecentRoots`; a root is recorded once a block that
  appended), the nullifier set, and the turnstile (shielded in minus unshielded out minus fees). The frontier write at
  a level is made only when the index's lower bits are all ones there (every other write would be superseded before
  it is read), so an append writes the leaf and, amortised, two frontier slots.
- **The hash** (hash.go): `Hasher` (a tree node, a commitment cm = H(v, pk, r), a nullifier nf = H(nk, cm, position))
  with BLAKE3 in derive-key mode under a domain tag a use as this cut's value (`blake3-v1`). The ADR names Poseidon2 as
  the in-circuit hash H with the tree laid out for a replacement at a gated height (section 4.7); cut one's prover
  fixes H's field and round count, and the swap is the one value behind the interface.
- **The two forms** (record.go): a SHIELD is a transaction of value to the pool address carrying the tag, the new
  commitment and the recipient's ciphertext (opaque to the node, kept in the transaction's data on the chain); the EVM
  moves the value to the pool as the block runs, and Finalize appends the commitment and raises the turnstile, or
  refuses and returns the value to the sender in the same block (nothing is ever stuck). A SPEND is a transaction of
  no value to the pool carrying the anchor, one to four nullifiers, zero to four new commitments with their
  ciphertexts, the public value v_pub and its output address, the fee, and the proof. Finalize checks, in order: the
  record's bounds, the public value and fee non-negative, no nullifier named twice, each nullifier unseen, the anchor
  among the last K recorded roots, the proof against the statement through the Verifier, the turnstile at or above
  v_pub + fee; then marks the nullifiers, appends the outputs, pays v_pub from the pool to the output and burns the fee
  (debited from the pool, credited to no one: the fee rule's part 1). Nothing is written on a refusal.
- **The Verifier** (verifier.go): `Verify(Statement, proof) error` with the statement (the anchor, the nullifiers, the
  commitments, v_pub, the fee, the output) bound by `Statement.Hash`, so a proof moved to another record fails. The
  node's default is `NoVerifier` (every spend refused: a chain with the gate on and no verifier admits shields only).
  `UnsafeDigestDouble` is the lab's test double, NOT a verifier: it accepts a proof whose first 32 bytes are the
  BLAKE3 derive-key hash of the statement's hash and the proof's remaining bytes, which anyone can make for any
  statement; it is installed by the backend only under `--dev --dev.pool.unsafe-verifier` on a research-range chain
  id (the backend refuses the flag anywhere else, before the node starts) and the node's log says UNSAFE at start.
- **The engine** (pool_settle.go): `settlePoolTxs` runs in Finalize after the sponsored lane's records, under the
  gate `poolOn` (height at or past `IgneumPoolFrom`); it reads the block's transactions to the pool address, admits or
  refuses each, and records the root once if the block appended. A block with no transaction to the pool writes
  nothing; a chain at gate zero writes nothing.
- **The gate** (node/params/config_igneum.go): `IgneumChainPoolFrom` by chain id, its own table (F-703's rule), ZERO
  IS OFF: every chain of record (7322, 7358, 7398, 7310) has no entry; the lab chain 7401 from block 1. The backend
  hands it to the engine beside the other gates (eth/ethconfig). The research chain id range's end moved from 7399
  to 7499 for the Money lab chains (every id to 7399 taken); the range is the development chain's alone.
- **The view** (api_pool.go): `igneum_pool` (the address, the hash and the verifier in use, the gate, the count, the
  root, the sequence, the turnstile, the balance) and `igneum_poolLeaf`.
- **tools/ci/finalize-writes.txt**: the five new write sites listed; the landing's commit carries `Fork-Gate:
  IgneumPoolFrom` (a change only a chain not yet started will run, as the check's rule reads).

## The unit tests (eight new, green on lab box A)

- node/consensus/igneum/pool: `TestAppendAndRoot` (the incremental root equal to the full tree's after each of 37
  appends, the empty root, the leaves by index, the sequence, the state reopened), `TestNullifierReplayRefused` (a
  replay refused, a nullifier named twice refused, a refusal writes nothing: the state root unchanged),
  `TestStaleRootRefused` (the root K - 1 appending blocks back inside the window, K back outside, an unknown root
  refused, the fresh root admitted), `TestValueConservation` (the turnstile and the balance to the wei through a
  shield, an unshield and a fee; over the turnstile refused; value on a spend refused; the fee burned),
  `TestVerifierDouble` (the double's own proof accepted; a wrong digest, a moved proof, a short proof refused;
  NoVerifier refuses everything, so the pool with no verifier admits no spend), `TestRecordRoundTrip`.
- node/consensus/igneum: `TestPoolGateOffByDefault` (no gate on any chain of record; the engine at gate 0 or above the
  block writes nothing for a block carrying a shield; at or under the block it appends), `TestPoolRefusalReturnsValue`
  (an unreadable record's value returned to the sender in the block; the default verifier; a block with no pool
  transaction writes nothing).
- The benchmarks on a memory state (EPYC 9454P, one core): `BenchmarkAdmitSpend` (two in, two out, the double)
  301.7 us an op, 454 KB, 469 allocations; `BenchmarkAdmitShield` 171.5 us an op, 212 KB, 202 allocations (the
  append: 32 node hashes, 32 slot keys, the leaf and the amortised two frontier slots).
- The existing suites on this tree (go test ./consensus/... ./params/... ./eth/ethconfig/... ./cmd/utils/...
  ./cmd/geth/... ./core/vm/igneumreg/...): 663 tests pass, 4 fail, every failure in cmd/geth (TestGethFailureToLaunch, its case 7, TestCustomBackend,
  TestCustomGenesis) and each failing identically on a clean worktree of main b0c7d2ecc in the same environment (the
  test harness's launch of the geth binary, not this cut); the consensus/igneum suite (32.7 s), params, cmd/utils,
  core/vm/igneumreg, pow/igneumhash and rxe1 green; consensus/igneum/igneumhash (the dataset suite, untouched by
  this cut) still running at the push.

## The lab row (lab box A, chain 7401, under the lab directory; pool-stage.sh, pool-read.sh, the driver)

Two nodes from a fresh genesis under `--dev --dev.chainid 7401` on the null algorithm (the difficulty rule {target 3 s,
halfLife 60, dtMax 60, min 65,536}: a block a second or so at the floor), each node's bond seed and bond key from 0600
files by the two file flags (never printed), `--dev.pool.unsafe-verifier` on both; S sealing on the CPU (two threads),
R reading from genesis as S's peer. The driver (node/cmd/money-pool-lab, committed) signs with the development chain's
published faucet key, keeps its own wallet (the lab's stand-in derivation from one seed; cut one fixes the real one),
and sends rounds of four transactions: a shield of 1 to 7 IGN with a 96-byte stand-in ciphertext, or a spend of one
or two spendable notes (appended in a round already mined) against the current recorded root, with a fee of 1,000
wei, a public value of a third of the inputs to the public output 0x...aeg15001 on every fourth transaction, and one
or two new notes; after every round the chain's count and root are checked against the wallet's. Then the refusal
rows, each a transaction mined with a success receipt and refused by Finalize (the log line names the reason, the
pool's figures unchanged, the value returned to the sender less the gas), then the reconciliation.

The run (UTC, 18 October 2026): S up 19:04:55 (pid recorded), R 19:04:57, peers 1 each; the driver from 19:04:58 at
head 5; 200 transactions sent by 19:05:5x (102 shields, 98 spends, 49 of them with a public output) over blocks 6 to
61; the refusal rows and the stale-anchor fill (one shield a block until the first root was 64 recorded roots back:
15 blocks) to block 86; the driver done 19:06:19 (80.9 s wall, 0.39 s a transaction waited); the nodes stopped by
their recorded pids at 19:07:15 at head 103 on both; the seeds and keys moved to
the lab directory (never deleted); the data kept under the lab directory
(the two earlier trials' data under lab-trial1 and lab-trial2: the first driver spent notes before their root was
recorded, a driver fault, cured by the spendable set; the second ran whole but recorded 50 roots, under K + 1, so the
stale row could not run; the fill was added and the row re-run from a fresh genesis).

## The figures

| what | figure |
|---|---|
| transactions | 200 scripted (102 shields, 98 spends: 49 private transfers, 49 with a public output) + 15 fill shields + 1 admitted spend after the stale row + 5 refused |
| notes on the chain / in the wallet | 314 / 314; spent 197; roots recorded 66 |
| turnstile = pool balance = wallet unspent | 319,044,810,242,341,029,531 wei (all three equal) |
| shielded in / unshielded out / fees burned | 417,000,000,000,000,000,000 / 97,955,189,757,658,871,469 / 99,000 wei; in minus out minus fees = the turnstile to the wei |
| public outputs' balance | 97,955,189,757,658,871,469 wei = unshielded out |
| the root | 0xef8c82758a2bcd108ed14a95f24c93f088bbbb8bdaebf93d52a10ba12db8bbfe on S, on R and the wallet's full-tree recompute |
| block hashes S = R | 1 0x476a8681b494e129, 7 0xded56b6055e6bc28, 32 0x631584c2a5c8b8a5, 62 0x4f1c373271400eeb, 66 0xd4b4bef105f0579a, 80 0x81784920a253211c; heads 103 both |
| the refusal rows | replayed nullifier refused at 62; the first root (block 6, sequence 1) outside the last 64 at sequence 65, the stale spend refused at 81, the same note against the current root admitted at 83; wrong proof digest refused at 84; unreadable record with 1 IGN refused at 85 and the value returned; spend carrying 1 wei refused at 86 |
| block validation time, a spend (the double) | S median 240 us, mean 302, p90 407, max 2,755 us over 200 admissions; R (an import) median 287 us, mean 443, p90 1,035, max 3,331 us |
| a shield | S median 116 us, mean 178, p90 233 us; R median 141 us |
| the pool's work a block (four records) | median 1.29 ms, max 7.4 ms on S; median 1.28 ms, max 9.8 ms on R |
| a refusal | median 24 to 28 us |
| the tree's growth | 314 leaves in 66 recorded roots; the root sequence 66; 32 node hashes an append |
| state slots a note | the leaf (1) + the frontier (amortised 2, exactly the index's trailing ones + 1) + the nullifier when spent (1); a block that appended adds the count, the turnstile, the root, the sequence and the anchor's sequence (5); a 32-byte key and a 32-byte value each in the pool's storage trie |
| chaindata growth | 11,232 to 206,875 bytes on both nodes over 103 blocks: 195,643 bytes, 623 bytes a note all in (the blocks, the 221 transactions' data with their ciphertexts and 41-byte stand-in proofs, the receipts and the state) |
| the proof's cost in these figures | the double's one BLAKE3 hash; the real verifier adds the ADR's estimated 2 to 15 ms a proof (section 4.5), to be measured when cut one's verifier is swapped in |

The counts in the nodes' logs run above the driver's (236 shield admissions on S, 226 on R) because the sealer
finalizes a block at assembly and again at import and the dev chain seals sibling blocks at a height; the driver's
receipts and the state are the figures of record.

## What the forms leave open (named, not claimed)

- The spend's carrier is a signed public transaction (any account pays the gas; the record is the pool's). The
  signerless shielded transaction type with no ECDSA signer, its fee split into the base fee and the tip, and its relay
  and mempool rules are F-761's stage 1 design item; this cut burns the statement's fee whole and the carrier pays gas.
- The statement binds the record's fields, not the carrier's other bytes; the real verifier's tx_hash input (the ADR's
  item 4) is set when the proof system fixes its public input layout.
- The value conservation inside the pool (inputs = outputs + fee + v_pub, every value in range) is the proof's claim;
  the node checks the public side (v_pub, the fee, the turnstile) and the double proves nothing. The lab's
  reconciliation holds because the driver's wallet is honest.
- Mining-reward privacy (the direct shielded coinbase), the per-d addresses (F-756) and ML-KEM note encryption are not
  in this cut; the ciphertext is opaque bytes.

## The exact next step

Swap the double for cut one's verifier: implement `pool.Verifier` over the pinned prover's verify call (the statement's
public inputs in the prover's layout, the proof bytes as the record carries them, `MaxProof` raised if the measured
proof needs it), set it where the engine is built (`SetPoolVerifier`) under a flag or by default when the chain's gate
is on, keep `UnsafeDigestDouble` behind `--dev.pool.unsafe-verifier` for the rules' tests, re-run this row on a fresh
7401 with real proofs from the driver, and record the proof size, the verification time on the N100-class node and the
pool's work a block beside these figures. Then the research chain's gate (7358) is the review lead's word after the panel's
read of the measured stage.
