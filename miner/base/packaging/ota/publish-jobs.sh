#!/usr/bin/env bash
# Publishes signed remote jobs for the Ember apps: igneum-jobs.json (canonical JSON, sorted keys, no
# whitespace), its detached Ed25519 signature igneum-jobs.json.sig, and igneum-jobs.signed.json, ONE object that
# carries the file text and the signature together (the an earlier release apps fetch that one; 5 October 2026, 13:19:41Z: the second Windows machine
# fetched the file and the signature in two requests across a deploy and refused the pair), next to the update
# manifest in the downloads folder (dl/<token>/), signed on this Mac with the OTA key $IGNEUM_SECRETS_DIR/ota-signing-key. Every app holds a
# long-poll on the relay's public /wake (relay/api/wake.mjs) and fetches the file the moment --deploy records the new
# stamp there (an earlier release, app/igneum-app/src/jobrun.rs; a poll every 2 minutes is the fallback), verifies it with the
# public key compiled into src/manifest.rs, runs each job that targets it ONCE per id, and reports to the log intake
# as run_id job-<id>-<machine id8> (read back with tools/jobs.mjs).
#
#   packaging/ota/publish-jobs.sh add --kind run --target 1ccfe586 --script path.ps1 [--elevated] [--stop-miners] \
#       [--cards-off key,key] [--cards-leave-off] [--timeout-minutes 60] [--shell powershell|bash] --title "..." [--expires-hours 48] [--deploy]
#       (--cards-leave-off, with --cards-off: the runner restores the cards as OFF on any exit, so they stay off and
#        persisted by the app after the job; the way to hold a card out of mining without a script on api/cards, 7 October 2026)
#       (--cards-off: the RUNNER switches these cards off through the app's own card path before the script and puts them
#        back exactly on any exit, done, failed, timeout, aborted or the app quitting; keys with or without the device
#        index; a script never posts to /api/cards itself: tools/ci/playbook-quit-check.sh, 6 October 2026)
#   packaging/ota/publish-jobs.sh add --kind fetch --target all --file the Desktop folder/x.zip [--dir jobs|prove|packs|updates] \
#       [--to name] [--extract] [--extract-dir sub] [--fresh]           (or --url https://... --sha256 ... [--size N])
#   packaging/ota/publish-jobs.sh add --kind collect --target all --glob "logs/app-*.log" [--glob ...] [--command "nvidia-smi"]
#   packaging/ota/publish-jobs.sh add --kind restart --target 1ccfe586 --what miners|node|app
#   packaging/ota/publish-jobs.sh add --kind cards --target 1ccfe586 --cards "nvidia:0:NVIDIA GeForce RTX 5090=on:8,amd::Radeon=off"
#       (the signed card settings kind, 7 October 2026: per card, on|off and the identities after the colon, power_pct
#        after a second colon; a card the machine does not have is refused; applied through the app's own card path
#        and persisted; the report reads every named card back)
#   packaging/ota/publish-jobs.sh add --kind update-now --target all
#   packaging/ota/publish-jobs.sh add --kind shard-benchmark --target 1ccfe586 [--zip the Desktop folder/igneum-prove-wsl2.zip] \
#       [--fixtures "block-338-shard1 block-341-shards2 block-344-shards4"] [--cap-minutes 90] [--distro Ubuntu-24.04] [--wsl-user <name>]
#   packaging/ota/publish-jobs.sh add --kind build --target ae432dc7 [--zip <dl>/build-inputs.zip] [--targets linux,windows] \
#       [--budget-minutes 40] [--stage-minutes '{"linux":20,"windows":20}'] [--min-free-gb 20] [--no-tests] [--relay-url https://...] \
#       [--nice 19] [--cargo-jobs 0] [--distro Ubuntu-24.04] [--wsl-user root]
#       (the zip comes from packaging/windows/push-build-inputs.sh; the default is the one in the downloads folder;
#        tools/build-job.mjs does pack + publish + watch + fetch in one go)
#   common: --target <id8 or id16, comma list, or all> [--platform windows|mac|any] [--requires wsl-prover,nvidia | none]
#           (shard-benchmark defaults to --requires wsl-prover; --requires none or "" publishes with no requirement)
#           [--id custom-id] [--title "..."] [--expires-hours 48] [--deploy]
#   packaging/ota/publish-jobs.sh list                 what is published (expired jobs marked)
#   packaging/ota/publish-jobs.sh remove <id> [--deploy]
#   packaging/ota/publish-jobs.sh sign [--deploy]      re-sign the file as it is (expired jobs dropped)
#   packaging/ota/publish-jobs.sh verify [--tries N]   check the live files against the local ones in every folder they
#                                                      were written to (reachable, identical, verify)
#
# Jobs already in the file stay (expired ones are dropped on every write). A machine runs an id once: to run the
# same thing again, add it again (a new id is generated from the kind and the time unless --id is given).
# Without --deploy the script prints the deploy command; with --deploy it runs the Vercel CLI from the downloads
# folder and verifies the live file (up to --tries times, 5 s apart: the edge serves the previous file for a few
# seconds after a deploy). Testing: --dest <folder> writes elsewhere; --base-url overrides the file URLs.
set -euo pipefail
export TZ=UTC   # every commit from here carries +0000, never the local offset (ledger G14)
HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
export PATH="$HOME/.cargo/bin:$PATH"
KEY="$HOME/.config/igneum/ota-signing-key"
PUB="$HOME/.config/igneum/ota-signing-key.pub"
TOKEN_FILE="$HOME/.config/igneum/dl-token"
SIGNER="$ROOT/app/igneum-app/target/release/igneum-ota-sign"

