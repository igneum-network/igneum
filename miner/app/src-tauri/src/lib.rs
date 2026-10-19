//! The Tauri host: the command layer between the WebView and the supervisor (design section 2.4). Every
//! argument is typed and range-checked here before anything happens; no command takes a path, a URL to fetch,
//! a shell string or a process name. The WebView has no network of its own (CSP connect-src 'none').
//!
//! The updater plugin is added when the reliability engineer hands over the minisign public key (design
//! section 1.4); until then the Settings card says "updates: not configured in this build".

use igneum_supervisor::economics::{Tariff, Window};
use igneum_supervisor::modes::{Limits, Mode};
use igneum_supervisor::schedule::{GamingPause, TimeOfUse};
use igneum_supervisor::settings::{UpdatePolicy, WorkSourceSetting};
use igneum_supervisor::state::Snapshot;
use igneum_supervisor::{Config, Supervisor};
use serde::Deserialize;
use std::thread;
use std::time::Duration;
use tauri::{AppHandle, Emitter, Manager, State};

const APP_NAME: &str = "Ember";
const VERSION: &str = env!("CARGO_PKG_VERSION");

struct App {
    sup: Supervisor,
}

type R<T> = Result<T, String>;

fn err<E: std::fmt::Display>(e: E) -> String {
    e.to_string()
}

#[tauri::command]
fn get_state(app: State<'_, App>) -> Snapshot {
    app.sup.snapshot()
}

#[tauri::command]
fn detect_devices(app: State<'_, App>) -> R<Snapshot> {
    app.sup.inventory().map_err(err)?;
    Ok(app.sup.snapshot())
}

#[tauri::command]
fn set_payout_address(app: State<'_, App>, address: String) -> R<String> {
    if address.len() > 64 {
        return Err("an address is 0x followed by 40 hex characters".into());
    }
    let checksummed = igneum_supervisor::settings::validate_address(&address)?;
    let c = checksummed.clone();
    app.sup.update_settings(move |s| {
        s.payout_address = Some(c);
        Ok(())
    })
    .map_err(err)?;
    Ok(checksummed)
}

#[derive(Deserialize)]
#[serde(tag = "kind", rename_all = "snake_case")]
enum WorkSourceArg {
    Pool { pool_id: String },
    PoolCustom { host: String, port: u16, tls: bool },
    Solo { rpc_url: String },
    Stub,
}

#[tauri::command]
fn set_work_source(app: State<'_, App>, source: WorkSourceArg) -> R<()> {
    let ws = match source {
        WorkSourceArg::Pool { pool_id } => WorkSourceSetting::Pool { pool_id },
        WorkSourceArg::PoolCustom { host, port, tls } => {
            if host.len() > 253 {
                return Err("a host name is up to 253 characters".into());
            }
            WorkSourceSetting::PoolCustom { host, port, tls }
        }
        WorkSourceArg::Solo { rpc_url } => {
            if rpc_url.len() > 512 {
                return Err("a node address is up to 512 characters".into());
            }
            WorkSourceSetting::Solo { rpc_url }
        }
        WorkSourceArg::Stub => WorkSourceSetting::Stub,
    };
    app.sup.update_settings(move |s| {
        s.work_source = ws;
        Ok(())
    })
    .map_err(err)
}

#[tauri::command]
fn set_mode(app: State<'_, App>, mode: String, limits: Limits, power_budget_w: Option<f64>) -> R<()> {
    let m = match mode.as_str() {
        "quiet" => Mode::Quiet,
        "efficient" => Mode::Efficient,
        "max" => Mode::Max,
        _ => return Err("mode is quiet, efficient or max".into()),
    };
    limits.validate()?;
    app.sup.update_settings(move |s| {
        s.mode = m;
        s.limits = limits;
        s.power_budget_w = power_budget_w;
        Ok(())
    })
    .map_err(err)
}

#[derive(Deserialize)]
struct TariffArg {
    flat: Option<f64>,
    windows: Option<Vec<[f64; 3]>>,
}

