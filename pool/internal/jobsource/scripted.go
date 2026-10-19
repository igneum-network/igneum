package jobsource

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"strings"
	"sync"
	"time"
)

// Scripted is a chain state from a schedule file instead of a sealer (the product team's VectorWork router soak,
// 13 October 2026: no node carries VectorWork yet, so the router's chain state comes from the pool): one
// template per coordinate with a synthetic header, the per-coordinate difficulty moved by the schedule (a
// coordinate doubled or halved at a minute, a coordinate emptied for a span), a new height every block clock,
// shares accepted and never forwarded. Never on the public pool.
//
// The schedule file (JSON):
//
//	{"chain_id": 7399, "block_seconds": 20, "share_target": "0x00000fff...", "shares_per_block": 160,
//	 "subsidy_wei": "2000000000000000000",
//	 "coordinates": [{"coord": 0, "algorithm": "igneumhash", "params": {...}, "difficulty": "1000000", "reward_wei": "500000000000000000"}, ...],
//	 "moves": [{"at_s": 300, "coord": 1, "difficulty_x": 2}, {"at_s": 600, "coord": 2, "difficulty_x": 0.5},
//	           {"at_s": 900, "coord": 3, "empty_s": 600}]}
//
// at_s counts from the start; difficulty_x multiplies the coordinate's difficulty (and divides its share
// target the same way, so a harder coordinate yields fewer shares); empty_s removes the coordinate's template
// for that span (GetWorkCoord answers an error, as a sealer with no template would).
type Scripted struct {
	mu      sync.Mutex
	start   time.Time
	now     func() time.Time
	chainID uint64
	block   float64
	target  *big.Int
	spb     uint64
	subsidy string
	fixed   bool
	coords  []scriptedCoord
	moves   []scriptedMove
	applied map[int]bool
	submits uint64
}

type scriptedCoord struct {
	Coord      uint8           `json:"coord"`
	Algorithm  string          `json:"algorithm"`
	Params     json.RawMessage `json:"params"`
	Difficulty string          `json:"difficulty"`
	RewardWei  string          `json:"reward_wei"`
	difficulty *big.Int
	scale      *big.Rat // the difficulty's multiplier so far (the share target divides by it)
	lastHeight uint64   // the coordinate's last block: the head before it, frozen through an empty span
}

type scriptedMove struct {
	AtS         float64 `json:"at_s"`
	Coord       uint8   `json:"coord"`
	DifficultyX float64 `json:"difficulty_x,omitempty"`
	EmptyS      float64 `json:"empty_s,omitempty"`
}

type scriptedFile struct {
	ChainID        uint64  `json:"chain_id"`
	BlockSeconds   float64 `json:"block_seconds"`
	ShareTarget    string  `json:"share_target"`
	SharesPerBlock uint64  `json:"shares_per_block"`
	SubsidyWei     string  `json:"subsidy_wei"`
	// ShareTargetFixed keeps the share target at share_target through every difficulty move (the product team's router
	// soak of 06:15 UK, 13 October 2026: a fixed 2^24 target so each card finds a share every few seconds and the
	// step rule stays at 0); the advertised difficulty still moves.
	ShareTargetFixed bool            `json:"share_target_fixed"`
	Coordinates      []scriptedCoord `json:"coordinates"`
	Moves            []scriptedMove  `json:"moves"`
}

// NewScripted reads the schedule and starts its clock now.
func NewScripted(path string, now func() time.Time) (*Scripted, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f scriptedFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("schedule %s: %w", path, err)
	}
	if f.BlockSeconds <= 0 || len(f.Coordinates) == 0 || f.ChainID == 0 {
		return nil, fmt.Errorf("schedule %s: chain_id, block_seconds and coordinates are required", path)
	}
	t, ok := new(big.Int).SetString(stripHex(f.ShareTarget), 16)
	if !ok || t.Sign() <= 0 {
		return nil, fmt.Errorf("schedule %s: share_target is 0x-hex", path)
	}
	if now == nil {
		now = time.Now
	}
	s := &Scripted{start: now(), now: now, chainID: f.ChainID, block: f.BlockSeconds, target: t, spb: f.SharesPerBlock, subsidy: f.SubsidyWei, fixed: f.ShareTargetFixed, coords: f.Coordinates, moves: f.Moves, applied: map[int]bool{}}
	if s.spb == 0 {
		s.spb = 160
	}
	for i := range s.coords {
		d, ok := new(big.Int).SetString(s.coords[i].Difficulty, 10)
		if !ok {
			return nil, fmt.Errorf("schedule %s: coordinate %d difficulty is decimal", path, s.coords[i].Coord)
		}
		s.coords[i].difficulty = d
		s.coords[i].scale = big.NewRat(1, 1)
		if s.coords[i].Algorithm == "" {
			s.coords[i].Algorithm = "igneumhash"
		}
	}
	return s, nil
}

