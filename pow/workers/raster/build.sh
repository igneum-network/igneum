#!/bin/bash
# build.sh <out dir> (RP_REF_LINUX = the reference library for 18c's keyed dataset on Linux, e.g. "-L<ref build> -lignpow -lblake3 -lrvn_ethash"; RP_REF_WIN = the mingw reference, e.g. "-L<win build>/obj -lignpow", else the Windows worker carries no keyed target): igneum-raster-probe for Linux (EGL surfaceless, Mesa) and Windows (WGL, MinGW cross) from this tree; the ref's BLAKE3 from the workers' common build
set -e; W=$(cd "$(dirname "$0")/.." && pwd); O=${1:?out dir}; mkdir -p "$O"
INC="-I$W/raster -I$W/common -I$W/../ref -I$W/../ref/include"
g++ -O2 -std=c++17 $INC "$W/raster/raster_probe_gl.cpp" $RP_LIBS_LINUX -lEGL -lGL -lpthread -ldl -o "$O/igneum-raster-probe" && echo "linux $(sha256sum "$O/igneum-raster-probe" | cut -c1-16)"
g++ -O2 -std=c++17 $INC "$W/raster/raster_worker_gl.cpp" $RP_LIBS_LINUX $RP_REF_LINUX -lEGL -lGL -lpthread -ldl -o "$O/igneum-raster-worker" && echo "linux worker $(sha256sum "$O/igneum-raster-worker" | cut -c1-16)"
x86_64-w64-mingw32-g++ -O2 -std=c++17 -static $INC "$W/raster/raster_probe_gl.cpp" $RP_SRC_WIN $RP_LIBS_WIN -lopengl32 -lgdi32 -o "$O/igneum-raster-probe.exe" && echo "windows $(sha256sum "$O/igneum-raster-probe.exe" | cut -c1-16)"
x86_64-w64-mingw32-g++ -O2 -std=c++17 -static ${RP_REF_WIN:--DR18_NO_KEYED} $INC "$W/raster/raster_worker_gl.cpp" "$W/common/ih_params.cpp" "$W/common/json.cpp" $RP_SRC_WIN $RP_LIBS_WIN -lopengl32 -lgdi32 -o "$O/igneum-raster-worker.exe" && echo "windows worker $(sha256sum "$O/igneum-raster-worker.exe" | cut -c1-16)"
