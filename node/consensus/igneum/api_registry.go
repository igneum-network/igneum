// Copyright 2026 The Igneum Authors
// This file is part of the go-ethereum library (Igneum fork).

package igneum

import (
	"errors"
	"encoding/json"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/params/types/ctypes"
	"github.com/ethereum/go-ethereum/rpc"
)

// RegistryCoordinate is one coordinate of the chain-held registration as
// igneum_vectorRegistry serves it (cut B; the node team's method, its first
// form by the protocol team).
type RegistryCoordinate struct {
	Index        uint8           `json:"index"`
	Name         string          `json:"name"`
	Algorithm    string          `json:"algorithm"`
	Params       json.RawMessage `json:"params"`
	ParamsDigest common.Hash     `json:"paramsDigest"`
	Activation   uint64          `json:"activation"`
	RetiredFrom  uint64          `json:"retiredFrom"`
	Successor    *uint64         `json:"successor"`
	RevealBlocks uint64          `json:"revealBlocks"`
	Target       uint64          `json:"target"`
	DRef         uint64          `json:"dRef"`
	DMin         uint64          `json:"dMin"`
	Active       bool            `json:"active"`
	Weight       uint64          `json:"weight"`
	// Sponsored and Escrow (SPONSORED-LANE-NODE-FORM.md section 1): the
	// lane's mark and its escrow address, derived from the chain id and the
	// coordinate, held by the protocol with no key; absent on a base lane.
	Sponsored bool            `json:"sponsored,omitempty"`
	Escrow    *common.Address `json:"escrow,omitempty"`
	// DocumentChanges are the coordinate's in-place document changes
	// (LANE-MODIFICATION.md section 1), each with its document's digest.
	DocumentChanges []RegistryDocumentChange `json:"documentChanges,omitempty"`
}

// RegistryDocumentChange is one in-place document change of a coordinate
// as the registry view serves it.
type RegistryDocumentChange struct {
	Height       uint64          `json:"height"`
	Void         bool            `json:"void,omitempty"`   // a void entry naming the change at its height (section 1b)
	Voided       bool            `json:"voided,omitempty"` // a change a void entry names: never in force
	Params       json.RawMessage `json:"params,omitempty"`
	ParamsDigest common.Hash     `json:"paramsDigest"`
	DRef         uint64          `json:"dRef"`
	DMin         uint64          `json:"dMin"`
	LagCap       uint64          `json:"lagCap"`
	InForce      bool            `json:"inForce"` // the form in force at the view's height
}

// RegistryLanding is one landed re-registration.
type RegistryLanding struct {
	Height uint64      `json:"height"`
	Hash   common.Hash `json:"hash"`
	TxHash common.Hash `json:"txHash"`
}

