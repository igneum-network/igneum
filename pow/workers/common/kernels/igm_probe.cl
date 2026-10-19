// igm_probe.cl: the OpenCL port of the ladder lead's primitive probe (pow/harness/prim_probe.cu; the shapes fixed in
// docs/ladder/PRIM-PROBE.md, 10 Oct 2026 22:3x UK) for the RDNA4 rows of the escape search. Every kernel matches the CUDA
// arithmetic word for word where the ISA allows; a departure is labelled in the host's record (shape_notes).
//   RDNA4 departures: wave32 in place of warps of 32 (the same width); cross-lane xor through ds_swizzle/ds_bpermute in place of
//   __shfl_xor_sync; the byte permutation through v_perm_b32 (__builtin_amdgcn_perm) in place of PRMT (the selector's byte
//   numbering mapped); the matrix tile through the gfx12 wave matrix builtin (16x16x16 int8, 32-bit accumulation) in place of
//   mma.sync; on a runtime without the AMD builtins the same arithmetic as scalar code, labelled.
#if defined(__AMDGCN__)
#define PR_AMD 1
static inline uint pr_lane(void) { return (uint)__builtin_amdgcn_mbcnt_hi(~0u, __builtin_amdgcn_mbcnt_lo(~0u, 0u)); }
#define PR_BPERM(v, src) as_uint(__builtin_amdgcn_ds_bpermute((int)((src) << 2), as_int(v)))
#define PR_XOR(v, m) PR_BPERM((v), (pr_lane() ^ (m)))
#define PR_SHFL(v, src) PR_BPERM((v), ((pr_lane() & ~31u) + (src)))
#define PR_PERM(a, b, sel) __builtin_amdgcn_perm((a), (b), (sel))
#else
// the fallback exchange through local memory (the host passes a scratch word per work-item); never on the card rows
#define PR_XOR(v, m) pr_xch_xor(xch, tid, (v), (m))
#define PR_SHFL(v, src) pr_xch_src(xch, tid, (v), (src))
static inline uint pr_xch_xor(__local uint *xch, uint tid, uint v, uint m) { barrier(CLK_LOCAL_MEM_FENCE); xch[tid] = v; barrier(CLK_LOCAL_MEM_FENCE); return xch[tid ^ m]; }
static inline uint pr_xch_src(__local uint *xch, uint tid, uint v, uint src) { barrier(CLK_LOCAL_MEM_FENCE); xch[tid] = v; barrier(CLK_LOCAL_MEM_FENCE); return xch[(tid & ~31u) + src]; }
// CUDA's __byte_perm(a, b, s): result byte i = the byte numbered by nibble i of s from the 8 bytes {a0..a3, b0..b3}
static inline uint pr_perm_soft(uint a, uint b, uint s) { uint r = 0; for (uint i = 0; i < 4; i++) { uint n = (s >> (4u * i)) & 7u; uint byte = n < 4u ? (a >> (8u * n)) & 0xffu : (b >> (8u * (n - 4u))) & 0xffu; r |= byte << (8u * i); } return r; }
#define PR_PERM(a, b, sel) pr_perm_soft((a), (b), (sel))
#endif
#define ROTL32(x, n) (((x) << (n)) | ((x) >> (32u - (n))))
#define PR_ROUND4(a, b, c, d) { a = ROTL32(a * 0x9E3779B1u, 13u) ^ b; b = ROTL32(b * 0x85EBCA6Bu, 17u) ^ c; c = ROTL32(c * 0xC2B2AE35u, 5u) ^ d; d = ROTL32(d * 0x27D4EB2Fu, 11u) ^ a; }
// v_perm_b32's selector numbers the bytes of {b, a} as 0..7 with b low (0..3 = b's bytes, 4..7 = a's); CUDA's PRMT numbers
// {a, b} with a low; so a CUDA selector s maps to v_perm's s ^ 0x44444444 with the operands swapped (the host notes the mapping)
#ifdef PR_AMD
#define PR_BYTEPERM(a, b, s) PR_PERM((b), (a), ((s) ^ 0x44444444u))
#else
#define PR_BYTEPERM(a, b, s) PR_PERM((a), (b), (s))
#endif

