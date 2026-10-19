//! Every right the app will ever need is taken ONCE, at install, by one elevated step; the app never prompts at runtime;
//! an update asks again only when the list of rights grew (the founder, 7 October 2026: "all the 'rights' need to be done on
//! install, and then on update if anything new").
//!
//! The step asks only in an interactive session that is not a job's (SESSIONNAME set, no IGNEUM_JOB_* in the environment):
//! a PC job may never need a click, so a silent install from a job logs "rights: deferred" and the next interactive start
//! of the app asks once. The WebView2 runtime is a right too, "webview2-runtime@<min>" with <min> the host loader's minimum
//! (app/windows/version.h IGNEUM_WEBVIEW2_MIN): the elevated step reads the Edge WebView2 client key (HKLM WOW6432Node and
//! HKCU) and runs the bundled evergreen bootstrapper silently when the runtime is absent or below <min>; a raised minimum is
//! a new id, so that update asks once.
//!
//! Windows: the installer's [Run] entry `igneum-app.exe --rights` (every install, silent ones included) compares the
//! installed rights manifest (`<app data>/app/rights.json`: the version and the list the elevated step completed) with
//! this build's RIGHTS; when a right is missing it writes rights.ps1 and runs it elevated once (the one UAC prompt: the
//! Power Helper task for clock and power control, the boot task, the inbound firewall rules for the node and the pool
//! miner), then writes the manifest; when nothing is missing it exits at once with no prompt. At runtime: Power control
//! is a plain toggle (the Power Helper task does the work with no prompt); a right the manifest lacks is a notice
//! ("run the installer again"), never a prompt. The prompt counts are a function here, tested with the known-failed
//! shapes first (before an earlier release: a fresh install then Power control on prompted; the firewall rule prompted on the first
//! run; the boot task was registered at the engine's start).

use std::path::Path;

/// The rights this build needs, in the order the elevated step takes them. An id never changes meaning; a new need is
/// a new id (that is what makes an update ask once).
pub const RIGHTS: &[(&str, &str)] = &[
    ("power-helper-task", "the Igneum Power Helper task: the clock and power limits of NVIDIA cards with no prompt (src/powertask.rs)"),
    // an earlier release: the same task registered to run whether or not a user is logged on (S4U); a new id, so an install that holds the
    // interactive-only form takes it again once at the update (main, 7 October 2026, the first Windows machine)
    ("power-helper-task@unattended", "the Igneum Power Helper task in its unattended form: it runs whether or not a user is logged on, so a job's or the engine's own start is accepted (src/powertask.rs)"),
    // V6-06 (8 October 2026): the same task pointed at the protected copy in %ProgramData%\Igneum\helper; a new id, so an
    // install that holds the LOCALAPPDATA form takes it again once (by the running helper's own reregister where the
    // signed manifest names the engine hash, else one prompt at the next interactive start)
    ("power-helper-task@protected", "the Igneum Power Helper task run from a protected copy of the engine that only administrators can write (src/powertask.rs)"),
    ("boot-task", "the Ember (boot) task: the engine starts at boot with nobody logged on (src/boot.rs)"),
    ("firewall-node", "the inbound firewall rule for igneumd.exe (other nodes can dial in)"),
    ("firewall-miner", "the inbound firewall rule for igneum-miner.exe (a pool's stratum port)"),
    (WEBVIEW2_RIGHT, "the WebView2 runtime the window needs, at or above the host loader's minimum (installed silently from the bundled evergreen bootstrapper when absent or older)"),
    // the driver lane's right (branch driver-hold-22, src/driverinstall.rs): the same Power Helper task with a two-hour run limit, so a
    // vendor's driver installs unattended through it; at the merge the Power Helper line of script() becomes driverinstall::register_script
    ("driver-install-task", "the Igneum Power Helper task with a two-hour run limit: a vendor's driver installs unattended through it, the vendor's cards held, no prompt (src/driverinstall.rs)"),
    // Igneum 2.0 (8 October 2026): the node needs its proof verifier from block zero, and on Windows the host runs in WSL2;
    // the feature is the one part that needs rights, taken here; the distro and the host follow with no prompt (src/verifier.rs)
    (WSL2_RIGHT, "the Windows Subsystem for Linux (the feature and its kernel, no distribution): the node's proof verifier runs the payload's igneum-prove-host in WSL2 from block zero; the distribution installs with no prompt at the app's next start (src/verifier.rs)"),
];

/// The WSL2 feature right (Igneum 2.0): `wsl --install --no-distribution --no-launch` in the elevated step; a reboot
/// may follow before the feature answers. Idempotent: an installed feature returns at once.
pub const WSL2_RIGHT: &str = "wsl2-feature";