CMD="${1:-}"; [ $# -gt 0 ] && shift
KIND="" TARGET="" PLATFORM="" REQUIRES="" REQUIRES_SET=0 ID="" TITLE="" EXPIRES_H="48" DEPLOY=0 BASE="" DEST="" TRIES=12
SCRIPT="" SHELL_KIND="" ELEVATED=0 STOP_MINERS=0 TIMEOUT_MIN="" CARDS_OFF="" CARDS_LEAVE_OFF=0
FILE="" URL="" SHA="" SIZE="" DIR="" TO="" EXTRACT=0 EXTRACT_DIR="" FRESH=0
GLOBS=() COMMAND="" WHAT="" CARDS=""
ZIP="" FIXTURES="" CAP_MIN="" DISTRO="" WSL_USER="" REMOVE_ID=""
TARGETS="" BUDGET_MIN="" STAGE_MIN="" MIN_FREE_GB="" TESTS=1 RELAY_URL="" NICE="" CARGO_JOBS=""
case "$CMD" in
  remove) REMOVE_ID="${1:-}"; [ -n "$REMOVE_ID" ] || { echo "remove <id>" >&2; exit 2; }; shift ;;
esac
while [ $# -gt 0 ]; do
  case "$1" in
    --kind) KIND="$2"; shift 2 ;;
    --target) TARGET="$2"; shift 2 ;;
    --platform) PLATFORM="$2"; shift 2 ;;
    --requires) REQUIRES="$2"; REQUIRES_SET=1; [ "$REQUIRES" = none ] && REQUIRES=""; shift 2 ;;
    --id) ID="$2"; shift 2 ;;
    --title) TITLE="$2"; shift 2 ;;
    --expires-hours) EXPIRES_H="$2"; shift 2 ;;
    --script) SCRIPT="$2"; shift 2 ;;
    --shell) SHELL_KIND="$2"; shift 2 ;;
    --elevated) ELEVATED=1; shift ;;
    --stop-miners) STOP_MINERS=1; shift ;;
    --cards-off) CARDS_OFF="$2"; shift 2 ;;
    --cards-leave-off) CARDS_LEAVE_OFF=1; shift ;;
    --timeout-minutes) TIMEOUT_MIN="$2"; shift 2 ;;
    --file) FILE="$2"; shift 2 ;;
    --url) URL="$2"; shift 2 ;;
    --sha256) SHA="$2"; shift 2 ;;
    --size) SIZE="$2"; shift 2 ;;
    --dir) DIR="$2"; shift 2 ;;
    --to) TO="$2"; shift 2 ;;
    --extract) EXTRACT=1; shift ;;
    --extract-dir) EXTRACT_DIR="$2"; shift 2 ;;
    --fresh) FRESH=1; shift ;;
    --glob) GLOBS+=("$2"); shift 2 ;;
    --command) COMMAND="$2"; shift 2 ;;
    --what) WHAT="$2"; shift 2 ;;
    --cards) CARDS="$2"; shift 2 ;;
    --zip) ZIP="$2"; shift 2 ;;
    --fixtures) FIXTURES="$2"; shift 2 ;;
    --cap-minutes) CAP_MIN="$2"; shift 2 ;;
    --distro) DISTRO="$2"; shift 2 ;;
    --wsl-user) WSL_USER="$2"; shift 2 ;;
    --targets) TARGETS="$2"; shift 2 ;;
    --budget-minutes) BUDGET_MIN="$2"; shift 2 ;;
    --stage-minutes) STAGE_MIN="$2"; shift 2 ;;
    --min-free-gb) MIN_FREE_GB="$2"; shift 2 ;;
    --no-tests) TESTS=0; shift ;;
    --relay-url) RELAY_URL="$2"; shift 2 ;;
    --nice) NICE="$2"; shift 2 ;;
    --cargo-jobs) CARGO_JOBS="$2"; shift 2 ;;
    --deploy) DEPLOY=1; shift ;;
    --no-deploy) DEPLOY=0; shift ;;
    --base-url) BASE="$2"; shift 2 ;;
    --dest) DEST="$2"; shift 2 ;;
    --tries) TRIES="$2"; shift 2 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done
case "$CMD" in add|list|remove|sign|verify) ;; *) sed -n '2,35p' "$0" | sed 's/^# \{0,1\}//'; exit 2 ;; esac

