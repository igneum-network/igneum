package igneum

import (
	"math/big"
	"sort"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/consensus/igneum/shares"
	"github.com/ethereum/go-ethereum/core/types"
)

// The balanced reward (C-38; docs/consensus/BALANCED-REWARD.md, protocol
// B's row at 431897e11; the security review's list row at 96c3a26c7; the founder's
// word of 09:5x UK, 14 October 2026: built in the node now, on a research
// chain, the outside review only when it works). The FIRST CUT, as ordered: a
// key's claim on a block's pooled subsidy is its BALANCED WORK over the pay
// account (the last K_pay blocks' carried lists, this block's included),
// the weighted geometric mean of its normalised work per coordinate, with
// the minimum unit mu_c and NO soft floor (phi absent, the floor account
// unread); the identity unit is the share's KEY (its address, bound in the
// share's preimage H_s), and the share's COORDINATE is the carrying block's
// coordinate tag (a share is verified under its coordinate's algorithm by
// the sealer of that coordinate and carried only by that sealer's block,
// whose seal binds the tag), so no preimage or worker form changes. Behind
// the registration's balancedFrom height (IgneumVector), a payout-list
// validation change and nothing else in consensus.
//
// The integer form (section 6): N_{w,c} = sum over w's shares in
// coordinate-c blocks of the window of (the block's share difficulty x
// 2^step); X_{w,c} = max(N_{w,c}, m_c) with m_c the minimum unit, one
// share's target work in c at this height; B'_w = floor( (product over c of
// X_{w,c}^{p_c})^{1/q} ) with p_c the pay weights in force at the height
// (section 9's payShares, q their sum), the references' common factor
// dropped as the same for every key; the pooled share is split pro rata
// over B'_w by shares.Build as before, the sealer's term over their sum.

// balancedFrom is the registration's activation height of the balanced
// reward (0 off).
func (e *Engine) balancedFrom() uint64 {
	m := e.vectorMenu()
	if m == nil {
		return 0
	}
	return m.BalancedFrom()
}

// balancedOn reports whether the block at height settles under the
// balanced reward.
func (e *Engine) balancedOn(height uint64) bool {
	from := e.balancedFrom()
	return from > 0 && height >= from
}

// v2Terms are the breadth form's constants of the last derivation on this
// engine (igneum_wholeWork reads them right after balancedKeys for the
// same block; the derivation is deterministic in the block).
type v2Terms struct {
	L, unitL  *big.Int
	q, phiDen uint64
	lanes     []uint8
	shift     uint64
	v3        bool
	height    uint64
}

// balancedBlock is one window block's carried list as the rule reads it.
type balancedBlock struct {
	coord  uint8
	diff   *big.Int // the block's share difficulty
	block  *big.Int // the block's difficulty (the third cut's sealer term)
	sealer common.Address
	list   []types.MinedShare
}

// balancedWindowBlock reads an ancestor's carried list, coordinate and
// share difficulty (cached by hash, bounded with the carried cache).
func (e *Engine) balancedWindowBlock(chain consensus.ChainHeaderReader, h *types.Header) (*balancedBlock, bool) {
	e.balancedMu.Lock()
	if b, ok := e.balancedCache[h.Hash()]; ok {
		e.balancedMu.Unlock()
		return b, true
	}
	e.balancedMu.Unlock()
	c, ok := e.coordOf(h)
	if !ok {
		return nil, false
	}
	br, ok := chain.(blockReader)
	if !ok {
		return nil, false
	}
	blk := br.GetBlock(h.Hash(), h.Number.Uint64())
	if blk == nil {
		return nil, false
	}
	list, _ := types.ParseMinedSharesForm(blk.MinedShares(), e.shareFormAt(h.Number.Uint64()))
	b := &balancedBlock{coord: c, diff: e.shareDifficulty(h.Difficulty), block: new(big.Int).Set(h.Difficulty), sealer: sealerOf(h, blk.Withdrawals()), list: list}
	e.balancedMu.Lock()
	if e.balancedCache == nil || len(e.balancedCache) >= carriedCacheMax {
		e.balancedCache = make(map[common.Hash]*balancedBlock)
	}
	e.balancedCache[h.Hash()] = b
	e.balancedMu.Unlock()
	return b, true
}

