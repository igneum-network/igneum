// fast-oracle: the ADVERSARIAL oracle of GUARD-C-CLOSURE.md 1.4 (O3), the project's order of 11 Oct 2026 (the outside review's gate-14 reading):
// an oracle bounded by the wire and the compute rather than by the reference implementation's request loop, so the 10 Gb/s and
// co-located points of the split-execution row read the link or the keyed compute as the bind. The reference bond-oracle
// (pow/ref/tools/bond-oracle.cpp) stays the oracle of record for exactness; this one speaks the SAME wire form and computes every
// share with the SAME reference call (ignh_bond_share; the work seed by the reference's own BLAKE3 form), and differs only in how
// it moves bytes: each connection's thread reads a chunk of up to 64 KiB of pipelined requests per recv, answers the whole chunk
// with one send, and carries a partial request over to the next read; one thread per connection, any number of connections.
//
//   fast-oracle serve <port> <sk-file (32-byte hex)> <params.json> <C hex 16> <V> <P hex 64> <H hex 64> [report_s]
//
// Wire form (the reference's): request = u64 nonce LE || u32 i LE || m_i, with m_i ABSENT at i 0 (the oracle computes
// m_0 = S = BLAKE3(tag(T_SEED) || C || V || P || H || u64be(N)) from the template it holds): 12 or 44 bytes; answer = s_i, 32 bytes,
// in request order. Every report_s seconds (default 5) the same JSON report line as the reference's (requests per second, bits
// per second each way, compressions per second, total requests) to stderr. The secret is read from a file by path and never
// printed. Nothing here is served to anyone but the harness.
#include "igneumhash.h"
#include "blake3.h"
#include <arpa/inet.h>
#include <netinet/in.h>
#include <netinet/tcp.h>
#include <sys/socket.h>
#include <unistd.h>
#include <atomic>
#include <chrono>
#include <csignal>
#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <fstream>
#include <string>
#include <thread>
#include <vector>

