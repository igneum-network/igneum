// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {ERC721} from "@openzeppelin/contracts/token/ERC721/ERC721.sol";

/// @title TestCollectible
/// @notice A test ERC-721 for devnet and testnet: anyone mints. Worthless by design.
contract TestCollectible is ERC721 {
    uint256 public nextId = 1;

    constructor() ERC721("Igneum Test Collectible", "TIGC") {}

    function mint(address to) external returns (uint256 id) {
        id = nextId++;
        _safeMint(to, id);
    }

    function _baseURI() internal pure override returns (string memory) {
        return "https://igneum.network/test-assets/collectible/";
    }
}
