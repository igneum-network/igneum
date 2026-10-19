// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {IERC20} from "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import {SafeERC20} from "@openzeppelin/contracts/token/ERC20/utils/SafeERC20.sol";
import {ReentrancyGuard} from "@openzeppelin/contracts/utils/ReentrancyGuard.sol";

import {IIgneumEscrow} from "../interfaces/IIgneumEscrow.sol";
import {PayTypes} from "./PayTypes.sol";

/// @title IgneumEscrow
/// @notice Conditional payments in two templates, with an optional arbiter.
/// @dev The payer funds; then one of these ends it:
///      - `release` by the payer: the payee is paid;
///      - `refund` by the payee: the payer is repaid;
///      - `timeout` by anyone after the deadline, if nobody disputed: `PayeeClaims` pays the payee ("pay unless the
///        payer objects in time"), `PayerRefunds` repays the payer ("refund unless released in time");
///      - `dispute` by either party before the deadline, when an arbiter was named: the timeout is frozen and only
///        `resolve` by the arbiter ends it, splitting by basis points. The payer may still `release` and the payee
///        may still `refund` during a dispute (giving in ends it).
///      Without an arbiter there is no dispute: the template's timeout rule is the whole agreement.
///      The contract enforces its own rules; it does not make any off-chain promise true.
///      Fee-on-transfer ERC-20s are rejected at `open` (the amount received must equal the amount named).
contract IgneumEscrow is IIgneumEscrow, ReentrancyGuard {
    using SafeERC20 for IERC20;

    uint256 private constant BPS = 10_000;

    mapping(bytes32 id => Escrow) private _escrows;
    mapping(address to => uint256) public override claimable;
    uint256 public count;

    /// @inheritdoc IIgneumEscrow
    function open(address payee, address arbiter, address token, uint256 amount, uint64 deadline, OnTimeout onTimeout)
        external
        payable
        override
        nonReentrant
        returns (bytes32 id)
    {
        if (payee == address(0)) revert ZeroPayee();
        if (amount == 0) revert ZeroAmount();
        if (deadline <= block.timestamp) revert BadDeadline();
        if (token == address(0)) {
            if (msg.value != amount) revert WrongValue(amount, msg.value);
        } else {
            if (msg.value != 0) revert WrongValue(0, msg.value);
            uint256 before = IERC20(token).balanceOf(address(this));
            IERC20(token).safeTransferFrom(msg.sender, address(this), amount);
            // the strict check is the point: a token that delivers less than named is refused
            // forge-lint: disable-next-line(incorrect-strict-equality)
            if (IERC20(token).balanceOf(address(this)) - before != amount) revert FeeOnTransferToken();
        }
        id = keccak256(abi.encode(block.chainid, address(this), msg.sender, count++));
        _escrows[id] = Escrow({
            payer: msg.sender,
            payee: payee,
            arbiter: arbiter,
            token: token,
            amount: amount,
            deadline: deadline,
            onTimeout: onTimeout,
            state: State.Funded
        });
        emit Opened(id, msg.sender, payee, arbiter, token, amount, deadline, onTimeout);
    }

    /// @inheritdoc IIgneumEscrow
    function release(bytes32 id) external override nonReentrant {
        Escrow storage e = _load(id);
        if (e.payer != msg.sender) revert OnlyPayer(id);
        if (e.state != State.Funded && e.state != State.Disputed) revert WrongState(id, e.state);
        e.state = State.Released;
        _payout(e.token, e.payee, e.amount);
        emit Released(id, e.amount);
    }

    /// @inheritdoc IIgneumEscrow
    function refund(bytes32 id) external override nonReentrant {
        Escrow storage e = _load(id);
        if (e.payee != msg.sender) revert OnlyPayee(id);
        if (e.state != State.Funded && e.state != State.Disputed) revert WrongState(id, e.state);
        e.state = State.Refunded;
        _payout(e.token, e.payer, e.amount);
        emit Refunded(id, e.amount);
    }

    /// @inheritdoc IIgneumEscrow
    function timeout(bytes32 id) external override nonReentrant {
        Escrow storage e = _load(id);
        if (e.state != State.Funded) revert WrongState(id, e.state);
        if (block.timestamp < e.deadline) revert BeforeDeadline(id, e.deadline);
        if (e.onTimeout == OnTimeout.PayeeClaims) {
            e.state = State.Released;
            _payout(e.token, e.payee, e.amount);
            emit Released(id, e.amount);
        } else {
            e.state = State.Refunded;
            _payout(e.token, e.payer, e.amount);
            emit Refunded(id, e.amount);
        }
        emit TimedOut(id, e.onTimeout);
    }

    /// @inheritdoc IIgneumEscrow
    function dispute(bytes32 id) external override {
        Escrow storage e = _load(id);
        if (msg.sender != e.payer && msg.sender != e.payee) revert OnlyParty(id);
        if (e.arbiter == address(0)) revert NoArbiter(id);
        if (e.state != State.Funded) revert WrongState(id, e.state);
        if (block.timestamp >= e.deadline) revert AfterDeadline(id, e.deadline);
        e.state = State.Disputed;
        emit Disputed(id, msg.sender);
    }

    /// @inheritdoc IIgneumEscrow
    function resolve(bytes32 id, uint256 payeeShareBps) external override nonReentrant {
        Escrow storage e = _load(id);
        if (msg.sender != e.arbiter) revert OnlyArbiter(id);
        if (e.state != State.Disputed) revert WrongState(id, e.state);
        if (payeeShareBps > BPS) revert BadShare(payeeShareBps);
        e.state = State.Resolved;
        uint256 toPayee = (e.amount * payeeShareBps) / BPS;
        uint256 toPayer = e.amount - toPayee;
        if (toPayee != 0) _payout(e.token, e.payee, toPayee);
        if (toPayer != 0) _payout(e.token, e.payer, toPayer);
        emit Resolved(id, toPayee, toPayer);
    }

    /// @inheritdoc IIgneumEscrow
    function claim() external override nonReentrant {
        uint256 amount = claimable[msg.sender];
        if (amount == 0) revert NothingToClaim();
        claimable[msg.sender] = 0;
        // the return data is discarded at this site, so the bounded call copies none (evm-pay's F-13)
        // forge-lint: disable-next-line(return-bomb)
        (bool ok,) = msg.sender.call{value: amount, gas: PayTypes.PUSH_GAS}("");
        if (!ok) revert TransferFailed();
        emit Claimed(msg.sender, amount);
    }

    /// @inheritdoc IIgneumEscrow
    function escrow(bytes32 id) external view override returns (Escrow memory) {
        return _escrows[id];
    }

    // ---------------------------------------------------------------- internal

    function _load(bytes32 id) private view returns (Escrow storage e) {
        e = _escrows[id];
        if (e.payer == address(0)) revert Unknown(id);
    }

    function _payout(address token, address to, uint256 amount) private {
        if (token == address(0)) {
            // every caller of _payout is nonReentrant and has set the escrow's state first; the credit is written
            // only when the transfer failed, so there is no window in which a re-entered call sees stale state
            // forge-lint: disable-next-line(reentrancy-eth)
            // the return data is discarded at this site, so the bounded call copies none (evm-pay's F-13)
            // forge-lint: disable-next-line(return-bomb)
            (bool ok,) = to.call{value: amount, gas: PayTypes.PUSH_GAS}("");
            if (!ok) {
                claimable[to] += amount;
                emit Credited(to, amount);
            }
        } else {
            IERC20(token).safeTransfer(to, amount);
        }
    }
}
