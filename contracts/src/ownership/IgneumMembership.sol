// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {ERC721} from "@openzeppelin/contracts/token/ERC721/ERC721.sol";
import {AccessControl} from "@openzeppelin/contracts/access/AccessControl.sol";

import {IERC5192} from "../interfaces/IERC5192.sol";

/// @title IgneumMembership
/// @notice A membership or access token: ERC-721 that is locked (non-transferable, ERC-5192) until the issuer unlocks
///         it. The holder can always burn their own token. The issuer holds the issuer role and may renounce it.
contract IgneumMembership is ERC721, AccessControl, IERC5192 {
    bytes32 public constant ISSUER_ROLE = keccak256("ISSUER_ROLE");

    error TokenLocked(uint256 tokenId);
    error NotHolder(uint256 tokenId);

    uint256 public nextId = 1;
    string private _base;
    mapping(uint256 tokenId => bool) private _unlocked;

    constructor(string memory name_, string memory symbol_, string memory baseUri, address issuer)
        ERC721(name_, symbol_)
    {
        _base = baseUri;
        _grantRole(DEFAULT_ADMIN_ROLE, issuer);
        _grantRole(ISSUER_ROLE, issuer);
    }

    function mint(address to) external onlyRole(ISSUER_ROLE) returns (uint256 id) {
        id = nextId++;
        _safeMint(to, id);
        emit Locked(id);
    }

    /// @inheritdoc IERC5192
    function locked(uint256 tokenId) public view override returns (bool) {
        _requireOwned(tokenId);
        return !_unlocked[tokenId];
    }

    function setLocked(uint256 tokenId, bool lock) external onlyRole(ISSUER_ROLE) {
        _requireOwned(tokenId);
        _unlocked[tokenId] = !lock;
        if (lock) emit Locked(tokenId);
        else emit Unlocked(tokenId);
    }

    function burn(uint256 tokenId) external {
        if (_ownerOf(tokenId) != msg.sender) revert NotHolder(tokenId);
        _burn(tokenId);
    }

    function setBaseURI(string calldata baseUri) external onlyRole(DEFAULT_ADMIN_ROLE) {
        _base = baseUri;
    }

    function supportsInterface(bytes4 interfaceId) public view override(ERC721, AccessControl) returns (bool) {
        return interfaceId == type(IERC5192).interfaceId || super.supportsInterface(interfaceId);
    }

    function _baseURI() internal view override returns (string memory) {
        return _base;
    }

    /// @dev A transfer between two holders is refused while the token is locked; mint and burn are not transfers.
    function _update(address to, uint256 tokenId, address auth) internal override returns (address) {
        address from = _ownerOf(tokenId);
        if (from != address(0) && to != address(0) && !_unlocked[tokenId]) revert TokenLocked(tokenId);
        return super._update(to, tokenId, auth);
    }
}
