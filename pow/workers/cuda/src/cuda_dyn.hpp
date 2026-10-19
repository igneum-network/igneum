// Dynamic binding of the CUDA driver, NVRTC and NVML on Windows (MinGW cross-build):
// the three libraries are loaded at run time (nvcuda.dll and nvml.dll come with the
// driver; nvrtc64_120_0.dll and nvrtc-builtins64_129.dll sit beside the worker, or are
// unpacked from the worker's own tail, see dyn_unpack_tail). On Linux the worker links
// the libraries directly and this header is empty. Include after cuda.h, nvrtc.h, nvml.h.
#pragma once

#ifdef _WIN32
#include <windows.h>
#include <stdexcept>
#include <string>
#include <vector>
#include <fstream>
#include <cstring>

// X-macro: every symbol the engine calls, by its real (versioned) name.
#define IGN_CU_FUNCS(X) \
    X(cuInit) X(cuDeviceGetCount) X(cuDeviceGet) X(cuDeviceGetName) X(cuDeviceGetAttribute) X(cuDeviceTotalMem_v2) \
    X(cuDeviceGetPCIBusId) X(cuDriverGetVersion) X(cuCtxCreate_v2) X(cuCtxDestroy_v2) X(cuCtxSetCurrent) X(cuMemAlloc_v2) X(cuMemFree_v2) \
    X(cuMemGetInfo_v2) X(cuMemcpyHtoD_v2) X(cuMemcpyDtoH_v2) X(cuMemsetD32_v2) X(cuModuleLoadData) X(cuModuleLoadDataEx) X(cuModuleUnload) \
    X(cuArrayCreate_v2) X(cuArrayDestroy) X(cuMemcpy2D_v2) X(cuTexObjectCreate) X(cuTexObjectDestroy) \
    X(cuModuleGetFunction) X(cuModuleGetGlobal_v2) X(cuLaunchKernel) X(cuCtxSynchronize) X(cuEventCreate) X(cuEventRecord) X(cuEventSynchronize) X(cuEventElapsedTime) X(cuEventDestroy_v2) X(cuGetErrorString) X(cuFuncGetAttribute) X(cuOccupancyMaxActiveBlocksPerMultiprocessor) X(cuFuncSetAttribute)
#define IGN_NVRTC_FUNCS(X) \
    X(nvrtcGetNumSupportedArchs) X(nvrtcGetSupportedArchs) X(nvrtcVersion) X(nvrtcCreateProgram) X(nvrtcCompileProgram) \
    X(nvrtcGetProgramLogSize) X(nvrtcGetProgramLog) X(nvrtcGetPTXSize) X(nvrtcGetPTX) X(nvrtcGetCUBINSize) X(nvrtcGetCUBIN) \
    X(nvrtcDestroyProgram)
#define IGN_NVML_FUNCS(X) \
    X(nvmlInit_v2) X(nvmlDeviceGetHandleByPciBusId_v2) X(nvmlDeviceGetTemperatureThreshold) X(nvmlDeviceGetPowerUsage) \
    X(nvmlDeviceGetTemperature) X(nvmlDeviceGetClockInfo) X(nvmlDeviceGetMemoryInfo) X(nvmlDeviceGetPowerManagementLimit) \
    X(nvmlDeviceGetCurrentClocksThrottleReasons)

