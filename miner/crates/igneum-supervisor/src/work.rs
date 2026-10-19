//! Where work comes from: a pool over IS/1 (docs/ecosystem/DESIGN.md 2.3 to 2.5, pool/internal/stratum), a node
//! over its mining RPC (docs/protocol-a/DESIGN.md 4.5: `igneum_getWork`, `igneum_submitWork`, with the `eth_`
//! forms as the fallback), or the local stub. The supervisor holds the network; workers never do. Network sources
//! run on their own thread and never block the tick: `submit` enqueues, `outcomes` drains.

use crate::net::{self, Url};
use crate::settings::WorkSourceSetting;
use igneum_worker_proto::{self as proto, stubhash};
use std::io::Write as _;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::mpsc::{self, Receiver, Sender};
use std::sync::Arc;
use std::thread;
use std::time::{Duration, Instant, SystemTime, UNIX_EPOCH};

#[derive(Debug, Clone)]
pub struct JobTemplate {
    pub job_id: String,
    pub height: u64,
    /// The sealing hash as the node gives it.
    pub header: [u8; 32],
    /// The block target, big-endian.
    pub boundary: [u8; 32],
    /// The pool's share target (the boundary when solo).
    pub share_target: [u8; 32],
    /// The nonce range this miner owns for the job (a pool session's range; the whole space for solo).
    pub nonce_start: u64,
    pub nonce_count: u64,
    pub issued: Instant,
    /// The template identifier: the sealing hash for a node, the pool's job id for a pool.
    pub template: String,
    /// The epoch the source states for the template, when it does (a node's `epoch`); the supervisor derives it
    /// from the height otherwise.
    pub epoch: Option<u64>,
    /// The parameter document the source names for this template (a node's `igneum_getWork.params`); Null when
    /// the source states none (the session's document stands).
    pub params: serde_json::Value,
    /// The source's digest of that document (a node's `paramsDigest`), when stated.
    pub params_digest: Option<String>,
    /// A parameter change the source announces ahead (a node's nextParams, nextParamsDigest, nextParamsHeight):
    /// the document, its digest when stated, and the first height it is in force at.
    pub next_params: Option<(serde_json::Value, Option<String>, u64)>,
    /// The keyed form (the protocol team's KEYED-FORM.md): a node's `keyedRounds` on a keyed template (S; 0 or absent on an
    /// unkeyed one) and `bondKey`, the public key of the bonded identity whose secret the template's attempts need
    /// (hex of 32 bytes). A template under another identity's key than this miner's is refused by name.
    pub keyed_rounds: u32,
    pub bond_key: Option<[u8; 32]>,
    /// The share lane (docs/igneumhash/LAUNCH-GUARDS.md 2.9; the node's consensus/igneum/mineshares.go): Some when
    /// this template is the share search. Its `header` is then H_s = KECCAK(RLP(shareParent, shareHeight, address,
    /// extraNonce)), the search is unkeyed on the twin document at shareHeight, `boundary` and `share_target` are
    /// both the node's share target, and a result under it is submitted through igneum_submitShare, never as a
    /// block: a share never becomes a block (2.1a), so the share header and the block header are two searches.
    pub share: Option<ShareJob>,
    /// VectorWork (docs/product-a/ROUTER.md; the pool's form of 13 October 2026, main 82df5cec): the coordinate this
    /// template is for, with its own difficulty (D_c(next)), the coordinate's last block height when the pool states
    /// it, and the chain's subsidy per block in IGN; None on a one-form job (today's pool, a node).
    pub coord: Option<u8>,
    /// The coordinate's name as the pool states it (coord_name: matrix, bandwidth, fabric, graph); None on a one-form job.
    pub coord_name: Option<String>,
    pub coord_difficulty: Option<f64>,
    pub coord_last_height: Option<u64>,
    /// The pool's pay list at the job's height (0.1.76, F-290): the coordinate indices the chain pays, ascending; None when
    /// the pool sends none (a one-form chain, or a job before its first pay-list read). A coordinate outside it is unpaid or
    /// retired: dropped on arrival, never restored; it returns with a job.
    pub paid_coords: Option<Vec<u8>>,
    /// F-496 and F-543 (pool 0.1.92 and 0.1.96): the pool's worth of this lane's share weight against the chain's pay
    /// (`lane_worth`, one window behind, no forecast), None when the notify carries none or an empty string (unknown);
    /// `lane_worth_unknown` is true when the field came as "" (the lane is tried as uncovered); `lane_workers` the count
    /// of the pool's workers with a sealer-accepted share on this lane in the last 64 blocks (0: an uncovered paid lane).
    pub lane_worth: Option<f64>,
    pub lane_worth_unknown: bool,
    pub lane_workers: Option<u64>,
    pub subsidy_ign: Option<f64>,
    /// The period's programme anchor as the sealer resolves it (ecosystem, 03:2x UK 13 October 2026: the keyed
    /// documents hash shares with it and the document's own field is zeros), 64 hex characters, and the height it is
    /// for; None when the notify carries none (the document's own anchor stands).
    pub program_anchor: Option<String>,
    pub anchor_height: Option<u64>,
    /// The chain the source names on its job (the pool's chain_id on every notify); 0 when the source names none.
    pub chain_id: u64,
}

/// What a share's submission names beside the nonce and the mix: the share height the node served and the extra
/// nonce this session chose (part of the hashed header). `shares_per_block` is the node's operating count.
#[derive(Debug, Clone, PartialEq)]
pub struct ShareJob {
    pub share_height: u64,
    pub extra_nonce: u64,
    pub shares_per_block: u64,
    /// The parent the share was mined on (the template's shareParent): sent as igneum_submitShare's sixth parameter
    /// under share gossip (the protocol team, 11 October 2026), so a share on a sibling is refused as "stale parent" by
    /// name before any work check, never read as invalid work; a node without the parameter takes the five.
    pub parent: [u8; 32],
}

/// The share lane's identity for a node session: the payout address the hashed header carries (design G) and the
/// session's extra nonce (a u64 of this miner's own choosing; a fresh one each session so a restart never replays
/// a (address, extraNonce, nonce) the node has seen).
#[derive(Debug, Clone, Copy, PartialEq)]
pub struct ShareLane {
    pub address: [u8; 20],
    pub extra_nonce: u64,
}

impl ShareLane {
    /// From the payout address (0x and 40 hex); None for any other form, and then the node's share target is left
    /// alone and the block search stands.
    pub fn for_address(payout_address: &str) -> Option<Self> {
        let h = payout_address.trim().strip_prefix("0x").or_else(|| payout_address.trim().strip_prefix("0X"))?;
        if h.len() != 40 {
            return None;
        }
        let v = proto::hex_decode(&h.to_lowercase())?;
        let address: [u8; 20] = v.as_slice().try_into().ok()?;
        let nanos = SystemTime::now().duration_since(UNIX_EPOCH).map(|d| d.as_nanos() as u64).unwrap_or(1);
        Some(ShareLane { address, extra_nonce: nanos.max(1) })
    }
}

/// RLP of one byte string (the node's go-ethereum/rlp form: a single byte under 0x80 is itself; otherwise a
/// length prefix at 0x80 for under 56 bytes, 0xb7 plus the length's length beyond).
fn rlp_bytes(b: &[u8]) -> Vec<u8> {
    if b.len() == 1 && b[0] < 0x80 {
        return b.to_vec();
    }
    let mut out = Vec::with_capacity(b.len() + 9);
    rlp_len(&mut out, b.len(), 0x80);
    out.extend_from_slice(b);
    out
}

/// RLP of an unsigned integer: its minimal big-endian bytes as a string (zero is the empty string, 0x80).
fn rlp_uint(n: u64) -> Vec<u8> {
    let b = n.to_be_bytes();
    let s = b.iter().position(|&x| x != 0).unwrap_or(8);
    rlp_bytes(&b[s..])
}

/// RLP of a list of already encoded items (0xc0 plus the payload length under 56, 0xf7 plus the length's length
/// beyond).
fn rlp_list(items: &[Vec<u8>]) -> Vec<u8> {
    let payload: Vec<u8> = items.iter().flat_map(|i| i.iter().copied()).collect();
    let mut out = Vec::with_capacity(payload.len() + 9);
    rlp_len(&mut out, payload.len(), 0xc0);
    out.extend_from_slice(&payload);
    out
}

fn rlp_len(out: &mut Vec<u8>, len: usize, base: u8) {
    if len < 56 {
        out.push(base + len as u8);
    } else {
        let b = (len as u64).to_be_bytes();
        let s = b.iter().position(|&x| x != 0).unwrap_or(7);
        out.push(base + 55 + (8 - s) as u8);
        out.extend_from_slice(&b[s..]);
    }
}

/// H_s, the share's hashed header: KECCAK(RLP(shareParent, shareHeight, address, extraNonce)), the node's
/// ShareHeaderHash (consensus/igneum/mineshares.go) and the reference client's shareHeader
/// (pool/cmd/share-miner/main.go) byte for byte.
pub fn share_header(parent: &[u8; 32], share_height: u64, address: &[u8; 20], extra_nonce: u64) -> [u8; 32] {
    use sha3::{Digest, Keccak256};
    let enc = rlp_list(&[rlp_bytes(parent), rlp_uint(share_height), rlp_bytes(address), rlp_uint(extra_nonce)]);
    Keccak256::digest(&enc).into()
}

/// The digest of a parameter document as the miner tags it: sha-256 over the canonical JSON (sorted keys, no
/// whitespace); "default" for a null document. The spec's P (BLAKE3 over the canonical bytes) is the reference's
/// and the node's; the document digest is the same identity on the miner's side until the node exposes P.
pub fn params_digest(doc: &serde_json::Value) -> String {
    use sha2::{Digest, Sha256};
    if doc.is_null() {
        return "default".into();
    }
    fn canon(v: &serde_json::Value, out: &mut String) {
        match v {
            serde_json::Value::Object(m) => {
                let mut keys: Vec<&String> = m.keys().collect();
                keys.sort();
                out.push('{');
                for (i, k) in keys.iter().enumerate() {
                    if i > 0 {
                        out.push(',');
                    }
                    out.push_str(&serde_json::to_string(k).unwrap_or_default());
                    out.push(':');
                    canon(&m[*k], out);
                }
                out.push('}');
            }
            serde_json::Value::Array(a) => {
                out.push('[');
                for (i, x) in a.iter().enumerate() {
                    if i > 0 {
                        out.push(',');
                    }
                    canon(x, out);
                }
                out.push(']');
            }
            other => out.push_str(&other.to_string()),
        }
    }
    let mut c = String::new();
    canon(doc, &mut c);
    igneum_worker_proto::hex_encode(&Sha256::digest(c.as_bytes()))
}

#[derive(Debug, Clone)]
pub struct Share {
    pub job_id: String,
    pub nonce: u64,
    pub mix: Vec<u8>,
    pub hash: [u8; 32],
    pub worker_name: String,
    /// Under a keyed document (SPEC 0.9, bit 10): the bond's public key and the S shares the verifier produced
    /// under the secret; the node or the pool replays the walk with them. None for an unkeyed document.
    pub keyed: Option<crate::bond::KeyedProof>,
}

#[derive(Debug, Clone, PartialEq)]
pub enum SubmitOutcome {
    Accepted,
    /// Too old for the source (the template moved on).
    Stale,
    /// Never sent: a solo result for a template this miner had already sealed, or for one the node had moved
    /// past (one accepted solution per template is all a chain takes); counted as cancelled work, not logged.
    Superseded,
    /// The source recomputed the proof and disagrees (mix or boundary): the reference and the verifier differ.
    Invalid(String),
    /// Refused for another stated reason.
    Rejected(String),
    /// Refused BEFORE acceptance by the pool's admission bounds (pool/0.1.6 and later; F-50, 12 October 2026): the
    /// reason's second field names the case (pool at carriage, address at cap, the node's pool is at capacity, the
    /// list bound). Not a rejected share: it clears itself as the chain carries, and never counts against the session.
    Held(String),
}

/// The pool's admission-bound prefix (ecosystem's reply form of 11:11 UK, 12 October 2026: result false, error code
/// 23, the reason starting with this; the case is the second colon-field).
pub const HELD_PREFIX: &str = "refused before acceptance";

/// The pool's submit bucket (ecosystem, 11:4x UK, 12 October 2026: 50 submits a second with a burst of 50; a share
/// over it is dropped unread as "rate"). G-34 (the project's go; pool/0.1.15 on the public pool since 16:25Z, 12 October
/// 2026, with the persisted step and the probe): the session's own pacing under the bucket is gone; its token bucket
/// is the pool's own figures, so the pool's bucket is the only floor on what is sent and a share waits at most
/// 1/SUBMIT_PER_S for its token, never dropped.
pub const SUBMIT_PER_S: f64 = 50.0;
pub const SUBMIT_BURST: f64 = 50.0;

/// The bucket after one submit: `tokens` refilled over `elapsed_s`, one taken; the wait (seconds) before the
/// submit may go when the bucket is empty.
pub fn submit_pace(tokens: f64, elapsed_s: f64) -> (f64, f64) {
    let t = (tokens + elapsed_s.max(0.0) * SUBMIT_PER_S).min(SUBMIT_BURST);
    if t >= 1.0 { (t - 1.0, 0.0) } else { (0.0, (1.0 - t) / SUBMIT_PER_S) }
}

/// The case word of an admission-bound reason: "refused before acceptance: pool at carriage: 300 shares ..." reads
/// "pool at carriage"; None when the reason is not of that form.
pub fn held_case(reason: &str) -> Option<String> {
    let rest = reason.strip_prefix(HELD_PREFIX)?.trim_start_matches(':').trim();
    let case = rest.split(':').next()?.trim();
    (!case.is_empty()).then(|| case.to_string())
}

#[derive(Debug, Clone, PartialEq)]
pub struct SourceEconomics {
    pub difficulty: f64,
    pub block_interval_s: f64,
    /// The miner's part of the block subsidy after the protocol allocation (`igneum_blockReward.miner`), IGN,
    /// when the source reports it; none means the display says so.
    pub subsidy_ign: Option<f64>,
    /// Mean priority fees per block over the recent range (`igneum_blockReward.priorityFees`), IGN.
    pub fees_ign: f64,
    /// The pool's fee as a fraction, when its disclosure document was read; 0 for solo.
    pub pool_fee: Option<f64>,
    pub solo: bool,
    /// The protocol fund's part of the newest block as the chain reports it (`igneum_blockReward.fund`), IGN; 0
    /// when unreported. The chain's consensus rule, shown as such.
    pub fund_ign: f64,
}

pub trait WorkSource: Send {
    fn name(&self) -> String;
    fn algo(&self) -> String;
    /// The directory a pool session writes its receipts file into (`<host>-<session start unix>.jsonl`, one
    /// `mining.receipt` line per accepted share as the pool sent it: the settlement team's form, 12 October 2026);
    /// a node session keeps nothing here. Set before `connect`; the default ignores it.
    fn set_receipts_dir(&mut self, _dir: &std::path::Path) {}
    fn connect(&mut self, payout_address: &str, worker_name: &str) -> Result<(), String>;
    /// A new job when there is one.
    fn poll(&mut self) -> Result<Option<JobTemplate>, String>;
    /// Enqueue a share; the outcome arrives through `outcomes`.
    fn submit(&mut self, share: &Share) -> Result<(), String>;
    /// Outcomes that arrived since the last call: (job id, nonce, outcome).
    fn outcomes(&mut self) -> Vec<(String, u64, SubmitOutcome)>;
    fn economics(&self) -> SourceEconomics;
    /// The candidate's parameter document as the source states it (a node's `igneum_getWork.params`), passed to
    /// the worker's `configure.params` verbatim; Null selects the reference's defaults for the candidate.
    fn params(&self) -> serde_json::Value {
        serde_json::Value::Null
    }
    /// The source's own digest of the parameter document (a node's `paramsDigest`, P for IgneumHash), the key
    /// datasets are tagged by beside the algorithm id; None when the source states none.
    fn params_digest(&self) -> Option<String> {
        None
    }
    /// The miner's work-authority key for signed shares (a pool that advertises `signed_shares`); ignored by
    /// sources without the form.
    fn set_signer(&mut self, _k: std::sync::Arc<crate::minerkey::MinerKey>) {}
    /// F-E33: whether this source signs its authorize and shares (a pool source with the signer attached).
    fn signs(&self) -> bool {
        false
    }
    /// The share lane by node (a node source with several nodes): each node's accepted and refused counts and
    /// reasons; empty for a pool or the stub.
    fn node_answers(&mut self) -> Vec<crate::state::NodeAnswers> {
        Vec::new()
    }
    /// The dev fee's templates this session (devfee.rs): how many so far, and the recent job ids the supervisor
    /// marks; (0, empty) for a source without the fee (a pool, the stub).
    fn dev_fee_jobs(&mut self) -> (u64, Vec<String>) {
        (0, Vec::new())
    }
    /// The pool's fee answer to the dev-fee declaration (16.3): None before a pool answered or for a source that is
    /// no pool; Some(None) for a pool that carried no pool_fee; Some(Some(answer)) when it spoke.
    fn pool_fee(&mut self) -> Option<Option<crate::devfee::PoolFeeAnswer>> {
        None
    }
    /// The pool's share step for this session (ecosystem, 12 October 2026: `share_step` on mining.notify; the job's
    /// share target is the node's over 2^step and an accepted share weighs 2^step on the chain); 0 for the count
    /// form, a node session or a pool that names none. Display only: the job's share target is mined as given.
    fn share_step(&mut self) -> u8 {
        0
    }
    /// The pool's receipts as counted (16.3); the default for a source that sends none.
    fn receipts(&mut self) -> crate::state::ReceiptCounts {
        crate::state::ReceiptCounts::default()
    }
    fn disconnect(&mut self);
    fn connected(&self) -> bool;
    /// The last connection-level problem, for the status strip; cleared when the source recovers.
    fn problem(&self) -> Option<String> {
        None
    }
}

fn difficulty_of(target: &[u8; 32]) -> f64 {
    stubhash::difficulty_for_target(target)
}

// ---------------------------------------------------------------------------------------------------------------
// the stub

/// A local source for the stub worker: a new header every 15 s, accepted when the job is current. Labelled "stub"
/// wherever shown.
pub struct StubSource {
    difficulty: f64,
    interval: Duration,
    counter: u64,
    last: Option<Instant>,
    current: Option<JobTemplate>,
    connected: bool,
    outcomes: Vec<(String, u64, SubmitOutcome)>,
}

impl StubSource {
    pub fn new(difficulty: f64) -> Self {
        StubSource { difficulty, interval: Duration::from_secs(15), counter: 0, last: None, current: None, connected: false, outcomes: Vec::new() }
    }

