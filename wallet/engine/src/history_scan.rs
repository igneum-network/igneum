//! What happened to this address on the chain (OLD-BASE.md step 3: the previous wallet's `history.rs` on the
//! restart's RPC client): transfers in and out read from the blocks' transactions, and the blocks this address
//! sealed (the block's `miner` field). Scanned forward from the last block read, a bounded number of blocks per
//! poll, and kept in `<data>/history-scan.json` keyed by the chain's genesis and the address: a chain change or
//! another address starts the scan over. The previous wallet also read block rewards and proving payouts from the
//! node's segment records; this node line has no such call, so a sealed block is an entry with no amount and the
//! words say the reward is credited by the chain's rule (the explorer's Pay pages show it).

use serde::{Deserialize, Serialize};
use serde_json::Value;
use std::path::Path;

use igneum_wallet_core::{display, Address, Result, U256};
use igneum_wallet_rpc::Rpc;

/// At most this many blocks are read in one poll (a poll runs every 2 s; a block holds few transactions on a
/// development chain), and a first scan starts this far below the head.
pub const BLOCKS_PER_POLL: u64 = 40;
pub const FIRST_SCAN_DEPTH: u64 = 2_000;
pub const KEEP: usize = 2_000;

#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct Entry {
    pub hash: String,
    /// sent | received | self | sealed
    pub kind: String,
    pub block: u64,
    pub block_hash: String,
    pub from: String,
    pub to: String,
    /// wei as a decimal string; empty for a sealed block (the reward is the chain's, not readable here)
    pub value: String,
    pub fee: String,
    pub ok: bool,
    pub time: u64,
}

#[derive(Clone, Debug, Serialize, Deserialize, Default)]
pub struct Scan {
    pub genesis: String,
    pub address: String,
    /// every block up to and including this one was read
    pub scanned_to: Option<u64>,
    /// the first scan runs from the head DOWNWARDS so the newest rows show within a minute (the twenty-box test E3,
    /// 13 October 2026: a watch import read a paid balance with History empty for an hour while the scan walked
    /// up from the depth): the lowest block read going down, and the floor it stops at
    #[serde(default)]
    pub back_to: Option<u64>,
    #[serde(default)]
    pub back_floor: Option<u64>,
    pub entries: Vec<Entry>,
}

impl Scan {
    pub fn load(path: &Path, genesis: &str, address: &Address) -> Scan {
        let a = address.to_checksum().to_ascii_lowercase();
        std::fs::read_to_string(path)
            .ok()
            .and_then(|t| serde_json::from_str::<Scan>(&t).ok())
            .filter(|s| s.genesis.eq_ignore_ascii_case(genesis) && s.address == a)
            .unwrap_or(Scan { genesis: genesis.to_string(), address: a, scanned_to: None, back_to: None, back_floor: None, entries: vec![] })
    }

    pub fn save(&self, path: &Path) -> Result<()> {
        if let Some(d) = path.parent() {
            std::fs::create_dir_all(d)?;
        }
        std::fs::write(path, serde_json::to_vec(self)?)?;
        Ok(())
    }

    /// Adds or replaces by hash; newest block first; at most KEEP entries.
    pub fn put(&mut self, e: Entry) {
        self.entries.retain(|x| !x.hash.eq_ignore_ascii_case(&e.hash));
        self.entries.push(e);
        self.entries.sort_by(|a, b| b.block.cmp(&a.block).then(b.time.cmp(&a.time)));
        self.entries.truncate(KEEP);
    }

