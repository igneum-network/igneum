//! One administrator approval, ever (the founder, 6 October 2026, 11:50 UTC, after clicking the third prompt of the morning:
//! "can we make sure all these popups are not needed in future?").
//!
//! What an earlier release does: Power control on raises one prompt and sets every cap in that step; but every later cap (an app
//! start, a reboot, a slider move) and every tune's helper is another elevated launch, so another prompt. This module
//! makes the first approval the last: the one elevated step also registers a per-user Windows scheduled task,
//! `Igneum Power Helper`, principal = the signed-in user (S4U since an earlier release: it runs whether or not that user is logged
//! on, so a start from a job's session is accepted), RunLevel Highest, no trigger, whose action is this very
//! executable with `--power-helper`. A task the user owns can be STARTED by the user's unelevated processes without a
//! prompt (`Start-ScheduledTask`), and it runs elevated; so every later cap and tune starts the task and talks to it
//! through the command file `<app data>/app/sweep/cmd.txt` (the protocol the an earlier release helper scripts spoke: `<seq> pl
//! <watts>`, `<seq> lgc <MHz>`, `<seq> rgc`, `quit`; plus `remove`, the kill switch). The task survives app restarts,
//! updates (the per-user installer replaces the exe in place; the task's action path is the install folder) and
//! reboots (a task, not a process). Power control off starts the task once and sends `remove`: the helper unregisters
//! the task (elevated) and exits; nothing is left behind.
//!
//! Threat note (what the helper will and will not run):
//! - The action is fixed at registration: the app's own exe in the install folder with `--power-helper`. The task
//!   has no trigger and no arguments from outside; only `Start-ScheduledTask` by the owning user starts it.
//! - The helper reads ONE file, `<app data>/app/sweep/cmd.txt`, in the user's own profile. Every command it accepts
//!   is a fixed verb with digit-only arguments: `pl <watts>` runs `nvidia-smi -i <dev> -pl <watts>`, `lgc <MHz>` runs
//!   `nvidia-smi -i <dev> -lgc 0,<MHz>`, `rgc` runs `nvidia-smi -i <dev> -rgc`, `quit` ends it, `remove` unregisters
//!   the task and ends it. The device index is digits only too (`dev <n>` sets it). No shell, no path, no string from
//!   the file reaches a process: `Command::new(nvidia-smi).args([...])`, never `cmd /c`.
//! - nvidia-smi is resolved to the driver's install path (platform::tool), never from PATH.
//! - What an attacker running as the user gains: the power limit and the clock cap of the user's own NVIDIA cards,
//!   within the ranges the driver allows, which the same user could set with one approved prompt anyway. Nothing
//!   else: no file, no process, no registry, no other binary.
//! - The helper exits after 20 idle minutes; a stale command file is cleared at start (sequence numbers must rise).
//! - Linux keeps pkexec per step (no scheduled task); macOS has no cap to set.
//!
//! V6-06 (the master's R1 review, 8 October 2026; the design the window lane built):
//! - The task's action is a PROTECTED copy of the exe and its runtime DLLs in %ProgramData%\Igneum\helper, made by the
//!   one elevated step with inheritance cut and Administrators and SYSTEM full, Users read and execute, owner
//!   Administrators (`register_script`). The per-user install under LOCALAPPDATA, which the user can overwrite, is
//!   never what the scheduler runs elevated.
//! - The copy is refreshed only through `reregister`, and only when the signed update manifest names the installed
//!   exe's sha256 (platforms.windows.engine_sha256, `refresh_allowed`); a swapped binary is refused with its reason in
//!   helper.log. An update whose manifest names no engine hash leaves the old protected copy in place.
//! - Every elevated script travels inline (-EncodedCommand, platform::encoded_command_args): no .ps1 is read from a
//!   folder the user can write between the write and the run.
//! - `quit` and `remove` carry a sequence like every other verb and pass the same rising-sequence guard; a stale
//!   line re-added to the file is skipped. The command file stays the IPC: its boundary is the user account (the
//!   file is in the user's profile, the task's principal is the user), the same boundary a per-user named pipe
//!   would draw; what matters is that no verb lets a writer choose a path, a hash or a binary, which holds.
//! - The window host is asInvoker (app/windows/host.manifest); the one prompt it raises for the engine's step is
//!   a child process, the host itself never holds rights.

use std::path::{Path, PathBuf};
use std::time::{Duration, Instant};

/// The task name in the Windows Task Scheduler (per user).
pub const TASK_NAME: &str = "Igneum Power Helper";
/// The helper ends after this long without a new command.
pub const IDLE_S: u64 = 20 * 60;
/// The heartbeat file the helper refreshes every poll; an engine judges "the helper runs" by it, never by a flag
/// of its own (6 October 2026, the first Windows machine: the flag said yes after the helper's idle exit, and commands went to nobody).
pub const ALIVE_FILE: &str = "helper.alive";
/// A heartbeat older than this is a dead helper.
pub const ALIVE_MAX_S: u64 = 4;

/// Every end of the helper is written here as `<unix> <code> <reason>`, the panic path included; the engine reads it
/// when a start gave no heartbeat and shows "power control: helper not running (<reason>)" (the first Windows machine, 7 October 2026,
/// 22:08 UK: six "helper started" lines, no command run after any, no exit line, the task reading Running with no
/// process; the engine toggled nothing and said nothing).
pub const EXIT_FILE: &str = "helper.exit";

pub fn write_exit(dir: &Path, now: u64, code: i32, reason: &str) {
    let _ = std::fs::write(dir.join(EXIT_FILE), format!("{now} {code} {}\n", reason.replace('\n', " ")));
}

/// The exit written at or after `since`, as (code, reason); None when there is none that recent (the process was ended
/// from outside, or crashed before any Rust ran: nothing of ours writes then).
pub fn exit_reason(text: &str, since: u64) -> Option<(i32, String)> {
    let t = text.trim();
    let mut it = t.splitn(3, ' ');
    let at: u64 = it.next()?.parse().ok()?;
    let code: i32 = it.next()?.parse().ok()?;
    let reason = it.next().unwrap_or("").trim().to_string();
    (at >= since).then_some((code, reason))
}

pub fn last_exit(dir: &Path, since: u64) -> Option<(i32, String)> {
    std::fs::read_to_string(dir.join(EXIT_FILE)).ok().and_then(|t| exit_reason(&t, since))
}

/// The notice the engine shows when the task was started and no heartbeat came: the helper's own exit reason when it
/// wrote one, else what a missing reason means and where Windows keeps the rest.
pub fn not_running_notice(exit: Option<(i32, String)>, waited_s: u64) -> String {
    match exit {
        Some((code, reason)) => format!("power control: helper not running ({reason}; exit {code}; the task gave no heartbeat within {waited_s} s)"),
        None => format!("power control: helper not running (the task gave no heartbeat within {waited_s} s and the helper wrote no exit reason: its process was ended from outside or crashed before it ran; Event Viewer, Application, id 1000 names the module, and TaskScheduler/Operational ids 201/202 carry the task's return code)"),
    }
}

