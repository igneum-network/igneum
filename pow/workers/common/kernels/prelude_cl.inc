#pragma OPENCL FP_CONTRACT OFF
#define IGM_U2F(x) as_float(x)
#define IGM_MULHI(a, b) mul_hi((uint)(a), (uint)(b))
#define IGM_F2U(x) as_uint(x)
// IGM_ROTR_VAR(x, n): the variable right-rotate of the generated period programs (progpow_gen.cpp math_code case 5).
// The Intel OpenCL compiler folds `rotate(x, (0u - n) & 31u)` (and this 32u - n spelling of the same count) into a rotate
// LEFT by n, the negation dropped: every variable right-rotate of every program came out wrong on the Arc B580 while every
// other family was bit-exact (the register-trace bisect of 7 October 2026, the old tree's intel_rotr.h). The host defines
// IGM_INTEL_ROTR_FOLD on an Intel platform before the build and the helper is then the shift form the CPU reference computes;
// every other vendor sees the rotate builtin unchanged. The IgneumHash body itself carries no rotate builtin (ih_v8_rotl is
// the shift form already), so the fold cannot reach the IgneumHash kernel; the guard is for the comparator programs.
#ifndef IGM_ROTR_VAR
#if defined(IGM_INTEL_ROTR_FOLD)
static inline uint igm_rotr_var(uint x, uint n) { n &= 31u; return (x >> n) | (x << ((32u - n) & 31u)); }
#define IGM_ROTR_VAR(x, n) igm_rotr_var((x), (n))
#else
#define IGM_ROTR_VAR(x, n) rotate((x), (32u - (((n)) & 31u)) & 31u)
#endif
#endif
#if IGM_FH_SHUFFLE
#pragma OPENCL EXTENSION cl_khr_subgroups : enable
#pragma OPENCL EXTENSION cl_khr_subgroup_shuffle : enable
#endif
// prelude_cl.inc: the OpenCL C dialect for the shared kernel bodies.
#define IGM_GLOBAL __global
// IGM_INLINE: the AMD LC compiler needs a definition for a helper it leaves out of line (an OpenCL C inline function without
// static has none: "undefined hidden symbol: ih_b3_chunk" on the 9070 XT, 15:41Z); pocl's work-item compiler crashes in the
// dataset kernel when the same helpers are static (their private arrays, 10 October 2026, the pocl build box); so static on AMD only.
// Intel (IGM_INTEL, the host's define on an Intel platform, 11 October 2026): the Arc B580 reports 33,408 bytes of private memory
// per work-item on every kernel form, exchange and register-file mode alike (tune-1, grf-1, census-1), a fixed block rather than the
// body's arrays; an external-linkage `inline` helper that the Intel compiler leaves out of line is served with a per-thread stack in
// scratch, so the helpers are static there as on AMD; the row on the card says whether the block goes.
// IGM_UNROLL(n): the static-index form of the hot arrays (the Intel step, 11 October 2026). Intel's compiler keeps a private array in
// memory when any access indexes it inside a loop it does not unroll (the B580: about 2 KB per lane on the version 4 form, reported
// as 33,408 bytes per SIMD16 thread); AMD's dynamic register indexing needs no hint. On an Intel platform (IGM_INTEL) or under the
// proof define IGM_IH_STATIC (the environment hook, for pocl and the 9070 XT rows) every loop that indexes a private array carries the
// unroll hint and the scratch-fill loop compiles out when the document has no scratch (IGM_IH_SLICE_WORDS, the host's figure); every
// other vendor sees the text unchanged (the macro empty, the loop as before).
#define IGM_UNROLL_FULL(n) __attribute__((opencl_unroll_hint(n)))   // the version 16 register loops on every vendor (the Metal prelude maps this one to clang's pragma)
// the job struct's address space (14 Oct 2026, the Arc B580 reading): Intel's runtime served the version 15 kernel a STALE view of the
// __constant job struct across launches (the first launch's nonce and header for every launch after it, the write blocking and the
// argument re-set each time), so on an Intel platform the job is read through the global space, where every launch sees the write
#if defined(IGM_INTEL)
#define IGM_JOB_SPACE __global const
#else
#define IGM_JOB_SPACE __constant
#endif
#if defined(IGM_INTEL) || defined(IGM_IH_STATIC)
#define IGM_UNROLL(n) __attribute__((opencl_unroll_hint(n)))
#define IGM_IH_STATIC_INDEX 1
#else
#define IGM_UNROLL(n)
#endif
#if defined(IGM_INTEL)
#define IGM_INLINE static inline __attribute__((always_inline))   // Intel: the helpers inlined whatever the compiler's own choice (an array handed to an out-of-line callee lives in memory; the B580's 2 KB per lane unchanged by linkage, hints and forms, 11 Oct 2026)
#elif defined(__AMDGCN__)
#define IGM_INLINE static inline
#else
#define IGM_INLINE inline
#endif
#ifndef IGM_FINAL_STORE
#define IGM_FINAL_STORE(dst, q, v) (dst)[q] = (v)
#endif
#ifndef IGM_FINAL_SINK
#define IGM_FINAL_SINK(area, f) do { } while (0)   // the Metal prelude's keyed digest sink; nothing elsewhere
#endif
#ifndef IGM_KEYED_FN
#define IGM_KEYED_FN IGM_INLINE   // the keyed share helper inlined as every helper is (the Metal prelude says noinline: the M5 Max compiler fault)
#endif
#define IGM_PRIV
#define IGM_CONST __constant