// balancedEntries is the balanced form of acceptedEntries for the block
// whose parent is parent, sealed under coord with the given header
// difficulty, carrying list: one entry per key with any share in the pay
// window, its weight B'_w.
func (e *Engine) balancedEntries(chain consensus.ChainHeaderReader, parent *types.Header, coord uint8, difficulty *big.Int, sealer common.Address, list []types.MinedShare) []shares.Entry {
	keys := e.balancedKeys(chain, parent, coord, difficulty, sealer, list)
	if keys == nil {
		return acceptedEntries(list)
	}
	out := make([]shares.Entry, 0, len(keys))
	for _, k := range keys {
		out = append(out, shares.Entry{Address: k.Key, Weight: k.Weight})
	}
	return out
}

// BalancedKey is one key's settlement under the balanced reward at a block
// (igneum_wholeWork): its accrued work per coordinate over the pay window,
// the floored terms, the balanced work (the weight) and the minimum units.
type BalancedKey struct {
	Key      common.Address `json:"key"`
	Work     []*big.Int     `json:"work"`     // N_{w,c} on the pay window (64)
	Floored  []*big.Int     `json:"floored"`  // the first cut: X = max(N, mu_c); the breadth form: W'_c L (the floored, capped scaled lane work)
	Balanced *big.Int       `json:"balanced"` // the first cut: floor((prod X^{p_c})^{1/q}); the breadth form: the root of prod (W'_c L)^{p_c} capped (= L x B_w x K, K the shares' constant)
	Weight   uint64         `json:"weight"`   // the list weight: Balanced >> shift (the breadth form), Balanced bounded (the first cut)
	// The breadth form's served terms (the replay's inputs; nil under the first cut).
	Work256     []*big.Int `json:"work256,omitempty"`     // N_{w,c} on the floor account (256)
	ScaledLane  []*big.Int `json:"scaledLane,omitempty"`  // A_c = N_c x L / D_ref_c (the pay window)
	ScaledTotal *big.Int   `json:"scaledTotal,omitempty"` // T L = sum over the included lanes of A_c
	Scaled256   []*big.Int `json:"scaled256,omitempty"`   // A256_c = N256_c x L / D_ref_c
	Total256    *big.Int   `json:"total256,omitempty"`    // T256 L over the included lanes
	Over        []bool     `json:"over,omitempty"`        // the lane over its registered share (A_c q > T L p_c)
	LiftedByY   bool       `json:"liftedByY,omitempty"`   // an under lane lifted by y above its own x (the over lanes then read s_c)
	Eligible    bool       `json:"eligible,omitempty"`    // T L at or above one share's unit (else out of the split)
}

