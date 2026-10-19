//! Where the Linux `igneum-prove-host` lives as seen from inside WSL2 (Ubuntu-24.04) on a PC. One list, used by
//! three callers: the prover's probe (src/prover.rs), the verifier lookup the node spawn uses (src/verifier.rs)
//! and the Windows wrapper `igneum-prove-verify.exe` (src/bin/prove-verify.rs, which includes this file by path
//! because the package has no library target).
//!
//! Lookup order, first executable wins:
//!   1. the payload's `wsl2\bin\igneum-prove-host` next to the engine (`/mnt/<drive>/.../wsl2/bin/...` from Ubuntu)
//!   2. `~/igneum-prove/proving/igneum-prove/target/release/igneum-prove-host`, what setup-wsl.sh builds
//!      (it copies the package to `$HOME/igneum-prove` and builds in `proving/igneum-prove`)
//!   3. `~/igneum-prove/target/release/igneum-prove-host`, the layout before 5 October 2026
//!   4. `/opt/igneum/igneum-prove-host`, a hand install (the devnet jobs put the CUDA host there)

use std::path::{Path, PathBuf};
use std::process::Command;
use std::sync::atomic::{AtomicU64, Ordering};

/// The WSL distribution the host runs in.
pub const DISTRO: &str = "Ubuntu-24.04";

/// A Windows path as WSL sees it: `C:\Work\x\f` -> `/mnt/c/Work/x/f`. A path without a drive letter is
/// returned with forward slashes only.
pub fn wsl_path(p: &Path) -> String {
    let s = p.display().to_string().replace('\\', "/");
    let s = s.strip_prefix("//?/").map(|x| x.to_string()).unwrap_or(s);
    if s.len() > 2 && s.as_bytes()[1] == b':' {
        format!("/mnt/{}{}", s[..1].to_ascii_lowercase(), &s[2..])
    } else {
        s
    }
}

/// The candidates in lookup order. `bin_dir` is the engine's folder on Windows (the payload root); `~` is left
/// for the shell inside WSL to expand, so the list reads the same in a message.
pub fn candidates(bin_dir: &Path) -> Vec<String> {
    vec![
        wsl_path(&bin_dir.join("wsl2").join("bin").join("igneum-prove-host")),
        "~/igneum-prove/proving/igneum-prove/target/release/igneum-prove-host".to_string(),
        "~/igneum-prove/target/release/igneum-prove-host".to_string(),
        "/opt/igneum/igneum-prove-host".to_string(),
    ]
}

/// The candidates as one line for a message.
/// Whether the distribution answers at all (`wsl.exe -d Ubuntu-24.04 -- echo <marker>` within 30 s): the install-time
/// prover default (src/provedefault.rs) needs WSL2 on Windows before it switches proving on. Elsewhere: false.
#[allow(dead_code)] // also compiled into src/bin/prove-verify.rs, which does not call it
pub fn distro_answers() -> bool {
    if !cfg!(windows) {
        return false;
    }
    // self-contained (this file is also compiled into src/bin/prove-verify.rs, which has no detect or platform module)
    let mut cmd = std::process::Command::new("wsl");
    cmd.args(["-d", DISTRO, "--", "echo", "igneum-wsl-answers"]).stdin(std::process::Stdio::null()).stdout(std::process::Stdio::piped()).stderr(std::process::Stdio::null());
    #[cfg(windows)]
    {
        use std::os::windows::process::CommandExt;
        cmd.creation_flags(0x0800_0000); // CREATE_NO_WINDOW
    }
    let Ok(mut child) = cmd.spawn() else { return false };
    let Some(out) = child.stdout.take() else { return false };
    let reader = std::thread::spawn(move || {
        let mut s = String::new();
        let _ = std::io::Read::read_to_string(&mut std::io::BufReader::new(out), &mut s);
        s
    });
    let deadline = std::time::Instant::now() + std::time::Duration::from_secs(30);
    loop {
        match child.try_wait() {
            Ok(Some(_)) => break,
            Ok(None) if std::time::Instant::now() < deadline => std::thread::sleep(std::time::Duration::from_millis(100)),
            _ => {
                let _ = child.kill();
                let _ = child.wait();
                break;
            }
        }
    }
    reader.join().map(|o| o.replace('\0', "").contains("igneum-wsl-answers")).unwrap_or(false)
}

pub fn candidates_text(bin_dir: &Path) -> String {
    candidates(bin_dir).join(", ")
}

/// A `bash -lc` script that prints the first executable candidate (its path, one line) and nothing when there is
/// none. `~` expands in the shell; the shipped path is quoted.
pub fn lookup_script(bin_dir: &Path) -> String {
    let list: Vec<String> = candidates(bin_dir).into_iter().map(|c| if c.starts_with('~') { c } else { format!("'{}'", c.replace('\'', "'\\''")) }).collect();
    format!("for f in {}; do [ -x \"$f\" ] && {{ echo \"$f\"; break; }}; done", list.join(" "))
}

