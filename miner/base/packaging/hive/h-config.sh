#!/usr/bin/env bash
# Ember for HiveOS: writes igneum.conf from the Flight Sheet. Sourced by Hive (so: return, never exit).
#   CUSTOM_URL          the node the miner mines against: grpc://host:26610 (your own igneumd), or "local" to run the
#                       bundled node on this rig (data under the miner folder, peers from PEERS or the public seed);
#                       or a POOL, pool://host:4463 (spec 09; the vote key stays this rig's): then NODE=local or
#                       NODE=grpc://host:26610 in the extra config adds a verifier node for voting and seed checks,
#                       and without one the rig hashes and does not vote (pool mode takes no software dev fee)
#   CUSTOM_TEMPLATE     the payout address: 0x followed by 40 hex (an EVM address you hold the key for), optionally
#                       followed by .workername (the label of this rig's keys; defaults to the Hive worker name)
#   CUSTOM_PASS         unused
#   CUSTOM_USER_CONFIG  extra lines, KEY=VALUE, one per line or separated by spaces:
#                       DEV_FEE=1      the miner software's dev fee in whole percent (1 block template in 100 to the dev
#                                      address); DEV_FEE=0 turns it off. The protocol itself takes no fee.
#                       IDENTITIES=auto vote keys per card: 8 for a card with 8 GB or more, else 2 (IDENTITIES=auto
#                                      applies that rule per card from nvidia-smi or the amdgpu sysfs, 8 when neither
#                                      answers; the app's rule, app/igneum-app/src/detect.rs). A number overrides it
#                                      for every card.
#                       WORKER=auto    auto | cuda | opencl (the GPU worker; auto = cuda on NVIDIA, opencl on AMD)
#                       VOTE=1         sign finality checkpoints (0 = mine without voting)
#                       PEERS=a:26611,b:26611   peers for the bundled node when CUSTOM_URL=local
#                       NODE=local     pool mode only: the verifier node (local = the bundled node, or grpc://host:26610)
#                       EXTRA="..."    appended to every igneum-miner command line
#                       OVERRIDE={...} the devnet's consensus override, one JSON object on its own line (single or
#                                      double quotes around it are fine in the Hive UI); the bundled node (CUSTOM_URL=local)
#                                      starts with --override-params-file from it. Without it the node runs on genesis
#                                      parameters and every devnet peer refuses it. Copy the live object from the
#                                      downloads page whenever it changes.
# Hive sources h-manifest.conf before this hook; the fallback is for a run outside Hive (selftest.sh)
[[ -z "$CUSTOM_CONFIG_FILENAME" ]] && . "$(dirname "${BASH_SOURCE[0]}")/h-manifest.conf"

[[ -z "$CUSTOM_TEMPLATE" ]] && echo -e "${YELLOW:-}Igneum: the wallet (CUSTOM_TEMPLATE) is empty; it must be 0x followed by 40 hex${NOCOLOR:-}" && return 1
[[ -z "$CUSTOM_URL" ]] && echo -e "${YELLOW:-}Igneum: the pool URL (CUSTOM_URL) is empty; grpc://host:26610 of your node, local, or pool://host:4463${NOCOLOR:-}" && return 1

wallet="${CUSTOM_TEMPLATE%%.*}"
label="${CUSTOM_TEMPLATE#*.}"
[[ "$label" == "$CUSTOM_TEMPLATE" || -z "$label" ]] && label="${WORKER_NAME:-rig}"
label="$(printf '%s' "$label" | tr -c 'A-Za-z0-9_-' '-' | cut -c1-32)"
if ! [[ "$wallet" =~ ^0x[0-9a-fA-F]{40}$ ]]; then
  echo -e "${YELLOW:-}Igneum: the wallet must be 0x followed by 40 hex characters, got '$wallet'${NOCOLOR:-}"
  return 1
fi

# defaults, then the user's KEY=VALUE lines
DEV_FEE=1; IDENTITIES=auto; WORKER=auto; VOTE=1; PEERS=""; EXTRA=""; OVERRIDE=""; NODE=""
while IFS= read -r line; do
  [[ -z "$line" || "$line" == \#* ]] && continue
  if [[ "$line" == OVERRIDE=* ]]; then   # the whole line: JSON may carry spaces; quotes around it are stripped
    val="${line#OVERRIDE=}"; val="${val#\'}"; val="${val%\'}"; val="${val#\"}"; val="${val%\"}"
    OVERRIDE="$val"; continue
  fi
  while read -r kv; do
    [[ -z "$kv" ]] && continue
    key="${kv%%=*}"; val="${kv#*=}"
    case "$key" in
      DEV_FEE|IDENTITIES|WORKER|VOTE|PEERS|EXTRA|NODE) printf -v "$key" '%s' "$val" ;;
      *) echo "Igneum: unknown setting '$key' ignored" ;;
    esac
  done < <(printf '%s\n' "$line" | tr ' ' '\n' | sed 's/^"//; s/"$//')
done < <(printf '%s\n' "$CUSTOM_USER_CONFIG")
if [[ -n "$OVERRIDE" && ! "$OVERRIDE" =~ ^\{.*\}$ ]]; then
  echo -e "${YELLOW:-}Igneum: OVERRIDE must be one JSON object, {\"...\":N,...} from the downloads page; got '${OVERRIDE:0:40}'${NOCOLOR:-}"
  return 1
