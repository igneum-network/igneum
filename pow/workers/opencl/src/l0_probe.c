/* l0_probe.c: see l0_probe.h. C11; the Level Zero loader bound at run time on Windows (ze_loader.dll) and Linux
   (libze_loader.so.1), the entry points and the few structures declared here from the public Level Zero specification
   (version 1.x; intel/compute-runtime, Apache 2.0 with LLVM exception). Nothing is vendored. GPU engineer B, 11 October 2026. */
#include "l0_probe.h"
#include <stdint.h>
#include <string.h>
#include <stdio.h>
#include <stdlib.h>
#include <ctype.h>
#ifdef _WIN32
#include <windows.h>
#define L0_LIB "ze_loader.dll"
static HMODULE g_lib;
#define L0_SYM(n) ((void *)GetProcAddress(g_lib, n))
#else
#include <dlfcn.h>
#define L0_LIB "libze_loader.so.1"
static void *g_lib;
#define L0_SYM(n) dlsym(g_lib, n)
#endif
#ifndef _WIN32
#define L0_CALL
#else
#define L0_CALL __cdecl
#endif

typedef int32_t ze_result_t;      /* ZE_RESULT_SUCCESS == 0 */
typedef void *zes_driver_handle_t, *zes_device_handle_t, *zes_pwr_handle_t, *zes_freq_handle_t, *zes_temp_handle_t;
typedef uint8_t ze_bool_t;
typedef struct { uint8_t id[16]; } ze_device_uuid_t;
typedef struct {   /* ze_device_properties_t, specification 1.x */
    int32_t stype; void *pNext; int32_t type; uint32_t vendorId, deviceId, flags, subdeviceId, coreClockRate; uint64_t maxMemAllocSize;
    uint32_t maxHardwareContexts, maxCommandQueuePriority, numThreadsPerEU, physicalEUSimdWidth, numEUsPerSubslice, numSubslicesPerSlice, numSlices;
    uint64_t timerResolution; uint32_t timestampValidBits, kernelTimestampValidBits; ze_device_uuid_t uuid; char name[256];
} ze_device_properties_t;
typedef struct {   /* zes_device_properties_t */
    int32_t stype; void *pNext; ze_device_properties_t core; uint32_t numSubdevices; char serialNumber[64], boardNumber[64], brandName[64], modelName[64], vendorName[64], driverVersion[64];
} zes_device_properties_t;
typedef struct { uint64_t energy; uint64_t timestamp; } zes_power_energy_counter_t;   /* microjoules, microseconds */
typedef struct { int32_t stype; void *pNext; int32_t type; ze_bool_t onSubdevice; uint32_t subdeviceId; ze_bool_t canControl, isThrottleEventSupported; double min, max; } zes_freq_properties_t;   /* type 0 GPU, 1 memory */
typedef struct { int32_t stype; void *pNext; double currentVoltage, request, tdp, efficient, actual; uint32_t throttleReasons; } zes_freq_state_t;
enum { ZES_STRUCTURE_TYPE_DEVICE_PROPERTIES = 0x1, ZES_STRUCTURE_TYPE_FREQ_PROPERTIES = 0x9, ZES_STRUCTURE_TYPE_FREQ_STATE = 0x1b };

static ze_result_t (L0_CALL *p_zesInit)(uint32_t);
static ze_result_t (L0_CALL *p_zeInit)(uint32_t);
static ze_result_t (L0_CALL *p_zesDriverGet)(uint32_t *, zes_driver_handle_t *);
static ze_result_t (L0_CALL *p_zesDeviceGet)(zes_driver_handle_t, uint32_t *, zes_device_handle_t *);
static ze_result_t (L0_CALL *p_zesDeviceGetProperties)(zes_device_handle_t, zes_device_properties_t *);
static ze_result_t (L0_CALL *p_zesDeviceGetCardPowerDomain)(zes_device_handle_t, zes_pwr_handle_t *);
static ze_result_t (L0_CALL *p_zesDeviceEnumPowerDomains)(zes_device_handle_t, uint32_t *, zes_pwr_handle_t *);
static ze_result_t (L0_CALL *p_zesPowerGetEnergyCounter)(zes_pwr_handle_t, zes_power_energy_counter_t *);
static ze_result_t (L0_CALL *p_zesDeviceEnumFrequencyDomains)(zes_device_handle_t, uint32_t *, zes_freq_handle_t *);
static ze_result_t (L0_CALL *p_zesFrequencyGetProperties)(zes_freq_handle_t, zes_freq_properties_t *);
static ze_result_t (L0_CALL *p_zesFrequencyGetState)(zes_freq_handle_t, zes_freq_state_t *);
static ze_result_t (L0_CALL *p_zesDeviceEnumTemperatureSensors)(zes_device_handle_t, uint32_t *, zes_temp_handle_t *);
static ze_result_t (L0_CALL *p_zesTemperatureGetState)(zes_temp_handle_t, double *);

