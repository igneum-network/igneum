#!/usr/bin/env bash
# Ember for Linux: the one-command install. Run from the unpacked archive as the user who will mine; root is fine on a
# machine that has no other user (nothing is elevated; everything stays under that user's home):
#   ./install.sh --pool host:port --wallet 0x... [--backup host:port] (or --keep-config: the update, the config kept) [--worker NAME] [--tariff 0.28] [--currency GBP]
#                [--no-tls] [--no-pool-fee] [--node http://host:port] [--device 0,1]
# --device names the cards to mine on by their inventory index (ember devices); without it every supported card is on
# and a CPU-only host mines nothing until the CPU worker is switched on with --device 0.
# Installs under ~/.local/opt/ember, writes $IGNEUM_SECRETS_DIR/ember/ember.conf, registers the user service (systemd --user
# when there is a user session, else a supervised runner with a pid file under ~/.local/state/ember) and starts it.
# Removal: ~/.local/opt/ember/uninstall.sh. Nothing elevated and nothing outside the user's home, with one exception
# the user can see: when the user has promptless sudo, a root-owned validating helper for the power-cap call alone and
# its sudoers line with no free arguments (F-E18).
set -eu
here=$(cd "$(dirname "$0")" && pwd)
pool=""; backup=""; wallet=""; worker="$(hostname -s 2>/dev/null || echo ember)"; tariff=""; currency=""; no_tls=0; no_pool_fee=0; node=""; device=""; keep=0
while [ $# -gt 0 ]; do
  case "$1" in
    --keep-config) keep=1; shift;;
    --pool) pool=$2; shift 2;; --backup) backup=$2; shift 2;; --wallet|--address) wallet=$2; shift 2;; --worker) worker=$2; shift 2;;
    --tariff) tariff=$2; shift 2;; --device) device=$2; shift 2;; --currency) currency=$2; shift 2;; --no-tls) no_tls=1; shift;; --no-pool-fee) no_pool_fee=1; shift;; --node) node=$2; shift 2;;
    *) echo "unknown argument $1"; exit 2;;
  esac
done
# F-E24 (14 October 2026): --keep-config is the update's form on a host with no window (the offer in Ember's log and
# /state names it): the archive's binaries replace the installed ones, the config file stays as it is, the service
# restarts; nothing else is asked
if [ "$keep" = 1 ]; then
  [ -s "$HOME/.config/ember/ember.conf" ] || { echo "--keep-config: no config at $HOME/.config/ember/ember.conf to keep; run the install with --pool and --wallet"; exit 2; }
  wallet=$(sed -n 's/^address *= *//p' "$HOME/.config/ember/ember.conf" | head -1); pool=$(sed -n 's/^pool *= *//p' "$HOME/.config/ember/ember.conf" | head -1); node=$(sed -n 's/^node *= *//p' "$HOME/.config/ember/ember.conf" | head -1)
fi
[ -n "$wallet" ] || { echo "--wallet 0x... is required (the address your rewards go to)"; exit 2; }
[ -n "$pool" ] || [ -n "$node" ] || { echo "--pool host:port (or --node URL) is required"; exit 2; }
# the pool's own well-known document names whether its port speaks TLS (the E1 operability study, 12 October 2026:
# a default install spoke TLS to a plain port and looped); --no-tls stays as a hand, the document wins when it answers
# F-E17 (14 October 2026): a CUDA forward-compat library ahead of the driver's on the loader path makes cuInit fail
# on GeForce cards ("forward compatibility was attempted on non supported HW"), and the worker then lists no card. When
# nvidia-smi names a card, the loader's view of libcuda.so.1 is read and a compat copy ahead of the driver's is named
# with its fix before the first start; the engine also puts the driver's directories first for the worker.
if command -v nvidia-smi >/dev/null 2>&1 && nvidia-smi -L >/dev/null 2>&1; then
  first=$(ldconfig -p 2>/dev/null | awk '/libcuda\.so\.1 /{print $NF; exit}')
  case "$first" in
    */cuda-*/compat/*|*/compat/libcuda.so.1)
      drv=$(nvidia-smi --query-gpu=driver_version --format=csv,noheader 2>/dev/null | head -1)
      echo "note: the loader resolves libcuda.so.1 to $first (a CUDA forward-compatibility library) ahead of the driver $drv's own; forward compatibility is refused on GeForce cards, so the CUDA worker would find no card. Ember puts the driver's library directory first for its worker; to fix the box itself, remove the compat entry from /etc/ld.so.conf.d (or set LD_LIBRARY_PATH to the driver's directory) and run ldconfig." ;;
  esac
