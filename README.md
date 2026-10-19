# Igneum

[![Discord](https://igneum.network/badge/discord.svg)](https://discord.com/invite/igneum)

This repository is the GitHub mirror of the Igneum public tree. The tree lives at https://git.igneum.network; this
mirror follows it, commit for commit, with this README laid over the tip as one commit, and carries nothing the tree
does not but this README.

## What this is

Igneum is a GPU-mined chain whose work is IgneumHash, a proof of work in lanes that a GPU does well and a purpose-built
chip does not do much better. The tree holds what the network runs:

| path | what |
|---|---|
| `node/` | the node (an Ethereum-form chain with IgneumHash as its consensus work; GPL-3 and LGPL-3, see `node/COPYING` and `node/COPYING.LESSER`) |
| `pool/` | the pool (the stratum server, the share journal, the per-chain miner views) |
| `miner/` | Ember, the miner (Linux, HiveOS, macOS, Windows) |
| `wallet/` | the wallet (the desktop app and the command line) |
| `pow/` | the IgneumHash reference, the workers (CUDA, OpenCL, Metal, GL) and the registered vector sets |
| `explorer/`, `indexer/` | the block explorer and its indexer |
| `contracts/` | the chain's contracts (the faucets, the registry) |
| `brand/`, `look/` | the brand and the look |

## The public test network

The public test network is chain id 7322 (network id 7394). Its public hosts: the RPC at https://rpc.igneum.network,
the explorer at https://explorer.igneum.network, the pool at pool.igneum.network:3342 (and :3341), the status feed at
https://build.igneum.network/feed/7322/. Its coins are test coins with no monetary value; the faucet is named in the
feed and the explorer. The target block time is 12 seconds everywhere: the research chain measured 12.3 seconds over
its hour of 16:05 to 17:05 UK on 16 October 2026 (292 blocks, none replaced), and the public test network moves to
12-second blocks with node 0.10.40 (16 October 2026).

## The apps

Signed releases of the node, the pool, Ember and the wallet, with their sums, signatures and manifests, are at
https://build.igneum.network/releases/ and verify with the release key at https://build.igneum.network/keys/. Every
release is built on two clean builders that agree byte for byte; its manifest names the commit it was built from. The
pointer files under `releases/<component>/latest/` name the current release of each component.

## Building from source

Each component builds from its own directory with the toolchain its README names (the node and the pool with Go, the
reference and the workers with CMake and the vendor's GPU toolkit, Ember and the wallet with their packaging scripts
under `miner/` and `wallet/`). The release builders' exact commands are recorded in each release's builder manifests
beside its sums.

## Licence

Every directory of this tree is under the Apache License, Version 2.0 (the `LICENSE` file at the root), except
`node/`, a fork of go-ethereum, which keeps go-ethereum's licences: the library under the LGPL version 3
(`node/COPYING.LESSER`) and the binaries under the GPL version 3 (`node/COPYING`).

## Community

The team is on Discord at https://discord.com/invite/igneum. The channels that matter: `#github` carries each commit
of this repository as it lands on the public tree; `#start-here` is mining in a minute (download Ember, point it at the
pool, your address); your card's help channel (`#help-nvidia`, `#help-amd`, `#help-apple`, `#help-intel`,
`#help-hiveos`) is the install, what mines today and what to paste when asking. Beside Discord: the site at
https://igneum.network, the whitepaper at https://igneum.network/whitepaper, the explorer at
https://explorer.igneum.network and the public git host at https://git.igneum.network.

## This mirror

The tree lives at https://git.igneum.network; this repository follows it commit for commit, and this README is the
one commit laid over its tip. Issues and contributions are not taken here; the project's channels are at
https://igneum.network.
