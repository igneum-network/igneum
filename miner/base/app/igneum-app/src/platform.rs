//! What differs per operating system: directories, file permissions, keeping the machine awake, opening a URL,
//! starting at login, and stopping a child process gracefully.

use std::path::{Path, PathBuf};
use std::process::Command;

/// The absolute path of a system helper (R4.3.3: never a bare name on PATH). Windows: System32 (and NVIDIA's own
/// folder for nvidia-smi); macOS: /usr/bin, /usr/sbin, /bin. Unknown names fall back to the bare name.
pub fn tool(name: &str) -> PathBuf {
    #[cfg(windows)]
    {
        let root = std::env::var("SystemRoot").unwrap_or_else(|_| "C:\\Windows".into());
        let sys = format!("{root}\\System32");
        let p = match name {
            "powershell" => format!("{sys}\\WindowsPowerShell\\v1.0\\powershell.exe"),
            "cmd" | "curl" | "reg" | "icacls" | "taskkill" | "w32tm" | "net" | "tar" | "wsl" | "wevtutil" | "robocopy" => format!("{sys}\\{name}.exe"),
            "nvidia-smi" => {
                let pf = std::env::var("ProgramFiles").unwrap_or_else(|_| "C:\\Program Files".into());
                let a = format!("{pf}\\NVIDIA Corporation\\NVSMI\\nvidia-smi.exe");
                let b = format!("{sys}\\nvidia-smi.exe");
                if Path::new(&a).is_file() { a } else { b }
            }
            _ => name.to_string(),
        };
        PathBuf::from(p)
    }
    #[cfg(target_os = "macos")]
    {
        let p = match name {
            "curl" | "osascript" | "xattr" | "open" | "caffeinate" | "hdiutil" | "ditto" | "nohup" | "pgrep" | "pkill" | "shasum" | "xcrun" => format!("/usr/bin/{name}"),
            "sntp" | "scutil" | "system_profiler" | "sysctl" => format!("/usr/sbin/{name}"),
            "bash" | "sh" => format!("/bin/{name}"),
            _ => name.to_string(),
        };
        PathBuf::from(p)
    }
    #[cfg(not(any(windows, target_os = "macos")))]
    {
        for dir in ["/usr/bin", "/bin", "/usr/sbin", "/usr/local/bin"] {
            let p = Path::new(dir).join(name);
            if p.is_file() {
                return p;
            }
        }
        PathBuf::from(name)
    }
}

/// Strips the dashboard token from a line: "/t/<32 hex>/" becomes "/t/<token>/" (R4.3.8: the token is never logged
/// and the logs are uploaded).
pub fn redact(s: &str) -> String {
    let mut out = String::with_capacity(s.len());
    let mut rest = s;
    while let Some(i) = rest.find("/t/") {
        out.push_str(&rest[..i + 3]);
        let after = &rest[i + 3..];
        let hex_len = after.chars().take_while(|c| c.is_ascii_hexdigit()).count();
        if hex_len >= 16 {
            out.push_str("<token>");
            rest = &after[hex_len..];
        } else {
            rest = after;
        }
    }
    out.push_str(rest);
    out
}

/// True when a line still carries something that looks like the dashboard token (the upload guard).
pub fn carries_token(s: &str) -> bool {
    let mut rest = s;
    while let Some(i) = rest.find("/t/") {
        let after = &rest[i + 3..];
        if after.chars().take_while(|c| c.is_ascii_hexdigit()).count() >= 16 {
            return true;
        }
        rest = after;
    }
    false
}

pub fn unix_now() -> u64 {
    std::time::SystemTime::now().duration_since(std::time::UNIX_EPOCH).map(|d| d.as_secs()).unwrap_or(0)
}

pub fn unix_now_f() -> f64 {
    std::time::SystemTime::now().duration_since(std::time::UNIX_EPOCH).map(|d| d.as_secs_f64()).unwrap_or(0.0)
}

