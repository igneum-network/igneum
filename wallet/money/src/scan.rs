//! Scanning (ADR-0001 section 4.5, "Scanning"): every output of every block tried with the full viewing key; the
//! notes that open are the wallet's; the block's nullifiers mark which of the wallet's notes are spent.
//!
//! Positions: the commitment tree is append-only (section 4.3 item 2), so an output's position is the count of
//! outputs before it across the whole chain. The scanner is told the position of the first output it sees
//! (`start_position`, 0 for a scan from the chain's start) and counts from there; a block stream must therefore
//! be contiguous. Nullifiers in a block are matched after its outputs are taken in.
//!
//! Spent tracking: for each note the wallet owns it computes nf = H(nk, cm, position) once and keeps it in a map;
//! a block's nullifier hits the map or it does not, one lookup each. A restore from an old backup therefore shows
//! the true balance after the rescan, never a stale one (section 4.5, "Restore from the words").

use std::collections::HashMap;

use crate::keys::FullViewingKey;
use crate::note::{open, Note, Output};

/// What a block gives the wallet: its outputs in order, its published nullifiers and, when the node says it
/// (igneum_poolBlock), the tree position of its first output, so a wallet that skipped the blocks before its
/// birthday keeps every position right.
#[derive(Clone, Debug, Default)]
pub struct Block {
    pub height: u64,
    pub position: Option<u64>,
    pub outputs: Vec<Output>,
    pub nullifiers: Vec<[u8; 32]>,
    /// The block's coinbase notes (stage two, cut two; `igneum_poolBlock`'s `mints`): the sealer's share minted
    /// to its registered recipient key with no ciphertext, every field public.
    pub mints: Vec<Mint>,
}

/// A coinbase note as the chain records it (docs/money/PAYOUT-PRIVACY-SPEC-2026-10-18.md section 4.2): the leaf
/// position, the value, the recipient key the sealer registered, the randomness the node derived from public data
/// (B over the chain id, the height, the parent hash and the sealer, reduced to a word) and the commitment. The
/// wallet whose pk it names rebuilds the note (d zero, no memo) and keeps it when cm = H(H(v, pk), r) matches.
#[derive(Clone, PartialEq, Eq, Debug)]
pub struct Mint {
    pub position: u64,
    pub value: u128,
    pub pk: [u8; 32],
    pub r: [u8; 32],
    pub cm: [u8; 32],
}

/// A note the wallet owns, with where it sits and whether it is spent.
#[derive(Clone, PartialEq, Eq, Debug)]
pub struct OwnedNote {
    pub note: Note,
    pub cm: [u8; 32],
    pub position: u64,
    pub height: u64,
    pub nullifier: [u8; 32],
    /// The height of the block whose nullifier spent it.
    pub spent_at: Option<u64>,
}

/// The two numbers (section 8 rule 1 names two for the vault; here: unspent and spent).
#[derive(Clone, Copy, PartialEq, Eq, Debug, Default)]
pub struct Balance {
    pub unspent: u128,
    pub spent: u128,
    pub notes: usize,
    pub spent_notes: usize,
}

pub struct Scanner {
    fvk: FullViewingKey,
    next_position: u64,
    notes: Vec<OwnedNote>,
    by_nullifier: HashMap<[u8; 32], usize>,
    outputs_tried: u64,
}

impl Scanner {
    pub fn new(fvk: FullViewingKey, start_position: u64) -> Scanner {
        Scanner { fvk, next_position: start_position, notes: Vec::new(), by_nullifier: HashMap::new(), outputs_tried: 0 }
    }

    pub fn fvk(&self) -> &FullViewingKey {
        &self.fvk
    }

    /// The position the next output will take.
    pub fn next_position(&self) -> u64 {
        self.next_position
    }

    pub fn outputs_tried(&self) -> u64 {
        self.outputs_tried
    }

    /// Takes one block in; returns how many of its outputs were this wallet's.
    pub fn scan_block(&mut self, block: &Block) -> usize {
        let mut found = 0;
        if let Some(p) = block.position {
            self.next_position = p;
        }
        for output in &block.outputs {
            let position = self.next_position;
            self.next_position += 1;
            self.outputs_tried += 1;
            if let Some(note) = open(&self.fvk, output) {
                let nullifier = self.fvk.nullifier(&output.cm, position);
                self.by_nullifier.insert(nullifier, self.notes.len());
                self.notes.push(OwnedNote { note, cm: output.cm, position, height: block.height, nullifier, spent_at: None });
                found += 1;
            }
        }
        for m in &block.mints {
            if m.position >= self.next_position {
                self.next_position = m.position + 1;
            }
            if m.pk != *self.fvk.pk() {
                continue;
            }
            let note = Note { value: m.value, pk: m.pk, r: m.r, d: [0u8; crate::sizes::DIVERSIFIER], memo: Vec::new() };
            if note.commitment() != m.cm {
                continue;
            }
            let nullifier = self.fvk.nullifier(&m.cm, m.position);
            self.by_nullifier.insert(nullifier, self.notes.len());
            self.notes.push(OwnedNote { note, cm: m.cm, position: m.position, height: block.height, nullifier, spent_at: None });
            found += 1;
        }
        for nf in &block.nullifiers {
            if let Some(&i) = self.by_nullifier.get(nf) {
                if self.notes[i].spent_at.is_none() {
                    self.notes[i].spent_at = Some(block.height);
                }
            }
        }
        found
    }

