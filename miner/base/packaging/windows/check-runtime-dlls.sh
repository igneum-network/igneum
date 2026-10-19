#!/usr/bin/env bash
# Refuses a Windows payload whose exes import a symbol the shipped runtime DLL does not export.
#
#   packaging/windows/check-runtime-dlls.sh <folder with igneumd.exe, igneum-miner.exe and lib*.dll>
#
# 5 October 2026, Ember an earlier release: the PC-built igneumd.exe (GCC 13's mingw) was shipped with libstdc++-6.dll from
# the Mac's toolchain (GCC 16), which no longer exports five std::codecvt symbols the exe imports
# (_ZNKSt25__codecvt_utf8_utf16_baseIwE10do_unshiftERiPcS2_RS2_ and four siblings); Windows refused to start the node
# ("Entry Point Not Found") on both PCs. `-C link-arg=-static` had not removed the import. Rule from here: the DLLs
# in a payload come from the toolchain that linked the exes, and this check runs before anything is signed
# (push-inputs.sh) and before the installer is packed (make-payload.sh). Needs x86_64-w64-mingw32-objdump (Homebrew
# mingw-w64 on the Mac); prints a note and exits 0 where it is missing (the runner), exits 1 on a missing symbol.
set -euo pipefail
DIR="${1:?folder with the exes and the DLLs}"
OBJDUMP="${OBJDUMP:-x86_64-w64-mingw32-objdump}"
if ! command -v "$OBJDUMP" >/dev/null 2>&1; then echo "runtime DLL check: no $OBJDUMP here, skipped (the Mac's push-inputs.sh ran it before signing)"; exit 0; fi
# objdump -p prints, under "DLL Name: x.dll", one import per line: "<vma>  <ordinal or <none>>  <hint>  <name>"; and under
# "[Ordinal/Name Pointer] Table" one export per line: "[ n] +base[ m]  <hint> <name>"
imports_from() { "$OBJDUMP" -p "$1" | awk -v dll="$2" 'index(tolower($0), "dll name: " tolower(dll)) > 0 {f=1; next} /DLL Name:/ {f=0} f && /^\t[0-9a-f]+[ \t]+[^ \t]+[ \t]+[0-9a-f]+[ \t]+[^ \t]+[ \t]*$/ {print $NF}' | sort -u; }
exports_of() { "$OBJDUMP" -p "$1" | awk '/^\t\[ *[0-9]+\] \+base\[ *[0-9]+\][ \t]+[0-9a-f]+[ \t]+[^ \t]+[ \t]*$/ {print $NF}' | sort -u; }
bad=0; checked=0
for exe in "$DIR"/igneumd.exe "$DIR"/igneum-miner.exe; do
  [ -f "$exe" ] || continue
  for dll in $("$OBJDUMP" -p "$exe" | awk '/DLL Name:/ {print $3}' | sort -u); do
    case "$dll" in lib*.dll) ;; *) continue ;; esac
    if [ ! -f "$DIR/$dll" ]; then echo "runtime DLL check: $(basename "$exe") imports $dll and the payload has no $dll"; bad=1; continue; fi
    missing="$(comm -23 <(imports_from "$exe" "$dll") <(exports_of "$DIR/$dll") || true)"
    n_imp="$(imports_from "$exe" "$dll" | wc -l | tr -d ' ')"; n_exp="$(exports_of "$DIR/$dll" | wc -l | tr -d ' ')"
    checked=$((checked+1))
    if [ -n "$missing" ]; then
      echo "runtime DLL check: $(basename "$exe") imports $(echo "$missing" | wc -l | tr -d ' ') symbol(s) from $dll that this $dll does not export ($n_imp imported, $n_exp exported):"
      echo "$missing" | head -5 | sed 's/^/  /'; bad=1
    else
      echo "runtime DLL check: $(basename "$exe") <- $dll: all $n_imp imported symbols exported ($n_exp exports)"
    fi
  done
done
[ "$checked" -gt 0 ] || echo "runtime DLL check: no lib*.dll import in the exes (statically linked)"
[ "$bad" = 0 ] || { echo "runtime DLL check FAILED: ship the DLLs of the toolchain that linked the exes" >&2; exit 1; }
