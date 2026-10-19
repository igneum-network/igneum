// Igneum Wallet for Windows: the previous wallet's window on the restart's engine (OLD-BASE.md step 8, 11 October
// 2026). Built on a PC by BUILD-WALLET-APP.bat (MSVC, the WebView2 SDK from NuGet, static loader). It starts
// igneum-wallet-engine.exe next to it with the page (ui\) and the signer (igneum-signer.exe) and --exit-with-parent,
// reads "ENGINE {...}" and "HOST {...}" from its stdout, shows the URL in a WebView2 control, keeps a tray icon with
// the state (api/state every 10 s), and on quit closes the engine's stdin, which ends the engine and the signer.
// Closing the window hides it to the tray.
// Windows Hello (biometric.h, the page's window.chrome.webview.postMessage bridge, the HOST line): 5 October 2026.
//
// Untested on a real PC at the time of writing (written on a Mac): BUILD-WALLET-APP.bat is the first run.

#define WIN32_LEAN_AND_MEAN
#ifndef UNICODE
#define UNICODE
#endif
#ifndef _UNICODE
#define _UNICODE
#endif
#include <windows.h>
#include <shellapi.h>
#include <wrl.h>
#include <string>
#include <cwctype>
#include <vector>
#include <thread>
#include <mutex>
#include "WebView2.h"
#include "wallet-version.h"
#include "biometric.h"

using namespace Microsoft::WRL;

#define WM_ENGINE_LINE (WM_APP + 1)
#define WM_TRAY (WM_APP + 2)
#define WM_BIO_REPLY (WM_APP + 3)
#define ID_TRAY_OPEN 1001
#define ID_TRAY_PAUSE 1002
#define ID_TRAY_QUIT 1003
#define ID_QUIT_TIMER 7
#define IDI_APP 1

static HWND g_hwnd = nullptr;
static HANDLE g_engine = nullptr, g_engineIn = nullptr, g_engineOut = nullptr;
static ComPtr<ICoreWebView2Controller> g_controller;
static ComPtr<ICoreWebView2> g_webview;
static std::wstring g_url, g_status = L"starting the engine";
static std::string g_hostLine;   // the engine's HOST {...} line, kept until the window and the URL exist
static bool g_paused = false, g_quitting = false, g_exited = false, g_webviewOk = false, g_hintShown = false;
// the engine printed its UPDATE line: it exits for the installer, and this window ends with it so no file of ours is
// held (a Windows host's updater log, 15 October 2026: a window that stayed open made every silent install end with code 5)
static bool g_updating = false;
// the close notice's "do not show again": HKCU\Software\Igneum\Wallet\TrayHintSuppressed (1 once chosen)
static bool trayHintSuppressed() {
    HKEY k; DWORD v = 0, n = sizeof(v);
    if (RegOpenKeyExW(HKEY_CURRENT_USER, L"Software\\Igneum\\Wallet", 0, KEY_READ, &k) != ERROR_SUCCESS) return false;
    bool on = RegQueryValueExW(k, L"TrayHintSuppressed", nullptr, nullptr, (LPBYTE)&v, &n) == ERROR_SUCCESS && v == 1;
    RegCloseKey(k); return on;
}
static void setTrayHintSuppressed() {
    HKEY k; DWORD v = 1;
    if (RegCreateKeyExW(HKEY_CURRENT_USER, L"Software\\Igneum\\Wallet", 0, nullptr, 0, KEY_WRITE, nullptr, &k, nullptr) == ERROR_SUCCESS) {
        RegSetValueExW(k, L"TrayHintSuppressed", 0, REG_DWORD, (const BYTE*)&v, sizeof(v)); RegCloseKey(k);
    }
}
static NOTIFYICONDATAW g_nid = {};
static std::wstring g_trayTitle = L"Igneum Wallet";
static ULONGLONG g_quitStarted = 0;

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

