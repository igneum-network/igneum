#!/bin/bash
# The configuration the packagers write next to the engine (igneum-app.json): the update manifest URL, the log intake,
# the live page. Sourced by packaging/mac/build-dmg.sh and packaging/windows/make-payload.sh (on the Mac and on the
# GitHub runner alike). Run on its own (`packaging/mac/packaged-config.sh --test`) it checks itself.
#
# No secret lives in this file (rotation phase 2, 5 October 2026: the upload key used to be a literal here and is in
# eight commits of the history; docs/plans/rotation-phase-2.md and docs/plans/history-rewrite.md). Both values come
# from files named by two environment variables, each defaulting to the NEXT value when one is staged:
#
#   IGNEUM_UPLOAD_KEY_FILE   the log upload key (authorises log uploads only; it ships inside every package).
#                            Default: $IGNEUM_SECRETS_DIR/upload-key.next when that file exists, else
#                            $IGNEUM_SECRETS_DIR/upload-key.
#   IGNEUM_DL_TOKEN_FILE     the downloads path token: the manifest is https://dl.igneum.network/ember/latest/igneum-app-latest.json.
#                            Default: $IGNEUM_SECRETS_DIR/dl-token.next when that file exists, else $IGNEUM_SECRETS_DIR/dl-token.
#
# So the an earlier release build carries the rotated key and checks the manifest in the NEW folder with no flag at all, while a
# build that must target the old folder says so: IGNEUM_DL_TOKEN_FILE=$IGNEUM_SECRETS_DIR/dl-token. When the rotation is
# over, `mv upload-key.next upload-key` and `mv dl-token.next dl-token` make the defaults the plain files
# again. A missing or empty token file disables the update check (the note says so); a missing or empty key file
# disables log uploads. Values are never printed, only the file names and the values' lengths.
LOG_URL="${IGNEUM_INTAKE_URL:-https://igneum-six.vercel.app/api/log}"
# the old base (docs/product-a/OLD-BASE.md step 3): no observer page serves the pinned chain; an empty address makes the
# window read the chain through the node the user names as the work source (the engine's node URL) and nothing else
LIVE_PAGE="${IGNEUM_LIVE_PAGE:-}"
DOWNLOAD_PAGE="https://igneum.network/#mine"
# the release store on the public host (0.2.3: releases/ember/<version>/ and latest/ on build.igneum.network)
DL_HOST="https://build.igneum.network/releases"

# ---- PRODUCT=public|lab (the founder's word at 20:21 UK, 8 October 2026): ONE variable the packagers read -------------
# public: "Ember", the release root, channel ember, manifest igneum-app-latest.json.
# lab:    "Ember Lab", the lab signing root (IGNEUM_LAB_PUBLIC_KEY or $IGNEUM_SECRETS_DIR/lab-signing/lab-signing-key.pub,
#         the hex only, never the private key), channel ember-lab, manifest igneum-app-lab-latest.json in the
#         same downloads folder, its own bundle id, installer AppId, install folder and package names, so a lab install
#         never updates from, or over, the public one. The engine built for the package must be the matching build
#         (cargo --features lab for lab): the engine reads the edition and the root hex back from igneum-app.json and
#         refuses a mismatch (src/config.rs Packaged::edition_mismatch).
PRODUCT="${PRODUCT:-public}"
# the release root the engine compiles in (app/igneum-app/src/manifest.rs OTA_PUBLIC_KEY_HEX); the lab root is read, never written here
# the release signing key (infra/release/keys/release-signing.pub, ssh-ed25519, raw 32 bytes; the engine's own root
# since 0.2.1): the packaged root must equal it or every update check logs "package mismatch" (the morning of
# 12 October 2026: the packager wrote the older root b3c9c5bd while the engine's was c1895edd)
PUBLIC_OTA_ROOT_HEX="c1895edd5adc6c13c2eda472aee2324a854c0dca5023a80e592bd41588f144c5"
case "$PRODUCT" in
    public)
        PRODUCT_NAME="Ember"; PRODUCT_FILE="Ember"; PRODUCT_SLUG="igneum"
        PRODUCT_BUNDLE_ID="network.igneum.ember"; PRODUCT_CHANNEL="ember"; PRODUCT_MANIFEST_NAME="igneum-app-latest.json"
        PRODUCT_WINDOWS_APPID="{A4C1F0E2-6B8D-4E7A-9F31-2C5D8E7B9A01}"; PRODUCT_PAYLOAD_DIR="ember-windows-app" ;;
    lab)
        PRODUCT_NAME="Ember Lab"; PRODUCT_FILE="Ember-Lab"; PRODUCT_SLUG="igneum-lab"
        PRODUCT_BUNDLE_ID="network.igneum.ember.lab"; PRODUCT_CHANNEL="ember-lab"; PRODUCT_MANIFEST_NAME="igneum-app-lab-latest.json"
        PRODUCT_WINDOWS_APPID="{5E2B7C41-9D3A-4F06-B8E7-61C4A2D9F0B3}"; PRODUCT_PAYLOAD_DIR="ember-windows-app-lab" ;;
    *) echo "PRODUCT must be public or lab (got '$PRODUCT')" >&2; exit 2 ;;
