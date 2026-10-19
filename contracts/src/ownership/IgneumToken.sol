// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {ERC20} from "@openzeppelin/contracts/token/ERC20/ERC20.sol";
import {ERC20Permit} from "@openzeppelin/contracts/token/ERC20/extensions/ERC20Permit.sol";

/// @title IgneumToken
/// @notice A plain fungible asset: ERC-20 with ERC-2612 permit. The whole supply is minted once at construction to
///         the named holder. No owner, no minting afterwards, no pause. A template for issuers, not a protocol.
contract IgneumToken is ERC20, ERC20Permit {
    constructor(string memory name_, string memory symbol_, uint256 supply, address holder)
        ERC20(name_, symbol_)
        ERC20Permit(name_)
    {
        _mint(holder, supply);
    }
}
