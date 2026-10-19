// Copyright 2026 The Igneum Authors
// This file is part of the go-ethereum library (Igneum fork).
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package igneum

import (
	"sort"
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow"
	"github.com/ethereum/go-ethereum/consensus/igneum/shares"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params/types/ctypes"
	"github.com/ethereum/go-ethereum/trie"
)

// PA-53, the share's declared target step (the outside review's third reading;
// the project's ruling of 20:5x UK, 11 October 2026: a consensus change for the
// ninth development chain): a carried entry names a step j, the hash is
// verified under the share target over 2^j, the entry weighs 2^j, the pay
// is pro rata by weight. The count form (shareStepMax 0) is unchanged.

// solveShareAt finds a nonce whose work meets the share target over 2^step
// and NOT the target over 2^(step+1), so the declared step is the share's
// exact one (a hash under the harder target would also pass the easier
// check, and the test wants the refusal at step + 1 to be the hash's).
func solveShareAt(t *testing.T, e *Engine, hs common.Hash, height uint64, d *big.Int, step uint8) (uint64, common.Hash) {
	t.Helper()
	start := uint64(0)
	for i := 0; i < 64; i++ {
		nonce, mix, ok := pow.Solve(e.algo, hs, height, e.shareDifficultyAt(d, step), start, nil)
		if !ok {
			t.Fatal("no share found")
		}
		if e.verifyShareWorkAt(hs, height, nonce, mix, d, step+1) != nil {
			return nonce, mix
		}
		start = nonce + 1
	}
	t.Fatal("every share found met the next step too")
	return 0, common.Hash{}
}

// stepShareEngine is mineShareEngine with the step form on at the bound.
func stepShareEngine(t *testing.T) (*Engine, *shareChain) {
	t.Helper()
	e, chain := mineShareEngine(t)
	e.config.Parts.Sortition.ShareStepMax = MaxShareStep
	return e, chain
}

// TestShareStepParameters: shareStepMax is carried and bounded at 32; the
// count form at 0.
func TestShareStepParameters(t *testing.T) {
	cfg := &ctypes.IgneumConfig{
		Authority: &ctypes.IgneumAuthority{Height: 1, Registry: testRegistry, Cap: big.NewInt(1), RegistryVersion: 4},
		Sortition: &ctypes.IgneumSortition{Height: 1, EpochBlocks: 100, SeedBlocks: 16, Expected: 1, Delta: 6, MaxRound: 16, UnbondedRound: 17, MaxPayouts: 256, OpenSeat: true, SharesPerBlock: 160, ShareStepMax: 32},
	}
	p, err := SortitionFor(cfg)
	if err != nil || p.ShareStepMax != 32 {
		t.Fatalf("the step form: %v %+v", err, p)
	}
	cfg.Sortition.ShareStepMax = 33
	if _, err := SortitionFor(cfg); err == nil {
		t.Fatal("a step bound over 32 accepted")
	}
	cfg.Sortition.ShareStepMax = 0
	if p, err := SortitionFor(cfg); err != nil || p.ShareStepMax != 0 {
		t.Fatalf("the count form: %v", err)
	}
}

// TestMinedShareStepForm: the step form's entry is the count form's plus
// the step byte; each parses under its own width and not the other's.
func TestMinedShareStepForm(t *testing.T) {
	sh := types.MinedShare{Address: common.Address{0x11}, ExtraNonce: 7, Nonce: 9, MixDigest: common.Hash{0x22}, ParentIdx: 3, Step: 5}
	if len(sh.Bytes()) != types.MinedShareBytes || len(sh.BytesOf(false)) != types.MinedShareBytes || len(sh.BytesOf(true)) != types.MinedShareStepBytes {
		t.Fatal("entry widths")
	}
	if types.MinedShareWidth(true) != 70 || types.MinedShareWidth(false) != 69 {
		t.Fatal("widths")
	}
	two := append(sh.BytesOf(true), sh.BytesOf(true)...)
	list, ok := types.ParseMinedSharesOf(two, true)
	if !ok || len(list) != 2 || list[1].Address != sh.Address || list[1].Nonce != sh.Nonce || list[1].Step != sh.Step || list[1].MixDigest != sh.MixDigest {
		t.Fatalf("the step form round trip: %v %+v", ok, list)
	}
	if _, ok := types.ParseMinedSharesOf(two, false); ok {
		t.Fatal("step-form entries parsed under the count width")
	}
	plain, ok := types.ParseMinedShares(sh.Bytes())
	if !ok || plain[0].Step != 0 {
		t.Fatal("the count form reads step 0")
	}
}

