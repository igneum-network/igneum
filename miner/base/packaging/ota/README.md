# Over-the-air updates (packaging/ota)

the founder's rule (4 October 2026): every app updates itself and downloads the update without being asked. The Igneum
Miner app on Windows and macOS does, and the node, the miner and the GPU workers ship inside it, so a consensus
upgrade (a height-activated rule such as difficulty v2) reaches every node before its activation height.

The launcher packages (`proto-cuda/windows-app`, the `igneum-windows-v4.zip` console launchers, the Terminal DMGs
0.1.0 and 0.2.0) are NOT auto-updated, by design: they are the engineering path and are replaced by hand.

## The pieces

| Piece | Where | What it does |
|---|---|---|
| manifest | `dl.igneum.network/dl/<token>/igneum-app-latest.json` + `.sig` | version, per-platform file (url, sha256, size, kind), min_supported_version, notes, consensus activation height |
| signer | `app/igneum-app/src/bin/ota-sign.rs` (`igneum-ota-sign`, built with the app, never shipped) | keygen, sign, verify, sha256; includes `src/manifest.rs` so it signs what the app verifies |
| publisher | `packaging/ota/publish-release-manifest.sh` | copies the DMG or installer into the downloads folder, writes the canonical manifest, signs it, prints or runs the deploy |
| verifier | `app/igneum-app/src/manifest.rs` | Ed25519 check of the manifest bytes with the compiled-in public key, parse, version compare, safe-moment rule, unit tests |
| updater | `app/igneum-app/src/ota.rs` | check, download with resume, verify, stage, apply at a safe moment, rollback; `update` in `/api/state` |
| dashboard | `app/igneum-app/ui` | one banner (available, downloading, ready, applying, red bar for a close fork), Settings: Check now, Install now, automatic switch |
| Windows installer | `packaging/windows/Ember.iss` | `CloseApplications=yes`, `RestartApplications=no`, a `[Run]` relaunch on `/IGNOTA=1` |
| CI loop | `packaging/windows/fetch-ci-artifacts.sh` | after copying the installer it calls `publish-release-manifest.sh --win` (the Mac entry is carried over); `--deploy` ships both |

## Keys

Generated once on the Mac (4 October 2026), never in the repo or in CI:

    app/igneum-app/target/release/igneum-ota-sign keygen $IGNEUM_SECRETS_DIR/ota-signing-key $IGNEUM_SECRETS_DIR/ota-signing-key.pub

`ota-signing-key` is the 32-byte seed as hex, mode 0600. The public key is the constant `OTA_PUBLIC_KEY_HEX` in
`app/igneum-app/src/manifest.rs`; `igneum-ota-sign embedded` prints it with its fingerprint (SHA-256 of the 32 key
bytes). `publish-release-manifest.sh` refuses to sign when the embedded key is not the one in `$IGNEUM_SECRETS_DIR/igneum`.

Key rotation: a new key means a new app build (the constant), published and signed with the OLD key, then the next
manifest signed with the new one. Apps that skipped the bridge build stop updating and show "manifest signature does
not verify"; they are updated by hand from the download page.

## Publishing a version

1. Bump `version` in `app/igneum-app/Cargo.toml` (and `app/windows/version.h`, `resources/igneum-app.rc`, as the
   CI smoke run demands). Commit, push: the Windows installer builds on GitHub.
2. Mac: `packaging/mac/build-dmg.sh`, then

        packaging/ota/publish-release-manifest.sh --version an earlier release --mac packaging/mac/dist/Ember-an earlier release.dmg \
            --notes "difficulty v2 and over-the-air updates" [--activation-height 120000 --deadline-note "difficulty v2"]

   writes `dl/<token>/igneum-app-latest.json` with the Mac entry only and prints the deploy command. Deploying now is
   fine: a Windows app finds no `windows` entry and does nothing.
3. Windows: `packaging/windows/fetch-ci-artifacts.sh --deploy` copies the installer, adds the Windows entry to the
   same manifest (same version, Mac entry carried over), deploys the downloads folder.
4. Every app checks within the hour (`Settings > Check now` at once): it downloads, verifies and installs at the next
   safe moment. The event feed shows each step; `app-<run>.log` has the detail.

