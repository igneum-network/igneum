// Ember: the interface. Renders the supervisor's snapshot (pushed once a second) and sends commands.
// Screens: welcome, cards, address, source, tariff, start (the first-use flow of design section 2.6), then the
// dashboard with four pages behind the rail (Mine, Earnings, Pools, Settings). Written new on the kept look.
import { api, Snapshot, SlotView, Settings } from "./api";

type Phase = "loading" | "welcome" | "cards" | "address" | "source" | "tariff" | "start" | "dashboard";

const $ = <T extends HTMLElement = HTMLElement>(id: string) => document.getElementById(id) as T;
const body = document.body;

let snap: Snapshot | null = null;
let phase: Phase = "loading";
let page = "mine";
let sourceChoice: "stub" | "solo" | "pool_custom" | null = null;
let noticeClosed = "";
let drawerOpen = false;
let lastLogCount = -1;

function setPhase(p: Phase) {
  if (phase !== "loading" && phase !== p) api.journalMark("screen_leave", phase);
  phase = p;
  body.dataset.phase = p;
  const rail = p === "dashboard";
  $("rail").hidden = !rail;
  body.classList.toggle("has-rail", rail);
  $("page-title").hidden = !rail;
  $("top-brand").hidden = rail;
  api.journalMark("screen_enter", p);
}

function setPage(p: string) {
  page = p;
  body.dataset.page = p;
  document.querySelectorAll<HTMLButtonElement>(".rail-nav .nav").forEach((b) => {
    const on = b.dataset.page === p;
    b.classList.toggle("on", on);
    if (on) b.setAttribute("aria-current", "page"); else b.removeAttribute("aria-current");
  });
  document.querySelectorAll<HTMLElement>(".page").forEach((el) => (el.hidden = el.dataset.page !== p));
  $("page-title-text").textContent = { mine: "Mine", earnings: "Earnings", pools: "Pools", settings: "Settings" }[p] ?? p;
}

function toast(text: string) {
  const t = $("toast");
  t.textContent = text;
  t.hidden = false;
  window.setTimeout(() => (t.hidden = true), 2600);
}

function fmtMh(h: number) { return (h / 1e6).toFixed(h >= 1e7 ? 1 : 3); }
function gb(b: number) { return (b / 2 ** 30).toFixed(1) + " GB"; }
function el<K extends keyof HTMLElementTagNameMap>(tag: K, cls?: string, text?: string): HTMLElementTagNameMap[K] {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text !== undefined) e.textContent = text;
  return e;
}

// ---------------------------------------------------------------------------------------------------------
// rendering

function labelWord(l: SlotView["label"]) {
  return l === "supported" ? "supported" : l === "feasibility_cohort" ? "feasibility cohort" : "not supported";
}

/// The state word and the class it carries, so colour and word agree (the design system's section 7: mining is
/// ember, starting and building and waiting are running, a fault or a refusal is bad, the rest ash).
function stateWordClass(d: SlotView, s: Snapshot): [string, string] {
  switch (d.state) {
    case "off": return ["off", ""];
    case "idle": return ["ready", ""];
    case "opening": return ["opening", "running"];
    case "preparing": return [d.phase === "transition" ? "moving to the next epoch (not mining)" : d.phase === "compile" ? "building the kernels (not mining)" : "building the dataset (not mining)", "running"];
    case "mining": return s.mining === "starting" ? ["starting", "running"] : (s.source && (!s.source.connected || !s.source.current_job)) ? ["waiting for work", "running"] : ["mining", "on"];
    case "paused": return ["paused", ""];
    case "tuning": return ["tuning", "running"];
    case "refused": return ["cannot mine", "bad"];
    case "faulted": return ["stopped", "bad"];
  }
}

function stateWord(d: SlotView, s: Snapshot) {
  return stateWordClass(d, s)[0];
}

