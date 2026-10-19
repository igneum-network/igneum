@echo off
rem Builds "Igneum Wallet.exe" (the WebView2 window host) on a Windows PC. Double-click this file.
rem Needs Visual Studio with the MSVC v143 x64 component (cl.exe, rc.exe), the Windows SDK's C++/WinRT headers
rem (Windows Hello, biometric.h) and the internet on the first run (the WebView2 SDK comes from NuGet). The output lands in dist\ and, when the extracted payload folder
rem (igneum-windows-app, with igneum-wallet.exe) is next to this folder or its parent, is copied into it, so
rem packaging\windows\BUILD-INSTALLER.bat ships it. Without this exe the installer still works: the Start Menu entry
rem runs igneum-wallet.exe --launch, which opens the dashboard in the default browser.
setlocal EnableDelayedExpansion
cd /d "%~dp0"
set "SDKVER=1.0.2903.40"
set "SDKURL=https://www.nuget.org/api/v2/package/Microsoft.Web.WebView2/%SDKVER%"
if not exist build mkdir build
if not exist dist mkdir dist

rem ---- 1. the MSVC environment ----
set "VCVARS="
rem the Build Tools' own install path (C:\BuildTools, the silent bootstrapper's default on the second Windows machine, 11 October 2026; product
rem A's search) is searched beside the two Program Files roots; the build PC's variable IGNEUM_VCVARS names one directly
if not "%IGNEUM_VCVARS%"=="" if exist "%IGNEUM_VCVARS%" set "VCVARS=%IGNEUM_VCVARS%"
if "%VCVARS%"=="" (
  for %%d in ("C:\Program Files\Microsoft Visual Studio" "C:\Program Files (x86)\Microsoft Visual Studio" "C:\BuildTools") do (
    for /f "delims=" %%f in ('dir /s /b "%%~d\vcvarsall.bat" 2^>nul') do set "VCVARS=%%f"
  )
)
if "%VCVARS%"=="" (
  echo Visual Studio with the MSVC v143 x64 component was not found ^(no vcvarsall.bat under Program Files\Microsoft Visual Studio or C:\BuildTools^).
  pause
  exit /b 1
)
echo [build] MSVC: "%VCVARS%"
call "%VCVARS%" x64 >nul 2>&1
where cl.exe >nul 2>nul || (echo cl.exe is not on PATH after vcvarsall; open a "x64 Native Tools" prompt and run this file from there. & pause & exit /b 1)

rem ---- 2. the WebView2 SDK (NuGet package, a zip) ----
if not exist "build\webview2\build\native\include\WebView2.h" (
  echo [build] downloading the WebView2 SDK %SDKVER%
  powershell -NoProfile -ExecutionPolicy Bypass -Command "$ProgressPreference='SilentlyContinue'; [Net.ServicePointManager]::SecurityProtocol=[Net.SecurityProtocolType]::Tls12; Invoke-WebRequest -Uri '%SDKURL%' -OutFile 'build\webview2.zip' -UseBasicParsing; if (Test-Path 'build\webview2') { Remove-Item -Recurse -Force 'build\webview2' }; Expand-Archive -Path 'build\webview2.zip' -DestinationPath 'build\webview2' -Force"
  if not exist "build\webview2\build\native\include\WebView2.h" (echo the SDK did not unpack; see build\webview2 & pause & exit /b 1)
)

rem ---- 3. the art: the wallet's icon set (site\design\icons\wallet in the tree), or an art\ folder, or the payload ----
set "ART="
if exist "..\..\..\site\design\icons\wallet\icon.ico" set "ART=..\..\..\site\design\icons\wallet"
if exist "..\src-tauri\icons\icon.ico" set "ART=..\src-tauri\icons"
if exist "art\icon.ico" set "ART=art"
if exist "igneum-windows-wallet\icon.ico" set "ART=igneum-windows-wallet"
if "%ART%"=="" (echo icon.ico was not found ^(site\design\icons\wallet, an art\ folder, or the payload^). & pause & exit /b 1)

rem ---- 3b. the version: wallet-version.h written from the workspace's Cargo.toml by write-version.ps1 (the host's file
rem metadata carried 0.0.1 on every cut until 12 October 2026; the audit read it) ----
powershell -NoProfile -ExecutionPolicy Bypass -File write-version.ps1 || (echo the version header was not written & pause & exit /b 1)

rem ---- 4. compile ----
echo [build] rc
rc.exe /nologo /i "%ART%" /fo build\host.res wallet-host.rc || (pause & exit /b 1)
echo [build] cl
rem C++20: the C++/WinRT headers (biometric.h) take their coroutine support from <coroutine>; under C++17 they fall back to
rem <experimental/coroutine>, which MSVC 14.51 (Visual Studio 18) refuses as removed (the Windows builder's build of 0.0.15, 15 October 2026)
cl.exe /nologo /O2 /MT /EHsc /W3 /std:c++20 /DUNICODE /D_UNICODE /I "build\webview2\build\native\include" /Fo"build\\" wallet-host.cpp build\host.res ^
  /link /SUBSYSTEM:WINDOWS /OUT:"dist\Igneum Wallet.exe" "build\webview2\build\native\x64\WebView2LoaderStatic.lib" ^
  user32.lib shell32.lib ole32.lib advapi32.lib gdi32.lib version.lib shlwapi.lib windowsapp.lib crypt32.lib winhttp.lib || (pause & exit /b 1)
echo [build] done: dist\Igneum Wallet.exe

rem ---- 5. into the payload, when it is here (the restart's payload: igneum-windows-wallet with the engine) ----
for %%p in ("igneum-windows-wallet" "..\igneum-windows-wallet" "..\..\igneum-windows-wallet" "..\..\packaging\windows\igneum-windows-wallet") do (
  if exist "%%~p\igneum-wallet-engine.exe" (
    copy /y "dist\Igneum Wallet.exe" "%%~p\" >nul
    echo [build] copied into %%~p
  )
)
echo.
echo Next: packaging\windows\build-wallet-installer.ps1 builds the Setup exe with this host inside.
pause
