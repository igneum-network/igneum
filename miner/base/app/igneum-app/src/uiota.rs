//! Interface over the air (docs/plans/ui-ota.md, 7 October 2026): the dashboard (app/igneum-app/ui, embedded in the
//! engine binary) can be replaced by a signed bundle from the manifest's `ui` channel without a new app version.
//!
//! The flow, driven from the updater's hourly manifest (src/ota.rs hands the parsed `ui` entry over):
//!   decide: the entry is ignored when its min_engine is newer than this engine, when its version is the active
//!           one or the embedded one, when it was marked bad before, or when the miner chose the built-in interface
//!   -> download (curl to <app data>/ui/<version>.tar.gz, size and sha256 against the manifest, then the entry's own
//!      Ed25519 signature with the release key compiled into src/manifest.rs; the manifest's signature covered it too)
//!   -> unpack with the system tar into <app data>/ui/<version>.new, index.html, app.js and app.css must be there,
//!      rename to <app data>/ui/<version>
//!   -> swap: <app data>/ui/current.json names the version (written to .tmp and renamed: atomic); the server reads
//!      the pointer through Shared and serves the bundle's files in place of the embedded ones at the next page load;
//!      the open page sees state.ui.active_version change and reloads itself when idle
//!   -> confirm: the first page load from a new bundle starts a 10 s wait for the page's health ping
//!      (POST /api/ui/health after its first paint; a JS error on first paint posts the error instead); no ping, an
//!      error, or a bundle that fails to unpack rolls back to the embedded interface and marks the version bad
//!      (<app data>/ui/bad.json): it is never applied again
//! The kill switch is the manifest without a `ui` object: the engine falls back to the embedded interface on the
//! next check. Same origin, no remote script: the bundle is served from 127.0.0.1 by this engine like the embedded
//! files, and the signature is the only trust.

use crate::engine::{Cmd, Shared};
use crate::manifest::{self, UiEntry};
use std::path::{Path, PathBuf};
use std::sync::Arc;
use std::time::{Duration, Instant};

/// The page's health ping must arrive this long after the first load of a new bundle.
pub const HEALTH_WAIT_S: u64 = 10;
/// The version of the interface compiled into this engine (app/igneum-app/ui/VERSION).
pub const EMBEDDED_VERSION: &str = include_str!("../ui/VERSION");
/// The files a bundle may serve: fixed names, nothing else is read from the bundle folder.
pub const SERVED: [&str; 15] = ["index.html", "app.css", "app.js", "mark.svg", "chain.js", "chain.css", "VERSION", "fonts/IBMPlexMono-400.woff2", "fonts/IBMPlexMono-500.woff2", "fonts/IBMPlexSans-400.woff2", "fonts/IBMPlexSans-500.woff2", "fonts/IBMPlexSans-600.woff2", "fonts/Unbounded-500.woff2", "fonts/Unbounded-700.woff2", "fonts/Unbounded-900.woff2"];
/// What a bundle must carry to be swapped in.
const REQUIRED: [&str; 3] = ["index.html", "app.js", "app.css"];

pub fn embedded_version() -> &'static str {
    EMBEDDED_VERSION.trim()
}

pub enum Event {
    /// the download, the checks and the unpack finished: the bundle folder, or why not (with the version)
    Installed(String, Result<PathBuf, String>),
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Decision {
    Apply,
    Skip(String),
}

/// Whether a published entry is for this engine now (pure; the tests cover every branch).
pub fn decide(e: &UiEntry, engine: &str, embedded: &str, active: Option<&str>, bad: &[String], builtin: bool) -> Decision {
    if builtin {
        return Decision::Skip("the built-in interface is chosen in Settings".into());
    }
    if manifest::newer(&e.min_engine, engine) {
        return Decision::Skip(format!("interface {} needs engine {} or newer (this is {engine})", e.version, e.min_engine));
    }
    if bad.iter().any(|b| b == &e.version) {
        return Decision::Skip(format!("interface {} failed here before and is not tried again", e.version));
    }
    if active == Some(e.version.as_str()) {
        return Decision::Skip(format!("interface {} is active", e.version));
    }
    if e.version == embedded {
        return Decision::Skip(format!("interface {} is the built-in one", e.version));
    }
    Decision::Apply
}

/// The pointer file: which bundle the server serves ("embedded" or a version) and when it was swapped in.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct Current {
    pub version: String,
    pub installed_at: u64,
    /// the page confirmed this bundle with a health ping
    pub confirmed: bool,
}

