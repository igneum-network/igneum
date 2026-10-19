# Ember (the Igneum desktop miner), the ember CLI and the worker protocol

Product engineer A's tree. Design: `docs/product-a/DESIGN.md`; the protocol: `docs/product-a/WORKER-PROTOCOL.md`.

```
crates/igneum-worker-proto   the protocol types, framing, bounds, the stub hash (shared by the stub worker and the stub verifier)
crates/igneum-supervisor     devices, the worker manager, modes, the tuner, economics, settings, schedule, pools, journal, verify, state
crates/igneum-worker-stub    a protocol-complete worker with a placeholder hash and a simulated device (no card is touched)
crates/igneum-cli            the command-line miner `ember` (the same supervisor without a window; `igneum-cli` is the same binary under its old name for one release)
app/                         the Tauri desktop miner: src-tauri/ is the host and the commands, src/ the TypeScript interface
```

Builds and tests run on the assigned build box, never on the coordinating Mac. `cargo test --workspace` covers the
pure parts (economics, the tuner's search on a simulated bench, protocol bounds, address checks, tariff windows).

Phase Zero: the hash is not chosen. Everything that depends on it sits behind the worker protocol (`algo`, `seed`,
`epoch`, the `mix` length, the verifier). The stub's algorithm id is `stub-v0` and is labelled "not consensus"
wherever it appears.

Work sources (`work.rs`): a pool over IS/1 (the ecosystem lead's pool specification, `pool/internal/stratum`), a
node over `igneum_getWork` and `igneum_submitWork` (protocol engineer A's mining RPC, with the `eth_` forms as the
fallback), and the local stub. Network sources run on their own thread; TLS is rustls against the public roots.
