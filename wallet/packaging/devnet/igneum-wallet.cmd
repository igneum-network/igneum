@echo off
rem The Igneum Wallet devnet build's launcher (Windows): starts the engine from this package's bin with the page from
rem its ui and opens the page in the default browser (--open); the signer is the engine's own child. The engine's
rem window stays open while the wallet runs; closing it ends the engine and, with it, the signer. State:
rem %USERPROFILE%\.igneum-wallet (the vault, settings, history).
setlocal
set HERE=%~dp0
"%HERE%bin\igneum-wallet-engine.exe" --ui "%HERE%ui" --signer "%HERE%bin\igneum-signer.exe" --open
