//! Per-device resource coordination (review B F07, 8 October 2026): one place that says what a card does with its
//! memory, and who holds it. The modes, decided per device from measured figures:
//!
//! | Mode | When | Measured basis (docs/analysis/class-v6/coexist-rows.md, 8 October 2026; prover-tiers-real-cards.md) |
//! |---|---|---|
//! | simultaneous | a TESTED configuration with headroom: 24 GB or more (23,552 MiB, the old prover's threshold) | the shard prover peaked at 20,434 MiB alone and 22,210 beside the miner on the adopted shard; a chained aggregation 16,751 MiB with the miner resident; measured on 24 and 32 GB cards |
//! | time-share | under the tested line but the compressed shard proof fits ALONE | the 5.5 GiB miner holds 6,129 MiB resident; a compressed shard proof peaks at 7,532 MiB alone (8.2 s on a 4060 8 GB, 13.2 s on a 3060 12 GB) and dies in a device allocation beside the miner on both |
//! | mining-only | a complete paid job cannot fit even alone | under `PROOF_PEAK_MIB` plus the driver's own use |
//!
//! A 16 to 24 GB card runs time-share, not simultaneous: the sum (6,129 + 7,532 = 13,661 MiB) fits on paper and no
//! row has measured the two together there; a measured row moves the line (`TESTED_SIMULTANEOUS_MIB`).
//!
//! The lease table: a holder (the miner, the prover, an aggregation, a benchmark, the next-epoch preparation) takes a
//! reservation on a device; a second holder is admitted only when the mode allows it beside the first, or the first
//! has released. Pausing dispatch is not releasing memory: a lease ends only on `release`, which the engine calls
//! after the holder's PROCESS has exited and `nvidia-smi` reports the device's memory used under `FREE_MIB`.
//! Admission counts the clock: the transfer, the WSL start, the proof, the aggregation, the dataset rebuild and the
//! payment deadline (`Budget`), so a job that cannot pay before its deadline is refused rather than started.

use std::collections::HashMap;

/// The miner's resident set with the 5.5 GiB dataset (coexist-rows, both cards: 6,129 and 6,116 MiB).
pub const MINER_RESIDENT_MIB: u64 = 6_129;
/// A compressed shard proof's peak alone (coexist-rows: 7,525 on the 3060, 7,532 on the 4060).
pub const PROOF_PEAK_MIB: u64 = 7_532;
/// The driver's own use on an idle device (coexist-rows: 1 to 2 MiB idle) plus the display's: under this, the device
/// reads as free after a holder has exited.
pub const FREE_MIB: u64 = 1_024;
/// Headroom a simultaneous configuration keeps over its measured peak for the next epoch's preparation (the next
/// hour's program pack is exported beside the running one; the pack is small, the margin is the allocator's): 10%.
pub const HEADROOM_PCT: u64 = 10;
/// The line above which mining and proving together have been MEASURED (prover-tiers-real-cards.md: the adopted shard
/// beside the miner at 22,210 MiB on a 24 GB card; 30,039 on the 32 GB prototype shard): 23,552 MiB.
pub const TESTED_SIMULTANEOUS_MIB: u64 = 23_552;
/// The engine waits this long for a device to read free after a holder exits before it refuses the next admission.
pub const FREE_WAIT_S: u64 = 60;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Mode {
    /// the miner and the prover share the card: a tested configuration with headroom
    Simultaneous,
    /// one at a time: the miner's dataset is evicted (its process exits), the memory is confirmed free, the prover
    /// runs, the dataset is rebuilt, mining resumes
    TimeShare,
    /// the card mines; no complete paid proof job fits on it even alone
    MiningOnly,
    /// a card that is not mining proves alone (the switch is off on it)
    ProveOnly,
}

impl Mode {
    pub fn word(self) -> &'static str {
        match self {
            Mode::Simultaneous => "simultaneous",
            Mode::TimeShare => "time-share",
            Mode::MiningOnly => "mining-only",
            Mode::ProveOnly => "prove-only",
        }
    }
}

