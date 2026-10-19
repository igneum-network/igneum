//! The signer process (docs/product-b/DESIGN.md, 1.3). It reads one JSON request per line on stdin and writes one
//! JSON response per line on stdout. It has no network code, no window, and no way to reach a website. Only the
//! engine talks to it, over the pipes it was started with.
//!
//! Ops: status, open, create, import_phrase, import_key, unlock, lock, sign_tx, sign_message, reveal, set_backed_up.
//! Every signing op carries the payload and the sentences the window showed; the core recomputes the sentences and
//! refuses a mismatch. A secret never appears in a response except from `create` (the words, once) and `reveal`
//! (after the password is given again).

use std::fs;
use std::io::{self, BufRead, Write};
use std::path::PathBuf;

use serde_json::{json, Value};
use zeroize::Zeroizing;

use igneum_wallet_core::{
    envelope::UnsignedEnvelope,
    mnemonic,
    signer::{sign_personal_message, sign_transaction, Signer, SoftwareSigner},
    tx::Transaction,
    vault::{self, KdfParams, Public, Secret, SecretKind, VaultFile},
    Address, Error, Result,
};

struct State {
    path: Option<PathBuf>,
    file: Option<VaultFile>,
    signer: Option<SoftwareSigner>,
    /// the phrase of a freshly created wallet, kept until the window confirms the words were written down
    fresh_phrase: Option<Zeroizing<String>>,
    /// the previous wallet's file at `path` (OLD-BASE.md step 3): read once, re-sealed on the first unlock
    legacy: Option<vault::LegacyVault>,
    /// the phrase while unlocked (the key derived from it is in memory anyway), for the host-confirmed reveal of the
    /// biometric path; zeroed at lock, open and remove
    unlocked_phrase: Option<Zeroizing<String>>,
}

fn str_field<'a>(v: &'a Value, k: &str) -> Result<&'a str> {
    v.get(k).and_then(|x| x.as_str()).ok_or_else(|| Error::Input(format!("missing field {k}")))
}

fn write_file(path: &PathBuf, file: &VaultFile) -> Result<()> {
    if let Some(dir) = path.parent() {
        fs::create_dir_all(dir)?;
    }
    let tmp = path.with_extension("json.tmp");
    fs::write(&tmp, serde_json::to_vec_pretty(file)?)?;
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        fs::set_permissions(&tmp, fs::Permissions::from_mode(0o600))?;
    }
    fs::rename(&tmp, path)?;
    Ok(())
}

fn status(st: &State) -> Value {
    let (address, kind) = match (&st.file, &st.signer, &st.legacy) {
        (Some(f), _, _) => (Some(f.public.address.to_checksum()), Some(format!("{:?}", f.public.kind).to_lowercase())),
        (None, Some(s), _) => (Some(s.address().to_checksum()), Some("key".into())),
        // the previous wallet's file: its address is in clear, its kind follows its source (seed, created: a
        // phrase; key, miner: a key); the file is re-sealed on the first unlock
        (None, None, Some(l)) => (
            Address::parse(&l.address).ok().map(|a| a.to_checksum()),
            Some(if l.source == "seed" || l.source == "created" { "phrase".to_string() } else { "key".to_string() }),
        ),
        _ => (None, None),
    };
    json!({
        "has_vault": st.file.is_some() || st.legacy.is_some(),
        "unlocked": st.signer.is_some(),
        "address": address,
        "kind": kind,
        "backed_up": st.file.as_ref().map(|f| f.backed_up).or_else(|| st.legacy.as_ref().map(|l| l.backed_up)).unwrap_or(false),
        "fresh_phrase_pending": st.fresh_phrase.is_some(),
        "legacy": st.legacy.is_some(),
        // the process's own shape, read by itself (a process reads its own /proc entries whatever its dumpable
        // flag; an outside reader cannot once guard_memory() has run): the isolation row ISO-01 reads it here
        "process": process_shape(),
    })
}

