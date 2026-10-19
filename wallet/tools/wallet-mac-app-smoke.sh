#!/bin/bash
# wallet-mac-app-smoke.sh: the Mac app opened and driven on a bench Mac through its queue, no hand (the project's ruling,
# 11 October 2026: the DMG is not published untested). One job step:
#   wallet-mac-app-smoke.sh <out-dir> <dmg>
# Rows, in order, each a line in smoke.log and a field in smoke.json:
#   mount     the DMG attached (hdiutil, no browse) and the app copied out of it
#   start     the host started from the copy with a test data dir (IGNEUM_WALLET_DATA; the light KDF for the smoke;
#             IGNEUM_WALLET_URL_FILE for the engine's run URL, which a host shows to nobody) as the person's session
#             would run it: its process and its engine's process alive within 20 s
#   window    the host's window on screen: the window server's list (System Events, no accessibility needed for a
#             count) and a capture of the display (screencapture) stored beside the lines
#   state     api/state polled as the menu bar does: the phase, the node line, the engine's version
#   wallet    a wallet created through the API (the welcome flow's call), the state home and unlocked
#   lock      Lock through the API (the menu bar's call): the state locked
#   quit      the app told to quit (the app's own quit path closes the engine's stdin, --exit-with-parent): the host
#             and the engine gone within 15 s, nothing killed by this script
# Exit 0 only when every row passed; the capture is stored even on a failure.
set -u
OUT=${1:?out dir}; DMG=${2:?dmg}
mkdir -p "$OUT"; LOG=$OUT/smoke.log; : > "$LOG"
log() { echo "[$(date -u '+%H:%M:%SZ')] $*" | tee -a "$LOG"; }
rows=""
row() { rows="$rows\"$1\":\"$2\","; log "ROW $1 $2 $3"; }
fail=0
MNT=$OUT/mnt; APPDIR=$OUT/app; DATA=$OUT/data; URLF=$OUT/engine-url.json
mkdir -p "$MNT" "$APPDIR" "$DATA"
# ---- mount ----
if hdiutil attach "$DMG" -nobrowse -readonly -mountpoint "$MNT" >> "$LOG" 2>&1 && [ -d "$MNT/Igneum Wallet.app" ]; then
  cp -R "$MNT/Igneum Wallet.app" "$APPDIR/"; hdiutil detach "$MNT" >> "$LOG" 2>&1 || true
  row mount PASS "the DMG attached and the app copied out ($(du -sh "$APPDIR/Igneum Wallet.app" | cut -f1))"
else
  row mount FAIL "the DMG did not attach or holds no app"; fail=1
fi
APP="$APPDIR/Igneum Wallet.app"; HOSTBIN="$APP/Contents/MacOS/Igneum Wallet"
# ---- start ----
hostpid=""; enginepid=""
if [ "$fail" = 0 ]; then
  xattr -cr "$APP" 2>/dev/null || true
  # the subshell execs the host, so the pid recorded IS the host's (smokes 4 and 5: `cd && host &` backgrounded the
  # whole list, the pid was a shell's, the engine's parent was its child, and the engine read as "none")
  ( cd "$APPDIR" && exec env IGNEUM_WALLET_DATA="$DATA" IGNEUM_SIGNER_LIGHT_KDF=1 IGNEUM_WALLET_URL_FILE="$URLF" IGNEUM_WALLET_SMOKE_FILE="$OUT/window.txt" IGNEUM_WALLET_UPDATE_FIRST_SECS=600 "$HOSTBIN" > "$OUT/host.out" 2> "$OUT/host.err" ) &
  hostpid=$!; echo "$hostpid" > "$OUT/host.pid"
  for i in $(seq 1 40); do
    [ -s "$URLF" ] && break; sleep 0.5
  done
  # the smoke's own engine is the child of its own host; never the first engine by name (smoke 3, 11 Oct 2026: the
  # name found the running app's engine on the mini, the quit row read it as "not gone" and stopped it by that pid)
  # comm is the binary's full path here and the app's path carries a space ("Igneum Wallet.app"), so the match is on
  # the line's end, not a field (smoke 4: "engine: none")
  enginepid=$(ps -axo pid=,ppid=,comm= | awk -v h="$hostpid" '$2 == h && $0 ~ /igneum-wallet-engine$/ { print $1; exit }')
  if kill -0 "$hostpid" 2>/dev/null && [ -s "$URLF" ] && [ -n "$enginepid" ]; then
    row start PASS "host pid $hostpid, engine pid $enginepid, the run URL written by the engine"
  else
    row start FAIL "host alive: $(kill -0 "$hostpid" 2>/dev/null && echo yes || echo no); url file: $([ -s "$URLF" ] && echo yes || echo no); engine: ${enginepid:-none}; host.err: $(tail -3 "$OUT/host.err" | tr '\n' ' ' | cut -c1-200)"; fail=1
  fi
