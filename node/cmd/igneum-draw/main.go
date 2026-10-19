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

// devnet-draw replays the genesis form's round-0 draw per height from an
// archive node's state (the design seat's two counts for C19's cause on
// devnet7, 11 October 2026; GUARD-C-CLOSURE.md 4.4): for each height h in
// [from, to] it reads the registry's key list and each key's units in the
// epoch's table at the parent's state (the live table for epochs 0 and 1,
// the written table otherwise, by the slot forms of sortition_genesis.go),
// the epoch's seed, and whether each key's least lot over its units is
// under Theta(E, 0): round-0 eligible. Against the record's sealed round
// per height (the rows JSON) it prints (a) the round-0 OVERRUN: heights
// where a key with a LIVE sealer was round-0 eligible and the block sealed
// at round 1 or later, and (b) the DARK heights: heights where only keys
// with no sealer behind them were round-0 eligible, so the height passed
// to round 1 or later by construction; and the expectation over the live
// keys' units. Reads RPC only.
//
//	go run ./tools/authority/devnet-draw <rpc> <rows.json> <from> <to> <epochBlocks> <expected> <liveKey>...
package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"net/http"
	"os"
	"strconv"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/igneum"
	"github.com/ethereum/go-ethereum/core/vm/igneumreg"
	"github.com/ethereum/go-ethereum/crypto"
	"lukechampine.com/blake3"
)

const registry = "0x0000000000000000000000000000000000000b0d"

var (
	tableDomain = []byte("IgneumHash/R0A/table")
	unitsDomain = []byte("IgneumHash/R0A/units")
	seedDomain  = []byte("IgneumHash/R0A/sortition")
)

