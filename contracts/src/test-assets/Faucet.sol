// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

/// @title Faucet
/// @notice A testnet drip of native coin: one `drip` per address per `cooldown`. Anyone funds it. No owner.
contract Faucet {
    uint256 public immutable dripAmount;
    uint256 public immutable cooldown;
    mapping(address => uint256) public lastDrip;

    event Dripped(address indexed to, uint256 amount);
    event Funded(address indexed from, uint256 amount);

    error TooSoon(uint256 nextAt);
    error Empty(uint256 balance, uint256 needed);
    error TransferFailed();

    constructor(uint256 dripAmount_, uint256 cooldown_) {
        dripAmount = dripAmount_;
        cooldown = cooldown_;
    }

    receive() external payable {
        emit Funded(msg.sender, msg.value);
    }

    function drip() external {
        uint256 last = lastDrip[msg.sender];
        // an address that never dripped is never "too soon", whatever the chain's clock says
        if (last != 0 && block.timestamp < last + cooldown) revert TooSoon(last + cooldown);
        if (address(this).balance < dripAmount) revert Empty(address(this).balance, dripAmount);
        lastDrip[msg.sender] = block.timestamp;
        (bool ok,) = msg.sender.call{value: dripAmount}("");
        if (!ok) revert TransferFailed();
        emit Dripped(msg.sender, dripAmount);
    }
}