/// The text of a panic payload.
pub fn panic_text(p: &(dyn std::any::Any + Send)) -> String {
    p.downcast_ref::<String>().cloned().or_else(|| p.downcast_ref::<&str>().map(|s| s.to_string())).unwrap_or_else(|| "a panic with no message".to_string())
}

/// Runs the helper body under a panic catch: a panic becomes a FAULT line in the log, an exit reason and exit 101,
/// never a silent end (known-failed first, the first Windows machine, 7 October 2026).
pub fn run_guarded(dir: &Path, log: &dyn Fn(&str), body: &mut dyn FnMut() -> i32) -> i32 {
    match std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| body())) {
        Ok(code) => code,
        Err(p) => {
            let reason = format!("panicked: {}", panic_text(&*p));
            log(&format!("FAULT helper: {reason}"));
            write_exit(dir, crate::platform::unix_now(), 101, &reason);
            let _ = std::fs::remove_file(dir.join(ALIVE_FILE));
            101
        }
    }
}

/// Writes the heartbeat: the unix time, as text.
pub fn beat(dir: &Path, now: u64) {
    let _ = std::fs::write(dir.join(ALIVE_FILE), now.to_string());
}

/// Reads a heartbeat: is the helper alive at `now`?
pub fn alive_at(text: &str, now: u64) -> bool {
    text.trim().parse::<u64>().map(|t| now.saturating_sub(t) <= ALIVE_MAX_S).unwrap_or(false)
}

/// Is the helper that reads `dir` alive now?
pub fn alive(dir: &Path) -> bool {
    std::fs::read_to_string(dir.join(ALIVE_FILE)).map(|t| alive_at(&t, crate::platform::unix_now())).unwrap_or(false)
}

/// Starts the task unless the helper already runs, then waits for a fresh heartbeat (up to `wait`). The engine
/// writes a command only after this says Ok: the helper skips what the file held before its start (a stale quit
/// is not a command), so a command written before the start would be skipped with it (6 October 2026, 17:55:25Z
/// on the first Windows machine: the first request of the first tune).
pub fn ensure_running(dir: &Path, wait: Duration) -> Result<(), String> {
    if alive(dir) {
        return Ok(());
    }
    let _ = std::fs::create_dir_all(dir);
    let since = crate::platform::unix_now();
    start()?;
    let until = Instant::now() + wait;
    while Instant::now() < until {
        std::thread::sleep(Duration::from_millis(250));
        if alive(dir) {
            return Ok(());
        }
    }
    // the reason the helper itself wrote on its way out (helper.exit, this start or later), else what a missing one means
    Err(not_running_notice(last_exit(dir, since), wait.as_secs()))
}

/// One parsed command from cmd.txt.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum HelperCmd {
    Dev(String),
    PowerLimit(u64),
    ClockCap(u64),
    ClockReset,
    /// Ember 2: the memory clock locked to one value (`-lmc m,m`), or reset (`-rmc`)
    MemClock(u64),
    MemReset,
    Quit,
    Remove,
    /// re-point the task at the installed exe (no path argument: the helper finds the install folder itself, so a
    /// writer of cmd.txt can never choose what runs elevated); 6 October 2026, run 6 registered a scratch copy
    Reregister,
    /// the unattended driver install (src/driverinstall.rs): a vendor WORD only; the helper resolves the file, hash,
    /// signer and arguments from the signed table itself
    Driver(String),
}

/// Parses one line: `<seq> <verb> [<digits>]` (the an earlier release form `<seq> <watts>` reads as a power limit; `quit` and
/// `remove` need no sequence). Anything that is not a fixed verb with digit-only arguments is None.
pub fn parse_line(line: &str) -> Option<(u64, HelperCmd)> {
    let t = line.trim();
    // V6-06: a bare quit or remove is no command; both carry a sequence and pass the guard like every verb
    let p: Vec<&str> = t.split_whitespace().collect();
    let digits = |s: &str| !s.is_empty() && s.len() <= 6 && s.chars().all(|c| c.is_ascii_digit());
    let seq: u64 = p.first().filter(|s| digits(s)).and_then(|s| s.parse().ok())?;
    match p.as_slice() {
        [_, w] if digits(w) => Some((seq, HelperCmd::PowerLimit(w.parse().ok()?))),
        [_, "pl", w] if digits(w) => Some((seq, HelperCmd::PowerLimit(w.parse().ok()?))),
        [_, "lgc", m] if digits(m) => Some((seq, HelperCmd::ClockCap(m.parse().ok()?))),
        [_, "rgc"] => Some((seq, HelperCmd::ClockReset)),
        [_, "lmc", m] if digits(m) => Some((seq, HelperCmd::MemClock(m.parse().ok()?))),
        [_, "rmc"] => Some((seq, HelperCmd::MemReset)),
        [_, "dev", d] if digits(d) => Some((seq, HelperCmd::Dev(d.to_string()))),
        [_, "quit"] => Some((seq, HelperCmd::Quit)),
        [_, "remove"] => Some((seq, HelperCmd::Remove)),
        [_, "reregister"] => Some((seq, HelperCmd::Reregister)),
        [_, "driver", v] if crate::driverinstall::vendor_ok(v) => Some((seq, HelperCmd::Driver(v.to_string()))),
        _ => None,
    }
}

/// The sequence number a command line carries on the wire. The helper runs a line only when its number is above
/// every number it has seen (its `last_seq`, seeded from the file at its start), so every writer must draw from
/// ONE monotonic space: the unix time modulo a million (six digits, what `parse_line` accepts), plus a small
/// offset per line. (F) 6 October 2026, 22:19Z on the first Windows machine: the tune path wrote its request index ("00 dev 1", "01 pl
/// 160", ...) while the cap path had written 305327 and up, so the helper skipped every tune command as stale and
/// both climbs stopped on "the helper did not run sequence 1 within 15 s". Wraps every 11.6 days; the helper's
/// idle exit (20 minutes) re-seeds it from the file, so a wrap costs at most one tune step.
pub fn wire_seq() -> u64 {
    crate::platform::unix_now() % 999_990
}

/// How many leading lines of `text` are still the ones the helper saw at its start: `skip` while the file only grew
/// and its first `skip` lines read as before; 0 when the file shrank OR was rewritten (the same count, other text).
/// the second Windows machine, 7 October 2026 (every tune refused since the 14:35Z boot, "helper started" three times with no command run):
/// the engine writes its four command lines with fs::write over a stale four-line file, so the count never dropped,
/// the skip never reset, and the helper read every new command as "present at start".
pub fn effective_skip(initial: &str, text: &str, skip: usize) -> usize {
    if text.lines().count() < skip {
        return 0;
    }
    let same_prefix = text.lines().take(skip).eq(initial.lines().take(skip));
    if same_prefix { skip } else { 0 }
}

/// The commands a helper acts on: the lines added after its start (`skip` = the line count at the start, 0 again
/// when the file shrank or was rewritten: effective_skip). A stale `quit` or `remove` from an earlier engine is never a command.
pub fn commands_after(text: &str, skip: usize) -> Vec<(u64, HelperCmd)> {
    let skip = if text.lines().count() < skip { 0 } else { skip };
    text.lines().skip(skip).filter_map(parse_line).collect()
}

