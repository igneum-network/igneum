//! The engine starts at boot without a logon (an earlier release, MF-11's second half). the second Windows machine, 7 October 2026, 17:27 UK: a kernel
//! crash during the Intel driver install, the PC back at 17:28, and the per-user app then waited 67 minutes for a
//! logon with every card idle, because the only start was the Run key at logon and `--launch` always opened the window
//! host, which has no desktop before a logon.
//!
//! Windows, from an earlier release:
//!   - a per-user scheduled task `Ember (boot)` runs at system start under the user's own account with the S4U
//!     logon (no password stored, no logon needed, limited run level): `igneum-app.exe --launch --data-root <the
//!     user's %LOCALAPPDATA%\igneum>` (the data root is spelled out because an S4U session may not load the profile);
//!     registered by the engine at its start (unelevated, the user's own task) and again inside the one approved step
//!     the Power Helper uses, for the account that cannot register it alone;
//!   - `--launch` decides by the session: no interactive session (SESSIONNAME unset: session 0, the boot task, a
//!     service) runs the engine headless (`--boot`: no window host, no browser); a logon session opens the window host
//!     as before;
//!   - the window host, at logon, starts `igneum-app.exe --wrapper` as always; that engine finds the headless engine's
//!     app.url answering api/state and becomes a BRIDGE instead of a second engine: it prints the headless engine's
//!     URL and STATE lines to the host and relays the host's stdin commands (quit, pause, resume, detect) to the
//!     engine's API. The window closing (stdin gone) ends the bridge and leaves the engine mining; Quit in the tray
//!     quits the engine. A headless engine that stops answering ends the bridge with EXIT, and the host (an earlier release)
//!     starts `--wrapper` again, which then runs as a full engine.
//! The decisions are functions here so the tests drive them with the known-failed shape first.

use std::path::{Path, PathBuf};
use std::process::Command;
use std::time::Duration;

/// The boot task's name in the Windows Task Scheduler (per user).
pub const TASK_NAME: &str = "Ember (boot)";
/// How often the bridge reads api/state for a STATE line, and how many misses in a row end it.
pub const BRIDGE_POLL_S: u64 = 3;
pub const BRIDGE_MISSES: u32 = 4;

/// What `--launch` does.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum LaunchMode {
    /// start "Ember.exe" (the window host), which starts the engine
    Host,
    /// run the engine here, no window host, no browser (no interactive session, or no host next to the exe)
    Headless,
    /// run the engine here and open the dashboard in the default browser (a logon session without the window host)
    Browser,
}

/// The session a process runs in: `SESSIONNAME` is set in every interactive logon session ("Console", "RDP-Tcp#3")
/// and unset in session 0 (services, S4U scheduled tasks at boot).
pub fn interactive_session(session_name: Option<&str>) -> bool {
    session_name.map(|s| !s.trim().is_empty()).unwrap_or(false)
}

/// an earlier release: the window host only when someone is logged on; headless otherwise.
pub fn launch_mode(session_name: Option<&str>, host_exists: bool) -> LaunchMode {
    match (interactive_session(session_name), host_exists) {
        (true, true) => LaunchMode::Host,
        (true, false) => LaunchMode::Browser,
        (false, _) => LaunchMode::Headless,
    }
}

/// Before an earlier release, for the record: the host whenever it existed, whoever was or was not logged on.
pub fn legacy_launch_mode(host_exists: bool) -> LaunchMode {
    if host_exists { LaunchMode::Host } else { LaunchMode::Browser }
}

/// Does a closed stdin mean "the host went away" (quit)? Only for an engine a window host attached (`--wrapper`): a
/// headless engine (`--boot`, the boot task, a job's `--launch` with no session) owns itself and has no host to lose.
/// the second Windows machine, 7 October 2026, 19:09 to 19:11 UK: four engines started a minute apart each quit 3 s after "node started" with
/// "the window host went away (stdin closed)", their parent having closed the pipe at once.
pub fn stdin_close_quits(wrapper: bool, headless: bool) -> bool {
    wrapper && !headless
}

// ---- the boot task ---------------------------------------------------------------------------------------------------

fn ps_quote(s: &str) -> String {
    s.replace('\'', "''")
}