namespace {

bool unhex(const std::string& s, uint8_t* out, size_t n) {
    std::string t = s;
    if (t.rfind("0x", 0) == 0) t = t.substr(2);
    if (t.size() != 2 * n) return false;
    for (size_t i = 0; i < n; ++i) {
        unsigned v;
        if (std::sscanf(t.c_str() + 2 * i, "%2x", &v) != 1) return false;
        out[i] = uint8_t(v);
    }
    return true;
}

bool write_all(int fd, const uint8_t* p, size_t n) {
    while (n) {
        ssize_t r = ::send(fd, p, n, MSG_NOSIGNAL);
        if (r <= 0) return false;
        p += r; n -= size_t(r);
    }
    return true;
}

// The work seed S of SPEC-R0A-v0.md section 2, the reference oracle's form, byte for byte.
struct Template { uint8_t C[8]; uint32_t V; uint8_t P[32]; uint8_t H[32]; };
void work_seed(const Template& t, uint64_t nonce, uint8_t out[32]) {
    static const char kTag[] = "IgneumHash/R0A/seed";
    const uint8_t len = uint8_t(sizeof kTag - 1);
    uint8_t vb[4] = {uint8_t(t.V >> 24), uint8_t(t.V >> 16), uint8_t(t.V >> 8), uint8_t(t.V)};
    uint8_t nb[8]; for (int i = 0; i < 8; ++i) nb[i] = uint8_t(nonce >> (56 - 8 * i));
    blake3_hasher h; blake3_hasher_init(&h);
    blake3_hasher_update(&h, &len, 1); blake3_hasher_update(&h, kTag, len);
    blake3_hasher_update(&h, t.C, 8); blake3_hasher_update(&h, vb, 4); blake3_hasher_update(&h, t.P, 32); blake3_hasher_update(&h, t.H, 32); blake3_hasher_update(&h, nb, 8);
    blake3_hasher_finalize(&h, out, 32);
}

std::atomic<uint64_t> g_req{0}, g_in{0}, g_out{0}, g_comp{0}, g_conns{0};

// One connection: chunked reads of the pipelined stream, the chunk's answers in one send. A request is 12 bytes at i 0 and
// 44 otherwise; a chunk may end inside a request, and the tail is carried to the next read.
void serve_conn(int fd, const uint8_t sk[32], const Template t) {
    ++g_conns;
    int one = 1; ::setsockopt(fd, IPPROTO_TCP, TCP_NODELAY, &one, sizeof one);
    const size_t CH = 64 * 1024;
    std::vector<uint8_t> buf(CH + 44), ans(CH / 12 * 32 + 64);
    size_t pending = 0;
    uint64_t req = 0, in = 0, out = 0, comp = 0;
    for (;;) {
        const ssize_t r = ::recv(fd, buf.data() + pending, CH - pending, 0);
        if (r <= 0) break;
        size_t have = pending + size_t(r), pos = 0, na = 0;
        while (have - pos >= 12) {
            const uint8_t* p = buf.data() + pos;
            uint64_t nonce; uint32_t i; std::memcpy(&nonce, p, 8); std::memcpy(&i, p + 8, 4);
            uint8_t m[32];
            if (i == 0) { work_seed(t, nonce, m); ++comp; pos += 12; in += 12; }
            else { if (have - pos < 44) break; std::memcpy(m, p + 12, 32); pos += 44; in += 44; }
            ignh_bond_share(sk, i, m, ans.data() + na * 32);
            ++na; ++comp;
        }
        if (na && !write_all(fd, ans.data(), na * 32)) break;
        req += na; out += na * 32;
        pending = have - pos;
        if (pending) std::memmove(buf.data(), buf.data() + pos, pending);
        g_req.fetch_add(req, std::memory_order_relaxed); g_in.fetch_add(in, std::memory_order_relaxed); g_out.fetch_add(out, std::memory_order_relaxed); g_comp.fetch_add(comp, std::memory_order_relaxed);
        req = in = out = comp = 0;
    }
    ::close(fd);
    --g_conns;
}

int serve(int argc, char** argv) {
    if (argc < 9) return 2;
    const int port = std::atoi(argv[2]);
    uint8_t sk[32];
    { std::ifstream f(argv[3]); std::string hex; f >> hex; if (!unhex(hex, sk, 32)) { std::fprintf(stderr, "fast-oracle: the secret file is not 32 hex bytes\n"); return 2; } }
    Template t{};
    if (!unhex(argv[5], t.C, 8)) { std::fprintf(stderr, "fast-oracle: C is 8 hex bytes (u64be chain id)\n"); return 2; }
    t.V = uint32_t(std::atoi(argv[6]));
    if (!unhex(argv[7], t.P, 32) || !unhex(argv[8], t.H, 32)) { std::fprintf(stderr, "fast-oracle: P and H are 32 hex bytes\n"); return 2; }
    const int report_s = argc > 9 ? std::atoi(argv[9]) : 5;
    uint8_t pk[32]; ignh_bond_pk(sk, pk);
    std::fprintf(stderr, "fast-oracle: pk ");
    for (int k = 0; k < 32; ++k) std::fprintf(stderr, "%02x", pk[k]);
    std::fprintf(stderr, " on port %d (the adversarial oracle: chunked reads, one send per chunk, a thread per connection; every share by the reference's ignh_bond_share; params %s read for the record only)\n", port, argv[4]);
    signal(SIGPIPE, SIG_IGN);
    int ls = ::socket(AF_INET, SOCK_STREAM, 0);
    int one = 1; ::setsockopt(ls, SOL_SOCKET, SO_REUSEADDR, &one, sizeof one);
    sockaddr_in a{}; a.sin_family = AF_INET; a.sin_addr.s_addr = htonl(INADDR_ANY); a.sin_port = htons(uint16_t(port));
    if (::bind(ls, (sockaddr*)&a, sizeof a) != 0 || ::listen(ls, 256) != 0) { std::perror("fast-oracle: bind"); return 1; }
    std::thread([report_s]() {
        uint64_t pr = 0, pi = 0, po = 0, pc = 0;
        for (;;) {
            std::this_thread::sleep_for(std::chrono::seconds(report_s));
            const uint64_t r = g_req.load(), i = g_in.load(), o = g_out.load(), c = g_comp.load();
            std::fprintf(stderr, "{\"requests_per_s\": %.0f, \"bits_in_per_s\": %.0f, \"bits_out_per_s\": %.0f, \"compressions_per_s\": %.0f, \"total_requests\": %llu, \"connections\": %llu}\n",
                         double(r - pr) / report_s, 8.0 * double(i - pi) / report_s, 8.0 * double(o - po) / report_s, double(c - pc) / report_s, (unsigned long long)r, (unsigned long long)g_conns.load());
            std::fflush(stderr);
            pr = r; pi = i; po = o; pc = c;
        }
    }).detach();
    for (;;) {
        int fd = ::accept(ls, nullptr, nullptr);
        if (fd < 0) continue;
        std::thread(serve_conn, fd, sk, t).detach();
    }
}

}  // namespace

int main(int argc, char** argv) {
    if (argc >= 2 && std::strcmp(argv[1], "serve") == 0) return serve(argc, argv);
    std::fprintf(stderr, "usage: fast-oracle serve <port> <sk-file> <params.json> <C hex16> <V> <P hex64> <H hex64> [report_s]\n");
    return 2;
}
