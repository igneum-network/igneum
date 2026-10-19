//! `igneum-wallet`: the core on the command line. Keys come from a vault file or, for the differential gate only,
//! from `--key-hex` (never in a release workflow). Watch-only commands need no key at all.

use std::fs;
use std::io::{self, Read, Write};
use std::path::PathBuf;

use clap::{Parser, Subcommand};
use serde_json::json;
use zeroize::Zeroizing;

use igneum_wallet_authority::{claim, draw, receipts};
use igneum_wallet_rpc::Rpc;
use igneum_wallet_core::{
    authority,
    chain::Chain,
    display,
    envelope::{SignedEnvelope, UnsignedEnvelope},
    hex, mnemonic,
    settlement::{self, Observations, Policy, State},
    signer::{Signer, SoftwareSigner},
    tx::{Eip1559Tx, Transaction},
    vault::{self, KdfParams, Public, Secret, SecretKind, VaultFile},
    Address, Error, Result, U256,
};

#[derive(Parser)]
#[command(name = "igneum-wallet", about = "Igneum Wallet on the command line", version)]
struct Cli {
    /// the node's JSON-RPC URL
    #[arg(long, default_value = "http://127.0.0.1:8545", global = true)]
    rpc: String,
    /// the vault file
    #[arg(long, global = true)]
    vault: Option<PathBuf>,
    /// a raw key for the differential gate only; never for real funds
    #[arg(long, global = true, hide = true)]
    key_hex: Option<String>,
    #[command(subcommand)]
    cmd: Cmd,
}

