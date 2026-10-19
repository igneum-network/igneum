// mosaic-gate0: the Mosaic-VF v0.2 gate 0 reference tool (docs/igneumhash/mosaic/v0.2/GATE0-VECTORS.md, text
// revision 1): the quota score, the quota transition, the continuity reservoir and the fee reservoir with its
// overflow, as exact integer procedures, and their vectors. Written from the text; the Python port is the second
// reader and never reads this file.
//
//   mosaic-gate0 gen <out_dir> [seed]      writes score.json, transition.json, reservoir.json, overflow.json
//   mosaic-gate0 selftest                   the model checks of the text's section 2

#include <algorithm>
#include <cmath>
#include <cstdint>
#include <cstdio>
#include <cstring>
#include <map>
#include <string>
#include <vector>

namespace {
typedef unsigned __int128 u128;
constexpr uint64_t Q = 10000, A_NUM = 1, E_NUM = 2, E_DEN = 100;
constexpr uint64_t WINDOW = 1000;
constexpr uint64_t EMISSION = 1000000, D_REF = 1000000, STABLE = 4, RELEASE_CAP = 50000, TIMEOUT = 200;
constexpr uint64_t IMM_PCT = 20, FEE_CAP = 30000, FEE_WINDOW = 8, FEE_TIMEOUT = 40;
const std::vector<uint64_t> kDoc = {2375, 2375, 2375, 2375, 500};

struct rng { uint64_t s; explicit rng(uint64_t seed) : s(seed ? seed : 1) {} uint64_t next() { s ^= s >> 12; s ^= s << 25; s ^= s >> 27; return s * 0x2545F4914F6CDD1DULL; } };

std::string u128s(u128 v) { if (v == 0) return "0"; std::string s; while (v) { s += char('0' + int(v % 10)); v /= 10; } std::reverse(s.begin(), s.end()); return s; }
std::string dbl(double d) { char b[64]; std::snprintf(b, sizeof b, "%.17g", d); return b; }

bool doc_ok(const std::vector<uint64_t>& q) { if (q.size() < 2 || q.size() > 8) return false; uint64_t s = 0; for (uint64_t x : q) { if (x == 0) return false; s += x; } return s == Q; }

// section 2
size_t min_lane(const std::vector<uint64_t>& W, const std::vector<uint64_t>& q) {
    size_t m = 0;
    for (size_t i = 1; i < W.size(); ++i) if ((u128)W[i] * q[m] < (u128)W[m] * q[i]) m = i;
    return m;
}
void score(const std::vector<uint64_t>& W, const std::vector<uint64_t>& q, size_t& m, u128& num, u128& den, double& sf) {
    m = min_lane(W, q);
    u128 sum = 0; for (uint64_t w : W) sum += w;
    num = (u128)A_NUM * E_DEN * Q * W[m] + (u128)E_NUM * q[m] * sum;
    den = (u128)E_DEN * q[m];
    double mn = 0; double ds = 0;
    for (size_t i = 0; i < W.size(); ++i) { double u = (double)W[i] * (double)Q / (double)q[i]; if (i == 0 || u < mn) mn = u; ds += (double)W[i]; }
    sf = (double)A_NUM * mn + 0.02 * ds;
}
int cmp(u128 n1, u128 d1, u128 n2, u128 d2) { u128 a = n1 * d2, b = n2 * d1; return a < b ? -1 : a > b ? 1 : 0; }  // vectors keep products inside 128 bits
double multiplier(uint64_t qi) { return (1.0 + 0.02) / (0.02 * ((double)qi / 10000.0)); }  // the package's order (text section 2)
double outage(uint64_t qj) { return 0.02 * (1.0 - (double)qj / (double)Q) / (1.0 + 0.02); }
double portfolio(const std::vector<uint64_t>& q, const std::vector<double>& a) { double s = 0; for (size_t i = 0; i < q.size(); ++i) s += (double)q[i] / (double)Q / a[i]; return 1.0 / s; }

std::string vec(const std::vector<uint64_t>& v) { std::string s = "["; for (size_t i = 0; i < v.size(); ++i) { if (i) s += ", "; s += std::to_string(v[i]); } return s + "]"; }

int gen_score(const std::string& dir, uint64_t seed) {
    rng r(seed);
    std::vector<std::vector<uint64_t>> works;
    works.push_back({1000, 1000, 1000, 1000, 1000});
    works.push_back(kDoc);
    works.push_back({5000, 0, 5000, 5000, 1000});
    works.push_back({1, 1, 1, 1, 1000000000});
    for (int i = 0; i < 60; ++i) { std::vector<uint64_t> w(5); for (auto& x : w) x = r.next() % 2000000; works.push_back(w); }
    FILE* f = std::fopen((dir + "/score.json").c_str(), "w"); if (!f) return 2;
    std::fprintf(f, "{\"class\": \"score\", \"text\": \"GATE0-VECTORS.md revision 1\", \"seed\": %llu, \"quotas\": %s, \"vectors\": [\n", (unsigned long long)seed, vec(kDoc).c_str());
    std::vector<std::pair<u128, u128>> sc;
    for (size_t k = 0; k < works.size(); ++k) {
        size_t m; u128 num, den; double sf; score(works[k], kDoc, m, num, den, sf); sc.push_back({num, den});
        std::fprintf(f, "%s{\"W\": %s, \"m\": %zu, \"num\": \"%s\", \"den\": \"%s\", \"S_f\": %s}", k ? ",\n" : "", vec(works[k]).c_str(), m, u128s(num).c_str(), u128s(den).c_str(), dbl(sf).c_str());
    }
    std::fprintf(f, "\n], \"compare\": [");
    for (size_t k = 0; k < 20; ++k) std::fprintf(f, "%s%d", k ? ", " : "", cmp(sc[k].first, sc[k].second, sc[k + 1].first, sc[k + 1].second));
    std::fprintf(f, "], \"checks\": {\"S_q_q\": [\"%s\", \"%s\"], \"multiplier\": [", u128s(sc[1].first).c_str(), u128s(sc[1].second).c_str());
    for (size_t i = 0; i < kDoc.size(); ++i) std::fprintf(f, "%s%s", i ? ", " : "", dbl(multiplier(kDoc[i])).c_str());
    std::fprintf(f, "], \"outage\": [");
    for (size_t i = 0; i < kDoc.size(); ++i) std::fprintf(f, "%s%s", i ? ", " : "", dbl(outage(kDoc[i])).c_str());
    std::fprintf(f, "], \"portfolio_2_2\": %s, \"portfolio_1_9\": %s}}\n", dbl(portfolio(kDoc, {2.2, 2.2, 2.2, 2.2, 200})).c_str(), dbl(portfolio(kDoc, {1.9, 1.9, 1.9, 1.9, 200})).c_str());
    std::fclose(f);
    return 0;
}

// section 3
struct entry { uint64_t ratified, activation; std::vector<uint64_t> doc; };
int gen_transition(const std::string& dir) {
    std::vector<entry> menu = {
        {0, 0, kDoc},                                   // 0 genesis
        {1000, 3000, {2500, 2500, 2500, 2500, 0}},        // 1 invalid: a zero quota
        {1000, 3000, {2400, 2400, 2400, 2400, 400}},      // 2 valid
        {1500, 3000, {2000, 2000, 2000, 2000, 2000}},     // 3 invalid: same activation as entry 2 (one change at a time)
        {2000, 3500, {2000, 2000, 2000, 2000, 2000}},     // 4 invalid: not a multiple of WINDOW
        {3000, 4000, {2000, 2000, 2000, 2000, 2000}},     // 5 invalid: ratified fewer than two windows ahead
        {2000, 4000, {2000, 2000, 2000, 2000, 2000}},     // 6 valid
        {1000, 3000, {2500, 2500, 2500, 2500, 0}},        // 7 invalid: activation at or before the latest valid
        {0, 2000, {2600, 2600, 2600, 2100, 100}},         // 8 invalid: activation before the latest valid (entry 6)
        {3000, 6000, {2600, 2600, 2600, 2100, 101}},      // 9 invalid: the sum is not Q
        {3000, 6000, {2600, 2600, 2600, 2100, 100}},      // 10 valid
        {4000, 6000, {3000, 3000, 2000, 1000, 1000}},     // 11 invalid: same activation as entry 10
    };
    std::vector<bool> valid(menu.size(), false);
    uint64_t latest = 0; bool have = false;
    for (size_t k = 0; k < menu.size(); ++k) {
        const entry& e = menu[k];
        bool ok = k == 0 || (e.activation % WINDOW == 0 && e.activation >= e.ratified + 2 * WINDOW && doc_ok(e.doc) && (!have || e.activation > latest));
        valid[k] = ok; if (ok) { latest = e.activation; have = true; }
    }
    FILE* f = std::fopen((dir + "/transition.json").c_str(), "w"); if (!f) return 2;
    std::fprintf(f, "{\"class\": \"transition\", \"text\": \"GATE0-VECTORS.md revision 1\", \"window\": %llu, \"menu\": [\n", (unsigned long long)WINDOW);
    for (size_t k = 0; k < menu.size(); ++k) std::fprintf(f, "%s{\"ratified\": %llu, \"activation\": %llu, \"doc\": %s, \"valid\": %s}", k ? ",\n" : "", (unsigned long long)menu[k].ratified, (unsigned long long)menu[k].activation, vec(menu[k].doc).c_str(), valid[k] ? "true" : "false");
    std::fprintf(f, "\n], \"active\": [");
    const uint64_t heights[24] = {0, 1, 999, 1000, 2000, 2999, 3000, 3001, 3500, 3999, 4000, 4001, 4999, 5000, 5999, 6000, 6001, 7000, 9999, 10000, 100000, 123456, 1000000, 18446744073709551615ULL};
    for (int i = 0; i < 24; ++i) {
        size_t act = 0;
        for (size_t k = 0; k < menu.size(); ++k) if (valid[k] && menu[k].activation <= heights[i] && menu[k].activation >= menu[act].activation) act = k;
        std::fprintf(f, "%s{\"h\": %llu, \"entry\": %zu}", i ? ", " : "", (unsigned long long)heights[i], act);
    }
    std::fprintf(f, "]}\n");
    std::fclose(f);
    return 0;
}

// sections 4 and 5 over one chain
struct block { uint64_t h, lane, miner, D, F; };
struct lane_res { uint64_t balance = 0; bool weak = false; std::map<uint64_t, uint64_t> table; uint64_t stable = 0; bool has_expiry = false; uint64_t expiry = 0; };
struct lane_fee { uint64_t balance = 0; bool has_last = false; uint64_t last = 0; std::vector<uint64_t> recent; };

int gen_chain(const std::string& dir, uint64_t seed) {
    rng r(seed);
    const size_t n = kDoc.size();
    std::vector<block> chain;
    for (uint64_t h = 1; h <= 400; ++h) {
        uint64_t a = r.next(), b = r.next(), c = r.next(), d = r.next();
        uint64_t lane = a % 16; lane = lane < 15 ? lane % 4 : 4;  // lane 4 rare
        chain.push_back({h, lane, b % 4, D_REF * (40 + c % 121) / 100, d % 200000});
    }
    std::vector<lane_res> R(n); std::vector<lane_fee> Fv(n);
    FILE* fr = std::fopen((dir + "/reservoir.json").c_str(), "w"); FILE* fo = std::fopen((dir + "/overflow.json").c_str(), "w"); if (!fr || !fo) return 2;
    std::fprintf(fr, "{\"class\": \"reservoir\", \"text\": \"GATE0-VECTORS.md revision 1\", \"seed\": %llu, \"params\": {\"EMISSION\": %llu, \"D_REF\": %llu, \"STABLE\": %llu, \"RELEASE_CAP\": %llu, \"TIMEOUT\": %llu}, \"blocks\": [\n",
                 (unsigned long long)seed, (unsigned long long)EMISSION, (unsigned long long)D_REF, (unsigned long long)STABLE, (unsigned long long)RELEASE_CAP, (unsigned long long)TIMEOUT);
    std::fprintf(fo, "{\"class\": \"overflow\", \"text\": \"GATE0-VECTORS.md revision 1\", \"seed\": %llu, \"params\": {\"IMM_PCT\": %llu, \"FEE_CAP\": %llu, \"FEE_WINDOW\": %llu, \"FEE_TIMEOUT\": %llu}, \"blocks\": [\n",
                 (unsigned long long)seed, (unsigned long long)IMM_PCT, (unsigned long long)FEE_CAP, (unsigned long long)FEE_WINDOW, (unsigned long long)FEE_TIMEOUT);
    for (size_t k = 0; k < chain.size(); ++k) {
        const block& bl = chain[k]; const uint64_t h = bl.h, i = bl.lane, x = bl.miner, D = bl.D;
        // section 4
        std::string burned = "[", released = "[";
        for (size_t j = 0; j < n; ++j) if (R[j].balance > 0 && !R[j].weak && R[j].has_expiry && R[j].expiry <= h) {
            burned += (burned.size() > 1 ? ", " : "") + std::string("[") + std::to_string(j) + ", " + std::to_string(R[j].balance) + "]";
            R[j].balance = 0; R[j].table.clear(); R[j].has_expiry = false;
        }
        const uint64_t B = (uint64_t)((u128)EMISSION * kDoc[i] / Q);
        const uint64_t imm = D >= D_REF ? B : (uint64_t)((u128)B * D / D_REF);
        const uint64_t unpaid = B - imm;
        lane_res& L = R[i];
        if (unpaid > 0) {
            if (!L.weak && L.balance > 0) { burned += (burned.size() > 1 ? ", " : "") + std::string("[") + std::to_string(i) + ", " + std::to_string(L.balance) + "]"; L.balance = 0; L.table.clear(); }
            L.weak = true; L.balance += unpaid; L.table[x] += D; L.stable = 0; L.has_expiry = false;
        } else {
            if (L.weak) { L.weak = false; L.stable = 0; L.has_expiry = true; L.expiry = h + TIMEOUT; }
            if (L.balance > 0) {
                L.stable += 1;
                if (L.stable > STABLE) {
                    const uint64_t rel = std::min(L.balance, RELEASE_CAP);
                    u128 tot = 0; for (auto& kv : L.table) tot += kv.second;
                    for (auto& kv : L.table) { const uint64_t pay = (uint64_t)((u128)rel * kv.second / tot); L.balance -= pay; released += (released.size() > 1 ? ", " : "") + std::string("[") + std::to_string(kv.first) + ", " + std::to_string(pay) + "]"; }
                }
            }
        }
        std::fprintf(fr, "%s{\"h\": %llu, \"lane\": %llu, \"miner\": %llu, \"D\": %llu, \"B\": %llu, \"imm\": %llu, \"unpaid\": %llu, \"released\": %s], \"burned\": %s], \"balance_after\": %llu}",
                     k ? ",\n" : "", (unsigned long long)h, (unsigned long long)i, (unsigned long long)x, (unsigned long long)D, (unsigned long long)B, (unsigned long long)imm, (unsigned long long)unpaid, released.c_str(), burned.c_str(), (unsigned long long)L.balance);
        // section 5
        const uint64_t F = bl.F;
        std::string fburn = "[";
        for (size_t j = 0; j < n; ++j) if (Fv[j].balance > 0 && Fv[j].has_last && Fv[j].last + FEE_TIMEOUT <= h) { fburn += (fburn.size() > 1 ? ", " : "") + std::string("[") + std::to_string(j) + ", " + std::to_string(Fv[j].balance) + "]"; Fv[j].balance = 0; }
        const uint64_t fimm = (uint64_t)((u128)F * IMM_PCT / 100); const uint64_t Rm = F - fimm;
        std::vector<uint64_t> shares(n); uint64_t ssum = 0;
        for (size_t j = 0; j < n; ++j) { shares[j] = (uint64_t)((u128)Rm * kDoc[j] / Q); ssum += shares[j]; Fv[j].balance += shares[j]; }
        const uint64_t dust = Rm - ssum;
        lane_fee& G = Fv[i];
        G.recent.push_back(D); if (G.recent.size() > FEE_WINDOW) G.recent.erase(G.recent.begin());
        u128 w = 0; for (uint64_t v : G.recent) w += v;
        const uint64_t due = (uint64_t)((u128)G.balance * D / w);
        const uint64_t rel = std::min(due, FEE_CAP); const uint64_t over = due - rel;
        G.balance -= rel; G.has_last = true; G.last = h;
        std::fprintf(fo, "%s{\"h\": %llu, \"lane\": %llu, \"assembler\": %llu, \"D\": %llu, \"F\": %llu, \"imm\": %llu, \"dust\": %llu, \"shares\": %s, \"due\": %llu, \"rel\": %llu, \"overflow\": %llu, \"fee_balance_after\": %llu, \"fee_burned\": %s], \"assembler_total\": %llu}",
                     k ? ",\n" : "", (unsigned long long)h, (unsigned long long)i, (unsigned long long)x, (unsigned long long)D, (unsigned long long)F, (unsigned long long)fimm, (unsigned long long)dust, vec(shares).c_str(), (unsigned long long)due, (unsigned long long)rel, (unsigned long long)over, (unsigned long long)G.balance, fburn.c_str(), (unsigned long long)(fimm + dust + rel));
    }
    std::fprintf(fr, "\n]}\n"); std::fprintf(fo, "\n]}\n");
    std::fclose(fr); std::fclose(fo);
    return 0;
}

int selftest() {
    size_t m; u128 num, den; double sf; score(kDoc, kDoc, m, num, den, sf);
    if (num != (u128)10200 * den || sf != 10200.0) { std::fprintf(stderr, "S(q,q)\n"); return 1; }
    if (multiplier(500) != 1020.0 || multiplier(2000) != 255.0) { std::fprintf(stderr, "multiplier\n"); return 1; }
    // doubles compare by value (the text's section 6); the literals are the package's shortest-repr forms of the same doubles
    if (multiplier(2375) != 214.73684210526318) { std::fprintf(stderr, "multiplier 2375 %s\n", dbl(multiplier(2375)).c_str()); return 1; }
    if (portfolio(kDoc, {2.2, 2.2, 2.2, 2.2, 200}) != 2.314449529219926) { std::fprintf(stderr, "portfolio %s\n", dbl(portfolio(kDoc, {2.2, 2.2, 2.2, 2.2, 200})).c_str()); return 1; }
    if (portfolio(kDoc, {1.9, 1.9, 1.9, 1.9, 200}) != 1.999000499750125) { std::fprintf(stderr, "portfolio 1.9\n"); return 1; }
    std::printf("selftest: ok\n");
    return 0;
}
}  // namespace

int main(int argc, char** argv) {
    if (argc >= 2 && std::strcmp(argv[1], "selftest") == 0) return selftest();
    if (argc >= 3 && std::strcmp(argv[1], "gen") == 0) {
        if (selftest()) return 3;
        const uint64_t seed = argc >= 4 ? std::strtoull(argv[3], nullptr, 10) : 20261010ULL;
        int rc = gen_score(argv[2], seed); if (rc) return rc;
        rc = gen_transition(argv[2]); if (rc) return rc;
        return gen_chain(argv[2], seed);
    }
    std::fprintf(stderr, "usage: mosaic-gate0 gen <out_dir> [seed] | selftest\n");
    return 2;
}
