#!/usr/bin/env sh
# The wallet's isolation and permission rows (the outside review's litepaper reading, 11 October 2026: the wallet's security
# sentences may be served as fact only once these pass):
#   ISO-01  the signer process is isolated from the page process: its own pid under the engine, stdio pipes to the
#           engine and nothing else open, an environment of PATH alone, no socket of its own; the page reaches only
#           the engine's two loopback listeners
#   ISO-02  no network reach from the signer: no network crate in its tree, no socket symbol imported by its binary,
#           and no network system call in a traced session (open, create, status, sign, reveal, lock, unlock)
#   ISO-03  nothing is released to a page before the person's yes: no address before connect, no ask for a signing
#           request before connect, an ask that waits with nothing returned, a no that returns 4001 and leaves the
#           address, the signature and the nonce where they were; one yes at the end as the positive control
# Run from wallet/ on a Linux box after `cargo build` (needs /proc, ss, nm, strace, cargo, python3, curl) against a
# node (NODE_RPC, default the dev node) that funds the test seed's first address. Every row prints
# `ROW <id> PASS|FAIL <detail>`; the rows go to $WORK/isolation-rows.json and to RECORD_DIR; exit 1 on any FAIL.
# The engine is stopped by the pid recorded at its start, after its command line is read back.
set -u
cd "$(dirname "$0")/.."
BIN=$(pwd)/target/debug
RPC=${NODE_RPC:-http://127.0.0.1:8545}
BPORT=${BRIDGE_PORT:-26897}
RECORD=${RECORD_DIR:-${IGNEUM_BOX_ROOT:-/srv}/builds/product-b/work/isolation}
WORK=$(mktemp -d)
SHA=$(git rev-parse --short HEAD 2>/dev/null || echo unknown)
echo "work dir $WORK, tree $SHA, node $RPC"
ROWS="$WORK/rows.txt"
: > "$ROWS"
fail=0
row() { echo "ROW $1 $2 $3"; printf '%s\t%s\t%s\n' "$1" "$2" "$3" >> "$ROWS"; [ "$2" = PASS ] || fail=1; }
NET_SYMS='^(socket|connect|bind|listen|accept4?|sendto|recvfrom|sendmsg|recvmsg|getaddrinfo|getnameinfo|gethostbyname)(@.*)?$'
NET_CRATES='^(ureq|reqwest|hyper|tokio|mio|socket2|native-tls|rustls|openssl|openssl-sys|curl|curl-sys|async-std|smol|tiny_http|httparse)( |$)'
NET_CALLS='^[0-9]+ +(socket|socketpair|connect|bind|listen|accept4?|sendto|recvfrom|sendmsg|recvmsg|getsockopt|setsockopt|getpeername|getsockname)\('

# ---------- ISO-02, the static legs (no process needed) ----------
tree_hits=$(cargo tree -p igneum-signer -e normal --prefix none 2>/dev/null | sort -u | grep -E -c "$NET_CRATES")
sym_hits=$(nm -D --undefined-only "$BIN/igneum-signer" | awk '{print $NF}' | grep -E -c "$NET_SYMS")
engine_syms=$(nm -D --undefined-only "$BIN/igneum-wallet-engine" | awk '{print $NF}' | grep -E -c "$NET_SYMS")
echo "signer tree: $tree_hits network crates; signer imports: $sym_hits socket symbols; the engine imports $engine_syms (the control)"

# ---------- ISO-02, the traced session: the signer driven directly on its stdio under strace ----------
TRACE="$WORK/signer.strace"
REQ="$WORK/signer-requests.jsonl"
printf '%s\n' \
  "{\"id\":1,\"op\":\"open\",\"path\":\"$WORK/vault.json\"}" \
  '{"id":2,"op":"create","password":"isolation row","chain_id":7310}' \
  '{"id":3,"op":"status"}' \
  '{"id":4,"op":"sign_message","message":"0x68656c6c6f"}' \
  '{"id":5,"op":"reveal","password":"isolation row"}' \
  '{"id":6,"op":"lock"}' \
  '{"id":7,"op":"unlock","password":"isolation row"}' \
  '{"id":8,"op":"status"}' > "$REQ"
# the responses carry a fresh throwaway key (reveal); only id and ok are read out of them
IGNEUM_SIGNER_LIGHT_KDF=1 strace -f -e trace=network -o "$TRACE" "$BIN/igneum-signer" < "$REQ" > "$WORK/signer-responses.jsonl" 2> "$WORK/signer.err"
oks=$(python3 -c 'import sys,json; print(sum(1 for l in open(sys.argv[1]) if l.strip() and json.loads(l).get("ok") is True))' "$WORK/signer-responses.jsonl")
net_calls=$(grep -E -c "$NET_CALLS" "$TRACE")
# the control: the same trace on a process that does reach the network sees its calls
strace -f -e trace=network -o "$WORK/curl.strace" curl -s -m 5 -X POST "$RPC" -H 'Content-Type: application/json' -d '{"jsonrpc":"2.0","id":1,"method":"eth_chainId","params":[]}' > /dev/null 2>&1
control_calls=$(grep -E -c "$NET_CALLS" "$WORK/curl.strace")
if [ "$tree_hits" = 0 ] && [ "$sym_hits" = 0 ] && [ "$engine_syms" -gt 0 ] && [ "$oks" = 8 ] && [ "$net_calls" = 0 ] && [ "$control_calls" -gt 0 ]; then
  row ISO-02 PASS "no network crate in the signer's tree; 0 socket symbols imported (the engine imports $engine_syms); 8 of 8 ops answered under strace with 0 network calls (the control, curl, made $control_calls)"
else
  row ISO-02 FAIL "tree $tree_hits, symbols $sym_hits, engine control $engine_syms, ops ok $oks of 8, network calls $net_calls, control $control_calls"
fi

# ---------- the engine with the real signer, for ISO-01 and ISO-03 ----------
IGNEUM_SIGNER_LIGHT_KDF=1 "$BIN/igneum-wallet-engine" --data-dir "$WORK/engine" --rpc "$RPC" --signer "$BIN/igneum-signer" --ui app/ui --bridge-port "$BPORT" > "$WORK/engine.out" 2> "$WORK/engine.err" &
EPID=$!
echo "$EPID" > "$WORK/engine.pid"
stop_engine() {
  p=$(cat "$WORK/engine.pid" 2>/dev/null) || return 0
  case "$(ps -p "$p" -o command= 2>/dev/null)" in *igneum-wallet-engine*) kill "$p" 2>/dev/null || true ;; esac
}
trap stop_engine EXIT
sleep 3
URL=$(grep '^ENGINE' "$WORK/engine.out" | head -1 | sed 's/^ENGINE //' | python3 -c 'import sys,json; print(json.load(sys.stdin)["url"])')
post() { curl -s -X POST "$URL$1" -H 'Content-Type: application/json' -d "$2"; }
# the site's token (G8): the approved connect's reply carries it in the X-Igneum-Site header; the site sends it on
# every later call; site() keeps it in $WORK/site.token as the provider script keeps it for the tab
site() { curl -s -D "$WORK/site.hdr" -X POST "http://127.0.0.1:$BPORT/rpc" -H 'Content-Type: application/json' -H "Origin: https://shop.example" -H "X-Igneum-Site: $(cat "$WORK/site.token" 2>/dev/null)" -d "$1"; t=$(grep -i '^x-igneum-site:' "$WORK/site.hdr" | tr -d '\r' | awk '{print $2}'); [ -n "$t" ] && printf '%s' "$t" > "$WORK/site.token"; }
# a call that leaves the token out (a local process that never got the approval)
site_bare() { curl -s -X POST "http://127.0.0.1:$BPORT/rpc" -H 'Content-Type: application/json' -H "Origin: https://shop.example" -d "$1"; }
jget() { python3 -c 'import sys,json
d=json.load(sys.stdin)
for k in sys.argv[1:]:
    d=d[int(k)] if isinstance(d,list) else d.get(k)
    if d is None: break
print(json.dumps(d) if isinstance(d,(dict,list)) else ("" if d is None else d))' "$@"; }
pending_count() { curl -s "${URL}api/bridge/pending" | python3 -c 'import sys,json; print(len(json.load(sys.stdin)))'; }
decide_first() { ID=$(curl -s "${URL}api/bridge/pending" | python3 -c 'import sys,json; p=json.load(sys.stdin); print(p[0]["id"] if p else "")'); [ -n "$ID" ] && post api/bridge/decide "{\"id\":$ID,\"yes\":$1}" > /dev/null; }

post api/import '{"mode":"seed","data":"test test test test test test test test test test test junk","password":"correct horse battery"}' > /dev/null
STATE=$(curl -s "${URL}api/state")
SPID=$(echo "$STATE" | jget signer_pid)
ADDR=$(echo "$STATE" | jget address)

# ---------- ISO-01 ----------
iso1=PASS; why=""
[ -n "$SPID" ] && [ "$SPID" != "$EPID" ] || { iso1=FAIL; why="$why; signer pid '$SPID' is not its own"; }
ppid=$(awk '/^PPid:/{print $2}' "/proc/$SPID/status" 2>/dev/null)
[ "$ppid" = "$EPID" ] || { iso1=FAIL; why="$why; the signer's parent is $ppid, the engine is $EPID"; }
cmd=$(tr '\0' ' ' < "/proc/$SPID/cmdline" 2>/dev/null)
case "$cmd" in *igneum-signer*) ;; *) iso1=FAIL; why="$why; the pid's command line is '$cmd'";; esac
# the signer's own shape, read by itself through its status op (api/state carries it): the memory guard marks the
# signer not dumpable, so /proc/<pid>/fd and environ are hidden from an unprivileged outside reader (wallet-security,
# 12 October 2026); the signer reads its own entries whatever the flag
shape=$(curl -s "${URL}api/state" | python3 -c 'import sys,json; d=json.load(sys.stdin); print(json.dumps(d.get("signer_process") or {}))')
fds=$(printf '%s' "$shape" | python3 -c 'import sys,json; d=json.load(sys.stdin); print(" ".join(f["fd"] for f in d.get("fds", [])))')
[ "$fds" = "0 1 2" ] || { iso1=FAIL; why="$why; open fds '$fds' (expected 0 1 2)"; }
in0=$(printf '%s' "$shape" | python3 -c 'import sys,json; d=json.load(sys.stdin); print(next((f["target"] for f in d.get("fds", []) if f["fd"]=="0"), ""))')
out1=$(printf '%s' "$shape" | python3 -c 'import sys,json; d=json.load(sys.stdin); print(next((f["target"] for f in d.get("fds", []) if f["fd"]=="1"), ""))')
case "$in0" in pipe:*) ;; *) iso1=FAIL; why="$why; stdin is $in0, not a pipe";; esac
case "$out1" in pipe:*) ;; *) iso1=FAIL; why="$why; stdout is $out1, not a pipe";; esac
ends=0; for f in /proc/$EPID/fd/*; do l=$(readlink "$f"); [ "$l" = "$in0" ] && ends=$((ends+1)); [ "$l" = "$out1" ] && ends=$((ends+1)); done
[ "$ends" = 2 ] || { iso1=FAIL; why="$why; the engine holds $ends of the signer's 2 pipe ends"; }
socks=$(printf '%s' "$shape" | python3 -c 'import sys,json; print(json.load(sys.stdin).get("sockets", "?"))')
[ "$socks" = 0 ] || { iso1=FAIL; why="$why; the signer holds $socks socket(s)"; }
envkeys=$(printf '%s' "$shape" | python3 -c 'import sys,json; print(" ".join(k for k in json.load(sys.stdin).get("env", []) if k not in ("PATH", "IGNEUM_SIGNER_LIGHT_KDF")))')
[ -z "$envkeys" ] || { iso1=FAIL; why="$why; the signer's environment carries $envkeys"; }
socks=$(ss -apn 2>/dev/null | grep -c "pid=$SPID,")
[ "$socks" = 0 ] || { iso1=FAIL; why="$why; the signer owns $socks sockets"; }
listeners=$(ss -ltnp 2>/dev/null | grep "pid=$EPID," | awk '{print $4}' | tr '\n' ' ')
nonlocal=$(echo "$listeners" | tr ' ' '\n' | grep -v '^$' | grep -v -c '^127\.0\.0\.1:')
lcount=$(echo "$listeners" | wc -w | tr -d ' ')
[ "$nonlocal" = 0 ] && [ "$lcount" = 2 ] || { iso1=FAIL; why="$why; the engine listens on '$listeners' (expected two loopback listeners)"; }
if [ "$iso1" = PASS ]; then
  row ISO-01 PASS "signer pid $SPID under engine $EPID; fds 0 1 2 as pipes whose other ends the engine holds; environment PATH only; 0 sockets of its own; the page reaches the engine's two loopback listeners ($listeners) and nothing else"
else
  row ISO-01 FAIL "${why#; }"
fi

# ---------- ISO-03 ----------
iso3=PASS; why=""
r=$(site '{"jsonrpc":"2.0","id":1,"method":"eth_accounts","params":[]}' | jget result)
[ "$r" = "[]" ] || { iso3=FAIL; why="$why; eth_accounts before connect gave $r"; }
code=$(site '{"jsonrpc":"2.0","id":2,"method":"personal_sign","params":["0x68656c6c6f","'"$ADDR"'"]}' | jget error code)
pc=$(pending_count)
[ "$code" = 4100 ] && [ "$pc" = 0 ] || { iso3=FAIL; why="$why; personal_sign before connect gave code '$code' with $pc asks pending"; }
# the connect ask waits: nothing returned while it is open
site '{"jsonrpc":"2.0","id":3,"method":"eth_requestAccounts","params":[]}' > "$WORK/connect.out" & SP=$!
sleep 2
pc=$(pending_count); sites=$(curl -s "${URL}api/state" | jget bridge sites)
[ "$pc" = 1 ] && [ ! -s "$WORK/connect.out" ] && [ "$sites" = "[]" ] || { iso3=FAIL; why="$why; with the connect ask open: $pc pending, $(wc -c < "$WORK/connect.out") bytes returned, sites $sites"; }
decide_first false; wait $SP
code=$(jget error code < "$WORK/connect.out")
r=$(site '{"jsonrpc":"2.0","id":4,"method":"eth_accounts","params":[]}' | jget result)
[ "$code" = 4001 ] && [ "$r" = "[]" ] || { iso3=FAIL; why="$why; after no: code '$code', eth_accounts $r"; }
# connect with a yes, then a signing ask that waits and is declined
site '{"jsonrpc":"2.0","id":5,"method":"eth_requestAccounts","params":[]}' > "$WORK/connect2.out" & SP=$!
sleep 1; decide_first true; wait $SP
# the approved connect minted the site's token (the reply's header); a call without it after the approval is a local
# process riding the approval: refused 4100 with no ask (G8, A-IPC)
[ -s "$WORK/site.token" ] || { iso3=FAIL; why="$why; the approved connect gave no site token"; }
bare=$(site_bare '{"jsonrpc":"2.0","id":55,"method":"personal_sign","params":["0x68656c6c6f","'"$ADDR"'"]}' | jget error code)
pcb=$(curl -s "${URL}api/bridge/pending" | python3 -c 'import sys,json; print(len(json.load(sys.stdin)))')
[ "$bare" = 4100 ] && [ "$pcb" = 0 ] || { iso3=FAIL; why="$why; a signing call without the site token after the approval gave code '$bare' with $pcb asks pending"; }
got=$(jget result 0 < "$WORK/connect2.out")
[ "$(echo "$got" | tr 'A-F' 'a-f')" = "$(echo "$ADDR" | tr 'A-F' 'a-f')" ] || { iso3=FAIL; why="$why; the connect with a yes returned '$got'"; }
site '{"jsonrpc":"2.0","id":6,"method":"personal_sign","params":["0x68656c6c6f","'"$ADDR"'"]}' > "$WORK/sign.out" & SP=$!
sleep 2
pc=$(pending_count)
[ "$pc" = 1 ] && [ ! -s "$WORK/sign.out" ] || { iso3=FAIL; why="$why; with the sign ask open: $pc pending, $(wc -c < "$WORK/sign.out") bytes returned"; }
decide_first false; wait $SP
code=$(jget error code < "$WORK/sign.out"); sig=$(jget result < "$WORK/sign.out")
[ "$code" = 4001 ] && [ -z "$sig" ] || { iso3=FAIL; why="$why; the declined sign gave code '$code', result '$sig'"; }
# a transaction declined: the nonce on the node and the history unchanged
nonce_before=$(curl -s -X POST "$RPC" -H 'Content-Type: application/json' -d '{"jsonrpc":"2.0","id":1,"method":"eth_getTransactionCount","params":["'"$ADDR"'","pending"]}' | jget result)
site '{"jsonrpc":"2.0","id":7,"method":"eth_sendTransaction","params":[{"from":"'"$ADDR"'","to":"0x70997970C51812dc3A010C7d01b50e0d17dc79C8","value":"0x1"}]}' > "$WORK/send.out" & SP=$!
sleep 2
pc=$(pending_count)
[ "$pc" = 1 ] && [ ! -s "$WORK/send.out" ] || { iso3=FAIL; why="$why; with the send ask open: $pc pending, $(wc -c < "$WORK/send.out") bytes returned"; }
decide_first false; wait $SP
code=$(jget error code < "$WORK/send.out")
nonce_after=$(curl -s -X POST "$RPC" -H 'Content-Type: application/json' -d '{"jsonrpc":"2.0","id":1,"method":"eth_getTransactionCount","params":["'"$ADDR"'","pending"]}' | jget result)
hist=$(curl -s "${URL}api/state" | jget history | python3 -c 'import sys,json; print(len(json.load(sys.stdin)))')
[ "$code" = 4001 ] && [ "$nonce_before" = "$nonce_after" ] && [ "$hist" = 0 ] || { iso3=FAIL; why="$why; the declined send gave code '$code', nonce $nonce_before to $nonce_after, history $hist"; }
# the positive control: one yes releases exactly one signature
site '{"jsonrpc":"2.0","id":8,"method":"personal_sign","params":["0x68656c6c6f","'"$ADDR"'"]}' > "$WORK/sign2.out" & SP=$!
sleep 1; decide_first true; wait $SP
sig=$(jget result < "$WORK/sign2.out")
[ "${#sig}" = 132 ] || { iso3=FAIL; why="$why; the sign with a yes returned ${#sig} characters"; }
socks=$(ss -apn 2>/dev/null | grep -c "pid=$SPID,")
[ "$socks" = 0 ] || { iso3=FAIL; why="$why; the signer owns $socks sockets after the session"; }
if [ "$iso3" = PASS ]; then
  row ISO-03 PASS "eth_accounts [] before connect; a signing request before connect refused (4100) with no ask; the approved connect minted the site token and a signing call without it was refused (4100) with no ask; the connect, sign and send asks each waited 2 s with nothing returned; each no gave 4001 and left the address list, the signature and the nonce ($nonce_before) unchanged with an empty history; one yes gave one 65-byte signature; the signer still owns 0 sockets"
else
  row ISO-03 FAIL "${why#; }"
fi

python3 - "$ROWS" "$WORK/isolation-rows.json" "$SHA" "$RPC" "$fail" <<'PY'
import sys, json, socket, time
rows = [dict(zip(("id", "verdict", "detail"), l.rstrip("\n").split("\t", 2))) for l in open(sys.argv[1]) if l.strip()]
out = {"tree": sys.argv[3], "box": socket.gethostname(), "time_utc": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()), "node": sys.argv[4], "verdict": "FAIL" if sys.argv[5] == "1" else "PASS", "rows": rows}
json.dump(out, open(sys.argv[2], "w"), indent=1)
print(json.dumps(out, indent=1))
PY
mkdir -p "$RECORD" 2>/dev/null && cp "$WORK/isolation-rows.json" "$RECORD/isolation-$SHA.json" 2>/dev/null && echo "record: $RECORD/isolation-$SHA.json"
[ "$fail" = 0 ] && echo "ISOLATION-TESTS-DONE PASS" || echo "ISOLATION-TESTS-DONE FAIL"
exit $fail
