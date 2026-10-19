cmake_minimum_required(VERSION 3.24)
project(igneum-worker-metal LANGUAGES CXX OBJCXX)

# Native arm64 only (DESIGN.md 3.2): no Rosetta path, no x86_64 slice.
set(CMAKE_OSX_ARCHITECTURES arm64 CACHE STRING "" FORCE)
set(CMAKE_OSX_DEPLOYMENT_TARGET 14.0 CACHE STRING "D-B3 default: macOS 14")
set(CMAKE_CXX_STANDARD 20)
set(CMAKE_OBJCXX_STANDARD 20)

set(IGNEUM_REF_DIR "${CMAKE_CURRENT_SOURCE_DIR}/../../ref" CACHE PATH "A's reference tree (pow/ref)")
set(IGNEUM_REF_LIBS "" CACHE STRING "A's built libraries to link (ignpow and rvn_ethash), semicolon separated")
option(IGNEUM_REF_EXT "A's library carries the ignpow_ext.h accessors" OFF)

add_subdirectory(../common igm_common)

set(SHADER_FILES ${CMAKE_CURRENT_SOURCE_DIR}/shaders/igm_progpow.metal ${CMAKE_CURRENT_SOURCE_DIR}/shaders/igm_dag.metal
    ${CMAKE_CURRENT_SOURCE_DIR}/../common/kernels/prelude_msl.inc ${CMAKE_CURRENT_SOURCE_DIR}/../common/kernels/igm_fishhash.inc ${CMAKE_CURRENT_SOURCE_DIR}/../common/kernels/wrap_fishhash_msl.inc ${CMAKE_CURRENT_SOURCE_DIR}/../common/kernels/igm_fishhash8.inc ${CMAKE_CURRENT_SOURCE_DIR}/../common/kernels/wrap_fishhash8_msl.inc
    ${CMAKE_CURRENT_SOURCE_DIR}/../common/kernels/igm_autolykos.inc ${CMAKE_CURRENT_SOURCE_DIR}/../common/kernels/wrap_autolykos_msl.inc
    ${CMAKE_CURRENT_SOURCE_DIR}/../common/kernels/igm_igneumhash.inc ${CMAKE_CURRENT_SOURCE_DIR}/../common/kernels/igm_igneumhash1.inc ${CMAKE_CURRENT_SOURCE_DIR}/../common/kernels/igm_r0e_access.inc ${CMAKE_CURRENT_SOURCE_DIR}/../common/kernels/igm_r0f.inc ${CMAKE_CURRENT_SOURCE_DIR}/../common/kernels/igm_r16f.inc ${CMAKE_CURRENT_SOURCE_DIR}/../common/kernels/igm_r17.inc ${CMAKE_CURRENT_SOURCE_DIR}/../common/kernels/igm_forge.inc ${CMAKE_CURRENT_SOURCE_DIR}/../common/kernels/wrap_igneumhash_msl.inc ${CMAKE_CURRENT_SOURCE_DIR}/../common/kernels/igm_rxe1.inc ${CMAKE_CURRENT_SOURCE_DIR}/../common/kernels/wrap_rxe1_msl.inc)
add_custom_command(OUTPUT ${CMAKE_CURRENT_BINARY_DIR}/kernels_embedded.cpp
    COMMAND python3 ${CMAKE_CURRENT_SOURCE_DIR}/../common/tools_embed_kernels.py ${CMAKE_CURRENT_BINARY_DIR}/kernels_embedded.cpp ${SHADER_FILES}
    DEPENDS ${SHADER_FILES})
add_executable(igneum-worker-metal src/main.mm src/device.mm src/engine.mm src/engine_x.mm ${CMAKE_CURRENT_BINARY_DIR}/kernels_embedded.cpp)
target_include_directories(igneum-worker-metal PRIVATE src)
target_compile_options(igneum-worker-metal PRIVATE -Wall -Wextra -fobjc-arc)
target_link_libraries(igneum-worker-metal PRIVATE igm_common ${IGNEUM_REF_LIBS}
    "-framework Metal" "-framework Foundation" "-framework AppKit" "-framework IOKit" "-framework CoreFoundation")

add_custom_command(TARGET igneum-worker-metal POST_BUILD
    COMMAND ${CMAKE_COMMAND} -E make_directory $<TARGET_FILE_DIR:igneum-worker-metal>/shaders
    COMMAND ${CMAKE_COMMAND} -E copy ${CMAKE_CURRENT_SOURCE_DIR}/shaders/igm_progpow.metal ${CMAKE_CURRENT_SOURCE_DIR}/shaders/igm_dag.metal
            ${CMAKE_CURRENT_SOURCE_DIR}/../common/kernels/prelude_msl.inc ${CMAKE_CURRENT_SOURCE_DIR}/../common/kernels/igm_fishhash.inc ${CMAKE_CURRENT_SOURCE_DIR}/../common/kernels/wrap_fishhash_msl.inc
            ${CMAKE_CURRENT_SOURCE_DIR}/../common/kernels/igm_autolykos.inc ${CMAKE_CURRENT_SOURCE_DIR}/../common/kernels/wrap_autolykos_msl.inc
            ${CMAKE_CURRENT_SOURCE_DIR}/../common/kernels/igm_igneumhash.inc ${CMAKE_CURRENT_SOURCE_DIR}/../common/kernels/igm_igneumhash1.inc ${CMAKE_CURRENT_SOURCE_DIR}/../common/kernels/igm_r0e_access.inc ${CMAKE_CURRENT_SOURCE_DIR}/../common/kernels/igm_r0f.inc ${CMAKE_CURRENT_SOURCE_DIR}/../common/kernels/igm_r16f.inc ${CMAKE_CURRENT_SOURCE_DIR}/../common/kernels/igm_r17.inc ${CMAKE_CURRENT_SOURCE_DIR}/../common/kernels/igm_forge.inc ${CMAKE_CURRENT_SOURCE_DIR}/../common/kernels/wrap_igneumhash_msl.inc ${CMAKE_CURRENT_SOURCE_DIR}/../common/kernels/igm_rxe1.inc ${CMAKE_CURRENT_SOURCE_DIR}/../common/kernels/wrap_rxe1_msl.inc
            $<TARGET_FILE_DIR:igneum-worker-metal>/shaders/)

# Signing (DESIGN.md 3.9) belongs to the release pipeline:
#   codesign --force --options runtime --timestamp --sign "Developer ID Application: <team>" igneum-worker-metal