// local-memory broadcast within an eight-lane group (used when no sub-group shuffle exists)
static inline uint igm_fh_xch(__local uint *xch, uint group, uint lane, uint v, uint src) {
    xch[group * 8u + lane] = v;
    barrier(CLK_LOCAL_MEM_FENCE);
    uint out = xch[group * 8u + src];
    barrier(CLK_LOCAL_MEM_FENCE);
    return out;
}
// eight-lane broadcast for the FishHash lane kernel: the sub-group shuffle when the host
// says so (IGM_FH_SHUFFLE), else the local exchange; the body receives both handles
#define IGM_XCH_PARAMS uint simd_base, __local uint *xch, uint group
#define IGM_XCH_ARGS simd_base, xch, group   // forwards IGM_XCH_PARAMS to a callee
// The AMD wave lane path (the Riptide performance seat, 10 October 2026): on AMD's LC compiler (clang for amdgcn, which
// defines __AMDGCN__) and only when the host asks for it (IGM_IH_AMD_WAVE, the "-wave" variant suffix), the eight-lane
// exchange is the wave's own cross-lane instructions: ds_bpermute for a broadcast from any lane of the wave and ds_swizzle
// (bit mode, the xor of the lane id) for the xor-partner levels of the reduction tree. No local memory, no work-group
// barrier: the eight lanes of a nonce sit in one wave, so a cross-lane read is one LDS-unit instruction. The runtime on the
// 9070 XT reports no sub-group shuffle extension, so without this path every exchange is a store, a barrier, a load and a
// barrier through local memory. pocl and NVIDIA's ICD keep the sub-group shuffle path (the same relation, proven by the
// vectors and the identity set); the intrinsics are proven on the card by the same vectors. A lane's id inside the wave is
// the mbcnt idiom (the count of lanes below it).
// version 14's tile dots (SPEC-R0E-v0 2e): the signed 4-byte dot and the signed x unsigned dot on AMD's compiler where it has them; the scalar
// forms in igm_igneumhash.inc otherwise (the same integers)
#if defined(__AMDGCN__) && defined(__has_builtin)
#if __has_builtin(__builtin_amdgcn_sdot4)
#define IGM_DOT4S(a, b, c) __builtin_amdgcn_sdot4((a), (b), (c), false)
#endif
#if __has_builtin(__builtin_amdgcn_sudot4)
#define IGM_DOT4U(a, b, c) __builtin_amdgcn_sudot4(true, (a), false, (b), (c), false)
#endif
#if defined(IGM_DOT4S) && !defined(IGM_DOT4U)
IGM_INLINE int igm_dot4u_sc(uint a, uint b, int c) { for (uint j = 0; j < 4u; j++) { const int v = (int)((a >> (8u * j)) & 255u); c += (v - ((v >> 7) << 8)) * (int)((b >> (8u * j)) & 255u); } return c; }
#define IGM_DOT4U(a, b, c) igm_dot4u_sc((a), (b), (c))
#endif
#if defined(IGM_DOT4U) && !defined(IGM_DOT4S)
#undef IGM_DOT4U
#endif
#endif
#if defined(IGM_IH_AMD_WAVE) && defined(__AMDGCN__)
#define IGM_WAVE_INTRINSICS 1
static inline uint igm_wave_lane(void) { return (uint)__builtin_amdgcn_mbcnt_hi(~0u, __builtin_amdgcn_mbcnt_lo(~0u, 0u)); }
#if defined(IGM_IH_PL) && __has_builtin(__builtin_amdgcn_permlane16_var)
// the data-dependent group read on the VALU (the auditor's permlane item, 10 October 22:3x UK): gfx12's v_permlane16_var selects a
// lane within the 16-lane row by a per-lane VGPR index; every eight-lane group sits inside one row, so (simd_base + src) & 15 is the
// index and the DS unit is never issued for it. A compiler without the builtin keeps ds_bpermute (the row's isa record says which).
#define IGM_PL_NATIVE 1
#define IGM_BCAST(v, src) as_uint(__builtin_amdgcn_permlane16_var(as_int(v), as_int(v), (int)((simd_base + (src)) & 15u), false, true))
#else
#define IGM_BCAST(v, src) as_uint(__builtin_amdgcn_ds_bpermute((int)((simd_base + (src)) << 2), as_int(v)))
#endif
#if defined(IGM_IH_DPP)
// the fixed-pattern exchange as a DPP move (the auditor's item, 10 October 21:4x UK): the xor-partner levels 1, 2, 4 and 8 stay
// inside a DPP row of 16 lanes, so row_xmask (0x160 | m) does the partner read on the VALU with no DS issue and no lgkmcnt
// wait; level 16 (W1024's one-wave tree only) stays a swizzle. The data-dependent broadcasts stay on ds_bpermute.
#define IGM_XCHX(v, m) ((m) < 16u ? as_uint(__builtin_amdgcn_update_dpp(0, as_int(v), 0x160 | (int)(m), 0xf, 0xf, true)) : as_uint(__builtin_amdgcn_ds_swizzle(as_int(v), 0x1f | ((m) << 10))))
#else
#define IGM_XCHX(v, m) as_uint(__builtin_amdgcn_ds_swizzle(as_int(v), 0x1f | ((m) << 10)))
#endif
#define IGM_ST_SYNC() barrier(CLK_GLOBAL_MEM_FENCE)
// the wave-wide read for the span form (the source anywhere in the 32-lane wave): the permlane pair across the two rows under
// IGM_IH_PL, ds_bpermute otherwise; the wave-wide xor partner at distance 16 is a swizzle (DPP rows are 16 lanes)
#if defined(IGM_PL_NATIVE)
#define IGM_WBCAST(v, src) ((((src) >> 4u) == (igm_wl >> 4u)) ? as_uint(__builtin_amdgcn_permlane16_var(as_int(v), as_int(v), (int)((src) & 15u), false, true)) : as_uint(__builtin_amdgcn_permlanex16_var(as_int(v), as_int(v), (int)((src) & 15u), false, true)))
#else
#define IGM_WBCAST(v, src) as_uint(__builtin_amdgcn_ds_bpermute((int)((src) << 2), as_int(v)))
#endif
#define IGM_XCHX16(v) as_uint(__builtin_amdgcn_ds_swizzle(as_int(v), 0x1f | (16u << 10)))
#elif IGM_FH_SHUFFLE
#define IGM_BCAST(v, src) sub_group_shuffle((v), simd_base + (src))
#define IGM_XCHX(v, m) sub_group_shuffle_xor((v), (m))
#define IGM_ST_SYNC() sub_group_barrier(CLK_GLOBAL_MEM_FENCE)
#else
#define IGM_BCAST(v, src) igm_fh_xch(xch, group, lane, (v), (src))
#define IGM_XCHX(v, m) igm_fh_xch(xch, group, lane, (v), lane ^ (m))
#define IGM_ST_SYNC() barrier(CLK_GLOBAL_MEM_FENCE)
#endif
#define IGM_CONSTANT_U32
#define IGM_LOAD4(p) vload4(0, (p))
// a 16-byte load the compiler may take as aligned (the macro-item's steps are 16-byte aligned by construction): one
// global_load_b128 on AMD where vload4 over a uint pointer may split into four dword loads
#define IGM_LOAD4A(p) (*(__global const uint4 *)(p))
#define IGM_STORE4(v, p) vstore4((v), 0, (p))
#define IGM_LOCAL __local
// the Riptide form 2 staging area: this work-group's local words past the exchange words (the host sizes arg 6 to both)
#define IGM_STAGE_BASE (xch + get_local_size(0))
#if IGM_FH_SHUFFLE
#define IGM_LOCAL_SYNC() sub_group_barrier(CLK_LOCAL_MEM_FENCE)
#else
#define IGM_LOCAL_SYNC() barrier(CLK_LOCAL_MEM_FENCE)
#endif
// RXE1: sixteen work-items per nonce; on a runtime without sub-group shuffles the exchange goes through local
// memory under workgroup barriers (every work-item of the group reaches every barrier: the wrappers run the body
// on padding items too); with shuffles the sub-group path, the source indexed inside the nonce's 16-lane group
static inline uint igm_xchg(__local uint *xch, uint group, uint lane, uint gsz, uint v, uint src) {
    barrier(CLK_LOCAL_MEM_FENCE);
    xch[group * gsz + lane] = v;
    barrier(CLK_LOCAL_MEM_FENCE);
    return xch[group * gsz + src];
}
#if IGM_FH_SHUFFLE
#define IGM_XCH16_PARAMS uint simd_base16, __local uint *xch16, uint group16
#define IGM_BCAST16(v, src) sub_group_shuffle((v), simd_base16 + (src))
#define IGM_ST_SYNC16() sub_group_barrier(CLK_GLOBAL_MEM_FENCE)
#else
#define IGM_XCH16_PARAMS uint simd_base16, __local uint *xch16, uint group16
#define IGM_BCAST16(v, src) igm_xchg(xch16, group16, lane, igm_gsz, (v), (src))
#define IGM_ST_SYNC16() barrier(CLK_GLOBAL_MEM_FENCE)
#endif
#ifndef mulhi
#define mulhi(a, b) mul_hi((a), (b))
#endif