#[derive(Subcommand)]
enum Cmd {
    /// Make a new wallet: the vault written, the 24 recovery words shown once on the terminal after a warning and
    /// a press of enter (a logged terminal records them), or written to --words-file (mode 0600) with the address
    /// alone printed
    Create {
        /// a file holding the vault's password on one line, of the owner's choosing (never typed on the command line)
        #[arg(long)]
        password_file: PathBuf,
        /// write the 24 words to this file (mode 0600, created new) and print only the address and the vault's path
        #[arg(long)]
        words_file: Option<PathBuf>,
    },
    /// Import words or a raw key into a vault
    Import {
        #[arg(long)]
        password_file: PathBuf,
        /// read the phrase or key from stdin
        #[arg(long)]
        stdin: bool,
    },
    /// Show the vault's address and kind
    Address,
    /// Derive the address at an index of a phrase read from stdin (no vault written)
    Derive {
        #[arg(long, default_value_t = 0)]
        index: u32,
    },
    /// Build an unsigned envelope for a payment (watch-only or not): the node supplies nonce, fees and gas
    Prepare {
        #[arg(long)]
        from: String,
        #[arg(long)]
        to: String,
        /// in IGN, for example 1.5
        #[arg(long)]
        amount: String,
        #[arg(long, default_value = "")]
        data: String,
    },
    /// Sign an unsigned envelope read from stdin; writes the signed envelope
    Sign {
        #[arg(long)]
        password_file: Option<PathBuf>,
    },
    /// Broadcast a signed envelope read from stdin
    Broadcast,
    /// Prepare, sign and broadcast in one step
    Send {
        #[arg(long)]
        to: String,
        #[arg(long)]
        amount: String,
        #[arg(long)]
        password_file: Option<PathBuf>,
        /// Call data, hex (for a contract call; the sentences decode it where the wallet knows the form)
        #[arg(long, default_value = "")]
        data: String,
    },
    /// Show the sentences and the simulation for a payment without signing
    Simulate {
        #[arg(long)]
        from: String,
        #[arg(long)]
        to: String,
        #[arg(long)]
        amount: String,
        #[arg(long, default_value = "")]
        data: String,
    },
    /// The settlement words for a transaction hash
    Settlement {
        hash: String,
    },
    /// Sign a raw 32-byte hash (the differential gate's primitive); prints r, s, recovery id
    SignHash {
        hash: String,
    },
    /// Sign a transaction given as JSON on stdin (the differential gate against cast and the node); prints raw and hash
    SignTx {
        #[arg(long)]
        password_file: Option<PathBuf>,
        #[arg(long, default_value = "Igneum development chain 7310")]
        network: String,
    },
    /// Which chain the node serves: the chain id, the genesis hash and the name the wallet pins to it
    Chain,
    /// Bond IGN to a mining identity (a 32-byte Ed25519 public key) in the chain's registry; prepare, sign, broadcast
    Bond {
        /// the identity key, 32 bytes of hex
        #[arg(long)]
        key: String,
        /// in IGN
        #[arg(long)]
        amount: String,
        /// DEV-ONLY, registry version 3: name the exit address in the bond transaction (refused by today's registry)
        #[arg(long)]
        exit: Option<String>,
        #[arg(long)]
        password_file: Option<PathBuf>,
        /// skip the node's gas estimate and send with this gas limit (to record a chain's refusal on the chain itself)
        #[arg(long)]
        gas: Option<u64>,
        /// build and show the envelope, sign nothing
        #[arg(long)]
        dry_run: bool,
    },
    /// Rotate a mining identity's signing key (the registry's 64-byte call, from the bond's account)
    SetSigner {
        #[arg(long)]
        key: String,
        /// the new signing key, 32 bytes of hex (an Ed25519 public key); zero means the identity key itself signs
        #[arg(long)]
        signer: String,
        #[arg(long)]
        password_file: Option<PathBuf>,
        #[arg(long)]
        gas: Option<u64>,
        #[arg(long)]
        dry_run: bool,
    },
    /// Registry version 3: change a mining identity's exit address, by the bond's account, with a top-up (the lab
    /// build models no lock: the exit changes at once)
    SetExit {
        #[arg(long)]
        key: String,
        #[arg(long)]
        exit: String,
        /// the top-up in IGN the lab form carries (the bond grows by it)
        #[arg(long, default_value = "0.001")]
        amount: String,
        #[arg(long)]
        password_file: Option<PathBuf>,
        #[arg(long)]
        gas: Option<u64>,
        #[arg(long)]
        dry_run: bool,
    },
    /// DEV-ONLY (registry version 3): the claim: the proof pi under the bond's secret, made by the hash team's
    /// prover (pi-claim) from the secret's file by path, carried to the registry. To the registered exit address it
    /// is your own release after the lock; to any other address it is the hostile claim, a quarter burnt
    Claim {
        /// the bond's secret, a 32-byte hex file (0600; never the vault); the prover reads it by path
        #[arg(long)]
        sk_file: Option<PathBuf>,
        /// the identity key, when a proof file is given instead of the secret
        #[arg(long)]
        key: Option<String>,
        /// the claimant's address (the exit address for your own release)
        #[arg(long)]
        to: String,
        /// the chain id the claim message is bound to, when it is not the node's (a fixture's row); the node's own
        /// chain id otherwise (the envelope's C, u64 big-endian)
        #[arg(long)]
        c_hex: Option<String>,
        /// the bond's claim counter nonce_b (0 for a first claim)
        #[arg(long, default_value_t = 0)]
        claim_nonce: u64,
        /// the prover binary (pi-claim) to make the proof with; the proof is written to --out
        #[arg(long)]
        prover: Option<PathBuf>,
        #[arg(long)]
        out: Option<PathBuf>,
        /// a proof file made earlier (with --key and --share)
        #[arg(long)]
        pi_file: Option<PathBuf>,
        /// DEV-ONLY, the lab registry's stand-in (devnet5 until the verifier is in the node): put the bond's SECRET
        /// itself in the transaction (with --sk-file); the secret becomes readable on the chain
        #[arg(long)]
        reveal: bool,
        /// the share s_0 the prover printed, when --pi-file is given
        #[arg(long)]
        share: Option<String>,
        #[arg(long)]
        password_file: Option<PathBuf>,
        #[arg(long)]
        gas: Option<u64>,
        #[arg(long)]
        dry_run: bool,
    },
    /// The identity key pk = G(sk) of a bond's secret file (BLAKE3 over the tagged secret); prints the key only
    Identity {
        #[arg(long)]
        seed_file: PathBuf,
    },
    /// The sortition signing key (an Ed25519 public key) of a 32-byte seed file; prints the key only
    SigningKey {
        #[arg(long)]
        seed_file: PathBuf,
    },
    /// A mining identity's bond and its share of the draw: units, share of the bonded total, round-0 chance, and
    /// its eligible round at the next height recomputed from the chain's seed (the node's own reading beside it)
    Draw {
        /// the identity key; without it, the node's own bond key
        #[arg(long)]
        key: Option<String>,
        /// the height to draw at; the next height without it
        #[arg(long)]
        at: Option<u64>,
    },
    /// Verify a pool member's signed receipts (JSON lines of mining.receipt params) under the pool's receipt key
    Receipts {
        #[arg(long)]
        file: PathBuf,
        /// the pool's receipt key (the subscribe result's receipt_key), 32 bytes of hex
        #[arg(long)]
        pool_key: String,
        /// the member's own key, 32 bytes of hex
        #[arg(long)]
        member_key: String,
        /// print the whole report as JSON
        #[arg(long)]
        json: bool,
    },
}



/// Writes `text` to a new file readable by the owner alone (0600 where the platform has modes; on Windows the
/// user's profile is the boundary), refusing an existing path.
fn write_private(path: &PathBuf, text: &str) -> Result<()> {
    let mut o = fs::OpenOptions::new();
    o.write(true).create_new(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        o.mode(0o600);
    }
    let mut f = o.open(path)?;
    f.write_all(text.as_bytes())?;
    f.write_all(b"\n")?;
    Ok(())
}

