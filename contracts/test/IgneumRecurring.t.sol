// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {Test} from "forge-std/Test.sol";

import {IgneumRecurring} from "../src/pay/IgneumRecurring.sol";
import {IIgneumRecurring} from "../src/interfaces/IIgneumRecurring.sol";
import {TestToken} from "../src/test-assets/TestToken.sol";
import {TogglePayee} from "./mocks/Payees.sol";

contract IgneumRecurringTest is Test {
    IgneumRecurring internal rec;
    TestToken internal tok;

    address internal payer = makeAddr("payer");
    address internal payee = makeAddr("payee");
    address internal operator = makeAddr("operator");

    uint256 internal constant AMOUNT = 1 ether;
    uint256 internal constant FEE = 0.01 ether;
    uint64 internal constant PERIOD = 30 days;

    function setUp() public {
        rec = new IgneumRecurring();
        tok = new TestToken();
        vm.deal(payer, 100 ether);
        tok.mint(payer, 1_000 ether);
        vm.warp(1_800_000_000);
    }

    function _nativeAuth(uint256 depositAmount, uint256 cap) internal returns (bytes32 id) {
        vm.prank(payer);
        id = rec.authorise{value: depositAmount}(payee, address(0), AMOUNT, FEE, PERIOD, 0, 0, cap);
    }

    // ------------------------------------------------------------ authorise

    function test_authoriseStoresBounds() public {
        bytes32 id = _nativeAuth(5 ether, 12 * (AMOUNT + FEE));
        IIgneumRecurring.Authorisation memory a = rec.authorisation(id);
        assertEq(a.payer, payer);
        assertEq(a.payee, payee);
        assertEq(a.amountPerPeriod, AMOUNT);
        assertEq(a.operatorFee, FEE);
        assertEq(a.period, PERIOD);
        assertEq(a.start, uint64(block.timestamp));
        assertEq(a.deposit, 5 ether);
        assertEq(a.totalCap, 12 * (AMOUNT + FEE));
        assertFalse(a.revoked);
    }

    function test_authoriseRejectsCapBelowOnePeriod() public {
        vm.prank(payer);
        vm.expectRevert(IIgneumRecurring.CapBelowOnePeriod.selector);
        rec.authorise(payee, address(0), AMOUNT, FEE, PERIOD, 0, 0, AMOUNT);
    }

    function test_authoriseRejectsBadInputs() public {
        vm.startPrank(payer);
        vm.expectRevert(IIgneumRecurring.ZeroPayee.selector);
        rec.authorise(address(0), address(0), AMOUNT, FEE, PERIOD, 0, 0, 10 ether);
        vm.expectRevert(IIgneumRecurring.ZeroAmount.selector);
        rec.authorise(payee, address(0), 0, FEE, PERIOD, 0, 0, 10 ether);
        vm.expectRevert(IIgneumRecurring.BadPeriod.selector);
        rec.authorise(payee, address(0), AMOUNT, FEE, 0, 0, 0, 10 ether);
        vm.expectRevert(IIgneumRecurring.BadWindow.selector);
        rec.authorise(payee, address(0), AMOUNT, FEE, PERIOD, uint64(block.timestamp + 10), uint64(block.timestamp + 5), 10 ether);
        vm.expectRevert(abi.encodeWithSelector(IIgneumRecurring.WrongValue.selector, 0, 1));
        rec.authorise{value: 1}(payee, address(tok), AMOUNT, FEE, PERIOD, 0, 0, 10 ether);
        vm.stopPrank();
    }

    // ------------------------------------------------------------ collect, native

    function test_collectPaysPayeeAndOperator() public {
        bytes32 id = _nativeAuth(5 ether, 100 ether);
        vm.prank(operator);
        vm.expectEmit(true, true, true, true);
        emit IIgneumRecurring.Collected(id, 0, operator, AMOUNT, FEE);
        rec.collect(id);
        assertEq(payee.balance, AMOUNT);
        assertEq(operator.balance, FEE);
        assertTrue(rec.collected(id, 0));
        IIgneumRecurring.Authorisation memory a = rec.authorisation(id);
        assertEq(a.spent, AMOUNT + FEE);
        assertEq(a.deposit, 5 ether - AMOUNT - FEE);
    }

    function test_collectTwiceInOnePeriodReverts() public {
        bytes32 id = _nativeAuth(5 ether, 100 ether);
        vm.prank(operator);
        rec.collect(id);
        vm.warp(block.timestamp + PERIOD - 1);
        vm.prank(operator);
        vm.expectRevert(abi.encodeWithSelector(IIgneumRecurring.AlreadyCollected.selector, id, 0));
        rec.collect(id);
    }

    function test_collectNextPeriod() public {
        bytes32 id = _nativeAuth(5 ether, 100 ether);
        vm.prank(operator);
        rec.collect(id);
        vm.warp(block.timestamp + PERIOD);
        assertEq(rec.currentPeriod(id), 1);
        vm.prank(operator);
        rec.collect(id);
        assertEq(payee.balance, 2 * AMOUNT);
        assertEq(operator.balance, 2 * FEE);
    }

    function test_missedPeriodIsForgone() public {
        bytes32 id = _nativeAuth(5 ether, 100 ether);
        vm.warp(block.timestamp + 3 * PERIOD);
        vm.prank(operator);
        rec.collect(id);
        assertTrue(rec.collected(id, 3));
        assertFalse(rec.collected(id, 0));
        // nothing else is collectable now
        (bool ok, string memory why) = rec.collectable(id);
        assertFalse(ok);
        assertEq(why, "period collected");
    }

    function test_capBoundsEverything() public {
        // cap allows exactly two collections
        bytes32 id = _nativeAuth(10 ether, 2 * (AMOUNT + FEE));
        vm.prank(operator);
        rec.collect(id);
        vm.warp(block.timestamp + PERIOD);
        vm.prank(operator);
        rec.collect(id);
        vm.warp(block.timestamp + PERIOD);
        vm.prank(operator);
        vm.expectRevert(
            abi.encodeWithSelector(IIgneumRecurring.CapReached.selector, id, 2 * (AMOUNT + FEE), 2 * (AMOUNT + FEE))
        );
        rec.collect(id);
        assertEq(payee.balance + operator.balance, 2 * (AMOUNT + FEE));
    }

    function test_depositShortReverts() public {
        bytes32 id = _nativeAuth(0.5 ether, 100 ether);
        vm.prank(operator);
        vm.expectRevert(
            abi.encodeWithSelector(IIgneumRecurring.InsufficientDeposit.selector, id, 0.5 ether, AMOUNT + FEE)
        );
        rec.collect(id);
        vm.prank(payer);
        rec.deposit{value: 1 ether}(id);
        vm.prank(operator);
        rec.collect(id);
        assertEq(payee.balance, AMOUNT);
    }

    function test_endStopsCollection() public {
        uint64 end = uint64(block.timestamp + PERIOD + 1);
        vm.prank(payer);
        bytes32 id = rec.authorise{value: 5 ether}(payee, address(0), AMOUNT, FEE, PERIOD, 0, end, 100 ether);
        vm.prank(operator);
        rec.collect(id);
        vm.warp(end);
        vm.prank(operator);
        vm.expectRevert(abi.encodeWithSelector(IIgneumRecurring.Ended.selector, id, end));
        rec.collect(id);
    }

    function test_notStartedReverts() public {
        uint64 start = uint64(block.timestamp + 100);
        vm.prank(payer);
        bytes32 id = rec.authorise{value: 5 ether}(payee, address(0), AMOUNT, FEE, PERIOD, start, 0, 100 ether);
        vm.prank(operator);
        vm.expectRevert(abi.encodeWithSelector(IIgneumRecurring.NotStarted.selector, id, start));
        rec.collect(id);
    }

    // ------------------------------------------------------------ revoke and withdraw

    function test_revokeRefundsDepositAndStopsCollection() public {
        bytes32 id = _nativeAuth(5 ether, 100 ether);
        vm.prank(operator);
        rec.collect(id);
        uint256 before = payer.balance;
        vm.prank(payer);
        rec.revoke(id);
        assertEq(payer.balance - before, 5 ether - AMOUNT - FEE);
        assertEq(rec.authorisation(id).deposit, 0);
        vm.warp(block.timestamp + PERIOD);
        vm.prank(operator);
        vm.expectRevert(abi.encodeWithSelector(IIgneumRecurring.IsRevoked.selector, id));
        rec.collect(id);
    }

    function test_withdrawPartOfDeposit() public {
        bytes32 id = _nativeAuth(5 ether, 100 ether);
        vm.prank(payer);
        rec.withdraw(id, 2 ether);
        assertEq(rec.authorisation(id).deposit, 3 ether);
        vm.prank(payer);
        vm.expectRevert(abi.encodeWithSelector(IIgneumRecurring.InsufficientDeposit.selector, id, 3 ether, 4 ether));
        rec.withdraw(id, 4 ether);
    }

    function test_onlyPayerManages() public {
        bytes32 id = _nativeAuth(5 ether, 100 ether);
        vm.startPrank(operator);
        vm.expectRevert(abi.encodeWithSelector(IIgneumRecurring.OnlyPayer.selector, id));
        rec.revoke(id);
        vm.expectRevert(abi.encodeWithSelector(IIgneumRecurring.OnlyPayer.selector, id));
        rec.withdraw(id, 1);
        vm.expectRevert(abi.encodeWithSelector(IIgneumRecurring.OnlyPayer.selector, id));
        rec.deposit{value: 0}(id);
        vm.stopPrank();
    }

    function test_unknownIdReverts() public {
        vm.expectRevert(abi.encodeWithSelector(IIgneumRecurring.Unknown.selector, bytes32(uint256(1))));
        rec.collect(bytes32(uint256(1)));
    }

    // ------------------------------------------------------------ ERC-20

    function test_erc20CollectPullsByAllowance() public {
        vm.startPrank(payer);
        bytes32 id = rec.authorise(payee, address(tok), AMOUNT, FEE, PERIOD, 0, 0, 100 ether);
        tok.approve(address(rec), 100 ether);
        vm.stopPrank();
        (bool ok,) = rec.collectable(id);
        assertTrue(ok);
        vm.prank(operator);
        rec.collect(id);
        assertEq(tok.balanceOf(payee), AMOUNT);
        assertEq(tok.balanceOf(operator), FEE);
        assertEq(tok.balanceOf(payer), 1_000 ether - AMOUNT - FEE);
    }

    function test_erc20WithoutAllowanceFails() public {
        vm.prank(payer);
        bytes32 id = rec.authorise(payee, address(tok), AMOUNT, FEE, PERIOD, 0, 0, 100 ether);
        (bool ok, string memory why) = rec.collectable(id);
        assertFalse(ok);
        assertEq(why, "allowance short");
        vm.prank(operator);
        vm.expectRevert();
        rec.collect(id);
        assertFalse(rec.collected(id, 0));
    }

    function test_erc20NoDepositFunctions() public {
        vm.prank(payer);
        bytes32 id = rec.authorise(payee, address(tok), AMOUNT, FEE, PERIOD, 0, 0, 100 ether);
        vm.prank(payer);
        vm.expectRevert(abi.encodeWithSelector(IIgneumRecurring.NotNative.selector, id));
        rec.deposit{value: 1 ether}(id);
    }

    // ------------------------------------------------------------ credit and claim

    function test_refusingPayeeIsCredited() public {
        TogglePayee p = new TogglePayee();
        p.setAccept(false);
        vm.prank(payer);
        bytes32 id = rec.authorise{value: 5 ether}(address(p), address(0), AMOUNT, FEE, PERIOD, 0, 0, 100 ether);
        vm.prank(operator);
        rec.collect(id);
        assertEq(rec.claimable(address(p)), AMOUNT);
        assertEq(operator.balance, FEE);
        p.setAccept(true);
        p.call(address(rec), abi.encodeCall(IgneumRecurring.claim, ()));
        assertEq(address(p).balance, AMOUNT);
    }

    // ------------------------------------------------------------ fuzz: never more than the cap leaves

    function testFuzz_neverExceedsCap(uint8 periods, uint64 cap) public {
        vm.assume(cap >= AMOUNT + FEE);
        vm.deal(payer, 1_000_000 ether);
        vm.prank(payer);
        bytes32 id = rec.authorise{value: 1_000_000 ether}(payee, address(0), AMOUNT, FEE, PERIOD, 0, 0, cap);
        uint256 collections;
        for (uint256 i = 0; i < periods; i++) {
            vm.prank(operator);
            (bool ok,) = address(rec).call(abi.encodeCall(IgneumRecurring.collect, (id)));
            if (ok) collections++;
            vm.warp(block.timestamp + PERIOD);
        }
        assertEq(payee.balance, collections * AMOUNT);
        assertLe(payee.balance + operator.balance, cap);
        assertEq(payee.balance + operator.balance, rec.authorisation(id).spent);
    }
}
