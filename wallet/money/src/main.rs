//! `igneum-money`: the Money shielded wallet on the command line, independent of the node. The wallet file is
//! `{"seed": hex, "birthday": height}` at mode 0600; the tool never prints the seed, the spending secret or the
//! viewing key to the terminal: `backup` prints the words (its purpose), `viewing-key`, `shield-prepare` and
//! `spend-prepare` write files the person names. Blocks come in as JSON lines (`{"height": n, "position": p,
//! "outputs": [hex...], "nullifiers": [hex...]}`, one output 1,325 bytes of hex: the commitment then the
//! ciphertext, as igneum_poolBlock serves them), and a scan starts at the wallet's birthday, never at genesis
//! unless the birthday is unknown.

use std::collections::BTreeMap;
use std::fs;
use std::io::{self, Read, Write};
use std::path::{Path, PathBuf};
use std::time::Instant;

use clap::{Parser, Subcommand};
use serde_json::{json, Value};
use zeroize::Zeroizing;

use igneum_wallet_money::backup;
use igneum_wallet_money::hash::{CircuitHash, H};
use igneum_wallet_money::hex;
use igneum_wallet_money::keys::{Address, FullViewingKey, Seed, Wallet};
use igneum_wallet_money::note::{self, Output};
use igneum_wallet_money::scan::{Block, Mint, OwnedNote, Scanner};
use igneum_wallet_money::sizes;
use igneum_wallet_money::vault;
use igneum_wallet_money::witness::{self, SpendInput, SpendPlan};
use igneum_wallet_money::{Error, Result};

#[derive(Parser)]
#[command(name = "igneum-money", about = "Igneum Wallet, the Money shielded side, on the command line", version)]
struct Cli {
    /// the wallet file ({"seed": hex, "birthday": height}, mode 0600)
    #[arg(long, global = true)]
    wallet: Option<PathBuf>,
    /// a full viewing key file (hex) in place of the wallet: scans and receives, cannot spend
    #[arg(long, global = true)]
    viewing_key: Option<PathBuf>,
    #[command(subcommand)]
    cmd: Cmd,
}

