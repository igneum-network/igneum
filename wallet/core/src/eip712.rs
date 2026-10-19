//! EIP-712 typed data: the hash of a `TypedData` JSON as wallets receive it from `eth_signTypedData_v4`, and the
//! field-by-field rendering the window shows. Own encoder; the EIP's example is the test vector.

use serde_json::Value;

use crate::{hex, keccak::keccak256, sign::typed_data_hash, Address, Error, Result, U256};

/// The shape of typed data, checked before any hashing or rendering: every lookup below assumes it. No JSON shape
/// reaches a panic; a wrong one is refused in words (the security review's TP-03 finding of 10 October 2026: a
/// `types` that was a string reached an index assignment and killed the engine).
pub fn check_shape(typed: &Value) -> Result<()> {
    let obj = typed.as_object().ok_or_else(|| Error::Input("typed data must be an object".into()))?;
    let types = obj.get("types").ok_or_else(|| Error::Input("typed data has no types".into()))?;
    let types = types.as_object().ok_or_else(|| Error::Input("types must be an object of type arrays".into()))?;
    if types.len() > 256 {
        return Err(Error::Input("too many types".into()));
    }
    for (name, fields) in types {
        let arr = fields.as_array().ok_or_else(|| Error::Input(format!("type {name} must be an array of fields")))?;
        if arr.len() > 256 {
            return Err(Error::Input(format!("type {name} has too many fields")));
        }
        for f in arr {
            let fo = f.as_object().ok_or_else(|| Error::Input(format!("a field of {name} is not an object")))?;
            let fname = fo.get("name").and_then(|n| n.as_str()).ok_or_else(|| Error::Input(format!("a field of {name} has no name")))?;
            let fty = fo.get("type").and_then(|n| n.as_str()).ok_or_else(|| Error::Input(format!("field {fname} of {name} has no type")))?;
            if fname.is_empty() || fty.is_empty() || fty.len() > 128 {
                return Err(Error::Input(format!("field {fname} of {name} has a bad name or type")));
            }
        }
    }
    let primary = obj.get("primaryType").and_then(|p| p.as_str()).ok_or_else(|| Error::Input("typed data has no primaryType".into()))?;
    if !types.contains_key(primary) {
        return Err(Error::Input(format!("primaryType {primary} is not in types")));
    }
    if !obj.get("domain").map(|d| d.is_object()).unwrap_or(false) {
        return Err(Error::Input("domain must be an object".into()));
    }
    if !obj.get("message").map(|m| m.is_object()).unwrap_or(false) {
        return Err(Error::Input("message must be an object".into()));
    }
    // the type graph must be finite: a struct that reaches itself has no encodeType
    let mut stack = vec![primary.to_string()];
    let mut seen = Vec::new();
    while let Some(t) = stack.pop() {
        if seen.contains(&t) {
            continue;
        }
        seen.push(t.clone());
        for f in fields(obj.get("types").unwrap(), &t)? {
            let b = base_type(&f.ty).to_string();
            if types.contains_key(&b) {
                if reaches(types, &b, &t, &mut Vec::new()) {
                    return Err(Error::Input(format!("types {t} and {b} refer to each other; typed data must not be recursive")));
                }
                stack.push(b);
            }
        }
    }
    Ok(())
}

fn reaches(types: &serde_json::Map<String, Value>, from: &str, target: &str, path: &mut Vec<String>) -> bool {
    if from == target {
        return true;
    }
    if path.contains(&from.to_string()) || path.len() > 64 {
        return false;
    }
    path.push(from.to_string());
    if let Some(arr) = types.get(from).and_then(|a| a.as_array()) {
        for f in arr {
            if let Some(ty) = f.get("type").and_then(|t| t.as_str()) {
                let b = base_type(ty);
                if types.contains_key(b) && reaches(types, b, target, path) {
                    return true;
                }
            }
        }
    }
    false
}

/// A field of a struct type: `{ "name": ..., "type": ... }`.
#[derive(Clone, Debug)]
struct Field {
    name: String,
    ty: String,
}

fn fields(types: &Value, name: &str) -> Result<Vec<Field>> {
    let arr = types.get(name).and_then(|t| t.as_array()).ok_or_else(|| Error::Input(format!("unknown type {name}")))?;
    arr.iter()
        .map(|f| {
            Ok(Field {
                name: f.get("name").and_then(|n| n.as_str()).ok_or_else(|| Error::Input("field without a name".into()))?.to_string(),
                ty: f.get("type").and_then(|n| n.as_str()).ok_or_else(|| Error::Input("field without a type".into()))?.to_string(),
            })
        })
        .collect()
}

