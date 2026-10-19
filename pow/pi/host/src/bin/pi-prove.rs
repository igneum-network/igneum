//! The sealer's prover CLI (the protocol team's ask 3, 11 Oct 2026) and its RESIDENT form (KEYED-SEAL-WORKER.md 2.1, the project's
//! order of 03:52 UK, 12 Oct 2026, on the node team's devnet9 print: one process per seal costs 485 to 4,185 ms on the
//! chain against tPiMs 250, the CUDA context and the prover's set-up paid every time; the resident loop pays them once).
//!
//!   pi-prove <domain> <sk-file> <m-file> <out.bin>          one proof, one process (the lab and the claim)
//!   pi-prove serve <sk-file> <socket-path> [--no-warm]      the resident loop (the seal, domain 0 and 1)
//!   pi-prove verify <domain> <pk-hex> <m-file> <shares-file> <pi.bin>   the verifier on a receipt (tests, the lab)
//!
//! The one-shot form: `sk-file` holds the 32-byte bond secret (raw bytes, or 64 hex characters); `m-file` holds the K
//! messages m_0..m_(K-1) as K x 32 raw bytes (what ignh_hash_keyed_m writes) or K lines of 64 hex characters. Writes
//! the wire form `u8(2) || receipt` to `out.bin` and prints one JSON line on stdout: {"pk", "shares" (K hex), "bytes",
//! "prove_ms", "user_cycles", "total_cycles", "image_id"}. Exit 0 on success; 2 on usage; 1 on a prover error; 3 if
//! the embedded guest is not the pinned PI_IMAGE_ID (checked before proving).
//!
//! The resident form, under the custody rule of GUARD-C-CLOSURE.md 1.9 (widened part (ii)): the secret is read ONCE
//! from the 0600 file named on the command line (the bytes never in an argument or the environment), held in one
//! buffer for the process's life and overwritten with zeros at `quit` and on every exit path this process controls;
//! the Unix socket at `socket-path` (created at mode 0600) is the process's only peer; each request is one JSON line,
//! `{"domain": 0, "m": ["<32 hex>", ...]}`, answered by one JSON line `{"pk", "shares", "bytes", "prove_ms",
//! "user_cycles", "total_cycles"}` followed by exactly `bytes` bytes of the receipt in the wire form, in memory, no
//! file; a request the prover refuses answers `{"refused": "<code>"}` and the loop stays up (codes: json, domain, k,
//! m, prove); `{"quit": true}` wipes the secret and exits 0. The first proof after start pays the prover's warm-up
//! (the CUDA context, the method image), so the loop proves one zero message at start by default (--no-warm skips it)
//! and every seal after it pays the proof alone. Built with the `prove` feature (the guest embedded) on a box with
//! rzup, or copied as the docker-mode artefact; the guest must print the pinned PI_IMAGE_ID (exit 3 otherwise).
use igneum_pi::{guest_image_id_hex, image_id_hex, pi_prove, pi_verify, K_MAX};
use std::fs;
use std::io::{BufRead, BufReader, Read, Write};
use std::os::unix::fs::PermissionsExt;
use std::os::unix::net::{UnixListener, UnixStream};
use std::path::Path;

fn read32s(path: &str, what: &str) -> Vec<[u8; 32]> {
    let raw = fs::read(path).unwrap_or_else(|e| {
        eprintln!("{what}: {path}: {e}");
        std::process::exit(2)
    });
    let text = String::from_utf8_lossy(&raw);
    let hex_lines: Vec<&str> = text.lines().map(|l| l.trim()).filter(|l| !l.is_empty()).collect();
    let as_hex = !hex_lines.is_empty() && hex_lines.iter().all(|l| l.len() == 64 && l.chars().all(|c| c.is_ascii_hexdigit()));
    let bytes: Vec<u8> = if as_hex { hex_lines.iter().flat_map(|l| hex::decode(l).unwrap()).collect() } else { raw };
    if bytes.is_empty() || bytes.len() % 32 != 0 {
        eprintln!("{what}: {path}: {} bytes, not a multiple of 32 (raw) and not 64-hex lines", bytes.len());
        std::process::exit(2)
    }
    bytes.chunks(32).map(|c| { let mut a = [0u8; 32]; a.copy_from_slice(c); a }).collect()
}

/// Overwrite a secret's bytes with zeros through volatile writes (the compiler keeps them).
fn wipe(sk: &mut [u8; 32]) {
    for b in sk.iter_mut() {
        unsafe { std::ptr::write_volatile(b, 0) };
    }
    std::sync::atomic::compiler_fence(std::sync::atomic::Ordering::SeqCst);
}

fn pinned_or_exit() {
    if guest_image_id_hex() != image_id_hex() {
        eprintln!("this build's guest {} is not the pinned PI_IMAGE_ID {}: a build off the docker-mode recipe proves nothing a node accepts", guest_image_id_hex(), image_id_hex());
        std::process::exit(3)
    }
}

