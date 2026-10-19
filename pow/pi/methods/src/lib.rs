//! The guest's ELF and image id, embedded by risc0-build: `PI_GUEST_ELF` and `PI_GUEST_ID` (the consensus
//! constant `PI_IMAGE_ID` of SPEC-R0A-v0.md section 0.9.1 is `PI_GUEST_ID` of the landed build).
include!(concat!(env!("OUT_DIR"), "/methods.rs"));
