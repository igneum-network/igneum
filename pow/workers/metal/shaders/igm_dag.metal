// igm_dag.metal: dataset generation on the GPU from the light cache (Ethash lineage,
// as the ProgPoW/KAWPOW family uses it). One 64-byte item per thread, dispatched in
// bounded batches by the host so a cancel or a thermal pause lands within one batch.
// GPU engineer B, 9 October 2026. Source only; not built, not run.
//
// The item construction (keccak-512 of the seed node xor index, DATASET_PARENTS rounds
// of fnv over cache lookups, keccak-512 at the end) follows the Ethash definition that
// A's reference implements; A's igref_dataset_item is the CPU second opinion and the
// worker proves a sample of items byte for byte before the dataset is used (HW-01).

#include <metal_stdlib>
using namespace metal;

constant uint IGM_DATASET_PARENTS [[function_constant(0)]];  // 256 in Ethash
constant uint IGM_CACHE_NODES     [[function_constant(1)]];  // light cache size / 64

constant ulong keccakf1600_rndc[24] = {
    0x0000000000000001ul, 0x0000000000008082ul, 0x800000000000808aul, 0x8000000080008000ul,
    0x000000000000808bul, 0x0000000080000001ul, 0x8000000080008081ul, 0x8000000000008009ul,
    0x000000000000008aul, 0x0000000000000088ul, 0x0000000080008009ul, 0x000000008000000aul,
    0x000000008000808bul, 0x800000000000008bul, 0x8000000000008089ul, 0x8000000000008003ul,
    0x8000000000008002ul, 0x8000000000000080ul, 0x000000000000800aul, 0x800000008000000aul,
    0x8000000080008081ul, 0x8000000000008080ul, 0x0000000080000001ul, 0x8000000080008008ul
};
constant uint keccakf1600_rotc[24] = { 1, 3, 6, 10, 15, 21, 28, 36, 45, 55, 2, 14, 27, 41, 56, 8, 25, 43, 62, 18, 39, 61, 20, 44 };
constant uint keccakf1600_piln[24] = { 10, 7, 11, 17, 18, 3, 5, 16, 8, 21, 24, 4, 15, 23, 19, 13, 12, 2, 20, 14, 22, 9, 6, 1 };

inline ulong rotl64(ulong x, uint n) { return (x << n) | (x >> (64u - n)); }

inline void keccak_f1600(thread ulong st[25]) {
    for (uint r = 0; r < 24; r++) {
        ulong bc[5];
        for (uint i = 0; i < 5; i++) bc[i] = st[i] ^ st[i + 5] ^ st[i + 10] ^ st[i + 15] ^ st[i + 20];
        for (uint i = 0; i < 5; i++) {
            ulong t = bc[(i + 4) % 5] ^ rotl64(bc[(i + 1) % 5], 1u);
            for (uint j = 0; j < 25; j += 5) st[j + i] ^= t;
        }
        ulong t = st[1];
        for (uint i = 0; i < 24; i++) {
            uint j = keccakf1600_piln[i];
            ulong tmp = st[j];
            st[j] = rotl64(t, keccakf1600_rotc[i]);
            t = tmp;
        }
        for (uint j = 0; j < 25; j += 5) {
            for (uint i = 0; i < 5; i++) bc[i] = st[j + i];
            for (uint i = 0; i < 5; i++) st[j + i] ^= (~bc[(i + 1) % 5]) & bc[(i + 2) % 5];
        }
        st[0] ^= keccakf1600_rndc[r];
    }
}

// keccak-512 of a 64-byte input (rate 72 bytes): one block with the 0x01 ... 0x80 padding.
inline void keccak512_64(thread const ulong in8[8], thread ulong out8[8]) {
    ulong st[25];
    for (uint i = 0; i < 25; i++) st[i] = 0ul;
    for (uint i = 0; i < 8; i++) st[i] = in8[i];
    st[8] ^= 0x01ul;
    st[8] ^= 0x8000000000000000ul;
    keccak_f1600(st);
    for (uint i = 0; i < 8; i++) out8[i] = st[i];
}

inline uint fnv1(uint h, uint d) { return (h * 0x01000193u) ^ d; }

struct igm_dag_batch { uint first_item; uint count; uint pad0, pad1; };

kernel void igm_dag_generate(constant igm_dag_batch &b     [[buffer(0)]],
                             device const ulong *cache     [[buffer(1)]],   // light cache, 8 ulong per node
                             device ulong *dataset         [[buffer(2)]],   // 8 ulong per item
                             uint gid [[thread_position_in_grid]])
{
    if (gid >= b.count) return;
    uint index = b.first_item + gid;

    ulong mix8[8];
    uint node = index % IGM_CACHE_NODES;
    for (uint i = 0; i < 8; i++) mix8[i] = cache[node * 8 + i];
    mix8[0] ^= (ulong)index;
    keccak512_64(mix8, mix8);

    // work in 32-bit words, as the Ethash definition is written
    uint mix[16];
    for (uint i = 0; i < 8; i++) { mix[2 * i] = (uint)mix8[i]; mix[2 * i + 1] = (uint)(mix8[i] >> 32); }

    for (uint j = 0; j < IGM_DATASET_PARENTS; j++) {
        uint parent = fnv1(index ^ j, mix[j % 16]) % IGM_CACHE_NODES;
        for (uint i = 0; i < 8; i++) {
            ulong c = cache[parent * 8 + i];
            mix[2 * i]     = fnv1(mix[2 * i], (uint)c);
            mix[2 * i + 1] = fnv1(mix[2 * i + 1], (uint)(c >> 32));
        }
    }
    for (uint i = 0; i < 8; i++) mix8[i] = (ulong)mix[2 * i] | ((ulong)mix[2 * i + 1] << 32);
    keccak512_64(mix8, mix8);
    for (uint i = 0; i < 8; i++) dataset[(ulong)index * 8ul + i] = mix8[i];
}
