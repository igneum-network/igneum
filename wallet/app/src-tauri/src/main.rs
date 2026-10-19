//! The window host (docs/product-b/DESIGN.md, 1.1). It starts the engine (which starts the signer), reads the
//! engine's one `ENGINE {...}` line for the tokened URL, and opens the kept look on it. It holds no key and no
//! state of its own; the page talks to the engine directly on 127.0.0.1 under the token.
//!
//! In a bundle the engine and the signer sit next to the host as external binaries and the page under `ui/`;
//! in a development tree `IGNEUM_WALLET_ENGINE` and `IGNEUM_WALLET_UI` point at them.

#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

use std::io::{BufRead, BufReader};
use std::path::PathBuf;
use std::process::{Child, Command, Stdio};

use tauri::{Manager, WebviewUrl, WebviewWindowBuilder};

struct EngineProcess(Child);

impl Drop for EngineProcess {
    fn drop(&mut self) {
        let _ = self.0.kill();
        let _ = self.0.wait();
    }
}

fn engine_path(app: &tauri::AppHandle) -> PathBuf {
    if let Ok(p) = std::env::var("IGNEUM_WALLET_ENGINE") {
        return PathBuf::from(p);
    }
    let exe = std::env::current_exe().expect("own path");
    let dir = exe.parent().expect("own dir");
    let name = if cfg!(windows) { "igneum-wallet-engine.exe" } else { "igneum-wallet-engine" };
    let beside = dir.join(name);
    if beside.exists() {
        return beside;
    }
    app.path().resource_dir().map(|r| r.join(name)).unwrap_or(beside)
}

fn ui_path(app: &tauri::AppHandle) -> PathBuf {
    if let Ok(p) = std::env::var("IGNEUM_WALLET_UI") {
        return PathBuf::from(p);
    }
    app.path().resource_dir().map(|r| r.join("ui")).unwrap_or_else(|_| PathBuf::from("ui"))
}

fn data_dir(app: &tauri::AppHandle) -> PathBuf {
    if let Ok(p) = std::env::var("IGNEUM_WALLET_DATA") {
        return PathBuf::from(p);
    }
    app.path().app_data_dir().unwrap_or_else(|_| PathBuf::from(".igneum-wallet"))
}

fn start_engine(app: &tauri::AppHandle) -> Result<(EngineProcess, String), String> {
    let engine = engine_path(app);
    let ui = ui_path(app);
    let data = data_dir(app);
    let signer = engine.parent().map(|d| d.join(if cfg!(windows) { "igneum-signer.exe" } else { "igneum-signer" }));
    let mut cmd = Command::new(&engine);
    cmd.arg("--ui").arg(&ui).arg("--data-dir").arg(&data).arg("--exit-with-parent");
    if let Some(s) = signer {
        if s.exists() {
            cmd.arg("--signer").arg(s);
        }
    }
    // stdin stays open for the engine's lifetime: when this host dies the pipe closes and the engine exits
    cmd.stdin(Stdio::piped()).stdout(Stdio::piped()).stderr(Stdio::inherit());
    let mut child = cmd.spawn().map_err(|e| format!("starting the engine at {}: {e}", engine.display()))?;
    let stdout = child.stdout.take().ok_or("engine stdout")?;
    let mut reader = BufReader::new(stdout);
    let mut line = String::new();
    loop {
        line.clear();
        let n = reader.read_line(&mut line).map_err(|e| e.to_string())?;
        if n == 0 {
            return Err("the engine stopped before it printed its address".into());
        }
        if let Some(rest) = line.strip_prefix("ENGINE ") {
            let v: serde_json::Value = serde_json::from_str(rest.trim()).map_err(|e| e.to_string())?;
            let url = v.get("url").and_then(|u| u.as_str()).ok_or("no url")?.to_string();
            // keep draining stdout so the engine never blocks on a full pipe
            std::thread::spawn(move || {
                let mut sink = String::new();
                while reader.read_line(&mut sink).unwrap_or(0) > 0 {
                    sink.clear();
                }
            });
            return Ok((EngineProcess(child), url));
        }
    }
}

fn main() {
    tauri::Builder::default()
        .setup(|app| {
            eprintln!("igneum-wallet-app: setup, starting the engine");
            let handle = app.handle().clone();
            let (process, url) = start_engine(&handle).map_err(|e| std::io::Error::other(e))?;
            eprintln!("igneum-wallet-app: engine up at {}", url.split("/t/").next().unwrap_or(""));
            app.manage(process);
            let parsed: tauri::Url = url.parse().map_err(|e| std::io::Error::other(format!("engine url: {e}")))?;
            WebviewWindowBuilder::new(app, "main", WebviewUrl::External(parsed))
                .title("Igneum Wallet")
                .inner_size(1000.0, 680.0)
                .min_inner_size(900.0, 620.0)
                .build()?;
            Ok(())
        })
        .run(tauri::generate_context!())
        .expect("the window host");
}
