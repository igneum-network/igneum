//! The page bridge's words and rules (docs/product-b/DESIGN.md, 1.7): what a website's request means, what the
//! window says, and what is refused before anything is signed. The engine does the asking; this module is pure.

use serde::{Deserialize, Serialize};
use serde_json::Value;

use crate::{display, eip712, hex, tx::Transaction, Address, Error, Result};

/// What a request needs from the person.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(tag = "kind", rename_all = "snake_case")]
pub enum Ask {
    /// see the address; remembered per site
    Connect,
    /// a transaction, shown by its sentences
    SendTransaction { sentences: Vec<String> },
    /// a personal message, shown in full as text or named as binary
    SignMessage { text: Option<String>, bytes: usize },
    /// typed data, shown field by field
    SignTypedData { lines: Vec<String>, chain_id: Option<u64> },
    /// a connect from a request that carries no browser's own headers (Sec-Fetch): a program on this machine wearing
    /// a site's name; the ask says so (the security review's IPC-1 residual, wallet-security's closing form, 12 October 2026)
    ConnectLocal,
    /// switch to a network the wallet knows
    SwitchChain { chain_id: u64 },
    /// show a token in the display only
    WatchAsset { address: Address, symbol: String },
}

impl Ask {
    /// The card's title, in the words of the kept look ("a website asks").
    pub fn title(&self, host: &str) -> String {
        match self {
            Ask::Connect => format!("{host} asks to see your address"),
            Ask::ConnectLocal => format!("a program on this machine, not a browser, asks to see your address under the name {host}"),
            Ask::SendTransaction { .. } => format!("{host} asks you to send a transaction"),
            Ask::SignMessage { .. } => format!("{host} asks you to sign a message"),
            Ask::SignTypedData { .. } => format!("{host} asks you to sign typed data"),
            Ask::SwitchChain { chain_id } => format!("{host} asks to switch to chain {chain_id}"),
            Ask::WatchAsset { symbol, .. } => format!("{host} asks to show the token {symbol}"),
        }
    }

    pub fn yes_word(&self) -> &'static str {
        match self {
            Ask::Connect | Ask::ConnectLocal => "Connect",
            Ask::SendTransaction { .. } => "Send",
            Ask::SignMessage { .. } | Ask::SignTypedData { .. } => "Sign",
            Ask::SwitchChain { .. } => "Switch",
            Ask::WatchAsset { .. } => "Show it",
        }
    }

    /// The lines under the title.
    pub fn lines(&self) -> Vec<String> {
        match self {
            Ask::Connect => vec![
                "It will see your address and your balance, and can ask you to sign or send; every request is shown here first.".into(),
                "Disconnect it any time in Settings.".into(),
            ],
            Ask::ConnectLocal => vec![
                "A browser's request carries its own fetch headers; this one carries none, so it comes from a program running on this machine, whatever name it gives.".into(),
                "If you did not start such a program, say no. If you did (a command-line tool, a script), it will see your address and can ask you to sign or send; every request is shown here first.".into(),
            ],
            Ask::SendTransaction { sentences } => sentences.clone(),
            Ask::SignMessage { text: Some(t), .. } => vec![format!("Message: {t}"), "Signing a message costs nothing and moves nothing; a site may use it to prove you hold this address.".into()],
            Ask::SignMessage { text: None, bytes } => vec![format!("Binary data, {bytes} bytes, not text: sign only if you know what it is.")],
            Ask::SignTypedData { lines, .. } => lines.clone(),
            Ask::SwitchChain { .. } => vec!["Only networks this wallet knows.".into()],
            Ask::WatchAsset { address, .. } => vec![format!("Contract {}; the display only, nothing is approved.", address.short())],
        }
    }
}

/// What a site asked to send; the engine completes nonce, fees and gas.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct SendParams {
    pub to: Option<Address>,
    pub value: crate::U256,
    pub data: Vec<u8>,
    pub gas: Option<u64>,
}

/// Reads `eth_sendTransaction`'s one parameter.
pub fn parse_send_params(p: &Value, from: &Address) -> Result<SendParams> {
    let o = p.get(0).ok_or_else(|| Error::Input("eth_sendTransaction needs a transaction object".into()))?;
    if let Some(f) = o.get("from").and_then(|f| f.as_str()) {
        if Address::parse(f)? != *from {
            return Err(Error::Refused(format!("the site named {} as the sender but this wallet is {}", Address::parse(f)?.short(), from.short())));
        }
    }
    let to = match o.get("to").and_then(|t| t.as_str()) {
        Some(t) if !t.is_empty() => Some(Address::parse(t)?),
        _ => None,
    };
    let value = match o.get("value").and_then(|v| v.as_str()) {
        Some(v) => crate::U256::from_hex(v)?,
        None => crate::U256::ZERO,
    };
    let data = match o.get("data").or_else(|| o.get("input")).and_then(|d| d.as_str()) {
        Some(d) => hex::decode(d)?,
        None => vec![],
    };
    let gas = match o.get("gas").and_then(|g| g.as_str()) {
        Some(g) => crate::U256::from_hex(g)?.to_u64(),
        None => None,
    };
    Ok(SendParams { to, value, data, gas })
}