// The tray's state from the restart's api/state (the old body read the miner's STATE line): locked or the balance,
// the node's state and block. The node's fields sit inside "node":{...}, found from that key so the first "block"
// elsewhere in the JSON is not taken for it.
static void applyState(const std::string& j) {
    std::string phase = jsonField(j, "phase"), balance = jsonField(j, "balance_ign");
    bool unlocked = jsonField(j, "unlocked") == "true";
    g_paused = !unlocked;
    size_t n = j.find("\"node\":{");
    std::string nodeState = n == std::string::npos ? "" : jsonField(j.substr(n), "state");
    std::string block = n == std::string::npos ? "" : jsonField(j.substr(n), "block");
    wchar_t tip[160];
    if (g_quitting) swprintf_s(tip, L"Igneum Wallet: stopping");
    else if (phase != "home") swprintf_s(tip, L"Igneum Wallet: set up");
    else if (!unlocked) swprintf_s(tip, L"Igneum Wallet: locked, node %S, block %S", nodeState == "ok" ? "synced" : nodeState.c_str(), block.c_str());
    else swprintf_s(tip, L"Igneum Wallet: %S IGN, node %S, block %S", balance.c_str(), nodeState == "ok" ? "synced" : nodeState.c_str(), block.c_str());
    setTray(tip);
}

// The state poll: every 10 s a thread reads api/state through the run's URL (the token is in the path; the
// biometric helper's request carries the host header, harmless on a read) and posts it to the window as a STATE
// line, the form the previous engine printed; one thread per run, ended with the window.
static void startStatePoll() {
    std::thread([] {
        while (!g_quitting && g_hwnd) {
            igbio::Answer a = igbio::get("api/state");
            if (a.status == 200 && !a.body.empty()) PostMessageW(g_hwnd, WM_ENGINE_LINE, 0, (LPARAM) new std::string("STATE " + a.body));
            for (int i = 0; i < 100 && !g_quitting; i++) Sleep(100);
        }
    }).detach();
}

static void openInBrowser(const std::wstring& url) {
    ShellExecuteW(nullptr, L"open", url.c_str(), nullptr, nullptr, SW_SHOWNORMAL);
}

// the launch variables an agent's capture job sets (the project's audit, 12 October 2026): the page to open and the theme as
// query parameters the page reads; the window size by IGNEUM_WALLET_SIZE ("900x620")
static std::wstring envW(const wchar_t* name) {
    wchar_t buf[256]; DWORD n = GetEnvironmentVariableW(name, buf, 256);
    return (n > 0 && n < 256) ? std::wstring(buf, n) : L"";
}
static std::wstring launchQuery() {
    std::wstring q, o = envW(L"IGNEUM_WALLET_OPEN"), t = envW(L"IGNEUM_WALLET_THEME");
    bool ok = !o.empty();
    for (wchar_t c : o) if (!iswalpha(c)) ok = false;
    if (ok) q += L"&open=" + o;
    if (t == L"dark" || t == L"light") q += L"&theme=" + t;
    return q;
}
static bool launchSize(int& w, int& h) {
    std::wstring s = envW(L"IGNEUM_WALLET_SIZE"); size_t x = s.find(L'x');
    if (s.empty() || s == L"default" || x == std::wstring::npos) return false;
    int lw = _wtoi(s.substr(0, x).c_str()), lh = _wtoi(s.substr(x + 1).c_str());
    if (lw < 600 || lh < 400) return false;
    w = lw; h = lh; return true;
}

static void navigate() {
    if (g_webview && !g_url.empty()) g_webview->Navigate((g_url + L"?host=windows" + launchQuery()).c_str());
}

