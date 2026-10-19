#!/usr/bin/env sh
# The pool member's signed receipts, end to end: the ecosystem's reference client (pool/cmd/nc-miner, the igneumhash
# build) mines against the non-custodial listener for a minute with -receipts-out, then the wallet verifies the
# receipts under the pool's receipt key (the subscribe result's receipt_key, printed by the client) and the member's
# own key; then one receipt's weight is altered in a copy and the wallet names the break. Run on the box from
# wallet/ after `cargo build`, as one plain command. POOL=host:port of a signed-share listener (pool-pay-nc on
# builder nine's mesh address, named by IGNEUM_MESH_POOL); ACCOUNT the payout address the client binds (any address; a test identity).
set -eu
cd "$(dirname "$0")/.."
BIN=target/debug
POOL=${POOL:-${IGNEUM_MESH_POOL:?set the mesh pool host:port}}
ACCOUNT=${ACCOUNT:-0x8EfB30577370A071D77c63f3BaB7Da0A7af96bE0}
FOR=${FOR:-60s}
D=${AUTH_DIR:-${IGNEUM_BOX_ROOT:-/srv}/builds/product-b/work/authority}
mkdir -p "$D"; chmod 700 "$D"
LOG="$D/receipts-smoke-$(date -u +%Y%m%dT%H%M%SZ).log"
exec > "$LOG" 2>&1
echo "receipts smoke $(git rev-parse --short HEAD) pool $POOL $(date -u +%Y-%m-%dT%H:%M:%SZ)"

echo "--- the client (built with the node's igneumhash verifier)"
( cd ../pool && GOFLAGS=-mod=mod GOTOOLCHAIN=auto go build -modfile go.ih.mod -tags igneumhash -o "$D/nc-miner" ./cmd/nc-miner )
rm -f "$D/receipts.jsonl"
"$D/nc-miner" -pool "$POOL" -account "$ACCOUNT" -worker walletb -for "$FOR" -receipts-out "$D/receipts.jsonl" > "$D/nc-miner.log" 2>&1 || true
grep -E 'miner key|signs receipts under|accepted .* receipts verified' "$D/nc-miner.log" | tail -4
MK=$(grep -o 'miner key [0-9a-f]*' "$D/nc-miner.log" | head -1 | cut -d' ' -f3)
PK=$(grep -o 'signs receipts under [0-9a-f]*' "$D/nc-miner.log" | head -1 | cut -d' ' -f4)
echo "member key $MK pool key $PK lines $(wc -l < "$D/receipts.jsonl")"

echo "--- the wallet verifies the receipts"
"$BIN/igneum-wallet" receipts --file "$D/receipts.jsonl" --pool-key "$PK" --member-key "$MK" | head -8
"$BIN/igneum-wallet" receipts --file "$D/receipts.jsonl" --pool-key "$PK" --member-key "$MK" --json | python3 -c 'import sys,json; r=json.load(sys.stdin); print("verified", r["verified"], "bad", r["bad"], "weight_counted", r["weight_counted"], "pool_total", r["pool_total"])'

echo "--- one weight altered in a copy (expected: that receipt BAD on the digest, every later one not linked, exit 2)"
python3 - "$D/receipts.jsonl" "$D/receipts-tampered.jsonl" <<'EOF'
import sys, json
lines = [l for l in open(sys.argv[1]) if l.strip()]
out = open(sys.argv[2], "w")
for i, l in enumerate(lines):
    r = json.loads(l)
    if i == 2:
        r["weight"] = r["weight"] * 10
    out.write(json.dumps(r) + "\n")
EOF
"$BIN/igneum-wallet" receipts --file "$D/receipts-tampered.jsonl" --pool-key "$PK" --member-key "$MK" | head -6 || echo "exit $? as expected"

echo "--- a dropped receipt (expected: the next one not linked)"
python3 - "$D/receipts.jsonl" "$D/receipts-dropped.jsonl" <<'EOF'
import sys
lines = [l for l in open(sys.argv[1]) if l.strip()]
del lines[1]
open(sys.argv[2], "w").writelines(lines)
EOF
"$BIN/igneum-wallet" receipts --file "$D/receipts-dropped.jsonl" --pool-key "$PK" --member-key "$MK" | head -4 || echo "exit $? as expected"

echo "--- the engine's view of the same receipts"
export IGNEUM_SIGNER_LIGHT_KDF=1
RPC=${NODE_RPC:-${IGNEUM_MESH_RPC:?set the mesh RPC}}
rm -rf "$D/engine"
"$BIN/igneum-wallet-engine" --data-dir "$D/engine" --rpc "$RPC" --signer "$BIN/igneum-signer" --ui app/ui > "$D/engine.out" 2> "$D/engine.err" &
echo $! > "$D/engine.pid"
sleep 3
URL=$(grep '^ENGINE' "$D/engine.out" | head -1 | sed 's/^ENGINE //' | python3 -c 'import sys,json; print(json.load(sys.stdin)["url"])')
python3 - "$D/receipts.jsonl" "$PK" "$MK" <<'EOF' > "$D/receipts-body.json"
import sys, json
print(json.dumps({"lines": open(sys.argv[1]).read(), "pool_key": sys.argv[2], "member_key": sys.argv[3]}))
EOF
curl -s -X POST "${URL}api/receipts/verify" -H 'Content-Type: application/json' -d @"$D/receipts-body.json" | python3 -c 'import sys,json; r=json.load(sys.stdin); print("engine: verified", r["verified"], "bad", r["bad"], "pool_total", r["pool_total"])'
curl -s "${URL}api/state" | python3 -c 'import sys,json; s=json.load(sys.stdin)["network"]; print("engine network", s["name"], "pinned", s["pinned"], "registry", s["registry"])'
curl -s -X POST "${URL}api/authority/draw" -H 'Content-Type: application/json' -d '{}' | python3 -c 'import sys,json; d=json.load(sys.stdin); print("engine draw: key", d["key"][:18], "units", d["draw"]["units"], "eligible_round", d["eligible_round"])'
kill "$(cat "$D/engine.pid")" 2>/dev/null || true
echo "done $(date -u +%H:%M:%SZ)"
echo "$LOG"
