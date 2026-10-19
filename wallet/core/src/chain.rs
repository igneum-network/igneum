//! Network parameters. Everything here is protocol engineer A's proposal until P-I01 is ratified (D-B13); the
//! `proposed` flag reaches the window's eyebrow line.
//!
//! Every Igneum development chain carries chain id 7310 (builder one's devnet, devnet2, devnet3, devnet4 and devnet5
//! alike), so the chain id alone does not say which chain a node serves and a transaction signed for one of them is
//! valid on every other: the wallet therefore pins a chain by its genesis hash (D-B14) and names it by that pin in
//! every sentence a person reads. A chain id with no genesis pin is "chain 7310" and nothing more.

use serde::{Deserialize, Serialize};

use crate::{hex, Address, Error, Result};

/// A bond registry (the protocol team's genesis system contract) and the rule set the chain runs.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct Authority {
    /// the registry's address
    pub registry: Address,
    /// the registry's form: 2 is the devnet4 prototype (bond and signer only); 3 is the frozen rule set's form with
    /// the exit address, the release and the hostile claim (D-B15, a proposal until the protocol team lands it)
    pub registry_version: u32,
    /// the sortition stage is live (the draw decides who signs)
    pub sortition: bool,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct Chain {
    pub name: String,
    pub chain_id: u64,
    pub symbol: String,
    pub decimals: u32,
    pub rpc: Vec<String>,
    /// the public submit-only route (eth_sendRawTransaction alone), when the chain's public read endpoints refuse
    /// writes (devnet5: both do, through reliability's filter, 11 October 2026); the engine posts sends there when
    /// it reads from a public endpoint, and to the read endpoint itself otherwise. Empty until the route exists.
    #[serde(default)]
    pub submit: Option<String>,
    pub explorer: Option<String>,
    /// The kit's Faucet contract on a dev chain (1 IGN per address per day by `drip()`), for the page's one-tap
    /// "Get test IGN"; none on a chain with value.
    pub faucet: Option<String>,
    /// The faucet relay of a dev chain (the site's and the wallet's one tap; POST {address} for the drip, POST
    /// /bond {address, key_hash} for a bond's worth) and the BondFaucet contract; none on a chain with value.
    pub faucet_relay: Option<String>,
    pub bond_faucet: Option<String>,
    /// seconds between blocks, the target
    pub block_target_secs: u64,
    pub proposed: bool,
    /// true when `genesis` is this build's pin for the chain; false on the unpinned entry and on a re-sealed devnet5
    /// (the genesis seen on the node is kept in `genesis` and the page says it is not this build's pin)
    #[serde(default)]
    pub genesis_pinned: bool,
    /// the genesis block's hash, the pin that tells one chain id 7310 chain from another (None: unpinned)
    #[serde(default)]
    pub genesis: Option<String>,
    /// the bond registry and the rules, when the chain carries work authority
    #[serde(default)]
    pub authority: Option<Authority>,
}

