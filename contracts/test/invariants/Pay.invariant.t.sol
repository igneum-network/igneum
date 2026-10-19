// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {Test} from "forge-std/Test.sol";

import {IgneumPay} from "../../src/pay/IgneumPay.sol";
import {TogglePayee} from "../mocks/Payees.sol";

/// @dev The handler drives Pay with invoices to accepting and refusing payees and random claims.
contract PayHandler is Test {
    IgneumPay public pay;
    TogglePayee[] public payees;
    bytes32[] public open;
    uint256 public paidTotal;

    constructor(IgneumPay pay_) {
        pay = pay_;
        for (uint256 i = 0; i < 4; i++) {
            payees.push(new TogglePayee());
        }
    }

    function createInvoice(uint256 who, uint96 amount) external {
        amount = uint96(bound(amount, 1, 100 ether));
        TogglePayee p = payees[who % payees.length];
        vm.prank(address(p));
        bytes32 id = pay.createInvoice(address(0), amount, 0, bytes32(0), address(0));
        open.push(id);
    }

    function toggle(uint256 who, bool accept) external {
        payees[who % payees.length].setAccept(accept);
    }

    function payOne(uint256 which) external {
        if (open.length == 0) return;
        uint256 i = which % open.length;
        bytes32 id = open[i];
        open[i] = open[open.length - 1];
        open.pop();
        uint256 amount = pay.invoice(id).amount;
        address payer = makeAddr("payer");
        vm.deal(payer, amount);
        vm.prank(payer);
        pay.pay{value: amount}(id);
        paidTotal += amount;
    }

    function claimOne(uint256 who) external {
        TogglePayee p = payees[who % payees.length];
        if (pay.claimable(address(p)) == 0) return;
        p.setAccept(true);
        p.call(address(pay), abi.encodeCall(IgneumPay.claim, ()));
    }

    function payeeCount() external view returns (uint256) {
        return payees.length;
    }
}

contract PayInvariantTest is Test {
    IgneumPay internal pay;
    PayHandler internal h;

    function setUp() public {
        pay = new IgneumPay();
        h = new PayHandler(pay);
        targetContract(address(h));
    }

    /// The contract holds exactly the credits it owes, never more, never less.
    function invariant_balanceEqualsCredits() public view {
        uint256 credits;
        for (uint256 i = 0; i < h.payeeCount(); i++) {
            credits += pay.claimable(address(h.payees(i)));
        }
        assertEq(address(pay).balance, credits);
    }

    /// Everything paid is either with a payee or still owed to one.
    function invariant_paidIsWithPayeesOrOwed() public view {
        uint256 held;
        for (uint256 i = 0; i < h.payeeCount(); i++) {
            address p = address(h.payees(i));
            held += p.balance + pay.claimable(p);
        }
        assertEq(held, h.paidTotal());
    }
}
