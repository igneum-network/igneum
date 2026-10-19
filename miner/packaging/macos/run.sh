#!/bin/bash
# The launchd agent's handle: start | stop | status | restart. launchd keeps the miner alive (KeepAlive); this script
# only talks to launchd and reads the log. No pid is signalled by hand: a stop is a bootout of the agent.
set -u
state="$HOME/.local/state/ember"; plist="$HOME/Library/LaunchAgents/network.igneum.ember.plist"; label="network.igneum.ember"; dom="gui/$(id -u)"
case "${1:-status}" in
  start) [ -f "$plist" ] || { echo "no agent installed ($plist); run install.sh first"; exit 2; }; launchctl bootstrap "$dom" "$plist" 2>/dev/null || true; launchctl kickstart "$dom/$label" 2>/dev/null || true; echo "$label started";;
  stop) launchctl bootout "$dom/$label" 2>/dev/null && echo "$label stopped" || echo "$label was not running";;
  status) if launchctl print "$dom/$label" >/dev/null 2>&1; then echo "$label up (pid $(launchctl print "$dom/$label" 2>/dev/null | awk '/^\tpid = /{print $3}'))"; else echo "$label down"; fi; tail -2 "$state/ember.log" 2>/dev/null;;
  restart) launchctl kickstart -k "$dom/$label" 2>/dev/null && echo "$label restarted" || "$0" start;;
  *) echo "usage: run.sh start|stop|status|restart"; exit 2;;
esac
