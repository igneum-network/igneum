cmake_minimum_required(VERSION 3.16)
project(ignpow C CXX)

set(CMAKE_C_STANDARD 11)
set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
if(NOT CMAKE_BUILD_TYPE)
  set(CMAKE_BUILD_TYPE Release)
endif()

set(THIRD ${CMAKE_CURRENT_SOURCE_DIR}/../third_party)
set(RVN ${THIRD}/ravencoin-ethash-22549129)
set(B3 ${THIRD}/blake3-1.8.2)

# The build commit is written at BUILD time into ${CMAKE_CURRENT_BINARY_DIR}/ignpow_build_commit.h by cmake/build_commit.cmake
# (the release team's recipe fault 1, 12 Oct 2026: the configure-time read named every later build after the first commit).
add_custom_target(ignpow_commit
  COMMAND ${CMAKE_COMMAND} -DOUT=${CMAKE_CURRENT_BINARY_DIR}/ignpow_build_commit.h -DSRC=${CMAKE_CURRENT_SOURCE_DIR} -P ${CMAKE_CURRENT_SOURCE_DIR}/cmake/build_commit.cmake
  BYPRODUCTS ${CMAKE_CURRENT_BINARY_DIR}/ignpow_build_commit.h
  VERBATIM)

# The vendored Ravencoin ethash/ProgPoW library at the pinned commit.
# managed.cpp is not built (it needs Ravencoin's sync.h); the reference owns its contexts.
add_library(rvn_ethash STATIC
  ${RVN}/crypto/ethash/lib/ethash/ethash.cpp
  ${RVN}/crypto/ethash/lib/ethash/progpow.cpp
  ${RVN}/crypto/ethash/lib/ethash/primes.c
  ${RVN}/crypto/ethash/lib/keccak/keccak.c
  ${RVN}/crypto/ethash/lib/keccak/keccakf800.c
  ${RVN}/crypto/ethash/lib/keccak/keccakf1600.c)
target_include_directories(rvn_ethash PUBLIC ${RVN} ${RVN}/crypto/ethash/lib/ethash)
target_compile_options(rvn_ethash PRIVATE -O2 -fno-strict-aliasing)

# The official BLAKE3 C implementation at tag 1.8.2 (PIN beside it); on x86_64 the four intrinsics paths (SSE2, SSE4.1, AVX2,
# AVX-512; the PIN's note of 15 October 2026) selected at run time by the dispatcher, the portable path everywhere else and as
# the fallback. The same BLAKE3 on every path (the self-test's vectors and the sets re-read exact).
set(B3_SRC ${B3}/blake3.c ${B3}/blake3_dispatch.c ${B3}/blake3_portable.c)
if(CMAKE_SYSTEM_PROCESSOR MATCHES "x86_64|AMD64|amd64")
  list(APPEND B3_SRC ${B3}/blake3_sse2.c ${B3}/blake3_sse41.c ${B3}/blake3_avx2.c ${B3}/blake3_avx512.c)
  set_source_files_properties(${B3}/blake3_sse2.c PROPERTIES COMPILE_OPTIONS "-msse2")
  set_source_files_properties(${B3}/blake3_sse41.c PROPERTIES COMPILE_OPTIONS "-msse4.1")
  set_source_files_properties(${B3}/blake3_avx2.c PROPERTIES COMPILE_OPTIONS "-mavx2")
  set_source_files_properties(${B3}/blake3_avx512.c PROPERTIES COMPILE_OPTIONS "-mavx512f;-mavx512vl")
  set(B3_DEFS BLAKE3_USE_NEON=0)
else()
  set(B3_DEFS BLAKE3_NO_SSE2 BLAKE3_NO_SSE41 BLAKE3_NO_AVX2 BLAKE3_NO_AVX512 BLAKE3_USE_NEON=0)
endif()
add_library(blake3 STATIC ${B3_SRC})
target_include_directories(blake3 PUBLIC ${B3})
target_compile_definitions(blake3 PUBLIC ${B3_DEFS})
target_compile_options(blake3 PRIVATE -O2)

