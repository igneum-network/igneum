// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {ERC20} from "@openzeppelin/contracts/token/ERC20/ERC20.sol";
import {ERC20Permit} from "@openzeppelin/contracts/token/ERC20/extensions/ERC20Permit.sol";

/// @title TestToken
/// @notice A test ERC-20 for devnet and testnet: anyone mints up to `MAX_MINT` per call. Worthless by design.
contract TestToken is ERC20, ERC20Permit {
    uint256 public constant MAX_MINT = 1_000 ether;

    error TooMuch(uint256 asked, uint256 max);

    constructor() ERC20("Igneum Test Token", "TIGN") ERC20Permit("Igneum Test Token") {}

    function mint(address to, uint256 amount) external {
        if (amount > MAX_MINT) revert TooMuch(amount, MAX_MINT);
        _mint(to, amount);
    }
}
