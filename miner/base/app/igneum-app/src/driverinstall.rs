//! The unattended driver install (the rights-at-install step `driver-install-task`, 7 October 2026, 19:5x UK; the founder's
//! rule that evening: no PC job may need a click or a UAC prompt, and the Arc's 9034 retry on the second Windows machine was cancelled for
//! it). What the installer registers once (src/rights.rs on boot-start-22 takes the id into RIGHTS): the Igneum Power
//! Helper task (RunLevel Highest, the app's own exe with `--power-helper`) with a two-hour execution limit, so the
//! elevated helper can run a vendor's driver installer to its end. Nothing else: no second task, no new firewall rule.
//!
//! The protocol, on the helper's one command file: `<seq> driver <vendor>` with a vendor WORD only (nvidia | amd |
//! intel). The helper, elevated, resolves everything else itself from the signed table the manifest left at
//! `<app data>/drivers.json` and the file the app downloaded into `<app data>/drivers/`: the size, the sha256 and the
//! Authenticode signer must match the table and the signer must be one of the three vendors, or nothing runs. So a
//! writer of cmd.txt can never choose what runs elevated (the Power Helper's rule since 6 October 2026). The helper
//! runs the installer with the table's silent arguments, keeps its heartbeat during the run (cap 45 minutes), and
//! writes `<seq> <vendor> exit <code> reboot <0|1>` to `driver-result.txt` in its folder. The app holds the vendor's
//! cards before it asks (engine::driver_install), reads the result, and a "restart required" exit is the Restart now
//! button: the app never restarts the machine by itself. When the task is not registered (an install before an earlier release
//! whose rights step has not run), the one-prompt path of src/drivers.rs stays as it was.

use std::path::{Path, PathBuf};
use std::time::{Duration, Instant};

/// The right's id and sentence for src/rights.rs RIGHTS (an id never changes meaning; a new need is a new id).
pub const RIGHT: (&str, &str) = ("driver-install-task", "the Igneum Power Helper task with a two-hour run limit: a vendor's driver installs unattended through it, the vendor's cards held, no prompt (src/driverinstall.rs)");
pub const RESULT_FILE: &str = "driver-result.txt";
pub const VENDORS: &[&str] = &["nvidia", "amd", "intel"];
/// The Authenticode subjects a driver installer may carry to run elevated (the table's `signer` must match one too).
pub const ALLOWED_SIGNERS: &[&str] = &["Intel Corporation", "NVIDIA Corporation", "Advanced Micro Devices"];
/// How long the helper waits for the installer, and how long the app waits for the helper's result line.
pub const INSTALL_CAP: Duration = Duration::from_secs(45 * 60);
pub const RESULT_WAIT: Duration = Duration::from_secs(50 * 60);

/// The installer's registration for this right: the Power Helper task as src/powertask.rs registers it, with the
/// execution limit raised from one hour to two (a 1 GB download that the app already did is not in it; the installer
/// itself runs 2 to 15 minutes, and the helper's own idle exit is 20 minutes).
pub fn register_script(exe: &Path) -> String {
    crate::powertask::register_script(exe).replace("-ExecutionTimeLimit (New-TimeSpan -Hours 1)", "-ExecutionTimeLimit (New-TimeSpan -Hours 2)")
}

pub fn vendor_ok(v: &str) -> bool {
    VENDORS.contains(&v)
}

/// The command line the app writes for the helper (the sequence from the one wire space).
pub fn command_line(seq: u64, vendor: &str) -> String {
    format!("{seq} driver {vendor}\n")
}

/// The file the helper runs for a vendor: the table's URL's last path segment inside the app's drivers folder.
pub fn file_for(e: &crate::drivertable::VendorEntry, drivers_dir: &Path) -> PathBuf {
    let name = e.url.rsplit('/').next().filter(|n| !n.is_empty() && !n.contains("..") && !n.contains('\\')).unwrap_or("driver.exe");
    drivers_dir.join(name)
}

/// The elevated check before anything runs: size and sha256 equal to the table, the signer one of the vendors and the
/// table's own. Pure, so the box tests it.
pub fn verify(e: &crate::drivertable::VendorEntry, size: u64, sha256: &str, signer_subject: &str) -> Result<(), String> {
    if size != e.size {
        return Err(format!("size {size} is not the table's {}", e.size));
    }
    if !sha256.eq_ignore_ascii_case(&e.sha256) {
        return Err("sha256 is not the table's: the file is not the one the manifest names".into());
    }
    if !ALLOWED_SIGNERS.iter().any(|s| signer_subject.contains(s)) {
        return Err(format!("the signer '{signer_subject}' is not a driver vendor"));
    }
    if !e.signer.is_empty() && !signer_subject.contains(&e.signer) {
        return Err(format!("the signer '{signer_subject}' is not the table's '{}'", e.signer));
    }
    Ok(())
}