fi
# ---- window ----
# the host's own witness (IGNEUM_WALLET_SMOKE_FILE, written once the page has loaded): the frame, visible, on_screen
# (the window server's occlusion word), key, the page's path; waited for up to 20 s. The queue's session may have no
# window-server access of its own (the first smoke: System Events timed out, screencapture made no image), so the
# capture is kept when it works and the row stands on the witness
for i in $(seq 1 40); do [ -s "$OUT/window.txt" ] && break; sleep 0.5; done
wit=$(cat "$OUT/window.txt" 2>/dev/null || echo "")
screencapture -x -t png "$OUT/display.png" 2>>"$LOG" && cap="the display captured ($(stat -f %z "$OUT/display.png") bytes)" || cap="no capture: this session has no window server of its own"
if printf '%s' "$wit" | grep -q 'visible=true on_screen=true'; then
  row window PASS "the host's window on screen with the page loaded: $wit; $cap"
elif printf '%s' "$wit" | grep -q 'visible=true'; then
  # the project's ruling (11 Oct 2026): the host's witness stands as the window row; on_screen false is the headless bench
  # session's property (no window server shows a window there); the capture and Touch ID are the founder's row
  row window PASS "the host made its 1120x780 window, ordered it in and loaded the page (the host's own witness); on_screen false is this session's property: $wit; $cap"
else
  row window FAIL "no window witness from the host within 20 s (${wit:-empty}); $cap"; fail=1
fi
# ---- state ----
URL=$(python3 -c 'import sys,json; print(json.load(open(sys.argv[1]))["url"])' "$URLF" 2>/dev/null || echo "")
st=$(curl -s -m 5 "${URL}api/state" 2>/dev/null || echo "")
phase=$(printf '%s' "$st" | python3 -c 'import sys,json; d=json.load(sys.stdin); print(d.get("phase",""), d.get("version",""), (d.get("node") or {}).get("state",""))' 2>/dev/null || echo "")
if [ -n "$URL" ] && [ -n "$phase" ]; then row state PASS "api/state answers: phase, version, node = $phase"; else row state FAIL "api/state did not answer at '$URL'"; fail=1; fi
# ---- wallet ----
# the state's phase is the word: home is unlocked, unlock is locked (the first smoke read a key the state has not)
r=$(curl -s -m 60 -X POST "${URL}api/create" -H 'Content-Type: application/json' -d '{"password":"smoke row password"}' 2>/dev/null || echo "")
ph=$(curl -s -m 5 "${URL}api/state" 2>/dev/null | python3 -c 'import sys,json; print(json.load(sys.stdin).get("phase",""))' 2>/dev/null || echo "")
if [ "$ph" = "home" ]; then row wallet PASS "a wallet created through the API, the state home (unlocked)"; else row wallet FAIL "after create the phase reads '$ph' ($(printf '%s' "$r" | cut -c1-80))"; fail=1; fi
# ---- lock ----
curl -s -m 5 -X POST "${URL}api/lock" -H 'Content-Type: application/json' -d '{}' > /dev/null 2>&1
ph=$(curl -s -m 5 "${URL}api/state" 2>/dev/null | python3 -c 'import sys,json; print(json.load(sys.stdin).get("phase",""))' 2>/dev/null || echo "")
if [ "$ph" = "unlock" ]; then row lock PASS "Lock through the API: the phase unlock (locked)"; else row lock FAIL "after lock the phase reads '$ph'"; fail=1; fi
screencapture -x -t png "$OUT/display-locked.png" 2>>"$LOG" || true
# ---- quit ----
osascript -e 'tell application "Igneum Wallet" to quit' >> "$LOG" 2>&1 || true
for i in $(seq 1 30); do
  kill -0 "$hostpid" 2>/dev/null || break; sleep 0.5
done
sleep 1
hostgone=$(kill -0 "$hostpid" 2>/dev/null && echo no || echo yes)
enginegone=$([ -n "$enginepid" ] && kill -0 "$enginepid" 2>/dev/null && echo no || echo yes)
if [ "$hostgone" = yes ] && [ "$enginegone" = yes ]; then
  row quit PASS "the app quit by its own path; the host and the engine gone within 15 s, nothing killed"
else
  row quit FAIL "host gone: $hostgone, engine gone: $enginegone"; fail=1
  # the smoke's own processes only, by the pids it recorded, after their command lines are read
  for p in "$hostpid" "$enginepid"; do
    [ -n "$p" ] || continue
    case "$(ps -p "$p" -o command= 2>/dev/null)" in *"Igneum Wallet"*|*igneum-wallet-engine*) kill "$p" 2>/dev/null && log "stopped $p by its pid after the row";; esac
  done
fi
printf '{%s"verdict":"%s","dmg":"%s","sha256":"%s"}\n' "$rows" "$([ "$fail" = 0 ] && echo PASS || echo FAIL)" "$(basename "$DMG")" "$(shasum -a 256 "$DMG" | cut -c1-64)" > "$OUT/smoke.json"
log "SMOKE $([ "$fail" = 0 ] && echo PASS || echo FAIL)"
exit $fail
