//! A small JSON-RPC client over HTTP for the engine and the CLI. A crate of its own so the signer, which links only
//! the core, never carries an HTTP client (TP-16's signer_no_network probe reads the binary for one).

use serde_json::{json, Value};

use igneum_wallet_core::{hex, Address, Error, Result, U256};

pub struct Rpc {
    url: String,
    agent: ureq::Agent,
    next_id: std::cell::Cell<u64>,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Fees {
    pub base_fee_per_gas: U256,
    pub max_priority_fee_per_gas: U256,
    pub max_fee_per_gas: U256,
}

impl Rpc {
    pub fn new(url: &str) -> Self {
        let agent = ureq::AgentBuilder::new()
            .timeout_connect(std::time::Duration::from_secs(5))
            .timeout(std::time::Duration::from_secs(30))
            .build();
        Rpc { url: url.to_string(), agent, next_id: std::cell::Cell::new(1) }
    }

    pub fn url(&self) -> &str {
        &self.url
    }

    pub fn call(&self, method: &str, params: Value) -> Result<Value> {
        let id = self.next_id.get();
        self.next_id.set(id + 1);
        let body = json!({"jsonrpc": "2.0", "id": id, "method": method, "params": params});
        // a public endpoint limits each source (devnet5's filter: twenty calls a second, a burst of forty; the
        // submit route two a second): on a 429 the call waits a second and goes again, twice at most, then the
        // refusal is the caller's in the endpoint's words (the first public send row read 429, 11 October 2026)
        // devnet9's public listener answers its limit as a JSON-RPC error ("rate limited: this source exceeded the
        // public listener's rate") with status 200: the same wait and retry, three times at most (the Mining page's
        // reads at its opening tripped it, 12 October 2026)
        let mut tries = 0;
        loop {
            let resp = match self.agent.post(&self.url).set("Content-Type", "application/json").send_json(body.clone()) {
                Ok(r) => r,
                Err(ureq::Error::Status(429, _)) if tries < 3 => {
                    tries += 1;
                    std::thread::sleep(std::time::Duration::from_millis(1100));
                    continue;
                }
                Err(e) => return Err(Error::Rpc(format!("{method}: {e}"))),
            };
            let v: Value = resp.into_json().map_err(|e| Error::Rpc(format!("{method}: bad JSON: {e}")))?;
            if let Some(err) = v.get("error") {
                let msg = err.get("message").and_then(|m| m.as_str()).unwrap_or("error").to_string();
                if msg.to_ascii_lowercase().contains("rate limited") && tries < 3 {
                    tries += 1;
                    std::thread::sleep(std::time::Duration::from_millis(1100 + 400 * tries as u64));
                    continue;
                }
                return Err(Error::Rpc(format!("{method}: {msg}")));
            }
            return v.get("result").cloned().ok_or_else(|| Error::Rpc(format!("{method}: no result")));
        }
    }

    /// A JSON-RPC batch (the public edge counts a batch of up to twenty calls as one request: the Desk's header
    /// window reads twenty headers a request and never faster than the bucket). The results come back in the
    /// calls' order; a call's error is its own Err in the slot.
    pub fn call_batch(&self, calls: &[(&str, Value)]) -> Result<Vec<Result<Value>>> {
        let first = self.next_id.get();
        self.next_id.set(first + calls.len() as u64);
        let body: Vec<Value> = calls.iter().enumerate().map(|(i, (m, p))| json!({"jsonrpc": "2.0", "id": first + i as u64, "method": m, "params": p})).collect();
        let mut tries = 0;
        let resp = loop {
            match self.agent.post(&self.url).set("Content-Type", "application/json").send_json(Value::Array(body.clone())) {
                Ok(r) => break r,
                Err(ureq::Error::Status(429, _)) if tries < 2 => {
                    tries += 1;
                    std::thread::sleep(std::time::Duration::from_millis(1100));
                }
                Err(e) => return Err(Error::Rpc(format!("batch: {e}"))),
            }
        };
        let v: Value = resp.into_json().map_err(|e| Error::Rpc(format!("batch: bad JSON: {e}")))?;
        let arr = v.as_array().ok_or_else(|| Error::Rpc("batch: the answer is not an array".into()))?;
        let mut out: Vec<Result<Value>> = (0..calls.len()).map(|_| Err(Error::Rpc("batch: no answer".into()))).collect();
        for item in arr {
            let id = item.get("id").and_then(|i| i.as_u64()).unwrap_or(u64::MAX);
            if id < first || id >= first + calls.len() as u64 {
                continue;
            }
            let i = (id - first) as usize;
            out[i] = match item.get("error") {
                Some(err) => Err(Error::Rpc(format!("{}: {}", calls[i].0, err.get("message").and_then(|m| m.as_str()).unwrap_or("error")))),
                None => item.get("result").cloned().ok_or_else(|| Error::Rpc(format!("{}: no result", calls[i].0))),
            };
        }
        Ok(out)
    }

