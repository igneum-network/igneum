//! The tool end to end in a temporary directory: keygen, address, seal, receive, scan, backup, restore, rescan
//! (the restored wallet finds the same notes and the same balance), viewing-key scanning, spend-prepare.

use std::fs;
use std::path::{Path, PathBuf};
use std::process::Command;

use serde_json::Value;

fn bin() -> Command {
    Command::new(env!("CARGO_BIN_EXE_igneum-money"))
}

fn run(args: &[&str]) -> (bool, String, String) {
    let out = bin().args(args).output().unwrap();
    (out.status.success(), String::from_utf8_lossy(&out.stdout).trim().to_string(), String::from_utf8_lossy(&out.stderr).trim().to_string())
}

fn ok(args: &[&str]) -> String {
    let (good, out, err) = run(args);
    assert!(good, "{args:?}: {err}");
    out
}

fn tmp() -> PathBuf {
    let d = std::env::temp_dir().join(format!("money-cli-{}-{}", std::process::id(), std::time::SystemTime::now().duration_since(std::time::UNIX_EPOCH).unwrap().as_nanos()));
    fs::create_dir_all(&d).unwrap();
    d
}

fn p(d: &Path, n: &str) -> String {
    d.join(n).to_string_lossy().into_owned()
}

#[test]
fn the_tool_round_trips() {
    let d = tmp();
    let wallet = p(&d, "wallet.json");
    let addr = ok(&["--wallet", &wallet, "keygen", "--birthday", "1"]);
    assert!(addr.starts_with("money1"));
    assert_eq!(ok(&["--wallet", &wallet, "address"]), addr);
    let (good, _, err) = run(&["--wallet", &wallet, "keygen", "--birthday", "1"]);
    assert!(!good && err.contains("exists"), "keygen never overwrites: {err}");
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        assert_eq!(fs::metadata(&wallet).unwrap().permissions().mode() & 0o777, 0o600);
    }

    // a stranger's wallet for decoy outputs
    let other = p(&d, "other.json");
    let other_addr = ok(&["--wallet", &other, "keygen", "--birthday", "0"]);

    // seal three notes to us and two to the stranger; one output per line in a block stream
    let mine = [1_000u128, 2_500, 42];
    let mut lines = Vec::new();
    let mut outputs = Vec::new();
    for (i, v) in mine.iter().enumerate() {
        let o = ok(&["seal", "--to", &addr, "--value", &v.to_string(), "--memo", &format!("note {i}")]);
        assert_eq!(o.len(), 1325 * 2);
        outputs.push(o);
        outputs.push(ok(&["seal", "--to", &other_addr, "--value", "7"]));
    }
    // receive the first directly
    let r = ok(&["--wallet", &wallet, "receive", "--output", &outputs[0], "--position", "0"]);
    let rv: Value = serde_json::from_str(&r).unwrap();
    assert_eq!(rv["value"], "1000");
    assert_eq!(rv["memo"], "note 0");
    let nf0 = rv["nullifier"].as_str().unwrap().to_string();
    assert_eq!(ok(&["--wallet", &wallet, "receive", "--output", &outputs[1]]), "not this wallet's");

    // blocks: 1 carries outputs 0..3, block 2 carries 3..6 and spends our first note; block 0 is below the
    // birthday and is skipped by its position
    lines.push(r#"{"height":0,"position":0,"outputs":[],"nullifiers":[]}"#.to_string());
    lines.push(format!(r#"{{"height":1,"position":0,"outputs":["{}","{}","{}"],"nullifiers":[]}}"#, outputs[0], outputs[1], outputs[2]));
    lines.push(format!(r#"{{"height":2,"position":3,"outputs":["{}","{}","{}"],"nullifiers":["{}"]}}"#, outputs[3], outputs[4], outputs[5], nf0));
    let blocks = p(&d, "blocks.jsonl");
    fs::write(&blocks, lines.join("\n") + "\n").unwrap();
    let notes = p(&d, "notes.json");
    let scan = ok(&["--wallet", &wallet, "scan", "--blocks", &blocks, "--notes-out", &notes]);
    let sv: Value = serde_json::from_str(&scan).unwrap();
    assert_eq!(sv["notes"], 3);
    assert_eq!(sv["spent_notes"], 1);
    assert_eq!(sv["unspent"], "2542");
    assert_eq!(sv["spent"], "1000");
    assert_eq!(sv["outputs_tried"], 6);
    assert_eq!(sv["skipped_blocks"], 1);

    // the viewing key scans the same and cannot spend
    let vk = p(&d, "view.hex");
    ok(&["--wallet", &wallet, "viewing-key", "--out", &vk]);
    let scan_vk = ok(&["--viewing-key", &vk, "scan", "--blocks", &blocks]);
    let vv: Value = serde_json::from_str(&scan_vk).unwrap();
    assert_eq!(vv["unspent"], "2542");
    let (good, _, _) = run(&["--viewing-key", &vk, "spend-prepare", "--notes", &notes, "--positions", "2", "--to", &other_addr, "--value", "1", "--chain-id", "7402", "--witness-out", &p(&d, "w0.json"), "--record-out", &p(&d, "r0.json")]);
    assert!(!good);

    // backup and restore into a new file; the restored wallet rescans to the same notes
    let words = ok(&["--wallet", &wallet, "backup"]);
    assert_eq!(words.split(' ').count(), 24);
    let words_file = p(&d, "words.txt");
    fs::write(&words_file, &words).unwrap();
    let restored = p(&d, "restored.json");
    assert_eq!(ok(&["--wallet", &restored, "restore", "--words-file", &words_file, "--birthday", "1"]), addr);
    let notes2 = p(&d, "notes2.json");
    let scan2 = ok(&["--wallet", &restored, "scan", "--blocks", &blocks, "--notes-out", &notes2]);
    let s2: Value = serde_json::from_str(&scan2).unwrap();
    assert_eq!(s2["unspent"], sv["unspent"]);
    assert_eq!(s2["spent"], sv["spent"]);
    let n1: Value = serde_json::from_str(&fs::read_to_string(&notes).unwrap()).unwrap();
    let n2: Value = serde_json::from_str(&fs::read_to_string(&notes2).unwrap()).unwrap();
    assert_eq!(n1, n2);

    // spend-prepare on an unspent note writes a checked witness and a record; on the spent one it refuses
    let wit = p(&d, "witness.json");
    let rec = p(&d, "record.json");
    ok(&["--wallet", &restored, "spend-prepare", "--notes", &notes2, "--positions", "2", "--to", &other_addr, "--value", "2000", "--fee", "7", "--public-value", "100", "--chain-id", "7402", "--witness-out", &wit, "--record-out", &rec]);
    let wv: Value = serde_json::from_str(&fs::read_to_string(&wit).unwrap()).unwrap();
    assert_eq!(wv["statement"], "money-spend-v1");
    assert_eq!(wv["inputs"][0]["value"], "2500");
    assert_eq!(wv["inputs"][1]["value"], "0");
    assert_eq!(wv["inputs"][0]["path"].as_array().unwrap().len(), 32);
    assert_eq!(wv["outputs"][0]["value"], "2000");
    assert_eq!(wv["outputs"][1]["value"], "393");
    let rv: Value = serde_json::from_str(&fs::read_to_string(&rec).unwrap()).unwrap();
    assert_eq!(rv["path_placeholder"], true);
    assert_eq!(rv["change"], "393");
    assert_eq!(rv["outputs"][1]["ciphertext"].as_str().unwrap().len(), 1293 * 2);
    // the recipient opens output 0 of the record
    let o0 = format!("{}{}", rv["outputs"][0]["commitment"].as_str().unwrap(), rv["outputs"][0]["ciphertext"].as_str().unwrap());
    let r0: Value = serde_json::from_str(&ok(&["--wallet", &other, "receive", "--output", &o0])).unwrap();
    assert_eq!(r0["value"], "2000");
    let (good, _, err) = run(&["--wallet", &restored, "spend-prepare", "--notes", &notes2, "--positions", "0", "--to", &other_addr, "--value", "1", "--chain-id", "7402", "--witness-out", &p(&d, "w1.json"), "--record-out", &p(&d, "r1.json")]);
    assert!(!good && err.contains("spent"), "{err}");
    // a shield's witness and record
    let sw = p(&d, "shield-w.json");
    let sr = p(&d, "shield-r.json");
    let out = ok(&["--wallet", &wallet, "shield-prepare", "--value", "123456789012345678901", "--chain-id", "7402", "--witness-out", &sw, "--record-out", &sr]);
    let sv2: Value = serde_json::from_str(&out).unwrap();
    assert_eq!(sv2["value"], "123456789012345678901");
    let srv: Value = serde_json::from_str(&fs::read_to_string(&sr).unwrap()).unwrap();
    let so = format!("{}{}", srv["commitment"].as_str().unwrap(), srv["ciphertext"].as_str().unwrap());
    let rs: Value = serde_json::from_str(&ok(&["--wallet", &wallet, "receive", "--output", &so, "--position", "9"])).unwrap();
    assert_eq!(rs["value"], "123456789012345678901");

    // a wrong word is refused
    let mut ws: Vec<&str> = words.split(' ').collect();
    ws[0] = "zoo";
    ws[1] = "zoo";
    fs::write(&words_file, ws.join(" ")).unwrap();
    let (good, _, _) = run(&["--wallet", &p(&d, "bad.json"), "restore", "--words-file", &words_file]);
    assert!(!good);

    fs::remove_dir_all(&d).unwrap();
}

/// F-776: `restore --birthday H` and `backup` say that a birthday later than the true one hides the notes before
/// it and that a doubtful birthday is given as zero.
#[test]
fn the_birthday_words_are_said() {
    let d = tmp();
    let wallet = p(&d, "w.json");
    ok(&["--wallet", &wallet, "keygen", "--birthday", "5"]);
    let words_file = p(&d, "words.txt");
    fs::write(&words_file, ok(&["--wallet", &wallet, "backup"])).unwrap();
    let (good, _, err) = run(&["--wallet", &wallet, "backup"]);
    assert!(good);
    assert!(err.contains("a birthday later than the seed's true creation height hides every note before it"), "backup: {err}");
    assert!(err.contains("if the birthday is in doubt, give it as zero"), "backup: {err}");
    let restored = p(&d, "r.json");
    let (good, _, err) = run(&["--wallet", &restored, "restore", "--words-file", &words_file, "--birthday", "9"]);
    assert!(good, "{err}");
    assert!(err.contains("a birthday later than the seed's true creation height hides every note before it"), "restore: {err}");
    assert!(err.contains("if the birthday is in doubt, give it as zero"), "restore: {err}");
    let restored0 = p(&d, "r0.json");
    let (good, _, err) = run(&["--wallet", &restored0, "restore", "--words-file", &words_file]);
    assert!(good, "{err}");
    assert!(err.contains("scans from genesis"), "restore without a birthday: {err}");
    fs::remove_dir_all(&d).unwrap();
}
