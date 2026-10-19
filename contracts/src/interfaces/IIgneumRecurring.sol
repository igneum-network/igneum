// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

/// @title IIgneumRecurring
/// @notice Bounded recurring authorisations. The payer bounds what can leave: an amount per period, a fee per
///         collection to whoever submits it, an end, and a total cap that counts both. Anyone may collect a due period.
interface IIgneumRecurring {
    struct Authorisation {
        address payer;
        address payee;
        address token;
        uint128 amountPerPeriod;
        uint128 operatorFee;
        uint64 period;
        uint64 start;
        uint64 end;
        uint256 totalCap;
        uint256 spent;
        uint256 deposit;
        bool revoked;
    }

    event Authorised(
        bytes32 indexed id,
        address indexed payer,
        address indexed payee,
        address token,
        uint256 amountPerPeriod,
        uint256 operatorFee,
        uint64 period,
        uint64 start,
        uint64 end,
        uint256 totalCap
    );
    event Deposited(bytes32 indexed id, uint256 amount);
    event Withdrawn(bytes32 indexed id, uint256 amount);
    event Collected(bytes32 indexed id, uint256 indexed periodIndex, address indexed operator, uint256 amount, uint256 fee);
    event Revoked(bytes32 indexed id, uint256 refunded);
    event Credited(address indexed payee, uint256 amount);
    event Claimed(address indexed payee, uint256 amount);

    error ZeroPayee();
    error ZeroAmount();
    error AmountTooLarge(uint256 amount);
    error BadPeriod();
    error BadWindow();
    error CapBelowOnePeriod();
    error Unknown(bytes32 id);
    error OnlyPayer(bytes32 id);
    error NotNative(bytes32 id);
    error WrongValue(uint256 expected, uint256 given);
    error IsRevoked(bytes32 id);
    error NotStarted(bytes32 id, uint64 start);
    error Ended(bytes32 id, uint64 end);
    error AlreadyCollected(bytes32 id, uint256 periodIndex);
    error CapReached(bytes32 id, uint256 spent, uint256 cap);
    error InsufficientDeposit(bytes32 id, uint256 deposit, uint256 needed);
    error NothingToClaim();
    error TransferFailed();

    function authorise(
        address payee,
        address token,
        uint256 amountPerPeriod,
        uint256 operatorFee,
        uint64 period,
        uint64 start,
        uint64 end,
        uint256 totalCap
    ) external payable returns (bytes32 id);
    function deposit(bytes32 id) external payable;
    function withdraw(bytes32 id, uint256 amount) external;
    function revoke(bytes32 id) external;
    function collect(bytes32 id) external;
    function claim() external;

    function authorisation(bytes32 id) external view returns (Authorisation memory);
    function collected(bytes32 id, uint256 periodIndex) external view returns (bool);
    function currentPeriod(bytes32 id) external view returns (uint256);
    function claimable(address payee) external view returns (uint256);
}