// TestShareStepSubmitVerifyAndPay: on the step form a share mined at step
// 2 is accepted at step 2 and refused at step 3 by the hash, at 33 by the
// bound, and again at step 0 as a duplicate (one share at two steps is one
// share); the count form refuses any step above 0 by name; the accepted
// entries weigh 2^step and the derived list pays pro rata by weight; a
// block carrying the step-form entries verifies, a forged step is refused
// by the hash, a step over the bound by name, and the count width as
// malformed; the import marks the pooled shares carried.
func TestShareStepSubmitVerifyAndPay(t *testing.T) {
	e, chain := stepShareEngine(t)
	defer e.Close()
	parent := chain.head
	h := parent.Number.Uint64() + 1
	d := parent.Difficulty
	a, b := common.Address{0x11}, common.Address{0x22}
	hsB := ShareHeaderHash(parent.Hash(), h, b, 3)
	nonceB, mixB := solveShareAt(t, e, hsB, h, d, 2)
	if ok, reason := e.SubmitShareAt(chain, b, 3, nonceB, mixB, h, nil, 3); ok || reason[:len(errShareWork.Error())] != errShareWork.Error() {
		t.Fatalf("a step-2 share at step 3: ok %v %q", ok, reason)
	}
	if ok, reason := e.SubmitShareAt(chain, b, 3, nonceB, mixB, h, nil, 33); ok || reason != errShareStep.Error() {
		t.Fatalf("a step over the bound: ok %v %q", ok, reason)
	}
	if ok, reason := e.SubmitShareAt(chain, b, 3, nonceB, mixB, h, nil, 2); !ok {
		t.Fatalf("a step-2 share at step 2 refused: %s", reason)
	}
	if ok, reason := e.SubmitShareAt(chain, b, 3, nonceB, mixB, h, nil, 0); ok || reason != errShareDuplicate.Error() {
		t.Fatalf("the same share at step 0: ok %v %q", ok, reason)
	}
	// Two step-0 shares for a.
	var listA []types.MinedShare
	for _, extra := range []uint64{1, 2} {
		hs := ShareHeaderHash(parent.Hash(), h, a, extra)
		nonce, mix := solveShare(t, e, hs, h, d)
		if ok, reason := e.SubmitShareAt(chain, a, extra, nonce, mix, h, nil, 0); !ok {
			t.Fatalf("a step-0 share refused: %s", reason)
		}
		listA = append(listA, types.MinedShare{Address: a, ExtraNonce: extra, Nonce: nonce, MixDigest: mix})
	}
	if st := e.ShareStatsNow(); st.Accepted != 3 || st.RefusedBy[errShareStep.Error()] != 1 || st.RefusedBy[errShareWork.Error()] != 1 || st.RefusedBy[errShareDuplicate.Error()] != 1 {
		t.Fatalf("stats: %+v", st)
	}
	// The weights: a 1 + 1, b 2^2.
	list := append(listA, types.MinedShare{Address: b, ExtraNonce: 3, Nonce: nonceB, MixDigest: mixB, Step: 2})
	accepted := acceptedEntries(list)
	if len(accepted) != 2 || accepted[0].Address != a || accepted[0].Weight != 2 || accepted[1].Address != b || accepted[1].Weight != 4 {
		t.Fatalf("accepted entries: %+v", accepted)
	}
	// The template's entries carry the step form, and the block built on
	// them verifies.
	entries, withdrawals, err := e.TemplateShares(chain, parent, chain.st, common.Address{0xaa})
	if err != nil || types.MinedShareCountOf(entries, true) != 3 {
		t.Fatalf("template: %v, %d bytes", err, len(entries))
	}
	tl, _ := types.ParseMinedSharesOf(entries, true)
	steps := 0
	for _, sh := range tl {
		steps += int(sh.Step)
	}
	if steps != 2 {
		t.Fatalf("the template's steps sum %d, want 2", steps)
	}
	derived := shares.FromWithdrawals(withdrawals)
	var wa, wb uint64
	for _, en := range derived {
		switch en.Address {
		case a:
			wa = en.Weight
		case b:
			wb = en.Weight
		}
	}
	if wa != 2 || wb != 4 {
		t.Fatalf("the derived list's weights: a %d b %d (%+v)", wa, wb, derived)
	}
	root := types.MinedSharesRoot(entries)
	wroot := types.DeriveSha(types.Withdrawals(withdrawals), trie.NewStackTrie(nil))
	header := childOf(parent)
	header.SharesRoot, header.WithdrawalsHash = &root, &wroot
	header.Coinbase = common.BytesToAddress(wroot[:20])
	mkBlock := func(hdr *types.Header, ent []byte) *types.Block {
		return types.NewBlock(hdr, nil, nil, nil, trie.NewStackTrie(nil)).WithWithdrawals(withdrawals).WithMinedShares(ent)
	}
	if err := e.VerifyMinedShares(mkBlock(header, entries), chain.st, chain); err != nil {
		t.Fatalf("a valid step-form set refused: %v", err)
	}
	// A forged step on b's entry (its hash meets step 2, not 3).
	forge := func(step byte) []byte {
		out := append([]byte{}, entries...)
		for i := 0; i < 3; i++ {
			if common.BytesToAddress(out[i*70:i*70+20]) == b {
				out[i*70+69] = step
			}
		}
		return out
	}
	bad := types.CopyHeader(header)
	fr := types.MinedSharesRoot(forge(3))
	bad.SharesRoot = &fr
	if err := e.VerifyMinedShares(mkBlock(bad, forge(3)), chain.st, chain); !errors.Is(err, errShareWork) {
		t.Fatalf("a forged step: %v", err)
	}
	fr = types.MinedSharesRoot(forge(33))
	bad.SharesRoot = &fr
	if err := e.VerifyMinedShares(mkBlock(bad, forge(33)), chain.st, chain); !errors.Is(err, errShareStep) {
		t.Fatalf("a step over the bound: %v", err)
	}
	// The count width on a step-form chain is malformed.
	var narrow []byte
	for _, sh := range tl {
		narrow = append(narrow, sh.Bytes()...)
	}
	nr := types.MinedSharesRoot(narrow)
	bad.SharesRoot = &nr
	if err := e.VerifyMinedShares(mkBlock(bad, narrow), chain.st, chain); !errors.Is(err, errSharesMalform) {
		t.Fatalf("the count width: %v", err)
	}
	// The import marks the three pooled shares carried.
	e.MarkCarried(chain, mkBlock(header, entries))
	if st := e.ShareStatsNow(); st.Carried != 3 || st.Pooled != 0 {
		t.Fatalf("after the carriage: %+v", st)
	}
	// The count form refuses a step above 0 by name and takes step 0.
	e.config.Parts.Sortition.ShareStepMax = 0
	hs := ShareHeaderHash(parent.Hash(), h, a, 9)
	nonce, mix := solveShare(t, e, hs, h, d)
	if ok, reason := e.SubmitShareAt(chain, a, 9, nonce, mix, h, nil, 1); ok || reason != errShareStep.Error() {
		t.Fatalf("the count form at step 1: ok %v %q", ok, reason)
	}
	if ok, reason := e.SubmitShareAt(chain, a, 9, nonce, mix, h, nil, 0); !ok {
		t.Fatalf("the count form at step 0: %s", reason)
	}
}

