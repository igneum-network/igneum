Ember an earlier release for Mac (Apple silicon only: M1 or later). The Igneum 2.0 devnet, 8 October 2026. A test network; nothing is bought or sold.
A GPU-secured network for Ethereum-compatible applications and verifiable computation.
Nobody from Igneum will ever ask for your seed.
1. Drag "Ember" into Applications, then open it from there.
2. First time: macOS says it cannot verify the app. Tonight's build is unsigned (Developer ID signing and notarization
   follow with the Apple enrolment). Open it in two steps: open the app once and let macOS refuse; then open
   System Settings, Privacy and Security, scroll down, click "Open Anyway", and open the app again.
3. The window walks you through two steps: your GPU (switch it on, pick the identities) and where rewards go
   (make an address here, or paste your own). Then it starts the node, syncs (usually under a minute) and mines.
4. The dashboard shows your hash rate, your blocks, the node and the countdown to the next hourly program.
   Your miner appears on igneum.network/live within about a minute of its first block.
5. Closing the window keeps mining in the menu bar (the flame icon). Quit from there or from the window to stop:
   the miner stops first, then the node. "Stop Ember" on this disk does the same for a stuck copy.
Data: ~/Library/Application Support/Igneum (devnet-v4 chain, app/wallet.json for a made address). Logs: ~/Library/Logs/Igneum.
