package types

import (
	"encoding/binary"

	"github.com/ethereum/go-ethereum/common"
	"lukechampine.com/blake3"
)

// The share lane's carried shares (LAUNCH-GUARDS.md 2.9; consensus/igneum/
// mineshares.go): a block carries the accepted shares its payout list is
// built from, as fixed-size entries committed by the header's SharesRoot
// (inside the seal hash). One entry: the finder's payout address (20) ||
// LE64(extraNonce) || LE64(nonce) || mixDigest (32) || u8(parent index k),
// 69 bytes; k = 0 names the carrying block's parent as the share's parent,
// k up to 7 the ancestors before it (design G's recency 8).

// MinedShareBytes is one carried share's size.
const MinedShareBytes = 20 + 8 + 8 + 32 + 1

// MinedSharesRoot is the commitment to a block's carried shares:
// BLAKE3(tag("IgneumHash/R0A/shares") || entries), tag(T) = u8(len(T)) || T.
func MinedSharesRoot(entries []byte) common.Hash {
	const t = "IgneumHash/R0A/shares"
	h := blake3.New(32, nil)
	h.Write([]byte{byte(len(t))})
	h.Write([]byte(t))
	h.Write(entries)
	var out common.Hash
	h.Sum(out[:0])
	return out
}

// MinedShareCount is the number of whole entries in a list (-1 when the
// length is not a multiple of the entry size: malformed).
func MinedShareCount(entries []byte) int {
	if len(entries)%MinedShareBytes != 0 {
		return -1
	}
	return len(entries) / MinedShareBytes
}

// MinedShare is one decoded entry.
type MinedShare struct {
	Address    common.Address
	ExtraNonce uint64
	Nonce      uint64
	MixDigest  common.Hash
	ParentIdx  uint8
	// Step is PA-53's declared target step: the share was mined at the
	// chain's share target over 2^Step and weighs 2^Step (0 under the
	// count form, where the entry does not carry it).
	Step uint8
	// Ticket is a GRAPH share's 42-cycle witness (WHOLEWORK.md section 10
	// (b), 14 October 2026): 168 bytes under the typed list form, nil for
	// a hash share. MixDigest holds the ticket hash.
	Ticket []byte
}

// GraphTicketBytes is a graph share's ticket size (42 edges of 4 bytes).
const GraphTicketBytes = 168

// The typed list form (section 10 (b), behind the registration's
// graphSharesFrom): every entry is a kind byte then the entry's bytes under
// the step form; kind 0 a hash share (70 bytes follow), kind 1 a graph
// share (70 bytes then the 168-byte ticket).
const (
	MinedShareKindHash  = 0
	MinedShareKindGraph = 1
)

// BytesTyped is the entry's wire form under the typed list form.
func (s *MinedShare) BytesTyped() []byte {
	body := s.BytesOf(true)
	if len(s.Ticket) == GraphTicketBytes {
		out := make([]byte, 0, 1+len(body)+GraphTicketBytes)
		out = append(out, MinedShareKindGraph)
		out = append(out, body...)
		return append(out, s.Ticket...)
	}
	out := make([]byte, 0, 1+len(body))
	out = append(out, MinedShareKindHash)
	return append(out, body...)
}

// ParseMinedSharesTyped decodes a typed list (nil, false on a malformed
// kind or length).
func ParseMinedSharesTyped(entries []byte) ([]MinedShare, bool) {
	var out []MinedShare
	w := MinedShareStepBytes
	for len(entries) > 0 {
		kind := entries[0]
		entries = entries[1:]
		if len(entries) < w {
			return nil, false
		}
		list, ok := ParseMinedSharesOf(entries[:w], true)
		if !ok || len(list) != 1 {
			return nil, false
		}
		e := list[0]
		entries = entries[w:]
		switch kind {
		case MinedShareKindHash:
		case MinedShareKindGraph:
			if len(entries) < GraphTicketBytes {
				return nil, false
			}
			e.Ticket = append([]byte(nil), entries[:GraphTicketBytes]...)
			entries = entries[GraphTicketBytes:]
		default:
			return nil, false
		}
		out = append(out, e)
	}
	return out, true
}

