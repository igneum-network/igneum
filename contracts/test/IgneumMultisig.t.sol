// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {Test} from "forge-std/Test.sol";
import {IERC1271} from "@openzeppelin/contracts/interfaces/IERC1271.sol";

import {IgneumMultisig} from "../src/pay/IgneumMultisig.sol";
import {IIgneumMultisig} from "../src/interfaces/IIgneumMultisig.sol";
import {IgneumPay} from "../src/pay/IgneumPay.sol";
import {PayTypes} from "../src/pay/PayTypes.sol";
import {TestToken} from "../src/test-assets/TestToken.sol";
import {TestCollectible} from "../src/test-assets/TestCollectible.sol";

contract IgneumMultisigTest is Test {
    IgneumMultisig internal ms;

    // three owners, sorted ascending by address in setUp
    address[3] internal owner;
    uint256[3] internal key;
    address internal stranger = makeAddr("stranger");
    address internal recipient = makeAddr("recipient");

    function setUp() public {
        (address a, uint256 ka) = makeAddrAndKey("owner-a");
        (address b, uint256 kb) = makeAddrAndKey("owner-b");
        (address c, uint256 kc) = makeAddrAndKey("owner-c");
        address[3] memory o = [a, b, c];
        uint256[3] memory k = [ka, kb, kc];
        // insertion sort by address
        for (uint256 i = 1; i < 3; i++) {
            for (uint256 j = i; j > 0 && o[j - 1] > o[j]; j--) {
                (o[j - 1], o[j]) = (o[j], o[j - 1]);
                (k[j - 1], k[j]) = (k[j], k[j - 1]);
            }
        }
        owner = o;
        key = k;
        address[] memory owners = new address[](3);
        owners[0] = o[0];
        owners[1] = o[1];
        owners[2] = o[2];
        ms = new IgneumMultisig(owners, 2);
        vm.deal(address(ms), 10 ether);
    }

    function _tx(address to, uint256 value, bytes memory data) internal view returns (PayTypes.Transaction memory) {
        return PayTypes.Transaction({to: to, value: value, data: data, nonce: ms.nonce(), deadline: 0});
    }

    function _sig(uint256 pk, bytes32 h) internal pure returns (bytes memory) {
        (uint8 v, bytes32 r, bytes32 s) = vm.sign(pk, h);
        return abi.encodePacked(r, s, v);
    }

    // ------------------------------------------------------------ construction

    function test_constructorRejectsBadThreshold() public {
        address[] memory owners = new address[](1);
        owners[0] = owner[0];
        vm.expectRevert(abi.encodeWithSelector(IIgneumMultisig.BadThreshold.selector, 2, 1));
        new IgneumMultisig(owners, 2);
        vm.expectRevert(abi.encodeWithSelector(IIgneumMultisig.BadThreshold.selector, 0, 1));
        new IgneumMultisig(owners, 0);
    }

    function test_constructorRejectsDuplicateOwner() public {
        address[] memory owners = new address[](2);
        owners[0] = owner[0];
        owners[1] = owner[0];
        vm.expectRevert(abi.encodeWithSelector(IIgneumMultisig.BadOwner.selector, owner[0]));
        new IgneumMultisig(owners, 1);
    }

    function test_owners() public view {
        address[] memory o = ms.owners();
        assertEq(o.length, 3);
        assertTrue(ms.isOwner(owner[0]) && ms.isOwner(owner[1]) && ms.isOwner(owner[2]));
        assertFalse(ms.isOwner(stranger));
        assertEq(ms.threshold(), 2);
    }

    // ------------------------------------------------------------ execution with signatures

    function test_executeWithTwoSignatures() public {
        PayTypes.Transaction memory t = _tx(recipient, 1 ether, "");
        bytes32 h = ms.txHash(t);
        bytes[] memory sigs = new bytes[](2);
        sigs[0] = _sig(key[0], h);
        sigs[1] = _sig(key[1], h);
        vm.prank(stranger); // anyone may submit
        ms.execute(t, sigs);
        assertEq(recipient.balance, 1 ether);
        assertEq(ms.nonce(), 1);
    }

    function test_executeUnsortedReverts() public {
        PayTypes.Transaction memory t = _tx(recipient, 1 ether, "");
        bytes32 h = ms.txHash(t);
        bytes[] memory sigs = new bytes[](2);
        sigs[0] = _sig(key[1], h);
        sigs[1] = _sig(key[0], h);
        vm.expectRevert(abi.encodeWithSelector(IIgneumMultisig.UnsortedOrDuplicateSigner.selector, owner[0]));
        ms.execute(t, sigs);
    }

    function test_executeDuplicateSignerReverts() public {
        PayTypes.Transaction memory t = _tx(recipient, 1 ether, "");
        bytes32 h = ms.txHash(t);
        bytes[] memory sigs = new bytes[](2);
        sigs[0] = _sig(key[0], h);
        sigs[1] = _sig(key[0], h);
        vm.expectRevert(abi.encodeWithSelector(IIgneumMultisig.UnsortedOrDuplicateSigner.selector, owner[0]));
        ms.execute(t, sigs);
    }

    function test_executeOneSignatureBelowThreshold() public {
        PayTypes.Transaction memory t = _tx(recipient, 1 ether, "");
        bytes32 h = ms.txHash(t);
        bytes[] memory sigs = new bytes[](1);
        sigs[0] = _sig(key[0], h);
        vm.expectRevert(abi.encodeWithSelector(IIgneumMultisig.BelowThreshold.selector, 1, 2));
        ms.execute(t, sigs);
        assertEq(recipient.balance, 0);
    }

    function test_executeStrangerSignatureReverts() public {
        (address s, uint256 ks) = makeAddrAndKey("not-an-owner");
        PayTypes.Transaction memory t = _tx(recipient, 1 ether, "");
        bytes32 h = ms.txHash(t);
        bytes[] memory sigs = new bytes[](1);
        sigs[0] = _sig(ks, h);
        vm.expectRevert(abi.encodeWithSelector(IIgneumMultisig.NotAnOwner.selector, s));
        ms.execute(t, sigs);
    }

    function test_executeWrongNonceReverts() public {
        PayTypes.Transaction memory t = _tx(recipient, 1 ether, "");
        t.nonce = 5;
        bytes[] memory sigs = new bytes[](0);
        vm.expectRevert(abi.encodeWithSelector(IIgneumMultisig.WrongNonce.selector, 5, 0));
        ms.execute(t, sigs);
    }

    function test_executePastDeadlineReverts() public {
        PayTypes.Transaction memory t = _tx(recipient, 1 ether, "");
        t.deadline = block.timestamp + 10;
        bytes32 h = ms.txHash(t);
        bytes[] memory sigs = new bytes[](2);
        sigs[0] = _sig(key[0], h);
        sigs[1] = _sig(key[1], h);
        vm.warp(block.timestamp + 11);
        vm.expectRevert(abi.encodeWithSelector(IIgneumMultisig.PastDeadline.selector, t.deadline));
        ms.execute(t, sigs);
    }

    function test_signatureCannotBeReplayedOnNextNonce() public {
        PayTypes.Transaction memory t = _tx(recipient, 1 ether, "");
        bytes32 h = ms.txHash(t);
        bytes[] memory sigs = new bytes[](2);
        sigs[0] = _sig(key[0], h);
        sigs[1] = _sig(key[1], h);
        ms.execute(t, sigs);
        // same payload at nonce 1: the hash differs, so the old signatures recover to non-owners
        t.nonce = 1;
        vm.expectRevert();
        ms.execute(t, sigs);
        assertEq(recipient.balance, 1 ether);
    }

    function test_failedCallRevertsAndKeepsNonce() public {
        TestToken tok = new TestToken();
        // transfer without balance: the ERC-20 reverts
        PayTypes.Transaction memory t = _tx(address(tok), 0, abi.encodeCall(tok.transfer, (recipient, 1 ether)));
        bytes32 h = ms.txHash(t);
        bytes[] memory sigs = new bytes[](2);
        sigs[0] = _sig(key[0], h);
        sigs[1] = _sig(key[1], h);
        vm.expectRevert();
        ms.execute(t, sigs);
        assertEq(ms.nonce(), 0);
    }

    // ------------------------------------------------------------ on-chain approvals

    function test_executeWithOnChainApprovals() public {
        PayTypes.Transaction memory t = _tx(recipient, 1 ether, "");
        bytes32 h = ms.txHash(t);
        vm.prank(owner[0]);
        ms.approve(h);
        vm.prank(owner[2]);
        ms.approve(h);
        bytes[] memory sigs = new bytes[](0);
        ms.execute(t, sigs);
        assertEq(recipient.balance, 1 ether);
    }

    function test_mixedApprovalAndSignature() public {
        PayTypes.Transaction memory t = _tx(recipient, 1 ether, "");
        bytes32 h = ms.txHash(t);
        vm.prank(owner[2]);
        ms.approve(h);
        bytes[] memory sigs = new bytes[](1);
        sigs[0] = _sig(key[0], h);
        ms.execute(t, sigs);
        assertEq(recipient.balance, 1 ether);
    }

    function test_ownerCountedOnceWhenApprovedAndSigned() public {
        PayTypes.Transaction memory t = _tx(recipient, 1 ether, "");
        bytes32 h = ms.txHash(t);
        vm.prank(owner[0]);
        ms.approve(h);
        bytes[] memory sigs = new bytes[](1);
        sigs[0] = _sig(key[0], h);
        vm.expectRevert(abi.encodeWithSelector(IIgneumMultisig.BelowThreshold.selector, 1, 2));
        ms.execute(t, sigs);
    }

    function test_revokeApproval() public {
        PayTypes.Transaction memory t = _tx(recipient, 1 ether, "");
        bytes32 h = ms.txHash(t);
        vm.prank(owner[0]);
        ms.approve(h);
        vm.prank(owner[1]);
        ms.approve(h);
        vm.prank(owner[1]);
        ms.revokeApproval(h);
        bytes[] memory sigs = new bytes[](0);
        vm.expectRevert(abi.encodeWithSelector(IIgneumMultisig.BelowThreshold.selector, 1, 2));
        ms.execute(t, sigs);
    }

    function test_strangerCannotApprove() public {
        vm.prank(stranger);
        vm.expectRevert(abi.encodeWithSelector(IIgneumMultisig.OnlyOwner.selector, stranger));
        ms.approve(bytes32(0));
    }

    // ------------------------------------------------------------ self-administration

    function _selfCall(bytes memory data) internal {
        PayTypes.Transaction memory t = _tx(address(ms), 0, data);
        bytes32 h = ms.txHash(t);
        bytes[] memory sigs = new bytes[](2);
        sigs[0] = _sig(key[0], h);
        sigs[1] = _sig(key[1], h);
        ms.execute(t, sigs);
    }

    function test_addOwnerBySelfCall() public {
        address d = makeAddr("owner-d");
        _selfCall(abi.encodeCall(IIgneumMultisig.addOwner, (d)));
        assertTrue(ms.isOwner(d));
        assertEq(ms.owners().length, 4);
    }

    function test_removeOwnerAndChangeThreshold() public {
        _selfCall(abi.encodeCall(IIgneumMultisig.changeThreshold, (3)));
        assertEq(ms.threshold(), 3);
        // cannot remove below threshold
        PayTypes.Transaction memory t = _tx(address(ms), 0, abi.encodeCall(IIgneumMultisig.removeOwner, (owner[2])));
        bytes32 h = ms.txHash(t);
        bytes[] memory sigs = new bytes[](3);
        sigs[0] = _sig(key[0], h);
        sigs[1] = _sig(key[1], h);
        sigs[2] = _sig(key[2], h);
        vm.expectRevert();
        ms.execute(t, sigs);
        // lower the threshold, then remove
        t = _tx(address(ms), 0, abi.encodeCall(IIgneumMultisig.changeThreshold, (2)));
        h = ms.txHash(t);
        sigs[0] = _sig(key[0], h);
        sigs[1] = _sig(key[1], h);
        sigs[2] = _sig(key[2], h);
        ms.execute(t, sigs);
        _selfCall(abi.encodeCall(IIgneumMultisig.removeOwner, (owner[2])));
        assertFalse(ms.isOwner(owner[2]));
        assertEq(ms.owners().length, 2);
    }

    function test_directAdminCallReverts() public {
        vm.prank(owner[0]);
        vm.expectRevert(IIgneumMultisig.OnlySelf.selector);
        ms.addOwner(stranger);
        vm.prank(owner[0]);
        vm.expectRevert(IIgneumMultisig.OnlySelf.selector);
        ms.changeThreshold(1);
    }

    // ------------------------------------------------------------ ERC-1271

    function test_isValidSignatureWithConcatenatedOwnerSignatures() public view {
        bytes32 h = keccak256("hello");
        bytes memory sig = abi.encodePacked(_sig(key[0], h), _sig(key[1], h));
        assertEq(ms.isValidSignature(h, sig), IERC1271.isValidSignature.selector);
        bytes memory one = _sig(key[0], h);
        assertEq(ms.isValidSignature(h, one), bytes4(0xffffffff));
        bytes memory unsorted = abi.encodePacked(_sig(key[1], h), _sig(key[0], h));
        assertEq(ms.isValidSignature(h, unsorted), bytes4(0xffffffff));
        assertEq(ms.isValidSignature(h, hex"0102"), bytes4(0xffffffff));
    }

    function test_isValidSignatureWithApprovedMessage() public {
        bytes32 h = keccak256("approved");
        assertEq(ms.isValidSignature(h, ""), bytes4(0xffffffff));
        _selfCall(abi.encodeCall(IIgneumMultisig.approveMessage, (h)));
        assertEq(ms.isValidSignature(h, ""), IERC1271.isValidSignature.selector);
    }

    function test_multisigIssuesPayRequest() public {
        IgneumPay pay = new IgneumPay();
        PayTypes.PaymentRequest memory r = PayTypes.PaymentRequest({
            payee: address(ms),
            token: address(0),
            amount: 1 ether,
            expiry: 0,
            memo: bytes32("from-multisig"),
            payer: address(0),
            nonce: 1
        });
        bytes32 digest = pay.requestId(r);
        bytes memory sig = abi.encodePacked(_sig(key[0], digest), _sig(key[2], digest));
        address customer = makeAddr("customer");
        vm.deal(customer, 1 ether);
        vm.prank(customer);
        pay.payRequest{value: 1 ether}(r, sig);
        assertEq(address(ms).balance, 11 ether);
    }

    // ------------------------------------------------------------ receiving assets

    function test_receivesNftsAndItems() public {
        TestCollectible c = new TestCollectible();
        uint256 id = c.mint(address(ms));
        assertEq(c.ownerOf(id), address(ms));
        assertTrue(ms.supportsInterface(type(IERC1271).interfaceId));
    }
}