function renderCardsStep(s: Snapshot) {
  const list = $("cards-list");
  list.innerHTML = "";
  if (s.devices.length === 0) {
    $("cards-sub").textContent = "No graphics card reported in.";
    $("btn-cards-next").toggleAttribute("disabled", true);
    return;
  }
  $("cards-sub").textContent = s.devices.length === 1 ? "One device reported in." : `${s.devices.length} devices reported in.`;
  for (const d of s.devices) {
    const row = el("div", "gpu-row" + (d.enabled ? "" : " off"));
    const badge = el("div", "badge " + d.vendor, d.vendor === "nvidia" ? "NV" : d.vendor === "amd" ? "AMD" : d.vendor === "apple" ? "" : "GPU");
    const info = el("div", "info");
    const name = el("div", "name", d.name);
    const kind = el("span", "kind " + d.kind, d.kind);
    name.appendChild(kind);
    const meta = el("div", "meta");
    meta.append(el("span", "", gb(d.memory_total) + " total"), el("span", "", gb(d.memory_free) + " free"), el("span", "", d.driver), el("span", "", d.backend));
    const reason = el("div", "reason", labelWord(d.label) + ": " + d.reason + (d.worker_note ? " (" + d.worker_note + ")" : ""));
    info.append(name, meta, reason);
    const sw = el("label", "switch");
    const cb = el("input"); cb.type = "checkbox"; cb.checked = d.enabled;
    cb.addEventListener("change", () => api.setDeviceEnabled(d.fingerprint, cb.checked).catch((e) => toast(String(e))));
    sw.append(cb, el("span", "track"));
    row.append(badge, info, sw);
    list.appendChild(row);
  }
  const any = s.devices.some((d) => d.enabled);
  $("btn-cards-next").toggleAttribute("disabled", !any);
  $("cards-note").hidden = any;
  $("cards-note").textContent = any ? "" : "Turn on at least one device to continue.";
}

function renderSourceStep(s: Snapshot) {
  const box = $("source-options");
  if (box.childElementCount === 0) {
    const options: { id: "stub" | "pool_custom" | "solo"; t: string; tag?: string; s: string; fields?: { id: string; label: string; ph: string; type?: string }[] }[] = [
      { id: "pool_custom", t: "A pool", s: "Smaller, steadier payments for a fee. The pool's own disclosure (fee, payout terms) is read from its site; reliability and concentration come under Pools once the list is published.", fields: [{ id: "pool-host", label: "host", ph: "pool.example" }, { id: "pool-port", label: "port", ph: "3333", type: "number" }] },
      { id: "solo", t: "Solo with a node", s: "You keep the whole block; blocks are rare. Needs a node you run, or a remote node you trust (its operator sees your work). A solo block pays the node's coinbase address, not the address entered here.", fields: [{ id: "solo-url", label: "node address", ph: "http://127.0.0.1:8545" }] },
      { id: "stub", t: "Rehearse with the stub", tag: "nothing is mined", s: "Runs the whole path on a simulated device so you can see how the app behaves. No card is used, nothing is earned." },
    ];
    for (const o of options) {
      const lab = el("label", "option");
      const r = el("input"); r.type = "radio"; r.name = "source"; r.value = o.id;
      r.addEventListener("change", () => { sourceChoice = o.id; document.querySelectorAll(".option").forEach((x) => x.classList.toggle("on", x === lab)); $("btn-source-next").toggleAttribute("disabled", false); });
      const bodyEl = el("span", "body");
      const t = el("span", "t", o.t);
      if (o.tag) t.appendChild(el("span", "tag", o.tag));
      bodyEl.append(t, el("span", "s", o.s));
      if (o.fields) {
        const row = el("span", "row wrap");
        for (const f of o.fields) {
          const field = el("span", "field");
          field.appendChild(el("span", "k", f.label));
          const inp = el("input", "addr-input mono" + (f.type === "number" ? " short" : "")); inp.id = f.id; inp.placeholder = f.ph; inp.type = f.type ?? "text"; inp.spellcheck = false;
          inp.addEventListener("click", (e) => e.stopPropagation());
          field.appendChild(inp);
          row.appendChild(field);
        }
        bodyEl.appendChild(row);
        const tls = el("label", "check");
        const cb = el("input"); cb.type = "checkbox"; cb.id = o.id + "-tls"; cb.checked = true;
        if (o.id === "pool_custom") { tls.append(cb, el("span", "", "TLS (the pool's certificate is checked)")); bodyEl.appendChild(tls); }
      }
      lab.append(r, el("span", "radio"), bodyEl);
      box.appendChild(lab);
    }
  }
  const ws = s.settings.work_source.kind;
  $("source-note").textContent = ws === "unset" ? "" : "Chosen: " + ws.replace("_", " ") + ".";
}

