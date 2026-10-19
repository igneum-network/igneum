// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {Script, console} from "forge-std/Script.sol";

import {IgneumFund} from "../src/fund/IgneumFund.sol";

/// The fund's contract for a devnet (FUND-MANDATE-2026-10-11.md section 5). The seven signers and their flags come
/// from the environment as the published set (FUND_SIGNERS: seven addresses, comma-separated; FUND_FLAGS: seven
/// 0/1 company-connected flags, comma-separated); on a devnet they are placeholders the founder replaces by his
/// names, and the fund spends nothing until the set is published. The deployer's key reaches forge through
/// DEPLOYER_KEY in the environment, never argv.
///   DEPLOYER_KEY=<hex> FUND_SIGNERS=a,b,c[,d..g] FUND_FLAGS=1,0,0[,..] (three to seven; GOVERNANCE 8's table at that size) forge script script/DeployFund.s.sol --rpc-url $RPC --broadcast
contract DeployFund is Script {
    function run() external {
        address[] memory signers = vm.envAddress("FUND_SIGNERS", ",");
        uint256[] memory flagWords = vm.envUint("FUND_FLAGS", ",");
        require(signers.length >= 3 && signers.length <= 7 && flagWords.length == signers.length, "three to seven signers and as many flags");
        bool[] memory flags = new bool[](signers.length);
        for (uint256 i = 0; i < signers.length; i++) {
            flags[i] = flagWords[i] == 1;
        }
        uint256 key = vm.envOr("DEPLOYER_KEY", uint256(0));
        if (key != 0) vm.startBroadcast(key);
        else vm.startBroadcast();
        IgneumFund fund = new IgneumFund(signers, flags);
        vm.stopBroadcast();
        console.log("chain", block.chainid);
        console.log("IgneumFund", address(fund));
    }
}
