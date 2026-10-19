# Money stage one, cut 3: the wallet side of the pool as a library and a command-line tool (18 October 2026)

SUPERSEDED by docs/money/STAGE1-JOIN-2026-10-18.md (the review's reading of 18 October 2026, F-773): H is Poseidon2 over Goldilocks, not the BLAKE3 stand-in, and every vector was re-cut; a note's value is 128 bits as four 32-bit limbs (this page's caveat 7, answered), so the plaintext is version 2 at 189 bytes, the sealed body 205, the output ciphertext 1,293 and the output as carried 1,325 bytes; the Merkle path and the anchor come from the node (igneum_poolPath, igneum_poolBlock) and the wallet file carries the seed's birthday. The key derivation, the note encryption, the scanning figures and the backup words below stand.

**PASS.** Every test of the cut passes on builder seven (wallet/money: 15 library tests, the fixed-vector test, the end-to-end tool test; clippy clean), on the branch lead/money-stage1-wallet. The construction is ADR-0001 section 4.3 (docs/money/ADR-0001-PRIVATE-PQ-SAFE-MONEY-2026-10-18.md); the verdict of its section 4.6, "Money remains research", is unchanged by this page, which is one of the measured rows it asks for. No privacy or PQ claim is made here.

What this cut is: the wallet's half of the shielded pool with nothing of the node in it. Keys from one seed; an address; a note sealed to an address; scanning a stream of blocks with a viewing key; backup words and restore; the spend statement's witness for the prover. What it is not: the proof (stage one, cut 1, the prover), the pool's rules in the node (cut 2), the diversified address (F-756, OPEN), the disclosure receipt (stage 4).

Where: `wallet/money/` in the wallet's Rust workspace (the wallet is Rust, three processes and a core; this is a fourth library crate `igneum-wallet-money` with the binary `igneum-money`). Dependencies: `libcrux-ml-kem` 0.0.11 (ML-KEM-768, FIPS 203, Cryspen's formally verified implementation, the one lattice primitive, never hand-rolled; the AVX2 path chosen at run time on x86, NEON on arm), and the lines the workspace already pins: `blake3`, `chacha20poly1305`, `bip39`, `zeroize`, `rand_core`, `clap`, `serde`. Built and tested only on builder seven.

## 1. The key derivation

B is BLAKE3 in derive_key mode with the context strings below verbatim; H is the in-circuit hash (section 4 of this page says what it is in this build). s is 32 bytes from the operating system's randomness (getrandom through `rand_core::OsRng`).

```text
sk_spend   = B("Igneum Money stage 1 spend",     s)          32 bytes    the spend authority: a preimage
nk         = B("Igneum Money stage 1 nullifier", s)          32 bytes    the nullifier key
ivk        = B("Igneum Money stage 1 view",      s)          32 bytes    the outgoing-record key
kem_seed   = B("Igneum Money stage 1 kem",       s)[0..64]   64 bytes    d || z of FIPS 203 algorithm 19
(ek, dk)   = ML-KEM-768.KeyGen_internal(kem_seed)            1,184 and 2,400 bytes
pk         = H(sk_spend, nk)                                 32 bytes    the recipient key commitment
address    = (pk, ek, d), d = 11 zero bytes                  1,227 bytes
```

The full viewing key is (pk, nk, kem_seed, ivk), serialised with a version byte as 161 bytes. It opens every incoming note (dk regenerates from kem_seed), computes every nullifier (nk) and reads the wallet's own outgoing records (ivk); it cannot spend, because sk_spend is derived from s alone and pk hides it. The tool writes it only to a file the person names, at mode 0600, with the sentence that it reads every amount and payment of the wallet.

The ADR's item 1 names the same four derivations from s. This cut gives ivk the one role the ADR's item 6 leaves for a key "derived from its own seed": sealing the sender's own copy of each output (`OutgoingRecord`, ChaCha20-Poly1305 under B("Igneum Money stage 1 outgoing record key", ivk || cm) with cm as the associated data), so a disclosure receipt (stage 4) can be produced from the seed alone. If the review reads ivk otherwise, the name moves and nothing else does.

The diversifier d (F-756, OPEN): carried so the address has its final shape, written into the note plaintext, used for nothing else. Two addresses under one seed differ only in d and are linkable by inspection, as the ADR's section 4.4 says; no unlinkability between one holder's addresses is claimed, and the tool issues one address (d = 0).

The address's text form: `money1` then hex of (version 0x01, pk, ek, d, the first four bytes of B("Igneum Money stage 1 address checksum", version || pk || ek || d)): 2,470 characters, a QR code or a link, never typed. A wrong character fails the checksum, and the encapsulation key is checked by FIPS 203's modulus test on decode.

Fixed vectors: `wallet/money/tests/vectors.json`, two seeds that are byte patterns (00 01 .. 1f and ff × 32), not wallets, with sk_spend, nk, ivk, pk, the hash and first 32 bytes of ek, the address's hash and text prefix, the 24 words, a note sealed with fixed randomness (its commitment and the hash of the whole 1,317-byte output) and the nullifier at position 7. `tests/vectors.rs` re-derives every one of them. The vectors are the stand-in hash's (section 4) and re-cut when the prover's instance lands.

## 2. The note and its encryption

```text
(ct_kem, K) = ML-KEM-768.Encaps(ek_recipient; 32 fresh random bytes)                  ct_kem 1,088 bytes
key         = B("Igneum Money stage 1 note key", K || ct_kem)                           32 bytes
plaintext   = 0x01 || v (8, LE) || r (32) || d (11) || memo_len (1) || memo padded to 128     181 bytes
body        = ChaCha20-Poly1305(key, nonce = 12 zero bytes, aad = cm, plaintext)           197 bytes
cm          = H(v, pk, r)
output      = cm (32) || ct_kem (1,088) || body (197)                                      1,317 bytes
```

One key seals one message, so the nonce is fixed. ct_kem is bound into the key and cm into the associated data, so a body belongs to exactly one KEM ciphertext and one commitment. Every output is the same length whatever the memo (one output shape, ADR section 4.7's fingerprinting note). The recipient opens the body, recomputes cm from (v, its own pk, r) and keeps the note only when it equals the commitment carried; a body that opened by a forgery would still be refused. The value is 64 bits wei-scaled as the ADR's item 2 says; r is 256 bits from the operating system's randomness.

The sizes, every one asserted by a test (`igneum-money sizes` prints them):

| item | bytes | basis |
|---|---|---|
| seed, sk_spend, nk, ivk, pk, cm, nullifier | 32 each | the construction |
| ML-KEM-768 encapsulation key ek | 1,184 | FIPS 203 table 3, libcrux's type |
| ML-KEM-768 decapsulation key dk | 2,400 | FIPS 203; regenerated from kem_seed, never stored |
| ML-KEM-768 ciphertext | 1,088 | FIPS 203 |
| address (pk, ek, d) | 1,227 | the ADR's figure, exact |
| note plaintext | 181 | the layout above |
| sealed body | 197 | 181 and the 16-byte tag |
| output ciphertext (ct_kem and body) | 1,285 | the ADR estimated "about 1,300" |
| output as carried (cm and the ciphertext) | 1,317 | |
| full viewing key | 161 | version, pk, nk, kem_seed, ivk |
| 24 words | 256 bits of seed and an 8-bit checksum | BIP-39 |

## 3. Scanning: the cost and the year of blocks

The scanner takes blocks in order (each: its outputs in order, its published nullifiers), tries every output with the viewing key, keeps the notes that open with their position (the count of outputs before them across the chain, the append-only tree's leaf index), computes each note's nullifier nf = H(nk, cm, position) once into a map, and marks a note spent when a block's nullifier hits the map. A restore from old words rescans and shows the true balance including the spent notes (ADR section 4.5's "never a stale one"); the tests check the balance to the unit against a stream with the wallet's notes among a stranger's.

Measured on builder seven, one core pinned (`taskset -c 5`), AMD EPYC 9454, release build, `igneum-money bench-scan --n 20000`, two runs:

| quantity | run 1 | run 2 |
|---|---|---|
| microseconds a ciphertext, not this wallet's (the common case: decapsulate, derive, AEAD open fails) | 30.8 | 32.3 |
| microseconds a ciphertext, this wallet's (the same plus the commitment check) | 31.7 | 33.3 |
| microseconds one ML-KEM-768 decapsulation alone | 27.0 | 28.1 |

So the trial decryption is the decapsulation plus about 4 µs; the AEAD and the hash are noise beside it. The figure of record for this page: 32 µs a ciphertext on one server core.

A year of blocks at 12 seconds is 2,628,000 blocks. The work depends on the outputs, not the blocks: an empty block costs nothing. At two outputs a transfer:

| transfers a day | outputs a year | outputs a block (mean) | ciphertext bytes a year | one server core, 32 µs each |
|---|---|---|---|---|
| 1,000 (the ADR's first figure) | 730,000 | 0.28 | 0.94 GB | 23 s (0.0065 h) |
| 10,000 (the ADR's second figure) | 7,300,000 | 2.8 | 9.4 GB | 3.9 min (0.066 h) |
| 100,000 | 73,000,000 | 28 | 94 GB | 39 min (0.66 h) |

A day at 1,000 transfers a day: 2,000 outputs, 2.57 MB of ciphertexts, 64 ms of work on one server core (the ADR's "under a second of work a day" holds with room).

What a phone would need (ESTIMATE, not measured; the row is stage 3's on a phone): libcrux's NEON path on a recent phone core is taken at three to ten times the server core's cost, 100 to 320 µs a ciphertext; a year at 1,000 transfers a day is then 1.2 to 3.9 minutes of one core and 0.94 GB of download; at 10,000 a day, 12 to 39 minutes and 9.4 GB. The download, not the arithmetic, is the phone's cost at every rate: the ADR's note that a wallet that cannot afford the download may hand a viewing key to a server stands, with the sentence of section 8 rule 9 on the screen when it does. Memory: the scanner holds one decapsulation key (2,400 bytes), the notes it owns and one 32-byte map entry per owned note; nothing proportional to the chain.

## 4. The in-circuit hash in this build

H (pk = H(sk_spend, nk), cm = H(v, pk, r), nf = H(nk, cm, position)) is the prover's decision (cut 1: Poseidon2 at a round count read against the 2025 to 2026 cryptanalysis, ADR section 4.7). This build runs on `Blake3StandIn`: H's shape over BLAKE3 with three fixed domain strings, behind one trait (`hash::CircuitHash`), named "blake3-stand-in" in every output and vector. It is not the hash the circuit will check. Dropping the prover's instance in is one `impl` of the trait and a re-cut of `tests/vectors.json`; nothing else in the crate names the hash. B, the hash outside the circuit (the seed derivations, the note key, the address checksum, the outgoing records), is BLAKE3 and is final.

## 5. Backup and restore

The seed as 24 BIP-39 English words: the words encode the 32 bytes themselves (the standard's entropy field) with the standard's 8-bit checksum; no passphrase and no PBKDF2 stretch, because the seed is already the key material and the derivation of section 1 is fixed. Restore is the derivation again and a rescan. Tested: the words restore the same address, spending secret and viewing key; a word outside the list, 23 words or a wrong checksum are refused; upper case is accepted; and in the tool, a wallet restored from the words of another rescans the same stream to byte-identical notes and the same two balances.

## 6. The command-line tool

`igneum-money` (wallet/money/src/main.rs), the wallet file `{"seed": hex}` at mode 0600, never overwritten:

- `keygen --wallet F`: a new seed; prints the address.
- `address`: the address of `--wallet F` or `--viewing-key F`.
- `viewing-key --wallet F --out V`: the full viewing key to a 0600 file.
- `backup --wallet F`: the 24 words, printed (its purpose).
- `restore --wallet F --words-file W` (or the words on standard input).
- `seal --to ADDRESS --value N [--memo TEXT]`: the sender's side; prints the 1,317-byte output as hex.
- `receive --output HEX|@file [--position N]`: trial-decrypts one output; the note as JSON, with its nullifier when the position is given, or "not this wallet's".
- `scan --blocks B.jsonl [--start-position N] [--notes-out notes.json]`: a block stream (one JSON line a block: height, outputs as hex, nullifiers as hex); prints blocks, outputs tried, notes, spent notes, the two balances and the seconds.
- `spend-prepare --wallet F --notes notes.json --position N --out witness.json`: the spend statement's witness for one unspent note: the note (v, pk, r), cm, position, the Merkle path (32 siblings) and the anchor as a placeholder of zeros with `path_placeholder: true`, sk_spend, nk, the nullifier, and the hash's name; checked outside the circuit before it is written (pk, cm and nf recomputed) so a witness that cannot prove is refused here. Refused for a spent note, for a note that is not the wallet's and under a viewing key.
- `bench-scan --n N`: the figures of section 3. `sizes`: the table of section 2.

The tool prints no seed, spending secret or viewing key; the witness and the viewing key go to files at 0600.

## 7. Caveats

1. H is a stand-in (section 4). Every pk, commitment, nullifier and vector on this page changes when the prover's instance lands; the sizes and the scan cost do not (a Poseidon2 evaluation outside the circuit is also microseconds).
2. The Merkle path in the witness is zeros. The pool (cut 2) owns the tree and serves paths under an anchor; `witness::prepare` takes a real path and anchor through the same call and clears the placeholder flag.
3. The diversified address is not built (F-756). One address per wallet; d does nothing.
4. The scanner needs a contiguous stream and the position of its first output; a wallet's birthday height and the tree's size at it come from the node, which this crate does not speak to.
5. The scan cost is one server core on AVX2; the phone figure is an estimate and stage 3's row measures it.
6. The disclosure receipt is not here (stage 4); the outgoing record it will need is.
7. The value is 64 bits of wei; the ADR's item 2 fixes it so; a note cannot carry more than about 18.4 IGN at 10^18 wei an IGN, which the pool's value units (cut 2) must settle. Said here so it is not missed: if the pool's unit is wei, 64 bits is too small for a savings note and the ADR's figure needs a re-cut to a larger field or a coarser unit.
8. The ML-KEM-768 key pair regenerates from kem_seed on every load of the viewing key (one key generation, about 30 µs); nothing caches dk on disk.
9. No PQ, privacy, "safe" or "final" word is in the crate or the tool's output.

## 8. The exact next step

The prover's proof plugged into `spend-prepare`: cut 1's prover takes `witness.json` (the statement name `money-spend-v1-input`, the fields of section 6) and the public inputs of ADR section 4.3 item 4, and returns the proof bytes; `spend-prepare` then writes the transaction's shielded part (anchor, nullifier, output commitments and ciphertexts, v_pub, fee, the proof). Before that: the prover's H instance into `hash::CircuitHash` and the vectors re-cut; the pool's path and anchor into `witness::prepare` in place of the placeholder. Then stage 3's row on a phone, with the figure of section 3 beside it.

Branch: lead/money-stage1-wallet. Builder: seven, the lab directory. Commands: `cargo test -p igneum-wallet-money --release` and `taskset -c 5 target/release/igneum-money bench-scan --n 20000` from `wallet/`.
