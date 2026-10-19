#!/usr/bin/env python3
"""gen-pc2-wallet-installer.py <harness_commit> <payload zip name> <payload sha256> <store key> [--n N] [--machine M]

The wallet's Windows installer job for the second Windows machine's queue (OLD-BASE.md step 10; the project's ruling of
11 October 2026: that machine, where the product team's installer job carries Inno Setup and MSVC; the form is the product team's,
sent direct): a card-less job (its default lane); the queue's required worker block is the payload zip in the store (the three .exe from the
Linux cross-build, the page, the icon, the launcher, the stop script; wallet/tools/windows-payload.sh); the steps:
  1. unzip      Expand-Archive of the payload into {out}
  2. window-host  BUILD-WALLET-APP.bat in the checkout's wallet\\app\\windows with the payload copied beside it (it finds
                 MSVC's vcvarsall itself and fetches the WebView2 SDK from NuGet on first run), the host copied into
                 the payload
  3. installer  build-wallet-installer.ps1 -Payload {out}\\igneum-windows-wallet -Version <v> (ISCC on Igneum-Wallet.iss)
  4. collect    dist\\* into {out}; the Setup exe stored by its name
Enqueue on builder one: igneum-bench enqueue pc2 < job.json. One JSON on stdout."""
import json, os, re, subprocess, sys


def crate_version(commit=None):
    """The workspace's version at the harness commit (git show), else beside this script (the worktree moved on to
    0.0.6 while 0.0.5's installer job was made, and the job named the wrong Setup, 12 October 2026)."""
    if commit:
        import subprocess
        try:
            text = subprocess.run(["git", "show", "%s:wallet/Cargo.toml" % commit], capture_output=True, text=True, check=True, cwd=os.path.dirname(os.path.abspath(__file__))).stdout
            for line in text.splitlines():
                m = re.match(r'^version\s*=\s*"([^"]+)"', line)
                if m:
                    return m.group(1)
        except Exception:
            pass
    p = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "Cargo.toml")
    for line in open(p):
        m = re.match(r'^version\s*=\s*"([^"]+)"', line)
        if m:
            return m.group(1)
    return "0.0.0"


MACHINE = "pc2"

def smoke_job(commit, n, setup, setup_sha, key):
    """The Setup exe's own smoke on the second Windows machine through its queue, no hand (the project's word, 11 October 2026): the stored installer
    is the job's fetched file; wallet/packaging/windows/installer-smoke.ps1 installs it silently into a prefix under
    {out}, starts the host, drives api/state, creates and locks a wallet, closes the host, uninstalls; the rows stored."""
    cell = "product-b-wallet-installer-smoke__pc2__exactness"
    w = "{work}\\harness\\wallet"
    return {"job_id": "pb-pc2-wallet-installer-smoke-%s-%02d" % (commit[:8], n), "machine": MACHINE, "candidate": "igneumhash", "backend": "cuda",
            "configuration": MACHINE,
            "worker": {"name": setup, "sha256": setup_sha, "source": "%s (the stored Windows installer; the smoke installs it into a prefix under the job's output)" % key},
            "harness_commit": commit, "harness_paths": ["wallet/packaging/windows"],
            "cell": {"name": cell, "measure": "exactness", "extra_cells": []},
            "instrument": {"grade": "telemetry", "note": "an installer smoke: the silent install, the host up and its API answering, the uninstall; no measurement"},
            "environment": {"IGNEUM_DEV_UNCHECKED_WORKERS": "1"},
            "run_plan": {"kind": "screen", "heights": [1], "warmup_s": 0, "run_s": 0, "note": "the Setup exe run silently into a temporary prefix, the host started with a test data dir, api/state, a wallet created and locked, the host closed, the uninstaller run; nothing mined"},
            "operator": "PB", "priority": "short", "split": False, "max_total_s": 900,
            "label": "wallet installer smoke",
            "notes": "the product team: the Igneum Wallet Windows installer's own smoke on the second Windows machine through its queue (the project's word): install silently to a prefix under the job's output, the host started and its engine answering api/state, a wallet created and locked through the API, the host closed by its own path, the uninstaller run silently, the prefix's files gone; the rows in smoke.json",
            "steps": [
                {"name": "smoke", "cmd": ["powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", "%s\\packaging\\windows\\installer-smoke.ps1" % w, "-Setup", "{bin}\\" + setup, "-Out", "{out}"], "timeout_s": 600, "pid_file": "step.pid",
                 "store_as": ["%s:smoke.log" % cell, "%s:smoke.json" % cell, "%s:install.log" % cell, "%s:close-notice.png" % cell]},
            ]}