    fn make(&mut self) -> JobTemplate {
        self.counter += 1;
        let now = SystemTime::now().duration_since(UNIX_EPOCH).map(|d| d.as_nanos() as u64).unwrap_or(0);
        let mut header = [0u8; 32];
        header[..8].copy_from_slice(&self.counter.to_le_bytes());
        header[8..16].copy_from_slice(&now.to_le_bytes());
        header[16..24].copy_from_slice(&(now ^ 0xA5A5_A5A5_5A5A_5A5A).to_le_bytes());
        let boundary = stubhash::target_for_difficulty(self.difficulty);
        let template = igneum_worker_proto::hex_encode(&header);
        JobTemplate { job_id: format!("stub-{}", self.counter), height: self.counter, header, boundary, share_target: boundary, nonce_start: 0, nonce_count: u64::MAX, issued: Instant::now(), template, epoch: Some(stubhash::epoch_of(self.counter)), params: serde_json::Value::Null, params_digest: None, next_params: None, keyed_rounds: 0, bond_key: None, share: None, coord: None, coord_difficulty: None, coord_last_height: None, paid_coords: None, lane_worth: None, lane_worth_unknown: false, lane_workers: None, subsidy_ign: None, program_anchor: None, anchor_height: None, chain_id: 0, coord_name: None }
    }
}

impl WorkSource for StubSource {
    fn name(&self) -> String {
        "stub source (local, nothing is mined)".into()
    }
    fn algo(&self) -> String {
        stubhash::STUB_ALGO.into()
    }
    fn connect(&mut self, _payout_address: &str, _worker_name: &str) -> Result<(), String> {
        self.connected = true;
        self.last = None;
        Ok(())
    }
    fn poll(&mut self) -> Result<Option<JobTemplate>, String> {
        if !self.connected {
            return Err("not connected".into());
        }
        let due = self.last.map_or(true, |t| t.elapsed() >= self.interval);
        if due {
            let j = self.make();
            self.last = Some(Instant::now());
            self.current = Some(j.clone());
            Ok(Some(j))
        } else {
            Ok(None)
        }
    }
    fn submit(&mut self, share: &Share) -> Result<(), String> {
        let o = match &self.current {
            Some(j) if j.job_id == share.job_id => SubmitOutcome::Accepted,
            Some(_) => SubmitOutcome::Stale,
            None => SubmitOutcome::Rejected("no job".into()),
        };
        self.outcomes.push((share.job_id.clone(), share.nonce, o));
        Ok(())
    }
    fn outcomes(&mut self) -> Vec<(String, u64, SubmitOutcome)> {
        std::mem::take(&mut self.outcomes)
    }
    fn economics(&self) -> SourceEconomics {
        SourceEconomics { difficulty: self.difficulty, block_interval_s: 15.0, subsidy_ign: Some(0.99), fees_ign: 0.0, pool_fee: Some(0.0), solo: true, fund_ign: 0.0 }
    }
    fn disconnect(&mut self) {
        self.connected = false;
        self.current = None;
    }
    fn connected(&self) -> bool {
        self.connected
    }
}

// ---------------------------------------------------------------------------------------------------------------
// the thread model shared by the network sources

enum Cmd {
    Submit(Share),
    Stop,
}

enum Ev {
    Connected,
    /// The pool's parameter document and its digest, from the subscribe result or a notify.
    Params(serde_json::Value, Option<String>),
    Job(JobTemplate),
    Outcome(String, u64, SubmitOutcome),
    Problem(String),
    Economics(SourceEconomics),
    /// One node's answer to a share on the share lane (by the node's label), beside the one outcome above.
    NodeAnswer(String, SubmitOutcome),
    /// A template requested with the dev payout address (the dev fee, devfee.rs): its job id.
    DevFeeJob(String),
    /// The pool's answer to the dev-fee declaration in the subscribe (16.3); None when it answered nothing.
    PoolFee(Option<crate::devfee::PoolFeeAnswer>),
    /// A pool receipt as checked (devfee::check_receipt): Ok((v1 verified, dev_fee_percent, pool_fee_bps)), or why
    /// its v1 digest did not stand.
    /// A receipt's check and the receipt as the pool sent it (one JSON line for the receipts file).
    Receipt(Result<(bool, u32, u32), String>, String),
    /// The pool's share step on a notify (0 when absent: the count form).
    ShareStep(u8),
}

struct Remote {
    /// The pool's parameter document and digest, when it serves them.
    params: serde_json::Value,
    params_digest: Option<String>,
    tx: Sender<Cmd>,
    rx: Receiver<Ev>,
    handle: Option<thread::JoinHandle<()>>,
    /// Set at stop; the thread reads it between requests and ends without draining its queue.
    stopping: Arc<AtomicBool>,
    connected: bool,
    problem: Option<String>,
    economics: SourceEconomics,
    pending_jobs: Vec<JobTemplate>,
    outcomes: Vec<(String, u64, SubmitOutcome)>,
    /// The share lane by node: each node's accepted and refused counts and reasons, in the order first seen.
    node_answers: Vec<crate::state::NodeAnswers>,
    /// The dev fee's templates this session (devfee.rs): how many, and the last job ids (the supervisor marks
    /// their accepted shares).
    dev_fee_templates: u64,
    dev_fee_jobs: Vec<String>,
    /// The pool's fee answer (16.3): Some(answer) when the pool spoke, Some(None) when the subscribe carried no
    /// pool_fee, None before the subscribe answered.
    pool_fee: Option<Option<crate::devfee::PoolFeeAnswer>>,
    /// The pool's share step from the last notify (WorkSource::share_step); 0 for the count form.
    share_step: u8,
    /// The receipts (16.3): counted, the v1-verified and the failed, and the last receipt's two fee fields.
    receipts: crate::state::ReceiptCounts,
    /// The receipts file (the settlement team's form): the directory and the host when set, the session's start
    /// (unix seconds at the pool's Connected) naming the file, the file once opened.
    receipts_dir: Option<std::path::PathBuf>,
    receipts_host: String,
    session_start: u64,
    receipts_file: Option<std::fs::File>,
}

impl Remote {
    /// Appends one receipt line as the pool sent it to `<dir>/<host>-<session start>.jsonl`, opening the file at
    /// the session's first receipt; a write that fails is said in the counts' last_failure and never stops mining.
    fn record_receipt(&mut self, raw: &str) {
        let Some(dir) = self.receipts_dir.clone() else { return };
        if self.receipts_file.is_none() {
            let _ = std::fs::create_dir_all(&dir);
            let path = dir.join(format!("{}-{}.jsonl", self.receipts_host, self.session_start));
            match std::fs::OpenOptions::new().create(true).append(true).open(&path) {
                Ok(f) => {
                    self.receipts_file = Some(f);
                    self.receipts.file = path.to_string_lossy().into_owned();
                }
                Err(e) => {
                    self.receipts.last_failure = format!("receipts file: {e}");
                    return;
                }
            }
        }
        if let Some(f) = self.receipts_file.as_mut() {
            if writeln!(f, "{raw}").is_err() {
                self.receipts.last_failure = "receipts file: a write failed".into();
            }
        }
    }

    /// Whether the thread has ended on its own (not told to stop): a session with no thread is no session, and a
    /// Remote that kept `connected` after its thread's end read as a live source with no job for ever (the mini's
    /// 0.2.1 install, 12 October 2026 05:09 UK: no socket to the pool, the card "waiting", no words for half an hour).
    fn ended(&self) -> bool {
        !self.stopping.load(Ordering::SeqCst) && self.handle.as_ref().map_or(false, |h| h.is_finished())
    }

    fn drain(&mut self) {
        while let Ok(ev) = self.rx.try_recv() {
            match ev {
                Ev::Connected => {
                    self.connected = true;
                    // a fresh session: a fresh receipts file (each session's receipt chain starts at seq 1)
                    self.session_start = SystemTime::now().duration_since(UNIX_EPOCH).map(|d| d.as_secs()).unwrap_or(0);
                    self.receipts_file = None;
                    self.problem = None;
                }
                Ev::Job(j) => self.pending_jobs.push(j),
                Ev::Outcome(id, n, o) => self.outcomes.push((id, n, o)),
                Ev::DevFeeJob(id) => {
                    self.dev_fee_templates += 1;
                    self.dev_fee_jobs.push(id);
                    while self.dev_fee_jobs.len() > 64 {
                        self.dev_fee_jobs.remove(0);
                    }
                }
                Ev::PoolFee(a) => self.pool_fee = Some(a),
                Ev::ShareStep(s) => self.share_step = s,
                Ev::Receipt(r, raw) => {
                    self.receipts.total += 1;
                    self.record_receipt(&raw);
                    // the pool's weight on this receipt (the scene's share weight; a number or a numeric string)
                    if let Ok(v) = serde_json::from_str::<serde_json::Value>(&raw) {
                        if let Some(w) = v.get("weight").and_then(|w| w.as_u64().or_else(|| w.as_str().and_then(|s| s.parse().ok()))) {
                            self.receipts.last_weight = w;
                        }
                    }
                    match r {
                        Ok((true, dev, bps)) => {
                            self.receipts.v1_verified += 1;
                            self.receipts.last_dev_fee_percent = dev;
                            self.receipts.last_pool_fee_bps = bps;
                        }
                        Ok((false, dev, bps)) => {
                            self.receipts.last_dev_fee_percent = dev;
                            self.receipts.last_pool_fee_bps = bps;
                        }
                        Err(why) => {
                            self.receipts.v1_failed += 1;
                            self.receipts.last_failure = why;
                        }
                    }
                }
                Ev::NodeAnswer(node, o) => {
                    let i = match self.node_answers.iter().position(|a| a.node == node) {
                        Some(i) => i,
                        None => {
                            self.node_answers.push(crate::state::NodeAnswers { node, ..Default::default() });
                            self.node_answers.len() - 1
                        }
                    };
                    let a = &mut self.node_answers[i];
                    match o {
                        SubmitOutcome::Accepted => a.accepted += 1,
                        SubmitOutcome::Superseded | SubmitOutcome::Held(_) => {}
                        SubmitOutcome::Stale => {
                            a.refused += 1;
                            *a.reasons.entry("stale".into()).or_insert(0) += 1;
                        }
                        SubmitOutcome::Invalid(w) | SubmitOutcome::Rejected(w) => {
                            a.refused += 1;
                            *a.reasons.entry(w.chars().take(64).collect()).or_insert(0) += 1;
                        }
                    }
                }
                Ev::Problem(p) => {
                    self.connected = false;
                    self.problem = Some(p);
                }
                Ev::Economics(e) => self.economics = e,
                Ev::Params(doc, digest) => {
                    self.params = doc;
                    self.params_digest = digest;
                }
            }
        }
    }
    fn stop(&mut self) {
        // the thread is told to stop and left to end on its own: never joined (the 5090 pod, 11 October: a join
        // behind 362 queued submissions held the supervisor's tick for 209 s; the thread also reads the stop
        // flag between requests, so a Stop behind a queue is not waited for)
        self.stopping.store(true, Ordering::SeqCst);
        let _ = self.tx.send(Cmd::Stop);
        self.handle.take();
        self.connected = false;
    }
}

impl Drop for Remote {
    fn drop(&mut self) {
        let _ = self.tx.send(Cmd::Stop);
    }
}

// ---------------------------------------------------------------------------------------------------------------
// the pool: IS/1

pub struct PoolSource {
    host: String,
    port: u16,
    tls: bool,
    label: String,
    algo: String,
    remote: Option<Remote>,
    signer: Option<std::sync::Arc<crate::minerkey::MinerKey>>,
    /// The dev fee in force (devfee.rs), declared to the pool in the subscribe (the fee-waiver handshake, 16.3);
    /// None: declared off.
    dev_fee: Option<crate::devfee::DevFee>,
    /// The receipts file's directory (WorkSource::set_receipts_dir); None keeps no file.
    receipts_dir: Option<std::path::PathBuf>,
    /// The fee session (16.3, the project's ruling of 12 October 2026): Ember's own second session on the same pool under
    /// the dev payout address, declaring no fee; one member template in a hundred is answered with its job instead
    /// (devfee::is_fee_template over the member's templates), so the fee is the miner's own act on the chain's
    /// by-address count, the member's declaring handshake unchanged and the pool holding nothing.
    fee_remote: Option<Remote>,
    /// The fee session's latest job, offered in place of the member's when the count falls due.
    fee_latest: Option<JobTemplate>,
    /// The member's templates polled this connection (the fee counter), and whether a fee template fell due while
    /// the fee session held no fresh job (it is taken at the next member template with one, so the count holds).
    templates: u64,
    fee_due: bool,
    /// The address and worker name of the last `connect`, for the redial when a session's thread ends on its own.
    last_address: String,
    last_worker: String,
    /// Redials after a thread's end this source (Totals reads it through `problem` words; the log names each).
    redials: u64,
    /// 0.2.11: the fee session's accepted shares this settle window, as Ember counts them (declared in the "for" block).
    fee_window_shares: u64,
}

/// F-E30: the session's end words on the pool's client.reconnect notice; the supervisor re-dials at once on them.
pub const RECONNECT_WORDS: &str = "the pool asked a reconnect to the same endpoint (a roll); redialling at once";

/// The prefix on a fee session's job id as the supervisor sees it (the two sessions' ids may collide): shares on
/// such a job route to the fee session, which strips it; its outcomes carry it back.
pub const FEE_JOB_PREFIX: &str = "fee:";

/// The fee session's job in the supervisor's form: the id prefixed, the rest as the pool sent it.
/// F-496 and F-543: the notify's `lane_worth` (a number, or "" for unknown) and `lane_workers` (an integer) on a coordinate
/// job: (worth, worth unknown, workers). A worth that is not a finite non-negative number reads as none; a count that is
/// not an integer reads as none.
pub fn lane_fields(p: &serde_json::Value) -> (Option<f64>, bool, Option<u64>) {
    let worth_raw = p.get("lane_worth");
    let unknown = worth_raw.and_then(|v| v.as_str()).map_or(false, |s| s.trim().is_empty());
    let worth = worth_raw.and_then(|v| v.as_f64().or_else(|| v.as_str().and_then(|s| s.trim().parse::<f64>().ok()))).filter(|x| x.is_finite() && *x >= 0.0);
    let workers = p.get("lane_workers").and_then(|v| v.as_u64().or_else(|| v.as_str().and_then(|s| s.trim().parse::<u64>().ok())));
    (worth, unknown, workers)
}

pub fn fee_job_for(job: &JobTemplate) -> JobTemplate {
    let mut j = job.clone();
    j.job_id = format!("{FEE_JOB_PREFIX}{}", job.job_id);
    j.template = j.job_id.clone();
    j
}

/// Whether a fee session's job stands in for the member's: at the member's height or past it, and issued within
/// the last FEE_JOB_FRESH (a job older than that is the pool's stale by the time a share returns).
pub const FEE_JOB_FRESH: Duration = Duration::from_secs(45);
pub fn fee_job_stands_in(fee: &JobTemplate, member_height: u64) -> bool {
    fee.height >= member_height && fee.issued.elapsed() < FEE_JOB_FRESH
}

/// The member templates numbered `polled` (0 for the first) that take the fee session's job: the template that
/// devfee::is_fee_template names, or any later one while a due fee template waited for a fresh fee job.
pub fn fee_template_due(polled: u64, percent: u8, was_due: bool) -> bool {
    was_due || crate::devfee::is_fee_template(polled, percent)
}

/// The pick for the member template numbered `polled`: (the fee job is taken, the due carried onward). A due
/// template with no fresh fee job carries; one with a fresh fee job takes it and clears the carry.
pub fn fee_pick(polled: u64, percent: u8, was_due: bool, fee_fresh: bool) -> (bool, bool) {
    let due = fee_template_due(polled, percent, was_due);
    if due && fee_fresh { (true, false) } else { (false, due) }
}

impl PoolSource {
    /// The dev fee this source declares to the pool (devfee.rs, DESIGN.md 16.3). Set before `connect`.
    pub fn with_dev_fee(mut self, fee: Option<crate::devfee::DevFee>) -> Self {
        self.dev_fee = fee;
        self
    }
}

impl PoolSource {
    pub fn new(host: &str, port: u16, tls: bool, label: &str, algo: &str) -> Self {
        PoolSource { host: host.into(), port, tls, label: label.into(), algo: algo.into(), remote: None, signer: None, dev_fee: None, receipts_dir: None, fee_remote: None, fee_latest: None, templates: 0, fee_due: false, last_address: String::new(), last_worker: String::new(), redials: 0, fee_window_shares: 0 }
    }

