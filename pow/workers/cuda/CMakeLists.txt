cmake_minimum_required(VERSION 3.18)
project(igneum-worker-cuda CXX)

set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
if(NOT CMAKE_BUILD_TYPE)
  set(CMAKE_BUILD_TYPE Release)
endif()

# Host-only build: the device code is compiled at run time by NVRTC, so nvcc
# and a driver are not needed to build; only the toolkit headers and the
# static NVRTC libraries, plus the driver and NVML stubs for linking.
if(NOT CUDA_HOME)
  set(CUDA_HOME /usr/local/cuda)
endif()

add_subdirectory(${CMAKE_CURRENT_SOURCE_DIR}/../../ref ${CMAKE_BINARY_DIR}/ref)

set(RVN ${CMAKE_CURRENT_SOURCE_DIR}/../../third_party/ravencoin-ethash-22549129/crypto/ethash/lib/keccak)
file(READ ${RVN}/keccakf800.c K800)
file(READ ${RVN}/keccakf1600.c K1600)
file(READ ${CMAKE_CURRENT_SOURCE_DIR}/src/kernels.cu.txt KERN)
file(READ ${CMAKE_CURRENT_SOURCE_DIR}/src/kernels_autolykos.cu.txt KERNAL)
file(READ ${CMAKE_CURRENT_SOURCE_DIR}/src/kernels_fishhash.cu.txt KERNFH)
file(READ ${CMAKE_CURRENT_SOURCE_DIR}/src/kernels_igneumhash.cu.txt KERNIH)
file(READ ${CMAKE_CURRENT_SOURCE_DIR}/src/kernels_rxe1.cu.txt KERNRX)
file(READ ${CMAKE_CURRENT_SOURCE_DIR}/src/kernels_rxe3.cu.txt KERNRX3)
string(REGEX REPLACE "#include[^\n]*\n" "" K800 "${K800}")
string(REGEX REPLACE "#include[^\n]*\n" "" K1600 "${K1600}")
file(WRITE ${CMAKE_BINARY_DIR}/embedded.hpp
  "#pragma once\nstatic const char* const kEmbeddedKeccakf800 = R\"EMB(${K800})EMB\";\nstatic const char* const kEmbeddedKeccakf1600 = R\"EMB(${K1600})EMB\";\nstatic const char* const kEmbeddedKernels = R\"EMB(${KERN})EMB\";\nstatic const char* const kEmbeddedKernelsAutolykos = R\"EMB(${KERNAL})EMB\";\nstatic const char* const kEmbeddedKernelsFishhash = R\"EMB(${KERNFH})EMB\";\nstatic const char* const kEmbeddedKernelsIgneumhash = R\"EMB(${KERNIH})EMB\";\nstatic const char* const kEmbeddedKernelsRxe1 = R\"EMB(${KERNRX})EMB\";\nstatic const char* const kEmbeddedKernelsRxe3 = R\"EMB(${KERNRX3})EMB\";\n")
set_property(DIRECTORY APPEND PROPERTY CMAKE_CONFIGURE_DEPENDS src/kernels.cu.txt src/kernels_autolykos.cu.txt src/kernels_fishhash.cu.txt src/kernels_igneumhash.cu.txt src/kernels_rxe1.cu.txt src/kernels_rxe3.cu.txt)

# the worker's own params parser (pow/workers/common/ih_params.cpp, with its json) beside the reference's: the keyed-read steps under
# versions 21 and 25 (features bit 10, keyed_rounds; KEYED-RELEASED-VERSIONS-SPEC.md section 2) are read from it at the engine's open
add_executable(igneum-worker-cuda src/main.cpp src/engine.cpp src/codegen.cpp ../common/ih_params.cpp ../common/json.cpp)
target_include_directories(igneum-worker-cuda PRIVATE ${CMAKE_BINARY_DIR} ${CUDA_HOME}/include ${CMAKE_CURRENT_SOURCE_DIR}/../common)
target_link_directories(igneum-worker-cuda PRIVATE ${CUDA_HOME}/lib64 ${CUDA_HOME}/lib64/stubs)
target_link_libraries(igneum-worker-cuda PRIVATE ignpow nvrtc_static nvrtc-builtins_static nvptxcompiler_static cuda nvidia-ml pthread dl)
target_compile_options(igneum-worker-cuda PRIVATE -O2 -Wall -Wextra)
# The stubs' sonames are libcuda.so.1 and libnvidia-ml.so.1: the driver's own libraries at run time.
set_target_properties(igneum-worker-cuda PROPERTIES BUILD_RPATH "" INSTALL_RPATH "")
