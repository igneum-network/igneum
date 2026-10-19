//! The supervisor's network layer: a plain or TLS stream with timeouts, a line reader bounded at the pool's frame
//! limit, and a minimal HTTP/1.1 client for the node's JSON-RPC and the pool's disclosure document. Workers never
//! touch any of this.

/// How the miner names itself to a pool or a node: the product and its version (the register's F-16 names the
/// client as the user agent presents it).
pub const CLIENT_ID: &str = concat!("Ember/", env!("CARGO_PKG_VERSION"));

use std::sync::Mutex;
use std::io::{self, BufRead, BufReader, Read, Write};
use std::net::{TcpStream, ToSocketAddrs};
use std::sync::Arc;
use std::time::Duration;

/// The pool's frame bound (docs/ecosystem/DESIGN.md 2.5): 16 KiB per line.
pub const MAX_FRAME_BYTES: usize = 16 * 1024;
pub const CONNECT_TIMEOUT: Duration = Duration::from_secs(10);
pub const MAX_HTTP_BODY: usize = 4 << 20;

pub enum Stream {
    Plain(TcpStream),
    Tls(Box<rustls::StreamOwned<rustls::ClientConnection, TcpStream>>),
}

impl Read for Stream {
    fn read(&mut self, buf: &mut [u8]) -> io::Result<usize> {
        match self {
            Stream::Plain(s) => s.read(buf),
            Stream::Tls(s) => s.read(buf),
        }
    }
}

impl Write for Stream {
    fn write(&mut self, buf: &[u8]) -> io::Result<usize> {
        match self {
            Stream::Plain(s) => s.write(buf),
            Stream::Tls(s) => s.write(buf),
        }
    }
    fn flush(&mut self) -> io::Result<()> {
        match self {
            Stream::Plain(s) => s.flush(),
            Stream::Tls(s) => s.flush(),
        }
    }
}

impl Stream {
    pub fn set_read_timeout(&self, d: Option<Duration>) -> io::Result<()> {
        match self {
            Stream::Plain(s) => s.set_read_timeout(d),
            Stream::Tls(s) => s.sock.set_read_timeout(d),
        }
    }
    pub fn try_clone_tcp(&self) -> io::Result<TcpStream> {
        match self {
            Stream::Plain(s) => s.try_clone(),
            Stream::Tls(s) => s.sock.try_clone(),
        }
    }
    pub fn shutdown(&self) {
        let _ = match self {
            Stream::Plain(s) => s.shutdown(std::net::Shutdown::Both),
            Stream::Tls(s) => s.sock.shutdown(std::net::Shutdown::Both),
        };
    }
}

fn tls_config() -> Arc<rustls::ClientConfig> {
    let mut roots = rustls::RootCertStore::empty();
    roots.extend(webpki_roots::TLS_SERVER_ROOTS.iter().cloned());
    let cfg = rustls::ClientConfig::builder_with_provider(Arc::new(rustls::crypto::ring::default_provider()))
        .with_safe_default_protocol_versions()
        .expect("tls versions")
        .with_root_certificates(roots)
        .with_no_client_auth();
    Arc::new(cfg)
}

/// Connect with a timeout; with `tls`, a verified TLS session against the public roots (no self-signed pools).
pub fn connect(host: &str, port: u16, tls: bool, read_timeout: Option<Duration>) -> Result<Stream, String> {
    let addrs: Vec<_> = (host, port).to_socket_addrs().map_err(|e| format!("{host}:{port}: {e}"))?.collect();
    let mut last = String::from("no address");
    for a in addrs {
        match TcpStream::connect_timeout(&a, CONNECT_TIMEOUT) {
            Ok(tcp) => {
                let _ = tcp.set_nodelay(true);
                let _ = tcp.set_read_timeout(read_timeout);
                let _ = tcp.set_write_timeout(Some(Duration::from_secs(20)));
                if !tls {
                    return Ok(Stream::Plain(tcp));
                }
                let name = rustls::pki_types::ServerName::try_from(host.to_string()).map_err(|_| format!("{host}: not a valid server name for TLS"))?;
                let conn = rustls::ClientConnection::new(tls_config(), name).map_err(|e| format!("tls: {e}"))?;
                return Ok(Stream::Tls(Box::new(rustls::StreamOwned::new(conn, tcp))));
            }
            Err(e) => last = e.to_string(),
        }
    }
    Err(format!("{host}:{port}: {last}"))
}