/// F-55 (the security review, 13:1x UK, 12 October 2026, the custody pod): a crash of a process holding the bond secret
/// must not write it into a core file on the host's disk (a rented host's landlord reads the core_pattern's
/// directory), and the secret's page must not be swapped: the core limit zero, the process not dumpable (so no
/// ptrace and no /proc/<pid>/mem by another uid either), the secret's bytes locked in memory. Called before the
/// secret is read; an mlock refusal (RLIMIT_MEMLOCK 0 in a container) is said on stderr and the process goes on
/// with the core limit and the dumpable bit still set, which are the two that matter for the core file.
fn guard_secret_process() {
    unsafe {
        let lim = libc::rlimit { rlim_cur: 0, rlim_max: 0 };
        if libc::setrlimit(libc::RLIMIT_CORE, &lim) != 0 {
            eprintln!("guard: setrlimit(RLIMIT_CORE, 0) failed: {}", std::io::Error::last_os_error());
            std::process::exit(3)
        }
        #[cfg(target_os = "linux")]
        if libc::prctl(libc::PR_SET_DUMPABLE, 0, 0, 0, 0) != 0 {
            eprintln!("guard: prctl(PR_SET_DUMPABLE, 0) failed: {}", std::io::Error::last_os_error());
            std::process::exit(3)
        }
    }
}

/// lock_secret pins the secret's bytes in memory (never swapped); a refusal is said, not fatal.
fn lock_secret(sk: &[u8; 32]) {
    unsafe {
        if libc::mlock(sk.as_ptr() as *const libc::c_void, sk.len()) != 0 {
            eprintln!("guard: mlock of the secret refused ({}); the core limit and the dumpable bit still hold", std::io::Error::last_os_error());
        }
    }
}

fn read_secret(path: &str) -> [u8; 32] {
    guard_secret_process();
    let v = read32s(path, "sk-file");
    if v.len() != 1 {
        eprintln!("sk-file must hold exactly 32 bytes");
        std::process::exit(2)
    }
    let sk = v[0];
    lock_secret(&sk);
    sk
}

/// One request of the resident loop: the JSON line parsed by hand (the keys domain, m, quit), no serde on the
/// secret's path. Returns Ok(None) on quit.
fn serve_request(sk: &[u8; 32], line: &str, out: &mut UnixStream) -> std::io::Result<Option<()>> {
    let v: serde_json::Value = match serde_json::from_str(line) {
        Ok(v) => v,
        Err(_) => {
            out.write_all(b"{\"refused\": \"json\"}\n")?;
            return Ok(Some(()));
        }
    };
    if v.get("quit").and_then(|q| q.as_bool()).unwrap_or(false) {
        out.write_all(b"{\"quit\": true}\n")?;
        return Ok(None);
    }
    let domain = v.get("domain").and_then(|d| d.as_u64()).unwrap_or(255);
    if domain > 1 {
        out.write_all(b"{\"refused\": \"domain\"}\n")?;
        return Ok(Some(()));
    }
    let m: Vec<[u8; 32]> = match v.get("m").and_then(|m| m.as_array()) {
        Some(arr) => {
            let mut out_m = Vec::with_capacity(arr.len());
            for x in arr {
                match x.as_str().and_then(|s| hex::decode(s).ok()).filter(|b| b.len() == 32) {
                    Some(b) => { let mut a = [0u8; 32]; a.copy_from_slice(&b); out_m.push(a) }
                    None => { out.write_all(b"{\"refused\": \"m\"}\n")?; return Ok(Some(())); }
                }
            }
            out_m
        }
        None => { out.write_all(b"{\"refused\": \"m\"}\n")?; return Ok(Some(())); }
    };
    if m.is_empty() || m.len() > K_MAX || (domain == 1 && m.len() != 1) {
        out.write_all(b"{\"refused\": \"k\"}\n")?;
        return Ok(Some(()));
    }
    match pi_prove(domain as u8, sk, &m) {
        Ok(p) => {
            let shares: Vec<String> = p.shares.iter().map(hex::encode).collect();
            let head = serde_json::json!({"pk": hex::encode(p.pk), "shares": shares, "bytes": p.pi.len(), "prove_ms": p.prove_ms, "user_cycles": p.user_cycles, "total_cycles": p.total_cycles, "domain": domain, "k": m.len()});
            out.write_all(format!("{head}\n").as_bytes())?;
            out.write_all(&p.pi)?;
            out.flush()?;
        }
        Err(e) => {
            eprintln!("prove refused: {e}");
            out.write_all(b"{\"refused\": \"prove\"}\n")?;
        }
    }
    Ok(Some(()))
}

