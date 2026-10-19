/* adlx_probe.c: see adlx_probe.h. C11, compiled on the Windows cross-build with the ADLX
   C bindings on the include path; a stub everywhere else. */
#include "adlx_probe.h"
#include <string.h>
#include <stdio.h>
#include <ctype.h>
#ifdef _WIN32
#include <windows.h>
#include "ADLX.h"
#include "ISystem.h"
#include "IPerformanceMonitoring.h"
#include "IGPUTuning.h"
#include "IGPUManualPowerTuning.h"
#include "IGPUManualGFXTuning.h"
#include "IGPUManualVRAMTuning.h"
#include "ISystem1.h"

static HMODULE g_dll;
static IADLXSystem *g_sys;
static IADLXPerformanceMonitoringServices *g_perf;
static IADLXGPU *g_gpu;
static ADLXTerminate_Fn g_term;
static int g_state;   /* 0 untried, 1 ok, -1 failed */
static const char *g_why = "untried";
static char g_name[128];
static adlx_bool g_board_power, g_gpu_power, g_fan;

static int has_ci(const char *hay, const char *needle) {
    size_t n = strlen(needle);
    if (n == 0) return 1;
    for (const char *p = hay; *p; p++) {
        size_t i = 0;
        while (i < n && p[i] && tolower((unsigned char)p[i]) == tolower((unsigned char)needle[i])) i++;
        if (i == n) return 1;
    }
    return 0;
}

int igm_adlx_open(const char *name_substr) {
    if (g_state) return g_state > 0;
    g_state = -1;
    g_dll = LoadLibraryA("amdadlx64.dll");
    if (!g_dll) { g_why = "no library"; return 0; }
    ADLXInitialize_Fn init = (ADLXInitialize_Fn)GetProcAddress(g_dll, ADLX_INIT_FUNCTION_NAME);
    g_term = (ADLXTerminate_Fn)GetProcAddress(g_dll, ADLX_TERMINATE_FUNCTION_NAME);
    if (!init || !g_term) { g_why = "init failed"; return 0; }
    if (ADLX_FAILED(init(ADLX_FULL_VERSION, &g_sys)) || !g_sys) { g_why = "init failed"; return 0; }
    IADLXGPUList *gpus = NULL;
    if (ADLX_FAILED(g_sys->pVtbl->GetGPUs(g_sys, &gpus)) || !gpus) { g_why = "no gpu"; return 0; }
    adlx_uint n = gpus->pVtbl->Size(gpus);
    for (adlx_uint i = 0; i < n && !g_gpu; i++) {
        IADLXGPU *g = NULL;
        if (ADLX_FAILED(gpus->pVtbl->At_GPUList(gpus, i, &g)) || !g) continue;
        const char *nm = NULL;
        g->pVtbl->Name(g, &nm);
        if (nm && has_ci(nm, name_substr ? name_substr : "")) { g_gpu = g; snprintf(g_name, sizeof g_name, "%s", nm); }
        else g->pVtbl->Release(g);
    }
    gpus->pVtbl->Release(gpus);
    if (!g_gpu) { g_why = "no gpu"; return 0; }
    if (ADLX_FAILED(g_sys->pVtbl->GetPerformanceMonitoringServices(g_sys, &g_perf)) || !g_perf) { g_why = "init failed"; return 0; }
    IADLXGPUMetricsSupport *sup = NULL;
    if (ADLX_SUCCEEDED(g_perf->pVtbl->GetSupportedGPUMetrics(g_perf, g_gpu, &sup)) && sup) {
        sup->pVtbl->IsSupportedGPUTotalBoardPower(sup, &g_board_power);
        sup->pVtbl->IsSupportedGPUPower(sup, &g_gpu_power);
        sup->pVtbl->IsSupportedGPUFanSpeed(sup, &g_fan);
        sup->pVtbl->Release(sup);
    }
    g_state = 1; g_why = "ok";
    return 1;
}