function renderStages(container: HTMLElement, s: Snapshot) {
  container.innerHTML = "";
  for (const st of s.stages) {
    const row = el("div", "stage " + st.status);
    row.append(el("i"), el("span", "t", st.name), el("span", "d", st.detail));
    container.appendChild(row);
  }
}

function renderMine(s: Snapshot) {
  const t = s.totals;
  $("t-hash").textContent = fmtMh(t.validated_hps);
  $("t-hash-sub").textContent = "reported " + fmtMh(t.reported_hps) + " MH/s" + (t.expected_block_s ? ", a block expected every " + Math.round(t.expected_block_s) + " s" : "") + (t.energy ? ", " + t.energy.mh_per_wh.toFixed(1) + " MH/Wh (" + t.energy.grade + ")" : "");
  $("t-power").textContent = t.device_watts.toFixed(0);
  $("t-power-sub").textContent = s.over_power_budget_w ? "over your budget by " + s.over_power_budget_w.toFixed(0) + " W" : "device readings";
  const net = s.estimate_lines.find((l) => l.label === "Estimated net");
  $("t-net").textContent = net ? net.value.replace(" a day", "") : "--";
  $("t-net").classList.toggle("bad", !!net?.negative);
  // accepted work turns green on the first share: the one met reading on the screen (a share the source took)
  $("t-work").classList.toggle("is-met", t.accepted > 0);
  $("t-net-sub").textContent = net ? (net.negative ? "a loss at this tariff" : "estimated net") : "estimated net";
  $("t-work").textContent = String(t.accepted);
  $("t-work-sub").textContent = `rejected ${t.rejected}, stale ${t.stale}, invalid ${t.invalid}` + (t.source_invalid ? `, refused as invalid by the source ${t.source_invalid}` : "");

  const toggle = $<HTMLButtonElement>("btn-toggle");
  toggle.disabled = s.mining === "stopping";
  const txt = $("btn-toggle-text"), sub = $("btn-toggle-sub");
  toggle.classList.toggle("stop", s.mining !== "idle");
  if (s.mining === "idle") { txt.textContent = "Start mining"; sub.textContent = s.devices.some((d) => d.enabled) ? "with " + s.devices.filter((d) => d.enabled).length + " device(s)" : "turn a device on first"; }
  else if (s.mining === "paused") { txt.textContent = "Resume"; sub.textContent = s.paused_for === "user" ? "paused" : s.paused_for === "memory" ? "paused under memory pressure (the dataset released, the cache kept); resumes when the system returns to normal" : "paused: " + String(s.paused_for) + ", tap to resume"; }
  else { txt.textContent = "Stop mining"; sub.textContent = s.mining === "starting" ? "starting" : `up ${Math.floor(t.uptime_s / 60)} min, down ${t.downtime_s} s`; }

  $("stages-card").hidden = s.mining === "idle" || s.first_work_accepted;
  renderStages($("stages"), s);

  const list = $("d-cards");
  list.innerHTML = "";
  $("d-cards-eyebrow").textContent = s.devices.length ? `${s.devices.filter((d) => d.enabled).length} of ${s.devices.length} on` : "none";
  for (const d of s.devices) {
    const line = el("div", "gpu-line " + (d.enabled ? "" : "off"));
    const main = el("div", "gl-main");
    const badge = el("div", "badge " + d.vendor, d.vendor === "nvidia" ? "NV" : d.vendor === "amd" ? "AMD" : d.vendor === "apple" ? "" : "GPU");
    const who = el("div", "who");
    who.appendChild(el("div", "name", d.name));
    const st = el("div", "st " + stateWordClass(d, s)[1]);
    st.append(el("i"), document.createTextNode(stateWord(d, s) + (d.epoch != null ? " (epoch " + d.epoch + ")" : "") + (d.algo ? " " + d.algo : "") + (d.tune?.running ? " (" + Object.entries(d.tune.current_candidate ?? {}).map(([k, v]) => k + " " + v).join(", ") + ")" : "")));
    who.appendChild(st);
    if (d.message) who.appendChild(el("div", "msg", d.message));
    if (d.build_progress != null) who.appendChild(el("div", "msg", "next epoch's dataset " + Math.round(d.build_progress * 100) + "% built beside mining"));
    if (d.power_offer) who.appendChild(el("div", "msg", d.power_offer));
    const nums = el("div", "nums");
    const tel = d.telemetry;
    const num = (v: string, unit: string, k: string, cls = "") => { const n = el("div", "num " + cls); const vv = el("div", "v", v); vv.appendChild(el("span", "unit", unit)); n.append(vv, el("div", "k", k)); return n; };
    nums.append(num(fmtMh(d.validated_hps), "MH/s", "validated", "hash"), num(d.energy ? d.energy.mh_per_wh.toFixed(1) : "--", "MH/Wh", d.energy ? "energy (" + d.energy.grade + ")" : "energy (no power reading)"), num(tel?.power_w?.toFixed(0) ?? "--", "W", "power"), num(tel?.temp_c?.toFixed(0) ?? "--", "C", "temp", (tel?.temp_c ?? 0) > 80 ? "hot" : ""), num(String(d.invalid_results), "", "invalid"));
    const acts = el("div", "acts");
    const tuneBtn = el("button", "btn tiny", d.tune?.running ? "tuning" : "Tune");
    tuneBtn.disabled = d.state !== "mining" || s.tune_running;
    tuneBtn.addEventListener("click", () => api.tune(d.fingerprint).then(() => toast("Tuning " + d.name)).catch((e) => toast(String(e))));
    const sw = el("label", "switch");
    const cb = el("input"); cb.type = "checkbox"; cb.checked = d.enabled; cb.disabled = s.mining !== "idle";
    cb.addEventListener("change", () => api.setDeviceEnabled(d.fingerprint, cb.checked).catch((e) => toast(String(e))));
    sw.append(cb, el("span", "track"));
    acts.append(tuneBtn, sw);
    main.append(badge, who, nums, acts);
    line.appendChild(main);
    const tune = el("div", "gl-tune" + (d.tune?.running ? " running" : ""));
    const p = Object.entries(d.params).map(([k, v]) => k + " " + v + (d.pinned[k] !== undefined ? " (pinned)" : "")).join("  ");
    tune.appendChild(el("span", "t", (d.tune ? (d.tune.running ? "tuning: " : d.tune.fresh ? "tuned: " : "tuned (due again): ") : "starting point: ") + p));
    line.appendChild(tune);
    list.appendChild(line);
  }
  if (!s.devices.length) list.appendChild(el("div", "empty", "No device yet. Detect again under setup, or check the worker is installed."));
  $<HTMLButtonElement>("btn-tune-all").disabled = s.mining !== "mining" || s.tune_running;

  document.querySelectorAll<HTMLButtonElement>("#mode-seg .seg-b").forEach((b) => { const on = b.dataset.mode === s.mode; b.classList.toggle("on", on); b.setAttribute("aria-checked", String(on)); });
  const meaning = { quiet: "as much validated work as fits under your power, temperature and fan caps", efficient: "the most validated work per watt, keeping at least " + Math.round(s.limits.min_fraction * 100) + "% of the measured maximum", max: "the highest validated rate inside the safety limits" }[s.mode];
  $("mode-line").textContent = meaning;
  $("mode-eyebrow").textContent = s.tune_running ? "tuning" : "";
  renderLimits(s);
}