/// The line the helper logs when the command file changed and nothing in it is a command for it: how many lines, how
/// many are skipped as present at its start, the highest sequence it has run, so a silent helper reads as a skip or a
/// stale sequence in its own log. None when the file is empty (a truncation is not a command) or something will run.
pub fn nothing_to_run_line(text: &str, skip: usize, last_seq: u64, cmds: &[(u64, HelperCmd)]) -> Option<String> {
    let n = text.lines().count();
    if n == 0 {
        return None;
    }
    let runnable = cmds.iter().any(|(s, c)| matches!(c, HelperCmd::Quit | HelperCmd::Remove) || *s > last_seq);
    if runnable {
        return None;
    }
    let stale: Vec<u64> = cmds.iter().map(|(s, _)| *s).filter(|s| *s <= last_seq).collect();
    Some(format!(
        "cmd.txt changed ({n} line(s)) but nothing to run: {skip} skipped as present at start, {} parsed, {} at or below the last sequence run {last_seq}",
        cmds.len(),
        stale.len()
    ))
}

/// The nvidia-smi arguments a command becomes (None for the verbs that run nothing).
pub fn smi_args(dev: &str, c: &HelperCmd) -> Option<Vec<String>> {
    match c {
        HelperCmd::PowerLimit(w) => Some(vec!["-i".into(), dev.into(), "-pl".into(), w.to_string()]),
        HelperCmd::ClockCap(m) => Some(vec!["-i".into(), dev.into(), "-lgc".into(), format!("0,{m}")]),
        HelperCmd::ClockReset => Some(vec!["-i".into(), dev.into(), "-rgc".into()]),
        HelperCmd::MemClock(m) => Some(vec!["-i".into(), dev.into(), "-lmc".into(), format!("{m},{m}")]),
        HelperCmd::MemReset => Some(vec!["-i".into(), dev.into(), "-rmc".into()]),
        _ => None,
    }
}

/// The executable the task should point at: the running one, unless it runs out of a jobs folder (a measurement
/// kit a job fetched: `<app data>\app\jobs\<id>\igneum-app-ember.exe`), in which case the installed app's own
/// exe beside it is the durable target (6 October 2026: the first registration on the first Windows machine came from such a kit).
pub fn task_exe(running: &Path, install_candidates: &[PathBuf]) -> PathBuf {
    // the running exe stands only when it is itself an install candidate (either separator, any case: the path
    // comes from Windows, the tests run on the Mac too); a kit under jobs\ or a measurement run's scratch copy
    // (run 6, 6 October 2026: igneum-tune-<stamp>\bin\igneum-app.exe) never becomes the task's action when an
    // installed exe exists
    let norm = |p: &Path| p.to_string_lossy().replace('/', "\\").to_ascii_lowercase();
    let me = norm(running);
    if install_candidates.iter().any(|p| norm(p) == me) {
        return running.to_path_buf();
    }
    if let Some(p) = install_candidates.iter().find(|p| p.is_file()) {
        return p.clone();
    }
    running.to_path_buf()
}

/// Where the installed exe lives: the per-user install, then the old administrator install.
pub fn install_candidates() -> Vec<PathBuf> {
    [
        std::env::var_os("LOCALAPPDATA").map(|l| PathBuf::from(l).join("Programs").join("Ember").join("igneum-app.exe")),
        std::env::var_os("ProgramFiles").map(|p| PathBuf::from(p).join("Ember").join("igneum-app.exe")),
    ]
    .into_iter()
    .flatten()
    .collect()
}

/// The PowerShell that prints the task's action (what `reregister` is proven by).
pub fn readback_command() -> String {
    format!("$t = Get-ScheduledTask -TaskName '{TASK_NAME}' -ErrorAction SilentlyContinue; if ($t) {{ Write-Output ($t.Actions[0].Execute + ' ' + $t.Actions[0].Arguments) }}; exit 0")
}

/// The protected folder's PowerShell expression: %ProgramData%\Igneum\helper.
pub fn protected_dir_expression() -> &'static str {
    "Join-Path $env:ProgramData 'Igneum\\helper'"
}

/// The files the protected copy needs: the engine and the mingw runtime it links (packaging/windows/make-payload.sh).
pub const PROTECTED_FILES: [&str; 4] = ["igneum-app.exe", "libstdc++-6.dll", "libgcc_s_seh-1.dll", "libwinpthread-1.dll"];

/// The PowerShell that registers the task (run inside the ONE elevated step, with the caps). `exe` is the installed
/// executable; the script copies it and its runtime DLLs into the protected folder (V6-06: inheritance cut,
/// Administrators and SYSTEM full, Users read and execute, owner Administrators) and points the task at that copy.
/// Principal: the signed-in user, S4U, highest run level; no trigger; may start on battery; one hour limit per run;
/// multiple starts are ignored while one runs.
pub fn register_script(exe: &Path) -> String {
    let exe_s = exe.display().to_string();
    let src = exe_s.rfind(['\\', '/']).map(|i| exe_s[..i].to_string()).unwrap_or_default().replace('\'', "''");
    let copies: String = PROTECTED_FILES
        .iter()
        .map(|f| format!("if (Test-Path -LiteralPath (Join-Path $src '{f}')) {{ Copy-Item -LiteralPath (Join-Path $src '{f}') -Destination (Join-Path $helperDir '{f}') -Force }}\r\n"))
        .collect();
    format!(
        "$src = '{src}'\r\n\
         $helperDir = {pdir}\r\n\
         New-Item -ItemType Directory -Force -Path $helperDir | Out-Null\r\n\
         & icacls.exe $helperDir /inheritance:r /grant '*S-1-5-32-544:(OI)(CI)F' '*S-1-5-18:(OI)(CI)F' '*S-1-5-32-545:(OI)(CI)RX' /setowner '*S-1-5-32-544' | Out-Null\r\n\
         {copies}\
         $helperExe = Join-Path $helperDir 'igneum-app.exe'\r\n\
         $a = New-ScheduledTaskAction -Execute $helperExe -Argument '--power-helper' -WorkingDirectory $helperDir\r\n\
         $p = New-ScheduledTaskPrincipal -UserId ([System.Security.Principal.WindowsIdentity]::GetCurrent().Name) -LogonType S4U -RunLevel Highest\r\n\
         $s = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -ExecutionTimeLimit (New-TimeSpan -Hours 1) -MultipleInstances IgnoreNew -Hidden\r\n\
         Register-ScheduledTask -TaskName '{name}' -Action $a -Principal $p -Settings $s -Force | Out-Null\r\n\
         exit 0\r\n",
        pdir = protected_dir_expression(),
        name = TASK_NAME
    )
}

/// V6-06: whether the installed exe (its sha256 `candidate`) may replace the protected copy: only when the signed
/// manifest verifies under `pub_hex` and its windows entry names that very hash. Ok carries the manifest's version.
pub fn refresh_allowed(manifest_bytes: &[u8], sig_hex: &str, pub_hex: &str, candidate: &str) -> Result<String, String> {
    let m = crate::manifest::verify_and_parse(manifest_bytes, sig_hex, pub_hex).map_err(|e| format!("the update manifest does not verify ({e}); the protected copy stays"))?;
    let want = m.windows.as_ref().map(|w| w.engine_sha256.clone()).unwrap_or_default();
    if want.is_empty() {
        return Err("the signed manifest names no engine hash (the manifest's windows entry (publish-release-manifest.sh)); the protected copy stays".into());
    }
    if want != candidate.to_ascii_lowercase() {
        return Err(format!("{candidate} is not the exe the signed manifest names ({want}); the protected copy stays"));
    }
    Ok(m.version)
}

