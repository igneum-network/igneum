# Payout privacy specification: mining rewards without linking a miner to a balance (Project Money, 18 October 2026)

Status: DESIGN, on paper; PANEL-READ 18 October 2026 (docs/money/PANEL-READING-2-2026-10-18.md: PASS with re-cuts F-782 and F-783, applied). Stage 7 of ADR-0001 section 6, specified before any miner-privacy claim, as section 5 of the ADR requires. Nothing here is built or measured; every figure that is not a rule is marked ESTIMATE. The ADR's definitions are used unchanged (H, B, note, cm, nf, the tree, the anchor, the turnstile). The chain's settlement figures are read from docs/settlement/LEDGER.md, docs/economics/FEE-RULE-2026-10-15.md and docs/tokenomics/SUPPLY-2026-10-18.md on main b0c7d2ecc. No em dash appears on this page. No price or earning figure appears or can be derived.

## 1. The decision

Shielded coinbase: public amount, hidden owner. Every key on a 64-block payout list may register, once, a shielded payout address in place of its 0x address; at the window's close the node mints that key's exact share as a note whose amount is public, whose randomness is derived from public data, and which carries no ciphertext. The node verifies the mint to the wei exactly as it verifies the public credit today, the turnstile counts the minted amount in, and the settlement ledger gains one column, minted. The fund's one percent stays public to the fund's 0x address by rule. A pool outside the node (a coordinator that pays its members itself) pays them by shielded outputs from its own public account in one transaction per window, which hides each member's share and leaves the pool's total public. The alternative, a hidden amount under the ADR's ordinary note with a ciphertext, is rejected for the coinbase because the node could then verify issuance only by a proof per mint, which the minimum node cannot afford at a thousand keys a window, and because the amount is already public in the settlement that every miner can check.

## 2. The leak, stated

Today (docs/settlement/LEDGER.md): a block with a payout list has as its coinbase the list root's 20-byte prefix, an address nobody holds; at the window's close the node credits each listed key's 0x payout address with its pro rata part of the miner part of the subsidy plus the tips, exact to the wei, and the ledger reconciles every block. The chain therefore publishes, for every window, "key K earned v_K wei, paid to 0x address A_K". With the shielded pool beside it, a miner who wants privacy shields that balance: a public transaction from A_K into the pool with a public amount v (ADR section 4.3 item 9). The ledger then carries "A_K shielded v at height h" a block or a day after "A_K was paid v_K at height h minus one". When v = v_K, or v equals the sum of a few windows' payouts, the deposit is the miner's by amount and timing, and the one note it created is known to belong to K. Every later unshielding of a matching amount, and every timing pattern that follows the windows, extends the link. The same leak exists for a member of an outside pool paid by a public transaction from the pool's address.

What is private by the ledger alone, and what the leak undoes: the pool hides who paid whom and how much inside it; the leak marks one entry note as "a miner's, this miner's, this amount", and from a small pool that mark survives spends by timing and by amount correlation (ADR section 4.7, the anonymity set is the pool).

## 3. The options and their cost

