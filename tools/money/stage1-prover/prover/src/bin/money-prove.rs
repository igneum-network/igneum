//! money-prove: a witness (money-spend-v1 or money-shield-v1, JSON) in, the proof bytes and the public
//! claim (the program hash, the stack inputs, the stack outputs) out; the execute and prove times on
//! standard output. `runs` repeats the proving for a median.
use money_stage1_prover as money;
use serde::Serialize;

#[derive(Serialize)]
struct Claim {
    statement: String,
    kind: u64,
    hasher: String,
    program_hash: [u64; 4],
    statement_hash: String,
    stack_inputs: Vec<u64>,
    stack_outputs: Vec<u64>,
}

fn median(v: &mut Vec<f64>) -> f64 {
    v.sort_by(|a, b| a.partial_cmp(b).unwrap());
    v[v.len() / 2]
}

fn main() {
    let args: Vec<String> = std::env::args().collect();
    if args.len() < 4 {
        eprintln!("usage: money-prove <witness.json> <proof.bin> <claim.json> [runs]");
        std::process::exit(2);
    }
    let text = std::fs::read_to_string(&args[1]).expect("witness");
    let v: serde_json::Value = serde_json::from_str(&text).expect("json");
    let statement = v.get("statement").and_then(|s| s.as_str()).unwrap_or("").to_string();
    let prepared = match statement.as_str() {
        "money-spend-v1" => money::prepare_spend(&serde_json::from_value(v).expect("spend witness")),
        "money-shield-v1" => money::prepare_shield(&serde_json::from_value(v).expect("shield witness")),
        other if money::vault::kind_of(other).is_some() => money::vault::prepare_vault(&serde_json::from_value(v).expect("vault witness")),
        other => Err(format!("unknown statement {other:?}")),
    };
    let p = match prepared {
        Ok(p) => p,
        Err(e) => {
            eprintln!("money-prove: the witness is refused: {e}");
            std::process::exit(1);
        }
    };
    let runs: usize = args.get(4).map(|s| s.parse().expect("runs")).unwrap_or(1);
    let mut exec_ms = Vec::new();
    let mut prove_ms = Vec::new();
    let mut last = None;
    for run in 0..runs {
        match money::prove(&p) {
            Ok((bytes, outputs, e, pr)) => {
                println!("run {run}: execute {e:.3} ms, prove {pr:.1} ms, proof {} bytes", bytes.len());
                exec_ms.push(e);
                prove_ms.push(pr);
                last = Some((bytes, outputs));
            },
            Err(e) => {
                eprintln!("money-prove: {e}");
                std::process::exit(1);
            },
        }
    }
    let (bytes, outputs) = last.unwrap();
    println!(
        "median: execute {:.3} ms, prove {:.1} ms, proof {} bytes, statement {}",
        median(&mut exec_ms),
        median(&mut prove_ms),
        bytes.len(),
        statement
    );
    std::fs::write(&args[2], &bytes).expect("write proof");
    let claim = Claim {
        statement,
        kind: p.kind,
        hasher: "blake3".into(),
        program_hash: money::ints(money::program_hash(p.kind)),
        statement_hash: hex::encode(money::word_to_bytes(p.statement_hash)),
        stack_inputs: money::ints(p.statement_hash).to_vec(),
        stack_outputs: outputs,
    };
    std::fs::write(&args[3], serde_json::to_string_pretty(&claim).unwrap()).expect("write claim");
}