fn read_current(dir: &Path) -> Current {
    let v: serde_json::Value = std::fs::read_to_string(dir.join("current.json")).ok().and_then(|t| serde_json::from_str(&t).ok()).unwrap_or(serde_json::Value::Null);
    Current { version: v.get("version").and_then(|x| x.as_str()).unwrap_or("embedded").to_string(), installed_at: v.get("installed_at").and_then(|x| x.as_u64()).unwrap_or(0), confirmed: v.get("confirmed").and_then(|x| x.as_bool()).unwrap_or(false) }
}

/// Writes the pointer atomically (the .tmp then the rename).
pub fn write_current(dir: &Path, c: &Current) -> Result<(), String> {
    let _ = std::fs::create_dir_all(dir);
    let tmp = dir.join("current.json.tmp");
    std::fs::write(&tmp, serde_json::json!({ "version": c.version, "installed_at": c.installed_at, "confirmed": c.confirmed }).to_string()).map_err(|e| e.to_string())?;
    std::fs::rename(&tmp, dir.join("current.json")).map_err(|e| e.to_string())
}

fn read_bad(dir: &Path) -> Vec<String> {
    std::fs::read_to_string(dir.join("bad.json")).ok().and_then(|t| serde_json::from_str(&t).ok()).unwrap_or_default()
}

fn write_bad(dir: &Path, bad: &[String]) {
    let _ = std::fs::write(dir.join("bad.json"), serde_json::to_string(bad).unwrap_or_default());
}

/// The bundle folder for a version, if it holds what the server needs.
pub fn bundle_dir(dir: &Path, version: &str) -> Option<PathBuf> {
    if version == "embedded" || version.is_empty() {
        return None;
    }
    let d = dir.join(version);
    if REQUIRED.iter().all(|f| d.join(f).is_file()) { Some(d) } else { None }
}

/// Size, sha256 and the entry's own signature, then the unpack into <dir>/<version>: the folder on success.
/// `pub_hex` is the release key (manifest::OTA_PUBLIC_KEY_HEX in the engine; the tests pass their own).
pub fn install_bundle(e: &UiEntry, file: &Path, dir: &Path, pub_hex: &str) -> Result<PathBuf, String> {
    let size = std::fs::metadata(file).map(|m| m.len()).map_err(|er| format!("{}: {er}", file.display()))?;
    if size != e.size {
        return Err(format!("interface {}: the download is {size} bytes, the manifest says {}", e.version, e.size));
    }
    let sum = manifest::sha256_file(file).map_err(|er| er.to_string())?;
    if sum != e.sha256 {
        return Err(format!("interface {}: sha256 mismatch", e.version));
    }
    manifest::verify_ui_entry(e, pub_hex).map_err(|er| format!("interface {}: {er}", e.version))?;
    let fresh = dir.join(format!("{}.new", e.version));
    let _ = std::fs::remove_dir_all(&fresh);
    std::fs::create_dir_all(&fresh).map_err(|er| er.to_string())?;
    let mut c = std::process::Command::new(crate::platform::tool("tar"));
    c.args(["-xzf", &file.display().to_string(), "-C", &fresh.display().to_string()]);
    let out = crate::detect::run_timeout(&mut c, None, Duration::from_secs(60)).ok_or("tar is not available")?;
    // a bundle packed with a top-level folder: take it
    let root = if REQUIRED.iter().all(|f| fresh.join(f).is_file()) {
        fresh.clone()
    } else {
        let inner = std::fs::read_dir(&fresh).ok().into_iter().flatten().filter_map(|d| d.ok()).map(|d| d.path()).find(|p| p.is_dir() && REQUIRED.iter().all(|f| p.join(f).is_file()));
        match inner {
            Some(p) => p,
            None => {
                let _ = std::fs::remove_dir_all(&fresh);
                return Err(format!("interface {}: the bundle has no index.html, app.js and app.css ({})", e.version, out.lines().last().unwrap_or("tar said nothing")));
            }
        }
    };
    let dest = dir.join(&e.version);
    let _ = std::fs::remove_dir_all(&dest);
    std::fs::rename(&root, &dest).map_err(|er| format!("interface {}: {er}", e.version))?;
    let _ = std::fs::remove_dir_all(&fresh);
    // the bundle's own VERSION must agree with the manifest (a renamed bundle is refused)
    let v = std::fs::read_to_string(dest.join("VERSION")).unwrap_or_default();
    if v.trim() != e.version {
        let _ = std::fs::remove_dir_all(&dest);
        return Err(format!("interface {}: the bundle's VERSION file says '{}'", e.version, v.trim()));
    }
    Ok(dest)
}

