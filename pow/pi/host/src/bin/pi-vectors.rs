//! The `pi` vector class (SPEC-R0A-v0.md section 0.9.1).
//!
//!   pi-vectors gen <keyed set dir> <out dir> [--no-claim]
//!                                                 rows from the set's trace.json (the test secret, the m_i, the shares);
//!                                                 --no-claim leaves the claim rows out and flags the set (a host without
//!                                                 docker or x86, as the GPU team's pod: the block rows only)
//!   pi-vectors check <out dir>                    every row's verdict against its expectation; the claim rows present
//!                                                 unless the set is flagged no_claim (the project's ruling of 10:3x UK)
//!   pi-vectors bench <K> [n]                      prove and verify timings at K (the block's two kinds; at K 1 the claim's Groth16 too)
//!
//! Rows: valid, wrong_sk, wrong_share, wrong_pk, replayed (the other vector's receipt), bad_receipt (a byte flipped),
//! oversize (a pi over the domain's bound, synthesised at check), claim (domain 1, K 1, the Groth16 wrap), claim_as_block
//! (the claim's receipt under domain 0), claim_wrong_to (another claimant's message) and claim_composite (a claim carrying
//! the block's kind). Every row is public by construction: the secret is the set's test secret.
use igneum_pi::{bond_pk, claim_message, guest_image_id_hex, image_id_hex, max_bytes_for, share, Kind, ProofSystem, Risc0, PI_CLAIM_MAX_BYTES, PI_MAX_BYTES, SYSTEM_RISC0};
use serde::{Deserialize, Serialize};
use serde_json::Value;
use sha2::{Digest as _, Sha256};
use std::fs;
use std::path::Path;
use std::time::Instant;

#[derive(Serialize, Deserialize, Clone)]
struct Row {
    name: String,
    vector: usize,
    domain: u8,
    k: u32,
    header: String,
    nonce: String,
    pk: String,
    m: Vec<String>,
    shares: Vec<String>,
    receipt: String,
    receipt_bytes: usize,
    receipt_sha256: String,
    expect: String,
    reason: String,
    /// bad_receipt rows of the 100-row sets (11 Oct 2026): the receipt file is the valid proof's and the byte at this
    /// index is flipped at check (no stored copy; receipt_sha256 is the flipped bytes' sha); absent on the first sets,
    /// whose bad_receipt files are stored
    #[serde(default, skip_serializing_if = "Option::is_none")]
    flip: Option<usize>,
}

#[derive(Serialize, Deserialize)]
struct Bench {
    host: String,
    threads: usize,
    kind: String,
    k: u32,
    prove_ms: Vec<f64>,
    verify_ms: Vec<f64>,
    receipt_bytes: Vec<usize>,
    user_cycles: u64,
    total_cycles: u64,
}

#[derive(Serialize, Deserialize)]
struct Set {
    candidate: String,
    class: String,
    generator: String,
    system: u8,
    image_id: String,
    pi_max_bytes: usize,
    pi_claim_max_bytes: usize,
    kind: String,
    params: Value,
    source: String,
    /// true when the generator ran with --no-claim: the four claim rows (domain 1's Groth16 wrap, which needs docker
    /// and x86) are absent by the flag, not by loss; `check` accepts such a set and refuses an unflagged one without them
    #[serde(default)]
    no_claim: bool,
    rows: Vec<Row>,
    bench: Vec<Bench>,
}

/// the names of the claim rows a full set carries (gen's domain 1 block)
const CLAIM_ROWS: [&str; 4] = ["claim", "claim_as_block", "claim_wrong_to", "claim_composite"];

fn hex32(s: &str) -> [u8; 32] {
    let v = hex::decode(s).expect("hex");
    let mut a = [0u8; 32];
    a.copy_from_slice(&v);
    a
}

fn to_hex(b: &[u8]) -> String {
    hex::encode(b)
}

fn sha256_hex(b: &[u8]) -> String {
    to_hex(&Sha256::digest(b))
}

fn host_name() -> String {
    fs::read_to_string("/etc/hostname").map(|s| s.trim().to_string()).unwrap_or_else(|_| "unknown".into())
}

/// A keyed vector read from the trace: the test secret, the seed S, and (m_i, s_i) per keyed round.
struct Keyed {
    header: String,
    nonce: String,
    sk: [u8; 32],
    m: Vec<[u8; 32]>,
    s: Vec<[u8; 32]>,
}

