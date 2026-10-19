#!/usr/bin/env python3
"""The window host gate (0.1.22; the second Windows machine, 7 October 2026: a 0.1.21 installer carried a mingw-built "Ember.exe" whose
version resource read 0.1.22.0 and which crashed in ntdll seconds after starting its engine). Before a host enters a
payload, three facts must hold or the payload is refused:
  1. the exe's version resource (FileVersion and ProductVersion) equals the installer's version (as "a.b.c" or "a.b.c.0");
  2. the exe carries no mingw-w64 signature (the GNU runtime strings "Mingw-w64", "libgcc", "GCC: (GNU", "libstdc++-6.dll",
     "libwinpthread-1.dll"): the host is the MSVC build (app\\windows\\BUILD-APP.bat) or nothing;
  3. when a pin file is given (packaging/windows/host.sha256: one sha256 per line, the cut's MSVC host), the exe's sha256
     is in it.
    host-gate.py <Ember.exe> <version> [<pin file>]      exit 0 = pass, 1 = refused (every reason printed)
    host-gate.py --self-test                                    known-bad and known-good blobs
No dependency; reads the version strings straight from the resource's UTF-16LE text, which is how every PE carries them."""
import hashlib
import re
import sys

MINGW_MARKS = [b"Mingw-w64", b"mingw-w64", b"libgcc", b"GCC: (GNU", b"libstdc++-6.dll", b"libwinpthread-1.dll"]


def utf16(s):
    return s.encode("utf-16-le")


def version_strings(blob):
    """FileVersion and ProductVersion from the VS_VERSIONINFO StringFileInfo block: the UTF-16 key, 0 to 3 NUL words of
    padding, then the UTF-16 value up to its NUL."""
    out = {}
    for key in ("FileVersion", "ProductVersion"):
        m = re.search(re.escape(utf16(key)) + rb"\x00\x00(?:\x00\x00){0,3}((?:[^\x00]\x00|\x00[^\x00]){1,40}?)\x00\x00", blob)
        if m:
            try:
                out[key] = m.group(1).decode("utf-16-le").strip()
            except UnicodeDecodeError:
                out[key] = ""
    return out


def norm(v):
    parts = [p for p in re.split(r"[.,\s]+", v.strip()) if p != ""]
    while len(parts) < 4:
        parts.append("0")
    return ".".join(parts[:4])


def check(blob, version, pins=None):
    reasons = []
    vs = version_strings(blob)
    want = norm(version)
    for key in ("FileVersion", "ProductVersion"):
        have = vs.get(key)
        if have is None:
            reasons.append(f"no {key} in the version resource")
        elif norm(have) != want:
            reasons.append(f"{key} reads {have}, the installer is {version}")
    marks = [m.decode() for m in MINGW_MARKS if m in blob]
    if marks:
        reasons.append("a mingw-w64 build (" + ", ".join(marks) + "): the host must be the MSVC build")
    if pins is not None:
        sha = hashlib.sha256(blob).hexdigest()
        if sha not in pins:
            reasons.append(f"sha256 {sha[:12]} is not the cut's pinned MSVC host ({', '.join(p[:12] for p in pins) or 'no pin'})")
    return reasons


def read_pins(path):
    pins = []
    with open(path, encoding="utf-8") as f:
        for line in f:
            line = line.split("#", 1)[0].strip().lower()
            if re.fullmatch(r"[0-9a-f]{64}", line):
                pins.append(line)
    return pins


def fake_pe(file_version, product_version, extra=b""):
    """A blob with a version resource shaped like a real one (the strings, the padding, the NULs)."""
    return (b"MZ" + b"\x00" * 64 + utf16("FileVersion") + b"\x00\x00\x00\x00" + utf16(file_version) + b"\x00\x00"
            + utf16("ProductVersion") + b"\x00\x00\x00\x00" + utf16(product_version) + b"\x00\x00" + extra)


def self_test():
    # known-bad first: the take-4 host's shape, an earlier release.0 inside a an earlier release installer, mingw-built
    bad = fake_pe("0.1.22.0", "0.1.22.0", b"...Mingw-w64 runtime failure:...libgcc_s_seh-1.dll...")
    r = check(bad, "0.1.21", pins=[])
    assert any("FileVersion reads 0.1.22.0" in x for x in r), r
    assert any("mingw-w64 build" in x for x in r), r
    assert any("not the cut's pinned" in x for x in r), r
    # a right version but a mingw build is still refused
    r = check(fake_pe("0.1.21.0", "0.1.21.0", b"GCC: (GNU) 13-posix"), "0.1.21")
    assert r == ["a mingw-w64 build (GCC: (GNU): the host must be the MSVC build"], r
    # known-good: the version equal in both forms, no GNU strings, the sha pinned
    good = fake_pe("0.1.21.0", "0.1.21", b"Microsoft (R) C/C++ Optimizing Compiler")
    assert check(good, "0.1.21") == [], check(good, "0.1.21")
    assert check(good, "0.1.21", pins=[hashlib.sha256(good).hexdigest()]) == []
    assert check(good, "0.1.21", pins=["0" * 64]) != [], "a pin that is not this exe refuses"
    # no resource at all is refused (nothing to compare)
    r = check(b"MZ" + b"\x00" * 200, "0.1.21")
    assert r == ["no FileVersion in the version resource", "no ProductVersion in the version resource"], r
    print("self-test passed: a 0.1.22.0 mingw host fails a 0.1.21 installer on all three counts, a right-version mingw host fails, a pinned MSVC host passes, a resource-less blob fails")


def main(argv):
    if len(argv) >= 2 and argv[1] == "--self-test":
        self_test()
        return 0
    if len(argv) < 3:
        print(__doc__)
        return 2
    exe, version = argv[1], argv[2]
    pins = read_pins(argv[3]) if len(argv) > 3 else None
    with open(exe, "rb") as f:
        blob = f.read()
    reasons = check(blob, version, pins)
    vs = version_strings(blob)
    sha = hashlib.sha256(blob).hexdigest()
    if reasons:
        print(f"host-gate: REFUSED {exe} for {version} (FileVersion {vs.get('FileVersion')}, ProductVersion {vs.get('ProductVersion')}, sha256 {sha[:12]}):")
        for r in reasons:
            print(f"  - {r}")
        return 1
    print(f"host-gate: ok {exe} is {version} (FileVersion {vs.get('FileVersion')}, ProductVersion {vs.get('ProductVersion')}), no mingw signature, sha256 {sha[:12]}" + (" pinned" if pins is not None else ""))
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
