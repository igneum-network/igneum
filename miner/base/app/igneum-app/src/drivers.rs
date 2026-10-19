//! Driver check (branch driver-check, 7 October 2026; the founder: "can we package the drivers with the miner? for all
//! cards? and the system knows which to install if not present"). The answer given: not bundled (size, vendor
//! licences, staleness), but detected and installed on one click.
//!
//! The manifest carries a per-vendor table (`drivers`, signed with the rest): the version the worker needs at least,
//! the version on offer, the vendor's own download URL, size, sha256 and the page the hash was read from, the
//! installer's silent arguments and the exit codes that mean "restart required". The app never ships a driver: the
//! table rides the manifest (ota.rs writes `<app data>/drivers.json`), so a new vendor release is a manifest publish.
//!
//! At every detection each card's driver version (nvidia-smi's for NVIDIA, Windows' DriverVersion for AMD and Intel)
//! is compared with the table; a missing or old driver puts an offer on the card's row ("Install the NVIDIA driver
//! 581.57, 650 MB"). The click downloads from the vendor's server (curl with resume), checks size, sha256 and the
//! Authenticode signature (the signer must be the vendor), then runs the installer through ONE elevated prompt
//! (platform::elevated_command, the the first Windows machine driver job's fetch, verify and -s shape), reports "restart required" with a
//! Restart now button and never restarts by itself. Nothing else pauses: the miners keep mining through it.
//! macOS: no driver step (the row says so). Linux and HiveOS: a line naming the package, no installer.
//!
//! Dry run (`IGNEUM_DRIVER_DRY_RUN=1`, or `dry_run: true` in the table): the download and every check run, the
//! installer does not: the install step reports what it would have run and exit 0. The tests feed the table a
//! 127.0.0.1 URL served by a std TcpListener (the mocked vendor response).

use crate::engine::{Cmd, Shared};
use std::path::{Path, PathBuf};
use std::process::Command;
use std::sync::Arc;
use std::time::Duration;

pub use crate::drivertable::{exit_meaning, offer_for, platform_word, DriverState, Offer, Table, VendorEntry};
#[cfg(windows)]
use crate::drivertable::authenticode_verdict;

/// The install thread's reports.
pub enum Event {
    Progress(f64, String),
    /// the installer ran: its exit code and whether that means a restart
    Installed(Result<(i64, bool, String), String>),
}

fn curl(args: &[&str], limit: Duration) -> Result<(), String> {
    let mut c = Command::new(crate::platform::tool("curl"));
    c.args(args);
    let out = crate::detect::run_timeout(&mut c, None, limit).ok_or("curl is not available")?;
    let t = out.trim().to_string();
    if t.is_empty() { Ok(()) } else { Err(t.lines().last().unwrap_or("curl failed").to_string()) }
}

/// Downloads the vendor's file with resume into `dir`, checks size and sha256, renames `.part` to the final name.
pub fn download(e: &VendorEntry, dir: &Path) -> Result<PathBuf, String> {
    let name = e.url.rsplit('/').next().filter(|n| !n.is_empty() && !n.contains("..")).unwrap_or("driver.exe").to_string();
    let final_path = dir.join(&name);
    let part = dir.join(format!("{name}.part"));
    std::fs::create_dir_all(dir).map_err(|x| format!("cannot make {}: {x}", dir.display()))?;
    if final_path.is_file() && std::fs::metadata(&final_path).map(|m| m.len()).unwrap_or(0) == e.size && crate::manifest::sha256_file(&final_path).map(|s| s == e.sha256).unwrap_or(false) {
        return Ok(final_path);
    }
    let _ = std::fs::remove_file(&final_path);
    let have = std::fs::metadata(&part).map(|m| m.len()).unwrap_or(0);
    if have > e.size {
        let _ = std::fs::remove_file(&part);
    }
    if have != e.size {
        // drivers.amd.com answers 403 without an amd.com Referer (the mirror box, 7 October 2026): the row names one
        let mut args = vec!["-fsSL", "--retry", "3", "--retry-delay", "5", "-C", "-", "--max-time", "7200", "-A", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) Ember"];
        if !e.referer.is_empty() {
            args.push("-e");
            args.push(&e.referer);
        }
        let part_s = part.display().to_string();
        args.extend(["-o", &part_s, &e.url]);
        curl(&args, Duration::from_secs(7260))?;
    }
    let got = std::fs::metadata(&part).map(|m| m.len()).unwrap_or(0);
    if got != e.size {
        let _ = std::fs::remove_file(&part);
        return Err(format!("size mismatch: got {got} bytes, the table says {}", e.size));
    }
    let sum = crate::manifest::sha256_file(&part).map_err(|x| x.to_string())?;
    if sum != e.sha256 {
        let _ = std::fs::remove_file(&part);
        return Err(format!("sha256 mismatch: the file is not the one the table names (page {})", e.hash_source));
    }
    std::fs::rename(&part, &final_path).map_err(|x| x.to_string())?;
    Ok(final_path)
}

