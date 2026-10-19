#!/usr/bin/env python3
"""gen-wallet-mac.py: the product team's Mac build job for the bench queue (the mini, or --machine laptop: the Apple laptop registered under IGNEUM_MAC_LAPTOP_MACHINE), one JSON
on stdout, in the form of pow/harness/jobs/gen-ember-mac.py (the queue's job shape is the bench agent's):

  gen-wallet-mac.py build <harness_commit> [--n N] [--machine mini|laptop]
      the Igneum Wallet devnet package for macOS (arm64) built on the machine through its queue
      (wallet/tools/wallet-mac-build.sh): the engine, the signer and the CLI with the user's cargo, the page, the
      launcher; the package and its two-builder manifest into the store under product-b-wallet. No worker, no
      measurement: a short job.
  gen-wallet-mac.py app <harness_commit> [--n N] [--machine mini|laptop]
      the Igneum Wallet macOS app (the previous wallet's Swift host on the restart's engine, OLD-BASE.md steps 8
      and 10) built the same way by wallet/tools/wallet-mac-app-build.sh: the DMG and its manifest into the store.

Enqueue: `ssh <store-user>@<store-host> igneum-bench enqueue mini < job.json` (the M6 operator owns the mini's queue; this
seat enqueues only short build jobs, as the Orchard rules say).
"""
import json, subprocess, sys

MACHINE = "mini"; CONFIGURATION = "apple-m6-mini"


def crate_version(commit=None):
    """The workspace's version line in wallet/Cargo.toml at the harness commit (git show), else beside this script."""
    import os, re
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


def commit_time(commit):
    try:
        return subprocess.check_output(["git", "log", "-1", "--format=%ct", commit], stderr=subprocess.DEVNULL, text=True).strip() or "0"
    except Exception:
        return "0"


def build_job(commit, n):
    cell = "product-b-wallet-build__%s__exactness" % CONFIGURATION
    j = {"job_id": "pb-%s-wallet-build-%d" % (MACHINE, n), "machine": MACHINE, "candidate": "igneumhash", "backend": "metal",
         # the queue requires a worker already in the store by sha (the agent fetches it to {bin}); the wallet build
         # uses no worker, so the block names the Mac's stock Metal worker (the file every mini job fetches) and the
         # step never reads it
         "worker": {"name": "igneum-worker-metal", "sha256": "fe23b914cf6ff0a30b3114a398d45060c5a560f0ab2509e66915bdce9b9d9788",
                    "source": "phase-zero/workers-orchard/igneum-worker-metal-fe23b914 (the stock worker of the Ember build jobs; fetched by the queue's rule and unused by this build)"},
         # infra/release/keys: the engine's updater compiles the release key in (update.rs include_str!), so the
         # export must carry it (the first app job failed on its absence, 11 October 2026)
         "harness_commit": commit, "harness_paths": ["wallet", "site/design/icons", "infra/release/keys"], "configuration": CONFIGURATION,
         "cell": {"name": cell, "measure": "exactness", "extra_cells": []},
         "run_plan": {"kind": "screen", "heights": [1], "warmup_s": 0, "run_s": 0},
         "instrument": {"grade": "telemetry", "note": "a build job: no measurement"},
         "environment": {"IGNEUM_TIP": commit, "IGNEUM_SOURCE_DATE_EPOCH": commit_time(commit), "IGNEUM_BOX": MACHINE},
         # the package itself is stored by name (the first job stored the log and the manifest and left the tarball
         # on the mini); the name is the script's: igneum-wallet-macos-arm64-0.0.1-<sha8>.tar.gz
         "steps": [{"name": "build", "cmd": ["bash", "{work}/harness/wallet/tools/wallet-mac-build.sh", "{out}", "{work}/harness"], "timeout_s": 2400, "pid_file": "step.pid",
                    "store_as": ["build.log", "build.json", "manifest.json", "igneum-wallet-macos-arm64-%s-%s.tar.gz" % (crate_version(commit), commit[:8])]}],
         "max_total_s": 2700, "operator": "PB", "priority": "short",
         "notes": "the product team: the Igneum Wallet devnet5 package for macOS (arm64) built on this Mac from harness_commit through the queue (no build on the coordinating Mac): the engine, the signer and the CLI with the user's rustup toolchain (locked), the page and the launcher; the package igneum-wallet-macos-arm64-0.0.1-<sha8>.tar.gz and its two-builder manifest put into the store under product-b-wallet; one step, no measurement; the script is wallet/tools/wallet-mac-build.sh (the agent's harness export carries wallet and tools)"}
    return j