/// The set's keyed rows: the reference's `keyed` class file carrying the messages `m` (ignh_hash_keyed_m, 11 Oct 2026),
/// KEYED_FILE in the set dir (default keyed.json; `IGNPI_KEYED_FILE` names another, as keyed-n100.json); when the file
/// has no `m` field (the first sets) the trace is read instead. Every share is checked against H(test_sk, i, m_i).
fn read_input(dir: &Path) -> (Value, Vec<Keyed>) {
    let name = std::env::var("IGNPI_KEYED_FILE").unwrap_or_else(|_| "keyed.json".into());
    let path = dir.join(&name);
    if let Ok(text) = fs::read_to_string(&path) {
        let v: Value = serde_json::from_str(&text).expect("keyed file is not JSON");
        let rows = v["vectors"].as_array().expect("vectors");
        if rows.first().map(|r| r["m"].is_string()).unwrap_or(false) {
            let params = v["params"].clone();
            let k = params["keyed_rounds"].as_u64().expect("keyed_rounds") as usize;
            let mut out = Vec::new();
            for r in rows {
                let sk = hex32(r["test_sk"].as_str().expect("test_sk"));
                let mb = hex::decode(r["m"].as_str().unwrap()).expect("m hex");
                let sb = hex::decode(r["shares"].as_str().expect("shares")).expect("shares hex");
                assert_eq!(mb.len(), 32 * k, "m is not K x 32 bytes");
                assert_eq!(sb.len(), 32 * k, "shares is not K x 32 bytes");
                let m: Vec<[u8; 32]> = mb.chunks(32).map(|c| { let mut a = [0u8; 32]; a.copy_from_slice(c); a }).collect();
                let s: Vec<[u8; 32]> = sb.chunks(32).map(|c| { let mut a = [0u8; 32]; a.copy_from_slice(c); a }).collect();
                for i in 0..k {
                    assert_eq!(share(&sk, i as u32, &m[i]), s[i], "the keyed row's share {i} is not H(test_sk, i, m_i)");
                }
                out.push(Keyed { header: r["header"].as_str().unwrap().to_string(), nonce: r["nonce"].as_str().unwrap().to_string(), sk, m, s });
            }
            eprintln!("input: {} ({} rows with m)", path.display(), out.len());
            return (params, out);
        }
    }
    read_trace(dir)
}

fn read_trace(dir: &Path) -> (Value, Vec<Keyed>) {
    let text = fs::read_to_string(dir.join("trace.json")).expect("trace.json");
    let v: Value = serde_json::from_str(&text).expect("trace.json is not JSON (regenerate it with the reference of 11 Oct 2026 or later)");
    let params = v["params"].clone();
    let k = params["keyed_rounds"].as_u64().expect("keyed_rounds") as usize;
    let rounds = params["rounds"].as_u64().expect("rounds") as usize;
    let lanes = params["lanes"].as_u64().unwrap_or(8) as usize;
    let mut out = Vec::new();
    for vec in v["vectors"].as_array().expect("vectors") {
        let tr = &vec["trace"];
        let sk = hex32(tr["test_sk"].as_str().expect("test_sk"));
        let seed = hex32(tr["S"].as_str().expect("S"));
        let rr = tr["rounds"].as_array().expect("rounds");
        assert_eq!(rr.len(), rounds, "the trace carries every round");
        let mut m = Vec::new();
        let mut s = Vec::new();
        for r in 0..rounds {
            if (r * k) % rounds >= k {
                continue;
            }
            let i = (r * k) / rounds;
            let mi: [u8; 32] = if i == 0 {
                seed
            } else {
                // the state at the round's entry is the state the previous round left: lane word 0 of every lane
                let regs = rr[r - 1]["regs"].as_array().expect("regs");
                let mut b = [0u8; 32];
                for l in 0..lanes {
                    let w = regs[l].as_array().expect("lane")[0].as_u64().expect("reg") as u32;
                    b[4 * l..4 * l + 4].copy_from_slice(&w.to_le_bytes());
                }
                b
            };
            let si = hex32(rr[r]["share"].as_str().expect("share inside the round's record"));
            assert_eq!(share(&sk, i as u32, &mi), si, "the trace's share must equal H(test_sk, i, m_i): the m_i rule or the regs layout is wrong");
            m.push(mi);
            s.push(si);
        }
        assert_eq!(m.len(), k);
        out.push(Keyed { header: vec["header"].as_str().unwrap().to_string(), nonce: vec["nonce"].as_str().unwrap().to_string(), sk, m, s });
    }
    (params, out)
}

