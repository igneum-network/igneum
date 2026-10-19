# Light sync specification: a server that learns which notes might be yours (Project Money, 18 October 2026)

Status: DESIGN, on paper, for an engineering session to cut from; PANEL-READ 18 October 2026 (docs/money/PANEL-READING-2-2026-10-18.md: PASS with re-cuts F-787 and F-788, applied). It extends docs/money/NETWORK-PRIVACY-SPEC-2026-10-18.md section 3 (the sync modes A to D and their leaks) and its section 3.3, which now points here. The wallet's figures below are cut 3's (docs/money/STAGE1-CUT3-WALLET-2026-10-18.md): an output as carried 1,317 bytes, 32 microseconds a ciphertext on one server core, a phone three to ten times slower (ESTIMATE); the stage-one join (docs/money/STAGE1-JOIN-2026-10-18.md) made the output 1,325 bytes (the 128-bit value), a difference under one percent that moves no figure on this page in its first two digits except the threshold of section 1 item 3, about 9,400 transfers a day at 1,325; the clue's 768 bytes beside the join's 1,293-byte ciphertext is 2,061 bytes, past the node's MaxCiphertext bound of 2,048 (F-774), so the clue gate carries the clue in a field of its own or raises the bound. Nothing here is built or measured. No em dash appears on this page. No price or earning figure appears or can be derived.

## 1. The decision

1. The full scan on the owner's device stays the default and the private mode: modes A and C of the network page (every ciphertext downloaded, tried on the device; no server learns which opened). Nothing on this page weakens them.
2. The light mode is opt-in. The wallet names it, on the screen that offers it and on the home screen while it is on, as "a server learns which notes might be yours". It is outside the launch sentence's privacy claim, as mode D is, and its leak is a measured figure published beside the claim (section 5).
3. The light mode is offered only when the full scan of one day of the chain exceeds 25 MB of ciphertexts, about 9,500 transfers a day (section 4.2). At the ADR's first figure, 1,000 transfers a day, the full scan is 2.6 MB a day and the light mode is never shown.
4. The construction (section 2) is a lattice detection clue carried on every output, tested in the clear by a server that holds the wallet's detection key, with a false-positive rate fixed by the protocol at one in sixteen for every wallet alike. The curve-based published scheme (fuzzy message detection) is not used, because a discrete-logarithm break would later link every recorded clue to its recipient; the lattice published scheme (oblivious message retrieval) is the stronger and later option, named in section 2.5, whose server work is a thousand times this page's.
5. The first release ships no light mode (section 5). The address format carries the detection key from the first release so that light mode can arrive later at a gated height with one output shape for everyone, never two.

## 2. The construction

### 2.1 The published schemes, read for this page

Both ePrint PDFs sit behind a bot check on the day of writing; the abstracts, the authors' implementation pages and Penumbra's write-up of the first scheme were read. Figures from the papers' bodies are marked APPROXIMATE and come from memory.

