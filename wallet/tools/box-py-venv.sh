#!/usr/bin/env sh
# The wallet team's Python venv on a box, made once: builds/product-b/py under the box root (IGNEUM_BOX_ROOT) with argon2-cffi
# and pycryptodome (the legacy-vault fixture, wallet/tools/legacy-vault-make.py). Idempotent; prints the two packages' versions.
set -eu
V=${VENV:-${IGNEUM_BOX_ROOT:-/srv}/builds/product-b/py}
[ -x "$V/bin/python3" ] || python3 -m venv "$V"
"$V/bin/pip" install -q --disable-pip-version-check argon2-cffi pycryptodome
"$V/bin/python3" -c 'import argon2, Crypto; print("argon2-cffi", argon2.__version__, "pycryptodome", Crypto.__version__)'