#[derive(Subcommand)]
enum Cmd {
    /// a new seed from the operating system's randomness into --wallet; prints the address
    Keygen {
        /// the chain height at creation: a restore scans from it, never from genesis
        #[arg(long)]
        birthday: u64,
    },
    /// the address (text form) of --wallet or --viewing-key
    /// the address of --wallet (or --viewing-key): the text form; with --raw the 1,227 address bytes as hex
    /// (pk, ek, d), the form a sealer registers as its shielded payout address (the pool's registration record)
    Address {
        #[arg(long)]
        raw: bool,
    },
    /// the full viewing key of --wallet, written to --out (hex, mode 0600)
    ViewingKey {
        #[arg(long)]
        out: PathBuf,
    },
    /// the 24 backup words of --wallet, printed, with the birthday height after them
    Backup,
    /// --wallet written from 24 words read from --words-file or standard input
    Restore {
        #[arg(long)]
        words_file: Option<PathBuf>,
        /// the seed's birthday height (kept beside the words); without it the wallet scans from genesis; a birthday later than the true one hides the notes before it, so give 0 if in doubt
        #[arg(long)]
        birthday: Option<u64>,
    },
    /// the sender's side: seal a note to an address; prints the output's hex (1,325 bytes)
    Seal {
        #[arg(long)]
        to: String,
        #[arg(long)]
        value: u128,
        #[arg(long, default_value = "")]
        memo: String,
    },
    /// trial-decrypt one output with --wallet or --viewing-key; prints the note or "not this wallet's"
    Receive {
        /// the output's hex, or @file
        #[arg(long)]
        output: String,
        /// the output's position in the commitment tree, for the nullifier
        #[arg(long)]
        position: Option<u64>,
    },
    /// scan a block stream (JSON lines) with --wallet or --viewing-key from the birthday; prints the balance
    Scan {
        #[arg(long)]
        blocks: PathBuf,
        /// the first height to read (default: the wallet's birthday; 0 reads everything)
        #[arg(long)]
        from_height: Option<u64>,
        #[arg(long)]
        notes_out: Option<PathBuf>,
    },
    /// a shield's witness and record: one note of --value sealed to --to (default: this wallet's address)
    ShieldPrepare {
        #[arg(long)]
        to: Option<String>,
        #[arg(long)]
        value: u128,
        #[arg(long, default_value = "")]
        memo: String,
        #[arg(long)]
        chain_id: u64,
        #[arg(long)]
        witness_out: PathBuf,
        #[arg(long)]
        record_out: PathBuf,
    },
    /// a spend's witness and record from one or two of this wallet's notes (by position) in a notes file
    SpendPrepare {
        #[arg(long)]
        notes: PathBuf,
        /// one or two positions, comma-separated
        #[arg(long)]
        positions: String,
        /// the paths file the node served ({"anchor": hex, "paths": {"<position>": [32 hex siblings]}});
        /// without it the witness carries a placeholder path the prover refuses
        #[arg(long)]
        paths: Option<PathBuf>,
        #[arg(long)]
        to: String,
        #[arg(long)]
        value: u128,
        #[arg(long, default_value = "")]
        memo: String,
        #[arg(long, default_value_t = 0)]
        fee: u128,
        #[arg(long, default_value_t = 0)]
        public_value: u128,
        /// the 20-byte public output (hex), paid the public value
        #[arg(long, default_value = "0000000000000000000000000000000000000000")]
        public_output: String,
        #[arg(long)]
        chain_id: u64,
        #[arg(long)]
        witness_out: PathBuf,
        #[arg(long)]
        record_out: PathBuf,
    },
    /// the scan cost: microseconds a ciphertext on this machine, one core, and the year-of-blocks figures
    BenchScan {
        #[arg(long, default_value_t = 20000)]
        n: usize,
    },
    /// the derivation vectors for a given seed (test vectors only; a real seed never goes on a command line)
    #[command(hide = true)]
    Vectors {
        #[arg(long)]
        seed_hex: String,
    },
    /// the sizes the construction fixes
    Sizes,
    /// a fresh 32-byte secret for a cancellation key or a recovery key, written to --out (hex, mode 0600);
    /// prints its two commitments (as a cancellation key, as a recovery authority)
    VaultKeygen {
        #[arg(long)]
        out: PathBuf,
    },
    /// a vault from one or two of this wallet's notes: the witness, the record and the vault file (the vault's
    /// viewing key and state, mode 0600; a copy given to the cancellation key's holder reads and cannot spend)
    VaultCreate {
        #[arg(long)]
        notes: PathBuf,
        #[arg(long)]
        positions: String,
        #[arg(long)]
        paths: PathBuf,
        /// the amount into the vault; the rest of the inputs returns as change
        #[arg(long)]
        value: u128,
        #[arg(long, default_value_t = 7200)]
        delay_blocks: u64,
        #[arg(long, default_value_t = 86400)]
        delay_seconds: u64,
        #[arg(long, default_value_t = 7200)]
        period: u64,
        /// the most that may be requested in one period, in wei
        #[arg(long)]
        limit: u128,
        #[arg(long, default_value_t = 21600)]
        recovery_blocks: u64,
        #[arg(long, default_value_t = 259200)]
        recovery_seconds: u64,
        /// the cancellation key's commitment (vault-keygen's "cancel"), or all zero for none
        #[arg(long)]
        cancel_pk: String,
        /// the recovery authority's commitment (vault-keygen's "recover"), or all zero for none
        #[arg(long)]
        recovery_pk: String,
        #[arg(long)]
        chain_id: u64,
        #[arg(long)]
        witness_out: PathBuf,
        #[arg(long)]
        record_out: PathBuf,
        #[arg(long)]
        vault_out: PathBuf,
    },
    /// scan a block stream for the vault (its heads, pending and inbound notes, the nullifiers and position
    /// tags seen); the vault file is rewritten; prints the state
    VaultScan {
        #[arg(long)]
        vault: PathBuf,
        #[arg(long)]
        blocks: PathBuf,
    },
    /// a withdrawal request by the everyday key: the pending note to --to matures after the policy's delays
    VaultRequest {
        #[arg(long)]
        vault: PathBuf,
        #[arg(long)]
        paths: PathBuf,
        #[arg(long)]
        to: String,
        #[arg(long)]
        value: u128,
        /// the declared height (at or after the including block, within 64 of it)
        #[arg(long)]
        height: u64,
        /// the declared time (at or after the including block's, within 768 seconds of it)
        #[arg(long)]
        time: u64,
        #[arg(long)]
        witness_out: PathBuf,
        #[arg(long)]
        record_out: PathBuf,
        /// the pending note's fields for the destination (what it needs to complete), mode 0600
        #[arg(long)]
        receipt_out: PathBuf,
    },
    /// the cancellation key consumes a pending note (by its commitment) back into the vault as an inbound note
    VaultCancel {
        #[arg(long)]
        vault: PathBuf,
        /// the cancellation key's secret file (vault-keygen)
        #[arg(long)]
        key: PathBuf,
        #[arg(long)]
        pending_cm: String,
        #[arg(long)]
        paths: PathBuf,
        #[arg(long)]
        witness_out: PathBuf,
        #[arg(long)]
        record_out: PathBuf,
    },
    /// the destination wallet completes a matured pending note (its receipt) into its own notes
    VaultComplete {
        #[arg(long)]
        receipt: PathBuf,
        #[arg(long)]
        paths: PathBuf,
        /// the revoked set's witness as igneum_poolRevokedPath served it (JSON)
        #[arg(long)]
        revoked: PathBuf,
        #[arg(long)]
        height: u64,
        #[arg(long)]
        time: u64,
        #[arg(long)]
        chain_id: u64,
        #[arg(long)]
        witness_out: PathBuf,
        #[arg(long)]
        record_out: PathBuf,
    },
    /// a new policy for an announcement, under --wallet as the new everyday key; written to --out (mode 0600:
    /// it carries the new wallet's vault nullifier key)
    VaultNewPolicy {
        #[arg(long)]
        vault: PathBuf,
        #[arg(long, default_value_t = 7200)]
        delay_blocks: u64,
        #[arg(long, default_value_t = 86400)]
        delay_seconds: u64,
        #[arg(long, default_value_t = 7200)]
        period: u64,
        #[arg(long)]
        limit: u128,
        #[arg(long, default_value_t = 21600)]
        recovery_blocks: u64,
        #[arg(long, default_value_t = 259200)]
        recovery_seconds: u64,
        #[arg(long)]
        cancel_pk: String,
        #[arg(long)]
        recovery_pk: String,
        #[arg(long)]
        out: PathBuf,
    },
    /// the recovery authority announces a new policy (vault-new-policy's file); it applies after the recovery
    /// delay unless cancelled; with --wallet (the everyday wallet) the announcement is co-signed, so a rotation
    /// of the cancellation key is the recovery authority's alone to cancel (F-800)
    /// delay unless the cancellation key cancels it
    VaultAnnounce {
        #[arg(long)]
        vault: PathBuf,
        /// the recovery key's secret file
        #[arg(long)]
        key: PathBuf,
        #[arg(long)]
        new_policy: PathBuf,
        #[arg(long)]
        paths: PathBuf,
        #[arg(long)]
        height: u64,
        #[arg(long)]
        time: u64,
        #[arg(long)]
        witness_out: PathBuf,
        #[arg(long)]
        record_out: PathBuf,
    },
    /// the announced policy replaces the old one after the recovery delay (anyone holding the vault file and the
    /// new policy's file); the vault file's policy is replaced
    VaultPolicyComplete {
        #[arg(long)]
        vault: PathBuf,
        #[arg(long)]
        new_policy: PathBuf,
        #[arg(long)]
        paths: PathBuf,
        #[arg(long)]
        height: u64,
        #[arg(long)]
        time: u64,
        #[arg(long)]
        witness_out: PathBuf,
        #[arg(long)]
        record_out: PathBuf,
    },
    /// the cancellation key clears a pending recovery
    VaultRecCancel {
        #[arg(long)]
        vault: PathBuf,
        /// the cancellation key's secret file, or the recovery authority's when the announced policy changes
        /// the cancellation key (F-800)
        #[arg(long)]
        key: PathBuf,
        #[arg(long)]
        paths: PathBuf,
        /// skip the key check (the lab's attacker form; the prover refuses the witness)
        #[arg(long, default_value_t = false)]
        force: bool,
        #[arg(long)]
        witness_out: PathBuf,
        #[arg(long)]
        record_out: PathBuf,
    },
}

