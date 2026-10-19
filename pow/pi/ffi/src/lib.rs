//! The node's `pi` verifier as a C ABI (SPEC-R0A-v0.md section 0.9.1; PI-NOTES.md section 5; the header
//! `include/ignpi.h`): the Go node calls `ignpi_verify` behind cgo with the wire form `pi = u8(2) || receipt`, the
//! header's BondKey, the replay's `m_i` and the carried shares, and reads ONE refusal code (0 OK, 1 to 7 the checks in
//! order, 8 a panic caught at the boundary). The image id is the pinned constant `igneum_pi::PI_IMAGE_ID`; the
//! per-domain kind and bound are igneum-pi's; nothing here decides, it only carries. Built with plain cargo on any box
//! (`cargo build --release -p igneum-pi-ffi`): the verifier links without the guest, rzup or docker.
//!
//! Thread safety: every entry point is pure over its arguments (no global state); the node may call from any goroutine.
use igneum_pi::{image_id_hex, max_bytes_for, pi_verify, K_MAX, PI_IMAGE_ID, SYSTEM_RISC0};
use std::os::raw::c_char;
use std::panic::{catch_unwind, AssertUnwindSafe};

/// The ABI version this library speaks (the header's `IGNPI_ABI_VERSION`); a change in any signature or code bumps it.
pub const ABI_VERSION: u32 = 1;

pub const OK: i32 = 0;
pub const E_ARG: i32 = 1;
pub const E_BOUND: i32 = 2;
pub const E_SYSTEM: i32 = 3;
pub const E_DECODE: i32 = 4;
pub const E_KIND: i32 = 5;
pub const E_PROOF: i32 = 6;
pub const E_JOURNAL: i32 = 7;
pub const E_PANIC: i32 = 8;

/// The ABI version.
#[no_mangle]
pub extern "C" fn ignpi_abi_version() -> u32 {
    ABI_VERSION
}

/// The proof system identifier the wire form carries (2).
#[no_mangle]
pub extern "C" fn ignpi_system() -> u8 {
    SYSTEM_RISC0
}

/// `K_MAX` (64).
#[no_mangle]
pub extern "C" fn ignpi_k_max() -> u32 {
    K_MAX as u32
}

/// The wire bound of a domain's `pi` (294912 for the block, 2048 for the claim; 0 for a domain not 0 or 1).
#[no_mangle]
pub extern "C" fn ignpi_max_bytes(domain: u8) -> usize {
    if domain > 1 {
        return 0;
    }
    max_bytes_for(domain)
}

/// `PI_IMAGE_ID` into `out` (32 bytes, the digest's canonical byte order; `image_id_hex()` is its hex).
///
/// # Safety
/// `out` points to 32 writable bytes.
#[no_mangle]
pub unsafe extern "C" fn ignpi_image_id(out: *mut u8) {
    if out.is_null() {
        return;
    }
    std::ptr::copy_nonoverlapping(PI_IMAGE_ID.as_ptr(), out, 32);
}

/// Verify `pi` (`pi_len` bytes) under `domain` (0 the seal, 1 the claim) against `pk` (32 bytes), `m` (`k` x 32 bytes,
/// the m_i in order) and `s` (`k` x 32 bytes, the carried shares). Returns 0, or the refusal code of the first check
/// that fails, in this order: 1 arguments (domain, k, a null pointer), 2 the domain's bound (before parsing), 3 the
/// identifier byte, 4 the receipt's decoding, 5 the receipt's kind for the domain, 6 the STARK or SNARK against
/// `PI_IMAGE_ID`, 7 the journal (pk, an m_i or a share is not the proof's); 8 a panic inside the verifier, caught here.
///
/// # Safety
/// The pointers are readable for the lengths stated; a null `pi` with `pi_len` 0 is a valid empty proof (refused, 3).
#[no_mangle]
pub unsafe extern "C" fn ignpi_verify(domain: u8, k: u32, pk: *const u8, m: *const u8, s: *const u8, pi: *const u8, pi_len: usize) -> i32 {
    if domain > 1 || k == 0 || k as usize > K_MAX || pk.is_null() || m.is_null() || s.is_null() || (pi.is_null() && pi_len != 0) {
        return E_ARG;
    }
    let k = k as usize;
    let r = catch_unwind(AssertUnwindSafe(|| {
        let pk: &[u8; 32] = &*(pk as *const [u8; 32]);
        let m: Vec<[u8; 32]> = std::slice::from_raw_parts(m as *const [u8; 32], k).to_vec();
        let s: Vec<[u8; 32]> = std::slice::from_raw_parts(s as *const [u8; 32], k).to_vec();
        let pi: &[u8] = if pi_len == 0 { &[] } else { std::slice::from_raw_parts(pi, pi_len) };
        match pi_verify(domain, pk, &m, &s, pi) {
            Ok(()) => OK,
            Err(e) => e.code(),
        }
    }));
    r.unwrap_or(E_PANIC)
}

/// The name of a code, a static NUL-terminated string (for the node's logs).
#[no_mangle]
pub extern "C" fn ignpi_code_name(code: i32) -> *const c_char {
    let s: &'static [u8] = match code {
        OK => b"ok\0",
        E_ARG => b"arg: domain not 0 or 1, K 0 or over 64, or a null pointer\0",
        E_BOUND => b"bound: pi over the domain's bound\0",
        E_SYSTEM => b"system: pi empty or its identifier byte not 2\0",
        E_DECODE => b"decode: the receipt does not decode\0",
        E_KIND => b"kind: the receipt's kind is not the domain's\0",
        E_PROOF => b"proof: the STARK or SNARK does not verify against PI_IMAGE_ID\0",
        E_JOURNAL => b"journal: pk, an m_i or a share is not the proof's\0",
        E_PANIC => b"panic: the verifier panicked (caught)\0",
        _ => b"unknown code\0",
    };
    s.as_ptr() as *const c_char
}

/// `PI_IMAGE_ID` as hex (Rust callers; the C side reads the bytes).
pub fn image_id() -> String {
    image_id_hex()
}
