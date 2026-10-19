# The registry page (the research chain's registry, read from the chain)

What it is: one page and one reader. The reader (`registry-feed.py`) asks a node of the research chain for
`igneum_vectorRegistry` at `latest`, decodes every landing's document from its transaction's input (the four-byte
selector, then the JSON), and writes `registry.json`; for every pay and lane change the latest document names it reads
the registry back at the change height and at the height before, so the page can say the chain serves each row from
its height. The page (`index.html`) renders that file and re-reads it every twenty seconds; it carries no figure of its
own. The user unit (`registry-feed.service`) runs the reader every thirty seconds on the lab host, which reaches the
node over the mesh.

Where it serves: the lab host's `/lab/registry/` (the lab's directory, build-owned; the lab's publish script leaves
the subdirectory alone), under `noindex`, for the project's audit. The route it goes live at is the project's call; the page is one
file with one JSON beside it and moves with no change.

What the page shows: the head and its age; the registry's key, address, selector, notice and window; the pay list and
the lanes in force; the landings (block, time, transaction hash, block hash, nonce, bytes, gas, status, what the
document added against the one before it); the pay shares by height and the breadth lanes by height, each row checked
against the chain; every workload the registry has held with its activation, retirement, successor and share.

Rules kept: UTC only; no box name, path or local clock on the page; every figure the chain's answer to a named call.

## The public test network's instance (16 October 2026, F-351)

The same reader runs a second time for the public test network (chain 7322) as the template unit `registry-feed@.service`
(`registry-feed@7322`), reading the network's reader node on the mesh and writing `registry.json` into the network's feed
directory, served at `https://build.igneum.network/feed/7322/registry.json` beside the genesis entry `vector.json`: the
registry in force (the coordinates, the pay shares and lanes by height), every landing's document with its height,
transaction and block hashes and what it added, each row checked against the chain at its height. The file is live
(rewritten every thirty seconds), so it stands outside the genesis set's `SHA256SUMS`, as `status.json` does. Nothing in
it is of custody: the registry key's public address, the chain's own transaction inputs and hashes.