[ -f "$KEY" ] || { echo "no $KEY (see packaging/ota/README.md, Keys)" >&2; exit 1; }
[ -f "$PUB" ] || { echo "no $PUB" >&2; exit 1; }
[ -f "$TOKEN_FILE" ] || { echo "no $TOKEN_FILE" >&2; exit 1; }
TOKEN="$(tr -d '[:space:]' < "$TOKEN_FILE")"
if [ -z "$DEST" ]; then
  DLSITE="${IGNEUM_DLSITE:-}"
  [ -n "$DLSITE" ] || { [ -f "$HOME/.config/igneum/dlsite-dir" ] && DLSITE="$(tr -d '[:space:]' < "$HOME/.config/igneum/dlsite-dir")"; } || true
  [ -n "$DLSITE" ] && [ -d "$DLSITE/dl/$TOKEN" ] || { echo "no downloads folder: set IGNEUM_DLSITE or $IGNEUM_SECRETS_DIR/dlsite-dir (must hold dl/<token>/)" >&2; exit 1; }
  DEST="$DLSITE/dl/$TOKEN"
else
  DLSITE=""
  mkdir -p "$DEST"
fi
[ -n "$BASE" ] || BASE="https://dl.igneum.network/dl/$TOKEN"
BASE="${BASE%/}"
JOBS="$DEST/igneum-jobs.json"
SIGNED="$DEST/igneum-jobs.signed.json"
command -v python3 >/dev/null || { echo "python3 is needed for the canonical JSON" >&2; exit 1; }

if [ ! -x "$SIGNER" ]; then
  echo "building igneum-ota-sign"
  (cd "$ROOT/app/igneum-app" && nice -n 19 cargo build --release -j 4 --bin igneum-ota-sign --quiet)
fi
EMBEDDED="$("$SIGNER" embedded | sed -n 1p)"
OURS="$(tr -d '[:space:]' < "$PUB")"
if [ "$EMBEDDED" != "$OURS" ]; then
  echo "the public key in app/igneum-app/src/manifest.rs ($EMBEDDED) is not $PUB ($OURS); the apps would refuse this file" >&2
  exit 1
fi

# The folders the signed files are written to: the token's folder, and the next token's folder while a rotation
# runs (the mirror below). Each entry is "<base url> <local folder>"; the live check runs on every one.
mirror_folder() { # prints the next folder's path when the mirror applies, else nothing
  local nt
  [ -n "$DLSITE" ] && [ -f "$HOME/.config/igneum/dl-token.next" ] || return 0
  nt="$(tr -d '[:space:]' < "$HOME/.config/igneum/dl-token.next")"
  [ -n "$nt" ] && [ -d "$DLSITE/dl/$nt" ] && [ "$nt" != "$TOKEN" ] || return 0
  echo "$DLSITE/dl/$nt"
}
folders() {
  echo "$BASE $DEST"
  local nf
  nf="$(mirror_folder)"
  [ -n "$nf" ] && echo "https://dl.igneum.network/dl/$(basename "$nf") $nf"
  return 0
}

