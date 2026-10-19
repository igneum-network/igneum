// Copyright 2026 The Igneum Authors
// This file is part of the go-ethereum library (Igneum fork).

package igneum

import (
	"bytes"

	"encoding/json"
	"errors"
	"fmt"
	"github.com/ethereum/go-ethereum/common"

	"github.com/ethereum/go-ethereum/params/types/ctypes"
)

// Reregistration admits a vector document given to a persisted chain
// (F-113): with no stored document it is the first registration; otherwise
// it must keep every stored coordinate's entry as it is (compared as
// JSON) and the document's constants, and each coordinate it adds must
// activate above the chain's head, so the chain below the head reads the
// same under both.
func Reregistration(stored, given *ctypes.IgneumVector, head uint64) (rewindTo uint64, err error) {
	return ReregistrationFrom(stored, given, head, 0)
}

// ReregistrationFrom is Reregistration with the chain's notice-floor gate
// (F-192, 16 October 2026): the notice floor (a chain-held registration
// needs noticeBlocks of at least 64 unless marked research; cut at 11:45 UK
// on 15 October) binds only at a head at or above noticeFrom, 0 binding
// everywhere. The research chain's landings 4115 to 5379 were admitted by
// its nodes before the rule and carry no research mark, so a fresh walk
// under the rule refused every one of them, never took the 4206 landing
// whose pay entry binds at 4211, and stopped there deriving 4211 under the
// weights of 3622; the chain's gate is the height the rule reached its
// nodes (params.IgneumChainRegistryNoticeFrom: the research chain's 0.10.18
// roll, so the unmarked document of 5947 is refused on a fresh walk as the
// chain refused it, and the marked one of 5949 admitted).
func ReregistrationFrom(stored, given *ctypes.IgneumVector, head uint64, noticeFrom uint64) (rewindTo uint64, err error) {
	if given == nil {
		return 0, errors.New("no vector document")
	}
	if stored == nil {
		return 0, nil
	}
	if len(given.Coordinates) < len(stored.Coordinates) {
		return 0, fmt.Errorf("the document drops coordinates (%d stored, %d given)", len(stored.Coordinates), len(given.Coordinates))
	}
	// Cut B: the registry key is set once and immutable from then.
	if stored.RegistryKey != (common.Address{}) && given.RegistryKey != stored.RegistryKey {
		// The rotation: the stored next key at or past its height may
		// become the key (the landing applies it at RegistryKeyFrom).
		if !(stored.RegistryKeyNext != (common.Address{}) && given.RegistryKey == stored.RegistryKeyNext && stored.RegistryKeyFrom > 0 && stored.RegistryKeyFrom <= head+1) {
			return 0, errors.New("registryKey differs from the stored registration")
		}
	}
	// Cut B's public-chain residual, at the ADMISSION (a stored
	// registration always loads): a chain-held registration carries a
	// notice of at least the finality depth, 64, unless it marks itself
	// research; the notice is immutable, so a public chain registered at
	// 64 or more gains nothing by the mark.
	if (noticeFrom == 0 || head >= noticeFrom) && given.RegistryKey != (common.Address{}) && !given.Research && given.NoticeBlocks < 64 {
		return 0, fmt.Errorf("a chain-held registration (registryKey set) needs noticeBlocks of at least 64 unless marked research (%d)", given.NoticeBlocks)
	}
	// The cadence's constants: immutable once stored.
	if stored.NoticeBlocks != given.NoticeBlocks || stored.MaxLiveBlocks != given.MaxLiveBlocks {
		return 0, errors.New("noticeBlocks or maxLiveBlocks differs from the stored registration (a published rule change)")
	}
	for i := range stored.Coordinates {
		a, _ := json.Marshal(stored.Coordinates[i])
		b, _ := json.Marshal(given.Coordinates[i])
		if bytes.Equal(a, b) {
			continue
		}
		// REVISION-CADENCE.md cut A: a stored coordinate may take a
		// retirement height (once) and a successor (once), everything
		// else as stored. The retirement is admitted whatever the local
		// head (a consensus constant's admission never depends on when
		// a node reads the file, the review lead's read 14 October 2026): a
		// node past it rewinds to retiredFrom - 1 so the blocks above
		// re-import under the retirement. The notice rule (retiredFrom
		// at least noticeBlocks above the publication height) is the
		// registrar's and is checked against the registry transaction's
		// inclusion height in cut B, identically on every node.
		sc, gc := stored.Coordinates[i], given.Coordinates[i]
		sc2, gc2 := sc, gc
		sc2.RetiredFrom, gc2.RetiredFrom = 0, 0
		sc2.Successor, gc2.Successor = nil, nil
		// LANE-MODIFICATION.md section 1: a stored coordinate may take
		// in-place document changes APPENDED above the head (the stored
		// entries immutable, the notice the landing's rule); everything
		// else as stored.
		if len(gc.DocumentChanges) < len(sc.DocumentChanges) {
			return 0, fmt.Errorf("coordinate %d (%s): documentChanges drops stored entries", i, sc.Name)
		}
		for j := range sc.DocumentChanges {
			if !sameDocumentChangeEntry(sc.DocumentChanges[j], gc.DocumentChanges[j]) {
				return 0, fmt.Errorf("coordinate %d (%s): documentChanges[%d] differs from the stored registration (the stored entries are immutable)", i, sc.Name, j)
			}
		}
		for j := len(sc.DocumentChanges); j < len(gc.DocumentChanges); j++ {
			d := gc.DocumentChanges[j]
			if d.Void {
				// A void names a PENDING stored change (section 1b): one
				// at its height above the head, not voided already.
				if !pendingChange(sc, d.Height, head) {
					return 0, fmt.Errorf("coordinate %d (%s): documentChanges[%d] voids no pending stored change at %d (a void names a stored change above the head %d, not voided already)", i, sc.Name, j, d.Height, head)
				}
				continue
			}
			if d.Height <= head {
				return 0, fmt.Errorf("coordinate %d (%s): documentChanges[%d] at %d is at or under the head %d", i, sc.Name, j, d.Height, head)
			}
		}
		// The design lock (section 1b): a pending change at X makes a
		// retirement at or under X inadmissible, so no coordinate retires
		// under a document the chain has not yet reached; the registrant
		// voids the change first (an append-only void entry before X),
		// after which the retirement admits.
		if gc.RetiredFrom != 0 && gc.RetiredFrom != sc.RetiredFrom {
			for _, d := range liveDocumentChanges(gc) {
				if d.Height > head && gc.RetiredFrom <= d.Height {
					return 0, fmt.Errorf("coordinate %d (%s): a pending document change at %d locks a retirement at or under it (%d); void the change first (an entry {height: %d, void: true} landed before %d)", i, sc.Name, d.Height, gc.RetiredFrom, d.Height, d.Height)
				}
			}
		}
		sc2.DocumentChanges, gc2.DocumentChanges = nil, nil
		a2, _ := json.Marshal(sc2)
		b2, _ := json.Marshal(gc2)
		if bytes.Equal(a2, b2) {
			if sc.RetiredFrom != gc.RetiredFrom {
				if sc.RetiredFrom != 0 {
					return 0, fmt.Errorf("coordinate %d (%s): retiredFrom differs from the stored registration", i, sc.Name)
				}
				if gc.RetiredFrom <= head && (rewindTo == 0 || gc.RetiredFrom-1 < rewindTo) {
					rewindTo = gc.RetiredFrom - 1
				}
			}
			if (sc.Successor == nil) != (gc.Successor == nil) || (sc.Successor != nil && *sc.Successor != *gc.Successor) {
				if sc.Successor != nil {
					return 0, fmt.Errorf("coordinate %d (%s): successor differs from the stored registration", i, sc.Name)
				}
			}
			continue
		}
		// A stored coordinate whose activation is still ABOVE the head
		// (nothing imported under it) may be replaced by one that also
		// activates above the head (the project's word of 18:3x UK, 14 October
		// 2026: vectorwork8's textures re-cut from 953 to the rewind
		// head plus ten); any other change is refused.
		if stored.Coordinates[i].Activation > head && given.Coordinates[i].Activation > head {
			continue
		}
		return 0, fmt.Errorf("coordinate %d (%s) differs from the stored registration", i, stored.Coordinates[i].Name)
	}
	if stored.HalfLife != given.HalfLife || stored.DtMax != given.DtMax || stored.Rule != given.Rule || stored.Window != given.Window || stored.WindowForm != given.WindowForm || stored.SeedGenesis != given.SeedGenesis || stored.PayByCoordinate != given.PayByCoordinate {
		return 0, errors.New("the document's constants differ from the stored registration")
	}
	// payShares: the stored coordinates' weights unchanged (a change goes
	// by payChanges at a height); an added coordinate appends its weight.
	ns := len(stored.Coordinates)
	if len(given.PayShares) < len(stored.PayShares) || len(given.PayShares) > len(given.Coordinates) {
		return 0, errors.New("payShares differs from the stored registration (a change goes by payChanges at a height)")
	}
	if len(stored.PayShares) > 0 {
		if !bytes.Equal(jsonOf(stored.PayShares), jsonOf(given.PayShares[:len(stored.PayShares)])) {
			return 0, errors.New("payShares differs from the stored registration (a change goes by payChanges at a height)")
		}
	} else if len(given.PayShares) > 0 {
		for i := 0; i < ns && i < len(given.PayShares); i++ {
			if given.PayShares[i] != 1 {
				return 0, errors.New("payShares differs from the stored registration (the stored form is the equal shares)")
			}
		}
	}
	// The balanced reward's activation (BalancedFrom): set once, above
	// the head; never changed or cleared.
	if stored.BalancedFrom != given.BalancedFrom {
		if stored.BalancedFrom != 0 {
			return 0, errors.New("balancedFrom differs from the stored registration")
		}
		if given.BalancedFrom <= head {
			return 0, fmt.Errorf("balancedFrom %d is not above the head %d", given.BalancedFrom, head)
		}
	}
	if stored.BalancedV2From != given.BalancedV2From {
		if stored.BalancedV2From != 0 {
			return 0, errors.New("balancedV2From differs from the stored registration")
		}
		if given.BalancedV2From <= head {
			return 0, fmt.Errorf("balancedV2From %d is not above the head %d", given.BalancedV2From, head)
		}
	}
	if stored.BalancedSealerFrom != given.BalancedSealerFrom {
		if stored.BalancedSealerFrom != 0 {
			return 0, errors.New("balancedSealerFrom differs from the stored registration")
		}
		if given.BalancedSealerFrom <= head {
			return 0, fmt.Errorf("balancedSealerFrom %d is not above the head %d", given.BalancedSealerFrom, head)
		}
	}
	if stored.BalancedLanesFrom != given.BalancedLanesFrom || !bytes.Equal(jsonOf(stored.BalancedLanes), jsonOf(given.BalancedLanes)) {
		if stored.BalancedLanesFrom != 0 && stored.BalancedLanesFrom <= head {
			return 0, errors.New("balancedLanes differs from the stored registration")
		}
		if given.BalancedLanesFrom <= head {
			return 0, fmt.Errorf("balancedLanesFrom %d is not above the head %d", given.BalancedLanesFrom, head)
		}
	}
	if stored.GraphSharesFrom != given.GraphSharesFrom || stored.GraphSharesPerBlock != given.GraphSharesPerBlock {
		if stored.GraphSharesFrom != 0 {
			return 0, errors.New("graphShares differs from the stored registration")
		}
		if given.GraphSharesFrom <= head {
			return 0, fmt.Errorf("graphSharesFrom %d is not above the head %d", given.GraphSharesFrom, head)
		}
	}
	if stored.BalancedMinFrom != given.BalancedMinFrom {
		if stored.BalancedMinFrom != 0 {
			return 0, errors.New("balancedMinFrom differs from the stored registration")
		}
		if given.BalancedMinFrom <= head {
			return 0, fmt.Errorf("balancedMinFrom %d is not above the head %d", given.BalancedMinFrom, head)
		}
	}
	if stored.SealerFoldFrom != given.SealerFoldFrom {
		if stored.SealerFoldFrom != 0 {
			return 0, errors.New("sealerFoldFrom differs from the stored registration")
		}
		if given.SealerFoldFrom <= head {
			return 0, fmt.Errorf("sealerFoldFrom %d is not above the head %d", given.SealerFoldFrom, head)
		}
	}
	if stored.SealerFoldListFrom != given.SealerFoldListFrom {
		if stored.SealerFoldListFrom != 0 {
			return 0, errors.New("sealerFoldListFrom differs from the stored registration")
		}
		if given.SealerFoldListFrom <= head {
			return 0, fmt.Errorf("sealerFoldListFrom %d is not above the head %d", given.SealerFoldListFrom, head)
		}
	}
	if stored.SealingBreadthFrom != given.SealingBreadthFrom {
		if stored.SealingBreadthFrom != 0 {
			return 0, errors.New("sealingBreadthFrom differs from the stored registration")
		}
		if given.SealingBreadthFrom <= head {
			return 0, fmt.Errorf("sealingBreadthFrom %d is not above the head %d", given.SealingBreadthFrom, head)
		}
	}
	if stored.BalancedV3From != given.BalancedV3From {
		if stored.BalancedV3From != 0 {
			return 0, errors.New("balancedV3From differs from the stored registration")
		}
		if given.BalancedV3From <= head {
			return 0, fmt.Errorf("balancedV3From %d is not above the head %d", given.BalancedV3From, head)
		}
	}
	if stored.BalancedPhiFrom != given.BalancedPhiFrom || stored.BalancedPhiDenV2 != given.BalancedPhiDenV2 {
		if stored.BalancedPhiFrom != 0 {
			return 0, errors.New("balancedPhiFrom differs from the stored registration")
		}
		if given.BalancedPhiFrom <= head {
			return 0, fmt.Errorf("balancedPhiFrom %d is not above the head %d", given.BalancedPhiFrom, head)
		}
	}
	if stored.BalancedPhiDen != given.BalancedPhiDen && stored.BalancedV2From != 0 {
		return 0, errors.New("balancedPhiDen differs from the stored registration")
	}
	// Pay weight changes (PayChanges): the stored entries kept as they
	// are, an appended entry at a height above the head.
	if len(given.PayChanges) < len(stored.PayChanges) {
		return 0, errors.New("the document drops payChanges entries")
	}
	for i := range stored.PayChanges {
		if !bytes.Equal(jsonOf(stored.PayChanges[i]), jsonOf(given.PayChanges[i])) {
			return 0, fmt.Errorf("payChanges[%d] differs from the stored registration", i)
		}
	}
	for i := len(stored.PayChanges); i < len(given.PayChanges); i++ {
		if given.PayChanges[i].Height <= head {
			return 0, fmt.Errorf("payChanges[%d] at %d is not above the head %d", i, given.PayChanges[i].Height, head)
		}
	}
	// Lanes changes (BalancedLanesChanges): the same shape as the pay
	// weight changes.
	if len(given.BalancedLanesChanges) < len(stored.BalancedLanesChanges) {
		return 0, errors.New("the document drops balancedLanesChanges entries")
	}
	for i := range stored.BalancedLanesChanges {
		if !bytes.Equal(jsonOf(stored.BalancedLanesChanges[i]), jsonOf(given.BalancedLanesChanges[i])) {
			return 0, fmt.Errorf("balancedLanesChanges[%d] differs from the stored registration", i)
		}
	}
	for i := len(stored.BalancedLanesChanges); i < len(given.BalancedLanesChanges); i++ {
		if given.BalancedLanesChanges[i].Height <= head {
			return 0, fmt.Errorf("balancedLanesChanges[%d] at %d is not above the head %d", i, given.BalancedLanesChanges[i].Height, head)
		}
	}
	// Clock constant changes (ConstantsChanges, F-151): the same shape.
	if len(given.ConstantsChanges) < len(stored.ConstantsChanges) {
		return 0, errors.New("the document drops constantsChanges entries")
	}
	for i := range stored.ConstantsChanges {
		if !bytes.Equal(jsonOf(stored.ConstantsChanges[i]), jsonOf(given.ConstantsChanges[i])) {
			return 0, fmt.Errorf("constantsChanges[%d] differs from the stored registration", i)
		}
	}
	for i := len(stored.ConstantsChanges); i < len(given.ConstantsChanges); i++ {
		if given.ConstantsChanges[i].Height <= head {
			return 0, fmt.Errorf("constantsChanges[%d] at %d is not above the head %d", i, given.ConstantsChanges[i].Height, head)
		}
	}
	// An added coordinate must carry an activation; one at or below the
	// head means this node imported blocks above it under the stored
	// registration, so the chain rewinds to the block before the lowest
	// such activation and takes the document (the hole the node team named:
	// a refusal there left a late node forked for good).
	for i := len(stored.Coordinates); i < len(given.Coordinates); i++ {
		act := given.Coordinates[i].Activation
		if act == 0 {
			return 0, fmt.Errorf("coordinate %d (%s) is added without an activation height", i, given.Coordinates[i].Name)
		}
		if act <= head && (rewindTo == 0 || act-1 < rewindTo) {
			rewindTo = act - 1
		}
	}
	return rewindTo, nil
}

