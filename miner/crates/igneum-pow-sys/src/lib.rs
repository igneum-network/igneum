//! The C ABI of `pow/ref/include/ignpow.h` (ABI version 0), declared by hand and kept in step with the header.
//! Every value crossing the boundary is a fixed-width integer or a byte array; contexts are opaque.

#![allow(non_camel_case_types)]

use std::os::raw::{c_char, c_int, c_void};

pub const IGNPOW_ABI_VERSION: c_int = 0;

pub const IGNPOW_OK: c_int = 0;
pub const IGNPOW_E_ALGO: c_int = 1;
pub const IGNPOW_E_PARAMS: c_int = 2;
pub const IGNPOW_E_HEIGHT: c_int = 3;
pub const IGNPOW_E_EPOCH: c_int = 4;
pub const IGNPOW_E_BOUNDARY: c_int = 5;
pub const IGNPOW_E_MIX: c_int = 6;
pub const IGNPOW_E_ARG: c_int = 7;
pub const IGNPOW_E_MEMORY: c_int = 8;
pub const IGNPOW_E_VECTORS: c_int = 9;

#[repr(C)]
pub struct ignpow_ctx {
    _private: [u8; 0],
}

extern "C" {
    pub fn ignpow_algo_count() -> usize;
    pub fn ignpow_algo_id(i: usize) -> *const c_char;
    pub fn ignpow_epoch_of(algo: *const c_char, params_json: *const c_char, height: u64, status: *mut c_int) -> u64;
    pub fn ignpow_cache_bytes(algo: *const c_char, params_json: *const c_char, height: u64, status: *mut c_int) -> u64;
    pub fn ignpow_dataset_bytes(algo: *const c_char, params_json: *const c_char, height: u64, status: *mut c_int) -> u64;
    pub fn ignpow_ctx_create(algo: *const c_char, params_json: *const c_char, height: u64, full: c_int, status: *mut c_int) -> *mut ignpow_ctx;
    pub fn ignpow_ctx_free(ctx: *mut ignpow_ctx);
    pub fn ignpow_ctx_epoch(ctx: *const ignpow_ctx) -> u64;
    pub fn ignpow_ctx_algo(ctx: *const ignpow_ctx) -> *const c_char;
    pub fn ignpow_hash(ctx: *const ignpow_ctx, height: u64, header: *const u8, nonce: u64, mix_out: *mut u8, hash_out: *mut u8) -> c_int;
    pub fn ignpow_verify(ctx: *const ignpow_ctx, height: u64, header: *const u8, nonce: u64, mix: *const u8, boundary: *const u8) -> c_int;
    pub fn ignpow_ctx_light_cache(ctx: *const ignpow_ctx, data_out: *mut *const c_void, bytes_out: *mut u64, item_bytes_out: *mut u32) -> c_int;
    pub fn ignpow_vectors_check(algo: *const c_char, count_out: *mut usize) -> c_int;
    /// SPEC-R0A-v0.md section 0.9 (bit 10, the bond-keyed rounds): the prover's hash under the 32-byte secret `sk`
    /// (the document's keyed_rounds shares of 32 bytes each written to `shares`, at most ROUNDS x 32 bytes), or
    /// with `sk` null the verifier's replay from the shares given. A keyed document refuses the keyless
    /// `ignpow_hash` (IGNPOW_E_ARG).
    pub fn ignh_hash_keyed(ctx: *const ignpow_ctx, height: u64, header: *const u8, nonce: u64, sk: *const u8, shares: *mut u8, mix_out: *mut u8, hash_out: *mut u8) -> c_int;
    /// The vector sets' test secret, BLAKE3("IgneumHash/R0A/test-sk" || header): public by construction.
    pub fn ignh_test_sk(header: *const u8, sk_out: *mut u8);
    pub fn ignpow_build_string() -> *const c_char;
}

/// The reason for a status, for logs.
pub fn status_name(s: c_int) -> &'static str {
    match s {
        IGNPOW_OK => "ok",
        IGNPOW_E_ALGO => "unknown candidate id",
        IGNPOW_E_PARAMS => "params document rejected",
        IGNPOW_E_HEIGHT => "height outside the candidate's schedule",
        IGNPOW_E_EPOCH => "context prepared for another epoch",
        IGNPOW_E_BOUNDARY => "final hash above the boundary",
        IGNPOW_E_MIX => "mix hash does not match",
        IGNPOW_E_ARG => "null pointer or wrong length",
        IGNPOW_E_MEMORY => "context allocation refused",
        IGNPOW_E_VECTORS => "embedded self-test failed",
        _ => "unknown status",
    }
}
