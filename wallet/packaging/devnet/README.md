# Igneum Wallet, the public test network build

A public test network build of the Igneum Wallet, pinned to the Igneum public test network (chain id 7322, network
id 7394). The coins on the public test network have no value, nothing is bought or sold, and the chain can be reset at any time; everything you
make here is for trying the wallet and the chain, not for keeping. The build is labelled so on its first screen.

## What is in the package

| Path | What it is |
|---|---|
| `bin/igneum-wallet-engine` (`.exe`) | the engine: talks to the chain, composes transactions, serves the page on 127.0.0.1 under a per-run token |
| `bin/igneum-signer` (`.exe`) | the signer: the only process that touches a key; a child of the engine with pipes to it and nothing else, no network code |
| `bin/igneum-wallet` (`.exe`) | the command line (create, import, send, simulate, bond, claim, receipts) |
| `ui/` | the page the engine serves (the wallet's window in your browser) |
| `igneum-wallet.sh` (Linux and macOS), `igneum-wallet.cmd` (Windows) | the launcher: starts the engine and opens the page in your default browser |
| `icons/` | the wallet's icon set (`icon.icns` for macOS, `icon.ico` for Windows, `icon-512.png` for a Linux desktop entry, `icon-1024.png` for listings) |
| `VERSION` | the build's version and commit |

## Start

Linux and macOS: `./igneum-wallet.sh start` (then `stop`, `status`, `url`). Windows: double-click `igneum-wallet.cmd`
(close its window to stop). The page opens in your browser at a 127.0.0.1 address with a token only this run knows.
Nothing listens on any other interface. Websites reach the wallet only through the page bridge on 127.0.0.1:26811,
and only after you say yes in the wallet's own window.

Headless (a server, a container, a pod): start the engine yourself, `bin/igneum-wallet-engine --data-dir <dir>
--signer bin/igneum-signer --ui ui`; it prints one `ENGINE` line with the page's URL, and the page's API answers at
that URL for a script. The routes, all JSON, POST unless named GET: `api/state` (GET), `api/create`, `api/create/confirm`
(the three words checked back), `api/import` (mode seed, key or watch), `api/unlock`, `api/lock`, `api/backed-up`,
`api/reveal`, `api/password/check`, `api/send/quote` and `api/send` (a quote reviewed, then sent by its id), `api/faucet`
(the one-tap drip), `api/payments` (the address's payments since a block), `api/holder`, `api/authority/desk`,
`api/authority/quote`, `api/bond-faucet`, `api/ledger`, `api/settings`, `api/remove`, `api/activity`, `api/bridge/pending`
(GET) and `api/bridge/decide` (a site's ask answered), `api/quit`. Each answers `{"error": "..."}` with its reason when
it refuses; the engine's own words are the record.

The chain: the wallet reads the public test network and sends to it through the public endpoint `https://rpc.igneum.network` (one
host for reads and raw sends, twenty calls a second per source), names the chain by its genesis hash
(`0x45828a34…2236ea`), and shows the explorer at `https://explorer.igneum.network`. A reset chain reads as "chain
7322, not this build's pin" until the next build carries the new pin. Settings lets you point the wallet at your own
node. Test coins: the faucet at `https://igneum.network/faucet` (25 IGN a day per address, no value), or the "Get
test IGN" row in the wallet.

## Locked memory

The signer keeps the key's pages locked in RAM (never swapped) and, where the machine allows, every later page too:
the vault's key derivation (Argon2id) works in 256 MiB, so the signer asks for about 320 MiB of lockable memory. A
stock container usually allows 8 MiB and will not let even root raise it; there the signer locks the pages it holds
at start, leaves later allocations unlocked, prints one line on stderr saying so, and reports it in `api/state`
under the signer's `memory_lock` (`all`, `current` or `none`, with the limit). The wallet works either way; on a
machine of your own the lock covers everything.

## Signing

A signed cut carries `SHA256SUMS.sig` beside `SHA256SUMS` on the releases host (`https://build.igneum.network/releases/wallet/<version>/`),
made with the release key whose public half is `infra/release/keys/release-signing.pub` in the repository; a cut
without the `.sig` is a candidate, and `infra/release/verify.sh wallet <version>` says which from the host. Check the
package's sha256 against `SHA256SUMS` either way.

## The security sentences and their rows

The wallet is designed so that the key never leaves the signer process, the signer has no way to reach a website,
and a website gets nothing until you say yes. Three rows test that on this build's line and passed on the project's
build box (the record is `docs/product-b/RUNS.md`): ISO-01 (the signer is its own process with pipes to the engine
and nothing else open, no socket, an environment of PATH alone), ISO-02 (no network crate in the signer's tree, no
socket symbol in its binary, no network call under trace through a whole session), ISO-03 (no address, signature or
transaction before your yes; a no leaves everything as it was; a site's later calls carry the token its approval
minted). The threat model and the suite's records are under `docs/wallet-security/`.

## Reproducing the build

`wallet/tools/release-repro-build.sh <commit>` on a Linux box with the pinned Rust toolchain and mingw builds both
packages from the commit with the paths remapped and the commit's time as every file's time; two boxes build
independently and their manifests must agree byte for byte before a package is published.
