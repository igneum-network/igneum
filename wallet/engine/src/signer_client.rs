//! The engine's side of the signer pipe: one child process, one request at a time, JSON lines.

use std::io::{Read, Write};
use std::process::{Child, ChildStdin, ChildStdout, Command, Stdio};
use std::sync::Mutex;

use serde_json::{json, Value};

use igneum_wallet_core::{Error, Result};

/// The reply buffer's first size: a reply line that fits leaves no earlier copy behind (a growing buffer frees
/// each smaller one unzeroed); a reveal's reply is a few hundred bytes.
const REPLY_RESERVE: usize = 16 * 1024;

pub struct SignerClient {
    inner: Mutex<Inner>,
}

struct Inner {
    child: Child,
    stdin: ChildStdin,
    /// the signer's last stderr lines (wallet-security's finding on the signed cut, 12 October 2026: a signer that
    /// died on the host's memory-lock limit answered only "the signer stopped"; its own line said why): a thread
    /// copies the signer's stderr to this process's stderr and keeps the last three lines for the reply
    last_err: std::sync::Arc<Mutex<std::collections::VecDeque<String>>>,
    /// the raw pipe, read a byte at a time (wallet-security's MEM-3): a buffered reader would keep the last reply
    /// line, the words or the key among it, in its own buffer for the life of the process
    stdout: ChildStdout,
    next_id: u64,
}

/// The words of a stopped signer: its own last line when it left one (the memory-lock limit, an allocation that
/// failed), so the reply says why and not only that it stopped.
fn stopped_words(last_err: &std::sync::Arc<Mutex<std::collections::VecDeque<String>>>) -> String {
    std::thread::sleep(std::time::Duration::from_millis(150));
    let tail = last_err.lock().ok().map(|k| k.iter().cloned().collect::<Vec<_>>().join(" | ")).unwrap_or_default();
    if tail.trim().is_empty() {
        "the signer stopped".to_string()
    } else {
        format!("the signer stopped: {}", tail.trim())
    }
}

/// Every string in a value zeroed in place: a reply's words or key live on in no dropped Value.
pub fn scrub(v: &mut Value) {
    use zeroize::Zeroize;
    match v {
        Value::String(s) => s.zeroize(),
        Value::Array(a) => a.iter_mut().for_each(scrub),
        Value::Object(o) => o.values_mut().for_each(scrub),
        _ => {}
    }
}

impl SignerClient {
    pub fn spawn(program: &str, light_kdf: bool) -> Result<Self> {
        let mut cmd = Command::new(program);
        cmd.stdin(Stdio::piped()).stdout(Stdio::piped()).stderr(Stdio::piped());
        // the signer sees no environment but what it needs: no proxy, no RPC, nothing to phone home with
        cmd.env_clear();
        if let Ok(p) = std::env::var("PATH") {
            cmd.env("PATH", p);
        }
        if light_kdf {
            cmd.env("IGNEUM_SIGNER_LIGHT_KDF", "1");
        }
        let mut child = cmd.spawn().map_err(|e| Error::Io(std::io::Error::new(e.kind(), format!("starting the signer {program}: {e}"))))?;
        let stdin = child.stdin.take().ok_or_else(|| Error::Refused("signer stdin".into()))?;
        let stdout = child.stdout.take().ok_or_else(|| Error::Refused("signer stdout".into()))?;
        let last_err = std::sync::Arc::new(Mutex::new(std::collections::VecDeque::new()));
        if let Some(stderr) = child.stderr.take() {
            let keep = last_err.clone();
            std::thread::spawn(move || {
                use std::io::BufRead;
                for line in std::io::BufReader::new(stderr).lines().map_while(|l| l.ok()) {
                    eprintln!("{line}");
                    if let Ok(mut k) = keep.lock() {
                        k.push_back(line);
                        while k.len() > 3 {
                            k.pop_front();
                        }
                    }
                }
            });
        }
        Ok(SignerClient { inner: Mutex::new(Inner { child, stdin, stdout, next_id: 1, last_err }) })
    }

    pub fn request(&self, op: &str, mut params: Value) -> Result<Value> {
        let mut g = self.inner.lock().map_err(|_| Error::Refused("signer lock".into()))?;
        let id = g.next_id;
        g.next_id += 1;
        if !params.is_object() {
            params = json!({});
        }
        params["id"] = json!(id);
        params["op"] = json!(op);
        // the serialized request carries the password on the ops that take one (G2): written into a buffer of its
        // exact size (no copies left by growth) and zeroized when it drops
        let line = zeroize::Zeroizing::new(crate::http::json_exact(&params));
        scrub(&mut params);
        g.stdin.write_all(&line)?;
        g.stdin.write_all(b"\n")?;
        g.stdin.flush()?;
        // the reply line may carry the words or the key (create, reveal): read a byte at a time from the raw pipe
        // into a buffer reserved once, zeroized when it drops
        let mut resp = zeroize::Zeroizing::new(Vec::with_capacity(REPLY_RESERVE));
        let mut byte = [0u8; 1];
        loop {
            match g.stdout.read(&mut byte) {
                Ok(1) => {
                    if byte[0] == b'\n' {
                        break;
                    }
                    resp.push(byte[0]);
                }
                Ok(_) => return Err(Error::Refused(stopped_words(&g.last_err))),
                Err(e) if e.kind() == std::io::ErrorKind::Interrupted => continue,
                Err(e) => return Err(e.into()),
            }
        }
        let mut v: Value = serde_json::from_slice(&resp)?;
        let out = if v.get("ok").and_then(|o| o.as_bool()) == Some(true) {
            Ok(v.get("result").cloned().unwrap_or(Value::Null))
        } else {
            // the signer's text already names the refusal; Error::Refused prints "refused: " once in front of it
            // (wallet-security's finding 13: "refused: refused: the wallet is locked")
            Err(Error::Refused(v.get("error").and_then(|e| e.as_str()).unwrap_or("signer error").trim_start_matches("refused: ").to_string()))
        };
        // the parsed reply scrubbed before it drops: the caller's clone is the one copy left
        scrub(&mut v);
        out
    }

    pub fn pid(&self) -> Option<u32> {
        self.inner.lock().ok().map(|g| g.child.id())
    }
}

impl Drop for SignerClient {
    fn drop(&mut self) {
        if let Ok(mut g) = self.inner.lock() {
            let _ = g.child.kill();
            let _ = g.child.wait();
        }
    }
}