func stripHex(s string) string {
	if len(s) > 2 && (s[:2] == "0x" || s[:2] == "0X") {
		return s[2:]
	}
	return s
}

// Height is the scripted height now: one per block clock from the start.
func (s *Scripted) Height() uint64 {
	return uint64(s.now().Sub(s.start).Seconds()/s.block) + 1
}

// applyMoves applies every move whose minute has come, once.
func (s *Scripted) applyMoves() {
	el := s.now().Sub(s.start).Seconds()
	for i, m := range s.moves {
		if s.applied[i] || el < m.AtS {
			continue
		}
		s.applied[i] = true
		for j := range s.coords {
			if s.coords[j].Coord != m.Coord {
				continue
			}
			if m.DifficultyX > 0 {
				x := new(big.Rat).SetFloat64(m.DifficultyX)
				s.coords[j].scale.Mul(s.coords[j].scale, x)
				d := new(big.Rat).SetInt(s.coords[j].difficulty)
				d.Mul(d, x)
				s.coords[j].difficulty = new(big.Int).Quo(d.Num(), d.Denom())
			}
		}
	}
}

// empty reports whether the coordinate is inside an empty span now.
func (s *Scripted) empty(c uint8) bool {
	el := s.now().Sub(s.start).Seconds()
	for _, m := range s.moves {
		if m.Coord == c && m.EmptyS > 0 && el >= m.AtS && el < m.AtS+m.EmptyS {
			return true
		}
	}
	return false
}

// hdrAt is the synthetic header of a coordinate at a height: a hash over the chain id, coordinate and height.
func (s *Scripted) hdrAt(coord uint8, h uint64) [32]byte {
	var b [8 + 1 + 8]byte
	binary.BigEndian.PutUint64(b[:8], s.chainID)
	b[8] = coord
	binary.BigEndian.PutUint64(b[9:], h)
	return sha256.Sum256(b[:])
}

func (s *Scripted) work(c *scriptedCoord, height uint64) Work {
	// the synthetic header and parent: a hash over the chain id, coordinate and height (and the parent's over
	// height-1), so every height's header is new and a share names its height's
	hdr := func(h uint64) [32]byte {
		return s.hdrAt(c.Coord, h)
	}
	// the share target divides by the coordinate's difficulty scale
	t := new(big.Rat).SetInt(s.target)
	if !s.fixed {
		t.Quo(t, c.scale)
	}
	ti := new(big.Int).Quo(t.Num(), t.Denom())
	if ti.Sign() <= 0 {
		ti = big.NewInt(1)
	}
	w := Work{
		Header: hdr(height), Height: height, Epoch: 0, Algorithm: c.Algorithm, AlgorithmVersion: 1,
		Params: c.Params, ParamsDigest: fmt.Sprintf("0x%x", sha256.Sum256(c.Params)),
		ShareTarget: json.RawMessage(fmt.Sprintf("\"0x%x\"", ti)), SharesPerBlock: s.spb, ShareHeight: height, ShareParent: hdr(height - 1),
		ShareParams: c.Params, ShareParamsDigest: fmt.Sprintf("0x%x", sha256.Sum256(c.Params)), ShareOnly: true,
		Coord: c.Coord, HasCoord: true, Difficulty: c.difficulty.String(), RewardWei: c.RewardWei, LastHeight: c.lastHeight,
	}
	var bt [32]byte
	tb := ti.Bytes()
	copy(bt[32-len(tb):], tb)
	w.Boundary = bt
	return w
}

