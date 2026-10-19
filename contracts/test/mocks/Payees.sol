// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

/// @dev A payee contract that can refuse native coin, to exercise the credit-and-claim path.
contract TogglePayee {
    bool public accept = true;

    function setAccept(bool a) external {
        accept = a;
    }

    receive() external payable {
        if (!accept) revert("refused");
    }

    function call(address target, bytes calldata data) external returns (bytes memory) {
        (bool ok, bytes memory res) = target.call(data);
        require(ok, "call failed");
        return res;
    }
}
