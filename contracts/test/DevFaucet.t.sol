// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {Test} from "forge-std/Test.sol";

import {DevFaucet} from "../src/faucet/DevFaucet.sol";

/// The dev faucet against its bounds: a drip to any address by anyone, once per cooldown per recipient, the day's
/// cap for the whole, the label, nothing else (no owner, no pause).
contract DevFaucetTest is Test {
    DevFaucet internal faucet;
    address internal relay = makeAddr("relay");
    address internal alice = makeAddr("alice");
    address internal bob = makeAddr("bob");

    function setUp() public {
        vm.warp(1_800_000_000);
        faucet = new DevFaucet(25 ether, 1 days, 1000 ether);
        vm.deal(address(this), 5000 ether);
        (bool ok,) = address(faucet).call{value: 3000 ether}("");
        assertTrue(ok);
    }

    function test_label_and_parameters() public view {
        assertEq(faucet.LABEL(), "development-network tokens; no monetary value");
        assertEq(faucet.dripAmount(), 25 ether);
        assertEq(faucet.cooldown(), 1 days);
        assertEq(faucet.dailyCap(), 1000 ether);
        assertEq(faucet.leftToday(), 1000 ether);
    }

    function test_drip_byAnyoneToAnyAddressOncePerDay() public {
        vm.expectEmit(true, true, false, true);
        emit DevFaucet.Dripped(alice, 25 ether, relay);
        vm.prank(relay);
        faucet.drip(alice);
        assertEq(alice.balance, 25 ether);
        assertEq(faucet.nextDripAt(alice), block.timestamp + 1 days);
        vm.expectRevert(abi.encodeWithSelector(DevFaucet.TooSoon.selector, alice, block.timestamp + 1 days));
        vm.prank(bob);
        faucet.drip(alice);
        // the person themselves, a day on
        vm.warp(block.timestamp + 1 days);
        vm.prank(alice);
        faucet.drip(alice);
        assertEq(alice.balance, 50 ether);
        assertEq(faucet.nextDripAt(bob), 0);
    }

    function test_dayCap_boundsACrowdOfAddresses() public {
        for (uint256 i = 0; i < 40; i++) {
            vm.prank(relay);
            faucet.drip(address(uint160(0x1000 + i)));
        }
        assertEq(faucet.leftToday(), 0);
        vm.expectRevert(abi.encodeWithSelector(DevFaucet.DayCapReached.selector, 1000 ether, 1000 ether));
        vm.prank(relay);
        faucet.drip(bob);
        // the next day opens the cap again
        vm.warp(block.timestamp + 1 days);
        assertEq(faucet.leftToday(), 1000 ether);
        vm.prank(relay);
        faucet.drip(bob);
        assertEq(bob.balance, 25 ether);
    }

    function test_refusals_badRecipientAndEmpty() public {
        vm.expectRevert(abi.encodeWithSelector(DevFaucet.BadRecipient.selector, address(0)));
        faucet.drip(address(0));
        vm.expectRevert(abi.encodeWithSelector(DevFaucet.BadRecipient.selector, address(faucet)));
        faucet.drip(address(faucet));
        DevFaucet dry = new DevFaucet(25 ether, 1 days, 1000 ether);
        vm.expectRevert(abi.encodeWithSelector(DevFaucet.Empty.selector, 0, 25 ether));
        dry.drip(alice);
        vm.expectRevert("the parameters");
        new DevFaucet(25 ether, 1 days, 1 ether);
    }

    function test_funded_byAnyone() public {
        vm.deal(bob, 1 ether);
        vm.expectEmit(true, false, false, true);
        emit DevFaucet.Funded(bob, 1 ether);
        vm.prank(bob);
        (bool ok,) = address(faucet).call{value: 1 ether}("");
        assertTrue(ok);
    }

    function test_nothingElse_noOwnerOrPause() public {
        bytes4[4] memory absent = [bytes4(keccak256("owner()")), bytes4(keccak256("pause()")), bytes4(keccak256("withdraw(uint256)")), bytes4(keccak256("setDrip(uint256)"))];
        for (uint256 i = 0; i < absent.length; i++) {
            (bool ok,) = address(faucet).call(abi.encodeWithSelector(absent[i]));
            assertFalse(ok);
        }
    }
}