/// Reads newline-delimited frames, refusing one over the bound before it is buffered further.
pub struct Lines<R: Read> {
    r: BufReader<R>,
    buf: Vec<u8>,
}

impl<R: Read> Lines<R> {
    pub fn new(r: R) -> Self {
        Lines { r: BufReader::new(r), buf: Vec::with_capacity(1024) }
    }
    /// The underlying reader, for a caller that writes on the same stream between frames.
    pub fn inner_mut(&mut self) -> &mut R {
        self.r.get_mut()
    }
    /// `Ok(None)` at end of stream; an error for a timeout, a closed socket or an oversized frame.
    pub fn next_line(&mut self) -> io::Result<Option<String>> {
        self.buf.clear();
        let n = (&mut self.r).take(MAX_FRAME_BYTES as u64 + 1).read_until(b'\n', &mut self.buf)?;
        if n == 0 {
            return Ok(None);
        }
        if self.buf.len() > MAX_FRAME_BYTES {
            return Err(io::Error::new(io::ErrorKind::InvalidData, "frame over the bound"));
        }
        Ok(Some(String::from_utf8_lossy(&self.buf).trim_end_matches(['\n', '\r']).to_string()))
    }
}

/// A parsed URL: scheme, host, port, path.
#[derive(Debug, Clone, PartialEq)]
pub struct Url {
    pub tls: bool,
    pub host: String,
    pub port: u16,
    pub path: String,
}

pub fn parse_url(u: &str) -> Result<Url, String> {
    let (tls, rest) = if let Some(r) = u.strip_prefix("https://") {
        (true, r)
    } else if let Some(r) = u.strip_prefix("http://") {
        (false, r)
    } else {
        return Err("the address starts with http:// or https://".into());
    };
    let (hostport, path) = match rest.find('/') {
        Some(i) => (&rest[..i], &rest[i..]),
        None => (rest, "/"),
    };
    let hostport = hostport.rsplit('@').next().unwrap_or(hostport);
    let (host, port) = if let Some(h) = hostport.strip_prefix('[') {
        let end = h.find(']').ok_or("bad IPv6 host")?;
        let port = h[end + 1..].strip_prefix(':').map(|p| p.parse::<u16>().map_err(|_| "bad port".to_string())).transpose()?;
        (h[..end].to_string(), port)
    } else {
        match hostport.rsplit_once(':') {
            Some((h, p)) => (h.to_string(), Some(p.parse::<u16>().map_err(|_| "bad port".to_string())?)),
            None => (hostport.to_string(), None),
        }
    };
    if host.is_empty() {
        return Err("no host".into());
    }
    Ok(Url { tls, host, port: port.unwrap_or(if tls { 443 } else { 80 }), path: path.to_string() })
}