    fn quantity(v: &Value) -> Result<U256> {
        let s = v.as_str().ok_or_else(|| Error::Rpc("quantity is not a string".into()))?;
        U256::from_hex(s)
    }

    pub fn chain_id(&self) -> Result<u64> {
        Self::quantity(&self.call("eth_chainId", json!([]))?)?
            .to_u64()
            .ok_or_else(|| Error::Rpc("chain id does not fit".into()))
    }

    /// The genesis block's hash: the pin that tells one chain id 7310 chain from another (D-B14).
    pub fn genesis_hash(&self) -> Result<String> {
        let b = self.call("eth_getBlockByNumber", json!(["0x0", false]))?;
        b.get("hash")
            .and_then(|h| h.as_str())
            .map(|h| h.to_ascii_lowercase())
            .ok_or_else(|| Error::Rpc("the node has no genesis block".into()))
    }

    /// `igneum_authority`: the bonded rule's parameters and state on this node (Err when the node has no rule).
    pub fn igneum_authority(&self) -> Result<Value> {
        self.call("igneum_authority", json!([]))
    }

    /// `igneum_sortition`: the draw's parameters, the epoch, the seed, the snapshot and this node's own reading.
    pub fn igneum_sortition(&self) -> Result<Value> {
        self.call("igneum_sortition", json!([]))
    }

    /// `igneum_bondOf(key, block)`: a key's bond at a block's state (amount, account, signer, bonded).
    pub fn igneum_bond_of(&self, key: &[u8; 32], block: &str) -> Result<Value> {
        // the key as 0x-prefixed hex: the node's common.Hash refuses bare hex (devnet9's A9 direct; the devnet7 edge took both)
        self.call("igneum_bondOf", json!([hex::encode(key), block]))
    }

    pub fn block_number(&self) -> Result<u64> {
        Self::quantity(&self.call("eth_blockNumber", json!([]))?)?
            .to_u64()
            .ok_or_else(|| Error::Rpc("block number does not fit".into()))
    }

    pub fn balance(&self, a: &Address) -> Result<U256> {
        Self::quantity(&self.call("eth_getBalance", json!([a.to_checksum(), "latest"]))?)
    }

    /// The next nonce including the pool.
    pub fn nonce(&self, a: &Address) -> Result<u64> {
        Self::quantity(&self.call("eth_getTransactionCount", json!([a.to_checksum(), "pending"]))?)?
            .to_u64()
            .ok_or_else(|| Error::Rpc("nonce does not fit".into()))
    }

    /// EIP-1559 fees from the pending block's base fee and the node's tip estimate; a 1 gwei tip floor. A node with
    /// no pending block (a sortition sealer holds no template while it waits for its round: devnet4, 11 October
    /// 2026) gives the latest block's base fee instead; the max fee of twice the base covers the next block's rise.
    pub fn fees(&self) -> Result<Fees> {
        let mut base = None;
        for tag in ["pending", "latest"] {
            let block = self.call("eth_getBlockByNumber", json!([tag, false]))?;
            if let Some(b) = block.get("baseFeePerGas").filter(|b| !b.is_null()) {
                base = Some(Self::quantity(b)?);
                break;
            }
        }
        let base = base.ok_or_else(|| Error::Rpc("the node reports no base fee: is EIP-1559 live?".into()))?;
        let tip = match self.call("eth_maxPriorityFeePerGas", json!([])) {
            Ok(v) => Self::quantity(&v)?,
            Err(_) => U256::from_u64(1_000_000_000),
        };
        let tip = if tip < U256::from_u64(1_000_000_000) { U256::from_u64(1_000_000_000) } else { tip };
        // max fee: twice the base fee plus the tip, so a fee rise over a few blocks still lands
        let max = base
            .checked_mul_u64(2)
            .and_then(|b| b.checked_add(&tip))
            .ok_or_else(|| Error::Rpc("fee arithmetic overflow".into()))?;
        Ok(Fees { base_fee_per_gas: base, max_priority_fee_per_gas: tip, max_fee_per_gas: max })
    }

