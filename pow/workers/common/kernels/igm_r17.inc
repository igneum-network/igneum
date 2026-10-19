// igm_r17.inc: VERSION 17, THE TEXTURE FORM (the hash lead's line of 14 October 2026 16:34 UK; the reference's r17 text on main 8a84c57a).
// A static 4096 x 4096 R32 uint texture, texel (x, y) = fmix32(y * 4096 + x), sampled NEAREST at the unnormalised texel centre
// (x + 0.5, y + 0.5) through the texture unit (the probe's rule: read_imageui under CLK_NORMALIZED_COORDS_FALSE | CLK_FILTER_NEAREST on
// OpenCL, texture2d<uint, access::sample>.sample with filter::nearest and coord::pixel on Metal; the determinism gate of 14 Oct 2026
// read one digest on CUDA, AMD, Intel and Apple). Per attempt: K the period key (host), st[0..7] = BLAKE3(LP("IgneumHash/R0H/init") ||
// K || S) as eight LE words; for i in 0 .. rounds - 1: l = i & 7, c = st[l] ^ Kw[(i >> 3) & 7], x = c & 4095, y = (c >> 12) & 4095, t the
// texel, t output, st[l] = fmix32(st[l] ^ t ^ (i * 0x9E3779B1)); M = BLAKE3(LP("IgneumHash/R0H/mix") || K || the outputs LE32);
// the final digest over S and M as every version. One work-item an attempt, eight dependent lanes in its registers; the mix input
// (51 + 4 * rounds bytes: 2,099 at the registered 512, 16,435 at s4096) streamed through BLAKE3 as the samples arrive.
#if IGM_IH_VERSION == 17
IGM_CONST uchar ih_tag_r17_init[20] = {'I','g','n','e','u','m','H','a','s','h','/','R','0','H','/','i','n','i','t'};
IGM_CONST uchar ih_tag_r17_mix[19] = {'I','g','n','e','u','m','H','a','s','h','/','R','0','H','/','m','i','x'};
#ifndef IGM_R17_ROUNDS
#define IGM_R17_ROUNDS 512u
#endif
// The streamed BLAKE3 of the mix input (unkeyed; the total length known ahead): bytes into a 64-byte block, blocks into chunks of 16 with the
// chunk counter, a chunk's chaining value onto the stack and merged by the trailing-zero rule (two off, one parent on) as every complete
// chunk ends; the final chunk's value cascades down the stack with PARENT, the last merge ROOT (or the final block ROOT when it is the only
// chunk). 257 chunks at the s4096 document's 16,435 bytes... at rounds 65,536 (262,195 bytes); the stack holds ten.
typedef struct { uint cv[8]; uint stack[80]; uint depth; uint chunk; uint blk[16]; uint blen; uint bic; uint fed; uint total; } igm_r17_b3;
IGM_INLINE void igm_r17_b3_init(IGM_PRIV igm_r17_b3 *h, uint total) { for (uint i = 0; i < 8u; i++) h->cv[i] = igm_b3_iv[i]; for (uint i = 0; i < 16u; i++) h->blk[i] = 0u; h->depth = 0u; h->chunk = 0u; h->blen = 0u; h->bic = 0u; h->fed = 0u; h->total = total; }
IGM_INLINE void igm_r17_b3_parent(IGM_PRIV const uint *l, IGM_PRIV const uint *r, uint root, IGM_PRIV uint *out8) {
    uint m[16]; for (uint i = 0; i < 8u; i++) { m[i] = l[i]; m[8u + i] = r[i]; } uint iv[8]; for (uint i = 0; i < 8u; i++) iv[i] = igm_b3_iv[i];
    uint o[16]; ih_b3_compress(iv, m, 0u, 64u, IH_PARENT | (root ? IH_ROOT : 0u), o); for (uint i = 0; i < 8u; i++) out8[i] = o[i];
}
// the block in hand compressed: the chunk's last block when it is the sixteenth or the input's last; the root when the input ends in the first chunk
IGM_INLINE void igm_r17_b3_block(IGM_PRIV igm_r17_b3 *h, IGM_PRIV uint *root8) {
    const uint last_of_input = (h->fed == h->total) ? 1u : 0u, last_of_chunk = (h->bic == 15u || last_of_input) ? 1u : 0u;
    uint flags = 0u; if (h->bic == 0u) flags |= IH_CHUNK_START; if (last_of_chunk) flags |= IH_CHUNK_END;
    const uint root_here = (last_of_input && h->depth == 0u) ? 1u : 0u; if (root_here) flags |= IH_ROOT;
    uint o[16]; ih_b3_compress(h->cv, h->blk, h->chunk, h->blen, flags, o);
    for (uint i = 0; i < 16u; i++) h->blk[i] = 0u; h->blen = 0u;
    if (root_here) { for (uint i = 0; i < 8u; i++) root8[i] = o[i]; return; }
    if (!last_of_chunk) { for (uint i = 0; i < 8u; i++) h->cv[i] = o[i]; h->bic++; return; }
    // a chunk's chaining value
    uint c[8]; for (uint i = 0; i < 8u; i++) c[i] = o[i];
    h->chunk++; h->bic = 0u; for (uint i = 0; i < 8u; i++) h->cv[i] = igm_b3_iv[i];
    if (last_of_input) {   // the cascade: PARENT down the stack, the last merge ROOT
        while (h->depth > 0u) { h->depth--; uint p[8]; igm_r17_b3_parent(&h->stack[8u * h->depth], c, h->depth == 0u ? 1u : 0u, p); for (uint i = 0; i < 8u; i++) c[i] = p[i]; }
        for (uint i = 0; i < 8u; i++) root8[i] = c[i]; return;
    }
    // the trailing-zero rule over the completed chunk count
    uint t = h->chunk;
    while ((t & 1u) == 0u) { h->depth--; uint p[8]; igm_r17_b3_parent(&h->stack[8u * h->depth], c, 0u, p); for (uint i = 0; i < 8u; i++) c[i] = p[i]; t >>= 1u; }
    for (uint i = 0; i < 8u; i++) h->stack[8u * h->depth + i] = c[i]; h->depth++;
}
IGM_INLINE void igm_r17_b3_put8(IGM_PRIV igm_r17_b3 *h, uint b, IGM_PRIV uint *root8) {
    h->blk[h->blen >> 2] |= (b & 0xffu) << (8u * (h->blen & 3u)); h->blen++; h->fed++;
    if (h->blen == 64u || h->fed == h->total) igm_r17_b3_block(h, root8);
}
IGM_INLINE void igm_r17_b3_put32(IGM_PRIV igm_r17_b3 *h, uint w, IGM_PRIV uint *root8) { for (uint bb = 0; bb < 4u; bb++) igm_r17_b3_put8(h, w >> (8u * bb), root8); }
IGM_INLINE uint igm_r17_fmix32(uint h) { h ^= h >> 16; h *= 0x85EBCA6Bu; h ^= h >> 13; h *= 0xC2B2AE35u; h ^= h >> 16; return h; }
IGM_INLINE void igm_r17_attempt(IGM_PRIV const ih_consts *k, IGM_PRIV const uint *hdr8, uint nonce_hi, uint nonce_lo, IGM_PRIV const uint *Kw, IGM_R17_TEX_PARAM, IGM_PRIV uint *mix_out8, IGM_PRIV uint *final8) {
    uint S[16]; ih_stream_S(k, hdr8, nonce_hi, nonce_lo, S);
    uint st[8];
    {
        uint w[32]; for (uint j = 0; j < 32u; j++) w[j] = 0u;
        uint pos = ih_put_tag(w, 0u, ih_tag_r17_init, 19u); pos = ih_put_bytes32(w, pos, Kw); pos = ih_put_bytes32(w, pos, S);   // 20 + 32 + 32 = 84 bytes: two blocks of one chunk
        uint d[16]; ih_b3_chunk(k->p, false, w, pos, 1u, d);
        for (uint q = 0; q < 8u; q++) st[q] = d[q];
    }
    igm_r17_b3 h; igm_r17_b3_init(&h, 51u + 4u * IGM_R17_ROUNDS); uint M[8]; for (uint q = 0; q < 8u; q++) M[q] = 0u;
    igm_r17_b3_put8(&h, 18u, M); for (uint q = 0; q < 18u; q++) igm_r17_b3_put8(&h, ih_tag_r17_mix[q], M);   // LP("IgneumHash/R0H/mix"): the length byte and the 18 characters
    for (uint q = 0; q < 8u; q++) igm_r17_b3_put32(&h, Kw[q], M);
    for (uint i = 0; i < IGM_R17_ROUNDS; i++) {
        const uint l = i & 7u, cw = st[l] ^ Kw[(i >> 3) & 7u];
        const uint t = IGM_R17_SAMPLE(cw & 4095u, (cw >> 12) & 4095u);
        igm_r17_b3_put32(&h, t, M);
        st[l] = igm_r17_fmix32(st[l] ^ t ^ (i * 0x9E3779B1u));
    }
    for (uint q = 0; q < 8u; q++) mix_out8[q] = M[q];
    uint Fo[8]; ih_stream_F(k, S, M, Fo); for (uint q = 0; q < 8u; q++) final8[q] = Fo[q];
}
#endif
