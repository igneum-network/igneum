#!/usr/bin/env bash
# Prepare a self-hosted Blockscout for an Igneum node (docs/ecosystem/DESIGN.md, section 3; the plan, R06).
# Run inside a clone of blockscout at the pinned tag:  bash prepare.sh <path-to-blockscout>/docker-compose <rpc-host> [chain-id]
# What it does: points the backend at the node (HTTP, trace and WS), sets the chain id and coin, binds every published
# port to 127.0.0.1 (nothing public without the founder's go), disables SSL to the bundled database, and installs the
# dev-node override (igneum-devnode.yml) beside the compose files.
set -euo pipefail
DC="${1:?docker-compose directory}"; RPC="${2:?rpc host, e.g. devnode or NODE-HOST}"; CHAIN="${3:-7310}"; WIPE="${4:-}"
# the node's HTTP and WS ports (devnet5's node A on the build box listens on 8608 and 8609, not the 8545 and 8546 of the first chain)
RPC_PORT="${RPC_PORT:-8545}"; WS_PORT="${WS_PORT:-8546}"
HERE="$(cd "$(dirname "$0")" && pwd)"
E="$DC/envs/common-blockscout.env"; F="$DC/envs/common-frontend.env"
sed -i "s#^ETHEREUM_JSONRPC_VARIANT=.*#ETHEREUM_JSONRPC_VARIANT=geth#; s#^ETHEREUM_JSONRPC_HTTP_URL=.*#ETHEREUM_JSONRPC_HTTP_URL=http://$RPC:$RPC_PORT/#; s#^ETHEREUM_JSONRPC_TRACE_URL=.*#ETHEREUM_JSONRPC_TRACE_URL=http://$RPC:$RPC_PORT/#; s#^\# ETHEREUM_JSONRPC_WS_URL=.*#ETHEREUM_JSONRPC_WS_URL=ws://$RPC:$WS_PORT/#; s#^ETHEREUM_JSONRPC_WS_URL=.*#ETHEREUM_JSONRPC_WS_URL=ws://$RPC:$WS_PORT/#; s#^\# CHAIN_ID=.*#CHAIN_ID=$CHAIN#; s#^CHAIN_ID=.*#CHAIN_ID=$CHAIN#" "$E"
grep -q "^COIN=" "$E" && sed -i "s#^COIN=.*#COIN=IGN#" "$E" || echo "COIN=IGN" >> "$E"
grep -q "^ECTO_USE_SSL=" "$E" && sed -i "s#^ECTO_USE_SSL=.*#ECTO_USE_SSL=false#" "$E" || echo "ECTO_USE_SSL=false" >> "$E"
sed -i "s#^NEXT_PUBLIC_NETWORK_NAME=.*#NEXT_PUBLIC_NETWORK_NAME=Igneum devnet#; s#^NEXT_PUBLIC_NETWORK_SHORT_NAME=.*#NEXT_PUBLIC_NETWORK_SHORT_NAME=Igneum devnet#; s#^NEXT_PUBLIC_NETWORK_ID=.*#NEXT_PUBLIC_NETWORK_ID=$CHAIN#; s#^NEXT_PUBLIC_NETWORK_CURRENCY_NAME=.*#NEXT_PUBLIC_NETWORK_CURRENCY_NAME=Igneum#; s#^NEXT_PUBLIC_NETWORK_CURRENCY_SYMBOL=.*#NEXT_PUBLIC_NETWORK_CURRENCY_SYMBOL=IGN#" "$F"
# the design system's theme (site/design/ADOPTION.md section 2.5, 11 October 2026): Blockscout's own palette cannot take
# every token; the ground, the accent, the mark and the two faces are what make the explorer read as ours. The faces and
# the mark are served from the feed host (build.igneum.network: /fonts.css carries the @font-face lines, /brand/icons the
# mark). Each line is set or replaced, never doubled.
FEED="https://build.igneum.network"
set_f() { grep -q "^$1=" "$F" && sed -i "s#^$1=.*#$1=$2#" "$F" || echo "$1=$2" >> "$F"; }
set_f NEXT_PUBLIC_COLOR_THEME_DEFAULT dark
set_f NEXT_PUBLIC_NETWORK_LOGO "$FEED/brand/icons/igneum-256.png"
set_f NEXT_PUBLIC_NETWORK_LOGO_DARK "$FEED/brand/icons/igneum-256.png"
set_f NEXT_PUBLIC_NETWORK_ICON "$FEED/brand/icons/igneum-256.png"
set_f NEXT_PUBLIC_NETWORK_ICON_DARK "$FEED/brand/icons/igneum-256.png"
# NEXT_PUBLIC_HOMEPAGE_HERO_BACKGROUND and NEXT_PUBLIC_HOMEPAGE_PLOT_AREA_COLOR are NOT set: the frontend image
# (ghcr.io/blockscout/frontend v2.3.5, the build box, 11 October 2026) has no build-time placeholder for either, its start-up
# congruity check fails on them and the container restarts for ever (read 09:46Z). The ground and the accent wait for
# an env the image knows; a line here that the image refuses takes the whole explorer down. Lines set earlier are removed.
drop_f() { grep -q "^$1=" "$F" && sed -i "/^$1=/d" "$F" || true; }
drop_f NEXT_PUBLIC_HOMEPAGE_HERO_BACKGROUND
drop_f NEXT_PUBLIC_HOMEPAGE_PLOT_AREA_COLOR
# The ground and the accent on the variables the image has (site/design/ADOPTION.md section 2.5, main a4dcec24, checked by
# the design lead against the image's ENVS.md and toolkit/theme/foundations/colors.ts at v2.3.5): the hero plate, the
# theme overrides (light value first, dark second; colours in rgb() so no # reads as a comment in the env file), and no
# advertising provider on any surface of ours.
HERO="{'background':['rgb(244,241,236)','rgb(12,12,14)'],'text_color':['rgb(22,22,26)','rgb(244,241,236)'],'border':['1px solid rgb(226,222,216)','1px solid rgb(42,42,48)'],'search':{'border_width':['1px','1px']},'button':{'_default':{'background':['rgb(208,66,13)','rgb(242,84,27)'],'text_color':['rgb(255,255,255)','rgb(12,12,14)']},'_hover':{'background':['rgb(224,74,20)','rgb(255,106,43)'],'text_color':['rgb(255,255,255)','rgb(12,12,14)']}}}"
OVERRIDES="{'bg':{'primary':{'_light':{'value':'rgb(244,241,236)'},'_dark':{'value':'rgb(12,12,14)'}}},'text':{'primary':{'_light':{'value':'rgb(22,22,26)'},'_dark':{'value':'rgb(244,241,236)'}},'secondary':{'_light':{'value':'rgb(107,107,112)'},'_dark':{'value':'rgb(154,154,158)'}}},'link':{'primary':{'_light':{'value':'rgb(208,66,13)'},'_dark':{'value':'rgb(242,84,27)'}}},'hover':{'_light':{'value':'rgb(224,74,20)'},'_dark':{'value':'rgb(255,106,43)'}},'button':{'primary':{'_light':{'value':'rgb(208,66,13)'},'_dark':{'value':'rgb(242,84,27)'}}},'graph':{'line':{'_light':{'value':'rgb(208,66,13)'},'_dark':{'value':'rgb(242,84,27)'}},'gradient':{'start':{'_light':{'value':'rgba(208,66,13,0.3)'},'_dark':{'value':'rgba(242,84,27,0.3)'}},'stop':{'_light':{'value':'rgba(208,66,13,0)'},'_dark':{'value':'rgba(242,84,27,0)'}}}},'stats':{'bg':{'_light':{'value':'rgb(255,255,255)'},'_dark':{'value':'rgb(22,22,26)'}}},'topbar':{'bg':{'_light':{'value':'rgb(250,248,245)'},'_dark':{'value':'rgb(17,17,20)'}}},'navigation':{'bg':{'selected':{'_light':{'value':'rgba(208,66,13,0.09)'},'_dark':{'value':'rgba(242,84,27,0.10)'}}},'text':{'selected':{'_light':{'value':'rgb(208,66,13)'},'_dark':{'value':'rgb(242,84,27)'}}}},'selected':{'option':{'bg':{'_light':{'value':'rgb(208,66,13)'},'_dark':{'value':'rgb(242,84,27)'}}}}}"
set_f NEXT_PUBLIC_HOMEPAGE_HERO_BANNER_CONFIG "$HERO"
set_f NEXT_PUBLIC_COLOR_THEME_OVERRIDES "$OVERRIDES"
set_f NEXT_PUBLIC_AD_BANNER_PROVIDER none
set_f NEXT_PUBLIC_AD_TEXT_PROVIDER none
set_f NEXT_PUBLIC_FONT_FAMILY_HEADING "{'name':'Unbounded','url':'$FEED/fonts.css'}"
set_f NEXT_PUBLIC_FONT_FAMILY_BODY "{'name':'IBM Plex Sans','url':'$FEED/fonts.css'}"
# docker-compose.yml sets the backend's RPC URLs and CHAIN_ID inline; those win over the env file.
sed -i -E "s#ETHEREUM_JSONRPC_HTTP_URL: .*#ETHEREUM_JSONRPC_HTTP_URL: http://$RPC:$RPC_PORT/#; s#ETHEREUM_JSONRPC_TRACE_URL: .*#ETHEREUM_JSONRPC_TRACE_URL: http://$RPC:$RPC_PORT/#; s#ETHEREUM_JSONRPC_WS_URL: .*#ETHEREUM_JSONRPC_WS_URL: ws://$RPC:$WS_PORT/#; s#CHAIN_ID: .*#CHAIN_ID: \"$CHAIN\"#" "$DC/docker-compose.yml"
# Every published port on the loopback only (long port syntax: add host_ip under each published line).
for f in "$DC"/services/nginx.yml "$DC"/services/db.yml "$DC"/services/stats.yml "$DC"/services/nginx-explorer.yml; do
  [ -f "$f" ] || continue
  python3 - "$f" <<'PY'
import re, sys
p = sys.argv[1]; lines = open(p).read().split("\n"); out = []
for i, l in enumerate(lines):
    out.append(l)
    m = re.match(r"^(\s+)published:\s*(.+)$", l)
    if m and (i + 1 >= len(lines) or "host_ip" not in lines[i + 1]):
        out.append(m.group(1) + "host_ip: 127.0.0.1")
open(p, "w").write("\n".join(out))
PY
done
cp "$HERE/igneum-devnode.yml" "$DC/igneum-devnode.yml"
# The databases are bind mounts and survive `docker compose down -v`; a stack pointed at a new chain must start clean.
if [ "$WIPE" = "--wipe" ]; then
  for d in "$DC/services/blockscout-db-data" "$DC/services/stats-db-data"; do
    [ -d "$d" ] && docker run --rm -v "$d:/x" ubuntu:24.04 sh -c 'rm -rf /x/* /x/.[!.]* 2>/dev/null' && echo "wiped $d"
  done
fi
echo "prepared: rpc $RPC chain $CHAIN; run: docker compose -f docker-compose.yml -f igneum-devnode.yml up -d"
