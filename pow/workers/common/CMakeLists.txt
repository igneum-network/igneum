# The one worker core shared by the Metal and OpenCL backends.
# Expects IGNEUM_REF_DIR (the directory holding pow/ref/include/ignpow.h and A's built
# libraries) and, optionally, IGNEUM_REF_EXT=ON once ignpow_ext.h's accessors live in A's
# library (until then the refusing stub links).
cmake_minimum_required(VERSION 3.24)

add_library(igm_common STATIC
    json.cpp protocol.cpp progpow_gen.cpp r0d_gen.cpp vectors.cpp vectors_main.cpp refbridge.cpp bench_main.cpp kernel_source.cpp ih_params.cpp rxe1_params.cpp seams.cpp epochs.cpp)
target_include_directories(igm_common PUBLIC ${CMAKE_CURRENT_SOURCE_DIR} ${IGNEUM_REF_DIR}/include)
target_compile_features(igm_common PUBLIC cxx_std_20)
target_compile_options(igm_common PRIVATE -Wall -Wextra)
if(NOT IGNEUM_REF_EXT)
    message(WARNING "IGNEUM_REF_EXT is off: the ignpow_ext.h stub links; vector runs report unavailable, never pass")
    target_sources(igm_common PRIVATE ext_stub.cpp)
endif()