fn home() -> PathBuf {
    #[cfg(windows)]
    {
        if let Some(p) = std::env::var_os("USERPROFILE") {
            return PathBuf::from(p);
        }
    }
    std::env::var_os("HOME").map(PathBuf::from).unwrap_or_else(|| PathBuf::from("."))
}

/// The root the app writes under. macOS: ~/Library/Application Support/Igneum. Windows: %LOCALAPPDATA%\igneum
/// (the same folder today's launchers use, so an existing devnet-v4 database is reused).
pub fn data_root() -> PathBuf {
    if let Some(p) = std::env::var_os("IGNEUM_APP_DATA") {
        return PathBuf::from(p);
    }
    fixed_data_root()
}

/// The platform's own root, with IGNEUM_APP_DATA ignored: where the Power Helper task (started by Windows with no
/// environment of ours) reads its command file, so a measurement engine under a scratch root (IGNEUM_APP_DATA set)
/// writes its task commands here and not under its own root. 6 October 2026: found while planning the unelevated
/// climb through the task; the elevated runs never crossed it (they set the limits directly).
pub fn fixed_data_root() -> PathBuf {
    #[cfg(target_os = "macos")]
    {
        home().join("Library").join("Application Support").join("Igneum")
    }
    #[cfg(windows)]
    {
        std::env::var_os("LOCALAPPDATA").map(PathBuf::from).unwrap_or_else(|| home().join("AppData").join("Local")).join("igneum")
    }
    #[cfg(not(any(target_os = "macos", windows)))]
    {
        home().join(".igneum")
    }
}

pub fn log_root() -> PathBuf {
    if let Some(p) = std::env::var_os("IGNEUM_APP_LOGS") {
        return PathBuf::from(p);
    }
    #[cfg(target_os = "macos")]
    {
        home().join("Library").join("Logs").join("Igneum")
    }
    #[cfg(not(target_os = "macos"))]
    {
        data_root().join("logs")
    }
}

/// The machine's short name, cleaned to [A-Za-z0-9-], for the miner labels (mac-<host>, nvidia-<pc>).
pub fn host_label() -> String {
    let raw = {
        #[cfg(target_os = "macos")]
        {
            Command::new(tool("scutil")).args(["--get", "LocalHostName"]).output().ok().and_then(|o| String::from_utf8(o.stdout).ok())
        }
        #[cfg(windows)]
        {
            std::env::var("COMPUTERNAME").ok()
        }
        #[cfg(not(any(target_os = "macos", windows)))]
        {
            std::fs::read_to_string("/etc/hostname").ok()
        }
    };
    let raw = raw.unwrap_or_default();
    let cleaned: String = raw.trim().chars().map(|c| if c.is_ascii_alphanumeric() || c == '-' { c } else { '-' }).collect();
    let cleaned = cleaned.trim_matches('-').to_string();
    if cleaned.is_empty() {
        if cfg!(windows) { "pc".into() } else { "mac".into() }
    } else {
        cleaned
    }
}

/// Restricts a file (or directory) to the current user.
pub fn lock_permissions(path: &Path, dir: bool) {
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        let _ = std::fs::set_permissions(path, std::fs::Permissions::from_mode(if dir { 0o700 } else { 0o600 }));
    }
    #[cfg(windows)]
    {
        let user = std::env::var("USERNAME").unwrap_or_default();
        if !user.is_empty() {
            let _ = quiet(&mut Command::new(tool("icacls"))).arg(path).args(icacls_lock_args(dir, &user)).output();
        }
    }
}

