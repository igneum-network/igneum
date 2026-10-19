// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

/// @title IIgneumWork
/// @notice The Consensus Work Interface (docs/work-api/INTERFACE.md): canonical, reorg-safe summaries of accepted
///         GPU work by payout address over aligned, finalized windows, and the bonded sealer set per epoch. On
///         chain 7320 (the old public test network) the storage behind these views is written by every node at Finalize (the consensus form); on
///         devnet9 a named stand-in writer writes the same slots from tools/work-api/workroot.py (the stand-in form,
///         named by `label()`). Every function is bounded: a fixed number of storage reads and, for `verifyWork`,
///         at most MAX_DEPTH hashes.
interface IIgneumWork {
    /// One window's record (the storage layout of INTERFACE.md section 2: root, totalWork, endHash, then the
    /// packed word entries | depth << 32 | version << 40).
    struct Window {
        bytes32 root;
        uint256 totalWork;
        bytes32 endHash;
        uint32 entries;
        uint8 depth;
        uint8 version;
    }

    /// One epoch's mirror of the registry's table (setRoot, unitsTotal, seed, then keyCount | registryVersion << 32).
    struct Epoch {
        bytes32 setRoot;
        uint256 unitsTotal;
        bytes32 seed;
        uint32 keyCount;
        uint8 registryVersion;
    }

    error NotFinalized(uint64 window);
    error NoWindow();
    error NoEpoch(uint64 epoch);
    error BadProof();
    error UnknownVersion(uint8 version);

    // forge-lint: disable-next-line(mixed-case-function)
    function WINDOW_BLOCKS() external pure returns (uint64);
    // forge-lint: disable-next-line(mixed-case-function)
    function FINALITY_DEPTH() external pure returns (uint64);
    // forge-lint: disable-next-line(mixed-case-function)
    function VERSION() external pure returns (uint8);
    function windowOf(uint64 height) external pure returns (uint64);
    function windowBounds(uint64 w) external pure returns (uint64 start, uint64 end);
    function latestWindow() external view returns (uint64);
    function workRoot(uint64 w) external view returns (bytes32);
    function totalWork(uint64 w) external view returns (uint256);
    function window(uint64 w)
        external
        view
        returns (bytes32 root, uint256 total, bytes32 endHash, uint32 entries, uint8 depth, uint8 version);
    function verifyWork(uint64 w, address account, uint256 weight, uint256 index, bytes32[] calldata siblings)
        external
        view
        returns (bool);
    function bondedUnits(uint64 epoch) external view returns (uint256);
    function sealerSet(uint64 epoch)
        external
        view
        returns (bytes32 setRoot, uint32 keyCount, uint256 unitsTotal, bytes32 seed);
    function epochAndVersion()
        external
        view
        returns (uint64 latestEpoch, uint8 workRootVersion, uint8 registryVersion);
    function label() external view returns (string memory);
}
