# Igneum contracts

Product engineer B's contract set: Igneum Pay, the ownership templates and the test assets. Design and rationale in
`docs/product-b/DESIGN.md` (sections 3, 4 and 5). Written 9 October 2026; not yet compiled or run anywhere (the
coordinating Mac builds nothing; the first `forge build` and `forge test` happen on the assigned box and are recorded
in `docs/product-b/RUNS.md` with the box, the commit and the UK time).

```
src/pay/            IgneumPay, IgneumMultisig, IgneumRecurring, IgneumEscrow, PayTypes (EIP-712 structs)
src/interfaces/     IIgneumPay, IIgneumMultisig, IIgneumRecurring, IIgneumEscrow, IERC5192
src/ownership/      IgneumToken (ERC-20 + permit), IgneumCollectible (ERC-721 + URI + ERC-2981),
                    IgneumItems (ERC-1155 + supply + ERC-2981), IgneumMembership (ERC-721 + ERC-5192 locked)
src/test-assets/    TestToken, TestCollectible, Faucet (devnet and testnet only)
test/               Foundry tests, one file per contract family; mocks under test/mocks
lib/                OpenZeppelin v5.7.0 and forge-std v1.17.0 as pinned submodules (VENDOR.md)
```

## Rules the Pay contracts keep

- No owner, no proxy, no pause, no protocol fee. Anyone deploys another copy; the published addresses are a
  convenience, not an authority.
- Native IGN and any ERC-20 everywhere (`address(0)` is native). Fee-on-transfer and rebasing tokens are not supported
  by Pay and Recurring (the payee would receive less than the record says) and are rejected by Escrow at `open`.
- Funds go to the payee directly. A native payment a payee contract refuses is credited under `claimable` and claimed
  later, so a payee cannot block a payer.
- Every signed object is EIP-712 (`PayTypes`); contract signers are accepted through ERC-1271, so an IgneumMultisig
  can issue a payment request.
- Recurring: the payer sets every bound (amount per period, operator fee per collection, period, start, end, total cap
  that counts both). Anyone collects a due period and receives the fee. A period passes uncollected is forgone.
- Escrow: two templates (`PayeeClaims`, `PayerRefunds`) decide the timeout; a dispute needs a named arbiter and
  freezes the timeout; the arbiter splits by basis points.

## Building and testing (on the box)

```
cd contracts
forge build
forge test -vvv
forge test --match-contract IgneumPayTest
forge coverage
slither . --filter-paths lib
```

`foundry.toml` pins solc 0.8.28 and `evm_version = cancun` (decision D-B01: the protocol team's proposed Igneum EVM-1 has
PUSH0, MCOPY and transient storage and no blob opcodes; OpenZeppelin 5.7 needs MCOPY). `tools/no-blob-builtins.sh`
fails the build if `blobhash` or `blobbasefee` appears under `src/`. `deny = "warnings"`: a warning fails the build.

## Deployment

Not yet. Addresses, chain ids and compiler settings go into `dev/chain/` when the protocol team's devnet exists and the tests
have passed on the box. A re-deployment is a new entry, never an overwrite.
