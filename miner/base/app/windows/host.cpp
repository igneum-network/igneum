// Ember for Windows: the window around the engine. One C++ file, built on the PC by BUILD-APP.bat
// (MSVC, the WebView2 SDK from NuGet, static loader). It starts igneum-app.exe --wrapper next to it, reads
// "URL ..." / "STATE {...}" / "EXIT" from its stdout, shows the URL in a WebView2 control, keeps a tray icon with the
// state, and on quit writes "quit" to the engine's stdin and waits for EXIT (miners stop first, then the node).
// Closing the window hides it to the tray; the miner keeps running. If the WebView2 runtime is missing the dashboard
// opens in the default browser instead and the window says so. 4 October 2026.
//
// Untested on a real PC at the time of writing (written on a Mac): BUILD-APP.bat is the first run.

#define WIN32_LEAN_AND_MEAN
#ifndef UNICODE
#define UNICODE
#endif
#ifndef _UNICODE
#define _UNICODE
#endif
#include <windows.h>
#include "opening_size.h"
#include <shellapi.h>
#include <shlwapi.h>
#include <dbt.h>
#include <wrl.h>
#include <string>
#include <vector>
#include <thread>
#include <mutex>
#include "WebView2.h"
#include "version.h"

using namespace Microsoft::WRL;

#define WM_ENGINE_LINE (WM_APP + 1)
#define WM_TRAY (WM_APP + 2)
#define ID_TRAY_OPEN 1001
#define ID_TRAY_PAUSE 1002
#define ID_TRAY_QUIT 1003
#define ID_QUIT_TIMER 7
// MF-11 (7 October 2026): an engine that stops without a quit is started again (10 s, then 60 s after three in ten
// minutes); a second "Ember.exe" that finds this window asks it to do so now (WM_ENGINE_RESTART), instead of
// showing a window that says "the engine stopped" with nothing mining behind it.
#define ID_RESTART_TIMER 8
#define ID_SHOT_TIMER 9
#define ID_SHOT_DONE_TIMER 10
#define WM_ENGINE_RESTART (WM_APP + 3)
#define RESTART_SOON_MS 10000
#define RESTART_SLOW_MS 60000
#define RESTART_WINDOW_MS 600000
#define IDI_APP 1

static HWND g_hwnd = nullptr;
static HANDLE g_engine = nullptr, g_engineIn = nullptr, g_engineOut = nullptr;
static ComPtr<ICoreWebView2Controller> g_controller;
static ComPtr<ICoreWebView2> g_webview;
// a PNG of the web view at `path` (the SHOT line); the stream is released by the completion handler
static void capturePreview(const std::wstring& path) {
    if (!g_webview) return;
    ComPtr<IStream> stream;
    if (FAILED(SHCreateStreamOnFileEx(path.c_str(), STGM_CREATE | STGM_WRITE | STGM_SHARE_EXCLUSIVE, FILE_ATTRIBUTE_NORMAL, TRUE, nullptr, &stream))) return;
    g_webview->CapturePreview(COREWEBVIEW2_CAPTURE_PREVIEW_IMAGE_FORMAT_PNG, stream.Get(), Callback<ICoreWebView2CapturePreviewCompletedHandler>([stream](HRESULT) -> HRESULT { return S_OK; }).Get());
}
static std::wstring g_url, g_status = L"starting the engine";
static bool g_paused = false, g_quitting = false, g_exited = false, g_webviewOk = false, g_hintShown = false;
static NOTIFYICONDATAW g_nid = {};
static std::wstring g_trayTitle = L"Ember";
static ULONGLONG g_quitStarted = 0;
static bool g_exitForUpdate = false;       // the engine's last line was "EXIT update": an installer or the OTA stops it; this window goes too, no restart
static int g_restarts = 0;                 // engine restarts inside the current window
static ULONGLONG g_restartWindowStart = 0; // when that window opened

static std::wstring widen(const std::string& s) {
    if (s.empty()) return L"";
    int n = MultiByteToWideChar(CP_UTF8, 0, s.data(), (int)s.size(), nullptr, 0);
    std::wstring w(n, 0);
    MultiByteToWideChar(CP_UTF8, 0, s.data(), (int)s.size(), &w[0], n);
    return w;
}

static std::wstring exeDir() {
    wchar_t buf[MAX_PATH];
    GetModuleFileNameW(nullptr, buf, MAX_PATH);
    std::wstring p(buf);
    size_t i = p.find_last_of(L"\\/");
    return i == std::wstring::npos ? L"." : p.substr(0, i);
}

static void sendEngine(const char* line) {
    if (!g_engineIn) return;
    DWORD w = 0;
    WriteFile(g_engineIn, line, (DWORD)strlen(line), &w, nullptr);
    WriteFile(g_engineIn, "\n", 1, &w, nullptr);
}

static void setTray(const std::wstring& tip) {
    g_trayTitle = tip;
    wcsncpy_s(g_nid.szTip, tip.c_str(), _TRUNCATE);
    Shell_NotifyIconW(NIM_MODIFY, &g_nid);
}

static void repaintStatus() {
    InvalidateRect(g_hwnd, nullptr, TRUE);
}