int igm_adlx_sample(igm_adlx_sample_t *o) {
    memset(o, 0, sizeof *o);
    if (g_state <= 0) return 0;
    IADLXGPUMetrics *m = NULL;
    if (ADLX_FAILED(g_perf->pVtbl->GetCurrentGPUMetrics(g_perf, g_gpu, &m)) || !m) return 0;
    adlx_double d = 0; adlx_int v = 0;
    if (g_board_power && ADLX_SUCCEEDED(m->pVtbl->GPUTotalBoardPower(m, &d))) { o->power_w = d; o->power_kind = 2; }
    else if (g_gpu_power && ADLX_SUCCEEDED(m->pVtbl->GPUPower(m, &d))) { o->power_w = d; o->power_kind = 1; }
    if (ADLX_SUCCEEDED(m->pVtbl->GPUClockSpeed(m, &v))) o->core_mhz = v;
    if (ADLX_SUCCEEDED(m->pVtbl->GPUVRAMClockSpeed(m, &v))) o->mem_mhz = v;
    if (ADLX_SUCCEEDED(m->pVtbl->GPUTemperature(m, &d))) o->temp_c = d;
    if (ADLX_SUCCEEDED(m->pVtbl->GPUHotspotTemperature(m, &d))) o->hotspot_c = d;
    if (g_fan && ADLX_SUCCEEDED(m->pVtbl->GPUFanSpeed(m, &v))) o->fan_rpm = v;
    if (ADLX_SUCCEEDED(m->pVtbl->GPUUsage(m, &d))) o->util_pct = (int)(d + 0.5);
    m->pVtbl->Release(m);
    return 1;
}

static IADLXManualPowerTuning *g_pt;
static int g_pt_saved = 0, g_pt_saved_val = 0;
static int pt_open(void) {
    if (g_pt) return 1;
    if (g_state <= 0 || !g_sys || !g_gpu) return 0;
    IADLXGPUTuningServices *ts = NULL;
    if (ADLX_FAILED(g_sys->pVtbl->GetGPUTuningServices(g_sys, &ts)) || !ts) return 0;
    adlx_bool sup = 0;
    if (ADLX_FAILED(ts->pVtbl->IsSupportedManualPowerTuning(ts, g_gpu, &sup)) || !sup) { ts->pVtbl->Release(ts); return 0; }
    IADLXInterface *iface = NULL;
    if (ADLX_FAILED(ts->pVtbl->GetManualPowerTuning(ts, g_gpu, &iface)) || !iface) { ts->pVtbl->Release(ts); return 0; }
    if (ADLX_FAILED(iface->pVtbl->QueryInterface(iface, IID_IADLXManualPowerTuning(), (void **)&g_pt)) || !g_pt) { g_pt = NULL; }
    iface->pVtbl->Release(iface); ts->pVtbl->Release(ts);
    return g_pt != NULL;
}
int igm_adlx_power_limit_range(int *mn, int *mx, int *st) {
    if (!pt_open()) return 0;
    ADLX_IntRange r; if (ADLX_FAILED(g_pt->pVtbl->GetPowerLimitRange(g_pt, &r))) return 0;
    *mn = r.minValue; *mx = r.maxValue; *st = r.step; return 1;
}
int igm_adlx_power_limit_get(int *off) {
    if (!pt_open()) return 0;
    adlx_int v = 0; if (ADLX_FAILED(g_pt->pVtbl->GetPowerLimit(g_pt, &v))) return 0;
    *off = v; return 1;
}
int igm_adlx_power_limit_set(int pct, int *applied, const char **why) {
    *why = "";
    if (pct == 100) {   /* an explicit return to stock (power-restore): set the offset 0 and forget any saved offset, so the exit hook leaves stock in place (10 Oct 2026 19:2x UK: the hook put a cancelled run's -20 back after every restore, and every "stock" row since ran at cap 80) */
        if (!pt_open()) { *why = "manual power tuning not supported or not permitted for this user"; return 0; }
        if (ADLX_FAILED(g_pt->pVtbl->SetPowerLimit(g_pt, 0))) { *why = "the driver refused the stock offset"; return 0; }
        adlx_int back = 0; g_pt->pVtbl->GetPowerLimit(g_pt, &back); *applied = back; g_pt_saved = 0; return 1;
    }
    if (!pt_open()) { *why = "manual power tuning not supported or not permitted for this user"; return 0; }
    if (pct > 100) { *why = "above the stock limit is not run"; return 0; }
    if (pct < 50) { *why = "below 50 percent of stock is never run"; return 0; }
    ADLX_IntRange r; if (ADLX_FAILED(g_pt->pVtbl->GetPowerLimitRange(g_pt, &r))) { *why = "no range from the driver"; return 0; }
    int want = pct - 100;
    if (want < r.minValue) { *why = "below the driver's supported range"; return 0; }
    if (want > r.maxValue) want = r.maxValue;
    if (r.step > 1) want = (want / r.step) * r.step;
    adlx_int cur = 0; if (ADLX_FAILED(g_pt->pVtbl->GetPowerLimit(g_pt, &cur))) { *why = "no current value from the driver"; return 0; }
    if (!g_pt_saved) { g_pt_saved = 1; g_pt_saved_val = cur; }
    if (ADLX_FAILED(g_pt->pVtbl->SetPowerLimit(g_pt, want))) { *why = "the driver refused the setting"; return 0; }
    adlx_int back = 0; g_pt->pVtbl->GetPowerLimit(g_pt, &back);
    *applied = back; return 1;
}

