// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {IIgneumWork} from "./IIgneumWork.sol";
import {WorkProof} from "./WorkProof.sol";

/// @title IgneumWork
/// @notice The Consensus Work Interface's system contract (docs/work-api/INTERFACE.md section 2). Two forms of one
///         code: the CONSENSUS form (writer == address(0)), deployed in the genesis of chain 7320 (the old public test network) at the fixed address,
///         whose storage every node writes at Finalize and which has no function that writes; and the STAND-IN form
///         (writer != address(0)) on devnet9, where one named key writes the same slots from the reference tool and
///         `label()` says so. The storage layout is the specification's: slot 0 the windows mapping, slot 1 the
///         epochs mapping, slot 2 the latest window, slot 3 the latest epoch.
contract IgneumWork is IIgneumWork {
    uint64 private constant _WINDOW_BLOCKS = 360;
    uint64 private constant _FINALITY_DEPTH = 64;
    uint8 private constant _VERSION = 1;

    mapping(uint64 => Window) private _windows; // slot 0
    mapping(uint64 => Epoch) private _epochs; // slot 1
    uint256 private _latestWindow; // slot 2
    uint256 private _latestEpoch; // slot 3

    /// The stand-in writer; address(0) in the consensus form.
    address public immutable writer;

    error NotWriter(address caller);
    error AlreadyWritten(uint64 number);
    error NotYetFinal(uint64 window, uint64 head);
    error BadRecord();

    event WindowWritten(uint64 indexed window, bytes32 root, uint256 totalWork, uint32 entries, uint8 depth, bytes32 endHash);
    event EpochWritten(uint64 indexed epoch, bytes32 setRoot, uint32 keyCount, uint256 unitsTotal, bytes32 seed);

    // address(0) is the consensus form (no writer at all), so a zero check would refuse the form that matters
    // forge-lint: disable-next-line(missing-zero-check)
    constructor(address writer_) {
        writer = writer_;
    }

    // ------------------------------------------------------------------ constants

    // forge-lint: disable-next-line(mixed-case-function)
    function WINDOW_BLOCKS() external pure override returns (uint64) {
        return _WINDOW_BLOCKS;
    }

    // forge-lint: disable-next-line(mixed-case-function)
    function FINALITY_DEPTH() external pure override returns (uint64) {
        return _FINALITY_DEPTH;
    }

    // forge-lint: disable-next-line(mixed-case-function)
    function VERSION() external pure override returns (uint8) {
        return _VERSION;
    }

    function windowOf(uint64 height) public pure override returns (uint64) {
        return height / _WINDOW_BLOCKS;
    }

    function windowBounds(uint64 w) public pure override returns (uint64 start, uint64 end) {
        start = w * _WINDOW_BLOCKS;
        end = start + _WINDOW_BLOCKS - 1;
    }

    function label() external view override returns (string memory) {
        if (writer == address(0)) return "consensus";
        return
            "stand-in: roots written by a named key from tools/work-api/workroot.py; verifiable against the RPC; development-chain work; no monetary value";
    }

    // ------------------------------------------------------------------ windows

    function latestWindow() external view override returns (uint64) {
        // the word is only ever written from a uint64 (setWindow, or the node under the specification)
        // forge-lint: disable-next-line(unsafe-typecast)
        uint64 w = uint64(_latestWindow);
        if (_windows[w].endHash == bytes32(0)) revert NoWindow();
        return w;
    }

    function workRoot(uint64 w) external view override returns (bytes32) {
        return _written(w).root;
    }

    function totalWork(uint64 w) external view override returns (uint256) {
        return _written(w).totalWork;
    }

    function window(uint64 w)
        external
        view
        override
        returns (bytes32 root, uint256 total, bytes32 endHash, uint32 entries, uint8 depth, uint8 version)
    {
        Window storage wd = _written(w);
        return (wd.root, wd.totalWork, wd.endHash, wd.entries, wd.depth, wd.version);
    }

    /// True iff (account, weight) is the leaf at `index` under window w's root. Reverts for an unwritten window
    /// and for a proof whose length is not the window's depth; an unknown version byte reverts rather than folds.
    function verifyWork(uint64 w, address account, uint256 weight, uint256 index, bytes32[] calldata siblings)
        external
        view
        override
        returns (bool)
    {
        Window storage wd = _written(w);
        if (wd.version != _VERSION) revert UnknownVersion(wd.version);
        if (siblings.length != wd.depth) revert BadProof();
        return WorkProof.verify(wd.root, WorkProof.leaf(account, weight), index, siblings, wd.depth);
    }

    // ------------------------------------------------------------------ epochs

    function bondedUnits(uint64 epoch) external view override returns (uint256) {
        return _epoch(epoch).unitsTotal;
    }

    function sealerSet(uint64 epoch)
        external
        view
        override
        returns (bytes32 setRoot, uint32 keyCount, uint256 unitsTotal, bytes32 seed)
    {
        Epoch storage e = _epoch(epoch);
        return (e.setRoot, e.keyCount, e.unitsTotal, e.seed);
    }

    function epochAndVersion()
        external
        view
        override
        returns (uint64 latestEpoch, uint8 workRootVersion, uint8 registryVersion)
    {
        // the word is only ever written from a uint64 (setEpoch, or the node under the specification)
        // forge-lint: disable-next-line(unsafe-typecast)
        latestEpoch = uint64(_latestEpoch);
        return (latestEpoch, _VERSION, _epochs[latestEpoch].registryVersion);
    }

    // ------------------------------------------------------------------ the stand-in's writes (devnet9 only)

    /// Writes a window's record. The stand-in form only (the consensus form has writer == address(0) and refuses
    /// every caller); a written window is never overwritten; a window whose end is not FINALITY_DEPTH below the
    /// head is refused, so the stand-in cannot represent unfinalized data as stable.
    function setWindow(uint64 w, bytes32 root, uint256 total, bytes32 endHash, uint32 entries, uint8 depth)
        external
    {
        _onlyWriter();
        if (_windows[w].endHash != bytes32(0)) revert AlreadyWritten(w);
        (, uint64 end) = windowBounds(w);
        // a chain height fits a uint64 for longer than any chain runs
        // forge-lint: disable-next-line(unsafe-typecast)
        if (uint256(end) + _FINALITY_DEPTH > block.number) revert NotYetFinal(w, uint64(block.number));
        if (endHash == bytes32(0) || depth > WorkProof.MAX_DEPTH || (entries == 0) != (root == bytes32(0))) {
            revert BadRecord();
        }
        if (entries > (uint256(1) << depth) || (entries == 0) != (total == 0)) revert BadRecord();
        _windows[w] = Window({root: root, totalWork: total, endHash: endHash, entries: entries, depth: depth, version: _VERSION});
        if (w >= _latestWindow) _latestWindow = w;
        emit WindowWritten(w, root, total, entries, depth, endHash);
    }

    /// Writes an epoch's mirror of the registry's table. The stand-in form only; never overwritten.
    function setEpoch(uint64 epoch, bytes32 setRoot, uint32 keyCount, uint256 unitsTotal, bytes32 seed, uint8 registryVersion)
        external
    {
        _onlyWriter();
        if (_epochs[epoch].registryVersion != 0) revert AlreadyWritten(epoch);
        if (registryVersion == 0) revert BadRecord();
        _epochs[epoch] = Epoch({setRoot: setRoot, unitsTotal: unitsTotal, seed: seed, keyCount: keyCount, registryVersion: registryVersion});
        if (epoch >= _latestEpoch) _latestEpoch = epoch;
        emit EpochWritten(epoch, setRoot, keyCount, unitsTotal, seed);
    }

    // ------------------------------------------------------------------ internal

    function _written(uint64 w) private view returns (Window storage wd) {
        wd = _windows[w];
        if (wd.endHash == bytes32(0)) revert NotFinalized(w);
    }

    function _epoch(uint64 epoch) private view returns (Epoch storage e) {
        e = _epochs[epoch];
        if (e.registryVersion == 0) revert NoEpoch(epoch);
    }

    function _onlyWriter() private view {
        if (writer == address(0) || msg.sender != writer) revert NotWriter(msg.sender);
    }
}
