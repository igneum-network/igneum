@echo off
rem Stop Ember: the Start Menu entry. Asks the engine to stop its workers, then ends what is left.
title Stop Ember
cd /d "%~dp0"
powershell -NoProfile -ExecutionPolicy Bypass -File "%~dp0stop-igneum.ps1"
pause
