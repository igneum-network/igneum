#!/usr/bin/env bash
# Removes Ember from the user's home: the service, the binaries and the state; the config file is kept unless --purge.
set -u
opt="$HOME/.local/opt/ember"; state="$HOME/.local/state/ember"
systemctl --user disable --now ember.service 2>/dev/null || true; rm -f "$HOME/.config/systemd/user/ember.service"; systemctl --user daemon-reload 2>/dev/null || true
[ -x "$opt/run.sh" ] && "$opt/run.sh" stop || true
# F-E18: the power-cap sudoers rule the installer wrote goes with the install (best effort, never a prompt)
[ -f /etc/sudoers.d/ember-power-cap ] && sudo -n rm -f /etc/sudoers.d/ember-power-cap 2>/dev/null || true
[ -f /usr/local/sbin/ember-powercap ] && sudo -n rm -f /usr/local/sbin/ember-powercap 2>/dev/null || true
rm -rf "$opt" "$state"
[ "${1:-}" = "--purge" ] && rm -rf "$HOME/.config/ember"
echo "Ember removed"
