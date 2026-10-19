/* ignpow_ext.h: three accessors the GPU workers need beyond pow/ref/include/ignpow.h,
 * proposed to GPU engineer A on 9 October 2026 (GPU engineer B). Until A lands them
 * in ignpow.h, ext_stub.cpp provides refusing stubs so the workers link, cannot pass a
 * vector, and say so. Same conventions as ignpow.h: 0 is IGNPOW_OK. */
#ifndef IGNPOW_EXT_H
#define IGNPOW_EXT_H

#include <stddef.h>
#include <stdint.h>
#include "ignpow.h"

#ifdef __cplusplus
extern "C" {
#endif

/* The light cache accessor is A's: ignpow_ctx_light_cache(ctx, &data, &bytes, &item_bytes)
 * in ignpow.h since gpu-a/design 94167ba. The two below remain proposed. */

/* One dataset item (the candidate's smallest item, 64 bytes for the Ethash lineage)
 * computed on the CPU from the context's light cache: the second opinion on the
 * worker's device-built dataset, checked on a sample before the dataset is trusted. */
int ignpow_ctx_dataset_item(const ignpow_ctx *ctx, uint64_t index, void *out, size_t cap);

/* The generated-program seed for a height (ProgPoW family: height / period), so the
 * worker derives nothing about the period itself. */
int ignpow_program_seed(const char *algo, const char *params_json, uint64_t height, uint64_t *seed);

#ifdef __cplusplus
}
#endif
#endif
