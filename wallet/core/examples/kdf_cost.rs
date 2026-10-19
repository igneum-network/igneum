//! ST-1 (wallet-security's THREAT-MODEL.md section 7): the cost the vault's key derivation imposes per password
//! tried against a stolen file. The release seals with Argon2id at 256 MiB, 3 passes, 1 lane; this times one seal
//! and a run of opens at those parameters and prints the seconds a single derivation takes on this box. That time
//! is the cost per candidate: a random twelve-character password has about 78 bits, so no run of guesses reaches
//! it, and the number the row records is this per-derivation cost, which says how slow a search would be.
//!
//!   cargo run --release -p igneum-wallet-core --example kdf_cost [opens]

use std::time::Instant;

use igneum_wallet_core::address::Address;
use igneum_wallet_core::vault::{self, KdfParams, Public, Secret, SecretKind};

fn main() {
    let opens: u32 = std::env::args().nth(1).and_then(|a| a.parse().ok()).unwrap_or(5);
    let kdf = KdfParams::default_desktop();
    let password = b"a correct password for this measure";
    let secret = Secret { kind: SecretKind::Key, text: "0x".to_string() + &"11".repeat(32) };
    let public = Public {
        address: Address::parse("0x0000000000000000000000000000000000000001").unwrap(),
        kind: SecretKind::Key,
        created_unix: 0,
        chain_id_hint: None,
    };
    let t0 = Instant::now();
    let file = vault::seal(&secret, password, public, kdf.clone()).expect("seal");
    let seal_secs = t0.elapsed().as_secs_f64();
    let mut total = 0.0;
    for _ in 0..opens {
        let t = Instant::now();
        vault::open(&file, password).expect("open");
        total += t.elapsed().as_secs_f64();
    }
    let per = total / opens as f64;
    println!("argon2id m_kib {} t {} p {}", kdf.m_kib, kdf.t, kdf.p);
    println!("seal {seal_secs:.3} s; derivation (the cost per candidate) {per:.3} s over {opens} opens");
    println!("one core would try about {:.1} passwords a second at these parameters", 1.0 / per);
}