/// The icacls arguments that lock a path to the user. A folder gets an INHERITABLE grant (`user:(OI)(CI)F`) and
/// NO `/T`: Windows propagates the inheritable entry to every child, existing or future, as `(I)(F)`. Measured on
/// the first Windows machine, 6 October 2026 (collect ember-acl-2): the old non-inheritable `user:F` cut the folder's inheritance and
/// left a file COPIED in before the engine started with no entry at all (the measurement engine's settings.json,
/// machine-id and wallet.json read as nothing, so it ran on defaults with no payout address; its own files, written
/// after the lock, inherited fine and hid it); the same grant WITH `/T` also left the file empty, because `/T`
/// re-applies `/inheritance:r` to the file after the propagation and an `(OI)(CI)` entry on a file is inherit-only.
pub fn icacls_lock_args(dir: bool, user: &str) -> Vec<String> {
    if dir {
        vec!["/inheritance:r".into(), "/grant:r".into(), format!("{user}:(OI)(CI)F")]
    } else {
        vec!["/inheritance:r".into(), "/grant:r".into(), format!("{user}:F")]
    }
}

/// Opens a URL in the default browser (the fallback when no window host runs).
pub fn open_url(url: &str) {
    #[cfg(target_os = "macos")]
    let _ = Command::new(tool("open")).arg(url).spawn();
    #[cfg(windows)]
    let _ = quiet(&mut Command::new(tool("cmd"))).args(["/c", "start", "", url]).spawn();
    #[cfg(not(any(target_os = "macos", windows)))]
    let _ = Command::new("xdg-open").arg(url).spawn();
}

/// Shows a file in the system's file browser (the saved block card): Finder with the file selected, Explorer with
/// the file selected, the folder on other systems.
pub fn reveal_file(path: &std::path::Path) {
    #[cfg(target_os = "macos")]
    let _ = Command::new(tool("open")).arg("-R").arg(path).spawn();
    #[cfg(windows)]
    let _ = quiet(&mut Command::new(tool("explorer"))).arg(format!("/select,{}", path.display())).spawn();
    #[cfg(not(any(target_os = "macos", windows)))]
    let _ = Command::new("xdg-open").arg(path.parent().unwrap_or(path)).spawn();
}

/// Keeps the machine awake while the engine runs. macOS: caffeinate tied to this process. Windows: the execution state,
/// which must be refreshed (call `keep_awake_tick` every minute).
pub struct KeepAwake {
    #[cfg(target_os = "macos")]
    child: Option<std::process::Child>,
}

impl KeepAwake {
    pub fn start() -> KeepAwake {
        #[cfg(target_os = "macos")]
        {
            let child = Command::new(tool("caffeinate")).args(["-dims", "-w", &std::process::id().to_string()]).spawn().ok();
            KeepAwake { child }
        }
        #[cfg(not(target_os = "macos"))]
        {
            keep_awake_tick();
            KeepAwake {}
        }
    }
    pub fn stop(&mut self) {
        #[cfg(target_os = "macos")]
        if let Some(c) = self.child.as_mut() {
            let _ = c.kill();
            let _ = c.wait();
        }
        #[cfg(windows)]
        unsafe {
            SetThreadExecutionState(ES_CONTINUOUS);
        }
    }
}

#[cfg(windows)]
const ES_CONTINUOUS: u32 = 0x8000_0000;
#[cfg(windows)]
const ES_SYSTEM_REQUIRED: u32 = 0x0000_0001;
#[cfg(windows)]
#[link(name = "kernel32")]
extern "system" {
    fn SetThreadExecutionState(flags: u32) -> u32;
}

pub fn keep_awake_tick() {
    #[cfg(windows)]
    unsafe {
        SetThreadExecutionState(ES_CONTINUOUS | ES_SYSTEM_REQUIRED);
    }
}

