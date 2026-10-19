# Vendored source: Core-Geth v1.12.23 (Argos)

This directory is an unmodified export of the upstream tag below, imported by protocol engineer A on 9 October 2026 (PA-00 in docs/protocol-a/DESIGN.md). Every Igneum change to it is a later commit titled with its patch-inventory ID; `git log -- node/` is the complete patch set.

| Field | Value |
|---|---|
| Upstream repository | https://github.com/etclabscore/core-geth |
| Tag | `v1.12.23` ("Argos"), annotated and PGP-signed |
| Tag object | `7efa7293bca1d6a1eabad1a45da6822a4452c873` |
| Commit | `96b2afc25c7023bb500d14e8ed69455339b1ca90` (2026-08-14, "params: bump version from v1.12.23-unstable to v1.12.23-stable") |
| Signature | verified "Good signature" with the key published at https://github.com/diega.gpg, RSA fingerprint `FAB47216B1CBF8FA5A19C8DA1688910EC6300A60`, in a throwaway keyring on 9 October 2026; second observation (review F-09): the same key signs the earlier tags v1.12.21 and v1.12.22 (both "Good signature", same fingerprint), so the binding is to the account that has signed the release line since March 2026, not a one-off |
| Upstream version constants | `params/version.go`: 1.12.23 "stable" |
| Licence | GNU LGPL v3.0 for the library (`COPYING.LESSER`), GNU GPL v3.0 for the binaries under `cmd/` (`COPYING`); both files retained unmodified; the upstream `AUTHORS` file retained |
| Module path | `github.com/ethereum/go-ethereum` (unchanged at import; renaming is a later inventory item if needed) |
| Go directive at import | `go 1.21` (out of support; raised by PA-20) |
| Import method | `git archive --format=tar v1.12.23` from a shallow clone of the signed tag, extracted here; the import commit carries `git-subtree-dir: node` and `git-subtree-split: 96b2afc2...` trailers for provenance |
| Not imported | the test-fixture submodules (large; fetched on the build box by pinned commit): `tests/testdata` = https://github.com/ethereum/tests @ `fa51c5c164f79140730ccb8fe26a46c3d3994338`; `tests/testdata-etc` = https://github.com/etclabscore/tests @ `06ec708ea7f3707826142a97a94b61d40c920696`; `tests/evm-benchmarks` = https://github.com/ipsilon/evm-benchmarks @ `849b3e239a28f236dc99574b2e10e0c720895105`. Upstream `.gitmodules` dropped at import for that reason. |

## Deviations from the pristine export
- PA-20 removed the upstream CI and repository metadata so no upstream workflow can run on Igneum's runners (review F-08): `.github/`, `Jenkinsfile`, `appveyor.yml`, `circle.yml`, `.travis.yml`; and the stray `git.diff` (a 24,839-line merge diff in the upstream tag, not code). Everything else is as exported until a patch titled with its inventory ID changes it.

- Three em dashes in the upstream tree replaced with a hyphen for the charter's rule (no em dash in anything committed): a comment in `eth/downloader/queue_test.go`, a comment in `crypto/secp256k1/curve_cve_test.go`, a UI string in `graphql/internal/graphiql/graphiql.min.js` (the bundle goes with PA-18).

## How upstream fixes come in
Not by tracking. A relevant upstream change (a go-ethereum security advisory for a package Igneum keeps, or a core-geth fix) is cherry-picked onto `node/` as its own commit, reviewed and tested like any patch, and recorded in the release notes with its upstream commit id. The advisory check is part of every Igneum release (DESIGN.md section 9).

## Security state at import
See docs/protocol-a/DESIGN.md section 3.2: six CVEs checked file by file at this tag; four present, one partial (PA-21), one absent and closed by PA-12 with PA-22 as the interim; GraphQL depth unbounded (PA-23).

## Re-verifying this import
```
git clone --depth 1 --branch v1.12.23 https://github.com/etclabscore/core-geth.git
curl -sSL https://github.com/diega.gpg | GNUPGHOME=$(mktemp -d) gpg --import
git -C core-geth tag -v v1.12.23          # expect: Good signature, key FAB47216B1CBF8FA5A19C8DA1688910EC6300A60
git -C core-geth archive v1.12.23 | tar -x -C /tmp/cg && diff -r --exclude=VENDOR.md --exclude=.gitmodules /tmp/cg node
```