/// The engine's side: what is active, what is pending, the health wait, the rollback.
pub struct UiOta {
    dir: PathBuf,
    current: Current,
    bad: Vec<String>,
    builtin: bool,
    busy: bool,
    /// the manifest entry seen last (for the Settings line and the retry after an error)
    entry: Option<UiEntry>,
    /// a bundle served to a page and not yet confirmed: (version, when the page loaded)
    waiting: Option<(String, Instant)>,
    error: String,
    /// the whole download thread's last reason, kept for the state
    last_skip: String,
    health_wait: Duration,
}

impl UiOta {
    pub fn new(shared: &Arc<Shared>) -> UiOta {
        let dir = shared.runtime.app_dir.join("ui");
        let _ = std::fs::create_dir_all(&dir);
        let builtin = shared.settings.lock().unwrap().ui_builtin;
        let mut u = UiOta { dir: dir.clone(), current: read_current(&dir), bad: read_bad(&dir), builtin, busy: false, entry: None, waiting: None, error: String::new(), last_skip: String::new(), health_wait: Duration::from_secs(HEALTH_WAIT_S) };
        // a pointer to a folder that is not there (a cleaned app data folder) falls back without a word
        if bundle_dir(&dir, &u.current.version).is_none() && u.current.version != "embedded" {
            shared.log(&format!("ui: the pointer names interface {} but its folder is gone; the built-in interface serves", u.current.version));
            u.current = Current { version: "embedded".into(), installed_at: 0, confirmed: true };
            let _ = write_current(&dir, &u.current);
        }
        u.apply_pointer(shared);
        if u.current.version != "embedded" {
            shared.log(&format!("ui bundle {} active (installed {}){}", u.current.version, u.current.installed_at, if u.builtin { ", but the built-in interface is chosen" } else { "" }));
        }
        u.publish(shared);
        u
    }

    /// What the server serves: the bundle folder, or None for the embedded files.
    fn apply_pointer(&self, shared: &Arc<Shared>) {
        let d = if self.builtin { None } else { bundle_dir(&self.dir, &self.current.version) };
        shared.set_ui_dir(d);
    }

    pub fn active_version(&self) -> &str {
        &self.current.version
    }

    /// Called with every verified manifest: the `ui` entry or None (the kill switch).
    pub fn consider(&mut self, shared: &Arc<Shared>, entry: Option<&UiEntry>, engine: &str) {
        let Some(e) = entry else {
            self.entry = None;
            if self.current.version != "embedded" {
                shared.event("info", &format!("the interface channel was withdrawn; the built-in interface {} serves again", embedded_version()));
                shared.log(&format!("ui: no ui object in the manifest; interface {} retired", self.current.version));
                self.set_current(shared, Current { version: "embedded".into(), installed_at: crate::platform::unix_now(), confirmed: true });
            }
            self.publish(shared);
            return;
        };
        self.entry = Some(e.clone());
        if self.busy {
            return;
        }
        match decide(e, engine, embedded_version(), Some(&self.current.version), &self.bad, self.builtin) {
            Decision::Skip(why) => {
                if why != self.last_skip {
                    shared.log(&format!("ui: {why}"));
                    self.last_skip = why;
                }
            }
            Decision::Apply => {
                self.last_skip = String::new();
                self.busy = true;
                self.error = String::new();
                shared.event("info", &format!("interface {} is published: downloading ({} KB)", e.version, e.size / 1000));
                let e2 = e.clone();
                let dir = self.dir.clone();
                let shared2 = shared.clone();
                std::thread::spawn(move || {
                    let r = download_and_install(&e2, &dir);
                    shared2.send(Cmd::UiOta(Event::Installed(e2.version.clone(), r)));
                });
            }
        }
        self.publish(shared);
    }