`--min-supported an earlier release` marks older versions unsupported: they install at once, without waiting for a safe moment,
and show the red bar. `--activation-height N` does the same once a node's DAA score is within 1,800 blocks of N.

## What the app does

Check on start (20 to 50 s in) and every 60 minutes plus up to 10 minutes of per-machine jitter; after an error,
again in 10 minutes. Both files come through curl (the engine carries no TLS stack); the signature is checked over
the manifest bytes before parsing; a version that is not newer, or a manifest without this platform, ends the round.

Download into `<app data>/app/updates/` (`~/Library/Application Support/Igneum/app/updates`,
`%LOCALAPPDATA%\igneum\app\updates`) with `curl -C -` (resume) and `--retry 3`; then the size and the sha256 from
the manifest; a file already there with the right hash is not fetched again. The banner shows the percentage.

Stage. macOS: mount the DMG (or unpack the zip), copy `Ember.app` to `.Ember.app.new` next to the
running bundle (same volume: the swap is two renames), run its engine with `--version` and demand the manifest's
version. When the folder is not writable the state is `manual`: the banner says so and offers "Open the download".
Windows: the installer is the staged file.

Safe moment (`manifest::safe_to_apply`): the network has not lost over 30% of its identities in 10 minutes, this
minute is the machine's slot, node synced, no hourly program boundary within 180 s (`program.eta_s`), no worker
starting. Urgent (fork within 1,800 blocks, or unsupported version, or Install now) skips the wait. A ready update
that found no safe moment for 6 hours applies anyway in its slot (an unsynced node mines nothing).

Apply. The engine writes `update-pending.json` (from, to, starts), starts the helper detached and leaves through its
normal quit path: miners first (8 s grace), then the node (30 s), the last log upload, `EXIT` for the window.
- macOS helper `ota-apply.sh`: waits for the engine, asks the window (`network.igneum.ember`) to quit, moves the
  bundle to `Ember.app.previous`, the staged one in, strips quarantine, `open -n`. If the new engine is not
  running after 30 s it opens once more; if that fails too it puts `.previous` back and reports.
- Windows helper `ota-apply.ps1` (an earlier release, after the 4 October incident below): runs `Ember-Setup-<v>.exe
  /VERYSILENT /SUPPRESSMSGBOXES /NORESTART /CLOSEAPPLICATIONS /IGNOTA=1 /LOG=...` FIRST, with the engine still
  mining. The installer is per-user since an earlier release (`PrivilegesRequired=lowest`,
  `{localappdata}\Programs\Ember`), so nothing asks for an administrator; its `PrepareToInstall` runs
  `stop-igneum.ps1` (api/quit: miners first, then the node, then the window), replaces the files, and the `[Run]`
  entry on `/IGNOTA=1` relaunches `igneum-app.exe --launch`. An older install in Program Files (an earlier release and before)
  still raises one UAC prompt through ShellExecute: declined, timed out or unanswered, the helper writes
  `deferred:true`, the engine keeps mining, logs `OTA: waiting for administrator approval` / `OTA: administrator
  approval not given ...; mining continues`, shows "waits for the next time someone is at this PC" and retries on
  Install now, at the next start, or after 6 hours. The engine is never stopped before the installer is running.
- Machines take turns: an update applies only in the machine's own minute of the hour (`manifest::slot_minute`:
  the first 8 hex of the machine id modulo 60; the second Windows machine = :58), and never while `/api/live` shows the network lost
  over 30% of its identities (`state.miners_10m`) in the last 10 minutes. Urgent (fork close, unsupported) and
  Install now skip both.
- Per-user install, migration: the data (`%LOCALAPPDATA%\igneum`: chain, wallet, settings) is the same folder for
  both installs, nothing is copied. A hand-run installer offers to remove the Program Files copy (one
  administrator prompt for its uninstaller); a silent OTA install leaves it and says nothing. The login entry
  (HKCU Run) is rewritten to the new path on first start. The inbound firewall rule for `igneumd.exe` is requested
  once on the first run of a per-user install (one prompt, in a thread; declined or unanswered = the node dials out
  and mines without it, never asked again).

