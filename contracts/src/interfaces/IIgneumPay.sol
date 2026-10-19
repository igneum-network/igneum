// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {PayTypes} from "../pay/PayTypes.sol";

/// @title IIgneumPay
/// @notice Payment requests, invoices and receipts. One payment per invoice; the receipt is the chain's record.
interface IIgneumPay {
    enum Status {
        None,
        Open,
        Paid,
        Cancelled,
        Expired
    }

    struct Invoice {
        address payee;
        address token;
        uint256 amount;
        uint64 expiry;
        bytes32 memo;
        address payer;
        Status status;
    }

    struct Receipt {
        address payer;
        address token;
        uint256 amount;
        uint64 paidAt;
        uint64 blockNumber;
    }

    event InvoiceCreated(
        bytes32 indexed id,
        address indexed payee,
        address indexed token,
        uint256 amount,
        uint64 expiry,
        bytes32 memo,
        address payer
    );
    event InvoiceCancelled(bytes32 indexed id);
    event Paid(
        bytes32 indexed id, address indexed payee, address indexed payer, address token, uint256 amount, bytes32 memo
    );
    /// @notice A native payment the payee could not receive was credited for a later claim.
    event Credited(address indexed payee, uint256 amount);
    event Claimed(address indexed payee, uint256 amount);

    error ZeroAmount();
    error BadExpiry();
    error NotOpen(bytes32 id);
    error Expired(bytes32 id);
    error WrongPayer(bytes32 id, address sender);
    error WrongValue(uint256 expected, uint256 given);
    error BadSignature(bytes32 id);
    error NotPayee(bytes32 id);
    error NothingToClaim();
    error TransferFailed();

    function createInvoice(address token, uint256 amount, uint64 expiry, bytes32 memo, address payer)
        external
        returns (bytes32 id);
    function cancelInvoice(bytes32 id) external;
    function pay(bytes32 id) external payable;
    function payRequest(PayTypes.PaymentRequest calldata request, bytes calldata signature)
        external
        payable
        returns (bytes32 id);
    function claim() external;

    function requestId(PayTypes.PaymentRequest calldata request) external view returns (bytes32);
    function invoice(bytes32 id) external view returns (Invoice memory);
    function receipt(bytes32 id) external view returns (Receipt memory);
    function status(bytes32 id) external view returns (Status);
    function claimable(address payee) external view returns (uint256);
}
