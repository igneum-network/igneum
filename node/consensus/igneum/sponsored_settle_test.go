package igneum

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/params/types/ctypes"
	"github.com/holiman/uint256"
)

// TestSponsoredSetReadAtTheHeight (the review lead's replay condition ab8aaff9e):
// the sponsored set a block reads is the registration in force at its own
// height, the last landing BELOW it, never the head's; a block before a
// lane's landing reads no escrow for it, replayed or live alike.
func TestSponsoredSetReadAtTheHeight(t *testing.T) {
	e := &Engine{}
	base := &ctypes.IgneumVector{Coordinates: []ctypes.IgneumCoordinate{{Name: "raster", Algorithm: "null", Target: 36, DRef: 36000}}}
	landed := &ctypes.IgneumVector{Coordinates: []ctypes.IgneumCoordinate{
		{Name: "raster", Algorithm: "null", Target: 36, DRef: 36000},
		{Name: "chainhash", Algorithm: "null", Target: 36, DRef: 1000, Sponsored: true, Activation: 2000},
	}}
	e.registry.base = base
	e.registry.landings = []vectorLanding{{Height: 1000, Doc: landed}}
	if esc, _ := e.sponsoredEscrowsAt(7322, 999); len(esc) != 0 {
		t.Fatalf("a block before the landing reads no sponsored escrow (the head's menu would): %v", esc)
	}
	if esc, _ := e.sponsoredEscrowsAt(7322, 1000); len(esc) != 0 {
		t.Fatalf("the landing's own block reads the registration before it")
	}
	esc, v := e.sponsoredEscrowsAt(7322, 1001)
	if len(esc) != 1 || esc[SponsoredEscrow(7322, 1)] != 1 || v != landed {
		t.Fatalf("the block after the landing reads the landed set: %v", esc)
	}
}

// TestSponsoredRefundAndView (sections 4 and 5): the held figure, the
// paid-window record and the refund arithmetic (the unspent lock with every
// remainder plus the bond to the sponsor), read back through the view.
func TestSponsoredRefundAndView(t *testing.T) {
	st := sponsoredTestState(t)
	escrow := SponsoredEscrow(7322, 1)
	sponsor := common.HexToAddress("0x1111")
	amount, bond := big.NewInt(1_000_000), big.NewInt(1_000_000)
	value := new(big.Int).Add(new(big.Int).Mul(amount, big.NewInt(2)), bond) // two windows
	st.AddBalance(escrow, uint256.MustFromBig(value))
	facts := sponsoredFacts{Height: 100, Window: 64, Notice: 64, FundPerBlock: func(uint64) *big.Int { return big.NewInt(1) }}
	if why := admitSponsoredLock(st, escrow, sponsor, value, SponsoredLock{Coord: 1, FirstWindow: 10, LastWindow: 11, AmountPerWindow: amount, Bond: bond}, facts); why != "" {
		t.Fatal(why)
	}
	// One window paid in part (a key's part 600,000 of the 1,000,000; the remainder stays), the other unpaid.
	st.SetState(escrow, sponsoredSlot("paid", 1, 10), common.BigToHash(big.NewInt(1)))
	st.SetState(escrow, sponsoredSlot("paidTotal", 1, 10), common.BigToHash(big.NewInt(600_000)))
	st.SubBalance(escrow, uint256.NewInt(600_000))
	writeSponsoredHeld(st, escrow, new(big.Int).Sub(readSponsoredHeld(st, escrow), big.NewInt(600_000)))
	view := sponsoredLaneView(st, 7322, 1)
	if len(view.Locks) != 1 || view.Locks[0].Sponsor != sponsor || len(view.Windows) != 2 || !view.Windows[0].Paid || view.Windows[0].Total.Int64() != 600_000 || view.Windows[1].Paid {
		t.Fatalf("the view does not read the records: %+v %+v", view.Locks, view.Windows)
	}
	// The refund's height fixed at the admission under the notice then in
	// force: (11 + 1) x 64 + 64 = 832; a notice change inside the span
	// moves nothing (the security review's cell).
	rec := readSponsoredLock(st, escrow, 0)
	if rec.RefundAt != 832 {
		t.Fatalf("the refund height is fixed at the admission: %d", rec.RefundAt)
	}
	// The refund: locked 2,000,000 minus spent 600,000 plus the bond 1,000,000 = 2,400,000, the sponsor credited, the record closed.
	spent := new(big.Int)
	for w := rec.FirstWindow; w <= rec.LastWindow; w++ {
		spent.Add(spent, st.GetState(escrow, sponsoredSlot("paidTotal", 1, w)).Big())
	}
	refund := new(big.Int).Sub(new(big.Int).Mul(rec.AmountPerWindow, big.NewInt(2)), spent)
	refund.Add(refund, rec.Bond)
	if refund.Int64() != 2_400_000 {
		t.Fatalf("the refund is 2,400,000: %v", refund)
	}
	if st.GetBalance(escrow).ToBig().Cmp(refund) != 0 {
		t.Fatalf("the escrow holds exactly the refund before it: %v", st.GetBalance(escrow))
	}
	st.SubBalance(escrow, uint256.MustFromBig(refund))
	st.AddBalance(sponsor, uint256.MustFromBig(refund))
	rec.Refunded = true
	writeSponsoredLock(st, escrow, 0, rec)
	if readSponsoredLock(st, escrow, 0).live() {
		t.Fatalf("a refunded lock is not live")
	}
	if st.GetBalance(sponsor).ToBig().Int64() != 2_400_000 || st.GetBalance(escrow).Sign() != 0 {
		t.Fatalf("the sponsor holds the refund and the escrow nothing")
	}
	if !sponsoredNeedsRoot([]byte(`{"version":19,"sponsored_root":true}`)) || sponsoredNeedsRoot([]byte(`{"version":19}`)) {
		t.Fatalf("the root rule reads the document's field")
	}
}

