// json.cpp: see json.h.
#include "json.h"
#include <cmath>
#include <cstdio>
#include <cstring>

namespace igm {

namespace {

void dump_string(const std::string &s, std::string &o) {
    o.push_back('"');
    for (unsigned char c : s) {
        switch (c) {
            case '"': o += "\\\""; break;
            case '\\': o += "\\\\"; break;
            case '\n': o += "\\n"; break;
            case '\r': o += "\\r"; break;
            case '\t': o += "\\t"; break;
            default:
                if (c < 0x20) { char b[8]; snprintf(b, sizeof b, "\\u%04x", c); o += b; }
                else o.push_back((char)c);
        }
    }
    o.push_back('"');
}

struct Parser {
    const std::string &s; size_t i = 0;
    explicit Parser(const std::string &t) : s(t) {}
    void ws() { while (i < s.size() && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r')) i++; }
    bool lit(const char *w) { size_t n = strlen(w); if (s.compare(i, n, w) == 0) { i += n; return true; } return false; }
    bool str(std::string &out) {
        if (i >= s.size() || s[i] != '"') return false;
        i++;
        while (i < s.size()) {
            char c = s[i++];
            if (c == '"') return true;
            if (c == '\\') {
                if (i >= s.size()) return false;
                char e = s[i++];
                switch (e) {
                    case '"': out.push_back('"'); break;
                    case '\\': out.push_back('\\'); break;
                    case '/': out.push_back('/'); break;
                    case 'b': out.push_back('\b'); break;
                    case 'f': out.push_back('\f'); break;
                    case 'n': out.push_back('\n'); break;
                    case 'r': out.push_back('\r'); break;
                    case 't': out.push_back('\t'); break;
                    case 'u': {
                        if (i + 4 > s.size()) return false;
                        unsigned v = 0;
                        for (int k = 0; k < 4; k++) { char h = s[i++]; v <<= 4;
                            if (h >= '0' && h <= '9') v |= h - '0'; else if (h >= 'a' && h <= 'f') v |= h - 'a' + 10; else if (h >= 'A' && h <= 'F') v |= h - 'A' + 10; else return false; }
                        if (v < 0x80) out.push_back((char)v);
                        else if (v < 0x800) { out.push_back((char)(0xC0 | (v >> 6))); out.push_back((char)(0x80 | (v & 0x3F))); }
                        else { out.push_back((char)(0xE0 | (v >> 12))); out.push_back((char)(0x80 | ((v >> 6) & 0x3F))); out.push_back((char)(0x80 | (v & 0x3F))); }
                        break;
                    }
                    default: return false;
                }
            } else out.push_back(c);
        }
        return false;
    }
    bool value(Json &out) {
        ws();
        if (i >= s.size()) return false;
        char c = s[i];
        if (c == '{') {
            i++; out = Json::object(); ws();
            if (i < s.size() && s[i] == '}') { i++; return true; }
            while (true) {
                ws(); std::string k; if (!str(k)) return false; ws();
                if (i >= s.size() || s[i] != ':') return false; i++;
                Json v; if (!value(v)) return false; out.set(k, v); ws();
                if (i < s.size() && s[i] == ',') { i++; continue; }
                if (i < s.size() && s[i] == '}') { i++; return true; }
                return false;
            }
        }
        if (c == '[') {
            i++; out = Json::array(); ws();
            if (i < s.size() && s[i] == ']') { i++; return true; }
            while (true) {
                Json v; if (!value(v)) return false; out.push(v); ws();
                if (i < s.size() && s[i] == ',') { i++; continue; }
                if (i < s.size() && s[i] == ']') { i++; return true; }
                return false;
            }
        }
        if (c == '"') { std::string v; if (!str(v)) return false; out = Json(v); return true; }
        if (lit("true")) { out = Json(true); return true; }
        if (lit("false")) { out = Json(false); return true; }
        if (lit("null")) { out = Json(); return true; }
        if (c == '-' || (c >= '0' && c <= '9')) {
            size_t st = i; i++;
            while (i < s.size() && (isdigit((unsigned char)s[i]) || s[i] == '.' || s[i] == 'e' || s[i] == 'E' || s[i] == '+' || s[i] == '-')) i++;
            out = Json(strtod(s.c_str() + st, nullptr));
            return true;
        }
        return false;
    }
};

}  // namespace

std::string Json::dump() const {
    std::string o;
    switch (t_) {
        case Null: o = "null"; break;
        case Bool: o = b_ ? "true" : "false"; break;
        case Number: {
            if (std::floor(n_) == n_ && std::fabs(n_) < 9.007199254740992e15) { char b[32]; snprintf(b, sizeof b, "%lld", (long long)n_); o = b; }
            else { char b[64]; snprintf(b, sizeof b, "%.17g", n_); o = b; }
            break;
        }
        case String: dump_string(s_, o); break;
        case Array: { o = "["; bool first = true; for (auto &v : a_) { if (!first) o += ","; first = false; o += v.dump(); } o += "]"; break; }
        case Object: { o = "{"; bool first = true; for (auto &kv : o_) { if (!first) o += ","; first = false; dump_string(kv.first, o); o += ":"; o += kv.second.dump(); } o += "}"; break; }
    }
    return o;
}

bool Json::parse(const std::string &text, Json &out) {
    Parser p(text);
    if (!p.value(out)) return false;
    p.ws();
    return p.i == text.size();
}

}  // namespace igm