fn row(name: &str, vector: usize, domain: u8, kv: &Keyed, pk: &[u8; 32], m: &[[u8; 32]], s: &[[u8; 32]], file: &str, bytes: &[u8], expect: &str, reason: &str) -> Row {
    Row {
        name: name.into(),
        vector,
        domain,
        k: m.len() as u32,
        header: kv.header.clone(),
        nonce: kv.nonce.clone(),
        pk: to_hex(pk),
        m: m.iter().map(|x| to_hex(x)).collect(),
        shares: s.iter().map(|x| to_hex(x)).collect(),
        receipt: file.into(),
        receipt_bytes: bytes.len(),
        receipt_sha256: sha256_hex(bytes),
        expect: expect.into(),
        reason: reason.into(),
        flip: None,
    }
}

fn gen(set_dir: &Path, out: &Path, no_claim: bool) {
    let (params, vecs) = read_input(set_dir);
    fs::create_dir_all(out.join("receipts")).unwrap();
    let ps = Risc0::for_domain(0);
    let mut rows = Vec::new();
    let mut receipts: Vec<Vec<u8>> = Vec::new();
    let mut prove_ms = Vec::new();
    let mut verify_ms = Vec::new();
    let mut sizes = Vec::new();
    let (mut uc, mut tc) = (0u64, 0u64);
    for (vi, kv) in vecs.iter().enumerate() {
        let pk = bond_pk(&kv.sk);
        let p = ps.prove(0, &kv.sk, &kv.m).expect("prove");
        assert_eq!(p.pk, pk);
        assert_eq!(p.shares, kv.s, "the guest's shares must equal the reference's");
        let t0 = Instant::now();
        ps.verify(0, &pk, &kv.m, &kv.s, &p.pi).expect("the valid row verifies");
        verify_ms.push(t0.elapsed().as_secs_f64() * 1e3);
        prove_ms.push(p.prove_ms);
        sizes.push(p.pi.len());
        uc = p.user_cycles;
        tc = p.total_cycles;
        let f = format!("receipts/valid-{vi}.bin");
        fs::write(out.join(&f), &p.pi).unwrap();
        rows.push(row("valid", vi, 0, kv, &pk, &kv.m, &kv.s, &f, &p.pi, "ok", "the sealer's proof under the set's test secret"));
        // wrong_sk: a proof under another secret; the block names pk, so the journal's pk differs
        let mut sk2 = kv.sk;
        sk2[0] ^= 1;
        let p2 = ps.prove(0, &sk2, &kv.m).expect("prove");
        let f2 = format!("receipts/wrong_sk-{vi}.bin");
        fs::write(out.join(&f2), &p2.pi).unwrap();
        rows.push(row("wrong_sk", vi, 0, kv, &pk, &kv.m, &kv.s, &f2, &p2.pi, "fail", "proof under another secret: its pk and shares are not the block's"));
        let mut s2 = kv.s.clone();
        s2[0][0] ^= 1;
        rows.push(row("wrong_share", vi, 0, kv, &pk, &kv.m, &s2, &f, &p.pi, "fail", "a carried share differs from the proof's"));
        let mut pk2 = pk;
        pk2[0] ^= 1;
        rows.push(row("wrong_pk", vi, 0, kv, &pk2, &kv.m, &kv.s, &f, &p.pi, "fail", "the header's BondKey is not the proof's pk"));
        // bad_receipt: the valid file with the byte at mid flipped at check (no stored copy: the 100-row sets would
        // carry 22 MB of copies); the row's sha is the flipped bytes'
        let mut bad = p.pi.clone();
        let mid = bad.len() / 2;
        bad[mid] ^= 1;
        let mut rb = row("bad_receipt", vi, 0, kv, &pk, &kv.m, &kv.s, &f, &bad, "fail", "one byte of the receipt flipped (the valid file, the byte at flip flipped at check)");
        rb.flip = Some(mid);
        rows.push(rb);
        rows.push(row("oversize", vi, 0, kv, &pk, &kv.m, &kv.s, "", &[], "fail", "a pi of PI_MAX_BYTES + 1 bytes, refused before parsing (synthesised at check)"));
        receipts.push(p.pi.clone());
    }
    // replayed: every vector's block with the next vector's proof (the last with the first)
    if vecs.len() >= 2 {
        for vi in 0..vecs.len() {
            let other = (vi + 1) % vecs.len();
            let kv = &vecs[vi];
            let pk = bond_pk(&kv.sk);
            let f = format!("receipts/valid-{other}.bin");
            rows.push(row("replayed", vi, 0, kv, &pk, &kv.m, &kv.s, &f, &receipts[other], "fail", "another block's proof: m_0 is that block's seed"));
        }
    }
    // the hostile claim: domain 1, K 1, the Groth16 wrap (the project's ruling of 10:25 UK: a claim rides in a transaction's
    // data, under the node's 128 KiB bound), m_0 the claim message with C = u64be(chain id), devnet5's 7315.
    // Left out under --no-claim (the set flagged): the Groth16 wrap needs docker and x86, which a prover pod may lack.
    let mut bench = vec![Bench { host: host_name(), threads: std::thread::available_parallelism().map(|n| n.get()).unwrap_or(0), kind: "composite-1 (domain 0)".into(), k: vecs[0].m.len() as u32, prove_ms, verify_ms, receipt_bytes: sizes, user_cycles: uc, total_cycles: tc }];
    if !no_claim {
        let kv = &vecs[0];
        let pk = bond_pk(&kv.sk);
        let chain_id: u64 = 7315;
        let to = [0x11u8; 20];
        let m0 = claim_message(chain_id, &pk, 0, &to);
        let psc = Risc0::for_domain(1);
        let pc = psc.prove(1, &kv.sk, &[m0]).expect("prove claim (Groth16: needs docker and x86 on this box; gen with --no-claim on a host without them)");
        let t0 = Instant::now();
        psc.verify(1, &pk, &[m0], &pc.shares, &pc.pi).expect("the claim row verifies");
        let claim_verify_ms = t0.elapsed().as_secs_f64() * 1e3;
        let fc = "receipts/claim-0.bin".to_string();
        fs::write(out.join(&fc), &pc.pi).unwrap();
        rows.push(row("claim", 0, 1, kv, &pk, &[m0], &pc.shares, &fc, &pc.pi, "ok", "the hostile claim or release: domain 1, K 1, the Groth16 wrap; m_0 = BLAKE3(tag(hostile) || u64be(7315) || pk || LE64(0) || to), to = 0x11 x 20"));
        rows.push(row("claim_as_block", 0, 0, kv, &pk, &[m0], &pc.shares, &fc, &pc.pi, "fail", "the claim's proof checked under domain 0 (the kind and the journal both refuse it)"));
        let mut to2 = to;
        to2[0] ^= 1;
        let m0b = claim_message(chain_id, &pk, 0, &to2);
        rows.push(row("claim_wrong_to", 0, 1, kv, &pk, &[m0b], &pc.shares, &fc, &pc.pi, "fail", "the claim's proof presented for another claimant address (the journal's m_0 differs)"));
        let pcc = ps.prove(1, &kv.sk, &[m0]).expect("prove claim as composite");
        let fcc = "receipts/claim_composite-0.bin".to_string();
        fs::write(out.join(&fcc), &pcc.pi).unwrap();
        rows.push(row("claim_composite", 0, 1, kv, &pk, &[m0], &pcc.shares, &fcc, &pcc.pi, "fail", "a claim carrying the block's composite receipt: over the claim's 2 KiB bound and the wrong kind"));
        bench.push(Bench { host: host_name(), threads: std::thread::available_parallelism().map(|n| n.get()).unwrap_or(0), kind: "groth16 (domain 1)".into(), k: 1, prove_ms: vec![pc.prove_ms], verify_ms: vec![claim_verify_ms], receipt_bytes: vec![pc.pi.len()], user_cycles: pc.user_cycles, total_cycles: pc.total_cycles });
    }
    let set = Set {
        candidate: "pi".into(),
        class: "pi".into(),
        generator: format!("pi-vectors (igneum-pi {}, risc0 3.0.6)", env!("CARGO_PKG_VERSION")),
        system: SYSTEM_RISC0,
        // a set carries the guest's id, which must be the pinned constant: a build off the docker-mode recipe prints
        // another id and must never write a set (the verifier pins PI_IMAGE_ID and would refuse every receipt)
        image_id: {
            assert_eq!(guest_image_id_hex(), image_id_hex(), "this build's guest is not the pinned PI_IMAGE_ID: no set is written under it");
            guest_image_id_hex()
        },
        pi_max_bytes: PI_MAX_BYTES,
        pi_claim_max_bytes: PI_CLAIM_MAX_BYTES,
        kind: if no_claim { "composite-1 (domain 0)".into() } else { "composite-1 (domain 0); groth16 (domain 1)".into() },
        params,
        source: set_dir.file_name().unwrap().to_string_lossy().into(),
        no_claim,
        rows,
        bench,
    };
    fs::write(out.join("pi.json"), serde_json::to_string_pretty(&set).unwrap()).unwrap();
    println!("wrote {} rows to {}", set.rows.len(), out.display());
}

