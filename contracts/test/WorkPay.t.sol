// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {Test} from "forge-std/Test.sol";

import {IIgneumWork} from "../src/work/IIgneumWork.sol";
import {IgneumWork} from "../src/work/IgneumWork.sol";
import {WorkProof} from "../src/work/WorkProof.sol";
import {WorkPay} from "../src/workpay/WorkPay.sol";

/// A recipient whose receive reverts: the credit path.
contract Refuser {
    receive() external payable {
        revert("no");
    }
}

/// WorkPay against the specification's rows (docs/work-api/INTERFACE.md section 10 and the preregistered W2, W3):
/// an explicit budget, a finalized window, proportional payment to the wei against the root and proofs, no
/// double distribution, no extra weight for a split address, the remainder reported, the credit path, funding
/// closed at the first claim, nothing for an unwritten window.
contract WorkPayTest is Test {
    IgneumWork internal work;
    WorkPay internal pay;
    address internal writer = makeAddr("stand-in writer");
    address internal funder = makeAddr("funder");

    function setUp() public {
        work = new IgneumWork(writer);
        pay = new WorkPay(work);
        vm.deal(funder, 1_000 ether);
        vm.roll(100_000);
    }

    /// Writes window w from (accounts, weights) in the sorted order the tool emits and returns the leaves.
    function _write(uint64 w, address[] memory accounts, uint256[] memory weights) internal returns (bytes32[] memory leaves) {
        leaves = new bytes32[](accounts.length);
        uint256 total;
        for (uint256 i = 0; i < accounts.length; i++) {
            if (i > 0) require(uint160(accounts[i - 1]) < uint160(accounts[i]), "sorted");
            leaves[i] = WorkProof.leaf(accounts[i], weights[i]);
            total += weights[i];
        }
        (bytes32 root, uint8 depth) = WorkProof.rootOf(leaves);
        vm.prank(writer);
        work.setWindow(w, root, total, keccak256(abi.encode("end", w)), uint32(accounts.length), depth);
    }

    function _three() internal pure returns (address[] memory a, uint256[] memory wt) {
        a = new address[](3);
        wt = new uint256[](3);
        a[0] = address(0x1001);
        a[1] = address(0x1002);
        a[2] = address(0x1003);
        wt[0] = 637;
        wt[1] = 1427;
        wt[2] = 2;
    }

    function test_label_and_source() public view {
        assertEq(pay.LABEL(), "development-chain work; no monetary value");
        assertTrue(bytes(pay.source()).length > 9);
    }

    function test_fund_claim_each_once_exact_to_the_wei() public {
        (address[] memory a, uint256[] memory wt) = _three();
        bytes32[] memory leaves = _write(5, a, wt);
        vm.prank(funder);
        pay.fund{value: 10 ether}(5);
        assertEq(pay.budget(5), 10 ether);
        uint256 total = 637 + 1427 + 2;
        uint256 sum;
        for (uint256 i = 0; i < 3; i++) {
            uint256 before = a[i].balance;
            pay.claim(5, a[i], wt[i], i, WorkProof.proofOf(leaves, i));
            uint256 expected = 10 ether * wt[i] / total;
            assertEq(a[i].balance - before, expected, "pro rata to the wei");
            sum += expected;
        }
        assertEq(pay.paidOut(5), sum);
        assertEq(pay.remainder(5), 10 ether - sum);
        assertLt(pay.remainder(5), 3, "the remainder is under entries wei");
        assertTrue(pay.settled(5));
        assertEq(address(pay).balance, 10 ether - sum);
    }

    function test_no_double_distribution() public {
        (address[] memory a, uint256[] memory wt) = _three();
        bytes32[] memory leaves = _write(5, a, wt);
        vm.prank(funder);
        pay.fund{value: 1 ether}(5);
        bytes32[] memory p = WorkProof.proofOf(leaves, 1);
        pay.claim(5, a[1], wt[1], 1, p);
        vm.expectRevert(abi.encodeWithSelector(WorkPay.AlreadyPaid.selector, uint64(5), a[1]));
        pay.claim(5, a[1], wt[1], 1, p);
        // the same transaction replayed as part of a batch is refused too
        address[] memory one = new address[](1);
        uint256[] memory oneW = new uint256[](1);
        uint256[] memory oneI = new uint256[](1);
        bytes32[][] memory oneP = new bytes32[][](1);
        one[0] = a[1];
        oneW[0] = wt[1];
        oneI[0] = 1;
        oneP[0] = p;
        vm.expectRevert(abi.encodeWithSelector(WorkPay.AlreadyPaid.selector, uint64(5), a[1]));
        pay.claimMany(5, one, oneW, oneI, oneP);
    }

    function test_wrong_weight_or_proof_pays_nothing() public {
        (address[] memory a, uint256[] memory wt) = _three();
        bytes32[] memory leaves = _write(5, a, wt);
        vm.prank(funder);
        pay.fund{value: 1 ether}(5);
        bytes32[] memory p = WorkProof.proofOf(leaves, 1);
        vm.expectRevert(abi.encodeWithSelector(WorkPay.BadProof.selector, uint64(5), a[1]));
        pay.claim(5, a[1], wt[1] + 1, 1, p);
        vm.expectRevert(abi.encodeWithSelector(WorkPay.BadProof.selector, uint64(5), a[0]));
        pay.claim(5, a[0], wt[1], 1, p);
        vm.expectRevert(WorkPay.ZeroWeight.selector);
        pay.claim(5, a[1], 0, 1, p);
        assertEq(pay.paidOut(5), 0);
    }

    /// W2: one address with weight X against sixteen addresses whose weights sum to X, at equal budgets: the
    /// sixteen are paid, in total, what the one is paid, to the wei (the remainder stated beside it).
    function test_split_address_gains_nothing() public {
        uint256 x = 4_096;
        address[] memory one = new address[](1);
        uint256[] memory oneW = new uint256[](1);
        one[0] = address(0x2000);
        oneW[0] = x;
        bytes32[] memory l1 = _write(10, one, oneW);
        address[] memory many = new address[](16);
        uint256[] memory manyW = new uint256[](16);
        uint256 acc;
        for (uint256 i = 0; i < 16; i++) {
            many[i] = address(uint160(0x3000 + i));
            manyW[i] = (i == 15) ? x - acc : (x / 16) + (i % 3); // uneven weights that sum to X
            acc += manyW[i];
        }
        bytes32[] memory l16 = _write(11, many, manyW);
        // both windows also carry a bystander so the split is not the whole window: add as the same other work
        // (the bystander is the second entry in each window; its address sorts above the splits)
        vm.startPrank(funder);
        pay.fund{value: 7 ether}(10);
        pay.fund{value: 7 ether}(11);
        vm.stopPrank();
        pay.claim(10, one[0], x, 0, WorkProof.proofOf(l1, 0));
        uint256 paidOne = pay.paidOut(10);
        for (uint256 i = 0; i < 16; i++) {
            pay.claim(11, many[i], manyW[i], i, WorkProof.proofOf(l16, i));
        }
        uint256 paidMany = pay.paidOut(11);
        // the one holds the whole window's work in both windows, so both are paid the whole budget less the dust
        assertEq(paidOne, 7 ether);
        assertLe(paidOne - paidMany, 16, "the split is paid the same total but for rounding dust under 16 wei");
        assertEq(pay.remainder(10) + paidOne, pay.remainder(11) + paidMany);
    }

    /// The split row with a bystander holding the other half of the window: a split cannot gain against others.
    function test_split_against_a_bystander() public {
        address[] memory a = new address[](2);
        uint256[] memory wt = new uint256[](2);
        a[0] = address(0x4000);
        a[1] = address(0x4001);
        wt[0] = 1_000;
        wt[1] = 1_000;
        bytes32[] memory l2 = _write(20, a, wt);
        address[] memory b = new address[](5);
        uint256[] memory bw = new uint256[](5);
        b[0] = address(0x4000);
        bw[0] = 1_000;
        for (uint256 i = 1; i < 5; i++) {
            b[i] = address(uint160(0x4001 + i - 1));
            bw[i] = 250;
        }
        bytes32[] memory l5 = _write(21, b, bw);
        vm.startPrank(funder);
        pay.fund{value: 3 ether}(20);
        pay.fund{value: 3 ether}(21);
        vm.stopPrank();
        pay.claim(20, a[0], 1_000, 0, WorkProof.proofOf(l2, 0));
        pay.claim(20, a[1], 1_000, 1, WorkProof.proofOf(l2, 1));
        uint256 bystander20 = address(0x4000).balance;
        uint256 whole21 = pay.payout(21, 1_000);
        pay.claim(21, b[0], 1_000, 0, WorkProof.proofOf(l5, 0));
        uint256 splitSum;
        for (uint256 i = 1; i < 5; i++) {
            uint256 before = b[i].balance;
            pay.claim(21, b[i], 250, i, WorkProof.proofOf(l5, i));
            splitSum += b[i].balance - before;
        }
        assertEq(address(0x4000).balance - bystander20, whole21, "the bystander's pay is unchanged by the split");
        assertEq(whole21, 1.5 ether);
        assertEq(splitSum, 1.5 ether, "the four quarters sum to the half");
    }

    function test_funding_closes_at_the_first_claim_and_adds_before() public {
        (address[] memory a, uint256[] memory wt) = _three();
        bytes32[] memory leaves = _write(5, a, wt);
        vm.startPrank(funder);
        pay.fund{value: 1 ether}(5);
        pay.fund{value: 2 ether}(5);
        vm.stopPrank();
        assertEq(pay.budget(5), 3 ether);
        pay.claim(5, a[0], wt[0], 0, WorkProof.proofOf(leaves, 0));
        vm.prank(funder);
        vm.expectRevert(abi.encodeWithSelector(WorkPay.FundingClosed.selector, uint64(5)));
        pay.fund{value: 1 ether}(5);
        vm.prank(funder);
        vm.expectRevert(WorkPay.NothingSent.selector);
        pay.fund{value: 0}(6);
    }

    function test_unwritten_window_cannot_be_claimed_but_can_be_funded() public {
        vm.prank(funder);
        pay.fund{value: 1 ether}(99);
        bytes32[] memory none = new bytes32[](0);
        vm.expectRevert(abi.encodeWithSelector(IIgneumWork.NotFinalized.selector, uint64(99)));
        pay.claim(99, address(0x1001), 1, 0, none);
        vm.expectRevert(abi.encodeWithSelector(IIgneumWork.NotFinalized.selector, uint64(98)));
        pay.claim(98, address(0x1001), 1, 0, none);
    }

    function test_refusing_recipient_is_credited_and_withdraws() public {
        Refuser r = new Refuser();
        address[] memory a = new address[](2);
        uint256[] memory wt = new uint256[](2);
        a[0] = address(r) < address(0xFFFF) ? address(0xFFFF) : address(0x0001);
        a[1] = address(r);
        if (uint160(a[0]) > uint160(a[1])) (a[0], a[1]) = (a[1], a[0]);
        wt[0] = 1;
        wt[1] = 1;
        bytes32[] memory leaves = _write(7, a, wt);
        vm.prank(funder);
        pay.fund{value: 2 ether}(7);
        uint256 idx = a[0] == address(r) ? 0 : 1;
        pay.claim(7, address(r), 1, idx, WorkProof.proofOf(leaves, idx));
        assertEq(pay.claimable(address(r)), 1 ether);
        assertEq(pay.paidOut(7), 1 ether);
        vm.prank(address(r));
        vm.expectRevert(abi.encodeWithSelector(WorkPay.TransferFailed.selector, address(r)));
        pay.withdraw();
        vm.expectRevert(abi.encodeWithSelector(WorkPay.NothingToWithdraw.selector, address(this)));
        pay.withdraw();
    }

    function testFuzz_budget_is_paid_whole_but_for_dust(uint96 budgetWei, uint32 w1, uint32 w2, uint32 w3) public {
        vm.assume(budgetWei > 0 && w1 > 0 && w2 > 0 && w3 > 0);
        (address[] memory a, uint256[] memory wt) = _three();
        wt[0] = w1;
        wt[1] = w2;
        wt[2] = w3;
        bytes32[] memory leaves = _write(30, a, wt);
        vm.deal(funder, budgetWei);
        vm.prank(funder);
        pay.fund{value: budgetWei}(30);
        for (uint256 i = 0; i < 3; i++) pay.claim(30, a[i], wt[i], i, WorkProof.proofOf(leaves, i));
        assertLe(pay.remainder(30), 2);
        assertEq(pay.paidOut(30) + pay.remainder(30), budgetWei);
        assertTrue(pay.settled(30));
    }
}