    pub fn event(&mut self, shared: &Arc<Shared>, ev: Event) {
        self.busy = false;
        match ev {
            Event::Installed(version, Ok(_dir)) => {
                shared.event("ok", &format!("interface {version} installed; the window takes it at its next load"));
                self.set_current(shared, Current { version: version.clone(), installed_at: crate::platform::unix_now(), confirmed: false });
                shared.log(&format!("ui bundle {version} active (installed {}), awaiting the page's health ping", self.current.installed_at));
            }
            Event::Installed(version, Err(e)) => {
                self.mark_bad(shared, &version, &e);
            }
        }
        self.publish(shared);
    }

    fn set_current(&mut self, shared: &Arc<Shared>, c: Current) {
        self.current = c;
        if let Err(e) = write_current(&self.dir, &self.current) {
            shared.log(&format!("ui: could not write the pointer: {e}"));
        }
        self.waiting = None;
        self.apply_pointer(shared);
    }

    fn mark_bad(&mut self, shared: &Arc<Shared>, version: &str, why: &str) {
        if !self.bad.iter().any(|b| b == version) {
            self.bad.push(version.to_string());
            write_bad(&self.dir, &self.bad);
        }
        self.error = why.to_string();
        shared.event("error", &format!("interface {version} was refused: {why}. The built-in interface serves"));
        shared.log(&format!("ui: {version} marked bad: {why}"));
        if self.current.version == version {
            self.set_current(shared, Current { version: "embedded".into(), installed_at: crate::platform::unix_now(), confirmed: true });
        }
    }

    /// The server served index.html from a bundle: an unconfirmed one starts the health wait.
    pub fn page_loaded(&mut self, version: &str, now: Instant) {
        if version == self.current.version && !self.current.confirmed && self.waiting.is_none() {
            self.waiting = Some((version.to_string(), now));
        }
    }

    /// The page's ping after its first paint, or the error it caught before it.
    pub fn health(&mut self, shared: &Arc<Shared>, error: Option<&str>) {
        let Some((version, _)) = self.waiting.clone() else { return };
        match error {
            None => {
                self.waiting = None;
                self.current.confirmed = true;
                let _ = write_current(&self.dir, &self.current);
                shared.log(&format!("ui bundle {version} confirmed by the page"));
                self.publish(shared);
            }
            Some(e) => {
                self.mark_bad(shared, &version, &format!("a script error on first paint: {}", e.chars().take(200).collect::<String>()));
                self.publish(shared);
            }
        }
    }

    /// The wait ran out: the page never confirmed the bundle.
    pub fn tick(&mut self, shared: &Arc<Shared>, now: Instant) {
        if let Some((version, since)) = self.waiting.clone() {
            if now.duration_since(since) >= self.health_wait {
                self.mark_bad(shared, &version, &format!("the page did not answer within {} s of loading it", self.health_wait.as_secs()));
                self.publish(shared);
            }
        }
    }

    pub fn set_builtin(&mut self, shared: &Arc<Shared>, on: bool) {
        self.builtin = on;
        {
            let mut s = shared.settings.lock().unwrap();
            s.ui_builtin = on;
            s.save(&shared.settings_path);
        }
        shared.state.lock().unwrap().settings.ui_builtin = on;
        self.waiting = None;
        self.apply_pointer(shared);
        shared.event("info", if on { "the built-in interface serves from the next page load" } else { "the over-the-air interface serves again when one is active" });
        self.publish(shared);
    }

