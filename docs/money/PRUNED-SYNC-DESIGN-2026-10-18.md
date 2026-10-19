# Money: the pruned sync (the design of record for ADR-0001 section 4.6, row 5)

Date: 18 October 2026, 23:0x UK. Branch: lead/money-pruned-sync from lead/money-stage1-join 0f260033f. The lab that measured it: docs/money/STAGE1-PRUNED-SYNC-2026-10-18.md. This page says what a validating node must hold once the proofs are gone, why that is enough, what the chain commits to, how a fresh node joins from peers that have dropped the proofs, what it trusts and what it does not, and what the archive is for. Every statement about the code is about this node (the EVM-family node, consensus/igneum/pool, pool_settle.go) as it is on this branch.

## 1. What a validating node holds after the proofs are gone

The pool's consensus state lives in the chain state under the pool address (pool.Address(chainID), a storage trie inside the header's state root), and holds no proof bytes at all. After this cut its slots are:

| slot | what | size |
|---|---|---|
| count | the number of notes in the tree | 1 slot |
| frontier[level], 32 levels | one filled subtree a level: the append path | 32 slots |
| leaf[i] | the commitment at index i (the full leaf set, kept because igneum_poolPath builds a spend's witness from the leaves; the ADR's frontier-only node is the later form, with the witness served by a wallet-side or indexer-side tree) | 32 B a note |
| ct[i] | **new in this cut:** keccak of the ciphertext the note at i was admitted with | 32 B a note |
| root, seq, anchor[root] | the current root, the number of roots recorded, each recorded root's sequence number (a spend's anchor must be among the last K = 64 recorded: AnchorValid) | 1 + 1 + 1 a block that appended |
| count-at[height] | **new in this cut:** the count after each block that appended | 1 a block that appended |
| nf[nullifier] | the nullifier set | 32 B a spent note |
| turnstile | shielded in minus unshielded out minus fees | 1 slot |

That is the whole of what AdmitShield and AdmitSpend read: the anchor check reads anchor[] and seq; the double-spend check reads nf[]; the value check reads turnstile; the append writes frontier, leaf, ct, count. A node holding exactly this state at height h validates every spend at h + 1 as a node that kept every proof since genesis does: the two run the same code on the same slots.

What a node holds *outside* the consensus state, for wallets: the retained records (pool.RetainedBlock, one entry a block under its number and hash in the chain database): each admitted record's bytes up to its proof (the commitments, the ciphertexts, the nullifiers, the anchor, the values, the public output: 2,835 bytes a two-in two-out spend, 1,328 a shield, the join's figures), the proof's keccak and its length. This is what igneum_poolBlock serves a scanning wallet and igneum_poolRetained serves a joining node. The ciphertexts are not consensus (nothing validates them) but they are what a holder's money is written in, so section 3 commits to them too.

## 2. Why that is enough to validate every later spend

A spend at height h + 1 is admitted by four checks and nothing else: its anchor is one of the last K roots the pool recorded (state); none of its nullifiers is in the set (state); its proof verifies against its statement (the proof travels with the spend, verified by the node that imports it, kept until the pruning depth); the turnstile covers its public value and fee (state). None of these reads a past proof. A past proof's only consequence was the state it produced, and the state is what the node holds.

The pruned proofs' validity rests on the block being in the chain, exactly as a pruned transaction's signature does on Ethereum: a node that snap-syncs mainnet downloads the state at a pivot and never re-checks the signatures (or re-executes the transactions) of the blocks before it; it checks the headers' proof of work (or, post-merge, the beacon chain's finality) and the state root at the pivot, and executes everything after. Here a block's header commits to the state root the block's admissions produced; the chain's work commits to the header; a proof that did not verify would have been refused by every node that executed the block (its value returned, its record never admitted), so the state root the honest majority's work carries is the root of the admitted records alone. A syncing node that takes that root takes the verdict of every node that verified the proofs, under the same assumption that makes it take the chain at all.

## 3. What the header commits to (what the code does)

