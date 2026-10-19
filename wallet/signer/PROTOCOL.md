# The signer's protocol

`igneum-signer` is the only process that touches a key (docs/product-b/DESIGN.md, 1.3). It reads one JSON request
per line on stdin and writes one JSON response per line on stdout: `{"id": ..., "op": "...", ...}` in,
`{"id": ..., "ok": true, "result": {...}}` or `{"id": ..., "ok": false, "error": "..."}` out. It has no network code
(the TP-16 probe reads the binary for an HTTP client) and no arguments: everything, the vault path included, comes
through the protocol, so the same binary serves the engine, a pool's payout signer, or a test harness.

Environment: `IGNEUM_SIGNER_LIGHT_KDF=1` lowers the vault's Argon2 cost for tests only; never set it for a release.

| Op | Fields | Result | Notes |
|---|---|---|---|
| `status` | | `has_vault`, `unlocked`, `address`, `kind`, `backed_up`, `fresh_phrase_pending`, `legacy` | `legacy` is true while the file at `path` is the previous wallet's (OLD-BASE.md step 3): its address and kind read from the old file in clear |
| `open` | `path` | the status | names the vault file (loaded when it exists); send it first; the path is not an argv. The previous wallet's `vault.json` (address and source in clear, `ciphertext`) is recognised and held as legacy until the first unlock |
| `create` | `password`, `chain_id?` | `address`, `words` (24, shown once) | the words stay held until `confirm_words` |
| `confirm_words` | `checks: [{index, word}]` (three) | the status | marks `backed_up` |
| `import_phrase` | `phrase`, `password`, `chain_id?` | `address` | 12 to 24 words |
| `import_key` | `key` (hex), `password`, `chain_id?` | `address` | |
| `unlock` | `password` | the status (`migrated_from_legacy: true` once, on a migration) | the key stays in the signer's memory until `lock`. On a legacy file: the old vault is opened with the password (Argon2id with its own parameters, XChaCha20-Poly1305, no associated data), the secret (the phrase when the old file carried one, else the key) is re-sealed in this format at this wallet's KDF, the address checked against the old file's first, the new file written and the old one kept beside it as `vault.json.old` |
| `lock` | | the status | zeroes the key |
| `sign_tx` | `transaction`, `from`, `network`, `sentences` | `signature`, `raw`, `hash` | the sentences are recomputed and a mismatch refuses |
| `sign_envelope` | `envelope` (unsigned) | the signed envelope | the same check through the envelope |
| `sign_message` | `message` (hex) | `signature` (65 bytes r, s, v) | EIP-191 personal message |
| `sign_typed` | `typed` (EIP-712 JSON), `lines` | `signature` | the lines are recomputed and a mismatch refuses |
| `reveal` | `password` | `words`, `key` | the password again, every time |
| `verify_password` | `password` | `ok` | the password checked against the vault and nothing else returned: the biometric enrolment parks a password only after this (OLD-BASE.md step 3) |
| `reveal_unlocked` | `host_confirmed: true` | `words` (a phrase vault), `key`, `address` | the biometric path's reveal: from the key already unlocked here (and the phrase kept in a zeroizing buffer while unlocked), only while unlocked and only on a request the engine marks host-confirmed after its gate took a confirmed nonce; the signer trusts the engine's mark as it trusts its sign requests; the page cannot confirm its own challenges |
| `set_backed_up` | | the status | |
| `remove` | `password` | the status | deletes the vault file |
| `recover` | `hash`, `signature` | `address` | no key: who signed a 32-byte hash |
| `verify_envelope` | `unsigned`, `signed` | `ok`, `from`, `hash` | no key: the signed envelope against the unsigned one (`check_against`) |

There is no op that signs a bare hash with the key: `sign_tx`, `sign_envelope`, `sign_message` and `sign_typed` are
the four signing paths, each with the words the person saw. A pool that signs payout transactions uses
`sign_envelope` with the envelope its own code composed, and `verify_envelope` or `recover` to have the result
checked here.