**Fuzzy message detection** (Beck, Len, Miers, Green, "Fuzzy Message Detection", CCS 2021, ePrint 2021/089). A recipient derives from its key a detection key that a server uses to test each message's flag; the recipient's own messages always pass, and every other message passes with a chosen false-positive rate p, so the server learns a set that contains the recipient's messages among p of everyone else's. The constructions (FMD1, FMD2 and the polynomial form, APPROXIMATE from memory as to their names) all live in a prime-order group under the decisional Diffie-Hellman assumption: the public clue key is a list of group elements X_i = [x_i]B, a flag is one group element, one scalar and n bits (about 64 bytes and the bits, so the flag is small), the detection key is the first n of the scalars x_i and the rate is 2^-n; testing one flag against one key is n + 2 scalar multiplications (Penumbra's protocol page, which deploys the sender-chosen form S-FMD2 on its curve). What a QC adversary does to it: every x_i is recoverable from the X_i in a published address, and with every wallet's full detection key the adversary tests every recorded flag at the smallest rate the flags carry, retroactively, for the whole history. That is the retroactive linking the ADR rejected curve note encryption for (section 4.2 (b)), so the scheme is out for this wallet whatever its merits.

**Oblivious message retrieval** (Liu, Tromer, "Oblivious Message Retrieval", ePrint 2021/1256, revised April 2022; published at CRYPTO 2022, from memory). A sender attaches to each message a clue: a lattice (PVW, a learning-with-errors) encryption of a few bits under the recipient's clue key. A server holds a detection key that is the recipient's clue secret encrypted under a fully homomorphic scheme (BFV), decrypts every clue homomorphically, and folds the pertinent messages into an encrypted digest the recipient decodes; the server learns nothing at all, not even a fuzzy set. The implementation page's figures (github.com/ZeyuThomasLiu/ObliviousMessageRetrieval): about 0.1 to 0.15 seconds of server work per message per registered key on one cloud core, a detection key of about 129 MB per recipient, a digest of about 560 KB for 2^19 messages, the recipient's decoding about 20 ms; the follow-up PerfOMR (Liu, Tromer, Wang, ePrint 2024/204) moves the clue to ring learning-with-errors, cuts the clue key from about 132 kB to about 0.56 kB, the clue from about 1 kB to about 0.6 kB, and the server work to about 40 ms per message. Every assumption is a lattice one, the same family as the wallet's ML-KEM. The scheme fits a PQ wallet; its server work is the obstacle (section 2.5).

**Which fits.** Oblivious message retrieval's family. The construction of record below is its clue (a lattice encryption of a few bits that a wrong key decrypts to random bits) tested in the clear by a server that holds the clue secret, which gives exactly fuzzy message detection's leak profile without its curve. No paper publishes this combination as a scheme; the argument is one paragraph (section 2.3) and the review reads it before anything is cut. If the panel finds a published lattice fuzzy detector with a smaller clue, it replaces section 2.2 and nothing else on this page moves.

### 2.2 The keys, the clue and the test, exactly

Notation as cut 3: B is BLAKE3 in derive_key mode; s is the wallet's 32-byte seed; K-PKE is the public-key encryption inside ML-KEM (FIPS 203 section 5: K-PKE.KeyGen, K-PKE.Encrypt, K-PKE.Decrypt), used here without the KEM's Fujisaki-Okamoto transform (no re-encryption check, fresh randomness), at ML-KEM-512's parameter set (k = 2, n = 256, q = 3,329).

```text
det_seed        = B("Igneum Money stage 1 detect", s)[0..32]                  32 bytes
(ek_det, s_det) = K-PKE.KeyGen(det_seed) at the ML-KEM-512 parameters          800 and 768 bytes
address v2      = (pk, ek, ek_det, d)                                          2,027 bytes (cut 3's 1,227 and 800)

sender, per output:
  m    = 32 bytes: bits 0 to 3 set to 1, bits 4 to 255 fresh random
  r    = 32 fresh random bytes, independent of the note's randomness and of ct_kem's
  clue = K-PKE.Encrypt(ek_det, m, r)                                           768 bytes
  output as carried = cm (32) || ct_kem (1,088) || body (197) || clue (768)   2,085 bytes

server, per output per registered key:
  m'   = K-PKE.Decrypt(s_det, clue)
  candidate if and only if bits 0 to 3 of m' are all 1
```

The detection key given to the server is s_det alone (768 bytes), never det_seed and never the wallet's seed; s_det is regenerated on the device from det_seed when the wallet registers with a new server. The rate t = 4 bits, one in sixteen, is a protocol constant: every sender writes the same four positions, every server tests the same four positions, and no wallet has a rate of its own (section 3.4 says why). The compact stream of mode C omits the clue, so a full-scan wallet's download stays 1,317 bytes an output; the clue is in the transaction, under the block hash, kept by light servers and archive nodes, and droppable by the minimum node once the block is settled, because verification never reads it.

Why ML-KEM-512's parameters and not 768's: a break of the detection pair gives the adversary only the one-in-sixteen test (section 2.3), not the note, so the pair carries less than the note's key does; the saving is 320 bytes an output and 384 an address. The panel may take 768's set (clue 1,088 bytes, ek_det 1,184) for one parameter set across the wallet; the arithmetic of section 4 is given for 512's and the change is a constant.

The encryption call is a verified implementation's K-PKE exposed behind a feature (libcrux-ml-kem's internal IND-CPA module, or another verified source), never hand-rolled; this is the engineering session's first item, because cut 3's crate uses the KEM interface only.