// TestSponsoredStandingIsTheChainsOwnCredit (F-677, economics' a066a2d8f):
// a key is standing when the chain's own pay CREDITED it wei in the window
// (sponsoredPaidFrom's mark from the balance that rose), never from a list
// weight; the split's denominator is the standing keys' total, so a
// standing-less key with nine tenths of the lane's work earns nothing and
// leaves the standing keys' parts unchanged; a weight-above-zero key whose
// wei rounded to nothing is not standing.
func TestSponsoredStandingIsTheChainsOwnCredit(t *testing.T) {
	st := sponsoredTestState(t)
	escrow := SponsoredEscrow(7322, 2)
	a, b, chip := common.HexToAddress("0xaa"), common.HexToAddress("0xbb"), common.HexToAddress("0xcc")
	// The block's pay: a and b credited, chip in the list with a weight but zero wei.
	snap := &sponsoredPaySnapshot{lanes: []uint8{2}, window: 7, before: map[common.Address]*big.Int{a: big.NewInt(0), b: big.NewInt(0), chip: big.NewInt(0)}}
	st.AddBalance(a, uint256.NewInt(10))
	st.AddBalance(b, uint256.NewInt(30))
	e := &Engine{}
	e.sponsoredPaidFrom(snap, st, 7322)
	if st.GetState(escrow, sponsoredPaidKeySlot(2, 7, a)) == (common.Hash{}) || st.GetState(escrow, sponsoredPaidKeySlot(2, 7, b)) == (common.Hash{}) {
		t.Fatalf("the credited keys are marked paid")
	}
	if st.GetState(escrow, sponsoredPaidKeySlot(2, 7, chip)) != (common.Hash{}) {
		t.Fatalf("a key with a weight and no wei is not standing")
	}
	// The split: a 1 unit, b 3 units, the chip 36 units (nine tenths of the lane's work) without standing.
	work := map[common.Address]*big.Int{a: big.NewInt(1), b: big.NewInt(3), chip: big.NewInt(36)}
	amount := big.NewInt(1_000_000)
	st.AddBalance(escrow, uint256.MustFromBig(amount))
	standing := new(big.Int)
	var keys []common.Address
	for _, k := range []common.Address{a, b, chip} {
		if st.GetState(escrow, sponsoredPaidKeySlot(2, 7, k)) != (common.Hash{}) {
			standing.Add(standing, work[k])
			keys = append(keys, k)
		}
	}
	if standing.Int64() != 4 {
		t.Fatalf("the denominator is the standing keys' total (4): %v", standing)
	}
	total := splitSponsoredAmount(st, escrow, amount, keys, work, standing)
	if total.Int64() != 1_000_000 || st.GetBalance(a).ToBig().Int64() != 10+250_000 || st.GetBalance(b).ToBig().Int64() != 30+750_000 || st.GetBalance(chip).Sign() != 0 {
		t.Fatalf("the parts are 250,000 and 750,000 with the chip at nothing: total %v a %v b %v chip %v", total, st.GetBalance(a), st.GetBalance(b), st.GetBalance(chip))
	}
}

// TestSponsoredFailedWalkSettlesNothing (F-678): a window whose blocks the
// node cannot read (no block reader) settles nothing: no part paid, no
// window marked, the escrow and the held figure untouched; the settlement
// is not a zero.
func TestSponsoredFailedWalkSettlesNothing(t *testing.T) {
	st := sponsoredTestState(t)
	escrow := SponsoredEscrow(7322, 2)
	e := &Engine{}
	rec := sponsoredLockRecord{Sponsor: common.HexToAddress("0x1111"), FirstWindow: 10, LastWindow: 11, AmountPerWindow: big.NewInt(1_000_000), Bond: big.NewInt(1_000_000)}
	st.AddBalance(escrow, uint256.NewInt(3_000_000))
	writeSponsoredHeld(st, escrow, big.NewInt(3_000_000))
	v := &ctypes.IgneumVector{Coordinates: []ctypes.IgneumCoordinate{{Name: "a"}, {Name: "b"}, {Name: "sponsored", Sponsored: true, AlgorithmParams: []byte(`{"version":18}`)}}}
	total, ok := e.paySponsoredWindow(nil, nil, st, escrow, 2, 10, 64, rec, v)
	if ok || total != nil {
		t.Fatalf("a window the node cannot walk settles nothing: ok %v total %v", ok, total)
	}
	if st.GetBalance(escrow).ToBig().Int64() != 3_000_000 || readSponsoredHeld(st, escrow).Int64() != 3_000_000 || st.GetState(escrow, sponsoredSlot("paid", 2, 10)) != (common.Hash{}) {
		t.Fatalf("nothing moves and no window is marked on a failed read")
	}
}