/// The host loader's minimum WebView2 runtime (app/windows/version.h IGNEUM_WEBVIEW2_MIN), read at build time so the two
/// never drift; the right's id carries it.
pub const WEBVIEW2_MIN: &str = webview2_min_from_header(include_str!("../../windows/version.h"));
pub const WEBVIEW2_RIGHT: &str = const_format_webview2_right();
/// The bundled bootstrapper in the install folder and its pinned sha file (packaging/windows/webview2.sha256).
pub const WEBVIEW2_BOOTSTRAPPER: &str = "MicrosoftEdgeWebview2Setup.exe";
pub const WEBVIEW2_KEY: &str = "{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}";

const fn webview2_min_from_header(h: &str) -> &str {
    // the line `#define IGNEUM_WEBVIEW2_MIN "a.b.c.d"`: the text between the quotes
    let b = h.as_bytes();
    let key = b"IGNEUM_WEBVIEW2_MIN \"";
    let mut i = 0;
    while i + key.len() < b.len() {
        let mut j = 0;
        while j < key.len() && b[i + j] == key[j] {
            j += 1;
        }
        if j == key.len() {
            let start = i + key.len();
            let mut end = start;
            while end < b.len() && b[end] != b'"' {
                end += 1;
            }
            // SAFETY of the slice: start and end sit on ASCII bytes of a str literal
            match h.split_at(start).1.split_at(end - start).0 {
                s => return s,
            }
        }
        i += 1;
    }
    "0"
}
const fn const_format_webview2_right() -> &'static str {
    // "webview2-runtime@" + WEBVIEW2_MIN, built once at compile time
    const PREFIX: &str = "webview2-runtime@";
    const MIN: &str = webview2_min_from_header(include_str!("../../windows/version.h"));
    const LEN: usize = PREFIX.len() + MIN.len();
    const BUF: [u8; LEN] = {
        let mut out = [0u8; LEN];
        let p = PREFIX.as_bytes();
        let m = MIN.as_bytes();
        let mut i = 0;
        while i < p.len() {
            out[i] = p[i];
            i += 1;
        }
        let mut j = 0;
        while j < m.len() {
            out[p.len() + j] = m[j];
            j += 1;
        }
        out
    };
    match std::str::from_utf8(&BUF) {
        Ok(s) => s,
        Err(_) => "webview2-runtime@0",
    }
}

pub const MANIFEST_FILE: &str = "rights.json";
pub const SCRIPT_FILE: &str = "rights.ps1";

/// The manifest the elevated step leaves: which rights hold, from which version, when.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct Manifest {
    pub version: String,
    pub rights: Vec<String>,
    pub at: u64,
}

impl Manifest {
    pub fn parse(text: &str) -> Option<Manifest> {
        let v: serde_json::Value = serde_json::from_str(text).ok()?;
        Some(Manifest {
            version: v.get("version").and_then(|x| x.as_str()).unwrap_or("").to_string(),
            rights: v.get("rights").and_then(|x| x.as_array()).map(|a| a.iter().filter_map(|r| r.as_str().map(|s| s.to_string())).collect()).unwrap_or_default(),
            at: v.get("at").and_then(|x| x.as_u64()).unwrap_or(0),
        })
    }
    pub fn to_json(&self) -> String {
        serde_json::json!({ "version": self.version, "rights": self.rights, "at": self.at, "format": "igneum-rights-1" }).to_string()
    }
    pub fn load(app_dir: &Path) -> Option<Manifest> {
        std::fs::read_to_string(app_dir.join(MANIFEST_FILE)).ok().and_then(|t| Manifest::parse(&t))
    }
    pub fn save(&self, app_dir: &Path) -> std::io::Result<()> {
        std::fs::write(app_dir.join(MANIFEST_FILE), self.to_json())
    }
}

/// The ids this build wants that the installed manifest does not hold (every id when there is no manifest).
pub fn missing(installed: Option<&Manifest>, wanted: &[(&str, &str)]) -> Vec<String> {
    let have: Vec<&str> = installed.map(|m| m.rights.iter().map(|s| s.as_str()).collect()).unwrap_or_default();
    wanted.iter().map(|(id, _)| *id).filter(|id| !have.contains(id)).map(|s| s.to_string()).collect()
}

/// Where the step runs: an interactive session that is not a job's may ask; anything else defers (the founder, 7 October 2026:
/// no PC job may need a click).
pub fn may_ask(session_name: Option<&str>, job_env: bool) -> bool {
    crate::boot::interactive_session(session_name) && !job_env
}

/// Is this process inside a remote job (the runner's IGNEUM_JOB_* environment)?
pub fn in_job_env() -> bool {
    std::env::vars().any(|(k, _)| k.starts_with("IGNEUM_JOB_"))
}

