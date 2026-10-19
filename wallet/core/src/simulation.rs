//! What a transaction is expected to do, read from the node's `debug_traceCall` output (the `callTracer` with
//! `withLog`): native coin that leaves or arrives, token moves and approvals from the standard events, the contracts
//! touched, and the revert reason when there is one. The sentences say "expected": the chain may differ when the
//! transaction lands, and the window says so under them.

use serde::{Deserialize, Serialize};
use serde_json::Value;

use crate::{display, hex, keccak::keccak256, Address, U256};

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct NativeMove {
    pub from: Address,
    pub to: Address,
    pub amount: U256,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct TokenMove {
    pub token: Address,
    pub from: Address,
    pub to: Address,
    /// the amount for ERC-20 and ERC-1155, the token id for ERC-721
    pub amount: U256,
    pub kind: TokenKind,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum TokenKind {
    Erc20,
    Erc721,
    Erc1155,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct ApprovalChange {
    pub token: Address,
    pub owner: Address,
    pub spender: Address,
    /// None for setApprovalForAll
    pub amount: Option<U256>,
    pub all: bool,
    pub unlimited: bool,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct Summary {
    pub reverted: Option<String>,
    pub gas_used: Option<u64>,
    pub native: Vec<NativeMove>,
    pub tokens: Vec<TokenMove>,
    pub approvals: Vec<ApprovalChange>,
    pub contracts_called: Vec<Address>,
    pub sentences: Vec<String>,
}

fn topic(sig: &str) -> String {
    hex::encode(&keccak256(sig.as_bytes()))
}

fn topic_address(t: &str) -> Option<Address> {
    let b = hex::decode(t).ok()?;
    if b.len() != 32 {
        return None;
    }
    Address::from_slice(&b[12..]).ok()
}

fn word_u256(data: &[u8], i: usize) -> Option<U256> {
    let w = data.get(32 * i..32 * i + 32)?;
    U256::from_be_bytes(w).ok()
}

fn addr(v: &Value) -> Option<Address> {
    v.as_str().and_then(|s| Address::parse(s).ok())
}

fn quantity(v: &Value) -> Option<U256> {
    v.as_str().and_then(|s| U256::from_hex(s).ok())
}

/// Reads one call frame and its children.
fn walk(frame: &Value, me: &Address, out: &mut Summary, depth: usize) {
    if depth > 64 {
        return;
    }
    let from = addr(&frame["from"]);
    let to = addr(&frame["to"]);
    let value = quantity(&frame["value"]).unwrap_or(U256::ZERO);
    let kind = frame["type"].as_str().unwrap_or("CALL");
    if let Some(t) = to {
        if kind != "STATICCALL" && kind != "DELEGATECALL" && !out.contracts_called.contains(&t) && depth > 0 {
            out.contracts_called.push(t);
        }
        if depth == 0 && !frame["input"].as_str().map(|i| i == "0x" || i.is_empty()).unwrap_or(true) {
            out.contracts_called.push(t);
        }
    }
    if !value.is_zero() && kind != "STATICCALL" && kind != "DELEGATECALL" {
        if let (Some(f), Some(t)) = (from, to) {
            if f == *me || t == *me {
                out.native.push(NativeMove { from: f, to: t, amount: value });
            }
        }
    }
    if let Some(logs) = frame["logs"].as_array() {
        for log in logs {
            read_log(log, me, out);
        }
    }
    if let Some(calls) = frame["calls"].as_array() {
        for c in calls {
            walk(c, me, out, depth + 1);
        }
    }
}

fn read_log(log: &Value, me: &Address, out: &mut Summary) {
    let token = match addr(&log["address"]) {
        Some(a) => a,
        None => return,
    };
    let topics: Vec<String> = log["topics"]
        .as_array()
        .map(|t| t.iter().filter_map(|x| x.as_str().map(|s| s.to_lowercase())).collect())
        .unwrap_or_default();
    let data = log["data"].as_str().and_then(|d| hex::decode(d).ok()).unwrap_or_default();
    if topics.is_empty() {
        return;
    }
    let t0 = topics[0].as_str();
    let transfer = topic("Transfer(address,address,uint256)");
    let approval = topic("Approval(address,address,uint256)");
    let approval_all = topic("ApprovalForAll(address,address,bool)");
    let single = topic("TransferSingle(address,address,address,uint256,uint256)");
    let batch = topic("TransferBatch(address,address,address,uint256[],uint256[])");
    if t0 == transfer && topics.len() >= 3 {
        let (from, to) = (topic_address(&topics[1]), topic_address(&topics[2]));
        if let (Some(f), Some(t)) = (from, to) {
            if topics.len() == 4 {
                if let Some(id) = topic_address_u256(&topics[3]) {
                    if f == *me || t == *me {
                        out.tokens.push(TokenMove { token, from: f, to: t, amount: id, kind: TokenKind::Erc721 });
                    }
                }
            } else if let Some(amount) = word_u256(&data, 0) {
                if f == *me || t == *me {
                    out.tokens.push(TokenMove { token, from: f, to: t, amount, kind: TokenKind::Erc20 });
                }
            }
        }
    } else if t0 == approval && topics.len() >= 3 {
        if let (Some(owner), Some(spender)) = (topic_address(&topics[1]), topic_address(&topics[2])) {
            if owner == *me {
                let amount = if topics.len() == 4 { topic_address_u256(&topics[3]) } else { word_u256(&data, 0) };
                let unlimited = amount == Some(U256::MAX);
                out.approvals.push(ApprovalChange { token, owner, spender, amount, all: false, unlimited });
            }
        }
    } else if t0 == approval_all && topics.len() >= 3 {
        if let (Some(owner), Some(operator)) = (topic_address(&topics[1]), topic_address(&topics[2])) {
            if owner == *me {
                let on = word_u256(&data, 0).map(|v| !v.is_zero()).unwrap_or(false);
                out.approvals.push(ApprovalChange { token, owner, spender: operator, amount: None, all: on, unlimited: on });
            }
        }
    } else if t0 == single && topics.len() >= 4 {
        if let (Some(f), Some(t)) = (topic_address(&topics[2]), topic_address(&topics[3])) {
            if let (Some(id), Some(amount)) = (word_u256(&data, 0), word_u256(&data, 1)) {
                if f == *me || t == *me {
                    let _ = id;
                    out.tokens.push(TokenMove { token, from: f, to: t, amount, kind: TokenKind::Erc1155 });
                }
            }
        }
    } else if t0 == batch && topics.len() >= 4 {
        if let (Some(f), Some(t)) = (topic_address(&topics[2]), topic_address(&topics[3])) {
            if f == *me || t == *me {
                // the amounts array: offset word 1, then length, then items
                if let Some(off) = word_u256(&data, 1).and_then(|o| o.to_u64()) {
                    let base = off as usize / 32;
                    if let Some(len) = word_u256(&data, base).and_then(|l| l.to_u64()) {
                        let mut total = U256::ZERO;
                        for i in 0..len as usize {
                            if let Some(a) = word_u256(&data, base + 1 + i) {
                                total = total.checked_add(&a).unwrap_or(U256::MAX);
                            }
                        }
                        out.tokens.push(TokenMove { token, from: f, to: t, amount: total, kind: TokenKind::Erc1155 });
                    }
                }
            }
        }
    }
}

fn topic_address_u256(t: &str) -> Option<U256> {
    let b = hex::decode(t).ok()?;
    U256::from_be_bytes(&b).ok()
}

/// Summarises a `callTracer` result for the person at `me`.
pub fn summarise(trace: &Value, me: &Address) -> Summary {
    let mut out = Summary::default();
    if let Some(err) = trace["error"].as_str() {
        let reason = trace["revertReason"].as_str().unwrap_or("");
        out.reverted = Some(if reason.is_empty() { err.to_string() } else { format!("{err}: {reason}") });
    }
    out.gas_used = quantity(&trace["gasUsed"]).and_then(|g| g.to_u64());
    walk(trace, me, &mut out, 0);
    out.sentences = sentences(&out, me);
    out
}

fn sentences(s: &Summary, me: &Address) -> Vec<String> {
    let mut v = Vec::new();
    if let Some(r) = &s.reverted {
        v.push(format!("This would fail: {r}"));
        return v;
    }
    for n in &s.native {
        if n.from == *me {
            v.push(format!("Expected: {} IGN leaves you for {}", display::amount_words(&n.amount), n.to.short()));
        } else {
            v.push(format!("Expected: {} IGN arrives from {}", display::amount_words(&n.amount), n.from.short()));
        }
    }
    for t in &s.tokens {
        let what = match t.kind {
            TokenKind::Erc20 => format!("{} units of the token at {}", t.amount.to_dec_string(), t.token.short()),
            TokenKind::Erc721 => format!("item {} of the collection at {}", t.amount.to_dec_string(), t.token.short()),
            TokenKind::Erc1155 => format!("{} items of the collection at {}", t.amount.to_dec_string(), t.token.short()),
        };
        if t.from == *me {
            v.push(format!("Expected: {what} leave you for {}", t.to.short()));
        } else {
            v.push(format!("Expected: {what} arrive from {}", t.from.short()));
        }
    }
    for a in &s.approvals {
        if a.amount.is_none() {
            v.push(if a.all {
                format!("Expected: {} may move EVERY item of the collection at {} from now on", a.spender.short(), a.token.short())
            } else {
                format!("Expected: {}'s permission over the collection at {} is withdrawn", a.spender.short(), a.token.short())
            });
        } else if a.unlimited {
            v.push(format!("Expected: {} may spend an UNLIMITED amount of the token at {} from now on", a.spender.short(), a.token.short()));
        } else {
            v.push(format!(
                "Expected: {} may spend up to {} units of the token at {} (not unlimited)",
                a.spender.short(),
                a.amount.map(|x| x.to_dec_string()).unwrap_or_default(),
                a.token.short()
            ));
        }
    }
    if !s.contracts_called.is_empty() {
        let names: Vec<String> = s.contracts_called.iter().map(|c| c.short()).collect();
        v.push(format!("Contracts touched: {}", names.join(", ")));
    }
    if v.is_empty() {
        v.push("Expected: nothing moves but the fee".to_string());
    }
    v.push("The chain may differ from this when the transaction lands".to_string());
    v
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    fn me() -> Address {
        Address::parse("0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266").unwrap()
    }

    fn padded(a: &Address) -> String {
        format!("0x{}{}", "0".repeat(24), &hex::encode(&a.0)[2..])
    }

    #[test]
    fn plain_transfer() {
        let to = Address::parse("0x70997970C51812dc3A010C7d01b50e0d17dc79C8").unwrap();
        let trace = json!({"from": me(), "to": to, "value": "0x14d1120d7b160000", "gas": "0x5208", "gasUsed": "0x5208", "input": "0x", "type": "CALL"});
        let s = summarise(&trace, &me());
        assert_eq!(s.native.len(), 1);
        assert_eq!(s.sentences[0], "Expected: 1.5 IGN leaves you for 0x709979\u{2026}79C8");
        assert!(s.contracts_called.is_empty());
        assert_eq!(s.gas_used, Some(21000));
    }

    #[test]
    fn token_transfer_and_unlimited_approval() {
        let token = Address::parse("0x3C44CdDdB6a900fa2b585dd299e03d12FA4293BC").unwrap();
        let spender = Address::parse("0x90F79bf6EB2c4f870365E785982E1f101E93b906").unwrap();
        let transfer = topic("Transfer(address,address,uint256)");
        let approval = topic("Approval(address,address,uint256)");
        let trace = json!({"from": me(), "to": token, "value": "0x0", "input": "0xa9059cbb", "type": "CALL", "gasUsed": "0xc350",
            "logs": [
                {"address": token, "topics": [transfer, padded(&me()), padded(&spender)], "data": "0x0000000000000000000000000000000000000000000000000de0b6b3a7640000"},
                {"address": token, "topics": [approval, padded(&me()), padded(&spender)], "data": format!("0x{}", "f".repeat(64))}
            ]});
        let s = summarise(&trace, &me());
        assert_eq!(s.tokens.len(), 1);
        assert_eq!(s.tokens[0].kind, TokenKind::Erc20);
        assert_eq!(s.tokens[0].amount.to_dec_string(), "1000000000000000000");
        assert_eq!(s.approvals.len(), 1);
        assert!(s.approvals[0].unlimited);
        assert!(s.sentences.iter().any(|l| l.contains("UNLIMITED")));
        assert!(s.sentences.iter().any(|l| l.starts_with("Expected: 1000000000000000000 units of the token at") && l.contains("leave you")));
        assert_eq!(s.contracts_called, vec![token]);
    }

    #[test]
    fn erc721_and_revert() {
        let coll = Address::parse("0x3C44CdDdB6a900fa2b585dd299e03d12FA4293BC").unwrap();
        let other = Address::parse("0x70997970C51812dc3A010C7d01b50e0d17dc79C8").unwrap();
        let transfer = topic("Transfer(address,address,uint256)");
        let trace = json!({"from": me(), "to": coll, "value": "0x0", "input": "0x23b872dd", "type": "CALL",
            "logs": [{"address": coll, "topics": [transfer, padded(&other), padded(&me()), format!("0x{}7", "0".repeat(63))], "data": "0x"}]});
        let s = summarise(&trace, &me());
        assert_eq!(s.tokens[0].kind, TokenKind::Erc721);
        assert!(s.sentences[0].starts_with("Expected: item 7 of the collection at"));
        assert!(s.sentences[0].contains("arrive from"));
        let failing = json!({"from": me(), "to": coll, "value": "0x0", "input": "0x01", "type": "CALL", "error": "execution reverted", "revertReason": "not the owner"});
        let f = summarise(&failing, &me());
        assert_eq!(f.sentences, vec!["This would fail: execution reverted: not the owner".to_string()]);
    }

    #[test]
    fn nested_native_moves_and_calls() {
        let pay = Address::parse("0x3C44CdDdB6a900fa2b585dd299e03d12FA4293BC").unwrap();
        let payee = Address::parse("0x70997970C51812dc3A010C7d01b50e0d17dc79C8").unwrap();
        let trace = json!({"from": me(), "to": pay, "value": "0xde0b6b3a7640000", "input": "0x12345678", "type": "CALL",
            "calls": [{"from": pay, "to": payee, "value": "0xde0b6b3a7640000", "input": "0x", "type": "CALL"}]});
        let s = summarise(&trace, &me());
        // only the move that touches me is listed: the one from me to the contract
        assert_eq!(s.native.len(), 1);
        assert_eq!(s.native[0].to, pay);
        assert_eq!(s.contracts_called, vec![pay, payee]);
        assert!(s.sentences.last().unwrap().starts_with("The chain may differ"));
    }
}
