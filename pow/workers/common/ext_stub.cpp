// ext_stub.cpp: refusing stubs for ignpow_ext.h until A lands the accessors. Linked
// only when IGNEUM_REF_EXT is off. Every call fails with IGNPOW_E_ARG and the worker
// reports the vector run as unavailable, never as passed.
#include "ignpow_ext.h"

extern "C" {
int ignpow_ctx_dataset_item(const ignpow_ctx *, uint64_t, void *, size_t) { return IGNPOW_E_ARG; }
int ignpow_program_seed(const char *, const char *, uint64_t, uint64_t *s) { if (s) *s = 0; return IGNPOW_E_ARG; }
}
