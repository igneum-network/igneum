//! Verifies a proof of a statement against its claim (the claim.json money-prove writes); prints the
//! median time and the conjectured security report. Links only the Miden verifier: no assembler, no prover.
use std::time::Instant;

use miden_core::{
    Felt, Word,
    program::{ExecutionClaim, ProgramInfo, StackInputs, StackOutputs},
    proof::ExecutionProof,
};
use miden_verifier::Verifier;
use serde::Deserialize;

#[derive(Deserialize)]
struct Claim {
    program_hash: [u64; 4],
    stack_inputs: Vec<u64>,
    stack_outputs: Vec<u64>,
}

fn main() {
    let args: Vec<String> = std::env::args().collect();
    if args.len() < 3 {
        eprintln!("usage: money-verify <proof.bin> <claim.json> [runs]");
        std::process::exit(2);
    }
    let bytes = std::fs::read(&args[1]).expect("proof");
    let c: Claim = serde_json::from_str(&std::fs::read_to_string(&args[2]).expect("claim")).expect("json");
    let runs: usize = args.get(3).map(|s| s.parse().expect("runs")).unwrap_or(1);
    let program_hash = Word::new(c.program_hash.map(Felt::new_unchecked));
    let inputs: Vec<Felt> = c.stack_inputs.iter().map(|v| Felt::new_unchecked(*v)).collect();
    let outputs: Vec<Felt> = c.stack_outputs.iter().map(|v| Felt::new_unchecked(*v)).collect();
    let claim = ExecutionClaim::from_program_info(
        ProgramInfo::new(program_hash, Default::default()),
        StackInputs::new(&inputs).expect("inputs"),
        StackOutputs::new(&outputs).expect("outputs"),
    );
    let mut times = Vec::new();
    let mut report = None;
    for _ in 0..runs {
        let t0 = Instant::now();
        let proof = ExecutionProof::read_from_bytes(&bytes).expect("proof bytes");
        let outcome = Verifier::new().verify(&claim, &proof).expect("verification failed");
        times.push(t0.elapsed().as_secs_f64() * 1000.0);
        assert!(outcome.is_complete());
        report = Some(*outcome.vm_security_parameters());
    }
    times.sort_by(|a, b| a.partial_cmp(b).unwrap());
    let params = report.unwrap();
    println!(
        "verified {} runs: median {:.3} ms, min {:.3} ms, max {:.3} ms, proof {} bytes, conjectured security {} bits",
        runs,
        times[runs / 2],
        times[0],
        times[runs - 1],
        bytes.len(),
        params.conjectured_security_level()
    );
    let rep = params.conjectured_security_report();
    for t in rep.terms() {
        println!("  term {:<28} {} bits (fixed point {})", t.label, t.bits >> miden_air::security::FIXED_POINT_FRACTIONAL_BITS, t.bits);
    }
    println!("  log_final_degree {} instance {:?}", params.log_final_degree, params.instance_shape);
}