namespace igneum_dyn {
#define IGN_DECL(f) inline decltype(&::f) p_##f = nullptr;
IGN_CU_FUNCS(IGN_DECL)
IGN_NVRTC_FUNCS(IGN_DECL)
IGN_NVML_FUNCS(IGN_DECL)
#undef IGN_DECL

inline std::string exe_dir() {
    char buf[MAX_PATH];
    DWORD n = GetModuleFileNameA(nullptr, buf, MAX_PATH);
    std::string p(buf, n);
    size_t s = p.find_last_of("\\/");
    return s == std::string::npos ? "." : p.substr(0, s);
}

// The worker's file may carry appended payload files: [file bytes]... then a table
// "IGNTAIL1\n<name> <size>\n...<count> <table_len>IGNTAIL1" ending the file. Unpacks
// every entry that is not already beside the executable. Nothing is written anywhere else.
inline void unpack_tail() {
    char buf[MAX_PATH];
    DWORD n = GetModuleFileNameA(nullptr, buf, MAX_PATH);
    std::string self(buf, n);
    std::ifstream in(self, std::ios::binary);
    if (!in) return;
    in.seekg(0, std::ios::end);
    const long long total = in.tellg();
    if (total < 32) return;
    char trailer[32];
    in.seekg(total - 32);
    in.read(trailer, 32);
    std::string tr(trailer, 32);
    if (tr.compare(24, 8, "IGNTAIL1") != 0) return;
    const long long table_len = std::strtoll(tr.substr(0, 24).c_str(), nullptr, 10);
    if (table_len <= 0 || table_len > total) return;
    std::string table(table_len, '\0');
    in.seekg(total - 32 - table_len);
    in.read(&table[0], table_len);
    if (table.compare(0, 9, "IGNTAIL1\n") != 0) return;
    std::vector<std::pair<std::string, long long>> entries;
    size_t pos = 9;
    while (pos < table.size()) {
        size_t nl = table.find('\n', pos);
        if (nl == std::string::npos) break;
        std::string line = table.substr(pos, nl - pos);
        pos = nl + 1;
        size_t sp = line.find(' ');
        if (sp == std::string::npos) continue;
        entries.emplace_back(line.substr(0, sp), std::strtoll(line.c_str() + sp + 1, nullptr, 10));
    }
    long long payload = 0;
    for (auto& e : entries) payload += e.second;
    long long off = total - 32 - table_len - payload;
    const std::string dir = exe_dir();
    for (auto& e : entries) {
        const std::string path = dir + "\\" + e.first;
        std::ifstream probe(path, std::ios::binary);
        if (!probe) {
            std::vector<char> data(static_cast<size_t>(e.second));
            in.seekg(off);
            in.read(data.data(), e.second);
            std::ofstream o(path + ".part", std::ios::binary);
            o.write(data.data(), e.second);
            o.close();
            MoveFileExA((path + ".part").c_str(), path.c_str(), MOVEFILE_REPLACE_EXISTING);
        }
        off += e.second;
    }
}

inline void load_all() {
    unpack_tail();
    const std::string dir = exe_dir();
    SetDllDirectoryA(dir.c_str());
    HMODULE cu = LoadLibraryA("nvcuda.dll");
    if (!cu) throw std::runtime_error("nvcuda.dll not found: no NVIDIA driver on this machine");
    HMODULE rt = LoadLibraryA((dir + "\\nvrtc64_120_0.dll").c_str());
    if (!rt) rt = LoadLibraryA("nvrtc64_120_0.dll");
    if (!rt) throw std::runtime_error("nvrtc64_120_0.dll not found beside the worker");
    HMODULE ml = LoadLibraryA("nvml.dll");
    if (!ml) ml = LoadLibraryA("C:\\Program Files\\NVIDIA Corporation\\NVSMI\\nvml.dll");
#define IGN_LOAD(lib, f) p_##f = reinterpret_cast<decltype(&::f)>(GetProcAddress(lib, #f)); if (!p_##f && (lib) != ml) throw std::runtime_error("symbol missing: " #f);
#define IGN_LOAD_CU(f) IGN_LOAD(cu, f)
#define IGN_LOAD_RT(f) IGN_LOAD(rt, f)
#define IGN_LOAD_ML(f) if (ml) { IGN_LOAD(ml, f) }
    IGN_CU_FUNCS(IGN_LOAD_CU)
    IGN_NVRTC_FUNCS(IGN_LOAD_RT)
    IGN_NVML_FUNCS(IGN_LOAD_ML)
#undef IGN_LOAD
#undef IGN_LOAD_CU
#undef IGN_LOAD_RT
#undef IGN_LOAD_ML
}

inline bool nvml_present() { return p_nvmlInit_v2 != nullptr; }
}  // namespace igneum_dyn

// The engine's calls resolve to the pointers (cuda.h's own macros map cuCtxCreate to cuCtxCreate_v2 first).
#define IGN_MAP(f) \
    inline auto ign_##f = [](auto... a) { return igneum_dyn::p_##f(a...); };