# Checks the live files against the local ones in every folder: the envelope (one object, what the an earlier release apps read)
# reachable, byte-identical and verifying, and the plain pair identical (the older apps). Retries, because the edge
# serves the previous deployment for some seconds after a deploy (4 October 2026: a deploy that had succeeded was
# reported as "not reachable, differs from the local one, or does not verify" by the one check made the moment the CLI
# returned). Each failure names the folder and the condition that failed.
verify_live_one() { # <base> <local folder> <tries>; 0 = verified, 1 = not, with the reason on stderr
  local base="$1" local_dir="$2" tries="${3:-12}" t=0 verdict="" tmp shown
  shown="${base//$TOKEN/<token>}"; [ -n "$NEXT_TOKEN_SHOWN" ] && shown="${shown//$NEXT_TOKEN_SHOWN/<next token>}"
  tmp="$(mktemp -d)"
  while [ "$t" -lt "$tries" ]; do
    t=$((t + 1)); verdict=""
    if ! curl -fsSL -H 'Cache-Control: no-cache' -o "$tmp/j.signed" "$base/igneum-jobs.signed.json"; then verdict="is not reachable (the envelope)"
    elif ! cmp -s "$tmp/j.signed" "$local_dir/igneum-jobs.signed.json"; then verdict="differs from the local one (the envelope)"
    elif ! "$SIGNER" verify-signed-jobs "$PUB" "$tmp/j.signed" >/dev/null 2>&1; then verdict="does not verify against $PUB (the envelope)"
    elif ! curl -fsSL -H 'Cache-Control: no-cache' -o "$tmp/j.json" "$base/igneum-jobs.json"; then verdict="is not reachable (the plain file)"
    elif ! curl -fsSL -H 'Cache-Control: no-cache' -o "$tmp/j.sig" "$base/igneum-jobs.json.sig"; then verdict="has no reachable signature (the plain pair)"
    elif ! cmp -s "$tmp/j.json" "$local_dir/igneum-jobs.json"; then verdict="differs from the local one (the plain file; live $(python3 -c 'import json,sys; print(json.load(open(sys.argv[1])).get("published_at", "?"))' "$tmp/j.json" 2>/dev/null || echo unreadable), local $(python3 -c 'import json,sys; print(json.load(open(sys.argv[1])).get("published_at", "?"))' "$local_dir/igneum-jobs.json" 2>/dev/null || echo unreadable))"
    elif ! cmp -s "$tmp/j.sig" "$local_dir/igneum-jobs.json.sig"; then verdict="differs from the local one (the plain signature)"
    elif ! "$SIGNER" verify-jobs "$PUB" "$tmp/j.json" "$tmp/j.sig" >/dev/null 2>&1; then verdict="does not verify against $PUB (the plain pair)"
    fi
    [ -z "$verdict" ] && break
    [ "$t" -lt "$tries" ] && sleep 5
  done
  rm -rf "$tmp"
  if [ -z "$verdict" ]; then echo "live jobs files verified at $shown/igneum-jobs.signed.json and the plain pair (try $t of $tries)"; return 0; fi
  echo "the live jobs file at $shown $verdict after $t tr$([ "$t" = 1 ] && echo y || echo ies) ($(( (t - 1) * 5 )) s); check the deploy output, then: $0 verify" >&2
  return 1
}
verify_live() { # <tries>; every folder the files were written to must verify
  local tries="${1:-12}" rc=0 base dir
  NEXT_TOKEN_SHOWN="$(basename "$(mirror_folder)" 2>/dev/null)"; [ "$NEXT_TOKEN_SHOWN" = "." ] && NEXT_TOKEN_SHOWN=""
  while read -r base dir; do
    [ -n "$base" ] || continue
    verify_live_one "$base" "$dir" "$tries" || rc=1
  done < <(folders)
  return $rc
}

# After a verified deploy: records the new jobs-file stamp on the relay (relay/api/wake.mjs), where every app holds a
# long-poll and fetches the file the moment the stamp moves (an earlier release). The stamp is published_at plus 8 hex of the
# file's sha256, so a re-signed file wakes the apps too. The relay token ($IGNEUM_SECRETS_DIR/relay-token) travels in a
# header file, never on the command line or the screen. A failure here is a warning: the apps poll every 2 minutes.
wake_apps() { # <added job id or empty>
  local tf="$HOME/.config/igneum/relay-token" rurl="https://relay.igneum.network" sum size pub stamp hdr out rc=0 added='[]'
  [ -f "$tf" ] || { echo "warning: no $tf; the apps were not woken (they poll every 2 minutes)" >&2; return 0; }
  [ -f "$HOME/.config/igneum/relay-url" ] && rurl="$(tr -d '[:space:]' < "$HOME/.config/igneum/relay-url")"
  rurl="${rurl%/}"
  read -r sum size < <("$SIGNER" sha256 "$JOBS")
  pub="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1])).get("published_at", ""))' "$JOBS")"
  stamp="$pub.${sum:0:8}"
  if [ -n "${1:-}" ]; then added="[\"$1\"]"; fi
  hdr="$(mktemp)"; chmod 600 "$hdr"
  printf 'x-relay-token: %s\n' "$(tr -d '[:space:]' < "$tf")" > "$hdr"
  out="$(curl -sS --max-time 20 -X POST "$rurl/wake" -H 'Content-Type: application/json' -H @"$hdr" --data-binary "{\"stamp\":\"$stamp\",\"added\":$added}" 2>&1)" || rc=$?
  rm -f "$hdr"
  if [ "$rc" = 0 ] && printf '%s' "$out" | grep -q '"ok":true'; then
    echo "woke the apps (stamp $stamp)"
  else
    echo "warning: the wake call to $rurl/wake failed (${out:0:160}); the apps poll every 2 minutes and catch it" >&2
  fi
}

if [ "$CMD" = verify ]; then
  [ -f "$JOBS" ] || { echo "no local jobs file at $JOBS" >&2; exit 1; }
  verify_live "$TRIES"; exit $?
fi

if [ "$CMD" = list ]; then
  [ -f "$JOBS" ] || { echo "no jobs file in $DEST"; exit 0; }
  "$SIGNER" verify-jobs "$PUB" "$JOBS" "$JOBS.sig" || { echo "the file in $DEST does not verify; run: $0 sign" >&2; exit 1; }
  if [ -f "$SIGNED" ]; then "$SIGNER" verify-signed-jobs "$PUB" "$SIGNED" >/dev/null || { echo "the envelope in $DEST does not verify; run: $0 sign" >&2; exit 1; }; else echo "(no igneum-jobs.signed.json yet; the next write makes one)"; fi
  python3 - "$JOBS" <<'PY'