    /// A pool thread under a panic guard: a panic in the session's thread is reported as the source's problem
    /// (its words) instead of ending the thread in silence; the thread's end is then read by `Remote::ended` and
    /// the source redials.
    #[allow(clippy::too_many_arguments)]
    fn spawn_pool_thread(name: &str, host: String, port: u16, tls: bool, algo: String, address: String, worker: String, signer: Option<Arc<crate::minerkey::MinerKey>>, dev_fee: Option<crate::devfee::DevFee>, fee_for: Option<serde_json::Value>, cmds: Receiver<Cmd>, ev: Sender<Ev>, stopping: Arc<AtomicBool>) -> Result<thread::JoinHandle<()>, String> {
        let report = ev.clone();
        thread::Builder::new()
            .name(name.into())
            .spawn(move || {
                let r = std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| pool_thread(host, port, tls, algo, address, worker, signer, dev_fee, fee_for, cmds, ev, stopping)));
                if let Err(p) = r {
                    let words = p.downcast_ref::<String>().cloned().or_else(|| p.downcast_ref::<&str>().map(|s| s.to_string())).unwrap_or_else(|| "no words".into());
                    let _ = report.send(Ev::Problem(format!("the pool session's thread panicked ({words}); redialling")));
                }
            })
            .map_err(|e| e.to_string())
    }

    /// A session's thread ended on its own (`Remote::ended`): the words logged, both sessions ended and the
    /// source dialled again with the last address and worker. Err when the redial itself fails (the problem
    /// stays for the supervisor's notice; the next poll tries again).
    fn redial_if_ended(&mut self) -> Result<(), String> {
        let member_ended = self.remote.as_ref().map_or(false, |r| r.ended());
        let fee_ended = self.fee_remote.as_ref().map_or(false, |f| f.ended());
        if !member_ended && !fee_ended {
            return Ok(());
        }
        let words = self.remote.as_mut().and_then(|r| {
            r.drain();
            r.problem.clone()
        });
        self.redials += 1;
        let which = if member_ended { "the pool session's thread" } else { "the fee session's thread" };
        log::warn!("{which} ended on its own ({}); redial {} of this source", words.as_deref().unwrap_or("no words"), self.redials);
        let (a, w) = (self.last_address.clone(), self.last_worker.clone());
        self.disconnect();
        self.connect(&a, &w)
    }

    /// A pool thread's Remote before its first event (the member's and the fee's alike).
    fn remote_for(&self, ctx: Sender<Cmd>, erx: Receiver<Ev>, handle: thread::JoinHandle<()>, stopping: Arc<AtomicBool>, receipts_dir: Option<std::path::PathBuf>) -> Remote {
        Remote { params: serde_json::Value::Null, params_digest: None, tx: ctx, rx: erx, handle: Some(handle), stopping, connected: false, problem: Some("connecting".into()), economics: SourceEconomics { difficulty: 0.0, block_interval_s: 15.0, subsidy_ign: None, fees_ign: 0.0, pool_fee: None, solo: false, fund_ign: 0.0 }, pending_jobs: Vec::new(), outcomes: Vec::new(), node_answers: Vec::new(), dev_fee_templates: 0, dev_fee_jobs: Vec::new(), pool_fee: None, share_step: 0, receipts: crate::state::ReceiptCounts::default(), receipts_dir, receipts_host: self.host.clone(), session_start: 0, receipts_file: None }
    }

    /// The fee session's events folded in: its latest job kept, its outcomes carried to the member's list with
    /// the prefix on, its connection problems noted once in the log.
    fn drain_fee(&mut self) {
        let Some(f) = self.fee_remote.as_mut() else { return };
        f.drain();
        if let Some(j) = f.pending_jobs.pop() {
            self.fee_latest = Some(j);
        }
        f.pending_jobs.clear();
        let carried: Vec<(String, u64, SubmitOutcome)> = std::mem::take(&mut f.outcomes).into_iter().map(|(id, n, o)| (format!("{FEE_JOB_PREFIX}{id}"), n, o)).collect();
        // 0.2.11: the window's fee count, declared at the next fee subscribe
        let accepted_now = carried.iter().filter(|(_, _, o)| matches!(o, SubmitOutcome::Accepted)).count() as u64;
        self.fee_window_shares += accepted_now;
        if let Some(r) = self.remote.as_mut() {
            r.outcomes.extend(carried);
        }
    }
}

/// The receipts file's name for a session: `<host>-<session start unix>.jsonl` (the settlement team's form).
pub fn receipts_file_name(host: &str, session_start: u64) -> String {
    format!("{host}-{session_start}.jsonl")
}

/// Invalid refusals running before a pool session reloads its document (12 October 2026: the pool moved from
/// devnet8 to devnet9 under live sessions and every share went "invalid" against the old document).
pub const INVALID_RUN_RELOAD: u32 = 3;

/// A connected pool session that sees no job for this long ends itself and redials (the fleet session on the
/// public pool, 12 October 2026: alive and jobless from height 360 for an hour).
pub const JOBLESS_REDIAL_AFTER: Duration = Duration::from_secs(90);
/// F-E16 (the two-card Windows host, 23:09 to 23:28 UK, 13 October 2026: the engine counted 585 pool accepts while the pool received no frame
/// from the session and sent no receipt): on a pool that sends receipts, accepts answered with no receipt following
/// for this long end the session, which redials.
pub const RECEIPTLESS_REDIAL_AFTER: Duration = Duration::from_secs(60);
pub const RECEIPTLESS_REDIAL_MIN_ACCEPTS: u32 = 5;

/// The words for a mining.notify whose job fields Ember cannot read: the fields missing or malformed, by name.
pub fn unreadable_notify_words(p: &serde_json::Value) -> String {
    let mut bad = Vec::new();
    for k in ["header", "boundary", "share_target"] {
        match p.get(k).and_then(|v| v.as_str()) {
            None => bad.push(format!("{k} missing")),
            Some(h) if proto::hex_to_32(h).is_none() => bad.push(format!("{k} not 64 hex ({} chars)", h.len())),
            _ => {}
        }
    }
    format!("the pool sent a job this build cannot read ({}); reloading the session", bad.join(", "))
}

/// Whether a pool session reloads (ends itself so the reconnect's subscribe re-reads the document and the chain's
/// parameters before any further submit): a job's stated digest differing from the session's with no document
/// attached, or INVALID_RUN_RELOAD invalid refusals running. The words are the session's log line.
pub fn pool_reload_reason(session_digest: Option<&str>, job_digest: Option<&str>, job_has_doc: bool, chain_id: u64, invalid_run: u32) -> Option<String> {
    let chain = if chain_id == 0 { "the chain".to_string() } else { format!("chain {chain_id}") };
    if let (Some(s), Some(j)) = (session_digest, job_digest) {
        if s != j && !job_has_doc {
            return Some(format!("the pool's chain changed: {chain}, document {j}; reloading the document and the chain's parameters"));
        }
    }
    if invalid_run >= INVALID_RUN_RELOAD {
        return Some(format!("the pool refused {invalid_run} shares running as invalid ({chain}, document {}); reloading the document and the chain's parameters", session_digest.unwrap_or("unstated")));
    }
    None
}

fn reason_outcome(reason: &str) -> SubmitOutcome {
    // the pool's enumerated reasons (docs/ecosystem/DESIGN.md 2.5): stale is the template moving on; invalid and
    // low_difficulty are the pool's own verification refusing the proof; the rest are refusals for other causes
    match reason {
        "stale" => SubmitOutcome::Stale,
        "invalid" | "low_difficulty" => SubmitOutcome::Invalid(reason.to_string()),
        // F-E66: the pool's own submit bucket (50 a second, a burst of 50; pool 0.1.47 counts the dropped submit as the
        // session's rate and retargets it off the bucket): paced by the pool, never a rejection
        r if r.starts_with("admission: rate") => SubmitOutcome::Held("rate".to_string()),
        other => match held_case(other) {
            Some(case) => SubmitOutcome::Held(case),
            None => SubmitOutcome::Rejected(other.to_string()),
        },
    }
}

/// The pool thread: connect, subscribe, authorise, then read notifications and answer submissions; on any
/// error, report it and reconnect after a pause. Runs until `Stop`.
fn pool_thread(host: String, port: u16, tls: bool, algo_wanted: String, address: String, worker: String, signer: Option<std::sync::Arc<crate::minerkey::MinerKey>>, dev_fee: Option<crate::devfee::DevFee>, fee_for: Option<serde_json::Value>, cmds: Receiver<Cmd>, ev: Sender<Ev>, stopping: Arc<AtomicBool>) {
    let mut backoff = Duration::from_secs(2);
    let mut read_disclosure = false;
    let mut fee: Option<f64> = None;
    // the TLS choice (the E1 operability study, 12 October 2026: a default Linux install spoke TLS to the pool's plain
    // port and looped on "unexpected end of file" until the user found no_tls): the pool's own well-known document
    // names tls for the port dialled and it wins over the setting; failing that, a TLS failure of the plain-port
    // class turns the next dial plain, said once
    let mut tls = tls;
    let mut tls_from_pool = false;
    loop {
        if stopping.load(Ordering::SeqCst) {
            return;
        }
        // commands that arrived while disconnected: shares are stale by now
        while let Ok(c) = cmds.try_recv() {
            match c {
                Cmd::Stop => return,
                Cmd::Submit(s) => {
                    let _ = ev.send(Ev::Outcome(s.job_id, s.nonce, SubmitOutcome::Stale));
                }
            }
        }
        if !read_disclosure {
            read_disclosure = true;
            let url = Url { tls: true, host: host.clone(), port: 443, path: "/.well-known/igneum-pool.json".into() };
            if let Ok((200, body)) = net::http("GET", &url, None, &[], Duration::from_secs(10)) {
                if let Ok(v) = serde_json::from_slice::<serde_json::Value>(&body) {
                    if let Some(ep) = v.get("endpoints").and_then(|e| e.as_array()).and_then(|eps| eps.iter().find(|e| e.get("port").and_then(|p| p.as_u64()) == Some(port as u64) && e.get("host").and_then(|h| h.as_str()).map_or(true, |h| h.eq_ignore_ascii_case(&host)))) {
                        if let Some(t) = ep.get("tls").and_then(|t| t.as_bool()) {
                            if t != tls {
                                let _ = ev.send(Ev::Problem(format!("the pool's own document says its port {port} speaks {}; dialling it that way", if t { "TLS" } else { "plain TCP" })));
                            }
                            tls = t;
                            tls_from_pool = true;
                        }
                    }
                    fee = v.get("fee_bps").and_then(|x| x.as_f64()).map(|b| b / 10_000.0);
                    let _ = ev.send(Ev::Economics(SourceEconomics { difficulty: 0.0, block_interval_s: 15.0, subsidy_ign: None, fees_ign: 0.0, pool_fee: fee, solo: false, fund_ign: 0.0 }));
                }
            }
        }
        match pool_session(&host, port, tls, &algo_wanted, &address, &worker, fee, signer.as_ref(), dev_fee.as_ref(), fee_for.as_ref(), &cmds, &ev) {
            Ok(()) => return,
            Err(e) => {
                let plain_port = tls && !tls_from_pool && (e.contains("unexpected end of file") || e.contains("handshake") || e.contains("record overflow") || e.contains("wrong version"));
                if plain_port {
                    tls = false;
                    let _ = ev.send(Ev::Problem(format!("{e}; the port answers no TLS, dialling it as plain TCP")));
                } else {
                    let _ = ev.send(Ev::Problem(e));
                }
                let deadline = Instant::now() + backoff;
                while Instant::now() < deadline {
                    if stopping.load(Ordering::SeqCst) {
                        return;
                    }
                    match cmds.recv_timeout(Duration::from_millis(200)) {
                        Ok(Cmd::Stop) => return,
                        Ok(Cmd::Submit(s)) => {
                            let _ = ev.send(Ev::Outcome(s.job_id, s.nonce, SubmitOutcome::Stale));
                        }
                        Err(_) => {}
                    }
                }
                backoff = (backoff * 2).min(Duration::from_secs(60));
            }
        }
    }
}

/// One session; `Ok(())` only on `Stop`.
#[allow(clippy::too_many_arguments)]
/// A pool's digest field (`params_digest` or `paramsDigest`), lowercase hex without 0x, when well formed.
fn pool_digest(v: &serde_json::Value) -> Option<String> {
    v.get("params_digest").or_else(|| v.get("paramsDigest")).and_then(|d| d.as_str()).map(|d| d.trim_start_matches("0x").to_lowercase()).filter(|d| d.len() == 64 && d.bytes().all(|b| b.is_ascii_hexdigit()))
}

/// The engine's last measured total rate in hashes a second, declared at the pool subscribe (0 = unknown, not declared).
pub static LAST_RATE_HPS: std::sync::atomic::AtomicU64 = std::sync::atomic::AtomicU64::new(0);