/// Asks a child to stop. Unix: SIGTERM (the node closes its database cleanly). Windows: TerminateProcess through
/// Every `igneum-miner` process on this machine with its command line: (pid, command line). Windows reads
/// Win32_Process through PowerShell; unix reads `ps`. An empty list when the tool fails (the caller kills nothing).
pub fn miner_processes() -> Vec<(u32, String)> {
    let out = if cfg!(windows) {
        let mut c = std::process::Command::new(tool("powershell"));
        c.args(["-NoProfile", "-Command", "Get-CimInstance Win32_Process -Filter \"Name='igneum-miner.exe'\" | ForEach-Object { \"$($_.ProcessId)|$($_.CommandLine)\" }"]);
        crate::detect::run_timeout(&mut c, None, std::time::Duration::from_secs(20))
    } else {
        let mut c = std::process::Command::new("ps");
        c.args(["-eo", "pid=,args="]);
        crate::detect::run_timeout(&mut c, None, std::time::Duration::from_secs(10))
    };
    let Some(out) = out else { return vec![] };
    let mut v = Vec::new();
    for l in out.lines() {
        let l = l.trim();
        let (pid, cmd) = if cfg!(windows) {
            let Some((p, c)) = l.split_once('|') else { continue };
            (p.trim(), c.trim())
        } else {
            let Some((p, c)) = l.split_once(' ') else { continue };
            (p.trim(), c.trim())
        };
        let Ok(pid) = pid.parse::<u32>() else { continue };
        if !cfg!(windows) && !(cmd.contains("igneum-miner ") || cmd.ends_with("igneum-miner")) {
            continue;
        }
        if cmd.contains(" mine ") {
            v.push((pid, cmd.to_string()));
        }
    }
    v
}

/// Ends one process by pid (Windows: taskkill /T /F; unix: SIGKILL).
pub fn kill_pid(pid: u32) {
    if cfg!(windows) {
        let _ = quiet(&mut std::process::Command::new(tool("taskkill"))).args(["/PID", &pid.to_string(), "/T", "/F"]).output();
    } else {
        let _ = quiet(&mut std::process::Command::new("kill")).args(["-9", &pid.to_string()]).output();
    }
}

/// std (what today's launcher does with taskkill /F).
pub fn terminate(child: &mut std::process::Child) {
    #[cfg(unix)]
    unsafe {
        libc::kill(child.id() as i32, libc::SIGTERM);
    }
    #[cfg(not(unix))]
    {
        let _ = child.kill();
    }
}

/// The app bundle on macOS (Ember.app) when the engine runs from inside one.
pub fn bundle_path() -> Option<PathBuf> {
    let exe = std::env::current_exe().ok()?;
    let macos = exe.parent()?;
    let contents = macos.parent()?;
    if macos.file_name()? == "MacOS" && contents.file_name()? == "Contents" {
        contents.parent().map(|p| p.to_path_buf())
    } else {
        None
    }
}

/// What a login item should run: the bundle (macOS) or the window host / the engine (Windows).
fn login_command() -> Vec<String> {
    #[cfg(target_os = "macos")]
    {
        if let Some(b) = bundle_path() {
            return vec!["/usr/bin/open".into(), "-a".into(), b.to_string_lossy().into_owned()];
        }
    }
    let exe = std::env::current_exe().map(|p| p.to_string_lossy().into_owned()).unwrap_or_default();
    #[cfg(windows)]
    {
        if let Some(dir) = std::env::current_exe().ok().and_then(|p| p.parent().map(|d| d.to_path_buf())) {
            let host = dir.join("Ember.exe");
            if host.exists() {
                return vec![host.to_string_lossy().into_owned()];
            }
        }
    }
    vec![exe]
}

#[cfg(target_os = "macos")]
fn launch_agent_path() -> PathBuf {
    home().join("Library").join("LaunchAgents").join("network.igneum.ember.plist")
}

