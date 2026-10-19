//! money-witness: a synthetic two-in two-out spend witness at a given depth from one seed (the
//! measurement rows; nothing here is a wallet). The two inputs are real notes under a random path;
//! the outputs split the inputs less a fee and a public value.
use money_stage1_prover as money;
use miden_core::Word;

struct Rng(u64);
impl Rng {
    fn next(&mut self) -> u64 {
        self.0 = self.0.wrapping_add(0x9E37_79B9_7F4A_7C15);
        let mut z = self.0;
        z = (z ^ (z >> 30)).wrapping_mul(0xBF58_476D_1CE4_E5B9);
        z = (z ^ (z >> 27)).wrapping_mul(0x94D0_49BB_1331_11EB);
        z ^ (z >> 31)
    }
    fn word(&mut self) -> Word {
        money::word([self.next() % money::P, self.next() % money::P, self.next() % money::P, self.next() % money::P])
    }
}

fn h(w: Word) -> String {
    hex::encode(money::word_to_bytes(w))
}

fn main() {
    let args: Vec<String> = std::env::args().collect();
    if args.len() < 3 {
        eprintln!("usage: money-witness <seed> <out.json> [chain_id]");
        std::process::exit(2);
    }
    let seed: u64 = args[1].parse().expect("seed");
    let chain_id: u64 = args.get(3).map(|s| s.parse().expect("chain id")).unwrap_or(7402);
    let mut rng = Rng(seed);
    let depth = money::DEPTH as usize;
    // Two notes side by side at indexes 2k and 2k+1 share every sibling above the leaf level.
    let base = (rng.next() & ((1u64 << depth) - 1)) & !1u64;
    let values = [1_000_000_000_000_000_000u128 * (1 + (rng.next() % 7) as u128), 500_000_000_000_000_000u128 * (1 + (rng.next() % 5) as u128)];
    let sks = [rng.word(), rng.word()];
    let nks = [rng.word(), rng.word()];
    let rs = [rng.word(), rng.word()];
    let cms: Vec<Word> = (0..2).map(|i| money::commitment(values[i], money::pk(sks[i], nks[i]), rs[i])).collect();
    let upper: Vec<Word> = (1..depth).map(|_| rng.word()).collect();
    let mut anchor = money::merge(cms[0], cms[1]);
    let mut idx = base >> 1;
    for s in &upper {
        anchor = if idx & 1 == 0 { money::merge(anchor, *s) } else { money::merge(*s, anchor) };
        idx >>= 1;
    }
    let path = |i: usize| -> Vec<String> {
        let mut p = vec![h(cms[1 - i])];
        p.extend(upper.iter().map(|w| h(*w)));
        p
    };
    let fee: u128 = 1_000;
    let vpub: u128 = values[0] / 4;
    let rest = values[0] + values[1] - fee - vpub;
    let outs = [rest / 3, rest - rest / 3];
    let opks = [rng.word(), rng.word()];
    let ors = [rng.word(), rng.word()];
    let ocms: Vec<Word> = (0..2).map(|j| money::commitment(outs[j], opks[j], ors[j])).collect();
    let mut addr = [0u8; 20];
    addr[16..].copy_from_slice(&0xae91_5001u32.to_be_bytes());
    let w = money::SpendWitness {
        statement: "money-spend-v1".into(),
        chain_id,
        anchor: h(anchor),
        nullifiers: (0..2).map(|i| h(money::nullifier(nks[i], cms[i], base + i as u64))).collect(),
        commitments: ocms.iter().map(|c| h(*c)).collect(),
        public_value: vpub.to_string(),
        fee: fee.to_string(),
        public_output: hex::encode(addr),
        inputs: (0..2)
            .map(|i| money::InputWitness { value: values[i].to_string(), sk_spend: h(sks[i]), nk: h(nks[i]), r: h(rs[i]), index: base + i as u64, path: path(i) })
            .collect(),
        outputs: (0..2).map(|j| money::OutputWitness { value: outs[j].to_string(), pk: h(opks[j]), r: h(ors[j]) }).collect(),
    };
    std::fs::write(&args[2], serde_json::to_string_pretty(&w).unwrap()).expect("write");
    eprintln!("witness seed {seed} chain {chain_id} indexes {base} {} anchor {}", base + 1, h(anchor));
}
