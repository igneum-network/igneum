// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

/// @title IERC5192: minimal soulbound (locked) tokens
/// @dev Interface id 0xb45a3c0e. A locked token cannot be transferred by anyone; it can still be burned by its owner.
interface IERC5192 {
    event Locked(uint256 tokenId);
    event Unlocked(uint256 tokenId);

    function locked(uint256 tokenId) external view returns (bool);
}