/// The outcome of the install step.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Step {
    /// nothing missing: no prompt
    Nothing,
    /// a right is missing and this session may ask: one prompt
    Asked,
    /// a right is missing but this is a job's or a session-less run: no prompt, the next interactive start asks
    Deferred,
}

pub fn step(installed: Option<&Manifest>, wanted: &[(&str, &str)], session_name: Option<&str>, job_env: bool) -> Step {
    if missing(installed, wanted).is_empty() {
        Step::Nothing
    } else if may_ask(session_name, job_env) {
        Step::Asked
    } else {
        Step::Deferred
    }
}

/// The WebView2 version a registry read gave against the minimum: true when the runtime must be installed.
pub fn webview2_needs_install(installed: Option<&str>, min: &str) -> bool {
    let parse = |v: &str| -> Vec<u64> { v.trim().split('.').map(|p| p.trim().parse::<u64>().unwrap_or(0)).collect() };
    match installed.map(|v| v.trim()).filter(|v| !v.is_empty()) {
        None => true,
        Some(v) => parse(v) < parse(min),
    }
}

/// What happens to the user.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Event {
    /// the installer's --rights step (a fresh install or an update)
    Install,
    /// Power control switched on in Settings
    PowerControlOn,
    /// the engine's first run (the firewall rule, before an earlier release)
    FirstRun,
}

/// an earlier release: how many administrator prompts an event raises. Only the install step asks, and only when a right is missing.
pub fn prompts(event: Event, installed: Option<&Manifest>, wanted: &[(&str, &str)]) -> u32 {
    match event {
        Event::Install => if missing(installed, wanted).is_empty() { 0 } else { 1 },
        Event::PowerControlOn | Event::FirstRun => 0,
    }
}

/// Before an earlier release, for the record: the installer asked nothing; the first run asked for the firewall rule; Power control on
/// asked when the Power Helper task was not registered yet.
pub fn legacy_prompts(event: Event, power_task_registered: bool) -> u32 {
    match event {
        Event::Install => 0,
        Event::FirstRun => 1,
        Event::PowerControlOn => if power_task_registered { 0 } else { 1 },
    }
}

fn ps_quote(s: &str) -> String {
    s.replace('\'', "''")
}

/// The one elevated script: every right in RIGHTS, idempotent (a rule is removed before it is added, a task is
/// registered with -Force). `exe` is the installed igneum-app.exe, `install_dir` its folder, `data_root` the user's
/// data root for the boot task.
pub fn script(exe: &Path, install_dir: &Path, data_root: &Path) -> String {
    let node = ps_quote(&install_dir.join("igneumd.exe").display().to_string());
    let miner = ps_quote(&install_dir.join("igneum-miner.exe").display().to_string());
    let mut s = String::from("$ErrorActionPreference = 'Continue'\r\n# Igneum rights, one elevated step (src/rights.rs). Not for running by hand.\r\n");
    s.push_str(&crate::powertask::register_script(exe).replace("exit 0\r\n", ""));
    s.push_str(&crate::boot::register_script(exe, data_root).replace("exit 0\r\n", ""));
    for (name, prog) in [("Ember node", node), ("Ember pool", miner)] {
        s.push_str(&format!(
            "& netsh.exe advfirewall firewall delete rule name='{name}' | Out-Null\r\n\
             & netsh.exe advfirewall firewall add rule name='{name}' dir=in action=allow enable=yes profile=private,domain protocol=TCP program='{prog}' | Out-Null\r\n"
        ));
    }
    s.push_str(&webview2_script(install_dir));
    s.push_str(&wsl2_script(install_dir));
    s.push_str("exit 0\r\n");
    s
}

/// The one elevated command line (V6-06): PowerShell with the rights script inline (-EncodedCommand), never a file.
pub fn elevated_line(powershell: &Path, exe: &Path, install_dir: &Path, data_root: &Path) -> String {
    format!("\"{}\" {}", powershell.display(), crate::platform::encoded_command_args(&script(exe, install_dir, data_root)).join(" "))
}

/// The WSL2 part of the elevated script (Igneum 2.0): the feature and the kernel with no distribution and no window;
/// one log line either way; a reboot pending is said, never forced (the installer never restarts a PC by itself).
pub fn wsl2_script(install_dir: &Path) -> String {
    let log = ps_quote(&install_dir.join("rights.log").display().to_string());
    format!(
        "$wslOn = $false; try {{ & wsl.exe --status *> $null; $wslOn = ($LASTEXITCODE -eq 0) }} catch {{}}\r\n\
         if ($wslOn) {{ \"$(Get-Date -Format o) wsl2: on\" | Out-File -FilePath '{log}' -Append -Encoding utf8 }} else {{\r\n\
         \x20 $wslOut = ''; try {{ $wslOut = (& wsl.exe --install --no-distribution --no-launch 2>&1 | ForEach-Object {{ \"$_\" }}) -join ' ' }} catch {{ $wslOut = \"$_\" }}\r\n\
         \x20 \"$(Get-Date -Format o) wsl2: installed the feature and the kernel (exit $LASTEXITCODE; a restart of Windows may be needed before it answers): $wslOut\" | Out-File -FilePath '{log}' -Append -Encoding utf8\r\n\
         }}\r\n"
    )
}

