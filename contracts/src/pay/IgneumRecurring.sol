// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {IERC20} from "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import {SafeERC20} from "@openzeppelin/contracts/token/ERC20/utils/SafeERC20.sol";
import {ReentrancyGuard} from "@openzeppelin/contracts/utils/ReentrancyGuard.sol";

import {IIgneumRecurring} from "../interfaces/IIgneumRecurring.sol";
import {PayTypes} from "./PayTypes.sol";

/// @title IgneumRecurring
/// @notice Bounded recurring authorisations with permissionless collection and an explicit operator fee.
/// @dev The payer sets every bound: `amountPerPeriod` to the payee, `operatorFee` to whoever submits the collection,
///      `period`, `start`, `end`, and `totalCap`, against which both the amount and the fee count. So the cap is the
///      whole of what can ever leave under one authorisation.
///      - Period i covers [start + i * period, start + (i + 1) * period). A period is collected at most once; a
///        period that passes uncollected is forgone, never caught up.
///      - Native IGN is held here as the payer's deposit (a contract cannot pull native coin). ERC-20s are pulled
///        from the payer by allowance at collection time; nothing is held.
///      - Revocation is immediate and refunds the whole native deposit. The payer may also withdraw part of the
///        deposit at any time; a short deposit simply makes the next collection fail.
///      - No owner, no upgrade, no pause, no protocol fee.
contract IgneumRecurring is IIgneumRecurring, ReentrancyGuard {
    using SafeERC20 for IERC20;

    mapping(bytes32 id => Authorisation) private _auths;
    mapping(bytes32 id => mapping(uint256 periodIndex => bool)) public override collected;
    mapping(address payee => uint256) public override claimable;
    uint256 public count;

    // ---------------------------------------------------------------- payer

    /// @inheritdoc IIgneumRecurring
    function authorise(
        address payee,
        address token,
        uint256 amountPerPeriod,
        uint256 operatorFee,
        uint64 period,
        uint64 start,
        uint64 end,
        uint256 totalCap
    ) external payable override returns (bytes32 id) {
        if (payee == address(0)) revert ZeroPayee();
        if (amountPerPeriod == 0) revert ZeroAmount();
        if (period == 0) revert BadPeriod();
        // a uint64 holds timestamps for longer than the chain will exist
        // forge-lint: disable-next-line(unsafe-typecast)
        if (start == 0) start = uint64(block.timestamp);
        if (end != 0 && end <= start) revert BadWindow();
        if (totalCap < amountPerPeriod + operatorFee) revert CapBelowOnePeriod();
        if (token != address(0) && msg.value != 0) revert WrongValue(0, msg.value);

        id = keccak256(abi.encode(block.chainid, address(this), msg.sender, count++));
        _auths[id] = Authorisation({
            payer: msg.sender,
            payee: payee,
            token: token,
            amountPerPeriod: _u128(amountPerPeriod),
            operatorFee: _u128(operatorFee),
            period: period,
            start: start,
            end: end,
            totalCap: totalCap,
            spent: 0,
            deposit: msg.value,
            revoked: false
        });
        emit Authorised(id, msg.sender, payee, token, amountPerPeriod, operatorFee, period, start, end, totalCap);
        if (msg.value != 0) emit Deposited(id, msg.value);
    }

    /// @inheritdoc IIgneumRecurring
    function deposit(bytes32 id) external payable override {
        Authorisation storage a = _load(id);
        if (a.payer != msg.sender) revert OnlyPayer(id);
        if (a.token != address(0)) revert NotNative(id);
        if (a.revoked) revert IsRevoked(id);
        a.deposit += msg.value;
        emit Deposited(id, msg.value);
    }

    /// @inheritdoc IIgneumRecurring
    function withdraw(bytes32 id, uint256 amount) external override nonReentrant {
        Authorisation storage a = _load(id);
        if (a.payer != msg.sender) revert OnlyPayer(id);
        if (a.token != address(0)) revert NotNative(id);
        if (amount > a.deposit) revert InsufficientDeposit(id, a.deposit, amount);
        a.deposit -= amount;
        _sendOrRevert(msg.sender, amount);
        emit Withdrawn(id, amount);
    }

    /// @inheritdoc IIgneumRecurring
    function revoke(bytes32 id) external override nonReentrant {
        Authorisation storage a = _load(id);
        if (a.payer != msg.sender) revert OnlyPayer(id);
        if (a.revoked) revert IsRevoked(id);
        a.revoked = true;
        uint256 refund = a.deposit;
        a.deposit = 0;
        if (refund != 0) _sendOrRevert(msg.sender, refund);
        emit Revoked(id, refund);
    }

    // ---------------------------------------------------------------- anyone

    /// @inheritdoc IIgneumRecurring
    function collect(bytes32 id) external override nonReentrant {
        Authorisation storage a = _load(id);
        if (a.revoked) revert IsRevoked(id);
        if (block.timestamp < a.start) revert NotStarted(id, a.start);
        if (a.end != 0 && block.timestamp >= a.end) revert Ended(id, a.end);
        uint256 index = (block.timestamp - a.start) / a.period;
        if (collected[id][index]) revert AlreadyCollected(id, index);
        uint256 amount = a.amountPerPeriod;
        uint256 fee = a.operatorFee;
        uint256 total = amount + fee;
        if (a.spent + total > a.totalCap) revert CapReached(id, a.spent, a.totalCap);

        collected[id][index] = true;
        a.spent += total;

        if (a.token == address(0)) {
            if (a.deposit < total) revert InsufficientDeposit(id, a.deposit, total);
            a.deposit -= total;
            _payNative(a.payee, amount);
            if (fee != 0) _sendOrRevert(msg.sender, fee);
        } else {
            IERC20(a.token).safeTransferFrom(a.payer, a.payee, amount);
            if (fee != 0) IERC20(a.token).safeTransferFrom(a.payer, msg.sender, fee);
        }
        emit Collected(id, index, msg.sender, amount, fee);
    }

    /// @inheritdoc IIgneumRecurring
    function claim() external override nonReentrant {
        uint256 amount = claimable[msg.sender];
        if (amount == 0) revert NothingToClaim();
        claimable[msg.sender] = 0;
        _sendOrRevert(msg.sender, amount);
        emit Claimed(msg.sender, amount);
    }

    // ---------------------------------------------------------------- views

    /// @inheritdoc IIgneumRecurring
    function authorisation(bytes32 id) external view override returns (Authorisation memory) {
        return _auths[id];
    }

    /// @inheritdoc IIgneumRecurring
    function currentPeriod(bytes32 id) external view override returns (uint256) {
        Authorisation storage a = _load(id);
        if (block.timestamp < a.start) revert NotStarted(id, a.start);
        return (block.timestamp - a.start) / a.period;
    }

    /// @notice Whether `collect(id)` would succeed now, and the reason when it would not (for operators' schedulers).
    function collectable(bytes32 id) external view returns (bool ok, string memory reason) {
        Authorisation storage a = _auths[id];
        if (a.payer == address(0)) return (false, "unknown");
        if (a.revoked) return (false, "revoked");
        if (block.timestamp < a.start) return (false, "not started");
        if (a.end != 0 && block.timestamp >= a.end) return (false, "ended");
        uint256 index = (block.timestamp - a.start) / a.period;
        if (collected[id][index]) return (false, "period collected");
        uint256 total = uint256(a.amountPerPeriod) + a.operatorFee;
        if (a.spent + total > a.totalCap) return (false, "cap reached");
        if (a.token == address(0)) {
            if (a.deposit < total) return (false, "deposit short");
        } else {
            if (IERC20(a.token).allowance(a.payer, address(this)) < total) return (false, "allowance short");
            if (IERC20(a.token).balanceOf(a.payer) < total) return (false, "balance short");
        }
        return (true, "");
    }

    // ---------------------------------------------------------------- internal

    function _load(bytes32 id) private view returns (Authorisation storage a) {
        a = _auths[id];
        if (a.payer == address(0)) revert Unknown(id);
    }

    function _payNative(address to, uint256 amount) private {
        // the return data is discarded at this site, so the bounded call copies none (evm-pay's F-13)
        // forge-lint: disable-next-line(return-bomb)
        (bool ok,) = to.call{value: amount, gas: PayTypes.PUSH_GAS}("");
        if (!ok) {
            claimable[to] += amount;
            emit Credited(to, amount);
        }
    }

    function _sendOrRevert(address to, uint256 amount) private {
        // the return data is discarded at this site, so the bounded call copies none (evm-pay's F-13)
        // forge-lint: disable-next-line(return-bomb)
        (bool ok,) = to.call{value: amount, gas: PayTypes.PUSH_GAS}("");
        if (!ok) revert TransferFailed();
    }

    function _u128(uint256 v) private pure returns (uint128) {
        if (v > type(uint128).max) revert AmountTooLarge(v);
        // checked just above
        // forge-lint: disable-next-line(unsafe-typecast)
        return uint128(v);
    }
}