pub fn set_start_at_login(on: bool) -> Result<(), String> {
    #[cfg(target_os = "macos")]
    {
        let path = launch_agent_path();
        if on {
            let args: String = login_command()
                .iter()
                .map(|a| format!("        <string>{}</string>\n", a.replace('&', "&amp;").replace('<', "&lt;")))
                .collect();
            let plist = format!(
                "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">\n<plist version=\"1.0\">\n<dict>\n    <key>Label</key>\n    <string>network.igneum.ember</string>\n    <key>ProgramArguments</key>\n    <array>\n{args}    </array>\n    <key>RunAtLoad</key>\n    <true/>\n</dict>\n</plist>\n"
            );
            if let Some(d) = path.parent() {
                std::fs::create_dir_all(d).map_err(|e| e.to_string())?;
            }
            std::fs::write(&path, plist).map_err(|e| e.to_string())?;
        } else if path.exists() {
            std::fs::remove_file(&path).map_err(|e| e.to_string())?;
        }
        Ok(())
    }
    #[cfg(windows)]
    {
        let key = r"HKCU\Software\Microsoft\Windows\CurrentVersion\Run";
        let out = if on {
            let cmd = login_command().iter().map(|a| format!("\"{a}\"")).collect::<Vec<_>>().join(" ");
            quiet(&mut Command::new(tool("reg"))).args(["add", key, "/v", "Ember", "/t", "REG_SZ", "/d", &cmd, "/f"]).output()
        } else {
            quiet(&mut Command::new(tool("reg"))).args(["delete", key, "/v", "Ember", "/f"]).output()
        };
        match out {
            Ok(o) if o.status.success() || !on => Ok(()),
            Ok(o) => Err(String::from_utf8_lossy(&o.stderr).trim().to_string()),
            Err(e) => Err(e.to_string()),
        }
    }
    #[cfg(not(any(target_os = "macos", windows)))]
    {
        let _ = on;
        Err("start at login is not supported on this platform".into())
    }
}

pub fn start_at_login_is_on() -> bool {
    #[cfg(target_os = "macos")]
    {
        launch_agent_path().exists()
    }
    #[cfg(windows)]
    {
        quiet(&mut Command::new(tool("reg")))
            .args(["query", r"HKCU\Software\Microsoft\Windows\CurrentVersion\Run", "/v", "Ember"])
            .output()
            .map(|o| o.status.success())
            .unwrap_or(false)
    }
    #[cfg(not(any(target_os = "macos", windows)))]
    {
        false
    }
}

/// Removes the quarantine flag from the app's own files (macOS): after Gatekeeper lets the app through, each binary
/// inside would still be checked on its first exec. Best effort; only works on a writable volume.
pub fn clear_quarantine() {
    #[cfg(target_os = "macos")]
    if let Some(b) = bundle_path() {
        let _ = Command::new(tool("xattr")).args(["-dr", "com.apple.quarantine"]).arg(b.join("Contents")).output();
    }
}

/// The manual instruction for fixing the clock on this platform.
pub fn clock_hint() -> &'static str {
    #[cfg(target_os = "macos")]
    {
        "System Settings > General > Date & Time: turn on \"Set time and date automatically\", or run: sudo sntp -sS time.apple.com"
    }
    #[cfg(windows)]
    {
        "Settings > Time & language > Date & time: turn on \"Set time automatically\" and click \"Sync now\", or run as administrator: w32tm /resync"
    }
    #[cfg(not(any(target_os = "macos", windows)))]
    {
        "run: sudo chronyc makestep, or sudo timedatectl set-ntp true"
    }
}

/// Asks the operating system to set the clock from a time server (an administrator prompt appears). Returns what
/// happened, for the window. Blocking; call from a thread.
pub fn sync_clock() -> Result<String, String> {
    #[cfg(target_os = "macos")]
    {
        let out = Command::new(tool("osascript"))
            .args(["-e", "do shell script \"/usr/bin/sntp -sS time.apple.com 2>&1\" with administrator privileges"])
            .output()
            .map_err(|e| e.to_string())?;
        let text = format!("{}{}", String::from_utf8_lossy(&out.stdout), String::from_utf8_lossy(&out.stderr));
        if out.status.success() {
            Ok(if text.trim().is_empty() { "clock set from time.apple.com".into() } else { text.trim().to_string() })
        } else if text.contains("canceled") || text.contains("cancelled") {
            Err("the administrator prompt was cancelled".into())
        } else {
            Err(text.trim().to_string())
        }
    }
    #[cfg(windows)]
    {
        let cmd = tool("cmd").display().to_string();
        let mut c = elevated_command(&cmd, "/c net start w32time & w32tm /resync /force");
        let out = c.output().map_err(|e| e.to_string())?;
        if out.status.success() {
            Ok("asked Windows Time to resync (w32tm /resync)".into())
        } else {
            Err(String::from_utf8_lossy(&out.stderr).trim().to_string())
        }
    }
    #[cfg(not(any(target_os = "macos", windows)))]
    {
        for args in [vec!["chronyc", "makestep"], vec!["timedatectl", "set-ntp", "true"]] {
            if let Ok(out) = Command::new("pkexec").args(&args).output() {
                if out.status.success() {
                    return Ok(format!("ran {}", args.join(" ")));
                }
            }
        }
        Err("neither chronyc nor timedatectl could set the clock".into())
    }
}

