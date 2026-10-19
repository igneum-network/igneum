// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {ReentrancyGuard} from "@openzeppelin/contracts/utils/ReentrancyGuard.sol";

import {IIgneumWork} from "../work/IIgneumWork.sol";

/// @title WorkPay
/// @notice The Consensus Work Interface's reference application (docs/work-api/INTERFACE.md section 10): anyone
///         funds a window's budget; once the window is finalized and written, anyone presents a recipient with its
///         proof and the contract pays budget x weight / totalWork to the recipient, once. No owner, no pause, no
///         fee, no list in storage, no oracle beyond the IgneumWork contract. A reorganisation cannot double-pay: a
///         window's root cannot change below the finality depth, and a claim cannot repeat. The budget's rounding
///         remainder (under `entries` wei) stays in the pot and is reported by `remainder`.
/// @dev Funding closes at the first claim of a window so every recipient is paid against one budget. A recipient
///      whose receive reverts is credited under `claimable` and withdraws later, so no recipient blocks a window.
contract WorkPay is ReentrancyGuard {
    string public constant LABEL = "development-chain work; no monetary value";

    IIgneumWork public immutable work;

    mapping(uint64 => uint256) public budget;
    mapping(uint64 => uint256) public paidOut;
    mapping(uint64 => uint32) public claims;
    mapping(uint64 => mapping(address => bool)) public paid;
    mapping(address => uint256) public claimable;

    event Funded(uint64 indexed window, address indexed from, uint256 amount, uint256 budget);
    event Paid(uint64 indexed window, address indexed account, uint256 weight, uint256 amount);
    event Credited(address indexed account, uint256 amount);
    event Withdrawn(address indexed account, uint256 amount);

    error NothingSent();
    error FundingClosed(uint64 window);
    error NoBudget(uint64 window);
    error AlreadyPaid(uint64 window, address account);
    error ZeroWeight();
    error BadProof(uint64 window, address account);
    error LengthMismatch();
    error NothingToWithdraw(address account);
    error TransferFailed(address to);

    constructor(IIgneumWork work_) {
        work = work_;
    }

    /// Adds to a window's budget. Allowed before or after the window, by anyone, until its first claim.
    function fund(uint64 w) external payable {
        if (msg.value == 0) revert NothingSent();
        if (claims[w] != 0) revert FundingClosed(w);
        budget[w] += msg.value;
        emit Funded(w, msg.sender, msg.value, budget[w]);
    }

    /// Pays one recipient of window w its share of the budget against its proof.
    function claim(uint64 w, address account, uint256 weight, uint256 index, bytes32[] calldata siblings)
        external
        nonReentrant
    {
        (uint256 total,) = _windowTotals(w);
        _claim(w, total, account, weight, index, siblings);
    }

    /// Pays several recipients of window w in one transaction, bounded by the caller's gas.
    function claimMany(
        uint64 w,
        address[] calldata accounts,
        uint256[] calldata weights,
        uint256[] calldata indices,
        bytes32[][] calldata siblings
    ) external nonReentrant {
        if (accounts.length != weights.length || accounts.length != indices.length || accounts.length != siblings.length) {
            revert LengthMismatch();
        }
        (uint256 total,) = _windowTotals(w);
        for (uint256 i = 0; i < accounts.length; i++) {
            _claim(w, total, accounts[i], weights[i], indices[i], siblings[i]);
        }
    }

    /// Withdraws what a failed receive left under `claimable`.
    function withdraw() external nonReentrant {
        uint256 amount = claimable[msg.sender];
        if (amount == 0) revert NothingToWithdraw(msg.sender);
        claimable[msg.sender] = 0;
        (bool ok,) = msg.sender.call{value: amount}("");
        if (!ok) revert TransferFailed(msg.sender);
        emit Withdrawn(msg.sender, amount);
    }

    // ------------------------------------------------------------------ views

    /// What a recipient with `weight` in window w is paid from the current budget.
    function payout(uint64 w, uint256 weight) external view returns (uint256) {
        (uint256 total,) = _windowTotals(w);
        if (total == 0) return 0;
        return budget[w] * weight / total;
    }

    /// The budget not yet paid out: before the claims it is the budget; after every claim it is the rounding dust.
    function remainder(uint64 w) external view returns (uint256) {
        return budget[w] - paidOut[w];
    }

    /// True once every entry of the window has claimed.
    function settled(uint64 w) external view returns (bool) {
        (, uint32 entries) = _windowTotals(w);
        return claims[w] == entries;
    }

    /// The work source's own label ("consensus", or the stand-in's sentence).
    function source() external view returns (string memory) {
        return work.label();
    }

    // ------------------------------------------------------------------ internal

    function _windowTotals(uint64 w) private view returns (uint256 total, uint32 entries) {
        // the root, end hash, depth and version are the proof's business (verifyWork reads them in IgneumWork)
        // forge-lint: disable-next-line(unused-return)
        (, total,, entries,,) = work.window(w);
    }

    function _claim(uint64 w, uint256 total, address account, uint256 weight, uint256 index, bytes32[] calldata siblings)
        private
    {
        uint256 b = budget[w];
        // _claim runs inside claimMany's loop by design: a batch is bounded by the caller's gas and one bad entry
        // reverts the whole batch (nothing half-paid); the external calls are the work root's read and the payment
        // forge-lint: disable-next-line(require-revert-in-loop)
        if (b == 0) revert NoBudget(w);
        // forge-lint: disable-next-line(require-revert-in-loop)
        if (weight == 0) revert ZeroWeight();
        // forge-lint: disable-next-line(require-revert-in-loop)
        if (paid[w][account]) revert AlreadyPaid(w, account);
        // forge-lint: disable-next-line(require-revert-in-loop,calls-loop)
        if (total == 0 || !work.verifyWork(w, account, weight, index, siblings)) revert BadProof(w, account);
        uint256 amount = b * weight / total;
        paid[w][account] = true;
        claims[w] += 1;
        paidOut[w] += amount;
        emit Paid(w, account, weight, amount);
        if (amount == 0) return;
        // forge-lint: disable-next-line(calls-loop)
        (bool ok,) = account.call{value: amount}("");
        if (!ok) {
            claimable[account] += amount;
            emit Credited(account, amount);
        }
    }
}
