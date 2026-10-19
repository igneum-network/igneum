//! The fleet repair (the project's ruling of 17:2x UK, 12 October 2026): after every install and over-the-air update the
//! Windows installer runs `igneum-app --fleet-repair` as the signed-in user. The app fetches the fleet manifest from
//! the release store's fleet path, verifies its signature under the automatic key against the compiled key list (the
//! pointer's own path: an SSH signature in the release namespace, the supersession rule judged on written_utc), and
//! when this machine is named in it fetches the files the manifest lists, checks each one's sha256 and size, writes
//! them under the user's .igneum-bench folder and runs the named script as the user. A machine not named fetches
//! nothing; a store without a manifest is "no fleet manifest" and exit 0. The installer carries no fleet bytes: they
//! live on the store (the reliability team's), never in the tree.
use crate::manifest;
use serde::Deserialize;
use std::path::{Path, PathBuf};
use std::process::Command;
use std::time::Duration;

/// The fleet path under the release store.
pub const FLEET_BASE: &str = "https://build.igneum.network/releases/ember/fleet";
/// The folder the bench agent and its repair live in, under the user's profile.
pub const FLEET_DIR: &str = ".igneum-bench";

#[derive(Deserialize)]
struct FleetManifest {
    #[serde(default)]
    written_utc: String,
    /// Computer names (case-insensitive) or the app's own 16-hex machine ids.
    #[serde(default)]
    machines: Vec<String>,
    #[serde(default)]
    files: Vec<FleetFile>,
    /// The file to run after the files are in (a .ps1 under the fleet folder); empty = place only.
    #[serde(default)]
    run: String,
}

#[derive(Deserialize)]
struct FleetFile {
    /// The file's name under the fleet folder (one plain element).
    name: String,
    sha256: String,
    #[serde(default)]
    size: u64,
    /// Where the file sits on the release store: its full url under RELEASE_BASE (the signer admits files of signed
    /// sets only, so the fleet files ride a version's set, such as .../releases/ember/0.2.4/igneum-bench-repair.ps1);
    /// empty = the fleet path beside the manifest.
    #[serde(default)]
    url: String,
    /// The same as a path under the store's releases root (the served manifest of 12 October 2026 names
    /// fleet/1.0.1/<name>, the signed set the bench files sit in); read when url is empty.
    #[serde(default)]
    path: String,
}

/// The store's releases root, the parent of RELEASE_BASE (".../releases/").
fn releases_root() -> String {
    let b = crate::config::RELEASE_BASE;
    match b.rfind('/') {
        Some(i) => format!("{}/", &b[..i]),
        None => format!("{b}/"),
    }
}

/// The url a listed file is fetched from: its own, when it is under the release store and made of plain elements;
/// else the fleet path beside the manifest. Any other url is refused.
fn file_url(f: &FleetFile) -> Result<String, String> {
    if f.url.is_empty() && f.path.is_empty() {
        return Ok(format!("{FLEET_BASE}/{}", f.name));
    }
    let root = releases_root();
    let url = if f.url.is_empty() { format!("{root}{}", f.path.trim_start_matches('/')) } else { f.url.clone() };
    let rest = url.strip_prefix(&root).ok_or_else(|| format!("{}: its url is not under the release store", f.name))?;
    if rest.is_empty() || !rest.split('/').all(plain_name) {
        return Err(format!("{}: its path is not a plain store path", f.name));
    }
    Ok(url)
}

/// A file name the manifest may use: one path element, no separators, no dot-prefix.
fn plain_name(n: &str) -> bool {
    !n.is_empty() && !n.starts_with('.') && n.chars().all(|c| c.is_ascii_alphanumeric() || c == '.' || c == '-' || c == '_')
}

fn fleet_dir() -> Option<PathBuf> {
    let home = std::env::var_os("USERPROFILE").or_else(|| std::env::var_os("HOME"))?;
    Some(PathBuf::from(home).join(FLEET_DIR))
}

