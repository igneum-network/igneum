//! Igneum Wallet core.
//!
//! The library behind the signer, the engine and the CLI (docs/product-b/DESIGN.md, section 1). It holds the
//! independent signing path (own keccak, own RLP, own transaction encoding, recoverable ECDSA through `k256`), the
//! vault, the mnemonic derivation, the watch-only envelopes, the confirmation sentences and the settlement-confidence
//! model. It has no network code at all: the JSON-RPC client is the `igneum-wallet-rpc` crate, which the engine and
//! the CLI link and the signer never does.
//!
//! Rules the code keeps:
//! - a secret lives in a `Zeroizing` buffer and is never formatted, logged or serialised except by `vault::seal`;
//! - every signing call takes the sentences the person saw, recomputes them from the payload and refuses on a
//!   mismatch (`signer::sign_transaction`);
//! - nothing here says "final": `settlement` produces the words the window shows.

pub mod address;
pub mod authority;
pub mod bridge;
pub mod chain;
pub mod display;
pub mod eip712;
pub mod envelope;
pub mod error;
pub mod hex;
pub mod keccak;
pub mod ledger;
pub mod mnemonic;
pub mod password;
pub mod rlp;
pub mod settlement;
pub mod simulation;
pub mod sign;
pub mod signer;
pub mod tx;
pub mod u256;
pub mod vault;

pub use address::Address;
pub use error::{Error, Result};
pub use u256::U256;