#[tauri::command]
fn set_tariff(app: State<'_, App>, tariff: TariffArg) -> R<()> {
    let t = match (tariff.flat, tariff.windows) {
        (Some(rate), _) => Tariff::Flat { rate },
        (None, Some(w)) => {
            if w.len() > 8 {
                return Err("up to 8 windows".into());
            }
            Tariff::Schedule { windows: w.iter().map(|[a, b, r]| Window { start_min: (*a).clamp(0.0, 1439.0) as u32, end_min: (*b).clamp(0.0, 1440.0) as u32, rate: *r }).collect() }
        }
        _ => return Err("a flat rate or windows".into()),
    };
    t.validate()?;
    app.sup.update_settings(move |s| {
        s.tariff = t;
        Ok(())
    })
    .map_err(err)
}

#[derive(Deserialize)]
struct CostsArg {
    wall_watts: Option<f64>,
    price_per_ign: Option<f64>,
    other_per_day: Option<f64>,
    currency: Option<String>,
    host_allowance_w: Option<f64>,
}

#[tauri::command]
fn set_costs(app: State<'_, App>, costs: CostsArg) -> R<()> {
    app.sup.update_settings(move |s| {
        s.costs.wall_watts = costs.wall_watts;
        s.costs.price_per_ign = costs.price_per_ign;
        if let Some(o) = costs.other_per_day {
            s.costs.other_per_day = o;
        }
        if let Some(c) = costs.currency {
            s.costs.currency = c.to_ascii_uppercase();
        }
        s.costs.host_allowance_w = costs.host_allowance_w;
        Ok(())
    })
    .map_err(err)
}

#[tauri::command]
fn set_device_enabled(app: State<'_, App>, fingerprint: String, enabled: bool) -> R<()> {
    if !igneum_worker_proto::is_hex(&fingerprint, 64) {
        return Err("not a device fingerprint".into());
    }
    app.sup.update_settings(move |s| {
        s.devices_off.retain(|f| *f != fingerprint);
        s.devices_on.retain(|f| *f != fingerprint);
        if enabled {
            s.devices_on.push(fingerprint);
        } else {
            s.devices_off.push(fingerprint);
        }
        Ok(())
    })
    .map_err(err)
}

#[tauri::command]
fn pin_knob(app: State<'_, App>, fingerprint: String, knob: String, value: Option<f64>) -> R<()> {
    if !igneum_worker_proto::is_hex(&fingerprint, 64) || knob.len() > 32 || !knob.bytes().all(|b| b.is_ascii_alphanumeric() || b == b'_') {
        return Err("not a device fingerprint or knob name".into());
    }
    if let Some(v) = value {
        if !v.is_finite() {
            return Err("the value must be a number".into());
        }
    }
    app.sup.update_settings(move |s| {
        let e = s.pins.entry(fingerprint).or_default();
        match value {
            Some(v) => {
                e.insert(knob, v);
            }
            None => {
                e.remove(&knob);
            }
        }
        Ok(())
    })
    .map_err(err)
}

#[tauri::command]
fn start(app: State<'_, App>) -> R<()> {
    app.sup.start().map_err(err)
}
#[tauri::command]
fn stop(app: State<'_, App>) {
    app.sup.stop()
}
#[tauri::command]
fn pause(app: State<'_, App>) {
    app.sup.pause()
}
#[tauri::command]
fn resume(app: State<'_, App>) {
    app.sup.resume()
}

#[tauri::command]
fn tune(app: State<'_, App>, fingerprint: Option<String>) -> R<()> {
    if let Some(f) = &fingerprint {
        if !igneum_worker_proto::is_hex(f, 64) {
            return Err("not a device fingerprint".into());
        }
    }
    app.sup.tune(fingerprint).map_err(err)
}

#[derive(Deserialize)]
struct ScheduleArg {
    time_of_use: TimeOfUse,
    gaming_pause: GamingPause,
    pause_on_battery: bool,
    resume_after_sleep: bool,
}