static int g_state;   /* 0 untried, 1 ok, -1 failed */
static const char *g_why = "untried";
static char g_name[64];
static zes_device_handle_t g_dev;
static zes_pwr_handle_t g_pwr;
static int g_pwr_kind;
static zes_freq_handle_t g_freq_gpu, g_freq_mem;
static zes_temp_handle_t g_temp;
/* Every power domain the device exposes (the card domain first when the driver names one): the B580 on Windows driver 6733
   answered the card domain's counter without ever advancing it (probe-2, 11 Oct 2026), so each sample reads every domain and
   the first whose energy and timestamp both advanced is the row's; a domain that never advances is named static. */
#define L0_MAX_DOMAINS 9
static zes_pwr_handle_t g_doms[L0_MAX_DOMAINS];
static int g_dom_kind[L0_MAX_DOMAINS];   /* 2 card, 1 enumerated */
static int g_ndoms;
static zes_power_energy_counter_t g_last[L0_MAX_DOMAINS];
static int g_have_last[L0_MAX_DOMAINS];
static int g_samples;

static int has_ci(const char *hay, const char *needle) {
    size_t n = strlen(needle);
    if (n == 0) return 1;
    for (; *hay; hay++) { size_t i = 0; while (i < n && hay[i] && tolower((unsigned char)hay[i]) == tolower((unsigned char)needle[i])) i++; if (i == n) return 1; }
    return 0;
}

#define BIND(n) do { *(void **)&p_##n = L0_SYM(#n); } while (0)

int igm_l0_open(const char *name_substr) {
    if (g_state) return g_state > 0;
    g_state = -1;
#ifdef _WIN32
    _putenv("ZES_ENABLE_SYSMAN=1");
    g_lib = LoadLibraryA(L0_LIB);
#else
    setenv("ZES_ENABLE_SYSMAN", "1", 0);
    g_lib = dlopen(L0_LIB, RTLD_NOW);
#endif
    if (!g_lib) { g_why = "no loader"; return 0; }
    BIND(zesInit); BIND(zeInit); BIND(zesDriverGet); BIND(zesDeviceGet); BIND(zesDeviceGetProperties); BIND(zesDeviceGetCardPowerDomain);
    BIND(zesDeviceEnumPowerDomains); BIND(zesPowerGetEnergyCounter); BIND(zesDeviceEnumFrequencyDomains); BIND(zesFrequencyGetProperties);
    BIND(zesFrequencyGetState); BIND(zesDeviceEnumTemperatureSensors); BIND(zesTemperatureGetState);
    if (!p_zesDriverGet || !p_zesDeviceGet || !p_zesDeviceEnumPowerDomains || !p_zesPowerGetEnergyCounter) { g_why = "loader lacks sysman entry points"; return 0; }
    ze_result_t r = p_zesInit ? p_zesInit(0) : (p_zeInit ? p_zeInit(1u) : -1);   /* zesInit (sysman-only, 1.5+); else zeInit GPU_ONLY under ZES_ENABLE_SYSMAN */
    if (r != 0) { g_why = "init failed"; return 0; }
    uint32_t nd = 0;
    if (p_zesDriverGet(&nd, NULL) != 0 || nd == 0) { g_why = "no device"; return 0; }
    zes_driver_handle_t drivers[8]; if (nd > 8) nd = 8;
    if (p_zesDriverGet(&nd, drivers) != 0) { g_why = "no device"; return 0; }
    zes_device_handle_t first = NULL;
    for (uint32_t i = 0; i < nd && !g_dev; i++) {
        uint32_t n = 0;
        if (p_zesDeviceGet(drivers[i], &n, NULL) != 0 || n == 0) continue;
        zes_device_handle_t devs[16]; if (n > 16) n = 16;
        if (p_zesDeviceGet(drivers[i], &n, devs) != 0) continue;
        for (uint32_t j = 0; j < n; j++) {
            zes_device_properties_t pr; memset(&pr, 0, sizeof pr); pr.stype = ZES_STRUCTURE_TYPE_DEVICE_PROPERTIES; pr.core.stype = 0x3;
            const char *model = "";
            if (p_zesDeviceGetProperties && p_zesDeviceGetProperties(devs[j], &pr) == 0) model = pr.modelName[0] ? pr.modelName : pr.core.name;
            if (!first) first = devs[j];
            if (name_substr && *name_substr && has_ci(model, name_substr)) { g_dev = devs[j]; snprintf(g_name, sizeof g_name, "%s", model); break; }
            if (!g_name[0]) snprintf(g_name, sizeof g_name, "%s", model);
        }
    }
    if (!g_dev) g_dev = first;
    if (!g_dev) { g_why = "no device"; return 0; }
    g_ndoms = 0;
    if (p_zesDeviceGetCardPowerDomain && p_zesDeviceGetCardPowerDomain(g_dev, &g_pwr) == 0 && g_pwr) { g_doms[g_ndoms] = g_pwr; g_dom_kind[g_ndoms++] = 2; g_pwr_kind = 2; }
    {
        uint32_t n = 0; zes_pwr_handle_t ph[8];
        if (p_zesDeviceEnumPowerDomains(g_dev, &n, NULL) == 0 && n > 0) {
            if (n > 8) n = 8;
            if (p_zesDeviceEnumPowerDomains(g_dev, &n, ph) == 0)
                for (uint32_t i = 0; i < n && g_ndoms < L0_MAX_DOMAINS; i++) { if (ph[i] == g_pwr) continue; g_doms[g_ndoms] = ph[i]; g_dom_kind[g_ndoms++] = 1; if (!g_pwr) { g_pwr = ph[i]; g_pwr_kind = 1; } }
        }
    }
    if (!g_pwr) { g_why = "no power domain"; return 0; }
    if (p_zesDeviceEnumFrequencyDomains && p_zesFrequencyGetProperties) {
        uint32_t n = 0; zes_freq_handle_t fh[8];
        if (p_zesDeviceEnumFrequencyDomains(g_dev, &n, NULL) == 0 && n > 0) {
            if (n > 8) n = 8;
            if (p_zesDeviceEnumFrequencyDomains(g_dev, &n, fh) == 0)
                for (uint32_t i = 0; i < n; i++) { zes_freq_properties_t fp; memset(&fp, 0, sizeof fp); fp.stype = ZES_STRUCTURE_TYPE_FREQ_PROPERTIES; if (p_zesFrequencyGetProperties(fh[i], &fp) != 0) continue; if (fp.type == 0 && !g_freq_gpu) g_freq_gpu = fh[i]; else if (fp.type == 1 && !g_freq_mem) g_freq_mem = fh[i]; }
        }
    }
    if (p_zesDeviceEnumTemperatureSensors && p_zesTemperatureGetState) { uint32_t n = 0; zes_temp_handle_t th[8]; if (p_zesDeviceEnumTemperatureSensors(g_dev, &n, NULL) == 0 && n > 0) { if (n > 8) n = 8; if (p_zesDeviceEnumTemperatureSensors(g_dev, &n, th) == 0) g_temp = th[0]; } }
    g_state = 1; g_why = "ok";
    return 1;
}