fn read_password(path: &PathBuf) -> Result<Zeroizing<String>> {
    let text = fs::read_to_string(path)?;
    Ok(Zeroizing::new(text.trim_end_matches(['\n', '\r']).to_string()))
}

fn read_stdin() -> Result<Zeroizing<String>> {
    let mut s = String::new();
    io::stdin().read_to_string(&mut s)?;
    Ok(Zeroizing::new(s))
}

fn load_vault(path: &Option<PathBuf>) -> Result<VaultFile> {
    let p = path.as_ref().ok_or_else(|| Error::Input("--vault is needed".into()))?;
    Ok(serde_json::from_str(&fs::read_to_string(p)?)?)
}

fn open_signer(cli: &Cli, password_file: &Option<PathBuf>) -> Result<SoftwareSigner> {
    if let Some(k) = &cli.key_hex {
        return SoftwareSigner::from_key_hex(k);
    }
    let file = load_vault(&cli.vault)?;
    let pf = password_file.as_ref().ok_or_else(|| Error::Input("--password-file is needed".into()))?;
    let password = read_password(pf)?;
    let secret = vault::open(&file, password.as_bytes())?;
    match secret.kind {
        SecretKind::Phrase => SoftwareSigner::from_phrase(&secret.text, 0),
        SecretKind::Key => SoftwareSigner::from_key_hex(&secret.text),
    }
}

/// The chain the node serves, pinned by its genesis hash (D-B14): chain id 7310 alone names nothing.
/// Where a signed transaction goes: the chain's public read endpoint refuses sends, so when the CLI's RPC is that
/// endpoint and the chain names a submit route (the engine's rule, `submit_rpc`), the send goes there; any other
/// RPC takes it directly. Found 12 October 2026: a send on devnet7's public endpoint read "read-only endpoint".
fn send_raw_via(rpc: &Rpc, raw: &[u8]) -> Result<[u8; 32]> {
    if let Ok(chain) = chain_for(rpc) {
        let public = chain.rpc.first().map(|u| u.trim_end_matches('/') == rpc.url().trim_end_matches('/')).unwrap_or(false);
        if let (true, Some(route)) = (public, chain.submit.as_deref()) {
            if route.trim_end_matches('/') != rpc.url().trim_end_matches('/') {
                return Rpc::new(route).send_raw(raw);
            }
        }
    }
    rpc.send_raw(raw)
}

fn chain_for(rpc: &Rpc) -> Result<Chain> {
    let id = rpc.chain_id()?;
    let genesis = rpc.genesis_hash().ok();
    let mut c = Chain::identify(id, genesis.as_deref());
    if c.rpc.is_empty() {
        c.rpc = vec![rpc.url().to_string()];
    }
    Ok(c)
}

fn prepare(rpc: &Rpc, from: &Address, to: &Address, amount: &str, data: &str) -> Result<UnsignedEnvelope> {
    let value = display::parse_ign(amount)?;
    let data = if data.is_empty() { vec![] } else { hex::decode(data)? };
    prepare_call(rpc, from, to, value, data, None)
}

/// Builds the envelope for any call: the node supplies nonce and fees; the gas is the node's estimate (plus a fifth)
/// unless given, and an estimate the node refuses is reported as the chain's refusal, with nothing signed.
fn prepare_call(rpc: &Rpc, from: &Address, to: &Address, value: U256, data: Vec<u8>, gas: Option<u64>) -> Result<UnsignedEnvelope> {
    let chain = chain_for(rpc)?;
    let nonce = rpc.nonce(from)?;
    let fees = rpc.fees()?;
    let call = json!({
        "from": from.to_checksum(), "to": to.to_checksum(), "value": value.to_hex(), "data": hex::encode(&data)
    });
    let gas = match gas {
        Some(g) => g,
        // a plain transfer to an account is 21,000; to a contract it runs `receive` and is estimated
        None if data.is_empty() && !rpc.has_code(&to.to_checksum()).unwrap_or(true) => 21000,
        None => rpc
            .estimate_gas(&call)
            .map_err(|e| Error::Refused(format!("the chain refuses this call at the estimate ({e}); nothing signed. --gas <limit> sends it anyway to record the refusal on the chain")))?
            * 12
            / 10,
    };
    let tx = Transaction::Eip1559(Eip1559Tx {
        chain_id: chain.chain_id,
        nonce,
        max_priority_fee_per_gas: fees.max_priority_fee_per_gas,
        max_fee_per_gas: fees.max_fee_per_gas,
        gas_limit: gas,
        to: Some(*to),
        value,
        data,
    });
    Ok(UnsignedEnvelope::new(&chain.name, *from, tx))
}