#[cfg(windows)]
fn authenticode(path: &Path, signer: &str) -> Result<String, String> {
    let ps = format!("$s = Get-AuthenticodeSignature -LiteralPath '{}'; Write-Output ('status=' + $s.Status); Write-Output ('subject=' + $s.SignerCertificate.Subject)", path.display().to_string().replace('\'', "''"));
    let out = crate::detect::run_timeout(Command::new(crate::platform::tool("powershell")).args(["-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", &ps]), None, Duration::from_secs(60)).ok_or("PowerShell did not answer the signature check")?;
    authenticode_verdict(&out, signer)
}

#[cfg(not(windows))]
fn authenticode(_path: &Path, _signer: &str) -> Result<String, String> {
    Err("the installer runs on Windows only".into())
}

/// The click: download, verify, run elevated (one prompt), report. Runs on its own thread; the miners keep mining.
pub fn start_install(shared: &Arc<Shared>, e: VendorEntry, dir: PathBuf, dry_run: bool) {
    let shared2 = shared.clone();
    std::thread::spawn(move || {
        shared2.send(Cmd::Driver(Event::Progress(0.05, format!("downloading {} ({} MB) from {}", e.version, (e.size + 512 * 1024) / (1024 * 1024), host_of(&e.url)))));
        let file = match download(&e, &dir) {
            Ok(f) => f,
            Err(x) => {
                shared2.send(Cmd::Driver(Event::Installed(Err(format!("download: {x}")))));
                return;
            }
        };
        shared2.send(Cmd::Driver(Event::Progress(0.6, "size and sha256 match the table; checking the signature".into())));
        if !dry_run {
            if let Err(x) = authenticode(&file, &e.signer) {
                shared2.send(Cmd::Driver(Event::Installed(Err(x))));
                return;
            }
        }
        let unattended = !dry_run && cfg!(windows) && crate::powertask::registered();
        shared2.send(Cmd::Driver(Event::Progress(0.7, format!("{}installing {} {}: {}", if dry_run { "dry run: " } else { "" }, e.vendor.to_ascii_uppercase(), e.version, if unattended { "through the Igneum Power Helper task, no prompt" } else { "Windows asks for permission once" }))));
        if dry_run {
            shared2.send(Cmd::Driver(Event::Installed(Ok((0, false, format!("dry run: would run {} {}", file.display(), e.args.join(" ")))))));
            return;
        }
        // an earlier release (src/driverinstall.rs, the rights step driver-install-task): with the Power Helper task registered the
        // elevated helper runs the installer unattended after its own verification of the file; no prompt
        if unattended {
            if let Some(r) = crate::driverinstall::via_helper(&e.vendor) {
                let r = r.map(|(code, _)| { let (reboot, text) = exit_meaning(code, &e); (code, reboot, text) });
                shared2.send(Cmd::Driver(Event::Installed(r)));
                return;
            }
        }
        let args = e.args.join(" ");
        let mut c = crate::platform::elevated_command(&file.display().to_string(), &args);
        let out = crate::detect::run_timeout(&mut c, None, Duration::from_secs(1800));
        let code = out.as_deref().and_then(|t| t.lines().rev().find_map(|l| l.trim().parse::<i64>().ok())).unwrap_or(-1);
        // elevated_ps_line prints nothing on success and exits with the installer's code; run_timeout only gives
        // stdout, so the exit code is read from the wrapper's own echo below
        let code = exit_code_of(&file, &args, code);
        let (reboot, text) = exit_meaning(code, &e);
        shared2.send(Cmd::Driver(Event::Installed(Ok((code, reboot, text)))));
    });
}

