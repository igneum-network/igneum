//! Where the chain comes from (OLD-BASE.md step 3: the previous wallet's `node.rs` order on the restart's RPC
//! client), chosen at start and again whenever the source goes away:
//!   1. a node named by hand (`--rpc`, the Settings page, `IGNEUM_WALLET_RPC`): used as it is, nothing else probed;
//!   2. a node on this machine (Ember's, or one the person runs: 127.0.0.1:8545) when it serves the pinned chain,
//!      read by its genesis hash (a dev node on another chain is never taken for the chain the wallet is on);
//!   3. the chain's public read-only endpoint (the build's default).
//! The previous wallet's fourth source, a bundled node started by the wallet, is not carried: no package bundles a
//! node today (the plan's step 5 names it if one ever does). `State.node.source` says which; the trust line says
//! what the source can see.

use igneum_wallet_core::chain::Chain;
use igneum_wallet_rpc::Rpc;

pub const LOCAL_RPC: &str = "http://127.0.0.1:8545";

#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Source {
    /// a node the person named: used as it is
    Named { url: String },
    /// a node on this machine serving the pinned chain
    Local { url: String },
    /// the chain's public read-only endpoint
    Public { url: String },
}

impl Source {
    pub fn url(&self) -> &str {
        match self {
            Source::Named { url } | Source::Local { url } | Source::Public { url } => url,
        }
    }
    pub fn name(&self) -> &'static str {
        match self {
            Source::Named { .. } => "named",
            Source::Local { .. } => "local",
            Source::Public { .. } => "public",
        }
    }
    pub fn trust(&self) -> &'static str {
        match self {
            Source::Named { .. } => "the node you named sees your addresses; it cannot spend",
            Source::Local { .. } => "a node on this machine serving this chain; it sees your addresses and cannot spend",
            Source::Public { .. } => "the chain's public read-only endpoint: it sees your addresses and the requests of everyone who uses it; it cannot spend; a send goes to the chain's public submit route, which forwards the signed transaction and nothing else",
        }
    }
}

/// True when `url` is one of the public endpoints a known chain lists (so the person never named it).
pub fn is_public_endpoint(url: &str) -> bool {
    Chain::known().iter().any(|c| c.rpc.iter().any(|r| r == url && r.starts_with("https://")))
}

/// A node at `url` serving the chain whose genesis is `genesis` (the pin), read in one chain id and one block 0.
fn serves(url: &str, chain_id: u64, genesis: &str) -> bool {
    let rpc = Rpc::new(url);
    match (rpc.chain_id(), rpc.genesis_hash()) {
        (Ok(id), Ok(g)) => id == chain_id && g.eq_ignore_ascii_case(genesis),
        _ => false,
    }
}

/// The source for a wallet whose settings name `settings_rpc`, `named` when a person set it (or `--rpc` did), on
/// the chain `pinned` (the chain the settings' endpoint belongs to, by its pin).
pub fn choose(settings_rpc: &str, named: bool, pinned: &Chain) -> Source {
    if let Ok(u) = std::env::var("IGNEUM_WALLET_RPC") {
        if !u.is_empty() {
            return Source::Named { url: u };
        }
    }
    if named || !is_public_endpoint(settings_rpc) {
        return Source::Named { url: settings_rpc.to_string() };
    }
    if let Some(g) = pinned.genesis.as_deref() {
        if serves(LOCAL_RPC, pinned.chain_id, g) {
            return Source::Local { url: LOCAL_RPC.to_string() };
        }
    }
    Source::Public { url: settings_rpc.to_string() }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_named_node_is_never_second_guessed() {
        let c = Chain::igneum_devnet7();
        assert_eq!(choose("http://192.0.2.10:8648", false, &c), Source::Named { url: "http://192.0.2.10:8648".into() });
        assert_eq!(choose(igneum_wallet_core::chain::PUBLIC_DEVNET7_RPC, true, &c).name(), "named");
        assert!(is_public_endpoint(igneum_wallet_core::chain::PUBLIC_DEVNET7_RPC));
        assert!(!is_public_endpoint("http://127.0.0.1:8545"));
    }

    #[test]
    fn the_public_endpoint_falls_back_when_no_local_node_serves_the_chain() {
        // nothing serves 127.0.0.1:8545 with devnet5's genesis on a test box: the public endpoint is the source
        let c = Chain::igneum_devnet7();
        let s = choose(igneum_wallet_core::chain::PUBLIC_DEVNET7_RPC, false, &c);
        assert!(matches!(s, Source::Public { .. } | Source::Local { .. }));
        assert!(s.trust().contains("cannot spend"));
    }
}