fn settlement_view(rpc: &Rpc, hash: &[u8; 32]) -> Result<serde_json::Value> {
    let head = rpc.block_number()?;
    let head_block = rpc.block(head)?.ok_or_else(|| Error::Rpc("no head block".into()))?;
    let head_unix = head_block.get("timestamp").and_then(|t| t.as_str()).map(U256::from_hex).transpose()?.and_then(|t| t.to_u64()).unwrap_or(0);
    let now = vault::now_unix();
    let tx = rpc.transaction(hash)?;
    let value = tx
        .as_ref()
        .and_then(|t| t.get("value"))
        .and_then(|v| v.as_str())
        .map(U256::from_hex)
        .transpose()?
        .unwrap_or(U256::ZERO);
    let receipt = rpc.receipt(hash)?;
    let state = match (&tx, &receipt) {
        (_, Some(r)) => {
            let block = r.get("blockNumber").and_then(|b| b.as_str()).map(U256::from_hex).transpose()?.and_then(|b| b.to_u64()).unwrap_or(0);
            let ok = r.get("status").and_then(|s| s.as_str()).map(|s| s == "0x1").unwrap_or(true);
            if ok { State::Included { block } } else { State::Reverted { block } }
        }
        (Some(_), None) => State::Submitted { sent_unix: now },
        (None, None) => State::Dropped { sent_unix: now },
    };
    // work on top: the difficulties of the blocks after the including one (up to 256), against the recent rate
    let (work_minutes, head_rate) = match &state {
        State::Included { block } | State::Reverted { block } => {
            let mut diffs = Vec::new();
            let upto = head.min(block + 256);
            for n in (block + 1)..=upto {
                if let Some((d, _)) = rpc.block_work(n)? {
                    diffs.push(d);
                }
            }
            let mut window = Vec::new();
            let start = head.saturating_sub(32);
            for n in start..=head {
                if let Some(w) = rpc.block_work(n)? {
                    window.push(w);
                }
            }
            let rate = settlement::recent_rate(&window);
            (settlement::work_minutes(&diffs, rate), rate)
        }
        _ => (0.0, 0.0),
    };
    let obs = Observations {
        head,
        head_unix,
        now_unix: now,
        work_minutes_on_top: work_minutes,
        deepest_reorg_24h: 0,
        deepest_reorg_7d: 0,
    };
    let view = settlement::view(&state, &value, &obs, &Policy::default_wallet());
    Ok(json!({"state": state, "observations": obs, "view": view, "recent_difficulty_per_second": head_rate,
        "note": "reorganisation depth is 0 here: the CLI keeps no header history; the engine measures it"}))
}