    /// Whether an address holds code: a plain transfer to a contract runs its `receive` and needs more than the
    /// bare 21,000 (found 11 October 2026: two credits to contracts failed at 21,000 with status 0).
    pub fn has_code(&self, address: &str) -> Result<bool> {
        let code = self.call("eth_getCode", json!([address, "latest"]))?;
        Ok(code.as_str().map(|c| c.len() > 2).unwrap_or(false))
    }

    pub fn estimate_gas(&self, call: &Value) -> Result<u64> {
        Self::quantity(&self.call("eth_estimateGas", json!([call]))?)?
            .to_u64()
            .ok_or_else(|| Error::Rpc("gas estimate does not fit".into()))
    }

    pub fn eth_call(&self, call: &Value) -> Result<Vec<u8>> {
        // on top of the pending block when the node holds one, else the latest (a sortition sealer between rounds)
        let v = match self.call("eth_call", json!([call, "pending"])) {
            Ok(v) => v,
            // devnet9's build answers a call at the pending tag with -32603 "method handler crashed" (evm-pay's F-9,
            // 12 October 2026, with the protocol team for the fix): the call is made again at the latest block
            Err(e) if e.to_string().contains("pending") || e.to_string().contains("not found") || e.to_string().contains("handler crashed") => self.call("eth_call", json!([call, "latest"]))?,
            Err(e) => return Err(e),
        };
        hex::decode(v.as_str().unwrap_or("0x"))
    }

    /// debug_traceCall with the call tracer on the latest block; Err when the node does not serve it.
    /// "latest", not "pending": Core-Geth refuses to trace on top of the pending block ("tracing on top of pending
    /// is not supported", measured on builder seven against the dev node, 9 October 2026), and the latest state is what
    /// the simulation needs; eth_call keeps "pending" so the two together show both views.
    pub fn trace_call(&self, call: &Value) -> Result<Value> {
        self.call(
            "debug_traceCall",
            json!([call, "latest", {"tracer": "callTracer", "tracerConfig": {"withLog": true}}]),
        )
    }

    pub fn send_raw(&self, raw: &[u8]) -> Result<[u8; 32]> {
        let v = self.call("eth_sendRawTransaction", json!([hex::encode(raw)]))?;
        let bytes = hex::decode(v.as_str().unwrap_or(""))?;
        if bytes.len() != 32 {
            return Err(Error::Rpc("the node returned a hash of the wrong length".into()));
        }
        let mut h = [0u8; 32];
        h.copy_from_slice(&bytes);
        Ok(h)
    }

    pub fn receipt(&self, hash: &[u8; 32]) -> Result<Option<Value>> {
        let v = self.call("eth_getTransactionReceipt", json!([hex::encode(hash)]))?;
        Ok(if v.is_null() { None } else { Some(v) })
    }

    pub fn transaction(&self, hash: &[u8; 32]) -> Result<Option<Value>> {
        let v = self.call("eth_getTransactionByHash", json!([hex::encode(hash)]))?;
        Ok(if v.is_null() { None } else { Some(v) })
    }

    pub fn block(&self, number: u64) -> Result<Option<Value>> {
        let v = self.call("eth_getBlockByNumber", json!([U256::from_u64(number).to_hex(), false]))?;
        Ok(if v.is_null() { None } else { Some(v) })
    }

    /// The block with its transactions in full (the history scan).
    pub fn block_with_transactions(&self, number: u64) -> Result<Option<Value>> {
        let v = self.call("eth_getBlockByNumber", json!([U256::from_u64(number).to_hex(), true]))?;
        Ok(if v.is_null() { None } else { Some(v) })
    }

    /// A receipt by its 0x hash as the node printed it (the history scan reads hashes from blocks).
    pub fn receipt_by_hex(&self, hash: &str) -> Result<Option<Value>> {
        let v = self.call("eth_getTransactionReceipt", json!([hash]))?;
        Ok(if v.is_null() { None } else { Some(v) })
    }

    /// (difficulty, timestamp) of a block, for the settlement model.
    pub fn block_work(&self, number: u64) -> Result<Option<(U256, u64)>> {
        let b = match self.block(number)? {
            Some(b) => b,
            None => return Ok(None),
        };
        let d = b.get("difficulty").map(Self::quantity).transpose()?.unwrap_or(U256::ZERO);
        let t = b.get("timestamp").map(Self::quantity).transpose()?.and_then(|t| t.to_u64()).unwrap_or(0);
        Ok(Some((d, t)))
    }
}