func rpc(url, method string, params []interface{}) (json.RawMessage, error) {
	body, _ := json.Marshal(map[string]interface{}{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	resp, err := http.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if out.Error != nil {
		return nil, fmt.Errorf("%s: %s", method, out.Error.Message)
	}
	return out.Result, nil
}

func storage(url string, slot common.Hash, block uint64) (common.Hash, error) {
	r, err := rpc(url, "eth_getStorageAt", []interface{}{registry, slot.Hex(), "0x" + strconv.FormatUint(block, 16)})
	if err != nil {
		return common.Hash{}, err
	}
	var s string
	json.Unmarshal(r, &s)
	return common.HexToHash(s), nil
}

func balance(url string, block uint64) (*big.Int, error) {
	r, err := rpc(url, "eth_getBalance", []interface{}{registry, "0x" + strconv.FormatUint(block, 16)})
	if err != nil {
		return nil, err
	}
	var s string
	json.Unmarshal(r, &s)
	return new(big.Int).SetBytes(common.FromHex(s)), nil
}

func tableSlot(epoch uint64, i int64) common.Hash {
	var le [8]byte
	binary.LittleEndian.PutUint64(le[:], epoch)
	b := crypto.Keccak256Hash(tableDomain, le[:]).Big()
	return common.BigToHash(b.Add(b, big.NewInt(i)))
}

func unitsSlot(epoch uint64, key common.Hash) common.Hash {
	var le [8]byte
	binary.LittleEndian.PutUint64(le[:], epoch)
	return crypto.Keccak256Hash(unitsDomain, le[:], key[:])
}

func genesisSeed(epoch uint64) common.Hash {
	hs := blake3.New(32, nil)
	hs.Write(seedDomain)
	var le [8]byte
	binary.LittleEndian.PutUint64(le[:], epoch)
	hs.Write(le[:])
	var out common.Hash
	hs.Sum(out[:0])
	return out
}

func unitsOf(bond, u *big.Int) uint64 {
	if u.Sign() == 0 || bond.Sign() <= 0 {
		return 0
	}
	n := new(big.Int).Div(bond, u)
	if !n.IsUint64() || n.Uint64() > 16384 {
		return 16384
	}
	return n.Uint64()
}

type keyUnits struct {
	key   common.Hash
	units uint64
}

// tableAt reads the epoch's table at the parent's state: the keys with
// their units and the seed.
func tableAt(url string, epoch, epochBlocks, parent uint64) ([]keyUnits, common.Hash, error) {
	count, err := storage(url, igneumreg.SlotCount, parent)
	if err != nil {
		return nil, common.Hash{}, err
	}
	n := count.Big().Uint64()
	var keys []common.Hash
	for i := uint64(0); i < n; i++ {
		k, err := storage(url, igneumreg.KeySlot(i), parent)
		if err != nil {
			return nil, common.Hash{}, err
		}
		keys = append(keys, k)
	}
	written, err := storage(url, tableSlot(epoch, 3), parent)
	if err != nil {
		return nil, common.Hash{}, err
	}
	var out []keyUnits
	if written == (common.Hash{}) {
		// The live table (epochs 0 and 1): units priced off the state now.
		floor, _ := storage(url, igneumreg.SlotFloor, parent)
		total, _ := balance(url, parent)
		m := total
		if floor.Big().Cmp(m) > 0 {
			m = floor.Big()
		}
		u := new(big.Int).Rsh(m, 16)
		for _, k := range keys {
			amt, _ := storage(url, igneumreg.AmountSlot(k), parent)
			out = append(out, keyUnits{k, unitsOf(amt.Big(), u)})
		}
		return out, genesisSeed(epoch), nil
	}
	seed, _ := storage(url, tableSlot(epoch, 0), parent)
	for _, k := range keys {
		n, _ := storage(url, unitsSlot(epoch, k), parent)
		out = append(out, keyUnits{k, n.Big().Uint64()})
	}
	return out, seed, nil
}

func minLot(seed, key common.Hash, units, height uint64) *big.Int {
	var best *big.Int
	for j := uint64(0); j < units; j++ {
		x := igneum.Lot(seed, key, j, height)
		xi := new(big.Int).SetBytes(x[:])
		if best == nil || xi.Cmp(best) < 0 {
			best = xi
		}
	}
	return best
}

func main() {
	if len(os.Args) < 8 {
		fmt.Fprintln(os.Stderr, "usage: devnet-draw <rpc> <rows.json> <from> <to> <epochBlocks> <expected> <liveKey>...")
		os.Exit(2)
	}
	url := os.Args[1]
	from, _ := strconv.ParseUint(os.Args[3], 10, 64)
	to, _ := strconv.ParseUint(os.Args[4], 10, 64)
	epochBlocks, _ := strconv.ParseUint(os.Args[5], 10, 64)
	expected, _ := strconv.ParseUint(os.Args[6], 10, 64)
	live := map[common.Hash]bool{}
	for _, k := range os.Args[7:] {
		live[common.HexToHash(k)] = true
	}
	var rows struct {
		Blocks []struct {
			H     uint64 `json:"h"`
			Round uint64 `json:"round"`
			Form  string `json:"form"`
		} `json:"blocks"`
	}
	f, err := os.ReadFile(os.Args[2])
	if err != nil {
		panic(err)
	}
	json.Unmarshal(f, &rows)
	sealed := map[uint64]uint64{}
	form := map[uint64]string{}
	for _, b := range rows.Blocks {
		sealed[b.H] = b.Round
		form[b.H] = b.Form
	}
	theta0 := igneum.Theta(expected, 0)
	var overrun, dark, liveEligible, anyEligible, heights int
	var liveUnits, darkUnits uint64
	var cache struct {
		epoch uint64
		set   bool
		keys  []keyUnits
		seed  common.Hash
	}
	for h := from; h <= to; h++ {
		if _, ok := sealed[h]; !ok {
			continue
		}
		heights++
		epoch := (h - 1) / epochBlocks
		if !cache.set || cache.epoch != epoch {
			keys, seed, err := tableAt(url, epoch, epochBlocks, h-1)
			if err != nil {
				panic(err)
			}
			cache = struct {
				epoch uint64
				set   bool
				keys  []keyUnits
				seed  common.Hash
			}{epoch, true, keys, seed}
			liveUnits, darkUnits = 0, 0
			for _, k := range keys {
				if live[k.key] {
					liveUnits += k.units
				} else {
					darkUnits += k.units
				}
			}
			fmt.Printf("epoch %d (from height %d): %d keys, live units %d, dark units %d, seed %s\n", epoch, h, len(keys), liveUnits, darkUnits, cache.seed.Hex()[:18])
		}
		liveHit, darkHit := false, false
		for _, k := range cache.keys {
			if k.units == 0 {
				continue
			}
			if minLot(cache.seed, k.key, k.units, h).Cmp(theta0) < 0 {
				if live[k.key] {
					liveHit = true
				} else {
					darkHit = true
				}
			}
		}
		if liveHit || darkHit {
			anyEligible++
		}
		if liveHit {
			liveEligible++
			if sealed[h] > 0 {
				overrun++
			}
		} else if darkHit {
			dark++
		}
	}
	pLive := 1 - math.Exp(-float64(expected)*float64(liveUnits)/65536)
	fmt.Printf("heights read %d; round-0 eligible: any key %d, a live key %d\n", heights, anyEligible, liveEligible)
	fmt.Printf("(a) round-0 OVERRUN (a live key eligible at round 0, sealed at round 1 or later): %d of %d (%.1f percent of eligible)\n", overrun, liveEligible, 100*float64(overrun)/math.Max(1, float64(liveEligible)))
	fmt.Printf("(b) DARK heights (only keys with no sealer eligible at round 0): %d of %d (%.1f percent)\n", dark, heights, 100*float64(dark)/math.Max(1, float64(heights)))
	fmt.Printf("expectation over the live keys' units (%d): 1 - exp(-E x units / 65536) = %.4f; live-eligible fraction read %.4f\n", liveUnits, pLive, float64(liveEligible)/math.Max(1, float64(heights)))
	var r0 int
	for h := from; h <= to; h++ {
		if r, ok := sealed[h]; ok && r == 0 {
			r0++
		}
	}
	fmt.Printf("sealed at round 0: %d of %d = %.4f\n", r0, heights, float64(r0)/math.Max(1, float64(heights)))
}