    /// Settings > Interface: what serves, from where, since when.
    pub fn publish(&self, shared: &Arc<Shared>) {
        let mut st = shared.state.lock().unwrap();
        let u = &mut st.ui;
        let ota_active = !self.builtin && bundle_dir(&self.dir, &self.current.version).is_some();
        u.embedded_version = embedded_version().into();
        u.active_version = if ota_active { self.current.version.clone() } else { embedded_version().into() };
        u.source = if ota_active { "ota".into() } else { "embedded".into() };
        u.installed_at = if ota_active { self.current.installed_at as f64 } else { 0.0 };
        u.confirmed = !ota_active || self.current.confirmed;
        u.published_version = self.entry.as_ref().map(|e| e.version.clone()).unwrap_or_default();
        u.busy = self.busy;
        u.error = self.error.clone();
        u.bad = self.bad.clone();
        u.builtin = self.builtin;
        u.note = self.last_skip.clone();
    }
}

/// The download thread: curl with resume into <dir>/<version>.tar.gz, then install_bundle.
fn download_and_install(e: &UiEntry, dir: &Path) -> Result<PathBuf, String> {
    let _ = std::fs::create_dir_all(dir);
    let file = dir.join(format!("{}.tar.gz", e.version));
    let part = dir.join(format!("{}.tar.gz.part", e.version));
    let mut c = std::process::Command::new(crate::platform::tool("curl"));
    c.args(["-fsSL", "--max-time", "300", "-C", "-", "-o", &part.display().to_string(), &e.url]);
    let out = crate::detect::run_timeout(&mut c, None, Duration::from_secs(320)).ok_or("curl is not available")?;
    let t = out.trim();
    if !t.is_empty() && !part.is_file() {
        return Err(format!("interface {}: {}", e.version, t.lines().last().unwrap_or("curl failed")));
    }
    std::fs::rename(&part, &file).map_err(|er| er.to_string())?;
    let r = install_bundle(e, &file, dir, crate::edition::ota_key());
    if r.is_err() {
        let _ = std::fs::remove_file(&file);
    }
    r
}

/// The content type a served bundle file gets (the same list the embedded files use).
pub fn content_type(rel: &str) -> &'static str {
    if rel.ends_with(".html") { "text/html; charset=utf-8" } else if rel.ends_with(".css") { "text/css; charset=utf-8" } else if rel.ends_with(".js") { "application/javascript; charset=utf-8" } else if rel.ends_with(".svg") { "image/svg+xml" } else if rel.ends_with(".woff2") { "font/woff2" } else { "text/plain; charset=utf-8" }
}

#[cfg(test)]
mod tests {
    use super::*;
    use ed25519_dalek::{Signer, SigningKey};

    fn entry(version: &str, min_engine: &str) -> UiEntry {
        UiEntry { version: version.into(), sha256: "ab".repeat(32), size: 1, url: "https://dl.igneum.network/dl/x/ui/igneum-ui-1.0.1.tar.gz".into(), min_engine: min_engine.into(), signature: "cd".repeat(64) }
    }

    #[test]
    fn the_decision_covers_every_reason_to_skip() {
        let e = entry("1.0.1", "0.1.19");
        assert_eq!(decide(&e, "0.1.19", "1.0.0", Some("embedded"), &[], false), Decision::Apply);
        assert_eq!(decide(&e, "0.1.20", "1.0.0", Some("1.0.0"), &[], false), Decision::Apply);
        assert!(matches!(decide(&e, "0.1.18", "1.0.0", Some("embedded"), &[], false), Decision::Skip(w) if w.contains("needs engine 0.1.19")), "a too-new min_engine is ignored");
        assert!(matches!(decide(&e, "0.1.19", "1.0.0", Some("1.0.1"), &[], false), Decision::Skip(w) if w.contains("is active")));
        assert!(matches!(decide(&e, "0.1.19", "1.0.1", Some("embedded"), &[], false), Decision::Skip(w) if w.contains("built-in one")));
        assert!(matches!(decide(&e, "0.1.19", "1.0.0", Some("embedded"), &["1.0.1".to_string()], false), Decision::Skip(w) if w.contains("failed here before")));
        assert!(matches!(decide(&e, "0.1.19", "1.0.0", Some("embedded"), &[], true), Decision::Skip(w) if w.contains("chosen in Settings")));
    }