/// The PowerShell that starts the task from an unelevated process (no prompt: the user owns the task).
pub fn start_command() -> String {
    format!("Start-ScheduledTask -TaskName '{TASK_NAME}'; exit 0")
}

/// The PowerShell that says whether the task is registered AND its action's exe is still there (exit 0), or not (exit 1).
/// A task whose exe has gone (the stale-task class of 7 October 2026: a logon task pointing at a folder that was not
/// there any more) reads as not registered, so the next cap apply with Power control on registers it again through the
/// one approved step instead of starting a task that cannot run.
pub fn query_command() -> String {
    format!("$t = Get-ScheduledTask -TaskName '{TASK_NAME}' -ErrorAction SilentlyContinue; if ($t -and $t.State -ne 'Disabled' -and (Test-Path (($t.Actions[0].Execute).Trim('\"')))) {{ exit 0 }} else {{ exit 1 }}")
}

/// The PowerShell the helper itself runs (elevated) on `remove`: the task goes, nothing is left.
pub fn remove_command() -> String {
    format!("Unregister-ScheduledTask -TaskName '{TASK_NAME}' -Confirm:$false; exit 0")
}

/// Is the task registered? Windows only; false elsewhere.
pub fn registered() -> bool {
    if !cfg!(windows) {
        return false;
    }
    let mut c = std::process::Command::new(crate::platform::tool("powershell"));
    c.args(["-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", &query_command()]);
    crate::platform::quiet(&mut c);
    c.status().map(|s| s.success()).unwrap_or(false)
}

/// Starts the task (no prompt). Ok when Start-ScheduledTask returned 0.
pub fn start() -> Result<(), String> {
    let mut c = std::process::Command::new(crate::platform::tool("powershell"));
    c.args(["-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", &start_command()]);
    crate::platform::quiet(&mut c);
    let out = c.output().map_err(|e| e.to_string())?;
    if out.status.success() {
        Ok(())
    } else {
        Err(format!("Start-ScheduledTask failed: {}", String::from_utf8_lossy(&out.stderr).trim()))
    }
}

/// The helper process (`igneum-app --power-helper`): polls `<dir>/cmd.txt` twice a second, runs the parsed commands
/// through nvidia-smi, logs what it ran to `<dir>/helper.log`, ends on `quit`, on `remove` (after unregistering the
/// task) or after 20 idle minutes. `dir` is `<app data>/app/sweep`.
pub fn run_helper(dir: &Path) -> i32 {
    let made = std::fs::create_dir_all(dir);
    let log_file = dir.join("helper.log");
    let log = |line: &str| {
        use std::io::Write;
        if let Ok(mut f) = std::fs::OpenOptions::new().append(true).create(true).open(&log_file) {
            let _ = writeln!(f, "{} {line}", crate::platform::unix_now());
        }
    };
    log("helper started (scheduled task, elevated)");
    // the facts a silent end leaves nothing of (the first Windows machine, 7 October 2026, 22:08 UK): which exe, which version, the folder,
    // what the command file held; and every exit from here on writes its reason to helper.exit
    let exe = std::env::current_exe().map(|p| p.display().to_string()).unwrap_or_else(|e| format!("unknown ({e})"));
    let cmd_state = match std::fs::read_to_string(dir.join("cmd.txt")) {
        Ok(t) => format!("{} line(s)", t.lines().count()),
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => "absent".to_string(),
        Err(e) => format!("unreadable ({e})"),
    };
    log(&format!("helper facts: exe {exe}, version {}, pid {}, dir {}{}, cmd.txt {cmd_state}", crate::engine::VERSION, std::process::id(), dir.display(), made.as_ref().map(|_| "").unwrap_or(" (NOT writable)"), ));
    if let Err(e) = made {
        write_exit(dir, crate::platform::unix_now(), 2, &format!("the helper folder could not be made: {e}"));
        return 2;
    }
    let mut code = 0;
    let mut body = || {
        code = run_helper_loop(dir, &log);
        code
    };
    run_guarded(dir, &log, &mut body)
}

