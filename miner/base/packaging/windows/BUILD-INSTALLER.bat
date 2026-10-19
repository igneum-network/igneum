@echo off
rem Builds Ember-Setup-0.1.0.exe on a Windows PC. Double-click this file. The first run needs the internet:
rem Inno Setup 6 comes through winget if it is missing, rcedit stamps the icon into the exes if they do not carry it yet.
rem The payload is the extracted igneum-windows folder next to this file, or the zip at the URL in build-installer.ps1.
cd /d "%~dp0"
where powershell >nul 2>nul
if errorlevel 1 (
  echo PowerShell was not found. Windows 10 and 11 ship it.
  pause
  exit /b 1
)
powershell -NoProfile -ExecutionPolicy Bypass -File "%~dp0build-installer.ps1" %*
if errorlevel 1 (
  echo.
  echo The build did not finish. The reason is in the lines above.
)
pause
