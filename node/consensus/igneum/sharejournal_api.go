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
	"encoding/binary"
	"errors"
	"os"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// ShareJournalRecord is one record of this node's share journal as
// igneum_shareJournal returns it (the settlement team's ask of 00:2x UK,
// 12 October 2026, for the ledger's per-share placement): the kind
// ("accepted", "carried", "expired"), the share's identity (address,
// extraNonce, nonce; the height it was mined for where the record carries
// it: every accepted and expired record, and the carried record in its
// second form; the first carried form, written before the height was
// part of the identity, reads height 0), the acceptance's parent, mix and
// step, and the expiry's reason ("window": the recency passed with no
// carriage; "reorg": the share's parent lost a reorganisation). A carried
// record names the SHARE's height, not the carrying block's (the journal
// never held the block); the chain's canonical entries at that height are
// the settlement, this journal the node's view beside it.
type ShareJournalRecord struct {
	Kind       string         `json:"kind"`
	Address    common.Address `json:"address"`
	ExtraNonce uint64         `json:"extraNonce"`
	Nonce      uint64         `json:"nonce"`
	Height     uint64         `json:"height"`
	Parent     *common.Hash   `json:"parent,omitempty"`
	MixDigest  *common.Hash   `json:"mixDigest,omitempty"`
	Step       uint8          `json:"step,omitempty"`
	Reason     string         `json:"reason,omitempty"`
	Offset     int64          `json:"offset"` // the record's byte offset in the journal file (the file's order)
}

var errNoShareJournal = errors.New("igneum shares: this node keeps no share journal")

// journalRecords parses a journal's bytes into records in file order (a
// torn tail ends the list; an unknown type ends it, as the replay does).
func journalRecords(data []byte) []ShareJournalRecord {
	var out []ShareJournalRecord
	off := int64(0)
	for len(data) > 0 {
		var r ShareJournalRecord
		var n int
		switch data[0] {
		case shareJournalAccepted, shareJournalAcceptedStep, shareJournalAcceptedGraph:
			n = 1 + shareJournalAcceptedBytes
			if data[0] == shareJournalAcceptedStep || data[0] == shareJournalAcceptedGraph {
				n++
			}
			if data[0] == shareJournalAcceptedGraph {
				n += types.GraphTicketBytes
			}
			if len(data) < n {
				return out
			}
			b := data[1:n]
			r.Kind = "accepted"
			r.Height = binary.LittleEndian.Uint64(b[0:])
			p := common.BytesToHash(b[8:40])
			r.Parent = &p
			copy(r.Address[:], b[40:60])
			r.ExtraNonce = binary.LittleEndian.Uint64(b[60:])
			r.Nonce = binary.LittleEndian.Uint64(b[68:])
			m := common.BytesToHash(b[76:108])
			r.MixDigest = &m
			if data[0] == shareJournalAcceptedStep {
				r.Step = b[108]
			}
		case shareJournalCarried:
			n = 1 + shareJournalCarriedBytes
			if len(data) < n {
				return out
			}
			b := data[1:n]
			r.Kind = "carried"
			copy(r.Address[:], b[0:20])
			r.ExtraNonce = binary.LittleEndian.Uint64(b[20:])
			r.Nonce = binary.LittleEndian.Uint64(b[28:])
		case shareJournalCarriedAt, shareJournalUncarried, shareJournalCarriedP, shareJournalUncarriedP:
			withParent := data[0] == shareJournalCarriedP || data[0] == shareJournalUncarriedP
			n = 1 + shareJournalCarriedAtBytes
			if withParent {
				n = 1 + shareJournalCarriedPBytes
			}
			if len(data) < n {
				return out
			}
			b := data[1:n]
			r.Kind = "carried"
			if data[0] == shareJournalUncarried || data[0] == shareJournalUncarriedP {
				// the carriage undone: the carrying block lost its height
				r.Kind = "uncarried"
			}
			copy(r.Address[:], b[0:20])
			r.ExtraNonce = binary.LittleEndian.Uint64(b[20:])
			r.Nonce = binary.LittleEndian.Uint64(b[28:])
			r.Height = binary.LittleEndian.Uint64(b[36:])
			if withParent {
				// the share's parent (type 7 and 8): the carriage keyed with it
				p := common.BytesToHash(b[44:76])
				r.Parent = &p
			}
		case shareJournalExpired:
			n = 1 + shareJournalExpiredBytes
			if len(data) < n {
				return out
			}
			b := data[1:n]
			r.Kind = "expired"
			switch shareExpiry(b[0]) {
			case expiryReorg:
				r.Reason = "reorg"
			default:
				r.Reason = "window"
			}
			r.Height = binary.LittleEndian.Uint64(b[1:])
			copy(r.Address[:], b[9:29])
			r.ExtraNonce = binary.LittleEndian.Uint64(b[29:])
			r.Nonce = binary.LittleEndian.Uint64(b[37:])
		default:
			return out
		}
		r.Offset = off
		out = append(out, r)
		data = data[n:]
		off += int64(n)
	}
	return out
}

// ShareJournal reads this node's journal file and returns the records of
// an address (the zero address: every address) whose share height is in
// [from, to] (to 0: no upper bound; a first-form carried record, height
// 0, is returned only when from is 0), in file order. Read only; the
// file is read whole under the journal's lock, so a write in flight is
// seen or not seen whole.
func (e *Engine) ShareJournal(address common.Address, from, to uint64) ([]ShareJournalRecord, error) {
	j := &e.shareJournal
	j.mu.Lock()
	path := j.path
	j.mu.Unlock()
	if path == "" {
		return nil, errNoShareJournal
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	all := journalRecords(data)
	out := make([]ShareJournalRecord, 0, len(all))
	for _, r := range all {
		if address != (common.Address{}) && r.Address != address {
			continue
		}
		if r.Height < from || (to > 0 && r.Height > to) {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}