// the residency pin: IGM_IH_OCC N asks the AMD compiler for N waves per SIMD (amdgpu_waves_per_eu); elsewhere nothing
#if defined(IGM_IH_OCC) && defined(__AMDGCN__)
#define IGM_KERNEL_ATTR __attribute__((amdgpu_waves_per_eu(IGM_IH_OCC, IGM_IH_OCC)))
#elif defined(IGM_INTEL)
// Intel: the search kernel's sub-group width pinned to 16 (cl_intel_required_subgroup_size, NEO): on the inlined body the compiler
// chose SIMD32 for the Strata W128 kernel and cost it a quarter of its rate (7.32 to 5.4 MH/s, tune-2 on the B580, 11 October 2026)
// while the IgneumHash kernel stayed at SIMD16; IGM_INTEL_SIMD overrides the width (8, 16 or 32) for a screen
#ifndef IGM_INTEL_SIMD
#define IGM_INTEL_SIMD 16
#endif
#pragma OPENCL EXTENSION cl_intel_required_subgroup_size : enable
#define IGM_KERNEL_ATTR __attribute__((intel_reqd_sub_group_size(IGM_INTEL_SIMD)))
#else
#define IGM_KERNEL_ATTR
#endif
// version 10 (R0E): the octet's barriers for the tile (local) and the gather table (global)
#define IGM_R0E_BARRIER() barrier(CLK_LOCAL_MEM_FENCE)
#define IGM_R0E_GBARRIER() barrier(CLK_GLOBAL_MEM_FENCE)
// the per-operation trace of the period programme (14 Oct 2026, the Metal round fault on versions 13 and 14): under IGM_R0E_OPTRACE the
// generated programme writes the lane's sixteen registers after every operation of the first two rounds (round r, op i, lane l at
// ((r * IGM_R0E_OPS + i) * 8 + l) * 16) to a global buffer the first octet alone owns; the same text on both hands, the exact hand the witness
#ifndef IGM_R0E_OPTRACE
#define IGM_R0E_OPTRACE 0
#endif
#if IGM_R0E_OPTRACE
#define IGM_R0E_OPTRACE_PARAM , __global uint *optr
#define IGM_R0E_OPTRACE_ARG , optr
#define IGM_R0E_OPTRACE_LANE_PARAM , __global uint *optr_in
#define IGM_R0E_OPTRACE_LANE_ARG , optr_in
#define IGM_R0E_OPTRACE_CALL , ((r < 2u && optr_in) ? optr_in + (ulong)r * IGM_R0E_OPS * 128u : (__global uint *)0)
#define IGM_R0E_OPTRACE_WRITE(i) do { if (optr) { for (uint q_ = 0; q_ < 16u; q_++) optr[((i) * 8u + lane) * 16u + q_] = R[q_]; } } while (0)
#else
#define IGM_R0E_OPTRACE_PARAM
#define IGM_R0E_OPTRACE_ARG
#define IGM_R0E_OPTRACE_LANE_PARAM
#define IGM_R0E_OPTRACE_LANE_ARG
#define IGM_R0E_OPTRACE_CALL
#define IGM_R0E_OPTRACE_WRITE(i) do { } while (0)
#endif