esac
# igneum_ota_root_hex -> the signing root the package's engine must carry: the release root, or the lab root from the
# variable or the key file (64 hex); empty with a note when a lab package has no root to read
igneum_ota_root_hex() {
    if [ "$PRODUCT" = "public" ]; then printf '%s' "$PUBLIC_OTA_ROOT_HEX"; return 0; fi
    local hex="${IGNEUM_LAB_PUBLIC_KEY:-}"
    [ -n "$hex" ] || hex="$(igneum_read_trimmed "${IGNEUM_CONFIG_DIR:-$HOME/.config/igneum}/lab-signing/lab-signing-key.pub")"
    printf '%s' "$hex" | grep -qE '^[0-9a-f]{64}$' || { echo "lab package: no lab root (IGNEUM_LAB_PUBLIC_KEY or $IGNEUM_SECRETS_DIR/lab-signing/lab-signing-key.pub, 64 hex)" >&2; return 1; }
    printf '%s' "$hex"
}
# Consensus parameters pinned into the package for the bundled node: igneum-app.json "node_override_params"; the engine
# writes them to <app data>/override-params.json and starts igneumd with --override-params-file. Empty = no override
# file, the node runs the network's defaults. Difficulty v2 (4 Oct 2026): the version that bundles igneumd v2 must
# carry the devnet's activation height here, the same N as every other devnet node, before it is cut (Mac and CI alike:
# make-payload.sh sources this file). Rule and order: docs/plans/difficulty-v2-rollout-devnet.md.
# Example: NODE_OVERRIDE_PARAMS='{"difficulty_v2_activation_daa": 123456}'
# the live SIXTEEN-field object (publish 2 of an earlier release/an earlier release, 6 October 2026 22:43Z, digest eada4bda): a fresh install starts
# its node on the network's current object and peers at once; the manifest's consensus.override replaces it on the first read
# an earlier release (7 October 2026): the package named the network its node joined; that chain carried every parameter
# in its network object and the node refuses an override file there, so NODE_OVERRIDE_PARAMS is empty and the two
# network fields are set instead: the suffix (--devnet-suffix=3) and the static peers (the store host's seed of the time and
# node1-dn3; the fleet's genesis boxes have no stable public port and stay the fleet's seeds). The shared devnet's
# sixteen-field object of an earlier release is kept below for the record; a 0.1.2x package for the shared devnet would set it again.
NODE_OVERRIDE_PARAMS=''
NODE_DEVNET_SUFFIX='4'
# six dial targets across five boxes (8 October 2026, 19:2x UK: every target was down at once and every home miner lost its
# peers): the store host's seed and hand, the two fleet seeds, the two held hubs; a roll or heal never leaves fewer than two listening
# an earlier release (9 October 2026): 213.173.107.74:16516 is hub-1's standing pre-2.0 node (network igneum-devnet), never a peer of the later chain
# (hub-1's later node has no external port); every fresh sync tonight went through the three lp hubs; the store host's two hand
# nodes join the list as ordinary hub-chain peers (since 09:49 UK they run fresh datadirs on the hubs' chain with the three lp
# hubs as their peers), five dial targets across four boxes; the hubs run no an earlier release record store yet (it arrives when they take
# 27f54124 after the canary on the moving chain; the node lane's word, 10:22 UK)
NODE_PEERS='["213.173.98.75:21169","213.173.110.229:38511","213.173.108.150:12524","188.40.146.49:26631","188.40.146.49:26671"]'
# the shared devnet's object as an earlier release shipped it (digest 4bbbe816 on the pin): '{"difficulty_v2_activation_daa":33000,"exec_restart_hash":"bb45cf0dd2d7cc97ebfa5a2701527c09a8ede5d32de74efead9caa293b15688a","exec_restart_number":27276,"exec_restart_state_root":"0xed27bb2d6128daf50493ed5539a73bb93f9d7c63ad7f70a7cbb46ba81c9e164e","exec_restart_trust_daa":200000,"fees_v1_activation_daa":210000,"finality_v3_activation_daa":135200,"program_class_v3_activation_daa":154800,"program_class_v4_activation_daa":831600,"program_class_v4_signal_window_daa":86400,"proving_v0_activation_daa":84100,"proving_v1_activation_daa":154800,"proving_v1_aggregator_share_bps":1000,"proving_v1_fresh_rule_daa":198000,"proving_v1_segment_blocks":8,"proving_v1_unproven_daa":600}'
# igneum_secret_file <env var name> <base name> -> the file to read: the variable when set, else <base>.next when it
# exists, else <base>; IGNEUM_CONFIG_DIR (tests) replaces $IGNEUM_SECRETS_DIR/igneum
igneum_secret_file() {
    local var="$1" base="$2" dir="${IGNEUM_CONFIG_DIR:-$HOME/.config/igneum}" set_value=""
    set_value="${!var:-}"
    if [ -n "$set_value" ]; then printf '%s' "$set_value"
    elif [ -f "$dir/$base.next" ]; then printf '%s' "$dir/$base.next"
    else printf '%s' "$dir/$base"
    fi
}
# igneum_read_trimmed <file> -> the file's content without whitespace, or nothing when the file is missing or blank
igneum_read_trimmed() {
    [ -f "$1" ] && tr -d '[:space:]' < "$1" || true
}
# igneum_manifest_url <token> -> the manifest URL. The old base (step 5, 11 October 2026): the manifest lives on the
# release store at one fixed path (releases/ember/latest/, reliability's), served at $DL_HOST/ember/latest/; the
# downloads-folder token is read no more and an empty one makes no difference (the argument stays for the callers).
igneum_manifest_url() {
    printf '%s/ember/latest/%s' "$DL_HOST" "$PRODUCT_MANIFEST_NAME"
}
# igneum_fingerprint <value> -> the first 8 hex of sha256 over the value, for logs and the app's header (never the value)
igneum_fingerprint() {
    printf '%s' "$1" | { shasum -a 256 2>/dev/null || sha256sum; } | cut -c1-8
}

