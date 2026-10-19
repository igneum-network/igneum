// bench_main.h: the screening bench of one height for S seconds, in the line shapes GPU
// engineer A's CUDA worker prints (budget, prepared, periodic rate lines with telemetry,
// the final bench line) so pow/harness/table.py reads either worker's screen cell.
#pragma once
#include <cstdint>
#include <string>
#include "engine_iface.h"

namespace igm {
// transitions: "" for one height; "warm,cold" runs a third of the time at the height, prepares the next epoch beside
// the resident one and moves to it (warm), then moves one epoch further without preparation (cold); each transition
// prints its own line (prepare_ms, downtime_ms) and the bench summary carries them.
int bench_main(EngineIface &eng, const std::string &algo, uint64_t height, double seconds, const std::string &notes, const Knobs &knobs = Knobs(), const std::string &transitions = "", double peak_gbs = 0.0, double ceiling_gbs = 0.0, int share_bits = 32, int verify_every = 0, double release_after_s = 0.0);
// release_after_s: Ember's memory-pressure release proved on the bench (the memory seat): after that many seconds of the run the
// knob release_dataset is set; the engine drops the dataset and rebuilds before its next batch; the bench prints the gap.
// share_bits: the bench target is 2^(256 - share_bits) (32 by default: a hit per 4.3 G hashes); verify_every: every n-th hit
// is recomputed by the linked reference (0: none), verified and failed counted; accepted work = unique hits x 2^share_bits.
}