/// Builder one's devnet genesis (the final frozen dev genesis, main d31b966, re-frozen for F-11).
pub const GENESIS_BUILD1_DEVNET: &str = "0xdd91c21e498df949b07ca4a8818fae9ff1fa1ef83962625ab33ca76aefca50ec";
/// devnet4's genesis (the protocol team's sortition sealer pair, builder two and builder three; read from the chain 11 October 2026).
pub const GENESIS_DEVNET4: &str = "0x61b947c7da97135a38e0c226078b5004df9262421de400484a0d212cd9c7181c";
/// devnet5's genesis: the protocol team's run 9 from genesis (11 October 2026, the founder's "deploy everything" order;
/// the project's word of 13:2x by its clock), read from sealer A's block 0 (head 139 at 12:2x UK). The earlier runs'
/// genesis hashes (run 8 0xe7adfe8e…5a71, the first run 0xd2e5fd21…f075) are gone with those chains; a 7315 chain
/// under another genesis reads as devnet5 re-sealed (`identify`), never as this pin.
pub const GENESIS_DEVNET5: &str = "0xcb0784a9b97803f8ff38cba00356943a0dd27caf675bfeb051611682414da358";
/// The public read-only endpoints of devnet5 (reliability, infra/feed/Caddyfile): JSON-RPC over https and the
/// WebSocket; both answer 503 while the hold file is in place and open on the project's word. A public build's default.
pub const PUBLIC_DEVNET5_RPC: &str = "https://build.igneum.network/devnet5/rpc";
pub const PUBLIC_DEVNET5_WS: &str = "wss://build.igneum.network/devnet5/ws";
/// The public submit-only route (the project's word, 11 October 2026, 23:1x by its clock; reliability builds it): POST,
/// eth_sendRawTransaction alone forwarded to node A, every other method refused, a tight per-source limit. The
/// engine posts a send here when it reads from the public endpoint; while the route is absent its refusal is shown
/// in the endpoint's words.
pub const PUBLIC_DEVNET5_SUBMIT: &str = "https://build.igneum.network/devnet5/submit";
/// devnet7, the LAUNCH-EQUIVALENT chain (the protocol team's record `tests/igneumhash/protocol-a/out/devnet7-genesis.json`):
/// chain id 7317, network id 7371, the genesis read from the public endpoint at 19:0x UK on 11 October 2026 and equal
/// to the record's; the public read endpoint through reliability's filter (which serves the bond read, igneum_bondOf
/// by the key's hash, since 19:47 UK); the submit route below. The fresh wallet's default
/// chain from this build (the founder's order: the mini runs the wallet on devnet7).
pub const GENESIS_DEVNET7: &str = "0xb47f1cc6f3bd46ac0c414a0b02a712c9d2ec421d8c2b14b045a7df969c9dc51b";
pub const PUBLIC_DEVNET7_RPC: &str = "https://build.igneum.network/devnet7/rpc";
/// devnet7's public submit-only route (reliability, 11 October 2026, 19:47 by its clock: eth_sendRawTransaction alone
/// forwarded to sealer A7, two a second, a burst of five per source, the body 128 KB; every other method and every
/// batch refused).
pub const PUBLIC_DEVNET7_SUBMIT: &str = "https://build.igneum.network/devnet7/submit";
/// devnet9, the LAUNCH-CLOCK chain (the founder's "public ready" order of 11 October 2026, 23:45 UK, through main):
/// chain id 7319, network id 7391, the genesis read from A9 and pod a at block 0 on 12 October 2026 (sealing from 01:35 UK); the
/// public endpoints in devnet7's form. The fresh wallet's default chain from the cut that carries this pin; devnet7
/// and devnet5 stay named pins, devnet8 a named chain without a pin (the fallback if devnet9 slips).
pub const GENESIS_DEVNET9: &str = "0xeae5e6083195fe58658b1f13771362b19a38a0bffb5c8808d458993ee9f54ea1";
pub const PUBLIC_DEVNET9_RPC: &str = "https://rpc.igneum.network";
pub const PUBLIC_DEVNET9_SUBMIT: &str = "https://rpc.igneum.network";
/// The explorer for devnet9 (the testnet SRE seat, 12 October 2026, 01:42 UK: Blockscout, chain 7319, from outside;
/// /block/<n>, /tx/<hash>, /address/<0x>).
pub const EXPLORER_DEVNET9: &str = "https://explorer.igneum.network";
/// Chain 7320, the old public test network (the public test network from 13 to 16 October 2026; chain id 7320, network id 7392): the genesis served by A10
/// at 21:06:55Z on 12 October (the network team's line), the same allocation as devnet9; the public host and the explorer are
/// the same names, re-pointed by reliability; the faucets of 14 October 2026 (dev/chain/7320-faucets.json).
/// The wallet's default chain from 0.0.8 (the project's word, 14 October 2026 04:3x UK).
pub const GENESIS_7320: &str = "0xd45af8c64c56cf102aee3deb763d327394eb49fbe5b2f55b1bbd461e457f1468";
pub const PUBLIC_7320_RPC: &str = PUBLIC_DEVNET9_RPC;
pub const EXPLORER_7320: &str = EXPLORER_DEVNET9;
/// Chain 7322, the public test network on the vector form from genesis (chain id 7322, network id 7394; the project's path (a),
/// 15 October 2026): the genesis re-served by the network team on the re-cut set (docs/chain/7322/), read back from the
/// archive reader at block 0 on 15 October 2026 18:45 UK (dev/chain/igneum-chains.json, the 7322 entry); 7322's own
/// public host from step one of the public name's swap, the same allocation as chain 7320. The faucets land by this
/// seat's deployment (dev/chain/7322-faucets.json).
pub const GENESIS_7322: &str = "0x45828a34b05153fd2a9d0b7c49e84ec2289c38dd2f8c755729321e4f9c2236ea";
pub const PUBLIC_7322_RPC: &str = "https://rpc.igneum.network"; // the public name (chain 7322 since 04:24:45 UK, 16 October 2026; the review lead's ruling of 18:07 UK: a retired host is not a default)
pub const EXPLORER_7322: &str = EXPLORER_DEVNET9;
pub const PUBLIC_DEVNET8_RPC: &str = "https://build.igneum.network/devnet8/rpc";
pub const PUBLIC_DEVNET8_SUBMIT: &str = "https://build.igneum.network/devnet8/submit";
/// The kit's Faucet contract on builder one's dev chain (chain id 7310, DESIGN.md 6b, the deployer's nonce 7).
pub const FAUCET_DEV: &str = "0xB1106e6488A81B3a4816a87f1FDd6De9CB369f2F";
/// The registry address of the authority prototype (`--dev.authority`), devnet3, devnet4 and devnet5.
pub const REGISTRY_DEV: &str = "0x0000000000000000000000000000000000000b0d";

