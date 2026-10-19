//! The C ABI the node calls (node/consensus/igneum/pool/miden/verifier.go):
//!   money_program_hash(kind, out[4]) -> 0, or 4 for an unknown kind (kinds 1 and 2 stage one's, 3 to 9 the vault's);
//!   money_verify(kind, proof, proof_len, statement_hash[4], min_bits, bits_out) -> 0 verified, else:
//!     1 the proof bytes do not parse, 2 the proof does not verify, 3 the proof's commitment hash is not
//!     BLAKE3, 4 unknown kind, 5 the proof's conjectured security is below min_bits, 6 a bad statement hash.
//! The program hashes come from the statements' assembly, included here from prover/masm (one source).
use std::sync::OnceLock;

use miden_assembly::Assembler;
use miden_core::{
    Felt, program::Program,
    program::{ExecutionClaim, ProgramInfo, StackInputs, StackOutputs},
    proof::{ExecutionProof, HashFunction},
};
use miden_verifier::Verifier;

const SPEND_MASM: &str = include_str!("../../prover/masm/spend.masm");
const SHIELD_MASM: &str = include_str!("../../prover/masm/shield.masm");
// The vault's seven statements (stage 2, cut 1): each is the prelude and its body, joined as prover/src/vault.rs
// joins them (the prelude, a newline, the body), so the program hashes agree with the prover's.
const VAULT_PRELUDE_MASM: &str = include_str!("../../prover/masm/vault-prelude.masm");
const VAULT_BODIES: [(&str, &str); 7] = [
    ("money-vault-create-v1", include_str!("../../prover/masm/vault-create.masm")),
    ("money-vault-withdraw-v1", include_str!("../../prover/masm/vault-withdraw.masm")),
    ("money-vault-cancel-v1", include_str!("../../prover/masm/vault-cancel.masm")),
    ("money-vault-complete-v1", include_str!("../../prover/masm/vault-complete.masm")),
    ("money-vault-announce-v1", include_str!("../../prover/masm/vault-announce.masm")),
    ("money-vault-policy-complete-v1", include_str!("../../prover/masm/vault-policy-complete.masm")),
    ("money-vault-rec-cancel-v1", include_str!("../../prover/masm/vault-rec-cancel.masm")),
];
const P: u64 = 0xFFFF_FFFF_0000_0001;

static SPEND: OnceLock<Program> = OnceLock::new();
static SHIELD: OnceLock<Program> = OnceLock::new();
static VAULT: [OnceLock<Program>; 7] = [const { OnceLock::new() }; 7];

fn assemble(name: &str, src: &str) -> Program {
    Assembler::default().assemble_program(name, src).expect("assemble").unwrap_program()
}

fn program(kind: u32) -> Option<&'static Program> {
    Some(match kind {
        1 => SHIELD.get_or_init(|| assemble("shield", SHIELD_MASM)),
        2 => SPEND.get_or_init(|| assemble("spend", SPEND_MASM)),
        3..=9 => {
            let (name, body) = VAULT_BODIES[(kind - 3) as usize];
            VAULT[(kind - 3) as usize].get_or_init(|| assemble(name, &format!("{VAULT_PRELUDE_MASM}\n{body}")))
        },
        _ => return None,
    })
}

/// # Safety
/// `out` points at four u64.
#[no_mangle]
pub unsafe extern "C" fn money_program_hash(kind: u32, out: *mut u64) -> i32 {
    let Some(p) = program(kind) else { return 4 };
    let h = p.hash();
    let e = h.as_elements();
    for i in 0..4 {
        *out.add(i) = e[i].as_canonical_u64();
    }
    0
}

/// # Safety
/// `proof` points at `proof_len` bytes; `sh` at four u64; `bits_out` at one u32 or is null.
#[no_mangle]
pub unsafe extern "C" fn money_verify(kind: u32, proof: *const u8, proof_len: usize, sh: *const u64, min_bits: u32, bits_out: *mut u32) -> i32 {
    let Some(program) = program(kind) else { return 4 };
    let bytes = std::slice::from_raw_parts(proof, proof_len);
    let mut e = [0u64; 4];
    for i in 0..4 {
        e[i] = *sh.add(i);
        if e[i] >= P {
            return 6;
        }
    }
    let felts = e.map(Felt::new_unchecked);
    let mut outs = [Felt::new_unchecked(0); 16];
    outs[..4].copy_from_slice(&felts);
    let Ok(stack_inputs) = StackInputs::new(&felts) else { return 6 };
    let Ok(stack_outputs) = StackOutputs::new(&outs) else { return 6 };
    let claim = ExecutionClaim::from_program_info(ProgramInfo::new(program.hash(), Default::default()), stack_inputs, stack_outputs);
    let Ok(proof) = ExecutionProof::read_from_bytes(bytes) else { return 1 };
    if proof.vm().proof.hash_fn() != HashFunction::Blake3_256 {
        return 3;
    }
    let verifier = Verifier::new();
    match std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| verifier.verify(&claim, &proof))) {
        Ok(Ok(outcome)) => {
            if !outcome.is_complete() {
                return 2;
            }
            let bits = outcome.vm_security_parameters().conjectured_security_level();
            if !bits_out.is_null() {
                *bits_out = bits;
            }
            if bits < min_bits {
                return 5;
            }
            0
        },
        _ => 2,
    }
}

/// The pinned release, for the node's log line (0.36.1 -> 3601).
#[no_mangle]
pub extern "C" fn money_verifier_version() -> u32 {
    3601
}