function renderLimits(s: Snapshot) {
  const box = $("limits");
  if (box.dataset.mode === s.mode && box.childElementCount) return;
  box.dataset.mode = s.mode;
  box.innerHTML = "";
  const l = s.limits;
  const field = (label: string, id: string, value: number | null | undefined, step: string, ph: string) => {
    const f = el("div", "field"); f.append(el("div", "k", label));
    const i = el("input", "addr-input short mono"); i.type = "number"; i.id = id; i.step = step; i.placeholder = ph; i.value = value == null ? "" : String(value);
    f.appendChild(i); return f;
  };
  const row = el("div", "row wrap");
  if (s.mode === "quiet") row.append(field("power cap, W", "l-power", l.power_cap_w, "5", "120"), field("temperature cap, C", "l-temp", l.temp_cap_c, "1", "70"), field("fan cap, %", "l-fan", l.fan_cap_pct, "5", "60"));
  if (s.mode === "efficient") row.append(field("keep at least, % of max", "l-frac", Math.round(l.min_fraction * 100), "5", "60"));
  row.append(field("machine power budget, W", "l-budget", s.settings.power_budget_w, "10", "optional"));
  const save = el("button", "btn small", "Save");
  save.addEventListener("click", () => {
    const num = (id: string) => { const e = document.getElementById(id) as HTMLInputElement | null; if (!e || e.value === "") return null; const v = Number(e.value); return Number.isFinite(v) ? v : null; };
    const limits = { ...l, power_cap_w: num("l-power") ?? l.power_cap_w ?? null, temp_cap_c: num("l-temp") ?? l.temp_cap_c ?? null, fan_cap_pct: num("l-fan") ?? l.fan_cap_pct ?? null, min_fraction: (num("l-frac") ?? Math.round(l.min_fraction * 100)) / 100 };
    api.setMode(s.mode, limits, num("l-budget")).then(() => toast("Saved")).catch((e) => toast(String(e)));
  });
  row.appendChild(save);
  box.appendChild(row);
  // a cap on a field a worker does not report is shown as such, never guessed (GPU engineer B's rule)
  if (s.mode === "quiet") {
    const missing = s.devices.filter((d) => d.enabled && d.reports.length).map((d) => {
      const m = ["power_w", "temp_c", "fan_pct"].filter((f) => !d.reports.includes(f));
      const words: Record<string, string> = { power_w: "power", temp_c: "temperature", fan_pct: "fan" };
      return m.length ? d.name + ": " + m.map((f) => words[f]).join(", ") + " not reported by this worker" : "";
    }).filter(Boolean);
    if (missing.length) box.appendChild(el("p", "note", missing.join("; ") + ". A cap on a value that is not reported has no effect."));
  }
}