/// One HTTP/1.1 request on a fresh connection; the body up to `MAX_HTTP_BODY`. Returns (status, body).
pub fn http(method: &str, url: &Url, content_type: Option<&str>, body: &[u8], timeout: Duration) -> Result<(u16, Vec<u8>), String> {
    let mut s = connect(&url.host, url.port, url.tls, Some(timeout))?;
    let mut req = format!("{method} {} HTTP/1.1\r\nHost: {}\r\nUser-Agent: {}\r\nAccept: application/json\r\nConnection: close\r\n", url.path, url.host, CLIENT_ID);
    if let Some(ct) = content_type {
        req.push_str(&format!("Content-Type: {ct}\r\n"));
    }
    req.push_str(&format!("Content-Length: {}\r\n\r\n", body.len()));
    s.write_all(req.as_bytes()).and_then(|_| s.write_all(body)).and_then(|_| s.flush()).map_err(|e| format!("send: {e}"))?;
    let mut r = BufReader::new(s);
    let mut line = String::new();
    r.read_line(&mut line).map_err(|e| format!("response: {e}"))?;
    let status: u16 = line.split_whitespace().nth(1).and_then(|c| c.parse().ok()).ok_or_else(|| format!("not an HTTP response: {}", line.trim()))?;
    let mut content_length: Option<usize> = None;
    let mut chunked = false;
    loop {
        line.clear();
        r.read_line(&mut line).map_err(|e| format!("headers: {e}"))?;
        let l = line.trim_end();
        if l.is_empty() {
            break;
        }
        if let Some((k, v)) = l.split_once(':') {
            let k = k.trim().to_ascii_lowercase();
            let v = v.trim();
            if k == "content-length" {
                content_length = v.parse().ok();
            } else if k == "transfer-encoding" && v.to_ascii_lowercase().contains("chunked") {
                chunked = true;
            }
        }
    }
    let mut out = Vec::new();
    if chunked {
        loop {
            line.clear();
            r.read_line(&mut line).map_err(|e| format!("chunk: {e}"))?;
            let size = usize::from_str_radix(line.trim().split(';').next().unwrap_or("0"), 16).map_err(|_| "bad chunk size")?;
            if size == 0 {
                break;
            }
            if out.len() + size > MAX_HTTP_BODY {
                return Err("response over the size bound".into());
            }
            let mut chunk = vec![0u8; size];
            r.read_exact(&mut chunk).map_err(|e| format!("chunk: {e}"))?;
            out.extend_from_slice(&chunk);
            line.clear();
            let _ = r.read_line(&mut line);
        }
    } else if let Some(n) = content_length {
        if n > MAX_HTTP_BODY {
            return Err("response over the size bound".into());
        }
        out.resize(n, 0);
        r.read_exact(&mut out).map_err(|e| format!("body: {e}"))?;
    } else {
        (&mut r).take(MAX_HTTP_BODY as u64 + 1).read_to_end(&mut out).map_err(|e| format!("body: {e}"))?;
        if out.len() > MAX_HTTP_BODY {
            return Err("response over the size bound".into());
        }
    }
    Ok((status, out))
}

/// A keep-alive POST (the chain scene's header every second, 13 October 2026: a fresh TLS dial each second put Ember
/// one second behind the site at the seal): one connection per host kept open in a pool, HTTP/1.1 with
/// Connection: keep-alive, the response's headers returned beside its body; a connection that fails is dropped and
/// the call dialled once more on a new one. The body bound is MAX_HTTP_BODY.
/// F-E68 (15 October 2026): one open connection per host, up to KEEPALIVE_HOSTS of them (the chain's RPC host and
/// the feed host alternate every second; one slot for both dialled anew on every call)
pub const KEEPALIVE_HOSTS: usize = 4;
static KEEPALIVE: Mutex<Vec<(String, BufReader<Stream>)>> = Mutex::new(Vec::new());
pub fn http_keepalive(method: &str, url: &Url, content_type: Option<&str>, body: &[u8], timeout: Duration) -> Result<(u16, Vec<(String, String)>, Vec<u8>), String> {
    http_keepalive_with(method, url, content_type, body, &[], timeout)
}

