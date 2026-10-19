//! The C ABI over the landed pi sets (pow/vectors/pi/<tag>/pi.json, receipts beside it): every row's code against its
//! expectation (ok, or the refusal the row was built for), through `ignpi_verify` exactly as the node calls it, with the
//! verify time per row; the median must sit under the 20 ms budget on a build-box core (an EPYC 9454 class) (PI-NOTES.md section 3).
//! Run from the workspace: `cargo test --release -p igneum-pi-ffi -- --nocapture`.
use igneum_pi_ffi::*;
use serde::Deserialize;
use std::path::{Path, PathBuf};
use std::time::Instant;

#[derive(Deserialize)]
struct Row {
    name: String,
    vector: usize,
    domain: u8,
    k: u32,
    pk: String,
    m: Vec<String>,
    shares: Vec<String>,
    receipt: String,
    expect: String,
    #[serde(default)]
    flip: Option<usize>,
}

#[derive(Deserialize)]
struct Set {
    image_id: String,
    #[serde(default)]
    no_claim: bool,
    rows: Vec<Row>,
}

fn hex32(s: &str) -> [u8; 32] {
    let v = hex::decode(s).expect("hex");
    let mut a = [0u8; 32];
    a.copy_from_slice(&v);
    a
}

/// The code a row's name stands for (the reason it was built), beside its ok/fail expectation.
fn expected_code(name: &str, expect: &str) -> i32 {
    if expect == "ok" {
        return OK;
    }
    match name {
        "wrong_sk" | "wrong_share" | "wrong_pk" | "replayed" | "claim_wrong_to" => E_JOURNAL,
        "bad_receipt" => E_PROOF,
        "oversize" | "claim_composite" => E_BOUND,
        "claim_as_block" => E_KIND,
        other => panic!("a row this test does not know: {other}"),
    }
}

fn sets_dir() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR")).join("../../vectors/pi")
}

fn verify(domain: u8, pk: &[u8; 32], m: &[[u8; 32]], s: &[[u8; 32]], pi: &[u8]) -> i32 {
    let mb: Vec<u8> = m.iter().flat_map(|x| x.iter().copied()).collect();
    let sb: Vec<u8> = s.iter().flat_map(|x| x.iter().copied()).collect();
    unsafe { ignpi_verify(domain, m.len() as u32, pk.as_ptr(), mb.as_ptr(), sb.as_ptr(), if pi.is_empty() { std::ptr::null() } else { pi.as_ptr() }, pi.len()) }
}

#[test]
fn constants() {
    assert_eq!(ignpi_abi_version(), 1);
    assert_eq!(ignpi_system(), 2);
    assert_eq!(ignpi_k_max(), 64);
    assert_eq!(ignpi_max_bytes(0), 294_912);
    assert_eq!(ignpi_max_bytes(1), 2_048);
    assert_eq!(ignpi_max_bytes(2), 0);
    let mut id = [0u8; 32];
    unsafe { ignpi_image_id(id.as_mut_ptr()) };
    assert_eq!(hex::encode(id), "006bf405038926b456e1c6056f4564358e0fb1cab70f5d5d7f3257cb8a83787c");
    assert_eq!(image_id(), hex::encode(id));
    let name = unsafe { std::ffi::CStr::from_ptr(ignpi_code_name(E_JOURNAL)) };
    assert!(name.to_str().unwrap().starts_with("journal"));
}

#[test]
fn malformed_calls() {
    let z = [0u8; 32];
    assert_eq!(verify(2, &z, &[z], &[z], &[2u8; 4]), E_ARG, "domain 2");
    assert_eq!(unsafe { ignpi_verify(0, 0, z.as_ptr(), z.as_ptr(), z.as_ptr(), z.as_ptr(), 32) }, E_ARG, "k 0");
    assert_eq!(unsafe { ignpi_verify(0, 65, z.as_ptr(), z.as_ptr(), z.as_ptr(), z.as_ptr(), 32) }, E_ARG, "k 65");
    assert_eq!(unsafe { ignpi_verify(0, 1, std::ptr::null(), z.as_ptr(), z.as_ptr(), z.as_ptr(), 32) }, E_ARG, "null pk");
    assert_eq!(verify(0, &z, &[z], &[z], &[]), E_SYSTEM, "empty pi");
    assert_eq!(verify(0, &z, &[z], &[z], &[1u8; 8]), E_SYSTEM, "identifier 1");
    assert_eq!(verify(0, &z, &[z], &[z], &[2u8; 8]), E_DECODE, "garbage after the identifier");
    assert_eq!(verify(0, &z, &[z], &[z], &vec![2u8; 294_913]), E_BOUND, "one byte over the block's bound");
    assert_eq!(verify(1, &z, &[z], &[z], &vec![2u8; 2_049]), E_BOUND, "one byte over the claim's bound");
}