function renderEarnings(s: Snapshot) {
  const box = $("e-lines");
  box.innerHTML = "";
  if (!s.estimate_lines.length) { box.appendChild(el("div", "empty", "Start mining to see the estimate with its costs.")); }
  for (const l of s.estimate_lines) {
    const row = el("div", "money-row");
    // every estimate line is graded estimated (molten); the settled card's figures are green only when read from the chain
    row.append(el("span", "eyebrow", l.eyebrow), el("span", "v small" + (l.negative ? " bad" : ""), l.value), el("span", "s", l.label + (l.note ? ": " + l.note : "")), el("span", "ig-grade is-estimated", "estimated"));
    box.appendChild(row);
  }
  $("r-address").textContent = s.settings.payout_address ?? "not set";
}

function renderPools(s: Snapshot) {
  $("pools-eyebrow").textContent = s.pools_status;
  const list = $("pools-list");
  if (!list.childElementCount) list.appendChild(el("div", "empty", "No pool list is published yet. A pool you add yourself under setup is used as you entered it."));
}

let settingsFilled = false;
function renderSettings(s: Snapshot) {
  const st: Settings = s.settings;
  $("s-version").textContent = `${s.app} ${s.version}${s.stub ? " (stub worker: no card is mined)" : ""}`;
  if (settingsFilled) return;
  settingsFilled = true;
  $<HTMLInputElement>("s-ccy").value = st.costs.currency;
  $<HTMLInputElement>("s-rate").value = st.tariff.kind === "flat" ? String(st.tariff.rate) : "";
  $<HTMLInputElement>("s-wall").value = st.costs.wall_watts == null ? "" : String(st.costs.wall_watts);
  $<HTMLInputElement>("s-price").value = st.costs.price_per_ign == null ? "" : String(st.costs.price_per_ign);
  $<HTMLInputElement>("s-other").value = String(st.costs.other_per_day);
  $<HTMLInputElement>("s-game").checked = st.gaming_pause.enabled;
  $<HTMLInputElement>("s-game-resume").checked = st.gaming_pause.resume_by_itself;
  $<HTMLInputElement>("s-tou").checked = st.time_of_use.enabled;
  $<HTMLInputElement>("s-tou-rate").value = String(st.time_of_use.pause_above_rate);
  $<HTMLInputElement>("s-tou-resume").checked = st.time_of_use.resume_by_itself;
  $<HTMLInputElement>("s-battery").checked = st.pause_on_battery;
  $<HTMLInputElement>("s-sleep").checked = st.resume_after_sleep;
  $<HTMLInputElement>("s-auto-update").checked = st.update_policy === "auto";
  $<HTMLInputElement>("s-address").value = st.payout_address ?? "";
  document.querySelectorAll<HTMLButtonElement>("#theme-seg .seg-b").forEach((b) => b.classList.toggle("on", b.dataset.theme === st.theme));
}