/// `http_keepalive` with extra request headers (0.2.8, the Fleet proxy's X-Fleet-Token); a header name or value
/// carrying a line break is refused.
pub fn http_keepalive_with(method: &str, url: &Url, content_type: Option<&str>, body: &[u8], extra: &[(&str, &str)], timeout: Duration) -> Result<(u16, Vec<(String, String)>, Vec<u8>), String> {
    for (k, v) in extra {
        if k.contains(['\r', '\n', ':']) || v.contains(['\r', '\n']) {
            return Err("a request header carries a line break".into());
        }
    }
    let key = format!("{}:{}:{}", url.host, url.port, url.tls);
    let mut pool = KEEPALIVE.lock().map_err(|_| "the keep-alive pool is poisoned".to_string())?;
    for attempt in 0..2 {
        let found = pool.iter().position(|(k, _)| *k == key);
        let reuse = found.is_some();
        let i = match found {
            Some(i) => i,
            None => {
                let s = connect(&url.host, url.port, url.tls, Some(timeout))?;
                if pool.len() >= KEEPALIVE_HOSTS {
                    pool.remove(0);
                }
                pool.push((key.clone(), BufReader::new(s)));
                pool.len() - 1
            }
        };
        let (_, r) = &mut pool[i];
        match keepalive_round(r, method, url, content_type, body, extra) {
            Ok(v) => return Ok(v),
            Err(e) => {
                pool.remove(i);
                if attempt == 1 || !reuse {
                    return Err(e);
                }
            }
        }
    }
    Err("the keep-alive call did not complete".into())
}
fn keepalive_round(r: &mut BufReader<Stream>, method: &str, url: &Url, content_type: Option<&str>, body: &[u8], extra: &[(&str, &str)]) -> Result<(u16, Vec<(String, String)>, Vec<u8>), String> {
    let mut req = format!("{method} {} HTTP/1.1\r\nHost: {}\r\nUser-Agent: {}\r\nAccept: */*\r\nConnection: keep-alive\r\n", url.path, url.host, CLIENT_ID);
    for (k, v) in extra {
        req.push_str(&format!("{k}: {v}\r\n"));
    }
    if let Some(ct) = content_type {
        req.push_str(&format!("Content-Type: {ct}\r\n"));
    }
    req.push_str(&format!("Content-Length: {}\r\n\r\n", body.len()));
    {
        let s = r.get_mut();
        s.write_all(req.as_bytes()).and_then(|_| s.write_all(body)).and_then(|_| s.flush()).map_err(|e| format!("send: {e}"))?;
    }
    let mut line = String::new();
    r.read_line(&mut line).map_err(|e| format!("response: {e}"))?;
    let status: u16 = line.split_whitespace().nth(1).and_then(|c| c.parse().ok()).ok_or_else(|| format!("not an HTTP response: {}", line.trim()))?;
    let mut content_length: Option<usize> = None;
    let mut chunked = false;
    let mut close = false;
    let mut headers = Vec::new();
    loop {
        line.clear();
        r.read_line(&mut line).map_err(|e| format!("headers: {e}"))?;
        let l = line.trim_end();
        if l.is_empty() {
            break;
        }
        if let Some((k, v)) = l.split_once(':') {
            let k = k.trim().to_ascii_lowercase();
            let v = v.trim().to_string();
            if k == "content-length" {
                content_length = v.parse().ok();
            } else if k == "transfer-encoding" && v.to_ascii_lowercase().contains("chunked") {
                chunked = true;
            } else if k == "connection" && v.to_ascii_lowercase().contains("close") {
                close = true;
            }
            headers.push((k, v));
        }
    }
    let mut out = Vec::new();
    if chunked {
        loop {
            line.clear();
            r.read_line(&mut line).map_err(|e| format!("chunk: {e}"))?;
            let size = usize::from_str_radix(line.trim().split(';').next().unwrap_or("0"), 16).map_err(|_| "bad chunk size")?;
            if size == 0 {
                line.clear();
                let _ = r.read_line(&mut line);
                break;
            }
            if out.len() + size > MAX_HTTP_BODY {
                return Err("response over the size bound".into());
            }
            let mut chunk = vec![0u8; size];
            r.read_exact(&mut chunk).map_err(|e| format!("chunk: {e}"))?;
            out.extend_from_slice(&chunk);
            line.clear();
            let _ = r.read_line(&mut line);
        }
    } else if let Some(n) = content_length {
        if n > MAX_HTTP_BODY {
            return Err("response over the size bound".into());
        }
        out.resize(n, 0);
        r.read_exact(&mut out).map_err(|e| format!("body: {e}"))?;
    } else {
        return Err("no length on a keep-alive response".into());
    }
    // a host that closes after this answer is answered now and dialled afresh next time (the pool entry is dropped by the caller on the next failure)
    let _ = close;
    Ok((status, headers, out))
}

