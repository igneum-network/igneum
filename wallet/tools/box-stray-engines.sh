#!/usr/bin/env sh
# The wallet team's stray test engines on a box (the smokes start an engine under a mktemp data dir and stop it by pid; a
# run cut short can leave one): lists every process whose NAME is igneum-wallet-engine (read from /proc/<pid>/comm,
# never a pattern over command lines: a pattern once matched the shell running the check itself, 11 October 2026)
# with its data dir, and with `stop` ends only those whose data dir is under /tmp, by pid, after the command line
# is read back and names the binary. Engines with any other data dir are listed and left alone.
set -u
mode=${1:-list}
for d in /proc/[0-9]*; do
  p=${d#/proc/}
  # the kernel keeps 15 characters of a name in comm: "igneum-wallet-engine" reads "igneum-wallet-e" (found 11 Oct
  # 2026 when four strays sat unlisted; the command line read below names the binary in full)
  [ "$(cat "$d/comm" 2>/dev/null)" = "igneum-wallet-e" ] || continue
  cmd=$(tr '\0' ' ' < "$d/cmdline" 2>/dev/null)
  data=$(printf '%s' "$cmd" | sed -n 's/.*--data-dir \([^ ]*\).*/\1/p')
  case "$cmd" in *igneum-wallet-engine*) ;; *) continue ;; esac
  case "$data" in
    /tmp/*)
      if [ "$mode" = stop ]; then kill "$p" 2>/dev/null && echo "stopped $p (data dir $data)"; else echo "stray $p (data dir $data)"; fi ;;
    *) echo "left $p (data dir ${data:-none}): not a temp-dir test engine" ;;
  esac
done
echo "done ($mode)"
