//! The local web server: static dashboard files from the binary and a JSON API, on 127.0.0.1 with a random port.
//! Every path carries the per-launch token (`/t/<token>/...`); anything else is a 404, so another local user cannot
//! drive the miner. Plain HTTP/1.1, one thread per connection, Connection: close.

use crate::engine::{Cmd, Shared};
use serde_json::{json, Value};
use std::io::{BufRead, BufReader, Read, Write};
use std::net::{TcpListener, TcpStream};
use std::sync::Arc;

const INDEX: &str = include_str!("../ui/index.html");
const CSS: &str = include_str!("../ui/app.css");
const JS: &str = include_str!("../ui/app.js");
const MARK: &str = include_str!("../ui/mark.svg");
/// the network scene as a component (site/scenes/chain.js, the showcase team's; the Mine page's Activity card draws
/// this miner's shares landing in blocks from /api/scene)
const SCENE_JS: &str = include_str!("../ui/chain.js");
const SCENE_CSS: &str = include_str!("../ui/chain.css");
const COIN: &[u8] = include_bytes!("../../../brand/igneum-coin-1024.png");
const FONT_MONO_400: &[u8] = include_bytes!("../ui/fonts/IBMPlexMono-400.woff2");
const FONT_MONO_500: &[u8] = include_bytes!("../ui/fonts/IBMPlexMono-500.woff2");
const FONT_SANS_400: &[u8] = include_bytes!("../ui/fonts/IBMPlexSans-400.woff2");
const FONT_SANS_500: &[u8] = include_bytes!("../ui/fonts/IBMPlexSans-500.woff2");
const FONT_SANS_600: &[u8] = include_bytes!("../ui/fonts/IBMPlexSans-600.woff2");
const FONT_UNB_500: &[u8] = include_bytes!("../ui/fonts/Unbounded-500.woff2");
const FONT_UNB_700: &[u8] = include_bytes!("../ui/fonts/Unbounded-700.woff2");
const FONT_UNB_900: &[u8] = include_bytes!("../ui/fonts/Unbounded-900.woff2");

/// The page's default port (the project's order of 11:0x UK, 12 October 2026, after the founder's open tab on the mini went
/// dark across an install: a random port per launch made every install a new address). The installed app, the
/// --no-open launch and the fleet install all bind the same port; one in use steps to the next (PAGE_PORT_TRIES of
/// them) and says so in the log, never silently; only then a random one.
pub const PAGE_PORT: u16 = 18080;
pub const PAGE_PORT_TRIES: u16 = 20;
static PAGE_LAN: std::sync::atomic::AtomicBool = std::sync::atomic::AtomicBool::new(false);

/// The token an earlier launch wrote into app.url (`http://127.0.0.1:<port>/t/<32 hex>/`), so a restart or an install
/// keeps the page's address and an open tab reconnects by itself. None when the file is absent or not of that form.
pub fn token_from_url_file(path: &std::path::Path) -> Option<String> {
    let url = std::fs::read_to_string(path).ok()?;
    token_of_url(url.trim())
}

pub fn token_of_url(url: &str) -> Option<String> {
    let rest = url.split("/t/").nth(1)?;
    let tok = rest.split('/').next()?;
    (tok.len() == 32 && tok.bytes().all(|b| b.is_ascii_hexdigit())).then(|| tok.to_ascii_lowercase())
}

/// Binds the page: `wanted` first, then the next ports up to PAGE_PORT_TRIES, then any free one. The port bound is
/// returned; the caller logs the step when it is not the one wanted.
pub fn start(shared: Arc<Shared>, wanted: u16, lan: bool) -> std::io::Result<u16> {
    PAGE_LAN.store(lan, std::sync::atomic::Ordering::Relaxed);
    let host = if lan { "0.0.0.0" } else { "127.0.0.1" };
    let mut bound = None;
    for p in wanted..wanted.saturating_add(PAGE_PORT_TRIES) {
        if let Ok(l) = TcpListener::bind((host, p)) {
            bound = Some(l);
            break;
        }
    }
    let listener = match bound { Some(l) => l, None => TcpListener::bind((host, 0))? };
    let port = listener.local_addr()?.port();
    shared.set_port(port);
    std::thread::spawn(move || {
        for conn in listener.incoming() {
            let Ok(stream) = conn else { continue };
            let shared = shared.clone();
            std::thread::spawn(move || handle(stream, shared));
        }
    });
    Ok(port)
}

struct Req {
    method: String,
    path: String,
    query: String,
    body: Vec<u8>,
    origin: Option<String>,
    sec_fetch_site: Option<String>,
    host: Option<String>,
}

fn read_request(stream: &mut TcpStream) -> Option<Req> {
    let _ = stream.set_read_timeout(Some(std::time::Duration::from_secs(10)));
    let mut reader = BufReader::new(stream.try_clone().ok()?);
    let mut line = String::new();
    reader.read_line(&mut line).ok()?;
    let mut parts = line.split_whitespace();
    let method = parts.next()?.to_string();
    let target = parts.next()?.to_string();
    let mut content_length = 0usize;
    let (mut origin, mut sec_fetch_site, mut host) = (None, None, None);
    loop {
        let mut h = String::new();
        reader.read_line(&mut h).ok()?;
        let h = h.trim_end();
        if h.is_empty() {
            break;
        }
        if let Some((k, v)) = h.split_once(':') {
            let v = v.trim();
            if k.eq_ignore_ascii_case("content-length") {
                content_length = v.parse().unwrap_or(0);
            } else if k.eq_ignore_ascii_case("origin") {
                origin = Some(v.to_string());
            } else if k.eq_ignore_ascii_case("sec-fetch-site") {
                sec_fetch_site = Some(v.to_ascii_lowercase());
            } else if k.eq_ignore_ascii_case("host") {
                host = Some(v.to_string());
            }
        }
    }
    if content_length > 1 << 20 {
        return None;
    }
    let mut body = vec![0u8; content_length];
    if content_length > 0 {
        reader.read_exact(&mut body).ok()?;
    }
    let (path, query) = match target.split_once('?') {
        Some((p, q)) => (p.to_string(), q.to_string()),
        None => (target, String::new()),
    };
    Some(Req { method, path, query, body, origin, sec_fetch_site, host })
}