fn run_helper_loop(dir: &Path, log: &dyn Fn(&str)) -> i32 {
    let cmd_file = dir.join("cmd.txt");
    beat(dir, crate::platform::unix_now());
    // a stale file from an earlier run is not a command: only lines ADDED after the start count. 6 October 2026,
    // the first Windows machine 17:55:55Z: a helper that started after an engine had written `quit` read that line and exited in the
    // same second, and every later start did the same; so the lines present at the start are skipped whole
    // (quit and remove included), and a file that shrinks starts the count again
    let initial = std::fs::read_to_string(&cmd_file).unwrap_or_default();
    let mut last_seq: u64 = initial.lines().filter_map(parse_line).map(|(s, _)| s).max().unwrap_or(0);
    let mut skip = initial.lines().count();
    let initial_text = initial.clone();
    let mut last_text = initial;
    let mut dev = "0".to_string();
    let mut idle = Instant::now();
    let smi = crate::platform::tool("nvidia-smi");
    loop {
        let text = std::fs::read_to_string(&cmd_file).unwrap_or_default();
        if text != last_text {
            skip = effective_skip(&initial_text, &text, skip);
            last_text = text.clone();
            let cmds = commands_after(&text, skip);
            // a changed file that yields nothing is said, never silent (the first Windows machine, 7 October 2026, 21:08Z to 21:46Z on the
            // an earlier release helper: six starts, every 2-line rewrite read as "present at start", no line after "helper started")
            if let Some(line) = nothing_to_run_line(&text, skip, last_seq, &cmds) {
                log(&line);
            }
            for (seq, c) in cmds {
                // V6-06: the rising-sequence guard first, quit and remove included
                if seq <= last_seq {
                    continue;
                }
                match c {
                    HelperCmd::Quit => {
                        log("quit");
                        write_exit(dir, crate::platform::unix_now(), 0, "quit (the engine asked)");
                        let _ = std::fs::remove_file(dir.join(ALIVE_FILE));
                        return 0;
                    }
                    HelperCmd::Remove => {
                        let mut p = std::process::Command::new(crate::platform::tool("powershell"));
                        p.args(["-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", &remove_command()]);
                        crate::platform::quiet(&mut p);
                        let ok = p.status().map(|s| s.success()).unwrap_or(false);
                        log(&format!("remove: the task is {}", if ok { "unregistered" } else { "still registered (Unregister-ScheduledTask failed)" }));
                        write_exit(dir, crate::platform::unix_now(), if ok { 0 } else { 1 }, if ok { "remove (the task unregistered itself)" } else { "remove failed (Unregister-ScheduledTask returned an error)" });
                        let _ = std::fs::remove_file(dir.join(ALIVE_FILE));
                        return if ok { 0 } else { 1 };
                    }
                    HelperCmd::Reregister => {
                        last_seq = seq;
                        idle = Instant::now();
                        let Some(target) = install_candidates().into_iter().find(|p| p.is_file()) else {
                            log(&format!("{seq} reregister: no installed exe found"));
                            continue;
                        };
                        // V6-06: the protected copy is refreshed only when the signed manifest names this exe's hash
                        let updates = dir.parent().map(|p| p.join("updates")).unwrap_or_default();
                        let verdict = match (std::fs::read(updates.join("manifest.json")), std::fs::read_to_string(updates.join("manifest.json.sig")), crate::manifest::sha256_file(&target)) {
                            (Ok(b), Ok(sg), Ok(h)) => refresh_allowed(&b, sg.trim(), crate::edition::ota_key(), &h),
                            (Err(e), _, _) | (_, Err(e), _) | (_, _, Err(e)) => Err(format!("no verified manifest or hash to check against ({e}); the protected copy stays")),
                        };
                        let version = match verdict {
                            Ok(v) => v,
                            Err(e) => {
                                log(&format!("{seq} reregister refused: {e}"));
                                continue;
                            }
                        };
                        log(&format!("{seq} reregister: {} is the exe manifest {version} names; refreshing the protected copy", target.display()));
                        let ok = {
                            let mut p = std::process::Command::new(crate::platform::tool("powershell"));
                            p.args(crate::platform::encoded_command_args(&register_script(&target)));
                            crate::platform::quiet(&mut p);
                            p.status().map(|s| s.success()).unwrap_or(false)
                        };
                        let mut q = std::process::Command::new(crate::platform::tool("powershell"));
                        q.args(["-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", &readback_command()]);
                        crate::platform::quiet(&mut q);
                        let now = q.output().map(|o| String::from_utf8_lossy(&o.stdout).trim().to_string()).unwrap_or_default();
                        log(&format!("{seq} reregister {}: the task now runs {now}", if ok { "ok" } else { "failed" }));
                    }
                    HelperCmd::Driver(v) => {
                        last_seq = seq;
                        crate::driverinstall::run_in_helper(dir, seq, &v, &log);
                        idle = Instant::now();
                    }
                    HelperCmd::Dev(d) => {
                        last_seq = seq;
                        idle = Instant::now();
                        dev = d;
                        log(&format!("{seq} dev {dev}"));
                    }
                    other => {
                        last_seq = seq;
                        idle = Instant::now();
                        let args = smi_args(&dev, &other).unwrap_or_default();
                        let mut p = std::process::Command::new(&smi);
                        p.args(&args);
                        crate::platform::quiet(&mut p);
                        let out = p.output().map(|o| format!("{}{}", String::from_utf8_lossy(&o.stdout), String::from_utf8_lossy(&o.stderr))).unwrap_or_else(|e| e.to_string());
                        log(&format!("{seq} nvidia-smi {} : {}", args.join(" "), out.replace('\n', " ").trim()));
                    }
                }
            }
        }
        if idle.elapsed() >= Duration::from_secs(IDLE_S) {
            log("idle 20 min: exit (the engine starts the task again when it needs it)");
            write_exit(dir, crate::platform::unix_now(), 0, "idle 20 min");
            let _ = std::fs::remove_file(dir.join(ALIVE_FILE));
            return 0;
        }
        beat(dir, crate::platform::unix_now());
        std::thread::sleep(Duration::from_millis(500));
    }
}

/// Where the command file lives for a data root.
pub fn sweep_dir(app_dir: &Path) -> PathBuf {
    app_dir.join("sweep")
}

/// The one folder the task's helper reads: `<fixed data root>/app/sweep` (never under IGNEUM_APP_DATA). Every
/// engine that talks to the task writes its command file here; the `--power-helper` entry reads here.
pub fn helper_dir() -> PathBuf {
    sweep_dir(&crate::platform::fixed_data_root().join("app"))
}

#[cfg(test)]
mod tests {
    use super::*;