    /// A bundle folder, packed with the system tar, hashed and signed with a throwaway key.
    fn make_bundle(root: &Path, version: &str, with_index: bool, sk: &SigningKey) -> (UiEntry, PathBuf) {
        let src = root.join("src");
        let _ = std::fs::remove_dir_all(&src);
        std::fs::create_dir_all(src.join("fonts")).unwrap();
        if with_index {
            std::fs::write(src.join("index.html"), "<!doctype html><title>x</title>").unwrap();
        }
        std::fs::write(src.join("app.js"), "var x = 1;").unwrap();
        std::fs::write(src.join("app.css"), "body{}").unwrap();
        std::fs::write(src.join("VERSION"), format!("{version}\n")).unwrap();
        std::fs::write(src.join("fonts/IBMPlexMono-400.woff2"), b"wOF2").unwrap();
        let tar = root.join(format!("igneum-ui-{version}.tar.gz"));
        let mut c = std::process::Command::new("tar");
        c.args(["-czf", &tar.display().to_string(), "-C", &src.display().to_string(), "."]);
        let ok = crate::platform::quiet(&mut c).status().unwrap().success();
        assert!(ok, "tar packs the fixture");
        let sha = manifest::sha256_file(&tar).unwrap();
        let size = std::fs::metadata(&tar).unwrap().len();
        let sig = manifest::hex_encode(&sk.sign(&manifest::ui_sign_bytes(version, &sha, "0.1.19")).to_bytes());
        (UiEntry { version: version.into(), sha256: sha, size, url: "https://dl.igneum.network/dl/x/ui/x.tar.gz".into(), min_engine: "0.1.19".into(), signature: sig }, tar)
    }