    /// Reads the next blocks up to `head` (at most `budget` a poll: BLOCKS_PER_POLL on a named or local node, a
    /// few on the public endpoint, whose filter limits each source to twenty calls a second: the first public send
    /// row read 429 from the scan's reads, 11 October 2026), adds what touches the address, and moves `scanned_to`.
    /// Returns how many blocks were read.
    pub fn advance(&mut self, rpc: &Rpc, head: u64, budget: u64) -> Result<u64> {
        // the first pass: the head is the forward cursor at once, and the depth below it is read downwards from
        // the head, newest first, by the passes that follow; a scan file from before this form (no floor) goes on
        // as it was
        if self.scanned_to.is_none() {
            // the head itself is read by the downward pass first (back_to is one above the next block to read)
            self.scanned_to = Some(head);
            self.back_to = Some(head + 1);
            self.back_floor = Some(head.saturating_sub(FIRST_SCAN_DEPTH));
        }
        let me = self.address.clone();
        let budget = budget.max(1);
        let mut done = 0u64;
        // the public host allows twenty calls a second per source (the builder's Pay row, 12 October 2026: the
        // scan at start ran past it and stopped on "rate limited"): a pause between blocks keeps under it, and a
        // rate-limited answer ends this pass quietly, the next tick resuming from the cursors
        let mut read = |n: u64, me: &str| -> Result<Option<Vec<Entry>>> {
            match rpc.block_with_transactions(n) {
                Ok(Some(b)) => Ok(Some(entries_in(&b, n, me, rpc)?)),
                Ok(None) => Ok(None),
                Err(e) if e.to_string().contains("rate limited") => {
                    std::thread::sleep(std::time::Duration::from_millis(1500));
                    Ok(None)
                }
                Err(e) => Err(e),
            }
        };
        // 1. the new blocks above the forward cursor (the head stays fresh)
        let from = self.scanned_to.map(|n| n + 1).unwrap_or(head);
        if from <= head {
            let to = head.min(from + budget - 1);
            for n in from..=to {
                match read(n, &me)? {
                    Some(es) => {
                        for e in es {
                            self.put(e);
                        }
                    }
                    None => return Ok(done),
                }
                self.scanned_to = Some(n);
                done += 1;
                std::thread::sleep(std::time::Duration::from_millis(60));
            }
        }
        // 2. the depth below, downwards from the head to the floor, with what budget is left
        if let (Some(bt), Some(floor)) = (self.back_to, self.back_floor) {
            let mut n = bt;
            while n > floor && done < budget {
                n -= 1;
                match read(n, &me)? {
                    Some(es) => {
                        for e in es {
                            self.put(e);
                        }
                    }
                    None => return Ok(done),
                }
                self.back_to = Some(n);
                done += 1;
                std::thread::sleep(std::time::Duration::from_millis(60));
            }
        }
        Ok(done)
    }

    /// The entries as the page's history rows (the same fields as a row sent from here, with `kind` and `block`),
    /// newest first.
    pub fn rows(&self) -> Vec<Value> {
        self.entries
            .iter()
            .map(|e| {
                let value = U256::from_dec_str(&e.value).unwrap_or(U256::ZERO);
                let (to, sentence) = match e.kind.as_str() {
                    "received" => (e.to.clone(), format!("Received {} IGN from {}", display::ign(&value, 6), short(&e.from))),
                    "self" => (e.to.clone(), format!("Moved {} IGN to this address from itself", display::ign(&value, 6))),
                    "sealed" => (e.to.clone(), format!("Sealed block {}: the sealer's cut lands on the bond's account under the chain's rule (the identity page says how much per seal)", e.block)),
                    _ => (e.to.clone(), format!("Sent {} IGN to {}", display::ign(&value, 6), short(&e.to))),
                };
                serde_json::json!({
                    "hash": e.hash, "to": to, "value_ign": display::ign(&value, 18), "value_wei": e.value.clone(), "sent_unix": e.time, "sentences": [sentence],
                    "kind": e.kind, "block": e.block, "ok": e.ok, "from_scan": true
                })
            })
            .collect()
    }
}

fn s(v: &Value, k: &str) -> String {
    v.get(k).and_then(|x| x.as_str()).unwrap_or("").to_string()
}

