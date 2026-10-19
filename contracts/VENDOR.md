# Vendored third-party sources (contracts)

Both are git submodules under `contracts/lib/`, pinned to a release tag. The plan names OpenZeppelin (R17) and Foundry
(page 23); `forge-std` is Foundry's own test library and is vendored as part of that toolchain (decision D-B12).

| Library | Tag | Commit | Licence | Used for |
|---|---|---|---|---|
| OpenZeppelin Contracts | v5.7.0 | cab19933c33c2ad1d4c7a84864a3601dddfd16f3 | MIT | SafeERC20, ReentrancyGuard, EIP712, ECDSA, SignatureChecker, IERC1271, ERC721Holder, ERC1155Holder, ERC20, ERC20Permit, ERC721, ERC721URIStorage, ERC1155, ERC1155Supply, ERC2981, AccessControl |
| forge-std | v1.17.0 | f3dae6e6ee381f25eb6a246f7da9b85c91a68219 | MIT or Apache-2.0 | tests only; never deployed |
| account-abstraction (ERC-4337 reference, R18) | v0.7.0 | 7af70c8993a6f42973f520ae0752386a5032abe7 | GPL-3.0 | the EntryPoint and the sample account factory, deployed as they are (decision D-B03); compiled from dev/erc4337 with the upstream settings (solc 0.8.23, via-ir, 1,000,000 runs); v0.8.0 exists and is evaluated after v0.7 has its runs |

Rules:
- A tag moves only by a commit that names the new tag, its commit, the changelog entries read, and the audit record
  consulted (OpenZeppelin publishes one per release line).
- No file under `lib/` is edited. A needed change is a wrapper in `src/`.
- The audit plan (DESIGN.md, section 3.3, item 5) checks the pinned tag against the published audit record before any
  deployment that holds value.

Fetched 9 October 2026 (UK time) from the upstream repositories over HTTPS.