/// Runs a command line with administrator rights (one prompt): the NVIDIA power cap needs it on Windows.
/// Blocking; call from a thread.
/// The cmd.exe argument for one elevated line. cmd's documented rule: when the text after /c starts with a quote
/// and holds more than two quotes (two cards, or a cap plus a script), it strips the FIRST and LAST quote and runs
/// the broken remainder (the first Windows machine, 6 October 2026, 15:45Z: the one approved Power control step ran
/// `"...\nvidia-smi.exe" -i 0 -pl 460 & "...\nvidia-smi.exe" -i 1 -pl 160 & "...\powershell.exe" ... -File "...ps1"`,
/// exit 1, no cap applied, no task registered; a single-card PC, two quotes, was fine, which is why the second Windows machine never
/// showed it). Wrapping the whole line in one outer pair makes cmd strip exactly those.
pub fn cmd_c_args(cmdline: &str) -> String {
    format!("/c \"{cmdline}\"")
}

pub fn run_elevated(cmdline: &str) -> Result<(), String> {
    #[cfg(windows)]
    {
        let cmd = tool("cmd").display().to_string();
        let mut c = elevated_command(&cmd, &cmd_c_args(cmdline));
        let out = c.output().map_err(|e| e.to_string())?;
        if out.status.success() {
            Ok(())
        } else {
            let err = String::from_utf8_lossy(&out.stderr).trim().to_string();
            Err(elevated_failure(out.status.code(), &err))
        }
    }
    #[cfg(target_os = "linux")]
    {
        let out = Command::new("pkexec").args(["sh", "-c", cmdline]).output().map_err(|e| e.to_string())?;
        if out.status.success() { Ok(()) } else { Err(String::from_utf8_lossy(&out.stderr).trim().to_string()) }
    }
    #[cfg(not(any(windows, target_os = "linux")))]
    {
        let _ = cmdline;
        Err("not supported on this platform".into())
    }
}

/// The reason an elevated step failed, from the launcher's exit code and stderr: exit 251 (the prompt refused,
/// cancelled or timed out, `elevated_ps_line`) and the "canceled" wording name the prompt; any other code is the
/// step's own exit (the engine then keeps Power control on: rights were given).
pub fn elevated_failure(code: Option<i32>, stderr: &str) -> String {
    if code == Some(ELEVATED_LAUNCH_FAILED) || stderr.contains("canceled") || stderr.contains("cancelled") {
        "the administrator prompt was refused, cancelled or timed out".into()
    } else if stderr.is_empty() {
        format!("the elevated step exited with code {}", code.map(|c| c.to_string()).unwrap_or_else(|| "?".into()))
    } else {
        stderr.to_string()
    }
}

/// Standard base64 (RFC 4648, padded) of `bytes`: what PowerShell's -EncodedCommand reads.
pub fn base64_encode(bytes: &[u8]) -> String {
    const T: &[u8; 64] = b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
    let mut out = String::with_capacity(bytes.len().div_ceil(3) * 4);
    for chunk in bytes.chunks(3) {
        let n = (chunk[0] as u32) << 16 | (*chunk.get(1).unwrap_or(&0) as u32) << 8 | *chunk.get(2).unwrap_or(&0) as u32;
        out.push(T[(n >> 18) as usize & 63] as char);
        out.push(T[(n >> 12) as usize & 63] as char);
        out.push(if chunk.len() > 1 { T[(n >> 6) as usize & 63] as char } else { '=' });
        out.push(if chunk.len() > 2 { T[n as usize & 63] as char } else { '=' });
    }
    out
}