/// The helper's result line and its reading.
pub fn result_line(seq: u64, vendor: &str, code: i64, reboot: bool) -> String {
    format!("{seq} {vendor} exit {code} reboot {}\n", if reboot { 1 } else { 0 })
}
pub fn parse_result(text: &str, seq: u64) -> Option<(i64, bool)> {
    text.lines().rev().find_map(|l| {
        let p: Vec<&str> = l.split_whitespace().collect();
        match p.as_slice() {
            [s, _, "exit", c, "reboot", r] if s.parse::<u64>().ok() == Some(seq) => Some((c.parse().ok()?, *r == "1")),
            _ => None,
        }
    })
}

/// Inside the elevated helper: resolve, verify, run, report. `dir` is the helper's folder (<app data>/app/sweep).
pub fn run_in_helper(dir: &Path, seq: u64, vendor: &str, log: &dyn Fn(&str)) {
    let app_dir = dir.parent().map(|p| p.to_path_buf()).unwrap_or_else(|| dir.to_path_buf());
    let outcome = run_in_helper_inner(&app_dir, dir, vendor, log);
    let (code, reboot) = match outcome {
        Ok(v) => v,
        Err(e) => {
            log(&format!("{seq} driver {vendor}: refused: {e}"));
            (-2, false)
        }
    };
    let _ = std::fs::OpenOptions::new().append(true).create(true).open(dir.join(RESULT_FILE)).and_then(|mut f| {
        use std::io::Write;
        f.write_all(result_line(seq, vendor, code, reboot).as_bytes())
    });
    log(&format!("{seq} driver {vendor}: exit {code} reboot {reboot}"));
}

fn run_in_helper_inner(app_dir: &Path, helper_dir: &Path, vendor: &str, log: &dyn Fn(&str)) -> Result<(i64, bool), String> {
    if !vendor_ok(vendor) {
        return Err(format!("'{vendor}' is not a vendor word"));
    }
    let text = std::fs::read_to_string(app_dir.join("drivers.json")).map_err(|e| format!("no driver table at {}: {e}", app_dir.join("drivers.json").display()))?;
    let v: serde_json::Value = serde_json::from_str(&text).map_err(|e| format!("the driver table does not parse: {e}"))?;
    let table = crate::drivertable::Table::parse(&v)?;
    let e = table.entry(vendor).ok_or_else(|| format!("no {vendor} row in the table"))?.clone();
    let file = file_for(&e, &app_dir.join("drivers"));
    let size = std::fs::metadata(&file).map(|m| m.len()).map_err(|x| format!("no downloaded installer at {}: {x}", file.display()))?;
    let sha = crate::manifest::sha256_file(&file).map_err(|x| x.to_string())?;
    let subject = signer_subject(&file)?;
    verify(&e, size, &sha, &subject)?;
    log(&format!("driver {vendor}: {} verified (size, sha256, signer '{subject}'); running {} {}", file.display(), file.display(), e.args.join(" ")));
    let mut c = std::process::Command::new(&file);
    c.args(&e.args);
    crate::platform::quiet(&mut c);
    let mut child = c.spawn().map_err(|x| format!("the installer did not start: {x}"))?;
    let started = Instant::now();
    loop {
        crate::powertask::beat(helper_dir, crate::platform::unix_now());
        match child.try_wait() {
            Ok(Some(st)) => {
                let code = st.code().map(|c| c as i64).unwrap_or(-1);
                let (reboot, _) = crate::drivertable::exit_meaning(code, &e);
                return Ok((code, reboot));
            }
            Ok(None) => {
                if started.elapsed() > INSTALL_CAP {
                    let _ = child.kill();
                    return Err(format!("the installer ran past {} minutes and was ended", INSTALL_CAP.as_secs() / 60));
                }
                std::thread::sleep(Duration::from_secs(2));
            }
            Err(x) => return Err(format!("waiting on the installer: {x}")),
        }
    }
}

#[cfg(windows)]
fn signer_subject(path: &Path) -> Result<String, String> {
    let ps = format!("$s = Get-AuthenticodeSignature -LiteralPath '{}'; Write-Output ('status=' + $s.Status); Write-Output ('subject=' + $s.SignerCertificate.Subject)", path.display().to_string().replace('\'', "''"));
    let mut c = std::process::Command::new(crate::platform::tool("powershell"));
    c.args(["-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", &ps]);
    let out = crate::detect::run_timeout(&mut c, None, Duration::from_secs(60)).ok_or("PowerShell did not answer the signature check")?;
    let mut status = String::new();
    let mut subject = String::new();
    for l in out.lines() {
        if let Some(v) = l.strip_prefix("status=") { status = v.trim().to_string(); } else if let Some(v) = l.strip_prefix("subject=") { subject = v.trim().to_string(); }
    }
    if status != "Valid" {
        return Err(format!("the Authenticode signature is {status}, not Valid"));
    }
    Ok(subject)
}

