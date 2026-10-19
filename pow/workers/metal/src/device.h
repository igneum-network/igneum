// device.h: discovery, the unified-memory budget and the system observers for the
// Metal worker (DESIGN.md 3.3, 3.5, 3.6). GPU engineer B, 9 October 2026. Source only.
#pragma once
#import <Metal/Metal.h>
#include <cstdint>
#include <functional>
#include <string>

namespace igm {

struct DeviceInfo {
    std::string name;            // display only; never used for decisions
    std::string registry_id;     // MTLDevice.registryID as a string
    std::string compute;         // highest Apple family supported: "apple7".."apple9", or "mac2"
    std::string macos;           // operating system version string
    bool unified_memory = false;
    uint64_t recommended_working_set = 0;   // MTLDevice.recommendedMaxWorkingSetSize
    uint64_t current_allocated = 0;         // MTLDevice.currentAllocatedSize
    uint64_t max_buffer_length = 0;
    uint64_t physical_memory = 0;           // ProcessInfo.physicalMemory
    uint32_t max_threads_per_threadgroup = 0;
    bool fanless = false;                   // cooling class, from the model identifier and the fan query
    std::string model_identifier;           // hw.model
    std::string fingerprint;                // sha-256 over backend, vendor, the intrinsic id, name, compute, each followed by a zero byte (the product team's supervisor form)
    std::string platform_serial;            // IOPlatformSerialNumber: the machine's serial, the chip's intrinsic identity (the registry id changes per boot)
    std::string identity;                   // "platform-serial", or "bus" (the registry id, named as such) when the serial cannot be read
};

// Current system memory as the worker's budget sees it (host_statistics64).
struct HostMemory {
    uint64_t free_bytes = 0;
    uint64_t inactive_bytes = 0;
    uint64_t wired_bytes = 0;
    uint64_t compressed_bytes = 0;
    uint64_t internal_bytes = 0;     // anonymous (app) pages, active and inactive
    uint64_t external_bytes = 0;     // file-backed pages, reclaimable
    uint64_t purgeable_bytes = 0;    // purgeable, reclaimable
    uint64_t speculative_bytes = 0;
    // What the system cannot give back without paging: Activity Monitor's "memory used"
    // (app memory plus wired plus compressed). Free plus inactive understates what is
    // available on macOS, because file cache and clean pages sit in the active list
    // (seen on the mini: 9.8 GB "in use" on an idle 16 GB machine by that measure).
    uint64_t non_reclaimable_bytes() const {
        uint64_t app = internal_bytes > purgeable_bytes ? internal_bytes - purgeable_bytes : 0;
        return app + wired_bytes + compressed_bytes;
    }
    uint64_t available_bytes() const { return free_bytes + inactive_bytes + external_bytes + purgeable_bytes + speculative_bytes; }
};

// The budget decision for a working set (DESIGN.md 3.5, with the security review's
// headroom default: the larger of 25% of unified memory or 4 GB [D]).
struct BudgetDecision {
    uint64_t limit = 0;                 // min(recommended working set, physical - headroom - other residency, supervisor budget)
    uint64_t headroom = 0;              // the reserved system headroom used
    uint64_t other_residency = 0;       // non-worker use at decision time
    uint64_t working_set = 0;           // what the candidate needs now
    uint64_t next_dataset = 0;          // what warm preparation would add
    bool fits = false;                  // working_set <= limit
    bool warm_fits = false;             // working_set + next_dataset <= limit
    std::string explain;                // one sentence with the arithmetic, for `refused`
};

enum class Thermal { nominal, fair, serious, critical };
enum class Power { ac, battery, unknown };

struct SystemState {
    Thermal thermal = Thermal::nominal;
    Power power = Power::unknown;
    bool low_power_mode = false;
    bool asleep = false;
    int memory_pressure = 0;   // 0 normal, 1 warning, 2 critical
};

class Device {
public:
    struct Impl;   // the observer state; the IOKit callback is a free function that needs it
    explicit Device(id<MTLDevice> dev);
    ~Device();

    id<MTLDevice> metal() const { return dev_; }
    DeviceInfo info() const;                        // re-reads the live counters
    HostMemory host_memory() const;
    BudgetDecision budget(uint64_t working_set, uint64_t next_dataset, uint64_t supervisor_budget,
                          uint64_t headroom_override = 0) const;
    // The same decision, re-sampled every 100 ms for up to max_ms while it does not fit:
    // after a dataset is released the kernel reclaims its pages a little later than
    // Metal's own accounting drops (seen on the mini: a freed 4.6 GB dataset still in
    // the wired count at the next open). The last sample is returned either way.
    BudgetDecision budget_settled(uint64_t working_set, uint64_t next_dataset, uint64_t supervisor_budget,
                                  uint64_t headroom_override, unsigned max_ms) const;

    // Observers: thermal state, power source, Low Power Mode, sleep and wake, memory
    // pressure. The callback runs on the observer queue; the engine serialises it.
    void start_observers(std::function<void(const SystemState &)> on_change);
    void stop_observers();
    SystemState state() const;

private:
    id<MTLDevice> dev_;
    Impl *impl_;
};

// The highest Apple GPU family name the device supports, queried, never from the name.
std::string highest_family(id<MTLDevice> dev);

}  // namespace igm
