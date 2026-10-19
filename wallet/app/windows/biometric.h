// Windows Hello for the Igneum window hosts (wallet-host.cpp, host.cpp): the WebView2 side of what
// app/mac/Biometric.swift does with Touch ID. 5 October 2026. UNTESTED on a PC at the time of writing (written on a
// Mac): BUILD-WALLET-APP.bat / BUILD-APP.bat on the GitHub runner are the first compile.
//
// The page posts a JSON string {id, op, nonce?, token?} with window.chrome.webview.postMessage; the host answers with
// PostWebMessageAsJson({id, ok, code, message, ...}) and the page's listener routes it to window.__igneumBiometric.
// Ops: status, enrol, unlock (wallet), confirm, password (the host's own sheet, G1).
//
// The prompt is Windows.Security.Credentials.UI.UserConsentVerifier, through IUserConsentVerifierInterop so a Win32
// window can own it (RequestVerificationForWindowAsync). What is stored: the wallet's password, sealed with DPAPI
// (CryptProtectData, current user, CRYPTPROTECT_UI_FORBIDDEN) only after a Hello success, in the file the engine
// named on its HOST line (%LOCALAPPDATA%\igneum\wallet\biometric.json); the miner seals a fixed marker. DPAPI is as
// strong as the Windows account: anything running as this user can unseal it, which is why the host only unseals
// after Hello checked, and why the threat model in app/igneum-wallet/README.md says what this does and does not
// protect. The secret never goes through the page: the host posts it to the engine on 127.0.0.1 with the engine's
// URL token; X-Igneum-Host (the token from the HOST line) marks the calls only the host may make.
//
// Needs: C++/WinRT headers (the Windows SDK's cppwinrt include, on INCLUDE after vcvarsall), windowsapp.lib,
// crypt32.lib, winhttp.lib. C++17.

#pragma once
#include <windows.h>
#include <wincrypt.h>
#include <winhttp.h>
#include <winrt/base.h>
#include <winrt/Windows.Foundation.h>
#include <winrt/Windows.Security.Credentials.UI.h>
#include <UserConsentVerifierInterop.h>
#include <string>
#include <vector>
#include <thread>
#include <functional>

