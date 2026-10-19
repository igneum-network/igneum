# Explorer: self-hosted Blockscout for Igneum (R06)

The plan (page 7): a self-hostable Blockscout, independently operated instances encouraged, with the tracing and RPC support it actually needs. This directory holds what an operator adds to a Blockscout checkout to run it against an Igneum node. Nothing here is served publicly; the instance on the build box binds every port to 127.0.0.1 and nothing goes public without the founder's go.

## State at 00:52 UK, 10 October 2026: indexing the shared devnet

Blockscout on the build box now reads the reliability engineer's archive dev node on another build box over the mesh (`http://NODE-HOST:8545`, `ws://NODE-HOST:8546`, `--gcmode archive`, `debug` served): from a wiped database it indexed the whole chain (1,145 blocks, 159 transactions at the probe, indexed ratio 1.00, internal transactions 1.00) and recognises product engineer B's Pay set at its six published addresses as contracts. The private `devnode` container stays as the offline fallback (`igneum-devnode.yml`) and is not started.

Lesson from the switch: `docker compose down -v` does not wipe Blockscout's databases, which are bind mounts (`services/blockscout-db-data`, `services/stats-db-data`); pointing the stack at a different chain without wiping them mixes two chains (the stats showed 1,692 blocks against a node at 1,078). `prepare.sh --wipe` removes both before the first start.

The light indexer (`indexer/`) run against the same node at the same time: 1,155 blocks, 155 transactions all succeeded, one producer, four active addresses, supply from `igneum_supply`, and `useful_external_transactions` measured at 0: the devnet's traffic is three addresses paying each other plus the deployer's six creations; nobody has called a Pay contract yet, so 0 is the true reading, not a gap.

## What ran before the switch (9 October 2026, 22:5x UK)

- Blockscout v12.0.0 (commit 5b7c157d), the full `docker-compose/docker-compose.yml` stack (backend, frontend, proxy, two PostgreSQL, redis, stats, visualizer, sig-provider, user-ops-indexer, nft-media-handler), prepared with `prepare.sh`.
- The dev node from `main` (235dcde or later) as a container on the compose network (`igneum-devnode.yml`): `--dev.pow --miner.threads 1 --gcmode archive`, HTTP and WS on the container only, HTTP published on 127.0.0.1:8545 for probes.
- Result: blocks, transactions, addresses and balances index; `debug_traceTransaction` with the `callTracer` answers and the internal-transaction indexer runs; the API at `127.0.0.1/api/v2/*` and the frontend at `127.0.0.1/` answer.

## What the node must provide (confirmed against the running instance)

| Need | State |
|---|---|
| `eth` namespace over HTTP and WS | present; `eth_chainId` 0x1c8e (7310), `newHeads` over WS on 8546 |
| `debug_traceTransaction` and `debug_traceBlockByNumber` with `callTracer` | present (`--http.api` must list `debug`); the genesis block is "not traceable", which Blockscout tolerates |
| historical state for `eth_getBalance` at past blocks | needs `--gcmode archive`; without it the balance fetcher logs `missing trie node` for every address at every block |
| header shape | the geth shape with no added field, as protocol engineer A confirmed; Blockscout's `geth` variant decodes it unchanged |
| `txpool_content` | optional; present in the dev run |

