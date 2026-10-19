//! CPU verification of every share a worker reports, in the shape of `ignpow_verify` (pow/ref/include/ignpow.h):
//! height, header, nonce, mix, hash. With the `ignpow` feature the candidates are verified through the shared
//! reference (GPU engineer A's library, one light context per epoch, the embedded vectors checked at start);
//! the stub verifier matches the stub worker in every build.

use igneum_worker_proto::stubhash;

#[derive(Debug, thiserror::Error)]
pub enum VerifyError {
    #[error("algorithm {0} is not known to this verifier")]
    UnknownAlgo(String),
    #[error("malformed field {0}")]
    Malformed(&'static str),
    #[error("the reference's self-test for {0} failed at start; nothing is verified with it")]
    Vectors(String),
    #[error("the reference refused: {0}")]
    Reference(String),
}

pub trait Verifier: Send + Sync {
    fn algo(&self) -> &str;
    /// The epoch a height belongs to under this algorithm and parameter set (the tag on every job and dataset).
    fn epoch_at(&self, height: u64) -> Option<u64>;
    /// `true` when the worker's claimed `mix` and `hash` are what the reference computes for this nonce at this
    /// height. The verifier derives the epoch from the height itself (the caller knows nothing about it).
    fn verify(&self, height: u64, header: &[u8; 32], nonce: u64, mix: &[u8], hash: &[u8; 32]) -> Result<bool, VerifyError>;
    /// The keyed document's check (SPEC-R0A-v0.md section 0.9, bit 10): the prover's hash under the secret,
    /// compared with the worker's; on agreement the S shares (S x 32 bytes) the submission carries. A verifier
    /// without a keyed path answers an error, never a pass.
    fn verify_keyed(&self, _height: u64, _header: &[u8; 32], _nonce: u64, _mix: &[u8], _hash: &[u8; 32], _sk: &[u8; 32]) -> Result<Option<Vec<u8>>, VerifyError> {
        Err(VerifyError::Reference("this verifier has no keyed path".into()))
    }
    /// The document's keyed rounds S, when it is a keyed document.
    fn keyed_rounds(&self) -> Option<u32> {
        None
    }
    /// Build the context for `height` ahead of the first result (the full dataset in host RAM where it fits, so
    /// a check costs microseconds instead of a light recomputation of a second on a slow host); a no-op for
    /// verifiers without one. Returns a line for the log.
    fn warm(&self, _height: u64) -> Option<String> {
        None
    }
}

/// Matches `igneum-worker-stub`. Not consensus.
pub struct StubVerifier;

impl Verifier for StubVerifier {
    fn algo(&self) -> &str {
        stubhash::STUB_ALGO
    }
    fn epoch_at(&self, height: u64) -> Option<u64> {
        Some(stubhash::epoch_of(height))
    }
    fn verify(&self, height: u64, header: &[u8; 32], nonce: u64, mix: &[u8], hash: &[u8; 32]) -> Result<bool, VerifyError> {
        if mix.len() != 32 {
            return Err(VerifyError::Malformed("mix"));
        }
        let (m, h) = stubhash::stub_hash_at(height, header, nonce);
        Ok(m[..] == *mix && h == *hash)
    }
}

#[cfg(feature = "ignpow")]
pub mod reference {
    //! The shared reference through its C ABI. One light context per epoch, created on first use and kept;
    //! the vectors are checked once when the verifier is made.
    use super::{Verifier, VerifyError};
    use igneum_pow_sys as sys;
    use std::collections::HashMap;
    use std::ffi::{CStr, CString};
    use std::sync::Mutex;

    struct Ctx(*mut sys::ignpow_ctx);
    // the reference documents contexts as read-only after creation; every call here is under one mutex
    unsafe impl Send for Ctx {}
    impl Drop for Ctx {
        fn drop(&mut self) {
            unsafe { sys::ignpow_ctx_free(self.0) }
        }
    }

    pub struct ReferenceVerifier {
        algo: String,
        algo_c: CString,
        /// The candidate's params document (the job's, passed through unchanged); None is the candidate's
        /// defaults where it has them. IgneumHash has none: its document is mandatory (SPEC-R0A-v0.md 0.1).
        params_c: Option<CString>,
        contexts: Mutex<HashMap<u64, Ctx>>,
        /// Epochs whose context is the full dataset (the fast check); the others are light.
        full: Mutex<HashMap<u64, bool>>,
        /// An epoch whose full context is being built right now (the light one serves meanwhile).
        building: Mutex<Option<u64>>,
        /// The document's keyed rounds S (SPEC 0.9, bit 10), None for an unkeyed document: a keyed document's
        /// results are checked by the keyed path alone (the keyless entry refuses them).
        keyed_rounds: Option<u32>,
    }

