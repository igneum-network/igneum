// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {ERC721} from "@openzeppelin/contracts/token/ERC721/ERC721.sol";
import {ERC721URIStorage} from "@openzeppelin/contracts/token/ERC721/extensions/ERC721URIStorage.sol";
import {ERC2981} from "@openzeppelin/contracts/token/common/ERC2981.sol";
import {AccessControl} from "@openzeppelin/contracts/access/AccessControl.sol";

/// @title IgneumCollectible
/// @notice A collectible, ticket or digital item: ERC-721 with per-token metadata (ERC-721 URI storage, ERC-4906
///         update events) and ERC-2981 royalties. The issuer holds the minter role and may renounce it.
/// @dev The chain records and enforces the token's own rules. It does not make an off-chain issuer honour a ticket
///      or a legal right; wallets say so under every asset.
contract IgneumCollectible is ERC721URIStorage, ERC2981, AccessControl {
    bytes32 public constant MINTER_ROLE = keccak256("MINTER_ROLE");

    uint256 public nextId = 1;

    constructor(
        string memory name_,
        string memory symbol_,
        address issuer,
        address royaltyReceiver,
        uint96 royaltyBps
    ) ERC721(name_, symbol_) {
        _grantRole(DEFAULT_ADMIN_ROLE, issuer);
        _grantRole(MINTER_ROLE, issuer);
        if (royaltyReceiver != address(0)) _setDefaultRoyalty(royaltyReceiver, royaltyBps);
    }

    function mint(address to, string calldata uri) external onlyRole(MINTER_ROLE) returns (uint256 id) {
        id = nextId++;
        _safeMint(to, id);
        _setTokenURI(id, uri);
    }

    function setTokenRoyalty(uint256 id, address receiver, uint96 bps) external onlyRole(DEFAULT_ADMIN_ROLE) {
        _setTokenRoyalty(id, receiver, bps);
    }

    function burn(uint256 id) external {
        if (_ownerOf(id) != msg.sender) revert ERC721InsufficientApproval(msg.sender, id);
        _burn(id);
    }

    function supportsInterface(bytes4 interfaceId)
        public
        view
        override(ERC721URIStorage, ERC2981, AccessControl)
        returns (bool)
    {
        return super.supportsInterface(interfaceId);
    }
}