/// This machine's names: the computer name (lower-cased) and the app's machine id.
fn this_machine(machine_id: &str) -> Vec<String> {
    let mut v = vec![machine_id.to_ascii_lowercase()];
    if let Ok(n) = std::env::var("COMPUTERNAME") {
        v.push(n.to_ascii_lowercase());
    }
    if let Ok(n) = std::env::var("HOSTNAME") {
        v.push(n.to_ascii_lowercase());
    }
    v
}

/// Fetches the manifest and its signature into `work`, verifies them, parses. Err carries the words for the log.
fn fetch_manifest(work: &Path) -> Result<FleetManifest, String> {
    let mf = work.join("fleet-manifest.json");
    let sf = work.join("fleet-manifest.json.sig");
    let _ = std::fs::remove_file(&mf);
    let _ = std::fs::remove_file(&sf);
    crate::ota::curl(&["-fsSL", "--max-time", "20", "-o", &mf.display().to_string(), &format!("{FLEET_BASE}/manifest.json")], Duration::from_secs(25))
        .map_err(|e| if e.contains("404") { "no fleet manifest".to_string() } else { format!("fleet manifest: {e}") })?;
    crate::ota::curl(&["-fsSL", "--max-time", "20", "-o", &sf.display().to_string(), &format!("{FLEET_BASE}/manifest.json.sig")], Duration::from_secs(25))
        .map_err(|e| format!("fleet manifest signature: {e}"))?;
    let bytes = std::fs::read(&mf).map_err(|e| e.to_string())?;
    let sig = std::fs::read_to_string(&sf).map_err(|e| e.to_string())?;
    // the release root: the pinned key, or any key of the compiled rotation list (manifest::verify_sshsig)
    manifest::verify_signature(&bytes, sig.trim(), crate::edition::ota_key()).map_err(|e| format!("fleet manifest refused: {e}"))?;
    let m: FleetManifest = serde_json::from_slice(&bytes).map_err(|e| format!("fleet manifest is not the form: {e}"))?;
    if let Some(signer) = manifest::release_signer_of(sig.trim()) {
        manifest::superseded_check(&signer, &m.written_utc).map_err(|e| format!("fleet manifest refused: {e}"))?;
    }
    Ok(m)
}

/// One listed file: fetched to `<dir>/<name>.part`, its sha256 and size checked, renamed into place; its .sig is not
/// read (the manifest's own signature covers the digests).
fn fetch_file(f: &FleetFile, dir: &Path) -> Result<(), String> {
    if !plain_name(&f.name) {
        return Err(format!("the manifest names a file this app will not write: {:?}", f.name));
    }
    let url = file_url(f)?;
    let part = dir.join(format!("{}.part", f.name));
    let _ = std::fs::remove_file(&part);
    crate::ota::curl(&["-fsSL", "--max-time", "120", "-o", &part.display().to_string(), &url], Duration::from_secs(130))
        .map_err(|e| format!("{}: {e}", f.name))?;
    let got = manifest::sha256_file(&part).map_err(|e| e.to_string())?;
    let len = std::fs::metadata(&part).map(|m| m.len()).unwrap_or(0);
    if got != f.sha256.to_ascii_lowercase() || (f.size != 0 && len != f.size) {
        let _ = std::fs::remove_file(&part);
        return Err(format!("{}: the file on the store is not the one the manifest names (sha256 {} of {} bytes)", f.name, &got[..16], len));
    }
    let dest = dir.join(&f.name);
    let prev = dir.join(format!("{}.prev", f.name));
    if dest.is_file() {
        let _ = std::fs::remove_file(&prev);
        let _ = std::fs::rename(&dest, &prev);
    }
    std::fs::rename(&part, &dest).map_err(|e| format!("{}: {e}", f.name))?;
    Ok(())
}