    /// Contexts held at once: the current epoch's and the next's. A retired epoch's dataset is released the
    /// moment a newer one is held; nothing of the supervisor's keeps a retired dataset in memory.
    pub const MAX_CONTEXTS: usize = 2;

    /// The host's available memory, bytes (Linux: MemAvailable; elsewhere: unknown, which means light).
    fn mem_available() -> Option<u64> {
        let text = std::fs::read_to_string("/proc/meminfo").ok()?;
        for line in text.lines() {
            if let Some(rest) = line.strip_prefix("MemAvailable:") {
                let kb: u64 = rest.trim().trim_end_matches("kB").trim().parse().ok()?;
                return Some(kb * 1024);
            }
        }
        None
    }

    /// The candidate ids this build of the reference carries.
    pub fn algos() -> Vec<String> {
        let mut v = Vec::new();
        unsafe {
            for i in 0..sys::ignpow_algo_count() {
                let p = sys::ignpow_algo_id(i);
                if !p.is_null() {
                    v.push(CStr::from_ptr(p).to_string_lossy().into_owned());
                }
            }
        }
        v
    }

    pub fn build_string() -> String {
        unsafe {
            let p = sys::ignpow_build_string();
            if p.is_null() {
                String::new()
            } else {
                CStr::from_ptr(p).to_string_lossy().into_owned()
            }
        }
    }

    impl ReferenceVerifier {
        /// Runs the embedded vectors for the candidate first; a failure is an error, not a warning.
        pub fn new(algo: &str) -> Result<ReferenceVerifier, VerifyError> {
            Self::with_params(algo, None)
        }

        /// The same with the candidate's params document (a JSON object as the job carries it).
        pub fn with_params(algo: &str, params: Option<&str>) -> Result<ReferenceVerifier, VerifyError> {
            if !algos().iter().any(|a| a == algo) {
                return Err(VerifyError::UnknownAlgo(algo.to_string()));
            }
            let algo_c = CString::new(algo).map_err(|_| VerifyError::Malformed("algo"))?;
            let mut count = 0usize;
            let st = unsafe { sys::ignpow_vectors_check(algo_c.as_ptr(), &mut count) };
            if st != sys::IGNPOW_OK {
                return Err(VerifyError::Vectors(algo.to_string()));
            }
            log::info!("reference vectors for {algo}: {count} checked; {}", build_string());
            let params_c = match params {
                Some(p) => Some(CString::new(p).map_err(|_| VerifyError::Malformed("params"))?),
                None => None,
            };
            let keyed_rounds = params.and_then(|p| serde_json::from_str::<serde_json::Value>(p).ok()).and_then(|d| crate::bond::keyed_rounds(&d));
            let v = ReferenceVerifier { algo: algo.to_string(), algo_c, params_c, contexts: Mutex::new(HashMap::new()), full: Mutex::new(HashMap::new()), building: Mutex::new(None), keyed_rounds };
            // the reference judges the document now, not at the first share
            let mut st = 0;
            unsafe { sys::ignpow_epoch_of(v.algo_c.as_ptr(), v.params_ptr(), 0, &mut st) };
            if st != sys::IGNPOW_OK {
                return Err(VerifyError::Reference(sys::status_name(st).into()));
            }
            Ok(v)
        }

        fn params_ptr(&self) -> *const std::os::raw::c_char {
            self.params_c.as_ref().map(|c| c.as_ptr()).unwrap_or(std::ptr::null())
        }

        /// How many epoch contexts are held now (for the tests and the state).
        pub fn contexts_held(&self) -> usize {
            self.contexts.lock().map(|m| m.len()).unwrap_or(0)
        }

        pub fn holds_epoch(&self, epoch: u64) -> bool {
            self.contexts.lock().map(|m| m.contains_key(&epoch)).unwrap_or(false)
        }

