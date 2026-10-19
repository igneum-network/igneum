// The period program of the KAWPOW family, generated on the host exactly as
// the reference's mix_rng_state draws it (pow/third_party/.../progpow.cpp),
// emitted as CUDA source with literal register indices.
#pragma once

#include <cstdint>
#include <string>

namespace igneum {

// period_number = height / 3 for the KAWPOW constants.
std::string progpow_program_source(uint64_t period_number);

// The full device source: typedefs, the embedded keccak permutations, the
// kernels, with PROGPOW_PROGRAM replaced by the period's program.
std::string device_source(uint64_t period_number, bool epic = false);  // epic: ProgPoW 0.9.2 for progpow-epic (period 50, cache 12, math 20, 256-parent nodes, plain keccak seeding)

// The Autolykos v2 device source (typedefs plus the embedded kernels; no period).
std::string device_source_autolykos();

// The FishHash device source (the KAWPOW source at period 0 plus the FishHash kernel).
std::string device_source_fishhash();

// The IgneumHash-R0A device source: the FishHash source (BLAKE3 helpers) plus the R0A kernels, with the
// variant's defines in front (dataset items, scratch words, rounds, lanes).
std::string device_source_igneumhash(const std::string& variant_defines);  // the IH_* defines of the variant (engine::ih_load)
std::string device_source_rxe1(const std::string& defines);  // the IH_* defines of the dataset builder plus the RX_* defines (engine::rx_load)

}  // namespace igneum