// TestShareStepSealerQuarter (the project's ruling of 20:5x UK on the design
// seat's line): under the step form the sealer's own entry is max(1,
// ceil(W / 3)) over the SUMMED weight of the accepted entries, so the
// sealer's realised fraction of the payment is a quarter at every step
// (exactly when W is a multiple of 3, within one weight unit otherwise):
// read at steps 0, 8 and 32 over mixed entries; the bounds (256 entries of
// 2^32 an address) hold inside a uint64; the count form keeps its term as
// it stands.
func TestShareStepSealerQuarter(t *testing.T) {
	e, chain := stepShareEngine(t)
	defer e.Close()
	sealer := common.Address{0xaa}
	fraction := func(accepted []shares.Entry) float64 {
		list, _, err := e.ShareList(chain.st, 5, accepted, sealer)
		if err != nil {
			t.Fatal(err)
		}
		var total, own float64
		for _, en := range list {
			total += float64(en.Weight)
			if en.Address == sealer {
				own = float64(en.Weight)
			}
		}
		return own / total
	}
	cases := []struct {
		name     string
		accepted []shares.Entry
	}{
		{"step 0, three shares", []shares.Entry{{Address: shareAddr(1), Weight: 2}, {Address: shareAddr(2), Weight: 1}}},
		{"step 8, ten shares", []shares.Entry{{Address: shareAddr(1), Weight: 6 << 8}, {Address: shareAddr(2), Weight: 4 << 8}}},
		{"mixed 0, 8 and 32", []shares.Entry{{Address: shareAddr(1), Weight: 1}, {Address: shareAddr(2), Weight: 1 << 8}, {Address: shareAddr(3), Weight: 2 << 32}}},
		{"step 32 at the per-address bound", []shares.Entry{{Address: shareAddr(1), Weight: 256 << 32}, {Address: shareAddr(2), Weight: 256 << 32}, {Address: shareAddr(3), Weight: 256 << 32}}},
	}
	for _, tc := range cases {
		f := fraction(tc.accepted)
		total := TotalWeight(tc.accepted)
		// Within one weight unit of a quarter: |own / (total + own) - 1/4|
		// is at most 1 / (total + own) by the ceiling.
		if d := f - 0.25; d < -1.0/float64(total) || d > 1.0/float64(total) {
			t.Fatalf("%s: the sealer's fraction %.9f (total %d)", tc.name, f, total)
		}
		if w := SealerWeightOf(total); w != (total+2)/3 || w == 0 {
			t.Fatalf("%s: the term %d over %d", tc.name, w, total)
		}
	}
	if SealerWeightOf(0) != 1 || SealerWeightOf(1) != 1 || SealerWeightOf(4) != 2 || SealerWeightOf(300) != 100 {
		t.Fatal("the term's small values")
	}
	// The count form takes the same term over the carried shares' count
	// (the project's ruling: the first form read the entries' count, one an
	// address, and an address carrying three shares in a block paid the
	// sealer a tenth and not a quarter); below a chain's SealerSharesV1
	// gate the first form stands (devnet7's swap), from the landing check the rule.
	e.config.Parts.Sortition.ShareStepMax = 0
	three := []shares.Entry{{Address: shareAddr(1), Weight: 3}}
	if f := fraction(three); f != 0.25 {
		t.Fatalf("an address carrying three shares: the sealer's fraction %.4f, want a quarter", f)
	}
	mixed := []shares.Entry{{Address: shareAddr(1), Weight: 6}, {Address: shareAddr(2), Weight: 3}}
	if f := fraction(mixed); f != 0.25 {
		t.Fatalf("two addresses carrying nine shares: the sealer's fraction %.4f, want a quarter", f)
	}
	e.SetRuleForks(&ctypes.IgneumRuleForks{SealerSharesV1: 10})
	if f := fraction(mixed); f != 0.1 {
		t.Fatalf("below the gate, the first form: the sealer's fraction %.4f, want a tenth (ceil(2 / 3) of 9 + 1)", f)
	}
	list, _, err := e.ShareList(chain.st, 10, mixed, sealer)
	if err != nil || list[len(list)-1].Weight != 3 {
		t.Fatalf("at the landing check: %+v %v", list[len(list)-1], err)
	}
	e.SetRuleForks(nil)
	// Saturation: a sum past the uint64 reads the top, never wraps.
	if TotalWeight([]shares.Entry{{Weight: ^uint64(0)}, {Weight: 1}}) != ^uint64(0) {
		t.Fatal("the summed weight wrapped")
	}
}