fn pool_session(host: &str, port: u16, tls: bool, algo_wanted: &str, address: &str, worker: &str, fee: Option<f64>, signer: Option<&std::sync::Arc<crate::minerkey::MinerKey>>, dev_fee: Option<&crate::devfee::DevFee>, fee_for: Option<&serde_json::Value>, cmds: &Receiver<Cmd>, ev: &Sender<Ev>) -> Result<(), String> {
    let mut stream = net::connect(host, port, tls, Some(Duration::from_millis(250)))?;
    let mut id = 0u64;
    let mut send = |stream: &mut net::Stream, method: &str, params: serde_json::Value| -> Result<u64, String> {
        id += 1;
        let line = serde_json::json!({"id": id, "method": method, "params": params}).to_string() + "\n";
        if line.len() > net::MAX_FRAME_BYTES {
            return Err("frame over the bound".into());
        }
        stream.write_all(line.as_bytes()).and_then(|_| stream.flush()).map_err(|e| format!("send: {e}"))?;
        Ok(id)
    };
    // the TLS stream owns both directions; reading happens on this thread with a short timeout so commands are
    // served between frames (the plain stream could be split, the TLS one cannot without a lock)
    // the dev-fee declaration (DESIGN.md 16.3, the fee-waiver handshake): the state (on with the rate and the
    // address from the signed manifest, or off) signed by the session key over devfee::declaration_bytes, the
    // worker name inside it (IS/1's subscribe carries no worker, and the pool verifies at subscribe to answer
    // pool_fee in the same result); a miner without a key declares unsigned and the pool's answer says so
    let declared = {
        let (percent, address_hex, manifest) = match dev_fee {
            Some(f) => (f.percent, f.address_hex.clone(), f.manifest_sha256.clone()),
            None => (0u8, String::new(), String::new()),
        };
        let mut d = serde_json::json!({"percent": percent, "address": address_hex, "manifest_sha256": manifest, "worker": worker});
        if let Some(k) = signer {
            d["signature"] = serde_json::Value::String(k.sign_dev_fee(percent, &address_hex, &manifest, worker));
            d["session_key"] = serde_json::Value::String(k.public_hex());
        }
        d
    };
    // the pool's next cut (the pool team, 17:17 UK, 14 October 2026): a subscribe declaring the session's rate (hashes a
    // second) starts the share target at the step that rate chooses instead of step 0, so a redial or a second start
    // holds back nothing but the cap's remainder; the engine's last measured rate, unknown (0) on the first start
    let rate_hps = LAST_RATE_HPS.load(std::sync::atomic::Ordering::Relaxed);
    let mut sub = serde_json::json!({"dev_fee": declared});
    if rate_hps > 0 { sub["hashrate"] = serde_json::Value::from(rate_hps); }
    if let Some(f) = fee_for { sub["for"] = f.clone(); }
    let sub_id = send(&mut stream, "mining.subscribe", serde_json::json!([net::CLIENT_ID, sub]))?;
    let mut lines = net::Lines::new(ReadHalf(&mut stream));
    let mut nonce_start = 0u64;
    let mut nonce_count = u64::MAX;
    let mut session = String::new();
    // the non-custodial form: on when the pool's subscribe result says signed_shares and the miner has a key
    let mut signed = false;
    let mut pool_id = [0u8; 32];
    let mut headers: std::collections::HashMap<String, ([u8; 32], u64)> = std::collections::HashMap::new();
    let mut header_order: std::collections::VecDeque<String> = std::collections::VecDeque::new();
    let mut pending: std::collections::HashMap<u64, (String, u64)> = std::collections::HashMap::new();
    let mut authorised = false;
    let mut auth_id = 0u64;
    let mut last_frame = Instant::now();
    // the last job the pool sent: a connected session without one for JOBLESS_REDIAL_AFTER ends itself and redials
    let mut last_job = Instant::now();
    // F-E16: the pool's receipts beside its accepts; a receipt-sending pool answering accepts with no receipt is a dead session
    let mut receipts_seen = false;
    let mut last_receipt = Instant::now();
    let mut accepts_since_receipt: u32 = 0;
    // the session's document digest (the subscribe result's) and the invalid refusals running: a chain switch
    // under the session ends it so the reconnect reloads the document (pool_reload_reason)
    let mut session_digest: Option<String> = None;
    let mut invalid_run = 0u32;
    let (mut submit_tokens, mut submit_at) = (SUBMIT_BURST, Instant::now());
    loop {
        // commands
        while let Ok(c) = cmds.try_recv() {
            match c {
                Cmd::Stop => return Ok(()),
                Cmd::Submit(s) => {
                    if !authorised {
                        let _ = ev.send(Ev::Outcome(s.job_id, s.nonce, SubmitOutcome::Rejected("unauthorised".into())));
                        continue;
                    }
                    // the pool's submit bucket: paced here, never a burst past it (submit_pace)
                    let (t, wait) = submit_pace(submit_tokens, submit_at.elapsed().as_secs_f64());
                    if wait > 0.0 {
                        std::thread::sleep(Duration::from_secs_f64(wait));
                    }
                    submit_tokens = t;
                    submit_at = Instant::now();
                    let mut params = serde_json::json!([s.job_id, s.nonce.to_string(), proto::hex_encode(&s.mix), proto::hex_encode(&s.hash)]);
                    // the fifth element: the member key's signature (the non-custodial form) and, under a keyed
                    // document, the bond's public key and the S shares beside it (the pool verifies the share's
                    // replay and credits the member key as today; the bond key signs nothing on the pool path)
                    let mut extra = serde_json::Map::new();
                    if signed {
                        if let (Some(k), Some((header, chain_id))) = (signer, headers.get(&s.job_id)) {
                            let sig = k.sign_share(*chain_id, &pool_id, header, s.nonce, &s.mix, &s.hash);
                            extra.insert("key".into(), serde_json::Value::String(k.public_hex()));
                            extra.insert("sig".into(), serde_json::Value::String(sig));
                        }
                    }
                    if let Some(kp) = s.keyed.as_ref() {
                        if let serde_json::Value::Object(o) = kp.to_json() {
                            extra.extend(o);
                        }
                    }
                    if !extra.is_empty() {
                        if let Some(arr) = params.as_array_mut() {
                            arr.push(serde_json::Value::Object(extra));
                        }
                    }
                    let rid = send(&mut *lines.inner_mut().0, "mining.submit", params)?;
                    pending.insert(rid, (s.job_id, s.nonce));
                }
            }
        }
        // frames
        match lines.next_line() {
            Ok(Some(line)) => {
                last_frame = Instant::now();
                let v: serde_json::Value = match serde_json::from_str(&line) {
                    Ok(v) => v,
                    Err(_) => continue,
                };
                if let Some(method) = v.get("method").and_then(|m| m.as_str()) {
                    match method {
                        "mining.notify" => {
                            let p = v.get("params").cloned().unwrap_or_default();
                            let g = |k: &str| p.get(k).and_then(|x| x.as_str()).unwrap_or("");
                            let algo = g("algorithm");
                            if !algo.is_empty() && algo != algo_wanted {
                                return Err(format!("the pool mines {algo}, this build carries {algo_wanted}"));
                            }
                            let (Some(header), Some(boundary), Some(share_target)) = (proto::hex_to_32(g("header")), proto::hex_to_32(g("boundary")), proto::hex_to_32(g("share_target"))) else {
                                // a notify Ember cannot read is said with the fields it misses and ends the session
                                // for a reload, never skipped without a word (the project's order of 12 October 2026 on the
                                // fleet session's silence from height 360)
                                return Err(unreadable_notify_words(&p));
                            };
                            last_job = Instant::now();
                            let height = p.get("height").and_then(|h| h.as_u64().or_else(|| h.as_str().and_then(|s| s.parse().ok()))).unwrap_or(0);
                            let doc = p.get("params").filter(|d| d.is_object()).cloned().unwrap_or(serde_json::Value::Null);
                            let digest = pool_digest(&p);
                            let chain_id = p.get("chain_id").and_then(|c| c.as_u64().or_else(|| c.as_str().and_then(|x| x.parse().ok()))).or_else(|| doc.get("chain_id").and_then(|c| c.as_u64())).unwrap_or(0);
                            // F-E10 (13 October 2026): a job is a coordinate's only in the VectorWork form of record (coord with
                            // coord_name on the notify, main 82df5cec); a one-form pool relaying a node's template that carries a
                            // bare coord (the public pool on the old public test network, chain 7320) is one stream, and a share must name its newest job
                            let is_coord_job = p.get("coord").is_some() && p.get("coord_name").and_then(|n| n.as_str()).map_or(false, |n| !n.trim().is_empty());
                            if !is_coord_job {
                                if let Some(why) = pool_reload_reason(session_digest.as_deref(), digest.as_deref(), !doc.is_null(), chain_id, 0) {
                                    return Err(why);
                                }
                            }
                            // VectorWork (main 82df5cec, 13 October 2026): a notify with `coord` is one coordinate's
                            // template with its own document; the session's one document stands for the one-form job
                            // and the coordinate's rides the template (the supervisor keeps one context per coordinate)
                            let coord = if is_coord_job { p.get("coord").and_then(|c| c.as_u64().or_else(|| c.as_str().and_then(|x| x.parse().ok()))).filter(|c| *c < proto::MAX_COORDINATES as u64).map(|c| c as u8) } else { None };  // F-E54 (15 October 2026): every coordinate the protocol allows; "five" (F-E9) dropped coordinates 5 and up as plain jobs, so 0.2.11 to 0.2.13 never saw the texture lane
                            let coord_name = p.get("coord_name").and_then(|n| n.as_str()).map(|n| n.trim().to_string()).filter(|n| !n.is_empty() && n.len() <= 32);
                            let num = |k: &str| p.get(k).and_then(|v| v.as_f64().or_else(|| v.as_str().and_then(|x| x.parse::<f64>().ok())));
                            let coord_difficulty = if coord.is_some() { num("difficulty").filter(|d| *d > 0.0) } else { None };
                            let coord_last_height = if coord.is_some() { p.get("last_height").and_then(|v| v.as_u64().or_else(|| v.as_str().and_then(|x| x.parse().ok()))) } else { None };
                            let subsidy_ign = v.get("subsidy_wei").or_else(|| p.get("subsidy_wei")).and_then(|w| w.as_str().map(|x| x.to_string()).or_else(|| w.as_u64().map(|x| x.to_string()))).and_then(|w| w.parse::<f64>().ok()).map(|w| w / 1e18);
                            if !doc.is_null() && coord.is_none() {
                                let _ = ev.send(Ev::Params(doc.clone(), digest.clone()));
                                if digest.is_some() {
                                    session_digest = digest.clone();
                                }
                            }
                            // the pool's share step (ecosystem, 12 October 2026): the weight of a share on the chain,
                            // for the words; the job's share target is mined as given
                            let step = p.get("share_step").and_then(|v| v.as_u64().or_else(|| v.as_str().and_then(|s| s.parse().ok()))).unwrap_or(0).min(255) as u8;
                            let _ = ev.send(Ev::ShareStep(step));
                            if headers.insert(g("job_id").to_string(), (header, chain_id)).is_none() {
                                header_order.push_back(g("job_id").to_string());
                            }
                            while headers.len() > 64 {
                                match header_order.pop_front() {
                                    Some(k) => {
                                        headers.remove(&k);
                                    }
                                    None => break,
                                }
                            }
                            let job = JobTemplate { job_id: g("job_id").to_string(), height, header, boundary, share_target, nonce_start, nonce_count, issued: Instant::now(), template: g("job_id").to_string(), epoch: None, params: doc, params_digest: digest, next_params: None, keyed_rounds: 0, bond_key: None, share: None, coord: None, coord_difficulty: None, coord_last_height: None, paid_coords: None, lane_worth: None, lane_worth_unknown: false, lane_workers: None, subsidy_ign: None, program_anchor: None, anchor_height: None, chain_id: 0, coord_name: None };
                            let program_anchor = p.get("program_anchor").and_then(|a| a.as_str()).map(|a| a.trim_start_matches("0x").to_lowercase()).filter(|a| a.len() == 64 && a.bytes().all(|b| b.is_ascii_hexdigit()));
                            let anchor_height = p.get("anchor_height").and_then(|h| h.as_u64().or_else(|| h.as_str().and_then(|x| x.parse().ok())));
                            // 0.1.76's pay list (F-290): an array of coordinate indices; an empty or malformed list reads as none sent
                            let paid_coords = p.get("paid_coords").and_then(|a| a.as_array()).map(|a| a.iter().filter_map(|v| v.as_u64().or_else(|| v.as_str().and_then(|s| s.parse().ok()))).filter(|c| *c < proto::MAX_COORDINATES as u64).map(|c| c as u8).collect::<Vec<u8>>()).filter(|v| !v.is_empty());
                            // F-496 and F-543: the pool's lane worth and its worker count ride the coordinate job (0.1.92, 0.1.96);
                            // "" is the unknown worth, a number the worth; absent fields read as none
                            let (lane_worth, lane_worth_unknown, lane_workers) = if coord.is_some() { lane_fields(&p) } else { (None, false, None) };
                            let job = JobTemplate { coord, coord_name, coord_difficulty, coord_last_height, subsidy_ign, program_anchor, anchor_height, chain_id, paid_coords, lane_worth, lane_worth_unknown, lane_workers, ..job };
                            let _ = ev.send(Ev::Job(job));
                            let _ = ev.send(Ev::Economics(SourceEconomics { difficulty: difficulty_of(&boundary), block_interval_s: 15.0, subsidy_ign, fees_ign: 0.0, pool_fee: fee, solo: false, fund_ign: 0.0 }));
                        }
                        "mining.set_target" => {}
                        // F-E30 (the pool team's no-gap roll, 0.1.32): the old generation sends client.reconnect
                        // [host, port, wait] before it stops; the session ends at once with the words the supervisor
                        // re-dials on without a backoff, when the notice names this endpoint (or none); a notice naming
                        // another endpoint is logged and refused: no pool moves a miner by a frame
                        "client.reconnect" => {
                            let p = v.get("params").cloned().unwrap_or_default();
                            let nh = p.get(0).and_then(|x| x.as_str()).unwrap_or("").trim().to_string();
                            let np = p.get(1).and_then(|x| x.as_u64().or_else(|| x.as_str().and_then(|t| t.parse().ok()))).unwrap_or(0);
                            let same = (nh.is_empty() || nh.eq_ignore_ascii_case(&host)) && (np == 0 || np == port as u64);
                            if same {
                                return Err(RECONNECT_WORDS.to_string());
                            }
                            log::warn!("the pool asked a reconnect to {nh}:{np}, not this source's {host}:{port}; refused (no pool moves a miner by a frame); the session stays");
                        }
                        "mining.receipt" => {
                            // the pool's signed acknowledgement per share (16.3): the two fee fields inside, the v1
                            // digest checked when the receipt says version 1
                            let p = v.get("params").cloned().unwrap_or_default();
                            let p = if p.is_array() { p.get(0).cloned().unwrap_or_default() } else { p };
                            receipts_seen = true;
                            last_receipt = Instant::now();
                            accepts_since_receipt = 0;
                            let _ = ev.send(Ev::Receipt(crate::devfee::check_receipt(&p), p.to_string()));
                        }
                        _ => {}
                    }
                    continue;
                }
                let rid = v.get("id").and_then(|i| i.as_u64()).unwrap_or(0);
                let err = v.get("error").filter(|e| !e.is_null()).map(|e| e.get("reason").and_then(|r| r.as_str()).unwrap_or("invalid").to_string());
                if rid == sub_id {
                    let r = v.get("result").cloned().unwrap_or_default();
                    session = r.get("session_id").and_then(|s| s.as_str()).unwrap_or("").to_string();
                    if let Some(k) = r.get("keyed_to").and_then(|k| k.as_str()) { log::info!("pool: this fee session is keyed to the member {k}"); }
                    nonce_start = r.get("nonce_start").and_then(|s| s.as_str()).and_then(|s| s.parse().ok()).unwrap_or(0);
                    nonce_count = r.get("nonce_count").and_then(|s| s.as_str()).and_then(|s| s.parse().ok()).unwrap_or(u64::MAX);
                    if let Some(e) = err {
                        return Err(format!("subscribe refused: {e}"));
                    }
                    // a subscribe answered without a session id is redialled, never authorised: a signed
                    // authorize over an empty id is what the pool refuses as "unauthorised" (12 October 2026,
                    // 07:21:54Z on the mini: the reconnect under the pool's hang; ecosystem's reading of the
                    // refusal's two paths)
                    if signer.is_some() && session.is_empty() {
                        return Err("the pool's subscribe answered no session id; redialling".into());
                    }
                    // the pool's parameter document (the node's, verbatim) and its digest, when the pool serves them
                    if let Some(doc) = r.get("params").filter(|d| d.is_object()) {
                        let _ = ev.send(Ev::Params(doc.clone(), pool_digest(&r)));
                    }
                    session_digest = pool_digest(&r);
                    // the pool's answer to the dev-fee declaration (16.3): its fee for the session, waived or not
                    let _ = ev.send(Ev::PoolFee(crate::devfee::PoolFeeAnswer::from_result(&r)));
                    // the non-custodial form: the pool says signed_shares and names itself; the sign-in binds the
                    // miner's key to the payout address with a signature the pool checks. The key also rides the
                    // sign-in whenever the declaration was signed, so the pool can bind the two (16.3)
                    signed = r.get("signed_shares").and_then(|b| b.as_bool()).unwrap_or(false) && signer.is_some();
                    if signed {
                        let pid = r.get("pool_id").and_then(|x| x.as_str()).and_then(crate::minerkey::unhex).unwrap_or_default();
                        if pid.len() == 32 {
                            pool_id.copy_from_slice(&pid);
                        } else {
                            return Err("the pool advertises signed shares without a 32-byte pool_id".into());
                        }
                        let sid = crate::minerkey::unhex(&session).unwrap_or_else(|| session.as_bytes().to_vec());
                        let addr = crate::minerkey::unhex(&address.to_lowercase()).unwrap_or_default();
                        let Ok(addr20) = <[u8; 20]>::try_from(addr.as_slice()) else { return Err("the payout address is not 20 bytes".into()) };
                        let k = signer.expect("checked");
                        let sig = k.sign_authorize(&sid, &addr20);
                        auth_id = send(&mut *lines.inner_mut().0, "mining.authorize", serde_json::json!([address, worker, k.public_hex(), sig]))?;
                    } else if let Some(k) = signer {
                        // the declaration was signed: the sign-in carries the same key (and its binding) so the pool
                        // checks dev_fee.session_key against it; a pool without the handshake ignores the extra two
                        let sid = crate::minerkey::unhex(&session).unwrap_or_else(|| session.as_bytes().to_vec());
                        let addr = crate::minerkey::unhex(&address.to_lowercase()).unwrap_or_default();
                        match <[u8; 20]>::try_from(addr.as_slice()) {
                            Ok(addr20) => {
                                let sig = k.sign_authorize(&sid, &addr20);
                                auth_id = send(&mut *lines.inner_mut().0, "mining.authorize", serde_json::json!([address, worker, k.public_hex(), sig]))?;
                            }
                            Err(_) => {
                                auth_id = send(&mut *lines.inner_mut().0, "mining.authorize", serde_json::json!([address, worker]))?;
                            }
                        }
                    } else {
                        auth_id = send(&mut *lines.inner_mut().0, "mining.authorize", serde_json::json!([address, worker]))?;
                    }
                } else if rid == auth_id && auth_id != 0 {
                    if err.is_some() || v.get("result").and_then(|r| r.as_bool()) != Some(true) {
                        return Err(format!("the pool refused the address or worker name ({})", err.unwrap_or_default()));
                    }
                    authorised = true;
                    let _ = ev.send(Ev::Connected);
                    log::info!("pool session {session} authorised; nonce range {nonce_start} + {nonce_count}");
                } else if let Some((job_id, nonce)) = pending.remove(&rid) {
                    let o = match err {
                        Some(r) => reason_outcome(&r),
                        None => SubmitOutcome::Accepted,
                    };
                    if matches!(o, SubmitOutcome::Accepted) {
                        accepts_since_receipt += 1;
                        if receipts_seen && accepts_since_receipt >= RECEIPTLESS_REDIAL_MIN_ACCEPTS && last_receipt.elapsed() > RECEIPTLESS_REDIAL_AFTER {
                            return Err(format!("the pool answered {accepts_since_receipt} accepts with no receipt for {} s (F-E16); the session is ended and redialled", RECEIPTLESS_REDIAL_AFTER.as_secs()));
                        }
                    }
                    invalid_run = if matches!(o, SubmitOutcome::Invalid(_)) { invalid_run + 1 } else { 0 };
                    let _ = ev.send(Ev::Outcome(job_id, nonce, o));
                    if let Some(why) = pool_reload_reason(session_digest.as_deref(), None, false, 0, invalid_run) {
                        return Err(why);
                    }
                }
            }
            Ok(None) => return Err("the pool closed the connection".into()),
            Err(e) if e.kind() == std::io::ErrorKind::WouldBlock || e.kind() == std::io::ErrorKind::TimedOut => {
                if last_frame.elapsed() > Duration::from_secs(180) {
                    return Err("no frame from the pool for three minutes".into());
                }
                if authorised && last_job.elapsed() > JOBLESS_REDIAL_AFTER {
                    return Err(format!("no job from the pool for {} s while connected; redialling", JOBLESS_REDIAL_AFTER.as_secs()));
                }
            }
            Err(e) => return Err(format!("read: {e}")),
        }
    }
}

/// A read view over the stream for `Lines`, keeping the stream reachable for writes between frames.
struct ReadHalf<'a>(&'a mut net::Stream);
impl std::io::Read for ReadHalf<'_> {
    fn read(&mut self, buf: &mut [u8]) -> std::io::Result<usize> {
        self.0.read(buf)
    }
}

