// adlx_probe.h: AMD's ADLX bound at run time on Windows (amdadlx64.dll, the driver's own
// library; nothing shipped), read through the C bindings vendored by manifest under
// pow/third_party/adlx-1ca2dd42. Board power, clocks, temperature and fan for the telemetry
// line, telemetry grade like the NVML rows. Absent library or GPU: every call reports 0 and
// the row says "no power reading" as before. GPU engineer B, 10 October 2026.
#pragma once
#ifdef __cplusplus
extern "C" {
#endif
typedef struct igm_adlx_sample {
    double power_w;       /* board power when the driver supports it, else GPU power */
    int power_kind;       /* 0 none, 1 GPU (ASIC) power, 2 total board power */
    int core_mhz, mem_mhz, fan_rpm, util_pct;
    double temp_c, hotspot_c;
} igm_adlx_sample_t;
/* Binds the library once and selects the first ADLX GPU whose name contains name_substr
   (case-insensitive; empty: the first GPU). Returns 1 when a GPU is selected. */
int igm_adlx_open(const char *name_substr);
int igm_adlx_sample(igm_adlx_sample_t *out);
const char *igm_adlx_name(void);     /* the selected GPU's ADLX name, "" when none */
const char *igm_adlx_state(void);    /* "ok", "no library", "no gpu", "init failed", "untried" */
void igm_adlx_close(void);
/* The manual power limit (ADLX IADLXManualPowerTuning: a percent offset from the stock limit, range from the
   driver). igm_adlx_power_limit_range: 1 and the range when supported. igm_adlx_power_limit_set(pct_of_stock):
   sets offset = pct - 100 clamped to the range and never below -50 (50 percent of stock); returns 1 and records
   the previous value for restore; the restore runs on igm_adlx_power_limit_restore (and the worker calls it from
   every exit path). igm_adlx_power_limit_get returns the current offset read back from the driver. */
int igm_adlx_power_limit_range(int *min_out, int *max_out, int *step_out);
int igm_adlx_power_limit_get(int *offset_out);
int igm_adlx_power_limit_set(int pct_of_stock, int *applied_offset_out, const char **why);
void igm_adlx_power_limit_restore(void);
/* The manual clock ceilings (Breakwater phase C, the project's order 10 Oct 2026 16:1x UK): the GPU's maximum core frequency
   (ADLX IADLXManualGraphicsTuning2 SetGPUMaxFrequency) and maximum memory frequency (IADLXManualVRAMTuning2
   SetMaxVRAMFrequency) set to a percent of the stock maximum read from the driver, each within the driver's range and
   step; the stock values are saved on the first set and restored by igm_adlx_clocks_restore (the worker calls it from
   every exit path, beside the power limit). *_get reads the current ceiling in MHz; 0 = not supported or not permitted. */
int igm_adlx_core_max_get(int *mhz_out, int *stock_mhz_out);
int igm_adlx_core_max_set(int pct_of_stock, int *applied_mhz_out, const char **why);
int igm_adlx_mem_max_get(int *mhz_out, int *stock_mhz_out);
int igm_adlx_mem_max_set(int pct_of_stock, int *applied_mhz_out, const char **why);
/* the signed-offset form for a driver whose maximum is an offset around the stock boost (RDNA4): a negative MHz only */
int igm_adlx_core_offset_set(int offset_mhz, int *applied_out, const char **why);
void igm_adlx_clocks_restore(void);
/* The factory reset of the GPU's manual tuning (ADLX IADLXGPUTuningServices ResetToFactory): every clock, voltage and power
   offset back to the driver's defaults; the clock-restore command (10 Oct 2026 19:4x UK: a maximum set from the driver's range
   hung the owned bench PC). igm_adlx_clock_probe prints the manual tuning's ranges and current values and the live clocks, read-only. */
int igm_adlx_reset_to_factory(const char **why);
int igm_adlx_clock_probe(char *buf, unsigned long n);
/* The selected GPU's PCIe bus type and lane width from ADLX IADLXGPU1 (0 when not exposed). */
int igm_adlx_pcie(int *bus_type_out, int *lane_width_out);
/* The selected GPU's ADLX identity fields, read-only (the Windows AMD intrinsic-identity candidate, the project's order of 11 Oct 2026 23:03 UK):
   UniqueId (adlx_int), VendorId, DeviceId, SubSystemId, RevisionId (strings), Type (0 undefined, 1 integrated, 2 discrete), IsExternal.
   Returns 1 and the json object text in buf when the GPU is selected. */
int igm_adlx_identity(char *buf, unsigned long n);
/* Whether the selected GPU drives a desktop (ADLX HasDesktops): 1 yes, 0 no, -1 not known. */
int igm_adlx_has_desktops(void);
/* The host CPU name (Windows: the registry's ProcessorNameString; else ""). */
const char *igm_host_cpu_name(void);
#ifdef __cplusplus
}
#endif