// A tiny JSON number/string/bool reader for the STATE line (flat object, known keys).
static std::string jsonField(const std::string& j, const char* key) {
    std::string k = std::string("\"") + key + "\":";
    size_t i = j.find(k);
    if (i == std::string::npos) return "";
    i += k.size();
    while (i < j.size() && j[i] == ' ') i++;
    if (i < j.size() && j[i] == '"') {
        size_t e = j.find('"', i + 1);
        return e == std::string::npos ? "" : j.substr(i + 1, e - i - 1);
    }
    size_t e = i;
    while (e < j.size() && j[e] != ',' && j[e] != '}') e++;
    return j.substr(i, e - i);
}

static void applyState(const std::string& j) {
    std::string mining = jsonField(j, "mining"), node = jsonField(j, "node"), phase = jsonField(j, "phase");
    g_paused = jsonField(j, "paused") == "true";
    double hash = atof(jsonField(j, "hash_total").c_str());
    std::string blocks = jsonField(j, "blocks"), accepted = jsonField(j, "accepted_total");
    wchar_t tip[128];
    if (jsonField(j, "quitting") == "true") swprintf_s(tip, L"Ember: stopping");
    else if (phase != "dashboard") swprintf_s(tip, L"Ember: set up");
    else if (mining == "mining") swprintf_s(tip, L"Ember: %.1f MH/s, %S blocks found, node %S", hash, accepted.c_str(), node.c_str());
    else if (mining == "paused") swprintf_s(tip, L"Ember: paused, node %S", node.c_str());
    else swprintf_s(tip, L"Ember: %S, node %S (%S blocks)", mining.c_str(), node.c_str(), blocks.c_str());
    setTray(tip);
}

// The engine asks for an elevated step (the NVIDIA power cap): this process has a UI context, so the UAC prompt shows.
// Runs cmd /c <line> as administrator, waits, and answers on the engine's stdin.
static void runElevated(std::wstring line) {
    std::thread([line] {
        // cmd strips the first and last quote of a line that starts with one and holds more than two (two cards, or a
        // cap plus a script): one outer pair around the whole line is what it may strip (the first Windows machine, 6 October 2026)
        std::wstring params = L"/c \"" + line + L"\"";
        wchar_t sysdir[MAX_PATH];
        GetSystemDirectoryW(sysdir, MAX_PATH);
        std::wstring cmdExe = std::wstring(sysdir) + L"\\cmd.exe";   // the absolute path, never a bare name (R4.3.3)
        SHELLEXECUTEINFOW sei = { sizeof(sei) };
        sei.fMask = SEE_MASK_NOCLOSEPROCESS | SEE_MASK_FLAG_NO_UI;
        sei.lpVerb = L"runas";
        sei.lpFile = cmdExe.c_str();
        sei.lpParameters = params.c_str();
        sei.nShow = SW_HIDE;
        if (!ShellExecuteExW(&sei) || !sei.hProcess) {
            DWORD err = GetLastError();
            sendEngine(err == ERROR_CANCELLED ? "elevated fail: the administrator prompt was cancelled" : "elevated fail: could not start the elevated step");
            return;
        }
        WaitForSingleObject(sei.hProcess, 120000);
        DWORD code = 1;
        GetExitCodeProcess(sei.hProcess, &code);
        CloseHandle(sei.hProcess);
        if (code == 0) sendEngine("elevated ok");
        else { char buf[64]; sprintf_s(buf, "elevated fail: exit code %lu", code); sendEngine(buf); }
    }).detach();
}

static void openInBrowser(const std::wstring& url) {
    ShellExecuteW(nullptr, L"open", url.c_str(), nullptr, nullptr, SW_SHOWNORMAL);
}

static void navigate() {
    if (g_webview && !g_url.empty()) g_webview->Navigate((g_url + (g_url.find(L'?') == std::wstring::npos ? L"?host=windows" : L"&host=windows")).c_str());
}

// The audit mode (main's standing rule of 17:4x UK, 12 October 2026: every cut's pointer waits on real screen captures of
// the running window): "Ember.exe --url <page url> --size WxH" opens one more window of the given logical size on the
// running engine's own page (the url carries its token and ?page=&theme=), starts no engine, takes no tray item and
// skips the single-instance hand-over, so a capture job can photograph it beside the live window and close it.
static std::wstring g_auditUrl;
static int g_auditW = 0, g_auditH = 0;
// "--shot <png path> [--settle <ms>]": the audit window writes its own view as a PNG through WebView2's CapturePreview
// after the settle (default 6000 ms from the window's creation) and closes; no screen is read, so a machine whose
// display is asleep or absent (the three-card test machine at 00:0x UK, 13 October 2026: every screen capture black)
// still gives a real capture of the running page
static std::wstring g_shotPath;
static int g_settleMs = 6000;
static void readAuditFlags() {
    int argc = 0;
    LPWSTR* argv = CommandLineToArgvW(GetCommandLineW(), &argc);
    if (!argv) return;
    for (int i = 1; i < argc; i++) {
        std::wstring a = argv[i];
        if (a == L"--url" && i + 1 < argc) g_auditUrl = argv[++i];
        else if (a == L"--size" && i + 1 < argc) { swscanf_s(argv[++i], L"%dx%d", &g_auditW, &g_auditH); }
        else if (a == L"--shot" && i + 1 < argc) g_shotPath = argv[++i];
        else if (a == L"--settle" && i + 1 < argc) g_settleMs = _wtoi(argv[++i]);
    }
    LocalFree(argv);
}