/// The mode of one NVIDIA device from its memory and whether it mines. `vram_mib` as nvidia-smi reports it.
pub fn mode(vram_mib: u64, mining: bool) -> Mode {
    let proof_alone_fits = vram_mib >= PROOF_PEAK_MIB + FREE_MIB;
    if !mining {
        return if proof_alone_fits { Mode::ProveOnly } else { Mode::MiningOnly };
    }
    if vram_mib >= TESTED_SIMULTANEOUS_MIB && vram_mib * 100 >= (MINER_RESIDENT_MIB + PROOF_PEAK_MIB) * (100 + HEADROOM_PCT) {
        return Mode::Simultaneous;
    }
    if proof_alone_fits { Mode::TimeShare } else { Mode::MiningOnly }
}

/// One sentence for the window and the log.
pub fn line(name: &str, vram_mib: u64, m: Mode) -> String {
    let gb = (vram_mib + 512) / 1024;
    match m {
        Mode::Simultaneous => format!("{name}: proves while it mines ({gb} GB; a tested configuration with headroom)."),
        Mode::TimeShare => format!("{name}: one at a time ({gb} GB): the miner steps off the card, the memory is confirmed free, the proof runs, the miner rebuilds its dataset and resumes."),
        Mode::MiningOnly => format!("{name}: mines only ({gb} GB): a complete paid proof does not fit even alone."),
        Mode::ProveOnly => format!("{name}: proves alone ({gb} GB; not mining)."),
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
pub enum Holder {
    Miner,
    Prover,
    Aggregation,
    Benchmark,
    /// the next epoch's program preparation
    Prepare,
}

#[derive(Clone, Debug, PartialEq)]
pub struct Lease {
    pub holder: Holder,
    pub mib: u64,
    pub since: f64,
}

#[derive(Clone, Debug, PartialEq)]
pub enum Refusal {
    /// another holder has the device and the mode does not allow them side by side
    Held { by: Holder, mode: Mode },
    /// the device has not read free since the last holder exited (pausing dispatch is not releasing memory)
    NotFree { used_mib: u64 },
    /// the job cannot pay before its deadline
    Deadline { needs_s: f64, has_s: f64 },
    /// a complete paid job never fits on this device
    NoFit,
}

/// The clock a proof job needs, in seconds, against the payment deadline. The figures are the engine's measured or
/// configured ones; the defaults are coexist-rows and the prover log of 8 October 2026 (approximate where noted).
#[derive(Clone, Debug)]
pub struct Budget {
    /// moving the inputs to the prover (the shard's bodies and witnesses): the 3060 row's wall minus prove, 18 s
    pub transfer_s: f64,
    /// the host process start (a WSL2 process on Windows; 0 on Linux)
    pub startup_s: f64,
    /// the proof itself
    pub prove_s: f64,
    /// the aggregation step when the job is a segment (0 for a shard)
    pub aggregate_s: f64,
    /// the miner's dataset rebuild after a time-share (the worker's export and self-test; approximate, from the worker's
    /// own ready line on the first Windows machine)
    pub rebuild_s: f64,
    /// seconds left to the payment deadline
    pub deadline_s: f64,
}

impl Budget {
    pub fn needs_s(&self) -> f64 {
        self.transfer_s + self.startup_s + self.prove_s + self.aggregate_s + self.rebuild_s
    }
}

/// The job the host is admitted for (one per spawn): a shard proof, a segment aggregation, a whole chain run.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Workload {
    Shard,
    Aggregate,
    Chain,
}

impl Workload {
    pub fn word(self) -> &'static str {
        match self {
            Workload::Shard => "shard",
            Workload::Aggregate => "aggregate",
            Workload::Chain => "chain",
        }
    }
}

/// The exit code the host answers with when the budget is under its profile's floor (the V6-07 sub-lane's contract,
/// 8 October 2026): the engine records it on the lease and the card reads "proving needs N GB free".
pub const HOST_FLOOR_EXIT: i32 = 78;

