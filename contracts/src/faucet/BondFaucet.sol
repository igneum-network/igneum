// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

/// @title BondFaucet
/// @notice Development-network tokens; no monetary value. A one-off drip sized for a bond (the project's word, 12 October
///         2026: three units, 86.3 IGN) so a stranger reaches the draw on the development chain from the wallet's
///         Authority Desk with one tap: `drip(to, keyHash)` by anyone (the relay, or the person), once per bond
///         key hash and once per address, within a daily cap for the whole. Anyone funds it. No owner, no pause, no
///         upgrade path; the parameters are fixed at deployment and a new faucet is a new contract.
/// @dev The key hash is the bond key's 32-byte hash as the registry names it (igneum_bondOf's argument); the
///      contract cannot check that `to` will bond that key, only that neither the key nor the address drips twice;
///      the daily cap is the hard bound against a crowd of keys.
contract BondFaucet {
    string public constant LABEL = "development-network tokens; no monetary value";
    uint256 public immutable dripAmount;
    uint256 public immutable dailyCap;
    mapping(bytes32 => bool) public keyDripped;
    mapping(address => bool) public addressDripped;
    uint256 public dayIndex;
    uint256 public drippedToday;

    event Dripped(address indexed to, bytes32 indexed keyHash, uint256 amount, address indexed by);
    event Funded(address indexed from, uint256 amount);

    error KeyDripped(bytes32 keyHash);
    error AddressDripped(address to);
    error DayCapReached(uint256 drippedToday, uint256 dailyCap);
    error Empty(uint256 balance, uint256 needed);
    error BadRecipient(address to);
    error BadKey(bytes32 keyHash);
    error TransferFailed(address to);

    constructor(uint256 dripAmount_, uint256 dailyCap_) {
        require(dripAmount_ > 0 && dailyCap_ >= dripAmount_, "the parameters");
        dripAmount = dripAmount_;
        dailyCap = dailyCap_;
    }

    receive() external payable {
        emit Funded(msg.sender, msg.value);
    }

    /// @notice One drip of `dripAmount` to `to` for the bond key `keyHash`: once per key, once per address, within
    ///         the day's cap.
    function drip(address to, bytes32 keyHash) external {
        if (to == address(0) || to == address(this)) revert BadRecipient(to);
        if (keyHash == bytes32(0)) revert BadKey(keyHash);
        if (keyDripped[keyHash]) revert KeyDripped(keyHash);
        if (addressDripped[to]) revert AddressDripped(to);
        uint256 today = block.timestamp / 1 days;
        if (today != dayIndex) {
            dayIndex = today;
            drippedToday = 0;
        }
        if (drippedToday + dripAmount > dailyCap) revert DayCapReached(drippedToday, dailyCap);
        if (address(this).balance < dripAmount) revert Empty(address(this).balance, dripAmount);
        keyDripped[keyHash] = true;
        addressDripped[to] = true;
        drippedToday += dripAmount;
        emit Dripped(to, keyHash, dripAmount, msg.sender);
        (bool ok,) = to.call{value: dripAmount}("");
        if (!ok) revert TransferFailed(to);
    }

    /// @notice What the day has left under the cap.
    function leftToday() external view returns (uint256) {
        if (block.timestamp / 1 days != dayIndex) return dailyCap;
        return dailyCap - drippedToday;
    }
}