import json, sys, datetime
f = json.load(open(sys.argv[1]))
now = datetime.datetime.now(datetime.timezone.utc)
for j in f.get("jobs", []):
    exp = datetime.datetime.strptime(j["expires_at"], "%Y-%m-%dT%H:%M:%SZ").replace(tzinfo=datetime.timezone.utc)
    print(("  EXPIRED " if exp < now else "  ") + j["id"] + ": " + json.dumps(j.get("params", {}), sort_keys=True)[:200])
PY
  exit 0
fi

# ---- the new job (add) -------------------------------------------------------------------------------------------
NEW_JOB=""
hosted_file() { # <local file> -> "url sha256 size" (copied into the downloads folder when it is not there)
  local f="$1" name
  [ -f "$f" ] || { echo "missing: $f" >&2; exit 1; }
  name="$(basename "$f")"
  if [ "$(cd "$(dirname "$f")" && pwd)/$name" != "$DEST/$name" ]; then
    cp "$f" "$DEST/$name"
    echo "copied $name into the downloads folder (deploy ships it)" >&2
  fi
  read -r sum size < <("$SIGNER" sha256 "$DEST/$name")
  echo "$BASE/$name $sum $size"
}
if [ "$CMD" = add ]; then
  [ -n "$KIND" ] || { echo "--kind is required" >&2; exit 2; }
  [ -n "$TARGET" ] || { echo "--target is required (id8, id16, a comma list, or all)" >&2; exit 2; }
  PARAMS='{}'
  case "$KIND" in
    run)
      [ -n "$SCRIPT" ] && [ -f "$SCRIPT" ] || { echo "run: --script <file> is required" >&2; exit 2; }
      [ -n "$SHELL_KIND" ] || { case "$SCRIPT" in *.sh) SHELL_KIND=bash ;; *) SHELL_KIND=powershell ;; esac; }
      [ -n "$PLATFORM" ] || { [ "$SHELL_KIND" = powershell ] && PLATFORM=windows || PLATFORM=any; }
      # A job script is published from a worktree and never passes CI before it runs (5 October 2026: the root-socket
      # fault came back from a branch without the check), so the class checks run here on the script itself, before
      # anything is signed. A failure refuses the publish with the check's output; a missing check refuses it too.
      # PowerShell: every inline bash body must parse (tools/ci/bash-body-check.sh, the lost-quote class; a body it
      # cannot read fails, never skips). Bash: the script itself must parse. Both: a root prover run kills the GPU
      # server and unlinks its socket (tools/ci/prover-socket-check.sh, the root-socket class), and a fetched kit under
      # the jobs folder is tested before its first use (tools/ci/kit-path-check.sh, the wiped-jobs-folder class). A
      # check file that is missing from this tree refuses too (bash exits 127 with the reason), so no job goes out
      # unchecked.
      if [ "$SHELL_KIND" = powershell ]; then
        out="$(bash "$ROOT/tools/ci/bash-body-check.sh" "$SCRIPT" 2>&1)" || { echo "run: bash-body-check.sh refuses $SCRIPT:" >&2; printf '%s\n' "$out" >&2; exit 1; }
      else
        out="$(bash -n "$SCRIPT" 2>&1)" || { echo "run: bash -n refuses $SCRIPT (the lost-quote class):" >&2; printf '%s\n' "$out" >&2; exit 1; }
      fi
      out="$(bash "$ROOT/tools/ci/prover-socket-check.sh" "$SCRIPT" 2>&1)" || { echo "run: prover-socket-check.sh refuses $SCRIPT:" >&2; printf '%s\n' "$out" >&2; exit 1; }
      out="$(bash "$ROOT/tools/ci/kit-path-check.sh" "$SCRIPT" 2>&1)" || { echo "run: kit-path-check.sh refuses $SCRIPT:" >&2; printf '%s\n' "$out" >&2; exit 1; }
      PARAMS="$(python3 -c 'import json,sys; print(json.dumps({"script": open(sys.argv[1]).read(), "shell": sys.argv[2], "elevated": sys.argv[3]=="1", "stop_miners_first": sys.argv[4]=="1", **({"timeout_minutes": int(sys.argv[5])} if sys.argv[5] else {}), **({"cards_off": [k.strip() for k in sys.argv[6].split(",") if k.strip()]} if sys.argv[6] else {}), **({"cards_leave_off": True} if sys.argv[7]=="1" else {})}))' "$SCRIPT" "$SHELL_KIND" "$ELEVATED" "$STOP_MINERS" "$TIMEOUT_MIN" "$CARDS_OFF" "$CARDS_LEAVE_OFF")"
      [ -n "$TITLE" ] || TITLE="run $(basename "$SCRIPT")"
      ;;
    fetch)
      if [ -n "$FILE" ]; then read -r URL SHA SIZE < <(hosted_file "$FILE"); fi
      [ -n "$URL" ] && [ -n "$SHA" ] || { echo "fetch: --file <local> or --url and --sha256" >&2; exit 2; }
      PARAMS="$(python3 -c 'import json,sys; a=sys.argv; d={"url": a[1], "sha256": a[2]}