/// `personal_sign`: the message as text when it is valid UTF-8 without control characters, else as bytes.
/// A message that looks like a transaction or typed data is refused: a site must use the proper request.
pub fn parse_personal_sign(p: &Value) -> Result<(Vec<u8>, Ask)> {
    // the parameters are [message, address] (or the reverse in old clients); the message is the hex or text one
    let a = p.get(0).and_then(|x| x.as_str()).unwrap_or("");
    let b = p.get(1).and_then(|x| x.as_str()).unwrap_or("");
    let msg_text = if Address::parse(a).is_ok() && !Address::parse(b).is_ok() { b } else { a };
    let bytes = if msg_text.starts_with("0x") || msg_text.starts_with("0X") {
        hex::decode(msg_text).unwrap_or_else(|_| msg_text.as_bytes().to_vec())
    } else {
        msg_text.as_bytes().to_vec()
    };
    if bytes.is_empty() {
        return Err(Error::Refused("an empty message".into()));
    }
    if bytes[0] == 0x02 || bytes[0] == 0x01 || (bytes.len() > 2 && bytes[0] >= 0xc0) {
        return Err(Error::Refused("this message is shaped like a transaction; a site must use eth_sendTransaction".into()));
    }
    if bytes.len() == 2 + 64 && bytes[0] == 0x19 && bytes[1] == 0x01 {
        return Err(Error::Refused("this message is shaped like typed data; a site must use eth_signTypedData_v4".into()));
    }
    let text = match std::str::from_utf8(&bytes) {
        Ok(s) if !s.chars().any(|c| c.is_control() && c != '\n' && c != '\t' && c != '\r') => Some(s.to_string()),
        _ => None,
    };
    let n = bytes.len();
    Ok((bytes, Ask::SignMessage { text, bytes: n }))
}

/// `eth_signTypedData_v4`: [address, typedDataJson]. Returns the digest and the ask.
pub fn parse_typed_data(p: &Value, from: &Address, wallet_chain: u64) -> Result<([u8; 32], Ask)> {
    let addr = p.get(0).and_then(|x| x.as_str()).ok_or_else(|| Error::Input("eth_signTypedData_v4 needs the address".into()))?;
    if Address::parse(addr)? != *from {
        return Err(Error::Refused("the site named another address".into()));
    }
    let raw = p.get(1).ok_or_else(|| Error::Input("eth_signTypedData_v4 needs the typed data".into()))?;
    let typed: Value = match raw {
        Value::String(s) => serde_json::from_str(s).map_err(|e| Error::Input(format!("the typed data is not JSON: {e}")))?,
        other => other.clone(),
    };
    eip712::check_shape(&typed)?;
    let chain = eip712::domain_chain_id(&typed);
    if let Some(c) = chain {
        if c != wallet_chain {
            return Err(Error::Refused(format!("the typed data is for chain {c}; this wallet is on chain {wallet_chain}")));
        }
    }
    let d = eip712::digest(&typed)?;
    let lines = eip712::render(&typed)?;
    Ok((d, Ask::SignTypedData { lines, chain_id: chain }))
}

/// Which methods need no permission (reads the engine answers from its node).
pub fn is_read(method: &str) -> bool {
    matches!(
        method,
        "eth_chainId" | "net_version" | "eth_blockNumber" | "eth_call" | "eth_estimateGas" | "eth_gasPrice"
            | "eth_maxPriorityFeePerGas" | "eth_feeHistory" | "eth_getBalance" | "eth_getCode" | "eth_getStorageAt"
            | "eth_getTransactionCount" | "eth_getBlockByNumber" | "eth_getBlockByHash" | "eth_getTransactionByHash"
            | "eth_getTransactionReceipt" | "eth_getLogs" | "web3_clientVersion" | "eth_sendRawTransaction"
    )
}

