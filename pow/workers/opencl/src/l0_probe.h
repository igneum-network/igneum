// l0_probe.h: Intel's Level Zero Sysman bound at run time (ze_loader.dll on Windows, libze_loader.so.1 on Linux; the
// driver's own loader, nothing vendored, no header: the handful of entry points are declared here from the public
// Level Zero specification, version 1.x, Apache 2.0 with LLVM exception, intel/compute-runtime). Board power for the
// telemetry line of an Intel Arc, telemetry grade like the ADLX and NVML rows: the energy counter of the card power
// domain (zesDeviceGetCardPowerDomain) where the driver exposes one, else the first enumerated power domain, read as
// the difference quotient of two samples (microjoules over microseconds). Absent loader or device: every call reports
// 0 and igm_l0_state() says why, so a row without the runtime carries "none" and the reason, never a number.
#ifndef IGM_L0_PROBE_H
#define IGM_L0_PROBE_H
#ifdef __cplusplus
extern "C" {
#endif
typedef struct igm_l0_sample {
    double power_w;      /* the mean watts over the interval since the previous sample; 0 on the first sample */
    int power_kind;      /* 0 none (first sample, or every domain's counter static), 1 an enumerated power domain, 2 the card power domain */
    int domain;          /* which of the device's power domains advanced (0 = the first; the card domain when the driver names one) */
    unsigned core_mhz;   /* the GPU frequency domain's actual MHz, 0 when not exposed */
    unsigned mem_mhz;    /* the memory frequency domain's actual MHz, 0 when not exposed */
    double temp_c;       /* the first temperature sensor (GPU or global), 0 when not exposed */
} igm_l0_sample_t;
/* Binds the loader once and selects the first Sysman device whose model name contains name_substr (case-insensitive);
   with no match, the first Intel device. Returns 1 when a device with a power domain is selected. */
int igm_l0_open(const char *name_substr);
int igm_l0_sample(igm_l0_sample_t *out);
const char *igm_l0_name(void);     /* the selected device's model name, "" when none */
const char *igm_l0_state(void);    /* "ok", "no loader", "init failed", "no device", "no power domain", "untried" */
void igm_l0_close(void);
#ifdef __cplusplus
}
#endif
#endif