/// The five variables every igneum-prove-host spawn carries, exactly as the host reads them (the shipper's contract with
/// the V6-07 sub-lane, 8 October 2026): the card's ordinal as the host enumerates it, the one workload admitted, the free
/// memory the engine read at admission, the lease's grant (the host's hard ceiling) and the lease's deadline.
pub fn host_env(device: u32, workload: Workload, free_mib: u64, budget_mib: u64, deadline_s: u64) -> Vec<(&'static str, String)> {
    vec![
        ("IGNEUM_PROVE_DEVICE", device.to_string()),
        ("IGNEUM_PROVE_WORKLOAD", workload.word().to_string()),
        ("IGNEUM_PROVE_MEM_FREE_MB", free_mib.to_string()),
        ("IGNEUM_PROVE_MEM_BUDGET_MB", budget_mib.to_string()),
        ("IGNEUM_PROVE_DEADLINE_S", deadline_s.to_string()),
    ]
}

/// The lease's grant for a proof on a card of `vram_mib` under `mode`: beside the miner the measured peak plus the
/// headroom; alone on the card everything above the free floor; nothing on a card that only mines.
pub fn proof_budget_mib(vram_mib: u64, mode: Mode) -> u64 {
    match mode {
        Mode::Simultaneous => PROOF_PEAK_MIB + PROOF_PEAK_MIB * HEADROOM_PCT / 100,
        Mode::TimeShare | Mode::ProveOnly => vram_mib.saturating_sub(FREE_MIB),
        Mode::MiningOnly => 0,
    }
}

/// The lease's figure for a spawn: the grant the mode allows, never more than the card reads free (the V6-07 sub-lane's
/// 12 GB row: 6,159 MiB free beside the miner, under the 8,285 MiB grant).
pub fn lease_mib(vram_mib: u64, mode: Mode, free_mib: u64) -> u64 {
    proof_budget_mib(vram_mib, mode).min(free_mib)
}

/// The card's words after exit 78: the figure the host printed ("needs N GB" or "needs N MB"/"MiB"; the host's own line
/// is "RESULT memory_profile refused: proving needs N GB free on the card for the <workload> workload (<floor> MiB
/// floor); <free> MiB free") when it did, else the budget it was offered, in whole GB rounded up.
pub fn floor_refusal_words(out: &str, budget_mib: u64) -> String {
    let printed = out.lines().rev().find_map(|l| {
        let i = l.find("needs ")?;
        let rest = &l[i + 6..];
        let n: String = rest.chars().take_while(|c| c.is_ascii_digit()).collect();
        let n = n.parse::<u64>().ok().filter(|n| *n > 0)?;
        let unit = rest[n.to_string().len()..].trim_start();
        Some(if unit.starts_with("GB") || unit.starts_with("GiB") { n } else { n.div_ceil(1024) })
    });
    let gb = printed.unwrap_or(budget_mib.div_ceil(1024));
    format!("proving needs {gb} GB free")
}

#[derive(Default, Debug)]
pub struct Coordinator {
    leases: HashMap<String, Vec<Lease>>,
    /// a device whose last holder exited and whose memory has not read free yet: the last reading
    pending_free: HashMap<String, u64>,
}

impl Coordinator {
    pub fn new() -> Self {
        Self::default()
    }

    pub fn leases(&self, device: &str) -> &[Lease] {
        self.leases.get(device).map(|v| v.as_slice()).unwrap_or(&[])
    }