/// The signer's own file descriptors (with their targets), the names of its environment and the count of its
/// sockets, from /proc/self on Linux; `null` elsewhere. Used by wallet/tools/isolation-tests.sh for ISO-01 since
/// the memory guard hides /proc/<pid> from an unprivileged outside reader (wallet-security, 12 October 2026).
#[cfg(target_os = "linux")]
fn process_shape() -> Value {
    let mut fds = Vec::new();
    let mut sockets = 0;
    if let Ok(rd) = fs::read_dir("/proc/self/fd") {
        let mut entries: Vec<_> = rd.flatten().collect();
        entries.sort_by_key(|e| e.file_name().to_string_lossy().parse::<u64>().unwrap_or(u64::MAX));
        for e in entries {
            let name = e.file_name().to_string_lossy().to_string();
            let target = fs::read_link(e.path()).map(|t| t.to_string_lossy().to_string()).unwrap_or_default();
            // the directory handle of this very listing is not an open file of the signer's
            if target.ends_with("/proc/self/fd") || target.contains(&format!("/proc/{}/fd", std::process::id())) {
                continue;
            }
            if target.starts_with("socket:") {
                sockets += 1;
            }
            fds.push(json!({"fd": name, "target": target}));
        }
    }
    let env: Vec<String> = fs::read("/proc/self/environ")
        .map(|b| b.split(|c| *c == 0).filter(|s| !s.is_empty()).map(|s| String::from_utf8_lossy(s).split('=').next().unwrap_or("").to_string()).collect())
        .unwrap_or_default();
    json!({"pid": std::process::id(), "fds": fds, "env": env, "sockets": sockets, "memory_lock": memory_lock_shape()})
}

#[cfg(not(target_os = "linux"))]
fn process_shape() -> Value {
    #[cfg(unix)]
    { json!({"memory_lock": memory_lock_shape()}) }
    #[cfg(not(unix))]
    { Value::Null }
}

/// BLAKE3 over the chain's tagged form: tag(T) = u8(len(T)) || T, then the data.
fn blake3_tagged(tag: &[u8], data: &[u8]) -> [u8; 32] {
    let mut h = blake3::Hasher::new();
    h.update(&[tag.len() as u8]);
    h.update(tag);
    h.update(data);
    *h.finalize().as_bytes()
}

fn seal_and_keep(st: &mut State, secret: Secret, password: &str, chain_id: Option<u64>) -> Result<Value> {
    // the minimum (ST-4, the project's ruling of 12 October 2026): twelve characters, the count in the refusal, no
    // composition rule; every new password passes here (create, import, the re-seal). A single repeated character
    // is refused too (the security review's note; not a composition rule)
    igneum_wallet_core::password::check_minimum(password)?;
    igneum_wallet_core::password::check_not_trivial(password)?;
    let path = st.path.clone().ok_or_else(|| Error::Input("no vault path: send open first".into()))?;
    let signer = match secret.kind {
        SecretKind::Phrase => SoftwareSigner::from_phrase(&secret.text, 0)?,
        SecretKind::Key => SoftwareSigner::from_key_hex(&secret.text)?,
    };
    let public = Public {
        address: signer.address(),
        kind: secret.kind.clone(),
        created_unix: vault::now_unix(),
        chain_id_hint: chain_id,
    };
    let kdf = if std::env::var("IGNEUM_SIGNER_LIGHT_KDF").is_ok() { KdfParams::light() } else { KdfParams::default_desktop() };
    let file = vault::seal(&secret, password.as_bytes(), public, kdf)?;
    write_file(&path, &file)?;
    let address = signer.address().to_checksum();
    st.unlocked_phrase = if secret.kind == SecretKind::Phrase { Some(Zeroizing::new(secret.text.clone())) } else { None };
    st.file = Some(file);
    st.signer = Some(signer);
    Ok(json!({"address": address}))
}

