#!/bin/bash
# build-windows.sh [repo] [out dir]: cross-builds A's reference and the OpenCL worker for
# Windows x86_64 on a Linux box with mingw-w64 (never on the Mac): a single static
# igneum-worker-opencl.exe with the kernels embedded, resolving OpenCL.dll at run time.
# Prints the sha256 the bench job names. GPU engineer B, 10 October 2026.
set -eu
REPO=${1:?the repository tree (the first argument)}
OUT=${2:?the output directory (the second argument)}
W=${IGNEUM_WORKERS:-$REPO/pow/workers}
CXX=x86_64-w64-mingw32-g++-posix
CC=x86_64-w64-mingw32-gcc-posix
RVN=$REPO/pow/third_party/ravencoin-ethash-22549129
REF=$REPO/pow/ref
FHROOT=$REPO/pow/third_party/fish-hash-823531c/src
mkdir -p "$OUT/obj" "$OUT/CL"
# the Khronos headers from the box's opencl-headers package, copied so no Linux system header is reached
cp -r /usr/include/CL/. "$OUT/CL/"
cd "$OUT/obj"
COMMIT=${IGNEUM_BUILD_COMMIT:-$(cd "$REPO" && git rev-parse --short=8 HEAD 2>/dev/null || echo unknown)}
FHDEF=""; [ -f "$FHROOT/cpp/FishHash.cpp" ] && [ -f "$REF/src/fishhash_control.cpp" ] && FHDEF="-DIGNPOW_HAVE_FISHHASH=1"
B3=$REPO/pow/third_party/blake3-1.8.2; B3INC=""; B3SRC=""; [ -f "$B3/blake3.c" ] && { B3INC="-I$B3"; B3SRC="$B3/blake3.c $B3/blake3_dispatch.c $B3/blake3_portable.c"; }
echo "reference ($COMMIT)${FHDEF:+ with FishHash}${B3SRC:+ with BLAKE3}"
if [ -n "$B3SRC" ]; then $CC -std=c11 -O2 -w -DBLAKE3_NO_SSE2 -DBLAKE3_NO_SSE41 -DBLAKE3_NO_AVX2 -DBLAKE3_NO_AVX512 -DBLAKE3_USE_NEON=0 $B3INC -c $B3SRC; fi
$CC -std=c11 -O2 -fno-strict-aliasing -w -I"$RVN" -I"$RVN/crypto/ethash/lib/ethash" -c "$RVN/crypto/ethash/lib/ethash/primes.c" "$RVN/crypto/ethash/lib/keccak/keccak.c" "$RVN/crypto/ethash/lib/keccak/keccakf800.c" "$RVN/crypto/ethash/lib/keccak/keccakf1600.c"
$CXX -std=c++17 -O2 -fno-strict-aliasing -w -I"$RVN" -I"$RVN/crypto/ethash/lib/ethash" -c "$RVN/crypto/ethash/lib/ethash/ethash.cpp" "$RVN/crypto/ethash/lib/ethash/progpow.cpp"
# the lead's controls (cuckaroo29.cpp, 10 Oct 2026) include tromp's vendored headers and link its blake2b-ref.c (pow/ref/CMakeLists.txt lines 40 to 42)
C29=$REPO/pow/third_party/cuckaroo29-5585282/src/src; C29INC=""; [ -d "$C29/cuckaroo" ] && { C29INC="-I$C29"; $CC -std=c11 -O2 -w -c "$C29/crypto/blake2b-ref.c"; }
$CXX -std=c++17 -O2 -w -DIGNPOW_BUILD_COMMIT="\"$COMMIT\"" $FHDEF $B3INC $C29INC -I"$FHROOT" -I"$RVN" -I"$RVN/crypto/ethash/lib/ethash" -I"$REF/include" -I"$REF/src" -c $(ls "$REF"/src/*.cpp | grep -v fishhash)
if [ -n "$FHDEF" ]; then
  $CXX -std=c++17 -O2 -w -fpermissive $FHDEF -I"$FHROOT" -I"$RVN" -I"$RVN/crypto/ethash/lib/ethash" -I"$REF/include" -I"$REF/src" -c "$REF/src/fishhash_control.cpp"
  # the upstream FishHash unit carries its own BLAKE3; its global blake3_* symbols are prefixed so they never
  # collide with the vendored BLAKE3 1.8.2 the IgneumHash reference links (GNU ld pulls both objects)
  x86_64-w64-mingw32-nm fishhash_control.o | awk '$2 ~ /^[TDRB]$/ && $3 ~ /^blake3_/ {print $3" fh_"$3}' > fh-redef.txt
  x86_64-w64-mingw32-objcopy --redefine-syms=fh-redef.txt fishhash_control.o
fi
x86_64-w64-mingw32-ar rcs libignpow.a ./*.o
ADLX=$REPO/pow/third_party/adlx-1ca2dd42/src
[ -f "$ADLX/SDK/Include/ADLX.h" ] || bash "$REPO/pow/third_party/fetch.sh" adlx-1ca2dd42 >/dev/null
mkdir -p "$OUT/shim"; echo '#include <windows.h>' > "$OUT/shim/Windows.h"   # the ADLX headers name the Windows header capitalised; the cross toolchain's is lower case
echo "adlx probe ($(grep -m1 '^# commit:' "$REPO/pow/third_party/adlx-1ca2dd42/FETCH" | awk '{print substr($3,1,8)}'))"
$CC -std=c11 -O2 -w -I"$OUT/shim" -I"$ADLX/SDK/Include" -c "$W/opencl/src/adlx_probe.c" -o adlx_probe.o
echo "level zero probe (ze_loader.dll bound at run time; no header, no vendoring)"
$CC -std=c11 -O2 -w -c "$W/opencl/src/l0_probe.c" -o l0_probe.o
echo "kernels embedded"
python3 "$W/common/tools_embed_kernels.py" kernels_embedded.cpp "$W/opencl/kernels/igm_progpow.cl" "$W/opencl/kernels/igm_dag.cl" "$W"/common/kernels/prelude_cl.inc "$W"/common/kernels/igm_fishhash.inc "$W"/common/kernels/wrap_fishhash_cl.inc "$W"/common/kernels/igm_fishhash8.inc "$W"/common/kernels/wrap_fishhash8_cl.inc "$W"/common/kernels/igm_autolykos.inc "$W"/common/kernels/wrap_autolykos_cl.inc "$W"/common/kernels/igm_igneumhash.inc "$W"/common/kernels/igm_igneumhash1.inc "$W"/common/kernels/igm_igneumhash_ctx.inc "$W"/common/kernels/igm_r0e_access.inc "$W"/common/kernels/igm_r0f.inc "$W"/common/kernels/igm_r16f.inc "$W"/common/kernels/igm_r17.inc "$W"/common/kernels/igm_forge.inc "$W"/common/kernels/wrap_igneumhash_ctx_cl.inc "$W"/common/kernels/igm_igneumhash_bucket.inc "$W"/common/kernels/wrap_igneumhash_bucket_cl.inc "$W"/common/kernels/wrap_igneumhash_cl.inc "$W"/common/kernels/igm_igneumhash_bw.inc "$W"/common/kernels/wrap_igneumhash_bw_cl.inc "$W"/common/kernels/igm_igneumhash_x2.inc "$W"/common/kernels/igm_rxe1.inc "$W"/common/kernels/wrap_rxe1_cl.inc "$W"/common/kernels/igm_probe.cl
echo "worker"
$CXX -std=c++20 -O2 -Wall -I"$OUT" -I"$W/common" -I"$W/opencl/src" -I"$REF/include" \
  "$W"/common/json.cpp "$W"/common/protocol.cpp "$W"/common/progpow_gen.cpp "$W"/common/r0d_gen.cpp "$W"/common/vectors.cpp "$W"/common/vectors_main.cpp "$W"/common/refbridge.cpp "$W"/common/ext_stub.cpp "$W"/common/bench_main.cpp "$W"/common/kernel_source.cpp "$W"/common/ih_params.cpp "$W"/common/rxe1_params.cpp kernels_embedded.cpp \
  "$W"/opencl/src/main.cpp "$W"/opencl/src/cl_engine.cpp "$W"/opencl/src/cl_engine_x.cpp "$W"/opencl/src/cl_probe.cpp "$W"/opencl/src/cl_shim.cpp adlx_probe.o l0_probe.o \
  libignpow.a -static -static-libgcc -static-libstdc++ -lwinpthread -o "$OUT/igneum-worker-opencl.exe"
x86_64-w64-mingw32-strip "$OUT/igneum-worker-opencl.exe"
ls -la "$OUT/igneum-worker-opencl.exe"
sha256sum "$OUT/igneum-worker-opencl.exe"
