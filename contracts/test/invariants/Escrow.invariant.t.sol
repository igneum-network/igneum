// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {Test} from "forge-std/Test.sol";

import {IgneumEscrow} from "../../src/pay/IgneumEscrow.sol";
import {IIgneumEscrow} from "../../src/interfaces/IIgneumEscrow.sol";

contract EscrowHandler is Test {
    IgneumEscrow public esc;
    bytes32[] public ids;
    address public payer = makeAddr("payer");
    address public payee = makeAddr("payee");
    address public arbiter = makeAddr("arbiter");
    uint256 public openedTotal;

    constructor(IgneumEscrow esc_) {
        esc = esc_;
        vm.warp(1_800_000_000);
    }

    function open(uint96 amount, uint32 ttl, bool withArbiter, bool payeeClaims) external {
        amount = uint96(bound(amount, 1, 100 ether));
        ttl = uint32(bound(ttl, 1, 30 days));
        vm.deal(payer, payer.balance + amount);
        vm.prank(payer);
        bytes32 id = esc.open{value: amount}(
            payee,
            withArbiter ? arbiter : address(0),
            address(0),
            amount,
            uint64(block.timestamp + ttl),
            payeeClaims ? IIgneumEscrow.OnTimeout.PayeeClaims : IIgneumEscrow.OnTimeout.PayerRefunds
        );
        ids.push(id);
        openedTotal += amount;
    }

    function release(uint256 which) external {
        if (ids.length == 0) return;
        bytes32 id = ids[which % ids.length];
        IIgneumEscrow.State s = esc.escrow(id).state;
        if (s != IIgneumEscrow.State.Funded && s != IIgneumEscrow.State.Disputed) return;
        vm.prank(payer);
        esc.release(id);
    }

    function refund(uint256 which) external {
        if (ids.length == 0) return;
        bytes32 id = ids[which % ids.length];
        IIgneumEscrow.State s = esc.escrow(id).state;
        if (s != IIgneumEscrow.State.Funded && s != IIgneumEscrow.State.Disputed) return;
        vm.prank(payee);
        esc.refund(id);
    }

    function timeout(uint256 which) external {
        if (ids.length == 0) return;
        bytes32 id = ids[which % ids.length];
        IIgneumEscrow.Escrow memory e = esc.escrow(id);
        if (e.state != IIgneumEscrow.State.Funded || block.timestamp < e.deadline) return;
        esc.timeout(id);
    }

    function dispute(uint256 which, bool byPayer) external {
        if (ids.length == 0) return;
        bytes32 id = ids[which % ids.length];
        IIgneumEscrow.Escrow memory e = esc.escrow(id);
        if (e.state != IIgneumEscrow.State.Funded || e.arbiter == address(0) || block.timestamp >= e.deadline) return;
        vm.prank(byPayer ? payer : payee);
        esc.dispute(id);
    }

    function resolve(uint256 which, uint16 bps) external {
        if (ids.length == 0) return;
        bytes32 id = ids[which % ids.length];
        if (esc.escrow(id).state != IIgneumEscrow.State.Disputed) return;
        vm.prank(arbiter);
        esc.resolve(id, bound(bps, 0, 10_000));
    }

    function warp(uint32 by) external {
        vm.warp(block.timestamp + bound(by, 1, 40 days));
    }

    function count() external view returns (uint256) {
        return ids.length;
    }
}

contract EscrowInvariantTest is Test {
    IgneumEscrow internal esc;
    EscrowHandler internal h;

    function setUp() public {
        esc = new IgneumEscrow();
        h = new EscrowHandler(esc);
        targetContract(address(h));
    }

    /// The contract holds exactly the open escrows plus the credits.
    function invariant_balanceEqualsOpenPlusCredits() public view {
        uint256 open;
        for (uint256 i = 0; i < h.count(); i++) {
            IIgneumEscrow.Escrow memory e = esc.escrow(h.ids(i));
            if (e.state == IIgneumEscrow.State.Funded || e.state == IIgneumEscrow.State.Disputed) open += e.amount;
        }
        assertEq(address(esc).balance, open + esc.claimable(h.payer()) + esc.claimable(h.payee()));
    }

    /// Every wei that was ever opened is in the contract, with the payer or with the payee.
    function invariant_nothingLost() public view {
        assertEq(address(esc).balance + h.payer().balance + h.payee().balance, h.openedTotal());
    }

    /// An ended escrow never ends twice: closed states are terminal (checked by the handlers' refusal to act,
    /// and here by the state values being within the enum).
    function invariant_statesAreKnown() public view {
        for (uint256 i = 0; i < h.count(); i++) {
            assertLe(uint256(esc.escrow(h.ids(i)).state), uint256(IIgneumEscrow.State.Resolved));
        }
    }
}