// balancedKeys is the rule's per-key settlement for the block whose parent
// is parent (nil off the vector form): the breadth form (section 9) from
// balancedV2From, the first cut's below it.
func (e *Engine) balancedKeys(chain consensus.ChainHeaderReader, parent *types.Header, coord uint8, difficulty *big.Int, sealer common.Address, list []types.MinedShare) []BalancedKey {
	m := e.vectorMenu()
	if m == nil {
		return nil
	}
	if v2 := m.BalancedV2From(); v2 > 0 && parent.Number.Uint64()+1 >= v2 {
		return e.balancedKeysV2(chain, parent, coord, difficulty, sealer, list)
	}
	n := m.Count()
	height := parent.Number.Uint64() + 1
	k := int(m.Window())
	if k == 0 {
		k = 64
	}
	// N_{w,c} over the window: this block's list first, then the
	// ancestors up to k-1 blocks.
	work := make(map[common.Address][]*big.Int)
	add := func(b *balancedBlock) {
		for _, s := range b.list {
			w := work[s.Address]
			if w == nil {
				w = make([]*big.Int, n)
				for i := range w {
					w[i] = new(big.Int)
				}
				work[s.Address] = w
			}
			d := new(big.Int).Lsh(b.diff, uint(s.Step))
			w[b.coord].Add(w[b.coord], d)
		}
	}
	add(&balancedBlock{coord: coord, diff: e.shareDifficulty(difficulty), list: list})
	cur := parent
	for i := 1; i < k && cur != nil; i++ {
		if b, ok := e.balancedWindowBlock(chain, cur); ok {
			add(b)
		}
		if cur.Number.Sign() == 0 {
			break
		}
		cur = chain.GetHeader(cur.ParentHash, cur.Number.Uint64()-1)
	}
	// The minimum unit per coordinate: one share's target work at this
	// height, from the coordinate's next difficulty on this branch.
	mu := make([]*big.Int, n)
	for c := 0; c < n; c++ {
		if d, err := e.vectorDifficulty(chain, parent, uint8(c)); err == nil {
			mu[c] = e.shareDifficulty(d)
		} else {
			mu[c] = big.NewInt(1)
		}
	}
	// The pay weights in force at the height, q their sum; a coordinate
	// not yet ACTIVE at the height has weight 0 and no factor (F-123, 14
	// October 2026: vectorwork8's coordinate 5 registered by activation at
	// 953 entered the derivation of every block from 200 on a resyncing
	// node, which then refused the chain's own 200).
	p := append([]uint64(nil), m.PaySharesAt(height)...)
	if len(p) != n {
		p = make([]uint64, n)
		for i := range p {
			p[i] = 1
		}
	}
	for c := 0; c < n; c++ {
		if !m.ActiveAt(uint8(c), height) {
			p[c] = 0
		}
	}
	var q uint64
	for _, x := range p {
		q += x
	}
	if q == 0 {
		return nil
	}
	out := make([]BalancedKey, 0, len(work))
	for a, w := range work {
		prod := big.NewInt(1)
		floored := make([]*big.Int, n)
		for c := 0; c < n; c++ {
			x := w[c]
			if x.Cmp(mu[c]) < 0 {
				x = mu[c]
			}
			floored[c] = new(big.Int).Set(x)
			if p[c] > 0 {
				prod.Mul(prod, new(big.Int).Exp(x, new(big.Int).SetUint64(p[c]), nil))
			}
		}
		b := integerRoot(prod, q)
		weight := uint64(1)
		if b.IsUint64() {
			weight = b.Uint64()
		} else {
			weight = ^uint64(0)
		}
		if weight == 0 {
			weight = 1
		}
		out = append(out, BalancedKey{Key: a, Work: w, Floored: floored, Balanced: b, Weight: weight})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key.Hex() < out[j].Key.Hex() })
	return out
}

// WholeWorkInfo is igneum_wholeWork's answer at a block: the rule's state
// and every key's settlement for that block's list.
type WholeWorkInfo struct {
	// The breadth form's constants at this block (nil under the first cut).
	Scale        *big.Int            `json:"scale,omitempty"` // L = the product of the ACTIVE coordinates' references
	Shift        uint64              `json:"shift"`           // the right shift that takes the largest root to 52 bits (every key alike)
	PhiDen       uint64              `json:"phiDen"`          // 1/phi in force (0: no phi floor)
	Lanes        []uint64            `json:"lanes,omitempty"` // the included lanes (active, registered, served under F-124)
	UnitL        *big.Int            `json:"unitL,omitempty"` // one share's unit times L / D_ref_c, the least over the included lanes (eligibility)
	Q            uint64              `json:"q"`               // the sum of the included lanes' weights
	FormV2       bool                `json:"formV2"`          // the breadth form at this height
	FormV3       bool                `json:"formV3"`          // F-124's form at this height
	Number       uint64              `json:"number"`
	Hash         common.Hash         `json:"hash"`
	Coord        uint8               `json:"coord"`
	BalancedFrom uint64              `json:"balancedFrom"`
	Active       bool                `json:"active"`
	Window       uint64              `json:"window"`
	PayShares    []uint64            `json:"payShares"`
	MinimumUnit  []*big.Int          `json:"minimumUnit"`
	Carried      []CarriedShare      `json:"carried"`
	Keys         []BalancedKey       `json:"keys"`
	Paid         map[string]*big.Int `json:"paid"` // the wei the block credited by address (the sealer's quarter included)
}

// CarriedShare is one carried share as the list row reads it: its key,
// coordinate (the carrying block's), step and the share's target work.
type CarriedShare struct {
	Key   common.Address `json:"key"`
	Coord uint8          `json:"coord"`
	Step  uint8          `json:"step"`
	Work  *big.Int       `json:"work"`
	Nonce uint64         `json:"nonce"`
	Extra uint64         `json:"extraNonce"`
}

