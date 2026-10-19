# Ember for HiveOS

The Igneum miner as a HiveOS custom miner: `igneum-hive-<version>.tar.gz` holds the Linux `igneum-miner`, `igneumd`,
the two GPU workers (`igneum-worker-cuda` for NVIDIA, `igneum-worker-opencl` for AMD) and the four hooks Hive runs
(`h-manifest.conf`, `h-config.sh`, `h-run.sh`, `h-stats.sh`). Built on 4 October 2026. **Hive itself is untested**: the
scripts were self-tested with stub binaries (`selftest.sh`) and the binaries were cross-compiled on a Mac; the first real
run on a Hive rig is still to come. Report what breaks.

**A HiveOS rig mines only.** The package (`make-hive-package.sh`) carries `igneumd`, `igneum-miner` and the two
workers; no `igneum-prove-host`, no `igneum-prove-export`, no SP1 GPU server. So a rig on this package earns from the
80% lottery share and nothing from the 20% proving share until a Linux prover build is published. The Ubuntu rig
installer (`packaging/linux/README.md`, branch `rig-install`, commit dd632c1) carries a prover unit that idles in state
`setup` for the same reason; its per-card table is the fuller version of the one below. What each card could do once
the prover ships, from the app's default (`app/igneum-app/src/provedefault.rs` at 440fd59 on `proving-v1`: proving on
at 24 GB or more, off below) and from the proving agent's S_p curve of 5 October 2026 (`docs/bench-log.md`, "proving
v1": jobs `memsweep-pc2-pv1`, `memminer-pc2-pv1` and the S_p curve, one RTX 5090, SP1 6.8.1's GPU server):

| Card | Once a Linux prover ships | Measured on 5 October 2026 |
|---|---|---|
| 8 GB | mines only | the prover's floor is 13.9 GB for an empty shard |
| 12 GB | mines only; proves nothing on this SP1 build | the same 13.9 GB floor |
| 16 GB | in practice mines only: proves empty shards alone, nothing beside the miner | 13.9 GB alone; 15.7 GB beside the miner leaves nothing for the display |
| 24 GB | proves the adopted v1 shard (30,000 pgas) from the fee switch at DAA 210,000, nothing before it | 20.4 GB alone, about 22 GB beside the miner (approximate, not measured on a 24 GB card); the prototype shard at 28.3 GB does not fit |
| 32 GB | mines and proves, prototype shard included | 28.3 GB alone, 30.0 GB beside the miner, 2.5 GB spare |

## Flight Sheet

| Field | Value |
|---|---|
| Miner | Custom |
| Installation URL | `https://<host>/igneum-hive-<version>.tar.gz` (the archive from `make-hive-package.sh`, published on the download host) |
| Miner name | `igneum` |
| Wallet and worker template | `0x<40 hex>.%WORKER_NAME%`: the payout address is an EVM address you hold the key for; the part after the dot labels this rig's keys |
| Pool URL | `grpc://<your node>:26610` (your own igneumd, solo mining), `local` to run the bundled node on the rig, or `pool://<pool host>:4463` to mine to a pool (spec 09; `NODE=local` in the extra config adds a verifier node for voting) |
| Pass | empty |
| OVERRIDE | the devnet's consensus override as one JSON object on its own line, needed with `local`: `OVERRIDE={"difficulty_v2_activation_daa":33000,"proving_v0_activation_daa":84100,"fees_v1_activation_daa":210000,"finality_v3_activation_daa":135200}` is the four-field object an earlier release shipped; after the an earlier release switch it has nine fields, and the downloads page carries the live one. Set OVERRIDE from the downloads page when it changes. **A rig without it is refused**: its node runs on genesis parameters, prints another digest, and every devnet peer drops it (`h-run.sh` warns in the main log) |
| Extra config arguments | `DEV_FEE=1 IDENTITIES=auto WORKER=auto VOTE=1` (one per line also works); `PEERS=a:26611,b:26611` for the bundled node; `NODE=local` or `NODE=grpc://host:26610` for the verifier node in pool mode; `EXTRA="..."` for more miner flags |
| IDENTITIES | vote keys per card: 8 for a card with 8 GB or more, else 2 (`IDENTITIES=auto` applies that rule, per card, from `nvidia-smi` or the amdgpu sysfs; 8 when neither answers; a number overrides it for every card). The rule is the app's (`app/igneum-app/src/detect.rs`) |

Solo mining: each card mines block templates from the node and the block reward pays the wallet in the template (80% of
each block to its finder, 20% to the proving pool; the protocol takes no fee for anyone).