/// The whole step; the line returned is printed by main and lands in the installer's log. Never an error exit: an
/// install does not fail on the fleet path.
pub fn repair(machine_id: &str, work: &Path) -> String {
    let _ = std::fs::create_dir_all(work);
    let m = match fetch_manifest(work) {
        Ok(m) => m,
        Err(e) => return e,
    };
    let mine = this_machine(machine_id);
    let named = m.machines.iter().any(|x| mine.iter().any(|y| y == &x.to_ascii_lowercase()));
    if !named {
        return "this machine is not in the fleet manifest; nothing fetched".into();
    }
    let Some(dir) = fleet_dir() else { return "no user profile folder".into() };
    if let Err(e) = std::fs::create_dir_all(&dir) {
        return format!("{}: {e}", dir.display());
    }
    for f in &m.files {
        if let Err(e) = fetch_file(f, &dir) {
            return e;
        }
    }
    if m.run.is_empty() {
        return format!("{} fleet file(s) placed under {}", m.files.len(), dir.display());
    }
    if !plain_name(&m.run) || !m.files.iter().any(|f| f.name == m.run) {
        return format!("the manifest's run file {:?} is not one of its files; placed only", m.run);
    }
    let script = dir.join(&m.run);
    #[cfg(windows)]
    let started = Command::new("powershell.exe").args(["-NoProfile", "-ExecutionPolicy", "Bypass", "-File"]).arg(&script).current_dir(&dir).spawn();
    #[cfg(not(windows))]
    let started = Command::new("sh").arg(&script).current_dir(&dir).spawn();
    match started {
        Ok(_) => format!("{} fleet file(s) placed; {} started as the user", m.files.len(), m.run),
        Err(e) => format!("{} placed; {} did not start: {e}", m.files.len(), m.run),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_file_name_is_one_plain_element() {
        assert!(plain_name("igneum-bench-repair.ps1"));
        assert!(plain_name("igneum-bench-agent.ps1.sha256"));
        assert!(!plain_name("..\\evil.ps1"));
        assert!(!plain_name("sub/dir.ps1"));
        assert!(!plain_name(".hidden"));
        assert!(!plain_name(""));
    }

    #[test]
    fn a_listed_file_comes_from_the_store_or_the_fleet_path() {
        let f = |name: &str, url: &str, path: &str| FleetFile { name: name.into(), sha256: String::new(), size: 0, url: url.into(), path: path.into() };
        assert_eq!(file_url(&f("a.ps1", "", "")).unwrap(), format!("{FLEET_BASE}/a.ps1"));
        let ok = format!("{}/0.2.4/igneum-bench-repair.ps1", crate::config::RELEASE_BASE);
        assert_eq!(file_url(&f("igneum-bench-repair.ps1", &ok, "")).unwrap(), ok);
        // the served form of 12 October 2026: a path under the releases root
        assert_eq!(file_url(&f("igneum-bench-repair.ps1", "", "fleet/1.0.1/igneum-bench-repair.ps1")).unwrap(), "https://build.igneum.network/releases/fleet/1.0.1/igneum-bench-repair.ps1");
        assert!(file_url(&f("a.ps1", "https://example.invalid/a.ps1", "")).is_err(), "another host is refused");
        assert!(file_url(&f("a.ps1", &format!("{}/../x/a.ps1", crate::config::RELEASE_BASE), "")).is_err(), "a dot path is refused");
        assert!(file_url(&f("a.ps1", "", "fleet/../a.ps1")).is_err(), "a dot path is refused");
    }

    #[test]
    fn the_machine_is_named_by_id_or_computer_name() {
        let mine = this_machine("0123456789abcdef");
        assert!(mine.contains(&"0123456789abcdef".to_string()));
        let m: FleetManifest = serde_json::from_str(r#"{"written_utc":"2026-10-12T17:30:00Z","machines":["PC-ONE","0123456789ABCDEF"],"files":[],"run":""}"#).unwrap();
        assert!(m.machines.iter().any(|x| mine.iter().any(|y| y == &x.to_ascii_lowercase())));
        let other: FleetManifest = serde_json::from_str(r#"{"machines":["someone-else"]}"#).unwrap();
        assert!(!other.machines.iter().any(|x| mine.iter().any(|y| y == &x.to_ascii_lowercase())));
    }
}