fn handle(st: &mut State, op: &str, p: &Value, pw: Option<&str>) -> Result<Value> {
    match op {
        "status" => Ok(status(st)),
        "open" => {
            let path = PathBuf::from(str_field(p, "path")?);
            st.legacy = None;
            st.file = if path.exists() {
                let text = fs::read_to_string(&path)?;
                if vault::is_legacy_text(&text) {
                    // the previous wallet's file (OLD-BASE.md step 3): its address shows while locked; the first
                    // unlock re-seals it in this format and keeps the old file beside it
                    st.legacy = Some(serde_json::from_str(&text)?);
                    None
                } else {
                    Some(serde_json::from_str(&text)?)
                }
            } else {
                None
            };
            st.path = Some(path);
            st.signer = None;
            st.unlocked_phrase = None;
            Ok(status(st))
        }
        "create" => {
            let password = pw.filter(|x| !x.is_empty()).ok_or_else(|| Error::Input("missing field password".into()))?;
            let chain_id = p.get("chain_id").and_then(|c| c.as_u64());
            let phrase = mnemonic::new_phrase();
            let secret = Secret { kind: SecretKind::Phrase, text: phrase.to_string() };
            let mut out = seal_and_keep(st, secret, password, chain_id)?;
            // the words are shown once by the window; the signer keeps them until the check passes
            out["words"] = json!(phrase.split(' ').collect::<Vec<_>>());
            st.fresh_phrase = Some(phrase);
            Ok(out)
        }
        "confirm_words" => {
            // {checks: [{index, word}]}: three positions typed back; on success the fresh phrase is dropped
            let phrase = st.fresh_phrase.as_ref().ok_or_else(|| Error::Input("no words to confirm".into()))?;
            let words: Vec<&str> = phrase.split(' ').collect();
            let checks = p.get("checks").and_then(|c| c.as_array()).ok_or_else(|| Error::Input("no checks".into()))?;
            if checks.len() < 3 {
                return Err(Error::Input("three words are checked".into()));
            }
            for c in checks {
                let i = c.get("index").and_then(|x| x.as_u64()).ok_or_else(|| Error::Input("bad check".into()))? as usize;
                let w = str_field(c, "word")?.trim().to_lowercase();
                if i >= words.len() || words[i] != w {
                    return Err(Error::Refused(format!("word {} is not right", i + 1)));
                }
            }
            // the three-word check proves the words were read once; it is not a backup (the project's 0.0.6 order on
            // participant 7, 12 October 2026): only the Backup section's reveal or a restore sets backed_up
            st.fresh_phrase = None;
            Ok(status(st))
        }
        "import_phrase" => {
            let phrase = mnemonic::check_phrase(str_field(p, "phrase")?)?;
            let secret = Secret { kind: SecretKind::Phrase, text: phrase.to_string() };
            let out = seal_and_keep(st, secret, pw.filter(|x| !x.is_empty()).ok_or_else(|| Error::Input("missing field password".into()))?, p.get("chain_id").and_then(|c| c.as_u64()))?;
            if let (Some(path), Some(file)) = (&st.path, st.file.as_mut()) {
                file.backed_up = true; // imported words are already somewhere else
                write_file(path, file)?;
            }
            Ok(out)
        }
        "import_key" => {
            let key = Zeroizing::new(str_field(p, "key")?.trim().to_string());
            let secret = Secret { kind: SecretKind::Key, text: key.to_string() };
            let out = seal_and_keep(st, secret, pw.filter(|x| !x.is_empty()).ok_or_else(|| Error::Input("missing field password".into()))?, p.get("chain_id").and_then(|c| c.as_u64()))?;
            if let (Some(path), Some(file)) = (&st.path, st.file.as_mut()) {
                file.backed_up = true;
                write_file(path, file)?;
            }
            Ok(out)
        }
        "unlock" => {
            let password = pw.filter(|x| !x.is_empty()).ok_or_else(|| Error::Input("missing field password".into()))?;
            if st.file.is_none() {
                if let Some(legacy) = st.legacy.as_ref() {
                    // the previous wallet's file, on its password: re-sealed in this format at this wallet's KDF,
                    // the old file kept beside it as vault.json.old (the words or the key are the same secret;
                    // the address is checked against the old file's before anything is written). The old file
                    // stays held until the migration has succeeded: a wrong password leaves everything as it was
                    // (the first smoke found the state dropped on the refusal)
                    let path = st.path.clone().ok_or_else(|| Error::Input("no vault path: send open first".into()))?;
                    let (secret, backed_up) = vault::open_legacy(legacy, password.as_bytes())?;
                    let signer = match secret.kind {
                        SecretKind::Phrase => SoftwareSigner::from_phrase(&secret.text, 0)?,
                        SecretKind::Key => SoftwareSigner::from_key_hex(&secret.text)?,
                    };
                    let old_address = Address::parse(&legacy.address)?;
                    if signer.address() != old_address {
                        return Err(Error::Vault("the key in the old vault does not match its address".into()));
                    }
                    let public = Public { address: signer.address(), kind: secret.kind.clone(), created_unix: vault::now_unix(), chain_id_hint: None };
                    let kdf = if std::env::var("IGNEUM_SIGNER_LIGHT_KDF").is_ok() { KdfParams::light() } else { KdfParams::default_desktop() };
                    let mut file = vault::seal(&secret, password.as_bytes(), public, kdf)?;
                    file.backed_up = backed_up;
                    let old = path.with_extension("json.old");
                    fs::rename(&path, &old)?;
                    write_file(&path, &file)?;
                    // the new seal is read back and opened with the same password before the old file goes: on
                    // success the old file (the previous wallet's weaker seal of the same secret) is removed at
                    // once (wallet-security's G7, 12 October 2026); on failure the old file is put back and the
                    // migration refused, nothing lost
                    let check = fs::read(&path).ok().and_then(|b| serde_json::from_slice::<VaultFile>(&b).ok()).and_then(|f| vault::open(&f, password.as_bytes()).ok());
                    match check {
                        Some(_) => {
                            let _ = fs::remove_file(&old);
                        }
                        None => {
                            let _ = fs::rename(&old, &path);
                            return Err(Error::Vault("the re-sealed vault did not open; the old file is back and nothing changed".into()));
                        }
                    }
                    st.legacy = None;
                    st.unlocked_phrase = if secret.kind == SecretKind::Phrase { Some(Zeroizing::new(secret.text.clone())) } else { None };
                    st.file = Some(file);
                    st.signer = Some(signer);
                    let mut out = status(st);
                    out["migrated_from_legacy"] = json!(true);
                    return Ok(out);
                }
            }
            let file = st.file.as_ref().ok_or_else(|| Error::Vault("no wallet on this machine".into()))?;
            let secret = vault::open(file, password.as_bytes())?;
            let signer = match secret.kind {
                SecretKind::Phrase => SoftwareSigner::from_phrase(&secret.text, 0)?,
                SecretKind::Key => SoftwareSigner::from_key_hex(&secret.text)?,
            };
            if signer.address() != file.public.address {
                return Err(Error::Vault("the key in the vault does not match its address".into()));
            }
            st.unlocked_phrase = if secret.kind == SecretKind::Phrase { Some(Zeroizing::new(secret.text.clone())) } else { None };
            st.signer = Some(signer);
            Ok(status(st))
        }
        "lock" => {
            st.signer = None;
            st.unlocked_phrase = None;
            st.fresh_phrase = None;
            Ok(status(st))
        }
        // the mining identity made in the window (the project's order on participant 4's finding, 12 October 2026): the
        // identity's secret is derived from the wallet's own key under a domain tag, so the words restore it on any
        // machine and nothing new is stored; the public half pk = BLAKE3(tag("IgneumHash/R0A/bond-pk") || sk) is the
        // chain's form (tag(T) = u8(len(T)) || T); the secret never leaves the signer in this build (a sealer takes it
        // by a later reviewed export)
        "identity" => {
            let signer = st.signer.as_ref().ok_or_else(|| Error::Refused("the wallet is locked".into()))?;
            let key_hex = signer.export_key_hex();
            let key_bytes = Zeroizing::new(igneum_wallet_core::hex::decode(&key_hex)?);
            let sk = Zeroizing::new(blake3_tagged(b"IgneumHash/R0A/wallet-identity", &key_bytes));
            let pk = blake3_tagged(b"IgneumHash/R0A/bond-pk", &sk[..]);
            Ok(json!({"key": igneum_wallet_core::hex::encode(&pk[..])}))
        }
        "sign_tx" => {
            let signer = st.signer.as_ref().ok_or_else(|| Error::Refused("the wallet is locked".into()))?;
            let tx: Transaction = serde_json::from_value(p.get("transaction").cloned().unwrap_or(Value::Null))?;
            let from = Address::parse(str_field(p, "from")?)?;
            let network = str_field(p, "network")?;
            let shown: Vec<String> = serde_json::from_value(p.get("sentences").cloned().unwrap_or(json!([])))?;
            let signed = sign_transaction(signer, &tx, &from, network, &shown)?;
            Ok(serde_json::to_value(signed)?)
        }
        "sign_envelope" => {
            let signer = st.signer.as_ref().ok_or_else(|| Error::Refused("the wallet is locked".into()))?;
            let env: UnsignedEnvelope = serde_json::from_value(p.get("envelope").cloned().unwrap_or(Value::Null))?;
            Ok(serde_json::to_value(env.sign(signer)?)?)
        }
        "sign_typed" => {
            // the typed data itself, never a digest: the signer recomputes the digest and the lines the window
            // showed and refuses when the lines differ
            let signer = st.signer.as_ref().ok_or_else(|| Error::Refused("the wallet is locked".into()))?;
            let typed = p.get("typed").cloned().ok_or_else(|| Error::Input("missing typed".into()))?;
            let shown: Vec<String> = serde_json::from_value(p.get("lines").cloned().unwrap_or(json!([])))?;
            let lines = igneum_wallet_core::eip712::render(&typed)?;
            if lines != shown {
                return Err(Error::Refused("what was shown is not what would be signed".into()));
            }
            let digest = igneum_wallet_core::eip712::digest(&typed)?;
            let sig = signer.sign_hash(&digest)?;
            Ok(json!({"signature": igneum_wallet_core::hex::encode(&sig.to_rsv())}))
        }
        "recover" => {
            // no key involved: the address that signed a 32-byte hash (for a pool or any caller that wants the
            // recovery done here rather than in its own code)
            let hash = igneum_wallet_core::hex::decode(str_field(p, "hash")?)?;
            if hash.len() != 32 {
                return Err(Error::Input("a hash has 32 bytes".into()));
            }
            let mut h = [0u8; 32];
            h.copy_from_slice(&hash);
            let sig = igneum_wallet_core::sign::Signature::from_rsv(&igneum_wallet_core::hex::decode(str_field(p, "signature")?)?)?;
            Ok(json!({"address": sig.recover(&h)?}))
        }
        "verify_envelope" => {
            // no key involved: a signed envelope checked against the unsigned one it answers (the signature recovers
            // to the wallet's address, the raw bytes match the transaction and signature)
            let unsigned: UnsignedEnvelope = serde_json::from_value(p.get("unsigned").cloned().unwrap_or(Value::Null))?;
            let signed: igneum_wallet_core::envelope::SignedEnvelope = serde_json::from_value(p.get("signed").cloned().unwrap_or(Value::Null))?;
            unsigned.check()?;
            signed.check_against(&unsigned)?;
            Ok(json!({"ok": true, "from": unsigned.from, "hash": igneum_wallet_core::hex::encode(&signed.hash)}))
        }
        "sign_message" => {
            let signer = st.signer.as_ref().ok_or_else(|| Error::Refused("the wallet is locked".into()))?;
            let message = igneum_wallet_core::hex::decode(str_field(p, "message")?)?;
            let sig = sign_personal_message(signer, &message)?;
            Ok(json!({"signature": igneum_wallet_core::hex::encode(&sig.to_rsv())}))
        }
        "verify_password" => {
            // the password checked against the vault and nothing returned but ok: the biometric enrolment parks a
            // password only after this says it opens the vault (OLD-BASE.md step 3, the gate)
            let file = st.file.as_ref().ok_or_else(|| Error::Vault("no wallet on this machine".into()))?;
            let _ = vault::open(file, pw.filter(|x| !x.is_empty()).ok_or_else(|| Error::Input("missing field password".into()))?.as_bytes())?;
            Ok(json!({"ok": true}))
        }
        "reveal_unlocked" => {
            // the biometric path: the words and the key from the key already unlocked here, only while unlocked
            // and only on a request the engine marks host_confirmed (the engine sends it after the landing check took a
            // confirmed nonce; the page cannot confirm its own challenges). The signer never sees a prompt: the
            // engine is the boundary that this op trusts, as it trusts the engine's sign requests
            if p.get("host_confirmed").and_then(|h| h.as_bool()) != Some(true) {
                return Err(Error::Refused("reveal_unlocked needs a host-confirmed request".into()));
            }
            let signer = st.signer.as_ref().ok_or_else(|| Error::Refused("the wallet is locked".into()))?;
            let file = st.file.as_ref().ok_or_else(|| Error::Vault("no wallet on this machine".into()))?;
            let words = match (&file.public.kind, st.unlocked_phrase.as_ref()) {
                (SecretKind::Phrase, Some(ph)) => json!(ph.split(' ').collect::<Vec<_>>()),
                _ => Value::Null,
            };
            Ok(json!({"words": words, "key": signer.export_key_hex().to_string(), "address": signer.address().to_checksum()}))
        }
        "reveal" => {
            // the password again, every time: the words and the key leave the signer only on purpose
            let file = st.file.as_ref().ok_or_else(|| Error::Vault("no wallet on this machine".into()))?;
            let secret = vault::open(file, pw.filter(|x| !x.is_empty()).ok_or_else(|| Error::Input("missing field password".into()))?.as_bytes())?;
            let key_hex = match secret.kind {
                SecretKind::Phrase => SoftwareSigner::from_phrase(&secret.text, 0)?.export_key_hex(),
                SecretKind::Key => SoftwareSigner::from_key_hex(&secret.text)?.export_key_hex(),
            };
            Ok(json!({
                "words": if secret.kind == SecretKind::Phrase { json!(secret.text.split(' ').collect::<Vec<_>>()) } else { Value::Null },
                "key": key_hex.to_string(),
            }))
        }
        "set_backed_up" => {
            if let (Some(path), Some(file)) = (&st.path, st.file.as_mut()) {
                file.backed_up = true;
                write_file(path, file)?;
            }
            Ok(status(st))
        }
        "remove" => {
            let file = st.file.as_ref().ok_or_else(|| Error::Vault("no wallet on this machine".into()))?;
            vault::open(file, pw.filter(|x| !x.is_empty()).ok_or_else(|| Error::Input("missing field password".into()))?.as_bytes())?;
            if let Some(path) = &st.path {
                if path.exists() {
                    fs::remove_file(path)?;
                }
            }
            st.file = None;
            st.signer = None;
            st.unlocked_phrase = None;
            st.fresh_phrase = None;
            Ok(status(st))
        }
        other => Err(Error::Input(format!("unknown op {other}"))),
    }
}