/// R4.3.7: a mutating request must come from the dashboard itself. A browser sends Sec-Fetch-Site (same-origin for
/// the dashboard; cross-site, same-site or none otherwise) and, on POST, an Origin; a cross-site page can reach
/// 127.0.0.1 only through the browser, so both are checked. A request without either header (curl, the window host)
/// still needs the token in the path.
fn from_dashboard(req: &Req, port: u16) -> bool {
    if let Some(s) = &req.sec_fetch_site {
        if s != "same-origin" && s != "none" {
            return false;
        }
    }
    // on a LAN-bound page (page_lan) the Host may be any address of this machine with the page's port, and the Origin
    // must be that same host; on loopback the two local names only
    let lan = PAGE_LAN.load(std::sync::atomic::Ordering::Relaxed);
    let host_ok = |h: &str| if lan { h.rsplit_once(':').map(|(_, p)| p == port.to_string()).unwrap_or(false) } else { h == format!("127.0.0.1:{port}") || h == format!("localhost:{port}") };
    if let Some(h) = &req.host {
        if !host_ok(h) {
            return false;
        }
    }
    if let Some(o) = &req.origin {
        let ok = match &req.host { Some(h) => o == &format!("http://{h}"), None => o == &format!("http://127.0.0.1:{port}") || o == &format!("http://localhost:{port}") };
        if !ok {
            return false;
        }
    }
    true
}

/// The Fleet proxy's one call (0.2.8): `<url><path>?<query>` with the method, the body and X-Fleet-Token; the
/// console's status, content type and body as answered. The path is the console's relative one (console.html,
/// console.json, history.json, act, api/v1/...); a path that climbs ("..") is refused.
pub fn fleet_proxy(url: &str, token: &str, method: &str, path: &str, query: &str, body: &[u8]) -> Result<(u16, String, Vec<u8>), String> {
    if path.contains("..") || path.starts_with('/') {
        return Err("the path climbs out of the console".into());
    }
    let full = if query.is_empty() { format!("{url}{path}") } else { format!("{url}{path}?{query}") };
    let parsed = igneum_supervisor::net::parse_url(&full).map_err(|e| format!("the console URL: {e}"))?;
    let method = if method == "POST" { "POST" } else { "GET" };
    let ctype = if method == "POST" { Some("application/json") } else { None };
    let (status, headers, raw) = igneum_supervisor::net::http_keepalive_with(method, &parsed, ctype, body, &[("X-Fleet-Token", token)], std::time::Duration::from_secs(20))?;
    let ct = headers.iter().find(|(k, _)| k.eq_ignore_ascii_case("content-type")).map(|(_, v)| v.clone()).unwrap_or_else(|| "application/octet-stream".into());
    Ok((status, ct, raw))
}

/// The sandboxed Fleet frame's own request: Origin "null" (an opaque origin) with the Host this engine's own; nothing
/// else about the request is trusted, and only the api/fleet path accepts it. Every path of this server sits under the
/// per-launch token (/t/<32 hex>/...), which a foreign page's sandboxed frame or form does not hold (the token lives in
/// app.url and the window's own URL alone), so an opaque-origin request without it is a 404 before this check.
/// The content type the Fleet proxy answers with for a console path: the page as html, a stylesheet, a script, JSON,
/// an SVG and a font by their extension, any other html as text (no second document under this origin's path), the
/// rest as the console said (F-E43).
pub fn fleet_content_type(path: &str, upstream: &str, is_page: bool) -> String {
    let ext = path.rsplit('.').next().unwrap_or("").to_ascii_lowercase();
    let by_ext = match ext.as_str() {
        "css" => Some("text/css; charset=utf-8"),
        "js" | "mjs" => Some("text/javascript; charset=utf-8"),
        "json" => Some("application/json; charset=utf-8"),
        "svg" => Some("image/svg+xml"),
        "woff2" => Some("font/woff2"),
        "woff" => Some("font/woff"),
        "png" => Some("image/png"),
        _ => None,
    };
    if let Some(t) = by_ext { return t.to_string(); }
    if !is_page && upstream.to_ascii_lowercase().starts_with("text/html") { return "text/plain; charset=utf-8".to_string(); }
    upstream.to_string()
}

#[cfg(test)]
mod f_e43 {
    #[test]
    fn types_by_extension_and_html_as_text() {
        assert_eq!(super::fleet_content_type("tokens.css", "text/plain", false), "text/css; charset=utf-8");
        assert_eq!(super::fleet_content_type("console.json", "text/plain", false), "application/json; charset=utf-8");
        assert_eq!(super::fleet_content_type("console.html", "text/html; charset=utf-8", true), "text/html; charset=utf-8");
        assert_eq!(super::fleet_content_type("other.html", "text/html; charset=utf-8", false), "text/plain; charset=utf-8");
        assert_eq!(super::fleet_content_type("act", "text/plain; charset=utf-8", false), "text/plain; charset=utf-8");
    }
}

/// The console page with each `<link rel="stylesheet" href="<name>.css">` (a relative name, no slash) replaced by the
/// sheet's text in a style element; a sheet the fetch cannot give keeps its link (F-E43).
pub fn inline_stylesheets(html: &[u8], fetch: impl Fn(&str) -> Option<Vec<u8>>) -> Vec<u8> {
    let text = match std::str::from_utf8(html) { Ok(t) => t, Err(_) => return html.to_vec() };
    let mut out = String::with_capacity(text.len() + 65536);
    let mut rest = text;
    while let Some(i) = rest.find("<link ") {
        let (head, tail) = rest.split_at(i);
        out.push_str(head);
        let end = match tail.find('>') { Some(e) => e + 1, None => { out.push_str(tail); rest = ""; break; } };
        let tag = &tail[..end];
        let name = tag.find("href=\"").and_then(|h| { let v = &tag[h + 6..]; v.find('"').map(|e| &v[..e]) });
        let inlined = match name {
            Some(n) if tag.contains("stylesheet") && n.ends_with(".css") && !n.contains('/') && !n.contains("..") => fetch(n).and_then(|b| String::from_utf8(b).ok()).filter(|css| !css.to_ascii_lowercase().contains("</style")).map(|css| format!("<style data-inlined=\"{n}\">\n{css}\n</style>")),
            _ => None,
        };
        match inlined { Some(st) => out.push_str(&st), None => out.push_str(tag) }
        rest = &tail[end..];
    }
    out.push_str(rest);
    out.into_bytes()
}