# writes the JSON to $1; prints which files were used (names), the lengths and the fingerprints, never the values
write_packaged_config() {
    local out="$1" token="" manifest="" key="" key_file="" token_file="" root=""
    root="$(igneum_ota_root_hex)" || return 1
    echo "product: $PRODUCT ($PRODUCT_NAME, channel $PRODUCT_CHANNEL, root fingerprint $(igneum_fingerprint "$root"))"
    key_file="$(igneum_secret_file IGNEUM_UPLOAD_KEY_FILE upload-key)"
    token_file="$(igneum_secret_file IGNEUM_DL_TOKEN_FILE dl-token)"
    key="$(igneum_read_trimmed "$key_file")"
    token="$(igneum_read_trimmed "$token_file")"
    manifest="$(igneum_manifest_url "$token")"
    if [ -n "$key" ]; then echo "upload key: $key_file (${#key} chars, fingerprint $(igneum_fingerprint "$key"))"
    else echo "note: no key in $key_file; log uploads are disabled in this build"; fi
    if [ -n "$manifest" ]; then echo "manifest: $token_file (${#token} chars, fingerprint $(igneum_fingerprint "$token")) -> ${manifest//$token/<token>}"
    else echo "note: no token in $token_file; the update check is disabled in this build"; fi
    local override_line="" network_lines=""
    [ -n "$NODE_OVERRIDE_PARAMS" ] && override_line="  \"node_override_params\": $NODE_OVERRIDE_PARAMS,"
    [ -n "${NODE_DEVNET_SUFFIX:-}" ] && network_lines="  \"node_devnet_suffix\": $NODE_DEVNET_SUFFIX,"
    [ -n "${NODE_PEERS:-}" ] && network_lines="$network_lines
  \"node_peers\": $NODE_PEERS,"
    cat > "$out" <<JSON
{
$override_line
$network_lines
  "edition": "$PRODUCT",
  "product": "$PRODUCT_NAME",
  "channel": "$PRODUCT_CHANNEL",
  "ota_root_hex": "$root",
  "update_manifest": "$manifest",
  "upload_url": "$LOG_URL",
  "upload_key": "$key",
  "live_page": "$LIVE_PAGE",
  "download_page": "$DOWNLOAD_PAGE"
}
JSON
}