        /// The dataset's size at `height` under this document.
        fn dataset_bytes(&self, height: u64) -> u64 {
            let mut st = 0;
            unsafe { sys::ignpow_dataset_bytes(self.algo_c.as_ptr(), self.params_ptr(), height, &mut st) }
        }

        /// Build the full context for the epoch of `height` and swap it in, when the host has the memory (the
        /// dataset plus half again free); the build runs on the caller's thread, outside the map lock, so checks
        /// on the light context continue meanwhile.
        fn build_full(&self, height: u64) -> Option<String> {
            let epoch = self.epoch_of(height).ok()?;
            if self.full.lock().ok()?.get(&epoch).copied().unwrap_or(false) {
                return None;
            }
            {
                let mut b = self.building.lock().ok()?;
                if *b == Some(epoch) {
                    return None;
                }
                *b = Some(epoch);
            }
            let need = self.dataset_bytes(height);
            let avail = mem_available();
            let fits = avail.map_or(false, |a| need > 0 && a > need + need / 2);
            if !fits {
                if let Ok(mut b) = self.building.lock() {
                    *b = None;
                }
                return Some(format!("the verifier keeps the light context for epoch {epoch}: the full dataset needs {:.2} GB and the host has {} available", need as f64 / 1e9, avail.map_or("an unknown amount".to_string(), |a| format!("{:.2} GB", a as f64 / 1e9))));
            }
            let t0 = std::time::Instant::now();
            let mut st = 0;
            let p = unsafe { sys::ignpow_ctx_create(self.algo_c.as_ptr(), self.params_ptr(), height, 1, &mut st) };
            let line = if p.is_null() || st != sys::IGNPOW_OK {
                format!("the verifier's full context for epoch {epoch} could not be built ({}); the light one stays", sys::status_name(st))
            } else {
                if let Ok(mut map) = self.contexts.lock() {
                    map.insert(epoch, Ctx(p));
                    // a full context is a dataset in host RAM: the current epoch's and the next's at most, the
                    // retired one released the moment a newer one is held (the memory seat's finding on the
                    // Metal swap path, 11 October 2026: nothing of the supervisor's keeps a retired dataset)
                    while map.len() > MAX_CONTEXTS {
                        let oldest = *map.keys().min().unwrap_or(&epoch);
                        if oldest == epoch {
                            break;
                        }
                        map.remove(&oldest);
                        if let Ok(mut f) = self.full.lock() {
                            f.remove(&oldest);
                        }
                    }
                }
                if let Ok(mut f) = self.full.lock() {
                    f.insert(epoch, true);
                }
                format!("the verifier holds the full dataset for epoch {epoch} ({:.2} GB, built in {:.1} s): a check now costs microseconds", need as f64 / 1e9, t0.elapsed().as_secs_f64())
            };
            if let Ok(mut b) = self.building.lock() {
                *b = None;
            }
            Some(line)
        }

        pub fn epoch_of(&self, height: u64) -> Result<u64, VerifyError> {
            let mut st = 0;
            let e = unsafe { sys::ignpow_epoch_of(self.algo_c.as_ptr(), self.params_ptr(), height, &mut st) };
            if st != sys::IGNPOW_OK {
                return Err(VerifyError::Reference(sys::status_name(st).into()));
            }
            Ok(e)
        }
    }