/// The memory guards (wallet-security's G4; DESIGN.md 1.2): before any secret is read, the core-dump limit is set
/// to zero (a crash writes no image of the key), the process is marked not dumpable on Linux (no ptrace by a
/// same-user process, no core), and the whole address space is locked in RAM where the system permits (no page
/// of a key reaches swap). Each is best effort on a desktop: a refusal is written once to stderr as a fact and the
/// signer runs on, since a wallet that will not start is worse than one that says what it could not lock. libc
/// (MIT or Apache-2.0, 0.2) is the one dependency for the three calls.
#[cfg(unix)]
fn guard_memory() {
    unsafe {
        let none = libc::rlimit { rlim_cur: 0, rlim_max: 0 };
        if libc::setrlimit(libc::RLIMIT_CORE, &none) != 0 {
            eprintln!("signer: the core-dump limit could not be set to zero");
        }
        #[cfg(target_os = "linux")]
        if libc::prctl(libc::PR_SET_DUMPABLE, 0, 0, 0, 0) != 0 {
            eprintln!("signer: the process could not be marked not dumpable");
        }
        // the lock limit (the builder team's Pay row, 12 October 2026: a stock container gives 8 MiB of lockable
        // memory, and with MCL_FUTURE every later allocation must be lockable, so the KDF's 256 MiB working set
        // failed and the signer died at the first create): the soft limit is raised to the hard one, and when the
        // hard limit is below what the KDF needs the current pages are locked without MCL_FUTURE and the status
        // says so; the key's pages are still locked, the KDF's working memory is not
        let mut lim = libc::rlimit { rlim_cur: 0, rlim_max: 0 };
        let mut limit_kib: u64 = 0;
        let mut unlimited = false;
        if libc::getrlimit(libc::RLIMIT_MEMLOCK, &mut lim) == 0 {
            if lim.rlim_cur != lim.rlim_max {
                let raised = libc::rlimit { rlim_cur: lim.rlim_max, rlim_max: lim.rlim_max };
                let _ = libc::setrlimit(libc::RLIMIT_MEMLOCK, &raised);
            }
            unlimited = lim.rlim_max == libc::RLIM_INFINITY;
            limit_kib = if unlimited { u64::MAX } else { (lim.rlim_max as u64) / 1024 };
        }
        let flags = if lock_future(if unlimited { None } else { Some(limit_kib) }) { libc::MCL_CURRENT | libc::MCL_FUTURE } else { libc::MCL_CURRENT };
        if libc::mlockall(flags) != 0 {
            eprintln!("signer: memory could not be locked in RAM (mlockall refused; the key may be paged out under pressure)");
            MEMORY_LOCK.store(0, std::sync::atomic::Ordering::Relaxed);
        } else if flags & libc::MCL_FUTURE != 0 {
            MEMORY_LOCK.store(2, std::sync::atomic::Ordering::Relaxed);
        } else {
            eprintln!("signer: the lockable-memory limit on this machine is {limit_kib} KiB, below the {KDF_LOCK_NEED_KIB} KiB the vault's key derivation uses: the pages held now are locked, later allocations are not (on a container raise the limit, ulimit -l unlimited, to lock everything)");
            MEMORY_LOCK.store(1, std::sync::atomic::Ordering::Relaxed);
        }
        MEMORY_LOCK_LIMIT_KIB.store(limit_kib, std::sync::atomic::Ordering::Relaxed);
    }
}