    fn tmp(name: &str) -> PathBuf {
        let d = std::env::temp_dir().join(format!("igneum-uiota-{name}-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&d);
        std::fs::create_dir_all(&d).unwrap();
        d
    }

    #[test]
    fn a_good_bundle_installs_and_the_pointer_swaps_atomically() {
        let root = tmp("good");
        let sk = SigningKey::from_bytes(&[7u8; 32]);
        let pk = manifest::hex_encode(sk.verifying_key().as_bytes());
        let (e, tar) = make_bundle(&root, "1.0.1", true, &sk);
        let dir = root.join("ui");
        let dest = install_bundle(&e, &tar, &dir, &pk).expect("installs");
        assert_eq!(dest, dir.join("1.0.1"));
        assert!(bundle_dir(&dir, "1.0.1").is_some());
        assert!(!dir.join("1.0.1.new").exists(), "the staging folder is gone");
        write_current(&dir, &Current { version: "1.0.1".into(), installed_at: 5, confirmed: false }).unwrap();
        assert_eq!(read_current(&dir).version, "1.0.1");
        assert!(!dir.join("current.json.tmp").exists());
        assert_eq!(content_type("app.js"), "application/javascript; charset=utf-8");
        assert_eq!(bundle_dir(&dir, "embedded"), None);
    }

    #[test]
    fn a_bad_signature_is_refused_before_anything_is_unpacked() {
        let root = tmp("badsig");
        let sk = SigningKey::from_bytes(&[7u8; 32]);
        let other = manifest::hex_encode(SigningKey::from_bytes(&[8u8; 32]).verifying_key().as_bytes());
        let (e, tar) = make_bundle(&root, "1.0.2", true, &sk);
        let dir = root.join("ui");
        let err = install_bundle(&e, &tar, &dir, &other).unwrap_err();
        assert!(err.contains("signature does not verify"), "{err}");
        assert!(!dir.join("1.0.2").exists());
        // a tampered hash is caught first
        let mut t = e.clone();
        t.sha256 = "00".repeat(32);
        let err = install_bundle(&t, &tar, &dir, &manifest::hex_encode(sk.verifying_key().as_bytes())).unwrap_err();
        assert!(err.contains("sha256 mismatch"), "{err}");
        // a wrong size too
        let mut t = e.clone();
        t.size += 1;
        assert!(install_bundle(&t, &tar, &dir, &manifest::hex_encode(sk.verifying_key().as_bytes())).unwrap_err().contains("bytes"));
    }

    #[test]
    fn a_broken_bundle_without_index_html_is_refused_and_leaves_nothing_behind() {
        let root = tmp("broken");
        let sk = SigningKey::from_bytes(&[7u8; 32]);
        let pk = manifest::hex_encode(sk.verifying_key().as_bytes());
        let (e, tar) = make_bundle(&root, "1.0.3", false, &sk);
        let dir = root.join("ui");
        let err = install_bundle(&e, &tar, &dir, &pk).unwrap_err();
        assert!(err.contains("no index.html"), "{err}");
        assert!(!dir.join("1.0.3").exists() && !dir.join("1.0.3.new").exists());
    }

    #[test]
    fn a_renamed_bundle_is_refused_by_its_version_file() {
        let root = tmp("renamed");
        let sk = SigningKey::from_bytes(&[7u8; 32]);
        let pk = manifest::hex_encode(sk.verifying_key().as_bytes());
        let (mut e, tar) = make_bundle(&root, "1.0.4", true, &sk);
        e.version = "1.0.5".into();
        e.signature = manifest::hex_encode(&sk.sign(&manifest::ui_sign_bytes("1.0.5", &e.sha256, "0.1.19")).to_bytes());
        let err = install_bundle(&e, &tar, &root.join("ui"), &pk).unwrap_err();
        assert!(err.contains("VERSION file says '1.0.4'"), "{err}");
    }

    #[test]
    fn the_health_wait_rolls_back_to_the_embedded_interface_and_marks_the_version_bad() {
        // the state machine without threads: a UiOta over a temp dir, driven by hand
        let root = tmp("health");
        let dir = root.join("ui");
        std::fs::create_dir_all(dir.join("1.0.6")).unwrap();
        for f in REQUIRED {
            std::fs::write(dir.join("1.0.6").join(f), "x").unwrap();
        }
        write_current(&dir, &Current { version: "1.0.6".into(), installed_at: 1, confirmed: false }).unwrap();
        let mut u = UiOta { dir: dir.clone(), current: read_current(&dir), bad: vec![], builtin: false, busy: false, entry: None, waiting: None, error: String::new(), last_skip: String::new(), health_wait: Duration::from_secs(10) };
        let t0 = Instant::now();
        u.page_loaded("1.0.6", t0);
        assert!(u.waiting.is_some());
        // a ping in time confirms
        let shared = crate::engine::Shared::for_tests(root.join("data"));
        u.health(&shared, None);
        assert!(u.current.confirmed && u.waiting.is_none());
        assert_eq!(read_current(&dir).confirmed, true);
        // a second bundle that never answers
        std::fs::create_dir_all(dir.join("1.0.7")).unwrap();
        for f in REQUIRED {
            std::fs::write(dir.join("1.0.7").join(f), "x").unwrap();
        }
        u.set_current(&shared, Current { version: "1.0.7".into(), installed_at: 2, confirmed: false });
        u.page_loaded("1.0.7", t0);
        u.tick(&shared, t0 + Duration::from_secs(9));
        assert_eq!(u.current.version, "1.0.7", "still waiting inside the window");
        u.tick(&shared, t0 + Duration::from_secs(10));
        assert_eq!(u.current.version, "embedded", "rolled back");
        assert_eq!(read_current(&dir).version, "embedded");
        assert_eq!(read_bad(&dir), vec!["1.0.7".to_string()]);
        assert!(shared.ui_dir().is_none(), "the server serves the embedded files again");
        // a script error on first paint rolls back the same way
        std::fs::create_dir_all(dir.join("1.0.8")).unwrap();
        for f in REQUIRED {
            std::fs::write(dir.join("1.0.8").join(f), "x").unwrap();
        }
        u.set_current(&shared, Current { version: "1.0.8".into(), installed_at: 3, confirmed: false });
        u.page_loaded("1.0.8", t0);
        u.health(&shared, Some("TypeError: x is not a function"));
        assert_eq!(u.current.version, "embedded");
        assert!(u.bad.contains(&"1.0.8".to_string()));
        // and the decision never applies it again
        let e = entry("1.0.8", "0.1.19");
        assert!(matches!(decide(&e, "0.1.19", "1.0.0", Some("embedded"), &u.bad, false), Decision::Skip(_)));
    }
}