impl WorkSource for PoolSource {
    fn name(&self) -> String {
        format!("pool {} ({}:{}{})", self.label, self.host, self.port, if self.tls { ", TLS" } else { ", no TLS" })
    }
    fn set_receipts_dir(&mut self, dir: &std::path::Path) {
        self.receipts_dir = Some(dir.to_path_buf());
    }
    fn share_step(&mut self) -> u8 {
        match self.remote.as_mut() {
            Some(r) => {
                r.drain();
                r.share_step
            }
            None => 0,
        }
    }
    fn algo(&self) -> String {
        self.algo.clone()
    }
    fn connect(&mut self, payout_address: &str, worker_name: &str) -> Result<(), String> {
        let (ctx, crx) = mpsc::channel();
        let (etx, erx) = mpsc::channel();
        let (h, p, t, a, addr, w, k) = (self.host.clone(), self.port, self.tls, self.algo.clone(), payout_address.to_string(), worker_name.to_string(), self.signer.clone());
        let dev = self.dev_fee.clone();
        let stopping = Arc::new(AtomicBool::new(false));
        let flag = stopping.clone();
        self.last_address = payout_address.to_string();
        self.last_worker = worker_name.to_string();
        let handle = Self::spawn_pool_thread("work-pool", h, p, t, a, addr, w, k, dev, None, crx, etx, flag)?;
        self.remote = Some(self.remote_for(ctx, erx, handle, stopping, self.receipts_dir.clone()));
        self.fee_latest = None;
        self.templates = 0;
        self.fee_due = false;
        // wait for the session (subscribe and authorize, and the pool's document when it serves one) so the
        // caller sees the document at start; a pool that does not answer in time is a connect error
        let t0 = Instant::now();
        while t0.elapsed() < Duration::from_secs(15) {
            if let Some(r) = self.remote.as_mut() {
                r.drain();
                if r.connected {
                    break;
                }
                if let Some(p) = r.problem.as_deref() {
                    if p != "connecting" {
                        let p = p.to_string();
                        self.disconnect();
                        return Err(p);
                    }
                }
            }
            thread::sleep(Duration::from_millis(100));
        }
        // the fee session (16.3), dialled once the member session is up (pool 0.1.36 keys the signed "for" block
        // to the member session already authorised under that key, so the member goes first): the same pool, the dev payout address, the same worker name and session key, no
        // fee declared (the company's own session pays the pool's fee like any member); its receipts keep no file
        // (the member's file is the settlement form; the fee's shares are in the fee record by job id)
        if let Some(f) = self.dev_fee.as_ref().filter(|f| f.percent > 0 && !f.address_hex.is_empty()) {
            let (ctx, crx) = mpsc::channel();
            let (etx, erx) = mpsc::channel();
            // the fee session's own session key, derived from the member's (the pool keys its receipt chain and
            // running total per session key, ecosystem's F-34 line of 12 October 2026)
            let k = self.signer.as_ref().map(|k| Arc::new(k.derived(crate::minerkey::FEE_SESSION_TAG)));
            // 0.2.11 (the founder's "1 percent total" by verifiable accounting; pool 0.1.36): the fee session names and
            // SIGNS its member, so the pool keys the fee's shares to the member's session over the settle window; the
            // window's count so far travels with it (carried across a redial or a fee switch, never a free window)
            let fee_for = match (self.signer.as_ref(), k.as_ref()) {
                (Some(member), Some(fee_key)) => {
                    let (addr_m, fee_pub) = (payout_address.to_string(), fee_key.public_hex());
                    let n = self.fee_window_shares;
                    Some(serde_json::json!({"address": addr_m, "session_key": member.public_hex(), "fee_session_key": fee_pub, "percent": f.percent, "window_shares": n, "signature": member.sign_fee_for(&addr_m, &fee_pub, f.percent, n)}))
                }
                _ => None,
            };
            let (h, p, t, a, addr, w) = (self.host.clone(), self.port, self.tls, self.algo.clone(), f.address_hex.clone(), worker_name.to_string());
            let stopping = Arc::new(AtomicBool::new(false));
            let flag = stopping.clone();
            match Self::spawn_pool_thread("work-pool-fee", h, p, t, a, addr, w, k, None, fee_for, crx, etx, flag) {
                Ok(handle) => self.fee_remote = Some(self.remote_for(ctx, erx, handle, stopping, None)),
                Err(e) => log::warn!("the fee session could not start ({e}); no fee template this connection"),
            }
        }
        Ok(())
    }
    fn poll(&mut self) -> Result<Option<JobTemplate>, String> {
        self.redial_if_ended()?;
        self.drain_fee();
        let percent = self.dev_fee.as_ref().map_or(0, |f| f.percent);
        let r = self.remote.as_mut().ok_or("not connected")?;
        r.drain();
        // the newest job wins; older ones were superseded before the tick saw them. VectorWork (13 October 2026):
        // a coordinate's job supersedes only its own coordinate's earlier jobs; the newest job of every coordinate
        // is delivered, one per poll, oldest coordinate first, so no coordinate's template is dropped by another's
        let last = if r.pending_jobs.iter().any(|j| j.coord.is_some()) {
            let mut newest: std::collections::BTreeMap<Option<u8>, JobTemplate> = std::collections::BTreeMap::new();
            for j in r.pending_jobs.drain(..) {
                newest.insert(j.coord, j);
            }
            let first = newest.keys().next().cloned();
            let job = first.and_then(|k| newest.remove(&k));
            r.pending_jobs.extend(newest.into_values());
            job
        } else {
            let last = r.pending_jobs.pop();
            r.pending_jobs.clear();
            last
        };
        let Some(member) = last else { return Ok(None) };
        // the fee template (16.3): the member's template that falls due by the count takes the fee session's job
        // when that job is fresh at the member's height; otherwise the due carries to the next member template
        if self.fee_remote.is_some() {
            let fresh = self.fee_latest.as_ref().map_or(false, |f| fee_job_stands_in(f, member.height));
            let (take, due) = fee_pick(self.templates, percent, self.fee_due, fresh);
            self.fee_due = due;
            if take {
                let job = fee_job_for(self.fee_latest.as_ref().expect("fresh"));
                self.templates += 1;
                r.dev_fee_templates += 1;
                r.dev_fee_jobs.push(job.job_id.clone());
                while r.dev_fee_jobs.len() > 64 {
                    r.dev_fee_jobs.remove(0);
                }
                log::info!("fee template {} at height {} (one member template in {} to the dev payout address)", job.job_id, job.height, 100 / u64::from(percent.max(1)));
                return Ok(Some(job));
            }
        }
        self.templates += 1;
        Ok(Some(member))
    }
    fn submit(&mut self, share: &Share) -> Result<(), String> {
        if let Some(id) = share.job_id.strip_prefix(FEE_JOB_PREFIX) {
            let f = self.fee_remote.as_mut().ok_or("no fee session")?;
            let mut s = share.clone();
            s.job_id = id.to_string();
            return f.tx.send(Cmd::Submit(s)).map_err(|_| "the fee session's thread has gone".to_string());
        }
        let r = self.remote.as_mut().ok_or("not connected")?;
        r.tx.send(Cmd::Submit(share.clone())).map_err(|_| "the pool thread has gone".to_string())
    }
    fn outcomes(&mut self) -> Vec<(String, u64, SubmitOutcome)> {
        self.drain_fee();
        match self.remote.as_mut() {
            Some(r) => {
                r.drain();
                std::mem::take(&mut r.outcomes)
            }
            None => Vec::new(),
        }
    }
    fn dev_fee_jobs(&mut self) -> (u64, Vec<String>) {
        match self.remote.as_mut() {
            Some(r) => (r.dev_fee_templates, r.dev_fee_jobs.clone()),
            None => (0, Vec::new()),
        }
    }
    fn pool_fee(&mut self) -> Option<Option<crate::devfee::PoolFeeAnswer>> {
        match self.remote.as_mut() {
            Some(r) => {
                r.drain();
                r.pool_fee.clone()
            }
            None => None,
        }
    }
    fn receipts(&mut self) -> crate::state::ReceiptCounts {
        match self.remote.as_mut() {
            Some(r) => {
                r.drain();
                r.receipts.clone()
            }
            None => crate::state::ReceiptCounts::default(),
        }
    }
    fn economics(&self) -> SourceEconomics {
        self.remote.as_ref().map(|r| r.economics.clone()).unwrap_or(SourceEconomics { difficulty: 0.0, block_interval_s: 15.0, subsidy_ign: None, fees_ign: 0.0, pool_fee: None, solo: false, fund_ign: 0.0 })
    }
    fn disconnect(&mut self) {
        if let Some(mut r) = self.remote.take() {
            r.stop();
        }
        if let Some(mut f) = self.fee_remote.take() {
            f.stop();
        }
        self.fee_latest = None;
    }
    fn params(&self) -> serde_json::Value {
        self.remote.as_ref().map(|r| r.params.clone()).unwrap_or(serde_json::Value::Null)
    }
    fn params_digest(&self) -> Option<String> {
        self.remote.as_ref().and_then(|r| r.params_digest.clone())
    }
    fn signs(&self) -> bool {
        self.signer.is_some()
    }
    fn set_signer(&mut self, k: std::sync::Arc<crate::minerkey::MinerKey>) {
        self.signer = Some(k);
    }
    fn connected(&self) -> bool {
        self.remote.as_ref().map_or(false, |r| r.connected && !r.ended())
    }
    fn problem(&self) -> Option<String> {
        let r = self.remote.as_ref()?;
        if r.ended() {
            return Some(r.problem.clone().unwrap_or_else(|| "the pool session's thread ended on its own; redialling".into()));
        }
        r.problem.clone()
    }
}

// ---------------------------------------------------------------------------------------------------------------
// solo: the node's mining RPC

pub struct SoloSource {
    /// The nodes, in the setting's order (the project's ruling of 11 October 2026 after ecosystem's reconciliation of the
    /// 4070 row to the wei: every block that paid the card was sealed by the node it submitted to, so until share
    /// gossip lands a solo miner submits every share to every sealer it knows): the first that answers serves the
    /// work and takes a block; every one takes the shares.
    urls: Vec<Url>,
    /// The lab switch: the one node the template is read from (EMBER_TEMPLATE_NODE), the shares going to `urls`;
    /// None means the first of `urls` that answers serves the template.
    template: Option<Url>,
    /// The algorithm the installed workers carry (the hint); replaced by the node's own at connect.
    algo: String,
    /// The node's algorithm parameter document, when igneum_getWork carries `params`.
    params: serde_json::Value,
    /// The node's `paramsDigest` (the protocol team, 10 October 2026), when it carries one.
    params_digest: Option<String>,
    remote: Option<Remote>,
    /// The share lane's identity for this session (the payout address and the session's extra nonce), set at
    /// connect from the payout address; None when the address is not 20 bytes of hex, and then the block search
    /// stands whatever the node serves.
    lane: Option<ShareLane>,
    /// The dev fee in force (devfee.rs): one template in `100 / percent` is requested on the share lane with the
    /// dev payout address in place of the miner's. None: off.
    dev_fee: Option<crate::devfee::DevFee>,
}

impl SoloSource {
    /// The dev fee this source applies (devfee.rs); None turns it off. Set before `connect`.
    pub fn with_dev_fee(mut self, fee: Option<crate::devfee::DevFee>) -> Self {
        self.dev_fee = fee;
        self
    }

    pub fn new(rpc_url: &str, algo: &str) -> Result<Self, String> {
        let mut urls = Vec::new();
        for one in crate::settings::rpc_url_list(rpc_url) {
            urls.push(net::parse_url(&one)?);
        }
        if urls.is_empty() {
            return Err("no node address".into());
        }
        // the lab switch (the project's order of 16:5x UK by date, 11 October 2026, the founder's gossip row): the template
        // from one node, every share to the nodes named; EMBER_TEMPLATE_NODE names the template node (the CLI's
        // --template-node sets it); unset, the first node that answers serves the template as above
        let template = match std::env::var("EMBER_TEMPLATE_NODE") {
            Ok(t) if !t.trim().is_empty() => Some(net::parse_url(t.trim())?),
            _ => None,
        };
        Ok(SoloSource { urls, template, algo: algo.into(), params: serde_json::Value::Null, params_digest: None, remote: None, lane: None, dev_fee: None })
    }
}

/// `igneum_getWork` (the object) or `eth_getWork` (the four strings). A node mining another algorithm than the
/// workers carry returns `Err(Mismatch)` with the job, so the economics still read live while no work is taken.
pub enum GetWork {
    Job(JobTemplate),
    Mismatch { node_algo: String, job: JobTemplate },
}

/// The session's parameter document from a node's `igneum_getWork` answer: the block document (`params`,
/// `paramsDigest`), or on the share lane the share's (`shareParams`, `shareParamsDigest`: the unkeyed twin on a
/// keyed row). Devnet7's block document carries keyed_rounds 2 while its twin carries none: the session reads the
/// twin, so the start asks no bond secret, since the shares are unkeyed work (KEYED-FORM.md). A zero digest is the
/// node's absent hash and leaves the block's digest standing.
pub fn session_document(raw: &str, lane_on: bool) -> (serde_json::Value, Option<String>) {
    let digest_of = |k: &str| net::raw_field(raw, k).map(|d| d.trim_matches('"').trim_start_matches("0x").to_lowercase()).filter(|d| d.len() == 64 && d.bytes().all(|b| b.is_ascii_hexdigit()) && d.bytes().any(|b| b != b'0'));
    let block = net::raw_field(raw, "params").and_then(|p| serde_json::from_str::<serde_json::Value>(&p).ok()).unwrap_or(serde_json::Value::Null);
    let block_digest = digest_of("paramsDigest");
    let st = net::raw_field(raw, "shareTarget").unwrap_or_default();
    let lane = lane_on && (net::decimal_to_u256(&st).is_some() || net::hex0x_to_u256(&st).is_some());
    match net::raw_field(raw, "shareParams").and_then(|p| serde_json::from_str::<serde_json::Value>(&p).ok()).filter(|v| v.is_object() && lane) {
        Some(doc) => (doc, digest_of("shareParamsDigest").or(block_digest)),
        None => (block, block_digest),
    }
}

/// A quantity field as the node serves it: a hex quantity ("0x1a"), a decimal number or a quoted decimal.
fn quantity(s: &str) -> Option<u64> {
    let s = s.trim().trim_matches('"');
    if s.is_empty() {
        return None;
    }
    if let Some(x) = s.strip_prefix("0x") {
        u64::from_str_radix(x, 16).ok()
    } else {
        s.parse().ok()
    }
}

/// The node's work: the parsed form and, for `igneum_getWork`, the raw answer (so the dev fee's template can be
/// parsed again on the dev lane without a second request); None for the `eth_getWork` fallback.
fn get_work(url: &Url, id: u64, algo_wanted: &str, lane: Option<&ShareLane>) -> Result<(GetWork, Option<String>), String> {
    get_work_inner(url, id, algo_wanted, lane)
}

fn get_work_inner(url: &Url, id: u64, algo_wanted: &str, lane: Option<&ShareLane>) -> Result<(GetWork, Option<String>), String> {
    let t = Duration::from_secs(10);
    match net::json_rpc(url, id, "igneum_getWork", &serde_json::json!([]), t) {
        Ok(raw) => parse_igneum_get_work(&raw, algo_wanted, lane).map(|g| (g, Some(raw))),
        Err(e1) => {
            let raw = net::json_rpc(url, id, "eth_getWork", &serde_json::json!([]), t).map_err(|e2| format!("{e1}; {e2}"))?;
            let arr: Vec<String> = serde_json::from_str(&raw).map_err(|_| "eth_getWork: not four strings")?;
            if arr.len() < 3 {
                return Err("eth_getWork: not four strings".into());
            }
            let header = net::hex0x_to_u256(&arr[0]).ok_or("eth_getWork: header")?;
            let boundary = net::hex0x_to_u256(&arr[2]).ok_or("eth_getWork: target")?;
            let height = arr.get(3).and_then(|h| u64::from_str_radix(h.trim_start_matches("0x"), 16).ok()).unwrap_or(0);
            Ok((GetWork::Job(JobTemplate { job_id: format!("solo-{height}-{}", &proto::hex_encode(&header)[..8]), height, header, boundary, share_target: boundary, nonce_start: 0, nonce_count: u64::MAX, issued: Instant::now(), template: proto::hex_encode(&header), epoch: None, params: serde_json::Value::Null, params_digest: None, next_params: None, keyed_rounds: 0, bond_key: None, share: None, coord: None, coord_difficulty: None, coord_last_height: None, paid_coords: None, lane_worth: None, lane_worth_unknown: false, lane_workers: None, subsidy_ign: None, program_anchor: None, anchor_height: None, chain_id: 0, coord_name: None }), None))
        }
    }
}

/// The template from an `igneum_getWork` answer: the share search when the node serves a share target and this
/// session has a lane (the payout address), the block search otherwise; `Mismatch` when the node's algorithm is
/// not the workers'.
pub fn parse_igneum_get_work(raw: &str, algo_wanted: &str, lane: Option<&ShareLane>) -> Result<GetWork, String> {
            let g = |k: &str| net::raw_field(raw, k).unwrap_or_default();
            // the share lane: a node serving shareTarget (the share lane's node, mineshares.go) is mined on the
            // share header when this session has a payout address for it; the twin at shareHeight is the document
            // (shareParams, the unkeyed twin on a keyed row; the block's own document otherwise)
            if let Some(lane) = lane {
                let st = g("shareTarget");
                if let Some(share_target) = net::decimal_to_u256(&st).or_else(|| net::hex0x_to_u256(&st)) {
                    let algo = g("algorithm");
                    let algo = algo.trim_matches('"').to_string();
                    // a node that seals nothing (the gossip peer, the protocol team's launch form) serves the share fields
                    // with a zero headerHash and no seal work: the share height stands in for the height, and the
                    // boundary below falls back to the share target when no block target is served
                    let share_height = quantity(&g("shareHeight")).or_else(|| quantity(&g("height"))).ok_or("igneum_getWork: no shareHeight")?;
                    let height = quantity(&g("height")).unwrap_or(share_height);
                    let shares_per_block = quantity(&g("sharesPerBlock")).unwrap_or(0);
                    let parent = net::hex0x_to_u256(&g("shareParent")).ok_or("igneum_getWork: shareTarget without shareParent")?;
                    let digest_of = |k: &str| net::raw_field(&raw, k).map(|d| d.trim_matches('"').trim_start_matches("0x").to_lowercase()).filter(|d| d.len() == 64 && d.bytes().all(|b| b.is_ascii_hexdigit()) && d.bytes().any(|b| b != b'0'));
                    let params = net::raw_field(&raw, "shareParams").and_then(|p| serde_json::from_str::<serde_json::Value>(&p).ok()).filter(|v| v.is_object())
                        .or_else(|| net::raw_field(&raw, "params").and_then(|p| serde_json::from_str(&p).ok())).unwrap_or(serde_json::Value::Null);
                    let params_digest = digest_of("shareParamsDigest").or_else(|| digest_of("paramsDigest"));
                    let epoch = if share_height == height { quantity(&g("epoch")) } else { None };
                    let header = share_header(&parent, share_height, &lane.address, lane.extra_nonce);
                    // the boundary stays the block target: the economics read the chain's difficulty from it (a
                    // share pays pro rata, so the expectation per block is the block's); the share target is what
                    // the workers report against, and a result under the block target on this header is still a
                    // share (never a block)
                    let tgt = g("target");
                    let boundary = net::decimal_to_u256(&tgt).or_else(|| net::hex0x_to_u256(&tgt)).unwrap_or(share_target);
                    let job = JobTemplate { job_id: format!("share-{share_height}-{}", &proto::hex_encode(&header)[..8]), height: share_height, header, boundary, share_target, nonce_start: 0, nonce_count: u64::MAX, issued: Instant::now(), template: proto::hex_encode(&header), epoch, params, params_digest, next_params: None, keyed_rounds: 0, bond_key: None, share: Some(ShareJob { share_height, extra_nonce: lane.extra_nonce, shares_per_block, parent }), coord: None, coord_difficulty: None, coord_last_height: None, paid_coords: None, lane_worth: None, lane_worth_unknown: false, lane_workers: None, subsidy_ign: None, program_anchor: None, anchor_height: None, chain_id: 0, coord_name: None };
                    if !algo.is_empty() && algo != algo_wanted {
                        return Ok(GetWork::Mismatch { node_algo: algo, job });
                    }
                    return Ok(GetWork::Job(job));
                }
            }
            let algo = g("algorithm");
            let algo = algo.trim_matches('"').to_string();
            let header = net::hex0x_to_u256(&g("headerHash")).ok_or("igneum_getWork: no headerHash")?;
            let h = g("height");
            let h = h.trim_matches('"');
            let height: u64 = if let Some(x) = h.strip_prefix("0x") { u64::from_str_radix(x, 16).map_err(|_| "igneum_getWork: no height")? } else { h.parse().map_err(|_| "igneum_getWork: no height")? };
            let tgt = g("target");
            let boundary = net::decimal_to_u256(&tgt).or_else(|| net::hex0x_to_u256(&tgt)).ok_or("igneum_getWork: no target")?;
            let epoch_field = g("epoch");
            let epoch_field = epoch_field.trim_matches('"');
            let epoch = if epoch_field.is_empty() { None } else if let Some(x) = epoch_field.strip_prefix("0x") { u64::from_str_radix(x, 16).ok() } else { epoch_field.parse().ok() };
            let params = net::raw_field(&raw, "params").and_then(|p| serde_json::from_str(&p).ok()).unwrap_or(serde_json::Value::Null);
            let params_digest = net::raw_field(&raw, "paramsDigest").map(|d| d.trim_matches('"').trim_start_matches("0x").to_lowercase()).filter(|d| d.len() == 64 && d.bytes().all(|b| b.is_ascii_hexdigit()));
            let digest_of = |k: &str| net::raw_field(&raw, k).map(|d| d.trim_matches('"').trim_start_matches("0x").to_lowercase()).filter(|d| d.len() == 64 && d.bytes().all(|b| b.is_ascii_hexdigit()));
            let next_params = net::raw_field(&raw, "nextParams").and_then(|p| serde_json::from_str::<serde_json::Value>(&p).ok()).filter(|v| v.is_object()).and_then(|doc| {
                let h = net::raw_field(&raw, "nextParamsHeight").unwrap_or_default();
                let h = h.trim_matches('"');
                let height = if let Some(x) = h.strip_prefix("0x") { u64::from_str_radix(x, 16).ok() } else { h.parse().ok() }?;
                Some((doc, digest_of("nextParamsDigest"), height))
            });
            let kr = g("keyedRounds");
            let kr = kr.trim_matches('"');
            let keyed_rounds: u32 = if kr.is_empty() { 0 } else if let Some(x) = kr.strip_prefix("0x") { u32::from_str_radix(x, 16).unwrap_or(0) } else { kr.parse().unwrap_or(0) };
            let bond_key = net::raw_field(&raw, "bondKey").and_then(|b| proto::hex_to_32(b.trim_matches('"').trim_start_matches("0x")));
            let job = JobTemplate { job_id: format!("solo-{height}-{}", &proto::hex_encode(&header)[..8]), height, header, boundary, share_target: boundary, nonce_start: 0, nonce_count: u64::MAX, issued: Instant::now(), template: proto::hex_encode(&header), epoch, params, params_digest, next_params, keyed_rounds, bond_key, share: None, coord: None, coord_difficulty: None, coord_last_height: None, paid_coords: None, lane_worth: None, lane_worth_unknown: false, lane_workers: None, subsidy_ign: None, program_anchor: None, anchor_height: None, chain_id: 0, coord_name: None };
            if !algo.is_empty() && algo != algo_wanted {
                return Ok(GetWork::Mismatch { node_algo: algo, job });
            }
            Ok(GetWork::Job(job))
}

