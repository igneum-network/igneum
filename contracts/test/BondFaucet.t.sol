// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {Test} from "forge-std/Test.sol";

import {BondFaucet} from "../src/faucet/BondFaucet.sol";

/// The bond faucet against its bounds: one drip per bond key hash, one per address, the day's cap, the label.
contract BondFaucetTest is Test {
    BondFaucet internal faucet;
    address internal relay = makeAddr("relay");
    address internal alice = makeAddr("alice");
    address internal bob = makeAddr("bob");
    bytes32 internal keyA = keccak256("bond key a");
    bytes32 internal keyB = keccak256("bond key b");

    function setUp() public {
        vm.warp(1_800_000_000);
        faucet = new BondFaucet(86.3 ether, 1726 ether);
        vm.deal(address(this), 10000 ether);
        (bool ok,) = address(faucet).call{value: 5000 ether}("");
        assertTrue(ok);
    }

    function test_label_and_parameters() public view {
        assertEq(faucet.LABEL(), "development-network tokens; no monetary value");
        assertEq(faucet.dripAmount(), 86.3 ether);
        assertEq(faucet.dailyCap(), 1726 ether);
        assertEq(faucet.leftToday(), 1726 ether);
    }

    function test_drip_oncePerKeyAndOncePerAddress() public {
        vm.expectEmit(true, true, true, true);
        emit BondFaucet.Dripped(alice, keyA, 86.3 ether, relay);
        vm.prank(relay);
        faucet.drip(alice, keyA);
        assertEq(alice.balance, 86.3 ether);
        vm.expectRevert(abi.encodeWithSelector(BondFaucet.KeyDripped.selector, keyA));
        vm.prank(relay);
        faucet.drip(bob, keyA);
        vm.expectRevert(abi.encodeWithSelector(BondFaucet.AddressDripped.selector, alice));
        vm.prank(relay);
        faucet.drip(alice, keyB);
        // a day on changes nothing: the bound is once, not once a day
        vm.warp(block.timestamp + 2 days);
        vm.expectRevert(abi.encodeWithSelector(BondFaucet.KeyDripped.selector, keyA));
        faucet.drip(bob, keyA);
        vm.prank(bob);
        faucet.drip(bob, keyB);
        assertEq(bob.balance, 86.3 ether);
    }

    function test_dayCap_twentyKeysADay() public {
        for (uint256 i = 0; i < 20; i++) {
            vm.prank(relay);
            faucet.drip(address(uint160(0x2000 + i)), keccak256(abi.encode("key", i)));
        }
        assertEq(faucet.leftToday(), 0);
        vm.expectRevert(abi.encodeWithSelector(BondFaucet.DayCapReached.selector, 1726 ether, 1726 ether));
        vm.prank(relay);
        faucet.drip(bob, keyB);
        vm.warp(block.timestamp + 1 days);
        assertEq(faucet.leftToday(), 1726 ether);
        vm.prank(relay);
        faucet.drip(bob, keyB);
    }

    function test_refusals() public {
        vm.expectRevert(abi.encodeWithSelector(BondFaucet.BadRecipient.selector, address(0)));
        faucet.drip(address(0), keyA);
        vm.expectRevert(abi.encodeWithSelector(BondFaucet.BadKey.selector, bytes32(0)));
        faucet.drip(alice, bytes32(0));
        BondFaucet dry = new BondFaucet(86.3 ether, 1726 ether);
        vm.expectRevert(abi.encodeWithSelector(BondFaucet.Empty.selector, 0, 86.3 ether));
        dry.drip(alice, keyA);
        vm.expectRevert("the parameters");
        new BondFaucet(86.3 ether, 1 ether);
    }

    function test_nothingElse() public {
        bytes4[3] memory absent = [bytes4(keccak256("owner()")), bytes4(keccak256("pause()")), bytes4(keccak256("withdraw(uint256)"))];
        for (uint256 i = 0; i < absent.length; i++) {
            (bool ok,) = address(faucet).call(abi.encodeWithSelector(absent[i]));
            assertFalse(ok);
        }
    }
}
