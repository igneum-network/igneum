# Network privacy specification: what the ledger hides, the network must not undo (Project Money, 18 October 2026)

Status: DESIGN, on paper; PANEL-READ 18 October 2026 (docs/money/PANEL-READING-2-2026-10-18.md: PASS with re-cuts F-784 and F-785, applied). The requirement is ADR-0001 section 3's closing paragraph (the IP a transaction is sent from, the IP a wallet syncs from, and a wallet's query pattern each link a person to a payment the ledger hides) and section 7's malicious-wallet-server and correlation rows. Nothing here is built or measured. No em dash appears on this page. No price or earning figure appears or can be derived.

## 1. The decision

Four surfaces, one rule each for the first release, one measurement each:

1. Transaction relay: every shielded transaction (and every vault statement) enters the network by Dandelion++ stem routing on the node's own peer connections, with the paper's parameters; a wallet submits only to its own node or over Tor to a public node, never to a public node in the clear by default.
2. Wallet sync: the full scan on the owner's device is the only mode the privacy claim covers; a light mode that downloads every ciphertext from a server (compact blocks with the whole ciphertext, no follow-up fetch) is allowed and its leak is stated; a viewing-key server mode is allowed only with the ADR's home-screen sentence and is outside the claim.
3. The public RPC: no request body, no address and no IP is written to any log; the rate limiter keys on a keyed hash of the IP with a key rotated every hour and never stored; the configuration is published and the logs are audited by a test.
4. The pool: a pool learns a miner's IP, key, shares and registered payout address and nothing of its wallet; the pool keeps no log beyond the journal of record; the miner may connect over Tor; the per-key registered address is the only link between mining and the pool, and it is a public one.

## 2. Transaction relay: the sender's IP

### 2.1 The leak

A node that receives a transaction first, or a listener connected to many nodes (a supernode), records which peer sent it and when; the first-spy estimator identifies the source of a diffused transaction with high probability on Bitcoin-style gossip (Fanti and Viswanath 2017, the deanonymisation of diffusion; Biryukov, Khovratovich and Pustogarov 2014 for the client-to-entry-node fingerprint). On this chain the node is an EVM-style node with devp2p transaction gossip (node/eth, the transaction fetcher and broadcaster as inherited), which announces a new transaction's hash to every peer at once: the plainest diffusion.

### 2.2 The rule

Dandelion++ (Fanti, Bojja Venkatakrishnan, Bhat, Lu, Niu, Viswanath, "Dandelion++: Lightweight Cryptocurrency Networking with Formal Anonymity Guarantees", SIGMETRICS 2018, arXiv 1805.11060), on the form of the original Dandelion (Bojja Venkatakrishnan, Fanti, Viswanath, "Dandelion: Redesigning the Bitcoin Network for Anonymity", SIGMETRICS 2017, arXiv 1701.04439), with these parameters, fixed in the node and read in its configuration page:

| parameter | setting | from |
|---|---|---|
| epoch | 600 seconds; at each epoch a node picks two outbound stem peers and decides whether it is a stem or fluff node this epoch | the paper's section 5 (the 4-regular anonymity graph approximated by two outbound relays per node) |
| forwarding in stem | a received stem transaction is sent to one of the node's two stem peers, chosen per inbound peer (the per-inbound-edge mapping, the paper's "one-to-one" routing) | the paper's section 5.3 |
| fluff probability q | 0.1 at each hop; the expected stem length 10 | the paper's section 5 |
| stem timeout | 60 seconds: a node that relayed a transaction in stem and has not seen it fluffed by then fluffs it itself (the fail-safe) | the paper's section 5.4; Monero's monerod uses a comparable embargo |
| fluff | the node's ordinary broadcast (the hash announced to every peer), with a random delay drawn per peer from an exponential with mean 5 seconds before the first announcement | the paper's diffusion with per-peer delays |
| what is stemmed | every transaction of the shielded type (ADR F-761's new type) and every vault statement; public-side transactions follow the inherited gossip unchanged in the first release | this page |
| the mempool | a transaction in stem is not in the public mempool (not returned by `txpool_content` or `eth_getTransactionByHash`) until fluffed; the embargo timer re-broadcasts | the paper's embargo |
| black-hole defence | the stem timeout above; the node does not learn whether its stem peer forwarded | the paper's section 5.4 |
| peer selection for stem | outbound connections only (a node never stems to a peer that dialled it), so a supernode that dials every node is never a stem relay | the paper's remark on the intersection attack; Bitcoin Core's outbound-only relay form |

The expected number of nodes a transaction crosses before diffusion is 1 over q, ten; the first-spy precision against the paper's adversary with a fraction p of spy nodes is bounded as the paper's theorem 2 states (near p for small p, against near 1 for plain diffusion); the figure for this network is a measurement, not a claim (section 2.4).

### 2.3 The wallet's submission path

The wallet submits a shielded transaction (a) to its own node, by default, which stems it; or (b) to a public node over Tor (the wallet carries a Tor client as Monero's wallets carry one; the wallet's configuration says "over Tor" or "direct" on the home screen); or (c) directly to a public node in the clear only after the wallet has said, in words, "the node at <name> will see your address and this transaction". The public RPC node stems what it receives (section 4), which protects the sender from the rest of the network and not from the RPC node itself; (c) is therefore outside the claim.

The alternative to Dandelion++, stated with its cost: every wallet and node submitting over Tor or I2P only (Monero's optional form). The cost: Tor's latency on every submission, a dependency on the Tor network's availability, and a node fleet whose peering becomes anonymous too, which the first release does not attempt; Dandelion++ costs nothing but the stem delay (ESTIMATE under 10 seconds expected at ten hops) and needs no third network. The first release carries Dandelion++ in the node and Tor as the wallet's option.

### 2.4 The measurement

On the research chain with N nodes of record and k spy nodes dialled into all of them (k from 1 to one quarter of N): for 1,000 transactions submitted from known nodes, the first-spy estimator's precision (the fraction of transactions whose true source the spies name correctly) under plain gossip and under Dandelion++. The published figure is both precisions, N, k and the stem parameters; the claim after measurement is the measured precision, never the paper's bound.

## 3. Wallet sync: what a server learns

### 3.1 The modes, exactly

| mode | what the wallet downloads | what the server learns | in the privacy claim |
|---|---|---|---|
| A. Full scan on the device from its own node | every block's shielded outputs (ciphertexts, commitments, nullifiers, mint records) | nothing beyond what any peer learns of a node: that it exists and syncs | yes |
| B. Full scan on the device from a public node (a server) | the same, by block range from a server | the IP, the block ranges requested, the request times, the wallet's birthday height (its first range), the frequency of sync; NOT which outputs opened, because every output is downloaded | yes, with the leak named: the server can tell "a wallet born at height h syncs from this IP at these times"; it cannot tell which payments are the wallet's |
| C. Compact light mode | per block, every output's full ciphertext (1,088 + about 200 bytes) and nullifiers, with no transaction bodies and no proofs; the wallet decapsulates on the device; NO follow-up fetch of a matched transaction exists in the protocol (everything a wallet needs to spend is in the compact form, including the note's position) | as B; and nothing more, because there is no second request that depends on a match | yes, as B; the follow-up-fetch leak of Zcash's lightwalletd (a wallet that fetches the full transaction after a compact match tells the server which transaction is its own) is designed out by carrying the whole ciphertext |
| D. Viewing-key server | the wallet hands the server its incoming viewing key (dk, the ML-KEM decapsulation key, and nk for spends); the server scans and returns the wallet's notes | every incoming note's amount, memo and sender tag of that wallet; with nk, every spend; the wallet's whole history | no; the home screen says "the server sees this wallet's amounts and payments" (ADR section 8.9) |

The compact form's size: at 1,000 transfers a day, 2,000 outputs of about 1,300 bytes, 2.6 MB a day (ADR section 4.5), plus the mint records; at 10,000 a day, 26 MB; a phone on a 50 Mbit/s link downloads a day's worth in ESTIMATE under 5 seconds and decapsulates 20,000 outputs in ESTIMATE 2 to 6.4 seconds (32 microseconds a ciphertext measured on one server core, docs/money/STAGE1-JOIN-2026-10-18.md section 8; a phone at a factor of 3 to 10, not measured).

### 3.2 The rules for a server and a wallet

1. A wallet never sends a server a nullifier, a commitment, an address or a note; it sends block ranges only. (A wallet that asks "is nullifier X spent?" tells the server which note it holds; the wallet answers that question from the full nullifier set it downloads.)
2. A wallet downloads whole ranges; it never skips a block or filters by anything that depends on its keys.
3. A wallet's first range starts at its birthday height; the wallet pads the start down by a random 0 to 1,440 blocks so the birthday is not exact.
4. The compact form carries, per output, the commitment, the full ciphertext, the position; per block, the nullifiers, the position tags, the anchor, the revoked-set root, the mint records. A wallet proves and spends from this alone, with a Merkle path it builds locally from the commitments (the whole tree is rebuilt on the device from the commitment stream, ESTIMATE 32 H per leaf, or the server supplies a path on request, which leaks the note's position and is refused by rule).
5. A server that returns a filtered set, a wrong anchor or a stale nullifier set is detected: the wallet checks each block's commitment count and anchor against the block header obtained from a second source (any node; the header chain is tiny) and refuses a server whose anchors disagree; the test plan's group J rows.
6. The wallet's own node is mode A; a phone with no node uses mode C from a server of the owner's choosing, the Igneum public one by default, over Tor when the wallet is set to it.

### 3.3 Later options, named and not chosen

Fuzzy message detection (Beck, Len, Miers, Green, "Fuzzy Message Detection", CCS 2021, ePrint 2021/089) and oblivious message retrieval (Liu and Tromer, "Oblivious Message Retrieval", ePrint 2021/1256) let a server find a wallet's outputs without learning which they are, at a cost in server computation and in the privacy of the false-positive rate. Neither is in the first release; the full-ciphertext compact mode makes them unnecessary at the ADR's transfer rates. The light mode for a higher rate is specified in docs/money/LIGHT-SYNC-SPEC-2026-10-18.md: a lattice detection clue in oblivious message retrieval's family tested in the clear at one in sixteen, with fuzzy message detection's curve rejected (a discrete-logarithm break would link every recorded clue retroactively) and oblivious message retrieval proper named as the later option.

### 3.4 The measurement

A server of record logs everything it can (IP, ranges, times) for a set of test wallets with known notes on the research chain; an analyst with the logs and the chain attempts to assign each test note to a wallet. The published figure is the assignment accuracy for modes B and C against chance, and the exact set of fields the server had. For mode D the figure is 100 percent by construction and is published as such.

## 4. The public RPC's logs

The public endpoint (rpc.igneum.network, dev/rpc.md) serves reads and `eth_sendRawTransaction`. Rules for the first release:

1. No access log: the reverse proxy and the node write no request line, no IP, no user agent and no body to disk; the only counters kept are totals per method per minute with no key.
2. Rate limiting keys on B(k_hour, IP) where k_hour is a random 32-byte key generated in memory every hour and never written; the limiter's table is in memory and is dropped with the key.
3. A received shielded transaction is handed to the node's Dandelion++ stem as if the node had made it; the RPC process does not store the transaction's hash beside the request.
4. The configuration of the proxy and the node (the exact files, with secrets removed) is published on the site's reliability pages, and the test plan's row J-07 reads the proxy's log directories on the box of record and finds them empty of request lines.
5. The same rules apply to the compact-block server of section 3 and to the relay (relay.igneum.network).
6. The sentence the site may say once row J-07 has passed and not before: "the public node keeps no log of who asked what; it still sees your IP while you are connected; use your own node or Tor for a connection it cannot see".

The measurement: row J-07 (the logs empty); row J-08 (a packet capture on the box shows no IP leaves the proxy to any third party, such as an analytics or a tracing service).

## 5. The pool's view of its miners

What a pool (pool.igneum.network, or any coordinator on the public recipe) learns: the miner's IP at connection, the miner's key (the share's signed key, POOL-NONCUSTODIAL.md), every share with its time, the hashrate per worker, the worker names the miner sends, the registered payout address (public on the chain anyway, docs/money/PAYOUT-PRIVACY-SPEC-2026-10-18.md), and the Ember version. What it does not learn: anything in the wallet; the miner's balance after the mint; the miner's spends.

Rules for the first release:

1. The pool keeps the journal of record (shares, keys, weights, payouts, POOL-NONCUSTODIAL.md section 3) and no connection log; the IP is held in memory for the session and never written; the rate limiter as section 4's.
2. The pool protocol runs over TLS; a miner may connect through Tor (the pool publishes an onion address) at the cost of latency on share submission (ESTIMATE 0.5 to 2 seconds a round trip, which raises stale shares on a twelve-second block by a measured amount, row J-10).
3. Worker names are chosen by the miner; Ember's default is a random word pair, never the machine's hostname (the console's hostname rule of record applies to the console, not to the pool).
4. A miner who registers a shielded payout address registers one per key; two keys of one person are linkable only if they register the same address, which Ember never does by itself (F-756's per-diversifier item decides whether two addresses of one seed are linkable).
5. The IP-to-key link is the pool's, and it is the same link a pool has today; the claim is only that it stops at the key and never reaches the wallet.

The measurement: row J-09 (the pool's disk after a day of a test miner's session contains no IP), row J-10 (the stale-share rate over Tor against direct).

## 6. The first release's rules, in one table

| surface | rule | measurement | row |
|---|---|---|---|
| relay | Dandelion++ with the parameters of 2.2 on every shielded transaction; wallet to own node or Tor by default | first-spy precision with k spies, before and after | J-01, J-02 |
| wallet sync | modes A, B, C in the claim with the leak named; D outside it with the home-screen sentence; rules 3.2.1 to 3.2.6 | assignment accuracy from server logs per mode | J-03 to J-06 |
| public RPC | no request log, hourly-keyed limiter, stem hand-off, published configuration | logs empty; no third-party egress | J-07, J-08 |
| pool | journal only, no connection log, TLS and Tor, random worker names, one address per key | disk free of IPs; stale rate over Tor | J-09, J-10 |

## 7. What is not claimed

- No figure on this page is measured; the paper's bounds are the paper's, on its graph model, and the network of record is small (the node count of record is the network team's status feed's), so the anonymity a stem gives on it is a measurement.
- Dandelion++ protects against the paper's adversary (spy nodes observing timing); it does not protect against an adversary that watches the wire at the sender's ISP, which Tor addresses and this page does not claim.
- A wallet's IP at its own node is the node's IP; a node at home is the household; the claim stops at the node.
- Modes B and C tell the server when a wallet syncs and from where; a server that sees one IP sync and, a minute later, the network fluff one transaction, has a timing correlation the test plan measures (K-07) and this page does not claim away.
- Timing correlation between a shield, a spend and an unshield on a small pool is the ledger's leak, measured in group K, and no network rule removes it.
- The public side (0x transactions) is relayed as today and is not private.
- The node's devp2p discovery and peering reveal a node's IP to the network by design; running a node is a public act.

## 8. Sources

- docs/money/ADR-0001-PRIVATE-PQ-SAFE-MONEY-2026-10-18.md (section 3's network paragraph; section 4.5's scanning; section 7's server and correlation rows; section 8.9)
- dev/rpc.md (the public endpoint and its proposed limits)
- docs/ecosystem/POOL-NONCUSTODIAL.md (the pool's journal and the key-bound share)
- docs/money/PAYOUT-PRIVACY-SPEC-2026-10-18.md (the registered payout address)
- Dandelion: S. Bojja Venkatakrishnan, G. Fanti, P. Viswanath, "Dandelion: Redesigning the Bitcoin Network for Anonymity", SIGMETRICS 2017, https://arxiv.org/abs/1701.04439
- Dandelion++: G. Fanti, S. Bojja Venkatakrishnan, S. Bhat, M. Lu, B. Niu, P. Viswanath, "Dandelion++: Lightweight Cryptocurrency Networking with Formal Anonymity Guarantees", SIGMETRICS 2018, https://arxiv.org/abs/1805.11060
- Deanonymisation of diffusion: G. Fanti, P. Viswanath, "Deanonymization in the Bitcoin P2P Network", NeurIPS 2017, https://arxiv.org/abs/1707.01798
- Client fingerprinting: A. Biryukov, D. Khovratovich, I. Pustogarov, "Deanonymisation of Clients in Bitcoin P2P Network", CCS 2014, https://arxiv.org/abs/1405.7418
- Bitcoin's Dandelion proposal (BIP 156, draft): https://github.com/bitcoin/bips/blob/master/bip-0156.mediawiki
- Monero's Dandelion++ (monero v0.17, 2020): https://github.com/monero-project/monero/pull/6314
- Zcash light client protocol (ZIP 307, compact blocks) and lightwalletd: https://zips.z.cash/zip-0307 and https://github.com/zcash/lightwalletd
- Fuzzy message detection: G. Beck, J. Len, I. Miers, M. Green, CCS 2021, https://eprint.iacr.org/2021/089
- Oblivious message retrieval: Z. Liu, E. Tromer, https://eprint.iacr.org/2021/1256
- ML-KEM-768 sizes (FIPS 203): https://pkg.go.dev/crypto/mlkem@master