/// `igneum_blockReward(from, to)` (the protocol team, PA-15): per block `{number, hash, coinbase, subsidy, miner, fund,
/// priorityFees, burned}` in wei. The display takes the newest block's `miner` as the subsidy the miner keeps and
/// the mean of `priorityFees` over the range as the fees. Returns (miner_ign, mean_fees_ign).
pub const REWARD_WINDOW: u64 = 100;

pub fn parse_block_rewards(raw: &str) -> Result<(f64, f64), String> {
    parse_block_rewards_full(raw).map(|(m, f, _)| (m, f))
}

/// The same with the protocol fund's part of the newest block (`fund`, 0 when the node does not report it): the
/// chain's own rule (protocol B: fund = subsidy / 100, credited by consensus on every block), shown as the chain's
/// rule and never as a setting.
pub fn parse_block_rewards_full(raw: &str) -> Result<(f64, f64, f64), String> {
    let v: serde_json::Value = serde_json::from_str(raw).map_err(|e| format!("igneum_blockReward: {e}"))?;
    let arr = v.as_array().ok_or("igneum_blockReward: not a list")?;
    if arr.is_empty() {
        return Err("igneum_blockReward: empty".into());
    }
    let mut newest: Option<(u64, f64, f64)> = None;
    let mut fees_sum = 0.0;
    let mut fees_n = 0u32;
    for b in arr {
        let number = b.get("number").and_then(|n| n.as_u64().or_else(|| n.as_str().and_then(|s| if let Some(h) = s.strip_prefix("0x") { u64::from_str_radix(h, 16).ok() } else { s.parse().ok() }))).unwrap_or(0);
        let miner = b.get("miner").and_then(net::wei_value_to_ign).ok_or("igneum_blockReward: no miner field")?;
        let fund = b.get("fund").and_then(net::wei_value_to_ign).unwrap_or(0.0);
        if newest.map_or(true, |(n, _, _)| number >= n) {
            newest = Some((number, miner, fund));
        }
        if let Some(f) = b.get("priorityFees").and_then(net::wei_value_to_ign) {
            fees_sum += f;
            fees_n += 1;
        }
    }
    let (_, miner, fund) = newest.ok_or("igneum_blockReward: no blocks")?;
    Ok((miner, if fees_n > 0 { fees_sum / fees_n as f64 } else { 0.0 }, fund))
}

/// The range `[height-100, height-1]` as hex quantities (the node's `rpc.BlockNumber` form), never below block 1.
pub fn reward_range(height: u64) -> (String, String) {
    let to = height.saturating_sub(1).max(1);
    let from = to.saturating_sub(REWARD_WINDOW - 1).max(1);
    (format!("0x{from:x}"), format!("0x{to:x}"))
}

fn block_rewards(url: &Url, id: u64, height: u64) -> Result<(f64, f64, f64), String> {
    let (from, to) = reward_range(height);
    let raw = net::json_rpc(url, id, "igneum_blockReward", &serde_json::json!([from, to]), Duration::from_secs(10))?;
    parse_block_rewards_full(&raw)
}

/// `igneum_submitShare(address, extraNonce, nonce, mixDigest, shareHeight, parentHash)` (the node's api.go
/// SubmitShare; the reference client's call form; the sixth parameter under share gossip, optional on the node: a
/// share on a sibling reads "stale parent" by name): the answer is `{accepted, reason}`. Every share goes (the node judges recency:
/// a share's parent may be the head or one of the seven before it); the node's reasons are kept as its words with
/// the "igneum shares: " prefix dropped, so the counts read by cause.
fn submit_share(url: &Url, id: u64, address: &[u8; 20], sj: &ShareJob, share: &Share) -> SubmitOutcome {
    let t = Duration::from_secs(10);
    let mix32: [u8; 32] = match share.mix.as_slice().try_into() {
        Ok(m) => m,
        Err(_) => return SubmitOutcome::Rejected("mix is not 32 bytes".into()),
    };
    let params = serde_json::json!([
        format!("0x{}", proto::hex_encode(address)),
        format!("0x{:x}", sj.extra_nonce),
        format!("0x{:x}", share.nonce),
        format!("0x{}", proto::hex_encode(&mix32)),
        format!("0x{:x}", sj.share_height),
        format!("0x{}", proto::hex_encode(&sj.parent))
    ]);
    match net::json_rpc(url, id, "igneum_submitShare", &params, t) {
        Ok(raw) => {
            if net::raw_field(&raw, "accepted").map_or(false, |a| a == "true") {
                return SubmitOutcome::Accepted;
            }
            let reason = net::raw_field(&raw, "reason").unwrap_or_default().trim_matches('"').trim().to_string();
            let reason = reason.strip_prefix("igneum shares: ").map(str::to_string).unwrap_or(reason);
            if reason.contains("does not verify") {
                SubmitOutcome::Invalid(reason)
            } else {
                SubmitOutcome::Rejected(if reason.is_empty() { "refused".into() } else { reason })
            }
        }
        Err(e) => SubmitOutcome::Rejected(format!("no answer: {e}")),
    }
}

fn submit_work(url: &Url, id: u64, header: &[u8; 32], share: &Share) -> SubmitOutcome {
    // "verifier busy" is the one reason that asks for a retry rather than a new template (the protocol team): three
    // tries, 200 ms apart
    let mut out = submit_work_once(url, id, header, share);
    for _ in 0..2 {
        match &out {
            SubmitOutcome::Rejected(r) if r.contains("verifier busy") => {
                thread::sleep(Duration::from_millis(200));
                out = submit_work_once(url, id, header, share);
            }
            _ => break,
        }
    }
    out
}

fn submit_work_once(url: &Url, id: u64, header: &[u8; 32], share: &Share) -> SubmitOutcome {
    let t = Duration::from_secs(10);
    let nonce = format!("0x{:016x}", share.nonce);
    let mix32: [u8; 32] = match share.mix.as_slice().try_into() {
        Ok(m) => m,
        Err(_) => return SubmitOutcome::Rejected("mix is not 32 bytes".into()),
    };
    let mut params = serde_json::json!([nonce, format!("0x{}", proto::hex_encode(header)), format!("0x{}", proto::hex_encode(&mix32))]);
    // the keyed form (the protocol team's KEYED-FORM.md): igneum_submitWork(nonce, headerHash, mixDigest, shares) with
    // shares the bare hex of the S shares concatenated (S x 32 bytes), absent on an unkeyed template; the node names
    // the bond's key itself (bondKey on the template), so the public key is not sent
    if let (Some(k), Some(arr)) = (share.keyed.as_ref(), params.as_array_mut()) {
        arr.push(serde_json::Value::String(format!("0x{}", proto::hex_encode(&k.shares))));
    }
    match net::json_rpc(url, id, "igneum_submitWork", &params, t) {
        Ok(raw) => {
            let accepted = net::raw_field(&raw, "accepted").map_or(false, |a| a == "true");
            if accepted {
                SubmitOutcome::Accepted
            } else {
                let reason = net::raw_field(&raw, "reason").unwrap_or_default().trim_matches('"').to_string();
                // the node's reasons (the protocol team, 10 October 2026): "invalid proof-of-work solution: ..." is
                // invalid (the node's recomputation disagrees), "work submitted is too old" and "none pending for
                // that header hash" are stale; the older "invalid or stale" string said neither and counts as
                // rejected with the node's words; chapter 15 counts the three apart
                if reason.contains("invalid proof-of-work solution") {
                    SubmitOutcome::Invalid(reason)
                } else if reason.contains("too old") || reason.contains("none pending") || reason.contains("unknown work") || reason.contains("duplicate") {
                    SubmitOutcome::Stale
                } else {
                    SubmitOutcome::Rejected(if reason.is_empty() { "refused".into() } else { reason })
                }
            }
        }
        Err(_) => match net::json_rpc(url, id, "eth_submitWork", &params, t) {
            Ok(raw) if raw.trim() == "true" => SubmitOutcome::Accepted,
            Ok(_) => SubmitOutcome::Stale,
            Err(e) => SubmitOutcome::Rejected(e),
        },
    }
}

/// What the node thread keeps per template it has served: the header, and the share job's names when the template
/// is the share search.
struct SoloJob {
    header: [u8; 32],
    share: Option<ShareJob>,
    /// The payout address this template's shares carry: the miner's, or the dev fee's on its template.
    address: Option<[u8; 20]>,
}

/// A node's label on the share lane's per-node counts: its host and port.
fn node_label(u: &Url) -> String {
    format!("{}:{}", u.host, u.port)
}

/// `poll` are the nodes the template is read from (the one template node under the lab switch, else every node),
/// `urls` the nodes every share goes to.
fn solo_thread(poll: Vec<Url>, urls: Vec<Url>, algo: String, lane: Option<ShareLane>, dev: Option<(ShareLane, u8)>, cmds: Receiver<Cmd>, ev: Sender<Ev>, stopping: Arc<AtomicBool>) {
    // the dev fee's template counter (devfee.rs): block templates are counted by their parent as the node serves
    // them; the template numbered 99, 199, ... at 1 percent is requested on the dev lane, and every poll of the
    // same template keeps that choice
    let mut templates: u64 = 0;
    let mut last_parent: Option<[u8; 32]> = None;
    let mut current_is_fee = false;
    // the primary: the poll node that last answered the work poll; the poll tries every poll node from it in order,
    // and a block (the open seat's or a keyed one) goes to it alone; a share goes to every submit node
    let mut primary = 0usize;
    let mut id = 0u64;
    let mut last_header: Option<[u8; 32]> = None;
    let mut headers: std::collections::HashMap<String, SoloJob> = std::collections::HashMap::new();
    // insertion order, for the eviction: the oldest template leaves, never the lexicographically smallest key
    // (which evicted the current template once heights reached 100 and left every result unsubmitted)
    let mut header_order: std::collections::VecDeque<String> = std::collections::VecDeque::new();
    let mut next_poll = Instant::now();
    let mut was_ok = false;
    let mut rewards: Option<(f64, f64, f64)> = None;
    let mut next_rewards = Instant::now();
    let mut difficulty = 0.0;
    loop {
        if stopping.load(Ordering::SeqCst) {
            return;
        }
        // the queue is drained whole: a result for a template other than the latest the node served is stale
        // without a request (the node seals the latest; an older header forks), and once one result for the
        // latest template is accepted the rest for it are stale too (the 5090 pod, 11 October: sixteen block
        // solutions a second at the DAA's starting difficulty, each a node request, starved the poll and held
        // the queue for minutes)
        let mut queued: Vec<Share> = Vec::new();
        match cmds.recv_timeout(Duration::from_millis(200)) {
            Ok(Cmd::Stop) => return,
            Ok(Cmd::Submit(s)) => queued.push(s),
            Err(_) => {}
        }
        while let Ok(c) = cmds.try_recv() {
            match c {
                Cmd::Stop => return,
                Cmd::Submit(s) => queued.push(s),
            }
        }
        let latest = header_order.back().cloned();
        let mut sealed_latest = false;
        for s in queued {
            if stopping.load(Ordering::SeqCst) {
                return;
            }
            let o = match headers.get(&s.job_id) {
                // a share: every one goes, whichever recent template it was found on (the node takes a share whose
                // parent is the head or one of the seven before it, and answers each by reason)
                Some(SoloJob { share: Some(sj), address, .. }) => match address.or(lane.as_ref().map(|l| l.address)) {
                    Some(address) => {
                        let l = ShareLane { address, extra_nonce: 0 };
                        // every node takes the share (a share bound to the same parent is valid at each sealer);
                        // each node's answer is counted by its label, and the one outcome the supervisor counts
                        // is accepted when any node accepted, else the first refusal
                        let mut combined: Option<SubmitOutcome> = None;
                        for u in &urls {
                            id += 1;
                            let o = submit_share(u, id, &l.address, sj, &s);
                            let _ = ev.send(Ev::NodeAnswer(node_label(u), o.clone()));
                            combined = match (combined, o) {
                                (Some(SubmitOutcome::Accepted), _) => Some(SubmitOutcome::Accepted),
                                (_, SubmitOutcome::Accepted) => Some(SubmitOutcome::Accepted),
                                (None, o) => Some(o),
                                (c, _) => c,
                            };
                        }
                        combined.unwrap_or(SubmitOutcome::Rejected("no node".into()))
                    }
                    None => SubmitOutcome::Rejected("no share lane".into()),
                },
                Some(j) if latest.as_deref() == Some(s.job_id.as_str()) && !sealed_latest => {
                    id += 1;
                    let o = submit_work(&poll[primary], id, &j.header, &s);
                    if matches!(o, SubmitOutcome::Accepted) {
                        sealed_latest = true;
                    }
                    o
                }
                Some(_) => SubmitOutcome::Superseded,
                None => SubmitOutcome::Stale,
            };
            let _ = ev.send(Ev::Outcome(s.job_id, s.nonce, o));
        }
        if sealed_latest {
            // the node has a new template the moment it seals: poll now, not in two seconds
            next_poll = Instant::now();
        }
        if Instant::now() >= next_poll {
            id += 1;
            // the first node that answers, tried from the primary in order; the primary moves to it
            let mut got: Result<(GetWork, Option<String>), String> = Err("no node".into());
            for k in 0..poll.len() {
                let i = (primary + k) % poll.len();
                id += 1;
                match get_work(&poll[i], id, &algo, lane.as_ref()) {
                    Ok(g) => {
                        primary = i;
                        got = Ok(g);
                        break;
                    }
                    Err(e) => got = Err(format!("{}: {e}", node_label(&poll[i]))),
                }
            }
            let url = poll[primary].clone();
            // the dev fee's template choice (devfee.rs): a new block template (by its parent) takes the next
            // number; the fee's numbers are parsed again on the dev lane, every poll of that template alike
            let got = match got {
                Ok((GetWork::Job(job), raw)) if dev.is_some() => {
                    let (dev_lane, percent) = dev.as_ref().map(|(l, p)| (*l, *p)).unwrap_or((ShareLane { address: [0u8; 20], extra_nonce: 0 }, 0));
                    match job.share.as_ref().map(|s| s.parent) {
                        Some(parent) => {
                            if last_parent != Some(parent) {
                                last_parent = Some(parent);
                                current_is_fee = crate::devfee::is_fee_template(templates, percent);
                                templates += 1;
                            }
                            if current_is_fee {
                                match raw.as_deref().map(|r| parse_igneum_get_work(r, &algo, Some(&dev_lane))) {
                                    Some(Ok(GetWork::Job(fee_job))) => Ok((GetWork::Job(fee_job), raw)),
                                    _ => Ok((GetWork::Job(job), raw)),
                                }
                            } else {
                                Ok((GetWork::Job(job), raw))
                            }
                        }
                        None => Ok((GetWork::Job(job), raw)),
                    }
                }
                other => other,
            };
            let fee_address: Option<[u8; 20]> = if current_is_fee { dev.as_ref().map(|(l, _)| l.address) } else { lane.as_ref().map(|l| l.address) };
            match got {
                Ok((GetWork::Mismatch { node_algo, job }, _)) => {
                    // the chain's economics read live; no work is taken for an algorithm the workers do not carry
                    was_ok = false;
                    if Instant::now() >= next_rewards && job.height > 0 {
                        id += 1;
                        match block_rewards(&url, id, job.height) {
                            Ok(r) => {
                                rewards = Some(r);
                                next_rewards = Instant::now() + Duration::from_secs(60);
                            }
                            Err(e) => {
                                log::warn!("{e}; the subsidy stays unreported");
                                next_rewards = Instant::now() + Duration::from_secs(300);
                            }
                        }
                    }
                    difficulty = difficulty_of(&job.boundary);
                    let _ = ev.send(Ev::Economics(SourceEconomics { difficulty, block_interval_s: 15.0, subsidy_ign: rewards.map(|r| r.0), fees_ign: rewards.map_or(0.0, |r| r.1), pool_fee: Some(0.0), solo: true, fund_ign: rewards.map_or(0.0, |r| r.2) }));
                    let _ = ev.send(Ev::Problem(format!("the node mines {node_algo}; the installed workers carry {algo}; no work is taken (the chain's figures still read live)")));
                    next_poll = Instant::now() + Duration::from_secs(5);
                }
                Ok((GetWork::Job(job), _)) => {
                    if !was_ok {
                        was_ok = true;
                        let _ = ev.send(Ev::Connected);
                    }
                    if Instant::now() >= next_rewards && job.height > 0 {
                        // behind the fallback: a node without the method leaves the subsidy unreported
                        id += 1;
                        match block_rewards(&url, id, job.height) {
                            Ok(r) => {
                                rewards = Some(r);
                                next_rewards = Instant::now() + Duration::from_secs(60);
                            }
                            Err(e) => {
                                log::info!("{e}; the subsidy stays unreported");
                                next_rewards = Instant::now() + Duration::from_secs(300);
                            }
                        }
                    }
                    if last_header != Some(job.header) {
                        last_header = Some(job.header);
                        if headers.insert(job.job_id.clone(), SoloJob { header: job.header, share: job.share.clone(), address: fee_address }).is_none() {
                            header_order.push_back(job.job_id.clone());
                            if current_is_fee && job.share.is_some() {
                                let _ = ev.send(Ev::DevFeeJob(job.job_id.clone()));
                            }
                        }
                        while headers.len() > 16 {
                            match header_order.pop_front() {
                                Some(k) => {
                                    headers.remove(&k);
                                }
                                None => break,
                            }
                        }
                        difficulty = difficulty_of(&job.boundary);
                        let _ = ev.send(Ev::Economics(SourceEconomics { difficulty, block_interval_s: 15.0, subsidy_ign: rewards.map(|r| r.0), fees_ign: rewards.map_or(0.0, |r| r.1), pool_fee: Some(0.0), solo: true, fund_ign: rewards.map_or(0.0, |r| r.2) }));
                        let _ = ev.send(Ev::Job(job));
                    } else if rewards.is_some() {
                        let _ = ev.send(Ev::Economics(SourceEconomics { difficulty, block_interval_s: 15.0, subsidy_ign: rewards.map(|r| r.0), fees_ign: rewards.map_or(0.0, |r| r.1), pool_fee: Some(0.0), solo: true, fund_ign: rewards.map_or(0.0, |r| r.2) }));
                    }
                    next_poll = Instant::now() + Duration::from_secs(2);
                }
                Err(e) => {
                    was_ok = false;
                    let _ = ev.send(Ev::Problem(e));
                    next_poll = Instant::now() + Duration::from_secs(5);
                }
            }
        }
    }
}

