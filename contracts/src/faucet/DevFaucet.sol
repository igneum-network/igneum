// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

/// @title DevFaucet
/// @notice Development-network tokens; no monetary value. A drip of the dev chain's native IGN to any address:
///         `drip(to)` may be called by anyone (a relay that pays the gas for a stranger with nothing, or the person
///         themselves), bounded per recipient (`dripAmount` once per `cooldown`) and per day for the whole faucet
///         (`dailyCap`), so a crowd of addresses drains nothing beyond the day's cap. Anyone funds it. No owner, no
///         pause, no upgrade path; the parameters are fixed at deployment and a new faucet is a new contract.
/// @dev The per-source bound (one person, many addresses) is the relay's (by its own source rule); the chain sees
///      only addresses, so the daily cap is the hard bound here.
contract DevFaucet {
    string public constant LABEL = "development-network tokens; no monetary value";
    uint256 public immutable dripAmount;
    uint256 public immutable cooldown;
    uint256 public immutable dailyCap;
    mapping(address => uint256) public lastDrip;
    uint256 public dayIndex;
    uint256 public drippedToday;

    event Dripped(address indexed to, uint256 amount, address indexed by);
    event Funded(address indexed from, uint256 amount);

    error TooSoon(address to, uint256 nextAt);
    error DayCapReached(uint256 drippedToday, uint256 dailyCap);
    error Empty(uint256 balance, uint256 needed);
    error BadRecipient(address to);
    error TransferFailed(address to);

    constructor(uint256 dripAmount_, uint256 cooldown_, uint256 dailyCap_) {
        require(dripAmount_ > 0 && cooldown_ > 0 && dailyCap_ >= dripAmount_, "the parameters");
        dripAmount = dripAmount_;
        cooldown = cooldown_;
        dailyCap = dailyCap_;
    }

    receive() external payable {
        emit Funded(msg.sender, msg.value);
    }

    /// @notice One drip to `to`: `dripAmount` once per `cooldown` per recipient, within the day's cap.
    function drip(address to) external {
        if (to == address(0) || to == address(this)) revert BadRecipient(to);
        uint256 last = lastDrip[to];
        // an address that never dripped is never "too soon", whatever the chain's clock says
        if (last != 0 && block.timestamp < last + cooldown) revert TooSoon(to, last + cooldown);
        uint256 today = block.timestamp / 1 days;
        if (today != dayIndex) {
            dayIndex = today;
            drippedToday = 0;
        }
        if (drippedToday + dripAmount > dailyCap) revert DayCapReached(drippedToday, dailyCap);
        if (address(this).balance < dripAmount) revert Empty(address(this).balance, dripAmount);
        lastDrip[to] = block.timestamp;
        drippedToday += dripAmount;
        emit Dripped(to, dripAmount, msg.sender);
        (bool ok,) = to.call{value: dripAmount}("");
        if (!ok) revert TransferFailed(to);
    }

    /// @notice When `to` may drip next (0 when now).
    function nextDripAt(address to) external view returns (uint256) {
        uint256 last = lastDrip[to];
        if (last == 0 || block.timestamp >= last + cooldown) return 0;
        return last + cooldown;
    }

    /// @notice What the day has left under the cap.
    function leftToday() external view returns (uint256) {
        if (block.timestamp / 1 days != dayIndex) return dailyCap;
        return dailyCap - drippedToday;
    }
}
