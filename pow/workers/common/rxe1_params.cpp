// rxe1_params.cpp: see rxe1_params.h.
#include "rxe1_params.h"
#include "ih_params.h"
#include "json.h"
#include <cstring>

namespace igm {
namespace {
struct Enc {
    std::vector<uint8_t> v;
    Enc &lp(const uint8_t *p, size_t n) { for (int i = 0; i < 4; i++) v.push_back((uint8_t)((n >> (8 * i)) & 255)); v.insert(v.end(), p, p + n); return *this; }
    Enc &tag(const char *t) { return lp((const uint8_t *)t, std::strlen(t)); }
    Enc &le32(uint32_t x) { uint8_t b[4]; for (int i = 0; i < 4; i++) b[i] = (uint8_t)(x >> (8 * i)); return lp(b, 4); }
    Enc &le64(uint64_t x) { uint8_t b[8]; for (int i = 0; i < 8; i++) b[i] = (uint8_t)(x >> (8 * i)); return lp(b, 8); }
};
void H(const Enc &e, uint8_t out[32]) { ih_blake3_host(e.v.data(), e.v.size(), nullptr, out); }
bool pow2(uint64_t x) { return x && !(x & (x - 1)); }
}  // namespace

std::string Rxe1Params::variant() const {
    const char *m = mode == 0 ? "m" : mode == 1 ? "f" : mode == 3 ? "p" : "e";
    uint32_t sv = schema_version();
    std::string pre = sv == 2 ? "s2-" : sv == 3 ? "s3-" : sv == 5 ? "s5-" : "";
    std::string mid = (sv == 3 || sv == 5) ? "-g" + std::to_string(group) + "-u" + std::to_string(route) + "-x" + std::to_string(chunks) + "-y" + std::to_string(payload) : "";
    if (sv == 5) return pre + m + "-b" + std::to_string(blocks) + "-r" + std::to_string(rounds) + "-k" + std::to_string(registers) + mid;   // section 11: no dataset, parents or cache in the tag
    return pre + m + "-b" + std::to_string(blocks) + "-r" + std::to_string(rounds) + "-k" + std::to_string(registers) + mid + "-d" + std::to_string(dataset_bytes >> 20) + "-p" + std::to_string(parents) + "-c" + std::to_string(cache_bytes >> 20);
}

bool rxe1_parse_params(const std::string &json, Rxe1Params &out, std::string &err) {
    out = Rxe1Params();
    Json j; if (!Json::parse(json, j) || !j.is_object()) { err = "params document is not a JSON object"; return false; }
    bool hs = false, hm = false, hb = false, hr = false, hk = false, hd = false;
    if (j.has("schema")) { out.schema = (uint32_t)j["schema"].as_u64(); hs = true; }
    if (j.has("mode")) { out.mode = (uint32_t)j["mode"].as_u64(); hm = true; }
    if (j.has("blocks")) { out.blocks = (uint32_t)j["blocks"].as_u64(); hb = true; }
    if (j.has("rounds")) { out.rounds = (uint32_t)j["rounds"].as_u64(); hr = true; }
    if (j.has("registers")) { out.registers = (uint32_t)j["registers"].as_u64(); hk = true; }
    if (j.has("dataset_bytes")) { out.dataset_bytes = j["dataset_bytes"].as_u64(); hd = true; }
    if (j.has("parents")) out.parents = (uint32_t)j["parents"].as_u64();
    if (j.has("cache_bytes")) out.cache_bytes = j["cache_bytes"].as_u64();
    if (j.has("chain_id")) out.chain_id = j["chain_id"].as_u64();
    if (j.has("epoch_blocks")) out.epoch_blocks = j["epoch_blocks"].as_u64();
    if (j.has("test_only")) out.test_only = (uint32_t)j["test_only"].as_u64();
    if (j.has("group")) out.group = (uint32_t)j["group"].as_u64();
    if (j.has("route")) out.route = (uint32_t)j["route"].as_u64();
    if (j.has("chunks")) out.chunks = (uint32_t)j["chunks"].as_u64();
    if (j.has("payload_bytes")) out.payload = (uint32_t)j["payload_bytes"].as_u64();
    if (!(hs && hm && hb && hr && hk && hd)) { err = "schema, mode, blocks, rounds, registers and dataset_bytes are mandatory"; return false; }
    if (!(out.schema == 1 || out.schema == 2 || out.schema == 3 || out.schema == 5)) { err = "schema must be 1, 2, 3 or 5"; return false; }
    if (out.schema < 3 && (j.has("group") || j.has("route") || j.has("chunks") || j.has("payload_bytes"))) { err = "group, route, chunks and payload_bytes are schema 3 members"; return false; }
    if (out.schema == 3) {
        if (!(out.group == 8 || out.group == 16 || out.group == 32)) { err = "group must be 8, 16 or 32"; return false; }
        if (out.route > 3) { err = "route must be 0, 1, 2 or 3"; return false; }
        if (!(out.chunks == 1 || out.chunks == 2 || out.chunks == 4)) { err = "chunks must be 1, 2 or 4"; return false; }
        if (!(out.payload == 128 || out.payload == 256)) { err = "payload_bytes must be 128 or 256"; return false; }
        uint32_t cb = out.payload / out.chunks; if (!(cb == 64 || cb == 128 || cb == 256)) { err = "payload_bytes / chunks must be 64, 128 or 256"; return false; }
        if ((out.payload / 4) % out.group != 0) { err = "payload_bytes / 4 must be a multiple of group"; return false; }
    }
    if (out.mode > 3 || (out.mode == 3 && out.schema_version() < 3)) { err = "mode must be 0, 1 or 2 (3 under schema 3 and 5)"; return false; }
    if (out.schema_version() == 5 && (out.mode == 0 || out.mode == 4)) { err = "schema 5 admits modes 1, 2 and 3 only"; return false; }
    if (out.mode == 0 ? out.blocks != 0 : !(out.blocks == 1 || out.blocks == 2 || out.blocks == 4)) { err = "blocks: 0 under M, 1, 2 or 4 under F and E"; return false; }
    if (!(out.rounds == 32 || out.rounds == 64)) { err = "rounds must be 32 or 64"; return false; }
    if (!(out.registers == 8 || out.registers == 16 || (out.schema == 3 && out.registers == 4))) { err = "registers must be 8 or 16 (4 under schema 3)"; return false; }
    if (out.schema == 3 && (out.group * out.registers) % 8 != 0) { err = "group x registers must be a multiple of 8"; return false; }
    if (out.rounds < out.registers) { err = "rounds must be at least registers"; return false; }
    if (!(out.parents == 256 || out.parents == 512)) { err = "parents must be 256 or 512"; return false; }
    if (!pow2(out.cache_bytes) || out.cache_bytes < (1ull << 20)) { err = "cache_bytes must be a power of two of at least 1 MiB"; return false; }
    if (out.test_only > 1) { err = "test_only must be 0 or 1"; return false; }
    if (out.schema_version() == 5) {   // the memory-free execution lane (section 11): no dataset, no cache, no parents
        if (out.dataset_bytes != 0) { err = "schema 5: dataset_bytes must be 0"; return false; }
        out.cache_bytes = 0; out.parents = 0;
    } else
    if (out.test_only) { if (!pow2(out.dataset_bytes) || out.dataset_bytes < (16ull << 20) || out.dataset_bytes > (4ull << 30)) { err = "dataset_bytes (test) outside 16 MiB..4 GiB or not a power of two"; return false; } }
    else if (!(out.dataset_bytes == (2ull << 30) || out.dataset_bytes == (4ull << 30))) { err = "dataset_bytes must be 2 GiB or 4 GiB (test_only for smaller)"; return false; }
    if (out.epoch_blocks < 1 || out.epoch_blocks > 0x100000000ull) { err = "epoch_blocks outside 1..2^32"; return false; }
    if (out.test_only) out.schema = 0x54455354u + (out.schema - 1);   // the test marker (REFERENCE-NOTES.md section 2)
    return true;
}

void rxe1_spec_digest(uint8_t out[32]) {
    // the binding constant (REFERENCE-NOTES.md; dependencies.lock.json resolves it to source and vector hashes)
    static const char *s = "IgneumHash-R0A/v1/dataset-builder/SPEC-R0A-v0.md/revision-0.6/section-5.3";
    ih_blake3_host((const uint8_t *)s, std::strlen(s), nullptr, out);
}

void rxe1_record_bytes(const Rxe1Params &p, uint8_t out[88]) {
    int k = 0;
    if (p.schema_version() == 5) { std::memset(out, 0, 32); k = 32; }   // section 11: no dataset builder is bound; the digest field is 32 zero bytes
    else { rxe1_spec_digest(out); k = 32; }
    auto w32 = [&](uint32_t v) { for (int i = 0; i < 4; i++) out[k++] = (uint8_t)(v >> (8 * i)); };
    auto w64 = [&](uint64_t v) { for (int i = 0; i < 8; i++) out[k++] = (uint8_t)(v >> (8 * i)); };
    w64(p.dataset_bytes); w32(p.parents); w64(p.cache_bytes); w32(p.rounds); w32(p.registers); w32(p.blocks); w32(p.mode); w32(p.schema);
    if (p.schema_version() >= 3) { w32(p.group); w32(p.route); w32(p.chunks); w32(p.payload); }
}

void rxe1_pid(const Rxe1Params &p, uint8_t out[32]) { uint8_t rec[88]; rxe1_record_bytes(p, rec); Enc e; e.tag("RXE1/params").lp(rec, p.record_len()); H(e, out); }
void rxe1_route_seed(const Rxe1Params &p, const uint8_t pid[32], uint64_t epoch_number, const uint8_t E[32], uint8_t out[32]) {
    uint8_t cid[8]; for (int i = 0; i < 8; i++) cid[i] = (uint8_t)(p.chain_id >> (8 * i));
    Enc e; e.tag("RXE1/route").lp(cid, 8).lp(pid, 32).le64(epoch_number).lp(E, 32); H(e, out);
}
void rxe1_route_descriptors(const Rxe1Params &p, const uint8_t rs[32], std::vector<uint32_t> &out) {
    const uint32_t R = p.rounds, K = p.registers, G = p.group;
    out.assign((size_t)R * K, 0u);
    for (uint32_t j = 0; j < R; j++) for (uint32_t k = 0; k < K; k++) {
        Enc e; e.tag("RXE1/route-op").lp(rs, 32).le32(j).le32(k);
        uint8_t h[32]; H(e, h);
        uint32_t off = 1 + h[0] % (G - 1), src = h[1] % K, rot = 1 + h[2] % 31, op = h[3] % 2;
        out[(size_t)j * K + k] = off | (src << 8) | (rot << 16) | (op << 24);
    }
}
void rxe1_harness_epoch_seed(uint64_t chain_id, uint64_t epoch_number, uint8_t out[32]) { Enc e; e.tag("RXE1/harness-epoch").le64(chain_id).le64(epoch_number); H(e, out); }
void rxe1_program_seed(const Rxe1Params &p, const uint8_t pid[32], uint64_t epoch_number, const uint8_t E[32], uint8_t out[32]) {
    Enc e;
    if (p.mode == 1) e.tag("RXE1/fixed").lp(pid, 32);
    else { uint8_t cid[8]; for (int i = 0; i < 8; i++) cid[i] = (uint8_t)(p.chain_id >> (8 * i)); e.tag("RXE1/epoch").lp(cid, 8).lp(pid, 32).le64(epoch_number).lp(E, 32); }
    H(e, out);
}

void rxe1_schedule(const Rxe1Params &p, const uint8_t ps[32], std::vector<uint32_t> &out) {
    const uint32_t R = p.rounds, B = p.blocks, K = p.registers;
    out.assign((size_t)R * B * K, 0u);
    for (uint32_t j = 0; j < R; j++) for (uint32_t b = 0; b < B; b++) for (uint32_t d = 0; d < K; d++) {
        Enc e; e.tag("RXE1/op").lp(ps, 32).le32(j).le32(b).le32(d);
        uint8_t h[32]; H(e, h);   // X(.., 4) = the first 4 bytes of the extendable output = the first 4 bytes of the 32-byte hash
        uint32_t src1 = (d + 1 + h[0] % (K - 1)) % K, src2;
        if (p.schema_version() >= 2) { uint32_t r1 = (src1 + K - d - 1) % K; uint32_t t = h[1] % (K - 2); if (t >= r1) t++; src2 = (d + 1 + t) % K; }   // schema 3 keeps schema 2's draw (SPEC-TEXT-v0.2-A2A3.md section 1)
        else src2 = (d + 1 + h[1] % (K - 1)) % K;
        uint32_t rot = 1 + h[2] % 31, op = (d + h[3] % 4) % 4;
        out[((size_t)j * B + b) * K + d] = src1 | (src2 << 8) | (rot << 16) | (op << 24);
    }
}

}  // namespace igm