// wholeWorkInfo builds igneum_wholeWork's answer for a canonical block.
func (e *Engine) wholeWorkInfo(chain consensus.ChainHeaderReader, header *types.Header, blk *types.Block) (*WholeWorkInfo, error) {
	m := e.vectorMenu()
	if m == nil {
		return nil, errVectorCoordOff
	}
	c, ok := e.coordOf(header)
	if !ok {
		return nil, errVectorCoord
	}
	height := header.Number.Uint64()
	info := &WholeWorkInfo{Number: height, Hash: header.Hash(), Coord: c, BalancedFrom: m.BalancedFrom(), Active: e.balancedOn(height), Window: m.Window(), PayShares: m.PaySharesAt(height), Paid: map[string]*big.Int{}}
	if info.Window == 0 {
		info.Window = 64
	}
	var list []types.MinedShare
	if blk != nil {
		list, _ = types.ParseMinedSharesForm(blk.MinedShares(), e.shareFormAt(height))
		// The wei the block credited, as Finalize splits it (the second
		// form over the block's list: the sealer's quarter, the pot pro
		// rata over the listed weights); the amount is the coordinate's
		// accrued share under the coordinate rule, else the miner part
		// (the fees, nil here, are the coinbase's on a research chain).
		if wl := blk.Withdrawals(); len(wl) > 0 {
			amount, _ := e.config.Parts.Issuance.Reward(height)
			if pot, ok := e.coordinatePay(chain, header); ok {
				amount = pot
			}
			list := shares.FromWithdrawals(wl)
			if e.payV2On(height) {
				a := payV2Amounts(amount, list, e.payQuorumWeight(), e.sealerFoldAt(height))
				for _, p := range a.Paid {
					info.Paid[p.Address.Hex()] = new(big.Int).Set(p.Amount)
				}
				if a.Quarter != nil && a.Quarter.Sign() > 0 {
					prev := info.Paid[a.Sealer.Hex()]
					if prev == nil {
						prev = new(big.Int)
					}
					info.Paid[a.Sealer.Hex()] = new(big.Int).Add(prev, a.Quarter)
				}
			} else {
				// The first form (v1): pro rata over every entry, the
				// sealer's term a weight among them.
				paid, _ := shares.Pay(amount, list)
				for _, p := range paid {
					prev := info.Paid[p.Address.Hex()]
					if prev == nil {
						prev = new(big.Int)
					}
					info.Paid[p.Address.Hex()] = new(big.Int).Add(prev, p.Amount)
				}
			}
		}
	}
	sd := e.shareDifficulty(header.Difficulty)
	for _, s := range list {
		info.Carried = append(info.Carried, CarriedShare{Key: s.Address, Coord: c, Step: s.Step, Work: new(big.Int).Lsh(sd, uint(s.Step)), Nonce: s.Nonce, Extra: s.ExtraNonce})
	}
	parent := chain.GetHeader(header.ParentHash, height-1)
	if parent == nil {
		return info, nil
	}
	for i := 0; i < m.Count(); i++ {
		if d, err := e.vectorDifficulty(chain, parent, uint8(i)); err == nil {
			info.MinimumUnit = append(info.MinimumUnit, e.shareDifficulty(d))
		} else {
			info.MinimumUnit = append(info.MinimumUnit, big.NewInt(1))
		}
	}
	var wl []*types.Withdrawal
	if blk != nil {
		wl = blk.Withdrawals()
	}
	info.Keys = e.balancedKeys(chain, parent, c, header.Difficulty, sealerOf(header, wl), list)
	if v2 := m.BalancedV2From(); v2 > 0 && height >= v2 {
		info.FormV2 = true
		e.balancedMu.Lock()
		t := e.lastV2
		e.balancedMu.Unlock()
		if t.height == height {
			info.Scale, info.UnitL, info.Q, info.PhiDen, info.Shift, info.FormV3 = t.L, t.unitL, t.q, t.phiDen, t.shift, t.v3
			for _, l := range t.lanes {
				info.Lanes = append(info.Lanes, uint64(l))
			}
		}
	}
	return info, nil
}