int igm_l0_sample(igm_l0_sample_t *out) {
    memset(out, 0, sizeof *out);
    if (g_state <= 0 || !g_pwr) return 0;
    g_samples++;
    for (int i = 0; i < g_ndoms; i++) {
        zes_power_energy_counter_t c;
        if (p_zesPowerGetEnergyCounter(g_doms[i], &c) != 0) continue;
        if (g_have_last[i] && c.timestamp > g_last[i].timestamp && c.energy > g_last[i].energy && !out->power_kind) {
            out->power_w = (double)(c.energy - g_last[i].energy) / (double)(c.timestamp - g_last[i].timestamp);   /* uJ over us = W */
            out->power_kind = g_dom_kind[i];
            out->domain = i;
        }
        g_last[i] = c; g_have_last[i] = 1;
    }
    if (!out->power_kind && g_samples > 2) {
        static char why[96];
        snprintf(why, sizeof why, "energy counter static on %d domain%s (no board power from this driver)", g_ndoms, g_ndoms == 1 ? "" : "s");
        g_why = why;
    }
    zes_freq_state_t st;
    if (g_freq_gpu && p_zesFrequencyGetState) { memset(&st, 0, sizeof st); st.stype = ZES_STRUCTURE_TYPE_FREQ_STATE; if (p_zesFrequencyGetState(g_freq_gpu, &st) == 0 && st.actual > 0) out->core_mhz = (unsigned)st.actual; }
    if (g_freq_mem && p_zesFrequencyGetState) { memset(&st, 0, sizeof st); st.stype = ZES_STRUCTURE_TYPE_FREQ_STATE; if (p_zesFrequencyGetState(g_freq_mem, &st) == 0 && st.actual > 0) out->mem_mhz = (unsigned)st.actual; }
    if (g_temp) { double t = 0; if (p_zesTemperatureGetState(g_temp, &t) == 0 && t > 0) out->temp_c = t; }
    return 1;
}

const char *igm_l0_name(void) { return g_name; }
const char *igm_l0_state(void) { return g_why; }
void igm_l0_close(void) {
    g_dev = NULL; g_pwr = NULL; g_freq_gpu = g_freq_mem = NULL; g_temp = NULL; g_ndoms = 0; g_samples = 0; memset(g_have_last, 0, sizeof g_have_last);
#ifdef _WIN32
    if (g_lib) FreeLibrary(g_lib);
#else
    if (g_lib) dlclose(g_lib);
#endif
    g_lib = NULL; g_state = 0; g_why = "untried";
}