// ---------------------------------------------------------------- the fills (the CUDA counter fills, bit for bit)
__kernel void pr_fill_u32(__global uint *p, ulong n, uint seed) { ulong i = get_global_id(0); if (i < n) { uint x = (uint)i * 2654435761u ^ seed; x ^= x >> 13; x *= 0x5bd1e995u; x ^= x >> 15; p[i] = x; } }
__kernel void pr_fill_i8(__global char *p, ulong n, uint seed) { ulong i = get_global_id(0); if (i < n) { uint x = (uint)i * 2654435761u ^ seed; x ^= x >> 13; x *= 0x5bd1e995u; p[i] = (char)(((x >> 24) & 0x0fu) - 7u); } }
#ifdef cl_khr_fp16
#pragma OPENCL EXTENSION cl_khr_fp16 : enable
__kernel void pr_fill_h(__global half *p, ulong n, uint seed) { ulong i = get_global_id(0); if (i < n) { uint x = (uint)i * 2654435761u ^ seed; x ^= x >> 13; x *= 0x5bd1e995u; p[i] = (half)((int)((x >> 24) & 0x1fu) - 15); } }
#endif
// the checksums (the CUDA weighted sums: every word times (i mod 1021 + 1), summed in 64 bits; here per workgroup then the host sums)
__kernel void pr_sum_u32(__global const uint *p, ulong n, __global ulong *out, __local ulong *acc) {
    ulong i = get_global_id(0); uint tid = get_local_id(0); acc[tid] = i < n ? (ulong)p[i] * (ulong)(i % 1021ul + 1ul) : 0ul; barrier(CLK_LOCAL_MEM_FENCE);
    for (uint s = get_local_size(0) / 2u; s > 0u; s >>= 1u) { if (tid < s) acc[tid] += acc[tid + s]; barrier(CLK_LOCAL_MEM_FENCE); }
    if (tid == 0u) out[get_group_id(0)] = acc[0];
}
__kernel void pr_sum_f32(__global const float *p, ulong n, __global ulong *out, __local ulong *acc) {
    ulong i = get_global_id(0); uint tid = get_local_id(0); acc[tid] = i < n ? (ulong)as_uint(p[i]) * (ulong)(i % 1021ul + 1ul) : 0ul; barrier(CLK_LOCAL_MEM_FENCE);
    for (uint s = get_local_size(0) / 2u; s > 0u; s >>= 1u) { if (tid < s) acc[tid] += acc[tid + s]; barrier(CLK_LOCAL_MEM_FENCE); }
    if (tid == 0u) out[get_group_id(0)] = acc[0];
}

// ---------------------------------------------------------------- the integer round, the shuffle network, the byte permutations
// CUDA: blocks of 256 threads; here workgroups of 256 work-items (eight wave32s); blockIdx.x = get_group_id(0), threadIdx.x = get_local_id(0)
__kernel void pr_intround(__global uint *out, uint seed, int passes) {
    uint gid = get_group_id(0), tid = get_local_id(0);
    uint a = seed ^ (gid * 977u + tid), b = a * 0x9E3779B1u, c = b ^ 0xA5A5A5A5u, d = c + 0x3C6EF372u;
    for (int p = 0; p < passes; ++p) { for (int r = 0; r < 64; ++r) PR_ROUND4(a, b, c, d) }
    out[gid * get_local_size(0) + tid] = a ^ b ^ c ^ d;
}
__kernel void pr_shuffle(__global uint *out, uint seed, int passes, __local uint *xch) {
    uint gid = get_group_id(0), tid = get_local_id(0); const uint lane = tid & 31u;
    uint w[8]; for (int i = 0; i < 8; ++i) w[i] = seed ^ (gid * 7919u + tid * 131u + (uint)i * 0x9E3779B1u);
    for (int p = 0; p < passes; ++p) {
        for (int n = 0; n < 64; ++n) {   // one 32-byte permutation: a 5-stage butterfly over the 8 words of each lane (40 cross-lane reads)
            for (uint s = 1u; s < 32u; s <<= 1u) {
                for (int i = 0; i < 8; ++i) { uint v = PR_XOR(w[i], s); w[i] = (lane & s) ? (v ^ w[(i + 1) & 7]) : (w[i] + v); }
            }
        }
    }
    uint x = 0; for (int i = 0; i < 8; ++i) x ^= w[i];
    out[gid * get_local_size(0) + tid] = x;
}
__kernel void pr_byteperm(__global uint *out, uint seed, int passes) {
    uint gid = get_group_id(0), tid = get_local_id(0);
    uint w[8]; for (int i = 0; i < 8; ++i) w[i] = seed ^ (gid * 7919u + tid * 131u + (uint)i * 0x9E3779B1u);
    for (int p = 0; p < passes; ++p) {
        for (int n = 0; n < 64; ++n) { for (int i = 0; i < 8; ++i) w[i] = PR_BYTEPERM(w[i], w[(i + 3) & 7], 0x3210u ^ (w[(i + 5) & 7] & 0x7777u)); }
    }
    uint x = 0; for (int i = 0; i < 8; ++i) x ^= w[i];
    out[gid * get_local_size(0) + tid] = x;
}