/// The WebView2 part of the elevated script: the client key's pv (HKLM WOW6432Node, then HKCU), the bundled bootstrapper
/// /silent /install when absent or below the minimum (exit 0 required), the version read back, one log line either way.
pub fn webview2_script(install_dir: &Path) -> String {
    let boot = ps_quote(&install_dir.join(WEBVIEW2_BOOTSTRAPPER).display().to_string());
    let log = ps_quote(&install_dir.join("rights.log").display().to_string());
    format!(
        "function WV2 {{ foreach ($k in @('HKLM:\\SOFTWARE\\WOW6432Node\\Microsoft\\EdgeUpdate\\Clients\\{key}', 'HKCU:\\SOFTWARE\\Microsoft\\EdgeUpdate\\Clients\\{key}')) {{ $v = (Get-ItemProperty $k -ErrorAction SilentlyContinue).pv; if ($v) {{ return \"$v\" }} }}; return '' }}\r\n\
         $wvMin = '{min}'\r\n\
         $wvHave = WV2\r\n\
         $wvNeed = (-not $wvHave) -or ([version]$wvHave -lt [version]$wvMin)\r\n\
         if ($wvNeed) {{\r\n\
         \x20 if (Test-Path '{boot}') {{ $wp = Start-Process -FilePath '{boot}' -ArgumentList @('/silent', '/install') -Wait -PassThru -WindowStyle Hidden; Add-Content -Path '{log}' -Value ((Get-Date -Format s) + ' webview2: was ' + $wvHave + ', installed from the bundled bootstrapper, exit ' + $wp.ExitCode + ', now ' + (WV2)) }}\r\n\
         \x20 else {{ Add-Content -Path '{log}' -Value ((Get-Date -Format s) + ' webview2: was ' + $wvHave + ', below ' + $wvMin + ', and no bootstrapper beside the app; the window opens the dashboard in the browser until the runtime is installed') }}\r\n\
         }} else {{ Add-Content -Path '{log}' -Value ((Get-Date -Format s) + ' webview2: ' + $wvHave + ' ok (minimum ' + $wvMin + ')') }}\r\n",
        key = WEBVIEW2_KEY,
        min = WEBVIEW2_MIN
    )
}

/// The installer's step. Compares, asks once when something is missing, writes the manifest. Ok(prompted).
/// The rights the registered Igneum Power Helper can take by itself, elevated and with no prompt, through its `reregister`
/// verb (the helper re-registers its own task from the running build's script: src/powertask.rs). an earlier release: the task's
/// unattended form is such a right, so an update by job on a PC with nobody at the desk (the second Windows machine, 7 October 2026) takes it
/// without a click, where the deferred rule alone would have left it to "the next interactive start".
pub const HELPER_TAKEABLE: &[&str] = &["power-helper-task@unattended", "power-helper-task@protected"];

pub fn helper_can_take(missing: &[String], task_registered: bool) -> bool {
    task_registered && !missing.is_empty() && missing.iter().all(|m| HELPER_TAKEABLE.contains(&m.as_str()))
}

/// The helper's answer to `<seq> reregister` in its log: Some(true) on "ok", Some(false) on "failed", None while no line.
pub fn reregister_ack(log: &str, seq: u64) -> Option<bool> {
    let ok = format!(" {seq} reregister ok");
    let failed = format!(" {seq} reregister failed");
    log.lines().rev().find_map(|l| if l.contains(&ok) { Some(true) } else if l.contains(&failed) { Some(false) } else { None })
}

/// Takes the helper-takeable rights through the running Power Helper task: heartbeat first, then the reregister line, then
/// the helper's own ok line within 20 s. Windows only (elsewhere the task does not exist).
fn take_through_helper(missing: &[String]) -> Result<(), String> {
    if !cfg!(windows) {
        return Err("the Power Helper task is Windows only".into());
    }
    let dir = crate::powertask::helper_dir();
    let _ = std::fs::create_dir_all(&dir);
    crate::powertask::ensure_running(&dir, std::time::Duration::from_secs(12))?;
    let seq = crate::powertask::wire_seq() + 1;
    std::fs::write(dir.join("cmd.txt"), format!("{seq} reregister\n")).map_err(|e| e.to_string())?;
    let until = std::time::Instant::now() + std::time::Duration::from_secs(20);
    loop {
        std::thread::sleep(std::time::Duration::from_millis(500));
        let log = std::fs::read_to_string(dir.join("helper.log")).unwrap_or_default();
        match reregister_ack(&log, seq) {
            Some(true) => return Ok(()),
            Some(false) => return Err(format!("the helper's reregister failed (its log names why); {} not taken", missing.join(", "))),
            None if std::time::Instant::now() >= until => return Err(format!("the helper did not answer sequence {seq} reregister within 20 s; {} not taken", missing.join(", "))),
            None => {}
        }
    }
}

