# Ember for Linux

One archive: `bin/ember` (the miner), `workers/` (the worker for each card kind, checked against `workers.sha256`),
`install.sh`, `run.sh`, `uninstall.sh`, `ember.service`, `hiveos/` (the HiveOS custom-miner package's files).

Install, as the user who will mine (root is fine on a machine with no other user; nothing is elevated, except that a user with promptless sudo gets a root-owned validating helper for the power-cap call alone (/usr/local/sbin/ember-powercap <card> <watts>) and its sudoers line with no free arguments, so Ember applies caps itself):

    tar xzf ember-linux-x86_64-<version>.tar.gz && cd ember-linux-x86_64-<version>
    ./install.sh --pool pool.igneum.network:3341 --wallet 0xYourAddress [--backup pool2.example:3334] [--tariff 0.28 --currency GBP]

It writes one config file, `$IGNEUM_SECRETS_DIR/ember/ember.conf` (the keys are the `ember mine` flags: `pool`, `pool_backup`,
`node`, `address`, `worker_name`, `no_tls = true` for a pool whose port is plain (Ember reads the pool's own document and
falls back to plain by itself from 0.2.5), `dev_fee = 0` to switch the dev fee off, `power_cap = 100` for a cap in watts
applied through the driver and read back, `tariff`, `currency`, `device`), registers `ember.service`
under `systemd --user` (or a supervised runner where there is no user session) and starts mining. The state line is in
the journal every 30 s and at `http://127.0.0.1:4068/metrics`. Edit the config and restart the service to change
anything. `~/.local/opt/ember/uninstall.sh` removes it.

Fees, plainly: Ember carries a dev fee of 1 percent, read from the signed `workers/dev-fee-manifest.json` beside the
workers (one template in a hundred is mined for the dev payout address; on a pool session, Ember's own second session
under that address); the state line names it. `--dev-fee 0` turns it off. On the project's pool the handshake declares
it; the pool's own declared fee (1 percent, read from its document at /.well-known/igneum-pool.json) applies to every
session, the declaring one too (the founder's decision of 13 October 2026: the dev fee is the miner's own, never the
pool's to waive), so the two together are 2 percent of the paid work. The protocol fund is 1 percent
of every block by the chain's consensus rule, shown as read from the chain per block; it is not a fee of this software
and not optional.