// ---------------------------------------------------------------- the Strata-shaped walk, plain and with side work per item
// 8 lanes per context read one 128-byte item (16 bytes per lane), the next index from the words read (xorshift64 folded with the
// 8 lanes' words over the xor tree), 64 reads per context. SIDE 0 plain; SIDE 2 the 64 integer rounds per lane on the item's words;
// SIDE 1 (walk-gemm) is the host's separate kernel (pr_walk_gemm) on the wave matrix builtin.
static inline uint pr_walk_fold(uint f, uint tid, __local uint *xch) { f = PR_XOR(f, 1u) ^ f; f = PR_XOR(f, 2u) ^ f; f = PR_XOR(f, 4u) ^ f; return f; }
#define PR_WALK(NAME, SIDE) \
__kernel void NAME(__global const uint4 *buf, ulong items, int reads, __global uint *out, uint seed, __local uint *xch) { \
    uint gid = get_group_id(0), tid = get_local_id(0); const uint lane = tid & 7u; \
    const uint ctx = (uint)((gid * (ulong)get_local_size(0) + tid) >> 3); \
    ulong x = 0x9E3779B97F4A7C15ul * (ulong)(1u + ctx) ^ (ulong)seed; uint acc = 0; \
    for (int r = 0; r < reads; ++r) { \
        x ^= x << 13; x ^= x >> 7; x ^= x << 17; \
        const ulong item = x % items; \
        const uint4 v = buf[item * 8ul + lane]; \
        uint f = v.x ^ v.y ^ v.z ^ v.w; \
        if (SIDE == 2) { uint a = v.x, b = v.y, c = v.z, d = v.w; for (int q = 0; q < 64; ++q) PR_ROUND4(a, b, c, d) f ^= a ^ b ^ c ^ d; } \
        acc ^= f; \
        f = pr_walk_fold(f, tid, xch); \
        x += (ulong)f; \
    } \
    out[gid * get_local_size(0) + tid] = acc; \
}
PR_WALK(pr_walk, 0)
PR_WALK(pr_walk_int, 2)

