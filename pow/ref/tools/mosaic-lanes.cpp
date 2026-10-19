// mosaic-lanes: the Mosaic-VF lane portfolio's identifiers (docs/igneumhash/mosaic/LANES.md) from the registered
// lane documents (LANES.jsonl, one JSON object per line). Prints one table row per lane document.
//
//   mosaic-lanes <LANES.jsonl>
//
// candidate_pid: the candidate's own parameter identity (igneumhash P = BLAKE3(params bytes); rxe1 pid; forgeb1 pid;
// 32 zero bytes for a control without a params document). lane_pid = BLAKE3(tag("Mosaic/lane/v0") || LE32(lane)
// || LE32(parameter_version) || tag(candidate_id) || candidate_pid); for the composite, candidate_pid =
// BLAKE3(tag("Mosaic/composite/v0") || the member lane_pids in the members' order). tag(T) = u8(len(T)) || T.

#include "blake3.h"
#include "forgeb1.h"
#include "igneumhash.h"
#include "rxe1.h"

#include <cstdio>
#include <cstring>
#include <fstream>
#include <map>
#include <string>
#include <vector>

namespace {
std::string hex(const uint8_t* p, size_t n) { static const char* d = "0123456789abcdef"; std::string s; for (size_t i = 0; i < n; ++i) { s += d[p[i] >> 4]; s += d[p[i] & 15]; } return s; }
std::string field(const std::string& line, const char* key) {
    std::string k = std::string("\"") + key + "\":";
    size_t p = line.find(k); if (p == std::string::npos) return "";
    p += k.size(); while (p < line.size() && line[p] == ' ') ++p;
    if (line[p] == '"') { size_t q = line.find('"', p + 1); return line.substr(p + 1, q - p - 1); }
    if (line[p] == '{' || line[p] == '[') { char o = line[p], c = o == '{' ? '}' : ']'; int d = 0; size_t q = p; for (; q < line.size(); ++q) { if (line[q] == o) ++d; else if (line[q] == c && --d == 0) break; } return line.substr(p, q - p + 1); }
    size_t q = line.find_first_of(",}", p); return line.substr(p, q - p);
}
struct enc { std::vector<uint8_t> v; void tag(const std::string& t) { v.push_back(uint8_t(t.size())); v.insert(v.end(), t.begin(), t.end()); } void le32(uint32_t x) { for (int i = 0; i < 4; ++i) v.push_back(uint8_t(x >> (8 * i))); } void raw(const uint8_t* p, size_t n) { v.insert(v.end(), p, p + n); } };
void H(const enc& e, uint8_t out[32]) { blake3_hasher h; blake3_hasher_init(&h); blake3_hasher_update(&h, e.v.data(), e.v.size()); blake3_hasher_finalize(&h, out, 32); }
}  // namespace

int main(int argc, char** argv) {
    if (argc < 2) { std::fprintf(stderr, "usage: mosaic-lanes <LANES.jsonl>\n"); return 2; }
    std::ifstream in(argv[1]); if (!in) { std::fprintf(stderr, "cannot read %s\n", argv[1]); return 2; }
    std::map<std::pair<uint32_t, uint32_t>, std::vector<uint8_t>> lane_pids;
    std::string line;
    std::printf("| lane | pv | role | name | candidate | candidate_pid | lane_pid |\n|---|---|---|---|---|---|---|\n");
    while (std::getline(in, line)) {
        if (line.find("\"lane\"") == std::string::npos) continue;
        const uint32_t lane = uint32_t(std::stoul(field(line, "lane"))), pv = uint32_t(std::stoul(field(line, "parameter_version")));
        const std::string cand = field(line, "candidate"), params = field(line, "params"), name = field(line, "name"), role = field(line, "role");
        uint8_t cpid[32]; std::memset(cpid, 0, 32);
        if (cand == "igneumhash") { ignh_params p; if (ignh_params_parse(params.c_str(), &p) != IGNPOW_OK) { std::fprintf(stderr, "%s: params rejected\n", name.c_str()); return 1; } ignh_params_hash(&p, cpid); }
        else if (cand == "rxe1") { rxe1_params p; if (rxe1_params_parse(params.c_str(), &p) != IGNPOW_OK) { std::fprintf(stderr, "%s: params rejected\n", name.c_str()); return 1; } rxe1_pid(&p, cpid); }
        else if (cand == "forgeb1") { b1_params p; if (b1_params_parse(params.c_str(), &p) != IGNPOW_OK) { std::fprintf(stderr, "%s: params rejected\n", name.c_str()); return 1; } b1_pid(&p, cpid); }
        else if (cand == "mosaic-composite") {
            enc e; e.tag("Mosaic/composite/v0");
            std::string m = field(line, "members");
            for (size_t p = 0; (p = m.find('[', p + 1)) != std::string::npos;) {
                uint32_t a = 0, b = 0; if (std::sscanf(m.c_str() + p, "[%u, %u]", &a, &b) != 2) { std::fprintf(stderr, "%s: members\n", name.c_str()); return 1; }
                auto it = lane_pids.find({a, b}); if (it == lane_pids.end()) { std::fprintf(stderr, "%s: member (%u, %u) not defined above\n", name.c_str(), a, b); return 1; }
                e.raw(it->second.data(), 32);
            }
            H(e, cpid);
        } else if (cand != "kawpow-0.9.4" && cand != "fishhash-1" && cand != "autolykos-2") { std::fprintf(stderr, "%s: unknown candidate %s\n", name.c_str(), cand.c_str()); return 1; }
        enc e; e.tag("Mosaic/lane/v0"); e.le32(lane); e.le32(pv); e.tag(cand); e.raw(cpid, 32);
        uint8_t lp[32]; H(e, lp);
        lane_pids[{lane, pv}] = std::vector<uint8_t>(lp, lp + 32);
        std::printf("| %u | %u | %s | %s | %s | %s | %s |\n", lane, pv, role.c_str(), name.c_str(), cand.c_str(), hex(cpid, 32).c_str(), hex(lp, 32).c_str());
    }
    return 0;
}