/* ---- the clock ceilings (Breakwater phase C) ---- */
static IADLXManualGraphicsTuning2 *g_gt; static IADLXManualVRAMTuning2 *g_vt;
static int g_gt_saved = 0, g_gt_saved_mhz = 0, g_vt_saved = 0, g_vt_saved_mhz = 0;
static int gt_open(void) {
    if (g_gt) return 1;
    if (g_state <= 0 || !g_sys || !g_gpu) return 0;
    IADLXGPUTuningServices *ts = NULL;
    if (ADLX_FAILED(g_sys->pVtbl->GetGPUTuningServices(g_sys, &ts)) || !ts) return 0;
    adlx_bool sup = 0;
    if (ADLX_FAILED(ts->pVtbl->IsSupportedManualGFXTuning(ts, g_gpu, &sup)) || !sup) { ts->pVtbl->Release(ts); return 0; }
    IADLXInterface *iface = NULL;
    if (ADLX_FAILED(ts->pVtbl->GetManualGFXTuning(ts, g_gpu, &iface)) || !iface) { ts->pVtbl->Release(ts); return 0; }
    if (ADLX_FAILED(iface->pVtbl->QueryInterface(iface, IID_IADLXManualGraphicsTuning2(), (void **)&g_gt)) || !g_gt) g_gt = NULL;
    iface->pVtbl->Release(iface); ts->pVtbl->Release(ts);
    return g_gt != NULL;
}
static int vt_open(void) {
    if (g_vt) return 1;
    if (g_state <= 0 || !g_sys || !g_gpu) return 0;
    IADLXGPUTuningServices *ts = NULL;
    if (ADLX_FAILED(g_sys->pVtbl->GetGPUTuningServices(g_sys, &ts)) || !ts) return 0;
    adlx_bool sup = 0;
    if (ADLX_FAILED(ts->pVtbl->IsSupportedManualVRAMTuning(ts, g_gpu, &sup)) || !sup) { ts->pVtbl->Release(ts); return 0; }
    IADLXInterface *iface = NULL;
    if (ADLX_FAILED(ts->pVtbl->GetManualVRAMTuning(ts, g_gpu, &iface)) || !iface) { ts->pVtbl->Release(ts); return 0; }
    if (ADLX_FAILED(iface->pVtbl->QueryInterface(iface, IID_IADLXManualVRAMTuning2(), (void **)&g_vt)) || !g_vt) g_vt = NULL;
    iface->pVtbl->Release(iface); ts->pVtbl->Release(ts);
    return g_vt != NULL;
}
int igm_adlx_core_max_get(int *mhz, int *stock) {
    if (!gt_open()) return 0;
    adlx_int v = 0; if (ADLX_FAILED(g_gt->pVtbl->GetGPUMaxFrequency(g_gt, &v))) return 0;
    *mhz = v; *stock = g_gt_saved ? g_gt_saved_mhz : v; return 1;
}
int igm_adlx_mem_max_get(int *mhz, int *stock) {
    if (!vt_open()) return 0;
    adlx_int v = 0; if (ADLX_FAILED(g_vt->pVtbl->GetMaxVRAMFrequency(g_vt, &v))) return 0;
    *mhz = v; *stock = g_vt_saved ? g_vt_saved_mhz : v; return 1;
}
/* pct of the stock maximum, floored to the driver's step and clamped to its range; above stock is never run */
int igm_adlx_core_max_set(int pct, int *applied, const char **why) {
    *why = "";
    if (!gt_open()) { *why = "manual graphics tuning not supported or not permitted for this user"; return 0; }
    if (pct > 100) { *why = "above the stock maximum is not run"; return 0; }
    if (pct < 40) { *why = "below 40 percent of stock is never run"; return 0; }
    ADLX_IntRange r; if (ADLX_FAILED(g_gt->pVtbl->GetGPUMaxFrequencyRange(g_gt, &r))) { *why = "no range from the driver"; return 0; }
    adlx_int cur = 0; if (ADLX_FAILED(g_gt->pVtbl->GetGPUMaxFrequency(g_gt, &cur))) { *why = "no current value from the driver"; return 0; }
    if (cur <= 0) { *why = "the driver reports no current maximum (manual tuning at its default): the stock is unknown and no set is made (the range's maximum is not the stock: 1000 MHz set at the 100 percent point hung the owned bench PC, 10 Oct 2026 19:38 UK)"; return 0; }
    if (cur <= 0) { *why = "the driver reports no stock maximum"; return 0; }
    if (!g_gt_saved) { g_gt_saved = 1; g_gt_saved_mhz = cur; }
    int want = (int)((long)g_gt_saved_mhz * pct / 100);
    if (r.step > 1) want = (want / r.step) * r.step;
    if (want < r.minValue) { *why = "below the driver's supported range"; return 0; }
    if (want > r.maxValue) want = r.maxValue;
    if (ADLX_FAILED(g_gt->pVtbl->SetGPUMaxFrequency(g_gt, want))) { *why = "the driver refused the setting"; return 0; }
    adlx_int back = 0; g_gt->pVtbl->GetGPUMaxFrequency(g_gt, &back);
    if (back <= 0) { *why = "the driver read back no maximum after the set"; return 0; }
    *applied = back; return 1;
}
/* the offset form (RDNA4: the driver's "GPU maximum" is a signed offset in MHz around the stock boost, 0 = stock; the 9070 XT's
   range reads [-500, +1000], probe 10 Oct 2026 19:47 UK): only a negative offset is run, only when the range is signed */
