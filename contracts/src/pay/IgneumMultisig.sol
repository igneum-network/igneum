// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {IERC1271} from "@openzeppelin/contracts/interfaces/IERC1271.sol";
import {ERC721Holder} from "@openzeppelin/contracts/token/ERC721/utils/ERC721Holder.sol";
import {ERC1155Holder} from "@openzeppelin/contracts/token/ERC1155/utils/ERC1155Holder.sol";
import {ReentrancyGuard} from "@openzeppelin/contracts/utils/ReentrancyGuard.sol";
import {ECDSA} from "@openzeppelin/contracts/utils/cryptography/ECDSA.sol";
import {EIP712} from "@openzeppelin/contracts/utils/cryptography/EIP712.sol";

import {IIgneumMultisig} from "../interfaces/IIgneumMultisig.sol";
import {PayTypes} from "./PayTypes.sol";

/// @title IgneumMultisig
/// @notice An M-of-N account that holds IGN, ERC-20s, ERC-721s and ERC-1155s and signs as a contract (ERC-1271).
/// @dev Approvals come two ways and both count towards the threshold for one transaction hash:
///      - on chain: an owner calls `approve(txHash)`;
///      - off chain: owners sign the EIP-712 `Transaction` and the signatures are passed to `execute`, sorted by
///        signer address ascending with no duplicates.
///      Owner changes are made by the multisig calling itself through `execute`. There is no other administrator.
///      Transactions execute in nonce order; a transaction that fails reverts the call and keeps the nonce.
contract IgneumMultisig is IIgneumMultisig, IERC1271, EIP712, ERC721Holder, ERC1155Holder, ReentrancyGuard {
    bytes4 private constant ERC1271_MAGIC = 0x1626ba7e;
    bytes4 private constant ERC1271_BAD = 0xffffffff;

    address[] private _owners;
    mapping(address owner => bool) public override isOwner;
    uint256 public override threshold;
    uint256 public override nonce;
    mapping(bytes32 txHash => mapping(address owner => bool)) public override approved;
    mapping(bytes32 hash => bool) public approvedMessages;

    modifier onlySelf() {
        if (msg.sender != address(this)) revert OnlySelf();
        _;
    }

    modifier onlyOwner() {
        if (!isOwner[msg.sender]) revert OnlyOwner(msg.sender);
        _;
    }

    constructor(address[] memory owners_, uint256 threshold_) EIP712("IgneumMultisig", "1") {
        for (uint256 i = 0; i < owners_.length; i++) {
            address o = owners_[i];
            // a bad owner list is a construction error; reverting in the loop is the point
            // forge-lint: disable-next-line(require-revert-in-loop)
            if (o == address(0) || isOwner[o]) revert BadOwner(o);
            isOwner[o] = true;
            _owners.push(o);
            emit OwnerAdded(o);
        }
        if (threshold_ == 0 || threshold_ > _owners.length) revert BadThreshold(threshold_, _owners.length);
        threshold = threshold_;
        emit ThresholdChanged(threshold_);
    }

    receive() external payable {
        emit Received(msg.sender, msg.value);
    }

    // ---------------------------------------------------------------- approvals and execution

    /// @inheritdoc IIgneumMultisig
    function approve(bytes32 txHash_) external override onlyOwner {
        approved[txHash_][msg.sender] = true;
        emit Approved(txHash_, msg.sender);
    }

    /// @inheritdoc IIgneumMultisig
    function revokeApproval(bytes32 txHash_) external override onlyOwner {
        approved[txHash_][msg.sender] = false;
        emit ApprovalRevoked(txHash_, msg.sender);
    }

    /// @inheritdoc IIgneumMultisig
    function execute(PayTypes.Transaction calldata transaction, bytes[] calldata signatures)
        external
        override
        nonReentrant
        returns (bytes memory result)
    {
        if (transaction.nonce != nonce) revert WrongNonce(transaction.nonce, nonce);
        if (transaction.deadline != 0 && block.timestamp > transaction.deadline) {
            revert PastDeadline(transaction.deadline);
        }
        bytes32 h = txHash(transaction);
        uint256 count = _countApprovals(h, signatures);
        if (count < threshold) revert BelowThreshold(count, threshold);
        uint256 executedNonce = nonce++;
        (bool ok, bytes memory res) = transaction.to.call{value: transaction.value}(transaction.data);
        if (!ok) revert ExecutionFailed(h, res);
        emit Executed(h, transaction.to, transaction.value, transaction.data, executedNonce);
        return res;
    }

    /// @inheritdoc IIgneumMultisig
    function txHash(PayTypes.Transaction calldata transaction) public view override returns (bytes32) {
        return _hashTypedDataV4(PayTypes.hash(transaction));
    }

    // ---------------------------------------------------------------- self-administration

    /// @inheritdoc IIgneumMultisig
    function addOwner(address owner) external override onlySelf {
        if (owner == address(0) || isOwner[owner]) revert BadOwner(owner);
        isOwner[owner] = true;
        _owners.push(owner);
        emit OwnerAdded(owner);
    }

    /// @inheritdoc IIgneumMultisig
    function removeOwner(address owner) external override onlySelf {
        if (!isOwner[owner]) revert BadOwner(owner);
        uint256 remaining = _owners.length - 1;
        if (remaining < threshold) revert BadThreshold(threshold, remaining);
        isOwner[owner] = false;
        for (uint256 i = 0; i < _owners.length; i++) {
            if (_owners[i] == owner) {
                _owners[i] = _owners[_owners.length - 1];
                _owners.pop();
                break;
            }
        }
        emit OwnerRemoved(owner);
    }

    /// @inheritdoc IIgneumMultisig
    function changeThreshold(uint256 newThreshold) external override onlySelf {
        if (newThreshold == 0 || newThreshold > _owners.length) revert BadThreshold(newThreshold, _owners.length);
        threshold = newThreshold;
        emit ThresholdChanged(newThreshold);
    }

    /// @inheritdoc IIgneumMultisig
    function approveMessage(bytes32 hash) external override onlySelf {
        approvedMessages[hash] = true;
        emit MessageApproved(hash);
    }

    // ---------------------------------------------------------------- ERC-1271

    /// @notice Valid when the hash was approved through `approveMessage`, or when `signature` is a concatenation of
    ///         65-byte owner signatures over `hash`, sorted by signer ascending, meeting the threshold.
    function isValidSignature(bytes32 hash, bytes calldata signature) external view override returns (bytes4) {
        if (signature.length == 0) return approvedMessages[hash] ? ERC1271_MAGIC : ERC1271_BAD;
        if (signature.length % 65 != 0) return ERC1271_BAD;
        uint256 n = signature.length / 65;
        address last = address(0);
        for (uint256 i = 0; i < n; i++) {
            (address signer, ECDSA.RecoverError err,) = ECDSA.tryRecover(hash, signature[i * 65:(i + 1) * 65]);
            if (err != ECDSA.RecoverError.NoError || signer <= last || !isOwner[signer]) return ERC1271_BAD;
            last = signer;
        }
        return n >= threshold ? ERC1271_MAGIC : ERC1271_BAD;
    }

    // ---------------------------------------------------------------- views

    /// @inheritdoc IIgneumMultisig
    function owners() external view override returns (address[] memory) {
        return _owners;
    }

    function supportsInterface(bytes4 interfaceId) public view override returns (bool) {
        return interfaceId == type(IERC1271).interfaceId || super.supportsInterface(interfaceId);
    }

    // ---------------------------------------------------------------- internal

    /// @dev Signatures must be from distinct owners in strictly ascending address order. An owner's on-chain
    ///      approval counts only if that owner did not also sign, so nobody is counted twice.
    function _countApprovals(bytes32 h, bytes[] calldata signatures) private view returns (uint256 count) {
        address[] memory signers = new address[](signatures.length);
        address last = address(0);
        for (uint256 i = 0; i < signatures.length; i++) {
            address signer = ECDSA.recover(h, signatures[i]);
            // a bad signature set fails the whole execution; reverting in the loop is the point
            // forge-lint: disable-next-line(require-revert-in-loop)
            if (signer <= last) revert UnsortedOrDuplicateSigner(signer);
            // forge-lint: disable-next-line(require-revert-in-loop)
            if (!isOwner[signer]) revert NotAnOwner(signer);
            signers[i] = signer;
            last = signer;
        }
        count = signatures.length;
        uint256 n = _owners.length;
        for (uint256 i = 0; i < n; i++) {
            address o = _owners[i];
            if (!approved[h][o]) continue;
            bool signed = false;
            for (uint256 j = 0; j < signers.length; j++) {
                if (signers[j] == o) {
                    signed = true;
                    break;
                }
            }
            if (!signed) count++;
        }
    }
}
