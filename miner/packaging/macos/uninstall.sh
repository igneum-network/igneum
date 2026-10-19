#!/bin/bash
# Removes Ember from the user's home: the launchd agent, the binaries and the state; the config file is kept unless --purge.
set -u
opt="$HOME/.local/opt/ember"; state="$HOME/.local/state/ember"; plist="$HOME/Library/LaunchAgents/network.igneum.ember.plist"
launchctl bootout "gui/$(id -u)/network.igneum.ember" 2>/dev/null || true
rm -f "$plist"
rm -rf "$opt" "$state"
[ "${1:-}" = "--purge" ] && rm -rf "$HOME/.config/ember"
echo "Ember removed"