/// The PowerShell arguments that run `script` INLINE (V6-06, 8 October 2026): `-EncodedCommand` over the script's
/// UTF-16LE bytes, so an elevated step never reads a .ps1 from a folder the user can write between the write and the
/// run (rights.ps1 under the data root, register-power-task.ps1 under the sweep folder were that). The command line
/// cap is 32 K characters; the callers' scripts are a few K.
pub fn encoded_command_args(script: &str) -> Vec<String> {
    let utf16: Vec<u8> = script.encode_utf16().flat_map(|u| u.to_le_bytes()).collect();
    vec!["-NoProfile".into(), "-ExecutionPolicy".into(), "Bypass".into(), "-EncodedCommand".into(), base64_encode(&utf16)]
}

/// Doubles the single quotes of `s` for a single-quoted PowerShell literal.
pub fn ps_quote(s: &str) -> String {
    s.replace('\'', "''")
}

/// The PowerShell line that starts `file args` as administrator (one UAC prompt), waits, and exits with the child's
/// code. Every elevated launch of the app goes through here (the NVIDIA power cap, the sweep helper, the clock sync,
/// an elevated remote job) so the console flags live in one place: `-WindowStyle Hidden` is SW_HIDE on the new
/// process the AppInfo service creates; the elevated child cannot inherit this process's headless console, so without
/// it the child gets a console of its own (5 October 2026, the first Windows machine watcher, tools/windows/console-watch*.ps1).
/// A refused, cancelled or unanswered prompt makes Start-Process throw and `$p` stay null: that is exit 251 with the
/// reason on stderr, never `exit $p.ExitCode` = 0 (the 5 October 2026 driver job on the first Windows machine was reported done after
/// Windows cancelled its prompt at 122 s).
pub fn elevated_ps_line(file: &str, args: &str) -> String {
    format!(
        "try {{ $p = Start-Process -FilePath '{}' -ArgumentList '{}' -Verb RunAs -Wait -WindowStyle Hidden -PassThru -ErrorAction Stop }} catch {{ Write-Error ('elevated launch failed (UAC refused, cancelled or timed out): ' + $_.Exception.Message); exit 251 }}; if ($null -eq $p) {{ Write-Error 'elevated launch failed: no process'; exit 251 }}; exit $p.ExitCode",
        ps_quote(file),
        ps_quote(args)
    )
}

/// The exit code `elevated_ps_line` uses when the elevated process never started (the prompt refused, cancelled or
/// timed out).
pub const ELEVATED_LAUNCH_FAILED: i32 = 251;

/// The hidden PowerShell that runs `elevated_ps_line(file, args)`: blocking when run, one UAC prompt on the PC.
pub fn elevated_command(file: &str, args: &str) -> Command {
    let mut c = Command::new(tool("powershell"));
    c.args(["-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", &elevated_ps_line(file, args)]);
    quiet(&mut c);
    c
}

/// Builds a command that runs without a console window on Windows.
pub fn quiet(cmd: &mut Command) -> &mut Command {
    #[cfg(windows)]
    {
        use std::os::windows::process::CommandExt;
        cmd.creation_flags(0x0800_0000); // CREATE_NO_WINDOW
    }
    cmd
}

#[cfg(test)]
mod cmd_tests {
    #[test]
    fn an_elevated_line_is_wrapped_so_cmd_keeps_every_inner_quote() {
        let line = r#""C:\WINDOWS\System32\nvidia-smi.exe" -i 0 -pl 460 & "C:\WINDOWS\System32\nvidia-smi.exe" -i 1 -pl 160 & "C:\WINDOWS\System32\WindowsPowerShell\v1.0\powershell.exe" -NoProfile -File "C:\x\register-power-task.ps1""#;
        let a = super::cmd_c_args(line);
        assert!(a.starts_with("/c \"\"C:\\WINDOWS"), "{a}");
        assert!(a.ends_with("register-power-task.ps1\"\""), "{a}");
        // the inner line is intact between the outer pair
        assert_eq!(&a[4..a.len() - 1], line);
        assert_eq!(super::cmd_c_args("echo hi"), "/c \"echo hi\"");
    }
}