// ---- scripts run from a file, never inline on the command line ------------------------------------------------------
//
// 5 October 2026, the second Windows machine on an earlier release: `wsl -d Ubuntu-24.04 -- bash -lc "<script>"` with `[ -x "$f" ]` and a path with a
// space ("Ember") printed nothing, so the app said "proving needs the WSL2 setup" while /opt/igneum held the
// host; the same script written to a file and run as `bash /mnt/c/.../probe.sh` found it. Rust's Command wraps the
// argument in double quotes and escapes the inner ones with backslashes; wsl.exe hands the line to the shell inside
// the distribution, which reads it differently. The rule from here: every script goes to a file (UTF-8, LF, no BOM),
// the command line after `--` is `bash [-l] '<file>' '<arg>'...` with every word single-quoted and never a double
// quote or a newline, and on Windows that tail is placed on the command line as written (CommandExt::raw_arg), so
// neither the C runtime's quoting nor the shell's re-reading can change it. Arguments reach the script as $1, $2...

/// Where the script files go: `%LOCALAPPDATA%\igneum\wsl` on Windows (`IGNEUM_APP_DATA` first, as the engine's data
/// root); a temp folder elsewhere (tests).
pub fn script_dir() -> PathBuf {
    if let Some(p) = std::env::var_os("IGNEUM_APP_DATA") {
        return PathBuf::from(p).join("wsl");
    }
    #[cfg(windows)]
    {
        std::env::var_os("LOCALAPPDATA")
            .map(PathBuf::from)
            .unwrap_or_else(|| std::env::temp_dir())
            .join("igneum")
            .join("wsl")
    }
    #[cfg(not(windows))]
    {
        std::env::temp_dir().join("igneum-wsl")
    }
}

/// Single-quoted for a POSIX shell: `a b` becomes `'a b'`, a quote inside becomes `'\''`.
pub fn sq(s: &str) -> String {
    format!("'{}'", s.replace('\'', "'\\''"))
}

/// A script file written for `bash <file>`; removed when dropped unless `keep()` was called.
pub struct ScriptFile {
    pub path: PathBuf,
    keep: bool,
}

impl ScriptFile {
    /// Leaves the file in place (a run that outlives the engine, such as the setup window).
    pub fn keep(mut self) -> PathBuf {
        self.keep = true;
        self.path.clone()
    }
    /// The file as the distribution sees it.
    pub fn wsl_path(&self) -> String {
        wsl_path(&self.path)
    }
}

impl Drop for ScriptFile {
    fn drop(&mut self) {
        if !self.keep {
            let _ = std::fs::remove_file(&self.path);
        }
    }
}

static SCRIPT_SEQ: AtomicU64 = AtomicU64::new(0);

/// Writes `body` as `<script_dir>/<stem>-<pid>-<n>.sh`: a `#!/bin/bash` first line, UTF-8, LF line endings, no BOM,
/// a final newline. The name is unique per process and call, so two runs never share a file.
pub fn write_script(stem: &str, body: &str) -> std::io::Result<ScriptFile> {
    let dir = script_dir();
    std::fs::create_dir_all(&dir)?;
    let n = SCRIPT_SEQ.fetch_add(1, Ordering::Relaxed);
    let path = dir.join(format!("{stem}-{}-{n}.sh", std::process::id()));
    std::fs::write(&path, script_text(body).as_bytes())?;
    Ok(ScriptFile { path, keep: false })
}

/// The bytes a script file holds: the shebang line, the body with LF endings, one final newline.
pub fn script_text(body: &str) -> String {
    let body = body.replace("\r\n", "\n").replace('\r', "\n");
    let body = body.trim_start_matches('\u{feff}');
    format!("#!/bin/bash\n{}\n", body.trim_end_matches('\n'))
}

/// The words after `--`: `bash [-l] '<wsl path of file>' '<arg>'...`, every word single-quoted. No double quote and no
/// newline can appear, whatever the paths and arguments hold (a newline inside an argument is replaced by a space).
pub fn bash_line(file: &Path, login: bool, args: &[&str]) -> String {
    let mut words = vec!["bash".to_string()];
    if login {
        words.push("-l".to_string());
    }
    words.push(sq(&wsl_path(file)));
    for a in args {
        words.push(sq(&a.replace(['\r', '\n'], " ")));
    }
    words.join(" ")
}

