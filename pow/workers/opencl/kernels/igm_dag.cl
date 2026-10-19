// igm_dag.cl: dataset generation from the light cache (Ethash lineage), one 64-byte item
// per work-item, bounded batches. Build defines: IGM_DATASET_PARENTS, IGM_CACHE_NODES.
// GPU engineer B, 9 October 2026. Source only; not built, not run.

__constant ulong keccakf1600_rndc[24] = {
    0x0000000000000001ul, 0x0000000000008082ul, 0x800000000000808aul, 0x8000000080008000ul,
    0x000000000000808bul, 0x0000000080000001ul, 0x8000000080008081ul, 0x8000000000008009ul,
    0x000000000000008aul, 0x0000000000000088ul, 0x0000000080008009ul, 0x000000008000000aul,
    0x000000008000808bul, 0x800000000000008bul, 0x8000000000008089ul, 0x8000000000008003ul,
    0x8000000000008002ul, 0x8000000000000080ul, 0x000000000000800aul, 0x800000008000000aul,
    0x8000000080008081ul, 0x8000000000008080ul, 0x0000000080000001ul, 0x8000000080008008ul
};
__constant uint keccakf1600_rotc[24] = { 1, 3, 6, 10, 15, 21, 28, 36, 45, 55, 2, 14, 27, 41, 56, 8, 25, 43, 62, 18, 39, 61, 20, 44 };
__constant uint keccakf1600_piln[24] = { 10, 7, 11, 17, 18, 3, 5, 16, 8, 21, 24, 4, 15, 23, 19, 13, 12, 2, 20, 14, 22, 9, 6, 1 };

inline void keccak_f1600(ulong *st) {
    for (uint r = 0; r < 24; r++) {
        ulong bc[5];
        for (uint i = 0; i < 5; i++) bc[i] = st[i] ^ st[i + 5] ^ st[i + 10] ^ st[i + 15] ^ st[i + 20];
        for (uint i = 0; i < 5; i++) {
            ulong t = bc[(i + 4) % 5] ^ rotate(bc[(i + 1) % 5], (ulong)1);
            for (uint j = 0; j < 25; j += 5) st[j + i] ^= t;
        }
        ulong t = st[1];
        for (uint i = 0; i < 24; i++) {
            uint j = keccakf1600_piln[i];
            ulong tmp = st[j];
            st[j] = rotate(t, (ulong)keccakf1600_rotc[i]);
            t = tmp;
        }
        for (uint j = 0; j < 25; j += 5) {
            for (uint i = 0; i < 5; i++) bc[i] = st[j + i];
            for (uint i = 0; i < 5; i++) st[j + i] ^= (~bc[(i + 1) % 5]) & bc[(i + 2) % 5];
        }
        st[0] ^= keccakf1600_rndc[r];
    }
}

inline void keccak512_64(const ulong *in8, ulong *out8) {
    ulong st[25];
    for (uint i = 0; i < 25; i++) st[i] = 0ul;
    for (uint i = 0; i < 8; i++) st[i] = in8[i];
    st[8] ^= 0x01ul;
    st[8] ^= 0x8000000000000000ul;
    keccak_f1600(st);
    for (uint i = 0; i < 8; i++) out8[i] = st[i];
}

inline uint fnv1(uint h, uint d) { return (h * 0x01000193u) ^ d; }

__kernel void igm_dag_generate(uint first_item, uint count,
                               __global const ulong *cache,
                               __global ulong *dataset)
{
    uint gid = get_global_id(0);
    if (gid >= count) return;
    uint index = first_item + gid;

    ulong mix8[8];
    uint node = index % IGM_CACHE_NODES;
    for (uint i = 0; i < 8; i++) mix8[i] = cache[node * 8 + i];
    mix8[0] ^= (ulong)index;
    keccak512_64(mix8, mix8);

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