add_library(ignpow STATIC src/ignpow.cpp src/kawpow.cpp src/autolykos.cpp src/igneumhash.cpp src/rxe1.cpp src/compactproof.cpp src/forgeb1.cpp src/progpow_epic.cpp src/cuckaroo29.cpp src/cuckaroo28.cpp src/cuckaroo28k.cpp src/play.cpp src/qpow_quantus.cpp ${THIRD}/cuckaroo29-5585282/src/src/crypto/blake2b-ref.c)
target_include_directories(ignpow PUBLIC include PRIVATE src)
target_compile_options(ignpow PRIVATE -ffp-contract=off)  # version 8 option (b): the product and the sum rounded separately (SPEC 0.8)
target_include_directories(ignpow PRIVATE ${THIRD}/cuckaroo29-5585282/src/src)  # tromp's cuckaroo headers, included by cuckaroo29.cpp only
target_link_libraries(ignpow PUBLIC rvn_ethash blake3)
target_include_directories(ignpow PRIVATE ${CMAKE_CURRENT_BINARY_DIR})
add_dependencies(ignpow ignpow_commit)
target_compile_definitions(ignpow PRIVATE IGNPOW_BUILD_COMMIT_HEADER=1)

# C2 FishHash: measure-only from the manifest-fetched upstream (D-GA-04):
#   bash pow/third_party/fetch.sh fish-hash-823531c
#   cmake -S pow/ref -B <dir> -DIGNPOW_FISHHASH_SRC=<repo>/pow/third_party/fish-hash-823531c/src
set(IGNPOW_FISHHASH_SRC "" CACHE PATH "path to the fetched fish-hash src root, the directory holding cpp/ (empty: C2 not built)")
if(IGNPOW_FISHHASH_SRC)
  # the upstream FishHash.cpp includes its keccak.c and blake3.c, and blake3.c its dispatch and portable files: one translation unit
  target_sources(ignpow PRIVATE src/fishhash_control.cpp)
  target_include_directories(ignpow PRIVATE ${IGNPOW_FISHHASH_SRC})
  target_compile_definitions(ignpow PRIVATE IGNPOW_HAVE_FISHHASH=1)
  # the upstream's C files are compiled inside its C++ translation unit; GCC needs -fpermissive for their enum arithmetic
  set_source_files_properties(src/fishhash_control.cpp PROPERTIES COMPILE_OPTIONS "-fpermissive;-w" COMPILE_DEFINITIONS "BLAKE3_NO_SSE2;BLAKE3_NO_SSE41;BLAKE3_NO_AVX2;BLAKE3_NO_AVX512")  # its own included blake3.c stays on the portable path
endif()
target_compile_options(ignpow PRIVATE -O2 -Wall -Wextra)
# IGNPOW_NATIVE=ON: the host's vector set (-march=native) for the verifier's timing rows and a node's own build; the bits are the
# same with or without it (the reference's FMA is std::fma per lane, exact), the sets of record are cut with it OFF
option(IGNPOW_NATIVE "build the reference and its tools for the host's vector set" OFF)
if(IGNPOW_NATIVE)
  target_compile_options(ignpow PRIVATE -march=native -O3)
endif()
# IGNPOW_VNNI=ON: the version 14 tile on AVX-512 VNNI (vpdpbusd), exact against the scalar; the sets of record are cut with it OFF and
# check it ON; an ARM build takes NEON sdot through -march=armv8.2-a+dotprod (or native) the same way
option(IGNPOW_OPERAND_STATS "a research build: the version 14 tile's operand statistics (never for the sets or a node)" OFF)
if(IGNPOW_OPERAND_STATS)
  target_compile_definitions(ignpow PRIVATE IGNH_OPERAND_STATS=1)
endif()
option(IGNPOW_VNNI "build the version 14 tile on AVX-512 VNNI" OFF)
if(IGNPOW_VNNI)
  target_compile_options(ignpow PRIVATE -mavx2 -mavx512f -mavx512bw -mavx512vl -mavx512vnni -mavx512vbmi -O3)  # the EVEX form only: -mavxvnni's VEX form faulted (illegal instruction) on the x86 builder's avx512_vnni cores
endif()

find_package(Threads REQUIRED)
# BeamHash III control (decision-6): measure-only from the manifest-fetched upstream (Beam 7.5.14493, Apache 2.0):
#   bash pow/third_party/fetch.sh beamhash3-7.5.14493
#   cmake -S pow/ref -B <dir> -DIGNPOW_BEAMHASH3_SRC=<repo>/pow/third_party/beamhash3-7.5.14493/src
set(IGNPOW_BEAMHASH3_SRC "" CACHE PATH "path to the fetched Beam src root, the directory holding 3rdparty/ (empty: the control is not built)")
if(IGNPOW_BEAMHASH3_SRC)
  set(BH3 ${IGNPOW_BEAMHASH3_SRC}/3rdparty/crypto)
  add_executable(beamhash3-control tools/beamhash3-control.cpp ${BH3}/beamHashIII_impl.cpp ${BH3}/blake/ref/blake2b-ref.c)  # beamHashIII_impl.cpp carries its own index packing; equihashR_impl.cpp (boost, compat) is not needed
  target_include_directories(beamhash3-control PRIVATE ${BH3} ${IGNPOW_BEAMHASH3_SRC}/3rdparty)
  target_compile_definitions(beamhash3-control PRIVATE ENABLE_MINING=1)
  target_compile_options(beamhash3-control PRIVATE -O2 -w)
  target_link_libraries(beamhash3-control Threads::Threads)
