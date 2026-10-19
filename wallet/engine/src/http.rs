//! A small HTTP/1.1 server on 127.0.0.1: one thread per connection, JSON in and out, static files for the page,
//! and, for the page bridge, CORS answers to a website's origin. No TLS, no keep-alive, no bodies above 1 MiB.
//! Only the loopback interface is ever bound.

use std::io::{Read, Write};
use std::net::{TcpListener, TcpStream};
use std::sync::Arc;

/// The window's policy (G14): its own files, its own engine, no frames in or out, no objects, no other base.
pub const CSP: &str = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self'; connect-src 'self'; frame-src 'none'; frame-ancestors 'none'; object-src 'none'; base-uri 'self'; form-action 'self'";

pub struct Request {
    pub method: String,
    pub path: String,
    pub body: Vec<u8>,
    /// lower-cased header names
    pub headers: Vec<(String, String)>,
}

impl Drop for Request {
    /// The body carries the password on unlock, create, import and reveal (wallet-security's G2): its bytes are
    /// zeroed when the request drops, so no copy of the password outlives the turn in this process.
    fn drop(&mut self) {
        use zeroize::Zeroize;
        self.body.zeroize();
    }
}

impl Drop for Response {
    /// The body may carry a reveal's words or key: zeroed when the response drops, after it was written.
    fn drop(&mut self) {
        use zeroize::Zeroize;
        self.body.zeroize();
    }
}

impl Request {
    pub fn header(&self, name: &str) -> Option<&str> {
        self.headers.iter().find(|(k, _)| k == name).map(|(_, v)| v.as_str())
    }
}

pub struct Response {
    pub status: u16,
    pub content_type: &'static str,
    pub body: Vec<u8>,
    /// when set, the response carries CORS headers for exactly this origin
    pub cors_origin: Option<String>,
    /// when set, the response carries the site's minted token (X-Igneum-Site; the bridge's approved connect)
    pub site_token: Option<String>,
}

/// A JSON value serialized into a buffer allocated once at its exact size (wallet-security's MEM-3): a growing
/// String leaves every earlier, smaller copy of a reveal's words or a password in freed memory as it reallocates;
/// counting first and writing once leaves none.
pub fn json_exact(v: &serde_json::Value) -> Vec<u8> {
    struct Count(usize);
    impl Write for Count {
        fn write(&mut self, b: &[u8]) -> std::io::Result<usize> {
            self.0 += b.len();
            Ok(b.len())
        }
        fn flush(&mut self) -> std::io::Result<()> {
            Ok(())
        }
    }
    let mut c = Count(0);
    let _ = serde_json::to_writer(&mut c, v);
    let mut out = Vec::with_capacity(c.0);
    let _ = serde_json::to_writer(&mut out, v);
    out
}

impl Response {
    pub fn json(status: u16, v: &serde_json::Value) -> Self {
        Response { status, content_type: "application/json", body: json_exact(v), cors_origin: None, site_token: None }
    }

    pub fn file(content_type: &'static str, body: Vec<u8>) -> Self {
        Response { status: 200, content_type, body, cors_origin: None, site_token: None }
    }

    pub fn not_found() -> Self {
        Response::json(404, &serde_json::json!({"error": "not found"}))
    }

    pub fn with_cors(mut self, origin: &str) -> Self {
        self.cors_origin = Some(origin.to_string());
        self
    }
}

fn reason(status: u16) -> &'static str {
    match status {
        200 => "OK",
        204 => "No Content",
        400 => "Bad Request",
        403 => "Forbidden",
        404 => "Not Found",
        409 => "Conflict",
        500 => "Internal Server Error",
        _ => "Status",
    }
}