Field incident, 4 October 2026 15:40 UK: an earlier release published; both Windows PCs (an earlier release, nobody at either keyboard)
reached apply, stopped the miners and the node, then sat at the installer's UAC prompt. The chain fell to one
laptop. The an earlier release helper waits for the engine to exit before it runs the installer, so a prompt nobody answers
strands the machine; an earlier release inverts the order (installer first, engine stops only when the installer runs) and the
per-user installer removes the prompt altogether. The an earlier release engines still carry the old helper: their an earlier release update
raises the prompt once more (with an earlier release's R4.3.6 rule the version is then marked failed, not retried); click Yes
once, or run the an earlier release installer by hand.

Rollback. The helper writes `update-result.json`; the new engine reads it on start and reports "updated to X from
Y" or the error. The new engine counts its starts in `update-pending.json` and deletes the file after 90 healthy
seconds; a third start without reaching that point restores the previous version (macOS: the `.previous` bundle;
Windows: the previous version's installer kept in `updates/`, so the FIRST update from an earlier release has no rollback target
on Windows, said so in the state) and shows "rolled back" in Settings.

Settings: `auto_update` (default on). Off: downloads still happen, the banner waits for Install now. The forced
screenshots: `?update=available|downloading|ready|waiting|applying|urgent|manual|error|updated` on the dashboard URL
(and `?job=running|done|failed` for the remote-job notice). The dashboard shows one notice at a time in the strip under
the header, the most important first (app/igneum-app/ui/app.js, Notices).

## Testing

Unit tests: `cargo test` in `app/igneum-app` (manifest parse, a bad signature and a tampered manifest refused,
sha256 of a file, version ordering incl. pre-releases, safe-moment rules, fork closeness, unsupported versions).

Dry run on the Mac, 4 October 2026 (`packaging/mac/README.md` has the private-devnet recipe; ports 29700+):
the an earlier release bundle from the tree ran under its window host against a private devnet with
`IGNEUM_APP_UPDATE_MANIFEST=http://127.0.0.1:29790/dl/<token>/igneum-app-latest.json` (a `python3 -m http.server`
over a folder written by `publish-release-manifest.sh --base-url ... --dest ...`; loopback http is the one non-https URL the
parser accepts), found the an earlier release manifest, downloaded and verified the DMG, staged the bundle, waited for the worker
to start, applied, and came back as an earlier release with "updated to Ember an earlier release from an earlier release" in the event feed. The
screenshots are `docs/design/app-screens/update-*.png`. Windows: reviewed only, see `TEST.md`.

## Remote jobs (4 October 2026)

the founder's rule: one app on both PCs that the Mac can send commands and files to over the line, so everything is tested
and built without a person at the PC. The channel is `igneum-jobs.json` plus `igneum-jobs.json.sig`, next to the
update manifest, signed with the same OTA key and verified by the same code. Since an earlier release (5 October 2026) every app
holds a long-poll on the relay's public `/wake` and fetches the file within seconds of `--deploy` (the script records
the new stamp there); a poll every 2 minutes is the fallback (it was 10 minutes before an earlier release).
The relay (`relay/`) stays for the Mac and for humans; its PC agent is replaced by this.

| Piece | Where |
|---|---|
| model: parse, verify, targeting, expiry, the once-per-id ledger (`jobs-state.json` in the app data folder), unit tests | `app/igneum-app/src/jobs.rs` |
| runner: poll, requirement probes, the kinds, reports, the dashboard state | `app/igneum-app/src/jobrun.rs` |
| signer: `igneum-ota-sign sign-jobs` and `verify-jobs` (a bad job is refused at signing) | `app/igneum-app/src/bin/ota-sign.rs` |
| publisher: `packaging/ota/publish-jobs.sh add|list|remove|sign [--deploy]` | this folder |
| reader: the published file (signature checked), status per machine, a job's result, watch | `tools/jobs.mjs` |

A job: `{id, kind, title, created_at, expires_at, target: {machine_ids: [...] | "all", platform, requires}, params,
report: "log-upload"}`. Machine ids are the per-install id (16 hex) or its first 8 (the first Windows machine `ae432dc7`, the second Windows machine
`1ccfe586`). Requirements the engine probes: `wsl`, `wsl-prover` (cargo, `~/.sp1` and `~/igneum-prove` inside
Ubuntu-24.04), `nvidia`; an unknown one is never met and the job waits until it expires.