fn run(cli: Cli) -> Result<()> {
    let rpc = Rpc::new(&cli.rpc);
    match &cli.cmd {
        Cmd::Create { password_file, words_file } => {
            let path = cli.vault.clone().ok_or_else(|| Error::Input("--vault is needed".into()))?;
            if path.exists() {
                return Err(Error::Input("that vault file exists; choose another path".into()));
            }
            if let Some(w) = &words_file {
                if w.exists() {
                    return Err(Error::Input("that words file exists; choose another path".into()));
                }
            }
            let password = read_password(password_file)?;
            let phrase = mnemonic::new_phrase();
            let signer = SoftwareSigner::from_phrase(&phrase, 0)?;
            let secret = Secret { kind: SecretKind::Phrase, text: phrase.to_string() };
            let public = Public { address: signer.address(), kind: SecretKind::Phrase, created_unix: vault::now_unix(), chain_id_hint: None };
            let file = vault::seal(&secret, password.as_bytes(), public, KdfParams::default_desktop())?;
            fs::write(&path, serde_json::to_vec_pretty(&file)?)?;
            println!("address {}", signer.address());
            println!("vault   {}", path.display());
            // the words: to a file the owner names (mode 0600, the terminal never sees them), or to the terminal
            // after a warning and a press of enter (a study's create printed them to a logged terminal with no
            // warning, 14 October 2026; the owner discarded that address)
            if let Some(w) = words_file {
                write_private(&w, phrase.as_str())?;
                println!("words   written to {} (mode 0600; anyone with them has your IGN; keep the file off any shared or logged place)", w.display());
            } else {
                println!("the next 24 lines are your recovery words: a logged terminal records them; press enter on a terminal that is not logged (or run again with --words-file)");
                io::stdout().flush()?;
                let mut line = String::new();
                io::stdin().read_line(&mut line)?;
                println!("words (shown once; write them down; anyone with them has your IGN):");
                for (i, w) in phrase.split(' ').enumerate() {
                    println!("  {:2}. {}", i + 1, w);
                }
            }
        }
        Cmd::Import { password_file, stdin } => {
            let path = cli.vault.clone().ok_or_else(|| Error::Input("--vault is needed".into()))?;
            if path.exists() {
                return Err(Error::Input("that vault file exists; choose another path".into()));
            }
            if !stdin {
                return Err(Error::Input("pass --stdin and pipe the words or the key in".into()));
            }
            let text = read_stdin()?;
            let t = text.trim();
            let (secret, signer) = if t.starts_with("0x") || t.len() == 64 {
                let s = SoftwareSigner::from_key_hex(t)?;
                (Secret { kind: SecretKind::Key, text: t.to_string() }, s)
            } else {
                let phrase = mnemonic::check_phrase(t)?;
                let s = SoftwareSigner::from_phrase(&phrase, 0)?;
                (Secret { kind: SecretKind::Phrase, text: phrase.to_string() }, s)
            };
            let password = read_password(password_file)?;
            let public = Public { address: signer.address(), kind: secret.kind.clone(), created_unix: vault::now_unix(), chain_id_hint: None };
            let mut file = vault::seal(&secret, password.as_bytes(), public, KdfParams::default_desktop())?;
            file.backed_up = true;
            fs::write(&path, serde_json::to_vec_pretty(&file)?)?;
            println!("address {}", signer.address());
        }
        Cmd::Address => {
            let file = load_vault(&cli.vault)?;
            println!("{}", json!({"address": file.public.address, "kind": file.public.kind, "backed_up": file.backed_up}));
        }
        Cmd::Derive { index } => {
            let phrase = mnemonic::check_phrase(&read_stdin()?)?;
            let key = mnemonic::derive(&phrase, *index)?;
            println!("{} {}", mnemonic::path(*index), Address::from_public_key(key.verifying_key()));
        }
        Cmd::Prepare { from, to, amount, data } => {
            let env = prepare(&rpc, &Address::parse(from)?, &Address::parse(to)?, amount, data)?;
            println!("{}", serde_json::to_string_pretty(&env)?);
        }
        Cmd::Sign { password_file } => {
            let env: UnsignedEnvelope = serde_json::from_str(&read_stdin()?)?;
            env.check()?;
            for s in &env.sentences {
                eprintln!("  {s}");
            }
            let signer = open_signer(&cli, password_file)?;
            let signed = env.sign(&signer)?;
            println!("{}", serde_json::to_string_pretty(&signed)?);
        }
        Cmd::Broadcast => {
            let signed: SignedEnvelope = serde_json::from_str(&read_stdin()?)?;
            let hash = send_raw_via(&rpc, &signed.raw)?;
            println!("{}", hex::encode(&hash));
        }
        Cmd::Send { to, amount, password_file, data } => {
            let signer = open_signer(&cli, password_file)?;
            let env = prepare(&rpc, &signer.address(), &Address::parse(to)?, amount, data)?;
            for s in &env.sentences {
                eprintln!("  {s}");
            }
            let signed = env.sign(&signer)?;
            let hash = send_raw_via(&rpc, &signed.raw)?;
            println!("{}", hex::encode(&hash));
        }
        Cmd::Simulate { from, to, amount, data } => {
            let from_a = Address::parse(from)?;
            let env = prepare(&rpc, &from_a, &Address::parse(to)?, amount, data)?;
            for s in &env.sentences {
                println!("  {s}");
            }
            let call = json!({
                "from": from_a.to_checksum(), "to": env.transaction.to().map(|a| a.to_checksum()),
                "value": env.transaction.value().to_hex(), "data": hex::encode(env.transaction.data()),
                "gas": U256::from_u64(env.transaction.gas_limit()).to_hex(),
            });
            match rpc.eth_call(&call) {
                Ok(out) => println!("  eth_call: ok, {} bytes returned", out.len()),
                Err(e) => println!("  this would fail: {e}"),
            }
            match rpc.trace_call(&call) {
                Ok(trace) => println!("  trace: {}", serde_json::to_string(&trace)?),
                Err(e) => println!("  simulated without a trace ({e})"),
            }
        }
        Cmd::Settlement { hash } => {
            let bytes = hex::decode(hash)?;
            if bytes.len() != 32 {
                return Err(Error::Input("a hash has 32 bytes".into()));
            }
            let mut h = [0u8; 32];
            h.copy_from_slice(&bytes);
            let v = settlement_view(&rpc, &h)?;
            println!("{}", serde_json::to_string_pretty(&v)?);
        }
        Cmd::SignTx { password_file, network } => {
            let tx: Transaction = serde_json::from_str(&read_stdin()?)?;
            let signer = open_signer(&cli, password_file)?;
            let from = signer.address();
            let shown = display::sentences(&tx, &from, network);
            let signed = igneum_wallet_core::signer::sign_transaction(&signer, &tx, &from, network, &shown)?;
            println!("{}", json!({"raw": hex::encode(&signed.raw), "hash": hex::encode(&signed.hash), "from": from}));
        }
        Cmd::SignHash { hash } => {
            let bytes = hex::decode(hash)?;
            if bytes.len() != 32 {
                return Err(Error::Input("a hash has 32 bytes".into()));
            }
            let mut h = [0u8; 32];
            h.copy_from_slice(&bytes);
            let signer = open_signer(&cli, &None)?;
            let sig = signer.sign_hash(&h)?;
            println!("{}", json!({"address": signer.address(), "r": hex::encode(&sig.r), "s": hex::encode(&sig.s), "recovery_id": sig.recovery_id, "rsv": hex::encode(&sig.to_rsv())}));
        }
        Cmd::Chain => {
            let chain = chain_for(&rpc)?;
            let head = rpc.block_number()?;
            println!(
                "{}",
                serde_json::to_string_pretty(&json!({
                    "name": chain.name, "chain_id": chain.chain_id, "genesis": chain.genesis, "pinned": chain.genesis.is_some() && !chain.name.starts_with("chain "),
                    "registry": chain.registry().map(|r| r.to_checksum()), "authority": chain.authority, "head": head, "rpc": rpc.url(),
                }))?
            );
        }
        Cmd::Bond { key, amount, exit, password_file, gas, dry_run } => {
            let k = authority::parse_key(key)?;
            let data = match exit {
                None => authority::bond_data(&k),
                Some(e) => authority::set_exit_data(&k, &Address::parse(e)?),
            };
            registry_action(&cli, &rpc, display::parse_ign(amount)?, data, password_file, *gas, *dry_run)?;
        }
        Cmd::SetSigner { key, signer, password_file, gas, dry_run } => {
            let k = authority::parse_key(key)?;
            let s = authority::parse_key(signer)?;
            registry_action(&cli, &rpc, U256::ZERO, authority::set_signer_data(&k, &s), password_file, *gas, *dry_run)?;
        }
        Cmd::SetExit { key, exit, amount, password_file, gas, dry_run } => {
            let k = authority::parse_key(key)?;
            registry_action(&cli, &rpc, display::parse_ign(amount)?, authority::set_exit_data(&k, &Address::parse(exit)?), password_file, *gas, *dry_run)?;
        }
        Cmd::Claim { sk_file, key, to, c_hex, claim_nonce, prover, out, pi_file, share, reveal, password_file, gas, dry_run } => {
            let to_a = Address::parse(to)?;
            let chain = chain_for(&rpc)?;
            if *reveal {
                let sk_path = sk_file.as_ref().ok_or_else(|| Error::Input("--reveal needs --sk-file".into()))?;
                let text = Zeroizing::new(fs::read_to_string(sk_path)?);
                let sk = Zeroizing::new(claim::seed_from_hex(&text)?);
                eprintln!("  DEV-ONLY: the lab registry's stand-in: the identity {} (pk = G(sk)) is claimed by revealing its secret on the chain to {}", hex::encode(&claim::bond_pk(&sk)), to_a);
                registry_action(&cli, &rpc, U256::ZERO, authority::claim_reveal_data(&sk, &to_a), password_file, *gas, *dry_run)?;
                return Ok(());
            }
            // C in the claim message: the chain id as u64 big-endian, the envelope's C (the project's ruling of 10:30 UK,
            // 11 Oct 2026; the hash team's prover takes the chain id in decimal); --c-hex names another chain id for
            // a fixture's row, never for a real claim
            let claim_chain: u64 = match c_hex {
                Some(h) => h.trim().parse().map_err(|_| Error::Input("--c-hex now takes the chain id in decimal (the envelope's C is u64be(chain id))".into()))?,
                None => chain.chain_id,
            };
            if claim_chain != chain.chain_id {
                eprintln!("  the claim message is bound to chain id {claim_chain}, not this node's {}: a fixture's row, the registry will refuse it", chain.chain_id);
            }
            let (c, label) = claim::default_c(&Chain::unpinned(claim_chain, None));
            eprintln!("  C in the claim message: {} ({label})", hex::encode(&c));
            let (pk, s0, pi) = match (prover, sk_file, pi_file) {
                (Some(prover), Some(sk), _) => {
                    let out = out.clone().unwrap_or_else(|| sk.with_extension("claim.bin"));
                    eprintln!("  proving the claim with {}: {}", prover.display(), claim::PROVER_WORDS);
                    let p = claim::prove_with(prover, sk, claim_chain, *claim_nonce, &to_a, &out)?;
                    eprintln!("  proof {} bytes in {:.0} ms, written to {}; pk {}", p.pi.len(), p.prove_ms, out.display(), hex::encode(&p.pk));
                    (p.pk, p.share, p.pi)
                }
                (_, _, Some(file)) => {
                    let pk = authority::parse_key(key.as_deref().ok_or_else(|| Error::Input("--key is needed with --pi-file".into()))?)?;
                    let s0 = authority::parse_key(share.as_deref().ok_or_else(|| Error::Input("--share is needed with --pi-file".into()))?)?;
                    let pi = fs::read(file)?;
                    authority::check_pi_bounded(&pi, authority::PI_CLAIM_MAX_BYTES)?;
                    (pk, s0, pi)
                }
                _ => return Err(Error::Input("give --prover with --sk-file, or --pi-file with --key and --share".into())),
            };
            eprintln!("  the claim names the identity {} and binds the claimant {} (nonce {})", hex::encode(&pk), to_a, claim_nonce);
            registry_action(&cli, &rpc, U256::ZERO, authority::claim_data(&pk, &to_a, &s0, &pi)?, password_file, *gas, *dry_run)?;
        }
        Cmd::Identity { seed_file } => {
            let text = Zeroizing::new(fs::read_to_string(seed_file)?);
            let sk = Zeroizing::new(claim::seed_from_hex(&text)?);
            println!("{}", hex::encode(&claim::bond_pk(&sk)));
        }
        Cmd::SigningKey { seed_file } => {
            let text = Zeroizing::new(fs::read_to_string(seed_file)?);
            let seed = Zeroizing::new(claim::seed_from_hex(&text)?);
            println!("{}", hex::encode(&claim::signing_key(&seed)));
        }
        Cmd::Draw { key, at } => {
            let chain = chain_for(&rpc)?;
            let s = rpc.igneum_sortition()?;
            let a = rpc.igneum_authority().unwrap_or(json!({}));
            let params = s.get("params").cloned().unwrap_or(json!({}));
            let p = draw::Params {
                expected: params.get("Expected").and_then(|v| v.as_u64()).unwrap_or(1),
                max_round: params.get("MaxRound").and_then(|v| v.as_u64()).unwrap_or(16),
                unbonded_round: params.get("UnbondedRound").and_then(|v| v.as_u64()).unwrap_or(17),
                delta_secs: params.get("Delta").and_then(|v| v.as_u64()).unwrap_or(20),
            };
            // the epoch length in force now: the last change at or under the head, else the base value
            let head = s.get("head").and_then(|v| v.as_u64()).unwrap_or(0);
            let mut epoch_blocks = params.get("EpochBlocks").and_then(|v| v.as_u64()).unwrap_or(5760);
            if let Some(changes) = params.get("EpochChanges").and_then(|v| v.as_array()) {
                for c in changes {
                    if c.get("Height").and_then(|h| h.as_u64()).unwrap_or(u64::MAX) <= head {
                        epoch_blocks = c.get("EpochBlocks").and_then(|e| e.as_u64()).unwrap_or(epoch_blocks);
                    }
                }
            }
            let key_bytes = match key {
                Some(k) => authority::parse_key(k)?,
                None => authority::parse_key(s.get("bondKey").and_then(|v| v.as_str()).ok_or_else(|| Error::Rpc("the node has no bond key of its own; pass --key".into()))?)?,
            };
            let height = at.unwrap_or_else(|| s.get("next").and_then(|v| v.as_u64()).unwrap_or(head + 1));
            // the bond and the total at the snapshot: the draw reads the snapshot's state, not the head's
            let snapshot = s.get("snapshotBlock").and_then(|v| v.as_u64()).unwrap_or(0);
            let snap_tag = U256::from_u64(snapshot).to_hex();
            let bond_now = rpc.igneum_bond_of(&key_bytes, "latest")?;
            let bond_snap = rpc.igneum_bond_of(&key_bytes, &snap_tag)?;
            let registry = chain.registry().ok_or_else(|| Error::Input(format!("{} carries no bond registry", chain.name)))?;
            // the registry's own read (128 bytes on version 3, with the exit address)
            let read = rpc
                .eth_call(&json!({"to": registry.to_checksum(), "data": hex::encode(&authority::bond_data(&key_bytes))}))
                .ok()
                .and_then(|out| authority::decode_read(&out).ok());
            let exit_addr = read.as_ref().and_then(|r| r.exit).map(|e| e.to_checksum());
            let total_snap = U256::from_hex(
                rpc.call("eth_getBalance", json!([registry.to_checksum(), snap_tag]))?.as_str().unwrap_or("0x0"),
            )?;
            let amount_snap = U256::from_hex(bond_snap.get("amount").and_then(|v| v.as_str()).unwrap_or("0x0"))?;
            let seed_hex = s.get("seed").and_then(|v| v.as_str()).unwrap_or("0x0");
            let seed = authority::parse_key(seed_hex).unwrap_or([0u8; 32]);
            let share = draw::share(&amount_snap, &total_snap, p.expected, epoch_blocks);
            let round = draw::eligible_round(&p, &seed, &key_bytes, share.units, height);
            let node_round = if s.get("bondKey").and_then(|v| v.as_str()).map(|k| k.eq_ignore_ascii_case(&hex::encode(&key_bytes))).unwrap_or(false)
                && at.is_none()
            {
                s.get("eligibleRound").and_then(|v| v.as_u64())
            } else {
                None
            };
            let round_words = if round > p.max_round {
                format!("not drawn by round {} at height {}: only the unbonded round {} ({} s after the parent) is open to it", p.max_round, height, p.unbonded_round, p.unbonded_round * p.delta_secs)
            } else {
                format!("eligible at round {} of height {} ({} s after the parent at the earliest)", round, height, round * p.delta_secs)
            };
            println!(
                "{}",
                serde_json::to_string_pretty(&json!({
                    "chain": chain.name,
                    "registry": registry.to_checksum(),
                    "rule": {"authority_active": a.get("active"), "sortition_active": s.get("active"), "expected": p.expected, "max_round": p.max_round, "unbonded_round": p.unbonded_round, "delta_secs": p.delta_secs, "epoch_blocks": epoch_blocks},
                    "key": hex::encode(&key_bytes),
                    "bond_now": {"amount_ign": display::ign(&U256::from_hex(bond_now.get("amount").and_then(|v| v.as_str()).unwrap_or("0x0"))?, 6), "account": bond_now.get("account"), "signer": bond_now.get("signer"), "bonded": bond_now.get("bonded"), "exit": exit_addr},
                    "snapshot": {"block": snapshot, "epoch": s.get("epoch"), "seed": seed_hex, "bond_ign": display::ign(&amount_snap, 6), "total_ign": display::ign(&total_snap, 6)},
                    "draw": {"units": share.units, "share": share.share, "share_percent": format!("{:.4}", share.share * 100.0), "round0_chance": share.round0_chance, "round0_heights_per_epoch": share.round0_heights_per_epoch},
                    "height": height,
                    "eligible_round": round,
                    "eligible_words": round_words,
                    "node_reading": node_round,
                    "agrees_with_node": node_round.map(|r| r == round),
                    "note": "the share is bond over the bonded total at the snapshot (the last block of epoch e minus 2); a bond posted now enters the draw two epochs on; a key is paid only when its block is sealed, and hashing decides only who is paid (the frozen rule set)",
                }))?
            );
        }
        Cmd::Receipts { file, pool_key, member_key, json: as_json } => {
            let text = fs::read_to_string(file)?;
            let rs = receipts::read_lines(&text)?;
            let report = receipts::verify(&rs, pool_key, member_key)?;
            if *as_json {
                println!("{}", serde_json::to_string_pretty(&report)?);
            } else {
                println!("receipts {} verified {} bad {}", report.receipts, report.verified, report.bad);
                println!("your count {} the pool's running total {}{}", report.weight_counted, report.pool_total, if report.weight_counted == report.pool_total && report.bad == 0 { " (they agree)" } else { " (THEY DO NOT AGREE)" });
                println!("pool key {} member key {}", report.pool_key, report.member_key);
                for v in &report.verdicts {
                    if v.ok {
                        println!("  {:>6}  ok      weight {:>6}  total {:>8}  share {}", v.seq, v.weight, v.total, &v.share_hash[..v.share_hash.len().min(16)]);
                    } else {
                        println!("  {:>6}  BAD     weight {:>6}  total {:>8}  {}", v.seq, v.weight, v.total, v.reason);
                    }
                }
                if report.bad > 0 {
                    std::process::exit(2);
                }
            }
        }
    }
    Ok(())
}