/// The no-claim set derived from a full set by reference (no receipt copied): the claim rows dropped, no_claim set, every
/// receipt path rewritten to the source set's receipts (relative to the new dir), the claim bench dropped. The flag's
/// class for the outside review's count: as many block rows as the source, under the flag.
fn derive_no_claim(src: &Path, out: &Path) {
    let mut set: Set = serde_json::from_str(&fs::read_to_string(src.join("pi.json")).expect("pi.json")).expect("pi.json");
    assert!(!set.no_claim, "the source is a no-claim set already");
    fs::create_dir_all(out).unwrap();
    let src_abs = fs::canonicalize(src).unwrap();
    let out_abs = fs::canonicalize(out).unwrap();
    let rel = pathdiff(&out_abs, &src_abs);
    set.rows.retain(|r| !CLAIM_ROWS.contains(&r.name.as_str()));
    for r in set.rows.iter_mut() {
        if !r.receipt.is_empty() {
            r.receipt = format!("{}/{}", rel, r.receipt);
        }
    }
    set.bench.retain(|b| !b.kind.starts_with("groth16"));
    set.no_claim = true;
    set.kind = "composite-1 (domain 0)".into();
    set.source = format!("{} (derived by reference from {})", set.source, src.file_name().unwrap().to_string_lossy());
    fs::write(out.join("pi.json"), serde_json::to_string_pretty(&set).unwrap()).unwrap();
    println!("wrote {} rows to {} (no-claim, receipts by reference under {rel})", set.rows.len(), out.display());
}

