# Igneum Wallet

Desktop wallet on the miner's bones: a Rust engine (`src/`) that keeps the key encrypted, signs, reads a node and
verifies finality certificates, plus a window served on 127.0.0.1 (`ui/`). macOS host: `app/mac/IgneumWallet.swift`;
packaging: `packaging/mac/build-wallet-dmg.sh`, `packaging/windows/Igneum-Wallet.iss`. Shared code with the miner:
`app/igneum-common`.

## Versions

| Version | Date | What |
|---|---|---|
| 0.1.0 | 4 Oct 2026 | first DMG; built before `/coin.png` existed, so the coin on the home screen is blank; updates download and offer "Open the download" only |
| 0.1.1 | 5 Oct 2026 | coin served from `brand/igneum-coin-1024.png`; over-the-air updates v2 (unattended install) |
| 0.1.2 | 5 Oct 2026 | Touch ID (macOS) and Windows Hello (Windows, untested): unlock, confirm sends, show the backup, idle lock; the coin and the "reading the chain" line on the balance card; the version in the header and in Settings |
| 0.1.3 | 5 Oct 2026 | the update card: one centred card over the window when a new version is ready, with what changed and one tap to install (`ui/update-card.js`) |
| 0.1.6 | 8 Oct 2026 | the page bridge (`src/bridge.rs`, `src/eip712.rs`, `site/wallet-provider.js`): websites connect through 127.0.0.1:26811 after your approval in the window; eth_requestAccounts, eth_chainId, eth_sendTransaction, personal_sign and typed data, each shown and confirmed here (Touch ID or Windows Hello when enrolled), the finality line after a transaction; Settings lists the connected sites; a page without approval gets nothing |
| 0.1.4 | 5 Oct 2026 | the lock screen (`ui/lock-screen.js`): the coin on the ember glow, the name, the short address, one control; the Touch ID sheet on a tap, once by itself when the wallet opens (setting), never on an idle lock; locking is a 250 ms transition; the idle lock's minutes in Settings (1, 5, 15, 60, never) |

## The lock screen (ui/lock-screen.js, index.html #screen-unlock, app.js onLockScreen; 0.1.4)

The coin large and centred on the obsidian ground with the ember glow (it breathes over 6 s; reduced motion stops
it), "Igneum Wallet" in Unbounded, the wallet's short address in mono, one primary control. With Touch ID (or Windows
Hello) on and the app window as the host: the fingerprint button "Unlock with Touch ID" in ember, its line under it,
and a quiet "Use password" that reveals the password field in place (the control area keeps one height, so nothing
above it moves). Otherwise the password field is the control and the button is absent. Locking (the Lock button,
the menu bar, the idle lock) is a 250 ms transition from the home screen to the lock screen, not a cut.

When the system sheet appears (`LockScreen.autoPrompt`, the rules in `ui/lock-screen.test.mjs`):

| Moment | The sheet |
|---|---|
| a tap on the button, or Return or Space on it (it has the focus on arrival, and again when the window comes back) | yes |
| the first arrival after the person opened the app, with "Ask for Touch ID when the wallet opens" on (Settings, default on) | once, 600 ms after the lock screen is fully drawn |
| the idle lock, the Lock button or menu item, the window coming back from the menu bar or the Dock | never |
| after a cancelled or unmatched sheet | never; the line says "Touch ID cancelled, tap to try again" |
| the first run after an over-the-air update (the updater opened the app, not the person) | never |
| the window not visible at arrival (opened at login behind another app) | never |

After the sheet the line under the button, in our words and never a modal: "Touch ID confirmed, unlocking", "Touch
ID cancelled, tap to try again", "Touch ID did not match, tap to try again", "Enter your password" (the sheet's
Use password button reveals the field), "Touch ID is locked, use your password". A sheet that cannot help (locked,
invalidated, not set up, a browser tab) reveals the password field and focuses it. Escape in the password field
returns to the button. `?bio=touch` on the page shows the Touch ID layout without a host (screenshots).

## Touch ID and Windows Hello (src/engine.rs, igneum-common/src/biometric.rs, app/mac/Biometric.swift, app/windows/biometric.h)

What it gates once "Use Touch ID" is on (Settings): unlocking at start and after the idle lock, every send, and
showing the words or the key. The password still works for all three. Nothing else asks for a finger.