pub fn install(exe: &Path, install_dir: &Path, data_root: &Path, app_dir: &Path, version: &str) -> Result<bool, String> {
    let installed = Manifest::load(app_dir);
    match step(installed.as_ref(), RIGHTS, std::env::var("SESSIONNAME").ok().as_deref(), in_job_env()) {
        Step::Nothing => return Ok(false),
        Step::Deferred => {
            let miss = missing(installed.as_ref(), RIGHTS);
            // an earlier release: what the registered helper can take itself, it takes now, elevated, with no prompt (the second Windows machine by job)
            if helper_can_take(&miss, cfg!(windows) && crate::powertask::registered()) {
                take_through_helper(&miss)?;
                let mut rights: Vec<String> = installed.as_ref().map(|m| m.rights.clone()).unwrap_or_default();
                rights.extend(miss.iter().cloned());
                let _ = std::fs::create_dir_all(app_dir);
                Manifest { version: version.to_string(), rights, at: crate::platform::unix_now() }.save(app_dir).map_err(|e| format!("cannot write the rights manifest: {e}"))?;
                return Ok(true);
            }
            // a job's or a session-less install never raises a prompt (the founder, 7 October 2026); the next interactive start asks once
            return Err(format!("rights: deferred ({} missing: {}); no prompt in a job's or a session-less install, the next interactive start of the app asks once", miss.len(), miss.join(", ")));
        }
        Step::Asked => {}
    }
    let _ = std::fs::create_dir_all(app_dir);
    // V6-06: the script travels inline; no rights.ps1 under the data root is read by the elevated step (a copy is left
    // for the record, after the run, never as its input)
    #[cfg(windows)]
    {
        let line = elevated_line(&crate::platform::tool("powershell"), exe, install_dir, data_root);
        crate::platform::run_elevated(&line)?;
        let _ = std::fs::write(app_dir.join(SCRIPT_FILE), [b"\xEF\xBB\xBF".as_slice(), script(exe, install_dir, data_root).as_bytes()].concat());
    }
    #[cfg(not(windows))]
    {
        let _ = (exe, install_dir, data_root);
        return Err("the rights step is Windows only".into());
    }
    #[allow(unreachable_code)]
    {
        let m = Manifest { version: version.to_string(), rights: RIGHTS.iter().map(|(id, _)| id.to_string()).collect(), at: crate::platform::unix_now() };
        m.save(app_dir).map_err(|e| format!("cannot write the rights manifest: {e}"))?;
        Ok(true)
    }
}

/// Does the installed manifest hold this right? (The runtime's question before it would have prompted.)
pub fn held(app_dir: &Path, id: &str) -> bool {
    Manifest::load(app_dir).map(|m| m.rights.iter().any(|r| r == id)).unwrap_or(false)
}

/// The sentence the dashboard shows for a right the manifest lacks.
pub fn missing_note(id: &str) -> String {
    let what = RIGHTS.iter().find(|(i, _)| *i == id).map(|(_, d)| *d).unwrap_or(id);
    format!("not set up on this PC ({what}); run the Ember installer again: it asks for administrator rights once and sets everything up")
}

#[cfg(test)]
mod tests {
    use super::*;

    /// Known failed first (V6-06): rights.ps1 was written to the user's data folder and run elevated with -File, so a
    /// swap between the write and the run would have run as administrator. The script now travels inline.
    #[test]
    fn the_rights_step_runs_its_script_inline_under_the_command_line_cap() {
        let (exe, dir, root) = (Path::new(r"C:\Work\Admin\AppData\Local\Programs\Ember\igneum-app.exe"), Path::new(r"C:\Work\Admin\AppData\Local\Programs\Ember"), Path::new(r"C:\Work\Admin\AppData\Local\igneum"));
        let line = elevated_line(Path::new(r"C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe"), exe, dir, root);
        assert!(line.contains("-EncodedCommand ") && !line.contains("-File") && !line.contains(".ps1"), "{line}");
        assert!(line.len() < 30_000, "the elevated line must fit the 32 K command line: {} chars", line.len());
    }