int igm_adlx_core_offset_set(int mhz, int *applied, const char **why) {
    *why = "";
    if (!gt_open()) { *why = "manual graphics tuning not supported or not permitted for this user"; return 0; }
    if (mhz >= 0) { *why = "only a negative offset (an underclock) is run"; return 0; }
    ADLX_IntRange r; if (ADLX_FAILED(g_gt->pVtbl->GetGPUMaxFrequencyRange(g_gt, &r))) { *why = "no range from the driver"; return 0; }
    if (r.minValue >= 0) { *why = "the driver's range is not a signed offset (this card exposes an absolute maximum: use the percent form)"; return 0; }
    if (mhz < r.minValue) { *why = "below the driver's supported offset range"; return 0; }
    adlx_int cur = 0; if (ADLX_FAILED(g_gt->pVtbl->GetGPUMaxFrequency(g_gt, &cur))) { *why = "no current value from the driver"; return 0; }
    if (!g_gt_saved) { g_gt_saved = 1; g_gt_saved_mhz = cur; }
    int want = mhz; if (r.step > 1) want = (want / r.step) * r.step;
    if (ADLX_FAILED(g_gt->pVtbl->SetGPUMaxFrequency(g_gt, want))) { *why = "the driver refused the setting"; return 0; }
    adlx_int back = 0; g_gt->pVtbl->GetGPUMaxFrequency(g_gt, &back);
    if (back != want) { *why = "the driver read back a different offset after the set"; g_gt->pVtbl->SetGPUMaxFrequency(g_gt, g_gt_saved_mhz); return 0; }
    *applied = back; return 1;
}
int igm_adlx_mem_max_set(int pct, int *applied, const char **why) {
    *why = "";
    if (!vt_open()) { *why = "manual memory tuning not supported or not permitted for this user"; return 0; }
    if (pct > 100) { *why = "above the stock maximum is not run"; return 0; }
    if (pct < 50) { *why = "below 50 percent of stock is never run"; return 0; }
    ADLX_IntRange r; if (ADLX_FAILED(g_vt->pVtbl->GetMaxVRAMFrequencyRange(g_vt, &r))) { *why = "no range from the driver"; return 0; }
    adlx_int cur = 0; if (ADLX_FAILED(g_vt->pVtbl->GetMaxVRAMFrequency(g_vt, &cur))) { *why = "no current value from the driver"; return 0; }
    if (cur <= 0) { *why = "the driver reports no current maximum (manual tuning at its default): the stock is unknown and no set is made (the range's maximum is not the stock: 1000 MHz set at the 100 percent point hung the owned bench PC, 10 Oct 2026 19:38 UK)"; return 0; }
    if (cur <= 0) { *why = "the driver reports no stock maximum"; return 0; }
    if (!g_vt_saved) { g_vt_saved = 1; g_vt_saved_mhz = cur; }
    int want = (int)((long)g_vt_saved_mhz * pct / 100);
    if (r.step > 1) want = (want / r.step) * r.step;
    if (want < r.minValue) { *why = "below the driver's supported range"; return 0; }
    if (want > r.maxValue) want = r.maxValue;
    if (ADLX_FAILED(g_vt->pVtbl->SetMaxVRAMFrequency(g_vt, want))) { *why = "the driver refused the setting"; return 0; }
    adlx_int back = 0; g_vt->pVtbl->GetMaxVRAMFrequency(g_vt, &back);
    if (back <= 0) { *why = "the driver read back no maximum after the set"; return 0; }
    *applied = back; return 1;
}