// the compute unit: rounds x the 4-chain round, then one 32-byte byte permutation of 8 words
static inline uint pr_cunit(uint *a, uint *b, uint *c, uint *d, uint *w, int rounds) {
    uint A = *a, B = *b, C = *c, D = *d;
    for (int q = 0; q < rounds; ++q) PR_ROUND4(A, B, C, D)
    *a = A; *b = B; *c = C; *d = D;
    w[0] ^= A; w[3] ^= B; w[5] ^= C; w[7] ^= D;
    for (int i = 0; i < 8; ++i) w[i] = PR_BYTEPERM(w[i], w[(i + 3) & 7], 0x3210u ^ (w[(i + 5) & 7] & 0x7777u));
    return w[0] ^ w[1] ^ w[2] ^ w[3] ^ w[4] ^ w[5] ^ w[6] ^ w[7];
}
// MODE 0: the walk with write-back (write_pct of the items; write_random: a random write of another item; storm_ways: the R3
// address rule); MODE 1: compute only; MODE 2: sequential (cunits compute units after every item in the same work-item);
// MODE 3: simultaneous (waves 0..3 of a 256-item workgroup walk, waves 4..7 compute). reads = items per context.
#define PR_DUPLEX(NAME, MODE) \
__kernel void NAME(__global uint4 *buf, ulong items, int reads, int cunits, int rounds, uint write_pct, __global uint *out, uint seed, int write_random, uint storm_ways, ulong storm_stride_items, __local uint *xch) { \
    uint gid = get_group_id(0), tid = get_local_id(0); const uint lane = tid & 7u; \
    const bool walker = (MODE == 0 || MODE == 2) || (MODE == 3 && (tid >> 5) < 4u); \
    const uint id = gid * get_local_size(0) + tid; uint acc = 0; \
    if (walker) { \
        const uint ctx = (MODE == 3) ? (gid * 16u + (tid >> 3)) : (id >> 3); \
        ulong x = 0x9E3779B97F4A7C15ul * (ulong)(1u + ctx) ^ (ulong)seed; \
        uint a = seed ^ id, b = a * 0x9E3779B1u, c = b ^ 0xA5A5A5A5u, d = c + 0x3C6EF372u; \
        for (int r = 0; r < reads; ++r) { \
            x ^= x << 13; x ^= x >> 7; x ^= x << 17; \
            ulong item; \
            if (storm_ways) { const ulong span = (ulong)storm_ways * storm_stride_items; item = (x % (items / span)) * span + (ulong)((ctx + (uint)r) % storm_ways) * storm_stride_items; } \
            else item = x % items; \
            uint4 v = buf[item * 8ul + lane]; \
            uint f = v.x ^ v.y ^ v.z ^ v.w; \
            if (MODE == 2) { uint w[8] = {v.x, v.y, v.z, v.w, v.x ^ 1u, v.y ^ 2u, v.z ^ 3u, v.w ^ 4u}; for (int u = 0; u < cunits; ++u) f ^= pr_cunit(&a, &b, &c, &d, w, rounds); } \
            if ((uint)((item * 0x9E3779B1ul) >> 32) % 100u < write_pct) { \
                v.x += f; v.y ^= f; v.z -= f; v.w ^= (uint)r; \
                const ulong wi = write_random ? ((x * 0xD1B54A32D192ED03ul + 0x9E3779B97F4A7C15ul) % items) : item; \
                buf[wi * 8ul + lane] = v; } \
            acc ^= f; \
            f = pr_walk_fold(f, tid, xch); \
            x += (ulong)f; \
        } \
    } else { \
        uint a = seed ^ id, b = a * 0x9E3779B1u, c = b ^ 0xA5A5A5A5u, d = c + 0x3C6EF372u; \
        uint w[8]; for (int i = 0; i < 8; ++i) w[i] = seed ^ (id * 131u + (uint)i * 0x9E3779B1u); \
        for (int u = 0; u < cunits; ++u) acc ^= pr_cunit(&a, &b, &c, &d, w, rounds); \
    } \
    out[id] = acc; \
}
PR_DUPLEX(pr_duplex_mem, 0)
PR_DUPLEX(pr_duplex_compute, 1)
PR_DUPLEX(pr_duplex_seq, 2)
PR_DUPLEX(pr_duplex_sim, 3)

// ---------------------------------------------------------------- the matrix rows (gfx12 wave matrix builtins)
// 64 x 64 output tile per workgroup of four wave32s, each wave 2 x 2 fragments of 16x16, K stepped by 16: the CUDA layout.
// A row-major M x K, B col-major (stored as N x K row-major), C row-major M x N. The gfx12 int8 WMMA takes per lane 8 int8 of A
// (a v2i) and 8 of B, the accumulator 8 int32 per lane; the fragment layouts of the hardware are loaded as the ISA defines
// (lane l holds row l mod 16 of the A tile, k = 8 x (l div 16) .. +7; the same for B's column), and the accumulator's lane
// layout is folded into the checksum without a store in the CUDA order (the sum is over every output word, order-free).
#if defined(PR_AMD) && defined(IGM_PROBE_WMMA)
typedef int v2i __attribute__((ext_vector_type(2)));
typedef int v8i __attribute__((ext_vector_type(8)));
static inline v2i pr_load_a(__global const char *A, int row, int k, int K, uint lane) { const __global char *p = A + (ulong)(row + (int)(lane & 15u)) * K + k + 8 * (int)(lane >> 4); return (v2i)(*(const __global int *)p, *(const __global int *)(p + 4)); }
static inline v2i pr_load_b(__global const char *B, int col, int k, int K, uint lane) { const __global char *p = B + (ulong)(col + (int)(lane & 15u)) * K + k + 8 * (int)(lane >> 4); return (v2i)(*(const __global int *)p, *(const __global int *)(p + 4)); }
__kernel void pr_gemm_int8(__global const char *A, __global const char *B, __global int *C, int M, int N, int K) {
    const uint tid = get_local_id(0), lane = pr_lane() & 31u, wave = tid >> 5;
    const int row0 = (int)get_group_id(1) * 64 + (int)(wave >> 1) * 32, col0 = (int)get_group_id(0) * 64 + (int)(wave & 1u) * 32;
    v8i acc[2][2]; for (int i = 0; i < 2; ++i) for (int j = 0; j < 2; ++j) acc[i][j] = (v8i)(0);
    for (int k = 0; k < K; k += 16) {
        v2i a[2], b[2];
        for (int i = 0; i < 2; ++i) a[i] = pr_load_a(A, row0 + 16 * i, k, K, lane);
        for (int j = 0; j < 2; ++j) b[j] = pr_load_b(B, col0 + 16 * j, k, K, lane);
        for (int i = 0; i < 2; ++i) for (int j = 0; j < 2; ++j) acc[i][j] = __builtin_amdgcn_wmma_i32_16x16x16_iu8_w32_gfx12(true, a[i], true, b[j], acc[i][j], false);
    }
    // the store: lane l holds column l mod 16 of the tile, rows 8 x (l div 16) .. +7 (the gfx12 accumulator layout)
    for (int i = 0; i < 2; ++i) for (int j = 0; j < 2; ++j) for (int e = 0; e < 8; ++e) C[(ulong)(row0 + 16 * i + 8 * (int)(lane >> 4) + e) * N + col0 + 16 * j + (int)(lane & 15u)] = acc[i][j][e];
}
#endif