/// A JSON-RPC 2.0 call; the raw `result` text (so big integers survive) or the error message.
pub fn json_rpc(url: &Url, id: u64, method: &str, params: &serde_json::Value, timeout: Duration) -> Result<String, String> {
    let body = serde_json::json!({"jsonrpc": "2.0", "id": id, "method": method, "params": params}).to_string();
    let (status, resp) = http("POST", url, Some("application/json"), body.as_bytes(), timeout)?;
    if status != 200 {
        return Err(format!("{method}: HTTP {status}"));
    }
    let text = String::from_utf8_lossy(&resp);
    let v: serde_json::Value = serde_json::from_str(&text).map_err(|e| format!("{method}: not JSON: {e}"))?;
    if let Some(e) = v.get("error") {
        let msg = e.get("message").and_then(|m| m.as_str()).unwrap_or("error");
        return Err(format!("{method}: {msg}"));
    }
    // the raw result text: serde_json's Value loses big integers, so cut the result out of the body as written
    raw_field(&text, "result").ok_or_else(|| format!("{method}: no result"))
}

/// The raw text of a top-level JSON field's value (an object, array, string or number), as written.
pub fn raw_field(text: &str, key: &str) -> Option<String> {
    let needle = format!("\"{key}\"");
    let mut from = 0;
    while let Some(i) = text[from..].find(&needle) {
        let after = from + i + needle.len();
        let rest = text[after..].trim_start();
        if let Some(rest) = rest.strip_prefix(':') {
            let rest = rest.trim_start();
            let b = rest.as_bytes();
            let end = match b.first()? {
                b'{' | b'[' => {
                    let (open, close) = if b[0] == b'{' { (b'{', b'}') } else { (b'[', b']') };
                    let mut depth = 0usize;
                    let mut in_str = false;
                    let mut esc = false;
                    let mut e = None;
                    for (k, c) in b.iter().enumerate() {
                        if in_str {
                            if esc {
                                esc = false;
                            } else if *c == b'\\' {
                                esc = true;
                            } else if *c == b'"' {
                                in_str = false;
                            }
                            continue;
                        }
                        if *c == b'"' {
                            in_str = true;
                        } else if *c == open {
                            depth += 1;
                        } else if *c == close {
                            depth -= 1;
                            if depth == 0 {
                                e = Some(k + 1);
                                break;
                            }
                        }
                    }
                    e?
                }
                b'"' => {
                    let mut esc = false;
                    let mut e = None;
                    for (k, c) in b.iter().enumerate().skip(1) {
                        if esc {
                            esc = false;
                        } else if *c == b'\\' {
                            esc = true;
                        } else if *c == b'"' {
                            e = Some(k + 1);
                            break;
                        }
                    }
                    e?
                }
                _ => b.iter().position(|c| matches!(c, b',' | b'}' | b']' | b' ' | b'\n' | b'\r' | b'\t')).unwrap_or(b.len()),
            };
            return Some(rest[..end].to_string());
        }
        from = after;
    }
    None
}

/// A decimal integer (as text) to a big-endian 256-bit value; `None` if it does not fit or is not decimal.
pub fn decimal_to_u256(s: &str) -> Option<[u8; 32]> {
    let s = s.trim().trim_matches('"');
    if s.is_empty() || !s.bytes().all(|b| b.is_ascii_digit()) {
        return None;
    }
    let mut limbs = [0u32; 8]; // little-endian 32-bit limbs
    for d in s.bytes() {
        let mut carry = (d - b'0') as u64;
        for l in limbs.iter_mut() {
            let v = (*l as u64) * 10 + carry;
            *l = v as u32;
            carry = v >> 32;
        }
        if carry != 0 {
            return None;
        }
    }
    let mut out = [0u8; 32];
    for (i, l) in limbs.iter().enumerate() {
        out[32 - 4 * (i + 1)..32 - 4 * i].copy_from_slice(&l.to_be_bytes());
    }
    Some(out)
}

/// `0x`-prefixed hex of any length up to 32 bytes, left-padded.
pub fn hex0x_to_u256(s: &str) -> Option<[u8; 32]> {
    let h = s.trim().trim_matches('"').strip_prefix("0x")?;
    let h = if h.len() % 2 == 1 { format!("0{h}") } else { h.to_string() };
    let v = igneum_worker_proto::hex_decode(&h)?;
    if v.len() > 32 {
        return None;
    }
    let mut out = [0u8; 32];
    out[32 - v.len()..].copy_from_slice(&v);
    Some(out)
}