#[tauri::command]
fn set_schedule(app: State<'_, App>, schedule: ScheduleArg) -> R<()> {
    schedule.time_of_use.validate()?;
    if schedule.gaming_pause.resume_after_s > 3600 {
        return Err("resume after up to an hour".into());
    }
    app.sup.update_settings(move |s| {
        s.time_of_use = schedule.time_of_use;
        s.gaming_pause = schedule.gaming_pause;
        s.pause_on_battery = schedule.pause_on_battery;
        s.resume_after_sleep = schedule.resume_after_sleep;
        Ok(())
    })
    .map_err(err)
}

#[tauri::command]
fn set_update_policy(app: State<'_, App>, policy: String) -> R<()> {
    let p = match policy.as_str() {
        "ask" => UpdatePolicy::Ask,
        "auto" => UpdatePolicy::Auto,
        _ => return Err("ask or auto".into()),
    };
    app.sup.update_settings(move |s| {
        s.update_policy = p;
        Ok(())
    })
    .map_err(err)
}

#[tauri::command]
fn set_preferences(app: State<'_, App>, theme: Option<String>, machine_name: Option<String>, worker_name: Option<String>, setup_done: Option<bool>) -> R<()> {
    app.sup.update_settings(move |s| {
        if let Some(t) = theme {
            s.theme = t;
        }
        if let Some(m) = machine_name {
            s.machine_name = m;
        }
        if let Some(w) = worker_name {
            s.worker_name = w;
        }
        if let Some(d) = setup_done {
            s.setup_done = d;
        }
        Ok(())
    })
    .map_err(err)
}

#[tauri::command]
fn install_update() -> R<()> {
    Err("updates are not configured in this build".into())
}

#[tauri::command]
fn journal_mark(app: State<'_, App>, kind: String, detail: String) -> R<()> {
    app.sup.journal_mark(&kind, &detail).map_err(err)
}

#[tauri::command]
fn journal_export(app: State<'_, App>) -> String {
    app.sup.journal_export()
}

#[tauri::command]
fn log_tail(app: State<'_, App>, n: usize) -> Vec<String> {
    app.sup.log_tail(n.min(5000))
}

#[tauri::command]
fn quit(app: State<'_, App>, handle: AppHandle) {
    app.sup.request_stop();
    handle.exit(0);
}

fn data_dir(handle: &AppHandle) -> std::path::PathBuf {
    handle.path().app_data_dir().unwrap_or_else(|_| std::env::temp_dir().join("ember"))
}

fn resources_dir(handle: &AppHandle) -> std::path::PathBuf {
    // the bundle's resources, where `resources/*` from tauri.conf.json lands; in development, the staged folder
    let bundled = handle.path().resource_dir().map(|p| p.join("resources")).ok().filter(|p| p.is_dir());
    bundled.or_else(|| handle.path().resource_dir().ok()).unwrap_or_else(|| std::path::PathBuf::from("resources"))
}

pub fn run() {
    tauri::Builder::default()
        .setup(|app| {
            let handle = app.handle().clone();
            let cfg = Config::new(APP_NAME, VERSION, &resources_dir(&handle), data_dir(&handle));
            let sup = Supervisor::new(cfg);
            app.manage(App { sup: sup.clone() });
            // the ticker: four ticks a second
            sup.spawn_ticker();
            sup.spawn_watchdog();
            // the state push: once a second to the window
            let s2 = sup.clone();
            thread::spawn(move || loop {
                let snap = s2.snapshot();
                let _ = handle.emit("state", &snap);
                thread::sleep(Duration::from_secs(1));
            });
            Ok(())
        })
        .on_window_event(|window, event| {
            // closing the window hides it; mining continues; Quit in the rail stops everything
            if let tauri::WindowEvent::CloseRequested { api, .. } = event {
                let _ = window.hide();
                api.prevent_close();
            }
        })
        .invoke_handler(tauri::generate_handler![
            get_state,
            detect_devices,
            set_payout_address,
            set_work_source,
            set_mode,
            set_tariff,
            set_costs,
            set_device_enabled,
            pin_knob,
            start,
            stop,
            pause,
            resume,
            tune,
            set_schedule,
            set_update_policy,
            set_preferences,
            install_update,
            journal_mark,
            journal_export,
            log_tail,
            quit
        ])
        .run(tauri::generate_context!())
        .expect("Ember could not start");
}
