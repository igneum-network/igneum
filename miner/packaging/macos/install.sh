#!/bin/bash
# Ember for macOS (Apple silicon): the one-command install. Run from the unpacked archive as the user who will mine
# (no administrator password, nothing outside the user's home):
#   ./install.sh --pool host:port --wallet 0x... [--backup host:port] [--worker NAME] [--tariff 0.28] [--currency GBP]
#                [--no-tls] [--no-pool-fee] [--node http://host:port] [--device 0]
# Installs under ~/.local/opt/ember, writes $IGNEUM_SECRETS_DIR/ember/ember.conf, registers a launchd agent
# (network.igneum.ember, KeepAlive) in ~/Library/LaunchAgents and starts it; it comes back at every login.
# Removal: ~/.local/opt/ember/uninstall.sh. The Metal worker pauses on battery and under memory pressure by itself.
set -eu
here=$(cd "$(dirname "$0")" && pwd)
pool=""; backup=""; wallet=""; worker="$(hostname -s 2>/dev/null || echo ember)"; tariff=""; currency=""; no_tls=0; no_pool_fee=0; node=""; device=""
while [ $# -gt 0 ]; do
  case "$1" in
    --pool) pool=$2; shift 2;; --backup) backup=$2; shift 2;; --wallet|--address) wallet=$2; shift 2;; --worker) worker=$2; shift 2;;
    --tariff) tariff=$2; shift 2;; --device) device=$2; shift 2;; --currency) currency=$2; shift 2;; --no-tls) no_tls=1; shift;; --no-pool-fee) no_pool_fee=1; shift;; --node) node=$2; shift 2;;
    *) echo "unknown argument $1"; exit 2;;
  esac
done
[ -n "$wallet" ] || { echo "--wallet 0x... is required (the address your rewards go to)"; exit 2; }
[ -n "$pool" ] || [ -n "$node" ] || { echo "--pool host:port (or --node URL) is required"; exit 2; }
[ "$(uname -m)" = "arm64" ] || { echo "this archive is for Apple silicon (arm64); this Mac is $(uname -m)"; exit 2; }
opt="$HOME/.local/opt/ember"; conf_dir="$HOME/.config/ember"; state="$HOME/.local/state/ember"; agents="$HOME/Library/LaunchAgents"
mkdir -p "$opt" "$conf_dir" "$state" "$agents"
# a running agent is stopped before its binaries are replaced
launchctl bootout "gui/$(id -u)/network.igneum.ember" 2>/dev/null || true
cp -f "$here/bin/ember" "$opt/ember"; chmod +x "$opt/ember"
mkdir -p "$opt/workers"; cp -f "$here"/workers/* "$opt/workers/"; chmod +x "$opt"/workers/igneum-worker-* 2>/dev/null || true
cp -f "$here/VERSION" "$opt/VERSION"; cp -f "$here/uninstall.sh" "$opt/uninstall.sh"; chmod +x "$opt/uninstall.sh"; cp -f "$here/run.sh" "$opt/run.sh"; chmod +x "$opt/run.sh"
( cd "$opt/workers" && shasum -a 256 -c --quiet workers.sha256 ) || { echo "the workers' checksums do not match the manifest; the archive is damaged"; exit 3; }
# the binaries are unsigned until the founder's signing step: the quarantine flag a browser download sets is cleared
# for the files this script copied (the archive came from the user's own download; nothing else is touched)
xattr -d com.apple.quarantine "$opt/ember" "$opt"/workers/* 2>/dev/null || true
conf="$conf_dir/ember.conf"
{
  echo "# Ember: the one config file. Keys are the mine command's flags; edit and restart the agent (run.sh restart)."
  [ -n "$pool" ] && echo "pool = $pool"
  [ -n "$backup" ] && echo "pool_backup = $backup"
  [ -n "$node" ] && echo "node = $node"
  echo "address = $wallet"
  echo "worker_name = $worker"
  echo "algo = igneumhash"
  [ "$no_tls" = 1 ] && echo "no_tls = true"
  [ "$no_pool_fee" = 1 ] && echo "no_pool_fee = true"
  [ -n "$tariff" ] && echo "tariff = $tariff"
  [ -n "$currency" ] && echo "currency = $currency"
  [ -n "$device" ] && echo "device = $device"
  echo "api = 127.0.0.1:4068"
  echo "workers_dir = $opt/workers"
  echo "data_dir = $state"
} > "$conf"
echo "config written to $conf"
plist="$agents/network.igneum.ember.plist"
sed "s|@OPT@|$opt|g; s|@CONF@|$conf|g; s|@STATE@|$state|g" "$here/network.igneum.ember.plist" > "$plist"
launchctl bootstrap "gui/$(id -u)" "$plist"
launchctl kickstart -k "gui/$(id -u)/network.igneum.ember" 2>/dev/null || true
echo "network.igneum.ember started (launchd, back at every login); status: $opt/run.sh status; log: $state/ember.log"
echo "mining for $wallet; the state line is in the log every 30 s and at http://127.0.0.1:4068/metrics"
