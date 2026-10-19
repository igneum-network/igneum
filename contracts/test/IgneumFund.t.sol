// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {Test, Vm} from "forge-std/Test.sol";

import {IgneumFund} from "../src/fund/IgneumFund.sol";
import {IIgneumFund} from "../src/interfaces/IIgneumFund.sol";

/// The fund's contract against its mandate (docs/igneumhash/FUND-MANDATE-2026-10-11.md section 5), one test per
/// item and its refusal: the form, the spend, the ceiling and the notice, the freeze and the lift, the recusal,
/// the company-majority check and the flag set, the signer change, the events, and what the contract has not.
contract IgneumFundTest is Test {
    IgneumFund internal fund;
    address[7] internal s;
    bytes32 internal constant SCOPE = keccak256("scope: reference client maintenance, milestone 1");
    address internal payee = makeAddr("payee");
    address internal stranger = makeAddr("stranger");

    function setUp() public {
        address[] memory signers = new address[](7);
        bool[] memory flags = new bool[](7);
        for (uint256 i = 0; i < 7; i++) {
            s[i] = makeAddr(string(abi.encodePacked("signer-", bytes1(uint8(0x61 + i)))));
            signers[i] = s[i];
        }
        // three company-connected signers: a, b, c; four independent: d, e, f, g
        flags[0] = true;
        flags[1] = true;
        flags[2] = true;
        vm.warp(1_800_000_000);
        fund = new IgneumFund(signers, flags);
        vm.deal(address(fund), 1000 ether);
    }

    // ---------------------------------------------------------------- helpers

    function _approve(uint64 id, uint256[] memory who) internal {
        for (uint256 i = 0; i < who.length; i++) {
            vm.prank(s[who[i]]);
            fund.approve(id);
        }
    }

    function _list(uint256 a, uint256 b, uint256 c, uint256 d) internal pure returns (uint256[] memory l) {
        l = new uint256[](4);
        l[0] = a;
        l[1] = b;
        l[2] = c;
        l[3] = d;
    }

    function _spend(uint256 amount) internal returns (uint64 id) {
        vm.prank(s[3]);
        id = fund.proposeSpend(payee, amount, SCOPE, 0, 0, false);
    }

    /// four independent signers approve and one executes
    function _passIndependent(uint64 id) internal {
        _approve(id, _list(3, 4, 5, 6));
        vm.prank(s[3]);
        fund.execute(id);
    }

    // ---------------------------------------------------------------- the form

    function test_form_sevenSignersFourToSpend() public view {
        assertEq(fund.signers().length, 7);
        assertEq(fund.SPEND_QUORUM(), 4);
        assertEq(fund.FREEZE_QUORUM(), 2);
        assertEq(fund.CHANGE_QUORUM(), 5);
        assertEq(fund.MAX_COMPANY_CONNECTED(), 3);
        assertEq(fund.WINDOW(), 90 days);
        assertEq(fund.NOTICE_DELAY(), 60 days);
        assertEq(fund.FREEZE_DURATION(), 30 days);
        assertTrue(fund.companyConnected(s[0]) && fund.companyConnected(s[2]) && !fund.companyConnected(s[3]));
    }

    function test_form_refusesFourCompanyConnected() public {
        address[] memory signers = new address[](7);
        bool[] memory flags = new bool[](7);
        for (uint256 i = 0; i < 7; i++) {
            signers[i] = s[i];
            flags[i] = i < 4;
        }
        vm.expectRevert(abi.encodeWithSelector(IIgneumFund.TooManyCompanyConnected.selector, 4));
        new IgneumFund(signers, flags);
    }

    function test_form_refusesSixOrDuplicateSigners() public {
        address[] memory six = new address[](6);
        bool[] memory flags6 = new bool[](6);
        for (uint256 i = 0; i < 6; i++) {
            six[i] = s[i];
        }
        // six is a size the table knows (GOVERNANCE 8): it constructs; two is not
        new IgneumFund(six, flags6);
        address[] memory two = new address[](2);
        bool[] memory flags2 = new bool[](2);
        two[0] = s[0];
        two[1] = s[1];
        vm.expectRevert(abi.encodeWithSelector(IIgneumFund.BadInput.selector, "three to seven signers and as many flags"));
        new IgneumFund(two, flags2);
        address[] memory dup = new address[](7);
        bool[] memory flags7 = new bool[](7);
        for (uint256 i = 0; i < 7; i++) {
            dup[i] = s[i];
        }
        dup[6] = s[0];
        vm.expectRevert(abi.encodeWithSelector(IIgneumFund.BadSigner.selector, s[0]));
        new IgneumFund(dup, flags7);
    }

    function test_form_holdsIgnOnlyAndLogsCredits() public {
        vm.expectEmit(true, false, false, true);
        emit IIgneumFund.Received(stranger, 1 ether);
        vm.deal(stranger, 1 ether);
        vm.prank(stranger);
        (bool ok,) = address(fund).call{value: 1 ether}("");
        assertTrue(ok);
        assertEq(address(fund).balance, 1001 ether);
    }

    // ---------------------------------------------------------------- spend

    function test_spend_fourOfSevenPays() public {
        uint64 id = _spend(10 ether);
        _approve(id, _list(3, 4, 5, 6));
        vm.expectEmit(true, false, false, true);
        emit IIgneumFund.Executed(id, IIgneumFund.Kind.Spend, payee, 10 ether);
        vm.prank(s[3]);
        fund.execute(id);
        assertEq(payee.balance, 10 ether);
        assertTrue(fund.proposals(id).closed);
        assertEq(fund.windowSpent(), 10 ether);
    }

    function test_spend_threeApprovalsRevertBelowQuorum() public {
        uint64 id = _spend(10 ether);
        uint256[] memory three = new uint256[](3);
        three[0] = 3;
        three[1] = 4;
        three[2] = 5;
        _approve(id, three);
        vm.expectRevert(abi.encodeWithSelector(IIgneumFund.BelowQuorum.selector, id, 3, 4));
        vm.prank(s[3]);
        fund.execute(id);
    }

    function test_spend_onlySignersAct() public {
        vm.expectRevert(abi.encodeWithSelector(IIgneumFund.OnlySigner.selector, stranger));
        vm.prank(stranger);
        fund.proposeSpend(payee, 1 ether, SCOPE, 0, 0, false);
        uint64 id = _spend(1 ether);
        vm.expectRevert(abi.encodeWithSelector(IIgneumFund.OnlySigner.selector, stranger));
        vm.prank(stranger);
        fund.approve(id);
        vm.expectRevert(abi.encodeWithSelector(IIgneumFund.OnlySigner.selector, stranger));
        vm.prank(stranger);
        fund.execute(id);
    }

    function test_spend_expiryAndSelfSendAndZeroRefused() public {
        vm.prank(s[3]);
        uint64 id = fund.proposeSpend(payee, 1 ether, SCOPE, uint64(block.timestamp + 1 days), 0, false);
        _approve(id, _list(3, 4, 5, 6));
        vm.warp(block.timestamp + 2 days);
        vm.expectRevert(abi.encodeWithSelector(IIgneumFund.PastExpiry.selector, id, uint64(block.timestamp - 1 days)));
        vm.prank(s[3]);
        fund.execute(id);
        vm.expectRevert(abi.encodeWithSelector(IIgneumFund.BadInput.selector, "the payee"));
        vm.prank(s[3]);
        fund.proposeSpend(address(fund), 1 ether, SCOPE, 0, 0, false);
        vm.expectRevert(abi.encodeWithSelector(IIgneumFund.BadInput.selector, "the amount"));
        vm.prank(s[3]);
        fund.proposeSpend(payee, 0, SCOPE, 0, 0, false);
        vm.expectRevert(abi.encodeWithSelector(IIgneumFund.BadInput.selector, "the scope"));
        vm.prank(s[3]);
        fund.proposeSpend(payee, 1 ether, bytes32(0), 0, 0, false);
    }

    function test_spend_beyondBalanceRefusedByEvent() public {
        uint64 id = _spend(2000 ether);
        _approve(id, _list(3, 4, 5, 6));
        vm.expectEmit(true, false, false, true);
        emit IIgneumFund.Refused(id, IIgneumFund.Kind.Spend, "beyond the balance");
        vm.prank(s[3]);
        fund.execute(id);
        assertTrue(fund.proposals(id).closed);
        assertEq(payee.balance, 0);
    }

    // ---------------------------------------------------------------- the ceiling and the notice

    function test_ceiling_quarterOfBalanceAtWindowStart() public {
        // 1000 at the window's start: the ceiling is 250
        uint64 a = _spend(200 ether);
        _passIndependent(a);
        uint64 b = _spend(60 ether);
        _approve(b, _list(3, 4, 5, 6));
        vm.expectEmit(true, false, false, true);
        emit IIgneumFund.Refused(b, IIgneumFund.Kind.Spend, "above the window's ceiling without a notice");
        vm.prank(s[3]);
        fund.execute(b);
        assertEq(payee.balance, 200 ether);
        // a credit after the snapshot does not raise the ceiling
        vm.deal(address(fund), address(fund).balance + 1000 ether);
        uint64 c = _spend(60 ether);
        _approve(c, _list(3, 4, 5, 6));
        vm.expectEmit(true, false, false, true);
        emit IIgneumFund.Refused(c, IIgneumFund.Kind.Spend, "above the window's ceiling without a notice");
        vm.prank(s[3]);
        fund.execute(c);
        // the next window opens on a fresh snapshot
        vm.warp(block.timestamp + 90 days);
        uint64 d = _spend(60 ether);
        _passIndependent(d);
        assertEq(payee.balance, 260 ether);
        assertEq(fund.windowIndex(), 1);
    }

    function test_ceiling_noticeSixtyDaysEarlierLiftsIt() public {
        vm.prank(s[3]);
        uint64 n = fund.proposeNotice(500 ether, SCOPE, 0);
        _approve(n, _list(3, 4, 5, 6));
        vm.prank(s[3]);
        fund.execute(n);
        assertEq(fund.noticeExecutedAt(n), uint64(block.timestamp));
        // too soon
        vm.prank(s[3]);
        uint64 early = fund.proposeSpend(payee, 500 ether, SCOPE, 0, n, false);
        _approve(early, _list(3, 4, 5, 6));
        vm.expectEmit(true, false, false, true);
        emit IIgneumFund.Refused(early, IIgneumFund.Kind.Spend, "the notice is younger than sixty days");
        vm.prank(s[3]);
        fund.execute(early);
        // sixty days on, inside the same window
        vm.warp(block.timestamp + 60 days);
        vm.prank(s[3]);
        uint64 big = fund.proposeSpend(payee, 500 ether, SCOPE, 0, n, false);
        _passIndependent(big);
        assertEq(payee.balance, 500 ether);
        assertTrue(fund.noticeSpent(n));
        // the notice is spent: a second spend on it is refused
        vm.prank(s[3]);
        uint64 again = fund.proposeSpend(payee, 100 ether, SCOPE, 0, n, false);
        _approve(again, _list(3, 4, 5, 6));
        vm.expectEmit(true, false, false, true);
        emit IIgneumFund.Refused(again, IIgneumFund.Kind.Spend, "the notice is spent");
        vm.prank(s[3]);
        fund.execute(again);
        // a notice that never executed cannot be cited
        vm.expectRevert(abi.encodeWithSelector(IIgneumFund.BadInput.selector, "the notice"));
        vm.prank(s[3]);
        fund.proposeSpend(payee, 1 ether, SCOPE, 0, 99, false);
    }

    // ---------------------------------------------------------------- freeze and lift

    function test_freeze_twoSignersThirtyDays() public {
        vm.prank(s[0]);
        uint64 f = fund.proposeFreeze();
        vm.prank(s[1]);
        fund.approve(f);
        vm.expectRevert(abi.encodeWithSelector(IIgneumFund.BelowQuorum.selector, f, 1, 2));
        vm.prank(s[1]);
        fund.execute(f);
        vm.prank(s[0]);
        fund.approve(f);
        vm.expectEmit(true, false, false, true);
        emit IIgneumFund.Frozen(f, uint64(block.timestamp + 30 days));
        vm.prank(s[1]);
        fund.execute(f);
        // a spend with four approvals is refused while frozen
        uint64 id = _spend(1 ether);
        _approve(id, _list(3, 4, 5, 6));
        vm.expectEmit(true, false, false, true);
        emit IIgneumFund.Refused(id, IIgneumFund.Kind.Spend, "frozen");
        vm.prank(s[3]);
        fund.execute(id);
        // thirty days on it is over by itself
        vm.warp(block.timestamp + 30 days);
        uint64 later = _spend(1 ether);
        _passIndependent(later);
        assertEq(payee.balance, 1 ether);
    }

    function test_freeze_liftNeedsFreshFourOfSeven() public {
        // a lift proposed before the freeze is not fresh
        vm.prank(s[0]);
        uint64 f1 = fund.proposeFreeze();
        _approve(f1, _list(0, 1, 0, 1));
        vm.prank(s[0]);
        fund.execute(f1);
        vm.prank(s[3]);
        uint64 stale = fund.proposeLift();
        _approve(stale, _list(3, 4, 5, 6));
        vm.warp(block.timestamp + 1 days);
        vm.prank(s[0]);
        uint64 f2 = fund.proposeFreeze();
        _approve(f2, _list(0, 1, 0, 1));
        vm.prank(s[0]);
        fund.execute(f2);
        vm.expectEmit(true, false, false, true);
        emit IIgneumFund.Refused(stale, IIgneumFund.Kind.Lift, "a lift made before the freeze is not fresh");
        vm.prank(s[3]);
        fund.execute(stale);
        assertGt(fund.frozenUntil(), block.timestamp);
        // a fresh lift with four ends it
        vm.prank(s[3]);
        uint64 fresh = fund.proposeLift();
        _approve(fresh, _list(3, 4, 5, 6));
        vm.expectEmit(true, false, false, true);
        emit IIgneumFund.Lifted(fresh);
        vm.prank(s[3]);
        fund.execute(fresh);
        assertEq(fund.frozenUntil(), 0);
        uint64 id = _spend(1 ether);
        _passIndependent(id);
        assertEq(payee.balance, 1 ether);
        // no freeze, no lift to propose
        vm.expectRevert(abi.encodeWithSelector(IIgneumFund.BadInput.selector, "not frozen"));
        vm.prank(s[3]);
        fund.proposeLift();
    }

    // ---------------------------------------------------------------- recusal

    function test_recusal_removesTheApprovalFromQuorum() public {
        uint64 id = _spend(1 ether);
        _approve(id, _list(3, 4, 5, 6));
        vm.expectEmit(true, true, false, true);
        emit IIgneumFund.Recused(id, s[6]);
        vm.prank(s[6]);
        fund.recuse(id);
        assertFalse(fund.approved(id, s[6]));
        assertTrue(fund.recused(id, s[6]));
        vm.expectRevert(abi.encodeWithSelector(IIgneumFund.BelowQuorum.selector, id, 3, 4));
        vm.prank(s[3]);
        fund.execute(id);
        // a recused signer cannot approve again on it
        vm.expectRevert(abi.encodeWithSelector(IIgneumFund.BadInput.selector, "recused"));
        vm.prank(s[6]);
        fund.approve(id);
        // a fourth other signer carries it (an independent one)
        vm.prank(s[2]);
        fund.approve(id);
        vm.prank(s[3]);
        fund.execute(id);
        assertEq(payee.balance, 1 ether);
    }

    // ---------------------------------------------------------------- the company-majority check and the flags

    function test_majority_flaggedMajorityRefused() public {
        // a, b, c flagged plus d: three of four approving are company-connected
        uint64 id = _spend(1 ether);
        _approve(id, _list(0, 1, 2, 3));
        vm.expectEmit(true, false, false, true);
        emit IIgneumFund.Refused(id, IIgneumFund.Kind.Spend, "the approving signers are a majority company-connected");
        vm.prank(s[0]);
        fund.execute(id);
        assertEq(payee.balance, 0);
        // two flagged of four is not a majority
        uint64 ok = _spend(1 ether);
        _approve(ok, _list(0, 1, 3, 4));
        vm.prank(s[0]);
        fund.execute(ok);
        assertEq(payee.balance, 1 ether);
        // a recusal by a flagged signer changes the count: a, b, c, d, e approving, c recuses: 2 of 4
        uint64 r = _spend(1 ether);
        uint256[] memory five = new uint256[](5);
        five[0] = 0;
        five[1] = 1;
        five[2] = 2;
        five[3] = 3;
        five[4] = 4;
        _approve(r, five);
        vm.prank(s[2]);
        fund.recuse(r);
        vm.prank(s[0]);
        fund.execute(r);
        assertEq(payee.balance, 2 ether);
    }

    function test_flags_setByFourAtMostThree() public {
        // a fourth flag is refused
        vm.prank(s[3]);
        uint64 id = fund.proposeSetFlag(s[3], true);
        _approve(id, _list(3, 4, 5, 6));
        vm.expectEmit(true, false, false, true);
        emit IIgneumFund.Refused(id, IIgneumFund.Kind.SetFlag, "more company-connected signers than the size allows");
        vm.prank(s[3]);
        fund.execute(id);
        assertFalse(fund.companyConnected(s[3]));
        // clearing one then setting another passes
        vm.prank(s[3]);
        uint64 clear = fund.proposeSetFlag(s[0], false);
        _approve(clear, _list(3, 4, 5, 6));
        vm.expectEmit(true, false, false, true);
        emit IIgneumFund.FlagSet(s[0], false);
        vm.prank(s[3]);
        fund.execute(clear);
        vm.prank(s[3]);
        uint64 set = fund.proposeSetFlag(s[3], true);
        _approve(set, _list(3, 4, 5, 6));
        vm.prank(s[3]);
        fund.execute(set);
        assertTrue(fund.companyConnected(s[3]));
        vm.expectRevert(abi.encodeWithSelector(IIgneumFund.BadSigner.selector, stranger));
        vm.prank(s[3]);
        fund.proposeSetFlag(stranger, true);
    }

    // ---------------------------------------------------------------- the signer change

    function test_replace_fiveOfSevenThenSixtyDays() public {
        address fresh = makeAddr("signer-h");
        vm.prank(s[3]);
        uint64 id = fund.proposeReplace(s[6], fresh, false);
        _approve(id, _list(3, 4, 5, 6));
        vm.expectRevert(abi.encodeWithSelector(IIgneumFund.BelowQuorum.selector, id, 4, 5));
        vm.prank(s[3]);
        fund.execute(id);
        vm.expectEmit(true, false, false, true);
        emit IIgneumFund.ReplaceReady(id, uint64(block.timestamp + 60 days));
        vm.prank(s[2]);
        fund.approve(id);
        vm.expectRevert(abi.encodeWithSelector(IIgneumFund.NotReady.selector, id, uint64(block.timestamp + 60 days)));
        vm.prank(s[3]);
        fund.execute(id);
        vm.warp(block.timestamp + 60 days);
        vm.expectEmit(true, true, false, true);
        emit IIgneumFund.SignerReplaced(s[6], fresh, false);
        vm.prank(s[3]);
        fund.execute(id);
        assertTrue(fund.isSigner(fresh));
        assertFalse(fund.isSigner(s[6]));
        assertEq(fund.signers().length, 7);
        // the old signer can no longer act; the new one can
        vm.expectRevert(abi.encodeWithSelector(IIgneumFund.OnlySigner.selector, s[6]));
        vm.prank(s[6]);
        fund.proposeFreeze();
        vm.prank(fresh);
        fund.proposeFreeze();
    }

    function test_replace_compositionCheckedAtExecution() public {
        // replacing an independent signer with a company-connected one would make four: refused at execution
        address fresh = makeAddr("signer-h");
        vm.prank(s[3]);
        uint64 id = fund.proposeReplace(s[6], fresh, true);
        uint256[] memory five = new uint256[](5);
        five[0] = 2;
        five[1] = 3;
        five[2] = 4;
        five[3] = 5;
        five[4] = 6;
        _approve(id, five);
        vm.warp(block.timestamp + 60 days);
        vm.expectEmit(true, false, false, true);
        emit IIgneumFund.Refused(id, IIgneumFund.Kind.Replace, "more company-connected signers than the size allows");
        vm.prank(s[3]);
        fund.execute(id);
        assertTrue(fund.isSigner(s[6]));
        // replacing a company-connected signer with a company-connected one keeps three: passes
        vm.prank(s[3]);
        uint64 swap = fund.proposeReplace(s[0], fresh, true);
        _approve(swap, five);
        vm.warp(block.timestamp + 60 days);
        vm.prank(s[3]);
        fund.execute(swap);
        assertTrue(fund.isSigner(fresh) && fund.companyConnected(fresh) && !fund.isSigner(s[0]));
    }

    function test_replace_approvalOfAReplacedSignerDoesNotCount() public {
        uint64 id = _spend(1 ether);
        _approve(id, _list(3, 4, 5, 6));
        // g is replaced before the spend executes
        address fresh = makeAddr("signer-h");
        vm.prank(s[3]);
        uint64 rep = fund.proposeReplace(s[6], fresh, false);
        uint256[] memory five = new uint256[](5);
        five[0] = 2;
        five[1] = 3;
        five[2] = 4;
        five[3] = 5;
        five[4] = 6;
        _approve(rep, five);
        vm.warp(block.timestamp + 60 days);
        vm.prank(s[3]);
        fund.execute(rep);
        vm.expectRevert(abi.encodeWithSelector(IIgneumFund.BelowQuorum.selector, id, 3, 4));
        vm.prank(s[3]);
        fund.execute(id);
    }

    // ---------------------------------------------------------------- the log and what the contract has not

    function test_log_everyActIsAnEvent() public {
        vm.recordLogs();
        uint64 id = _spend(1 ether);
        vm.prank(s[3]);
        fund.approve(id);
        vm.prank(s[3]);
        fund.revokeApproval(id);
        _approve(id, _list(3, 4, 5, 6));
        vm.prank(s[2]);
        fund.recuse(id);
        vm.prank(s[3]);
        fund.execute(id);
        Vm.Log[] memory logs = vm.getRecordedLogs();
        // Proposed, Approved, ApprovalRevoked, Approved x4, Recused, WindowOpened, Executed
        assertEq(logs.length, 10);
        assertEq(logs[0].topics[0], IIgneumFund.Proposed.selector);
        assertEq(logs[2].topics[0], IIgneumFund.ApprovalRevoked.selector);
        assertEq(logs[7].topics[0], IIgneumFund.Recused.selector);
        assertEq(logs[8].topics[0], IIgneumFund.WindowOpened.selector);
        assertEq(logs[9].topics[0], IIgneumFund.Executed.selector);
    }

    function test_nothingElse_noOwnerPauseFeeOrUpgrade() public {
        // no owner, pause, fee, upgrade, bond or vote entry point: the selectors do not exist
        bytes4[9] memory absent = [
            bytes4(keccak256("owner()")),
            bytes4(keccak256("pause()")),
            bytes4(keccak256("unpause()")),
            bytes4(keccak256("upgradeTo(address)")),
            bytes4(keccak256("setFee(uint256)")),
            bytes4(keccak256("bond(bytes32,uint256)")),
            bytes4(keccak256("vote(uint256,bool)")),
            bytes4(keccak256("transferOwnership(address)")),
            bytes4(keccak256("withdraw(uint256)"))
        ];
        for (uint256 i = 0; i < absent.length; i++) {
            (bool ok,) = address(fund).call(abi.encodeWithSelector(absent[i]));
            assertFalse(ok);
        }
        // the signers cannot move the balance by any path but a spend proposal: a direct call carries nothing
        uint256 before = address(fund).balance;
        vm.prank(s[0]);
        (bool okCall,) = address(fund).call("");
        assertTrue(okCall);
        assertEq(address(fund).balance, before);
    }

    // ---------------------------------------------------------------- a payment to the company

    address internal labs = makeAddr("igneum-labs");

    function _companySpend() internal returns (uint64 id) {
        vm.prank(s[3]);
        id = fund.proposeSpend(labs, 10 ether, SCOPE, 0, 0, true);
    }

    function test_companyPayee_flaggedMinorityDoesNotCount() public {
        // a, b flagged plus d, e, f: five approvals, two flagged; three unflagged is below the quorum
        uint64 id = _companySpend();
        uint256[] memory five = new uint256[](5);
        five[0] = 0;
        five[1] = 1;
        five[2] = 3;
        five[3] = 4;
        five[4] = 5;
        _approve(id, five);
        address[] memory ex = new address[](2);
        ex[0] = s[0];
        ex[1] = s[1];
        vm.expectEmit(true, false, false, true);
        emit IIgneumFund.CompanyPayeeApprovalsExcluded(id, ex, 3, 4);
        vm.expectEmit(true, false, false, true);
        emit IIgneumFund.Refused(id, IIgneumFund.Kind.Spend, "a payment to the company needs the spend quorum of approvals from signers not connected to it");
        vm.prank(s[3]);
        fund.execute(id);
        assertEq(labs.balance, 0);
    }

    function test_companyPayee_flaggedMajorityRefused() public {
        uint64 id = _companySpend();
        _approve(id, _list(0, 1, 2, 3));
        vm.expectEmit(true, false, false, true);
        emit IIgneumFund.Refused(id, IIgneumFund.Kind.Spend, "a payment to the company needs the spend quorum of approvals from signers not connected to it");
        vm.prank(s[0]);
        fund.execute(id);
        assertEq(labs.balance, 0);
    }

    function test_companyPayee_wholeUnflaggedSetPays() public {
        // the four independent signers alone carry it, with the flagged three approving too (excluded, logged)
        uint64 id = _companySpend();
        uint256[] memory seven = new uint256[](7);
        for (uint256 i = 0; i < 7; i++) {
            seven[i] = i;
        }
        _approve(id, seven);
        vm.prank(s[3]);
        fund.execute(id);
        assertEq(labs.balance, 10 ether);
    }

    function test_companyPayee_markedByAnySignerBeforeExecution() public {
        // the proposer left the flag off; one signer marks it; the flagged approvals then do not count
        vm.prank(s[0]);
        uint64 id = fund.proposeSpend(labs, 10 ether, SCOPE, 0, 0, false);
        _approve(id, _list(0, 1, 2, 3));
        vm.expectEmit(true, true, false, true);
        emit IIgneumFund.CompanyPayeeMarked(id, s[6]);
        vm.prank(s[6]);
        fund.markCompanyPayee(id);
        assertTrue(fund.proposals(id).companyPayee);
        vm.expectEmit(true, false, false, true);
        emit IIgneumFund.Refused(id, IIgneumFund.Kind.Spend, "a payment to the company needs the spend quorum of approvals from signers not connected to it");
        vm.prank(s[0]);
        fund.execute(id);
        assertEq(labs.balance, 0);
        vm.expectRevert(abi.encodeWithSelector(IIgneumFund.OnlySigner.selector, stranger));
        vm.prank(stranger);
        fund.markCompanyPayee(id);
    }

    // ---------------------------------------------------------------- the migrate path (a new mandate)

    function test_migrate_fiveOfSevenThenSixtyDaysMovesTheWholeBalance() public {
        IgneumFund successor = _successor();
        vm.prank(s[3]);
        uint64 id = fund.proposeMigrate(address(successor));
        _approve(id, _list(3, 4, 5, 6));
        vm.expectRevert(abi.encodeWithSelector(IIgneumFund.BelowQuorum.selector, id, 4, 5));
        vm.prank(s[3]);
        fund.execute(id);
        vm.prank(s[2]);
        fund.approve(id);
        vm.expectRevert(abi.encodeWithSelector(IIgneumFund.NotReady.selector, id, uint64(block.timestamp + 60 days)));
        vm.prank(s[3]);
        fund.execute(id);
        vm.warp(block.timestamp + 60 days);
        vm.expectEmit(true, true, false, true);
        emit IIgneumFund.Migrated(id, address(successor), 1000 ether);
        vm.prank(s[3]);
        fund.execute(id);
        assertEq(address(fund).balance, 0);
        assertEq(address(successor).balance, 1000 ether);
        assertTrue(fund.proposals(id).closed);
    }

    function test_migrate_refusals() public {
        // the successor must be a contract that is not this one and not a signer
        vm.expectRevert(abi.encodeWithSelector(IIgneumFund.BadInput.selector, "the successor"));
        vm.prank(s[3]);
        fund.proposeMigrate(payee);
        vm.expectRevert(abi.encodeWithSelector(IIgneumFund.BadInput.selector, "the successor"));
        vm.prank(s[3]);
        fund.proposeMigrate(address(fund));
        IgneumFund successor = _successor();
        // frozen at execution: refused by event, the balance stays
        vm.prank(s[3]);
        uint64 id = fund.proposeMigrate(address(successor));
        uint256[] memory five = new uint256[](5);
        five[0] = 2;
        five[1] = 3;
        five[2] = 4;
        five[3] = 5;
        five[4] = 6;
        _approve(id, five);
        vm.warp(block.timestamp + 60 days);
        vm.prank(s[0]);
        uint64 f = fund.proposeFreeze();
        _approve(f, _list(0, 1, 0, 1));
        vm.prank(s[0]);
        fund.execute(f);
        vm.expectEmit(true, false, false, true);
        emit IIgneumFund.Refused(id, IIgneumFund.Kind.Migrate, "frozen");
        vm.prank(s[3]);
        fund.execute(id);
        assertEq(address(fund).balance, 1000 ether);
        // a company-connected majority of the five: a, b, c, d, e (three flagged of five) refused
        vm.warp(block.timestamp + 30 days);
        vm.prank(s[3]);
        uint64 m = fund.proposeMigrate(address(successor));
        uint256[] memory abcde = new uint256[](5);
        for (uint256 i = 0; i < 5; i++) {
            abcde[i] = i;
        }
        _approve(m, abcde);
        vm.warp(block.timestamp + 60 days);
        vm.expectEmit(true, false, false, true);
        emit IIgneumFund.Refused(m, IIgneumFund.Kind.Migrate, "the approving signers are a majority company-connected");
        vm.prank(s[3]);
        fund.execute(m);
        assertEq(address(fund).balance, 1000 ether);
    }

    function _successor() internal returns (IgneumFund successor) {
        address[] memory signers = new address[](7);
        bool[] memory flags = new bool[](7);
        for (uint256 i = 0; i < 7; i++) {
            signers[i] = s[i];
        }
        successor = new IgneumFund(signers, flags);
    }

    function test_window_openByAnyoneAtTheBoundary() public {
        vm.prank(stranger);
        fund.openWindow();
        assertEq(fund.windowBalanceAtStart(), 1000 ether);
        vm.deal(address(fund), 2000 ether);
        vm.prank(stranger);
        fund.openWindow();
        assertEq(fund.windowBalanceAtStart(), 1000 ether, "the same window keeps its snapshot");
        vm.warp(block.timestamp + 90 days);
        vm.prank(stranger);
        fund.openWindow();
        assertEq(fund.windowIndex(), 1);
        assertEq(fund.windowBalanceAtStart(), 2000 ether);
    }

    // ---------------------------------------------------------------- the size table and Add (GOVERNANCE 8)

    function _three() internal returns (IgneumFund f) {
        address[] memory three = new address[](3);
        bool[] memory flags3 = new bool[](3);
        for (uint256 i = 0; i < 3; i++) {
            three[i] = s[i];
        }
        flags3[0] = true; // one company-connected at three
        f = new IgneumFund(three, flags3);
        vm.deal(address(f), 1000 ether);
    }

    function test_size_threeOpensWithTheTablesQuorums() public {
        IgneumFund f = _three();
        assertEq(f.SIGNERS(), 3);
        assertEq(f.SPEND_QUORUM(), 2);
        assertEq(f.FREEZE_QUORUM(), 1);
        assertEq(f.CHANGE_QUORUM(), 2);
        assertEq(f.MAX_COMPANY_CONNECTED(), 1);
        (uint256 sp, uint256 fr, uint256 ch, uint256 cc) = f.quorumsAt(7);
        assertEq(sp, 4);
        assertEq(fr, 2);
        assertEq(ch, 5);
        assertEq(cc, 3);
        // a spend at three: two of three; the one flagged signer is not a majority of two
        vm.prank(s[1]);
        uint64 id = f.proposeSpend(payee, 10 ether, SCOPE, 0, 0, false);
        vm.prank(s[1]);
        f.approve(id);
        vm.prank(s[2]);
        f.approve(id);
        vm.prank(s[1]);
        f.execute(id);
        assertEq(payee.balance, 10 ether);
    }

    function test_size_threeRefusesTwoFlagged() public {
        address[] memory three = new address[](3);
        bool[] memory flags3 = new bool[](3);
        for (uint256 i = 0; i < 3; i++) {
            three[i] = s[i];
        }
        flags3[0] = true;
        flags3[1] = true;
        vm.expectRevert(abi.encodeWithSelector(IIgneumFund.TooManyCompanyConnected.selector, 2));
        new IgneumFund(three, flags3);
    }

    function test_add_twoOfThreeThenSixtyDaysStepsTheQuorums() public {
        IgneumFund f = _three();
        address fresh = makeAddr("signer-new");
        vm.prank(s[1]);
        uint64 id = f.proposeAdd(fresh, false);
        vm.prank(s[1]);
        f.approve(id);
        vm.expectRevert(abi.encodeWithSelector(IIgneumFund.BelowQuorum.selector, id, 1, 2));
        vm.prank(s[1]);
        f.execute(id);
        vm.expectEmit(true, false, false, true);
        emit IIgneumFund.ReplaceReady(id, uint64(block.timestamp + 60 days));
        vm.prank(s[2]);
        f.approve(id);
        vm.expectRevert(abi.encodeWithSelector(IIgneumFund.NotReady.selector, id, uint64(block.timestamp + 60 days)));
        vm.prank(s[1]);
        f.execute(id);
        vm.warp(block.timestamp + 60 days);
        vm.expectEmit(true, false, false, true);
        emit IIgneumFund.SignerAdded(fresh, false, 4);
        vm.prank(s[1]);
        f.execute(id);
        assertTrue(f.isSigner(fresh));
        assertEq(f.signers().length, 4);
        assertEq(f.SIGNERS(), 4);
        assertEq(f.SPEND_QUORUM(), 3);
        assertEq(f.CHANGE_QUORUM(), 3);
        assertEq(f.MAX_COMPANY_CONNECTED(), 1);
        // at four a spend needs three: two no longer pass
        vm.prank(s[1]);
        uint64 sp = f.proposeSpend(payee, 10 ether, SCOPE, 0, 0, false);
        vm.prank(s[1]);
        f.approve(sp);
        vm.prank(s[2]);
        f.approve(sp);
        vm.expectRevert(abi.encodeWithSelector(IIgneumFund.BelowQuorum.selector, sp, 2, 3));
        vm.prank(s[1]);
        f.execute(sp);
        vm.prank(fresh);
        f.approve(sp);
        vm.prank(s[1]);
        f.execute(sp);
        assertEq(payee.balance, 10 ether);
    }

    function test_add_refusedBeyondTheNextSizesCompanyBound() public {
        IgneumFund f = _three();
        // at three one is flagged (a); a second flagged seat at four is beyond the table's bound of one
        address fresh = makeAddr("signer-new-flagged");
        vm.prank(s[1]);
        uint64 id = f.proposeAdd(fresh, true);
        vm.prank(s[1]);
        f.approve(id);
        vm.prank(s[2]);
        f.approve(id);
        vm.warp(block.timestamp + 60 days);
        vm.expectEmit(true, false, false, true);
        emit IIgneumFund.Refused(id, IIgneumFund.Kind.Add, "more company-connected signers than the next size allows");
        vm.prank(s[1]);
        f.execute(id);
        assertFalse(f.isSigner(fresh));
        assertEq(f.SIGNERS(), 3);
    }

    function test_replace_needsEveryKeyBelowFive() public {
        IgneumFund f = _three();
        assertEq(f.REPLACE_QUORUM(), 3);
        assertEq(fund.REPLACE_QUORUM(), 5);
        address fresh = makeAddr("signer-pair-own");
        // the pair at three cannot replace the honest third at two: the third key is needed
        vm.prank(s[1]);
        uint64 id = f.proposeReplace(s[0], fresh, false);
        vm.prank(s[1]);
        f.approve(id);
        vm.prank(s[2]);
        f.approve(id);
        vm.warp(block.timestamp + 61 days);
        vm.expectRevert(abi.encodeWithSelector(IIgneumFund.BelowQuorum.selector, id, 2, 3));
        vm.prank(s[1]);
        f.execute(id);
        vm.expectEmit(true, false, false, true);
        emit IIgneumFund.ReplaceReady(id, uint64(block.timestamp + 60 days));
        vm.prank(s[0]);
        f.approve(id);
    }

    function test_migrate_needsEveryKeyBelowFive() public {
        IgneumFund f = _three();
        assertEq(f.MIGRATE_QUORUM(), 3);
        assertEq(fund.MIGRATE_QUORUM(), 5);
        address[] memory three = new address[](3);
        bool[] memory flags3 = new bool[](3);
        for (uint256 i = 0; i < 3; i++) {
            three[i] = s[i];
        }
        IgneumFund succ = new IgneumFund(three, flags3);
        // two colluding signers at three cannot migrate the whole balance: the third key is needed
        vm.prank(s[1]);
        uint64 id = f.proposeMigrate(address(succ));
        vm.prank(s[1]);
        f.approve(id);
        vm.prank(s[2]);
        f.approve(id);
        vm.warp(block.timestamp + 61 days);
        vm.expectRevert(abi.encodeWithSelector(IIgneumFund.BelowQuorum.selector, id, 2, 3));
        vm.prank(s[1]);
        f.execute(id);
        // the third approval sets the sixty days from then
        vm.expectEmit(true, false, false, true);
        emit IIgneumFund.ReplaceReady(id, uint64(block.timestamp + 60 days));
        vm.prank(s[0]);
        f.approve(id);
    }

    function test_add_refusedAtSeven() public {
        address fresh = makeAddr("signer-eighth");
        vm.expectRevert(abi.encodeWithSelector(IIgneumFund.BadInput.selector, "the set is seven"));
        vm.prank(s[3]);
        fund.proposeAdd(fresh, false);
    }
}
