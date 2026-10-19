@echo off
rem Builds "Ember.exe" (the WebView2 window host) on a Windows PC. Double-click this file.
rem Needs Visual Studio with the MSVC v143 x64 component (cl.exe, rc.exe) and the internet on the first run
rem (the WebView2 SDK comes from NuGet). The output lands in dist\ and, when the extracted payload folder
rem (ember-windows-app, with igneum-app.exe) is next to this folder or its parent, is copied into it, so
rem packaging\windows\BUILD-INSTALLER.bat ships it. Without this exe the installer still works: the Start Menu entry
rem runs igneum-app.exe --launch, which opens the dashboard in the default browser.
setlocal EnableDelayedExpansion
cd /d "%~dp0"
set "SDKVER=1.0.2903.40"
set "SDKURL=https://www.nuget.org/api/v2/package/Microsoft.Web.WebView2/%SDKVER%"
if not exist build mkdir build
if not exist dist mkdir dist

rem ---- 1. the MSVC environment ----
set "VCVARS="
rem the Build Tools' own install path (C:\BuildTools, the silent bootstrapper's default on the second Windows machine, 11 October 2026) is
rem searched beside the two Program Files roots; the build PC's variable IGNEUM_VCVARS names one directly
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

rem ---- 3. the art: brand\icons in the repo layout, or an art\ folder next to this file ----
set "ART="
if exist "..\..\brand\icons\igneum.ico" set "ART=..\..\brand\icons"
if exist "art\igneum.ico" set "ART=art"
if exist "..\brand\icons\igneum.ico" set "ART=..\brand\icons"
if "%ART%"=="" (echo igneum.ico was not found ^(brand\icons or an art\ folder^). & pause & exit /b 1)

rem ---- 4. compile ----
echo [build] rc
rc.exe /nologo /i "%ART%" /fo build\host.res host.rc || (pause & exit /b 1)
echo [build] cl
cl.exe /nologo /O2 /MT /EHsc /W3 /std:c++17 /DUNICODE /D_UNICODE /I "build\webview2\build\native\include" /Fo"build\\" host.cpp build\host.res ^
  /link /SUBSYSTEM:WINDOWS /MANIFEST:NO /OUT:"dist\Ember.exe" "build\webview2\build\native\x64\WebView2LoaderStatic.lib" ^
  user32.lib shell32.lib ole32.lib advapi32.lib gdi32.lib version.lib shlwapi.lib || (pause & exit /b 1)
echo [build] done: dist\Ember.exe

rem ---- 5. into the payload, when it is here ----
for %%p in ("ember-windows-app" "..\ember-windows-app" "..\..\ember-windows-app" "..\..\packaging\windows\ember-windows-app") do (
  if exist "%%~p\igneum-app.exe" (
    copy /y "dist\Ember.exe" "%%~p\" >nul
    echo [build] copied into %%~p
  )
)
echo.
echo Next: packaging\windows\BUILD-INSTALLER.bat builds the Setup exe with this host inside.
pause