Pool mining (5 October 2026, `pool/README.md`): with `pool://host:4463` each card runs `igneum-miner ... --pool host:4463
--evm-address <wallet> --worker-name <label>-gpuN`; the pool names this rig's own vote key in every header (one key per
rig, `IDENTITIES` is ignored), pays the wallet by PPLNS on the EVM side, and the miner takes no software dev fee in pool
mode (the pool's fee, printed in its welcome line, is the only fee). With `NODE=local` the bundled node runs beside the
cards as the verifier: the miner checks the pool's seeds and templates against it, votes with the rig's key through it
and submits found blocks there too. Without a node the rig hashes and does not vote. Untested on a Hive rig, like the rest
of this package.

## The dev fee

The Igneum miner software takes a 1% dev fee the way every GPU miner does (lolMiner, T-Rex): one block template in
100 is requested with the dev payout address instead of yours. It is a counter, not a random draw, so it is exactly
1 in 100 and anyone can audit it from the source (`igneum/miner/src/main.rs`, "Software dev fee") or from the chain
(`igneum-miner payouts grpc://<node>:26610` lists blocks per payout address). `DEV_FEE=0` in the extra config turns it
off. The miner prints its own line at start, in each GPU's log:

    dev fee 1% (1 block in 100) to 0x<address>; --dev-fee 0 turns it off

The protocol carries no fee: this is the software's, and any other miner client is welcome.

## What the hooks do

| Hook | What |
|---|---|
| `h-config.sh` | writes `igneum.conf` from the Flight Sheet (node URL, wallet, label, DEV_FEE, IDENTITIES, WORKER, VOTE, PEERS, EXTRA, OVERRIDE); refuses an OVERRIDE that is not `{...}`; resolves `IDENTITIES=auto` to `IDENTITIES_GPU<N>` keys by VRAM; refuses a wallet that is not 0x + 40 hex |
| `h-run.sh` | starts the bundled node when the URL is `local` (writes `data/override-params.json` from OVERRIDE and passes `--override-params-file`; warns when OVERRIDE is empty; copies the node's switch lines and its `Consensus params digest` line into the main log), waits for the node, exports the hourly program pack (`igneum-miner export-pack`), then one `igneum-miner` per GPU with its worker (`--identities` from `IDENTITIES_GPU<N>`, else `IDENTITIES`); restarts a miner that exits (exit 42 = program change without prepare support: the pack is re-exported first); per-GPU logs `<log>.gpu<N>.log`, merged into the main log |
| `h-stats.sh` | per-GPU hash rate from each miner's last `STATUS` line (`now=<MH/s>`), accepted and rejected totals, dev-fee block count, temperatures and fans from Hive's `gpu-stats` (else `nvidia-smi`), uptime, version |

Stats JSON (what Hive reads from `$stats`): `hs` (kH/s per GPU), `hs_units` (`khs`), `temp`, `fan`, `uptime` (s),
`ver`, `ar` (`[accepted, rejected]`), `algo` (`igneum`), `bus_numbers`, plus `dev_fee_blocks` (ours). `$khs` is the total.

## Requirements on the rig

- NVIDIA: the driver (`libcuda.so.1`) and NVRTC (`libnvrtc.so.12`, from a CUDA 12 toolkit or the NVRTC redistributable
  on the library path). The worker compiles the hourly program at run time with NVRTC; without the library it says so
  and the miner retries. Hive images ship the driver; whether `libnvrtc.so.12` is present depends on the image, untested.
- AMD: an OpenCL ICD (`libOpenCL.so.1` from ROCm or amdgpu-pro). The OpenCL worker compiles the program through the ICD.
- The bundled node (`local`) keeps its chain data under the miner folder (`data/`); a devnet chain is small today. It
  needs OVERRIDE (the Flight Sheet table): compare the `node: Consensus params digest:` line in the main log with the
  digest on the downloads page; a different one means the override is stale and the node is refused.
- Ports: the bundled node listens on 26611 (p2p) and answers RPC on 127.0.0.1:26610 only.

## Building the package

    infra/cross/build-linux.sh              # igneumd and igneum-miner for Linux (cargo-zigbuild), into infra/cross/out
    infra/cross/build-workers-linux.sh      # the two workers (zig c++ and zig cc), into infra/cross/out-workers
    packaging/hive/make-hive-package.sh     # the archive, sha256 and the Flight Sheet lines; NODE_OUT=infra/cross/out-v2 to pick a build
    packaging/hive/selftest.sh              # bash -n, a stub package, the three hooks the way Hive runs them

## Known gaps (4 October 2026)

- Never run on Hive. The hook contract follows Hive's custom-miner README (archive `name-version.tar.gz` with the
  directory `name/`; `h-config.sh` and `h-stats.sh` are sourced; `$khs` and `$stats`), approximate until a rig confirms it.
- GPU order: the CUDA device index is assumed to follow `nvidia-smi` order and Hive's `gpu-stats` arrays (NVIDIA first);
  a mixed NVIDIA and AMD rig may show temperatures against the wrong card.
- Each card runs its own `igneum-miner` and node connection; the node's template RPC serves them all.
- Before this change no package carried the override: `h-run.sh` started the node without `--override-params-file`, so
  a `local` rig was refused by every devnet peer (release an earlier release, section 5, C41). Still untested on a rig.
- No prover in the package (the second paragraph above): the rig earns nothing from the proving share until a Linux
  prover build ships.