#[cfg(test)]
mod f_e43_inline {
    #[test]
    fn relative_stylesheets_inlined_others_kept() {
        let html = b"<head><link rel=\"stylesheet\" href=\"tokens.css\"><link rel=\"icon\" href=\"x.svg\"><link rel=\"stylesheet\" href=\"/abs.css\"></head>";
        let out = super::inline_stylesheets(html, |n| if n == "tokens.css" { Some(b"body{color:red}".to_vec()) } else { None });
        let s = String::from_utf8(out).unwrap();
        assert!(s.contains("<style data-inlined=\"tokens.css\">\nbody{color:red}\n</style>"));
        assert!(s.contains("<link rel=\"icon\" href=\"x.svg\">"));
        assert!(s.contains("href=\"/abs.css\""));
    }
}

fn from_sandboxed_frame(req: &Req, port: u16) -> bool {
    let origin_null = req.origin.as_deref().map_or(false, |o| o.trim() == "null");
    let lan = PAGE_LAN.load(std::sync::atomic::Ordering::Relaxed);
    let host_ok = req.host.as_deref().map_or(false, |h| if lan { h.rsplit_once(':').map(|(_, p)| p == port.to_string()).unwrap_or(false) } else { h == format!("127.0.0.1:{port}") || h == format!("localhost:{port}") });
    origin_null && host_ok
}

/// A console URL on the owner's own network, where http:// is allowed: loopback, a .local, .lan or .home name, or a
/// private (RFC 1918) address.
pub fn own_network_host(url: &str) -> bool {
    let rest = url.trim_start_matches("http://").trim_start_matches("https://");
    let host = rest.split(|c| c == '/' || c == ':').next().unwrap_or("").to_ascii_lowercase();
    if host == "localhost" || host == "127.0.0.1" || host == "::1" || host == "[::1]" { return true; }
    if host.ends_with(".local") || host.ends_with(".lan") || host.ends_with(".home") || host.ends_with(".internal") { return true; }
    let parts: Vec<u8> = host.split('.').filter_map(|p| p.parse::<u8>().ok()).collect();
    if parts.len() == 4 {
        return parts[0] == 10 || (parts[0] == 192 && parts[1] == 168) || (parts[0] == 172 && (16..=31).contains(&parts[1]));
    }
    false
}

fn respond(stream: &mut TcpStream, status: u16, ctype: &str, body: &[u8], cache: bool) {
    respond_with(stream, status, ctype, body, cache, "")
}

/// `respond` with extra header lines (each ending in CRLF): the Fleet proxy's answer to the sandboxed frame (F-E43).
fn respond_with(stream: &mut TcpStream, status: u16, ctype: &str, body: &[u8], cache: bool, extra: &str) {
    let reason = match status {
        200 => "OK",
        204 => "No Content",
        400 => "Bad Request",
        403 => "Forbidden",
        404 => "Not Found",
        405 => "Method Not Allowed",
        502 => "Bad Gateway",
        _ => "Error",
    };
    let head = format!(
        "HTTP/1.1 {status} {reason}\r\nContent-Type: {ctype}\r\nContent-Length: {}\r\nConnection: close\r\nCache-Control: {}\r\nX-Content-Type-Options: nosniff\r\nReferrer-Policy: no-referrer\r\n{extra}\r\n",
        body.len(),
        if cache { "public, max-age=86400" } else { "no-store" }
    );
    let _ = stream.write_all(head.as_bytes());
    let _ = stream.write_all(body);
    let _ = stream.flush();
}

fn json_resp(stream: &mut TcpStream, status: u16, v: Value) {
    respond(stream, status, "application/json; charset=utf-8", v.to_string().as_bytes(), false);
}

/// A JSON answer carrying the upstream host's Date header (the feed proxies: the chain scene measures the feed
/// host's clock from it; the page is same-origin, so the header is readable by its fetch).
fn json_resp_dated(stream: &mut TcpStream, status: u16, v: Value, date: Option<&str>) {
    let body = v.to_string();
    let date = date.map(|d| d.replace(['\r', '\n'], "")).filter(|d| !d.is_empty());
    let head = format!(
        "HTTP/1.1 {status} {}\r\nContent-Type: application/json; charset=utf-8\r\nContent-Length: {}\r\nConnection: close\r\nCache-Control: no-store\r\nX-Content-Type-Options: nosniff\r\nReferrer-Policy: no-referrer\r\n{}\r\n",
        if status == 200 { "OK" } else { "Error" },
        body.len(),
        date.map(|d| format!("Date: {d}\r\nX-Upstream-Date: {d}\r\nAccess-Control-Expose-Headers: Date, X-Upstream-Date\r\n")).unwrap_or_default()
    );
    let _ = stream.write_all(head.as_bytes());
    let _ = stream.write_all(body.as_bytes());
}

/// Where a screenshot lands: `<data root>/shots/igneum-<name>-<unix>.png`, the name reduced to [a-z0-9-] (at most
/// 24 characters, "shot" when empty) so a caller cannot steer the host outside the folder.
pub fn shot_path(data_root: &std::path::Path, name: &str, unix: u64) -> std::path::PathBuf {
    let safe: String = name.chars().filter(|c| c.is_ascii_alphanumeric() || *c == '-').map(|c| c.to_ascii_lowercase()).take(24).collect();
    let safe = if safe.is_empty() { "shot".to_string() } else { safe };
    data_root.join("shots").join(format!("igneum-{safe}-{unix}.png"))
}


fn query_param(q: &str, key: &str) -> Option<String> {
    q.split('&').find_map(|kv| {
        let (k, v) = kv.split_once('=')?;
        if k == key { Some(v.to_string()) } else { None }
    })
}