fn main() {
    let cli = Cli::parse();
    if let Err(e) = run(cli) {
        eprintln!("igneum-money: {e}");
        std::process::exit(1);
    }
}

fn write_private(path: &Path, bytes: &[u8]) -> Result<()> {
    let mut opts = fs::OpenOptions::new();
    opts.write(true).create_new(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        opts.mode(0o600);
    }
    let mut f = opts.open(path).map_err(|e| Error(format!("{}: {e}", path.display())))?;
    f.write_all(bytes).map_err(|e| Error(format!("{}: {e}", path.display())))
}

/// F-776, the ADR's section 8 honesty rule: what a birthday later than the true one costs, and what to do when it is in doubt.
const BIRTHDAY_WARNING: &str = "a birthday later than the seed's true creation height hides every note before it; if the birthday is in doubt, give it as zero";

fn write_wallet(path: &Path, w: &Wallet, birthday: Option<u64>) -> Result<()> {
    let text = Zeroizing::new(json!({ "seed": hex::encode(w.seed().as_bytes()), "kind": "money-stage1", "birthday": birthday }).to_string());
    write_private(path, text.as_bytes())
}

/// The wallet and its birthday (None when the file carries none: a restore without one).
fn load_wallet(path: &Option<PathBuf>) -> Result<(Wallet, Option<u64>)> {
    let path = path.as_ref().ok_or_else(|| Error("--wallet is needed".into()))?;
    let text = Zeroizing::new(fs::read_to_string(path).map_err(|e| Error(format!("{}: {e}", path.display())))?);
    let v: Value = serde_json::from_str(&text).map_err(|e| Error(format!("wallet file: {e}")))?;
    let seed_hex = v.get("seed").and_then(Value::as_str).ok_or_else(|| Error("wallet file: no seed".into()))?;
    let seed: [u8; 32] = hex::decode_array(seed_hex, "seed")?;
    Ok((Wallet::from_seed(Seed::from_bytes(seed)), v.get("birthday").and_then(Value::as_u64)))
}

fn load_fvk(cli_wallet: &Option<PathBuf>, cli_fvk: &Option<PathBuf>) -> Result<(FullViewingKey, Option<u64>)> {
    if let Some(p) = cli_fvk {
        let text = Zeroizing::new(fs::read_to_string(p).map_err(|e| Error(format!("{}: {e}", p.display())))?);
        let bytes = Zeroizing::new(hex::decode(&text)?);
        return Ok((FullViewingKey::from_bytes(&bytes)?, None));
    }
    let (w, b) = load_wallet(cli_wallet)?;
    Ok((w.fvk().clone(), b))
}

fn note_json(n: &OwnedNote) -> Value {
    json!({
        "value": n.note.value.to_string(),
        "pk": hex::encode(&n.note.pk),
        "r": hex::encode(&n.note.r),
        "d": hex::encode(&n.note.d),
        "memo": hex::encode(&n.note.memo),
        "cm": hex::encode(&n.cm),
        "position": n.position,
        "height": n.height,
        "nullifier": hex::encode(&n.nullifier),
        "spent_at": n.spent_at,
    })
}

fn note_from_json(v: &Value) -> Result<OwnedNote> {
    let s = |k: &str| v.get(k).and_then(Value::as_str).ok_or_else(|| Error(format!("note: no {k}")));
    let u = |k: &str| v.get(k).and_then(Value::as_u64).ok_or_else(|| Error(format!("note: no {k}")));
    Ok(OwnedNote {
        note: note::Note {
            value: s("value")?.parse().map_err(|_| Error("note: the value".into()))?,
            pk: hex::decode_array(s("pk")?, "pk")?,
            r: hex::decode_array(s("r")?, "r")?,
            d: hex::decode_array(s("d")?, "d")?,
            memo: hex::decode(s("memo")?)?,
        },
        cm: hex::decode_array(s("cm")?, "cm")?,
        position: u("position")?,
        height: u("height")?,
        nullifier: hex::decode_array(s("nullifier")?, "nullifier")?,
        spent_at: v.get("spent_at").and_then(Value::as_u64),
    })
}

fn block_from_line(line: &str) -> Result<Block> {
    let v: Value = serde_json::from_str(line).map_err(|e| Error(format!("block: {e}")))?;
    let height = v.get("height").and_then(Value::as_u64).ok_or_else(|| Error("block: no height".into()))?;
    let mut b = Block { height, position: v.get("position").and_then(Value::as_u64), ..Block::default() };
    for o in v.get("outputs").and_then(Value::as_array).into_iter().flatten() {
        let h = o.as_str().ok_or_else(|| Error("block: an output is not hex".into()))?;
        b.outputs.push(Output::from_bytes(&hex::decode(h)?)?);
    }
    for nf in v.get("nullifiers").and_then(Value::as_array).into_iter().flatten() {
        let h = nf.as_str().ok_or_else(|| Error("block: a nullifier is not hex".into()))?;
        b.nullifiers.push(hex::decode_array(h, "nullifier")?);
    }
    for m in v.get("mints").and_then(Value::as_array).into_iter().flatten() {
        let s = |k: &str| m.get(k).and_then(Value::as_str).ok_or_else(|| Error(format!("block: a mint has no {k}")));
        b.mints.push(Mint {
            position: m.get("position").and_then(Value::as_u64).ok_or_else(|| Error("block: a mint has no position".into()))?,
            value: s("value")?.parse().map_err(|_| Error("block: a mint's value".into()))?,
            pk: hex::decode_array(s("pk")?, "pk")?,
            r: hex::decode_array(s("r")?, "r")?,
            cm: hex::decode_array(s("cm")?, "cm")?,
        });
    }
    Ok(b)
}

fn write_prepared(p: &witness::Prepared, witness_out: &Path, record_out: &Path) -> Result<()> {
    write_private(witness_out, p.witness.as_bytes())?;
    fs::write(record_out, serde_json::to_string_pretty(&p.record).unwrap()).map_err(|e| Error(format!("{}: {e}", record_out.display())))
}