# ---- self-test: packaging/mac/packaged-config.sh --test (temporary files only; nothing under $IGNEUM_SECRETS_DIR is read) -----
if [ "${BASH_SOURCE[0]}" = "$0" ]; then
    set -euo pipefail
    # --lab-probe <out>: writes one config with the environment's PRODUCT (the self-test's child for the lab rows)
    if [ "${1:-}" = "--lab-probe" ]; then write_packaged_config "$2"; exit $?; fi
    [ "${1:-}" = "--test" ] || { echo "usage: $0 --test   (otherwise source this file)" >&2; exit 2; }
    T="$(mktemp -d)"; trap 'rm -rf "$T"' EXIT
    PY="$(command -v python3 || command -v python)"   # the GitHub Windows runner's Git Bash may only have python
    fails=0
    check() { if [ "$2" = "$3" ]; then echo "  ok   $1"; else echo "  FAIL $1: got '$2', want '$3'"; fails=$((fails + 1)); fi; }
    export IGNEUM_CONFIG_DIR="$T/cfg"; mkdir -p "$T/cfg"
    unset IGNEUM_UPLOAD_KEY_FILE IGNEUM_DL_TOKEN_FILE
    # 1. nothing staged: the plain files
    check "default key file is the plain one" "$(igneum_secret_file IGNEUM_UPLOAD_KEY_FILE upload-key)" "$T/cfg/upload-key"
    check "default token file is the plain one" "$(igneum_secret_file IGNEUM_DL_TOKEN_FILE dl-token)" "$T/cfg/dl-token"
    # 2. a .next file wins
    printf 'nextkeyvalue0123456789abcdef\n' > "$T/cfg/upload-key.next"
    printf 'plainkeyvalue0123456789abcdef\n' > "$T/cfg/upload-key"
    printf 'tok2new\n' > "$T/cfg/dl-token.next"
    printf 'tok1old\n' > "$T/cfg/dl-token"
    check ".next key file wins when present" "$(igneum_secret_file IGNEUM_UPLOAD_KEY_FILE upload-key)" "$T/cfg/upload-key.next"
    check ".next token file wins when present" "$(igneum_secret_file IGNEUM_DL_TOKEN_FILE dl-token)" "$T/cfg/dl-token.next"
    # 3. the variable beats both
    check "the variable names the file" "$(IGNEUM_DL_TOKEN_FILE="$T/cfg/dl-token" igneum_secret_file IGNEUM_DL_TOKEN_FILE dl-token)" "$T/cfg/dl-token"
    # 4. reading trims; a missing file reads as nothing
    check "trimmed read" "$(igneum_read_trimmed "$T/cfg/dl-token.next")" "tok2new"
    check "missing file reads empty" "$(igneum_read_trimmed "$T/cfg/none")" ""
    check "manifest url (the release store's fixed path; the token is read no more)" "$(igneum_manifest_url tok2new)" "$DL_HOST/ember/latest/igneum-app-latest.json"
    check "an empty token gives the same manifest" "$(igneum_manifest_url '')" "$DL_HOST/ember/latest/igneum-app-latest.json"
    check "fingerprint is 8 hex" "$(igneum_fingerprint abc | grep -cE '^[0-9a-f]{8}$')" "1"
    check "fingerprint of abc" "$(igneum_fingerprint abc)" "ba7816bf"
    # 5. the written JSON: defaults pick the .next pair; the values land; nothing printed carries them
    out="$(NODE_OVERRIDE_PARAMS='' write_packaged_config "$T/a.json")"
    check "output names the .next key file" "$(printf '%s' "$out" | grep -c 'upload-key.next')" "1"
    check "output never carries the key" "$(printf '%s' "$out" | grep -c 'nextkeyvalue')" "0"
    check "output never carries the token" "$(printf '%s' "$out" | grep -c 'tok2new')" "0"
    check "json carries the .next key" "$("$PY" -c 'import json,sys; print(json.load(open(sys.argv[1]))["upload_key"])' "$T/a.json")" "nextkeyvalue0123456789abcdef"
    check "json manifest points at the .next folder" "$("$PY" -c 'import json,sys; print(json.load(open(sys.argv[1]))["update_manifest"])' "$T/a.json")" "$DL_HOST/ember/latest/igneum-app-latest.json"
    check "json has no override line" "$("$PY" -c 'import json,sys; print("node_override_params" in json.load(open(sys.argv[1])))' "$T/a.json")" "False"
    # 5b. an earlier release: the network fields land as JSON when set, and are absent when unset
    check "json names the devnet suffix" "$("$PY" -c 'import json,sys; print(json.load(open(sys.argv[1])).get("node_devnet_suffix"))' "$T/a.json")" "$NODE_DEVNET_SUFFIX"
    check "json carries the static peers (the list's own length)" "$("$PY" -c 'import json,sys; print(len(json.load(open(sys.argv[1])).get("node_peers", [])))' "$T/a.json")" "$("$PY" -c 'import json,sys; print(len(json.loads(sys.argv[1])))' "$NODE_PEERS")"
    NODE_DEVNET_SUFFIX='' NODE_PEERS='' write_packaged_config "$T/d.json" >/dev/null
    check "no network fields when unset" "$("$PY" -c 'import json,sys; j=json.load(open(sys.argv[1])); print("node_devnet_suffix" in j or "node_peers" in j)' "$T/d.json")" "False"
    # 6. the variables aim a build at the old folder and the old key
    out="$(IGNEUM_UPLOAD_KEY_FILE="$T/cfg/upload-key" IGNEUM_DL_TOKEN_FILE="$T/cfg/dl-token" write_packaged_config "$T/b.json")"
    check "old-folder build: manifest" "$("$PY" -c 'import json,sys; print(json.load(open(sys.argv[1]))["update_manifest"])' "$T/b.json")" "$DL_HOST/ember/latest/igneum-app-latest.json"
    check "old-folder build: key" "$("$PY" -c 'import json,sys; print(json.load(open(sys.argv[1]))["upload_key"])' "$T/b.json")" "plainkeyvalue0123456789abcdef"
    # 7. missing files: notes, empty fields, valid JSON
    out="$(IGNEUM_UPLOAD_KEY_FILE="$T/cfg/nokey" IGNEUM_DL_TOKEN_FILE="$T/cfg/notoken" write_packaged_config "$T/c.json")"
    check "missing key noted" "$(printf '%s' "$out" | grep -c 'log uploads are disabled')" "1"
    check "a missing token disables nothing now (the manifest is on the fixed path)" "$(printf '%s' "$out" | grep -c 'update check is disabled')" "0"
    check "missing files give empty fields (the manifest URL stays: it is the fixed path)" "$("$PY" -c 'import json,sys; j=json.load(open(sys.argv[1])); print(j["update_manifest"]+"|"+j["upload_key"])' "$T/c.json")" "$DL_HOST/ember/latest/igneum-app-latest.json|"
    # 8. the override line
    NODE_OVERRIDE_PARAMS='{"difficulty_v2_activation_daa": 123456}' write_packaged_config "$T/d.json" >/dev/null
    check "override params land" "$("$PY" -c 'import json,sys; print(json.load(open(sys.argv[1]))["node_override_params"]["difficulty_v2_activation_daa"])' "$T/d.json")" "123456"
    # 9. PRODUCT=public|lab (8 October 2026): the table, the manifest name, the root, the refusal without a lab root
    check "public edition lands" "$("$PY" -c 'import json,sys; j=json.load(open(sys.argv[1])); print(j["edition"], j["product"], j["channel"], j["ota_root_hex"])' "$T/a.json")" "public Ember ember $PUBLIC_OTA_ROOT_HEX"
    lab_out="$(PRODUCT=lab IGNEUM_LAB_PUBLIC_KEY="$(printf 'ab%.0s' $(seq 32))" bash "${BASH_SOURCE[0]}" --lab-probe "$T/lab.json" 2>&1)"
    check "lab edition lands" "$("$PY" -c 'import json,sys; j=json.load(open(sys.argv[1])); print(j["edition"], j["product"], j["channel"], j["ota_root_hex"][:6])' "$T/lab.json")" "lab Ember Lab ember-lab ababab"
    check "lab manifest has its own name" "$("$PY" -c 'import json,sys; print(json.load(open(sys.argv[1]))["update_manifest"])' "$T/lab.json")" "$DL_HOST/ember/latest/igneum-app-lab-latest.json"
    check "lab output never carries the root" "$(printf '%s' "$lab_out" | grep -c 'abababab')" "0"
    check "lab without a root is refused" "$(PRODUCT=lab IGNEUM_LAB_PUBLIC_KEY= IGNEUM_CONFIG_DIR="$T/cfg" bash "${BASH_SOURCE[0]}" --lab-probe "$T/lab2.json" >/dev/null 2>&1; echo $?)" "1"
    check "a bad PRODUCT is refused" "$(PRODUCT=beta bash "${BASH_SOURCE[0]}" --lab-probe "$T/lab3.json" >/dev/null 2>&1; echo $?)" "2"
    if [ "$fails" = 0 ]; then echo "packaged-config: all checks passed"; else echo "packaged-config: $fails check(s) failed"; exit 1; fi
fi