int igm_adlx_reset_to_factory(const char **why) {
    *why = "";
    if (g_state <= 0 || !g_sys || !g_gpu) { *why = "no ADLX"; return 0; }
    IADLXGPUTuningServices *ts = NULL;
    if (ADLX_FAILED(g_sys->pVtbl->GetGPUTuningServices(g_sys, &ts)) || !ts) { *why = "no tuning services"; return 0; }
    ADLX_RESULT r = ts->pVtbl->ResetToFactory(ts, g_gpu); ts->pVtbl->Release(ts);
    if (ADLX_FAILED(r)) { *why = "the driver refused the factory reset"; return 0; }
    g_pt_saved = 0; g_gt_saved = 0; g_vt_saved = 0;
    return 1;
}
int igm_adlx_clock_probe(char *buf, unsigned long n) {
    if (g_state <= 0 || !g_sys || !g_gpu) return 0;
    int gmn = -1, gmx = -1, gst = -1, gcur = -1, gmin_cur = -1, vmn = -1, vmx = -1, vst = -1, vcur = -1, core = -1, mem = -1;
    if (gt_open()) { ADLX_IntRange r; if (ADLX_SUCCEEDED(g_gt->pVtbl->GetGPUMaxFrequencyRange(g_gt, &r))) { gmn = r.minValue; gmx = r.maxValue; gst = r.step; } adlx_int v = 0; if (ADLX_SUCCEEDED(g_gt->pVtbl->GetGPUMaxFrequency(g_gt, &v))) gcur = v; v = 0; if (ADLX_SUCCEEDED(g_gt->pVtbl->GetGPUMinFrequency(g_gt, &v))) gmin_cur = v; }
    if (vt_open()) { ADLX_IntRange r; if (ADLX_SUCCEEDED(g_vt->pVtbl->GetMaxVRAMFrequencyRange(g_vt, &r))) { vmn = r.minValue; vmx = r.maxValue; vst = r.step; } adlx_int v = 0; if (ADLX_SUCCEEDED(g_vt->pVtbl->GetMaxVRAMFrequency(g_vt, &v))) vcur = v; }
    { igm_adlx_sample_t s; memset(&s, 0, sizeof s); if (igm_adlx_sample(&s)) { core = s.core_mhz; mem = s.mem_mhz; } }
    snprintf(buf, n, "{\"clock_probe\": {\"core_max_range\": [%d, %d, %d], \"core_max_current\": %d, \"core_min_current\": %d, \"mem_max_range\": [%d, %d, %d], \"mem_max_current\": %d, \"live_core_mhz\": %d, \"live_mem_mhz\": %d, \"note\": \"read-only; -1 = not exposed; the range maximum is not the stock maximum on RDNA4 (1000 at the 9070 XT's 100 percent point hung the owned bench PC, 10 Oct 2026)\"}}", gmn, gmx, gst, gcur, gmin_cur, vmn, vmx, vst, vcur, core, mem);
    return 1;
}
void igm_adlx_clocks_restore(void) {
    if (g_gt && g_gt_saved) { g_gt->pVtbl->SetGPUMaxFrequency(g_gt, g_gt_saved_mhz); adlx_int b = 0; g_gt->pVtbl->GetGPUMaxFrequency(g_gt, &b); fprintf(stderr, "adlx: core maximum restored to %d MHz (read back %d)\n", g_gt_saved_mhz, (int)b); g_gt_saved = 0; }
    if (g_vt && g_vt_saved) { g_vt->pVtbl->SetMaxVRAMFrequency(g_vt, g_vt_saved_mhz); adlx_int b = 0; g_vt->pVtbl->GetMaxVRAMFrequency(g_vt, &b); fprintf(stderr, "adlx: memory maximum restored to %d MHz (read back %d)\n", g_vt_saved_mhz, (int)b); g_vt_saved = 0; }
}
void igm_adlx_power_limit_restore(void) {
    if (!g_pt || !g_pt_saved) return;
    g_pt->pVtbl->SetPowerLimit(g_pt, g_pt_saved_val);
    adlx_int back = 0; g_pt->pVtbl->GetPowerLimit(g_pt, &back);
    fprintf(stderr, "adlx: power limit restored to %d (read back %d)\n", g_pt_saved_val, (int)back);
    g_pt_saved = 0;
}

