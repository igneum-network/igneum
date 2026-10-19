//! Ember engine. Supervises igneumd, igneum-miner and the GPU workers and serves the dashboard on
//! 127.0.0.1:<random port>/t/<token>/. The window host (macOS: the Swift app, Windows: the WebView2 host) starts it with
//! --wrapper, reads `URL ...` and `STATE {...}` lines from its stdout and writes `quit` on its stdin. Without a host
//! (--open) the dashboard opens in the default browser.
//!
//!   igneum-app [--wrapper | --open | --no-open | --launch | --sweep] [--print-url]
//! --launch is what the Start Menu entry runs on Windows: it hands over to "Ember.exe" (the window host) when
//! that is installed next to the engine, else runs the engine with the dashboard in the default browser.
//! --sweep runs the efficiency sweep (src/sweep.rs) on every supported card as soon as it mines, prints the SWEEP
//! table on stdout, leaves the chosen caps in force and quits; no browser opens (the PC measurement job).
//!
//! Environment (tests): IGNEUM_APP_NETWORK devnet|simnet, IGNEUM_APP_RPC_PORT, IGNEUM_APP_P2P_PORT, IGNEUM_APP_PEERS
//! (comma list, empty for none), IGNEUM_APP_UNSYNCED=1, IGNEUM_APP_DATA, IGNEUM_APP_LOGS, IGNEUM_APP_BIN, IGNEUM_APP_NODE_DIR.
#![cfg_attr(all(windows, not(debug_assertions)), windows_subsystem = "windows")]

mod config;
mod detect;
mod engine;
mod sup;
mod hotplug;
mod keys;
mod platform;
mod procs;
mod server;
mod state;
mod manifest;
mod ota;
mod fleet;
mod uiota;
mod update;
mod execrpc;
mod jobs;
mod jobrun;
mod jobbuild;
mod wslhost;
mod sweep;
mod ember;
mod tiertable;
mod heat;
mod powertask;
mod watchdog;
mod quitguard;
mod device;
mod edition;
mod live;
mod pools;
mod scene;
mod feedclient;
mod ladder;
mod chainfacts;
mod card;
mod drivertable;
mod drivers;
mod driverinstall;
mod bootcheck;
mod boot;
mod rights;

use std::io::{BufRead, Write};
use std::sync::mpsc::channel;
use std::sync::Arc;