| Piece | Where | What it does |
|---|---|---|
| the gate | `igneum-common/src/biometric.rs` | nonces the engine issues, the host confirms and the action consumes once; the one-time enrolment token; the state in `/api/state` |
| the engine | `src/engine.rs`, `src/server.rs` | `/api/biometric/*`; `/api/send` refuses without a confirmed nonce for that quote while enrolled; `/api/reveal` with a nonce reads the unlocked key in memory; the idle lock; the `HOST {...}` line on stdout |
| the Mac host | `app/mac/Biometric.swift` | the `biometric` script message handler; `LAPolicy.deviceOwnerAuthenticationWithBiometrics`; the Secure Enclave seal |
| the Windows host | `app/windows/biometric.h` | the `window.chrome.webview` bridge; `UserConsentVerifier` through `IUserConsentVerifierInterop`; DPAPI |
| the page | `ui/app.js` | the fingerprint controls on the unlock, send and Settings screens; the activity ping for the idle lock |

The prompt's lines, worded by the engine or the host, never by the page, in our voice, under 60 characters, no
trailing full stop:

| Prompt | Line |
|---|---|
| unlock | Unlock your wallet |
| send | Send 1.5 IGN to 0x7E5F…5Bdf (the amount to 4 decimals; the address as its first 6 and last 4 characters) |
| backup and export | Show your recovery words |
| turn on | Turn on Touch ID for your wallet |

The prompt's buttons: "Use password" (the fallback button; the policy is biometrics only, so it never reaches the
device password: the window asks for the wallet's own password) and "Cancel". After the system prompt the page shows
its own line with the fingerprint glyph in ember: "Touch ID confirmed, unlocking", "Touch ID cancelled, enter your
password", "Touch ID did not match, try again or enter your password", and so on (`bioLine` in `ui/app.js`). The
prompt's title and icon are the bundled app's ("Igneum Wallet", the mark): the window host is the bundle's own
executable, so the system attributes the prompt to it; a bare engine or a test binary shows its own name instead.
macOS composes the sentence itself ("Igneum Wallet is trying to unlock your wallet."), so the Mac host lowercases
the line's first letter at display time; Windows Hello shows the line on its own, with its capital.

The flow for a send: the quote (`/api/send/quote`) comes with `confirm_nonce` and `confirm_reason`. The page hands the nonce to the host; the host
reads the reason from the engine with its host token, shows the prompt with that line, and on success posts
`/api/biometric/confirm`. The page then calls `/api/send` with the quote and the nonce; the engine checks the nonce
was confirmed within 30 s, for this exact quote (address, value, transaction nonce, chain id), and not used before.
Unlock: the host shows the prompt, unseals the password and posts it to `/api/unlock` itself. The backup: a
`reveal` challenge, the prompt, then `/api/reveal` with the nonce; the words come from the key already unlocked in
memory, so the password is neither typed nor stored for it.

The host token: the engine prints `HOST {"token": ..., "biometric_file": ...}` on its stdout, which only the window
host reads. The host sends it as `X-Igneum-Host` on the calls only it may make (status, report, confirm, taking the
parked password at enrolment, recording the enrolment). The page cannot confirm its own challenges.

Enrolment: the page posts the password to `/api/biometric/enrol/begin`; the engine checks it against the vault and
parks it under a one-time token for 120 s; the host shows the prompt ("Turn on Touch ID for Igneum Wallet"), takes
the password with the token (once), seals it and writes the file, then posts `/api/biometric/enrolled`. A password
change deletes the sealed file and Settings asks to turn Touch ID on again. Removing the wallet deletes it too.

The idle lock: with Touch ID on, Settings > "Lock when idle" chooses 1, 5 (the default), 15 or 60 minutes, or never
(`settings.json: idle_lock_min`, 0 for never; `idle_lock` mirrors on/off for 0.1.2 and 0.1.3). The engine zeroes
the key after that long without the window reporting input (mouse, keys; `/api/activity` every 20 s while there is
some), never during a send or with a confirm screen open. The next unlock is a tap on the button, or the password;
the sheet is never raised by itself after an idle lock.

### What is stored, and where (macOS)

The packaging signs the app ad hoc. Under an ad hoc signature macOS refuses every Keychain item that carries a
biometric access control, on the login keychain and the data-protection keychain alike (`errSecMissingEntitlement`,
-34018: `keychain-access-groups` needs a team signature; measured 5 October 2026 on this Mac with a 40-line probe).
A Secure Enclave key with the same control is allowed, so the password is sealed to one instead:

- enrol: a P-256 key is made in the Secure Enclave with `SecAccessControl(.privateKeyUsage, .biometryCurrentSet)`;
  an ephemeral P-256 key agrees a shared secret with its public half (no prompt), HKDF-SHA256 derives an AES-GCM key
  and the password is sealed. `~/Library/Application Support/Igneum/wallet/biometric.json` (0600) holds the Secure
  Enclave key's wrapped form, the ephemeral public key and the box.