    /// Whether `holder` may take `mib` on `device` under `mode` now. The miner's lease and the prover's coexist only in
    /// the simultaneous mode; an aggregation, a benchmark or the next-epoch preparation never shares a time-shared
    /// device with a prover; the preparation shares with the miner (it is the miner's own next pack).
    pub fn admit(&mut self, device: &str, holder: Holder, mib: u64, mode: Mode, now: f64, budget: Option<&Budget>) -> Result<Lease, Refusal> {
        if mode == Mode::MiningOnly && matches!(holder, Holder::Prover | Holder::Aggregation) {
            return Err(Refusal::NoFit);
        }
        if let Some(used) = self.pending_free.get(device).copied() {
            if used >= FREE_MIB {
                return Err(Refusal::NotFree { used_mib: used });
            }
        }
        if let Some(b) = budget {
            if b.needs_s() > b.deadline_s {
                return Err(Refusal::Deadline { needs_s: b.needs_s(), has_s: b.deadline_s });
            }
        }
        let held = self.leases(device).to_vec();
        for l in &held {
            if l.holder == holder {
                return Ok(l.clone()); // idempotent: the same holder keeps its lease
            }
            let beside_ok = match (l.holder, holder) {
                (Holder::Miner, Holder::Prepare) | (Holder::Prepare, Holder::Miner) => true,
                // the next epoch's pack is small; it rides beside a prover only where the prover rides beside the miner
                (Holder::Prepare, Holder::Prover) | (Holder::Prover, Holder::Prepare) => mode == Mode::Simultaneous,
                (Holder::Miner, Holder::Prover) | (Holder::Prover, Holder::Miner) => mode == Mode::Simultaneous,
                (Holder::Miner, Holder::Aggregation) | (Holder::Aggregation, Holder::Miner) => mode == Mode::Simultaneous,
                _ => false,
            };
            if !beside_ok {
                return Err(Refusal::Held { by: l.holder, mode });
            }
        }
        let lease = Lease { holder, mib, since: now };
        self.leases.entry(device.to_string()).or_default().push(lease.clone());
        self.pending_free.remove(device);
        Ok(lease)
    }

    /// The holder's process has exited; its lease ends only once the device reads free (`confirm_free`). Until then
    /// the device admits nobody new: pausing dispatch is not releasing memory.
    pub fn exited(&mut self, device: &str, holder: Holder) {
        if let Some(v) = self.leases.get_mut(device) {
            v.retain(|l| l.holder != holder);
        }
        self.pending_free.entry(device.to_string()).or_insert(u64::MAX);
    }

    /// A memory reading after an exit: under FREE_MIB the device is free again (when no lease remains); the reading
    /// is kept otherwise and the next admission is refused with it.
    pub fn confirm_free(&mut self, device: &str, used_mib: u64) -> bool {
        if self.leases(device).is_empty() && used_mib < FREE_MIB {
            self.pending_free.remove(device);
            true
        } else {
            if self.leases(device).is_empty() {
                self.pending_free.insert(device.to_string(), used_mib);
            } else {
                self.pending_free.remove(device);
            }
            false
        }
    }