impl Chain {
    /// The devnet as the protocol team proposes it (docs/protocol-a/DESIGN.md): chain id 7310, IGN, 18 decimals. Builder
    /// one's devnet on the stand-in algorithm, the Pay set deployed (DESIGN.md 6b).
    pub fn igneum_devnet() -> Self {
        Chain {
            name: "Igneum development chain 7310".into(),
            chain_id: 7310,
            symbol: "IGN".into(),
            decimals: 18,
            rpc: vec!["http://127.0.0.1:8545".into()],
            submit: None,
            explorer: None,
            faucet: Some(FAUCET_DEV.into()),
            faucet_relay: None,
            bond_faucet: None,
            // the kit's Faucet on builder one's dev chain (DESIGN.md 6b: nonce 7, block 548; 1 IGN per address per day)
            block_target_secs: 15,
            proposed: true,
            genesis_pinned: true,
            genesis: Some(GENESIS_BUILD1_DEVNET.into()),
            authority: None,
        }
    }

    /// devnet4: the bonded rule from 40 and the sortition rule from 80 (authority/PROTOTYPE.md section 2); the
    /// registry in its version 2 form (bond, read, set the signer). Its RPCs are the sealers' mesh endpoints
    /// (private), which an install cannot reach: the entry carries none, and a node is named by NODE_RPC.
    pub fn igneum_devnet4() -> Self {
        Chain {
            name: "Igneum development chain 7310, fourth cut".into(),
            chain_id: 7310,
            symbol: "IGN".into(),
            decimals: 18,
            rpc: vec![],
            submit: None,
            explorer: None,
            faucet: None,
            faucet_relay: None,
            bond_faucet: None,
            block_target_secs: 15,
            proposed: true,
            genesis_pinned: true,
            genesis: Some(GENESIS_DEVNET4.into()),
            authority: Some(Authority {
                registry: Address::parse(REGISTRY_DEV).expect("a fixed address"),
                registry_version: 2,
                sortition: true,
            }),
        }
    }

