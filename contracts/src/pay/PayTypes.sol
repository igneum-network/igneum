// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

/// @title PayTypes
/// @notice The EIP-712 structs and struct hashes shared by the Igneum Pay contracts and by wallets that render them.
/// @dev The type strings are the contract between the wallet's typed-data renderer and the chain: a change here is a
///      new domain version in every contract that uses it.
library PayTypes {
    /// @notice The gas a native push forwards to the payee (evm-pay's F-12, 12 October 2026): all-but-a-64th let a
    ///         gas-burning payee leave the credit path too little to run, so the whole pay reverted below about
    ///         5.2 M gas and the payer paid for the revert; under this bound a plain receive fits, a heavier one
    ///         fails and is credited, and the credit path always has what it needs.
    uint256 internal constant PUSH_GAS = 100_000;

    /// @notice A payment request signed off chain by the payee (an EOA or an ERC-1271 contract such as IgneumMultisig)
    ///         and settled on chain by the payer through IgneumPay.payRequest.
    /// @param payee who is paid
    /// @param token the ERC-20 to pay in, or address(0) for native IGN
    /// @param amount the exact amount, in the token's smallest unit
    /// @param expiry the last second at which it can be paid, or 0 for no expiry
    /// @param memo the payee's own memo (an order id, a hash of an invoice document); opaque to the contract
    /// @param payer who may pay it, or address(0) for anyone
    /// @param nonce the payee's own uniqueness value; two requests from one payee with the same fields and nonce are one
    struct PaymentRequest {
        address payee;
        address token;
        uint256 amount;
        uint64 expiry;
        bytes32 memo;
        address payer;
        uint256 nonce;
    }

    bytes32 internal constant PAYMENT_REQUEST_TYPEHASH = keccak256(
        "PaymentRequest(address payee,address token,uint256 amount,uint64 expiry,bytes32 memo,address payer,uint256 nonce)"
    );

    /// @notice A transaction proposed to an IgneumMultisig.
    /// @param to the target
    /// @param value native IGN to send with the call
    /// @param data the calldata
    /// @param nonce the multisig's current nonce (one transaction per nonce; executed in order)
    /// @param deadline the last second at which it may execute, or 0 for none
    struct Transaction {
        address to;
        uint256 value;
        bytes data;
        uint256 nonce;
        uint256 deadline;
    }

    bytes32 internal constant TRANSACTION_TYPEHASH =
        keccak256("Transaction(address to,uint256 value,bytes data,uint256 nonce,uint256 deadline)");

    function hash(PaymentRequest memory r) internal pure returns (bytes32) {
        return keccak256(
            abi.encode(PAYMENT_REQUEST_TYPEHASH, r.payee, r.token, r.amount, r.expiry, r.memo, r.payer, r.nonce)
        );
    }

    function hash(Transaction memory t) internal pure returns (bytes32) {
        return keccak256(abi.encode(TRANSACTION_TYPEHASH, t.to, t.value, keccak256(t.data), t.nonce, t.deadline));
    }
}