if a[3]: d["size"]=int(a[3])
if a[4]: d["dir"]=a[4]
if a[5]: d["to"]=a[5]
if a[6]=="1": d["extract"]=True
if a[7]: d["extract_dir"]=a[7]
if a[8]=="1": d["fresh"]=True
print(json.dumps(d))' "$URL" "$SHA" "$SIZE" "$DIR" "$TO" "$EXTRACT" "$EXTRACT_DIR" "$FRESH")"
      [ -n "$TITLE" ] || TITLE="fetch $(basename "$URL")"
      ;;
    collect)
      [ ${#GLOBS[@]} -gt 0 ] || [ -n "$COMMAND" ] || { echo "collect: --glob and/or --command" >&2; exit 2; }
      if printf '%s' "$COMMAND" | grep -qE 'ForEach-Object|Where-Object|\| *% |\| *\? ' && ! printf '%s' "$COMMAND" | grep -qE '\$_|\$PSItem'; then
        echo "collect: the command pipes into a script block but holds no \$_ or \$PSItem; the shell that published it has expanded \$_ to nothing (4 October 2026, collect-pc1-board3: PowerShell saw '.Name' and failed to parse). Pass the command in single quotes." >&2; exit 2
      fi
      PARAMS="$(python3 -c 'import json,sys; d={"globs": [g for g in sys.argv[2:] if g]}
if sys.argv[1]: d["command"]=sys.argv[1]
print(json.dumps(d))' "$COMMAND" "${GLOBS[@]:-}")"
      [ -n "$TITLE" ] || TITLE="collect ${GLOBS[*]:-command output}"
      ;;
    restart)
      case "$WHAT" in miners|node|app) ;; *) echo "restart: --what miners|node|app" >&2; exit 2 ;; esac
      PARAMS="$(python3 -c 'import json,sys; print(json.dumps({"what": sys.argv[1]}))' "$WHAT")"
      [ -n "$TITLE" ] || TITLE="restart $WHAT"
      ;;
    update-now)
      [ -n "$TITLE" ] || TITLE="update now"
      ;;
    cards)
      [ -n "$CARDS" ] || { echo "cards: --cards \"key=on:8,key=off\"" >&2; exit 2; }
      PARAMS="$(python3 -c 'import json,sys
out=[]
for item in [x for x in sys.argv[1].split(",") if x.strip()]:
    key, _, v = item.partition("=")
    parts = v.split(":")
    d = {"key": key.strip()}
    if parts[0] in ("on", "off"): d["enabled"] = parts[0] == "on"
    elif parts[0]: raise SystemExit("cards: %s: on|off expected, got %s" % (key, parts[0]))
    if len(parts) > 1 and parts[1]: d["identities"] = int(parts[1])
    if len(parts) > 2 and parts[2]: d["power_pct"] = int(parts[2])
    out.append(d)
print(json.dumps({"cards": out}))' "$CARDS")"
      [ -n "$TITLE" ] || TITLE="card settings"
      ;;
    shard-benchmark)
      [ -n "$ZIP" ] || ZIP="$HOME/Desktop/igneum-prove-wsl2.zip"
      read -r ZURL ZSHA ZSIZE < <(hosted_file "$ZIP")
      [ -n "$PLATFORM" ] || PLATFORM=windows
      [ "$REQUIRES_SET" = 1 ] || REQUIRES=wsl-prover
      PARAMS="$(python3 -c 'import json,sys; a=sys.argv; d={"zip_url": a[1], "sha256": a[2], "size": int(a[3])}
if a[4]: d["fixtures"]=a[4].split()
if a[5]: d["cap_minutes"]=int(a[5])
if a[6]: d["distro"]=a[6]
if a[7]: d["wsl_user"]=a[7]
print(json.dumps(d))' "$ZURL" "$ZSHA" "$ZSIZE" "$FIXTURES" "$CAP_MIN" "$DISTRO" "$WSL_USER")"
      [ -n "$TITLE" ] || TITLE="shard benchmark on the GPU"
      ;;
    build)
      [ -n "$ZIP" ] || ZIP="$DEST/build-inputs.zip"
      [ -f "$ZIP" ] || { echo "build: no $ZIP; run packaging/windows/push-build-inputs.sh first (or --zip <file>)" >&2; exit 2; }
      read -r ZURL ZSHA ZSIZE < <(hosted_file "$ZIP")
      [ -n "$PLATFORM" ] || PLATFORM=windows
      [ "$REQUIRES_SET" = 1 ] || REQUIRES=wsl
      PARAMS="$(python3 -c 'import json,sys; a=sys.argv; d={"zip_url": a[1], "sha256": a[2], "size": int(a[3])}
