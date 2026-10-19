# igneum-worker-metal

The native arm64 Metal worker for Apple Silicon (docs/gpu-b/DESIGN.md section 3): one process per device, the one worker core of `pow/workers/common` (the GPU team's protocol v0 loop as the product team's supervisor speaks it, the vectors runner, the generator) with the Metal backend behind it. `stat.telemetry` carries `thermal_pressure` and `on_battery` (plus low-power mode, memory pressure, sleep and the effective duty); `fan_pct` and `util_pct` are not reported on Metal (no public API without root) and are omitted.

Status: SOURCE ONLY, NOT BUILT, NOT RUN. The charter forbids builds on the coordinating Mac; the first build happens on a Mac from DESIGN.md section 5.1. Nothing here is a number about any machine.

## Layout

- `shaders/igm_progpow.metal`: the ProgPoW/KAWPOW-family search kernel following the vendored reference's `hash()` byte for byte (both keccak absorb layouts with the candidate's pad words, 64 rounds of the same generated program, the lane fold), with both lane-exchange paths (SIMD shuffle when the queried width is a multiple of the logical group, threadgroup memory with barriers otherwise). The host splices the per-period program at the `IGM_PERIOD_PROGRAM` marker.
- `shaders/igm_dag.metal`: dataset generation on the GPU from the light cache (keccak-f1600), one 64-byte item per thread, bounded batches; a sample of items is checked against the CPU reference before the dataset is used.
- `src/device.mm`: discovery (families, unified memory, working-set limit), the unified-memory budget (headroom the larger of 25% or 4 GB, the security review's default), thermal, power-source, Low Power Mode, sleep and wake, memory-pressure observers.
- `src/engine.mm`: the `EngineIface` backend: A's light context, the device-built dataset, pipelines per period in a binary archive, bounded batches with at most two in flight, cancellation between batches, the thermal duty ladder, warm or cold transitions.
- `src/main.mm`: the run loop and the worker loop or the vectors run.

## Build (on a Mac from section 5.1, never the coordinating Mac)

    cmake -S pow/workers/metal -B build/metal -DIGNEUM_REF_DIR=pow/ref -DIGNEUM_REF_LIBS="<ignpow.a>;<rvn_ethash.a>" [-DIGNEUM_REF_EXT=ON]
    cmake --build build/metal

Without `IGNEUM_REF_EXT=ON` the three `ignpow_ext.h` accessors link as refusing stubs and every open is refused with the reason: honest, not useful.

## Run

    igneum-worker-metal                                   # protocol mode, speaks first with hello
    igneum-worker-metal --vectors pow/vectors/kawpow-0.9.4/*.json    # HW-01; exit 0 only on 0 mismatches
    igneum-worker-metal --probe                           # prints the devices message and exits
    flags: --allow-battery, --allow-low-power-mode (both off by default, D-B4), --shaders <dir>