    /// Every device with a lease, for the record.
    pub fn held_devices(&self) -> Vec<String> {
        let mut v: Vec<String> = self.leases.iter().filter(|(_, l)| !l.is_empty()).map(|(d, _)| d.clone()).collect();
        v.sort();
        v
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// Known failed first (the shipper's contract with the V6-07 sub-lane, 8 October 2026): every igneum-prove-host spawn
    /// carries the five names, exactly as written, with the lease's figures; a spawn without them is the old shape.
    #[test]
    fn the_host_reads_its_lease_from_five_named_variables() {
        let env = host_env(1, Workload::Shard, 11_800, 7_532, 600);
        let names: Vec<&str> = env.iter().map(|(k, _)| *k).collect();
        assert_eq!(names, ["IGNEUM_PROVE_DEVICE", "IGNEUM_PROVE_WORKLOAD", "IGNEUM_PROVE_MEM_FREE_MB", "IGNEUM_PROVE_MEM_BUDGET_MB", "IGNEUM_PROVE_DEADLINE_S"]);
        let values: Vec<&str> = env.iter().map(|(_, v)| v.as_str()).collect();
        assert_eq!(values, ["1", "shard", "11800", "7532", "600"]);
        assert_eq!(host_env(0, Workload::Aggregate, 1, 2, 3)[1].1, "aggregate");
        assert_eq!(host_env(0, Workload::Chain, 1, 2, 3)[1].1, "chain");
    }

    /// The lease's grant per mode: the measured proof peak with headroom beside the miner; the card less the free floor
    /// when the prover has the card to itself; nothing on a mining-only card.
    #[test]
    fn the_budget_is_the_grant_the_mode_allows() {
        assert_eq!(proof_budget_mib(24_576, Mode::Simultaneous), PROOF_PEAK_MIB + PROOF_PEAK_MIB * HEADROOM_PCT / 100);
        assert_eq!(proof_budget_mib(12_288, Mode::TimeShare), 12_288 - FREE_MIB);
        assert_eq!(proof_budget_mib(8_192, Mode::ProveOnly), 8_192 - FREE_MIB);
        assert_eq!(proof_budget_mib(8_192, Mode::MiningOnly), 0);
    }

    /// The V6-07 sub-lane's row (8 October 2026): a 12 GB card beside its miner has 6,159 MiB free, under the grant the
    /// mode allows; the lease is the lesser of the grant and the free reading, never more than the card has.
    #[test]
    fn the_lease_is_the_lesser_of_the_grant_and_the_free_memory() {
        assert_eq!(lease_mib(12_288, Mode::Simultaneous, 6_159), 6_159);
        assert_eq!(lease_mib(24_576, Mode::Simultaneous, 18_000), PROOF_PEAK_MIB + PROOF_PEAK_MIB * HEADROOM_PCT / 100);
        assert_eq!(lease_mib(8_192, Mode::MiningOnly, 8_000), 0);
        assert_eq!(lease_mib(0, Mode::ProveOnly, 0), 0, "no card, no grant");
    }

    /// Exit 78 from the host is the floor refusal: the card's words name the floor the host printed, else the budget.
    #[test]
    fn the_floor_refusal_names_the_memory_proving_needs() {
        assert_eq!(HOST_FLOOR_EXIT, 78);
        assert_eq!(floor_refusal_words("RESULT refused: profile needs 9216 MB free, 7532 offered", 7_532), "proving needs 9 GB free");
        // the host's own line (the V6-07 sub-lane, 8 October 2026): the GB figure is read as GB, not as MiB
        assert_eq!(floor_refusal_words("RESULT memory_profile refused: proving needs 8 GB free on the card for the shard workload (7700 MiB floor); 6100 MiB free", 7_532), "proving needs 8 GB free");
        assert_eq!(floor_refusal_words("nothing useful", 7_532), "proving needs 8 GB free");
        assert_eq!(floor_refusal_words("", 12_000), "proving needs 12 GB free");
    }

    #[test]
    fn the_modes_follow_the_measured_rows() {
        // nvidia-smi's figures: 32 GB 32,607; 24 GB 24,564; 16 GB 16,376; 12 GB 12,208; 8 GB 8,188; 6 GB 6,144
        assert_eq!(mode(32_607, true), Mode::Simultaneous);
        assert_eq!(mode(24_564, true), Mode::Simultaneous);
        assert_eq!(mode(16_376, true), Mode::TimeShare, "16 to 24 GB: the sum fits on paper, no row measured it together");
        assert_eq!(mode(12_208, true), Mode::TimeShare, "the 3060 row: the proof verified alone, died beside the miner");
        assert_eq!(mode(8_188, true), Mode::MiningOnly, "the 4060 row: 7,532 MiB alone of 8,188 leaves no driver headroom; a complete paid proof does not fit");
        assert_eq!(mode(6_144, true), Mode::MiningOnly);
        assert_eq!(mode(12_208, false), Mode::ProveOnly);
        assert_eq!(mode(6_144, false), Mode::MiningOnly);
        assert!(line("RTX 3060", 12_208, Mode::TimeShare).contains("one at a time (12 GB)"));
        assert!(line("RTX 5090", 32_607, Mode::Simultaneous).contains("tested configuration"));
    }

    #[test]
    fn the_cycle_mine_evict_prove_aggregate_submit_rebuild_resume_leaks_no_reservation() {
        let mut c = Coordinator::new();
        let d = "nvidia:0";
        let m = Mode::TimeShare;
        c.admit(d, Holder::Miner, MINER_RESIDENT_MIB, m, 0.0, None).unwrap();
        // the prover asks while the miner holds the card: refused by the mode
        assert_eq!(c.admit(d, Holder::Prover, PROOF_PEAK_MIB, m, 1.0, None), Err(Refusal::Held { by: Holder::Miner, mode: m }));
        // the engine pauses dispatch: that releases nothing
        assert_eq!(c.leases(d).len(), 1);
        // the miner's process exits: the lease is gone, the device is not free until a reading says so
        c.exited(d, Holder::Miner);
        assert_eq!(c.admit(d, Holder::Prover, PROOF_PEAK_MIB, m, 2.0, None), Err(Refusal::NotFree { used_mib: u64::MAX }));
        assert!(!c.confirm_free(d, 6_000), "the dataset is still resident");
        assert_eq!(c.admit(d, Holder::Prover, PROOF_PEAK_MIB, m, 3.0, None), Err(Refusal::NotFree { used_mib: 6_000 }));
        assert!(c.confirm_free(d, 2));
        c.admit(d, Holder::Prover, PROOF_PEAK_MIB, m, 4.0, None).unwrap();
        // the aggregation never shares a time-shared device with the prover
        assert!(matches!(c.admit(d, Holder::Aggregation, 13_400, m, 5.0, None), Err(Refusal::Held { by: Holder::Prover, .. })));
        c.exited(d, Holder::Prover);
        assert!(c.confirm_free(d, 1));
        c.admit(d, Holder::Aggregation, 13_400, m, 6.0, None).unwrap();
        c.exited(d, Holder::Aggregation);
        assert!(c.confirm_free(d, 1));
        // the miner rebuilds and resumes; nothing is left over
        c.admit(d, Holder::Miner, MINER_RESIDENT_MIB, m, 7.0, None).unwrap();
        assert_eq!(c.leases(d).len(), 1);
        assert_eq!(c.held_devices(), vec![d.to_string()]);
        c.exited(d, Holder::Miner);
        assert!(c.confirm_free(d, 1));
        assert!(c.held_devices().is_empty());
        assert!(c.admit(d, Holder::Miner, MINER_RESIDENT_MIB, m, 8.0, None).is_ok(), "no leaked reservation");
    }

    #[test]
    fn simultaneous_admits_the_prover_beside_the_miner_and_mining_only_refuses_a_proof() {
        let mut c = Coordinator::new();
        let d = "nvidia:0";
        c.admit(d, Holder::Miner, MINER_RESIDENT_MIB, Mode::Simultaneous, 0.0, None).unwrap();
        c.admit(d, Holder::Prepare, 200, Mode::Simultaneous, 0.5, None).unwrap();
        c.admit(d, Holder::Prover, 20_434, Mode::Simultaneous, 1.0, None).unwrap();
        assert_eq!(c.leases(d).len(), 3);
        // a benchmark never runs beside a prover
        assert!(matches!(c.admit(d, Holder::Benchmark, 6_000, Mode::Simultaneous, 2.0, None), Err(Refusal::Held { .. })));
        let mut o = Coordinator::new();
        o.admit(d, Holder::Miner, MINER_RESIDENT_MIB, Mode::MiningOnly, 0.0, None).unwrap();
        assert_eq!(o.admit(d, Holder::Prover, PROOF_PEAK_MIB, Mode::MiningOnly, 1.0, None), Err(Refusal::NoFit));
        // the same holder asking twice keeps its one lease
        o.admit(d, Holder::Miner, MINER_RESIDENT_MIB, Mode::MiningOnly, 2.0, None).unwrap();
        assert_eq!(o.leases(d).len(), 1);
    }

    #[test]
    fn admission_counts_the_whole_clock_against_the_payment_deadline() {
        let mut c = Coordinator::new();
        let d = "nvidia:0";
        let shard = Budget { transfer_s: 18.0, startup_s: 4.0, prove_s: 13.2, aggregate_s: 0.0, rebuild_s: 90.0, deadline_s: 600.0 };
        assert!((shard.needs_s() - 125.2).abs() < 1e-9);
        c.admit(d, Holder::Prover, PROOF_PEAK_MIB, Mode::TimeShare, 0.0, Some(&shard)).unwrap();
        c.exited(d, Holder::Prover);
        c.confirm_free(d, 1);
        let late = Budget { deadline_s: 100.0, ..shard.clone() };
        assert_eq!(c.admit(d, Holder::Prover, PROOF_PEAK_MIB, Mode::TimeShare, 1.0, Some(&late)), Err(Refusal::Deadline { needs_s: 125.2, has_s: 100.0 }));
        assert!(c.leases(d).is_empty(), "a refused job takes no lease");
    }
}
