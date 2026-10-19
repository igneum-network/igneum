// json.h: a minimal JSON value with a parser and a serialiser, enough for the worker
// protocol (flat objects, one level of nesting for params and knob lists, strings,
// numbers, booleans). No third-party code. GPU engineer B, 9 October 2026.
#pragma once
#include <cstdint>
#include <map>
#include <memory>
#include <string>
#include <vector>

namespace igm {

class Json {
public:
    enum Type { Null, Bool, Number, String, Array, Object };
    Json() : t_(Null) {}
    Json(bool b) : t_(Bool), b_(b) {}
    Json(double d) : t_(Number), n_(d) {}
    Json(int v) : t_(Number), n_(v) {}
    Json(unsigned v) : t_(Number), n_(v) {}
    Json(long long v) : t_(Number), n_((double)v) {}
    Json(unsigned long long v) : t_(Number), n_((double)v) {}
    Json(const char *s) : t_(String), s_(s) {}
    Json(const std::string &s) : t_(String), s_(s) {}
    static Json array() { Json j; j.t_ = Array; return j; }
    static Json object() { Json j; j.t_ = Object; return j; }

    Type type() const { return t_; }
    bool is_null() const { return t_ == Null; }
    bool is_object() const { return t_ == Object; }
    bool as_bool(bool d = false) const { return t_ == Bool ? b_ : d; }
    double as_number(double d = 0) const { return t_ == Number ? n_ : d; }
    uint64_t as_u64(uint64_t d = 0) const { return t_ == Number ? (uint64_t)n_ : d; }
    const std::string &as_string() const { static const std::string e; return t_ == String ? s_ : e; }
    const std::vector<Json> &items() const { return a_; }
    bool has(const std::string &k) const { return t_ == Object && o_.count(k) > 0; }
    const Json &operator[](const std::string &k) const { static const Json nul; auto it = o_.find(k); return it == o_.end() ? nul : it->second; }
    Json &set(const std::string &k, const Json &v) { t_ = Object; o_[k] = v; return *this; }
    Json &push(const Json &v) { t_ = Array; a_.push_back(v); return *this; }
    size_t size() const { return t_ == Array ? a_.size() : t_ == Object ? o_.size() : 0; }
    const Json &at(size_t i) const { static const Json nul; return t_ == Array && i < a_.size() ? a_[i] : nul; }

    std::string dump() const;
    static bool parse(const std::string &text, Json &out);

private:
    Type t_;
    bool b_ = false;
    double n_ = 0;
    std::string s_;
    std::vector<Json> a_;
    std::map<std::string, Json> o_;
};

}  // namespace igm