    /// devnet5: the frozen rule set (LAUNCH-GUARDS.md section 2) live from genesis, chain id 7315 (the project's ruling of
    /// 10:30 UK, 11 October 2026: the first dev chain with its own id; network id 7351), stood up by the protocol team at
    /// 10:5x UK with the registry in its version 3 NATIVE form (core/vm/igneumreg at the engine block's registry
    /// address; KEYED-FORM.md section 4): the read returns 128 bytes (amount, account, signer, exit), bond-with-exit
    /// is 64 bytes with value, the claim is the secret-reveal STAND-IN (sk, to, zero) until the risc0 verifier is in
    /// the node. The genesis is the one read from sealer A on the first run; the protocol team says it changes with any
    /// genesis flag, so a re-sealed devnet5 reads as "chain 7315" until this pin is updated. The sealers' mesh
    /// endpoints (private) take writes but no install can reach them, so only the public endpoint is listed.
    pub fn igneum_devnet5() -> Self {
        Chain {
            name: "Igneum development chain 7315".into(),
            chain_id: 7315,
            symbol: "IGN".into(),
            decimals: 18,
            // the public read-only endpoint (a public build); the sealers' mesh endpoints are private and not listed
            rpc: vec![PUBLIC_DEVNET5_RPC.into()],
            submit: Some(PUBLIC_DEVNET5_SUBMIT.into()),
            explorer: None,
            faucet: None,
            faucet_relay: None,
            bond_faucet: None,
            block_target_secs: 15,
            proposed: true,
            genesis_pinned: true,
            genesis: Some(GENESIS_DEVNET5.into()),
            authority: Some(Authority {
                registry: Address::parse(REGISTRY_DEV).expect("a fixed address"),
                registry_version: 3,
                sortition: true,
            }),
        }
    }

    /// A local anvil instance, the stand-in dev node until the protocol team's node builds (chain id 31337).
    pub fn anvil() -> Self {
        Chain {
            name: "anvil (local)".into(),
            chain_id: 31337,
            symbol: "IGN".into(),
            decimals: 18,
            rpc: vec!["http://127.0.0.1:8545".into()],
            submit: None,
            explorer: None,
            faucet: None,
            faucet_relay: None,
            bond_faucet: None,
            block_target_secs: 1,
            proposed: true,
            genesis_pinned: false,
            genesis: None,
            authority: None,
        }
    }

    /// The word a person reads for a chain's name (the naming rule of 15 October 2026): the public test network and
    /// the old one by their words, every other chain by its name.
    pub fn public_word(name: &str) -> String {
        match name {
            "Igneum public test network" => "the public test network".to_string(),
            "Igneum old public test network" => "the old public test network".to_string(),
            other => other.to_string(),
        }
    }

    /// Every chain the wallet knows, pinned or not.
    pub fn known() -> Vec<Chain> {
        vec![Self::igneum_devnet(), Self::igneum_devnet4(), Self::igneum_devnet5(), Self::igneum_devnet7(), Self::igneum_devnet8(), Self::igneum_devnet9(), Self::igneum_7320(), Self::igneum_7322(), Self::anvil()]
    }

    /// devnet7: the launch-equivalent chain (chain id 7317, network id 7371; sealers A7 and B7 and the compliant
    /// peer on the sealers' mesh endpoints (private); the registry in its version 3 native form as devnet5's; the
    /// public read endpoint alone listed). Pinned by the genesis of the protocol team's record.
    pub fn igneum_devnet7() -> Self {
        Chain {
            name: "Igneum development chain 7317".into(),
            chain_id: 7317,
            symbol: "IGN".into(),
            decimals: 18,
            rpc: vec![PUBLIC_DEVNET7_RPC.into()],
            submit: Some(PUBLIC_DEVNET7_SUBMIT.into()),
            explorer: None,
            faucet: None,
            faucet_relay: None,
            bond_faucet: None,
            block_target_secs: 15,
            proposed: true,
            genesis_pinned: true,
            genesis: Some(GENESIS_DEVNET7.into()),
            authority: Some(Authority {
                registry: Address::parse(REGISTRY_DEV).expect("a fixed address"),
                registry_version: 3,
                sortition: true,
            }),
        }
    }