/// Methods that are refused outright, with the reason.
pub fn refused(method: &str) -> Option<&'static str> {
    match method {
        "eth_sign" => Some("this wallet never signs a bare hash"),
        "eth_signTypedData" | "eth_signTypedData_v1" | "eth_signTypedData_v3" => Some("use eth_signTypedData_v4"),
        "eth_decrypt" | "eth_getEncryptionPublicKey" => Some("not offered"),
        "wallet_requestPermissions" | "wallet_getPermissions" => Some("permissions are per site and set in the wallet window"),
        _ => None,
    }
}

/// The sentences for a bridge transaction, the same as a send from the window.
pub fn transaction_sentences(tx: &Transaction, from: &Address, network: &str) -> Vec<String> {
    display::sentences(tx, from, network)
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn personal_sign_words() {
        let (bytes, ask) = parse_personal_sign(&json!(["0x68656c6c6f", "0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266"])).unwrap();
        assert_eq!(bytes, b"hello");
        assert_eq!(ask, Ask::SignMessage { text: Some("hello".into()), bytes: 5 });
        assert_eq!(ask.title("site.example"), "site.example asks you to sign a message");
        let (_, ask) = parse_personal_sign(&json!(["0x00ff", "0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266"])).unwrap();
        assert_eq!(ask, Ask::SignMessage { text: None, bytes: 2 });
        assert!(parse_personal_sign(&json!(["0x02f87b01", "0x"])).is_err());
        assert!(parse_personal_sign(&json!(["", "0x"])).is_err());
    }

    #[test]
    fn typed_data_bad_shapes_refused() {
        let from = Address::parse("0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266").unwrap();
        // the security review's regression seed, as the site would send it
        let seed: Value = serde_json::from_str(r#"["0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266", {"types": "A", "domain":  {"EIP712Domain": [], "A": [{"name": "b", "type": "B"}], "B": [{"name": "a", "type": "A"}]}, "primaryType": "A", "domain": {}, "message": {"b": {"a": {"b": {}}}}}]"#).unwrap();
        let e = parse_typed_data(&seed, &from, 7310).unwrap_err().to_string();
        assert!(e.contains("types must be an object"), "{e}");
        for p in [json!([from, "not json"]), json!([from, 5]), json!([from, []]), json!([from]), json!([]), json!("x"), json!([from, {"types": {}, "primaryType": "A", "domain": {}, "message": {}}])] {
            assert!(parse_typed_data(&p, &from, 7310).is_err(), "{p}");
        }
        for p in [json!([]), json!("x"), json!([5, 6]), json!([{"a": 1}])] {
            assert!(parse_personal_sign(&p).is_err() || parse_personal_sign(&p).is_ok(), "{p}");
        }
        for p in [json!([]), json!("x"), json!([5]), json!([{"from": 7}]), json!([{"to": "zz"}]), json!([{"value": "zz"}]), json!([{"data": "0x1"}]), json!([{"gas": true}])] {
            let _ = parse_send_params(&p, &from);
        }
    }

    #[test]
    fn typed_data_chain_check() {
        let from = Address::parse("0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266").unwrap();
        let typed = json!({"types": {"T": [{"name": "x", "type": "uint8"}]}, "primaryType": "T", "domain": {"name": "d", "chainId": 1}, "message": {"x": 1}});
        assert!(parse_typed_data(&json!([from, typed]), &from, 7310).is_err());
        let typed = json!({"types": {"T": [{"name": "x", "type": "uint8"}]}, "primaryType": "T", "domain": {"name": "d", "chainId": 7310}, "message": {"x": 1}});
        let (_, ask) = parse_typed_data(&json!([from, typed]), &from, 7310).unwrap();
        assert!(matches!(ask, Ask::SignTypedData { .. }));
        assert_eq!(ask.yes_word(), "Sign");
    }

    #[test]
    fn send_params_and_refusals() {
        let from = Address::parse("0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266").unwrap();
        let p = parse_send_params(&json!([{"from": from, "to": "0x70997970C51812dc3A010C7d01b50e0d17dc79C8", "value": "0xde0b6b3a7640000", "data": "0x", "gas": "0x5208"}]), &from).unwrap();
        assert!(p.to.is_some());
        assert_eq!(p.value.to_dec_string(), "1000000000000000000");
        assert!(p.data.is_empty());
        assert_eq!(p.gas, Some(21000));
        assert!(parse_send_params(&json!([{"from": "0x70997970C51812dc3A010C7d01b50e0d17dc79C8"}]), &from).is_err());
        assert_eq!(refused("eth_sign"), Some("this wallet never signs a bare hash"));
        assert!(is_read("eth_call"));
        assert!(!is_read("eth_sendTransaction"));
    }
}