fn base_type(ty: &str) -> &str {
    match ty.find('[') {
        Some(i) => &ty[..i],
        None => ty,
    }
}

fn is_struct(types: &Value, ty: &str) -> bool {
    types.get(base_type(ty)).is_some()
}

/// The dependencies of a type, sorted, with the type itself first (the EIP's encodeType rule).
fn dependencies(types: &Value, primary: &str, out: &mut Vec<String>) -> Result<()> {
    if out.contains(&primary.to_string()) {
        return Ok(());
    }
    out.push(primary.to_string());
    for f in fields(types, primary)? {
        let b = base_type(&f.ty).to_string();
        if is_struct(types, &b) {
            dependencies(types, &b, out)?;
        }
    }
    Ok(())
}

pub fn encode_type(types: &Value, primary: &str) -> Result<String> {
    let mut deps = Vec::new();
    dependencies(types, primary, &mut deps)?;
    let first = deps.remove(0);
    deps.sort();
    let mut all = vec![first];
    all.extend(deps);
    let mut s = String::new();
    for name in all {
        s.push_str(&name);
        s.push('(');
        let fs = fields(types, &name)?;
        s.push_str(&fs.iter().map(|f| format!("{} {}", f.ty, f.name)).collect::<Vec<_>>().join(","));
        s.push(')');
    }
    Ok(s)
}

pub fn type_hash(types: &Value, primary: &str) -> Result<[u8; 32]> {
    Ok(keccak256(encode_type(types, primary)?.as_bytes()))
}

fn encode_value(types: &Value, ty: &str, v: &Value) -> Result<[u8; 32]> {
    let mut word = [0u8; 32];
    if let Some(i) = ty.find('[') {
        // an array: the hash of the concatenated encodings of its items
        let inner = &ty[..i];
        let rest = &ty[i + 1..];
        let inner_full = if let Some(j) = rest.find(']') { format!("{}{}", inner, &rest[j + 1..]) } else { inner.to_string() };
        let items = v.as_array().ok_or_else(|| Error::Input(format!("{ty}: not an array")))?;
        let mut buf = Vec::with_capacity(items.len() * 32);
        for it in items {
            buf.extend_from_slice(&encode_value(types, &inner_full, it)?);
        }
        return Ok(keccak256(&buf));
    }
    if is_struct(types, ty) {
        return hash_struct(types, ty, v);
    }
    match ty {
        "string" => {
            let s = v.as_str().ok_or_else(|| Error::Input("string field is not text".into()))?;
            return Ok(keccak256(s.as_bytes()));
        }
        "bytes" => {
            let b = hex::decode(v.as_str().unwrap_or(""))?;
            return Ok(keccak256(&b));
        }
        "address" => {
            let a = Address::parse(v.as_str().ok_or_else(|| Error::Input("address is not text".into()))?)?;
            word[12..].copy_from_slice(&a.0);
            return Ok(word);
        }
        "bool" => {
            let b = v.as_bool().ok_or_else(|| Error::Input("bool is not true or false".into()))?;
            word[31] = b as u8;
            return Ok(word);
        }
        _ => {}
    }
    if let Some(n) = ty.strip_prefix("bytes") {
        let len: usize = n.parse().map_err(|_| Error::Input(format!("bad type {ty}")))?;
        let b = hex::decode(v.as_str().unwrap_or(""))?;
        if b.len() != len {
            return Err(Error::Input(format!("{ty}: {} bytes given", b.len())));
        }
        word[..len].copy_from_slice(&b);
        return Ok(word);
    }
    if ty.starts_with("uint") || ty.starts_with("int") {
        let u = match v {
            Value::Number(n) => {
                if let Some(x) = n.as_u64() {
                    U256::from_u64(x)
                } else if let Some(x) = n.as_i64() {
                    if x < 0 {
                        return neg(x);
                    }
                    U256::from_u64(x as u64)
                } else {
                    return Err(Error::Input(format!("{ty}: not an integer")));
                }
            }
            Value::String(s) => {
                let t = s.trim();
                if let Some(rest) = t.strip_prefix('-') {
                    let mag = U256::from_dec_str(rest)?;
                    // two's complement: 2^256 - mag
                    let inv = U256(mag.0.map(|b| !b));
                    inv.checked_add(&U256::from_u64(1)).ok_or_else(|| Error::Input("int overflow".into()))?
                } else if t.starts_with("0x") || t.starts_with("0X") {
                    U256::from_hex(t)?
                } else {
                    U256::from_dec_str(t)?
                }
            }
            _ => return Err(Error::Input(format!("{ty}: not an integer"))),
        };
        return Ok(u.0);
    }
    Err(Error::Input(format!("unsupported type {ty}")))
}

