// protocol.h: the worker protocol version 0 loop (docs/gpu-a/WORKER-PROTOCOL.md, the one
// product engineer A's supervisor speaks) over an EngineIface: NDJSON on stdin and
// stdout with per-direction seq and re, hello first, configure, ready or refuse, job
// with boundary and share target, share, stat at the configured rate with telemetry and
// state, fault, cancel, pause and resume, prepare, shutdown then bye.
// GPU engineer B, 9 October 2026.
#pragma once
#include <string>
#include "engine_iface.h"

namespace igm {

// Runs until shutdown or EOF; returns the process exit code (64 when the first line
// from the supervisor is not configure, as the protocol says). `vectors_ok` is the
// result of the start-up self-test for the hello line.
int run_worker_loop(EngineIface &eng, const std::string &worker, const std::string &version,
                    const std::string &algo_id, bool vectors_ok, bool probe_only);

}  // namespace igm
