#!/usr/bin/env python3
"""Checks that a Windows exe carries the Igneum resources: a .rsrc section with an icon group and a version block whose
CompanyName is Igneum and ProductName is Ember. Prints what it finds. Small PE parser, no dependencies, runs on
the Mac where rcedit and Explorer are not available.
    python3 verify-exe.py [--version 0.1.0] exe [exe ...]      exit 1 if any exe fails"""
import struct, sys

RT_ICON, RT_GROUP_ICON, RT_VERSION = 3, 14, 16
REQUIRED = ("CompanyName", "ProductName", "FileDescription", "FileVersion", "ProductVersion", "LegalCopyright")


def sections_of(data):
    pe = struct.unpack_from("<I", data, 0x3C)[0]
    if data[pe:pe + 4] != b"PE\0\0":
        raise ValueError("not a PE file")
    nsec, opt_size = struct.unpack_from("<H", data, pe + 6)[0], struct.unpack_from("<H", data, pe + 20)[0]
    opt = pe + 24
    magic = struct.unpack_from("<H", data, opt)[0]
    dd = opt + (112 if magic == 0x20B else 96)
    res_rva, res_size = struct.unpack_from("<II", data, dd + 2 * 8)
    secs = []
    for i in range(nsec):
        s = opt + opt_size + 40 * i
        name = data[s:s + 8].rstrip(b"\0").decode("latin1")
        vsize, vaddr, rsize, raw = struct.unpack_from("<IIII", data, s + 8)
        secs.append((name, vaddr, max(vsize, rsize), raw))
    return secs, res_rva, res_size


def rva_to_off(secs, rva):
    for _, vaddr, size, raw in secs:
        if vaddr <= rva < vaddr + size:
            return raw + (rva - vaddr)
    raise ValueError("rva 0x%x outside every section" % rva)


def walk(data, base, off, depth, found, path=()):
    named, ids = struct.unpack_from("<HH", data, base + off + 12)
    for i in range(named + ids):
        e = base + off + 16 + 8 * i
        ident, child = struct.unpack_from("<II", data, e)
        if child & 0x80000000:
            walk(data, base, child & 0x7FFFFFFF, depth + 1, found, path + (ident,))
        else:
            rva, size = struct.unpack_from("<II", data, base + child)
            found.append((path + (ident,), rva, size))


def version_strings(data, off, size):
    """Parses VS_VERSIONINFO into (fixed FileVersion tuple, {key: value})."""
    end = off + size

    def block(pos):
        length, vlen, vtype = struct.unpack_from("<HHH", data, pos)
        key_end = pos + 6
        while data[key_end:key_end + 2] != b"\0\0":
            key_end += 2
        key = data[pos + 6:key_end].decode("utf-16-le")
        val = (key_end + 2 + 3) & ~3
        return length, vlen, vtype, key, val

    strings, fixed = {}, None
    length, vlen, _, key, val = block(off)
    if vlen >= 52 and struct.unpack_from("<I", data, val)[0] == 0xFEEF04BD:
        ms, ls, pms, pls = struct.unpack_from("<IIII", data, val + 8)
        fixed = (ms >> 16, ms & 0xFFFF, ls >> 16, ls & 0xFFFF, pms >> 16, pms & 0xFFFF, pls >> 16, pls & 0xFFFF)
    pos = (val + vlen + 3) & ~3
    while pos < off + length:
        clen, _, _, ckey, cval = block(pos)
        if ckey == "StringFileInfo":
            tpos = cval
            while tpos < pos + clen:
                tlen, _, _, _, tval = block(tpos)
                spos = tval
                while spos < tpos + tlen:
                    slen, svlen, _, skey, sval = block(spos)
                    if slen == 0:
                        break
                    raw = data[sval:sval + 2 * svlen]
                    strings[skey] = raw.decode("utf-16-le").rstrip("\0")
                    spos = (spos + slen + 3) & ~3
                tpos = (tpos + tlen + 3) & ~3
        if clen == 0:
            break
        pos = (pos + clen + 3) & ~3
    return fixed, strings


def check(path, want_version):
    data = open(path, "rb").read()
    secs, res_rva, res_size = sections_of(data)
    names = [s[0] for s in secs]
    problems = []
    if ".rsrc" not in names:
        problems.append("no .rsrc section")
    if not res_rva:
        problems.append("resource table empty in the PE data directory")
    icons = groups = 0
    fixed, strings = None, {}
    if res_rva:
        base = rva_to_off(secs, res_rva)
        found = []
        walk(data, base, 0, 0, found)
        for p, rva, size in found:
            if p[0] == RT_ICON:
                icons += 1
            elif p[0] == RT_GROUP_ICON:
                groups += 1
            elif p[0] == RT_VERSION and fixed is None:
                fixed, strings = version_strings(data, rva_to_off(secs, rva), size)
    if not groups:
        problems.append("no icon group (RT_GROUP_ICON)")
    if fixed is None:
        problems.append("no version block (RT_VERSION)")
    for k in REQUIRED:
        if not strings.get(k):
            problems.append("missing " + k)
    if strings.get("CompanyName") not in (None, "Igneum"):
        problems.append("CompanyName is %r, not Igneum" % strings["CompanyName"])
    if strings.get("ProductName") not in (None, "Ember"):
        problems.append("ProductName is %r, not Ember" % strings["ProductName"])
    if want_version:
        for k in ("FileVersion", "ProductVersion"):
            if strings.get(k) and strings[k] != want_version:
                problems.append("%s is %s, not %s" % (k, strings[k], want_version))
        if fixed and ".".join(map(str, fixed[:3])) != want_version:
            problems.append("FILEVERSION is %s, not %s" % (".".join(map(str, fixed[:4])), want_version))
    print("%s: %d bytes, sections %s" % (path, len(data), " ".join(names)))
    print("  icon images %d, icon groups %d, FILEVERSION %s" % (icons, groups, ".".join(map(str, fixed[:4])) if fixed else "none"))
    for k in REQUIRED + ("InternalName", "OriginalFilename"):
        if k in strings:
            print("  %-17s %s" % (k, strings[k]))
    for p in problems:
        print("  FAIL: " + p)
    return not problems


if __name__ == "__main__":
    args = sys.argv[1:]
    want = None
    if args[:1] == ["--version"]:
        want, args = args[1], args[2:]
    if not args:
        sys.exit(__doc__)
    ok = all([check(p, want) for p in args])
    print("all %d exe(s) carry the Igneum resources" % len(args) if ok else "FAILED")
    sys.exit(0 if ok else 1)
