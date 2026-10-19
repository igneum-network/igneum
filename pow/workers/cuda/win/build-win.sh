#!/bin/bash
# build-win.sh <repo> <out dir> [nvrtc dll dir]: the Windows worker and reference by MinGW cross-build on a box.
# Produces igneum-worker-cuda.exe (with the two NVRTC DLLs appended as a tail it unpacks beside itself at
# start, so the bench agent's single-file fetch is enough) and ignpow-vectors.exe. The CUDA driver, NVRTC and
# NVML are bound at run time (src/cuda_dyn.hpp); only the toolkit headers are needed here.
set -eu
REPO=${1:?repo}
OUT=${2:?out dir}
DLLS=${3:-}
CUDA=${CUDA_HOME:-/usr/local/cuda}
CXX=x86_64-w64-mingw32-g++
CC=x86_64-w64-mingw32-gcc
mkdir -p "$OUT/obj"
RVN=$REPO/pow/third_party/ravencoin-ethash-22549129
FLAGS="-O2 -std=c++17 -static -static-libgcc -static-libstdc++ -D_WIN32_WINNT=0x0601"
B3=$REPO/pow/third_party/blake3-1.8.2
INC="-I$REPO/pow/ref/include -I$REPO/pow/ref/src -I$RVN -I$RVN/crypto/ethash/lib/ethash -I$B3 -I$CUDA/include"
B3FLAGS="-DBLAKE3_NO_SSE2 -DBLAKE3_NO_SSE41 -DBLAKE3_NO_AVX2 -DBLAKE3_NO_AVX512 -DBLAKE3_USE_NEON=0"
# the vendored ethash library
for f in ethash.cpp progpow.cpp; do $CXX $FLAGS -fno-strict-aliasing $INC -c $RVN/crypto/ethash/lib/ethash/$f -o $OUT/obj/$f.o; done
for f in primes.c; do $CC -O2 -static $INC -c $RVN/crypto/ethash/lib/ethash/$f -o $OUT/obj/$f.o; done
for f in keccak.c keccakf800.c keccakf1600.c; do $CC -O2 -static $INC -c $RVN/crypto/ethash/lib/keccak/$f -o $OUT/obj/$f.o; done
# the vendored BLAKE3 (portable only, as pow/ref/CMakeLists.txt builds it)
for f in blake3.c blake3_dispatch.c blake3_portable.c; do $CC -O2 -static $B3FLAGS -I$B3 -c $B3/$f -o $OUT/obj/$f.o; done
# as an archive, so a FishHash unit that carries its own blake3.c (one translation unit upstream) wins the symbols, as in the CMake build
rm -f "$OUT/obj/libblake3.a"; x86_64-w64-mingw32-ar rcs "$OUT/obj/libblake3.a" "$OUT/obj/blake3.c.o" "$OUT/obj/blake3_dispatch.c.o" "$OUT/obj/blake3_portable.c.o"
B3OBJ="$OUT/obj/libblake3.a"
# the reference (C1, C3 and C4; C2 when the fetched tree is present)
REFSRC="$REPO/pow/ref/src/ignpow.cpp $REPO/pow/ref/src/kawpow.cpp $REPO/pow/ref/src/autolykos.cpp $REPO/pow/ref/src/igneumhash.cpp $REPO/pow/ref/src/rxe1.cpp"
# the candidates main added on 10 Oct 2026 (compactproof, forgeb1, the controls progpow-epic, cuckaroo29-tari, qpow-quantus)
# play, cuckaroo28, cuckaroo28k: the reference library's sources since 14 October 2026 (pow/ref/CMakeLists.txt lists them; ignpow.cpp references them; the Windows CUDA worker 2ce685f5 of 15 October needed them)
for f in compactproof forgeb1 progpow_epic cuckaroo29 qpow_quantus play cuckaroo28 cuckaroo28k; do [ -f "$REPO/pow/ref/src/$f.cpp" ] && REFSRC="$REFSRC $REPO/pow/ref/src/$f.cpp"; done
CK="$REPO/pow/third_party/cuckaroo29-5585282/src/src"
CKFLAGS=""; CKSRC=""; [ -f "$CK/cuckaroo/cuckaroo.hpp" ] && { CKFLAGS="-I$CK"; CKSRC="$CK/crypto/blake2b-ref.c"; }
FH=$REPO/pow/third_party/fish-hash-823531c/src
FHFLAGS=""
if [ -d "$FH/cpp" ]; then REFSRC="$REFSRC $REPO/pow/ref/src/fishhash_control.cpp"; FHFLAGS="-DIGNPOW_HAVE_FISHHASH=1 -I$FH"; fi
COMMIT=$(git -C "$REPO" rev-parse --short=8 HEAD 2>/dev/null || echo unknown)
for f in $REFSRC; do
  extra=""; case "$f" in *fishhash_control.cpp) extra="-fpermissive -w";; *cuckaroo29.cpp|*cuckaroo28.cpp|*cuckaroo28k.cpp) extra="$CKFLAGS -w";; esac
  $CXX $FLAGS $INC $FHFLAGS $B3FLAGS $extra -DIGNPOW_BUILD_COMMIT="\"$COMMIT\"" -c "$f" -o "$OUT/obj/$(basename "$f").o"