| option | what the ledger shows | the node's verification of issuance | cost per paid key | why chosen or not |
|---|---|---|---|---|
| (a) The public credit, then the miner shields it | the credit, the deposit, both amounts, the timing | as today | nothing new | the leak of section 2; kept as the fallback, labelled a privacy boundary by the wallet |
| (b) A shielded coinbase with a public amount and a hidden owner (chosen) | "key K earned v_K and it was minted as commitment cm_K" | the node computes cm_K itself from public data and checks the sum of minted amounts equals the miner part plus tips to the wei, as it checks the credits today | one H for the commitment, 32 H for the tree insertion, no proof | the amount is public already; the owner's address is a registered shielded address, so the note's spend is unlinkable to K from the ledger; no ciphertext, no proof, no change to the minimum node's budget beyond the hashes |
| (c) A shielded coinbase with a hidden amount | a commitment and a ciphertext per key; the window's total public | a proof per window that the hidden amounts sum to the public total (a range proof and a sum, one STARK per list) | one proof per window of ESTIMATE 80 to 140 KB and ESTIMATE 2 to 15 ms to verify, plus a ciphertext per key | rejected: the settlement ledger's exactness to the wei, which every miner checks by reading the list and the chain (LEDGER.md identity 3), would become a proof the ledger must trust; and the amount is public in the list's weights anyway (each entry's share of the window is its weight over the sum), so hiding the minted amount hides nothing |
| (d) The outside pool pays by public transactions | the pool's address paying each member's 0x address | as today | nothing new | the leak of section 2 for the member |
| (e) The outside pool pays by one shielded transaction per window (chosen for outside pools) | one transaction from the pool's public account into the pool with a public total, up to 64 outputs per transaction, each a note to a member's shielded address, each amount hidden | the ordinary transfer statement extended to 64 outputs (the pool's statement S_payout: inputs public, v_pub = the total, outputs' amounts sum to it, each in range) | one proof per 64 members, ESTIMATE 100 to 200 KB, ESTIMATE 3 to 20 ms to verify; one ciphertext per member | the pool's total stays public (the pool is a public account), the members' shares are hidden; a member's privacy then rests on the pool's honesty about which address it paid, as it rests on the pool's honesty about the share count today |

The fund's one percent: public, to the fund's 0x address, every block, by rule (FEE-RULE-2026-10-15.md part 2: the fund is credited from the subsidy alone). The ADR's "transparent issuance" requires that every wei issued be countable from public data; the fund's credit is the plainest instance and is not changed by this page.

## 4. The chosen construction, exactly

### 4.1 Registration

A key K (the 20-byte bonded key of the share lane, LEDGER.md) registers a shielded payout address by one public transaction to the registry (the same path as the bond's set-signer, docs/product-b/DESIGN.md's `set-signer` form): data = K then the address (pk, ek, d; ADR section 4.3 item 1, 1,227 bytes), from the bond's account, no amount. The node stores payout_address[K]. Rules: a registration replaces the previous one from the next window's first block, never inside a window; a registration with a malformed KEM key (not 1,184 bytes, or not a valid ML-KEM-768 encapsulation key by the standard's check) is refused; a second registration in the same window is refused; an unregistered key is paid to its 0x address as today. Registration is public: the ledger says "K pays to shielded address S_K". Ember offers the registration as a default when the wallet is present and never sends it by itself (ADR section 10).

### 4.2 The mint

At the window's close, for each listed key K with a registered address and a pro rata amount v_K > 0 (dust and carry as today, LEDGER.md):

- r_K = B("mint", chain_id, block_hash, window_index, K), 256 bits, derived from public data;
- cm_K = H("note", v_K, pk_K, r_K), the ordinary note form (so the minted note is spent by the ordinary transfer statement S_transfer and is indistinguishable from any other note once in the tree);
- the node appends cm_K to the commitment tree in the list's block, in list order, after the block's transactions' commitments, and credits the pool address with v_K in place of the 0x credit (the pool form of record keeps the turnstile equal to the pool address's balance, and an unshield pays from that balance);
- the block's mint record (a new field of the block body beside `withdrawals`) carries, per key, (K, v_K, cm_K); a block whose mint record differs from the node's own computation is invalid;
- no ciphertext: the miner's wallet recomputes (v_K, pk_K, r_K) from the public record and its own registration, and derives the note's position from the block;
- the turnstile adds v_K (ADR section 4.3 item 7 as re-cut by F-761).

The amount is public, as the credit is today; the owner is the holder of sk_spend behind pk_K; the ledger learns nothing after the mint. A key paid to a shielded address is not paid to its 0x address; the two are exclusive per window.

### 4.3 The outside pool's payout (S_payout)