/// The PowerShell that registers the boot task: trigger at system start, principal the signed-in user with the S4U
/// logon (runs with nobody logged on, no password stored), limited run level, no time limit, one instance, hidden;
/// the action is the installed exe with `--launch --data-root <root>`.
pub fn register_script(exe: &Path, data_root: &Path) -> String {
    let exe_s = ps_quote(&exe.display().to_string());
    let dir = exe.parent().map(|d| ps_quote(&d.display().to_string())).unwrap_or_default();
    let root = ps_quote(&data_root.display().to_string());
    format!(
        "$a = New-ScheduledTaskAction -Execute '{exe_s}' -Argument '--launch --data-root \"{root}\"' -WorkingDirectory '{dir}'\r\n\
         $t = New-ScheduledTaskTrigger -AtStartup\r\n\
         $t.Delay = 'PT30S'\r\n\
         $p = New-ScheduledTaskPrincipal -UserId ([System.Security.Principal.WindowsIdentity]::GetCurrent().Name) -LogonType S4U -RunLevel Limited\r\n\
         $s = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -ExecutionTimeLimit (New-TimeSpan -Seconds 0) -MultipleInstances IgnoreNew -StartWhenAvailable -Hidden\r\n\
         Register-ScheduledTask -TaskName '{name}' -Action $a -Trigger $t -Principal $p -Settings $s -Force | Out-Null\r\n\
         exit 0\r\n",
        name = TASK_NAME
    )
}

/// The PowerShell that says whether the boot task is registered, enabled and its action's exe present (exit 0).
pub fn query_command() -> String {
    format!("$t = Get-ScheduledTask -TaskName '{TASK_NAME}' -ErrorAction SilentlyContinue; if ($t -and $t.State -ne 'Disabled' -and (Test-Path (($t.Actions[0].Execute).Trim('\"')))) {{ exit 0 }} else {{ exit 1 }}")
}

/// The PowerShell that removes the task (an uninstall, or Start at login switched off).
pub fn remove_command() -> String {
    format!("Unregister-ScheduledTask -TaskName '{TASK_NAME}' -Confirm:$false -ErrorAction SilentlyContinue; exit 0")
}

fn powershell(command: &str, limit: Duration) -> Option<(bool, String)> {
    let mut c = Command::new(crate::platform::tool("powershell"));
    c.args(["-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", command]);
    crate::platform::quiet(&mut c);
    let out = crate::detect::run_timeout(&mut c, None, limit)?;
    // run_timeout folds the streams; the exit code is read again through a second probe when needed
    Some((true, out))
}

/// Is the boot task registered (Windows only; false elsewhere)?
pub fn registered() -> bool {
    if !cfg!(windows) {
        return false;
    }
    let mut c = Command::new(crate::platform::tool("powershell"));
    c.args(["-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", &query_command()]);
    crate::platform::quiet(&mut c);
    c.status().map(|s| s.success()).unwrap_or(false)
}

/// Registers the boot task unelevated (the user's own task). Ok(true) registered now, Ok(false) already there,
/// Err(why) when Windows refused (an account without the batch-logon right: the one approved step registers it).
pub fn ensure_registered(exe: &Path, data_root: &Path) -> Result<bool, String> {
    if !cfg!(windows) {
        return Ok(false);
    }
    if registered() {
        return Ok(false);
    }
    let script = register_script(exe, data_root);
    let (_, out) = powershell(&script, Duration::from_secs(60)).ok_or("powershell did not answer")?;
    if registered() {
        Ok(true)
    } else {
        Err(format!("the boot task was not registered: {}", out.lines().last().unwrap_or("no reason given").trim()))
    }
}

/// The installed exe the task's action names (never a scratch copy), as the Power Helper picks it.
pub fn task_exe(running: &Path) -> PathBuf {
    crate::powertask::task_exe(running, &crate::powertask::install_candidates())
}

// ---- the bridge -------------------------------------------------------------------------------------------------------

/// The engine already running under the user's data root: its URL when app.url names one that answers api/state.
pub fn running_engine(app_dir: &Path) -> Option<String> {
    let url = std::fs::read_to_string(app_dir.join("app.url")).ok()?.trim().to_string();
    if !url.starts_with("http://127.0.0.1:") {
        return None;
    }
    let state = api_get(&url, "api/state")?;
    let v: serde_json::Value = serde_json::from_str(state.trim()).ok()?;
    v.get("version").and_then(|x| x.as_str()).map(|_| url)
}

fn api_get(url: &str, path: &str) -> Option<String> {
    crate::detect::run_timeout(Command::new(crate::platform::tool("curl")).args(["-fsS", "--max-time", "4", &format!("{url}{path}")]), None, Duration::from_secs(6)).filter(|o| !o.trim().is_empty())
}

fn api_post(url: &str, path: &str) -> bool {
    crate::detect::run_timeout(Command::new(crate::platform::tool("curl")).args(["-fsS", "--max-time", "6", "-X", "POST", "-H", "Content-Type: application/json", "-d", "{}", &format!("{url}{path}")]), None, Duration::from_secs(8)).is_some()
}

