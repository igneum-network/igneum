// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {Test} from "forge-std/Test.sol";

import {IgneumEscrow} from "../src/pay/IgneumEscrow.sol";
import {IIgneumEscrow} from "../src/interfaces/IIgneumEscrow.sol";
import {TestToken} from "../src/test-assets/TestToken.sol";
import {TogglePayee} from "./mocks/Payees.sol";

contract IgneumEscrowTest is Test {
    IgneumEscrow internal esc;
    TestToken internal tok;

    address internal payer = makeAddr("payer");
    address internal payee = makeAddr("payee");
    address internal arbiter = makeAddr("arbiter");
    address internal stranger = makeAddr("stranger");

    uint64 internal deadline;

    function setUp() public {
        esc = new IgneumEscrow();
        tok = new TestToken();
        vm.deal(payer, 100 ether);
        tok.mint(payer, 1_000 ether);
        vm.warp(1_800_000_000);
        deadline = uint64(block.timestamp + 7 days);
    }

    function _open(address arb, IIgneumEscrow.OnTimeout t) internal returns (bytes32 id) {
        vm.prank(payer);
        id = esc.open{value: 1 ether}(payee, arb, address(0), 1 ether, deadline, t);
    }

    // ------------------------------------------------------------ open

    function test_openStores() public {
        bytes32 id = _open(arbiter, IIgneumEscrow.OnTimeout.PayerRefunds);
        IIgneumEscrow.Escrow memory e = esc.escrow(id);
        assertEq(e.payer, payer);
        assertEq(e.payee, payee);
        assertEq(e.arbiter, arbiter);
        assertEq(e.amount, 1 ether);
        assertEq(e.deadline, deadline);
        assertEq(uint256(e.state), uint256(IIgneumEscrow.State.Funded));
        assertEq(address(esc).balance, 1 ether);
    }

    function test_openRejectsBadInputs() public {
        vm.startPrank(payer);
        vm.expectRevert(IIgneumEscrow.ZeroPayee.selector);
        esc.open{value: 1 ether}(address(0), arbiter, address(0), 1 ether, deadline, IIgneumEscrow.OnTimeout.PayerRefunds);
        vm.expectRevert(IIgneumEscrow.ZeroAmount.selector);
        esc.open(payee, arbiter, address(0), 0, deadline, IIgneumEscrow.OnTimeout.PayerRefunds);
        vm.expectRevert(IIgneumEscrow.BadDeadline.selector);
        esc.open{value: 1 ether}(payee, arbiter, address(0), 1 ether, uint64(block.timestamp), IIgneumEscrow.OnTimeout.PayerRefunds);
        vm.expectRevert(abi.encodeWithSelector(IIgneumEscrow.WrongValue.selector, 1 ether, 0.5 ether));
        esc.open{value: 0.5 ether}(payee, arbiter, address(0), 1 ether, deadline, IIgneumEscrow.OnTimeout.PayerRefunds);
        vm.stopPrank();
    }

    // ------------------------------------------------------------ release and refund

    function test_releaseByPayer() public {
        bytes32 id = _open(arbiter, IIgneumEscrow.OnTimeout.PayerRefunds);
        vm.prank(payer);
        esc.release(id);
        assertEq(payee.balance, 1 ether);
        assertEq(uint256(esc.escrow(id).state), uint256(IIgneumEscrow.State.Released));
        vm.prank(payer);
        vm.expectRevert(abi.encodeWithSelector(IIgneumEscrow.WrongState.selector, id, IIgneumEscrow.State.Released));
        esc.release(id);
    }

    function test_releaseOnlyPayer() public {
        bytes32 id = _open(arbiter, IIgneumEscrow.OnTimeout.PayerRefunds);
        vm.prank(payee);
        vm.expectRevert(abi.encodeWithSelector(IIgneumEscrow.OnlyPayer.selector, id));
        esc.release(id);
    }

    function test_refundByPayee() public {
        bytes32 id = _open(arbiter, IIgneumEscrow.OnTimeout.PayeeClaims);
        uint256 before = payer.balance;
        vm.prank(payee);
        esc.refund(id);
        assertEq(payer.balance - before, 1 ether);
        assertEq(uint256(esc.escrow(id).state), uint256(IIgneumEscrow.State.Refunded));
    }

    function test_refundOnlyPayee() public {
        bytes32 id = _open(arbiter, IIgneumEscrow.OnTimeout.PayeeClaims);
        vm.prank(stranger);
        vm.expectRevert(abi.encodeWithSelector(IIgneumEscrow.OnlyPayee.selector, id));
        esc.refund(id);
    }

    // ------------------------------------------------------------ timeout templates

    function test_timeoutPayeeClaims() public {
        bytes32 id = _open(address(0), IIgneumEscrow.OnTimeout.PayeeClaims);
        vm.prank(stranger);
        vm.expectRevert(abi.encodeWithSelector(IIgneumEscrow.BeforeDeadline.selector, id, deadline));
        esc.timeout(id);
        vm.warp(deadline);
        vm.prank(stranger);
        esc.timeout(id);
        assertEq(payee.balance, 1 ether);
        assertEq(uint256(esc.escrow(id).state), uint256(IIgneumEscrow.State.Released));
    }

    function test_timeoutPayerRefunds() public {
        bytes32 id = _open(address(0), IIgneumEscrow.OnTimeout.PayerRefunds);
        uint256 before = payer.balance;
        vm.warp(deadline + 1);
        esc.timeout(id);
        assertEq(payer.balance - before, 1 ether);
        assertEq(uint256(esc.escrow(id).state), uint256(IIgneumEscrow.State.Refunded));
    }

    // ------------------------------------------------------------ disputes

    function test_disputeFreezesTimeoutAndArbiterResolves() public {
        bytes32 id = _open(arbiter, IIgneumEscrow.OnTimeout.PayeeClaims);
        vm.prank(payee);
        esc.dispute(id);
        assertEq(uint256(esc.escrow(id).state), uint256(IIgneumEscrow.State.Disputed));
        vm.warp(deadline + 1);
        vm.expectRevert(abi.encodeWithSelector(IIgneumEscrow.WrongState.selector, id, IIgneumEscrow.State.Disputed));
        esc.timeout(id);
        uint256 before = payer.balance;
        vm.prank(arbiter);
        esc.resolve(id, 6_000);
        assertEq(payee.balance, 0.6 ether);
        assertEq(payer.balance - before, 0.4 ether);
        assertEq(uint256(esc.escrow(id).state), uint256(IIgneumEscrow.State.Resolved));
        assertEq(address(esc).balance, 0);
    }

    function test_disputeNeedsArbiter() public {
        bytes32 id = _open(address(0), IIgneumEscrow.OnTimeout.PayeeClaims);
        vm.prank(payer);
        vm.expectRevert(abi.encodeWithSelector(IIgneumEscrow.NoArbiter.selector, id));
        esc.dispute(id);
    }

    function test_disputeOnlyPartiesBeforeDeadline() public {
        bytes32 id = _open(arbiter, IIgneumEscrow.OnTimeout.PayeeClaims);
        vm.prank(stranger);
        vm.expectRevert(abi.encodeWithSelector(IIgneumEscrow.OnlyParty.selector, id));
        esc.dispute(id);
        vm.warp(deadline);
        vm.prank(payer);
        vm.expectRevert(abi.encodeWithSelector(IIgneumEscrow.AfterDeadline.selector, id, deadline));
        esc.dispute(id);
    }

    function test_resolveOnlyArbiterOnlyDisputed() public {
        bytes32 id = _open(arbiter, IIgneumEscrow.OnTimeout.PayeeClaims);
        vm.prank(arbiter);
        vm.expectRevert(abi.encodeWithSelector(IIgneumEscrow.WrongState.selector, id, IIgneumEscrow.State.Funded));
        esc.resolve(id, 5_000);
        vm.prank(payer);
        esc.dispute(id);
        vm.prank(stranger);
        vm.expectRevert(abi.encodeWithSelector(IIgneumEscrow.OnlyArbiter.selector, id));
        esc.resolve(id, 5_000);
        vm.prank(arbiter);
        vm.expectRevert(abi.encodeWithSelector(IIgneumEscrow.BadShare.selector, 10_001));
        esc.resolve(id, 10_001);
    }

    function test_payerCanStillReleaseDuringDispute() public {
        bytes32 id = _open(arbiter, IIgneumEscrow.OnTimeout.PayeeClaims);
        vm.prank(payee);
        esc.dispute(id);
        vm.prank(payer);
        esc.release(id);
        assertEq(payee.balance, 1 ether);
    }

    // ------------------------------------------------------------ ERC-20

    function test_erc20Escrow() public {
        vm.startPrank(payer);
        tok.approve(address(esc), 10 ether);
        bytes32 id = esc.open(payee, address(0), address(tok), 10 ether, deadline, IIgneumEscrow.OnTimeout.PayerRefunds);
        assertEq(tok.balanceOf(address(esc)), 10 ether);
        esc.release(id);
        vm.stopPrank();
        assertEq(tok.balanceOf(payee), 10 ether);
        assertEq(tok.balanceOf(address(esc)), 0);
    }

    function test_erc20WithValueReverts() public {
        vm.startPrank(payer);
        tok.approve(address(esc), 10 ether);
        vm.expectRevert(abi.encodeWithSelector(IIgneumEscrow.WrongValue.selector, 0, 1));
        esc.open{value: 1}(payee, address(0), address(tok), 10 ether, deadline, IIgneumEscrow.OnTimeout.PayerRefunds);
        vm.stopPrank();
    }

    // ------------------------------------------------------------ credit and claim

    function test_refusingPayeeIsCredited() public {
        TogglePayee p = new TogglePayee();
        p.setAccept(false);
        vm.prank(payer);
        bytes32 id = esc.open{value: 1 ether}(address(p), address(0), address(0), 1 ether, deadline, IIgneumEscrow.OnTimeout.PayeeClaims);
        vm.prank(payer);
        esc.release(id);
        assertEq(esc.claimable(address(p)), 1 ether);
        p.setAccept(true);
        p.call(address(esc), abi.encodeCall(IgneumEscrow.claim, ()));
        assertEq(address(p).balance, 1 ether);
        assertEq(address(esc).balance, 0);
    }

    // ------------------------------------------------------------ fuzz: the split never loses a wei

    function testFuzz_resolveConservesAmount(uint96 amount, uint16 bps) public {
        vm.assume(amount > 0);
        bps = uint16(bound(bps, 0, 10_000));
        vm.deal(payer, amount);
        vm.prank(payer);
        bytes32 id = esc.open{value: amount}(payee, arbiter, address(0), amount, deadline, IIgneumEscrow.OnTimeout.PayeeClaims);
        vm.prank(payer);
        esc.dispute(id);
        vm.prank(arbiter);
        esc.resolve(id, bps);
        assertEq(payee.balance + payer.balance, amount);
        assertEq(address(esc).balance, 0);
    }
}
