// cl_shim.h: on Windows the worker resolves the OpenCL entry points from the system's
// OpenCL.dll (the Khronos ICD loader every AMD and NVIDIA driver installs) at first use,
// so no import library is needed at build time and a machine without a driver gets a
// plain "no OpenCL runtime" instead of a loader error. The names below are the ones the
// workers call; a macro routes each to the resolved pointer. Elsewhere this header is
// empty and the ICD loader is linked. GPU engineer B, 10 October 2026.
#pragma once
#define CL_TARGET_OPENCL_VERSION 300
#include <CL/cl.h>
#ifdef _WIN32
#include <CL/cl_function_types.h>

namespace igm {
struct ClFns {
    clGetPlatformIDs_fn f_clGetPlatformIDs = nullptr;
    clGetPlatformInfo_fn f_clGetPlatformInfo = nullptr;
    clGetDeviceIDs_fn f_clGetDeviceIDs = nullptr;
    clGetDeviceInfo_fn f_clGetDeviceInfo = nullptr;
    clCreateContext_fn f_clCreateContext = nullptr;
    clReleaseContext_fn f_clReleaseContext = nullptr;
    clCreateCommandQueueWithProperties_fn f_clCreateCommandQueueWithProperties = nullptr;
    clReleaseCommandQueue_fn f_clReleaseCommandQueue = nullptr;
    clCreateBuffer_fn f_clCreateBuffer = nullptr;
    clCreateImage_fn f_clCreateImage = nullptr;   // the texture determinism probe (14 Oct 2026)
    clReleaseMemObject_fn f_clReleaseMemObject = nullptr;
    clCreateProgramWithSource_fn f_clCreateProgramWithSource = nullptr;
    clBuildProgram_fn f_clBuildProgram = nullptr;
    typedef cl_int (CL_API_CALL *clGetProgramInfo_fn)(cl_program, cl_program_info, size_t, void *, size_t *);
    clGetProgramBuildInfo_fn f_clGetProgramBuildInfo = nullptr;
    clGetProgramInfo_fn f_clGetProgramInfo = nullptr;   // the program binary (the ten-field record)
    clReleaseProgram_fn f_clReleaseProgram = nullptr;
    clCreateKernel_fn f_clCreateKernel = nullptr;
    clReleaseKernel_fn f_clReleaseKernel = nullptr;
    clSetKernelArg_fn f_clSetKernelArg = nullptr;
    clGetKernelWorkGroupInfo_fn f_clGetKernelWorkGroupInfo = nullptr;
    clEnqueueNDRangeKernel_fn f_clEnqueueNDRangeKernel = nullptr;
    clEnqueueReadBuffer_fn f_clEnqueueReadBuffer = nullptr;
    clEnqueueWriteBuffer_fn f_clEnqueueWriteBuffer = nullptr;
    clFinish_fn f_clFinish = nullptr;
    clFlush_fn f_clFlush = nullptr;
    clWaitForEvents_fn f_clWaitForEvents = nullptr;
    clGetEventInfo_fn f_clGetEventInfo = nullptr;
    clCreateSubBuffer_fn f_clCreateSubBuffer = nullptr;
    typedef cl_int (CL_API_CALL *clEnqueueCopyBuffer_fn)(cl_command_queue, cl_mem, cl_mem, size_t, size_t, size_t, cl_uint, const cl_event *, cl_event *);
    clEnqueueCopyBuffer_fn f_clEnqueueCopyBuffer = nullptr;   // the channel-coloured copies of the dataset
    clEnqueueFillBuffer_fn f_clEnqueueFillBuffer = nullptr;
    clGetEventProfilingInfo_fn f_clGetEventProfilingInfo = nullptr;
    clReleaseEvent_fn f_clReleaseEvent = nullptr;
    bool loaded = false;
    const char *missing = nullptr;
};
// Resolves once from OpenCL.dll; returns the table (loaded false when the DLL or a symbol is absent).
const ClFns &cl_fns();
}  // namespace igm

#define clGetPlatformIDs (igm::cl_fns().f_clGetPlatformIDs)
#define clGetPlatformInfo (igm::cl_fns().f_clGetPlatformInfo)
#define clGetDeviceIDs (igm::cl_fns().f_clGetDeviceIDs)
#define clGetDeviceInfo (igm::cl_fns().f_clGetDeviceInfo)
#define clCreateContext (igm::cl_fns().f_clCreateContext)
#define clReleaseContext (igm::cl_fns().f_clReleaseContext)
#define clCreateCommandQueueWithProperties (igm::cl_fns().f_clCreateCommandQueueWithProperties)
#define clReleaseCommandQueue (igm::cl_fns().f_clReleaseCommandQueue)
#define clCreateBuffer (igm::cl_fns().f_clCreateBuffer)
#define clCreateImage (igm::cl_fns().f_clCreateImage)
#define clReleaseMemObject (igm::cl_fns().f_clReleaseMemObject)
#define clCreateProgramWithSource (igm::cl_fns().f_clCreateProgramWithSource)
#define clBuildProgram (igm::cl_fns().f_clBuildProgram)
#define clGetProgramBuildInfo (igm::cl_fns().f_clGetProgramBuildInfo)
#define clGetProgramInfo (igm::cl_fns().f_clGetProgramInfo)
#define clReleaseProgram (igm::cl_fns().f_clReleaseProgram)
#define clCreateKernel (igm::cl_fns().f_clCreateKernel)
#define clReleaseKernel (igm::cl_fns().f_clReleaseKernel)
#define clSetKernelArg (igm::cl_fns().f_clSetKernelArg)
#define clGetKernelWorkGroupInfo (igm::cl_fns().f_clGetKernelWorkGroupInfo)
#define clEnqueueNDRangeKernel (igm::cl_fns().f_clEnqueueNDRangeKernel)
#define clEnqueueReadBuffer (igm::cl_fns().f_clEnqueueReadBuffer)
#define clEnqueueWriteBuffer (igm::cl_fns().f_clEnqueueWriteBuffer)
#define clFinish (igm::cl_fns().f_clFinish)
#define clFlush (igm::cl_fns().f_clFlush)
#define clWaitForEvents (igm::cl_fns().f_clWaitForEvents)
#define clGetEventInfo (igm::cl_fns().f_clGetEventInfo)
#define clCreateSubBuffer (igm::cl_fns().f_clCreateSubBuffer)
#define clEnqueueCopyBuffer (igm::cl_fns().f_clEnqueueCopyBuffer)
#define clEnqueueFillBuffer (igm::cl_fns().f_clEnqueueFillBuffer)
#define clGetEventProfilingInfo (igm::cl_fns().f_clGetEventProfilingInfo)
#define clReleaseEvent (igm::cl_fns().f_clReleaseEvent)
#endif
