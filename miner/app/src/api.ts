// The typed side of the command layer (design section 2.4). Every call is one supervisor command; the
// interface never computes a number or touches a path.
import { invoke } from "@tauri-apps/api/core";
import { listen } from "@tauri-apps/api/event";

export type Params = Record<string, number>;

export interface Knob { name: string; min: number; max: number; step: number | "x2"; default: number; unit: string }

export interface Telemetry {
  power_w?: number; temp_c?: number; fan_pct?: number; core_mhz?: number; mem_mhz?: number; util_pct?: number;
  memory_used_bytes?: string; throttle: string[]; thermal_pressure?: string; on_battery?: boolean; low_power_mode?: boolean;
  memory_pressure?: string; asleep?: boolean; duty_effective?: number; gpu_allocated_bytes?: string;
}

export interface TuneView { running: boolean; chosen: Params; candidates: number; stopped: unknown; when_unix?: number; fresh: boolean; current_candidate?: Params }

export interface SlotView {
  fingerprint: string; index: number; backend: string; name: string; vendor: string; kind: string;
  id: string; bus_id: string; memory_total: number; memory_free: number; driver: string; arch: string; runtime: string;
  label: "supported" | "feasibility_cohort" | "not_supported"; reason: string; enabled: boolean;
  state: "off" | "idle" | "opening" | "preparing" | "mining" | "paused" | "tuning" | "refused" | "faulted"; message: string;
  phase: string; epoch?: number | null; algo: string;
  params: Params; pinned: Params; knobs: Knob[]; telemetry?: Telemetry; reports: string[]; validated_hps: number; reported_hps: number;
  invalid_results: number; restarts: number; worker: string; worker_version: string; worker_note: string; tune?: TuneView;
  transition: { state: "current" | "preparing" | "ready" | "switching" | "recovering"; to?: number; from?: number };
  dataset_epoch?: number | null; next_epoch?: number | null; can_warm: boolean;
  switch_median_ms?: number | null; switch_p99_ms?: number | null; switch_samples: number;
  energy?: EnergyView | null;
  build_progress?: number | null;
  power_offer?: string | null;
}

/// Energy per accepted hash from the device's own telemetry, with the instrument grade named (telemetry, wall, estimated).
export interface EnergyView { joules_per_hash: number; mh_per_wh: number; power_w: number; grade: string }

export interface Totals { validated_hps: number; reported_hps: number; device_watts: number; accepted: number; rejected: number; stale: number; invalid: number; source_invalid: number; blocks_found: number; worker_restarts: number; uptime_s: number; downtime_s: number; preparing_s: number; cancelled: number; energy?: EnergyView | null; transitions: number; transition_lost_hashes: number; switch_median_ms?: number | null; switch_p99_ms?: number | null; switch_samples: number; tick_max_ms: number; unverified: number; selfcheck_ms: number; verify_median_ms: number; verify_late: number; source_switches: number; source_reconnects: number; submitted: number; last_submit_age_s?: number | null; last_outcome_age_s?: number | null; expected_block_s?: number | null }

export interface Line { eyebrow: string; label: string; value: string; note: string; negative: boolean }
export interface Stage { name: string; status: "pending" | "active" | "done" | "failed"; detail: string }
export interface Notice { level: string; text: string; detail: string }

export interface Limits { power_cap_w?: number | null; temp_cap_c?: number | null; fan_cap_pct?: number | null; min_fraction: number; min_rate_hps?: number | null }

