// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {IERC20} from "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import {SafeERC20} from "@openzeppelin/contracts/token/ERC20/utils/SafeERC20.sol";
import {ReentrancyGuard} from "@openzeppelin/contracts/utils/ReentrancyGuard.sol";
import {EIP712} from "@openzeppelin/contracts/utils/cryptography/EIP712.sol";
import {SignatureChecker} from "@openzeppelin/contracts/utils/cryptography/SignatureChecker.sol";

import {IIgneumPay} from "../interfaces/IIgneumPay.sol";
import {PayTypes} from "./PayTypes.sol";

/// @title IgneumPay
/// @notice Payment requests, invoices and receipts for native IGN and ERC-20 tokens.
/// @dev No owner, no upgrade, no pause, no protocol fee. Anyone may deploy another copy.
///      - An invoice is created on chain by the payee and paid once by anyone (or by the named payer).
///      - A payment request is signed off chain by the payee (EOA or ERC-1271 contract) and paid once through
///        `payRequest`; its id is the EIP-712 digest, so the same request cannot be paid twice.
///      - The receipt is the `Paid` event and the `Receipt` record under the id. Both carry the payer's address, the
///        token, the amount and the block.
///      - Funds go straight to the payee. A native payment a payee contract cannot receive is credited under
///        `claimable` for a later `claim`, and the push forwards a bounded gas (PayTypes.PUSH_GAS), so a payee can
///        never block a payer by reverting or by burning the gas it is given.
///      - Fee-on-transfer and rebasing ERC-20s are not supported: the payee would receive less than the receipt says.
contract IgneumPay is IIgneumPay, EIP712, ReentrancyGuard {
    using SafeERC20 for IERC20;

    mapping(bytes32 id => Invoice) private _invoices;
    mapping(bytes32 id => Receipt) private _receipts;
    mapping(address payee => uint256) public invoiceCount;
    mapping(address payee => uint256) public override claimable;

    constructor() EIP712("IgneumPay", "1") {}

    // ---------------------------------------------------------------- invoices

    /// @inheritdoc IIgneumPay
    function createInvoice(address token, uint256 amount, uint64 expiry, bytes32 memo, address payer)
        external
        override
        returns (bytes32 id)
    {
        if (amount == 0) revert ZeroAmount();
        if (expiry != 0 && expiry <= block.timestamp) revert BadExpiry();
        uint256 n = invoiceCount[msg.sender]++;
        id = keccak256(abi.encode(block.chainid, address(this), msg.sender, n));
        _invoices[id] = Invoice({
            payee: msg.sender,
            token: token,
            amount: amount,
            expiry: expiry,
            memo: memo,
            payer: payer,
            status: Status.Open
        });
        emit InvoiceCreated(id, msg.sender, token, amount, expiry, memo, payer);
    }

    /// @inheritdoc IIgneumPay
    function cancelInvoice(bytes32 id) external override {
        Invoice storage inv = _invoices[id];
        if (inv.payee != msg.sender) revert NotPayee(id);
        if (inv.status != Status.Open) revert NotOpen(id);
        inv.status = Status.Cancelled;
        emit InvoiceCancelled(id);
    }

    /// @inheritdoc IIgneumPay
    function pay(bytes32 id) external payable override nonReentrant {
        Invoice storage inv = _invoices[id];
        if (inv.status != Status.Open) revert NotOpen(id);
        if (inv.expiry != 0 && block.timestamp > inv.expiry) revert Expired(id);
        if (inv.payer != address(0) && inv.payer != msg.sender) revert WrongPayer(id, msg.sender);
        inv.status = Status.Paid;
        _settle(id, inv.payee, inv.token, inv.amount, inv.memo);
    }

    // ---------------------------------------------------------------- signed requests

    /// @inheritdoc IIgneumPay
    function payRequest(PayTypes.PaymentRequest calldata request, bytes calldata signature)
        external
        payable
        override
        nonReentrant
        returns (bytes32 id)
    {
        id = _hashTypedDataV4(PayTypes.hash(request));
        if (_invoices[id].status != Status.None) revert NotOpen(id);
        if (request.amount == 0) revert ZeroAmount();
        if (request.expiry != 0 && block.timestamp > request.expiry) revert Expired(id);
        if (request.payer != address(0) && request.payer != msg.sender) revert WrongPayer(id, msg.sender);
        if (!SignatureChecker.isValidSignatureNow(request.payee, id, signature)) revert BadSignature(id);
        _invoices[id] = Invoice({
            payee: request.payee,
            token: request.token,
            amount: request.amount,
            expiry: request.expiry,
            memo: request.memo,
            payer: request.payer,
            status: Status.Paid
        });
        _settle(id, request.payee, request.token, request.amount, request.memo);
    }

    /// @inheritdoc IIgneumPay
    function requestId(PayTypes.PaymentRequest calldata request) external view override returns (bytes32) {
        return _hashTypedDataV4(PayTypes.hash(request));
    }

    // ---------------------------------------------------------------- claims

    /// @inheritdoc IIgneumPay
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

    // ---------------------------------------------------------------- views

    /// @inheritdoc IIgneumPay
    function invoice(bytes32 id) external view override returns (Invoice memory) {
        return _invoices[id];
    }

    /// @inheritdoc IIgneumPay
    function receipt(bytes32 id) external view override returns (Receipt memory) {
        return _receipts[id];
    }

    /// @inheritdoc IIgneumPay
    function status(bytes32 id) external view override returns (Status) {
        Invoice storage inv = _invoices[id];
        if (inv.status == Status.Open && inv.expiry != 0 && block.timestamp > inv.expiry) return Status.Expired;
        return inv.status;
    }

    /// @notice The EIP-712 domain separator, for wallets that build the typed data themselves.
    function domainSeparator() external view returns (bytes32) {
        return _domainSeparatorV4();
    }

    // ---------------------------------------------------------------- internal

    function _settle(bytes32 id, address payee, address token, uint256 amount, bytes32 memo) private {
        if (token == address(0)) {
            if (msg.value != amount) revert WrongValue(amount, msg.value);
            _payNative(payee, amount);
        } else {
            if (msg.value != 0) revert WrongValue(0, msg.value);
            IERC20(token).safeTransferFrom(msg.sender, payee, amount);
        }
        _receipts[id] = Receipt({
            payer: msg.sender,
            token: token,
            amount: amount,
            // a uint64 holds timestamps and block numbers for longer than the chain will exist
            // forge-lint: disable-next-line(unsafe-typecast)
            paidAt: uint64(block.timestamp),
            // forge-lint: disable-next-line(unsafe-typecast)
            blockNumber: uint64(block.number)
        });
        emit Paid(id, payee, msg.sender, token, amount, memo);
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
}
