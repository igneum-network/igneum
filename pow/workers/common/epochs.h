// epochs.h: Orchard OR-8's transition campaign (the Apple memory seat, 10 October 2026): N complete epoch transitions on one
// configuration, each measured whole, on any engine (the forms are the engine's knobs: alloc, storage, pretouch, persist,
// inflight, batch_ms, guard_mb).
//
// One transition, warm (the double-buffered form): mine the resident epoch to a steady rate; prepare the next epoch beside it
// (the build on the mining queue, one slice behind every batch); mine on while it builds (the rate retained during construction);
// once ready mine on for the post-build window; then move the job to the next epoch (the swap) and time the first batch after it
// (the job-switch latency; the hashes the steady rate would have given over that gap are the lost hashes at the swap).
// Cold (the single-buffer control): no prepare; the job moves and the engine rebuilds the dataset in place (the downtime).
// Every transition line carries build duration, the three rates, swap wait, first-batch latency, downtime, lost hashes, the
// memory counters (peak physical footprint, the host's faults, page-ins, page-outs, compressions, swap-outs over the
// transition, the compressor's size) and the worst thermal state seen; UTC bounds so a wall meter's samples can be sliced
// onto it when the plug exists (no energy column without one: named absent, never estimated). The summary line gives the
// campaign's totals and the epoch overhead fraction the gate reads (lost work over the steady rate's work).
#pragma once
#include <cstdint>
#include <string>
#include "engine_iface.h"

namespace igm {
// epoch_s: the window per epoch (settle, build and post-build inside it); share_bits and verify_every as the bench's.
int epochs_main(EngineIface &eng, const std::string &algo, uint64_t height, uint32_t transitions, bool cold, double epoch_s, const std::string &notes, const Knobs &knobs, int share_bits, int verify_every);
}