fn run(cli: Cli) -> Result<()> {
    if matches!(cli.cmd, Cmd::VaultKeygen { .. } | Cmd::VaultCreate { .. } | Cmd::VaultScan { .. } | Cmd::VaultRequest { .. } | Cmd::VaultCancel { .. } | Cmd::VaultComplete { .. } | Cmd::VaultNewPolicy { .. } | Cmd::VaultAnnounce { .. } | Cmd::VaultPolicyComplete { .. } | Cmd::VaultRecCancel { .. }) {
        return run_vault(&cli.wallet, cli.cmd);
    }
    match cli.cmd {
        Cmd::Keygen { birthday } => {
            let path = cli.wallet.as_ref().ok_or_else(|| Error("--wallet is needed".into()))?;
            let w = Wallet::generate();
            write_wallet(path, &w, Some(birthday))?;
            println!("{}", w.address().encode());
        }
        Cmd::ViewingKey { out } => {
            let (w, _) = load_wallet(&cli.wallet)?;
            let bytes = w.fvk().to_bytes();
            write_private(&out, hex::encode(&bytes).as_bytes())?;
            println!("viewing key written: {} bytes; it reads every amount and payment of this wallet and cannot spend", bytes.len());
        }
        Cmd::Backup => {
            let (w, birthday) = load_wallet(&cli.wallet)?;
            println!("{}", backup::words(w.seed()).as_str());
            match birthday {
                Some(b) => eprintln!("birthday {b}: keep the height with the words; a restore scans from it. {BIRTHDAY_WARNING}"),
                None => eprintln!("no birthday on this wallet: a restore scans from genesis"),
            }
        }
        Cmd::Restore { words_file, birthday } => {
            let path = cli.wallet.as_ref().ok_or_else(|| Error("--wallet is needed".into()))?;
            let phrase = Zeroizing::new(match words_file {
                Some(f) => fs::read_to_string(&f).map_err(|e| Error(format!("{}: {e}", f.display())))?,
                None => {
                    let mut s = String::new();
                    io::stdin().read_to_string(&mut s).map_err(|e| Error(format!("stdin: {e}")))?;
                    s
                }
            });
            let w = backup::restore(&phrase)?;
            write_wallet(path, &w, birthday)?;
            match birthday {
                None => eprintln!("no birthday given: this wallet scans from genesis (give --birthday to scan from the seed's creation height; {BIRTHDAY_WARNING})"),
                Some(b) => eprintln!("birthday {b}: the restore scans from it. {BIRTHDAY_WARNING}"),
            }
            println!("{}", w.address().encode());
        }
        Cmd::Seal { to, value, memo } => {
            let addr = Address::decode(&to)?;
            let (_, out) = note::seal(&addr, value, memo.as_bytes())?;
            println!("{}", hex::encode(&out.to_bytes()));
        }
        Cmd::Receive { output, position } => {
            let (fvk, _) = load_fvk(&cli.wallet, &cli.viewing_key)?;
            let text = match output.strip_prefix('@') {
                Some(f) => fs::read_to_string(f).map_err(|e| Error(format!("{f}: {e}")))?,
                None => output,
            };
            let out = Output::from_bytes(&hex::decode(&text)?)?;
            match note::open(&fvk, &out) {
                None => println!("not this wallet's"),
                Some(n) => {
                    let nf = position.map(|p| hex::encode(&fvk.nullifier(&out.cm, p)));
                    println!(
                        "{}",
                        json!({
                            "value": n.value.to_string(), "memo": String::from_utf8_lossy(&n.memo), "memo_hex": hex::encode(&n.memo),
                            "cm": hex::encode(&out.cm), "r": hex::encode(&n.r), "d": hex::encode(&n.d),
                            "position": position, "nullifier": nf,
                        })
                    );
                }
            }
        }
        Cmd::Scan { blocks, from_height, notes_out } => {
            let (fvk, birthday) = load_fvk(&cli.wallet, &cli.viewing_key)?;
            let from = from_height.or(birthday).unwrap_or(0);
            let text = fs::read_to_string(&blocks).map_err(|e| Error(format!("{}: {e}", blocks.display())))?;
            let mut s = Scanner::new(fvk, 0);
            let started = Instant::now();
            let (mut blocks_read, mut skipped_blocks, mut skipped_outputs) = (0u64, 0u64, 0u64);
            for line in text.lines().filter(|l| !l.trim().is_empty()) {
                let b = block_from_line(line)?;
                if b.height < from {
                    skipped_blocks += 1;
                    skipped_outputs += b.outputs.len() as u64;
                    if b.position.is_none() {
                        return Err(Error(format!("block {} is below the birthday and carries no position; the stream needs positions to skip", b.height)));
                    }
                    continue;
                }
                s.scan_block(&b);
                blocks_read += 1;
            }
            let took = started.elapsed();
            let b = s.balance();
            if let Some(p) = notes_out {
                let notes: Vec<Value> = s.notes().iter().map(note_json).collect();
                fs::write(&p, serde_json::to_string_pretty(&notes).unwrap()).map_err(|e| Error(format!("{}: {e}", p.display())))?;
            }
            println!(
                "{}",
                json!({
                    "from_height": from, "blocks": blocks_read, "skipped_blocks": skipped_blocks, "skipped_outputs": skipped_outputs,
                    "outputs_tried": s.outputs_tried(), "next_position": s.next_position(),
                    "notes": b.notes, "spent_notes": b.spent_notes, "unspent": b.unspent.to_string(), "spent": b.spent.to_string(),
                    "seconds": took.as_secs_f64(),
                })
            );
        }
        Cmd::ShieldPrepare { to, value, memo, chain_id, witness_out, record_out } => {
            let addr = match to {
                Some(t) => Address::decode(&t)?,
                None => load_fvk(&cli.wallet, &cli.viewing_key)?.0.address(),
            };
            let p = witness::prepare_shield(&addr, value, memo.as_bytes(), chain_id)?;
            write_prepared(&p, &witness_out, &record_out)?;
            println!("{}", json!({ "commitment": p.record["commitment"], "value": value.to_string() }));
        }
        Cmd::SpendPrepare { notes, positions, paths, to, value, memo, fee, public_value, public_output, chain_id, witness_out, record_out } => {
            let (w, _) = load_wallet(&cli.wallet)?;
            let text = fs::read_to_string(&notes).map_err(|e| Error(format!("{}: {e}", notes.display())))?;
            let v: Value = serde_json::from_str(&text).map_err(|e| Error(format!("notes: {e}")))?;
            let list = v.as_array().ok_or_else(|| Error("notes: not a list".into()))?;
            let found = list.iter().map(note_from_json).collect::<Result<Vec<_>>>()?;
            let (anchor, path_map, placeholder): ([u8; 32], BTreeMap<u64, Vec<[u8; 32]>>, bool) = match paths {
                Some(pf) => {
                    let t = fs::read_to_string(&pf).map_err(|e| Error(format!("{}: {e}", pf.display())))?;
                    let pv: Value = serde_json::from_str(&t).map_err(|e| Error(format!("paths: {e}")))?;
                    let anchor: [u8; 32] = hex::decode_array(pv["anchor"].as_str().unwrap_or(""), "anchor")?;
                    let mut m = BTreeMap::new();
                    for (k, arr) in pv["paths"].as_object().ok_or_else(|| Error("paths: no map".into()))? {
                        let pos: u64 = k.parse().map_err(|_| Error(format!("paths: position {k}")))?;
                        let mut sib = Vec::new();
                        for s in arr.as_array().ok_or_else(|| Error("paths: a path is not a list".into()))? {
                            sib.push(hex::decode_array(s.as_str().unwrap_or(""), "sibling")?);
                        }
                        m.insert(pos, sib);
                    }
                    (anchor, m, false)
                }
                None => ([0u8; 32], BTreeMap::new(), true),
            };
            let mut inputs = Vec::new();
            for ps in positions.split(',').map(|s| s.trim()).filter(|s| !s.is_empty()) {
                let position: u64 = ps.parse().map_err(|_| Error(format!("positions: {ps}")))?;
                let n = found.iter().find(|n| n.position == position).ok_or_else(|| Error(format!("no note at position {position}")))?;
                let path = match path_map.get(&position) {
                    Some(p) => p.clone(),
                    None if placeholder => vec![[0u8; 32]; sizes::TREE_DEPTH],
                    None => return Err(Error(format!("the paths file has no path for position {position}"))),
                };
                inputs.push(SpendInput { note: n.clone(), path });
            }
            let plan = SpendPlan {
                chain_id,
                anchor,
                inputs,
                to: Address::decode(&to)?,
                value,
                memo: memo.into_bytes(),
                fee,
                public_value,
                public_output: hex::decode_array(public_output.trim_start_matches("0x"), "public output")?,
            };
            let mut p = witness::prepare_spend(&w, &plan)?;
            witness::check_spend_witness(&p.witness)?;
            p.record["path_placeholder"] = json!(placeholder);
            write_prepared(&p, &witness_out, &record_out)?;
            println!(
                "{}",
                json!({ "inputs": plan.inputs.len(), "inputs_value": p.record["inputs_value"], "change": p.record["change"], "path_placeholder": placeholder })
            );
        }
        Cmd::Address { raw } => {
            let (fvk, _) = load_fvk(&cli.wallet, &cli.viewing_key)?;
            let a = fvk.address();
            if raw {
                println!("{}", hex::encode(&a.to_bytes()));
            } else {
                println!("{}", a.encode());
            }
        }
        Cmd::BenchScan { n } => bench(n),
        Cmd::Vectors { seed_hex } => {
            let seed: [u8; 32] = hex::decode_array(&seed_hex, "seed")?;
            let w = Wallet::from_seed(Seed::from_bytes(seed));
            let ek = w.fvk().kem_public_key().as_slice().to_vec();
            let (_, out) = note::seal_with(&w.address(), 123_456_789, b"vector", [0x11u8; 32], [0x22u8; 32])?;
            let nf = w.fvk().nullifier(&out.cm, 7);
            println!(
                "{}",
                serde_json::to_string_pretty(&json!({
                    "hash": H::NAME,
                    "seed": seed_hex,
                    "sk_spend": hex::encode(w.sk_spend()),
                    "nk": hex::encode(w.fvk().nk()),
                    "ivk": hex::encode(w.fvk().ivk()),
                    "pk": hex::encode(w.fvk().pk()),
                    "ek_blake3": hex::encode(&igneum_wallet_money::hash::b_hash(&ek)),
                    "ek_first32": hex::encode(&ek[..32]),
                    "address_blake3": hex::encode(&w.address().short_hash()),
                    "address_text_prefix": &w.address().encode()[..80],
                    "words": backup::words(w.seed()).as_str(),
                    "note": { "value": 123456789u64, "memo": "vector", "r": hex::encode(&[0x11u8; 32]), "encaps_randomness": hex::encode(&[0x22u8; 32]) },
                    "cm": hex::encode(&out.cm),
                    "output_blake3": hex::encode(&igneum_wallet_money::hash::b_hash(&out.to_bytes())),
                    "nullifier_at_7": hex::encode(&nf),
                }))
                .unwrap()
            );
        }
        Cmd::VaultKeygen { .. } | Cmd::VaultCreate { .. } | Cmd::VaultScan { .. } | Cmd::VaultRequest { .. } | Cmd::VaultCancel { .. } | Cmd::VaultComplete { .. } | Cmd::VaultNewPolicy { .. } | Cmd::VaultAnnounce { .. } | Cmd::VaultPolicyComplete { .. } | Cmd::VaultRecCancel { .. } => unreachable!(),
        Cmd::Sizes => {
            println!(
                "{}",
                serde_json::to_string_pretty(&json!({
                    "seed": sizes::SECRET, "kem_public_key": sizes::KEM_PUBLIC_KEY, "kem_private_key": sizes::KEM_PRIVATE_KEY,
                    "kem_ciphertext": sizes::KEM_CIPHERTEXT, "address": sizes::ADDRESS, "note_plaintext": sizes::NOTE_PLAINTEXT,
                    "note_body": sizes::NOTE_BODY, "output_ciphertext": sizes::OUTPUT_CIPHERTEXT, "output": sizes::OUTPUT,
                    "full_viewing_key": sizes::FULL_VIEWING_KEY, "memo_max": sizes::MEMO_MAX, "tree_depth": sizes::TREE_DEPTH,
                    "value_bits": 128, "hash": H::NAME,
                }))
                .unwrap()
            );
        }
    }
    Ok(())
}

