// prelude_msl.inc: the Metal dialect for the shared kernel bodies.
#include <metal_stdlib>
#include <metal_simdgroup_matrix>
using namespace metal;
#pragma METAL fp contract(off)
#define IGM_U2F(x) as_type<float>(x)
#define IGM_MULHI(a, b) mulhi((uint)(a), (uint)(b))
#define IGM_F2U(x) as_type<uint>(x)
// the generated programs' variable right-rotate (progpow_gen.cpp math_code case 5): the same text as before, under the name
// the OpenCL prelude rewrites on Intel platforms (the rotate fold); no Metal row changes
#define IGM_ROTR_VAR(x, n) rotate((x), (32u - (((n)) & 31u)) & 31u)
// The keyed share helper is NOT inlined on Metal: inlined into the round loop its temporaries (km, kk, ko, the share words) raised the
// live set past what the M5 Max's compiler handles, and the production kernel's digest came out with its first word zero (the mix exact,
// the trace build exact in every round: the Orchard lane's fault of 10 Oct in a new place; exact on the M6 either way); 12 Oct 2026.
#define IGM_KEYED_FN __attribute__((noinline))
// The digest's eight stores go through a volatile pointer on Metal: the M5 Max's compiler dropped the digest's first word on a keyed
// document (the mix exact, F[0] zero, the trace build exact), and a volatile store cannot be dropped or reordered past the wrapper's
// read; the M6 is unaffected either way; 12 Oct 2026.
#define IGM_FINAL_STORE(dst, q, v) do { volatile IGM_PRIV uint *igm_vf = (dst); igm_vf[q] = (v); } while (0)
// The keyed digest's sink (13 Oct 2026): the eight words written to the nonce's device state area (free in the stream form) before the private
// store, the store the trace build makes and the M6's compiler keeps the first word for; the other preludes define it empty.
#define IGM_FINAL_SINK(area, f) do { device uint *igm_sk = (area); for (uint q_ = 0; q_ < 8u; q_++) igm_sk[q_] = (f)[q_]; } while (0)
#define IGM_UNROLL(n)   // the OpenCL prelude's static-index hint (the Intel step); no Metal row changes (a pragma here met the bare attribute in the hash kernel: the Orchard build 150)
#define IGM_UNROLL_FULL(n) _Pragma("unroll")   // the version 16 register loops: clang's unroll pragma under Metal (13 Oct 2026: the 224-register array must never meet a runtime index)
#define IGM_GLOBAL device
#define IGM_PRIV thread
#define IGM_CONST constant
// eight-lane broadcast for the FishHash lane kernel: a SIMD shuffle from the lane group's
// base (the body receives the base as simd_base; the extra parameter is unused on Metal)
#define IGM_INLINE inline
#define IGM_XCH_PARAMS uint simd_base, uint xch_unused
#define IGM_XCH_ARGS simd_base, xch_unused   // forwards IGM_XCH_PARAMS to a callee
#define IGM_BCAST(v, src) simd_shuffle((v), (ushort)(simd_base + (src)))
#define IGM_XCHX(v, m) simd_shuffle_xor((v), (ushort)(m))   // the xor-partner exchange of the reduction tree (the eight lanes are SIMD-aligned)
#define IGM_LOAD4A(p) (*(device const uint4 *)(p))
// IgneumHash: the eight lanes of a nonce are one SIMD group's consecutive threads; a
// device-memory handoff between them needs the SIMD-group barrier with device scope
#define IGM_ST_SYNC() simdgroup_barrier(mem_flags::mem_device)
#define IGM_CONSTANT_U32 thread
#define IGM_LOAD4(p) (*(device const uint4 *)(p))
#define IGM_LOCAL threadgroup
// Orchard workstream 2: a shuffle across the whole 32-wide SIMD group by lane number (the lane groups' bases are 0, 8, 16, 24
// of one SIMD group: simd_base is simd_lane & ~7); the SIMD-group-wide macro-item loader (IGM_IH_DIAG_SG) is Metal only
#define IGM_SGBCAST(v, L) simd_shuffle((v), (ushort)(L))
#define IGM_SG_LANES 32u
#define opencl_unroll_hint   /* the unroll attribute is OpenCL C's; Metal unrolls constant loops on its own */
#if defined(IGM_IH_V4_FORM) && IGM_IH_V4_FORM == 2
#error "the Riptide form 2 (local-memory staging) is OpenCL only; Metal runs form 1"
#endif
// RXE1: the sixteen logical lanes of a nonce are sixteen consecutive threads of one SIMD group (two nonces per
// 32-wide group); the shuffle source is indexed inside the nonce's half
#define IGM_XCH16_PARAMS uint simd_base16, uint xch16_unused
#define IGM_BCAST16(v, src) simd_shuffle((v), (ushort)(simd_base16 + (src)))
#define IGM_ST_SYNC16() simdgroup_barrier(mem_flags::mem_device)
// version 10 (R0E): the octet's barriers for the tile (threadgroup) and the gather table (device); the eight lanes sit in one SIMD group
#define IGM_R0E_BARRIER() simdgroup_barrier(mem_flags::mem_threadgroup)
#define IGM_R0E_GBARRIER() simdgroup_barrier(mem_flags::mem_device)
// the per-operation trace of the period programme (14 Oct 2026, the Metal round fault on versions 13 and 14): under IGM_R0E_OPTRACE the
// generated programme writes the lane's sixteen registers after every operation of the first two rounds (round r, op i, lane l at
// ((r * IGM_R0E_OPS + i) * 8 + l) * 16) to a global buffer the first octet alone owns; the same text on both hands, the exact hand the witness
#ifndef IGM_R0E_OPTRACE
#define IGM_R0E_OPTRACE 0
#endif
#if IGM_R0E_OPTRACE
#define IGM_R0E_OPTRACE_PARAM , device uint *optr
#define IGM_R0E_OPTRACE_ARG , optr
#define IGM_R0E_OPTRACE_LANE_PARAM , device uint *optr_in
#define IGM_R0E_OPTRACE_LANE_ARG , optr_in
#define IGM_R0E_OPTRACE_CALL , ((r < 2u && optr_in) ? optr_in + (size_t)r * IGM_R0E_OPS * 128u : (device uint *)0)
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
// VERSION 17, the texture form: the probe's sampler (filter::nearest at the unnormalised texel centre, coord::pixel, clamp) and the texture argument
constexpr sampler igm_r17_smp(coord::pixel, filter::nearest, address::clamp_to_edge);
#define IGM_R17_TEX_PARAM texture2d<uint, access::sample> tex
#define IGM_R17_SAMPLE(px, py) (tex.sample(igm_r17_smp, float2(float(px) + 0.5f, float(py) + 0.5f)).x)
#endif
#if IGM_IH_VERSION == 20 || IGM_IH_VERSION == 21 || IGM_IH_VERSION == 25
#define IGM_FORGE_BARRIER() threadgroup_barrier(mem_flags::mem_threadgroup)
#define IGM_FORGE_BCAST0(v) (v)
#define IGM_FORGE_ALL(c) (true)
#endif
