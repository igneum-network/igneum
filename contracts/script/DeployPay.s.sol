// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {Script, console} from "forge-std/Script.sol";

import {IgneumPay} from "../src/pay/IgneumPay.sol";
import {IgneumRecurring} from "../src/pay/IgneumRecurring.sol";
import {IgneumEscrow} from "../src/pay/IgneumEscrow.sol";
import {TestToken} from "../src/test-assets/TestToken.sol";
import {TestCollectible} from "../src/test-assets/TestCollectible.sol";
import {Faucet} from "../src/test-assets/Faucet.sol";

/// The Pay set and the test assets, as published for a devnet or testnet (DESIGN.md 3.3 item 7). The multisig is
/// not deployed here: every multisig is its owners' own deployment. Addresses go into DESIGN.md and dev/chain/
/// with the chain id, the commit and the compiler settings; a re-deployment is a new entry, never an overwrite.
///   DEPLOYER_KEY=<hex> forge script script/DeployPay.s.sol --rpc-url $RPC --broadcast
/// The key comes from the environment, never from the command line (which every process list shows); without
/// DEPLOYER_KEY the script broadcasts with whatever forge was given (--private-key, a keystore, a hardware wallet).
contract DeployPay is Script {
    function run() external {
        uint256 key = vm.envOr("DEPLOYER_KEY", uint256(0));
        if (key != 0) vm.startBroadcast(key);
        else vm.startBroadcast();
        IgneumPay pay = new IgneumPay();
        IgneumRecurring recurring = new IgneumRecurring();
        IgneumEscrow escrow = new IgneumEscrow();
        TestToken token = new TestToken();
        TestCollectible collectible = new TestCollectible();
        Faucet faucet = new Faucet(1 ether, 1 days);
        vm.stopBroadcast();
        console.log("chain", block.chainid);
        console.log("IgneumPay", address(pay));
        console.log("IgneumRecurring", address(recurring));
        console.log("IgneumEscrow", address(escrow));
        console.log("TestToken", address(token));
        console.log("TestCollectible", address(collectible));
        console.log("Faucet", address(faucet));
    }
}
