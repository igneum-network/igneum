//! The signed payload-inputs manifest (review round 4, R4.5.2, ledger G13).
//!
//! The Windows build on GitHub's runner cannot make the node, the miner or the GPU workers (they come from the
//! node fork, which is not in the repository, and from NVIDIA's redistributables). Those files travel as
//! `payload-inputs.zip` on the downloads host. Before 4 October 2026 the runner checked the zip against a sha256
//! served beside it, which is a transfer check, not an authentication: whoever controls the host controls the
//! binaries, and the Mac then signed the update manifest over whatever the run produced.
//!
//! Now `packaging/windows/push-inputs.sh` writes `payload-inputs.json` (this format), signs it on the Mac with the
//! OTA key (`igneum-ota-sign sign-inputs`) and uploads the signature beside it. The workflow verifies the signature
//! with the public key compiled into the app (`manifest::OTA_PUBLIC_KEY_HEX`) before it builds anything, checks
//! the zip's sha256 and every unpacked file against the manifest, and checks the pinned node source commit
//! against `packaging/windows/node-source.pin` in the commit it builds. `fetch-ci-artifacts.sh` refuses to sign an
//! update manifest unless the run's verified inputs manifest re-verifies on the Mac.
//!
//! The bytes signed are the file as uploaded. `parse` refuses anything it does not understand, so a manifest the
//! signer would not sign is also one the verifier would not accept.

use crate::manifest::{hex_decode, sha256_file, verify_signature};
use serde::{Deserialize, Serialize};
use std::collections::BTreeMap;
use std::path::Path;

/// The format tag every manifest must carry.
pub const FORMAT: &str = "igneum-payload-inputs/1";