/// A paths file as the node served it: {"anchor": hex, "paths": {"<position>": [32 hex siblings]}}.
fn load_paths(pf: &Path) -> Result<([u8; 32], BTreeMap<u64, Vec<[u8; 32]>>)> {
    let t = fs::read_to_string(pf).map_err(|e| Error(format!("{}: {e}", pf.display())))?;
    let pv: Value = serde_json::from_str(&t).map_err(|e| Error(format!("paths: {e}")))?;
    let anchor: [u8; 32] = hex::decode_array(pv["anchor"].as_str().unwrap_or(""), "anchor")?;
    let mut m = BTreeMap::new();
    for (k, arr) in pv["paths"].as_object().ok_or_else(|| Error("paths: no map".into()))? {
        let pos: u64 = k.parse().map_err(|_| Error(format!("paths: position {k}")))?;
        let mut sib = Vec::new();
        for s in arr.as_array().ok_or_else(|| Error("paths: a path is not a list".into()))? {
            sib.push(hex::decode_array(s.as_str().unwrap_or(""), "sibling")?);
        }
        m.insert(pos, sib);
    }
    Ok((anchor, m))
}

fn path_of(m: &BTreeMap<u64, Vec<[u8; 32]>>, pos: Option<u64>, what: &str) -> Result<Vec<[u8; 32]>> {
    let pos = pos.ok_or_else(|| Error(format!("{what} has no position: scan first")))?;
    m.get(&pos).cloned().ok_or_else(|| Error(format!("the paths file has no path for {what} at position {pos}")))
}