fn handle(mut stream: TcpStream, shared: Arc<Shared>) {
    let Some(req) = read_request(&mut stream) else { return };
    let prefix = format!("/t/{}", shared.token);
    if req.path == "/" || req.path == format!("/t/{}", shared.token) {
        // a bare root or the token without a slash: redirect-free hint, keep it a 404 for the root (no token leak)
        if req.path == "/" {
            respond(&mut stream, 404, "text/plain", b"Ember: open the app window.", false);
            return;
        }
    }
    let Some(rest) = req.path.strip_prefix(&prefix) else {
        respond(&mut stream, 404, "text/plain", b"not found", false);
        return;
    };
    let rest = if rest.is_empty() { "/" } else { rest };
    // ui-ota (src/uiota.rs): an active interface bundle serves its files in place of the embedded ones; the names are
    // fixed (uiota::SERVED), nothing else is read from the folder; a file the bundle lacks falls back to the embedded one
    if req.method == "GET" {
        let rel = if rest == "/" { "index.html" } else { rest.trim_start_matches('/') };
        if crate::uiota::SERVED.contains(&rel) {
            if let Some(dir) = shared.ui_dir() {
                if let Ok(bytes) = std::fs::read(dir.join(rel)) {
                    if rel == "index.html" {
                        let v = std::fs::read_to_string(dir.join("VERSION")).unwrap_or_default().trim().to_string();
                        shared.send(Cmd::UiPageLoaded(v));
                    }
                    respond(&mut stream, 200, crate::uiota::content_type(rel), &bytes, rel.starts_with("fonts/"));
                    return;
                }
            }
        }
    }
    // 0.2.8, the Fleet item (docs/fleet/DESIGN.md 3.6): GET and POST api/fleet/<path> reach the owner's console
    // through the engine's own origin (the window never loads another origin): the method, the path and the query
    // pass, the body and its content type on POST, the token as X-Fleet-Token from the engine's settings; the
    // console's status, content type and body come back unchanged, no cookies either way. Refused when no console
    // URL is set, and for a request that is not the dashboard's own.
    if let Some(sub) = rest.strip_prefix("/api/fleet/") {
        // the security review's read (14 October 2026): the console runs in a sandboxed frame (allow-scripts allow-forms, no
        // allow-same-origin) so its scripts hold an opaque origin and never the dashboard's rights; its own fetches
        // reach this path with Origin "null", accepted here alone when the Host is this engine's (the token path gates
        // every request as before); every other API keeps the dashboard check and refuses the frame
        if !from_dashboard(&req, shared.port()) && !from_sandboxed_frame(&req, shared.port()) {
            respond(&mut stream, 403, "text/plain", b"not from the dashboard", false);
            return;
        }
        let (url, token) = {
            let st = shared.settings.lock().unwrap();
            (st.fleet_console_url.clone(), st.fleet_console_token.clone())
        };
        if url.is_empty() {
            respond(&mut stream, 404, "text/plain", b"no fleet console is set (Settings, Fleet console)", false);
            return;
        }
        // the security review's residual (F-116b): the console's act path answers POST alone through the proxy, never a bare
        // GET or a form; the per-launch token in every path (/t/<token>/...) is the nonce no foreign page holds
        if req.method != "POST" && (sub == "act" || sub.ends_with("/act")) {
            respond(&mut stream, 405, "text/plain", b"the console's act path takes POST alone", false);
            return;
        }
        match fleet_proxy(&url, &token, &req.method, sub, &req.query, &req.body) {
            // text/html only for the console's page; any other path's html answer is served as text so no second
            // document runs under this origin's path
            Ok((status, ctype, body)) => {
                let is_page = sub == "console.html" || sub.ends_with("/console.html");
                // F-E43 (14 October 2026, the signed 0.2.8's Fleet section rendered unstyled and empty): the console's own
                // files answer with their type by extension (a stylesheet served as text is blocked by the browser; a
                // second html document is still served as text), and the sandboxed frame's own requests (Origin null
                // under this engine's Host, on the token path, which is the whole gate) get the allow-origin answer a
                // frame without allow-same-origin needs for its fetches
                // F-E43's second half (the read of 16:3x UK, 14 October 2026 on the installed fix): the browser blocks the
                // console's cross-origin stylesheets for the opaque-origin frame (ERR_BLOCKED_BY_ORB) although they
                // answer as text/css, so the page is served with its relative stylesheets inlined; its fonts are
                // fetched in CORS mode by the browser's own rule and pass on the allow-origin answer above
                let body = if is_page { inline_stylesheets(&body, |name| fleet_proxy(&url, &token, "GET", name, "", &[]).ok().filter(|(st, _, _)| *st == 200).map(|(_, _, b)| b)) } else { body };
                let ct = fleet_content_type(sub, &ctype, is_page);
                // the security review's line of 12:37 UK, 14 October 2026: every proxied answer carries its own sandbox header, so a
                // proxied document opened directly under this origin (an SVG with a script, a page at another
                // console.html path) is opaque-origin by its header and not by the frame's attribute alone
                let csp = if is_page { "Content-Security-Policy: sandbox allow-scripts allow-forms\r\n" } else { "Content-Security-Policy: sandbox\r\n" };
                let acao = if from_sandboxed_frame(&req, shared.port()) { "Access-Control-Allow-Origin: null\r\nVary: Origin\r\n" } else { "" };
                let extra = format!("{csp}{acao}");
                respond_with(&mut stream, status, &ct, &body, false, &extra)
            }
            Err(e) => respond(&mut stream, 502, "text/plain; charset=utf-8", format!("the fleet console did not answer: {e}").as_bytes(), false),
        }
        return;
    }
    match (req.method.as_str(), rest) {
        ("GET", "/") | ("GET", "/index.html") => respond(&mut stream, 200, "text/html; charset=utf-8", INDEX.as_bytes(), false),
        ("GET", "/VERSION") => respond(&mut stream, 200, "text/plain; charset=utf-8", crate::uiota::EMBEDDED_VERSION.as_bytes(), false),
        ("GET", "/app.css") => respond(&mut stream, 200, "text/css; charset=utf-8", CSS.as_bytes(), false),
        ("GET", "/app.js") => respond(&mut stream, 200, "application/javascript; charset=utf-8", JS.as_bytes(), false),
        ("GET", "/mark.svg") => respond(&mut stream, 200, "image/svg+xml", MARK.as_bytes(), true),
        ("GET", "/chain.js") => respond(&mut stream, 200, "application/javascript; charset=utf-8", SCENE_JS.as_bytes(), false),
        ("GET", "/chain.css") => respond(&mut stream, 200, "text/css; charset=utf-8", SCENE_CSS.as_bytes(), false),
        ("GET", "/coin.png") => respond(&mut stream, 200, "image/png", COIN, true),
        ("GET", "/fonts/IBMPlexMono-400.woff2") => respond(&mut stream, 200, "font/woff2", FONT_MONO_400, true),
        ("GET", "/fonts/IBMPlexMono-500.woff2") => respond(&mut stream, 200, "font/woff2", FONT_MONO_500, true),
        ("GET", "/fonts/IBMPlexSans-400.woff2") => respond(&mut stream, 200, "font/woff2", FONT_SANS_400, true),
        ("GET", "/fonts/IBMPlexSans-500.woff2") => respond(&mut stream, 200, "font/woff2", FONT_SANS_500, true),
        ("GET", "/fonts/IBMPlexSans-600.woff2") => respond(&mut stream, 200, "font/woff2", FONT_SANS_600, true),
        ("GET", "/fonts/Unbounded-500.woff2") => respond(&mut stream, 200, "font/woff2", FONT_UNB_500, true),
        ("GET", "/fonts/Unbounded-700.woff2") => respond(&mut stream, 200, "font/woff2", FONT_UNB_700, true),
        ("GET", "/fonts/Unbounded-900.woff2") => respond(&mut stream, 200, "font/woff2", FONT_UNB_900, true),
        ("GET", "/api/state") => {
            let v = shared.state_json();
            json_resp(&mut stream, 200, v);
        }
        // the chain scene's feed (src/live.rs): the observer's /api/live with this machine's lane as "you", cached 2 s
        ("GET", "/api/live") => {
            let window = query_param(&req.query, "window").and_then(|s| s.parse().ok()).unwrap_or(120u32);
            let live_api = std::env::var("IGNEUM_APP_LIVE_API").unwrap_or_else(|_| crate::ota::live_api_from(&shared.packaged.live_page));
            let ids: std::collections::HashSet<String> = shared.state.lock().unwrap().mining.cards.iter().flat_map(|c| c.ids.iter().cloned()).collect();
            json_resp(&mut stream, 200, crate::live::fetch(&shared, &live_api, window, &ids));
        }
        // miner-ui-5: the ladder's chain facts (src/chainfacts.rs): the node's getFinalityWeights through the local
        // node when it answers igneum_getFinalityWeights, else the observer's relay; cached 10 s
        ("GET", "/api/ladder") => {
            let live_api = std::env::var("IGNEUM_APP_LIVE_API").unwrap_or_else(|_| crate::ota::live_api_from(&shared.packaged.live_page));
            let ids: Vec<String> = shared.state.lock().unwrap().mining.cards.iter().flat_map(|c| c.ids.iter().cloned()).collect();
            json_resp(&mut stream, 200, crate::chainfacts::fetch(shared.runtime.evm_port(), &live_api, &ids));
        }
        // the network scene's feed (src/scene.rs): this miner's shares and the blocks that carry them
        ("GET", "/api/scene") => {
            json_resp(&mut stream, 200, crate::scene::fetch(&shared));
        }
        // the Live Work Ledger's miner view for this address, read by the engine and cached a block (the window
        // never fetches another origin: the founder's order of 12 October 2026)
        // the Pools page (src/pools.rs): every listed pool from its own disclosure document beside the probe figure
        // the chain scene's feeds through the engine (src/scene.rs): the status and chain feeds the network source reads
        ("GET", "/api/feed/status") | ("GET", "/api/feed/devnet") => {
            match crate::scene::feed_proxy(rest.rsplit('/').next().unwrap_or("")) {
                Ok((v, date)) => json_resp_dated(&mut stream, 200, v, date.as_deref()),
                Err(e) => json_resp(&mut stream, 502, json!({ "ok": false, "error": e })),
            }
        }
        ("GET", "/api/pools") => {
            json_resp(&mut stream, 200, crate::pools::fetch());
        }
        ("GET", "/api/ledger") => {
            json_resp(&mut stream, 200, crate::scene::ledger(&shared));
        }
        // the live-shares feed for this address (src/scene.rs), cached 2 s: the chain card's sparks and tally
        ("GET", "/api/shares") => {
            json_resp(&mut stream, 200, crate::scene::shares(&shared));
        }
        ("GET", "/api/log") => {
            let after = query_param(&req.query, "after").and_then(|s| s.parse().ok()).unwrap_or(0u64);
            let limit = query_param(&req.query, "limit").and_then(|s| s.parse().ok()).unwrap_or(400usize).min(2000);
            let lines = shared.rings.lock().unwrap().since(after, limit);
            json_resp(&mut stream, 200, json!({ "lines": lines }));
        }
        // the chain scene's JSON-RPC proxy answers with the upstream Date header (showcase 98fb7cba3)
        ("POST", "/api/rpc") => {
            if !from_dashboard(&req, shared.port()) {
                json_resp(&mut stream, 403, json!({ "ok": false, "error": "not from the dashboard" }));
                return;
            }
            let body: Value = if req.body.is_empty() { json!({}) } else { serde_json::from_slice(&req.body).unwrap_or(json!({})) };
            match crate::scene::rpc_proxy(body) {
                Ok((v, date)) => json_resp_dated(&mut stream, 200, v, date.as_deref()),
                Err(e) => json_resp(&mut stream, 400, json!({ "ok": false, "error": e })),
            }
        }
        ("POST", p) => {
            if !from_dashboard(&req, shared.port()) {
                json_resp(&mut stream, 403, json!({ "ok": false, "error": "not from the dashboard" }));
                return;
            }
            let body: Value = if req.body.is_empty() { json!({}) } else { serde_json::from_slice(&req.body).unwrap_or(json!({})) };
            let out = api_post(&shared, p, body);
            match out {
                Ok(v) => json_resp(&mut stream, 200, v),
                Err(e) => json_resp(&mut stream, 400, json!({ "ok": false, "error": e })),
            }
        }
        _ => respond(&mut stream, 404, "text/plain", b"not found", false),
    }
}