fi
[[ "$DEV_FEE" =~ ^[0-9]+$ ]] || DEV_FEE=1
[[ "$IDENTITIES" =~ ^[0-9]+$ || "$IDENTITIES" == "auto" ]] || IDENTITIES=auto

# IDENTITIES=auto: 8 for a card with 8 GiB (8192 MiB) or more, else 2, per card, in the order h-run.sh numbers them
# (NVIDIA first unless WORKER=opencl, then AMD unless WORKER=cuda). VRAM from nvidia-smi (MiB per line) and from
# /sys/class/drm/card<N>/device/mem_info_vram_total (bytes, amdgpu). A card whose VRAM cannot be read gets 8.
ident_block=""   # IDENTITIES_GPU<N>=... lines, one per card, newline-terminated
ident_summary=""
if [[ "$IDENTITIES" == "auto" ]]; then
  vrams=()
  if [[ "$WORKER" != "opencl" ]] && command -v nvidia-smi >/dev/null 2>&1; then
    nvq=(nvidia-smi --query-gpu=memory.total --format=csv,noheader,nounits)
    command -v timeout >/dev/null 2>&1 && nvq=(timeout 20 "${nvq[@]}")
    while read -r mib; do
      mib="${mib//[[:space:]]/}"
      [[ "$mib" =~ ^[0-9]+$ ]] && vrams+=("$mib") || vrams+=("")
    done < <("${nvq[@]}" 2>/dev/null)
  fi
  if [[ "$WORKER" != "cuda" ]]; then
    for d in "${IGNEUM_DRM_ROOT:-/sys/class/drm}"/card*; do
      [[ "$(basename "$d")" =~ ^card[0-9]+$ && -r "$d/device/mem_info_vram_total" ]] || continue
      bytes="$(cat "$d/device/mem_info_vram_total" 2>/dev/null)"
      [[ "$bytes" =~ ^[0-9]+$ ]] && vrams+=("$((bytes / 1048576))") || vrams+=("")
    done
  fi
  n=0
  for mib in ${vrams[@]+"${vrams[@]}"}; do
    if [[ -z "$mib" || "$mib" -ge 8192 ]]; then ident=8; else ident=2; fi
    ident_block+="IDENTITIES_GPU$n=$ident"$'\n'
    ident_summary+="gpu$n ${mib:-?}MiB:$ident "
    n=$((n + 1))
  done
  IDENTITIES=8   # the fallback h-run.sh uses for a card h-config.sh could not see
  [[ $n == 0 ]] && ident_summary="no VRAM readable, 8 per card"
fi

url="$CUSTOM_URL"; pool_url=""; pool_tls=0
if [[ "$url" == pool://* || "$url" == pools://* || "$url" == stratum+tcp://* || "$url" == stratum+ssl://* ]]; then
  # pool mode: the Flight Sheet's pool URL is the pool; the node (if any) comes from NODE=. pools:// or
  # stratum+ssl:// is TLS 1.3 against the system roots (spec 9.3); POOL_PIN=<hex> pins a self-signed pool's certificate
  [[ "$url" == pools://* || "$url" == stratum+ssl://* ]] && pool_tls=1
  pool_url="${url#pool://}"; pool_url="${pool_url#pools://}"; pool_url="${pool_url#stratum+tcp://}"; pool_url="${pool_url#stratum+ssl://}"; pool_url="${pool_url%/}"
  url="${NODE:-none}"
  [[ "$url" != "local" && "$url" != "none" && "$url" != grpc://* ]] && url="grpc://$url"
else
  [[ "$url" == "local" ]] && url="local"
  [[ "$url" != "local" && "$url" != grpc://* ]] && url="grpc://$url"
fi

sq() { local v="${1//\'/\'\\\'\'}"; printf "'%s'" "$v"; }   # single-quoted for sourcing: JSON and flags carry shell characters
mkdir -p "$(dirname "$CUSTOM_CONFIG_FILENAME")"
cat > "$CUSTOM_CONFIG_FILENAME" <<CONF
# written by h-config.sh from the Flight Sheet; edit the Flight Sheet, not this file
NODE_URL=$url
WALLET=$(printf '%s' "$wallet" | tr 'A-F' 'a-f')
LABEL=$label
DEV_FEE=$DEV_FEE
IDENTITIES=$IDENTITIES
${ident_block}WORKER=$WORKER
VOTE=$VOTE
PEERS=$PEERS
EXTRA=$(sq "$EXTRA")
OVERRIDE=$(sq "$OVERRIDE")
POOL_URL=$pool_url
POOL_TLS=$pool_tls
POOL_PIN=${POOL_PIN:-}
CONF
if [[ -n "$pool_url" ]]; then
  echo "Igneum: config written to $CUSTOM_CONFIG_FILENAME (POOL $pool_url, $([[ -n "${POOL_PIN:-}" ]] && echo "TLS pinned" || { [[ $pool_tls == 1 ]] && echo "TLS" || echo "plain TCP"; }), verifier node $url, wallet ${wallet:0:8}..., worker $label; no software dev fee in pool mode; override ${OVERRIDE:+set}${OVERRIDE:-NONE: a local verifier node will be refused by devnet peers})"
else
  echo "Igneum: config written to $CUSTOM_CONFIG_FILENAME (node $url, wallet ${wallet:0:8}..., dev fee ${DEV_FEE}%, identities ${ident_summary:-$IDENTITIES per card}, override ${OVERRIDE:+set}${OVERRIDE:-NONE: a local node will be refused by devnet peers})"
fi