fn load_vault(p: &Path) -> Result<vault::VaultFile> {
    let t = Zeroizing::new(fs::read_to_string(p).map_err(|e| Error(format!("{}: {e}", p.display())))?);
    vault::VaultFile::from_json(&t)
}

fn save_vault(p: &Path, v: &vault::VaultFile) -> Result<()> {
    let text = Zeroizing::new(v.to_json());
    if p.exists() {
        fs::write(p, text.as_bytes()).map_err(|e| Error(format!("{}: {e}", p.display())))
    } else {
        write_private(p, text.as_bytes())
    }
}

fn load_secret(p: &Path) -> Result<Zeroizing<[u8; 32]>> {
    let t = Zeroizing::new(fs::read_to_string(p).map_err(|e| Error(format!("{}: {e}", p.display())))?);
    Ok(Zeroizing::new(hex::decode_array(t.trim(), "key")?))
}

fn vault_state(v: &vault::VaultFile) -> Value {
    let (head, pending, inbound) = v.balance();
    json!({
        "vid": v.vid, "chain_id": v.chain_id, "policy_hash": v.policy.hash().map(|h| hex::encode(&h)).unwrap_or_default(),
        "head": v.head().map(|h| json!({ "value": h.value, "position": h.position, "height": h.height, "period_idx": h.period_idx, "spent": h.spent, "recovery_pending": h.recovery_pending(), "rec_h": h.rec_h, "rec_t": h.rec_t, "co_signed": h.co == 1, "cm": h.cm })),
        "heads_known": v.heads.len(),
        "pending": v.pending.iter().map(|p| json!({ "value": p.value, "cm": p.cm, "position": p.position, "height": p.height, "matures_at_block": p.h_req + p.delay_blocks, "matures_at_time": p.t_req + p.delay_seconds, "pk_dest": p.pk_dest, "consumed_at": p.consumed_at })).collect::<Vec<_>>(),
        "inbound": v.inbound.iter().map(|i| json!({ "value": i.value, "cm": i.cm, "position": i.position })).collect::<Vec<_>>(),
        "protected": head.to_string(), "leaving": pending.to_string(), "returned": inbound.to_string(),
        "pending_policy": v.pending_policy.as_ref().map(|p| p.hash().map(|h| hex::encode(&h)).unwrap_or_default()),
    })
}

fn pk_arg(s: &str, what: &str) -> Result<[u8; 32]> {
    hex::decode_array(s.trim_start_matches("0x"), what)
}