fn api_post(shared: &Arc<Shared>, path: &str, body: Value) -> Result<Value, String> {
    let s = |k: &str| body.get(k).and_then(|v| v.as_str()).map(|v| v.to_string());
    match path {
        // the chain scene's own clock (the founder's order of 21:4x UK, 13 October 2026: the next-block logic
        // perfected on both surfaces): the component reads the chain's latest header and the last 64 headers
        // through the engine, never another origin from the window (the order of 12 October 2026); the engine
        // forwards an allow-listed JSON-RPC request, single or a batch of at most 20, to the public RPC
        "/api/rpc" => crate::scene::rpc_proxy(body).map(|(v, _)| v),
        "/api/detect" => {
            shared.send(Cmd::Detect);
            Ok(json!({ "ok": true }))
        }
        "/api/phase" => {
            let phase = s("phase").ok_or("phase missing")?;
            shared.state.lock().unwrap().phase = phase;
            Ok(json!({ "ok": true }))
        }
        "/api/setup" => {
            let mode = s("mode").unwrap_or_else(|| "generate".into());
            let identities = body.get("identities").and_then(|v| v.as_u64()).map(|v| v.clamp(1, 64) as u32);
            let out = shared.setup(&mode, s("address").as_deref(), identities)?;
            Ok(out)
        }
        "/api/key/saved" => {
            shared.key_saved();
            Ok(json!({ "ok": true }))
        }
        "/api/key/reveal" => shared.reveal_key(),
        "/api/start" => {
            shared.send(Cmd::Start);
            Ok(json!({ "ok": true }))
        }
        "/api/pause" => {
            // the page says by=user; a script (a job's pause) says nothing and is a job's
            let by = if body.get("by").and_then(|v| v.as_str()) == Some("user") { "user" } else { "job" };
            shared.send(Cmd::Pause(by.into()));
            Ok(json!({ "ok": true }))
        }
        "/api/resume" => {
            shared.send(Cmd::Resume);
            Ok(json!({ "ok": true }))
        }
        "/api/cards" => {
            let list = body.get("cards").and_then(|v| v.as_array()).ok_or("cards missing")?;
            let choices: Vec<crate::engine::CardChoice> = list
                .iter()
                .filter_map(|c| {
                    Some(crate::engine::CardChoice {
                        key: c.get("key")?.as_str()?.to_string(),
                        enabled: c.get("enabled").and_then(|v| v.as_bool()).unwrap_or(true),
                        identities: c.get("identities").and_then(|v| v.as_u64()).unwrap_or(1).clamp(1, 64) as u32,
                        power_pct: c.get("power_pct").and_then(|v| v.as_u64()).map(|v| v.clamp(crate::sweep::MIN_PCT as u64, 100) as u32),
                    })
                })
                .collect();
            shared.send(Cmd::ApplyCards(choices));
            Ok(json!({ "ok": true }))
        }
        "/api/tune/tier" => {
            // Ember Tune tiers (8 October 2026): apply a measured tier to one card or every card, no re-search
            let tier = body.get("tier").and_then(|v| v.as_str()).unwrap_or("").to_string();
            if !crate::ember::TIER_IDS.contains(&tier.as_str()) {
                return Err("tier must be efficiency, balanced or max".into());
            }
            let key = body.get("key").and_then(|v| v.as_str()).filter(|k| !k.is_empty()).map(|k| k.to_string());
            let applied: Vec<String> = {
                let st = shared.state.lock().unwrap();
                match &key {
                    Some(k) => {
                        let Some(c) = st.mining.cards.iter().find(|c| &c.key == k) else { return Err("no such card".into()) };
                        if crate::ember::tiers_stale(&c.tiers_class, &c.program_class) {
                            return Err(crate::ember::stale_text(&c.name, &c.tiers_class, &c.program_class));
                        }
                        if crate::ember::tier_from_json(&c.tiers, &tier).is_none() {
                            return Err("not measured yet: the search runs 2 min into steady mining".into());
                        }
                        vec![c.key.clone()]
                    }
                    None => st.mining.cards.iter().filter(|c| !crate::ember::tiers_stale(&c.tiers_class, &c.program_class) && crate::ember::tier_from_json(&c.tiers, &tier).is_some()).map(|c| c.key.clone()).collect(),
                }
            };
            shared.send(Cmd::TuneTier(key, tier));
            Ok(json!({ "ok": true, "applied": applied }))
        }
        "/api/tune/goal" => {
            // Ember 2: the goal, the electricity price (pence per kWh) and the hill-climb switch
            let goal = body.get("goal").and_then(|v| v.as_str()).map(|g| crate::ember::Goal::parse(g).name().to_string());
            let price = body.get("price_pence").and_then(|v| v.as_f64()).filter(|p| (0.0..=500.0).contains(p));
            let climb = body.get("climb").and_then(|v| v.as_bool());
            // with "key": that card's goal override ("" or "global" clears it); without: the global goal
            let key = body.get("key").and_then(|v| v.as_str()).map(|k| k.to_string());
            if let Some(k) = key {
                let g = body.get("goal").and_then(|v| v.as_str()).unwrap_or("");
                let g = if g.is_empty() || g == "global" { String::new() } else { crate::ember::Goal::parse(g).name().to_string() };
                shared.send(Cmd::TuneCardGoal(k, g));
                return Ok(json!({ "ok": true }));
            }
            shared.send(Cmd::TuneGoal(goal, price, climb));
            Ok(json!({ "ok": true }))
        }
        "/api/source" => {
            // the work source (docs/product-a/OLD-BASE.md step 3): {kind: "node" | "pool", url}; the engine checks it,
            // saves it and restarts the worker layer on it
            let kind = body.get("kind").and_then(|v| v.as_str()).unwrap_or("").to_string();
            let url = body.get("url").and_then(|v| v.as_str()).unwrap_or("").to_string();
            shared.send(Cmd::Source(kind, url));
            Ok(json!({ "ok": true }))
        }
        "/api/region" => {
            // Ember Heat: the region code and the typed price per kWh in that region's minor unit (never fetched)
            let region = body.get("region").and_then(|v| v.as_str()).map(|r| r.to_string());
            // hundredths of the chosen unit per kWh: up to 100,000 so a zero-decimal currency (yen, won) fits
            let price = body.get("price_pence").and_then(|v| v.as_f64()).filter(|p| (0.0..=100_000.0).contains(p));
            let currency = body.get("currency").and_then(|v| v.as_str()).map(|c| c.to_string());
            shared.send(Cmd::Region(region, price, currency));
            Ok(json!({ "ok": true }))
        }
        "/api/heat" => {
            // Ember Heat: the switch, the set point, the schedule with the window's clock offset, a typed room reading
            let patch = crate::engine::HeatPatch {
                on: body.get("on").and_then(|v| v.as_bool()),
                set_c: body.get("set_c").and_then(|v| v.as_f64()),
                schedule: body.get("schedule").and_then(|v| v.as_str()).map(|t| (t.to_string(), body.get("tz_min").and_then(|v| v.as_i64()).unwrap_or(0).clamp(-840, 840) as i32)),
                room_c: body.get("room_c").and_then(|v| v.as_f64()),
            };
            if let Some(c) = patch.set_c {
                if !(crate::heat::SET_MIN_C..=crate::heat::SET_MAX_C).contains(&c) {
                    return Err(format!("the set point is {:.0} to {:.0} degrees", crate::heat::SET_MIN_C, crate::heat::SET_MAX_C));
                }
            }
            if let Some((t, _)) = &patch.schedule {
                crate::heat::parse_schedule(t)?;
            }
            if let Some(c) = patch.room_c {
                if !(-20.0..=50.0).contains(&c) {
                    return Err("a room reading is -20 to 50 degrees".into());
                }
            }
            shared.send(Cmd::Heat(patch));
            Ok(json!({ "ok": true }))
        }
        // 0.2.8, the Fleet item: the console URL and the window's token (an absent key leaves a field as it is; an
        // empty token clears the held one)
        "/api/fleet" => shared.set_fleet(s("fleet_console_url").as_deref(), s("fleet_console_token").as_deref()),
        "/api/settings" => {
            let identities = body.get("identities").and_then(|v| v.as_u64()).map(|v| v.clamp(1, 64) as u32);
            let vote = body.get("vote").and_then(|v| v.as_bool());
            let login = body.get("start_at_login").and_then(|v| v.as_bool());
            let address = s("address");
            let display_name = s("display_name");
            let dev_fee = body.get("dev_fee").and_then(|v| v.as_bool());
            let proof_verify_trust = body.get("proof_verify_trust").and_then(|v| v.as_bool());
            let profile_public = body.get("profile_public").and_then(|v| v.as_bool());
            shared.apply_settings(identities, vote, login, address.as_deref(), display_name.as_deref(), dev_fee, proof_verify_trust, profile_public)
        }
        "/api/drivers/install" => {
            let vendor = body.get("vendor").and_then(|v| v.as_str()).unwrap_or("").to_ascii_lowercase();
            if !["nvidia", "amd", "intel"].contains(&vendor.as_str()) {
                return Err("vendor must be nvidia, amd or intel".into());
            }
            shared.send(Cmd::DriverInstall(vendor));
            Ok(json!({ "ok": true }))
        }
        "/api/drivers/restart" => {
            shared.send(Cmd::DriverRestart);
            Ok(json!({ "ok": true }))
        }
        "/api/update/check" => {
            shared.send(Cmd::CheckUpdate);
            Ok(json!({ "ok": true }))
        }
        // the founder's order of 11:0x UK, 12 October 2026: "Reset to recommended" puts every default back (the
        // address, the cards' identities and the counters stay)
        "/api/settings/recommended" => {
            shared.reset_recommended();
            Ok(json!({ "ok": true }))
        }
        "/api/update/install" => {
            shared.send(Cmd::InstallUpdate);
            Ok(json!({ "ok": true }))
        }
        "/api/ui/health" => {
            // ui-ota: the page's first-paint ping (no error) or the error it caught before it (src/uiota.rs)
            let err = body.get("error").and_then(|v| v.as_str()).filter(|e| !e.is_empty()).map(|e| e.to_string());
            shared.send(Cmd::UiHealth(err));
            Ok(json!({ "ok": true }))
        }
        "/api/ui/builtin" => {
            let on = body.get("on").and_then(|v| v.as_bool()).ok_or("on missing")?;
            shared.send(Cmd::UiBuiltin(on));
            Ok(json!({ "ok": true }))
        }
        "/api/update/auto" => {
            let on = body.get("on").and_then(|v| v.as_bool()).ok_or("on missing")?;
            shared.send(Cmd::AutoUpdate(on));
            Ok(json!({ "ok": true }))
        }
        "/api/update/open" => {
            shared.send(Cmd::OpenUpdateFile);
            Ok(json!({ "ok": true }))
        }
        // F14: the remote-jobs routes exist on the lab build only; the public miner has no route that runs a job
        #[cfg(feature = "lab")]
        "/api/jobs/allow" => {
            let on = body.get("on").and_then(|v| v.as_bool()).ok_or("on missing")?;
            shared.send(Cmd::JobsAllow(on));
            Ok(json!({ "ok": true }))
        }
        #[cfg(feature = "lab")]
        "/api/jobs/check" => {
            shared.send(Cmd::JobsCheck);
            Ok(json!({ "ok": true }))
        }
        "/api/open" => {
            let url = s("url").ok_or("url missing")?;
            if url.starts_with("https://") || url.starts_with("http://") {
                crate::platform::open_url(&url);
                Ok(json!({ "ok": true }))
            } else {
                Err("only http(s) links open".into())
            }
        }
        "/api/power/apply" => {
            shared.send(Cmd::ApplyPower);
            Ok(json!({ "ok": true }))
        }
        // the efficiency sweep (src/sweep.rs): start one card now, stop the running one, pin or unpin a card's cap,
        // the weekly toggle
        "/api/sweep/start" => {
            let key = s("key").ok_or("key missing")?;
            shared.send(Cmd::SweepStart(key));
            Ok(json!({ "ok": true }))
        }
        "/api/sweep/stop" => {
            shared.send(Cmd::SweepStop);
            Ok(json!({ "ok": true }))
        }
        "/api/sweep/pin" => {
            let key = s("key").ok_or("key missing")?;
            let pinned = body.get("pinned").and_then(|v| v.as_bool()).ok_or("pinned missing")?;
            shared.send(Cmd::SweepPin(key, pinned));
            Ok(json!({ "ok": true }))
        }
        // Power control (config.rs power_control): on = one administrator prompt now for the cap, off = nothing asks
        "/api/power/control" => {
            let on = body.get("on").and_then(|v| v.as_bool()).ok_or("on missing")?;
            shared.send(Cmd::PowerControl(on));
            Ok(json!({ "ok": true }))
        }
        // a measurement engine's live tune state for one card (forwarded by its job playbook): the row shows the step
        "/api/tune-progress" => {
            shared.send(Cmd::TuneProgress(body));
            Ok(json!({ "ok": true }))
        }
        "/api/sweep/enable" => {
            let on = body.get("on").and_then(|v| v.as_bool()).ok_or("on missing")?;
            shared.send(Cmd::SweepEnable(on));
            Ok(json!({ "ok": true }))
        }
        "/api/clock/sync" => {
            shared.send(Cmd::ClockSync);
            Ok(json!({ "ok": true }))
        }
        "/api/clock/check" => {
            shared.send(Cmd::ClockCheck);
            Ok(json!({ "ok": true }))
        }
        // Miner UI 4 (b): a screenshot from the machine itself. The host (app/mac, app/windows) owns the window, so the
        // engine asks it over the host protocol ("SHOT <path>", beside URL, STATE and EXIT) and the host writes a PNG of
        // what it shows to that path under <data root>/shots/, where a collect job's glob "shots/*.png" picks it up.
        // Read-only: nothing about mining changes. A caller without a host (a bare engine) gets the path and no file.
        // miner-ui-5: the block card's PNG, drawn by the page on a canvas (no network call), written under
        // <data root>/cards/ and the folder opened for the user. The body is {name, png: "data:image/png;base64,..."}.
        // first-block-21: the page showed (or the user dismissed) the block card for the milestone at {count, at};
        // the card is spent for later runs and a first-block card sets the lifetime flag (src/ladder.rs card_seen)
        "/api/card/seen" => {
            let count = body.get("count").and_then(|v| v.as_u64()).unwrap_or(0);
            let at = body.get("at").and_then(|v| v.as_f64()).unwrap_or(0.0);
            let (changed, mark) = { let mut ld = shared.ladder.lock().unwrap(); let mark = ld.first_card_mark(); (ld.card_seen(count, at), mark) };
            if changed { shared.save_ladder(); }
            // v0.2.1: a first card seen writes the per-address mark into settings.json (once per payout address, ever)
            if changed {
                if let Some((hash, when)) = mark {
                    let mut s = shared.settings.lock().unwrap();
                    let addr = s.address.to_ascii_lowercase();
                    if !addr.is_empty() && !s.first_block_shown.contains_key(&addr) {
                        s.first_block_shown.insert(addr, crate::config::FirstBlockMark { hash, at: when });
                        s.save(&shared.settings_path);
                    }
                }
            }
            Ok(json!({ "ok": true, "changed": changed }))
        }
        "/api/card" => {
            let name = body.get("name").and_then(|v| v.as_str()).unwrap_or("block");
            let png = body.get("png").and_then(|v| v.as_str()).ok_or("png missing")?;
            let bytes = crate::card::decode_data_url(png).ok_or("the png is not a base64 data URL")?;
            let path = crate::card::card_path(&crate::platform::data_root(), name, crate::platform::unix_now());
            if let Some(dir) = path.parent() { let _ = std::fs::create_dir_all(dir); }
            std::fs::write(&path, &bytes).map_err(|e| format!("could not write the card: {e}"))?;
            if body.get("reveal").and_then(|v| v.as_bool()).unwrap_or(true) { crate::platform::reveal_file(&path); }
            Ok(json!({ "ok": true, "path": path.display().to_string(), "bytes": bytes.len() }))
        }
        "/api/shot" => {
            let name = body.get("name").and_then(|v| v.as_str()).unwrap_or("");
            let path = shot_path(&crate::platform::data_root(), name, crate::platform::unix_now());
            if let Some(dir) = path.parent() { let _ = std::fs::create_dir_all(dir); }
            println!("SHOT {}", path.display());
            Ok(json!({ "ok": true, "path": path.display().to_string(), "note": "the window host writes the PNG within a few seconds; a bare engine without a host writes nothing" }))
        }
        "/api/quit" => {
            // the caller is on 127.0.0.1 and holds the token: the installer, the OTA apply, a script that read app.url
            shared.send(Cmd::Quit("POST /api/quit (a local caller with the token: the installer, the OTA apply, or a script that read app.url)"));
            // an earlier release: armed here too, so a tick blocked on a syncing node (the second Windows machine, 21:24 UK) cannot hold the quit
            crate::quitguard::arm(shared.clone(), "EXIT update");
            Ok(json!({ "ok": true }))
        }
        _ => Err("unknown api".into()),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn the_token_is_read_back_from_the_url_file_form() {
        assert_eq!(token_of_url("http://127.0.0.1:18080/t/0e69a1b2c3d4e5f60718293a4b5c6d7e/"), Some("0e69a1b2c3d4e5f60718293a4b5c6d7e".to_string()));
    }

    #[test]
    fn the_fleet_proxy_refuses_a_climbing_path_and_an_absolute_one() {
        // 0.2.8: the console's relative paths only; a climb or an absolute path never reaches the console host
        assert!(fleet_proxy("https://h/_fleet/t/", "tok", "GET", "../secret", "", &[]).is_err());
        assert!(fleet_proxy("https://h/_fleet/t/", "tok", "GET", "/etc/passwd", "", &[]).is_err());
        // the security review's read: http:// only on the owner's own network
        assert!(own_network_host("http://localhost:8080/_fleet/t/"));
        assert!(own_network_host(&format!("http://{}.1.20/_fleet/t/", "192.168")) && own_network_host("http://10.0.0.5:81/x/") && own_network_host("http://172.16.9.9/"));
        assert!(own_network_host("http://console.local/_fleet/t/"));
        assert!(!own_network_host("http://build.igneum.network/_fleet/t/") && !own_network_host("http://8.8.8.8/"));
        assert_eq!(token_of_url("http://127.0.0.1:18080/t/0E69A1B2C3D4E5F60718293A4B5C6D7E/\n"), Some("0e69a1b2c3d4e5f60718293a4b5c6d7e".to_string()));
        assert_eq!(token_of_url("http://127.0.0.1:18080/"), None);
        assert_eq!(token_of_url("http://127.0.0.1:18080/t/short/"), None);
        assert_eq!(token_of_url("http://127.0.0.1:18080/t/zz69a1b2c3d4e5f60718293a4b5c6d7e/"), None);
        assert_eq!(token_from_url_file(std::path::Path::new("/nonexistent/app.url")), None);
    }
    #[test]
    fn a_shot_lands_under_the_data_root_with_a_safe_name() {
        let root = std::path::Path::new("/tmp/igneum-data");
        assert_eq!(shot_path(root, "overview", 1791327000), std::path::PathBuf::from("/tmp/igneum-data/shots/igneum-overview-1791327000.png"));
        assert_eq!(shot_path(root, "../../etc/passwd", 1), std::path::PathBuf::from("/tmp/igneum-data/shots/igneum-etcpasswd-1.png"));
        assert_eq!(shot_path(root, "", 2), std::path::PathBuf::from("/tmp/igneum-data/shots/igneum-shot-2.png"));
        assert_eq!(shot_path(root, "Cards Light!", 3), std::path::PathBuf::from("/tmp/igneum-data/shots/igneum-cardslight-3.png"));
        assert!(shot_path(root, &"x".repeat(80), 4).file_name().unwrap().to_string_lossy().len() < 48);
    }
}
