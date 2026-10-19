package igneum

import (
	"encoding/binary"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/holiman/uint256"
)

func sponsoredTestState(t *testing.T) *state.StateDB {
	st, err := state.New(types.EmptyRootHash, state.NewDatabase(rawdb.NewMemoryDatabase()), nil)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func lockData(c uint8, first, last uint64, amount, bond *big.Int) []byte {
	d := make([]byte, sponsoredLockLen)
	d[0], d[1] = sponsoredLockTag, c
	binary.BigEndian.PutUint64(d[2:10], first)
	binary.BigEndian.PutUint64(d[10:18], last)
	amount.FillBytes(d[18:50])
	bond.FillBytes(d[50:82])
	return d
}

func rootData(c uint8, window uint64, root common.Hash) []byte {
	d := make([]byte, sponsoredRootLen)
	d[0], d[1] = sponsoredRootTag, c
	binary.BigEndian.PutUint64(d[2:10], window)
	copy(d[10:42], root[:])
	return d
}

// TestSponsoredLockAndRootRecords (the form's sections 2 and 3): the
// records parse and refuse by length and tag; a lock is admitted when its
// value is whole and exact, its span past the landing plus the notice,
// inside the lane's active span, its amount at the floor, no other lock
// overlapping; the storage round-trips; a root is admitted once a window
// from the lock's sponsor the notice ahead.
func TestSponsoredLockAndRootRecords(t *testing.T) {
	st := sponsoredTestState(t)
	escrow := SponsoredEscrow(7322, 8)
	sponsor := common.HexToAddress("0x1111")
	amount, bond := big.NewInt(1_000_000), big.NewInt(5_000_000)
	l, ok := ParseSponsoredLock(lockData(8, 10, 12, amount, bond))
	if !ok || l.Coord != 8 || l.FirstWindow != 10 || l.LastWindow != 12 || l.AmountPerWindow.Cmp(amount) != 0 || l.Bond.Cmp(bond) != 0 {
		t.Fatalf("the lock record does not parse: %+v %v", l, ok)
	}
	if _, ok := ParseSponsoredLock(lockData(8, 10, 12, amount, bond)[:81]); ok {
		t.Fatalf("a short record must not parse")
	}
	if _, ok := ParseSponsoredRoot(lockData(8, 10, 12, amount, bond)); ok {
		t.Fatalf("a lock is no root")
	}
	facts := sponsoredFacts{Height: 500, Window: 64, Notice: 64,
		Active:       func(c uint8, h uint64) bool { return h >= 320 },
		RetiredFrom:  func(c uint8) uint64 { return 0 },
		FundPerBlock: func(h uint64) *big.Int { return big.NewInt(10_000) }, // the floor: the fund's share of the window, 64 x 10,000 = 640,000
	}
	value := new(big.Int).Mul(amount, big.NewInt(3))
	value.Add(value, bond)
	// The value must have reached the escrow.
	if why := admitSponsoredLock(st, escrow, sponsor, value, l, facts); why != "the value did not reach the escrow" {
		t.Fatalf("a lock whose value is not in the escrow must be refused: %q", why)
	}
	st.AddBalance(escrow, uint256.MustFromBig(value))
	if why := admitSponsoredLock(st, escrow, sponsor, new(big.Int).Sub(value, big.NewInt(1)), l, facts); why == "" {
		t.Fatalf("a value short by one wei must be refused")
	}
	if why := admitSponsoredLock(st, escrow, sponsor, value, SponsoredLock{Coord: 8, FirstWindow: 8, LastWindow: 10, AmountPerWindow: amount, Bond: bond}, facts); why != "the span opens before the landing plus the notice" {
		t.Fatalf("a span under the notice must be refused: %q", why)
	}
	low := SponsoredLock{Coord: 8, FirstWindow: 10, LastWindow: 12, AmountPerWindow: big.NewInt(600_000), Bond: bond}
	lowValue := new(big.Int).Add(new(big.Int).Mul(low.AmountPerWindow, big.NewInt(3)), bond)
	st.AddBalance(escrow, uint256.MustFromBig(lowValue))
	if why := admitSponsoredLock(st, escrow, sponsor, lowValue, low, facts); why == "" {
		t.Fatalf("an amount under the floor must be refused")
	}
	if why := admitSponsoredLock(st, escrow, sponsor, value, l, facts); why != "" {
		t.Fatalf("the lock must be admitted: %q", why)
	}
	r := readSponsoredLock(st, escrow, 0)
	if r.Sponsor != sponsor || r.FirstWindow != 10 || r.LastWindow != 12 || r.AmountPerWindow.Cmp(amount) != 0 || r.Bond.Cmp(bond) != 0 || r.WindowsPaid != 0 || r.Refunded || !r.live() {
		t.Fatalf("the stored record does not round-trip: %+v", r)
	}
	if readSponsoredHeld(st, escrow).Cmp(value) != 0 {
		t.Fatalf("the held balance is the lock's value")
	}
	// A second lock overlapping the span is refused; one after it admitted.
	other := common.HexToAddress("0x2222")
	st.AddBalance(escrow, uint256.MustFromBig(value))
	if why := admitSponsoredLock(st, escrow, other, value, SponsoredLock{Coord: 8, FirstWindow: 12, LastWindow: 14, AmountPerWindow: amount, Bond: bond}, facts); why != "another lock holds an overlapping span on the lane" {
		t.Fatalf("an overlapping span must be refused: %q", why)
	}
	if why := admitSponsoredLock(st, escrow, other, value, SponsoredLock{Coord: 8, FirstWindow: 13, LastWindow: 15, AmountPerWindow: amount, Bond: bond}, facts); why != "" {
		t.Fatalf("a later span must be admitted: %q", why)
	}
	// The root: from the sponsor, inside its span, the notice ahead, once.
	root := common.HexToHash("0xabcd")
	if why := admitSponsoredRoot(st, escrow, other, nil, SponsoredRoot{Coord: 8, Window: 11, Root: root}, facts); why != "the sender holds no lock covering the window" {
		t.Fatalf("a root from the wrong sponsor must be refused: %q", why)
	}
	if why := admitSponsoredRoot(st, escrow, sponsor, big.NewInt(1), SponsoredRoot{Coord: 8, Window: 11, Root: root}, facts); why != "a root carries no value" {
		t.Fatalf("a root with value must be refused: %q", why)
	}
	if why := admitSponsoredRoot(st, escrow, sponsor, nil, SponsoredRoot{Coord: 8, Window: 11, Root: root}, facts); why != "" {
		t.Fatalf("the root must be admitted: %q", why)
	}
	if SponsoredRootAt(st, escrow, 11) != root {
		t.Fatalf("the root is stored by window")
	}
	if why := admitSponsoredRoot(st, escrow, sponsor, nil, SponsoredRoot{Coord: 8, Window: 11, Root: common.HexToHash("0x1234")}, facts); why == "" {
		t.Fatalf("a second root for the window must be refused (the first stands)")
	}
	if SponsoredRootAt(st, escrow, 11) != root {
		t.Fatalf("the first root stands")
	}
	late := sponsoredFacts{Height: 11*64 - 10, Window: 64, Notice: 64}
	if why := admitSponsoredRoot(st, escrow, sponsor, nil, SponsoredRoot{Coord: 8, Window: 11, Root: root}, late); why != "the window opens under the notice" {
		t.Fatalf("a late root must be refused: %q", why)
	}
	// A record parsed from the wire into the same fields.
	rr, ok := ParseSponsoredRoot(rootData(8, 11, root))
	if !ok || rr.Window != 11 || rr.Root != root || rr.Coord != 8 {
		t.Fatalf("the root record does not parse")
	}
}

// TestSponsoredFloorAndWindowBound (F-672, F-673): the floor is the fund's
// share of the window itself (the fund's share a block times the window,
// no further percent): on the public test network from the 12-second
// gate 3.048778 IGN a window is admitted and one wei under it refused,
// 5.083779 before the landing check; a window field above the bound is refused
// before any arithmetic; the bond at or above one window's amount.
func TestSponsoredFloorAndWindowBound(t *testing.T) {
	st := sponsoredTestState(t)
	escrow := SponsoredEscrow(7322, 8)
	sponsor := common.HexToAddress("0x1111")
	ign := new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)
	wei := func(s string) *big.Int {
		v, ok := new(big.Int).SetString(s, 10)
		if !ok {
			t.Fatal(s)
		}
		return v
	}
	// The fund's share a block on the public test network: 3.048778307856991872 IGN / 64 from the landing check, 5.083779 / 64 before it.
	gateFund := new(big.Int).Div(wei("3048778307856991872"), big.NewInt(64))
	preFund := new(big.Int).Div(wei("5083779000000000000"), big.NewInt(64))
	facts := sponsoredFacts{Height: 100, Window: 64, Notice: 64,
		FundPerBlock: func(h uint64) *big.Int {
			if h >= 12288 {
				return gateFund
			}
			return preFund
		},
	}
	try := func(first, last uint64, amount *big.Int) string {
		bond := new(big.Int).Set(amount)
		value := new(big.Int).Add(new(big.Int).Mul(amount, new(big.Int).SetUint64(last-first+1)), bond)
		st.AddBalance(escrow, uint256.MustFromBig(value))
		why := admitSponsoredLock(st, escrow, sponsor, value, SponsoredLock{Coord: 8, FirstWindow: first, LastWindow: last, AmountPerWindow: amount, Bond: bond}, facts)
		if why != "" {
			st.SubBalance(escrow, uint256.MustFromBig(value))
		}
		return why
	}
	gateFloor := new(big.Int).Mul(gateFund, big.NewInt(64))
	if why := try(200, 200, gateFloor); why != "" {
		t.Fatalf("the floor's own amount (%s wei, about 3.048778 IGN) is admitted from the landing check: %q", gateFloor, why)
	}
	if why := try(201, 201, new(big.Int).Sub(gateFloor, big.NewInt(1))); why == "" || why[:30] != "the amount a window is under t" {
		t.Fatalf("one wei under the floor is refused: %q", why)
	}
	preFloor := new(big.Int).Mul(preFund, big.NewInt(64))
	if why := try(100, 100, preFloor); why != "" { // window 100 x 64 = 6,400, before the landing check
		t.Fatalf("the floor before the landing check (%s wei, about 5.083779 IGN) is admitted: %q", preFloor, why)
	}
	if why := try(101, 101, new(big.Int).Sub(preFloor, big.NewInt(1))); why == "" {
		t.Fatalf("one wei under the pre-gate floor is refused")
	}
	_ = ign
	// The window bound: a window past the horizon is refused before any arithmetic.
	huge := sponsoredWindowMax + 1
	if why := try(huge, huge, gateFloor); why != "the window is past the chain's horizon" {
		t.Fatalf("a window past the horizon must be refused: %q", why)
	}
	if why := try(sponsoredWindowMax, sponsoredWindowMax, gateFloor); why == "the window is past the chain's horizon" {
		t.Fatalf("the bound itself is inside the horizon")
	}
	if why := admitSponsoredRoot(st, escrow, sponsor, nil, SponsoredRoot{Coord: 8, Window: huge, Root: common.HexToHash("0x1")}, facts); why != "the window is past the chain's horizon" {
		t.Fatalf("a root's window past the horizon must be refused: %q", why)
	}
	// The bond at or above one window's amount.
	short := SponsoredLock{Coord: 8, FirstWindow: 300, LastWindow: 300, AmountPerWindow: gateFloor, Bond: new(big.Int).Sub(gateFloor, big.NewInt(1))}
	v := new(big.Int).Add(short.AmountPerWindow, short.Bond)
	st.AddBalance(escrow, uint256.MustFromBig(v))
	if why := admitSponsoredLock(st, escrow, sponsor, v, short, facts); why != "the bond is under one window's amount" {
		t.Fatalf("a bond under one window's amount must be refused: %q", why)
	}
}
