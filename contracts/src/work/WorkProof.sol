// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

/// @title WorkProof
/// @notice The work root's byte form (docs/work-api/INTERFACE.md section 3): leaf keccak256(0x00 || address ||
///         uint256 weight), node keccak256(0x01 || left || right), sealer leaf keccak256(0x03 || key || uint256
///         units); the complete binary tree over the leaves in order, padded on the right with the zero word to
///         2^depth. A proof is the depth siblings from the leaf up; the index is the leaf's rank. The same bytes as
///         tools/work-api/workroot.py, which is the reference.
library WorkProof {
    uint8 internal constant MAX_DEPTH = 32;
    bytes32 internal constant EMPTY = bytes32(0);

    function leaf(address account, uint256 weight) internal pure returns (bytes32) {
        return keccak256(abi.encodePacked(uint8(0), account, weight));
    }

    function sealerLeaf(bytes32 key, uint256 units) internal pure returns (bytes32) {
        return keccak256(abi.encodePacked(uint8(3), key, units));
    }

    function node(bytes32 left, bytes32 right) internal pure returns (bytes32) {
        return keccak256(abi.encodePacked(uint8(1), left, right));
    }

    /// Folds a proof of exactly `depth` siblings for the leaf at `index` and compares with `root`. A proof of the
    /// wrong length, an index outside 2^depth or a depth above MAX_DEPTH is false, never a revert.
    function verify(bytes32 root, bytes32 leafHash, uint256 index, bytes32[] calldata siblings, uint8 depth)
        internal
        pure
        returns (bool)
    {
        if (siblings.length != depth || depth > MAX_DEPTH || index >= (uint256(1) << depth)) return false;
        bytes32 n = leafHash;
        for (uint256 i = 0; i < siblings.length; i++) {
            n = (index & 1) == 1 ? node(siblings[i], n) : node(n, siblings[i]);
            index >>= 1;
        }
        return n == root;
    }

    /// The root and depth over leaves in order (a reference for tests and small sets; the node and the tool build
    /// the same tree). An empty set is (EMPTY, 0).
    function rootOf(bytes32[] memory leaves) internal pure returns (bytes32 root, uint8 depth) {
        uint256 n = leaves.length;
        if (n == 0) return (EMPTY, 0);
        while ((uint256(1) << depth) < n) depth++;
        require(depth <= MAX_DEPTH, "WorkProof: too many leaves");
        uint256 size = uint256(1) << depth;
        bytes32[] memory level = new bytes32[](size);
        for (uint256 i = 0; i < n; i++) level[i] = leaves[i];
        while (size > 1) {
            size >>= 1;
            for (uint256 i = 0; i < size; i++) level[i] = node(level[2 * i], level[2 * i + 1]);
        }
        return (level[0], depth);
    }

    /// The siblings of the leaf at `index` (a reference for tests).
    function proofOf(bytes32[] memory leaves, uint256 index) internal pure returns (bytes32[] memory siblings) {
        uint256 n = leaves.length;
        uint8 depth = 0;
        while ((uint256(1) << depth) < n) depth++;
        uint256 size = uint256(1) << depth;
        bytes32[] memory level = new bytes32[](size);
        for (uint256 i = 0; i < n; i++) level[i] = leaves[i];
        siblings = new bytes32[](depth);
        for (uint256 d = 0; d < depth; d++) {
            siblings[d] = level[index ^ 1];
            index >>= 1;
            size >>= 1;
            for (uint256 i = 0; i < size; i++) level[i] = node(level[2 * i], level[2 * i + 1]);
        }
    }
}