fn neg(x: i64) -> Result<[u8; 32]> {
    let mag = U256::from_u64(x.unsigned_abs());
    let inv = U256(mag.0.map(|b| !b));
    Ok(inv.checked_add(&U256::from_u64(1)).ok_or_else(|| Error::Input("int overflow".into()))?.0)
}

pub fn hash_struct(types: &Value, ty: &str, v: &Value) -> Result<[u8; 32]> {
    let mut buf = Vec::new();
    buf.extend_from_slice(&type_hash(types, ty)?);
    for f in fields(types, ty)? {
        let fv = v.get(&f.name).ok_or_else(|| Error::Input(format!("missing field {}", f.name)))?;
        buf.extend_from_slice(&encode_value(types, &f.ty, fv)?);
    }
    Ok(keccak256(&buf))
}

/// The domain separator from the `domain` object, with the EIP712Domain type as given (or derived from the
/// fields present when the types block omits it).
pub fn domain_separator(typed: &Value) -> Result<[u8; 32]> {
    check_shape(typed)?;
    let domain = typed.get("domain").ok_or_else(|| Error::Input("no domain".into()))?;
    let mut types = typed.get("types").cloned().unwrap_or(Value::Object(Default::default()));
    if types.get("EIP712Domain").is_none() {
        let mut fs = Vec::new();
        for (name, ty) in [("name", "string"), ("version", "string"), ("chainId", "uint256"), ("verifyingContract", "address"), ("salt", "bytes32")] {
            if domain.get(name).is_some() {
                fs.push(serde_json::json!({"name": name, "type": ty}));
            }
        }
        // types is an object here (check_shape), so the insertion cannot panic
        if let Some(map) = types.as_object_mut() {
            map.insert("EIP712Domain".to_string(), Value::Array(fs));
        } else {
            return Err(Error::Input("types must be an object".into()));
        }
    }
    hash_struct(&types, "EIP712Domain", domain)
}

/// The digest to sign for `eth_signTypedData_v4`.
pub fn digest(typed: &Value) -> Result<[u8; 32]> {
    check_shape(typed)?;
    let types = typed.get("types").ok_or_else(|| Error::Input("no types".into()))?;
    let primary = typed.get("primaryType").and_then(|p| p.as_str()).ok_or_else(|| Error::Input("no primaryType".into()))?;
    let message = typed.get("message").ok_or_else(|| Error::Input("no message".into()))?;
    let ds = domain_separator(typed)?;
    let sh = hash_struct(types, primary, message)?;
    Ok(typed_data_hash(&ds, &sh))
}

/// The chain id the domain names, when it names one.
pub fn domain_chain_id(typed: &Value) -> Option<u64> {
    let c = typed.get("domain")?.as_object()?.get("chainId")?;
    match c {
        Value::Number(n) => n.as_u64(),
        Value::String(s) => U256::from_hex(s).ok().or_else(|| U256::from_dec_str(s).ok()).and_then(|u| u.to_u64()),
        _ => None,
    }
}

/// The lines the window shows: the domain first, then every field in order, nested structs indented.
pub fn render(typed: &Value) -> Result<Vec<String>> {
    check_shape(typed)?;
    let mut out = Vec::new();
    let domain = typed.get("domain").cloned().unwrap_or(Value::Null);
    let mut d = Vec::new();
    for k in ["name", "version", "chainId", "verifyingContract"] {
        if let Some(v) = domain.get(k) {
            d.push(format!("{k} {}", scalar(v)));
        }
    }
    out.push(format!("Domain: {}", if d.is_empty() { "none".to_string() } else { d.join(", ") }));
    let types = typed.get("types").ok_or_else(|| Error::Input("no types".into()))?;
    let primary = typed.get("primaryType").and_then(|p| p.as_str()).ok_or_else(|| Error::Input("no primaryType".into()))?;
    let message = typed.get("message").ok_or_else(|| Error::Input("no message".into()))?;
    out.push(format!("Type: {primary}"));
    render_struct(types, primary, message, 1, &mut out)?;
    Ok(out)
}

