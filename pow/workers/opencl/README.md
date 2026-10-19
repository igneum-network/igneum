# igneum-worker-opencl

The OpenCL worker for AMD (and, as a second OpenCL implementation for portability and HW-01, NVIDIA's ICD on rented pods): the portable OpenCL C 1.2 baseline with the sub-group lane path when the device offers `cl_khr_subgroups` and `cl_khr_subgroup_shuffle` and its wave width is a multiple of the logical group (docs/gpu-b/DESIGN.md section 4). One process per device on the one worker core of `pow/workers/common`.

Status: SOURCE ONLY. The host side builds on a build box (no GPU: compiles and links, runs `--list`); the kernels first run on a rented NVIDIA pod, then on the AMD bench rows of docs/gpu-b/AMD-COHORT.md. Nothing here is a number about any card.

## Build

    cmake -S pow/workers/opencl -B build/opencl -DIGNEUM_REF_DIR=pow/ref -DIGNEUM_REF_LIBS="<ignpow.a>;<rvn_ethash.a>" [-DIGNEUM_REF_EXT=ON]
    cmake --build build/opencl

## Run

    igneum-worker-opencl --list                            # platforms and devices with driver strings (the matrix row's pin)
    igneum-worker-opencl --device N                        # protocol mode
    igneum-worker-opencl --device N --vectors pow/vectors/kawpow-0.9.4/*.json
    igneum-worker-opencl --probe

The driver and runtime version strings the device reports are what the matrix row is pinned to; the worker never keys a decision on the device name.

Protocol: the GPU team's v0 (configure, ready with the budget terms, job, share, stat, fault, pause, resume, prepare, shutdown, bye). `stat.telemetry` on Linux with amdgpu carries `temp_c`, `power_w`, `fan_pct` (pwm1 of pwm1_max), `util_pct` (gpu_busy_percent), `core_mhz`, `mem_mhz` and a `throttle` list from the device's own hwmon node (matched by PCI topology); OpenCL itself exposes no telemetry, and a field the driver does not provide is omitted. `thermal_pressure` and `on_battery` are Apple signals and are not reported here.