A pool that pays its members itself (docs/ecosystem/POOL-NONCUSTODIAL.md) does so by one shielding transaction per window from its public payout account: v_pub = the window's total for the pool's members, the statement S_payout with up to 64 output notes whose amounts sum to v_pub, each output encrypted to the member's registered shielded address (the pool reads the registry as the node does), and the ordinary fee. A window with more than 64 members takes more than one transaction. The pool's journal (POOL-NONCUSTODIAL.md section 3) gains the commitment per member so a member can check its note against the journal and the chain. What the member checks: the window's total on the chain equals the journal's credits times the rate (as today), and its own note opens to the journal's row.

### 4.4 The wallet's side

The miner's wallet scans the public mint records for its registered pk_K (no decapsulation, no cost beyond reading the block's record), reconstructs each note, and holds it as an ordinary note. Ember shows the window's payout as "minted to your shielded address" and the wallet shows it as a note in the pool with the ADR's settlement words. For an outside pool's member the wallet scans S_payout outputs by decapsulation as for any transfer.

## 5. The verification the node performs

Per list block, in order: (1) the payout list and its pro rata amounts as today (the Pay rule, node/consensus/igneum/shares); (2) for each key with a registered address, v_K from the same arithmetic, r_K and cm_K recomputed, the mint record compared byte for byte; (3) the sum over the mint record plus the sum of public credits equals the miner part plus the tips to the wei, the fund's part beside it unchanged; (4) the tree extended by the mint record's commitments in list order, the new root recorded in the pool's state under the pool address (the state root covers it; the stage-one form has no header field for it); (5) the turnstile advanced by the mint sum and the pool address's balance by the same. A block fails on any mismatch. Nothing in this path is a proof; it is arithmetic and hashing from public data, so a node of the minimum class verifies it as it verifies the credits today.

## 6. The supply reconciliation the ledger keeps

The settlement ledger (tools/settlement/ledger.py, LEDGER.md) keeps its identities and adds:

- identity 3 (the whole list to the wei) splits each entry's amount into credited (to a 0x address, as today) or minted (a commitment in the mint record); the sum of both equals the miner part plus the tips;
- a new column, minted, per block and per window, read from the mint record and from the node's `igneum_supply`, which gains `mintedShielded` beside `issued`, `fundIssued`, `burned` and `net`;
- the pool identity: turnstile = shieldedIn + mintedShielded minus unshieldedOut minus shieldedFees, never negative, read from `igneum_supply`'s new fields and recomputed by the ledger from the blocks;
- the supply identity unchanged in form: issued = fund + credited + minted, and the schedule's figure per block (SUPPLY-2026-10-18.md) is the authority for issued.

A minted note that is later unshielded appears in unshieldedOut as any note's unshielding does; the ledger does not, and cannot, say which.

## 7. The cost per block