fn q(v: &Value) -> U256 {
    v.as_str().and_then(|x| U256::from_hex(x).ok()).unwrap_or(U256::ZERO)
}

fn short(a: &str) -> String {
    Address::parse(a).map(|a| a.short()).unwrap_or_else(|_| a.to_string())
}

/// The entries of one block (with its transactions) that touch `me` (lower-case 0x address).
pub fn entries_in(b: &Value, n: u64, me: &str, rpc: &Rpc) -> Result<Vec<Entry>> {
    let mut out = Vec::new();
    let bh = s(b, "hash");
    let time = b.get("timestamp").map(q).and_then(|t| t.to_u64()).unwrap_or(0);
    if let Some(txs) = b.get("transactions").and_then(|t| t.as_array()) {
        for t in txs {
            let from = s(t, "from").to_ascii_lowercase();
            let to = s(t, "to").to_ascii_lowercase();
            if from != me && to != me {
                continue;
            }
            let hash = s(t, "hash");
            let value = q(t.get("value").unwrap_or(&Value::Null));
            let kind = if from == me && to == me { "self" } else if from == me { "sent" } else { "received" };
            let (ok, fee) = match rpc.receipt_by_hex(&hash)? {
                Some(r) => {
                    let ok = r.get("status").and_then(|x| x.as_str()).map(|x| x == "0x1").unwrap_or(true);
                    let gas = q(r.get("gasUsed").unwrap_or(&Value::Null));
                    let price = q(r.get("effectiveGasPrice").unwrap_or(&Value::Null));
                    (ok, gas.to_u64().and_then(|g| price.checked_mul_u64(g)).unwrap_or(U256::ZERO))
                }
                None => (true, U256::ZERO),
            };
            out.push(Entry { hash, kind: kind.into(), block: n, block_hash: bh.clone(), from, to, value: value.to_dec_string(), fee: fee.to_dec_string(), ok, time });
        }
    }
    if s(b, "miner").eq_ignore_ascii_case(me) {
        out.push(Entry { hash: format!("sealed-{n}"), kind: "sealed".into(), block: n, block_hash: bh, from: String::new(), to: me.to_string(), value: String::new(), fee: String::new(), ok: true, time });
    }
    Ok(out)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_scan_belongs_to_one_chain_and_address() {
        let dir = std::env::temp_dir().join(format!("igneum-scan-{}", std::process::id()));
        let p = dir.join("history-scan.json");
        let a = Address::parse("0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266").unwrap();
        let mut sc = Scan::load(&p, "0xaa", &a);
        assert!(sc.entries.is_empty() && sc.scanned_to.is_none());
        sc.put(Entry { hash: "0x01".into(), kind: "received".into(), block: 5, block_hash: "0xb".into(), from: "0xdead".into(), to: sc.address.clone(), value: "1500000000000000000".into(), fee: "0".into(), ok: true, time: 10 });
        sc.put(Entry { hash: "sealed-7".into(), kind: "sealed".into(), block: 7, block_hash: "0xc".into(), from: String::new(), to: sc.address.clone(), value: String::new(), fee: String::new(), ok: true, time: 12 });
        sc.scanned_to = Some(7);
        sc.save(&p).unwrap();
        let back = Scan::load(&p, "0xaa", &a);
        assert_eq!(back.scanned_to, Some(7));
        assert_eq!(back.entries[0].kind, "sealed");
        let rows = back.rows();
        assert_eq!(rows[0]["sentences"][0], "Sealed block 7: the sealer's cut lands on the bond's account under the chain's rule (the identity page says how much per seal)");
        assert_eq!(rows[1]["sentences"][0], "Received 1.5 IGN from 0xdead");
        assert_eq!(rows[1]["value_ign"], "1.5");
        // another chain's genesis, or another address, starts over
        assert!(Scan::load(&p, "0xbb", &a).entries.is_empty());
        let _ = std::fs::remove_dir_all(&dir);
    }
}