    fn m(rights: &[&str]) -> Manifest {
        Manifest { version: "0.1.22".into(), rights: rights.iter().map(|s| s.to_string()).collect(), at: 1 }
    }

    /// Known-failed first: before an earlier release a fresh install asked nothing and then the first run and Power control on each
    /// asked (two prompts on the way to a tuned card); an earlier release asks once at install and never again.
    #[test]
    fn a_fresh_install_then_power_control_on_shows_one_prompt_at_install_and_none_after() {
        assert_eq!(legacy_prompts(Event::Install, false) + legacy_prompts(Event::FirstRun, false) + legacy_prompts(Event::PowerControlOn, false), 2, "the old way: two prompts");
        assert_eq!(prompts(Event::Install, None, RIGHTS), 1, "the one prompt, at install");
        let installed = m(&RIGHTS.iter().map(|(i, _)| *i).collect::<Vec<_>>());
        assert_eq!(prompts(Event::FirstRun, Some(&installed), RIGHTS), 0);
        assert_eq!(prompts(Event::PowerControlOn, Some(&installed), RIGHTS), 0);
    }

    /// the founder's rule of the same night: no PC job may need a click. Known-failed first: 3fbf4280's step asked on every install with
    /// a missing right, a job's silent install included.
    #[test]
    fn the_rights_step_asks_only_in_an_interactive_session_that_is_not_a_jobs() {
        assert_eq!(step(None, RIGHTS, Some("Console"), true), Step::Deferred, "a job's install (IGNEUM_JOB_* set): never a prompt");
        assert_eq!(step(None, RIGHTS, None, false), Step::Deferred, "no interactive session: never a prompt");
        assert_eq!(step(None, RIGHTS, Some("Console"), false), Step::Asked, "the person at the PC: one prompt");
        let all = m(&RIGHTS.iter().map(|(i, _)| *i).collect::<Vec<_>>());
        assert_eq!(step(Some(&all), RIGHTS, Some("Console"), false), Step::Nothing);
        assert_eq!(step(Some(&all), RIGHTS, None, true), Step::Nothing, "nothing missing: nothing, wherever it runs");
        assert!(may_ask(Some("RDP-Tcp#2"), false) && !may_ask(Some("Console"), true) && !may_ask(None, false));
    }

    /// The WebView2 runtime as a right (packaging's step, named 7 October 2026). Known-failed first: before it the runtime
    /// missing meant the window said "install the runtime from microsoft.com" and nothing installed it.
    #[test]
    fn the_webview2_runtime_is_a_right_with_the_minimum_in_its_id() {
        assert_eq!(WEBVIEW2_MIN, "109.0.1518.78", "read from app/windows/version.h at build time");
        assert_eq!(WEBVIEW2_RIGHT, "webview2-runtime@109.0.1518.78");
        assert!(RIGHTS.iter().any(|(id, _)| *id == WEBVIEW2_RIGHT));
        // absent runtime: one prompt at install; present at or above the minimum: the right still has to be taken once
        // (the manifest records it), but the script installs nothing (its own check); a raised minimum is a new id
        let old = m(&["power-helper-task", "power-helper-task@unattended", "power-helper-task@protected", "boot-task", "firewall-node", "firewall-miner", "webview2-runtime@101.0.0.0", "driver-install-task", WSL2_RIGHT]);
        assert_eq!(missing(Some(&old), RIGHTS), vec![WEBVIEW2_RIGHT.to_string()], "a raised minimum is exactly one new right");
        assert_eq!(prompts(Event::Install, Some(&old), RIGHTS), 1);
        let now = m(&RIGHTS.iter().map(|(i, _)| *i).collect::<Vec<_>>());
        assert_eq!(prompts(Event::Install, Some(&now), RIGHTS), 0, "the same minimum: no prompt");
        assert!(webview2_needs_install(None, WEBVIEW2_MIN), "absent: install");
        assert!(webview2_needs_install(Some(""), WEBVIEW2_MIN));
        assert!(webview2_needs_install(Some("108.0.1462.76"), WEBVIEW2_MIN), "below: install");
        assert!(!webview2_needs_install(Some("109.0.1518.78"), WEBVIEW2_MIN), "equal: nothing");
        assert!(!webview2_needs_install(Some("154.0.4258.62"), WEBVIEW2_MIN), "the second Windows machine's runtime: nothing");
        let s = webview2_script(Path::new("C:\\p\\Ember"));
        assert!(s.contains("EdgeUpdate\\Clients\\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}") && s.contains("HKCU:"), "both registry forms");
        assert!(s.contains("MicrosoftEdgeWebview2Setup.exe") && s.contains("'/silent', '/install'") && s.contains("-Wait -PassThru -WindowStyle Hidden"));
        assert!(s.contains("$wvMin = '109.0.1518.78'") && s.contains("[version]$wvHave -lt [version]$wvMin"));
        assert!(s.contains("webview2: ") && s.contains(" ok (minimum "), "one log line either way");
    }