fn run_vault(cli_wallet: &Option<PathBuf>, cmd: Cmd) -> Result<()> {
    match cmd {
        Cmd::VaultKeygen { out } => {
            let sk = vault::keygen_secret();
            write_private(&out, hex::encode(sk.as_ref()).as_bytes())?;
            println!("{}", json!({ "cancel": hex::encode(&vault::pk_cancel(&sk)), "recover": hex::encode(&vault::pk_recover(&sk)) }));
        }
        Cmd::VaultCreate { notes, positions, paths, value, delay_blocks, delay_seconds, period, limit, recovery_blocks, recovery_seconds, cancel_pk, recovery_pk, chain_id, witness_out, record_out, vault_out } => {
            let (w, _) = load_wallet(cli_wallet)?;
            let text = fs::read_to_string(&notes).map_err(|e| Error(format!("{}: {e}", notes.display())))?;
            let v: Value = serde_json::from_str(&text).map_err(|e| Error(format!("notes: {e}")))?;
            let found = v.as_array().ok_or_else(|| Error("notes: not a list".into()))?.iter().map(note_from_json).collect::<Result<Vec<_>>>()?;
            let (anchor, path_map) = load_paths(&paths)?;
            let mut inputs = Vec::new();
            for ps in positions.split(',').map(|s| s.trim()).filter(|s| !s.is_empty()) {
                let position: u64 = ps.parse().map_err(|_| Error(format!("positions: {ps}")))?;
                let n = found.iter().find(|n| n.position == position).ok_or_else(|| Error(format!("no note at position {position}")))?;
                inputs.push(SpendInput { note: n.clone(), path: path_of(&path_map, Some(position), "the input")? });
            }
            let (p, vf) = vault::prepare_create(&w, chain_id, &anchor, &inputs, value, &pk_arg(&cancel_pk, "cancel_pk")?, &pk_arg(&recovery_pk, "recovery_pk")?, (delay_blocks, delay_seconds, period, recovery_blocks, recovery_seconds), limit)?;
            write_private(&witness_out, p.witness.as_bytes())?;
            fs::write(&record_out, serde_json::to_string_pretty(&p.record).unwrap()).map_err(|e| Error(format!("{}: {e}", record_out.display())))?;
            save_vault(&vault_out, &vf)?;
            println!("{}", json!({ "vid": vf.vid, "policy_hash": hex::encode(&vf.policy.hash()?), "head_commitment": vf.heads[0].cm, "value": value.to_string(), "change": p.record["change"] }));
        }
        Cmd::VaultScan { vault: vp, blocks } => {
            let mut v = load_vault(&vp)?;
            let kem = v.kem()?;
            let text = fs::read_to_string(&blocks).map_err(|e| Error(format!("{}: {e}", blocks.display())))?;
            let mut found = 0;
            for line in text.lines().filter(|l| !l.trim().is_empty()) {
                let b = block_from_line(line)?;
                found += vault::scan_block(&mut v, &kem, &b)?;
            }
            save_vault(&vp, &v)?;
            let mut st = vault_state(&v);
            st["found"] = json!(found);
            println!("{st}");
        }
        Cmd::VaultRequest { vault: vp, paths, to, value, height, time, witness_out, record_out, receipt_out } => {
            let (w, _) = load_wallet(cli_wallet)?;
            let mut v = load_vault(&vp)?;
            let (anchor, path_map) = load_paths(&paths)?;
            let head_path = path_of(&path_map, v.head().and_then(|h| h.position), "the head")?;
            let (p, pending) = vault::prepare_withdraw(&w, &mut v, &anchor, &head_path, &Address::decode(&to)?, value, height, time)?;
            write_private(&witness_out, p.witness.as_bytes())?;
            fs::write(&record_out, serde_json::to_string_pretty(&p.record).unwrap()).map_err(|e| Error(format!("{}: {e}", record_out.display())))?;
            write_private(&receipt_out, serde_json::to_string_pretty(&pending).unwrap().as_bytes())?;
            save_vault(&vp, &v)?;
            println!("{}", json!({ "pending_commitment": pending.cm, "value": value.to_string(), "matures_at_block": pending.h_req + pending.delay_blocks, "matures_at_time": pending.t_req + pending.delay_seconds, "head_out": p.record["outputs"][0]["commitment"] }));
        }
        Cmd::VaultCancel { vault: vp, key, pending_cm, paths, witness_out, record_out } => {
            let v = load_vault(&vp)?;
            let sk = load_secret(&key)?;
            let (anchor, path_map) = load_paths(&paths)?;
            let pending = v.pending.iter().find(|p| p.cm == pending_cm.trim_start_matches("0x")).cloned().ok_or_else(|| Error("no pending note with that commitment in the vault file: scan first".into()))?;
            let path = path_of(&path_map, pending.position, "the pending note")?;
            let p = vault::prepare_cancel(&v, &sk, &anchor, &pending, &path)?;
            write_private(&witness_out, p.witness.as_bytes())?;
            fs::write(&record_out, serde_json::to_string_pretty(&p.record).unwrap()).map_err(|e| Error(format!("{}: {e}", record_out.display())))?;
            println!("{}", json!({ "inbound_commitment": p.record["outputs"][0]["commitment"], "ptag": p.record["ptag"], "value": pending.value }));
        }
        Cmd::VaultComplete { receipt, paths, revoked, height, time, chain_id, witness_out, record_out } => {
            let (w, _) = load_wallet(cli_wallet)?;
            let t = fs::read_to_string(&receipt).map_err(|e| Error(format!("{}: {e}", receipt.display())))?;
            let mut pending: vault::Pending = serde_json::from_str(&t).map_err(|e| Error(format!("receipt: {e}")))?;
            let (anchor, path_map) = load_paths(&paths)?;
            if pending.position.is_none() {
                // the receipt carries no position: the paths file names exactly one, the note's
                if path_map.len() == 1 {
                    pending.position = path_map.keys().next().copied();
                }
            }
            let path = path_of(&path_map, pending.position, "the pending note")?;
            let rt = fs::read_to_string(&revoked).map_err(|e| Error(format!("{}: {e}", revoked.display())))?;
            let rp: vault::RevokedPath = serde_json::from_str(&rt).map_err(|e| Error(format!("revoked: {e}")))?;
            let p = vault::prepare_complete(&w, chain_id, &anchor, &pending, &path, &rp, height, time)?;
            write_private(&witness_out, p.witness.as_bytes())?;
            fs::write(&record_out, serde_json::to_string_pretty(&p.record).unwrap()).map_err(|e| Error(format!("{}: {e}", record_out.display())))?;
            println!("{}", json!({ "value": pending.value, "outputs": p.record["outputs"].as_array().map(|a| a.len()).unwrap_or(0) }));
        }
        Cmd::VaultNewPolicy { vault: vp, delay_blocks, delay_seconds, period, limit, recovery_blocks, recovery_seconds, cancel_pk, recovery_pk, out } => {
            let (w, _) = load_wallet(cli_wallet)?;
            let v = load_vault(&vp)?;
            let (p, nk_v2) = vault::new_policy(&w, &v, &pk_arg(&cancel_pk, "cancel_pk")?, &pk_arg(&recovery_pk, "recovery_pk")?, (delay_blocks, delay_seconds, period, recovery_blocks, recovery_seconds), limit)?;
            write_private(&out, serde_json::to_string_pretty(&json!({ "policy": p, "nk_v": hex::encode(&nk_v2) })).unwrap().as_bytes())?;
            println!("{}", json!({ "policy_hash": hex::encode(&p.hash()?) }));
        }
        Cmd::VaultAnnounce { vault: vp, key, new_policy, paths, height, time, witness_out, record_out } => {
            let mut v = load_vault(&vp)?;
            let sk = load_secret(&key)?;
            let t = Zeroizing::new(fs::read_to_string(&new_policy).map_err(|e| Error(format!("{}: {e}", new_policy.display())))?);
            let np: Value = serde_json::from_str(&t).map_err(|e| Error(format!("new policy: {e}")))?;
            let policy: vault::Policy = serde_json::from_value(np["policy"].clone()).map_err(|e| Error(format!("new policy: {e}")))?;
            let (anchor, path_map) = load_paths(&paths)?;
            let head_path = path_of(&path_map, v.head().and_then(|h| h.position), "the head")?;
            let cosign = match cli_wallet {
                Some(_) => Some(load_wallet(cli_wallet)?.0),
                None => None,
            };
            let p = vault::prepare_announce(&mut v, &sk, &anchor, &head_path, &policy, height, time, cosign.as_ref())?;
            write_private(&witness_out, p.witness.as_bytes())?;
            fs::write(&record_out, serde_json::to_string_pretty(&p.record).unwrap()).map_err(|e| Error(format!("{}: {e}", record_out.display())))?;
            save_vault(&vp, &v)?;
            println!("{}", json!({ "new_policy_hash": hex::encode(&policy.hash()?), "completes_at_block": height + v.policy.recovery_blocks, "completes_at_time": time + v.policy.recovery_seconds, "head_out": p.record["outputs"][0]["commitment"] }));
        }
        Cmd::VaultPolicyComplete { vault: vp, new_policy, paths, height, time, witness_out, record_out } => {
            let mut v = load_vault(&vp)?;
            let t = Zeroizing::new(fs::read_to_string(&new_policy).map_err(|e| Error(format!("{}: {e}", new_policy.display())))?);
            let np: Value = serde_json::from_str(&t).map_err(|e| Error(format!("new policy: {e}")))?;
            let nk_v2: [u8; 32] = hex::decode_array(np["nk_v"].as_str().unwrap_or(""), "nk_v")?;
            let (anchor, path_map) = load_paths(&paths)?;
            let head_path = path_of(&path_map, v.head().and_then(|h| h.position), "the head")?;
            let p = vault::prepare_policy_complete(&mut v, &anchor, &head_path, &nk_v2, height, time)?;
            write_private(&witness_out, p.witness.as_bytes())?;
            fs::write(&record_out, serde_json::to_string_pretty(&p.record).unwrap()).map_err(|e| Error(format!("{}: {e}", record_out.display())))?;
            save_vault(&vp, &v)?;
            println!("{}", json!({ "revealed": p.record["revealed"], "policy_hash": hex::encode(&v.policy.hash()?), "head_out": p.record["outputs"][0]["commitment"] }));
        }
        Cmd::VaultRecCancel { vault: vp, key, paths, force, witness_out, record_out } => {
            let mut v = load_vault(&vp)?;
            let sk = load_secret(&key)?;
            let (anchor, path_map) = load_paths(&paths)?;
            let head_path = path_of(&path_map, v.head().and_then(|h| h.position), "the head")?;
            let p = vault::prepare_rec_cancel(&mut v, &sk, &anchor, &head_path, force)?;
            write_private(&witness_out, p.witness.as_bytes())?;
            fs::write(&record_out, serde_json::to_string_pretty(&p.record).unwrap()).map_err(|e| Error(format!("{}: {e}", record_out.display())))?;
            save_vault(&vp, &v)?;
            println!("{}", json!({ "head_out": p.record["outputs"][0]["commitment"] }));
        }
        _ => unreachable!(),
    }
    Ok(())
}