endif()

add_executable(ignh-bench tools/ignh-bench.cpp)
if(IGNPOW_OPERAND_STATS)
  target_compile_definitions(ignh-bench PRIVATE IGNH_OPERAND_STATS=1)
endif()  # the per-header cost of one document on one core: '<params json>' N [full]
target_include_directories(ignh-bench PRIVATE include)
target_link_libraries(ignh-bench PRIVATE ignpow)
add_executable(ignpow-vectors tools/ignpow-vectors.cpp)
target_link_libraries(ignpow-vectors ignpow Threads::Threads)
target_compile_options(ignpow-vectors PRIVATE -O2 -Wall -Wextra)

# The reference oracle of SPEC-R0A-v0.md section 0.9.2 (the split-execution harness: one oracle text for the renter and the rate half).
add_executable(bond-oracle tools/bond-oracle.cpp)
target_link_libraries(bond-oracle ignpow Threads::Threads)
target_compile_options(bond-oracle PRIVATE -O2 -Wall -Wextra)

# The RXE1 spec-form vectors (SPEC-TEXT-v0.1.md section 12) from the explicit harness inputs.
add_executable(rxe1-vectors tools/rxe1-vectors.cpp)
target_link_libraries(rxe1-vectors ignpow Threads::Threads)
target_compile_options(rxe1-vectors PRIVATE -O2 -Wall -Wextra)

# The compact-proof research reference's vectors and measurements (docs/igneumhash/research/COMPACTPROOF-NOTES.md).
add_executable(compactproof-bench tools/compactproof-bench.cpp)
target_link_libraries(compactproof-bench ignpow Threads::Threads)
target_compile_options(compactproof-bench PRIVATE -O2 -Wall -Wextra)

# Forge F1 under B1 (docs/igneumhash/forge/F1-B1.md): vectors and measurements.
add_executable(kiln-verify-bench tools/kiln-verify-bench.cpp)
target_link_libraries(kiln-verify-bench ignpow Threads::Threads)
add_executable(forgeb1-bench tools/forgeb1-bench.cpp)
target_link_libraries(forgeb1-bench ignpow Threads::Threads)
target_compile_options(forgeb1-bench PRIVATE -O2 -Wall -Wextra)

# The Mosaic-VF lane portfolio's identifiers (docs/igneumhash/mosaic/LANES.md) from the lane documents.
add_executable(mosaic-lanes tools/mosaic-lanes.cpp)
target_link_libraries(mosaic-lanes ignpow Threads::Threads)
target_compile_options(mosaic-lanes PRIVATE -O2 -Wall -Wextra)

# Mosaic-VF v0.2 gate 0 (docs/igneumhash/mosaic/v0.2/GATE0-VECTORS.md): the quota score, transition, reservoir and overflow vectors.
add_executable(mosaic-gate0 tools/mosaic-gate0.cpp)
target_compile_options(mosaic-gate0 PRIVATE -O2 -Wall -Wextra)

# The Tari Cuckaroo29 control's solver and verifier sides (docs/igneumhash/controls/SOURCES.md).
add_executable(cuckaroo29-vectors tools/cuckaroo29-vectors.cpp)
target_link_libraries(cuckaroo29-vectors ignpow Threads::Threads)
target_compile_options(cuckaroo29-vectors PRIVATE -O2 -Wall -Wextra)

# The install carries every static library a linker needs beside the headers
# (lib/libignpow.a, lib/librvn_ethash.a, lib/libblake3.a; include/ignpow.h, include/igneumhash.h).
install(TARGETS ignpow rvn_ethash blake3 ignpow-vectors rxe1-vectors compactproof-bench forgeb1-bench mosaic-lanes mosaic-gate0 cuckaroo29-vectors)
install(FILES include/ignpow.h include/igneumhash.h include/rxe1.h include/compactproof.h include/forgeb1.h include/controls.h DESTINATION include)