// RegistryInfo is igneum_vectorRegistry's answer: the registration in force
// at the asked height and the landings on the canonical chain.
type RegistryInfo struct {
	Height uint64 `json:"height"`
	// DocumentChangeEntry is true on every node that admits a coordinate's
	// in-place document changes (LANE-MODIFICATION.md section 1a); a node
	// before them answers a view without the field, and igneum-regcheck's
	// pre-flight refuses a document carrying documentChanges until every
	// node of record answers true (a landing an old node refuses forks the
	// chain between versions at the change's height).
	DocumentChangeEntry bool           `json:"documentChangeEntry"`
	RegistryKey         common.Address `json:"registryKey"`
	Registry            common.Address `json:"registryAddress"`
	Selector            hexutil.Bytes  `json:"selector"`
	ChainHeld           bool           `json:"chainHeld"`
	// NoticeBlocks 0 means NO notice (a research chain: a landing's
	// activation may be the next block); MaxLiveBlocks 0 means no
	// expiry of a form without a successor. A public chain sets both.
	NoticeBlocks  uint64   `json:"noticeBlocks"`
	MaxLiveBlocks uint64   `json:"maxLiveBlocks"`
	Window        uint64   `json:"window"`
	PayShares     []uint64 `json:"payShares"`
	// BalancedLanes is the breadth's lanes at the height as an index list
	// (not bytes: a []uint8 marshals as base64, the node team's read).
	BalancedLanes []int                `json:"balancedLanes"`
	Coordinates   []RegistryCoordinate `json:"coordinates"`
	Landings      []RegistryLanding    `json:"landings"`
	// The height-gated rules of the registration in force, served whole so
	// the explorer and the settlement team read the registry from the RPC
	// and never from a node's journal (the review lead's cell on F-151's landing
	// read, 15 October 2026): the clock constants' changes and the
	// constants in force at the height (F-151), the pay and lanes changes,
	// and the activation heights of the balanced rules, the fold (F-165)
	// and the sealing breadth (F-166), zero where unset.
	ConstantsChanges   []ctypes.IgneumConstantsChange `json:"constantsChanges,omitempty"`
	Constants          ctypes.IgneumConstantsChange   `json:"constants"`
	PayChanges         []ctypes.IgneumPayChange       `json:"payChanges,omitempty"`
	LanesChanges       []RegistryLanesChange          `json:"balancedLanesChanges,omitempty"`
	BalancedFrom       uint64                         `json:"balancedFrom"`
	BalancedV2From     uint64                         `json:"balancedV2From"`
	BalancedV3From     uint64                         `json:"balancedV3From"`
	BalancedMinFrom    uint64                         `json:"balancedMinFrom"`
	SealerFoldFrom     uint64                         `json:"sealerFoldFrom"`
	SealerFoldListFrom uint64                         `json:"sealerFoldListFrom"`
	SealingBreadthFrom uint64                         `json:"sealingBreadthFrom"`
	// SealerFold and SealingBreadth are the two rules' states at the height.
	SealerFold     bool `json:"sealerFold"`
	SealingBreadth bool `json:"sealingBreadth"`
}

// RegistryLanesChange is one height-gated change of the breadth's lanes as
// an index list (a []uint8 marshals as base64).
type RegistryLanesChange struct {
	Height uint64 `json:"height"`
	Lanes  []int  `json:"lanes"`
}