Update 23:02 UK 9 October 2026: with the node rebuilt from main 24f5854 (carrying ce5c6c5, the reliability engineer's PA-10 fix that stops the null chain sealing ahead of the wall clock) the dev chain runs at about 0.7 blocks a second with no reorganisations and no backend errors over the observation; the protocol team will give the PoW dev mode a `--dev.period` minimum interval on its own branch after PA-11.

## Findings handed to other seats

- the protocol team: `--dev` on main up to 235dcde starts the merge-era `SimulatedBeacon` and panics on a nil terminal difficulty (`eth/catalyst/api.go:326`); `--dev.pow` is the working proof-of-work dev mode. (the product team's landing at 72c75e0 changed `--dev`; retest there.)
- the protocol team: `--dev.pow` without `--miner.threads N` commits sealing work and never seals; with `--miner.threads 1` the null dev chain seals about one block a second and occasionally reorganises, which Blockscout reports as `header not found` for a moment. A pacing option for the PoW dev mode (the equivalent of `--dev.period`) would make the explorer and the pool realistic on a dev chain.
- Reliability: the node binary built on the build box needs `GLIBC_2.38`; a Debian bookworm container cannot run it. Reproducible releases should decide static linking or the base image.
- Reliability: a container on the compose network cannot reach the host at `172.17.0.1` or at its own gateway `172.18.0.1` (host firewall); Blockscout's `host.docker.internal` route does not work on the boxes. The dev node therefore ran as a container on the same network until the the build box node arrived; the mesh address `NODE-HOST` is reachable from the containers, which is the route for a shared node.
- Register E-12: Blockscout's tags since 22 April 2026 (v11.3.x, v12) carry the "Blockscout Software Licence" (temporary, revocable, non-transferable, with attribution duties), not GPL. The devnet instance runs under it with attribution kept; the licence under which the public-testnet explorer and the "independent instances" guide are published is the founder's decision before Phase 3.

## Running it

```
git clone --depth 1 --branch v12.0.0 https://github.com/blockscout/blockscout.git
bash explorer/prepare.sh blockscout/docker-compose NODE-HOST 7310 --wipe   # or: devnode 7310, with the private dev-node override
cd blockscout/docker-compose
docker compose -f docker-compose.yml -f igneum-devnode.yml up -d
curl -s http://127.0.0.1/api/v2/stats
```

`igneum-devnode.yml` expects the node binary at `./igneum-work/out/igneum-node` and a data directory at `./igneum-work/work/devnode-docker`; change the two volume lines for another host. For a real network replace the `devnode` service with the operator's own node (archive mode, `debug` in `--http.api`) and pass its host name to `prepare.sh`.

## The page head (15 October 2026, the founder's order through main)

Every public page of the explorer carries its own head: a title of two to six words in a card owner's voice (the home
"The explorer of the test network", a block "Block 11080 of the test network", a transaction "Transaction 0x5c019c…0dc3
on the test network", an address, a token, the lists, the numbers page), one plain description under 155 characters
naming "the public test network", the canonical link, og:type website, og:site_name Igneum, og:url, og:title,
og:description, the explorer's own image (https://igneum.network/og-rebuild/explorer-1200x630.png, 1200 by 630 PNG,
rendered by the site from its template) with its type, width and height, twitter:card summary_large_image with its
title, description and image, and the site's icon set and manifest. The chain is named "the public test network" and
never by an internal name.

A stock Blockscout cannot say this from its settings (its titles are templates on the network name, its favicons are
built into its image, its description and stock image are its own), so the head is written at the explorer's own proxy
with nothing external: the nginx image's njs module runs `proxy/igneum-head.js` as a header and body filter on the
frontend location (`proxy/default.conf.template`); on every HTML answer the vendor's title, description, Open Graph and
Twitter lines, canonical link and favicon links are removed and the set above is written after `<head>`; nothing else
in the answer is touched; an answer that is not HTML passes as it came, and a rewrite that fails is logged and the page
served as it came. The body is held in the request's own variable (`js_var $igneum_held`) until its last chunk, since the
head is not always in the first chunk and the module's scope does not carry state from one chunk's call to the next.
The upstream answers uncompressed (`Accept-Encoding ""`) so the body can be read; the length header is dropped since the
body changes; the host's Caddy compresses outward. The icon set (`static/icons`, the site's files as served) and the
manifest are served by the proxy at `/icons/` and `/site.webmanifest`, the paths the site's own head uses.

Applying it on the explorer host: `proxy/default.conf.template`, `proxy/nginx.conf` (the stock nginx.conf with
`load_module modules/ngx_http_js_module.so`) and `proxy/igneum-head.js` over the checkout's `docker-compose/proxy/`,
`static/` as `docker-compose/proxy/static`, `igneum-proxy.yml` beside the compose file, then, with the same compose files
the stack was created with and nothing but the proxy touched:

```
docker compose -f docker-compose.yml -f igneum-devnode.yml -f igneum-proxy.yml up -d --no-deps proxy
```

Lesson from the apply (15 October 2026, 17:56 by the team's clock): a compose override's relative paths resolve from the
override file's own directory, not from the file it overrides; the first apply mounted `../proxy/nginx.conf` from the
checkout's parent, the proxy failed to start and the explorer answered nothing for twelve seconds until the paths were
corrected to `./proxy/...` and the proxy recreated. The override carries `./proxy/...` now.

Rule from main on the fault (15 October 2026): a proxy change is rehearsed on the staging instance first, with the same
files and the same compose override the live apply will use, never a throwaway container with its own mounts; the live
apply repeats the rehearsed command and nothing else.

The explorer's list for its next cut (not this one): a client-side navigation inside the explorer sets the tab's title
from the vendor's templates until the next full load; the served HTML, which crawlers and share cards read, carries the
head above on every path.