fn render_struct(types: &Value, ty: &str, v: &Value, depth: usize, out: &mut Vec<String>) -> Result<()> {
    if depth > 8 {
        out.push(format!("{}(deeper fields not shown)", "  ".repeat(depth)));
        return Ok(());
    }
    for f in fields(types, ty)? {
        let fv = v.get(&f.name).cloned().unwrap_or(Value::Null);
        let pad = "  ".repeat(depth);
        if is_struct(types, &f.ty) && !f.ty.contains('[') {
            out.push(format!("{pad}{} ({}):", f.name, f.ty));
            render_struct(types, &f.ty, &fv, depth + 1, out)?;
        } else if f.ty.contains('[') {
            let n = fv.as_array().map(|a| a.len()).unwrap_or(0);
            out.push(format!("{pad}{} ({}): {} items", f.name, f.ty, n));
            if let Some(items) = fv.as_array() {
                for (i, it) in items.iter().take(8).enumerate() {
                    if is_struct(types, base_type(&f.ty)) {
                        out.push(format!("{pad}  [{i}]:"));
                        render_struct(types, base_type(&f.ty), it, depth + 2, out)?;
                    } else {
                        out.push(format!("{pad}  [{i}] {}", scalar(it)));
                    }
                }
                if items.len() > 8 {
                    out.push(format!("{pad}  and {} more", items.len() - 8));
                }
            }
        } else {
            let shown = if f.ty.starts_with("uint") && (f.name == "value" || f.name == "amount") && is_max(&fv) {
                "UNLIMITED (the maximum)".to_string()
            } else {
                scalar(&fv)
            };
            out.push(format!("{pad}{} ({}): {}", f.name, f.ty, shown));
        }
    }
    Ok(())
}

fn is_max(v: &Value) -> bool {
    match v {
        Value::String(s) => {
            let t = s.trim();
            (t.starts_with("0x") && U256::from_hex(t).map(|u| u == U256::MAX).unwrap_or(false))
                || U256::from_dec_str(t).map(|u| u == U256::MAX).unwrap_or(false)
        }
        _ => false,
    }
}