// GetWork is the first coordinate's template (a one-form caller).
func (s *Scripted) GetWork(ctx context.Context) (Work, error) {
	return s.GetWorkCoord(ctx, s.coords[0].Coord)
}

// GetWorkCoord is the coordinate's template now, or an error inside its empty span.
func (s *Scripted) GetWorkCoord(ctx context.Context, coord uint8) (Work, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.applyMoves()
	for i := range s.coords {
		if s.coords[i].Coord != coord {
			continue
		}
		if s.empty(coord) {
			// the coordinate's last block stays where it was when the span began (the router's restore signal)
			return Work{}, fmt.Errorf("scripted: coordinate %d has no template in this span", coord)
		}
		h := s.Height()
		if h > 0 {
			s.coords[i].lastHeight = h - 1
		}
		return s.work(&s.coords[i], h), nil
	}
	return Work{}, fmt.Errorf("scripted: no coordinate %d", coord)
}

// SubmitWork accepts a block solution (nothing to seal).
func (s *Scripted) SubmitWork(ctx context.Context, nonce uint64, header, mix [32]byte) error {
	return nil
}

// Call answers the pool's RPCs from the script: the chain id, the share limits and stats, a share submit
// (accepted, counted), an empty share account, a block number, and nothing else.
func (s *Scripted) Call(ctx context.Context, method string, params interface{}, out interface{}) error {
	var res interface{}
	switch method {
	case "eth_chainId":
		res = fmt.Sprintf("0x%x", s.chainID)
	case "eth_blockNumber":
		res = fmt.Sprintf("0x%x", s.Height())
	case "eth_getBlockByNumber":
		// the block's number and hash (the synthetic header of coordinate 0 at that height, as work() builds it), for
		// the pool's best-head read of its own branch (0.1.64); "latest" is the scripted height now
		var n uint64
		if p, ok := params.([]interface{}); ok && len(p) > 0 {
			if tag, ok := p[0].(string); ok {
				if tag == "latest" {
					n = s.Height()
				} else {
					fmt.Sscanf(strings.TrimPrefix(tag, "0x"), "%x", &n) //nolint:errcheck
				}
			}
		}
		if n > s.Height() {
			res = nil
		} else {
			h := s.hdrAt(0, n)
			res = map[string]interface{}{"number": fmt.Sprintf("0x%x", n), "hash": "0x" + hex.EncodeToString(h[:])}
		}
	case "igneum_shareLimits":
		res = map[string]interface{}{"shareCapPerAddress": 256, "poolCapacity": 16384, "maxShares": 256, "recency": 64}
	case "igneum_shareStats":
		s.mu.Lock()
		res = map[string]interface{}{"pooled": s.submits}
		s.mu.Unlock()
	case "igneum_submitShare":
		s.mu.Lock()
		s.submits++
		s.mu.Unlock()
		res = map[string]interface{}{"accepted": true, "reason": ""}
	case "igneum_shareAccount":
		res = map[string]interface{}{"carriedByHeight": map[string]uint64{}, "expiredReorgByHeight": map[string]uint64{}, "expiredWindowByHeight": map[string]uint64{}}
	case "eth_getBalance":
		res = "0x0"
	default:
		return fmt.Errorf("scripted: the method %s is not scripted", method)
	}
	b, _ := json.Marshal(res)
	return json.Unmarshal(b, out)
}

// Summary is the script's state for /shares (the difficulty per coordinate now, the moves applied).
func (s *Scripted) Summary() map[string]interface{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.applyMoves()
	cs := []map[string]interface{}{}
	for _, c := range s.coords {
		cs = append(cs, map[string]interface{}{"coord": c.Coord, "algorithm": c.Algorithm, "difficulty": c.difficulty.String(), "reward_wei": c.RewardWei, "empty": s.empty(c.Coord), "last_height": c.lastHeight})
	}
	return map[string]interface{}{"scripted": true, "chain_id": s.chainID, "height": s.Height(), "since": s.start.UTC().Format(time.RFC3339), "coordinates": cs, "moves_applied": len(s.applied), "moves": len(s.moves)}
}