    /// Known-failed first (the second Windows machine by job, 7 October 2026): the deferred rule left every new right to "the next interactive
    /// start", which on a PC with nobody at the desk never comes. The task's unattended form is a right the registered
    /// helper takes itself through `reregister`, elevated, no prompt; anything else stays deferred.
    #[test]
    fn a_job_install_takes_the_task_form_through_the_registered_helper_and_defers_the_rest() {
        let one = vec!["power-helper-task@unattended".to_string()];
        assert!(helper_can_take(&one, true));
        assert!(!helper_can_take(&one, false), "no task registered: nothing can take it, deferred");
        assert!(!helper_can_take(&[], true), "nothing missing: nothing to take");
        assert!(!helper_can_take(&["power-helper-task@unattended".to_string(), "firewall-node".to_string()], true), "a firewall rule is not the helper's to take");
        assert!(!helper_can_take(&["boot-task".to_string()], true));
        assert_eq!(reregister_ack("1791409581 helper started\n1791409590 427400 reregister ok: the task now runs C:\\x\\igneum-app.exe --power-helper\n", 427400), Some(true));
        assert_eq!(reregister_ack("1791409590 427400 reregister failed: the task now runs \n", 427400), Some(false));
        assert_eq!(reregister_ack("1791409590 427399 reregister ok: ...\n", 427400), None, "another sequence's answer is not this one's");
        assert_eq!(reregister_ack("", 427400), None);
        // the install path: the deferred branch tries the helper before it defers, and only for what the helper can take
        let s = include_str!("rights.rs");
        let i = s.find("Step::Deferred => {").unwrap();
        let body = &s[i..i + 1500];
        let take = body.find("helper_can_take(&miss").expect("the helper is asked first");
        let defer = body.find("rights: deferred").expect("then the deferral");
        assert!(take < defer);
        assert!(body.contains("take_through_helper(&miss)?") && body.contains("rights.extend(miss.iter().cloned())"), "the taken rights go into the manifest");
        // the helper's reregister writes the running build's script: the S4U form (src/powertask.rs register_script)
        assert!(crate::powertask::register_script(Path::new("C:\\p\\igneum-app.exe")).contains("-LogonType S4U -RunLevel Highest"));
    }

    /// an earlier release: an install that holds every an earlier release right lacks exactly the unattended task form; the update asks once.
    #[test]
    fn the_unattended_task_form_is_one_new_right_for_a_0_3_23_install() {
        let r0323 = m(&["power-helper-task", "boot-task", "firewall-node", "firewall-miner", WEBVIEW2_RIGHT, "driver-install-task"]);
        assert_eq!(missing(Some(&r0323), RIGHTS), vec!["power-helper-task@unattended".to_string(), "power-helper-task@protected".to_string(), WSL2_RIGHT.to_string()], "2.0: the WSL feature and the protected copy are the other rights a 0.1.23 install lacks");
        assert_eq!(prompts(Event::Install, Some(&r0323), RIGHTS), 1);
        let s = script(Path::new("C:\\p\\Ember\\igneum-app.exe"), Path::new("C:\\p\\Ember"), Path::new("C:\\u\\AppData\\Local\\igneum"));
        assert!(s.contains("-TaskName 'Igneum Power Helper'") && s.contains("-LogonType S4U -RunLevel Highest"), "the rights step registers the unattended form: {s}");
    }

    /// V6-06, known-failed first (8 October 2026): a changed script re-asks nothing (rights are by id), so an installed
    /// an earlier release PC would have kept its task on the LOCALAPPDATA exe for ever. The protected copy is a new id; a an earlier release
    /// install lacks exactly it; the running helper may take it itself (its reregister makes the copy, under the
    /// signed manifest's engine hash) so an update by job asks no click, and an interactive start asks once otherwise.
    #[test]
    fn the_protected_helper_copy_is_one_new_right_for_a_2_0_1_install() {
        assert!(RIGHTS.iter().any(|(id, _)| *id == "power-helper-task@protected"));
        let r201 = m(&["power-helper-task", "power-helper-task@unattended", "boot-task", "firewall-node", "firewall-miner", WEBVIEW2_RIGHT, "driver-install-task", WSL2_RIGHT]);
        assert_eq!(missing(Some(&r201), RIGHTS), vec!["power-helper-task@protected".to_string()]);
        assert_eq!(prompts(Event::Install, Some(&r201), RIGHTS), 1);
        assert!(helper_can_take(&["power-helper-task@protected".to_string()], true));
        assert!(!helper_can_take(&["power-helper-task@protected".to_string()], false), "no task registered: the one prompt");
    }