/// The host's stdin line as the engine's API path; None for a line the bridge does not relay.
pub fn command_path(line: &str) -> Option<&'static str> {
    match line.trim() {
        "quit" => Some("api/quit"),
        "pause" => Some("api/pause"),
        "resume" => Some("api/resume"),
        "detect" => Some("api/detect"),
        _ => None,
    }
}

/// The STATE line the host reads (engine.rs wrapper_state), built from the engine's api/state reply.
pub fn state_line(api_state: &serde_json::Value) -> String {
    let g = |p: &[&str]| -> serde_json::Value {
        let mut v = api_state;
        for k in p {
            v = match v.get(k) {
                Some(x) => x,
                None => return serde_json::Value::Null,
            };
        }
        v.clone()
    };
    serde_json::json!({
        "phase": g(&["phase"]), "mining": g(&["mining", "state"]), "paused": g(&["mining", "paused"]),
        "hash_total": g(&["mining", "hash_total"]), "accepted_total": g(&["mining", "accepted_total"]),
        "node": g(&["node", "state"]), "blocks": g(&["node", "blocks"]), "peers": g(&["node", "peers"]), "quitting": g(&["quitting"]),
        "update": g(&["update", "available"]), "bridge": true,
    })
    .to_string()
}

/// What the bridge does after one poll: carry on, or end (with EXIT when the engine is gone; silently when the host
/// closed its end, which leaves the engine mining).
#[derive(Debug, PartialEq, Eq)]
pub enum BridgeStep {
    Continue,
    EngineGone,
    HostGone,
}

pub fn bridge_step(misses: u32, stdin_closed: bool) -> BridgeStep {
    if stdin_closed {
        BridgeStep::HostGone
    } else if misses >= BRIDGE_MISSES {
        BridgeStep::EngineGone
    } else {
        BridgeStep::Continue
    }
}

