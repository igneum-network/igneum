// bond-oracle: the reference oracle of SPEC-R0A-v0.md section 0.9.2 (the split-execution model; GUARD-C-CLOSURE.md 1.4's
// O3 harness, the design seat's word of 09:5x UK, 11 Oct 2026). It holds a secret and answers keyed-round requests
// over TCP so the renter side (a card querying an oracle per keyed round) and the rate half (security's shaped link)
// speak ONE oracle text, reproducible from the reference and not from a harness.
//
//   bond-oracle serve <port> <sk-file (32-byte hex)> <params.json> <C hex 16> <V> <P hex 64> <H hex 64> [report_s]
//   bond-oracle client <host> <port> <n> <S>        a load client: n attempts of S keyed rounds (round 0 the nonce alone)
//
// Wire form (one request, one answer; binary, little-endian, framed by length): request = u64 nonce || u32 i || m_i
// (m_i 32 bytes, ABSENT at i 0: the oracle computes m_0 = S = BLAKE3(tag(T_SEED) || C || V || P || H || u64be(N))
// itself from the template it holds and the nonce), 12 or 44 bytes; answer = s_i, 32 bytes, from ignh_bond_share.
// Many requests ride in one TCP stream; the oracle answers in order. Every `report_s` seconds (default 5) the server
// prints requests per second, bytes per second each way and keyed compressions per second to stderr, the numbers
// 0.9.2's table is read against (640 S bits per attempt framed, 72 payload).
//
// The secret is read from a file by path and never printed. Nothing here is served to anyone but the harness.
#include "igneumhash.h"
#include "blake3.h"
#include <arpa/inet.h>
#include <netinet/in.h>
#include <netinet/tcp.h>
#include <sys/socket.h>
#include <unistd.h>
#include <atomic>
#include <chrono>
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