### 2.3 Why the test is fuzzy and the clue says nothing to anyone else

1. The recipient's key always passes: K-PKE at these parameters decrypts correctly except with probability about 2^-139 (FIPS 203's table for ML-KEM-512), so a false negative has that probability per note and is treated as zero.
2. A wrong key passes with probability one in sixteen, up to a term of order t/q: decryption computes v minus the inner product of s' with u and rounds each coefficient to a bit; for a key s' other than the one the clue was made for, that difference is a pseudorandom ring element under module learning-with-errors, so each of the 256 decoded bits is uniform to within 1/q and the four tested bits are all 1 with probability 2^-4 up to that term. The server cannot do better, because the other 252 positions are the sender's random bits and there is no re-encryption check to run: this is the one property the KEM form lacks (a holder of an ML-KEM decapsulation key sees its own re-encryption check succeed or fail, which makes detection exact, which is why the detection clue is a plain K-PKE ciphertext and not a second KEM ciphertext).
3. The clue names no recipient: a K-PKE ciphertext is pseudorandom under module learning-with-errors for whoever lacks the secret, including the pair's own public matrix, so an observer with every address on the chain cannot tell which ek_det a clue was made for. The result relied on, named by the review's reading of 18 October 2026 and to be confirmed against the source by the engineering session: the Kyber IND-CPA proof (Bos, Ducas, Kiltz, Lepoint, Lyubashevsky, Schanck, Schwabe, Seiler, Stehle, "CRYSTALS-Kyber", EuroS&P 2018, ePrint 2017/634, the IND-CPA theorem), whose two-step argument replaces the public key's t and then the ciphertext (u, v) by uniform under module learning-with-errors, so a ciphertext is indistinguishable from uniform even to a holder of every public key, which is key privacy for the PKE directly; the KEM anonymity papers (Grubbs, Maram, Paterson, ePrint 2021/708; Maram, Xagawa, ePrint 2022/1696) cover the Fujisaki-Okamoto form and are not needed for a plain K-PKE clue.
4. Under a lattice break the detection secret of every address is recoverable from the public ek_det, and the adversary then runs the test of section 2.2 on every recorded clue: the leak is the one-in-sixteen set, retroactively, for every wallet, no more. The note ciphertext under the same break leaks every amount and recipient (ADR section 4.4), so the clue adds nothing to the break's damage.
5. A sender who colludes with the server can mark the notes it sends (a fixed pattern in the 252 random bits) so the server recognises them exactly; the server then learns that those notes are the wallet's, which the sender already knew. A sender who sends many notes to one wallet raises that wallet's candidate count (section 3.3).

### 2.4 The protocol between wallet and server

