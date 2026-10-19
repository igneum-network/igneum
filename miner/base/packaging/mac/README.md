# Ember for Mac (packaging/mac)

A DMG a miner opens on an Apple silicon Mac: drag the app to Applications, open it, answer two questions, mine.
Version an earlier release (4 October 2026) is the app: a branded window (no Terminal), the engine that runs the node and the miner,
a menu-bar item, settings, an update check. 0.1.0 and 0.2.0 (3 and 4 October 2026) were the Terminal launchers; the
0.2.0 run script's command lines live on inside the engine (`app/igneum-app/src/engine.rs`).

## Build

    packaging/mac/build-dmg.sh

Output: `packaging/mac/dist/Ember-an earlier release.dmg` (about 20 MB, lzfse). The script compiles the engine
(`app/igneum-app`, `cargo build --release`, nice 19 and 4 jobs because the devnet mines on this Mac), the window
(`app/mac/Ember.swift`, `swiftc -target arm64-apple-macos11`) and the Metal worker (`proto-metal/main.swift`,
same target), takes `igneumd` and `igneum-miner` from `vendor/igneum-node/target-integration/release` (the devnet-v4
integration build; `NODE=`, `MINER=`, `WORKER=`, `ENGINE=` override), strips and ad-hoc signs the copies, checks each
one runs, writes `igneum-app.json` (below) and lays the image out with `dmgbuild` (`pip3 install dmgbuild`; without it a
plain hdiutil image). The app icon is `brand/icons/igneum.icns` (the master mark, a plain black square), the volume icon `brand/icons/igneum-volume.icns` (its rounded variant), the DMG art `brand/icons/dmg-background.tiff`
(`python3 brand/icons/make-icons.py`). `build/` and `dist/` are ignored by git.

## The bundle

    Ember.app/Contents/MacOS/Ember        the window: WKWebView + NSStatusItem (app/mac/Ember.swift)
    Ember.app/Contents/MacOS/igneum-app          the engine (app/igneum-app)
    Ember.app/Contents/Resources/bin/igneumd, igneum-miner, igneum-bench
    Ember.app/Contents/Resources/igneum-app.json  packaged configuration (packaged-config.sh writes it)
    Ember.app/Contents/Resources/igneum.icns
    Ember.app/Contents/Info.plist                 app/Info.plist (CFBundleVersion = build stamp, LSMinimumSystemVersion 11.0)

The window starts `igneum-app --wrapper`, reads `URL ...` and `STATE {...}` lines from its stdout, shows the URL in the
web view and keeps the menu-bar title (hash rate, paused, syncing). Quit writes `quit` to the engine's stdin and waits
for `EXIT` (up to 45 s): the miners stop first (8 s grace), then the node (30 s), then the last log upload. Closing
the window hides it; the app lives on in the menu bar and the Dock. The engine serves the dashboard on
`127.0.0.1:<random port>/t/<32-hex token>/`; nothing without the token answers. Links off 127.0.0.1 open in the
default browser.

`igneum-app.json` (never in the UI source): `update_manifest` = `https://dl.igneum.network/ember/latest/igneum-app-latest.json`
with the token read from `$IGNEUM_SECRETS_DIR/dl-token` at build time (missing file = update check off), the log intake
URL and key (the key only authorises uploads, as the 0.2.0 launchers shipped it), the live page.

Over-the-air updates (4 October 2026, `packaging/ota/README.md`): the engine checks the signed manifest on start and
hourly, downloads the newer DMG into `~/Library/Application Support/Igneum/app/updates`, verifies size, sha256 and the
Ed25519 signature, stages the new bundle next to the running one, and at a safe moment (node synced, no hourly
boundary within 3 minutes, no worker starting) stops the miners and the node, swaps the bundle (the old one stays as
`Ember.app.previous`) and opens the new one. Publish with `packaging/ota/publish-release-manifest.sh --version <v>
--mac dist/Ember-<v>.dmg --notes "..."`. The 0.1.0 and 0.2.0 Terminal launchers are not auto-updated.

Devnet vote keys are test keys derived from the miner's label (`<platform>-<machine id8>-<n>`); mainnet vote keys will be
random and stored like the wallet (R4.3.12, devnet-only by design).

Gatekeeper: the app is unsigned (ad hoc). Right-click > Open the first time. The engine strips
`com.apple.quarantine` from the bundle's Contents on start, so the binaries inside are not refused one by one; that
needs a writable volume, hence "drag to Applications first".

## What the engine does (app/igneum-app)

Order: binaries next to the app > `igneum-app.json` > settings (`~/Library/Application Support/Igneum/app/settings.json`)
> GPU detection (the Metal worker's `ready` line gives the device name; system_profiler the cores) > first run: cards
(switch, identities) and the rewards address (a secp256k1 key made here, `app/wallet.json` mode 0600, shown once; or a
pasted 0x address) > `igneumd --devnet --appdir ~/Library/Application Support/Igneum/devnet-v4 --rpclisten 127.0.0.1:26610
--listen 0.0.0.0:26611 --addpeer 188.245.5.161:26611 --nodnsseed --disable-upnp --nologfiles
--yes` (a node already answering on 26610 is used as is and never stopped) > `igneum-miner watch 1000000000` as one
long-lived reader (a line every 10 s: blocks, headers, daa, peers, difficulty, blue, synced) > synced (the node's own
flag with a peer, else the 0.2.0 rule) > per enabled card `igneum-miner mine grpc://127.0.0.1:26610 1 100000000
mac-<host> --worker igneum-bench --status-secs 30 --exit-on-seed-change --evm-address <addr> [--identities N]
[--no-vote] --payout-label mac-<host>` > the loop: STATUS lines into hash rate (the v4 `now=` interval rate) and
counts, ACCEPTED into blocks and the strip, `worker: prepared` into "next program compiled and ready", LOCK/VOTE into
finality, a status line every 30 s, uploads every 60 s, restarts (exit 42 at once; crashes 5 to 60 s; the node 5 to
60 s with the miners held until it is synced again). Pause stops the miners and keeps the node. Settings restart only
the affected worker.

Tests: `IGNEUM_APP_NETWORK=devnet IGNEUM_APP_DEVNET_SUFFIX=7 IGNEUM_APP_RPC_PORT=29400 IGNEUM_APP_P2P_PORT=29401
IGNEUM_APP_PEERS= IGNEUM_APP_UNSYNCED=1 IGNEUM_APP_DATA=<dir> IGNEUM_APP_LOGS=<dir> igneum-app --print-url --no-open`
runs a private devnet (its own genesis, no peers) with real mining; the window binary takes the same environment.
Screenshots: `"Ember" --snapshot out.png --url <dashboard url>?screen=welcome|cards|address|key|dashboard|settings`
(docs/design/app-screens).

## What is on the DMG

| Item | What it is |
|---|---|
| `Ember.app` | the app |
| `README.txt` | 8 lines for the miner (`dmg/README.txt`), with the seed line |
| `Stop Ember.command` | asks a running copy to stop through its API, then ends leftovers (`app/Stop Ember.command`) |
| `Applications` | symlink, for the drag |