// MinedShareCountTyped is the entry count of a typed list, -1 malformed.
func MinedShareCountTyped(entries []byte) int {
	list, ok := ParseMinedSharesTyped(entries)
	if !ok {
		return -1
	}
	return len(list)
}

// ShareListForm names a chain's carried-list form at a height: typed (the
// kind byte, section 10 (b)), else the step or count form.
type ShareListForm struct{ Typed, Step bool }

// ParseMinedSharesForm decodes a list under a form.
func ParseMinedSharesForm(entries []byte, f ShareListForm) ([]MinedShare, bool) {
	if f.Typed {
		return ParseMinedSharesTyped(entries)
	}
	return ParseMinedSharesOf(entries, f.Step)
}

// BytesForm is the entry's wire form under a form.
func (s *MinedShare) BytesForm(f ShareListForm) []byte {
	if f.Typed {
		return s.BytesTyped()
	}
	return s.BytesOf(f.Step)
}

// MinedShareCountForm is the entry count under a form, -1 malformed.
func MinedShareCountForm(entries []byte, f ShareListForm) int {
	if f.Typed {
		return MinedShareCountTyped(entries)
	}
	return MinedShareCountOf(entries, f.Step)
}

// Bytes is the entry's wire form.
func (s *MinedShare) Bytes() []byte {
	out := make([]byte, 0, MinedShareBytes)
	out = append(out, s.Address[:]...)
	var b8 [8]byte
	binary.LittleEndian.PutUint64(b8[:], s.ExtraNonce)
	out = append(out, b8[:]...)
	binary.LittleEndian.PutUint64(b8[:], s.Nonce)
	out = append(out, b8[:]...)
	out = append(out, s.MixDigest[:]...)
	return append(out, s.ParentIdx)
}

// MinedShareStepBytes is one carried share's size under PA-53's step form:
// the count form's entry plus the declared target step, one byte.
const MinedShareStepBytes = MinedShareBytes + 1

// MinedShareWidth is an entry's size under a form: the step form's when
// step is true (PA-53), else the count form's.
func MinedShareWidth(step bool) int {
	if step {
		return MinedShareStepBytes
	}
	return MinedShareBytes
}

// MinedShareCountOf is MinedShareCount under a form.
func MinedShareCountOf(entries []byte, step bool) int {
	w := MinedShareWidth(step)
	if len(entries)%w != 0 {
		return -1
	}
	return len(entries) / w
}

// BytesOf is the entry's wire form under a form: the count form's bytes,
// then the step under the step form.
func (s *MinedShare) BytesOf(step bool) []byte {
	out := s.Bytes()
	if step {
		out = append(out, s.Step)
	}
	return out
}

// ParseMinedShares decodes a concatenated list (nil, false on a malformed
// length) under the count form.
func ParseMinedShares(entries []byte) ([]MinedShare, bool) {
	return ParseMinedSharesOf(entries, false)
}

// ParseMinedSharesOf decodes a concatenated list under a form (the step
// form's entries carry the step last; the count form's read step 0).
func ParseMinedSharesOf(entries []byte, step bool) ([]MinedShare, bool) {
	n := MinedShareCountOf(entries, step)
	if n < 0 {
		return nil, false
	}
	w := MinedShareWidth(step)
	out := make([]MinedShare, n)
	for i := 0; i < n; i++ {
		e := entries[i*w : (i+1)*w]
		copy(out[i].Address[:], e[:20])
		out[i].ExtraNonce = binary.LittleEndian.Uint64(e[20:28])
		out[i].Nonce = binary.LittleEndian.Uint64(e[28:36])
		copy(out[i].MixDigest[:], e[36:68])
		out[i].ParentIdx = e[68]
		if step {
			out[i].Step = e[69]
		}
	}
	return out, true
}