    pub fn notes(&self) -> &[OwnedNote] {
        &self.notes
    }

    pub fn unspent(&self) -> impl Iterator<Item = &OwnedNote> {
        self.notes.iter().filter(|n| n.spent_at.is_none())
    }

    pub fn balance(&self) -> Balance {
        let mut b = Balance { notes: self.notes.len(), ..Balance::default() };
        for n in &self.notes {
            match n.spent_at {
                None => b.unspent += n.note.value,
                Some(_) => {
                    b.spent += n.note.value;
                    b.spent_notes += 1;
                }
            }
        }
        b
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::hash::{CircuitHash, H};
    use crate::keys::Wallet;
    use crate::note::seal;

    /// A stream with the wallet's notes among other people's; some spent.
    pub(crate) fn stream(w: &Wallet, blocks: u64, per_block: usize, mine_every: usize) -> (Vec<Block>, u128, u128) {
        let stranger = Wallet::generate();
        let mut out = Vec::new();
        let mut position = 0u64;
        let mut mine: Vec<([u8; 32], u64, u128)> = Vec::new(); // cm, position, value
        let mut unspent = 0u128;
        let mut spent = 0u128;
        for h in 1..=blocks {
            let mut b = Block { height: h, ..Block::default() };
            for i in 0..per_block {
                let value = (h * 1000 + i as u64) as u128;
                let to = if (position as usize) % mine_every == 0 { w.address() } else { stranger.address() };
                let (_, o) = seal(&to, value, b"x").unwrap();
                if (position as usize) % mine_every == 0 {
                    mine.push((o.cm, position, value));
                    unspent += value as u128;
                }
                b.outputs.push(o);
                position += 1;
            }
            // every third block spends the wallet's oldest unspent note, and carries a stranger's nullifier too
            if h % 3 == 0 && !mine.is_empty() {
                let (cm, pos, value) = mine.remove(0);
                b.nullifiers.push(w.fvk().nullifier(&cm, pos));
                unspent -= value as u128;
                spent += value as u128;
            }
            b.nullifiers.push(stranger.fvk().nullifier(&[7u8; 32], h));
            out.push(b);
        }
        (out, unspent, spent)
    }

    #[test]
    fn finds_every_note_and_marks_the_spent_ones() {
        let w = Wallet::generate();
        let (blocks, unspent, spent) = stream(&w, 12, 5, 4);
        let mut s = Scanner::new(w.fvk().clone(), 0);
        let mut found = 0;
        for b in &blocks {
            found += s.scan_block(b);
        }
        assert_eq!(found, 15);
        let bal = s.balance();
        assert_eq!(bal.unspent, unspent);
        assert_eq!(bal.spent, spent);
        assert_eq!(bal.notes, 15);
        assert_eq!(bal.spent_notes, 4);
        assert_eq!(s.next_position(), 60);
        assert_eq!(s.outputs_tried(), 60);
        for n in s.notes() {
            assert_eq!(n.position % 4, 0);
            assert_eq!(n.nullifier, w.fvk().nullifier(&n.cm, n.position));
        }
    }

    /// A coinbase note in a block (after its outputs) is the wallet's when the mint names its pk and the
    /// commitment matches; another wallet's mint, or one with a wrong commitment, is not; its nullifier spends it.
    #[test]
    fn a_coinbase_note_is_found_from_the_public_record_alone() {
        let w = Wallet::generate();
        let stranger = Wallet::generate();
        let (mut blocks, unspent, _) = stream(&w, 3, 2, 2);
        let r = crate::hash::reduce_word(&[0x5au8; 32]);
        let mine = Mint { position: 6, value: 4_763_716_106_026_549_823, pk: *w.fvk().pk(), r, cm: H::commitment(4_763_716_106_026_549_823, w.fvk().pk(), &r) };
        let theirs = Mint { position: 7, value: 1, pk: *stranger.fvk().pk(), r, cm: H::commitment(1, stranger.fvk().pk(), &r) };
        let mut wrong = Mint { position: 8, value: 2, pk: *w.fvk().pk(), r, cm: [0u8; 32] };
        wrong.cm[0] = 1;
        blocks.push(Block { height: 4, position: Some(6), mints: vec![mine.clone(), theirs, wrong], ..Block::default() });
        let mut s = Scanner::new(w.fvk().clone(), 0);
        for b in &blocks {
            s.scan_block(b);
        }
        assert_eq!(s.next_position(), 9);
        assert_eq!(s.balance().unspent, unspent + mine.value);
        let n = s.notes().iter().find(|n| n.position == 6).expect("the coinbase note");
        assert_eq!(n.note.value, mine.value);
        assert_eq!(n.note.d, [0u8; 11]);
        assert!(n.note.memo.is_empty());
        let nf = n.nullifier;
        s.scan_block(&Block { height: 5, nullifiers: vec![nf], ..Block::default() });
        assert_eq!(s.balance().unspent, unspent);
    }

    #[test]
    fn a_viewing_key_alone_scans_the_same() {
        let w = Wallet::generate();
        let (blocks, unspent, spent) = stream(&w, 6, 4, 3);
        let fvk = FullViewingKey::from_bytes(&w.fvk().to_bytes()).unwrap();
        let mut s = Scanner::new(fvk, 0);
        for b in &blocks {
            s.scan_block(b);
        }
        assert_eq!(s.balance().unspent, unspent);
        assert_eq!(s.balance().spent, spent);
    }
}