/// A JSON quantity in wei (a hex quantity `0x..`, a decimal string, or a number) as IGN (18 decimals), for the
/// display only: f64 precision is what a money line needs, never what consensus needs.
pub fn wei_value_to_ign(v: &serde_json::Value) -> Option<f64> {
    let bytes = match v {
        serde_json::Value::String(s) => {
            if s.starts_with("0x") || s.starts_with("0X") {
                hex0x_to_u256(&s.to_ascii_lowercase())?
            } else {
                decimal_to_u256(s)?
            }
        }
        serde_json::Value::Number(n) => {
            if let Some(u) = n.as_u64() {
                let mut a = [0u8; 32];
                a[24..].copy_from_slice(&u.to_be_bytes());
                a
            } else {
                return n.as_f64().map(|f| f / 1e18);
            }
        }
        _ => return None,
    };
    let mut acc = 0f64;
    for b in bytes {
        acc = acc * 256.0 + b as f64;
    }
    Some(acc / 1e18)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn wei_values() {
        let one = serde_json::json!("0xde0b6b3a7640000");
        assert!((wei_value_to_ign(&one).unwrap() - 1.0).abs() < 1e-12);
        let two = serde_json::json!("2000000000000000000");
        assert!((wei_value_to_ign(&two).unwrap() - 2.0).abs() < 1e-12);
        let small = serde_json::json!(1500000000000000000u64);
        assert!((wei_value_to_ign(&small).unwrap() - 1.5).abs() < 1e-12);
        assert_eq!(wei_value_to_ign(&serde_json::json!(null)), None);
    }

    #[test]
    fn urls() {
        let u = parse_url("http://127.0.0.1:8545").unwrap();
        assert_eq!(u, Url { tls: false, host: "127.0.0.1".into(), port: 8545, path: "/".into() });
        let u = parse_url("https://node.example/rpc/v1").unwrap();
        assert_eq!((u.tls, u.port, u.path.as_str()), (true, 443, "/rpc/v1"));
        assert!(parse_url("ftp://x").is_err());
        assert_eq!(parse_url("http://[::1]:8545/").unwrap().host, "::1");
    }

    #[test]
    fn raw_fields_and_big_integers() {
        let body = r#"{"jsonrpc":"2.0","id":1,"result":{"algorithm":"null","headerHash":"0xab","height":7,"target":115792089237316195423570985008687907853269984665640564039457584007913129639935}}"#;
        let r = raw_field(body, "result").unwrap();
        assert!(r.starts_with('{') && r.ends_with('}'));
        let t = raw_field(&r, "target").unwrap();
        assert_eq!(decimal_to_u256(&t), Some([0xff; 32]));
        assert_eq!(raw_field(&r, "headerHash").as_deref(), Some("\"0xab\""));
        assert_eq!(raw_field(&r, "height").as_deref(), Some("7"));
        assert_eq!(decimal_to_u256("256"), Some({ let mut a = [0u8; 32]; a[30] = 1; a }));
        assert_eq!(decimal_to_u256("115792089237316195423570985008687907853269984665640564039457584007913129639936"), None);
        assert_eq!(hex0x_to_u256("0x100"), Some({ let mut a = [0u8; 32]; a[30] = 1; a }));
        assert_eq!(raw_field(r#"{"result":["0xaa","0xbb"],"error":null}"#, "result").as_deref(), Some(r#"["0xaa","0xbb"]"#));
    }

    #[test]
    fn lines_refuse_an_oversized_frame() {
        let data = vec![b'x'; MAX_FRAME_BYTES + 10];
        let mut l = Lines::new(&data[..]);
        assert!(l.next_line().is_err());
        let mut l2 = Lines::new(&b"{\"a\":1}\r\nnext\n"[..]);
        assert_eq!(l2.next_line().unwrap().as_deref(), Some("{\"a\":1}"));
        assert_eq!(l2.next_line().unwrap().as_deref(), Some("next"));
        assert_eq!(l2.next_line().unwrap(), None);
    }
}