// VectorRegistry is the chain-held registration at a height ("latest" the
// head): every coordinate with its document and digest, its activation,
// retirement, successor and reveal depth, whether it is active at the
// height and its pay weight there; the registry key, address and selector
// a re-registration transaction uses; the landings so far.
func (api *IgneumAPI) VectorRegistry(at *rpc.BlockNumberOrHash) (*RegistryInfo, error) {
	m := api.engine.vectorMenu()
	if m == nil {
		return nil, errVectorCoord
	}
	var height uint64
	if api.chain != nil {
		if h := api.chain.CurrentHeader(); h != nil {
			height = h.Number.Uint64()
		}
	}
	if at != nil {
		if n, ok := at.Number(); ok && n >= 0 {
			height = uint64(n)
		} else if h, ok := at.Hash(); ok && api.chain != nil {
			if hd := api.chain.GetHeaderByHash(h); hd != nil {
				height = hd.Number.Uint64()
			}
		}
	}
	v := m.Registration()
	info := &RegistryInfo{Height: height, DocumentChangeEntry: true, RegistryKey: v.RegistryKey, Registry: VectorRegistryAddress, Selector: VectorRegistrySelector, ChainHeld: v.RegistryKey != (common.Address{}), NoticeBlocks: v.NoticeBlocks, MaxLiveBlocks: v.MaxLiveBlocks, Window: m.Window(), PayShares: m.PaySharesAt(height)}
	for _, l := range m.BalancedLanesAt(height) {
		info.BalancedLanes = append(info.BalancedLanes, int(l))
	}
	w := m.PaySharesAt(height)
	for i, c := range v.Coordinates {
		rc := RegistryCoordinate{Index: uint8(i), Name: c.Name, Algorithm: c.Algorithm, Params: json.RawMessage(c.AlgorithmParams), Activation: c.Activation, RetiredFrom: c.RetiredFrom, Successor: c.Successor, RevealBlocks: c.RevealBlocks, Target: c.Target, DRef: c.DRef, DMin: c.DMin, Active: m.ActiveAt(uint8(i), height)}
		if i < len(w) {
			rc.Weight = w[i]
		}
		if c.Sponsored {
			rc.Sponsored = true
			if api.chain != nil && api.chain.Config() != nil {
				if id := api.chain.Config().GetChainID(); id != nil {
					esc := SponsoredEscrow(id.Uint64(), uint8(i))
					rc.Escrow = &esc
				}
			}
		}
		if a := m.ByCoordinate(uint8(i)); a != nil {
			if d, ok := a.(interface{ ParamsDigest() common.Hash }); ok {
				rc.ParamsDigest = d.ParamsDigest()
			}
		}
		inForce := m.ByCoordinateAt(uint8(i), height)
		voided := map[uint64]bool{}
		for _, dc := range c.DocumentChanges {
			if dc.Void {
				voided[dc.Height] = true
			}
		}
		for _, dc := range c.DocumentChanges {
			rd := RegistryDocumentChange{Height: dc.Height, Void: dc.Void, Voided: !dc.Void && voided[dc.Height], Params: json.RawMessage(dc.AlgorithmParams), DRef: dc.DRef, DMin: dc.DMin, LagCap: dc.LagCap}
			// The built form of a live change (the menu holds none for a
			// void or a voided entry): its digest and whether it is in
			// force at the view's height.
			for _, ch := range m.Changes(uint8(i)) {
				if ch.From == dc.Height && !dc.Void && !rd.Voided && ch.Algorithm != nil {
					if d, ok := ch.Algorithm.(interface{ ParamsDigest() common.Hash }); ok {
						rd.ParamsDigest = d.ParamsDigest()
					}
					rd.InForce = ch.Algorithm == inForce
				}
			}
			rc.DocumentChanges = append(rc.DocumentChanges, rd)
		}
		info.Coordinates = append(info.Coordinates, rc)
	}
	for _, l := range api.engine.VectorLandings() {
		info.Landings = append(info.Landings, RegistryLanding{Height: l.Height, Hash: l.Hash, TxHash: l.TxHash})
	}
	info.ConstantsChanges = m.ConstantsChanges()
	info.Constants = m.ConstantsAt(height)
	info.PayChanges = m.PayChanges()
	for _, c := range v.BalancedLanesChanges {
		lc := RegistryLanesChange{Height: c.Height}
		for _, l := range c.Lanes {
			lc.Lanes = append(lc.Lanes, int(l))
		}
		info.LanesChanges = append(info.LanesChanges, lc)
	}
	info.BalancedFrom, info.BalancedV2From, info.BalancedV3From, info.BalancedMinFrom = v.BalancedFrom, v.BalancedV2From, v.BalancedV3From, v.BalancedMinFrom
	info.SealerFoldFrom, info.SealerFoldListFrom, info.SealingBreadthFrom = v.SealerFoldFrom, v.SealerFoldListFrom, v.SealingBreadthFrom
	info.SealerFold, info.SealingBreadth = m.SealerFoldAt(height), m.SealingBreadthFrom() > 0 && height >= m.SealingBreadthFrom()
	return info, nil
}

// SponsoredLane is igneum_sponsoredLane: a sponsored coordinate's escrow and
// its lock records, roots and paid windows as the state at the asked
// block holds them (the sponsor's eye on a refusal, whose transaction's
// receipt reads success: the security review's cell on commit two).
func (api *IgneumAPI) SponsoredLane(c uint8, number *rpc.BlockNumber) (*SponsoredLaneInfo, error) {
	if api.chain == nil || api.chain.Config() == nil || api.chain.Config().GetChainID() == nil {
		return nil, errors.New("no chain id")
	}
	st, err := api.stateAt(number)
	if err != nil {
		return nil, err
	}
	return sponsoredLaneView(st, api.chain.Config().GetChainID().Uint64(), c), nil
}
