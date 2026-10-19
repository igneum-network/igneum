// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {Test} from "forge-std/Test.sol";

import {IgneumRecurring} from "../../src/pay/IgneumRecurring.sol";
import {IIgneumRecurring} from "../../src/interfaces/IIgneumRecurring.sol";

/// @dev Native authorisations only (the ERC-20 path holds nothing): deposits, collections by anyone, withdrawals,
///      revocations and time moving forward.
contract RecurringHandler is Test {
    IgneumRecurring public rec;
    bytes32[] public ids;
    address public payer = makeAddr("payer");
    address public payee = makeAddr("payee");
    address public operator = makeAddr("operator");

    constructor(IgneumRecurring rec_) {
        rec = rec_;
        vm.warp(1_800_000_000);
    }

    function authorise(uint96 amount, uint64 fee, uint32 period, uint96 cap, uint96 dep) external {
        amount = uint96(bound(amount, 1, 10 ether));
        fee = uint64(bound(fee, 0, 0.1 ether));
        period = uint32(bound(period, 1, 30 days));
        cap = uint96(bound(cap, uint256(amount) + fee, 1000 ether));
        dep = uint96(bound(dep, 0, 100 ether));
        vm.deal(payer, payer.balance + dep);
        vm.prank(payer);
        bytes32 id = rec.authorise{value: dep}(payee, address(0), amount, fee, period, 0, 0, cap);
        ids.push(id);
    }

    function deposit(uint256 which, uint96 dep) external {
        if (ids.length == 0) return;
        bytes32 id = ids[which % ids.length];
        if (rec.authorisation(id).revoked) return;
        dep = uint96(bound(dep, 0, 100 ether));
        vm.deal(payer, payer.balance + dep);
        vm.prank(payer);
        rec.deposit{value: dep}(id);
    }

    function collect(uint256 which) external {
        if (ids.length == 0) return;
        bytes32 id = ids[which % ids.length];
        (bool ok,) = rec.collectable(id);
        if (!ok) return;
        vm.prank(operator);
        rec.collect(id);
    }

    function withdraw(uint256 which, uint96 amount) external {
        if (ids.length == 0) return;
        bytes32 id = ids[which % ids.length];
        uint256 d = rec.authorisation(id).deposit;
        if (d == 0) return;
        amount = uint96(bound(amount, 0, d));
        vm.prank(payer);
        rec.withdraw(id, amount);
    }

    function revoke(uint256 which) external {
        if (ids.length == 0) return;
        bytes32 id = ids[which % ids.length];
        if (rec.authorisation(id).revoked) return;
        vm.prank(payer);
        rec.revoke(id);
    }

    function warp(uint32 by) external {
        vm.warp(block.timestamp + bound(by, 1, 60 days));
    }

    function count() external view returns (uint256) {
        return ids.length;
    }
}

contract RecurringInvariantTest is Test {
    IgneumRecurring internal rec;
    RecurringHandler internal h;

    function setUp() public {
        rec = new IgneumRecurring();
        h = new RecurringHandler(rec);
        targetContract(address(h));
    }

    /// Nothing under an authorisation ever exceeds its cap, and a revoked one holds no deposit.
    function invariant_spentWithinCap() public view {
        for (uint256 i = 0; i < h.count(); i++) {
            IIgneumRecurring.Authorisation memory a = rec.authorisation(h.ids(i));
            assertLe(a.spent, a.totalCap);
            if (a.revoked) assertEq(a.deposit, 0);
        }
    }

    /// The contract holds exactly the deposits plus the credits.
    function invariant_balanceEqualsDepositsPlusCredits() public view {
        uint256 deposits;
        for (uint256 i = 0; i < h.count(); i++) {
            deposits += rec.authorisation(h.ids(i)).deposit;
        }
        assertEq(address(rec).balance, deposits + rec.claimable(h.payee()) + rec.claimable(h.operator()));
    }

    /// What left the contract to the payee and the operator is exactly the sum of spent.
    function invariant_spentEqualsPaidOut() public view {
        uint256 spent;
        for (uint256 i = 0; i < h.count(); i++) {
            spent += rec.authorisation(h.ids(i)).spent;
        }
        assertEq(spent, h.payee().balance + h.operator().balance + rec.claimable(h.payee()) + rec.claimable(h.operator()));
    }
}