| Kind | What the app does | Params |
|---|---|---|
| `run` | writes the script body to `<app data>/app/jobs/<id>/` and runs it (powershell or bash), output captured, exit code reported | `script`, `shell`, `elevated`, `stop_miners_first`, `timeout_minutes` (60, at most 600) |
| `fetch` | downloads by https, checks the sha256 (and size), into the job folder or a named app folder; can extract | `url`, `sha256`, `size`, `dir` jobs/prove/packs/updates, `to`, `extract`, `extract_dir`, `fresh` |
| `collect` | uploads files matching globs under the app data root, and/or a command's output, to the intake | `globs`, `command` |
| `restart` | miners, node (the miners follow) or the app (a detached helper opens it again) | `what` |
| `update-now` | the updater checks and installs a newer version at once | |
| `shard-benchmark` | miners stopped, GPU under 5%, the prove zip fetched fresh, `prove-shard.sh` inside WSL under the cap, RESULT and STAGE lines and `results/*.json` uploaded, miners back | `zip_url`, `sha256`, `size`, `fixtures`, `cap_minutes` (90), `distro`, `wsl_user` |
| `build` | mining untouched; free space checked (20 GB on the drive and in the distro), the build-inputs zip fetched (sha256), setup inside WSL2 as root (mingw-w64, clang, protoc, zstd, the Windows rust target; idempotent), sources extracted, `cargo build --release` native and for `x86_64-pc-windows-gnu`, `cargo test` for the manifest's packages, binaries zstd-compressed and sent to the relay (50 MB each) with sha256 in RESULT lines; the target dir under `/opt/igneum-build` persists between jobs. `src/jobbuild.rs`, `packaging/windows/push-build-inputs.sh`, `tools/build-job.mjs` | `zip_url`, `sha256`, `size`, `targets` (linux, windows), `budget_minutes` (40), `stage_minutes` {stage: min}, `min_free_gb` (20), `tests` (true), `relay_url`, `nice` (19), `cargo_jobs`, `distro`, `wsl_user` |

Reports: every job uploads to the log intake as run_id `job-<id>-<machine id8>` with the label `job-<kind>`; the
first line is `SUMMARY {json}` (status, exit, times, the RESULT lines, per-kind extras), then the captured output.
Long jobs upload a running report every 5 minutes; collected files and result files are separate labels
(`file-<name>`, `result-<name>`, `prove-log`) under the same run_id.

Safety: the file is rejected when the signature or any job's `expires_at` or params fail (since an earlier release a job of a kind the app does not know is skipped instead of rejecting the file; an earlier release and earlier reject the whole file, so a new kind reaches the PCs in an app update before its first job is published); a job id runs once per
machine (a crash mid-job counts); nothing writes outside the app data folder except an explicit `run` script,
which is the operator's responsibility; the dashboard shows the running job and a history (id, kind, started, exit,
report uploaded); Settings has "Allow remote jobs from Igneum (signed)", ON by default on this devnet build, with
the key fingerprint, and OFF aborts the running job. `elevated` needs someone at the UAC prompt (or UAC set to
elevate without prompting); unattended it fails after the prompt times out. A `restart app` or `update-now` job
relaunches through the usual quit path.

Publishing and reading:

    packaging/ota/publish-jobs.sh add --kind shard-benchmark --target 1ccfe586 --title "Shard proof run on the 5090" --deploy
    packaging/ota/publish-jobs.sh add --kind run --target ae432dc7,1ccfe586 --script fix.ps1 --title "..." [--elevated] [--stop-miners]
    packaging/ota/publish-jobs.sh add --kind build --target ae432dc7 --deploy     (after packaging/windows/push-build-inputs.sh; or node tools/build-job.mjs run)
    packaging/ota/publish-jobs.sh list | remove <id> | sign
    node tools/jobs.mjs | status | <id> [--all] | watch <id>

Tested on the Mac, 4 October 2026: the unit tests (parse, bad jobs refused, signature and tampering, times,
targeting, the once-only ledger, globs), the signer with the real key, the publisher against a scratch folder, the
reader against the live intake; the crate also compiles for `x86_64-pc-windows-gnu`. Not yet run on a PC: the
first job goes to the second Windows machine (`1ccfe586`) once an earlier release is installed there (`docs/plans/shard-test-pc2.md`).
