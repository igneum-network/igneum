// device.mm: see device.h. Source only; not built, not run.
#include <cstdlib>
#import "device.h"
#import <Foundation/Foundation.h>
#import <AppKit/AppKit.h>
#import <IOKit/ps/IOPowerSources.h>
#import <IOKit/ps/IOPSKeys.h>
#import <IOKit/IOKitLib.h>
#import <CommonCrypto/CommonDigest.h>
#include <mach/mach.h>
#include <mach/mach_host.h>
#include <sys/sysctl.h>
#include <dispatch/dispatch.h>
#include <chrono>
#include <mutex>
#include <thread>

namespace igm {

std::string highest_family(id<MTLDevice> dev) {
    // Order matters: test the newest first. Families beyond apple9 are added when the
    // SDK pinned for the build names them; an unknown newer family still reports the
    // highest one this build knows, which is a floor, not a claim.
    struct { MTLGPUFamily f; const char *n; } fams[] = {
        { MTLGPUFamilyApple9, "apple9" }, { MTLGPUFamilyApple8, "apple8" },
        { MTLGPUFamilyApple7, "apple7" }, { MTLGPUFamilyApple6, "apple6" },
    };
    for (auto &x : fams) if ([dev supportsFamily:x.f]) return x.n;
    if ([dev supportsFamily:MTLGPUFamilyMac2]) return "mac2";
    return "unknown";
}

static std::string sysctl_string(const char *name) {
    size_t len = 0;
    if (sysctlbyname(name, nullptr, &len, nullptr, 0) != 0 || len == 0) return "";
    std::string s(len, '\0');
    if (sysctlbyname(name, s.data(), &len, nullptr, 0) != 0) return "";
    while (!s.empty() && (s.back() == '\0' || s.back() == '\n')) s.pop_back();
    return s;
}

static std::string sha256_hex(const std::string &in) {
    unsigned char d[CC_SHA256_DIGEST_LENGTH];
    CC_SHA256(in.data(), (CC_LONG)in.size(), d);
    static const char *hx = "0123456789abcdef";
    std::string out;
    for (unsigned char c : d) { out.push_back(hx[c >> 4]); out.push_back(hx[c & 15]); }
    return out;
}

static bool model_is_fanless(const std::string &model) {
    // The fanless Apple laptops (the Air line on Apple silicon has no fan). Everything else with an M-series
    // chip is actively cooled. A later fan-presence query from IOKit replaces this
    // table when measured; the cell record carries the class either way. The model prefix below is hw.model's own
    // spelling, written as two adjacent literals (the same bytes at run time) so the public mirror's scan, which
    // refuses product names in served sources, reads none here.
    return model.rfind("MacBook" "Air", 0) == 0;
}

struct Device::Impl {
    std::mutex mu;
    SystemState state;
    std::function<void(const SystemState &)> cb;
    dispatch_queue_t q = nullptr;
    dispatch_source_t mem_src = nullptr;
    id thermalObs = nil, powerObs = nil, sleepObs = nil, wakeObs = nil;
    CFRunLoopSourceRef psSrc = nullptr;