// jsonOf is a value's JSON (the admission's byte comparison).
// sameDocumentChangeEntry reports whether two in-place document change
// entries are one entry: the height, the kind (a change or a void), the
// document bytes, the reference, the floor and the cap.
func sameDocumentChangeEntry(a, b ctypes.IgneumDocumentChange) bool {
	return a.Height == b.Height && a.Void == b.Void && a.DRef == b.DRef && a.DMin == b.DMin && a.LagCap == b.LagCap && bytes.Equal(bytes.TrimSpace(a.AlgorithmParams), bytes.TrimSpace(b.AlgorithmParams))
}

// liveDocumentChanges are a coordinate's non-void entries no void names.
func liveDocumentChanges(c ctypes.IgneumCoordinate) []ctypes.IgneumDocumentChange {
	voided := map[uint64]bool{}
	for _, d := range c.DocumentChanges {
		if d.Void {
			voided[d.Height] = true
		}
	}
	var out []ctypes.IgneumDocumentChange
	for _, d := range c.DocumentChanges {
		if !d.Void && !voided[d.Height] {
			out = append(out, d)
		}
	}
	return out
}

// pendingChange reports whether the stored coordinate carries a live change
// at height above the head (the one a void may name).
func pendingChange(c ctypes.IgneumCoordinate, height, head uint64) bool {
	for _, d := range liveDocumentChanges(c) {
		if d.Height == height && height > head {
			return true
		}
	}
	return false
}

func jsonOf(v interface{}) []byte {
	b, _ := json.Marshal(v)
	return b
}
