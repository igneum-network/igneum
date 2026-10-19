#!/usr/bin/env python3
"""legacy-vault-make.py <path> <password>: writes a vault file in the previous wallet's format (its vault.rs of 8
October 2026): the address and the source in clear, Argon2id over the password (the parameters in the file; light
here so the row is quick), XChaCha20-Poly1305 over the JSON of {private_key, mnemonic} with no associated data, hex
without 0x. The secret is the test phrase of the smokes (the first anvil account); the address is printed. Needs
argon2-cffi and pycryptodome. A fixture for wallet/tools/legacy-vault-smoke.sh, never a wallet of anyone's."""
import json, os, sys, time
from argon2.low_level import hash_secret_raw, Type
from Crypto.Cipher import ChaCha20_Poly1305

PHRASE = "test test test test test test test test test test test junk"
KEY = "0xac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"
ADDRESS = "0xf39fd6e51aad88f6f4ce6ab8827279cfffb92266"


def main(path, password):
    salt, nonce = os.urandom(16), os.urandom(24)
    m_kib, t, p = 8 * 1024, 1, 1
    key = hash_secret_raw(password.encode(), salt, time_cost=t, memory_cost=m_kib, parallelism=p, hash_len=32, type=Type.ID)
    plain = json.dumps({"private_key": KEY, "mnemonic": PHRASE}).encode()
    c = ChaCha20_Poly1305.new(key=key, nonce=nonce)
    ct, tag = c.encrypt_and_digest(plain)
    v = {"version": 1, "address": ADDRESS, "source": "seed", "created": int(time.time()),
         "kdf": {"algo": "argon2id", "m_kib": m_kib, "t": t, "p": p, "salt": salt.hex()},
         "cipher": "xchacha20poly1305", "nonce": nonce.hex(), "ciphertext": (ct + tag).hex(), "backed_up": True}
    with open(path, "w") as f:
        json.dump(v, f, indent=2)
    os.chmod(path, 0o600)
    print(ADDRESS)


if __name__ == "__main__":
    if len(sys.argv) != 3:
        print(__doc__); sys.exit(2)
    main(sys.argv[1], sys.argv[2])