/// What the vault's key derivation needs lockable, with headroom: the release's 256 MiB plus the process.
const KDF_LOCK_NEED_KIB: u64 = 320 * 1024;

/// Whether every future page may be locked too (MCL_FUTURE): yes with no limit or a limit above the KDF's need;
/// no under a container's 8 MiB, where a locked future allocation of 256 MiB would abort the process (the project's rule
/// of 12 October 2026: a refused lock degrades and names itself, never fails the create). `None` is no limit.
fn lock_future(hard_limit_kib: Option<u64>) -> bool {
    match hard_limit_kib {
        None => true,
        Some(k) => k >= KDF_LOCK_NEED_KIB,
    }
}

#[cfg(test)]
mod lock_tests {
    use super::*;

    #[test]
    fn a_container_limit_locks_the_current_pages_only() {
        assert!(!lock_future(Some(8 * 1024)), "8 MiB: no future lock, the create must not abort");
        assert!(!lock_future(Some(256 * 1024)), "exactly the KDF's size leaves no headroom");
        assert!(lock_future(Some(400 * 1024)));
        assert!(lock_future(None));
    }
}
/// 0 none, 1 the current pages only, 2 everything (current and future)
static MEMORY_LOCK: std::sync::atomic::AtomicU8 = std::sync::atomic::AtomicU8::new(0);
static MEMORY_LOCK_LIMIT_KIB: std::sync::atomic::AtomicU64 = std::sync::atomic::AtomicU64::new(0);

