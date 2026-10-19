#!/usr/bin/env python3
# The public aliases: what the two manifests and the newest HiveOS package in dl/public/ name (publish-public.sh calls
# this with <pub dir> <dry 0|1>; a separate file because bash 3.2 mis-parses a heredoc with parentheses inside $( )).
import json, os, re, sys
pub, dry = sys.argv[1], sys.argv[2] == "1"
# a platform the current manifest lacks keeps the newest versioned file of that platform already in dl/public/ (6 October
# 2026, an earlier release: the Mac row moved first while the Windows installer waited on the GitHub billing block; the Windows link of the site
# must stay on the version that exists, never go dark, and never name a version that is not there). The index below
# reads such a file"s version from its name, so the site stamps the carried version honestly.
CARRY = {"windows": r"Ember-Setup-(\d+\.\d+\.\d+)\.exe", "mac": r"Ember-(\d+\.\d+\.\d+)\.dmg"}
def newest(pattern):
    fs = [f for f in os.listdir(pub) if re.fullmatch(pattern, f)] if os.path.isdir(pub) else []
    fs.sort(key=lambda f: tuple(int(x) for x in re.findall(r"\d+", f)[:3]))
    return fs[-1] if fs else None
def entry(name, plat):
    p = os.path.join(pub, name)
    if not os.path.exists(p): return None
    e = json.load(open(p)).get("platforms", {}).get(plat)
    if not e:
        return newest(CARRY[plat]) if name == "igneum-app-latest.json" and plat in CARRY else None
    f = e["url"].rsplit("/", 1)[1]
    return f if os.path.exists(os.path.join(pub, f)) or dry else None
hive = sorted([f for f in os.listdir(pub) if re.fullmatch(r"igneum-hive-\d+\.\d+\.\d+\.tar\.gz", f)],
              key=lambda f: tuple(int(x) for x in re.findall(r"\d+", f)[:3])) if os.path.isdir(pub) else []
aliases = {
    "igneum-miner-windows.exe": entry("igneum-app-latest.json", "windows"),
    "igneum-miner-mac.dmg": entry("igneum-app-latest.json", "mac"),
    "igneum-miner-hive.tar.gz": hive[-1] if hive else None,
    "igneum-wallet-mac.dmg": entry("igneum-wallet-latest.json", "mac"),
}
print(json.dumps(aliases))