    impl Verifier for ReferenceVerifier {
        fn algo(&self) -> &str {
            &self.algo
        }
        fn epoch_at(&self, height: u64) -> Option<u64> {
            self.epoch_of(height).ok()
        }
        fn warm(&self, height: u64) -> Option<String> {
            self.build_full(height)
        }
        fn verify(&self, height: u64, header: &[u8; 32], nonce: u64, mix: &[u8], hash: &[u8; 32]) -> Result<bool, VerifyError> {
            if mix.len() != 32 {
                return Err(VerifyError::Malformed("mix"));
            }
            if self.keyed_rounds.is_some() {
                // the keyless entry refuses a keyed document (IGNPOW_E_ARG): the caller must hold the secret
                return Err(VerifyError::Reference("a keyed document is checked by the keyed path".into()));
            }
            let epoch = self.epoch_of(height)?;
            let map = self.context_map(epoch, height)?;
            let ctx = map.get(&epoch).expect("context");
            let mut mix_out = [0u8; 32];
            let mut hash_out = [0u8; 32];
            let st = unsafe { sys::ignpow_hash(ctx.0, height, header.as_ptr(), nonce, mix_out.as_mut_ptr(), hash_out.as_mut_ptr()) };
            if st != sys::IGNPOW_OK {
                return Err(VerifyError::Reference(sys::status_name(st).into()));
            }
            Ok(mix_out[..] == *mix && hash_out == *hash)
        }
        fn verify_keyed(&self, height: u64, header: &[u8; 32], nonce: u64, mix: &[u8], hash: &[u8; 32], sk: &[u8; 32]) -> Result<Option<Vec<u8>>, VerifyError> {
            if mix.len() != 32 {
                return Err(VerifyError::Malformed("mix"));
            }
            let Some(s) = self.keyed_rounds else {
                return Err(VerifyError::Reference("an unkeyed document has no shares to prove".into()));
            };
            let epoch = self.epoch_of(height)?;
            let map = self.context_map(epoch, height)?;
            let ctx = map.get(&epoch).expect("context");
            // the reference writes at most ROUNDS x 32 bytes of shares; the document's S of them are the proof
            let mut shares = vec![0u8; 64 * 32];
            let mut mix_out = [0u8; 32];
            let mut hash_out = [0u8; 32];
            let st = unsafe { sys::ignh_hash_keyed(ctx.0, height, header.as_ptr(), nonce, sk.as_ptr(), shares.as_mut_ptr(), mix_out.as_mut_ptr(), hash_out.as_mut_ptr()) };
            if st != sys::IGNPOW_OK {
                return Err(VerifyError::Reference(sys::status_name(st).into()));
            }
            if mix_out[..] != *mix || hash_out != *hash {
                return Ok(None);
            }
            shares.truncate(s as usize * 32);
            Ok(Some(shares))
        }
        fn keyed_rounds(&self) -> Option<u32> {
            self.keyed_rounds
        }
    }

    impl ReferenceVerifier {
        /// The context map with the epoch's context in it (a light one created on first use, the full one if
        /// `warm` built it), locked for the caller's check.
        fn context_map(&self, epoch: u64, height: u64) -> Result<std::sync::MutexGuard<'_, HashMap<u64, Ctx>>, VerifyError> {
            let mut map = self.contexts.lock().unwrap_or_else(|e| e.into_inner());
            if !map.contains_key(&epoch) {
                let mut st = 0;
                let p = unsafe { sys::ignpow_ctx_create(self.algo_c.as_ptr(), self.params_ptr(), height, 0, &mut st) };
                if p.is_null() || st != sys::IGNPOW_OK {
                    return Err(VerifyError::Reference(sys::status_name(st).into()));
                }
                map.insert(epoch, Ctx(p));
                // the current epoch's context and the next's at most; a retired one is released here
                while map.len() > MAX_CONTEXTS {
                    let oldest = *map.keys().min().unwrap_or(&epoch);
                    if oldest == epoch {
                        break;
                    }
                    map.remove(&oldest);
                    if let Ok(mut f) = self.full.lock() {
                        f.remove(&oldest);
                    }
                }
            }
            Ok(map)
        }
    }
}

/// Picks the verifier for an algorithm id: the stub for the stub worker; with the `ignpow` feature, the shared
/// reference for the candidates it carries.
pub fn verifier_for(algo: &str) -> Result<Box<dyn Verifier>, VerifyError> {
    verifier_for_params(algo, None)
}