if a[4]: d["targets"]=[t.strip() for t in a[4].split(",") if t.strip()]
if a[5]: d["budget_minutes"]=int(a[5])
if a[6]: d["stage_minutes"]=json.loads(a[6])
if a[7]: d["min_free_gb"]=int(a[7])
if a[8]=="0": d["tests"]=False
if a[9]: d["relay_url"]=a[9]
if a[10]: d["nice"]=int(a[10])
if a[11]: d["cargo_jobs"]=int(a[11])
if a[12]: d["distro"]=a[12]
if a[13]: d["wsl_user"]=a[13]
print(json.dumps(d))' "$ZURL" "$ZSHA" "$ZSIZE" "$TARGETS" "$BUDGET_MIN" "$STAGE_MIN" "$MIN_FREE_GB" "$TESTS" "$RELAY_URL" "$NICE" "$CARGO_JOBS" "$DISTRO" "$WSL_USER")"
      if [ -z "$TITLE" ]; then
        BR="$(python3 -c 'import json,sys
try:
    m=json.load(open(sys.argv[1])); print(m.get("node",{}).get("branch",""), m.get("node",{}).get("commit",""))
except Exception: print("")' "${ZIP%.zip}.json" 2>/dev/null || true)"
        TITLE="build node and app${BR:+ ($BR)}"
      fi
      ;;
    *) echo "unknown kind $KIND (run, fetch, collect, restart, update-now, shard-benchmark, build)" >&2; exit 2 ;;
  esac
  [ -n "$PLATFORM" ] || PLATFORM=any
  [ -n "$ID" ] || ID="$KIND-$(date -u +%Y%m%d-%H%M%S)"
  NEW_JOB="$(python3 -c 'import json,sys,datetime
a=sys.argv
now=datetime.datetime.now(datetime.timezone.utc)
t={"machine_ids": "all" if a[2]=="all" else [x.strip().lower() for x in a[2].split(",") if x.strip()], "platform": a[3]}
if a[4]: t["requires"]=[x.strip() for x in a[4].split(",") if x.strip()]
print(json.dumps({"id": a[1], "kind": a[5], "title": a[6], "created_at": now.strftime("%Y-%m-%dT%H:%M:%SZ"), "expires_at": (now+datetime.timedelta(hours=float(a[7]))).strftime("%Y-%m-%dT%H:%M:%SZ"), "target": t, "params": json.loads(a[8]), "report": "log-upload"}))' "$ID" "$TARGET" "$PLATFORM" "$REQUIRES" "$KIND" "$TITLE" "$EXPIRES_H" "$PARAMS")"
fi

# ---- merge: current jobs minus expired (minus a removed id), plus the new one; canonical JSON ---------------------
NEW="$JOBS.new"
python3 - "$JOBS" "$NEW" "$NEW_JOB" "$REMOVE_ID" <<'PY'
import json, sys, datetime, os
cur, out, new_job, remove = sys.argv[1:5]
now = datetime.datetime.now(datetime.timezone.utc)
jobs = []
if os.path.exists(cur):
    for j in json.load(open(cur)).get("jobs", []):
        exp = datetime.datetime.strptime(j["expires_at"], "%Y-%m-%dT%H:%M:%SZ").replace(tzinfo=datetime.timezone.utc)
        if exp < now:
            print("dropped (expired):", j["id"], file=sys.stderr); continue
        if remove and j["id"] == remove:
            print("removed:", j["id"], file=sys.stderr); continue
        jobs.append(j)
if new_job:
    nj = json.loads(new_job)
    if any(j["id"] == nj["id"] for j in jobs):
        print("a job with id %s is already published; give --id another value" % nj["id"], file=sys.stderr); sys.exit(1)
    jobs.append(nj)
    print("added:", nj["id"], nj["kind"], "to", nj["target"]["machine_ids"], "until", nj["expires_at"], file=sys.stderr)
f = {"published_at": now.strftime("%Y-%m-%dT%H:%M:%SZ"), "jobs": jobs}
open(out, "w").write(json.dumps(f, sort_keys=True, separators=(",", ":"), ensure_ascii=False))
PY
"$SIGNER" sign-jobs "$KEY" "$NEW" > "$NEW.sig"
"$SIGNER" verify-jobs "$PUB" "$NEW" "$NEW.sig"
# the envelope is made from the signed pair by the signer, which refuses a pair that does not verify, and is read
# back with the app's own code before anything moves into place
"$SIGNER" envelope-jobs "$PUB" "$NEW" "$NEW.sig" > "$NEW.signed"
"$SIGNER" verify-signed-jobs "$PUB" "$NEW.signed" >/dev/null
mv "$NEW" "$JOBS"
mv "$NEW.sig" "$JOBS.sig"
mv "$NEW.signed" "$SIGNED"
echo "jobs file: $JOBS ($(wc -c < "$JOBS" | tr -d ' ') bytes), signature, and the envelope $(basename "$SIGNED") ($(wc -c < "$SIGNED" | tr -d ' ') bytes)"
# Rotation phase 2 (5 October 2026): apps built with the next token read the next folder, so the three signed
# files go to both folders while both exist (the an earlier release apps missed a job published to the old folder only). The
# copy happens after the signer's read-back, so the mirror never carries a half-written set; the live check after
# the deploy covers both folders.
NEXT_FOLDER="$(mirror_folder)"
if [ -n "$NEXT_FOLDER" ]; then
  cp "$JOBS" "$JOBS.sig" "$SIGNED" "$NEXT_FOLDER/" && echo "jobs file, signature and envelope mirrored to the next folder"