/// `wsl.exe -d <distro> [-u <user>] -- bash [-l] '<file>' '<arg>'...`. On Windows the tail after `--` goes on the
/// command line exactly as `bash_line` wrote it; elsewhere the words are ordinary arguments (nothing runs wsl there).
/// The caller adds stdio, the hidden-window flag and the timeout.
pub fn command(wsl_exe: &Path, distro: &str, user: Option<&str>, file: &Path, login: bool, args: &[&str]) -> Command {
    // console: a builder; every caller runs it through run_capture, run_streamed or platform::quiet (tools/ci/windows-spawn-check.mjs)
    let mut c = Command::new(wsl_exe);
    c.args(["-d", distro]);
    if let Some(u) = user.filter(|u| !u.is_empty()) {
        c.args(["-u", u]);
    }
    c.arg("--");
    let line = bash_line(file, login, args);
    #[cfg(windows)]
    {
        use std::os::windows::process::CommandExt;
        c.raw_arg(line);
    }
    #[cfg(not(windows))]
    {
        let _ = line;
        c.arg("bash");
        if login {
            c.arg("-l");
        }
        c.arg(wsl_path(file));
        c.args(args);
    }
    c
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn the_command_line_after_the_dashes_never_carries_a_double_quote_or_a_newline() {
        let file = Path::new("C:\\Work\\Miner\\AppData\\Local\\igneum\\wsl\\probe-12-3.sh");
        let line = bash_line(file, true, &["--proof", "/mnt/c/Work/Miner/AppData/Local/igneum/app/proving/p.bin", "--statement", "0xab", "it's", "two\nlines"]);
        assert_eq!(
            line,
            "bash -l '/mnt/c/Work/Miner/AppData/Local/igneum/wsl/probe-12-3.sh' '--proof' '/mnt/c/Work/Miner/AppData/Local/igneum/app/proving/p.bin' '--statement' '0xab' 'it'\\''s' 'two lines'"
        );
        assert!(!line.contains('"') && !line.contains('\n'), "{line}");
        // the lookup script's own double quotes live in the file, never on the line
        let body = format!("{}\ncommand -v nvidia-smi >/dev/null && echo cuda", lookup_script(Path::new("C:\\Program Files\\Ember")));
        assert!(body.contains('"'));
        assert!(!bash_line(file, false, &[]).contains('"'));
    }

    #[test]
    fn script_files_are_lf_utf8_without_bom_and_removed_after_use() {
        let text = script_text("\u{feff}echo \"$1\"\r\nfor f in '/mnt/c/Program Files/x'; do [ -x \"$f\" ] && echo \"$f\"; done\r\n");
        assert_eq!(text, "#!/bin/bash\necho \"$1\"\nfor f in '/mnt/c/Program Files/x'; do [ -x \"$f\" ] && echo \"$f\"; done\n");
        assert!(!text.contains('\r') && !text.starts_with('\u{feff}'));
        let f = write_script("unit", "echo \"$1\"").unwrap();
        let bytes = std::fs::read(&f.path).unwrap();
        assert!(bytes.starts_with(b"#!/bin/bash\necho \"$1\"\n"), "{:?}", String::from_utf8_lossy(&bytes));
        assert!(!bytes.contains(&b'\r') && !bytes.starts_with(&[0xef, 0xbb, 0xbf]));
        let path = f.path.clone();
        drop(f);
        assert!(!path.exists());
        let kept = write_script("unit-keep", "true").unwrap().keep();
        assert!(kept.exists());
        let _ = std::fs::remove_file(kept);
    }

    #[test]
    fn shell_quoting() {
        assert_eq!(sq("a b"), "'a b'");
        assert_eq!(sq("it's"), "'it'\\''s'");
        assert_eq!(sq(""), "''");
    }

    #[test]
    fn candidate_order_is_shipped_then_setup_build_then_old_layout_then_opt() {
        let c = candidates(Path::new("C:\\Program Files\\Ember"));
        assert_eq!(
            c,
            vec![
                "/mnt/c/Program Files/Ember/wsl2/bin/igneum-prove-host",
                "~/igneum-prove/proving/igneum-prove/target/release/igneum-prove-host",
                "~/igneum-prove/target/release/igneum-prove-host",
                "/opt/igneum/igneum-prove-host",
            ]
        );
    }

    #[test]
    fn lookup_script_quotes_the_shipped_path_and_leaves_tilde_to_the_shell() {
        let s = lookup_script(Path::new("C:\\Program Files\\Ember"));
        assert!(s.starts_with("for f in '/mnt/c/Program Files/Ember/wsl2/bin/igneum-prove-host' ~/igneum-prove/proving/"), "{s}");
        assert!(s.contains("'/opt/igneum/igneum-prove-host'"));
        assert!(s.ends_with("[ -x \"$f\" ] && { echo \"$f\"; break; }; done"));
    }

    #[test]
    fn wsl_paths() {
        assert_eq!(wsl_path(Path::new("C:\\Work\\miner\\AppData\\Local\\igneum\\app\\proving\\seq.json")), "/mnt/c/Work/miner/AppData/Local/igneum/app/proving/seq.json");
        assert_eq!(wsl_path(Path::new("\\\\?\\D:\\x")), "/mnt/d/x");
        assert_eq!(wsl_path(Path::new("/tmp/x")), "/tmp/x");
    }
}
