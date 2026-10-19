#!/usr/bin/env bash
cd "$(dirname "$0")"
[ -t 1 ] && . colors
mkdir -p state state/worker-cache
export IGNEUM_WORKER_CACHE=$PWD/state/worker-cache
exec ./ember mine --config "$CUSTOM_CONFIG_FILENAME" --every 30 2>&1 | tee "$CUSTOM_LOG_BASENAME.log"