fi
if [ -n "$pool" ] && [ "$no_tls" = 0 ] && command -v curl >/dev/null 2>&1; then
  ph=${pool%%:*}; pp=${pool##*:}
  doc=$(curl -fsS -m 10 "https://$ph/.well-known/igneum-pool.json" 2>/dev/null)
  if [ -n "$doc" ] && printf '%s' "$doc" | python3 -c '
import json,sys
d=json.load(sys.stdin); port=int(sys.argv[1])
eps=[e for e in d.get("endpoints",[]) if int(e.get("port",0))==port]
sys.exit(0 if eps and eps[0].get("tls") is False else 1)' "$pp" 2>/dev/null; then
    no_tls=1; echo "the pool's document says port $pp speaks plain TCP; no_tls set"
  fi
fi
opt="$HOME/.local/opt/ember"; conf_dir="$HOME/.config/ember"; state="$HOME/.local/state/ember"
mkdir -p "$opt" "$conf_dir" "$state"
cp -f "$here/bin/ember" "$opt/ember"; chmod +x "$opt/ember"
mkdir -p "$opt/workers"; cp -f "$here"/workers/* "$opt/workers/"; chmod +x "$opt"/workers/igneum-worker-* 2>/dev/null || true
cp -f "$here/VERSION" "$opt/VERSION"; cp -f "$here/uninstall.sh" "$opt/uninstall.sh"; chmod +x "$opt/uninstall.sh"; cp -f "$here/run.sh" "$opt/run.sh"; chmod +x "$opt/run.sh"
( cd "$opt/workers" && sha256sum -c --quiet workers.sha256 ) || { echo "the workers' checksums do not match the manifest; the archive is damaged"; exit 3; }
conf="$conf_dir/ember.conf"
if [ "$keep" = 1 ]; then
  echo "config kept at $conf (--keep-config); version $(cat "$opt/VERSION") installed"
  echo "$(date -u +%FT%TZ) updated to $(cat "$opt/VERSION") with --keep-config" >> "$state/install.log"
else
{
  echo "# Ember: the one config file. Keys are the mine command's flags; edit and restart the service."
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
fi
# F-E18 (14 October 2026): the power cap is one driver call that needs root (nvidia-smi -i <card> -pl <watts>); on a box
# where that user is not root the cap in the settings was a silent no-op (the E1 operability study). When the installing
# user can run sudo without a prompt here, a root-owned validating wrapper (/usr/local/sbin/ember-powercap <card> <watts>:
# both arguments checked as a bounded card index or PCI bus id and a bounded integer of watts before the one exec of
# nvidia-smi -i <card> -pl <watts>, nothing else passed) and a sudoers line for that wrapper with NO free arguments are
# installed (checked by visudo before it counts), and Ember applies caps itself through sudo -n. A rule with `*` in
# the arguments would let the whole nvidia-smi surface run as root (security A, 14 October 2026), so none is written.
# Otherwise the row says the cap was requested and not applied, with the reason, and names the command for root.
# Both files are recorded in $state/install.log and removed by uninstall.sh.
if [ "$(id -u)" != 0 ] && command -v nvidia-smi >/dev/null 2>&1 && command -v sudo >/dev/null 2>&1; then
  smi=$(command -v nvidia-smi); who=${USER:-$(id -un)}; wrap=/usr/local/sbin/ember-powercap
  rule="$who ALL=(root) NOPASSWD: $wrap"
  # the exec target baked into the root-owned helper is the driver's own binary, never one the installing user's PATH
  # put first (security A, 14 October 2026): it must sit under /usr/bin, /usr/local/bin or /opt, be owned by root and
  # be writable by root alone; otherwise no helper is installed and the reason is said
  smi_ok=1
  case "$smi" in /usr/bin/*|/usr/local/bin/*|/opt/*) ;; *) smi_ok=0;; esac
  if [ "$smi_ok" = 1 ]; then
    st=$(stat -c '%u %a' "$smi" 2>/dev/null || echo "x x"); set -- $st
    case "$1 $2" in "0 "[0-7][0145][0145]) ;; *) smi_ok=0;; esac
  fi
  if [ "$smi_ok" != 1 ]; then
    echo "power cap: no helper is installed because nvidia-smi resolves to $smi, which is not the driver's own root-owned binary under /usr/bin, /usr/local/bin or /opt; a power_cap in the config is reported as requested, not applied, until root runs: nvidia-smi -i <card> -pl <watts>"
    echo "$(date -u +%FT%TZ) power-cap helper not installed: nvidia-smi at $smi is not a root-owned binary under /usr/bin, /usr/local/bin or /opt" >> "$state/install.log"
  elif sudo -n true 2>/dev/null; then
    tmpw=$(mktemp); tmpr=$(mktemp)
    cat > "$tmpw" <<WRAP
#!/bin/sh
# Ember's power-cap helper (F-E18): the one driver call Ember runs as root. Two arguments, both checked, nothing else
# passed: the card (an index 0 to 63 or a PCI bus id) and the cap in watts (1 to 2000). Installed by install.sh.
set -eu
[ \$# -eq 2 ] || { echo "usage: ember-powercap <card> <watts>" >&2; exit 2; }
card=\$1; watts=\$2
case "\$card" in
  [0-9]|[1-5][0-9]|6[0-3]) ;;
  [0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F]:[0-9a-fA-F][0-9a-fA-F]:[0-9a-fA-F][0-9a-fA-F][.][0-9]) ;;
  *) echo "ember-powercap: the card is an index 0 to 63 or a PCI bus id" >&2; exit 2;;
esac
case "\$watts" in
  [1-9]|[1-9][0-9]|[1-9][0-9][0-9]|1[0-9][0-9][0-9]|2000) ;;
  *) echo "ember-powercap: the cap is 1 to 2000 watts" >&2; exit 2;;
esac
exec $smi -i "\$card" -pl "\$watts"
WRAP
    printf '# Ember: the one helper Ember runs as root for the power cap (no free arguments); installed by install.sh %s\n%s\n' "$(date -u +%FT%TZ)" "$rule" > "$tmpr"
    if sudo -n visudo -cf "$tmpr" >/dev/null 2>&1 && sudo -n install -m 0755 -o root -g root "$tmpw" "$wrap" 2>/dev/null && sudo -n install -m 0440 -o root -g root "$tmpr" /etc/sudoers.d/ember-power-cap 2>/dev/null; then
      echo "power cap: the helper $wrap (root, two checked arguments) and its sudoers line /etc/sudoers.d/ember-power-cap are installed; Ember applies power caps itself"
      echo "$(date -u +%FT%TZ) power-cap helper installed at $wrap (sha256 $(sha256sum "$tmpw" | cut -c1-16)); sudoers rule at /etc/sudoers.d/ember-power-cap: $rule" >> "$state/install.log"
    else
      echo "power cap: the helper was not installed (visudo refused the rule or /usr/local/sbin or /etc/sudoers.d is not writable); a power_cap in the config is reported as requested, not applied, until root runs: $smi -i <card> -pl <watts>"
      echo "$(date -u +%FT%TZ) power-cap helper NOT installed (visudo, /usr/local/sbin or /etc/sudoers.d refused): $rule" >> "$state/install.log"
    fi
    rm -f "$tmpw" "$tmpr"
  else
    echo "power cap: $who cannot run sudo without a prompt here, so no helper is installed; a power_cap in the config is reported as requested, not applied, until root runs: $smi -i <card> -pl <watts>"
    echo "$(date -u +%FT%TZ) power-cap helper not installed: $who has no promptless sudo" >> "$state/install.log"
  fi
fi
if command -v systemctl >/dev/null 2>&1 && systemctl --user show-environment >/dev/null 2>&1; then
  mkdir -p "$HOME/.config/systemd/user"
  sed "s|@OPT@|$opt|g; s|@CONF@|$conf|g" "$here/ember.service" > "$HOME/.config/systemd/user/ember.service"
  systemctl --user daemon-reload
  if [ "$keep" = 1 ]; then systemctl --user restart ember.service; else systemctl --user enable --now ember.service; fi
  loginctl enable-linger "$USER" 2>/dev/null || true
  echo "ember.service started (systemd --user); status: systemctl --user status ember; log: journalctl --user -u ember -f"
else
  if [ "$keep" = 1 ]; then "$opt/run.sh" stop 2>/dev/null || true; fi
  "$opt/run.sh" start
  echo "ember started under the supervised runner (no user session for systemd here); log: $state/ember.log; stop: $opt/run.sh stop"
fi
echo "mining for $wallet; the state line is in the log every 30 s and at http://127.0.0.1:4068/metrics"