1. Register: the wallet sends s_det and a start height, padded down from its birthday by a random 0 to 1,440 blocks (the network page's rule 3.2.3), over the same channel rules as mode C (the owner's chosen server, Tor when set).
2. Scan: the server tests every output from the start height forward, once per registered key, and keeps for each key the list of candidate positions (the output's leaf index in the commitment tree).
3. Fetch: the wallet asks for its candidates by block range (never by position, never by anything that depends on which candidates opened). The server returns, per candidate, the position and the 1,317-byte compact output; and, for the range, the whole commitment stream (32 bytes an output) and the whole nullifier stream (32 bytes a spend), exactly as mode C carries them, plus the block headers' anchors.
4. Check: for each candidate the wallet confirms that the commitment carried equals the commitment at that position in the stream; a candidate that fails is a fabrication and the server is refused. The wallet trial-decrypts each candidate with its own decapsulation key, as cut 3's scanner does, and keeps the notes that open and whose commitment recomputes.
5. Tree and spends: the wallet rebuilds the append-only tree's frontier from the commitment stream on the device and keeps a witness per owned note (the incremental form: a few hashes a leaf), so a spend never asks a server for a path; and it marks spent notes from the nullifier stream. Rule 3.2.1 of the network page holds unchanged: no nullifier, commitment, address or note is ever sent to a server.
6. Withholding check: section 3.5.

A named optimisation for the engineering session, allowed only under its stated condition: in place of the whole commitment stream the wallet may fetch a fixed public layer of the tree (every node at level 10, one per 1,024 leaves) plus the leaves of the current incomplete 1,024-leaf block plus, per candidate, the ten lowest siblings, since the server already knows the candidate set; the condition is that every request depends only on the candidate set, the block range and protocol constants, never on which candidates opened. The figures of section 4 use the whole stream, the larger and simpler form.

### 2.5 The later option

Oblivious message retrieval proper (PerfOMR's form) gives the server nothing: no candidate set, no rate. Its clue is of the same order as this page's (about 0.6 kB), so the chain's cost is the same; its server work is about 40 ms per output per registered key against this page's bound of 27 microseconds (section 4.1), about 1,500 times more, and its detection key is of the order of 100 MB a wallet against 768 bytes (the first paper's figure; PerfOMR's is not on its abstract page and is marked unknown). It is the path when a server must learn nothing and the servers can carry that work; the clue field of section 2.2 is replaceable by its clue at a gated height with one output shape as before.

## 3. The leak, exactly

### 3.1 What the server learns

With N the outputs in a day (two a transfer), p = 1/16 and M the wallet's own incoming notes that day, the server's candidate set for one key has expected size pN + M(1 - p), with standard deviation about the square root of pN. The server learns:

- the candidate set itself: a list of note positions of which about pN are other people's and M are its own, and it cannot tell which are which (section 2.3);
- that this wallet uses light mode, from which server, when it syncs and from which IP (mode C's leak, unchanged);
- the padded start height (the birthday to within 1,440 blocks);
- M's excess over pN when M is large: a wallet receiving many notes a day stands out above the noise of the false positives (section 3.3);
- across many registered wallets, for each output, which registered keys pass: the true recipient if it is registered, plus about one sixteenth of the others; a note that passes for no registered key is known to belong to no registered light wallet (section 3.3).

### 3.2 The anonymity set per wallet per day

The false positives among which a wallet's true notes hide, pN, at the three chain rates and at the rates the panel could set. The row of record is t = 4.

| t (bits) | rate p | 1,000 transfers a day (N = 2,000) | 30,000 a day (N = 60,000) | 100,000 a day (N = 200,000) |
|---|---|---|---|---|
| 4 | 1/16 | 125 | 3,750 | 12,500 |
| 6 | 1/64 | 31 | 938 | 3,125 |
| 8 | 1/256 | 8 | 234 | 781 |
| 12 | 1/4,096 | 0.5 | 15 | 49 |

Read: at 30,000 transfers a day and one in sixteen, a server that picks one candidate of a wallet's day at random names one of that wallet's notes with probability about 1 in 3,751 for a wallet with one note that day. At 1,000 a day the sets are small at every rate and the light mode is not offered. The figure of record for the screen (section 5): "about 1 in 16 of every note on the chain, and all of yours among them".

### 3.3 The per-note view, and the two ways the set shrinks

For one note, the light wallets it could belong to are the registered keys that pass: the true one, if registered, plus about (W - 1)/16 of the other W - 1 registered wallets; and every full-scan wallet, which the server never tests and can never exclude. With W = 16 light wallets on a server the passing set is about two; with 160 about eleven; with 1,600 about 101. A small network's light-mode population is itself a small anonymity set, and that is one reason the first release ships without the mode.

The set shrinks two ways the construction does not stop: a wallet that receives many notes a day (M above about two standard deviations of pN: at 30,000 a day about 120 notes a day) is visible as a heavy receiver, though not which notes are its; and a sender who floods a wallet raises its M on purpose (Penumbra's note on manipulating M, the flashlight form). Both are measured in section 5.3 and said in the wallet's words, not hidden.

### 3.4 Why one rate for everyone and no decoys

A per-wallet rate (a longer or shorter tag by the recipient's choice, as the published scheme allows) would be known to the server from the candidate count and would fingerprint the wallet; so the tag length is the protocol's. Decoys added by the server at the wallet's request would raise the apparent set against an honest server only: a server that chose the decoys knows them, and a server whose logs are taken knows them from the logs; so the figure of record is the one the construction gives against any server, the protocol's one in sixteen, and the wallet requests no decoys. A wallet wanting a smaller leak uses the full scan.

### 3.5 What the server never learns, and what a malicious one can do

Never, by construction: amounts, memos, senders (no output names its sender), which candidate is real, the nullifier key (so no spend is ever attributed), the decapsulation key, the spend secret, the viewing key, the balance, and the wallet's address (s_det does not contain ek_det, and the server is never sent the address).

A malicious server can lie only by omission: it cannot fabricate a candidate (the commitment check of 2.4 item 4) and cannot forge an anchor or hide a nullifier without being caught by the header check of the network page's rule 3.2.5. It can withhold a true note by leaving it out of the candidate list, which the wallet detects by two means, both in the first cut of the mode:

1. A second server, when one exists: the test is deterministic in s_det and the clue, so two honest servers return identical candidate sets for the same range; any difference proves one lied, the wallet refuses both until a full scan of that range (mode C from any node) settles it, and the full scan is the arbiter. The wallet registers with two servers by default when two are offered, the Igneum public one and one of the owner's choosing.
2. A random full-scan check, always: once in every 30 days, chosen by the wallet at random, the wallet fetches one whole day in mode C from a node other than its light server and compares the notes found with the light result for that day; a note the light server omitted marks the server as lying and the wallet refuses it. The server cannot tell which days are checked, because the check runs against a different source.

A withheld note is money the wallet does not yet see, never money lost: the words restore it by a full scan, exactly as cut 3's restore does.

## 4. The costs

### 4.1 The server's work

One K-PKE decryption at ML-KEM-512's parameters, a four-bit comparison, per output per registered key. The decryption is the inner part of a decapsulation; cut 3 measured one ML-KEM-768 decapsulation at 27 microseconds on one EPYC core, which bounds the decryption at the smaller parameter set from above (ESTIMATE under 15 microseconds; the bound 27 is used below).

| transfers a day | outputs a day | server work a day per registered wallet | a year from the birthday, per registration (outputs times 27 us) | 10,000 registered wallets, a day |
|---|---|---|---|---|
| 1,000 | 2,000 | 54 ms | 730,000 outputs: 20 s | 9 core-minutes |
| 30,000 | 60,000 | 1.6 s | 21,900,000 outputs: 9.9 core-minutes | 4.5 core-hours |
| 100,000 | 200,000 | 5.4 s | 73,000,000 outputs: 33 core-minutes | 15 core-hours |

A registration from a birthday a year back is the year column once; the daily column thereafter. There is no sharing of work between keys: every key's decryption is its own.

### 4.2 The wallet's download per day

The full scan's download is N times 1,317 bytes. The light mode's is the fixed streams (commitments and nullifiers, 128 bytes a transfer: two of each at 32 bytes) plus the candidates at 1,317 bytes each (pN plus M; M taken as small). Shown at the rates of section 3.2; the row of record is one in sixteen.

| transfers a day | full scan a day | fixed streams a day | candidates a day at 1/16 | light total at 1/16 | at 1/64 | at 1/256 |
|---|---|---|---|---|---|---|
| 1,000 | 2.63 MB | 0.13 MB | 125 notes, 0.16 MB | 0.29 MB | 0.17 MB | 0.14 MB |
| 30,000 | 79.0 MB | 3.84 MB | 3,750 notes, 4.94 MB | 8.8 MB | 5.1 MB | 4.2 MB |
| 100,000 | 263 MB | 12.8 MB | 12,500 notes, 16.5 MB | 29.3 MB | 16.9 MB | 13.8 MB |

The arithmetic for the row of record at 30,000 a day: 60,000 outputs; 60,000 / 16 = 3,750 candidates; 3,750 times 1,317 = 4,938,750 bytes; the streams 30,000 times 128 = 3,840,000 bytes; together 8,778,750 bytes, 8.8 MB a day, one ninth of the full scan's 79.0 MB. At 100,000 a day the light mode is also one ninth of the full scan. The fixed streams are the floor below which no rate goes without the optimisation of section 2.4, which would cut them to a few hundred kilobytes a day at every rate.

The threshold of section 1 item 3: the full scan's day exceeds 25 MB when N times 1,317 exceeds 25,000,000, that is above 18,983 outputs, about 9,500 transfers a day. The wallet computes it from the chain's own count of the last day's outputs and shows the actual full-scan figure beside the choice.

### 4.3 A restore from the words, a year back

| transfers a day | full scan, a year (cut 3) | light mode, a year at 1/16: candidates plus streams |
|---|---|---|
| 1,000 | 0.94 GB and 23 s of one server core | 45,625 notes, 60 MB, plus 47 MB of streams: 0.11 GB |
| 30,000 | 28.2 GB and 12 min | 1,368,750 notes, 1.80 GB, plus 1.40 GB: 3.2 GB |
| 100,000 | 94 GB and 39 min | 4,562,500 notes, 6.0 GB, plus 4.67 GB: 10.7 GB |

The server's side of a restore is the year column of 4.1. The wallet's birthday is the start of the server's scan, padded down (2.4 item 1); a wallet born a day ago costs the server a day of outputs, a wallet restored from old words the whole span, and the server learns the span to within 1,440 blocks either way.

### 4.4 The phone's work

Trial decryption of the candidates only, at cut 3's phone estimate of 100 to 320 microseconds a ciphertext: at one in sixteen, 0.01 to 0.04 s a day at 1,000 transfers a day, 0.4 to 1.2 s at 30,000, 1.3 to 4.0 s at 100,000. The tree's frontier from the commitment stream: about two evaluations of the in-circuit hash a leaf outside the circuit (ESTIMATE 2 to 5 microseconds each on a phone), 0.3 s a day at 30,000 transfers a day and 1 s at 100,000. Memory: the frontier (32 hashes), one witness (32 hashes) per owned note, the day's nullifier set while it is being matched; nothing proportional to the chain. The download is the phone's cost at every rate, as cut 3 found for the full scan.

### 4.5 The chain's cost, paid by everyone

The clue is 768 bytes on every output whether or not any wallet uses light mode: 1,536 bytes a transfer, 0.56 GB a year at 1,000 transfers a day, 5.6 GB at 10,000, 16.8 GB at 30,000, 56 GB at 100,000, on every node that keeps clues (light servers and archives; the minimum node may drop them after settlement, section 2.2). The ADR's 2.75 KB a transfer of state kept after proofs are pruned becomes 4.29 KB for a node that keeps clues. The address grows from 1,227 to 2,027 bytes, which in cut 3's hex text form is 4,070 characters, past a QR code's alphanumeric capacity; the engineering session gives the address a binary QR form (2,027 bytes and the checksum fit a version 40 code at its lowest error level, whose capacity is 2,953 bytes) or a denser text form. This is the finding of the page, said plainly: a PQ detection clue is of the order of a kilobyte an output in every form known to this page (this page's 768, PerfOMR's about 600, the first OMR's about 1,000), where the curve form's is about 70 bytes; the kilobyte is what the PQ requirement adds, and the panel decides whether the chain carries it from the landing check.

## 5. The rules for the first release

1. Ships: modes A and C (the full scan on the device, from the owner's node or from a compact server). Does not ship: the light mode's server, the light mode's screen, the clue on outputs. The address format v2 of section 2.2 ships from the first release, so every address can receive clues from the landing check on and no address's age partitions the set.
2. The gate: the clue joins the output shape at a gated height (IgneumMoneyClueFrom, zero is off, on the research chain first), when the chain's rate first makes the full scan of a day exceed 25 MB or when the panel sets it earlier; from the landing check every output carries a clue, with m's random fill for senders who choose nothing (there is nothing to choose: every address has ek_det). Before the landing check no output carries one. The shape changes once, for everyone, at one height; never two shapes side by side.
3. The screen that offers the mode, in the honesty rules' form (ADR section 8; this is rule 10, proposed): "Light sync. A server learns which notes might be yours: about 1 in 16 of every note on the chain, and all of yours among them. It does not see amounts, senders, memos or spends, and it cannot spend. It sees when and from where this wallet syncs. Full sync on this device shares nothing with a server." Two buttons, "Keep full sync" first and preselected, "Use light sync" second. The home screen line while the mode is on: "Light sync: a server learns which notes might be yours". The words "safe", "private", "anonymous" and "trustless" never appear beside the mode; the launch sentence's privacy claim is stated as not covering it, in the same place mode D's exclusion is stated.
4. The wallet shows the two downloads side by side on the offering screen, the full scan's actual figure for the last day and the light mode's, computed from the chain's count, so the choice is made on the real number.
5. The rate is one in sixteen, the protocol's constant; the wallet offers no rate and no decoys (section 3.4).
6. Two servers by default when two exist, and the random full-scan check once in 30 days always (section 3.5); a server caught lying is refused and named on screen as "this server withheld a payment".
7. The measurement that proves the leak figure, before the mode is offered on any chain of record (the test plan's group J grows by the rows of section 6.2): a server of record logging everything it can, ten test wallets with known notes on the research chain at a synthetic load of 30,000 transfers a day for three days; the published figures are the observed false-positive rate per key against 1/16 with its binomial interval, the analyst's assignment accuracy (for each true note, the probability the analyst's best guess is right, expected about 1 in 3,751 per candidate at that load), the heavy-receiver threshold (the M at which a wallet is distinguishable at two standard deviations), and zero false negatives. The figure on the screen is the measured one, re-cut if it differs from 1 in 16.

## 6. The tests

### 6.1 Unit and vector tests, in the wallet crate

1. `detect_keys_from_seed`: det_seed, ek_det and s_det from cut 3's two fixed seeds, added to tests/vectors.json; the address v2's bytes and checksum.
2. `clue_round_trip`: a clue made for ek_det passes the test under s_det, one million times with fresh randomness, zero failures.
3. `clue_false_positive_rate`: one million clues made for a stranger's key tested under s_det: the passing fraction within three standard deviations of 1/16 (the binomial at a million trials gives a standard deviation of 0.00024 on 0.0625).
4. `clue_bits_are_fresh`: two clues to the same key differ in all of ct and in the random fill; the test positions are the only fixed bits.
5. `clue_no_exact_test`: a test that any attempt to use more than the four positions is impossible by construction is not a test; instead `clue_fill_is_uniform` checks the decoded 252 fill bits under the stranger's key for uniformity (a chi-square at the usual bound, a statistical check named as such).
6. `candidate_commitment_check`: a candidate whose commitment does not match the stream at its position is refused and the server marked.
7. `light_restore_equals_full`: the same block stream scanned fully and scanned as candidates plus streams gives byte-identical notes, the same spent set and the same two balances, including a spent note and a note received after a spend.
8. `light_withholding_detected`: a server fixture that omits one true note is caught by the 30-day check and by the two-server comparison; a server that omits a false positive is not caught (and need not be: the test states this).
9. `server_never_sent`: the registration carries s_det and a start height and nothing else; a test asserts the bytes.
10. `screen_words`: the offering screen and the home line carry the sentences of section 5.3 verbatim and none of the four forbidden words.
11. `address_v2_qr`: the binary form fits the stated code capacity.

### 6.2 Rows for the test plan (group J, continued)

| id | name | setup | action | expected | evidence |
|---|---|---|---|---|---|
| J-11 | the leak figure | the research chain at 30,000 transfers a day synthetic for three days; ten test wallets with known notes; a server logging everything | the analyst assigns notes to wallets from the logs and the chain | the false-positive rate per key within its interval of 1/16; the assignment accuracy published against 1 in (pN + M); zero false negatives | J-11/assignment.json |
| J-12 | the heavy receiver | the same; one wallet fed 50, 120, 300 notes a day | the analyst ranks wallets by candidate count | the M at which the fed wallet is distinguishable at two standard deviations, published | J-12/ |
| J-13 | withholding | a server that omits one true note for one wallet | the wallet syncs for 30 days against it, with and without a second server | caught by the random check within the window; caught at once with a second server; the server named on screen | J-13/ |
| J-14 | fabrication | a server that returns a candidate not at the stated position | the wallet checks it against the commitment stream | refused; the server marked | J-14/ |
| J-15 | the clue's anonymity in practice | a thousand clues to each of ten keys, shuffled | an analyst with every ek_det but no s_det assigns clues to keys | accuracy at chance, published as a statistical check and not a proof | J-15/ |
| J-16 | the server's work | the server of record on one core | the microseconds per output per key over a day of 60,000 outputs, ten registered keys | the figure beside this page's 27-microsecond bound | J-16/bench.json |
| J-17 | the phone's day | a phone of record in light mode at 30,000 transfers a day | one day's sync | the download in bytes beside 8.8 MB; the seconds of work | J-17/ |
| J-18 | the gate | the research chain with IgneumMoneyClueFrom set | outputs before and after the gate | before: no clue, a clue refused; after: every output carries one, an output without one refused; one shape on each side | J-18/ |

## 7. What is not claimed

- No figure on this page is measured; the server's bound is cut 3's decapsulation figure, the phone's an estimate, the rate's exactness an argument the review reads.
- The construction is this page's combination, not a published scheme; the anonymity of the clue rests on a result cited approximately until the review names the exact one.
- The anonymity set is the chain's rate times one sixteenth, and a small chain has a small set; the light-mode population on a server is a second, smaller set; neither is manufactured by the construction.
- The light mode is outside the launch sentence's privacy claim; its own figure is published beside it after J-11.
- Timing, IP and birthday leaks are mode C's and are not reduced here; the network page's section 7 applies in full.
- The kilobyte an output is paid by every node that keeps clues from the landing check on, whether or not anyone uses the mode.

## 8. Sources

- docs/money/ADR-0001-PRIVATE-PQ-SAFE-MONEY-2026-10-18.md (section 4.3 item 6 the note encryption; 4.4 the lattice-break row; 4.5 scanning; 4.7 the output-shape rule; section 8 the honesty rules)
- docs/money/NETWORK-PRIVACY-SPEC-2026-10-18.md (section 3 the modes A to D, rules 3.2.1 to 3.2.6, section 3.3 the two schemes named, section 3.4 the measurement)
- docs/money/STAGE1-CUT3-WALLET-2026-10-18.md and wallet/money (the keys, the 1,317-byte output, 32 and 27 microseconds, the year table, the phone estimate)
- docs/money/TEST-PLAN-2026-10-18.md (group J's form)
- Fuzzy message detection: G. Beck, J. Len, I. Miers, M. Green, "Fuzzy Message Detection", CCS 2021, https://eprint.iacr.org/2021/089 (abstract read; the body's figures from memory, APPROXIMATE)
- Penumbra's write-up and deployment of S-FMD2: https://protocol.penumbra.zone/main/crypto/fmd.html, .../fmd/construction.html, .../fmd/considerations.html (read: the construction's formulas, the 448-byte clue key at precision 14 in a 256-bit group, the expected detections formula E[D] = pN + M(1 - p), the manipulation of N and M)
- Oblivious message retrieval: Z. Liu, E. Tromer, "Oblivious Message Retrieval", ePrint 2021/1256 (abstract read: a few bits a message in the digest, decoding about 20 ms); the implementation and its figures https://github.com/ZeyuThomasLiu/ObliviousMessageRetrieval (read: 0.145 s a message on one core with HEXL, the 129 MB detection key, the 560 KB digest at 2^19 messages)
- PerfOMR: Z. Liu, E. Tromer, Y. Wang, "PerfOMR: Oblivious Message Retrieval with Reduced Communication and Computation", ePrint 2024/204 (abstract read: about 40 ms a message, the clue key 235 times smaller than about 132 kB, the clue 1.6 times smaller than about 1 kB)
- FIPS 203 (ML-KEM): K-PKE in section 5, the parameter sets and the decryption failure rates in table 2, the sizes in table 3; https://csrc.nist.gov/pubs/fips/203/final
- Anonymity of Kyber's encryption and KEM: P. Grubbs, V. Maram, K. Paterson, "Anonymous, Robust PQ Public Key Encryption", ePrint 2021/708; V. Maram, K. Xagawa, "PQ Anonymity of Kyber", ePrint 2022/1696 (both from memory, APPROXIMATE as to scope)
- Zcash's light client protocol and its follow-up-fetch leak, for contrast: ZIP 307 https://zips.z.cash/zip-0307 and lightwalletd
- libcrux-ml-kem 0.0.11 (cut 3's pinned implementation; its IND-CPA module is internal on the day of writing)