    /// Known failed first (V6-06, the master's R1 review, 8 October 2026): a bare `quit` or `remove` carried no sequence
    /// and so skipped the guard every other verb passes; a stale line re-added to the file ended the helper. Now every
    /// verb carries a rising sequence, quit and remove included.
    #[test]
    fn quit_and_remove_carry_a_sequence_and_a_stale_one_is_ignored() {
        assert_eq!(parse_line("quit"), None, "a bare quit is not a command");
        assert_eq!(parse_line("remove"), None, "a bare remove is not a command");
        assert_eq!(parse_line("12 quit"), Some((12, HelperCmd::Quit)));
        assert_eq!(parse_line("12 remove"), Some((12, HelperCmd::Remove)));
        assert_eq!(parse_line("quit 12"), None);
        // the loop itself: a stale quit (sequence at or under the highest seen) is skipped; a rising one ends the helper
        let dir = std::env::temp_dir().join(format!("igneum-helper-seq-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&dir);
        std::fs::create_dir_all(&dir).unwrap();
        std::fs::write(dir.join("cmd.txt"), "5 dev 0\n").unwrap();
        let d2 = dir.clone();
        let t = std::thread::spawn(move || run_helper_loop(&d2, &|_| {}));
        std::thread::sleep(Duration::from_millis(900));
        std::fs::write(dir.join("cmd.txt"), "5 dev 0\n3 quit\n").unwrap();
        std::thread::sleep(Duration::from_millis(1500));
        assert!(!t.is_finished(), "a quit with a stale sequence must not end the helper");
        std::fs::write(dir.join("cmd.txt"), "5 dev 0\n3 quit\n6 quit\n").unwrap();
        let started = Instant::now();
        while !t.is_finished() && started.elapsed() < Duration::from_secs(10) {
            std::thread::sleep(Duration::from_millis(100));
        }
        assert!(t.is_finished(), "a quit with a rising sequence ends the helper");
        assert_eq!(t.join().unwrap(), 0);
        let _ = std::fs::remove_dir_all(&dir);
    }

    /// Known failed first (V6-06): the task's action was the per-user install's exe under LOCALAPPDATA, which the user
    /// (and anything running as the user) can overwrite, and the scheduler then ran it elevated with no prompt. The one
    /// elevated step now copies the exe and its runtime DLLs into %ProgramData%\Igneum\helper, a folder only
    /// administrators and SYSTEM may write (inheritance cut, Users read and execute), and the task runs that copy.
    #[test]
    fn the_task_runs_a_protected_copy_the_user_cannot_write() {
        let s = register_script(Path::new(r"C:\Work\Admin\AppData\Local\Programs\Ember\igneum-app.exe"));
        assert!(s.contains("Join-Path $env:ProgramData 'Igneum\\helper'"), "{s}");
        assert!(s.contains("icacls") && s.contains("/inheritance:r"), "{s}");
        for grant in ["*S-1-5-32-544:(OI)(CI)F", "*S-1-5-18:(OI)(CI)F", "*S-1-5-32-545:(OI)(CI)RX"] {
            assert!(s.contains(grant), "missing {grant}: {s}");
        }
        assert!(!s.contains("(OI)(CI)M") && !s.contains("S-1-5-32-545:(OI)(CI)F"), "users never get modify or full: {s}");
        for f in ["igneum-app.exe", "libstdc++-6.dll", "libgcc_s_seh-1.dll", "libwinpthread-1.dll"] {
            assert!(s.contains(&format!("Copy-Item -LiteralPath (Join-Path $src '{f}')")), "{f} is not copied: {s}");
        }
        assert!(s.contains("$src = 'C:\\Work\\Admin\\AppData\\Local\\Programs\\Ember'"), "{s}");
        assert!(s.contains("-Execute $helperExe -Argument '--power-helper' -WorkingDirectory $helperDir"), "{s}");
        assert!(!s.contains("-Execute 'C:\\Work"), "the action is never the user-writable exe: {s}");
        assert_eq!(protected_dir_expression(), "Join-Path $env:ProgramData 'Igneum\\helper'");
    }

    /// Known failed first (V6-06): the helper's `reregister` copied whatever exe sat in the install folder into the
    /// task's action. Now the protected copy is refreshed only when the signed manifest names the exe's sha256
    /// (platforms.windows.engine_sha256), so a swapped binary never becomes the elevated one.
    #[test]
    fn a_refresh_of_the_protected_copy_needs_the_signed_manifest_to_name_the_exe() {
        use ed25519_dalek::{Signer, SigningKey};
        let sk = SigningKey::from_bytes(&[7u8; 32]);
        let pk = crate::manifest::hex_encode(sk.verifying_key().as_bytes());
        let good = "ab".repeat(32);
        let text = format!(r#"{{"version":"0.2.2","platforms":{{"windows":{{"url":"https://x/s.exe","sha256":"{}","size":1,"kind":"inno-setup","engine_sha256":"{good}"}}}}}}"#, "cd".repeat(32));
        let sig = crate::manifest::hex_encode(&sk.sign(text.as_bytes()).to_bytes());
        assert_eq!(refresh_allowed(text.as_bytes(), &sig, &pk, &good), Ok("0.2.2".to_string()));
        assert!(refresh_allowed(text.as_bytes(), &sig, &pk, &"ef".repeat(32)).unwrap_err().contains("not the exe the signed manifest names"));
        let other = crate::manifest::hex_encode(SigningKey::from_bytes(&[9u8; 32]).verifying_key().as_bytes());
        assert!(refresh_allowed(text.as_bytes(), &sig, &other, &good).is_err(), "a signature under another key is refused");
        let bare = format!(r#"{{"version":"0.2.2","platforms":{{"windows":{{"url":"https://x/s.exe","sha256":"{}","size":1,"kind":"inno-setup"}}}}}}"#, "cd".repeat(32));
        let sig2 = crate::manifest::hex_encode(&sk.sign(bare.as_bytes()).to_bytes());
        assert!(refresh_allowed(bare.as_bytes(), &sig2, &pk, &good).unwrap_err().contains("names no engine"), "a manifest without the engine hash refreshes nothing");
    }

    /// V6-06: the window host stays unprivileged (asInvoker; a prompt it raises for the engine's one step is a child, not
    /// the host); every elevated script of the app travels inline in the command line, never as a file under a folder
    /// the user can write between the write and the run.
    #[test]
    fn the_window_host_is_unprivileged_and_elevated_scripts_travel_inline() {
        let manifest = include_str!("../../windows/host.manifest");
        assert!(manifest.contains(r#"<requestedExecutionLevel level="asInvoker" uiAccess="false"/>"#), "{manifest}");
        assert!(!manifest.contains("requireAdministrator") && !manifest.contains("highestAvailable"));
        let args = crate::platform::encoded_command_args("exit 0");
        assert_eq!(args, ["-NoProfile", "-ExecutionPolicy", "Bypass", "-EncodedCommand", "ZQB4AGkAdAAgADAA"]);
        assert!(!register_script(Path::new(r"C:\p\igneum-app.exe")).contains("-File "), "no script file is run");
    }

    /// Known-failed first (the first Windows machine, 7 October 2026, 22:08 UK): the helper's process died at once after each of six starts
    /// with no line after "helper started", and the engine toggled nothing and said nothing. Now a panic is a FAULT
    /// line, an exit reason and exit 101, every planned exit writes its reason, and the engine's notice carries it.
    #[test]
    fn a_helper_that_dies_at_start_leaves_its_reason_and_the_engine_names_it() {
        let dir = std::env::temp_dir().join(format!("igneum-helper-exit-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&dir);
        std::fs::create_dir_all(&dir).unwrap();
        let lines = std::sync::Mutex::new(Vec::new());
        let log = |l: &str| lines.lock().unwrap().push(l.to_string());
        let mut body = || -> i32 { panic!("the cmd file lock was held by pid 4242") };
        let code = run_guarded(&dir, &log, &mut body);
        assert_eq!(code, 101);
        let logged = lines.lock().unwrap().join("\n");
        assert!(logged.contains("FAULT helper: panicked: the cmd file lock was held by pid 4242"), "{logged}");
        let since = crate::platform::unix_now() - 5;
        let exit = last_exit(&dir, since).expect("the exit reason is written");
        assert_eq!(exit.0, 101);
        assert!(exit.1.starts_with("panicked: the cmd file lock"));
        assert!(!dir.join(ALIVE_FILE).exists() || !alive(&dir), "no heartbeat survives the exit");
        // the engine's notice: the reason when there is one, the meaning of none when there is none
        let n = not_running_notice(Some(exit), 12);
        assert!(n.starts_with("power control: helper not running (panicked: the cmd file lock was held by pid 4242; exit 101;"), "{n}");
        assert!(n.contains("no heartbeat"), "helper_fault_line still reads it as the helper not answering: {n}");
        let none = not_running_notice(None, 12);
        assert!(none.starts_with("power control: helper not running (") && none.contains("ended from outside or crashed before it ran") && none.contains("1000"), "{none}");
        // an older exit is not this start's
        assert_eq!(exit_reason("1000 0 idle 20 min", 2000), None);
        assert_eq!(exit_reason("3000 0 idle 20 min", 2000), Some((0, "idle 20 min".to_string())));
        // a planned end writes its reason too
        write_exit(&dir, 5000, 0, "quit (the engine asked)");
        assert_eq!(last_exit(&dir, 4000), Some((0, "quit (the engine asked)".to_string())));
        let _ = std::fs::remove_dir_all(&dir);
    }

    /// the first Windows machine, 7 October 2026, 21:08Z to 21:46Z, known-failed first on the an earlier release rule: the helper started over the 2-line
    /// restore ("423698 dev 0", "423699 rgc"), the next writer put 2 fresh lines in its place, and the old rule (skip the
    /// first N lines whenever the file holds N or more) read both as present at start: six starts, one "helper started"
    /// line each, nothing after. The an earlier release rule (effective_skip) runs them, and when a changed file truly yields nothing
    /// the helper says so in its log instead of nothing.
    #[test]
    fn a_two_line_rewrite_over_a_two_line_start_runs_and_a_silent_change_is_logged() {
        let initial = "423698 dev 0\n423699 rgc\n";
        let rewritten = "427312 dev 1\n427313 rgc\n";
        let old_rule_skip = initial.lines().count();
        assert_eq!(commands_after(rewritten, old_rule_skip), vec![], "the 0.1.20 rule: both lines present at start, the helper silent");
        let skip = effective_skip(initial, rewritten, old_rule_skip);
        assert_eq!(skip, 0, "0.1.21: a rewritten prefix resets the skip");
        let cmds = commands_after(rewritten, skip);
        assert_eq!(cmds, vec![(427312, HelperCmd::Dev("1".into())), (427313, HelperCmd::ClockReset)]);
        assert_eq!(nothing_to_run_line(rewritten, skip, 423699, &cmds), None, "something runs: no complaint");
        // the cases that still yield nothing are said: stale sequences, and lines present at start
        let stale = commands_after("423690 dev 1\n423691 rgc\n", 0);
        let l = nothing_to_run_line("423690 dev 1\n423691 rgc\n", 0, 423699, &stale).unwrap();
        assert_eq!(l, "cmd.txt changed (2 line(s)) but nothing to run: 0 skipped as present at start, 2 parsed, 2 at or below the last sequence run 423699");
        let l = nothing_to_run_line(rewritten, 2, 423699, &commands_after(rewritten, 2)).unwrap();
        assert!(l.contains("2 skipped as present at start, 0 parsed"), "{l}");
        assert_eq!(nothing_to_run_line("", 0, 1, &[]), None, "a truncation is not a command and not a complaint");
        assert_eq!(nothing_to_run_line("10 quit\n", 0, 9, &commands_after("10 quit\n", 0)), None, "quit runs");
        assert!(include_str!("powertask.rs").matches("nothing_to_run_line(").count() >= 2, "the loop logs it");
    }

    /// A helper started over a file that holds `remove` skips it (a line present at the start is never a command, 6
    /// October 2026), so the engine writes `remove` only once the helper's heartbeat is there: start first, then write.
    #[test]
    fn remove_is_written_after_the_heartbeat_not_before_the_start() {
        assert_eq!(commands_after("5 remove\n", 1), vec![], "the remove present at the start is skipped");
        assert_eq!(commands_after("5 remove\n", 0), vec![(5, HelperCmd::Remove)], "a remove added after the start runs");
        let s = include_str!("engine.rs");
        let off = s.find("fn power_control_off").unwrap();
        let body = &s[off..off + 2500];
        let ensure = body.find("ensure_running").expect("the off path waits for the heartbeat");
        let write = body.find(" remove\\n\"").expect("then writes remove (with its sequence, V6-06)");
        assert!(ensure < write, "the heartbeat comes before the remove line");
    }

    /// Known-failed first (7 October 2026, 19:5x UK): the helper knew no driver verb, so a driver install needed an
    /// elevated prompt. The verb carries a vendor WORD only: the helper resolves the file, the hash, the signer and the
    /// arguments from the signed table itself, so a writer of cmd.txt can never choose what runs elevated.
    #[test]
    fn the_helper_takes_a_driver_verb_for_a_vendor_word_and_nothing_else() {
        assert_eq!(parse_line("305327 driver intel"), Some((305327, HelperCmd::Driver("intel".into()))));
        assert_eq!(parse_line("305328 driver nvidia"), Some((305328, HelperCmd::Driver("nvidia".into()))));
        assert_eq!(parse_line("305329 driver amd"), Some((305329, HelperCmd::Driver("amd".into()))));
        assert_eq!(parse_line("305330 driver C:\\evil.exe"), None, "a path is not a vendor word");
        assert_eq!(parse_line("305331 driver apple"), None, "no installer for that vendor");
        assert_eq!(parse_line("driver intel"), None, "a sequence number is required");
        assert_eq!(smi_args("0", &HelperCmd::Driver("intel".into())), None, "a driver verb is never an nvidia-smi call");
    }

    #[test]
    fn only_fixed_verbs_with_digit_arguments_parse() {
        assert_eq!(parse_line("7 pl 460"), Some((7, HelperCmd::PowerLimit(460))));
        assert_eq!(parse_line("8 lgc 2472"), Some((8, HelperCmd::ClockCap(2472))));
        assert_eq!(parse_line("9 rgc"), Some((9, HelperCmd::ClockReset)));
        assert_eq!(parse_line("10 lmc 14001"), Some((10, HelperCmd::MemClock(14001))));
        assert_eq!(parse_line("11 rmc"), Some((11, HelperCmd::MemReset)));
        assert_eq!(smi_args("0", &HelperCmd::MemClock(14001)).unwrap(), vec!["-i", "0", "-lmc", "14001,14001"]);
        assert_eq!(smi_args("0", &HelperCmd::MemReset).unwrap(), vec!["-i", "0", "-rmc"]);
        assert_eq!(parse_line("3 dev 1"), Some((3, HelperCmd::Dev("1".into()))));
        assert_eq!(parse_line("5 403"), Some((5, HelperCmd::PowerLimit(403))), "the 0.1.9 form");
        assert_eq!(parse_line("7 quit"), Some((7, HelperCmd::Quit)));
        assert_eq!(parse_line("7 remove"), Some((7, HelperCmd::Remove)));
        assert_eq!(parse_line("12 reregister"), Some((12, HelperCmd::Reregister)));
        // a stale quit present at the start is skipped; a quit added later counts; a rewritten (shorter) file counts whole
        assert_eq!(commands_after("5 dev 0\n6 pl 460\nquit\n", 3), vec![]);
        assert_eq!(commands_after("5 dev 0\n6 pl 460\nquit\n7 dev 1\n", 3), vec![(7, HelperCmd::Dev("1".into()))]);
        assert_eq!(commands_after("quit\n", 3), vec![], "a bare quit is no command (V6-06)");
        assert_eq!(commands_after("8 quit\n", 0), vec![(8, HelperCmd::Quit)]);
        // the second Windows machine, 7 October 2026: the known-failed shape first. A stale four-line file at the helper's start, then the engine's
        // four-line rewrite: the count never dropped, so the old rule skipped every new command
        let stale = "401000 dev 0\n401001 pl 460\n401002 rgc\n401003 rmc\n";
        let fresh = "401888 dev 0\n401889 pl 575\n401890 rgc\n401891 rmc\n";
        assert_eq!(commands_after(fresh, 4), vec![], "the old rule: a same-length rewrite is invisible");
        assert_eq!(effective_skip(stale, fresh, 4), 0, "a rewrite resets the skip");
        assert_eq!(commands_after(fresh, effective_skip(stale, fresh, 4)).len(), 4, "every new command runs");
        // the file only grew: the stale prefix stays skipped (a stale quit at start is never a command)
        let grown = format!("{stale}401888 dev 0\n");
        assert_eq!(effective_skip(stale, &grown, 4), 4);
        assert_eq!(commands_after(&grown, effective_skip(stale, &grown, 4)), vec![(401888, HelperCmd::Dev("0".into()))]);
        assert_eq!(effective_skip("quit\n", "quit\n401888 dev 0\n", 1), 1, "a stale quit stays skipped while the file only grows");
        assert_eq!(effective_skip(stale, "401888 dev 0\n", 4), 0, "a shorter file resets as before");
        assert_eq!(effective_skip("", fresh, 0), 0);
        // the heartbeat: fresh within ALIVE_MAX_S, dead after, dead when unreadable
        // (F) every wire number parses (six digits at most) and leaves room for the four lines of a tune step
        let w = wire_seq();
        assert!(w + 3 <= 999_999 && parse_line(&format!("{} rmc", w + 3)).is_some());
        assert!(alive_at("1791309325", 1791309325 + ALIVE_MAX_S));
        assert!(!alive_at("1791309325", 1791309325 + ALIVE_MAX_S + 1));
        assert!(!alive_at("", 1791309325));
        assert!(!alive_at("soon", 1791309325));
        assert_eq!(parse_line("12 reregister C:\\evil.exe"), None, "no path argument: the helper picks the install folder itself");
        assert_eq!(smi_args("0", &HelperCmd::Reregister), None);
        assert!(readback_command().contains("Actions[0].Execute"));
        // the helper's folder never follows IGNEUM_APP_DATA (the task has no environment of ours)
        assert!(helper_dir().ends_with(Path::new("app").join("sweep")));
        assert!(helper_dir().starts_with(crate::platform::fixed_data_root()));
        // nothing else: no shell, no path, no string argument, no oversized number
        for bad in ["7 pl 460; calc", "7 pl -460", "7 pl 4.60", "7 lgc 0,2472", "7 rm C:\\x", "x pl 460", "7 pl", "7 lgc 12345678", "7 dev ../1", "", "7 pl 460 extra"] {
            assert_eq!(parse_line(bad), None, "{bad:?}");
        }
    }

    #[test]
    fn the_arguments_reach_nvidia_smi_as_a_list_never_a_shell() {
        assert_eq!(smi_args("0", &HelperCmd::PowerLimit(460)).unwrap(), vec!["-i", "0", "-pl", "460"]);
        assert_eq!(smi_args("1", &HelperCmd::ClockCap(2472)).unwrap(), vec!["-i", "1", "-lgc", "0,2472"]);
        assert_eq!(smi_args("1", &HelperCmd::ClockReset).unwrap(), vec!["-i", "1", "-rgc"]);
        assert_eq!(smi_args("0", &HelperCmd::Quit), None);
        assert_eq!(smi_args("0", &HelperCmd::Remove), None);
        assert_eq!(smi_args("0", &HelperCmd::Dev("1".into())), None);
    }

    #[test]
    fn the_registration_is_per_user_highest_no_trigger_fixed_action() {
        let s = register_script(Path::new(r"C:\Work\Admin\AppData\Local\Programs\Ember\igneum-app.exe"));
        // V6-06: the action is the protected copy under %ProgramData%, taken from the install folder by the elevated step
        assert!(s.contains("-Execute $helperExe -Argument '--power-helper' -WorkingDirectory $helperDir"), "{s}");
        assert!(s.contains("$src = 'C:\\Work\\Admin\\AppData\\Local\\Programs\\Ember'"), "{s}");
        // an earlier release (main, 7 October 2026, the first Windows machine): the task runs whether or not a user is logged on (S4U, the user's own token,
        // no stored password), so a start from a job's session or the engine's own tune is accepted; known-failed first
        // (the Interactive-only form)
        assert!(s.contains("-RunLevel Highest") && s.contains("-LogonType S4U"), "{s}");
        assert!(!s.contains("-LogonType Interactive"), "the interactive-only form is gone: {s}");
        assert!(s.contains("[System.Security.Principal.WindowsIdentity]::GetCurrent().Name"), "the signed-in user, never a literal");
        assert!(!s.contains("-Trigger"), "no trigger: only the app starts it");
        assert!(s.contains("-MultipleInstances IgnoreNew") && s.contains("-ExecutionTimeLimit"), "{s}");
        assert!(s.contains(&format!("-TaskName '{TASK_NAME}'")));
        // no window at logon or at a start (the founder, 7 October 2026, the second Windows machine's Terminal window): the action is the app's own exe,
        // a windows-subsystem program with no console, never powershell.exe, cmd.exe or a `start` of a batch file
        assert!(s.contains("-Argument '--power-helper'"), "{s}");
        assert!(!s.contains("powershell.exe") && !s.contains("cmd.exe") && !s.contains("cmd /c start"), "the task's action must be the exe itself: {s}");
        assert!(s.contains("-Hidden"), "the task is hidden in the scheduler too");
        // the registered probe also wants the action's exe on disk and the task enabled (the stale-task class)
        let q = query_command();
        assert!(q.contains("Test-Path (($t.Actions[0].Execute).Trim") && q.contains("$t.State -ne 'Disabled'") && q.contains("exit 1"), "{q}");
        // a quote in the path cannot break out of the literal
        let q = register_script(Path::new(r"C:\it's\igneum-app.exe"));
        assert!(q.contains("$src = 'C:\\it''s'"), "{q}");
        assert!(start_command().starts_with("Start-ScheduledTask -TaskName 'Igneum Power Helper'"));
        assert!(remove_command().starts_with("Unregister-ScheduledTask -TaskName 'Igneum Power Helper' -Confirm:$false"));
        assert!(query_command().contains("Get-ScheduledTask -TaskName 'Igneum Power Helper'"));
    }

    #[test]
    fn a_kit_run_from_a_jobs_folder_registers_the_installed_exe() {
        let kit = Path::new(r"C:\Work\Admin\AppData\Local\igneum\app\jobs\ember-kit-5\igneum-app-ember.exe");
        let own = Path::new(r"C:\Work\Admin\AppData\Local\Programs\Ember\igneum-app.exe");
        // no installed candidate exists on this machine: the running exe stands
        assert_eq!(task_exe(kit, &[PathBuf::from(r"C:\nonexistent\igneum-app.exe")]), kit.to_path_buf());
        assert_eq!(task_exe(own, &[]), own.to_path_buf());
        // an installed candidate that exists (the test binary stands in for it) wins for a kit, not for the installed exe
        let here = std::env::current_exe().unwrap();
        assert!(here.is_file());
        assert_eq!(task_exe(kit, &[here.clone()]), here);
        // a measurement run's scratch copy (run 6, 6 October 2026) is not under jobs\ and still yields
        let scratch = Path::new(r"C:\Work\Admin\AppData\Local\igneum-tune-20261006-170105\bin\igneum-app.exe");
        assert_eq!(task_exe(scratch, &[here.clone()]), here);
        // the installed exe itself stands (matched by path, any case or separator)
        assert_eq!(task_exe(own, &[PathBuf::from(r"c:/work/admin/appdata/local/programs/ember/IGNEUM-APP.EXE"), here]), own.to_path_buf());
    }

    #[test]
    fn a_stale_command_file_does_not_run_at_start() {
        // the helper's start reads the highest sequence already in the file and runs nothing below or at it
        let text = "3 pl 460\n4 lgc 2472\n";
        let last = text.lines().filter_map(parse_line).map(|(s, _)| s).max().unwrap_or(0);
        assert_eq!(last, 4);
        let newer: Vec<_> = "3 pl 460\n4 lgc 2472\n5 rgc\n".lines().filter_map(parse_line).filter(|(s, _)| *s > last).collect();
        assert_eq!(newer, vec![(5, HelperCmd::ClockReset)]);
    }
}