Per list block with n registered keys: n evaluations of B for the randomness, n of H for the commitments, 32 n of H for the insertions (a batched insertion of n leaves costs fewer; the figure is the bound), 32 n bytes of block body for the commitments plus 28 n for the key and amount. At n = 1,000 (ESTIMATE, the public test network's address count is below this): 33,000 H evaluations by the one-leaf-at-a-time form; the node's Go Poseidon2 merge measured about 2 microseconds on one EPYC core (the review's reading of 18 October 2026: the shield admission benchmark 225 microseconds under Poseidon2 against 172 under BLAKE3 over 32 merges, node/consensus/igneum/pool), so ESTIMATE 4 to 8 microseconds on one N100-class core: 130 to 260 ms once per 64 blocks, which crosses the 165 ms a list block can spare inside the G1 budget of 250 ms p99 (docs/reliability/DEVNET.md); the batched insertion of n consecutive leaves (about n + 64 merges: the leaves' subtree hashed once, then the frontier) is therefore the rule, at about 1,100 H, 4 to 9 ms on the N100-class core, and the stage 7 measurement replaces both figures. Body growth: 60 KB per list block at n = 1,000; 7,200 blocks a day at twelve seconds is 112 windows a day, so ESTIMATE 6.7 MB a day at n = 1,000 (the ADR's section 5 figure, 3.6 MB of commitments a day, is the commitments alone), within the ADR's growth table beside the transfers' own bytes. An outside pool's S_payout adds one shielded transaction per 64 members per window, inside the transfer budget of the ADR's section 4.5.

## 8. What is not claimed

- No miner-privacy claim until the mint is landed on the research chain, the ledger's minted column reconciles every window to the wei, and the link measurement of the test plan (group K, rows K-05 and K-06) is run: before and after, the fraction of a miner's entry notes a correlation adversary links to its spends by amount and timing.
- The amount a key earned per window is public, as today; so is the key's registered shielded address; so is the fact that it mines. What is hidden is everything after the mint: balance, spend time, payee, whether it was unshielded.
- A key whose registered address is linkable to another address by F-756 (pk and ek in the clear) is linkable; the per-diversifier derivation is stage 1's item, and until then a miner who registers the same pk it uses to receive payments has linked its mining to its receiving.
- An outside pool sees its members' shielded addresses and amounts, as it sees their 0x addresses and amounts today; the pool's honesty is unchanged as an assumption.
- The pool's view of a miner's IP and hashrate is docs/money/NETWORK-PRIVACY-SPEC-2026-10-18.md section 5, not this page.
- Timing: the mint lands at the window's close, so a spend soon after a mint is correlated by time on a small pool; the measurement decides how much.
- Nothing is measured; the hash cost per key and the body growth are estimates.

## 9. The tests that prove it

Rows in docs/money/TEST-PLAN-2026-10-18.md: P-01 (a registration with a malformed KEM key refused), P-02 (a second registration in one window refused; a replacement applies from the next window), P-03 (the mint record recomputed by a second node byte for byte), P-04 (a block with a mint record off by one wei refused), P-05 (a block with a mint omitted or duplicated refused), P-06 (the ledger's identity 3 over a window with mixed credited and minted entries, to the wei), P-07 (the turnstile's mintedShielded equals the mint records' sum from genesis), P-08 (the miner's wallet reconstructs every minted note without a ciphertext and spends one by S_transfer), P-09 (an S_payout with 64 outputs accepted; with 65 refused; with a non-conserving sum refused), P-10 (the G1 budget at n = 1,000 registered keys on the minimum node), K-05 and K-06 (the link measurement).

## 10. Sources

- docs/money/ADR-0001-PRIVATE-PQ-SAFE-MONEY-2026-10-18.md section 5 (the direct shielded coinbase as decided), section 4.3 items 7 and 9 (the turnstile and the public side), section 4.7 (the public-side leakage), section 10 (Ember offers, never sets)
- docs/settlement/LEDGER.md (the 64-block lists, the coinbase as the list root's prefix, identity 3 to the wei, the ledger's columns, `igneum_blockReward`, `igneum_shareAccount`)
- docs/economics/FEE-RULE-2026-10-15.md part 2 (the fund's one percent from the subsidy alone; the tip into the list)
- docs/tokenomics/SUPPLY-2026-10-18.md (the schedule, the fund's share a block)
- docs/ecosystem/POOL-NONCUSTODIAL.md (the outside pool's credit by key and payout the coordinator cannot redirect)
- docs/product-b/DESIGN.md (the registry's set-signer form; the settlement words)
- docs/reliability/DEVNET.md (the minimum node; G1)
- Zcash shielded coinbase, decryptable outputs: https://zebra.zfnd.org/internal/zebra_consensus/transaction/check/fn.coinbase_outputs_are_decryptable.html
- Zcash ZIP 209: https://zips.z.cash/zip-0209
- ML-KEM-768 sizes (FIPS 203): https://pkg.go.dev/crypto/mlkem@master