The existing state root, not a new field. The pool's storage trie sits under pool.Address(chainID) in the account trie the header's Root names, so the pool root (the Poseidon2 tree's root, slot root), the nullifier set (nf[]), the leaf set, the ciphertext hashes (ct[]) and the per-block counts (count-at[]) are all committed by every header from the landing check. No pool root or nullifier commitment is added to the header; a syncing node gets them through the state root's Merkle proofs (the snap protocol's account and storage range proofs), which is the existing state sync.

The two slots this cut adds are for the retained set, which the state did not pin before: without ct[i] a peer could serve a wrong ciphertext for an admitted note and the wallet would never open it (no value moves, but the holder cannot see a note that is theirs: a denial a lying peer could do at no cost); without count-at[h] a peer could serve a block's records as the next block's (the positions still contiguous, the leaves still matching) and a wallet's birthday skip would miscount. With them, pool.Check (section 4) pins a served block's retained set to the state in these respects: the positions, the commitments, the ciphertexts, the block boundaries, and the coinbase note's place among them (F-791: a block that minted one ends past it). What the state does NOT pin (F-794, the panel's third reading): a spend record's nullifiers are checked as members of the set, not as this record's (a lying peer can swap one spent nullifier for another and the served record still checks; a wallet scanning through it may read a note of its own as unspent when it is spent, or the reverse, and a spend it then prepares is refused by the node: a denial, no value moves); the proof's keccak, its length and the carrying transaction's hash are served on the peer's word (a wrong proof hash fails the archive's check, nothing else). The next cut names the cure: a per-record digest in state (the record's retained bytes and its proof hash, one slot a record), or the nullifiers by position.

Cost of the two slots: 32 bytes of state a note (about +2.5 per cent on the retained 1,328 of a shield; the ct hash is a slot write at admission, 1 keccak) and one slot a block that appended. The rules change only where the gate is on (7402, retired with its data; 7403, this lab); every chain of record is at zero.

## 4. The sync protocol (as built: --pool.prune)

A pruning node (--pool.prune, every node by default off):

1. at every import of a block it executes, writes the block's retained set from the block's state and its parent's (pool.Extract: a record is admitted when its first commitment is the leaf at the next position and, for a spend, its first nullifier entered the set at this block) to the store;
2. 256 blocks later (--pool.prune.keep, at least 256: the snap pivot's 64 full blocks, the pivot's advance by up to 120 before those are fetched, and a margin; far inside the freezer's 90,000, so a body reaches the ancient store already pruned) rewrites the block's stored body with every pool transaction's data cut to its retained bytes plus the proof's keccak (pool.Prune): the proof bytes leave the disk. The body no longer matches the header's transaction root, so this node serves no *full* sync of that block (a full-syncing peer drops it as a bad body: pruned history is for pruned peers; the archive serves full syncs); the headers, the receipts and the public transactions stay;
3. serves igneum_poolBlock (wallets) and igneum_poolRetained (joining nodes) from the store.

A joining node (--pool.prune on a fresh data directory, which puts it in snap sync):

1. takes the headers from its peers and checks each by the chain's rules (the work, the seal, the ancestry), as snap sync does;
2. fetches no body and no receipt before the pivot (head - 64 at the start of the sync: the downloader's pruned mode, downloader.go skipTo): the blocks before the pivot are headers alone on it, by design;
3. takes the state at the pivot by the snap protocol from any peer, pruned or not (the account and storage ranges with their proofs against the pivot header's Root): the pool's storage with it;
4. takes the pivot's body and the bodies after it whole, with their proofs, and executes every block from the pivot on: Finalize verifies every proof in them, exactly as the sealer did;
5. backfills the retained sets of the blocks before the pivot from a peer (igneum_poolBackfill source from to: the lab's transport is RPC; the peer protocol's message is the next cut's) and checks every block's set against the pool state of its own head (pool.Check): the positions contiguous from the previous block's end, every commitment the leaf at its position, every ciphertext hashing to ct[] at that position, every nullifier in the set, the block's end count equal to count-at[height] when the block appended and no record when it did not. The first block that fails stops the backfill with the refusal's words; nothing of it is stored; the node is as valid a validator without it (the backfill serves wallets, not consensus);
6. from then on it is a pruning node like any other.

Refusal: a block whose retained set does not match is refused (the lab's fourth test: a ciphertext byte flipped, a commitment byte flipped, a record dropped: each refused at the block, nothing stored, the honest peer's set then taken).

## 5. The trust statement

A node that joins by the pruned sync trusts the chain's work for the pruned history exactly as it trusts it for the pruned signatures: it does not re-verify the proofs of the blocks before its pivot, as a snap-syncing Ethereum node does not re-verify the signatures or re-execute the transactions before its pivot. What it checks itself: every header's work and rules from genesis; the state at the pivot against that header's root; every block, every proof and every state root from the pivot on; every retained record it serves against the state. What it does not check: that the proofs of the blocks before the pivot verified (it takes the state they produced). The assumption under which this is sound is the chain's own: a majority of the work executed those blocks honestly. The proof-pruning rule adds no assumption beyond the one a non-archive node already makes.

What the pruned sync does NOT give: a full-sync from pruned peers (impossible by construction: the bodies are gone), and an audit of the pruned history's proofs from pruned peers. Both are the archive's.

## 6. The archive's role

An archive node (no --pool.prune) keeps every body whole: the proofs with them. It serves (a) full syncs to any node that wants to re-execute everything from genesis and re-verify every proof (the outside review's node); (b) proofs by request to anyone holding the retained record's proof hash (the 32-byte tail of a pruned record is the keccak of the proof the archive holds). One archive per operator who cares, and the founder's own, is the ADR's form; a pruned node is the minimum node of section 4.6.

## 7. What is left (the next cuts)

- the peer protocol's message for the retained sets (today the RPC igneum_poolRetained and igneum_poolBackfill);
- the frontier-only node (the leaf set dropped from state; the witness from a wallet-side tree): 32 bytes a note of state less;
- ancient-store pruning for a node that joined before the flag (the pruner rewrites live bodies; a body already in the freezer is not rewritten: a node turning the flag on after 90,000 blocks keeps those proofs);
- the ciphertext bound into the statement (the join's caveat): with ct[] in state the pruned sync no longer needs it, but the relay-swap finding stands for the sealer's admission.