/// `from` to `to` as a relative path of ".." steps and the remainder (both absolute, both under one root).
fn pathdiff(from: &Path, to: &Path) -> String {
    let f: Vec<_> = from.components().collect();
    let t: Vec<_> = to.components().collect();
    let common = f.iter().zip(t.iter()).take_while(|(a, b)| a == b).count();
    let mut parts: Vec<String> = vec!["..".into(); f.len() - common];
    parts.extend(t[common..].iter().map(|c| c.as_os_str().to_string_lossy().into_owned()));
    parts.join("/")
}

fn check(out: &Path) -> bool {
    let set: Set = serde_json::from_str(&fs::read_to_string(out.join("pi.json")).expect("pi.json")).expect("pi.json");
    assert_eq!(set.image_id, image_id_hex(), "the set's image id is not this build's guest");
    let mut ok = true;
    // the claim rows: all four present unless the set is flagged no_claim, in which case none (a flagged set with a
    // claim row, or an unflagged set missing one, is a set that lost or gained rows after its generator ran)
    let present: Vec<&str> = CLAIM_ROWS.iter().copied().filter(|n| set.rows.iter().any(|r| r.name == *n)).collect();
    if set.no_claim {
        let pass = present.is_empty() && set.rows.iter().all(|r| r.domain == 0);
        ok &= pass;
        println!("{} no-claim set (flagged by its generator): the claim rows absent, the block rows only{}", if pass { "PASS" } else { "FAIL" }, if pass { "" } else { " (a claim or domain 1 row is present)" });
    } else if present.len() != CLAIM_ROWS.len() {
        ok = false;
        println!("FAIL claim rows: {} of {} present and the set not flagged no_claim", present.len(), CLAIM_ROWS.len());
    }
    for r in &set.rows {
        let ps = Risc0::for_domain(r.domain);
        let mut pi: Vec<u8> = if r.name == "oversize" { vec![SYSTEM_RISC0; max_bytes_for(r.domain) + 1] } else { fs::read(out.join(&r.receipt)).expect("receipt") };
        if let Some(i) = r.flip {
            pi[i] ^= 1;
        }
        if !r.receipt.is_empty() {
            assert_eq!(sha256_hex(&pi), r.receipt_sha256, "receipt file changed: {}", r.name);
        }
        let pk = hex32(&r.pk);
        let m: Vec<[u8; 32]> = r.m.iter().map(|x| hex32(x)).collect();
        let s: Vec<[u8; 32]> = r.shares.iter().map(|x| hex32(x)).collect();
        let t0 = Instant::now();
        let res = ps.verify(r.domain, &pk, &m, &s, &pi);
        let ms = t0.elapsed().as_secs_f64() * 1e3;
        let verdict = if res.is_ok() { "ok" } else { "fail" };
        let pass = verdict == r.expect;
        ok &= pass;
        println!("{} {:<14} v{} {:>5.2} ms {}{}", if pass { "PASS" } else { "FAIL" }, r.name, r.vector, ms, verdict, res.err().map(|e| format!(" (code {}: {e})", e.code())).unwrap_or_default());
    }
    println!("{}", if ok { "ALL PASS" } else { "SOME FAIL" });
    ok
}

