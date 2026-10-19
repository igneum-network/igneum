// The Menu handle (cut B, 15 October 2026): an immutable menu behind an
// atomic pointer, replaced whole when a registration lands on the chain, so
// every reader sees one registration at a time and a running node takes a
// landed re-registration without a restart.

package vector

import (
	"math/big"
	"sync/atomic"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/igneum/pow"
	"github.com/ethereum/go-ethereum/params/types/ctypes"
)

// Menu is the registration's handle.
type Menu struct {
	p     atomic.Pointer[menu]
	build func(name string, params []byte) (pow.Algorithm, error)
	// gen counts the registrations this handle has held (F-197): a reader
	// caching a derivation of the registration keys it by the generation,
	// so a landing never serves a state computed under the menu before it.
	gen atomic.Uint64
}

// Generation is the count of registrations this handle has held beyond the
// one it was built with: 0 until the first Replace, one more for each.
func (m *Menu) Generation() uint64 { return m.gen.Load() }

// Replace builds the registration v, reusing the built algorithms of every
// coordinate whose document is unchanged, carries the seal coordinate and
// the dataset settings over, and makes it the menu.
func (m *Menu) Replace(v *ctypes.IgneumVector) error {
	old := m.p.Load()
	inner, err := newMenuReusing(v, m.build, old)
	if err != nil {
		return err
	}
	if old != nil {
		inner.full = old.full
		if old.seal != nil {
			sc := *old.seal
			inner.seal = &sc
		}
		// F-215: the single form reaches the adapters built for this
		// registration as the full choice does (the ones reused keep it;
		// the call is idempotent), BEFORE the full choice builds anything.
		if old.single {
			inner.SetSingleDataset(true)
		}
		inner.applyFull()
	}
	m.p.Store(inner)
	m.gen.Add(1)
	return nil
}

// Registration is the registration the menu was built from.
func (m *Menu) Registration() *ctypes.IgneumVector { return m.p.Load().v }