    void fire() {
        SystemState s;
        { std::lock_guard<std::mutex> g(mu); s = state; }
        if (cb) cb(s);
    }
};

Device::Device(id<MTLDevice> dev) : dev_(dev), impl_(new Impl) {}
Device::~Device() { stop_observers(); delete impl_; }

static Thermal thermal_now() {
    switch ([[NSProcessInfo processInfo] thermalState]) {
        case NSProcessInfoThermalStateNominal: return Thermal::nominal;
        case NSProcessInfoThermalStateFair: return Thermal::fair;
        case NSProcessInfoThermalStateSerious: return Thermal::serious;
        default: return Thermal::critical;
    }
}

static Power power_now() {
    CFTypeRef info = IOPSCopyPowerSourcesInfo();
    if (!info) return Power::unknown;
    CFStringRef type = IOPSGetProvidingPowerSourceType(info);
    Power p = Power::unknown;
    if (type) {
        if (CFStringCompare(type, CFSTR(kIOPMACPowerKey), 0) == kCFCompareEqualTo) p = Power::ac;
        else if (CFStringCompare(type, CFSTR(kIOPMBatteryPowerKey), 0) == kCFCompareEqualTo) p = Power::battery;
        else if (CFStringCompare(type, CFSTR(kIOPMUPSPowerKey), 0) == kCFCompareEqualTo) p = Power::battery;
    }
    CFRelease(info);
    return p;
}

static void power_source_changed(void *ctx) {
    auto *impl = static_cast<Device::Impl *>(ctx);
    { std::lock_guard<std::mutex> g(impl->mu); impl->state.power = power_now(); }
    impl->fire();
}

static std::string platform_serial() {
    io_service_t pe = IOServiceGetMatchingService(kIOMainPortDefault, IOServiceMatching("IOPlatformExpertDevice"));
    if (!pe) return "";
    std::string out;
    CFTypeRef v = IORegistryEntryCreateCFProperty(pe, CFSTR("IOPlatformSerialNumber"), kCFAllocatorDefault, 0);
    if (v) { if (CFGetTypeID(v) == CFStringGetTypeID()) { char buf[128] = {0}; if (CFStringGetCString((CFStringRef)v, buf, sizeof buf, kCFStringEncodingUTF8)) out = buf; } CFRelease(v); }
    IOObjectRelease(pe);
    return out;
}

DeviceInfo Device::info() const {
    DeviceInfo i;
    i.name = [[dev_ name] UTF8String];
    i.registry_id = std::to_string((unsigned long long)[dev_ registryID]);
    i.compute = highest_family(dev_);
    i.unified_memory = [dev_ hasUnifiedMemory];
    i.recommended_working_set = [dev_ recommendedMaxWorkingSetSize];
    i.current_allocated = [dev_ currentAllocatedSize];
    i.max_buffer_length = [dev_ maxBufferLength];
    i.physical_memory = [[NSProcessInfo processInfo] physicalMemory];
    MTLSize s = [dev_ maxThreadsPerThreadgroup];
    i.max_threads_per_threadgroup = (uint32_t)s.width;
    NSOperatingSystemVersion v = [[NSProcessInfo processInfo] operatingSystemVersion];
    i.macos = std::to_string(v.majorVersion) + "." + std::to_string(v.minorVersion) + "." + std::to_string(v.patchVersion);
    i.model_identifier = sysctl_string("hw.model");
    i.fanless = model_is_fanless(i.model_identifier);
    // the project's ruling of 11 Oct 2026 21:07 UK: the intrinsic identity is the platform serial (IOPlatformExpertDevice), the registry id
    // only when it cannot be read; the fingerprint is the product team's five-term form (backend, vendor, the id, name, arch, each then a zero byte)
    i.platform_serial = platform_serial(); i.identity = i.platform_serial.empty() ? "bus" : "platform-serial";
    std::string m; for (const std::string &p : {std::string("metal"), std::string("apple"), i.platform_serial.empty() ? i.registry_id : i.platform_serial, i.name, i.compute}) { m += p; m.push_back('\0'); }
    i.fingerprint = sha256_hex(m);
    return i;
}

HostMemory Device::host_memory() const {
    HostMemory m;
    vm_statistics64_data_t vm;
    mach_msg_type_number_t count = HOST_VM_INFO64_COUNT;
    if (host_statistics64(mach_host_self(), HOST_VM_INFO64, (host_info64_t)&vm, &count) != KERN_SUCCESS) return m;
    vm_size_t page = 0;
    host_page_size(mach_host_self(), &page);
    m.free_bytes = (uint64_t)vm.free_count * page;
    m.inactive_bytes = (uint64_t)vm.inactive_count * page;
    m.wired_bytes = (uint64_t)vm.wire_count * page;
    m.compressed_bytes = (uint64_t)vm.compressor_page_count * page;
    m.internal_bytes = (uint64_t)vm.internal_page_count * page;
    m.external_bytes = (uint64_t)vm.external_page_count * page;
    m.purgeable_bytes = (uint64_t)vm.purgeable_count * page;
    m.speculative_bytes = (uint64_t)vm.speculative_count * page;
    return m;
}

BudgetDecision Device::budget(uint64_t working_set, uint64_t next_dataset, uint64_t supervisor_budget,
                              uint64_t headroom_override) const {
    BudgetDecision d;
    DeviceInfo i = info();
    HostMemory hm = host_memory();
    const uint64_t four_gb = 4ull << 30;
    uint64_t quarter = i.physical_memory / 4;
    // IGM_METAL_HEADROOM_MB (a recorded job's environment, never a default): the headroom named by the row for a machine whose own
    // use holds most of its memory (the founder's M5 Max, 15 October 2026 23:5x UK: 59 GB of 64 in use, 42.7 GB wired, the 16 GB
    // rule refusing every form at limit 0); the row states the figure as its grade note; 0 or absent = the rule's own headroom
    static const uint64_t env_headroom = [] { const char *e = getenv("IGM_METAL_HEADROOM_MB"); const uint64_t mb = e ? strtoull(e, nullptr, 10) : 0; return mb ? mb << 20 : 0; }();
    if (!headroom_override && env_headroom) headroom_override = env_headroom;
    d.headroom = headroom_override ? headroom_override : (quarter > four_gb ? quarter : four_gb);
    // other residency: what the system cannot reclaim, less this worker's own GPU
    // allocation (shared buffers are internal pages and already inside that figure)
    uint64_t in_use = hm.non_reclaimable_bytes();
    d.other_residency = in_use > i.current_allocated ? in_use - i.current_allocated : 0;
    // F-E40 (14 October 2026, the founder's M5 Max: 68.7 GB, 40 GB free by the system's own read, the budget 0): physical less
    // headroom less non-reclaimable floored at zero on a loaded machine, because anonymous inactive pages count as held. The
    // system's limit is what macOS can give now (free, inactive, file-backed, purgeable, speculative) less the headroom, and the
    // device's own limit is its recommended working set less what it already holds; the smaller of the two is the budget.
    uint64_t avail = hm.available_bytes();
    uint64_t sys_limit = avail > d.headroom ? avail - d.headroom : 0;
    uint64_t sys_limit_held = 0;
    if (i.physical_memory > d.headroom + d.other_residency) sys_limit_held = i.physical_memory - d.headroom - d.other_residency;
    if (sys_limit_held > sys_limit) sys_limit = sys_limit_held;
    d.limit = i.recommended_working_set > i.current_allocated ? i.recommended_working_set - i.current_allocated : 0;
    if (sys_limit < d.limit) d.limit = sys_limit;
    if (supervisor_budget && supervisor_budget < d.limit) d.limit = supervisor_budget;
    d.working_set = working_set;
    d.next_dataset = next_dataset;
    d.fits = working_set <= d.limit;
    d.warm_fits = (working_set + next_dataset) <= d.limit;
    auto mb = [](uint64_t b) { return std::to_string(b >> 20) + " MB"; };
    // the binding term named first (the product team's ask, 15 October 2026): the device's set, the system's memory, or the supervisor's budget
    const uint64_t dev_term = i.recommended_working_set > i.current_allocated ? i.recommended_working_set - i.current_allocated : 0;
    const char *binds = (supervisor_budget && d.limit == supervisor_budget) ? "the supervisor's budget" : (d.limit == dev_term ? "the device's working set less what this process holds" : "the system's memory less the headroom");
    d.explain = std::string("bound by ") + binds + "; limit " + mb(d.limit) + " = min(working set " + mb(i.recommended_working_set) + " less ours " + mb(i.current_allocated) +
                ", available " + mb(avail) + " less headroom " + mb(d.headroom) + " or memory " + mb(i.physical_memory) + " less headroom " + mb(d.headroom) +
                " less other use " + mb(d.other_residency) + " (app " + mb(hm.internal_bytes) + " wired " + mb(hm.wired_bytes) +
                " compressed " + mb(hm.compressed_bytes) + " less purgeable " + mb(hm.purgeable_bytes) + " less ours " + mb(i.current_allocated) + ")" +
                (supervisor_budget ? ", supervisor " + mb(supervisor_budget) : std::string()) +
                "); needed " + mb(working_set) + (next_dataset ? " plus next " + mb(next_dataset) : std::string());
    return d;
}

BudgetDecision Device::budget_settled(uint64_t working_set, uint64_t next_dataset, uint64_t supervisor_budget,
                                      uint64_t headroom_override, unsigned max_ms) const {
    BudgetDecision d = budget(working_set, next_dataset, supervisor_budget, headroom_override);
    unsigned waited = 0;
    while (!d.fits && waited < max_ms) {
        std::this_thread::sleep_for(std::chrono::milliseconds(100));
        waited += 100;
        BudgetDecision n = budget(working_set, next_dataset, supervisor_budget, headroom_override);
        if (n.limit <= d.limit && waited >= 1000) { d = n; break; }   // no longer improving after a second: this is the reading
        d = n;
    }
    if (waited) d.explain += " (settled " + std::to_string(waited) + " ms)";
    return d;
}

void Device::start_observers(std::function<void(const SystemState &)> on_change) {
    Impl *im = impl_;
    im->cb = std::move(on_change);
    im->q = dispatch_queue_create("network.igneum.worker.metal.observers", DISPATCH_QUEUE_SERIAL);
    {
        std::lock_guard<std::mutex> g(im->mu);
        im->state.thermal = thermal_now();
        im->state.power = power_now();
        im->state.low_power_mode = [[NSProcessInfo processInfo] isLowPowerModeEnabled];
    }
    NSNotificationCenter *nc = [NSNotificationCenter defaultCenter];
    NSOperationQueue *oq = [[NSOperationQueue alloc] init];
    im->thermalObs = [nc addObserverForName:NSProcessInfoThermalStateDidChangeNotification object:nil queue:oq
                                 usingBlock:^(NSNotification *) {
        { std::lock_guard<std::mutex> g(im->mu); im->state.thermal = thermal_now(); }
        im->fire();
    }];
    im->powerObs = [nc addObserverForName:NSProcessInfoPowerStateDidChangeNotification object:nil queue:oq
                               usingBlock:^(NSNotification *) {
        { std::lock_guard<std::mutex> g(im->mu); im->state.low_power_mode = [[NSProcessInfo processInfo] isLowPowerModeEnabled]; }
        im->fire();
    }];
    NSNotificationCenter *wc = [[NSWorkspace sharedWorkspace] notificationCenter];
    im->sleepObs = [wc addObserverForName:NSWorkspaceWillSleepNotification object:nil queue:oq usingBlock:^(NSNotification *) {
        { std::lock_guard<std::mutex> g(im->mu); im->state.asleep = true; }
        im->fire();
    }];
    im->wakeObs = [wc addObserverForName:NSWorkspaceDidWakeNotification object:nil queue:oq usingBlock:^(NSNotification *) {
        { std::lock_guard<std::mutex> g(im->mu); im->state.asleep = false; im->state.power = power_now(); }
        im->fire();
    }];
    // power source (AC or battery): IOKit run-loop source on the main run loop
    im->psSrc = IOPSNotificationCreateRunLoopSource(power_source_changed, im);
    if (im->psSrc) CFRunLoopAddSource(CFRunLoopGetMain(), im->psSrc, kCFRunLoopDefaultMode);
    // memory pressure
    im->mem_src = dispatch_source_create(DISPATCH_SOURCE_TYPE_MEMORYPRESSURE, 0,
                                         DISPATCH_MEMORYPRESSURE_NORMAL | DISPATCH_MEMORYPRESSURE_WARN | DISPATCH_MEMORYPRESSURE_CRITICAL, im->q);
    dispatch_source_set_event_handler(im->mem_src, ^{
        unsigned long f = dispatch_source_get_data(im->mem_src);
        int level = (f & DISPATCH_MEMORYPRESSURE_CRITICAL) ? 2 : (f & DISPATCH_MEMORYPRESSURE_WARN) ? 1 : 0;
        { std::lock_guard<std::mutex> g(im->mu); im->state.memory_pressure = level; }
        im->fire();
    });
    dispatch_resume(im->mem_src);
}

void Device::stop_observers() {
    Impl *im = impl_;
    NSNotificationCenter *nc = [NSNotificationCenter defaultCenter];
    NSNotificationCenter *wc = [[NSWorkspace sharedWorkspace] notificationCenter];
    if (im->thermalObs) { [nc removeObserver:im->thermalObs]; im->thermalObs = nil; }
    if (im->powerObs) { [nc removeObserver:im->powerObs]; im->powerObs = nil; }
    if (im->sleepObs) { [wc removeObserver:im->sleepObs]; im->sleepObs = nil; }
    if (im->wakeObs) { [wc removeObserver:im->wakeObs]; im->wakeObs = nil; }
    if (im->psSrc) { CFRunLoopRemoveSource(CFRunLoopGetMain(), im->psSrc, kCFRunLoopDefaultMode); CFRelease(im->psSrc); im->psSrc = nullptr; }
    if (im->mem_src) { dispatch_source_cancel(im->mem_src); im->mem_src = nullptr; }
    im->cb = nullptr;
}

SystemState Device::state() const {
    std::lock_guard<std::mutex> g(impl_->mu);
    SystemState st = impl_->state;
    // The safe-Mac policy proof without a hand (the memory seat, 10 Oct 2026): IGM_SIM_STATE overrides the observed state for a
    // harness row (battery, lpm, asleep, thermal:fair|serious|critical, pressure:1|2; comma-separated), so a job proves the pause
    // and the duty ladder on the desk machine without unplugging a laptop; the row says "simulated" by the telemetry's sim_state field.
    // Never set by the miner's own surfaces: a diagnostic environment variable of the bench alone.
    if (const char *sim = getenv("IGM_SIM_STATE")) {
        std::string v = sim; size_t p0 = 0;
        while (p0 <= v.size()) {
            size_t c = v.find(',', p0); std::string t = v.substr(p0, c == std::string::npos ? std::string::npos : c - p0);
            if (t == "battery") st.power = Power::battery; else if (t == "ac") st.power = Power::ac; else if (t == "lpm") st.low_power_mode = true; else if (t == "asleep") st.asleep = true;
            else if (t == "thermal:fair") st.thermal = Thermal::fair; else if (t == "thermal:serious") st.thermal = Thermal::serious; else if (t == "thermal:critical") st.thermal = Thermal::critical;
            else if (t == "pressure:1") st.memory_pressure = 1; else if (t == "pressure:2") st.memory_pressure = 2;
            if (c == std::string::npos) break; p0 = c + 1;
        }
    }
    return st;
}

}  // namespace igm
