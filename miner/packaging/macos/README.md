# Ember for macOS (Apple silicon)

One archive: `bin/ember` (the miner, arm64), `workers/igneum-worker-metal` (the Metal worker, checked against
`workers.sha256`), `install.sh`, `run.sh`, `uninstall.sh`, `network.igneum.ember.plist` (the launchd agent).

Install, as the user who will mine, no administrator password:

    tar xzf ember-macos-arm64-<version>.tar.gz && cd ember-macos-arm64-<version>
    ./install.sh --pool pool.example:3334 --wallet 0xYourAddress [--backup pool2.example:3334] [--tariff 0.28 --currency GBP]

It writes one config file, `$IGNEUM_SECRETS_DIR/ember/ember.conf` (the keys are the `ember mine` flags), registers the launchd
agent `network.igneum.ember` in `~/Library/LaunchAgents` (KeepAlive, back at every login) and starts mining. The state
line is in `~/.local/state/ember/ember.log` every 30 s and at `http://127.0.0.1:4068/metrics`. Edit the config and
`~/.local/opt/ember/run.sh restart` to change anything. `~/.local/opt/ember/uninstall.sh` removes it.

What the Mac does by itself: the worker pauses on battery power and under memory pressure (the dataset is released
and rebuilt when the pressure clears), follows the thermal state as a duty, and holds one dataset of 2 GiB in unified
memory (a 16 GB Mac keeps the 2 GiB class; the next epoch's dataset is built beside it when memory allows).

Fees, plainly: Ember carries a dev fee of 1 percent, read from the signed `workers/dev-fee-manifest.json` beside the
workers (one template in a hundred is mined for the dev payout address; on a pool session, Ember's own second session
under that address); the state line names it. `--dev-fee 0` turns it off. On the project's pool the handshake declares
it; the pool's own declared fee (1 percent, read from its document at /.well-known/igneum-pool.json) applies to every
session, the declaring one too (the founder's decision of 13 October 2026: the dev fee is the miner's own, never the
pool's to waive), so the two together are 2 percent of the paid work. The protocol fund is 1 percent
of every block by the chain's consensus rule, shown as read from the chain per block; it is not a fee of this software
and not optional.

The binaries in this archive are not yet signed or notarised (the founder's signing step comes after the candidate's
go); `install.sh` clears the download quarantine flag on the files it copies and nothing else.
