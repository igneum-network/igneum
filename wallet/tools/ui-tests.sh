#!/usr/bin/env sh
# The page's pure models under node (no browser): settlement.js, lock-screen.js and update-card.js with their
# *.test.mjs beside them (the two carried from the previous wallet, OLD-BASE.md step 3, the page). Run from wallet/
# on a box with node 20 or later; prints node's own summary and exits non-zero on a failing test.
set -u
cd "$(dirname "$0")/.."
out=$(node --test app/ui/settlement.test.mjs app/ui/lock-screen.test.mjs app/ui/update-card.test.mjs 2>&1)
rc=$?
printf '%s\n' "$out" | grep -E 'tests [0-9]|pass [0-9]|fail [0-9]|✖|not ok' | head -40
exit $rc