#if IGM_IH_VERSION == 17
// VERSION 17, the texture form: the probe's sampler (NEAREST at the unnormalised texel centre, clamp) and the texture argument
__constant sampler_t igm_r17_smp = CLK_NORMALIZED_COORDS_FALSE | CLK_ADDRESS_CLAMP_TO_EDGE | CLK_FILTER_NEAREST;
#define IGM_R17_TEX_PARAM __read_only image2d_t tex
#define IGM_R17_SAMPLE(px, py) (read_imageui(tex, igm_r17_smp, (float2)((float)(px) + 0.5f, (float)(py) + 0.5f)).x)
#endif
#if IGM_IH_VERSION == 20 || IGM_IH_VERSION == 21 || IGM_IH_VERSION == 25
#define IGM_FORGE_BARRIER() barrier(CLK_LOCAL_MEM_FENCE)
// the period table's scalar path needs the subgroup functions: enabled where the platform defines cl_khr_subgroups (rusticl's clang
// refused the bare calls: "sub_group_broadcast is ambiguous", "sub_group_all undeclared", the 9060 XT 15 Oct 2026 11:5x UK); without
// them every attempt takes the vector path (exact either way)
#if defined(cl_khr_subgroups)
#pragma OPENCL EXTENSION cl_khr_subgroups : enable
#define IGM_FORGE_BCAST0(v) sub_group_broadcast((uint)(v), (uint)0)   // the period table's shape index from lane 0
#define IGM_FORGE_ALL(c) (sub_group_all((int)(c)) != 0)
#else
#define IGM_FORGE_BCAST0(v) (v)
#define IGM_FORGE_ALL(c) (0)
#endif
#endif