#[cfg(not(windows))]
fn signer_subject(_path: &Path) -> Result<String, String> {
    Err("the installer runs on Windows only".into())
}

/// The app's side: when the Power Helper task is registered, ask it and wait for the result line; None when it is not
/// (the caller falls back to the one-prompt path). `file` is the verified download.
pub fn via_helper(vendor: &str) -> Option<Result<(i64, bool), String>> {
    if !cfg!(windows) || !crate::powertask::registered() {
        return None;
    }
    let dir = crate::powertask::helper_dir();
    Some((|| {
        crate::powertask::ensure_running(&dir, Duration::from_secs(30))?;
        let seq = crate::powertask::wire_seq();
        let mut text = std::fs::read_to_string(dir.join("cmd.txt")).unwrap_or_default();
        text.push_str(&command_line(seq, vendor));
        std::fs::write(dir.join("cmd.txt"), text).map_err(|e| e.to_string())?;
        let started = Instant::now();
        loop {
            let res = std::fs::read_to_string(dir.join(RESULT_FILE)).unwrap_or_default();
            if let Some(r) = parse_result(&res, seq) {
                return Ok(r);
            }
            if started.elapsed() > RESULT_WAIT {
                return Err(format!("the Power Helper gave no result for the {vendor} install within {} minutes", RESULT_WAIT.as_secs() / 60));
            }
            if !crate::powertask::alive(&dir) && started.elapsed() > Duration::from_secs(120) {
                return Err("the Power Helper stopped during the install (no heartbeat)".into());
            }
            std::thread::sleep(Duration::from_secs(3));
        }
    })())
}

#[cfg(test)]
mod tests {
    use super::*;

    fn intel() -> crate::drivertable::VendorEntry {
        let v: serde_json::Value = serde_json::from_str(include_str!("../../../packaging/ota/drivers.json")).unwrap();
        crate::drivertable::Table::parse(&v).unwrap().entry("intel").unwrap().clone()
    }

    /// Known-failed first: an installer that is not the table's file, or not signed by a driver vendor, must never run
    /// elevated; the first cut of the unattended path had no such check.
    #[test]
    fn the_helper_refuses_a_file_that_is_not_the_tables_or_not_the_vendors() {
        let e = intel();
        assert!(verify(&e, e.size, &e.sha256, "CN=Intel Corporation, O=Intel Corporation, S=California, C=US").is_ok());
        assert!(verify(&e, e.size + 1, &e.sha256, "CN=Intel Corporation").unwrap_err().starts_with("size"));
        assert!(verify(&e, e.size, "00", "CN=Intel Corporation").unwrap_err().starts_with("sha256"));
        assert!(verify(&e, e.size, &e.sha256, "CN=Some Miner Tools Ltd").unwrap_err().contains("not a driver vendor"));
        assert!(verify(&e, e.size, &e.sha256, "CN=NVIDIA Corporation").unwrap_err().contains("not the table's"), "a vendor, but not this row's");
        assert_eq!(file_for(&e, Path::new("D")).file_name().unwrap().to_str().unwrap(), "gfx_win_101.9034.exe");
    }

    #[test]
    fn the_right_is_the_power_helper_task_with_a_two_hour_limit_and_the_protocol_round_trips() {
        assert_eq!(RIGHT.0, "driver-install-task", "the id src/rights.rs's test already anticipates");
        let s = register_script(Path::new("C:\\p\\Ember\\igneum-app.exe"));
        assert!(s.contains("-ExecutionTimeLimit (New-TimeSpan -Hours 2)") && !s.contains("-Hours 1"), "{s}");
        assert!(s.contains("-TaskName 'Igneum Power Helper'") && s.contains("--power-helper"), "the same task, the same action");
        assert_eq!(command_line(305327, "intel"), "305327 driver intel\n");
        let r = result_line(305327, "intel", 14, true);
        assert_eq!(parse_result(&r, 305327), Some((14, true)));
        assert_eq!(parse_result(&r, 305328), None, "another sequence's result is not this one");
        assert_eq!(parse_result("305327 intel exit -2 reboot 0\n", 305327), Some((-2, false)), "a refusal reads as exit -2");
        assert!(vendor_ok("amd") && !vendor_ok("apple") && !vendor_ok("C:\\x.exe"));
    }
}