static void initWebView() {
    std::wstring data = L"";
    wchar_t* local = nullptr;
    size_t len = 0;
    if (_wdupenv_s(&local, &len, L"LOCALAPPDATA") == 0 && local) { data = std::wstring(local) + L"\\igneum\\ember\\webview2"; free(local); }
    HRESULT hr = CreateCoreWebView2EnvironmentWithOptions(nullptr, data.empty() ? nullptr : data.c_str(), nullptr,
        Callback<ICoreWebView2CreateCoreWebView2EnvironmentCompletedHandler>([](HRESULT result, ICoreWebView2Environment* env) -> HRESULT {
            if (FAILED(result) || !env) {
                g_status = L"The WebView2 runtime is not installed. The dashboard opens in your browser; install the runtime from microsoft.com for the app window.";
                repaintStatus();
                if (!g_url.empty()) { openInBrowser(g_url); g_hintShown = true; }
                return S_OK;
            }
            env->CreateCoreWebView2Controller(g_hwnd, Callback<ICoreWebView2CreateCoreWebView2ControllerCompletedHandler>([](HRESULT result, ICoreWebView2Controller* controller) -> HRESULT {
                if (FAILED(result) || !controller) {
                    g_status = L"The app window could not start WebView2. The dashboard opens in your browser.";
                    repaintStatus();
                    if (!g_url.empty()) { openInBrowser(g_url); g_hintShown = true; }
                    return S_OK;
                }
                g_controller = controller;
                g_controller->get_CoreWebView2(&g_webview);
                g_webviewOk = true;
                ComPtr<ICoreWebView2Settings> settings;
                if (g_webview && SUCCEEDED(g_webview->get_Settings(&settings)) && settings) {
                    settings->put_AreDefaultContextMenusEnabled(FALSE);
                    settings->put_IsStatusBarEnabled(FALSE);
                    settings->put_AreDevToolsEnabled(FALSE);
                }
                // links off 127.0.0.1 open in the default browser
                g_webview->add_NewWindowRequested(Callback<ICoreWebView2NewWindowRequestedEventHandler>([](ICoreWebView2*, ICoreWebView2NewWindowRequestedEventArgs* args) -> HRESULT {
                    LPWSTR uri = nullptr;
                    if (SUCCEEDED(args->get_Uri(&uri)) && uri) { openInBrowser(uri); CoTaskMemFree(uri); }
                    args->put_Handled(TRUE);
                    return S_OK;
                }).Get(), nullptr);
                g_webview->add_NavigationStarting(Callback<ICoreWebView2NavigationStartingEventHandler>([](ICoreWebView2*, ICoreWebView2NavigationStartingEventArgs* args) -> HRESULT {
                    LPWSTR uri = nullptr;
                    if (SUCCEEDED(args->get_Uri(&uri)) && uri) {
                        std::wstring u(uri);
                        CoTaskMemFree(uri);
                        if (u.rfind(L"http", 0) == 0 && u.find(L"127.0.0.1") == std::wstring::npos) { args->put_Cancel(TRUE); openInBrowser(u); }
                    }
                    return S_OK;
                }).Get(), nullptr);
                RECT rc; GetClientRect(g_hwnd, &rc);
                g_controller->put_Bounds(rc);
                COREWEBVIEW2_COLOR dark = { 255, 12, 12, 14 };
                ComPtr<ICoreWebView2Controller2> c2;
                if (SUCCEEDED(g_controller.As(&c2)) && c2) c2->put_DefaultBackgroundColor(dark);
                g_status = L"";
                repaintStatus();
                navigate();
                return S_OK;
            }).Get());
            return S_OK;
        }).Get());
    if (FAILED(hr)) {
        g_status = L"The WebView2 runtime is not installed. The dashboard opens in your browser; install the runtime from microsoft.com for the app window.";
        repaintStatus();
        if (!g_url.empty()) { openInBrowser(g_url); g_hintShown = true; }
    }
}

