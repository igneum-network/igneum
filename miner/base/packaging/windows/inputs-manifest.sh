#!/usr/bin/env bash
# The payload-inputs manifest writer, shared by push-inputs.sh (the real thing) and test-inputs-signing.sh (the
# Mac-side test), so the test signs and verifies exactly the shape the runner sees. Format: app/igneum-app/src/inputs.rs
# (`igneum-payload-inputs/1`): the zip's sha256 and size, every file inside the zip's folder with its sha256 and
# size, the node fork commit (40 hex) and branch the exes came from, the main repository commit, the build time.
#
#   source packaging/windows/inputs-manifest.sh
#   write_inputs_manifest <stage-dir> <zip> <node-commit-40hex> <node-branch> <repo-commit-40hex> <out.json>
#
# Sorted file names, two-space indentation, one entry per line: the bytes are what gets signed, so the writer is
# deterministic for the same inputs.

inputs_sha256() { shasum -a 256 "$1" | cut -d' ' -f1; }
inputs_size() { stat -f %z "$1" 2>/dev/null || stat -c %s "$1"; }

write_inputs_manifest() {
  local stage="$1" zip="$2" node_commit="$3" node_branch="$4" repo_commit="$5" out="$6"
  [ -d "$stage" ] || { echo "write_inputs_manifest: no stage folder $stage" >&2; return 1; }
  [ -f "$zip" ] || { echo "write_inputs_manifest: no zip $zip" >&2; return 1; }
  case "$node_commit" in [0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f]*) [ ${#node_commit} = 40 ] || { echo "write_inputs_manifest: node commit is not 40 hex" >&2; return 1; } ;; *) echo "write_inputs_manifest: node commit is not 40 hex" >&2; return 1 ;; esac
  [ ${#repo_commit} = 40 ] || { echo "write_inputs_manifest: repo commit is not 40 hex" >&2; return 1; }
  {
    echo '{'
    echo '  "format": "igneum-payload-inputs/1",'
    echo "  \"built_at\": \"$(date -u +%Y-%m-%dT%H:%M:%SZ)\","
    echo "  \"node_source_commit\": \"$node_commit\","
    echo "  \"node_source_branch\": \"$node_branch\","
    echo "  \"repo_commit\": \"$repo_commit\","
    echo "  \"zip\": { \"sha256\": \"$(inputs_sha256 "$zip")\", \"bytes\": $(inputs_size "$zip") },"
    echo '  "files": {'
    local first=1 f name
    while IFS= read -r f; do
      [ -n "$f" ] || continue
      name="$(basename "$f")"
      [ "$name" = ".DS_Store" ] && continue
      [ $first = 1 ] || echo ','
      first=0
      printf '    "%s": { "sha256": "%s", "bytes": %s }' "$name" "$(inputs_sha256 "$f")" "$(inputs_size "$f")"
    done < <(find "$stage" -maxdepth 1 -type f | LC_ALL=C sort)
    echo
    echo '  }'
    echo '}'
  } > "$out"
}