fn memory_lock_shape() -> Value {
    let mode = match MEMORY_LOCK.load(std::sync::atomic::Ordering::Relaxed) {
        2 => "all",
        1 => "current",
        _ => "none",
    };
    let limit = MEMORY_LOCK_LIMIT_KIB.load(std::sync::atomic::Ordering::Relaxed);
    json!({"mode": mode, "limit_kib": if limit == u64::MAX { Value::Null } else { json!(limit) }, "kdf_need_kib": KDF_LOCK_NEED_KIB})
}

/// Windows: the process's own address space pinned with VirtualLock page by page is not practical for a whole
/// process, so the guard here is the dump side: the process refuses to be a source of a minidump by setting the
/// error-mode and the "no dumps" mitigation where the system offers it, and the secrets are zeroized as on the
/// other hosts. The kernel32 calls are declared here rather than through a crate (the in-house rule).
#[cfg(windows)]
fn guard_memory() {
    #[link(name = "kernel32")]
    extern "system" {
        fn SetErrorMode(mode: u32) -> u32;
    }
    const SEM_NOGPFAULTERRORBOX: u32 = 0x0002;
    const SEM_FAILCRITICALERRORS: u32 = 0x0001;
    unsafe {
        // no fault dialog and no Windows Error Reporting hand-off on a crash: the signer ends quietly
        SetErrorMode(SEM_NOGPFAULTERRORBOX | SEM_FAILCRITICALERRORS);
    }
}