    /// devnet9: the launch-clock chain (chain id 7319, network id 7391; the public endpoints in devnet7's form;
    /// the registry in its version 3 native form). Pinned by the genesis of the protocol team's line; while
    /// GENESIS_DEVNET9 is empty the entry is unpinned and a 7319 node reads as "Igneum development chain 7319 (not this build's
    /// pin)" under the re-seal rule.
    pub fn igneum_devnet9() -> Self {
        Chain {
            name: "Igneum development chain 7319".into(),
            chain_id: 7319,
            symbol: "IGN".into(),
            decimals: 18,
            // the public host takes raw sends too (reliability, 12 October 2026, 00:12 UK), so the submit is the
            // same host; the sealer A9's mesh endpoint (private) is not listed, no install can reach it
            rpc: vec![PUBLIC_DEVNET9_RPC.into()],
            submit: Some(PUBLIC_DEVNET9_SUBMIT.into()),
            explorer: Some(EXPLORER_DEVNET9.into()),
            faucet: Some("0x671D4f197b5F37430751bD9acA38789806dAcD23".into()),
            faucet_relay: Some("https://build.igneum.network/devnet9/faucet".into()),
            bond_faucet: Some("0xD235241bC06afDDa79f1834f4a702c67d0118Eda".into()),
            block_target_secs: 20,
            proposed: true,
            genesis_pinned: !GENESIS_DEVNET9.is_empty(),
            genesis: if GENESIS_DEVNET9.is_empty() { None } else { Some(GENESIS_DEVNET9.into()) },
            authority: Some(Authority {
                registry: Address::parse(REGISTRY_DEV).expect("a fixed address"),
                registry_version: 4,
                sortition: true,
            }),
        }
    }

    /// Chain 7320, the old public test network (chain id 7320, network id 7392), pinned by GENESIS_7320; the wallet's
    /// default chain from 0.0.8. The faucet relay's route keeps its devnet9 path (one relay, re-pointed to chain 7320
    /// by its environment on 14 October 2026).
    pub fn igneum_7320() -> Self {
        Chain {
            name: "Igneum old public test network".into(),
            chain_id: 7320,
            symbol: "IGN".into(),
            decimals: 18,
            rpc: vec![PUBLIC_7320_RPC.into()],
            submit: Some(PUBLIC_7320_RPC.into()),
            explorer: Some(EXPLORER_7320.into()),
            faucet: Some("0x93482fa8a07bba7d3cc33058dd4f51a8a30a66b4".into()),
            faucet_relay: Some("https://build.igneum.network/devnet9/faucet".into()),
            bond_faucet: Some("0xf7649aaa2999402d69444470c5b56d136b1365cb".into()),
            block_target_secs: 20,
            proposed: true,
            genesis_pinned: true,
            genesis: Some(GENESIS_7320.into()),
            authority: Some(Authority {
                registry: Address::parse(REGISTRY_DEV).expect("a fixed address"),
                registry_version: 4,
                sortition: true,
            }),
        }
    }

    /// Chain 7322, the public test network on the vector form (chain id 7322, network id 7394), pinned by
    /// GENESIS_7322, read at 7322's own public host; the faucets are filled in by their deployment record.
    pub fn igneum_7322() -> Self {
        Chain {
            name: "Igneum public test network".into(), // the chains file's name of record for 7322 (F-244, 16 October 2026); the test below ties them
            chain_id: 7322,
            symbol: "IGN".into(),
            decimals: 18,
            rpc: vec![PUBLIC_7322_RPC.into()],
            submit: Some(PUBLIC_7322_RPC.into()),
            explorer: Some(EXPLORER_7322.into()),
            // the faucets of 15 October 2026 (dev/chain/7322-faucets.json): the same addresses as chain 7320's (the
            // same deployer at nonces 0 and 1); the relay keeps its path and is re-pointed at step two of the swap
            faucet: Some("0x93482fa8a07bba7d3cc33058dd4f51a8a30a66b4".into()),
            faucet_relay: Some("https://build.igneum.network/faucet".into()), // the public path (the older path stays an alias on the host for wallets before 0.0.25)
            bond_faucet: Some("0xf7649aaa2999402d69444470c5b56d136b1365cb".into()),
            block_target_secs: 12, // 12-second blocks under node 0.10.40 (the move dated 16 October 2026, the founder's decision); the research chain measured 12.3 s over 16:05 to 17:05 UK that day
            proposed: true,
            genesis_pinned: true,
            genesis: Some(GENESIS_7322.into()),
            authority: Some(Authority {
                registry: Address::parse(REGISTRY_DEV).expect("a fixed address"),
                registry_version: 4,
                sortition: true,
            }),
        }
    }