export interface Settings {
  work_source_backup: { kind: "pool"; pool_id: string } | { kind: "pool_custom"; host: string; port: number; tls: boolean } | { kind: "solo"; rpc_url: string } | { kind: "stub" } | { kind: "unset" };
  version: number; setup_done: boolean; payout_address?: string | null; algo?: string | null;
  work_source: { kind: "pool"; pool_id: string } | { kind: "pool_custom"; host: string; port: number; tls: boolean } | { kind: "solo"; rpc_url: string } | { kind: "stub" } | { kind: "unset" };
  mode: "quiet" | "efficient" | "max"; limits: Limits; power_budget_w?: number | null;
  tariff: { kind: "flat"; rate: number } | { kind: "schedule"; windows: { start_min: number; end_min: number; rate: number }[] };
  costs: { wall_watts?: number | null; price_per_ign?: number | null; other_per_day: number; currency: string; host_allowance_w?: number | null };
  devices_off: string[]; devices_on: string[]; pins: Record<string, Params>;
  time_of_use: { enabled: boolean; pause_above_rate: number; resume_by_itself: boolean };
  gaming_pause: { enabled: boolean; resume_by_itself: boolean; resume_after_s: number };
  update_policy: "ask" | "auto"; resume_after_sleep: boolean; pause_on_battery: boolean;
  machine_name: string; worker_name: string; theme: string; settled_since_unix?: number | null;
}

export interface Snapshot {
  tick_age_ms: number; tick_fault?: string | null; launch_errors: string[];
  app: string; version: string; stub: boolean;
  mining: "idle" | "starting" | "mining" | "paused" | "stopping"; paused_for?: string | null;
  mode: "quiet" | "efficient" | "max"; limits: Limits; devices: SlotView[]; totals: Totals;
  source?: { name: string; algo: string; connected: boolean; problem?: string | null; solo: boolean; difficulty: number; current_job?: string; height: number } | null;
  estimate?: unknown; estimate_lines: Line[]; stages: Stage[]; first_work_accepted: boolean; notices: Notice[];
  settings: Settings; log_lines: number; tune_running: boolean; over_power_budget_w?: number | null; pools_status: string;
}

export const api = {
  getState: () => invoke<Snapshot>("get_state"),
  detectDevices: () => invoke<Snapshot>("detect_devices"),
  setPayoutAddress: (address: string) => invoke<string>("set_payout_address", { address }),
  setWorkSource: (source: { kind: "stub" } | { kind: "pool"; pool_id: string } | { kind: "pool_custom"; host: string; port: number; tls: boolean } | { kind: "solo"; rpc_url: string }) => invoke<void>("set_work_source", { source }),
  setMode: (mode: string, limits: Limits, powerBudgetW: number | null) => invoke<void>("set_mode", { mode, limits, powerBudgetW }),
  setTariff: (tariff: { flat?: number; windows?: number[][] }) => invoke<void>("set_tariff", { tariff }),
  setCosts: (costs: { wall_watts: number | null; price_per_ign: number | null; other_per_day: number | null; currency: string | null; host_allowance_w: number | null }) => invoke<void>("set_costs", { costs }),
  setDeviceEnabled: (fingerprint: string, enabled: boolean) => invoke<void>("set_device_enabled", { fingerprint, enabled }),
  pinKnob: (fingerprint: string, knob: string, value: number | null) => invoke<void>("pin_knob", { fingerprint, knob, value }),
  start: () => invoke<void>("start"),
  stop: () => invoke<void>("stop"),
  pause: () => invoke<void>("pause"),
  resume: () => invoke<void>("resume"),
  tune: (fingerprint: string | null) => invoke<void>("tune", { fingerprint }),
  setSchedule: (schedule: { time_of_use: Settings["time_of_use"]; gaming_pause: Settings["gaming_pause"]; pause_on_battery: boolean; resume_after_sleep: boolean }) => invoke<void>("set_schedule", { schedule }),
  setUpdatePolicy: (policy: "ask" | "auto") => invoke<void>("set_update_policy", { policy }),
  setPreferences: (p: { theme?: string; machineName?: string; workerName?: string; setupDone?: boolean }) => invoke<void>("set_preferences", p),
  journalMark: (kind: string, detail: string) => invoke<void>("journal_mark", { kind, detail }).catch(() => undefined),
  journalExport: () => invoke<string>("journal_export"),
  logTail: (n: number) => invoke<string[]>("log_tail", { n }),
  quit: () => invoke<void>("quit"),
  onState: (f: (s: Snapshot) => void) => listen<Snapshot>("state", (e) => f(e.payload)),
};
