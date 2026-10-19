//! Child processes with their output read line by line into one channel, and written to a log file each.

use std::io::{BufRead, BufReader, Write};
use std::path::{Path, PathBuf};
use std::process::{Child, Command, Stdio};
use std::sync::mpsc::Sender;
use std::sync::{Arc, Mutex};
use std::time::Instant;

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum Source {
    Node,
    Watch,
    Miner(usize), // card index
    Telemetry,    // nvidia-smi -l 5
    AmdTelemetry, // igneum-gpu-telemetry -l 5 (ADLX or sysfs), 5 October 2026
}

impl Source {
    pub fn tag(&self) -> String {
        match self {
            Source::Node => "node".into(),
            Source::Watch => "watch".into(),
            Source::Miner(i) => format!("miner{}", i + 1),
            Source::Telemetry => "gpu".into(),
            Source::AmdTelemetry => "gpu-amd".into(),
        }
    }
}

pub struct Line {
    pub src: Source,
    pub stderr: bool,
    pub text: String,
}

pub struct Proc {
    pub child: Child,
    pub started: Instant,
    pub log_path: PathBuf,
    pub cmdline: String,
    pub exit_code: Option<i32>,
}

/// Starts a program with stdout and stderr piped; every line goes to `tx` and to the log file. `envs` are added to
/// the child's environment (a miner passes IGNEUM_TUNING_FILE on to its GPU worker).
pub fn spawn(src: Source, exe: &Path, args: &[String], cwd: Option<&Path>, log_path: &Path, tx: &Sender<Line>, envs: &[(String, String)]) -> std::io::Result<Proc> {
    let mut cmd = Command::new(exe);
    cmd.args(args).stdin(Stdio::piped()).stdout(Stdio::piped()).stderr(Stdio::piped());
    for (k, v) in envs {
        cmd.env(k, v);
    }
    if let Some(d) = cwd {
        cmd.current_dir(d);
    }
    crate::platform::quiet(&mut cmd);
    let mut child = cmd.spawn()?;
    let file = Arc::new(Mutex::new(std::fs::File::create(log_path).ok()));
    let cmdline = format!("{} {}", exe.display(), args.join(" "));
    if let Some(f) = file.lock().unwrap().as_mut() {
        let _ = writeln!(f, "# {cmdline}");
    }
    for (stream, is_err) in [(child.stdout.take().map(|s| Box::new(s) as Box<dyn std::io::Read + Send>), false), (child.stderr.take().map(|s| Box::new(s) as Box<dyn std::io::Read + Send>), true)] {
        let Some(stream) = stream else { continue };
        let tx = tx.clone();
        let file = file.clone();
        std::thread::spawn(move || {
            let reader = BufReader::new(stream);
            for line in reader.split(b'\n') {
                let Ok(bytes) = line else { break };
                let text = String::from_utf8_lossy(&bytes).trim_end_matches('\r').to_string();
                if let Some(f) = file.lock().unwrap().as_mut() {
                    let _ = writeln!(f, "{}{}", if is_err { "! " } else { "" }, text);
                }
                if tx.send(Line { src, stderr: is_err, text }).is_err() {
                    break;
                }
            }
        });
    }
    Ok(Proc { child, started: Instant::now(), log_path: log_path.to_path_buf(), cmdline, exit_code: None })
}

impl Proc {
    pub fn pid(&self) -> u32 {
        self.child.id()
    }
    /// Polls the process; records the exit code once it has ended.
    pub fn alive(&mut self) -> bool {
        if self.exit_code.is_some() {
            return false;
        }
        match self.child.try_wait() {
            Ok(Some(st)) => {
                self.exit_code = Some(st.code().unwrap_or(-1));
                false
            }
            Ok(None) => true,
            Err(_) => false,
        }
    }
    pub fn write_stdin(&mut self, text: &str) {
        if let Some(s) = self.child.stdin.as_mut() {
            let _ = s.write_all(text.as_bytes());
            let _ = s.flush();
        }
    }
    /// Graceful stop: a terminate signal, then up to `grace_secs`, then a kill.
    pub fn stop(&mut self, grace_secs: u64) -> Option<i32> {
        if !self.alive() {
            return self.exit_code;
        }
        crate::platform::terminate(&mut self.child);
        let deadline = Instant::now() + std::time::Duration::from_secs(grace_secs);
        while Instant::now() < deadline {
            if !self.alive() {
                return self.exit_code;
            }
            std::thread::sleep(std::time::Duration::from_millis(100));
        }
        let _ = self.child.kill();
        let _ = self.child.wait();
        self.exit_code = Some(-9);
        self.exit_code
    }
}