/// Files the payload cannot do without; the verifier refuses a manifest that omits one.
pub const REQUIRED_FILES: &[&str] = &["igneumd.exe", "igneum-miner.exe"];

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct FileEntry {
    pub sha256: String,
    pub bytes: u64,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct InputsManifest {
    pub format: String,
    /// When the zip was built, UTC, `YYYY-MM-DDTHH:MM:SSZ`.
    pub built_at: String,
    /// The node fork commit the exes were built from (40 hex), and its branch (informational).
    pub node_source_commit: String,
    pub node_source_branch: String,
    /// The main repository commit `push-inputs.sh` ran at (40 hex; informational).
    pub repo_commit: String,
    /// The zip as uploaded.
    pub zip: FileEntry,
    /// Every file inside the zip's `payload-inputs/` folder, by name.
    pub files: BTreeMap<String, FileEntry>,
}

fn is_hex(s: &str, len: usize) -> bool {
    s.len() == len && s.bytes().all(|b| b.is_ascii_hexdigit()) && s.bytes().all(|b| !b.is_ascii_uppercase())
}

fn check_entry(name: &str, e: &FileEntry) -> Result<(), String> {
    if !is_hex(&e.sha256, 64) {
        return Err(format!("{name}: sha256 is not 64 lowercase hex characters"));
    }
    if e.bytes == 0 {
        return Err(format!("{name}: bytes is 0"));
    }
    Ok(())
}

/// Parses and validates a manifest. Unknown fields, missing fields, a wrong format tag, a malformed hash or
/// commit, an empty file list or a missing required file are all refused.
pub fn parse(text: &str) -> Result<InputsManifest, String> {
    let m: InputsManifest = serde_json::from_str(text).map_err(|e| format!("inputs manifest: {e}"))?;
    if m.format != FORMAT {
        return Err(format!("inputs manifest: format is {:?}, this build understands {FORMAT:?}", m.format));
    }
    if m.built_at.len() != 20 || !m.built_at.ends_with('Z') || m.built_at.as_bytes()[10] != b'T' {
        return Err("inputs manifest: built_at is not YYYY-MM-DDTHH:MM:SSZ".into());
    }
    if !is_hex(&m.node_source_commit, 40) {
        return Err("inputs manifest: node_source_commit is not a 40-character lowercase hex commit".into());
    }
    if !is_hex(&m.repo_commit, 40) {
        return Err("inputs manifest: repo_commit is not a 40-character lowercase hex commit".into());
    }
    if m.node_source_branch.trim().is_empty() {
        return Err("inputs manifest: node_source_branch is empty".into());
    }
    check_entry("zip", &m.zip)?;
    if m.files.is_empty() {
        return Err("inputs manifest: files is empty".into());
    }
    for (name, e) in &m.files {
        if name.is_empty() || name.contains('/') || name.contains('\\') || name == "." || name == ".." {
            return Err(format!("inputs manifest: {name:?} is not a plain file name"));
        }
        check_entry(name, e)?;
    }
    for r in REQUIRED_FILES {
        if !m.files.contains_key(*r) {
            return Err(format!("inputs manifest: no {r} in files"));
        }
    }
    Ok(m)
}

/// Verifies the detached signature over the exact bytes, then parses.
pub fn verify_and_parse(bytes: &[u8], sig_hex: &str, pub_hex: &str) -> Result<InputsManifest, String> {
    verify_signature(bytes, sig_hex, pub_hex)?;
    let text = std::str::from_utf8(bytes).map_err(|_| "inputs manifest is not UTF-8")?;
    parse(text)
}

/// The zip on disk must be the one the manifest names: same sha256, same size.
pub fn check_zip(m: &InputsManifest, zip: &Path) -> Result<(), String> {
    let sum = sha256_file(zip).map_err(|e| format!("{}: {e}", zip.display()))?;
    let size = std::fs::metadata(zip).map(|md| md.len()).unwrap_or(0);
    if sum != m.zip.sha256 {
        return Err(format!("{}: sha256 {sum} is not the manifest's {}", zip.display(), m.zip.sha256));
    }
    if size != m.zip.bytes {
        return Err(format!("{}: {size} bytes, the manifest says {}", zip.display(), m.zip.bytes));
    }
    Ok(())
}

/// The unpacked folder must hold exactly the manifest's files, each with its sha256 and size. A file the manifest
/// does not name is refused too: nothing rides into the payload unsigned.
pub fn check_dir(m: &InputsManifest, dir: &Path) -> Result<(), String> {
    let mut seen = 0usize;
    let entries = std::fs::read_dir(dir).map_err(|e| format!("{}: {e}", dir.display()))?;
    for entry in entries {
        let entry = entry.map_err(|e| e.to_string())?;
        let name = entry.file_name().to_string_lossy().to_string();
        if name == ".DS_Store" {
            continue;
        }
        let Some(want) = m.files.get(&name) else {
            return Err(format!("{name}: in the folder but not in the signed manifest"));
        };
        let p = entry.path();
        let sum = sha256_file(&p).map_err(|e| format!("{name}: {e}"))?;
        let size = std::fs::metadata(&p).map(|md| md.len()).unwrap_or(0);
        if sum != want.sha256 || size != want.bytes {
            return Err(format!("{name}: sha256 {sum} ({size} bytes) is not the manifest's {} ({} bytes)", want.sha256, want.bytes));
        }
        seen += 1;
    }
    if seen != m.files.len() {
        let missing: Vec<&String> = m.files.keys().filter(|k| !dir.join(k).is_file()).collect();
        return Err(format!("the folder holds {seen} of the manifest's {} files; missing {:?}", m.files.len(), missing));
    }
    Ok(())
}

/// The commit the manifest pins must be the commit the repository expects (`packaging/windows/node-source.pin`).
pub fn check_node_commit(m: &InputsManifest, expected: &str) -> Result<(), String> {
    let expected = expected.trim();
    if !is_hex(expected, 40) {
        return Err(format!("expected node commit {expected:?} is not a 40-character lowercase hex commit"));
    }
    if m.node_source_commit != expected {
        return Err(format!("the manifest pins node commit {} but the repository expects {expected}", m.node_source_commit));
    }
    Ok(())
}

/// A signature file holds 128 hex characters and nothing else of substance.
pub fn read_signature(text: &str) -> Result<String, String> {
    let s = text.trim();
    match hex_decode(s) {
        Some(b) if b.len() == 64 => Ok(s.to_string()),
        _ => Err("signature is not 128 hex characters".into()),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::manifest::hex_encode;
    use ed25519_dalek::{Signer, SigningKey};

    const SHA: &str = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855";
    const COMMIT: &str = "6aa69a45364b9b30a32695e33eb66f100c9be85f";

    fn sample() -> String {
        format!(
            r#"{{"format":"{FORMAT}","built_at":"2026-10-04T20:07:21Z","node_source_commit":"{COMMIT}","node_source_branch":"finality-fixes","repo_commit":"{COMMIT}","zip":{{"sha256":"{SHA}","bytes":123}},"files":{{"igneumd.exe":{{"sha256":"{SHA}","bytes":1}},"igneum-miner.exe":{{"sha256":"{SHA}","bytes":2}}}}}}"#
        )
    }

    fn key() -> (SigningKey, String) {
        let sk = SigningKey::from_bytes(&[7u8; 32]);
        let pk = hex_encode(sk.verifying_key().as_bytes());
        (sk, pk)
    }

    #[test]
    fn parses_a_good_manifest() {
        let m = parse(&sample()).unwrap();
        assert_eq!(m.node_source_commit, COMMIT);
        assert_eq!(m.files.len(), 2);
        assert_eq!(m.zip.bytes, 123);
        check_node_commit(&m, COMMIT).unwrap();
        assert!(check_node_commit(&m, &COMMIT.replace('6', "7")).unwrap_err().contains("expects"));
        assert!(check_node_commit(&m, "6aa69a45").unwrap_err().contains("40-character"));
    }

    #[test]
    fn refuses_what_the_signer_would_not_sign() {
        let good = sample();
        let cases = [
            (good.replace(FORMAT, "igneum-payload-inputs/2"), "format"),
            (good.replace("\"node_source_branch\":\"finality-fixes\",", ""), "missing field"),
            (good.replace("\"zip\":", "\"extra\":1,\"zip\":"), "unknown field"),
            (good.replace(&format!("\"node_source_commit\":\"{COMMIT}\""), "\"node_source_commit\":\"6aa69a45\""), "node_source_commit"),
            (good.replace("2026-10-04T20:07:21Z", "2026-10-04 20:07:21"), "built_at"),
            (good.replace("\"bytes\":123", "\"bytes\":0"), "bytes is 0"),
            (good.replace("\"igneum-miner.exe\"", "\"igneum-miner.exe.bak\""), "no igneum-miner.exe"),
            (good.replace("\"igneumd.exe\"", "\"../igneumd.exe\""), "plain file name"),
            (good.replace(SHA, &SHA.to_uppercase()), "lowercase hex"),
        ];
        for (text, why) in cases {
            let err = parse(&text).unwrap_err();
            assert!(err.contains(why), "{why}: {err}");
        }
    }

    #[test]
    fn sign_verify_and_tamper() {
        let (sk, pk) = key();
        let bytes = sample().into_bytes();
        let sig = hex_encode(&sk.sign(&bytes).to_bytes());
        assert_eq!(read_signature(&format!("{sig}\n")).unwrap(), sig);
        assert!(read_signature("abc").is_err());
        let m = verify_and_parse(&bytes, &sig, &pk).unwrap();
        assert_eq!(m.node_source_commit, COMMIT);
        // one byte changed anywhere: the signature no longer verifies
        let mut tampered = bytes.clone();
        let i = tampered.iter().position(|b| *b == b'1').unwrap();
        tampered[i] = b'2';
        assert!(verify_and_parse(&tampered, &sig, &pk).is_err());
        // a different key: refused
        let other = hex_encode(SigningKey::from_bytes(&[9u8; 32]).verifying_key().as_bytes());
        assert!(verify_and_parse(&bytes, &sig, &other).is_err());
        // the embedded OTA key refuses a signature from this test key
        assert!(verify_and_parse(&bytes, &sig, crate::manifest::OTA_PUBLIC_KEY_HEX).is_err());
    }

    #[test]
    fn zip_and_folder_checks() {
        let dir = std::env::temp_dir().join(format!("igneum-inputs-test-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&dir);
        std::fs::create_dir_all(dir.join("unpacked")).unwrap();
        std::fs::write(dir.join("unpacked/igneumd.exe"), b"node").unwrap();
        std::fs::write(dir.join("unpacked/igneum-miner.exe"), b"miner!").unwrap();
        std::fs::write(dir.join("payload-inputs.zip"), b"zipzipzip").unwrap();
        let sha = |p: &Path| sha256_file(p).unwrap();
        let text = format!(
            r#"{{"format":"{FORMAT}","built_at":"2026-10-04T20:07:21Z","node_source_commit":"{COMMIT}","node_source_branch":"finality-fixes","repo_commit":"{COMMIT}","zip":{{"sha256":"{}","bytes":9}},"files":{{"igneumd.exe":{{"sha256":"{}","bytes":4}},"igneum-miner.exe":{{"sha256":"{}","bytes":6}}}}}}"#,
            sha(&dir.join("payload-inputs.zip")),
            sha(&dir.join("unpacked/igneumd.exe")),
            sha(&dir.join("unpacked/igneum-miner.exe"))
        );
        let m = parse(&text).unwrap();
        check_zip(&m, &dir.join("payload-inputs.zip")).unwrap();
        check_dir(&m, &dir.join("unpacked")).unwrap();
        // a changed byte in the zip
        std::fs::write(dir.join("payload-inputs.zip"), b"zipzipzip!").unwrap();
        assert!(check_zip(&m, &dir.join("payload-inputs.zip")).unwrap_err().contains("sha256"));
        // an unlisted file in the folder
        std::fs::write(dir.join("unpacked/extra.dll"), b"x").unwrap();
        assert!(check_dir(&m, &dir.join("unpacked")).unwrap_err().contains("not in the signed manifest"));
        std::fs::remove_file(dir.join("unpacked/extra.dll")).unwrap();
        // a changed file
        std::fs::write(dir.join("unpacked/igneumd.exe"), b"nodE").unwrap();
        assert!(check_dir(&m, &dir.join("unpacked")).unwrap_err().contains("igneumd.exe"));
        // a missing file
        std::fs::remove_file(dir.join("unpacked/igneumd.exe")).unwrap();
        assert!(check_dir(&m, &dir.join("unpacked")).unwrap_err().contains("missing"));
        let _ = std::fs::remove_dir_all(&dir);
    }
}
