// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {Test} from "forge-std/Test.sol";

import {IIgneumWork} from "../src/work/IIgneumWork.sol";
import {IgneumWork} from "../src/work/IgneumWork.sol";
import {WorkProof} from "../src/work/WorkProof.sol";

/// The system contract and the proof library against the reference tool's bytes: the devnet8 record of heights
/// 1 to 40 read at 00:24 UK on 12 October 2026 from two nodes (docs/work-api/records/devnet8-range-1-40-*.json,
/// tools/work-api/workroot.py sha256 188750af...): 8 entries, total 7,359, depth 3, root 0x8809c218...; the proof
/// for index 3 (0x...0c7c36, weight 1,427) with its three siblings. The Solidity tree must give the same root from
/// the same entries, and the tool's proof must verify here, or the two implementations disagree.
contract IgneumWorkTest is Test {
    bytes32 internal constant RECORD_ROOT = 0x8809c218fe9c2d79adbfbc64c4f4876fe320b1f62ef94fd71e29687c05b5ca59;
    bytes32 internal constant RECORD_END_HASH = bytes32(uint256(0x49e57b84e1d47696));

    IgneumWork internal work;
    address internal writer = makeAddr("stand-in writer");
    address[8] internal addrs;
    uint256[8] internal weights;

    function setUp() public {
        work = new IgneumWork(writer);
        addrs[0] = 0x000000000000000000000000000000000000c8d1;
        addrs[1] = 0x000000000000000000000000000000000000c8e2;
        addrs[2] = 0x00000000000000000000000000000000000c7C35;
        addrs[3] = 0x00000000000000000000000000000000000C7C36;
        addrs[4] = 0x00000000000000000000000000000000000c7C37;
        addrs[5] = 0x00000000000000000000000000000000000C7C38;
        addrs[6] = 0x5EcF000000000000000000000000000000000001;
        addrs[7] = 0xF4b0Ae5D45684b56351Cc06B0420056668d1b801;
        weights = [uint256(637), 469, 1427, 1427, 1518, 1528, 2, 351];
        vm.roll(1_000);
    }

    function _leaves() internal view returns (bytes32[] memory leaves) {
        leaves = new bytes32[](8);
        for (uint256 i = 0; i < 8; i++) leaves[i] = WorkProof.leaf(addrs[i], weights[i]);
    }

    function _recordSiblings() internal pure returns (bytes32[] memory s) {
        s = new bytes32[](3);
        s[0] = 0x94b2d0a85af90b84b6759269d34ff2cffda0f3763526c04c4b71bc146acd9f8a;
        s[1] = 0xd39a3d524a814c8bd8a135247f5344fb0c80428fe4ac6167a9ef1b8a5181e809;
        s[2] = 0x3365aee43d55c4e53d8b8e3ad6e29f50e6105bd349eeeb71a977a9059d7a4b69;
    }

    function _writeRecord() internal {
        vm.prank(writer);
        work.setWindow(0, RECORD_ROOT, 7359, RECORD_END_HASH, 8, 3);
    }

    function test_tree_reproduces_the_tool_root() public view {
        (bytes32 root, uint8 depth) = WorkProof.rootOf(_leaves());
        assertEq(root, RECORD_ROOT, "the Solidity tree and the tool disagree on the root");
        assertEq(depth, 3);
    }

    function test_tool_proof_verifies_here() public {
        _writeRecord();
        assertTrue(work.verifyWork(0, addrs[3], 1427, 3, _recordSiblings()));
        assertEq(work.workRoot(0), RECORD_ROOT);
        assertEq(work.totalWork(0), 7359);
        assertEq(work.latestWindow(), 0);
    }

    function test_tampered_weight_index_or_address_refused() public {
        _writeRecord();
        bytes32[] memory s = _recordSiblings();
        assertFalse(work.verifyWork(0, addrs[3], 1428, 3, s));
        assertFalse(work.verifyWork(0, addrs[3], 1427, 2, s));
        assertFalse(work.verifyWork(0, addrs[2], 1427, 3, s));
        bytes32[] memory wrongLength = new bytes32[](2);
        vm.expectRevert(IIgneumWork.BadProof.selector);
        work.verifyWork(0, addrs[3], 1427, 3, wrongLength);
    }

    function test_every_entry_has_a_proof() public {
        _writeRecord();
        bytes32[] memory leaves = _leaves();
        for (uint256 i = 0; i < 8; i++) {
            bytes32[] memory s = WorkProof.proofOf(leaves, i);
            assertTrue(work.verifyWork(0, addrs[i], weights[i], i, s));
            assertFalse(work.verifyWork(0, addrs[i], weights[i] + 1, i, s));
        }
    }

    function test_unwritten_window_reverts_not_answers() public {
        vm.expectRevert(abi.encodeWithSelector(IIgneumWork.NotFinalized.selector, uint64(0)));
        work.workRoot(0);
        vm.expectRevert(IIgneumWork.NoWindow.selector);
        work.latestWindow();
        vm.expectRevert(abi.encodeWithSelector(IIgneumWork.NoEpoch.selector, uint64(0)));
        work.bondedUnits(0);
    }

    function test_stand_in_refuses_unfinalized_overwrite_and_strangers() public {
        // window 2 ends at 1079; written only from head 1143
        vm.roll(1_142);
        vm.prank(writer);
        vm.expectRevert(abi.encodeWithSelector(IgneumWork.NotYetFinal.selector, uint64(2), uint64(1_142)));
        work.setWindow(2, RECORD_ROOT, 7359, RECORD_END_HASH, 8, 3);
        vm.roll(1_143);
        vm.prank(writer);
        work.setWindow(2, RECORD_ROOT, 7359, RECORD_END_HASH, 8, 3);
        vm.prank(writer);
        vm.expectRevert(abi.encodeWithSelector(IgneumWork.AlreadyWritten.selector, uint64(2)));
        work.setWindow(2, bytes32(uint256(1)), 1, RECORD_END_HASH, 1, 0);
        vm.expectRevert(abi.encodeWithSelector(IgneumWork.NotWriter.selector, address(this)));
        work.setWindow(3, RECORD_ROOT, 7359, RECORD_END_HASH, 8, 3);
        assertEq(work.latestWindow(), 2);
    }

    function test_consensus_form_has_no_writer() public {
        IgneumWork consensus = new IgneumWork(address(0));
        assertEq(consensus.label(), "consensus");
        vm.expectRevert(abi.encodeWithSelector(IgneumWork.NotWriter.selector, address(this)));
        consensus.setWindow(0, RECORD_ROOT, 7359, RECORD_END_HASH, 8, 3);
        assertTrue(bytes(work.label()).length > 9);
    }

    function test_bounds_and_constants() public view {
        assertEq(work.WINDOW_BLOCKS(), 360);
        assertEq(work.FINALITY_DEPTH(), 64);
        assertEq(work.VERSION(), 1);
        assertEq(work.windowOf(0), 0);
        assertEq(work.windowOf(359), 0);
        assertEq(work.windowOf(360), 1);
        (uint64 s, uint64 e) = work.windowBounds(7);
        assertEq(s, 2520);
        assertEq(e, 2879);
    }

    function test_empty_window_is_written_with_zero_root() public {
        vm.prank(writer);
        work.setWindow(0, bytes32(0), 0, RECORD_END_HASH, 0, 0);
        assertEq(work.workRoot(0), bytes32(0));
        bytes32[] memory none = new bytes32[](0);
        assertFalse(work.verifyWork(0, addrs[0], 1, 0, none));
    }

    function test_epoch_mirror() public {
        vm.prank(writer);
        work.setEpoch(1, bytes32(uint256(7)), 3, 9362, bytes32(uint256(11)), 4);
        assertEq(work.bondedUnits(1), 9362);
        (bytes32 r, uint32 k, uint256 u, bytes32 seed) = work.sealerSet(1);
        assertEq(r, bytes32(uint256(7)));
        assertEq(k, 3);
        assertEq(u, 9362);
        assertEq(seed, bytes32(uint256(11)));
        (uint64 le, uint8 wv, uint8 rv) = work.epochAndVersion();
        assertEq(le, 1);
        assertEq(wv, 1);
        assertEq(rv, 4);
    }

    /// The storage layout the node writes to (INTERFACE.md section 2): window w's record at
    /// keccak256(abi.encode(w, 0)) + 0..3 with the packed word entries | depth << 32 | version << 40.
    function test_storage_layout_matches_the_specification() public {
        _writeRecord();
        bytes32 k = keccak256(abi.encode(uint256(0), uint256(0)));
        assertEq(vm.load(address(work), k), RECORD_ROOT);
        assertEq(uint256(vm.load(address(work), bytes32(uint256(k) + 1))), 7359);
        assertEq(vm.load(address(work), bytes32(uint256(k) + 2)), RECORD_END_HASH);
        uint256 packed = uint256(8) | (uint256(3) << 32) | (uint256(1) << 40);
        assertEq(uint256(vm.load(address(work), bytes32(uint256(k) + 3))), packed);
        assertEq(uint256(vm.load(address(work), bytes32(uint256(2)))), 0);
    }

    function testFuzz_fold_rejects_wrong_index(uint256 index) public {
        _writeRecord();
        index = bound(index, 0, 7);
        bytes32[] memory s = _recordSiblings();
        assertEq(work.verifyWork(0, addrs[3], 1427, index, s), index == 3);
    }
}