#[cfg(not(any(unix, windows)))]
fn guard_memory() {}

fn main() {
    guard_memory();
    let stdin = io::stdin();
    let stdout = io::stdout();
    let mut st = State { path: None, file: None, signer: None, fresh_phrase: None, legacy: None, unlocked_phrase: None };
    for line in stdin.lock().lines() {
        // the raw request line holds the password on the ops that carry one (G2): it is zeroized when it drops, as
        // is the parsed request at the end of the turn
        let line = match line {
            Ok(l) => Zeroizing::new(l),
            Err(_) => break,
        };
        if line.trim().is_empty() {
            continue;
        }
        let mut req: Value = match serde_json::from_str(&line) {
            Ok(v) => v,
            Err(e) => {
                let _ = writeln!(stdout.lock(), "{}", json!({"id": null, "ok": false, "error": format!("bad request: {e}")}));
                continue;
            }
        };
        let id = req.get("id").cloned().unwrap_or(Value::Null);
        let op = req.get("op").and_then(|o| o.as_str()).unwrap_or("").to_string();
        // the password's one copy: out of the parsed request into a zeroizing string before the op runs
        let pw: Option<Zeroizing<String>> = match req.get_mut("password") {
            Some(Value::String(p)) => Some(Zeroizing::new(std::mem::take(p))),
            _ => None,
        };
        let mut resp = match handle(&mut st, &op, &req, pw.as_deref().map(|p| p.as_str())) {
            Ok(result) => json!({"id": id, "ok": true, "result": result}),
            Err(e) => json!({"id": id, "ok": false, "error": e.to_string()}),
        };
        // the request (a password, a phrase, a key) and the reply (the words, the key) are scrubbed once the line
        // is written: no copy outlives the turn (wallet-security's MEM-2 on the signer side, 12 October 2026)
        let line = Zeroizing::new(resp.to_string());
        scrub(&mut req);
        scrub(&mut resp);
        let mut out = stdout.lock();
        if writeln!(out, "{}", line.as_str()).is_err() || out.flush().is_err() {
            break;
        }
    }
}

/// Zeroizes every string in a JSON value.
fn scrub(v: &mut Value) {
    use zeroize::Zeroize;
    match v {
        Value::String(s) => s.zeroize(),
        Value::Array(a) => a.iter_mut().for_each(scrub),
        Value::Object(o) => o.values_mut().for_each(scrub),
        _ => {}
    }
}
