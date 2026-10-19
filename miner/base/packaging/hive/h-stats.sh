#!/usr/bin/env bash
# Ember for HiveOS: the stats Hive polls. Sourced by Hive (so: no exit); sets $khs (total kH/s) and $stats
# (JSON: hs per GPU in kH/s, temp, fan, uptime, ver, ar = [accepted, rejected], algo, bus_numbers). The per-GPU numbers
# come from the miners' own STATUS lines ($CUSTOM_LOG_BASENAME.gpu<N>.log, "now=<MH/s> MH/s"); accepted, rejected and
# dev-fee blocks are the cumulative counters of the same line. Temperatures and fans come from Hive's gpu-stats when it
# exists (its arrays follow Hive's GPU order; NVIDIA first here as on Hive), else nvidia-smi, else empty.
[[ -z "$CUSTOM_LOG_BASENAME" ]] && . "$(dirname "${BASH_SOURCE[0]}")/h-manifest.conf"
_hdir="$(dirname "${BASH_SOURCE[0]}")"
_ver="$(head -1 "$_hdir/version.txt" 2>/dev/null | awk '{print $2}')"; _ver="${_ver:-$CUSTOM_VERSION}"
_hs=(); _acc=0; _rej=0; _fee=0; _total_khs=0
for _f in $(ls "$CUSTOM_LOG_BASENAME".gpu*.log 2>/dev/null | sort -t u -k 3 -n); do
  _line="$(grep ' STATUS ' "$_f" 2>/dev/null | tail -1)"
  _mh="$(printf '%s' "$_line" | sed -n 's/.* now=\([0-9.]*\) MH\/s.*/\1/p')"
  [[ -z "$_mh" ]] && _mh="$(printf '%s' "$_line" | sed -n 's/.* hash=\([0-9.]*\) MH\/s.*/\1/p')"
  [[ -z "$_mh" ]] && _mh=0
  _k="$(awk -v m="$_mh" 'BEGIN{printf "%.1f", m*1000}')"
  _hs+=("$_k")
  _total_khs="$(awk -v a="$_total_khs" -v b="$_k" 'BEGIN{printf "%.1f", a+b}')"
  _a="$(printf '%s' "$_line" | sed -n 's/.* accepted=\([0-9]*\).*/\1/p')"; _acc=$((_acc + ${_a:-0}))
  _r="$(printf '%s' "$_line" | sed -n 's/.* rejected=\([0-9]*\).*/\1/p')"; _rej=$((_rej + ${_r:-0}))
  _d="$(printf '%s' "$_line" | sed -n 's/.* fee=\([0-9]*\).*/\1/p')"; _fee=$((_fee + ${_d:-0}))
done
_n=${#_hs[@]}
_temp="[]"; _fan="[]"; _bus="[]"
if command -v gpu-stats >/dev/null 2>&1 && command -v jq >/dev/null 2>&1; then
  _gs="$(gpu-stats 2>/dev/null)"
  _temp="$(jq -c ".temp[0:$_n] // []" <<< "$_gs" 2>/dev/null || echo '[]')"
  _fan="$(jq -c ".fan[0:$_n] // []" <<< "$_gs" 2>/dev/null || echo '[]')"
  _bus="[$(jq -r ".busids[0:$_n][]? | split(\":\")[0]" <<< "$_gs" 2>/dev/null | while read -r _b; do printf '%d\n' "0x$_b" 2>/dev/null; done | paste -sd, -)]"
elif command -v nvidia-smi >/dev/null 2>&1; then
  _temp="[$(nvidia-smi --query-gpu=temperature.gpu --format=csv,noheader,nounits 2>/dev/null | head -n "$_n" | paste -sd, -)]"
  _fan="[$(nvidia-smi --query-gpu=fan.speed --format=csv,noheader,nounits 2>/dev/null | head -n "$_n" | sed 's/\[N\/A\]/0/' | paste -sd, -)]"
  _bus="[$(nvidia-smi --query-gpu=pci.bus_id --format=csv,noheader 2>/dev/null | head -n "$_n" | awk -F: '{printf "%d\n", strtonum("0x"$2)}' | paste -sd, -)]"
fi
_started="$(cat "$CUSTOM_LOG_BASENAME.started" 2>/dev/null)"; _up=0
[[ -n "$_started" ]] && _up=$(( $(date +%s) - _started ))
_hs_json="[$(IFS=,; printf '%s' "${_hs[*]}")]"
khs="$_total_khs"
stats="{\"hs\":$_hs_json,\"hs_units\":\"khs\",\"temp\":$_temp,\"fan\":$_fan,\"uptime\":$_up,\"ver\":\"$_ver\",\"ar\":[$_acc,$_rej],\"algo\":\"igneum\",\"bus_numbers\":$_bus,\"dev_fee_blocks\":$_fee}"