static bool startEngine() {
    SECURITY_ATTRIBUTES sa = { sizeof(sa), nullptr, TRUE };
    HANDLE outR, outW, inR, inW;
    if (!CreatePipe(&outR, &outW, &sa, 0) || !CreatePipe(&inR, &inW, &sa, 0)) return false;
    SetHandleInformation(outR, HANDLE_FLAG_INHERIT, 0);
    SetHandleInformation(inW, HANDLE_FLAG_INHERIT, 0);
    STARTUPINFOW si = { sizeof(si) };
    si.dwFlags = STARTF_USESTDHANDLES;
    si.hStdOutput = outW;
    si.hStdError = GetStdHandle(STD_ERROR_HANDLE);
    si.hStdInput = inR;
    PROCESS_INFORMATION pi = {};
    std::wstring exe = exeDir() + L"\\igneum-app.exe";
    std::wstring cmd = L"\"" + exe + L"\" --wrapper";
    std::vector<wchar_t> buf(cmd.begin(), cmd.end());
    buf.push_back(0);
    BOOL ok = CreateProcessW(exe.c_str(), buf.data(), nullptr, nullptr, TRUE, CREATE_NO_WINDOW, nullptr, exeDir().c_str(), &si, &pi);
    CloseHandle(outW);
    CloseHandle(inR);
    if (!ok) { CloseHandle(outR); CloseHandle(inW); return false; }
    CloseHandle(pi.hThread);
    g_engine = pi.hProcess;
    g_engineIn = inW;
    g_engineOut = outR;
    std::thread([] {
        std::string acc;
        char chunk[4096];
        DWORD n;
        while (ReadFile(g_engineOut, chunk, sizeof(chunk), &n, nullptr) && n > 0) {
            acc.append(chunk, n);
            size_t nl;
            while ((nl = acc.find('\n')) != std::string::npos) {
                std::string line = acc.substr(0, nl);
                acc.erase(0, nl + 1);
                if (!line.empty() && line.back() == '\r') line.pop_back();
                PostMessageW(g_hwnd, WM_ENGINE_LINE, 0, (LPARAM) new std::string(line));
            }
        }
        PostMessageW(g_hwnd, WM_ENGINE_LINE, 1, 0);
    }).detach();
    return true;
}

// The engine's handles, closed before another engine is started (the reader thread has already left: it posts the
// "gone" line only after the pipe closed).
static void closeEngine() {
    if (g_engineIn) { CloseHandle(g_engineIn); g_engineIn = nullptr; }
    if (g_engineOut) { CloseHandle(g_engineOut); g_engineOut = nullptr; }
    if (g_engine) { CloseHandle(g_engine); g_engine = nullptr; }
}

// Is an installer running? Its marker (%LOCALAPPDATA%\igneum\app\install-running.flag, written by Ember.iss's
// PrepareToInstall and removed at its end) holds this window's restart ladder: the old engine must never start again
// under an installer (the second Windows machine, 7 October 2026, 20:54 UK). A marker older than 15 minutes is an installer that died.
static bool installerRunning() {
    wchar_t* local = nullptr;
    size_t len = 0;
    if (_wdupenv_s(&local, &len, L"LOCALAPPDATA") != 0 || !local) return false;
    std::wstring p = std::wstring(local) + L"\\igneum\\app\\install-running.flag";
    free(local);
    WIN32_FILE_ATTRIBUTE_DATA fad;
    if (!GetFileAttributesExW(p.c_str(), GetFileExInfoStandard, &fad)) return false;
    FILETIME now;
    GetSystemTimeAsFileTime(&now);
    ULARGE_INTEGER a, b;
    a.LowPart = fad.ftLastWriteTime.dwLowDateTime; a.HighPart = fad.ftLastWriteTime.dwHighDateTime;
    b.LowPart = now.dwLowDateTime; b.HighPart = now.dwHighDateTime;
    ULONGLONG ageS = b.QuadPart > a.QuadPart ? (b.QuadPart - a.QuadPart) / 10000000ULL : 0;
    return ageS <= 15 * 60;
}

// Starts the engine again after it stopped on its own. Three restarts inside ten minutes come 10 s apart; from the
// fourth they come a minute apart, for ever: a miner that sits stopped is a miner lost (plug, tune, play).
static void scheduleRestart() {
    ULONGLONG now = GetTickCount64();
    if (g_restartWindowStart == 0 || now - g_restartWindowStart > RESTART_WINDOW_MS) { g_restartWindowStart = now; g_restarts = 0; }
    g_restarts++;
    UINT delay = g_restarts <= 3 ? RESTART_SOON_MS : RESTART_SLOW_MS;
    wchar_t buf[160];
    swprintf_s(buf, L"The engine stopped. Starting it again in %u s (restart %d).", delay / 1000, g_restarts);
    g_status = buf;
    setTray(L"Ember: starting the engine again");
    repaintStatus();
    SetTimer(g_hwnd, ID_RESTART_TIMER, delay, nullptr);
}

static void restartEngineNow() {
    KillTimer(g_hwnd, ID_RESTART_TIMER);
    if (g_quitting || !g_exited) return;
    if (installerRunning()) {
        // held: an installer is replacing the files; this window ends so the installer can replace it too, and the
        // installer's own [Run] step (or the update helper) starts the new app
        g_status = L"An update is installing; the app opens again when it is done.";
        repaintStatus();
        DestroyWindow(g_hwnd);
        return;
    }
    closeEngine();
    g_exited = false;
    g_url.clear();
    if (startEngine()) {
        g_status = L"";
        setTray(L"Ember: starting");
        repaintStatus();
    } else {
        g_exited = true;
        g_status = L"igneum-app.exe is missing next to this program. Run the installer again.";
        repaintStatus();
        scheduleRestart();
    }
}

