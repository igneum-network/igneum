//! Igneum Wallet, the work-authority side (docs/product-b/DESIGN.md 1.13).
//!
//! Three things, none of them a key the signer holds: the draw (`draw`: a bonded identity's units, its share of the
//! lottery and its eligible round at a height, the node's form from `consensus/igneum/sortition.go` recomputed so
//! the wallet never has to trust the node's own reading), the hostile claim's signature (`claim`: the bond key's
//! Ed25519 signature over the claim message, DEV-ONLY until a registry reads it), and the pool's signed receipts
//! (`receipts`: the ecosystem's `mining.receipt` form verified under the pool's receipt key, the per-member digest
//! chain and the running total, so a member can prove its credit from its receipts alone).
//!
//! The bond key and the signing key are Ed25519 keys and never secp256k1 ones: they are not the account, and the
//! wallet's vault does not hold them. This crate reads them from files the person names (0600, 32-byte seeds) and
//! zeroises nothing beyond what `ed25519-dalek` does, which is why a bond key is "never on a mining machine" and,
//! for now, never in the window either.

pub mod claim;
pub mod draw;
pub mod receipts;

pub use igneum_wallet_core::{Address, Error, Result, U256};