fn main() {
    let args: Vec<String> = std::env::args().skip(1).collect();
    let wrapper = args.iter().any(|a| a == "--wrapper") && !args.iter().any(|a| a == "--boot");
    let sweep = args.iter().any(|a| a == "--sweep");
    if sweep {
        // the runtime reads it (config::Runtime::from_env); the engine starts at once and quits after the sweep
        std::env::set_var("IGNEUM_APP_SWEEP", "1");
    }
    let no_open = wrapper || sweep || args.iter().any(|a| a == "--no-open");
    // the worker's CUDA device order pinned to the PCI order, nvidia-smi's own, so a worker device id and an nvidia-smi
    // index name one card (the project's read of the three-card test machine's 0.2.4 capture, 19:2x UK, 12 October 2026); PASSTHROUGH_ENV carries it
    if std::env::var_os("CUDA_DEVICE_ORDER").is_none() {
        std::env::set_var("CUDA_DEVICE_ORDER", "PCI_BUS_ID");
    }
    // the reference verifier's dataset build takes IGNPOW_THREADS (pow/ref igneumhash.cpp) and otherwise every core:
    // bounded here to half the box, at most eight, so a warm-up never starves the engine (the E1 study, 12 October 2026)
    if std::env::var_os("IGNPOW_THREADS").is_none() {
        let n = std::thread::available_parallelism().map(|n| n.get()).unwrap_or(4);
        std::env::set_var("IGNPOW_THREADS", ((n / 2).clamp(1, 8)).to_string());
    }
    if args.iter().any(|a| a == "--version" || a == "-V") {
        println!("igneum-app {}", engine::VERSION);
        return;
    }
    if args.iter().any(|a| a == "--rights") {
        // the installer's one elevated step (src/rights.rs): compares the installed rights manifest with this build's list,
        // asks for administrator rights once when something is missing, else exits at once; exit 0 either way (an install
        // never fails on a declined prompt: the app runs, the missing right is a notice)
        let exe = std::env::current_exe().unwrap_or_default();
        let install_dir = exe.parent().map(|d| d.to_path_buf()).unwrap_or_default();
        let runtime = config::Runtime::from_env();
        match rights::install(&exe, &install_dir, &crate::platform::fixed_data_root(), &runtime.app_dir, engine::VERSION) {
            Ok(true) => println!("rights: set up (one administrator approval)"),
            Ok(false) => println!("rights: nothing new to set up"),
            Err(e) if e.starts_with("rights: deferred") => println!("{e}"),
            Err(e) => println!("rights: not set up ({e}); the app runs, and the missing rights are notices in Settings"),
        }
        return;
    }
    if args.iter().any(|a| a == "--fleet-repair") {
        // the installer's fleet step (src/fleet.rs; the project's ruling of 17:2x UK, 12 October 2026): the signed fleet
        // manifest from the release store, this machine named in it or nothing fetched; exit 0 either way
        let runtime = config::Runtime::from_env();
        println!("fleet: {}", fleet::repair(&runtime.machine_id, &runtime.app_dir.join("fleet")));
        return;
    }
    if args.iter().any(|a| a == "--power-helper") {
        // the scheduled task's action (src/powertask.rs): elevated, runs only digit-argument nvidia-smi commands
        // from <app data>/app/sweep/cmd.txt, exits on quit, remove or 20 idle minutes
        let dir = powertask::helper_dir();
        std::process::exit(powertask::run_helper(&dir));
    }
    // an earlier release: `--data-root <path>` pins the data root (the boot task spells it out: an S4U session may not load the
    // profile); `--boot` is the headless engine (no window host, no browser); `--launch` with no interactive session is
    // `--boot` (src/boot.rs launch_mode: the second Windows machine waited 67 minutes for a logon on 7 October 2026)
    if let Some(i) = args.iter().position(|a| a == "--data-root") {
        if let Some(p) = args.get(i + 1) {
            std::env::set_var("IGNEUM_APP_DATA", p);
        }
    }
    let boot = args.iter().any(|a| a == "--boot");
    let mut no_open = no_open || boot;
    if args.iter().any(|a| a == "--launch") {
        let dir = std::env::current_exe().ok().and_then(|p| p.parent().map(|d| d.to_path_buf()));
        let host = dir.as_ref().map(|d| d.join("Ember.exe"));
        let host_exists = host.as_ref().map(|h| h.exists()).unwrap_or(false);
        match boot::launch_mode(std::env::var("SESSIONNAME").ok().as_deref(), host_exists) {
            boot::LaunchMode::Host => {
                if let (Some(dir), Some(host)) = (dir.as_ref(), host.as_ref()) {
                    let mut c = std::process::Command::new(host);
                    c.current_dir(dir);
                    crate::platform::quiet(&mut c); // no console of our own for the window host (it is a GUI program; the flag only governs a console)
                    if c.spawn().is_ok() {
                        return;
                    }
                }
            }
            boot::LaunchMode::Headless => no_open = true,
            boot::LaunchMode::Browser => {}
        }
        // no window host (or no logon): the engine runs on its own; the dashboard opens in the default browser only in a session
    }
    platform::clear_quarantine();
    let runtime = config::Runtime::from_env();
    // an earlier release: a window host's engine that finds the boot engine already running under this data root becomes the
    // bridge to it (src/boot.rs) instead of a second engine on the same ports and folder
    if wrapper && !sweep {
        if let Some(url) = boot::running_engine(&runtime.app_dir) {
            std::process::exit(boot::run_bridge(&url));
        }
    }
    let _ = std::fs::create_dir_all(&runtime.app_dir);
    let _ = std::fs::create_dir_all(&runtime.log_dir);
    platform::lock_permissions(&runtime.app_dir, true);

    let bins = match detect::find_bins() {
        Ok(b) => b,
        Err(e) => {
            eprintln!("{e}");
            if wrapper {
                println!("FATAL {e}");
            }
            std::process::exit(1);
        }
    };
    let mut candidates = vec![bins.dir.join("igneum-app.json")];
    if let Ok(exe) = std::env::current_exe() {
        if let Some(d) = exe.parent() {
            candidates.push(d.join("igneum-app.json"));
            if let Some(c) = d.parent() {
                candidates.push(c.join("Resources").join("igneum-app.json"));
            }
        }
    }
    let packaged = config::Packaged::load(&candidates).with_env_overrides();
    // the chain is pinned (config::CHAIN_ID); the runtime read above knew only the
    // environment, so it is read again with the package's suffix and peers (the environment still wins inside)
    // the network step (an earlier release): the saved choice is read first (the settings file lives in <data root>/app whatever the
    // network); the old network choice is read no more
    let chosen_network = config::Settings::load(&runtime.app_dir.join("settings.json")).network;
    let runtime = if packaged.node_devnet_suffix.is_some() || packaged.node_peers.is_some() || !chosen_network.is_empty() {
        config::Runtime::from_env_with_choice(packaged.node_devnet_suffix, packaged.node_peers.as_deref(), &chosen_network)
    } else {
        runtime
    };
    let _ = std::fs::create_dir_all(&runtime.app_dir);
    let settings = config::Settings::load(&runtime.app_dir.join("settings.json"));
    // a measurement engine runs with the installed app's choices and its own switches (config.rs for_measurement)
    let settings = if sweep { settings.for_measurement() } else { settings };

    // the page's token: the one an earlier launch wrote into app.url (the founder's rule of 11:0x UK, 12 October 2026:
    // an install or a restart never changes the page's address, so an open tab reconnects by itself), else 32 hex
    // characters from the OS
    let token = server::token_from_url_file(&runtime.app_dir.join("app.url")).unwrap_or_else(|| {
        let mut raw = [0u8; 16];
        getrandom::getrandom(&mut raw).expect("os randomness");
        keys::hex(&raw)
    });
    // the page's port: IGNEUM_APP_PORT, else the package's page_port, else 18080; page_lan (or IGNEUM_APP_LAN=1) binds
    // every address of the machine
    let page_wanted = std::env::var("IGNEUM_APP_PORT").ok().and_then(|v| v.trim().parse::<u16>().ok()).filter(|p| *p > 0)
        .or_else(|| (packaged.page_port > 0).then_some(packaged.page_port)).unwrap_or(server::PAGE_PORT);
    let page_lan = packaged.page_lan || std::env::var("IGNEUM_APP_LAN").map(|v| v.trim() == "1").unwrap_or(false);

    let stamp_file = runtime.log_dir.join(format!("app-{}.log", stamp_now()));
    let engine_log = std::fs::File::create(&stamp_file).ok();
    let (tx, rx) = channel();
    let shared = Arc::new(engine::Shared::new(token.clone(), runtime, packaged, settings, tx, engine_log, stamp_file.clone()));
    shared.set_page(page_wanted, page_lan);
    let port = match server::start(shared.clone(), page_wanted, page_lan) {
        Ok(p) => p,
        Err(e) => {
            eprintln!("cannot listen on {}: {e}", if page_lan { "0.0.0.0" } else { "127.0.0.1" });
            std::process::exit(1);
        }
    };
    shared.set_port(port);
    if port != page_wanted {
        shared.log(&format!("page port {page_wanted} in use; listening on {port} instead (the page's address changed: open the one in app.url)"));
    }
    let url = format!("http://127.0.0.1:{port}/t/{token}/");
    // the URL file lets a second launch or a support script find the window; user-only permissions
    let url_file = shared.runtime.app_dir.join("app.url");
    let _ = std::fs::write(&url_file, &url);
    platform::lock_permissions(&url_file, false);
    // the token is never logged (the logs are uploaded); app.url, 0600, is the one place it is written
    shared.log(&format!("dashboard listening on {}:{port} (the URL with its token is in app.url; log {})", if page_lan { "every address" } else { "127.0.0.1" }, stamp_file.display()));
    if wrapper || args.iter().any(|a| a == "--print-url") {
        println!("URL {url}");
        let _ = std::io::stdout().flush();
    }
    if !no_open {
        platform::open_url(&url);
    }
    // the host's commands on stdin; stdin closing in wrapper mode means the host is gone
    {
        let shared = shared.clone();
        std::thread::spawn(move || {
            let stdin = std::io::stdin();
            for line in stdin.lock().lines() {
                let Ok(l) = line else { break };
                let t = l.trim();
                match t {
                    "quit" => shared.send(engine::Cmd::Quit("the window host (quit on stdin: the tray menu or the installer)")),
                    // the tray menu is the user's hand
                    "pause" => shared.send(engine::Cmd::Pause("user".into())),
                    "resume" => shared.send(engine::Cmd::Resume),
                    // the window host saw WM_DEVICECHANGE (a card plugged in or out): enumerate now, not at the next minute
                    "detect" => shared.send(engine::Cmd::Detect),
                    "elevated ok" => shared.send(engine::Cmd::ElevatedDone(Ok(()))),
                    _ if t.starts_with("elevated fail") => shared.send(engine::Cmd::ElevatedDone(Err(t.trim_start_matches("elevated fail").trim_start_matches(':').trim().to_string()))),
                    _ => {}
                }
            }
            // an earlier release: only an engine a host attached quits with the host (src/boot.rs stdin_close_quits)
            if boot::stdin_close_quits(wrapper, boot) {
                shared.send(engine::Cmd::Quit("the window host went away (stdin closed)"));
            }
        });
    }
    engine::Engine::new(shared, bins, rx, wrapper).run();
}

fn stamp_now() -> String {
    let t = platform::unix_now();
    format!("{}-{:02}{:02}{:02}", t / 86400, t % 86400 / 3600, t % 3600 / 60, t % 60)
}
