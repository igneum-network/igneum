// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {PayTypes} from "../pay/PayTypes.sol";

/// @title IIgneumMultisig
/// @notice An M-of-N account. Owners approve on chain or sign EIP-712 transactions off chain; both count.
interface IIgneumMultisig {
    event Received(address indexed from, uint256 amount);
    event Approved(bytes32 indexed txHash, address indexed owner);
    event ApprovalRevoked(bytes32 indexed txHash, address indexed owner);
    event Executed(bytes32 indexed txHash, address indexed to, uint256 value, bytes data, uint256 nonce);
    event OwnerAdded(address indexed owner);
    event OwnerRemoved(address indexed owner);
    event ThresholdChanged(uint256 threshold);
    event MessageApproved(bytes32 indexed hash);

    error OnlySelf();
    error OnlyOwner(address sender);
    error BadOwner(address owner);
    error BadThreshold(uint256 threshold, uint256 owners);
    error WrongNonce(uint256 given, uint256 expected);
    error PastDeadline(uint256 deadline);
    error UnsortedOrDuplicateSigner(address signer);
    error NotAnOwner(address signer);
    error BelowThreshold(uint256 approvals, uint256 threshold);
    error ExecutionFailed(bytes32 txHash, bytes result);

    function approve(bytes32 hash) external;
    function revokeApproval(bytes32 hash) external;
    function execute(PayTypes.Transaction calldata transaction, bytes[] calldata signatures)
        external
        returns (bytes memory result);

    function addOwner(address owner) external;
    function removeOwner(address owner) external;
    function changeThreshold(uint256 newThreshold) external;
    function approveMessage(bytes32 hash) external;

    function txHash(PayTypes.Transaction calldata transaction) external view returns (bytes32);
    function owners() external view returns (address[] memory);
    function isOwner(address account) external view returns (bool);
    function threshold() external view returns (uint256);
    function nonce() external view returns (uint256);
    function approved(bytes32 hash, address owner) external view returns (bool);
}