/// A transaction to the chain's bond registry: built, shown, and signed and broadcast unless --dry-run.
fn registry_action(cli: &Cli, rpc: &Rpc, value: U256, data: Vec<u8>, password_file: &Option<PathBuf>, gas: Option<u64>, dry_run: bool) -> Result<()> {
    let chain = chain_for(rpc)?;
    let registry = chain.registry().ok_or_else(|| {
        Error::Refused(format!(
            "{} carries no bond registry the wallet knows (genesis {}); a bond is only built for a pinned authority chain",
            chain.name,
            chain.genesis.clone().unwrap_or_else(|| "unread".into())
        ))
    })?;
    let from = if dry_run && cli.vault.is_none() && cli.key_hex.is_none() {
        Address::default()
    } else {
        open_signer(cli, password_file)?.address()
    };
    let env = prepare_call(rpc, &from, &registry, value, data, gas)?;
    for s in &env.sentences {
        eprintln!("  {s}");
    }
    if dry_run {
        println!("{}", serde_json::to_string_pretty(&env)?);
        return Ok(());
    }
    let signer = open_signer(cli, password_file)?;
    let signed = env.sign(&signer)?;
    let hash = send_raw_via(&rpc, &signed.raw)?;
    println!("{}", hex::encode(&hash));
    Ok(())
}

fn main() {
    let cli = Cli::parse();
    if let Err(e) = run(cli) {
        let _ = writeln!(io::stderr(), "igneum-wallet: {e}");
        std::process::exit(1);
    }
}
