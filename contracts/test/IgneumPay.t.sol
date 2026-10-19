// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {Test} from "forge-std/Test.sol";

import {IgneumPay} from "../src/pay/IgneumPay.sol";
import {IIgneumPay} from "../src/interfaces/IIgneumPay.sol";
import {PayTypes} from "../src/pay/PayTypes.sol";
import {TestToken} from "../src/test-assets/TestToken.sol";
import {TogglePayee} from "./mocks/Payees.sol";

contract IgneumPayTest is Test {
    IgneumPay internal pay;
    TestToken internal tok;

    address internal payee = makeAddr("payee");
    address internal payer = makeAddr("payer");
    address internal other = makeAddr("other");

    function setUp() public {
        pay = new IgneumPay();
        tok = new TestToken();
        vm.deal(payer, 100 ether);
        vm.deal(other, 100 ether);
        tok.mint(payer, 1_000 ether);
    }

    // ------------------------------------------------------------ invoices, native

    function test_createAndPayNative() public {
        vm.prank(payee);
        bytes32 id = pay.createInvoice(address(0), 1 ether, 0, bytes32("order-1"), address(0));
        assertEq(uint256(pay.status(id)), uint256(IIgneumPay.Status.Open));

        vm.prank(payer);
        vm.expectEmit(true, true, true, true);
        emit IIgneumPay.Paid(id, payee, payer, address(0), 1 ether, bytes32("order-1"));
        pay.pay{value: 1 ether}(id);

        assertEq(payee.balance, 1 ether);
        assertEq(uint256(pay.status(id)), uint256(IIgneumPay.Status.Paid));
        IIgneumPay.Receipt memory r = pay.receipt(id);
        assertEq(r.payer, payer);
        assertEq(r.amount, 1 ether);
        assertEq(r.token, address(0));
        assertEq(r.blockNumber, uint64(block.number));
    }

    function test_payWrongValueReverts() public {
        vm.prank(payee);
        bytes32 id = pay.createInvoice(address(0), 1 ether, 0, 0, address(0));
        vm.prank(payer);
        vm.expectRevert(abi.encodeWithSelector(IIgneumPay.WrongValue.selector, 1 ether, 0.5 ether));
        pay.pay{value: 0.5 ether}(id);
    }

    function test_payTwiceReverts() public {
        vm.prank(payee);
        bytes32 id = pay.createInvoice(address(0), 1 ether, 0, 0, address(0));
        vm.prank(payer);
        pay.pay{value: 1 ether}(id);
        vm.prank(other);
        vm.expectRevert(abi.encodeWithSelector(IIgneumPay.NotOpen.selector, id));
        pay.pay{value: 1 ether}(id);
    }

    function test_cancelThenPayReverts() public {
        vm.prank(payee);
        bytes32 id = pay.createInvoice(address(0), 1 ether, 0, 0, address(0));
        vm.prank(other);
        vm.expectRevert(abi.encodeWithSelector(IIgneumPay.NotPayee.selector, id));
        pay.cancelInvoice(id);
        vm.prank(payee);
        pay.cancelInvoice(id);
        assertEq(uint256(pay.status(id)), uint256(IIgneumPay.Status.Cancelled));
        vm.prank(payer);
        vm.expectRevert(abi.encodeWithSelector(IIgneumPay.NotOpen.selector, id));
        pay.pay{value: 1 ether}(id);
    }

    function test_expiry() public {
        uint64 expiry = uint64(block.timestamp + 100);
        vm.prank(payee);
        bytes32 id = pay.createInvoice(address(0), 1 ether, expiry, 0, address(0));
        vm.warp(expiry + 1);
        assertEq(uint256(pay.status(id)), uint256(IIgneumPay.Status.Expired));
        vm.prank(payer);
        vm.expectRevert(abi.encodeWithSelector(IIgneumPay.Expired.selector, id));
        pay.pay{value: 1 ether}(id);
    }

    function test_badExpiryReverts() public {
        vm.prank(payee);
        vm.expectRevert(IIgneumPay.BadExpiry.selector);
        pay.createInvoice(address(0), 1 ether, uint64(block.timestamp), 0, address(0));
    }

    function test_zeroAmountReverts() public {
        vm.prank(payee);
        vm.expectRevert(IIgneumPay.ZeroAmount.selector);
        pay.createInvoice(address(0), 0, 0, 0, address(0));
    }

    function test_namedPayerOnly() public {
        vm.prank(payee);
        bytes32 id = pay.createInvoice(address(0), 1 ether, 0, 0, payer);
        vm.prank(other);
        vm.expectRevert(abi.encodeWithSelector(IIgneumPay.WrongPayer.selector, id, other));
        pay.pay{value: 1 ether}(id);
        vm.prank(payer);
        pay.pay{value: 1 ether}(id);
        assertEq(payee.balance, 1 ether);
    }

    function test_idsAreDistinctPerPayee() public {
        vm.startPrank(payee);
        bytes32 a = pay.createInvoice(address(0), 1 ether, 0, 0, address(0));
        bytes32 b = pay.createInvoice(address(0), 1 ether, 0, 0, address(0));
        vm.stopPrank();
        assertTrue(a != b);
        assertEq(pay.invoiceCount(payee), 2);
    }

    // ------------------------------------------------------------ invoices, ERC-20

    function test_payErc20() public {
        vm.prank(payee);
        bytes32 id = pay.createInvoice(address(tok), 10 ether, 0, 0, address(0));
        vm.startPrank(payer);
        tok.approve(address(pay), 10 ether);
        pay.pay(id);
        vm.stopPrank();
        assertEq(tok.balanceOf(payee), 10 ether);
        assertEq(tok.balanceOf(payer), 990 ether);
    }

    function test_payErc20WithValueReverts() public {
        vm.prank(payee);
        bytes32 id = pay.createInvoice(address(tok), 10 ether, 0, 0, address(0));
        vm.startPrank(payer);
        tok.approve(address(pay), 10 ether);
        vm.expectRevert(abi.encodeWithSelector(IIgneumPay.WrongValue.selector, 0, 1));
        pay.pay{value: 1}(id);
        vm.stopPrank();
    }

    function test_payErc20WithoutAllowanceReverts() public {
        vm.prank(payee);
        bytes32 id = pay.createInvoice(address(tok), 10 ether, 0, 0, address(0));
        vm.prank(payer);
        vm.expectRevert();
        pay.pay(id);
        assertEq(uint256(pay.status(id)), uint256(IIgneumPay.Status.Open));
    }

    // ------------------------------------------------------------ signed requests

    function _request(address signer, uint256 nonce) internal pure returns (PayTypes.PaymentRequest memory r) {
        r = PayTypes.PaymentRequest({
            payee: signer,
            token: address(0),
            amount: 2 ether,
            expiry: 0,
            memo: bytes32("req"),
            payer: address(0),
            nonce: nonce
        });
    }

    function _sign(uint256 pk, PayTypes.PaymentRequest memory r) internal view returns (bytes memory) {
        bytes32 digest = pay.requestId(r);
        (uint8 v, bytes32 rs, bytes32 ss) = vm.sign(pk, digest);
        return abi.encodePacked(rs, ss, v);
    }

    function test_payRequest() public {
        (address signer, uint256 pk) = makeAddrAndKey("signer");
        PayTypes.PaymentRequest memory r = _request(signer, 1);
        bytes memory sig = _sign(pk, r);
        vm.prank(payer);
        bytes32 id = pay.payRequest{value: 2 ether}(r, sig);
        assertEq(id, pay.requestId(r));
        assertEq(signer.balance, 2 ether);
        assertEq(uint256(pay.status(id)), uint256(IIgneumPay.Status.Paid));
        assertEq(pay.receipt(id).payer, payer);
        IIgneumPay.Invoice memory inv = pay.invoice(id);
        assertEq(inv.payee, signer);
        assertEq(inv.memo, bytes32("req"));
    }

    function test_payRequestReplayReverts() public {
        (address signer, uint256 pk) = makeAddrAndKey("signer");
        PayTypes.PaymentRequest memory r = _request(signer, 1);
        bytes memory sig = _sign(pk, r);
        vm.prank(payer);
        bytes32 id = pay.payRequest{value: 2 ether}(r, sig);
        vm.prank(other);
        vm.expectRevert(abi.encodeWithSelector(IIgneumPay.NotOpen.selector, id));
        pay.payRequest{value: 2 ether}(r, sig);
    }

    function test_payRequestBadSignatureReverts() public {
        (address signer,) = makeAddrAndKey("signer");
        (, uint256 wrongPk) = makeAddrAndKey("wrong");
        PayTypes.PaymentRequest memory r = _request(signer, 1);
        bytes memory sig = _sign(wrongPk, r);
        bytes32 id = pay.requestId(r);
        vm.prank(payer);
        vm.expectRevert(abi.encodeWithSelector(IIgneumPay.BadSignature.selector, id));
        pay.payRequest{value: 2 ether}(r, sig);
    }

    function test_payRequestTamperedAmountReverts() public {
        (address signer, uint256 pk) = makeAddrAndKey("signer");
        PayTypes.PaymentRequest memory r = _request(signer, 1);
        bytes memory sig = _sign(pk, r);
        r.amount = 1 ether;
        bytes32 id = pay.requestId(r);
        vm.prank(payer);
        vm.expectRevert(abi.encodeWithSelector(IIgneumPay.BadSignature.selector, id));
        pay.payRequest{value: 1 ether}(r, sig);
    }

    function test_payRequestExpired() public {
        (address signer, uint256 pk) = makeAddrAndKey("signer");
        PayTypes.PaymentRequest memory r = _request(signer, 1);
        r.expiry = uint64(block.timestamp + 10);
        bytes memory sig = _sign(pk, r);
        vm.warp(block.timestamp + 11);
        bytes32 id = pay.requestId(r);
        vm.prank(payer);
        vm.expectRevert(abi.encodeWithSelector(IIgneumPay.Expired.selector, id));
        pay.payRequest{value: 2 ether}(r, sig);
    }

    function test_payRequestNamedPayer() public {
        (address signer, uint256 pk) = makeAddrAndKey("signer");
        PayTypes.PaymentRequest memory r = _request(signer, 1);
        r.payer = payer;
        bytes memory sig = _sign(pk, r);
        bytes32 id = pay.requestId(r);
        vm.prank(other);
        vm.expectRevert(abi.encodeWithSelector(IIgneumPay.WrongPayer.selector, id, other));
        pay.payRequest{value: 2 ether}(r, sig);
        vm.prank(payer);
        pay.payRequest{value: 2 ether}(r, sig);
        assertEq(signer.balance, 2 ether);
    }

    function test_payRequestErc20() public {
        (address signer, uint256 pk) = makeAddrAndKey("signer");
        PayTypes.PaymentRequest memory r = _request(signer, 1);
        r.token = address(tok);
        bytes memory sig = _sign(pk, r);
        vm.startPrank(payer);
        tok.approve(address(pay), 2 ether);
        pay.payRequest(r, sig);
        vm.stopPrank();
        assertEq(tok.balanceOf(signer), 2 ether);
    }

    function test_twoNoncesTwoPayments() public {
        (address signer, uint256 pk) = makeAddrAndKey("signer");
        PayTypes.PaymentRequest memory a = _request(signer, 1);
        PayTypes.PaymentRequest memory b = _request(signer, 2);
        vm.startPrank(payer);
        pay.payRequest{value: 2 ether}(a, _sign(pk, a));
        pay.payRequest{value: 2 ether}(b, _sign(pk, b));
        vm.stopPrank();
        assertEq(signer.balance, 4 ether);
    }

    // ------------------------------------------------------------ the domain

    function test_domainSeparatorMatchesEip712() public view {
        bytes32 expected = keccak256(
            abi.encode(
                keccak256("EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)"),
                keccak256("IgneumPay"),
                keccak256("1"),
                block.chainid,
                address(pay)
            )
        );
        assertEq(pay.domainSeparator(), expected);
    }

    // ------------------------------------------------------------ credit and claim

    function test_refusingPayeeIsCreditedAndClaims() public {
        TogglePayee p = new TogglePayee();
        vm.prank(address(p));
        bytes32 id = pay.createInvoice(address(0), 1 ether, 0, 0, address(0));
        p.setAccept(false);
        vm.prank(payer);
        vm.expectEmit(true, false, false, true);
        emit IIgneumPay.Credited(address(p), 1 ether);
        pay.pay{value: 1 ether}(id);
        assertEq(address(p).balance, 0);
        assertEq(pay.claimable(address(p)), 1 ether);
        assertEq(address(pay).balance, 1 ether);
        assertEq(uint256(pay.status(id)), uint256(IIgneumPay.Status.Paid));

        // still refusing: the claim reverts and the credit stays
        vm.expectRevert("call failed");
        p.call(address(pay), abi.encodeCall(IgneumPay.claim, ()));
        assertEq(pay.claimable(address(p)), 1 ether);

        p.setAccept(true);
        p.call(address(pay), abi.encodeCall(IgneumPay.claim, ()));
        assertEq(address(p).balance, 1 ether);
        assertEq(pay.claimable(address(p)), 0);
        assertEq(address(pay).balance, 0);
    }

    function test_claimNothingReverts() public {
        vm.prank(payee);
        vm.expectRevert(IIgneumPay.NothingToClaim.selector);
        pay.claim();
    }

    // ------------------------------------------------------------ invariant-style: the contract holds only credits

    function testFuzz_balanceEqualsCredits(uint96 amount, bool refuse) public {
        vm.assume(amount > 0);
        TogglePayee p = new TogglePayee();
        p.setAccept(!refuse);
        vm.prank(address(p));
        bytes32 id = pay.createInvoice(address(0), amount, 0, 0, address(0));
        vm.deal(payer, amount);
        vm.prank(payer);
        pay.pay{value: amount}(id);
        assertEq(address(pay).balance, pay.claimable(address(p)));
        assertEq(address(pay).balance + address(p).balance, amount);
    }
}