namespace igbio {

using winrt::Windows::Security::Credentials::UI::UserConsentVerifier;
using winrt::Windows::Security::Credentials::UI::UserConsentVerifierAvailability;
using winrt::Windows::Security::Credentials::UI::UserConsentVerificationResult;

struct Config {
    std::wstring product;      // L"Igneum Wallet" | L"Ember"
    std::wstring enrolReason;  // L"Turn on Windows Hello for your wallet" | L"... for the miner"
    std::wstring engineURL;    // http://127.0.0.1:<port>/t/<token>/
    std::string hostToken;     // from the HOST line
    std::wstring file;         // the sealed file's path from the HOST line
    HWND hwnd = nullptr;       // the window that owns the prompt
};

static Config g_cfg;
static const char* MARKER = "igneum-biometric-marker-v1";

// ---- small JSON helpers (flat objects, string values) ----

static std::string jsonEscape(const std::string& s) {
    std::string o;
    for (unsigned char c : s) {
        switch (c) {
        case '"': o += "\\\""; break;
        case '\\': o += "\\\\"; break;
        case '\n': o += "\\n"; break;
        case '\r': o += "\\r"; break;
        case '\t': o += "\\t"; break;
        default:
            if (c < 0x20) { char b[8]; sprintf_s(b, "\\u%04x", c); o += b; } else o += (char)c;
        }
    }
    return o;
}

// The value of "key" in a flat JSON object: a string (escapes decoded), a number, or a bool as text.
static std::string jsonGet(const std::string& j, const char* key) {
    std::string k = std::string("\"") + key + "\"";
    size_t i = j.find(k);
    if (i == std::string::npos) return "";
    i = j.find(':', i + k.size());
    if (i == std::string::npos) return "";
    i++;
    while (i < j.size() && (j[i] == ' ' || j[i] == '\t')) i++;
    if (i < j.size() && j[i] == '"') {
        std::string o;
        for (i++; i < j.size() && j[i] != '"'; i++) {
            if (j[i] == '\\' && i + 1 < j.size()) {
                char e = j[++i];
                if (e == 'n') o += '\n';
                else if (e == 'r') o += '\r';
                else if (e == 't') o += '\t';
                else if (e == 'u' && i + 4 < j.size()) {
                    unsigned v = strtoul(j.substr(i + 1, 4).c_str(), nullptr, 16);
                    i += 4;
                    if (v < 0x80) o += (char)v;
                    else if (v < 0x800) { o += (char)(0xC0 | (v >> 6)); o += (char)(0x80 | (v & 0x3F)); }
                    else { o += (char)(0xE0 | (v >> 12)); o += (char)(0x80 | ((v >> 6) & 0x3F)); o += (char)(0x80 | (v & 0x3F)); }
                } else o += e;
            } else o += j[i];
        }
        return o;
    }
    size_t e = i;
    while (e < j.size() && j[e] != ',' && j[e] != '}') e++;
    std::string v = j.substr(i, e - i);
    while (!v.empty() && (v.back() == ' ' || v.back() == '\r' || v.back() == '\n')) v.pop_back();
    return v;
}

static std::wstring widen8(const std::string& s) {
    if (s.empty()) return L"";
    int n = MultiByteToWideChar(CP_UTF8, 0, s.data(), (int)s.size(), nullptr, 0);
    std::wstring w(n, 0);
    MultiByteToWideChar(CP_UTF8, 0, s.data(), (int)s.size(), &w[0], n);
    return w;
}
static std::string narrow8(const std::wstring& w) {
    if (w.empty()) return "";
    int n = WideCharToMultiByte(CP_UTF8, 0, w.data(), (int)w.size(), nullptr, 0, nullptr, nullptr);
    std::string s(n, 0);
    WideCharToMultiByte(CP_UTF8, 0, w.data(), (int)w.size(), &s[0], n, nullptr, nullptr);
    return s;
}

static std::string b64(const std::vector<BYTE>& d) {
    DWORD n = 0;
    CryptBinaryToStringA(d.data(), (DWORD)d.size(), CRYPT_STRING_BASE64 | CRYPT_STRING_NOCRLF, nullptr, &n);
    std::string o(n, 0);
    CryptBinaryToStringA(d.data(), (DWORD)d.size(), CRYPT_STRING_BASE64 | CRYPT_STRING_NOCRLF, &o[0], &n);
    while (!o.empty() && o.back() == 0) o.pop_back();
    return o;
}
static std::vector<BYTE> unb64(const std::string& s) {
    DWORD n = 0;
    if (!CryptStringToBinaryA(s.c_str(), (DWORD)s.size(), CRYPT_STRING_BASE64, nullptr, &n, nullptr, nullptr)) return {};
    std::vector<BYTE> o(n);
    if (!CryptStringToBinaryA(s.c_str(), (DWORD)s.size(), CRYPT_STRING_BASE64, o.data(), &n, nullptr, nullptr)) return {};
    o.resize(n);
    return o;
}

static std::string reply(bool ok, const std::string& code, const std::string& message) {
    return std::string("{\"ok\":") + (ok ? "true" : "false") + ",\"code\":\"" + jsonEscape(code) + "\",\"message\":\"" + jsonEscape(message) + "\"}";
}

// ---- the engine on 127.0.0.1 (WinHTTP) ----

struct Answer { bool ok; int status; std::string body; };

static Answer call(const std::wstring& method, const std::string& path, const std::string& body) {
    Answer a = { false, 0, "" };
    URL_COMPONENTS uc = { sizeof(uc) };
    wchar_t host[64] = {}, upath[1024] = {};
    uc.lpszHostName = host; uc.dwHostNameLength = 64;
    uc.lpszUrlPath = upath; uc.dwUrlPathLength = 1024;
    std::wstring full = g_cfg.engineURL + widen8(path);
    if (!WinHttpCrackUrl(full.c_str(), 0, 0, &uc)) { a.body = "{\"error\":\"bad engine url\"}"; return a; }
    HINTERNET s = WinHttpOpen(L"IgneumHost/1", WINHTTP_ACCESS_TYPE_NO_PROXY, WINHTTP_NO_PROXY_NAME, WINHTTP_NO_PROXY_BYPASS, 0);
    if (!s) { a.body = "{\"error\":\"winhttp\"}"; return a; }
    WinHttpSetTimeouts(s, 5000, 5000, 15000, 15000);
    HINTERNET c = WinHttpConnect(s, host, uc.nPort, 0);
    HINTERNET r = c ? WinHttpOpenRequest(c, method.c_str(), upath, nullptr, WINHTTP_NO_REFERER, WINHTTP_DEFAULT_ACCEPT_TYPES, 0) : nullptr;
    if (r) {
        std::wstring hdr = L"X-Igneum-Host: " + widen8(g_cfg.hostToken) + L"\r\nContent-Type: application/json\r\n";
        if (WinHttpSendRequest(r, hdr.c_str(), (DWORD)-1, body.empty() ? WINHTTP_NO_REQUEST_DATA : (LPVOID)body.data(), (DWORD)body.size(), (DWORD)body.size(), 0) && WinHttpReceiveResponse(r, nullptr)) {
            DWORD st = 0, n = sizeof(st);
            WinHttpQueryHeaders(r, WINHTTP_QUERY_STATUS_CODE | WINHTTP_QUERY_FLAG_NUMBER, WINHTTP_HEADER_NAME_BY_INDEX, &st, &n, WINHTTP_NO_HEADER_INDEX);
            a.status = (int)st;
            DWORD avail = 0;
            while (WinHttpQueryDataAvailable(r, &avail) && avail > 0) {
                std::string chunk(avail, 0);
                DWORD got = 0;
                if (!WinHttpReadData(r, &chunk[0], avail, &got)) break;
                a.body.append(chunk, 0, got);
            }
            a.ok = st == 200 && jsonGet(a.body, "ok") != "false";
        } else a.body = "{\"error\":\"no answer from the engine\"}";
    }
    if (r) WinHttpCloseHandle(r);
    if (c) WinHttpCloseHandle(c);
    WinHttpCloseHandle(s);
    return a;
}
static Answer post(const std::string& path, const std::string& body) { return call(L"POST", path, body); }
static Answer get(const std::string& path) { return call(L"GET", path, ""); }

// ---- Windows Hello ----

static std::string availabilityText(UserConsentVerifierAvailability a, bool* available) {
    *available = false;
    switch (a) {
    case UserConsentVerifierAvailability::Available: *available = true; return "";
    case UserConsentVerifierAvailability::DeviceNotPresent: return "Windows Hello has no fingerprint or face sensor on this PC.";
    case UserConsentVerifierAvailability::NotConfiguredForUser: return "Windows Hello is not set up on this PC. Set it up in Settings > Accounts > Sign-in options.";
    case UserConsentVerifierAvailability::DisabledByPolicy: return "Windows Hello is disabled by policy on this PC.";
    case UserConsentVerifierAvailability::DeviceBusy: return "The Windows Hello sensor is busy.";
    default: return "Windows Hello is not available on this PC.";
    }
}

static std::string status(bool* available) {
    try {
        auto a = UserConsentVerifier::CheckAvailabilityAsync().get();
        return availabilityText(a, available);
    } catch (...) {
        *available = false;
        return "Windows Hello could not be checked on this PC.";
    }
}

// The prompt. Returns "" on success, else the reply JSON for the page.
static std::string verify(const std::wstring& reason) {
    try {
        auto factory = winrt::get_activation_factory<UserConsentVerifier, IUserConsentVerifierInterop>();
        winrt::Windows::Foundation::IAsyncOperation<UserConsentVerificationResult> op{ nullptr };
        winrt::hstring msg(reason.substr(0, 80));
        winrt::check_hresult(factory->RequestVerificationForWindowAsync(g_cfg.hwnd, static_cast<HSTRING>(winrt::get_abi(msg)),
            winrt::guid_of<winrt::Windows::Foundation::IAsyncOperation<UserConsentVerificationResult>>(), winrt::put_abi(op)));
        auto r = op.get();
        switch (r) {
        case UserConsentVerificationResult::Verified: return "";   // the API's own name (the old-base text pass had rewritten it; found by the second Windows machine's second build, 11 October 2026)
        case UserConsentVerificationResult::Canceled: return reply(false, "cancelled", "Windows Hello was cancelled.");
        case UserConsentVerificationResult::RetriesExhausted: return reply(false, "locked", "Windows Hello is locked after too many tries. Use the password.");
        case UserConsentVerificationResult::NotConfiguredForUser: return reply(false, "not_set_up", "Windows Hello is not set up on this PC. Set it up in Settings > Accounts > Sign-in options.");
        case UserConsentVerificationResult::DeviceNotPresent: return reply(false, "unavailable", "Windows Hello has no sensor on this PC.");
        case UserConsentVerificationResult::DisabledByPolicy: return reply(false, "unavailable", "Windows Hello is disabled by policy on this PC.");
        case UserConsentVerificationResult::DeviceBusy: return reply(false, "failed", "The Windows Hello sensor is busy; try again.");
        default: return reply(false, "failed", "Windows Hello did not succeed.");
        }
    } catch (const winrt::hresult_error& e) {
        return reply(false, "failed", "Windows Hello failed: " + narrow8(std::wstring(e.message())));
    } catch (...) {
        return reply(false, "failed", "Windows Hello failed.");
    }
}

// ---- the sealed file (DPAPI, current user) ----

static bool readFile(std::string* out) {
    HANDLE h = CreateFileW(g_cfg.file.c_str(), GENERIC_READ, FILE_SHARE_READ, nullptr, OPEN_EXISTING, FILE_ATTRIBUTE_NORMAL, nullptr);
    if (h == INVALID_HANDLE_VALUE) return false;
    char buf[8192]; DWORD n = 0;
    out->clear();
    while (ReadFile(h, buf, sizeof(buf), &n, nullptr) && n > 0) out->append(buf, n);
    CloseHandle(h);
    return true;
}
static bool writeFile(const std::string& text) {
    size_t i = g_cfg.file.find_last_of(L"\\/");
    if (i != std::wstring::npos) CreateDirectoryW(g_cfg.file.substr(0, i).c_str(), nullptr);
    HANDLE h = CreateFileW(g_cfg.file.c_str(), GENERIC_WRITE, 0, nullptr, CREATE_ALWAYS, FILE_ATTRIBUTE_NORMAL, nullptr);
    if (h == INVALID_HANDLE_VALUE) return false;
    DWORD w = 0;
    BOOL ok = WriteFile(h, text.data(), (DWORD)text.size(), &w, nullptr);
    CloseHandle(h);
    return ok && w == text.size();
}
static bool sealedExists() {
    DWORD a = GetFileAttributesW(g_cfg.file.c_str());
    return a != INVALID_FILE_ATTRIBUTES && !(a & FILE_ATTRIBUTE_DIRECTORY);
}

static bool seal(const std::string& secret, std::string* boxB64) {
    DATA_BLOB in = { (DWORD)secret.size(), (BYTE*)secret.data() }, out = {};
    std::wstring desc = g_cfg.product + L" biometric";
    if (!CryptProtectData(&in, desc.c_str(), nullptr, nullptr, nullptr, CRYPTPROTECT_UI_FORBIDDEN, &out)) return false;
    std::vector<BYTE> v(out.pbData, out.pbData + out.cbData);
    LocalFree(out.pbData);
    *boxB64 = b64(v);
    return true;
}
static bool unseal(const std::string& boxB64, std::string* secret) {
    std::vector<BYTE> v = unb64(boxB64);
    if (v.empty()) return false;
    DATA_BLOB in = { (DWORD)v.size(), v.data() }, out = {};
    if (!CryptUnprotectData(&in, nullptr, nullptr, nullptr, nullptr, CRYPTPROTECT_UI_FORBIDDEN, &out)) return false;
    secret->assign((char*)out.pbData, out.cbData);
    SecureZeroMemory(out.pbData, out.cbData);
    LocalFree(out.pbData);
    return true;
}

// ---- the ops ----

static std::string opStatus() {
    bool avail = false;
    std::string msg = status(&avail);
    return std::string("{\"ok\":true,\"available\":") + (avail ? "true" : "false") + ",\"kind\":\"hello\",\"message\":\"" + jsonEscape(msg) + "\",\"enrolled\":" + (sealedExists() ? "true" : "false") + "}";
}

static std::string opEnrol(const std::string& token) {
    if (g_cfg.file.empty()) return reply(false, "unavailable", "The engine has not named the sealed file yet.");
    bool avail = false;
    std::string msg = status(&avail);
    if (!avail) return reply(false, "unavailable", msg);
    std::string e = verify(g_cfg.enrolReason);
    if (!e.empty()) return e;
    std::string secret;
    if (!token.empty()) {
        Answer a = post("api/biometric/enrol/take", "{\"token\":\"" + jsonEscape(token) + "\"}");
        if (!a.ok) return reply(false, "engine", jsonGet(a.body, "error").empty() ? "the engine did not hand over the password" : jsonGet(a.body, "error"));
        secret = jsonGet(a.body, "secret");
    } else secret = MARKER;
    std::string box;
    bool ok = seal(secret, &box);
    SecureZeroMemory(&secret[0], secret.size());
    if (!ok) return reply(false, "seal", "DPAPI could not seal the secret.");
    std::string doc = "{\"version\":1,\"kind\":\"hello\",\"product\":\"" + jsonEscape(narrow8(g_cfg.product)) + "\",\"created\":" + std::to_string((long long)(GetTickCount64() / 1000)) + ",\"box\":\"" + box + "\"}";
    if (!writeFile(doc)) return reply(false, "file", "The sealed file could not be written.");
    Answer a = post("api/biometric/enrolled", "{\"kind\":\"hello\"}");
    if (!a.ok) return reply(false, "engine", "the engine did not record the enrolment");
    return reply(true, "", "");
}

// The host's own password dialog (wallet-security's G1, 12 October 2026): a Win32 dialog built in memory with one
// ES_PASSWORD edit, the typed password posted by the host to the engine's unlock with the host token, never given
// to the page; the buffer is zeroed after. The renderer's field stays for a browser tab.
static std::wstring g_pwReason;
static std::wstring g_pwTyped;
static INT_PTR CALLBACK pwDlgProc(HWND h, UINT m, WPARAM w, LPARAM) {
    switch (m) {
    case WM_INITDIALOG:
        SetDlgItemTextW(h, 100, g_pwReason.c_str());
        SetFocus(GetDlgItem(h, 101));
        return FALSE;
    case WM_COMMAND:
        if (LOWORD(w) == IDOK) {
            wchar_t buf[512];
            GetDlgItemTextW(h, 101, buf, 512);
            g_pwTyped = buf;
            SecureZeroMemory(buf, sizeof(buf));
            SetDlgItemTextW(h, 101, L"");
            EndDialog(h, IDOK);
            return TRUE;
        }
        if (LOWORD(w) == IDCANCEL) { EndDialog(h, IDCANCEL); return TRUE; }
        break;
    case WM_CLOSE: EndDialog(h, IDCANCEL); return TRUE;
    }
    return FALSE;
}
static std::string opPassword(const std::string& reason) {
    g_pwReason = reason.empty() ? L"Unlock your wallet" : std::wstring(reason.begin(), reason.end());
    g_pwTyped.clear();
    // the template: a dialog with a static (100), a password edit (101), OK and Cancel; built word by word
    std::vector<WORD> tpl;
    auto w16 = [&](WORD v) { tpl.push_back(v); };
    auto w32 = [&](DWORD v) { w16((WORD)(v & 0xffff)); w16((WORD)(v >> 16)); };
    auto str = [&](const wchar_t* s) { for (; *s; ++s) w16((WORD)*s); w16(0); };
    auto align = [&]() { while (tpl.size() % 2) w16(0); };
    w32(DS_MODALFRAME | DS_SETFONT | WS_POPUP | WS_CAPTION | WS_SYSMENU); w32(0); w16(4); w16(0); w16(0); w16(220); w16(78);
    w16(0); w16(0); str(L"Igneum Wallet"); w16(9); str(L"Segoe UI");
    auto item = [&](DWORD style, short x, short y, short cx, short cy, WORD id, WORD cls, const wchar_t* text) {
        align(); w32(style | WS_CHILD | WS_VISIBLE); w32(0); w16(x); w16(y); w16(cx); w16(cy); w16(id); w16(0xFFFF); w16(cls); str(text); w16(0);
    };
    item(SS_LEFT, 10, 8, 200, 20, 100, 0x0082, L"");
    item(ES_PASSWORD | ES_AUTOHSCROLL | WS_BORDER | WS_TABSTOP, 10, 30, 200, 14, 101, 0x0081, L"");
    item(BS_DEFPUSHBUTTON | WS_TABSTOP, 100, 52, 50, 16, IDOK, 0x0080, L"Unlock");
    item(BS_PUSHBUTTON | WS_TABSTOP, 160, 52, 50, 16, IDCANCEL, 0x0080, L"Cancel");
    INT_PTR rc = DialogBoxIndirectParamW(GetModuleHandleW(nullptr), (LPCDLGTEMPLATEW)tpl.data(), g_cfg.hwnd, pwDlgProc, 0);
    if (rc != IDOK || g_pwTyped.empty()) { g_pwTyped.clear(); return reply(false, "cancelled", "No password given."); }
    std::string secret = narrow8(g_pwTyped);
    SecureZeroMemory(&g_pwTyped[0], g_pwTyped.size() * sizeof(wchar_t));
    g_pwTyped.clear();
    Answer a = post("api/unlock", "{\"password\":\"" + jsonEscape(secret) + "\"}");
    SecureZeroMemory(&secret[0], secret.size());
    if (!a.ok) return reply(false, "engine", jsonGet(a.body, "error").empty() ? "the engine did not unlock" : jsonGet(a.body, "error"));
    return reply(true, "", "");
}

static std::string opUnlock() {
    std::string text;
    if (!sealedExists() || !readFile(&text)) return reply(false, "not_enrolled", "Windows Hello is not turned on for this wallet.");
    std::string e = verify(L"Unlock your wallet");
    if (!e.empty()) return e;
    std::string secret;
    if (!unseal(jsonGet(text, "box"), &secret)) return reply(false, "invalidated", "Windows Hello no longer opens this wallet: the sealed password could not be read. Use the password, then turn Windows Hello on again in Settings.");
    Answer a = post("api/unlock", "{\"password\":\"" + jsonEscape(secret) + "\"}");
    SecureZeroMemory(&secret[0], secret.size());
    if (!a.ok) return reply(false, "engine", jsonGet(a.body, "error").empty() ? "the engine did not unlock" : jsonGet(a.body, "error"));
    return reply(true, "", "");
}

static std::string opConfirm(const std::string& nonce) {
    if (nonce.empty()) return reply(false, "bad_nonce", "No challenge.");
    Answer c = get("api/biometric/challenge?nonce=" + nonce);
    if (!c.ok) return reply(false, "engine", jsonGet(c.body, "error").empty() ? "the engine does not know this challenge" : jsonGet(c.body, "error"));
    std::string e = verify(widen8(jsonGet(c.body, "reason")));
    if (!e.empty()) return e;
    if (sealedExists()) {
        // the sealed file must still open under this account (DPAPI; a reset account leaves it unreadable)
        std::string text, secret;
        if (!readFile(&text) || !unseal(jsonGet(text, "box"), &secret)) return reply(false, "invalidated", "The sealed file could not be read. Turn Windows Hello on again in Settings.");
        SecureZeroMemory(&secret[0], secret.size());
    }
    Answer a = post("api/biometric/confirm", "{\"nonce\":\"" + jsonEscape(nonce) + "\"}");
    if (!a.ok) return reply(false, "engine", jsonGet(a.body, "error").empty() ? "the engine refused the confirmation" : jsonGet(a.body, "error"));
    return reply(true, "", "");
}

/// The HOST line from the engine: {"token": "...", "biometric_file": "..."}. Posts the availability at once.
static void configure(const std::string& hostLineJson, const std::wstring& engineURL, const std::wstring& product, const std::wstring& enrolReason, HWND hwnd) {
    g_cfg.product = product;
    g_cfg.enrolReason = enrolReason;
    g_cfg.engineURL = engineURL;
    g_cfg.hostToken = jsonGet(hostLineJson, "token");
    g_cfg.file = widen8(jsonGet(hostLineJson, "biometric_file"));
    g_cfg.hwnd = hwnd;
    std::thread([] {
        winrt::init_apartment(winrt::apartment_type::multi_threaded);
        bool avail = false;
        std::string msg = status(&avail);
        post("api/biometric/status", std::string("{\"available\":") + (avail ? "true" : "false") + ",\"kind\":\"hello\",\"message\":\"" + jsonEscape(msg) + "\"}");
    }).detach();
}

/// A message from the page (the JSON string it posted). `answer` receives the reply JSON with the id put back; it is
/// called on a worker thread, so the host marshals it to the UI thread for PostWebMessageAsJson.
static void handle(const std::string& msg, std::function<void(const std::string&)> answer) {
    std::string id = jsonGet(msg, "id"), op = jsonGet(msg, "op"), nonce = jsonGet(msg, "nonce"), token = jsonGet(msg, "token"), reason = jsonGet(msg, "reason");
    std::thread([id, op, nonce, token, reason, answer] {
        winrt::init_apartment(winrt::apartment_type::multi_threaded);
        std::string r;
        if (op == "status") r = opStatus();
        else if (op == "enrol") r = opEnrol(token);
        else if (op == "unlock") r = opUnlock();
        else if (op == "confirm") r = opConfirm(nonce);
        else if (op == "password") r = opPassword(reason);
        else r = reply(false, "bad_op", "unknown op " + op);
        if (op != "status") {
            post("api/biometric/report", "{\"op\":\"" + jsonEscape(op) + "\",\"ok\":" + (jsonGet(r, "ok") == "true" ? "true" : "false") + ",\"code\":\"" + jsonEscape(jsonGet(r, "code")) + "\",\"message\":\"" + jsonEscape(jsonGet(r, "message")) + "\"}");
        }
        // put the id in front: {"id":N, ...rest}
        std::string withId = "{\"id\":" + (id.empty() ? "0" : id) + "," + r.substr(1);
        answer(withId);
    }).detach();
}

} // namespace igbio
