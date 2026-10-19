//! igneum-ota-sign: the publisher's side of the update manifest. Built with the app (cargo build) but never shipped.
//! It includes src/manifest.rs as is, so the bytes it signs are verified by the same code the app runs.
//!
//!   igneum-ota-sign keygen <private-key-file> <public-key-file>   32-byte seed as hex (0600) and the public key as hex
//!   igneum-ota-sign sign <private-key-file> <manifest.json>       prints the detached signature (128 hex)
//!   igneum-ota-sign verify <public-key-file|hex> <manifest.json> <sig-file>   exit 0 when it verifies and parses
//!   igneum-ota-sign embedded                                       prints the public key compiled into the app and its fingerprint
//!   igneum-ota-sign fingerprint <public-key-file|hex>
//!   igneum-ota-sign sha256 <file>                                  the file's sha256 and size, for the manifest
//!   igneum-ota-sign sign-ui <private-key-file> <version> <sha256> <min-engine>    the interface entry's own signature
//!                   (src/manifest.rs ui_sign_bytes; tools/ui-ota/publish.mjs calls this), same key
//!   igneum-ota-sign verify-ui <public-key-file|hex|embedded> <version> <sha256> <min-engine> <signature-hex>
//!   igneum-ota-sign sign-jobs <private-key-file> <igneum-jobs.json>    the remote-jobs file (src/jobs.rs), same key
//!   igneum-ota-sign verify-jobs <public-key-file|hex> <igneum-jobs.json> <sig-file>
//!   igneum-ota-sign envelope-jobs <public-key-file|hex> <igneum-jobs.json> <sig-file>   prints igneum-jobs.signed.json:
//!                   the file and its signature in ONE object (src/jobs.rs), refused when the pair does not verify
//!   igneum-ota-sign verify-signed-jobs <public-key-file|hex> <igneum-jobs.signed.json>
//!   igneum-ota-sign sign-inputs <private-key-file> <payload-inputs.json>    the Windows build inputs (src/inputs.rs), same key
//!   igneum-ota-sign verify-inputs <public-key-file|hex|embedded> <payload-inputs.json> <sig-file>
//!                   [--zip <payload-inputs.zip>] [--dir <unpacked folder>] [--node-commit <40 hex>]
//!                                                                  exit 0 only when the signature, the zip, every
//!                                                                  unpacked file and the pinned commit all check

#[allow(dead_code)]
#[path = "../drivertable.rs"]
mod drivertable;
#[path = "../manifest.rs"]
mod manifest;
#[path = "../jobs.rs"]
mod jobs;
#[path = "../inputs.rs"]
mod inputs;

use ed25519_dalek::{Signer, SigningKey};
use std::path::Path;

fn read_key_arg(a: &str) -> String {
    if Path::new(a).is_file() {
        std::fs::read_to_string(a).unwrap_or_default().trim().to_string()
    } else {
        a.trim().to_string()
    }
}

fn die(msg: &str) -> ! {
    eprintln!("igneum-ota-sign: {msg}");
    std::process::exit(2)
}

