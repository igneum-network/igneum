// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {Test} from "forge-std/Test.sol";
import {IERC2981} from "@openzeppelin/contracts/interfaces/IERC2981.sol";
import {IERC721} from "@openzeppelin/contracts/token/ERC721/IERC721.sol";
import {IERC1155} from "@openzeppelin/contracts/token/ERC1155/IERC1155.sol";
import {IERC20Permit} from "@openzeppelin/contracts/token/ERC20/extensions/IERC20Permit.sol";

import {IgneumToken} from "../src/ownership/IgneumToken.sol";
import {IgneumCollectible} from "../src/ownership/IgneumCollectible.sol";
import {IgneumItems} from "../src/ownership/IgneumItems.sol";
import {IgneumMembership} from "../src/ownership/IgneumMembership.sol";
import {IERC5192} from "../src/interfaces/IERC5192.sol";
import {Faucet} from "../src/test-assets/Faucet.sol";
import {TestToken} from "../src/test-assets/TestToken.sol";

contract OwnershipTest is Test {
    address internal issuer = makeAddr("issuer");
    address internal alice = makeAddr("alice");
    address internal bob = makeAddr("bob");
    address internal royalties = makeAddr("royalties");

    // ------------------------------------------------------------ IgneumToken

    function test_tokenFixedSupplyAndPermit() public {
        IgneumToken t = new IgneumToken("Example", "EXM", 1_000_000 ether, issuer);
        assertEq(t.totalSupply(), 1_000_000 ether);
        assertEq(t.balanceOf(issuer), 1_000_000 ether);
        assertEq(t.decimals(), 18);
        assertTrue(t.DOMAIN_SEPARATOR() != bytes32(0));
        assertEq(t.nonces(issuer), 0);
    }

    function test_tokenPermit() public {
        (address holder, uint256 pk) = makeAddrAndKey("holder");
        IgneumToken t = new IgneumToken("Example", "EXM", 100 ether, holder);
        uint256 deadline = block.timestamp + 1 hours;
        bytes32 structHash = keccak256(
            abi.encode(
                keccak256("Permit(address owner,address spender,uint256 value,uint256 nonce,uint256 deadline)"),
                holder,
                bob,
                10 ether,
                0,
                deadline
            )
        );
        bytes32 digest = keccak256(abi.encodePacked("\x19\x01", t.DOMAIN_SEPARATOR(), structHash));
        (uint8 v, bytes32 r, bytes32 s) = vm.sign(pk, digest);
        t.permit(holder, bob, 10 ether, deadline, v, r, s);
        assertEq(t.allowance(holder, bob), 10 ether);
        vm.prank(bob);
        t.transferFrom(holder, bob, 10 ether);
        assertEq(t.balanceOf(bob), 10 ether);
    }

    // ------------------------------------------------------------ IgneumCollectible

    function test_collectibleMintUriRoyalty() public {
        IgneumCollectible c = new IgneumCollectible("Tickets", "TKT", issuer, royalties, 500);
        vm.prank(issuer);
        uint256 id = c.mint(alice, "ipfs://ticket/1");
        assertEq(c.ownerOf(id), alice);
        assertEq(c.tokenURI(id), "ipfs://ticket/1");
        (address r, uint256 amount) = c.royaltyInfo(id, 1 ether);
        assertEq(r, royalties);
        assertEq(amount, 0.05 ether);
        assertTrue(c.supportsInterface(type(IERC2981).interfaceId));
        assertTrue(c.supportsInterface(type(IERC721).interfaceId));
        vm.prank(alice);
        c.transferFrom(alice, bob, id);
        assertEq(c.ownerOf(id), bob);
    }

    function test_collectibleOnlyMinter() public {
        IgneumCollectible c = new IgneumCollectible("Tickets", "TKT", issuer, address(0), 0);
        vm.prank(alice);
        vm.expectRevert();
        c.mint(alice, "x");
        bytes32 minter = c.MINTER_ROLE();
        vm.prank(issuer);
        c.renounceRole(minter, issuer);
        vm.prank(issuer);
        vm.expectRevert();
        c.mint(alice, "x");
    }

    function test_collectibleBurnByHolderOnly() public {
        IgneumCollectible c = new IgneumCollectible("Tickets", "TKT", issuer, address(0), 0);
        vm.prank(issuer);
        uint256 id = c.mint(alice, "x");
        vm.prank(bob);
        vm.expectRevert();
        c.burn(id);
        vm.prank(alice);
        c.burn(id);
        vm.expectRevert();
        c.ownerOf(id);
    }

    // ------------------------------------------------------------ IgneumItems

    function test_itemsMintSupplyRoyalty() public {
        IgneumItems it = new IgneumItems("https://x/{id}.json", issuer, royalties, 250);
        vm.prank(issuer);
        it.mint(alice, 7, 100, "");
        assertEq(it.balanceOf(alice, 7), 100);
        assertEq(it.totalSupply(7), 100);
        assertTrue(it.exists(7));
        (address r, uint256 amount) = it.royaltyInfo(7, 1 ether);
        assertEq(r, royalties);
        assertEq(amount, 0.025 ether);
        assertTrue(it.supportsInterface(type(IERC1155).interfaceId));
        vm.prank(alice);
        it.burn(alice, 7, 40);
        assertEq(it.totalSupply(7), 60);
        vm.prank(bob);
        vm.expectRevert();
        it.burn(alice, 7, 1);
    }

    // ------------------------------------------------------------ IgneumMembership

    function test_membershipLockedUntilUnlocked() public {
        IgneumMembership m = new IgneumMembership("Club", "CLUB", "https://club/", issuer);
        vm.prank(issuer);
        uint256 id = m.mint(alice);
        assertTrue(m.locked(id));
        assertTrue(m.supportsInterface(type(IERC5192).interfaceId));
        assertEq(m.tokenURI(id), "https://club/1");
        vm.prank(alice);
        vm.expectRevert(abi.encodeWithSelector(IgneumMembership.TokenLocked.selector, id));
        m.transferFrom(alice, bob, id);
        vm.prank(issuer);
        m.setLocked(id, false);
        assertFalse(m.locked(id));
        vm.prank(alice);
        m.transferFrom(alice, bob, id);
        assertEq(m.ownerOf(id), bob);
        vm.prank(issuer);
        m.setLocked(id, true);
        vm.prank(bob);
        vm.expectRevert(abi.encodeWithSelector(IgneumMembership.TokenLocked.selector, id));
        m.transferFrom(bob, alice, id);
    }

    function test_membershipHolderBurnsEvenWhenLocked() public {
        IgneumMembership m = new IgneumMembership("Club", "CLUB", "https://club/", issuer);
        vm.prank(issuer);
        uint256 id = m.mint(alice);
        vm.prank(bob);
        vm.expectRevert(abi.encodeWithSelector(IgneumMembership.NotHolder.selector, id));
        m.burn(id);
        vm.prank(alice);
        m.burn(id);
        vm.expectRevert();
        m.ownerOf(id);
    }

    // ------------------------------------------------------------ test assets

    function test_testTokenMintCap() public {
        TestToken t = new TestToken();
        t.mint(alice, 1_000 ether);
        vm.expectRevert(abi.encodeWithSelector(TestToken.TooMuch.selector, 1_001 ether, 1_000 ether));
        t.mint(alice, 1_001 ether);
        assertEq(t.balanceOf(alice), 1_000 ether);
    }

    function test_faucetDripAndCooldown() public {
        Faucet f = new Faucet(1 ether, 1 days);
        vm.deal(address(this), 5 ether);
        (bool ok,) = address(f).call{value: 5 ether}("");
        assertTrue(ok);
        vm.prank(alice);
        f.drip();
        assertEq(alice.balance, 1 ether);
        vm.prank(alice);
        vm.expectRevert(abi.encodeWithSelector(Faucet.TooSoon.selector, block.timestamp + 1 days));
        f.drip();
        vm.warp(block.timestamp + 1 days);
        vm.prank(alice);
        f.drip();
        assertEq(alice.balance, 2 ether);
    }
}