impl WorkSource for SoloSource {
    fn name(&self) -> String {
        let u = &self.urls[0];
        let more = if self.urls.len() > 1 { format!(" and {} more node{}", self.urls.len() - 1, if self.urls.len() > 2 { "s" } else { "" }) } else { String::new() };
        format!("solo through {}://{}:{}{more}", if u.tls { "https" } else { "http" }, u.host, u.port)
    }
    fn algo(&self) -> String {
        self.algo.clone()
    }
    fn connect(&mut self, payout_address: &str, _worker_name: &str) -> Result<(), String> {
        // the node's coinbase decides where a solo block pays (docs/ecosystem/DESIGN.md 2.1); on the share lane
        // (LAUNCH-GUARDS.md 2.9) the payout address is inside every share's hashed header, so it is the lane's
        // identity here. One synchronous getWork learns the node's algorithm (the candidate id, versioned) so the
        // workers are configured for what the chain mines, not for a guess.
        self.lane = ShareLane::for_address(payout_address);
        // the first node that answers serves the session's document; every node is named in the thread
        let mut first: Result<String, String> = Err("no node".into());
        let poll: Vec<Url> = match &self.template {
            Some(t) => vec![t.clone()],
            None => self.urls.clone(),
        };
        for u in &poll {
            match net::json_rpc(u, 1, "igneum_getWork", &serde_json::json!([]), Duration::from_secs(10)) {
                Ok(raw) => {
                    first = Ok(raw);
                    break;
                }
                Err(e) => first = Err(format!("the node did not answer igneum_getWork: {e}")),
            }
        }
        match first {
            Ok(raw) => {
                let a = net::raw_field(&raw, "algorithm").unwrap_or_default();
                let a = a.trim_matches('"').to_string();
                if !a.is_empty() {
                    self.algo = a;
                }
                let (doc, digest) = session_document(&raw, self.lane.is_some());
                self.params = doc;
                self.params_digest = digest;
            }
            Err(e) => return Err(e),
        }
        let (ctx, crx) = mpsc::channel();
        let (etx, erx) = mpsc::channel();
        let (u, a, l) = (self.urls.clone(), self.algo.clone(), self.lane);
        // the dev fee's lane: the dev address with the session's extra nonce (devfee.rs); only on the share lane
        let dev: Option<(ShareLane, u8)> = match (&self.dev_fee, &self.lane) {
            (Some(f), Some(lane)) => Some((ShareLane { address: f.address, extra_nonce: lane.extra_nonce }, f.percent)),
            _ => None,
        };
        let stopping = Arc::new(AtomicBool::new(false));
        let flag = stopping.clone();
        let handle = thread::Builder::new().name("work-solo".into()).spawn(move || solo_thread(poll, u, a, l, dev, crx, etx, flag)).map_err(|e| e.to_string())?;
        self.remote = Some(Remote { params: serde_json::Value::Null, params_digest: None, tx: ctx, rx: erx, handle: Some(handle), stopping, connected: false, problem: Some("asking the node for work".into()), economics: SourceEconomics { difficulty: 0.0, block_interval_s: 15.0, subsidy_ign: None, fees_ign: 0.0, pool_fee: Some(0.0), solo: true, fund_ign: 0.0 }, pending_jobs: Vec::new(), outcomes: Vec::new(), node_answers: Vec::new(), dev_fee_templates: 0, dev_fee_jobs: Vec::new(), pool_fee: None, share_step: 0, receipts: crate::state::ReceiptCounts::default(), receipts_dir: None, receipts_host: String::new(), session_start: 0, receipts_file: None });
        Ok(())
    }
    fn poll(&mut self) -> Result<Option<JobTemplate>, String> {
        let r = self.remote.as_mut().ok_or("not connected")?;
        r.drain();
        let last = r.pending_jobs.pop();
        r.pending_jobs.clear();
        Ok(last)
    }
    fn submit(&mut self, share: &Share) -> Result<(), String> {
        let r = self.remote.as_mut().ok_or("not connected")?;
        r.tx.send(Cmd::Submit(share.clone())).map_err(|_| "the node thread has gone".to_string())
    }
    fn outcomes(&mut self) -> Vec<(String, u64, SubmitOutcome)> {
        match self.remote.as_mut() {
            Some(r) => {
                r.drain();
                std::mem::take(&mut r.outcomes)
            }
            None => Vec::new(),
        }
    }
    fn node_answers(&mut self) -> Vec<crate::state::NodeAnswers> {
        match self.remote.as_mut() {
            Some(r) => {
                r.drain();
                r.node_answers.clone()
            }
            None => Vec::new(),
        }
    }
    fn dev_fee_jobs(&mut self) -> (u64, Vec<String>) {
        match self.remote.as_mut() {
            Some(r) => {
                r.drain();
                (r.dev_fee_templates, r.dev_fee_jobs.clone())
            }
            None => (0, Vec::new()),
        }
    }
    fn economics(&self) -> SourceEconomics {
        self.remote.as_ref().map(|r| r.economics.clone()).unwrap_or(SourceEconomics { difficulty: 0.0, block_interval_s: 15.0, subsidy_ign: None, fees_ign: 0.0, pool_fee: Some(0.0), solo: true, fund_ign: 0.0 })
    }
    fn params(&self) -> serde_json::Value {
        self.params.clone()
    }
    fn params_digest(&self) -> Option<String> {
        self.params_digest.clone()
    }
    fn disconnect(&mut self) {
        if let Some(mut r) = self.remote.take() {
            r.stop();
        }
    }
    fn connected(&self) -> bool {
        self.remote.as_ref().map_or(false, |r| r.connected)
    }
    fn problem(&self) -> Option<String> {
        self.remote.as_ref().and_then(|r| r.problem.clone())
    }
}

/// The algorithm a network source mines is the one the worker carries that is not the stub; with several, the
/// first. The source refuses work for any other (the pool's `algorithm`, the node's `algorithm`).
pub fn source_for(setting: &WorkSourceSetting, algo_for_network: &str) -> Result<Box<dyn WorkSource>, String> {
    source_for_with_fee(setting, algo_for_network, None)
}

/// `source_for` with the dev fee (devfee.rs) a node source applies; a pool session binds one address at sign-in,
/// so the fee rides node sessions (the share lane) only until the pool protocol carries a per-share address.
pub fn source_for_with_fee(setting: &WorkSourceSetting, algo_for_network: &str, dev_fee: Option<crate::devfee::DevFee>) -> Result<Box<dyn WorkSource>, String> {
    match setting {
        WorkSourceSetting::Stub => Ok(Box::new(StubSource::new(2.0e7))),
        WorkSourceSetting::PoolCustom { host, port, tls } => Ok(Box::new(PoolSource::new(host, *port, *tls, host, algo_for_network).with_dev_fee(dev_fee))),
        WorkSourceSetting::Pool { pool_id } => Err(format!("the pool list is not published yet, so {pool_id} cannot be looked up; add the pool by host and port")),
        WorkSourceSetting::Solo { rpc_url } => Ok(Box::new(SoloSource::new(rpc_url, algo_for_network)?.with_dev_fee(dev_fee))),
        WorkSourceSetting::Unset => Err("Choose where to mine first.".into()),
    }
}