    /// devnet8: the bench chain between devnet7 and devnet9 (chain id 7318), named without a pin: the fallback
    /// default if devnet9 slips (the project's word, 12 October 2026). A 7318 node reads by name with its genesis kept.
    pub fn igneum_devnet8() -> Self {
        Chain {
            name: "Igneum development chain 7318".into(),
            chain_id: 7318,
            symbol: "IGN".into(),
            decimals: 18,
            rpc: vec![PUBLIC_DEVNET8_RPC.into()],
            submit: Some(PUBLIC_DEVNET8_SUBMIT.into()),
            explorer: None,
            faucet: None,
            faucet_relay: None,
            bond_faucet: None,
            block_target_secs: 20,
            proposed: true,
            genesis_pinned: false,
            genesis: None,
            authority: Some(Authority {
                registry: Address::parse(REGISTRY_DEV).expect("a fixed address"),
                registry_version: 4,
                sortition: true,
            }),
        }
    }

    /// A chain by its name (the envelope's `network` field): the sentences are recomputed from the name on the signing
    /// side, so the name is the only thing that must travel.
    pub fn by_name(name: &str) -> Option<Self> {
        Self::known().into_iter().find(|c| c.name == name)
    }

    /// The chain a node serves, from its chain id and its genesis hash. A pinned chain is returned only on its own
    /// genesis; an unknown genesis under a known chain id gives the unpinned "chain <id>" entry, never a named chain.
    pub fn identify(chain_id: u64, genesis: Option<&str>) -> Self {
        let g = genesis.map(|s| s.trim().to_ascii_lowercase());
        for c in Self::known() {
            if c.chain_id != chain_id {
                continue;
            }
            match (&c.genesis, &g) {
                (Some(pin), Some(seen)) if pin.eq_ignore_ascii_case(seen) => return c,
                // a dev chain with an id of its own (devnet5 7315, devnet7 7317; the project's ruling of 11 October 2026)
                // is re-sealed by the protocol team from genesis under the same id, rules and registry address: a chain
                // under that id whose genesis is not this build's pin is still that chain, named so, with the seen
                // genesis kept and the entry marked unpinned (the page says "not this build's pin"); chain id 7310
                // stays strict (five chains share it)
                (Some(_), Some(seen)) if chain_id != 7310 => {
                    let mut d = c;
                    d.genesis = Some(seen.clone());
                    d.genesis_pinned = false;
                    return d;
                }
                // an unpinned entry names a chain only when it carries no registry (anvil): a registry chain with
                // no pin yet is never returned by name under the shared id 7310
                (None, _) if c.authority.is_none() && chain_id != 7310 => return c,
                // a registry chain with an id of its own and no pin in this build (devnet8; devnet9 until its
                // genesis is in the pin) reads by name, unpinned, the seen genesis kept: the same rule as the
                // re-seal arm above, since the id names the chain (the project's word, 12 October 2026: devnet8 the
                // fallback default)
                (None, seen) if chain_id != 7310 => {
                    let mut d = c;
                    d.genesis = seen.clone();
                    d.genesis_pinned = false;
                    return d;
                }
                _ => {}
            }
        }
        Self::unpinned(chain_id, g.as_deref())
    }

    /// An entry for a chain the wallet has no pin for: named by its id, the genesis kept for the record.
    pub fn unpinned(chain_id: u64, genesis: Option<&str>) -> Self {
        Chain {
            name: format!("chain {chain_id}"),
            chain_id,
            symbol: "IGN".into(),
            decimals: 18,
            rpc: vec![],
            submit: None,
            explorer: None,
            faucet: None,
            faucet_relay: None,
            bond_faucet: None,
            block_target_secs: 12,
            proposed: true,
            genesis_pinned: false,
            genesis: genesis.map(|g| g.to_string()),
            authority: None,
        }
    }

    /// The old entry point: a chain by id alone. Chain id 7310 is ambiguous (five dev chains), so it gives the
    /// unpinned entry; callers that know the genesis use `identify`.
    pub fn by_id(id: u64) -> Option<Self> {
        match id {
            7310 => Some(Self::unpinned(7310, None)),
            31337 => Some(Self::anvil()),
            _ => None,
        }
    }

    /// The registry's address when the chain carries work authority.
    pub fn registry(&self) -> Option<Address> {
        self.authority.as_ref().map(|a| a.registry)
    }

