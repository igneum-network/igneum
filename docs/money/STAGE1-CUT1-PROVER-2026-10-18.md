SUPERSEDED by docs/money/STAGE1-JOIN-2026-10-18.md (the review's reading of 18 October 2026, F-773): the join's figures are the record; this page's section 6 estimate that 37 queries reach 128 bits is overturned (the lookup-challenge term of the quadratic extension caps the release at 108 conjectured bits whatever the query count, the join's section 5), its toy statement is replaced by money-spend-v1 and money-shield-v1, its single-secret note form by the ADR's two-key form, and its hash reading (threshold 4) is done on the join page. The page below is kept as the record of the choice of proof system and of the first measurements.

PASS on every row this cut can measure, with two conditions carried in the figures: the proof is at 96 conjectured bits (the pinned release's one preset) and the 128-bit size is an estimate; verification was measured on the lab box's core, not on the N100-class node. The ADR's verdict, "Money remains research", stands.

# Money stage 1, cut 1: the proof system chosen, a note-spend-shaped statement proven and measured

18 October 2026, 18:4x to 19:1x UK, on the lab box lab box A. The record this page answers is docs/money/ADR-0001-PRIVATE-PQ-SAFE-MONEY-2026-10-18.md (section 4.3 the construction, 4.5 the sizes, 4.6 the five thresholds, 6 stage one) as read by the review in docs/money/PANEL-READING-2026-10-18.md. The source of the prover and the verifier is under tools/money/stage1-prover/ in the same commit as this page. No binary is committed and no library is vendored.

## 1. The thresholds, each beside its measured figure

| ADR 4.6 row | the threshold | measured on this cut (depth 32, the median of five runs) | reading |
|---|---|---|---|
| (1) the proof | at or under 140 KB at 128 bits, the soundness bound named | 61,388 bytes at 96 conjectured bits (BLAKE3 commitments); 65,236 bytes with Poseidon2 commitments. At 128 bits, ESTIMATE 84 KB and 89 KB (section 6). The bound is the library's conjectured FRI security, computed by the verifier from the proof's own parameters and printed as 96 bits. | under the size by a factor of two; the bits short of 128 on this release |
| (2) verification | at or under 15 ms a proof on the N100-class node; 32 a block inside 250 ms | 0.815 ms on one EPYC 9454P core (3.74 GHz); 3.095 ms with Poseidon2 commitments. On an N100 core, ESTIMATE 2 to 3.5 ms (a factor of 2.5 to 4 on this kind of work, not measured). | under by a factor of four or more; the N100 row still to run |
| (3) proving | at or under 30 s and 3 GB on an 8-core PC; at or under 120 s and 2 GB on a phone | 17.5 ms and 10 MB peak on 8 EPYC cores; 69.7 ms on one core. A phone: ESTIMATE 0.2 to 0.4 s for this statement (section 7). | under by three orders of magnitude on this toy statement |
| (4) the in-circuit hash | chosen with its round count read against the 2025 to 2026 cryptanalysis and a fallback named | Poseidon2 over Goldilocks, width 12 (rate 8, capacity 4), 8 external and 22 internal rounds, S-box x^7, the library's padding rule (ePrint 2024/911). The reading against ePrint 2025/954, 2025/937 and 2026/150 is NOT done in this cut; the fallback (RPO, which the same library ships as a commitment hash but no longer as a VM instruction) is named, not measured. | parameters recorded; the reading open |
| (5) chain growth and the pruned sync | measured at 1,000 and 10,000 transfers a day | not this cut | open |

The same statement at depth 20 (section 5) is smaller and faster by the amounts the table shows; nothing changes in the reading.

## 2. The choice and its grounds

Chosen: **Miden VM 0.36.1** (crates.io release; git tag v0.36.1, commit 70bf609ade95a21d811045ee8d66fffe5d5a48dc; the crate checksum in Cargo.lock 93612f6da3ce48cade55a5faf63a3748d49e023d0906b74b28532b9bca2e5b07 for miden-vm and 1ab161622ecee8049127487b569122826264ebd67645cc6a898eb2eba4fd964e for miden-verifier). Rust, MIT or Apache 2.0, the 0xMiden organisation, released on 16 October 2026.

The grounds, against the brief's five:

1. **An open-source library with a maintained release.** Yes: a release two days old, a changelog, a security policy, a published performance page.
2. **A Rust toolchain that builds on lab box A.** Yes: rustc 1.99.0 at /opt/rust/cargo/bin, the release build of the two binaries 1 m 36 s on the first attempt after the API was read from the tag's source; no fix to the library was needed.
3. **A GPU-accelerated prover if one exists.** No, on two counts, both plain: the library's GPU path is Apple Metal only, there is no CUDA prover in the pinned release; and lab box A carries no GPU (lspci shows an ASPEED display controller and no NVIDIA or AMD device; nvidia-smi is absent). The GPU column of this page is therefore empty, not estimated. Of the ADR's other candidates, RISC Zero has a CUDA prover, but its proofs are receipts of a whole RISC-V execution (hundreds of KB before a curve-based wrap that is not PQ), so it fails threshold (1) by construction; Plonky3 and Stwo would need the Merkle-path AIR written by hand before any number existed.
4. **A verifier that runs in milliseconds on a CPU.** Yes: under a millisecond measured (section 5).
5. **Proof sizes published.** Yes: the performance page in the repository (80 KB at 2^14 cycles to 136 KB at 2^20, 96 bits, BLAKE3), which is the source the ADR's 80 to 140 KB estimate was read from, so the measurement here is against the same scale.

What decided it over a hand-written AIR (Plonky3) or a Circle STARK (Stwo): the hash-based Merkle path, the native Poseidon2 hash and the Merkle-store advice are instructions of the VM (hash, hmerge, mtree_verify), so the whole statement is 60 lines of assembly and the numbers exist tonight; the proof system is a FRI STARK over Goldilocks with its quadratic extension, which is the family the ADR chose (hash-based, no curve, the QROM argument of ePrint 2019/834); and the library's own verifier computes the security level from the proof's transcript parameters, so the bound is read, not asserted.

What the choice does not settle: the VM's one preset is 96 conjectured bits (27 FRI queries at blowup 8, 17 bits of query grinding, 12 bits of DEEP grinding, 4 of folding grinding, folding arity 4, final degree 128). The CLI refuses any other setting and the library exposes none. A 128-bit setting is a constant change in the library's config (more queries or a larger blowup), which this cut did not patch, because a patched library is neither a release nor a pin.

## 3. The statement

The note-spend-shaped statement, as small as it can be while keeping every part the ADR's spend has: a public root, a private leaf and path, a nullifier and a value commitment as outputs. H is Poseidon2 over Goldilocks, the VM's native hash: hash(A) on one word, H(A, B) on two words. A word is four field elements.

- Public input: ROOT, the root of a Merkle tree of depth 32 over H.
- Private: v the value (one element), S the owner secret (one word), R the note randomness (one word), ROUT the output randomness (one word), index the leaf position, and the 32 sibling nodes of the path (in the VM's advice Merkle store).
- The leaf: CM = H( H([v,0,0,0], hash(S)), R ), which is H(value || owner_secret_hash || randomness) with the owner secret hash as hash(S).
- The check: CM sits at depth 32, position index, under ROOT (the mtree_verify instruction, which drives the hash chiplet through the 32 merges).
- Public outputs: NF = H(S, [index,0,0,0]), the nullifier H(owner_secret || leaf_index); VC = H([v,0,0,0], ROUT), a value commitment under fresh randomness; and ROOT carried through.

The verifier's claim is the program's MAST root (9131b655112545d66e3e3548b476e990df68e3bb660597fdb2d1cc12140f5d2d at depth 32; 34dc7c0cbd839cdda4d418448ad53dbeae3ba65ead8073c73e7fe4b10ed1336c at depth 20), the stack input (ROOT) and the stack outputs (NF, VC, ROOT). The witness never leaves the prover. The host computes NF and VC independently (miden-crypto's Poseidon2, the same crate) and the prover refuses to write a proof whose outputs differ from the host's; the host's hash of [1,2,3,4] equals the VM's, checked on the first run.

The trace: 656 VM cycles padded to 1,024 steps at depth 32 (41 Poseidon2 permutations: the 32 merges of the path, the five hashes of the statement, and the VM's own four); 464 cycles padded to 512 at depth 20. This is the smallest trace the VM proves; a real spend (two inputs, two outputs, the value balance, the range checks, the transaction-hash binding) is a few thousand cycles, and the proof grows slowly with the trace (section 6).

Not in the statement: the range check on v, the balance of inputs against outputs, the second input and output, the binding to a transaction hash, and the pk = H(sk_spend, nk) split of the ADR's keys. Each is a handful of instructions; none changes the shape of the numbers.

## 4. The machine, the pins, the commands

- lab box A: AMD EPYC 9454P, 48 cores and 96 threads at up to 3.74 GHz, 125 GB; Ubuntu 24.04.5, kernel 6.8.0-139; no GPU. Other seats' nodes and a Go test run were on the box at the time; the load average was about 8 during the measurements.
- rustc 1.99.0 (b940084d7 2026-09-28), cargo 1.99.0, the release profile with fat LTO and one codegen unit.
- Pins: miden-vm, miden-assembly, miden-core, miden-crypto and miden-verifier all "=0.36.1" (tools/money/stage1-prover/Cargo.lock; the registry's .cargo_vcs_info.json for the crate names commit 70bf609ade95a21d811045ee8d66fffe5d5a48dc, the v0.36.1 tag).
- The built binaries (not committed): money-prove 14,642,544 bytes, sha256 e9854d85c710233a5836ab632e8e9bbffd052b58ea66e4d6b7b9b40398428c0a; money-verify 5,798,584 bytes, sha256 cc996172094e268aef7c39901483cfa82036bd93ae731bcce56c1c4b849de0ee (4,984,200 bytes after strip); money-witness 438,648 bytes, sha256 28582cccbeb09dc2ed7bb4c1c1b57d973762558cdddc9e6835477d3068c917b2.
- The proofs of record: out/p-32-blake3.bin sha256 bd70c6e75f3877ee..., out/p-32-poseidon2.bin 38c594147dacdf33..., out/p-20-blake3.bin 6986de868b24f6df..., out/p-20-poseidon2.bin 6f376c78f6ec7b0b... (left on lab box A under the lab user's home).

To reproduce on the box:

```
cd tools/money/stage1-prover
cargo build --release
./measure.sh                      # writes out/*.log, out/*.bin, out/*.json
```

measure.sh does, for each depth (32, 20) and each commitment hash (blake3, poseidon2): the witness from seed 1 (money-witness <depth> 1 out/w<depth>.json); five prove runs in one process on 96 threads, then on 8 cores (RAYON_NUM_THREADS=8 taskset -c 0-7), then on one core (RAYON_NUM_THREADS=1 taskset -c 0), the median printed by the process; one 8-core run under /usr/bin/time -v for the peak resident set; and five verifier processes pinned to core 0, each 101 verifications of the proof of record with the median printed. A verification run includes reading the proof bytes.

The Miden CLI (built from the tag at the same commit) executes the same assembly with the same inputs to the same outputs and MAST root; it is the cross-check, not the measurement.

## 5. The figures

Medians of five, lab box A, Poseidon2 inside the statement in every row. "Commitments" is the hash the STARK uses for its Merkle commitments and Fiat-Shamir: BLAKE3 (the library's default, fastest) or Poseidon2 (recursion-friendly, slower).

| depth | commitments | proof bytes | prove, 96 threads | prove, 8 cores | prove, 1 core | peak RSS (8 cores) | verify, 1 core | security |
|---|---|---|---|---|---|---|---|---|
| 32 | BLAKE3 | 61,388 (five runs 61,132 to 62,636) | 63.7 ms | 17.5 ms | 69.7 ms | 9,984 KB | 0.815 ms | 96 bits conjectured |
| 32 | Poseidon2 | 65,236 (64,436 to 65,972) | 89.8 ms | 70.7 ms | 293.0 ms | 10,572 KB | 3.095 ms | 96 bits conjectured |
| 20 | BLAKE3 | 52,636 (52,636 to 55,588) | 50.7 ms | 12.4 ms | 31.7 ms | 7,680 KB | 0.699 ms | 96 bits conjectured |
| 20 | Poseidon2 | 57,900 (57,388 to 58,476) | 74.9 ms | 81.1 ms | 165.3 ms | 7,680 KB | 2.639 ms | 96 bits conjectured |

- Proof bytes vary from run to run by about one to two percent because the FRI query positions differ and the shared Merkle nodes dedupe differently; the figure is the proof of record, the band is the five runs.
- The raw proof is the only form: the library offers no compression and no wrapping; the bytes are the serialised STARK (wincode encoding) with the proof's header. The two commitment hashes are the only size choice.
- Execution of the program (not the proof) is about 0.5 ms in every row.
- 96 threads are slower than 8 on a 1,024-step trace: the thread pool costs more than the work. The 8-core figure is the one the ADR's "8-core PC" row asks for.
- Verifier binary: 5,798,584 bytes as built (4,984,200 stripped); it links the verifier, the precompile registry and a JSON reader, no assembler and no prover.
- GPU: no figure (section 2, ground 3).

## 6. The sizes against the ADR's estimate

- The ADR estimated the two-in two-out proof at 80 to 140 KB and the transaction at 85 to 145 KB. This toy statement's proof is 61 KB at 96 bits; with about 2.8 KB of public data and ciphertexts the transaction would be about 64 KB.
- At 128 bits, ESTIMATE: the query round at blowup 8 gives three conjectured bits a query plus 17 bits of grinding; 27 queries make the preset's 96. 37 queries at the same grinding make 128. The query openings are the bulk of the proof, so the size scales by about 37/27: 84 KB (BLAKE3) and 89 KB (Poseidon2) for this statement. A blowup of 16 would reach 128 bits with the same 27 queries at about the same size and twice the proving time. Neither is measured.
- The real statement is two paths, two output commitments, the balance and the range checks: about four times this trace, 2^12 steps. The library's own scale (80 KB at 2^14, 100 KB at 2^16, 96 bits) puts it at about 65 to 75 KB at 96 bits and 90 to 105 KB at 128 bits, ESTIMATE, inside the ADR's band and in its lower half.
- Compared with the public figures in the ADR: Orchard's two-action transaction is 9,160 bytes and Monero's 2-in 2-out about 2 to 2.5 KB; this hash-based proof is seven to thirty times larger, which is the price the ADR already priced.

## 7. What a phone would need

An estimate from the one-core CPU figure, labelled as such: one EPYC core proves this statement in 69.7 ms (BLAKE3 commitments) at 10 MB. A phone's big core is about a third to a fifth of that core on this kind of work, so 0.2 to 0.4 s and under 50 MB for this toy, and for the real statement at 2^12 steps about 1 to 2 s and under 200 MB. The ADR's phone row (120 s, 2 GB) is two orders of magnitude away. No phone was used; the Metal path exists in the library for Apple hardware and was not built.

## 8. Caveats

1. A toy statement is not the pool. It proves one path, one nullifier and one commitment, with no balance, no range check, no second input or output, no transaction binding, no key split. The numbers are the floor of the VM's cost, which is why they sit at the library's published minimum.
2. 96 bits, not 128. The pinned release proves at one conjectured security level and the ADR's threshold (1) says 128. The size at 128 bits is estimated in section 6 and is under the threshold; it is not measured. "Conjectured" is the library's word: the FRI bound it uses is the conjectured one, not the proven one, and the ADR's section 4.7 asks for exactly that to be named.
3. The hash reading (threshold 4) is not done: the parameters are recorded and the same Poseidon2 instantiation (Goldilocks, width 12, 8 external and 22 internal rounds) is the one the Quantus chip audit of record found in QPoW, so one reading would serve both pages.
4. Verification was measured on a 3.7 GHz server core, not on the N100-class node, and includes the proof's deserialisation. The figure is 18 times under the threshold; the N100 run is a cell of the next cut, not a risk.
5. The box carried other seats' work during the runs (load about 8 on 96 threads). The 1-core and 8-core rows were pinned with taskset; the 96-thread row was not and is the least steady of the four.
6. No GPU figure. The library has no CUDA prover and the box has no card. If a GPU column is wanted, it is a different library (RISC Zero, with proofs that fail the size row) or a different box.
7. The sizes before and after wrapping are the same: there is no wrapping in this system.

## 9. The next cut

The note, the commitment and the nullifier in the node's rules on a throwaway chain: a new transaction type carrying the proof bytes, the anchor, the nullifiers and the output commitments (ADR 4.3 item 4, with F-761's fee path); the node keeps the commitment tree (depth 32, Poseidon2 over Goldilocks, the same crate from Go through a small FFI or a Go port of the permutation, the test vectors from this page's witness) and the nullifier set; a block with a repeated nullifier or an unverifiable proof is refused; the verifier of this page is the node's verifier, linked once, with the 250 ms block budget measured at 32 proofs a block on the N100-class node. Alongside it, the statement grows to the ADR's full spend (two inputs, two outputs, the balance, the range checks, the transaction hash) and is measured again on this page's form, and the 128-bit setting is measured on a patched build of the library to replace section 6's estimate with a figure, labelled as a patch until a release offers the setting.