// integerRoot is floor(x^(1/q)) for x >= 0, q >= 1 (Newton's method on
// big integers, exact).
func integerRoot(x *big.Int, q uint64) *big.Int {
	if x.Sign() <= 0 || q <= 1 {
		return new(big.Int).Set(x)
	}
	qi := new(big.Int).SetUint64(q)
	qm1 := new(big.Int).SetUint64(q - 1)
	// Start above the root: 2^(ceil(bits/q)).
	r := new(big.Int).Lsh(big.NewInt(1), uint((x.BitLen()+int(q)-1)/int(q)))
	for {
		// r' = ((q-1) r + x / r^(q-1)) / q
		t := new(big.Int).Exp(r, qm1, nil)
		t.Div(x, t)
		t.Add(t, new(big.Int).Mul(qm1, r))
		t.Div(t, qi)
		if t.Cmp(r) >= 0 {
			break
		}
		r = t
	}
	// Floor correction.
	for new(big.Int).Exp(r, qi, nil).Cmp(x) > 0 {
		r.Sub(r, big.NewInt(1))
	}
	for {
		next := new(big.Int).Add(r, big.NewInt(1))
		if new(big.Int).Exp(next, qi, nil).Cmp(x) > 0 {
			break
		}
		r = next
	}
	return r
}

