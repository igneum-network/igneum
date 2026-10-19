# pow/ref: the shared proof-of-work reference

One consensus truth for every backend and for the node's verification hook. C ABI in `include/ignpow.h`; candidates in `src/`; the vendored upstreams in `../third_party/<name>-<commit>/` with a PIN file each.

Candidates in this build: `kawpow-0.9.4` (C1, from the pinned Ravencoin v4.8.0 library), `autolykos-2` (C3) and `igneumhash` (IgneumHash-R0A v0, `docs/igneumhash/SPEC-R0A-v0.md`, research only; the variant is the params document, `include/igneumhash.h` carries the candidate-specific side). C2 (`fishhash-1`) follows behind the same interface. The install carries `lib/libignpow.a`, `lib/librvn_ethash.a`, `lib/libblake3.a` and both headers; link all three libraries in that order.

Build (on the assigned CPU box or a one-shot CPU host, never on the Mac):

    cmake -S pow/ref -B build/ref && cmake --build build/ref -j
    build/ref/ignpow-vectors selftest                         # the embedded upstream vectors
    build/ref/ignpow-vectors gen kawpow-0.9.4 unseen pow/vectors/kawpow-0.9.4/unseen.json
    build/ref/ignpow-vectors gen kawpow-0.9.4 nonce-boundary pow/vectors/kawpow-0.9.4/nonce-boundary.json
    build/ref/ignpow-vectors gen kawpow-0.9.4 transitions pow/vectors/kawpow-0.9.4/transitions.json
    build/ref/ignpow-vectors gen kawpow-0.9.4 malformed pow/vectors/kawpow-0.9.4/malformed.json
    build/ref/ignpow-vectors gen kawpow-0.9.4 tails pow/vectors/kawpow-0.9.4/tails.json     # builds the 1 GiB epoch-0 dataset
    build/ref/ignpow-vectors check kawpow-0.9.4 pow/vectors/kawpow-0.9.4/<class>.json
    build/ref/ignpow-vectors bench kawpow-0.9.4 1 ; ... 3360000 ; ... 4487776            # light-verify cost (T-3)

IgneumHash takes its variant as `--params '<json>'` before the command (or `IGNPOW_PARAMS`), for example
`build/ref/ignpow-vectors --params '{"dataset_mib": 256, "scratch_kib": 16, "rounds": 64}' gen igneumhash unseen pow/vectors/igneumhash/d256-s16-r64-c1/unseen.json`;
it adds the classes `trace` (every intermediate of two hashes, for kernel authors) and `dataset` (E, D, the cache digest under version 1 and the digest of the whole dataset per epoch, with the build time), and the `addrtrace` command (the security review's access and lane-dependence traces, `tests/igneumhash/me01.py`'s format). Heights used by every class come from the candidate's own epoch length. `version` is mandatory in every document (`{"version": 1}` is the version 1 baseline, `{"version": 0}` the version 0 baseline, `{"version": 2}` the R0-B schedule baseline); a document without it is refused.

Vector classes and what each proves are in `docs/gpu-a/DESIGN.md` section 3. Generated files record the generator build string and the seed; `pow/vectors/<candidate>/upstream/` keeps the upstream test source they are cross-checked against.