#[cfg(test)]
mod lock_tests {
    #[test]
    fn a_locked_folder_grants_the_user_inheritably_and_covers_what_is_inside() {
        let d = super::icacls_lock_args(true, "Admin");
        assert_eq!(d, vec!["/inheritance:r", "/grant:r", "Admin:(OI)(CI)F"], "inheritable, and never /T (it empties the children)");
        let f = super::icacls_lock_args(false, "Admin");
        assert_eq!(f, vec!["/inheritance:r", "/grant:r", "Admin:F"]);
    }
}

#[cfg(test)]
mod tests {
    #[test]
    fn elevated_line_is_hidden_and_quoted() {
        let l = super::elevated_ps_line(r"C:\WINDOWS\system32\cmd.exe", "/c echo it's & exit 3");
        assert!(l.starts_with("try { $p = "), "{l}");
        assert!(l.contains("-FilePath 'C:\\WINDOWS\\system32\\cmd.exe' -ArgumentList '/c echo it''s & exit 3' -Verb RunAs -Wait -WindowStyle Hidden -PassThru -ErrorAction Stop } catch {"), "{l}");
        assert!(l.contains("-Verb RunAs"), "{l}");
        assert!(l.contains("-WindowStyle Hidden"), "{l}");
        // a thrown Start-Process (the prompt refused) never falls through to `exit $p.ExitCode`
        assert!(l.contains("exit 251 }; if ($null -eq $p) { Write-Error 'elevated launch failed: no process'; exit 251 }; exit $p.ExitCode"), "{l}");
        assert!(l.ends_with("exit $p.ExitCode"), "{l}");
        assert_eq!(super::ELEVATED_LAUNCH_FAILED, 251);
        assert_eq!(super::elevated_failure(Some(251), "elevated launch failed (UAC refused, cancelled or timed out): ..."), "the administrator prompt was refused, cancelled or timed out");
        assert_eq!(super::elevated_failure(Some(1), "The operation was canceled by the user."), "the administrator prompt was refused, cancelled or timed out");
        assert_eq!(super::elevated_failure(Some(2), ""), "the elevated step exited with code 2");
        assert_eq!(super::elevated_failure(Some(3), "nvidia-smi: bad"), "nvidia-smi: bad");
        assert_eq!(super::ps_quote("a'b''c"), "a''b''''c");
        assert_eq!(super::ps_quote("plain"), "plain");
    }

    #[test]
    fn elevated_command_is_a_hidden_powershell() {
        let c = super::elevated_command("powershell.exe", "-NoProfile -File \"C:\\x y\\elevated.ps1\"");
        let args: Vec<String> = c.get_args().map(|a| a.to_string_lossy().into_owned()).collect();
        assert_eq!(&args[..4], ["-NoProfile", "-ExecutionPolicy", "Bypass", "-Command"]);
        assert!(args[4].contains("-ArgumentList '-NoProfile -File \"C:\\x y\\elevated.ps1\"' -Verb RunAs -Wait -WindowStyle Hidden"), "{}", args[4]);
        assert!(c.get_program().to_string_lossy().contains("powershell"));
    }

    #[test]
    fn token_redaction() {
        let l = "dashboard at http://127.0.0.1:58776/t/a3a01c537130bceeaa1f6118ba48d63e/ (log x)";
        assert_eq!(super::redact(l), "dashboard at http://127.0.0.1:58776/t/<token>/ (log x)");
        assert!(super::carries_token(l));
        assert!(!super::carries_token("GET /t/short/ nothing"));
        assert_eq!(super::redact("no token here"), "no token here");
    }
}