/// One core: trial decryption of outputs that are not the wallet's (the common case) and of ones that are;
/// the year-of-blocks and the restore figures at 1,000, 10,000 and 30,000 transfers a day.
fn bench(n: usize) {
    let mine = Wallet::generate();
    let stranger = Wallet::generate();
    let miss: Vec<Output> = (0..n).map(|i| note::seal(&stranger.address(), i as u128, b"").unwrap().1).collect();
    let hit: Vec<Output> = (0..n.min(2000)).map(|i| note::seal(&mine.address(), i as u128, b"").unwrap().1).collect();
    let mut s = Scanner::new(mine.fvk().clone(), 0);
    for o in miss.iter().take(500) {
        let _ = note::open(s.fvk(), o);
    }
    let t = Instant::now();
    let b = Block { height: 1, position: None, outputs: miss.clone(), nullifiers: vec![], mints: vec![] };
    let found_miss = s.scan_block(&b);
    let miss_us = t.elapsed().as_secs_f64() * 1e6 / n as f64;
    let t = Instant::now();
    let b = Block { height: 2, position: None, outputs: hit.clone(), nullifiers: vec![], mints: vec![] };
    let found_hit = s.scan_block(&b);
    let hit_us = t.elapsed().as_secs_f64() * 1e6 / hit.len() as f64;
    let t = Instant::now();
    let kem = libcrux_ml_kem::mlkem768::MlKem768Ciphertext::from(miss[0].kem_ct);
    for _ in 0..n {
        let _ = std::hint::black_box(libcrux_ml_kem::mlkem768::decapsulate(mine.fvk().kem_private_key(), &kem));
    }
    let decaps_us = t.elapsed().as_secs_f64() * 1e6 / n as f64;
    let year_blocks = 365.0 * 86400.0 / 12.0;
    let mut rows = Vec::new();
    for transfers_a_day in [1_000u64, 10_000, 30_000, 100_000] {
        let outputs_a_year = transfers_a_day as f64 * 2.0 * 365.0;
        let hours = outputs_a_year * miss_us / 3.6e9;
        let bytes = outputs_a_year * sizes::OUTPUT_CIPHERTEXT as f64;
        rows.push(json!({
            "transfers_a_day": transfers_a_day, "outputs_a_year": outputs_a_year as u64,
            "outputs_a_block": transfers_a_day as f64 * 2.0 / 7200.0,
            "hours_one_core": hours, "minutes_one_core": hours * 60.0,
            "ciphertext_gb_a_year": bytes / 1e9,
            "restore_one_year_old_wallet_minutes": hours * 60.0,
            "restore_without_birthday_minutes_per_chain_year": hours * 60.0,
        }));
    }
    println!(
        "{}",
        serde_json::to_string_pretty(&json!({
            "n": n, "found_miss": found_miss, "found_hit": found_hit,
            "us_per_ciphertext_miss": miss_us, "us_per_ciphertext_hit": hit_us, "us_per_decapsulation": decaps_us,
            "blocks_a_year_at_12s": year_blocks as u64,
            "year": rows,
            "cpu": std::fs::read_to_string("/proc/cpuinfo").ok().and_then(|t| t.lines().find(|l| l.starts_with("model name")).map(|l| l.split(':').nth(1).unwrap_or("").trim().to_string())),
        }))
        .unwrap()
    );
}