fn serve(sk_path: &str, sock_path: &str, warm: bool) -> ! {
    pinned_or_exit();
    let mut sk = read_secret(sk_path);
    if Path::new(sock_path).exists() {
        let _ = fs::remove_file(sock_path);
    }
    let listener = match UnixListener::bind(sock_path) {
        Ok(l) => l,
        Err(e) => { eprintln!("bind {sock_path}: {e}"); wipe(&mut sk); std::process::exit(1) }
    };
    if let Err(e) = fs::set_permissions(sock_path, fs::Permissions::from_mode(0o600)) {
        eprintln!("chmod {sock_path}: {e}"); wipe(&mut sk); std::process::exit(1)
    }
    if warm {
        // the prover's warm-up (the CUDA context, the method image) paid here, once, on a zero message
        let t0 = std::time::Instant::now();
        match pi_prove(0, &sk, &[[0u8; 32]]) {
            Ok(_) => eprintln!("warm: the first proof in {:.0} ms", t0.elapsed().as_secs_f64() * 1e3),
            Err(e) => { eprintln!("warm-up proof refused: {e}"); wipe(&mut sk); std::process::exit(1) }
        }
    }
    eprintln!("serve: {sock_path} (mode 0600), image id {}", image_id_hex());
    for conn in listener.incoming() {
        let mut stream = match conn { Ok(s) => s, Err(e) => { eprintln!("accept: {e}"); continue } };
        let reader = match stream.try_clone() { Ok(s) => BufReader::new(s), Err(e) => { eprintln!("clone: {e}"); continue } };
        for line in reader.lines() {
            let line = match line { Ok(l) => l, Err(_) => break };
            if line.trim().is_empty() { continue; }
            match serve_request(&sk, &line, &mut stream) {
                Ok(Some(())) => {}
                Ok(None) => { wipe(&mut sk); let _ = fs::remove_file(sock_path); std::process::exit(0) }
                Err(e) => { eprintln!("peer: {e}"); break }
            }
        }
    }
    wipe(&mut sk);
    std::process::exit(0)
}

fn verify(a: &[String]) -> ! {
    // pi-prove verify <domain> <pk-hex> <m-file> <shares-file> <pi.bin>
    let domain: u8 = a[2].parse().unwrap_or(255);
    let pk = { let b = hex::decode(&a[3]).unwrap_or_default(); if b.len() != 32 { eprintln!("pk: 64 hex"); std::process::exit(2) } let mut x = [0u8; 32]; x.copy_from_slice(&b); x };
    let m = read32s(&a[4], "m-file");
    let s = read32s(&a[5], "shares-file");
    let mut pi = Vec::new();
    if let Err(e) = fs::File::open(&a[6]).and_then(|mut f| f.read_to_end(&mut pi)) { eprintln!("{}: {e}", a[6]); std::process::exit(2) }
    match pi_verify(domain, &pk, &m, &s, &pi) {
        Ok(()) => { println!("{{\"verify\": \"ok\", \"bytes\": {}}}", pi.len()); std::process::exit(0) }
        Err(e) => { println!("{{\"verify\": \"fail\", \"code\": {}, \"reason\": \"{e}\"}}", e.code()); std::process::exit(1) }
    }
}

fn main() {
    let a: Vec<String> = std::env::args().collect();
    if a.len() >= 4 && a[1] == "serve" {
        let warm = !a.iter().any(|x| x == "--no-warm");
        serve(&a[2], &a[3], warm)
    }
    if a.len() == 7 && a[1] == "verify" {
        verify(&a)
    }
    if a.len() != 5 {
        eprintln!("usage: pi-prove <domain 0|1> <sk-file> <m-file> <out.bin> | serve <sk-file> <socket-path> [--no-warm] | verify <domain> <pk-hex> <m-file> <shares-file> <pi.bin>");
        std::process::exit(2)
    }
    let domain: u8 = a[1].parse().unwrap_or(255);
    if domain > 1 {
        eprintln!("domain must be 0 (the seal) or 1 (the claim)");
        std::process::exit(2)
    }
    pinned_or_exit();
    let mut sk = read_secret(&a[2]);
    let m = read32s(&a[3], "m-file");
    if m.len() > K_MAX || (domain == 1 && m.len() != 1) {
        eprintln!("K {} out of range (1 to {K_MAX}; the claim's K is 1)", m.len());
        wipe(&mut sk);
        std::process::exit(2)
    }
    let r = pi_prove(domain, &sk, &m);
    wipe(&mut sk);
    match r {
        Ok(p) => {
            if let Err(e) = fs::write(&a[4], &p.pi) {
                eprintln!("{}: {e}", a[4]);
                std::process::exit(1)
            }
            let shares: Vec<String> = p.shares.iter().map(hex::encode).collect();
            println!("{}", serde_json::json!({"pk": hex::encode(p.pk), "shares": shares, "bytes": p.pi.len(), "prove_ms": p.prove_ms, "user_cycles": p.user_cycles, "total_cycles": p.total_cycles, "image_id": image_id_hex(), "domain": domain, "k": m.len()}));
        }
        Err(e) => {
            eprintln!("prove: {e}");
            std::process::exit(1)
        }
    }
}
