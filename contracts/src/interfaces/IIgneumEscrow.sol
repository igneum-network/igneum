// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

/// @title IIgneumEscrow
/// @notice Two templates of conditional payment with an optional arbiter. The contract enforces its own rules only.
interface IIgneumEscrow {
    /// @notice What happens at the deadline when nobody acted and nobody disputed.
    enum OnTimeout {
        PayeeClaims,
        PayerRefunds
    }

    enum State {
        None,
        Funded,
        Disputed,
        Released,
        Refunded,
        Resolved
    }

    struct Escrow {
        address payer;
        address payee;
        address arbiter;
        address token;
        uint256 amount;
        uint64 deadline;
        OnTimeout onTimeout;
        State state;
    }

    event Opened(
        bytes32 indexed id,
        address indexed payer,
        address indexed payee,
        address arbiter,
        address token,
        uint256 amount,
        uint64 deadline,
        OnTimeout onTimeout
    );
    event Released(bytes32 indexed id, uint256 toPayee);
    event Refunded(bytes32 indexed id, uint256 toPayer);
    event TimedOut(bytes32 indexed id, OnTimeout onTimeout);
    event Disputed(bytes32 indexed id, address indexed by);
    event Resolved(bytes32 indexed id, uint256 toPayee, uint256 toPayer);
    event Credited(address indexed to, uint256 amount);
    event Claimed(address indexed to, uint256 amount);

    error ZeroPayee();
    error ZeroAmount();
    error BadDeadline();
    error WrongValue(uint256 expected, uint256 given);
    error FeeOnTransferToken();
    error Unknown(bytes32 id);
    error WrongState(bytes32 id, State state);
    error OnlyPayer(bytes32 id);
    error OnlyPayee(bytes32 id);
    error OnlyParty(bytes32 id);
    error OnlyArbiter(bytes32 id);
    error NoArbiter(bytes32 id);
    error BeforeDeadline(bytes32 id, uint64 deadline);
    error AfterDeadline(bytes32 id, uint64 deadline);
    error BadShare(uint256 bps);
    error NothingToClaim();
    error TransferFailed();

    function open(address payee, address arbiter, address token, uint256 amount, uint64 deadline, OnTimeout onTimeout)
        external
        payable
        returns (bytes32 id);
    function release(bytes32 id) external;
    function refund(bytes32 id) external;
    function timeout(bytes32 id) external;
    function dispute(bytes32 id) external;
    function resolve(bytes32 id, uint256 payeeShareBps) external;
    function claim() external;

    function escrow(bytes32 id) external view returns (Escrow memory);
    function claimable(address to) external view returns (uint256);
}