/// The elevated wrapper's exit code: `elevated_ps_line` exits with the installer's code, which run_timeout does not
/// return; so the installer is run through a second form that echoes the code on its last line.
#[cfg(windows)]
fn exit_code_of(file: &Path, args: &str, _seen: i64) -> i64 {
    let line = format!("{}; Write-Output ('exit=' + $LASTEXITCODE)", crate::platform::elevated_ps_line(&file.display().to_string(), args).trim_end_matches("exit $p.ExitCode").to_string() + "$global:LASTEXITCODE = $p.ExitCode");
    let mut c = Command::new(crate::platform::tool("powershell"));
    c.args(["-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", &line]);
    crate::platform::quiet(&mut c);
    let out = crate::detect::run_timeout(&mut c, None, Duration::from_secs(1800)).unwrap_or_default();
    out.lines().rev().find_map(|l| l.trim().strip_prefix("exit=").and_then(|v| v.parse::<i64>().ok())).unwrap_or(-1)
}

#[cfg(not(windows))]
fn exit_code_of(_file: &Path, _args: &str, seen: i64) -> i64 {
    seen
}

fn host_of(url: &str) -> String {
    url.split("//").nth(1).and_then(|r| r.split('/').next()).unwrap_or("the vendor").to_string()
}

/// "Restart now": a plain restart in 20 s (no elevation needed for the signed-in user); never called by the app itself.
pub fn restart_now() -> Result<(), String> {
    if !cfg!(windows) {
        return Err("restart from the app is for Windows only".into());
    }
    let root = std::env::var("SystemRoot").unwrap_or_else(|_| "C:\\Windows".into());
    let mut c = Command::new(format!("{root}\\System32\\shutdown.exe"));
    c.args(["/r", "/t", "20", "/c", "Ember: restarting to finish the driver install"]);
    crate::platform::quiet(&mut c);
    match crate::detect::run_timeout(&mut c, None, Duration::from_secs(20)) {
        Some(t) if t.trim().is_empty() => Ok(()),
        Some(t) => Err(t.trim().to_string()),
        None => Err("shutdown.exe did not answer".into()),
    }
}

#[cfg(test)]
mod download_tests {
    use super::*;

    /// The mocked vendor response: a std TcpListener on 127.0.0.1 serves the file; the right sha256 passes, a wrong
    /// one is refused and the part file is gone; a second call with the file in place downloads nothing.
    #[test]
    fn download_verifies_against_a_mocked_vendor_server() {
        use std::io::{Read, Write};
        let body: Vec<u8> = (0..100_000u32).map(|i| (i % 251) as u8).collect();
        let listener = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
        let port = listener.local_addr().unwrap().port();
        let served = body.clone();
        std::thread::spawn(move || {
            for stream in listener.incoming().take(3) {
                let mut s = stream.unwrap();
                let mut buf = [0u8; 4096];
                let _ = s.read(&mut buf);
                let head = format!("HTTP/1.1 200 OK\r\nContent-Length: {}\r\nContent-Type: application/octet-stream\r\nConnection: close\r\n\r\n", served.len());
                let _ = s.write_all(head.as_bytes());
                let _ = s.write_all(&served);
            }
        });
        let dir = std::env::temp_dir().join(format!("igneum-driver-test-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&dir);
        use sha2::Digest;
        let sum = format!("{:x}", sha2::Sha256::digest(&body));
        let mut e = VendorEntry { vendor: "intel".into(), version: "1.0".into(), min_version: "1.0".into(), url: format!("http://127.0.0.1:{port}/gfx_test.exe"), size: body.len() as u64, sha256: sum.clone(), args: vec!["-s".into()], signer: "Intel".into(), ..Default::default() };
        let f = download(&e, &dir).expect("the right sha256 passes");
        assert_eq!(f.file_name().unwrap(), "gfx_test.exe");
        assert_eq!(std::fs::read(&f).unwrap(), body);
        assert!(download(&e, &dir).is_ok(), "the file in place is kept without a second download");
        let _ = std::fs::remove_file(&f);
        e.sha256 = "0".repeat(64);
        let err = download(&e, &dir).unwrap_err();
        assert!(err.contains("sha256 mismatch"), "{err}");
        assert!(!dir.join("gfx_test.exe.part").exists() && !dir.join("gfx_test.exe").exists());
        e.sha256 = sum;
        e.size = 7;
        assert!(download(&e, &dir).unwrap_err().contains("size mismatch"));
        let _ = std::fs::remove_dir_all(&dir);
    }
}