done
CKOBJ=""; [ -n "$CKSRC" ] && { $CC -O2 -static $CKFLAGS -c "$CKSRC" -o "$OUT/obj/blake2b-ref.c.o"; CKOBJ="$OUT/obj/blake2b-ref.c.o"; }
$CXX $FLAGS $INC "$REPO/pow/ref/tools/ignpow-vectors.cpp" -c -o "$OUT/obj/ignpow-vectors.o"
$CXX $FLAGS -o "$OUT/ignpow-vectors.exe" "$OUT/obj/ignpow-vectors.o" $(for f in $REFSRC; do echo "$OUT/obj/$(basename "$f").o"; done) \
  "$OUT"/obj/{ethash.cpp,progpow.cpp,primes.c,keccak.c,keccakf800.c,keccakf1600.c}.o $CKOBJ $B3OBJ -lpthread
# the worker: the embedded kernel text and keccak sources as the CMake build makes them
W=$REPO/pow/workers/cuda
python3 - "$RVN/crypto/ethash/lib/keccak" "$W/src" "$OUT/obj/embedded.hpp" <<'EOF'
import re, sys
kdir, sdir, out = sys.argv[1:4]
def strip(p): return re.sub(r"#include[^\n]*\n", "", open(p).read())
k800, k1600 = strip(kdir + "/keccakf800.c"), strip(kdir + "/keccakf1600.c")
kern = open(sdir + "/kernels.cu.txt").read(); al = open(sdir + "/kernels_autolykos.cu.txt").read(); fh = open(sdir + "/kernels_fishhash.cu.txt").read(); ih = open(sdir + "/kernels_igneumhash.cu.txt").read(); rx = open(sdir + "/kernels_rxe1.cu.txt").read(); rx3 = open(sdir + "/kernels_rxe3.cu.txt").read()
open(out, "w").write('#pragma once\nstatic const char* const kEmbeddedKeccakf800 = R"EMB(%s)EMB";\nstatic const char* const kEmbeddedKeccakf1600 = R"EMB(%s)EMB";\nstatic const char* const kEmbeddedKernels = R"EMB(%s)EMB";\nstatic const char* const kEmbeddedKernelsAutolykos = R"EMB(%s)EMB";\nstatic const char* const kEmbeddedKernelsFishhash = R"EMB(%s)EMB";\nstatic const char* const kEmbeddedKernelsIgneumhash = R"EMB(%s)EMB";\nstatic const char* const kEmbeddedKernelsRxe1 = R"EMB(%s)EMB";\nstatic const char* const kEmbeddedKernelsRxe3 = R"EMB(%s)EMB";\n' % (k800, k1600, kern, al, fh, ih, rx, rx3))
EOF
for f in main.cpp engine.cpp codegen.cpp; do $CXX $FLAGS $INC -I"$OUT/obj" -c "$W/src/$f" -o "$OUT/obj/w-$f.o"; done
$CXX $FLAGS -o "$OUT/igneum-worker-cuda.exe" "$OUT"/obj/w-{main,engine,codegen}.cpp.o $(for f in $REFSRC; do echo "$OUT/obj/$(basename "$f").o"; done) \
  "$OUT"/obj/{ethash.cpp,progpow.cpp,primes.c,keccak.c,keccakf800.c,keccakf1600.c}.o $CKOBJ $B3OBJ -lpthread
# the NVRTC DLLs appended as the tail (see cuda_dyn.hpp unpack_tail)
if [ -n "$DLLS" ]; then
  python3 - "$OUT/igneum-worker-cuda.exe" "$DLLS/nvrtc64_120_0.dll" "$DLLS/nvrtc-builtins64_129.dll" <<'EOF'
import os, sys
exe, *files = sys.argv[1:]
with open(exe, "ab") as o:
    table = "IGNTAIL1\n"
    for f in files:
        data = open(f, "rb").read()
        o.write(data)
        table += "%s %d\n" % (os.path.basename(f), len(data))
    t = table.encode()
    o.write(t)
    o.write(("%24d" % len(t)).encode() + b"IGNTAIL1")
print("tail appended:", [os.path.basename(f) for f in files])
EOF
fi
ls -la "$OUT"/*.exe
sha256sum "$OUT"/*.exe