bool read_all(int fd, uint8_t* p, size_t n) {
    while (n) {
        ssize_t r = ::recv(fd, p, n, MSG_WAITALL);
        if (r <= 0) return false;
        p += r; n -= size_t(r);
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

// The work seed S of section 2: BLAKE3(tag(T_SEED) || C || V || P || H || u64be(N)), tag(T) = u8(len) || T.
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

std::atomic<uint64_t> g_req{0}, g_in{0}, g_out{0}, g_comp{0};

void serve_conn(int fd, const uint8_t sk[32], const Template t) {
    int one = 1; ::setsockopt(fd, IPPROTO_TCP, TCP_NODELAY, &one, sizeof one);
    uint8_t head[12], m[32], s[32];
    for (;;) {
        if (!read_all(fd, head, 12)) break;
        uint64_t nonce; uint32_t i; std::memcpy(&nonce, head, 8); std::memcpy(&i, head + 8, 4);
        size_t in_bytes = 12;
        if (i == 0) { work_seed(t, nonce, m); g_comp.fetch_add(1, std::memory_order_relaxed); }
        else { if (!read_all(fd, m, 32)) break; in_bytes += 32; }
        ignh_bond_share(sk, i, m, s);
        if (!write_all(fd, s, 32)) break;
        g_req.fetch_add(1, std::memory_order_relaxed); g_in.fetch_add(in_bytes, std::memory_order_relaxed); g_out.fetch_add(32, std::memory_order_relaxed); g_comp.fetch_add(1, std::memory_order_relaxed);
    }
    ::close(fd);
}

int serve(int argc, char** argv) {
    if (argc < 9) return 2;
    const int port = std::atoi(argv[2]);
    uint8_t sk[32];
    { std::ifstream f(argv[3]); std::string hex; f >> hex; if (!unhex(hex, sk, 32)) { std::fprintf(stderr, "bond-oracle: the secret file is not 32 hex bytes\n"); return 2; } }
    Template t{};
    if (!unhex(argv[5], t.C, 8)) { std::fprintf(stderr, "bond-oracle: C is 8 hex bytes (u64be chain id)\n"); return 2; }
    t.V = uint32_t(std::atoi(argv[6]));
    if (!unhex(argv[7], t.P, 32) || !unhex(argv[8], t.H, 32)) { std::fprintf(stderr, "bond-oracle: P and H are 32 hex bytes\n"); return 2; }
    const int report_s = argc > 9 ? std::atoi(argv[9]) : 5;
    uint8_t pk[32]; ignh_bond_pk(sk, pk);
    std::fprintf(stderr, "bond-oracle: pk ");
    for (int k = 0; k < 32; ++k) std::fprintf(stderr, "%02x", pk[k]);
    std::fprintf(stderr, " on port %d (params %s read for the record only; the oracle needs the template, not the dataset)\n", port, argv[4]);
    int ls = ::socket(AF_INET, SOCK_STREAM, 0);
    int one = 1; ::setsockopt(ls, SOL_SOCKET, SO_REUSEADDR, &one, sizeof one);
    sockaddr_in a{}; a.sin_family = AF_INET; a.sin_addr.s_addr = htonl(INADDR_ANY); a.sin_port = htons(uint16_t(port));
    if (::bind(ls, (sockaddr*)&a, sizeof a) != 0 || ::listen(ls, 64) != 0) { std::perror("bond-oracle: bind"); return 1; }
    std::thread([report_s]() {
        uint64_t pr = 0, pi = 0, po = 0, pc = 0;
        for (;;) {
            std::this_thread::sleep_for(std::chrono::seconds(report_s));
            const uint64_t r = g_req.load(), i = g_in.load(), o = g_out.load(), c = g_comp.load();
            std::fprintf(stderr, "{\"requests_per_s\": %.0f, \"bits_in_per_s\": %.0f, \"bits_out_per_s\": %.0f, \"compressions_per_s\": %.0f, \"total_requests\": %llu}\n",
                         double(r - pr) / report_s, 8.0 * double(i - pi) / report_s, 8.0 * double(o - po) / report_s, double(c - pc) / report_s, (unsigned long long)r);
            pr = r; pi = i; po = o; pc = c;
        }
    }).detach();
    for (;;) {
        int fd = ::accept(ls, nullptr, nullptr);
        if (fd < 0) continue;
        std::thread(serve_conn, fd, sk, t).detach();
    }
}

int client(int argc, char** argv) {
    if (argc < 6) return 2;
    const char* host = argv[2]; const int port = std::atoi(argv[3]);
    const uint64_t n = std::strtoull(argv[4], nullptr, 10); const uint32_t S = uint32_t(std::atoi(argv[5]));
    int fd = ::socket(AF_INET, SOCK_STREAM, 0);
    sockaddr_in a{}; a.sin_family = AF_INET; a.sin_port = htons(uint16_t(port)); inet_pton(AF_INET, host, &a.sin_addr);
    if (::connect(fd, (sockaddr*)&a, sizeof a) != 0) { std::perror("bond-oracle: connect"); return 1; }
    int one = 1; ::setsockopt(fd, IPPROTO_TCP, TCP_NODELAY, &one, sizeof one);
    uint8_t req[44], s[32], m[32];
    const auto t0 = std::chrono::steady_clock::now();
    for (uint64_t nonce = 0; nonce < n; ++nonce) {
        for (uint32_t i = 0; i < S; ++i) {
            std::memcpy(req, &nonce, 8); std::memcpy(req + 8, &i, 4);
            size_t len = 12;
            if (i) { std::memcpy(m, s, 32); std::memcpy(req + 12, m, 32); len = 44; }  // the renter's m_i stands in for the state (the last share here)
            if (!write_all(fd, req, len) || !read_all(fd, s, 32)) { std::fprintf(stderr, "bond-oracle: the stream closed\n"); return 1; }
        }
    }
    const double sec = std::chrono::duration<double>(std::chrono::steady_clock::now() - t0).count();
    std::printf("{\"attempts\": %llu, \"S\": %u, \"seconds\": %.3f, \"attempts_per_s\": %.0f, \"round_trips_per_s\": %.0f}\n", (unsigned long long)n, S, sec, n / sec, double(n) * S / sec);
    ::close(fd);
    return 0;
}

}  // namespace

int main(int argc, char** argv) {
    if (argc >= 2 && std::strcmp(argv[1], "serve") == 0) return serve(argc, argv);
    if (argc >= 2 && std::strcmp(argv[1], "client") == 0) return client(argc, argv);
    std::fprintf(stderr, "usage: bond-oracle serve <port> <sk-file> <params.json> <C hex16> <V> <P hex64> <H hex64> [report_s] | client <host> <port> <n> <S>\n");
    return 2;
}