/// Runs the bridge until the host or the engine goes. Returns the process exit code.
pub fn run_bridge(url: &str) -> i32 {
    use std::io::{BufRead, Write};
    use std::sync::atomic::{AtomicBool, Ordering};
    use std::sync::Arc;
    println!("URL {url}");
    let _ = std::io::stdout().flush();
    let closed = Arc::new(AtomicBool::new(false));
    {
        let closed = closed.clone();
        let url = url.to_string();
        std::thread::spawn(move || {
            let stdin = std::io::stdin();
            for line in stdin.lock().lines() {
                let Ok(l) = line else { break };
                if let Some(p) = command_path(&l) {
                    let _ = api_post(&url, p);
                }
            }
            closed.store(true, Ordering::Relaxed);
        });
    }
    let mut misses = 0u32;
    loop {
        match api_get(url, "api/state").and_then(|s| serde_json::from_str::<serde_json::Value>(s.trim()).ok()) {
            Some(v) => {
                misses = 0;
                println!("STATE {}", state_line(&v));
                let _ = std::io::stdout().flush();
                if v.get("quitting").and_then(|q| q.as_bool()).unwrap_or(false) {
                    // the engine is leaving (Quit from the tray relayed above, or its own update): the host wants EXIT
                    std::thread::sleep(Duration::from_secs(2));
                    misses = BRIDGE_MISSES;
                }
            }
            None => misses += 1,
        }
        match bridge_step(misses, closed.load(Ordering::Relaxed)) {
            BridgeStep::Continue => std::thread::sleep(Duration::from_secs(BRIDGE_POLL_S)),
            BridgeStep::EngineGone => {
                println!("EXIT");
                let _ = std::io::stdout().flush();
                return 0;
            }
            BridgeStep::HostGone => return 0,
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// Known-failed first: before an earlier release a boot with nobody logged on opened the window host (no desktop, no engine);
    /// the second Windows machine sat 67 minutes idle after its 17:28 UK self-reboot on 7 October 2026.
    #[test]
    fn before_0322_no_logon_meant_the_host_and_no_engine() {
        assert_eq!(legacy_launch_mode(true), LaunchMode::Host, "the host regardless of the session");
        assert_eq!(launch_mode(None, true), LaunchMode::Headless, "0.1.22: no session, the engine runs headless");
        assert_eq!(launch_mode(Some(""), true), LaunchMode::Headless);
    }

    /// Known-failed first: before an earlier release every `--wrapper` engine quit on a closed stdin, whoever had started it.
    #[test]
    fn a_headless_engine_never_reads_a_closed_stdin_as_the_host_leaving() {
        assert!(stdin_close_quits(true, false), "the window host's own engine: the host left, the engine follows (by design)");
        assert!(!stdin_close_quits(true, true), "0.1.22: headless, even with --wrapper on the line, stays up");
        assert!(!stdin_close_quits(false, false), "an engine with no host never had a stdin to lose");
        assert!(!stdin_close_quits(false, true));
    }

    #[test]
    fn a_logon_session_keeps_the_window_host_or_the_browser() {
        assert_eq!(launch_mode(Some("Console"), true), LaunchMode::Host);
        assert_eq!(launch_mode(Some("RDP-Tcp#3"), true), LaunchMode::Host);
        assert_eq!(launch_mode(Some("Console"), false), LaunchMode::Browser);
        assert!(interactive_session(Some("Console")) && !interactive_session(None));
    }

    #[test]
    fn the_boot_task_runs_at_startup_as_the_user_without_a_logon_and_names_the_data_root() {
        let s = register_script(Path::new("C:\\p\\Ember\\igneum-app.exe"), Path::new("C:\\u\\AppData\\Local\\igneum"));
        assert!(s.contains("-Execute 'C:\\p\\Ember\\igneum-app.exe' -Argument '--launch --data-root \"C:\\u\\AppData\\Local\\igneum\"'"), "{s}");
        assert!(s.contains("New-ScheduledTaskTrigger -AtStartup"), "at system start, not at logon");
        assert!(s.contains("-LogonType S4U -RunLevel Limited"), "the user's own token with nobody logged on, never elevated");
        assert!(s.contains("-ExecutionTimeLimit (New-TimeSpan -Seconds 0)") && s.contains("-MultipleInstances IgnoreNew") && s.contains("-Hidden"));
        assert!(s.contains(&format!("-TaskName '{TASK_NAME}'")));
        assert!(!s.contains("powershell.exe") && !s.contains("cmd /c"), "the action is the exe itself: no console window at boot");
        let q = query_command();
        assert!(q.contains("Test-Path") && q.contains("-ne 'Disabled'") && q.contains("exit 1"), "{q}");
        assert!(remove_command().starts_with("Unregister-ScheduledTask -TaskName 'Ember (boot)'"));
    }

    #[test]
    fn the_bridge_relays_the_hosts_commands_and_nothing_else() {
        assert_eq!(command_path("quit"), Some("api/quit"));
        assert_eq!(command_path(" pause \r"), Some("api/pause"));
        assert_eq!(command_path("resume"), Some("api/resume"));
        assert_eq!(command_path("detect"), Some("api/detect"));
        assert_eq!(command_path("elevated ok"), None, "the elevated answers belong to a real engine's host");
        assert_eq!(command_path("rm -rf"), None);
    }

    #[test]
    fn the_bridge_state_line_is_the_wrapper_state() {
        let st: serde_json::Value = serde_json::from_str(r#"{"version":"0.1.22","phase":"dashboard","mining":{"state":"mining","paused":false,"hash_total":121.0,"accepted_total":95511},"node":{"state":"synced","blocks":165529,"peers":4},"quitting":false,"update":{"available":false}}"#).unwrap();
        let line: serde_json::Value = serde_json::from_str(&state_line(&st)).unwrap();
        assert_eq!(line["phase"], "dashboard");
        assert_eq!(line["mining"], "mining");
        assert_eq!(line["hash_total"], 121.0);
        assert_eq!(line["accepted_total"], 95511);
        assert_eq!(line["node"], "synced");
        assert_eq!(line["blocks"], 165529);
        assert_eq!(line["quitting"], false);
        assert_eq!(line["bridge"], true);
        let partial: serde_json::Value = serde_json::from_str(r#"{"version":"0.1.22"}"#).unwrap();
        assert!(state_line(&partial).contains("\"phase\":null"), "a missing field is null, never a panic");
    }

    #[test]
    fn the_bridge_ends_with_exit_when_the_engine_is_gone_and_silently_when_the_host_is() {
        assert_eq!(bridge_step(0, false), BridgeStep::Continue);
        assert_eq!(bridge_step(BRIDGE_MISSES - 1, false), BridgeStep::Continue);
        assert_eq!(bridge_step(BRIDGE_MISSES, false), BridgeStep::EngineGone, "the host then starts a fresh --wrapper, which becomes a full engine");
        assert_eq!(bridge_step(0, true), BridgeStep::HostGone, "the window closed: the headless engine keeps mining");
        assert_eq!(bridge_step(BRIDGE_MISSES, true), BridgeStep::HostGone);
    }
}
