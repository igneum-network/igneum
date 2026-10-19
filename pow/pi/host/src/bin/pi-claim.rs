//! `pi-claim`: the hostile claim's proof (SPEC-R0A-v0.md 0.9.1, domain 1, K 1) from files, for the wallet (product
//! B, D-B15) and for the registry's fixtures. The secret is read from a file by path and never printed; the proof
//! goes to a file in its wire form `u8(2) || receipt`.
//!
//!   pi-claim prove  <sk-file (32-byte hex)> <chain id (decimal)> <nonce_b> <to (20-byte hex)> <out.bin>
//!   pi-claim verify <pk (32-byte hex)>      <chain id (decimal)> <nonce_b> <to (20-byte hex)> <pi.bin>
//!   pi-claim pk     <sk-file>                 prints pk = G(sk)
//!   pi-claim message <chain id> <pk> <nonce_b> <to>  prints m_0
//!
//! C is the envelope's u64be(chain id) (the project's ruling of 10:25 UK, 11 Oct 2026; devnet5 7315); the proof is the Groth16
//! wrap (domain 1), sub-kilobyte, about a minute of CPU proving behind a container. The 32-byte test constant of the
//! first devnet4 rows is retired: those rows are labelled devnet4 and are not this tool's.
//!
//! `prove` prints `pk`, `m_0`, the share `s_0`, the receipt's bytes and the prove time as JSON on stdout; `verify`
//! prints the verdict and exits 0 on ok, 1 on a refusal. The journal the registry compares is
//! `u8(1) || LE32(1) || pk || m_0 || s_0` (101 bytes); `s_0` travels in the transaction beside `pi`.
use igneum_pi::{bond_pk, claim_message, pi_prove, pi_verify, PI_CLAIM_MAX_BYTES, SYSTEM_RISC0};
use std::fs;
use std::process::exit;

fn hex_n<const N: usize>(what: &str, s: &str) -> [u8; N] {
    let t = s.trim();
    let t = t.strip_prefix("0x").unwrap_or(t);
    let b = hex::decode(t).unwrap_or_else(|e| {
        eprintln!("pi-claim: {what}: {e}");
        exit(2)
    });
    if b.len() != N {
        eprintln!("pi-claim: {what} has {} bytes, not {N}", b.len());
        exit(2);
    }
    let mut out = [0u8; N];
    out.copy_from_slice(&b);
    out
}

fn sk_from_file(path: &str) -> [u8; 32] {
    let text = fs::read_to_string(path).unwrap_or_else(|e| {
        eprintln!("pi-claim: {path}: {e}");
        exit(2)
    });
    hex_n::<32>("the secret", &text)
}

fn main() {
    let a: Vec<String> = std::env::args().collect();
    match a.get(1).map(|s| s.as_str()) {
        Some("pk") if a.len() == 3 => {
            println!("{}", hex::encode(bond_pk(&sk_from_file(&a[2]))));
        }
        Some("message") if a.len() == 6 => {
            let c: u64 = a[2].parse().expect("chain id");
            let pk = hex_n::<32>("pk", &a[3]);
            let nonce: u64 = a[4].parse().expect("nonce_b");
            let to = hex_n::<20>("to", &a[5]);
            println!("{}", hex::encode(claim_message(c, &pk, nonce, &to)));
        }
        Some("prove") if a.len() == 7 => {
            let sk = sk_from_file(&a[2]);
            let c: u64 = a[3].parse().expect("chain id");
            let nonce: u64 = a[4].parse().expect("nonce_b");
            let to = hex_n::<20>("to", &a[5]);
            let pk = bond_pk(&sk);
            let m0 = claim_message(c, &pk, nonce, &to);
            let p = pi_prove(1, &sk, &[m0]).unwrap_or_else(|e| {
                eprintln!("pi-claim: prove: {e}");
                exit(1)
            });
            fs::write(&a[6], &p.pi).unwrap_or_else(|e| {
                eprintln!("pi-claim: {}: {e}", a[6]);
                exit(2)
            });
            println!(
                "{{\"pk\":\"{}\",\"m0\":\"{}\",\"share\":\"{}\",\"pi_bytes\":{},\"pi_max_bytes\":{},\"system\":{},\"prove_ms\":{:.0},\"out\":\"{}\"}}",
                hex::encode(p.pk),
                hex::encode(m0),
                hex::encode(p.shares[0]),
                p.pi.len(),
                PI_CLAIM_MAX_BYTES,
                SYSTEM_RISC0,
                p.prove_ms,
                a[6]
            );
        }
        Some("verify") if a.len() == 7 => {
            let pk = hex_n::<32>("pk", &a[2]);
            let c: u64 = a[3].parse().expect("chain id");
            let nonce: u64 = a[4].parse().expect("nonce_b");
            let to = hex_n::<20>("to", &a[5]);
            let pi = fs::read(&a[6]).unwrap_or_else(|e| {
                eprintln!("pi-claim: {}: {e}", a[6]);
                exit(2)
            });
            // the share is what the proof says it is: the verifier takes it from the transaction beside pi; here the
            // receipt's journal supplies it (pi_verify compares the journal against the expected bytes, so a share
            // is needed: read it from the journal first)
            let m0 = claim_message(c, &pk, nonce, &to);
            let share = match journal_share(&pi) {
                Some(s) => s,
                None => {
                    println!("refused: the receipt carries no readable journal");
                    exit(1)
                }
            };
            match pi_verify(1, &pk, &[m0], &[share], &pi) {
                Ok(()) => println!("ok: the claim's proof verifies under domain 1 for pk {} and claimant {} (nonce {nonce}); share {}", hex::encode(pk), hex::encode(to), hex::encode(share)),
                Err(e) => {
                    println!("refused: {e}");
                    exit(1)
                }
            }
        }
        _ => {
            eprintln!("usage: pi-claim prove <sk-file> <chain id> <nonce_b> <to> <out.bin> | verify <pk> <chain id> <nonce_b> <to> <pi.bin> | pk <sk-file> | message <chain id> <pk> <nonce_b> <to>");
            exit(2)
        }
    }
}

/// The share `s_0` from the receipt's journal (the last 32 bytes of the 101-byte journal at K 1), for `verify`.
fn journal_share(pi: &[u8]) -> Option<[u8; 32]> {
    let r = igneum_pi::Risc0::for_domain(1).verify_receipt(1, pi).ok()?;
    let j = r.journal.bytes;
    if j.len() != 5 + 32 + 64 {
        return None;
    }
    let mut s = [0u8; 32];
    s.copy_from_slice(&j[69..101]);
    Some(s)
}