/// The same with the job's params document, passed through to the reference unchanged (mandatory for igneumhash).
pub fn verifier_for_params(algo: &str, params: Option<&str>) -> Result<Box<dyn Verifier>, VerifyError> {
    if algo == stubhash::STUB_ALGO {
        return Ok(Box::new(StubVerifier));
    }
    #[cfg(feature = "ignpow")]
    {
        return Ok(Box::new(reference::ReferenceVerifier::with_params(algo, params)?));
    }
    #[allow(unreachable_code)]
    let _ = params;
    #[allow(unreachable_code)]
    Err(VerifyError::UnknownAlgo(algo.to_string()))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn stub_agrees_with_itself_and_rejects_a_flip() {
        let h = [3u8; 32];
        let (m, x) = stubhash::stub_hash_at(250, &h, 99);
        let v = StubVerifier;
        assert!(v.verify(250, &h, 99, &m, &x).unwrap());
        let mut bad = x;
        bad[31] ^= 1;
        assert!(!v.verify(250, &h, 99, &m, &bad).unwrap());
        assert!(!v.verify(250, &h, 98, &m, &x).unwrap());
        assert!(!v.verify(300, &h, 99, &m, &x).unwrap(), "another epoch");
        assert!(v.verify(250, &h, 99, &m[..8], &x).is_err());
    }

    /// The memory seat's finding on the Metal swap path, 11 October 2026: a retired epoch's dataset must be
    /// released at the swap. On the supervisor's side the only datasets held are the verifier's contexts: the
    /// current epoch's and the next's at most, the oldest released as a newer one is held.
    #[cfg(feature = "ignpow")]
    #[test]
    fn a_retired_epochs_context_is_released_at_the_swap() {
        let v = reference::ReferenceVerifier::with_params("igneumhash", Some("{\"version\": 1, \"dataset_mib\": 16, \"epoch_blocks\": 4}")).unwrap();
        let header = [0x11u8; 32];
        let bad = [0u8; 32];
        for epoch in 0..5u64 {
            let height = epoch * 4 + 1;
            let _ = v.verify(height, &header, 1, &bad, &bad);
            let held = v.contexts_held();
            assert!(held <= reference::MAX_CONTEXTS, "after epoch {epoch}: {held} contexts held");
            assert!(held >= 1);
        }
        assert_eq!(v.contexts_held(), reference::MAX_CONTEXTS);
        assert!(v.holds_epoch(4) && v.holds_epoch(3) && !v.holds_epoch(0) && !v.holds_epoch(1) && !v.holds_epoch(2));
    }

    #[cfg(feature = "ignpow")]
    #[test]
    fn reference_candidates_pass_their_vectors_and_verify_their_own_hash() {
        // every candidate is checked on its own thread under a bound: a reference whose context build or hash
        // never returns (the gate hang of 10 October 2026 on a research candidate) fails this test by name
        // within the bound instead of holding the gate's test binary
        for algo in reference::algos() {
            let (tx, rx) = std::sync::mpsc::channel::<()>();
            let name = algo.clone();
            let worker = std::thread::Builder::new().name(format!("verify-{name}")).spawn(move || {
                check_candidate(&name);
                let _ = tx.send(());
            }).expect("thread");
            match rx.recv_timeout(std::time::Duration::from_secs(90)) {
                Ok(()) => {
                    worker.join().expect("the candidate's check panicked");
                }
                Err(std::sync::mpsc::RecvTimeoutError::Timeout) => {
                    panic!("{algo}: the reference did not return within 90 s (its context build or hash hangs); the candidate's reference is at fault, not the verifier");
                }
                Err(std::sync::mpsc::RecvTimeoutError::Disconnected) => {
                    // the thread panicked before sending: join carries the panic with the candidate's name
                    worker.join().unwrap_or_else(|e| panic!("{algo}: {}", e.downcast_ref::<String>().cloned().or_else(|| e.downcast_ref::<&str>().map(|s| s.to_string())).unwrap_or_default()));
                    panic!("{algo}: the check ended without a result");
                }
            }
        }
    }

    #[cfg(feature = "ignpow")]
    fn check_candidate(algo: &str) {
        {
            // igneumhash's document is mandatory (version first); the controls take their upstream defaults
            let params = match algo {
                "igneumhash" => Some("{\"version\": 1}"),
                // rxe1's document is mandatory too; a test-only 16 MiB configuration keeps the test fast (REFERENCE-NOTES.md section 2)
                "rxe1" => Some("{\"schema\": 2, \"mode\": 1, \"blocks\": 1, \"rounds\": 32, \"registers\": 8, \"dataset_bytes\": 16777216, \"cache_bytes\": 1048576, \"test_only\": 1}"),
                // the research references (compact-proof and Forge F1 under B1) at test-only 1 MiB points
                "compactproof" => Some("{\"schema\": 1, \"memory_bytes\": 1048576, \"label_bytes\": 32, \"challenges\": 4, \"test_only\": 1}"),
                "forgeb1" => Some("{\"schema\": 1, \"graph\": 0, \"memory_bytes\": 1048576, \"label_bytes\": 128, \"challenges\": 4, \"batch\": 64, \"test_only\": 1}"),
                _ => None,
            };
            let v = reference::ReferenceVerifier::with_params(algo, params).unwrap_or_else(|e| panic!("{algo}: {e}"));
            let header = [0x11u8; 32];
            let epoch = v.epoch_of(1).unwrap();
            // compute through the same path the verifier uses, then confirm it agrees with itself
            let mut map_guard_free = false;
            let _ = (&mut map_guard_free, epoch);
            let mut mix = [0u8; 32];
            let mut hash = [0u8; 32];
            unsafe {
                let mut st = 0;
                let c = std::ffi::CString::new(algo).unwrap();
                let pc = params.map(|p| std::ffi::CString::new(p).unwrap());
                let pp = pc.as_ref().map(|c| c.as_ptr()).unwrap_or(std::ptr::null());
                let ctx = igneum_pow_sys::ignpow_ctx_create(c.as_ptr(), pp, 1, 0, &mut st);
                assert!(!ctx.is_null(), "{algo}: {}", igneum_pow_sys::status_name(st));
                assert_eq!(igneum_pow_sys::ignpow_hash(ctx, 1, header.as_ptr(), 7, mix.as_mut_ptr(), hash.as_mut_ptr()), 0);
                igneum_pow_sys::ignpow_ctx_free(ctx);
            }
            assert!(v.verify(1, &header, 7, &mix, &hash).unwrap(), "{algo}");
            let mut bad = hash;
            bad[0] ^= 1;
            assert!(!v.verify(1, &header, 7, &mix, &bad).unwrap(), "{algo}");
        }
    }

    /// The keyed path (SPEC 0.9, bit 10) against the hash team's `keyed` class at the genesis value S = 2 (the sets
    /// regenerated under SPEC 0.9.3, the dataset over P_d, main bf6604ed): under the vector's test secret the
    /// verifier reproduces the mix, the digest and the S shares of the row; a wrong secret agrees with nothing;
    /// the keyless entry refuses the keyed document. Four rows (a light context at 2 GiB: each row costs a light
    /// hash).
    #[cfg(feature = "ignpow")]
    #[test]
    fn the_keyed_path_reproduces_the_keyed_class_and_refuses_the_keyless_entry() {
        let root = std::path::PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../../../pow/vectors/igneumhash/v8-d2048-s0-r64-k32-p256-w128-b16-x16-f1056-k2/keyed.json");
        let text = match std::fs::read_to_string(&root) {
            Ok(t) => t,
            Err(_) => {
                eprintln!("no keyed set at {}: the row is skipped here (the gate's tree carries it)", root.display());
                return;
            }
        };
        let set: serde_json::Value = serde_json::from_str(&text).unwrap();
        let doc = set["params"].to_string();
        let v = reference::ReferenceVerifier::with_params("igneumhash", Some(&doc)).unwrap();
        assert_eq!(v.keyed_rounds(), Some(2), "the set's document carries keyed_rounds 2");
        let rows = set["vectors"].as_array().unwrap();
        for row in rows.iter().take(4) {
            // height and nonce are JSON numbers in the sets (a string form is taken too)
            let num = |v: &serde_json::Value| v.as_u64().or_else(|| v.as_str().and_then(|t| t.parse().ok())).expect("a number");
            let height: u64 = num(&row["height"]);
            let nonce: u64 = num(&row["nonce"]);
            let header = igneum_worker_proto::hex_to_32(row["header"].as_str().unwrap()).unwrap();
            let mix = igneum_worker_proto::hex_decode(row["mix"].as_str().unwrap()).unwrap();
            let hash = igneum_worker_proto::hex_to_32(row["hash"].as_str().unwrap()).unwrap();
            let sk = igneum_worker_proto::hex_to_32(row["test_sk"].as_str().unwrap()).unwrap();
            let shares = igneum_worker_proto::hex_decode(row["shares"].as_str().unwrap()).unwrap();
            assert_eq!(shares.len(), 2 * 32, "the set carries S shares of 32 bytes");
            let got = v.verify_keyed(height, &header, nonce, &mix, &hash, &sk).unwrap();
            assert_eq!(got.as_deref(), Some(shares.as_slice()), "the shares of row {nonce}");
            let mut wrong = sk;
            wrong[31] ^= 1;
            assert_eq!(v.verify_keyed(height, &header, nonce, &mix, &hash, &wrong).unwrap(), None, "another secret agrees with nothing");
            assert!(v.verify(height, &header, nonce, &mix, &hash).is_err(), "the keyless entry refuses a keyed document");
        }
    }
}
