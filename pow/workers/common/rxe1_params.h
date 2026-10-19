// rxe1_params.h: the host side of RXE1 (docs/igneumhash/rxe1/SPEC-TEXT-v0.1.md sections 6 and 8, the harness
// conventions of REFERENCE-NOTES.md): the parameter record and its document, pid, the program seed, the schedule
// (schema 1: the text verbatim; schema 2: the src2 rule of the project's decision), the harness epoch seed. Every hash
// input here is under one BLAKE3 chunk; the framing is LP(x) = u32le(len) || x on every field, integers at their
// width. GPU engineer B, 10 October 2026.
#pragma once
#include <cstdint>
#include <string>
#include <vector>

namespace igm {

struct Rxe1Params {
    uint64_t dataset_bytes = 0, cache_bytes = 32ull << 20, chain_id = 7310, epoch_blocks = 4096;
    uint32_t parents = 256, rounds = 0, registers = 0, blocks = 0, mode = 0, schema = 0, test_only = 0;
    uint32_t group = 16, route = 1, chunks = 1, payload = 128;   // schema 3 (SPEC-TEXT-v0.2-A2A3.md); the v0.1 point under schema 1 and 2
    uint32_t n_items() const { return (uint32_t)(dataset_bytes / 128); }
    uint32_t n_cache() const { return (uint32_t)(cache_bytes / 128); }
    uint32_t schema_version() const { return schema >= 0x54455354u ? schema - 0x54455354u + 1 : schema; }
    size_t record_len() const { return schema_version() >= 3 ? 88 : 72; }
    uint32_t chunk_bytes() const { return payload / chunks; }
    uint32_t n_regions() const { return dataset_bytes ? (uint32_t)(dataset_bytes / chunk_bytes()) : 1u; }
    bool memory_free() const { return schema_version() == 5; }
    uint32_t wpl() const { return payload / 4 / group; }
    std::string variant() const;
};
bool rxe1_parse_params(const std::string &json, Rxe1Params &out, std::string &err);
void rxe1_record_bytes(const Rxe1Params &p, uint8_t out[88]);   // record_len() bytes written
void rxe1_pid(const Rxe1Params &p, uint8_t out[32]);
void rxe1_spec_digest(uint8_t out[32]);
void rxe1_harness_epoch_seed(uint64_t chain_id, uint64_t epoch_number, uint8_t out[32]);
void rxe1_program_seed(const Rxe1Params &p, const uint8_t pid[32], uint64_t epoch_number, const uint8_t E[32], uint8_t out[32]);
// descriptors[(j * B + b) * K + d] = q0 | q1 << 8 | q2 << 16 | q3 << 24 resolved to src1 | src2 << 8 | rotate << 16 | opcode << 24
void rxe1_schedule(const Rxe1Params &p, const uint8_t program_seed[32], std::vector<uint32_t> &out);
// route 2 (schema 3): rseed = H("RXE1/route", [chain_id, pid, LE64(epoch), E]); descriptors[j * K + k] = off | src << 8 | rot << 16 | op << 24
void rxe1_route_seed(const Rxe1Params &p, const uint8_t pid[32], uint64_t epoch_number, const uint8_t E[32], uint8_t out[32]);
void rxe1_route_descriptors(const Rxe1Params &p, const uint8_t rseed[32], std::vector<uint32_t> &out);

}  // namespace igm