// ---------------------------------------------------------------- version 8 option (b): the binary32 form of opcode 2 (SPEC-R0A-v0.md 0.8 bit 4)
// y' = (y & 0x3FFFFFFF) | 0x20000000 as binary32, z' and x' likewise; f = round(round(y' * z') + x'), nearest-even, the product
// rounded before the sum (no contraction: FP_CONTRACT OFF here, -fp-contract=off on AMD, no fast-math option on the build);
// out = bits(f) ^ x. The check kernel evaluates the recorded cases of the set's fp class on the device, bit for bit.
#pragma OPENCL FP_CONTRACT OFF
static inline uint v8_fop(uint x, uint y, uint z) {
    const uint yb = (y & 0x3FFFFFFFu) | 0x20000000u, zb = (z & 0x3FFFFFFFu) | 0x20000000u, xb = (x & 0x3FFFFFFFu) | 0x20000000u;
    const float yf = as_float(yb), zf = as_float(zb), xf = as_float(xb);
    volatile float prod = yf * zf;   // rounded once; volatile keeps the sum from contracting into an FMA on any compiler
    const float sum = prod + xf;     // rounded once
    return as_uint(sum) ^ x;
}
// bit 5 (supersedes bit 4; the hash lead 23:13 UK): every opcode binary32 with two roundings, the inner operation first:
// 0: x' + (y' - z'); 1: x' - (y' + z'); 2: (y' * z') + x'; 3: (x' - y') + z'; out = bits(f) ^ x; a zero difference is +0.0
static inline uint v8_fop_wide(uint opcode, uint x, uint y, uint z) {
    const uint yb = (y & 0x3FFFFFFFu) | 0x20000000u, zb = (z & 0x3FFFFFFFu) | 0x20000000u, xb = (x & 0x3FFFFFFFu) | 0x20000000u;
    const float yf = as_float(yb), zf = as_float(zb), xf = as_float(xb);
    volatile float inner;
    float f;
    switch (opcode & 3u) {
        case 0u: inner = yf - zf; f = xf + inner; break;
        case 1u: inner = yf + zf; f = xf - inner; break;
        case 2u: inner = yf * zf; f = inner + xf; break;
        default: inner = xf - yf; f = inner + zf; break;
    }
    return as_uint(f) ^ x;
}
__kernel void igm_v8_fop_check(__global const uint *cases, __global uint *out, uint n, uint wide) {   // cases: x, y, z, opcode per case; out: the device's result bits
    uint i = get_global_id(0); if (i >= n) return;
    out[i] = wide ? v8_fop_wide(cases[4u * i + 3u], cases[4u * i], cases[4u * i + 1u], cases[4u * i + 2u]) : v8_fop(cases[4u * i], cases[4u * i + 1u], cases[4u * i + 2u]);
}
