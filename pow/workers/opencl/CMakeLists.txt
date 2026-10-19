cmake_minimum_required(VERSION 3.24)
project(igneum-worker-opencl LANGUAGES C CXX)
set(CMAKE_CXX_STANDARD 20)

set(IGNEUM_REF_DIR "${CMAKE_CURRENT_SOURCE_DIR}/../../ref" CACHE PATH "A's reference tree (pow/ref)")
set(IGNEUM_REF_LIBS "" CACHE STRING "A's built libraries to link (ignpow and rvn_ethash), semicolon separated")
option(IGNEUM_REF_EXT "A's library carries the ignpow_ext.h accessors" OFF)
set(IGNEUM_CL_INCLUDE "" CACHE PATH "OpenCL headers directory when not the system's (cross builds)")

add_subdirectory(../common igm_common)

set(KERNEL_FILES
    ${CMAKE_CURRENT_SOURCE_DIR}/kernels/igm_progpow.cl ${CMAKE_CURRENT_SOURCE_DIR}/kernels/igm_dag.cl
    ${CMAKE_CURRENT_SOURCE_DIR}/../common/kernels/prelude_cl.inc ${CMAKE_CURRENT_SOURCE_DIR}/../common/kernels/igm_fishhash.inc ${CMAKE_CURRENT_SOURCE_DIR}/../common/kernels/wrap_fishhash_cl.inc ${CMAKE_CURRENT_SOURCE_DIR}/../common/kernels/igm_fishhash8.inc ${CMAKE_CURRENT_SOURCE_DIR}/../common/kernels/wrap_fishhash8_cl.inc
    ${CMAKE_CURRENT_SOURCE_DIR}/../common/kernels/igm_autolykos.inc ${CMAKE_CURRENT_SOURCE_DIR}/../common/kernels/wrap_autolykos_cl.inc
    ${CMAKE_CURRENT_SOURCE_DIR}/../common/kernels/igm_igneumhash.inc ${CMAKE_CURRENT_SOURCE_DIR}/../common/kernels/igm_igneumhash1.inc ${CMAKE_CURRENT_SOURCE_DIR}/../common/kernels/igm_igneumhash_bw.inc ${CMAKE_CURRENT_SOURCE_DIR}/../common/kernels/wrap_igneumhash_bw_cl.inc ${CMAKE_CURRENT_SOURCE_DIR}/../common/kernels/igm_igneumhash_x2.inc ${CMAKE_CURRENT_SOURCE_DIR}/../common/kernels/igm_igneumhash_ctx.inc ${CMAKE_CURRENT_SOURCE_DIR}/../common/kernels/igm_r0e_access.inc ${CMAKE_CURRENT_SOURCE_DIR}/../common/kernels/igm_r0f.inc ${CMAKE_CURRENT_SOURCE_DIR}/../common/kernels/igm_r16f.inc ${CMAKE_CURRENT_SOURCE_DIR}/../common/kernels/igm_r17.inc ${CMAKE_CURRENT_SOURCE_DIR}/../common/kernels/igm_forge.inc ${CMAKE_CURRENT_SOURCE_DIR}/../common/kernels/wrap_igneumhash_ctx_cl.inc ${CMAKE_CURRENT_SOURCE_DIR}/../common/kernels/igm_igneumhash_bucket.inc ${CMAKE_CURRENT_SOURCE_DIR}/../common/kernels/wrap_igneumhash_bucket_cl.inc ${CMAKE_CURRENT_SOURCE_DIR}/../common/kernels/wrap_igneumhash_cl.inc ${CMAKE_CURRENT_SOURCE_DIR}/../common/kernels/igm_rxe1.inc ${CMAKE_CURRENT_SOURCE_DIR}/../common/kernels/wrap_rxe1_cl.inc ${CMAKE_CURRENT_SOURCE_DIR}/../common/kernels/igm_probe.cl)
add_custom_command(OUTPUT ${CMAKE_CURRENT_BINARY_DIR}/kernels_embedded.cpp
    COMMAND python3 ${CMAKE_CURRENT_SOURCE_DIR}/../common/tools_embed_kernels.py ${CMAKE_CURRENT_BINARY_DIR}/kernels_embedded.cpp ${KERNEL_FILES}
    DEPENDS ${KERNEL_FILES} ${CMAKE_CURRENT_SOURCE_DIR}/../common/tools_embed_kernels.py)

add_executable(igneum-worker-opencl src/main.cpp src/cl_engine.cpp src/cl_engine_x.cpp src/cl_probe.cpp src/cl_shim.cpp src/adlx_probe.c src/l0_probe.c ${CMAKE_CURRENT_BINARY_DIR}/kernels_embedded.cpp)
target_include_directories(igneum-worker-opencl PRIVATE src)
target_compile_options(igneum-worker-opencl PRIVATE -Wall -Wextra)
if(WIN32)
    if(IGNEUM_CL_INCLUDE)
        target_include_directories(igneum-worker-opencl PRIVATE ${IGNEUM_CL_INCLUDE})
    endif()
    target_link_libraries(igneum-worker-opencl PRIVATE igm_common ${IGNEUM_REF_LIBS} -static -static-libgcc -static-libstdc++ -lwinpthread)
else()
    find_package(OpenCL REQUIRED)
    target_link_libraries(igneum-worker-opencl PRIVATE igm_common ${IGNEUM_REF_LIBS} OpenCL::OpenCL pthread ${CMAKE_DL_LIBS})
endif()

add_custom_command(TARGET igneum-worker-opencl POST_BUILD
    COMMAND ${CMAKE_COMMAND} -E make_directory $<TARGET_FILE_DIR:igneum-worker-opencl>/kernels
    COMMAND ${CMAKE_COMMAND} -E copy ${KERNEL_FILES} $<TARGET_FILE_DIR:igneum-worker-opencl>/kernels/)