function renderNotice(s: Snapshot) {
  const n = [...s.notices].reverse().find((x) => x.text !== noticeClosed);
  $("notices").hidden = !n;
  if (!n) return;
  $("notice").className = "notice " + (n.level === "bad" ? "bad" : n.level === "warn" ? "warn" : "");
  $("notice-text").textContent = n.text;
  $("notice-detail").textContent = n.detail;
  $("notice-detail").hidden = !n.detail;
}

function renderTop(s: Snapshot) {
  const pill = $("pill");
  const word = s.mining === "idle" ? "idle" : s.mining === "paused" ? "paused" : s.mining === "starting" ? "starting" : s.mining === "stopping" ? "stopping" : s.tune_running ? "tuning" : "mining";
  $("pill-text").textContent = word + (s.stub ? " (stub)" : "");
  const anyBad = s.devices.some((d) => d.state === "faulted" || d.state === "refused");
  const anyMining = s.devices.some((d) => stateWordClass(d, s)[1] === "on");
  pill.classList.toggle("warn", anyBad);
  pill.classList.toggle("on", !anyBad && anyMining);
  pill.classList.toggle("running", !anyBad && !anyMining && s.mining !== "idle");
  $("foot-version").textContent = `${s.version}${s.stub ? " stub" : ""}`;
  $("rail-status").textContent = s.source ? (s.source.problem ? s.source.name + ": " + s.source.problem : s.source.name) : "";
  $("page-sub").textContent = s.mining === "mining" ? `${fmtMh(s.totals.validated_hps)} MH/s validated` : "";
}

function render() {
  if (!snap) return;
  const s = snap;
  renderTop(s);
  renderNotice(s);
  if (phase === "cards") renderCardsStep(s);
  if (phase === "source") renderSourceStep(s);
  if (phase === "start") { $("start-stages").hidden = s.mining === "idle"; renderStages($("start-stages"), s); if (s.first_work_accepted) { api.setPreferences({ setupDone: true }); api.journalMark("setup_done", ""); setPhase("dashboard"); setPage("mine"); } }
  if (phase === "dashboard") {
    if (page === "mine") renderMine(s);
    if (page === "earnings") renderEarnings(s);
    if (page === "pools") renderPools(s);
    if (page === "settings") renderSettings(s);
  }
  if (drawerOpen && s.log_lines !== lastLogCount) { lastLogCount = s.log_lines; refreshLog(); }
}

async function refreshLog() {
  const lines = await api.logTail(500);
  const pre = $("log");
  pre.textContent = lines.join("\n");
  if ($<HTMLInputElement>("log-follow").checked) $("log-view").scrollTop = $("log-view").scrollHeight;
}

// ---------------------------------------------------------------------------------------------------------
// wiring