fi

# --deploy ships the JOBS FILE ONLY (6 October 2026, 22:0x UK rule): the downloads folder is shared disk state and a deploy
# ships all of it, so a manifest or a package staged there by another lane went live with the next job publish (the Mac and
# the first Windows machine took earlier an earlier release builds that way). Before deploying, every file in the live token folder and dl/public/ apart from
# the jobs file and its signature must equal what is live (size and sha256 against the served copy); anything else differing
# refuses the deploy and names the files. The release's own deploy goes through packaging/ota/publish-release-manifest.sh --deploy or
# tools/ship-app.mjs, never through here. IGNEUM_JOBS_DEPLOY_ALL=1 overrides, knowingly, for a full-folder deploy.
jobs_only_check() {
  [ "${IGNEUM_JOBS_DEPLOY_ALL:-0}" = 1 ] && { echo "IGNEUM_JOBS_DEPLOY_ALL=1: deploying the whole folder" >&2; return 0; }
  local base="https://dl.igneum.network" bad=0 f n live_size local_size local_sha live_sha
  for f in "$DEST"/* "$DLSITE"/dl/public/*; do
    [ -f "$f" ] || continue
    n="$(basename "$f")"
    case "$n" in igneum-jobs.json|igneum-jobs.signed.json|igneum-jobs.json.sig) continue ;; esac
    local rel; rel="${f#"$DLSITE"}"
    local_size="$(stat -f %z "$f" 2>/dev/null || stat -c %s "$f")"
    live_size="$(curl -sI -m 10 -H 'Cache-Control: no-cache' "$base$rel?x=$RANDOM" | awk 'tolower($1)=="content-length:"{print $2}' | tr -d '\r' | tail -1)"
    if [ -z "$live_size" ]; then echo "  not live yet: $rel (a new file; the release step deploys it)" >&2; bad=1; continue; fi
    if [ "$live_size" != "$local_size" ]; then echo "  differs from the live copy: $rel (local $local_size B, live $live_size B)" >&2; bad=1; continue; fi
    case "$n" in *.json|*.sig|*.sha256) # small: compare the bytes
      # the downloads index carries an "updated" stamp rewritten after every alias pass; the stamp is not a change
      norm() { if [ "$n" = igneum-downloads.json ]; then sed -E 's/"updated": *"[^"]*"/"updated": ""/'; else cat; fi; }
      local_sha="$(norm < "$f" | shasum -a 256 | cut -c1-64)"; live_sha="$(curl -s -m 15 -H 'Cache-Control: no-cache' "$base$rel?x=$RANDOM" | norm | shasum -a 256 | cut -c1-64)"
      [ "$local_sha" = "$live_sha" ] || { echo "  differs from the live copy: $rel (content)" >&2; bad=1; } ;;
    esac
  done
  if [ "$bad" = 1 ]; then echo "publish-jobs: the folder holds changes besides the jobs file (above); a job publish never deploys them. Deploy the release through publish-release-manifest.sh --deploy or ship-app.mjs, or stage elsewhere. Not deployed." >&2; return 1; fi
  return 0
}
if [ "$DEPLOY" = 1 ] && ! jobs_only_check; then DEPLOY=0; echo "the jobs file is written and signed locally; the deploy was refused" >&2; exit 1; fi
if [ "$DEPLOY" = 1 ]; then
  [ -n "$DLSITE" ] || { echo "--deploy needs the real downloads folder (no --dest)" >&2; exit 1; }
  echo "deploying $DLSITE"
  (cd "$DLSITE" && npx --yes vercel@latest --global-config "$HOME/.config/igneum/vercel" deploy --prod --yes 2>&1 | sed "s#$TOKEN#<token>#g"; exit "${PIPESTATUS[0]}") \
    || { echo "the deploy failed (the Vercel CLI's exit status above); nothing verified" >&2; exit 1; }
  verify_live "$TRIES" || exit 1
  wake_apps "$ID"
  echo "the apps fetch it within seconds when woken, else within 2 minutes (Settings > remote jobs > Check now at once); results: node tools/jobs.mjs ${ID:-<id>}"
else
  if [ -n "$DLSITE" ]; then
    echo "not deployed: cd $DLSITE && npx --yes vercel@latest --global-config $IGNEUM_SECRETS_DIR/vercel deploy --prod --yes   (or re-run with --deploy)"
  else
    echo "written to $DEST for ${BASE//$TOKEN/<token>} (test file; not the downloads folder)"
  fi
fi