fn scalar(v: &Value) -> String {
    match v {
        Value::String(s) => {
            if s.len() > 80 {
                format!("{}\u{2026} ({} characters)", &s[..40], s.len())
            } else {
                s.clone()
            }
        }
        Value::Null => "(none)".into(),
        other => other.to_string(),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    fn mail() -> Value {
        json!({
            "types": {
                "EIP712Domain": [
                    {"name": "name", "type": "string"},
                    {"name": "version", "type": "string"},
                    {"name": "chainId", "type": "uint256"},
                    {"name": "verifyingContract", "type": "address"}
                ],
                "Person": [{"name": "name", "type": "string"}, {"name": "wallet", "type": "address"}],
                "Mail": [{"name": "from", "type": "Person"}, {"name": "to", "type": "Person"}, {"name": "contents", "type": "string"}]
            },
            "primaryType": "Mail",
            "domain": {"name": "Ether Mail", "version": "1", "chainId": 1, "verifyingContract": "0xCcCCccccCCCCcCCCCCCcCcCccCcCCCcCcccccccC"},
            "message": {
                "from": {"name": "Cow", "wallet": "0xCD2a3d9F938E13CD947Ec05AbC7FE734Df8DD826"},
                "to": {"name": "Bob", "wallet": "0xbBbBBBBbbBBBbbbBbbBbbbbBBbBbbbbBbBbbBBbB"},
                "contents": "Hello, Bob!"
            }
        })
    }

    #[test]
    fn eip_example_vectors() {
        let t = mail();
        let types = t.get("types").unwrap();
        assert_eq!(encode_type(types, "Mail").unwrap(), "Mail(Person from,Person to,string contents)Person(string name,address wallet)");
        assert_eq!(
            hex::encode(&domain_separator(&t).unwrap()),
            "0xf2cee375fa42b42143804025fc449deafd50cc031ca257e0b194a650a912090f"
        );
        assert_eq!(
            hex::encode(&hash_struct(types, "Mail", t.get("message").unwrap()).unwrap()),
            "0xc52c0ee5d84264471806290a3f2c4cecfc5490626bf912d01f240d7a274b371e"
        );
        assert_eq!(
            hex::encode(&digest(&t).unwrap()),
            "0xbe609aee343fb3c4b28e1df9e632fca64fcfaede20f02e86244efddf30957bd2"
        );
        assert_eq!(domain_chain_id(&t), Some(1));
    }

    #[test]
    fn renders_fields_and_unlimited() {
        let t = mail();
        let lines = render(&t).unwrap();
        assert_eq!(lines[0], "Domain: name Ether Mail, version 1, chainId 1, verifyingContract 0xCcCCccccCCCCcCCCCCCcCcCccCcCCCcCcccccccC");
        assert_eq!(lines[1], "Type: Mail");
        assert_eq!(lines[2], "  from (Person):");
        assert_eq!(lines[3], "    name (string): Cow");
        assert!(lines.iter().any(|l| l == "  contents (string): Hello, Bob!"));
        let permit = json!({
            "types": {"Permit": [{"name": "spender", "type": "address"}, {"name": "value", "type": "uint256"}]},
            "primaryType": "Permit",
            "domain": {"name": "Token", "chainId": 7310},
            "message": {"spender": "0xbBbBBBBbbBBBbbbBbbBbbbbBBbBbbbbBbBbbBBbB", "value": "115792089237316195423570985008687907853269984665640564039457584007913129639935"}
        });
        let lines = render(&permit).unwrap();
        assert!(lines.iter().any(|l| l.contains("UNLIMITED")));
        digest(&permit).unwrap();
    }

    #[test]
    fn bad_shapes_are_refused_in_words() {
        // the security review's regression seed: types is a string (this killed the engine before the shape check)
        let seed: Value = serde_json::from_str(r#"{"types": "A", "primaryType": "A", "domain": {}, "message": {"b": {"a": {"b": {}}}}}"#).unwrap();
        for f in [digest, |t: &Value| render(t).map(|_| [0u8; 32]), domain_separator] {
            let e = f(&seed).unwrap_err().to_string();
            assert!(e.contains("types must be an object"), "{e}");
        }
        let cases: Vec<(Value, &str)> = vec![
            (json!("not an object"), "typed data must be an object"),
            (json!({"primaryType": "A", "domain": {}, "message": {}}), "typed data has no types"),
            (json!({"types": [], "primaryType": "A", "domain": {}, "message": {}}), "types must be an object"),
            (json!({"types": {"A": "x"}, "primaryType": "A", "domain": {}, "message": {}}), "type A must be an array of fields"),
            (json!({"types": {"A": [1]}, "primaryType": "A", "domain": {}, "message": {}}), "a field of A is not an object"),
            (json!({"types": {"A": [{"type": "uint8"}]}, "primaryType": "A", "domain": {}, "message": {}}), "a field of A has no name"),
            (json!({"types": {"A": [{"name": "x"}]}, "primaryType": "A", "domain": {}, "message": {}}), "field x of A has no type"),
            (json!({"types": {"A": []}, "domain": {}, "message": {}}), "typed data has no primaryType"),
            (json!({"types": {"A": []}, "primaryType": "B", "domain": {}, "message": {}}), "primaryType B is not in types"),
            (json!({"types": {"A": []}, "primaryType": "A", "domain": "d", "message": {}}), "domain must be an object"),
            (json!({"types": {"A": []}, "primaryType": "A", "domain": {}, "message": []}), "message must be an object"),
            (json!({"types": {"A": [{"name": "b", "type": "B"}], "B": [{"name": "a", "type": "A"}]}, "primaryType": "A", "domain": {}, "message": {}}), "refer to each other"),
            (json!({"types": {"A": [{"name": "a", "type": "A"}]}, "primaryType": "A", "domain": {}, "message": {}}), "refer to each other"),
        ];
        for (v, words) in cases {
            let e = digest(&v).unwrap_err().to_string();
            assert!(e.contains(words), "{v}: {e}");
            assert!(render(&v).is_err());
        }
        // a missing message field, a wrong scalar, an unknown type: refused, never a panic
        let t = json!({"types": {"A": [{"name": "x", "type": "uint8"}, {"name": "y", "type": "nope"}]}, "primaryType": "A", "domain": {"name": "d"}, "message": {"x": "abc"}});
        assert!(digest(&t).is_err());
        let t = json!({"types": {"A": [{"name": "x", "type": "uint8"}]}, "primaryType": "A", "domain": {"name": "d", "chainId": {"a": 1}}, "message": {}});
        assert!(digest(&t).is_err());
        assert_eq!(domain_chain_id(&t), None);
    }

    #[test]
    fn arrays_and_negative_ints() {
        let t = json!({
            "types": {"Batch": [{"name": "ids", "type": "uint256[]"}, {"name": "delta", "type": "int8"}, {"name": "tag", "type": "bytes4"}]},
            "primaryType": "Batch",
            "domain": {"name": "x"},
            "message": {"ids": [1, 2, 3], "delta": -1, "tag": "0x01020304"}
        });
        let d = digest(&t).unwrap();
        assert_ne!(d, [0u8; 32]);
        let types = t.get("types").unwrap();
        let neg_word = encode_value(types, "int8", &json!(-1)).unwrap();
        assert_eq!(neg_word, [0xffu8; 32]);
        let lines = render(&t).unwrap();
        assert!(lines.iter().any(|l| l.contains("ids (uint256[]): 3 items")));
    }
}