fn bench(k: usize, n: usize) {
    let sk = *blake3::hash(b"IgneumHash/R0A/bench-sk").as_bytes();
    let m: Vec<[u8; 32]> = (0..k).map(|i| *blake3::hash(&(i as u32).to_le_bytes()).as_bytes()).collect();
    // the block's two kinds at K under domain 0; the claim's Groth16 wrap at K 1 under domain 1 (it needs docker and x86)
    let kinds: Vec<(Kind, u8)> = if k == 1 { vec![(Kind::Composite, 0), (Kind::Succinct, 0), (Kind::Groth16, 1)] } else { vec![(Kind::Composite, 0), (Kind::Succinct, 0)] };
    for (kind, domain) in kinds {
        let ps = Risc0 { kind };
        let mut b = Bench { host: host_name(), threads: std::thread::available_parallelism().map(|n| n.get()).unwrap_or(0), kind: format!("{kind:?} (domain {domain})").to_lowercase(), k: k as u32, prove_ms: vec![], verify_ms: vec![], receipt_bytes: vec![], user_cycles: 0, total_cycles: 0 };
        for _ in 0..n {
            let p = ps.prove(domain, &sk, &m).expect("prove");
            let t0 = Instant::now();
            ps.verify(domain, &p.pk, &m, &p.shares, &p.pi).expect("verify");
            b.verify_ms.push(t0.elapsed().as_secs_f64() * 1e3);
            b.prove_ms.push(p.prove_ms);
            b.receipt_bytes.push(p.pi.len());
            b.user_cycles = p.user_cycles;
            b.total_cycles = p.total_cycles;
        }
        println!("{}", serde_json::to_string(&b).unwrap());
    }
}

fn main() {
    let a: Vec<String> = std::env::args().collect();
    match a.get(1).map(|s| s.as_str()) {
        Some("gen") if a.len() == 4 => gen(Path::new(&a[2]), Path::new(&a[3]), false),
        Some("gen") if a.len() == 5 && a[4] == "--no-claim" => gen(Path::new(&a[2]), Path::new(&a[3]), true),
        Some("derive-no-claim") if a.len() == 4 => derive_no_claim(Path::new(&a[2]), Path::new(&a[3])),
        Some("check") if a.len() == 3 => {
            if !check(Path::new(&a[2])) {
                std::process::exit(1)
            }
        }
        Some("bench") if a.len() >= 3 => bench(a[2].parse().expect("K"), a.get(3).map(|s| s.parse().expect("n")).unwrap_or(3)),
        // the guest this build embeds; the pinned constant beside it when they differ (then this build writes no set)
        Some("image-id") => {
            println!("{}", guest_image_id_hex());
            if guest_image_id_hex() != image_id_hex() {
                eprintln!("NOT THE PINNED PI_IMAGE_ID {} (a build off the docker-mode recipe)", image_id_hex());
                std::process::exit(3)
            }
        }
        _ => {
            eprintln!("usage: pi-vectors gen <keyed set dir> <out dir> [--no-claim] | check <out dir> | bench <K> [n] | image-id");
            std::process::exit(2)
        }
    }
}