def app_job(commit, n):
    """The macOS app (the Swift host on the restart's engine, OLD-BASE.md steps 8 and 10): the DMG built on the Mac
    through its queue by wallet/tools/wallet-mac-app-build.sh; the DMG stored by its name."""
    cell = "product-b-wallet-app__%s__exactness" % CONFIGURATION
    dmg = "Igneum-Wallet-%s-%s.dmg" % (crate_version(commit), commit[:8])
    j = {"job_id": "pb-%s-wallet-app-%d" % (MACHINE, n), "machine": MACHINE, "candidate": "igneumhash", "backend": "metal",
         "worker": {"name": "igneum-worker-metal", "sha256": "fe23b914cf6ff0a30b3114a398d45060c5a560f0ab2509e66915bdce9b9d9788",
                    "source": "phase-zero/workers-orchard/igneum-worker-metal-fe23b914 (the queue's required worker block; unused by this build)"},
         # infra/release/keys: the engine's updater compiles the release key in (update.rs include_str!), so the
         # export must carry it (the first app job failed on its absence, 11 October 2026)
         "harness_commit": commit, "harness_paths": ["wallet", "site/design/icons", "infra/release/keys"], "configuration": CONFIGURATION,
         "cell": {"name": cell, "measure": "exactness", "extra_cells": []},
         "run_plan": {"kind": "screen", "heights": [1], "warmup_s": 0, "run_s": 0},
         "instrument": {"grade": "telemetry", "note": "a build job: no measurement"},
         "environment": {"IGNEUM_TIP": commit, "IGNEUM_SOURCE_DATE_EPOCH": commit_time(commit), "IGNEUM_BOX": MACHINE},
         "steps": [{"name": "build", "cmd": ["bash", "{work}/harness/wallet/tools/wallet-mac-app-build.sh", "{out}", "{work}/harness"], "timeout_s": 2400, "pid_file": "step.pid",
                    "store_as": ["build.log", "build.json", "manifest.json", dmg]}],
         "max_total_s": 2700, "operator": "PB", "priority": "short",
         "notes": "the product team: the Igneum Wallet macOS app (the previous wallet's Swift host on the restart's engine) built on this Mac from harness_commit through the queue: the engine, the signer and the CLI by cargo (locked), the window by swiftc with the command line tools, the bundle with the page and the icon, ad hoc signing, the DMG by hdiutil; the DMG and the manifest into the store under product-b-wallet; one step, no measurement"}
    return j


def smoke_job(commit, n, dmg, dmg_sha, dmg_key):
    """The app smoke (the project's ruling, 11 October 2026: the DMG is not published untested): the stored DMG is the job's
    fetched file; wallet/tools/wallet-mac-app-smoke.sh opens the app, waits for its window, captures the display,
    drives api/state, Lock and the quit; the capture and the lines are stored."""
    cell = "product-b-wallet-app-smoke__%s__screen" % CONFIGURATION
    j = {"job_id": "pb-%s-wallet-app-smoke-%d" % (MACHINE, n), "machine": MACHINE, "candidate": "igneumhash", "backend": "metal",
         "worker": {"name": dmg, "sha256": dmg_sha, "source": "%s (the stored macOS app; the smoke opens it)" % dmg_key},
         "harness_commit": commit, "harness_paths": ["wallet/tools"], "configuration": CONFIGURATION,
         "cell": {"name": cell, "measure": "screen", "extra_cells": []},
         "run_plan": {"kind": "screen", "heights": [1], "warmup_s": 0, "run_s": 0},
         "instrument": {"grade": "telemetry", "note": "an app smoke: the window on screen, the API answering, the quit clean; no measurement"},
         "environment": {"IGNEUM_TIP": commit, "IGNEUM_BOX": MACHINE},
         "split": False,
         "steps": [{"name": "smoke", "cmd": ["bash", "{work}/harness/wallet/tools/wallet-mac-app-smoke.sh", "{out}", "{bin}/" + dmg], "timeout_s": 300, "pid_file": "step.pid",
                    "store_as": ["smoke.log", "smoke.json", "window.txt", "display.png", "display-locked.png", "host.err"]}],
         "max_total_s": 600, "operator": "PB", "priority": "short",
         "notes": "the product team: the Igneum Wallet macOS app opened and driven on this Mac through the queue, no hand (the project's ruling): the DMG attached and the app copied out, the host started with a test data dir, its window on the window server's list and the display captured, api/state polled as the menu bar does, a wallet created, Lock through the API, the app told to quit and the host and engine gone after; nothing killed by the script"}
    return j


def main(argv):
    global MACHINE, CONFIGURATION
    if "--machine" in argv:
        i = argv.index("--machine"); MACHINE = argv[i + 1]; del argv[i:i + 2]
        CONFIGURATION = {"mini": "apple-m6-mini", "laptop": "apple-m5max"}.get(MACHINE, MACHINE)
    n = 1
    if "--n" in argv:
        i = argv.index("--n"); n = int(argv[i + 1]); del argv[i:i + 2]
    if len(argv) < 3 or argv[1] not in ("build", "app", "smoke"):
        print(__doc__); return 2
    if argv[1] == "smoke":
        # gen-wallet-mac.py smoke <commit> <dmg name> <dmg sha256> <store key> [--n N]
        if len(argv) < 6:
            print(__doc__); return 2
        print(json.dumps(smoke_job(argv[2], n, argv[3], argv[4], argv[5]), indent=1))
        return 0
    print(json.dumps(build_job(argv[2], n) if argv[1] == "build" else app_job(argv[2], n), indent=1))
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