static void showTrayMenu() {
    HMENU m = CreatePopupMenu();
    AppendMenuW(m, MF_STRING, ID_TRAY_OPEN, L"Open Ember");
    AppendMenuW(m, MF_STRING, ID_TRAY_PAUSE, g_paused ? L"Resume mining" : L"Pause mining");
    AppendMenuW(m, MF_SEPARATOR, 0, nullptr);
    AppendMenuW(m, MF_STRING | MF_GRAYED, 0, g_trayTitle.c_str());
    AppendMenuW(m, MF_SEPARATOR, 0, nullptr);
    AppendMenuW(m, MF_STRING, ID_TRAY_QUIT, L"Quit Ember");
    POINT pt; GetCursorPos(&pt);
    SetForegroundWindow(g_hwnd);
    TrackPopupMenu(m, TPM_RIGHTBUTTON | TPM_BOTTOMALIGN, pt.x, pt.y, 0, g_hwnd, nullptr);
    DestroyMenu(m);
}

static void beginQuit() {
    if (g_quitting) return;
    g_quitting = true;
    g_quitStarted = GetTickCount64();
    g_status = L"Stopping: the miner first, then the node";
    setTray(L"Ember: stopping");
    if (g_webview) g_webview->ExecuteScript(L"document.getElementById('pill-text').textContent='stopping'", nullptr);
    if (g_engine && !g_exited) {
        sendEngine("quit");
        SetTimer(g_hwnd, ID_QUIT_TIMER, 500, nullptr);
    } else {
        DestroyWindow(g_hwnd);
    }
}

// scaling-21 (7 October 2026, the founder: "the miner needs some scaling so more is visible in the initial window"): the window
// opens at about 80 percent of the work area, capped at 1440 by 900 on a display up to 1920 wide and scaled up with the
// display beyond that; once the user resizes, the size is remembered per machine under HKCU\Software\Igneum\Miner
// (WindowW, WindowH) and used on the next start while it still fits the work area. The Mac window does the same
// through its frame autosave name (app/mac/Ember.swift).
static const wchar_t* kSizeKey = L"Software\\Igneum\\Miner";
// dpi-23 (7 October 2026): the host is per-monitor DPI aware (host.manifest, and the runtime call below for a Windows that
// ignores the manifest), so every rectangle it reads is PHYSICAL pixels; the window's monitor DPI (96 = 100 percent) turns
// them into the logical pixels the opening-size rule and the remembered frame use. GetDpiForMonitor is looked up at run
// time (Shcore, Windows 8.1 and later) so the build links nothing new; the fallback is the desktop's LOGPIXELSX.
typedef HRESULT (WINAPI *GetDpiForMonitorFn)(HMONITOR, int, UINT*, UINT*);
static int monitorDpi(HMONITOR mon) {
    HMODULE sh = LoadLibraryW(L"Shcore.dll");
    if (sh) { GetDpiForMonitorFn f = (GetDpiForMonitorFn)GetProcAddress(sh, "GetDpiForMonitor"); UINT x = 96, y = 96; if (f && mon && SUCCEEDED(f(mon, 0 /* MDT_EFFECTIVE_DPI */, &x, &y)) && x > 0) { FreeLibrary(sh); return (int)x; } FreeLibrary(sh); }
    HDC dc = GetDC(nullptr); int dpi = dc ? GetDeviceCaps(dc, LOGPIXELSX) : 96; if (dc) ReleaseDC(nullptr, dc); return dpi > 0 ? dpi : 96;
}
static void enableDpiAwareness() {
    // the manifest asks for PerMonitorV2; this is the runtime path for a Windows build that ignores it (older than 1703)
    HMODULE u = GetModuleHandleW(L"user32.dll"); if (!u) return;
    typedef BOOL (WINAPI *SetCtxFn)(HANDLE);
    SetCtxFn setCtx = (SetCtxFn)GetProcAddress(u, "SetProcessDpiAwarenessContext");
    if (setCtx && setCtx((HANDLE)-4 /* DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 */)) return;
    typedef BOOL (WINAPI *SetAwareFn)(void);
    SetAwareFn setAware = (SetAwareFn)GetProcAddress(u, "SetProcessDPIAware"); if (setAware) setAware();
}
static void saveWindowSize(HWND hwnd) {
    if (IsIconic(hwnd) || IsZoomed(hwnd)) return;
    RECT r; if (!GetWindowRect(hwnd, &r)) return;
    int dpi = monitorDpi(MonitorFromWindow(hwnd, MONITOR_DEFAULTTONEAREST));
    DWORD w = (DWORD)igneum_scale_to_logical(r.right - r.left, dpi), h = (DWORD)igneum_scale_to_logical(r.bottom - r.top, dpi), units = 1;
    if (w < 900 || h < 600 || w > (DWORD)(h * 1.6)) return;
    HKEY k; if (RegCreateKeyExW(HKEY_CURRENT_USER, kSizeKey, 0, nullptr, 0, KEY_SET_VALUE, nullptr, &k, nullptr) != ERROR_SUCCESS) return;
    RegSetValueExW(k, L"WindowW", 0, REG_DWORD, (const BYTE*)&w, sizeof(w));
    RegSetValueExW(k, L"WindowH", 0, REG_DWORD, (const BYTE*)&h, sizeof(h));
    RegSetValueExW(k, L"WindowUnits", 0, REG_DWORD, (const BYTE*)&units, sizeof(units)); // 1 = logical pixels, written by a DPI-aware host
    RegCloseKey(k);
}
static void openingSize(int& w, int& h, int& sx, int& sy) {
    // the PRIMARY monitor's work area, never the virtual desktop (window-22: a two-monitor span opened the app super wide)
    RECT work = { 0, 0, GetSystemMetrics(SM_CXSCREEN), GetSystemMetrics(SM_CYSCREEN) };
    POINT origin = { 0, 0 }; HMONITOR mon = MonitorFromPoint(origin, MONITOR_DEFAULTTOPRIMARY);
    MONITORINFO mi = { sizeof(mi) }; if (mon && GetMonitorInfoW(mon, &mi)) work = mi.rcWork; else SystemParametersInfoW(SPI_GETWORKAREA, 0, &work, 0);
    int ww = work.right - work.left, wh = work.bottom - work.top;
    HKEY k; DWORD sw = 0, sh = 0, n = sizeof(DWORD);
    if (RegOpenKeyExW(HKEY_CURRENT_USER, kSizeKey, 0, KEY_QUERY_VALUE, &k) == ERROR_SUCCESS) {
        RegQueryValueExW(k, L"WindowW", nullptr, nullptr, (LPBYTE)&sw, &n); n = sizeof(DWORD);
        RegQueryValueExW(k, L"WindowH", nullptr, nullptr, (LPBYTE)&sh, &n);
        RegCloseKey(k);
    }
    // physical work area and DPI in, physical window out; the rule itself runs in logical pixels (opening_size.h)
    igneum_opening_size_scaled(ww, wh, monitorDpi(mon), (int)sw, (int)sh, &w, &h, nullptr, nullptr);
    sx = work.left + (ww - w) / 2; sy = work.top + (wh - h) / 2;
}

