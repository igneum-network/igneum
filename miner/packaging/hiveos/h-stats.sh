#!/usr/bin/env bash
# HiveOS stats from Ember's own /metrics: hashrate in kH/s per card, temperatures, accepted and rejected.
m=$(curl -s --max-time 3 http://127.0.0.1:4068/metrics 2>/dev/null)
if [ -z "$m" ]; then khs=0; stats='{"hs":[0],"hs_units":"khs","temp":[],"fan":[],"uptime":0,"ver":"'$(cat VERSION 2>/dev/null)'","ar":[0,0],"algo":"igneumhash","bus_numbers":[]}'; return 0 2>/dev/null || exit 0; fi
g() { echo "$m" | awk -v k="$1" '$1==k {print $2; exit}'; }
hps=$(g igneum_validated_hps); acc=$(g igneum_accepted_shares_total); rej=$(g igneum_rejected_shares_total); up=$(g igneum_uptime_seconds)
hs=$(echo "$m" | awk '$1 ~ /^igneum_device_validated_hps\{/ {printf "%s%.1f", (n++?",":""), $2/1000}')
temps=$(echo "$m" | awk '$1 ~ /^igneum_device_temp_celsius\{/ {printf "%s%d", (n++?",":""), $2}')
fans=""
bus=""
khs=$(awk -v h="${hps:-0}" 'BEGIN{printf "%.1f", h/1000}')
stats=$(printf '{"hs":[%s],"hs_units":"khs","temp":[%s],"fan":[%s],"uptime":%d,"ver":"%s","ar":[%d,%d],"algo":"igneumhash","bus_numbers":[%s]}' "${hs:-$khs}" "$temps" "$fans" "${up:-0}" "$(cat VERSION 2>/dev/null)" "${acc:-0}" "${rej:-0}" "$bus")