    /// Igneum 2.0, known-failed first (main, 8 October 2026): the node refused to start on Windows without igneum-prove-host
    /// and nothing installed it. The WSL feature is a right the installer's elevated step takes (no distribution, no
    /// window); a an earlier release install lacks exactly it and asks once at the update.
    #[test]
    fn the_wsl2_feature_is_a_right_the_installer_takes_once() {
        assert!(RIGHTS.iter().any(|(id, _)| *id == WSL2_RIGHT));
        let r0326 = m(&["power-helper-task", "power-helper-task@unattended", "power-helper-task@protected", "boot-task", "firewall-node", "firewall-miner", WEBVIEW2_RIGHT, "driver-install-task"]);
        assert_eq!(missing(Some(&r0326), RIGHTS), vec![WSL2_RIGHT.to_string()]);
        assert_eq!(prompts(Event::Install, Some(&r0326), RIGHTS), 1);
        let s = script(Path::new("C:\\p\\Ember\\igneum-app.exe"), Path::new("C:\\p\\Ember"), Path::new("C:\\u\\AppData\\Local\\igneum"));
        assert!(s.contains("wsl.exe --install --no-distribution --no-launch"), "the feature and the kernel only: {s}");
        assert!(s.contains("wsl.exe --status") && s.contains("wsl2: on"), "idempotent: an installed feature is a log line");
        assert!(!s.contains("Restart-Computer") && !s.contains("shutdown"), "the installer never restarts the PC by itself");
    }

    #[test]
    fn an_update_with_no_new_right_shows_no_prompt() {
        let installed = m(&RIGHTS.iter().map(|(i, _)| *i).collect::<Vec<_>>());
        assert_eq!(missing(Some(&installed), RIGHTS), Vec::<String>::new());
        assert_eq!(prompts(Event::Install, Some(&installed), RIGHTS), 0);
    }

    #[test]
    fn an_update_with_a_new_right_shows_exactly_one_prompt() {
        let installed = m(&RIGHTS.iter().map(|(i, _)| *i).collect::<Vec<_>>());
        let grown: Vec<(&str, &str)> = RIGHTS.iter().cloned().chain([("example-new-right", "a right a later build needs")]).collect();
        assert_eq!(missing(Some(&installed), &grown), vec!["example-new-right".to_string()]);
        assert_eq!(prompts(Event::Install, Some(&installed), &grown), 1);
        // and a manifest from an older build that lacks two rights still asks exactly once
        let older = m(&["power-helper-task"]);
        assert_eq!(missing(Some(&older), RIGHTS).len(), RIGHTS.len() - 1);
        assert_eq!(prompts(Event::Install, Some(&older), RIGHTS), 1);
    }

    #[test]
    fn the_manifest_round_trips_and_a_bad_file_reads_as_none() {
        let a = m(&["power-helper-task", "boot-task"]);
        assert_eq!(Manifest::parse(&a.to_json()), Some(a.clone()));
        assert!(a.to_json().contains("\"format\":\"igneum-rights-1\""));
        assert_eq!(Manifest::parse("not json"), None);
        assert_eq!(Manifest::parse("{}"), Some(Manifest::default()));
    }

    #[test]
    fn the_one_script_takes_every_right_and_is_idempotent() {
        let s = script(Path::new("C:\\p\\Ember\\igneum-app.exe"), Path::new("C:\\p\\Ember"), Path::new("C:\\u\\igneum"));
        assert!(s.contains("-TaskName 'Igneum Power Helper'"), "the Power Helper task");
        assert!(s.contains("-TaskName 'Ember (boot)'"), "the boot task");
        // the join's separator is the test host's (the box runs this on Linux), so the path is read by its file name
        assert!(s.contains("advfirewall firewall add rule name='Ember node'") && s.contains("Ember") && s.contains("igneumd.exe'"), "{s}");
        assert!(s.contains("advfirewall firewall add rule name='Ember pool'") && s.contains("igneum-miner.exe'"));
        assert!(s.contains("firewall delete rule name='Ember node'"), "the rule is replaced, never doubled");
        assert_eq!(s.matches("exit 0").count(), 1, "one exit at the end, the sub-scripts' own stripped");
        assert!(!s.contains("RunAs"), "the script itself never elevates: the step that runs it does, once (the bootstrapper's hidden run is a plain run)"); // console: a test string, not a spawn
        for (id, _) in RIGHTS {
            assert!(!id.is_empty() && id.chars().all(|c| c.is_ascii_lowercase() || c.is_ascii_digit() || c == '-' || c == '@' || c == '.'), "ids are stable lowercase words, with an @version suffix where the minimum is part of the right: {id}");
        }
        assert!(missing_note("firewall-node").contains("run the Ember installer again"));
    }
}