fn handle_conn(mut stream: TcpStream, handler: &dyn Fn(Request) -> Response) {
    let _ = stream.set_read_timeout(Some(std::time::Duration::from_secs(10)));
    // the head is read a byte at a time straight from the socket (wallet-security's MEM-3): a buffered reader
    // would pull the body's first bytes, the password among them, into its own buffer, which is freed unzeroed;
    // this way the body's only copy in this process is the request's, zeroed when it drops
    let mut head: Vec<u8> = Vec::with_capacity(1024);
    let mut byte = [0u8; 1];
    loop {
        match stream.read(&mut byte) {
            Ok(1) => head.push(byte[0]),
            _ => return,
        }
        if head.ends_with(b"\r\n\r\n") || head.ends_with(b"\n\n") {
            break;
        }
        if head.len() > 16 * 1024 {
            return;
        }
    }
    let head = String::from_utf8_lossy(&head).into_owned();
    let mut lines = head.lines();
    let line = lines.next().unwrap_or("");
    let mut parts = line.split_whitespace();
    let method = parts.next().unwrap_or("").to_string();
    let path = parts.next().unwrap_or("/").to_string();
    if method.is_empty() {
        return;
    }
    let mut content_length = 0usize;
    let mut headers = Vec::new();
    for h in lines {
        let t = h.trim_end();
        if t.is_empty() {
            break;
        }
        if let Some((k, v)) = t.split_once(':') {
            let key = k.trim().to_ascii_lowercase();
            let val = v.trim().to_string();
            if key == "content-length" {
                content_length = val.parse().unwrap_or(0);
            }
            headers.push((key, val));
        }
    }
    if content_length > 1 << 20 {
        let _ = write_response(&mut stream, Response::json(400, &serde_json::json!({"error": "body too large"})));
        return;
    }
    let mut body = vec![0u8; content_length];
    if content_length > 0 && stream.read_exact(&mut body).is_err() {
        return;
    }
    // a bridge request may wait for the person's answer: no read timeout applies to the handler
    let resp = handler(Request { method, path, body, headers });
    let _ = write_response(&mut stream, resp);
}

fn write_response(stream: &mut TcpStream, r: Response) -> std::io::Result<()> {
    // the content security policy as a header on every answer (wallet-security's G14): the page's meta says the
    // same, but a header holds before the first byte is parsed and can name frame-ancestors, which a meta cannot;
    // the window loads only its own files and talks only to this engine
    let mut head = format!(
        "HTTP/1.1 {} {}\r\nContent-Type: {}\r\nContent-Length: {}\r\nCache-Control: no-store\r\nX-Content-Type-Options: nosniff\r\nContent-Security-Policy: {}\r\nReferrer-Policy: no-referrer\r\nConnection: close\r\n",
        r.status,
        reason(r.status),
        r.content_type,
        r.body.len(),
        CSP
    );
    if let Some(o) = &r.cors_origin {
        head.push_str(&format!(
            "Access-Control-Allow-Origin: {o}\r\nAccess-Control-Allow-Private-Network: true\r\nAccess-Control-Allow-Methods: POST, GET, OPTIONS\r\nAccess-Control-Allow-Headers: Content-Type, X-Igneum-Site\r\nAccess-Control-Expose-Headers: X-Igneum-Site\r\nAccess-Control-Max-Age: 600\r\nVary: Origin\r\n"
        ));
    }
    if let Some(tok) = &r.site_token {
        head.push_str(&format!("X-Igneum-Site: {tok}\r\n"));
    }
    head.push_str("\r\n");
    stream.write_all(head.as_bytes())?;
    stream.write_all(&r.body)?;
    stream.flush()
}

/// Binds 127.0.0.1 on `port` (0 for any) and serves forever on the calling thread. Returns the bound port first
/// through `on_bound`.
pub fn serve(port: u16, on_bound: impl FnOnce(u16), handler: Arc<dyn Fn(Request) -> Response + Send + Sync>) -> std::io::Result<()> {
    let listener = TcpListener::bind(("127.0.0.1", port))?;
    on_bound(listener.local_addr()?.port());
    for stream in listener.incoming() {
        match stream {
            Ok(s) => {
                let h = handler.clone();
                std::thread::spawn(move || handle_conn(s, &*h));
            }
            Err(_) => continue,
        }
    }
    Ok(())
}

pub fn content_type_for(path: &str) -> &'static str {
    match path.rsplit('.').next().unwrap_or("") {
        "html" => "text/html; charset=utf-8",
        "css" => "text/css; charset=utf-8",
        "js" | "mjs" => "text/javascript; charset=utf-8",
        "svg" => "image/svg+xml",
        "png" => "image/png",
        "woff2" => "font/woff2",
        "json" => "application/json",
        _ => "application/octet-stream",
    }
}