static void initWebView() {
    std::wstring data = L"";
    wchar_t* local = nullptr;
    size_t len = 0;
    if (_wdupenv_s(&local, &len, L"LOCALAPPDATA") == 0 && local) { data = std::wstring(local) + L"\\igneum\\wallet\\webview2"; free(local); } // the wallet's own folder: WebView2 refuses a second executable on a folder another holds, and Ember's host used igneum\webview2 too (the second Windows machine under test, 12 October 2026 21:45 UK)
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
                // the page's Windows Hello bridge (biometric.h): a JSON string in, a JSON object back on the UI thread
                g_webview->add_WebMessageReceived(Callback<ICoreWebView2WebMessageReceivedEventHandler>([](ICoreWebView2*, ICoreWebView2WebMessageReceivedEventArgs* args) -> HRESULT {
                    LPWSTR src = nullptr;
                    if (FAILED(args->get_Source(&src)) || !src) return S_OK;
                    std::wstring origin(src);
                    CoTaskMemFree(src);
                    if (origin.rfind(L"http://127.0.0.1:", 0) != 0) return S_OK;   // only the engine's own page
                    LPWSTR text = nullptr;
                    if (SUCCEEDED(args->TryGetWebMessageAsString(&text)) && text) {
                        std::string msg = igbio::narrow8(text);
                        CoTaskMemFree(text);
                        igbio::handle(msg, [](const std::string& json) { PostMessageW(g_hwnd, WM_BIO_REPLY, 0, (LPARAM) new std::string(json)); });
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
    // the restart's engine (OLD-BASE.md step 8: the previous wallet's host on it): the page from ui\ beside the
    // host, the signer beside the engine, --exit-with-parent so the engine and the signer end when this host's end
    // of the stdin pipe closes; the engine prints ENGINE {url} then HOST {token, biometric_file}
    std::wstring exe = exeDir() + L"\\igneum-wallet-engine.exe";
    std::wstring cmd = L"\"" + exe + L"\" --ui \"" + exeDir() + L"\\ui\" --signer \"" + exeDir() + L"\\igneum-signer.exe\" --exit-with-parent";
    // this window's pid for the engine's updater: before an installer runs, the engine ends its own process tree by
    // pid (this window first), never a process by name (the 0.0.21 fix for the second Windows machine's code 5)
    SetEnvironmentVariableW(L"IGNEUM_WALLET_HOST_PID", std::to_wstring(GetCurrentProcessId()).c_str());
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

static void showTrayMenu() {
    HMENU m = CreatePopupMenu();
    AppendMenuW(m, MF_STRING, ID_TRAY_OPEN, L"Open Igneum Wallet");
    AppendMenuW(m, MF_STRING, ID_TRAY_PAUSE, g_paused ? L"Lock" : L"Lock");
    AppendMenuW(m, MF_SEPARATOR, 0, nullptr);
    AppendMenuW(m, MF_STRING | MF_GRAYED, 0, g_trayTitle.c_str());
    AppendMenuW(m, MF_SEPARATOR, 0, nullptr);
    AppendMenuW(m, MF_STRING, ID_TRAY_QUIT, L"Quit Igneum Wallet");
    POINT pt; GetCursorPos(&pt);
    SetForegroundWindow(g_hwnd);
    TrackPopupMenu(m, TPM_RIGHTBUTTON | TPM_BOTTOMALIGN, pt.x, pt.y, 0, g_hwnd, nullptr);
    DestroyMenu(m);
}

static void beginQuit() {
    if (g_quitting) return;
    g_quitting = true;
    g_quitStarted = GetTickCount64();
    g_status = L"Stopping";
    setTray(L"Igneum Wallet: stopping");
    if (g_webview) g_webview->ExecuteScript(L"document.getElementById('pill-text').textContent='stopping'", nullptr);
    if (g_engine && !g_exited) {
        // the restart's engine ends when its stdin closes (--exit-with-parent), locking the signer with it; the
        // quit timer below watches the process handle and ends the window when it is gone
        if (g_engineIn) { CloseHandle(g_engineIn); g_engineIn = nullptr; }
        SetTimer(g_hwnd, ID_QUIT_TIMER, 500, nullptr);
    } else {
        DestroyWindow(g_hwnd);
    }
}

static LRESULT CALLBACK WndProc(HWND hwnd, UINT msg, WPARAM wp, LPARAM lp) {
    switch (msg) {
    case WM_SIZE:
        if (g_controller) { RECT rc; GetClientRect(hwnd, &rc); g_controller->put_Bounds(rc); }
        return 0;
    case WM_ENGINE_LINE: {
        if (wp == 1) {
            g_exited = true;
            if (g_quitting || g_updating) { DestroyWindow(hwnd); return 0; }
            g_status = L"The engine stopped. Close this window and open Igneum Wallet again.";
            setTray(L"Igneum Wallet: stopped");
            repaintStatus();
            return 0;
        }
        std::string* line = (std::string*)lp;
        if (line->rfind("ENGINE ", 0) == 0) {
            // ENGINE {"url": "http://127.0.0.1:<port>/t/<token>/", "pid": n}: the page's address for this run; the
            // url is read out of the JSON by its quotes (no JSON library in the host)
            std::string j = line->substr(7);
            size_t k = j.find("\"url\"");
            size_t q1 = (k == std::string::npos) ? std::string::npos : j.find('"', j.find(':', k) + 1);
            size_t q2 = (q1 == std::string::npos) ? std::string::npos : j.find('"', q1 + 1);
            if (q1 != std::string::npos && q2 != std::string::npos) {
                g_url = widen(j.substr(q1 + 1, q2 - q1 - 1));
                if (g_webviewOk) navigate();
                else if (!g_webview && g_status.find(L"WebView2") != std::wstring::npos && !g_hintShown) { openInBrowser(g_url); g_hintShown = true; }
            }
        } else if (line->rfind("STATE ", 0) == 0) {
            // the poll thread's line (startStatePoll), the form the previous engine printed
            applyState(line->substr(6));
        } else if (line->rfind("UPDATE ", 0) == 0) {
            // the engine applies an update and exits; the window ends when the engine is gone, the installer's
            // relaunch line opens the new wallet
            g_updating = true;
            g_status = L"Updating";
            setTray(L"Igneum Wallet: updating");
        } else if (line->rfind("HOST ", 0) == 0) {
            // the host token and the sealed file's path; the page never sees this line
            g_hostLine = line->substr(5);
            igbio::configure(g_hostLine, g_url, L"Igneum Wallet", L"Turn on Windows Hello for your wallet", hwnd);
            startStatePoll();
        }
        // the restart's engine prints no STATE, ELEVATE, FATAL or EXIT line: the tray's state comes from a poll of
        // api/state (applyState on a timer, below), nothing on stdout runs a command, and the engine's end is seen
        // by the process handle (the previous ELEVATE branch, which ran an elevated command named on a stdout
        // line, is gone with the bundled node it served)
        delete line;
        return 0;
    }
    case WM_BIO_REPLY: {
        std::string* json = (std::string*)lp;
        if (g_webview) g_webview->PostWebMessageAsJson(igbio::widen8(*json).c_str());
        delete json;
        return 0;
    }
    case WM_TIMER:
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
    case WM_TRAY:
        if (lp == WM_LBUTTONUP || lp == WM_LBUTTONDBLCLK) { ShowWindow(hwnd, SW_SHOW); SetForegroundWindow(hwnd); }
        else if (lp == WM_RBUTTONUP || lp == WM_CONTEXTMENU) showTrayMenu();
        return 0;
    case WM_COMMAND:
        switch (LOWORD(wp)) {
        case ID_TRAY_OPEN: ShowWindow(hwnd, SW_SHOW); SetForegroundWindow(hwnd); return 0;
        case ID_TRAY_PAUSE: std::thread([] { igbio::post("api/lock", "{}"); }).detach(); return 0;   // the restart's engine takes commands on its API
        case ID_TRAY_QUIT: beginQuit(); return 0;
        }
        return 0;
    case WM_QUERYENDSESSION:
        // the session ends, or an installer's Restart Manager asks (ENDSESSION_CLOSEAPP): this window can end
        return TRUE;
    case WM_ENDSESSION:
        // the answer was yes: the engine's stdin closes (it locks the signer and ends), the window ends with it; a
        // hidden window that only hid on WM_CLOSE held the installer's files for its thirty-five seconds and failed it
        if (wp) beginQuit();
        return 0;
    case WM_CLOSE:
        // closing hides the window to the tray as Ember does (the tray's Open and Quit; the node connection and the
        // watch kept); the one notice names what happened in the wallet's own words, once, with a box that keeps it
        // from showing again (the founder's bug of 14 October 2026: the miner's "keeps mining" words on the wallet)
        ShowWindow(hwnd, SW_HIDE);
        if (!g_hintShown && !trayHintSuppressed()) {
            g_hintShown = true;
            // a plain message box (no common-controls dependency: a TaskDialog import failed to load on the first
            // 0.0.12 build, "ordinal 345 not found"); the second question is the "do not show again" box
            std::thread([] {
                MessageBoxW(nullptr, L"The window is hidden. Open it from the tray icon; Quit from the tray or the window ends the wallet.", L"Igneum Wallet is still running in the tray", MB_OK | MB_ICONINFORMATION | MB_SETFOREGROUND);
                if (MessageBoxW(nullptr, L"Show this notice again the next time the window is closed?", L"Igneum Wallet", MB_YESNO | MB_ICONQUESTION | MB_DEFBUTTON1 | MB_SETFOREGROUND) == IDNO) setTrayHintSuppressed();
            }).detach();
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
        if (a == L"--version" || a == L"-V") text = L"Igneum Wallet " IGNEUM_HOST_VERSION_STR L" (window host)\r\n";
        else if (a == L"--help" || a == L"-h" || a == L"/?")
            text = L"Igneum Wallet " IGNEUM_HOST_VERSION_STR L" (window host)\r\n"
                   L"Usage: \"Igneum Wallet.exe\" [--version | --help]\r\n"
                   L"Without flags it starts igneum-wallet-engine.exe next to it (with the page and the signer beside it) and shows the wallet in a WebView2 window.\r\n";
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
    HANDLE once = CreateMutexW(nullptr, TRUE, L"Local\\IgneumWalletWindow");
    if (GetLastError() == ERROR_ALREADY_EXISTS) {
        HWND other = FindWindowW(L"IgneumWalletWindow", nullptr);
        if (other) { ShowWindow(other, SW_SHOW); SetForegroundWindow(other); }
        return 0;
    }
    CoInitializeEx(nullptr, COINIT_APARTMENTTHREADED);
    WNDCLASSW wc = {};
    wc.lpfnWndProc = WndProc;
    wc.hInstance = hInst;
    wc.lpszClassName = L"IgneumWalletWindow";
    wc.hIcon = LoadIconW(hInst, MAKEINTRESOURCEW(IDI_APP));
    wc.hCursor = LoadCursorW(nullptr, IDC_ARROW);
    wc.hbrBackground = CreateSolidBrush(RGB(12, 12, 14));
    RegisterClassW(&wc);
    int w = 1120, h = 820;
    launchSize(w, h);
    int sx = (GetSystemMetrics(SM_CXSCREEN) - w) / 2, sy = (GetSystemMetrics(SM_CYSCREEN) - h) / 2;
    g_hwnd = CreateWindowExW(0, wc.lpszClassName, L"Igneum Wallet", WS_OVERLAPPEDWINDOW, sx, sy, w, h, nullptr, nullptr, hInst, nullptr);
    ShowWindow(g_hwnd, SW_SHOW);

    g_nid.cbSize = sizeof(g_nid);
    g_nid.hWnd = g_hwnd;
    g_nid.uID = 1;
    g_nid.uFlags = NIF_ICON | NIF_MESSAGE | NIF_TIP;
    g_nid.uCallbackMessage = WM_TRAY;
    g_nid.hIcon = (HICON)LoadImageW(hInst, MAKEINTRESOURCEW(IDI_APP), IMAGE_ICON, 16, 16, LR_DEFAULTCOLOR);
    wcscpy_s(g_nid.szTip, L"Igneum Wallet: starting");
    Shell_NotifyIconW(NIM_ADD, &g_nid);

    if (!startEngine()) {
        g_status = L"igneum-wallet-engine.exe or igneum-signer.exe is missing next to this program. Run the installer again.";
        repaintStatus();
        MessageBoxW(g_hwnd, g_status.c_str(), L"Igneum Wallet", MB_OK | MB_ICONERROR);
    }
    initWebView();

    MSG msg;
    while (GetMessageW(&msg, nullptr, 0, 0)) {
        TranslateMessage(&msg);
        DispatchMessageW(&msg);
    }
    if (g_engine) { CloseHandle(g_engine); }
    CloseHandle(once);
    CoUninitialize();
    return 0;
}
