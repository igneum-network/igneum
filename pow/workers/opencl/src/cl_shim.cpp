// cl_shim.cpp: see cl_shim.h. Windows only; elsewhere this file is empty.
#ifdef _WIN32
#include "cl_shim.h"
#include <windows.h>
#undef clGetPlatformIDs
#undef clGetPlatformInfo
#undef clGetDeviceIDs
#undef clGetDeviceInfo
#undef clCreateContext
#undef clReleaseContext
#undef clCreateCommandQueueWithProperties
#undef clReleaseCommandQueue
#undef clCreateBuffer
#undef clCreateImage
#undef clReleaseMemObject
#undef clCreateProgramWithSource
#undef clBuildProgram
#undef clGetProgramBuildInfo
#undef clGetProgramInfo
#undef clEnqueueCopyBuffer
#undef clReleaseProgram
#undef clCreateKernel
#undef clReleaseKernel
#undef clSetKernelArg
#undef clGetKernelWorkGroupInfo
#undef clEnqueueNDRangeKernel
#undef clEnqueueReadBuffer
#undef clEnqueueWriteBuffer
#undef clFinish
#undef clFlush
#undef clWaitForEvents
#undef clGetEventInfo
#undef clCreateSubBuffer
#undef clEnqueueFillBuffer
#undef clGetEventProfilingInfo
#undef clReleaseEvent
namespace igm {
static ClFns g_fns;
static bool g_done = false;
template <class F> static bool res(HMODULE h, F &f, const char *n, const char *&missing) { f = (F)GetProcAddress(h, n); if (!f && !missing) missing = n; return f != nullptr; }
const ClFns &cl_fns() {
    if (g_done) return g_fns;
    g_done = true;
    HMODULE h = LoadLibraryA("OpenCL.dll");
    if (!h) { g_fns.loaded = false; g_fns.missing = "OpenCL.dll"; return g_fns; }
    const char *m = nullptr;
#define R(n) res(h, g_fns.f_##n, #n, m)
    bool ok = R(clGetPlatformIDs) & R(clGetPlatformInfo) & R(clGetDeviceIDs) & R(clGetDeviceInfo) & R(clCreateContext) & R(clReleaseContext) & R(clCreateCommandQueueWithProperties) & R(clReleaseCommandQueue)
            & R(clCreateBuffer) & R(clCreateImage) & R(clReleaseMemObject) & R(clCreateProgramWithSource) & R(clBuildProgram) & R(clGetProgramBuildInfo) & R(clGetProgramInfo) & R(clReleaseProgram) & R(clCreateKernel) & R(clReleaseKernel)
            & R(clSetKernelArg) & R(clGetKernelWorkGroupInfo) & R(clEnqueueNDRangeKernel) & R(clEnqueueReadBuffer) & R(clEnqueueWriteBuffer) & R(clFinish) & R(clFlush) & R(clWaitForEvents) & R(clGetEventInfo) & R(clCreateSubBuffer) & R(clEnqueueCopyBuffer) & R(clEnqueueFillBuffer) & R(clGetEventProfilingInfo) & R(clReleaseEvent);
#undef R
    g_fns.loaded = ok; g_fns.missing = m;
    return g_fns;
}
}  // namespace igm
#endif