#define IGN_REDIRECT(f) ign_##f
IGN_CU_FUNCS(IGN_MAP)
IGN_NVRTC_FUNCS(IGN_MAP)
IGN_NVML_FUNCS(IGN_MAP)
#undef IGN_MAP
#define cuInit ign_cuInit
#define cuDeviceGetCount ign_cuDeviceGetCount
#define cuDeviceGet ign_cuDeviceGet
#define cuDeviceGetName ign_cuDeviceGetName
#define cuDeviceGetAttribute ign_cuDeviceGetAttribute
#undef cuDeviceTotalMem
#define cuDeviceTotalMem ign_cuDeviceTotalMem_v2
#define cuDeviceGetPCIBusId ign_cuDeviceGetPCIBusId
#define cuDriverGetVersion ign_cuDriverGetVersion
#undef cuCtxCreate
#define cuCtxCreate ign_cuCtxCreate_v2
#undef cuCtxDestroy
#define cuCtxDestroy ign_cuCtxDestroy_v2
#define cuCtxSetCurrent ign_cuCtxSetCurrent
#define cuEventCreate ign_cuEventCreate
#define cuEventRecord ign_cuEventRecord
#define cuEventSynchronize ign_cuEventSynchronize
#define cuEventElapsedTime ign_cuEventElapsedTime
#undef cuEventDestroy
#define cuEventDestroy ign_cuEventDestroy_v2
#undef cuMemAlloc
#define cuMemAlloc ign_cuMemAlloc_v2
#undef cuMemFree
#define cuMemFree ign_cuMemFree_v2
#undef cuMemGetInfo
#define cuMemGetInfo ign_cuMemGetInfo_v2
#undef cuMemcpyHtoD
#define cuMemcpyHtoD ign_cuMemcpyHtoD_v2
#undef cuArrayCreate
#define cuArrayCreate ign_cuArrayCreate_v2
#undef cuMemcpy2D
#define cuMemcpy2D ign_cuMemcpy2D_v2
#undef cuArrayDestroy
#define cuArrayDestroy ign_cuArrayDestroy
#undef cuTexObjectCreate
#define cuTexObjectCreate ign_cuTexObjectCreate
#undef cuTexObjectDestroy
#define cuTexObjectDestroy ign_cuTexObjectDestroy
#undef cuMemcpyDtoH
#define cuMemcpyDtoH ign_cuMemcpyDtoH_v2
#undef cuMemsetD32
#define cuMemsetD32 ign_cuMemsetD32_v2
#define cuModuleLoadData ign_cuModuleLoadData
#undef cuModuleLoadDataEx
#define cuModuleLoadDataEx ign_cuModuleLoadDataEx   // the period module from PTX with the driver's ptxas at a chosen level (tune jit)
#define cuModuleUnload ign_cuModuleUnload
#define cuModuleGetFunction ign_cuModuleGetFunction
#undef cuFuncGetAttribute
#define cuFuncGetAttribute ign_cuFuncGetAttribute
#undef cuOccupancyMaxActiveBlocksPerMultiprocessor
#define cuOccupancyMaxActiveBlocksPerMultiprocessor ign_cuOccupancyMaxActiveBlocksPerMultiprocessor
#undef cuFuncSetAttribute
#define cuFuncSetAttribute ign_cuFuncSetAttribute   // the matched-resource probe's shared-memory opt-in (the Windows build binds the driver at run time)
#undef cuModuleGetGlobal
#define cuModuleGetGlobal ign_cuModuleGetGlobal_v2
#define cuLaunchKernel ign_cuLaunchKernel
#define cuCtxSynchronize ign_cuCtxSynchronize
#define cuGetErrorString ign_cuGetErrorString
#define nvrtcGetNumSupportedArchs ign_nvrtcGetNumSupportedArchs
#define nvrtcGetSupportedArchs ign_nvrtcGetSupportedArchs
#define nvrtcVersion ign_nvrtcVersion
#define nvrtcCreateProgram ign_nvrtcCreateProgram
#define nvrtcCompileProgram ign_nvrtcCompileProgram
#define nvrtcGetProgramLogSize ign_nvrtcGetProgramLogSize
#define nvrtcGetProgramLog ign_nvrtcGetProgramLog
#define nvrtcGetPTXSize ign_nvrtcGetPTXSize
#define nvrtcGetPTX ign_nvrtcGetPTX
#define nvrtcGetCUBINSize ign_nvrtcGetCUBINSize
#define nvrtcGetCUBIN ign_nvrtcGetCUBIN
#define nvrtcDestroyProgram ign_nvrtcDestroyProgram
#define nvmlInit_v2 ign_nvmlInit_v2
#undef nvmlDeviceGetHandleByPciBusId
#define nvmlDeviceGetHandleByPciBusId_v2 ign_nvmlDeviceGetHandleByPciBusId_v2
#define nvmlDeviceGetTemperatureThreshold ign_nvmlDeviceGetTemperatureThreshold
#define nvmlDeviceGetPowerUsage ign_nvmlDeviceGetPowerUsage
#define nvmlDeviceGetTemperature ign_nvmlDeviceGetTemperature
#define nvmlDeviceGetClockInfo ign_nvmlDeviceGetClockInfo
#define nvmlDeviceGetMemoryInfo ign_nvmlDeviceGetMemoryInfo
#define nvmlDeviceGetPowerManagementLimit ign_nvmlDeviceGetPowerManagementLimit
#define nvmlDeviceGetCurrentClocksThrottleReasons ign_nvmlDeviceGetCurrentClocksThrottleReasons
#define IGN_DYN_LOAD() igneum_dyn::load_all()
#define IGN_NVML_PRESENT() igneum_dyn::nvml_present()
#else
#define IGN_DYN_LOAD() ((void)0)
#define IGN_NVML_PRESENT() true
#endif