def main(argv):
    n = 1
    if "--n" in argv:
        i = argv.index("--n"); n = int(argv[i + 1]); del argv[i:i + 2]
    # the Windows machine the job runs on: the second Windows machine by default; another (the Windows canary VM,
    # the project's allowance of 12 October 2026 while the PCs waited for the founder's hand) by --machine, recorded in the job
    global MACHINE
    if "--machine" in argv:
        i = argv.index("--machine"); MACHINE = argv[i + 1]; del argv[i:i + 2]
    if len(argv) >= 2 and argv[1] == "over-open":
        # the gate's update-over-open row (0.0.21): the previous release's wallet open, the Setup under test applied over it,
        # then a held file ending the install with words; inside the pause form on the first Windows machine
        if len(argv) < 6:
            print("usage: gen-pc2-wallet-installer.py over-open <commit> <setup exe> <sha256> <store key> [--n N] [--machine M]", file=sys.stderr); sys.exit(2)
        j = smoke_job(argv[2], n, argv[3], argv[4], argv[5])
        j["job_id"] = "pb-pc2-wallet-update-over-open-%s-%02d" % (argv[2][:8], n)
        j["label"] = "wallet update over an open wallet"
        j["notes"] = "the product team: the landing check's rule of 16 October 2026 (the 0.0.21 fix): an update applies while a previous wallet is open, and a held file ends the install with words, never a bare code; the pause form stops the installed wallet first and relaunches it after."
        st = j["steps"][0]; st["name"] = "over-open"
        st["cmd"] = [c.replace("installer-smoke.ps1", "installed-pause-smoke.ps1") for c in st["cmd"]] + ["-Script", "update-over-open.ps1"]
        st["timeout_s"] = 900
        cell = st["store_as"][0].split(":")[0]
        st["store_as"] = [cell + ":over-open.log", cell + ":over-open.json", cell + ":update.log", cell + ":update-locked.log", cell + ":pause.log"]
        print(json.dumps(j, indent=1)); return
    if len(argv) >= 2 and argv[1] == "smoke":
        # gen-pc2-wallet-installer.py smoke <commit> <setup exe name> <sha256> <store key> [--n N]
        if len(argv) < 6:
            print(__doc__); return 2
        print(json.dumps(smoke_job(argv[2], n, argv[3], argv[4], argv[5]), indent=1))
        return 0
    if len(argv) < 5:
        print(__doc__); return 2
    commit, zipname, zipsha, key = argv[1], argv[2], argv[3], argv[4]
    ver = crate_version(commit)
    setup = "Igneum-Wallet-Setup-%s.exe" % ver
    cell = "product-b-wallet-installer__pc2__exactness"
    w = "{work}\\harness\\wallet"
    j = {"job_id": "pb-pc2-wallet-installer-%s-%02d" % (commit[:8], n), "machine": MACHINE, "candidate": "igneumhash", "backend": "cuda",
         "configuration": "pc2",
         "worker": {"name": zipname, "sha256": zipsha, "source": "%s (the wallet's Windows payload: the three .exe from the Linux cross-build, the page, the icon, the launcher, the stop script)" % key},
         "harness_commit": commit, "harness_paths": ["wallet", "site/design/icons", "infra/release/keys"],
         "cell": {"name": cell, "measure": "exactness", "extra_cells": []},
         "instrument": {"grade": "telemetry", "note": "a build job: no measurement"},
         "environment": {"IGNEUM_DEV_UNCHECKED_WORKERS": "1", "PRODUCT": "public"},
         "run_plan": {"kind": "screen", "heights": [1], "warmup_s": 0, "run_s": 0, "note": "a build job: the window host compiled on the PC, the installer by Inno Setup; nothing mined"},
         "operator": "PB", "priority": "short", "split": False, "max_total_s": 2400,
         "label": "wallet installer %s" % ver,
         "notes": "the product team: the Igneum Wallet Windows installer (the previous wallet's WebView2 host on the restart's engine) built on the second Windows machine through its queue in the product team's form: the payload unzipped, the host compiled by BUILD-WALLET-APP.bat (MSVC found by vcvarsall; the WebView2 SDK from NuGet on first run), the installer by build-wallet-installer.ps1 (Inno Setup 6, found by its uninstall keys; installs nothing on the PC), the Setup exe stored by its name",
         "steps": [
             {"name": "unzip", "cmd": ["powershell", "-Command", "Expand-Archive -LiteralPath '{bin}\\%s' -DestinationPath '{out}' -Force" % zipname], "timeout_s": 300, "pid_file": "step.pid", "store_as": []},
             # the build script copies the host into the payload folder beside it (igneum-windows-wallet under
             # app\windows), so the installer step takes that folder; a trailing copy of my own with quoted paths
             # was eaten by cmd on the third run (11 October 2026) and is gone
             {"name": "window-host", "cmd": ["cmd.exe", "/c", "cd /d %s\\app\\windows && xcopy /e /i /y {out}\\igneum-windows-wallet igneum-windows-wallet >nul && BUILD-WALLET-APP.bat < nul" % w], "timeout_s": 1500, "pid_file": "step.pid", "store_as": []},
             {"name": "installer", "cmd": ["powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", "%s\\packaging\\windows\\build-wallet-installer.ps1" % w, "-Payload", "%s\\app\\windows\\igneum-windows-wallet" % w, "-Version", ver], "timeout_s": 600, "pid_file": "step.pid", "store_as": []},
             {"name": "collect", "cmd": ["powershell", "-Command", "Copy-Item '%s\\packaging\\windows\\dist\\*' '{out}' -Force; Get-ChildItem '{out}\\*.exe','{out}\\SHA256SUMS' | ForEach-Object { $h = (Get-FileHash -Algorithm SHA256 $_.FullName).Hash.ToLower(); Write-Output ($h + '  ' + $_.Name + '  ' + $_.Length) }" % w], "timeout_s": 120, "pid_file": "step.pid", "store_as": ["%s:%s" % (cell, setup), "%s:SHA256SUMS" % cell, "%s:signing.json" % cell]},
         ]}
    print(json.dumps(j, indent=1))
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