static LRESULT CALLBACK WndProc(HWND hwnd, UINT msg, WPARAM wp, LPARAM lp) {
    switch (msg) {
    case WM_SIZE:
        if (g_controller) { RECT rc; GetClientRect(hwnd, &rc); g_controller->put_Bounds(rc); }
        return 0;
    case WM_EXITSIZEMOVE:
        saveWindowSize(hwnd);
        return 0;
    case WM_DPICHANGED: { // the window moved to a monitor of another scale: take the rectangle Windows suggests, WebView2 re-renders at the new DPI
        const RECT* r = (const RECT*)lp;
        if (r) SetWindowPos(hwnd, nullptr, r->left, r->top, r->right - r->left, r->bottom - r->top, SWP_NOZORDER | SWP_NOACTIVATE);
        return 0;
    }
    case WM_GETMINMAXINFO: // the dashboard lays out from 900 x 600 up (app/igneum-app/ui); the Mac window says the same
        ((MINMAXINFO*)lp)->ptMinTrackSize.x = 900; ((MINMAXINFO*)lp)->ptMinTrackSize.y = 600;
        return 0;
    case WM_ENGINE_LINE: {
        if (wp == 1) {
            g_exited = true;
            if (g_quitting) { DestroyWindow(hwnd); return 0; }
            // an installer or the app's own OTA stopped the engine ("EXIT update"): the window ends too, so the installer can
            // replace this exe, and the old engine is never started again under it (the second Windows machine, 7 October 2026, 20:54 UK)
            if (g_exitForUpdate) { DestroyWindow(hwnd); return 0; }
            // not a quit of ours: the engine died, or a local caller (a job) asked it to stop and nothing will start it
            // again; this window does (MF-11)
            scheduleRestart();
            return 0;
        }
        std::string* line = (std::string*)lp;
        if (line->rfind("URL ", 0) == 0) {
            g_url = widen(line->substr(4));
            if (g_webviewOk) navigate();
            else if (!g_webview && g_status.find(L"WebView2") != std::wstring::npos && !g_hintShown) { openInBrowser(g_url); g_hintShown = true; }
        } else if (line->rfind("STATE ", 0) == 0) {
            applyState(line->substr(6));
        } else if (line->rfind("ELEVATE ", 0) == 0) {
            runElevated(widen(line->substr(8)));
        } else if (line->rfind("SHOT ", 0) == 0) {
            // Miner UI 4 (b): the engine's POST /api/shot asks for a PNG of what the window shows, at this path
            capturePreview(widen(line->substr(5)));
        } else if (line->rfind("FATAL ", 0) == 0) {
            g_status = widen(line->substr(6));
            repaintStatus();
            MessageBoxW(hwnd, g_status.c_str(), L"Ember", MB_OK | MB_ICONERROR);
        } else if (line->rfind("EXIT", 0) == 0) {
            g_exited = true;
            if (line->find("update") != std::string::npos) g_exitForUpdate = true;
            if (g_quitting || g_exitForUpdate) DestroyWindow(hwnd);
        }
        delete line;
        return 0;
    }
    case WM_ENGINE_RESTART:
        // a second "Ember.exe" (the update helper, the Start Menu, the relay agent's start-app) found this window:
        // an engine that is gone starts now, not in its backoff
        if (g_exited && !g_quitting) restartEngineNow();
        return 0;
    case WM_QUERYENDSESSION:
        // the Restart Manager (an installer's /CLOSEAPPLICATIONS) or a sign-out: this window closes for real, it does not
        // hide to the tray (WM_CLOSE below is the user's X)
        beginQuit();
        return TRUE;
    case WM_ENDSESSION:
        if (wp) { if (g_engine && !g_exited) sendEngine("quit"); }
        return 0;
    case WM_TIMER:
        if (wp == ID_RESTART_TIMER) { restartEngineNow(); return 0; }
        if (wp == ID_SHOT_TIMER) { KillTimer(hwnd, ID_SHOT_TIMER); capturePreview(g_shotPath); SetTimer(hwnd, ID_SHOT_DONE_TIMER, 2500, nullptr); return 0; }
        if (wp == ID_SHOT_DONE_TIMER) { KillTimer(hwnd, ID_SHOT_DONE_TIMER); DestroyWindow(hwnd); return 0; }
        if (wp == ID_QUIT_TIMER) {
            DWORD code = 0;
            bool gone = !g_engine || (GetExitCodeProcess(g_engine, &code) && code != STILL_ACTIVE);
            if (gone || g_exited || GetTickCount64() - g_quitStarted > 45000) {
                if (!gone && g_engine) TerminateProcess(g_engine, 1);
                KillTimer(hwnd, ID_QUIT_TIMER);
                DestroyWindow(hwnd);
            }
        }
        return 0;
    case WM_DEVICECHANGE:
        // a device arrived or left (an eGPU through a USB4 box, a driver coming up or crashing): the engine enumerates
        // the cards now instead of at its next minute poll (src/hotplug.rs); DBT_DEVNODES_CHANGED needs no registration
        if (wp == DBT_DEVNODES_CHANGED || wp == DBT_DEVICEARRIVAL || wp == DBT_DEVICEREMOVECOMPLETE) sendEngine("detect");
        return TRUE;
    case WM_TRAY:
        if (lp == WM_LBUTTONUP || lp == WM_LBUTTONDBLCLK) { ShowWindow(hwnd, SW_SHOW); SetForegroundWindow(hwnd); }
        else if (lp == WM_RBUTTONUP || lp == WM_CONTEXTMENU) showTrayMenu();
        return 0;
    case WM_COMMAND:
        switch (LOWORD(wp)) {
        case ID_TRAY_OPEN: ShowWindow(hwnd, SW_SHOW); SetForegroundWindow(hwnd); return 0;
        case ID_TRAY_PAUSE: sendEngine(g_paused ? "resume" : "pause"); return 0;
        case ID_TRAY_QUIT: beginQuit(); return 0;
        }
        return 0;
    case WM_CLOSE:
        // hide to the tray; the miner keeps running
        ShowWindow(hwnd, SW_HIDE);
        if (!g_hintShown) {
            g_nid.uFlags |= NIF_INFO;
            wcscpy_s(g_nid.szInfoTitle, L"Ember keeps mining");
            wcscpy_s(g_nid.szInfo, L"The window is in the tray. Right-click the icon to pause or quit.");
            g_nid.dwInfoFlags = NIIF_INFO;
            Shell_NotifyIconW(NIM_MODIFY, &g_nid);
            g_nid.uFlags &= ~NIF_INFO;
            g_hintShown = true;
        }
        return 0;
    case WM_PAINT: {
        PAINTSTRUCT ps;
        HDC dc = BeginPaint(hwnd, &ps);
        RECT rc; GetClientRect(hwnd, &rc);
        HBRUSH bg = CreateSolidBrush(RGB(12, 12, 14));
        FillRect(dc, &rc, bg);
        DeleteObject(bg);
        if (!g_status.empty()) {
            SetBkMode(dc, TRANSPARENT);
            SetTextColor(dc, RGB(154, 154, 158));
            HFONT f = CreateFontW(-15, 0, 0, 0, FW_NORMAL, 0, 0, 0, DEFAULT_CHARSET, 0, 0, CLEARTYPE_QUALITY, 0, L"Segoe UI");
            HFONT old = (HFONT)SelectObject(dc, f);
            RECT tr = rc; tr.left += 40; tr.right -= 40;
            DrawTextW(dc, g_status.c_str(), -1, &tr, DT_CENTER | DT_VCENTER | DT_WORDBREAK | DT_NOPREFIX | (g_webviewOk ? DT_BOTTOM : DT_VCENTER));
            SelectObject(dc, old);
            DeleteObject(f);
        }
        EndPaint(hwnd, &ps);
        return 0;
    }
    case WM_DESTROY:
        Shell_NotifyIconW(NIM_DELETE, &g_nid);
        PostQuitMessage(0);
        return 0;
    }
    return DefWindowProcW(hwnd, msg, wp, lp);
}