int igm_adlx_pcie(int *bt, int *lw) {
    *bt = 0; *lw = 0;
    if (g_state <= 0 || !g_gpu) return 0;
    IADLXGPU1 *g1 = NULL;
    if (ADLX_FAILED(g_gpu->pVtbl->QueryInterface(g_gpu, IID_IADLXGPU1(), (void **)&g1)) || !g1) return 0;
    ADLX_PCI_BUS_TYPE t = UNDEFINED; adlx_uint w = 0;
    g1->pVtbl->PCIBusType(g1, &t); g1->pVtbl->PCIBusLaneWidth(g1, &w);
    g1->pVtbl->Release(g1);
    *bt = (int)t; *lw = (int)w; return 1;
}
int igm_adlx_identity(char *buf, unsigned long n) {
    if (g_state <= 0 || !g_gpu) return 0;
    adlx_int uid = -1; const char *vid = "", *did = "", *ssid = "", *rid = ""; ADLX_GPU_TYPE ty = GPUTYPE_UNDEFINED; adlx_bool ext = 0;
    g_gpu->pVtbl->UniqueId(g_gpu, &uid); g_gpu->pVtbl->VendorId(g_gpu, &vid); g_gpu->pVtbl->DeviceId(g_gpu, &did);
    g_gpu->pVtbl->SubSystemId(g_gpu, &ssid); g_gpu->pVtbl->RevisionId(g_gpu, &rid); g_gpu->pVtbl->Type(g_gpu, &ty); g_gpu->pVtbl->IsExternal(g_gpu, &ext);
    snprintf(buf, n, "{\"unique_id\": %d, \"unique_id_hex\": \"%x\", \"vendor_id\": \"%s\", \"device_id\": \"%s\", \"subsystem_id\": \"%s\", \"revision_id\": \"%s\", \"type\": %d, \"external\": %s, \"name\": \"%s\"}",
             (int)uid, (unsigned)uid, vid ? vid : "", did ? did : "", ssid ? ssid : "", rid ? rid : "", (int)ty, ext ? "true" : "false", g_name);
    return 1;
}
int igm_adlx_has_desktops(void) { if (g_state <= 0 || !g_gpu) return -1; adlx_bool h = 0; if (ADLX_FAILED(g_gpu->pVtbl->HasDesktops(g_gpu, &h))) return -1; return h ? 1 : 0; }
static char g_cpu[256];
const char *igm_host_cpu_name(void) {
    if (g_cpu[0]) return g_cpu;
    HKEY k; DWORD n = sizeof g_cpu - 1;
    if (RegOpenKeyExA(HKEY_LOCAL_MACHINE, "HARDWARE\\DESCRIPTION\\System\\CentralProcessor\\0", 0, KEY_READ, &k) == ERROR_SUCCESS) {
        if (RegQueryValueExA(k, "ProcessorNameString", NULL, NULL, (LPBYTE)g_cpu, &n) != ERROR_SUCCESS) g_cpu[0] = 0;
        RegCloseKey(k);
    }
    return g_cpu;
}
const char *igm_adlx_name(void) { return g_name; }
const char *igm_adlx_state(void) { return g_why; }
void igm_adlx_close(void) {
    igm_adlx_power_limit_restore(); igm_adlx_clocks_restore();
    if (g_gt) { g_gt->pVtbl->Release(g_gt); g_gt = NULL; } if (g_vt) { g_vt->pVtbl->Release(g_vt); g_vt = NULL; }
    if (g_pt) { g_pt->pVtbl->Release(g_pt); g_pt = NULL; }
    if (g_gpu) { g_gpu->pVtbl->Release(g_gpu); g_gpu = NULL; }
    if (g_perf) { g_perf->pVtbl->Release(g_perf); g_perf = NULL; }
    if (g_term) g_term();
    if (g_dll) FreeLibrary(g_dll);
    g_dll = NULL; g_sys = NULL; g_term = NULL; g_state = 0; g_why = "untried";
}
#else
int igm_adlx_pcie(int *a, int *b) { *a = 0; *b = 0; return 0; }
int igm_adlx_has_desktops(void) { return -1; }
int igm_adlx_identity(char *b, unsigned long n) { (void)b; (void)n; return 0; }
const char *igm_host_cpu_name(void) { return ""; }
int igm_adlx_power_limit_range(int *a, int *b, int *c) { (void)a; (void)b; (void)c; return 0; }
int igm_adlx_power_limit_get(int *o) { (void)o; return 0; }
int igm_adlx_power_limit_set(int p, int *a, const char **w) { (void)p; (void)a; *w = "no library"; return 0; }
void igm_adlx_power_limit_restore(void) {}
int igm_adlx_core_max_get(int *m, int *s) { (void)m; (void)s; return 0; }
int igm_adlx_core_max_set(int p, int *a, const char **w) { (void)p; (void)a; *w = "no library"; return 0; }
int igm_adlx_mem_max_get(int *m, int *s) { (void)m; (void)s; return 0; }
int igm_adlx_mem_max_set(int p, int *a, const char **w) { (void)p; (void)a; *w = "no library"; return 0; }
int igm_adlx_core_offset_set(int m, int *a, const char **w) { (void)m; (void)a; *w = "no library"; return 0; }
void igm_adlx_clocks_restore(void) {}
int igm_adlx_reset_to_factory(const char **w) { *w = "no library"; return 0; }
int igm_adlx_clock_probe(char *b, unsigned long n) { (void)b; (void)n; return 0; }
int igm_adlx_open(const char *s) { (void)s; return 0; }
int igm_adlx_sample(igm_adlx_sample_t *o) { memset(o, 0, sizeof *o); return 0; }
const char *igm_adlx_name(void) { return ""; }
const char *igm_adlx_state(void) { return "no library"; }
void igm_adlx_close(void) {}
#endif