- unlock or confirm: the host evaluates `LAPolicy.deviceOwnerAuthenticationWithBiometrics` (fallback button "Use
  password", which only closes the prompt with `LAError.userFallback`; the device password is never offered), then
  uses the Secure Enclave key under that context. The key agreement runs on every confirm
  too, so a changed fingerprint set is caught on a send, not only on an unlock.
- `.biometryCurrentSet`: a fingerprint added or removed in System Settings makes the Secure Enclave refuse the key.
  The host reports "invalidated"; the window says to use the password and turn Touch ID on again.

Windows: the password is sealed with DPAPI (`CryptProtectData`, current user, `CRYPTPROTECT_UI_FORBIDDEN`) only after
a `UserConsentVerifier` success and written to `%LOCALAPPDATA%\igneum\wallet\biometric.json`. DPAPI has no
biometric binding of its own: the host unseals only after Hello verified.

### Threat model

| | Touch ID protects | Touch ID does not protect |
|---|---|---|
| Casual access at an unlocked Mac (someone at the keyboard while the wallet is locked) | yes: no finger, no unlock, no send, no words; the idle lock closes the window within 5 minutes of nobody being there | |
| A copy of `vault.json` taken off the machine | yes, as before: Argon2id + XChaCha20-Poly1305 under the password; the sealed file is useless off this Mac's Secure Enclave | |
| A copy of `biometric.json` by another user on this Mac | yes: 0600, and the Secure Enclave key is bound to this device and the current fingerprint set | |
| A root attacker, or malware running as the signed-in user | | no: it can read the wallet's memory while unlocked, patch the app, or drive the window; the sealed file is as strong as the user's macOS login and Secure Enclave, not stronger |
| Someone who knows the password | | no: the password works everywhere Touch ID does, by design |
| A fingerprint added to this Mac by someone with the macOS password | | the Secure Enclave key dies (`.biometryCurrentSet`), so the new finger cannot unlock; the person with the macOS password could still enrol again, which needs the wallet password |
| The page (ui/) or a cross-site page in the browser | yes: the host token is never in the page; confirmations are host-only; `/api/send` binds the nonce to the quote; the same-origin guard stays | |

Windows (untested): DPAPI protects against other accounts and offline copies, not against code running as the user;
the same table applies with "Windows Hello" and "the Windows account".

### Verified (5 October 2026)

- Unit tests: `cargo test biometric` in `app/igneum-common` (confirm/take once, replay, expiry, stale, mismatch,
  the enrolment token, reason clipping, the constant-time compare, the binding).
- The Swift host compiles with `Biometric.swift` and links LocalAuthentication; the DMG builds.
- The probe: `SecItemAdd` with `.biometryCurrentSet` fails with -34018 under the ad hoc signature; a non-permanent
  Secure Enclave key with the same control is created, exported (`dataRepresentation`, 569 bytes), re-imported, and
  the ephemeral key agreement seals a box without a prompt.

### Untested

- The Windows path: `biometric.h` was written on a Mac; the first compile is BUILD-WALLET-APP.bat on the runner.
- See the report for whether the Touch ID prompt was exercised end to end on this Mac (a finger is needed).

## Over-the-air updates (src/updater.rs, igneum-common/src/{fetch,ota}.rs)

The signed manifest `igneum-wallet-latest.json` (+ `.sig`, the miner's Ed25519 key) is published with
`packaging/ota/publish-manifest.sh --product wallet --version <v> --mac packaging/mac/dist/Igneum-Wallet-<v>.dmg --notes "..." --deploy`.
The engine checks it 25 s after start, then hourly (10 minutes after an error), and from Settings > Check for updates.

States (`state.update.status`, what the banner says):

| State | Meaning |
|---|---|
| off | the build has no manifest URL |
| unknown | not checked yet |
| checking | fetching and verifying the manifest |
| current | this is the latest version |
| available | a newer version has a build for this platform; the download starts at once |
| downloading | curl with resume into `<wallet data>/updates/`; size and sha256 checked against the manifest |
| staging | macOS: the DMG mounted, the bundle copied next to the running one as `.Igneum Wallet.app.new`, its engine asked `--version`, the bundle digested; Windows: the installer is the staged artefact |
| ready | waits for the safe moment (below); "Install now" applies at once; "Later" hides the banner for that version only |
| applying | the download and the staged bundle re-verified, `update-pending.json` written, the helper started; macOS: the engine quits and the helper swaps the bundle and opens the new app |
| deferred | Windows only: the installer's administrator prompt was not answered; retried in 6 hours or on Install now |
| manual | the engine cannot swap itself (not in a bundle, Applications not writable): "Open the download" |
| error | what failed, in `state.update.error`; a version whose apply failed is never re-applied by itself |

The window shows an update twice over. The card (`ui/update-card.js`, `renderUpdateCard` in `ui/app.js`) is the
first sight: one centred card with the mark and a progress ring, "Igneum Wallet 0.1.3", one line, up to three lines of
the manifest's notes (the rest behind "What changed"), the size, Install now and Later. It never opens while the send
screen is open, while a Touch ID or Windows Hello line is on screen, or while a wallet is being created or imported;
it waits and comes once that is over. Later (also Escape and the backdrop) hides that version at that stage and
leaves the banner; the card comes back for a newer version, or when the download is ready and automatic updates are
off. An open card follows its update through downloading, ready, installing ("Installing. The app restarts itself.")
and failed (the one-line cause, Try again). The banner is the small strip that stays. `?update=<state>[&auto=0][&card=1]`
on the page shows each state without a manifest; `ui/update-card.test.mjs` checks the words and the rules.

The setting "Install updates by itself when nothing is being sent" (`settings.json: auto_update`) defaults to on.
The safe moment is no send in flight: no `/api/send` running, no quote given in the last 180 s (a confirm screen may
be open), no sent transaction still waiting for its block, no create flow half way. A version below the manifest's
`min_supported_version` installs at once.

Rollback: the macOS helper puts `Igneum Wallet.app.previous` back when the new app does not start twice. The new
engine counts its starts in `update-pending.json`; on the third start without 90 healthy seconds it restores the
previous version (never below `min_supported_version`). The window shows "Updated from X" on the first run after an
update and "Rolled back: ..." after a restore.

Files in `~/Library/Application Support/Igneum/wallet/` (Windows: `%LOCALAPPDATA%\igneum\wallet\`): `updates/`
(manifest, download), `update-pending.json`, `update-result.json`, `ota-apply.sh` or `ota-apply.ps1`,
`ota-apply.log`, `failed-versions.json`, `ota-relaunch.env` (test runs only).

### Verified (5 October 2026)

- Unit tests: manifest parse, version comparison, plan, safe moment (`cargo test` in `app/igneum-wallet`); helper
  templates, pending/result files, env file, digest recipe (`cargo test` in `app/igneum-common`).
- End to end on this Mac with a scratch copy of the 0.1.1 bundle against a 0.1.2 test manifest served from
  127.0.0.1 (`publish-manifest.sh --dest <folder> --base-url http://127.0.0.1:<port>`; the engine run with
  `IGNEUM_APP_DATA`, `IGNEUM_WALLET_UPDATE_MANIFEST`, `IGNEUM_WALLET_UPDATE_FIRST_SECS=3`, `IGNEUM_OTA_RELAUNCH_ENGINE=1`
  so the helper relaunches the engine alone): check, download, stage, apply, swap, relaunch as 0.1.2,
  "updated to Igneum Wallet 0.1.2 from 0.1.1".
- For real on the founder's Mac, through LaunchServices with the real window host: 0.1.1 -> 0.1.2 (5 Oct 2026, 09:14Z)
  and 0.1.2 -> 0.1.3 (5 Oct 2026: check posted 13:23:18Z, staged 13:23:31Z, applied 13:23:40Z, 0.1.3 up 13:23:46Z,
  28 s end to end; `/api/state` then showed `updated_from` 0.1.2 and `current`).
- 0.1.3 -> 0.1.4 (5 Oct 2026): check posted 15:48:50Z, downloaded and verified 15:48:52Z (`staging` in `/api/state`),
  staged bundle digested 15:49:15Z, helper started 15:49:16Z (the 0.1.3 engine gone), 0.1.4 up 15:49:19Z with
  `updated_from` 0.1.3 and `unknown`, `current` at 15:49:45Z after its own check. 0.1.4 raised no Touch ID sheet on
  that first run (the updater's relaunch, not the person's: `last_op` stayed empty).
- The download token rotated on 5 Oct 2026 (docs/plans/rotation-phase-2.md): `publish-manifest.sh` writes the NEW
  folder (it reads its token file under the user's config directory), and a 0.1.4 bundle points there. The installed 0.1.3 polls the OLD
  folder's `igneum-wallet-latest.json`, which phase 2 had removed (the plan's section 8b kept only the miner's bridge),
  so the 0.1.4 manifest, its signature and the DMG were copied into the OLD folder too, as a bridge, until the 0.1.3
  wallets have moved; the old folder goes on 7 October (section 8f).

### Untested

- The Windows path (installer first, `/IGNOTA=1`, deferral on an unanswered prompt): copied from the miner's, never
  run for the wallet. Needs the wallet installer on the GitHub runner and a PC.
- The rollback paths (the helper's "did not start twice", the engine's third-start restore) and `deferred`.
- A version below `min_supported_version` (no wallet manifest has set one).
- Code signatures: bundles are signed ad hoc by the packaging script; the updater verifies the manifest's sha256 and
  the staged bundle's digest, not a Developer ID signature (the miner does the same).
