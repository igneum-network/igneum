#!/bin/bash
# The Kiln seat's pod recipe (15 Oct 2026): packages, the build of pow/ref and the Vulkan worker, the exactness run on the set, the
# sixty-second window with nvidia-smi at 1 Hz. Runs on a rented CUDA devel pod as its user in $HOME/kiln (the tree copied there).
# Usage: pod-run.sh setup | build | set <DOC> <SET> [fetch|copy] | window <DOC> [fetch|copy] [K]
set -euo pipefail
cd "$HOME/kiln"
case "${1:-}" in
  setup)
    export DEBIAN_FRONTEND=noninteractive
    apt-get update -qq && apt-get install -y -qq cmake g++ libvulkan-dev vulkan-tools libshaderc-dev glslang-tools rsync libegl1 libgl1 libglvnd0 libxext6 libx11-6 >/dev/null
    vulkaninfo --summary 2>/dev/null | grep -E "deviceName|driverVersion|apiVersion" | head -4 || echo "vulkaninfo: no device"
    ;;
  build)
    cmake -S pow/workers/kiln -B build -DCMAKE_BUILD_TYPE=Release >/dev/null && cmake --build build -j"$(nproc)" --target kiln_vk ignpow-vectors 2>&1 | grep -E "error|warning: unused|Built target" | head -20
    ls -la build/kiln_vk
    ;;
  set)
    DOC=$2; SET=$3; PATHK=${4:-fetch}
    ./build/kiln_vk --doc "$DOC" --set "$SET" --tile-path "$PATHK" 2>&1 | tee "out/set-$(basename "$SET" .json)-$PATHK.json"
    ;;
  window)
    DOC=$2; PATHK=${3:-fetch}; K=${4:-1024}; TAG="$(basename "$DOC" .json)-$PATHK-k$K"; mkdir -p out
    nvidia-smi --query-gpu=timestamp,power.draw,clocks.sm,clocks.max.sm,power.limit,temperature.gpu,utilization.gpu,clocks_event_reasons.active --format=csv,noheader -lms 1000 > "out/power-$TAG.csv" &
    SMI=$!
    ./build/kiln_vk --doc "$DOC" --seconds 60 --k "$K" --tile-path "$PATHK" 2>&1 | tee "out/window-$TAG.log"
    kill $SMI 2>/dev/null || true
    python3 pow/workers/kiln/window.py "out/window-$TAG.log" "out/power-$TAG.csv" | tee "out/row-$TAG.json"
    ;;
  *) echo "setup | build | set DOC SET [fetch|copy] | window DOC [fetch|copy] [K]"; exit 2;;
esac