/// The distribution (the outside review's gate-14 reading): the valid K 2 rows verified IGNPI_BENCH_N times (default 1000)
/// through the C ABI, with the receipt's decode timed apart (bincode over the 221 KB wire form) so the STARK's share
/// is named; median, p95, p99, max in ms.
#[test]
fn percentiles() {
    let n: usize = std::env::var("IGNPI_BENCH_N").ok().and_then(|v| v.parse().ok()).unwrap_or(1000);
    // IGNPI_BENCH_SET names the set (the launch document's: v8-d2048-s0-r64-k32-p256-w128-b2-x16-f5152-k2-n100)
    let set_name = std::env::var("IGNPI_BENCH_SET").unwrap_or_else(|_| "v8-d2048-s0-r64-k32-p256-w128-b16-x16-f1056-k2".into());
    let tag = sets_dir().join(&set_name);
    let set: Set = serde_json::from_str(&std::fs::read_to_string(tag.join("pi.json")).unwrap()).unwrap();
    let rows: Vec<([u8; 32], Vec<[u8; 32]>, Vec<[u8; 32]>, Vec<u8>)> = set
        .rows
        .iter()
        .filter(|r| r.name == "valid" && r.domain == 0)
        .map(|r| (hex32(&r.pk), r.m.iter().map(|x| hex32(x)).collect(), r.shares.iter().map(|x| hex32(x)).collect(), std::fs::read(tag.join(&r.receipt)).unwrap()))
        .collect();
    assert!(!rows.is_empty());
    let q = |v: &[f64], p: f64| v[((v.len() - 1) as f64 * p) as usize];
    let (pk, m, s, pi) = &rows[0];
    let t0 = Instant::now();
    assert_eq!(verify(0, pk, m, s, pi), OK);
    let first = t0.elapsed().as_secs_f64() * 1e3;
    let mut full = Vec::with_capacity(n);
    let mut decode = Vec::with_capacity(n);
    for i in 0..n {
        let (pk, m, s, pi) = &rows[i % rows.len()];
        let t1 = Instant::now();
        assert_eq!(verify(0, pk, m, s, pi), OK);
        full.push(t1.elapsed().as_secs_f64() * 1e3);
        let t2 = Instant::now();
        let r: risc0_zkvm::Receipt = bincode::deserialize(&pi[1..]).unwrap();
        decode.push(t2.elapsed().as_secs_f64() * 1e3);
        std::hint::black_box(r);
    }
    full.sort_by(|a, b| a.partial_cmp(b).unwrap());
    decode.sort_by(|a, b| a.partial_cmp(b).unwrap());
    let host = std::fs::read_to_string("/etc/hostname").unwrap_or_default().trim().to_string();
    println!("PERCENTILES host {host} set {set_name} n {n} valid rows ({} distinct proofs, {} bytes each) through ignpi_verify: first {first:.2} ms; min {:.2} median {:.2} p95 {:.2} p99 {:.2} max {:.2} ms; the receipt's decode alone median {:.2} p99 {:.2} ms", rows.len(), rows[0].3.len(), full[0], q(&full, 0.5), q(&full, 0.95), q(&full, 0.99), full[n - 1], q(&decode, 0.5), q(&decode, 0.99));
}

#[test]
fn landed_sets() {
    let dir = sets_dir();
    let mut tags: Vec<PathBuf> = std::fs::read_dir(&dir).expect("pow/vectors/pi").filter_map(|e| e.ok().map(|e| e.path())).filter(|p| p.join("pi.json").exists()).collect();
    tags.sort();
    assert!(!tags.is_empty(), "no sets under {}", dir.display());
    let mut total = 0;
    let mut times_ok: Vec<f64> = Vec::new();
    for tag in &tags {
        let set: Set = serde_json::from_str(&std::fs::read_to_string(tag.join("pi.json")).unwrap()).unwrap();
        assert_eq!(set.image_id, image_id(), "{}: the set's image id is not the pinned PI_IMAGE_ID", tag.display());
        for r in &set.rows {
            let mut pi: Vec<u8> = if r.name == "oversize" { vec![2u8; ignpi_max_bytes(r.domain) + 1] } else { std::fs::read(tag.join(&r.receipt)).expect("receipt") };
            if let Some(i) = r.flip {
                pi[i] ^= 1;
            }
            let pk = hex32(&r.pk);
            let m: Vec<[u8; 32]> = r.m.iter().map(|x| hex32(x)).collect();
            let s: Vec<[u8; 32]> = r.shares.iter().map(|x| hex32(x)).collect();
            assert_eq!(m.len() as u32, r.k);
            let t0 = Instant::now();
            let code = verify(r.domain, &pk, &m, &s, &pi);
            let ms = t0.elapsed().as_secs_f64() * 1e3;
            let want = expected_code(&r.name, &r.expect);
            println!("{} {:<14} v{} domain {} K {:>2} {:>6.2} ms code {} ({})", if code == want { "PASS" } else { "FAIL" }, r.name, r.vector, r.domain, r.k, ms, code, unsafe { std::ffi::CStr::from_ptr(ignpi_code_name(code)) }.to_str().unwrap());
            assert_eq!(code, want, "{}: row {} v{}", tag.file_name().unwrap().to_string_lossy(), r.name, r.vector);
            if r.expect == "ok" && r.domain == 0 {
                times_ok.push(ms);
            }
            total += 1;
        }
        println!("{}: {} rows{}", tag.file_name().unwrap().to_string_lossy(), set.rows.len(), if set.no_claim { " (no-claim set)" } else { "" });
    }
    times_ok.sort_by(|a, b| a.partial_cmp(b).unwrap());
    let median = times_ok[times_ok.len() / 2];
    println!("{total} rows; the block's valid rows verify in median {median:.2} ms, max {:.2} ms (budget 20 ms on a build-box core (an EPYC 9454 class))", times_ok.last().unwrap());
    assert!(total >= 36, "the first two landed sets alone carry 36 rows; {total} read");
    assert!(median < 20.0, "the median verify {median:.2} ms is over the 20 ms budget");
}