function wire() {
  $("btn-begin").addEventListener("click", async () => {
    setPhase("cards");
    try { snap = await api.detectDevices(); render(); } catch (e) { $("cards-sub").textContent = String(e); api.journalMark("failure_shown", "detect"); }
  });
  $("btn-cards-retry").addEventListener("click", async () => { $("cards-sub").textContent = "Asking the graphics cards to report in."; try { snap = await api.detectDevices(); render(); api.journalMark("recovered", "detect"); } catch (e) { $("cards-sub").textContent = String(e); api.journalMark("failure_shown", "detect"); } });
  $("btn-cards-next").addEventListener("click", () => setPhase("address"));
  $("btn-address-back").addEventListener("click", () => setPhase("cards"));
  const addr = $<HTMLInputElement>("addr-input");
  addr.addEventListener("input", () => { const ok = /^0x[0-9a-fA-F]{40}$/.test(addr.value.trim()); $("btn-address-next").toggleAttribute("disabled", !ok); $("addr-err").hidden = ok || addr.value.length < 42; $("addr-err").textContent = "An address is 0x followed by 40 hex characters."; });
  $("btn-address-next").addEventListener("click", async () => {
    try { await api.setPayoutAddress(addr.value.trim()); api.journalMark("address_entered", ""); setPhase("source"); }
    catch (e) { $("addr-err").textContent = String(e); $("addr-err").hidden = false; api.journalMark("address_error", ""); api.journalMark("failure_shown", "address"); }
  });
  $("btn-source-back").addEventListener("click", () => setPhase("address"));
  $("btn-source-next").addEventListener("click", async () => {
    if (!sourceChoice) return;
    try {
      if (sourceChoice === "stub") await api.setWorkSource({ kind: "stub" });
      else if (sourceChoice === "solo") await api.setWorkSource({ kind: "solo", rpc_url: $<HTMLInputElement>("solo-url").value.trim() || "http://127.0.0.1:8545" });
      else {
        const port = Number($<HTMLInputElement>("pool-port").value || 0);
        if (!Number.isInteger(port) || port < 1 || port > 65535) throw new Error("the port is 1 to 65535");
        await api.setWorkSource({ kind: "pool_custom", host: $<HTMLInputElement>("pool-host").value.trim(), port, tls: $<HTMLInputElement>("pool_custom-tls").checked });
      }
      api.journalMark("source_chosen", sourceChoice);
      setPhase("tariff");
    } catch (e) { toast(String(e)); api.journalMark("failure_shown", "source"); }
  });
  $("btn-tariff-back").addEventListener("click", () => setPhase("source"));
  $("btn-tariff-skip").addEventListener("click", () => { api.journalMark("tariff_skipped", ""); setPhase("start"); });
  $("btn-tariff-next").addEventListener("click", async () => {
    const rate = Number($<HTMLInputElement>("tariff-rate").value);
    const ccy = $<HTMLSelectElement>("tariff-ccy").value;
    try { await api.setTariff({ flat: Number.isFinite(rate) ? rate : 0 }); await api.setCosts({ wall_watts: null, price_per_ign: null, other_per_day: null, currency: ccy, host_allowance_w: null }); api.journalMark("tariff_entered", ""); setPhase("start"); }
    catch (e) { toast(String(e)); }
  });
  $("btn-start-back").addEventListener("click", () => setPhase("tariff"));
  $("btn-start").addEventListener("click", async () => {
    $("start-err").hidden = true;
    try { await api.start(); $("btn-start").toggleAttribute("disabled", true); }
    catch (e) { $("start-err").textContent = String(e); $("start-err").hidden = false; api.journalMark("failure_shown", "start"); }
  });

  document.querySelectorAll<HTMLButtonElement>(".rail-nav .nav").forEach((b) => b.addEventListener("click", () => { setPage(b.dataset.page!); render(); }));
  $("btn-toggle").addEventListener("click", async () => {
    if (!snap) return;
    try {
      if (snap.mining === "idle") await api.start();
      else if (snap.mining === "paused") await api.resume();
      else await api.stop();
    } catch (e) { toast(String(e)); }
  });
  $("btn-tune-all").addEventListener("click", () => api.tune(null).then(() => toast("Tuning every device")).catch((e) => toast(String(e))));
  document.querySelectorAll<HTMLButtonElement>("#mode-seg .seg-b").forEach((b) => b.addEventListener("click", () => { if (!snap) return; api.setMode(b.dataset.mode!, snap.limits, snap.settings.power_budget_w ?? null).catch((e) => toast(String(e))); }));
  $("btn-open-log").addEventListener("click", () => toggleDrawer(true));
  $("btn-logs").addEventListener("click", () => toggleDrawer(!drawerOpen));
  $("log-close").addEventListener("click", () => toggleDrawer(false));
  $("log-copy").addEventListener("click", () => navigator.clipboard.writeText($("log").textContent ?? "").then(() => toast("Copied")));
  $("notice-close").addEventListener("click", () => { noticeClosed = $("notice-text").textContent ?? ""; render(); });
  $("btn-quit").addEventListener("click", () => ($("ask-quit").hidden = false));
  $("ask-quit-no").addEventListener("click", () => ($("ask-quit").hidden = true));
  $("ask-quit-yes").addEventListener("click", () => api.quit());

  $("s-costs-save").addEventListener("click", async () => {
    const v = (id: string) => { const x = $<HTMLInputElement>(id).value; return x === "" ? null : Number(x); };
    try {
      const rate = v("s-rate");
      if (rate !== null) await api.setTariff({ flat: rate });
      await api.setCosts({ wall_watts: v("s-wall"), price_per_ign: v("s-price"), other_per_day: v("s-other"), currency: $<HTMLInputElement>("s-ccy").value.toUpperCase(), host_allowance_w: null });
      $("s-costs-msg").textContent = "Saved.";
    } catch (e) { $("s-costs-msg").textContent = String(e); }
  });
  $("s-sched-save").addEventListener("click", async () => {
    const c = (id: string) => $<HTMLInputElement>(id).checked;
    try {
      await api.setSchedule({ time_of_use: { enabled: c("s-tou"), pause_above_rate: Number($<HTMLInputElement>("s-tou-rate").value || 0), resume_by_itself: c("s-tou-resume") }, gaming_pause: { enabled: c("s-game"), resume_by_itself: c("s-game-resume"), resume_after_s: 120 }, pause_on_battery: c("s-battery"), resume_after_sleep: c("s-sleep") });
      $("s-sched-msg").textContent = "Saved.";
    } catch (e) { $("s-sched-msg").textContent = String(e); }
  });
  $("s-address-save").addEventListener("click", async () => {
    try { const a = await api.setPayoutAddress($<HTMLInputElement>("s-address").value.trim()); $<HTMLInputElement>("s-address").value = a; $("s-address-err").hidden = true; toast("Address changed"); }
    catch (e) { $("s-address-err").textContent = String(e); $("s-address-err").hidden = false; }
  });
  $("s-auto-update").addEventListener("change", () => api.setUpdatePolicy($<HTMLInputElement>("s-auto-update").checked ? "auto" : "ask").catch((e) => toast(String(e))));
  document.querySelectorAll<HTMLButtonElement>("#theme-seg .seg-b").forEach((b) => b.addEventListener("click", () => { const t = b.dataset.theme!; applyTheme(t); api.setPreferences({ theme: t }).catch((e) => toast(String(e))); document.querySelectorAll<HTMLButtonElement>("#theme-seg .seg-b").forEach((x) => x.classList.toggle("on", x === b)); }));
  $("s-journal").addEventListener("click", async () => { const text = await api.journalExport(); await navigator.clipboard.writeText(text); toast("Journal copied"); });
  $("s-setup-again").addEventListener("click", () => { settingsFilled = false; setPhase("welcome"); });
}

function applyTheme(t: string) {
  if (t === "light" || t === "dark") document.documentElement.dataset.theme = t; else delete document.documentElement.dataset.theme;
}

function toggleDrawer(open: boolean) {
  drawerOpen = open;
  body.classList.toggle("drawer-open", open);
  $("btn-logs").setAttribute("aria-expanded", String(open));
  if (open) refreshLog();
}

async function boot() {
  wire();
  if (navigator.platform.toLowerCase().includes("mac")) body.classList.add("mac");
  try { snap = await api.getState(); } catch (e) { toast("The engine did not answer: " + String(e)); }
  if (snap) {
    applyTheme(snap.settings.theme);
    if (snap.settings.setup_done) { setPhase("dashboard"); setPage("mine"); } else setPhase("welcome");
    $("welcome-eyebrow").textContent = snap.stub ? "stub worker: nothing is mined" : "nothing is bought or sold";
  } else setPhase("welcome");
  render();
  await api.onState((s) => { snap = s; render(); });
}

boot();