// balancedKeysV2 is WHOLEWORK.md section 9's breadth form (protocol B at
// a7e2b5e48, the second cut): the pay weight is homogeneous of degree
// one, B_w = T_w x beta_w, T_w the key's total normalised work on the pay
// window and beta_w = min(1, product over c of (x'_c / s_c)^{s_c}) its
// breadth on lane shares x_c = W_c / T_w floored INSIDE as x'_c =
// max(x_c, phi x s_c, y_c), y_c the key's lane share on the 256-block
// floor account; equivalently the first cut's q-th root over W'_c =
// min(T_w, max(W_c, phi s_c T_w, y_c T_w)), the per-key constant floor
// mu_c surviving only as eligibility (a key whose T_w is under one
// share's unit is out of the split). The integer form holds every term
// times the common scale L = product over c of D_ref_c (W_c L = N_c L /
// D_ref_c an integer), the floors by floor division, the q-th root over
// the product of (W'_c L)^{p_c} = L^q x the rule's product, so the root is
// L x B_w for every key, the same factor, and the split over it is the
// rule's exactly; the weights are then shifted together so the largest
// fits 52 bits (the list's uint64), the same shift on every node.
func (e *Engine) balancedKeysV2(chain consensus.ChainHeaderReader, parent *types.Header, coord uint8, difficulty *big.Int, sealer common.Address, list []types.MinedShare) []BalancedKey {
	m := e.vectorMenu()
	n := m.Count()
	height := parent.Number.Uint64() + 1
	k := int(m.Window())
	if k == 0 {
		k = 64
	}
	const kFloor = 256
	type acc struct{ pay, floor []*big.Int }
	work := make(map[common.Address]*acc)
	get := func(a common.Address) *acc {
		x := work[a]
		if x == nil {
			x = &acc{pay: make([]*big.Int, n), floor: make([]*big.Int, n)}
			for i := 0; i < n; i++ {
				x.pay[i], x.floor[i] = new(big.Int), new(big.Int)
			}
			work[a] = x
		}
		return x
	}
	sealerFrom := m.BalancedSealerFrom()
	add := func(b *balancedBlock, inPay bool, h uint64) {
		for _, s := range b.list {
			a := get(s.Address)
			d := new(big.Int).Lsh(b.diff, uint(s.Step))
			if inPay {
				a.pay[b.coord].Add(a.pay[b.coord], d)
			}
			a.floor[b.coord].Add(a.floor[b.coord], d)
		}
		// The third cut (2ac296768): the sealed block as its sealer's
		// lane work of the block's coordinate, the block's difficulty.
		if sealerFrom > 0 && h >= sealerFrom && b.block != nil && b.block.Sign() > 0 {
			a := get(b.sealer)
			if inPay {
				a.pay[b.coord].Add(a.pay[b.coord], b.block)
			}
			a.floor[b.coord].Add(a.floor[b.coord], b.block)
		}
	}
	add(&balancedBlock{coord: coord, diff: e.shareDifficulty(difficulty), block: difficulty, sealer: sealer, list: list}, true, height)
	cur := parent
	for i := 1; i < kFloor && cur != nil; i++ {
		if b, ok := e.balancedWindowBlock(chain, cur); ok {
			add(b, i < k, cur.Number.Uint64())
		}
		if cur.Number.Sign() == 0 {
			break
		}
		cur = chain.GetHeader(cur.ParentHash, cur.Number.Uint64()-1)
	}
	// The references, the common scale L and L / D_ref_c, over the
	// coordinates ACTIVE at the height alone (F-123's second half: a
	// coordinate added by activation entered L below its height, and the
	// weights shifted to 52 bits moved by its reference's residue, so a
	// node resyncing under the new registration refused the chain's own
	// blocks at 500 with every weight 1.341x).
	dref := make([]*big.Int, n)
	active := make([]bool, n)
	L := big.NewInt(1)
	for c := 0; c < n; c++ {
		co, _ := m.CoordinateAt(uint8(c), height) // the reference in force at the height
		dref[c] = co.DRef
		active[c] = m.ActiveAt(uint8(c), height)
		if active[c] {
			L.Mul(L, co.DRef)
		}
	}
	lOver := make([]*big.Int, n)
	for c := 0; c < n; c++ {
		if active[c] {
			lOver[c] = new(big.Int).Div(L, dref[c])
		} else {
			lOver[c] = new(big.Int)
		}
	}
	// The minimum units (eligibility): one share's unit in c, times L / D_ref_c.
	unitL := new(big.Int)
	for c := 0; c < n; c++ {
		if !active[c] {
			continue
		}
		d, err := e.vectorDifficulty(chain, parent, uint8(c))
		if err != nil {
			d = dref[c]
		}
		u := new(big.Int).Mul(e.shareDifficulty(d), lOver[c])
		if unitL.Sign() == 0 || u.Cmp(unitL) < 0 {
			unitL = u
		}
	}
	p := append([]uint64(nil), m.PaySharesAt(height)...)
	if len(p) != n {
		p = make([]uint64, n)
		for i := range p {
			p[i] = 1
		}
	}
	// Section 10 (a): the breadth over the registered lanes only, the
	// shares renormalised over them (a lane left out has p_c = 0 and
	// contributes no factor; its work is outside the split).
	lanes := m.BalancedLanesAt(height)
	inLane := make([]bool, n)
	for _, l := range lanes {
		if int(l) < n && m.ActiveAt(l, height) {
			inLane[l] = true
		}
	}
	v3 := m.BalancedV3At(height)
	minForm := m.BalancedMinAt(height)
	if v3 {
		// F-124 (2): a lane with no work by any key in the pay window
		// is outside the breadth for this window.
		served := make([]bool, n)
		for _, w := range work {
			for c := 0; c < n; c++ {
				if w.pay[c].Sign() > 0 {
					served[c] = true
				}
			}
		}
		for c := 0; c < n; c++ {
			if !served[c] {
				inLane[c] = false
			}
		}
	}
	for c := 0; c < n; c++ {
		if !inLane[c] {
			p[c] = 0
		}
	}
	var q uint64
	for _, x := range p {
		q += x
	}
	if q == 0 {
		return nil
	}
	// phi at the height: the denominator times q; zero means no floor.
	phiDenU := m.PhiDenAt(height)
	phiDen := new(big.Int).SetUint64(phiDenU * q)
	type keyed struct {
		key     common.Address
		work    []*big.Int
		work256 []*big.Int
		floored []*big.Int
		root    *big.Int
		AL      []*big.Int
		TL      *big.Int
		A256    []*big.Int
		T256L   *big.Int
		over    []bool
		lifted  bool
	}
	var keys []keyed
	maxBits := 0
	e.balancedMu.Lock()
	e.lastV2 = v2Terms{L: new(big.Int).Set(L), unitL: new(big.Int).Set(unitL), q: q, phiDen: phiDenU, lanes: nil, v3: v3, height: height}
	for c := 0; c < n; c++ {
		if inLane[c] {
			e.lastV2.lanes = append(e.lastV2.lanes, uint8(c))
		}
	}
	e.balancedMu.Unlock()
	for a, w := range work {
		TL := new(big.Int)
		AL := make([]*big.Int, n)
		for c := 0; c < n; c++ {
			AL[c] = new(big.Int).Mul(w.pay[c], lOver[c])
			if inLane[c] {
				TL.Add(TL, AL[c])
			}
		}
		if TL.Cmp(unitL) < 0 {
			continue // under one share's unit: out of the split
		}
		T256L := new(big.Int)
		A256 := make([]*big.Int, n)
		for c := 0; c < n; c++ {
			A256[c] = new(big.Int).Mul(w.floor[c], lOver[c])
			if inLane[c] {
				T256L.Add(T256L, A256[c])
			}
		}
		// The over-share cap (190353552, the security review's plateau): the
		// floors lift only lanes the key is at or under its registered
		// share on (x_c <= s_c, i.e. AL_c x q <= TL x p_c); a lane it is
		// over on keeps x_c, unless any under-share lane was lifted by y
		// above its own x, in which case the over lane reads s_c exactly.
		floored := make([]*big.Int, n)
		over := make([]bool, n)
		liftedByY := false
		for c := 0; c < n; c++ {
			x := new(big.Int).Set(AL[c])
			if !inLane[c] {
				floored[c] = x
				continue
			}
			sT := new(big.Int).Mul(TL, new(big.Int).SetUint64(p[c])) // s_c T L x q
			over[c] = new(big.Int).Mul(AL[c], new(big.Int).SetUint64(q)).Cmp(sT) > 0
			if !over[c] {
				// phi s_c T_w: TL x p_c / (q x phiDen); no floor at phi 0.
				// Under the minimum rule phi is 0 by the rule's own (designer
				// B's cell 1, 15 October 2026: a floor at 1/8 paid a lone
				// stacked chip 1.27x to 1.51x a card with no card work).
				if phiDenU > 0 && !minForm {
					f := new(big.Int).Div(sT, phiDen)
					if f.Cmp(x) > 0 {
						x = f
					}
				}
				// y_c T_w: A256_c x TL / T256L. Under the minimum rule the
				// lift is bounded by the key's OWN 256-window lane work,
				// A256_c (the design review's cell 2: a chip alternating lanes in
				// time took 6 to 17 percent over balancing through the y
				// floor on the 256-block account; the lift never exceeds
				// what the key itself did on the lane over the long window).
				if T256L.Sign() > 0 {
					y := new(big.Int).Mul(A256[c], TL)
					y.Div(y, T256L)
					if minForm {
						// The bound scaled to the pay window (the design review's
						// re-simulation on the cut, 3f0355845 section 4: at
						// the unscaled 256-window work, four pay windows, it
						// never bound and a lane-alternating key read 6 to 17
						// percent over its balanced twin; at A256_c x window /
						// 256 it reads 1.11x to 1.27x, under the twin). F-192
						// (16 October 2026): the scaling rolled onto the
						// research chain in node 0.10.18 without a height
						// gate, so the blocks sealed before it (the unscaled
						// bound, which never bound) re-derived under the scaled
						// one and a fresh walk stopped at 4211; the unscaled
						// bound stands up to the chain's payBoundUnscaledUntil
						// (params.IgneumPayBoundUnscaledUntil), the scaled one
						// above it.
						if y.Cmp(minPayBound(A256[c], m.Window(), !e.payBoundUnscaledAt(height))) > 0 {
							y = minPayBound(A256[c], m.Window(), !e.payBoundUnscaledAt(height))
						}
					}
					if y.Cmp(x) > 0 {
						x = y
						if y.Cmp(AL[c]) > 0 {
							liftedByY = true
						}
					}
				}
			}
			if x.Cmp(TL) > 0 {
				x = new(big.Int).Set(TL)
			}
			floored[c] = x
		}
		if liftedByY {
			for c := 0; c < n; c++ {
				if over[c] {
					floored[c] = new(big.Int).Div(new(big.Int).Mul(TL, new(big.Int).SetUint64(p[c])), new(big.Int).SetUint64(q))
				}
			}
		}
		if minForm {
			// THE MINIMUM PAY RULE (the founder's direction of record, 15
			// October 2026: a key is paid on its weakest lane). The key's
			// balanced work is the minimum over the breadth lanes of its
			// floored lane work relative to the lane's share, x'_c x q /
			// p_c, in TL units (a key at its full share on every lane reads
			// TL, as under the geometric mean); the over-share cap above
			// stands (a lane the key is over on counts at its share once
			// another lane was lifted), and the cap beta <= 1 is the
			// minimum at most TL. THE SYBIL READ: the first cut's hole was
			// a key splitting into k keys each with 1/k of its work on every
			// lane, paid k x the degree-one share under a concave split;
			// the degree rule (each key's pay linear in its own balanced
			// work, the pot split in proportion) makes the k keys' pays sum
			// to the one key's, and the minimum is HOMOGENEOUS of degree one
			// in the key's work vector (min over c of (x_c / k) = (min over
			// c of x_c) / k), so splitting a key moves nothing; a split that
			// puts whole lanes on different keys is worse for the splitter
			// (a key with a lane at zero reads zero, F-124 (1)); and the
			// over-share cap keeps a key from lifting its minimum by the
			// floors of another's lane. The degree rule and the cap close
			// the hole under the minimum exactly as under the mean.
			root := new(big.Int)
			first := true
			for c := 0; c < n; c++ {
				if !inLane[c] || p[c] == 0 {
					continue
				}
				x := new(big.Int).Mul(floored[c], new(big.Int).SetUint64(q))
				x.Div(x, new(big.Int).SetUint64(p[c]))
				if first || x.Cmp(root) < 0 {
					root.Set(x)
					first = false
				}
			}
			if root.Cmp(TL) > 0 {
				root.Set(TL)
			}
			if root.BitLen() > maxBits {
				maxBits = root.BitLen()
			}
			keys = append(keys, keyed{key: a, work: w.pay, work256: w.floor, floored: floored, root: root, AL: AL, TL: TL, A256: A256, T256L: T256L, over: over, lifted: liftedByY})
			continue
		}
		prod := big.NewInt(1)
		for c := 0; c < n; c++ {
			prod.Mul(prod, new(big.Int).Exp(floored[c], new(big.Int).SetUint64(p[c]), nil))
		}
		// beta = min(1, product over c of (x'_c / s_c)^{s_c}): the split
		// over root_q(product of W'_c^{p_c}) is the rule's split over
		// T x beta up to the constant product of (q / p_c)^{p_c / q},
		// the same for every key, so only the CAP needs the constant:
		// beta <= 1 reads product of (W'_c q)^{p_c} <= T^q x product of
		// p_c^{p_c}, and a key over it takes T^q x product of p_c^{p_c}
		// / q^q in the split's units (T / that constant).
		capVal := new(big.Int).Exp(TL, new(big.Int).SetUint64(q), nil)
		qq := new(big.Int).Exp(new(big.Int).SetUint64(q), new(big.Int).SetUint64(q), nil)
		for c := 0; c < n; c++ {
			if p[c] > 0 {
				capVal.Mul(capVal, new(big.Int).Exp(new(big.Int).SetUint64(p[c]), new(big.Int).SetUint64(p[c]), nil))
			}
		}
		capVal.Div(capVal, qq)
		if prod.Cmp(capVal) > 0 {
			prod = capVal
		}
		root := integerRoot(prod, q)
		if root.BitLen() > maxBits {
			maxBits = root.BitLen()
		}
		keys = append(keys, keyed{key: a, work: w.pay, work256: w.floor, floored: floored, root: root, AL: AL, TL: TL, A256: A256, T256L: T256L, over: over, lifted: liftedByY})
	}
	shift := 0
	if maxBits > 52 {
		shift = maxBits - 52
	}
	e.balancedMu.Lock()
	e.lastV2.shift = uint64(shift)
	e.balancedMu.Unlock()
	out := make([]BalancedKey, 0, len(keys))
	for _, kk := range keys {
		wgt := new(big.Int).Rsh(kk.root, uint(shift))
		weight := wgt.Uint64()
		if weight == 0 {
			if v3 && kk.root.Sign() == 0 {
				// F-124 (1): a key with balanced work zero is paid nothing.
				continue
			}
			weight = 1
		}
		out = append(out, BalancedKey{Key: kk.key, Work: kk.work, Floored: kk.floored, Balanced: kk.root, Weight: weight, Work256: kk.work256, ScaledLane: kk.AL, ScaledTotal: kk.TL, Scaled256: kk.A256, Total256: kk.T256L, Over: kk.over, LiftedByY: kk.lifted, Eligible: true})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key.Hex() < out[j].Key.Hex() })
	return out
}

// sealerOf is a block's sealer KEY for the third cut (F-121, 14 October
// 2026): under a payout list the header's coinbase is the first twenty
// bytes of the withdrawals root, not an address anyone holds, and the
// sealer's payout address is the list's LAST entry (shares_engine.go
// VerifyShareList reads it there); without a list the coinbase. The
// template passes the same address (its etherbase, the list's last entry)
// to TemplateShares, so the two sides key the sealer's lane work alike;
// the first form keyed it on header.Coinbase at the verify and refused
// every block at vectorwork8's activation of the third cut (500).
func sealerOf(h *types.Header, withdrawals []*types.Withdrawal) common.Address {
	if n := len(withdrawals); n > 0 {
		return withdrawals[n-1].Address
	}
	return h.Coinbase
}