// --version and --help print one line and exit, for the CI smoke run and support scripts. A windows-subsystem exe has
// no console of its own: the text goes to the inherited stdout when there is one (a pipe or a file), else to the
// parent's console.
static bool handleCliFlags() {
    int argc = 0;
    LPWSTR* argv = CommandLineToArgvW(GetCommandLineW(), &argc);
    if (!argv) return false;
    std::wstring text;
    for (int i = 1; i < argc; i++) {
        std::wstring a = argv[i];
        if (a == L"--version" || a == L"-V") text = L"Ember " IGNEUM_HOST_VERSION_STR L" (window host)\r\n";
        else if (a == L"--help" || a == L"-h" || a == L"/?")
            text = L"Ember " IGNEUM_HOST_VERSION_STR L" (window host)\r\n"
                   L"Usage: \"Ember.exe\" [--version | --help]\r\n"
                   L"Without flags it starts igneum-app.exe --wrapper next to it and shows the dashboard in a WebView2 window.\r\n";
    }
    LocalFree(argv);
    if (text.empty()) return false;
    HANDLE out = GetStdHandle(STD_OUTPUT_HANDLE);
    if (out == nullptr || out == INVALID_HANDLE_VALUE) {
        if (AttachConsole(ATTACH_PARENT_PROCESS)) out = GetStdHandle(STD_OUTPUT_HANDLE);
    }
    if (out && out != INVALID_HANDLE_VALUE) {
        int n = WideCharToMultiByte(CP_UTF8, 0, text.c_str(), (int)text.size(), nullptr, 0, nullptr, nullptr);
        std::string utf8(n, 0);
        WideCharToMultiByte(CP_UTF8, 0, text.c_str(), (int)text.size(), &utf8[0], n, nullptr, nullptr);
        DWORD written = 0;
        WriteFile(out, utf8.data(), (DWORD)utf8.size(), &written, nullptr);
    }
    return true;
}