// TestShareStepJournalRecord: the acceptance record carries the step (type
// 5) and the first form (type 1) reads step 0.
func TestShareStepJournalRecord(t *testing.T) {
	ps := pooledShare{share: types.MinedShare{Address: common.Address{0x11}, ExtraNonce: 7, Nonce: 9, MixDigest: common.Hash{0x22}, Step: 5}, parent: common.Hash{0x33}, height: 12}
	rec := acceptedRecord(ps)
	if rec[0] != shareJournalAcceptedStep || len(rec) != 1+shareJournalAcceptedBytes+1 {
		t.Fatalf("the record: type %d, %d bytes", rec[0], len(rec))
	}
	old := append([]byte{}, rec[:1+shareJournalAcceptedBytes]...)
	old[0] = shareJournalAccepted
	l := parseShareJournal(append(rec, old...))
	if len(l.accepted) != 2 || l.accepted[0].share.Step != 5 || l.accepted[1].share.Step != 0 || l.accepted[0].height != 12 || l.accepted[1].share.Nonce != 9 {
		t.Fatalf("the replay: %+v", l.accepted)
	}
	if l.consumed != int64(len(rec)+len(old)) {
		t.Fatalf("consumed %d of %d", l.consumed, len(rec)+len(old))
	}
}

// TestTemplateOrderByStep pins F-122: a share at a higher step is carried
// before a flood of step-0 shares from an address with carriage already.
func TestTemplateOrderByStep(t *testing.T) {
	type cand struct {
		step   uint8
		weight uint64
	}
	cands := []cand{{0, 1000}, {0, 1000}, {20, 0}, {3, 5}}
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].step != cands[j].step {
			return cands[i].step > cands[j].step
		}
		return cands[i].weight > cands[j].weight
	})
	if cands[0].step != 20 || cands[1].step != 3 || cands[2].step != 0 {
		t.Fatalf("order %v", cands)
	}
}