fn main() {
    let args: Vec<String> = std::env::args().skip(1).collect();
    match args.first().map(|s| s.as_str()) {
        Some("keygen") if args.len() == 3 => {
            if Path::new(&args[1]).exists() {
                die(&format!("{} exists; not overwriting a signing key", args[1]));
            }
            let mut seed = [0u8; 32];
            getrandom::getrandom(&mut seed).expect("os randomness");
            let sk = SigningKey::from_bytes(&seed);
            let pk = manifest::hex_encode(sk.verifying_key().as_bytes());
            if let Some(d) = Path::new(&args[1]).parent() {
                let _ = std::fs::create_dir_all(d);
            }
            std::fs::write(&args[1], format!("{}\n", manifest::hex_encode(&seed))).unwrap_or_else(|e| die(&e.to_string()));
            #[cfg(unix)]
            {
                use std::os::unix::fs::PermissionsExt;
                let _ = std::fs::set_permissions(&args[1], std::fs::Permissions::from_mode(0o600));
            }
            std::fs::write(&args[2], format!("{pk}\n")).unwrap_or_else(|e| die(&e.to_string()));
            println!("public key {pk}");
            println!("fingerprint sha256:{}", manifest::fingerprint(&pk));
        }
        Some("sign") if args.len() == 3 => {
            let seed = manifest::hex_decode(&read_key_arg(&args[1])).unwrap_or_else(|| die("private key is not hex"));
            let seed: [u8; 32] = seed.try_into().unwrap_or_else(|_| die("private key is not 32 bytes"));
            let sk = SigningKey::from_bytes(&seed);
            let bytes = std::fs::read(&args[2]).unwrap_or_else(|e| die(&format!("{}: {e}", args[2])));
            let text = std::str::from_utf8(&bytes).unwrap_or_else(|_| die("manifest is not UTF-8"));
            manifest::parse(text).unwrap_or_else(|e| die(&format!("refusing to sign: {e}")));
            println!("{}", manifest::hex_encode(&sk.sign(&bytes).to_bytes()));
        }
        Some("verify") if args.len() == 4 => {
            let pk = read_key_arg(&args[1]);
            let bytes = std::fs::read(&args[2]).unwrap_or_else(|e| die(&format!("{}: {e}", args[2])));
            let sig = std::fs::read_to_string(&args[3]).unwrap_or_else(|e| die(&format!("{}: {e}", args[3])));
            match manifest::verify_and_parse(&bytes, sig.trim(), &pk) {
                Ok(m) => println!("ok: version {} ({}), mac {}, windows {}", m.version, m.channel, m.mac.map(|e| e.url).unwrap_or_else(|| "none".into()), m.windows.map(|e| e.url).unwrap_or_else(|| "none".into())),
                Err(e) => die(&e),
            }
        }
        Some("embedded") if args.len() == 1 => {
            println!("{}", manifest::OTA_PUBLIC_KEY_HEX);
            println!("fingerprint sha256:{}", manifest::fingerprint(manifest::OTA_PUBLIC_KEY_HEX));
        }
        Some("fingerprint") if args.len() == 2 => {
            let pk = read_key_arg(&args[1]);
            if manifest::public_key(&pk).is_err() {
                die("not a public key");
            }
            println!("{pk}");
            println!("fingerprint sha256:{}", manifest::fingerprint(&pk));
        }
        Some("sha256") if args.len() == 2 => {
            let p = Path::new(&args[1]);
            let sum = manifest::sha256_file(p).unwrap_or_else(|e| die(&format!("{}: {e}", args[1])));
            let size = std::fs::metadata(p).map(|m| m.len()).unwrap_or(0);
            println!("{sum} {size}");
        }
        Some("sign-ui") if args.len() == 5 => {
            let seed = manifest::hex_decode(&read_key_arg(&args[1])).unwrap_or_else(|| die("private key is not hex"));
            let seed: [u8; 32] = seed.try_into().unwrap_or_else(|_| die("private key is not 32 bytes"));
            let sk = SigningKey::from_bytes(&seed);
            if manifest::parse_version(&args[2]).is_none() || manifest::parse_version(&args[4]).is_none() {
                die("the version and the engine floor are versions like 0.1.19 or 0.1.19.1");
            }
            if args[3].len() != 64 || !args[3].chars().all(|c| c.is_ascii_hexdigit()) {
                die("the sha256 is 64 hex characters");
            }
            println!("{}", manifest::hex_encode(&sk.sign(&manifest::ui_sign_bytes(&args[2], &args[3], &args[4])).to_bytes()));
        }
        Some("verify-ui") if args.len() == 6 => {
            let pk = if args[1] == "embedded" { manifest::OTA_PUBLIC_KEY_HEX.to_string() } else { read_key_arg(&args[1]) };
            let e = manifest::UiEntry { version: args[2].clone(), sha256: args[3].to_ascii_lowercase(), size: 1, url: "https://x".into(), min_engine: args[4].clone(), signature: args[5].to_ascii_lowercase() };
            match manifest::verify_ui_entry(&e, &pk) {
                Ok(()) => println!("verifies"),
                Err(e) => die(&e),
            }
        }
        Some("sign-jobs") if args.len() == 3 => {
            let seed = manifest::hex_decode(&read_key_arg(&args[1])).unwrap_or_else(|| die("private key is not hex"));
            let seed: [u8; 32] = seed.try_into().unwrap_or_else(|_| die("private key is not 32 bytes"));
            let sk = SigningKey::from_bytes(&seed);
            let bytes = std::fs::read(&args[2]).unwrap_or_else(|e| die(&format!("{}: {e}", args[2])));
            let text = std::str::from_utf8(&bytes).unwrap_or_else(|_| die("jobs file is not UTF-8"));
            let f = jobs::parse(text).unwrap_or_else(|e| die(&format!("refusing to sign: {e}")));
            eprintln!("signing {} job(s): {}", f.jobs.len(), f.jobs.iter().map(|j| format!("{} ({})", j.id, j.kind)).collect::<Vec<_>>().join(", "));
            println!("{}", manifest::hex_encode(&sk.sign(&bytes).to_bytes()));
        }
        Some("verify-jobs") if args.len() == 4 => {
            let pk = read_key_arg(&args[1]);
            let bytes = std::fs::read(&args[2]).unwrap_or_else(|e| die(&format!("{}: {e}", args[2])));
            let sig = std::fs::read_to_string(&args[3]).unwrap_or_else(|e| die(&format!("{}: {e}", args[3])));
            match jobs::verify_and_parse(&bytes, sig.trim(), &pk) {
                Ok(f) => {
                    println!("ok: {} job(s), published {}", f.jobs.len(), f.published_at);
                    for j in &f.jobs {
                        println!("  {} {} to {} on {} until {}{}: {}", j.id, j.kind, if j.target.all { "all".to_string() } else { j.target.machine_ids.join(",") }, j.target.platform, j.expires_at, if j.target.requires.is_empty() { String::new() } else { format!(" needs {}", j.target.requires.join(",")) }, j.label());
                    }
                }
                Err(e) => die(&e),
            }
        }
        Some("envelope-jobs") if args.len() == 4 => {
            let pk = read_key_arg(&args[1]);
            let bytes = std::fs::read(&args[2]).unwrap_or_else(|e| die(&format!("{}: {e}", args[2])));
            let sig = std::fs::read_to_string(&args[3]).unwrap_or_else(|e| die(&format!("{}: {e}", args[3])));
            jobs::verify_and_parse(&bytes, sig.trim(), &pk).unwrap_or_else(|e| die(&format!("refusing to wrap: {e}")));
            let env = jobs::signed_envelope(&bytes, sig.trim(), &pk).unwrap_or_else(|e| die(&e));
            jobs::verify_and_parse_signed(env.as_bytes(), &pk).unwrap_or_else(|e| die(&format!("the envelope does not read back: {e}")));
            println!("{env}");
        }
        Some("verify-signed-jobs") if args.len() == 3 => {
            let pk = read_key_arg(&args[1]);
            let bytes = std::fs::read(&args[2]).unwrap_or_else(|e| die(&format!("{}: {e}", args[2])));
            match jobs::verify_and_parse_signed(&bytes, &pk) {
                Ok(f) => println!("ok: {} job(s), published {}, file and signature in one object", f.jobs.len(), f.published_at),
                Err(e) => die(&e),
            }
        }
        Some("sign-inputs") if args.len() == 3 => {
            let seed = manifest::hex_decode(&read_key_arg(&args[1])).unwrap_or_else(|| die("private key is not hex"));
            let seed: [u8; 32] = seed.try_into().unwrap_or_else(|_| die("private key is not 32 bytes"));
            let sk = SigningKey::from_bytes(&seed);
            let bytes = std::fs::read(&args[2]).unwrap_or_else(|e| die(&format!("{}: {e}", args[2])));
            let text = std::str::from_utf8(&bytes).unwrap_or_else(|_| die("inputs manifest is not UTF-8"));
            let m = inputs::parse(text).unwrap_or_else(|e| die(&format!("refusing to sign: {e}")));
            eprintln!(
                "signing inputs built {} from node commit {} ({}): zip {} bytes, {} file(s)",
                m.built_at,
                &m.node_source_commit[..12],
                m.node_source_branch,
                m.zip.bytes,
                m.files.len()
            );
            println!("{}", manifest::hex_encode(&sk.sign(&bytes).to_bytes()));
        }
        Some("verify-inputs") if args.len() >= 4 => {
            let pk = if args[1] == "embedded" { manifest::OTA_PUBLIC_KEY_HEX.to_string() } else { read_key_arg(&args[1]) };
            let bytes = std::fs::read(&args[2]).unwrap_or_else(|e| die(&format!("{}: {e}", args[2])));
            let sig_text = std::fs::read_to_string(&args[3]).unwrap_or_else(|e| die(&format!("{}: {e}", args[3])));
            let sig = inputs::read_signature(&sig_text).unwrap_or_else(|e| die(&e));
            let m = inputs::verify_and_parse(&bytes, &sig, &pk).unwrap_or_else(|e| die(&format!("inputs signature: {e}")));
            let mut i = 4;
            let mut checked: Vec<String> = vec![format!("signature by {}", manifest::fingerprint(&pk))];
            while i < args.len() {
                match (args[i].as_str(), args.get(i + 1)) {
                    ("--zip", Some(z)) => {
                        inputs::check_zip(&m, Path::new(z)).unwrap_or_else(|e| die(&e));
                        checked.push(format!("zip {} ({} bytes)", m.zip.sha256, m.zip.bytes));
                    }
                    ("--dir", Some(d)) => {
                        inputs::check_dir(&m, Path::new(d)).unwrap_or_else(|e| die(&e));
                        checked.push(format!("{} unpacked file(s)", m.files.len()));
                    }
                    ("--node-commit", Some(c)) => {
                        let c = if Path::new(c).is_file() { std::fs::read_to_string(c).unwrap_or_default() } else { c.to_string() };
                        inputs::check_node_commit(&m, &c).unwrap_or_else(|e| die(&e));
                        checked.push(format!("node commit {}", m.node_source_commit));
                    }
                    (flag, _) => die(&format!("unknown or incomplete argument {flag}")),
                }
                i += 2;
            }
            println!("ok: inputs built {} from node commit {} ({}); checked: {}", m.built_at, m.node_source_commit, m.node_source_branch, checked.join(", "));
        }
        _ => {
            eprintln!("usage: igneum-ota-sign keygen <priv> <pub> | sign <priv> <manifest.json> | verify <pub> <manifest.json> <sig> | embedded | fingerprint <pub> | sha256 <file> | sign-jobs <priv> <jobs.json> | verify-jobs <pub> <jobs.json> <sig> | envelope-jobs <pub> <jobs.json> <sig> | verify-signed-jobs <pub> <jobs.signed.json> | sign-inputs <priv> <payload-inputs.json> | verify-inputs <pub|embedded> <payload-inputs.json> <sig> [--zip z] [--dir d] [--node-commit c]");
            std::process::exit(2);
        }
    }
}