int WINAPI wWinMain(HINSTANCE hInst, HINSTANCE, PWSTR, int) {
    if (handleCliFlags()) return 0;
    enableDpiAwareness();
    readAuditFlags();
    const bool audit = !g_auditUrl.empty() && g_auditW >= 300 && g_auditH >= 200;
    HANDLE once = audit ? nullptr : CreateMutexW(nullptr, TRUE, L"Local\\EmberWindow");
    if (!audit && GetLastError() == ERROR_ALREADY_EXISTS) {
        HWND other = FindWindowW(L"EmberWindow", nullptr);
        if (other) { PostMessageW(other, WM_ENGINE_RESTART, 0, 0); ShowWindow(other, SW_SHOW); SetForegroundWindow(other); }
        return 0;
    }
    CoInitializeEx(nullptr, COINIT_APARTMENTTHREADED);
    WNDCLASSW wc = {};
    wc.lpfnWndProc = WndProc;
    wc.hInstance = hInst;
    wc.lpszClassName = audit ? L"EmberAuditWindow" : L"EmberWindow";
    wc.hIcon = LoadIconW(hInst, MAKEINTRESOURCEW(IDI_APP));
    wc.hCursor = LoadCursorW(nullptr, IDC_ARROW);
    wc.hbrBackground = CreateSolidBrush(RGB(12, 12, 14));
    RegisterClassW(&wc);
    int w, h, sx, sy; openingSize(w, h, sx, sy);
    if (audit) {
        // the given logical size as the window's outer size at the primary monitor's scale, placed at the work area's corner
        UINT dpi = GetDpiForSystem();
        w = MulDiv(g_auditW, (int)dpi, 96); h = MulDiv(g_auditH, (int)dpi, 96); sx = 40; sy = 40;
    }
    g_hwnd = CreateWindowExW(0, wc.lpszClassName, L"Ember", WS_OVERLAPPEDWINDOW, sx, sy, w, h, nullptr, nullptr, hInst, nullptr);
    ShowWindow(g_hwnd, SW_SHOW);
    if (audit) {
        g_url = g_auditUrl;
        g_status = L"";
        initWebView();
        if (!g_shotPath.empty()) SetTimer(g_hwnd, ID_SHOT_TIMER, (UINT)(g_settleMs < 1000 ? 1000 : g_settleMs), nullptr);
        MSG msg;
        while (GetMessageW(&msg, nullptr, 0, 0)) { TranslateMessage(&msg); DispatchMessageW(&msg); }
        CoUninitialize();
        return 0;
    }

    g_nid.cbSize = sizeof(g_nid);
    g_nid.hWnd = g_hwnd;
    g_nid.uID = 1;
    g_nid.uFlags = NIF_ICON | NIF_MESSAGE | NIF_TIP;
    g_nid.uCallbackMessage = WM_TRAY;
    g_nid.hIcon = (HICON)LoadImageW(hInst, MAKEINTRESOURCEW(IDI_APP), IMAGE_ICON, 16, 16, LR_DEFAULTCOLOR);
    wcscpy_s(g_nid.szTip, L"Ember: starting");
    Shell_NotifyIconW(NIM_ADD, &g_nid);

    if (!startEngine()) {
        g_status = L"igneum-app.exe is missing next to this program. Run the installer again.";
        repaintStatus();
        MessageBoxW(g_hwnd, g_status.c_str(), L"Ember", MB_OK | MB_ICONERROR);
    }
    initWebView();

    MSG msg;
    while (GetMessageW(&msg, nullptr, 0, 0)) {
        TranslateMessage(&msg);
        DispatchMessageW(&msg);
    }
    closeEngine();
    CloseHandle(once);
    CoUninitialize();
    return 0;
}