func (m *Menu) PayShares() []uint64                    { return m.p.Load().PayShares() }
func (m *Menu) PaySharesAt(height uint64) []uint64     { return m.p.Load().PaySharesAt(height) }
func (m *Menu) BalancedFrom() uint64                   { return m.p.Load().BalancedFrom() }
func (m *Menu) BalancedV2From() uint64                 { return m.p.Load().BalancedV2From() }
func (m *Menu) PhiDen() uint64                         { return m.p.Load().PhiDen() }
func (m *Menu) BalancedV3At(height uint64) bool        { return m.p.Load().BalancedV3At(height) }
func (m *Menu) BalancedMinAt(height uint64) bool       { return m.p.Load().BalancedMinAt(height) }
func (m *Menu) SealerFoldAt(height uint64) bool        { return m.p.Load().SealerFoldAt(height) }
func (m *Menu) SealerFoldListAt(height uint64) bool    { return m.p.Load().SealerFoldListAt(height) }
func (m *Menu) SealableAt(c uint8, height uint64) bool { return m.p.Load().SealableAt(c, height) }
func (m *Menu) SealingBreadthFrom() uint64             { return m.p.Load().SealingBreadthFrom() }
func (m *Menu) PhiDenAt(height uint64) uint64          { return m.p.Load().PhiDenAt(height) }
func (m *Menu) BalancedSealerFrom() uint64             { return m.p.Load().BalancedSealerFrom() }
func (m *Menu) GraphSharesFrom() uint64                { return m.p.Load().GraphSharesFrom() }
func (m *Menu) GraphSharesPerBlock() uint64            { return m.p.Load().GraphSharesPerBlock() }
func (m *Menu) BalancedLanesAt(height uint64) []uint8  { return m.p.Load().BalancedLanesAt(height) }
func (m *Menu) PayChanges() []ctypes.IgneumPayChange   { return m.p.Load().PayChanges() }
func (m *Menu) PayByCoordinate() bool                  { return m.p.Load().PayByCoordinate() }
func (m *Menu) ActiveAt(c uint8, height uint64) bool   { return m.p.Load().ActiveAt(c, height) }
func (m *Menu) RevealOf(a pow.Algorithm) uint64        { return m.p.Load().RevealOf(a) }
func (m *Menu) NoticeBlocks() uint64                   { return m.p.Load().NoticeBlocks() }
func (m *Menu) MaxLiveBlocks() uint64                  { return m.p.Load().MaxLiveBlocks() }
func (m *Menu) Coverage() bool                         { return m.p.Load().Coverage() }
func (m *Menu) Window() uint64                         { return m.p.Load().Window() }
func (m *Menu) WindowBranch() bool                     { return m.p.Load().WindowBranch() }
func (m *Menu) SeedGenesis() bool                      { return m.p.Load().SeedGenesis() }
func (m *Menu) Count() int                             { return m.p.Load().Count() }
func (m *Menu) Minimum() bool                          { return m.p.Load().Minimum() }
func (m *Menu) HalfLife() uint64                       { return m.p.Load().HalfLife() }
func (m *Menu) DtMax() uint64                          { return m.p.Load().DtMax() }
func (m *Menu) DocumentRevealOf(a pow.Algorithm) uint64 {
	return m.p.Load().DocumentRevealOf(a)
}
func (m *Menu) ConstantsChanges() []ctypes.IgneumConstantsChange {
	return m.p.Load().ConstantsChanges()
}
func (m *Menu) ConstantsAt(height uint64) ctypes.IgneumConstantsChange {
	return m.p.Load().ConstantsAt(height)
}
func (m *Menu) HalfLifeAt(height uint64) uint64        { return m.p.Load().HalfLifeAt(height) }
func (m *Menu) DtMaxAt(height uint64) uint64           { return m.p.Load().DtMaxAt(height) }
func (m *Menu) LagCapAt(c uint8, height uint64) uint64 { return m.p.Load().LagCapAt(c, height) }
func (m *Menu) Coordinate(c uint8) (Coordinate, bool)  { return m.p.Load().Coordinate(c) }
func (m *Menu) ByCoordinate(c uint8) pow.Algorithm     { return m.p.Load().ByCoordinate(c) }
func (m *Menu) CoordinateAt(c uint8, height uint64) (Coordinate, bool) {
	return m.p.Load().CoordinateAt(c, height)
}
func (m *Menu) ByCoordinateAt(c uint8, height uint64) pow.Algorithm {
	return m.p.Load().ByCoordinateAt(c, height)
}
func (m *Menu) Changes(c uint8) []CoordinateChange { return m.p.Load().Changes(c) }
func (m *Menu) UnkeyedWorkParamsAt(height uint64) (any, common.Hash, bool) {
	return m.p.Load().UnkeyedWorkParamsAt(height)
}
func (m *Menu) UnkeyedByCoordinateAt(c uint8, height uint64) (any, common.Hash, bool) {
	return m.p.Load().UnkeyedByCoordinateAt(c, height)
}
func (m *Menu) UnkeyedAlgorithmAt(c uint8, height uint64) pow.Algorithm {
	return m.p.Load().UnkeyedAlgorithmAt(c, height)
}
func (m *Menu) Name() string                       { return m.p.Load().Name() }
func (m *Menu) Epoch(height uint64) uint64         { return m.p.Load().Epoch(height) }
func (m *Menu) SeedHash(height uint64) common.Hash { return m.p.Load().SeedHash(height) }
func (m *Menu) Vectors() []pow.Vector              { return m.p.Load().Vectors() }
func (m *Menu) Compute(headerHash common.Hash, height uint64, nonce uint64) (common.Hash, common.Hash) {
	return m.p.Load().Compute(headerHash, height, nonce)
}
func (m *Menu) Verify(headerHash common.Hash, height uint64, nonce uint64, mixDigest common.Hash, difficulty *big.Int, fullDataset bool) error {
	return m.p.Load().Verify(headerHash, height, nonce, mixDigest, difficulty, fullDataset)
}
func (m *Menu) Anchor(height uint64)      { m.p.Load().Anchor(height) }
func (m *Menu) SetFullDataset(full bool)  { m.p.Load().SetFullDataset(full) }
func (m *Menu) SetSingleDataset(on bool)  { m.p.Load().SetSingleDataset(on) }
func (m *Menu) SetSealCoordinate(c uint8) { m.p.Load().SetSealCoordinate(c) }
func (m *Menu) Close()                    { m.p.Load().Close() }