#[cfg(test)]
mod tests {
    #[test]
    fn the_lane_fields_read_a_worth_an_unknown_worth_and_a_worker_count() {
        let p: serde_json::Value = serde_json::from_str(r#"{"lane_worth":"1.2500e-01","lane_workers":3}"#).unwrap();
        assert_eq!(super::lane_fields(&p), (Some(0.125), false, Some(3)));
        let p: serde_json::Value = serde_json::from_str(r#"{"lane_worth":"","lane_workers":0}"#).unwrap();
        assert_eq!(super::lane_fields(&p), (None, true, Some(0)), "an empty worth is unknown; zero workers is an uncovered lane");
        let p: serde_json::Value = serde_json::from_str(r#"{"lane_worth":0.5}"#).unwrap();
        assert_eq!(super::lane_fields(&p), (Some(0.5), false, None), "a number, no count");
        let p: serde_json::Value = serde_json::from_str(r#"{"difficulty":1}"#).unwrap();
        assert_eq!(super::lane_fields(&p), (None, false, None), "absent fields read as none, not unknown");
        let p: serde_json::Value = serde_json::from_str(r#"{"lane_worth":"-1","lane_workers":"x"}"#).unwrap();
        assert_eq!(super::lane_fields(&p), (None, false, None), "a negative worth and a word count read as none");
    }

    #[test]
    fn solo_job_ids_do_not_sort_by_height() {
        // the fault of 10 October 2026: eviction by the smallest key removed "solo-100-..." before "solo-99-..."
        let a = format!("solo-{}-{}", 100, "0a0a0a0a");
        let b = format!("solo-{}-{}", 99, "0b0b0b0b");
        assert!(a < b, "lexicographic order is not height order; the eviction must use insertion order");
    }
    use super::*;

    #[test]
    fn stub_source_issues_and_judges() {
        let mut s = StubSource::new(1000.0);
        assert!(s.poll().is_err());
        s.connect("0x0", "w").unwrap();
        let j = s.poll().unwrap().unwrap();
        assert!(s.poll().unwrap().is_none());
        assert_eq!(j.height, 1);
        let share = Share { job_id: j.job_id.clone(), nonce: 1, mix: vec![], hash: [0; 32], worker_name: "w".into(), keyed: None };
        s.submit(&share).unwrap();
        let old = Share { job_id: "stub-0".into(), ..share };
        s.submit(&old).unwrap();
        let o = s.outcomes();
        assert_eq!(o[0].2, SubmitOutcome::Accepted);
        assert_eq!(o[1].2, SubmitOutcome::Stale);
        assert!(s.outcomes().is_empty());
        assert!(s.economics().solo);
    }

    #[test]
    fn sources_by_setting() {
        assert!(source_for(&WorkSourceSetting::Solo { rpc_url: "ftp://x".into() }, "a").is_err());
        assert!(source_for(&WorkSourceSetting::Solo { rpc_url: "http://127.0.0.1:8545".into() }, "a").is_ok());
        assert!(source_for(&WorkSourceSetting::PoolCustom { host: "pool.example".into(), port: 3333, tls: true }, "a").is_ok());
        assert!(source_for(&WorkSourceSetting::Pool { pool_id: "x".into() }, "a").is_err());
        assert!(source_for(&WorkSourceSetting::Stub, "a").is_ok());
    }

    #[test]
    fn block_rewards_parse_hex_and_decimal_and_take_the_newest_miner() {
        let raw = r#"[
          {"number":"0x63","hash":"0xaa","coinbase":"0x1","subsidy":"0xde0b6b3a7640000","miner":"0xdbd2fc137a30000","fund":"0x2386f26fc10000","priorityFees":"0x16345785d8a0000","burned":"0x0"},
          {"number":100,"hash":"0xbb","coinbase":"0x1","subsidy":"1000000000000000000","miner":"990000000000000000","fund":"10000000000000000","priorityFees":"0","burned":"0"}
        ]"#;
        let (miner, fees) = parse_block_rewards(raw).unwrap();
        assert!((miner - 0.99).abs() < 1e-9, "{miner}");
        assert!((fees - 0.05).abs() < 1e-9, "{fees}");
        assert!(parse_block_rewards("[]").is_err());
        assert!(parse_block_rewards(r#"[{"number":1}]"#).is_err());
        assert_eq!(reward_range(25), ("0x1".into(), "0x18".into()));
        assert_eq!(reward_range(1000), ("0x384".into(), "0x3e7".into()));
        assert_eq!(reward_range(0), ("0x1".into(), "0x1".into()));
    }

    #[test]
    fn params_digest_is_canonical() {
        let a: serde_json::Value = serde_json::from_str(r#"{"version":1, "parents":512}"#).unwrap();
        let b: serde_json::Value = serde_json::from_str(r#"{ "parents" : 512 , "version" : 1 }"#).unwrap();
        assert_eq!(params_digest(&a), params_digest(&b));
        assert_eq!(params_digest(&a).len(), 64);
        assert_eq!(params_digest(&serde_json::Value::Null), "default");
        let c: serde_json::Value = serde_json::from_str(r#"{"version":1, "parents":256}"#).unwrap();
        assert_ne!(params_digest(&a), params_digest(&c));
    }

    #[test]
    fn reasons_map_to_outcomes() {
        assert_eq!(reason_outcome("stale"), SubmitOutcome::Stale);
        assert_eq!(reason_outcome("low_difficulty"), SubmitOutcome::Invalid("low_difficulty".into()));
        assert_eq!(reason_outcome("refused before acceptance: pool at carriage: 300 shares outstanding within the recency against the 16384 the chain carries over it (256 a block for 64 blocks)"), SubmitOutcome::Held("pool at carriage".into()));
        assert_eq!(reason_outcome("refused before acceptance: address at cap: 64 of 64"), SubmitOutcome::Held("address at cap".into()));
        assert_eq!(reason_outcome("bad shares: the session is closed at the pool's threshold: stale"), SubmitOutcome::Rejected("bad shares: the session is closed at the pool's threshold: stale".into()));
        assert_eq!(held_case("refused before acceptance"), None);
        // the submit bucket (the pool's own figures since G-34): a full bucket takes one; an empty one waits one token's
        // time (1/SUBMIT_PER_S); a second's rest refills it whole
        assert_eq!(submit_pace(SUBMIT_BURST, 0.0), (SUBMIT_BURST - 1.0, 0.0));
        let (t, w) = submit_pace(0.0, 0.0);
        assert_eq!(t, 0.0);
        assert!((w - 1.0 / SUBMIT_PER_S).abs() < 1e-9);
        assert_eq!(submit_pace(0.0, 1.0), (SUBMIT_BURST - 1.0, 0.0));
        // a burst found at once beyond the bucket goes out at the pool's rate, none dropped: SUBMIT_BURST at once and
        // the rest a token's time apart
        let n = 60usize;
        let (mut tok, mut total) = (SUBMIT_BURST, 0.0f64);
        for _ in 0..n {
            // the elapsed time between submits is the wait the last one served (the shares are all in hand)
            let (t, w) = submit_pace(tok, 0.0);
            tok = t;
            total += w;
        }
        let expected = (n as f64 - SUBMIT_BURST).max(0.0) / SUBMIT_PER_S;
        assert!((total - expected).abs() < 0.05, "{total} against {expected}");
        assert_eq!(reason_outcome("rate"), SubmitOutcome::Rejected("rate".into()));
    }

    #[test]
    fn the_receipts_file_is_named_by_the_host_and_the_session_start() {
        // the settlement team's form (12 October 2026): <data dir>/receipts/<pool host>-<session start unix>.jsonl
        assert_eq!(receipts_file_name("pool.igneum.network", 1791770000), "pool.igneum.network-1791770000.jsonl");
        let mut p = PoolSource::new("pool.igneum.network", 3341, false, "the project's pool", "igneumhash");
        assert!(p.receipts_dir.is_none());
        p.set_receipts_dir(std::path::Path::new("/tmp/ember-receipts-test"));
        assert_eq!(p.receipts_dir.as_deref(), Some(std::path::Path::new("/tmp/ember-receipts-test")));
    }

    #[test]
    fn an_unreadable_notify_is_said_by_its_fields_and_a_jobless_session_redials() {
        // the project's order of 12 October 2026 on the fleet session's silence: never a skip without a word
        let p = serde_json::json!({"job_id": "ab", "header": "00".repeat(32), "boundary": "zz", "height": 360});
        let w = unreadable_notify_words(&p);
        assert!(w.starts_with("the pool sent a job this build cannot read (boundary not 64 hex (2 chars), share_target missing)"), "{w}");
        let ok = serde_json::json!({"header": "00".repeat(32), "boundary": "00".repeat(32), "share_target": "00".repeat(32)});
        assert_eq!(unreadable_notify_words(&ok), "the pool sent a job this build cannot read (); reloading the session");
        assert_eq!(JOBLESS_REDIAL_AFTER, Duration::from_secs(90));
    }

    #[test]
    fn a_pool_session_reloads_on_a_chain_switch_or_a_run_of_invalid_refusals() {
        // the pool moved from devnet8 to devnet9 under live sessions (12 October 2026, 01:47 UK): every share
        // went "invalid" against the old document and the addresses cooled
        let s = Some("aa".repeat(32));
        let j = Some("bb".repeat(32));
        let why = pool_reload_reason(s.as_deref(), j.as_deref(), false, 7319, 0).expect("a differing digest with no document reloads");
        assert!(why.starts_with("the pool's chain changed: chain 7319, document bbbb"), "{why}");
        assert!(why.ends_with("reloading the document and the chain's parameters"));
        assert_eq!(pool_reload_reason(s.as_deref(), j.as_deref(), true, 7319, 0), None, "a document attached is applied, not reloaded");
        assert_eq!(pool_reload_reason(s.as_deref(), s.as_deref(), false, 7319, 0), None, "the same digest stands");
        assert_eq!(pool_reload_reason(None, j.as_deref(), false, 0, 0), None, "no session digest to differ from");
        assert_eq!(pool_reload_reason(s.as_deref(), None, false, 0, 2), None);
        let why = pool_reload_reason(s.as_deref(), None, false, 0, INVALID_RUN_RELOAD).expect("three invalid refusals running reload");
        assert!(why.starts_with("the pool refused 3 shares running as invalid (the chain, document aaaa"), "{why}");
    }

    #[test]
    fn rlp_follows_the_node_encoding() {
        // go-ethereum/rlp: a byte under 0x80 is itself, zero is the empty string, a 32-byte string is 0xa0 + bytes,
        // a list over 55 bytes is 0xf8 + its length
        assert_eq!(rlp_uint(0), vec![0x80]);
        assert_eq!(rlp_uint(1), vec![0x01]);
        assert_eq!(rlp_uint(0x7f), vec![0x7f]);
        assert_eq!(rlp_uint(0x80), vec![0x81, 0x80]);
        assert_eq!(rlp_uint(1024), vec![0x82, 0x04, 0x00]);
        assert_eq!(rlp_bytes(&[]), vec![0x80]);
        let h = rlp_bytes(&[0x11u8; 32]);
        assert_eq!(h[0], 0xa0);
        assert_eq!(h.len(), 33);
        let a = rlp_bytes(&[0x22u8; 20]);
        assert_eq!(a[0], 0x94);
        let l = rlp_list(&[h.clone(), rlp_uint(7), a.clone(), rlp_uint(7)]);
        assert_eq!(l[0], 0xf8);
        assert_eq!(l[1] as usize, 33 + 1 + 21 + 1);
        assert_eq!(l.len(), 2 + 56);
        let long = rlp_bytes(&[0u8; 300]);
        assert_eq!(&long[..3], &[0xb7 + 2, 0x01, 0x2c]);
    }

    #[test]
    fn share_header_is_keccak_of_the_rlp_list_and_moves_with_every_field() {
        // the vector the node's ShareHeaderHash gives for these inputs (consensus/igneum/mineshares.go, read on
        // the Linux builder against main 39710e0e): parent 0x11..11, height 7, address 0x22..22, extraNonce 7
        let parent = [0x11u8; 32];
        let address = [0x22u8; 20];
        let hs = share_header(&parent, 7, &address, 7);
        assert_eq!(proto::hex_encode(&hs), SHARE_HEADER_VECTOR);
        assert_ne!(share_header(&parent, 7, &address, 8), hs);
        assert_ne!(share_header(&parent, 8, &address, 7), hs);
        assert_ne!(share_header(&[0x12u8; 32], 7, &address, 7), hs);
        assert_ne!(share_header(&parent, 7, &[0x23u8; 20], 7), hs);
        // the zero height and the zero extra nonce encode as the empty string, still a valid header
        let z = share_header(&parent, 0, &address, 0);
        assert_eq!(proto::hex_encode(&z), SHARE_HEADER_ZERO_VECTOR);
    }

    #[test]
    fn the_share_lane_takes_a_20_byte_address_only() {
        let l = ShareLane::for_address("0x5aAeb6053F3E94C9b9A09f33669435E7Ef1BeAed").unwrap();
        assert_eq!(l.address[0], 0x5a);
        assert_eq!(l.address[19], 0xed);
        assert!(l.extra_nonce > 0);
        assert!(ShareLane::for_address("0x5aAeb6053F3E94C9b9A09f33669435E7Ef1BeAe").is_none());
        assert!(ShareLane::for_address("nope").is_none());
        assert!(ShareLane::for_address("0x0").is_none());
    }

    /// The node's ShareHeaderHash (consensus/igneum/mineshares.go) for parent 0x11 x 32, height 7, address 0x22 x 20,
    /// extraNonce 7, and for height 0, extraNonce 0: printed on the Linux builder from main 39710e0e (go run ./cmd/hs-vector).
    const SHARE_HEADER_VECTOR: &str = "4fa59d789ecea83fc338722fb69254a2a3e6bed3a58f0ed0d8585e6e507dd1c9";
    const SHARE_HEADER_ZERO_VECTOR: &str = "00c87de1c2eea4c8f888463138e48803e1d49fcfa113a90c838f25f68b75df0f";

    #[test]
    fn a_share_template_is_the_share_search_on_the_block_boundary() {
        // the node's getWork with the lane (pow.go GetWork: shareTarget a number, shareParent a hash, shareHeight
        // the template's height, sharesPerBlock 160): the job is the share header against the share target with
        // the block target as its boundary (the economics read the chain's difficulty), the share's own document,
        // and never keyed; without a lane the same answer is the block search, keyed as the node says
        let raw = r#"{"headerHash":"0x1111111111111111111111111111111111111111111111111111111111111111","height":"0x2a","target":"1000","epoch":"0x0","algorithm":"igneumhash","params":{"v":9},"paramsDigest":"0x3333333333333333333333333333333333333333333333333333333333333333","shareTarget":160000,"sharesPerBlock":160,"shareHeight":42,"shareParent":"0x2222222222222222222222222222222222222222222222222222222222222222","shareParams":{"v":8},"shareParamsDigest":"0x0000000000000000000000000000000000000000000000000000000000000000","keyedRounds":2}"#;
        let lane = ShareLane { address: [0x22u8; 20], extra_nonce: 7 };
        let parent = [0x22u8; 32];
        let expected = share_header(&parent, 42, &lane.address, 7);
        let GetWork::Job(j) = parse_igneum_get_work(raw, "igneumhash", Some(&lane)).unwrap() else { panic!("the algorithm matches") };
        assert_eq!(j.job_id, format!("share-42-{}", &proto::hex_encode(&expected)[..8]));
        assert_eq!(j.header, expected);
        assert_eq!(j.height, 42);
        assert_eq!(j.share_target, net::decimal_to_u256("160000").unwrap());
        assert_eq!(j.boundary, net::decimal_to_u256("1000").unwrap());
        assert_eq!(j.params, serde_json::json!({"v": 8}));
        // a zero shareParamsDigest is the node's absent hash: the block's digest stands for the twin
        assert_eq!(j.params_digest.as_deref(), Some("3333333333333333333333333333333333333333333333333333333333333333"));
        assert_eq!(j.keyed_rounds, 0);
        assert_eq!(j.epoch, Some(0));
        assert_eq!(j.share, Some(ShareJob { share_height: 42, extra_nonce: 7, shares_per_block: 160, parent }));
        // the same answer without a lane: the block search on the sealing hash, keyed
        let GetWork::Job(b) = parse_igneum_get_work(raw, "igneumhash", None).unwrap() else { panic!("the algorithm matches") };
        assert_eq!(b.job_id, "solo-42-11111111");
        assert_eq!(b.keyed_rounds, 2);
        assert!(b.share.is_none());
        assert_eq!(b.share_target, b.boundary);
        // an answer without the lane's fields is the block search even with a lane
        let plain = r#"{"headerHash":"0x1111111111111111111111111111111111111111111111111111111111111111","height":"0x2a","target":"1000","algorithm":"igneumhash"}"#;
        let GetWork::Job(p) = parse_igneum_get_work(plain, "igneumhash", Some(&lane)).unwrap() else { panic!("the algorithm matches") };
        assert!(p.share.is_none());
        assert_eq!(p.job_id, "solo-42-11111111");
        // the node's algorithm is read on the share form too
        assert!(matches!(parse_igneum_get_work(raw, "other", Some(&lane)), Ok(GetWork::Mismatch { .. })));
        assert_eq!(quantity("\"0x10\""), Some(16));
        assert_eq!(quantity("42"), Some(42));
        assert_eq!(quantity(""), None);
    }

    #[test]
    fn the_session_document_is_the_share_twin_on_the_lane() {
        // devnet7's getWork at height 74 (read from the mesh, 11 October 2026): the block document carries
        // keyed_rounds 2 and the twin none; the first row on the second Windows machine stopped at Ember's keyed-document guard because the
        // session took the block document and asked for a bond secret the share lane never needs
        let raw = r#"{"algorithm":"igneumhash","headerHash":"0x58ccabd4a3a808ca0adfb300546268fce307ffb41eb53ddc2e13c508396c24e0","height":"74","target":"2142671105962439544486056605330913710946688341548834478256464240260415","params":{"version":8,"keyed_rounds":2,"rounds":64},"paramsDigest":"0x671c467a1b9d87c7f838b91d4ffa0d0f02d1e566eb56b0542e5e36b3d19701e5","bondKey":"0xb5655bb8d5c11e5213d87cab48ec7d49fe0715d1fb70f27b4ffb7aa7c7f4152f","keyedRounds":"2","shareTarget":"2270433122300317557324921274680155055946470287561579687048187921723786","sharesPerBlock":"160","shareHeight":"74","shareParent":"0x99835baf6e15c6c454490d35b8463257a52b67c4c7e41df13edab318f6bb0490","shareParams":{"version":8,"rounds":64},"shareParamsDigest":"0xf3ff71d531aca97bd98fcbacb0dd8282837c434795c99d83c3e150b922075fee"}"#;
        let (doc, digest) = session_document(raw, true);
        assert_eq!(doc.get("keyed_rounds"), None);
        assert_eq!(doc.get("version"), Some(&serde_json::json!(8)));
        assert_eq!(digest.as_deref(), Some("f3ff71d531aca97bd98fcbacb0dd8282837c434795c99d83c3e150b922075fee"));
        assert!(crate::bond::keyed_rounds(&doc).is_none(), "the twin asks no bond secret");
        // without the lane (no payout address of 20 bytes) the block document stands, keyed
        let (doc, digest) = session_document(raw, false);
        assert_eq!(doc.get("keyed_rounds"), Some(&serde_json::json!(2)));
        assert_eq!(digest.as_deref(), Some("671c467a1b9d87c7f838b91d4ffa0d0f02d1e566eb56b0542e5e36b3d19701e5"));
        // the template agrees with the session: the share job carries the twin and its digest
        let lane = ShareLane { address: [0x28u8; 20], extra_nonce: 1 };
        let GetWork::Job(j) = parse_igneum_get_work(raw, "igneumhash", Some(&lane)).unwrap() else { panic!("the algorithm matches") };
        assert_eq!(j.params_digest.as_deref(), Some("f3ff71d531aca97bd98fcbacb0dd8282837c434795c99d83c3e150b922075fee"));
        assert_eq!(j.keyed_rounds, 0);
        assert!(j.bond_key.is_none());
        assert_eq!(j.share.as_ref().map(|s| s.shares_per_block), Some(160));
        // a node without the lane: the block document with the zero digest left as absent
        let plain = r#"{"headerHash":"0x1111111111111111111111111111111111111111111111111111111111111111","height":"0x2a","target":"1000","params":{"version":8},"paramsDigest":"0x0000000000000000000000000000000000000000000000000000000000000000"}"#;
        let (doc, digest) = session_document(plain, true);
        assert_eq!(doc.get("version"), Some(&serde_json::json!(8)));
        assert_eq!(digest, None);
    }

    /// A tiny IS/1 pool on a local socket: subscribe, authorise, one notify, one submit accepted, one stale.
    #[test]
    fn a_coordinate_at_or_above_five_is_a_coordinates_job() {
        // F-E54: the parse took five coordinates alone (F-E9) and read coordinate 5's job as a plain one, handing its
        // document to the session's worker (0.2.11 to 0.2.13 never saw the texture lane); every coordinate under the
        // protocol's bound is a coordinate's job
        assert_eq!(proto::MAX_COORDINATES, 64);
        for c in [0u64, 4, 5, 6, 7, 63] {
            assert!(c < proto::MAX_COORDINATES as u64, "coordinate {c} is served");
        }
        assert!(64u64 >= proto::MAX_COORDINATES as u64, "coordinate 64 is refused");
    }

    #[test]
    fn pool_session_against_a_local_pool() {
        use std::io::{BufRead, BufReader, Write};
        let listener = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
        let port = listener.local_addr().unwrap().port();
        let server = thread::spawn(move || {
            let (s, _) = listener.accept().unwrap();
            let mut w = s.try_clone().unwrap();
            let mut r = BufReader::new(s);
            let mut line = String::new();
            r.read_line(&mut line).unwrap();
            assert!(line.contains("mining.subscribe"));
            let id: serde_json::Value = serde_json::from_str(&line).unwrap();
            writeln!(w, "{}", serde_json::json!({"id": id["id"], "result": {"session_id":"ab","extranonce1":"00000007","nonce_start":"30064771072","nonce_count":"4294967296","protocol":"IS/1"}, "error": null})).unwrap();
            line.clear();
            r.read_line(&mut line).unwrap();
            assert!(line.contains("mining.authorize"));
            let id: serde_json::Value = serde_json::from_str(&line).unwrap();
            writeln!(w, "{}", serde_json::json!({"id": id["id"], "result": true, "error": null})).unwrap();
            writeln!(w, "{}", serde_json::json!({"method":"mining.notify","params":{"job_id":"0123456789abcdef","height":42,"algorithm":"stub-v0","header":"11".repeat(32),"boundary":"ff".repeat(32),"share_target":"ff".repeat(32),"clean":true}})).unwrap();
            for expect in ["accepted", "stale"] {
                line.clear();
                r.read_line(&mut line).unwrap();
                assert!(line.contains("mining.submit"), "{line}");
                let v: serde_json::Value = serde_json::from_str(&line).unwrap();
                assert_eq!(v["params"][0], "0123456789abcdef");
                if expect == "accepted" {
                    writeln!(w, "{}", serde_json::json!({"id": v["id"], "result": true, "error": null})).unwrap();
                } else {
                    writeln!(w, "{}", serde_json::json!({"id": v["id"], "result": false, "error": {"code": 23, "reason": "stale"}})).unwrap();
                }
            }
            thread::sleep(Duration::from_millis(300));
        });
        let mut src = PoolSource::new("127.0.0.1", port, false, "test", "stub-v0");
        src.connect("0x5aAeb6053F3E94C9b9A09f33669435E7Ef1BeAed", "rig").unwrap();
        let t0 = Instant::now();
        let mut job = None;
        while t0.elapsed() < Duration::from_secs(5) {
            if let Some(j) = src.poll().unwrap() {
                job = Some(j);
                break;
            }
            thread::sleep(Duration::from_millis(20));
        }
        let job = job.expect("a job from the pool");
        assert_eq!(job.height, 42);
        assert_eq!(job.nonce_start, 30064771072);
        assert_eq!(job.nonce_count, 4294967296);
        assert!(src.connected());
        let share = Share { job_id: job.job_id.clone(), nonce: job.nonce_start + 5, mix: vec![0u8; 32], hash: [0u8; 32], worker_name: "rig".into(), keyed: None };
        src.submit(&share).unwrap();
        src.submit(&share).unwrap();
        let mut got = Vec::new();
        let t1 = Instant::now();
        while got.len() < 2 && t1.elapsed() < Duration::from_secs(5) {
            got.extend(src.outcomes());
            thread::sleep(Duration::from_millis(20));
        }
        assert_eq!(got.len(), 2);
        assert_eq!(got[0].2, SubmitOutcome::Accepted);
        assert_eq!(got[1].2, SubmitOutcome::Stale);
        src.disconnect();
        server.join().unwrap();
    }

    #[test]
    fn the_fee_session_takes_one_member_template_in_a_hundred_within_the_dust() {
        // every member template has a fresh fee job: templates 99, 199, ... are the fee's, ten in a thousand
        let mut due = false;
        let mut taken = Vec::new();
        for polled in 0..1_000u64 {
            let (take, carry) = fee_pick(polled, 1, due, true);
            due = carry;
            if take {
                taken.push(polled);
            }
        }
        assert_eq!(taken.len(), 10);
        assert_eq!(taken[0], 99);
        assert_eq!(taken[9], 999);
        // the fee job stale at every due template and fresh one later: the due carries, the count holds
        let mut due = false;
        let mut taken = Vec::new();
        for polled in 0..1_000u64 {
            let fresh = polled % 100 != 99;
            let (take, carry) = fee_pick(polled, 1, due, fresh);
            due = carry;
            if take {
                taken.push(polled);
            }
        }
        assert_eq!(taken, vec![100, 200, 300, 400, 500, 600, 700, 800, 900], "nine taken one late, the tenth due at the next template");
        assert!(due, "the tenth carries past the thousand");
        // the fee off: nothing taken, nothing due
        assert_eq!((0..1_000u64).filter(|&p| fee_pick(p, 0, false, true).0).count(), 0);
        assert!(!fee_pick(999, 0, false, true).1);
    }

    #[test]
    fn a_session_thread_that_ends_on_its_own_reads_as_ended_and_a_stopped_one_does_not() {
        let src = PoolSource::new("pool.test", 3341, false, "t", "igneumhash");
        let (ctx, _crx) = mpsc::channel();
        let (etx, erx) = mpsc::channel();
        let stopping = Arc::new(AtomicBool::new(false));
        let handle = thread::spawn(|| {});
        let mut r = src.remote_for(ctx, erx, handle, stopping, None);
        r.connected = true;
        let t0 = Instant::now();
        while !r.handle.as_ref().unwrap().is_finished() && t0.elapsed() < Duration::from_secs(5) {
            thread::sleep(Duration::from_millis(5));
        }
        assert!(r.ended(), "a thread that returned on its own");
        let _ = etx.send(Ev::Problem("the pool session's thread panicked (boom); redialling".into()));
        r.drain();
        assert_eq!(r.problem.as_deref(), Some("the pool session's thread panicked (boom); redialling"));
        r.stop();
        assert!(!r.ended(), "told to stop: not an end on its own");
        // the panic guard's words reach the source's problem
        let (ctx2, crx2) = mpsc::channel();
        let (etx2, erx2) = mpsc::channel();
        let stopping2 = Arc::new(AtomicBool::new(false));
        let h = PoolSource::spawn_pool_thread("work-pool-test", "127.0.0.1".into(), 1, false, "igneumhash".into(), "0x11".into(), "w".into(), None, None, None, crx2, etx2, stopping2.clone()).unwrap();
        let mut r2 = src.remote_for(ctx2, erx2, h, stopping2.clone(), None);
        // the thread dials a closed port and reports; tell it to stop, then its end is a stop, not an end on its own
        stopping2.store(true, Ordering::SeqCst);
        let t0 = Instant::now();
        while !r2.handle.as_ref().unwrap().is_finished() && t0.elapsed() < Duration::from_secs(20) {
            thread::sleep(Duration::from_millis(20));
        }
        r2.drain();
        assert!(!r2.ended());
        assert!(r2.problem.is_some(), "the closed port's words");
    }

    #[test]
    fn the_fee_job_carries_the_prefix_and_stands_in_only_fresh_at_the_height() {
        let mut s = StubSource::new(1000.0);
        let j = s.make();
        let f = fee_job_for(&j);
        assert_eq!(f.job_id, format!("{FEE_JOB_PREFIX}{}", j.job_id));
        assert_eq!(f.template, f.job_id);
        assert_eq!(f.header, j.header);
        assert_eq!(f.job_id.strip_prefix(FEE_JOB_PREFIX), Some(j.job_id.as_str()));
        assert!(fee_job_stands_in(&j, j.height), "at the member's height");
        assert!(fee_job_stands_in(&j, j.height.saturating_sub(1)), "past the member's height");
        assert!(!fee_job_stands_in(&j, j.height + 1), "behind the member's height");
        let mut old = j.clone();
        old.issued = Instant::now() - FEE_JOB_FRESH - Duration::from_secs(1);
        assert!(!fee_job_stands_in(&old, old.height), "older than FEE_JOB_FRESH");
    }
}