    /// The genesis pin as bytes, when set.
    pub fn genesis_bytes(&self) -> Result<Option<[u8; 32]>> {
        match &self.genesis {
            None => Ok(None),
            Some(g) => {
                let b = hex::decode(g)?;
                if b.len() != 32 {
                    return Err(Error::Input("a genesis hash has 32 bytes".into()));
                }
                let mut out = [0u8; 32];
                out.copy_from_slice(&b);
                Ok(Some(out))
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn devnets_are_told_apart_by_genesis() {
        assert_eq!(Chain::identify(7310, Some(GENESIS_BUILD1_DEVNET)).name, "Igneum development chain 7310");
        assert_eq!(Chain::identify(7310, Some(GENESIS_DEVNET4)).name, "Igneum development chain 7310, fourth cut");
        // upper-case hex is the same pin
        assert_eq!(Chain::identify(7310, Some(&GENESIS_DEVNET4.to_ascii_uppercase())).name, "Igneum development chain 7310, fourth cut");
        // an unknown genesis under 7310 is never a named chain
        let other = Chain::identify(7310, Some("0x00000000000000000000000000000000000000000000000000000000000000aa"));
        assert_eq!(other.name, "chain 7310");
        assert!(other.authority.is_none());
        // no genesis at all: unpinned
        assert_eq!(Chain::identify(7310, None).name, "chain 7310");
        // devnet5 is chain id 7315, pinned to the genesis of the protocol team's record; a re-sealed devnet5 under the same
        // id is still devnet5 with its registry, the seen genesis kept and the entry marked not pinned
        assert_eq!(Chain::identify(7315, Some(GENESIS_DEVNET5)).name, "Igneum development chain 7315");
        assert!(Chain::identify(7315, Some(GENESIS_DEVNET5)).genesis_pinned);
        assert_eq!(Chain::identify(7315, Some(GENESIS_DEVNET5)).authority.unwrap().registry_version, 3);
        let resealed = Chain::identify(7315, Some("0xe7adfe8e014bff9498c37c0dacf447f8f4578d950025981daea3b739d84a5a71"));
        assert_eq!(resealed.name, "Igneum development chain 7315");
        assert!(!resealed.genesis_pinned);
        assert_eq!(resealed.genesis.as_deref(), Some("0xe7adfe8e014bff9498c37c0dacf447f8f4578d950025981daea3b739d84a5a71"));
        assert_eq!(resealed.authority.unwrap().registry_version, 3);
        assert!(!Chain::identify(7315, None).genesis_pinned);
        assert_eq!(Chain::identify(7315, None).name, "chain 7315");
        // the public submit route is devnet5's alone and follows a re-seal; the nameless entry has none
        assert_eq!(Chain::igneum_devnet5().submit.as_deref(), Some(PUBLIC_DEVNET5_SUBMIT));
        assert_eq!(resealed.submit.as_deref(), Some(PUBLIC_DEVNET5_SUBMIT));
        assert!(Chain::identify(7315, None).submit.is_none());
        assert!(Chain::igneum_devnet4().submit.is_none());
        // devnet7: pinned by the record's genesis; re-sealed under 7317 is still devnet7; no submit route yet
        assert_eq!(Chain::identify(7317, Some(GENESIS_DEVNET7)).name, "Igneum development chain 7317");
        assert!(Chain::identify(7317, Some(GENESIS_DEVNET7)).genesis_pinned);
        let r7 = Chain::identify(7317, Some("0x00000000000000000000000000000000000000000000000000000000000000cc"));
        assert_eq!(r7.name, "Igneum development chain 7317");
        assert!(!r7.genesis_pinned);
        assert_eq!(r7.submit.as_deref(), Some(PUBLIC_DEVNET7_SUBMIT));
        assert_eq!(Chain::by_name("Igneum development chain 7317").unwrap().chain_id, 7317);
        // another 7315 genesis is devnet5 re-sealed (above), never a nameless chain; a 7310 chain with another
        // genesis stays nameless (five chains share that id)
        assert_eq!(Chain::identify(7310, Some("0x00000000000000000000000000000000000000000000000000000000000000bb")).name, "chain 7310");
        assert_eq!(Chain::identify(31337, None).name, "anvil (local)");
        // devnet9: pinned once GENESIS_DEVNET9 is filled; until then (and for devnet8, which has no pin) a node
        // under the chain's own id reads by name, unpinned, its genesis kept
        let r9 = Chain::identify(7319, Some("0x00000000000000000000000000000000000000000000000000000000000000dd"));
        assert_eq!(r9.name, "Igneum development chain 7319");
        assert_eq!(r9.genesis_pinned, GENESIS_DEVNET9.eq_ignore_ascii_case("0x00000000000000000000000000000000000000000000000000000000000000dd"));
        assert_eq!(r9.submit.as_deref(), Some(PUBLIC_DEVNET9_SUBMIT));
        if !GENESIS_DEVNET9.is_empty() {
            assert!(Chain::identify(7319, Some(GENESIS_DEVNET9)).genesis_pinned);
        }
        let r8 = Chain::identify(7318, Some("0x00000000000000000000000000000000000000000000000000000000000000ee"));
        assert_eq!(r8.name, "Igneum development chain 7318");
        assert!(!r8.genesis_pinned);
        assert_eq!(r8.genesis.as_deref(), Some("0x00000000000000000000000000000000000000000000000000000000000000ee"));
        assert_eq!(Chain::by_name("Igneum development chain 7319").unwrap().chain_id, 7319);
        // chain 7320: pinned by its genesis; another 7320 genesis reads by name, unpinned (the re-seal rule)
        let r10 = Chain::identify(7320, Some(GENESIS_7320));
        assert_eq!(r10.name, "Igneum old public test network");
        assert!(r10.genesis_pinned);
        assert_eq!(r10.faucet.as_deref(), Some("0x93482fa8a07bba7d3cc33058dd4f51a8a30a66b4"));
        assert_eq!(Chain::by_name("Igneum old public test network").unwrap().chain_id, 7320);
        // chain 7322: pinned by its genesis at 7322's own public host; another 7322 genesis reads by name, unpinned
        let r12 = Chain::identify(7322, Some(GENESIS_7322));
        assert_eq!(r12.name, "Igneum public test network");
        assert!(r12.genesis_pinned);
        assert_eq!(r12.rpc, vec![PUBLIC_7322_RPC.to_string()]);
        assert_eq!(Chain::by_name("Igneum public test network").unwrap().chain_id, 7322);
    }

    /// The public test network carries the chains file's name on every surface (F-244, 16 October 2026): the chain
    /// table, the page's word, the builders' manifests and the pointer all read it from here.
    #[test]
    fn the_public_test_networks_name_is_the_chains_entrys() {
        let path = concat!(env!("CARGO_MANIFEST_DIR"), "/../../dev/chain/igneum-chains.json");
        let text = std::fs::read_to_string(path).expect("the chains file beside the tree");
        let file: serde_json::Value = serde_json::from_str(&text).unwrap();
        let entry = file["networks"].as_array().unwrap().iter().find(|n| n["chain_id"] == 7322).expect("the 7322 entry");
        assert_eq!(entry["name"].as_str().unwrap(), Chain::igneum_7322().name);
        assert_eq!(Chain::public_word(&Chain::igneum_7322().name), "the public test network");
    }

    #[test]
    fn by_name_round_trips_and_carries_the_registry() {
        let c = Chain::by_name("Igneum development chain 7310, fourth cut").unwrap();
        assert_eq!(c.registry().unwrap().to_checksum().to_ascii_lowercase(), REGISTRY_DEV);
        assert_eq!(c.authority.as_ref().unwrap().registry_version, 2);
        assert!(Chain::by_name("Igneum development chain 7310").unwrap().registry().is_none());
        assert!(Chain::by_name("chain 7310").is_none());
        assert_eq!(Chain::by_id(7310).unwrap().name, "chain 7310");
    }

    #[test]
    fn serialises_with_the_new_fields_optional() {
        let c = Chain::igneum_devnet4();
        let j = serde_json::to_string(&c).unwrap();
        let back: Chain = serde_json::from_str(&j).unwrap();
        assert_eq!(back, c);
        // an old record without the fields still reads
        let old = r#"{"name":"x","chain_id":1,"symbol":"IGN","decimals":18,"rpc":[],"explorer":null,"block_target_secs":15,"proposed":true}"#;
        let o: Chain = serde_json::from_str(old).unwrap();
        assert!(o.genesis.is_none() && o.authority.is_none());
    }
}
